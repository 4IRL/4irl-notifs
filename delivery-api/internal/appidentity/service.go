package appidentity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
)

const (
	// ExchangeIssued is the outcome of an exchange that issued the app a key.
	ExchangeIssued = "issued"
	// ExchangePending is the outcome of an exchange that filed (or refreshed) a request for the operator.
	ExchangePending = "pending"

	// Outcome labels of the name-form exchange log line (failures use the same labels as the
	// normal form plus outcomeRegistrarDenied and outcomeRegistrarCap).
	outcomeRegistrarIssued  = "registrar_issued"
	outcomeRegistrarCreated = "registrar_created"
	outcomeRegistrarPending = "registrar_pending"
	outcomeRegistrarDenied  = "registrar_denied"
	outcomeRegistrarCap     = "registrar_cap"

	// maxLoggedNameChars bounds a caller-supplied registrar app name in a log line.
	maxLoggedNameChars = 64
	// MaxAcknowledgeApps is the most app ids one acknowledge-all call may list.
	MaxAcknowledgeApps = 200

	// maxLoggedReasonChars bounds an attacker-influenced rejection reason in a log line.
	maxLoggedReasonChars = 300

	// reservedAppID is the app id the people directory reserves for itself and refuses to register.
	reservedAppID = "everyone"
	// registerTimeout bounds one people-directory registration during an approval. It is a backstop
	// over the registrar client's own (shorter) timeout, so with the default client it is not the
	// binding limit.
	registerTimeout = 10 * time.Second
)

var (
	// ErrBindingMismatch means the app has an approved binding and the token's workflow
	// identity differs from it in at least one of the five bound fields. Nothing is filed.
	ErrBindingMismatch = errors.New("identity does not match the approved binding")
)

// ValidationError is a client-safe description of a malformed request.
type ValidationError struct {
	Message string
}

// Error implements error.
func (validationErr *ValidationError) Error() string {
	return validationErr.Message
}

// ValidateAppID checks an app_id against the slug rule every app-scoped table enforces.
func ValidateAppID(appID string) error {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return &ValidationError{Message: validateErr.Error()}
	}
	return nil
}

// validateExchangeAppID rejects the people directory's reserved app id at the exchange only, so
// no request that could never be approved is filed. ValidateAppID stays the plain slug rule
// (Unbind uses it), because a binding for the reserved id may predate this check and must
// remain removable.
func validateExchangeAppID(appID string) error {
	if appID == reservedAppID {
		return &ValidationError{Message: "invalid app_id"}
	}
	return nil
}

// TokenVerifier checks a GitHub Actions OIDC token. *Verifier satisfies it.
type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string, replay ReplayRecorder) (Claims, error)
}

// ExchangeInput is one exchange request: exactly one of AppID (a normal app) or Name (a
// registrar's bare app name), plus the GitHub OIDC token.
type ExchangeInput struct {
	AppID string
	Name  string
	Token string
}

// ExchangeResult is the successful outcome of an exchange.
type ExchangeResult struct {
	// Status is ExchangeIssued or ExchangePending.
	Status string
	// Created is true when this exchange created the app (name form, first sight of the name).
	Created bool
	// AppID, Key and KeyID are set for ExchangeIssued. Key is the plaintext, shown once.
	AppID string
	Key   string
	KeyID int64
	// RequestID is set for ExchangePending.
	RequestID int64
}

// AppRegistrar registers an app in the people directory. *personsvc.Client satisfies it. A
// CreateApp for an app that already exists succeeds.
type AppRegistrar interface {
	CreateApp(ctx context.Context, appID string, displayName string) error
}

// ServiceConfig configures a Service. Store, Admin and Verifier are required.
type ServiceConfig struct {
	Store    Store
	Admin    AdminStore
	Verifier TokenVerifier
	// Registrar registers an approved app in the people directory. Nil makes ApproveRequest
	// answer ErrAppRegistryNotConfigured (fail closed).
	Registrar AppRegistrar
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Now is the clock; defaults to time.Now. Tests set it.
	Now func() time.Time
}

// Service is the app identity use cases: the token exchange an app's deploy calls, and the
// operator's approval queue, key revocation and unbinding.
type Service struct {
	store     Store
	admin     AdminStore
	verifier  TokenVerifier
	registrar AppRegistrar
	logger    *slog.Logger
	now       func() time.Time
}

// NewService builds a Service from config.
func NewService(config ServiceConfig) *Service {
	service := &Service{
		store:     config.Store,
		admin:     config.Admin,
		verifier:  config.Verifier,
		registrar: config.Registrar,
		logger:    config.Logger,
		now:       config.Now,
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.now == nil {
		service.now = time.Now
	}
	return service
}

// verifyOnly is the ReplayRecorder for the verification step of an exchange: it records
// nothing. The jti is recorded afterwards, inside the exchange transaction, so no database
// transaction is held open across the verifier's JWKS fetch, and so the jti, the key issue
// and the request filing commit or roll back as one.
type verifyOnly struct{}

// RecordJTI implements ReplayRecorder.
func (verifyOnly) RecordJTI(context.Context, string, time.Time) (bool, error) {
	return true, nil
}

// Exchange trades a GitHub OIDC token for the app's identity. It returns:
//   - *ValidationError for a malformed app_id or a missing token.
//   - an error wrapping ErrInvalidToken for any token check failure, including a replayed
//     jti (the specific reason is in the log only).
//   - ErrKeysUnavailable when GitHub's signing keys could not be fetched.
//   - ExchangePending (and a filed or refreshed pending request) when the app has no binding.
//   - ErrPendingCap when that request would exceed MaxPendingRequests.
//   - ErrBindingMismatch when the app is bound to a different workflow identity.
//   - ExchangeIssued with a fresh key when the claims match the binding exactly; the app's
//     previous keys are superseded in the same transaction.
//
// One log line records every exchange, with its outcome and duration; never the token or key.
//
// The name form (input.Name, a registrar's bare app name) additionally returns ErrInvalidName,
// ErrRegistrarOwnership, ErrRegistrarAppLimit, ErrAppIDOverlap, ErrAppRegistryNotConfigured,
// ErrAppRegistryUnavailable and ErrBindingAppeared (a retryable race); see exchangeAsRegistrar.
// Input naming both or neither of AppID and Name returns ErrExchangeTarget.
func (service *Service) Exchange(ctx context.Context, input ExchangeInput) (ExchangeResult, error) {
	started := time.Now()
	appID, name := input.AppID, input.Name
	if (appID == "") == (name == "") {
		return ExchangeResult{}, ErrExchangeTarget
	}
	if name != "" {
		if nameErr := ValidateName(name); nameErr != nil {
			return ExchangeResult{}, nameErr
		}
	} else {
		if validateErr := ValidateAppID(appID); validateErr != nil {
			return ExchangeResult{}, validateErr
		}
		if reservedErr := validateExchangeAppID(appID); reservedErr != nil {
			return ExchangeResult{}, reservedErr
		}
	}
	if input.Token == "" {
		return ExchangeResult{}, &ValidationError{Message: "token is required"}
	}

	entry := exchangeLogEntry{appID: appID, name: name}
	claims, verifyErr := service.verifier.Verify(ctx, input.Token, verifyOnly{})
	if verifyErr != nil {
		service.logRejectedToken(ctx, entry, verifyErr, time.Since(started))
		return ExchangeResult{}, verifyErr
	}
	if name != "" {
		return service.exchangeAsRegistrar(ctx, entry, claims, started)
	}

	var result ExchangeResult
	txErr := service.store.Exchange(ctx, func(tx ExchangeTx) error {
		fresh, recordErr := tx.RecordJTI(ctx, claims.JTI, claims.ReplayExpiry())
		if recordErr != nil {
			return recordErr
		}
		if !fresh {
			return ErrReplayed
		}
		exchanged, exchangeErr := service.exchangeClaims(ctx, tx, appID, claims)
		result = exchanged
		return exchangeErr
	})
	if txErr != nil {
		service.logFailedExchange(ctx, entry, claims, txErr, time.Since(started))
		return ExchangeResult{}, txErr
	}
	service.logExchange(ctx, entry, claims, result.Status, time.Since(started))
	return result, nil
}

// exchangeClaims decides what verified claims earn appID, inside the exchange transaction. The
// normal flow first refuses claims that are a registrar's identity (403), then an app id inside
// a registrar's space (409); FilePendingRequest repeats both under its lock.
func (service *Service) exchangeClaims(
	ctx context.Context, tx ExchangeTx, appID string, claims Claims,
) (ExchangeResult, error) {
	registrar, findErr := tx.FindRegistrarByClaims(ctx, claims)
	if findErr != nil {
		return ExchangeResult{}, findErr
	}
	if registrar != nil {
		return ExchangeResult{}, ErrBindingMismatch
	}
	inPrefix, prefixErr := tx.AppIDInRegistrarPrefix(ctx, appID)
	if prefixErr != nil {
		return ExchangeResult{}, prefixErr
	}
	if inPrefix {
		return ExchangeResult{}, ErrAppIDInRegistrarPrefix
	}
	return service.decideClaims(ctx, tx, appID, claims, service.now(), true)
}

// exchangeAsRegistrar is the name form of the exchange, after the token verified.
//
// Order: unlocked read (registrar, derived id state, cap, overlap, jti) -> deterministic refusals
// -> person-service CreateApp for a new id -> one transaction that records the jti and
// re-decides everything under the store's lock. A replayed jti and a refusal that does not
// depend on a race never reach person-service.
func (service *Service) exchangeAsRegistrar(
	ctx context.Context, entry exchangeLogEntry, claims Claims, started time.Time,
) (ExchangeResult, error) {
	fail := func(err error) (ExchangeResult, error) {
		service.logFailedExchange(ctx, entry, claims, err, time.Since(started))
		return ExchangeResult{}, err
	}

	state, readErr := service.store.ReadRegistrarExchange(ctx, claims, entry.name)
	if readErr != nil {
		return fail(readErr)
	}
	if state.Registrar != nil {
		entry.prefix, entry.appID = state.Registrar.Prefix, state.DerivedAppID
	}
	if state.JTIRecorded {
		return fail(ErrReplayed)
	}

	createInDirectory := false
	if state.Registrar != nil {
		switch state.Derived {
		case DerivedAppOwned:
		case DerivedAppAbsent:
			switch {
			case state.AppCount >= RegistrarAppLimit:
				return fail(ErrRegistrarAppLimit)
			case state.Overlaps:
				return fail(ErrAppIDOverlap)
			}
			createInDirectory = true
		default:
			// DerivedAppForeign, and any state added later: fail closed.
			return fail(ErrRegistrarOwnership)
		}
	}
	if createInDirectory {
		if registerErr := service.registerAppInDirectory(ctx, state.DerivedAppID, "exchange_name", entry.name); registerErr != nil {
			return fail(registerErr)
		}
	}

	var result ExchangeResult
	txErr := service.store.Exchange(ctx, func(tx ExchangeTx) error {
		fresh, recordErr := tx.RecordJTI(ctx, claims.JTI, claims.ReplayExpiry())
		if recordErr != nil {
			return recordErr
		}
		if !fresh {
			return ErrReplayed
		}
		exchanged, exchangeErr := service.decideRegistrarClaims(ctx, tx, entry.name, claims, createInDirectory, service.now())
		result = exchanged
		return exchangeErr
	})
	if txErr != nil {
		return fail(txErr)
	}
	if result.AppID != "" {
		entry.appID = result.AppID
	}
	var outcome string
	switch {
	case result.Status == ExchangePending:
		outcome = outcomeRegistrarPending
	case result.Created:
		outcome = outcomeRegistrarCreated
	default:
		outcome = outcomeRegistrarIssued
	}
	service.logExchange(ctx, entry, claims, outcome, time.Since(started))
	return result, nil
}

// decideRegistrarClaims is the transaction half of the name form. A registrar that is gone
// (never found, or deleted before the binding insert) is the "no registrar" case.
// createdInDirectory says the caller already registered a new id in the people directory; a new
// id found without that is ErrBindingAppeared, never a skip of person-service.
func (service *Service) decideRegistrarClaims(
	ctx context.Context, tx ExchangeTx, name string, claims Claims, createdInDirectory bool, now time.Time,
) (ExchangeResult, error) {
	registrar, findErr := tx.FindRegistrarByClaims(ctx, claims)
	if findErr != nil {
		return ExchangeResult{}, findErr
	}
	if registrar == nil {
		return service.fileRegistrarRequest(ctx, tx, claims, now)
	}
	derived, deriveErr := DeriveAppID(registrar.Prefix, name)
	if deriveErr != nil {
		return ExchangeResult{}, deriveErr
	}

	binding, bound, lockErr := tx.LockBinding(ctx, derived)
	if lockErr != nil {
		return ExchangeResult{}, lockErr
	}
	created := false
	if !bound {
		if !createdInDirectory {
			return ExchangeResult{}, ErrBindingAppeared
		}
		createErr := tx.CreateRegistrarBinding(ctx, *registrar, derived, now)
		switch {
		case createErr == nil:
			created = true
		case errors.Is(createErr, ErrRegistrarNotFound):
			return service.fileRegistrarRequest(ctx, tx, claims, now)
		case errors.Is(createErr, ErrBindingAppeared):
			// Lost a create race: look once more, then apply the bound branch.
			binding, bound, lockErr = tx.LockBinding(ctx, derived)
			if lockErr != nil {
				return ExchangeResult{}, lockErr
			}
			if !bound {
				return ExchangeResult{}, ErrBindingAppeared
			}
		default:
			return ExchangeResult{}, createErr
		}
	}
	if !created && (binding.RegistrarID == nil || *binding.RegistrarID != registrar.ID) {
		return ExchangeResult{}, ErrRegistrarOwnership
	}
	issued, issueErr := tx.IssueKey(ctx, derived, claims.RunID, now)
	if issueErr != nil {
		return ExchangeResult{}, issueErr
	}
	return ExchangeResult{
		Status: ExchangeIssued, AppID: derived, Key: issued.Plaintext, KeyID: issued.ID, Created: created,
	}, nil
}

// fileRegistrarRequest handles claims that match no registrar: a workflow already bound as a
// normal app is refused (nothing filed), anything else files or refreshes a registrar request.
func (service *Service) fileRegistrarRequest(
	ctx context.Context, tx ExchangeTx, claims Claims, now time.Time,
) (ExchangeResult, error) {
	normal, normalErr := tx.HasNormalBindingForIdentity(ctx, claims)
	if normalErr != nil {
		return ExchangeResult{}, normalErr
	}
	if normal {
		return ExchangeResult{}, ErrBindingMismatch
	}
	requestID, fileErr := tx.FilePendingRegistrarRequest(ctx, claims, now)
	if fileErr != nil {
		return ExchangeResult{}, fileErr
	}
	return ExchangeResult{Status: ExchangePending, RequestID: requestID}, nil
}

// registerAppInDirectory registers appID in the people directory, outside any transaction or
// lock. A nil registrar is ErrAppRegistryNotConfigured; a failure is ErrAppRegistryUnavailable
// (wrapping the cause) after one WARN with the extra log attributes; a caller that went away
// returns its own context error without a WARN.
func (service *Service) registerAppInDirectory(ctx context.Context, appID string, extra ...any) error {
	if service.registrar == nil {
		return ErrAppRegistryNotConfigured
	}
	registerCtx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()
	registerStarted := time.Now()
	registerErr := service.registrar.CreateApp(registerCtx, appID, appID)
	if registerErr == nil {
		return nil
	}
	// The caller went away (parent context only, so the registerTimeout still maps to
	// ErrAppRegistryUnavailable): not a registry fault, so no WARN.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	service.logger.WarnContext(ctx, "app registry registration failed",
		append(extra, "app_id", appID,
			"duration", time.Since(registerStarted).String(), "error", loggedReason(registerErr))...)
	return fmt.Errorf("%w: %w", ErrAppRegistryUnavailable, registerErr)
}

// decideClaims reads the app's binding and acts on it. When the app has none it files a
// pending request, unless mayFile is false. FilePendingRequest answers ErrBindingAppeared when
// an approval committed after the binding was read; the decision is then made once more, with
// mayFile false: the binding is committed, so this read sees it, and a binding that is still
// missing is an error rather than a loop.
func (service *Service) decideClaims(
	ctx context.Context, tx ExchangeTx, appID string, claims Claims, now time.Time, mayFile bool,
) (ExchangeResult, error) {
	binding, bound, lockErr := tx.LockBinding(ctx, appID)
	if lockErr != nil {
		return ExchangeResult{}, lockErr
	}
	if !bound {
		if !mayFile {
			return ExchangeResult{}, ErrBindingAppeared
		}
		requestID, fileErr := tx.FilePendingRequest(ctx, appID, claims, now)
		if errors.Is(fileErr, ErrBindingAppeared) {
			return service.decideClaims(ctx, tx, appID, claims, now, false)
		}
		if fileErr != nil {
			return ExchangeResult{}, fileErr
		}
		return ExchangeResult{Status: ExchangePending, RequestID: requestID}, nil
	}
	if !binding.Matches(claims) {
		return ExchangeResult{}, ErrBindingMismatch
	}
	issued, issueErr := tx.IssueKey(ctx, appID, claims.RunID, now)
	if issueErr != nil {
		return ExchangeResult{}, issueErr
	}
	return ExchangeResult{Status: ExchangeIssued, AppID: appID, Key: issued.Plaintext, KeyID: issued.ID}, nil
}

// exchangeLogEntry names what an exchange was about, for its log line. appID is the requested
// app id (normal form) or the derived one once known (name form); name and prefix are set for
// the name form only. name is caller-controlled, so it is truncated when logged.
type exchangeLogEntry struct {
	appID  string
	name   string
	prefix string
}

// attributes renders the entry's log fields. The name-form fields appear only for the name form.
func (entry exchangeLogEntry) attributes() []any {
	attributes := []any{"app_id", entry.appID}
	if entry.name != "" {
		attributes = append(attributes, "registrar_prefix", entry.prefix, "name", truncateForLog(entry.name))
	}
	return attributes
}

// truncateForLog bounds a caller-controlled value to maxLoggedNameChars runes for a log line.
func truncateForLog(value string) string {
	runes := []rune(value)
	if len(runes) <= maxLoggedNameChars {
		return value
	}
	return string(runes[:maxLoggedNameChars])
}

// logRejectedToken logs an exchange whose token failed verification. The reason names the
// failed check but never the token.
func (service *Service) logRejectedToken(ctx context.Context, entry exchangeLogEntry, verifyErr error, elapsed time.Duration) {
	outcome := "error"
	level := slog.LevelError
	switch {
	case errors.Is(verifyErr, ErrInvalidToken):
		outcome, level = "invalid_token", slog.LevelWarn
	case errors.Is(verifyErr, ErrKeysUnavailable):
		outcome = "keys_unavailable"
	case errors.Is(verifyErr, context.Canceled):
		// The client hung up: not a server fault, and a client that can disconnect at will must
		// not be able to flood the error log.
		outcome, level = "canceled", slog.LevelDebug
	}
	service.logger.Log(ctx, level, "app identity exchange",
		append(entry.attributes(),
			"outcome", outcome, "reason", loggedReason(verifyErr), "duration", elapsed.String())...)
}

// logFailedExchange logs an exchange that failed after its token verified.
func (service *Service) logFailedExchange(
	ctx context.Context, entry exchangeLogEntry, claims Claims, exchangeErr error, elapsed time.Duration,
) {
	outcome := "error"
	level := slog.LevelError
	switch {
	case errors.Is(exchangeErr, ErrReplayed):
		outcome, level = "invalid_token", slog.LevelWarn
	case errors.Is(exchangeErr, ErrBindingMismatch):
		outcome, level = "mismatch", slog.LevelWarn
	case errors.Is(exchangeErr, ErrPendingCap):
		outcome, level = "pending_cap", slog.LevelWarn
	case errors.Is(exchangeErr, ErrRegistrarOwnership):
		outcome, level = outcomeRegistrarDenied, slog.LevelWarn
	case errors.Is(exchangeErr, ErrRegistrarAppLimit):
		outcome, level = outcomeRegistrarCap, slog.LevelWarn
	case errors.Is(exchangeErr, ErrAppIDOverlap):
		outcome, level = "app_id_overlap", slog.LevelWarn
	case errors.Is(exchangeErr, ErrAppIDInRegistrarPrefix):
		outcome, level = "reserved_by_registrar", slog.LevelWarn
	case errors.Is(exchangeErr, ErrBindingAppeared):
		outcome, level = "registration_changed", slog.LevelWarn
	case errors.Is(exchangeErr, context.Canceled):
		outcome, level = "canceled", slog.LevelDebug
	}
	service.logger.Log(ctx, level, "app identity exchange",
		append(claimAttributes(entry, claims),
			"outcome", outcome, "reason", loggedReason(exchangeErr), "duration", elapsed.String())...)
}

// logExchange logs a successful exchange.
func (service *Service) logExchange(ctx context.Context, entry exchangeLogEntry, claims Claims, outcome string, elapsed time.Duration) {
	service.logger.InfoContext(ctx, "app identity exchange",
		append(claimAttributes(entry, claims), "outcome", outcome, "duration", elapsed.String())...)
}

// claimAttributes are the log fields every exchange that got past verification carries. The
// claims were signed by the issuer, so they are safe to log.
func claimAttributes(entry exchangeLogEntry, claims Claims) []any {
	return append(entry.attributes(),
		"repository", claims.Repository,
		"workflow_path", claims.WorkflowPath,
		"ref", claims.Ref,
		"environment", claims.Environment,
		"run_id", claims.RunID)
}

// loggedReason renders err for a log line, bounded so a hostile token cannot inflate it.
func loggedReason(err error) string {
	reason := err.Error()
	if len(reason) <= maxLoggedReasonChars {
		return reason
	}
	// Cut at a rune boundary: the text is attacker-influenced and a split rune is invalid UTF-8.
	cut := maxLoggedReasonChars
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return fmt.Sprintf("%s...", reason[:cut])
}

// ListRequests returns the operator's approval queue: pending requests first, then the
// requests rejected within RejectedRequestWindow.
func (service *Service) ListRequests(ctx context.Context) ([]Request, error) {
	return service.admin.ListRequests(ctx, service.now())
}

// ApproveRequest turns a pending request into the app's binding.
//
// The app is registered in the people directory first, outside the binding transaction and its
// advisory lock, so a slow or failing registry never holds a database lock. A failed
// registration returns ErrAppRegistryUnavailable and writes nothing. The store re-checks under
// its row lock that the request still names the app that was registered.
func (service *Service) ApproveRequest(ctx context.Context, requestID int64) (Binding, error) {
	request, getErr := service.admin.GetRequest(ctx, requestID)
	if getErr != nil {
		return Binding{}, getErr
	}
	if request.Status != RequestPending {
		return Binding{}, ErrRequestNotPending
	}
	if request.Kind == RequestKindRegistrar {
		return Binding{}, ErrRequestIsRegistrar
	}
	if request.AppID == reservedAppID {
		return Binding{}, ErrAppIDReserved
	}
	// Only an approvable app request reaches the not-configured check, so 404 / 409 win over 503.
	if registerErr := service.registerAppInDirectory(ctx, request.AppID, "request_id", requestID); registerErr != nil {
		return Binding{}, registerErr
	}

	binding, approveErr := service.admin.ApproveRequest(ctx, requestID, request.AppID, service.now())
	if approveErr != nil {
		return Binding{}, approveErr
	}
	service.logger.InfoContext(ctx, "app identity request approved",
		"request_id", requestID, "app_id", binding.AppID, "repository", binding.Repository,
		"workflow_path", binding.WorkflowPath, "ref", binding.Ref, "environment", binding.Environment)
	return binding, nil
}

// ApproveRegistrarRequest turns a pending registrar request into a registrar owning prefix. The
// prefix grammar is checked first (ErrInvalidPrefix, nothing else is called); no person-service
// call happens, because apps are registered when the registrar first creates them.
func (service *Service) ApproveRegistrarRequest(
	ctx context.Context, requestID int64, prefix string,
) (WorkflowRegistrar, error) {
	if prefixErr := ValidatePrefix(prefix); prefixErr != nil {
		return WorkflowRegistrar{}, prefixErr
	}
	registrar, approveErr := service.admin.ApproveRegistrarRequest(ctx, requestID, prefix, service.now())
	if approveErr != nil {
		return WorkflowRegistrar{}, approveErr
	}
	service.logger.InfoContext(ctx, "app identity registrar approved",
		"request_id", requestID, "registrar_id", registrar.ID, "prefix", registrar.Prefix,
		"repository", registrar.Repository, "workflow_path", registrar.WorkflowPath,
		"ref", registrar.Ref, "environment", registrar.Environment)
	return registrar, nil
}

// ListRegistrars returns every registrar sorted by prefix with its app count and limit.
func (service *Service) ListRegistrars(ctx context.Context) ([]WorkflowRegistrar, error) {
	return service.admin.ListRegistrars(ctx)
}

// DeleteRegistrar deletes a registrar and unbinds every app it created. It reports whether the
// registrar existed and how many apps went with it. Deleting an unknown id is not an error.
func (service *Service) DeleteRegistrar(ctx context.Context, registrarID int64) (bool, int, error) {
	registrar, unbound, deleteErr := service.admin.DeleteRegistrar(ctx, registrarID)
	if errors.Is(deleteErr, ErrRegistrarNotFound) {
		return false, 0, nil
	}
	if deleteErr != nil {
		return false, 0, deleteErr
	}
	service.logger.InfoContext(ctx, "app identity registrar deleted",
		"registrar_id", registrarID, "prefix", registrar.Prefix, "repository", registrar.Repository,
		"deleted", true, "unbound_apps", unbound)
	return true, unbound, nil
}

// AcknowledgeBinding marks one app as seen by the operator. It is idempotent and keeps the
// first time. ErrBindingNotFound when the app has no binding.
func (service *Service) AcknowledgeBinding(ctx context.Context, appID string) (BindingWithKeys, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return BindingWithKeys{}, validateErr
	}
	binding, acknowledgeErr := service.admin.AcknowledgeBinding(ctx, appID, service.now())
	if acknowledgeErr != nil {
		return BindingWithKeys{}, acknowledgeErr
	}
	service.logger.InfoContext(ctx, "binding acknowledged", "app_id", appID)
	return binding, nil
}

// AcknowledgeAllBindings marks the listed registrar-created apps as seen and returns how many it
// changed. The list must hold 1 to MaxAcknowledgeApps ids (counted before duplicates are
// dropped), each a valid app id.
func (service *Service) AcknowledgeAllBindings(ctx context.Context, appIDs []string) (int, error) {
	if len(appIDs) == 0 || len(appIDs) > MaxAcknowledgeApps {
		return 0, &ValidationError{Message: fmt.Sprintf("app_ids must list 1 to %d apps", MaxAcknowledgeApps)}
	}
	unique := make([]string, 0, len(appIDs))
	seen := make(map[string]struct{}, len(appIDs))
	for _, appID := range appIDs {
		if validateErr := ValidateAppID(appID); validateErr != nil {
			return 0, validateErr
		}
		if _, duplicate := seen[appID]; duplicate {
			continue
		}
		seen[appID] = struct{}{}
		unique = append(unique, appID)
	}
	changed, acknowledgeErr := service.admin.AcknowledgeAllBindings(ctx, unique, service.now())
	if acknowledgeErr != nil {
		return 0, acknowledgeErr
	}
	service.logger.InfoContext(ctx, "bindings acknowledged all", "requested", len(unique), "changed", changed)
	return changed, nil
}

// RejectRequest rejects a pending request; rejecting a rejected one is a no-op.
func (service *Service) RejectRequest(ctx context.Context, requestID int64) (Request, error) {
	request, rejectErr := service.admin.RejectRequest(ctx, requestID, service.now())
	if rejectErr != nil {
		return Request{}, rejectErr
	}
	service.logger.InfoContext(ctx, "app identity request rejected", "request_id", requestID, "app_id", request.AppID)
	return request, nil
}

// ListBindings returns every binding with its keys.
func (service *Service) ListBindings(ctx context.Context) ([]BindingWithKeys, error) {
	return service.admin.ListBindings(ctx, service.now())
}

// ResolveKey authenticates a presented app key and returns the app it belongs to. An error
// wrapping ErrInvalidKey means the key is malformed, unknown, revoked or past its supersede
// grace; any other error is a store failure the caller must treat as unavailable (fail closed).
// A valid key's last_used_at is refreshed at most once a minute. That write is best effort: a
// failure is logged and never turns a valid key into a denial.
func (service *Service) ResolveKey(ctx context.Context, key string) (ResolvedKey, error) {
	now := service.now()
	resolved, resolveErr := service.store.ResolveKey(ctx, key, now)
	if resolveErr != nil {
		return ResolvedKey{}, resolveErr
	}
	if _, touchErr := service.store.TouchKey(ctx, resolved.ID, now); touchErr != nil {
		service.logger.WarnContext(ctx, "app key last_used_at not recorded",
			"key_id", resolved.ID, "app_id", resolved.AppID, "key_hint", resolved.Hint, "error", touchErr)
	}
	return resolved, nil
}

// RevokeKey revokes one key immediately.
func (service *Service) RevokeKey(ctx context.Context, keyID int64) (Key, error) {
	key, revokeErr := service.admin.RevokeKey(ctx, keyID, service.now())
	if revokeErr != nil {
		return Key{}, revokeErr
	}
	service.logger.InfoContext(ctx, "app key revoked", "key_id", keyID, "app_id", key.AppID, "key_hint", key.Hint, "state", string(key.State))
	return key, nil
}

// HasBinding reports whether the app has an approved binding. An invalid app id is an error;
// a store failure is propagated.
func (service *Service) HasBinding(ctx context.Context, appID string) (bool, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return false, validateErr
	}
	return service.admin.BindingExists(ctx, appID)
}

// Unbind deletes an app's binding and invalidates every key issued under it. The objects
// the app owns are kept.
func (service *Service) Unbind(ctx context.Context, appID string) (bool, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return false, validateErr
	}
	deleted, unbindErr := service.admin.Unbind(ctx, appID)
	if unbindErr != nil {
		return false, unbindErr
	}
	service.logger.InfoContext(ctx, "app unbound", "app_id", appID, "deleted", deleted)
	return deleted, nil
}

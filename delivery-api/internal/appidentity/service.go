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

// ExchangeResult is the successful outcome of an exchange.
type ExchangeResult struct {
	// Status is ExchangeIssued or ExchangePending.
	Status string
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
func (service *Service) Exchange(ctx context.Context, appID string, token string) (ExchangeResult, error) {
	started := time.Now()
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return ExchangeResult{}, validateErr
	}
	if reservedErr := validateExchangeAppID(appID); reservedErr != nil {
		return ExchangeResult{}, reservedErr
	}
	if token == "" {
		return ExchangeResult{}, &ValidationError{Message: "token is required"}
	}

	claims, verifyErr := service.verifier.Verify(ctx, token, verifyOnly{})
	if verifyErr != nil {
		service.logRejectedToken(ctx, appID, verifyErr, time.Since(started))
		return ExchangeResult{}, verifyErr
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
		service.logFailedExchange(ctx, appID, claims, txErr, time.Since(started))
		return ExchangeResult{}, txErr
	}
	service.logExchange(ctx, appID, claims, result.Status, time.Since(started))
	return result, nil
}

// exchangeClaims decides what verified claims earn appID, inside the exchange transaction.
func (service *Service) exchangeClaims(
	ctx context.Context, tx ExchangeTx, appID string, claims Claims,
) (ExchangeResult, error) {
	return service.decideClaims(ctx, tx, appID, claims, service.now(), true)
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

// logRejectedToken logs an exchange whose token failed verification. The reason names the
// failed check but never the token.
func (service *Service) logRejectedToken(ctx context.Context, appID string, verifyErr error, elapsed time.Duration) {
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
		"app_id", appID, "outcome", outcome, "reason", loggedReason(verifyErr), "duration", elapsed.String())
}

// logFailedExchange logs an exchange that failed after its token verified.
func (service *Service) logFailedExchange(
	ctx context.Context, appID string, claims Claims, exchangeErr error, elapsed time.Duration,
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
	case errors.Is(exchangeErr, context.Canceled):
		outcome, level = "canceled", slog.LevelDebug
	}
	service.logger.Log(ctx, level, "app identity exchange",
		append(claimAttributes(appID, claims),
			"outcome", outcome, "reason", loggedReason(exchangeErr), "duration", elapsed.String())...)
}

// logExchange logs a successful exchange.
func (service *Service) logExchange(ctx context.Context, appID string, claims Claims, outcome string, elapsed time.Duration) {
	service.logger.InfoContext(ctx, "app identity exchange",
		append(claimAttributes(appID, claims), "outcome", outcome, "duration", elapsed.String())...)
}

// claimAttributes are the log fields every exchange that got past verification carries. The
// claims were signed by the issuer, so they are safe to log.
func claimAttributes(appID string, claims Claims) []any {
	return []any{
		"app_id", appID,
		"repository", claims.Repository,
		"workflow_path", claims.WorkflowPath,
		"ref", claims.Ref,
		"environment", claims.Environment,
		"run_id", claims.RunID,
	}
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
	if service.registrar == nil {
		return Binding{}, ErrAppRegistryNotConfigured
	}
	request, getErr := service.admin.GetRequest(ctx, requestID)
	if getErr != nil {
		return Binding{}, getErr
	}
	if request.Status != RequestPending {
		return Binding{}, ErrRequestNotPending
	}
	if request.AppID == reservedAppID {
		return Binding{}, ErrAppIDReserved
	}

	registerCtx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()
	registerStarted := time.Now()
	if registerErr := service.registrar.CreateApp(registerCtx, request.AppID, request.AppID); registerErr != nil {
		// The caller went away (parent context only, so the registerTimeout still maps to
		// ErrAppRegistryUnavailable): not a registry fault, so no WARN.
		if ctx.Err() != nil {
			return Binding{}, ctx.Err()
		}
		service.logger.WarnContext(ctx, "app registry registration failed",
			"request_id", requestID, "app_id", request.AppID,
			"duration", time.Since(registerStarted).String(), "error", loggedReason(registerErr))
		return Binding{}, fmt.Errorf("%w: %w", ErrAppRegistryUnavailable, registerErr)
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

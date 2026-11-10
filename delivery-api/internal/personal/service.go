package personal

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	maxLabelLength          = 64
	maxDisabledReasonLength = 200
	// secretsErrorKey marks a view whose sealed secrets could not be opened.
	secretsErrorKey = "secrets_error"
	maskedValue     = "****"
)

// Messages the HTTP layer returns verbatim (they name fields, never values).
const (
	msgExactlyOnePerson = "exactly one of email or person_hash required"
	msgInvalidEmail     = "invalid email"
	msgInvalidPerson    = "invalid person_hash"
	msgInvalidType      = "type must be discord or webhook"
	msgInvalidLabel     = "invalid label"
	msgInvalidReason    = "invalid disabled_reason"
)

// ValidationError reports a request field that failed validation. Message is
// safe to return to the client: it names the field, never its value.
type ValidationError struct {
	Message string
}

// Error returns the validation message.
func (validationError *ValidationError) Error() string { return validationError.Message }

// PersonRef names the person a call is about: exactly one of Email (asserted by
// the app, validated then hashed, never stored) or PersonHash.
type PersonRef struct {
	Email      string
	PersonHash string
}

// Resolve returns the person hash, or a *ValidationError.
func (ref PersonRef) Resolve() (string, error) {
	hasEmail := ref.Email != ""
	hasHash := ref.PersonHash != ""
	if hasEmail == hasHash {
		return "", &ValidationError{Message: msgExactlyOnePerson}
	}
	if hasEmail {
		if !personhash.ValidEmail(ref.Email) {
			return "", &ValidationError{Message: msgInvalidEmail}
		}
		return personhash.Hash(ref.Email), nil
	}
	if !personhash.IsHash(ref.PersonHash) {
		return "", &ValidationError{Message: msgInvalidPerson}
	}
	return ref.PersonHash, nil
}

// Store is the persistence the Service needs. *PGStore implements it.
type Store interface {
	Insert(ctx context.Context, input NewDestination) (Destination, error)
	Get(ctx context.Context, id int64) (Destination, error)
	ListByPerson(ctx context.Context, personHash string) ([]Destination, error)
	ListForAdmin(ctx context.Context, personHash string) ([]Destination, int, error)
	Update(ctx context.Context, id int64, mutate func(Destination) (Destination, error)) (Destination, error)
	Delete(ctx context.Context, id int64) (bool, error)
	// DeleteManaged deletes the row only when it is the person's and appID is its live registrar, in one statement.
	DeleteManaged(ctx context.Context, id int64, appID, personHash string) (bool, error)
	OptIn(ctx context.Context, appID string, id int64) error
	OptOut(ctx context.Context, appID string, id int64) (reaped bool, err error)
	OptedInAppIDs(ctx context.Context, id int64) ([]string, error)
	IsOptedIn(ctx context.Context, appID string, id int64) (bool, error)
	// ListEnabledOptedInByIDs returns the person's enabled rows among ids that appID is opted in for,
	// ordered by id; an ineligible id is simply absent.
	ListEnabledOptedInByIDs(ctx context.Context, appID, personHash string, ids []int64) ([]Destination, error)
	// DisableBySystem disables only an enabled row; it reports whether a transition happened.
	DisableBySystem(ctx context.Context, id int64, reason string) (bool, error)
	// ForgetPersonForApp drops appID's opt-ins for the person and reaps the rows that leave orphaned.
	ForgetPersonForApp(ctx context.Context, appID, personHash string) (optIns int64, destinations int64, err error)
	// ForgetPerson deletes all the person's rows.
	ForgetPerson(ctx context.Context, personHash string) (int64, error)
}

var _ Store = (*PGStore)(nil)

// Service holds the business rules for personal destinations, independent of HTTP.
type Service struct {
	store      Store
	sealer     *seal.Sealer
	urlChecker registry.DestinationURLChecker
	logger     *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithURLChecker sets the registration-time SSRF check. Without one, every write
// that carries a URL fails closed with registry.ErrURLCheckUnavailable.
func WithURLChecker(checker registry.DestinationURLChecker) Option {
	return func(service *Service) { service.urlChecker = checker }
}

// NewService builds a Service. A nil logger defaults to slog.Default().
func NewService(store Store, sealer *seal.Sealer, logger *slog.Logger, options ...Option) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	service := &Service{store: store, sealer: sealer, logger: logger}
	for _, option := range options {
		option(service)
	}
	return service
}

// CreateInput is an app's request to register a personal destination.
type CreateInput struct {
	Person  PersonRef
	Label   string
	Type    string
	Config  map[string]string
	Secrets map[string]string
	// OptIn nil means true: the registering app is opted in.
	OptIn *bool
}

// Patch is a partial update. A nil field is kept; Config and Secrets merge per key.
type Patch struct {
	Label   *string
	Config  map[string]string
	Secrets map[string]string
	Enabled *bool
}

// AppView is a destination as an app sees it. Secrets are masked.
type AppView struct {
	ID                int64             `json:"id"`
	PersonHash        string            `json:"person_hash"`
	Label             string            `json:"label"`
	Type              string            `json:"type"`
	Config            map[string]string `json:"config"`
	Secrets           map[string]string `json:"secrets"`
	Enabled           bool              `json:"enabled"`
	DisabledBy        *DisabledBy       `json:"disabled_by"`
	DisabledReason    *string           `json:"disabled_reason"`
	RegisteredByAppID string            `json:"registered_by_app_id"`
	ManagedByCaller   bool              `json:"managed_by_caller"`
	OptedIn           bool              `json:"opted_in"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// AdminView is a destination as the operator sees it. Secrets are masked.
type AdminView struct {
	ID                int64             `json:"id"`
	PersonHash        string            `json:"person_hash"`
	Label             string            `json:"label"`
	Type              string            `json:"type"`
	Config            map[string]string `json:"config"`
	Secrets           map[string]string `json:"secrets"`
	Enabled           bool              `json:"enabled"`
	DisabledBy        *DisabledBy       `json:"disabled_by"`
	DisabledReason    *string           `json:"disabled_reason"`
	RegisteredByAppID string            `json:"registered_by_app_id"`
	OptedInAppIDs     []string          `json:"opted_in_app_ids"`
	RegistrarRemoved  bool              `json:"registrar_removed"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// Create registers a personal destination for the person on behalf of appID.
func (service *Service) Create(ctx context.Context, appID string, input CreateInput) (AppView, error) {
	if err := validateAppID(appID); err != nil {
		return AppView{}, err
	}
	personHash, personErr := input.Person.Resolve()
	if personErr != nil {
		return AppView{}, personErr
	}
	if input.Type != TypeDiscord && input.Type != TypeWebhook {
		return AppView{}, &ValidationError{Message: msgInvalidType}
	}
	label, labelErr := normalizeLabel(input.Label)
	if labelErr != nil {
		return AppView{}, labelErr
	}
	config := cloneMap(input.Config)
	secrets := cloneMap(input.Secrets)
	if fieldsErr := validateFields(input.Type, config, secrets); fieldsErr != nil {
		return AppView{}, fieldsErr
	}
	// The DNS lookup can take seconds, so it runs before any write, never in a transaction.
	if urlErr := service.checkURL(ctx, appID, personHash, input.Type, secrets); urlErr != nil {
		return AppView{}, urlErr
	}
	stored, storeErr := service.store.Insert(ctx, NewDestination{
		PersonHash:        personHash,
		RegisteredByAppID: appID,
		Label:             label,
		Type:              input.Type,
		Config:            config,
		Secrets:           secrets,
		OptIn:             input.OptIn == nil || *input.OptIn,
	})
	if storeErr != nil {
		return AppView{}, storeErr
	}
	service.logger.InfoContext(ctx, "personal destination created",
		"app_id", appID, "person_hash", personHash, "id", stored.ID, "type", stored.Type)
	// The opt-in state comes from the request, not a second read: the row is already
	// written, so a failing read must not turn a success into an error.
	return service.buildAppView(ctx, appID, stored, input.OptIn == nil || *input.OptIn), nil
}

// Lookup returns the person's hash and all their destinations (any registrar),
// newest first, never nil.
func (service *Service) Lookup(ctx context.Context, appID string, person PersonRef) (string, []AppView, error) {
	if err := validateAppID(appID); err != nil {
		return "", nil, err
	}
	personHash, personErr := person.Resolve()
	if personErr != nil {
		return "", nil, personErr
	}
	stored, storeErr := service.store.ListByPerson(ctx, personHash)
	if storeErr != nil {
		return "", nil, storeErr
	}
	views := make([]AppView, 0, len(stored))
	for _, destination := range stored {
		view, viewErr := service.appView(ctx, appID, destination)
		if viewErr != nil {
			return "", nil, viewErr
		}
		views = append(views, view)
	}
	return personHash, views, nil
}

// Update applies patch when appID is the live registrar. Checks run in order:
// person match (ErrNotFound), registrar (ErrManagedByOtherApp), operator disable
// (ErrDisabledByOperator, which rejects the whole patch).
//
// The SSRF URL pre-check covers the patched URL value only, not the locked row
// snapshot: a row's stored URL is not re-checked by an update that leaves the
// URL alone.
func (service *Service) Update(
	ctx context.Context, appID string, id int64, person PersonRef, patch Patch,
) (AppView, error) {
	if err := validateAppID(appID); err != nil {
		return AppView{}, err
	}
	personHash, personErr := person.Resolve()
	if personErr != nil {
		return AppView{}, personErr
	}
	if patch.Label != nil {
		if _, labelErr := normalizeLabel(*patch.Label); labelErr != nil {
			return AppView{}, labelErr
		}
	}
	current, getErr := service.getManaged(ctx, appID, id, personHash)
	if getErr != nil {
		return AppView{}, getErr
	}
	if enableErr := checkEnable(current, patch.Enabled); enableErr != nil {
		return AppView{}, enableErr
	}
	if checkErr := service.checkPatchedURL(ctx, appID, current, patch); checkErr != nil {
		return AppView{}, checkErr
	}
	stored, storeErr := service.store.Update(ctx, id, func(locked Destination) (Destination, error) {
		// Re-run the checks on the locked row: the pre-lock decision is not trusted.
		if authErr := authorizeManage(locked, appID, personHash); authErr != nil {
			return Destination{}, authErr
		}
		if enableErr := checkEnable(locked, patch.Enabled); enableErr != nil {
			return Destination{}, enableErr
		}
		return service.applyPatch(locked, patch)
	})
	if storeErr != nil {
		return AppView{}, storeErr
	}
	service.logger.InfoContext(ctx, "personal destination updated",
		"app_id", appID, "person_hash", personHash, "id", id)
	return service.appView(ctx, appID, stored)
}

// Delete removes the destination when appID is its live registrar.
func (service *Service) Delete(ctx context.Context, appID string, id int64, person PersonRef) error {
	if err := validateAppID(appID); err != nil {
		return err
	}
	personHash, personErr := person.Resolve()
	if personErr != nil {
		return personErr
	}
	if _, getErr := service.getManaged(ctx, appID, id, personHash); getErr != nil {
		return getErr
	}
	deleted, storeErr := service.store.DeleteManaged(ctx, id, appID, personHash)
	if storeErr != nil {
		return storeErr
	}
	if !deleted {
		return ErrNotFound
	}
	service.logger.InfoContext(ctx, "personal destination deleted",
		"app_id", appID, "person_hash", personHash, "id", id)
	return nil
}

// OptIn opts appID in for the destination (idempotent). It works on a disabled
// or registrar-removed row; only the person must match.
func (service *Service) OptIn(ctx context.Context, appID string, id int64, person PersonRef) (AppView, error) {
	if err := validateAppID(appID); err != nil {
		return AppView{}, err
	}
	personHash, personErr := person.Resolve()
	if personErr != nil {
		return AppView{}, personErr
	}
	if _, getErr := service.getForPerson(ctx, id, personHash); getErr != nil {
		return AppView{}, getErr
	}
	if optErr := service.store.OptIn(ctx, appID, id); optErr != nil {
		return AppView{}, optErr
	}
	service.logger.InfoContext(ctx, "personal destination opted in",
		"app_id", appID, "person_hash", personHash, "id", id)
	stored, getErr := service.getForPerson(ctx, id, personHash)
	if getErr != nil {
		return AppView{}, getErr
	}
	return service.appView(ctx, appID, stored)
}

// OptOut removes appID's opt-in (idempotent). When that leaves a registrar-removed
// row with no opt-ins the store deletes it in the same transaction and deleted is
// true (the view is then zero); an opt-out on a live-registrar row never deletes.
func (service *Service) OptOut(
	ctx context.Context, appID string, id int64, person PersonRef,
) (AppView, bool, error) {
	if validateErr := validateAppID(appID); validateErr != nil {
		return AppView{}, false, validateErr
	}
	personHash, personErr := person.Resolve()
	if personErr != nil {
		return AppView{}, false, personErr
	}
	if _, getErr := service.getForPerson(ctx, id, personHash); getErr != nil {
		return AppView{}, false, getErr
	}
	reaped, storeErr := service.store.OptOut(ctx, appID, id)
	if storeErr != nil {
		return AppView{}, false, storeErr
	}
	service.logger.InfoContext(ctx, "personal destination opted out",
		"app_id", appID, "person_hash", personHash, "id", id, "reaped", reaped)
	if reaped {
		return AppView{}, true, nil
	}
	stored, getErr := service.getForPerson(ctx, id, personHash)
	if getErr != nil {
		return AppView{}, false, getErr
	}
	appViewResult, viewErr := service.appView(ctx, appID, stored)
	return appViewResult, false, viewErr
}

// AdminList returns the newest AdminListLimit destinations (optionally one
// person's; an empty personHash means all) and the total matching the filter.
func (service *Service) AdminList(ctx context.Context, personHash string) ([]AdminView, int, error) {
	if personHash != "" && !personhash.IsHash(personHash) {
		return nil, 0, &ValidationError{Message: msgInvalidPerson}
	}
	stored, total, storeErr := service.store.ListForAdmin(ctx, personHash)
	if storeErr != nil {
		return nil, 0, storeErr
	}
	views := make([]AdminView, 0, len(stored))
	for _, destination := range stored {
		view, viewErr := service.adminView(ctx, destination)
		if viewErr != nil {
			return nil, 0, viewErr
		}
		views = append(views, view)
	}
	return views, total, nil
}

// AdminSetEnabled is the operator's disable/enable. Disabling records
// disabled_by = operator and the optional reason (at most 200 characters);
// enabling clears both, whoever disabled the row. ErrNotFound when absent.
func (service *Service) AdminSetEnabled(
	ctx context.Context, id int64, enabled bool, reason string,
) (AdminView, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > maxDisabledReasonLength || containsControl(reason) {
		return AdminView{}, &ValidationError{Message: msgInvalidReason}
	}
	stored, storeErr := service.store.Update(ctx, id, func(locked Destination) (Destination, error) {
		if enabled {
			locked.Enabled = true
			locked.DisabledBy = nil
			locked.DisabledReason = nil
			return locked, nil
		}
		operator := DisabledByOperator
		locked.Enabled = false
		locked.DisabledBy = &operator
		locked.DisabledReason = nil
		if reason != "" {
			locked.DisabledReason = &reason
		}
		return locked, nil
	})
	if storeErr != nil {
		return AdminView{}, storeErr
	}
	service.logger.InfoContext(ctx, "personal destination set by operator",
		"person_hash", stored.PersonHash, "id", id, "enabled", enabled)
	return service.adminView(ctx, stored)
}

// AdminDelete removes the destination; ErrNotFound when absent.
func (service *Service) AdminDelete(ctx context.Context, id int64) error {
	deleted, storeErr := service.store.Delete(ctx, id)
	if storeErr != nil {
		return storeErr
	}
	if !deleted {
		return ErrNotFound
	}
	service.logger.InfoContext(ctx, "personal destination deleted by operator", "id", id)
	return nil
}

// ForgetPersonForApp removes the person's footprint for one app: that app's
// opt-ins, and the rows the removal leaves with no opt-in when their registrar
// is appID or already removed. A live other-registrar row loses only this app's
// opt-in. Counts are logged with app_id and person_hash only.
func (service *Service) ForgetPersonForApp(ctx context.Context, appID, personHash string) error {
	if err := validateAppID(appID); err != nil {
		return err
	}
	if !personhash.IsHash(personHash) {
		return &ValidationError{Message: msgInvalidPerson}
	}
	optIns, destinations, storeErr := service.store.ForgetPersonForApp(ctx, appID, personHash)
	if storeErr != nil {
		return storeErr
	}
	service.logger.InfoContext(ctx, "personal destinations cleaned up for person and app",
		"app_id", appID, "person_hash", personHash, "opt_ins", optIns, "destinations", destinations)
	return nil
}

// ForgetPerson deletes every personal destination of the person.
func (service *Service) ForgetPerson(ctx context.Context, personHash string) error {
	if !personhash.IsHash(personHash) {
		return &ValidationError{Message: msgInvalidPerson}
	}
	destinations, storeErr := service.store.ForgetPerson(ctx, personHash)
	if storeErr != nil {
		return storeErr
	}
	service.logger.InfoContext(ctx, "personal destinations deleted for person",
		"person_hash", personHash, "destinations", destinations)
	return nil
}

// getForPerson loads the row and returns ErrNotFound unless it belongs to the
// person; a foreign row and an absent one are the same error (no oracle).
func (service *Service) getForPerson(ctx context.Context, id int64, personHash string) (Destination, error) {
	stored, storeErr := service.store.Get(ctx, id)
	if storeErr != nil {
		return Destination{}, storeErr
	}
	if stored.PersonHash != personHash {
		return Destination{}, ErrNotFound
	}
	return stored, nil
}

// getManaged is getForPerson plus the registrar check.
func (service *Service) getManaged(
	ctx context.Context, appID string, id int64, personHash string,
) (Destination, error) {
	stored, getErr := service.getForPerson(ctx, id, personHash)
	if getErr != nil {
		return Destination{}, getErr
	}
	if authErr := authorizeManage(stored, appID, personHash); authErr != nil {
		return Destination{}, authErr
	}
	return stored, nil
}

// authorizeManage fails closed: the row must be the person's (ErrNotFound) and
// registered by appID with the registrar still present (ErrManagedByOtherApp).
func authorizeManage(destination Destination, appID, personHash string) error {
	if destination.PersonHash != personHash {
		return ErrNotFound
	}
	if appID == "" || destination.RegisteredByAppID != appID || destination.RegistrarRemovedAt != nil {
		return ErrManagedByOtherApp
	}
	return nil
}

// checkEnable refuses an app enabling an operator-disabled row.
func checkEnable(destination Destination, enabled *bool) error {
	if enabled == nil || !*enabled {
		return nil
	}
	if !destination.Enabled && destination.DisabledBy != nil && *destination.DisabledBy == DisabledByOperator {
		return ErrDisabledByOperator
	}
	return nil
}

// applyPatch is the Update mutate step, run inside the store's row lock. Each
// field applies independently; an unchanged result is detected by the store,
// which then skips the write.
func (service *Service) applyPatch(current Destination, patch Patch) (Destination, error) {
	if patch.Label != nil {
		label, labelErr := normalizeLabel(*patch.Label)
		if labelErr != nil {
			return Destination{}, labelErr
		}
		current.Label = label
	}
	if patch.Enabled != nil {
		if *patch.Enabled {
			current.Enabled = true
			current.DisabledBy = nil
			current.DisabledReason = nil
		} else if current.Enabled {
			// Only an enabled row can be disabled by an app. On an already-disabled row
			// disabled_by and disabled_reason are preserved.
			app := DisabledByApp
			current.Enabled = false
			current.DisabledBy = &app
			current.DisabledReason = nil
		}
	}
	if len(patch.Config) == 0 && len(patch.Secrets) == 0 {
		return current, nil
	}
	secrets, openErr := OpenSecrets(service.sealer, current.ID, current.SealedSecrets)
	if openErr != nil {
		return Destination{}, openErr
	}
	config := cloneMap(current.Config)
	maps.Copy(config, patch.Config)
	merged := cloneMap(secrets)
	maps.Copy(merged, patch.Secrets)
	if fieldsErr := validateFields(current.Type, config, merged); fieldsErr != nil {
		return Destination{}, fieldsErr
	}
	current.Config = config
	if !maps.Equal(secrets, merged) {
		sealed, sealErr := SealSecrets(service.sealer, current.ID, merged)
		if sealErr != nil {
			return Destination{}, sealErr
		}
		current.SealedSecrets = sealed
	}
	return current, nil
}

// checkPatchedURL runs the SSRF check when the patch changes the effective URL
// secret. The merged fields are validated first, so only a statically valid URL
// is ever resolved.
func (service *Service) checkPatchedURL(
	ctx context.Context, appID string, current Destination, patch Patch,
) error {
	urlKey := urlSecretKey(current.Type)
	if urlKey == "" {
		return nil
	}
	newURL, patchesURL := patch.Secrets[urlKey]
	if !patchesURL {
		return nil
	}
	secrets, openErr := OpenSecrets(service.sealer, current.ID, current.SealedSecrets)
	if openErr != nil {
		return openErr
	}
	if secrets[urlKey] == newURL {
		return nil
	}
	config := cloneMap(current.Config)
	maps.Copy(config, patch.Config)
	maps.Copy(secrets, patch.Secrets)
	if fieldsErr := validateFields(current.Type, config, secrets); fieldsErr != nil {
		return fieldsErr
	}
	return service.checkURL(ctx, appID, current.PersonHash, current.Type, secrets)
}

// checkURL runs the registration-time SSRF check. With no checker configured it
// fails closed with registry.ErrURLCheckUnavailable.
func (service *Service) checkURL(
	ctx context.Context, appID, personHash, destinationType string, secrets map[string]string,
) error {
	if urlSecretKey(destinationType) == "" {
		return nil
	}
	if service.urlChecker == nil {
		service.logger.ErrorContext(ctx, "personal destination url check skipped: no checker configured; refused",
			"app_id", appID, "person_hash", personHash, "type", destinationType)
		return registry.ErrURLCheckUnavailable
	}
	if checkErr := service.urlChecker.CheckDestinationSecrets(ctx, destinationType, secrets); checkErr != nil {
		// The checker's message names the field, never the URL.
		service.logger.WarnContext(ctx, "personal destination url refused",
			"app_id", appID, "person_hash", personHash, "type", destinationType, "reason", checkErr.Error())
		return asValidation(checkErr)
	}
	return nil
}

func (service *Service) appView(ctx context.Context, callerAppID string, stored Destination) (AppView, error) {
	optedIn, optErr := service.store.IsOptedIn(ctx, callerAppID, stored.ID)
	if optErr != nil {
		return AppView{}, optErr
	}
	return service.buildAppView(ctx, callerAppID, stored, optedIn), nil
}

func (service *Service) buildAppView(
	ctx context.Context, callerAppID string, stored Destination, optedIn bool,
) AppView {
	return AppView{
		ID:                stored.ID,
		PersonHash:        stored.PersonHash,
		Label:             stored.Label,
		Type:              stored.Type,
		Config:            cloneMap(stored.Config),
		Secrets:           service.maskedSecrets(ctx, stored),
		Enabled:           stored.Enabled,
		DisabledBy:        stored.DisabledBy,
		DisabledReason:    stored.DisabledReason,
		RegisteredByAppID: stored.RegisteredByAppID,
		ManagedByCaller:   stored.RegisteredByAppID == callerAppID && stored.RegistrarRemovedAt == nil,
		OptedIn:           optedIn,
		CreatedAt:         stored.CreatedAt,
		UpdatedAt:         stored.UpdatedAt,
	}
}

func (service *Service) adminView(ctx context.Context, stored Destination) (AdminView, error) {
	appIDs, listErr := service.store.OptedInAppIDs(ctx, stored.ID)
	if listErr != nil {
		return AdminView{}, listErr
	}
	appIDs = slices.Clone(appIDs)
	if appIDs == nil {
		appIDs = []string{}
	}
	slices.Sort(appIDs)
	return AdminView{
		ID:                stored.ID,
		PersonHash:        stored.PersonHash,
		Label:             stored.Label,
		Type:              stored.Type,
		Config:            cloneMap(stored.Config),
		Secrets:           service.maskedSecrets(ctx, stored),
		Enabled:           stored.Enabled,
		DisabledBy:        stored.DisabledBy,
		DisabledReason:    stored.DisabledReason,
		RegisteredByAppID: stored.RegisteredByAppID,
		OptedInAppIDs:     appIDs,
		RegistrarRemoved:  stored.RegistrarRemovedAt != nil,
		CreatedAt:         stored.CreatedAt,
		UpdatedAt:         stored.UpdatedAt,
	}, nil
}

// maskedSecrets opens and masks the row's secrets; on a failure it logs and
// returns an error marker instead of a value.
func (service *Service) maskedSecrets(ctx context.Context, stored Destination) map[string]string {
	secrets, openErr := OpenSecrets(service.sealer, stored.ID, stored.SealedSecrets)
	if openErr != nil {
		service.logger.ErrorContext(ctx, "personal destination secrets could not be opened",
			"id", stored.ID, "error", openErr)
		return map[string]string{secretsErrorKey: maskedValue}
	}
	return registry.MaskSecrets(secrets)
}

// urlSecretKey names the secrets key holding the app-supplied URL of a type.
func urlSecretKey(destinationType string) string {
	switch destinationType {
	case TypeWebhook:
		return "url"
	case TypeDiscord:
		return "webhook_url"
	default:
		return ""
	}
}

// asValidation converts a registry validation error into this package's
// ValidationError so callers map one shape; any other error passes through.
func asValidation(err error) error {
	var registryErr *registry.ValidationError
	if errors.As(err, &registryErr) {
		return &ValidationError{Message: registryErr.Message}
	}
	return err
}

// validateFields applies the registry's config/secret rules for the type.
func validateFields(destinationType string, config, secrets map[string]string) error {
	return asValidation(registry.ValidateDestinationFields(destinationType, config, secrets))
}

func validateAppID(appID string) error {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return &ValidationError{Message: validateErr.Error()}
	}
	return nil
}

// normalizeLabel trims the label and requires 1-64 printable characters with no
// control characters.
func normalizeLabel(label string) (string, error) {
	trimmed := strings.TrimSpace(label)
	length := utf8.RuneCountInString(trimmed)
	if !utf8.ValidString(trimmed) || length < 1 || length > maxLabelLength {
		return "", &ValidationError{Message: msgInvalidLabel}
	}
	for _, character := range trimmed {
		if !unicode.IsPrint(character) {
			return "", &ValidationError{Message: msgInvalidLabel}
		}
	}
	return trimmed, nil
}

func containsControl(text string) bool {
	return strings.ContainsFunc(text, unicode.IsControl)
}

func cloneMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	maps.Copy(cloned, values)
	return cloned
}

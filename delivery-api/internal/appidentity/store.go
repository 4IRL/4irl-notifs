package appidentity

import (
	"context"
	"errors"
	"time"
)

const (
	// MaxPendingRequests caps the pending registration requests held at once, across all
	// apps. Past it a new request is refused, so an unauthenticated flood of exchanges cannot
	// grow the operator's queue without bound.
	MaxPendingRequests = 20
)

var (
	// ErrPendingCap means MaxPendingRequests pending requests already exist and the claims
	// are not already one of them.
	ErrPendingCap = errors.New("too many pending identity requests")
	// ErrInvalidKey wraps every reason a presented key is not usable: not well formed,
	// unknown, revoked, or past its supersede grace. The wrapped text is for the log only.
	ErrInvalidKey = errors.New("invalid app key")
	// ErrBindingAppeared is returned by FilePendingRequest when the app got a binding after
	// the exchange looked for one (an approval committed in between). Nothing was filed; the
	// exchange re-reads the binding in the same transaction and decides again.
	ErrBindingAppeared = errors.New("app binding appeared while filing a request")

	// ErrExchangeTarget means an exchange named both or neither of app_id and name. The
	// service returns it and the HTTP layer maps it to 400.
	ErrExchangeTarget = errors.New("send exactly one of app_id or name")
	// ErrRegistrarAppLimit means the registrar already created RegistrarAppLimit apps.
	ErrRegistrarAppLimit = errors.New("registrar app limit reached")
	// ErrPrefixOverlap means a registrar prefix overlaps a bound app id or another registrar's
	// prefix (equal stem, one starting with the other, or an underscore-extension).
	ErrPrefixOverlap = errors.New("prefix overlaps an approved app id or registrar")
	// ErrPrefixTaken means another registrar already owns exactly this prefix.
	ErrPrefixTaken = errors.New("prefix already used")
	// ErrRequestIsRegistrar means an app approval was attempted on a registrar request.
	ErrRequestIsRegistrar = errors.New("request is a registrar request")
	// ErrRequestNotRegistrar means a registrar approval was attempted on an app request.
	ErrRequestNotRegistrar = errors.New("request is not a registrar request")
	// ErrAppIDInRegistrarPrefix means an app id equals a registrar's stem or starts with its
	// prefix, so only that registrar may create it.
	ErrAppIDInRegistrarPrefix = errors.New("app id is reserved by a registrar")
	// ErrRegistrarOwnership means a derived app id is bound to a normal flow or to another
	// registrar, so this registrar may not touch it.
	ErrRegistrarOwnership = errors.New("app id is not owned by this registrar")
	// ErrRegistrarNotFound is returned only by CreateRegistrarBinding when the registrar was
	// deleted since it was read. The service treats it as "no registrar"; no client sees it.
	ErrRegistrarNotFound = errors.New("registrar does not exist")
	// ErrBindingNotFound means no binding has the given app id.
	ErrBindingNotFound = errors.New("app binding does not exist")
	// ErrInvalidPrefix means a registrar prefix breaks the prefix grammar.
	ErrInvalidPrefix = errors.New("invalid prefix")
	// ErrInvalidName means a registrar app name breaks the name grammar or the derived app id
	// is not a valid app id.
	ErrInvalidName = errors.New("invalid name")
)

// RegistrarAppLimit is the most apps one registrar may create.
const RegistrarAppLimit = 50

// Binding is the approved identity of one app: the GitHub workflow identity its keys are
// issued to.
type Binding struct {
	AppID             string
	Repository        string
	RepositoryID      int64
	RepositoryOwnerID int64
	WorkflowPath      string
	Ref               string
	// Environment is empty when the workflow runs with no GitHub environment.
	Environment string
	ApprovedAt  time.Time
	// RegistrarID is set for an app a registrar created, nil for an operator-approved app.
	RegistrarID *int64
	// RegistrarPrefix is the creating registrar's prefix, empty when RegistrarID is nil.
	RegistrarPrefix string
	// AcknowledgedAt is when the operator saw the app. Nil only for a registrar-created app
	// not yet marked seen.
	AcknowledgedAt *time.Time
}

// sameWorkflowIdentity is the five-field identity rule shared by bindings and registrars:
// the same repository id, owner id, workflow path, ref and environment (an absent
// environment only matches an absent one). The display-only repository name is not compared.
func sameWorkflowIdentity(
	repositoryID int64, ownerID int64, workflowPath string, ref string, environment string, claims Claims,
) bool {
	return repositoryID == claims.RepositoryID &&
		ownerID == claims.RepositoryOwnerID &&
		workflowPath == claims.WorkflowPath &&
		ref == claims.Ref &&
		environment == claims.Environment
}

// Matches reports whether claims carry exactly the identity the binding approved.
func (binding Binding) Matches(claims Claims) bool {
	return sameWorkflowIdentity(binding.RepositoryID, binding.RepositoryOwnerID, binding.WorkflowPath,
		binding.Ref, binding.Environment, claims)
}

// WorkflowRegistrar is an approved workflow identity plus the app-id prefix it may create
// apps under. (appidentity.AppRegistrar already means the person-service app creator.)
type WorkflowRegistrar struct {
	ID                int64
	Prefix            string
	Repository        string
	RepositoryID      int64
	RepositoryOwnerID int64
	WorkflowPath      string
	Ref               string
	// Environment is empty when the workflow runs with no GitHub environment.
	Environment string
	ApprovedAt  time.Time
	// AppCount is how many apps the registrar created; filled by ListRegistrars (and 0 by
	// ApproveRegistrarRequest).
	AppCount int
	// AppLimit is RegistrarAppLimit, filled by ListRegistrars and ApproveRegistrarRequest.
	AppLimit int
}

// Matches reports whether claims carry exactly the registrar's workflow identity.
func (registrar WorkflowRegistrar) Matches(claims Claims) bool {
	return sameWorkflowIdentity(registrar.RepositoryID, registrar.RepositoryOwnerID, registrar.WorkflowPath,
		registrar.Ref, registrar.Environment, claims)
}

// DerivedAppState is how a registrar exchange's derived app id is bound.
type DerivedAppState int

const (
	// DerivedAppAbsent means the derived app id has no binding.
	DerivedAppAbsent DerivedAppState = iota
	// DerivedAppOwned means the registrar itself created the bound app.
	DerivedAppOwned
	// DerivedAppForeign means a normal flow or another registrar bound the app id.
	DerivedAppForeign
)

// RegistrarExchangeState is the read-only picture Service.Exchange needs before it calls the
// people directory, so a replayed token or a deterministic refusal never reaches it.
type RegistrarExchangeState struct {
	// Registrar is the registrar matching the claims, nil when none.
	Registrar *WorkflowRegistrar
	// NormalBinding is true when a normal (registrar_id IS NULL) binding has the claims' identity.
	NormalBinding bool
	// DerivedAppID is prefix+name, set only when Registrar is set.
	DerivedAppID string
	// Derived is the derived app id's binding state. Meaningful only when Registrar is set.
	Derived DerivedAppState
	// AppCount is the registrar's current app count (0 without a registrar).
	AppCount int
	// Overlaps is true when the derived id overlaps another bound app id (underscore-extension
	// either way, excluding an exact match).
	Overlaps bool
	// JTIRecorded is true when the claims' jti is already in the replay table.
	JTIRecorded bool
}

// IssuedKey is a freshly issued app key. Plaintext is shown to the caller once and never
// stored.
type IssuedKey struct {
	ID        int64
	Plaintext string
	Hint      string
}

// ResolvedKey is a valid key presented by an app.
type ResolvedKey struct {
	ID    int64
	AppID string
	Hint  string
}

// ExchangeTx is the surface of one token-exchange transaction. Everything it does commits or
// rolls back together, so a replayed token, a half-issued key and a half-filed request cannot
// exist.
type ExchangeTx interface {
	ReplayRecorder

	// LockBinding returns the app's binding with its row locked until the transaction ends,
	// so two exchanges for one app issue their keys one after the other. found is false, and
	// nothing is locked, when the app has no binding.
	LockBinding(ctx context.Context, appID string) (binding Binding, found bool, err error)

	// FilePendingRequest records that appID asked to be bound to claims' workflow identity.
	// The same app and claims again refresh the existing pending row (updated_at, run id)
	// instead of adding one. A new row past MaxPendingRequests is ErrPendingCap. It returns
	// the pending request's id. It takes the same lock ApproveRequest takes, and then
	// re-checks the app's binding: when one now exists (an approval committed since
	// LockBinding found none) it files nothing and returns ErrBindingAppeared.
	FilePendingRequest(ctx context.Context, appID string, claims Claims, now time.Time) (requestID int64, err error)

	// IssueKey marks the app's current unsuperseded, unrevoked keys superseded at now, then
	// inserts a new key, and returns it. The app must have a binding (LockBinding first).
	IssueKey(ctx context.Context, appID string, githubRunID string, now time.Time) (IssuedKey, error)

	// AppIDInRegistrarPrefix reports whether appID equals a registrar's stem or starts with its
	// prefix. The normal flow's fast-path refusal; FilePendingRequest re-checks under the lock.
	AppIDInRegistrarPrefix(ctx context.Context, appID string) (bool, error)

	// FindRegistrarByClaims returns the registrar whose workflow identity the claims carry, or
	// nil when none does.
	FindRegistrarByClaims(ctx context.Context, claims Claims) (*WorkflowRegistrar, error)

	// HasNormalBindingForIdentity reports whether a registrar_id IS NULL binding has the
	// claims' workflow identity.
	HasNormalBindingForIdentity(ctx context.Context, claims Claims) (bool, error)

	// FilePendingRegistrarRequest is FilePendingRequest for a kind "registrar" request (no app
	// id): same lock, same cap (ErrPendingCap), same refresh of an identical pending request, so
	// a repeat returns the same request id. After the lock it re-checks that the identity is
	// neither a registrar nor a normal binding, and returns ErrBindingMismatch if it became
	// either.
	FilePendingRegistrarRequest(ctx context.Context, claims Claims, now time.Time) (requestID int64, err error)

	// CreateRegistrarBinding binds appID (prefix + name) to the registrar's identity with
	// acknowledged_at NULL, under the pending-request lock. It returns ErrRegistrarNotFound
	// when the registrar no longer exists, ErrBindingAppeared when appID is already bound,
	// ErrRegistrarAppLimit at RegistrarAppLimit apps and ErrAppIDOverlap on an underscore
	// overlap with another bound app id.
	CreateRegistrarBinding(ctx context.Context, registrar WorkflowRegistrar, appID string, now time.Time) error
}

// Store is the persistence the token exchange and app-key checks need. It is satisfied by
// *PGStore.
type Store interface {
	// Exchange runs fn in one transaction and commits when it returns nil. fn's error is
	// returned unchanged and rolls the transaction back.
	Exchange(ctx context.Context, fn func(tx ExchangeTx) error) error

	// ReadRegistrarExchange reads, without a lock and without writing, what a registrar
	// exchange needs before it calls the people directory: the registrar matching claims, the
	// derived id's binding state, the app count, the overlap result and whether the claims' jti
	// is already recorded. The store derives the app id (registrar prefix + name) itself because
	// the prefix is only known once the registrar is found; a derived id that is not a valid app
	// id is an error wrapping ErrInvalidName.
	ReadRegistrarExchange(ctx context.Context, claims Claims, name string) (RegistrarExchangeState, error)

	// ResolveKey looks up a presented key and returns it when KeyRecord.Valid holds at now.
	// Any other outcome is an error wrapping ErrInvalidKey; a database failure is returned
	// as is so the caller can fail closed with a 503.
	ResolveKey(ctx context.Context, key string, now time.Time) (ResolvedKey, error)

	// TouchKey records last_used_at = now for the key, but only when it was last recorded
	// more than a minute ago, so a busy key costs at most one write a minute. It reports
	// whether it wrote.
	TouchKey(ctx context.Context, keyID int64, now time.Time) (wrote bool, err error)

	// DeleteExpiredReplay deletes every oidc_replay row whose expires_at is before now and
	// returns how many.
	DeleteExpiredReplay(ctx context.Context, now time.Time) (int64, error)
}

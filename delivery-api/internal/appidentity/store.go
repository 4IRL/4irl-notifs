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
)

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
}

// Matches reports whether claims carry exactly the identity the binding approved: the same
// repository id, owner id, workflow path, ref and environment (an absent environment only
// matches an absent one). The display-only repository name is not compared.
func (binding Binding) Matches(claims Claims) bool {
	return binding.RepositoryID == claims.RepositoryID &&
		binding.RepositoryOwnerID == claims.RepositoryOwnerID &&
		binding.WorkflowPath == claims.WorkflowPath &&
		binding.Ref == claims.Ref &&
		binding.Environment == claims.Environment
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
}

// Store is the persistence the token exchange and app-key checks need. It is satisfied by
// *PGStore.
type Store interface {
	// Exchange runs fn in one transaction and commits when it returns nil. fn's error is
	// returned unchanged and rolls the transaction back.
	Exchange(ctx context.Context, fn func(tx ExchangeTx) error) error

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

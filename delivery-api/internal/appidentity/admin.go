package appidentity

import (
	"context"
	"errors"
	"time"
)

const (
	// RejectedRequestWindow is how long a rejected registration request stays in the
	// operator's list; older rejected requests are no longer shown.
	RejectedRequestWindow = 30 * 24 * time.Hour

	// RequestPending is the status of a request awaiting the operator.
	RequestPending = "pending"
	// RequestRejected is the status of a request the operator rejected.
	RequestRejected = "rejected"

	// RevokedReasonOperator is the reason of a key the operator revoked.
	RevokedReasonOperator = "operator"
	// RevokedReasonSuperseded is the derived reason shown for a key whose supersede grace
	// window has passed. It is never stored: the key simply stopped being valid.
	RevokedReasonSuperseded = "superseded"
)

var (
	// ErrRequestNotFound means no registration request has the given id.
	ErrRequestNotFound = errors.New("registration request does not exist")
	// ErrRequestNotPending means the request was already decided (rejected), so it can no
	// longer be approved.
	ErrRequestNotPending = errors.New("registration request is not pending")
	// ErrAppAlreadyBound means the request's app_id has a binding already.
	ErrAppAlreadyBound = errors.New("app already bound")
	// ErrWorkflowIdentityBound means the request's workflow identity is bound to another app.
	ErrWorkflowIdentityBound = errors.New("workflow identity already bound")
	// ErrAppIDOverlap means the request's app_id is an underscore-extension of an approved app_id
	// ("a_b" next to "a"), or the reverse. Destination names only need the prefix "<app_id>_",
	// so the two apps' name spaces would overlap and one could squat names in the other's.
	ErrAppIDOverlap = errors.New("app id overlaps an approved app id")
	// ErrKeyNotFound means no app key has the given id.
	ErrKeyNotFound = errors.New("app key does not exist")
)

// Request is one registration request in the operator's approval queue.
type Request struct {
	ID                int64
	AppID             string
	Repository        string
	RepositoryID      int64
	RepositoryOwnerID int64
	WorkflowPath      string
	Ref               string
	// Environment is empty when the workflow ran with no GitHub environment.
	Environment string
	GitHubRunID string
	// Status is RequestPending or RequestRejected.
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
	// DecidedAt is set only on a rejected request.
	DecidedAt *time.Time
}

// Key is the operator's view of one app key. It never carries the key itself.
type Key struct {
	ID    int64
	AppID string
	// Hint is the display hint ("dak_" plus the first random characters).
	Hint         string
	State        KeyState
	CreatedAt    time.Time
	SupersededAt *time.Time
	// RevokedAt is when the key stopped working, when it is State KeyRevoked: the revoke time,
	// or the end of the supersede grace window.
	RevokedAt *time.Time
	// RevokedReason is RevokedReasonOperator or RevokedReasonSuperseded, set when State is
	// KeyRevoked and empty otherwise.
	RevokedReason string
	LastUsedAt    *time.Time
}

// BindingWithKeys is an approved binding with every key issued under it, newest first.
type BindingWithKeys struct {
	Binding Binding
	Keys    []Key
}

// storedKey is the columns of an app_keys row the operator view derives a Key from.
type storedKey struct {
	ID            int64
	AppID         string
	Hint          string
	CreatedAt     time.Time
	SupersededAt  *time.Time
	RevokedAt     *time.Time
	RevokedReason *string
	LastUsedAt    *time.Time
}

// toKey derives the operator view of the key at now. A key past its supersede grace window
// reads as revoked with reason "superseded", stopped at the end of the window.
func (stored storedKey) toKey(now time.Time) Key {
	key := Key{
		ID:           stored.ID,
		AppID:        stored.AppID,
		Hint:         stored.Hint,
		CreatedAt:    stored.CreatedAt,
		SupersededAt: stored.SupersededAt,
		LastUsedAt:   stored.LastUsedAt,
	}
	record := KeyRecord{SupersededAt: stored.SupersededAt, RevokedAt: stored.RevokedAt}
	key.State = record.State(now)
	if key.State != KeyRevoked {
		return key
	}
	if stored.RevokedAt != nil {
		key.RevokedAt = stored.RevokedAt
		key.RevokedReason = RevokedReasonOperator
		if stored.RevokedReason != nil {
			key.RevokedReason = *stored.RevokedReason
		}
		return key
	}
	graceEnd := stored.SupersededAt.Add(SupersededGrace)
	key.RevokedAt = &graceEnd
	key.RevokedReason = RevokedReasonSuperseded
	return key
}

// AdminStore is the persistence behind the operator identity API. It is satisfied by
// *PGStore.
type AdminStore interface {
	// ListRequests returns every pending request, then the requests rejected since
	// now-RejectedRequestWindow; newest first within each group. Never nil.
	ListRequests(ctx context.Context, now time.Time) ([]Request, error)

	// ApproveRequest creates the binding from the pending request's claims and deletes the
	// request, in one transaction, along with every other pending request for the same app id
	// or the same workflow identity (they can never be approved now). Rejected requests stay.
	// It returns ErrRequestNotFound, ErrRequestNotPending, ErrAppIDOverlap,
	// ErrAppAlreadyBound or ErrWorkflowIdentityBound, and changes nothing in those cases.
	ApproveRequest(ctx context.Context, requestID int64, now time.Time) (Binding, error)

	// RejectRequest marks a pending request rejected at now. A request that is already
	// rejected is returned unchanged. ErrRequestNotFound for an unknown id.
	RejectRequest(ctx context.Context, requestID int64, now time.Time) (Request, error)

	// ListBindings returns every binding with its keys. Never nil.
	ListBindings(ctx context.Context, now time.Time) ([]BindingWithKeys, error)

	// RevokeKey revokes the key at now. A key that is already revoked, or past its grace
	// window, is returned unchanged. ErrKeyNotFound for an unknown id.
	RevokeKey(ctx context.Context, keyID int64, now time.Time) (Key, error)

	// Unbind deletes the app's binding and with it every key issued under it. It reports
	// whether a binding existed. Objects the app owns are not touched.
	Unbind(ctx context.Context, appID string) (deleted bool, err error)
}

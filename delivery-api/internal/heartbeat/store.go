package heartbeat

import (
	"context"
	"time"
)

// Store is the persistence surface for heartbeat checks. Every method that takes
// a `now` uses it for the check's timestamps, so callers (and tests) own the
// clock. State-changing methods apply the pure state machine under a row lock
// and write the outbox fields in the same transaction.
type Store interface {
	// Create persists a new check in state new with its anchor at now. Each
	// destination name is resolved to an id in the same transaction: an unknown
	// name returns registry.ErrUnknownDestination with nothing inserted, and a duplicate
	// (app_id, name) returns ErrCheckExists.
	Create(ctx context.Context, check NewCheck, now time.Time) (StoredCheck, error)
	// Get returns the check with that id, or ErrCheckNotFound.
	Get(ctx context.Context, id int64) (StoredCheck, error)
	// List returns every check ordered by app_id, then name, never nil.
	List(ctx context.Context) ([]StoredCheck, error)
	// Patch merges the patch into the check and replaces its period, grace and
	// destination set, all under the check's row lock: the merge reads the locked
	// row, so a concurrent Patch of other fields is never reverted. The merged
	// result is validated before anything is written (a *ValidationError). It
	// returns ErrCheckNotFound for a missing id and registry.ErrUnknownDestination for an
	// unknown name.
	Patch(ctx context.Context, id int64, patch CheckPatch, now time.Time) (StoredCheck, error)
	// Delete removes the check (its destination links cascade) and reports
	// whether a row existed; deleting a missing check is not an error.
	Delete(ctx context.Context, id int64) (bool, error)
	// Pause pauses the check and clears any unflushed alert. It returns
	// ErrCheckNotFound for a missing id; pausing a paused check is a no-op.
	Pause(ctx context.Context, id int64, now time.Time) (StoredCheck, error)
	// Resume resumes a paused check and restarts its deadline clock at now,
	// emitting nothing. It returns ErrCheckNotFound for a missing id.
	Resume(ctx context.Context, id int64, now time.Time) (StoredCheck, error)
	// RecordPing applies a ping of the given kind (PingSuccess or PingFail) to
	// the check named by app_id and name. An unknown check returns
	// ErrCheckNotFound; an unknown kind returns a *ValidationError. The returned
	// check carries any alert the ping emitted in PendingAlert / AlertSeq.
	RecordPing(ctx context.Context, appID string, name string, kind string, now time.Time) (StoredCheck, error)
	// AdvanceDue applies the checker tick: every unpaused new or up check past
	// its deadline goes down and emits a down alert. It returns the checks that
	// changed, ordered by id.
	AdvanceDue(ctx context.Context, now time.Time) ([]StoredCheck, error)
	// ListPendingAlerts returns every check with an unflushed alert, ordered by
	// id, never nil.
	ListPendingAlerts(ctx context.Context) ([]StoredCheck, error)
	// ClearPendingAlert clears the check's pending alert only if its AlertSeq
	// still equals alertSeq, so a newer alert is never cleared by a stale flush.
	// It reports whether a pending alert was cleared.
	ClearPendingAlert(ctx context.Context, id int64, alertSeq int) (bool, error)
}

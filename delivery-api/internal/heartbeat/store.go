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
	// name, or a destination the check's app_id may not use (see
	// registry.DestinationUsableBy), returns registry.ErrUnknownDestination with
	// nothing inserted, and a duplicate (app_id, name) returns ErrCheckExists.
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
	// unknown name or a destination the check's app_id may not use.
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

	// RecordProbeResult applies one probe result under the check's row lock. It writes
	// nothing and returns applied=false (and a zero check) for a missing check, a push
	// check, a configVersion that is no longer the stored one, or a result the state
	// machine denies (a paused check, a bad reason). Any accepted result, alerting or
	// not, is saved and returns applied=true with the reloaded check.
	RecordProbeResult(
		ctx context.Context, checkID int64, configVersion int64, result ProbeResult, now, holdOffUntil time.Time,
	) (check StoredCheck, applied bool, err error)
	// ClaimDueProbes hands out up to limit unpaused probes whose next_probe_at has
	// arrived, earliest first (down checks included), and advances each claimed
	// row's next_probe_at to now plus its interval in the same statement, so a row is
	// claimed once per interval. A limit below 1 is a *ValidationError.
	ClaimDueProbes(ctx context.Context, now time.Time, limit int) ([]ProbeClaim, error)

	// The *OwnedBy methods below confine a write to the checks the app appID owns
	// (owner_kind 'app', owner_app_id appID): the owner is part of the row lock
	// (or of the one DELETE statement), so a check owned by anyone else, or absent,
	// returns ErrCheckNotFound (DeleteOwnedBy reports false) and is never touched.

	// ListByOwner returns the checks the app appID owns, ordered by name, never nil.
	ListByOwner(ctx context.Context, appID string) ([]StoredCheck, error)
	// PatchOwnedBy is Patch for a check the app appID owns.
	PatchOwnedBy(ctx context.Context, id int64, appID string, patch CheckPatch, now time.Time) (StoredCheck, error)
	// DeleteOwnedBy is Delete for a check the app appID owns; it reports whether a
	// row was removed.
	DeleteOwnedBy(ctx context.Context, id int64, appID string) (bool, error)
	// PauseOwnedBy is Pause for a check the app appID owns.
	PauseOwnedBy(ctx context.Context, id int64, appID string, now time.Time) (StoredCheck, error)
	// ResumeOwnedBy is Resume for a check the app appID owns.
	ResumeOwnedBy(ctx context.Context, id int64, appID string, now time.Time) (StoredCheck, error)
}

//go:build integration

package message_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

func heldCount(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool) int {
	testInstance.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE held_at IS NOT NULL`).Scan(&count); err != nil {
		testInstance.Fatalf("count held: %v", err)
	}
	return count
}

func TestCancelHeldMessageWithoutDeliverAtClearsHeldAtAndIsIdempotent(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil || held.Envelope.DeliverAt != nil {
		testInstance.Fatalf("hold = deliver_at %v, err %v; want an unscheduled hold", held.Envelope.DeliverAt, err)
	}
	cancelAt := scheduleBase.Add(time.Minute)

	first, err := store.CancelMessage(ctx, held.ID, "u4i", cancelAt)
	want := message.CancelOutcome{MessageID: held.ID, Status: message.StatusCancelled, Cancelled: true, Transitioned: true, Found: true}
	if err != nil || first != want {
		testInstance.Fatalf("first cancel = %+v, err %v; want %+v", first, err, want)
	}
	stored, err := store.GetMessage(ctx, held.ID)
	if err != nil {
		testInstance.Fatalf("GetMessage: %v", err)
	}
	if stored.HeldAt != nil || stored.CancelledAt == nil || stored.HeldCancelledAt == nil ||
		!stored.CancelledAt.Equal(cancelAt) || !stored.HeldCancelledAt.Equal(cancelAt) {
		testInstance.Fatalf("after cancel held_at %v, cancelled_at %v, held_cancelled_at %v", stored.HeldAt, stored.CancelledAt, stored.HeldCancelledAt)
	}
	if got := message.DeriveStatus(stored.Deliveries, stored.State()); got != message.StatusCancelled {
		testInstance.Fatalf("status = %q, want cancelled (not unrouted)", got)
	}
	if got := heldCount(ctx, testInstance, pool); got != 0 {
		testInstance.Fatalf("held count after cancel = %d, want 0", got)
	}

	again, err := store.CancelMessage(ctx, held.ID, "u4i", cancelAt.Add(time.Hour))
	wantAgain := message.CancelOutcome{MessageID: held.ID, Status: message.StatusCancelled, Cancelled: true, Found: true}
	if err != nil || again != wantAgain {
		testInstance.Fatalf("re-cancel = %+v, err %v; want %+v", again, err, wantAgain)
	}
	stored, _ = store.GetMessage(ctx, held.ID)
	if !stored.CancelledAt.Equal(cancelAt) {
		testInstance.Fatalf("re-cancel moved cancelled_at to %v, want %v", stored.CancelledAt, cancelAt)
	}
}

func TestCancelHeldMessageByKeyAndAdminCancelByID(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "by-key"), heldCaps(5, 10), scheduleBase); err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	byKey, err := store.CancelMessageByKey(ctx, "u4i", "by-key", scheduleBase)
	if err != nil || !byKey.Cancelled || !byKey.Transitioned {
		testInstance.Fatalf("cancel by key = %+v, err %v; want a transition", byKey, err)
	}

	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "by-admin"), heldCaps(5, 10), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	admin, err := store.AdminCancelMessage(ctx, held.ID, scheduleBase)
	if err != nil || !admin.Cancelled || !admin.Transitioned || admin.Status != message.StatusCancelled {
		testInstance.Fatalf("admin cancel = %+v, err %v; want a transition", admin, err)
	}
}

func TestCancelHeldMessageIsScopedToItsApp(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	outcome, err := store.CancelMessage(ctx, held.ID, "other", scheduleBase)
	if err != nil || outcome.Found {
		testInstance.Fatalf("cross-app cancel = %+v, err %v; want not found", outcome, err)
	}
	if got := heldCount(ctx, testInstance, pool); got != 1 {
		testInstance.Fatalf("held count = %d, want 1", got)
	}
}

func TestCancelReleaseFailedMessageChangesNothing(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	// The release transaction does not exist yet; set the failed state directly.
	if _, execErr := pool.Exec(ctx,
		`UPDATE messages SET held_at = NULL, release_failed_at = $2, release_failed_reason = 'release_invalid' WHERE id = $1::text::uuid`,
		held.ID, scheduleBase); execErr != nil {
		testInstance.Fatalf("mark release failed: %v", execErr)
	}

	outcome, err := store.CancelMessage(ctx, held.ID, "u4i", scheduleBase.Add(time.Hour))
	want := message.CancelOutcome{MessageID: held.ID, Status: message.StatusReleaseFailed, Found: true}
	if err != nil || outcome != want {
		testInstance.Fatalf("cancel = %+v, err %v; want %+v", outcome, err, want)
	}
	stored, err := store.GetMessage(ctx, held.ID)
	if err != nil || stored.CancelledAt != nil || stored.HeldCancelledAt != nil || stored.ReleaseFailedAt == nil {
		testInstance.Fatalf("release-failed row changed: %+v, err %v", stored, err)
	}

	replay, duplicate, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil || !duplicate || replay.ID != held.ID {
		testInstance.Fatalf("replay of a release-failed key = id %q, duplicate %v, err %v; want the original, true, nil", replay.ID, duplicate, err)
	}
}

func TestCancelledHeldMessageFreesCapRoom(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(1, 1), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-2"), heldCaps(1, 1), scheduleBase); !errors.Is(err, message.ErrTooManyHeld) {
		testInstance.Fatalf("hold at the cap err = %v, want ErrTooManyHeld", err)
	}
	if _, err := store.CancelMessage(ctx, held.ID, "u4i", scheduleBase); err != nil {
		testInstance.Fatalf("cancel: %v", err)
	}
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-2"), heldCaps(1, 1), scheduleBase); err != nil {
		testInstance.Fatalf("hold after cancel freed the cap: %v", err)
	}
	if got := heldCount(ctx, testInstance, pool); got != 1 {
		testInstance.Fatalf("held count = %d, want 1", got)
	}
}

func TestHeldReplayWithChangedContentAndDeliverAtIsAnUnchangedDuplicate(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	first, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}

	changed := heldEnvelopeFor("u4i", "held-1")
	changed.Channel = "other_channel"
	deliverAt := scheduleBase.Add(48 * time.Hour)
	changed.DeliverAt = &deliverAt
	again, duplicate, err := store.InsertHeldMessage(ctx, changed, heldCaps(5, 10), scheduleBase.Add(time.Hour))
	if err != nil || !duplicate || again.ID != first.ID {
		testInstance.Fatalf("replay = id %q, duplicate %v, err %v; want the original, true, nil", again.ID, duplicate, err)
	}
	stored, err := store.GetMessage(ctx, first.ID)
	if err != nil || stored.Envelope.Channel != first.Envelope.Channel || stored.Envelope.DeliverAt != nil || stored.HeldAt == nil {
		testInstance.Fatalf("stored message changed: %+v, err %v", stored, err)
	}
	if got := heldCount(ctx, testInstance, pool); got != 1 {
		testInstance.Fatalf("held count = %d, want 1", got)
	}
}

func TestHeldCancelWaitsForTheRowLock(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		testInstance.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, execErr := tx.Exec(ctx, `SELECT 1 FROM messages WHERE id = $1::text::uuid FOR UPDATE`, held.ID); execErr != nil {
		testInstance.Fatalf("lock row: %v", execErr)
	}

	done := make(chan message.CancelOutcome, 1)
	go func() {
		outcome, _ := store.CancelMessage(ctx, held.ID, "u4i", scheduleBase)
		done <- outcome
	}()
	select {
	case outcome := <-done:
		testInstance.Fatalf("cancel returned %+v while another transaction held the row lock", outcome)
	case <-time.After(300 * time.Millisecond):
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		testInstance.Fatalf("commit: %v", commitErr)
	}
	select {
	case outcome := <-done:
		if !outcome.Cancelled || !outcome.Transitioned {
			testInstance.Fatalf("cancel after the lock was released = %+v, want a transition", outcome)
		}
	case <-time.After(5 * time.Second):
		testInstance.Fatalf("cancel did not finish after the row lock was released")
	}
}

func TestCancelScheduledAndUnroutedHeldMessage(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	deliverAt := scheduleBase.Add(24 * time.Hour)
	envelope := heldEnvelopeFor("u4i", "held-future")
	envelope.DeliverAt = &deliverAt
	held, _, err := store.InsertHeldMessage(ctx, envelope, heldCaps(5, 10), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	outcome, err := store.CancelMessage(ctx, held.ID, "u4i", scheduleBase)
	if err != nil || !outcome.Cancelled || !outcome.Transitioned || outcome.Status != message.StatusCancelled {
		testInstance.Fatalf("cancel = %+v, err %v; want a transition", outcome, err)
	}
	if got := heldCount(ctx, testInstance, pool); got != 0 {
		testInstance.Fatalf("held count = %d, want 0", got)
	}
}

func TestReplayAfterHeldCancelIsADuplicateAndNewKeyIsAccepted(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	if _, cancelErr := store.CancelMessage(ctx, held.ID, "u4i", scheduleBase); cancelErr != nil {
		testInstance.Fatalf("cancel: %v", cancelErr)
	}
	again, duplicate, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil || !duplicate || again.ID != held.ID || again.HeldAt != nil {
		testInstance.Fatalf("same-key replay after cancel = %+v, duplicate %v, err %v; want the cancelled original", again, duplicate, err)
	}
	if _, duplicateNew, newErr := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-2"), heldCaps(5, 10), scheduleBase); newErr != nil || duplicateNew {
		testInstance.Fatalf("new key after cancel = duplicate %v, err %v; want a fresh hold", duplicateNew, newErr)
	}
	if got := heldCount(ctx, testInstance, pool); got != 1 {
		testInstance.Fatalf("held count = %d, want 1", got)
	}
}

func TestRetentionSweepNeverDeletesAStillHeldMessage(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase); err != nil {
		testInstance.Fatalf("hold: %v", err)
	}
	deleted, err := store.DeleteExpired(ctx, scheduleBase.Add(365*24*time.Hour))
	if err != nil || deleted != 0 {
		testInstance.Fatalf("DeleteExpired = %d, err %v; want 0 (a live hold is exempt)", deleted, err)
	}
	if got := heldCount(ctx, testInstance, pool); got != 1 {
		testInstance.Fatalf("held count = %d, want 1", got)
	}
}

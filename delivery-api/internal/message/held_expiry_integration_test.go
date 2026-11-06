//go:build integration

package message_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

const (
	retentionWindow = 30 * 24 * time.Hour
	heldTTLWindow   = 7 * 24 * time.Hour
)

// setMessageColumn sets one timestamp column on a message row. The column name
// is a test-controlled constant, never user input.
func setMessageColumn(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, messageID string, column string, value time.Time,
) {
	testInstance.Helper()
	statement := fmt.Sprintf(`UPDATE messages SET %s = $2 WHERE id = $1::text::uuid`, column)
	if _, execErr := pool.Exec(ctx, statement, messageID, value); execErr != nil {
		testInstance.Fatalf("set %s: %v", column, execErr)
	}
}

func messageExists(ctx context.Context, store *message.PGStore, messageID string) bool {
	_, err := store.GetMessage(ctx, messageID)
	return err == nil
}

func holdAt(
	ctx context.Context, testInstance *testing.T, store *message.PGStore, key string, now time.Time,
) message.StoredMessage {
	testInstance.Helper()
	held, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", key), heldCaps(50, 100), now)
	if err != nil {
		testInstance.Fatalf("hold %s: %v", key, err)
	}
	return held
}

func TestDeleteExpiredHeldAgesFromHeldAtNotCreatedAt(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	realNow := time.Now().UTC()
	heldCutoff := realNow.Add(-heldTTLWindow)
	oldHold := holdAt(ctx, testInstance, store, "old-hold", realNow)
	freshHoldOldCreate := holdAt(ctx, testInstance, store, "fresh-hold-old-create", realNow)
	insideTTL := holdAt(ctx, testInstance, store, "inside-ttl", realNow)
	oldCreated := realNow.Add(-200 * 24 * time.Hour)
	setMessageColumn(ctx, testInstance, pool, oldHold.ID, "created_at", oldCreated)
	setMessageColumn(ctx, testInstance, pool, oldHold.ID, "held_at", heldCutoff.Add(-time.Hour))
	setMessageColumn(ctx, testInstance, pool, freshHoldOldCreate.ID, "created_at", oldCreated)
	setMessageColumn(ctx, testInstance, pool, insideTTL.ID, "held_at", heldCutoff.Add(time.Hour))

	// The 30-day retention sweep never touches a live hold, however old.
	if deleted, err := store.DeleteExpired(ctx, realNow.Add(-retentionWindow)); err != nil || deleted != 0 {
		testInstance.Fatalf("DeleteExpired = %d, err %v; want 0 (live holds are exempt)", deleted, err)
	}
	deleted, err := store.DeleteExpiredHeld(ctx, heldCutoff)
	if err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpiredHeld = %d, err %v; want 1 (only the hold past the TTL)", deleted, err)
	}
	if messageExists(ctx, store, oldHold.ID) {
		testInstance.Error("a hold past the TTL survived")
	}
	if !messageExists(ctx, store, freshHoldOldCreate.ID) {
		testInstance.Error("a hold with created_at 200 days old but held_at now was deleted (aged from created_at)")
	}
	if !messageExists(ctx, store, insideTTL.ID) {
		testInstance.Error("a hold inside the TTL was deleted")
	}
}

func TestReleasedAndReleaseFailedMessagesAgeFromTheirOwnTimestamp(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	realNow := time.Now().UTC()
	cutoff := realNow.Add(-retentionWindow)
	oldCreated := realNow.Add(-200 * 24 * time.Hour)

	released := holdAt(ctx, testInstance, store, "released", realNow)
	failed := holdAt(ctx, testInstance, store, "failed", realNow)
	for _, id := range []string{released.ID, failed.ID} {
		setMessageColumn(ctx, testInstance, pool, id, "created_at", oldCreated)
	}
	// Simulate the releaser: held_at cleared and the outcome stamped.
	if _, execErr := pool.Exec(ctx,
		`UPDATE messages SET held_at = NULL, released_at = $2 WHERE id = $1::text::uuid`, released.ID, realNow); execErr != nil {
		testInstance.Fatalf("mark released: %v", execErr)
	}
	if _, execErr := pool.Exec(ctx,
		`UPDATE messages SET held_at = NULL, release_failed_at = $2, release_failed_reason = 'release_invalid'
		 WHERE id = $1::text::uuid`, failed.ID, realNow); execErr != nil {
		testInstance.Fatalf("mark release failed: %v", execErr)
	}

	if deleted, err := store.DeleteExpired(ctx, cutoff); err != nil || deleted != 0 {
		testInstance.Fatalf("DeleteExpired = %d, err %v; want 0 (created_at is old but the release is fresh)", deleted, err)
	}
	if !messageExists(ctx, store, released.ID) || !messageExists(ctx, store, failed.ID) {
		testInstance.Fatal("a freshly released or release-failed message was deleted")
	}

	setMessageColumn(ctx, testInstance, pool, released.ID, "released_at", cutoff.Add(-time.Hour))
	if deleted, err := store.DeleteExpired(ctx, cutoff); err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpired after aging released_at = %d, err %v; want 1", deleted, err)
	}
	if messageExists(ctx, store, released.ID) || !messageExists(ctx, store, failed.ID) {
		testInstance.Fatal("only the released message past its own cutoff should be gone")
	}

	setMessageColumn(ctx, testInstance, pool, failed.ID, "release_failed_at", cutoff.Add(-time.Hour))
	if deleted, err := store.DeleteExpired(ctx, cutoff); err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpired after aging release_failed_at = %d, err %v; want 1", deleted, err)
	}
	if messageExists(ctx, store, failed.ID) {
		testInstance.Error("the release-failed message survived past its own cutoff")
	}
}

func TestCancelledHeldMessageAgesFromCancelTime(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	realNow := time.Now().UTC()
	cutoff := realNow.Add(-retentionWindow)
	held := holdAt(ctx, testInstance, store, "cancelled-hold", realNow)
	if outcome, err := store.CancelMessage(ctx, held.ID, "u4i", realNow); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("cancel = %+v, err %v", outcome, err)
	}
	setMessageColumn(ctx, testInstance, pool, held.ID, "created_at", realNow.Add(-200*24*time.Hour))

	if deleted, err := store.DeleteExpired(ctx, cutoff); err != nil || deleted != 0 {
		testInstance.Fatalf("DeleteExpired = %d, err %v; want 0 (cancelled now, created long ago)", deleted, err)
	}
	if !messageExists(ctx, store, held.ID) {
		testInstance.Fatal("a freshly cancelled held message was deleted")
	}
	setMessageColumn(ctx, testInstance, pool, held.ID, "cancelled_at", cutoff.Add(-time.Hour))
	if deleted, err := store.DeleteExpired(ctx, cutoff); err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpired after aging cancelled_at = %d, err %v; want 1", deleted, err)
	}
}

func TestDeleteExpiredHeldBatchesAndRejectsBadArguments(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	realNow := time.Now().UTC()
	heldCutoff := realNow.Add(-heldTTLWindow)
	for index := range 5 {
		held := holdAt(ctx, testInstance, store, fmt.Sprintf("batch-%d", index), realNow)
		setMessageColumn(ctx, testInstance, pool, held.ID, "held_at", heldCutoff.Add(-time.Hour))
	}

	if _, err := store.DeleteExpiredHeldInBatches(ctx, heldCutoff, 0); err == nil {
		testInstance.Error("batch size 0 was accepted")
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.DeleteExpiredHeldInBatches(canceledCtx, heldCutoff, 2); !errors.Is(err, context.Canceled) {
		testInstance.Errorf("canceled context err = %v, want context.Canceled", err)
	}
	if got := heldCount(ctx, testInstance, pool); got != 5 {
		testInstance.Fatalf("held count after rejected calls = %d, want 5", got)
	}
	deleted, err := store.DeleteExpiredHeldInBatches(ctx, heldCutoff, 2)
	if err != nil || deleted != 5 {
		testInstance.Fatalf("DeleteExpiredHeldInBatches(2) = %d, err %v; want 5 across 3 batches", deleted, err)
	}
	if got := heldCount(ctx, testInstance, pool); got != 0 {
		testInstance.Errorf("held count = %d, want 0", got)
	}
}

func TestDeleteExpiredHeldNeverDeletesAReleasedMessage(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	realNow := time.Now().UTC()
	heldCutoff := realNow.Add(-heldTTLWindow)
	released := holdAt(ctx, testInstance, store, "released-late", realNow)
	// Simulate the releaser winning: held_at cleared, even though the old
	// held_at would have been past the TTL.
	setMessageColumn(ctx, testInstance, pool, released.ID, "created_at", heldCutoff.Add(-time.Hour))
	if _, execErr := pool.Exec(ctx,
		`UPDATE messages SET held_at = NULL, released_at = $2 WHERE id = $1::text::uuid`, released.ID, realNow); execErr != nil {
		testInstance.Fatalf("mark released: %v", execErr)
	}
	if deleted, err := store.DeleteExpiredHeld(ctx, realNow.Add(time.Hour)); err != nil || deleted != 0 {
		testInstance.Fatalf("DeleteExpiredHeld = %d, err %v; want 0 (a released message is never held-expired)", deleted, err)
	}
	if !messageExists(ctx, store, released.ID) {
		testInstance.Fatal("a released message was deleted by the held sweep")
	}
}

func TestDeleteExpiredHeldStopsAfterTheBatchCap(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	realNow := time.Now().UTC()
	heldCutoff := realNow.Add(-heldTTLWindow)
	extra := 3
	total := message.DeleteExpiredMaxBatches + extra
	for index := range total {
		held, _, err := store.InsertHeldMessage(ctx,
			heldEnvelopeFor("u4i", fmt.Sprintf("cap-%d", index)), heldCaps(total, total), realNow)
		if err != nil {
			testInstance.Fatalf("hold %d: %v", index, err)
		}
		setMessageColumn(ctx, testInstance, pool, held.ID, "held_at", heldCutoff.Add(-time.Hour))
	}
	deleted, err := store.DeleteExpiredHeldInBatches(ctx, heldCutoff, 1)
	if err != nil || deleted != int64(message.DeleteExpiredMaxBatches) {
		testInstance.Fatalf("DeleteExpiredHeldInBatches(1) = %d, err %v; want the cap %d", deleted, err, message.DeleteExpiredMaxBatches)
	}
	if got := heldCount(ctx, testInstance, pool); got != extra {
		testInstance.Errorf("held count after one capped call = %d, want %d left for the next sweep", got, extra)
	}
}

func TestDeleteExpiredHeldUsesTheHeldIndex(testInstance *testing.T) {
	ctx, _, _, pool := newMessageStore(testInstance)
	plan := explainPlan(ctx, testInstance, pool, message.DeleteExpiredHeldSQL, time.Now().UTC(), 500)
	if !strings.Contains(plan, "messages_held_idx") {
		testInstance.Errorf("held delete does not use messages_held_idx:\n%s", plan)
	}
}

func TestDeleteExpiredHeldSkipsRowsLockedByAnotherTransaction(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	realNow := time.Now().UTC()
	heldCutoff := realNow.Add(-heldTTLWindow)
	locked := holdAt(ctx, testInstance, store, "locked", realNow)
	free := holdAt(ctx, testInstance, store, "free", realNow)
	for _, id := range []string{locked.ID, free.ID} {
		setMessageColumn(ctx, testInstance, pool, id, "held_at", heldCutoff.Add(-time.Hour))
	}

	transaction, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		testInstance.Fatalf("begin: %v", beginErr)
	}
	defer func() { _ = transaction.Rollback(ctx) }()
	if _, lockErr := transaction.Exec(ctx,
		`SELECT 1 FROM messages WHERE id = $1::text::uuid FOR UPDATE`, locked.ID); lockErr != nil {
		testInstance.Fatalf("lock: %v", lockErr)
	}

	deleted, err := store.DeleteExpiredHeld(ctx, heldCutoff)
	if err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpiredHeld = %d, err %v; want 1 (the unlocked row only)", deleted, err)
	}
	if !messageExists(ctx, store, locked.ID) {
		testInstance.Error("a row locked by another transaction was deleted")
	}
	if messageExists(ctx, store, free.ID) {
		testInstance.Error("the unlocked expired hold survived")
	}

	if commitErr := transaction.Commit(ctx); commitErr != nil {
		testInstance.Fatalf("commit: %v", commitErr)
	}
	if deleted, err = store.DeleteExpiredHeld(ctx, heldCutoff); err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpiredHeld after unlock = %d, err %v; want 1", deleted, err)
	}
}

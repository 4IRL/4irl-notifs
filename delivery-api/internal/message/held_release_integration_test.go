//go:build integration

package message_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

// routeHeld stores a route-mode held message (no to_destination/to_recipient)
// for appID/channel, held at heldAt.
func routeHeld(
	ctx context.Context, testInstance *testing.T, store *message.PGStore,
	appID string, channel string, key string, heldAt time.Time, deliverAt *time.Time,
) message.StoredMessage {
	testInstance.Helper()
	envelope := heldEnvelopeFor(appID, key)
	envelope.Channel = channel
	envelope.ToDestination = ""
	envelope.DeliverAt = deliverAt
	stored, duplicate, err := store.InsertHeldMessage(ctx, envelope, heldCaps(1000, roomyTotalCap), heldAt)
	if err != nil || duplicate {
		testInstance.Fatalf("InsertHeldMessage %s = duplicate %v, err %v", key, duplicate, err)
	}
	return stored
}

func countDeliveries(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, messageID string) int {
	testInstance.Helper()
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM message_deliveries WHERE message_id = $1::text::uuid`, messageID).Scan(&count); err != nil {
		testInstance.Fatalf("count deliveries: %v", err)
	}
	return count
}

func requireStillHeld(ctx context.Context, testInstance *testing.T, store *message.PGStore, id string) {
	testInstance.Helper()
	got := mustGet(ctx, testInstance, store, id)
	if got.HeldAt == nil || got.ReleasedAt != nil || got.ReleaseFailedAt != nil || len(got.Deliveries) != 0 {
		testInstance.Fatalf("message = %+v, want untouched and held", got)
	}
}

func TestReleaseHeldMessageInsertsPendingDeliveriesAndSetsReleasedAt(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	now := scheduleBase.Add(200 * 24 * time.Hour)
	held := routeHeld(ctx, testInstance, store, "u4i", "alerts", "rel-1", scheduleBase, nil)

	result, err := store.ReleaseHeldMessage(ctx, held.ID, []message.DeliveryTarget{pager, audit}, now)
	if err != nil || result != message.HeldReleased {
		testInstance.Fatalf("ReleaseHeldMessage = %q, err %v; want released", result, err)
	}
	got := mustGet(ctx, testInstance, store, held.ID)
	if got.HeldAt != nil || got.ReleasedAt == nil || !got.ReleasedAt.Equal(now) {
		testInstance.Errorf("held_at %v, released_at %v; want cleared and %v", got.HeldAt, got.ReleasedAt, now)
	}
	if !slices.Equal(deliveryNames(got), []string{"audit", "pager"}) || !allDeliveriesIn(got, message.StatePending) {
		testInstance.Errorf("deliveries = %+v, want one pending row per target", got.Deliveries)
	}
	for _, delivery := range got.Deliveries {
		if !delivery.NextAttemptAt.Equal(now) || !delivery.BudgetStartedAt.Equal(now) {
			testInstance.Errorf("delivery %+v, want next_attempt_at and budget_started_at = now", delivery)
		}
	}
}

func TestReleaseHeldMessageHonorsDeliverAt(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	now := scheduleBase.Add(time.Hour)
	future := now.Add(3 * time.Hour)
	past := now.Add(-time.Minute)
	futureMessage := routeHeld(ctx, testInstance, store, "u4i", "alerts", "rel-future", scheduleBase, &future)
	pastMessage := routeHeld(ctx, testInstance, store, "u4i", "alerts", "rel-past", scheduleBase, &past)

	result, err := store.ReleaseHeldMessage(ctx, futureMessage.ID, []message.DeliveryTarget{pager}, now)
	if err != nil || result != message.HeldScheduled {
		testInstance.Fatalf("future release = %q, err %v; want scheduled", result, err)
	}
	got := mustGet(ctx, testInstance, store, futureMessage.ID)
	if !allDeliveriesIn(got, message.StateScheduled) || !got.Deliveries[0].NextAttemptAt.Equal(future) || got.ReleasedAt == nil {
		testInstance.Errorf("future message = %+v, want scheduled at deliver_at and released_at set", got)
	}

	result, err = store.ReleaseHeldMessage(ctx, pastMessage.ID, []message.DeliveryTarget{pager}, now)
	if err != nil || result != message.HeldReleased {
		testInstance.Fatalf("past release = %q, err %v; want released", result, err)
	}
	if got := mustGet(ctx, testInstance, store, pastMessage.ID); !allDeliveriesIn(got, message.StatePending) {
		testInstance.Errorf("past deliveries = %+v, want pending", got.Deliveries)
	}
}

func TestReleaseHeldMessageRefusesEmptyTargetsAndBadArguments(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	held := routeHeld(ctx, testInstance, store, "u4i", "alerts", "rel-args", scheduleBase, nil)

	if result, err := store.ReleaseHeldMessage(ctx, held.ID, nil, scheduleBase); err == nil || result != "" {
		testInstance.Errorf("empty targets = %q, err %v; want an error", result, err)
	}
	if _, err := store.ReleaseHeldMessage(ctx, held.ID, []message.DeliveryTarget{pager}, time.Time{}); err == nil {
		testInstance.Error("zero now: want an error")
	}
	if _, err := store.ReleaseHeldMessage(ctx, "not-a-uuid", []message.DeliveryTarget{pager}, scheduleBase); err == nil {
		testInstance.Error("bad id: want an error")
	}
	requireStillHeld(ctx, testInstance, store, held.ID)
	if got := countDeliveries(ctx, testInstance, pool, held.ID); got != 0 {
		testInstance.Errorf("deliveries = %d, want 0", got)
	}
}

func TestConcurrentHeldReleasesGiveExactlyOneRelease(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	targets := []message.DeliveryTarget{pager, audit}

	for iteration := range raceIterations {
		held := routeHeld(ctx, testInstance, store, "u4i", "alerts", fmt.Sprintf("race-double-%d", iteration), scheduleBase, nil)
		var results [2]message.HeldReleaseResult
		var errs [2]error
		raceAll(
			func() { results[0], errs[0] = store.ReleaseHeldMessage(ctx, held.ID, targets, scheduleBase) },
			func() { results[1], errs[1] = store.ReleaseHeldMessage(ctx, held.ID, targets, scheduleBase) },
		)
		if errs[0] != nil || errs[1] != nil {
			testInstance.Fatalf("iteration %d: errors %v, %v", iteration, errs[0], errs[1])
		}
		released, skipped := 0, 0
		for _, result := range results {
			switch result {
			case message.HeldReleased:
				released++
			case message.HeldSkipped:
				skipped++
			}
		}
		if released != 1 || skipped != 1 {
			testInstance.Fatalf("iteration %d: released %d, skipped %d; want 1 and 1", iteration, released, skipped)
		}
		if got := countDeliveries(ctx, testInstance, pool, held.ID); got != len(targets) {
			testInstance.Fatalf("iteration %d: %d delivery rows, want %d", iteration, got, len(targets))
		}
		if final := mustGet(ctx, testInstance, store, held.ID); !allDeliveriesIn(final, message.StatePending) {
			testInstance.Fatalf("iteration %d: deliveries = %+v, want all pending", iteration, final.Deliveries)
		}
	}
}

func TestReleaseHeldMessageIsSkippedForReleasedAndCancelledMessages(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	targets := []message.DeliveryTarget{pager}

	released := routeHeld(ctx, testInstance, store, "u4i", "alerts", "skip-released", scheduleBase, nil)
	if result, err := store.ReleaseHeldMessage(ctx, released.ID, targets, scheduleBase); err != nil || result != message.HeldReleased {
		testInstance.Fatalf("first release = %q, err %v; want released", result, err)
	}
	if result, err := store.ReleaseHeldMessage(ctx, released.ID, targets, scheduleBase); err != nil || result != message.HeldSkipped {
		testInstance.Errorf("second release = %q, err %v; want skipped", result, err)
	}
	if got := countDeliveries(ctx, testInstance, pool, released.ID); got != 1 {
		testInstance.Errorf("released deliveries = %d, want 1 (no new rows)", got)
	}
	if failed, err := store.FailHeldRelease(ctx, released.ID, message.ReleaseFailedReasonInvalid, scheduleBase); err != nil || failed {
		testInstance.Errorf("FailHeldRelease on released = %v, err %v; want false", failed, err)
	}

	cancelled := routeHeld(ctx, testInstance, store, "u4i", "alerts", "skip-cancelled", scheduleBase, nil)
	if outcome, err := store.CancelMessage(ctx, cancelled.ID, "u4i", scheduleBase); err != nil || !outcome.Transitioned {
		testInstance.Fatalf("CancelMessage = %+v, err %v; want transitioned", outcome, err)
	}
	if result, err := store.ReleaseHeldMessage(ctx, cancelled.ID, targets, scheduleBase); err != nil || result != message.HeldSkipped {
		testInstance.Errorf("release of cancelled = %q, err %v; want skipped", result, err)
	}
	if got := countDeliveries(ctx, testInstance, pool, cancelled.ID); got != 0 {
		testInstance.Errorf("cancelled deliveries = %d, want 0", got)
	}
}

func TestReleaseHeldMessageIsSkippedWhileTheRowIsLocked(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	held := routeHeld(ctx, testInstance, store, "u4i", "alerts", "rel-locked", scheduleBase, nil)

	tx, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		testInstance.Fatalf("begin: %v", beginErr)
	}
	if _, lockErr := tx.Exec(ctx, `SELECT 1 FROM messages WHERE id = $1::text::uuid FOR UPDATE`, held.ID); lockErr != nil {
		_ = tx.Rollback(ctx)
		testInstance.Fatalf("lock message: %v", lockErr)
	}
	result, releaseErr := store.ReleaseHeldMessage(ctx, held.ID, []message.DeliveryTarget{pager}, scheduleBase)
	failed, failErr := store.FailHeldRelease(ctx, held.ID, message.ReleaseFailedReasonInvalid, scheduleBase)
	if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
		testInstance.Fatalf("rollback: %v", rollbackErr)
	}
	if releaseErr != nil || result != message.HeldSkipped {
		testInstance.Errorf("release with the row locked = %q, err %v; want skipped", result, releaseErr)
	}
	if failErr != nil || failed {
		testInstance.Errorf("fail with the row locked = %v, err %v; want false", failed, failErr)
	}
	requireStillHeld(ctx, testInstance, store, held.ID)
}

func TestConcurrentCancelAndHeldReleaseExactlyOneWins(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")

	for iteration := range raceIterations {
		held := routeHeld(ctx, testInstance, store, "u4i", "alerts", fmt.Sprintf("race-cancel-%d", iteration), scheduleBase, nil)
		var outcome message.CancelOutcome
		var result message.HeldReleaseResult
		var cancelErr, releaseErr error
		raceAll(
			func() { outcome, cancelErr = store.CancelMessage(ctx, held.ID, "u4i", scheduleBase) },
			func() {
				result, releaseErr = store.ReleaseHeldMessage(ctx, held.ID, []message.DeliveryTarget{pager}, scheduleBase)
			},
		)
		if cancelErr != nil || releaseErr != nil {
			testInstance.Fatalf("iteration %d: cancel err %v, release err %v", iteration, cancelErr, releaseErr)
		}
		final := mustGet(ctx, testInstance, store, held.ID)
		deliveries := countDeliveries(ctx, testInstance, pool, held.ID)
		if outcome.Transitioned {
			if result != message.HeldSkipped || final.HeldAt != nil || final.CancelledAt == nil ||
				final.HeldCancelledAt == nil || deliveries != 0 || final.ReleasedAt != nil {
				testInstance.Fatalf("iteration %d cancel won: result %q, message %+v, deliveries %d", iteration, result, final, deliveries)
			}
			continue
		}
		if result != message.HeldReleased || final.CancelledAt != nil || deliveries != 1 || final.ReleasedAt == nil ||
			final.HeldAt != nil || final.HeldCancelledAt != nil {
			testInstance.Fatalf("iteration %d release won: outcome %+v, result %q, message %+v, deliveries %d",
				iteration, outcome, result, final, deliveries)
		}
	}
}

func TestConcurrentHeldSweeperAndReleaseNeverReleaseThenDelete(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	targets := []message.DeliveryTarget{pager, audit}

	for iteration := range raceIterations {
		held := routeHeld(ctx, testInstance, store, "u4i", "alerts", fmt.Sprintf("race-sweep-%d", iteration), scheduleBase, nil)
		var result message.HeldReleaseResult
		var sweepErr, releaseErr error
		raceAll(
			func() { _, sweepErr = store.DeleteExpiredHeld(ctx, scheduleBase.Add(time.Hour)) },
			func() {
				result, releaseErr = store.ReleaseHeldMessage(ctx, held.ID, targets, scheduleBase.Add(2*time.Hour))
			},
		)
		if sweepErr != nil || releaseErr != nil {
			testInstance.Fatalf("iteration %d: sweep err %v, release err %v", iteration, sweepErr, releaseErr)
		}
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM messages WHERE id = $1::text::uuid)`, held.ID).Scan(&exists); err != nil {
			testInstance.Fatalf("exists: %v", err)
		}
		deliveries := countDeliveries(ctx, testInstance, pool, held.ID)
		var fetched *message.StoredMessage
		if exists {
			stored := mustGet(ctx, testInstance, store, held.ID)
			fetched = &stored
		}
		switch {
		case !exists && deliveries == 0 && result == message.HeldSkipped:
		case exists && result == message.HeldReleased && deliveries == len(targets) &&
			fetched.ReleasedAt != nil && fetched.HeldAt == nil:
		default:
			testInstance.Fatalf("iteration %d: exists %v, result %q, deliveries %d; want deleted-held or released intact",
				iteration, exists, result, deliveries)
		}
	}
}

func TestFailHeldReleaseRecordsTheTerminalFailure(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	held := routeHeld(ctx, testInstance, store, "u4i", "alerts", "fail-1", scheduleBase, nil)
	now := scheduleBase.Add(time.Hour)

	failed, err := store.FailHeldRelease(ctx, held.ID, message.ReleaseFailedReasonInvalid, now)
	if err != nil || !failed {
		testInstance.Fatalf("FailHeldRelease = %v, err %v; want true", failed, err)
	}
	got := mustGet(ctx, testInstance, store, held.ID)
	if got.HeldAt != nil || got.ReleaseFailedAt == nil || !got.ReleaseFailedAt.Equal(now) ||
		got.ReleaseFailedReason != message.ReleaseFailedReasonInvalid || got.ReleasedAt != nil {
		testInstance.Errorf("message = %+v, want release_failed with held_at cleared", got)
	}
	if count := countDeliveries(ctx, testInstance, pool, held.ID); count != 0 {
		testInstance.Errorf("deliveries = %d, want 0", count)
	}
	if again, againErr := store.FailHeldRelease(ctx, held.ID, message.ReleaseFailedReasonInvalid, now); againErr != nil || again {
		testInstance.Errorf("second FailHeldRelease = %v, err %v; want false (no longer held)", again, againErr)
	}
}

func TestFailHeldReleaseIsSkippedForCancelledAndRejectsBadArguments(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)
	held := routeHeld(ctx, testInstance, store, "u4i", "alerts", "fail-cancelled", scheduleBase, nil)
	if outcome, err := store.CancelMessage(ctx, held.ID, "u4i", scheduleBase); err != nil || !outcome.Transitioned {
		testInstance.Fatalf("CancelMessage = %+v, err %v; want transitioned", outcome, err)
	}
	if failed, err := store.FailHeldRelease(ctx, held.ID, message.ReleaseFailedReasonInvalid, scheduleBase); err != nil || failed {
		testInstance.Errorf("FailHeldRelease on cancelled = %v, err %v; want false", failed, err)
	}
	if got := mustGet(ctx, testInstance, store, held.ID); got.ReleaseFailedAt != nil {
		testInstance.Errorf("release_failed_at = %v, want untouched", got.ReleaseFailedAt)
	}

	other := routeHeld(ctx, testInstance, store, "u4i", "alerts", "fail-args", scheduleBase, nil)
	if _, err := store.FailHeldRelease(ctx, other.ID, "transient", scheduleBase); err == nil {
		testInstance.Error("a non-release_invalid reason: want an error")
	}
	if _, err := store.FailHeldRelease(ctx, other.ID, message.ReleaseFailedReasonInvalid, time.Time{}); err == nil {
		testInstance.Error("zero now: want an error")
	}
	requireStillHeld(ctx, testInstance, store, other.ID)
}

// seedRawHeld inserts a held row through SQL so it can carry to_destination or
// to_recipient, which the service never holds.
func seedRawHeld(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool,
	appID string, channel string, key string, toDestination any, toRecipient any,
) {
	testInstance.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO messages (app_id, channel, idempotency_key, to_destination, to_recipient, payload, held_at)
VALUES ($1, $2, $3, $4, $5, '{}'::jsonb, $6)`,
		appID, channel, key, toDestination, toRecipient, scheduleBase); err != nil {
		testInstance.Fatalf("seed raw held %s: %v", key, err)
	}
}

func TestListHeldPairsPagesByKeysetAndExcludesIneligibleRows(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	routeHeld(ctx, testInstance, store, "a", "x", "p-1", scheduleBase, nil)
	routeHeld(ctx, testInstance, store, "a", "x", "p-2", scheduleBase, nil)
	routeHeld(ctx, testInstance, store, "a", "y", "p-3", scheduleBase, nil)
	routeHeld(ctx, testInstance, store, "b", "x", "p-4", scheduleBase, nil)
	cancelled := routeHeld(ctx, testInstance, store, "c", "x", "p-5", scheduleBase, nil)
	released := routeHeld(ctx, testInstance, store, "d", "x", "p-6", scheduleBase, nil)
	if _, err := store.CancelMessage(ctx, cancelled.ID, "c", scheduleBase); err != nil {
		testInstance.Fatalf("cancel: %v", err)
	}
	if _, err := store.ReleaseHeldMessage(ctx, released.ID, []message.DeliveryTarget{pager}, scheduleBase); err != nil {
		testInstance.Fatalf("release: %v", err)
	}
	seedRawHeld(ctx, testInstance, pool, "e", "x", "raw-dest", "pager", nil)
	seedRawHeld(ctx, testInstance, pool, "f", "x", "raw-recipient", nil, "hash")

	first, err := store.ListHeldPairs(ctx, message.HeldPair{}, 2)
	want := []message.HeldPair{{AppID: "a", Channel: "x"}, {AppID: "a", Channel: "y"}}
	if err != nil || !slices.Equal(first, want) {
		testInstance.Fatalf("first page = %+v, err %v; want %+v", first, err, want)
	}
	second, err := store.ListHeldPairs(ctx, first[len(first)-1], 2)
	if err != nil || !slices.Equal(second, []message.HeldPair{{AppID: "b", Channel: "x"}}) {
		testInstance.Fatalf("second page = %+v, err %v; want only b/x (cancelled, released and raw rows excluded)", second, err)
	}
	empty, err := store.ListHeldPairs(ctx, second[0], 2)
	if err != nil || empty == nil || len(empty) != 0 {
		testInstance.Fatalf("past the end = %+v (nil %v), err %v; want a non-nil empty page", empty, empty == nil, err)
	}
	wrapped, err := store.ListHeldPairs(ctx, message.HeldPair{}, 10)
	wantWrapped := []message.HeldPair{{AppID: "a", Channel: "x"}, {AppID: "a", Channel: "y"}, {AppID: "b", Channel: "x"}}
	if err != nil || !slices.Equal(wrapped, wantWrapped) {
		testInstance.Fatalf("wrap input = %+v, err %v; want %+v", wrapped, err, wantWrapped)
	}
	if _, err := store.ListHeldPairs(ctx, message.HeldPair{}, 0); err == nil {
		testInstance.Error("limit 0: want an error")
	}
}

func TestListReleasableHeldOrdersByHeldAtThenIDAndFiltersPairs(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	late := routeHeld(ctx, testInstance, store, "a", "x", "r-late", scheduleBase.Add(2*time.Hour), nil)
	early := routeHeld(ctx, testInstance, store, "b", "y", "r-early", scheduleBase, nil)
	tieA := routeHeld(ctx, testInstance, store, "a", "x", "r-tie-a", scheduleBase.Add(time.Hour), nil)
	tieB := routeHeld(ctx, testInstance, store, "b", "y", "r-tie-b", scheduleBase.Add(time.Hour), nil)
	routeHeld(ctx, testInstance, store, "c", "z", "r-outside", scheduleBase, nil)
	// a/y and b/x: each component appears in the pair set but the tuple does not.
	crossAY := routeHeld(ctx, testInstance, store, "a", "y", "r-cross-ay", scheduleBase, nil)
	crossBX := routeHeld(ctx, testInstance, store, "b", "x", "r-cross-bx", scheduleBase, nil)
	seedRawHeld(ctx, testInstance, pool, "a", "x", "r-raw-dest", "pager", nil)
	seedRawHeld(ctx, testInstance, pool, "b", "y", "r-raw-recipient", nil, "hash")
	cancelled := routeHeld(ctx, testInstance, store, "a", "x", "r-cancelled", scheduleBase, nil)
	if _, err := store.CancelMessage(ctx, cancelled.ID, "a", scheduleBase); err != nil {
		testInstance.Fatalf("cancel: %v", err)
	}

	pairs := []message.HeldPair{{AppID: "a", Channel: "x"}, {AppID: "b", Channel: "y"}}
	listed, err := store.ListReleasableHeld(ctx, pairs, 10)
	if err != nil {
		testInstance.Fatalf("ListReleasableHeld: %v", err)
	}
	tieOrder := []string{tieA.ID, tieB.ID}
	slices.Sort(tieOrder)
	want := []string{early.ID, tieOrder[0], tieOrder[1], late.ID}
	got := make([]string, 0, len(listed))
	for _, stored := range listed {
		got = append(got, stored.ID)
		if stored.ID == crossAY.ID || stored.ID == crossBX.ID {
			testInstance.Errorf("listed %s, want pairs matched as tuples (a/y and b/x excluded)", stored.ID)
		}
		if stored.HeldAt == nil || len(stored.Deliveries) != 0 || stored.Envelope.Display == nil {
			testInstance.Errorf("listed %+v, want held_at and display set and no deliveries", stored)
		}
	}
	if !slices.Equal(got, want) {
		testInstance.Errorf("order = %v, want %v (held_at, id; no outside/raw/cancelled rows)", got, want)
	}

	limited, err := store.ListReleasableHeld(ctx, pairs, 2)
	if err != nil || len(limited) != 2 {
		testInstance.Errorf("limit 2 = %d rows, err %v", len(limited), err)
	}
	none, err := store.ListReleasableHeld(ctx, nil, 10)
	if err != nil || none == nil || len(none) != 0 {
		testInstance.Errorf("empty pairs = %+v (nil %v), err %v; want a non-nil empty slice", none, none == nil, err)
	}
	if _, err := store.ListReleasableHeld(ctx, pairs, 0); err == nil {
		testInstance.Error("limit 0: want an error")
	}
}

func TestListHeldFailuresNewestFirstWithTotal(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	empty, total, err := store.ListHeldFailures(ctx, 10)
	if err != nil || empty == nil || len(empty) != 0 || total != 0 {
		testInstance.Fatalf("empty = %+v (nil %v), total %d, err %v; want non-nil empty and 0", empty, empty == nil, total, err)
	}

	routeHeld(ctx, testInstance, store, "u4i", "alerts", "still-held", scheduleBase, nil)
	failedIDs := make([]string, 0, 3)
	failTimes := []time.Time{scheduleBase.Add(time.Hour), scheduleBase.Add(3 * time.Hour), scheduleBase.Add(3 * time.Hour)}
	for index, failTime := range failTimes {
		held := routeHeld(ctx, testInstance, store, "u4i", "alerts", fmt.Sprintf("failed-%d", index), scheduleBase, nil)
		if failed, failErr := store.FailHeldRelease(ctx, held.ID, message.ReleaseFailedReasonInvalid, failTime); failErr != nil || !failed {
			testInstance.Fatalf("fail %d = %v, err %v", index, failed, failErr)
		}
		failedIDs = append(failedIDs, held.ID)
	}
	releasedMessage := routeHeld(ctx, testInstance, store, "u4i", "alerts", "released-1", scheduleBase, nil)
	if _, err := store.ReleaseHeldMessage(ctx, releasedMessage.ID, []message.DeliveryTarget{pager}, scheduleBase); err != nil {
		testInstance.Fatalf("release: %v", err)
	}
	cancelledMessage := routeHeld(ctx, testInstance, store, "u4i", "alerts", "cancelled-1", scheduleBase, nil)
	if _, err := store.CancelMessage(ctx, cancelledMessage.ID, "u4i", scheduleBase); err != nil {
		testInstance.Fatalf("cancel: %v", err)
	}
	tied := []string{failedIDs[1], failedIDs[2]}
	slices.Sort(tied)
	wantOrder := []string{tied[1], tied[0], failedIDs[0]}

	everything, everythingTotal, err := store.ListHeldFailures(ctx, 10)
	if err != nil || everythingTotal != 3 || len(everything) != 3 {
		testInstance.Fatalf("limit 10 = %+v, total %d, err %v; want exactly the 3 failed rows", everything, everythingTotal, err)
	}
	for _, failure := range everything {
		if failure.MessageID == releasedMessage.ID || failure.MessageID == cancelledMessage.ID {
			testInstance.Errorf("listed %+v, want released and cancelled messages excluded", failure)
		}
	}

	listed, total, err := store.ListHeldFailures(ctx, 2)
	if err != nil || total != 3 || len(listed) != 2 {
		testInstance.Fatalf("limit 2 = %d rows, total %d, err %v; want 2 rows and total 3", len(listed), total, err)
	}
	for index, failure := range listed {
		if failure.MessageID != wantOrder[index] || failure.AppID != "u4i" || failure.Channel != "alerts" ||
			failure.Reason != message.ReleaseFailedReasonInvalid {
			testInstance.Errorf("row %d = %+v, want id %s", index, failure, wantOrder[index])
		}
	}
	if !listed[0].FailedAt.Equal(failTimes[1]) {
		testInstance.Errorf("FailedAt = %v, want %v", listed[0].FailedAt, failTimes[1])
	}
	if _, _, err := store.ListHeldFailures(ctx, 0); err == nil {
		testInstance.Error("limit 0: want an error")
	}
}

//go:build integration

package message_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// scheduleBase is the fixed clock the scheduled tests use as the origin for
// deliver_at and "now", so no test depends on the host or database clock.
var scheduleBase = queueBaseTime

const raceIterations = 25

// roomyTotalCap is a service-wide scheduled cap no test but the total-cap ones
// comes near.
const roomyTotalCap = 100000

// appCaps is a per-app cap with the roomy total, for tests of the per-app cap.
func appCaps(perApp int) message.ScheduledCaps {
	return message.ScheduledCaps{PerApp: perApp, Total: roomyTotalCap}
}

func scheduledEnvelope(idempotencyKey string, deliverAt time.Time) message.Envelope {
	envelope := sampleEnvelope(idempotencyKey)
	envelope.DeliverAt = &deliverAt
	return envelope
}

func mustInsertScheduled(
	ctx context.Context, testInstance *testing.T, store *message.PGStore,
	envelope message.Envelope, targets ...message.DeliveryTarget,
) message.StoredMessage {
	testInstance.Helper()
	stored, duplicate, err := store.InsertScheduledMessage(ctx, envelope, targets, appCaps(1000))
	if err != nil {
		testInstance.Fatalf("InsertScheduledMessage %s: %v", envelope.IdempotencyKey, err)
	}
	if duplicate {
		testInstance.Fatalf("InsertScheduledMessage %s: unexpected duplicate", envelope.IdempotencyKey)
	}
	return stored
}

func mustGet(ctx context.Context, testInstance *testing.T, store *message.PGStore, id string) message.StoredMessage {
	testInstance.Helper()
	stored, err := store.GetMessage(ctx, id)
	if err != nil {
		testInstance.Fatalf("GetMessage %s: %v", id, err)
	}
	return stored
}

func deliveryNames(stored message.StoredMessage) []string {
	names := make([]string, 0, len(stored.Deliveries))
	for _, delivery := range stored.Deliveries {
		names = append(names, delivery.DestinationName)
	}
	slices.Sort(names)
	return names
}

func allDeliveriesIn(stored message.StoredMessage, state string) bool {
	for _, delivery := range stored.Deliveries {
		if delivery.State != state {
			return false
		}
	}
	return len(stored.Deliveries) > 0
}

func payloadKind(testInstance *testing.T, stored message.StoredMessage) string {
	testInstance.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(stored.Envelope.Payload, &decoded); err != nil {
		testInstance.Fatalf("decode payload %s: %v", stored.Envelope.Payload, err)
	}
	kind, _ := decoded["kind"].(string)
	return kind
}

// raceAll starts every action behind one barrier and waits for all of them.
func raceAll(actions ...func()) {
	start := make(chan struct{})
	var ready, finished sync.WaitGroup
	ready.Add(len(actions))
	finished.Add(len(actions))
	for _, action := range actions {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			action()
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
}

func countState(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, state string,
) int {
	testInstance.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_deliveries WHERE state = $1`, state).Scan(&count); err != nil {
		testInstance.Fatalf("count %s deliveries: %v", state, err)
	}
	return count
}

func requireDeliveryTimesMatchDeliverAt(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, messageID string,
) {
	testInstance.Helper()
	var mismatched int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM message_deliveries d JOIN messages m ON m.id = d.message_id
WHERE d.message_id = $1::text::uuid AND d.state = 'scheduled' AND d.next_attempt_at IS DISTINCT FROM m.deliver_at`,
		messageID).Scan(&mismatched); err != nil {
		testInstance.Fatalf("compare next_attempt_at to deliver_at: %v", err)
	}
	if mismatched != 0 {
		testInstance.Errorf("%d scheduled deliveries have next_attempt_at != messages.deliver_at", mismatched)
	}
}

func TestInsertScheduledStoresScheduledDeliveriesThatNeverClaim(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	// An offset time: the stored deliver_at and next_attempt_at are the same instant.
	deliverAt := time.Date(2030, 6, 1, 9, 0, 0, 0, time.FixedZone("plus2", 2*3600))

	stored := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("sched", deliverAt), pager, audit)

	if len(stored.Deliveries) != 2 {
		testInstance.Fatalf("deliveries = %d, want one per target (2)", len(stored.Deliveries))
	}
	for _, delivery := range stored.Deliveries {
		if delivery.State != message.StateScheduled {
			testInstance.Errorf("delivery %s state = %q, want scheduled", delivery.DestinationName, delivery.State)
		}
		if !delivery.NextAttemptAt.Equal(deliverAt) {
			testInstance.Errorf("delivery %s next_attempt_at = %v, want deliver_at %v",
				delivery.DestinationName, delivery.NextAttemptAt, deliverAt)
		}
	}
	if stored.Envelope.DeliverAt == nil || !stored.Envelope.DeliverAt.Equal(deliverAt) {
		testInstance.Errorf("stored deliver_at = %v, want %v", stored.Envelope.DeliverAt, deliverAt)
	}
	if status := message.DeriveStatus(stored.Deliveries); status != message.StatusScheduled {
		testInstance.Errorf("status = %q, want scheduled", status)
	}
	requireDeliveryTimesMatchDeliverAt(ctx, testInstance, pool, stored.ID)

	for _, now := range []time.Time{deliverAt.Add(-time.Hour), deliverAt, deliverAt.Add(24 * 365 * time.Hour)} {
		if claim, found, claimErr := store.ClaimDue(ctx, now, queueTestLease); claimErr != nil || found {
			testInstance.Errorf("ClaimDue(%v) = %+v, found %v, err %v; want nothing claimable", now, claim, found, claimErr)
		}
	}
}

func TestInsertScheduledRejectsBadArgumentsAndStoresNothing(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	targets := []message.DeliveryTarget{pager}

	if _, _, err := store.InsertScheduledMessage(ctx, sampleEnvelope("no-deliver-at"), targets, appCaps(10)); err == nil {
		testInstance.Error("a scheduled insert without deliver_at succeeded")
	}
	envelope := scheduledEnvelope("zero-cap", scheduleBase.Add(time.Hour))
	if _, _, err := store.InsertScheduledMessage(ctx, envelope, targets, appCaps(0)); err == nil {
		testInstance.Error("a scheduled insert with a zero per-app cap succeeded")
	}
	if _, _, err := store.InsertScheduledMessage(
		ctx, envelope, targets, message.ScheduledCaps{PerApp: 10, Total: 0}); err == nil {
		testInstance.Error("a scheduled insert with a zero total cap succeeded")
	}
	if _, _, err := store.InsertScheduledMessage(ctx, envelope, nil, appCaps(10)); err == nil {
		testInstance.Error("a scheduled insert without targets succeeded")
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != 0 {
		testInstance.Errorf("messages rows = %d, want 0", got)
	}
}

func TestReplaceAndCancelRejectBadArgumentsAndChangeNothing(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	original := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("guards", scheduleBase.Add(time.Hour)), pager)
	replacement := scheduledEnvelope("guards", scheduleBase.Add(5*time.Hour))
	replacement.Channel = "other_channel"

	if _, replaced, err := store.ReplaceScheduledMessage(ctx, replacement, nil, scheduleBase); err == nil || replaced {
		testInstance.Errorf("a replace without targets = replaced %v, err %v; want an error and no replace", replaced, err)
	}
	if _, replaced, err := store.ReplaceScheduledMessage(
		ctx, replacement, []message.DeliveryTarget{audit}, time.Time{}); err == nil || replaced {
		testInstance.Errorf("a replace with a zero now = replaced %v, err %v; want an error and no replace", replaced, err)
	}
	if outcome, err := store.AdminCancelMessage(ctx, original.ID, time.Time{}); err == nil || outcome.Found {
		testInstance.Errorf("an admin cancel with a zero now = %+v, err %v; want an error and no outcome", outcome, err)
	}
	if outcome, err := store.CancelMessageByKey(ctx, original.Envelope.AppID, "guards", time.Time{}); err == nil || outcome.Found {
		testInstance.Errorf("a cancel by key with a zero now = %+v, err %v; want an error and no outcome", outcome, err)
	}

	if after := mustGet(ctx, testInstance, store, original.ID); !reflect.DeepEqual(after, original) {
		testInstance.Errorf("message after the rejected calls = %+v, want it unchanged: %+v", after, original)
	}
}

func TestInsertScheduledDuplicateKeyReturnsTheOriginal(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	first := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("dup", scheduleBase.Add(time.Hour)), pager)

	again, duplicate, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelope("dup", scheduleBase.Add(48*time.Hour)), []message.DeliveryTarget{pager}, appCaps(1))
	if err != nil {
		testInstance.Fatalf("duplicate InsertScheduledMessage at the cap: %v", err)
	}
	if !duplicate || again.ID != first.ID {
		testInstance.Errorf("duplicate = %v, id %q; want true and %q", duplicate, again.ID, first.ID)
	}
	if !again.Envelope.DeliverAt.Equal(scheduleBase.Add(time.Hour)) {
		testInstance.Errorf("duplicate deliver_at = %v, want the original %v", again.Envelope.DeliverAt, scheduleBase.Add(time.Hour))
	}
}

func TestReleaseDueReleasesOnlyDueMessagesOldestFirst(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	first := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("due-1", scheduleBase.Add(time.Hour)), pager, audit)
	second := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("due-2", scheduleBase.Add(2*time.Hour)), pager)
	third := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("due-3", scheduleBase.Add(3*time.Hour)), pager)

	now := scheduleBase.Add(90 * time.Minute)
	released, err := store.ReleaseDue(ctx, now, 100)
	if err != nil {
		testInstance.Fatalf("ReleaseDue: %v", err)
	}
	if released != 1 {
		testInstance.Fatalf("released = %d, want 1 message (not its 2 deliveries)", released)
	}
	got := mustGet(ctx, testInstance, store, first.ID)
	if !allDeliveriesIn(got, message.StatePending) || len(got.Deliveries) != 2 {
		testInstance.Fatalf("released message deliveries = %+v, want 2 pending", got.Deliveries)
	}
	for _, delivery := range got.Deliveries {
		if !delivery.NextAttemptAt.Equal(now) || !delivery.BudgetStartedAt.Equal(now) {
			testInstance.Errorf("delivery %s next_attempt_at %v, budget_started_at %v; want both %v",
				delivery.DestinationName, delivery.NextAttemptAt, delivery.BudgetStartedAt, now)
		}
	}
	for name, id := range map[string]string{"second": second.ID, "third": third.ID} {
		if !allDeliveriesIn(mustGet(ctx, testInstance, store, id), message.StateScheduled) {
			testInstance.Errorf("%s message was released early", name)
		}
	}
	if again, againErr := store.ReleaseDue(ctx, now, 100); againErr != nil || again != 0 {
		testInstance.Errorf("second ReleaseDue = %d, err %v; want 0", again, againErr)
	}

	// The released deliveries are claimable now, and only they.
	claimed := 0
	for {
		_, found, claimErr := store.ClaimDue(ctx, now, queueTestLease)
		if claimErr != nil {
			testInstance.Fatalf("ClaimDue: %v", claimErr)
		}
		if !found {
			break
		}
		claimed++
	}
	if claimed != 2 {
		testInstance.Errorf("claimed %d deliveries, want the 2 released ones", claimed)
	}

	// limit bounds a run, oldest first.
	late := scheduleBase.Add(10 * time.Hour)
	if limited, limitedErr := store.ReleaseDue(ctx, late, 1); limitedErr != nil || limited != 1 {
		testInstance.Fatalf("ReleaseDue limit 1 = %d, err %v; want 1", limited, limitedErr)
	}
	if !allDeliveriesIn(mustGet(ctx, testInstance, store, second.ID), message.StatePending) {
		testInstance.Error("limit 1 did not release the oldest due message (second)")
	}
	if !allDeliveriesIn(mustGet(ctx, testInstance, store, third.ID), message.StateScheduled) {
		testInstance.Error("limit 1 released more than one message")
	}
	if rest, restErr := store.ReleaseDue(ctx, late, 100); restErr != nil || rest != 1 {
		testInstance.Errorf("final ReleaseDue = %d, err %v; want 1", rest, restErr)
	}

	if _, zeroErr := store.ReleaseDue(ctx, late, 0); zeroErr == nil {
		testInstance.Error("ReleaseDue with limit 0 succeeded")
	}
	if _, zeroTimeErr := store.ReleaseDue(ctx, time.Time{}, 1); zeroTimeErr == nil {
		testInstance.Error("ReleaseDue with a zero now succeeded")
	}
}

func TestConcurrentReplaceAndReleaseNeverMixOldAndNew(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	extra := seedTarget(ctx, testInstance, registryStore, "extra")
	now := scheduleBase.Add(2 * time.Hour)

	// Either side may win a given race; each outcome must be internally
	// consistent, and the deterministic lock test below proves the serialization.
	for iteration := range raceIterations {
		key := fmt.Sprintf("race-replace-release-%d", iteration)
		original := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope(key, scheduleBase.Add(time.Hour)), pager)
		replacement := scheduledEnvelope(key, scheduleBase.Add(48*time.Hour))
		replacement.Payload = json.RawMessage(`{"kind":"replaced","count":4}`)

		var replaced bool
		var released int64
		var replaceErr, releaseErr error
		raceAll(
			func() {
				_, replaced, replaceErr = store.ReplaceScheduledMessage(
					ctx, replacement, []message.DeliveryTarget{audit, extra}, now)
			},
			func() { released, releaseErr = store.ReleaseDue(ctx, now, 100) },
		)
		if replaceErr != nil || releaseErr != nil {
			testInstance.Fatalf("iteration %d: replace err %v, release err %v", iteration, replaceErr, releaseErr)
		}

		final := mustGet(ctx, testInstance, store, original.ID)
		names := deliveryNames(final)
		if replaced {
			if released != 0 || !allDeliveriesIn(final, message.StateScheduled) ||
				payloadKind(testInstance, final) != "replaced" || !slices.Equal(names, []string{"audit", "extra"}) {
				testInstance.Errorf("iteration %d replaced: released %d, deliveries %+v, payload %s; want scheduled audit+extra with the new payload",
					iteration, released, final.Deliveries, final.Envelope.Payload)
			}
		} else {
			if released != 1 || !allDeliveriesIn(final, message.StatePending) ||
				payloadKind(testInstance, final) != "deploy" || !slices.Equal(names, []string{"pager"}) {
				testInstance.Errorf("iteration %d released: released %d, deliveries %+v, payload %s; want pending pager with the original payload",
					iteration, released, final.Deliveries, final.Envelope.Payload)
			}
		}
	}
}

// requireBlocksWhileLocked holds FOR UPDATE on the message row in an open
// transaction, starts run, and asserts run is still blocked while the lock is
// held and finishes once the transaction ends.
func requireBlocksWhileLocked(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, messageID string, run func(),
) {
	testInstance.Helper()
	tx, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		testInstance.Fatalf("begin: %v", beginErr)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked string
	if lockErr := tx.QueryRow(ctx,
		`SELECT id::text FROM messages WHERE id = $1::text::uuid FOR UPDATE`, messageID).Scan(&locked); lockErr != nil {
		testInstance.Fatalf("lock message: %v", lockErr)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	// Wait until some backend is queued on a lock, then confirm run is still
	// not finished while the lock is held.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if scanErr := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&waiting); scanErr != nil {
			testInstance.Fatalf("read pg_locks: %v", scanErr)
		}
		if waiting > 0 {
			break
		}
		select {
		case <-done:
			testInstance.Fatal("the operation finished while the message row lock was held")
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			testInstance.Fatal("the operation never queued on the message row lock")
		}
	}
	select {
	case <-done:
		testInstance.Fatal("the operation finished while the message row lock was held")
	case <-time.After(200 * time.Millisecond):
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		testInstance.Fatalf("commit: %v", commitErr)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		testInstance.Fatal("the operation did not finish after the lock was released")
	}
}

func TestMessageRowLockSerializesReleaseCancelAndReplace(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	now := scheduleBase.Add(2 * time.Hour)
	dueAt := scheduleBase.Add(time.Hour)

	held := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("lock-held", dueAt), pager)

	// ReleaseDue skips a locked message instead of waiting on it or overwriting.
	tx, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		testInstance.Fatalf("begin: %v", beginErr)
	}
	if _, lockErr := tx.Exec(ctx,
		`SELECT 1 FROM messages WHERE id = $1::text::uuid FOR UPDATE`, held.ID); lockErr != nil {
		_ = tx.Rollback(ctx)
		testInstance.Fatalf("lock message: %v", lockErr)
	}
	released, releaseErr := store.ReleaseDue(ctx, now, 100)
	if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
		testInstance.Fatalf("rollback: %v", rollbackErr)
	}
	if releaseErr != nil || released != 0 {
		testInstance.Fatalf("ReleaseDue with the row locked = %d, err %v; want 0 (skipped)", released, releaseErr)
	}
	if got := mustGet(ctx, testInstance, store, held.ID); !allDeliveriesIn(got, message.StateScheduled) {
		testInstance.Fatalf("deliveries = %+v, want the locked message still scheduled", got.Deliveries)
	}

	// Cancel waits for the lock and then cancels the still-scheduled message.
	var cancelOutcome message.CancelOutcome
	var cancelErr error
	requireBlocksWhileLocked(ctx, testInstance, pool, held.ID, func() {
		cancelOutcome, cancelErr = store.CancelMessage(ctx, held.ID, "u4i", now)
	})
	if cancelErr != nil || !cancelOutcome.Cancelled {
		testInstance.Errorf("CancelMessage after the lock = %+v, err %v; want cancelled", cancelOutcome, cancelErr)
	}

	// Replace waits for the lock and then replaces the still-scheduled message.
	other := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("lock-replace", dueAt), pager)
	replacement := scheduledEnvelope("lock-replace", scheduleBase.Add(48*time.Hour))
	replacement.Payload = json.RawMessage(`{"kind":"replaced","count":4}`)
	var replaced bool
	var replaceErr error
	requireBlocksWhileLocked(ctx, testInstance, pool, other.ID, func() {
		_, replaced, replaceErr = store.ReplaceScheduledMessage(
			ctx, replacement, []message.DeliveryTarget{audit}, now)
	})
	if replaceErr != nil || !replaced {
		testInstance.Errorf("ReplaceScheduledMessage after the lock = replaced %v, err %v; want replaced", replaced, replaceErr)
	}
	if got := mustGet(ctx, testInstance, store, other.ID); payloadKind(testInstance, got) != "replaced" ||
		!slices.Equal(deliveryNames(got), []string{"audit"}) {
		testInstance.Errorf("replaced message = %+v / %s, want the replacement", got.Deliveries, got.Envelope.Payload)
	}
}

func TestConcurrentCancelAndReleaseEndAllCancelledOrAllPending(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	now := scheduleBase.Add(2 * time.Hour)

	for iteration := range raceIterations {
		key := fmt.Sprintf("race-cancel-release-%d", iteration)
		original := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope(key, scheduleBase.Add(time.Hour)), pager, audit)

		var outcome message.CancelOutcome
		var released int64
		var cancelErr, releaseErr error
		raceAll(
			func() { outcome, cancelErr = store.CancelMessage(ctx, original.ID, "u4i", now) },
			func() { released, releaseErr = store.ReleaseDue(ctx, now, 100) },
		)
		if cancelErr != nil || releaseErr != nil {
			testInstance.Fatalf("iteration %d: cancel err %v, release err %v", iteration, cancelErr, releaseErr)
		}

		final := mustGet(ctx, testInstance, store, original.ID)
		switch {
		case outcome.Cancelled:
			if released != 0 || !allDeliveriesIn(final, message.StateCancelled) || final.CancelledAt == nil ||
				outcome.Status != message.StatusCancelled {
				testInstance.Errorf("iteration %d cancel won: released %d, outcome %+v, deliveries %+v, cancelled_at %v; want all cancelled",
					iteration, released, outcome, final.Deliveries, final.CancelledAt)
			}
		default:
			if released != 1 || !allDeliveriesIn(final, message.StatePending) || final.CancelledAt != nil ||
				outcome.Status != message.StatusPending || !outcome.Found {
				testInstance.Errorf("iteration %d release won: released %d, outcome %+v, deliveries %+v, cancelled_at %v; want all pending",
					iteration, released, outcome, final.Deliveries, final.CancelledAt)
			}
		}
	}
	if got := countState(ctx, testInstance, pool, message.StateScheduled); got != 0 {
		testInstance.Errorf("%d deliveries are still scheduled after every race", got)
	}
}

func TestConcurrentReplaceAndCancelEndReplacedScheduledOrAllCancelled(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	extra := seedTarget(ctx, testInstance, registryStore, "extra")
	now := scheduleBase.Add(30 * time.Minute)

	for iteration := range raceIterations {
		key := fmt.Sprintf("race-replace-cancel-%d", iteration)
		original := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope(key, scheduleBase.Add(time.Hour)), pager)
		replacement := scheduledEnvelope(key, scheduleBase.Add(48*time.Hour))
		replacement.Payload = json.RawMessage(`{"kind":"replaced","count":4}`)

		var replaced bool
		var replaceErr, cancelErr error
		raceAll(
			func() {
				_, replaced, replaceErr = store.ReplaceScheduledMessage(
					ctx, replacement, []message.DeliveryTarget{audit, extra}, now)
			},
			func() { _, cancelErr = store.CancelMessageByKey(ctx, "u4i", key, now) },
		)
		if replaceErr != nil || cancelErr != nil {
			testInstance.Fatalf("iteration %d: replace err %v, cancel err %v", iteration, replaceErr, cancelErr)
		}

		final := mustGet(ctx, testInstance, store, original.ID)
		names := deliveryNames(final)
		// Cancel always runs against a scheduled message, so it always wins in the
		// end; what matters is that the deliveries are one consistent set.
		switch {
		case allDeliveriesIn(final, message.StateCancelled):
			wantNames, wantKind := []string{"pager"}, "deploy"
			if replaced {
				wantNames, wantKind = []string{"audit", "extra"}, "replaced"
			}
			if final.CancelledAt == nil || !slices.Equal(names, wantNames) || payloadKind(testInstance, final) != wantKind {
				testInstance.Errorf("iteration %d cancelled (replaced %v): deliveries %v, payload %s, cancelled_at %v; want %v / %s",
					iteration, replaced, names, final.Envelope.Payload, final.CancelledAt, wantNames, wantKind)
			}
		case allDeliveriesIn(final, message.StateScheduled):
			if !replaced || !slices.Equal(names, []string{"audit", "extra"}) || payloadKind(testInstance, final) != "replaced" {
				testInstance.Errorf("iteration %d scheduled: replaced %v, deliveries %v, payload %s; want the replaced set",
					iteration, replaced, names, final.Envelope.Payload)
			}
		default:
			testInstance.Errorf("iteration %d: mixed deliveries %+v", iteration, final.Deliveries)
		}
	}
}

func TestScheduledCapIsNeverExceededUnderConcurrentInserts(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	const capacity = 3
	const racers = capacity + 5
	deliverAt := scheduleBase.Add(time.Hour)

	errs := make([]error, racers)
	raceActions := make([]func(), 0, racers)
	for index := range racers {
		raceActions = append(raceActions, func() {
			_, _, errs[index] = store.InsertScheduledMessage(
				ctx, scheduledEnvelope(fmt.Sprintf("cap-%d", index), deliverAt), []message.DeliveryTarget{pager}, appCaps(capacity))
		})
	}
	raceAll(raceActions...)

	stored, refused := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			stored++
		case errors.Is(err, message.ErrTooManyScheduled):
			refused++
		default:
			testInstance.Errorf("racer %d: unexpected error %v", index, err)
		}
	}
	if stored != capacity || refused != racers-capacity {
		testInstance.Errorf("stored %d, refused %d; want %d and %d", stored, refused, capacity, racers-capacity)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != capacity {
		testInstance.Errorf("messages rows = %d, want exactly the cap (%d)", got, capacity)
	}
	if got := countRows(ctx, testInstance, pool, "message_deliveries"); got != capacity {
		testInstance.Errorf("message_deliveries rows = %d, want %d (a refused submit stores nothing)", got, capacity)
	}

	// Another app has its own allowance.
	otherEnvelope := scheduledEnvelope("other-app", deliverAt)
	otherEnvelope.AppID = "other_app"
	if _, _, err := store.InsertScheduledMessage(ctx, otherEnvelope, []message.DeliveryTarget{pager}, appCaps(capacity)); err != nil {
		testInstance.Errorf("another app's scheduled insert at the first app's cap: %v", err)
	}
}

func TestScheduledCapFreesUpOnCancelAndOnRelease(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	targets := []message.DeliveryTarget{pager}
	const capacity = 2
	deliverAt := scheduleBase.Add(time.Hour)
	insert := func(key string) (message.StoredMessage, error) {
		stored, _, err := store.InsertScheduledMessage(ctx, scheduledEnvelope(key, deliverAt), targets, appCaps(capacity))
		return stored, err
	}

	first, firstErr := insert("cap-a")
	_, secondErr := insert("cap-b")
	if firstErr != nil || secondErr != nil {
		testInstance.Fatalf("filling the cap: %v, %v", firstErr, secondErr)
	}
	if _, err := insert("cap-c"); !errors.Is(err, message.ErrTooManyScheduled) {
		testInstance.Fatalf("insert over the cap err = %v, want ErrTooManyScheduled", err)
	}

	// A duplicate key at the cap is not a new scheduled message.
	if again, duplicate, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelope("cap-a", deliverAt), targets, appCaps(capacity)); err != nil || !duplicate || again.ID != first.ID {
		testInstance.Errorf("duplicate at the cap = id %q, duplicate %v, err %v; want the original, true, nil", again.ID, duplicate, err)
	}

	// Cancelling one frees a slot.
	if outcome, err := store.CancelMessage(ctx, first.ID, "u4i", scheduleBase); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("CancelMessage = %+v, err %v; want cancelled", outcome, err)
	}
	if _, err := insert("cap-c"); err != nil {
		testInstance.Fatalf("insert after a cancel: %v", err)
	}
	if _, err := insert("cap-d"); !errors.Is(err, message.ErrTooManyScheduled) {
		testInstance.Fatalf("insert over the cap again err = %v, want ErrTooManyScheduled", err)
	}

	// Releasing one frees a slot too.
	if released, err := store.ReleaseDue(ctx, deliverAt.Add(time.Minute), 1); err != nil || released != 1 {
		testInstance.Fatalf("ReleaseDue = %d, err %v; want 1", released, err)
	}
	if _, err := insert("cap-d"); err != nil {
		testInstance.Errorf("insert after a release: %v", err)
	}
}

// scheduledEnvelopeFor is scheduledEnvelope for the app appID.
func scheduledEnvelopeFor(appID string, idempotencyKey string, deliverAt time.Time) message.Envelope {
	envelope := scheduledEnvelope(idempotencyKey, deliverAt)
	envelope.AppID = appID
	return envelope
}

func TestScheduledTotalCapSpansAppsAndIsReportedDistinctly(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	targets := []message.DeliveryTarget{pager}
	caps := message.ScheduledCaps{PerApp: 10, Total: 3}
	deliverAt := scheduleBase.Add(time.Hour)
	insert := func(appID string, key string) (message.StoredMessage, error) {
		stored, _, err := store.InsertScheduledMessage(ctx, scheduledEnvelopeFor(appID, key, deliverAt), targets, caps)
		return stored, err
	}

	first, firstErr := insert("app_a", "total-1")
	_, secondErr := insert("app_b", "total-2")
	_, thirdErr := insert("app_c", "total-3")
	if firstErr != nil || secondErr != nil || thirdErr != nil {
		testInstance.Fatalf("filling the total cap with three apps: %v, %v, %v", firstErr, secondErr, thirdErr)
	}

	// A fourth app is refused with the distinct error, and nothing is stored.
	_, overErr := insert("app_d", "total-4")
	if !errors.Is(overErr, message.ErrScheduledCapacityReached) || errors.Is(overErr, message.ErrTooManyScheduled) {
		testInstance.Fatalf("insert over the total cap err = %v, want ErrScheduledCapacityReached only", overErr)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != 3 {
		testInstance.Fatalf("messages rows = %d, want 3 (a refused submit stores nothing)", got)
	}
	if got := countRows(ctx, testInstance, pool, "message_deliveries"); got != 3 {
		testInstance.Fatalf("message_deliveries rows = %d, want 3", got)
	}

	// An immediate message is not a scheduled one, so the total cap never blocks it.
	if _, _, err := store.InsertMessage(ctx, sampleEnvelope("immediate-at-total-cap"), targets); err != nil {
		testInstance.Errorf("immediate insert at the total cap: %v", err)
	}

	// A duplicate key at the total cap is not a new scheduled message.
	if again, duplicate, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelopeFor("app_a", "total-1", deliverAt), targets, caps); err != nil || !duplicate || again.ID != first.ID {
		testInstance.Errorf("duplicate at the total cap = id %q, duplicate %v, err %v; want the original, true, nil", again.ID, duplicate, err)
	}

	// Cancelling one frees a slot for any app; releasing one does too.
	if outcome, err := store.CancelMessage(ctx, first.ID, "app_a", scheduleBase); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("CancelMessage = %+v, err %v; want cancelled", outcome, err)
	}
	if _, err := insert("app_d", "total-4"); err != nil {
		testInstance.Fatalf("insert after a cancel: %v", err)
	}
	if _, err := insert("app_e", "total-5"); !errors.Is(err, message.ErrScheduledCapacityReached) {
		testInstance.Fatalf("insert over the total cap again err = %v, want ErrScheduledCapacityReached", err)
	}
	if released, err := store.ReleaseDue(ctx, deliverAt.Add(time.Minute), 1); err != nil || released != 1 {
		testInstance.Fatalf("ReleaseDue = %d, err %v; want 1", released, err)
	}
	if _, err := insert("app_e", "total-5"); err != nil {
		testInstance.Errorf("insert after a release: %v", err)
	}
}

func TestScheduledPerAppCapIsReportedBeforeTheTotalCap(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	targets := []message.DeliveryTarget{pager}
	caps := message.ScheduledCaps{PerApp: 1, Total: 1}
	deliverAt := scheduleBase.Add(time.Hour)

	if _, _, err := store.InsertScheduledMessage(ctx, scheduledEnvelopeFor("app_a", "order-1", deliverAt), targets, caps); err != nil {
		testInstance.Fatalf("first insert: %v", err)
	}
	// Both caps are full for app_a: the app's own cap is the answer.
	if _, _, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelopeFor("app_a", "order-2", deliverAt), targets, caps); !errors.Is(err, message.ErrTooManyScheduled) {
		testInstance.Errorf("app at both caps err = %v, want ErrTooManyScheduled", err)
	}
	// Another app is under its own cap but the shared one is full.
	if _, _, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelopeFor("app_b", "order-3", deliverAt), targets, caps); !errors.Is(err, message.ErrScheduledCapacityReached) {
		testInstance.Errorf("other app at the total cap err = %v, want ErrScheduledCapacityReached", err)
	}
}

func TestReplaceScheduledIsAllowedAtTheTotalCap(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	caps := message.ScheduledCaps{PerApp: 1, Total: 1}
	original := scheduledEnvelopeFor("app_a", "replace-at-cap", scheduleBase.Add(time.Hour))
	stored, _, err := store.InsertScheduledMessage(ctx, original, []message.DeliveryTarget{pager}, caps)
	if err != nil {
		testInstance.Fatalf("fill both caps: %v", err)
	}

	// A replace that keeps the message scheduled does not grow either count.
	replacement := scheduledEnvelopeFor("app_a", "replace-at-cap", scheduleBase.Add(5*time.Hour))
	replacement.Channel = "other_channel"
	replaced, wasReplaced, err := store.ReplaceScheduledMessage(ctx, replacement, []message.DeliveryTarget{audit}, scheduleBase)
	if err != nil || !wasReplaced || replaced.ID != stored.ID {
		testInstance.Fatalf("replace at both caps = id %q, replaced %v, err %v; want the same id, true, nil", replaced.ID, wasReplaced, err)
	}
	if !allDeliveriesIn(replaced, message.StateScheduled) {
		testInstance.Errorf("replaced deliveries %+v, want all scheduled", replaced.Deliveries)
	}
	if _, _, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelopeFor("app_b", "still-full", scheduleBase.Add(time.Hour)), []message.DeliveryTarget{pager}, caps); !errors.Is(err, message.ErrScheduledCapacityReached) {
		testInstance.Errorf("insert after the replace err = %v, want the caps still full", err)
	}

	// A replace that releases the message at once frees its slot.
	release := scheduledEnvelopeFor("app_a", "replace-at-cap", scheduleBase)
	release.Channel = "released_channel"
	if _, wasReplaced, err = store.ReplaceScheduledMessage(ctx, release, []message.DeliveryTarget{pager}, scheduleBase); err != nil || !wasReplaced {
		testInstance.Fatalf("releasing replace: replaced %v, err %v", wasReplaced, err)
	}
	if _, _, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelopeFor("app_b", "now-free", scheduleBase.Add(time.Hour)), []message.DeliveryTarget{pager}, caps); err != nil {
		testInstance.Errorf("insert after a releasing replace: %v", err)
	}
}

func TestScheduledTotalCapIsNeverExceededUnderConcurrentInserts(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	const totalCap = 3
	const racers = totalCap + 5
	caps := message.ScheduledCaps{PerApp: 100, Total: totalCap}
	deliverAt := scheduleBase.Add(time.Hour)

	// Every racer is its own app, so only the total cap can stop them.
	errs := make([]error, racers)
	raceActions := make([]func(), 0, racers)
	for index := range racers {
		raceActions = append(raceActions, func() {
			_, _, errs[index] = store.InsertScheduledMessage(
				ctx, scheduledEnvelopeFor(fmt.Sprintf("race_app_%d", index), "total-race", deliverAt),
				[]message.DeliveryTarget{pager}, caps)
		})
	}
	raceAll(raceActions...)

	stored, refused := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			stored++
		case errors.Is(err, message.ErrScheduledCapacityReached):
			refused++
		default:
			testInstance.Errorf("racer %d: unexpected error %v", index, err)
		}
	}
	if stored != totalCap || refused != racers-totalCap {
		testInstance.Errorf("stored %d, refused %d; want %d and %d", stored, refused, totalCap, racers-totalCap)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != totalCap {
		testInstance.Errorf("messages rows = %d, want exactly the total cap (%d)", got, totalCap)
	}
}

// TestScheduledCapsHoldTogetherUnderConcurrentInsertsAcrossApps races two apps
// against a per-app cap that is smaller than the total one: neither cap may be
// exceeded, the locks (global, then app) must not deadlock, and exactly the total
// is stored, since a refusal by the total cap means it is full, and refusals by
// the per-app cap alone could not fill both apps' allowances past it.
func TestScheduledCapsHoldTogetherUnderConcurrentInsertsAcrossApps(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	caps := message.ScheduledCaps{PerApp: 2, Total: 3}
	const racers = 8
	deliverAt := scheduleBase.Add(time.Hour)

	errs := make([]error, racers)
	appIDs := make([]string, racers)
	raceActions := make([]func(), 0, racers)
	for index := range racers {
		appIDs[index] = fmt.Sprintf("mixed_app_%d", index%2)
		raceActions = append(raceActions, func() {
			_, _, errs[index] = store.InsertScheduledMessage(
				ctx, scheduledEnvelopeFor(appIDs[index], fmt.Sprintf("mixed-%d", index), deliverAt),
				[]message.DeliveryTarget{pager}, caps)
		})
	}
	raceAll(raceActions...)

	storedPerApp := map[string]int{}
	stored := 0
	for index, err := range errs {
		switch {
		case err == nil:
			stored++
			storedPerApp[appIDs[index]]++
		case errors.Is(err, message.ErrTooManyScheduled), errors.Is(err, message.ErrScheduledCapacityReached):
		default:
			testInstance.Errorf("racer %d: unexpected error %v", index, err)
		}
	}
	if stored != caps.Total {
		testInstance.Errorf("stored %d, want exactly the total cap %d", stored, caps.Total)
	}
	for appID, count := range storedPerApp {
		if count > caps.PerApp {
			testInstance.Errorf("app %s stored %d, over its cap %d", appID, count, caps.PerApp)
		}
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != caps.Total {
		testInstance.Errorf("messages rows = %d, want %d", got, caps.Total)
	}
}

func TestReplaceScheduledSwapsContentAndKeepsIdentity(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	extra := seedTarget(ctx, testInstance, registryStore, "extra")
	now := scheduleBase
	original := mustInsertScheduled(ctx, testInstance, store,
		scheduledEnvelope("swap", scheduleBase.Add(time.Hour)), pager, audit)

	replacement := scheduledEnvelope("swap", scheduleBase.Add(5*time.Hour))
	replacement.Channel = "other_channel"
	replacement.Critical = false
	replacement.Payload = json.RawMessage(`{"kind":"replaced","count":9}`)
	replacement.Display = nil

	// N=2 targets down to M=1.
	replaced, wasReplaced, err := store.ReplaceScheduledMessage(ctx, replacement, []message.DeliveryTarget{extra}, now)
	if err != nil || !wasReplaced {
		testInstance.Fatalf("ReplaceScheduledMessage = replaced %v, err %v; want true", wasReplaced, err)
	}
	if replaced.ID != original.ID || !replaced.CreatedAt.Equal(original.CreatedAt) {
		testInstance.Errorf("identity = (%s, %v), want kept (%s, %v)", replaced.ID, replaced.CreatedAt, original.ID, original.CreatedAt)
	}
	got := mustGet(ctx, testInstance, store, original.ID)
	if got.Envelope.Channel != "other_channel" || got.Envelope.Critical || got.Envelope.Display != nil ||
		payloadKind(testInstance, got) != "replaced" || !got.Envelope.DeliverAt.Equal(scheduleBase.Add(5*time.Hour)) {
		testInstance.Errorf("replaced envelope = %+v, want the new channel, critical=false, no display, new payload and deliver_at", got.Envelope)
	}
	if names := deliveryNames(got); !slices.Equal(names, []string{"extra"}) || !allDeliveriesIn(got, message.StateScheduled) {
		testInstance.Errorf("deliveries after 2->1 = %+v, want exactly [extra] scheduled", got.Deliveries)
	}
	requireDeliveryTimesMatchDeliverAt(ctx, testInstance, pool, original.ID)

	// M=1 up to 3 targets, with the display coming back.
	again := scheduledEnvelope("swap", scheduleBase.Add(7*time.Hour))
	again.Display = &message.Display{Title: "Back"}
	if _, wasReplaced, err = store.ReplaceScheduledMessage(ctx, again, []message.DeliveryTarget{pager, audit, extra}, now); err != nil || !wasReplaced {
		testInstance.Fatalf("second ReplaceScheduledMessage = replaced %v, err %v; want true", wasReplaced, err)
	}
	got = mustGet(ctx, testInstance, store, original.ID)
	if names := deliveryNames(got); !slices.Equal(names, []string{"audit", "extra", "pager"}) || !allDeliveriesIn(got, message.StateScheduled) {
		testInstance.Errorf("deliveries after 1->3 = %+v, want exactly 3 scheduled", got.Deliveries)
	}
	if got.Envelope.Display == nil || got.Envelope.Display.Title != "Back" {
		testInstance.Errorf("display = %+v, want the new display", got.Envelope.Display)
	}
	requireDeliveryTimesMatchDeliverAt(ctx, testInstance, pool, original.ID)
	if total := countRows(ctx, testInstance, pool, "message_deliveries"); total != 3 {
		testInstance.Errorf("message_deliveries rows = %d, want 3 (old rows swapped out)", total)
	}
}

func TestReplaceScheduledSwitchesBetweenDestinationAndRecipient(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	hash := personhash.Hash("alice@example.com")
	recipientTarget := message.DeliveryTarget{
		DestinationID: nil, DestinationName: registry.RecipientDestinationName, DestinationType: registry.TypeNtfy,
	}
	deliverAt := scheduleBase.Add(time.Hour)
	original := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("addressing", deliverAt), pager)

	nonNulls := func() (toDestination, toRecipient *string, count int) {
		testInstance.Helper()
		if err := pool.QueryRow(ctx,
			`SELECT to_destination, to_recipient, num_nonnulls(to_destination, to_recipient) FROM messages WHERE id = $1::text::uuid`,
			original.ID).Scan(&toDestination, &toRecipient, &count); err != nil {
			testInstance.Fatalf("read addressing: %v", err)
		}
		return toDestination, toRecipient, count
	}

	toRecipient := scheduledEnvelope("addressing", deliverAt)
	toRecipient.ToDestination = ""
	toRecipient.ToRecipient = hash
	if _, replaced, err := store.ReplaceScheduledMessage(ctx, toRecipient, []message.DeliveryTarget{recipientTarget}, scheduleBase); err != nil || !replaced {
		testInstance.Fatalf("replace to recipient = replaced %v, err %v; want true", replaced, err)
	}
	destination, recipient, count := nonNulls()
	if destination != nil || recipient == nil || *recipient != hash || count != 1 {
		testInstance.Errorf("addressing after destination->recipient = (%v, %v, %d), want (nil, hash, 1)", destination, recipient, count)
	}
	got := mustGet(ctx, testInstance, store, original.ID)
	if len(got.Deliveries) != 1 || got.Deliveries[0].DestinationID != nil ||
		got.Deliveries[0].DestinationName != registry.RecipientDestinationName {
		testInstance.Errorf("deliveries after destination->recipient = %+v, want one @recipient delivery", got.Deliveries)
	}

	toDestination := scheduledEnvelope("addressing", deliverAt)
	if _, replaced, err := store.ReplaceScheduledMessage(ctx, toDestination, []message.DeliveryTarget{pager}, scheduleBase); err != nil || !replaced {
		testInstance.Fatalf("replace to destination = replaced %v, err %v; want true", replaced, err)
	}
	destination, recipient, count = nonNulls()
	if destination == nil || *destination != "pager" || recipient != nil || count != 1 {
		testInstance.Errorf("addressing after recipient->destination = (%v, %v, %d), want (pager, nil, 1)", destination, recipient, count)
	}
	got = mustGet(ctx, testInstance, store, original.ID)
	if names := deliveryNames(got); !slices.Equal(names, []string{"pager"}) || got.Deliveries[0].DestinationID == nil {
		testInstance.Errorf("deliveries after recipient->destination = %+v, want one pager delivery with a destination id", got.Deliveries)
	}
}

func TestReplaceScheduledToAnImmediateTimeReleasesTheMessage(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	now := scheduleBase.Add(2 * time.Hour)
	past := scheduleBase
	exactlyNow := now

	testCases := []struct {
		name      string
		deliverAt *time.Time
	}{
		{name: "absent deliver_at", deliverAt: nil},
		{name: "past deliver_at", deliverAt: &past},
		{name: "deliver_at equal to now", deliverAt: &exactlyNow},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			key := "immediate " + testCase.name
			key = strings.ReplaceAll(key, " ", "-")
			original := mustInsertScheduled(ctx, subTest, store, scheduledEnvelope(key, scheduleBase.Add(24*time.Hour)), pager)
			replacement := sampleEnvelope(key)
			replacement.DeliverAt = testCase.deliverAt

			replaced, wasReplaced, err := store.ReplaceScheduledMessage(ctx, replacement, []message.DeliveryTarget{pager, audit}, now)
			if err != nil || !wasReplaced {
				subTest.Fatalf("ReplaceScheduledMessage = replaced %v, err %v; want true", wasReplaced, err)
			}
			if replaced.ID != original.ID {
				subTest.Errorf("id = %s, want %s", replaced.ID, original.ID)
			}
			got := mustGet(ctx, subTest, store, original.ID)
			if len(got.Deliveries) != 2 || !allDeliveriesIn(got, message.StatePending) {
				subTest.Fatalf("deliveries = %+v, want 2 pending", got.Deliveries)
			}
			for _, delivery := range got.Deliveries {
				if !delivery.NextAttemptAt.Equal(now) || !delivery.BudgetStartedAt.Equal(now) {
					subTest.Errorf("delivery %s next_attempt_at %v, budget_started_at %v; want both %v",
						delivery.DestinationName, delivery.NextAttemptAt, delivery.BudgetStartedAt, now)
				}
			}
			if got.Envelope.DeliverAt == nil || !got.Envelope.DeliverAt.Equal(now) {
				subTest.Errorf("deliver_at = %v, want the release time %v", got.Envelope.DeliverAt, now)
			}
			// Drain the claims: the subtests share one store, so earlier subtests
			// leave their own pending deliveries behind. This message's two must
			// be among them.
			claimedForMessage := 0
			for {
				claim, found, claimErr := store.ClaimDue(ctx, now, queueTestLease)
				if claimErr != nil {
					subTest.Fatalf("ClaimDue after an immediate replace: %v", claimErr)
				}
				if !found {
					break
				}
				if claim.Message.ID == original.ID {
					claimedForMessage++
				}
			}
			if claimedForMessage != 2 {
				subTest.Errorf("claimed %d of the replaced message's deliveries, want 2", claimedForMessage)
			}
		})
	}
}

func TestReplaceScheduledToImmediateKeepsFullRetention(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	realNow := time.Now().UTC()
	cutoff := realNow.Add(-time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	past := old

	testCases := []struct {
		name      string
		deliverAt *time.Time
	}{
		{name: "absent", deliverAt: nil},
		{name: "past", deliverAt: &past},
	}
	for _, testCase := range testCases {
		key := "retention-replace-" + testCase.name
		stored := mustInsertScheduled(ctx, testInstance, store,
			scheduledEnvelope(key, realNow.Add(45*24*time.Hour)), pager)
		// The message was scheduled long ago: only created_at is stale.
		if _, execErr := pool.Exec(ctx,
			`UPDATE messages SET created_at = $2 WHERE id = $1::text::uuid`, stored.ID, old); execErr != nil {
			testInstance.Fatalf("backdate created_at: %v", execErr)
		}
		replacement := sampleEnvelope(key)
		replacement.DeliverAt = testCase.deliverAt
		if _, replaced, err := store.ReplaceScheduledMessage(
			ctx, replacement, []message.DeliveryTarget{pager}, realNow); err != nil || !replaced {
			testInstance.Fatalf("%s: ReplaceScheduledMessage = replaced %v, err %v", testCase.name, replaced, err)
		}
		delivered := mustGet(ctx, testInstance, store, stored.ID)
		if _, recordErr := store.RecordAttempt(ctx, successResult(delivered.Deliveries[0].ID)); recordErr != nil {
			testInstance.Fatalf("%s: RecordAttempt: %v", testCase.name, recordErr)
		}
	}

	deleted, err := store.DeleteExpired(ctx, cutoff)
	if err != nil || deleted != 0 {
		testInstance.Errorf("DeleteExpired = %d, err %v; want 0: a message just released by replace kept no retention", deleted, err)
	}
}

func TestReplaceScheduledIsRefusedOnceReleasedOrCancelled(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	now := scheduleBase.Add(2 * time.Hour)

	released := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("released", scheduleBase.Add(time.Hour)), pager)
	if count, err := store.ReleaseDue(ctx, now, 100); err != nil || count != 1 {
		testInstance.Fatalf("ReleaseDue = %d, err %v; want 1", count, err)
	}
	cancelled := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("cancelled", scheduleBase.Add(24*time.Hour)), pager)
	if outcome, err := store.CancelMessage(ctx, cancelled.ID, "u4i", now); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("CancelMessage = %+v, err %v; want cancelled", outcome, err)
	}
	before := countRows(ctx, testInstance, pool, "message_deliveries")

	for name, original := range map[string]message.StoredMessage{"released": released, "cancelled": cancelled} {
		replacement := scheduledEnvelope(original.Envelope.IdempotencyKey, scheduleBase.Add(48*time.Hour))
		replacement.Payload = json.RawMessage(`{"kind":"replaced"}`)
		current, wasReplaced, err := store.ReplaceScheduledMessage(ctx, replacement, []message.DeliveryTarget{audit}, now)
		if err != nil || wasReplaced {
			testInstance.Errorf("%s: replaced %v, err %v; want a refusal without error", name, wasReplaced, err)
		}
		if current.ID != original.ID || payloadKind(testInstance, current) != "deploy" {
			testInstance.Errorf("%s: returned %s with payload %s, want the stored message", name, current.ID, current.Envelope.Payload)
		}
		after := mustGet(ctx, testInstance, store, original.ID)
		if !slices.Equal(deliveryNames(after), []string{"pager"}) || payloadKind(testInstance, after) != "deploy" {
			testInstance.Errorf("%s: stored message changed to %+v", name, after.Deliveries)
		}
	}
	if after := countRows(ctx, testInstance, pool, "message_deliveries"); after != before {
		testInstance.Errorf("message_deliveries rows = %d, want %d unchanged", after, before)
	}

	_, wasReplaced, err := store.ReplaceScheduledMessage(
		ctx, scheduledEnvelope("never-stored", scheduleBase.Add(time.Hour)), []message.DeliveryTarget{pager}, now)
	if !errors.Is(err, message.ErrMessageNotFound) || wasReplaced {
		testInstance.Errorf("replace of an unknown key = replaced %v, err %v; want ErrMessageNotFound", wasReplaced, err)
	}
}

func TestCancelMessageCancelsAScheduledMessageIdempotently(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	byID := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("by-id", scheduleBase.Add(time.Hour)), pager, audit)
	byKey := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("by-key", scheduleBase.Add(time.Hour)), pager)
	cancelledAt := scheduleBase.Add(10 * time.Minute)

	outcome, err := store.CancelMessage(ctx, byID.ID, "u4i", cancelledAt)
	want := message.CancelOutcome{
		MessageID: byID.ID, Status: message.StatusCancelled, Cancelled: true, Transitioned: true, Found: true,
	}
	if err != nil || outcome != want {
		testInstance.Fatalf("CancelMessage = %+v, err %v; want %+v", outcome, err, want)
	}
	got := mustGet(ctx, testInstance, store, byID.ID)
	if len(got.Deliveries) != 2 || !allDeliveriesIn(got, message.StateCancelled) {
		testInstance.Errorf("deliveries = %+v, want 2 cancelled", got.Deliveries)
	}
	if got.CancelledAt == nil || !got.CancelledAt.Equal(cancelledAt) {
		testInstance.Errorf("cancelled_at = %v, want %v", got.CancelledAt, cancelledAt)
	}
	if status := message.DeriveStatus(got.Deliveries); status != message.StatusCancelled {
		testInstance.Errorf("status = %q, want cancelled", status)
	}

	// A second cancel is a no-op that reports cancelled (but not transitioned) and keeps cancelled_at.
	wantRepeat := want
	wantRepeat.Transitioned = false
	repeat, repeatErr := store.CancelMessage(ctx, byID.ID, "u4i", cancelledAt.Add(time.Hour))
	if repeatErr != nil || repeat != wantRepeat {
		testInstance.Errorf("second CancelMessage = %+v, err %v; want %+v", repeat, repeatErr, wantRepeat)
	}
	if again := mustGet(ctx, testInstance, store, byID.ID); again.CancelledAt == nil || !again.CancelledAt.Equal(cancelledAt) {
		testInstance.Errorf("cancelled_at after a repeat = %v, want unchanged %v", again.CancelledAt, cancelledAt)
	}

	keyOutcome, keyErr := store.CancelMessageByKey(ctx, "u4i", "by-key", cancelledAt)
	wantByKey := message.CancelOutcome{
		MessageID: byKey.ID, Status: message.StatusCancelled, Cancelled: true, Transitioned: true, Found: true,
	}
	if keyErr != nil || keyOutcome != wantByKey {
		testInstance.Errorf("CancelMessageByKey = %+v, err %v; want %+v", keyOutcome, keyErr, wantByKey)
	}

	if _, zeroErr := store.CancelMessage(ctx, byID.ID, "u4i", time.Time{}); zeroErr == nil {
		testInstance.Error("CancelMessage with a zero now succeeded")
	}
}

func TestCancelMessageAfterReleaseLeavesDeliveriesUntouched(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	stored := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("late-cancel", scheduleBase.Add(time.Hour)), pager, audit)
	now := scheduleBase.Add(2 * time.Hour)
	if released, err := store.ReleaseDue(ctx, now, 10); err != nil || released != 1 {
		testInstance.Fatalf("ReleaseDue = %d, err %v; want 1", released, err)
	}
	before := mustGet(ctx, testInstance, store, stored.ID)

	outcome, err := store.CancelMessage(ctx, stored.ID, "u4i", now)
	want := message.CancelOutcome{MessageID: stored.ID, Status: message.StatusPending, Cancelled: false, Found: true}
	if err != nil || outcome != want {
		testInstance.Fatalf("CancelMessage after release = %+v, err %v; want %+v", outcome, err, want)
	}
	after := mustGet(ctx, testInstance, store, stored.ID)
	if after.CancelledAt != nil || !allDeliveriesIn(after, message.StatePending) {
		testInstance.Errorf("message changed: cancelled_at %v, deliveries %+v", after.CancelledAt, after.Deliveries)
	}
	for index := range before.Deliveries {
		if !reflect.DeepEqual(before.Deliveries[index], after.Deliveries[index]) {
			testInstance.Errorf("delivery %d changed from %+v to %+v", index, before.Deliveries[index], after.Deliveries[index])
		}
	}

	// A message already delivered reports its real status.
	for _, delivery := range after.Deliveries {
		if _, recordErr := store.RecordAttempt(ctx, successResult(delivery.ID)); recordErr != nil {
			testInstance.Fatalf("RecordAttempt: %v", recordErr)
		}
	}
	delivered, deliveredErr := store.CancelMessageByKey(ctx, "u4i", "late-cancel", now)
	wantDelivered := message.CancelOutcome{MessageID: stored.ID, Status: message.StatusDelivered, Cancelled: false, Found: true}
	if deliveredErr != nil || delivered != wantDelivered {
		testInstance.Errorf("cancel of a delivered message = %+v, err %v; want %+v", delivered, deliveredErr, wantDelivered)
	}
}

func TestCancelMessageReportsNotFoundAndChangesNothing(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	stored := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("mine", scheduleBase.Add(time.Hour)), pager)
	now := scheduleBase

	notFound := message.CancelOutcome{}
	testCases := []struct {
		name string
		run  func() (message.CancelOutcome, error)
	}{
		{"unknown id", func() (message.CancelOutcome, error) {
			return store.CancelMessage(ctx, "00000000-0000-0000-0000-000000000000", "u4i", now)
		}},
		{"non-UUID id", func() (message.CancelOutcome, error) { return store.CancelMessage(ctx, "not-a-uuid", "u4i", now) }},
		{"other app's id", func() (message.CancelOutcome, error) { return store.CancelMessage(ctx, stored.ID, "other_app", now) }},
		{"empty app id", func() (message.CancelOutcome, error) { return store.CancelMessage(ctx, stored.ID, "", now) }},
		{"admin non-UUID id", func() (message.CancelOutcome, error) { return store.AdminCancelMessage(ctx, "not-a-uuid", now) }},
		{"admin unknown id", func() (message.CancelOutcome, error) {
			return store.AdminCancelMessage(ctx, "00000000-0000-0000-0000-000000000000", now)
		}},
		{"unknown key", func() (message.CancelOutcome, error) { return store.CancelMessageByKey(ctx, "u4i", "missing", now) }},
		{"other app's key", func() (message.CancelOutcome, error) { return store.CancelMessageByKey(ctx, "other_app", "mine", now) }},
	}
	for _, testCase := range testCases {
		outcome, err := testCase.run()
		if err != nil || outcome != notFound {
			testInstance.Errorf("%s: outcome %+v, err %v; want not found (%+v)", testCase.name, outcome, err, notFound)
		}
	}
	if got := countState(ctx, testInstance, pool, message.StateScheduled); got != 1 {
		testInstance.Errorf("scheduled deliveries = %d, want the message untouched (1)", got)
	}

	// The operator's unscoped cancel reaches any app's message.
	admin, adminErr := store.AdminCancelMessage(ctx, stored.ID, now)
	if adminErr != nil || !admin.Cancelled || !admin.Found {
		testInstance.Errorf("AdminCancelMessage = %+v, err %v; want cancelled", admin, adminErr)
	}
}

func TestDeleteExpiredHonorsScheduledCancelledAndDeliverAtAge(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	targets := []message.DeliveryTarget{pager}
	realNow := time.Now().UTC()
	cutoff := realNow.Add(-time.Hour)
	old := cutoff.Add(-24 * time.Hour)
	recent := realNow.Add(-10 * time.Minute)
	farFuture := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	scheduled := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("old-scheduled", farFuture), pager)
	cancelledOld := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("cancelled-old", farFuture), pager)
	cancelledRecent := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("cancelled-recent", farFuture), pager)
	if outcome, err := store.CancelMessage(ctx, cancelledOld.ID, "u4i", old); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("cancel old = %+v, err %v", outcome, err)
	}
	if outcome, err := store.CancelMessage(ctx, cancelledRecent.ID, "u4i", recent); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("cancel recent = %+v, err %v", outcome, err)
	}
	releasedEnvelope := sampleEnvelope("released-old")
	releasedEnvelope.DeliverAt = &recent
	released, _, insertErr := store.InsertMessage(ctx, releasedEnvelope, targets)
	if insertErr != nil {
		testInstance.Fatalf("InsertMessage released: %v", insertErr)
	}
	if _, recordErr := store.RecordAttempt(ctx, successResult(released.Deliveries[0].ID)); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	// An overdue scheduled message (a poller outage longer than retention): its
	// created_at and deliver_at are both past retention, so only the scheduled
	// delivery keeps it.
	overdue := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("overdue-scheduled", farFuture), pager)
	for _, id := range []string{scheduled.ID, cancelledOld.ID, cancelledRecent.ID, released.ID, overdue.ID} {
		if _, execErr := pool.Exec(ctx, `UPDATE messages SET created_at = $2 WHERE id = $1::text::uuid`, id, old); execErr != nil {
			testInstance.Fatalf("backdate created_at: %v", execErr)
		}
	}
	if _, execErr := pool.Exec(ctx,
		`UPDATE messages SET deliver_at = $2 WHERE id = $1::text::uuid`, overdue.ID, old); execErr != nil {
		testInstance.Fatalf("age overdue deliver_at: %v", execErr)
	}

	exists := func(id string) bool {
		_, err := store.GetMessage(ctx, id)
		return err == nil
	}
	deleted, err := store.DeleteExpired(ctx, cutoff)
	if err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpired = %d, err %v; want 1 (the cancelled one past retention)", deleted, err)
	}
	if exists(cancelledOld.ID) {
		testInstance.Error("a cancelled message whose cancelled_at is past retention survived, despite its far-future deliver_at")
	}
	for name, id := range map[string]string{
		"scheduled (created_at past retention)":                    scheduled.ID,
		"overdue scheduled (created_at and deliver_at past)":       overdue.ID,
		"cancelled within retention":                               cancelledRecent.ID,
		"released with created_at old but deliver_at in retention": released.ID,
	} {
		if !exists(id) {
			testInstance.Errorf("%s was deleted", name)
		}
	}

	// Once the released message's deliver_at is also past retention it goes.
	if _, execErr := pool.Exec(ctx, `UPDATE messages SET deliver_at = $2 WHERE id = $1::text::uuid`, released.ID, old); execErr != nil {
		testInstance.Fatalf("age deliver_at: %v", execErr)
	}
	if deleted, err = store.DeleteExpired(ctx, cutoff); err != nil || deleted != 1 {
		testInstance.Fatalf("second DeleteExpired = %d, err %v; want 1", deleted, err)
	}
	if exists(released.ID) {
		testInstance.Error("the released message survived with both created_at and deliver_at past retention")
	}
	if !exists(scheduled.ID) || !exists(cancelledRecent.ID) || !exists(overdue.ID) {
		testInstance.Error("a scheduled or recently cancelled message was deleted")
	}
}

func TestListScheduledOrdersLimitsAndShapesDestinations(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	hash := personhash.Hash("alice@example.com")
	recipientTarget := message.DeliveryTarget{
		DestinationID: nil, DestinationName: registry.RecipientDestinationName, DestinationType: registry.TypeNtfy,
	}

	if empty, total, err := store.ListScheduled(ctx, 10); err != nil || len(empty) != 0 || total != 0 || empty == nil {
		testInstance.Fatalf("empty ListScheduled = %v, total %d, err %v; want a non-nil empty list and 0", empty, total, err)
	}

	late := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("late", scheduleBase.Add(5*time.Hour)), pager, audit)
	recipientEnvelope := scheduledEnvelope("recipient", scheduleBase.Add(2*time.Hour))
	recipientEnvelope.ToDestination = ""
	recipientEnvelope.ToRecipient = hash
	toRecipient := mustInsertScheduled(ctx, testInstance, store, recipientEnvelope, recipientTarget)
	overdue := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("overdue", scheduleBase.Add(-time.Hour)), pager)
	mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("released", scheduleBase.Add(-2*time.Hour)), pager)
	cancelled := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("cancelled", scheduleBase.Add(time.Hour)), pager)
	if outcome, err := store.CancelMessage(ctx, cancelled.ID, "u4i", scheduleBase); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("cancel = %+v, err %v", outcome, err)
	}
	// Release only the earliest message: it is due, the overdue one is not released here.
	if count, err := store.ReleaseDue(ctx, scheduleBase.Add(-90*time.Minute), 10); err != nil || count != 1 {
		testInstance.Fatalf("ReleaseDue = %d, err %v; want 1", count, err)
	}

	listed, total, err := store.ListScheduled(ctx, 10)
	if err != nil {
		testInstance.Fatalf("ListScheduled: %v", err)
	}
	if total != 3 {
		testInstance.Errorf("total = %d, want the 3 scheduled messages", total)
	}
	gotIDs := make([]string, 0, len(listed))
	for _, entry := range listed {
		gotIDs = append(gotIDs, entry.MessageID)
	}
	if want := []string{overdue.ID, toRecipient.ID, late.ID}; !slices.Equal(gotIDs, want) {
		testInstance.Fatalf("listed ids = %v, want deliver_at order %v (overdue first, released and cancelled excluded)", gotIDs, want)
	}

	first, second, third := listed[0], listed[1], listed[2]
	if first.AppID != "u4i" || first.Channel != "alerts" || first.IdempotencyKey != "overdue" ||
		!first.DeliverAt.Equal(scheduleBase.Add(-time.Hour)) || first.CreatedAt.IsZero() || first.Recipient {
		testInstance.Errorf("first entry = %+v, want the overdue message's fields", first)
	}
	if !slices.Equal(first.Destinations, []message.ScheduledDestination{{Name: "pager", Type: registry.TypeNtfy}}) {
		testInstance.Errorf("first destinations = %+v, want [pager]", first.Destinations)
	}
	if !second.Recipient || second.Destinations == nil || len(second.Destinations) != 0 {
		testInstance.Errorf("recipient entry = %+v, want recipient=true with an empty non-nil destinations list", second)
	}
	wantThird := []message.ScheduledDestination{{Name: "audit", Type: registry.TypeNtfy}, {Name: "pager", Type: registry.TypeNtfy}}
	if !slices.Equal(third.Destinations, wantThird) {
		testInstance.Errorf("third destinations = %+v, want %+v sorted by name", third.Destinations, wantThird)
	}

	limited, limitedTotal, limitedErr := store.ListScheduled(ctx, 2)
	if limitedErr != nil || len(limited) != 2 || limitedTotal != 3 {
		testInstance.Errorf("ListScheduled(2) = %d entries, total %d, err %v; want 2 entries and total 3", len(limited), limitedTotal, limitedErr)
	}
	if _, _, zeroErr := store.ListScheduled(ctx, 0); zeroErr == nil {
		testInstance.Error("ListScheduled with limit 0 succeeded")
	}
}

func TestListScheduledBreaksDeliverAtTiesByID(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	deliverAt := scheduleBase.Add(time.Hour)
	for index := range 6 {
		mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope(fmt.Sprintf("tie-%d", index), deliverAt), pager)
	}
	listed, _, err := store.ListScheduled(ctx, 10)
	if err != nil {
		testInstance.Fatalf("ListScheduled: %v", err)
	}
	ids := make([]string, 0, len(listed))
	for _, entry := range listed {
		ids = append(ids, entry.MessageID)
	}
	if !slices.IsSorted(ids) || len(ids) != 6 {
		testInstance.Errorf("tied ids = %v, want 6 in ascending id order", ids)
	}
}

func TestListDeliveriesRejectsScheduledAndCancelledStates(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)
	for _, state := range []string{message.StateScheduled, message.StateCancelled} {
		_, err := store.ListDeliveries(ctx, state, 10)
		var validationErr *message.ValidationError
		if !errors.As(err, &validationErr) || validationErr.Message != "invalid state" {
			testInstance.Errorf("ListDeliveries(%q) err = %v, want ValidationError invalid state", state, err)
		}
	}
}

func TestScheduledAndCancelledDeliveriesNeverAppearInTheLog(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	scheduled := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("log-a", scheduleBase.Add(time.Hour)), pager)
	cancelled := mustInsertScheduled(ctx, testInstance, store, scheduledEnvelope("log-b", scheduleBase.Add(time.Hour)), pager)
	if outcome, err := store.CancelMessage(ctx, cancelled.ID, "u4i", scheduleBase); err != nil || !outcome.Cancelled {
		testInstance.Fatalf("cancel = %+v, err %v", outcome, err)
	}
	for _, state := range []string{message.StatePending, message.StateDelivered, message.StateDead} {
		listed, err := store.ListDeliveries(ctx, state, 50)
		if err != nil {
			testInstance.Fatalf("ListDeliveries %s: %v", state, err)
		}
		for _, delivery := range listed {
			if delivery.MessageID == scheduled.ID || delivery.MessageID == cancelled.ID {
				testInstance.Errorf("%s log lists a scheduled or cancelled message's delivery %+v", state, delivery)
			}
		}
	}
	// Replaying a cancelled delivery is refused as not dead.
	if _, err := store.Replay(ctx, cancelled.Deliveries[0].ID, scheduleBase); !errors.Is(err, message.ErrDeliveryNotDead) {
		testInstance.Errorf("Replay of a cancelled delivery err = %v, want ErrDeliveryNotDead", err)
	}
}

// explainPlan returns the EXPLAIN text of sql with sequential scans disabled
// for the transaction, so a tiny test table cannot make the planner pick a
// Seq Scan over the index the query is designed to use.
func explainPlan(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, sql string, args ...any,
) string {
	testInstance.Helper()
	tx, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		testInstance.Fatalf("begin: %v", beginErr)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, setErr := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); setErr != nil {
		testInstance.Fatalf("disable seqscan: %v", setErr)
	}
	rows, queryErr := tx.Query(ctx, "EXPLAIN "+sql, args...)
	if queryErr != nil {
		testInstance.Fatalf("EXPLAIN: %v", queryErr)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if scanErr := rows.Scan(&line); scanErr != nil {
			testInstance.Fatalf("scan plan: %v", scanErr)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		testInstance.Fatalf("read plan: %v", rowsErr)
	}
	return plan.String()
}

func TestScheduledProbesUseThePartialIndex(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	for index := range 5 {
		mustInsertScheduled(ctx, testInstance, store,
			scheduledEnvelope(fmt.Sprintf("plan-%d", index), scheduleBase.Add(time.Duration(index)*time.Hour)), pager)
	}

	testCases := []struct {
		name string
		sql  string
		args []any
	}{
		{"per-app cap count", message.ScheduledCountSQL, []any{"u4i"}},
		{"global admin list", message.ScheduledListSQL, []any{50}},
		{"global admin total", message.ScheduledTotalSQL, nil},
		{"poller release probe", message.ReleaseProbeSQL, []any{scheduleBase.Add(time.Hour), 100}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			plan := explainPlan(ctx, subTest, pool, testCase.sql, testCase.args...)
			if !strings.Contains(plan, "message_deliveries_scheduled_idx") {
				subTest.Errorf("plan does not use the scheduled-deliveries partial index:\n%s", plan)
			}
			if strings.Contains(plan, "Seq Scan on message_deliveries") {
				subTest.Errorf("plan sequentially scans message_deliveries:\n%s", plan)
			}
		})
	}
}

func TestDeleteExpiredKeepsTheCreatedAtIndexUsable(testInstance *testing.T) {
	ctx, _, _, pool := newMessageStore(testInstance)
	plan := explainPlan(ctx, testInstance, pool, message.DeleteExpiredSQL, scheduleBase, 500)
	if !strings.Contains(plan, "messages_created_at_idx") {
		testInstance.Errorf("retention delete does not use messages_created_at_idx:\n%s", plan)
	}
	// The index could also be picked for sort order alone; the sargable bound
	// must be an index condition, not merely a filter.
	if !createdAtIndexCondPattern.MatchString(plan) {
		testInstance.Errorf("retention delete does not bound messages_created_at_idx by created_at < cutoff:\n%s", plan)
	}
}

var createdAtIndexCondPattern = regexp.MustCompile(`Index Cond: \((m\.)?created_at < `)

//go:build integration

package message_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// queueBaseTime is the fixed clock the queue tests pass as "now". Every seeded
// delivery is pinned to it, so no test depends on the host or database clock.
var queueBaseTime = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

const queueTestLease = 2 * time.Minute

// seedQueue inserts one message whose deliveries go to the given destination
// names, and pins every delivery's next_attempt_at to queueBaseTime. It returns
// the stored message and a name -> delivery id map.
func seedQueue(
	ctx context.Context, testInstance *testing.T, store *message.PGStore,
	registryStore *registry.PGStore, pool *pgxpool.Pool, idempotencyKey string, names ...string,
) (message.StoredMessage, map[string]int64) {
	testInstance.Helper()
	targets := make([]message.DeliveryTarget, 0, len(names))
	for _, name := range names {
		targets = append(targets, seedTarget(ctx, testInstance, registryStore, name))
	}
	stored, _, insertErr := store.InsertMessage(ctx, sampleEnvelope(idempotencyKey), targets)
	if insertErr != nil {
		testInstance.Fatalf("InsertMessage %s: %v", idempotencyKey, insertErr)
	}
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET next_attempt_at = $2 WHERE message_id = $1::text::uuid`,
		stored.ID, queueBaseTime); execErr != nil {
		testInstance.Fatalf("pin next_attempt_at: %v", execErr)
	}
	ids := make(map[string]int64, len(names))
	for _, delivery := range stored.Deliveries {
		ids[delivery.DestinationName] = delivery.ID
	}
	return stored, ids
}

func loadDelivery(
	ctx context.Context, testInstance *testing.T, store *message.PGStore, messageID string, deliveryID int64,
) message.Delivery {
	testInstance.Helper()
	stored, getErr := store.GetMessage(ctx, messageID)
	if getErr != nil {
		testInstance.Fatalf("GetMessage %s: %v", messageID, getErr)
	}
	for _, delivery := range stored.Deliveries {
		if delivery.ID == deliveryID {
			return delivery
		}
	}
	testInstance.Fatalf("delivery %d not found on message %s", deliveryID, messageID)
	return message.Delivery{}
}

func intPointer(value int) *int { return &value }

func stringPointer(value string) *string { return &value }

func successResult(deliveryID int64) message.AttemptResult {
	return message.AttemptResult{
		Record: message.AttemptRecord{
			DeliveryID: deliveryID,
			Outcome:    message.OutcomeSuccess,
			StatusCode: intPointer(200),
			LatencyMS:  12,
			StartedAt:  queueBaseTime,
		},
		NextState: message.StateDelivered,
	}
}

func retryResult(deliveryID int64, nextAttemptAt time.Time) message.AttemptResult {
	return message.AttemptResult{
		Record: message.AttemptRecord{
			DeliveryID: deliveryID,
			Outcome:    message.OutcomeRetryable,
			StatusCode: intPointer(503),
			LatencyMS:  40,
			StartedAt:  queueBaseTime,
		},
		NextState:     message.StatePending,
		NextAttemptAt: nextAttemptAt,
	}
}

func deadResult(deliveryID int64, reason string) message.AttemptResult {
	return message.AttemptResult{
		Record: message.AttemptRecord{
			DeliveryID: deliveryID,
			Outcome:    message.OutcomeTerminal,
			StatusCode: intPointer(400),
			LatencyMS:  8,
			StartedAt:  queueBaseTime,
		},
		NextState:  message.StateDead,
		DeadReason: &reason,
	}
}

// forceState sets a delivery's state directly, bypassing the store.
func forceState(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool,
	deliveryID int64, state string, deadReason *string, updatedAt time.Time,
) {
	testInstance.Helper()
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET state = $2, dead_reason = $3, updated_at = $4 WHERE id = $1`,
		deliveryID, state, deadReason, updatedAt); execErr != nil {
		testInstance.Fatalf("force state of delivery %d: %v", deliveryID, execErr)
	}
}

func TestConcurrentClaimsAreDisjointAndExhaustive(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	const deliveryCount = 20
	const workerCount = 5
	names := make([]string, 0, deliveryCount)
	for index := range deliveryCount {
		names = append(names, fmt.Sprintf("dest%02d", index))
	}
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "fanout", names...)

	start := make(chan struct{})
	claimed := make([][]int64, workerCount)
	var group sync.WaitGroup
	for worker := range workerCount {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for {
				claim, found, claimErr := store.ClaimDue(ctx, queueBaseTime, queueTestLease)
				if claimErr != nil {
					testInstance.Errorf("worker %d ClaimDue: %v", worker, claimErr)
					return
				}
				if !found {
					return
				}
				if !claim.Delivery.NextAttemptAt.Equal(queueBaseTime.Add(queueTestLease)) {
					testInstance.Errorf("worker %d claim next_attempt_at = %v, want lease end", worker, claim.Delivery.NextAttemptAt)
				}
				claimed[worker] = append(claimed[worker], claim.Delivery.ID)
			}
		}()
	}
	close(start)
	group.Wait()

	seen := make(map[int64]int, deliveryCount)
	for worker, workerIDs := range claimed {
		for _, deliveryID := range workerIDs {
			if previous, duplicate := seen[deliveryID]; duplicate {
				testInstance.Errorf("delivery %d claimed by worker %d and worker %d", deliveryID, previous, worker)
			}
			seen[deliveryID] = worker
		}
	}
	if len(seen) != deliveryCount {
		testInstance.Errorf("claimed %d distinct deliveries, want %d", len(seen), deliveryCount)
	}
	for name, deliveryID := range ids {
		if _, ok := seen[deliveryID]; !ok {
			testInstance.Errorf("delivery %s (%d) was never claimed", name, deliveryID)
		}
	}
}

func TestClaimDueReturnsOldestDueDeliveryWithItsMessage(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	stored, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "ordering", "first", "second", "future")
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET next_attempt_at = $2 WHERE id = $1`,
		ids["first"], queueBaseTime.Add(-time.Hour)); execErr != nil {
		testInstance.Fatalf("backdate first: %v", execErr)
	}
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET next_attempt_at = $2 WHERE id = $1`,
		ids["future"], queueBaseTime.Add(time.Hour)); execErr != nil {
		testInstance.Fatalf("postpone future: %v", execErr)
	}

	var claimedIDs []int64
	for {
		claim, found, claimErr := store.ClaimDue(ctx, queueBaseTime, queueTestLease)
		if claimErr != nil {
			testInstance.Fatalf("ClaimDue: %v", claimErr)
		}
		if !found {
			break
		}
		claimedIDs = append(claimedIDs, claim.Delivery.ID)
		if claim.Message.ID != stored.ID || claim.Message.Envelope.AppID != "u4i" ||
			claim.Message.Envelope.IdempotencyKey != "ordering" || len(claim.Message.Envelope.Payload) == 0 {
			testInstance.Errorf("claim message = %+v, want the parent envelope", claim.Message)
		}
		if len(claim.Message.Deliveries) != 0 {
			testInstance.Errorf("claim message carries %d deliveries, want none", len(claim.Message.Deliveries))
		}
		if claim.Delivery.State != message.StatePending {
			testInstance.Errorf("claim delivery state = %q, want pending", claim.Delivery.State)
		}
	}
	want := []int64{ids["first"], ids["second"]}
	if !slices.Equal(claimedIDs, want) {
		testInstance.Errorf("claim order = %v, want %v (oldest first, the future row untouched)", claimedIDs, want)
	}
}

func TestClaimDueSkipsNonPendingDeliveries(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "terminal", "done", "dead")
	forceState(ctx, testInstance, pool, ids["done"], message.StateDelivered, nil, queueBaseTime)
	forceState(ctx, testInstance, pool, ids["dead"], message.StateDead,
		stringPointer(message.DeadReasonTerminalStatus), queueBaseTime)

	if _, found, claimErr := store.ClaimDue(ctx, queueBaseTime, queueTestLease); claimErr != nil || found {
		testInstance.Fatalf("ClaimDue = found %v, err %v; want nothing due", found, claimErr)
	}
}

func TestClaimDueRejectsANonPositiveLease(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	seedQueue(ctx, testInstance, store, registryStore, pool, "lease", "only")
	for _, lease := range []time.Duration{0, -time.Second} {
		if _, found, claimErr := store.ClaimDue(ctx, queueBaseTime, lease); claimErr == nil || found {
			testInstance.Errorf("lease %v: found %v, err %v; want an error", lease, found, claimErr)
		}
	}
}

func TestClaimedRowIsNotReclaimableUntilTheLeaseExpires(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "lease", "only")

	claim, found, claimErr := store.ClaimDue(ctx, queueBaseTime, queueTestLease)
	if claimErr != nil || !found {
		testInstance.Fatalf("first ClaimDue = found %v, err %v", found, claimErr)
	}
	if claim.Delivery.ID != ids["only"] {
		testInstance.Fatalf("claimed %d, want %d", claim.Delivery.ID, ids["only"])
	}

	for _, offset := range []time.Duration{0, queueTestLease - time.Second} {
		if _, again, againErr := store.ClaimDue(ctx, queueBaseTime.Add(offset), queueTestLease); againErr != nil || again {
			testInstance.Fatalf("ClaimDue at +%v = found %v, err %v; want nothing (lease still held)", offset, again, againErr)
		}
	}

	reclaimed, again, againErr := store.ClaimDue(ctx, queueBaseTime.Add(queueTestLease+time.Second), queueTestLease)
	if againErr != nil || !again {
		testInstance.Fatalf("ClaimDue after lease = found %v, err %v; want the row back", again, againErr)
	}
	if reclaimed.Delivery.ID != ids["only"] {
		testInstance.Errorf("reclaimed %d, want %d", reclaimed.Delivery.ID, ids["only"])
	}
}

func TestRecordAttemptSuccessMarksDelivered(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	stored, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "ok", "only")

	updated, recordErr := store.RecordAttempt(ctx, successResult(ids["only"]))
	if recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	if updated.State != message.StateDelivered || updated.DeadReason != nil {
		testInstance.Errorf("state = %q, dead_reason = %v; want delivered, nil", updated.State, updated.DeadReason)
	}
	if updated.DeliveredAt == nil {
		testInstance.Error("delivered_at is nil after a success")
	}
	if updated.AttemptCount != 1 || updated.BudgetAttempts != 1 {
		testInstance.Errorf("attempt_count = %d, budget_attempts = %d; want 1, 1", updated.AttemptCount, updated.BudgetAttempts)
	}
	if updated.LastStatusCode == nil || *updated.LastStatusCode != 200 {
		testInstance.Errorf("last_status_code = %v, want 200", updated.LastStatusCode)
	}
	if reloaded := loadDelivery(ctx, testInstance, store, stored.ID, ids["only"]); reloaded.State != message.StateDelivered {
		testInstance.Errorf("reloaded state = %q, want delivered", reloaded.State)
	}
	if got := countRows(ctx, testInstance, pool, "delivery_attempts"); got != 1 {
		testInstance.Errorf("delivery_attempts rows = %d, want 1", got)
	}
}

func TestRecordAttemptRetryableStaysPendingWithTheGivenNextAttempt(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "retry", "only")
	next := queueBaseTime.Add(90 * time.Second)

	updated, recordErr := store.RecordAttempt(ctx, retryResult(ids["only"], next))
	if recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	if updated.State != message.StatePending || updated.DeliveredAt != nil || updated.DeadReason != nil {
		testInstance.Errorf("delivery = %+v; want pending with no delivered_at or dead_reason", updated)
	}
	if !updated.NextAttemptAt.Equal(next) {
		testInstance.Errorf("next_attempt_at = %v, want %v", updated.NextAttemptAt, next)
	}
	if updated.LastStatusCode == nil || *updated.LastStatusCode != 503 {
		testInstance.Errorf("last_status_code = %v, want 503", updated.LastStatusCode)
	}

	// It is claimable again only from the new next_attempt_at.
	if _, found, claimErr := store.ClaimDue(ctx, queueBaseTime.Add(time.Minute), queueTestLease); claimErr != nil || found {
		testInstance.Errorf("ClaimDue before the retry was due = found %v, err %v; want nothing", found, claimErr)
	}
	if _, found, claimErr := store.ClaimDue(ctx, next, queueTestLease); claimErr != nil || !found {
		testInstance.Errorf("ClaimDue at the retry time = found %v, err %v; want the row", found, claimErr)
	}
}

func TestRecordAttemptDeadSetsTheReason(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "dead", "only")

	updated, recordErr := store.RecordAttempt(ctx, deadResult(ids["only"], message.DeadReasonTerminalStatus))
	if recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	if updated.State != message.StateDead || updated.DeadReason == nil ||
		*updated.DeadReason != message.DeadReasonTerminalStatus {
		testInstance.Errorf("state = %q, dead_reason = %v; want dead, terminal_status", updated.State, updated.DeadReason)
	}
	if updated.DeliveredAt != nil {
		testInstance.Error("delivered_at set on a dead delivery")
	}
	if _, found, claimErr := store.ClaimDue(ctx, queueBaseTime.Add(24*time.Hour), queueTestLease); claimErr != nil || found {
		testInstance.Errorf("ClaimDue of a dead delivery = found %v, err %v; want nothing", found, claimErr)
	}
	attempts, listErr := store.ListAttempts(ctx, ids["only"])
	if listErr != nil || len(attempts) != 1 {
		testInstance.Fatalf("ListAttempts = %d attempts, err %v; want 1", len(attempts), listErr)
	}
	if attempts[0].Outcome != message.OutcomeTerminal || attempts[0].AttemptNumber != 1 || attempts[0].LatencyMS != 8 ||
		attempts[0].StatusCode == nil || *attempts[0].StatusCode != 400 || !attempts[0].StartedAt.Equal(queueBaseTime) {
		testInstance.Errorf("logged attempt = %+v, want the recorded terminal 400", attempts[0])
	}
}

func TestRecordAttemptOnANonPendingDeliveryIsANoOp(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "noop", "only")
	if _, recordErr := store.RecordAttempt(ctx, successResult(ids["only"])); recordErr != nil {
		testInstance.Fatalf("first RecordAttempt: %v", recordErr)
	}

	again, recordErr := store.RecordAttempt(ctx, deadResult(ids["only"], message.DeadReasonGone))
	if recordErr != nil {
		testInstance.Fatalf("second RecordAttempt: %v", recordErr)
	}
	if again.State != message.StateDelivered || again.AttemptCount != 1 || again.DeadReason != nil {
		testInstance.Errorf("delivery after no-op = %+v; want the unchanged delivered row", again)
	}
	if got := countRows(ctx, testInstance, pool, "delivery_attempts"); got != 1 {
		testInstance.Errorf("delivery_attempts rows = %d, want 1 (no-op must not log)", got)
	}
}

func TestConcurrentRecordsOfOneFinalAttemptLogExactlyOnce(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "race", "only")

	const recorders = 6
	start := make(chan struct{})
	var group sync.WaitGroup
	for range recorders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if _, recordErr := store.RecordAttempt(ctx, successResult(ids["only"])); recordErr != nil {
				testInstance.Errorf("RecordAttempt: %v", recordErr)
			}
		}()
	}
	close(start)
	group.Wait()

	if got := countRows(ctx, testInstance, pool, "delivery_attempts"); got != 1 {
		testInstance.Errorf("delivery_attempts rows = %d, want exactly 1", got)
	}
}

func TestRecordAttemptNumbersIncreaseMonotonically(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "numbers", "only")
	for attempt := 1; attempt <= 3; attempt++ {
		next := queueBaseTime.Add(time.Duration(attempt) * time.Minute)
		updated, recordErr := store.RecordAttempt(ctx, retryResult(ids["only"], next))
		if recordErr != nil {
			testInstance.Fatalf("RecordAttempt %d: %v", attempt, recordErr)
		}
		if updated.AttemptCount != attempt || updated.BudgetAttempts != attempt {
			testInstance.Errorf("after attempt %d: attempt_count = %d, budget_attempts = %d", attempt, updated.AttemptCount, updated.BudgetAttempts)
		}
	}

	attempts, listErr := store.ListAttempts(ctx, ids["only"])
	if listErr != nil {
		testInstance.Fatalf("ListAttempts: %v", listErr)
	}
	numbers := make([]int, 0, len(attempts))
	for _, attempt := range attempts {
		numbers = append(numbers, attempt.AttemptNumber)
	}
	if !slices.Equal(numbers, []int{1, 2, 3}) {
		testInstance.Errorf("attempt numbers = %v, want [1 2 3]", numbers)
	}
}

func TestRecordAttemptTruncatesAndSanitizesUntrustedText(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "text", "only")

	result := retryResult(ids["only"], queueBaseTime.Add(time.Minute))
	// 600 two-byte runes for the error; 2 KiB of three-byte runes for the snippet.
	result.Record.Error = stringPointer(strings.Repeat("é", 600))
	result.Record.ResponseSnippet = stringPointer(strings.Repeat("€", 700))
	if _, recordErr := store.RecordAttempt(ctx, result); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}

	// Invalid UTF-8 and NUL bytes (what a hostile or binary response produces)
	// must not fail the insert.
	messy := retryResult(ids["only"], queueBaseTime.Add(2*time.Minute))
	messy.Record.Error = stringPointer("bad\x00bytes\xff")
	messy.Record.ResponseSnippet = stringPointer("\x00\xfe\xffbinary")
	if _, recordErr := store.RecordAttempt(ctx, messy); recordErr != nil {
		testInstance.Fatalf("RecordAttempt with binary text: %v", recordErr)
	}

	attempts, listErr := store.ListAttempts(ctx, ids["only"])
	if listErr != nil || len(attempts) != 2 {
		testInstance.Fatalf("ListAttempts = %d attempts, err %v; want 2", len(attempts), listErr)
	}
	long := attempts[0]
	if long.Error == nil || utf8.RuneCountInString(*long.Error) != 500 {
		testInstance.Errorf("stored error = %v, want 500 runes", long.Error)
	}
	// 341 whole three-byte runes: the 1024-byte cap cut on a rune boundary.
	if derefOrEmpty(long.ResponseSnippet) != strings.Repeat("€", 341) {
		testInstance.Errorf("stored snippet has %d bytes, want 341 whole runes (1023 bytes)",
			len(derefOrEmpty(long.ResponseSnippet)))
	}
	cleaned := attempts[1]
	// Invalid bytes become U+FFFD (a run collapses to one) and NUL bytes vanish.
	wantError := "badbytes" + string(utf8.RuneError)
	if derefOrEmpty(cleaned.Error) != wantError {
		testInstance.Errorf("sanitized error = %q, want %q", derefOrEmpty(cleaned.Error), wantError)
	}
	wantSnippet := string(utf8.RuneError) + "binary"
	if derefOrEmpty(cleaned.ResponseSnippet) != wantSnippet {
		testInstance.Errorf("sanitized snippet = %q, want %q", derefOrEmpty(cleaned.ResponseSnippet), wantSnippet)
	}
}

func derefOrEmpty(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

func TestRecordAttemptRejectsInvalidResultsAndStoresNothing(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	stored, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "invalid", "only")

	deadWithoutReason := deadResult(ids["only"], message.DeadReasonGone)
	deadWithoutReason.DeadReason = nil
	pendingWithReason := retryResult(ids["only"], queueBaseTime)
	pendingWithReason.DeadReason = stringPointer(message.DeadReasonGone)
	unknownState := successResult(ids["only"])
	unknownState.NextState = "archived"
	badReason := deadResult(ids["only"], "made_up_reason")
	badOutcome := successResult(ids["only"])
	badOutcome.Record.Outcome = "exploded"

	for name, result := range map[string]message.AttemptResult{
		"dead without reason": deadWithoutReason,
		"pending with reason": pendingWithReason,
		"unknown state":       unknownState,
		"unknown dead reason": badReason,
		"unknown outcome":     badOutcome,
	} {
		if _, recordErr := store.RecordAttempt(ctx, result); recordErr == nil {
			testInstance.Errorf("%s: RecordAttempt succeeded, want an error", name)
		}
	}
	if got := countRows(ctx, testInstance, pool, "delivery_attempts"); got != 0 {
		testInstance.Errorf("delivery_attempts rows = %d, want 0", got)
	}
	delivery := loadDelivery(ctx, testInstance, store, stored.ID, ids["only"])
	if delivery.State != message.StatePending || delivery.AttemptCount != 0 {
		testInstance.Errorf("delivery changed despite errors: %+v", delivery)
	}
}

func TestRecordAttemptUnknownDelivery(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)
	if _, recordErr := store.RecordAttempt(ctx, successResult(987654)); !errors.Is(recordErr, message.ErrDeliveryNotFound) {
		testInstance.Errorf("RecordAttempt unknown = %v, want ErrDeliveryNotFound", recordErr)
	}
}

func TestReplayResetsTheBudgetAndKeepsTheAttemptCount(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	stored, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "replay", "dead", "pending", "delivered")
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET state = 'dead', dead_reason = 'budget_exhausted',
attempt_count = 12, budget_attempts = 12, budget_started_at = $2, next_attempt_at = $3 WHERE id = $1`,
		ids["dead"], queueBaseTime.Add(-48*time.Hour), queueBaseTime.Add(-time.Hour)); execErr != nil {
		testInstance.Fatalf("make dead: %v", execErr)
	}
	forceState(ctx, testInstance, pool, ids["delivered"], message.StateDelivered, nil, queueBaseTime)
	replayAt := queueBaseTime.Add(time.Hour)
	// Park the untouched pending row so the replayed one is the only due delivery.
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET next_attempt_at = $2 WHERE id = $1`,
		ids["pending"], replayAt.Add(24*time.Hour)); execErr != nil {
		testInstance.Fatalf("park pending row: %v", execErr)
	}

	replayed, replayErr := store.Replay(ctx, ids["dead"], replayAt)
	if replayErr != nil {
		testInstance.Fatalf("Replay: %v", replayErr)
	}
	if replayed.State != message.StatePending || replayed.DeadReason != nil {
		testInstance.Errorf("state = %q, dead_reason = %v; want pending, nil", replayed.State, replayed.DeadReason)
	}
	if replayed.BudgetAttempts != 0 || !replayed.BudgetStartedAt.Equal(replayAt) || !replayed.NextAttemptAt.Equal(replayAt) {
		testInstance.Errorf("budget = %d started %v next %v; want 0, %v, %v",
			replayed.BudgetAttempts, replayed.BudgetStartedAt, replayed.NextAttemptAt, replayAt, replayAt)
	}
	if replayed.AttemptCount != 12 {
		testInstance.Errorf("attempt_count = %d, want the cumulative 12 kept", replayed.AttemptCount)
	}
	if reloaded := loadDelivery(ctx, testInstance, store, stored.ID, ids["dead"]); reloaded.State != message.StatePending {
		testInstance.Errorf("reloaded state = %q, want pending", reloaded.State)
	}

	// The replayed row is due at the replay time and numbering continues at 13.
	claim, found, claimErr := store.ClaimDue(ctx, replayAt, queueTestLease)
	if claimErr != nil || !found || claim.Delivery.ID != ids["dead"] {
		testInstance.Fatalf("ClaimDue after replay = %+v found %v err %v", claim.Delivery, found, claimErr)
	}
	if _, recordErr := store.RecordAttempt(ctx, successResult(ids["dead"])); recordErr != nil {
		testInstance.Fatalf("RecordAttempt after replay: %v", recordErr)
	}
	attempts, listErr := store.ListAttempts(ctx, ids["dead"])
	if listErr != nil || len(attempts) != 1 || attempts[0].AttemptNumber != 13 {
		testInstance.Errorf("attempts after replay = %+v, err %v; want one attempt numbered 13", attempts, listErr)
	}

	for name, deliveryID := range map[string]int64{"pending": ids["pending"], "delivered": ids["delivered"]} {
		if _, replayErr := store.Replay(ctx, deliveryID, replayAt); !errors.Is(replayErr, message.ErrDeliveryNotDead) {
			testInstance.Errorf("Replay %s = %v, want ErrDeliveryNotDead", name, replayErr)
		}
	}
	if _, replayErr := store.Replay(ctx, 987654, replayAt); !errors.Is(replayErr, message.ErrDeliveryNotFound) {
		testInstance.Errorf("Replay unknown = %v, want ErrDeliveryNotFound", replayErr)
	}
	if _, replayErr := store.Replay(ctx, ids["dead"], time.Time{}); replayErr == nil {
		testInstance.Error("Replay with a zero time succeeded, want an error")
	}
}

func TestDeleteExpired(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	// Fresh messages get created_at from the database clock (now), so the cutoff
	// is real time; an hour of margin absorbs any host/database skew.
	cutoff := time.Now().UTC().Add(-time.Hour)
	old := cutoff.Add(-24 * time.Hour)

	terminal, terminalIDs := seedQueue(ctx, testInstance, store, registryStore, pool, "old-terminal", "t_done", "t_dead")
	pendingOld, pendingIDs := seedQueue(ctx, testInstance, store, registryStore, pool, "old-pending", "p_done", "p_wait")
	fresh, freshIDs := seedQueue(ctx, testInstance, store, registryStore, pool, "fresh", "f_done")

	if _, recordErr := store.RecordAttempt(ctx, successResult(terminalIDs["t_done"])); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	if _, recordErr := store.RecordAttempt(ctx, deadResult(terminalIDs["t_dead"], message.DeadReasonTerminalStatus)); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	if _, recordErr := store.RecordAttempt(ctx, successResult(pendingIDs["p_done"])); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	if _, recordErr := store.RecordAttempt(ctx, successResult(freshIDs["f_done"])); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	for _, messageID := range []string{terminal.ID, pendingOld.ID} {
		if _, execErr := pool.Exec(ctx, `UPDATE messages SET created_at = $2 WHERE id = $1::text::uuid`, messageID, old); execErr != nil {
			testInstance.Fatalf("backdate message: %v", execErr)
		}
	}
	if got := countRows(ctx, testInstance, pool, "delivery_attempts"); got != 4 {
		testInstance.Fatalf("seeded attempts = %d, want 4", got)
	}

	deleted, deleteErr := store.DeleteExpired(ctx, cutoff)
	if deleteErr != nil {
		testInstance.Fatalf("DeleteExpired: %v", deleteErr)
	}
	if deleted != 1 {
		testInstance.Errorf("deleted = %d, want 1", deleted)
	}
	if _, getErr := store.GetMessage(ctx, terminal.ID); !errors.Is(getErr, message.ErrMessageNotFound) {
		testInstance.Errorf("old terminal message: GetMessage = %v, want ErrMessageNotFound", getErr)
	}
	for name, messageID := range map[string]string{"old with a pending delivery": pendingOld.ID, "fresh": fresh.ID} {
		if _, getErr := store.GetMessage(ctx, messageID); getErr != nil {
			testInstance.Errorf("%s message was deleted: %v", name, getErr)
		}
	}
	if got := countRows(ctx, testInstance, pool, "message_deliveries"); got != 3 {
		testInstance.Errorf("message_deliveries rows = %d, want 3 (the deleted message's two cascaded)", got)
	}
	if got := countRows(ctx, testInstance, pool, "delivery_attempts"); got != 2 {
		testInstance.Errorf("delivery_attempts rows = %d, want 2 (the deleted message's two cascaded)", got)
	}

	// Once the last pending delivery finishes, the old message becomes deletable.
	if _, recordErr := store.RecordAttempt(ctx, successResult(pendingIDs["p_wait"])); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	if deleted, deleteErr = store.DeleteExpired(ctx, cutoff); deleteErr != nil || deleted != 1 {
		testInstance.Errorf("second DeleteExpired = %d, err %v; want 1", deleted, deleteErr)
	}
}

func TestDeleteExpiredInBatchesDrainsEverythingAcrossBatches(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	cutoff := time.Now().UTC().Add(-time.Hour)
	old := cutoff.Add(-24 * time.Hour)

	const expiredMessages = 5
	expiredIDs := make([]string, 0, expiredMessages)
	for index := range expiredMessages {
		stored, ids := seedQueue(ctx, testInstance, store, registryStore, pool,
			fmt.Sprintf("batch-old-%d", index), fmt.Sprintf("b%d_a", index), fmt.Sprintf("b%d_b", index))
		for _, deliveryID := range ids {
			if _, recordErr := store.RecordAttempt(ctx, successResult(deliveryID)); recordErr != nil {
				testInstance.Fatalf("RecordAttempt: %v", recordErr)
			}
		}
		expiredIDs = append(expiredIDs, stored.ID)
	}
	pendingOld, _ := seedQueue(ctx, testInstance, store, registryStore, pool, "batch-old-pending", "bp_wait")
	fresh, freshIDs := seedQueue(ctx, testInstance, store, registryStore, pool, "batch-fresh", "bf_done")
	if _, recordErr := store.RecordAttempt(ctx, successResult(freshIDs["bf_done"])); recordErr != nil {
		testInstance.Fatalf("RecordAttempt: %v", recordErr)
	}
	for _, messageID := range append(expiredIDs, pendingOld.ID) {
		if _, execErr := pool.Exec(ctx, `UPDATE messages SET created_at = $2 WHERE id = $1::text::uuid`, messageID, old); execErr != nil {
			testInstance.Fatalf("backdate message: %v", execErr)
		}
	}

	// Batch size 2 over 5 expired messages takes three statements (2, 2, 1): the loop must continue past full
	// batches and stop on the short one.
	deleted, deleteErr := store.DeleteExpiredInBatches(ctx, cutoff, 2)
	if deleteErr != nil {
		testInstance.Fatalf("DeleteExpiredInBatches: %v", deleteErr)
	}
	if deleted != expiredMessages {
		testInstance.Errorf("deleted = %d, want %d", deleted, expiredMessages)
	}
	for _, messageID := range expiredIDs {
		if _, getErr := store.GetMessage(ctx, messageID); !errors.Is(getErr, message.ErrMessageNotFound) {
			testInstance.Errorf("expired message %s: GetMessage = %v, want ErrMessageNotFound", messageID, getErr)
		}
	}
	for name, messageID := range map[string]string{"old with a pending delivery": pendingOld.ID, "fresh": fresh.ID} {
		if _, getErr := store.GetMessage(ctx, messageID); getErr != nil {
			testInstance.Errorf("%s message was deleted: %v", name, getErr)
		}
	}
	if again, againErr := store.DeleteExpiredInBatches(ctx, cutoff, 5); againErr != nil || again != 0 {
		testInstance.Errorf("second pass deleted %d, err %v; want 0", again, againErr)
	}

	if _, invalidErr := store.DeleteExpiredInBatches(ctx, cutoff, 0); invalidErr == nil {
		testInstance.Error("a zero batch size succeeded, want an error")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, canceledErr := store.DeleteExpiredInBatches(canceled, cutoff, 2); !errors.Is(canceledErr, context.Canceled) {
		testInstance.Errorf("canceled context: err = %v, want context.Canceled", canceledErr)
	}
}

func TestListDeliveriesOrdersFiltersAndLimits(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "listing",
		"dead_old", "dead_tie_a", "dead_tie_b", "dead_mid", "delivered", "pending")
	goneReason := stringPointer(message.DeadReasonGone)
	forceState(ctx, testInstance, pool, ids["dead_old"], message.StateDead, goneReason, queueBaseTime.Add(1*time.Minute))
	forceState(ctx, testInstance, pool, ids["dead_mid"], message.StateDead, goneReason, queueBaseTime.Add(2*time.Minute))
	forceState(ctx, testInstance, pool, ids["dead_tie_a"], message.StateDead, goneReason, queueBaseTime.Add(3*time.Minute))
	forceState(ctx, testInstance, pool, ids["dead_tie_b"], message.StateDead, goneReason, queueBaseTime.Add(3*time.Minute))
	forceState(ctx, testInstance, pool, ids["delivered"], message.StateDelivered, nil, queueBaseTime.Add(4*time.Minute))

	idsOf := func(deliveries []message.AdminDelivery) []int64 {
		listed := make([]int64, 0, len(deliveries))
		for _, delivery := range deliveries {
			listed = append(listed, delivery.ID)
		}
		return listed
	}

	dead, deadErr := store.ListDeliveries(ctx, message.StateDead, 50)
	if deadErr != nil {
		testInstance.Fatalf("ListDeliveries dead: %v", deadErr)
	}
	// updated_at DESC, then id DESC: the 3-minute tie resolves to the higher id first.
	wantDead := []int64{ids["dead_tie_b"], ids["dead_tie_a"], ids["dead_mid"], ids["dead_old"]}
	if ids["dead_tie_b"] < ids["dead_tie_a"] {
		wantDead[0], wantDead[1] = ids["dead_tie_a"], ids["dead_tie_b"]
	}
	if !slices.Equal(idsOf(dead), wantDead) {
		testInstance.Errorf("dead order = %v, want %v", idsOf(dead), wantDead)
	}

	limited, limitedErr := store.ListDeliveries(ctx, message.StateDead, 2)
	if limitedErr != nil {
		testInstance.Fatalf("ListDeliveries limited: %v", limitedErr)
	}
	if !slices.Equal(idsOf(limited), wantDead[:2]) {
		testInstance.Errorf("limit 2 = %v, want %v", idsOf(limited), wantDead[:2])
	}

	delivered, deliveredErr := store.ListDeliveries(ctx, message.StateDelivered, 50)
	if deliveredErr != nil || !slices.Equal(idsOf(delivered), []int64{ids["delivered"]}) {
		testInstance.Errorf("delivered = %v, err %v; want only %d", idsOf(delivered), deliveredErr, ids["delivered"])
	}
}

func TestListDeliveriesAndReplayCarryMessageFieldsAndSnapshots(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	stored, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "admin-fields",
		"gone_dest", "recipient_dest")
	envelope := sampleEnvelope("admin-fields")
	deletedReason := stringPointer(message.DeadReasonDestinationDeleted)
	forceState(ctx, testInstance, pool, ids["gone_dest"], message.StateDead, deletedReason, queueBaseTime.Add(2*time.Minute))
	forceState(ctx, testInstance, pool, ids["recipient_dest"], message.StateDead, deletedReason, queueBaseTime.Add(time.Minute))
	// A deleted destination leaves destination_id NULL; a to.recipient delivery is stored as ntfy "@name".
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET destination_id = NULL WHERE id = $1`, ids["gone_dest"]); execErr != nil {
		testInstance.Fatalf("null destination_id: %v", execErr)
	}
	if _, execErr := pool.Exec(ctx,
		`UPDATE message_deliveries SET destination_id = NULL, destination_name = '@alice', destination_type = 'ntfy'
WHERE id = $1`, ids["recipient_dest"]); execErr != nil {
		testInstance.Fatalf("make recipient delivery: %v", execErr)
	}

	listed, listErr := store.ListDeliveries(ctx, message.StateDead, 50)
	if listErr != nil || len(listed) != 2 {
		testInstance.Fatalf("ListDeliveries = %+v, err %v; want 2 rows", listed, listErr)
	}
	// Newest-updated first: the deleted-destination row, then the recipient row.
	if listed[0].ID != ids["gone_dest"] || listed[1].ID != ids["recipient_dest"] {
		testInstance.Fatalf("order = %d, %d; want %d, %d", listed[0].ID, listed[1].ID, ids["gone_dest"], ids["recipient_dest"])
	}
	for _, row := range listed {
		if row.MessageID != stored.ID || row.AppID != envelope.AppID || row.Channel != envelope.Channel {
			testInstance.Errorf("row %d = message %s app %q channel %q; want %s %q %q",
				row.ID, row.MessageID, row.AppID, row.Channel, stored.ID, envelope.AppID, envelope.Channel)
		}
		if row.DestinationID != nil || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
			testInstance.Errorf("row %d = %+v; want a nil destination_id and both timestamps set", row.ID, row)
		}
	}
	if listed[0].DestinationName != "gone_dest" || listed[0].DestinationType == "" {
		testInstance.Errorf("deleted-destination row kept name %q type %q; want the snapshot", listed[0].DestinationName, listed[0].DestinationType)
	}
	if listed[1].DestinationName != "@alice" || listed[1].DestinationType != "ntfy" {
		testInstance.Errorf("recipient row = %q/%q, want @alice/ntfy", listed[1].DestinationName, listed[1].DestinationType)
	}

	replayed, replayErr := store.Replay(ctx, ids["gone_dest"], queueBaseTime.Add(time.Hour))
	if replayErr != nil {
		testInstance.Fatalf("Replay: %v", replayErr)
	}
	if replayed.State != message.StatePending || replayed.MessageID != stored.ID ||
		replayed.AppID != envelope.AppID || replayed.Channel != envelope.Channel ||
		replayed.DestinationName != "gone_dest" || replayed.DestinationType != listed[0].DestinationType ||
		replayed.CreatedAt.IsZero() || replayed.UpdatedAt.IsZero() {
		testInstance.Errorf("replayed = %+v; want the enriched pending row", replayed)
	}
}

func TestListDeliveriesNoMatchIsANonNilEmptySlice(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	seedQueue(ctx, testInstance, store, registryStore, pool, "all-pending", "only")

	for _, state := range []string{message.StateDead, message.StateDelivered} {
		listed, listErr := store.ListDeliveries(ctx, state, 10)
		if listErr != nil {
			testInstance.Fatalf("ListDeliveries %s: %v", state, listErr)
		}
		if listed == nil || len(listed) != 0 {
			testInstance.Errorf("ListDeliveries %s = %#v, want a non-nil empty slice", state, listed)
		}
	}
}

func TestListDeliveriesRejectsBadArguments(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)
	for name, call := range map[string]func() error{
		"unknown state": func() error { _, err := store.ListDeliveries(ctx, "archived", 10); return err },
		"empty state":   func() error { _, err := store.ListDeliveries(ctx, "", 10); return err },
		"zero limit":    func() error { _, err := store.ListDeliveries(ctx, message.StateDead, 0); return err },
		"negative":      func() error { _, err := store.ListDeliveries(ctx, message.StateDead, -1); return err },
	} {
		var validationErr *message.ValidationError
		if callErr := call(); !errors.As(callErr, &validationErr) {
			testInstance.Errorf("%s: err = %v, want *ValidationError", name, callErr)
		}
	}
}

func TestListAttemptsOrdersByAttemptNumber(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "attempts", "only", "other")

	// Insert out of order so the result can only be right if it sorts by attempt_number.
	for _, number := range []int{3, 1, 2} {
		if _, execErr := pool.Exec(ctx,
			`INSERT INTO delivery_attempts (delivery_id, attempt_number, outcome, status_code, latency_ms, error, response_snippet, started_at)
VALUES ($1, $2, 'retryable', 503, $3, $4, $5, $6)`,
			ids["only"], number, number*10, fmt.Sprintf("boom %d", number), fmt.Sprintf("body %d", number),
			queueBaseTime.Add(time.Duration(number)*time.Minute)); execErr != nil {
			testInstance.Fatalf("seed attempt %d: %v", number, execErr)
		}
	}
	if _, execErr := pool.Exec(ctx,
		`INSERT INTO delivery_attempts (delivery_id, attempt_number, outcome, latency_ms, started_at)
VALUES ($1, 1, 'success', 1, $2)`, ids["other"], queueBaseTime); execErr != nil {
		testInstance.Fatalf("seed other delivery's attempt: %v", execErr)
	}

	attempts, listErr := store.ListAttempts(ctx, ids["only"])
	if listErr != nil {
		testInstance.Fatalf("ListAttempts: %v", listErr)
	}
	if len(attempts) != 3 {
		testInstance.Fatalf("attempts = %d, want 3 (another delivery's attempt must not leak in)", len(attempts))
	}
	for index, attempt := range attempts {
		number := index + 1
		if attempt.AttemptNumber != number || attempt.DeliveryID != ids["only"] || attempt.ID == 0 {
			testInstance.Errorf("attempts[%d] = %+v, want attempt_number %d for delivery %d", index, attempt, number, ids["only"])
		}
		if attempt.Outcome != message.OutcomeRetryable || attempt.LatencyMS != number*10 ||
			attempt.StatusCode == nil || *attempt.StatusCode != 503 ||
			derefOrEmpty(attempt.Error) != fmt.Sprintf("boom %d", number) ||
			derefOrEmpty(attempt.ResponseSnippet) != fmt.Sprintf("body %d", number) ||
			!attempt.StartedAt.Equal(queueBaseTime.Add(time.Duration(number)*time.Minute)) {
			testInstance.Errorf("attempts[%d] fields = %+v", index, attempt)
		}
	}
}

func TestListAttemptsForADeliveryWithNoAttemptsIsEmptyNotMissing(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	_, ids := seedQueue(ctx, testInstance, store, registryStore, pool, "fresh", "only")

	attempts, listErr := store.ListAttempts(ctx, ids["only"])
	if listErr != nil {
		testInstance.Fatalf("ListAttempts: %v", listErr)
	}
	if attempts == nil || len(attempts) != 0 {
		testInstance.Errorf("attempts = %#v, want a non-nil empty slice", attempts)
	}
}

func TestListAttemptsUnknownDelivery(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)
	if _, listErr := store.ListAttempts(ctx, 987654); !errors.Is(listErr, message.ErrDeliveryNotFound) {
		testInstance.Errorf("ListAttempts unknown = %v, want ErrDeliveryNotFound", listErr)
	}
}

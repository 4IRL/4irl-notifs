package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
)

const (
	testWaitTimeout  = 3 * time.Second
	testPollInterval = 5 * time.Millisecond
	testLease        = 90 * time.Second
	testClockStep    = 50 * time.Millisecond

	secretURL        = "https://hooks.example.test/services/T0000/B0000/super-secret-path"
	secretSigning    = "super-secret-signing-value-0123456789"
	payloadMarker    = "PAYLOAD-MARKER-4f9c1"
	testDestination  = "ops"
	testClockStartAt = "2026-10-16T12:00:00Z"
)

var testClockStart = func() time.Time {
	parsed, parseErr := time.Parse(time.RFC3339, testClockStartAt)
	if parseErr != nil {
		panic(parseErr)
	}
	return parsed
}()

// stepClock returns a strictly increasing time that advances by step on every call, so a worker that reads
// the clock twice around a sink call measures exactly one step of latency.
type stepClock struct {
	mutex   sync.Mutex
	current time.Time
	step    time.Duration
}

func (clock *stepClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	value := clock.current
	clock.current = clock.current.Add(clock.step)
	return value
}

// fakeQueue hands out its claims once each and records every attempt it is given.
type fakeQueue struct {
	mutex        sync.Mutex
	pending      []message.Claim
	claimErrors  int
	claimCalls   int
	lastClaimNow time.Time
	lastLease    time.Duration
	// recordErrors is how many leading RecordAttempt calls fail; negative means every call fails.
	recordErrors int
	recordCalls  int
	recorded     []message.AttemptResult
	// recordContextErrors holds ctx.Err() as seen by every RecordAttempt call.
	recordContextErrors []error
}

func (queue *fakeQueue) ClaimDue(ctx context.Context, now time.Time, lease time.Duration) (message.Claim, bool, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	queue.claimCalls++
	queue.lastClaimNow = now
	queue.lastLease = lease
	if ctxErr := ctx.Err(); ctxErr != nil {
		return message.Claim{}, false, ctxErr
	}
	if queue.claimErrors > 0 {
		queue.claimErrors--
		return message.Claim{}, false, errors.New("claim failed: database unavailable")
	}
	if len(queue.pending) == 0 {
		return message.Claim{}, false, nil
	}
	claim := queue.pending[0]
	queue.pending = queue.pending[1:]
	return claim, true, nil
}

func (queue *fakeQueue) RecordAttempt(ctx context.Context, result message.AttemptResult) (message.Delivery, error) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	queue.recordCalls++
	queue.recordContextErrors = append(queue.recordContextErrors, ctx.Err())
	if queue.recordErrors != 0 {
		if queue.recordErrors > 0 {
			queue.recordErrors--
		}
		return message.Delivery{}, errors.New("record failed: database unavailable")
	}
	queue.recorded = append(queue.recorded, result)
	return message.Delivery{ID: result.Record.DeliveryID}, nil
}

func (queue *fakeQueue) add(claims ...message.Claim) {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	queue.pending = append(queue.pending, claims...)
}

func (queue *fakeQueue) recordedResults() []message.AttemptResult {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return append([]message.AttemptResult(nil), queue.recorded...)
}

func (queue *fakeQueue) recordContextErrorsSeen() []error {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return append([]error(nil), queue.recordContextErrors...)
}

func (queue *fakeQueue) claimCallCount() int {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return queue.claimCalls
}

func (queue *fakeQueue) recordCallCount() int {
	queue.mutex.Lock()
	defer queue.mutex.Unlock()
	return queue.recordCalls
}

type resolveResult struct {
	destinations []registry.ResolvedDestination
	err          error
}

type disableCall struct {
	name          string
	destinationID int64
	reason        string
}

type resolveArguments struct{ appID, destinationName, channel string }

// fakeResolver resolves any name to a webhook destination unless results says otherwise.
type fakeResolver struct {
	mutex          sync.Mutex
	results        map[string]resolveResult
	destination    registry.ResolvedDestination
	resolveCalls   int
	resolveSeen    []resolveArguments
	disableErr     error
	disabled       []disableCall
	disableCtxErrs []error
}

func (resolver *fakeResolver) Resolve(
	_ context.Context, appID, destinationName, channel string,
) ([]registry.ResolvedDestination, error) {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	resolver.resolveCalls++
	resolver.resolveSeen = append(resolver.resolveSeen, resolveArguments{appID, destinationName, channel})
	if result, ok := resolver.results[destinationName]; ok {
		return result.destinations, result.err
	}
	destination := resolver.destination
	destination.Name = destinationName
	return []registry.ResolvedDestination{destination}, nil
}

func (resolver *fakeResolver) DisableDestination(
	ctx context.Context, name string, destinationID int64, reason string,
) error {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	resolver.disabled = append(resolver.disabled, disableCall{name: name, destinationID: destinationID, reason: reason})
	resolver.disableCtxErrs = append(resolver.disableCtxErrs, ctx.Err())
	return resolver.disableErr
}

func (resolver *fakeResolver) disableContextErrors() []error {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return append([]error(nil), resolver.disableCtxErrs...)
}

func (resolver *fakeResolver) resolveArgumentsSeen() []resolveArguments {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return append([]resolveArguments(nil), resolver.resolveSeen...)
}

func (resolver *fakeResolver) disableCalls() []disableCall {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return append([]disableCall(nil), resolver.disabled...)
}

func (resolver *fakeResolver) resolveCallCount() int {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return resolver.resolveCalls
}

// fakeSink runs deliver for every call and keeps the requests it saw.
type fakeSink struct {
	mutex    sync.Mutex
	deliver  func(ctx context.Context, destination registry.ResolvedDestination, request sink.Request) sink.Outcome
	requests []sink.Request
}

func (fake *fakeSink) Deliver(
	ctx context.Context, destination registry.ResolvedDestination, request sink.Request,
) sink.Outcome {
	fake.mutex.Lock()
	fake.requests = append(fake.requests, request)
	deliver := fake.deliver
	fake.mutex.Unlock()
	if deliver == nil {
		return sink.Outcome{StatusCode: 200}
	}
	return deliver(ctx, destination, request)
}

func (fake *fakeSink) callCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return len(fake.requests)
}

func (fake *fakeSink) seenRequests() []sink.Request {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]sink.Request(nil), fake.requests...)
}

// lockedBuffer is a bytes.Buffer safe for a slog handler writing from several goroutines.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *lockedBuffer) Write(data []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.Write(data)
}

func (locked *lockedBuffer) String() string {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.String()
}

func (locked *lockedBuffer) Reset() {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	locked.buffer.Reset()
}

type harness struct {
	queue    *fakeQueue
	resolver *fakeResolver
	sink     *fakeSink
	clock    *stepClock
	logs     *lockedBuffer
	worker   *Worker
	cancel   context.CancelFunc
	done     chan struct{}
}

// newHarness builds a single-loop worker (so event order is deterministic) on a fake clock with the fakes
// wired in. mutate may adjust the config before the worker is built.
func newHarness(t *testing.T, mutate func(config *WorkerConfig)) *harness {
	t.Helper()
	logs := &lockedBuffer{}
	harnessInstance := &harness{
		queue: &fakeQueue{},
		resolver: &fakeResolver{destination: registry.ResolvedDestination{
			ID:      7,
			Type:    registry.TypeWebhook,
			Config:  map[string]string{},
			Secrets: map[string]string{"url": secretURL, "signing_secret": secretSigning},
		}},
		sink:  &fakeSink{},
		clock: &stepClock{current: testClockStart, step: testClockStep},
		logs:  logs,
		done:  make(chan struct{}),
	}
	config := WorkerConfig{
		Queue:            harnessInstance.queue,
		Resolver:         harnessInstance.resolver,
		Sinks:            map[string]sink.Sink{registry.TypeWebhook: harnessInstance.sink},
		Logger:           slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Concurrency:      1,
		PollInterval:     testPollInterval,
		Lease:            testLease,
		AttemptTimeout:   5 * time.Second,
		RecordRetryDelay: time.Millisecond,
		Now:              harnessInstance.clock.Now,
		Random:           func() float64 { return 0 },
	}
	if mutate != nil {
		mutate(&config)
	}
	harnessInstance.worker = NewWorker(config)
	return harnessInstance
}

func (harnessInstance *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	harnessInstance.cancel = cancel
	go func() {
		defer close(harnessInstance.done)
		harnessInstance.worker.Run(ctx)
	}()
}

// stop cancels the worker and fails the test if Run does not return promptly.
func (harnessInstance *harness) stop(t *testing.T) {
	t.Helper()
	harnessInstance.cancel()
	select {
	case <-harnessInstance.done:
	case <-time.After(testWaitTimeout):
		t.Fatal("Run did not return after the context was canceled")
	}
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

func (harnessInstance *harness) waitForRecords(t *testing.T, count int) []message.AttemptResult {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d recorded attempts", count), func() bool {
		return len(harnessInstance.queue.recordedResults()) >= count
	})
	return harnessInstance.queue.recordedResults()
}

func newClaim(deliveryID int64, destinationName string) message.Claim {
	payload, marshalErr := json.Marshal(map[string]string{"note": payloadMarker})
	if marshalErr != nil {
		panic(marshalErr)
	}
	// 7 is the ID the fake resolver gives every destination it resolves.
	destinationID := int64(7)
	return message.Claim{
		Delivery: message.Delivery{
			ID:              deliveryID,
			MessageID:       fmt.Sprintf("msg-%d", deliveryID),
			DestinationID:   &destinationID,
			DestinationName: destinationName,
			DestinationType: registry.TypeWebhook,
			State:           message.StatePending,
			AttemptCount:    2,
			BudgetAttempts:  0,
			BudgetStartedAt: testClockStart,
		},
		Message: message.StoredMessage{
			ID:        fmt.Sprintf("msg-%d", deliveryID),
			CreatedAt: testClockStart.Add(-time.Minute),
			Envelope: message.Envelope{
				AppID:          "urls4irl",
				Channel:        "alerts",
				IdempotencyKey: fmt.Sprintf("key-%d", deliveryID),
				Payload:        payload,
			},
		},
	}
}

func TestNewWorkerAppliesDefaults(t *testing.T) {
	worker := NewWorker(WorkerConfig{})
	config := worker.config
	if config.Concurrency != 4 || config.PollInterval != time.Second || config.Lease != 2*time.Minute ||
		config.AttemptTimeout != 9*time.Second {
		t.Errorf("defaults = %d / %v / %v / %v, want 4 / 1s / 2m / 9s",
			config.Concurrency, config.PollInterval, config.Lease, config.AttemptTimeout)
	}
	if config.Now == nil || config.Random == nil || config.Logger == nil {
		t.Error("Now, Random and Logger must default to non-nil values")
	}
	if fraction := config.Random(); fraction < 0 || fraction >= 1 {
		t.Errorf("default Random() = %v, want [0, 1)", fraction)
	}
}

func TestWorkerRecordsSuccess(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	claim := newClaim(11, testDestination)
	harnessInstance.queue.add(claim)
	harnessInstance.start()
	defer harnessInstance.stop(t)

	results := harnessInstance.waitForRecords(t, 1)
	result := results[0]
	if result.NextState != message.StateDelivered || result.Record.Outcome != message.OutcomeSuccess ||
		result.DeadReason != nil {
		t.Fatalf("result = %+v, want a delivered success", result)
	}
	if result.Record.DeliveryID != 11 {
		t.Errorf("DeliveryID = %d, want 11", result.Record.DeliveryID)
	}
	// Decide stamps StartedAt = now and LatencyMS = 0; the worker must overwrite both with measured values.
	if result.Record.LatencyMS != int(testClockStep.Milliseconds()) {
		t.Errorf("LatencyMS = %d, want %d (one clock step around the sink call)",
			result.Record.LatencyMS, testClockStep.Milliseconds())
	}
	if result.Record.StartedAt.IsZero() || !result.Record.StartedAt.After(testClockStart) {
		t.Errorf("StartedAt = %v, want the measured start time after %v", result.Record.StartedAt, testClockStart)
	}
	if result.Record.StatusCode == nil || *result.Record.StatusCode != 200 {
		t.Errorf("StatusCode = %v, want 200", result.Record.StatusCode)
	}

	requests := harnessInstance.sink.seenRequests()
	if len(requests) != 1 {
		t.Fatalf("sink calls = %d, want 1", len(requests))
	}
	if requests[0].MessageID != claim.Message.ID || requests[0].Envelope.IdempotencyKey != "key-11" ||
		!requests[0].CreatedAt.Equal(claim.Message.CreatedAt) {
		t.Errorf("sink request = %+v, want the claimed message", requests[0])
	}
	harnessInstance.queue.mutex.Lock()
	lease := harnessInstance.queue.lastLease
	harnessInstance.queue.mutex.Unlock()
	if lease != testLease {
		t.Errorf("ClaimDue lease = %v, want %v", lease, testLease)
	}

	wantResolve := resolveArguments{
		appID:           claim.Message.Envelope.AppID,
		destinationName: claim.Delivery.DestinationName,
		channel:         claim.Message.Envelope.Channel,
	}
	if wantResolve.appID != "urls4irl" || wantResolve.channel != "alerts" {
		t.Fatalf("test claim envelope = %+v, want app urls4irl and channel alerts", wantResolve)
	}
	if seen := harnessInstance.resolver.resolveArgumentsSeen(); len(seen) != 1 || seen[0] != wantResolve {
		t.Errorf("Resolve arguments = %+v, want exactly one call with %+v", seen, wantResolve)
	}
}

func TestWorkerRetryableFailureStaysPendingAtRetryAfter(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	retryAfter := 5 * time.Minute
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{StatusCode: 503, RetryAfter: &retryAfter}
	}
	harnessInstance.queue.add(newClaim(12, testDestination))
	harnessInstance.start()
	defer harnessInstance.stop(t)

	result := harnessInstance.waitForRecords(t, 1)[0]
	if result.NextState != message.StatePending || result.DeadReason != nil ||
		result.Record.Outcome != message.OutcomeRetryable {
		t.Fatalf("result = %+v, want a pending retryable", result)
	}
	// Retry-After (5 min) beats the first-attempt backoff ceiling (30 s); the clock is measured after the call.
	finished := result.Record.StartedAt.Add(testClockStep)
	if want := finished.Add(retryAfter); !result.NextAttemptAt.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want %v", result.NextAttemptAt, want)
	}
}

func TestWorkerGoneDisablesDestinationOnceAndRecordsDeadGone(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{StatusCode: 410}
	}
	harnessInstance.queue.add(newClaim(13, testDestination))
	harnessInstance.start()
	defer harnessInstance.stop(t)

	result := harnessInstance.waitForRecords(t, 1)[0]
	if result.NextState != message.StateDead || result.DeadReason == nil || *result.DeadReason != message.DeadReasonGone ||
		result.Record.Outcome != message.OutcomeGone {
		t.Fatalf("result = %+v, want dead/gone", result)
	}
	calls := harnessInstance.resolver.disableCalls()
	if len(calls) != 1 || calls[0].name != testDestination || calls[0].reason != "webhook returned 410 Gone" {
		t.Errorf("DisableDestination calls = %+v, want one for %q with the 410 reason", calls, testDestination)
	}
	// The disable is conditional on the ID this attempt resolved (7 is the ID the fake resolver hands out), so a
	// destination re-created under the same name cannot be disabled by the old endpoint's 410.
	if len(calls) == 1 && calls[0].destinationID != 7 {
		t.Errorf("DisableDestination destination id = %d, want the resolved id 7", calls[0].destinationID)
	}
}

func TestWorkerGoneForAReplacedDestinationIsRecordedWithoutAnErrorLog(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	// The registry answers not-found when the ID no longer matches the name: the destination was deleted and
	// re-created between resolve and disable.
	harnessInstance.resolver.disableErr = fmt.Errorf("wrapped: %w", registry.ErrDestinationNotFound)
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{StatusCode: 410}
	}
	harnessInstance.queue.add(newClaim(15, testDestination))
	harnessInstance.start()

	result := harnessInstance.waitForRecords(t, 1)[0]
	harnessInstance.stop(t)
	if result.NextState != message.StateDead || *result.DeadReason != message.DeadReasonGone {
		t.Errorf("result = %+v, want dead/gone", result)
	}
	logged := harnessInstance.logs.String()
	if strings.Contains(logged, "disable destination after 410 failed") || strings.Contains(logged, "level=ERROR") {
		t.Errorf("a replaced destination is not a disable failure:\n%s", logged)
	}
	if !strings.Contains(logged, "not disabled") {
		t.Errorf("the skipped disable was not logged:\n%s", logged)
	}
}

func TestWorkerRecordsOutcomeEvenWhenDisableFails(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.resolver.disableErr = errors.New("disable failed")
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{StatusCode: 410}
	}
	harnessInstance.queue.add(newClaim(14, testDestination))
	harnessInstance.start()
	defer harnessInstance.stop(t)

	result := harnessInstance.waitForRecords(t, 1)[0]
	if result.NextState != message.StateDead || *result.DeadReason != message.DeadReasonGone {
		t.Errorf("result = %+v, want dead/gone despite the disable failure", result)
	}
	harnessInstance.stop(t)
	if !strings.Contains(harnessInstance.logs.String(), "disable destination after 410 failed") {
		t.Error("the disable failure was not logged")
	}
}

func TestWorkerDeadLettersWithoutSending(t *testing.T) {
	tests := []struct {
		name        string
		destName    string
		resolve     *resolveResult
		mutateClaim func(claim *message.Claim)
		wantReason  string
		wantError   string
	}{
		{
			name: "unknown destination", destName: testDestination,
			resolve:    &resolveResult{err: fmt.Errorf("wrapped: %w", registry.ErrUnknownDestination)},
			wantReason: message.DeadReasonDestinationDeleted, wantError: errorTextDestinationDeleted,
		},
		{
			name: "disabled destination", destName: testDestination,
			resolve:    &resolveResult{err: registry.ErrDestinationDisabled},
			wantReason: message.DeadReasonDestinationDisabled, wantError: errorTextDestinationDisabled,
		},
		{
			name: "empty destination name", destName: "",
			wantReason: message.DeadReasonDestinationDeleted, wantError: errorTextDestinationDeleted,
		},
		{
			name: "unregistered destination type", destName: testDestination,
			resolve: &resolveResult{destinations: []registry.ResolvedDestination{
				{ID: 7, Name: testDestination, Type: registry.TypeNtfy},
			}},
			wantReason: message.DeadReasonUnsupportedType, wantError: errorTextUnsupportedType,
		},
		{
			// destination_id is ON DELETE SET NULL: the original destination is gone, and a same-named one must
			// not receive the message.
			name: "destination id cleared by a delete", destName: testDestination,
			mutateClaim: func(claim *message.Claim) { claim.Delivery.DestinationID = nil },
			wantReason:  message.DeadReasonDestinationDeleted, wantError: errorTextDestinationDeleted,
		},
		{
			name: "same name but a different destination", destName: testDestination,
			resolve: &resolveResult{destinations: []registry.ResolvedDestination{
				{ID: 99, Name: testDestination, Type: registry.TypeWebhook},
			}},
			wantReason: message.DeadReasonDestinationDeleted, wantError: errorTextDestinationDeleted,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harnessInstance := newHarness(t, nil)
			if testCase.resolve != nil {
				harnessInstance.resolver.results = map[string]resolveResult{testCase.destName: *testCase.resolve}
			}
			claim := newClaim(20, testCase.destName)
			if testCase.mutateClaim != nil {
				testCase.mutateClaim(&claim)
			}
			harnessInstance.queue.add(claim)
			harnessInstance.start()
			defer harnessInstance.stop(t)

			result := harnessInstance.waitForRecords(t, 1)[0]
			if result.NextState != message.StateDead || result.DeadReason == nil ||
				*result.DeadReason != testCase.wantReason {
				t.Fatalf("result = %+v, want dead/%s", result, testCase.wantReason)
			}
			record := result.Record
			if record.Outcome != message.OutcomeTerminal || record.LatencyMS != 0 || record.StatusCode != nil ||
				record.Error == nil || *record.Error != testCase.wantError || record.StartedAt.IsZero() {
				t.Errorf("record = %+v, want terminal, zero latency, no status and error %q", record, testCase.wantError)
			}
			if got := harnessInstance.sink.callCount(); got != 0 {
				t.Errorf("sink calls = %d, want 0", got)
			}
			if testCase.destName == "" && harnessInstance.resolver.resolveCallCount() != 0 {
				t.Error("an empty destination name must not reach Resolve (it would fan out by route)")
			}
			if len(harnessInstance.resolver.disableCalls()) != 0 {
				t.Error("DisableDestination must not be called when nothing was sent")
			}
		})
	}
}

func TestWorkerKeepsRunningAfterClaimError(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.queue.claimErrors = 2
	harnessInstance.queue.add(newClaim(30, testDestination))
	harnessInstance.start()

	harnessInstance.waitForRecords(t, 1)
	if got := harnessInstance.queue.claimCallCount(); got < 3 {
		t.Errorf("ClaimDue calls = %d, want at least 3 (two failures, then the claim)", got)
	}
	harnessInstance.stop(t)
	logged := harnessInstance.logs.String()
	// Only the first failure of a streak is an Error; the rest are Debug so an outage does not flood the log.
	if got := strings.Count(logged, `level=ERROR msg="claim delivery failed"`); got != 1 {
		t.Errorf("claim failures logged at error %d times, want 1 for a streak of 2:\n%s", got, logged)
	}
	if got := strings.Count(logged, `level=DEBUG msg="claim delivery failed"`); got != 1 {
		t.Errorf("claim failures logged at debug %d times, want 1:\n%s", got, logged)
	}
	if !strings.Contains(logged, "consecutive_failures=2") {
		t.Errorf("the second failure did not log consecutive_failures=2:\n%s", logged)
	}
}

func TestClaimBackoff(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second},
		{7, 30 * time.Second},
		{8, 30 * time.Second},
	}
	for _, testCase := range tests {
		if got := claimBackoff(time.Second, testCase.failures); got != testCase.want {
			t.Errorf("claimBackoff(1s, %d) = %v, want %v", testCase.failures, got, testCase.want)
		}
	}
	if got := claimBackoff(time.Minute, 4); got != time.Minute {
		t.Errorf("claimBackoff(1m, 4) = %v, want the poll interval itself (a failure never polls faster)", got)
	}
	if got := claimBackoff(time.Duration(1<<62), 6); got != time.Duration(1<<62) {
		t.Errorf("claimBackoff with a huge interval = %v, want it unchanged and without overflow", got)
	}
}

func TestWorkerKeepsErrorLevelForTheFirstFailureOfEachStreak(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.queue.claimErrors = 1
	harnessInstance.queue.add(newClaim(31, testDestination))
	harnessInstance.start()
	harnessInstance.waitForRecords(t, 1)
	// A success ended the first streak; a new failure must be logged at error again.
	harnessInstance.queue.mutex.Lock()
	harnessInstance.queue.claimErrors = 1
	harnessInstance.queue.mutex.Unlock()
	waitFor(t, "the second streak's error", func() bool {
		return strings.Count(harnessInstance.logs.String(), `level=ERROR msg="claim delivery failed"`) == 2
	})
	harnessInstance.stop(t)
}

func TestWorkerLeavesLeaseWhenResolveFailsUnexpectedly(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.resolver.results = map[string]resolveResult{
		"broken": {err: errors.New("registry database unavailable")},
		"short":  {destinations: nil},
	}
	harnessInstance.queue.add(newClaim(40, "broken"), newClaim(41, "short"), newClaim(42, testDestination))
	harnessInstance.start()

	// Single loop, claims taken in order: once claim 42 is recorded, 40 and 41 were fully processed.
	results := harnessInstance.waitForRecords(t, 1)
	harnessInstance.stop(t)
	if len(results) != 1 || results[0].Record.DeliveryID != 42 {
		t.Fatalf("recorded = %+v, want only delivery 42", results)
	}
	if got := harnessInstance.queue.recordCallCount(); got != 1 {
		t.Errorf("RecordAttempt calls = %d, want 1 (nothing written for the unresolved deliveries)", got)
	}
	if got := strings.Count(harnessInstance.logs.String(), "resolve destination failed"); got != 2 {
		t.Errorf("resolve failures logged %d times, want 2", got)
	}
}

func TestWorkerWakeClaimsBeforePollInterval(t *testing.T) {
	harnessInstance := newHarness(t, func(config *WorkerConfig) { config.PollInterval = time.Hour })
	harnessInstance.start()
	defer harnessInstance.stop(t)

	waitFor(t, "the first (empty) claim", func() bool { return harnessInstance.queue.claimCallCount() >= 1 })
	harnessInstance.queue.add(newClaim(50, testDestination))
	harnessInstance.worker.Wake()

	start := time.Now()
	harnessInstance.waitForRecords(t, 1)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("claim took %v after Wake, want well under the 1h poll interval", elapsed)
	}
}

func TestWakeNeverBlocks(t *testing.T) {
	worker := NewWorker(WorkerConfig{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for range 100 {
			worker.Wake()
		}
	}()
	select {
	case <-finished:
	case <-time.After(testWaitTimeout):
		t.Fatal("Wake blocked with no loop running")
	}
}

func TestWorkerCancelLetsInFlightAttemptFinishAndRunReturns(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var sinkContextErr error
	harnessInstance.sink.deliver = func(ctx context.Context, _ registry.ResolvedDestination, _ sink.Request) sink.Outcome {
		close(entered)
		<-release
		sinkContextErr = ctx.Err()
		return sink.Outcome{StatusCode: 200}
	}
	harnessInstance.queue.add(newClaim(60, testDestination), newClaim(61, testDestination))
	harnessInstance.start()

	select {
	case <-entered:
	case <-time.After(testWaitTimeout):
		t.Fatal("the sink was never called")
	}
	harnessInstance.cancel()
	close(release)
	select {
	case <-harnessInstance.done:
	case <-time.After(testWaitTimeout):
		t.Fatal("Run did not return after cancel")
	}

	if sinkContextErr != nil {
		t.Errorf("the in-flight attempt's context was canceled with the worker: %v", sinkContextErr)
	}
	results := harnessInstance.queue.recordedResults()
	if len(results) != 1 || results[0].Record.DeliveryID != 60 || results[0].NextState != message.StateDelivered {
		t.Errorf("recorded = %+v, want only the in-flight delivery 60, delivered", results)
	}
	if got := harnessInstance.sink.callCount(); got != 1 {
		t.Errorf("sink calls = %d, want 1 (nothing claimed after cancel)", got)
	}
}

func TestWorkerLogsOnlySafeFieldsAtTheRightLevel(t *testing.T) {
	tests := []struct {
		name      string
		outcome   sink.Outcome
		wantLevel string
		avoid     string
	}{
		{
			name:      "success logs at info",
			outcome:   sink.Outcome{StatusCode: 200, ResponseSnippet: secretURL},
			wantLevel: "level=INFO", avoid: "level=WARN",
		},
		{
			name:      "http failure logs at warn",
			outcome:   sink.Outcome{StatusCode: 500, ResponseSnippet: secretSigning + payloadMarker},
			wantLevel: "level=WARN", avoid: "level=INFO",
		},
		{
			name:      "transport error logs at warn",
			outcome:   sink.Outcome{Err: errors.New("dial: connection refused")},
			wantLevel: "level=WARN", avoid: "level=INFO",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harnessInstance := newHarness(t, nil)
			harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
				return testCase.outcome
			}
			harnessInstance.queue.add(newClaim(70, testDestination))
			harnessInstance.start()
			harnessInstance.waitForRecords(t, 1)
			harnessInstance.stop(t)

			logged := harnessInstance.logs.String()
			for _, forbidden := range []string{secretURL, "hooks.example.test", secretSigning, payloadMarker} {
				if strings.Contains(logged, forbidden) {
					t.Errorf("log leaks %q:\n%s", forbidden, logged)
				}
			}
			for _, field := range []string{
				"message_id=msg-70", "delivery_id=70", "destination=ops", "attempt=3", "outcome=", "status_code=", "latency_ms=50",
			} {
				if !strings.Contains(logged, field) {
					t.Errorf("log is missing %q:\n%s", field, logged)
				}
			}
			if !strings.Contains(logged, testCase.wantLevel) || strings.Contains(logged, testCase.avoid) {
				t.Errorf("log level: want %s and not %s:\n%s", testCase.wantLevel, testCase.avoid, logged)
			}
		})
	}
}

func TestWorkerSurvivesSinkPanicAndRecordsRetryable(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	var calls int
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		calls++
		if calls == 1 {
			panic("boom talking to " + secretURL)
		}
		return sink.Outcome{StatusCode: 200}
	}
	harnessInstance.queue.add(newClaim(80, testDestination), newClaim(81, testDestination))
	harnessInstance.start()

	results := harnessInstance.waitForRecords(t, 2)
	harnessInstance.stop(t)
	first, second := results[0], results[1]
	if first.Record.DeliveryID != 80 || first.NextState != message.StatePending ||
		first.Record.Outcome != message.OutcomeRetryable || first.Record.Error == nil ||
		*first.Record.Error != errorTextSinkPanic {
		t.Errorf("panicked attempt = %+v, want a pending retryable with the fixed panic error", first)
	}
	if second.Record.DeliveryID != 81 || second.NextState != message.StateDelivered {
		t.Errorf("next attempt = %+v, want delivery 81 delivered (the loop survived)", second)
	}
	if strings.Contains(harnessInstance.logs.String(), secretURL) {
		t.Error("the panic value leaked into the log")
	}
}

func TestWorkerSurvivesPanicOutsideTheSink(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	panicking := &panicOnceResolver{fakeResolver: harnessInstance.resolver}
	harnessInstance.worker.config.Resolver = panicking
	harnessInstance.queue.add(newClaim(82, testDestination), newClaim(83, testDestination))
	harnessInstance.start()

	results := harnessInstance.waitForRecords(t, 1)
	harnessInstance.stop(t)
	if results[0].Record.DeliveryID != 83 {
		t.Errorf("recorded delivery %d, want 83 after the panicking claim 82", results[0].Record.DeliveryID)
	}
	if !strings.Contains(harnessInstance.logs.String(), "delivery attempt panicked") {
		t.Error("the panic was not logged")
	}
}

type panicOnceResolver struct {
	*fakeResolver
	panicked bool
}

func (resolver *panicOnceResolver) Resolve(
	ctx context.Context, appID, destinationName, channel string,
) ([]registry.ResolvedDestination, error) {
	if !resolver.panicked {
		resolver.panicked = true
		panic("resolver exploded")
	}
	return resolver.fakeResolver.Resolve(ctx, appID, destinationName, channel)
}

func TestWorkerRetriesRecordAfterSuccessfulSendWithoutResending(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.queue.recordErrors = 2
	harnessInstance.queue.add(newClaim(90, testDestination))
	harnessInstance.start()

	results := harnessInstance.waitForRecords(t, 1)
	harnessInstance.stop(t)
	if results[0].NextState != message.StateDelivered {
		t.Errorf("result = %+v, want delivered", results[0])
	}
	if got := harnessInstance.queue.recordCallCount(); got != 3 {
		t.Errorf("RecordAttempt calls = %d, want 3 (two failures, then success)", got)
	}
	if got := harnessInstance.sink.callCount(); got != 1 {
		t.Errorf("sink calls = %d, want 1: a record failure must not resend", got)
	}
}

func TestWorkerGivesUpRecordingAfterBoundedRetriesAndContinues(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.queue.recordErrors = recordAttempts
	harnessInstance.queue.add(newClaim(91, testDestination), newClaim(92, testDestination))
	harnessInstance.start()

	results := harnessInstance.waitForRecords(t, 1)
	harnessInstance.stop(t)
	if results[0].Record.DeliveryID != 92 {
		t.Errorf("recorded delivery %d, want 92 after claim 91 exhausted its retries", results[0].Record.DeliveryID)
	}
	if got := harnessInstance.sink.callCount(); got != 2 {
		t.Errorf("sink calls = %d, want 2 (one per claim, no resend loop)", got)
	}
	if got := harnessInstance.queue.recordCallCount(); got != recordAttempts+1 {
		t.Errorf("RecordAttempt calls = %d, want %d", got, recordAttempts+1)
	}
	if !strings.Contains(harnessInstance.logs.String(), "the outcome is lost") {
		t.Error("the lost outcome was not logged")
	}
}

func TestWorkerConcurrentLoopsProcessEachClaimOnce(t *testing.T) {
	const claimCount = 40
	harnessInstance := newHarness(t, func(config *WorkerConfig) {
		config.Concurrency = 4
		config.Now = time.Now
	})
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		time.Sleep(time.Millisecond)
		return sink.Outcome{StatusCode: 200}
	}
	for index := range claimCount {
		harnessInstance.queue.add(newClaim(int64(1000+index), testDestination))
	}
	harnessInstance.start()

	results := harnessInstance.waitForRecords(t, claimCount)
	harnessInstance.stop(t)
	seen := map[int64]int{}
	for _, result := range results {
		seen[result.Record.DeliveryID]++
	}
	for index := range claimCount {
		if got := seen[int64(1000+index)]; got != 1 {
			t.Errorf("delivery %d recorded %d times, want 1", 1000+index, got)
		}
	}
	if got := harnessInstance.sink.callCount(); got != claimCount {
		t.Errorf("sink calls = %d, want %d", got, claimCount)
	}
}

func TestWorkerDeadLettersAnUnresolvableDeliveryOnceItsBudgetIsSpent(t *testing.T) {
	tests := []struct {
		name            string
		budgetStartedAt time.Time
	}{
		{"budget older than the maximum age", testClockStart.Add(-MaxBudgetAge - time.Hour)},
		{"zero budget start fails closed", time.Time{}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harnessInstance := newHarness(t, nil)
			harnessInstance.resolver.results = map[string]resolveResult{
				"broken": {err: errors.New("registry database unavailable")},
			}
			claim := newClaim(43, "broken")
			claim.Delivery.BudgetStartedAt = testCase.budgetStartedAt
			harnessInstance.queue.add(claim)
			harnessInstance.start()
			defer harnessInstance.stop(t)

			result := harnessInstance.waitForRecords(t, 1)[0]
			if result.NextState != message.StateDead || result.DeadReason == nil ||
				*result.DeadReason != message.DeadReasonBudgetExhausted {
				t.Fatalf("result = %+v, want dead/budget_exhausted", result)
			}
			record := result.Record
			if record.Outcome != message.OutcomeTerminal || record.StatusCode != nil || record.LatencyMS != 0 ||
				record.Error == nil || *record.Error != errorTextUnresolvable {
				t.Errorf("record = %+v, want terminal, no status, zero latency and error %q", record, errorTextUnresolvable)
			}
			if got := harnessInstance.sink.callCount(); got != 0 {
				t.Errorf("sink calls = %d, want 0", got)
			}
			if len(harnessInstance.resolver.disableCalls()) != 0 {
				t.Error("DisableDestination must not be called when nothing was sent")
			}
		})
	}
}

func TestWorkerPostSendContextsStayLiveAfterTheAttemptContextExpired(t *testing.T) {
	tests := []struct {
		name         string
		outcome      func(ctx context.Context) sink.Outcome
		wantOutcome  string
		wantDisables int
	}{
		{
			name:        "transport timeout",
			outcome:     func(ctx context.Context) sink.Outcome { return sink.Outcome{Err: ctx.Err()} },
			wantOutcome: message.OutcomeRetryable, wantDisables: 0,
		},
		{
			name:        "410 returned after the deadline passed",
			outcome:     func(context.Context) sink.Outcome { return sink.Outcome{StatusCode: 410} },
			wantOutcome: message.OutcomeGone, wantDisables: 1,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harnessInstance := newHarness(t, func(config *WorkerConfig) { config.AttemptTimeout = 30 * time.Millisecond })
			var sinkContextErr error
			harnessInstance.sink.deliver = func(ctx context.Context, _ registry.ResolvedDestination, _ sink.Request) sink.Outcome {
				<-ctx.Done()
				sinkContextErr = ctx.Err()
				return testCase.outcome(ctx)
			}
			harnessInstance.queue.add(newClaim(100, testDestination))
			harnessInstance.start()

			result := harnessInstance.waitForRecords(t, 1)[0]
			harnessInstance.stop(t)
			if !errors.Is(sinkContextErr, context.DeadlineExceeded) {
				t.Fatalf("attempt context error = %v, want it expired with DeadlineExceeded", sinkContextErr)
			}
			if result.Record.Outcome != testCase.wantOutcome {
				t.Errorf("outcome = %q, want %q", result.Record.Outcome, testCase.wantOutcome)
			}
			recordErrs := harnessInstance.queue.recordContextErrorsSeen()
			if len(recordErrs) == 0 {
				t.Fatal("RecordAttempt was never called")
			}
			for _, recordErr := range recordErrs {
				if recordErr != nil {
					t.Errorf("RecordAttempt saw a dead context: %v", recordErr)
				}
			}
			disableErrs := harnessInstance.resolver.disableContextErrors()
			if len(disableErrs) != testCase.wantDisables {
				t.Fatalf("DisableDestination calls = %d, want %d", len(disableErrs), testCase.wantDisables)
			}
			for _, disableErr := range disableErrs {
				if disableErr != nil {
					t.Errorf("DisableDestination saw a dead context: %v", disableErr)
				}
			}
		})
	}
}

func TestWorkerStripsATopLevelURLErrorBeforeRecording(t *testing.T) {
	harnessInstance := newHarness(t, nil)
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{Err: &url.Error{Op: "Post", URL: secretURL, Err: errors.New("connection refused")}}
	}
	harnessInstance.queue.add(newClaim(101, testDestination))
	harnessInstance.start()

	result := harnessInstance.waitForRecords(t, 1)[0]
	harnessInstance.stop(t)
	if result.Record.Outcome != message.OutcomeRetryable || result.NextState != message.StatePending {
		t.Errorf("result = %+v, want a pending retryable", result)
	}
	if result.Record.Error == nil || *result.Record.Error != "connection refused" {
		t.Fatalf("recorded error = %v, want the bare cause", result.Record.Error)
	}
	for _, forbidden := range []string{secretURL, "hooks.example.test", "super-secret-path"} {
		if strings.Contains(*result.Record.Error, forbidden) || strings.Contains(harnessInstance.logs.String(), forbidden) {
			t.Errorf("the recorded error or the log leaks %q", forbidden)
		}
	}
}

func TestNewWorkerRaisesATooSmallLeaseToTheFloor(t *testing.T) {
	logs := &lockedBuffer{}
	worker := NewWorker(WorkerConfig{
		Lease:  time.Second,
		Logger: slog.New(slog.NewTextHandler(logs, nil)),
	})
	wantFloor := DefaultAttemptTimeout + recordTimeout + disableTimeout + leaseMargin
	if worker.config.Lease != wantFloor {
		t.Errorf("Lease = %v, want the floor %v", worker.config.Lease, wantFloor)
	}
	logged := logs.String()
	if strings.Count(logged, "level=WARN") != 1 ||
		!strings.Contains(logged, "configured_lease=1s") || !strings.Contains(logged, "effective_lease=46s") {
		t.Errorf("want one warning with the configured and effective lease:\n%s", logged)
	}

	// The floor follows the attempt timeout: a longer attempt needs a longer lease.
	longAttempt := NewWorker(WorkerConfig{AttemptTimeout: 3 * time.Minute, Logger: slog.New(slog.NewTextHandler(&lockedBuffer{}, nil))})
	if want := 3*time.Minute + recordTimeout + disableTimeout + leaseMargin; longAttempt.config.Lease != want {
		t.Errorf("Lease with a 3m attempt timeout = %v, want %v", longAttempt.config.Lease, want)
	}
}

func TestNewWorkerLeavesTheDefaultAndAnAdequateLeaseUntouched(t *testing.T) {
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	if worker := NewWorker(WorkerConfig{Logger: logger}); worker.config.Lease != DefaultLease {
		t.Errorf("default Lease = %v, want %v", worker.config.Lease, DefaultLease)
	}
	if worker := NewWorker(WorkerConfig{Lease: 10 * time.Minute, Logger: logger}); worker.config.Lease != 10*time.Minute {
		t.Errorf("adequate Lease = %v, want 10m", worker.config.Lease)
	}
	if logged := logs.String(); logged != "" {
		t.Errorf("an adequate lease must not warn:\n%s", logged)
	}
}

func TestMaxInFlightShutdownCoversAttemptRecordAndDisable(t *testing.T) {
	if MaxInFlightShutdown != 16*time.Second {
		t.Errorf("MaxInFlightShutdown = %v, want 16s (9s attempt + 5s record + 2s disable)", MaxInFlightShutdown)
	}
}

func TestLatencyMSNeverNegative(t *testing.T) {
	started := testClockStart
	tests := []struct {
		name     string
		finished time.Time
		want     int
	}{
		{"elapsed", started.Add(1500 * time.Millisecond), 1500},
		{"sub-millisecond", started.Add(900 * time.Microsecond), 0},
		{"clock went backwards", started.Add(-time.Second), 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := latencyMS(started, testCase.finished); got != testCase.want {
				t.Errorf("latencyMS = %d, want %d", got, testCase.want)
			}
		})
	}
}

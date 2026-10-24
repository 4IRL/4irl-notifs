package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

type recordedProbe struct {
	checkID       int64
	configVersion int64
	result        ProbeResult
	now           time.Time
	holdOffUntil  time.Time
	ctxErrAtCall  error
	ctxErrAfter   error
}

// fakeProbeStore scripts ClaimDueProbes and records RecordProbeResult. It is the dedicated
// ProbeStore fake for the prober tests.
type fakeProbeStore struct {
	mutex sync.Mutex

	pending      []ProbeClaim
	claimErrs    []error
	claimPanic   bool
	beforeReturn func(ctx context.Context)
	claimLimits  []int
	claimCalled  chan struct{}

	records      []recordedProbe
	recordErr    error
	notApplied   bool
	blockWrite   chan struct{}
	writeEntered chan struct{}
}

func newFakeProbeStore(claims ...ProbeClaim) *fakeProbeStore {
	return &fakeProbeStore{
		pending: claims, claimCalled: make(chan struct{}, 1024), writeEntered: make(chan struct{}, 16),
	}
}

// receiveOrFail waits for a signal and fails the test instead of hanging when it never comes.
func receiveOrFail[Signal any](testInstance *testing.T, channel <-chan Signal, what string) {
	testInstance.Helper()
	select {
	case <-channel:
	case <-time.After(10 * time.Second):
		testInstance.Fatalf("timed out waiting for %s", what)
	}
}

func (store *fakeProbeStore) ClaimDueProbes(ctx context.Context, _ time.Time, limit int) ([]ProbeClaim, error) {
	store.mutex.Lock()
	store.claimLimits = append(store.claimLimits, limit)
	store.claimCalled <- struct{}{}
	if store.claimPanic {
		store.claimPanic = false
		store.mutex.Unlock()
		panic("claim-panic-value")
	}
	if len(store.claimErrs) > 0 {
		claimErr := store.claimErrs[0]
		store.claimErrs = store.claimErrs[1:]
		store.mutex.Unlock()
		return nil, claimErr
	}
	count := min(limit, len(store.pending))
	claims := append([]ProbeClaim(nil), store.pending[:count]...)
	store.pending = store.pending[count:]
	hook := store.beforeReturn
	store.mutex.Unlock()
	if hook != nil {
		hook(ctx)
	}
	return claims, nil
}

func (store *fakeProbeStore) RecordProbeResult(ctx context.Context, checkID int64, configVersion int64, result ProbeResult, now, holdOffUntil time.Time) (StoredCheck, bool, error) {
	record := recordedProbe{
		checkID: checkID, configVersion: configVersion, result: result,
		now: now, holdOffUntil: holdOffUntil, ctxErrAtCall: ctx.Err(),
	}
	select {
	case store.writeEntered <- struct{}{}:
	default:
	}
	store.mutex.Lock()
	block, recordErr, notApplied := store.blockWrite, store.recordErr, store.notApplied
	store.mutex.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
		record.ctxErrAfter = ctx.Err()
	}
	store.mutex.Lock()
	store.records = append(store.records, record)
	store.mutex.Unlock()
	return StoredCheck{}, !notApplied, recordErr
}

func (store *fakeProbeStore) recorded() []recordedProbe {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return append([]recordedProbe(nil), store.records...)
}

func (store *fakeProbeStore) limits() []int {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return append([]int(nil), store.claimLimits...)
}

// proberClock is a settable clock.
type proberClock struct{ nanos atomic.Int64 }

func newProberClock() *proberClock {
	clock := &proberClock{}
	clock.nanos.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return clock
}

func (clock *proberClock) Now() time.Time { return time.Unix(0, clock.nanos.Load()).UTC() }

func (clock *proberClock) Advance(delta time.Duration) { clock.nanos.Add(int64(delta)) }

// eventually polls a condition that another goroutine makes true; the deadline only bounds a
// failing run.
func eventually(testInstance *testing.T, description string, condition func() bool) {
	testInstance.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			testInstance.Fatalf("condition never became true: %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

func debugLogger(buffer *lockedBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type proberFixture struct {
	store     *fakeProbeStore
	clock     *proberClock
	freshness *Freshness
	logs      *lockedBuffer
	holdOff   *atomic.Int64
	config    ProberConfig
}

func newProberFixture(client *http.Client, store *fakeProbeStore) *proberFixture {
	clock := newProberClock()
	fixture := &proberFixture{
		store: store, clock: clock, freshness: NewFreshness(clock.Now),
		logs: &lockedBuffer{}, holdOff: &atomic.Int64{},
	}
	fixture.config = ProberConfig{
		Store:        store,
		HoldOffUntil: func() time.Time { return time.Unix(0, fixture.holdOff.Load()).UTC() },
		Client:       client,
		Interval:     time.Hour,
		Concurrency:  2,
		Now:          clock.Now,
		Logger:       debugLogger(fixture.logs),
		Freshness:    fixture.freshness,
	}
	return fixture
}

func (fixture *proberFixture) prober(testInstance *testing.T) *Prober {
	testInstance.Helper()
	prober, newErr := NewProber(fixture.config)
	if newErr != nil {
		testInstance.Fatalf("NewProber: %v", newErr)
	}
	return prober
}

func okClaim(checkID int64, url string) ProbeClaim {
	claim := probeEvalClaim(url, []int{200}, nil, 5)
	claim.CheckID = checkID
	return claim
}

func TestProberScanPassesClaimClockAndHoldOffAtWriteTime(testInstance *testing.T) {
	var fixture *proberFixture
	laterHoldOff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		// The claim has returned and the request is in flight: the hold-off changes now.
		fixture.holdOff.Store(laterHoldOff.UnixNano())
		writer.WriteHeader(http.StatusOK)
	}))
	claim := okClaim(7, server.URL)
	claim.ConfigVersion = 3
	fixture = newProberFixture(loopbackProbeClient(testInstance, server), newFakeProbeStore(claim))
	fixture.holdOff.Store(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).UnixNano())

	prober := fixture.prober(testInstance)
	if scanErr := prober.Scan(context.Background()); scanErr != nil {
		testInstance.Fatalf("Scan: %v", scanErr)
	}
	prober.Wait()

	records := fixture.store.recorded()
	if len(records) != 1 {
		testInstance.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.checkID != 7 || record.configVersion != 3 {
		testInstance.Fatalf("recorded check %d version %d, want 7 and 3", record.checkID, record.configVersion)
	}
	if !record.now.Equal(fixture.clock.Now()) {
		testInstance.Fatalf("now = %v, want %v", record.now, fixture.clock.Now())
	}
	if !record.holdOffUntil.Equal(laterHoldOff) {
		testInstance.Fatalf("holdOffUntil = %v, want %v (read at write time)", record.holdOffUntil, laterHoldOff)
	}
	if record.result.Reason != nil {
		testInstance.Fatalf("Reason = %q, want success", *record.result.Reason)
	}
	if fixture.store.limits()[0] != 2 {
		testInstance.Fatalf("claim limit = %d, want 2", fixture.store.limits()[0])
	}
}

func TestProberConcurrencyBoundAndClaimLimit(testInstance *testing.T) {
	var current, maxSeen atomic.Int64
	entered := make(chan struct{}, 16)
	release := make(chan struct{}, 16)
	releaseAll := sync.OnceFunc(func() { close(release) })
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		now := current.Add(1)
		for {
			seen := maxSeen.Load()
			if now <= seen || maxSeen.CompareAndSwap(seen, now) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		current.Add(-1)
		writer.WriteHeader(http.StatusOK)
	}))
	testInstance.Cleanup(releaseAll)

	var claims []ProbeClaim
	for index := int64(1); index <= 5; index++ {
		claims = append(claims, okClaim(index, server.URL))
	}
	fixture := newProberFixture(loopbackProbeClient(testInstance, server), newFakeProbeStore(claims...))
	prober := fixture.prober(testInstance)
	ctx := context.Background()

	_ = prober.Scan(ctx)
	<-entered
	<-entered
	firstTick, _ := fixture.freshness.Status()

	// No free slot: no claim call, but freshness still records.
	fixture.clock.Advance(time.Second)
	_ = prober.Scan(ctx)
	if got := fixture.store.limits(); len(got) != 1 {
		testInstance.Fatalf("claim limits after full pass = %v, want one call", got)
	}
	secondTick, _ := fixture.freshness.Status()
	if firstTick == nil || secondTick == nil || !secondTick.After(*firstTick) {
		testInstance.Fatalf("a capacity-skipped pass must record freshness: first %v second %v", firstTick, secondTick)
	}

	// One probe finishes: exactly one slot is free.
	release <- struct{}{}
	eventually(testInstance, "one slot freed", func() bool { return prober.inFlight.Load() == 1 })
	_ = prober.Scan(ctx)
	<-entered
	if got := fixture.store.limits(); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		testInstance.Fatalf("claim limits = %v, want [2 1]", got)
	}

	releaseAll()
	prober.Wait()
	for attempt := 0; len(fixture.store.recorded()) < 5; attempt++ {
		if attempt >= 5 {
			testInstance.Fatalf("only %d of 5 probes recorded", len(fixture.store.recorded()))
		}
		_ = prober.Scan(ctx)
		prober.Wait()
	}
	if maxSeen.Load() > 2 {
		testInstance.Fatalf("max in-flight requests = %d, want <= 2", maxSeen.Load())
	}
	for _, limit := range fixture.store.limits() {
		if limit < 1 || limit > 2 {
			testInstance.Fatalf("claim limit %d outside 1..2", limit)
		}
	}
}

func TestProberFreshness(testInstance *testing.T) {
	testInstance.Run("idle pass records", func(testInstance *testing.T) {
		fixture := newProberFixture(http.DefaultClient, newFakeProbeStore())
		prober := fixture.prober(testInstance)
		if last, _ := fixture.freshness.Status(); last != nil {
			testInstance.Fatalf("fresh tracker already has a tick: %v", last)
		}
		if scanErr := prober.Scan(context.Background()); scanErr != nil {
			testInstance.Fatalf("Scan: %v", scanErr)
		}
		if last, stale := fixture.freshness.Status(); last == nil || stale {
			testInstance.Fatalf("after an idle pass: last %v stale %v", last, stale)
		}
	})

	testInstance.Run("recorded before in-flight probes finish and during hold-off", func(testInstance *testing.T) {
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		releaseAll := sync.OnceFunc(func() { close(release) })
		server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			entered <- struct{}{}
			<-release
			writer.WriteHeader(http.StatusOK)
		}))
		testInstance.Cleanup(releaseAll)
		fixture := newProberFixture(loopbackProbeClient(testInstance, server), newFakeProbeStore(okClaim(1, server.URL)))
		fixture.holdOff.Store(fixture.clock.Now().Add(time.Hour).UnixNano())
		prober := fixture.prober(testInstance)

		_ = prober.Scan(context.Background())
		<-entered
		if last, stale := fixture.freshness.Status(); last == nil || stale {
			testInstance.Fatalf("freshness must be recorded while the probe is blocked: last %v stale %v", last, stale)
		}
		if got := len(fixture.store.recorded()); got != 0 {
			testInstance.Fatalf("result written before the probe finished: %d", got)
		}
		releaseAll()
		prober.Wait()
		if got := fixture.store.recorded(); len(got) != 1 || !got[0].holdOffUntil.After(got[0].now) {
			testInstance.Fatalf("hold-off not passed through: %+v", got)
		}
	})

	testInstance.Run("claim error does not record and logs once", func(testInstance *testing.T) {
		store := newFakeProbeStore()
		store.claimErrs = []error{errors.New("claim-boom")}
		fixture := newProberFixture(http.DefaultClient, store)
		prober := fixture.prober(testInstance)
		if scanErr := prober.Scan(context.Background()); scanErr == nil {
			testInstance.Fatal("Scan returned nil for a claim error")
		}
		if last, _ := fixture.freshness.Status(); last != nil {
			testInstance.Fatalf("a failing pass recorded freshness: %v", last)
		}
		if count := strings.Count(fixture.logs.String(), "heartbeat probe scan failed"); count != 1 {
			testInstance.Fatalf("failure logged %d times, want 1: %s", count, fixture.logs.String())
		}
		if !strings.Contains(fixture.logs.String(), "claim-boom") {
			testInstance.Fatalf("claim error text missing from log: %s", fixture.logs.String())
		}
		if scanErr := prober.Scan(context.Background()); scanErr != nil {
			testInstance.Fatalf("next pass: %v", scanErr)
		}
		if last, _ := fixture.freshness.Status(); last == nil {
			testInstance.Fatal("the next successful pass must record freshness")
		}
	})

	testInstance.Run("wrapped context.Canceled is not logged", func(testInstance *testing.T) {
		store := newFakeProbeStore()
		store.claimErrs = []error{fmt.Errorf("claim: %w", context.Canceled)}
		fixture := newProberFixture(http.DefaultClient, store)
		prober := fixture.prober(testInstance)
		if scanErr := prober.Scan(context.Background()); !errors.Is(scanErr, context.Canceled) {
			testInstance.Fatalf("Scan = %v, want context.Canceled", scanErr)
		}
		if fixture.logs.String() != "" {
			testInstance.Fatalf("a cancellation was logged: %s", fixture.logs.String())
		}
	})
}

// panicOnceTransport panics on its first round trip and delegates afterwards.
type panicOnceTransport struct {
	calls atomic.Int64
	next  http.RoundTripper
}

func (transportUnderTest *panicOnceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transportUnderTest.calls.Add(1) == 1 {
		panic("probe-panic-value")
	}
	return transportUnderTest.next.RoundTrip(request)
}

func TestProberProbePanicIsRecoveredAndSlotReleased(testInstance *testing.T) {
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	client := &http.Client{Transport: &panicOnceTransport{next: loopbackProbeClient(testInstance, server).Transport}}
	fixture := newProberFixture(client, newFakeProbeStore(okClaim(1, server.URL), okClaim(2, server.URL)))
	fixture.config.Concurrency = 1
	prober := fixture.prober(testInstance)
	ctx := context.Background()

	_ = prober.Scan(ctx)
	prober.Wait()
	if prober.inFlight.Load() != 0 {
		testInstance.Fatalf("slot not released after panic: inFlight = %d", prober.inFlight.Load())
	}
	if got := fixture.store.recorded(); len(got) != 0 {
		testInstance.Fatalf("a panicked probe wrote a result: %+v", got)
	}
	logs := fixture.logs.String()
	if !strings.Contains(logs, "heartbeat probe panicked") || !strings.Contains(logs, "check_id=1") {
		testInstance.Fatalf("panic not logged with the check id: %s", logs)
	}
	if strings.Contains(logs, "probe-panic-value") {
		testInstance.Fatalf("panic value leaked into the log: %s", logs)
	}

	_ = prober.Scan(ctx)
	prober.Wait()
	records := fixture.store.recorded()
	if len(records) != 1 || records[0].checkID != 2 {
		testInstance.Fatalf("next scan did not probe check 2: %+v", records)
	}
	if limits := fixture.store.limits(); len(limits) != 2 || limits[1] != 1 {
		testInstance.Fatalf("claim limits = %v, want the slot back (1) on the second scan", limits)
	}
}

func TestProberScanPanicIsRecoveredAndLoopKeepsScanning(testInstance *testing.T) {
	store := newFakeProbeStore()
	store.claimPanic = true
	fixture := newProberFixture(http.DefaultClient, store)
	prober := fixture.prober(testInstance)

	if scanErr := prober.Scan(context.Background()); scanErr == nil {
		testInstance.Fatal("Scan returned nil after a panic")
	}
	logs := fixture.logs.String()
	if !strings.Contains(logs, "heartbeat probe scan panicked") || strings.Contains(logs, "claim-panic-value") {
		testInstance.Fatalf("scan panic log wrong: %s", logs)
	}
	if last, _ := fixture.freshness.Status(); last != nil {
		testInstance.Fatalf("a panicking scan recorded freshness: %v", last)
	}

	// The running loop survives a scan panic and scans again.
	loopStore := newFakeProbeStore()
	loopStore.claimPanic = true
	loopFixture := newProberFixture(http.DefaultClient, loopStore)
	loopFixture.config.Interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunProber(ctx, loopFixture.config)
		close(done)
	}()
	for range 3 {
		<-loopStore.claimCalled
	}
	cancel()
	<-done
	if last, _ := loopFixture.freshness.Status(); last == nil {
		testInstance.Fatal("the scans after the panic should have recorded freshness")
	}
}

func TestProberResultWriteFailureAndDiscard(testInstance *testing.T) {
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	client := loopbackProbeClient(testInstance, server)

	testInstance.Run("write error is logged, not retried, loop continues", func(testInstance *testing.T) {
		store := newFakeProbeStore(okClaim(1, server.URL), okClaim(2, server.URL))
		store.recordErr = errors.New("db-write-boom")
		fixture := newProberFixture(client, store)
		fixture.config.Concurrency = 1
		prober := fixture.prober(testInstance)

		_ = prober.Scan(context.Background())
		prober.Wait()
		logs := fixture.logs.String()
		if !strings.Contains(logs, "heartbeat probe result write failed") || !strings.Contains(logs, "db-write-boom") {
			testInstance.Fatalf("write failure not logged with the store error: %s", logs)
		}
		if got := len(store.recorded()); got != 1 {
			testInstance.Fatalf("write attempted %d times, want 1 (no inline retry)", got)
		}
		if scanErr := prober.Scan(context.Background()); scanErr != nil {
			testInstance.Fatalf("next scan: %v", scanErr)
		}
		prober.Wait()
		if got := len(store.recorded()); got != 2 {
			testInstance.Fatalf("second scan recorded %d total, want 2", got)
		}
	})

	testInstance.Run("applied false is logged as discarded and not retried", func(testInstance *testing.T) {
		store := newFakeProbeStore(okClaim(1, server.URL))
		store.notApplied = true
		fixture := newProberFixture(client, store)
		prober := fixture.prober(testInstance)

		_ = prober.Scan(context.Background())
		prober.Wait()
		logs := fixture.logs.String()
		if !strings.Contains(logs, "heartbeat probe result discarded") || !strings.Contains(logs, "applied=false") {
			testInstance.Fatalf("discard not logged: %s", logs)
		}
		if strings.Contains(logs, "write failed") {
			testInstance.Fatalf("a discard is not a write failure: %s", logs)
		}
		if got := len(store.recorded()); got != 1 {
			testInstance.Fatalf("recorded %d times, want 1", got)
		}
	})
}

func TestProberShutdownLetsInFlightProbeFinishAndWriteOnDetachedContext(testInstance *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseAll := sync.OnceFunc(func() { close(release) })
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		writer.WriteHeader(http.StatusOK)
	}))
	testInstance.Cleanup(releaseAll)
	fixture := newProberFixture(loopbackProbeClient(testInstance, server), newFakeProbeStore(okClaim(1, server.URL)))
	writeGate := make(chan struct{})
	fixture.store.blockWrite = writeGate

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunProber(ctx, fixture.config)
		close(done)
	}()
	receiveOrFail(testInstance, entered, "probe request")
	cancel()
	releaseAll()
	// The result write is held open: only a RunProber that skipped Wait() could have returned.
	receiveOrFail(testInstance, fixture.store.writeEntered, "result write")
	select {
	case <-done:
		testInstance.Fatal("RunProber returned before the in-flight probe's result was written")
	default:
	}
	close(writeGate)
	receiveOrFail(testInstance, done, "RunProber return")

	records := fixture.store.recorded()
	if len(records) != 1 || records[0].ctxErrAfter != nil {
		testInstance.Fatalf("records = %+v, want the in-flight probe written before RunProber returned", len(records))
	}
	if records[0].ctxErrAtCall != nil {
		testInstance.Fatalf("result written on a cancelled context: %v", records[0].ctxErrAtCall)
	}
	if records[0].result.Reason != nil {
		testInstance.Fatalf("the in-flight probe was cancelled by shutdown: %q", *records[0].result.Reason)
	}
	if got := fixture.store.limits(); len(got) != 1 {
		testInstance.Fatalf("claims after cancellation: %v, want exactly the first", got)
	}
}

func TestProberAbandonsAResultWriteBlockedPastItsTimeout(testInstance *testing.T) {
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	store := newFakeProbeStore(okClaim(1, server.URL))
	store.blockWrite = make(chan struct{})
	testInstance.Cleanup(func() { close(store.blockWrite) })
	fixture := newProberFixture(loopbackProbeClient(testInstance, server), store)
	prober := fixture.prober(testInstance)
	if prober.writeTimeout != ProbeResultWriteTimeout {
		testInstance.Fatalf("writeTimeout = %v, want %v", prober.writeTimeout, ProbeResultWriteTimeout)
	}
	prober.writeTimeout = 20 * time.Millisecond

	_ = prober.Scan(context.Background())
	prober.Wait()
	records := store.recorded()
	if len(records) != 1 || !errors.Is(records[0].ctxErrAfter, context.DeadlineExceeded) {
		testInstance.Fatalf("blocked write not abandoned by its deadline: %+v", records)
	}
}

func TestProberClaimedButNotStartedProbesStillRun(testInstance *testing.T) {
	var hits atomic.Int64
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	store := newFakeProbeStore(okClaim(1, server.URL), okClaim(2, server.URL))
	fixture := newProberFixture(loopbackProbeClient(testInstance, server), store)
	ctx, cancel := context.WithCancel(context.Background())
	store.beforeReturn = func(context.Context) { cancel() }

	RunProber(ctx, fixture.config)

	if hits.Load() != 2 {
		testInstance.Fatalf("requests = %d, want both claimed probes requested", hits.Load())
	}
	records := store.recorded()
	if len(records) != 2 {
		testInstance.Fatalf("records = %d, want 2", len(records))
	}
	for _, record := range records {
		if record.ctxErrAtCall != nil || record.result.Reason != nil {
			testInstance.Fatalf("claimed probe not completed cleanly: %+v", record)
		}
	}
	if got := store.limits(); len(got) != 1 {
		testInstance.Fatalf("claims = %v, want no claim after cancellation", got)
	}
}

func TestProberInvalidConfigFailsClosed(testInstance *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProberConfig)
	}{
		{"nil store", func(config *ProberConfig) { config.Store = nil }},
		{"nil hold-off", func(config *ProberConfig) { config.HoldOffUntil = nil }},
		{"nil client", func(config *ProberConfig) { config.Client = nil }},
		{"nil freshness", func(config *ProberConfig) { config.Freshness = nil }},
		{"zero interval", func(config *ProberConfig) { config.Interval = 0 }},
		{"negative interval", func(config *ProberConfig) { config.Interval = -time.Second }},
		{"zero concurrency", func(config *ProberConfig) { config.Concurrency = 0 }},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			store := newFakeProbeStore()
			fixture := newProberFixture(http.DefaultClient, store)
			testCase.mutate(&fixture.config)

			if _, newErr := NewProber(fixture.config); newErr == nil {
				testInstance.Fatal("NewProber accepted an invalid configuration")
			}
			RunProber(context.Background(), fixture.config)
			if !strings.Contains(fixture.logs.String(), "heartbeat prober not started: invalid configuration") {
				testInstance.Fatalf("not-started line missing: %s", fixture.logs.String())
			}
			if len(store.limits()) != 0 {
				testInstance.Fatal("an invalid prober claimed work")
			}
		})
	}

	testInstance.Run("nil sealer and defaults are valid", func(testInstance *testing.T) {
		fixture := newProberFixture(http.DefaultClient, newFakeProbeStore())
		fixture.config.Sealer, fixture.config.Now, fixture.config.Logger = nil, nil, nil
		if _, newErr := NewProber(fixture.config); newErr != nil {
			testInstance.Fatalf("NewProber: %v", newErr)
		}
	})
}

func TestProberLogsNeverCarryTheTargetOrCredentials(testInstance *testing.T) {
	const (
		secretQuery  = "token=SECRET"
		clientID     = "known-client-id-9f3"
		clientSecret = "known-client-secret-7d1"
	)
	sealer := newProbeEvalSealer(testInstance)
	ownToken, ownSealErr := sealProbeAccessToken(sealer, 1, AccessToken{ClientID: clientID, ClientSecret: clientSecret})
	if ownSealErr != nil {
		testInstance.Fatalf("sealProbeAccessToken: %v", ownSealErr)
	}
	otherCheckToken, sealErr := sealProbeAccessToken(sealer, 999, AccessToken{ClientID: clientID, ClientSecret: clientSecret})
	if sealErr != nil {
		testInstance.Fatalf("sealProbeAccessToken: %v", sealErr)
	}

	okHandler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("hello"))
	})
	type probeCase struct {
		reason string
		server *httptest.Server
		client func(*httptest.Server) *http.Client
		url    func(*httptest.Server) string
		mutate func(*ProbeClaim)
	}
	loopback := func(server *httptest.Server) *http.Client { return loopbackProbeClient(testInstance, server) }
	httpsURL := func(server *httptest.Server) string { return server.URL + "/path?" + secretQuery }

	closedServer := newProbeServer(testInstance, okHandler)
	closedClient := loopbackProbeClient(testInstance, closedServer)
	closedServer.Close()

	cases := []probeCase{
		{
			reason: ReasonHTTPStatus, client: loopback, url: httpsURL,
			server: newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusInternalServerError)
			})),
		},
		{
			// A validly sealed token is opened and sent as headers; the failure log must still omit it.
			reason: ReasonHTTPStatus, client: loopback, url: httpsURL,
			server: newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get(headerAccessClientID) != clientID || request.Header.Get(headerAccessClientSecret) != clientSecret {
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				writer.WriteHeader(http.StatusServiceUnavailable)
			})),
			mutate: func(claim *ProbeClaim) { claim.SealedAccessToken = ownToken },
		},
		{
			reason: ReasonTimeout, client: loopback, url: httpsURL,
			server: newProbeServer(testInstance, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				<-request.Context().Done()
			})),
			mutate: func(claim *ProbeClaim) { claim.Config.TimeoutSeconds = 1 },
		},
		{
			reason: ReasonBodyMismatch, client: loopback, url: httpsURL, server: newProbeServer(testInstance, okHandler),
			mutate: func(claim *ProbeClaim) { needle := "not-in-body"; claim.Config.BodyMatch = &needle },
		},
		{
			reason: ReasonBlocked, url: httpsURL, server: newProbeServer(testInstance, okHandler),
			client: func(*httptest.Server) *http.Client { return transport.New(transport.Config{}) },
		},
		{
			reason: ReasonTLS, url: httpsURL, server: newProbeServer(testInstance, okHandler),
			client: func(*httptest.Server) *http.Client {
				return transport.New(transport.Config{AllowAddr: allowLoopbackAddr})
			},
		},
		{
			reason: ReasonScheme, client: loopback, server: newProbeServer(testInstance, okHandler),
			url: func(server *httptest.Server) string {
				return "http://" + strings.TrimPrefix(server.URL, "https://") + "/path?" + secretQuery
			},
		},
		{
			reason: ReasonNetwork, server: closedServer, url: httpsURL,
			client: func(*httptest.Server) *http.Client { return closedClient },
		},
		{
			reason: ReasonConfig, client: loopback, url: httpsURL, server: newProbeServer(testInstance, okHandler),
			mutate: func(claim *ProbeClaim) { claim.SealedAccessToken = otherCheckToken },
		},
	}

	logs := &lockedBuffer{}
	for _, testCase := range cases {
		claim := okClaim(1, testCase.url(testCase.server))
		if testCase.mutate != nil {
			testCase.mutate(&claim)
		}
		store := newFakeProbeStore(claim)
		fixture := newProberFixture(testCase.client(testCase.server), store)
		fixture.config.Logger = debugLogger(logs)
		fixture.config.Sealer = sealer
		prober := fixture.prober(testInstance)

		_ = prober.Scan(context.Background())
		prober.Wait()
		records := store.recorded()
		if len(records) != 1 || records[0].result.Reason == nil || *records[0].result.Reason != testCase.reason {
			testInstance.Fatalf("%s: records = %+v", testCase.reason, records)
		}
		if !strings.Contains(logs.String(), "reason="+testCase.reason) {
			testInstance.Fatalf("%s: reason missing from the log: %s", testCase.reason, logs.String())
		}
	}

	text := logs.String()
	for _, forbidden := range []string{secretQuery, "SECRET", "/path", clientID, clientSecret} {
		if strings.Contains(text, forbidden) {
			testInstance.Fatalf("log output contains %q:\n%s", forbidden, text)
		}
	}
}

func TestMaxProberShutdownCoversProbeAndWrite(testInstance *testing.T) {
	if want := MaxProbeTimeoutSeconds*time.Second + ProbeResultWriteTimeout; MaxProberShutdown != want {
		testInstance.Fatalf("MaxProberShutdown = %v, want %v", MaxProberShutdown, want)
	}
}

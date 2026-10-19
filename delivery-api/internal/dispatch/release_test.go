package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const releaseTestBatch = 3

// releaseStep is one scripted ReleaseDue result.
type releaseStep struct {
	released int64
	err      error
	panics   bool
}

type releaseCall struct {
	now   time.Time
	limit int
}

// fakeReleaser answers ReleaseDue from a script and then, once the script is
// exhausted, with fallback. It records every call and signals each one.
type fakeReleaser struct {
	mutex    sync.Mutex
	script   []releaseStep
	fallback releaseStep
	calls    []releaseCall
	called   chan struct{}
}

func newFakeReleaser(fallback releaseStep, script ...releaseStep) *fakeReleaser {
	return &fakeReleaser{script: script, fallback: fallback, called: make(chan struct{}, 1024)}
}

func (releaser *fakeReleaser) ReleaseDue(_ context.Context, now time.Time, limit int) (int64, error) {
	releaser.mutex.Lock()
	releaser.calls = append(releaser.calls, releaseCall{now: now, limit: limit})
	step := releaser.fallback
	if len(releaser.script) > 0 {
		step, releaser.script = releaser.script[0], releaser.script[1:]
	}
	releaser.mutex.Unlock()
	select {
	case releaser.called <- struct{}{}:
	default:
	}
	if step.panics {
		panic("store exploded")
	}
	return step.released, step.err
}

func (releaser *fakeReleaser) callsSeen() []releaseCall {
	releaser.mutex.Lock()
	defer releaser.mutex.Unlock()
	return append([]releaseCall(nil), releaser.calls...)
}

func waitForRelease(t *testing.T, releaser *fakeReleaser, count int) {
	t.Helper()
	for range count {
		select {
		case <-releaser.called:
		case <-time.After(testWaitTimeout):
			t.Fatal("timed out waiting for ReleaseDue")
		}
	}
}

// waitForCalls blocks until at least count ReleaseDue calls were made.
func waitForCalls(t *testing.T, releaser *fakeReleaser, count int) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for len(releaser.callsSeen()) < count {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d ReleaseDue calls, saw %d", count, len(releaser.callsSeen()))
		}
		time.Sleep(time.Millisecond)
	}
}

// startReleaser runs RunReleaser and returns a stop function that cancels it
// and waits for it to return.
func startReleaser(
	t *testing.T, releaser DueReleaser, interval time.Duration, maxBatches int, wake func(), logs *lockedBuffer,
) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fixedNow := func() time.Time { return testClockStart }
	go func() {
		defer close(done)
		RunReleaser(ctx, ReleaserConfig{
			Releaser: releaser, Interval: interval, BatchSize: releaseTestBatch, MaxBatchesPerRun: maxBatches,
			Now: fixedNow, Wake: wake, Logger: logger,
		})
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testWaitTimeout):
			t.Error("RunReleaser did not return after cancel")
		}
	}
}

func TestRunReleaserReleasesImmediatelyWithInjectedNowAndBatchSize(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{}, releaseStep{released: 2})
	var wakes atomic.Int32
	logs := &lockedBuffer{}
	// A long interval proves the first run does not wait for a tick.
	stop := startReleaser(t, releaser, time.Hour, 5, func() { wakes.Add(1) }, logs)

	waitForRelease(t, releaser, 1)
	stop()
	calls := releaser.callsSeen()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want exactly the one short-batch call", len(calls))
	}
	if !calls[0].now.Equal(testClockStart) || calls[0].limit != releaseTestBatch {
		t.Errorf("call = %+v, want now %v and limit %d", calls[0], testClockStart, releaseTestBatch)
	}
	if got := wakes.Load(); got != 1 {
		t.Errorf("wakes = %d, want 1", got)
	}
	logged := logs.String()
	if !strings.Contains(logged, "level=INFO") || !strings.Contains(logged, "released=2") {
		t.Errorf("a run that released messages must log the count at info:\n%s", logged)
	}
}

func TestRunReleaserDrainsFullBatchesUntilAShortOneAndWakesOnce(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{},
		releaseStep{released: releaseTestBatch}, releaseStep{released: releaseTestBatch}, releaseStep{released: 1})
	var wakes atomic.Int32
	logs := &lockedBuffer{}
	stop := startReleaser(t, releaser, time.Hour, 50, func() { wakes.Add(1) }, logs)

	waitForCalls(t, releaser, 3)
	stop()
	calls := releaser.callsSeen()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3 (two full batches, then the short one)", len(calls))
	}
	for index, call := range calls {
		if call.limit != releaseTestBatch {
			t.Errorf("call %d limit = %d, want %d", index, call.limit, releaseTestBatch)
		}
	}
	if got := wakes.Load(); got != 1 {
		t.Errorf("wakes = %d, want exactly one per run", got)
	}
	if want := fmt.Sprintf("released=%d", 2*releaseTestBatch+1); !strings.Contains(logs.String(), want) {
		t.Errorf("the log must carry the summed message count (%s):\n%s", want, logs.String())
	}
	if got := strings.Count(logs.String(), "level=INFO"); got != 1 {
		t.Errorf("info lines = %d, want one per run", got)
	}
}

func TestRunReleaserStopsAfterMaxBatchesWhenEveryBatchIsFull(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{released: releaseTestBatch})
	woke := make(chan struct{}, 4)
	logs := &lockedBuffer{}
	const maxBatches = 4
	// A long interval keeps the run under test to the immediate first one.
	stop := startReleaser(t, releaser, time.Hour, maxBatches, func() { woke <- struct{}{} }, logs)

	// The wake comes after the loop, so once it arrives the run is over.
	select {
	case <-woke:
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for the wake")
	}
	stop()
	if got := len(releaser.callsSeen()); got != maxBatches {
		t.Errorf("calls = %d, want exactly MaxBatchesPerRun (%d)", got, maxBatches)
	}
	if len(woke) != 0 {
		t.Errorf("woke more than once in a single run")
	}
	if want := fmt.Sprintf("released=%d", maxBatches*releaseTestBatch); !strings.Contains(logs.String(), want) {
		t.Errorf("the log must carry the capped run's count (%s):\n%s", want, logs.String())
	}
}

func TestRunReleaserNextTickContinuesAfterACappedRun(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{released: releaseTestBatch})
	var wakes atomic.Int32
	const maxBatches = 2
	stop := startReleaser(t, releaser, 5*time.Millisecond, maxBatches, func() { wakes.Add(1) }, &lockedBuffer{})
	defer stop()

	// More than one run's worth of calls can only come from a later tick.
	waitForCalls(t, releaser, 2*maxBatches+1)
}

func TestRunReleaserNonPositiveMaxBatchesTakesTheDefault(t *testing.T) {
	for _, configured := range []int{0, -1} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			releaser := newFakeReleaser(releaseStep{released: releaseTestBatch})
			woke := make(chan struct{}, 4)
			stop := startReleaser(t, releaser, time.Hour, configured, func() { woke <- struct{}{} }, &lockedBuffer{})
			// The wake comes after the loop, so once it arrives the run is over.
			select {
			case <-woke:
			case <-time.After(testWaitTimeout):
				t.Fatal("timed out waiting for the wake")
			}
			stop()
			if got := len(releaser.callsSeen()); got != DefaultMaxReleaseBatches {
				t.Errorf("calls = %d, want DefaultMaxReleaseBatches = %d", got, DefaultMaxReleaseBatches)
			}
		})
	}
}

func TestRunReleaserNonPositiveBatchSizeTakesTheDefault(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunReleaser(ctx, ReleaserConfig{
			Releaser: releaser, Interval: time.Hour, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		})
	}()
	waitForRelease(t, releaser, 1)
	cancel()
	<-done
	if calls := releaser.callsSeen(); len(calls) != 1 || calls[0].limit != DefaultReleaseBatchSize {
		t.Errorf("calls = %+v, want one call with limit %d", calls, DefaultReleaseBatchSize)
	}
}

func TestRunReleaserZeroFirstCallMakesOneCallWithNoWakeAndNoLog(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{})
	var wakes atomic.Int32
	logs := &lockedBuffer{}
	stop := startReleaser(t, releaser, time.Hour, 5, func() { wakes.Add(1) }, logs)
	waitForRelease(t, releaser, 1)
	stop()
	if got := len(releaser.callsSeen()); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
	if wakes.Load() != 0 {
		t.Errorf("wakes = %d, want 0 when nothing was released", wakes.Load())
	}
	if logged := logs.String(); logged != "" {
		t.Errorf("an empty run logged:\n%s", logged)
	}
}

func TestRunReleaserRunsAgainOnEveryTick(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{})
	stop := startReleaser(t, releaser, 5*time.Millisecond, 5, nil, &lockedBuffer{})
	defer stop()
	waitForRelease(t, releaser, 3)
}

func TestRunReleaserErrorMidRunEndsTheRunAndKeepsTheCount(t *testing.T) {
	// The fallback is a full batch, so a run that kept going after the error
	// would keep calling until MaxBatchesPerRun.
	releaser := newFakeReleaser(releaseStep{released: releaseTestBatch},
		releaseStep{released: releaseTestBatch}, releaseStep{err: errors.New("release due: database unavailable")})
	callsAtWake := make(chan int, 4)
	logs := &lockedBuffer{}
	// A long interval keeps the run under test to the immediate first one.
	stop := startReleaser(t, releaser, time.Hour, 50, func() { callsAtWake <- len(releaser.callsSeen()) }, logs)

	// The wake runs right after the loop, so the call count it sees is the
	// run's total.
	var runCalls int
	select {
	case runCalls = <-callsAtWake:
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for the wake")
	}
	stop()
	if runCalls != 2 {
		t.Errorf("calls in the run = %d, want 2: the error must end the run", runCalls)
	}
	if got := len(releaser.callsSeen()); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
	if len(callsAtWake) != 0 {
		t.Errorf("woke more than once in a single run")
	}
	logged := logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "scheduled release failed") {
		t.Errorf("a failed release must log at error:\n%s", logged)
	}
	if !strings.Contains(logged, "level=INFO") || !strings.Contains(logged, fmt.Sprintf("released=%d", releaseTestBatch)) {
		t.Errorf("the batch released before the error must still be logged:\n%s", logged)
	}
}

func TestRunReleaserLoopContinuesOnTheNextTickAfterAMidRunError(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{},
		releaseStep{released: releaseTestBatch}, releaseStep{err: errors.New("release due: database unavailable")})
	stop := startReleaser(t, releaser, 5*time.Millisecond, 50, nil, &lockedBuffer{})
	defer stop()

	// Call 3 comes from a later tick, so the error did not stop the loop.
	waitForCalls(t, releaser, 3)
}

func TestRunReleaserErrorOnFirstCallEndsTheRunWithoutAWake(t *testing.T) {
	// The first call fails and every later one is a full batch, so a run that
	// kept going after the error would call until MaxBatchesPerRun.
	const maxBatches = 4
	releaser := newFakeReleaser(releaseStep{released: releaseTestBatch},
		releaseStep{err: errors.New("release due: database unavailable")})
	callsAtWake := make(chan int, 4)
	stop := startReleaser(t, releaser, 5*time.Millisecond, maxBatches, func() { callsAtWake <- len(releaser.callsSeen()) }, &lockedBuffer{})

	// The failed first run does not wake; the first wake belongs to the next
	// tick's run, and the calls it sees are the failed call plus that full run.
	var callsSeenAtFirstWake int
	select {
	case callsSeenAtFirstWake = <-callsAtWake:
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for the wake")
	}
	stop()
	if want := 1 + maxBatches; callsSeenAtFirstWake != want {
		t.Errorf("calls at the first wake = %d, want %d: the error must end the first run and not wake", callsSeenAtFirstWake, want)
	}
}

func TestRunReleaserDoesNotLogAWrappedContextCancellationAsAFailure(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{err: fmt.Errorf("wrapped: %w", context.Canceled)})
	logs := &lockedBuffer{}
	stop := startReleaser(t, releaser, time.Hour, 5, nil, logs)
	waitForRelease(t, releaser, 1)
	stop()
	if logged := logs.String(); strings.Contains(logged, "scheduled release failed") {
		t.Errorf("a canceled release was logged as a failure:\n%s", logged)
	}
}

func TestRunReleaserSurvivesAPanickingStore(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{panics: true})
	logs := &lockedBuffer{}
	stop := startReleaser(t, releaser, 5*time.Millisecond, 5, nil, logs)
	waitForRelease(t, releaser, 2)
	stop()
	if !strings.Contains(logs.String(), "scheduled release panicked") {
		t.Errorf("the panic was not logged:\n%s", logs.String())
	}
}

func TestRunReleaserWakesForMessagesReleasedBeforeAPanic(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{},
		releaseStep{released: releaseTestBatch}, releaseStep{panics: true})
	var wakes atomic.Int32
	stop := startReleaser(t, releaser, time.Hour, 50, func() { wakes.Add(1) }, &lockedBuffer{})
	waitForCalls(t, releaser, 2)
	stop()
	if got := wakes.Load(); got != 1 {
		t.Errorf("wakes = %d, want 1 for the batch released before the panic", got)
	}
}

func TestRunReleaserSurvivesAPanickingWake(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{released: 1})
	logs := &lockedBuffer{}
	stop := startReleaser(t, releaser, 5*time.Millisecond, 5, func() { panic("wake exploded") }, logs)
	waitForRelease(t, releaser, 2)
	stop()
	if !strings.Contains(logs.String(), "scheduled release wake panicked") {
		t.Errorf("the wake panic was not logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "released=1") {
		t.Errorf("the count must be logged even when the wake panics:\n%s", logs.String())
	}
}

func TestRunReleaserDoesNotReleaseWhenAlreadyCanceled(t *testing.T) {
	releaser := newFakeReleaser(releaseStep{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunReleaser(ctx, ReleaserConfig{
		Releaser: releaser, Interval: time.Hour,
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if got := len(releaser.callsSeen()); got != 0 {
		t.Errorf("calls = %d, want 0 for a canceled context", got)
	}
}

func TestRunReleaserRejectsInvalidConfigurationWithoutLooping(t *testing.T) {
	tests := []struct {
		name     string
		releaser DueReleaser
		interval time.Duration
	}{
		{"nil releaser", nil, time.Hour},
		{"zero interval", newFakeReleaser(releaseStep{}), 0},
		{"negative interval", newFakeReleaser(releaseStep{}), -time.Second},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				RunReleaser(context.Background(), ReleaserConfig{
					Releaser: testCase.releaser, Interval: testCase.interval,
					Logger: slog.New(slog.NewTextHandler(logs, nil)),
				})
			}()
			select {
			case <-done:
			case <-time.After(testWaitTimeout):
				t.Fatal("RunReleaser must return at once on an invalid configuration")
			}
			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("an invalid configuration must be logged at error:\n%s", logs.String())
			}
			if fake, ok := testCase.releaser.(*fakeReleaser); ok && len(fake.callsSeen()) != 0 {
				t.Errorf("an invalid configuration released %d times", len(fake.callsSeen()))
			}
		})
	}
}

package heartbeat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

const checkerWaitTimeout = 3 * time.Second

var checkerClock = time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)

// tickStep is one scripted Tick result.
type tickStep struct {
	err    error
	panics bool
}

// fakeTicker answers Tick from a script and then, once it is exhausted, with
// fallback. It records and signals every call.
type fakeTicker struct {
	mutex    sync.Mutex
	script   []tickStep
	fallback tickStep
	calls    int
	called   chan struct{}
}

func newFakeTicker(fallback tickStep, script ...tickStep) *fakeTicker {
	return &fakeTicker{script: script, fallback: fallback, called: make(chan struct{}, 1024)}
}

func (ticker *fakeTicker) Tick(context.Context) error {
	ticker.mutex.Lock()
	ticker.calls++
	step := ticker.fallback
	if len(ticker.script) > 0 {
		step, ticker.script = ticker.script[0], ticker.script[1:]
	}
	ticker.mutex.Unlock()
	defer func() {
		select {
		case ticker.called <- struct{}{}:
		default:
		}
	}()
	if step.panics {
		panic("store exploded")
	}
	return step.err
}

func (ticker *fakeTicker) callCount() int {
	ticker.mutex.Lock()
	defer ticker.mutex.Unlock()
	return ticker.calls
}

func waitForTicks(t *testing.T, ticker *fakeTicker, count int) {
	t.Helper()
	deadline := time.After(checkerWaitTimeout)
	for range count {
		select {
		case <-ticker.called:
		case <-deadline:
			t.Fatalf("timed out waiting for %d ticks, saw %d", count, ticker.callCount())
		}
	}
}

// startChecker runs RunChecker and returns a stop function that cancels it and
// waits for it to return.
func startChecker(t *testing.T, ticker Ticker, interval time.Duration, freshness *Freshness, logs *lockedBuffer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunChecker(ctx, CheckerConfig{
			Service:   ticker,
			Interval:  interval,
			Now:       func() time.Time { return checkerClock },
			Logger:    slog.New(slog.NewTextHandler(logs, nil)),
			Freshness: freshness,
		})
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(checkerWaitTimeout):
			t.Error("RunChecker did not return after cancel")
		}
	}
}

func newTestFreshness(now time.Time) *Freshness {
	return NewFreshness(func() time.Time { return now })
}

func TestRunCheckerTicksImmediately(t *testing.T) {
	ticker := newFakeTicker(tickStep{})
	// A long interval proves the first tick does not wait for the ticker.
	stop := startChecker(t, ticker, time.Hour, nil, &lockedBuffer{})
	waitForTicks(t, ticker, 1)
	stop()
	if got := ticker.callCount(); got != 1 {
		t.Errorf("ticks = %d, want exactly the immediate one", got)
	}
}

func TestRunCheckerTicksAgainOnEveryInterval(t *testing.T) {
	ticker := newFakeTicker(tickStep{})
	stop := startChecker(t, ticker, 5*time.Millisecond, nil, &lockedBuffer{})
	defer stop()
	waitForTicks(t, ticker, 3)
}

func TestRunCheckerRecordsFreshnessAfterASuccessfulTick(t *testing.T) {
	ticker := newFakeTicker(tickStep{})
	// The tracker's own clock differs from CheckerConfig.Now, so the recorded
	// value can only have come from the config clock.
	freshness := newTestFreshness(checkerClock.Add(10 * time.Second))
	if last, stale := freshness.Status(); last != nil || !stale {
		t.Fatalf("a new tracker = %v, %v, want nil, stale", last, stale)
	}
	stop := startChecker(t, ticker, time.Hour, freshness, &lockedBuffer{})
	defer stop()
	waitForTicks(t, ticker, 1)
	stop()
	last, stale := freshness.Status()
	if last == nil || !last.Equal(checkerClock) || stale {
		t.Errorf("freshness = %v, stale %v, want %v, fresh", last, stale, checkerClock)
	}
}

func TestRunCheckerFailingTickKeepsLoopingAndDoesNotAdvanceFreshness(t *testing.T) {
	ticker := newFakeTicker(tickStep{err: errors.New("database unavailable")})
	freshness := newTestFreshness(checkerClock)
	logs := &lockedBuffer{}
	stop := startChecker(t, ticker, 5*time.Millisecond, freshness, logs)
	waitForTicks(t, ticker, 3)
	stop()

	if last, stale := freshness.Status(); last != nil || !stale {
		t.Errorf("freshness = %v, stale %v, want never ticked after only failing ticks", last, stale)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "heartbeat tick failed") {
		t.Errorf("a failed tick must log at error:\n%s", logs.String())
	}
}

func TestRunCheckerFreshnessResumesAfterAFailureStreak(t *testing.T) {
	ticker := newFakeTicker(tickStep{}, tickStep{err: errors.New("boom")})
	freshness := newTestFreshness(checkerClock)
	stop := startChecker(t, ticker, 5*time.Millisecond, freshness, &lockedBuffer{})
	waitForTicks(t, ticker, 2)
	stop()
	if last, _ := freshness.Status(); last == nil {
		t.Error("freshness was not recorded by the first successful tick after the failure")
	}
}

func TestRunCheckerSurvivesAPanickingTickWithoutAdvancingFreshness(t *testing.T) {
	ticker := newFakeTicker(tickStep{panics: true})
	freshness := newTestFreshness(checkerClock)
	logs := &lockedBuffer{}
	stop := startChecker(t, ticker, 5*time.Millisecond, freshness, logs)
	waitForTicks(t, ticker, 2)
	stop()
	if !strings.Contains(logs.String(), "heartbeat tick panicked") {
		t.Errorf("the panic was not logged:\n%s", logs.String())
	}
	if last, _ := freshness.Status(); last != nil {
		t.Errorf("freshness = %v, want unset after panicking ticks", last)
	}
}

func TestRunCheckerDoesNotLogAWrappedCancellationAsAFailure(t *testing.T) {
	ticker := newFakeTicker(tickStep{err: fmt.Errorf("wrapped: %w", context.Canceled)})
	logs := &lockedBuffer{}
	stop := startChecker(t, ticker, time.Hour, nil, logs)
	waitForTicks(t, ticker, 1)
	stop()
	if strings.Contains(logs.String(), "heartbeat tick failed") {
		t.Errorf("a canceled tick was logged as a failure:\n%s", logs.String())
	}
}

func TestRunCheckerExitsOnCancelAndDoesNotTickWhenAlreadyCanceled(t *testing.T) {
	ticker := newFakeTicker(tickStep{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunChecker(ctx, CheckerConfig{
		Service: ticker, Interval: time.Hour, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if got := ticker.callCount(); got != 0 {
		t.Errorf("ticks = %d, want 0 for a canceled context", got)
	}
}

func TestRunCheckerRejectsInvalidConfigurationWithoutLooping(t *testing.T) {
	tests := []struct {
		name     string
		service  Ticker
		interval time.Duration
	}{
		{"nil service", nil, time.Hour},
		{"zero interval", newFakeTicker(tickStep{}), 0},
		{"negative interval", newFakeTicker(tickStep{}), -time.Second},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				RunChecker(context.Background(), CheckerConfig{
					Service: testCase.service, Interval: testCase.interval,
					Logger: slog.New(slog.NewTextHandler(logs, nil)),
				})
			}()
			select {
			case <-done:
			case <-time.After(checkerWaitTimeout):
				t.Fatal("RunChecker must return at once on an invalid configuration")
			}
			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("an invalid configuration must be logged at error:\n%s", logs.String())
			}
			if fake, ok := testCase.service.(*fakeTicker); ok && fake.callCount() != 0 {
				t.Errorf("an invalid configuration ticked %d times", fake.callCount())
			}
		})
	}
}

func TestFreshnessBoundaryIsStrictlyMoreThanNinetySeconds(t *testing.T) {
	last := checkerClock
	tests := []struct {
		name      string
		elapsed   time.Duration
		wantStale bool
	}{
		{"just ticked", 0, false},
		{"one second before the boundary", 89 * time.Second, false},
		{"exactly on the boundary", 90 * time.Second, false},
		{"one nanosecond past the boundary", 90*time.Second + time.Nanosecond, true},
		{"well past the boundary", 10 * time.Minute, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			freshness := newTestFreshness(last.Add(testCase.elapsed))
			freshness.Record(last)
			reported, stale := freshness.Status()
			if stale != testCase.wantStale {
				t.Errorf("stale = %v, want %v", stale, testCase.wantStale)
			}
			if reported == nil || !reported.Equal(last) {
				t.Errorf("last tick = %v, want %v", reported, last)
			}
		})
	}
}

func TestFreshnessIsSafeForConcurrentUse(t *testing.T) {
	freshness := newTestFreshness(checkerClock)
	var group sync.WaitGroup
	for range 8 {
		group.Add(2)
		go func() {
			defer group.Done()
			for range 200 {
				freshness.Record(checkerClock)
			}
		}()
		go func() {
			defer group.Done()
			for range 200 {
				freshness.Status()
			}
		}()
	}
	group.Wait()
}

package appidentity

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

// fakeSweeper records every sweep time it is asked for and signals each call.
type fakeSweeper struct {
	mutex   sync.Mutex
	nows    []time.Time
	deleted int64
	err     error
	panics  bool
	called  chan struct{}
}

func newFakeSweeper() *fakeSweeper { return &fakeSweeper{called: make(chan struct{}, 16)} }

func (sweeper *fakeSweeper) DeleteExpiredReplay(_ context.Context, now time.Time) (int64, error) {
	sweeper.mutex.Lock()
	sweeper.nows = append(sweeper.nows, now)
	deleted, err, shouldPanic := sweeper.deleted, sweeper.err, sweeper.panics
	sweeper.mutex.Unlock()
	select {
	case sweeper.called <- struct{}{}:
	default:
	}
	if shouldPanic {
		panic("store exploded")
	}
	return deleted, err
}

func (sweeper *fakeSweeper) sweepTimes() []time.Time {
	sweeper.mutex.Lock()
	defer sweeper.mutex.Unlock()
	return append([]time.Time(nil), sweeper.nows...)
}

func waitForSweep(testInstance *testing.T, sweeper *fakeSweeper) {
	testInstance.Helper()
	select {
	case <-sweeper.called:
	case <-time.After(testWaitBudget):
		testInstance.Fatal("timed out waiting for DeleteExpiredReplay")
	}
}

// startCleanup runs RunReplayCleanup and returns a stop function that cancels it and waits
// for it to return.
func startCleanup(
	testInstance *testing.T, store ReplaySweeper, interval time.Duration, logs *syncBuffer,
) (stop func()) {
	testInstance.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunReplayCleanup(ctx, ReplayCleanupConfig{
			Store: store, Interval: interval, Now: func() time.Time { return testNow }, Logger: textLogger(logs),
		})
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testWaitBudget):
			testInstance.Error("RunReplayCleanup did not return after cancel")
		}
	}
}

func TestReplayCleanupSweepsImmediatelyAndStopsOnCancel(testInstance *testing.T) {
	sweeper := newFakeSweeper()
	sweeper.deleted = 3
	logs := &syncBuffer{}
	// A long interval proves the first sweep does not wait for a tick.
	stop := startCleanup(testInstance, sweeper, time.Hour, logs)

	waitForSweep(testInstance, sweeper)
	stop()

	times := sweeper.sweepTimes()
	if len(times) != 1 {
		testInstance.Fatalf("sweeps = %d, want exactly the immediate one", len(times))
	}
	if !times[0].Equal(testNow) {
		testInstance.Errorf("swept at %v, want the clock's now %v", times[0], testNow)
	}
	logged := logs.String()
	if !strings.Contains(logged, "level=INFO") || !strings.Contains(logged, "deleted=3") {
		testInstance.Errorf("a sweep that deleted rows must log the count at info:\n%s", logged)
	}
}

func TestReplayCleanupSweepsAgainOnEveryTick(testInstance *testing.T) {
	sweeper := newFakeSweeper()
	stop := startCleanup(testInstance, sweeper, 5*time.Millisecond, &syncBuffer{})
	defer stop()

	waitForSweep(testInstance, sweeper)
	waitForSweep(testInstance, sweeper)
	waitForSweep(testInstance, sweeper)
}

func TestReplayCleanupStaysQuietWhenNothingIsExpired(testInstance *testing.T) {
	sweeper := newFakeSweeper()
	logs := &syncBuffer{}
	stop := startCleanup(testInstance, sweeper, time.Hour, logs)

	waitForSweep(testInstance, sweeper)
	stop()

	if logged := logs.String(); logged != "" {
		testInstance.Errorf("an empty sweep logged:\n%s", logged)
	}
}

func TestReplayCleanupLogsErrorsAndKeepsSweeping(testInstance *testing.T) {
	sweeper := newFakeSweeper()
	sweeper.err = errors.New("delete expired oidc replay rows: database unavailable")
	logs := &syncBuffer{}
	stop := startCleanup(testInstance, sweeper, 5*time.Millisecond, logs)

	waitForSweep(testInstance, sweeper)
	waitForSweep(testInstance, sweeper)
	stop()

	logged := logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "oidc replay cleanup failed") {
		testInstance.Errorf("a failed sweep must log at error:\n%s", logged)
	}
	if !strings.Contains(logged, "database unavailable") {
		testInstance.Errorf("the failure log must carry the error text:\n%s", logged)
	}
}

func TestReplayCleanupDoesNotLogAWrappedCancellationAsAFailure(testInstance *testing.T) {
	sweeper := newFakeSweeper()
	sweeper.err = fmt.Errorf("wrapped: %w", context.Canceled)
	logs := &syncBuffer{}
	stop := startCleanup(testInstance, sweeper, time.Hour, logs)

	waitForSweep(testInstance, sweeper)
	stop()

	if logged := logs.String(); strings.Contains(logged, "oidc replay cleanup failed") {
		testInstance.Errorf("a canceled sweep was logged as a failure:\n%s", logged)
	}
}

func TestReplayCleanupSurvivesAPanickingStore(testInstance *testing.T) {
	sweeper := newFakeSweeper()
	sweeper.panics = true
	logs := &syncBuffer{}
	stop := startCleanup(testInstance, sweeper, 5*time.Millisecond, logs)

	waitForSweep(testInstance, sweeper)
	waitForSweep(testInstance, sweeper)
	stop()

	if !strings.Contains(logs.String(), "oidc replay cleanup panicked") {
		testInstance.Errorf("the panic was not logged:\n%s", logs.String())
	}
}

func TestReplayCleanupDoesNotSweepWhenAlreadyCanceled(testInstance *testing.T) {
	sweeper := newFakeSweeper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	RunReplayCleanup(ctx, ReplayCleanupConfig{
		Store: sweeper, Interval: time.Hour, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})

	if got := len(sweeper.sweepTimes()); got != 0 {
		testInstance.Errorf("sweeps = %d, want 0 for a canceled context", got)
	}
}

// fakeRejectedSweeper records every rejected-request sweep time and signals each call.
type fakeRejectedSweeper struct {
	mutex   sync.Mutex
	nows    []time.Time
	deleted int64
	err     error
	called  chan struct{}
}

func newFakeRejectedSweeper() *fakeRejectedSweeper {
	return &fakeRejectedSweeper{called: make(chan struct{}, 16)}
}

func (sweeper *fakeRejectedSweeper) DeleteExpiredRejectedRequests(_ context.Context, now time.Time) (int64, error) {
	sweeper.mutex.Lock()
	sweeper.nows = append(sweeper.nows, now)
	deleted, err := sweeper.deleted, sweeper.err
	sweeper.mutex.Unlock()
	select {
	case sweeper.called <- struct{}{}:
	default:
	}
	return deleted, err
}

func (sweeper *fakeRejectedSweeper) sweepTimes() []time.Time {
	sweeper.mutex.Lock()
	defer sweeper.mutex.Unlock()
	return append([]time.Time(nil), sweeper.nows...)
}

// startCleanupWithRejected is startCleanup with a rejected-request sweeper on the same ticker.
func startCleanupWithRejected(
	testInstance *testing.T, store ReplaySweeper, rejected RejectedRequestSweeper, interval time.Duration,
	logs *syncBuffer,
) (stop func()) {
	testInstance.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunReplayCleanup(ctx, ReplayCleanupConfig{
			Store: store, RejectedStore: rejected, Interval: interval,
			Now: func() time.Time { return testNow }, Logger: textLogger(logs),
		})
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testWaitBudget):
			testInstance.Error("RunReplayCleanup did not return after cancel")
		}
	}
}

func waitForRejectedSweep(testInstance *testing.T, sweeper *fakeRejectedSweeper) {
	testInstance.Helper()
	select {
	case <-sweeper.called:
	case <-time.After(testWaitBudget):
		testInstance.Fatal("timed out waiting for DeleteExpiredRejectedRequests")
	}
}

func TestReplayCleanupAlsoSweepsRejectedRequestsOnTheSameTick(testInstance *testing.T) {
	replay := newFakeSweeper()
	rejected := newFakeRejectedSweeper()
	rejected.deleted = 4
	logs := &syncBuffer{}
	stop := startCleanupWithRejected(testInstance, replay, rejected, time.Hour, logs)

	waitForSweep(testInstance, replay)
	waitForRejectedSweep(testInstance, rejected)
	stop()

	times := rejected.sweepTimes()
	if len(times) != 1 || !times[0].Equal(testNow) {
		testInstance.Errorf("rejected sweeps = %v, want exactly one at the clock's now %v", times, testNow)
	}
	logged := logs.String()
	if !strings.Contains(logged, "rejected identity request cleanup deleted old rows") ||
		!strings.Contains(logged, "deleted=4") {
		testInstance.Errorf("a rejected sweep that deleted rows must log the count at info:\n%s", logged)
	}
}

func TestReplayCleanupSweepsRejectedRequestsAgainOnEveryTick(testInstance *testing.T) {
	rejected := newFakeRejectedSweeper()
	stop := startCleanupWithRejected(testInstance, newFakeSweeper(), rejected, 5*time.Millisecond, &syncBuffer{})
	defer stop()

	waitForRejectedSweep(testInstance, rejected)
	waitForRejectedSweep(testInstance, rejected)
	waitForRejectedSweep(testInstance, rejected)
}

func TestReplayCleanupRejectedSweepFailureDoesNotBlockTheReplaySweep(testInstance *testing.T) {
	replay := newFakeSweeper()
	rejected := newFakeRejectedSweeper()
	rejected.err = errors.New("delete old rejected identity requests: database unavailable")
	logs := &syncBuffer{}
	stop := startCleanupWithRejected(testInstance, replay, rejected, 5*time.Millisecond, logs)

	waitForRejectedSweep(testInstance, rejected)
	waitForSweep(testInstance, replay)
	waitForRejectedSweep(testInstance, rejected)
	stop()

	logged := logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "rejected identity request cleanup failed") {
		testInstance.Errorf("a failed rejected sweep must log at error:\n%s", logged)
	}
	if !strings.Contains(logged, "database unavailable") {
		testInstance.Errorf("the failure log must carry the error text:\n%s", logged)
	}
	if strings.Contains(logged, "oidc replay cleanup failed") {
		testInstance.Errorf("the replay sweep did not fail:\n%s", logged)
	}
}

func TestReplayCleanupStaysQuietWhenNoRejectedRequestIsOld(testInstance *testing.T) {
	rejected := newFakeRejectedSweeper()
	logs := &syncBuffer{}
	stop := startCleanupWithRejected(testInstance, newFakeSweeper(), rejected, time.Hour, logs)

	waitForRejectedSweep(testInstance, rejected)
	stop()

	if logged := logs.String(); logged != "" {
		testInstance.Errorf("an empty rejected sweep logged:\n%s", logged)
	}
}

func TestReplayCleanupRejectsInvalidConfigurationWithoutPanicking(testInstance *testing.T) {
	testCases := []struct {
		label    string
		store    ReplaySweeper
		interval time.Duration
	}{
		{"nil store", nil, time.Hour},
		{"zero interval", newFakeSweeper(), 0},
		{"negative interval", newFakeSweeper(), -time.Hour},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			logs := &syncBuffer{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				RunReplayCleanup(context.Background(), ReplayCleanupConfig{
					Store: testCase.store, Interval: testCase.interval, Logger: textLogger(logs),
				})
			}()
			select {
			case <-done:
			case <-time.After(testWaitBudget):
				subTest.Fatal("RunReplayCleanup must return at once on an invalid configuration")
			}
			if !strings.Contains(logs.String(), "level=ERROR") {
				subTest.Errorf("an invalid configuration must be logged at error:\n%s", logs.String())
			}
		})
	}
}

// fakePendingSweeper records every pending-request sweep (time and ttl) and signals each call.
type fakePendingSweeper struct {
	mutex   sync.Mutex
	nows    []time.Time
	ttls    []time.Duration
	deleted int64
	err     error
	called  chan struct{}
}

func newFakePendingSweeper() *fakePendingSweeper {
	return &fakePendingSweeper{called: make(chan struct{}, 16)}
}

func (sweeper *fakePendingSweeper) DeleteStalePendingRequests(
	_ context.Context, now time.Time, ttl time.Duration,
) (int64, error) {
	sweeper.mutex.Lock()
	sweeper.nows = append(sweeper.nows, now)
	sweeper.ttls = append(sweeper.ttls, ttl)
	deleted, err := sweeper.deleted, sweeper.err
	sweeper.mutex.Unlock()
	select {
	case sweeper.called <- struct{}{}:
	default:
	}
	return deleted, err
}

func (sweeper *fakePendingSweeper) sweeps() (nows []time.Time, ttls []time.Duration) {
	sweeper.mutex.Lock()
	defer sweeper.mutex.Unlock()
	return append([]time.Time(nil), sweeper.nows...), append([]time.Duration(nil), sweeper.ttls...)
}

func waitForPendingSweep(testInstance *testing.T, sweeper *fakePendingSweeper) {
	testInstance.Helper()
	select {
	case <-sweeper.called:
	case <-time.After(testWaitBudget):
		testInstance.Fatal("timed out waiting for DeleteStalePendingRequests")
	}
}

// startCleanupWithPending is startCleanup with a pending-request sweeper on the same ticker.
func startCleanupWithPending(
	testInstance *testing.T, pending PendingRequestSweeper, pendingTTL time.Duration, interval time.Duration,
	logs *syncBuffer,
) (stop func()) {
	testInstance.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunReplayCleanup(ctx, ReplayCleanupConfig{
			Store: newFakeSweeper(), PendingStore: pending, PendingTTL: pendingTTL, Interval: interval,
			Now: func() time.Time { return testNow }, Logger: textLogger(logs),
		})
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testWaitBudget):
			testInstance.Error("RunReplayCleanup did not return after cancel")
		}
	}
}

func TestReplayCleanupAlsoSweepsStalePendingRequestsOnTheSameTick(testInstance *testing.T) {
	pending := newFakePendingSweeper()
	pending.deleted = 3
	logs := &syncBuffer{}
	stop := startCleanupWithPending(testInstance, pending, 7*24*time.Hour, time.Hour, logs)

	waitForPendingSweep(testInstance, pending)
	stop()

	nows, ttls := pending.sweeps()
	if len(nows) != 1 || !nows[0].Equal(testNow) || ttls[0] != 7*24*time.Hour {
		testInstance.Errorf("pending sweeps = %v with ttls %v, want one at %v with a 168h ttl", nows, ttls, testNow)
	}
	logged := logs.String()
	if !strings.Contains(logged, "pending identity request cleanup deleted stale rows") || !strings.Contains(logged, "deleted=3") {
		testInstance.Errorf("a pending sweep that deleted rows must log the count at info:\n%s", logged)
	}
}

func TestReplayCleanupSweepsPendingRequestsAgainOnEveryTick(testInstance *testing.T) {
	pending := newFakePendingSweeper()
	stop := startCleanupWithPending(testInstance, pending, time.Hour, 5*time.Millisecond, &syncBuffer{})
	defer stop()

	waitForPendingSweep(testInstance, pending)
	waitForPendingSweep(testInstance, pending)
	waitForPendingSweep(testInstance, pending)
}

func TestReplayCleanupPendingSweepFailureIsLoggedAndDoesNotStopTheLoop(testInstance *testing.T) {
	pending := newFakePendingSweeper()
	pending.err = errors.New("delete stale pending identity requests: database unavailable")
	logs := &syncBuffer{}
	stop := startCleanupWithPending(testInstance, pending, time.Hour, 5*time.Millisecond, logs)

	waitForPendingSweep(testInstance, pending)
	waitForPendingSweep(testInstance, pending)
	stop()

	logged := logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "pending identity request cleanup failed") {
		testInstance.Errorf("a failed pending sweep must log at error:\n%s", logged)
	}
	if !strings.Contains(logged, "database unavailable") {
		testInstance.Errorf("the failure log must carry the error text:\n%s", logged)
	}
	if strings.Contains(logged, "oidc replay cleanup failed") {
		testInstance.Errorf("the replay sweep did not fail:\n%s", logged)
	}
}

func TestReplayCleanupStaysQuietWhenNoPendingRequestIsStale(testInstance *testing.T) {
	pending := newFakePendingSweeper()
	logs := &syncBuffer{}
	stop := startCleanupWithPending(testInstance, pending, time.Hour, time.Hour, logs)

	waitForPendingSweep(testInstance, pending)
	stop()

	if logged := logs.String(); logged != "" {
		testInstance.Errorf("an empty pending sweep logged:\n%s", logged)
	}
}

func TestReplayCleanupNeverSweepsPendingRequestsWithoutAPositiveTTL(testInstance *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Hour} {
		testInstance.Run(ttl.String(), func(subTest *testing.T) {
			pending := newFakePendingSweeper()
			replay := newFakeSweeper()
			logs := &syncBuffer{}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				RunReplayCleanup(ctx, ReplayCleanupConfig{
					Store: replay, PendingStore: pending, PendingTTL: ttl, Interval: 5 * time.Millisecond,
					Now: func() time.Time { return testNow }, Logger: textLogger(logs),
				})
			}()
			// The replay sweep still runs, so the loop is alive; the pending store must stay untouched.
			waitForSweep(subTest, replay)
			waitForSweep(subTest, replay)
			cancel()
			<-done

			if nows, _ := pending.sweeps(); len(nows) != 0 {
				subTest.Errorf("pending sweeps = %v, want none for ttl %v", nows, ttl)
			}
			logged := logs.String()
			if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "pending identity request cleanup not started") {
				subTest.Errorf("an invalid ttl must be logged at error:\n%s", logged)
			}
		})
	}
}

// fakeMintSweeper records every publisher-mint sweep (time and window) and signals each call.
type fakeMintSweeper struct {
	mutex      sync.Mutex
	nows       []time.Time
	windows    []time.Duration
	deleted    int64
	err        error
	panicValue any
	called     chan struct{}
}

func newFakeMintSweeper() *fakeMintSweeper {
	return &fakeMintSweeper{called: make(chan struct{}, 16)}
}

func (sweeper *fakeMintSweeper) DeleteExpiredPublisherMints(
	_ context.Context, now time.Time, window time.Duration,
) (int64, error) {
	sweeper.mutex.Lock()
	sweeper.nows = append(sweeper.nows, now)
	sweeper.windows = append(sweeper.windows, window)
	deleted, err, panicValue := sweeper.deleted, sweeper.err, sweeper.panicValue
	sweeper.mutex.Unlock()
	select {
	case sweeper.called <- struct{}{}:
	default:
	}
	if panicValue != nil {
		panic(panicValue)
	}
	return deleted, err
}

func (sweeper *fakeMintSweeper) sweeps() (nows []time.Time, windows []time.Duration) {
	sweeper.mutex.Lock()
	defer sweeper.mutex.Unlock()
	return append([]time.Time(nil), sweeper.nows...), append([]time.Duration(nil), sweeper.windows...)
}

func waitForMintSweep(testInstance *testing.T, sweeper *fakeMintSweeper) {
	testInstance.Helper()
	select {
	case <-sweeper.called:
	case <-time.After(testWaitBudget):
		testInstance.Fatal("timed out waiting for DeleteExpiredPublisherMints")
	}
}

// startCleanupWithMints runs RunReplayCleanup with a mint sweeper on the same ticker.
func startCleanupWithMints(
	testInstance *testing.T, replay ReplaySweeper, mints PublisherMintSweeper, window time.Duration,
	interval time.Duration, logs *syncBuffer,
) (stop func()) {
	testInstance.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunReplayCleanup(ctx, ReplayCleanupConfig{
			Store: replay, MintStore: mints, MintWindow: window, Interval: interval,
			Now: func() time.Time { return testNow }, Logger: textLogger(logs),
		})
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testWaitBudget):
			testInstance.Error("RunReplayCleanup did not return after cancel")
		}
	}
}

func TestReplayCleanupAlsoSweepsPublisherMintsOnTheSameTick(testInstance *testing.T) {
	mints := newFakeMintSweeper()
	mints.deleted = 4
	replay := newFakeSweeper()
	logs := &syncBuffer{}
	stop := startCleanupWithMints(testInstance, replay, mints, time.Hour, time.Hour, logs)

	waitForMintSweep(testInstance, mints)
	stop()

	nows, windows := mints.sweeps()
	if len(nows) != 1 || !nows[0].Equal(testNow) || windows[0] != time.Hour {
		testInstance.Errorf("mint sweeps = %v with windows %v, want one at %v with a 1h window", nows, windows, testNow)
	}
	if len(replay.sweepTimes()) != 1 {
		testInstance.Errorf("the replay sweep did not run on the same tick")
	}
	logged := logs.String()
	if !strings.Contains(logged, "publisher mint cleanup deleted old rows") || !strings.Contains(logged, "deleted=4") {
		testInstance.Errorf("a mint sweep that deleted rows must log the count at info:\n%s", logged)
	}
}

func TestReplayCleanupSweepsPublisherMintsAgainOnEveryTick(testInstance *testing.T) {
	mints := newFakeMintSweeper()
	stop := startCleanupWithMints(testInstance, newFakeSweeper(), mints, time.Hour, 5*time.Millisecond, &syncBuffer{})
	defer stop()

	waitForMintSweep(testInstance, mints)
	waitForMintSweep(testInstance, mints)
	waitForMintSweep(testInstance, mints)
}

func TestReplayCleanupMintSweepFailureIsLoggedAndDoesNotBlockTheReplaySweep(testInstance *testing.T) {
	mints := newFakeMintSweeper()
	mints.err = errors.New("delete old publisher mints: database unavailable")
	replay := newFakeSweeper()
	logs := &syncBuffer{}
	stop := startCleanupWithMints(testInstance, replay, mints, time.Hour, 5*time.Millisecond, logs)

	waitForMintSweep(testInstance, mints)
	waitForMintSweep(testInstance, mints)
	waitForSweep(testInstance, replay)
	stop()

	logged := logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "publisher mint cleanup failed") ||
		!strings.Contains(logged, "database unavailable") {
		testInstance.Errorf("a failed mint sweep must log the error text at error:\n%s", logged)
	}
	if strings.Contains(logged, "oidc replay cleanup failed") {
		testInstance.Errorf("the replay sweep did not fail:\n%s", logged)
	}
}

func TestReplayCleanupSurvivesAPanickingMintStore(testInstance *testing.T) {
	mints := newFakeMintSweeper()
	mints.panicValue = "boom"
	logs := &syncBuffer{}
	stop := startCleanupWithMints(testInstance, newFakeSweeper(), mints, time.Hour, 5*time.Millisecond, logs)

	waitForMintSweep(testInstance, mints)
	waitForMintSweep(testInstance, mints)
	stop()

	if !strings.Contains(logs.String(), "publisher mint cleanup panicked") {
		testInstance.Errorf("a panicking mint store must be recovered and logged:\n%s", logs.String())
	}
}

func TestReplayCleanupStaysQuietWhenNoPublisherMintIsOld(testInstance *testing.T) {
	mints := newFakeMintSweeper()
	logs := &syncBuffer{}
	stop := startCleanupWithMints(testInstance, newFakeSweeper(), mints, time.Hour, time.Hour, logs)

	waitForMintSweep(testInstance, mints)
	stop()

	if logged := logs.String(); logged != "" {
		testInstance.Errorf("an empty mint sweep logged:\n%s", logged)
	}
}

func TestReplayCleanupNeverSweepsPublisherMintsWithoutAPositiveWindow(testInstance *testing.T) {
	for _, window := range []time.Duration{0, -time.Hour} {
		testInstance.Run(window.String(), func(subTest *testing.T) {
			mints := newFakeMintSweeper()
			replay := newFakeSweeper()
			logs := &syncBuffer{}
			stop := startCleanupWithMints(subTest, replay, mints, window, 5*time.Millisecond, logs)

			// The replay sweep still runs, so the loop is alive; the mint store must stay untouched.
			waitForSweep(subTest, replay)
			waitForSweep(subTest, replay)
			stop()

			if nows, _ := mints.sweeps(); len(nows) != 0 {
				subTest.Errorf("mint sweeps = %v, want none for window %v", nows, window)
			}
			logged := logs.String()
			if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "publisher mint cleanup not started: invalid window") {
				subTest.Errorf("an invalid window must be logged at error:\n%s", logged)
			}
		})
	}
}

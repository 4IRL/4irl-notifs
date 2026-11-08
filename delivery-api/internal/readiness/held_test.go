package readiness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

type fakeClock struct {
	mutex   sync.Mutex
	current time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.current
}

func (clock *fakeClock) Advance(delta time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.current = clock.current.Add(delta)
}

type fakeMessages struct {
	mutex     sync.Mutex
	calls     int
	summaries []message.HeldAppSummary
	err       error
	onCall    func(ctx context.Context) error
}

func (fake *fakeMessages) HeldSummary(ctx context.Context) ([]message.HeldAppSummary, error) {
	fake.mutex.Lock()
	fake.calls++
	summaries, err, onCall := fake.summaries, fake.err, fake.onCall
	fake.mutex.Unlock()
	if onCall != nil {
		if hookErr := onCall(ctx); hookErr != nil {
			return nil, hookErr
		}
	}
	return summaries, err
}

func (fake *fakeMessages) set(summaries []message.HeldAppSummary, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.summaries, fake.err = summaries, err
}

func (fake *fakeMessages) callCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.calls
}

type fakeAlerts struct {
	mutex sync.Mutex
	calls int
	count int
	err   error
}

func (fake *fakeAlerts) CountUnassignedAlerts(context.Context) (int, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.calls++
	return fake.count, fake.err
}

func (fake *fakeAlerts) callCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.calls
}

func (fake *fakeAlerts) set(count int, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.count, fake.err = count, err
}

type heldHarness struct {
	cache    *HeldCache
	clock    *fakeClock
	messages *fakeMessages
	alerts   *fakeAlerts
	logs     *bytes.Buffer
}

func newHeldHarness() *heldHarness {
	harness := &heldHarness{
		clock:    &fakeClock{current: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)},
		messages: &fakeMessages{},
		alerts:   &fakeAlerts{},
		logs:     &bytes.Buffer{},
	}
	harness.cache = NewHeldCache(HeldConfig{
		Messages: harness.messages,
		Alerts:   harness.alerts,
		Now:      harness.clock.Now,
		Logger:   slog.New(slog.NewTextHandler(&lockedWriter{buffer: harness.logs}, nil)),
	})
	return harness
}

type lockedWriter struct {
	mutex  sync.Mutex
	buffer *bytes.Buffer
}

func (writer *lockedWriter) Write(data []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.buffer.Write(data)
}

func TestHeldEmptyState(testInstance *testing.T) {
	harness := newHeldHarness()
	snapshot := harness.cache.HeldSnapshot(context.Background())
	if !snapshot.Available || snapshot.Stale || snapshot.HeldMessages != 0 || snapshot.UnassignedAlerts != 0 {
		testInstance.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.OldestHeldAt != nil || snapshot.Apps == nil || len(snapshot.Apps) != 0 || snapshot.AppsMore != 0 {
		testInstance.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestHeldAggregates(testInstance *testing.T) {
	harness := newHeldHarness()
	older := harness.clock.Now().Add(-5 * time.Hour)
	newer := harness.clock.Now().Add(-time.Hour)
	harness.messages.set([]message.HeldAppSummary{
		{AppID: "billing", Count: 4, OldestHeldAt: newer},
		{AppID: "u4i", Count: 1, OldestHeldAt: older},
	}, nil)
	harness.alerts.set(2, nil)
	snapshot := harness.cache.HeldSnapshot(context.Background())
	if snapshot.HeldMessages != 5 || snapshot.UnassignedAlerts != 2 || snapshot.OldestHeldAt == nil || !snapshot.OldestHeldAt.Equal(older) {
		testInstance.Fatalf("snapshot = %+v", snapshot)
	}
	if !snapshot.AsOf.Equal(harness.clock.Now()) || len(snapshot.Apps) != 2 || snapshot.Apps[0].AppID != "billing" {
		testInstance.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestHeldServedWithinTTLAndRefreshedAfter(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.cache.HeldSnapshot(context.Background())
	harness.clock.Advance(HeldCacheTTL - time.Second)
	harness.cache.HeldSnapshot(context.Background())
	if harness.messages.callCount() != 1 {
		testInstance.Fatalf("calls within TTL = %d, want 1", harness.messages.callCount())
	}
	harness.clock.Advance(time.Second)
	harness.cache.HeldSnapshot(context.Background())
	if harness.messages.callCount() != 2 {
		testInstance.Fatalf("calls after TTL = %d, want 2", harness.messages.callCount())
	}
}

func TestHeldFirstRefreshFailingIsUnavailable(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.messages.set(nil, errors.New("db down"))
	snapshot := harness.cache.HeldSnapshot(context.Background())
	if snapshot.Available || snapshot.HeldMessages != 0 || snapshot.Apps != nil {
		testInstance.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestHeldFailureAfterSuccessServesStale(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.messages.set([]message.HeldAppSummary{{AppID: "a", Count: 3, OldestHeldAt: harness.clock.Now()}}, nil)
	first := harness.cache.HeldSnapshot(context.Background())
	harness.messages.set(nil, errors.New("db down"))
	harness.clock.Advance(HeldCacheTTL)
	second := harness.cache.HeldSnapshot(context.Background())
	if !second.Available || !second.Stale || second.HeldMessages != 3 || !second.AsOf.Equal(first.AsOf) {
		testInstance.Fatalf("second = %+v", second)
	}
}

func TestHeldPartialFailureIsFailure(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.messages.set([]message.HeldAppSummary{{AppID: "a", Count: 3, OldestHeldAt: harness.clock.Now()}}, nil)
	harness.alerts.set(0, errors.New("alerts down"))
	snapshot := harness.cache.HeldSnapshot(context.Background())
	if snapshot.Available {
		testInstance.Fatalf("snapshot = %+v, want unavailable", snapshot)
	}
}

func TestHeldRetryWaitsOneTTLAfterFailure(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.messages.set(nil, errors.New("db down"))
	harness.cache.HeldSnapshot(context.Background())
	harness.clock.Advance(HeldCacheTTL - time.Second)
	harness.cache.HeldSnapshot(context.Background())
	if harness.messages.callCount() != 1 {
		testInstance.Fatalf("calls = %d, want 1", harness.messages.callCount())
	}
	harness.clock.Advance(time.Second)
	harness.cache.HeldSnapshot(context.Background())
	if harness.messages.callCount() != 2 {
		testInstance.Fatalf("calls = %d, want 2", harness.messages.callCount())
	}
}

func TestHeldAppCap(testInstance *testing.T) {
	harness := newHeldHarness()
	summaries := []message.HeldAppSummary{}
	for index := 0; index < HeldAppsLimit+3; index++ {
		summaries = append(summaries, message.HeldAppSummary{AppID: fmt.Sprintf("app%02d", index), Count: 1, OldestHeldAt: harness.clock.Now()})
	}
	harness.messages.set(summaries, nil)
	snapshot := harness.cache.HeldSnapshot(context.Background())
	if len(snapshot.Apps) != HeldAppsLimit || snapshot.AppsMore != 3 || snapshot.HeldMessages != HeldAppsLimit+3 {
		testInstance.Fatalf("apps=%d more=%d total=%d", len(snapshot.Apps), snapshot.AppsMore, snapshot.HeldMessages)
	}
}

func TestHeldConcurrentCallersOneRefresh(testInstance *testing.T) {
	harness := newHeldHarness()
	release := make(chan struct{})
	entered := make(chan struct{}, 10)
	harness.messages.onCall = func(context.Context) error {
		entered <- struct{}{}
		<-release
		return nil
	}
	var group sync.WaitGroup
	results := make(chan bool, 10)
	for index := 0; index < 10; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- harness.cache.HeldSnapshot(context.Background()).Available
		}()
	}
	<-entered
	time.Sleep(50 * time.Millisecond)
	close(release)
	group.Wait()
	close(results)
	for available := range results {
		if !available {
			testInstance.Fatalf("a concurrent caller saw an unavailable snapshot")
		}
	}
	if harness.messages.callCount() != 1 || harness.alerts.callCount() != 1 || len(entered) != 0 {
		testInstance.Fatalf("message calls = %d, alert calls = %d, extra entries = %d, want one refresh", harness.messages.callCount(), harness.alerts.callCount(), len(entered))
	}
}

func TestHeldParentCancelWithoutPriorSuccess(testInstance *testing.T) {
	harness := newHeldHarness()
	ctx, cancel := context.WithCancel(context.Background())
	harness.messages.onCall = func(callCtx context.Context) error {
		cancel()
		<-callCtx.Done()
		return callCtx.Err()
	}
	snapshot := harness.cache.HeldSnapshot(ctx)
	if snapshot.Available || harness.logs.Len() != 0 {
		testInstance.Fatalf("snapshot = %+v, logs = %s", snapshot, harness.logs.String())
	}
	harness.messages.onCall = nil
	recovered := harness.cache.HeldSnapshot(context.Background())
	if !recovered.Available || recovered.Stale || harness.messages.callCount() != 2 {
		testInstance.Fatalf("recovered = %+v, calls = %d", recovered, harness.messages.callCount())
	}
}

func TestHeldLastAttemptStampedAtCompletion(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.messages.onCall = func(context.Context) error {
		harness.clock.Advance(HeldCacheTTL - time.Second)
		return nil
	}
	harness.cache.HeldSnapshot(context.Background())
	harness.clock.Advance(HeldCacheTTL - time.Second - time.Millisecond)
	harness.cache.HeldSnapshot(context.Background())
	if harness.messages.callCount() != 1 {
		testInstance.Fatalf("calls = %d, want 1 (TTL runs from completion)", harness.messages.callCount())
	}
}

func TestHeldRefreshTimeout(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.messages.onCall = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	started := time.Now()
	snapshot := harness.cache.HeldSnapshot(context.Background())
	elapsed := time.Since(started)
	if snapshot.Available {
		testInstance.Fatalf("snapshot = %+v, want unavailable", snapshot)
	}
	if elapsed < heldRefreshTimeout || elapsed > heldRefreshTimeout+3*time.Second {
		testInstance.Fatalf("elapsed = %v, want about %v", elapsed, heldRefreshTimeout)
	}
	if !bytes.Contains(harness.logs.Bytes(), []byte("held counts refresh failed")) {
		testInstance.Fatalf("no warn log: %s", harness.logs.String())
	}
}

func TestHeldParentCancelNotAFailure(testInstance *testing.T) {
	harness := newHeldHarness()
	harness.messages.set([]message.HeldAppSummary{{AppID: "a", Count: 3, OldestHeldAt: harness.clock.Now()}}, nil)
	harness.cache.HeldSnapshot(context.Background())
	harness.clock.Advance(HeldCacheTTL)

	ctx, cancel := context.WithCancel(context.Background())
	harness.messages.onCall = func(callCtx context.Context) error {
		cancel()
		<-callCtx.Done()
		return callCtx.Err()
	}
	harness.logs.Reset()
	snapshot := harness.cache.HeldSnapshot(ctx)
	if snapshot.Stale || !snapshot.Available || snapshot.HeldMessages != 3 {
		testInstance.Fatalf("snapshot = %+v", snapshot)
	}
	if harness.logs.Len() != 0 {
		testInstance.Fatalf("unexpected log: %s", harness.logs.String())
	}
	harness.messages.onCall = nil
	callsBefore := harness.messages.callCount()
	harness.cache.HeldSnapshot(context.Background())
	if harness.messages.callCount() != callsBefore+1 {
		testInstance.Fatalf("next call did not query again")
	}
}

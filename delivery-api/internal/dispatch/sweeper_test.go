package dispatch

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

// fakeExpirer records every cutoff it is asked to delete and signals each call.
type fakeExpirer struct {
	mutex   sync.Mutex
	cutoffs []time.Time
	deleted int64
	err     error
	panics  bool
	called  chan struct{}
}

func newFakeExpirer() *fakeExpirer { return &fakeExpirer{called: make(chan struct{}, 16)} }

func (expirer *fakeExpirer) DeleteExpired(_ context.Context, cutoff time.Time) (int64, error) {
	expirer.mutex.Lock()
	expirer.cutoffs = append(expirer.cutoffs, cutoff)
	deleted, err, shouldPanic := expirer.deleted, expirer.err, expirer.panics
	expirer.mutex.Unlock()
	select {
	case expirer.called <- struct{}{}:
	default:
	}
	if shouldPanic {
		panic("store exploded")
	}
	return deleted, err
}

func (expirer *fakeExpirer) cutoffsSeen() []time.Time {
	expirer.mutex.Lock()
	defer expirer.mutex.Unlock()
	return append([]time.Time(nil), expirer.cutoffs...)
}

func waitForCall(t *testing.T, expirer *fakeExpirer) {
	t.Helper()
	select {
	case <-expirer.called:
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for DeleteExpired")
	}
}

// startSweeper runs RunSweeper and returns a stop function that cancels it and waits for it to return.
func startSweeper(
	t *testing.T, expirer Expirer, retention, interval time.Duration, logs *lockedBuffer,
) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fixedNow := func() time.Time { return testClockStart }
	go func() {
		defer close(done)
		RunSweeper(ctx, SweeperConfig{
			Expirer: expirer, Retention: retention, Interval: interval, Now: fixedNow, Logger: logger,
		})
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(testWaitTimeout):
			t.Error("RunSweeper did not return after cancel")
		}
	}
}

func TestRunSweeperSweepsImmediatelyWithRetentionCutoffAndStopsOnCancel(t *testing.T) {
	expirer := newFakeExpirer()
	expirer.deleted = 3
	logs := &lockedBuffer{}
	retention := 30 * 24 * time.Hour
	// A long interval proves the first sweep does not wait for a tick.
	stop := startSweeper(t, expirer, retention, time.Hour, logs)

	waitForCall(t, expirer)
	stop()
	cutoffs := expirer.cutoffsSeen()
	if len(cutoffs) != 1 {
		t.Fatalf("sweeps = %d, want exactly the immediate one", len(cutoffs))
	}
	if want := testClockStart.Add(-retention); !cutoffs[0].Equal(want) {
		t.Errorf("cutoff = %v, want now - retention = %v", cutoffs[0], want)
	}
	logged := logs.String()
	if !strings.Contains(logged, "level=INFO") || !strings.Contains(logged, "deleted=3") {
		t.Errorf("a sweep that deleted rows must log the count at info:\n%s", logged)
	}
}

func TestRunSweeperSweepsAgainOnEveryTick(t *testing.T) {
	expirer := newFakeExpirer()
	stop := startSweeper(t, expirer, time.Hour, 5*time.Millisecond, &lockedBuffer{})
	defer stop()

	waitForCall(t, expirer)
	waitForCall(t, expirer)
	waitForCall(t, expirer)
}

func TestRunSweeperStaysQuietWhenNothingDeleted(t *testing.T) {
	expirer := newFakeExpirer()
	logs := &lockedBuffer{}
	stop := startSweeper(t, expirer, time.Hour, time.Hour, logs)
	waitForCall(t, expirer)
	stop()
	if logged := logs.String(); logged != "" {
		t.Errorf("an empty sweep logged:\n%s", logged)
	}
}

func TestRunSweeperLogsErrorsAndKeepsSweeping(t *testing.T) {
	expirer := newFakeExpirer()
	expirer.err = errors.New("delete expired messages: database unavailable")
	logs := &lockedBuffer{}
	stop := startSweeper(t, expirer, time.Hour, 5*time.Millisecond, logs)

	waitForCall(t, expirer)
	waitForCall(t, expirer)
	stop()
	logged := logs.String()
	if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "retention sweep failed") {
		t.Errorf("a failed sweep must log at error:\n%s", logged)
	}
}

func TestRunSweeperDoesNotLogAWrappedContextCancellationAsAFailure(t *testing.T) {
	expirer := newFakeExpirer()
	expirer.err = fmt.Errorf("wrapped: %w", context.Canceled)
	logs := &lockedBuffer{}
	stop := startSweeper(t, expirer, time.Hour, time.Hour, logs)

	waitForCall(t, expirer)
	stop()
	if logged := logs.String(); strings.Contains(logged, "retention sweep failed") {
		t.Errorf("a canceled sweep was logged as a failure:\n%s", logged)
	}
}

func TestRunSweeperSurvivesPanickingStore(t *testing.T) {
	expirer := newFakeExpirer()
	expirer.panics = true
	logs := &lockedBuffer{}
	stop := startSweeper(t, expirer, time.Hour, 5*time.Millisecond, logs)

	waitForCall(t, expirer)
	waitForCall(t, expirer)
	stop()
	if !strings.Contains(logs.String(), "retention sweep panicked") {
		t.Errorf("the panic was not logged:\n%s", logs.String())
	}
}

func TestRunSweeperDoesNotSweepWhenAlreadyCanceled(t *testing.T) {
	expirer := newFakeExpirer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunSweeper(ctx, SweeperConfig{
		Expirer: expirer, Retention: time.Hour, Interval: time.Hour,
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if got := len(expirer.cutoffsSeen()); got != 0 {
		t.Errorf("sweeps = %d, want 0 for a canceled context", got)
	}
}

func TestRunSweeperRejectsInvalidConfigurationWithoutPanicking(t *testing.T) {
	tests := []struct {
		name      string
		expirer   Expirer
		retention time.Duration
		interval  time.Duration
	}{
		{"nil expirer", nil, time.Hour, time.Hour},
		{"zero retention", newFakeExpirer(), 0, time.Hour},
		{"negative retention", newFakeExpirer(), -time.Hour, time.Hour},
		{"zero interval", newFakeExpirer(), time.Hour, 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				RunSweeper(context.Background(), SweeperConfig{
					Expirer: testCase.expirer, Retention: testCase.retention, Interval: testCase.interval,
					Logger: slog.New(slog.NewTextHandler(logs, nil)),
				})
			}()
			select {
			case <-done:
			case <-time.After(testWaitTimeout):
				t.Fatal("RunSweeper must return at once on an invalid configuration")
			}
			if !strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("an invalid configuration must be logged at error:\n%s", logs.String())
			}
		})
	}
}

func TestRetentionFromEnv(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "empty defaults to 30 days", value: "", want: 30 * day},
		{name: "one day", value: "1", want: day},
		{name: "ninety days", value: "90", want: 90 * day},
		{name: "upper bound", value: "3650", want: 3650 * day},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-5", wantErr: true},
		{name: "above upper bound", value: "3651", wantErr: true},
		{name: "not a number", value: "thirty", wantErr: true},
		{name: "fractional", value: "1.5", wantErr: true},
		{name: "padded", value: " 30", wantErr: true},
		{name: "overflowing", value: "99999999999999999999", wantErr: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := RetentionFromEnv(func(name string) string {
				if name != "DELIVERY_RETENTION_DAYS" {
					t.Errorf("read env %q, want DELIVERY_RETENTION_DAYS", name)
				}
				return testCase.value
			})
			if testCase.wantErr {
				if err == nil || err.Error() != "invalid DELIVERY_RETENTION_DAYS" {
					t.Fatalf("err = %v, want invalid DELIVERY_RETENTION_DAYS", err)
				}
				if strings.Contains(err.Error(), testCase.value) && testCase.value != "" {
					t.Errorf("the error echoes the value %q", testCase.value)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Errorf("got %v, %v; want %v", got, err, testCase.want)
			}
		})
	}
}

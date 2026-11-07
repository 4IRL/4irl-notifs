package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestReleaserFuncSatisfiesDueReleaser(t *testing.T) {
	var releaser DueReleaser = ReleaserFunc(func(_ context.Context, now time.Time, limit int) (int64, error) {
		if !now.Equal(testClockStart) || limit != 7 {
			t.Errorf("got now=%v limit=%d", now, limit)
		}
		return 3, nil
	})
	released, err := releaser.ReleaseDue(context.Background(), testClockStart, 7)
	if released != 3 || err != nil {
		t.Errorf("got (%d, %v), want (3, nil)", released, err)
	}
	if DefaultHeldReleaseInterval != 5*time.Second {
		t.Errorf("DefaultHeldReleaseInterval = %v", DefaultHeldReleaseInterval)
	}
}

func runKindReleaser(t *testing.T, kind string, releaser DueReleaser, interval time.Duration, wake func(), logs *lockedBuffer, calls int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	go func() {
		defer close(done)
		RunReleaser(ctx, ReleaserConfig{
			Releaser: releaser, Interval: interval, Kind: kind, BatchSize: releaseTestBatch,
			MaxBatchesPerRun: 5, Now: func() time.Time { return testClockStart }, Wake: wake, Logger: logger,
		})
	}()
	if fake, ok := releaser.(*fakeReleaser); ok {
		waitForCalls(t, fake, calls)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(testWaitTimeout):
		t.Error("RunReleaser did not return after cancel")
	}
}

func TestRunReleaserHeldKindLogsHeldStrings(t *testing.T) {
	cases := []struct {
		name     string
		releaser DueReleaser
		wake     func()
		want     string
	}{
		{"not started", nil, nil, "held-message releaser not started: invalid configuration"},
		{"panicked", newFakeReleaser(releaseStep{panics: true}), nil, "held release panicked"},
		{"failed", newFakeReleaser(releaseStep{err: errors.New("db down")}), nil, "held release failed"},
		{"wake panicked", newFakeReleaser(releaseStep{released: 1}), func() { panic("boom") }, "held release wake panicked"},
		{"released", newFakeReleaser(releaseStep{released: 1}), nil, "released held messages"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &lockedBuffer{}
			interval := time.Hour
			if testCase.releaser == nil {
				interval = 0
			}
			runKindReleaser(t, "held", testCase.releaser, interval, testCase.wake, logs, 1)
			logged := logs.String()
			if !strings.Contains(logged, testCase.want) {
				t.Errorf("missing %q in:\n%s", testCase.want, logged)
			}
			if strings.Contains(logged, "scheduled") {
				t.Errorf("held loop logged a scheduled string:\n%s", logged)
			}
		})
	}
}

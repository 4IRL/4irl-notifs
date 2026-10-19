package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	// DefaultReleaseInterval is how often the releaser runs after its immediate
	// first run.
	DefaultReleaseInterval = time.Second
	// DefaultReleaseBatchSize is how many messages one ReleaseDue call may
	// release.
	DefaultReleaseBatchSize = 100
	// DefaultMaxReleaseBatches bounds how many batches one run may drain before
	// it yields to the next tick.
	DefaultMaxReleaseBatches = 50
)

// DueReleaser releases scheduled messages that have come due. It is satisfied
// by *message.PGStore.
type DueReleaser interface {
	// ReleaseDue releases up to limit messages with a scheduled delivery due at
	// or before now and returns how many messages it released. It skips a
	// message whose row is locked, so a count below limit does not prove that
	// nothing else is due.
	ReleaseDue(ctx context.Context, now time.Time, limit int) (int64, error)
}

// ReleaserConfig configures RunReleaser. Releaser and Interval are required.
// BatchSize and MaxBatchesPerRun take their defaults when <= 0; Now is
// time.Now, Wake a no-op and Logger slog.Default() when left zero.
type ReleaserConfig struct {
	Releaser DueReleaser
	Interval time.Duration
	// BatchSize is the limit passed to every ReleaseDue call.
	BatchSize int
	// MaxBatchesPerRun caps the ReleaseDue calls of one run; hitting it ends the
	// run and the next tick continues.
	MaxBatchesPerRun int

	// Now supplies the release time, so tests can run on a fake clock.
	Now func() time.Time
	// Wake nudges the delivery worker after a run released messages.
	Wake   func()
	Logger *slog.Logger
}

// RunReleaser releases due scheduled messages immediately and then every
// config.Interval until ctx is done. A run calls ReleaseDue with config.BatchSize
// until a call returns fewer messages than that or MaxBatchesPerRun calls were
// made, then wakes the worker once and logs the total at Info when it is above
// 0. A short batch ends the run, but ReleaseDue skips locked messages, so the
// next tick, not this run, picks up anything it skipped. An error ends the run
// after the messages released so far are woken and logged, and never stops the
// loop; a panic is recovered. It returns at once, after logging an error, when
// Releaser is nil or Interval is not positive.
func RunReleaser(ctx context.Context, config ReleaserConfig) {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	wake := config.Wake
	if wake == nil {
		wake = func() {}
	}
	batchSize := config.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultReleaseBatchSize
	}
	maxBatches := config.MaxBatchesPerRun
	if maxBatches <= 0 {
		maxBatches = DefaultMaxReleaseBatches
	}
	releaser, interval := config.Releaser, config.Interval
	if releaser == nil || interval <= 0 {
		logger.Error("scheduled-message releaser not started: invalid configuration")
		return
	}

	// drain releases batches and returns how many messages it released in
	// total, including those released before an error or a panic.
	drain := func() (total int64) {
		defer func() {
			if recover() != nil {
				logger.Error("scheduled release panicked", "released", total)
			}
		}()
		for batch := 0; batch < maxBatches && ctx.Err() == nil; batch++ {
			released, releaseErr := releaser.ReleaseDue(ctx, now(), batchSize)
			if releaseErr != nil {
				if !errors.Is(releaseErr, context.Canceled) {
					logger.Error("scheduled release failed", "released", total, "error", releaseErr)
				}
				return total
			}
			total += released
			if released < int64(batchSize) {
				return total
			}
		}
		return total
	}

	run := func() {
		// Wake and the logger sit behind their own recover so a panic there
		// cannot end the loop either.
		defer func() {
			if recover() != nil {
				logger.Error("scheduled release wake panicked")
			}
		}()
		released := drain()
		if released > 0 {
			// Log first so a panicking wake cannot suppress the count.
			logger.Info("released scheduled messages", "released", released)
			wake()
		}
	}

	if ctx.Err() != nil {
		return
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

const (
	// DefaultCheckerInterval is how often the checker ticks after its immediate
	// first tick.
	DefaultCheckerInterval = 30 * time.Second
	// CheckerStaleAfter is how long after its last successful tick the checker
	// is reported stale: three missed intervals. The prober reuses it, where it
	// is nine DefaultProberInterval scans.
	CheckerStaleAfter = 90 * time.Second
)

// Freshness records when a background loop (the checker, or the prober through
// its own instance) last completed a pass successfully, so /readyz can report
// whether it is still running. It is safe for concurrent use.
type Freshness struct {
	now      func() time.Time
	lastTick atomic.Pointer[time.Time]
}

// NewFreshness returns a tracker that has never ticked. now is the clock Status
// compares against; nil means time.Now.
func NewFreshness(now func() time.Time) *Freshness {
	if now == nil {
		now = time.Now
	}
	return &Freshness{now: now}
}

// Record stores the time of a successful tick.
func (freshness *Freshness) Record(at time.Time) {
	freshness.lastTick.Store(&at)
}

// Status returns the last successful tick time (nil when the checker has never
// ticked) and whether the checker is stale: it never ticked, or its last tick
// is strictly more than CheckerStaleAfter old.
func (freshness *Freshness) Status() (*time.Time, bool) {
	last := freshness.lastTick.Load()
	if last == nil {
		return nil, true
	}
	return last, freshness.now().Sub(*last) > CheckerStaleAfter
}

// Ticker is one checker pass. *Service satisfies it.
type Ticker interface {
	Tick(ctx context.Context) error
}

// CheckerConfig configures RunChecker. Service and Interval are required; Now
// is time.Now and Logger slog.Default() when left zero. Freshness is optional.
type CheckerConfig struct {
	Service  Ticker
	Interval time.Duration
	// Now supplies the time recorded in Freshness after a successful tick.
	Now    func() time.Time
	Logger *slog.Logger
	// Freshness, when set, receives Now() after every successful tick (never
	// after a failed or panicking one).
	Freshness *Freshness
}

// RunChecker ticks immediately and then every config.Interval until ctx is
// done. A tick that succeeds records the time in config.Freshness; a failing
// tick is logged (a context cancellation is not) and never stops the loop or
// advances freshness; a panic is recovered and logged. It returns at once, after
// logging an error, when Service is nil or Interval is not positive.
func RunChecker(ctx context.Context, config CheckerConfig) {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	service, interval := config.Service, config.Interval
	if service == nil || interval <= 0 {
		logger.Error("heartbeat checker not started: invalid configuration")
		return
	}

	run := func() {
		defer func() {
			if recover() != nil {
				logger.Error("heartbeat tick panicked")
			}
		}()
		if tickErr := service.Tick(ctx); tickErr != nil {
			if !errors.Is(tickErr, context.Canceled) {
				logger.Error("heartbeat tick failed", "error", tickErr)
			}
			return
		}
		if config.Freshness != nil {
			config.Freshness.Record(now())
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

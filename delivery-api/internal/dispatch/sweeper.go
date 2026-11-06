package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/envint"
)

const (
	// DefaultRetentionDays is how long messages are kept when
	// DELIVERY_RETENTION_DAYS is unset.
	DefaultRetentionDays = 30
	// MaxRetentionDays is the largest accepted DELIVERY_RETENTION_DAYS.
	MaxRetentionDays = 3650
	// RetentionEnvVar names the optional environment variable that overrides
	// the retention window.
	RetentionEnvVar = "DELIVERY_RETENTION_DAYS"
	// DefaultHeldTTLDays is how long an unreleased held message is kept before
	// the sweeper expires it.
	DefaultHeldTTLDays = 7
	// MaxHeldTTLDays is the largest accepted DELIVERY_HELD_TTL_DAYS.
	MaxHeldTTLDays = 365
	// HeldTTLEnvVar names the optional environment variable that overrides the
	// held-message expiry.
	HeldTTLEnvVar = "DELIVERY_HELD_TTL_DAYS"
	// DefaultSweepInterval is how often the sweeper runs after its immediate
	// first sweep.
	DefaultSweepInterval = time.Hour

	hoursPerDay = 24
)

// Expirer deletes messages that have outlived the retention window. It is
// satisfied by *message.PGStore.
type Expirer interface {
	// DeleteExpired deletes messages older than cutoff that have no pending or
	// scheduled delivery and returns how many. A message's age counts from the
	// latest of created_at, deliver_at, released_at and release_failed_at, or
	// from cancelled_at for a cancelled message; a still-held message is exempt.
	DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
	// DeleteExpiredHeld deletes held messages whose held_at is older than
	// heldCutoff and returns how many.
	DeleteExpiredHeld(ctx context.Context, heldCutoff time.Time) (int64, error)
}

// RetentionFromEnv reads DELIVERY_RETENTION_DAYS through getenv. An empty value
// gives DefaultRetentionDays; anything else must be an integer from 1 to
// MaxRetentionDays. The error text never echoes the value.
func RetentionFromEnv(getenv func(string) string) (time.Duration, error) {
	days, err := envint.Bounded(getenv, RetentionEnvVar, DefaultRetentionDays, MaxRetentionDays)
	if err != nil {
		return 0, err
	}
	return time.Duration(days) * hoursPerDay * time.Hour, nil
}

// HeldTTLFromEnv reads DELIVERY_HELD_TTL_DAYS through getenv. An empty value
// gives DefaultHeldTTLDays; anything else must be an integer from 1 to
// MaxHeldTTLDays. The error text never echoes the value.
func HeldTTLFromEnv(getenv func(string) string) (time.Duration, error) {
	days, err := envint.Bounded(getenv, HeldTTLEnvVar, DefaultHeldTTLDays, MaxHeldTTLDays)
	if err != nil {
		return 0, err
	}
	return time.Duration(days) * hoursPerDay * time.Hour, nil
}

// SweeperConfig configures RunSweeper. Expirer, Retention, HeldTTL and Interval
// are required; Now is time.Now and Logger is slog.Default() when left zero.
type SweeperConfig struct {
	Expirer   Expirer
	Retention time.Duration
	// HeldTTL is how long an unreleased held message is kept, counted from
	// held_at.
	HeldTTL  time.Duration
	Interval time.Duration

	// Now supplies the sweep time, so tests can run on a fake clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// RunSweeper deletes expired messages immediately and then every
// config.Interval until ctx is done. A sweep removes messages older than
// config.Retention that have no pending or scheduled delivery; age counts from
// the latest of created_at, deliver_at, released_at and release_failed_at, or
// from cancelled_at for a cancelled message (see message.PGStore.DeleteExpired),
// and independently expires held
// messages whose held_at is older than config.HeldTTL. It logs each deleted
// count at Info when it is above 0 and a failure at Error; a failed sweep never
// stops the loop or skips the other phase. It returns at once, after logging an
// error, when Retention, HeldTTL or Interval is not positive or Expirer is nil.
func RunSweeper(ctx context.Context, config SweeperConfig) {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	expirer, retention, heldTTL, interval := config.Expirer, config.Retention, config.HeldTTL, config.Interval
	if expirer == nil || retention <= 0 || heldTTL <= 0 || interval <= 0 {
		logger.Error("sweeper not started: invalid configuration",
			"expirer_nil", expirer == nil, "retention_valid", retention > 0,
			"held_ttl_valid", heldTTL > 0, "interval_valid", interval > 0)
		return
	}

	// Each phase has its own recover guard so a panic or error in one never
	// skips the other; the next tick retries.
	retentionSweep := func(sweepStart time.Time) {
		defer func() {
			if recover() != nil {
				logger.Error("retention sweep panicked")
			}
		}()
		deleted, deleteErr := expirer.DeleteExpired(ctx, sweepStart.Add(-retention))
		if deleteErr != nil && !errors.Is(deleteErr, context.Canceled) {
			logger.Error("retention sweep failed", "deleted", deleted, "error", deleteErr)
			return
		}
		if deleted > 0 {
			logger.Info("retention sweep deleted expired messages", "deleted", deleted)
		}
	}
	heldSweep := func(sweepStart time.Time) {
		defer func() {
			if recover() != nil {
				logger.Error("held sweep panicked")
			}
		}()
		deleted, deleteErr := expirer.DeleteExpiredHeld(ctx, sweepStart.Add(-heldTTL))
		if deleteErr != nil && !errors.Is(deleteErr, context.Canceled) {
			logger.Error("held sweep failed", "deleted", deleted, "error", deleteErr)
			return
		}
		if deleted > 0 {
			logger.Info("held messages expired", "deleted", deleted)
		}
	}
	sweep := func() {
		sweepStart := now()
		retentionSweep(sweepStart)
		heldSweep(sweepStart)
	}

	if ctx.Err() != nil {
		return
	}
	sweep()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

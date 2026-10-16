package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"
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
	// DefaultSweepInterval is how often the sweeper runs after its immediate
	// first sweep.
	DefaultSweepInterval = time.Hour

	hoursPerDay = 24
)

// Expirer deletes messages that have outlived the retention window. It is
// satisfied by *message.PGStore.
type Expirer interface {
	// DeleteExpired deletes messages created before cutoff that have no pending
	// delivery and returns how many.
	DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
}

// RetentionFromEnv reads DELIVERY_RETENTION_DAYS through getenv. An empty value
// gives DefaultRetentionDays; anything else must be an integer from 1 to
// MaxRetentionDays. The error text never echoes the value.
func RetentionFromEnv(getenv func(string) string) (time.Duration, error) {
	raw := getenv(RetentionEnvVar)
	if raw == "" {
		return DefaultRetentionDays * hoursPerDay * time.Hour, nil
	}
	days, parseErr := strconv.Atoi(raw)
	if parseErr != nil || days < 1 || days > MaxRetentionDays {
		return 0, fmt.Errorf("invalid %s", RetentionEnvVar)
	}
	return time.Duration(days) * hoursPerDay * time.Hour, nil
}

// SweeperConfig configures RunSweeper. Expirer, Retention and Interval are
// required; Now is time.Now and Logger is slog.Default() when left zero.
type SweeperConfig struct {
	Expirer   Expirer
	Retention time.Duration
	Interval  time.Duration

	// Now supplies the sweep time, so tests can run on a fake clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// RunSweeper deletes expired messages immediately and then every
// config.Interval until ctx is done. A sweep removes messages created more
// than config.Retention before now that have no pending delivery. It logs the
// deleted count at Info when it is above 0 and a failure at Error; a failed
// sweep never stops the loop. It returns at once, after logging an error, when
// Retention or Interval is not positive or Expirer is nil.
func RunSweeper(ctx context.Context, config SweeperConfig) {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	expirer, retention, interval := config.Expirer, config.Retention, config.Interval
	if expirer == nil || retention <= 0 || interval <= 0 {
		logger.Error("retention sweeper not started: invalid configuration")
		return
	}

	sweep := func() {
		// A panic in the store must not take the whole process down; the next
		// tick retries.
		defer func() {
			if recover() != nil {
				logger.Error("retention sweep panicked")
			}
		}()
		deleted, deleteErr := expirer.DeleteExpired(ctx, now().Add(-retention))
		if deleteErr != nil && !errors.Is(deleteErr, context.Canceled) {
			logger.Error("retention sweep failed", "deleted", deleted, "error", deleteErr)
			return
		}
		if deleted > 0 {
			logger.Info("retention sweep deleted expired messages", "deleted", deleted)
		}
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

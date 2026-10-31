package appidentity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// DefaultReplayCleanupInterval is how often the replay ledger (and the old rejected
// registration requests) are swept after the immediate first sweep. A replay row only matters
// until its token expires (minutes), so this is generous.
const DefaultReplayCleanupInterval = 15 * time.Minute

// ReplaySweeper deletes replay-ledger rows that have expired. It is satisfied by *PGStore.
type ReplaySweeper interface {
	// DeleteExpiredReplay deletes every oidc_replay row whose expires_at is before now and
	// returns how many.
	DeleteExpiredReplay(ctx context.Context, now time.Time) (int64, error)
}

// RejectedRequestSweeper deletes rejected registration requests that have aged out of the
// operator's list. It is satisfied by *PGStore.
type RejectedRequestSweeper interface {
	// DeleteExpiredRejectedRequests deletes every rejected app_registration_requests row whose
	// decided_at is before now-RejectedRequestWindow (the rows ListRequests no longer shows)
	// and returns how many.
	DeleteExpiredRejectedRequests(ctx context.Context, now time.Time) (int64, error)
}

// PendingRequestSweeper deletes pending registration requests the operator never decided, so
// they stop holding MaxPendingRequests slots. It is satisfied by *PGStore.
type PendingRequestSweeper interface {
	// DeleteStalePendingRequests deletes every pending app_registration_requests row whose
	// updated_at is before now-ttl and returns how many. It refuses a ttl that is not positive.
	DeleteStalePendingRequests(ctx context.Context, now time.Time, ttl time.Duration) (int64, error)
}

// PublisherMintSweeper deletes publisher mint-attempt rows that aged out of the rate-limit window.
// It is satisfied by *publisher.PGStore.
type PublisherMintSweeper interface {
	// DeleteExpiredPublisherMints deletes every app_publisher_mints row older than now-window and
	// returns how many. It refuses a window that is not positive.
	DeleteExpiredPublisherMints(ctx context.Context, now time.Time, window time.Duration) (int64, error)
}

// ReplayCleanupConfig configures RunReplayCleanup. Store and Interval are required; Now is
// time.Now and Logger is slog.Default() when left zero. RejectedStore is optional: when set,
// the same ticker also sweeps rejected registration requests older than RejectedRequestWindow.
// PendingStore is optional too: when set, the same ticker also deletes pending requests not
// refreshed within PendingTTL. A PendingStore with a PendingTTL that is not positive is
// invalid and its sweep is not run (it never deletes on a bad value). MintStore is optional too:
// when set (with a positive MintWindow, else it is not swept) the same ticker also deletes
// publisher mint-attempt rows older than MintWindow.
type ReplayCleanupConfig struct {
	Store         ReplaySweeper
	RejectedStore RejectedRequestSweeper
	PendingStore  PendingRequestSweeper
	PendingTTL    time.Duration
	MintStore     PublisherMintSweeper
	MintWindow    time.Duration
	Interval      time.Duration

	// Now supplies the sweep time, so tests can run on a fake clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// RunReplayCleanup deletes expired oidc_replay rows immediately and then every
// config.Interval until ctx is done; when config.RejectedStore is set it also deletes rejected
// registration requests older than RejectedRequestWindow, and when config.PendingStore is set
// (with a positive PendingTTL) pending requests not refreshed within PendingTTL, on the same
// tick, and when config.MintStore is set (with a positive MintWindow) publisher mint-attempt rows
// older than MintWindow. It runs on its own
// ticker, independent of the message retention sweeper. Each sweep logs the deleted count at
// Info when it is above 0 and a failure at Error, and logs nothing when there was nothing to
// delete; a failed sweep (including one of the two failing while the other succeeds) never
// stops the loop. It returns at once, after logging an error, when Store is nil or Interval
// is not positive.
func RunReplayCleanup(ctx context.Context, config ReplayCleanupConfig) {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	store, interval := config.Store, config.Interval
	if store == nil || interval <= 0 {
		logger.Error("oidc replay cleanup not started: invalid configuration")
		return
	}

	// runSweep runs one sweep. A panic in the store must not take the whole process down; the
	// next tick retries.
	runSweep := func(label string, deleteRows func() (int64, error), deletedMessage string) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error(label+" panicked", "panic", fmt.Sprint(recovered))
			}
		}()
		deleted, deleteErr := deleteRows()
		if deleteErr != nil && !errors.Is(deleteErr, context.Canceled) {
			logger.Error(label+" failed", "deleted", deleted, "error", deleteErr)
			return
		}
		if deleted > 0 {
			logger.Info(deletedMessage, "deleted", deleted)
		}
	}

	// Fail closed: a pending store without a positive TTL is never swept, because a zero or
	// negative TTL would put the cutoff at or after now and delete every pending request.
	pendingStore, pendingTTL := config.PendingStore, config.PendingTTL
	if pendingStore != nil && pendingTTL <= 0 {
		logger.Error("pending identity request cleanup not started: invalid ttl", "ttl", pendingTTL)
		pendingStore = nil
	}

	// Same fail-closed rule for the publisher mint sweep: a zero window would delete in-window rows.
	mintStore, mintWindow := config.MintStore, config.MintWindow
	if mintStore != nil && mintWindow <= 0 {
		logger.Error("publisher mint cleanup not started: invalid window", "window", mintWindow)
		mintStore = nil
	}

	sweep := func() {
		sweepTime := now()
		runSweep("oidc replay cleanup", func() (int64, error) {
			return store.DeleteExpiredReplay(ctx, sweepTime)
		}, "oidc replay cleanup deleted expired rows")
		if config.RejectedStore != nil {
			runSweep("rejected identity request cleanup", func() (int64, error) {
				return config.RejectedStore.DeleteExpiredRejectedRequests(ctx, sweepTime)
			}, "rejected identity request cleanup deleted old rows")
		}
		if pendingStore != nil {
			runSweep("pending identity request cleanup", func() (int64, error) {
				return pendingStore.DeleteStalePendingRequests(ctx, sweepTime, pendingTTL)
			}, "pending identity request cleanup deleted stale rows")
		}
		if mintStore != nil {
			runSweep("publisher mint cleanup", func() (int64, error) {
				return mintStore.DeleteExpiredPublisherMints(ctx, sweepTime, mintWindow)
			}, "publisher mint cleanup deleted old rows")
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

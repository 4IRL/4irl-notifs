package readiness

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/httpapi"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

const (
	// HeldCacheTTL is how long a refresh attempt is reused before the next one.
	HeldCacheTTL = 30 * time.Second
	// HeldAppsLimit caps the per-app list in a snapshot.
	HeldAppsLimit = 20
	// heldRefreshTimeout bounds one refresh (both queries). Worst-case probe
	// latency is this plus checkTimeout (about 4s), which must stay under the
	// compose healthcheck timeout of 5s.
	heldRefreshTimeout = 2 * time.Second
)

// HeldMessageCounter returns per-app held-message summaries.
type HeldMessageCounter interface {
	HeldSummary(ctx context.Context) ([]message.HeldAppSummary, error)
}

// UnassignedAlertCounter counts pending alerts with no deliverable destination.
type UnassignedAlertCounter interface {
	CountUnassignedAlerts(ctx context.Context) (int, error)
}

// HeldConfig configures a HeldCache.
type HeldConfig struct {
	Messages HeldMessageCounter
	Alerts   UnassignedAlertCounter
	Now      func() time.Time
	Logger   *slog.Logger
}

// HeldCache is a TTL-cached, fail-soft httpapi.HeldReporter.
type HeldCache struct {
	messages HeldMessageCounter
	alerts   UnassignedAlertCounter
	now      func() time.Time
	logger   *slog.Logger

	mutex       sync.Mutex
	hasAttempt  bool
	lastAttempt time.Time
	hasGood     bool
	good        httpapi.HeldSnapshot
	stale       bool
}

var _ httpapi.HeldReporter = (*HeldCache)(nil)

// NewHeldCache returns a HeldCache. It panics on nil Messages or Alerts.
func NewHeldCache(config HeldConfig) *HeldCache {
	if config.Messages == nil || config.Alerts == nil {
		panic("readiness.NewHeldCache: nil counter")
	}
	nowFunc := config.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &HeldCache{messages: config.Messages, alerts: config.Alerts, now: nowFunc, logger: logger}
}

// HeldSnapshot serves the cached snapshot within the TTL, otherwise refreshes
// synchronously (single-flight under the mutex).
func (cache *HeldCache) HeldSnapshot(ctx context.Context) httpapi.HeldSnapshot {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	if cache.hasAttempt && cache.now().Sub(cache.lastAttempt) < HeldCacheTTL {
		return cache.current()
	}

	started := cache.now()
	refreshCtx, cancel := context.WithTimeout(ctx, heldRefreshTimeout)
	snapshot, refreshErr := cache.refresh(refreshCtx)
	cancel()

	if refreshErr != nil {
		if ctx.Err() != nil {
			// The caller went away: not a refresh failure.
			return cache.current()
		}
		cache.lastAttempt = cache.now()
		cache.hasAttempt = true
		logText := "held counts refresh failed; held counts unavailable"
		if cache.hasGood {
			cache.stale = true
			logText = "held counts refresh failed; serving last good value"
		}
		cache.logger.Warn(logText, "error", refreshErr, "duration_ms", cache.now().Sub(started).Milliseconds())
		return cache.current()
	}

	cache.lastAttempt = cache.now()
	cache.hasAttempt = true
	cache.hasGood = true
	cache.stale = false
	cache.good = snapshot
	return cache.current()
}

// current returns the cached snapshot (caller holds the mutex).
func (cache *HeldCache) current() httpapi.HeldSnapshot {
	if !cache.hasGood {
		return httpapi.HeldSnapshot{Available: false}
	}
	snapshot := cache.good
	snapshot.Stale = cache.stale
	snapshot.Apps = append([]httpapi.HeldAppCount{}, cache.good.Apps...)
	if cache.good.OldestHeldAt != nil {
		oldest := *cache.good.OldestHeldAt
		snapshot.OldestHeldAt = &oldest
	}
	return snapshot
}

// refresh runs both queries; it succeeds only if both do.
func (cache *HeldCache) refresh(ctx context.Context) (httpapi.HeldSnapshot, error) {
	summaries, summaryErr := cache.messages.HeldSummary(ctx)
	if summaryErr != nil {
		return httpapi.HeldSnapshot{}, summaryErr
	}
	alertCount, alertErr := cache.alerts.CountUnassignedAlerts(ctx)
	if alertErr != nil {
		return httpapi.HeldSnapshot{}, alertErr
	}

	snapshot := httpapi.HeldSnapshot{
		Available:        true,
		AsOf:             cache.now(),
		UnassignedAlerts: alertCount,
		Apps:             []httpapi.HeldAppCount{},
	}
	for index, summary := range summaries {
		snapshot.HeldMessages += summary.Count
		if snapshot.OldestHeldAt == nil || summary.OldestHeldAt.Before(*snapshot.OldestHeldAt) {
			oldest := summary.OldestHeldAt
			snapshot.OldestHeldAt = &oldest
		}
		if index < HeldAppsLimit {
			snapshot.Apps = append(snapshot.Apps, httpapi.HeldAppCount{AppID: summary.AppID, Count: summary.Count})
		} else {
			snapshot.AppsMore++
		}
	}
	return snapshot, nil
}

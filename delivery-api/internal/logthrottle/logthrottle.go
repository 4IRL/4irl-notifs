// Package logthrottle rate-limits repeating log lines per key.
package logthrottle

import (
	"sync"
	"time"
)

// MaxKeys is the hard bound on keys a Throttle remembers.
const MaxKeys = 1024

// Throttle lets a repeating log line through at most once per interval per
// key. It remembers at most MaxKeys keys. Safe for concurrent use.
type Throttle struct {
	mutex    sync.Mutex
	interval time.Duration
	last     map[string]time.Time
}

// New returns a Throttle with the given interval. A non-positive interval is a
// programmer error and panics.
func New(interval time.Duration) *Throttle {
	if interval <= 0 {
		panic("logthrottle: interval must be positive")
	}
	return &Throttle{interval: interval, last: make(map[string]time.Time)}
}

// Allow reports whether a log for key may be emitted at now, and records it
// when so. At the key bound it first drops entries older than the interval,
// then, if still full, evicts the oldest entry, so a new key is always
// rate-limited from its first log.
func (throttle *Throttle) Allow(key string, now time.Time) bool {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	if previous, ok := throttle.last[key]; ok && now.Sub(previous) < throttle.interval {
		return false
	}
	if _, ok := throttle.last[key]; !ok && len(throttle.last) >= MaxKeys {
		throttle.makeRoom(now)
	}
	throttle.last[key] = now
	return true
}

func (throttle *Throttle) makeRoom(now time.Time) {
	for key, loggedAt := range throttle.last {
		if now.Sub(loggedAt) >= throttle.interval {
			delete(throttle.last, key)
		}
	}
	if len(throttle.last) < MaxKeys {
		return
	}
	var oldestKey string
	var oldest time.Time
	first := true
	for key, loggedAt := range throttle.last {
		if first || loggedAt.Before(oldest) {
			oldestKey, oldest, first = key, loggedAt, false
		}
	}
	delete(throttle.last, oldestKey)
}

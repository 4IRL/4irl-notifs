package message

import (
	"sync"
	"time"
)

const (
	// heldReleaseWarnInterval is the minimum gap between two throttled held
	// release warnings with the same key.
	heldReleaseWarnInterval = 10 * time.Minute
	// logThrottleMaxKeys is the hard bound on keys a logThrottle remembers.
	logThrottleMaxKeys = 1024
)

// logThrottle lets a repeating log line through at most once per interval per
// key. It remembers at most logThrottleMaxKeys keys.
type logThrottle struct {
	mutex    sync.Mutex
	interval time.Duration
	last     map[string]time.Time
}

func newLogThrottle() *logThrottle {
	return &logThrottle{interval: heldReleaseWarnInterval, last: make(map[string]time.Time)}
}

// Allow reports whether a log for key may be emitted at now, and records it
// when so. At the key bound it first drops entries older than the interval,
// then, if still full, evicts the oldest entry, so a new key is always
// rate-limited from its first log.
func (throttle *logThrottle) Allow(key string, now time.Time) bool {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	if previous, ok := throttle.last[key]; ok && now.Sub(previous) < throttle.interval {
		return false
	}
	if _, ok := throttle.last[key]; !ok && len(throttle.last) >= logThrottleMaxKeys {
		throttle.makeRoom(now)
	}
	throttle.last[key] = now
	return true
}

func (throttle *logThrottle) makeRoom(now time.Time) {
	for key, loggedAt := range throttle.last {
		if now.Sub(loggedAt) >= throttle.interval {
			delete(throttle.last, key)
		}
	}
	if len(throttle.last) < logThrottleMaxKeys {
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

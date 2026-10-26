package provisioning

import (
	"sync"
	"time"
)

// attemptThrottle is a cheap in-process sliding-window counter per string key.
// ResetPassword uses it ahead of the grant lookup so a caller hammering one
// person/app pair cannot queue unbounded `ntfy access` subprocesses behind the
// ntfycli mutex. It counts every attempt, rejected ones included, and is
// separate from the person-service reset slot (which only a request that passed
// the grant check may claim), so a rejected request never burns a reset slot.
// Production runs a single provisioning-api replica, so in-process state is
// enough. The zero value is ready to use.
type attemptThrottle struct {
	guard    sync.Mutex
	attempts map[string][]time.Time
	// lastSweep is when the expired-key sweep last ran; the zero value lets the
	// first sweep run immediately.
	lastSweep time.Time
	// sweeps counts sweep runs, so tests can prove the sweep is rate-limited.
	sweeps int
	// now returns the current time; nil means time.Now (tests inject a clock).
	now func() time.Time
}

const (
	// attemptThrottleWindow is the rolling window attempts are counted over.
	attemptThrottleWindow = time.Hour
	// attemptThrottleSweepThreshold is the number of tracked keys above which a
	// new key may first drop every fully expired key, bounding memory.
	attemptThrottleSweepThreshold = 1024
	// attemptThrottleSweepInterval is the minimum time between sweeps, so a flood
	// of distinct keys cannot make every new key pay an O(n) scan under the mutex.
	attemptThrottleSweepInterval = time.Minute
)

// allow records an attempt for key at the current time and reports whether it
// fits within limit attempts per attemptThrottleWindow. When it does not, retryAfter is how
// long until the oldest counted attempt leaves the window; the refused attempt
// is not counted, so a throttled caller is not extended by retrying.
func (throttle *attemptThrottle) allow(key string, limit int) (allowed bool, retryAfter time.Duration) {
	now := time.Now()
	if throttle.now != nil {
		now = throttle.now()
	}
	windowStart := now.Add(-attemptThrottleWindow)

	throttle.guard.Lock()
	defer throttle.guard.Unlock()
	if throttle.attempts == nil {
		throttle.attempts = map[string][]time.Time{}
	}

	recent := throttle.attempts[key][:0]
	for _, attemptAt := range throttle.attempts[key] {
		if attemptAt.After(windowStart) {
			recent = append(recent, attemptAt)
		}
	}
	if len(recent) >= limit {
		throttle.attempts[key] = recent
		return false, recent[0].Add(attemptThrottleWindow).Sub(now)
	}
	if _, tracked := throttle.attempts[key]; !tracked && len(throttle.attempts) >= attemptThrottleSweepThreshold &&
		now.Sub(throttle.lastSweep) >= attemptThrottleSweepInterval {
		throttle.sweep(windowStart)
		throttle.lastSweep = now
	}
	throttle.attempts[key] = append(recent, now)
	return true, 0
}

// sweep drops every key whose attempts have all left the window. The caller
// holds the guard.
func (throttle *attemptThrottle) sweep(windowStart time.Time) {
	throttle.sweeps++
	for key, attemptTimes := range throttle.attempts {
		if len(attemptTimes) == 0 || !attemptTimes[len(attemptTimes)-1].After(windowStart) {
			delete(throttle.attempts, key)
		}
	}
}

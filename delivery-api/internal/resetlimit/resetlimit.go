// Package resetlimit is the door limit for app reset-password calls: a Postgres-backed count of
// attempts per app and per app+person in a rolling window. Reserve records an attempt in one short
// transaction that is committed before the upstream relay starts.
package resetlimit

import "time"

const (
	// AppLimit is the most reset attempts one app may make per Window.
	AppLimit = 30
	// PersonLimit is the most reset attempts one app may make for one person per Window.
	PersonLimit = 3
	// Window is the rolling window both caps count over.
	Window = time.Hour
	// LockPrefix prefixes the app id in the per-app advisory lock key.
	LockPrefix = "reset_attempt:"
	// ReserveTimeout bounds the whole reservation (begin, lock, prune, count, insert, commit).
	ReserveTimeout = 3 * time.Second
)

// Scope names which cap refused an attempt.
const (
	ScopeApp    = "app"
	ScopePerson = "person"
)

// RateLimitedError is returned by Reserve when a cap is reached. Nothing was recorded.
type RateLimitedError struct {
	// RetryAfter is the wait until the attempt could be admitted, in whole seconds, at least 1s.
	RetryAfter time.Duration
	// Scope is ScopeApp when the app cap is hit (including when both are), else ScopePerson.
	Scope string
}

// Error implements error.
func (*RateLimitedError) Error() string { return "reset rate limit exceeded" }

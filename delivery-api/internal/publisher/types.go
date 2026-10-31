// Package publisher holds the per-app ntfy publisher token: minted once through
// provisioning-api by an explicit operator action, stored sealed, and read back
// by the ntfy sink when it publishes a to-recipient envelope. types.go is pure
// domain logic; store.go, pgstore.go and service.go are the I/O layer.
//
// The import graph stays acyclic: publisher imports registry, seal and
// provisionclient, and never dispatch or message.
package publisher

import (
	"errors"
	"time"
)

// ErrMintUnavailable means minting cannot run right now: it is not configured (no provisioning-api
// origin), the process-wide in-flight cap is full, the transaction or lock could not be taken, or
// the token was minted upstream but could not be stored. Callers match it with errors.Is.
var ErrMintUnavailable = errors.New("publisher minting is unavailable")

// ErrMintBusy means another mint for the same app is in flight. The second mint is refused at once
// (never queued), so the stored token is always the last one minted.
var ErrMintBusy = errors.New("publisher mint already in progress")

// RateLimitedError reports that an app has used its mint quota. RetryAfter is the time until the
// oldest counted attempt ages out of the window, rounded up to whole seconds (minimum one second).
type RateLimitedError struct {
	RetryAfter time.Duration
}

// Error returns a fixed message that carries no app data.
func (rateLimited *RateLimitedError) Error() string { return "publisher mint rate limit exceeded" }

// Mint tuning. The in-flight cap and the begin timeout are tied to the database pool: the pool
// default is max(4, NumCPU) connections (database.NewPool sets no MaxConns), and each in-flight mint
// holds exactly one connection for up to the 15s upstream call. Raise the pool's MaxConns before
// raising MaxInFlightMints.
const (
	// MintRateLimit is the number of app-route mint attempts allowed per app per MintRateWindow.
	MintRateLimit = 10
	// MintRateWindow is the rolling window of the per-app mint limit.
	MintRateWindow = time.Hour
	// MintLockPrefix prefixes the app id in the advisory-lock key of a mint, so integration tests
	// derive the identical key.
	MintLockPrefix = "publisher_mint:"
	// MaxInFlightMints caps concurrent mints across all apps (both routes).
	MaxInFlightMints = 2
	// MintBeginTimeout bounds the pool acquire, BEGIN and try-lock query of a mint.
	MintBeginTimeout = 3 * time.Second
	// MintPersistTimeout bounds the grant check, seal and upsert after the upstream mint returned.
	MintPersistTimeout = 5 * time.Second
	// MintCommitTimeout bounds the commit or rollback of a mint transaction.
	MintCommitTimeout = 5 * time.Second
)

// ValidationError reports a request field that failed validation. Message is
// safe to return to the client: it names the field, never its value. It mirrors
// registry.ValidationError and message.ValidationError so this package keeps its
// own errors.
type ValidationError struct {
	// Message names the offending field, for example "invalid app_id".
	Message string
}

// Error returns the validation message.
func (validationError *ValidationError) Error() string { return validationError.Message }

// StoredPublisher is a publisher as persisted. SealedToken holds the AES-GCM
// sealed ntfy token and is never serialized.
type StoredPublisher struct {
	AppID           string
	PublisherUserID string
	TopicPattern    string
	SealedToken     []byte `json:"-"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PublisherView is the operator-facing publisher record. It carries no token.
// It is the public admin-API view of a publisher and reads naturally as
// publisher.PublisherView at call sites, so the revive stutter check is waived.
//
//nolint:revive // the admin-API view; publisher.PublisherView reads clearly at call sites
type PublisherView struct {
	AppID           string    `json:"app_id"`
	PublisherUserID string    `json:"publisher_user_id"`
	TopicPattern    string    `json:"topic_pattern"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

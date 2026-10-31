package publisher

import (
	"context"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

// Store persists app publishers.
type Store interface {
	// Get returns the app's publisher, or registry.ErrNoPublisher.
	Get(ctx context.Context, appID string) (StoredPublisher, error)
	// Delete removes the app's publisher row. It is idempotent: an app with no
	// publisher is not an error.
	Delete(ctx context.Context, appID string) error
	// WithAppMintLock runs fn inside ONE transaction holding a non-blocking per-app advisory
	// lock. If the lock is held elsewhere it returns ErrMintBusy without calling fn. A begin or
	// lock failure returns an error satisfying errors.Is(err, ErrMintUnavailable). When fn
	// returns an error the transaction still commits (so a recorded slot persists) and fn's error
	// is returned; a panic in fn rolls back and is re-raised. The lock is released on every exit.
	WithAppMintLock(ctx context.Context, appID string, fn func(tx MintTx) error) error
}

// MintTx is the transaction-bound view a mint uses inside WithAppMintLock. Both operations run
// on the same connection as the lock.
type MintTx interface {
	// ReserveMintSlot prunes the app's attempts older than window, returns *RateLimitedError when
	// limit attempts remain in the window, and otherwise records one attempt.
	ReserveMintSlot(ctx context.Context, appID string, limit int, window time.Duration) error
	// Upsert inserts or replaces the app's publisher (in a savepoint, so a failed statement does
	// not abort the transaction).
	Upsert(ctx context.Context, publisher StoredPublisher) (StoredPublisher, error)
}

// Minter obtains a fresh publisher grant for an app. *provisionclient.Client
// satisfies it.
type Minter interface {
	MintPublisher(ctx context.Context, appID string, rotate bool) (provisionclient.PublisherGrant, error)
}

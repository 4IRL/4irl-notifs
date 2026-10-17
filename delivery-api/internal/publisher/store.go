package publisher

import (
	"context"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

// Store persists app publishers.
type Store interface {
	// Upsert inserts the publisher or replaces the existing row for its app,
	// bumping updated_at, and returns the stored row.
	Upsert(ctx context.Context, publisher StoredPublisher) (StoredPublisher, error)
	// Get returns the app's publisher, or registry.ErrNoPublisher.
	Get(ctx context.Context, appID string) (StoredPublisher, error)
}

// Minter obtains a fresh publisher grant for an app. *provisionclient.Client
// satisfies it.
type Minter interface {
	MintPublisher(ctx context.Context, appID string) (provisionclient.PublisherGrant, error)
}

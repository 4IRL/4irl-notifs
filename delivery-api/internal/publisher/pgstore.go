package publisher

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const publisherColumns = `app_id, publisher_user_id, topic_pattern, sealed_token, created_at, updated_at`

// PGStore is the Postgres-backed Store. It is covered by the integration tests,
// not unit tests.
type PGStore struct {
	pool *pgxpool.Pool
}

// Compile-time check that PGStore satisfies Store.
var _ Store = (*PGStore)(nil)

// NewPGStore returns a PGStore over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Upsert inserts the publisher, or replaces the existing row for its app and
// bumps updated_at, and returns the stored row.
func (store *PGStore) Upsert(ctx context.Context, publisher StoredPublisher) (StoredPublisher, error) {
	stored, scanErr := scanPublisher(store.pool.QueryRow(ctx,
		`INSERT INTO app_publishers (app_id, publisher_user_id, topic_pattern, sealed_token)
VALUES ($1, $2, $3, $4)
ON CONFLICT (app_id) DO UPDATE SET publisher_user_id = EXCLUDED.publisher_user_id,
  topic_pattern = EXCLUDED.topic_pattern, sealed_token = EXCLUDED.sealed_token, updated_at = now()
RETURNING `+publisherColumns,
		publisher.AppID, publisher.PublisherUserID, publisher.TopicPattern, publisher.SealedToken))
	if scanErr != nil {
		return StoredPublisher{}, pgutil.WrapDBError("upsert app publisher", scanErr)
	}
	return stored, nil
}

// Get returns the app's publisher, or registry.ErrNoPublisher.
func (store *PGStore) Get(ctx context.Context, appID string) (StoredPublisher, error) {
	stored, scanErr := scanPublisher(store.pool.QueryRow(ctx,
		`SELECT `+publisherColumns+` FROM app_publishers WHERE app_id = $1`, appID))
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return StoredPublisher{}, registry.ErrNoPublisher
		}
		return StoredPublisher{}, pgutil.WrapDBError("get app publisher", scanErr)
	}
	return stored, nil
}

func scanPublisher(row pgutil.RowScanner) (StoredPublisher, error) {
	var stored StoredPublisher
	scanErr := row.Scan(&stored.AppID, &stored.PublisherUserID, &stored.TopicPattern,
		&stored.SealedToken, &stored.CreatedAt, &stored.UpdatedAt)
	return stored, scanErr
}

package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const publisherColumns = `app_id, publisher_user_id, topic_pattern, sealed_token, created_at, updated_at`

const upsertPublisherSQL = `INSERT INTO app_publishers (app_id, publisher_user_id, topic_pattern, sealed_token)
VALUES ($1, $2, $3, $4)
ON CONFLICT (app_id) DO UPDATE SET publisher_user_id = EXCLUDED.publisher_user_id,
  topic_pattern = EXCLUDED.topic_pattern, sealed_token = EXCLUDED.sealed_token, updated_at = now()
RETURNING ` + publisherColumns

// PGStore is the Postgres-backed Store. It is covered by the integration tests,
// not unit tests.
type PGStore struct {
	pool *pgxpool.Pool

	// beginTimeout and commitTimeout default to MintBeginTimeout and MintCommitTimeout; in-package
	// integration tests override them.
	beginTimeout  time.Duration
	commitTimeout time.Duration
	logger        *slog.Logger
}

// Compile-time check that PGStore satisfies Store.
var _ Store = (*PGStore)(nil)

// NewPGStore returns a PGStore over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, beginTimeout: MintBeginTimeout, commitTimeout: MintCommitTimeout, logger: slog.Default()}
}

func upsertPublisher(ctx context.Context, querier pgutil.Querier, publisher StoredPublisher) (StoredPublisher, error) {
	stored, scanErr := scanPublisher(querier.QueryRow(ctx, upsertPublisherSQL,
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

// Delete removes the app's publisher row. Deleting an app with no publisher is
// not an error.
func (store *PGStore) Delete(ctx context.Context, appID string) error {
	if _, execErr := store.pool.Exec(ctx, `DELETE FROM app_publishers WHERE app_id = $1`, appID); execErr != nil {
		return pgutil.WrapDBError("delete app publisher", execErr)
	}
	return nil
}

// WithAppMintLock implements Store. One transaction (one pooled connection) holds a non-blocking
// advisory try-lock keyed on MintLockPrefix+appID; fn's work runs on it. The transaction commits
// even when fn fails, on a detached context bounded by commitTimeout; it rolls back on a panic.
func (store *PGStore) WithAppMintLock(ctx context.Context, appID string, fn func(tx MintTx) error) error {
	beginCtx, cancelBegin := context.WithTimeout(ctx, store.beginTimeout)
	defer cancelBegin()
	tx, beginErr := store.pool.Begin(beginCtx)
	if beginErr != nil {
		return fmt.Errorf("%w: %w", ErrMintUnavailable, pgutil.WrapDBError("begin mint transaction", beginErr))
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), store.commitTimeout)
		defer cancelRollback()
		_ = tx.Rollback(rollbackCtx)
	}()

	var locked bool
	lockErr := tx.QueryRow(beginCtx,
		`SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, MintLockPrefix+appID).Scan(&locked)
	if lockErr != nil {
		return fmt.Errorf("%w: %w", ErrMintUnavailable, pgutil.WrapDBError("take mint lock", lockErr))
	}
	if !locked {
		return ErrMintBusy
	}
	cancelBegin()

	fnErr := fn(&pgMintTx{tx: tx, commitTimeout: store.commitTimeout})

	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), store.commitTimeout)
	defer cancelCommit()
	commitErr := tx.Commit(commitCtx)
	committed = commitErr == nil
	if fnErr != nil {
		if commitErr != nil {
			store.logger.ErrorContext(ctx, "mint transaction commit failed after fn error",
				"app_id", appID, "error", commitErr)
		}
		return fnErr
	}
	if commitErr != nil {
		return pgutil.WrapDBError("commit mint transaction", commitErr)
	}
	return nil
}

// pgMintTx is the MintTx over one open transaction.
type pgMintTx struct {
	tx            pgx.Tx
	commitTimeout time.Duration
}

// ReserveMintSlot implements MintTx.
func (mintTx *pgMintTx) ReserveMintSlot(ctx context.Context, appID string, limit int, window time.Duration) error {
	windowSeconds := window.Seconds()
	if _, pruneErr := mintTx.tx.Exec(ctx,
		`DELETE FROM app_publisher_mints WHERE app_id = $1 AND minted_at < now() - $2::float8 * interval '1 second'`,
		appID, windowSeconds); pruneErr != nil {
		return pgutil.WrapDBError("prune publisher mints", pruneErr)
	}
	var used int
	var retrySeconds int64
	if countErr := mintTx.tx.QueryRow(ctx,
		`SELECT count(*), COALESCE(ceil(extract(epoch FROM (min(minted_at) + $2::float8 * interval '1 second' - now())))::bigint, 0)
FROM app_publisher_mints WHERE app_id = $1`,
		appID, windowSeconds).Scan(&used, &retrySeconds); countErr != nil {
		return pgutil.WrapDBError("count publisher mints", countErr)
	}
	if used >= limit {
		retrySeconds = max(retrySeconds, 1)
		return &RateLimitedError{RetryAfter: time.Duration(retrySeconds) * time.Second}
	}
	if _, insertErr := mintTx.tx.Exec(ctx,
		`INSERT INTO app_publisher_mints (app_id) VALUES ($1)`, appID); insertErr != nil {
		return pgutil.WrapDBError("record publisher mint", insertErr)
	}
	return nil
}

// Upsert implements MintTx. It runs in a savepoint so a failed statement does not abort the
// transaction (and lose the recorded slot row).
func (mintTx *pgMintTx) Upsert(ctx context.Context, publisher StoredPublisher) (StoredPublisher, error) {
	savepoint, savepointErr := mintTx.tx.Begin(ctx)
	if savepointErr != nil {
		return StoredPublisher{}, pgutil.WrapDBError("begin upsert savepoint", savepointErr)
	}
	stored, upsertErr := upsertPublisher(ctx, savepoint, publisher)
	if upsertErr != nil {
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), mintTx.commitTimeout)
		defer cancelRollback()
		_ = savepoint.Rollback(rollbackCtx)
		return StoredPublisher{}, upsertErr
	}
	if releaseErr := savepoint.Commit(ctx); releaseErr != nil {
		return StoredPublisher{}, pgutil.WrapDBError("release upsert savepoint", releaseErr)
	}
	return stored, nil
}

// DeleteExpiredPublisherMints deletes mint-attempt rows older than window and returns how many.
// It refuses a window that is not positive, which would delete in-window rows and reopen the limit.
func (store *PGStore) DeleteExpiredPublisherMints(ctx context.Context, now time.Time, window time.Duration) (int64, error) {
	if window <= 0 {
		return 0, errors.New("delete old publisher mints: window must be positive")
	}
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM app_publisher_mints WHERE minted_at < $1`, now.Add(-window))
	if execErr != nil {
		return 0, pgutil.WrapDBError("delete old publisher mints", execErr)
	}
	return tag.RowsAffected(), nil
}

func scanPublisher(row pgutil.RowScanner) (StoredPublisher, error) {
	var stored StoredPublisher
	scanErr := row.Scan(&stored.AppID, &stored.PublisherUserID, &stored.TopicPattern,
		&stored.SealedToken, &stored.CreatedAt, &stored.UpdatedAt)
	return stored, scanErr
}

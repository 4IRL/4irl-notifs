package migrate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey serializes concurrent migrators; it is the ASCII bytes of
// "delivery".
const advisoryLockKey int64 = 0x64656c6976657279

const (
	// defaultLockTimeout stays below the deploy verify loop (150s) so a stuck lock
	// holder fails the migrate step visibly instead of hanging the deploy.
	defaultLockTimeout = 60 * time.Second
	lockPollInterval   = 250 * time.Millisecond

	// maxRecordedFailureRunes caps the failure message stored in
	// schema_migration_failures.
	maxRecordedFailureRunes = 500
)

var errLockTimeout = errors.New("timed out waiting for the migration advisory lock")

const (
	createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version integer PRIMARY KEY,
	name text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`
	createFailuresTable = `CREATE TABLE IF NOT EXISTS schema_migration_failures (
	id bigserial PRIMARY KEY,
	version integer NOT NULL,
	error text NOT NULL,
	failed_at timestamptz NOT NULL DEFAULT now()
)`
)

// PGStore is the Postgres-backed Store. It is covered by the integration
// tests, not unit tests.
type PGStore struct {
	pool *pgxpool.Pool
	// lockTimeout is a field so tests can shorten the wait.
	lockTimeout time.Duration
}

// Compile-time check that PGStore satisfies Store.
var _ Store = (*PGStore)(nil)

// NewPGStore returns a PGStore over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, lockTimeout: defaultLockTimeout}
}

// Lock takes a session-level advisory lock on one dedicated connection, polling
// pg_try_advisory_lock until it succeeds, ctx is done, or lockTimeout elapses,
// and returns a func that releases the lock and the connection. If the unlock
// statement fails, the connection is closed instead of returned to the pool,
// which releases the lock server-side so it can never leak to a later user.
func (store *PGStore) Lock(ctx context.Context) (func(context.Context) error, error) {
	conn, acquireErr := store.pool.Acquire(ctx)
	if acquireErr != nil {
		return nil, fmt.Errorf("acquire connection: %w", acquireErr)
	}

	deadline := time.Now().Add(store.lockTimeout)
	for {
		var locked bool
		if lockErr := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryLockKey).Scan(&locked); lockErr != nil {
			conn.Release()
			return nil, fmt.Errorf("pg_try_advisory_lock: %w", lockErr)
		}
		if locked {
			break
		}
		if !time.Now().Before(deadline) {
			conn.Release()
			return nil, fmt.Errorf("%w after %s", errLockTimeout, store.lockTimeout)
		}
		select {
		case <-ctx.Done():
			conn.Release()
			return nil, fmt.Errorf("wait for migration advisory lock: %w", ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}

	unlock := func(unlockCtx context.Context) error {
		if _, unlockErr := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey); unlockErr != nil {
			_ = conn.Hijack().Close(unlockCtx)
			return fmt.Errorf("pg_advisory_unlock: %w", unlockErr)
		}
		conn.Release()
		return nil
	}
	return unlock, nil
}

// EnsureTables creates the bookkeeping tables when they do not exist.
func (store *PGStore) EnsureTables(ctx context.Context) error {
	for _, statement := range []string{createMigrationsTable, createFailuresTable} {
		if _, execErr := store.pool.Exec(ctx, statement); execErr != nil {
			return fmt.Errorf("create bookkeeping table: %w", execErr)
		}
	}
	return nil
}

// AppliedVersions returns the versions recorded in schema_migrations.
func (store *PGStore) AppliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, queryErr := store.pool.Query(ctx, "SELECT version FROM schema_migrations")
	if queryErr != nil {
		return nil, fmt.Errorf("query applied versions: %w", queryErr)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if scanErr := rows.Scan(&version); scanErr != nil {
			return nil, fmt.Errorf("scan applied version: %w", scanErr)
		}
		applied[version] = true
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("read applied versions: %w", rowsErr)
	}
	return applied, nil
}

// Apply runs the migration's SQL and records its schema_migrations row in one
// transaction, rolling back on any error. Exec without arguments uses the
// simple protocol, so a migration file may hold several statements. Because
// everything runs inside one transaction, a migration must not contain its own
// BEGIN/COMMIT or statements Postgres forbids in a transaction block (such as
// CREATE INDEX CONCURRENTLY).
func (store *PGStore) Apply(ctx context.Context, migration Migration) error {
	tx, beginErr := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if beginErr != nil {
		return fmt.Errorf("begin transaction: %w", beginErr)
	}
	// Rollback after a successful Commit is a harmless no-op. Detached from ctx
	// so it still runs after a cancellation, but bounded.
	defer func() {
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancelRollback()
		_ = tx.Rollback(rollbackCtx)
	}()

	if _, execErr := tx.Exec(ctx, migration.SQL); execErr != nil {
		return fmt.Errorf("execute migration sql: %w", execErr)
	}
	if _, insertErr := tx.Exec(
		ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
		migration.Version, migration.Name,
	); insertErr != nil {
		return fmt.Errorf("record migration: %w", insertErr)
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return fmt.Errorf("commit migration: %w", commitErr)
	}
	return nil
}

// RecordFailure stores a failed migration in its own statement, outside the
// transaction that Apply rolled back. The message is capped at 500 runes.
func (store *PGStore) RecordFailure(ctx context.Context, version int, message string) error {
	if _, execErr := store.pool.Exec(
		ctx,
		"INSERT INTO schema_migration_failures (version, error) VALUES ($1, $2)",
		version, truncateRunes(message, maxRecordedFailureRunes),
	); execErr != nil {
		return fmt.Errorf("insert migration failure: %w", execErr)
	}
	return nil
}

// truncateRunes returns text cut to at most limit runes.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

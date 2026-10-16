package migrate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// undefinedTableSQLState is Postgres's SQLSTATE for "relation does not exist".
const undefinedTableSQLState = "42P01"

// Failure is one recorded migration failure.
type Failure struct {
	Version  int
	Error    string
	FailedAt time.Time
}

// Status is the schema state reported by readiness: the highest applied
// version and, when a newer migration has failed since, that failure.
type Status struct {
	SchemaVersion int
	LastFailure   *Failure
}

// Status reads the current schema version and the newest recorded failure for
// a version above it (a failure later fixed by a successful apply is no longer
// reported). Missing bookkeeping tables, i.e. migrate has never run, yield the
// zero Status rather than an error.
func (store *PGStore) Status(ctx context.Context) (Status, error) {
	var status Status

	versionErr := store.pool.QueryRow(
		ctx,
		"SELECT coalesce(max(version), 0) FROM schema_migrations",
	).Scan(&status.SchemaVersion)
	if versionErr != nil {
		if isUndefinedTable(versionErr) {
			return Status{}, nil
		}
		return Status{}, fmt.Errorf("read schema version: %w", versionErr)
	}

	var failure Failure
	failureErr := store.pool.QueryRow(
		ctx,
		`SELECT version, error, failed_at FROM schema_migration_failures
		WHERE version > $1 ORDER BY id DESC LIMIT 1`,
		status.SchemaVersion,
	).Scan(&failure.Version, &failure.Error, &failure.FailedAt)
	switch {
	case failureErr == nil:
		status.LastFailure = &failure
	case errors.Is(failureErr, pgx.ErrNoRows), isUndefinedTable(failureErr):
		// No qualifying failure to report.
	default:
		return Status{}, fmt.Errorf("read last migration failure: %w", failureErr)
	}
	return status, nil
}

// isUndefinedTable reports whether err is Postgres's undefined-table error.
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == undefinedTableSQLState
}

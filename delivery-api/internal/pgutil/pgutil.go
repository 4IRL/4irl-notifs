// Package pgutil holds the pgx helpers shared by the Postgres-backed stores: the
// query surface shared by a pool and a transaction, a transaction runner that
// always rolls back safely, and a database-error wrapper that never leaks row
// content.
package pgutil

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rollbackTimeout bounds the deferred rollback, which runs detached from the
// request context so a cancelled request still releases its locks.
const rollbackTimeout = 5 * time.Second

// Querier is the read/write surface shared by *pgxpool.Pool and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// RowScanner is satisfied by both pgx.Row and pgx.Rows.
type RowScanner interface {
	Scan(dest ...any) error
}

// WithTx runs fn in a transaction on pool with the given options and commits
// when it returns nil. fn's error is returned unchanged so sentinels and mutate
// errors survive; the rollback is deferred and harmless after a successful
// commit.
func WithTx(ctx context.Context, pool *pgxpool.Pool, options pgx.TxOptions, fn func(tx pgx.Tx) error) error {
	tx, beginErr := pool.BeginTx(ctx, options)
	if beginErr != nil {
		return WrapDBError("begin transaction", beginErr)
	}
	defer func() {
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancelRollback()
		_ = tx.Rollback(rollbackCtx)
	}()

	if fnErr := fn(tx); fnErr != nil {
		return fnErr
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return WrapDBError("commit transaction", commitErr)
	}
	return nil
}

// WrapDBError labels a database error with its operation. A *pgconn.PgError is
// flattened to its message and SQLSTATE without %w: its Detail field can hold
// the whole failing row (including sealed_secrets), so it must not stay
// reachable through errors.As or %+v. Other errors (context cancellation,
// connection failures) keep their chain.
func WrapDBError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("%s: %s (SQLSTATE %s)", operation, pgErr.Message, pgErr.Code)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// SQLStateUniqueViolation is the SQLSTATE Postgres reports for a unique-constraint violation.
const SQLStateUniqueViolation = "23505"

// SQLStateCheckViolation is the SQLSTATE Postgres reports for a CHECK-constraint violation.
const SQLStateCheckViolation = "23514"

// ErrorCode returns the SQLSTATE of a Postgres error, or "" for any other.
func ErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Distinct returns names with duplicates removed, never nil.
func Distinct(names []string) []string {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	return unique
}

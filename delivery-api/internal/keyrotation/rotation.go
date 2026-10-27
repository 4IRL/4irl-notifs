package keyrotation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

// rowTimeout bounds every database statement issued for one row (or one table
// read) so a hang names its step instead of stalling the run.
const rowTimeout = 10 * time.Second

// Reason says why a row could not be rotated or verified.
type Reason string

const (
	// ReasonDecryptFailed means the value did not open under any available key
	// with the row's AAD.
	ReasonDecryptFailed Reason = "decrypt_failed"
	// ReasonMalformed means the blob is too short, or the row key was rejected
	// by the table's AAD function.
	ReasonMalformed Reason = "malformed"
	// ReasonUnsupportedVersion means the blob carries an unknown version byte.
	ReasonUnsupportedVersion Reason = "unsupported_version"
	// ReasonChangedConcurrently means the conditional update matched no row: the
	// value changed, or the row was deleted, after it was read.
	ReasonChangedConcurrently Reason = "changed_concurrently"
)

// RowIssue is one row that needs attention. RowKey is a non-secret identifier.
type RowIssue struct {
	Table  string
	RowKey string
	Reason Reason
}

// TableCounts tallies one table's rows.
type TableCounts struct {
	Scanned        int
	Rotated        int
	AlreadyCurrent int
	SkippedNull    int
	Failed         int
}

// Report is the outcome of a Rotate or Verify run.
type Report struct {
	Tables map[string]TableCounts
	Issues []RowIssue
}

// ErrIncomplete means the run finished but some rows need attention.
var ErrIncomplete = errors.New("key rotation incomplete: rows need attention")

// Options configures Rotate.
type Options struct {
	Sealer *seal.Sealer
	Logger *slog.Logger
	// Tables defaults to Registry() when nil.
	Tables []SealedTable
	// BeforeUpdate is a test seam called just before each row's update; an
	// error aborts the run. It is nil in production.
	BeforeUpdate func(ctx context.Context, table SealedTable, rowKey string) error
}

type sealedRow struct {
	key    string
	sealed []byte
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func reasonFor(openErr error) Reason {
	switch {
	case errors.Is(openErr, seal.ErrUnsupportedVersion):
		return ReasonUnsupportedVersion
	case errors.Is(openErr, seal.ErrMalformed):
		return ReasonMalformed
	default:
		return ReasonDecryptFailed
	}
}

// loadRows reads every (key as text, sealed) pair of a table ordered by key.
func loadRows(ctx context.Context, pool *pgxpool.Pool, table SealedTable) ([]sealedRow, error) {
	readCtx, cancel := context.WithTimeout(ctx, rowTimeout)
	defer cancel()
	query := fmt.Sprintf("SELECT %s::text, %s FROM %s ORDER BY %s",
		pgx.Identifier{table.KeyColumn}.Sanitize(), pgx.Identifier{table.SealedColumn}.Sanitize(),
		pgx.Identifier{table.Table}.Sanitize(), pgx.Identifier{table.KeyColumn}.Sanitize())
	rows, queryErr := pool.Query(readCtx, query)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("read "+table.Table, queryErr)
	}
	defer rows.Close()

	var loaded []sealedRow
	for rows.Next() {
		var row sealedRow
		if scanErr := rows.Scan(&row.key, &row.sealed); scanErr != nil {
			return nil, pgutil.WrapDBError("scan "+table.Table, scanErr)
		}
		loaded = append(loaded, row)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("read "+table.Table, rowsErr)
	}
	return loaded, nil
}

// updateSealed swaps the sealed value of one row only if it still equals old,
// in its own transaction. It reports whether a row matched.
func updateSealed(ctx context.Context, pool *pgxpool.Pool, table SealedTable, row sealedRow, resealed []byte) (bool, error) {
	updateCtx, cancel := context.WithTimeout(ctx, rowTimeout)
	defer cancel()
	statement := fmt.Sprintf("UPDATE %s SET %s = $1 WHERE %s::text = $2 AND %s = $3",
		pgx.Identifier{table.Table}.Sanitize(), pgx.Identifier{table.SealedColumn}.Sanitize(),
		pgx.Identifier{table.KeyColumn}.Sanitize(), pgx.Identifier{table.SealedColumn}.Sanitize())
	var matched bool
	txErr := pgutil.WithTx(updateCtx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(updateCtx, statement, resealed, row.key, row.sealed)
		if execErr != nil {
			return pgutil.WrapDBError("update "+table.Table, execErr)
		}
		matched = tag.RowsAffected() == 1
		return nil
	})
	return matched, txErr
}

func totals(report Report) TableCounts {
	var sum TableCounts
	for _, counts := range report.Tables {
		sum.Scanned += counts.Scanned
		sum.Rotated += counts.Rotated
		sum.AlreadyCurrent += counts.AlreadyCurrent
		sum.SkippedNull += counts.SkippedNull
		sum.Failed += counts.Failed
	}
	return sum
}

func logTableEnd(logger *slog.Logger, table string, counts TableCounts, started time.Time) {
	logger.Info("key rotation table end", "table", table,
		"scanned", counts.Scanned, "rotated", counts.Rotated, "already_current", counts.AlreadyCurrent,
		"skipped_null", counts.SkippedNull, "failed", counts.Failed,
		"duration_ms", time.Since(started).Milliseconds())
}

func logRunEnd(logger *slog.Logger, message string, report Report, outcome string, started time.Time) {
	sum := totals(report)
	logger.Info(message, "outcome", outcome,
		"scanned", sum.Scanned, "rotated", sum.Rotated, "already_current", sum.AlreadyCurrent,
		"skipped_null", sum.SkippedNull, "failed", sum.Failed, "issues", len(report.Issues),
		"duration_ms", time.Since(started).Milliseconds())
}

// logAbort records why a run stopped, with the table, the row when known and the
// error text (which never carries secret material).
func logAbort(logger *slog.Logger, table string, rowKey string, abortErr error, counts TableCounts, started time.Time) {
	logger.Error("key rotation aborted", "table", table, "row_key", rowKey, "error", abortErr.Error(),
		"scanned", counts.Scanned, "rotated", counts.Rotated, "already_current", counts.AlreadyCurrent,
		"skipped_null", counts.SkippedNull, "failed", counts.Failed,
		"duration_ms", time.Since(started).Milliseconds())
}

func recordIssue(logger *slog.Logger, report *Report, counts *TableCounts, table SealedTable, rowKey string, reason Reason) {
	counts.Failed++
	report.Issues = append(report.Issues, RowIssue{Table: table.Table, RowKey: rowKey, Reason: reason})
	logger.Warn("key rotation row issue", "table", table.Table, "row_key", rowKey, "reason", string(reason))
}

// Rotate re-seals every stored secret under options.Sealer's primary key, one
// row at a time. Each update is atomic and conditional on the sealed value
// being unchanged, so a failure mid-run leaves every row openable under the old
// or the new key. Rows already under the new key are skipped. Row-level
// problems are reported in the Report and yield ErrIncomplete; hook and
// database errors abort the run and are returned as plain errors.
func Rotate(ctx context.Context, pool *pgxpool.Pool, options Options) (Report, error) {
	report := Report{Tables: map[string]TableCounts{}}
	if options.Sealer == nil || !options.Sealer.HasPrevious() {
		return report, errors.New("key rotation needs a sealer with both a primary and a previous key")
	}
	logger := options.Logger
	if logger == nil {
		logger = discardLogger()
	}
	tables := options.Tables
	if tables == nil {
		tables = Registry()
	}

	runStarted := time.Now()
	logger.Info("key rotation start", "tables", len(tables))
	for _, table := range tables {
		tableStarted := time.Now()
		logger.Info("key rotation table start", "table", table.Table)
		counts := TableCounts{}
		rows, loadErr := loadRows(ctx, pool, table)
		if loadErr != nil {
			report.Tables[table.Table] = counts
			logAbort(logger, table.Table, "", loadErr, counts, tableStarted)
			logRunEnd(logger, "key rotation end", report, "error", runStarted)
			return report, loadErr
		}
		for _, row := range rows {
			counts.Scanned++
			if row.sealed == nil {
				counts.SkippedNull++
				continue
			}
			aad, aadErr := table.AAD(row.key)
			if aadErr != nil {
				recordIssue(logger, &report, &counts, table, row.key, ReasonMalformed)
				continue
			}
			plaintext, used, openErr := options.Sealer.OpenReportingKey(row.sealed, aad)
			if openErr != nil {
				recordIssue(logger, &report, &counts, table, row.key, reasonFor(openErr))
				continue
			}
			if used == seal.KeyPrimary {
				counts.AlreadyCurrent++
				continue
			}
			resealed, sealErr := options.Sealer.Seal(plaintext, aad)
			if sealErr != nil {
				report.Tables[table.Table] = counts
				logAbort(logger, table.Table, row.key, sealErr, counts, tableStarted)
				logRunEnd(logger, "key rotation end", report, "error", runStarted)
				return report, fmt.Errorf("reseal %s row %s: %w", table.Table, row.key, sealErr)
			}
			if options.BeforeUpdate != nil {
				if hookErr := options.BeforeUpdate(ctx, table, row.key); hookErr != nil {
					report.Tables[table.Table] = counts
					logAbort(logger, table.Table, row.key, hookErr, counts, tableStarted)
					logRunEnd(logger, "key rotation end", report, "aborted", runStarted)
					return report, fmt.Errorf("before update %s row %s: %w", table.Table, row.key, hookErr)
				}
			}
			matched, updateErr := updateSealed(ctx, pool, table, row, resealed)
			if updateErr != nil {
				report.Tables[table.Table] = counts
				logAbort(logger, table.Table, row.key, updateErr, counts, tableStarted)
				logRunEnd(logger, "key rotation end", report, "error", runStarted)
				return report, updateErr
			}
			if !matched {
				recordIssue(logger, &report, &counts, table, row.key, ReasonChangedConcurrently)
				continue
			}
			counts.Rotated++
		}
		report.Tables[table.Table] = counts
		logTableEnd(logger, table.Table, counts, tableStarted)
	}

	if len(report.Issues) > 0 {
		logRunEnd(logger, "key rotation end", report, "incomplete", runStarted)
		return report, ErrIncomplete
	}
	logRunEnd(logger, "key rotation end", report, "complete", runStarted)
	return report, nil
}

// Verify proves every non-NULL sealed value opens under the sealer's primary
// key alone, whatever previous key the sealer carries. It only reads.
func Verify(ctx context.Context, pool *pgxpool.Pool, sealer *seal.Sealer, logger *slog.Logger) (Report, error) {
	report := Report{Tables: map[string]TableCounts{}}
	if sealer == nil {
		return report, errors.New("key verification needs a sealer")
	}
	if logger == nil {
		logger = discardLogger()
	}
	primaryOnly := sealer.PrimaryOnly()
	tables := Registry()

	runStarted := time.Now()
	logger.Info("key verification start", "tables", len(tables))
	for _, table := range tables {
		tableStarted := time.Now()
		logger.Info("key verification table start", "table", table.Table)
		counts := TableCounts{}
		rows, loadErr := loadRows(ctx, pool, table)
		if loadErr != nil {
			report.Tables[table.Table] = counts
			logAbort(logger, table.Table, "", loadErr, counts, tableStarted)
			logRunEnd(logger, "key verification end", report, "error", runStarted)
			return report, loadErr
		}
		for _, row := range rows {
			counts.Scanned++
			if row.sealed == nil {
				counts.SkippedNull++
				continue
			}
			aad, aadErr := table.AAD(row.key)
			if aadErr != nil {
				recordIssue(logger, &report, &counts, table, row.key, ReasonMalformed)
				continue
			}
			if _, _, openErr := primaryOnly.OpenReportingKey(row.sealed, aad); openErr != nil {
				recordIssue(logger, &report, &counts, table, row.key, reasonFor(openErr))
				continue
			}
			counts.AlreadyCurrent++
		}
		report.Tables[table.Table] = counts
		logTableEnd(logger, table.Table, counts, tableStarted)
	}

	if len(report.Issues) > 0 {
		logRunEnd(logger, "key verification end", report, "incomplete", runStarted)
		return report, ErrIncomplete
	}
	logRunEnd(logger, "key verification end", report, "complete", runStarted)
	return report, nil
}

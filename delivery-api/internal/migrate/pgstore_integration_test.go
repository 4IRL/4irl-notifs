//go:build integration

package migrate

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultTestDSN          = "postgres://delivery:delivery-local-dev@127.0.0.1:18300/delivery?sslmode=disable"
	failingMigrationVersion = 9001
	integrationTestBudget   = 30 * time.Second
)

// testDSN returns the local-only delivery-postgres DSN. make
// delivery-integration-test derives it from the worktree's DELIVERY_PORT.
func testDSN() string {
	if value := os.Getenv("NOTIFS_DELIVERY_TEST_DSN"); value != "" {
		return value
	}
	return defaultTestDSN
}

func TestFailingMigrationRollsBackAndRecordsFailure(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()

	pool, poolErr := pgxpool.New(ctx, testDSN())
	if poolErr != nil {
		testInstance.Fatalf("open pool: %v", poolErr)
	}
	defer pool.Close()
	if pingErr := pool.Ping(ctx); pingErr != nil {
		testInstance.Fatalf("ping delivery-postgres (is the stack up and NOTIFS_DELIVERY_TEST_DSN's port right?): %v", pingErr)
	}

	const cleanupSQL = "DELETE FROM schema_migration_failures WHERE version = $1"
	// Cleanup runs on a fresh context so it still executes if ctx expired.
	cleanup := func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancelCleanup()
		_, _ = pool.Exec(cleanupCtx, cleanupSQL, failingMigrationVersion)
	}
	cleanup()
	defer cleanup()

	// A non-embedded migration that raises a 600-character error, so the 500
	// rune truncation is exercised deterministically.
	failing := []Migration{{
		Version: failingMigrationVersion,
		Name:    "integration_bad",
		SQL:     `DO $$ BEGIN RAISE EXCEPTION '%', repeat('x', 600); END $$;`,
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	appliedCount, runErr := Run(ctx, NewPGStore(pool), failing, logger)

	if runErr == nil {
		testInstance.Fatal("Run returned nil error for a failing migration")
	}
	if appliedCount != 0 {
		testInstance.Fatalf("appliedCount = %d, want 0", appliedCount)
	}

	var appliedRows int
	if scanErr := pool.QueryRow(
		ctx, "SELECT count(*) FROM schema_migrations WHERE version = $1", failingMigrationVersion,
	).Scan(&appliedRows); scanErr != nil {
		testInstance.Fatalf("count schema_migrations: %v", scanErr)
	}
	if appliedRows != 0 {
		testInstance.Fatalf("schema_migrations rows for %d = %d, want 0 (transaction must roll back)",
			failingMigrationVersion, appliedRows)
	}

	var failureRows, recordedLength int
	if scanErr := pool.QueryRow(
		ctx,
		"SELECT count(*), coalesce(max(length(error)), 0) FROM schema_migration_failures WHERE version = $1",
		failingMigrationVersion,
	).Scan(&failureRows, &recordedLength); scanErr != nil {
		testInstance.Fatalf("read schema_migration_failures: %v", scanErr)
	}
	if failureRows != 1 {
		testInstance.Fatalf("schema_migration_failures rows for %d = %d, want 1", failingMigrationVersion, failureRows)
	}
	if recordedLength != maxRecordedFailureRunes {
		testInstance.Fatalf("recorded error length = %d, want %d (the 600-character message must be truncated)",
			recordedLength, maxRecordedFailureRunes)
	}
}

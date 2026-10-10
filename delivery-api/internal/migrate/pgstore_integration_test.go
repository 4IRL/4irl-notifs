//go:build integration

package migrate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultTestDSN          = "postgres://delivery:delivery-local-dev@127.0.0.1:18300/delivery?sslmode=disable"
	failingMigrationVersion = 9001
	integrationTestBudget   = 30 * time.Second
	shortLockWait           = 600 * time.Millisecond
)

// testDSN returns the local-only delivery-postgres DSN. make
// delivery-integration-test derives it from the worktree's DELIVERY_PORT.
func testDSN() string {
	if value := os.Getenv("NOTIFS_DELIVERY_TEST_DSN"); value != "" {
		return value
	}
	return defaultTestDSN
}

// openTestPool opens a pool on the local delivery-postgres, closed at test end.
// runtimeParams, when non-nil, are set on every connection (e.g. search_path).
func openTestPool(ctx context.Context, testInstance *testing.T, runtimeParams map[string]string) *pgxpool.Pool {
	testInstance.Helper()
	poolConfig, parseErr := pgxpool.ParseConfig(testDSN())
	if parseErr != nil {
		testInstance.Fatalf("parse test dsn: %v", parseErr)
	}
	for key, value := range runtimeParams {
		poolConfig.ConnConfig.RuntimeParams[key] = value
	}
	pool, poolErr := pgxpool.NewWithConfig(ctx, poolConfig)
	if poolErr != nil {
		testInstance.Fatalf("open pool: %v", poolErr)
	}
	testInstance.Cleanup(pool.Close)
	if pingErr := pool.Ping(ctx); pingErr != nil {
		testInstance.Fatalf("ping delivery-postgres (is the stack up and NOTIFS_DELIVERY_TEST_DSN's port right?): %v", pingErr)
	}
	return pool
}

func TestFailingMigrationRollsBackAndRecordsFailure(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()

	pool := openTestPool(ctx, testInstance, nil)

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

func TestLockExcludesSecondHolderUntilReleased(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()
	pool := openTestPool(ctx, testInstance, nil)

	unlockFirst, firstErr := NewPGStore(pool).Lock(ctx)
	if firstErr != nil {
		testInstance.Fatalf("first Lock: %v", firstErr)
	}
	firstReleased := false
	defer func() {
		if !firstReleased {
			_ = unlockFirst(context.Background())
		}
	}()

	blockedCtx, cancelBlocked := context.WithTimeout(ctx, shortLockWait)
	_, blockedErr := NewPGStore(pool).Lock(blockedCtx)
	cancelBlocked()
	if !errors.Is(blockedErr, context.DeadlineExceeded) {
		testInstance.Fatalf("second Lock error = %v, want context deadline exceeded while the first holds the lock", blockedErr)
	}

	if unlockErr := unlockFirst(ctx); unlockErr != nil {
		testInstance.Fatalf("first unlock: %v", unlockErr)
	}
	firstReleased = true

	unlockSecond, secondErr := NewPGStore(pool).Lock(ctx)
	if secondErr != nil {
		testInstance.Fatalf("second Lock after release: %v", secondErr)
	}
	if unlockErr := unlockSecond(ctx); unlockErr != nil {
		testInstance.Fatalf("second unlock: %v", unlockErr)
	}
}

func TestLockTimesOutWithDescriptiveError(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()
	pool := openTestPool(ctx, testInstance, nil)

	unlockFirst, firstErr := NewPGStore(pool).Lock(ctx)
	if firstErr != nil {
		testInstance.Fatalf("first Lock: %v", firstErr)
	}
	defer func() { _ = unlockFirst(context.Background()) }()

	waiter := &PGStore{pool: pool, lockTimeout: shortLockWait}
	started := time.Now()
	_, waitErr := waiter.Lock(ctx)
	elapsed := time.Since(started)

	if !errors.Is(waitErr, errLockTimeout) {
		testInstance.Fatalf("Lock error = %v, want it to wrap errLockTimeout", waitErr)
	}
	if !strings.Contains(waitErr.Error(), shortLockWait.String()) {
		testInstance.Fatalf("Lock error = %q, want it to name the %s wait", waitErr, shortLockWait)
	}
	if elapsed < shortLockWait {
		testInstance.Fatalf("Lock returned after %s, want it to wait at least %s", elapsed, shortLockWait)
	}
}

func TestStatusWithoutBookkeepingTablesIsZero(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()
	adminPool := openTestPool(ctx, testInstance, nil)

	testCases := []struct {
		name   string
		schema string
		setup  string
	}{
		{name: "no tables", schema: "status_never_migrated"},
		{
			name:   "failures table missing",
			schema: "status_versions_only",
			setup:  "CREATE TABLE schema_migrations (version integer PRIMARY KEY)",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			dropSchema := "DROP SCHEMA IF EXISTS " + testCase.schema + " CASCADE"
			if _, dropErr := adminPool.Exec(ctx, dropSchema); dropErr != nil {
				subTest.Fatalf("drop stale schema: %v", dropErr)
			}
			if _, createErr := adminPool.Exec(ctx, "CREATE SCHEMA "+testCase.schema); createErr != nil {
				subTest.Fatalf("create schema: %v", createErr)
			}
			defer func() {
				cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), cleanupTimeout)
				defer cancelCleanup()
				_, _ = adminPool.Exec(cleanupCtx, dropSchema)
			}()

			isolatedPool := openTestPool(ctx, subTest, map[string]string{"search_path": testCase.schema})
			if testCase.setup != "" {
				if _, setupErr := isolatedPool.Exec(ctx, testCase.setup); setupErr != nil {
					subTest.Fatalf("setup: %v", setupErr)
				}
			}

			status, statusErr := NewPGStore(isolatedPool).Status(ctx)

			if statusErr != nil {
				subTest.Fatalf("Status error = %v, want nil", statusErr)
			}
			if status.SchemaVersion != 0 || status.LastFailure != nil {
				subTest.Fatalf("Status = %+v, want the zero Status", status)
			}
		})
	}
}

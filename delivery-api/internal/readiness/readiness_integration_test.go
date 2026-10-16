//go:build integration

package readiness

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	defaultTestDSN        = "postgres://delivery:delivery-local-dev@127.0.0.1:18300/delivery?sslmode=disable"
	integrationTestBudget = 30 * time.Second
)

// testDSN returns the local-only delivery-postgres DSN. make
// delivery-integration-test derives it from the worktree's DELIVERY_PORT.
func testDSN() string {
	if value := os.Getenv("NOTIFS_DELIVERY_TEST_DSN"); value != "" {
		return value
	}
	return defaultTestDSN
}

func openTestPool(ctx context.Context, testInstance *testing.T) *pgxpool.Pool {
	testInstance.Helper()
	pool, poolErr := pgxpool.New(ctx, testDSN())
	if poolErr != nil {
		testInstance.Fatalf("open pool: %v", poolErr)
	}
	testInstance.Cleanup(pool.Close)
	if pingErr := pool.Ping(ctx); pingErr != nil {
		testInstance.Fatalf("ping delivery-postgres (is the stack up and NOTIFS_DELIVERY_TEST_DSN's port right?): %v", pingErr)
	}
	return pool
}

func expectedVersion(testInstance *testing.T) int {
	testInstance.Helper()
	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load embedded migrations: %v", loadErr)
	}
	return migrate.ExpectedVersion(loaded)
}

func TestCheckerReportsCurrentSchemaFromLivePool(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()
	pool := openTestPool(ctx, testInstance)
	expected := expectedVersion(testInstance)

	readiness, readinessErr := NewChecker(pool, migrate.NewPGStore(pool), expected).Readiness(ctx)

	if readinessErr != nil {
		testInstance.Fatalf("Readiness error = %v, want nil", readinessErr)
	}
	if readiness.SchemaVersion != expected {
		testInstance.Fatalf("SchemaVersion = %d, want %d (the stack's delivery-migrate should have applied every embedded migration)",
			readiness.SchemaVersion, expected)
	}
	if readiness.ExpectedSchemaVersion != expected {
		testInstance.Fatalf("ExpectedSchemaVersion = %d, want %d", readiness.ExpectedSchemaVersion, expected)
	}
	if readiness.LastMigrationFailure != nil {
		testInstance.Fatalf("LastMigrationFailure = %+v, want nil", readiness.LastMigrationFailure)
	}
}

func TestCheckerWrapsPingErrorForClosedPool(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()
	pool := openTestPool(ctx, testInstance)
	checker := NewChecker(pool, migrate.NewPGStore(pool), expectedVersion(testInstance))
	pool.Close()

	_, readinessErr := checker.Readiness(ctx)

	if readinessErr == nil {
		testInstance.Fatal("Readiness error = nil, want a ping error for a closed pool")
	}
	if !strings.HasPrefix(readinessErr.Error(), "ping database: ") {
		testInstance.Fatalf("Readiness error = %q, want it wrapped with %q", readinessErr, "ping database")
	}
}

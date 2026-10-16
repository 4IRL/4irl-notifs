//go:build integration

// Package pgtest gives integration tests an isolated, fully migrated schema on
// the local delivery-postgres. It is test-only (integration build tag).
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	defaultDSN     = "postgres://delivery:delivery-local-dev@127.0.0.1:18300/delivery?sslmode=disable"
	schemaPrefix   = "pgtest_"
	schemaRandSize = 8 // bytes, rendered as 16 hex characters
	cleanupBudget  = 10 * time.Second
)

// DSN returns the local-only delivery-postgres DSN. make
// delivery-integration-test derives NOTIFS_DELIVERY_TEST_DSN from the
// worktree's DELIVERY_PORT.
func DSN() string {
	if value := os.Getenv("NOTIFS_DELIVERY_TEST_DSN"); value != "" {
		return value
	}
	return defaultDSN
}

// IsolatedPool creates a throwaway schema, applies every embedded migration
// into it, and returns a pool whose search_path is that schema. The pool is
// closed and the schema dropped when the test ends.
func IsolatedPool(ctx context.Context, testInstance *testing.T) *pgxpool.Pool {
	testInstance.Helper()

	adminConfig, parseErr := pgxpool.ParseConfig(DSN())
	if parseErr != nil {
		testInstance.Fatalf("parse test dsn: %v", parseErr)
	}
	adminPool, adminErr := pgxpool.NewWithConfig(ctx, adminConfig)
	if adminErr != nil {
		testInstance.Fatalf("open admin pool: %v", adminErr)
	}
	testInstance.Cleanup(adminPool.Close)
	if pingErr := adminPool.Ping(ctx); pingErr != nil {
		testInstance.Fatalf("ping delivery-postgres (is the stack up and NOTIFS_DELIVERY_TEST_DSN's port right?): %v", pingErr)
	}

	randomBytes := make([]byte, schemaRandSize)
	if _, readErr := rand.Read(randomBytes); readErr != nil {
		testInstance.Fatalf("generate schema name: %v", readErr)
	}
	schemaName := schemaPrefix + hex.EncodeToString(randomBytes)
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()

	if _, createErr := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); createErr != nil {
		testInstance.Fatalf("create schema %s: %v", schemaName, createErr)
	}

	isolatedConfig := adminConfig.Copy()
	isolatedConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	isolatedPool, isolatedErr := pgxpool.NewWithConfig(ctx, isolatedConfig)
	if isolatedErr != nil {
		dropSchema(adminPool, quotedSchema)
		testInstance.Fatalf("open isolated pool: %v", isolatedErr)
	}

	// Registered after the admin pool's Close, so (LIFO) it runs first: the
	// isolated pool closes, then the schema is dropped on the admin pool.
	testInstance.Cleanup(func() {
		isolatedPool.Close()
		dropSchema(adminPool, quotedSchema)
	})

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load embedded migrations: %v", loadErr)
	}
	discard := slog.New(slog.DiscardHandler)
	if _, runErr := migrate.Run(ctx, migrate.NewPGStore(isolatedPool), loaded, discard); runErr != nil {
		testInstance.Fatalf("migrate schema %s: %v", schemaName, runErr)
	}
	return isolatedPool
}

// dropSchema drops the schema on a fresh context so it still runs after the
// test's own context has expired.
func dropSchema(adminPool *pgxpool.Pool, quotedSchema string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	_, _ = adminPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
}

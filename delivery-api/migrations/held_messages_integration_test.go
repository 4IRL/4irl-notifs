//go:build integration

package migrations_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const heldMessagesVersion = 12

func TestHeldMessagesColumnsAreNullable(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	for _, column := range []string{"held_at", "held_cancelled_at", "released_at", "release_failed_at", "release_failed_reason"} {
		var nullable string
		err := pool.QueryRow(ctx,
			`SELECT is_nullable FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'messages' AND column_name = $1`,
			column).Scan(&nullable)
		if err != nil {
			testInstance.Errorf("read column %s: %v", column, err)
			continue
		}
		if nullable != "YES" {
			testInstance.Errorf("messages.%s is_nullable = %q, want YES", column, nullable)
		}
	}
}

func TestHeldMessagesCancelledConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	instant := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	cancelledOnlyID := mustInsertMessage(ctx, testInstance, pool, "held-cancelled-only")
	witnessOnlyID := mustInsertMessage(ctx, testInstance, pool, "held-witness-only")
	heldCancelID := mustInsertMessage(ctx, testInstance, pool, "held-cancel-both")
	heldID := mustInsertMessage(ctx, testInstance, pool, "held-plain")

	testInstance.Run("cancelled_at without deliver_at or held_cancelled_at rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET cancelled_at = $2 WHERE id = $1`, cancelledOnlyID, instant)
		expectSQLState(subTest, "set cancelled_at alone", err, sqlStateCheckViolation)
	})
	testInstance.Run("held_cancelled_at without cancelled_at rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET held_cancelled_at = $2 WHERE id = $1`, witnessOnlyID, instant)
		expectSQLState(subTest, "set held_cancelled_at alone", err, sqlStateCheckViolation)
	})
	testInstance.Run("cancelled_at with held_cancelled_at and no deliver_at accepted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET held_at = $2, cancelled_at = $2, held_cancelled_at = $2 WHERE id = $1`, heldCancelID, instant)
		expectSQLState(subTest, "set both cancel columns", err, "")
	})
	testInstance.Run("held_at alone accepted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET held_at = $2 WHERE id = $1`, heldID, instant)
		expectSQLState(subTest, "set held_at", err, "")
	})
}

func TestHeldMessagesReleaseFailedChecks(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	instant := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	messageID := mustInsertMessage(ctx, testInstance, pool, "release-failed")

	testInstance.Run("at without reason rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET release_failed_at = $2 WHERE id = $1`, messageID, instant)
		expectSQLState(subTest, "set release_failed_at alone", err, sqlStateCheckViolation)
	})
	testInstance.Run("reason without at rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET release_failed_reason = 'release_invalid' WHERE id = $1`, messageID)
		expectSQLState(subTest, "set release_failed_reason alone", err, sqlStateCheckViolation)
	})
	testInstance.Run("unknown reason rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET release_failed_at = $2, release_failed_reason = 'bogus' WHERE id = $1`, messageID, instant)
		expectSQLState(subTest, "set unknown reason", err, sqlStateCheckViolation)
	})
	testInstance.Run("pair with release_invalid accepted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET release_failed_at = $2, release_failed_reason = 'release_invalid' WHERE id = $1`, messageID, instant)
		expectSQLState(subTest, "set release-failed pair", err, "")
	})
}

func TestHeldMessagesIndexesArePartial(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	for indexName, wants := range map[string][]string{
		"messages_held_idx":     {"(held_at, id)", "held_at IS NOT NULL"},
		"messages_held_app_idx": {"(app_id)", "held_at IS NOT NULL"},
	} {
		var definition string
		err := pool.QueryRow(ctx,
			`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, indexName,
		).Scan(&definition)
		if err != nil {
			testInstance.Errorf("read %s: %v", indexName, err)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(definition, want) {
				testInstance.Errorf("index %s definition %q does not contain %q", indexName, definition, want)
			}
		}
	}
}

// TestHeldMessagesAppliesOverExistingRows loads rows in the 0011 schema, then
// applies 0012: the rebuilt cancelled_at CHECK must accept existing data.
func TestHeldMessagesAppliesOverExistingRows(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, heldMessagesVersion-1)

	messageID := mustInsertMessage(ctx, testInstance, pool, "existing-before-0012")
	if _, err := pool.Exec(ctx, `UPDATE messages SET deliver_at = now(), cancelled_at = now() WHERE id = $1`, messageID); err != nil {
		testInstance.Fatalf("cancel a scheduled message in the 0011 schema: %v", err)
	}

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	applied, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler))
	if runErr != nil {
		testInstance.Fatalf("apply 0012 over existing rows: %v", runErr)
	}
	if applied < 1 {
		testInstance.Fatalf("applied %d migrations over the 0011 schema, want at least 1 (0012)", applied)
	}

	var heldNull bool
	if err := pool.QueryRow(ctx, `SELECT held_at IS NULL AND held_cancelled_at IS NULL FROM messages WHERE id = $1`, messageID).Scan(&heldNull); err != nil {
		testInstance.Fatalf("read held columns: %v", err)
	}
	if !heldNull {
		testInstance.Error("an existing message got a non-NULL held column")
	}
}

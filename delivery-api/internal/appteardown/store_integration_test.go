//go:build integration

package appteardown

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

func execSQL(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	testInstance.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		testInstance.Fatalf("exec %q: %v", sql, err)
	}
}

func insertDestination(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, name, ownerApp string) int64 {
	testInstance.Helper()
	var id int64
	var err error
	if ownerApp == "" {
		err = pool.QueryRow(ctx,
			`INSERT INTO destinations (name, type, sealed_secrets) VALUES ($1, 'webhook', '\x00') RETURNING id`,
			name).Scan(&id)
	} else {
		err = pool.QueryRow(ctx,
			`INSERT INTO destinations (name, type, sealed_secrets, owner_kind, owner_app_id)
VALUES ($1, 'webhook', '\x00', 'app', $2) RETURNING id`, name, ownerApp).Scan(&id)
	}
	if err != nil {
		testInstance.Fatalf("insert destination %s: %v", name, err)
	}
	return id
}

func insertRoute(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, pattern, channel, ownerApp string) int64 {
	testInstance.Helper()
	var id int64
	var err error
	if ownerApp == "" {
		err = pool.QueryRow(ctx,
			`INSERT INTO routes (app_pattern, channel_pattern) VALUES ($1, $2) RETURNING id`, pattern, channel).Scan(&id)
	} else {
		err = pool.QueryRow(ctx,
			`INSERT INTO routes (app_pattern, channel_pattern, owner_kind, owner_app_id)
VALUES ($1, $2, 'app', $3) RETURNING id`, pattern, channel, ownerApp).Scan(&id)
	}
	if err != nil {
		testInstance.Fatalf("insert route: %v", err)
	}
	return id
}

func insertCheck(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, appID, name, ownerApp string) int64 {
	testInstance.Helper()
	var id int64
	var err error
	if ownerApp == "" {
		err = pool.QueryRow(ctx,
			`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at)
VALUES ($1, $2, 300, 300, now()) RETURNING id`, appID, name).Scan(&id)
	} else {
		err = pool.QueryRow(ctx,
			`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at, owner_kind, owner_app_id)
VALUES ($1, $2, 300, 300, now(), 'app', $3) RETURNING id`, appID, name, ownerApp).Scan(&id)
	}
	if err != nil {
		testInstance.Fatalf("insert check: %v", err)
	}
	return id
}

func count(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	testInstance.Helper()
	var total int
	if err := pool.QueryRow(ctx, query, args...).Scan(&total); err != nil {
		testInstance.Fatalf("count %q: %v", query, err)
	}
	return total
}

func TestDeleteOwnedObjectsRemovesOnlyTheAppsRows(testInstance *testing.T) {
	ctx := context.Background()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	store := NewPGStore(pool)

	// The app being removed: a destination, a route, a check, and links between them.
	appDestination := insertDestination(ctx, testInstance, pool, "urls4irl_hook", "urls4irl")
	appRoute := insertRoute(ctx, testInstance, pool, "urls4irl", "*", "urls4irl")
	appCheck := insertCheck(ctx, testInstance, pool, "urls4irl", "nightly", "urls4irl")
	execSQL(ctx, testInstance, pool, `INSERT INTO route_destinations VALUES ($1, $2)`, appRoute, appDestination)
	execSQL(ctx, testInstance, pool, `INSERT INTO heartbeat_check_destinations VALUES ($1, $2)`, appCheck, appDestination)

	// An operator route and check linked to the app's destination: they survive, minus the link.
	operatorRoute := insertRoute(ctx, testInstance, pool, "urls4irl", "alerts", "")
	operatorCheck := insertCheck(ctx, testInstance, pool, "urls4irl", "operator_check", "")
	execSQL(ctx, testInstance, pool, `INSERT INTO route_destinations VALUES ($1, $2)`, operatorRoute, appDestination)
	execSQL(ctx, testInstance, pool, `INSERT INTO heartbeat_check_destinations VALUES ($1, $2)`, operatorCheck, appDestination)

	// Another app's and the operator's objects, untouched.
	otherDestination := insertDestination(ctx, testInstance, pool, "chores4irl_hook", "chores4irl")
	otherRoute := insertRoute(ctx, testInstance, pool, "chores4irl", "*", "chores4irl")
	otherCheck := insertCheck(ctx, testInstance, pool, "chores4irl", "nightly", "chores4irl")
	operatorDestination := insertDestination(ctx, testInstance, pool, "ops_hook", "")
	execSQL(ctx, testInstance, pool, `INSERT INTO route_destinations VALUES ($1, $2)`, otherRoute, otherDestination)
	execSQL(ctx, testInstance, pool, `INSERT INTO route_destinations VALUES ($1, $2)`, operatorRoute, operatorDestination)

	// A delivery to the app's destination: the log row survives with destination_id NULL.
	var messageID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO messages (app_id, channel, idempotency_key, payload) VALUES ('urls4irl', 'alerts', 'k1', '{}') RETURNING id`,
	).Scan(&messageID); err != nil {
		testInstance.Fatalf("insert message: %v", err)
	}
	execSQL(ctx, testInstance, pool,
		`INSERT INTO message_deliveries (message_id, destination_id, destination_name, destination_type)
VALUES ($1, $2, 'urls4irl_hook', 'webhook')`, messageID, appDestination)

	deleted, err := store.DeleteOwnedObjects(ctx, "urls4irl")
	if err != nil {
		testInstance.Fatalf("DeleteOwnedObjects: %v", err)
	}
	// The operator check lost its only link (orphaned); the operator route keeps its other link.
	if deleted != (Deleted{Checks: 1, Routes: 1, Destinations: 1, OrphanedRoutes: 0, OrphanedChecks: 1}) {
		testInstance.Fatalf("deleted = %+v, want 1/1/1 with 0 orphaned routes and 1 orphaned check", deleted)
	}

	if got := count(ctx, testInstance, pool, `SELECT count(*) FROM destinations WHERE owner_app_id = 'urls4irl'`); got != 0 {
		testInstance.Fatalf("app destinations left = %d", got)
	}
	if got := count(ctx, testInstance, pool, `SELECT count(*) FROM routes WHERE id = $1`, operatorRoute); got != 1 {
		testInstance.Fatalf("operator route survived = %d, want 1", got)
	}
	if got := count(ctx, testInstance, pool, `SELECT count(*) FROM heartbeat_checks WHERE id = $1`, operatorCheck); got != 1 {
		testInstance.Fatalf("operator check survived = %d, want 1", got)
	}
	if got := count(ctx, testInstance, pool,
		`SELECT count(*) FROM route_destinations WHERE route_id = $1`, operatorRoute); got != 1 {
		testInstance.Fatalf("operator route links = %d, want only its link to the operator destination", got)
	}
	if got := count(ctx, testInstance, pool,
		`SELECT count(*) FROM heartbeat_check_destinations WHERE check_id = $1`, operatorCheck); got != 0 {
		testInstance.Fatalf("operator check links = %d, want the app-destination link removed", got)
	}
	survivors := []struct {
		table string
		id    int64
	}{
		{"destinations", otherDestination}, {"routes", otherRoute},
		{"heartbeat_checks", otherCheck}, {"destinations", operatorDestination},
	}
	for _, survivor := range survivors {
		if got := count(ctx, testInstance, pool, `SELECT count(*) FROM `+survivor.table+` WHERE id = $1`, survivor.id); got != 1 {
			testInstance.Fatalf("%s id %d should survive, count = %d", survivor.table, survivor.id, got)
		}
	}
	if got := count(ctx, testInstance, pool,
		`SELECT count(*) FROM message_deliveries WHERE destination_id IS NULL AND destination_name = 'urls4irl_hook'`); got != 1 {
		testInstance.Fatalf("delivery rows with NULL destination_id = %d, want 1", got)
	}

	// Repeating is a no-op, not an error.
	again, repeatErr := store.DeleteOwnedObjects(ctx, "urls4irl")
	if repeatErr != nil || again != (Deleted{}) {
		testInstance.Fatalf("repeat = %+v, %v, want zero counts", again, repeatErr)
	}
}

func TestDeleteOwnedObjectsRejectsInvalidAppIDs(testInstance *testing.T) {
	ctx := context.Background()
	store := NewPGStore(pgtest.IsolatedPool(ctx, testInstance))

	for _, appID := range []string{"", "Bad-App", "_platform", "x y"} {
		if _, err := store.DeleteOwnedObjects(ctx, appID); err == nil {
			testInstance.Fatalf("app %q: want an error", appID)
		}
	}
}

//go:build integration

package migrations_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	ownershipVersion = 7

	insertOwnedDestinationSQL = `INSERT INTO destinations (name, type, owner_kind, owner_app_id, sealed_secrets)
VALUES ($1, 'ntfy', $2, $3, $4)`
	insertOwnedRouteSQL = `INSERT INTO routes (app_pattern, channel_pattern, owner_kind, owner_app_id)
VALUES ($1, '*', $2, $3)`
	insertOwnedCheckSQL = `INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at, owner_kind, owner_app_id)
VALUES ($1, 'nightly', 3600, 3600, now(), $2, $3)`
)

// TestOwnershipKeepsExistingRowsOperatorOwned loads rows in the 0006 schema and
// applies 0007, so every pre-existing destination, route and check reads back as
// operator-owned with no owner app.
func TestOwnershipKeepsExistingRowsOperatorOwned(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, ownershipVersion-1)

	if _, err := pool.Exec(ctx, insertDestinationSQL, "ops_ntfy", "ntfy", placeholderSealedSecrets); err != nil {
		testInstance.Fatalf("insert destination in the 0006 schema: %v", err)
	}
	if _, err := pool.Exec(ctx, insertRouteSQL, "*", "*"); err != nil {
		testInstance.Fatalf("insert route in the 0006 schema: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at)
VALUES ('urls4irl', 'nightly', 3600, 3600, now())`); err != nil {
		testInstance.Fatalf("insert check in the 0006 schema: %v", err)
	}

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	if _, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler)); runErr != nil {
		testInstance.Fatalf("apply 0007 over existing rows: %v", runErr)
	}

	for _, table := range []string{"destinations", "routes", "heartbeat_checks"} {
		var rows, operatorOwned int
		if err := pool.QueryRow(ctx,
			"SELECT count(*), count(*) FILTER (WHERE owner_kind = 'operator' AND owner_app_id IS NULL) FROM "+table,
		).Scan(&rows, &operatorOwned); err != nil {
			testInstance.Fatalf("count %s owners: %v", table, err)
		}
		if rows != 1 || operatorOwned != 1 {
			testInstance.Fatalf("%s: %d rows, %d operator-owned with no owner app; want 1 and 1", table, rows, operatorOwned)
		}
	}
}

func TestDestinationOwnerConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	// Order matters: no case inserts a name another valid case uses.
	testCases := []struct {
		label     string
		name      string
		ownerKind string
		ownerApp  *string
		wantState string
	}{
		{label: "operator with no owner inserts", name: "ops_ntfy", ownerKind: "operator"},
		{label: "app owner with prefixed name inserts", name: "urls4irl_alerts", ownerKind: "app", ownerApp: stringPointer("urls4irl")},
		{label: "app owner whose name is only the prefix inserts", name: "urls4irl_", ownerKind: "app", ownerApp: stringPointer("urls4irl")},
		{label: "app kind with a null owner rejected", name: "app_null_owner", ownerKind: "app", wantState: sqlStateCheckViolation},
		{label: "operator kind with an owner rejected", name: "urls4irl_op", ownerKind: "operator", ownerApp: stringPointer("urls4irl"), wantState: sqlStateCheckViolation},
		{label: "app owner with an unprefixed name rejected", name: "ops_alerts", ownerKind: "app", ownerApp: stringPointer("urls4irl"), wantState: sqlStateCheckViolation},
		{label: "owner id that is only a prefix of the name rejected", name: "urls4irlalerts", ownerKind: "app", ownerApp: stringPointer("urls4irl"), wantState: sqlStateCheckViolation},
		{label: "owner id with an underscore is not a LIKE wildcard", name: "axb_alerts", ownerKind: "app", ownerApp: stringPointer("a_b"), wantState: sqlStateCheckViolation},
		{label: "owner id with an underscore matches its literal prefix", name: "a_b_alerts", ownerKind: "app", ownerApp: stringPointer("a_b")},
		{label: "uppercase owner id rejected", name: "urls4irl_up", ownerKind: "app", ownerApp: stringPointer("Urls4irl"), wantState: sqlStateCheckViolation},
		{label: "empty owner id rejected", name: "empty_owner", ownerKind: "app", ownerApp: stringPointer(""), wantState: sqlStateCheckViolation},
		{label: "unknown owner kind rejected", name: "person_owned", ownerKind: "person", wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertOwnedDestinationSQL,
				testCase.name, testCase.ownerKind, testCase.ownerApp, placeholderSealedSecrets)
			expectSQLState(subTest, "insert destination "+testCase.name, err, testCase.wantState)
		})
	}
}

func TestRouteOwnerConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	testCases := []struct {
		label      string
		appPattern string
		ownerKind  string
		ownerApp   *string
		wantState  string
	}{
		{label: "operator wildcard route inserts", appPattern: "*", ownerKind: "operator"},
		{label: "operator route for one app inserts", appPattern: "other", ownerKind: "operator"},
		{label: "app route matching its owner inserts", appPattern: "urls4irl", ownerKind: "app", ownerApp: stringPointer("urls4irl")},
		{label: "app route with another app's pattern rejected", appPattern: "chores4irl", ownerKind: "app", ownerApp: stringPointer("urls4irl"), wantState: sqlStateCheckViolation},
		{label: "app route with a wildcard pattern rejected", appPattern: "*", ownerKind: "app", ownerApp: stringPointer("urls4irl"), wantState: sqlStateCheckViolation},
		{label: "app kind with a null owner rejected", appPattern: "nullowner", ownerKind: "app", wantState: sqlStateCheckViolation},
		{label: "operator kind with an owner rejected", appPattern: "urls4irl", ownerKind: "operator", ownerApp: stringPointer("urls4irl"), wantState: sqlStateCheckViolation},
		{label: "unknown owner kind rejected", appPattern: "person", ownerKind: "person", wantState: sqlStateCheckViolation},
		{label: "uppercase owner id rejected", appPattern: "Upper", ownerKind: "app", ownerApp: stringPointer("Upper"), wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertOwnedRouteSQL, testCase.appPattern, testCase.ownerKind, testCase.ownerApp)
			expectSQLState(subTest, "insert route "+testCase.appPattern, err, testCase.wantState)
		})
	}
}

func TestHeartbeatCheckOwnerConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	// Every case uses the same check name, so each needs its own app_id.
	testCases := []struct {
		label     string
		appID     string
		ownerKind string
		ownerApp  *string
		wantState string
	}{
		{label: "operator check inserts", appID: "ops", ownerKind: "operator"},
		{label: "app check matching its owner inserts", appID: "urls4irl", ownerKind: "app", ownerApp: stringPointer("urls4irl")},
		{label: "app check pinging as another app rejected", appID: "chores4irl", ownerKind: "app", ownerApp: stringPointer("urls4irl"), wantState: sqlStateCheckViolation},
		{label: "app kind with a null owner rejected", appID: "nullowner", ownerKind: "app", wantState: sqlStateCheckViolation},
		{label: "operator kind with an owner rejected", appID: "opowner", ownerKind: "operator", ownerApp: stringPointer("opowner"), wantState: sqlStateCheckViolation},
		{label: "unknown owner kind rejected", appID: "person", ownerKind: "person", wantState: sqlStateCheckViolation},
		{label: "uppercase owner id rejected", appID: "Upper", ownerKind: "app", ownerApp: stringPointer("Upper"), wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertOwnedCheckSQL, testCase.appID, testCase.ownerKind, testCase.ownerApp)
			expectSQLState(subTest, "insert check "+testCase.appID, err, testCase.wantState)
		})
	}
}

func stringPointer(value string) *string { return &value }

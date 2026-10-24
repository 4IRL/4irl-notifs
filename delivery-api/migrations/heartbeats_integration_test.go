//go:build integration

package migrations_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	heartbeatsVersion = 6

	insertCheckSQL = `INSERT INTO heartbeat_checks
(app_id, name, period_seconds, grace_seconds, state, anchor_at, last_ping_at, last_ping_kind, down_since, down_reason, pending_alert)
VALUES ($1, $2, $3, $4, $5, now(), $6, $7, $8, $9, $10)`
)

// TestHeartbeatsApplyOverExistingSchema loads the 0005 schema and applies 0006 and
// every later migration on top of it; the highest embedded version is what
// /readyz reports as expected_schema_version.
func TestHeartbeatsApplyOverExistingSchema(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, heartbeatsVersion-1)

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	expected := migrate.ExpectedVersion(loaded)
	if expected < heartbeatsVersion {
		testInstance.Fatalf("ExpectedVersion = %d, want at least %d", expected, heartbeatsVersion)
	}
	applied, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler))
	if runErr != nil {
		testInstance.Fatalf("apply 0006 and later over the 0005 schema: %v", runErr)
	}
	// Every migration from 0006 up to the newest is still pending on the 0005 schema.
	if want := expected - (heartbeatsVersion - 1); applied != want {
		testInstance.Fatalf("applied %d migrations over the 0005 schema, want %d (0006 through %04d)", applied, want, expected)
	}
}

func TestHeartbeatCheckConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	pingKind := "success"
	downReason := "missed"
	pending := "down"
	someTime := "2026-10-18T12:00:00Z"

	type constraintCase struct {
		label            string
		appID, checkName string
		period, grace    int
		state            string
		lastPingAt       *string
		lastPingKind     *string
		downSince        *string
		downReason       *string
		pendingAlert     *string
		wantState        string
	}
	testCases := []constraintCase{
		{label: "minimal new check accepted", appID: "billing", checkName: "ok_new", period: 60, grace: 60, state: "new"},
		{label: "maximum bounds accepted", appID: "billing", checkName: "ok_max", period: 2_678_400, grace: 604_800, state: "new"},
		{label: "up with a success ping accepted", appID: "billing", checkName: "ok_up", period: 60, grace: 60, state: "up", lastPingAt: &someTime, lastPingKind: &pingKind},
		{label: "down with since and reason accepted", appID: "billing", checkName: "ok_down", period: 60, grace: 60, state: "down", downSince: &someTime, downReason: &downReason, pendingAlert: &pending},
		{label: "app_id uppercase rejected", appID: "Billing", checkName: "bad_app", period: 60, grace: 60, state: "new", wantState: sqlStateCheckViolation},
		{label: "name with hyphen rejected", appID: "billing", checkName: "bad-name", period: 60, grace: 60, state: "new", wantState: sqlStateCheckViolation},
		{label: "name too long rejected", appID: "billing", checkName: strings.Repeat("a", 64), period: 60, grace: 60, state: "new", wantState: sqlStateCheckViolation},
		{label: "period below minimum rejected", appID: "billing", checkName: "low_period", period: 59, grace: 60, state: "new", wantState: sqlStateCheckViolation},
		{label: "period above maximum rejected", appID: "billing", checkName: "high_period", period: 2_678_401, grace: 60, state: "new", wantState: sqlStateCheckViolation},
		{label: "grace below minimum rejected", appID: "billing", checkName: "low_grace", period: 60, grace: 59, state: "new", wantState: sqlStateCheckViolation},
		{label: "grace above maximum rejected", appID: "billing", checkName: "high_grace", period: 60, grace: 604_801, state: "new", wantState: sqlStateCheckViolation},
		{label: "unknown state rejected", appID: "billing", checkName: "bad_state", period: 60, grace: 60, state: "late", wantState: sqlStateCheckViolation},
		{label: "unknown ping kind rejected", appID: "billing", checkName: "bad_kind", period: 60, grace: 60, state: "up", lastPingAt: &someTime, lastPingKind: strPtr("start"), wantState: sqlStateCheckViolation},
		{label: "ping time without kind rejected", appID: "billing", checkName: "half_ping", period: 60, grace: 60, state: "up", lastPingAt: &someTime, wantState: sqlStateCheckViolation},
		{label: "unknown down reason rejected", appID: "billing", checkName: "bad_reason", period: 60, grace: 60, state: "down", downSince: &someTime, downReason: strPtr("boom"), wantState: sqlStateCheckViolation},
		{label: "unknown pending alert rejected", appID: "billing", checkName: "bad_pending", period: 60, grace: 60, state: "new", pendingAlert: strPtr("late"), wantState: sqlStateCheckViolation},
		{label: "down without since rejected", appID: "billing", checkName: "down_no_since", period: 60, grace: 60, state: "down", downReason: &downReason, wantState: sqlStateCheckViolation},
		{label: "duplicate app and name rejected", appID: "billing", checkName: "ok_new", period: 60, grace: 60, state: "new", wantState: sqlStateUniqueViolation},
		{label: "same name under another app accepted", appID: "other", checkName: "ok_new", period: 60, grace: 60, state: "new"},
	}
	for _, reason := range heartbeat.KnownDownReasons() {
		testCases = append(testCases, constraintCase{
			label: "down with reason " + reason + " accepted", appID: "billing", checkName: "ok_down_" + reason,
			period: 60, grace: 60, state: "down", downSince: &someTime, downReason: strPtr(reason),
		})
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertCheckSQL, testCase.appID, testCase.checkName, testCase.period, testCase.grace,
				testCase.state, testCase.lastPingAt, testCase.lastPingKind, testCase.downSince, testCase.downReason,
				testCase.pendingAlert)
			expectSQLState(subTest, "insert heartbeat check "+testCase.label, err, testCase.wantState)
		})
	}
}

func TestHeartbeatCheckDestinationLinks(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var destinationID, checkID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO destinations (name, type, sealed_secrets) VALUES ('ops', 'ntfy', '\x00') RETURNING id`,
	).Scan(&destinationID); err != nil {
		testInstance.Fatalf("insert destination: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at)
VALUES ('billing', 'nightly', 60, 60, now()) RETURNING id`,
	).Scan(&checkID); err != nil {
		testInstance.Fatalf("insert check: %v", err)
	}

	testInstance.Run("link accepted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO heartbeat_check_destinations (check_id, destination_id) VALUES ($1, $2)`, checkID, destinationID)
		expectSQLState(subTest, "link destination", err, "")
	})
	testInstance.Run("duplicate link rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO heartbeat_check_destinations (check_id, destination_id) VALUES ($1, $2)`, checkID, destinationID)
		expectSQLState(subTest, "duplicate link", err, sqlStateUniqueViolation)
	})
	testInstance.Run("link to an absent destination rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			`INSERT INTO heartbeat_check_destinations (check_id, destination_id) VALUES ($1, $2)`, checkID, destinationID+1000)
		expectSQLState(subTest, "link absent destination", err, sqlStateForeignKeyViolation)
	})
	testInstance.Run("deleting a linked destination is restricted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `DELETE FROM destinations WHERE id = $1`, destinationID)
		expectSQLState(subTest, "delete linked destination", err, sqlStateForeignKeyViolation)
	})
	testInstance.Run("deleting the check cascades its links", func(subTest *testing.T) {
		if _, err := pool.Exec(ctx, `DELETE FROM heartbeat_checks WHERE id = $1`, checkID); err != nil {
			subTest.Fatalf("delete check: %v", err)
		}
		var links int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM heartbeat_check_destinations WHERE check_id = $1`, checkID).Scan(&links); err != nil {
			subTest.Fatalf("count links: %v", err)
		}
		if links != 0 {
			subTest.Errorf("links after deleting the check = %d, want 0", links)
		}
	})
}

func TestHeartbeatIndexesArePartial(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	wants := map[string][]string{
		"heartbeat_checks_tick_idx":    {"(anchor_at)", "state <> 'down'", "NOT paused"},
		"heartbeat_checks_pending_idx": {"pending_alert IS NOT NULL"},
	}
	for indexName, fragments := range wants {
		var definition string
		err := pool.QueryRow(ctx,
			`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`, indexName,
		).Scan(&definition)
		if err != nil {
			testInstance.Fatalf("read %s definition: %v", indexName, err)
		}
		for _, want := range fragments {
			if !strings.Contains(definition, want) {
				testInstance.Errorf("index %s definition %q does not contain %q", indexName, definition, want)
			}
		}
	}
}

func strPtr(value string) *string { return &value }

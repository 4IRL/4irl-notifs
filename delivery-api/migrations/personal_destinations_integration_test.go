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
	personalDestinationsVersion = 13

	insertPersonalDestinationSQL = `INSERT INTO personal_destinations
(person_hash, registered_by_app_id, label, type, sealed_secrets, enabled, disabled_by)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`
	validPersonHash = "abcdefghijklmnop"
)

// TestPersonalDestinationsKeepExistingDeadDeliveries loads a dead delivery in
// the 0012 schema and applies 0013, so the recreated dead_reason CHECK still
// accepts the pre-existing row.
func TestPersonalDestinationsKeepExistingDeadDeliveries(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, personalDestinationsVersion-1)

	messageID := mustInsertMessage(ctx, testInstance, pool, "pre-personal")
	deadReason := "gone"
	var deliveryID int64
	if err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "old_dead", stateDead, &deadReason).Scan(&deliveryID); err != nil {
		testInstance.Fatalf("insert dead delivery in the 0012 schema: %v", err)
	}

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	if _, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler)); runErr != nil {
		testInstance.Fatalf("apply 0013 over existing rows: %v", runErr)
	}

	var reason string
	if err := pool.QueryRow(ctx, "SELECT dead_reason FROM message_deliveries WHERE id = $1", deliveryID).Scan(&reason); err != nil {
		testInstance.Fatalf("read dead delivery: %v", err)
	}
	if reason != deadReason {
		testInstance.Fatalf("dead_reason = %q, want %q", reason, deadReason)
	}
}

func TestPersonalDestinationConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	disabledBy := func(value string) *string { return &value }
	testCases := []struct {
		label      string
		personHash string
		appID      string
		personal   string
		kind       string
		enabled    bool
		disabledBy *string
		wantState  string
	}{
		{label: "valid discord inserts", personHash: validPersonHash, appID: "urls4irl", personal: "My Discord", kind: "discord", enabled: true},
		{label: "valid webhook disabled by operator inserts", personHash: validPersonHash, appID: "urls4irl", personal: "Hook", kind: "webhook", disabledBy: disabledBy("operator")},
		{label: "bad hash rejected", personHash: "short", appID: "urls4irl", personal: "x", kind: "discord", enabled: true, wantState: sqlStateCheckViolation},
		{label: "uppercase hash rejected", personHash: "ABCDEFGHIJKLMNOP", appID: "urls4irl", personal: "x", kind: "discord", enabled: true, wantState: sqlStateCheckViolation},
		{label: "bad app id rejected", personHash: validPersonHash, appID: "Bad-App", personal: "x", kind: "discord", enabled: true, wantState: sqlStateCheckViolation},
		{label: "empty label rejected", personHash: validPersonHash, appID: "urls4irl", personal: "", kind: "discord", enabled: true, wantState: sqlStateCheckViolation},
		{label: "65-char label rejected", personHash: validPersonHash, appID: "urls4irl", personal: repeatByte('a', 65), kind: "discord", enabled: true, wantState: sqlStateCheckViolation},
		{label: "64-char label inserts", personHash: validPersonHash, appID: "urls4irl", personal: repeatByte('a', 64), kind: "discord", enabled: true},
		{label: "ntfy type rejected", personHash: validPersonHash, appID: "urls4irl", personal: "x", kind: "ntfy", enabled: true, wantState: sqlStateCheckViolation},
		{label: "disabled without disabled_by rejected", personHash: validPersonHash, appID: "urls4irl", personal: "x", kind: "discord", enabled: false, wantState: sqlStateCheckViolation},
		{label: "enabled with disabled_by rejected", personHash: validPersonHash, appID: "urls4irl", personal: "x", kind: "discord", enabled: true, disabledBy: disabledBy("app"), wantState: sqlStateCheckViolation},
		{label: "unknown disabled_by rejected", personHash: validPersonHash, appID: "urls4irl", personal: "x", kind: "discord", enabled: false, disabledBy: disabledBy("nobody"), wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			var personalID int64
			err := pool.QueryRow(ctx, insertPersonalDestinationSQL,
				testCase.personHash, testCase.appID, testCase.personal, testCase.kind,
				placeholderSealedSecrets, testCase.enabled, testCase.disabledBy).Scan(&personalID)
			expectSQLState(subTest, "insert personal destination", err, testCase.wantState)
		})
	}
}

func TestPersonalOptInConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var personalID int64
	if err := pool.QueryRow(ctx, insertPersonalDestinationSQL,
		validPersonHash, "urls4irl", "Mine", "discord", placeholderSealedSecrets, true, nil).Scan(&personalID); err != nil {
		testInstance.Fatalf("insert personal destination: %v", err)
	}
	const insertOptInSQL = "INSERT INTO personal_destination_opt_ins (app_id, personal_destination_id) VALUES ($1, $2)"

	testInstance.Run("opt-in inserts", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, insertOptInSQL, "urls4irl", personalID)
		expectSQLState(subTest, "insert opt-in", err, "")
	})
	testInstance.Run("duplicate opt-in rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, insertOptInSQL, "urls4irl", personalID)
		expectSQLState(subTest, "insert duplicate opt-in", err, sqlStateUniqueViolation)
	})
	testInstance.Run("bad app id rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, insertOptInSQL, "Bad-App", personalID)
		expectSQLState(subTest, "insert opt-in with bad app id", err, sqlStateCheckViolation)
	})
	testInstance.Run("unknown destination rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, insertOptInSQL, "urls4irl", personalID+1000)
		expectSQLState(subTest, "insert opt-in for unknown destination", err, sqlStateForeignKeyViolation)
	})
	testInstance.Run("destination delete cascades to opt-ins", func(subTest *testing.T) {
		if _, err := pool.Exec(ctx, "DELETE FROM personal_destinations WHERE id = $1", personalID); err != nil {
			subTest.Fatalf("delete personal destination: %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM personal_destination_opt_ins").Scan(&remaining); err != nil {
			subTest.Fatalf("count opt-ins: %v", err)
		}
		if remaining != 0 {
			subTest.Fatalf("%d opt-ins remain after the destination was deleted, want 0", remaining)
		}
	})
}

func TestOptedOutIsAValidDeadReason(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	messageID := mustInsertMessage(ctx, testInstance, pool, "opted-out")

	testCases := []struct {
		label     string
		name      string
		reason    string
		wantState string
	}{
		{label: "opted_out accepted", name: "@personal_1", reason: "opted_out"},
		{label: "no_publisher still accepted", name: "@recipient", reason: "no_publisher"},
		{label: "terminal_status still accepted", name: "d_terminal", reason: "terminal_status"},
		{label: "gone still accepted", name: "d_gone", reason: "gone"},
		{label: "budget_exhausted still accepted", name: "d_budget", reason: "budget_exhausted"},
		{label: "destination_disabled still accepted", name: "d_disabled", reason: "destination_disabled"},
		{label: "destination_deleted still accepted", name: "d_deleted", reason: "destination_deleted"},
		{label: "blocked_address still accepted", name: "d_blocked", reason: "blocked_address"},
		{label: "unsupported_type still accepted", name: "d_unsupported", reason: "unsupported_type"},
		{label: "unknown reason still rejected", name: "d_unknown", reason: "mystery", wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			var deliveryID int64
			reason := testCase.reason
			err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, testCase.name, stateDead, &reason).Scan(&deliveryID)
			expectSQLState(subTest, "insert dead delivery", err, testCase.wantState)
		})
	}
}

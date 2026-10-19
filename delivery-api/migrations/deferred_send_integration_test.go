//go:build integration

package migrations_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	stateScheduled = "scheduled"
	stateCancelled = "cancelled"

	deferredSendVersion = 5
)

func TestDeferredSendDeliveryStates(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	messageID := mustInsertMessage(ctx, testInstance, pool, "deferred-send-states")
	deadReason := "gone"

	testCases := []struct {
		name       string
		state      string
		deadReason *string
		wantState  string
	}{
		{name: "scheduled accepted", state: stateScheduled},
		{name: "cancelled accepted", state: stateCancelled},
		{name: "pending still accepted", state: statePending},
		{name: "delivered still accepted", state: stateDelivered},
		{name: "dead with reason still accepted", state: stateDead, deadReason: &deadReason},
		{name: "dead without reason still rejected", state: stateDead, wantState: sqlStateCheckViolation},
		{name: "scheduled with a dead reason rejected", state: stateScheduled, deadReason: &deadReason, wantState: sqlStateCheckViolation},
		{name: "cancelled with a dead reason rejected", state: stateCancelled, deadReason: &deadReason, wantState: sqlStateCheckViolation},
		{name: "unknown state rejected", state: "released", wantState: sqlStateCheckViolation},
	}
	for index, testCase := range testCases {
		destinationName := fmt.Sprintf("dest_%d", index)
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var deliveryID int64
			err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, destinationName, testCase.state, testCase.deadReason).Scan(&deliveryID)
			expectSQLState(subTest, "insert delivery "+testCase.name, err, testCase.wantState)
		})
	}
}

func TestDeferredSendCancelledAtConstraint(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	unscheduledID := mustInsertMessage(ctx, testInstance, pool, "cancelled-without-deliver-at")
	scheduledID := mustInsertMessage(ctx, testInstance, pool, "cancelled-with-deliver-at")
	cancelledAt := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

	testInstance.Run("cancelled_at is NULL by default", func(subTest *testing.T) {
		var isNull bool
		if err := pool.QueryRow(ctx, `SELECT cancelled_at IS NULL FROM messages WHERE id = $1`, unscheduledID).Scan(&isNull); err != nil {
			subTest.Fatalf("read cancelled_at: %v", err)
		}
		if !isNull {
			subTest.Error("cancelled_at is not NULL on a fresh message")
		}
	})
	testInstance.Run("cancelled_at without deliver_at rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET cancelled_at = $2 WHERE id = $1`, unscheduledID, cancelledAt)
		expectSQLState(subTest, "set cancelled_at without deliver_at", err, sqlStateCheckViolation)
	})
	testInstance.Run("cancelled_at with deliver_at accepted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET deliver_at = $2, cancelled_at = $2 WHERE id = $1`, scheduledID, cancelledAt)
		expectSQLState(subTest, "set cancelled_at with deliver_at", err, "")
	})
	testInstance.Run("deliver_at alone accepted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE messages SET deliver_at = $2 WHERE id = $1`, unscheduledID, cancelledAt)
		expectSQLState(subTest, "set deliver_at", err, "")
	})
}

// TestDeferredSendKeepsExistingRows loads rows in the 0004 schema and then
// applies 0005, so a state or dead_reason CHECK that the new constraint rebuild
// would reject on existing data fails the migration here.
func TestDeferredSendKeepsExistingRows(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, deferredSendVersion-1)

	messageID := mustInsertMessage(ctx, testInstance, pool, "existing-rows")
	deadReason := "no_publisher"
	existing := []struct {
		name       string
		state      string
		deadReason *string
	}{
		{name: "old_pending", state: statePending},
		{name: "old_delivered", state: stateDelivered},
		{name: "old_dead", state: stateDead, deadReason: &deadReason},
	}
	for _, row := range existing {
		var deliveryID int64
		if err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, row.name, row.state, row.deadReason).Scan(&deliveryID); err != nil {
			testInstance.Fatalf("insert %s in the 0004 schema: %v", row.name, err)
		}
	}

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	applied, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler))
	if runErr != nil {
		testInstance.Fatalf("apply the remaining migrations over existing rows: %v", runErr)
	}
	if applied < 1 {
		testInstance.Fatalf("applied %d migrations over the 0004 schema, want at least 1 (0005)", applied)
	}

	var deliveries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_deliveries WHERE message_id = $1`, messageID).Scan(&deliveries); err != nil {
		testInstance.Fatalf("count deliveries: %v", err)
	}
	if deliveries != len(existing) {
		testInstance.Errorf("deliveries after 0005 = %d, want %d", deliveries, len(existing))
	}
	var cancelledNull bool
	if err := pool.QueryRow(ctx, `SELECT cancelled_at IS NULL FROM messages WHERE id = $1`, messageID).Scan(&cancelledNull); err != nil {
		testInstance.Fatalf("read cancelled_at: %v", err)
	}
	if !cancelledNull {
		testInstance.Error("an existing message got a non-NULL cancelled_at")
	}

	var scheduledID int64
	if err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "new_scheduled", stateScheduled, nil).Scan(&scheduledID); err != nil {
		testInstance.Errorf("insert a scheduled delivery after 0005: %v", err)
	}
}

func TestDeferredSendScheduledIndexIsPartial(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var definition string
	err := pool.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'message_deliveries_scheduled_idx'`,
	).Scan(&definition)
	if err != nil {
		testInstance.Fatalf("read scheduled index definition: %v", err)
	}
	for _, want := range []string{"(next_attempt_at, message_id)", "state = 'scheduled'"} {
		if !strings.Contains(definition, want) {
			testInstance.Errorf("index definition %q does not contain %q", definition, want)
		}
	}
}

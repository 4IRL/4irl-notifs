//go:build integration

package migrations_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	recipientViaVersion = 15

	insertRoutedMessageSQL = "INSERT INTO messages (app_id, channel, idempotency_key, to_recipient, to_recipient_via, to_personal_destination_ids, payload) " +
		"VALUES ('urls4irl', 'alerts', $1, $2, $3, $4, '{}'::jsonb) RETURNING id"
)

func TestRecipientViaColumnsAndConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	text := func(value string) *string { return &value }
	testCases := []struct {
		label     string
		recipient *string
		via       *string
		ids       []int64
		wantState string
	}{
		{label: "recipient without routing inserts", recipient: text("abcdefghijklmnop")},
		{label: "recipient ntfy without ids inserts", recipient: text("abcdefghijklmnop"), via: text("ntfy")},
		{label: "recipient personal with ids inserts", recipient: text("abcdefghijklmnop"), via: text("personal"), ids: []int64{12, 15}},
		{label: "recipient both with ids inserts", recipient: text("abcdefghijklmnop"), via: text("both"), ids: []int64{12}},
		{label: "no recipient and no routing inserts"},
		{label: "bad via rejected", recipient: text("abcdefghijklmnop"), via: text("sms"), wantState: sqlStateCheckViolation},
		{label: "empty via rejected", recipient: text("abcdefghijklmnop"), via: text(""), wantState: sqlStateCheckViolation},
		{label: "via without recipient rejected", via: text("ntfy"), wantState: sqlStateCheckViolation},
		{label: "ids without recipient rejected", via: nil, ids: []int64{1}, wantState: sqlStateCheckViolation},
		{label: "personal via without recipient rejected", via: text("personal"), ids: []int64{1}, wantState: sqlStateCheckViolation},
		{label: "ids with ntfy rejected", recipient: text("abcdefghijklmnop"), via: text("ntfy"), ids: []int64{1}, wantState: sqlStateCheckViolation},
		{label: "ids with unset via rejected", recipient: text("abcdefghijklmnop"), ids: []int64{1}, wantState: sqlStateCheckViolation},
		{label: "personal without ids rejected", recipient: text("abcdefghijklmnop"), via: text("personal"), wantState: sqlStateCheckViolation},
		{label: "both without ids rejected", recipient: text("abcdefghijklmnop"), via: text("both"), wantState: sqlStateCheckViolation},
		{label: "personal with empty array rejected", recipient: text("abcdefghijklmnop"), via: text("personal"), ids: []int64{}, wantState: sqlStateCheckViolation},
	}
	for index, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			var messageID string
			insertErr := pool.QueryRow(ctx, insertRoutedMessageSQL,
				"routed-"+string(rune('a'+index)), testCase.recipient, testCase.via, testCase.ids).Scan(&messageID)
			expectSQLState(subTest, "insert routed message", insertErr, testCase.wantState)
		})
	}
}

func TestRecipientViaKeepsExistingRowsNull(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, recipientViaVersion-1)

	recipient := "abcdefghijklmnop"
	messageID, insertErr := insertMessage(ctx, pool, "pre-via", nil, &recipient)
	if insertErr != nil {
		testInstance.Fatalf("insert message in the 0014 schema: %v", insertErr)
	}

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	if _, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler)); runErr != nil {
		testInstance.Fatalf("apply 0015 over existing rows: %v", runErr)
	}

	var via *string
	var ids []int64
	if readErr := pool.QueryRow(ctx,
		"SELECT to_recipient_via, to_personal_destination_ids FROM messages WHERE id = $1", messageID).Scan(&via, &ids); readErr != nil {
		testInstance.Fatalf("read existing message: %v", readErr)
	}
	if via != nil || !slices.Equal(ids, []int64(nil)) {
		testInstance.Fatalf("existing row via/ids = %v/%v, want NULL/NULL", via, ids)
	}
}

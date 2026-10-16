//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

const (
	insertMessageSQL = "INSERT INTO messages (app_id, channel, idempotency_key, to_destination, to_recipient, payload) " +
		"VALUES ($1, $2, $3, $4, $5, '{}'::jsonb) RETURNING id"
	insertDeliverySQL = "INSERT INTO message_deliveries (message_id, destination_id, destination_name, destination_type, state, dead_reason) " +
		"VALUES ($1, $2, $3, 'webhook', $4, $5) RETURNING id"
	insertAttemptSQL = "INSERT INTO delivery_attempts (delivery_id, attempt_number, outcome, latency_ms, started_at) " +
		"VALUES ($1, $2, 'success', 12, now())"

	stateDelivered = "delivered"
	statePending   = "pending"
	stateDead      = "dead"
)

// insertMessage inserts a minimal message and returns its generated id. The
// nullable to_* arguments are passed as *string so an omitted value is SQL NULL
// rather than an empty string (num_nonnulls treats ” as non-null).
func insertMessage(
	ctx context.Context, pool *pgxpool.Pool, idempotencyKey string, toDestination *string, toRecipient *string,
) (string, error) {
	var messageID string
	err := pool.QueryRow(ctx, insertMessageSQL,
		"urls4irl", "alerts", idempotencyKey, toDestination, toRecipient).Scan(&messageID)
	return messageID, err
}

func mustInsertMessage(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, idempotencyKey string) string {
	testInstance.Helper()
	messageID, err := insertMessage(ctx, pool, idempotencyKey, nil, nil)
	if err != nil {
		testInstance.Fatalf("insert message %q: %v", idempotencyKey, err)
	}
	return messageID
}

func TestMessageConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	destinationName := "ops_webhook"
	recipientName := "someone"

	// Order matters: the duplicate case relies on the first row existing.
	testCases := []struct {
		name           string
		idempotencyKey string
		toDestination  *string
		toRecipient    *string
		wantState      string
	}{
		{name: "valid row with no target inserts", idempotencyKey: "key-1"},
		{name: "valid row with destination inserts", idempotencyKey: "key-2", toDestination: &destinationName},
		{name: "valid row with recipient inserts", idempotencyKey: "key-3", toRecipient: &recipientName},
		{
			name: "destination and recipient both set rejected", idempotencyKey: "key-4",
			toDestination: &destinationName, toRecipient: &recipientName, wantState: sqlStateCheckViolation,
		},
		{name: "duplicate app and idempotency key rejected", idempotencyKey: "key-1", wantState: sqlStateUniqueViolation},
		{name: "empty idempotency key rejected", idempotencyKey: "", wantState: sqlStateCheckViolation},
		{name: "space in idempotency key rejected", idempotencyKey: "a b", wantState: sqlStateCheckViolation},
		{name: "201 byte idempotency key rejected", idempotencyKey: repeatByte('k', 201), wantState: sqlStateCheckViolation},
		{name: "200 byte idempotency key inserts", idempotencyKey: repeatByte('k', 200)},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			_, err := insertMessage(ctx, pool, testCase.idempotencyKey, testCase.toDestination, testCase.toRecipient)
			expectSQLState(subTest, "insert message "+testCase.name, err, testCase.wantState)
		})
	}

	testInstance.Run("non-object payload rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO messages (app_id, channel, idempotency_key, payload) VALUES ('urls4irl', 'alerts', 'array-payload', '[]'::jsonb)")
		expectSQLState(subTest, "insert message with array payload", err, sqlStateCheckViolation)
	})

	testInstance.Run("invalid channel rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO messages (app_id, channel, idempotency_key, payload) VALUES ('urls4irl', 'Bad-Channel', 'bad-channel', '{}'::jsonb)")
		expectSQLState(subTest, "insert message with invalid channel", err, sqlStateCheckViolation)
	})

	testInstance.Run("non-object display rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO messages (app_id, channel, idempotency_key, payload, display) VALUES ('urls4irl', 'alerts', 'bad-display', '{}'::jsonb, '\"text\"'::jsonb)")
		expectSQLState(subTest, "insert message with string display", err, sqlStateCheckViolation)
	})
}

// repeatByte returns a string of count copies of the given byte.
func repeatByte(char byte, count int) string {
	repeated := make([]byte, count)
	for index := range repeated {
		repeated[index] = char
	}
	return string(repeated)
}

func TestDeliveryConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	messageID := mustInsertMessage(ctx, testInstance, pool, "delivery-constraints")
	deadReason := "gone"
	badReason := "mystery"

	testInstance.Run("pending delivery without dead_reason inserts", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_one", statePending, nil).Scan(&deliveryID)
		expectSQLState(subTest, "insert pending delivery", err, "")
	})

	testInstance.Run("dead delivery with dead_reason inserts", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_two", stateDead, &deadReason).Scan(&deliveryID)
		expectSQLState(subTest, "insert dead delivery", err, "")
	})

	testInstance.Run("dead delivery without dead_reason rejected", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_three", stateDead, nil).Scan(&deliveryID)
		expectSQLState(subTest, "insert dead delivery without reason", err, sqlStateCheckViolation)
	})

	testInstance.Run("delivered delivery with dead_reason rejected", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_four", stateDelivered, &deadReason).Scan(&deliveryID)
		expectSQLState(subTest, "insert delivered delivery with reason", err, sqlStateCheckViolation)
	})

	testInstance.Run("unknown dead_reason rejected", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_five", stateDead, &badReason).Scan(&deliveryID)
		expectSQLState(subTest, "insert delivery with unknown reason", err, sqlStateCheckViolation)
	})

	testInstance.Run("unknown state rejected", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_six", "limbo", nil).Scan(&deliveryID)
		expectSQLState(subTest, "insert delivery with unknown state", err, sqlStateCheckViolation)
	})

	testInstance.Run("duplicate message and destination name rejected", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_one", statePending, nil).Scan(&deliveryID)
		expectSQLState(subTest, "insert duplicate delivery", err, sqlStateUniqueViolation)
	})

	testInstance.Run("absent message rejected", func(subTest *testing.T) {
		var deliveryID int64
		err := pool.QueryRow(ctx, insertDeliverySQL,
			"00000000-0000-0000-0000-000000000000", nil, "dest_seven", statePending, nil).Scan(&deliveryID)
		expectSQLState(subTest, "insert delivery for absent message", err, sqlStateForeignKeyViolation)
	})
}

func TestDeliveryAttemptConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	messageID := mustInsertMessage(ctx, testInstance, pool, "attempt-constraints")
	var deliveryID int64
	if scanErr := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "dest_one", statePending, nil).Scan(&deliveryID); scanErr != nil {
		testInstance.Fatalf("insert delivery: %v", scanErr)
	}

	testInstance.Run("valid attempt inserts", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, insertAttemptSQL, deliveryID, 1)
		expectSQLState(subTest, "insert attempt", err, "")
	})

	testInstance.Run("duplicate attempt number rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, insertAttemptSQL, deliveryID, 1)
		expectSQLState(subTest, "insert duplicate attempt", err, sqlStateUniqueViolation)
	})

	testInstance.Run("unknown outcome rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO delivery_attempts (delivery_id, attempt_number, outcome, latency_ms, started_at) VALUES ($1, 2, 'exploded', 1, now())",
			deliveryID)
		expectSQLState(subTest, "insert attempt with unknown outcome", err, sqlStateCheckViolation)
	})

	testInstance.Run("negative latency rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO delivery_attempts (delivery_id, attempt_number, outcome, latency_ms, started_at) VALUES ($1, 3, 'success', -1, now())",
			deliveryID)
		expectSQLState(subTest, "insert attempt with negative latency", err, sqlStateCheckViolation)
	})

	testInstance.Run("501 character error rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO delivery_attempts (delivery_id, attempt_number, outcome, latency_ms, error, started_at) VALUES ($1, 4, 'retryable', 1, $2, now())",
			deliveryID, repeatByte('e', 501))
		expectSQLState(subTest, "insert attempt with oversized error", err, sqlStateCheckViolation)
	})

	testInstance.Run("1025 byte response snippet rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO delivery_attempts (delivery_id, attempt_number, outcome, latency_ms, response_snippet, started_at) VALUES ($1, 5, 'retryable', 1, $2, now())",
			deliveryID, repeatByte('s', 1025))
		expectSQLState(subTest, "insert attempt with oversized snippet", err, sqlStateCheckViolation)
	})
}

func TestDeliveryReferentialActions(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var destinationID int64
	if scanErr := pool.QueryRow(ctx,
		"INSERT INTO destinations (name, type, sealed_secrets) VALUES ('ops_webhook', 'webhook', $1) RETURNING id",
		placeholderSealedSecrets,
	).Scan(&destinationID); scanErr != nil {
		testInstance.Fatalf("insert destination: %v", scanErr)
	}
	messageID := mustInsertMessage(ctx, testInstance, pool, "referential-actions")
	var deliveryID int64
	if scanErr := pool.QueryRow(ctx, insertDeliverySQL,
		messageID, destinationID, "ops_webhook", statePending, nil).Scan(&deliveryID); scanErr != nil {
		testInstance.Fatalf("insert delivery: %v", scanErr)
	}
	if _, attemptErr := pool.Exec(ctx, insertAttemptSQL, deliveryID, 1); attemptErr != nil {
		testInstance.Fatalf("insert attempt: %v", attemptErr)
	}

	testInstance.Run("deleting a referenced destination nulls destination_id", func(subTest *testing.T) {
		_, deleteErr := pool.Exec(ctx, "DELETE FROM destinations WHERE id = $1", destinationID)
		expectSQLState(subTest, "delete referenced destination", deleteErr, "")

		var remainingID *int64
		var snapshotName string
		if scanErr := pool.QueryRow(ctx,
			"SELECT destination_id, destination_name FROM message_deliveries WHERE id = $1", deliveryID,
		).Scan(&remainingID, &snapshotName); scanErr != nil {
			subTest.Fatalf("select delivery after destination delete: %v", scanErr)
		}
		if remainingID != nil {
			subTest.Fatalf("destination_id after destination delete = %d, want NULL", *remainingID)
		}
		if snapshotName != "ops_webhook" {
			subTest.Fatalf("destination_name after destination delete = %q, want the snapshot %q", snapshotName, "ops_webhook")
		}
	})

	testInstance.Run("deleting a message cascades to deliveries and attempts", func(subTest *testing.T) {
		_, deleteErr := pool.Exec(ctx, "DELETE FROM messages WHERE id = $1", messageID)
		expectSQLState(subTest, "delete message", deleteErr, "")

		for _, table := range []string{"message_deliveries", "delivery_attempts"} {
			var remaining int
			if scanErr := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&remaining); scanErr != nil {
				subTest.Fatalf("count %s: %v", table, scanErr)
			}
			if remaining != 0 {
				subTest.Fatalf("%s rows after message delete = %d, want 0", table, remaining)
			}
		}
	})
}

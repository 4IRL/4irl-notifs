//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

func TestAppPublishersConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	const insertPublisherSQL = "INSERT INTO app_publishers (app_id, publisher_user_id, topic_pattern, sealed_token) " +
		"VALUES ($1, 'app-user', 'pattern-*', '\\x01'::bytea)"

	testCases := []struct {
		name      string
		appID     string
		wantState string
	}{
		{name: "valid app inserts", appID: "urls4irl"},
		{name: "duplicate app rejected", appID: "urls4irl", wantState: sqlStateUniqueViolation},
		{name: "uppercase app rejected", appID: "Bad-App", wantState: sqlStateCheckViolation},
		{name: "hyphen app rejected", appID: "bad-app", wantState: sqlStateCheckViolation},
		{name: "empty app rejected", appID: "", wantState: sqlStateCheckViolation},
		{name: "64 character app rejected", appID: repeatByte('a', 64), wantState: sqlStateCheckViolation},
		{name: "63 character app inserts", appID: repeatByte('a', 63)},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertPublisherSQL, testCase.appID)
			expectSQLState(subTest, "insert app publisher "+testCase.name, err, testCase.wantState)
		})
	}
}

func TestNoPublisherDeadReasonAccepted(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	messageID := mustInsertMessage(ctx, testInstance, pool, "no-publisher")
	noPublisher := "no_publisher"

	var deliveryID int64
	err := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "recipient_one", stateDead, &noPublisher).Scan(&deliveryID)
	expectSQLState(testInstance, "insert dead delivery with no_publisher", err, "")

	// Every reason from 0003 still passes after the constraint was rebuilt.
	for _, reason := range []string{
		"terminal_status", "gone", "budget_exhausted", "destination_disabled",
		"destination_deleted", "blocked_address", "unsupported_type",
	} {
		reasonValue := reason
		var otherID int64
		otherErr := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "kept_"+reason, stateDead, &reasonValue).Scan(&otherID)
		expectSQLState(testInstance, "insert dead delivery with "+reason, otherErr, "")
	}

	unknown := "mystery"
	var rejectedID int64
	rejectedErr := pool.QueryRow(ctx, insertDeliverySQL, messageID, nil, "recipient_two", stateDead, &unknown).Scan(&rejectedID)
	expectSQLState(testInstance, "insert dead delivery with unknown reason", rejectedErr, sqlStateCheckViolation)
}

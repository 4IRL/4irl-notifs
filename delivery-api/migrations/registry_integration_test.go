//go:build integration

package migrations_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

const (
	registryTestBudget = 30 * time.Second

	sqlStateUniqueViolation     = "23505"
	sqlStateCheckViolation      = "23514"
	sqlStateForeignKeyViolation = "23503"

	insertDestinationSQL = "INSERT INTO destinations (name, type, sealed_secrets) VALUES ($1, $2, $3)"
	insertRouteSQL       = "INSERT INTO routes (app_pattern, channel_pattern) VALUES ($1, $2)"
	insertLinkSQL        = "INSERT INTO route_destinations (route_id, destination_id) VALUES ($1, $2)"

	// absentIDOffset pushes a destination id far past any row the test inserted.
	absentIDOffset = 1000
)

// placeholderSealedSecrets stands in for a sealed blob; the schema only
// requires the column to be non-null.
var placeholderSealedSecrets = []byte{0x01}

const (
	// Boundary strings for the slug / channel patterns.
	name63Chars          = "a12345678901234567890123456789012345678901234567890123456789012"
	channel32Chars       = "a1234567890123456789012345678901"
	channel33Chars       = "a12345678901234567890123456789012"
	channelPrefix31Chars = "a123456789012345678901234567890*"
	channelPrefix32Chars = "a1234567890123456789012345678901*"
)

// expectSQLState fails the test unless err is a Postgres error with the given
// SQLSTATE (wantState empty means err must be nil).
func expectSQLState(testInstance *testing.T, statement string, err error, wantState string) {
	testInstance.Helper()
	if wantState == "" {
		if err != nil {
			testInstance.Fatalf("%s: unexpected error: %v", statement, err)
		}
		return
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		testInstance.Fatalf("%s: error = %v, want SQLSTATE %s", statement, err, wantState)
	}
	if pgErr.Code != wantState {
		testInstance.Fatalf("%s: SQLSTATE = %s (%s), want %s", statement, pgErr.Code, pgErr.Message, wantState)
	}
}

func insertDestination(
	ctx context.Context, pool *pgxpool.Pool, name string, destinationType string,
) error {
	_, err := pool.Exec(ctx, insertDestinationSQL, name, destinationType, placeholderSealedSecrets)
	return err
}

func TestDestinationConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	// Order matters: the duplicate case relies on the first row existing.
	testCases := []struct {
		name            string
		destinationName string
		destinationType string
		wantState       string
	}{
		{name: "valid row inserts", destinationName: "ops_ntfy", destinationType: "ntfy"},
		{name: "valid discord row inserts", destinationName: "ops_discord", destinationType: "discord"},
		{name: "valid webhook row inserts", destinationName: "ops_webhook", destinationType: "webhook"},
		{name: "uppercase name rejected", destinationName: "Ops", destinationType: "ntfy", wantState: sqlStateCheckViolation},
		{name: "leading underscore rejected", destinationName: "_ops", destinationType: "ntfy", wantState: sqlStateCheckViolation},
		{name: "hyphen rejected", destinationName: "ops-ntfy", destinationType: "ntfy", wantState: sqlStateCheckViolation},
		{name: "empty name rejected", destinationName: "", destinationType: "ntfy", wantState: sqlStateCheckViolation},
		{
			name:            "64 character name rejected",
			destinationName: "a123456789012345678901234567890123456789012345678901234567890123",
			destinationType: "ntfy",
			wantState:       sqlStateCheckViolation,
		},
		{name: "63 character name inserts", destinationName: name63Chars, destinationType: "ntfy"},
		{name: "unknown type rejected", destinationName: "ops_sms", destinationType: "sms", wantState: sqlStateCheckViolation},
		{name: "duplicate name rejected", destinationName: "ops_ntfy", destinationType: "ntfy", wantState: sqlStateUniqueViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			err := insertDestination(ctx, pool, testCase.destinationName, testCase.destinationType)
			expectSQLState(subTest, "insert destination "+testCase.destinationName, err, testCase.wantState)
		})
	}

	// 0007 widened owner_kind to operator or app; an app owner must now name its
	// app (the owner constraints are covered in ownership_integration_test.go).
	testInstance.Run("owner_kind app without owner_app_id rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO destinations (name, type, owner_kind, sealed_secrets) VALUES ('owned', 'ntfy', 'app', $1)",
			placeholderSealedSecrets)
		expectSQLState(subTest, "insert destination with owner_kind app and no owner", err, sqlStateCheckViolation)
	})

	testInstance.Run("owner_kind other than operator or app rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			"INSERT INTO destinations (name, type, owner_kind, sealed_secrets) VALUES ('owned', 'ntfy', 'person', $1)",
			placeholderSealedSecrets)
		expectSQLState(subTest, "insert destination with owner_kind person", err, sqlStateCheckViolation)
	})
}

func TestRouteConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	// Order matters: the duplicate case relies on the first row existing.
	testCases := []struct {
		name           string
		appPattern     string
		channelPattern string
		wantState      string
	}{
		{name: "wildcard app with prefix channel inserts", appPattern: "*", channelPattern: "backup_*"},
		{name: "exact app and channel inserts", appPattern: "urls4irl", channelPattern: "alerts"},
		{name: "wildcard channel inserts", appPattern: "urls4irl", channelPattern: "*"},
		{name: "32 character channel inserts", appPattern: "limits", channelPattern: channel32Chars},
		{name: "31 character prefix channel inserts", appPattern: "limits", channelPattern: channelPrefix31Chars},
		{name: "33 character channel rejected", appPattern: "limits", channelPattern: channel33Chars, wantState: sqlStateCheckViolation},
		{name: "32 character prefix channel rejected", appPattern: "limits", channelPattern: channelPrefix32Chars, wantState: sqlStateCheckViolation},
		{name: "duplicate pattern pair rejected", appPattern: "*", channelPattern: "backup_*", wantState: sqlStateUniqueViolation},
		{name: "hyphen in channel rejected", appPattern: "*", channelPattern: "a-b", wantState: sqlStateCheckViolation},
		{name: "uppercase channel rejected", appPattern: "*", channelPattern: "Alerts", wantState: sqlStateCheckViolation},
		{name: "bare star prefix rejected", appPattern: "*", channelPattern: "_*", wantState: sqlStateCheckViolation},
		{name: "mid-string star rejected", appPattern: "*", channelPattern: "a*b", wantState: sqlStateCheckViolation},
		{name: "app prefix pattern rejected", appPattern: "urls*", channelPattern: "alerts", wantState: sqlStateCheckViolation},
		{name: "empty app pattern rejected", appPattern: "", channelPattern: "alerts", wantState: sqlStateCheckViolation},
		{name: "empty channel pattern rejected", appPattern: "*", channelPattern: "", wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertRouteSQL, testCase.appPattern, testCase.channelPattern)
			expectSQLState(subTest, "insert route "+testCase.appPattern+"/"+testCase.channelPattern, err, testCase.wantState)
		})
	}
}

func TestRouteDestinationReferentialActions(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var routeID, destinationID int64
	if scanErr := pool.QueryRow(ctx,
		"INSERT INTO routes (app_pattern, channel_pattern) VALUES ('*', '*') RETURNING id",
	).Scan(&routeID); scanErr != nil {
		testInstance.Fatalf("insert route: %v", scanErr)
	}
	if scanErr := pool.QueryRow(ctx,
		"INSERT INTO destinations (name, type, sealed_secrets) VALUES ('ops_ntfy', 'ntfy', $1) RETURNING id",
		placeholderSealedSecrets,
	).Scan(&destinationID); scanErr != nil {
		testInstance.Fatalf("insert destination: %v", scanErr)
	}
	if _, linkErr := pool.Exec(ctx,
		insertLinkSQL, routeID, destinationID,
	); linkErr != nil {
		testInstance.Fatalf("link route to destination: %v", linkErr)
	}

	testInstance.Run("deleting a referenced destination is restricted", func(subTest *testing.T) {
		_, err := pool.Exec(ctx, "DELETE FROM destinations WHERE id = $1", destinationID)
		expectSQLState(subTest, "delete referenced destination", err, sqlStateForeignKeyViolation)
	})

	testInstance.Run("linking an absent destination is rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			insertLinkSQL, routeID, destinationID+absentIDOffset)
		expectSQLState(subTest, "link absent destination", err, sqlStateForeignKeyViolation)
	})

	testInstance.Run("duplicate link is rejected", func(subTest *testing.T) {
		_, err := pool.Exec(ctx,
			insertLinkSQL, routeID, destinationID)
		expectSQLState(subTest, "duplicate link", err, sqlStateUniqueViolation)
	})

	testInstance.Run("deleting a route cascades its links", func(subTest *testing.T) {
		_, deleteErr := pool.Exec(ctx, "DELETE FROM routes WHERE id = $1", routeID)
		expectSQLState(subTest, "delete route", deleteErr, "")

		var remaining int
		if scanErr := pool.QueryRow(ctx,
			"SELECT count(*) FROM route_destinations WHERE route_id = $1", routeID,
		).Scan(&remaining); scanErr != nil {
			subTest.Fatalf("count route_destinations: %v", scanErr)
		}
		if remaining != 0 {
			subTest.Fatalf("route_destinations rows after route delete = %d, want 0", remaining)
		}

		_, destinationDeleteErr := pool.Exec(ctx, "DELETE FROM destinations WHERE id = $1", destinationID)
		expectSQLState(subTest, "delete now-unreferenced destination", destinationDeleteErr, "")
	})
}

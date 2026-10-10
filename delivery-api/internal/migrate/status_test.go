package migrate

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUndefinedTable(testInstance *testing.T) {
	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "undefined table", err: &pgconn.PgError{Code: "42P01"}, want: true},
		{name: "wrapped undefined table", err: fmt.Errorf("query: %w", &pgconn.PgError{Code: "42P01"}), want: true},
		{name: "other sqlstate", err: &pgconn.PgError{Code: "42601"}, want: false},
		{name: "non-postgres error", err: errors.New("connection refused"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			if got := isUndefinedTable(testCase.err); got != testCase.want {
				subTest.Fatalf("isUndefinedTable(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

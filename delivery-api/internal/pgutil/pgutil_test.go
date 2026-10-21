package pgutil

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestWrapDBErrorFlattensPostgresErrors(t *testing.T) {
	pgErr := &pgconn.PgError{
		Code:    "23505",
		Message: "duplicate key value violates unique constraint",
		Detail:  "Key (name)=(secret-row-content) already exists.",
	}

	wrapped := WrapDBError("insert thing", pgErr)

	if want := "insert thing: duplicate key value violates unique constraint (SQLSTATE 23505)"; wrapped.Error() != want {
		t.Errorf("error = %q, want %q", wrapped, want)
	}
	if strings.Contains(wrapped.Error(), "secret-row-content") {
		t.Error("wrapped error leaks the PgError detail")
	}
	var reachable *pgconn.PgError
	if errors.As(wrapped, &reachable) {
		t.Error("PgError is still reachable through errors.As")
	}
}

func TestErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "postgres error", err: &pgconn.PgError{Code: SQLStateUniqueViolation}, want: SQLStateUniqueViolation},
		{name: "wrapped postgres error", err: fmt.Errorf("scan: %w", &pgconn.PgError{Code: "23503"}), want: "23503"},
		{name: "other error", err: context.Canceled, want: ""},
		{name: "nil error", err: nil, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ErrorCode(test.err); got != test.want {
				t.Errorf("ErrorCode() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDistinct(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  []string
	}{
		{name: "nil input yields empty non-nil", names: nil, want: []string{}},
		{name: "no duplicates", names: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "duplicates removed keeping first occurrence order", names: []string{"b", "a", "b", "a", "c"}, want: []string{"b", "a", "c"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Distinct(test.names)
			if got == nil || !slices.Equal(got, test.want) {
				t.Errorf("Distinct(%v) = %#v, want %#v", test.names, got, test.want)
			}
		})
	}
}

func TestWrapDBErrorKeepsTheChainOfOtherErrors(t *testing.T) {
	wrapped := WrapDBError("begin transaction", context.Canceled)

	if !errors.Is(wrapped, context.Canceled) {
		t.Errorf("errors.Is(%v, context.Canceled) = false, want true", wrapped)
	}
	if want := "begin transaction: context canceled"; wrapped.Error() != want {
		t.Errorf("error = %q, want %q", wrapped, want)
	}
}

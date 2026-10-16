package pgutil

import (
	"context"
	"errors"
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

func TestWrapDBErrorKeepsTheChainOfOtherErrors(t *testing.T) {
	wrapped := WrapDBError("begin transaction", context.Canceled)

	if !errors.Is(wrapped, context.Canceled) {
		t.Errorf("errors.Is(%v, context.Canceled) = false, want true", wrapped)
	}
	if want := "begin transaction: context canceled"; wrapped.Error() != want {
		t.Errorf("error = %q, want %q", wrapped, want)
	}
}

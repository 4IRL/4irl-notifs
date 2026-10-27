//go:build integration

package keyrotation

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

func TestSealedColumnsCoveredByRegistry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, t)

	found, err := SealedColumns(ctx, pool)
	if err != nil {
		t.Fatalf("SealedColumns: %v", err)
	}
	missing, stale := CoverageGaps(found, Registry())
	if len(missing) != 0 || len(stale) != 0 {
		t.Fatalf("missing=%v stale=%v, want none", missing, stale)
	}
}

func TestSealedColumnsDetectsNewUncoveredColumn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, t)

	if _, err := pool.Exec(ctx, "ALTER TABLE destinations ADD COLUMN sealed_extra bytea"); err != nil {
		t.Fatalf("add column: %v", err)
	}
	found, err := SealedColumns(ctx, pool)
	if err != nil {
		t.Fatalf("SealedColumns: %v", err)
	}
	missing, stale := CoverageGaps(found, Registry())
	want := []SealedColumn{{Table: "destinations", Column: "sealed_extra"}}
	if !slices.Equal(missing, want) || len(stale) != 0 {
		t.Fatalf("missing=%v stale=%v, want missing=%v", missing, stale, want)
	}
}

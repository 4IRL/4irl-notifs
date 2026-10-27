//go:build integration

package keyrotation

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

func TestSealedColumnsCoveredByRegistry(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	found, columnsErr := SealedColumns(ctx, pool)
	if columnsErr != nil {
		testInstance.Fatalf("SealedColumns: %v", columnsErr)
	}
	missing, stale := CoverageGaps(found, Registry())
	if len(missing) != 0 || len(stale) != 0 {
		testInstance.Fatalf("missing=%v stale=%v, want none", missing, stale)
	}
}

func TestSealedColumnsDetectsNewUncoveredColumn(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	if _, alterErr := pool.Exec(ctx, "ALTER TABLE destinations ADD COLUMN sealed_extra bytea"); alterErr != nil {
		testInstance.Fatalf("add column: %v", alterErr)
	}
	found, columnsErr := SealedColumns(ctx, pool)
	if columnsErr != nil {
		testInstance.Fatalf("SealedColumns: %v", columnsErr)
	}
	missing, stale := CoverageGaps(found, Registry())
	want := []SealedColumn{{Table: "destinations", Column: "sealed_extra"}}
	if !slices.Equal(missing, want) || len(stale) != 0 {
		testInstance.Fatalf("missing=%v stale=%v, want missing=%v", missing, stale, want)
	}
}

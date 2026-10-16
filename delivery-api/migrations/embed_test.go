package migrations_test

import (
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

// TestEmbeddedMigrationsLoad guards the shipped migration files: they must all
// satisfy the loader's naming rules, or the binary would fail at startup.
func TestEmbeddedMigrationsLoad(testInstance *testing.T) {
	loaded, err := migrate.Load(migrations.FS)
	if err != nil {
		testInstance.Fatalf("Load(migrations.FS) error = %v, want nil", err)
	}
	if len(loaded) == 0 {
		testInstance.Fatal("Load(migrations.FS) returned no migrations, want at least the baseline")
	}
	if loaded[0].Version != 1 || loaded[0].Name != "baseline" {
		testInstance.Fatalf("first migration = %04d_%s, want 0001_baseline", loaded[0].Version, loaded[0].Name)
	}
	if got := migrate.ExpectedVersion(loaded); got != loaded[len(loaded)-1].Version {
		testInstance.Fatalf("ExpectedVersion = %d, want the last migration's version %d", got, loaded[len(loaded)-1].Version)
	}
}

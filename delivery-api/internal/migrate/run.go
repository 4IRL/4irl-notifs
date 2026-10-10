package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// cleanupTimeout bounds cleanup that must outlive a canceled context (lock
// release, transaction rollback).
const cleanupTimeout = 5 * time.Second

// Store is the persistence the runner needs. PGStore is the Postgres
// implementation; tests use a fake.
type Store interface {
	// Lock takes a cross-process migration lock and returns its release func.
	Lock(ctx context.Context) (unlock func(context.Context) error, err error)
	// EnsureTables creates schema_migrations and schema_migration_failures.
	EnsureTables(ctx context.Context) error
	// AppliedVersions returns the set of versions already applied.
	AppliedVersions(ctx context.Context) (map[int]bool, error)
	// Apply runs one migration and records it, atomically.
	Apply(ctx context.Context, migration Migration) error
	// RecordFailure stores a failure outside any rolled-back transaction.
	RecordFailure(ctx context.Context, version int, message string) error
}

// Run applies, in order, every migration in migrations not yet applied and
// returns how many it applied. It holds the store's lock for the whole run.
// On the first apply failure it stops, records the failure so readiness
// can surface it, and returns the apply error; a failure
// to record is logged but never replaces the apply error.
func Run(
	ctx context.Context,
	store Store,
	migrations []Migration,
	logger *slog.Logger,
) (appliedCount int, err error) {
	unlock, lockErr := store.Lock(ctx)
	if lockErr != nil {
		return 0, fmt.Errorf("acquire migration lock: %w", lockErr)
	}
	defer func() {
		// Detached from ctx so the lock is still released after a cancellation,
		// but bounded so a hung database cannot stall process exit.
		unlockCtx, cancelUnlock := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancelUnlock()
		if unlockErr := unlock(unlockCtx); unlockErr != nil {
			logger.Error("release migration lock", "error", unlockErr)
		}
	}()

	if ensureErr := store.EnsureTables(ctx); ensureErr != nil {
		return 0, fmt.Errorf("ensure migration tables: %w", ensureErr)
	}
	applied, appliedErr := store.AppliedVersions(ctx)
	if appliedErr != nil {
		return 0, fmt.Errorf("read applied migrations: %w", appliedErr)
	}

	for _, migration := range migrations {
		if applied[migration.Version] {
			continue
		}

		if applyErr := store.Apply(ctx, migration); applyErr != nil {
			// A canceled ctx (SIGTERM mid-migration) is an interruption, not a
			// defect in the migration, so it must not surface as a failure.
			if ctx.Err() == nil {
				if recordErr := store.RecordFailure(ctx, migration.Version, applyErr.Error()); recordErr != nil {
					logger.Error("record migration failure", "version", migration.Version, "error", recordErr)
				}
			}
			return appliedCount, fmt.Errorf("apply migration %04d_%s: %w", migration.Version, migration.Name, applyErr)
		}
		logger.Info("migration applied", "version", migration.Version, "name", migration.Name)
		appliedCount++
	}
	return appliedCount, nil
}

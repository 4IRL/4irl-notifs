// Package readiness adapts the database pool and migration status to the
// httpapi.ReadinessChecker interface that backs GET /readyz.
package readiness

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/httpapi"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
)

// checkTimeout bounds one readiness check.
const checkTimeout = 2 * time.Second

// Checker reports whether the database is reachable and which schema version
// it holds, against the version this binary expects.
type Checker struct {
	pool     *pgxpool.Pool
	store    *migrate.PGStore
	expected int
}

// NewChecker returns a Checker. expected is the highest migration version
// embedded in this binary (migrate.ExpectedVersion).
func NewChecker(pool *pgxpool.Pool, store *migrate.PGStore, expected int) *Checker {
	return &Checker{pool: pool, store: store, expected: expected}
}

// Readiness pings the database, then reads the schema status.
func (checker *Checker) Readiness(ctx context.Context) (httpapi.Readiness, error) {
	// Bounded so a hung or blackholed database fails the probe instead of
	// hanging it until the client gives up.
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	if pingErr := checker.pool.Ping(ctx); pingErr != nil {
		return httpapi.Readiness{}, fmt.Errorf("ping database: %w", pingErr)
	}
	status, statusErr := checker.store.Status(ctx)
	if statusErr != nil {
		return httpapi.Readiness{}, fmt.Errorf("read schema status: %w", statusErr)
	}
	return httpapi.Readiness{
		SchemaVersion:         status.SchemaVersion,
		ExpectedSchemaVersion: checker.expected,
		LastMigrationFailure:  status.LastFailure,
	}, nil
}

package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// connectTimeout bounds each connection attempt so a dead host fails fast
// instead of blocking migrate or a readiness check indefinitely.
const connectTimeout = 5 * time.Second

// NewPool builds a pgx connection pool for config. pgxpool connects lazily, so
// this succeeds without a reachable database; connection problems surface on
// first use (readiness checks, migrations).
func NewPool(ctx context.Context, config Config) (*pgxpool.Pool, error) {
	poolConfig, parseErr := pgxpool.ParseConfig(config.ConnString())
	if parseErr != nil {
		return nil, fmt.Errorf("parse database config: %w", parseErr)
	}
	poolConfig.ConnConfig.ConnectTimeout = connectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	return pool, nil
}

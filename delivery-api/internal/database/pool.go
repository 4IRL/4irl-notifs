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

// maxConns is the explicit pool size. The publisher keeps up to MaxInFlightMints*2 connections busy
// (publisher/types.go: one for the mint lock transaction, one for the in-lock binding re-check), the
// dispatch worker uses DefaultConcurrency (4) briefly, and 8 leaves headroom for HTTP handlers while
// staying under the Postgres max_connections=20 set in docker-compose.yml. Raise this before raising
// MaxInFlightMints.
const maxConns int32 = 8

// NewPool builds a pgx connection pool for config. pgxpool connects lazily, so
// this succeeds without a reachable database; connection problems surface on
// first use (readiness checks, migrations).
func NewPool(ctx context.Context, config Config) (*pgxpool.Pool, error) {
	poolConfig, parseErr := pgxpool.ParseConfig(config.ConnString())
	if parseErr != nil {
		return nil, fmt.Errorf("parse database config: %w", parseErr)
	}
	poolConfig.ConnConfig.ConnectTimeout = connectTimeout
	poolConfig.MaxConns = maxConns

	pool, newPoolErr := pgxpool.NewWithConfig(ctx, poolConfig)
	if newPoolErr != nil {
		return nil, fmt.Errorf("create database pool: %w", newPoolErr)
	}
	return pool, nil
}

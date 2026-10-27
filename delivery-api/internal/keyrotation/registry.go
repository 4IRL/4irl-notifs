// Package keyrotation re-seals every stored secret under a new master key. It
// owns the registry of sealed columns, which a coverage test checks against the
// real schema so a new sealed column cannot be forgotten.
package keyrotation

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// SealedTable names one database column that holds a sealed secret and how its
// encryption context (AAD) is built. The key column is immutable: the AAD binds
// it, so changing it would orphan the secret, and rotation relies on that.
// Row keys are carried as text.
type SealedTable struct {
	Table        string
	KeyColumn    string
	SealedColumn string
	Nullable     bool
	// AAD builds the additional authenticated data for the row with that key.
	AAD func(rowKey string) ([]byte, error)
}

// Registry returns every sealed column. It is the single list rotation walks.
func Registry() []SealedTable {
	return []SealedTable{
		{
			Table:        "destinations",
			KeyColumn:    "name",
			SealedColumn: "sealed_secrets",
			Nullable:     false,
			AAD: func(rowKey string) ([]byte, error) {
				return registry.DestinationSecretsAAD(rowKey), nil
			},
		},
		{
			Table:        "app_publishers",
			KeyColumn:    "app_id",
			SealedColumn: "sealed_token",
			Nullable:     false,
			AAD: func(rowKey string) ([]byte, error) {
				return publisher.TokenAAD(rowKey), nil
			},
		},
		{
			Table:        "heartbeat_check_probes",
			KeyColumn:    "check_id",
			SealedColumn: "sealed_access_headers",
			Nullable:     true,
			AAD: func(rowKey string) ([]byte, error) {
				checkID, parseErr := strconv.ParseInt(rowKey, 10, 64)
				if parseErr != nil {
					return nil, fmt.Errorf("heartbeat_check_probes row key %q is not an integer check id", rowKey)
				}
				return heartbeat.ProbeAccessAAD(checkID), nil
			},
		},
	}
}

// SealedColumn identifies one table column.
type SealedColumn struct {
	Table  string
	Column string
}

// CoverageGaps compares the sealed columns found in the database with the
// registry. missing are database columns absent from the registry; stale are
// registry entries with no such database column (a typo in the registry).
func CoverageGaps(found []SealedColumn, tables []SealedTable) (missing, stale []SealedColumn) {
	registered := make([]SealedColumn, 0, len(tables))
	for _, table := range tables {
		registered = append(registered, SealedColumn{Table: table.Table, Column: table.SealedColumn})
	}
	for _, column := range found {
		if !slices.Contains(registered, column) {
			missing = append(missing, column)
		}
	}
	for _, column := range registered {
		if !slices.Contains(found, column) {
			stale = append(stale, column)
		}
	}
	return missing, stale
}

// sealedColumnsQuery lists the bytea columns named sealed_* in the current
// schema only, so other schemas on a shared database never leak in.
const sealedColumnsQuery = `SELECT table_name, column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND data_type = 'bytea' AND column_name LIKE 'sealed\_%'
ORDER BY table_name, column_name`

// SealedColumns loads every sealed bytea column in the current schema.
func SealedColumns(ctx context.Context, querier pgutil.Querier) ([]SealedColumn, error) {
	rows, queryErr := querier.Query(ctx, sealedColumnsQuery)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list sealed columns", queryErr)
	}
	defer rows.Close()

	var columns []SealedColumn
	for rows.Next() {
		var column SealedColumn
		if scanErr := rows.Scan(&column.Table, &column.Column); scanErr != nil {
			return nil, pgutil.WrapDBError("scan sealed column", scanErr)
		}
		columns = append(columns, column)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("list sealed columns", rowsErr)
	}
	return columns, nil
}

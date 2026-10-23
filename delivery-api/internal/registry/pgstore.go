package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const sqlStateForeignKeyViolation = "23503"

// CheckViolationError turns a SQLSTATE 23514 error from operation into ErrCheckViolation, keeping the name of
// the constraint that fired so the service log can tell which CHECK a request tripped. Only the constraint
// name is kept: a PgError's Message and Detail can carry the failing row (sealed secrets included), so they
// never leave this function and the PgError itself is not reachable from the returned error. It is exported
// so the heartbeat store maps its CHECK violations the same way.
func CheckViolationError(operation string, err error) error {
	var pgErr *pgconn.PgError
	constraintName := ""
	if errors.As(err, &pgErr) {
		constraintName = pgErr.ConstraintName
	}
	return fmt.Errorf("%s: %w (constraint %s)", operation, ErrCheckViolation, constraintName)
}

// Names sort bytewise (COLLATE "C") so ordering never depends on the database's
// locale: destination names are [a-z0-9_] slugs, and glibc locales ignore
// underscores when comparing.
const (
	destinationColumns = `id, name, type, config, sealed_secrets, enabled, disabled_reason, created_at, updated_at,
	owner_kind, owner_app_id`

	// routeSelect and routeGroup bracket a WHERE clause: one row per route with its
	// destination ids and names aggregated in name order, so the two slices line up.
	routeSelect = `SELECT r.id, r.app_pattern, r.channel_pattern, r.enabled, r.created_at, r.updated_at,
	r.owner_kind, r.owner_app_id,
	COALESCE(array_agg(d.id ORDER BY d.name COLLATE "C") FILTER (WHERE d.id IS NOT NULL), '{}'::bigint[]),
	COALESCE(array_agg(d.name ORDER BY d.name COLLATE "C") FILTER (WHERE d.id IS NOT NULL), '{}'::text[])
FROM routes r
LEFT JOIN route_destinations rd ON rd.route_id = r.id
LEFT JOIN destinations d ON d.id = rd.destination_id`
	routeGroup = ` GROUP BY r.id ORDER BY r.id`
)

// PGStore is the Postgres-backed Store. It is covered by the integration
// tests, not unit tests.
type PGStore struct {
	pool *pgxpool.Pool
}

// Compile-time check that PGStore satisfies Store.
var _ Store = (*PGStore)(nil)

// NewPGStore returns a PGStore over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// InsertDestination persists destination and returns it with its id and
// timestamps. SQLSTATE 23505 maps to ErrDestinationExists.
func (store *PGStore) InsertDestination(
	ctx context.Context, destination StoredDestination,
) (StoredDestination, error) {
	row := store.pool.QueryRow(ctx,
		`INSERT INTO destinations (name, type, config, sealed_secrets, enabled, disabled_reason, owner_kind, owner_app_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+destinationColumns,
		destination.Name, destination.Type, configOrEmpty(destination.Config),
		destination.SealedSecrets, destination.Enabled, destination.DisabledReason,
		ownerKindOrOperator(destination.OwnerKind), destination.OwnerAppID)
	inserted, scanErr := scanDestination(row)
	if scanErr != nil {
		switch pgutil.ErrorCode(scanErr) {
		case pgutil.SQLStateUniqueViolation:
			return StoredDestination{}, ErrDestinationExists
		case pgutil.SQLStateCheckViolation:
			return StoredDestination{}, CheckViolationError("insert destination", scanErr)
		}
		return StoredDestination{}, pgutil.WrapDBError("insert destination", scanErr)
	}
	return inserted, nil
}

// ListDestinations returns every destination ordered by name.
func (store *PGStore) ListDestinations(ctx context.Context) ([]StoredDestination, error) {
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+destinationColumns+` FROM destinations ORDER BY name COLLATE "C"`)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list destinations", queryErr)
	}
	destinations, collectErr := collectDestinations(rows)
	if collectErr != nil {
		return nil, pgutil.WrapDBError("list destinations", collectErr)
	}
	return destinations, nil
}

// GetDestination returns the named destination, or ErrDestinationNotFound.
func (store *PGStore) GetDestination(ctx context.Context, name string) (StoredDestination, error) {
	destination, scanErr := scanDestination(store.pool.QueryRow(ctx,
		`SELECT `+destinationColumns+` FROM destinations WHERE name = $1`, name))
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return StoredDestination{}, ErrDestinationNotFound
		}
		return StoredDestination{}, pgutil.WrapDBError("get destination", scanErr)
	}
	return destination, nil
}

// GetDestinationsByID returns the destinations whose ids are in ids, ordered by
// name. Unmatched ids are omitted.
func (store *PGStore) GetDestinationsByID(ctx context.Context, ids []int64) ([]StoredDestination, error) {
	if ids == nil {
		ids = []int64{}
	}
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+destinationColumns+` FROM destinations WHERE id = ANY($1) ORDER BY name COLLATE "C"`, ids)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("get destinations by id", queryErr)
	}
	destinations, collectErr := collectDestinations(rows)
	if collectErr != nil {
		return nil, pgutil.WrapDBError("get destinations by id", collectErr)
	}
	return destinations, nil
}

// UpdateDestination locks the named row with SELECT ... FOR UPDATE, runs
// mutate, and persists the returned config, sealed secrets, enabled flag and
// disabled reason (name and type are immutable) while bumping updated_at. A
// mutate error rolls back and is returned unchanged.
func (store *PGStore) UpdateDestination(
	ctx context.Context, name string, mutate func(StoredDestination) (StoredDestination, error),
) (StoredDestination, error) {
	var updated StoredDestination
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, scanErr := scanDestination(tx.QueryRow(ctx,
			`SELECT `+destinationColumns+` FROM destinations WHERE name = $1 FOR UPDATE`, name))
		if scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrDestinationNotFound
			}
			return pgutil.WrapDBError("lock destination", scanErr)
		}

		mutated, mutateErr := mutate(current)
		if mutateErr != nil {
			return mutateErr
		}

		saved, saveErr := scanDestination(tx.QueryRow(ctx,
			`UPDATE destinations
SET config = $2, sealed_secrets = $3, enabled = $4, disabled_reason = $5, updated_at = now()
WHERE id = $1 RETURNING `+destinationColumns,
			current.ID, configOrEmpty(mutated.Config), mutated.SealedSecrets,
			mutated.Enabled, mutated.DisabledReason))
		if saveErr != nil {
			if pgutil.ErrorCode(saveErr) == pgutil.SQLStateCheckViolation {
				return CheckViolationError("update destination", saveErr)
			}
			return pgutil.WrapDBError("update destination", saveErr)
		}
		updated = saved
		return nil
	})
	if txErr != nil {
		return StoredDestination{}, txErr
	}
	return updated, nil
}

// DeleteDestination removes the named destination and reports whether a row
// existed. SQLSTATE 23503 (a route or heartbeat check still references it) maps to
// ErrDestinationInUse.
func (store *PGStore) DeleteDestination(ctx context.Context, name string) (bool, error) {
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM destinations WHERE name = $1`, name)
	if execErr != nil {
		return false, deleteDestinationError(execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// ListDestinationsByOwner returns the destinations owned by app appID, ordered by name.
func (store *PGStore) ListDestinationsByOwner(ctx context.Context, appID string) ([]StoredDestination, error) {
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+destinationColumns+` FROM destinations
WHERE owner_kind = 'app' AND owner_app_id = $1 ORDER BY name COLLATE "C"`, appID)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list app destinations", queryErr)
	}
	destinations, collectErr := collectDestinations(rows)
	if collectErr != nil {
		return nil, pgutil.WrapDBError("list app destinations", collectErr)
	}
	return destinations, nil
}

// DeleteDestinationOwnedBy removes the named destination only when app appID owns it, in one
// statement, so a destination re-created under that name with another owner is never touched.
// SQLSTATE 23503 maps to ErrDestinationInUse.
func (store *PGStore) DeleteDestinationOwnedBy(ctx context.Context, name, appID string) (bool, error) {
	tag, execErr := store.pool.Exec(ctx,
		`DELETE FROM destinations WHERE name = $1 AND owner_kind = 'app' AND owner_app_id = $2`, name, appID)
	if execErr != nil {
		return false, deleteDestinationError(execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// deleteDestinationError maps a failed destination DELETE: a foreign-key violation (a route or
// heartbeat check still references the row) is ErrDestinationInUse.
func deleteDestinationError(execErr error) error {
	if pgutil.ErrorCode(execErr) == sqlStateForeignKeyViolation {
		return ErrDestinationInUse
	}
	return pgutil.WrapDBError("delete destination", execErr)
}

// InsertRoute persists route and links the destinations named in
// route.DestinationNames in one transaction. An unknown name returns
// ErrUnknownDestination with nothing inserted; SQLSTATE 23505 maps to
// ErrRouteExists.
func (store *PGStore) InsertRoute(ctx context.Context, route StoredRoute) (StoredRoute, error) {
	var inserted StoredRoute
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		destinationIDs, resolveErr := resolveDestinationIDs(ctx, tx, route.DestinationNames, route.AppPattern)
		if resolveErr != nil {
			return resolveErr
		}

		var routeID int64
		if scanErr := tx.QueryRow(ctx,
			`INSERT INTO routes (app_pattern, channel_pattern, enabled, owner_kind, owner_app_id)
VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			route.AppPattern, route.ChannelPattern, route.Enabled,
			ownerKindOrOperator(route.OwnerKind), route.OwnerAppID).Scan(&routeID); scanErr != nil {
			switch pgutil.ErrorCode(scanErr) {
			case pgutil.SQLStateUniqueViolation:
				return ErrRouteExists
			case pgutil.SQLStateCheckViolation:
				return CheckViolationError("insert route", scanErr)
			}
			return pgutil.WrapDBError("insert route", scanErr)
		}
		if linkErr := linkDestinations(ctx, tx, routeID, destinationIDs); linkErr != nil {
			return linkErr
		}

		loaded, loadErr := queryRoute(ctx, tx, routeID)
		if loadErr != nil {
			return loadErr
		}
		inserted = loaded
		return nil
	})
	if txErr != nil {
		return StoredRoute{}, txErr
	}
	return inserted, nil
}

// ListRoutes returns every route ordered by id.
func (store *PGStore) ListRoutes(ctx context.Context) ([]StoredRoute, error) {
	return queryRoutes(ctx, store.pool, "list routes", "")
}

// GetRoute returns the route, or ErrRouteNotFound.
func (store *PGStore) GetRoute(ctx context.Context, id int64) (StoredRoute, error) {
	return queryRoute(ctx, store.pool, id)
}

// UpdateRoute locks the route, runs mutate, and persists the returned patterns,
// enabled flag and destination set (DestinationNames) in one transaction. A
// mutate error rolls back and is returned unchanged.
func (store *PGStore) UpdateRoute(
	ctx context.Context, id int64, mutate func(StoredRoute) (StoredRoute, error),
) (StoredRoute, error) {
	var updated StoredRoute
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var lockedID int64
		lockErr := tx.QueryRow(ctx, `SELECT id FROM routes WHERE id = $1 FOR UPDATE`, id).Scan(&lockedID)
		if lockErr != nil {
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return ErrRouteNotFound
			}
			return pgutil.WrapDBError("lock route", lockErr)
		}
		current, loadErr := queryRoute(ctx, tx, id)
		if loadErr != nil {
			return loadErr
		}

		mutated, mutateErr := mutate(current)
		if mutateErr != nil {
			return mutateErr
		}
		destinationIDs, resolveErr := resolveDestinationIDs(ctx, tx, mutated.DestinationNames, mutated.AppPattern)
		if resolveErr != nil {
			return resolveErr
		}

		if _, execErr := tx.Exec(ctx,
			`UPDATE routes SET app_pattern = $2, channel_pattern = $3, enabled = $4, updated_at = now() WHERE id = $1`,
			id, mutated.AppPattern, mutated.ChannelPattern, mutated.Enabled); execErr != nil {
			switch pgutil.ErrorCode(execErr) {
			case pgutil.SQLStateUniqueViolation:
				return ErrRouteExists
			case pgutil.SQLStateCheckViolation:
				return CheckViolationError("update route", execErr)
			}
			return pgutil.WrapDBError("update route", execErr)
		}
		if _, execErr := tx.Exec(ctx, `DELETE FROM route_destinations WHERE route_id = $1`, id); execErr != nil {
			return pgutil.WrapDBError("clear route destinations", execErr)
		}
		if linkErr := linkDestinations(ctx, tx, id, destinationIDs); linkErr != nil {
			return linkErr
		}

		reloaded, reloadErr := queryRoute(ctx, tx, id)
		if reloadErr != nil {
			return reloadErr
		}
		updated = reloaded
		return nil
	})
	if txErr != nil {
		return StoredRoute{}, txErr
	}
	return updated, nil
}

// DeleteRoute removes the route (its destination links cascade) and reports
// whether a row existed.
func (store *PGStore) DeleteRoute(ctx context.Context, id int64) (bool, error) {
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM routes WHERE id = $1`, id)
	if execErr != nil {
		return false, pgutil.WrapDBError("delete route", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// ListEnabledRoutes returns the enabled routes ordered by id.
func (store *PGStore) ListEnabledRoutes(ctx context.Context) ([]StoredRoute, error) {
	return queryRoutes(ctx, store.pool, "list enabled routes", " WHERE r.enabled")
}

// ListRoutesByOwner returns the routes owned by app appID, ordered by id.
func (store *PGStore) ListRoutesByOwner(ctx context.Context, appID string) ([]StoredRoute, error) {
	return queryRoutes(ctx, store.pool, "list app routes",
		" WHERE r.owner_kind = 'app' AND r.owner_app_id = $1", appID)
}

// DeleteRouteOwnedBy removes the route only when app appID owns it, in the one statement, and reports
// whether a row was removed.
func (store *PGStore) DeleteRouteOwnedBy(ctx context.Context, id int64, appID string) (bool, error) {
	tag, execErr := store.pool.Exec(ctx,
		`DELETE FROM routes WHERE id = $1 AND owner_kind = 'app' AND owner_app_id = $2`, id, appID)
	if execErr != nil {
		return false, pgutil.WrapDBError("delete app route", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// resolveDestinationIDs maps destination names to ids, share-locking the rows so
// a concurrent delete cannot slip in before the link rows are written. A name
// with no row (a count mismatch against the distinct names) returns
// ErrUnknownDestination, and so does a destination the linking route's
// appPattern may not use (see DestinationUsableBy): the caller cannot tell an
// absent destination from another app's. The SELECT only matches operator-owned
// rows and rows owned by appPattern, so a foreign destination is simply not
// found (a count mismatch) and is never share-locked; DestinationUsableBy stays
// the authoritative check on the returned rows.
func resolveDestinationIDs(ctx context.Context, tx pgx.Tx, names []string, appPattern string) ([]int64, error) {
	distinctNames := pgutil.Distinct(names)
	rows, queryErr := tx.Query(ctx,
		`SELECT id, owner_kind, owner_app_id FROM destinations
WHERE name = ANY($1) AND (owner_kind = 'operator' OR owner_app_id = $2) FOR SHARE`,
		distinctNames, appPattern)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("resolve destinations", queryErr)
	}
	defer rows.Close()

	destinationIDs := make([]int64, 0, len(distinctNames))
	unusable := false
	for rows.Next() {
		var destinationID int64
		var ownerKind string
		var ownerAppID *string
		if scanErr := rows.Scan(&destinationID, &ownerKind, &ownerAppID); scanErr != nil {
			return nil, pgutil.WrapDBError("resolve destinations", scanErr)
		}
		if !DestinationUsableBy(ownerKind, ownerAppID, appPattern) {
			unusable = true
		}
		destinationIDs = append(destinationIDs, destinationID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("resolve destinations", rowsErr)
	}
	if unusable || len(destinationIDs) != len(distinctNames) {
		return nil, ErrUnknownDestination
	}
	return destinationIDs, nil
}

// linkDestinations inserts one route_destinations row per destination id.
func linkDestinations(ctx context.Context, tx pgx.Tx, routeID int64, destinationIDs []int64) error {
	if _, execErr := tx.Exec(ctx,
		`INSERT INTO route_destinations (route_id, destination_id) SELECT $1, unnest($2::bigint[])`,
		routeID, destinationIDs); execErr != nil {
		return pgutil.WrapDBError("link route destinations", execErr)
	}
	return nil
}

// queryRoute loads one route with its destinations, or ErrRouteNotFound.
func queryRoute(ctx context.Context, db pgutil.Querier, id int64) (StoredRoute, error) {
	route, scanErr := scanRoute(db.QueryRow(ctx, routeSelect+` WHERE r.id = $1`+routeGroup, id))
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return StoredRoute{}, ErrRouteNotFound
		}
		return StoredRoute{}, pgutil.WrapDBError("get route", scanErr)
	}
	return route, nil
}

// queryRoutes loads every route matching the optional where clause (leading
// space included, its placeholders bound to args), ordered by id and never nil.
func queryRoutes(
	ctx context.Context, db pgutil.Querier, operation string, where string, args ...any,
) ([]StoredRoute, error) {
	rows, queryErr := db.Query(ctx, routeSelect+where+routeGroup, args...)
	if queryErr != nil {
		return nil, pgutil.WrapDBError(operation, queryErr)
	}
	defer rows.Close()

	routes := []StoredRoute{}
	for rows.Next() {
		route, scanErr := scanRoute(rows)
		if scanErr != nil {
			return nil, pgutil.WrapDBError(operation, scanErr)
		}
		routes = append(routes, route)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError(operation, rowsErr)
	}
	return routes, nil
}

// collectDestinations drains rows into a non-nil slice and closes them.
func collectDestinations(rows pgx.Rows) ([]StoredDestination, error) {
	defer rows.Close()
	destinations := []StoredDestination{}
	for rows.Next() {
		destination, scanErr := scanDestination(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		destinations = append(destinations, destination)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return destinations, nil
}

// scanDestination reads one row selected with destinationColumns.
func scanDestination(row pgutil.RowScanner) (StoredDestination, error) {
	var destination StoredDestination
	if scanErr := row.Scan(
		&destination.ID, &destination.Name, &destination.Type, &destination.Config,
		&destination.SealedSecrets, &destination.Enabled, &destination.DisabledReason,
		&destination.CreatedAt, &destination.UpdatedAt,
		&destination.OwnerKind, &destination.OwnerAppID,
	); scanErr != nil {
		return StoredDestination{}, scanErr
	}
	if destination.Config == nil {
		destination.Config = map[string]string{}
	}
	return destination, nil
}

// scanRoute reads one row selected with routeSelect.
func scanRoute(row pgutil.RowScanner) (StoredRoute, error) {
	var route StoredRoute
	if scanErr := row.Scan(
		&route.ID, &route.AppPattern, &route.ChannelPattern, &route.Enabled,
		&route.CreatedAt, &route.UpdatedAt, &route.OwnerKind, &route.OwnerAppID,
		&route.DestinationIDs, &route.DestinationNames,
	); scanErr != nil {
		return StoredRoute{}, scanErr
	}
	if route.DestinationIDs == nil {
		route.DestinationIDs = []int64{}
	}
	if route.DestinationNames == nil {
		route.DestinationNames = []string{}
	}
	return route, nil
}

// ownerKindOrOperator maps an unset owner kind to the operator, so a caller that
// never names an owner (every admin create) writes an operator-owned row.
func ownerKindOrOperator(ownerKind string) string {
	if ownerKind == "" {
		return OwnerOperator
	}
	return ownerKind
}

// configOrEmpty keeps a nil config from encoding as JSON null in the NOT NULL
// jsonb column.
func configOrEmpty(config map[string]string) map[string]string {
	if config == nil {
		return map[string]string{}
	}
	return config
}

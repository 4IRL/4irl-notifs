package appteardown

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// Deleted counts the rows DeleteOwnedObjects removed, by kind.
type Deleted struct {
	Checks       int64 `json:"checks"`
	Routes       int64 `json:"routes"`
	Destinations int64 `json:"destinations"`
	// OrphanedRoutes and OrphanedChecks count operator-owned routes and checks that lost their last
	// destination link because it pointed at one of the app's destinations. They survive, but deliver
	// nowhere until the operator re-links them.
	OrphanedRoutes int64 `json:"orphaned_routes"`
	OrphanedChecks int64 `json:"orphaned_checks"`
}

// PGStore deletes the delivery objects an app owns.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore builds a store over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// DeleteOwnedObjects removes, in one transaction, every check, route and destination owned by appID
// (owner_kind = 'app' AND owner_app_id = appID). Operator-owned and other apps' rows are untouched.
//
// Order matters: destination_id is ON DELETE RESTRICT on both link tables, so the checks and routes go
// first, then every link row that still points at one of the app's destinations (including links from an
// operator-owned route or check), then the destinations themselves.
//
// Concurrency: the transaction runs at READ COMMITTED. If an admin inserts a new link to one of the app's
// destinations between the link deletes and the destination delete, the RESTRICT foreign key fails the
// transaction: nothing is deleted, the caller sees a 503, and repeating the call is safe.
//
// Held messages are not handled yet: see the hold-and-release-routing plan (teardown follow-up step).
//
// An invalid app id is rejected before the database is touched (second line of defense; the handler
// validates first).
func (store *PGStore) DeleteOwnedObjects(ctx context.Context, appID string) (Deleted, error) {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return Deleted{}, validateErr
	}
	var deleted Deleted
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		checkTag, execErr := tx.Exec(ctx,
			`DELETE FROM heartbeat_checks WHERE owner_kind = 'app' AND owner_app_id = $1`, appID)
		if execErr != nil {
			return pgutil.WrapDBError("delete app checks", execErr)
		}
		routeTag, execErr := tx.Exec(ctx,
			`DELETE FROM routes WHERE owner_kind = 'app' AND owner_app_id = $1`, appID)
		if execErr != nil {
			return pgutil.WrapDBError("delete app routes", execErr)
		}
		orphanedRoutes, orphanErr := deleteLinksCountOrphans(ctx, tx, linkTable{
			links: "route_destinations", linkColumn: "route_id", owners: "routes",
		}, appID)
		if orphanErr != nil {
			return pgutil.WrapDBError("delete route links to app destinations", orphanErr)
		}
		orphanedChecks, orphanErr := deleteLinksCountOrphans(ctx, tx, linkTable{
			links: "heartbeat_check_destinations", linkColumn: "check_id", owners: "heartbeat_checks",
		}, appID)
		if orphanErr != nil {
			return pgutil.WrapDBError("delete check links to app destinations", orphanErr)
		}
		destinationTag, execErr := tx.Exec(ctx,
			`DELETE FROM destinations WHERE owner_kind = 'app' AND owner_app_id = $1`, appID)
		if execErr != nil {
			return pgutil.WrapDBError("delete app destinations", execErr)
		}
		deleted = Deleted{
			Checks: checkTag.RowsAffected(), Routes: routeTag.RowsAffected(), Destinations: destinationTag.RowsAffected(),
			OrphanedRoutes: orphanedRoutes, OrphanedChecks: orphanedChecks,
		}
		return nil
	})
	if txErr != nil {
		return Deleted{}, txErr
	}
	return deleted, nil
}

// linkTable names a link table, its owner-id column and the owner table. The values are package-local
// constants, never caller input, so they are safe to splice into SQL.
type linkTable struct {
	links      string
	linkColumn string
	owners     string
}

// deleteLinksCountOrphans deletes every link row pointing at one of appID's destinations, then counts
// the operator-owned owners (routes or checks) among the affected ones that now have no link left.
func deleteLinksCountOrphans(ctx context.Context, tx pgx.Tx, table linkTable, appID string) (int64, error) {
	rows, queryErr := tx.Query(ctx,
		`DELETE FROM `+table.links+` WHERE destination_id IN
  (SELECT id FROM destinations WHERE owner_kind = 'app' AND owner_app_id = $1)
  RETURNING `+table.linkColumn, appID)
	if queryErr != nil {
		return 0, queryErr
	}
	affectedIDs, collectErr := pgx.CollectRows(rows, pgx.RowTo[int64])
	if collectErr != nil {
		return 0, collectErr
	}
	if len(affectedIDs) == 0 {
		return 0, nil
	}
	var orphaned int64
	scanErr := tx.QueryRow(ctx,
		`SELECT count(*) FROM `+table.owners+` AS owner
WHERE owner.id = ANY($1) AND owner.owner_kind = 'operator'
  AND NOT EXISTS (SELECT 1 FROM `+table.links+` AS link WHERE link.`+table.linkColumn+` = owner.id)`,
		affectedIDs).Scan(&orphaned)
	return orphaned, scanErr
}

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
	// HeldMessages counts the app's held messages (held_at IS NOT NULL) the cascade deleted.
	HeldMessages int64 `json:"held_messages"`
	// PersonalOptIns counts the app's personal destination opt-ins removed. PersonalDestinations counts
	// the personal destinations deleted: the ones only this app used plus any rows orphaned earlier by a
	// previously torn-down registrar that this teardown left with no opt-in.
	PersonalOptIns       int64 `json:"personal_opt_ins"`
	PersonalDestinations int64 `json:"personal_destinations"`
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
// Held messages: the app's rows with held_at IS NOT NULL are deleted first, on the same transaction handle
// as every other delete here (a single pgutil.WithTx call, no separate pool call), so a later error in the
// transaction rolls the held delete back too. Only held_at IS NOT NULL rows are removed: cancelled-held
// rows (held_at cleared) and release-failed rows of the app are deliberately left and age out through the
// normal retention sweep; they are inert since nothing releases them. A new hold after teardown is not
// accepted, because Deprovision unbinds the app and its keys are gone before this runs, so requireAppKey
// rejects the key with 401. The only window is while the upstream phase runs; a hold created then is
// removed here, and a repeated teardown clears anything later.
//
// An invalid app id is rejected before the database is touched (second line of defense; the handler
// validates first).
func (store *PGStore) DeleteOwnedObjects(ctx context.Context, appID string) (Deleted, error) {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return Deleted{}, validateErr
	}
	var deleted Deleted
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		heldTag, execErr := tx.Exec(ctx,
			`DELETE FROM messages WHERE app_id = $1 AND held_at IS NOT NULL`, appID)
		if execErr != nil {
			return pgutil.WrapDBError("delete app held messages", execErr)
		}
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
		personalOptIns, personalDestinations, personalErr := deletePersonalObjects(ctx, tx, appID)
		if personalErr != nil {
			return personalErr
		}
		deleted = Deleted{
			Checks: checkTag.RowsAffected(), Routes: routeTag.RowsAffected(), Destinations: destinationTag.RowsAffected(),
			OrphanedRoutes: orphanedRoutes, OrphanedChecks: orphanedChecks,
			HeldMessages:         heldTag.RowsAffected(),
			PersonalOptIns:       personalOptIns,
			PersonalDestinations: personalDestinations,
		}
		return nil
	})
	if txErr != nil {
		return Deleted{}, txErr
	}
	return deleted, nil
}

// deletePersonalObjects removes the app's personal destination footprint inside the teardown transaction.
// Lock-first (the plan's Reap serialisation rule): the rows holding the app's opt-ins, the rows the app
// registered and every registrar-removed row are locked FOR UPDATE in ascending id order before any opt-in
// is deleted, so a concurrent opt-in PUT (which holds FOR SHARE on its row) either commits first and is seen
// by the re-checks below, or finds the row gone. Then: delete the app's opt-ins; delete the rows it
// registered that now have no opt-ins; mark the rest it registered registrar-removed; finally delete every
// registrar-removed row with no opt-ins (rows orphaned earlier whose last opt-in was this app's). Each
// delete is its own statement, so it re-checks "no opt-ins" against the then-current state.
func deletePersonalObjects(ctx context.Context, tx pgx.Tx, appID string) (optIns int64, destinations int64, err error) {
	if _, lockErr := tx.Exec(ctx,
		`SELECT id FROM personal_destinations pd
WHERE pd.registered_by_app_id = $1 OR pd.registrar_removed_at IS NOT NULL
   OR EXISTS (SELECT 1 FROM personal_destination_opt_ins oi
              WHERE oi.personal_destination_id = pd.id AND oi.app_id = $1)
ORDER BY pd.id FOR UPDATE`, appID); lockErr != nil {
		return 0, 0, pgutil.WrapDBError("lock app personal destinations", lockErr)
	}
	optInTag, execErr := tx.Exec(ctx, `DELETE FROM personal_destination_opt_ins WHERE app_id = $1`, appID)
	if execErr != nil {
		return 0, 0, pgutil.WrapDBError("delete app personal opt-ins", execErr)
	}
	ownedTag, execErr := tx.Exec(ctx,
		`DELETE FROM personal_destinations pd WHERE pd.registered_by_app_id = $1
  AND NOT EXISTS (SELECT 1 FROM personal_destination_opt_ins oi WHERE oi.personal_destination_id = pd.id)`, appID)
	if execErr != nil {
		return 0, 0, pgutil.WrapDBError("delete app personal destinations", execErr)
	}
	// The rows are already locked in id order above, so this UPDATE acquires no new lock.
	if _, execErr = tx.Exec(ctx,
		`UPDATE personal_destinations SET registrar_removed_at = now()
WHERE registered_by_app_id = $1 AND registrar_removed_at IS NULL`, appID); execErr != nil {
		return 0, 0, pgutil.WrapDBError("mark personal destinations registrar-removed", execErr)
	}
	orphanTag, execErr := tx.Exec(ctx,
		`DELETE FROM personal_destinations pd WHERE pd.registrar_removed_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM personal_destination_opt_ins oi WHERE oi.personal_destination_id = pd.id)`)
	if execErr != nil {
		return 0, 0, pgutil.WrapDBError("delete orphaned personal destinations", execErr)
	}
	return optInTag.RowsAffected(), ownedTag.RowsAffected() + orphanTag.RowsAffected(), nil
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

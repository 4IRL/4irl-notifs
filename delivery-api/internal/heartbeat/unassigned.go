package heartbeat

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// undeliverableCheckPredicate selects checks (aliased c) with a pending alert
// and no deliverable destination: no link to a destination that is enabled AND
// of a supported type. It is the SQL form of Deliverable (keep them in step) and
// the unassigned checks listing reuses it. $1 is the supported-types text[].
const undeliverableCheckPredicate = `c.pending_alert IS NOT NULL AND NOT EXISTS (
	SELECT 1 FROM heartbeat_check_destinations cd
	JOIN destinations d ON d.id = cd.destination_id
	WHERE cd.check_id = c.id AND d.enabled AND d.type = ANY($1::text[]))`

const countUnassignedAlertsSQL = `SELECT count(*) FROM heartbeat_checks c WHERE ` + undeliverableCheckPredicate

const listUnassignedAlertsSQL = `SELECT c.id, c.app_id, c.name, c.pending_alert FROM heartbeat_checks c WHERE ` +
	undeliverableCheckPredicate + ` ORDER BY c.app_id COLLATE "C", c.name COLLATE "C", c.id LIMIT $2`

// readSnapshotOptions gives the list and its total one consistent snapshot.
var readSnapshotOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// UnassignedCheck is one heartbeat check whose pending alert has nowhere to go.
type UnassignedCheck struct {
	CheckID      int64
	AppID        string
	Name         string
	PendingAlert string
}

// ListUnassignedAlerts returns up to limit checks matching undeliverableCheckPredicate
// (ordered by app, name, id) and the uncapped total of the same predicate, read in
// one snapshot so total >= len(list). Read-only and deliberately not on the Store
// interface. A limit below 1 is a programmer error and fails before any database access.
func (store *PGStore) ListUnassignedAlerts(ctx context.Context, limit int) ([]UnassignedCheck, int, error) {
	if limit < 1 {
		return nil, 0, errors.New("list unassigned alerts: limit must be positive")
	}
	checks := []UnassignedCheck{}
	var total int
	txErr := pgutil.WithTx(ctx, store.pool, readSnapshotOptions, func(tx pgx.Tx) error {
		checks = []UnassignedCheck{}
		if countErr := tx.QueryRow(ctx, countUnassignedAlertsSQL, store.supportedTypes).Scan(&total); countErr != nil {
			return pgutil.WrapDBError("count unassigned alerts", countErr)
		}
		rows, queryErr := tx.Query(ctx, listUnassignedAlertsSQL, store.supportedTypes, limit)
		if queryErr != nil {
			return pgutil.WrapDBError("list unassigned alerts", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var check UnassignedCheck
			if scanErr := rows.Scan(&check.CheckID, &check.AppID, &check.Name, &check.PendingAlert); scanErr != nil {
				return pgutil.WrapDBError("list unassigned alerts", scanErr)
			}
			checks = append(checks, check)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return pgutil.WrapDBError("list unassigned alerts", rowsErr)
		}
		return nil
	})
	if txErr != nil {
		return nil, 0, txErr
	}
	return checks, total, nil
}

// CountUnassignedAlerts counts checks with a pending alert and no deliverable
// destination. It is read-only and deliberately not on the Store interface.
func (store *PGStore) CountUnassignedAlerts(ctx context.Context) (int, error) {
	var count int
	// supportedTypes is never nil (supportedTypeList), so $1 is an empty array, not NULL.
	if scanErr := store.pool.QueryRow(ctx, countUnassignedAlertsSQL, store.supportedTypes).Scan(&count); scanErr != nil {
		return 0, pgutil.WrapDBError("count unassigned alerts", scanErr)
	}
	return count, nil
}

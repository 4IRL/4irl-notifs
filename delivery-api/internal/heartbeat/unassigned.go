package heartbeat

import (
	"context"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// undeliverableCheckPredicate selects checks (aliased c) with a pending alert
// and no deliverable destination: no link to a destination that is enabled AND
// of a supported type. It is the SQL form of Deliverable (keep them in step) and
// Step 6's unassigned_checks reuses it. $1 is the supported-types text[].
const undeliverableCheckPredicate = `c.pending_alert IS NOT NULL AND NOT EXISTS (
	SELECT 1 FROM heartbeat_check_destinations cd
	JOIN destinations d ON d.id = cd.destination_id
	WHERE cd.check_id = c.id AND d.enabled AND d.type = ANY($1::text[]))`

const countUnassignedAlertsSQL = `SELECT count(*) FROM heartbeat_checks c WHERE ` + undeliverableCheckPredicate

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

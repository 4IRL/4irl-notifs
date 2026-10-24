package heartbeat

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	// checkColumns are the heartbeat_checks columns, aliased as c, in the order
	// checkScanTargets scans them.
	checkColumns = `c.id, c.app_id, c.name, c.period_seconds, c.grace_seconds, c.state, c.paused, c.anchor_at,
	c.last_ping_at, c.last_ping_kind, c.down_since, c.down_reason, c.alert_seq, c.pending_alert,
	c.created_at, c.updated_at, c.owner_kind, c.owner_app_id`

	// checkSelect and checkGroup bracket a WHERE clause: one row per check with
	// its destination names, types and enabled flags aggregated in name order, so
	// the three arrays line up. Names sort bytewise (COLLATE "C") so ordering
	// never depends on the database's locale: names are [a-z0-9_] slugs, and
	// glibc locales ignore underscores when comparing.
	//
	// The probe row is a 1:1 LEFT JOIN (all NULL for a push check); its columns are functionally
	// dependent on p.check_id, so grouping by it lets them be selected ungrouped. This read is unlocked
	// (Get/List/reload); locked reads never join the probe row (see lockCheck).
	checkSelect = `SELECT ` + checkColumns + `,
	COALESCE(array_agg(d.name ORDER BY d.name COLLATE "C") FILTER (WHERE d.id IS NOT NULL), '{}'::text[]),
	COALESCE(array_agg(d.type ORDER BY d.name COLLATE "C") FILTER (WHERE d.id IS NOT NULL), '{}'::text[]),
	COALESCE(array_agg(d.enabled ORDER BY d.name COLLATE "C") FILTER (WHERE d.id IS NOT NULL), '{}'::boolean[]),
	` + probeColumns + `
FROM heartbeat_checks c
LEFT JOIN heartbeat_check_destinations cd ON cd.check_id = c.id
LEFT JOIN destinations d ON d.id = cd.destination_id
LEFT JOIN heartbeat_check_probes p ON p.check_id = c.id`
	checkGroup = ` GROUP BY c.id, p.check_id`

	orderByName = ` ORDER BY c.app_id COLLATE "C", c.name COLLATE "C"`
	orderByID   = ` ORDER BY c.id`

	// dueWhere selects the checks the checker tick may take down: not already
	// down, not paused and strictly past anchor_at + period + grace. $1 is now.
	dueWhere = `c.state <> 'down' AND NOT c.paused
AND c.anchor_at + (c.period_seconds + c.grace_seconds) * interval '1 second' < $1`
)

// PGStore is the Postgres-backed Store. It is covered by the integration tests,
// not unit tests.
type PGStore struct {
	pool   *pgxpool.Pool
	sealer *seal.Sealer
}

// Compile-time check that PGStore satisfies Store.
var _ Store = (*PGStore)(nil)

// NewPGStore returns a PGStore over pool. sealer seals a probe check's Access credentials; it may be
// nil for push-only use, in which case sealing a token fails closed.
func NewPGStore(pool *pgxpool.Pool, sealer *seal.Sealer) *PGStore {
	return &PGStore{pool: pool, sealer: sealer}
}

// Create implements Store.
func (store *PGStore) Create(ctx context.Context, check NewCheck, now time.Time) (StoredCheck, error) {
	now = storeTime(now)
	var created StoredCheck
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		destinationIDs, resolveErr := resolveDestinationIDs(ctx, tx, check.DestinationNames, check.AppID)
		if resolveErr != nil {
			return resolveErr
		}

		if check.Probe == nil && check.AccessToken != nil {
			return &ValidationError{Message: "probe config is not allowed on a push check"}
		}
		if check.Probe != nil {
			// A probe check's schedule is derived from its interval, whatever the caller passed.
			check.PeriodSeconds, check.GraceSeconds = DerivedSchedule(check.Probe.IntervalSeconds)
		}

		var checkID int64
		if scanErr := tx.QueryRow(ctx,
			`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at, created_at, updated_at, owner_kind, owner_app_id)
VALUES ($1, $2, $3, $4, $5, $5, $5, $6, $7) RETURNING id`,
			check.AppID, check.Name, check.PeriodSeconds, check.GraceSeconds, now,
			ownerKindOrOperator(check.OwnerKind), check.OwnerAppID).Scan(&checkID); scanErr != nil {
			switch pgutil.ErrorCode(scanErr) {
			case pgutil.SQLStateUniqueViolation:
				return ErrCheckExists
			case pgutil.SQLStateCheckViolation:
				// A CHECK the database enforces (an app-owned check's app_id must be its owner's id,
				// say) is a request the client can correct, not a server fault.
				return registry.CheckViolationError("insert heartbeat check", scanErr)
			}
			return pgutil.WrapDBError("insert heartbeat check", scanErr)
		}
		if linkErr := linkDestinations(ctx, tx, checkID, destinationIDs); linkErr != nil {
			return linkErr
		}
		if check.Probe != nil {
			if probeErr := insertProbeRow(ctx, tx, store.sealer, checkID, check, now); probeErr != nil {
				return probeErr
			}
		}

		loaded, loadErr := queryCheckByID(ctx, tx, checkID)
		if loadErr != nil {
			return loadErr
		}
		created = loaded
		return nil
	})
	if txErr != nil {
		return StoredCheck{}, txErr
	}
	return created, nil
}

// Get implements Store.
func (store *PGStore) Get(ctx context.Context, id int64) (StoredCheck, error) {
	return queryCheckByID(ctx, store.pool, id)
}

// List implements Store.
func (store *PGStore) List(ctx context.Context) ([]StoredCheck, error) {
	return queryChecks(ctx, store.pool, "list heartbeat checks", "", orderByName)
}

// Patch implements Store. The check is row-locked before it is read, so the merge
// sees the latest committed values and a concurrent Patch waits for
// this one instead of overwriting fields it never named.
func (store *PGStore) Patch(ctx context.Context, id int64, patch CheckPatch, now time.Time) (StoredCheck, error) {
	return store.patch(ctx, id, patch, now, func(tx pgx.Tx) (StoredCheck, error) {
		return lockCheckByID(ctx, tx, id)
	})
}

// PatchOwnedBy implements Store.
func (store *PGStore) PatchOwnedBy(
	ctx context.Context, id int64, appID string, patch CheckPatch, now time.Time,
) (StoredCheck, error) {
	return store.patch(ctx, id, patch, now, func(tx pgx.Tx) (StoredCheck, error) {
		return lockOwnedCheckByID(ctx, tx, id, appID)
	})
}

// checkLocker row-locks the one check a write is about and returns its stored columns. A check the write
// may not touch (absent, or not owned by the caller) is ErrCheckNotFound.
type checkLocker func(tx pgx.Tx) (StoredCheck, error)

// patch is Patch for the check lock locks.
func (store *PGStore) patch(
	ctx context.Context, id int64, patch CheckPatch, now time.Time, lock checkLocker,
) (StoredCheck, error) {
	now = storeTime(now)
	var patched StoredCheck
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, lockErr := lock(tx); lockErr != nil {
			return lockErr
		}
		current, loadErr := queryCheckByID(ctx, tx, id)
		if loadErr != nil {
			return loadErr
		}
		update := ApplyPatch(current, patch)
		if validateErr := ValidateCheckUpdate(update); validateErr != nil {
			return validateErr
		}
		replaced, replaceErr := replaceSchedule(ctx, tx, current.AppID, id, update, now)
		if replaceErr != nil {
			return replaceErr
		}
		patched = replaced
		return nil
	})
	if txErr != nil {
		return StoredCheck{}, txErr
	}
	return patched, nil
}

// replaceSchedule writes update's period, grace and destination set to the check
// the caller already holds locked, and returns the reloaded check.
func replaceSchedule(
	ctx context.Context, tx pgx.Tx, appID string, id int64, update CheckUpdate, now time.Time,
) (StoredCheck, error) {
	destinationIDs, resolveErr := resolveDestinationIDs(ctx, tx, update.DestinationNames, appID)
	if resolveErr != nil {
		return StoredCheck{}, resolveErr
	}
	if _, execErr := tx.Exec(ctx,
		`UPDATE heartbeat_checks SET period_seconds = $2, grace_seconds = $3, updated_at = $4 WHERE id = $1`,
		id, update.PeriodSeconds, update.GraceSeconds, now); execErr != nil {
		return StoredCheck{}, pgutil.WrapDBError("update heartbeat check", execErr)
	}
	if _, execErr := tx.Exec(ctx,
		`DELETE FROM heartbeat_check_destinations WHERE check_id = $1`, id); execErr != nil {
		return StoredCheck{}, pgutil.WrapDBError("clear heartbeat check destinations", execErr)
	}
	if linkErr := linkDestinations(ctx, tx, id, destinationIDs); linkErr != nil {
		return StoredCheck{}, linkErr
	}
	return queryCheckByID(ctx, tx, id)
}

// Delete implements Store.
func (store *PGStore) Delete(ctx context.Context, id int64) (bool, error) {
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM heartbeat_checks WHERE id = $1`, id)
	if execErr != nil {
		return false, pgutil.WrapDBError("delete heartbeat check", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteOwnedBy implements Store: the owner is part of the one DELETE statement.
func (store *PGStore) DeleteOwnedBy(ctx context.Context, id int64, appID string) (bool, error) {
	tag, execErr := store.pool.Exec(ctx,
		`DELETE FROM heartbeat_checks WHERE id = $1 AND owner_kind = 'app' AND owner_app_id = $2`, id, appID)
	if execErr != nil {
		return false, pgutil.WrapDBError("delete app heartbeat check", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// ListByOwner implements Store.
func (store *PGStore) ListByOwner(ctx context.Context, appID string) ([]StoredCheck, error) {
	return queryChecks(ctx, store.pool, "list app heartbeat checks",
		" WHERE c.owner_kind = 'app' AND c.owner_app_id = $1", orderByName, appID)
}

// Pause implements Store.
func (store *PGStore) Pause(ctx context.Context, id int64, now time.Time) (StoredCheck, error) {
	now = storeTime(now)
	return store.mutate(ctx, now, func(tx pgx.Tx) (StoredCheck, error) {
		return lockCheckByID(ctx, tx, id)
	}, ApplyPause)
}

// PauseOwnedBy implements Store.
func (store *PGStore) PauseOwnedBy(ctx context.Context, id int64, appID string, now time.Time) (StoredCheck, error) {
	now = storeTime(now)
	return store.mutate(ctx, now, func(tx pgx.Tx) (StoredCheck, error) {
		return lockOwnedCheckByID(ctx, tx, id, appID)
	}, ApplyPause)
}

// Resume implements Store.
func (store *PGStore) Resume(ctx context.Context, id int64, now time.Time) (StoredCheck, error) {
	now = storeTime(now)
	return store.mutate(ctx, now, func(tx pgx.Tx) (StoredCheck, error) {
		return lockCheckByID(ctx, tx, id)
	}, func(current StoredCheck) StoredCheck {
		return ApplyResume(current, now)
	})
}

// ResumeOwnedBy implements Store.
func (store *PGStore) ResumeOwnedBy(ctx context.Context, id int64, appID string, now time.Time) (StoredCheck, error) {
	now = storeTime(now)
	return store.mutate(ctx, now, func(tx pgx.Tx) (StoredCheck, error) {
		return lockOwnedCheckByID(ctx, tx, id, appID)
	}, func(current StoredCheck) StoredCheck {
		return ApplyResume(current, now)
	})
}

// RecordPing implements Store.
func (store *PGStore) RecordPing(
	ctx context.Context, appID string, name string, kind string, now time.Time,
) (StoredCheck, error) {
	if validateErr := ValidatePingKind(kind); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	now = storeTime(now)
	var recorded StoredCheck
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, lockErr := lockCheck(ctx, tx, `c.app_id = $1 AND c.name = $2`, appID, name)
		if lockErr != nil {
			return lockErr
		}
		if current.Probe != nil {
			return ErrProbeCheckPing
		}
		next, _ := ApplyPing(current, kind, now)
		reloaded, saveErr := saveCheck(ctx, tx, next, current, now)
		if saveErr != nil {
			return saveErr
		}
		recorded = reloaded
		return nil
	})
	if txErr != nil {
		return StoredCheck{}, txErr
	}
	return recorded, nil
}

// AdvanceDue implements Store. The due rows are selected FOR UPDATE, so a ping
// racing the tick either commits first (the row is no longer due once the lock
// is released and the filter is re-evaluated) or waits for the tick (and then
// moves the check from down to up); two concurrent ticks serialize the same way.
func (store *PGStore) AdvanceDue(ctx context.Context, now time.Time) ([]StoredCheck, error) {
	now = storeTime(now)
	advanced := []StoredCheck{}
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		due, lockErr := lockChecks(ctx, tx, dueWhere, now)
		if lockErr != nil {
			return lockErr
		}
		changedIDs := make([]int64, 0, len(due))
		for _, current := range due {
			next, changed := AdvanceTick(current, now)
			if !changed {
				continue
			}
			if _, saveErr := saveCheck(ctx, tx, next, current, now); saveErr != nil {
				return saveErr
			}
			changedIDs = append(changedIDs, next.ID)
		}
		if len(changedIDs) == 0 {
			return nil
		}
		loaded, loadErr := queryChecks(ctx, tx, "reload advanced heartbeat checks", " WHERE c.id = ANY($1)", orderByID, changedIDs)
		if loadErr != nil {
			return loadErr
		}
		advanced = loaded
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return advanced, nil
}

// ListPendingAlerts implements Store.
func (store *PGStore) ListPendingAlerts(ctx context.Context) ([]StoredCheck, error) {
	return queryChecks(ctx, store.pool, "list pending heartbeat alerts", " WHERE c.pending_alert IS NOT NULL", orderByID)
}

// ClearPendingAlert implements Store.
func (store *PGStore) ClearPendingAlert(ctx context.Context, id int64, alertSeq int) (bool, error) {
	tag, execErr := store.pool.Exec(ctx,
		`UPDATE heartbeat_checks SET pending_alert = NULL WHERE id = $1 AND alert_seq = $2 AND pending_alert IS NOT NULL`,
		id, alertSeq)
	if execErr != nil {
		return false, pgutil.WrapDBError("clear heartbeat pending alert", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// mutate locks the check with lock, applies mutate (a pure state-machine function) and
// persists the result in one transaction.
func (store *PGStore) mutate(
	ctx context.Context, now time.Time, lock checkLocker, mutate func(StoredCheck) StoredCheck,
) (StoredCheck, error) {
	var saved StoredCheck
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, lockErr := lock(tx)
		if lockErr != nil {
			return lockErr
		}
		reloaded, saveErr := saveCheck(ctx, tx, mutate(current), current, now)
		if saveErr != nil {
			return saveErr
		}
		saved = reloaded
		return nil
	})
	if txErr != nil {
		return StoredCheck{}, txErr
	}
	return saved, nil
}

// saveCheck writes the state-machine fields of check (the schedule and the
// destination set are untouched) and returns the reloaded check. locked is the
// check as read under the lock: for a probe check it is the baseline the probe
// row's next_probe_at is compared against.
func saveCheck(ctx context.Context, tx pgx.Tx, check StoredCheck, locked StoredCheck, now time.Time) (StoredCheck, error) {
	if _, execErr := tx.Exec(ctx,
		`UPDATE heartbeat_checks
SET state = $2, paused = $3, anchor_at = $4, last_ping_at = $5, last_ping_kind = $6,
	down_since = $7, down_reason = $8, alert_seq = $9, pending_alert = $10, updated_at = $11
WHERE id = $1`,
		check.ID, check.State, check.Paused, check.AnchorAt, check.LastPingAt, check.LastPingKind,
		check.DownSince, check.DownReason, check.AlertSeq, check.PendingAlert, now); execErr != nil {
		return StoredCheck{}, pgutil.WrapDBError("save heartbeat check", execErr)
	}
	if check.Probe != nil {
		if probeErr := writeProbeRow(ctx, tx, check, lockedNextProbeAt(locked), sealedAccessChange{}); probeErr != nil {
			return StoredCheck{}, probeErr
		}
	}
	return queryCheckByID(ctx, tx, check.ID)
}

// lockCheckByID row-locks the check, or returns ErrCheckNotFound.
func lockCheckByID(ctx context.Context, tx pgx.Tx, id int64) (StoredCheck, error) {
	return lockCheck(ctx, tx, `c.id = $1`, id)
}

// lockOwnedCheckByID row-locks the check only when the app appID owns it, so the owner is checked on the
// locked row itself. Any other check, or an absent one, is ErrCheckNotFound.
func lockOwnedCheckByID(ctx context.Context, tx pgx.Tx, id int64, appID string) (StoredCheck, error) {
	return lockCheck(ctx, tx, `c.id = $1 AND c.owner_kind = 'app' AND c.owner_app_id = $2`, id, appID)
}

// lockCheck row-locks the one check matching where (FOR UPDATE) and returns its
// stored columns; the destinations are not loaded. A missing row returns
// ErrCheckNotFound.
func lockCheck(ctx context.Context, tx pgx.Tx, where string, args ...any) (StoredCheck, error) {
	var check StoredCheck
	scanErr := tx.QueryRow(ctx,
		`SELECT `+checkColumns+` FROM heartbeat_checks c WHERE `+where+` FOR UPDATE`, args...,
	).Scan(checkScanTargets(&check)...)
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return StoredCheck{}, ErrCheckNotFound
		}
		return StoredCheck{}, pgutil.WrapDBError("lock heartbeat check", scanErr)
	}
	normalizeTimes(&check)
	// The probe row is read in a second statement, after the parent lock was granted, so a writer
	// that waited sees the committed probe state rather than a pre-wait join snapshot. Lock order is
	// always parent then probe row.
	probes, probeErr := lockProbeRows(ctx, tx, []int64{check.ID})
	if probeErr != nil {
		return StoredCheck{}, probeErr
	}
	check.Probe = probes[check.ID]
	return check, nil
}

// lockChecks row-locks every check matching where (FOR UPDATE, ordered by id so
// concurrent callers lock in the same order) and returns their stored columns;
// the destinations are not loaded.
func lockChecks(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]StoredCheck, error) {
	rows, queryErr := tx.Query(ctx,
		`SELECT `+checkColumns+` FROM heartbeat_checks c WHERE `+where+` ORDER BY c.id FOR UPDATE`, args...)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("lock heartbeat checks", queryErr)
	}
	defer rows.Close()

	checks := []StoredCheck{}
	for rows.Next() {
		var check StoredCheck
		if scanErr := rows.Scan(checkScanTargets(&check)...); scanErr != nil {
			return nil, pgutil.WrapDBError("lock heartbeat checks", scanErr)
		}
		normalizeTimes(&check)
		checks = append(checks, check)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("lock heartbeat checks", rowsErr)
	}
	rows.Close()

	// Probe rows are locked in a second statement, after every parent lock (see lockCheck).
	checkIDs := make([]int64, 0, len(checks))
	for _, check := range checks {
		checkIDs = append(checkIDs, check.ID)
	}
	probes, probeErr := lockProbeRows(ctx, tx, checkIDs)
	if probeErr != nil {
		return nil, probeErr
	}
	for index := range checks {
		checks[index].Probe = probes[checks[index].ID]
	}
	return checks, nil
}

// resolveDestinationIDs maps destination names to ids, share-locking the rows so
// a concurrent delete cannot slip in before the link rows are written. A name
// with no row (a count mismatch against the distinct names) returns
// registry.ErrUnknownDestination, and so does a destination the check's appID may
// not use (see registry.DestinationUsableBy): the caller cannot tell an absent
// destination from another app's. The SELECT only matches operator-owned rows
// and rows owned by appID, so a foreign destination is simply not found (a count
// mismatch) and is never share-locked; registry.DestinationUsableBy stays the
// authoritative check on the returned rows.
func resolveDestinationIDs(ctx context.Context, tx pgx.Tx, names []string, appID string) ([]int64, error) {
	distinctNames := pgutil.Distinct(names)
	// Fail closed even if the caller skipped ValidateDestinations: a check with no
	// destinations would go down without ever alerting anywhere.
	if len(distinctNames) < MinDestinations || len(distinctNames) > MaxDestinations {
		return nil, &ValidationError{Message: "invalid destinations"}
	}
	rows, queryErr := tx.Query(ctx,
		`SELECT id, owner_kind, owner_app_id FROM destinations
WHERE name = ANY($1) AND (owner_kind = 'operator' OR owner_app_id = $2) ORDER BY id FOR SHARE`,
		distinctNames, appID)
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
		if !registry.DestinationUsableBy(ownerKind, ownerAppID, appID) {
			unusable = true
		}
		destinationIDs = append(destinationIDs, destinationID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("resolve destinations", rowsErr)
	}
	if unusable || len(destinationIDs) != len(distinctNames) {
		return nil, registry.ErrUnknownDestination
	}
	return destinationIDs, nil
}

// linkDestinations inserts one heartbeat_check_destinations row per destination id.
func linkDestinations(ctx context.Context, tx pgx.Tx, checkID int64, destinationIDs []int64) error {
	if _, execErr := tx.Exec(ctx,
		`INSERT INTO heartbeat_check_destinations (check_id, destination_id) SELECT $1, unnest($2::bigint[])`,
		checkID, destinationIDs); execErr != nil {
		return pgutil.WrapDBError("link heartbeat check destinations", execErr)
	}
	return nil
}

// queryCheckByID loads one check with its destinations, or ErrCheckNotFound.
func queryCheckByID(ctx context.Context, db pgutil.Querier, id int64) (StoredCheck, error) {
	checks, queryErr := queryChecks(ctx, db, "get heartbeat check", " WHERE c.id = $1", orderByID, id)
	if queryErr != nil {
		return StoredCheck{}, queryErr
	}
	if len(checks) == 0 {
		return StoredCheck{}, ErrCheckNotFound
	}
	return checks[0], nil
}

// queryChecks loads every check matching the optional where clause (leading
// space included) in the given order, never nil.
func queryChecks(
	ctx context.Context, db pgutil.Querier, operation string, where string, order string, args ...any,
) ([]StoredCheck, error) {
	rows, queryErr := db.Query(ctx, checkSelect+where+checkGroup+order, args...)
	if queryErr != nil {
		return nil, pgutil.WrapDBError(operation, queryErr)
	}
	defer rows.Close()

	checks := []StoredCheck{}
	for rows.Next() {
		check, scanErr := scanCheck(rows)
		if scanErr != nil {
			return nil, pgutil.WrapDBError(operation, scanErr)
		}
		checks = append(checks, check)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError(operation, rowsErr)
	}
	return checks, nil
}

// checkScanTargets returns the scan destinations for checkColumns, in order.
func checkScanTargets(check *StoredCheck) []any {
	return []any{
		&check.ID, &check.AppID, &check.Name, &check.PeriodSeconds, &check.GraceSeconds,
		&check.State, &check.Paused, &check.AnchorAt, &check.LastPingAt, &check.LastPingKind,
		&check.DownSince, &check.DownReason, &check.AlertSeq, &check.PendingAlert,
		&check.CreatedAt, &check.UpdatedAt, &check.OwnerKind, &check.OwnerAppID,
	}
}

// ownerKindOrOperator maps an unset owner kind to the operator, so a caller that
// never names an owner (every admin create) writes an operator-owned row.
func ownerKindOrOperator(ownerKind string) string {
	if ownerKind == "" {
		return registry.OwnerOperator
	}
	return ownerKind
}

// scanCheck reads one row selected with checkSelect.
func scanCheck(row pgutil.RowScanner) (StoredCheck, error) {
	var check StoredCheck
	var names, types []string
	var enabled []bool
	var probe probeRow
	targets := append(checkScanTargets(&check), &names, &types, &enabled)
	targets = append(targets, probe.targets()...)
	if scanErr := row.Scan(targets...); scanErr != nil {
		return StoredCheck{}, scanErr
	}
	normalizeTimes(&check)
	check.Probe = probe.state()
	check.Destinations = make([]DestinationRef, 0, len(names))
	for index, name := range names {
		check.Destinations = append(check.Destinations, DestinationRef{
			Name: name, Type: types[index], Enabled: enabled[index],
		})
	}
	return check, nil
}

// normalizeTimes converts every scanned timestamp to UTC (pgx returns the
// process's local zone), so RFC 3339 output is deterministic.
func normalizeTimes(check *StoredCheck) {
	check.AnchorAt = check.AnchorAt.UTC()
	check.CreatedAt = check.CreatedAt.UTC()
	check.UpdatedAt = check.UpdatedAt.UTC()
	for _, field := range []**time.Time{&check.LastPingAt, &check.DownSince} {
		if *field != nil {
			utc := (*field).UTC()
			*field = &utc
		}
	}
}

// storeTime truncates now to Postgres's microsecond timestamp precision, so a
// time written and read back compares equal and the SQL due filter agrees with
// the state machine's deadline comparison.
func storeTime(now time.Time) time.Time {
	return now.UTC().Truncate(time.Microsecond)
}

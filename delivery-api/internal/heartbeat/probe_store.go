package heartbeat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

// probeColumns are the heartbeat_check_probes columns, aliased as p, in the order probeRow scans
// them. The sealed bytes are never selected: the loaded state carries only whether a token exists.
const probeColumns = `p.check_id, p.url, p.expected_statuses, p.body_match, p.timeout_seconds, p.interval_seconds,
	p.consecutive_failures, p.next_probe_at, p.config_version, p.last_result_at, p.last_result_status,
	p.last_result_reason, p.last_result_detail, p.down_detail, p.sealed_access_headers IS NOT NULL`

// probeRow scans probeColumns. Every column is a pointer or slice so the all-NULL row of a LEFT JOIN
// (a push check) scans cleanly; checkID nil means there is no probe row.
type probeRow struct {
	checkID             *int64
	url                 *string
	expectedStatuses    []int
	bodyMatch           *string
	timeoutSeconds      *int
	intervalSeconds     *int
	consecutiveFailures *int
	nextProbeAt         *time.Time
	configVersion       *int64
	lastResultAt        *time.Time
	lastResultStatus    *int
	lastResultReason    *string
	lastResultDetail    *string
	downDetail          *string
	hasAccessToken      *bool
}

func (row *probeRow) targets() []any {
	return []any{
		&row.checkID, &row.url, &row.expectedStatuses, &row.bodyMatch, &row.timeoutSeconds,
		&row.intervalSeconds, &row.consecutiveFailures, &row.nextProbeAt, &row.configVersion,
		&row.lastResultAt, &row.lastResultStatus, &row.lastResultReason, &row.lastResultDetail,
		&row.downDetail, &row.hasAccessToken,
	}
}

// state builds the ProbeState, or nil when the row is absent. Times are normalised to UTC.
func (row *probeRow) state() *ProbeState {
	if row.checkID == nil {
		return nil
	}
	state := &ProbeState{
		Config: ProbeConfig{
			URL:              *row.url,
			ExpectedStatuses: row.expectedStatuses,
			BodyMatch:        row.bodyMatch,
			TimeoutSeconds:   *row.timeoutSeconds,
			IntervalSeconds:  *row.intervalSeconds,
		},
		HasAccessToken:      row.hasAccessToken != nil && *row.hasAccessToken,
		ConsecutiveFailures: *row.consecutiveFailures,
		NextProbeAt:         row.nextProbeAt.UTC(),
		ConfigVersion:       *row.configVersion,
		DownDetail:          row.downDetail,
	}
	if row.lastResultAt != nil {
		state.LastResult = &ProbeResultRecord{
			At:     row.lastResultAt.UTC(),
			Status: row.lastResultStatus,
			Reason: row.lastResultReason,
			Detail: row.lastResultDetail,
		}
	}
	return state
}

// lockProbeRows reads and locks (FOR UPDATE) the probe rows of the given checks, keyed by check id.
// It runs as its own statement after the parents are locked, so the rows are read after any lock
// wait and are never a stale join snapshot.
func lockProbeRows(ctx context.Context, tx pgx.Tx, checkIDs []int64) (map[int64]*ProbeState, error) {
	probes := make(map[int64]*ProbeState, len(checkIDs))
	if len(checkIDs) == 0 {
		return probes, nil
	}
	rows, queryErr := tx.Query(ctx,
		`SELECT `+probeColumns+` FROM heartbeat_check_probes p WHERE p.check_id = ANY($1) ORDER BY p.check_id FOR UPDATE`,
		checkIDs)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("lock heartbeat probes", queryErr)
	}
	defer rows.Close()
	for rows.Next() {
		var row probeRow
		if scanErr := rows.Scan(row.targets()...); scanErr != nil {
			return nil, pgutil.WrapDBError("lock heartbeat probes", scanErr)
		}
		probes[*row.checkID] = row.state()
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("lock heartbeat probes", rowsErr)
	}
	return probes, nil
}

// sealedAccessChange says what a probe-row write does to sealed_access_headers. The zero value
// leaves the column alone; Changed with nil Sealed clears it.
type sealedAccessChange struct {
	Changed bool
	Sealed  []byte
}

// writeProbeRow is the one save-path UPDATE of heartbeat_checks' probe row, shared by saveCheck and
// the patch path. next_probe_at is written only when the check's value differs from baseline (the
// value read under the lock); sealed_access_headers only when access.Changed.
func writeProbeRow(
	ctx context.Context, tx pgx.Tx, check StoredCheck, baselineNextProbeAt *time.Time, access sealedAccessChange,
) error {
	probe := check.Probe
	writeNextProbeAt := baselineNextProbeAt == nil || !probe.NextProbeAt.Equal(*baselineNextProbeAt)
	var lastAt *time.Time
	var lastStatus *int
	var lastReason, lastDetail *string
	if probe.LastResult != nil {
		at := probe.LastResult.At
		lastAt = &at
		lastStatus, lastReason, lastDetail = probe.LastResult.Status, probe.LastResult.Reason, probe.LastResult.Detail
	}
	if _, execErr := tx.Exec(ctx,
		`UPDATE heartbeat_check_probes SET
	url = $2, expected_statuses = $3, body_match = $4, timeout_seconds = $5, interval_seconds = $6,
	consecutive_failures = $7, config_version = $8, last_result_at = $9, last_result_status = $10,
	last_result_reason = $11, last_result_detail = $12, down_detail = $13,
	next_probe_at = CASE WHEN $14::boolean THEN $15::timestamptz ELSE next_probe_at END,
	sealed_access_headers = CASE WHEN $16::boolean THEN $17::bytea ELSE sealed_access_headers END
WHERE check_id = $1`,
		check.ID, probe.Config.URL, NormalizeStatuses(probe.Config.ExpectedStatuses), probe.Config.BodyMatch,
		probe.Config.TimeoutSeconds, probe.Config.IntervalSeconds, probe.ConsecutiveFailures, probe.ConfigVersion,
		lastAt, lastStatus, lastReason, lastDetail, probe.DownDetail,
		writeNextProbeAt, storeTime(probe.NextProbeAt), access.Changed, access.Sealed); execErr != nil {
		return probeWriteError("save heartbeat probe", execErr)
	}
	return nil
}

// insertProbeRow inserts the probe row of a new check, sealing its token (if any) with the check id.
// There is no baseline on create, so next_probe_at is always written.
func insertProbeRow(
	ctx context.Context, tx pgx.Tx, sealer *seal.Sealer, checkID int64, check NewCheck, now time.Time,
) error {
	probe := check.Probe
	var sealed []byte
	if check.AccessToken != nil {
		sealedToken, sealErr := sealProbeAccessToken(sealer, checkID, *check.AccessToken)
		if sealErr != nil {
			return fmt.Errorf("seal heartbeat probe access token: %w", sealErr)
		}
		sealed = sealedToken
	}
	nextProbeAt := now.Add(time.Duration(probe.IntervalSeconds) * time.Second)
	if _, execErr := tx.Exec(ctx,
		`INSERT INTO heartbeat_check_probes
	(check_id, url, expected_statuses, body_match, timeout_seconds, interval_seconds, next_probe_at, config_version, sealed_access_headers)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8)`,
		checkID, probe.URL, NormalizeStatuses(probe.ExpectedStatuses), probe.BodyMatch, probe.TimeoutSeconds,
		probe.IntervalSeconds, nextProbeAt, sealed); execErr != nil {
		return probeWriteError("insert heartbeat probe", execErr)
	}
	return nil
}

// probeWriteError maps a probe-row write failure. A CHECK violation is a request the client can
// correct; the error names the constraint only, never the row (which holds the URL).
func probeWriteError(operation string, err error) error {
	if pgutil.ErrorCode(err) == pgutil.SQLStateCheckViolation {
		return registry.CheckViolationError(operation, err)
	}
	return pgutil.WrapDBError(operation, err)
}

// ProbeClaim is one due probe handed to the prober by ClaimDueProbes. ConfigVersion is the version
// the result must be recorded with; SealedAccessToken is nil when the check has no token.
type ProbeClaim struct {
	CheckID           int64
	AppID             string
	Name              string
	ConfigVersion     int64
	Config            ProbeConfig
	SealedAccessToken []byte
}

// claimDueProbesSQL claims due probe rows in one statement: the CTE picks and locks at most $2 rows
// (SKIP LOCKED, probe row only, never the parent) before the UPDATE advances next_probe_at, so the
// limit cannot over-claim and a row is handed out once per interval.
const claimDueProbesSQL = `WITH due AS MATERIALIZED (
	SELECT p2.check_id FROM heartbeat_check_probes p2
	JOIN heartbeat_checks c ON c.id = p2.check_id
	WHERE p2.next_probe_at <= $1::timestamptz AND NOT c.paused
	ORDER BY p2.next_probe_at
	LIMIT $2::int
	FOR UPDATE OF p2 SKIP LOCKED)
UPDATE heartbeat_check_probes p
SET next_probe_at = $1::timestamptz + p.interval_seconds * interval '1 second'
FROM due, heartbeat_checks c
WHERE p.check_id = due.check_id AND c.id = p.check_id
RETURNING p.check_id, c.app_id, c.name, p.config_version, p.url, p.expected_statuses, p.body_match,
	p.timeout_seconds, p.interval_seconds, p.sealed_access_headers`

// ClaimDueProbes implements Store.
func (store *PGStore) ClaimDueProbes(ctx context.Context, now time.Time, limit int) ([]ProbeClaim, error) {
	if limit < 1 {
		return nil, &ValidationError{Message: "invalid claim limit"}
	}
	rows, queryErr := store.pool.Query(ctx, claimDueProbesSQL, storeTime(now), limit)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("claim due heartbeat probes", queryErr)
	}
	defer rows.Close()

	claims := []ProbeClaim{}
	for rows.Next() {
		var claim ProbeClaim
		if scanErr := rows.Scan(
			&claim.CheckID, &claim.AppID, &claim.Name, &claim.ConfigVersion, &claim.Config.URL,
			&claim.Config.ExpectedStatuses, &claim.Config.BodyMatch, &claim.Config.TimeoutSeconds,
			&claim.Config.IntervalSeconds, &claim.SealedAccessToken); scanErr != nil {
			return nil, pgutil.WrapDBError("claim due heartbeat probes", scanErr)
		}
		claims = append(claims, claim)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("claim due heartbeat probes", rowsErr)
	}
	return claims, nil
}

// RecordProbeResult implements Store. The check is locked by id, so concurrent results serialise.
// A missing check, a push check, a stale config version or a result ApplyProbeResult denies (paused,
// bad reason) returns applied=false with nothing written and a zero check.
func (store *PGStore) RecordProbeResult(
	ctx context.Context, checkID int64, configVersion int64, result ProbeResult, now time.Time, holdOffUntil time.Time,
) (StoredCheck, bool, error) {
	now = storeTime(now)
	holdOffUntil = storeTime(holdOffUntil)
	var recorded StoredCheck
	applied := false
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, lockErr := lockCheckByID(ctx, tx, checkID)
		if errors.Is(lockErr, ErrCheckNotFound) {
			return nil
		}
		if lockErr != nil {
			return lockErr
		}
		if current.Probe == nil || current.Probe.ConfigVersion != configVersion {
			return nil
		}
		next, accepted, _ := ApplyProbeResult(current, result, now, holdOffUntil)
		if !accepted {
			return nil
		}
		reloaded, saveErr := saveCheck(ctx, tx, next, current, now)
		if saveErr != nil {
			return saveErr
		}
		recorded, applied = reloaded, true
		return nil
	})
	if txErr != nil {
		return StoredCheck{}, false, txErr
	}
	return recorded, applied, nil
}

// lockedNextProbeAt returns the next_probe_at read under the lock, or nil when the locked check
// had no probe row.
func lockedNextProbeAt(locked StoredCheck) *time.Time {
	if locked.Probe == nil {
		return nil
	}
	nextProbeAt := locked.Probe.NextProbeAt
	return &nextProbeAt
}

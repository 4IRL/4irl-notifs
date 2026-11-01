package resetlimit

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// pruneSQL, appUsageSQL and personUsageSQL mirror publisher.pgMintTx.ReserveMintSlot. The wait is the
// seconds until the oldest in-window row ages out, rounded up.
const (
	pruneSQL = `DELETE FROM app_reset_attempts WHERE app_id = $1 AND attempted_at < clock_timestamp() - $2::float8 * interval '1 second'`

	appUsageSQL = `SELECT count(*), COALESCE(ceil(extract(epoch FROM (min(attempted_at) + $2::float8 * interval '1 second' - clock_timestamp())))::bigint, 0)
FROM app_reset_attempts WHERE app_id = $1`

	personUsageSQL = `SELECT count(*), COALESCE(ceil(extract(epoch FROM (min(attempted_at) + $3::float8 * interval '1 second' - clock_timestamp())))::bigint, 0)
FROM app_reset_attempts WHERE app_id = $1 AND person_hash = $2`
)

// PGStore is the Postgres-backed reset attempt store. It is covered by the integration tests.
type PGStore struct {
	pool           *pgxpool.Pool
	reserveTimeout time.Duration
}

// NewPGStore returns a PGStore over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, reserveTimeout: ReserveTimeout}
}

// Reserve checks both caps and records one attempt for (appID, personHash) in a single short
// transaction. It returns *RateLimitedError (recording nothing) when either cap is reached, and any
// other error (wrapped) when the store could not answer; callers must treat that as a refusal. The
// transaction is committed and its connection released before Reserve returns.
func (store *PGStore) Reserve(ctx context.Context, appID, personHash string) error {
	reserveCtx, cancel := context.WithTimeout(ctx, store.reserveTimeout)
	defer cancel()

	tx, beginErr := store.pool.Begin(reserveCtx)
	if beginErr != nil {
		return pgutil.WrapDBError("begin reset reservation", beginErr)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), store.reserveTimeout)
		defer cancelRollback()
		_ = tx.Rollback(rollbackCtx)
	}()

	// Blocking lock: concurrent reserves for one app serialize for milliseconds; it is released at
	// commit or rollback.
	if _, lockErr := tx.Exec(reserveCtx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, LockPrefix+appID); lockErr != nil {
		return pgutil.WrapDBError("take reset lock", lockErr)
	}
	windowSeconds := Window.Seconds()
	if _, pruneErr := tx.Exec(reserveCtx, pruneSQL, appID, windowSeconds); pruneErr != nil {
		return pgutil.WrapDBError("prune reset attempts", pruneErr)
	}

	var appUsed, appWaitSeconds int64
	if appErr := tx.QueryRow(reserveCtx, appUsageSQL, appID, windowSeconds).Scan(&appUsed, &appWaitSeconds); appErr != nil {
		return pgutil.WrapDBError("count app reset attempts", appErr)
	}
	var personUsed, personWaitSeconds int64
	if personErr := tx.QueryRow(reserveCtx, personUsageSQL, appID, personHash, windowSeconds).
		Scan(&personUsed, &personWaitSeconds); personErr != nil {
		return pgutil.WrapDBError("count person reset attempts", personErr)
	}

	appFull := appUsed >= AppLimit
	personFull := personUsed >= PersonLimit
	if appFull || personFull {
		var waitSeconds int64
		scope := ScopePerson
		if personFull {
			waitSeconds = personWaitSeconds
		}
		if appFull {
			waitSeconds = max(waitSeconds, appWaitSeconds)
			scope = ScopeApp
		}
		waitSeconds = max(waitSeconds, 1)
		return &RateLimitedError{RetryAfter: time.Duration(waitSeconds) * time.Second, Scope: scope}
	}

	if _, insertErr := tx.Exec(reserveCtx,
		`INSERT INTO app_reset_attempts (app_id, person_hash) VALUES ($1, $2)`, appID, personHash); insertErr != nil {
		return pgutil.WrapDBError("record reset attempt", insertErr)
	}
	if commitErr := tx.Commit(reserveCtx); commitErr != nil {
		return pgutil.WrapDBError("commit reset reservation", commitErr)
	}
	committed = true
	return nil
}

// DeleteExpiredResetAttempts deletes attempt rows older than window and returns how many. It refuses
// a window that is not positive, which would delete in-window rows and reopen the limit.
func (store *PGStore) DeleteExpiredResetAttempts(ctx context.Context, now time.Time, window time.Duration) (int64, error) {
	if window <= 0 {
		return 0, errors.New("delete old reset attempts: window must be positive")
	}
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM app_reset_attempts WHERE attempted_at < $1`, now.Add(-window))
	if execErr != nil {
		return 0, pgutil.WrapDBError("delete old reset attempts", execErr)
	}
	return tag.RowsAffected(), nil
}

package appidentity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

const (
	// pendingCapLockKey is the Postgres advisory-lock key that serialises the pending-request
	// cap check, so concurrent first requests cannot all pass the count and overshoot
	// MaxPendingRequests. It is a transaction-scoped lock, released at commit or rollback.
	pendingCapLockKey int64 = 0x4944454e5f504e44

	// touchInterval is the least gap between two last_used_at writes for one key.
	touchInterval = time.Minute
)

// PGStore is the Postgres-backed Store. It is covered by the integration tests, not unit
// tests.
type PGStore struct {
	pool *pgxpool.Pool
}

// Compile-time check that PGStore satisfies Store.
var _ Store = (*PGStore)(nil)

// NewPGStore returns a PGStore over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Exchange implements Store.
func (store *PGStore) Exchange(ctx context.Context, fn func(tx ExchangeTx) error) error {
	// READ COMMITTED is load-bearing: the pending-request cap takes an advisory lock and then
	// counts, which is only correct if the count sees rows committed while it waited for the
	// lock. A snapshot isolation level would let concurrent first requests overshoot the cap.
	return pgutil.WithTx(ctx, store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		return fn(&pgExchangeTx{tx: tx})
	})
}

// ResolveKey implements Store.
func (store *PGStore) ResolveKey(ctx context.Context, key string, now time.Time) (ResolvedKey, error) {
	if !WellFormedKey(key) {
		return ResolvedKey{}, fmt.Errorf("%w: malformed", ErrInvalidKey)
	}
	var (
		resolved     ResolvedKey
		supersededAt *time.Time
		revokedAt    *time.Time
	)
	scanErr := store.pool.QueryRow(ctx,
		`SELECT id, app_id, key_hint, superseded_at, revoked_at FROM app_keys WHERE key_hash = $1`,
		HashKey(key)).Scan(&resolved.ID, &resolved.AppID, &resolved.Hint, &supersededAt, &revokedAt)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return ResolvedKey{}, fmt.Errorf("%w: unknown", ErrInvalidKey)
	}
	if scanErr != nil {
		return ResolvedKey{}, pgutil.WrapDBError("look up app key", scanErr)
	}

	record := KeyRecord{SupersededAt: supersededAt, RevokedAt: revokedAt}
	if !record.Valid(storeTime(now)) {
		reason := "past supersede grace"
		if revokedAt != nil {
			reason = "revoked"
		}
		return ResolvedKey{}, fmt.Errorf("%w: %s (key %s)", ErrInvalidKey, reason, resolved.Hint)
	}
	return resolved, nil
}

// TouchKey implements Store.
func (store *PGStore) TouchKey(ctx context.Context, keyID int64, now time.Time) (bool, error) {
	now = storeTime(now)
	tag, execErr := store.pool.Exec(ctx,
		`UPDATE app_keys SET last_used_at = $2
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at <= $3)`,
		keyID, now, now.Add(-touchInterval))
	if execErr != nil {
		return false, pgutil.WrapDBError("touch app key", execErr)
	}
	return tag.RowsAffected() == 1, nil
}

// DeleteExpiredReplay implements Store.
func (store *PGStore) DeleteExpiredReplay(ctx context.Context, now time.Time) (int64, error) {
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM oidc_replay WHERE expires_at < $1`, storeTime(now))
	if execErr != nil {
		return 0, pgutil.WrapDBError("delete expired oidc replay rows", execErr)
	}
	return tag.RowsAffected(), nil
}

// pgExchangeTx is ExchangeTx over one pgx transaction.
type pgExchangeTx struct {
	tx pgx.Tx
}

// RecordJTI implements ReplayRecorder. A concurrent exchange of the same jti blocks on the
// primary-key index until the first commits, then inserts nothing: exactly one wins.
func (exchange *pgExchangeTx) RecordJTI(ctx context.Context, jti string, expiresAt time.Time) (bool, error) {
	tag, execErr := exchange.tx.Exec(ctx,
		`INSERT INTO oidc_replay (jti, expires_at) VALUES ($1, $2) ON CONFLICT (jti) DO NOTHING`,
		jti, storeTime(expiresAt))
	if execErr != nil {
		return false, pgutil.WrapDBError("record oidc jti", execErr)
	}
	return tag.RowsAffected() == 1, nil
}

// LockBinding implements ExchangeTx.
func (exchange *pgExchangeTx) LockBinding(ctx context.Context, appID string) (Binding, bool, error) {
	var (
		binding     Binding
		environment *string
	)
	scanErr := exchange.tx.QueryRow(ctx,
		`SELECT app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at
FROM app_bindings WHERE app_id = $1 FOR UPDATE`, appID).Scan(
		&binding.AppID, &binding.Repository, &binding.RepositoryID, &binding.RepositoryOwnerID,
		&binding.WorkflowPath, &binding.Ref, &environment, &binding.ApprovedAt)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return Binding{}, false, nil
	}
	if scanErr != nil {
		return Binding{}, false, pgutil.WrapDBError("lock app binding", scanErr)
	}
	if environment != nil {
		binding.Environment = *environment
	}
	return binding, true, nil
}

// FilePendingRequest implements ExchangeTx.
func (exchange *pgExchangeTx) FilePendingRequest(
	ctx context.Context, appID string, claims Claims, now time.Time,
) (int64, error) {
	now = storeTime(now)
	environment := environmentArgument(claims.Environment)

	if _, lockErr := exchange.tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pendingCapLockKey); lockErr != nil {
		return 0, pgutil.WrapDBError("lock pending identity requests", lockErr)
	}

	// ApproveRequest holds this lock while it inserts the binding, so once it is ours any
	// approval for this app has committed or not begun. A binding visible now was invisible to
	// the LockBinding that sent this exchange down the unbound path.
	var bound bool
	if boundErr := exchange.tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app_bindings WHERE app_id = $1)`, appID).Scan(&bound); boundErr != nil {
		return 0, pgutil.WrapDBError("check app binding", boundErr)
	}
	if bound {
		return 0, ErrBindingAppeared
	}

	var requestID int64
	refreshErr := exchange.tx.QueryRow(ctx,
		`UPDATE app_registration_requests
SET updated_at = $6, github_run_id = $7, github_repository = $8, github_owner_id = $9
WHERE status = 'pending' AND app_id = $1 AND github_repository_id = $2 AND workflow_path = $3
  AND git_ref = $4 AND environment IS NOT DISTINCT FROM $5::text
RETURNING id`,
		appID, claims.RepositoryID, claims.WorkflowPath, claims.Ref, environment,
		now, claims.RunID, claims.Repository, claims.RepositoryOwnerID).Scan(&requestID)
	if refreshErr == nil {
		return requestID, nil
	}
	if !errors.Is(refreshErr, pgx.ErrNoRows) {
		return 0, pgutil.WrapDBError("refresh pending identity request", refreshErr)
	}

	var pending int
	if countErr := exchange.tx.QueryRow(ctx,
		`SELECT count(*) FROM app_registration_requests WHERE status = 'pending'`).Scan(&pending); countErr != nil {
		return 0, pgutil.WrapDBError("count pending identity requests", countErr)
	}
	if pending >= MaxPendingRequests {
		return 0, ErrPendingCap
	}

	if insertErr := exchange.tx.QueryRow(ctx,
		`INSERT INTO app_registration_requests
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment,
   github_run_id, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9, $9) RETURNING id`,
		appID, claims.Repository, claims.RepositoryID, claims.RepositoryOwnerID, claims.WorkflowPath,
		claims.Ref, environment, claims.RunID, now).Scan(&requestID); insertErr != nil {
		return 0, pgutil.WrapDBError("insert pending identity request", insertErr)
	}
	return requestID, nil
}

// IssueKey implements ExchangeTx.
func (exchange *pgExchangeTx) IssueKey(
	ctx context.Context, appID string, githubRunID string, now time.Time,
) (IssuedKey, error) {
	now = storeTime(now)
	plaintext, keyErr := NewKey()
	if keyErr != nil {
		return IssuedKey{}, keyErr
	}

	if _, supersedeErr := exchange.tx.Exec(ctx,
		`UPDATE app_keys SET superseded_at = $2 WHERE app_id = $1 AND superseded_at IS NULL AND revoked_at IS NULL`,
		appID, now); supersedeErr != nil {
		return IssuedKey{}, pgutil.WrapDBError("supersede app keys", supersedeErr)
	}

	issued := IssuedKey{Plaintext: plaintext, Hint: KeyHint(plaintext)}
	if insertErr := exchange.tx.QueryRow(ctx,
		`INSERT INTO app_keys (app_id, key_hash, key_hint, created_at, github_run_id) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		appID, HashKey(plaintext), issued.Hint, now, githubRunID).Scan(&issued.ID); insertErr != nil {
		return IssuedKey{}, pgutil.WrapDBError("insert app key", insertErr)
	}
	return issued, nil
}

// environmentArgument maps the empty "no environment" to a SQL NULL.
func environmentArgument(environment string) *string {
	if environment == "" {
		return nil
	}
	return &environment
}

// storeTime truncates now to Postgres's microsecond timestamp precision, so a time written
// and read back compares equal.
func storeTime(now time.Time) time.Time {
	return now.UTC().Truncate(time.Microsecond)
}

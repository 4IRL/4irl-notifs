package appidentity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// Compile-time checks that PGStore satisfies AdminStore, RejectedRequestSweeper and
// PendingRequestSweeper.
var (
	_ AdminStore             = (*PGStore)(nil)
	_ RejectedRequestSweeper = (*PGStore)(nil)
	_ PendingRequestSweeper  = (*PGStore)(nil)
)

const requestColumns = `id, app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref,
  environment, github_run_id, status, created_at, updated_at, decided_at`

const keyColumns = `id, app_id, key_hint, created_at, superseded_at, revoked_at, revoked_reason, last_used_at`

// scanRequest reads one app_registration_requests row selected with requestColumns.
func scanRequest(row pgutil.RowScanner) (Request, error) {
	var (
		request     Request
		environment *string
	)
	if scanErr := row.Scan(&request.ID, &request.AppID, &request.Repository, &request.RepositoryID,
		&request.RepositoryOwnerID, &request.WorkflowPath, &request.Ref, &environment, &request.GitHubRunID,
		&request.Status, &request.CreatedAt, &request.UpdatedAt, &request.DecidedAt); scanErr != nil {
		return Request{}, scanErr
	}
	if environment != nil {
		request.Environment = *environment
	}
	return request, nil
}

// scanKey reads one app_keys row selected with keyColumns.
func scanKey(row pgutil.RowScanner) (storedKey, error) {
	var stored storedKey
	scanErr := row.Scan(&stored.ID, &stored.AppID, &stored.Hint, &stored.CreatedAt, &stored.SupersededAt,
		&stored.RevokedAt, &stored.RevokedReason, &stored.LastUsedAt)
	return stored, scanErr
}

// ListRequests implements AdminStore.
func (store *PGStore) ListRequests(ctx context.Context, now time.Time) ([]Request, error) {
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+requestColumns+`
FROM app_registration_requests
WHERE status = 'pending' OR (status = 'rejected' AND decided_at >= $1)
ORDER BY (status = 'pending') DESC, updated_at DESC, id DESC`,
		storeTime(now).Add(-RejectedRequestWindow))
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list identity requests", queryErr)
	}
	defer rows.Close()

	requests := []Request{}
	for rows.Next() {
		request, scanErr := scanRequest(rows)
		if scanErr != nil {
			return nil, pgutil.WrapDBError("scan identity request", scanErr)
		}
		requests = append(requests, request)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("list identity requests", rowsErr)
	}
	return requests, nil
}

// DeleteExpiredRejectedRequests implements RejectedRequestSweeper. The cutoff is the one
// ListRequests uses, so exactly the rejected rows the operator no longer sees are deleted: a
// rejection decided at the window's edge is kept.
func (store *PGStore) DeleteExpiredRejectedRequests(ctx context.Context, now time.Time) (int64, error) {
	tag, execErr := store.pool.Exec(ctx,
		`DELETE FROM app_registration_requests WHERE status = 'rejected' AND decided_at < $1`,
		storeTime(now).Add(-RejectedRequestWindow))
	if execErr != nil {
		return 0, pgutil.WrapDBError("delete old rejected identity requests", execErr)
	}
	return tag.RowsAffected(), nil
}

// DeleteStalePendingRequests implements PendingRequestSweeper. Age counts from updated_at,
// which every repeat exchange with the same claims refreshes, so a request an app keeps
// re-filing is never swept. The delete re-checks updated_at against a concurrent refresh, and an
// approval deletes its own row, so neither race removes a live request. A ttl that is not
// positive is refused: it would put the cutoff at or after now and sweep every pending row.
func (store *PGStore) DeleteStalePendingRequests(ctx context.Context, now time.Time, ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, errors.New("delete stale pending identity requests: ttl must be positive")
	}
	tag, execErr := store.pool.Exec(ctx,
		`DELETE FROM app_registration_requests WHERE status = 'pending' AND updated_at < $1`,
		storeTime(now).Add(-ttl))
	if execErr != nil {
		return 0, pgutil.WrapDBError("delete stale pending identity requests", execErr)
	}
	return tag.RowsAffected(), nil
}

// ApproveRequest implements AdminStore. The request row is locked first, so two approvals
// of one request (or an approval racing a rejection) run one after the other.
func (store *PGStore) ApproveRequest(ctx context.Context, requestID int64, now time.Time) (Binding, error) {
	now = storeTime(now)
	var binding Binding
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		// The same lock FilePendingRequest takes, taken first (an exchange also takes it before
		// any request row), so an exchange cannot file a request for this app between the binding
		// insert below and its commit.
		if _, lockErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pendingCapLockKey); lockErr != nil {
			return pgutil.WrapDBError("lock pending identity requests", lockErr)
		}
		request, lockErr := lockRequest(ctx, tx, requestID)
		if lockErr != nil {
			return lockErr
		}
		if request.Status != RequestPending {
			return ErrRequestNotPending
		}

		// An app owns the destination names that start with "<app_id>_", so an app id that is an
		// underscore-extension of a bound one (or the reverse) would share a name space with it.
		// starts_with, not LIKE: "_" is a LIKE wildcard. An exact match is not an overlap; the
		// insert below reports it as ErrAppAlreadyBound. The advisory lock above means no other
		// approval can bind an overlapping id between this check and the insert.
		var overlaps bool
		if overlapErr := tx.QueryRow(ctx,
			`SELECT EXISTS (
  SELECT 1 FROM app_bindings
  WHERE starts_with($1::text, app_id || '_') OR starts_with(app_id, $1::text || '_'))`,
			request.AppID).Scan(&overlaps); overlapErr != nil {
			return pgutil.WrapDBError("check app id overlap", overlapErr)
		}
		if overlaps {
			return ErrAppIDOverlap
		}

		// DO NOTHING, not an error, on either unique constraint (the app_id primary key or the
		// workflow identity): a failed statement would abort the transaction before the cause
		// could be told apart.
		tag, insertErr := tx.Exec(ctx,
			`INSERT INTO app_bindings
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT DO NOTHING`,
			request.AppID, request.Repository, request.RepositoryID, request.RepositoryOwnerID,
			request.WorkflowPath, request.Ref, environmentArgument(request.Environment), now)
		if insertErr != nil {
			return pgutil.WrapDBError("insert app binding", insertErr)
		}
		if tag.RowsAffected() == 0 {
			var appBound bool
			if existsErr := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM app_bindings WHERE app_id = $1)`, request.AppID).Scan(&appBound); existsErr != nil {
				return pgutil.WrapDBError("check app binding", existsErr)
			}
			if appBound {
				return ErrAppAlreadyBound
			}
			return ErrWorkflowIdentityBound
		}

		if _, deleteErr := tx.Exec(ctx, `DELETE FROM app_registration_requests WHERE id = $1`, requestID); deleteErr != nil {
			return pgutil.WrapDBError("delete approved identity request", deleteErr)
		}
		// The other pending requests for this app id or this workflow identity can never be
		// approved now (ErrAppAlreadyBound / ErrWorkflowIdentityBound) and would hold pending-cap
		// slots until rejected one by one, so they go with the approval. Rejected rows are
		// decisions already made and stay. The advisory lock above serializes this against any
		// other approval or filing.
		if _, siblingErr := tx.Exec(ctx,
			`DELETE FROM app_registration_requests
WHERE status = 'pending'
  AND (app_id = $1
       OR (github_repository_id = $2 AND workflow_path = $3 AND git_ref = $4
           AND environment IS NOT DISTINCT FROM $5::text))`,
			request.AppID, request.RepositoryID, request.WorkflowPath, request.Ref,
			environmentArgument(request.Environment)); siblingErr != nil {
			return pgutil.WrapDBError("delete sibling identity requests", siblingErr)
		}
		binding = Binding{
			AppID:             request.AppID,
			Repository:        request.Repository,
			RepositoryID:      request.RepositoryID,
			RepositoryOwnerID: request.RepositoryOwnerID,
			WorkflowPath:      request.WorkflowPath,
			Ref:               request.Ref,
			Environment:       request.Environment,
			ApprovedAt:        now,
		}
		return nil
	})
	if txErr != nil {
		return Binding{}, txErr
	}
	return binding, nil
}

// RejectRequest implements AdminStore.
func (store *PGStore) RejectRequest(ctx context.Context, requestID int64, now time.Time) (Request, error) {
	now = storeTime(now)
	var rejected Request
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		request, lockErr := lockRequest(ctx, tx, requestID)
		if lockErr != nil {
			return lockErr
		}
		if request.Status == RequestRejected {
			rejected = request
			return nil
		}
		updated, updateErr := scanRequest(tx.QueryRow(ctx,
			`UPDATE app_registration_requests SET status = 'rejected', decided_at = $2, updated_at = $2
WHERE id = $1 RETURNING `+requestColumns, requestID, now))
		if updateErr != nil {
			return pgutil.WrapDBError("reject identity request", updateErr)
		}
		rejected = updated
		return nil
	})
	if txErr != nil {
		return Request{}, txErr
	}
	return rejected, nil
}

// lockRequest reads one request with its row locked until the transaction ends.
func lockRequest(ctx context.Context, tx pgx.Tx, requestID int64) (Request, error) {
	request, scanErr := scanRequest(tx.QueryRow(ctx,
		`SELECT `+requestColumns+` FROM app_registration_requests WHERE id = $1 FOR UPDATE`, requestID))
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return Request{}, ErrRequestNotFound
	}
	if scanErr != nil {
		return Request{}, pgutil.WrapDBError("lock identity request", scanErr)
	}
	return request, nil
}

// ListBindings implements AdminStore. Bindings and keys are read in one snapshot, so a key
// issued between the two reads cannot appear under a binding the list does not show.
func (store *PGStore) ListBindings(ctx context.Context, now time.Time) ([]BindingWithKeys, error) {
	now = storeTime(now)
	listed := []BindingWithKeys{}
	txErr := pgutil.WithTx(ctx, store.pool,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			bindingRows, queryErr := tx.Query(ctx,
				`SELECT app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref,
  environment, approved_at
FROM app_bindings ORDER BY app_id`)
			if queryErr != nil {
				return pgutil.WrapDBError("list app bindings", queryErr)
			}
			indexByApp := map[string]int{}
			for bindingRows.Next() {
				var (
					binding     Binding
					environment *string
				)
				if scanErr := bindingRows.Scan(&binding.AppID, &binding.Repository, &binding.RepositoryID,
					&binding.RepositoryOwnerID, &binding.WorkflowPath, &binding.Ref, &environment,
					&binding.ApprovedAt); scanErr != nil {
					bindingRows.Close()
					return pgutil.WrapDBError("scan app binding", scanErr)
				}
				if environment != nil {
					binding.Environment = *environment
				}
				indexByApp[binding.AppID] = len(listed)
				listed = append(listed, BindingWithKeys{Binding: binding, Keys: []Key{}})
			}
			bindingRows.Close()
			if rowsErr := bindingRows.Err(); rowsErr != nil {
				return pgutil.WrapDBError("list app bindings", rowsErr)
			}

			keyRows, keyQueryErr := tx.Query(ctx, `SELECT `+keyColumns+` FROM app_keys ORDER BY app_id, id DESC`)
			if keyQueryErr != nil {
				return pgutil.WrapDBError("list app keys", keyQueryErr)
			}
			defer keyRows.Close()
			for keyRows.Next() {
				stored, scanErr := scanKey(keyRows)
				if scanErr != nil {
					return pgutil.WrapDBError("scan app key", scanErr)
				}
				if index, found := indexByApp[stored.AppID]; found {
					listed[index].Keys = append(listed[index].Keys, stored.toKey(now))
				}
			}
			if rowsErr := keyRows.Err(); rowsErr != nil {
				return pgutil.WrapDBError("list app keys", rowsErr)
			}
			return nil
		})
	if txErr != nil {
		return nil, txErr
	}
	return listed, nil
}

// RevokeKey implements AdminStore.
func (store *PGStore) RevokeKey(ctx context.Context, keyID int64, now time.Time) (Key, error) {
	now = storeTime(now)
	var revoked Key
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		// Only a key that still works is revoked. One already revoked keeps its revoke time and
		// reason; one past its supersede grace window keeps reading "superseded".
		if _, updateErr := tx.Exec(ctx,
			`UPDATE app_keys SET revoked_at = $2, revoked_reason = 'operator'
WHERE id = $1 AND revoked_at IS NULL AND (superseded_at IS NULL OR superseded_at > $3)`,
			keyID, now, now.Add(-SupersededGrace)); updateErr != nil {
			return pgutil.WrapDBError("revoke app key", updateErr)
		}
		stored, scanErr := scanKey(tx.QueryRow(ctx, `SELECT `+keyColumns+` FROM app_keys WHERE id = $1`, keyID))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrKeyNotFound
		}
		if scanErr != nil {
			return pgutil.WrapDBError("read revoked app key", scanErr)
		}
		revoked = stored.toKey(now)
		return nil
	})
	if txErr != nil {
		return Key{}, txErr
	}
	return revoked, nil
}

// Unbind implements AdminStore. Deleting the binding cascades to its keys (app_keys.app_id
// is ON DELETE CASCADE), so every key stops resolving in the same statement; the exchange
// transaction locks the binding row, so an exchange in flight finishes first.
func (store *PGStore) Unbind(ctx context.Context, appID string) (bool, error) {
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM app_bindings WHERE app_id = $1`, appID)
	if execErr != nil {
		return false, pgutil.WrapDBError("delete app binding", execErr)
	}
	return tag.RowsAffected() == 1, nil
}

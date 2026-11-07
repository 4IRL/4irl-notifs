package appidentity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// bindingColumns selects one app_bindings row (alias b) with its registrar's prefix (alias r,
// LEFT JOINed on b.registrar_id). Read it with scanBinding.
const bindingColumns = `b.app_id, b.github_repository, b.github_repository_id, b.github_owner_id, b.workflow_path,
  b.git_ref, b.environment, b.approved_at, b.registrar_id, COALESCE(r.prefix, ''), b.acknowledged_at`

// registrarColumns selects one app_registrars row. Read it with scanRegistrar.
const registrarColumns = `id, prefix, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref,
  environment, approved_at`

// identityPredicate matches the five-field workflow identity of a table with the registrar and
// binding column names, against claims bound as $1..$5 (repository id, owner id, workflow path,
// ref, environment).
const identityPredicate = `github_repository_id = $1 AND github_owner_id = $2 AND workflow_path = $3
  AND git_ref = $4 AND environment IS NOT DISTINCT FROM $5::text`

// Compile-time check that pgExchangeTx satisfies ExchangeTx.
var _ ExchangeTx = (*pgExchangeTx)(nil)

// scanBinding reads one row selected with bindingColumns.
func scanBinding(row pgutil.RowScanner) (Binding, error) {
	var (
		binding     Binding
		environment *string
	)
	if scanErr := row.Scan(&binding.AppID, &binding.Repository, &binding.RepositoryID, &binding.RepositoryOwnerID,
		&binding.WorkflowPath, &binding.Ref, &environment, &binding.ApprovedAt, &binding.RegistrarID,
		&binding.RegistrarPrefix, &binding.AcknowledgedAt); scanErr != nil {
		return Binding{}, scanErr
	}
	if environment != nil {
		binding.Environment = *environment
	}
	return binding, nil
}

// scanRegistrar reads one row selected with registrarColumns.
func scanRegistrar(row pgutil.RowScanner) (WorkflowRegistrar, error) {
	return scanRegistrarWith(row)
}

// scanRegistrarWith reads one row selected with registrarColumns followed by the extra
// destinations (for example a trailing app count).
func scanRegistrarWith(row pgutil.RowScanner, extra ...any) (WorkflowRegistrar, error) {
	var (
		registrar   WorkflowRegistrar
		environment *string
	)
	destinations := append([]any{&registrar.ID, &registrar.Prefix, &registrar.Repository, &registrar.RepositoryID,
		&registrar.RepositoryOwnerID, &registrar.WorkflowPath, &registrar.Ref, &environment,
		&registrar.ApprovedAt}, extra...)
	if scanErr := row.Scan(destinations...); scanErr != nil {
		return WorkflowRegistrar{}, scanErr
	}
	if environment != nil {
		registrar.Environment = *environment
	}
	registrar.AppLimit = RegistrarAppLimit
	return registrar, nil
}

// identityArguments are the $1..$5 identityPredicate binds for claims.
func identityArguments(claims Claims) []any {
	return []any{claims.RepositoryID, claims.RepositoryOwnerID, claims.WorkflowPath, claims.Ref,
		environmentArgument(claims.Environment)}
}

// lockPendingRequests takes the transaction-scoped advisory lock that serialises request
// filing, app and registrar approval, registrar-binding creation and registrar deletion.
func lockPendingRequests(ctx context.Context, tx pgutil.Querier) error {
	if _, lockErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, pendingCapLockKey); lockErr != nil {
		return pgutil.WrapDBError("lock pending identity requests", lockErr)
	}
	return nil
}

// findRegistrarByClaims returns the registrar with the claims' workflow identity, nil when none.
func findRegistrarByClaims(ctx context.Context, querier pgutil.Querier, claims Claims) (*WorkflowRegistrar, error) {
	registrar, scanErr := scanRegistrar(querier.QueryRow(ctx,
		`SELECT `+registrarColumns+` FROM app_registrars WHERE `+identityPredicate, identityArguments(claims)...))
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return nil, nil
	}
	if scanErr != nil {
		return nil, pgutil.WrapDBError("find registrar", scanErr)
	}
	return &registrar, nil
}

// identityIsRegistrar reports whether a registrar has the claims' workflow identity.
func identityIsRegistrar(ctx context.Context, querier pgutil.Querier, claims Claims) (bool, error) {
	var exists bool
	if queryErr := querier.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app_registrars WHERE `+identityPredicate+`)`,
		identityArguments(claims)...).Scan(&exists); queryErr != nil {
		return false, pgutil.WrapDBError("check registrar identity", queryErr)
	}
	return exists, nil
}

// identityIsNormalBinding reports whether a registrar_id IS NULL binding has the claims'
// workflow identity.
func identityIsNormalBinding(ctx context.Context, querier pgutil.Querier, claims Claims) (bool, error) {
	var exists bool
	if queryErr := querier.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app_bindings WHERE registrar_id IS NULL AND `+identityPredicate+`)`,
		identityArguments(claims)...).Scan(&exists); queryErr != nil {
		return false, pgutil.WrapDBError("check normal binding identity", queryErr)
	}
	return exists, nil
}

// appIDInRegistrarPrefix reports whether appID equals some registrar's stem or starts with its
// prefix. starts_with, not LIKE: "_" is a LIKE wildcard.
func appIDInRegistrarPrefix(ctx context.Context, querier pgutil.Querier, appID string) (bool, error) {
	var inside bool
	if queryErr := querier.QueryRow(ctx,
		`SELECT EXISTS (
  SELECT 1 FROM app_registrars WHERE $1::text = left(prefix, -1) OR starts_with($1::text, prefix))`,
		appID).Scan(&inside); queryErr != nil {
		return false, pgutil.WrapDBError("check registrar prefix", queryErr)
	}
	return inside, nil
}

// appIDOverlaps reports whether appID is an underscore-extension of a bound app id, or the
// reverse. An app owns the destination names that start with "<app_id>_", so such a pair would
// share a name space. An exact match is not an overlap (the insert reports it as already bound).
func appIDOverlaps(ctx context.Context, querier pgutil.Querier, appID string) (bool, error) {
	var overlaps bool
	if overlapErr := querier.QueryRow(ctx,
		`SELECT EXISTS (
  SELECT 1 FROM app_bindings
  WHERE starts_with($1::text, app_id || '_') OR starts_with(app_id, $1::text || '_'))`,
		appID).Scan(&overlaps); overlapErr != nil {
		return false, pgutil.WrapDBError("check app id overlap", overlapErr)
	}
	return overlaps, nil
}

// registrarAppCount counts the apps the registrar created.
func registrarAppCount(ctx context.Context, querier pgutil.Querier, registrarID int64) (int, error) {
	var count int
	if countErr := querier.QueryRow(ctx,
		`SELECT count(*) FROM app_bindings WHERE registrar_id = $1`, registrarID).Scan(&count); countErr != nil {
		return 0, pgutil.WrapDBError("count registrar apps", countErr)
	}
	return count, nil
}

// ReadRegistrarExchange implements Store. It runs in one read-only snapshot and writes nothing.
func (store *PGStore) ReadRegistrarExchange(
	ctx context.Context, claims Claims, name string,
) (RegistrarExchangeState, error) {
	var state RegistrarExchangeState
	txErr := pgutil.WithTx(ctx, store.pool,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			registrar, findErr := findRegistrarByClaims(ctx, tx, claims)
			if findErr != nil {
				return findErr
			}
			state.Registrar = registrar

			normal, normalErr := identityIsNormalBinding(ctx, tx, claims)
			if normalErr != nil {
				return normalErr
			}
			state.NormalBinding = normal

			if recordedErr := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM oidc_replay WHERE jti = $1)`,
				claims.JTI).Scan(&state.JTIRecorded); recordedErr != nil {
				return pgutil.WrapDBError("read oidc jti", recordedErr)
			}
			if registrar == nil {
				return nil
			}

			// The prefix is only known once the registrar is found, so the store derives the id.
			derivedAppID, deriveErr := DeriveAppID(registrar.Prefix, name)
			if deriveErr != nil {
				return deriveErr
			}
			state.DerivedAppID = derivedAppID

			var ownerID *int64
			ownerErr := tx.QueryRow(ctx,
				`SELECT registrar_id FROM app_bindings WHERE app_id = $1`, derivedAppID).Scan(&ownerID)
			switch {
			case errors.Is(ownerErr, pgx.ErrNoRows):
				state.Derived = DerivedAppAbsent
			case ownerErr != nil:
				return pgutil.WrapDBError("read derived app binding", ownerErr)
			case ownerID != nil && *ownerID == registrar.ID:
				state.Derived = DerivedAppOwned
			default:
				state.Derived = DerivedAppForeign
			}

			count, countErr := registrarAppCount(ctx, tx, registrar.ID)
			if countErr != nil {
				return countErr
			}
			state.AppCount = count
			overlaps, overlapErr := appIDOverlaps(ctx, tx, derivedAppID)
			if overlapErr != nil {
				return overlapErr
			}
			state.Overlaps = overlaps
			return nil
		})
	if txErr != nil {
		return RegistrarExchangeState{}, txErr
	}
	return state, nil
}

// AppIDInRegistrarPrefix implements ExchangeTx.
func (exchange *pgExchangeTx) AppIDInRegistrarPrefix(ctx context.Context, appID string) (bool, error) {
	return appIDInRegistrarPrefix(ctx, exchange.tx, appID)
}

// FindRegistrarByClaims implements ExchangeTx.
func (exchange *pgExchangeTx) FindRegistrarByClaims(ctx context.Context, claims Claims) (*WorkflowRegistrar, error) {
	return findRegistrarByClaims(ctx, exchange.tx, claims)
}

// HasNormalBindingForIdentity implements ExchangeTx.
func (exchange *pgExchangeTx) HasNormalBindingForIdentity(ctx context.Context, claims Claims) (bool, error) {
	return identityIsNormalBinding(ctx, exchange.tx, claims)
}

// FilePendingRegistrarRequest implements ExchangeTx.
func (exchange *pgExchangeTx) FilePendingRegistrarRequest(
	ctx context.Context, claims Claims, now time.Time,
) (int64, error) {
	now = storeTime(now)
	environment := environmentArgument(claims.Environment)

	if lockErr := lockPendingRequests(ctx, exchange.tx); lockErr != nil {
		return 0, lockErr
	}

	// Approvals hold this lock, so after it is ours the identity is settled: it may have become a
	// registrar or a normal binding since the exchange looked.
	isRegistrar, registrarErr := identityIsRegistrar(ctx, exchange.tx, claims)
	if registrarErr != nil {
		return 0, registrarErr
	}
	isNormal, normalErr := identityIsNormalBinding(ctx, exchange.tx, claims)
	if normalErr != nil {
		return 0, normalErr
	}
	if isRegistrar || isNormal {
		return 0, ErrBindingMismatch
	}

	// app_id = $1 cannot match a NULL app_id, so the refresh matches kind and NULL explicitly.
	var requestID int64
	refreshErr := exchange.tx.QueryRow(ctx,
		`UPDATE app_registration_requests
SET updated_at = $5, github_run_id = $6, github_repository = $7, github_owner_id = $8
WHERE status = 'pending' AND kind = 'registrar' AND app_id IS NULL AND github_repository_id = $1
  AND workflow_path = $2 AND git_ref = $3 AND environment IS NOT DISTINCT FROM $4::text
RETURNING id`,
		claims.RepositoryID, claims.WorkflowPath, claims.Ref, environment,
		now, claims.RunID, claims.Repository, claims.RepositoryOwnerID).Scan(&requestID)
	if refreshErr == nil {
		return requestID, nil
	}
	if !errors.Is(refreshErr, pgx.ErrNoRows) {
		return 0, pgutil.WrapDBError("refresh pending registrar request", refreshErr)
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
  (kind, app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment,
   github_run_id, status, created_at, updated_at)
VALUES ('registrar', NULL, $1, $2, $3, $4, $5, $6, $7, 'pending', $8, $8) RETURNING id`,
		claims.Repository, claims.RepositoryID, claims.RepositoryOwnerID, claims.WorkflowPath,
		claims.Ref, environment, claims.RunID, now).Scan(&requestID); insertErr != nil {
		return 0, pgutil.WrapDBError("insert pending registrar request", insertErr)
	}
	return requestID, nil
}

// CreateRegistrarBinding implements ExchangeTx.
func (exchange *pgExchangeTx) CreateRegistrarBinding(
	ctx context.Context, registrar WorkflowRegistrar, appID string, now time.Time,
) error {
	now = storeTime(now)
	if lockErr := lockPendingRequests(ctx, exchange.tx); lockErr != nil {
		return lockErr
	}

	// DeleteRegistrar takes the same lock, so the registrar read here cannot vanish before the
	// insert commits. Re-read it: the caller's copy predates the lock. The row, not the caller's
	// copy, supplies the identity columns and the prefix.
	current, scanErr := scanRegistrar(exchange.tx.QueryRow(ctx,
		`SELECT `+registrarColumns+` FROM app_registrars WHERE id = $1`, registrar.ID))
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return ErrRegistrarNotFound
	}
	if scanErr != nil {
		return pgutil.WrapDBError("read registrar", scanErr)
	}
	// Fail closed: a registrar may only create ids inside its own prefix.
	if len(appID) <= len(current.Prefix) || appID[:len(current.Prefix)] != current.Prefix {
		return ErrRegistrarOwnership
	}

	var bound bool
	if boundErr := exchange.tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM app_bindings WHERE app_id = $1)`, appID).Scan(&bound); boundErr != nil {
		return pgutil.WrapDBError("check app binding", boundErr)
	}
	if bound {
		return ErrBindingAppeared
	}

	count, countErr := registrarAppCount(ctx, exchange.tx, current.ID)
	if countErr != nil {
		return countErr
	}
	if count >= RegistrarAppLimit {
		return ErrRegistrarAppLimit
	}
	overlaps, overlapErr := appIDOverlaps(ctx, exchange.tx, appID)
	if overlapErr != nil {
		return overlapErr
	}
	if overlaps {
		return ErrAppIDOverlap
	}

	tag, insertErr := exchange.tx.Exec(ctx,
		`INSERT INTO app_bindings
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment,
   approved_at, registrar_id, acknowledged_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL)
ON CONFLICT (app_id) DO NOTHING`,
		appID, current.Repository, current.RepositoryID, current.RepositoryOwnerID, current.WorkflowPath,
		current.Ref, environmentArgument(current.Environment), now, current.ID)
	if insertErr != nil {
		return pgutil.WrapDBError("insert registrar app binding", insertErr)
	}
	if tag.RowsAffected() == 0 {
		return ErrBindingAppeared
	}
	return nil
}

// ApproveRegistrarRequest implements AdminStore.
func (store *PGStore) ApproveRegistrarRequest(
	ctx context.Context, requestID int64, prefix string, now time.Time,
) (WorkflowRegistrar, error) {
	if validateErr := ValidatePrefix(prefix); validateErr != nil {
		return WorkflowRegistrar{}, validateErr
	}
	now = storeTime(now)
	stem := prefix[:len(prefix)-1]
	var approved WorkflowRegistrar
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if lockErr := lockPendingRequests(ctx, tx); lockErr != nil {
			return lockErr
		}
		request, lockErr := lockRequest(ctx, tx, requestID)
		if lockErr != nil {
			return lockErr
		}
		if request.Status != RequestPending {
			return ErrRequestNotPending
		}
		if request.Kind != RequestKindRegistrar {
			return ErrRequestNotRegistrar
		}
		claims := Claims{
			RepositoryID: request.RepositoryID, RepositoryOwnerID: request.RepositoryOwnerID,
			WorkflowPath: request.WorkflowPath, Ref: request.Ref, Environment: request.Environment,
		}

		// One identity, one role.
		isRegistrar, registrarErr := identityIsRegistrar(ctx, tx, claims)
		if registrarErr != nil {
			return registrarErr
		}
		isNormal, normalErr := identityIsNormalBinding(ctx, tx, claims)
		if normalErr != nil {
			return normalErr
		}
		if isRegistrar || isNormal {
			return ErrWorkflowIdentityBound
		}

		var taken bool
		if takenErr := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM app_registrars WHERE prefix = $1)`, prefix).Scan(&taken); takenErr != nil {
			return pgutil.WrapDBError("check registrar prefix", takenErr)
		}
		if taken {
			return ErrPrefixTaken
		}

		// Overlap with a bound app id (the stem itself, anything under the prefix, or an
		// underscore-extension of an app id) or with another registrar's prefix, either way round.
		var overlaps bool
		if overlapErr := tx.QueryRow(ctx,
			`SELECT EXISTS (
  SELECT 1 FROM app_bindings WHERE app_id = $2::text OR starts_with(app_id, $1::text) OR starts_with($1::text, app_id || '_')
) OR EXISTS (
  SELECT 1 FROM app_registrars WHERE starts_with(prefix, $1::text) OR starts_with($1::text, prefix))`,
			prefix, stem).Scan(&overlaps); overlapErr != nil {
			return pgutil.WrapDBError("check registrar prefix overlap", overlapErr)
		}
		if overlaps {
			return ErrPrefixOverlap
		}

		scanErr := tx.QueryRow(ctx,
			`INSERT INTO app_registrars
  (prefix, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id`,
			prefix, request.Repository, request.RepositoryID, request.RepositoryOwnerID, request.WorkflowPath,
			request.Ref, environmentArgument(request.Environment), now).Scan(&approved.ID)
		if scanErr != nil {
			return pgutil.WrapDBError("insert registrar", scanErr)
		}

		// This request and every other pending request for the identity (it is a registrar now).
		if _, deleteErr := tx.Exec(ctx,
			`DELETE FROM app_registration_requests
WHERE id = $1 OR (status = 'pending' AND github_repository_id = $2 AND workflow_path = $3 AND git_ref = $4
                  AND environment IS NOT DISTINCT FROM $5::text)`,
			requestID, request.RepositoryID, request.WorkflowPath, request.Ref,
			environmentArgument(request.Environment)); deleteErr != nil {
			return pgutil.WrapDBError("delete registrar request and siblings", deleteErr)
		}
		// Pending app requests the prefix now covers can never be approved; reject them in this
		// transaction. FilePendingRequest re-checks the prefix under the same lock, so none can be
		// filed afterwards.
		if _, sweepErr := tx.Exec(ctx,
			`UPDATE app_registration_requests SET status = 'rejected', decided_at = $3, updated_at = $3
WHERE status = 'pending' AND kind = 'app' AND (app_id = $2 OR starts_with(app_id, $1))`,
			prefix, stem, now); sweepErr != nil {
			return pgutil.WrapDBError("reject requests inside registrar prefix", sweepErr)
		}

		approved = WorkflowRegistrar{
			ID: approved.ID, Prefix: prefix, Repository: request.Repository, RepositoryID: request.RepositoryID,
			RepositoryOwnerID: request.RepositoryOwnerID, WorkflowPath: request.WorkflowPath, Ref: request.Ref,
			Environment: request.Environment, ApprovedAt: now, AppLimit: RegistrarAppLimit,
		}
		return nil
	})
	if txErr != nil {
		return WorkflowRegistrar{}, txErr
	}
	return approved, nil
}

// ListRegistrars implements AdminStore.
func (store *PGStore) ListRegistrars(ctx context.Context) ([]WorkflowRegistrar, error) {
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+registrarColumns+`,
  (SELECT count(*) FROM app_bindings b WHERE b.registrar_id = app_registrars.id)
FROM app_registrars ORDER BY prefix`)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list registrars", queryErr)
	}
	defer rows.Close()

	registrars := []WorkflowRegistrar{}
	for rows.Next() {
		var appCount int
		registrar, scanErr := scanRegistrarWith(rows, &appCount)
		if scanErr != nil {
			return nil, pgutil.WrapDBError("scan registrar", scanErr)
		}
		registrar.AppCount = appCount
		registrars = append(registrars, registrar)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("list registrars", rowsErr)
	}
	return registrars, nil
}

// DeleteRegistrar implements AdminStore. The advisory lock makes it queue behind exchanges that
// are creating a binding for this registrar, so the cascade never meets a half-inserted binding.
func (store *PGStore) DeleteRegistrar(ctx context.Context, registrarID int64) (WorkflowRegistrar, int, error) {
	var (
		deleted     WorkflowRegistrar
		unboundApps int
	)
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if lockErr := lockPendingRequests(ctx, tx); lockErr != nil {
			return lockErr
		}
		count, countErr := registrarAppCount(ctx, tx, registrarID)
		if countErr != nil {
			return countErr
		}
		removed, scanErr := scanRegistrar(tx.QueryRow(ctx,
			`DELETE FROM app_registrars WHERE id = $1 RETURNING `+registrarColumns, registrarID))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrRegistrarNotFound
		}
		if scanErr != nil {
			return pgutil.WrapDBError("delete registrar", scanErr)
		}
		deleted, unboundApps = removed, count
		return nil
	})
	if txErr != nil {
		return WorkflowRegistrar{}, 0, txErr
	}
	return deleted, unboundApps, nil
}

// AcknowledgeBinding implements AdminStore. COALESCE keeps the first acknowledged_at.
func (store *PGStore) AcknowledgeBinding(ctx context.Context, appID string, now time.Time) (BindingWithKeys, error) {
	now = storeTime(now)
	var result BindingWithKeys
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		tag, updateErr := tx.Exec(ctx,
			`UPDATE app_bindings SET acknowledged_at = COALESCE(acknowledged_at, $2) WHERE app_id = $1`, appID, now)
		if updateErr != nil {
			return pgutil.WrapDBError("acknowledge app binding", updateErr)
		}
		if tag.RowsAffected() == 0 {
			return ErrBindingNotFound
		}
		binding, scanErr := scanBinding(tx.QueryRow(ctx,
			`SELECT `+bindingColumns+`
FROM app_bindings b LEFT JOIN app_registrars r ON r.id = b.registrar_id WHERE b.app_id = $1`, appID))
		if scanErr != nil {
			return pgutil.WrapDBError("read acknowledged app binding", scanErr)
		}
		keyRows, queryErr := tx.Query(ctx, `SELECT `+keyColumns+` FROM app_keys WHERE app_id = $1 ORDER BY id DESC`, appID)
		if queryErr != nil {
			return pgutil.WrapDBError("list app keys", queryErr)
		}
		defer keyRows.Close()
		keys := []Key{}
		for keyRows.Next() {
			stored, keyScanErr := scanKey(keyRows)
			if keyScanErr != nil {
				return pgutil.WrapDBError("scan app key", keyScanErr)
			}
			keys = append(keys, stored.toKey(now))
		}
		if rowsErr := keyRows.Err(); rowsErr != nil {
			return pgutil.WrapDBError("list app keys", rowsErr)
		}
		result = BindingWithKeys{Binding: binding, Keys: keys}
		return nil
	})
	if txErr != nil {
		return BindingWithKeys{}, txErr
	}
	return result, nil
}

// AcknowledgeAllBindings implements AdminStore.
func (store *PGStore) AcknowledgeAllBindings(ctx context.Context, appIDs []string, now time.Time) (int, error) {
	tag, execErr := store.pool.Exec(ctx,
		`UPDATE app_bindings SET acknowledged_at = $2
WHERE app_id = ANY($1) AND registrar_id IS NOT NULL AND acknowledged_at IS NULL`,
		appIDs, storeTime(now))
	if execErr != nil {
		return 0, pgutil.WrapDBError("acknowledge app bindings", execErr)
	}
	return int(tag.RowsAffected()), nil
}

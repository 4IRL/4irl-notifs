package personal

import (
	"bytes"
	"context"
	"errors"
	"maps"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const destinationColumns = `id, person_hash, registered_by_app_id, registrar_removed_at, label, type, config,
	sealed_secrets, enabled, disabled_by, disabled_reason, created_at, updated_at`

// PGStore is the Postgres-backed personal destination store. It is covered by
// the integration tests, not unit tests.
type PGStore struct {
	pool   *pgxpool.Pool
	sealer *seal.Sealer
}

// NewPGStore returns a PGStore over pool. The sealer seals new secrets under the
// final row id; a nil sealer makes Insert fail closed.
func NewPGStore(pool *pgxpool.Pool, sealer *seal.Sealer) *PGStore {
	return &PGStore{pool: pool, sealer: sealer}
}

// Insert creates a personal destination. In one transaction it takes the
// per-person advisory lock, enforces MaxPerPersonPerApp for the registering app (ErrLimitReached), draws the
// id from the sequence, seals the secrets under that final id, writes the row
// and, when requested, opts the registering app in.
func (store *PGStore) Insert(ctx context.Context, input NewDestination) (Destination, error) {
	var inserted Destination
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, lockErr := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended('personal:' || $1, 0))`, input.PersonHash); lockErr != nil {
			return pgutil.WrapDBError("lock person for personal destination create", lockErr)
		}
		var existing int
		if countErr := tx.QueryRow(ctx,
			`SELECT count(*) FROM personal_destinations WHERE person_hash = $1 AND registered_by_app_id = $2`,
			input.PersonHash, input.RegisteredByAppID).Scan(&existing); countErr != nil {
			return pgutil.WrapDBError("count personal destinations", countErr)
		}
		if existing >= MaxPerPersonPerApp {
			return ErrLimitReached
		}

		var destinationID int64
		if idErr := tx.QueryRow(ctx,
			`SELECT nextval(pg_get_serial_sequence('personal_destinations', 'id'))`).Scan(&destinationID); idErr != nil {
			return pgutil.WrapDBError("allocate personal destination id", idErr)
		}
		sealed, sealErr := SealSecrets(store.sealer, destinationID, input.Secrets)
		if sealErr != nil {
			return sealErr
		}

		row, scanErr := scanDestination(tx.QueryRow(ctx,
			`INSERT INTO personal_destinations (id, person_hash, registered_by_app_id, label, type, config, sealed_secrets)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+destinationColumns,
			destinationID, input.PersonHash, input.RegisteredByAppID, input.Label, input.Type,
			configOrEmpty(input.Config), sealed))
		if scanErr != nil {
			if pgutil.ErrorCode(scanErr) == pgutil.SQLStateCheckViolation {
				return registry.CheckViolationError("insert personal destination", scanErr)
			}
			return pgutil.WrapDBError("insert personal destination", scanErr)
		}
		if input.OptIn {
			if _, optInErr := tx.Exec(ctx,
				`INSERT INTO personal_destination_opt_ins (app_id, personal_destination_id) VALUES ($1, $2)`,
				input.RegisteredByAppID, destinationID); optInErr != nil {
				return pgutil.WrapDBError("insert initial opt-in", optInErr)
			}
		}
		inserted = row
		return nil
	})
	if txErr != nil {
		return Destination{}, txErr
	}
	return inserted, nil
}

// Get returns the destination with that id, or ErrNotFound.
func (store *PGStore) Get(ctx context.Context, id int64) (Destination, error) {
	destination, scanErr := scanDestination(store.pool.QueryRow(ctx,
		`SELECT `+destinationColumns+` FROM personal_destinations WHERE id = $1`, id))
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return Destination{}, ErrNotFound
		}
		return Destination{}, pgutil.WrapDBError("get personal destination", scanErr)
	}
	return destination, nil
}

// ListByPerson returns the person's destinations, newest first.
func (store *PGStore) ListByPerson(ctx context.Context, personHash string) ([]Destination, error) {
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+destinationColumns+` FROM personal_destinations
WHERE person_hash = $1 ORDER BY created_at DESC, id DESC`, personHash)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list personal destinations", queryErr)
	}
	return collectDestinations(rows, "list personal destinations")
}

// ListForAdmin returns the newest AdminListLimit destinations across everyone
// (empty personHash), or every destination of one person when personHash is set
// (that set is bounded by MaxPerPersonPerApp per app, so the cap is not applied),
// and the total count matching the filter.
func (store *PGStore) ListForAdmin(ctx context.Context, personHash string) ([]Destination, int, error) {
	var total int
	if countErr := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM personal_destinations WHERE ($1 = '' OR person_hash = $1)`, personHash).Scan(&total); countErr != nil {
		return nil, 0, pgutil.WrapDBError("count personal destinations for admin", countErr)
	}
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+destinationColumns+` FROM personal_destinations WHERE ($1 = '' OR person_hash = $1)
ORDER BY created_at DESC, id DESC LIMIT (CASE WHEN $1 = '' THEN $2::int END)`, personHash, AdminListLimit)
	if queryErr != nil {
		return nil, 0, pgutil.WrapDBError("list personal destinations for admin", queryErr)
	}
	destinations, collectErr := collectDestinations(rows, "list personal destinations for admin")
	if collectErr != nil {
		return nil, 0, collectErr
	}
	return destinations, total, nil
}

// CountByPerson returns how many destinations the person holds.
func (store *PGStore) CountByPerson(ctx context.Context, personHash string) (int, error) {
	var count int
	if countErr := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM personal_destinations WHERE person_hash = $1`, personHash).Scan(&count); countErr != nil {
		return 0, pgutil.WrapDBError("count personal destinations", countErr)
	}
	return count, nil
}

// Update locks the row with SELECT ... FOR UPDATE, runs mutate and persists the
// returned label, config, sealed secrets, enabled flag, disabled_by and
// disabled reason (person, registrar and type are immutable), bumping
// updated_at only when one of them changed (an unchanged result skips the write). A mutate error rolls back and is returned unchanged, so a
// rejected PATCH writes nothing.
func (store *PGStore) Update(
	ctx context.Context, id int64, mutate func(Destination) (Destination, error),
) (Destination, error) {
	var updated Destination
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, scanErr := scanDestination(tx.QueryRow(ctx,
			`SELECT `+destinationColumns+` FROM personal_destinations WHERE id = $1 FOR UPDATE`, id))
		if scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return pgutil.WrapDBError("lock personal destination", scanErr)
		}
		mutated, mutateErr := mutate(current)
		if mutateErr != nil {
			return mutateErr
		}
		if mutableFieldsEqual(current, mutated) {
			// Nothing changed: no write, so updated_at stays untouched.
			updated = current
			return nil
		}
		saved, saveErr := scanDestination(tx.QueryRow(ctx,
			`UPDATE personal_destinations
SET label = $2, config = $3, sealed_secrets = $4, enabled = $5, disabled_by = $6, disabled_reason = $7, updated_at = now()
WHERE id = $1 RETURNING `+destinationColumns,
			id, mutated.Label, configOrEmpty(mutated.Config), mutated.SealedSecrets,
			mutated.Enabled, mutated.DisabledBy, mutated.DisabledReason))
		if saveErr != nil {
			if pgutil.ErrorCode(saveErr) == pgutil.SQLStateCheckViolation {
				return registry.CheckViolationError("update personal destination", saveErr)
			}
			return pgutil.WrapDBError("update personal destination", saveErr)
		}
		updated = saved
		return nil
	})
	if txErr != nil {
		return Destination{}, txErr
	}
	return updated, nil
}

// Delete removes the destination (its opt-ins cascade) and reports whether a row existed.
func (store *PGStore) Delete(ctx context.Context, id int64) (bool, error) {
	tag, execErr := store.pool.Exec(ctx, `DELETE FROM personal_destinations WHERE id = $1`, id)
	if execErr != nil {
		return false, pgutil.WrapDBError("delete personal destination", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteManaged deletes the destination in one statement that carries the
// ownership checks (the person's row, registered by appID, registrar not removed),
// so a registrar removal racing the delete cannot be overridden. It reports
// whether a row was deleted.
func (store *PGStore) DeleteManaged(ctx context.Context, id int64, appID, personHash string) (bool, error) {
	tag, execErr := store.pool.Exec(ctx,
		`DELETE FROM personal_destinations
WHERE id = $1 AND person_hash = $2 AND registered_by_app_id = $3 AND registrar_removed_at IS NULL`,
		id, personHash, appID)
	if execErr != nil {
		return false, pgutil.WrapDBError("delete managed personal destination", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// OptIn opts appID in for the destination, idempotently. It locks the
// destination row FOR SHARE in the same transaction as the insert, so a
// concurrent reap either waits for it (and then sees the opt-in) or has already
// removed the row, which is ErrNotFound; it never surfaces a foreign-key error.
func (store *PGStore) OptIn(ctx context.Context, appID string, id int64) error {
	return pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var locked int64
		lockErr := tx.QueryRow(ctx,
			`SELECT id FROM personal_destinations WHERE id = $1 FOR SHARE`, id).Scan(&locked)
		if lockErr != nil {
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return pgutil.WrapDBError("lock personal destination for opt-in", lockErr)
		}
		if _, insertErr := tx.Exec(ctx,
			`INSERT INTO personal_destination_opt_ins (app_id, personal_destination_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING`, appID, id); insertErr != nil {
			return pgutil.WrapDBError("insert opt-in", insertErr)
		}
		return nil
	})
}

// OptOut removes appID's opt-in, idempotently. Lock-first: it locks the
// destination row FOR UPDATE before deleting the opt-in, then in a fresh
// statement deletes the row when its registrar was removed and no opt-in is
// left. It reports whether the row was reaped; a missing row is ErrNotFound.
func (store *PGStore) OptOut(ctx context.Context, appID string, id int64) (reaped bool, err error) {
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var locked int64
		lockErr := tx.QueryRow(ctx,
			`SELECT id FROM personal_destinations WHERE id = $1 FOR UPDATE`, id).Scan(&locked)
		if lockErr != nil {
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return pgutil.WrapDBError("lock personal destination for opt-out", lockErr)
		}
		if _, deleteErr := tx.Exec(ctx,
			`DELETE FROM personal_destination_opt_ins WHERE app_id = $1 AND personal_destination_id = $2`,
			appID, id); deleteErr != nil {
			return pgutil.WrapDBError("delete opt-in", deleteErr)
		}
		reapedIDs, reapErr := reapOrphans(ctx, tx, []int64{id})
		if reapErr != nil {
			return reapErr
		}
		reaped = len(reapedIDs) > 0
		return nil
	})
	if txErr != nil {
		return false, txErr
	}
	return reaped, nil
}

// ReapOrphans deletes the listed destinations that are orphans (registrar
// removed, no opt-in left). It locks the rows FOR UPDATE in ascending id order
// first, then re-checks in a fresh statement, so it cannot race an opt-in. It
// returns the ids it deleted. Callers that also delete opt-ins (app teardown,
// person cleanup) must lock their rows before deleting those opt-ins; see the
// plan's Reap serialisation rule.
func (store *PGStore) ReapOrphans(ctx context.Context, ids []int64) ([]int64, error) {
	var reapedIDs []int64
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, lockErr := tx.Exec(ctx,
			`SELECT id FROM personal_destinations WHERE id = ANY($1) ORDER BY id FOR UPDATE`, nonNil(ids)); lockErr != nil {
			return pgutil.WrapDBError("lock personal destinations for reap", lockErr)
		}
		reaped, reapErr := reapOrphans(ctx, tx, ids)
		if reapErr != nil {
			return reapErr
		}
		reapedIDs = reaped
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return reapedIDs, nil
}

// ForgetPersonForApp removes appID's opt-ins for the person's destinations and
// deletes the rows that leave with no opt-in when their registrar is appID or
// already removed, plus any of the person's registrar-removed rows with no
// opt-in. Lock-first (Reap serialisation): the affected rows are locked FOR
// UPDATE in ascending id order before any opt-in is deleted, and the delete
// re-checks "no opt-ins" in a fresh statement. It returns the opt-in and
// destination counts removed.
func (store *PGStore) ForgetPersonForApp(
	ctx context.Context, appID, personHash string,
) (optIns int64, destinations int64, err error) {
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, lockErr := tx.Exec(ctx,
			`SELECT id FROM personal_destinations pd
WHERE pd.person_hash = $2
  AND (pd.registrar_removed_at IS NOT NULL
       OR EXISTS (SELECT 1 FROM personal_destination_opt_ins oi
                  WHERE oi.personal_destination_id = pd.id AND oi.app_id = $1))
ORDER BY pd.id FOR UPDATE`, appID, personHash); lockErr != nil {
			return pgutil.WrapDBError("lock personal destinations for person cleanup", lockErr)
		}
		optInRows, optInErr := tx.Query(ctx,
			`DELETE FROM personal_destination_opt_ins oi USING personal_destinations pd
WHERE oi.app_id = $1 AND oi.personal_destination_id = pd.id AND pd.person_hash = $2
RETURNING pd.id`, appID, personHash)
		if optInErr != nil {
			return pgutil.WrapDBError("delete person opt-ins for app", optInErr)
		}
		freedIDs, collectErr := pgx.CollectRows(optInRows, pgx.RowTo[int64])
		if collectErr != nil {
			return pgutil.WrapDBError("delete person opt-ins for app", collectErr)
		}
		optIns = int64(len(freedIDs))
		// A row the app opted in to after the first lock statement's snapshot is in freedIDs but was not
		// locked; lock it (ascending id) so the re-check below cannot race another app's opt-in PUT.
		if _, relockErr := tx.Exec(ctx,
			`SELECT id FROM personal_destinations WHERE id = ANY($1) ORDER BY id FOR UPDATE`,
			nonNil(freedIDs)); relockErr != nil {
			return pgutil.WrapDBError("relock freed personal destinations", relockErr)
		}
		deleteTag, deleteErr := tx.Exec(ctx,
			`DELETE FROM personal_destinations pd
WHERE pd.person_hash = $2
  AND NOT EXISTS (SELECT 1 FROM personal_destination_opt_ins oi WHERE oi.personal_destination_id = pd.id)
  AND (pd.registrar_removed_at IS NOT NULL
       OR (pd.id = ANY($3) AND pd.registered_by_app_id = $1))`, appID, personHash, nonNil(freedIDs))
		if deleteErr != nil {
			return pgutil.WrapDBError("delete person destinations for app", deleteErr)
		}
		destinations = deleteTag.RowsAffected()
		return nil
	})
	if txErr != nil {
		return 0, 0, txErr
	}
	return optIns, destinations, nil
}

// ForgetPerson deletes every personal destination of the person (opt-ins
// cascade) and returns how many rows it removed. The rows are locked FOR UPDATE
// in ascending id order first.
func (store *PGStore) ForgetPerson(ctx context.Context, personHash string) (int64, error) {
	var removed int64
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, lockErr := tx.Exec(ctx,
			`SELECT id FROM personal_destinations WHERE person_hash = $1 ORDER BY id FOR UPDATE`,
			personHash); lockErr != nil {
			return pgutil.WrapDBError("lock personal destinations for person delete", lockErr)
		}
		tag, deleteErr := tx.Exec(ctx, `DELETE FROM personal_destinations WHERE person_hash = $1`, personHash)
		if deleteErr != nil {
			return pgutil.WrapDBError("delete person personal destinations", deleteErr)
		}
		removed = tag.RowsAffected()
		return nil
	})
	if txErr != nil {
		return 0, txErr
	}
	return removed, nil
}

// reapOrphans is the re-check-and-delete half, run after the caller holds the
// row locks. It runs as its own statement so it sees any opt-in committed while
// the caller waited for the lock.
func reapOrphans(ctx context.Context, querier pgutil.Querier, ids []int64) ([]int64, error) {
	rows, queryErr := querier.Query(ctx,
		`DELETE FROM personal_destinations pd
WHERE pd.id = ANY($1) AND pd.registrar_removed_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM personal_destination_opt_ins oi WHERE oi.personal_destination_id = pd.id)
RETURNING pd.id`, nonNil(ids))
	if queryErr != nil {
		return nil, pgutil.WrapDBError("reap personal destinations", queryErr)
	}
	defer rows.Close()
	var reaped []int64
	for rows.Next() {
		var reapedID int64
		if scanErr := rows.Scan(&reapedID); scanErr != nil {
			return nil, pgutil.WrapDBError("scan reaped personal destination", scanErr)
		}
		reaped = append(reaped, reapedID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("reap personal destinations", rowsErr)
	}
	return reaped, nil
}

// OptedInAppIDs returns the app ids opted in for the destination, sorted.
func (store *PGStore) OptedInAppIDs(ctx context.Context, id int64) ([]string, error) {
	rows, queryErr := store.pool.Query(ctx,
		`SELECT app_id FROM personal_destination_opt_ins WHERE personal_destination_id = $1 ORDER BY app_id COLLATE "C"`, id)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list opted-in apps", queryErr)
	}
	defer rows.Close()
	appIDs := []string{}
	for rows.Next() {
		var appID string
		if scanErr := rows.Scan(&appID); scanErr != nil {
			return nil, pgutil.WrapDBError("scan opted-in app", scanErr)
		}
		appIDs = append(appIDs, appID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("list opted-in apps", rowsErr)
	}
	return appIDs, nil
}

// IsOptedIn reports whether appID has an opt-in for the destination.
func (store *PGStore) IsOptedIn(ctx context.Context, appID string, id int64) (bool, error) {
	var optedIn bool
	if scanErr := store.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM personal_destination_opt_ins WHERE app_id = $1 AND personal_destination_id = $2)`,
		appID, id).Scan(&optedIn); scanErr != nil {
		return false, pgutil.WrapDBError("check opt-in", scanErr)
	}
	return optedIn, nil
}

// ListEnabledOptedInByIDs returns the destinations among ids that are the
// person's, enabled and opted in for appID, ordered by id. Any other id is
// absent from the result.
func (store *PGStore) ListEnabledOptedInByIDs(ctx context.Context, appID, personHash string, ids []int64) ([]Destination, error) {
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+destinationColumns+` FROM personal_destinations pd
WHERE pd.person_hash = $2 AND pd.enabled AND pd.id = ANY($3)
  AND EXISTS (SELECT 1 FROM personal_destination_opt_ins oi
              WHERE oi.personal_destination_id = pd.id AND oi.app_id = $1)
ORDER BY pd.id`, appID, personHash, ids)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list selected personal destinations", queryErr)
	}
	return collectDestinations(rows, "list selected personal destinations")
}

// DisableBySystem disables an enabled destination as 'system' with reason. It
// touches only enabled rows: an operator- or app-disabled row is left alone and
// reported as no transition.
func (store *PGStore) DisableBySystem(ctx context.Context, id int64, reason string) (bool, error) {
	tag, execErr := store.pool.Exec(ctx,
		`UPDATE personal_destinations
SET enabled = false, disabled_by = 'system', disabled_reason = $2, updated_at = now()
WHERE id = $1 AND enabled`, id, reason)
	if execErr != nil {
		return false, pgutil.WrapDBError("disable personal destination", execErr)
	}
	return tag.RowsAffected() > 0, nil
}

// mutableFieldsEqual reports whether two destinations agree on every field
// Update persists. The store skips the write (and the updated_at bump) when a
// mutate step changed nothing.
func mutableFieldsEqual(left, right Destination) bool {
	return left.Label == right.Label &&
		maps.Equal(left.Config, right.Config) &&
		bytes.Equal(left.SealedSecrets, right.SealedSecrets) &&
		left.Enabled == right.Enabled &&
		equalPointers(left.DisabledBy, right.DisabledBy) &&
		equalPointers(left.DisabledReason, right.DisabledReason)
}

func equalPointers[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func nonNil(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func configOrEmpty(config map[string]string) map[string]string {
	if config == nil {
		return map[string]string{}
	}
	return config
}

func collectDestinations(rows pgx.Rows, operation string) ([]Destination, error) {
	defer rows.Close()
	destinations := []Destination{}
	for rows.Next() {
		destination, scanErr := scanDestination(rows)
		if scanErr != nil {
			return nil, pgutil.WrapDBError(operation, scanErr)
		}
		destinations = append(destinations, destination)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError(operation, rowsErr)
	}
	return destinations, nil
}

func scanDestination(row pgutil.RowScanner) (Destination, error) {
	var destination Destination
	var disabledBy *string
	if scanErr := row.Scan(
		&destination.ID, &destination.PersonHash, &destination.RegisteredByAppID, &destination.RegistrarRemovedAt,
		&destination.Label, &destination.Type, &destination.Config, &destination.SealedSecrets,
		&destination.Enabled, &disabledBy, &destination.DisabledReason,
		&destination.CreatedAt, &destination.UpdatedAt,
	); scanErr != nil {
		return Destination{}, scanErr
	}
	if disabledBy != nil {
		converted := DisabledBy(*disabledBy)
		destination.DisabledBy = &converted
	}
	if destination.Config == nil {
		destination.Config = map[string]string{}
	}
	return destination, nil
}

package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// rollbackTimeout bounds the deferred rollback, which runs detached from the
// request context so a cancelled request still releases its locks.
const rollbackTimeout = 5 * time.Second

// maxInsertAttempts bounds the InsertMessage retry when the conflicting row is
// deleted between the conflict and the re-select.
const maxInsertAttempts = 2

// readSnapshotOptions gives a multi-statement read one consistent snapshot.
var readSnapshotOptions = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// Column lists. The uuid columns are selected as text so scanning never depends
// on the driver's uuid codec, and the nullable to_* columns are coalesced so the
// plain-string Envelope fields read back as "" when omitted.
const (
	messageColumns = `id::text, app_id, channel, idempotency_key,
	COALESCE(to_destination, ''), COALESCE(to_recipient, ''), deliver_at, critical, payload, display, created_at`

	deliveryColumns = `id, message_id::text, destination_id, destination_name, destination_type, state, dead_reason,
	attempt_count, budget_attempts, budget_started_at, next_attempt_at, last_status_code, delivered_at,
	created_at, updated_at`

	// deliveryOrder sorts bytewise (COLLATE "C") so ordering never depends on the
	// database's locale: destination names are [a-z0-9_] slugs, and glibc locales
	// ignore underscores when comparing.
	deliveryOrder = `ORDER BY destination_name COLLATE "C", id`
)

// canonicalUUIDPattern is the 8-4-4-4-12 hex form. A malformed id is rejected
// here, before the query, so Postgres never raises invalid_text_representation.
var canonicalUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// querier is the read/write surface shared by *pgxpool.Pool and pgx.Tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// PGStore is the Postgres-backed Store. It is covered by the integration
// tests, not unit tests.
type PGStore struct {
	pool *pgxpool.Pool
}

// Compile-time check that PGStore satisfies Store.
var _ Store = (*PGStore)(nil)

// NewPGStore returns a PGStore over pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// InsertMessage persists envelope and one delivery per target atomically. The
// insert uses ON CONFLICT DO NOTHING on (app_id, idempotency_key), so a
// concurrent submit of the same key waits for the first transaction and then
// takes the duplicate path: it re-selects the existing message and inserts
// nothing. If the existing row vanishes between the conflict and the re-select
// (for example a retention sweep), the insert is retried once. At least one
// target is required; a stored message always has a delivery.
func (store *PGStore) InsertMessage(
	ctx context.Context, envelope Envelope, targets []DeliveryTarget,
) (StoredMessage, bool, error) {
	if len(targets) == 0 {
		return StoredMessage{}, false, errors.New("insert message: no delivery targets")
	}
	payload := []byte(envelope.Payload)
	displayJSON, displayErr := encodeDisplay(envelope.Display)
	if displayErr != nil {
		return StoredMessage{}, false, displayErr
	}

	for range maxInsertAttempts {
		stored, duplicate, insertErr := store.insertOnce(ctx, envelope, targets, payload, displayJSON)
		if errors.Is(insertErr, ErrMessageNotFound) {
			continue
		}
		return stored, duplicate, insertErr
	}
	return StoredMessage{}, false, errors.New("insert message: conflicting message deleted during every attempt")
}

// insertOnce runs one insert transaction. It returns ErrMessageNotFound only
// when the insert hit a conflict and the conflicting row was gone by the
// re-select.
func (store *PGStore) insertOnce(
	ctx context.Context, envelope Envelope, targets []DeliveryTarget, payload []byte, displayJSON []byte,
) (StoredMessage, bool, error) {
	var stored StoredMessage
	var duplicate bool
	txErr := store.withTx(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var messageID string
		insertErr := tx.QueryRow(ctx,
			`INSERT INTO messages (app_id, channel, idempotency_key, to_destination, to_recipient, deliver_at, critical, payload, display)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (app_id, idempotency_key) DO NOTHING
RETURNING id::text`,
			envelope.AppID, envelope.Channel, envelope.IdempotencyKey,
			nullIfEmpty(envelope.ToDestination), nullIfEmpty(envelope.ToRecipient),
			envelope.DeliverAt, envelope.Critical, payload, displayJSON,
		).Scan(&messageID)
		if errors.Is(insertErr, pgx.ErrNoRows) {
			existing, loadErr := queryMessage(ctx, tx, `app_id = $1 AND idempotency_key = $2`,
				envelope.AppID, envelope.IdempotencyKey)
			if loadErr != nil {
				return loadErr
			}
			stored = existing
			duplicate = true
			return nil
		}
		if insertErr != nil {
			return wrapDBError("insert message", insertErr)
		}

		if deliveriesErr := insertDeliveries(ctx, tx, messageID, targets); deliveriesErr != nil {
			return deliveriesErr
		}
		inserted, loadErr := queryMessage(ctx, tx, `id = $1::text::uuid`, messageID)
		if loadErr != nil {
			return loadErr
		}
		stored = inserted
		return nil
	})
	if txErr != nil {
		return StoredMessage{}, false, txErr
	}
	return stored, duplicate, nil
}

// GetMessage returns the message and its deliveries, or ErrMessageNotFound.
// An id that is not a canonical UUID is a miss, not a database error.
func (store *PGStore) GetMessage(ctx context.Context, id string) (StoredMessage, error) {
	if !canonicalUUIDPattern.MatchString(id) {
		return StoredMessage{}, ErrMessageNotFound
	}
	return store.readMessage(ctx, `id = $1::text::uuid`, id)
}

// GetMessageByKey returns the message for (appID, idempotencyKey), or
// ErrMessageNotFound.
func (store *PGStore) GetMessageByKey(
	ctx context.Context, appID string, idempotencyKey string,
) (StoredMessage, error) {
	return store.readMessage(ctx, `app_id = $1 AND idempotency_key = $2`, appID, idempotencyKey)
}

// FindMessageByKey is GetMessageByKey with absence reported as found=false and
// a nil error.
func (store *PGStore) FindMessageByKey(
	ctx context.Context, appID string, idempotencyKey string,
) (StoredMessage, bool, error) {
	found, getErr := store.GetMessageByKey(ctx, appID, idempotencyKey)
	if errors.Is(getErr, ErrMessageNotFound) {
		return StoredMessage{}, false, nil
	}
	if getErr != nil {
		return StoredMessage{}, false, getErr
	}
	return found, true, nil
}

// readMessage loads a message and its deliveries from one REPEATABLE READ
// read-only snapshot, so a concurrent delivery update can never be seen
// half-applied across the two statements.
func (store *PGStore) readMessage(ctx context.Context, where string, args ...any) (StoredMessage, error) {
	var stored StoredMessage
	txErr := store.withTx(ctx, readSnapshotOptions, func(tx pgx.Tx) error {
		var loadErr error
		stored, loadErr = queryMessage(ctx, tx, where, args...)
		return loadErr
	})
	if txErr != nil {
		return StoredMessage{}, txErr
	}
	return stored, nil
}

// withTx runs fn in a transaction with the given options and commits when it
// returns nil. fn's error is returned unchanged so sentinels survive; the
// rollback is deferred and harmless after a successful commit.
func (store *PGStore) withTx(ctx context.Context, options pgx.TxOptions, fn func(tx pgx.Tx) error) error {
	tx, beginErr := store.pool.BeginTx(ctx, options)
	if beginErr != nil {
		return wrapDBError("begin transaction", beginErr)
	}
	defer func() {
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancelRollback()
		_ = tx.Rollback(rollbackCtx)
	}()

	if fnErr := fn(tx); fnErr != nil {
		return fnErr
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return wrapDBError("commit transaction", commitErr)
	}
	return nil
}

// insertDeliveries bulk-inserts one pending delivery per target.
func insertDeliveries(ctx context.Context, tx pgx.Tx, messageID string, targets []DeliveryTarget) error {
	destinationIDs := make([]int64, 0, len(targets))
	destinationNames := make([]string, 0, len(targets))
	destinationTypes := make([]string, 0, len(targets))
	for _, target := range targets {
		destinationIDs = append(destinationIDs, target.DestinationID)
		destinationNames = append(destinationNames, target.DestinationName)
		destinationTypes = append(destinationTypes, target.DestinationType)
	}
	if _, execErr := tx.Exec(ctx,
		`INSERT INTO message_deliveries (message_id, destination_id, destination_name, destination_type)
SELECT $1::text::uuid, destination_id, destination_name, destination_type
FROM unnest($2::bigint[], $3::text[], $4::text[]) AS target(destination_id, destination_name, destination_type)`,
		messageID, destinationIDs, destinationNames, destinationTypes); execErr != nil {
		return wrapDBError("insert deliveries", execErr)
	}
	return nil
}

// queryMessage loads the message matching the where clause and its deliveries,
// or ErrMessageNotFound.
func queryMessage(ctx context.Context, db querier, where string, args ...any) (StoredMessage, error) {
	stored, scanErr := scanMessage(db.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE `+where, args...))
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return StoredMessage{}, ErrMessageNotFound
		}
		return StoredMessage{}, wrapDBError("get message", scanErr)
	}

	rows, queryErr := db.Query(ctx,
		`SELECT `+deliveryColumns+` FROM message_deliveries WHERE message_id = $1::text::uuid `+deliveryOrder, stored.ID)
	if queryErr != nil {
		return StoredMessage{}, wrapDBError("get deliveries", queryErr)
	}
	deliveries, collectErr := collectDeliveries(rows)
	if collectErr != nil {
		return StoredMessage{}, wrapDBError("get deliveries", collectErr)
	}
	stored.Deliveries = deliveries
	return stored, nil
}

// scanMessage reads one row selected with messageColumns (Deliveries is left
// for the caller).
func scanMessage(row rowScanner) (StoredMessage, error) {
	var stored StoredMessage
	var payload, displayJSON []byte
	if scanErr := row.Scan(
		&stored.ID, &stored.Envelope.AppID, &stored.Envelope.Channel, &stored.Envelope.IdempotencyKey,
		&stored.Envelope.ToDestination, &stored.Envelope.ToRecipient, &stored.Envelope.DeliverAt,
		&stored.Envelope.Critical, &payload, &displayJSON, &stored.CreatedAt,
	); scanErr != nil {
		return StoredMessage{}, scanErr
	}
	stored.Envelope.Payload = json.RawMessage(payload)
	if displayJSON != nil {
		var display Display
		if unmarshalErr := json.Unmarshal(displayJSON, &display); unmarshalErr != nil {
			return StoredMessage{}, fmt.Errorf("decode display: %w", unmarshalErr)
		}
		stored.Envelope.Display = &display
	}
	return stored, nil
}

// collectDeliveries drains rows into a non-nil slice and closes them.
func collectDeliveries(rows pgx.Rows) ([]Delivery, error) {
	defer rows.Close()
	deliveries := []Delivery{}
	for rows.Next() {
		delivery, scanErr := scanDelivery(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		deliveries = append(deliveries, delivery)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return deliveries, nil
}

// scanDelivery reads one row selected with deliveryColumns.
func scanDelivery(row rowScanner) (Delivery, error) {
	var delivery Delivery
	if scanErr := row.Scan(
		&delivery.ID, &delivery.MessageID, &delivery.DestinationID, &delivery.DestinationName,
		&delivery.DestinationType, &delivery.State, &delivery.DeadReason, &delivery.AttemptCount,
		&delivery.BudgetAttempts, &delivery.BudgetStartedAt, &delivery.NextAttemptAt,
		&delivery.LastStatusCode, &delivery.DeliveredAt, &delivery.CreatedAt, &delivery.UpdatedAt,
	); scanErr != nil {
		return Delivery{}, scanErr
	}
	return delivery, nil
}

// encodeDisplay returns the display as JSON, or nil (SQL NULL) when absent.
func encodeDisplay(display *Display) ([]byte, error) {
	if display == nil {
		return nil, nil
	}
	encoded, marshalErr := json.Marshal(display)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode display: %w", marshalErr)
	}
	return encoded, nil
}

// nullIfEmpty maps an omitted (empty) string to SQL NULL, so the
// num_nonnulls(to_destination, to_recipient) <= 1 CHECK sees NULL, not ”.
func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// wrapDBError labels a database error with its operation. A *pgconn.PgError is
// flattened to its message and SQLSTATE without %w: its Detail field can hold
// the whole failing row, so it must not stay reachable through errors.As or
// %+v. Other errors (context cancellation, connection failures) keep their
// chain.
func wrapDBError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("%s: %s (SQLSTATE %s)", operation, pgErr.Message, pgErr.Code)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

package message

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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

// Attempt text caps, matching the delivery_attempts CHECK constraints: the error
// is capped in runes (char_length) and the response snippet in bytes
// (octet_length).
const (
	maxAttemptErrorRunes    = 500
	maxResponseSnippetBytes = 1024
)

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

	attemptColumns = `id, delivery_id, attempt_number, outcome, status_code, latency_ms, error, response_snippet, started_at`

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

// ClaimDue leases the oldest due pending delivery. The SELECT ... FOR UPDATE
// SKIP LOCKED and the lease UPDATE run in one transaction on one connection, so
// a second worker either skips the locked row or sees the pushed-out
// next_attempt_at; no transaction spans the HTTP attempt that follows.
func (store *PGStore) ClaimDue(
	ctx context.Context, now time.Time, lease time.Duration,
) (Claim, bool, error) {
	if lease <= 0 {
		return Claim{}, false, errors.New("claim due: lease must be positive")
	}
	var claim Claim
	var found bool
	txErr := store.withTx(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
		due, selectErr := scanDelivery(tx.QueryRow(ctx,
			`SELECT `+deliveryColumns+` FROM message_deliveries
WHERE state = 'pending' AND next_attempt_at <= $1
ORDER BY next_attempt_at, id
FOR UPDATE SKIP LOCKED
LIMIT 1`, now))
		if errors.Is(selectErr, pgx.ErrNoRows) {
			return nil
		}
		if selectErr != nil {
			return wrapDBError("claim delivery", selectErr)
		}

		leased, leaseErr := scanDelivery(tx.QueryRow(ctx,
			`UPDATE message_deliveries SET next_attempt_at = $2, updated_at = now()
WHERE id = $1 RETURNING `+deliveryColumns, due.ID, now.Add(lease)))
		if leaseErr != nil {
			return wrapDBError("lease delivery", leaseErr)
		}
		parent, parentErr := queryMessageHead(ctx, tx, `id = $1::text::uuid`, leased.MessageID)
		if parentErr != nil {
			return parentErr
		}
		claim = Claim{Delivery: leased, Message: parent}
		found = true
		return nil
	})
	if txErr != nil {
		return Claim{}, false, txErr
	}
	return claim, found, nil
}

// RecordAttempt logs the attempt and applies its transition under a row lock on
// the delivery, so concurrent recorders serialize and attempt numbers stay
// gap-free and monotonic.
func (store *PGStore) RecordAttempt(ctx context.Context, result AttemptResult) (Delivery, error) {
	if validateErr := validateAttemptResult(result); validateErr != nil {
		return Delivery{}, validateErr
	}
	record := result.Record
	errorText := cleanOptionalText(record.Error, maxAttemptErrorRunes, truncateRunes)
	snippet := cleanOptionalText(record.ResponseSnippet, maxResponseSnippetBytes, truncateBytes)

	var updated Delivery
	txErr := store.withTx(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, lockErr := lockDelivery(ctx, tx, record.DeliveryID)
		if lockErr != nil {
			return lockErr
		}
		if current.State != StatePending {
			updated = current
			return nil
		}

		if _, insertErr := tx.Exec(ctx,
			`INSERT INTO delivery_attempts
  (delivery_id, attempt_number, outcome, status_code, latency_ms, error, response_snippet, started_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			current.ID, current.AttemptCount+1, record.Outcome, record.StatusCode, record.LatencyMS,
			errorText, snippet, record.StartedAt); insertErr != nil {
			return wrapDBError("insert attempt", insertErr)
		}

		nextAttemptAt := current.NextAttemptAt
		if result.NextState == StatePending {
			nextAttemptAt = result.NextAttemptAt
		}
		var updateErr error
		updated, updateErr = scanDelivery(tx.QueryRow(ctx,
			`UPDATE message_deliveries SET
  state = $2, dead_reason = $3,
  attempt_count = attempt_count + 1, budget_attempts = budget_attempts + 1,
  next_attempt_at = $4, last_status_code = $5,
  delivered_at = CASE WHEN $2::text = 'delivered' THEN now() ELSE delivered_at END,
  updated_at = now()
WHERE id = $1 RETURNING `+deliveryColumns,
			current.ID, result.NextState, result.DeadReason, nextAttemptAt, record.StatusCode))
		if updateErr != nil {
			return wrapDBError("update delivery", updateErr)
		}
		return nil
	})
	if txErr != nil {
		return Delivery{}, txErr
	}
	return updated, nil
}

// ListDeliveries returns up to limit deliveries in state, most recently updated
// first.
func (store *PGStore) ListDeliveries(ctx context.Context, state string, limit int) ([]Delivery, error) {
	if !isDeliveryState(state) {
		return nil, &ValidationError{Message: "invalid state"}
	}
	if limit < 1 {
		return nil, &ValidationError{Message: "invalid limit"}
	}
	rows, queryErr := store.pool.Query(ctx,
		`SELECT `+deliveryColumns+` FROM message_deliveries WHERE state = $1
ORDER BY updated_at DESC, id DESC LIMIT $2`, state, limit)
	if queryErr != nil {
		return nil, wrapDBError("list deliveries", queryErr)
	}
	deliveries, collectErr := collectDeliveries(rows)
	if collectErr != nil {
		return nil, wrapDBError("list deliveries", collectErr)
	}
	return deliveries, nil
}

// ListAttempts returns a delivery's attempts in attempt order. The existence
// check and the read share one snapshot, so an absent delivery is
// ErrDeliveryNotFound rather than an empty list.
func (store *PGStore) ListAttempts(ctx context.Context, deliveryID int64) ([]Attempt, error) {
	attempts := []Attempt{}
	txErr := store.withTx(ctx, readSnapshotOptions, func(tx pgx.Tx) error {
		var exists bool
		if existsErr := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM message_deliveries WHERE id = $1)`, deliveryID,
		).Scan(&exists); existsErr != nil {
			return wrapDBError("check delivery", existsErr)
		}
		if !exists {
			return ErrDeliveryNotFound
		}

		rows, queryErr := tx.Query(ctx,
			`SELECT `+attemptColumns+` FROM delivery_attempts WHERE delivery_id = $1 ORDER BY attempt_number`,
			deliveryID)
		if queryErr != nil {
			return wrapDBError("list attempts", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var attempt Attempt
			if scanErr := rows.Scan(
				&attempt.ID, &attempt.DeliveryID, &attempt.AttemptNumber, &attempt.Outcome, &attempt.StatusCode,
				&attempt.LatencyMS, &attempt.Error, &attempt.ResponseSnippet, &attempt.StartedAt,
			); scanErr != nil {
				return wrapDBError("list attempts", scanErr)
			}
			attempts = append(attempts, attempt)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return wrapDBError("list attempts", rowsErr)
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return attempts, nil
}

// Replay returns a dead delivery to the queue with a fresh retry budget. The
// cumulative attempt_count is kept so log numbering stays monotonic.
func (store *PGStore) Replay(ctx context.Context, deliveryID int64, now time.Time) (Delivery, error) {
	if now.IsZero() {
		return Delivery{}, errors.New("replay delivery: now must be set")
	}
	var replayed Delivery
	txErr := store.withTx(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, lockErr := lockDelivery(ctx, tx, deliveryID)
		if lockErr != nil {
			return lockErr
		}
		if current.State != StateDead {
			return ErrDeliveryNotDead
		}
		var updateErr error
		replayed, updateErr = scanDelivery(tx.QueryRow(ctx,
			`UPDATE message_deliveries SET
  state = 'pending', dead_reason = NULL, budget_attempts = 0,
  budget_started_at = $2, next_attempt_at = $2, updated_at = now()
WHERE id = $1 RETURNING `+deliveryColumns, current.ID, now))
		if updateErr != nil {
			return wrapDBError("replay delivery", updateErr)
		}
		return nil
	})
	if txErr != nil {
		return Delivery{}, txErr
	}
	return replayed, nil
}

// DeleteExpired deletes messages older than cutoff that have no pending
// delivery. Deliveries and attempts cascade.
//
// Known, accepted race: a Replay of a dead delivery on a message sitting right
// at the retention cutoff can commit between this statement's snapshot and its
// cascade, and the replayed delivery is then deleted with its message. The
// window is one statement long and the operator is replaying a delivery that is
// already about to age out, so it is not worth a lock protocol.
func (store *PGStore) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, execErr := store.pool.Exec(ctx,
		`DELETE FROM messages m WHERE m.created_at < $1
AND NOT EXISTS (SELECT 1 FROM message_deliveries d WHERE d.message_id = m.id AND d.state = 'pending')`, cutoff)
	if execErr != nil {
		return 0, wrapDBError("delete expired messages", execErr)
	}
	return tag.RowsAffected(), nil
}

// lockDelivery selects a delivery FOR UPDATE, or ErrDeliveryNotFound.
func lockDelivery(ctx context.Context, db querier, deliveryID int64) (Delivery, error) {
	delivery, scanErr := scanDelivery(db.QueryRow(ctx,
		`SELECT `+deliveryColumns+` FROM message_deliveries WHERE id = $1 FOR UPDATE`, deliveryID))
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return Delivery{}, ErrDeliveryNotFound
	}
	if scanErr != nil {
		return Delivery{}, wrapDBError("lock delivery", scanErr)
	}
	return delivery, nil
}

// validateAttemptResult rejects a result whose transition would break the
// state / dead_reason invariant. It fails closed: an unknown NextState is an
// error, never a silent pending.
func validateAttemptResult(result AttemptResult) error {
	if (result.Record.Outcome == OutcomeSuccess) != (result.NextState == StateDelivered) {
		return errors.New("record attempt: outcome success must pair with state delivered, and no other outcome may")
	}
	switch result.NextState {
	case StatePending:
		if result.NextAttemptAt.IsZero() {
			return errors.New("record attempt: pending result needs next_attempt_at")
		}
		if result.DeadReason != nil {
			return errors.New("record attempt: dead_reason set on a non-dead result")
		}
	case StateDelivered:
		if result.DeadReason != nil {
			return errors.New("record attempt: dead_reason set on a non-dead result")
		}
	case StateDead:
		if result.DeadReason == nil || *result.DeadReason == "" {
			return errors.New("record attempt: dead result needs dead_reason")
		}
	default:
		return fmt.Errorf("record attempt: unknown next state %q", result.NextState)
	}
	return nil
}

// isDeliveryState reports whether state is one of the State* constants.
func isDeliveryState(state string) bool {
	return state == StatePending || state == StateDelivered || state == StateDead
}

// cleanOptionalText makes untrusted remote text storable and bounded: invalid
// UTF-8 becomes U+FFFD and NUL bytes (which Postgres text rejects) are dropped,
// then truncate cuts it to limit. A nil input stays nil (SQL NULL).
func cleanOptionalText(text *string, limit int, truncate func(string, int) string) *string {
	if text == nil {
		return nil
	}
	cleaned := strings.ToValidUTF8(*text, string(utf8.RuneError))
	cleaned = strings.ReplaceAll(cleaned, "\x00", "")
	cleaned = truncate(cleaned, limit)
	return &cleaned
}

// truncateRunes returns text cut to at most limit runes. text must be valid UTF-8.
func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	count := 0
	for index := range text {
		if count == limit {
			return text[:index]
		}
		count++
	}
	return text
}

// truncateBytes returns text cut to at most limit bytes, backing up to a rune
// boundary so a multi-byte rune is dropped whole. text must be valid UTF-8.
func truncateBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
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
	stored, headErr := queryMessageHead(ctx, db, where, args...)
	if headErr != nil {
		return StoredMessage{}, headErr
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

// queryMessageHead loads the message matching the where clause without its
// deliveries, or ErrMessageNotFound.
func queryMessageHead(ctx context.Context, db querier, where string, args ...any) (StoredMessage, error) {
	stored, scanErr := scanMessage(db.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE `+where, args...))
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return StoredMessage{}, ErrMessageNotFound
		}
		return StoredMessage{}, wrapDBError("get message", scanErr)
	}
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

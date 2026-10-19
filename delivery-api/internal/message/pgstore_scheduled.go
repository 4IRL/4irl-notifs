package message

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// Scheduled-message queries that must probe the scheduled-deliveries partial
// index (message_deliveries_scheduled_idx, WHERE state = 'scheduled'). They are
// constants so the integration tests can EXPLAIN exactly what runs.
const (
	// scheduledCountSQL counts an app's scheduled messages: it joins from the
	// app's messages (the UNIQUE (app_id, idempotency_key) index) to the partial
	// index with EXISTS.
	scheduledCountSQL = `SELECT count(*) FROM messages m WHERE m.app_id = $1
AND EXISTS (SELECT 1 FROM message_deliveries d WHERE d.message_id = m.id AND d.state = 'scheduled')`

	// releaseProbeSQL picks the messages with a due scheduled delivery, oldest
	// first.
	releaseProbeSQL = `SELECT message_id::text FROM message_deliveries
WHERE state = 'scheduled' AND next_attempt_at <= $1
GROUP BY message_id ORDER BY min(next_attempt_at), message_id LIMIT $2`

	// scheduledListSQL lists scheduled messages by deliver_at, then id.
	scheduledListSQL = `SELECT m.id::text, m.app_id, m.channel, m.idempotency_key, m.deliver_at, m.created_at,
m.to_recipient IS NOT NULL
FROM messages m
WHERE m.id IN (SELECT d.message_id FROM message_deliveries d WHERE d.state = 'scheduled')
ORDER BY m.deliver_at, m.id LIMIT $1`

	// scheduledTotalSQL counts every scheduled message.
	scheduledTotalSQL = `SELECT count(DISTINCT message_id) FROM message_deliveries WHERE state = 'scheduled'`
)

// lockAppScheduleCap serializes scheduled inserts for one app until the
// transaction ends, so the cap check and the insert are atomic per app. The
// lock key is namespaced so it cannot collide with another advisory-lock user.
func lockAppScheduleCap(ctx context.Context, tx pgx.Tx, appID string) error {
	if _, execErr := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('scheduled_cap:' || $1::text, 0))`, appID); execErr != nil {
		return pgutil.WrapDBError("lock scheduled cap", execErr)
	}
	return nil
}

// lockGlobalScheduleCap serializes every capped scheduled insert, across apps,
// until the transaction ends, so the total-cap check and the insert are atomic.
// A capped insert always takes this lock before lockAppScheduleCap, never the
// other way round, so two inserts cannot deadlock.
func lockGlobalScheduleCap(ctx context.Context, tx pgx.Tx) error {
	if _, execErr := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('scheduled_cap_total', 0))`); execErr != nil {
		return pgutil.WrapDBError("lock scheduled total cap", execErr)
	}
	return nil
}

// countScheduledTotal returns how many messages, in any app, are scheduled.
func countScheduledTotal(ctx context.Context, db pgutil.Querier) (int, error) {
	var count int
	if scanErr := db.QueryRow(ctx, scheduledTotalSQL).Scan(&count); scanErr != nil {
		return 0, pgutil.WrapDBError("count scheduled messages", scanErr)
	}
	return count, nil
}

// countScheduledForApp returns how many of the app's messages are scheduled.
func countScheduledForApp(ctx context.Context, db pgutil.Querier, appID string) (int, error) {
	var count int
	if scanErr := db.QueryRow(ctx, scheduledCountSQL, appID).Scan(&count); scanErr != nil {
		return 0, pgutil.WrapDBError("count scheduled messages", scanErr)
	}
	return count, nil
}

// ReplaceScheduledMessage swaps the content and deliveries of a still-scheduled
// message. It locks the messages row first, the same lock ReleaseDue and the
// cancels take, so a replace that loses the race to a release or cancel sees
// the new status and returns the stored message untouched with false.
//
// A replace needs no scheduled-cap check: it only ever swaps a message that is
// already scheduled, which then stays scheduled or is released, so it never
// grows the per-app or total count, and it is allowed even when both caps are
// full.
func (store *PGStore) ReplaceScheduledMessage(
	ctx context.Context, envelope Envelope, targets []DeliveryTarget, now time.Time,
) (StoredMessage, bool, error) {
	if len(targets) == 0 {
		return StoredMessage{}, false, errors.New("replace scheduled message: no delivery targets")
	}
	if now.IsZero() {
		return StoredMessage{}, false, errors.New("replace scheduled message: now must be set")
	}
	payload := []byte(envelope.Payload)
	displayJSON, displayErr := encodeDisplay(envelope.Display)
	if displayErr != nil {
		return StoredMessage{}, false, displayErr
	}

	var deliverAt *time.Time
	if envelope.DeliverAt != nil {
		utc := envelope.DeliverAt.UTC()
		deliverAt = &utc
	}
	schedule := deliverySchedule{state: StateScheduled, nextAttemptAt: deliverAt}
	if deliverAt == nil || !deliverAt.After(now) {
		schedule = deliverySchedule{state: StatePending, nextAttemptAt: &now, budgetStartedAt: &now}
		// The message keeps its original created_at, so retention (which counts
		// from greatest(created_at, deliver_at)) would otherwise start at the
		// first submit and a long-scheduled message sent now could be swept
		// right after it is delivered. Anchor the age on the release instead.
		released := now.UTC()
		deliverAt = &released
	}

	var stored StoredMessage
	var replaced bool
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var messageID string
		lockErr := tx.QueryRow(ctx,
			`SELECT id::text FROM messages WHERE app_id = $1 AND idempotency_key = $2 FOR UPDATE`,
			envelope.AppID, envelope.IdempotencyKey).Scan(&messageID)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ErrMessageNotFound
		}
		if lockErr != nil {
			return pgutil.WrapDBError("lock message", lockErr)
		}

		current, loadErr := queryMessage(ctx, tx, `id = $1::text::uuid`, messageID)
		if loadErr != nil {
			return loadErr
		}
		if DeriveStatus(current.Deliveries) != StatusScheduled {
			stored = current
			return nil
		}

		if _, updateErr := tx.Exec(ctx,
			`UPDATE messages SET channel = $2, to_destination = $3, to_recipient = $4, deliver_at = $5,
  critical = $6, payload = $7, display = $8
WHERE id = $1::text::uuid`,
			messageID, envelope.Channel, nullIfEmpty(envelope.ToDestination), nullIfEmpty(envelope.ToRecipient),
			deliverAt, envelope.Critical, payload, displayJSON); updateErr != nil {
			return pgutil.WrapDBError("replace message", updateErr)
		}
		if _, deleteErr := tx.Exec(ctx,
			`DELETE FROM message_deliveries WHERE message_id = $1::text::uuid`, messageID); deleteErr != nil {
			return pgutil.WrapDBError("replace deliveries", deleteErr)
		}
		if insertErr := insertDeliveries(ctx, tx, messageID, targets, schedule); insertErr != nil {
			return insertErr
		}

		replacedMessage, reloadErr := queryMessage(ctx, tx, `id = $1::text::uuid`, messageID)
		if reloadErr != nil {
			return reloadErr
		}
		stored = replacedMessage
		replaced = true
		return nil
	})
	if txErr != nil {
		return StoredMessage{}, false, txErr
	}
	return stored, replaced, nil
}

// CancelMessage cancels the scheduled message with the id, scoped to appID; see
// Store. An empty appID matches no message, so it can never widen into an
// unscoped cancel.
func (store *PGStore) CancelMessage(
	ctx context.Context, id string, appID string, now time.Time,
) (CancelOutcome, error) {
	if !canonicalUUIDPattern.MatchString(id) || appID == "" {
		return CancelOutcome{}, nil
	}
	return store.cancel(ctx, `id = $1::text::uuid AND app_id = $2`, now, id, appID)
}

// AdminCancelMessage cancels the scheduled message with the id in any app; see
// Store.
func (store *PGStore) AdminCancelMessage(
	ctx context.Context, id string, now time.Time,
) (CancelOutcome, error) {
	if !canonicalUUIDPattern.MatchString(id) {
		return CancelOutcome{}, nil
	}
	return store.cancel(ctx, `id = $1::text::uuid`, now, id)
}

// CancelMessageByKey cancels the scheduled message at (appID, idempotencyKey);
// see Store.
func (store *PGStore) CancelMessageByKey(
	ctx context.Context, appID string, idempotencyKey string, now time.Time,
) (CancelOutcome, error) {
	return store.cancel(ctx, `app_id = $1 AND idempotency_key = $2`, now, appID, idempotencyKey)
}

// cancel locks the message matching where (FOR UPDATE, the lock ReleaseDue and
// replace also take), and cancels it if every delivery is still scheduled.
func (store *PGStore) cancel(
	ctx context.Context, where string, now time.Time, args ...any,
) (CancelOutcome, error) {
	if now.IsZero() {
		return CancelOutcome{}, errors.New("cancel message: now must be set")
	}
	var outcome CancelOutcome
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var messageID string
		lockErr := tx.QueryRow(ctx, `SELECT id::text FROM messages WHERE `+where+` FOR UPDATE`, args...).Scan(&messageID)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return nil
		}
		if lockErr != nil {
			return pgutil.WrapDBError("lock message", lockErr)
		}

		rows, queryErr := tx.Query(ctx,
			`SELECT state FROM message_deliveries WHERE message_id = $1::text::uuid`, messageID)
		if queryErr != nil {
			return pgutil.WrapDBError("load delivery states", queryErr)
		}
		deliveries := []Delivery{}
		for rows.Next() {
			var delivery Delivery
			if scanErr := rows.Scan(&delivery.State); scanErr != nil {
				rows.Close()
				return pgutil.WrapDBError("load delivery states", scanErr)
			}
			deliveries = append(deliveries, delivery)
		}
		rows.Close()
		if rowsErr := rows.Err(); rowsErr != nil {
			return pgutil.WrapDBError("load delivery states", rowsErr)
		}

		status := DeriveStatus(deliveries)
		outcome = CancelOutcome{MessageID: messageID, Status: status, Cancelled: status == StatusCancelled, Found: true}
		if status != StatusScheduled {
			return nil
		}

		if _, updateErr := tx.Exec(ctx,
			`UPDATE message_deliveries SET state = 'cancelled', updated_at = $2
WHERE message_id = $1::text::uuid AND state = 'scheduled'`, messageID, now); updateErr != nil {
			return pgutil.WrapDBError("cancel deliveries", updateErr)
		}
		if _, updateErr := tx.Exec(ctx,
			`UPDATE messages SET cancelled_at = $2 WHERE id = $1::text::uuid`, messageID, now); updateErr != nil {
			return pgutil.WrapDBError("cancel message", updateErr)
		}
		outcome.Status = StatusCancelled
		outcome.Cancelled = true
		outcome.Transitioned = true
		return nil
	})
	if txErr != nil {
		return CancelOutcome{}, txErr
	}
	return outcome, nil
}

// ReleaseDue releases up to limit messages with a due scheduled delivery; see
// Store. The probe runs unlocked and the candidates are then locked with SKIP
// LOCKED, so a message a replace or cancel holds is left for the next run. The
// update re-checks state and due time under the lock, so a replace or cancel
// that committed in between cannot be overwritten.
func (store *PGStore) ReleaseDue(ctx context.Context, now time.Time, limit int) (int64, error) {
	if now.IsZero() {
		return 0, errors.New("release due: now must be set")
	}
	if limit < 1 {
		return 0, errors.New("release due: limit must be positive")
	}
	var released int64
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		candidates, probeErr := collectStrings(ctx, tx, "probe due messages", releaseProbeSQL, now, limit)
		if probeErr != nil || len(candidates) == 0 {
			return probeErr
		}
		locked, lockErr := collectStrings(ctx, tx, "lock due messages",
			`SELECT id::text FROM messages WHERE id = ANY($1::text[]::uuid[]) ORDER BY id FOR UPDATE SKIP LOCKED`,
			candidates)
		if lockErr != nil || len(locked) == 0 {
			return lockErr
		}
		releasedIDs, releaseErr := collectStrings(ctx, tx, "release deliveries",
			`UPDATE message_deliveries SET state = 'pending', next_attempt_at = $2, budget_started_at = $2, updated_at = $2
WHERE message_id = ANY($1::text[]::uuid[]) AND state = 'scheduled' AND next_attempt_at <= $2
RETURNING message_id::text`, locked, now)
		if releaseErr != nil {
			return releaseErr
		}
		distinct := make(map[string]struct{}, len(releasedIDs))
		for _, messageID := range releasedIDs {
			distinct[messageID] = struct{}{}
		}
		released = int64(len(distinct))
		return nil
	})
	if txErr != nil {
		return 0, txErr
	}
	return released, nil
}

// ListScheduled returns up to limit scheduled messages and the total scheduled
// count from one snapshot; see Store.
func (store *PGStore) ListScheduled(ctx context.Context, limit int) ([]ScheduledMessage, int, error) {
	if limit < 1 {
		return nil, 0, errors.New("list scheduled messages: limit must be positive")
	}
	listed := []ScheduledMessage{}
	var total int
	txErr := pgutil.WithTx(ctx, store.pool, readSnapshotOptions, func(tx pgx.Tx) error {
		if countErr := tx.QueryRow(ctx, scheduledTotalSQL).Scan(&total); countErr != nil {
			return pgutil.WrapDBError("count scheduled messages", countErr)
		}
		rows, queryErr := tx.Query(ctx, scheduledListSQL, limit)
		if queryErr != nil {
			return pgutil.WrapDBError("list scheduled messages", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var scheduled ScheduledMessage
			var deliverAt *time.Time
			if scanErr := rows.Scan(
				&scheduled.MessageID, &scheduled.AppID, &scheduled.Channel, &scheduled.IdempotencyKey,
				&deliverAt, &scheduled.CreatedAt, &scheduled.Recipient,
			); scanErr != nil {
				return pgutil.WrapDBError("list scheduled messages", scanErr)
			}
			if deliverAt == nil {
				return fmt.Errorf("list scheduled messages: message %s is scheduled without a deliver_at", scheduled.MessageID)
			}
			scheduled.DeliverAt = *deliverAt
			scheduled.Destinations = []ScheduledDestination{}
			listed = append(listed, scheduled)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return pgutil.WrapDBError("list scheduled messages", rowsErr)
		}
		rows.Close()
		return attachScheduledDestinations(ctx, tx, listed)
	})
	if txErr != nil {
		return nil, 0, txErr
	}
	return listed, total, nil
}

// attachScheduledDestinations fills Destinations for the listed messages that
// target destinations; a recipient message keeps an empty list.
func attachScheduledDestinations(ctx context.Context, tx pgx.Tx, listed []ScheduledMessage) error {
	positions := make(map[string]int, len(listed))
	ids := make([]string, 0, len(listed))
	for index, scheduled := range listed {
		if scheduled.Recipient {
			continue
		}
		positions[scheduled.MessageID] = index
		ids = append(ids, scheduled.MessageID)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, queryErr := tx.Query(ctx,
		`SELECT message_id::text, destination_name, destination_type FROM message_deliveries
WHERE message_id = ANY($1::text[]::uuid[]) AND state = 'scheduled'
ORDER BY message_id, destination_name COLLATE "C"`, ids)
	if queryErr != nil {
		return pgutil.WrapDBError("list scheduled destinations", queryErr)
	}
	defer rows.Close()
	for rows.Next() {
		var messageID string
		var destination ScheduledDestination
		if scanErr := rows.Scan(&messageID, &destination.Name, &destination.Type); scanErr != nil {
			return pgutil.WrapDBError("list scheduled destinations", scanErr)
		}
		index := positions[messageID]
		listed[index].Destinations = append(listed[index].Destinations, destination)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return pgutil.WrapDBError("list scheduled destinations", rowsErr)
	}
	return nil
}

// collectStrings runs a query whose first column is text and returns it.
func collectStrings(
	ctx context.Context, db pgutil.Querier, operation string, sql string, args ...any,
) ([]string, error) {
	rows, queryErr := db.Query(ctx, sql, args...)
	if queryErr != nil {
		return nil, pgutil.WrapDBError(operation, queryErr)
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if scanErr := rows.Scan(&value); scanErr != nil {
			return nil, pgutil.WrapDBError(operation, scanErr)
		}
		values = append(values, value)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError(operation, rowsErr)
	}
	return values, nil
}

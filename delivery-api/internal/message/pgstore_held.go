package message

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// Held-message queries. They are constants so the integration tests can
// reference exactly what runs. The per-app count probes messages_held_app_idx
// and the total count probes messages_held_idx (both WHERE held_at IS NOT NULL).
const (
	// heldCountForAppSQL counts an app's held messages.
	heldCountForAppSQL = `SELECT count(*) FROM messages WHERE app_id = $1 AND held_at IS NOT NULL`

	// heldCountTotalSQL counts every held message.
	heldCountTotalSQL = `SELECT count(*) FROM messages WHERE held_at IS NOT NULL`
)

// insertHeldFunc is the signature of one held-insert transaction attempt.
type insertHeldFunc func(
	ctx context.Context, envelope Envelope, caps HeldCaps, now time.Time, payload []byte, displayJSON []byte,
) (StoredMessage, bool, error)

// lockGlobalHeldCap serializes every held insert, across apps, until the
// transaction ends, so the total-cap check and the insert are atomic. A held
// insert always takes this lock before lockAppHeldCap, never the other way
// round (the same order as the scheduled caps), so two inserts cannot deadlock.
func lockGlobalHeldCap(ctx context.Context, tx pgx.Tx) error {
	if _, execErr := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('held_cap_total', 0))`); execErr != nil {
		return pgutil.WrapDBError("lock held total cap", execErr)
	}
	return nil
}

// lockAppHeldCap serializes held inserts for one app until the transaction
// ends. It is always taken after lockGlobalHeldCap (see there).
func lockAppHeldCap(ctx context.Context, tx pgx.Tx, appID string) error {
	if _, execErr := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('held_cap:' || $1::text, 0))`, appID); execErr != nil {
		return pgutil.WrapDBError("lock held cap", execErr)
	}
	return nil
}

// InsertHeldMessage stores envelope as a held message with no delivery rows,
// enforcing the held caps under two advisory locks (global, then app) held to
// the end of the transaction; see Store. If the conflicting row vanishes
// between the conflict and the re-select (a retention sweep), the insert is
// retried up to maxInsertAttempts times, never surfaced as ErrMessageNotFound.
func (store *PGStore) InsertHeldMessage(
	ctx context.Context, envelope Envelope, caps HeldCaps, now time.Time,
) (StoredMessage, bool, error) {
	if caps.PerApp < 1 {
		return StoredMessage{}, false, errors.New("insert held message: max held per app must be positive")
	}
	if caps.Total < 1 {
		return StoredMessage{}, false, errors.New("insert held message: max held total must be positive")
	}
	if now.IsZero() {
		return StoredMessage{}, false, errors.New("insert held message: now must be set")
	}
	payload := []byte(envelope.Payload)
	displayJSON, displayErr := encodeDisplay(envelope.Display)
	if displayErr != nil {
		return StoredMessage{}, false, displayErr
	}
	if envelope.DeliverAt != nil {
		deliverAt := envelope.DeliverAt.UTC()
		envelope.DeliverAt = &deliverAt
	}

	for range maxInsertAttempts {
		stored, duplicate, insertErr := store.insertHeldTx(ctx, envelope, caps, now.UTC(), payload, displayJSON)
		if errors.Is(insertErr, ErrMessageNotFound) {
			continue
		}
		return stored, duplicate, insertErr
	}
	return StoredMessage{}, false, errors.New("insert held message: conflicting message deleted during every attempt")
}

// insertHeldOnce runs one held-insert transaction. It returns ErrMessageNotFound
// only when the insert hit a conflict and the conflicting row was gone by the
// re-select.
func (store *PGStore) insertHeldOnce(
	ctx context.Context, envelope Envelope, caps HeldCaps, now time.Time, payload []byte, displayJSON []byte,
) (StoredMessage, bool, error) {
	var stored StoredMessage
	var duplicate bool
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Fixed order (global, then app) so two submits can never deadlock.
		if lockErr := lockGlobalHeldCap(ctx, tx); lockErr != nil {
			return lockErr
		}
		if lockErr := lockAppHeldCap(ctx, tx, envelope.AppID); lockErr != nil {
			return lockErr
		}
		var messageID string
		insertErr := tx.QueryRow(ctx,
			`INSERT INTO messages (app_id, channel, idempotency_key, to_destination, to_recipient, deliver_at, critical, payload, display, held_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (app_id, idempotency_key) DO NOTHING
RETURNING id::text`,
			envelope.AppID, envelope.Channel, envelope.IdempotencyKey,
			nullIfEmpty(envelope.ToDestination), nullIfEmpty(envelope.ToRecipient),
			envelope.DeliverAt, envelope.Critical, payload, displayJSON, now,
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
			return pgutil.WrapDBError("insert held message", insertErr)
		}

		// The counts include the message just inserted. The per-app cap is
		// checked first: when both are full it is the more specific answer.
		appCount, countErr := countHeld(ctx, tx, heldCountForAppSQL, envelope.AppID)
		if countErr != nil {
			return countErr
		}
		if appCount > caps.PerApp {
			return ErrTooManyHeld
		}
		totalCount, totalErr := countHeld(ctx, tx, heldCountTotalSQL)
		if totalErr != nil {
			return totalErr
		}
		if totalCount > caps.Total {
			return ErrHeldCapacityReached
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

// countHeld runs one of the held count queries.
func countHeld(ctx context.Context, db pgutil.Querier, query string, args ...any) (int, error) {
	var count int
	if scanErr := db.QueryRow(ctx, query, args...).Scan(&count); scanErr != nil {
		return 0, pgutil.WrapDBError("count held messages", scanErr)
	}
	return count, nil
}

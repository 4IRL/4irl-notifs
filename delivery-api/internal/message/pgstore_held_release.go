package message

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
)

// Held-release queries. They are constants so the integration tests can
// reference exactly what runs. Held candidates are route-mode only: a row that
// carries to_destination or to_recipient is left held (fail closed).
const (
	// heldCandidatePredicate selects the held messages the releaser may touch.
	heldCandidatePredicate = `held_at IS NOT NULL AND cancelled_at IS NULL
AND to_destination IS NULL AND to_recipient IS NULL`

	// heldPairsSQL pages through the distinct held (app_id, channel) pairs.
	heldPairsSQL = `SELECT DISTINCT app_id, channel FROM messages
WHERE ` + heldCandidatePredicate + ` AND (app_id, channel) > ($1, $2)
ORDER BY app_id, channel LIMIT $3`

	// releasableHeldSQL lists the held messages of the given pairs, oldest first.
	releasableHeldSQL = `SELECT ` + messageColumns + ` FROM messages
WHERE ` + heldCandidatePredicate + `
AND (app_id, channel) IN (SELECT pair.app_id, pair.channel FROM unnest($1::text[], $2::text[]) AS pair(app_id, channel))
ORDER BY held_at, id LIMIT $3`

	// lockHeldMessageSQL locks one still-held, uncancelled message, skipping it
	// when another transaction (a cancel, the sweeper, another releaser) holds it.
	lockHeldMessageSQL = `SELECT deliver_at FROM messages
WHERE id = $1::text::uuid AND ` + heldCandidatePredicate + ` FOR UPDATE SKIP LOCKED`

	// releaseHeldSQL marks a locked held message released.
	releaseHeldSQL = `UPDATE messages SET held_at = NULL, released_at = $2 WHERE id = $1::text::uuid`

	// failHeldReleaseSQL marks a locked held message as terminally release-failed.
	failHeldReleaseSQL = `UPDATE messages SET held_at = NULL, release_failed_at = $2, release_failed_reason = $3
WHERE id = $1::text::uuid`

	// heldFailuresTotalSQL counts every release-failed message.
	heldFailuresTotalSQL = `SELECT count(*) FROM messages WHERE release_failed_at IS NOT NULL`

	// heldFailuresListSQL lists release-failed messages, newest first.
	heldFailuresListSQL = `SELECT id::text, app_id, channel, COALESCE(release_failed_reason, ''), release_failed_at
FROM messages WHERE release_failed_at IS NOT NULL
ORDER BY release_failed_at DESC, id DESC LIMIT $1`
)

// ListHeldPairs returns the next page of held pairs after the cursor; see Store.
func (store *PGStore) ListHeldPairs(ctx context.Context, after HeldPair, limit int) ([]HeldPair, error) {
	if limit < 1 {
		return nil, errors.New("list held pairs: limit must be positive")
	}
	rows, queryErr := store.pool.Query(ctx, heldPairsSQL, after.AppID, after.Channel, limit)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list held pairs", queryErr)
	}
	defer rows.Close()
	pairs := []HeldPair{}
	for rows.Next() {
		var pair HeldPair
		if scanErr := rows.Scan(&pair.AppID, &pair.Channel); scanErr != nil {
			return nil, pgutil.WrapDBError("list held pairs", scanErr)
		}
		pairs = append(pairs, pair)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("list held pairs", rowsErr)
	}
	return pairs, nil
}

// ListReleasableHeld is the unlocked probe for the held messages of pairs; see
// Store.
func (store *PGStore) ListReleasableHeld(ctx context.Context, pairs []HeldPair, limit int) ([]StoredMessage, error) {
	if limit < 1 {
		return nil, errors.New("list releasable held: limit must be positive")
	}
	if len(pairs) == 0 {
		return []StoredMessage{}, nil
	}
	appIDs := make([]string, 0, len(pairs))
	channels := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		appIDs = append(appIDs, pair.AppID)
		channels = append(channels, pair.Channel)
	}
	rows, queryErr := store.pool.Query(ctx, releasableHeldSQL, appIDs, channels, limit)
	if queryErr != nil {
		return nil, pgutil.WrapDBError("list releasable held", queryErr)
	}
	defer rows.Close()
	messages := []StoredMessage{}
	for rows.Next() {
		stored, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, pgutil.WrapDBError("list releasable held", scanErr)
		}
		messages = append(messages, stored)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, pgutil.WrapDBError("list releasable held", rowsErr)
	}
	return messages, nil
}

// lockHeldMessage takes the row lock of a still-held message. found is false
// when no row came back (locked elsewhere, cancelled, or no longer held).
func lockHeldMessage(ctx context.Context, tx pgx.Tx, messageID string) (deliverAt *time.Time, found bool, err error) {
	scanErr := tx.QueryRow(ctx, lockHeldMessageSQL, messageID).Scan(&deliverAt)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if scanErr != nil {
		return nil, false, pgutil.WrapDBError("lock held message", scanErr)
	}
	return deliverAt, true, nil
}

// ReleaseHeldMessage releases one held message under its row lock; see Store.
// It deliberately takes no scheduled-cap lock: the held caps already bounded
// the volume at submit.
func (store *PGStore) ReleaseHeldMessage(
	ctx context.Context, messageID string, targets []DeliveryTarget, now time.Time,
) (HeldReleaseResult, error) {
	if len(targets) == 0 {
		return "", errors.New("release held message: no delivery targets")
	}
	if now.IsZero() {
		return "", errors.New("release held message: now must be set")
	}
	if !canonicalUUIDPattern.MatchString(messageID) {
		return "", errors.New("release held message: message id is not a canonical uuid")
	}
	now = now.UTC()
	result := HeldSkipped
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		result = HeldSkipped
		deliverAt, found, lockErr := lockHeldMessage(ctx, tx, messageID)
		if lockErr != nil || !found {
			return lockErr
		}
		schedule := deliverySchedule{state: StatePending, nextAttemptAt: &now, budgetStartedAt: &now}
		outcome := HeldReleased
		if deliverAt != nil && deliverAt.After(now) {
			due := deliverAt.UTC()
			schedule = deliverySchedule{state: StateScheduled, nextAttemptAt: &due}
			outcome = HeldScheduled
		}
		if insertErr := insertDeliveries(ctx, tx, messageID, targets, schedule); insertErr != nil {
			return insertErr
		}
		if _, updateErr := tx.Exec(ctx, releaseHeldSQL, messageID, now); updateErr != nil {
			return pgutil.WrapDBError("release held message", updateErr)
		}
		result = outcome
		return nil
	})
	if txErr != nil {
		return "", txErr
	}
	return result, nil
}

// FailHeldRelease terminally fails one held message under its row lock; see
// Store.
func (store *PGStore) FailHeldRelease(
	ctx context.Context, messageID string, reason string, now time.Time,
) (bool, error) {
	if reason != ReleaseFailedReasonInvalid {
		return false, errors.New("fail held release: unsupported reason")
	}
	if now.IsZero() {
		return false, errors.New("fail held release: now must be set")
	}
	if !canonicalUUIDPattern.MatchString(messageID) {
		return false, errors.New("fail held release: message id is not a canonical uuid")
	}
	failed := false
	txErr := pgutil.WithTx(ctx, store.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		failed = false
		_, found, lockErr := lockHeldMessage(ctx, tx, messageID)
		if lockErr != nil || !found {
			return lockErr
		}
		if _, updateErr := tx.Exec(ctx, failHeldReleaseSQL, messageID, now.UTC(), reason); updateErr != nil {
			return pgutil.WrapDBError("fail held release", updateErr)
		}
		failed = true
		return nil
	})
	if txErr != nil {
		return false, txErr
	}
	return failed, nil
}

// ListHeldFailures returns the newest release failures and the total count
// from one snapshot; see Store.
func (store *PGStore) ListHeldFailures(ctx context.Context, limit int) ([]HeldFailure, int, error) {
	if limit < 1 {
		return nil, 0, errors.New("list held failures: limit must be positive")
	}
	failures := []HeldFailure{}
	var total int
	txErr := pgutil.WithTx(ctx, store.pool, readSnapshotOptions, func(tx pgx.Tx) error {
		failures = []HeldFailure{}
		if countErr := tx.QueryRow(ctx, heldFailuresTotalSQL).Scan(&total); countErr != nil {
			return pgutil.WrapDBError("count held failures", countErr)
		}
		rows, queryErr := tx.Query(ctx, heldFailuresListSQL, limit)
		if queryErr != nil {
			return pgutil.WrapDBError("list held failures", queryErr)
		}
		defer rows.Close()
		for rows.Next() {
			var failure HeldFailure
			if scanErr := rows.Scan(
				&failure.MessageID, &failure.AppID, &failure.Channel, &failure.Reason, &failure.FailedAt,
			); scanErr != nil {
				return pgutil.WrapDBError("list held failures", scanErr)
			}
			failure.FailedAt = failure.FailedAt.UTC()
			failures = append(failures, failure)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return pgutil.WrapDBError("list held failures", rowsErr)
		}
		return nil
	})
	if txErr != nil {
		return nil, 0, txErr
	}
	return failures, total, nil
}

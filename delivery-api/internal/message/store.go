package message

import (
	"context"
	"time"
)

// Store persists messages and their delivery fan-out.
type Store interface {
	// InsertMessage persists envelope and one delivery per target in a single
	// transaction. The bool reports a duplicate: on an (app_id, idempotency_key)
	// hit nothing is inserted and the existing message is returned; the submitted
	// content (channel, payload, targets) is ignored in favor of the original.
	InsertMessage(ctx context.Context, envelope Envelope, targets []DeliveryTarget) (StoredMessage, bool, error)
	// GetMessage returns the message with its deliveries, or ErrMessageNotFound
	// (including for an id that is not a canonical UUID).
	GetMessage(ctx context.Context, id string) (StoredMessage, error)
	// GetMessageByKey returns the message for (appID, idempotencyKey), or
	// ErrMessageNotFound.
	GetMessageByKey(ctx context.Context, appID string, idempotencyKey string) (StoredMessage, error)
	// FindMessageByKey is the dedup pre-check: it reports absence as found=false
	// with a nil error rather than ErrMessageNotFound.
	FindMessageByKey(ctx context.Context, appID string, idempotencyKey string) (StoredMessage, bool, error)
	// ClaimDue leases the oldest pending delivery whose next_attempt_at is at or
	// before now, by pushing its next_attempt_at to now+lease in the same
	// transaction (FOR UPDATE SKIP LOCKED), so concurrent workers never claim the
	// same row. The bool is false, with a nil error, when nothing is due. If the
	// worker dies before RecordAttempt, the row is claimable again once the lease
	// expires. The lease must be positive.
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration) (Claim, bool, error)
	// RecordAttempt appends result.Record to the delivery log and applies the
	// transition to the delivery in one transaction, returning the updated row.
	// If the delivery is no longer pending (replayed, or recorded by a duplicate
	// worker) nothing is written and the current row is returned. An unknown
	// delivery is ErrDeliveryNotFound.
	RecordAttempt(ctx context.Context, result AttemptResult) (Delivery, error)
	// ListDeliveries returns up to limit deliveries in state (one of the State*
	// constants), newest-updated first. It never returns nil without an error. An
	// unknown state or a limit below 1 is a *ValidationError. Service validates
	// first; the store repeats the check as defense in depth so a direct call
	// cannot reach SQL with bad arguments.
	ListDeliveries(ctx context.Context, state string, limit int) ([]Delivery, error)
	// ListAttempts returns a delivery's attempts in attempt order, or
	// ErrDeliveryNotFound when the delivery does not exist.
	ListAttempts(ctx context.Context, deliveryID int64) ([]Attempt, error)
	// Replay returns a dead delivery to the queue with a fresh retry budget,
	// due at now. The cumulative attempt count is kept. It returns
	// ErrDeliveryNotFound for an unknown delivery and ErrDeliveryNotDead unless
	// the delivery is dead.
	Replay(ctx context.Context, deliveryID int64, now time.Time) (Delivery, error)
	// DeleteExpired deletes messages created before cutoff that have no pending
	// delivery (their deliveries and attempts cascade) and returns how many.
	DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
}

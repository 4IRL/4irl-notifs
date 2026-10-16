package message

import "context"

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
}

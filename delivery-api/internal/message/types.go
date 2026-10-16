// Package message holds the delivery-core domain: the envelope an app submits,
// its validation, and the queue / delivery-log types built on it.
//
// types.go and validate.go are pure (no I/O, no clock, no database); the store,
// pgstore and service files added alongside them are the I/O layer.
package message

import (
	"encoding/json"
	"errors"
	"time"
)

// Delivery states. The message-level status is derived from these and is never
// stored.
const (
	// StatePending marks a delivery that is queued or awaiting a retry.
	StatePending = "pending"
	// StateDelivered marks a delivery the sink accepted.
	StateDelivered = "delivered"
	// StateDead marks a delivery that will not be attempted again unless an
	// operator replays it.
	StateDead = "dead"
)

// Dead-letter reasons, stored in message_deliveries.dead_reason.
const (
	// DeadReasonTerminalStatus is a non-retryable response from the sink.
	DeadReasonTerminalStatus = "terminal_status"
	// DeadReasonGone is a 410 from the sink; the destination is also disabled.
	DeadReasonGone = "gone"
	// DeadReasonBudgetExhausted is a retryable failure after the retry budget ran out.
	DeadReasonBudgetExhausted = "budget_exhausted"
	// DeadReasonDestinationDisabled is a destination disabled after the delivery was queued.
	DeadReasonDestinationDisabled = "destination_disabled"
	// DeadReasonDestinationDeleted is a destination deleted after the delivery was queued.
	DeadReasonDestinationDeleted = "destination_deleted"
	// DeadReasonBlockedAddress is a destination URL the SSRF guard refused.
	DeadReasonBlockedAddress = "blocked_address"
	// DeadReasonUnsupportedType is a destination type with no sink yet.
	DeadReasonUnsupportedType = "unsupported_type"
)

// MaxPayloadBytes is the largest accepted raw payload.
const MaxPayloadBytes = 128 << 10

// Sentinel errors returned by the message service and store. The HTTP layer
// maps each to a fixed status.
var (
	// ErrMessageNotFound reports that no message matches the id or key.
	ErrMessageNotFound = errors.New("message not found")
	// ErrDeliveryNotFound reports that no delivery has the given id.
	ErrDeliveryNotFound = errors.New("delivery not found")
	// ErrDeliveryNotDead reports a replay of a delivery that is not dead.
	ErrDeliveryNotDead = errors.New("delivery not dead")
	// ErrNoDestinationMatched reports an envelope that resolved to no destination.
	ErrNoDestinationMatched = errors.New("no destination matched")
	// ErrUnsupportedDestinationType reports a resolved destination whose type
	// has no sink yet.
	ErrUnsupportedDestinationType = errors.New("destination type not yet supported")
)

// ValidationError reports an envelope field that failed validation. Message is
// safe to return to the client: it names the field, never its value. It mirrors
// registry.ValidationError so the message package keeps its own errors.
type ValidationError struct {
	// Message names the offending field, for example "invalid app_id".
	Message string
}

// Error returns the validation message.
func (validationError *ValidationError) Error() string { return validationError.Message }

// Display is the optional human-facing rendering of a message.
type Display struct {
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
	URL      string `json:"url,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// Envelope is a message as an app submits it. Payload is opaque to the
// service; it is validated only for shape and size.
type Envelope struct {
	AppID          string
	Channel        string
	IdempotencyKey string
	ToDestination  string
	ToRecipient    string
	DeliverAt      *time.Time
	Critical       bool
	Payload        json.RawMessage
	Display        *Display
}

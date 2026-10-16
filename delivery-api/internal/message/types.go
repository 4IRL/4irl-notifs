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

// Message-level statuses returned by DeriveStatus. They are derived from the
// delivery states, never stored, and kept distinct from the State* names.
const (
	// StatusPending marks a message with at least one pending delivery.
	StatusPending = "pending"
	// StatusDelivered marks a message whose deliveries were all delivered.
	StatusDelivered = "delivered"
	// StatusDead marks a message whose deliveries are all dead.
	StatusDead = "dead"
	// StatusPartial marks a message whose deliveries ended in a mix of delivered
	// and dead.
	StatusPartial = "partial"
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

// Attempt outcomes, stored in delivery_attempts.outcome.
const (
	// OutcomeSuccess is a 2xx response from the sink.
	OutcomeSuccess = "success"
	// OutcomeRetryable is a failure worth retrying (408, 429, 5xx, timeout, connection error).
	OutcomeRetryable = "retryable"
	// OutcomeTerminal is a failure that will not improve on retry (other 3xx and 4xx).
	OutcomeTerminal = "terminal"
	// OutcomeGone is a 410 from the sink.
	OutcomeGone = "gone"
	// OutcomeBlocked is an attempt the SSRF guard or the https-only rule refused.
	OutcomeBlocked = "blocked"
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

// DeliveryTarget is one resolved destination a message fans out to. The name
// and type are snapshotted onto the delivery row so it stays meaningful if the
// destination is later renamed away or deleted.
type DeliveryTarget struct {
	DestinationID   int64
	DestinationName string
	DestinationType string
}

// Delivery is the mutable per-destination state of a stored message.
// DestinationID is nil once the destination has been deleted.
type Delivery struct {
	ID              int64
	MessageID       string
	DestinationID   *int64
	DestinationName string
	DestinationType string
	State           string
	DeadReason      *string
	AttemptCount    int
	BudgetAttempts  int
	BudgetStartedAt time.Time
	NextAttemptAt   time.Time
	LastStatusCode  *int
	DeliveredAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// StoredMessage is a message as persisted, with its deliveries ordered by
// destination name.
type StoredMessage struct {
	ID         string
	Envelope   Envelope
	CreatedAt  time.Time
	Deliveries []Delivery
}

// Claim is a due delivery a worker has leased, with its parent message. The
// message carries no deliveries list.
type Claim struct {
	Delivery Delivery
	Message  StoredMessage
}

// AttemptRecord is one delivery attempt as the worker observed it. Error and
// ResponseSnippet are untrusted remote text: the store sanitizes and truncates
// them (Error to 500 runes, ResponseSnippet to 1024 bytes) before persisting.
type AttemptRecord struct {
	DeliveryID      int64
	Outcome         string
	StatusCode      *int
	LatencyMS       int
	Error           *string
	ResponseSnippet *string
	StartedAt       time.Time
}

// AttemptResult is an attempt plus the delivery transition it causes.
// NextState is one of the State* constants. DeadReason must be set if and only
// if NextState is StateDead. NextAttemptAt is required when NextState is
// StatePending and ignored otherwise.
type AttemptResult struct {
	Record        AttemptRecord
	NextState     string
	DeadReason    *string
	NextAttemptAt time.Time
}

// Attempt is a persisted delivery-log row.
type Attempt struct {
	ID            int64
	AttemptNumber int
	AttemptRecord
}

// DeriveStatus returns the message-level status: StatusPending when any delivery
// is pending (or there are none, which cannot happen for a stored message),
// StatusDelivered when all are delivered, StatusDead when all are dead, and
// StatusPartial for a mix of delivered and dead.
func DeriveStatus(deliveries []Delivery) string {
	if len(deliveries) == 0 {
		return StatusPending
	}
	delivered, dead := 0, 0
	for _, delivery := range deliveries {
		switch delivery.State {
		case StateDelivered:
			delivered++
		case StateDead:
			dead++
		default:
			return StatusPending
		}
	}
	switch {
	case dead == 0:
		return StatusDelivered
	case delivered == 0:
		return StatusDead
	default:
		return StatusPartial
	}
}

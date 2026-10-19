// Package message holds the delivery-core domain: the envelope an app submits,
// its validation, and the queue / delivery-log types built on it.
//
// types.go and validate.go are pure (no I/O, no clock, no database); store.go,
// pgstore.go and service.go are the I/O layer.
package message

import (
	"encoding/json"
	"errors"
	"strings"
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
	// StateScheduled marks a delivery held until its message's deliver_at. A
	// release moves it to StatePending; the worker never claims it.
	StateScheduled = "scheduled"
	// StateCancelled marks a delivery whose scheduled message was cancelled
	// before release. It is final.
	StateCancelled = "cancelled"
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
	// StatusScheduled marks a message whose deliveries are all scheduled.
	StatusScheduled = "scheduled"
	// StatusCancelled marks a message whose deliveries are all cancelled.
	StatusCancelled = "cancelled"
	// StatusNotFound is the status a cancel reports when no message matched. It
	// is never a DeriveStatus result.
	StatusNotFound = "not_found"
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
	// DeadReasonNoPublisher is a recipient delivery whose app has no ntfy
	// publisher token.
	DeadReasonNoPublisher = "no_publisher"
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
	// ErrTooManyScheduled reports a new scheduled submit by an app that already
	// holds its maximum number of scheduled messages.
	ErrTooManyScheduled = errors.New("too many scheduled messages")
	// ErrScheduledCapacityReached reports a new scheduled submit while the whole
	// service already holds its maximum number of scheduled messages, across all
	// apps. It is distinct from ErrTooManyScheduled so an app can tell its own cap
	// from the shared one.
	ErrScheduledCapacityReached = errors.New("scheduled message capacity reached")
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

// HasRenderableText reports whether the display carries something a
// human-facing sink can show: a title or body that is not blank (empty or only
// whitespace). It is nil-safe: a nil display, or one with only a URL or
// priority, has no renderable text. The message service (at submit) and the
// ntfy and Discord sinks (at send) all use this one rule so they agree.
func (display *Display) HasRenderableText() bool {
	return display != nil &&
		(strings.TrimSpace(display.Title) != "" || strings.TrimSpace(display.Body) != "")
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
// destination is later renamed away or deleted. DestinationID is nil for a
// to.recipient target, which has no registry destination.
type DeliveryTarget struct {
	DestinationID   *int64
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

// AdminDelivery is a Delivery plus the parent message's app and channel: the
// operator view returned by the delivery list and replay.
type AdminDelivery struct {
	Delivery
	AppID   string
	Channel string
}

// StoredMessage is a message as persisted, with its deliveries ordered by
// destination name.
type StoredMessage struct {
	ID        string
	Envelope  Envelope
	CreatedAt time.Time
	// CancelledAt is when a scheduled message was cancelled; nil otherwise.
	CancelledAt *time.Time
	Deliveries  []Delivery
}

// ScheduledDestination is one destination a scheduled message will fan out to,
// as the delivery's snapshotted name and type.
type ScheduledDestination struct {
	Name string
	Type string
}

// ScheduledMessage is the operator read model of a message still waiting for
// its deliver_at. Recipient is true for a to.recipient message, whose
// Destinations is then empty: the synthetic recipient delivery is never listed
// and the person hash is never exposed.
type ScheduledMessage struct {
	MessageID      string
	AppID          string
	Channel        string
	IdempotencyKey string
	DeliverAt      time.Time
	CreatedAt      time.Time
	Recipient      bool
	Destinations   []ScheduledDestination
}

// SubmitOutcome reports how Service.Submit treated an envelope. Duplicate and
// Replaced are never both true. Scheduled reports whether the returned message
// is (still) held for a future deliver_at.
type SubmitOutcome struct {
	// Duplicate is true when the key already existed and the stored message is
	// returned unchanged: a released or cancelled message, an identical retry of
	// a scheduled one, or a replace that lost its race.
	Duplicate bool
	// Replaced is true when an existing scheduled message was updated in place.
	Replaced bool
	// Scheduled is true when the returned message is scheduled.
	Scheduled bool
}

// CancelOutcome is the result of a cancel request. Found is false when no
// message matched (unknown id, non-UUID id, another app's message); Status is
// then empty from the store, and StatusNotFound from the Service. Otherwise
// Status is the message's status after the call and Cancelled is true when it
// is (now) cancelled. Transitioned is true only when this very call performed
// the cancel (a repeat on an already cancelled message is Cancelled but not
// Transitioned). MessageID is the matched message's id, empty when nothing
// matched; the Service fills in the requested id for a by-id cancel.
type CancelOutcome struct {
	MessageID    string
	Status       string
	Cancelled    bool
	Transitioned bool
	Found        bool
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
// StatusScheduled when all are scheduled, StatusCancelled when all are
// cancelled, StatusDelivered when all are delivered, StatusDead when all are
// dead, and StatusPartial for a mix of delivered and dead. Scheduled and
// cancelled deliveries only ever change message-atomically, so a mix of either
// with another state cannot happen; if one ever did, it reads as StatusPending
// (not final) rather than as a terminal status.
func DeriveStatus(deliveries []Delivery) string {
	if len(deliveries) == 0 {
		return StatusPending
	}
	delivered, dead, scheduled, cancelled := 0, 0, 0, 0
	for _, delivery := range deliveries {
		switch delivery.State {
		case StateDelivered:
			delivered++
		case StateDead:
			dead++
		case StateScheduled:
			scheduled++
		case StateCancelled:
			cancelled++
		default:
			return StatusPending
		}
	}
	switch {
	case scheduled == len(deliveries):
		return StatusScheduled
	case cancelled == len(deliveries):
		return StatusCancelled
	case scheduled > 0 || cancelled > 0:
		return StatusPending
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

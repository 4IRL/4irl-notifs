// Package sink delivers one stored message to one resolved destination.
//
// Phase 3 ships a single implementation, the generic HMAC webhook sink in
// webhook.go. Phase 4 adds the ntfy and Discord sinks behind the same Sink
// interface. The package imports registry and message for their types only and
// never the dispatch package, so the worker can depend on it without a cycle.
package sink

import (
	"context"
	"errors"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// ErrInvalidDelivery reports a delivery the sink refused to build because the
// destination is misconfigured (missing or malformed URL or signing secret), the
// envelope cannot be sent safely or encoded, or the sink itself is unusable (no
// HTTP client). Nothing was sent, and retrying cannot fix it: it is a permanent
// failure the dispatcher should dead-letter. Sinks wrap it, so test for it with
// errors.Is. Its text never includes the destination URL, a secret or payload
// content.
var ErrInvalidDelivery = errors.New("delivery cannot be built from this destination")

// Request is the message a sink delivers: the stored envelope plus the
// identifiers the store assigned to it.
type Request struct {
	MessageID string
	CreatedAt time.Time
	Envelope  message.Envelope
}

// Outcome is what one delivery attempt produced.
//
// Err is set only when nothing usable came back; StatusCode is 0 in that case.
// How to treat it:
//   - errors.Is(err, ErrInvalidDelivery): permanent, retrying cannot help, so the
//     dispatcher should dead-letter the delivery.
//   - transport.IsBlocked(err): the SSRF guard refused the destination, also
//     terminal.
//   - anything else is a transient transport failure (DNS, connect, timeout,
//     cancellation) and may be retried.
//
// Err never contains the destination URL, its hostname or a secret, so it is
// safe to log and to store.
type Outcome struct {
	StatusCode      int
	RetryAfter      *time.Duration
	ResponseSnippet string
	Err             error
}

// Sink delivers a request to one destination. Implementations must not retain
// the destination's secrets after returning and must never put a URL or secret
// in the Outcome.
type Sink interface {
	Deliver(ctx context.Context, destination registry.ResolvedDestination, request Request) Outcome
}

// Package dispatch is the delivery worker, retry policy and retention sweeper.
//
// policy.go is pure: it turns a sink outcome into the next delivery state with
// no I/O, no sleeping and no clock of its own (the caller passes the time and
// the jitter source), so every branch is reachable from a unit test.
package dispatch

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

// Retry budget and backoff parameters.
const (
	// MaxBudgetAttempts is how many retryable attempts a delivery gets before it is dead-lettered.
	MaxBudgetAttempts = 12
	// MaxBudgetAge is how long a delivery may keep retrying, measured from its budget start.
	MaxBudgetAge = 24 * time.Hour
	// BaseBackoff is the backoff ceiling after the first retryable failure; it doubles per attempt.
	BaseBackoff = 30 * time.Second
	// MaxBackoff is the largest backoff ceiling between two attempts.
	MaxBackoff = time.Hour
	// MaxRetryAfter is the largest Retry-After the dispatcher honors, whatever the sink reports.
	MaxRetryAfter = time.Hour
)

// Classify maps a sink outcome to a message.Outcome* value. It fails closed: an
// outcome that is neither a recognized success, a recognized retryable failure
// nor a transport error (no status and no error, a 1xx, a 3xx, a status outside
// 100-599) is terminal, so it is dead-lettered rather than retried forever or
// mistaken for success.
//
//   - A transport error is blocked when the SSRF guard or the https-only rule
//     refused it, terminal when the sink reports sink.ErrInvalidDelivery
//     (misconfigured destination; retrying cannot help), otherwise retryable.
//   - 2xx is success, 410 (or a sink-asserted Gone) is gone, 408, 429 and
//     500-599 are retryable.
func Classify(outcome sink.Outcome) string {
	if outcome.Err != nil {
		switch {
		case transport.IsBlocked(outcome.Err):
			return message.OutcomeBlocked
		case errors.Is(outcome.Err, sink.ErrInvalidDelivery):
			return message.OutcomeTerminal
		default:
			return message.OutcomeRetryable
		}
	}
	switch code := outcome.StatusCode; {
	case code >= 200 && code <= 299:
		return message.OutcomeSuccess
	case code == 410, outcome.Gone:
		return message.OutcomeGone
	case code == 408, code == 429, code >= 500 && code <= 599:
		return message.OutcomeRetryable
	default:
		return message.OutcomeTerminal
	}
}

// Backoff returns how long to wait after the budgetAttempts-th retryable
// failure (1-based; a value below 1 is treated as 1). The ceiling starts at
// BaseBackoff, doubles per attempt and saturates at MaxBackoff; the result is
// "equal jitter": ceiling/2 plus a random share of the other half, so it always
// lies in [ceiling/2, ceiling]. A retryAfter from the sink raises the result to
// min(*retryAfter, MaxRetryAfter) when that is larger; a non-positive
// retryAfter is ignored. random must return a value in [0, 1); anything else
// (nil, NaN, out of range) is clamped into that range, so the result is always
// within bounds.
func Backoff(budgetAttempts int, retryAfter *time.Duration, random func() float64) time.Duration {
	// Doubling in a loop that stops at the cap cannot overflow time.Duration,
	// unlike BaseBackoff << (budgetAttempts-1) for a large attempt count.
	ceiling := BaseBackoff
	for attempt := 1; attempt < budgetAttempts && ceiling < MaxBackoff; attempt++ {
		ceiling *= 2
	}
	ceiling = min(ceiling, MaxBackoff)

	half := ceiling / 2
	backoff := half + time.Duration(jitterFraction(random)*float64(half))

	if retryAfter != nil && *retryAfter > 0 {
		backoff = max(backoff, min(*retryAfter, MaxRetryAfter))
	}
	return backoff
}

// jitterFraction returns random() clamped into [0, 1]; a nil source, NaN or a
// negative value yields 0.
func jitterFraction(random func() float64) float64 {
	if random == nil {
		return 0
	}
	fraction := random()
	if math.IsNaN(fraction) || fraction < 0 {
		return 0
	}
	return min(fraction, 1)
}

// Decide produces the attempt record and the next delivery state for an
// outcome observed at now:
//
//   - success: delivered;
//   - gone: dead with reason gone (the worker also disables the destination);
//   - terminal (other 3xx/4xx, an unclassifiable outcome, a misconfigured
//     destination): dead with reason terminal_status;
//   - blocked: dead with reason blocked_address;
//   - retryable: dead with reason budget_exhausted when this attempt would be
//     the MaxBudgetAttempts-th or the budget is MaxBudgetAge old (a zero
//     BudgetStartedAt counts as ancient, failing closed); otherwise pending
//     with NextAttemptAt = now + Backoff.
//
// The record's StartedAt is now and LatencyMS is 0; the worker overwrites both
// with the measured values. The record's Error is the transport error text, or
// "HTTP <code>" for a non-2xx response.
func Decide(delivery message.Delivery, outcome sink.Outcome, now time.Time, random func() float64) message.AttemptResult {
	class := Classify(outcome)
	result := message.AttemptResult{Record: buildRecord(delivery, outcome, class, now)}

	switch class {
	case message.OutcomeSuccess:
		result.NextState = message.StateDelivered
	case message.OutcomeGone:
		return deadResult(result, message.DeadReasonGone)
	case message.OutcomeBlocked:
		return deadResult(result, message.DeadReasonBlockedAddress)
	case message.OutcomeRetryable:
		// Compare before adding one: BudgetAttempts+1 would wrap at math.MaxInt and fail open. A start
		// time in the future gives a negative age, so the attempt cap is then the only bound.
		if delivery.BudgetAttempts >= MaxBudgetAttempts-1 || now.Sub(delivery.BudgetStartedAt) >= MaxBudgetAge {
			return deadResult(result, message.DeadReasonBudgetExhausted)
		}
		result.NextState = message.StatePending
		result.NextAttemptAt = now.Add(Backoff(delivery.BudgetAttempts+1, outcome.RetryAfter, random))
	default:
		// OutcomeTerminal and any class Classify may gain later: dead, never retried.
		return deadResult(result, message.DeadReasonTerminalStatus)
	}
	return result
}

// deadResult marks result dead with the given reason.
func deadResult(result message.AttemptResult, reason string) message.AttemptResult {
	result.NextState = message.StateDead
	result.DeadReason = &reason
	return result
}

// buildRecord assembles the delivery-log row for an outcome.
func buildRecord(delivery message.Delivery, outcome sink.Outcome, class string, now time.Time) message.AttemptRecord {
	record := message.AttemptRecord{
		DeliveryID: delivery.ID,
		Outcome:    class,
		StartedAt:  now,
	}
	if outcome.StatusCode > 0 {
		statusCode := outcome.StatusCode
		record.StatusCode = &statusCode
	}
	switch {
	case outcome.Err != nil:
		errorText := outcome.Err.Error()
		record.Error = &errorText
	case class != message.OutcomeSuccess:
		errorText := fmt.Sprintf("HTTP %d", outcome.StatusCode)
		record.Error = &errorText
	}
	if outcome.ResponseSnippet != "" {
		snippet := outcome.ResponseSnippet
		record.ResponseSnippet = &snippet
	}
	return record
}

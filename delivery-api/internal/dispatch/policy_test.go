package dispatch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/url"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

func durationPointer(duration time.Duration) *time.Duration { return &duration }

func TestClassify(t *testing.T) {
	blockedWrapped := &url.Error{Op: "Post", URL: "https://example.invalid/", Err: transport.ErrBlockedAddress}
	notHTTPS := fmt.Errorf("send: %w", transport.ErrNotHTTPS)
	invalid := fmt.Errorf("build request: %w", sink.ErrInvalidDelivery)
	tests := []struct {
		name    string
		outcome sink.Outcome
		want    string
	}{
		{"200", sink.Outcome{StatusCode: 200}, message.OutcomeSuccess},
		{"204", sink.Outcome{StatusCode: 204}, message.OutcomeSuccess},
		{"299", sink.Outcome{StatusCode: 299}, message.OutcomeSuccess},
		{"301 unfollowed redirect", sink.Outcome{StatusCode: 301}, message.OutcomeTerminal},
		{"307 unfollowed redirect", sink.Outcome{StatusCode: 307}, message.OutcomeTerminal},
		{"400", sink.Outcome{StatusCode: 400}, message.OutcomeTerminal},
		{"401", sink.Outcome{StatusCode: 401}, message.OutcomeTerminal},
		{"404", sink.Outcome{StatusCode: 404}, message.OutcomeTerminal},
		{"408", sink.Outcome{StatusCode: 408}, message.OutcomeRetryable},
		{"410", sink.Outcome{StatusCode: 410}, message.OutcomeGone},
		{"429", sink.Outcome{StatusCode: 429}, message.OutcomeRetryable},
		{"500", sink.Outcome{StatusCode: 500}, message.OutcomeRetryable},
		{"503", sink.Outcome{StatusCode: 503}, message.OutcomeRetryable},
		{"599", sink.Outcome{StatusCode: 599}, message.OutcomeRetryable},
		{"blocked address", sink.Outcome{Err: transport.ErrBlockedAddress}, message.OutcomeBlocked},
		{"blocked address inside url.Error", sink.Outcome{Err: blockedWrapped}, message.OutcomeBlocked},
		{"non-https", sink.Outcome{Err: notHTTPS}, message.OutcomeBlocked},
		{"invalid delivery is permanent", sink.Outcome{Err: invalid}, message.OutcomeTerminal},
		{"blocked and invalid together is blocked", sink.Outcome{Err: errors.Join(invalid, notHTTPS)}, message.OutcomeBlocked},
		{"timeout", sink.Outcome{Err: context.DeadlineExceeded}, message.OutcomeRetryable},
		{"connection error", sink.Outcome{Err: errors.New("connection refused")}, message.OutcomeRetryable},
		{"error wins over a 2xx status", sink.Outcome{StatusCode: 200, Err: errors.New("read body")}, message.OutcomeRetryable},
		{"error wins over 410", sink.Outcome{StatusCode: 410, Err: errors.New("boom")}, message.OutcomeRetryable},
		{"no status and no error is unclassified so terminal", sink.Outcome{}, message.OutcomeTerminal},
		{"1xx is unclassified so terminal", sink.Outcome{StatusCode: 100}, message.OutcomeTerminal},
		{"negative status is unclassified so terminal", sink.Outcome{StatusCode: -1}, message.OutcomeTerminal},
		{"600 is unclassified so terminal", sink.Outcome{StatusCode: 600}, message.OutcomeTerminal},
		{"99999 is unclassified so terminal", sink.Outcome{StatusCode: 99999}, message.OutcomeTerminal},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := Classify(testCase.outcome); got != testCase.want {
				t.Fatalf("Classify() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// wantCeiling is the documented ceiling for a 1-based budget attempt: 30s doubling, capped at 1h.
func wantCeiling(attempt int) time.Duration {
	ceiling := 30 * time.Second
	for step := 1; step < attempt; step++ {
		ceiling *= 2
		if ceiling >= time.Hour {
			return time.Hour
		}
	}
	return ceiling
}

func TestBackoffBoundsAcrossAttempts(t *testing.T) {
	for attempt := 1; attempt <= 200; attempt++ {
		ceiling := wantCeiling(attempt)
		for sample := 0; sample < 1000; sample++ {
			got := Backoff(attempt, nil, rand.Float64)
			if got < ceiling/2 || got > ceiling {
				t.Fatalf("attempt %d: Backoff() = %v, want within [%v, %v]", attempt, got, ceiling/2, ceiling)
			}
		}
	}
}

func TestBackoffExactValues(t *testing.T) {
	zero := func() float64 { return 0 }
	nearOne := func() float64 { return math.Nextafter(1, 0) }
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 15 * time.Second},
		{2, 30 * time.Second},
		{3, time.Minute},
		{7, 16 * time.Minute},
		{8, 30 * time.Minute},
		{14, 30 * time.Minute},
	}
	for _, testCase := range tests {
		if got := Backoff(testCase.attempt, nil, zero); got != testCase.want {
			t.Errorf("attempt %d with random 0: got %v, want %v", testCase.attempt, got, testCase.want)
		}
	}
	if got := Backoff(1, nil, nearOne); got < 29990*time.Millisecond || got > 30*time.Second {
		t.Errorf("attempt 1 with random just below 1: got %v, want just under 30s", got)
	}
}

func TestBackoffCapHoldsAtAndPastAttempt14(t *testing.T) {
	for _, attempt := range []int{14, 20, 62, 63, 64, 65, 100, 1 << 20, math.MaxInt} {
		for sample := 0; sample < 1000; sample++ {
			got := Backoff(attempt, nil, rand.Float64)
			if got < MaxBackoff/2 || got > MaxBackoff {
				t.Fatalf("attempt %d: Backoff() = %v, want within [%v, %v] (shift overflow?)", attempt, got, MaxBackoff/2, MaxBackoff)
			}
		}
	}
}

func TestBackoffNonPositiveAttemptTreatedAsFirst(t *testing.T) {
	zero := func() float64 { return 0 }
	for _, attempt := range []int{0, -1, math.MinInt} {
		if got := Backoff(attempt, nil, zero); got != BaseBackoff/2 {
			t.Errorf("attempt %d: got %v, want %v", attempt, got, BaseBackoff/2)
		}
	}
}

func TestBackoffToleratesBadRandomSources(t *testing.T) {
	tests := []struct {
		name   string
		random func() float64
	}{
		{"nil", nil},
		{"negative", func() float64 { return -5 }},
		{"above one", func() float64 { return 7 }},
		{"NaN", math.NaN},
		{"positive infinity", func() float64 { return math.Inf(1) }},
		{"negative infinity", func() float64 { return math.Inf(-1) }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			for _, attempt := range []int{1, 5, 14} {
				ceiling := wantCeiling(attempt)
				got := Backoff(attempt, nil, testCase.random)
				if got < ceiling/2 || got > ceiling {
					t.Fatalf("attempt %d: Backoff() = %v, want within [%v, %v]", attempt, got, ceiling/2, ceiling)
				}
			}
		})
	}
}

func TestBackoffRetryAfter(t *testing.T) {
	zero := func() float64 { return 0 }
	tests := []struct {
		name       string
		attempt    int
		retryAfter *time.Duration
		want       time.Duration
	}{
		{"2h is clamped to 1h", 1, durationPointer(2 * time.Hour), time.Hour},
		{"absurdly large is clamped to 1h", 1, durationPointer(time.Duration(math.MaxInt64)), time.Hour},
		{"5 min beats the attempt-1 ceiling of 30s", 1, durationPointer(5 * time.Minute), 5 * time.Minute},
		{"smaller than the jittered backoff is ignored", 14, durationPointer(time.Second), 30 * time.Minute},
		{"negative is ignored", 1, durationPointer(-time.Hour), 15 * time.Second},
		{"zero is ignored", 1, durationPointer(0), 15 * time.Second},
		{"nil is ignored", 1, nil, 15 * time.Second},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := Backoff(testCase.attempt, testCase.retryAfter, zero)
			if got != testCase.want {
				t.Fatalf("Backoff() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestBackoffNeverExceedsOneHourWithAnyInputs(t *testing.T) {
	for attempt := -2; attempt <= 100; attempt++ {
		for _, retryAfter := range []*time.Duration{nil, durationPointer(0), durationPointer(time.Minute), durationPointer(1000 * time.Hour)} {
			got := Backoff(attempt, retryAfter, rand.Float64)
			if got <= 0 || got > time.Hour {
				t.Fatalf("attempt %d retryAfter %v: Backoff() = %v, want in (0, 1h]", attempt, retryAfter, got)
			}
		}
	}
}

func stringPointer(value string) *string { return &value }

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	zero := func() float64 { return 0 }
	freshDelivery := func(budgetAttempts int, age time.Duration) message.Delivery {
		return message.Delivery{ID: 42, BudgetAttempts: budgetAttempts, BudgetStartedAt: now.Add(-age)}
	}
	timeoutErr := context.DeadlineExceeded
	tests := []struct {
		name          string
		delivery      message.Delivery
		outcome       sink.Outcome
		wantState     string
		wantReason    *string
		wantNext      time.Time
		wantRecord    string
		wantErrorText *string
		wantStatus    *int
	}{
		{
			name:       "200 delivered",
			delivery:   freshDelivery(0, 0),
			outcome:    sink.Outcome{StatusCode: 200},
			wantState:  message.StateDelivered,
			wantRecord: message.OutcomeSuccess,
			wantStatus: intPointer(200),
		},
		{
			name:          "410 is dead gone",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{StatusCode: 410},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonGone),
			wantRecord:    message.OutcomeGone,
			wantErrorText: stringPointer("HTTP 410"),
			wantStatus:    intPointer(410),
		},
		{
			name:          "404 is dead terminal_status",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{StatusCode: 404},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonTerminalStatus),
			wantRecord:    message.OutcomeTerminal,
			wantErrorText: stringPointer("HTTP 404"),
			wantStatus:    intPointer(404),
		},
		{
			name:          "301 is dead terminal_status",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{StatusCode: 301},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonTerminalStatus),
			wantRecord:    message.OutcomeTerminal,
			wantErrorText: stringPointer("HTTP 301"),
			wantStatus:    intPointer(301),
		},
		{
			name:          "blocked address is dead blocked_address",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{Err: transport.ErrBlockedAddress},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonBlockedAddress),
			wantRecord:    message.OutcomeBlocked,
			wantErrorText: stringPointer(transport.ErrBlockedAddress.Error()),
		},
		{
			name:          "invalid delivery is dead terminal_status",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{Err: sink.ErrInvalidDelivery},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonTerminalStatus),
			wantRecord:    message.OutcomeTerminal,
			wantErrorText: stringPointer(sink.ErrInvalidDelivery.Error()),
		},
		{
			name:          "no status and no error is dead terminal_status",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonTerminalStatus),
			wantRecord:    message.OutcomeTerminal,
			wantErrorText: stringPointer("HTTP 0"),
		},
		{
			name:          "first 503 retries after the jittered backoff",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{StatusCode: 503},
			wantState:     message.StatePending,
			wantNext:      now.Add(15 * time.Second),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 503"),
			wantStatus:    intPointer(503),
		},
		{
			name:          "second attempt doubles the ceiling",
			delivery:      freshDelivery(1, time.Minute),
			outcome:       sink.Outcome{Err: timeoutErr},
			wantState:     message.StatePending,
			wantNext:      now.Add(30 * time.Second),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer(timeoutErr.Error()),
		},
		{
			name:          "429 Retry-After beats the backoff",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{StatusCode: 429, RetryAfter: durationPointer(10 * time.Minute)},
			wantState:     message.StatePending,
			wantNext:      now.Add(10 * time.Minute),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 429"),
			wantStatus:    intPointer(429),
		},
		{
			name:          "Retry-After is clamped to 1h",
			delivery:      freshDelivery(0, 0),
			outcome:       sink.Outcome{StatusCode: 503, RetryAfter: durationPointer(24 * time.Hour)},
			wantState:     message.StatePending,
			wantNext:      now.Add(time.Hour),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 503"),
			wantStatus:    intPointer(503),
		},
		{
			name:          "11th retryable attempt still retries",
			delivery:      freshDelivery(10, time.Hour),
			outcome:       sink.Outcome{StatusCode: 500},
			wantState:     message.StatePending,
			wantNext:      now.Add(30 * time.Minute),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 500"),
			wantStatus:    intPointer(500),
		},
		{
			name:          "12th retryable attempt exhausts the budget",
			delivery:      freshDelivery(11, time.Hour),
			outcome:       sink.Outcome{StatusCode: 500},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonBudgetExhausted),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 500"),
			wantStatus:    intPointer(500),
		},
		{
			name:          "budget attempts beyond the limit exhaust it",
			delivery:      freshDelivery(500, time.Hour),
			outcome:       sink.Outcome{Err: timeoutErr},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonBudgetExhausted),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer(timeoutErr.Error()),
		},
		{
			name:          "a 24h-old budget exhausts on its first retryable failure",
			delivery:      freshDelivery(0, 24*time.Hour),
			outcome:       sink.Outcome{StatusCode: 503},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonBudgetExhausted),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 503"),
			wantStatus:    intPointer(503),
		},
		{
			name:          "a budget just under 24h still retries",
			delivery:      freshDelivery(0, 24*time.Hour-time.Second),
			outcome:       sink.Outcome{StatusCode: 503},
			wantState:     message.StatePending,
			wantNext:      now.Add(15 * time.Second),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 503"),
			wantStatus:    intPointer(503),
		},
		{
			name:          "a zero budget start time is treated as exhausted",
			delivery:      message.Delivery{ID: 42},
			outcome:       sink.Outcome{StatusCode: 503},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonBudgetExhausted),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 503"),
			wantStatus:    intPointer(503),
		},
		{
			name:          "a MaxInt attempt counter exhausts the budget instead of wrapping",
			delivery:      freshDelivery(math.MaxInt, time.Hour),
			outcome:       sink.Outcome{StatusCode: 503},
			wantState:     message.StateDead,
			wantReason:    stringPointer(message.DeadReasonBudgetExhausted),
			wantRecord:    message.OutcomeRetryable,
			wantErrorText: stringPointer("HTTP 503"),
			wantStatus:    intPointer(503),
		},
		{
			name:       "success is delivered even with an exhausted budget",
			delivery:   freshDelivery(11, 48*time.Hour),
			outcome:    sink.Outcome{StatusCode: 204},
			wantState:  message.StateDelivered,
			wantRecord: message.OutcomeSuccess,
			wantStatus: intPointer(204),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := Decide(testCase.delivery, testCase.outcome, now, zero)
			if result.NextState != testCase.wantState {
				t.Fatalf("NextState = %q, want %q", result.NextState, testCase.wantState)
			}
			if !equalStringPointers(result.DeadReason, testCase.wantReason) {
				t.Fatalf("DeadReason = %v, want %v", derefString(result.DeadReason), derefString(testCase.wantReason))
			}
			if testCase.wantState == message.StatePending && !result.NextAttemptAt.Equal(testCase.wantNext) {
				t.Fatalf("NextAttemptAt = %v, want %v", result.NextAttemptAt, testCase.wantNext)
			}
			if testCase.wantState != message.StatePending && !result.NextAttemptAt.IsZero() {
				t.Fatalf("NextAttemptAt = %v, want zero for state %q", result.NextAttemptAt, testCase.wantState)
			}
			record := result.Record
			if record.Outcome != testCase.wantRecord {
				t.Fatalf("Record.Outcome = %q, want %q", record.Outcome, testCase.wantRecord)
			}
			if record.DeliveryID != testCase.delivery.ID {
				t.Fatalf("Record.DeliveryID = %d, want %d", record.DeliveryID, testCase.delivery.ID)
			}
			if !record.StartedAt.Equal(now) {
				t.Fatalf("Record.StartedAt = %v, want %v", record.StartedAt, now)
			}
			if !equalStringPointers(record.Error, testCase.wantErrorText) {
				t.Fatalf("Record.Error = %v, want %v", derefString(record.Error), derefString(testCase.wantErrorText))
			}
			if (record.StatusCode == nil) != (testCase.wantStatus == nil) || (record.StatusCode != nil && *record.StatusCode != *testCase.wantStatus) {
				t.Fatalf("Record.StatusCode = %v, want %v", record.StatusCode, testCase.wantStatus)
			}
		})
	}
}

func TestDecideCarriesResponseSnippet(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	delivery := message.Delivery{ID: 1, BudgetStartedAt: now}
	result := Decide(delivery, sink.Outcome{StatusCode: 502, ResponseSnippet: "bad gateway"}, now, func() float64 { return 0 })
	if result.Record.ResponseSnippet == nil || *result.Record.ResponseSnippet != "bad gateway" {
		t.Fatalf("ResponseSnippet = %v, want %q", derefString(result.Record.ResponseSnippet), "bad gateway")
	}
	result = Decide(delivery, sink.Outcome{StatusCode: 200}, now, func() float64 { return 0 })
	if result.Record.ResponseSnippet != nil {
		t.Fatalf("ResponseSnippet = %v, want nil when the sink returned none", derefString(result.Record.ResponseSnippet))
	}
}

// TestDecideAlwaysYieldsAValidTransition sweeps outcomes and budgets and asserts the AttemptResult invariants
// the store enforces: dead if and only if a reason is set, and a pending retry is strictly in the future.
func TestDecideAlwaysYieldsAValidTransition(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	outcomes := []sink.Outcome{
		{}, {StatusCode: -3}, {StatusCode: 100}, {StatusCode: 200}, {StatusCode: 301}, {StatusCode: 404}, {StatusCode: 408},
		{StatusCode: 410}, {StatusCode: 429}, {StatusCode: 500}, {StatusCode: 599}, {StatusCode: 700},
		{Err: errors.New("boom")}, {Err: transport.ErrBlockedAddress}, {Err: sink.ErrInvalidDelivery},
		{StatusCode: 503, RetryAfter: durationPointer(-time.Hour)}, {StatusCode: 503, RetryAfter: durationPointer(1000 * time.Hour)},
	}
	for _, outcome := range outcomes {
		for budgetAttempts := -1; budgetAttempts <= 14; budgetAttempts++ {
			for _, age := range []time.Duration{-time.Hour, 0, time.Hour, 24 * time.Hour, 1000 * time.Hour} {
				delivery := message.Delivery{ID: 7, BudgetAttempts: budgetAttempts, BudgetStartedAt: now.Add(-age)}
				result := Decide(delivery, outcome, now, rand.Float64)
				switch result.NextState {
				case message.StateDelivered:
					if result.DeadReason != nil || result.Record.Outcome != message.OutcomeSuccess {
						t.Fatalf("%+v: delivered with reason %v / outcome %q", outcome, derefString(result.DeadReason), result.Record.Outcome)
					}
				case message.StateDead:
					if result.DeadReason == nil || *result.DeadReason == "" {
						t.Fatalf("%+v: dead without a reason", outcome)
					}
				case message.StatePending:
					if result.DeadReason != nil {
						t.Fatalf("%+v: pending with a dead reason", outcome)
					}
					if !result.NextAttemptAt.After(now) || result.NextAttemptAt.Sub(now) > MaxBackoff {
						t.Fatalf("%+v: NextAttemptAt %v not in (now, now+1h]", outcome, result.NextAttemptAt)
					}
					if result.Record.Outcome != message.OutcomeRetryable {
						t.Fatalf("%+v: pending after non-retryable outcome %q", outcome, result.Record.Outcome)
					}
				default:
					t.Fatalf("%+v: unknown NextState %q", outcome, result.NextState)
				}
			}
		}
	}
}

// TestRetryableOutcomesAlwaysTerminate replays Decide the way the store does (budget_attempts +1 per recorded
// attempt, the clock advancing to NextAttemptAt) and asserts a delivery that only ever fails retryably is
// dead-lettered with budget_exhausted by the 12th attempt, whatever the outcome shape or jitter source.
func TestRetryableOutcomesAlwaysTerminate(t *testing.T) {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	outcomes := []sink.Outcome{
		{StatusCode: 408}, {StatusCode: 429}, {StatusCode: 503}, {Err: errors.New("connection reset")},
		{StatusCode: 429, RetryAfter: durationPointer(time.Minute)},
		{StatusCode: 503, RetryAfter: durationPointer(1000 * time.Hour)},
	}
	randoms := map[string]func() float64{"zero": func() float64 { return 0 }, "rand": rand.Float64}
	for _, outcome := range outcomes {
		for randomName, random := range randoms {
			delivery := message.Delivery{ID: 9, BudgetStartedAt: start}
			now := start
			attempts := 0
			for {
				attempts++
				if attempts > MaxBudgetAttempts {
					t.Fatalf("%+v (%s): still pending after %d attempts", outcome, randomName, attempts-1)
				}
				result := Decide(delivery, outcome, now, random)
				if result.NextState == message.StateDead {
					if result.DeadReason == nil || *result.DeadReason != message.DeadReasonBudgetExhausted {
						t.Fatalf("%+v (%s): dead for %v, want budget_exhausted", outcome, randomName, derefString(result.DeadReason))
					}
					break
				}
				if result.NextState != message.StatePending {
					t.Fatalf("%+v (%s): unexpected state %q", outcome, randomName, result.NextState)
				}
				delivery.BudgetAttempts++
				now = result.NextAttemptAt
			}
		}
	}
}

func intPointer(value int) *int { return &value }

func derefString(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func equalStringPointers(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

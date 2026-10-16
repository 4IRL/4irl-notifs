package message

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateRunes(testInstance *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{name: "shorter than limit is untouched", text: "héllo", limit: 10, want: "héllo"},
		{name: "exactly the limit is untouched", text: "héllo", limit: 5, want: "héllo"},
		{name: "cuts on a rune count, not a byte count", text: "héllo", limit: 2, want: "hé"},
		{name: "multi-byte runes count once", text: "日本語テキスト", limit: 3, want: "日本語"},
		{name: "zero limit empties the text", text: "abc", limit: 0, want: ""},
		{name: "empty text stays empty", text: "", limit: 5, want: ""},
	}
	for _, test := range tests {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			if got := truncateRunes(test.text, test.limit); got != test.want {
				testInstance.Errorf("truncateRunes(%q, %d) = %q, want %q", test.text, test.limit, got, test.want)
			}
		})
	}
}

func TestTruncateBytesCutsOnARuneBoundary(testInstance *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{name: "shorter than limit is untouched", text: "héllo", limit: 10, want: "héllo"},
		{name: "exactly the limit is untouched", text: "héllo", limit: 6, want: "héllo"},
		{name: "ascii cut", text: "abcdef", limit: 3, want: "abc"},
		{name: "limit lands mid-rune so the rune is dropped whole", text: "a€b", limit: 3, want: "a"},
		{name: "limit lands on a boundary after a multi-byte rune", text: "a€b", limit: 4, want: "a€"},
		{name: "limit smaller than the first rune empties the text", text: "€", limit: 2, want: ""},
		{name: "zero limit empties the text", text: "abc", limit: 0, want: ""},
	}
	for _, test := range tests {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			got := truncateBytes(test.text, test.limit)
			if got != test.want {
				testInstance.Errorf("truncateBytes(%q, %d) = %q, want %q", test.text, test.limit, got, test.want)
			}
			if !utf8.ValidString(got) {
				testInstance.Errorf("truncateBytes(%q, %d) = %q is not valid UTF-8", test.text, test.limit, got)
			}
		})
	}
}

func TestTruncateBytesNeverSplitsARuneAtAnyLimit(testInstance *testing.T) {
	text := strings.Repeat("a€😀é", 40)
	for limit := 0; limit <= len(text)+2; limit++ {
		got := truncateBytes(text, limit)
		if len(got) > limit {
			testInstance.Fatalf("limit %d: result has %d bytes", limit, len(got))
		}
		if !utf8.ValidString(got) {
			testInstance.Fatalf("limit %d: result %q is not valid UTF-8", limit, got)
		}
		if !strings.HasPrefix(text, got) {
			testInstance.Fatalf("limit %d: result %q is not a prefix of the input", limit, got)
		}
	}
}

func TestCleanOptionalText(testInstance *testing.T) {
	if got := cleanOptionalText(nil, 10, truncateRunes); got != nil {
		testInstance.Errorf("nil input = %q, want nil", *got)
	}

	tests := []struct {
		name     string
		input    string
		limit    int
		truncate func(string, int) string
		want     string
	}{
		{name: "NUL bytes are dropped", input: "a\x00b", limit: 10, truncate: truncateRunes, want: "ab"},
		{name: "invalid UTF-8 becomes U+FFFD", input: "a\xffb", limit: 10, truncate: truncateRunes, want: "a�b"},
		{
			name: "truncation runs after sanitizing, so replacement runes cannot overflow the byte cap",
			// Each lone invalid byte becomes a 3-byte U+FFFD, so 600 pairs grow from
			// 1200 to 2400 bytes; the cap then keeps 256 whole pairs (1024 bytes).
			input: strings.Repeat("a\xff", 600), limit: 1024, truncate: truncateBytes,
			want: strings.Repeat("a�", 256),
		},
		{
			name:  "a run of invalid bytes collapses to one U+FFFD",
			input: "a\xff\xfe\xfdb", limit: 10, truncate: truncateRunes, want: "a�b",
		},
		{name: "clean text passes through", input: "all good", limit: 100, truncate: truncateBytes, want: "all good"},
	}
	for _, test := range tests {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			input := test.input
			got := cleanOptionalText(&input, test.limit, test.truncate)
			if got == nil || *got != test.want {
				testInstance.Errorf("cleanOptionalText(%q) = %v, want %q", test.input, got, test.want)
			}
		})
	}
}

func attemptResultOf(outcome string, nextState string, deadReason *string, nextAttemptAt time.Time) AttemptResult {
	return AttemptResult{
		Record:        AttemptRecord{Outcome: outcome},
		NextState:     nextState,
		DeadReason:    deadReason,
		NextAttemptAt: nextAttemptAt,
	}
}

func TestValidateAttemptResult(testInstance *testing.T) {
	reason := DeadReasonTerminalStatus
	emptyReason := ""
	later := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		result  AttemptResult
		wantErr bool
	}{
		{name: "delivered", result: attemptResultOf(OutcomeSuccess, StateDelivered, nil, time.Time{})},
		{name: "pending retry", result: attemptResultOf(OutcomeRetryable, StatePending, nil, later)},
		{name: "dead terminal", result: attemptResultOf(OutcomeTerminal, StateDead, &reason, time.Time{})},
		{name: "dead gone", result: attemptResultOf(OutcomeGone, StateDead, &reason, time.Time{})},
		{name: "pending without a next attempt", result: attemptResultOf(OutcomeRetryable, StatePending, nil, time.Time{}), wantErr: true},
		{name: "pending with a dead reason", result: attemptResultOf(OutcomeRetryable, StatePending, &reason, later), wantErr: true},
		{name: "delivered with a dead reason", result: attemptResultOf(OutcomeSuccess, StateDelivered, &reason, time.Time{}), wantErr: true},
		{name: "dead without a reason", result: attemptResultOf(OutcomeTerminal, StateDead, nil, time.Time{}), wantErr: true},
		{name: "dead with an empty reason", result: attemptResultOf(OutcomeTerminal, StateDead, &emptyReason, time.Time{}), wantErr: true},
		{name: "unknown state fails closed", result: attemptResultOf(OutcomeTerminal, "archived", nil, time.Time{}), wantErr: true},
		{name: "empty result fails closed", result: AttemptResult{}, wantErr: true},
		{name: "success outcome with a dead state", result: attemptResultOf(OutcomeSuccess, StateDead, &reason, time.Time{}), wantErr: true},
		{name: "success outcome with a pending state", result: attemptResultOf(OutcomeSuccess, StatePending, nil, later), wantErr: true},
		{name: "failure outcome with a delivered state", result: attemptResultOf(OutcomeRetryable, StateDelivered, nil, time.Time{}), wantErr: true},
	}
	for _, test := range tests {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			err := validateAttemptResult(test.result)
			if (err != nil) != test.wantErr {
				testInstance.Errorf("validateAttemptResult error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

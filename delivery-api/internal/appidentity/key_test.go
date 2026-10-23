package appidentity

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestNewKeyShape(testInstance *testing.T) {
	key, err := NewKey()
	if err != nil {
		testInstance.Fatalf("NewKey: %v", err)
	}
	if !strings.HasPrefix(key, "dak_") {
		testInstance.Errorf("key %q lacks the dak_ prefix", key)
	}
	if got := len(key); got != len("dak_")+43 {
		testInstance.Errorf("key length = %d, want %d", got, len("dak_")+43)
	}
	if !WellFormedKey(key) {
		testInstance.Errorf("a generated key must be well formed: %q", key)
	}

	other, otherErr := NewKey()
	if otherErr != nil {
		testInstance.Fatalf("NewKey: %v", otherErr)
	}
	if key == other {
		testInstance.Error("two generated keys are equal")
	}
}

func TestWellFormedKey(testInstance *testing.T) {
	const random = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ" // 43 characters
	testCases := []struct {
		label string
		key   string
		want  bool
	}{
		{label: "exact shape", key: "dak_" + random, want: true},
		{label: "url-safe characters", key: "dak_" + strings.Repeat("-_", 21) + "A", want: true},
		{label: "empty", key: "", want: false},
		{label: "prefix only", key: "dak_", want: false},
		{label: "too short", key: "dak_" + random[:42], want: false},
		{label: "too long", key: "dak_" + random + "A", want: false},
		{label: "wrong prefix", key: "dbk_" + random, want: false},
		{label: "no prefix", key: random, want: false},
		{label: "padding character", key: "dak_" + random[:42] + "=", want: false},
		{label: "space inside", key: "dak_" + random[:20] + " " + random[21:], want: false},
		{label: "trailing newline", key: "dak_" + random + "\n", want: false},
		{label: "leading space", key: " dak_" + random, want: false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			if got := WellFormedKey(testCase.key); got != testCase.want {
				subTest.Errorf("WellFormedKey(%q) = %v, want %v", testCase.key, got, testCase.want)
			}
		})
	}
}

func TestHashKeyIsSHA256OfTheFullString(testInstance *testing.T) {
	// SHA-256("abc"), the standard test vector: pins the algorithm and that the whole string
	// (prefix included, in a real key) is what is hashed.
	const abcDigest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	got := HashKey("abc")
	if len(got) != 32 {
		testInstance.Fatalf("hash length = %d, want 32", len(got))
	}
	if hexGot := hex.EncodeToString(got); hexGot != abcDigest {
		testInstance.Errorf("HashKey(abc) = %s, want %s", hexGot, abcDigest)
	}
	if string(HashKey("dak_x")) == string(HashKey("x")) {
		testInstance.Error("the prefix must be part of the hashed string")
	}
}

func TestKeyHint(testInstance *testing.T) {
	const key = "dak_AbCdEfghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	if got := KeyHint(key); got != "dak_AbCd" {
		testInstance.Errorf("KeyHint = %q, want dak_AbCd", got)
	}
	if got := KeyHint("not-a-key-but-longer-than-eight"); got != "" {
		testInstance.Errorf("KeyHint of a malformed key = %q, want empty", got)
	}
}

func TestKeyRecordValidityRule(testInstance *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) *time.Time {
		moment := now.Add(offset)
		return &moment
	}

	testCases := []struct {
		label     string
		record    KeyRecord
		wantValid bool
		wantState KeyState
	}{
		{label: "active", record: KeyRecord{}, wantValid: true, wantState: KeyActive},
		{
			label:     "superseded a moment ago",
			record:    KeyRecord{SupersededAt: at(-time.Second)},
			wantValid: true, wantState: KeySuperseded,
		},
		{
			label:     "superseded 23h59m ago, inside the grace",
			record:    KeyRecord{SupersededAt: at(-24*time.Hour + time.Minute)},
			wantValid: true, wantState: KeySuperseded,
		},
		{
			label:     "superseded exactly 24h ago, the grace has ended",
			record:    KeyRecord{SupersededAt: at(-24 * time.Hour)},
			wantValid: false, wantState: KeyRevoked,
		},
		{
			label:     "superseded 24h1m ago, past the grace",
			record:    KeyRecord{SupersededAt: at(-24*time.Hour - time.Minute)},
			wantValid: false, wantState: KeyRevoked,
		},
		{
			label:     "revoked while active",
			record:    KeyRecord{RevokedAt: at(-time.Hour)},
			wantValid: false, wantState: KeyRevoked,
		},
		{
			label:     "revoked while superseded and inside the grace",
			record:    KeyRecord{SupersededAt: at(-time.Hour), RevokedAt: at(-time.Minute)},
			wantValid: false, wantState: KeyRevoked,
		},
		{
			label:     "revoked at a future time is still revoked",
			record:    KeyRecord{RevokedAt: at(time.Hour)},
			wantValid: false, wantState: KeyRevoked,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			if got := testCase.record.Valid(now); got != testCase.wantValid {
				subTest.Errorf("Valid = %v, want %v", got, testCase.wantValid)
			}
			if got := testCase.record.State(now); got != testCase.wantState {
				subTest.Errorf("State = %q, want %q", got, testCase.wantState)
			}
		})
	}
}

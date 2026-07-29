package httpapi

import (
	"strings"
	"testing"
)

func TestValidateEmail(t *testing.T) {
	testCases := []struct {
		name     string
		email    string
		expected bool
	}{
		{name: "valid lowercase email", email: "alice@example.com", expected: true},
		{name: "uppercase folds to valid", email: "Alice@Example.COM", expected: true},
		{name: "surrounding whitespace trimmed", email: "  alice@example.com  ", expected: true},
		{name: "empty string", email: "", expected: false},
		{name: "whitespace only", email: "   ", expected: false},
		{name: "missing @", email: "aliceexample.com", expected: false},
		{name: "multiple @", email: "alice@ex@ample.com", expected: false},
		{name: "empty local part", email: "@example.com", expected: false},
		{name: "empty domain part", email: "alice@", expected: false},
		{name: "internal whitespace", email: "alice @example.com", expected: false},
		{name: "too long", email: longLocalPart() + "@example.com", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateEmail(testCase.email); got != testCase.expected {
				t.Fatalf("validateEmail(%q) = %v, expected %v", testCase.email, got, testCase.expected)
			}
		})
	}
}

// longLocalPart returns a local-part long enough that, combined with
// "@example.com", the full address exceeds the 254-character maximum.
func longLocalPart() string {
	return strings.Repeat("a", 250)
}

func TestValidateChannel(t *testing.T) {
	testCases := []struct {
		name     string
		channel  string
		expected bool
	}{
		{name: "simple lowercase", channel: "alerts", expected: true},
		{name: "another valid", channel: "digest", expected: true},
		{name: "alnum with underscore", channel: "a_b2", expected: true},
		{name: "single char", channel: "a", expected: true},
		{name: "max length 32", channel: strings.Repeat("a", 32), expected: true},
		{name: "empty", channel: "", expected: false},
		{name: "uppercase", channel: "Alerts", expected: false},
		{name: "hyphen", channel: "has-hyphen", expected: false},
		{name: "too long 33", channel: strings.Repeat("a", 33), expected: false},
		{name: "leading underscore", channel: "_lead", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateChannel(testCase.channel); got != testCase.expected {
				t.Fatalf("validateChannel(%q) = %v, expected %v", testCase.channel, got, testCase.expected)
			}
		})
	}
}

func TestValidateMessage(t *testing.T) {
	testCases := []struct {
		name     string
		message  string
		expected bool
	}{
		{name: "empty is valid", message: "", expected: true},
		{name: "short message", message: "hello", expected: true},
		{name: "at max length 4096", message: strings.Repeat("x", 4096), expected: true},
		{name: "over max length 4097", message: strings.Repeat("x", 4097), expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateMessage(testCase.message); got != testCase.expected {
				t.Fatalf("validateMessage(len=%d) = %v, expected %v", len(testCase.message), got, testCase.expected)
			}
		})
	}
}

func TestValidateNtfyUserID(t *testing.T) {
	testCases := []struct {
		name     string
		userID   string
		expected bool
	}{
		{name: "valid derived ntfy user id", userID: "u_76gzqgp4byjl6dje", expected: true},
		{name: "missing u_ prefix", userID: "76gzqgp4byjl6dje", expected: false},
		{name: "wrong hash length", userID: "u_76gzqgp4byjl6d", expected: false},
		{name: "uppercase in hash", userID: "u_76GZQGP4BYJL6DJE", expected: false},
		{name: "arbitrary app-side user id", userID: "alice", expected: false},
		{name: "empty string", userID: "", expected: false},
		{name: "invalid base32 char (1)", userID: "u_76gzqgp4byjl6d1e", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateNtfyUserID(testCase.userID); got != testCase.expected {
				t.Fatalf("validateNtfyUserID(%q) = %v, expected %v", testCase.userID, got, testCase.expected)
			}
		})
	}
}

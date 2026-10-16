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
		{name: "standard all channel", channel: "all", expected: true},
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

func TestValidateTitleLength(t *testing.T) {
	testCases := []struct {
		name     string
		title    string
		expected bool
	}{
		{name: "typical title", title: "Chore Reaper test", expected: true},
		{name: "at max length 256", title: strings.Repeat("a", 256), expected: true},
		{name: "over max length 257", title: strings.Repeat("a", 257), expected: false},
		{name: "multi-byte at max 256 bytes", title: strings.Repeat("é", 128), expected: true},
		{name: "multi-byte over max 258 bytes", title: strings.Repeat("é", 129), expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateTitleLength(testCase.title); got != testCase.expected {
				t.Fatalf("validateTitleLength(len=%d) = %v, expected %v", len(testCase.title), got, testCase.expected)
			}
		})
	}
}

func TestValidateTitleCharacters(t *testing.T) {
	testCases := []struct {
		name     string
		title    string
		expected bool
	}{
		{name: "typical title", title: "Chore Reaper test", expected: true},
		{name: "non-ASCII letters", title: "Café test", expected: true},
		{name: "newline", title: "a\nb", expected: false},
		{name: "carriage return", title: "a\rb", expected: false},
		{name: "tab", title: "tab\tx", expected: false},
		{name: "invalid UTF-8", title: "a\xffb", expected: false},
		{name: "NUL", title: "a\x00b", expected: false},
		{name: "DEL", title: "a\x7fb", expected: false},
		{name: "C1 control NEL", title: "a\u0085b", expected: false},
		{name: "emoji", title: "Reminder \U0001F514", expected: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateTitleCharacters(testCase.title); got != testCase.expected {
				t.Fatalf("validateTitleCharacters(%q) = %v, expected %v", testCase.title, got, testCase.expected)
			}
		})
	}
}

func TestValidateRecipientsCount(t *testing.T) {
	testCases := []struct {
		name       string
		recipients []string
		expected   bool
	}{
		{name: "empty is within bound", recipients: []string{}, expected: true},
		{name: "single recipient", recipients: []string{"alice@example.com"}, expected: true},
		{name: "at max count 100", recipients: makeRecipients(maxRecipients), expected: true},
		{name: "over max count 101", recipients: makeRecipients(maxRecipients + 1), expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateRecipientsCount(testCase.recipients); got != testCase.expected {
				t.Fatalf("validateRecipientsCount(len=%d) = %v, expected %v", len(testCase.recipients), got, testCase.expected)
			}
		})
	}
}

// makeRecipients returns a slice of count placeholder recipient strings, used
// to exercise the recipient-count bound without inflating each test case.
func makeRecipients(count int) []string {
	recipients := make([]string, count)
	for index := range recipients {
		recipients[index] = "r"
	}
	return recipients
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

func TestValidatePassword(t *testing.T) {
	testCases := []struct {
		name     string
		password string
		expected bool
	}{
		{name: "empty", password: "", expected: false},
		{name: "too short 7", password: strings.Repeat("a", 7), expected: false},
		{name: "at min length 8", password: strings.Repeat("a", 8), expected: true},
		{name: "at max length 128", password: strings.Repeat("a", 128), expected: true},
		{name: "over max length 129", password: strings.Repeat("a", 129), expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validatePassword(testCase.password); got != testCase.expected {
				t.Fatalf("validatePassword(len=%d) = %v, expected %v", len(testCase.password), got, testCase.expected)
			}
		})
	}
}

package validation

import (
	"strings"
	"testing"
)

func TestIsValidEmail(t *testing.T) {
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
			if got := IsValidEmail(testCase.email); got != testCase.expected {
				t.Fatalf("IsValidEmail(%q) = %v, expected %v", testCase.email, got, testCase.expected)
			}
		})
	}
}

// longLocalPart returns a local-part long enough that, combined with
// "@example.com", the full address exceeds the 254-character maximum.
func longLocalPart() string {
	return strings.Repeat("a", 250)
}

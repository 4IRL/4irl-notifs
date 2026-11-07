package personhash

import (
	"strings"
	"testing"
)

func TestValidEmail(testInstance *testing.T) {
	testCases := []struct {
		name     string
		email    string
		expected bool
	}{
		{name: "plain", email: "alice@example.com", expected: true},
		{name: "uppercase and padding", email: "  Alice@Example.COM ", expected: true},
		{name: "empty", email: "", expected: false},
		{name: "only whitespace", email: "   ", expected: false},
		{name: "no at", email: "alice.example.com", expected: false},
		{name: "two ats", email: "a@b@example.com", expected: false},
		{name: "empty local part", email: "@example.com", expected: false},
		{name: "empty domain", email: "alice@", expected: false},
		{name: "internal space", email: "ali ce@example.com", expected: false},
		{name: "exactly 254 bytes", email: strings.Repeat("a", 242) + "@example.com", expected: true},
		{name: "255 bytes", email: strings.Repeat("a", 243) + "@example.com", expected: false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			if got := ValidEmail(testCase.email); got != testCase.expected {
				testInstance.Fatalf("ValidEmail(%q) = %v, expected %v", testCase.email, got, testCase.expected)
			}
		})
	}
}

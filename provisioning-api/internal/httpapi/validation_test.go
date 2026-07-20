package httpapi

import "testing"

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
		{name: "too long", email: strings254LocalPart() + "@example.com", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validateEmail(testCase.email); got != testCase.expected {
				t.Fatalf("validateEmail(%q) = %v, expected %v", testCase.email, got, testCase.expected)
			}
		})
	}
}

// strings254LocalPart returns a local-part long enough that, combined with
// "@example.com", the full address exceeds the 254-character maximum.
func strings254LocalPart() string {
	localPart := make([]byte, 250)
	for index := range localPart {
		localPart[index] = 'a'
	}
	return string(localPart)
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

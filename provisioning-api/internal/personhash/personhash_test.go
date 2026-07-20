package personhash

import (
	"regexp"
	"testing"
)

// hashPattern is the required shape of a person hash: 16 characters of the
// lowercased RFC 4648 base32 alphabet.
var hashPattern = regexp.MustCompile(`^[a-z2-7]{16}$`)

func TestNormalize(t *testing.T) {
	testCases := []struct {
		name     string
		email    string
		expected string
	}{
		{name: "already normalized", email: "alice@example.com", expected: "alice@example.com"},
		{name: "uppercase folded", email: "Alice@Example.COM", expected: "alice@example.com"},
		{name: "surrounding whitespace trimmed", email: "  alice@example.com\t", expected: "alice@example.com"},
		{name: "mixed case and whitespace", email: " Bob.Smith@Mail.Example.ORG ", expected: "bob.smith@mail.example.org"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if normalized := Normalize(testCase.email); normalized != testCase.expected {
				t.Fatalf("Normalize(%q) = %q, expected %q", testCase.email, normalized, testCase.expected)
			}
		})
	}
}

func TestHash(t *testing.T) {
	testCases := []struct {
		name     string
		email    string
		expected string
	}{
		{
			// Golden vector: lowercase(base32(sha256("alice@example.com")))[:16].
			name:     "golden vector",
			email:    "alice@example.com",
			expected: "76gzqgp4byjl6dje",
		},
		{
			// Normalization happens before hashing, so case/whitespace
			// variants of the same address collapse to the same hash.
			name:     "case and whitespace variant of the golden vector",
			email:    "  Alice@Example.COM ",
			expected: "76gzqgp4byjl6dje",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if hashed := Hash(testCase.email); hashed != testCase.expected {
				t.Fatalf("Hash(%q) = %q, expected %q", testCase.email, hashed, testCase.expected)
			}
		})
	}
}

func TestHashShapeAndDistinctness(t *testing.T) {
	emails := []string{
		"alice@example.com",
		"bob@example.com",
		"alice@example.org",
		"a@b",
	}

	seenHashes := make(map[string]string, len(emails))
	for _, email := range emails {
		hashed := Hash(email)
		if !hashPattern.MatchString(hashed) {
			t.Fatalf("Hash(%q) = %q, does not match %s", email, hashed, hashPattern)
		}
		if priorEmail, seen := seenHashes[hashed]; seen {
			t.Fatalf("Hash collision between %q and %q: %q", priorEmail, email, hashed)
		}
		seenHashes[hashed] = email
	}
}

func TestNtfyUser(t *testing.T) {
	testCases := []struct {
		name     string
		email    string
		expected string
	}{
		{name: "golden vector", email: "alice@example.com", expected: "u_76gzqgp4byjl6dje"},
		{name: "normalized variant matches", email: "ALICE@EXAMPLE.COM", expected: "u_76gzqgp4byjl6dje"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if ntfyUser := NtfyUser(testCase.email); ntfyUser != testCase.expected {
				t.Fatalf("NtfyUser(%q) = %q, expected %q", testCase.email, ntfyUser, testCase.expected)
			}
		})
	}
}

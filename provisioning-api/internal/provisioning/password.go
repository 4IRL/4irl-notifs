package provisioning

import (
	"crypto/rand"
	"encoding/hex"
)

// passwordEntropyBytes is the entropy of generated user passwords. Users never
// authenticate with these passwords (they use tokens) — the password only has
// to be unguessable.
const passwordEntropyBytes = 32

// GenerateRandomPassword returns a cryptographically random hex secret used
// as the throwaway password for newly created ntfy users.
func GenerateRandomPassword() (string, error) {
	entropy := make([]byte, passwordEntropyBytes)
	if _, readErr := rand.Read(entropy); readErr != nil {
		return "", readErr
	}
	return hex.EncodeToString(entropy), nil
}

// subscriberPasswordAlphabet has exactly 32 symbols (no 0 o 1 l), so indexing
// with `byte & 31` is unbiased.
const subscriberPasswordAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

const (
	subscriberPasswordSymbols   = 16
	subscriberPasswordGroupSize = 4
)

// GenerateSubscriberPassword returns a typeable xxxx-xxxx-xxxx-xxxx password
// (16 symbols, 80 bits) for a person's phone sign-in.
func GenerateSubscriberPassword() (string, error) {
	entropy := make([]byte, subscriberPasswordSymbols)
	if _, readErr := rand.Read(entropy); readErr != nil {
		return "", readErr
	}
	password := make([]byte, 0, subscriberPasswordSymbols+subscriberPasswordSymbols/subscriberPasswordGroupSize-1)
	for index, entropyByte := range entropy {
		if index > 0 && index%subscriberPasswordGroupSize == 0 {
			password = append(password, '-')
		}
		password = append(password, subscriberPasswordAlphabet[entropyByte&31])
	}
	return string(password), nil
}

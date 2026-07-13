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

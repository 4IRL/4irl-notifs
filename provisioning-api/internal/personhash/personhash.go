// Package personhash derives the stable, opaque per-person identifier the
// personalized notification model keys on. The ntfy username and every
// per-user topic embed a hash of the person's email — never the raw email —
// so topics and usernames leak nothing while remaining deterministic: the
// same person resolves to the same ntfy user from any 4IRL app, with no
// lookup database on the provisioning path.
package personhash

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// hashLength is how many characters of the base32-encoded digest form the
// person hash: 16 characters (80 bits) — comfortably collision-free for a
// personal app family while keeping topic names short.
const hashLength = 16

// ntfyUserPrefix namespaces derived ntfy usernames so they are recognizable
// and cannot collide with app publisher identities ("{app_id}-publisher").
const ntfyUserPrefix = "u_"

// Normalize canonicalizes an email address for hashing and storage:
// surrounding whitespace is trimmed and the address is lowercased. This is
// the single normalization rule shared across the stack (the person-service
// Worker applies the same rule before persisting).
func Normalize(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Hash returns the person hash for an email address:
// lowercase(base32(sha256(Normalize(email))))[:16], i.e. 16 characters of
// the lowercased RFC 4648 base32 alphabet ([a-z2-7]).
func Hash(email string) string {
	digest := sha256.Sum256([]byte(Normalize(email)))
	encoded := base32.StdEncoding.EncodeToString(digest[:])
	return strings.ToLower(encoded[:hashLength])
}

// NtfyUser returns the global ntfy username for an email address:
// "u_" + Hash(email). One ntfy user per person, shared by every 4IRL app.
func NtfyUser(email string) string {
	return ntfyUserPrefix + Hash(email)
}

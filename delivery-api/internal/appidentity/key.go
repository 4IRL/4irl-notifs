// Package appidentity is delivery-api's per-app identity: the app key format and its
// validity rule, the GitHub Actions OIDC token verifier, and the Postgres store behind the
// token exchange (bindings, registration requests, keys and the replay ledger). It holds no
// HTTP handlers; the exchange endpoint and the operator API are built on it.
package appidentity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"regexp"
	"time"
)

const (
	// KeyPrefix starts every app key, so a leaked one is recognisable and a malformed
	// Authorization header is rejected without a database lookup.
	KeyPrefix = "dak_"
	// keyRandomBytes is the entropy of a key: 32 bytes from crypto/rand.
	keyRandomBytes = 32
	// keyRandomChars is keyRandomBytes rendered as unpadded base64url.
	keyRandomChars = 43
	// keyHintRandomChars is how many leading random characters the display hint shows.
	keyHintRandomChars = 4

	// SupersededGrace is how long a key keeps working after a newer key is issued, so a
	// deploy in flight is not cut off by the next deploy's exchange.
	SupersededGrace = 24 * time.Hour
)

// keyPattern is the full shape of a key: the prefix plus keyRandomChars base64url characters.
var keyPattern = regexp.MustCompile(fmt.Sprintf("^%s[A-Za-z0-9_-]{%d}$", KeyPrefix, keyRandomChars))

// NewKey returns a fresh app key: "dak_" plus 43 base64url characters (32 random bytes).
// The plaintext must be shown to its owner once and stored only as HashKey of it.
func NewKey() (string, error) {
	random := make([]byte, keyRandomBytes)
	if _, readErr := rand.Read(random); readErr != nil {
		return "", fmt.Errorf("generate app key: %w", readErr)
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(random), nil
}

// WellFormedKey reports whether key has the exact shape NewKey produces. It says nothing
// about whether the key is issued or valid.
func WellFormedKey(key string) bool {
	return keyPattern.MatchString(key)
}

// HashKey returns the SHA-256 of the full key string, the only form in which a key is stored.
func HashKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// KeyHint returns the display hint for key: the prefix and the first few random characters.
// It is empty for a key that is not well formed, so a hint can never echo arbitrary input.
func KeyHint(key string) string {
	if !WellFormedKey(key) {
		return ""
	}
	return key[:len(KeyPrefix)+keyHintRandomChars]
}

// KeyState is the derived lifecycle state of a key.
type KeyState string

const (
	// KeyActive is a key no newer key has replaced and nobody revoked.
	KeyActive KeyState = "active"
	// KeySuperseded is a replaced key still inside its grace window.
	KeySuperseded KeyState = "superseded"
	// KeyRevoked is a key that is revoked, or whose grace window has passed.
	KeyRevoked KeyState = "revoked"
)

// KeyRecord is the part of a stored key that decides whether it is valid.
type KeyRecord struct {
	SupersededAt *time.Time
	RevokedAt    *time.Time
}

// Valid reports whether the key may be used at now. It fails closed: the result is false
// unless a case below sets it. A revoked key is never valid; an unsuperseded key is valid;
// a superseded key is valid strictly before SupersededAt + SupersededGrace.
func (record KeyRecord) Valid(now time.Time) bool {
	valid := false
	if record.RevokedAt == nil {
		if record.SupersededAt == nil {
			valid = true
		} else if now.Before(record.SupersededAt.Add(SupersededGrace)) {
			valid = true
		}
	}
	return valid
}

// State derives the lifecycle state at now: revoked when the key is not Valid (revoked, or
// past its grace window), superseded when valid but replaced, active otherwise.
func (record KeyRecord) State(now time.Time) KeyState {
	if !record.Valid(now) {
		return KeyRevoked
	}
	if record.SupersededAt != nil {
		return KeySuperseded
	}
	return KeyActive
}

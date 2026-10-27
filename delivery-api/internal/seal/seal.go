// Package seal encrypts and decrypts small secrets at rest with AES-256-GCM.
//
// A sealed blob is 0x01 version byte | 12-byte random nonce | ciphertext | 16-byte GCM tag.
// Losing the master key makes every sealed value unrecoverable: there is no escrow.
//
// During key rotation a Sealer holds a primary key and an optional previous
// key: Seal always uses the primary, Open tries the primary first and then the
// previous key, so values sealed under either key stay readable until they are
// re-sealed.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// KeySize is the required master key length in bytes (AES-256).
	KeySize = 32

	// EnvMasterKey names the environment variable (or its _FILE indirection)
	// that carries the hex-encoded master key.
	EnvMasterKey = "DELIVERY_MASTER_KEY"

	// EnvMasterKeyPrevious names the optional variable (or its _FILE
	// indirection) carrying the previous master key during a rotation window.
	EnvMasterKeyPrevious = "DELIVERY_MASTER_KEY_PREVIOUS"

	// formatVersion is the first byte of every sealed blob.
	formatVersion byte = 0x01

	nonceSize = 12
	tagSize   = 16

	hexKeyLength = 2 * KeySize
)

var (
	// ErrMalformed means the sealed input is too short to be a sealed blob.
	ErrMalformed = errors.New("sealed value is malformed")

	// ErrUnsupportedVersion means the sealed input carries an unknown format version.
	ErrUnsupportedVersion = errors.New("sealed value has an unsupported version")

	// ErrDecrypt means authentication failed: the key, the additional data or
	// the sealed bytes are wrong. The cause is deliberately not distinguished.
	ErrDecrypt = errors.New("sealed value could not be decrypted")
)

// KeyUsed reports which key opened a value, without exposing key material.
type KeyUsed int

const (
	// KeyNone is the zero value; it is never returned with a nil error.
	KeyNone KeyUsed = iota
	// KeyPrimary means the current master key opened the value.
	KeyPrimary
	// KeyPrevious means the previous master key opened the value.
	KeyPrevious
)

// Sealer seals values under one primary master key and opens them under the
// primary or, during a rotation window, an optional previous key. It is safe
// for concurrent use.
type Sealer struct {
	aead         cipher.AEAD
	previousAEAD cipher.AEAD
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("seal key must be %d bytes", KeySize)
	}
	block, blockErr := aes.NewCipher(key)
	if blockErr != nil {
		return nil, fmt.Errorf("create cipher: %w", blockErr)
	}
	aead, gcmErr := cipher.NewGCM(block)
	if gcmErr != nil {
		return nil, fmt.Errorf("create gcm: %w", gcmErr)
	}
	return aead, nil
}

// New builds a single-key Sealer from a raw key, which must be exactly KeySize bytes.
func New(key []byte) (*Sealer, error) {
	aead, aeadErr := newAEAD(key)
	if aeadErr != nil {
		return nil, aeadErr
	}
	return &Sealer{aead: aead}, nil
}

// NewWithPrevious builds a Sealer that seals under primary and opens under
// primary or previous. Both must be KeySize bytes and must differ.
func NewWithPrevious(primary, previous []byte) (*Sealer, error) {
	if len(primary) == KeySize && subtle.ConstantTimeCompare(primary, previous) == 1 {
		return nil, errors.New("previous seal key must differ from the primary key")
	}
	sealer, primaryErr := New(primary)
	if primaryErr != nil {
		return nil, primaryErr
	}
	previousAEAD, previousErr := newAEAD(previous)
	if previousErr != nil {
		return nil, fmt.Errorf("previous key: %w", previousErr)
	}
	sealer.previousAEAD = previousAEAD
	return sealer, nil
}

// HasPrevious reports whether a previous key is configured.
func (sealer *Sealer) HasPrevious() bool {
	return sealer.previousAEAD != nil
}

// PrimaryOnly returns a view with the same primary key and no previous key.
func (sealer *Sealer) PrimaryOnly() *Sealer {
	return &Sealer{aead: sealer.aead}
}

// Seal encrypts plaintext, binding it to aad, and returns the sealed blob
// (version byte, random nonce, ciphertext and tag).
func (sealer *Sealer) Seal(plaintext, aad []byte) ([]byte, error) {
	sealed := make([]byte, 1+nonceSize, 1+nonceSize+len(plaintext)+tagSize)
	sealed[0] = formatVersion
	nonce := sealed[1 : 1+nonceSize]
	if _, readErr := io.ReadFull(rand.Reader, nonce); readErr != nil {
		return nil, fmt.Errorf("generate nonce: %w", readErr)
	}
	return sealer.aead.Seal(sealed, nonce, plaintext, aad), nil
}

// Open authenticates and decrypts a blob produced by Seal with the same aad.
// It returns ErrMalformed for input that is too short, ErrUnsupportedVersion
// for an unknown version byte and ErrDecrypt for any authentication failure.
// It never returns plaintext alongside an error.
func (sealer *Sealer) Open(sealed, aad []byte) ([]byte, error) {
	plaintext, _, openErr := sealer.OpenReportingKey(sealed, aad)
	return plaintext, openErr
}

// OpenReportingKey is Open that also reports which key opened the value. The
// blob checks come first and never fall back; the primary key is tried first
// and the previous key only after an authentication failure. Both failing
// returns ErrDecrypt with KeyNone.
func (sealer *Sealer) OpenReportingKey(sealed, aad []byte) ([]byte, KeyUsed, error) {
	if len(sealed) < 1+nonceSize+tagSize {
		return nil, KeyNone, ErrMalformed
	}
	if sealed[0] != formatVersion {
		return nil, KeyNone, ErrUnsupportedVersion
	}
	nonce := sealed[1 : 1+nonceSize]
	body := sealed[1+nonceSize:]
	plaintext, openErr := sealer.aead.Open(nil, nonce, body, aad)
	if openErr == nil {
		return plaintext, KeyPrimary, nil
	}
	if sealer.previousAEAD != nil {
		plaintext, openErr = sealer.previousAEAD.Open(nil, nonce, body, aad)
		if openErr == nil {
			return plaintext, KeyPrevious, nil
		}
	}
	return nil, KeyNone, ErrDecrypt
}

// ParseHexKey decodes a master key given as exactly 64 lowercase hex
// characters. The error never contains the value.
func ParseHexKey(value string) ([]byte, error) {
	return parseHexKeyNamed(EnvMasterKey, value)
}

func parseHexKeyNamed(name, value string) ([]byte, error) {
	key, decodeErr := hex.DecodeString(value)
	if len(value) != hexKeyLength || strings.ToLower(value) != value || decodeErr != nil {
		return nil, fmt.Errorf("%s must be 64 lowercase hex characters", name)
	}
	return key, nil
}

// FromEnv resolves the master key through resolveSecret (typically
// secretenv.Resolve, which honors the DELIVERY_MASTER_KEY_FILE indirection),
// validates it and builds a Sealer. A missing or malformed key is an error so
// the service fails fast; no error message contains the key value.
//
// It also resolves the optional DELIVERY_MASTER_KEY_PREVIOUS: "" means absent
// (single-key); a resolver error, a malformed value or a value equal to the
// primary is an error, never silently ignored.
func FromEnv(resolveSecret func(string) (string, error)) (*Sealer, error) {
	value, resolveErr := resolveSecret(EnvMasterKey)
	if resolveErr != nil {
		return nil, fmt.Errorf("resolve %s: %w", EnvMasterKey, resolveErr)
	}
	if value == "" {
		return nil, fmt.Errorf("%s is required", EnvMasterKey)
	}
	key, parseErr := ParseHexKey(value)
	if parseErr != nil {
		return nil, parseErr
	}

	previousValue, previousResolveErr := resolveSecret(EnvMasterKeyPrevious)
	if previousResolveErr != nil {
		return nil, fmt.Errorf("resolve %s: %w", EnvMasterKeyPrevious, previousResolveErr)
	}
	if previousValue == "" {
		return New(key)
	}
	previousKey, previousParseErr := parseHexKeyNamed(EnvMasterKeyPrevious, previousValue)
	if previousParseErr != nil {
		return nil, previousParseErr
	}
	if subtle.ConstantTimeCompare(key, previousKey) == 1 {
		return nil, fmt.Errorf("%s must differ from %s", EnvMasterKeyPrevious, EnvMasterKey)
	}
	return NewWithPrevious(key, previousKey)
}

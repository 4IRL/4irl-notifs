// Package seal encrypts and decrypts small secrets at rest with AES-256-GCM.
//
// A sealed blob has the layout
//
//	0x01 version byte | 12-byte random nonce | ciphertext | 16-byte GCM tag
//
// The caller supplies additional authenticated data (aad) on every Seal and
// Open, so a blob sealed for one row cannot be moved onto another: opening it
// with a different aad fails authentication.
//
// The 32-byte master key is delivered as the DELIVERY_MASTER_KEY secret
// (64 lowercase hex characters). Losing the master key makes every sealed
// value unrecoverable: there is no escrow and no fallback. Errors from this
// package never contain key material or plaintext.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
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

// Sealer seals and opens values under one master key. It is safe for
// concurrent use.
type Sealer struct {
	aead cipher.AEAD
}

// New builds a Sealer from a raw key, which must be exactly KeySize bytes.
func New(key []byte) (*Sealer, error) {
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
	return &Sealer{aead: aead}, nil
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
	if len(sealed) < 1+nonceSize+tagSize {
		return nil, ErrMalformed
	}
	if sealed[0] != formatVersion {
		return nil, ErrUnsupportedVersion
	}
	nonce := sealed[1 : 1+nonceSize]
	plaintext, openErr := sealer.aead.Open(nil, nonce, sealed[1+nonceSize:], aad)
	if openErr != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// ParseHexKey decodes a master key given as exactly 64 lowercase hex
// characters. The error never contains the value.
func ParseHexKey(value string) ([]byte, error) {
	key, decodeErr := hex.DecodeString(value)
	if len(value) != hexKeyLength || strings.ToLower(value) != value || decodeErr != nil {
		return nil, fmt.Errorf("%s must be 64 lowercase hex characters", EnvMasterKey)
	}
	return key, nil
}

// FromEnv resolves the master key through resolveSecret (typically
// secretenv.Resolve, which honors the DELIVERY_MASTER_KEY_FILE indirection),
// validates it and builds a Sealer. A missing or malformed key is an error so
// the service fails fast; no error message contains the key value.
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
	return New(key)
}

// Package publisher holds the per-app ntfy publisher token: minted once through
// provisioning-api by an explicit operator action, stored sealed, and read back
// by the ntfy sink when it publishes a to-recipient envelope. types.go is pure
// domain logic; store.go, pgstore.go and service.go are the I/O layer.
//
// The import graph stays acyclic: publisher imports registry, seal and
// provisionclient, and never dispatch or message.
package publisher

import (
	"errors"
	"time"
)

// ErrMintUnavailable means minting is not configured (no provisioning-api origin).
var ErrMintUnavailable = errors.New("publisher minting is unavailable")

// ValidationError reports a request field that failed validation. Message is
// safe to return to the client: it names the field, never its value. It mirrors
// registry.ValidationError and message.ValidationError so this package keeps its
// own errors.
type ValidationError struct {
	// Message names the offending field, for example "invalid app_id".
	Message string
}

// Error returns the validation message.
func (validationError *ValidationError) Error() string { return validationError.Message }

// StoredPublisher is a publisher as persisted. SealedToken holds the AES-GCM
// sealed ntfy token and is never serialized.
type StoredPublisher struct {
	AppID           string
	PublisherUserID string
	TopicPattern    string
	SealedToken     []byte `json:"-"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PublisherView is the operator-facing publisher record. It carries no token.
// It is the public admin-API view of a publisher and reads naturally as
// publisher.PublisherView at call sites, so the revive stutter check is waived.
//
//nolint:revive // the admin-API view; publisher.PublisherView reads clearly at call sites
type PublisherView struct {
	AppID           string    `json:"app_id"`
	PublisherUserID string    `json:"publisher_user_id"`
	TopicPattern    string    `json:"topic_pattern"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

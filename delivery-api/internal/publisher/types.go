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
	"regexp"
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

// appIDPattern is the app identifier shape, the same rule the messages and
// app_publishers tables enforce.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

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
// The name is fixed by the delivery-sinks plan and is referenced by the admin
// API, so the revive stutter check is waived here.
//
//nolint:revive // plan-mandated name
type PublisherView struct {
	AppID           string    `json:"app_id"`
	PublisherUserID string    `json:"publisher_user_id"`
	TopicPattern    string    `json:"topic_pattern"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

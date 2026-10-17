// Package registry holds the operator-managed delivery registry: destinations
// (ntfy, Discord, webhook) and routes (app pattern and channel pattern mapped
// to a set of destinations). types.go, validate.go, mask.go and match.go are
// pure domain logic (types, validation, secret masking and route matching)
// with no I/O. pgstore.go holds the Postgres store and service.go the
// orchestration (sealing, logging).
package registry

import (
	"errors"
	"time"
)

// Destination types accepted by the registry.
const (
	// TypeNtfy is a destination that publishes to an ntfy topic.
	TypeNtfy = "ntfy"
	// TypeDiscord is a destination that posts to a Discord webhook.
	TypeDiscord = "discord"
	// TypeWebhook is a destination that posts a signed request to an HTTPS URL.
	TypeWebhook = "webhook"
)

// RecipientDestinationName is the synthetic destination_name of a to.recipient
// delivery; '@' is outside destinationNamePattern, so no operator destination
// can collide with it.
const RecipientDestinationName = "@recipient"

// Sentinel errors returned by the registry service and store. Handlers map
// them to HTTP statuses at a single chokepoint.
var (
	// ErrDestinationNotFound means no destination has the requested name or id.
	ErrDestinationNotFound = errors.New("destination not found")
	// ErrDestinationExists means a destination with that name already exists.
	ErrDestinationExists = errors.New("destination already exists")
	// ErrDestinationInUse means a route still references the destination.
	ErrDestinationInUse = errors.New("destination is referenced by a route")
	// ErrDestinationDisabled means the destination exists but is disabled.
	ErrDestinationDisabled = errors.New("destination is disabled")
	// ErrUnknownDestination means a name in a route or envelope does not name
	// an existing destination.
	ErrUnknownDestination = errors.New("unknown destination")
	// ErrRouteNotFound means no route has the requested id.
	ErrRouteNotFound = errors.New("route not found")
	// ErrRouteExists means a route with that app and channel pattern exists.
	ErrRouteExists = errors.New("route already exists")
	// ErrNoPublisher means an app has no ntfy publisher token. It lives here
	// rather than in the publisher package so dispatch and message can match it
	// without importing publisher.
	ErrNoPublisher = errors.New("app has no ntfy publisher")
)

// ValidationError reports a request field that failed validation. Message is
// safe to return to the client: it names the field, never its value.
type ValidationError struct {
	// Message names the offending field, for example "invalid config.topic".
	Message string
}

// Error returns the validation message.
func (validationError *ValidationError) Error() string { return validationError.Message }

// StoredDestination is a destination as persisted. SealedSecrets holds the
// AES-GCM sealed JSON object of every secret field and is never serialized.
type StoredDestination struct {
	ID             int64
	Name           string
	Type           string
	Config         map[string]string
	SealedSecrets  []byte `json:"-"`
	Enabled        bool
	DisabledReason *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StoredRoute is a route as persisted, with its destination ids and names
// resolved by the store.
type StoredRoute struct {
	ID               int64
	AppPattern       string
	ChannelPattern   string
	DestinationIDs   []int64
	DestinationNames []string
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// DestinationView is the public representation of a destination. Secrets are
// always masked: the plaintext never leaves the service.
type DestinationView struct {
	Name           string            `json:"name"`
	Type           string            `json:"type"`
	Enabled        bool              `json:"enabled"`
	DisabledReason *string           `json:"disabled_reason"`
	Config         map[string]string `json:"config"`
	Secrets        map[string]string `json:"secrets"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// RouteView is the public representation of a route.
type RouteView struct {
	ID             int64     `json:"id"`
	AppPattern     string    `json:"app_pattern"`
	ChannelPattern string    `json:"channel_pattern"`
	Destinations   []string  `json:"destinations"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ResolvedDestination is a destination with its secrets decrypted, handed to
// sinks only. It carries plaintext secrets and must never be serialized,
// logged or returned from an API; it deliberately has no JSON tags.
type ResolvedDestination struct {
	ID      int64
	Name    string
	Type    string
	Config  map[string]string
	Secrets map[string]string
}

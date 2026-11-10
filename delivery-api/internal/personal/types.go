// Package personal holds end-user personal destinations: a person's own
// Discord or webhook endpoint, registered by an app on their behalf and opted in
// per app. The service stores only the person's hash, never the email. This file
// is pure domain (types, sentinels, the delivery-name and sealing helpers);
// pgstore.go holds the Postgres store.
package personal

import (
	"errors"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// Personal destination types: discord or webhook only. A personal ntfy type is
// refused on purpose (the ntfy sink skips the SSRF-guarded transport).
const (
	TypeDiscord = registry.TypeDiscord
	TypeWebhook = registry.TypeWebhook
)

// DisabledBy says who disabled a personal destination.
type DisabledBy string

// Who may disable a personal destination.
const (
	// DisabledByApp is an app's PATCH {"enabled": false}.
	DisabledByApp DisabledBy = "app"
	// DisabledByOperator is the operator's admin disable; an app cannot undo it.
	DisabledByOperator DisabledBy = "operator"
	// DisabledBySystem is the worker disabling the row after a 410 Gone.
	DisabledBySystem DisabledBy = "system"
)

// MaxPerPersonPerApp caps the personal destinations one person may hold that
// one app registered; each registering app has its own allowance. It bounds
// storage only (a destination is one small row); the number of destinations a
// single message may name is the separate message.MaxPersonalDestinationsPerMessage.
const MaxPerPersonPerApp = 1000

// AdminListLimit is how many of the newest rows the operator list returns.
const AdminListLimit = 200

// Sentinel errors returned by the store (and, from Step 2, the service).
var (
	// ErrNotFound means no personal destination matches (absent, or another person's).
	ErrNotFound = errors.New("personal destination does not exist")
	// ErrManagedByOtherApp means the caller is not the (live) registering app.
	ErrManagedByOtherApp = errors.New("personal destination is managed by another app")
	// ErrDisabledByOperator means an app tried to enable a row the operator disabled.
	ErrDisabledByOperator = errors.New("personal destination disabled by the operator")
	// ErrLimitReached means the person already holds MaxPerPersonPerApp destinations registered by the app.
	ErrLimitReached = errors.New("personal destination limit reached")
	// ErrNotOptedIn means the app has no opt-in for the destination.
	ErrNotOptedIn = errors.New("personal destination is not opted in for the app")
	// ErrDisabled means the destination is disabled.
	ErrDisabled = errors.New("personal destination is disabled")
)

// Destination is one stored personal destination. SealedSecrets stays sealed;
// callers open it with OpenSecrets.
type Destination struct {
	ID                 int64
	PersonHash         string
	RegisteredByAppID  string
	RegistrarRemovedAt *time.Time
	Label              string
	Type               string
	Config             map[string]string
	SealedSecrets      []byte
	Enabled            bool
	DisabledBy         *DisabledBy
	DisabledReason     *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewDestination is the input to PGStore.Insert. Secrets are sealed by the store
// under the final row id before the row is written.
type NewDestination struct {
	PersonHash        string
	RegisteredByAppID string
	Label             string
	Type              string
	Config            map[string]string
	Secrets           map[string]string
	// OptIn also opts the destination in for RegisteredByAppID in the same transaction.
	OptIn bool
}

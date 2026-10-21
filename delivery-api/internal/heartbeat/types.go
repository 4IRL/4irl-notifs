// Package heartbeat holds delivery-api's heartbeat checks: an app pings a named
// check on a schedule, and a check that misses its period plus grace goes down
// and announces it (once) through the message pipeline. types.go, validate.go
// and machine.go are pure domain logic with no I/O and no clock (callers pass
// `now`); pgstore.go holds the Postgres store.
package heartbeat

import (
	"errors"
	"time"
)

// Stored check states. "late" is derived at read time (see DerivedState) and is
// never stored, so it is deliberately not a stored-state constant.
const (
	// StateNew is a check that has never been pinged.
	StateNew = "new"
	// StateUp is a check whose last ping arrived in time.
	StateUp = "up"
	// StateDown is a check that missed its deadline or reported a failure.
	StateDown = "down"
	// StateLate is the derived read state of an up check past its period but
	// still inside the grace window.
	StateLate = "late"
)

// Ping kinds.
const (
	// PingSuccess is a plain ping: the job ran.
	PingSuccess = "success"
	// PingFail is a /fail ping: the job reports that it failed.
	PingFail = "fail"
)

// Reasons a check went down.
const (
	// ReasonMissed means no ping arrived by the deadline.
	ReasonMissed = "missed"
	// ReasonFail means the app reported a failure.
	ReasonFail = "fail"
)

// Outbox alert kinds held in StoredCheck.PendingAlert.
const (
	// AlertDown is the announcement that a check went down.
	AlertDown = "down"
	// AlertRecovered is the announcement that a down check recovered.
	AlertRecovered = "recovered"
)

// Sentinel errors returned by the heartbeat store. Handlers map them to HTTP
// statuses at a single chokepoint.
var (
	// ErrCheckNotFound means no check has the requested id, or app and name.
	ErrCheckNotFound = errors.New("heartbeat check not found")
	// ErrCheckExists means a check with that app_id and name already exists.
	ErrCheckExists = errors.New("heartbeat check already exists")
)

// ValidationError reports a request field that failed validation. Message is
// safe to return to the client: it names the field, never its value.
type ValidationError struct {
	// Message names the offending field, for example "invalid period_seconds".
	Message string
}

// Error returns the validation message.
func (validationError *ValidationError) Error() string { return validationError.Message }

// DestinationRef is a check's destination as listed: just enough for an
// operator view and for the checker to skip a disabled destination.
type DestinationRef struct {
	Name    string
	Type    string
	Enabled bool
}

// StoredCheck is a heartbeat check as persisted. DownSince survives a recovery for the
// recovery alert; use ActiveDownSince for display.
type StoredCheck struct {
	ID            int64
	AppID         string
	Name          string
	PeriodSeconds int
	GraceSeconds  int
	State         string
	Paused        bool
	// AnchorAt is when the deadline clock started: creation, the latest ping or
	// the latest resume.
	AnchorAt     time.Time
	LastPingAt   *time.Time
	LastPingKind *string
	DownSince    *time.Time
	DownReason   *string
	// AlertSeq counts the alerts this check has emitted.
	AlertSeq int
	// PendingAlert is the outbox: the alert kind not yet flushed, or nil.
	PendingAlert *string
	Destinations []DestinationRef
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewCheck is the input to create a check.
type NewCheck struct {
	AppID            string
	Name             string
	PeriodSeconds    int
	GraceSeconds     int
	DestinationNames []string
}

// CheckUpdate is the input to change a check's schedule and destinations; the
// app_id and name are immutable.
type CheckUpdate struct {
	PeriodSeconds    int
	GraceSeconds     int
	DestinationNames []string
}

// CheckPatch is a partial CheckUpdate: a nil field keeps the check's current
// value. The store merges it into the row it holds locked (see Store.Patch), so
// two patches naming different fields can never revert each other.
type CheckPatch struct {
	PeriodSeconds    *int
	GraceSeconds     *int
	DestinationNames []string
}

// IsEmpty reports whether the patch names no field at all.
func (patch CheckPatch) IsEmpty() bool {
	return patch.PeriodSeconds == nil && patch.GraceSeconds == nil && patch.DestinationNames == nil
}

// ApplyPatch returns the update that results from merging patch into current:
// every field the patch leaves nil keeps current's value. The destination names
// of current are in name order, as the store lists them.
func ApplyPatch(current StoredCheck, patch CheckPatch) CheckUpdate {
	update := CheckUpdate{
		PeriodSeconds:    current.PeriodSeconds,
		GraceSeconds:     current.GraceSeconds,
		DestinationNames: make([]string, 0, len(current.Destinations)),
	}
	for _, destination := range current.Destinations {
		update.DestinationNames = append(update.DestinationNames, destination.Name)
	}
	if patch.PeriodSeconds != nil {
		update.PeriodSeconds = *patch.PeriodSeconds
	}
	if patch.GraceSeconds != nil {
		update.GraceSeconds = *patch.GraceSeconds
	}
	if patch.DestinationNames != nil {
		update.DestinationNames = patch.DestinationNames
	}
	return update
}

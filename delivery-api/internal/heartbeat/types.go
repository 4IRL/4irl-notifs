// Package heartbeat holds delivery-api's heartbeat checks: an app pings a named
// check on a schedule, and a check that misses its period plus grace goes down
// and announces it (once) through the message pipeline. types.go, validate.go
// and machine.go are pure domain logic with no I/O and no clock (callers pass
// `now`); pgstore.go holds the Postgres store.
package heartbeat

import (
	"errors"
	"slices"
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

// Reasons a check went down. Every new Reason const must also be added to
// KnownDownReasons, which feeds the reason-set checks and the database CHECK
// test.
const (
	// ReasonMissed means no ping arrived by the deadline.
	ReasonMissed = "missed"
	// ReasonFail means the app reported a failure.
	ReasonFail = "fail"
	// ReasonHTTPStatus means a probe got a status outside the expected set.
	ReasonHTTPStatus = "http_status"
	// ReasonTimeout means a probe timed out.
	ReasonTimeout = "timeout"
	// ReasonBodyMismatch means a probe response lacked the expected body text.
	ReasonBodyMismatch = "body_mismatch"
	// ReasonBlocked means the transport guard refused the probe target.
	ReasonBlocked = "blocked"
	// ReasonTLS means a probe failed the TLS handshake or verification.
	ReasonTLS = "tls"
	// ReasonScheme means a probe was redirected to a non-https URL.
	ReasonScheme = "scheme"
	// ReasonNetwork means a probe hit a network error such as DNS or refusal.
	ReasonNetwork = "network"
	// ReasonConfig means the probe could not run because its config is unusable.
	ReasonConfig = "config"
)

// KnownDownReasons returns a fresh slice of every down reason: the two push
// reasons and the eight probe result reasons.
func KnownDownReasons() []string {
	return []string{
		ReasonMissed,
		ReasonFail,
		ReasonHTTPStatus,
		ReasonTimeout,
		ReasonBodyMismatch,
		ReasonBlocked,
		ReasonTLS,
		ReasonScheme,
		ReasonNetwork,
		ReasonConfig,
	}
}

// IsProbeResultReason reports whether reason is one of the eight reasons a
// probe result can carry. missed and fail are not probe result reasons.
func IsProbeResultReason(reason string) bool {
	switch reason {
	case ReasonHTTPStatus, ReasonTimeout, ReasonBodyMismatch, ReasonBlocked,
		ReasonTLS, ReasonScheme, ReasonNetwork, ReasonConfig:
		return true
	default:
		return false
	}
}

// IsKnownDownReason reports whether reason is any recognised down reason.
func IsKnownDownReason(reason string) bool {
	return slices.Contains(KnownDownReasons(), reason)
}

// Check kinds. A check is a probe check exactly when it has probe state.
const (
	// KindPush is a check an app pings.
	KindPush = "push"
	// KindProbe is a check the delivery service probes over HTTP.
	KindProbe = "probe"
)

// Probe limits and the failure threshold.
const (
	// ProbeFailureThreshold is the number of consecutive failing results that
	// takes a probe check down.
	ProbeFailureThreshold = 3
	// MinProbeIntervalSeconds bounds a probe interval from below.
	MinProbeIntervalSeconds = 60
	// MaxProbeIntervalSeconds bounds a probe interval from above (one day).
	MaxProbeIntervalSeconds = 86_400
	// MinProbeTimeoutSeconds bounds a probe timeout from below.
	MinProbeTimeoutSeconds = 1
	// MaxProbeTimeoutSeconds bounds a probe timeout from above.
	MaxProbeTimeoutSeconds = 8
	// MaxProbeURLBytes bounds a probe URL's length.
	MaxProbeURLBytes = 2048
	// MaxExpectedStatuses bounds the expected status set size.
	MaxExpectedStatuses = 10
	// MaxBodyMatchBytes bounds the body match text.
	MaxBodyMatchBytes = 256
	// MaxAccessCredentialBytes bounds each Access credential half.
	MaxAccessCredentialBytes = 256
	// MaxProbeDetailBytes bounds a stored probe detail.
	MaxProbeDetailBytes = 256
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
	// ErrProbeCheckPing means a push ping targeted a probe check.
	ErrProbeCheckPing = errors.New("heartbeat: push ping on a probe check")
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
	// OwnerKind is registry.OwnerOperator or registry.OwnerApp; OwnerAppID is set
	// exactly when it is an app. This is the owner, distinct from AppID, which is
	// the ping identity.
	OwnerKind  string
	OwnerAppID *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// Probe is the probe state; nil for a push check.
	Probe *ProbeState
}

// Kind returns KindProbe when the check has probe state, else KindPush.
func (check StoredCheck) Kind() string {
	if check.Probe != nil {
		return KindProbe
	}
	return KindPush
}

// ProbeConfig is the operator-set configuration of a probe check.
type ProbeConfig struct {
	URL              string
	ExpectedStatuses []int
	BodyMatch        *string
	TimeoutSeconds   int
	IntervalSeconds  int
}

// AccessToken is a Cloudflare Access service token pair a probe presents.
type AccessToken struct {
	ClientID     string
	ClientSecret string
}

// ProbeResultRecord is the last accepted probe result as persisted.
type ProbeResultRecord struct {
	At     time.Time
	Status *int
	Reason *string
	Detail *string
}

// ProbeState is a probe check's persisted probe row.
type ProbeState struct {
	Config              ProbeConfig
	HasAccessToken      bool
	SealedAccessToken   []byte
	ConsecutiveFailures int
	NextProbeAt         time.Time
	ConfigVersion       int64
	LastResult          *ProbeResultRecord
	DownDetail          *string
}

// ProbeResult is one probe attempt's outcome. A nil Reason is a success.
type ProbeResult struct {
	Status *int
	Reason *string
	Detail string
}

// NewCheck is the input to create a check. An empty OwnerKind is stored as
// registry.OwnerOperator. Probe and AccessToken make it a probe check.
type NewCheck struct {
	AppID            string
	Name             string
	PeriodSeconds    int
	GraceSeconds     int
	DestinationNames []string
	OwnerKind        string
	OwnerAppID       *string
	Probe            *ProbeConfig
	AccessToken      *AccessToken
}

// CheckUpdate is the input to change a check's schedule and destinations; the
// app_id and name are immutable. Probe is the merged probe config, nil for a
// push check.
type CheckUpdate struct {
	PeriodSeconds    int
	GraceSeconds     int
	DestinationNames []string
	Probe            *ProbeConfig
	AccessToken      AccessTokenPatch
}

// ProbePatch is a partial ProbeConfig: a nil field keeps the current value.
type ProbePatch struct {
	URL              *string
	ExpectedStatuses []int // nil = unchanged
	BodyMatch        *string
	ClearBodyMatch   bool
	TimeoutSeconds   *int
	IntervalSeconds  *int
}

// AccessTokenAction says what a patch does to the stored Access token.
type AccessTokenAction int

const (
	// AccessTokenUnchanged leaves the stored token alone (the zero value).
	AccessTokenUnchanged AccessTokenAction = iota
	// AccessTokenReplace seals and stores the supplied token.
	AccessTokenReplace
	// AccessTokenClear removes the stored token.
	AccessTokenClear
)

// AccessTokenPatch is the token part of a patch.
type AccessTokenPatch struct {
	Action AccessTokenAction
	Token  AccessToken
}

// CheckPatch is a partial CheckUpdate: a nil field keeps the check's current
// value. The store merges it into the row it holds locked (see Store.Patch), so
// two patches naming different fields can never revert each other.
type CheckPatch struct {
	PeriodSeconds    *int
	GraceSeconds     *int
	DestinationNames []string
	Probe            *ProbePatch
	AccessToken      AccessTokenPatch
}

// IsEmpty reports whether the patch names no field at all.
func (patch CheckPatch) IsEmpty() bool {
	return patch.PeriodSeconds == nil && patch.GraceSeconds == nil && patch.DestinationNames == nil &&
		patch.Probe == nil && patch.AccessToken.Action == AccessTokenUnchanged
}

// ApplyPatch returns the update that results from merging patch into current:
// every field the patch leaves nil keeps current's value. The destination names
// of current are in name order, as the store lists them. For a probe check the
// probe config is merged field by field and period/grace are derived from the
// merged interval; for a push check Probe stays nil.
func ApplyPatch(current StoredCheck, patch CheckPatch) CheckUpdate {
	update := CheckUpdate{
		PeriodSeconds:    current.PeriodSeconds,
		GraceSeconds:     current.GraceSeconds,
		DestinationNames: make([]string, 0, len(current.Destinations)),
		AccessToken:      patch.AccessToken,
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
	if current.Probe != nil {
		merged := mergeProbeConfig(current.Probe.Config, patch.Probe)
		update.Probe = &merged
		update.PeriodSeconds, update.GraceSeconds = DerivedSchedule(merged.IntervalSeconds)
	}
	return update
}

// mergeProbeConfig returns current with every field patch names replaced.
// ClearBodyMatch wins over BodyMatch. The result owns its slice and statuses
// are ascending.
func mergeProbeConfig(current ProbeConfig, patch *ProbePatch) ProbeConfig {
	merged := current
	merged.ExpectedStatuses = NormalizeStatuses(current.ExpectedStatuses)
	if patch == nil {
		return merged
	}
	if patch.URL != nil {
		merged.URL = *patch.URL
	}
	if patch.ExpectedStatuses != nil {
		merged.ExpectedStatuses = NormalizeStatuses(patch.ExpectedStatuses)
	}
	if patch.BodyMatch != nil {
		merged.BodyMatch = patch.BodyMatch
	}
	if patch.ClearBodyMatch {
		merged.BodyMatch = nil
	}
	if patch.TimeoutSeconds != nil {
		merged.TimeoutSeconds = *patch.TimeoutSeconds
	}
	if patch.IntervalSeconds != nil {
		merged.IntervalSeconds = *patch.IntervalSeconds
	}
	return merged
}

// NormalizeStatuses returns a fresh ascending copy of statuses (nil stays nil)
// so set equality is order-free.
func NormalizeStatuses(statuses []int) []int {
	if statuses == nil {
		return nil
	}
	normalized := slices.Clone(statuses)
	slices.Sort(normalized)
	return normalized
}

// ProbeConfigChanged reports whether merged differs from current (statuses
// compared after normalisation, body match by value) or the token action is
// Replace or Clear.
func ProbeConfigChanged(current ProbeConfig, merged ProbeConfig, token AccessTokenPatch) bool {
	if token.Action == AccessTokenReplace || token.Action == AccessTokenClear {
		return true
	}
	if current.URL != merged.URL ||
		current.TimeoutSeconds != merged.TimeoutSeconds ||
		current.IntervalSeconds != merged.IntervalSeconds {
		return true
	}
	if !slices.Equal(NormalizeStatuses(current.ExpectedStatuses), NormalizeStatuses(merged.ExpectedStatuses)) {
		return true
	}
	if (current.BodyMatch == nil) != (merged.BodyMatch == nil) {
		return true
	}
	return current.BodyMatch != nil && *current.BodyMatch != *merged.BodyMatch
}

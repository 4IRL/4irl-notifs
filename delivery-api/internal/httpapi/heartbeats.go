package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// Error messages returned by the heartbeat endpoints.
const (
	errMsgHeartbeatsUnavailable  = "heartbeats unavailable"
	errMsgInvalidCheckID         = "invalid check id"
	errMsgCheckNotFound          = "check not found"
	errMsgCheckExists            = "check already exists"
	errMsgCheckIdentityImmutable = "app_id and name are immutable"
	errMsgProbeCheckPing         = "check is a probe check"
)

// Path value names of the heartbeat routes. The check id of the admin routes
// reuses pathValueID.
const (
	pathValueAppID = "app_id"
	pathValueName  = "name"
)

// Heartbeats is the heartbeat surface the /v1/heartbeats and
// /v1/admin/heartbeats endpoints call. *heartbeat.Service satisfies it.
type Heartbeats interface {
	// Create validates and persists a new check.
	Create(ctx context.Context, check heartbeat.NewCheck) (heartbeat.StoredCheck, error)
	// Get returns the check with that id, or heartbeat.ErrCheckNotFound.
	Get(ctx context.Context, id int64) (heartbeat.StoredCheck, error)
	// List returns every check ordered by app_id, then name.
	List(ctx context.Context) ([]heartbeat.StoredCheck, error)
	// Patch merges the patch into the check atomically (a nil field keeps its
	// current value) and validates the merged result.
	Patch(ctx context.Context, id int64, patch heartbeat.CheckPatch) (heartbeat.StoredCheck, error)
	// Delete removes the check and reports whether it existed.
	Delete(ctx context.Context, id int64) (bool, error)
	// Pause pauses the check; pausing a paused check is a no-op.
	Pause(ctx context.Context, id int64) (heartbeat.StoredCheck, error)
	// Resume resumes a paused check; resuming a running check is a no-op.
	Resume(ctx context.Context, id int64) (heartbeat.StoredCheck, error)
	// Ping records a success ping for the check named by app_id and name.
	Ping(ctx context.Context, appID string, name string) (heartbeat.StoredCheck, error)
	// Fail records a /fail ping for the check named by app_id and name.
	Fail(ctx context.Context, appID string, name string) (heartbeat.StoredCheck, error)
}

// createCheckRequestBody is the POST /v1/admin/heartbeats body.
type createCheckRequestBody struct {
	AppID         string   `json:"app_id"`
	Name          string   `json:"name"`
	PeriodSeconds int      `json:"period_seconds"`
	GraceSeconds  int      `json:"grace_seconds"`
	Destinations  []string `json:"destinations"`
}

// patchCheckRequestBody is the PATCH /v1/admin/heartbeats/{id} body. Any subset
// of the schedule fields may be sent. AppID and Name are accepted by the decoder
// only so that sending either (even as null) is the dedicated immutability 400
// rather than an unknown-field 400.
type patchCheckRequestBody struct {
	AppID         json.RawMessage `json:"app_id"`
	Name          json.RawMessage `json:"name"`
	PeriodSeconds *int            `json:"period_seconds"`
	GraceSeconds  *int            `json:"grace_seconds"`
	Destinations  []string        `json:"destinations"`
}

// pingResponseBody is the 200 body of a ping: the check's derived state after it.
type pingResponseBody struct {
	AppID string `json:"app_id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// checkDestinationView is one destination a check alerts.
type checkDestinationView struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// checkView is the operator JSON form of one heartbeat check. Timestamps are
// RFC3339 UTC or null, and Destinations is never null. State is the derived
// state (new, up, late or down); Paused is reported separately.
type checkView struct {
	ID            int64                  `json:"id"`
	AppID         string                 `json:"app_id"`
	Name          string                 `json:"name"`
	PeriodSeconds int                    `json:"period_seconds"`
	GraceSeconds  int                    `json:"grace_seconds"`
	Paused        bool                   `json:"paused"`
	State         string                 `json:"state"`
	LastPingAt    *string                `json:"last_ping_at"`
	LastPingKind  *string                `json:"last_ping_kind"`
	DownSince     *string                `json:"down_since"`
	DownReason    *string                `json:"down_reason"`
	DeadlineAt    string                 `json:"deadline_at"`
	Destinations  []checkDestinationView `json:"destinations"`
	OwnerKind     string                 `json:"owner_kind"`
	OwnerAppID    *string                `json:"owner_app_id"`
	CreatedAt     string                 `json:"created_at"`
	UpdatedAt     string                 `json:"updated_at"`
	// Probe is set only for probe checks; push checks omit the key.
	Probe *checkProbeView `json:"probe,omitempty"`
}

// checkProbeView is the probe block of a probe check's view. It never carries
// the sealed token, credentials, result detail, config version or next-probe
// time.
type checkProbeView struct {
	URL                 string                    `json:"url"`
	ExpectedStatuses    []int                     `json:"expected_statuses"`
	BodyMatch           *string                   `json:"body_match"`
	TimeoutSeconds      int                       `json:"timeout_seconds"`
	IntervalSeconds     int                       `json:"interval_seconds"`
	ConsecutiveFailures int                       `json:"consecutive_failures"`
	FailureThreshold    int                       `json:"failure_threshold"`
	HasAccessToken      bool                      `json:"has_access_token"`
	LastResult          *checkProbeLastResultView `json:"last_result"`
}

// checkProbeLastResultView is the last accepted probe result; a null reason is
// a success and a null status is a transport failure.
type checkProbeLastResultView struct {
	At     string  `json:"at"`
	Status *int    `json:"status"`
	Reason *string `json:"reason"`
}

// checkResponseBody wraps one check view.
type checkResponseBody struct {
	Check checkView `json:"check"`
}

// checksResponseBody wraps the check list; the slice is never nil.
type checksResponseBody struct {
	Checks []checkView `json:"checks"`
}

// deleteCheckResponseBody is the idempotent check delete result.
type deleteCheckResponseBody struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

// requireHeartbeats wraps a heartbeat handler so that it fails closed: with no
// Heartbeats configured the request is answered 503 before anything else is
// looked at.
func (server *Server) requireHeartbeats(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		// Check state is operational data: never cache it or let a browser sniff it.
		setSensitiveHeaders(responseWriter)
		if server.heartbeats == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgHeartbeatsUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handlePingCheck serves POST /v1/heartbeats/{app_id}/{name}. The body is
// ignored so a bare `curl -X POST` works.
func (server *Server) handlePingCheck(responseWriter http.ResponseWriter, request *http.Request) {
	check, err := server.heartbeats.Ping(request.Context(), request.PathValue(pathValueAppID), request.PathValue(pathValueName))
	server.writePingOutcome(responseWriter, request, check, err)
}

// handleFailCheck serves POST /v1/heartbeats/{app_id}/{name}/fail.
func (server *Server) handleFailCheck(responseWriter http.ResponseWriter, request *http.Request) {
	check, err := server.heartbeats.Fail(request.Context(), request.PathValue(pathValueAppID), request.PathValue(pathValueName))
	server.writePingOutcome(responseWriter, request, check, err)
}

// writePingOutcome answers a ping with the check's derived state, or maps err. A
// ping on a paused check is a normal 200: it is recorded and reports the state.
func (server *Server) writePingOutcome(
	responseWriter http.ResponseWriter, request *http.Request, check heartbeat.StoredCheck, err error,
) {
	if err != nil {
		server.writeHeartbeatError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, pingResponseBody{
		AppID: check.AppID,
		Name:  check.Name,
		State: heartbeat.DerivedState(check, server.now()),
	})
}

// handleListChecks serves GET /v1/admin/heartbeats.
func (server *Server) handleListChecks(responseWriter http.ResponseWriter, request *http.Request) {
	checks, err := server.heartbeats.List(request.Context())
	if err != nil {
		server.writeHeartbeatError(responseWriter, request, err)
		return
	}
	now := server.now()
	views := make([]checkView, 0, len(checks))
	for _, check := range checks {
		views = append(views, newCheckView(check, now))
	}
	writeJSON(responseWriter, http.StatusOK, checksResponseBody{Checks: views})
}

// handleCreateCheck serves POST /v1/admin/heartbeats (201).
func (server *Server) handleCreateCheck(responseWriter http.ResponseWriter, request *http.Request) {
	var body createCheckRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}

	check, err := server.heartbeats.Create(request.Context(), heartbeat.NewCheck{
		AppID:            body.AppID,
		Name:             body.Name,
		PeriodSeconds:    body.PeriodSeconds,
		GraceSeconds:     body.GraceSeconds,
		DestinationNames: body.Destinations,
		OwnerKind:        registry.OwnerOperator,
	})
	if err != nil {
		server.writeHeartbeatError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusCreated, checkResponseBody{Check: newCheckView(check, server.now())})
}

// handleGetCheck serves GET /v1/admin/heartbeats/{id}.
func (server *Server) handleGetCheck(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseCheckID(responseWriter, request)
	if !ok {
		return
	}

	check, err := server.heartbeats.Get(request.Context(), id)
	server.writeCheckOutcome(responseWriter, request, check, err)
}

// handleUpdateCheck serves PATCH /v1/admin/heartbeats/{id}. A field the body
// omits keeps the check's current value; the service validates the merged
// result. A body naming no mutable field changes nothing.
func (server *Server) handleUpdateCheck(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseCheckID(responseWriter, request)
	if !ok {
		return
	}

	var body patchCheckRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if len(body.AppID) > 0 || len(body.Name) > 0 {
		writeError(responseWriter, http.StatusBadRequest, errMsgCheckIdentityImmutable)
		return
	}

	// The merge of omitted fields happens inside the service, under the check's row
	// lock: merging here would read the check unlocked and let a concurrent PATCH
	// of other fields be reverted.
	patched, err := server.heartbeats.Patch(request.Context(), id, heartbeat.CheckPatch{
		PeriodSeconds:    body.PeriodSeconds,
		GraceSeconds:     body.GraceSeconds,
		DestinationNames: body.Destinations,
	})
	server.writeCheckOutcome(responseWriter, request, patched, err)
}

// handleDeleteCheck serves DELETE /v1/admin/heartbeats/{id}, idempotently: a
// check that is already gone answers 200 with deleted false.
func (server *Server) handleDeleteCheck(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseCheckID(responseWriter, request)
	if !ok {
		return
	}

	deleted, err := server.heartbeats.Delete(request.Context(), id)
	if err != nil {
		server.writeHeartbeatError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deleteCheckResponseBody{ID: id, Deleted: deleted})
}

// handlePauseCheck serves POST /v1/admin/heartbeats/{id}/pause, idempotently.
func (server *Server) handlePauseCheck(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseCheckID(responseWriter, request)
	if !ok {
		return
	}

	check, err := server.heartbeats.Pause(request.Context(), id)
	server.writeCheckOutcome(responseWriter, request, check, err)
}

// handleResumeCheck serves POST /v1/admin/heartbeats/{id}/resume, idempotently.
func (server *Server) handleResumeCheck(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseCheckID(responseWriter, request)
	if !ok {
		return
	}

	check, err := server.heartbeats.Resume(request.Context(), id)
	server.writeCheckOutcome(responseWriter, request, check, err)
}

// writeCheckOutcome answers with the 200 {"check":...} body, or maps err.
func (server *Server) writeCheckOutcome(
	responseWriter http.ResponseWriter, request *http.Request, check heartbeat.StoredCheck, err error,
) {
	if err != nil {
		server.writeHeartbeatError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, checkResponseBody{Check: newCheckView(check, server.now())})
}

// parseCheckID reads the {id} path value as a canonical positive integer (no
// sign, no leading zeros, so each check has exactly one URL). On failure it
// writes 400 "invalid check id" and returns false.
func parseCheckID(responseWriter http.ResponseWriter, request *http.Request) (int64, bool) {
	return parsePositiveID(responseWriter, request, errMsgInvalidCheckID)
}

// newCheckView converts a stored check into its JSON view as of now. Times are
// UTC, the state is the derived one, down_since is set only while the check is
// down, and the destinations slice is never nil.
func newCheckView(check heartbeat.StoredCheck, now time.Time) checkView {
	destinations := make([]checkDestinationView, 0, len(check.Destinations))
	for _, destination := range check.Destinations {
		destinations = append(destinations, checkDestinationView{
			Name:    destination.Name,
			Type:    destination.Type,
			Enabled: destination.Enabled,
		})
	}
	// A stale reason is never shown on a non-down check (fail closed).
	var downReason *string
	if check.State == heartbeat.StateDown {
		downReason = check.DownReason
	}
	return checkView{
		ID:            check.ID,
		AppID:         check.AppID,
		Name:          check.Name,
		PeriodSeconds: check.PeriodSeconds,
		GraceSeconds:  check.GraceSeconds,
		Paused:        check.Paused,
		State:         heartbeat.DerivedState(check, now),
		LastPingAt:    formatUTC(check.LastPingAt),
		LastPingKind:  check.LastPingKind,
		DownSince:     formatUTC(heartbeat.ActiveDownSince(check)),
		DownReason:    downReason,
		DeadlineAt:    heartbeat.Deadline(check).UTC().Format(time.RFC3339),
		Destinations:  destinations,
		OwnerKind:     check.OwnerKind,
		OwnerAppID:    check.OwnerAppID,
		CreatedAt:     check.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     check.UpdatedAt.UTC().Format(time.RFC3339),
		Probe:         newCheckProbeView(check.Probe),
	}
}

// newCheckProbeView converts probe state into its view, or nil for a push check.
func newCheckProbeView(probe *heartbeat.ProbeState) *checkProbeView {
	if probe == nil {
		return nil
	}
	var lastResult *checkProbeLastResultView
	if probe.LastResult != nil {
		lastResult = &checkProbeLastResultView{
			At:     probe.LastResult.At.UTC().Format(time.RFC3339),
			Status: probe.LastResult.Status,
			Reason: probe.LastResult.Reason,
		}
	}
	return &checkProbeView{
		URL:                 probe.Config.URL,
		ExpectedStatuses:    append([]int{}, probe.Config.ExpectedStatuses...),
		BodyMatch:           probe.Config.BodyMatch,
		TimeoutSeconds:      probe.Config.TimeoutSeconds,
		IntervalSeconds:     probe.Config.IntervalSeconds,
		ConsecutiveFailures: probe.ConsecutiveFailures,
		FailureThreshold:    heartbeat.ProbeFailureThreshold,
		HasAccessToken:      probe.HasAccessToken,
		LastResult:          lastResult,
	}
}

// formatUTC renders a timestamp as RFC3339 UTC, or nil for a nil time.
func formatUTC(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

// writeHeartbeatError is the single chokepoint mapping a heartbeat-service
// error to an HTTP response. Validation errors carry a client-safe message;
// known sentinels map to fixed messages; anything else is logged (path only,
// never the request body) and answered with a generic 500 so no internal detail
// leaks.
func (server *Server) writeHeartbeatError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	var validationErr *heartbeat.ValidationError
	switch {
	case errors.As(err, &validationErr):
		writeError(responseWriter, http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, heartbeat.ErrCheckNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgCheckNotFound)
	case errors.Is(err, heartbeat.ErrCheckExists):
		writeError(responseWriter, http.StatusConflict, errMsgCheckExists)
	case errors.Is(err, heartbeat.ErrProbeCheckPing):
		writeError(responseWriter, http.StatusConflict, errMsgProbeCheckPing)
	case errors.Is(err, registry.ErrUnknownDestination):
		writeError(responseWriter, http.StatusBadRequest, errMsgUnknownDestination)
	case errors.Is(err, registry.ErrCheckViolation):
		// A CHECK the database enforces (an app-owned check's app_id must stay its owner's id) is a
		// request the client can correct, not a server fault. The error text names the operation and the
		// constraint (never row data), so a Go validator that drifts from a CHECK is not a silent 400.
		server.logger.Warn("heartbeat request violated a data constraint", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusBadRequest, errMsgConstraintViolation)
	case errors.Is(err, context.Canceled):
		// The client went away: not a server fault, and a client that can
		// disconnect at will must not be able to flood the error log. The response
		// is moot, but stays the generic 500.
		server.logger.Debug("heartbeat request canceled", "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	case errors.Is(err, context.DeadlineExceeded):
		// A deadline is ambiguous (a client-supplied one or a server/driver one such
		// as a saturated pool), so it is logged where an operator can see it. The
		// body stays the generic 500.
		server.logger.Warn("heartbeat request deadline exceeded", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	default:
		server.logger.Error("heartbeat service error", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	}
}

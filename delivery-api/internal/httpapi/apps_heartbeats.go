package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
)

// errMsgCheckAppIDNotOwn is the client-safe message for an app-scoped check whose body app_id is anything
// but the calling app's own id.
const errMsgCheckAppIDNotOwn = "app_id must be the app's own id"

// AppHeartbeats is the app-scoped heartbeat check surface the /v1/apps/{app_id}/heartbeats endpoints call.
// *heartbeat.Service satisfies it. Every method takes the caller's app id (from its app key) and confines
// the call to checks that app owns: another owner's check reads as not found.
type AppHeartbeats interface {
	CreateForApp(ctx context.Context, appID string, input heartbeat.AppCheckInput) (heartbeat.StoredCheck, error)
	ListForApp(ctx context.Context, appID string) ([]heartbeat.StoredCheck, error)
	GetForApp(ctx context.Context, appID string, id int64) (heartbeat.StoredCheck, error)
	PatchForApp(ctx context.Context, appID string, id int64, patch heartbeat.CheckPatch) (heartbeat.StoredCheck, error)
	DeleteForApp(ctx context.Context, appID string, id int64) error
	PauseForApp(ctx context.Context, appID string, id int64) (heartbeat.StoredCheck, error)
	ResumeForApp(ctx context.Context, appID string, id int64) (heartbeat.StoredCheck, error)
}

// appHeartbeatScoped wraps an app-scoped heartbeat handler: the response is marked non-cacheable, the request
// answers 503 when no AppHeartbeats is configured, and the app key must resolve to the {app_id} in the path
// (a different asserted app is a 403) before the handler runs.
func (server *Server) appHeartbeatScoped(handler http.HandlerFunc) http.HandlerFunc {
	return server.requireAppHeartbeats(server.requireAppKey(assertedAppIDFromPath, handler))
}

// requireAppHeartbeats is requireHeartbeats for the app-scoped check routes: with no AppHeartbeats
// configured the request is answered 503 before anything else is looked at (fail closed).
func (server *Server) requireAppHeartbeats(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.appHeartbeats == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgHeartbeatsUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handleAppCreateCheck serves POST /v1/apps/{app_id}/heartbeats (201). The check is owned by the calling app
// and registered under its id. A body app_id may be omitted, empty or the app's own; anything else is a 400.
func (server *Server) handleAppCreateCheck(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body createCheckRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	// Fail closed: any probe key, including null or a malformed shape, is refused
	// before it is decoded or reaches the service.
	if len(body.Probe) > 0 {
		writeError(responseWriter, http.StatusBadRequest, heartbeat.MessageProbeOnAppOwned)
		return
	}
	periodSeconds, periodOK := parseScheduleField(body.PeriodSeconds)
	graceSeconds, graceOK := parseScheduleField(body.GraceSeconds)
	if !periodOK || !graceOK {
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidJSONBody)
		return
	}
	if body.AppID != "" && body.AppID != appID {
		writeError(responseWriter, http.StatusBadRequest, errMsgCheckAppIDNotOwn)
		return
	}

	check, err := server.appHeartbeats.CreateForApp(request.Context(), appID, heartbeat.AppCheckInput{
		Name:             body.Name,
		PeriodSeconds:    periodSeconds,
		GraceSeconds:     graceSeconds,
		DestinationNames: body.Destinations,
	})
	if err != nil {
		server.writeHeartbeatError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusCreated, checkResponseBody{Check: newCheckView(check, server.now())})
}

// handleAppListChecks serves GET /v1/apps/{app_id}/heartbeats: only the calling app's own.
func (server *Server) handleAppListChecks(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}

	checks, err := server.appHeartbeats.ListForApp(request.Context(), appID)
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

// handleAppGetCheck serves GET /v1/apps/{app_id}/heartbeats/{id}.
func (server *Server) handleAppGetCheck(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndCheckID(responseWriter, request)
	if !ok {
		return
	}

	check, err := server.appHeartbeats.GetForApp(request.Context(), appID, id)
	server.writeCheckOutcome(responseWriter, request, check, err)
}

// handleAppUpdateCheck serves PATCH /v1/apps/{app_id}/heartbeats/{id}. A field the body omits keeps the
// check's current value. The name is immutable, and so is the app_id: the body may repeat the app's own id
// (or leave it empty or null) but never name another.
func (server *Server) handleAppUpdateCheck(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndCheckID(responseWriter, request)
	if !ok {
		return
	}

	var body patchCheckRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if len(body.Name) > 0 {
		writeError(responseWriter, http.StatusBadRequest, errMsgCheckIdentityImmutable)
		return
	}
	if !assertedCheckAppIDIsOwn(body.AppID, appID) {
		writeError(responseWriter, http.StatusBadRequest, errMsgCheckAppIDNotOwn)
		return
	}
	// Fail closed: any probe key, including null or a malformed shape, is refused
	// before it is decoded or reaches the service.
	if len(body.Probe) > 0 {
		writeError(responseWriter, http.StatusBadRequest, heartbeat.MessageProbeOnAppOwned)
		return
	}

	patched, err := server.appHeartbeats.PatchForApp(request.Context(), appID, id, heartbeat.CheckPatch{
		PeriodSeconds:    body.PeriodSeconds,
		GraceSeconds:     body.GraceSeconds,
		DestinationNames: body.Destinations,
	})
	server.writeCheckOutcome(responseWriter, request, patched, err)
}

// handleAppDeleteCheck serves DELETE /v1/apps/{app_id}/heartbeats/{id}. Unlike the operator's idempotent
// delete, a check that is absent or not the app's own is 404, so the answer never tells another owner's
// check from an absent one.
func (server *Server) handleAppDeleteCheck(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndCheckID(responseWriter, request)
	if !ok {
		return
	}

	if err := server.appHeartbeats.DeleteForApp(request.Context(), appID, id); err != nil {
		server.writeHeartbeatError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deleteCheckResponseBody{ID: id, Deleted: true})
}

// handleAppPauseCheck serves POST /v1/apps/{app_id}/heartbeats/{id}/pause, idempotently.
func (server *Server) handleAppPauseCheck(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndCheckID(responseWriter, request)
	if !ok {
		return
	}

	check, err := server.appHeartbeats.PauseForApp(request.Context(), appID, id)
	server.writeCheckOutcome(responseWriter, request, check, err)
}

// handleAppResumeCheck serves POST /v1/apps/{app_id}/heartbeats/{id}/resume, idempotently.
func (server *Server) handleAppResumeCheck(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndCheckID(responseWriter, request)
	if !ok {
		return
	}

	check, err := server.appHeartbeats.ResumeForApp(request.Context(), appID, id)
	server.writeCheckOutcome(responseWriter, request, check, err)
}

// appAndCheckID returns the authenticated app id and the {id} path value as a canonical positive check id.
// On failure it writes the response and returns false.
func (server *Server) appAndCheckID(
	responseWriter http.ResponseWriter, request *http.Request,
) (string, int64, bool) {
	appID, authenticated := server.authenticatedAppID(responseWriter, request)
	if !authenticated {
		return "", 0, false
	}
	id, validID := parseCheckID(responseWriter, request)
	if !validID {
		return "", 0, false
	}
	return appID, id, true
}

// assertedCheckAppIDIsOwn reports whether the app_id a PATCH body carries (raw, absent when empty) is
// acceptable: absent, JSON null, an empty string or the calling app's own id. Any other value, including a
// non-string, is not.
func assertedCheckAppIDIsOwn(raw json.RawMessage, appID string) bool {
	if len(raw) == 0 {
		return true
	}
	var asserted string
	if err := json.Unmarshal(raw, &asserted); err != nil {
		return false
	}
	return asserted == "" || asserted == appID
}

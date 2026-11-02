package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appteardown"
)

// Client-safe messages of the deprovision-app cascade.
const (
	errMsgTeardownBusy        = "publisher mint already in progress, retry"
	errMsgTeardownUpstream    = "provisioning-api teardown failed"
	errMsgTeardownUnavailable = "app teardown unavailable"
	errMsgTeardownConfirm     = "body app_id must repeat the path app_id"
)

// adminDeprovisionAppRequestBody is the confirmation body of the irreversible cascade.
type adminDeprovisionAppRequestBody struct {
	AppID string `json:"app_id"`
}

// AppTeardown removes an app and everything it owns. *appteardown.Service satisfies it.
type AppTeardown interface {
	Deprovision(ctx context.Context, appID string) (appteardown.Result, error)
}

// requireAppTeardown answers 503 when no AppTeardown is configured (fail closed), and marks every
// response non-cacheable.
func (server *Server) requireAppTeardown(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.appTeardown == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgProvisioningUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handleAdminDeprovisionApp serves POST /v1/admin/apps/{app_id}/deprovision. The body must repeat the
// path app_id (a confirmation, since the cascade is irreversible); a missing or different one is 400. It is
// deliberately not binding-guarded: an unbound app must remain removable. The path app id is validated
// before the body is read and before any upstream call. Errors map to fixed client-safe messages; the cause is logged by the service.
func (server *Server) handleAdminDeprovisionApp(responseWriter http.ResponseWriter, request *http.Request) {
	appID := request.PathValue("app_id")
	if !validAdminAppID(responseWriter, appID) {
		return
	}
	var body adminDeprovisionAppRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if body.AppID != appID {
		writeError(responseWriter, http.StatusBadRequest, errMsgTeardownConfirm)
		return
	}
	result, teardownErr := server.appTeardown.Deprovision(request.Context(), appID)
	if teardownErr != nil {
		server.logger.Error("app teardown failed", "app_id", appID, "error", teardownErr)
	}
	switch {
	case teardownErr == nil:
		writeJSON(responseWriter, http.StatusOK, result)
	case errors.Is(teardownErr, appteardown.ErrInvalidAppID):
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidAppID)
	case errors.Is(teardownErr, appteardown.ErrBusy):
		writeError(responseWriter, http.StatusConflict, errMsgTeardownBusy)
	case errors.Is(teardownErr, appteardown.ErrUpstream):
		writeError(responseWriter, http.StatusBadGateway, errMsgTeardownUpstream)
	default:
		// ErrUnavailable and anything unclassified fail closed as 503.
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgTeardownUnavailable)
	}
}

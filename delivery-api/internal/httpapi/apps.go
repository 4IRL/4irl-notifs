package httpapi

import (
	"net/http"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// appScoped wraps an app-scoped handler: the response is marked non-cacheable (it carries masked secrets),
// the request answers 503 when no AppRegistry is configured, and the app key must resolve to the {app_id}
// in the path (a different asserted app is a 403) before the handler runs.
func (server *Server) appScoped(handler http.HandlerFunc) http.HandlerFunc {
	return server.requireAppRegistry(server.requireAppKey(assertedAppIDFromPath, handler))
}

// requireAppRegistry is requireRegistry for the app-scoped routes: with no AppRegistry configured the
// request is answered 503 before anything else is looked at (fail closed).
func (server *Server) requireAppRegistry(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		setSensitiveHeaders(responseWriter)
		if server.appRegistry == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgRegistryUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// authenticatedAppID returns the app id requireAppKey resolved from the app key. An empty id means the
// handler was reached without the key check, which must never happen: it answers 503 (fail closed) and
// reports false.
func (server *Server) authenticatedAppID(responseWriter http.ResponseWriter, request *http.Request) (string, bool) {
	appID := callerAppID(request)
	if appID == "" {
		server.logger.Error("app-scoped handler reached without an authenticated app", "path", request.URL.Path)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgIdentityUnavailable)
		return "", false
	}
	return appID, true
}

// handleAppCreateDestination serves POST /v1/apps/{app_id}/destinations (201). The destination is owned by
// the calling app; the body carries no owner.
func (server *Server) handleAppCreateDestination(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body createDestinationRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}

	view, err := server.appRegistry.CreateAppDestination(request.Context(), appID, registry.DestinationInput{
		Name:    body.Name,
		Type:    body.Type,
		Enabled: body.Enabled,
		Config:  body.Config,
		Secrets: body.Secrets,
	})
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusCreated, destinationResponseBody{Destination: view})
}

// handleAppListDestinations serves GET /v1/apps/{app_id}/destinations: only the calling app's own.
func (server *Server) handleAppListDestinations(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}

	views, err := server.appRegistry.ListAppDestinations(request.Context(), appID)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	if views == nil {
		views = []registry.DestinationView{}
	}
	writeJSON(responseWriter, http.StatusOK, destinationsResponseBody{Destinations: views})
}

// handleAppGetDestination serves GET /v1/apps/{app_id}/destinations/{name}.
func (server *Server) handleAppGetDestination(responseWriter http.ResponseWriter, request *http.Request) {
	appID, name, ok := server.appAndDestinationName(responseWriter, request)
	if !ok {
		return
	}

	view, err := server.appRegistry.GetAppDestination(request.Context(), appID, name)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, destinationResponseBody{Destination: view})
}

// handleAppUpdateDestination serves PATCH /v1/apps/{app_id}/destinations/{name}.
func (server *Server) handleAppUpdateDestination(responseWriter http.ResponseWriter, request *http.Request) {
	appID, name, ok := server.appAndDestinationName(responseWriter, request)
	if !ok {
		return
	}

	var body patchDestinationRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}

	view, err := server.appRegistry.UpdateAppDestination(request.Context(), appID, name, registry.DestinationPatch{
		Enabled: body.Enabled,
		Config:  body.Config,
		Secrets: body.Secrets,
	})
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, destinationResponseBody{Destination: view})
}

// handleAppDeleteDestination serves DELETE /v1/apps/{app_id}/destinations/{name}. Unlike the operator's
// idempotent delete, a destination that is absent or not the app's own is 404, so the answer never tells
// another owner's destination from an absent one.
func (server *Server) handleAppDeleteDestination(responseWriter http.ResponseWriter, request *http.Request) {
	appID, name, ok := server.appAndDestinationName(responseWriter, request)
	if !ok {
		return
	}

	if err := server.appRegistry.DeleteAppDestination(request.Context(), appID, name); err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deleteDestinationResponseBody{Name: name, Deleted: true})
}

// handleAppRotateSecret serves POST /v1/apps/{app_id}/destinations/{name}/rotate-secret. The new plaintext
// secret appears only in this response body.
func (server *Server) handleAppRotateSecret(responseWriter http.ResponseWriter, request *http.Request) {
	appID, name, ok := server.appAndDestinationName(responseWriter, request)
	if !ok {
		return
	}

	var body rotateSecretRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}
	if body.OverlapHours == nil || *body.OverlapHours < minOverlapHours || *body.OverlapHours > maxOverlapHours {
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidOverlapHours)
		return
	}

	view, signingSecret, err := server.appRegistry.RotateAppWebhookSecret(request.Context(), appID, name, *body.OverlapHours)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, rotateSecretResponseBody{Destination: view, SigningSecret: signingSecret})
}

// appAndDestinationName returns the authenticated app id and the {name} path value, checked against the
// destination slug rule. On failure it writes the response and returns false.
func (server *Server) appAndDestinationName(
	responseWriter http.ResponseWriter, request *http.Request,
) (string, string, bool) {
	appID, authenticated := server.authenticatedAppID(responseWriter, request)
	if !authenticated {
		return "", "", false
	}
	name, validName := server.pathDestinationName(responseWriter, request)
	if !validName {
		return "", "", false
	}
	return appID, name, true
}

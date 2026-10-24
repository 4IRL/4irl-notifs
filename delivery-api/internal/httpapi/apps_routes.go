package httpapi

import (
	"net/http"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The app-scoped route handlers serve /v1/apps/{app_id}/routes behind appScoped. The request bodies are the
// admin ones (createRouteRequestBody, patchRouteRequestBody): they carry no owner field, so an owner in a
// body is an unknown-field 400, and the route's owner and app_pattern come from the app key, never the body.

// handleAppCreateRoute serves POST /v1/apps/{app_id}/routes (201). The route is owned by the calling app; an
// omitted app_pattern defaults to the app's id.
func (server *Server) handleAppCreateRoute(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}
	var body createRouteRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}

	view, err := server.appRegistry.CreateAppRoute(request.Context(), appID, registry.RouteInput{
		AppPattern:     body.AppPattern,
		ChannelPattern: body.ChannelPattern,
		Destinations:   body.Destinations,
		Enabled:        body.Enabled,
	})
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusCreated, routeResponseBody{Route: view})
}

// handleAppListRoutes serves GET /v1/apps/{app_id}/routes: only the calling app's own.
func (server *Server) handleAppListRoutes(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := server.authenticatedAppID(responseWriter, request)
	if !ok {
		return
	}

	views, err := server.appRegistry.ListAppRoutes(request.Context(), appID)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	if views == nil {
		views = []registry.RouteView{}
	}
	writeJSON(responseWriter, http.StatusOK, routesResponseBody{Routes: views})
}

// handleAppGetRoute serves GET /v1/apps/{app_id}/routes/{id}.
func (server *Server) handleAppGetRoute(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndRouteID(responseWriter, request)
	if !ok {
		return
	}

	view, err := server.appRegistry.GetAppRoute(request.Context(), appID, id)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, routeResponseBody{Route: view})
}

// handleAppUpdateRoute serves PATCH /v1/apps/{app_id}/routes/{id}. A patch app_pattern other than the app's
// own id is a 400.
func (server *Server) handleAppUpdateRoute(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndRouteID(responseWriter, request)
	if !ok {
		return
	}

	var body patchRouteRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
		return
	}

	view, err := server.appRegistry.UpdateAppRoute(request.Context(), appID, id, registry.RoutePatch{
		AppPattern:     body.AppPattern,
		ChannelPattern: body.ChannelPattern,
		Destinations:   body.Destinations,
		Enabled:        body.Enabled,
	})
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, routeResponseBody{Route: view})
}

// handleAppDeleteRoute serves DELETE /v1/apps/{app_id}/routes/{id}. Unlike the operator's idempotent
// delete, a route that is absent or not the app's own is 404, so the answer never tells another owner's
// route from an absent one.
func (server *Server) handleAppDeleteRoute(responseWriter http.ResponseWriter, request *http.Request) {
	appID, id, ok := server.appAndRouteID(responseWriter, request)
	if !ok {
		return
	}

	if err := server.appRegistry.DeleteAppRoute(request.Context(), appID, id); err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deleteRouteResponseBody{ID: id, Deleted: true})
}

// appAndRouteID returns the authenticated app id and the {id} path value as a canonical positive route id.
// On failure it writes the response and returns false.
func (server *Server) appAndRouteID(
	responseWriter http.ResponseWriter, request *http.Request,
) (string, int64, bool) {
	appID, authenticated := server.authenticatedAppID(responseWriter, request)
	if !authenticated {
		return "", 0, false
	}
	id, validID := parseRouteID(responseWriter, request)
	if !validID {
		return "", 0, false
	}
	return appID, id, true
}

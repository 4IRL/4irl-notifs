package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// maxAdminBodyBytes caps an admin request body. The admin endpoints carry
// destination secrets and are operator-only, so 64 KiB is far above any
// legitimate payload.
const maxAdminBodyBytes = 64 << 10

// Error messages returned in the {"error": "..."} body of admin responses.
const (
	errMsgInvalidJSONBody     = "invalid JSON body"
	errMsgBodyTooLarge        = "request body too large"
	errMsgInvalidRouteID      = "invalid route id"
	errMsgRegistryUnavailable = "registry unavailable"
	errMsgDestinationNotFound = "destination does not exist"
	errMsgRouteNotFound       = "route does not exist"
	errMsgDestinationExists   = "destination already exists"
	errMsgRouteExists         = "route already exists"
	errMsgDestinationInUse    = "destination is used by a route"
	errMsgUnknownDestination  = "unknown destination"
	errMsgInternalError       = "internal error"
)

// errorResponseBody is the JSON body of every admin error response.
type errorResponseBody struct {
	Error string `json:"error"`
}

// destinationResponseBody wraps one destination view.
type destinationResponseBody struct {
	Destination registry.DestinationView `json:"destination"`
}

// destinationsResponseBody wraps the destination list; the slice is never nil.
type destinationsResponseBody struct {
	Destinations []registry.DestinationView `json:"destinations"`
}

// routeResponseBody wraps one route view.
type routeResponseBody struct {
	Route registry.RouteView `json:"route"`
}

// routesResponseBody wraps the route list; the slice is never nil.
type routesResponseBody struct {
	Routes []registry.RouteView `json:"routes"`
}

// deleteDestinationResponseBody is the idempotent destination delete result.
type deleteDestinationResponseBody struct {
	Name    string `json:"name"`
	Deleted bool   `json:"deleted"`
}

// deleteRouteResponseBody is the idempotent route delete result.
type deleteRouteResponseBody struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

// createDestinationRequestBody is the POST /v1/admin/destinations body.
type createDestinationRequestBody struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled *bool             `json:"enabled"`
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

// patchDestinationRequestBody is the PATCH /v1/admin/destinations/{name} body.
// The name and type are immutable, so they are deliberately absent: sending
// them is an unknown-field 400.
type patchDestinationRequestBody struct {
	Enabled *bool             `json:"enabled"`
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

// createRouteRequestBody is the POST /v1/admin/routes body.
type createRouteRequestBody struct {
	AppPattern     string   `json:"app_pattern"`
	ChannelPattern string   `json:"channel_pattern"`
	Destinations   []string `json:"destinations"`
	Enabled        *bool    `json:"enabled"`
}

// patchRouteRequestBody is the PATCH /v1/admin/routes/{id} body.
type patchRouteRequestBody struct {
	AppPattern     *string  `json:"app_pattern"`
	ChannelPattern *string  `json:"channel_pattern"`
	Destinations   []string `json:"destinations"`
	Enabled        *bool    `json:"enabled"`
}

// requireRegistry wraps an admin handler so that it fails closed: with no
// Registry configured the request is answered 503 before anything else is
// looked at.
func (server *Server) requireRegistry(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		// Responses carry masked secrets: never cache them or let a browser sniff them.
		responseWriter.Header().Set("Cache-Control", "no-store")
		responseWriter.Header().Set("X-Content-Type-Options", "nosniff")
		if server.registry == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgRegistryUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handleCreateDestination serves POST /v1/admin/destinations. Validation order:
// body, then the registry (which validates every field). 201 on success.
func (server *Server) handleCreateDestination(responseWriter http.ResponseWriter, request *http.Request) {
	var body createDestinationRequestBody
	if !decodeJSON(responseWriter, request, &body) {
		return
	}

	view, err := server.registry.CreateDestination(request.Context(), registry.DestinationInput{
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

// handleListDestinations serves GET /v1/admin/destinations. Validation order:
// the registry only (no path parameter or body).
func (server *Server) handleListDestinations(responseWriter http.ResponseWriter, request *http.Request) {
	views, err := server.registry.ListDestinations(request.Context())
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	if views == nil {
		views = []registry.DestinationView{}
	}
	writeJSON(responseWriter, http.StatusOK, destinationsResponseBody{Destinations: views})
}

// handleGetDestination serves GET /v1/admin/destinations/{name}. Validation
// order: path name (400 "invalid name"), then the registry.
func (server *Server) handleGetDestination(responseWriter http.ResponseWriter, request *http.Request) {
	name, ok := server.pathDestinationName(responseWriter, request)
	if !ok {
		return
	}

	view, err := server.registry.GetDestination(request.Context(), name)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, destinationResponseBody{Destination: view})
}

// handleUpdateDestination serves PATCH /v1/admin/destinations/{name}. Validation
// order: path param, then body (unknown fields such as type are rejected), then
// the registry.
func (server *Server) handleUpdateDestination(responseWriter http.ResponseWriter, request *http.Request) {
	name, ok := server.pathDestinationName(responseWriter, request)
	if !ok {
		return
	}

	var body patchDestinationRequestBody
	if !decodeJSON(responseWriter, request, &body) {
		return
	}

	view, err := server.registry.UpdateDestination(request.Context(), name, registry.DestinationPatch{
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

// handleDeleteDestination serves DELETE /v1/admin/destinations/{name}. The
// delete is idempotent: it answers 200 deleted:true whether or not the row
// existed. A destination a route still references is a 409 from the registry.
func (server *Server) handleDeleteDestination(responseWriter http.ResponseWriter, request *http.Request) {
	name, ok := server.pathDestinationName(responseWriter, request)
	if !ok {
		return
	}

	if _, err := server.registry.DeleteDestination(request.Context(), name); err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deleteDestinationResponseBody{Name: name, Deleted: true})
}

// handleCreateRoute serves POST /v1/admin/routes. Validation order: body, then
// the registry. 201 on success.
func (server *Server) handleCreateRoute(responseWriter http.ResponseWriter, request *http.Request) {
	var body createRouteRequestBody
	if !decodeJSON(responseWriter, request, &body) {
		return
	}

	view, err := server.registry.CreateRoute(request.Context(), registry.RouteInput{
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

// handleListRoutes serves GET /v1/admin/routes. Validation order: the registry
// only (no path parameter or body).
func (server *Server) handleListRoutes(responseWriter http.ResponseWriter, request *http.Request) {
	views, err := server.registry.ListRoutes(request.Context())
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	if views == nil {
		views = []registry.RouteView{}
	}
	writeJSON(responseWriter, http.StatusOK, routesResponseBody{Routes: views})
}

// handleGetRoute serves GET /v1/admin/routes/{id}. Validation order: path id,
// then the registry.
func (server *Server) handleGetRoute(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseRouteID(responseWriter, request)
	if !ok {
		return
	}

	view, err := server.registry.GetRoute(request.Context(), id)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, routeResponseBody{Route: view})
}

// handleUpdateRoute serves PATCH /v1/admin/routes/{id}. Validation order: path
// id, then body, then the registry.
func (server *Server) handleUpdateRoute(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseRouteID(responseWriter, request)
	if !ok {
		return
	}

	var body patchRouteRequestBody
	if !decodeJSON(responseWriter, request, &body) {
		return
	}

	view, err := server.registry.UpdateRoute(request.Context(), id, registry.RoutePatch{
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

// handleDeleteRoute serves DELETE /v1/admin/routes/{id}. Validation order: path
// id, then the registry. The delete is idempotent: 200 deleted:true whether or
// not the row existed.
func (server *Server) handleDeleteRoute(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseRouteID(responseWriter, request)
	if !ok {
		return
	}

	if _, err := server.registry.DeleteRoute(request.Context(), id); err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deleteRouteResponseBody{ID: id, Deleted: true})
}

// pathDestinationName reads the {name} path value and checks it against the
// destination slug rule, so a malformed name (NUL bytes, invalid UTF-8) is a
// client 400 and never reaches Postgres. On failure it writes the response and
// returns false.
func (server *Server) pathDestinationName(responseWriter http.ResponseWriter, request *http.Request) (string, bool) {
	name := request.PathValue("name")
	if err := registry.ValidateDestinationName(name); err != nil {
		server.writeServiceError(responseWriter, request, err)
		return "", false
	}
	return name, true
}

// parseRouteID reads the {id} path value as a canonical positive integer
// (no sign, no leading zeros, so each route has exactly one URL). On failure it
// writes 400 "invalid route id" and returns false.
func parseRouteID(responseWriter http.ResponseWriter, request *http.Request) (int64, bool) {
	rawID := request.PathValue("id")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != rawID {
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidRouteID)
		return 0, false
	}
	return id, true
}

// decodeJSON reads exactly one JSON value from the request body into dst. The
// body is capped at maxAdminBodyBytes, unknown fields are rejected, and any
// data after the first value is rejected. On failure it writes 413 for an
// oversized body or 400 "invalid JSON body" (never echoing the body or the
// decoder error, which can quote secret values) and returns false.
func decodeJSON(responseWriter http.ResponseWriter, request *http.Request, dst any) bool {
	limitedBody := http.MaxBytesReader(responseWriter, request.Body, maxAdminBodyBytes)
	decoder := json.NewDecoder(limitedBody)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		var tooLargeErr *http.MaxBytesError
		if errors.As(err, &tooLargeErr) {
			writeError(responseWriter, http.StatusRequestEntityTooLarge, errMsgBodyTooLarge)
			return false
		}
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidJSONBody)
		return false
	}
	// A second Decode must hit a clean EOF; anything else is trailing data.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidJSONBody)
		return false
	}
	return true
}

// writeError writes the single-key {"error": message} body with statusCode.
func writeError(responseWriter http.ResponseWriter, statusCode int, message string) {
	writeJSON(responseWriter, statusCode, errorResponseBody{Error: message})
}

// writeServiceError is the single chokepoint mapping a registry error to an
// HTTP response. Validation errors carry a client-safe message; known sentinels
// map to fixed messages; anything else is logged (path only, never the request
// body) and answered with a generic 500 so no internal detail leaks.
func (server *Server) writeServiceError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	var validationErr *registry.ValidationError
	switch {
	case errors.As(err, &validationErr):
		writeError(responseWriter, http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, registry.ErrDestinationNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgDestinationNotFound)
	case errors.Is(err, registry.ErrRouteNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgRouteNotFound)
	case errors.Is(err, registry.ErrDestinationExists):
		writeError(responseWriter, http.StatusConflict, errMsgDestinationExists)
	case errors.Is(err, registry.ErrRouteExists):
		writeError(responseWriter, http.StatusConflict, errMsgRouteExists)
	case errors.Is(err, registry.ErrDestinationInUse):
		writeError(responseWriter, http.StatusConflict, errMsgDestinationInUse)
	case errors.Is(err, registry.ErrUnknownDestination):
		writeError(responseWriter, http.StatusBadRequest, errMsgUnknownDestination)
	default:
		server.logger.Error("registry service error", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	}
}

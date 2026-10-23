package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// maxAdminBodyBytes caps an admin request body. The admin endpoints carry
// destination secrets and are operator-only, so 64 KiB is far above any
// legitimate payload.
const maxAdminBodyBytes = 64 << 10

// bodyReadTimeout bounds how long a client may take to send a request body.
const bodyReadTimeout = 30 * time.Second

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
	errMsgDestinationInUse    = "destination is in use"
	errMsgUnknownDestination  = "unknown destination"
	errMsgInternalError       = "internal error"
	errMsgInvalidOverlapHours = "invalid overlap_hours"
	errMsgRotationUnsupported = "destination type does not support rotation"
	errMsgConstraintViolation = "request violates a data constraint"
	errMsgURLCheckUnavailable = "destination url check unavailable"
)

// Bounds of the rotate-secret overlap window, in hours.
const (
	minOverlapHours = 1
	maxOverlapHours = 168
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

// rotateSecretRequestBody is the POST /v1/admin/destinations/{name}/rotate-secret
// body. OverlapHours is a pointer so a missing or null value is distinguishable
// from 0.
type rotateSecretRequestBody struct {
	OverlapHours *int `json:"overlap_hours"`
}

// rotateSecretResponseBody carries the rotated destination and the one-time
// plaintext of its new signing secret.
type rotateSecretResponseBody struct {
	Destination   registry.DestinationView `json:"destination"`
	SigningSecret string                   `json:"signing_secret"`
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
		setSensitiveHeaders(responseWriter)
		if server.registry == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgRegistryUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handleCreateDestination serves POST /v1/admin/destinations (201).
func (server *Server) handleCreateDestination(responseWriter http.ResponseWriter, request *http.Request) {
	var body createDestinationRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
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

// handleListDestinations serves GET /v1/admin/destinations.
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

// handleGetDestination serves GET /v1/admin/destinations/{name}.
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

// handleUpdateDestination serves PATCH /v1/admin/destinations/{name}.
func (server *Server) handleUpdateDestination(responseWriter http.ResponseWriter, request *http.Request) {
	name, ok := server.pathDestinationName(responseWriter, request)
	if !ok {
		return
	}

	var body patchDestinationRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
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

// handleDeleteDestination serves DELETE /v1/admin/destinations/{name}, idempotently.
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

// handleRotateSecret serves POST /v1/admin/destinations/{name}/rotate-secret.
// The new plaintext secret appears only in this response body.
func (server *Server) handleRotateSecret(responseWriter http.ResponseWriter, request *http.Request) {
	name, ok := server.pathDestinationName(responseWriter, request)
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

	view, signingSecret, err := server.registry.RotateWebhookSecret(request.Context(), name, *body.OverlapHours)
	if err != nil {
		server.writeServiceError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, rotateSecretResponseBody{Destination: view, SigningSecret: signingSecret})
}

// handleCreateRoute serves POST /v1/admin/routes (201).
func (server *Server) handleCreateRoute(responseWriter http.ResponseWriter, request *http.Request) {
	var body createRouteRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
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

// handleListRoutes serves GET /v1/admin/routes.
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

// handleGetRoute serves GET /v1/admin/routes/{id}.
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

// handleUpdateRoute serves PATCH /v1/admin/routes/{id}.
func (server *Server) handleUpdateRoute(responseWriter http.ResponseWriter, request *http.Request) {
	id, ok := parseRouteID(responseWriter, request)
	if !ok {
		return
	}

	var body patchRouteRequestBody
	if !decodeJSON(responseWriter, request, &body, maxAdminBodyBytes) {
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

// handleDeleteRoute serves DELETE /v1/admin/routes/{id}, idempotently.
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
	return parsePositiveID(responseWriter, request, errMsgInvalidRouteID)
}

// parsePositiveID reads the {id} path value as a canonical positive integer (no
// sign, no leading zeros, so each resource has exactly one URL). On failure it
// writes 400 with errorMessage and returns false.
func parsePositiveID(responseWriter http.ResponseWriter, request *http.Request, errorMessage string) (int64, bool) {
	rawID := request.PathValue(pathValueID)
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != rawID {
		writeError(responseWriter, http.StatusBadRequest, errorMessage)
		return 0, false
	}
	return id, true
}

// boundBodyRead gives the client bodyReadTimeout to send the request body and
// returns the func that clears the deadline again. Use it as
// `defer boundBodyRead(responseWriter)()` around every body read.
func boundBodyRead(responseWriter http.ResponseWriter) func() {
	// Best effort: ResponseController reports an error where the writer cannot
	// set a deadline (for example an httptest recorder), which is safe to ignore.
	responseController := http.NewResponseController(responseWriter)
	_ = responseController.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	return func() { _ = responseController.SetReadDeadline(time.Time{}) }
}

// decodeJSON reads exactly one JSON value from the request body into dst. The
// body is capped at maxBytes, unknown fields are rejected, and any data after
// the first value is rejected. On failure it writes 413 for an oversized body
// or 400 "invalid JSON body" (never echoing the body or the decoder error,
// which can quote secret values) and returns false. The client has
// bodyReadTimeout to send the body: the server has no global read timeout, so
// without a per-request deadline a slow client could hold a connection open
// indefinitely. The deadline is cleared again once the decode finishes, so it
// never cuts short the work that follows (for example a message wait).
func decodeJSON(responseWriter http.ResponseWriter, request *http.Request, dst any, maxBytes int64) bool {
	defer boundBodyRead(responseWriter)()

	limitedBody := http.MaxBytesReader(responseWriter, request.Body, maxBytes)
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
	case errors.Is(err, registry.ErrRotationUnsupported):
		writeError(responseWriter, http.StatusUnprocessableEntity, errMsgRotationUnsupported)
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
	case errors.Is(err, registry.ErrCheckViolation):
		// A CHECK the database enforces (for example an app-owned route's app_pattern must stay its
		// owner's id) is a request the client can correct, not a server fault. The error text names the
		// operation and the constraint (never row data), so it is logged: a Go validator that drifts from a
		// database CHECK would otherwise be a silent 400.
		server.logger.Warn("registry request violated a data constraint", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusBadRequest, errMsgConstraintViolation)
	case errors.Is(err, registry.ErrURLCheckUnavailable):
		// The service already logged why the check was unavailable.
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgURLCheckUnavailable)
	default:
		server.logger.Error("registry service error", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	}
}

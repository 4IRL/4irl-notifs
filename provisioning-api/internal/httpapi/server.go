// Package httpapi exposes the provisioning service over HTTP: a health
// check plus the provision/deprovision/list/delete endpoints consumed by the
// admin UI and individual 4IRL apps.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

// ProvisioningService is the subset of provisioning.Service the HTTP layer
// depends on, so handlers can be tested against a fake.
type ProvisioningService interface {
	// Provision creates (or reuses) the person's global ntfy user, grants
	// the app's scoped topic access, and returns a fresh app-labeled token.
	Provision(ctx context.Context, request provisioning.ProvisionRequest) (provisioning.ProvisionResult, error)
	// Deprovision revokes the app's topic access and removes the app's
	// tokens for the ntfy user, deleting the user entirely if no topic
	// grants remain.
	Deprovision(ctx context.Context, request provisioning.DeprovisionRequest) error
	// ListUsers returns every provisioned user with their derived apps and
	// raw topic patterns.
	ListUsers(ctx context.Context) ([]provisioning.UserSummary, error)
	// DeleteUser removes a user entirely.
	DeleteUser(ctx context.Context, userID string) error
	// ProvisionApp creates (or reuses) the app's publisher ntfy user, grants
	// its write-only app-wide topic access, and returns a fresh
	// publisher-labeled token.
	ProvisionApp(ctx context.Context, request provisioning.ProvisionAppRequest) (provisioning.ProvisionAppResult, error)
}

// ServerConfig configures a Server. Service is required; Logger defaults to
// slog.Default() when nil.
type ServerConfig struct {
	// Service is the provisioning backend the HTTP handlers delegate to.
	Service ProvisioningService
	// Logger receives internal-error diagnostics; defaults to slog.Default().
	Logger *slog.Logger
}

// Server wraps an *http.ServeMux exposing the provisioning HTTP API.
type Server struct {
	service ProvisioningService
	logger  *slog.Logger
	mux     *http.ServeMux
}

// NewServer builds a Server from config, applying production defaults and
// registering all routes.
func NewServer(config ServerConfig) *Server {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	server := &Server{
		service: config.Service,
		logger:  logger,
		mux:     http.NewServeMux(),
	}
	server.routes()
	return server
}

// Handler returns the http.Handler serving the provisioning API.
func (server *Server) Handler() http.Handler {
	return server.mux
}

// routes registers every HTTP route on the server's mux.
func (server *Server) routes() {
	server.mux.HandleFunc("GET /healthz", server.handleHealthz)
	server.mux.HandleFunc("POST /v1/provision", server.handleProvision)
	server.mux.HandleFunc("POST /v1/deprovision", server.handleDeprovision)
	server.mux.HandleFunc("GET /v1/users", server.handleListUsers)
	server.mux.HandleFunc("DELETE /v1/users/{user_id}", server.handleDeleteUser)
	server.mux.HandleFunc("POST /v1/provision-app", server.handleProvisionApp)
}

// handleHealthz responds 200 with a plain-text "ok" body for liveness checks.
func (server *Server) handleHealthz(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/plain")
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = responseWriter.Write([]byte("ok"))
}

// provisionRequestBody is the JSON body shared by /v1/provision and
// /v1/deprovision. For /v1/provision, UserID is the calling app's own
// user id and Email is required. For /v1/deprovision, both Email and
// UserID are optional — Email resolves to the derived ntfy user id, or
// UserID is taken as an already-derived ntfy user id directly.
type provisionRequestBody struct {
	AppID  string `json:"app_id"`
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

// provisionResponseBody is the JSON response for a successful provision.
type provisionResponseBody struct {
	UserID       string `json:"user_id"`
	AppID        string `json:"app_id"`
	PersonHash   string `json:"person_hash"`
	TopicPattern string `json:"topic_pattern"`
	Token        string `json:"token"`
}

// writeJSON encodes body as JSON with the given status code and
// application/json Content-Type.
func writeJSON(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

// writeServiceError maps a service-layer error onto its HTTP response: a
// 404 "user does not exist" when the error wraps ntfycli.ErrNotFound, or a
// 500 "internal error" for anything else — logging the real error via the
// server's logger so operators can diagnose it without leaking it to callers.
func (server *Server) writeServiceError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, ntfycli.ErrNotFound) {
		writeJSON(responseWriter, http.StatusNotFound, map[string]string{"error": "user does not exist"})
		return
	}
	server.logger.Error("provisioning service error", "error", err, "path", request.URL.Path)
	writeJSON(responseWriter, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// handleProvision decodes the request body, delegates to
// Service.Provision, and writes the resulting token/topic pattern as JSON.
func (server *Server) handleProvision(responseWriter http.ResponseWriter, request *http.Request) {
	var requestBody provisionRequestBody
	if decodeErr := json.NewDecoder(request.Body).Decode(&requestBody); decodeErr != nil {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	// app_id is validated before user_id, and user_id before email: when
	// multiple fields are invalid, the caller sees the earliest one first
	// (see validation.go).
	if !validateAppID(requestBody.AppID) {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid app_id"})
		return
	}
	if !validateUserID(requestBody.UserID) {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
		return
	}
	if !validateEmail(requestBody.Email) {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid email"})
		return
	}

	result, provisionErr := server.service.Provision(request.Context(), provisioning.ProvisionRequest{
		AppID:     requestBody.AppID,
		AppUserID: requestBody.UserID,
		Email:     requestBody.Email,
	})
	if provisionErr != nil {
		server.writeServiceError(responseWriter, request, provisionErr)
		return
	}

	writeJSON(responseWriter, http.StatusOK, provisionResponseBody{
		UserID:       result.UserID,
		AppID:        result.AppID,
		PersonHash:   result.PersonHash,
		TopicPattern: result.TopicPattern,
		Token:        result.Token,
	})
}

// deprovisionResponseBody is the JSON response for a successful deprovision.
type deprovisionResponseBody struct {
	UserID  string `json:"user_id"`
	AppID   string `json:"app_id"`
	Removed bool   `json:"removed"`
}

// handleDeprovision decodes the request body, resolves the target ntfy user
// id from either an email or an already-derived ntfy user_id, and delegates
// to Service.Deprovision, confirming removal as JSON.
func (server *Server) handleDeprovision(responseWriter http.ResponseWriter, request *http.Request) {
	var requestBody provisionRequestBody
	if decodeErr := json.NewDecoder(request.Body).Decode(&requestBody); decodeErr != nil {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if !validateAppID(requestBody.AppID) {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid app_id"})
		return
	}

	var ntfyUserID string
	switch {
	case requestBody.Email != "":
		if !validateEmail(requestBody.Email) {
			writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid email"})
			return
		}
		ntfyUserID = personhash.NtfyUser(requestBody.Email)
	case requestBody.UserID != "":
		if !validateNtfyUserID(requestBody.UserID) {
			writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
			return
		}
		ntfyUserID = requestBody.UserID
	default:
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "email or user_id required"})
		return
	}

	deprovisionErr := server.service.Deprovision(request.Context(), provisioning.DeprovisionRequest{
		AppID:      requestBody.AppID,
		NtfyUserID: ntfyUserID,
	})
	if deprovisionErr != nil {
		server.writeServiceError(responseWriter, request, deprovisionErr)
		return
	}

	writeJSON(responseWriter, http.StatusOK, deprovisionResponseBody{
		UserID:  ntfyUserID,
		AppID:   requestBody.AppID,
		Removed: true,
	})
}

// userSummaryBody is the JSON representation of one provisioning.UserSummary.
// Apps and TopicPatterns are always non-nil so they serialize as [] rather
// than null when empty.
type userSummaryBody struct {
	UserID        string   `json:"user_id"`
	Apps          []string `json:"apps"`
	TopicPatterns []string `json:"topic_patterns"`
}

// listUsersResponseBody is the JSON response for GET /v1/users. Users is
// always non-nil so it serializes as [] rather than null when empty.
type listUsersResponseBody struct {
	Users []userSummaryBody `json:"users"`
}

// nonNilStrings returns values unchanged, or an empty (non-nil) slice when
// values is nil, so JSON encoding always produces [] instead of null.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// handleListUsers delegates to Service.ListUsers and returns the summaries
// wrapped in a "users" array, normalizing nil slices to empty JSON arrays.
func (server *Server) handleListUsers(responseWriter http.ResponseWriter, request *http.Request) {
	summaries, listErr := server.service.ListUsers(request.Context())
	if listErr != nil {
		server.writeServiceError(responseWriter, request, listErr)
		return
	}

	users := make([]userSummaryBody, 0, len(summaries))
	for _, summary := range summaries {
		users = append(users, userSummaryBody{
			UserID:        summary.UserID,
			Apps:          nonNilStrings(summary.Apps),
			TopicPatterns: nonNilStrings(summary.TopicPatterns),
		})
	}

	writeJSON(responseWriter, http.StatusOK, listUsersResponseBody{Users: users})
}

// deleteUserResponseBody is the JSON response for a successful user deletion.
type deleteUserResponseBody struct {
	UserID  string `json:"user_id"`
	Deleted bool   `json:"deleted"`
}

// handleDeleteUser delegates to Service.DeleteUser using the {user_id} path
// parameter and confirms deletion as JSON.
func (server *Server) handleDeleteUser(responseWriter http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("user_id")
	if !validateUserID(userID) {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
		return
	}

	deleteErr := server.service.DeleteUser(request.Context(), userID)
	if deleteErr != nil {
		server.writeServiceError(responseWriter, request, deleteErr)
		return
	}

	writeJSON(responseWriter, http.StatusOK, deleteUserResponseBody{
		UserID:  userID,
		Deleted: true,
	})
}

// provisionAppRequestBody is the JSON body for /v1/provision-app.
type provisionAppRequestBody struct {
	AppID string `json:"app_id"`
}

// provisionAppResponseBody is the JSON response for a successful
// provision-app call. The token is revealed once at mint time — first-time
// handoff semantics, same as per-user provisioning.
type provisionAppResponseBody struct {
	AppID           string `json:"app_id"`
	PublisherUserID string `json:"publisher_user_id"`
	TopicPattern    string `json:"topic_pattern"`
	Token           string `json:"token"`
}

// handleProvisionApp decodes the request body, delegates to
// Service.ProvisionApp, and writes the resulting publisher identity and
// token as JSON.
func (server *Server) handleProvisionApp(responseWriter http.ResponseWriter, request *http.Request) {
	var requestBody provisionAppRequestBody
	if decodeErr := json.NewDecoder(request.Body).Decode(&requestBody); decodeErr != nil {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if !validateAppID(requestBody.AppID) {
		writeJSON(responseWriter, http.StatusBadRequest, map[string]string{"error": "invalid app_id"})
		return
	}

	result, provisionAppErr := server.service.ProvisionApp(request.Context(), provisioning.ProvisionAppRequest{
		AppID: requestBody.AppID,
	})
	if provisionAppErr != nil {
		server.writeServiceError(responseWriter, request, provisionAppErr)
		return
	}

	writeJSON(responseWriter, http.StatusOK, provisionAppResponseBody{
		AppID:           result.AppID,
		PublisherUserID: result.PublisherUserID,
		TopicPattern:    result.TopicPattern,
		Token:           result.Token,
	})
}

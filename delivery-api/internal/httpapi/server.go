// Package httpapi exposes the delivery service over HTTP: the liveness and
// readiness probes, the operator registry endpoints under /v1/admin, the app
// message endpoints (submit and status under /v1/messages) and the operator
// delivery log and replay endpoints under /v1/admin/deliveries.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// Readiness status values reported in the /readyz body.
const (
	statusOK             = "ok"
	statusSchemaMismatch = "schema_mismatch"
	statusUnavailable    = "unavailable"
)

// Readiness is the database state /readyz reports.
type Readiness struct {
	// SchemaVersion is the highest migration applied to the database.
	SchemaVersion int
	// ExpectedSchemaVersion is the highest migration this binary embeds.
	ExpectedSchemaVersion int
	// LastMigrationFailure is the newest failure recorded for a version above
	// SchemaVersion, or nil.
	LastMigrationFailure *migrate.Failure
}

// ReadinessChecker reports database reachability and schema state.
type ReadinessChecker interface {
	Readiness(ctx context.Context) (Readiness, error)
}

// Registry is the destination and route management surface the /v1/admin
// endpoints call. *registry.Service satisfies it. Resolve is deliberately not
// part of it: resolution is for the delivery path, not the admin API.
type Registry interface {
	CreateDestination(ctx context.Context, input registry.DestinationInput) (registry.DestinationView, error)
	ListDestinations(ctx context.Context) ([]registry.DestinationView, error)
	GetDestination(ctx context.Context, name string) (registry.DestinationView, error)
	UpdateDestination(ctx context.Context, name string, patch registry.DestinationPatch) (registry.DestinationView, error)
	DeleteDestination(ctx context.Context, name string) (bool, error)
	CreateRoute(ctx context.Context, input registry.RouteInput) (registry.RouteView, error)
	ListRoutes(ctx context.Context) ([]registry.RouteView, error)
	GetRoute(ctx context.Context, id int64) (registry.RouteView, error)
	UpdateRoute(ctx context.Context, id int64, patch registry.RoutePatch) (registry.RouteView, error)
	DeleteRoute(ctx context.Context, id int64) (bool, error)
}

// ServerConfig configures a Server. Logger defaults to slog.Default() when nil.
type ServerConfig struct {
	// Logger receives internal-error diagnostics; defaults to slog.Default().
	Logger *slog.Logger
	// Readiness backs GET /readyz. When nil, /readyz always reports unavailable.
	Readiness ReadinessChecker
	// Registry backs the /v1/admin endpoints. When nil, every admin route
	// answers 503 (fail closed). Callers must leave this an untyped nil when no
	// service exists: a typed-nil pointer stored here is a non-nil interface
	// and would bypass that guard.
	Registry Registry
	// Messages backs the /v1/messages and /v1/admin/deliveries endpoints. When
	// nil, every one of those routes answers 503 (fail closed). The same typed-nil
	// caveat as Registry applies.
	Messages Messages
}

// Server wraps an *http.ServeMux exposing the delivery HTTP API.
type Server struct {
	logger    *slog.Logger
	readiness ReadinessChecker
	registry  Registry
	messages  Messages
	mux       *http.ServeMux
}

// readinessResponseBody is the /readyz JSON body when the database answered.
type readinessResponseBody struct {
	Status                string                `json:"status"`
	SchemaVersion         int                   `json:"schema_version"`
	ExpectedSchemaVersion int                   `json:"expected_schema_version"`
	LastMigrationFailure  *migrationFailureBody `json:"last_migration_failure"`
}

// migrationFailureBody is the JSON form of a recorded migration failure.
type migrationFailureBody struct {
	Version  int    `json:"version"`
	Error    string `json:"error"`
	FailedAt string `json:"failed_at"`
}

// unavailableResponseBody is the /readyz JSON body when readiness cannot be
// determined; it deliberately carries no detail.
type unavailableResponseBody struct {
	Status string `json:"status"`
}

// NewServer builds a Server from config, applying production defaults and
// registering all routes.
func NewServer(config ServerConfig) *Server {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	server := &Server{
		logger:    logger,
		readiness: config.Readiness,
		registry:  config.Registry,
		messages:  config.Messages,
		mux:       http.NewServeMux(),
	}
	server.routes()
	return server
}

// Handler returns the http.Handler serving the delivery API.
func (server *Server) Handler() http.Handler {
	return server.mux
}

// routes registers every HTTP route on the server's mux.
func (server *Server) routes() {
	server.mux.HandleFunc("GET /healthz", server.handleHealthz)
	server.mux.HandleFunc("GET /readyz", server.handleReadyz)

	server.mux.HandleFunc("POST /v1/admin/destinations", server.requireRegistry(server.handleCreateDestination))
	server.mux.HandleFunc("GET /v1/admin/destinations", server.requireRegistry(server.handleListDestinations))
	server.mux.HandleFunc("GET /v1/admin/destinations/{name}", server.requireRegistry(server.handleGetDestination))
	server.mux.HandleFunc("PATCH /v1/admin/destinations/{name}", server.requireRegistry(server.handleUpdateDestination))
	server.mux.HandleFunc("DELETE /v1/admin/destinations/{name}", server.requireRegistry(server.handleDeleteDestination))
	server.mux.HandleFunc("POST /v1/admin/routes", server.requireRegistry(server.handleCreateRoute))
	server.mux.HandleFunc("GET /v1/admin/routes", server.requireRegistry(server.handleListRoutes))
	server.mux.HandleFunc("GET /v1/admin/routes/{id}", server.requireRegistry(server.handleGetRoute))
	server.mux.HandleFunc("PATCH /v1/admin/routes/{id}", server.requireRegistry(server.handleUpdateRoute))
	server.mux.HandleFunc("DELETE /v1/admin/routes/{id}", server.requireRegistry(server.handleDeleteRoute))

	server.mux.HandleFunc("POST /v1/messages", server.requireMessages(server.handleCreateMessage))
	server.mux.HandleFunc("GET /v1/messages/{id}", server.requireMessages(server.handleGetMessage))
	server.mux.HandleFunc("GET /v1/messages", server.requireMessages(server.handleGetMessageByKey))
	server.mux.HandleFunc("GET /v1/admin/deliveries", server.requireMessages(server.handleListDeliveries))
	server.mux.HandleFunc("GET /v1/admin/deliveries/{id}/attempts", server.requireMessages(server.handleListAttempts))
	server.mux.HandleFunc("POST /v1/admin/deliveries/{id}/replay", server.requireMessages(server.handleReplayDelivery))
}

// handleHealthz responds 200 with a plain-text "ok" body for liveness checks.
// It never touches the database, so it stays green while Postgres is down.
func (server *Server) handleHealthz(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/plain")
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = responseWriter.Write([]byte("ok"))
}

// handleReadyz reports database reachability and schema state. A missing
// checker or a checker error is 503 "unavailable" with the error logged but
// never leaked; a schema version that differs from the binary's embedded
// expectation (in either direction) is 503 "schema_mismatch"; otherwise 200.
// A recorded newer-migration failure is reported in either non-error case.
func (server *Server) handleReadyz(responseWriter http.ResponseWriter, request *http.Request) {
	if server.readiness == nil {
		writeJSON(responseWriter, http.StatusServiceUnavailable, unavailableResponseBody{Status: statusUnavailable})
		return
	}

	readiness, checkErr := server.readiness.Readiness(request.Context())
	if checkErr != nil {
		server.logger.Error("readiness check failed", "error", checkErr)
		writeJSON(responseWriter, http.StatusServiceUnavailable, unavailableResponseBody{Status: statusUnavailable})
		return
	}

	statusCode := http.StatusServiceUnavailable
	status := statusSchemaMismatch
	if readiness.SchemaVersion == readiness.ExpectedSchemaVersion {
		statusCode = http.StatusOK
		status = statusOK
	}

	body := readinessResponseBody{
		Status:                status,
		SchemaVersion:         readiness.SchemaVersion,
		ExpectedSchemaVersion: readiness.ExpectedSchemaVersion,
	}
	if failure := readiness.LastMigrationFailure; failure != nil {
		body.LastMigrationFailure = &migrationFailureBody{
			Version:  failure.Version,
			Error:    failure.Error,
			FailedAt: failure.FailedAt.UTC().Format(time.RFC3339),
		}
	}
	writeJSON(responseWriter, statusCode, body)
}

// setSensitiveHeaders marks a response as carrying data that must never be
// cached or content-sniffed by a browser.
func setSensitiveHeaders(responseWriter http.ResponseWriter) {
	responseWriter.Header().Set("Cache-Control", "no-store")
	responseWriter.Header().Set("X-Content-Type-Options", "nosniff")
}

// writeJSON encodes body as JSON with the given status code and
// application/json Content-Type.
func writeJSON(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

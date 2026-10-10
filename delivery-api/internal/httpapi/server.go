// Package httpapi exposes the delivery service over HTTP. For now it serves
// only the liveness and readiness probes; the delivery endpoints are added by
// later phases.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
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

// ServerConfig configures a Server. Logger defaults to slog.Default() when nil.
type ServerConfig struct {
	// Logger receives internal-error diagnostics; defaults to slog.Default().
	Logger *slog.Logger
	// Readiness backs GET /readyz. When nil, /readyz always reports unavailable.
	Readiness ReadinessChecker
}

// Server wraps an *http.ServeMux exposing the delivery HTTP API.
type Server struct {
	logger    *slog.Logger
	readiness ReadinessChecker
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

// writeJSON encodes body as JSON with the given status code and
// application/json Content-Type.
func writeJSON(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

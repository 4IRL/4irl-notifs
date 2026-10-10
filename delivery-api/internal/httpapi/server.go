// Package httpapi exposes the delivery service over HTTP. For now it serves
// only the liveness probe; readiness and the delivery endpoints are added by
// later steps and phases.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ServerConfig configures a Server. Logger defaults to slog.Default() when nil.
type ServerConfig struct {
	// Logger receives internal-error diagnostics; defaults to slog.Default().
	Logger *slog.Logger
}

// Server wraps an *http.ServeMux exposing the delivery HTTP API.
type Server struct {
	logger *slog.Logger
	mux    *http.ServeMux
}

// NewServer builds a Server from config, applying production defaults and
// registering all routes.
func NewServer(config ServerConfig) *Server {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	server := &Server{
		logger: logger,
		mux:    http.NewServeMux(),
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
}

// handleHealthz responds 200 with a plain-text "ok" body for liveness checks.
// It never touches the database, so it stays green while Postgres is down.
func (server *Server) handleHealthz(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("Content-Type", "text/plain")
	responseWriter.WriteHeader(http.StatusOK)
	_, _ = responseWriter.Write([]byte("ok"))
}

// writeJSON encodes body as JSON with the given status code and
// application/json Content-Type.
//
//nolint:unused // first caller is the /readyz handler added in step 4.
func writeJSON(responseWriter http.ResponseWriter, statusCode int, body any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(body)
}

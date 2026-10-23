// Package httpapi exposes the delivery service over HTTP: the liveness and
// readiness probes, the operator registry endpoints under /v1/admin, the app
// message endpoints (submit and status under /v1/messages), the operator
// delivery log and replay endpoints under /v1/admin/deliveries, and the
// per-app ntfy publisher endpoints under /v1/admin/apps, the app-facing
// heartbeat pings under /v1/heartbeats, the operator heartbeat check
// endpoints under /v1/admin/heartbeats, the app identity token exchange under
// /v1/app-identity and the operator identity endpoints under
// /v1/admin/app-identity.
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

// CheckerFreshness reports whether the heartbeat checker is still ticking.
// *heartbeat.Freshness satisfies it.
type CheckerFreshness interface {
	// Status returns the last successful tick (nil when the checker has never
	// ticked) and whether the checker is stale.
	Status() (lastTickAt *time.Time, stale bool)
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
	RotateWebhookSecret(ctx context.Context, name string, overlapHours int) (registry.DestinationView, string, error)
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
	// HeartbeatChecker backs the heartbeat_checker field of GET /readyz. When
	// nil, /readyz reports the checker as never ticked and stale (fail closed).
	// Callers must leave this an untyped nil when no tracker exists.
	HeartbeatChecker CheckerFreshness
	// Registry backs the /v1/admin endpoints. When nil, every admin route
	// answers 503 (fail closed). Callers must leave this an untyped nil when no
	// service exists: a typed-nil pointer stored here is a non-nil interface
	// and would bypass that guard.
	Registry Registry
	// Messages backs the /v1/messages and /v1/admin/deliveries endpoints. When
	// nil, every one of those routes answers 503 (fail closed). The same typed-nil
	// caveat as Registry applies.
	Messages Messages
	// Publishers backs the /v1/admin/apps/{app_id}/ntfy-publisher endpoints. When
	// nil, both routes answer 503 (fail closed). Callers must leave this an
	// untyped nil when no service exists: a typed-nil pointer stored here is a
	// non-nil interface and would bypass that guard.
	Publishers Publishers
	// Heartbeats backs the /v1/heartbeats and /v1/admin/heartbeats endpoints. When
	// nil, every one of those routes answers 503 (fail closed). The same typed-nil
	// caveat as Registry applies.
	Heartbeats Heartbeats
	// AppIdentity backs POST /v1/app-identity/exchange, the /v1/admin/app-identity
	// endpoints and the app key check on every app-facing route (/v1/messages and the
	// heartbeat pings). When nil, every one of those routes answers 503 (fail closed;
	// there is no bypass). The same typed-nil caveat as Registry applies.
	AppIdentity AppIdentity
	// Now supplies the time the heartbeat views derive their state at; defaults to
	// time.Now. Tests set it to run on a fixed clock.
	Now func() time.Time
}

// Server wraps an *http.ServeMux exposing the delivery HTTP API.
type Server struct {
	logger      *slog.Logger
	readiness   ReadinessChecker
	checker     CheckerFreshness
	registry    Registry
	messages    Messages
	publishers  Publishers
	heartbeats  Heartbeats
	appIdentity AppIdentity
	now         func() time.Time
	mux         *http.ServeMux
}

// readinessResponseBody is the /readyz JSON body when the database answered.
type readinessResponseBody struct {
	Status                string                `json:"status"`
	SchemaVersion         int                   `json:"schema_version"`
	ExpectedSchemaVersion int                   `json:"expected_schema_version"`
	LastMigrationFailure  *migrationFailureBody `json:"last_migration_failure"`
	HeartbeatChecker      heartbeatCheckerBody  `json:"heartbeat_checker"`
}

// heartbeatCheckerBody is the JSON form of the heartbeat checker's freshness.
// Staleness never changes the /readyz status or HTTP code: the compose
// healthcheck probes /readyz, and an unhealthy container is not the right
// reaction to a stalled checker. The watchdog interprets this field instead.
type heartbeatCheckerBody struct {
	LastTickAt *string `json:"last_tick_at"`
	Stale      bool    `json:"stale"`
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
		logger:      logger,
		readiness:   config.Readiness,
		checker:     config.HeartbeatChecker,
		registry:    config.Registry,
		messages:    config.Messages,
		publishers:  config.Publishers,
		heartbeats:  config.Heartbeats,
		appIdentity: config.AppIdentity,
		now:         config.Now,
		mux:         http.NewServeMux(),
	}
	if server.now == nil {
		server.now = time.Now
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
	server.mux.HandleFunc("POST /v1/admin/destinations/{name}/rotate-secret", server.requireRegistry(server.handleRotateSecret))
	server.mux.HandleFunc("POST /v1/admin/routes", server.requireRegistry(server.handleCreateRoute))
	server.mux.HandleFunc("GET /v1/admin/routes", server.requireRegistry(server.handleListRoutes))
	server.mux.HandleFunc("GET /v1/admin/routes/{id}", server.requireRegistry(server.handleGetRoute))
	server.mux.HandleFunc("PATCH /v1/admin/routes/{id}", server.requireRegistry(server.handleUpdateRoute))
	server.mux.HandleFunc("DELETE /v1/admin/routes/{id}", server.requireRegistry(server.handleDeleteRoute))

	server.mux.HandleFunc("POST /v1/admin/apps/{app_id}/ntfy-publisher", server.requirePublishers(server.handleMintPublisher))
	server.mux.HandleFunc("GET /v1/admin/apps/{app_id}/ntfy-publisher", server.requirePublishers(server.handleGetPublisher))
	server.mux.HandleFunc("DELETE /v1/admin/apps/{app_id}/ntfy-publisher", server.requirePublishers(server.handleDeletePublisher))

	// App-facing pings sit outside /v1/admin, like POST /v1/messages, and take the app key: the
	// key's app must be the {app_id} the ping names.
	server.mux.HandleFunc("POST /v1/heartbeats/{app_id}/{name}",
		server.requireHeartbeats(server.requireAppKey(assertedAppIDFromPath, server.handlePingCheck)))
	server.mux.HandleFunc("POST /v1/heartbeats/{app_id}/{name}/fail",
		server.requireHeartbeats(server.requireAppKey(assertedAppIDFromPath, server.handleFailCheck)))
	server.mux.HandleFunc("GET /v1/admin/heartbeats", server.requireHeartbeats(server.handleListChecks))
	server.mux.HandleFunc("POST /v1/admin/heartbeats", server.requireHeartbeats(server.handleCreateCheck))
	server.mux.HandleFunc("GET /v1/admin/heartbeats/{id}", server.requireHeartbeats(server.handleGetCheck))
	server.mux.HandleFunc("PATCH /v1/admin/heartbeats/{id}", server.requireHeartbeats(server.handleUpdateCheck))
	server.mux.HandleFunc("DELETE /v1/admin/heartbeats/{id}", server.requireHeartbeats(server.handleDeleteCheck))
	server.mux.HandleFunc("POST /v1/admin/heartbeats/{id}/pause", server.requireHeartbeats(server.handlePauseCheck))
	server.mux.HandleFunc("POST /v1/admin/heartbeats/{id}/resume", server.requireHeartbeats(server.handleResumeCheck))

	// The exchange is app-facing but takes no app key: its GitHub OIDC token is the credential.
	server.mux.HandleFunc("POST /v1/app-identity/exchange", server.requireAppIdentity(server.handleExchange))
	server.mux.HandleFunc("GET /v1/admin/app-identity/requests", server.requireAppIdentity(server.handleListIdentityRequests))
	server.mux.HandleFunc("POST /v1/admin/app-identity/requests/{id}/approve", server.requireAppIdentity(server.handleApproveIdentityRequest))
	server.mux.HandleFunc("POST /v1/admin/app-identity/requests/{id}/reject", server.requireAppIdentity(server.handleRejectIdentityRequest))
	server.mux.HandleFunc("GET /v1/admin/app-identity/bindings", server.requireAppIdentity(server.handleListIdentityBindings))
	server.mux.HandleFunc("POST /v1/admin/app-identity/keys/{id}/revoke", server.requireAppIdentity(server.handleRevokeIdentityKey))
	server.mux.HandleFunc("DELETE /v1/admin/app-identity/bindings/{app_id}", server.requireAppIdentity(server.handleUnbindApp))

	// The app-facing message routes take the app key: the key's app must be the app_id the
	// request asserts (in the body for a submit, in the query for a read or cancel).
	server.mux.HandleFunc("POST /v1/messages",
		server.requireMessages(server.requireAppKey(assertedAppIDFromBody, server.handleCreateMessage)))
	server.mux.HandleFunc("GET /v1/messages/{id}",
		server.requireMessages(server.requireAppKey(assertedAppIDFromQuery, server.handleGetMessage)))
	server.mux.HandleFunc("GET /v1/messages",
		server.requireMessages(server.requireAppKey(assertedAppIDFromQuery, server.handleGetMessageByKey)))
	server.mux.HandleFunc("DELETE /v1/messages/{id}",
		server.requireMessages(server.requireAppKey(assertedAppIDFromQuery, server.handleCancelMessage)))
	server.mux.HandleFunc("DELETE /v1/messages",
		server.requireMessages(server.requireAppKey(assertedAppIDFromQuery, server.handleCancelMessageByKey)))
	// Operator submit (the admin UI's send-test): the POST /v1/messages contract with no app key,
	// the operator asserting any app_id.
	server.mux.HandleFunc("POST /v1/admin/messages", server.requireMessages(server.handleCreateMessage))
	server.mux.HandleFunc("GET /v1/admin/scheduled-messages", server.requireMessages(server.handleListScheduledMessages))
	server.mux.HandleFunc("DELETE /v1/admin/scheduled-messages/{id}", server.requireMessages(server.handleAdminCancelMessage))
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
// A recorded newer-migration failure is reported in either non-error case. Both
// bodies carry heartbeat_checker, which never changes the status or HTTP code.
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
		HeartbeatChecker:      server.checkerStatus(),
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

// checkerStatus reports the checker's freshness; with no tracker it
// reports a checker that never ticked and is stale.
func (server *Server) checkerStatus() heartbeatCheckerBody {
	if server.checker == nil {
		return heartbeatCheckerBody{Stale: true}
	}
	lastTickAt, stale := server.checker.Status()
	body := heartbeatCheckerBody{Stale: stale}
	body.LastTickAt = formatUTC(lastTickAt)
	return body
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

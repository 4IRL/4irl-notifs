package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
)

// TestHealthzReturnsOK verifies GET /healthz responds 200 with body "ok" and
// Content-Type text/plain.
func TestHealthzReturnsOK(testInstance *testing.T) {
	server := NewServer(ServerConfig{})

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); body != "ok" {
		testInstance.Fatalf("body = %q, want %q", body, "ok")
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/plain" {
		testInstance.Fatalf("Content-Type = %q, want %q", contentType, "text/plain")
	}
}

// TestRoutesRejectUnsupportedRequests pins the mux behavior for the wrong
// method on a known path and for an unknown path.
func TestRoutesRejectUnsupportedRequests(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "POST healthz", method: http.MethodPost, path: "/healthz", wantStatus: http.StatusMethodNotAllowed},
		{name: "POST readyz", method: http.MethodPost, path: "/readyz", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET unknown path", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound},
		{name: "PUT admin destination", method: http.MethodPut, path: "/v1/admin/destinations/x", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET unknown admin path", method: http.MethodGet, path: "/v1/admin/nope", wantStatus: http.StatusNotFound},
		{name: "GET destinations outside admin", method: http.MethodGet, path: "/v1/destinations", wantStatus: http.StatusNotFound},
		{name: "GET heartbeat ping", method: http.MethodGet, path: "/v1/heartbeats/urls4irl/nightly_backup", wantStatus: http.StatusMethodNotAllowed},
		{name: "PUT heartbeat ping", method: http.MethodPut, path: "/v1/heartbeats/urls4irl/nightly_backup", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET heartbeat fail ping", method: http.MethodGet, path: "/v1/heartbeats/urls4irl/nightly_backup/fail", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE heartbeat fail ping", method: http.MethodDelete, path: "/v1/heartbeats/urls4irl/nightly_backup/fail", wantStatus: http.StatusMethodNotAllowed},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := NewServer(ServerConfig{})

			request := httptest.NewRequest(testCase.method, testCase.path, nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
		})
	}
}

// TestCancelAndScheduledRoutesFailClosedWithoutMessages pins that a server built
// with no Messages answers 503 on the cancel and scheduled-list routes, before it
// looks at the parameters, exactly like the other message routes.
func TestCancelAndScheduledRoutesFailClosedWithoutMessages(testInstance *testing.T) {
	const messageID = "5a3e0f0c-9d1b-4c3a-8f2e-0123456789ab"
	testCases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "cancel by id", method: http.MethodDelete, path: "/v1/messages/" + messageID + "?app_id=urls4irl"},
		{name: "cancel by id without app_id", method: http.MethodDelete, path: "/v1/messages/" + messageID},
		{name: "cancel by key", method: http.MethodDelete, path: "/v1/messages?app_id=urls4irl&idempotency_key=key-1"},
		{name: "cancel by key without params", method: http.MethodDelete, path: "/v1/messages"},
		{name: "admin list", method: http.MethodGet, path: "/v1/admin/scheduled-messages"},
		{name: "admin list with a bad limit", method: http.MethodGet, path: "/v1/admin/scheduled-messages?limit=0"},
		{name: "admin cancel", method: http.MethodDelete, path: "/v1/admin/scheduled-messages/" + messageID},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := NewServer(ServerConfig{})

			request := httptest.NewRequest(testCase.method, testCase.path, nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusServiceUnavailable {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
			}
			if body := recorder.Body.String(); body != `{"error":"messages unavailable"}`+"\n" {
				subTest.Fatalf("body = %q, want the messages unavailable error", body)
			}
		})
	}
}

// TestHeartbeatRoutesFailClosedWithoutHeartbeats pins that a server built with no
// Heartbeats answers 503 on every ping and admin heartbeat route, before it looks
// at the path values or the body.
func TestHeartbeatRoutesFailClosedWithoutHeartbeats(testInstance *testing.T) {
	testCases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "ping", method: http.MethodPost, path: "/v1/heartbeats/urls4irl/nightly_backup"},
		{name: "ping with invalid names", method: http.MethodPost, path: "/v1/heartbeats/BAD/BAD"},
		{name: "fail ping", method: http.MethodPost, path: "/v1/heartbeats/urls4irl/nightly_backup/fail"},
		{name: "admin list", method: http.MethodGet, path: "/v1/admin/heartbeats"},
		{name: "admin create", method: http.MethodPost, path: "/v1/admin/heartbeats", body: `{"app_id":"urls4irl"}`},
		{name: "admin create with a bad body", method: http.MethodPost, path: "/v1/admin/heartbeats", body: `nope`},
		{name: "admin get", method: http.MethodGet, path: "/v1/admin/heartbeats/7"},
		{name: "admin get with a bad id", method: http.MethodGet, path: "/v1/admin/heartbeats/abc"},
		{name: "admin patch", method: http.MethodPatch, path: "/v1/admin/heartbeats/7", body: `{"period_seconds":120}`},
		{name: "admin delete", method: http.MethodDelete, path: "/v1/admin/heartbeats/7"},
		{name: "admin pause", method: http.MethodPost, path: "/v1/admin/heartbeats/7/pause"},
		{name: "admin resume", method: http.MethodPost, path: "/v1/admin/heartbeats/7/resume"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := NewServer(ServerConfig{})

			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusServiceUnavailable {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
			}
			if body := recorder.Body.String(); body != `{"error":"heartbeats unavailable"}`+"\n" {
				subTest.Fatalf("body = %q, want the heartbeats unavailable error", body)
			}
		})
	}
}

// fakeReadinessChecker returns a scripted Readiness and error.
type fakeReadinessChecker struct {
	readiness Readiness
	err       error
}

func (checker *fakeReadinessChecker) Readiness(context.Context) (Readiness, error) {
	return checker.readiness, checker.err
}

// TestReadyz covers the readiness outcomes: schema current, schema mismatch
// (either direction), a recorded newer-migration failure, and a nil checker.
func TestReadyz(testInstance *testing.T) {
	failedAt := time.Date(2026, time.October, 10, 12, 30, 0, 0, time.UTC)
	failure := &migrate.Failure{Version: 2, Error: `relation "x" already exists`, FailedAt: failedAt}
	const failureJSON = `{"version":2,"error":"relation \"x\" already exists","failed_at":"2026-10-10T12:30:00Z"}`
	// neverTickedJSON is the heartbeat_checker member when no tracker is configured.
	const neverTickedJSON = `,"heartbeat_checker":{"last_tick_at":null,"stale":true}`

	testCases := []struct {
		name       string
		checker    ReadinessChecker
		wantStatus int
		wantBody   string
	}{
		{
			name:       "versions equal",
			checker:    &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 1, ExpectedSchemaVersion: 1}},
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok","schema_version":1,"expected_schema_version":1,"last_migration_failure":null` + neverTickedJSON + `}`,
		},
		{
			name: "versions equal with a newer failure still serves 200",
			checker: &fakeReadinessChecker{readiness: Readiness{
				SchemaVersion: 1, ExpectedSchemaVersion: 1, LastMigrationFailure: failure,
			}},
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok","schema_version":1,"expected_schema_version":1,"last_migration_failure":` + failureJSON + neverTickedJSON + `}`,
		},
		{
			name:       "database behind the binary",
			checker:    &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 1, ExpectedSchemaVersion: 2}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"schema_mismatch","schema_version":1,"expected_schema_version":2,"last_migration_failure":null` + neverTickedJSON + `}`,
		},
		{
			name:       "database ahead of the binary",
			checker:    &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 3, ExpectedSchemaVersion: 2}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"schema_mismatch","schema_version":3,"expected_schema_version":2,"last_migration_failure":null` + neverTickedJSON + `}`,
		},
		{
			name: "mismatch carries the failure",
			checker: &fakeReadinessChecker{readiness: Readiness{
				SchemaVersion: 1, ExpectedSchemaVersion: 2, LastMigrationFailure: failure,
			}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"schema_mismatch","schema_version":1,"expected_schema_version":2,"last_migration_failure":` + failureJSON + neverTickedJSON + `}`,
		},
		{
			name:       "nil checker",
			checker:    nil,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unavailable"}`,
		},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := NewServer(ServerConfig{Readiness: testCase.checker})

			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if body := recorder.Body.String(); body != testCase.wantBody+"\n" {
				subTest.Fatalf("body = %q, want %q", body, testCase.wantBody+"\n")
			}
			if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
				subTest.Fatalf("Content-Type = %q, want %q", contentType, "application/json")
			}
		})
	}
}

// TestReadyzCheckerErrorIsLoggedNotLeaked verifies a checker error maps to a
// bare 503 and that the real error text only reaches the log.
func TestReadyzCheckerErrorIsLoggedNotLeaked(testInstance *testing.T) {
	const secretErrText = "dial tcp 10.0.0.5:5432: connection refused for user delivery"

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	server := NewServer(ServerConfig{
		Logger:    logger,
		Readiness: &fakeReadinessChecker{err: errors.New(secretErrText)},
	})

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if body := recorder.Body.String(); body != `{"status":"unavailable"}`+"\n" {
		testInstance.Fatalf("body = %q, want %q", body, `{"status":"unavailable"}`+"\n")
	}
	if strings.Contains(recorder.Body.String(), secretErrText) {
		testInstance.Fatalf("response body leaked the real error text: %s", recorder.Body.String())
	}
	if !strings.Contains(logBuffer.String(), secretErrText) {
		testInstance.Fatalf("log output = %q, want it to contain the real error text %q", logBuffer.String(), secretErrText)
	}
}

// TestHealthzIgnoresReadinessChecker verifies liveness never consults the
// readiness checker, so /healthz stays green while the database is down.
func TestHealthzIgnoresReadinessChecker(testInstance *testing.T) {
	server := NewServer(ServerConfig{Readiness: &fakeReadinessChecker{err: errors.New("db down")}})

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

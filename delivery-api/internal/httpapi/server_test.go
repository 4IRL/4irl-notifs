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
			wantBody:   `{"status":"ok","schema_version":1,"expected_schema_version":1,"last_migration_failure":null}`,
		},
		{
			name: "versions equal with a newer failure still serves 200",
			checker: &fakeReadinessChecker{readiness: Readiness{
				SchemaVersion: 1, ExpectedSchemaVersion: 1, LastMigrationFailure: failure,
			}},
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok","schema_version":1,"expected_schema_version":1,"last_migration_failure":` + failureJSON + `}`,
		},
		{
			name:       "database behind the binary",
			checker:    &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 1, ExpectedSchemaVersion: 2}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"schema_mismatch","schema_version":1,"expected_schema_version":2,"last_migration_failure":null}`,
		},
		{
			name:       "database ahead of the binary",
			checker:    &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 3, ExpectedSchemaVersion: 2}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"schema_mismatch","schema_version":3,"expected_schema_version":2,"last_migration_failure":null}`,
		},
		{
			name: "mismatch carries the failure",
			checker: &fakeReadinessChecker{readiness: Readiness{
				SchemaVersion: 1, ExpectedSchemaVersion: 2, LastMigrationFailure: failure,
			}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"schema_mismatch","schema_version":1,"expected_schema_version":2,"last_migration_failure":` + failureJSON + `}`,
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

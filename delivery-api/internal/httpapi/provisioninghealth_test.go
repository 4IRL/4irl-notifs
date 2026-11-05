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
)

type fakeProvisioningHealth struct {
	err    error
	gotCtx context.Context
}

func (fake *fakeProvisioningHealth) Healthz(ctx context.Context) error {
	fake.gotCtx = ctx
	return fake.err
}

const upstreamSecret = "dial tcp provisioning-api:8080 secret-detail"

func TestProvisioningHealth(testInstance *testing.T) {
	testCases := []struct {
		name       string
		config     ServerConfig
		method     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "ok",
			config:     ServerConfig{ProvisioningHealth: &fakeProvisioningHealth{}},
			method:     http.MethodGet,
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok"}`,
		},
		{
			name:       "error",
			config:     ServerConfig{ProvisioningHealth: &fakeProvisioningHealth{err: errors.New(upstreamSecret)}},
			method:     http.MethodGet,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unavailable"}`,
		},
		{
			name:       "nil interface",
			config:     ServerConfig{},
			method:     http.MethodGet,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unavailable"}`,
		},
		{
			name:       "post is 405",
			config:     ServerConfig{ProvisioningHealth: &fakeProvisioningHealth{}},
			method:     http.MethodPost,
			wantStatus: http.StatusMethodNotAllowed,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := NewServer(testCase.config)
			// No Authorization header is sent: the route needs no key.
			request := httptest.NewRequest(testCase.method, "/healthz/provisioning", nil)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			if testCase.wantStatus == http.StatusMethodNotAllowed {
				return
			}
			if got := strings.TrimSpace(recorder.Body.String()); got != testCase.wantBody {
				subTest.Errorf("body = %q, want %q", got, testCase.wantBody)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				subTest.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Errorf("Cache-Control = %q, want no-store", got)
			}
			if strings.Contains(recorder.Body.String(), upstreamSecret) {
				subTest.Error("upstream error leaked into the response body")
			}
			for headerName, values := range recorder.Header() {
				for _, value := range values {
					if strings.Contains(value, upstreamSecret) {
						subTest.Errorf("upstream error leaked into header %s", headerName)
					}
				}
			}
		})
	}
}

func TestProvisioningHealthLogging(testInstance *testing.T) {
	testCases := []struct {
		name       string
		healthErr  error
		wantLogged bool
	}{
		{name: "failure is logged at warn", healthErr: errors.New(upstreamSecret), wantLogged: true},
		{name: "success logs nothing", healthErr: nil, wantLogged: false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
			server := NewServer(ServerConfig{
				Logger:             logger,
				ProvisioningHealth: &fakeProvisioningHealth{err: testCase.healthErr},
			})
			request := httptest.NewRequest(http.MethodGet, "/healthz/provisioning", nil)
			server.Handler().ServeHTTP(httptest.NewRecorder(), request)

			logged := logBuffer.String()
			if !testCase.wantLogged {
				if logged != "" {
					subTest.Errorf("log = %q, want nothing", logged)
				}
				return
			}
			for _, want := range []string{"level=WARN", "provisioning health check failed", "duration_ms=", upstreamSecret} {
				if !strings.Contains(logged, want) {
					subTest.Errorf("log = %q, want it to contain %q", logged, want)
				}
			}
		})
	}
}

type healthCtxKey struct{}

func TestProvisioningHealthPassesRequestContext(testInstance *testing.T) {
	fake := &fakeProvisioningHealth{}
	server := NewServer(ServerConfig{ProvisioningHealth: fake})
	requestContext := context.WithValue(context.Background(), healthCtxKey{}, "marker")
	request := httptest.NewRequest(http.MethodGet, "/healthz/provisioning", nil).WithContext(requestContext)
	server.Handler().ServeHTTP(httptest.NewRecorder(), request)

	if fake.gotCtx == nil || fake.gotCtx.Value(healthCtxKey{}) != "marker" {
		testInstance.Error("Healthz did not receive the request context")
	}
}

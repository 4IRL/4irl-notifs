package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
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

package ntfypublish

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublishHappyPathSendsExpectedRequestAndReturnsMessageID(t *testing.T) {
	var capturedMethod string
	var capturedPath string
	var capturedAuthorization string
	var capturedBody string

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedMethod = request.Method
		capturedPath = request.URL.Path
		capturedAuthorization = request.Header.Get("Authorization")
		bodyBytes, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("failed to read request body: %v", readErr)
		}
		capturedBody = string(bodyBytes)
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte(`{"id":"VkT2p9wQ"}`)); writeErr != nil {
			t.Fatalf("failed to write response body: %v", writeErr)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})

	messageID, err := client.Publish(context.Background(), "urls4irl-76gzqgp4byjl6dje-alerts", "tk_ephemeral", "hello world")
	if err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}

	if capturedMethod != http.MethodPost {
		t.Fatalf("method = %s, expected POST", capturedMethod)
	}
	if capturedPath != "/urls4irl-76gzqgp4byjl6dje-alerts" {
		t.Fatalf("path = %s, expected /urls4irl-76gzqgp4byjl6dje-alerts", capturedPath)
	}
	if capturedAuthorization != "Bearer tk_ephemeral" {
		t.Fatalf("Authorization = %q, expected %q", capturedAuthorization, "Bearer tk_ephemeral")
	}
	if capturedBody != "hello world" {
		t.Fatalf("body = %q, expected %q", capturedBody, "hello world")
	}
	if messageID != "VkT2p9wQ" {
		t.Fatalf("messageID = %q, expected %q", messageID, "VkT2p9wQ")
	}
}

func TestPublishTrailingSlashBaseURLAvoidsDoubleSlash(t *testing.T) {
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte(`{"id":"abc"}`)); writeErr != nil {
			t.Fatalf("failed to write response body: %v", writeErr)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL + "/"})

	if _, err := client.Publish(context.Background(), "urls4irl-76gzqgp4byjl6dje-alerts", "tk_ephemeral", "hello"); err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}

	if capturedPath != "/urls4irl-76gzqgp4byjl6dje-alerts" {
		t.Fatalf("path = %s, expected /urls4irl-76gzqgp4byjl6dje-alerts (no double slash)", capturedPath)
	}
}

func TestPublishNonSuccessStatusReturnsErrorMentioningStatus(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
		{name: "service unavailable", statusCode: http.StatusServiceUnavailable},
		{name: "internal server error", statusCode: http.StatusInternalServerError},
		{name: "bad request", statusCode: http.StatusBadRequest},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				responseWriter.WriteHeader(testCase.statusCode)
				if _, writeErr := responseWriter.Write([]byte("boom")); writeErr != nil {
					t.Fatalf("failed to write response body: %v", writeErr)
				}
			}))
			defer server.Close()

			client := NewClient(Config{BaseURL: server.URL})

			messageID, err := client.Publish(context.Background(), "urls4irl-76gzqgp4byjl6dje-alerts", "tk_ephemeral", "hello")
			if err == nil {
				t.Fatal("expected a non-nil error for a non-2xx response")
			}
			if messageID != "" {
				t.Fatalf("messageID = %q, expected empty on failure", messageID)
			}
			expectedStatusMarker := http.StatusText(testCase.statusCode)
			if !strings.Contains(err.Error(), expectedStatusMarker) {
				t.Fatalf("error %q does not mention status %q", err.Error(), expectedStatusMarker)
			}
		})
	}
}

func TestConfiguredReflectsWhetherBaseURLIsSet(t *testing.T) {
	if (NewClient(Config{})).Configured() {
		t.Fatal("Configured() must be false for an empty BaseURL")
	}
	if !(NewClient(Config{BaseURL: "http://ntfy:80"})).Configured() {
		t.Fatal("Configured() must be true for a non-empty BaseURL")
	}
}

func TestPublishPropagatesContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte(`{"id":"abc"}`)); writeErr != nil {
			t.Fatalf("failed to write response body: %v", writeErr)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Publish(ctx, "urls4irl-76gzqgp4byjl6dje-alerts", "tk_ephemeral", "hello"); err == nil {
		t.Fatal("expected an error when the context is already cancelled")
	}
}

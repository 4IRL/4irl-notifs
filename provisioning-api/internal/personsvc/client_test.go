package personsvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpsertPersonHappyPathSendsExpectedRequestAndReturnsNil(t *testing.T) {
	var capturedMethod string
	var capturedPath string
	var capturedContentType string
	var capturedBody upsertPersonRequestBody

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedMethod = request.Method
		capturedPath = request.URL.Path
		capturedContentType = request.Header.Get("Content-Type")
		if decodeErr := json.NewDecoder(request.Body).Decode(&capturedBody); decodeErr != nil {
			t.Fatalf("failed to decode request body: %v", decodeErr)
		}
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})

	if err := client.UpsertPerson(context.Background(), "76gzqgp4byjl6dje", "alice@example.com"); err != nil {
		t.Fatalf("UpsertPerson returned unexpected error: %v", err)
	}

	if capturedMethod != http.MethodPut {
		t.Fatalf("method = %s, expected PUT", capturedMethod)
	}
	if capturedPath != "/person" {
		t.Fatalf("path = %s, expected /person", capturedPath)
	}
	if capturedContentType != "application/json" {
		t.Fatalf("Content-Type = %s, expected application/json", capturedContentType)
	}
	expectedBody := upsertPersonRequestBody{PersonHash: "76gzqgp4byjl6dje", Email: "alice@example.com"}
	if capturedBody != expectedBody {
		t.Fatalf("body = %#v, expected %#v", capturedBody, expectedBody)
	}
}

func TestUpsertPersonSendsAccessHeadersOnlyWhenConfigured(t *testing.T) {
	testCases := []struct {
		name                     string
		accessClientID           string
		accessClientSecret       string
		expectClientIDHeader     bool
		expectClientSecretHeader bool
	}{
		{
			name:                     "both configured",
			accessClientID:           "test-client-id",
			accessClientSecret:       "test-client-secret",
			expectClientIDHeader:     true,
			expectClientSecretHeader: true,
		},
		{
			name:                     "both empty",
			accessClientID:           "",
			accessClientSecret:       "",
			expectClientIDHeader:     false,
			expectClientSecretHeader: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var capturedClientIDPresent bool
			var capturedClientIDValue string
			var capturedClientSecretPresent bool
			var capturedClientSecretValue string

			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				capturedClientIDValue = request.Header.Get("CF-Access-Client-Id")
				capturedClientIDPresent = capturedClientIDValue != ""
				capturedClientSecretValue = request.Header.Get("CF-Access-Client-Secret")
				capturedClientSecretPresent = capturedClientSecretValue != ""
				responseWriter.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewClient(Config{
				BaseURL:            server.URL,
				AccessClientID:     testCase.accessClientID,
				AccessClientSecret: testCase.accessClientSecret,
			})

			if err := client.UpsertPerson(context.Background(), "76gzqgp4byjl6dje", "alice@example.com"); err != nil {
				t.Fatalf("UpsertPerson returned unexpected error: %v", err)
			}

			if capturedClientIDPresent != testCase.expectClientIDHeader {
				t.Fatalf("CF-Access-Client-Id present = %v, expected %v", capturedClientIDPresent, testCase.expectClientIDHeader)
			}
			if capturedClientIDPresent && capturedClientIDValue != testCase.accessClientID {
				t.Fatalf("CF-Access-Client-Id value = %q, expected %q", capturedClientIDValue, testCase.accessClientID)
			}
			if capturedClientSecretPresent != testCase.expectClientSecretHeader {
				t.Fatalf("CF-Access-Client-Secret present = %v, expected %v", capturedClientSecretPresent, testCase.expectClientSecretHeader)
			}
			if capturedClientSecretPresent && capturedClientSecretValue != testCase.accessClientSecret {
				t.Fatalf("CF-Access-Client-Secret value = %q, expected %q", capturedClientSecretValue, testCase.accessClientSecret)
			}
		})
	}
}

func TestUpsertPersonTrailingSlashBaseURLAvoidsDoubleSlash(t *testing.T) {
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL + "/"})

	if err := client.UpsertPerson(context.Background(), "76gzqgp4byjl6dje", "alice@example.com"); err != nil {
		t.Fatalf("UpsertPerson returned unexpected error: %v", err)
	}

	if capturedPath != "/person" {
		t.Fatalf("path = %s, expected /person (no double slash)", capturedPath)
	}
}

func TestUpsertPersonNonSuccessStatusReturnsErrorMentioningStatus(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
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

			err := client.UpsertPerson(context.Background(), "76gzqgp4byjl6dje", "alice@example.com")
			if err == nil {
				t.Fatal("expected a non-nil error for a non-2xx response")
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
	if !(NewClient(Config{BaseURL: "https://example.com"})).Configured() {
		t.Fatal("Configured() must be true for a non-empty BaseURL")
	}
}

func TestUpsertPersonPropagatesContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := client.UpsertPerson(ctx, "76gzqgp4byjl6dje", "alice@example.com"); err == nil {
		t.Fatal("expected an error when the context is already cancelled")
	}
}

package provisionclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthz(testInstance *testing.T) {
	testCases := []struct {
		name            string
		handler         http.HandlerFunc
		wantErr         bool
		wantErrContains string
	}{
		{
			name:    "200 is nil",
			handler: func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) },
		},
		{
			name:            "500 is an error",
			handler:         func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusInternalServerError) },
			wantErr:         true,
			wantErrContains: "500",
		},
		{
			name: "redirect is not followed",
			handler: func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, "/elsewhere", http.StatusTemporaryRedirect)
			},
			wantErr:         true,
			wantErrContains: "307",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var mutex sync.Mutex
			var gotMethod, gotPath, gotCredential string
			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requestCount.Add(1)
				if request.URL.Path == "/elsewhere" {
					subTest.Error("redirect was followed")
				}
				mutex.Lock()
				gotMethod, gotPath = request.Method, request.URL.Path
				gotCredential = request.Header.Get(credentialHeader)
				mutex.Unlock()
				testCase.handler(writer, request)
			}))
			defer server.Close()

			client := New(Config{BaseURL: server.URL, Credential: testCredential})
			healthErr := client.Healthz(context.Background())
			if (healthErr != nil) != testCase.wantErr {
				subTest.Fatalf("Healthz err = %v, wantErr %v", healthErr, testCase.wantErr)
			}
			if healthErr != nil && !strings.Contains(healthErr.Error(), testCase.wantErrContains) {
				subTest.Errorf("Healthz err = %q, want it to contain %q", healthErr, testCase.wantErrContains)
			}
			if healthErr != nil && strings.Contains(healthErr.Error(), testCredential) {
				subTest.Error("Healthz error contains the credential")
			}
			mutex.Lock()
			defer mutex.Unlock()
			if gotMethod != http.MethodGet || gotPath != "/healthz" {
				subTest.Errorf("request = %s %s, want GET /healthz", gotMethod, gotPath)
			}
			if gotCredential != "" {
				subTest.Errorf("credential header sent to exempt /healthz: %q", gotCredential)
			}
			if got := requestCount.Load(); got != 1 {
				subTest.Errorf("request count = %d, want exactly 1 (no retry)", got)
			}
		})
	}
}

func TestHealthzTimesOutOnSlowServer(testInstance *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	client := New(Config{BaseURL: server.URL, Credential: testCredential})
	client.healthTimeout = 30 * time.Millisecond
	healthErr := client.Healthz(context.Background())
	if !errors.Is(healthErr, context.DeadlineExceeded) {
		testInstance.Fatalf("Healthz err = %v, want context.DeadlineExceeded", healthErr)
	}
}

func TestNewSetsHealthTimeoutToThreeSecondsRegardlessOfConfigTimeout(testInstance *testing.T) {
	client := New(Config{BaseURL: "http://provisioning-api:8080", Credential: testCredential, Timeout: 42 * time.Second})
	if client.healthTimeout != 3*time.Second {
		testInstance.Errorf("healthTimeout = %v, want 3s", client.healthTimeout)
	}
}

func TestHealthzNotConfiguredFailsClosed(testInstance *testing.T) {
	client := New(Config{})
	if healthErr := client.Healthz(context.Background()); !errors.Is(healthErr, ErrNotConfigured) {
		testInstance.Errorf("Healthz err = %v, want ErrNotConfigured", healthErr)
	}
}

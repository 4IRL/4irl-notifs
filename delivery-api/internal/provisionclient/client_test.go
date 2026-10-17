package provisionclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const validToken = "tk_abcdefghijklmnopqrstuvwxyz012"

func TestMintPublisherSendsExactRequestAndDecodesGrant(testInstance *testing.T) {
	var gotMethod, gotPath, gotContentType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotMethod = request.Method
		gotPath = request.URL.Path
		gotContentType = request.Header.Get("Content-Type")
		bodyBytes, _ := io.ReadAll(request.Body)
		gotBody = string(bodyBytes)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"app_id":            "urls4irl",
			"publisher_user_id": "app-urls4irl",
			"topic_pattern":     "urls4irl-*",
			"token":             validToken,
		})
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL + "/"})
	grant, mintErr := client.MintPublisher(context.Background(), "urls4irl")
	if mintErr != nil {
		testInstance.Fatalf("MintPublisher: %v", mintErr)
	}

	if gotMethod != http.MethodPost || gotPath != "/v1/provision-app" {
		testInstance.Errorf("request = %s %s, want POST /v1/provision-app", gotMethod, gotPath)
	}
	if gotContentType != "application/json" {
		testInstance.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody != `{"app_id":"urls4irl","rotate":false}` {
		testInstance.Errorf("body = %s, want exactly {\"app_id\":\"urls4irl\",\"rotate\":false}", gotBody)
	}
	want := PublisherGrant{
		AppID: "urls4irl", PublisherUserID: "app-urls4irl", TopicPattern: "urls4irl-*", Token: validToken,
	}
	if grant != want {
		testInstance.Errorf("grant = %+v, want %+v", grant, want)
	}
}

func TestMintPublisherRejectsBadResponses(testInstance *testing.T) {
	testCases := []struct {
		name       string
		status     int
		body       string
		wantStatus string
	}{
		{name: "server error", status: http.StatusInternalServerError, body: "boom urls4irl", wantStatus: "500"},
		{name: "unauthorized", status: http.StatusUnauthorized, body: "denied", wantStatus: "401"},
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":"invalid app_id echo"}`, wantStatus: "400"},
		{name: "token wrong shape", status: http.StatusOK, body: `{"token":"not-a-token"}`},
		{name: "token missing", status: http.StatusOK, body: `{"app_id":"urls4irl"}`},
		{name: "token uppercase", status: http.StatusOK, body: `{"token":"tk_ABCDEFGHIJKLMNOPQRSTUVWXYZ012"}`},
		{name: "malformed json", status: http.StatusOK, body: `{`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()

			client := New(Config{BaseURL: server.URL})
			grant, mintErr := client.MintPublisher(context.Background(), "urls4irl")
			if mintErr == nil {
				subTest.Fatalf("MintPublisher succeeded with %+v, want an error", grant)
			}
			if grant != (PublisherGrant{}) {
				subTest.Errorf("grant = %+v alongside an error, want zero", grant)
			}
			message := mintErr.Error()
			if testCase.wantStatus != "" && !strings.Contains(message, testCase.wantStatus) {
				subTest.Errorf("error %q does not name status %s", message, testCase.wantStatus)
			}
			for _, leaked := range []string{"boom", "denied", "echo", "not-a-token", validToken, "ABCDEFGHIJ"} {
				if strings.Contains(message, leaked) {
					subTest.Errorf("error %q leaks response content %q", message, leaked)
				}
			}
		})
	}
}

func TestMintPublisherHonorsContextCancellation(testInstance *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	client := New(Config{BaseURL: server.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, mintErr := client.MintPublisher(ctx, "urls4irl"); mintErr == nil {
		testInstance.Fatal("MintPublisher succeeded on a cancelled context, want an error")
	}
}

func TestMintPublisherAppliesTimeout(testInstance *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	client := New(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond})
	if _, mintErr := client.MintPublisher(context.Background(), "urls4irl"); mintErr == nil {
		testInstance.Fatal("MintPublisher succeeded against a hung server, want a timeout error")
	}
}

func TestConfigured(testInstance *testing.T) {
	if New(Config{}).Configured() {
		testInstance.Error("Configured() = true for an empty BaseURL")
	}
	if !New(Config{BaseURL: "http://provisioning-api:8080"}).Configured() {
		testInstance.Error("Configured() = false for a set BaseURL")
	}
}

// failingRoundTripper fails the test if any request reaches the transport.
type failingRoundTripper struct{ testInstance *testing.T }

func (failing failingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	failing.testInstance.Errorf("unexpected request to %s: an unconfigured client must not dial", request.URL)
	return nil, errors.New("unexpected request")
}

func TestMintPublisherUnconfiguredFailsWithoutDialing(testInstance *testing.T) {
	client := New(Config{})
	client.httpClient.Transport = failingRoundTripper{testInstance: testInstance}
	_, mintErr := client.MintPublisher(context.Background(), "urls4irl")
	if !errors.Is(mintErr, ErrNotConfigured) {
		testInstance.Fatalf("MintPublisher error = %v, want ErrNotConfigured", mintErr)
	}
}

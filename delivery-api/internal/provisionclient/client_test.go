package provisionclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	validToken = "tk_abcdefghijklmnopqrstuvwxyz012"
	// testCredential is a fake stand-in with the real shape (64 lowercase hex); it is not a real secret.
	testCredential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestMintPublisherSendsExactRequestAndDecodesGrant(testInstance *testing.T) {
	var gotMethod, gotPath, gotContentType, gotCredential, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotMethod = request.Method
		gotPath = request.URL.Path
		gotContentType = request.Header.Get("Content-Type")
		gotCredential = request.Header.Get("X-Notifs-Internal-Credential")
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

	client := New(Config{BaseURL: server.URL + "/", Credential: testCredential})
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
	if gotCredential != testCredential {
		testInstance.Error("X-Notifs-Internal-Credential header does not equal the configured credential")
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

			client := New(Config{BaseURL: server.URL, Credential: testCredential})
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

func TestMintPublisherRejectsAnOversizeResponseBody(testInstance *testing.T) {
	// A well-formed grant whose JSON value is larger than the read cap must not decode.
	oversize := `{"app_id":"urls4irl","token":"` + validToken + `","topic_pattern":"` +
		strings.Repeat("a", maxResponseBytes) + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(oversize))
	}))
	defer server.Close()

	grant, mintErr := New(Config{BaseURL: server.URL, Credential: testCredential}).MintPublisher(context.Background(), "urls4irl")

	if mintErr == nil {
		testInstance.Fatalf("MintPublisher succeeded with %d-byte body, want an error", len(oversize))
	}
	if grant != (PublisherGrant{}) {
		testInstance.Errorf("grant = %+v alongside an error, want zero", grant)
	}
	if strings.Contains(mintErr.Error(), validToken) {
		testInstance.Errorf("error %q leaks the token", mintErr)
	}
}

func TestMintPublisherDoesNotFollowRedirects(testInstance *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		testInstance.Run(http.StatusText(status), func(subTest *testing.T) {
			targetHits := 0
			target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				targetHits++
				_, _ = writer.Write([]byte(`{"app_id":"urls4irl","token":"` + validToken + `"}`))
			}))
			defer target.Close()
			redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, target.URL+"/v1/provision-app", status)
			}))
			defer redirector.Close()

			grant, mintErr := New(Config{BaseURL: redirector.URL, Credential: testCredential}).MintPublisher(context.Background(), "urls4irl")

			if mintErr == nil {
				subTest.Fatalf("MintPublisher succeeded with %+v after a redirect, want an error", grant)
			}
			if !strings.Contains(mintErr.Error(), strconv.Itoa(status)) {
				subTest.Errorf("error %q does not name status %d", mintErr, status)
			}
			if targetHits != 0 {
				subTest.Errorf("redirect target was hit %d times, want 0", targetHits)
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

	client := New(Config{BaseURL: server.URL, Credential: testCredential})
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

	client := New(Config{BaseURL: server.URL, Credential: testCredential, Timeout: 50 * time.Millisecond})
	if _, mintErr := client.MintPublisher(context.Background(), "urls4irl"); mintErr == nil {
		testInstance.Fatal("MintPublisher succeeded against a hung server, want a timeout error")
	}
}

func TestConfigured(testInstance *testing.T) {
	testCases := []struct {
		name   string
		config Config
		want   bool
	}{
		{name: "neither", config: Config{}, want: false},
		{name: "url only", config: Config{BaseURL: "http://provisioning-api:8080"}, want: false},
		{name: "credential only", config: Config{Credential: testCredential}, want: false},
		{name: "both", config: Config{BaseURL: "http://provisioning-api:8080", Credential: testCredential}, want: true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			if got := New(testCase.config).Configured(); got != testCase.want {
				subTest.Errorf("Configured() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestDefaultTimeoutCoversTheMeasuredWorstCase(testInstance *testing.T) {
	// Measured worst provisioning call was 663ms (5 concurrent provisions); the rule is
	// max(15s, 3 x worst), so the floor of 15s applies.
	if defaultTimeout < 15*time.Second {
		testInstance.Errorf("defaultTimeout = %s, want at least 15s", defaultTimeout)
	}
	if got := New(Config{}).httpClient.Timeout; got != defaultTimeout {
		testInstance.Errorf("client timeout = %s, want the default %s", got, defaultTimeout)
	}
}

func TestMintPublisherAttemptsExactlyOnce(testInstance *testing.T) {
	testCases := []struct {
		name    string
		hang    bool
		timeout time.Duration
	}{
		{name: "server error", timeout: 5 * time.Second},
		{name: "hang past timeout", hang: true, timeout: 300 * time.Millisecond},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var hits atomic.Int32
			received := make(chan struct{}, 8)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				hits.Add(1)
				received <- struct{}{}
				if testCase.hang {
					select {
					case <-release:
					case <-request.Context().Done():
					}
					return
				}
				writer.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			defer close(release)

			client := New(Config{BaseURL: server.URL, Credential: testCredential, Timeout: testCase.timeout})
			if _, mintErr := client.MintPublisher(context.Background(), "urls4irl"); mintErr == nil {
				subTest.Fatal("MintPublisher succeeded, want an error")
			}
			// On a loaded machine the client can time out before the handler goroutine has run, so wait
			// for the first arrival instead of reading the counter immediately.
			select {
			case <-received:
			case <-time.After(5 * time.Second):
				subTest.Fatal("server never saw the request")
			}
			if got := hits.Load(); got != 1 {
				subTest.Errorf("server saw %d requests, want exactly 1 (no retry)", got)
			}
		})
	}
}

func TestMintPublisherErrorsNeverContainTheCredential(testInstance *testing.T) {
	testCases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "server error echoing the header", handler: func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(request.Header.Get("X-Notifs-Internal-Credential")))
		}},
		{name: "malformed body", handler: func(writer http.ResponseWriter, request *http.Request) {
			_, _ = writer.Write([]byte(request.Header.Get("X-Notifs-Internal-Credential")))
		}},
		{name: "connection dropped", handler: func(writer http.ResponseWriter, _ *http.Request) {
			connection, _, hijackErr := writer.(http.Hijacker).Hijack()
			if hijackErr != nil {
				testInstance.Errorf("hijack failed, so this case would not drop the connection: %v", hijackErr)
				return
			}
			_ = connection.Close()
		}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := httptest.NewServer(testCase.handler)
			defer server.Close()

			_, mintErr := New(Config{BaseURL: server.URL, Credential: testCredential}).MintPublisher(
				context.Background(), "urls4irl")
			if mintErr == nil {
				subTest.Fatal("MintPublisher succeeded, want an error")
			}
			if strings.Contains(mintErr.Error(), testCredential) {
				subTest.Errorf("error %q leaks the credential", mintErr)
			}
		})
	}
}

// failingRoundTripper fails the test if any request reaches the transport.
type failingRoundTripper struct{ testInstance *testing.T }

func (failing failingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	failing.testInstance.Errorf("unexpected request to %s: an unconfigured client must not dial", request.URL)
	return nil, errors.New("unexpected request")
}

func TestMintPublisherUnconfiguredFailsWithoutDialing(testInstance *testing.T) {
	testCases := []struct {
		name   string
		config Config
	}{
		{name: "neither", config: Config{}},
		{name: "url only", config: Config{BaseURL: "http://provisioning-api:8080"}},
		{name: "credential only", config: Config{Credential: testCredential}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			client := New(testCase.config)
			client.httpClient.Transport = failingRoundTripper{testInstance: subTest}
			_, mintErr := client.MintPublisher(context.Background(), "urls4irl")
			if !errors.Is(mintErr, ErrNotConfigured) {
				subTest.Fatalf("MintPublisher error = %v, want ErrNotConfigured", mintErr)
			}
		})
	}
}

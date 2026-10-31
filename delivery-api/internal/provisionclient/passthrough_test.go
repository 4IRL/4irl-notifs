package provisionclient

import (
	"context"
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

const testEmail = "person@example.com"

type capturedRequest struct {
	method, path, contentType, credential, body string
}

// captureServer returns a server and a channel (capacity 1) that receives the request it saw, so the
// handler goroutine and the test goroutine never share a variable.
func captureServer(testInstance *testing.T) (*httptest.Server, <-chan capturedRequest) {
	testInstance.Helper()
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		bodyBytes, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			testInstance.Errorf("reading request body: %v", readErr)
		}
		captured <- capturedRequest{
			method:      request.Method,
			path:        request.URL.Path,
			contentType: request.Header.Get("Content-Type"),
			credential:  request.Header.Get("X-Notifs-Internal-Credential"),
			body:        string(bodyBytes),
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	testInstance.Cleanup(server.Close)
	return server, captured
}

func assertCaptured(testInstance *testing.T, captured <-chan capturedRequest, wantPath string, wantBody string) {
	testInstance.Helper()
	var got capturedRequest
	select {
	case got = <-captured:
	case <-time.After(5 * time.Second):
		testInstance.Fatal("upstream never received a request")
	}
	if got.method != http.MethodPost || got.path != wantPath {
		testInstance.Errorf("request = %s %s, want POST %s", got.method, got.path, wantPath)
	}
	if got.contentType != "application/json" {
		testInstance.Errorf("Content-Type = %q, want application/json", got.contentType)
	}
	if got.credential != testCredential {
		testInstance.Error("credential header does not equal the configured credential")
	}
	if got.body != wantBody {
		testInstance.Errorf("body = %s, want exactly %s", got.body, wantBody)
	}
}

func TestProvisionSendsExactRequest(testInstance *testing.T) {
	server, captured := captureServer(testInstance)

	result, provisionErr := New(Config{BaseURL: server.URL + "/", Credential: testCredential}).
		Provision(context.Background(), "urls4irl", testEmail)

	if provisionErr != nil {
		testInstance.Fatalf("Provision: %v", provisionErr)
	}
	assertCaptured(testInstance, captured, "/v1/provision", `{"app_id":"urls4irl","email":"person@example.com"}`)
	if result.Status != http.StatusOK || string(result.Body) != `{"ok":true}` {
		testInstance.Errorf("result = %+v", result)
	}
}

func TestDeprovisionSendsExactRequest(testInstance *testing.T) {
	testCases := []struct {
		name     string
		target   DeprovisionTarget
		wantBody string
	}{
		{name: "by email", target: DeprovisionTarget{Email: testEmail},
			wantBody: `{"app_id":"urls4irl","email":"person@example.com"}`},
		{name: "by user_id", target: DeprovisionTarget{UserID: "u_abc123"},
			wantBody: `{"app_id":"urls4irl","user_id":"u_abc123"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server, captured := captureServer(subTest)

			_, deprovisionErr := New(Config{BaseURL: server.URL, Credential: testCredential}).
				Deprovision(context.Background(), "urls4irl", testCase.target)

			if deprovisionErr != nil {
				subTest.Fatalf("Deprovision: %v", deprovisionErr)
			}
			assertCaptured(subTest, captured, "/v1/deprovision", testCase.wantBody)
		})
	}
}

func TestSetPasswordSendsExactRequest(testInstance *testing.T) {
	server, captured := captureServer(testInstance)

	result, setErr := New(Config{BaseURL: server.URL, Credential: testCredential}).
		SetPassword(context.Background(), "urls4irl", testEmail, "correct-horse")

	if setErr != nil {
		testInstance.Fatalf("SetPassword: %v", setErr)
	}
	assertCaptured(testInstance, captured, "/v1/set-password",
		`{"app_id":"urls4irl","email":"person@example.com","password":"correct-horse"}`)
	if result.Status != http.StatusOK || string(result.Body) != `{"ok":true}` {
		testInstance.Errorf("result = %+v", result)
	}
}

func TestDeprovisionRequiresExactlyOneTarget(testInstance *testing.T) {
	for _, target := range []DeprovisionTarget{{}, {Email: testEmail, UserID: "u_abc123"}} {
		client := New(Config{BaseURL: "http://provisioning-api:8080", Credential: testCredential})
		client.httpClient.Transport = failingRoundTripper{testInstance: testInstance}
		_, deprovisionErr := client.Deprovision(context.Background(), "urls4irl", target)
		if deprovisionErr == nil {
			testInstance.Errorf("Deprovision(%+v) succeeded, want an error", target)
			continue
		}
		if errors.Is(deprovisionErr, ErrNotConfigured) || !strings.Contains(deprovisionErr.Error(), "exactly one") {
			testInstance.Errorf("Deprovision(%+v) error = %v, want the exactly-one validation error", target, deprovisionErr)
		}
	}
}

func TestPassthroughReturnsUpstreamAnswerUnchanged(testInstance *testing.T) {
	testCases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
		noHeaders  bool // upstream sets neither Content-Type nor Retry-After
	}{
		{name: "no content", status: http.StatusNoContent, body: "", noHeaders: true},
		{name: "empty 400 without headers", status: http.StatusBadRequest, body: "", noHeaders: true},
		{name: "ok", status: http.StatusOK, body: `{"token":"secret-token-value"}`},
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":"invalid email"}`},
		{name: "not found", status: http.StatusNotFound, body: `{"error":"user does not exist"}`},
		{name: "too many requests", status: http.StatusTooManyRequests, body: `{"error":"slow down"}`, retryAfter: "7"},
		{name: "server error", status: http.StatusInternalServerError, body: "plain text boom"},
	}
	calls := []struct {
		name string
		call func(*Client) (Passthrough, error)
	}{
		{"provision", func(client *Client) (Passthrough, error) {
			return client.Provision(context.Background(), "urls4irl", testEmail)
		}},
		{"deprovision", func(client *Client) (Passthrough, error) {
			return client.Deprovision(context.Background(), "urls4irl", DeprovisionTarget{Email: testEmail})
		}},
	}
	for _, callCase := range calls {
		call := callCase.call
		for _, testCase := range testCases {
			testInstance.Run(callCase.name+"/"+testCase.name, func(subTest *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					if testCase.noHeaders {
						// A nil entry stops net/http sniffing a Content-Type for the body.
						writer.Header()["Content-Type"] = nil
					} else {
						writer.Header().Set("Content-Type", "application/test+json")
					}
					if testCase.retryAfter != "" {
						writer.Header().Set("Retry-After", testCase.retryAfter)
					}
					writer.WriteHeader(testCase.status)
					_, _ = writer.Write([]byte(testCase.body))
				}))
				defer server.Close()

				result, callErr := call(New(Config{BaseURL: server.URL, Credential: testCredential}))

				if callErr != nil {
					subTest.Fatalf("call returned error %v, want a pass-through", callErr)
				}
				wantContentType := "application/test+json"
				if testCase.noHeaders {
					wantContentType = ""
				}
				want := Passthrough{
					Status: testCase.status, Body: []byte(testCase.body),
					ContentType: wantContentType, RetryAfter: testCase.retryAfter,
				}
				if result.Status != want.Status || string(result.Body) != string(want.Body) ||
					result.ContentType != want.ContentType || result.RetryAfter != want.RetryAfter {
					subTest.Errorf("result = %+v, want %+v", result, want)
				}
			})
		}
	}
}

func TestPassthroughRejectsAnOverCapBody(testInstance *testing.T) {
	testCases := []struct {
		name string
		size int
		ok   bool
	}{
		{name: "exactly at cap", size: maxResponseBytes, ok: true},
		{name: "one over cap", size: maxResponseBytes + 1, ok: false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			payload := strings.Repeat("a", testCase.size)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(payload))
			}))
			defer server.Close()

			result, provisionErr := New(Config{BaseURL: server.URL, Credential: testCredential}).
				Provision(context.Background(), "urls4irl", testEmail)

			if testCase.ok {
				if provisionErr != nil || len(result.Body) != testCase.size {
					subTest.Fatalf("err = %v, body len = %d, want a full pass-through", provisionErr, len(result.Body))
				}
				return
			}
			if provisionErr == nil {
				subTest.Fatalf("Provision succeeded with %d-byte body, want an error", len(result.Body))
			}
			if len(result.Body) != 0 {
				subTest.Errorf("truncated body of %d bytes returned alongside an error", len(result.Body))
			}
		})
	}
}

func TestPassthroughUnconfiguredFailsWithoutDialing(testInstance *testing.T) {
	for _, config := range []Config{{}, {BaseURL: "http://provisioning-api:8080"}, {Credential: testCredential}} {
		client := New(config)
		client.httpClient.Transport = failingRoundTripper{testInstance: testInstance}
		if _, provisionErr := client.Provision(context.Background(), "urls4irl", testEmail); !errors.Is(provisionErr, ErrNotConfigured) {
			testInstance.Errorf("Provision error = %v, want ErrNotConfigured", provisionErr)
		}
		_, deprovisionErr := client.Deprovision(context.Background(), "urls4irl", DeprovisionTarget{Email: testEmail})
		if !errors.Is(deprovisionErr, ErrNotConfigured) {
			testInstance.Errorf("Deprovision error = %v, want ErrNotConfigured", deprovisionErr)
		}
	}
}

func TestPassthroughReturnsARedirectWithoutFollowingIt(testInstance *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		testInstance.Run(strconv.Itoa(status), func(subTest *testing.T) {
			var targetHits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { targetHits.Add(1) }))
			defer target.Close()
			redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, target.URL+"/v1/provision", status)
			}))
			defer redirector.Close()

			result, provisionErr := New(Config{BaseURL: redirector.URL, Credential: testCredential}).
				Provision(context.Background(), "urls4irl", testEmail)

			if provisionErr != nil {
				subTest.Fatalf("Provision: %v", provisionErr)
			}
			if result.Status != status {
				subTest.Errorf("status = %d, want %d", result.Status, status)
			}
			if hits := targetHits.Load(); hits != 0 {
				subTest.Errorf("redirect target was hit %d times, want 0", hits)
			}
		})
	}
}

// dropConnection closes the underlying connection without a (complete) response. It touches no
// testing.T, so it is safe to run on the server goroutine; if hijacking is unsupported the client simply
// gets an ordinary answer and the calling test fails on its own assertions.
func dropConnection(writer http.ResponseWriter, _ *http.Request) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		return
	}
	connection, _, hijackErr := hijacker.Hijack()
	if hijackErr != nil {
		return
	}
	_ = connection.Close()
}

func TestPassthroughErrorsNeverContainEmailCredentialOrBody(testInstance *testing.T) {
	testCases := []struct {
		name    string
		handler http.HandlerFunc
		cancel  bool
		timeout time.Duration
	}{
		{name: "connection dropped", handler: dropConnection},
		{name: "cancelled context", cancel: true, handler: func(http.ResponseWriter, *http.Request) {}},
		{name: "hung upstream bounded by timeout", timeout: 100 * time.Millisecond,
			handler: func(_ http.ResponseWriter, request *http.Request) {
				// Drain the body first: the server only notices the client leaving once it is read.
				_, _ = io.Copy(io.Discard, request.Body)
				<-request.Context().Done()
			}},
		{name: "connection dropped mid-body", handler: func(writer http.ResponseWriter, request *http.Request) {
			// Declare a longer body than is sent, then close: the client's read fails mid-body.
			writer.Header().Set("Content-Length", "1000")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte("leaky-partial-body"))
			dropConnection(writer, request)
		}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			server := httptest.NewServer(testCase.handler)
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if testCase.cancel {
				cancel()
			}
			client := New(Config{BaseURL: server.URL, Credential: testCredential, Timeout: testCase.timeout})

			_, provisionErr := client.Provision(ctx, "urls4irl", testEmail)
			_, deprovisionErr := client.Deprovision(ctx, "urls4irl", DeprovisionTarget{Email: testEmail})

			_, setPasswordErr := client.SetPassword(ctx, "urls4irl", testEmail, "leaky-password-value")

			for _, callErr := range []error{provisionErr, deprovisionErr, setPasswordErr} {
				if callErr == nil {
					subTest.Fatal("call succeeded, want an error")
				}
				for _, leaked := range []string{testEmail, testCredential, "leaky"} {
					if strings.Contains(callErr.Error(), leaked) {
						subTest.Errorf("error %q leaks %q", callErr, leaked)
					}
				}
			}
		})
	}
}

func TestPassthroughOverCapErrorDoesNotLeakTheBody(testInstance *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("leaky-" + strings.Repeat("a", maxResponseBytes)))
	}))
	defer server.Close()

	_, provisionErr := New(Config{BaseURL: server.URL, Credential: testCredential}).
		Provision(context.Background(), "urls4irl", testEmail)

	if provisionErr == nil || strings.Contains(provisionErr.Error(), "leaky") {
		testInstance.Errorf("error = %v, want an error without the body", provisionErr)
	}
}

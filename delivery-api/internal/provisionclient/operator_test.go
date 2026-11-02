package provisionclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperatorMethodsSendExactRequest(testInstance *testing.T) {
	testCases := []struct {
		name       string
		call       func(client *Client) (Passthrough, error)
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{"list users", func(client *Client) (Passthrough, error) { return client.ListUsers(context.Background()) },
			http.MethodGet, "/v1/users", ""},
		{"delete user", func(client *Client) (Passthrough, error) {
			return client.DeleteUser(context.Background(), "u_abc123")
		}, http.MethodDelete, "/v1/users/u_abc123", ""},
		{"deprovision app", func(client *Client) (Passthrough, error) {
			return client.DeprovisionApp(context.Background(), "urls4irl")
		}, http.MethodPost, "/v1/deprovision-app", `{"app_id":"urls4irl"}`},
		{"test notify", func(client *Client) (Passthrough, error) {
			return client.TestNotify(context.Background(), TestNotifyRequest{
				AppID: "urls4irl", Recipients: []string{"a@example.com"}, Channel: "ntfy", Message: "hi", Title: "t"})
		}, http.MethodPost, "/v1/test-notify",
			`{"app_id":"urls4irl","recipients":["a@example.com"],"channel":"ntfy","message":"hi","title":"t"}`},
		{"test notify zero value", func(client *Client) (Passthrough, error) {
			return client.TestNotify(context.Background(), TestNotifyRequest{})
		}, http.MethodPost, "/v1/test-notify",
			`{"app_id":"","recipients":[],"channel":"","message":"","title":""}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			server, captured := captureServer(testInstance)
			client := New(Config{BaseURL: server.URL, Credential: testCredential})
			result, callErr := testCase.call(client)
			if callErr != nil {
				testInstance.Fatalf("call: %v", callErr)
			}
			if result.Status != http.StatusOK || string(result.Body) != `{"ok":true}` {
				testInstance.Errorf("result = %d %s", result.Status, result.Body)
			}
			got := <-captured
			if got.method != testCase.wantMethod || got.path != testCase.wantPath {
				testInstance.Errorf("request = %s %s, want %s %s", got.method, got.path, testCase.wantMethod, testCase.wantPath)
			}
			if got.credential != testCredential {
				testInstance.Error("credential header missing or wrong")
			}
			if got.body != testCase.wantBody {
				testInstance.Errorf("body = %q, want %q", got.body, testCase.wantBody)
			}
			wantContentType := ""
			if testCase.wantBody != "" {
				wantContentType = "application/json"
			}
			if got.contentType != wantContentType {
				testInstance.Errorf("Content-Type = %q, want %q", got.contentType, wantContentType)
			}
		})
	}
}

func TestDeleteUserEscapesHostileUserID(testInstance *testing.T) {
	server, captured := captureServer(testInstance)
	client := New(Config{BaseURL: server.URL, Credential: testCredential})
	if _, callErr := client.DeleteUser(context.Background(), "../deprovision?x=1/y"); callErr != nil {
		testInstance.Fatalf("DeleteUser: %v", callErr)
	}
	got := <-captured
	if got.path != "/v1/users/../deprovision?x=1/y" {
		testInstance.Errorf("decoded path = %q", got.path)
	}
}

func TestDeleteUserHostileIDStaysInOneSegment(testInstance *testing.T) {
	var rawPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rawPath = request.URL.EscapedPath()
		writer.WriteHeader(http.StatusOK)
	}))
	testInstance.Cleanup(server.Close)
	client := New(Config{BaseURL: server.URL, Credential: testCredential})
	if _, callErr := client.DeleteUser(context.Background(), "../a?b/c"); callErr != nil {
		testInstance.Fatalf("DeleteUser: %v", callErr)
	}
	if want := "/v1/users/..%2Fa%3Fb%2Fc"; rawPath != want {
		testInstance.Errorf("escaped path = %q, want %q", rawPath, want)
	}
}

func TestDeleteUserRejectsDotSegmentsAndEmpty(testInstance *testing.T) {
	for _, userID := range []string{"", ".", ".."} {
		testInstance.Run("id="+userID, func(testInstance *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writer.WriteHeader(http.StatusOK)
			}))
			testInstance.Cleanup(server.Close)
			client := New(Config{BaseURL: server.URL, Credential: testCredential})
			if _, callErr := client.DeleteUser(context.Background(), userID); callErr == nil {
				testInstance.Error("DeleteUser must return an error")
			}
			if requests.Load() != 0 {
				testInstance.Errorf("requests sent = %d, want 0", requests.Load())
			}
		})
	}
}

func TestOperatorMethodsRelayNon2xx(testInstance *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "7")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":"slow"}`))
	}))
	testInstance.Cleanup(server.Close)
	client := New(Config{BaseURL: server.URL, Credential: testCredential})
	result, callErr := client.DeleteUser(context.Background(), "u_x")
	if callErr != nil {
		testInstance.Fatalf("DeleteUser: %v", callErr)
	}
	if result.Status != http.StatusTooManyRequests || result.RetryAfter != "7" || string(result.Body) != `{"error":"slow"}` {
		testInstance.Errorf("result = %+v", result)
	}
	notFound := httptest.NewServer(http.NotFoundHandler())
	testInstance.Cleanup(notFound.Close)
	result, callErr = New(Config{BaseURL: notFound.URL, Credential: testCredential}).DeprovisionApp(context.Background(), "x")
	if callErr != nil || result.Status != http.StatusNotFound {
		testInstance.Errorf("404 relay = %+v, %v", result, callErr)
	}
}

func TestOperatorMethodsUnconfigured(testInstance *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	testInstance.Cleanup(server.Close)
	// A base URL with no credential is unconfigured; the counting server proves nothing is dialed.
	client := New(Config{BaseURL: server.URL})
	calls := map[string]func() (Passthrough, error){
		"list":      func() (Passthrough, error) { return client.ListUsers(context.Background()) },
		"delete":    func() (Passthrough, error) { return client.DeleteUser(context.Background(), "u") },
		"deprov":    func() (Passthrough, error) { return client.DeprovisionApp(context.Background(), "a") },
		"testnotif": func() (Passthrough, error) { return client.TestNotify(context.Background(), TestNotifyRequest{}) },
	}
	for name, call := range calls {
		if _, callErr := call(); !errors.Is(callErr, ErrNotConfigured) {
			testInstance.Errorf("%s: err = %v, want ErrNotConfigured", name, callErr)
		}
	}
	if requests.Load() != 0 {
		testInstance.Errorf("requests sent = %d, want 0", requests.Load())
	}
}

func bigBodyServer(testInstance *testing.T, size int) *httptest.Server {
	testInstance.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("a", size)))
	}))
	testInstance.Cleanup(server.Close)
	return server
}

func TestListUsersLargerCap(testInstance *testing.T) {
	underCap := bigBodyServer(testInstance, maxResponseBytes*4)
	result, callErr := New(Config{BaseURL: underCap.URL, Credential: testCredential}).ListUsers(context.Background())
	if callErr != nil || len(result.Body) != maxResponseBytes*4 {
		testInstance.Fatalf("under cap: len=%d err=%v", len(result.Body), callErr)
	}
	overCap := bigBodyServer(testInstance, listUsersMaxResponseBytes+1)
	if _, callErr = New(Config{BaseURL: overCap.URL, Credential: testCredential}).ListUsers(context.Background()); callErr == nil {
		testInstance.Error("ListUsers over 4 MiB must error")
	}
}

func TestOtherOperatorMethodsKeep64KiBCap(testInstance *testing.T) {
	server := bigBodyServer(testInstance, maxResponseBytes+1)
	client := New(Config{BaseURL: server.URL, Credential: testCredential})
	if _, callErr := client.DeleteUser(context.Background(), "u"); callErr == nil {
		testInstance.Error("DeleteUser over cap must error")
	}
	if _, callErr := client.DeprovisionApp(context.Background(), "a"); callErr == nil {
		testInstance.Error("DeprovisionApp over cap must error")
	}
	if _, callErr := client.TestNotify(context.Background(), TestNotifyRequest{}); callErr == nil {
		testInstance.Error("TestNotify over cap must error")
	}
}

func TestOperatorMethodsDoNotFollowRedirects(testInstance *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	testInstance.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	testInstance.Cleanup(redirector.Close)
	client := New(Config{BaseURL: redirector.URL, Credential: testCredential})
	for name, call := range map[string]func() (Passthrough, error){
		"list":   func() (Passthrough, error) { return client.ListUsers(context.Background()) },
		"deprov": func() (Passthrough, error) { return client.DeprovisionApp(context.Background(), "a") },
		"delete": func() (Passthrough, error) { return client.DeleteUser(context.Background(), "u_x") },
		"notify": func() (Passthrough, error) { return client.TestNotify(context.Background(), TestNotifyRequest{}) },
	} {
		result, callErr := call()
		if callErr != nil || result.Status != http.StatusTemporaryRedirect {
			testInstance.Errorf("%s: %+v, %v", name, result, callErr)
		}
	}
	if followed.Load() {
		testInstance.Error("redirect was followed")
	}
}

func TestDeprovisionAppOutlastsDefaultTimeout(testInstance *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(time.Second)
		_, _ = writer.Write([]byte(`{}`))
	}))
	testInstance.Cleanup(server.Close)
	client := New(Config{BaseURL: server.URL, Credential: testCredential, Timeout: 100 * time.Millisecond})
	client.longHTTPClient.Timeout = 10 * time.Second
	if _, callErr := client.ListUsers(context.Background()); callErr == nil {
		testInstance.Error("ListUsers should time out at the default client timeout")
	}
	result, callErr := client.DeprovisionApp(context.Background(), "a")
	if callErr != nil || result.Status != http.StatusOK {
		testInstance.Errorf("DeprovisionApp = %+v, %v; want success past the default timeout", result, callErr)
	}
	if got := New(Config{}).longHTTPClient.Timeout; got != deprovisionAppTimeout {
		testInstance.Errorf("long client timeout = %v, want %v", got, deprovisionAppTimeout)
	}
	configured := 2 * time.Minute
	if got := New(Config{Timeout: configured}).longHTTPClient.Timeout; got != configured {
		testInstance.Errorf("long client timeout = %v, want configured %v", got, configured)
	}
}

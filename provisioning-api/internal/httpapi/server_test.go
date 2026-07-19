package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

// fakeProvisioningService is a scriptable ProvisioningService test double: it
// records every call it receives and returns whichever result/error was
// preconfigured, so handler tests can assert both behavior and interaction.
type fakeProvisioningService struct {
	provisionResult provisioning.ProvisionResult
	provisionErr    error
	provisionCalls  []provisioning.ProvisionRequest

	deprovisionErr   error
	deprovisionCalls []provisioning.DeprovisionRequest

	listUsersResult []provisioning.UserSummary
	listUsersErr    error
	listUsersCalls  int

	deleteUserErr   error
	deleteUserCalls []string
}

// Provision records the request and returns the preconfigured result/error.
func (fake *fakeProvisioningService) Provision(_ context.Context, request provisioning.ProvisionRequest) (provisioning.ProvisionResult, error) {
	fake.provisionCalls = append(fake.provisionCalls, request)
	return fake.provisionResult, fake.provisionErr
}

// Deprovision records the request and returns the preconfigured error.
func (fake *fakeProvisioningService) Deprovision(_ context.Context, request provisioning.DeprovisionRequest) error {
	fake.deprovisionCalls = append(fake.deprovisionCalls, request)
	return fake.deprovisionErr
}

// ListUsers records the call and returns the preconfigured result/error.
func (fake *fakeProvisioningService) ListUsers(_ context.Context) ([]provisioning.UserSummary, error) {
	fake.listUsersCalls++
	return fake.listUsersResult, fake.listUsersErr
}

// DeleteUser records the userID and returns the preconfigured error.
func (fake *fakeProvisioningService) DeleteUser(_ context.Context, userID string) error {
	fake.deleteUserCalls = append(fake.deleteUserCalls, userID)
	return fake.deleteUserErr
}

// aliceEmail/aliceHash/aliceNtfyUser are the golden-vector-derived identity
// used across these tests (see personhash.Hash("alice@example.com")).
const (
	aliceEmail    = "alice@example.com"
	aliceHash     = "76gzqgp4byjl6dje"
	aliceNtfyUser = "u_76gzqgp4byjl6dje"
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

// TestProvisionHappyPath verifies POST /v1/provision calls Service.Provision
// with the decoded request (including email) and returns the result as
// JSON, including person_hash.
func TestProvisionHappyPath(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{
		provisionResult: provisioning.ProvisionResult{
			UserID:       aliceNtfyUser,
			AppID:        "myapp",
			PersonHash:   aliceHash,
			TopicPattern: "myapp-" + aliceHash + "-*",
			Token:        "tk_abc123",
		},
	}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(fmt.Sprintf(`{"app_id":"myapp","user_id":"alice","email":%q}`, aliceEmail))
	request := httptest.NewRequest(http.MethodPost, "/v1/provision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		testInstance.Fatalf("Content-Type = %q, want %q", contentType, "application/json")
	}

	wantCalls := []provisioning.ProvisionRequest{{AppID: "myapp", AppUserID: "alice", Email: aliceEmail}}
	if len(fakeService.provisionCalls) != 1 || fakeService.provisionCalls[0] != wantCalls[0] {
		testInstance.Fatalf("provisionCalls = %+v, want %+v", fakeService.provisionCalls, wantCalls)
	}

	var responseBody map[string]string
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &responseBody); decodeErr != nil {
		testInstance.Fatalf("failed to decode response body: %v", decodeErr)
	}
	wantBody := map[string]string{
		"user_id":       aliceNtfyUser,
		"app_id":        "myapp",
		"person_hash":   aliceHash,
		"topic_pattern": "myapp-" + aliceHash + "-*",
		"token":         "tk_abc123",
	}
	for key, wantValue := range wantBody {
		if responseBody[key] != wantValue {
			testInstance.Fatalf("response[%q] = %q, want %q", key, responseBody[key], wantValue)
		}
	}
}

// TestProvisionMissingOrInvalidEmailRejected verifies POST /v1/provision
// rejects a missing or malformed email with 400 {"error":"invalid email"},
// only after app_id and user_id have already passed validation.
func TestProvisionMissingOrInvalidEmailRejected(testInstance *testing.T) {
	testCases := []struct {
		name  string
		email string
	}{
		{name: "missing email", email: ""},
		{name: "malformed email", email: "not-an-email"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fakeService := &fakeProvisioningService{}
			server := NewServer(ServerConfig{Service: fakeService})

			requestBody := strings.NewReader(fmt.Sprintf(`{"app_id":"myapp","user_id":"alice","email":%q}`, testCase.email))
			request := httptest.NewRequest(http.MethodPost, "/v1/provision", requestBody)
			recorder := httptest.NewRecorder()

			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			wantBody := `{"error":"invalid email"}` + "\n"
			if body := recorder.Body.String(); body != wantBody {
				subTest.Fatalf("body = %q, want %q", body, wantBody)
			}
			if len(fakeService.provisionCalls) != 0 {
				subTest.Fatalf("provisionCalls = %+v, want none (validation should short-circuit)", fakeService.provisionCalls)
			}
		})
	}
}

// TestDeprovisionByEmailHappyPath verifies POST /v1/deprovision with an
// email body resolves it to the derived ntfy user id and calls
// Service.Deprovision with that id.
func TestDeprovisionByEmailHappyPath(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail))
	request := httptest.NewRequest(http.MethodPost, "/v1/deprovision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	wantCalls := []provisioning.DeprovisionRequest{{AppID: "myapp", NtfyUserID: aliceNtfyUser}}
	if len(fakeService.deprovisionCalls) != 1 || fakeService.deprovisionCalls[0] != wantCalls[0] {
		testInstance.Fatalf("deprovisionCalls = %+v, want %+v", fakeService.deprovisionCalls, wantCalls)
	}

	var responseBody map[string]any
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &responseBody); decodeErr != nil {
		testInstance.Fatalf("failed to decode response body: %v", decodeErr)
	}
	if responseBody["user_id"] != aliceNtfyUser || responseBody["app_id"] != "myapp" || responseBody["removed"] != true {
		testInstance.Fatalf("response = %+v, want user_id=%s app_id=myapp removed=true", responseBody, aliceNtfyUser)
	}
}

// TestDeprovisionByNtfyUserIDHappyPath verifies POST /v1/deprovision with a
// user_id body (an already-derived ntfy user id) calls Service.Deprovision
// with that id directly, without deriving it from an email.
func TestDeprovisionByNtfyUserIDHappyPath(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(fmt.Sprintf(`{"app_id":"myapp","user_id":%q}`, aliceNtfyUser))
	request := httptest.NewRequest(http.MethodPost, "/v1/deprovision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	wantCalls := []provisioning.DeprovisionRequest{{AppID: "myapp", NtfyUserID: aliceNtfyUser}}
	if len(fakeService.deprovisionCalls) != 1 || fakeService.deprovisionCalls[0] != wantCalls[0] {
		testInstance.Fatalf("deprovisionCalls = %+v, want %+v", fakeService.deprovisionCalls, wantCalls)
	}
}

// TestDeprovisionWithNeitherEmailNorUserIDRejected verifies POST
// /v1/deprovision with neither email nor user_id set responds 400
// {"error":"email or user_id required"} without calling Service.Deprovision.
func TestDeprovisionWithNeitherEmailNorUserIDRejected(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(`{"app_id":"myapp"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/deprovision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	wantBody := `{"error":"email or user_id required"}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
	if len(fakeService.deprovisionCalls) != 0 {
		testInstance.Fatalf("deprovisionCalls = %+v, want none (validation should short-circuit)", fakeService.deprovisionCalls)
	}
}

// TestDeprovisionInvalidEmailRejected verifies POST /v1/deprovision with a
// malformed email responds 400 {"error":"invalid email"}.
func TestDeprovisionInvalidEmailRejected(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(`{"app_id":"myapp","email":"not-an-email"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/deprovision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	wantBody := `{"error":"invalid email"}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
	if len(fakeService.deprovisionCalls) != 0 {
		testInstance.Fatalf("deprovisionCalls = %+v, want none (validation should short-circuit)", fakeService.deprovisionCalls)
	}
}

// TestDeprovisionInvalidNtfyUserIDShapeRejected verifies POST
// /v1/deprovision with a user_id that does not match the derived ntfy
// username shape responds 400 {"error":"invalid user_id"}.
func TestDeprovisionInvalidNtfyUserIDShapeRejected(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(`{"app_id":"myapp","user_id":"alice"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/deprovision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	wantBody := `{"error":"invalid user_id"}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
	if len(fakeService.deprovisionCalls) != 0 {
		testInstance.Fatalf("deprovisionCalls = %+v, want none (validation should short-circuit)", fakeService.deprovisionCalls)
	}
}

// TestDeprovisionInvalidAppIDRejectedBeforeEmailOrUserID verifies app_id is
// validated first on POST /v1/deprovision, before email/user_id presence is
// even considered.
func TestDeprovisionInvalidAppIDRejectedBeforeEmailOrUserID(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(`{"app_id":"my-app"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/deprovision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	wantBody := `{"error":"invalid app_id"}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
}

// TestListUsersHappyPath verifies GET /v1/users calls Service.ListUsers and
// returns the summaries wrapped in a "users" array.
func TestListUsersHappyPath(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{
		listUsersResult: []provisioning.UserSummary{
			{UserID: "alice", Apps: []string{"myapp"}, TopicPatterns: []string{"myapp-*"}},
		},
	}
	server := NewServer(ServerConfig{Service: fakeService})

	request := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.listUsersCalls != 1 {
		testInstance.Fatalf("listUsersCalls = %d, want 1", fakeService.listUsersCalls)
	}

	wantBody := `{"users":[{"user_id":"alice","apps":["myapp"],"topic_patterns":["myapp-*"]}]}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
}

// TestDeleteUserHappyPath verifies DELETE /v1/users/{user_id} calls
// Service.DeleteUser with the path parameter and returns a confirmation.
func TestDeleteUserHappyPath(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	request := httptest.NewRequest(http.MethodDelete, "/v1/users/alice", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	if len(fakeService.deleteUserCalls) != 1 || fakeService.deleteUserCalls[0] != "alice" {
		testInstance.Fatalf("deleteUserCalls = %+v, want [alice]", fakeService.deleteUserCalls)
	}

	var responseBody map[string]any
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &responseBody); decodeErr != nil {
		testInstance.Fatalf("failed to decode response body: %v", decodeErr)
	}
	if responseBody["user_id"] != "alice" || responseBody["deleted"] != true {
		testInstance.Fatalf("response = %+v, want user_id=alice deleted=true", responseBody)
	}
}

// TestDeleteUserAcceptsDerivedNtfyUserIDShape verifies DELETE
// /v1/users/{user_id} still accepts a derived ntfy username shape
// ("u_"+16-char hash), since validateUserID (unchanged) must accept any
// ntfy username shape.
func TestDeleteUserAcceptsDerivedNtfyUserIDShape(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	request := httptest.NewRequest(http.MethodDelete, "/v1/users/"+aliceNtfyUser, nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(fakeService.deleteUserCalls) != 1 || fakeService.deleteUserCalls[0] != aliceNtfyUser {
		testInstance.Fatalf("deleteUserCalls = %+v, want [%s]", fakeService.deleteUserCalls, aliceNtfyUser)
	}
}

// TestMalformedOrEmptyBodyRejected verifies POST /v1/provision and
// POST /v1/deprovision both reject malformed-JSON and empty bodies with
// 400 {"error":"invalid JSON body"}.
func TestMalformedOrEmptyBodyRejected(testInstance *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"app_id": "myapp",`},
		{name: "empty body", body: ""},
	}

	routes := []string{"/v1/provision", "/v1/deprovision"}

	for _, testCase := range testCases {
		for _, route := range routes {
			testInstance.Run(testCase.name+" "+route, func(subTest *testing.T) {
				fakeService := &fakeProvisioningService{}
				server := NewServer(ServerConfig{Service: fakeService})

				request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(testCase.body))
				recorder := httptest.NewRecorder()

				server.Handler().ServeHTTP(recorder, request)

				if recorder.Code != http.StatusBadRequest {
					subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
				}
				wantBody := `{"error":"invalid JSON body"}` + "\n"
				if body := recorder.Body.String(); body != wantBody {
					subTest.Fatalf("body = %q, want %q", body, wantBody)
				}
				if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
					subTest.Fatalf("Content-Type = %q, want %q", contentType, "application/json")
				}
			})
		}
	}
}

// longIdentifier is 64 characters — one past the 63-char max the app_id and
// user_id regexes allow (1 leading char + 62 more).
var longIdentifier = strings.Repeat("a", 64)

// TestValidationFailures covers every invalid app_id/user_id case against
// POST /v1/provision: hyphenated/uppercase/empty/too-long/bad-leading-char
// identifiers, the reserved user_id values "everyone" and "*", the reserved
// app_id value "everyone", and that app_id is validated before user_id when
// both are invalid.
func TestValidationFailures(testInstance *testing.T) {
	testCases := []struct {
		name    string
		appID   string
		userID  string
		wantMsg string
	}{
		{name: "hyphenated app_id", appID: "my-app", userID: "alice", wantMsg: "invalid app_id"},
		{name: "uppercase app_id", appID: "MyApp", userID: "alice", wantMsg: "invalid app_id"},
		{name: "uppercase user_id", appID: "myapp", userID: "Alice", wantMsg: "invalid user_id"},
		{name: "empty app_id", appID: "", userID: "alice", wantMsg: "invalid app_id"},
		{name: "empty user_id", appID: "myapp", userID: "", wantMsg: "invalid user_id"},
		{name: "app_id too long", appID: longIdentifier, userID: "alice", wantMsg: "invalid app_id"},
		{name: "user_id too long", appID: "myapp", userID: longIdentifier, wantMsg: "invalid user_id"},
		{name: "app_id bad leading char", appID: "_myapp", userID: "alice", wantMsg: "invalid app_id"},
		{name: "user_id bad leading char", appID: "myapp", userID: "-alice", wantMsg: "invalid user_id"},
		{name: "reserved user_id everyone", appID: "myapp", userID: "everyone", wantMsg: "invalid user_id"},
		{name: "reserved user_id star", appID: "myapp", userID: "*", wantMsg: "invalid user_id"},
		{name: "reserved app_id everyone", appID: "everyone", userID: "alice", wantMsg: "invalid app_id"},
		{name: "both invalid, app_id wins", appID: "MyApp", userID: "Alice", wantMsg: "invalid app_id"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fakeService := &fakeProvisioningService{}
			server := NewServer(ServerConfig{Service: fakeService})

			var requestBodyBuffer bytes.Buffer
			if encodeErr := json.NewEncoder(&requestBodyBuffer).Encode(provisionRequestBody{AppID: testCase.appID, UserID: testCase.userID, Email: aliceEmail}); encodeErr != nil {
				subTest.Fatalf("failed to encode request body: %v", encodeErr)
			}

			request := httptest.NewRequest(http.MethodPost, "/v1/provision", &requestBodyBuffer)
			recorder := httptest.NewRecorder()

			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			wantBody := `{"error":"` + testCase.wantMsg + `"}` + "\n"
			if body := recorder.Body.String(); body != wantBody {
				subTest.Fatalf("body = %q, want %q", body, wantBody)
			}
			if len(fakeService.provisionCalls) != 0 {
				subTest.Fatalf("provisionCalls = %+v, want none (validation should short-circuit)", fakeService.provisionCalls)
			}
		})
	}
}

// TestDeprovisionNotFoundMapsTo404 verifies that when Service.Deprovision
// returns an error wrapping ntfycli.ErrNotFound, the handler responds 404
// with the fixed "user does not exist" message.
func TestDeprovisionNotFoundMapsTo404(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{
		deprovisionErr: fmt.Errorf("ntfy access: %w: no such user", ntfycli.ErrNotFound),
	}
	server := NewServer(ServerConfig{Service: fakeService})

	requestBody := strings.NewReader(fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail))
	request := httptest.NewRequest(http.MethodPost, "/v1/deprovision", requestBody)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	wantBody := `{"error":"user does not exist"}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
}

// TestDeleteUserNotFoundMapsTo404 verifies that when Service.DeleteUser
// returns an error wrapping ntfycli.ErrNotFound, the handler responds 404
// with the fixed "user does not exist" message.
func TestDeleteUserNotFoundMapsTo404(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{
		deleteUserErr: fmt.Errorf("ntfy user: %w: no such user", ntfycli.ErrNotFound),
	}
	server := NewServer(ServerConfig{Service: fakeService})

	request := httptest.NewRequest(http.MethodDelete, "/v1/users/alice", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	wantBody := `{"error":"user does not exist"}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
}

// TestGenericServiceErrorMapsTo500 verifies that a generic (non-ErrNotFound)
// service error maps to 500 {"error":"internal error"} for both
// POST /v1/provision and POST /v1/deprovision, that the real error text
// never appears in the response body, and that it IS logged via the
// configured slog.Logger.
func TestGenericServiceErrorMapsTo500(testInstance *testing.T) {
	const secretErrText = "connection refused to database at 10.0.0.5:9999 with credential xyz"

	testCases := []struct {
		name  string
		route string
		body  string
	}{
		{name: "provision", route: "/v1/provision", body: fmt.Sprintf(`{"app_id":"myapp","user_id":"alice","email":%q}`, aliceEmail)},
		{name: "deprovision", route: "/v1/deprovision", body: fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail)},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fakeService := &fakeProvisioningService{
				provisionErr:   errors.New(secretErrText),
				deprovisionErr: errors.New(secretErrText),
			}
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
			server := NewServer(ServerConfig{Service: fakeService, Logger: logger})

			requestBody := strings.NewReader(testCase.body)
			request := httptest.NewRequest(http.MethodPost, testCase.route, requestBody)
			recorder := httptest.NewRecorder()

			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusInternalServerError {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
			}
			wantBody := `{"error":"internal error"}` + "\n"
			if body := recorder.Body.String(); body != wantBody {
				subTest.Fatalf("body = %q, want %q", body, wantBody)
			}
			if strings.Contains(recorder.Body.String(), secretErrText) {
				subTest.Fatalf("response body leaked the real error text: %s", recorder.Body.String())
			}
			if !strings.Contains(logBuffer.String(), secretErrText) {
				subTest.Fatalf("log output = %q, want it to contain the real error text %q", logBuffer.String(), secretErrText)
			}
		})
	}
}

// TestListUsersEmptyReturnsEmptyArray verifies GET /v1/users with zero users
// serializes as {"users":[]} — a literal empty JSON array, never null.
func TestListUsersEmptyReturnsEmptyArray(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{listUsersResult: nil}
	server := NewServer(ServerConfig{Service: fakeService})

	request := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	wantBody := `{"users":[]}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
}

// TestListUsersEmptySlicesSerializeAsEmptyArrays verifies that a
// UserSummary with nil Apps/TopicPatterns serializes those fields as []
// rather than null.
func TestListUsersEmptySlicesSerializeAsEmptyArrays(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{
		listUsersResult: []provisioning.UserSummary{
			{UserID: "alice", Apps: nil, TopicPatterns: nil},
		},
	}
	server := NewServer(ServerConfig{Service: fakeService})

	request := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	wantBody := `{"users":[{"user_id":"alice","apps":[],"topic_patterns":[]}]}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
}

// TestDeleteUserInvalidPathUserIDRejected verifies DELETE
// /v1/users/{user_id} applies the same user_id validation as the body
// field: an invalid path user_id yields 400 {"error":"invalid user_id"}
// without ever calling Service.DeleteUser.
func TestDeleteUserInvalidPathUserIDRejected(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService})

	request := httptest.NewRequest(http.MethodDelete, "/v1/users/Alice", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	wantBody := `{"error":"invalid user_id"}` + "\n"
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
	if len(fakeService.deleteUserCalls) != 0 {
		testInstance.Fatalf("deleteUserCalls = %+v, want none (validation should short-circuit)", fakeService.deleteUserCalls)
	}
}

// TestGoldenVectorHashMatchesPersonhashPackage sanity-checks that this
// file's aliceHash/aliceNtfyUser constants stay in sync with the
// personhash package's own golden vector.
func TestGoldenVectorHashMatchesPersonhashPackage(testInstance *testing.T) {
	if got := personhash.Hash(aliceEmail); got != aliceHash {
		testInstance.Fatalf("personhash.Hash(%q) = %q, want %q (test constants are stale)", aliceEmail, got, aliceHash)
	}
	if got := personhash.NtfyUser(aliceEmail); got != aliceNtfyUser {
		testInstance.Fatalf("personhash.NtfyUser(%q) = %q, want %q (test constants are stale)", aliceEmail, got, aliceNtfyUser)
	}
}

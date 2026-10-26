package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

func postProvision(testInstance *testing.T, fakeService *fakeProvisioningService) *httptest.ResponseRecorder {
	testInstance.Helper()
	server := newTestServer(ServerConfig{Service: fakeService})
	requestBody := strings.NewReader(fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail))
	request := newAuthedRequest(http.MethodPost, "/v1/provision", requestBody)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// TestProvisionPasswordPresentOnlyWhenGenerated verifies the response carries
// "password" when the result has one and omits the key otherwise, and that
// every 200 sends Cache-Control: no-store.
func TestProvisionPasswordPresentOnlyWhenGenerated(testInstance *testing.T) {
	testCases := []struct {
		name         string
		password     string
		wantPassword bool
	}{
		{"generated password is returned", "abcd-efgh-ijkm-npqr", true},
		{"no password leaves the key out", "", false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			fakeService := &fakeProvisioningService{
				provisionResult: provisioning.ProvisionResult{UserID: aliceNtfyUser, AppID: "myapp", PersonHash: aliceHash, Token: "tk_abc123", Password: testCase.password},
			}

			recorder := postProvision(testInstance, fakeService)

			if recorder.Code != http.StatusOK {
				testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
				testInstance.Fatalf("Cache-Control = %q, want no-store", cacheControl)
			}
			var responseBody map[string]any
			if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &responseBody); decodeErr != nil {
				testInstance.Fatalf("failed to decode response body: %v", decodeErr)
			}
			password, hasPassword := responseBody["password"]
			if hasPassword != testCase.wantPassword {
				testInstance.Fatalf("password key present = %v, want %v; body = %s", hasPassword, testCase.wantPassword, recorder.Body.String())
			}
			if testCase.wantPassword && password != testCase.password {
				testInstance.Fatalf("password = %v, want %q", password, testCase.password)
			}
		})
	}
}

// TestSetPasswordAndDeleteStoreFailuresMapTo500 verifies the new failure modes
// (an unrecordable password stamp, a failed person-store delete) surface as the
// generic 500 body without leaking the error text.
func TestSetPasswordAndDeleteStoreFailuresMapTo500(testInstance *testing.T) {
	const wantBody = `{"error":"internal error"}` + "\n"

	testInstance.Run("set-password", func(testInstance *testing.T) {
		fakeService := &fakeProvisioningService{setPasswordErr: provisioning.ErrPersonStoreUnavailable}
		server := newTestServer(ServerConfig{Service: fakeService})
		requestBody := strings.NewReader(fmt.Sprintf(`{"app_id":"tasktracker","email":%q,"password":"sekrit-pw"}`, aliceEmail))
		recorder := httptest.NewRecorder()

		server.Handler().ServeHTTP(recorder, newAuthedRequest(http.MethodPost, "/v1/set-password", requestBody))

		if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != wantBody {
			testInstance.Fatalf("status = %d body = %q, want 500 %q", recorder.Code, recorder.Body.String(), wantBody)
		}
	})

	testInstance.Run("delete user", func(testInstance *testing.T) {
		fakeService := &fakeProvisioningService{deleteUserErr: errors.New("person-service: status 502")}
		server := newTestServer(ServerConfig{Service: fakeService})
		recorder := httptest.NewRecorder()

		server.Handler().ServeHTTP(recorder, newAuthedRequest(http.MethodDelete, "/v1/users/"+aliceNtfyUser, nil))

		if recorder.Code != http.StatusInternalServerError || recorder.Body.String() != wantBody {
			testInstance.Fatalf("status = %d body = %q, want 500 %q", recorder.Code, recorder.Body.String(), wantBody)
		}
	})
}

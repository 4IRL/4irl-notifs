package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

// TestPersonNotInAppMapsTo404OnEveryGuardedRoute pins that the per-app grant
// check's rejection answers the same 404 "user does not exist" an absent ntfy
// user gets (never 401/403, which the admin proxy rewrites to 502), on all three
// guarded routes, with Cache-Control: no-store kept on reset-password.
func TestPersonNotInAppMapsTo404OnEveryGuardedRoute(testInstance *testing.T) {
	testCases := []struct {
		name         string
		path         string
		body         string
		fakeService  *fakeProvisioningService
		wantNoStore  bool
		wantCallsLen func(fakeService *fakeProvisioningService) int
	}{
		{
			name:        "reset-password",
			path:        "/v1/reset-password",
			body:        fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail),
			fakeService: &fakeProvisioningService{resetPasswordErr: provisioning.ErrPersonNotInApp},
			wantNoStore: true,
			wantCallsLen: func(fakeService *fakeProvisioningService) int {
				return len(fakeService.resetPasswordCalls)
			},
		},
		{
			name:        "set-password",
			path:        "/v1/set-password",
			body:        fmt.Sprintf(`{"app_id":"myapp","email":%q,"password":"sekrit-pw"}`, aliceEmail),
			fakeService: &fakeProvisioningService{setPasswordErr: provisioning.ErrPersonNotInApp},
			wantCallsLen: func(fakeService *fakeProvisioningService) int {
				return len(fakeService.setPasswordCalls)
			},
		},
		{
			name:        "deprovision",
			path:        "/v1/deprovision",
			body:        fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail),
			fakeService: &fakeProvisioningService{deprovisionErr: provisioning.ErrPersonNotInApp},
			wantCallsLen: func(fakeService *fakeProvisioningService) int {
				return len(fakeService.deprovisionCalls)
			},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			server := newTestServer(ServerConfig{Service: testCase.fakeService})
			request := newAuthedRequest(http.MethodPost, testCase.path, strings.NewReader(testCase.body))
			recorder := httptest.NewRecorder()

			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNotFound {
				testInstance.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
			}
			wantBody := `{"error":"user does not exist"}` + "\n"
			if body := recorder.Body.String(); body != wantBody {
				testInstance.Fatalf("body = %q, want %q", body, wantBody)
			}
			if testCase.wantNoStore && recorder.Header().Get("Cache-Control") != "no-store" {
				testInstance.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
			if calls := testCase.wantCallsLen(testCase.fakeService); calls != 1 {
				testInstance.Fatalf("service calls = %d, want 1", calls)
			}
		})
	}
}

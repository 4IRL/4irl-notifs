package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

// TestPasswordRoutesKeepUnchangedBodiesWithoutCode pins, for reset-password and
// set-password, that a wrapped plain ntfycli.ErrNotFound keeps the old 404 body,
// a too-many-changes error keeps 429, and a generic error keeps the 500 body,
// none of them carrying a "code" key (guards the branch order).
func TestPasswordRoutesKeepUnchangedBodiesWithoutCode(testInstance *testing.T) {
	routes := []struct {
		name    string
		path    string
		body    string
		service func(err error) *fakeProvisioningService
	}{
		{
			name: "reset-password",
			path: "/v1/reset-password",
			body: fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail),
			service: func(err error) *fakeProvisioningService {
				return &fakeProvisioningService{resetPasswordErr: err}
			},
		},
		{
			name: "set-password",
			path: "/v1/set-password",
			body: fmt.Sprintf(`{"app_id":"myapp","email":%q,"password":"sekrit-pw"}`, aliceEmail),
			service: func(err error) *fakeProvisioningService {
				return &fakeProvisioningService{setPasswordErr: err}
			},
		},
	}
	errorCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"plain not found", fmt.Errorf("ntfy user: %w: no such user", ntfycli.ErrNotFound), http.StatusNotFound, `{"error":"user does not exist"}` + "\n"},
		{"too many changes", &provisioning.TooManyPasswordChangesError{RetryAfterSeconds: 42}, http.StatusTooManyRequests, `{"error":"too many password changes"}` + "\n"},
		{"generic", errors.New("boom"), http.StatusInternalServerError, `{"error":"internal error"}` + "\n"},
	}
	for _, route := range routes {
		for _, errorCase := range errorCases {
			testInstance.Run(route.name+"/"+errorCase.name, func(testInstance *testing.T) {
				server := newTestServer(ServerConfig{Service: route.service(errorCase.err)})
				request := newAuthedRequest(http.MethodPost, route.path, strings.NewReader(route.body))
				recorder := httptest.NewRecorder()

				server.Handler().ServeHTTP(recorder, request)

				if recorder.Code != errorCase.wantStatus {
					testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, errorCase.wantStatus, recorder.Body.String())
				}
				if body := recorder.Body.String(); body != errorCase.wantBody {
					testInstance.Fatalf("body = %q, want %q", body, errorCase.wantBody)
				}
				if strings.Contains(recorder.Body.String(), `"code"`) {
					testInstance.Fatalf("body must not carry a code key: %s", recorder.Body.String())
				}
			})
		}
	}
}

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
			var decoded map[string]string
			if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &decoded); decodeErr != nil {
				testInstance.Fatalf("body %q is not a JSON object: %v", recorder.Body.String(), decodeErr)
			}
			wantDecoded := map[string]string{"error": "user does not exist", "code": "person_not_in_app"}
			if !reflect.DeepEqual(decoded, wantDecoded) {
				testInstance.Fatalf("body = %v, want %v", decoded, wantDecoded)
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

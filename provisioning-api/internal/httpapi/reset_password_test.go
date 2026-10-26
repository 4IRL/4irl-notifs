package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

func postResetPassword(testInstance *testing.T, fakeService *fakeProvisioningService, rawBody string) (*httptest.ResponseRecorder, *bytes.Buffer) {
	testInstance.Helper()
	var logBuffer bytes.Buffer
	server := newTestServer(ServerConfig{Service: fakeService, Logger: slog.New(slog.NewTextHandler(&logBuffer, nil))})
	request := newAuthedRequest(http.MethodPost, "/v1/reset-password", strings.NewReader(rawBody))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder, &logBuffer
}

func validResetBody() string {
	return fmt.Sprintf(`{"app_id":"myapp","email":%q}`, aliceEmail)
}

func TestResetPasswordHappyPathReturnsPasswordNoStore(testInstance *testing.T) {
	fakeService := &fakeProvisioningService{
		resetPasswordResult: provisioning.ResetPasswordResult{UserID: aliceNtfyUser, AppID: "myapp", Password: "abcd-efgh-ijkm-npqr"},
	}

	recorder, _ := postResetPassword(testInstance, fakeService, validResetBody())

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	wantBody := fmt.Sprintf(`{"user_id":%q,"app_id":"myapp","password":"abcd-efgh-ijkm-npqr"}`, aliceNtfyUser) + "\n"
	if recorder.Body.String() != wantBody {
		testInstance.Fatalf("body = %q, want %q", recorder.Body.String(), wantBody)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		testInstance.Fatalf("Cache-Control = %q, want no-store", cacheControl)
	}
	if len(fakeService.resetPasswordCalls) != 1 || fakeService.resetPasswordCalls[0] != (provisioning.ResetPasswordRequest{AppID: "myapp", Email: aliceEmail}) {
		testInstance.Fatalf("calls = %+v", fakeService.resetPasswordCalls)
	}
}

func TestResetPasswordValidationOrder(testInstance *testing.T) {
	testCases := []struct {
		name      string
		body      string
		wantError string
	}{
		{"invalid json", `{not json`, "invalid JSON body"},
		{"both invalid reports app_id", `{"app_id":"BAD APP","email":"nope"}`, "invalid app_id"},
		{"valid app_id invalid email", `{"app_id":"myapp","email":"nope"}`, "invalid email"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			fakeService := &fakeProvisioningService{}

			recorder, _ := postResetPassword(testInstance, fakeService, testCase.body)

			if recorder.Code != http.StatusBadRequest {
				testInstance.Fatalf("status = %d, want 400", recorder.Code)
			}
			wantBody := fmt.Sprintf(`{"error":%q}`, testCase.wantError) + "\n"
			if recorder.Body.String() != wantBody {
				testInstance.Fatalf("body = %q, want %q", recorder.Body.String(), wantBody)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				testInstance.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
			if len(fakeService.resetPasswordCalls) != 0 {
				testInstance.Fatalf("service must not be called, got %+v", fakeService.resetPasswordCalls)
			}
		})
	}
}

func TestResetPasswordErrorMappingsAreNoStore(testInstance *testing.T) {
	const secretErrText = "connection refused to database at 10.0.0.5:9999 with credential xyz"
	testCases := []struct {
		name           string
		err            error
		wantStatus     int
		wantBody       string
		wantRetryAfter string
		wantLogged     bool
	}{
		{"not found", fmt.Errorf("ntfy user: %w: no such user", ntfycli.ErrNotFound), http.StatusNotFound, `{"error":"user does not exist"}` + "\n", "", false},
		{"true limit", &provisioning.TooManyPasswordChangesError{RetryAfterSeconds: 1234}, http.StatusTooManyRequests, `{"error":"too many password changes"}` + "\n", "1234", false},
		{"outage", &provisioning.TooManyPasswordChangesError{RetryAfterSeconds: 60}, http.StatusTooManyRequests, `{"error":"too many password changes"}` + "\n", "60", false},
		{"wrapped limit", fmt.Errorf("reset: %w", &provisioning.TooManyPasswordChangesError{RetryAfterSeconds: 77}), http.StatusTooManyRequests, `{"error":"too many password changes"}` + "\n", "77", false},
		{"bare sentinel falls back to 60", provisioning.ErrTooManyPasswordChanges, http.StatusTooManyRequests, `{"error":"too many password changes"}` + "\n", "60", false},
		{"zero seconds falls back to 60", &provisioning.TooManyPasswordChangesError{}, http.StatusTooManyRequests, `{"error":"too many password changes"}` + "\n", "60", false},
		{"internal", errors.New(secretErrText), http.StatusInternalServerError, `{"error":"internal error"}` + "\n", "", true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			fakeService := &fakeProvisioningService{resetPasswordErr: testCase.err}

			recorder, logBuffer := postResetPassword(testInstance, fakeService, validResetBody())

			if recorder.Code != testCase.wantStatus {
				testInstance.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			if recorder.Body.String() != testCase.wantBody {
				testInstance.Fatalf("body = %q, want %q", recorder.Body.String(), testCase.wantBody)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				testInstance.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
			if got := recorder.Header().Get("Retry-After"); got != testCase.wantRetryAfter {
				testInstance.Fatalf("Retry-After = %q, want %q", got, testCase.wantRetryAfter)
			}
			if testCase.wantLogged {
				if strings.Contains(recorder.Body.String(), secretErrText) {
					testInstance.Fatalf("response leaked the real error: %s", recorder.Body.String())
				}
				if !strings.Contains(logBuffer.String(), secretErrText) {
					testInstance.Fatalf("log = %q, want the real error text", logBuffer.String())
				}
			}
		})
	}
}

func TestResetPasswordRejectsWrongMethod(testInstance *testing.T) {
	server := newTestServer(ServerConfig{Service: &fakeProvisioningService{}})
	request := newAuthedRequest(http.MethodGet, "/v1/reset-password", nil)
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		testInstance.Fatalf("status = %d, want 405", recorder.Code)
	}
}

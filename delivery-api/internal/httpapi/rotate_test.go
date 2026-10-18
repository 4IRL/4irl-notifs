package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const rotatePath = "/v1/admin/destinations/hook/rotate-secret"

// TestAdminRotateSecretRejectsBadOverlapHours verifies a missing, null or
// out-of-range overlap_hours is 400 "invalid overlap_hours" and a wrong JSON
// type or unknown field is 400 "invalid JSON body"; the registry is never called.
func TestAdminRotateSecretRejectsBadOverlapHours(testInstance *testing.T) {
	testCases := []struct {
		name     string
		body     string
		wantBody string
	}{
		{name: "zero", body: `{"overlap_hours":0}`, wantBody: `{"error":"invalid overlap_hours"}` + "\n"},
		{name: "negative", body: `{"overlap_hours":-1}`, wantBody: `{"error":"invalid overlap_hours"}` + "\n"},
		{name: "169", body: `{"overlap_hours":169}`, wantBody: `{"error":"invalid overlap_hours"}` + "\n"},
		{name: "missing", body: `{}`, wantBody: `{"error":"invalid overlap_hours"}` + "\n"},
		{name: "null", body: `{"overlap_hours":null}`, wantBody: `{"error":"invalid overlap_hours"}` + "\n"},
		{name: "string", body: `{"overlap_hours":"24"}`, wantBody: `{"error":"invalid JSON body"}` + "\n"},
		{name: "float", body: `{"overlap_hours":1.5}`, wantBody: `{"error":"invalid JSON body"}` + "\n"},
		{name: "bool", body: `{"overlap_hours":true}`, wantBody: `{"error":"invalid JSON body"}` + "\n"},
		{name: "unknown field", body: `{"overlap_hours":24,"bogus":1}`, wantBody: `{"error":"invalid JSON body"}` + "\n"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeRegistry{}

			recorder := serveAdmin(fake, nil, http.MethodPost, rotatePath, testCase.body)

			assertResponse(subTest, recorder, http.StatusBadRequest, testCase.wantBody)
			if fake.totalCalls() != 0 {
				subTest.Fatalf("registry was called for a rejected body")
			}
		})
	}
}

// TestAdminRotateSecretBoundariesSucceed verifies overlap_hours 1 and 168 reach
// the registry and return 200.
func TestAdminRotateSecretBoundariesSucceed(testInstance *testing.T) {
	for _, hours := range []string{"1", "168"} {
		testInstance.Run(hours, func(subTest *testing.T) {
			fake := &fakeRegistry{rotateSecretResult: sampleDestinationView(), rotateSecretSecret: sampleRotatedSecret}

			recorder := serveAdmin(fake, nil, http.MethodPost, rotatePath, `{"overlap_hours":`+hours+`}`)

			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
			}
			if len(fake.rotateSecretCalls) != 1 || fake.rotateSecretCalls[0].name != "hook" {
				subTest.Fatalf("calls = %+v", fake.rotateSecretCalls)
			}
		})
	}
}

// TestAdminSecretResponsesAreNoStore verifies the successful rotate-secret
// response (which carries the one-time plaintext secret) and the successful
// create-destination response forbid caching and content sniffing.
func TestAdminSecretResponsesAreNoStore(testInstance *testing.T) {
	testCases := []struct {
		name string
		path string
		body string
	}{
		{name: "rotate secret", path: rotatePath, body: `{"overlap_hours":24}`},
		{name: "create destination", path: "/v1/admin/destinations", body: validCreateDestinationBody},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeRegistry{
				createDestinationResult: sampleDestinationView(),
				rotateSecretResult:      sampleDestinationView(),
				rotateSecretSecret:      sampleRotatedSecret,
			}

			recorder := serveAdmin(fake, nil, http.MethodPost, testCase.path, testCase.body)

			if recorder.Code != http.StatusOK && recorder.Code != http.StatusCreated {
				subTest.Fatalf("status = %d, want a success; body = %s", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				subTest.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

// TestAdminRotateSecretRejectsBadName verifies the path name is validated
// before the body is read.
func TestAdminRotateSecretRejectsBadName(testInstance *testing.T) {
	for _, badName := range []string{"%00", "%ff", "Upper", "has-dash"} {
		testInstance.Run(badName, func(subTest *testing.T) {
			fake := &fakeRegistry{}

			recorder := serveAdmin(fake, nil, http.MethodPost, "/v1/admin/destinations/"+badName+"/rotate-secret", `{not json`)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid name"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("registry was called for a bad destination name")
			}
		})
	}
}

// TestAdminRotateSecretMethods verifies only POST is routed.
func TestAdminRotateSecretMethods(testInstance *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		testInstance.Run(method, func(subTest *testing.T) {
			fake := &fakeRegistry{}

			recorder := serveAdmin(fake, nil, method, rotatePath, validRotateSecretBody)

			if recorder.Code != http.StatusMethodNotAllowed {
				subTest.Fatalf("status = %d, want 405", recorder.Code)
			}
			if fake.totalCalls() != 0 {
				subTest.Fatalf("registry was called for an unsupported method")
			}
		})
	}
}

// TestAdminRotateSecretNeverLogsTheSecret verifies the new secret is returned
// in the body only: not logged on success, and not in an error response or log.
func TestAdminRotateSecretNeverLogsTheSecret(testInstance *testing.T) {
	testInstance.Run("success", func(subTest *testing.T) {
		var logBuffer bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
		fake := &fakeRegistry{rotateSecretResult: sampleDestinationView(), rotateSecretSecret: sampleRotatedSecret}

		recorder := serveAdmin(fake, logger, http.MethodPost, rotatePath, validRotateSecretBody)

		if !strings.Contains(recorder.Body.String(), sampleRotatedSecret) {
			subTest.Fatalf("response body lacks the new secret: %s", recorder.Body.String())
		}
		if strings.Contains(logBuffer.String(), sampleRotatedSecret) {
			subTest.Fatalf("log output leaked the new secret: %s", logBuffer.String())
		}
	})
	testInstance.Run("error", func(subTest *testing.T) {
		var logBuffer bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
		fake := &fakeRegistry{rotateSecretSecret: sampleRotatedSecret, rotateSecretErr: errors.New("boom")}

		recorder := serveAdmin(fake, logger, http.MethodPost, rotatePath, validRotateSecretBody)

		assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
		if strings.Contains(logBuffer.String(), sampleRotatedSecret) {
			subTest.Fatalf("log output leaked the new secret: %s", logBuffer.String())
		}
	})
}

// TestAdminRotateSecretNotFoundAndUnsupported pins the exact error bodies.
func TestAdminRotateSecretNotFoundAndUnsupported(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "not found", err: registry.ErrDestinationNotFound, wantStatus: http.StatusNotFound, wantBody: `{"error":"destination does not exist"}` + "\n"},
		{name: "unsupported", err: registry.ErrRotationUnsupported, wantStatus: http.StatusUnprocessableEntity, wantBody: `{"error":"destination type does not support rotation"}` + "\n"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveAdmin(&fakeRegistry{rotateSecretErr: testCase.err}, nil, http.MethodPost, rotatePath, validRotateSecretBody)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
		})
	}
}

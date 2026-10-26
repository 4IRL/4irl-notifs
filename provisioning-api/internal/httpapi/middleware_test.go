package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/enforcement"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/provisioning"
)

const (
	testInternalCredential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// wrongSameLengthCredential has the configured credential's length but not its value.
	wrongSameLengthCredential = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	// wrongShortCredential is a distinct secret-shaped value of a different length.
	wrongShortCredential = "short-wrong-credential"
)

// newTestServer builds a server in the default (enforce) credential mode with
// the shared test credential configured. The handler tests build their servers
// through it and send their requests through newAuthedRequest, so they run
// against the same credential check production does.
func newTestServer(config ServerConfig) *Server {
	config.InternalCredential = testInternalCredential
	return NewServer(config)
}

// newAuthedRequest is httptest.NewRequest with the shared test credential
// attached in the internal-credential header.
func newAuthedRequest(method string, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set(internalCredentialHeader, testInternalCredential)
	return request
}

// serveWithCredential sends a POST /v1/reset-password to a server configured
// with the given credential and mode, presenting presentedHeader when non-empty.
func serveWithCredential(testInstance *testing.T, fakeService *fakeProvisioningService, configured string, mode enforcement.Mode, presentedHeader string) (*httptest.ResponseRecorder, string) {
	testInstance.Helper()
	var logBuffer bytes.Buffer
	server := NewServer(ServerConfig{
		Service:            fakeService,
		Logger:             slog.New(slog.NewTextHandler(&logBuffer, nil)),
		InternalCredential: configured,
		CredentialMode:     mode,
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/reset-password", strings.NewReader(validResetBody()))
	if presentedHeader != "" {
		request.Header.Set(internalCredentialHeader, presentedHeader)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder, logBuffer.String()
}

func TestInternalCredentialMiddleware(testInstance *testing.T) {
	testCases := []struct {
		name       string
		configured string
		presented  string
		wantReason string // "" means the credential check passes
	}{
		{"correct", testInternalCredential, testInternalCredential, ""},
		{"missing", testInternalCredential, "", credentialReasonMissing},
		{"wrong", testInternalCredential, wrongSameLengthCredential, credentialReasonMismatch},
		{"wrong length", testInternalCredential, wrongShortCredential, credentialReasonMismatch},
		{"credential not configured", "", testInternalCredential, credentialReasonNotConfigured},
		{"credential not configured and missing", "", "", credentialReasonNotConfigured},
	}
	modes := []enforcement.Mode{enforcement.Enforce, enforcement.LogOnly}

	for _, mode := range modes {
		for _, testCase := range testCases {
			testInstance.Run(mode.String()+"/"+testCase.name, func(testInstance *testing.T) {
				fakeService := &fakeProvisioningService{
					resetPasswordResult: provisioning.ResetPasswordResult{UserID: aliceNtfyUser, AppID: "myapp", Password: "abcd-efgh-ijkm-npqr"},
				}

				recorder, logOutput := serveWithCredential(testInstance, fakeService, testCase.configured, mode, testCase.presented)

				wantReached := testCase.wantReason == "" || mode == enforcement.LogOnly
				if reached := len(fakeService.resetPasswordCalls) == 1; reached != wantReached {
					testInstance.Fatalf("handler reached = %v, want %v (calls = %d)", reached, wantReached, len(fakeService.resetPasswordCalls))
				}
				if wantReached {
					if recorder.Code != http.StatusOK {
						testInstance.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
					}
				} else {
					if recorder.Code != http.StatusUnauthorized {
						testInstance.Fatalf("status = %d, want 401", recorder.Code)
					}
					if wantBody := `{"error":"unauthorized"}` + "\n"; recorder.Body.String() != wantBody {
						testInstance.Fatalf("body = %q, want %q", recorder.Body.String(), wantBody)
					}
				}

				wouldReject := strings.Contains(logOutput, "internal credential would reject")
				enforcedReject := strings.Contains(logOutput, "internal credential rejected")
				switch {
				case testCase.wantReason == "":
					if wouldReject || enforcedReject {
						testInstance.Fatalf("correct credential logged a rejection: %s", logOutput)
					}
				case mode == enforcement.LogOnly:
					if !wouldReject || enforcedReject {
						testInstance.Fatalf("log-only must log 'would reject' only: %s", logOutput)
					}
				default:
					if wouldReject || !enforcedReject {
						testInstance.Fatalf("enforce must log 'rejected' only: %s", logOutput)
					}
				}
				if testCase.wantReason != "" {
					for _, want := range []string{"reason=" + testCase.wantReason, "method=POST", "path=/v1/reset-password"} {
						if !strings.Contains(logOutput, want) {
							testInstance.Fatalf("log missing %q: %s", want, logOutput)
						}
					}
				}

				assertNoCredentialLeak(testInstance, "log", logOutput, testCase.configured, testCase.presented)
				assertNoCredentialLeak(testInstance, "response body", recorder.Body.String(), testCase.configured, testCase.presented)
				assertNoCredentialLeak(testInstance, "response headers", recorder.Header().Get(internalCredentialHeader), testCase.configured, testCase.presented)
			})
		}
	}
}

// assertNoCredentialLeak fails when text contains the configured or presented
// credential value (empty values are ignored).
func assertNoCredentialLeak(testInstance *testing.T, label string, text string, secrets ...string) {
	testInstance.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			testInstance.Fatalf("%s leaked a credential value: %q", label, text)
		}
	}
}

func TestZeroValueModeFailsClosed(testInstance *testing.T) {
	var logBuffer bytes.Buffer
	fakeService := &fakeProvisioningService{}
	server := NewServer(ServerConfig{Service: fakeService, Logger: slog.New(slog.NewTextHandler(&logBuffer, nil))})
	request := httptest.NewRequest(http.MethodPost, "/v1/reset-password", strings.NewReader(validResetBody()))
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || len(fakeService.resetPasswordCalls) != 0 {
		testInstance.Fatalf("zero-value config: status = %d, calls = %d; want 401 and no calls", recorder.Code, len(fakeService.resetPasswordCalls))
	}
}

func TestInternalCredentialAppliesToEveryRouteButHealthz(testInstance *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/provision"},
		{http.MethodPost, "/v1/deprovision"},
		{http.MethodGet, "/v1/users"},
		{http.MethodDelete, "/v1/users/someone"},
		{http.MethodPost, "/v1/provision-app"},
		{http.MethodPost, "/v1/deprovision-app"},
		{http.MethodPost, "/v1/test-notify"},
		{http.MethodPost, "/v1/set-password"},
		{http.MethodPost, "/v1/reset-password"},
		{http.MethodHead, "/healthz"},
		{http.MethodGet, "/healthz/"},
		{http.MethodGet, "/nonexistent"},
	}
	for _, route := range routes {
		testInstance.Run(route.method+" "+route.path, func(testInstance *testing.T) {
			var logBuffer bytes.Buffer
			fakeService := &fakeProvisioningService{}
			server := NewServer(ServerConfig{
				Service:            fakeService,
				Logger:             slog.New(slog.NewTextHandler(&logBuffer, nil)),
				InternalCredential: testInternalCredential,
				CredentialMode:     enforcement.Enforce,
			})
			recorder := httptest.NewRecorder()

			server.Handler().ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))

			if recorder.Code != http.StatusUnauthorized {
				testInstance.Fatalf("status = %d, want 401 without a credential", recorder.Code)
			}
			if !reflect.DeepEqual(fakeService, &fakeProvisioningService{}) {
				testInstance.Fatalf("a rejected request reached the service: %+v", fakeService)
			}
		})
	}
}

func TestHealthzIsReachableWithoutCredential(testInstance *testing.T) {
	for _, mode := range []enforcement.Mode{enforcement.Enforce, enforcement.LogOnly} {
		for _, configured := range []string{testInternalCredential, ""} {
			testInstance.Run(fmt.Sprintf("%s/credential configured=%t", mode, configured != ""), func(testInstance *testing.T) {
				var logBuffer bytes.Buffer
				server := NewServer(ServerConfig{
					Service:            &fakeProvisioningService{},
					Logger:             slog.New(slog.NewTextHandler(&logBuffer, nil)),
					InternalCredential: configured,
					CredentialMode:     mode,
				})
				recorder := httptest.NewRecorder()

				server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

				if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" {
					testInstance.Fatalf("status = %d body = %q, want 200 ok", recorder.Code, recorder.Body.String())
				}
				if strings.Contains(logBuffer.String(), "internal credential") {
					testInstance.Fatalf("healthz logged a credential rejection: %s", logBuffer.String())
				}
			})
		}
	}
}

func TestRequestCompletionLog(testInstance *testing.T) {
	testCases := []struct {
		name       string
		presented  string
		wantStatus string
	}{
		{"handled request", testInternalCredential, "status=200"},
		{"rejected request", "", "status=401"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			fakeService := &fakeProvisioningService{
				resetPasswordResult: provisioning.ResetPasswordResult{UserID: aliceNtfyUser, AppID: "myapp", Password: "abcd-efgh-ijkm-npqr"},
			}

			_, logOutput := serveWithCredential(testInstance, fakeService, testInternalCredential, enforcement.Enforce, testCase.presented)

			for _, want := range []string{"request completed", "method=POST", "path=/v1/reset-password", testCase.wantStatus, "duration_ms="} {
				if !strings.Contains(logOutput, want) {
					testInstance.Fatalf("completion log missing %q: %s", want, logOutput)
				}
			}
			// The body carries an email and a password: neither may reach the log.
			for _, forbidden := range []string{aliceEmail, "abcd-efgh-ijkm-npqr", testInternalCredential} {
				if strings.Contains(logOutput, forbidden) {
					testInstance.Fatalf("log leaked %q: %s", forbidden, logOutput)
				}
			}
		})
	}
}

func TestRequestCompletionLogDoesNotRecordQueryValues(testInstance *testing.T) {
	var logBuffer bytes.Buffer
	server := newTestServer(ServerConfig{
		Service: &fakeProvisioningService{},
		Logger:  slog.New(slog.NewTextHandler(&logBuffer, nil)),
	})
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, newAuthedRequest(http.MethodGet, "/v1/users?token=super-secret-query-value", nil))

	if !strings.Contains(logBuffer.String(), "request completed") {
		testInstance.Fatalf("no completion line was logged: %s", logBuffer.String())
	}
	if strings.Contains(logBuffer.String(), "super-secret-query-value") {
		testInstance.Fatalf("completion log recorded a query value: %s", logBuffer.String())
	}
}

func TestHealthzCompletionLog(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		status     int
		wantLogged bool
	}{
		{"successful GET /healthz is not logged", http.MethodGet, "/healthz", http.StatusOK, false},
		{"failing GET /healthz is logged", http.MethodGet, "/healthz", http.StatusServiceUnavailable, true},
		{"HEAD /healthz is logged", http.MethodHead, "/healthz", http.StatusOK, true},
		{"other path is logged", http.MethodGet, "/healthz/other", http.StatusOK, true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			var logBuffer bytes.Buffer
			server := newTestServer(ServerConfig{Logger: slog.New(slog.NewTextHandler(&logBuffer, nil))})
			handler := server.logCompletion(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
				responseWriter.WriteHeader(testCase.status)
			}))

			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(testCase.method, testCase.path, nil))

			if logged := strings.Contains(logBuffer.String(), "request completed"); logged != testCase.wantLogged {
				testInstance.Fatalf("completion line logged = %v, want %v: %s", logged, testCase.wantLogged, logBuffer.String())
			}
		})
	}
}

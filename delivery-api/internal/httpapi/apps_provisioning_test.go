package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

// The production client must satisfy the consumer interface.
var _ AppProvisioning = (*provisionclient.Client)(nil)

const (
	provisionPath   = "/v1/apps/" + keyApp + "/provision"
	deprovisionPath = "/v1/apps/" + keyApp + "/deprovision"

	distinctiveEmail    = "distinctive.person@example.com"
	distinctiveToken    = "tk_distinctivetokenvalue0000000"
	distinctivePassword = "distinctive-password-value"
)

type provisionCall struct {
	appID  string
	email  string
	target provisionclient.DeprovisionTarget

	password string
}

// fakeAppProvisioning records calls and returns a scripted answer or error.
type fakeAppProvisioning struct {
	answer provisionclient.Passthrough
	err    error
	calls  []provisionCall
}

func (fake *fakeAppProvisioning) Provision(
	_ context.Context, appID string, email string,
) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, provisionCall{appID: appID, email: email})
	return fake.answer, fake.err
}

func (fake *fakeAppProvisioning) Deprovision(
	_ context.Context, appID string, target provisionclient.DeprovisionTarget,
) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, provisionCall{appID: appID, target: target})
	return fake.answer, fake.err
}

func (fake *fakeAppProvisioning) SetPassword(
	_ context.Context, appID, email, password string,
) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, provisionCall{appID: appID, email: email, password: password})
	return fake.answer, fake.err
}

func (fake *fakeAppProvisioning) totalCalls() int { return len(fake.calls) }

type provisioningRig struct {
	fake   *fakeAppProvisioning
	logs   *bytes.Buffer
	server *Server
}

func newProvisioningRig(fake *fakeAppProvisioning) *provisioningRig {
	rig := &provisioningRig{fake: fake, logs: &bytes.Buffer{}}
	var provisioning AppProvisioning
	if fake != nil {
		provisioning = fake
	}
	rig.server = NewServer(ServerConfig{
		AppProvisioning: provisioning,
		AppIdentity:     &fakeAppIdentity{resolved: appidentity.ResolvedKey{ID: 4, AppID: keyApp, Hint: "dak_abcd"}},
		Logger:          slog.New(slog.NewTextHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return rig
}

func (rig *provisioningRig) post(path string, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set(authorizationHeader, bearerKey)
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func assertNoStore(testInstance *testing.T, recorder *httptest.ResponseRecorder) {
	testInstance.Helper()
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		testInstance.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestAppProvisioningNilServiceIs503(testInstance *testing.T) {
	for _, path := range []string{provisionPath, deprovisionPath} {
		rig := newProvisioningRig(nil)
		recorder := rig.post(path, `{"email":"a@example.com"}`)
		assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"provisioning unavailable"}`+"\n")
		assertNoStore(testInstance, recorder)
	}
}

func TestAppProvisioningRelaysAnswersWithForcedHeaders(testInstance *testing.T) {
	testCases := []struct {
		name           string
		status         int
		retryAfter     string
		wantRetryAfter string
	}{
		{name: "200", status: http.StatusOK},
		{name: "400", status: http.StatusBadRequest},
		{name: "404", status: http.StatusNotFound},
		{name: "429 numeric retry-after", status: http.StatusTooManyRequests, retryAfter: "30", wantRetryAfter: "30"},
		{name: "429 non-numeric retry-after dropped", status: http.StatusTooManyRequests, retryAfter: "<script>"},
		{name: "429 nine-digit retry-after relayed", status: http.StatusTooManyRequests, retryAfter: "123456789", wantRetryAfter: "123456789"},
		{name: "429 ten-digit retry-after dropped", status: http.StatusTooManyRequests, retryAfter: "1234567890"},
		{name: "429 trailing newline retry-after dropped", status: http.StatusTooManyRequests, retryAfter: "30\n"},
		{name: "429 trailing space retry-after dropped", status: http.StatusTooManyRequests, retryAfter: "30 "},
		{name: "500", status: http.StatusInternalServerError},
		{name: "200 with numeric retry-after copied", status: http.StatusOK, retryAfter: "30", wantRetryAfter: "30"},
	}
	for _, path := range []string{provisionPath, deprovisionPath} {
		for _, testCase := range testCases {
			testInstance.Run(path+"/"+testCase.name, func(subTest *testing.T) {
				fake := &fakeAppProvisioning{answer: provisionclient.Passthrough{
					Status: testCase.status, Body: []byte(`{"upstream":true}`),
					ContentType: "text/html", RetryAfter: testCase.retryAfter,
				}}
				recorder := newProvisioningRig(fake).post(path, `{"email":"a@example.com"}`)

				if recorder.Code != testCase.status || recorder.Body.String() != `{"upstream":true}` {
					subTest.Fatalf("got %d %q", recorder.Code, recorder.Body.String())
				}
				if got := recorder.Header().Get("Content-Type"); got != "application/json" {
					subTest.Fatalf("Content-Type = %q, want forced application/json", got)
				}
				assertNoStore(subTest, recorder)
				values := recorder.Header().Values("Retry-After")
				if testCase.wantRetryAfter == "" && len(values) != 0 {
					subTest.Fatalf("Retry-After = %v, want none", values)
				}
				if testCase.wantRetryAfter != "" && (len(values) != 1 || values[0] != testCase.wantRetryAfter) {
					subTest.Fatalf("Retry-After = %v, want %q", values, testCase.wantRetryAfter)
				}
			})
		}
	}
}

func TestAppProvisioningUpstreamCredentialFailuresBecome502(testInstance *testing.T) {
	for _, path := range []string{provisionPath, deprovisionPath} {
		for _, status := range []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusMovedPermanently, http.StatusFound,
			http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusContinue, 600,
		} {
			testInstance.Run(fmt.Sprintf("%s/%d", path, status), func(subTest *testing.T) {
				fake := &fakeAppProvisioning{answer: provisionclient.Passthrough{
					Status: status, Body: []byte(`{"secret":"upstream"}`), ContentType: "text/html", RetryAfter: "30",
				}}
				rig := newProvisioningRig(fake)
				recorder := rig.post(path, `{"email":"a@example.com"}`)

				assertResponse(subTest, recorder, http.StatusBadGateway, `{"error":"provisioning request failed"}`+"\n")
				assertNoStore(subTest, recorder)
				if len(recorder.Header().Values("Retry-After")) != 0 || strings.Contains(recorder.Body.String(), "upstream") {
					subTest.Fatalf("upstream body or Retry-After leaked: %v %s", recorder.Header(), recorder.Body.String())
				}
				logged := rig.logs.String()
				if !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, fmt.Sprintf("upstream_status=%d", status)) {
					subTest.Fatalf("want an ERROR log naming the upstream status: %s", logged)
				}
			})
		}
	}
}

func TestAppProvisioningRequestValidation(testInstance *testing.T) {
	validHash := personhash.Hash("a@example.com")
	testCases := []struct {
		name    string
		path    string
		body    string
		status  int
		message string
	}{
		{name: "provision empty body email", path: provisionPath, body: `{}`, status: http.StatusBadRequest, message: "email required"},
		{name: "provision empty email", path: provisionPath, body: `{"email":""}`, status: http.StatusBadRequest, message: "email required"},
		{name: "provision body app_id", path: provisionPath, body: `{"email":"a@example.com","app_id":"urls4irl"}`, status: http.StatusBadRequest, message: "invalid JSON body"},
		{name: "provision person_hash", path: provisionPath, body: `{"person_hash":"` + validHash + `"}`, status: http.StatusBadRequest, message: "invalid JSON body"},
		{name: "provision oversize", path: provisionPath, body: `{"email":"` + strings.Repeat("a", maxAdminBodyBytes) + `"}`, status: http.StatusRequestEntityTooLarge, message: "request body too large"},
		{name: "deprovision neither", path: deprovisionPath, body: `{}`, status: http.StatusBadRequest, message: "exactly one of email or person_hash required"},
		{name: "deprovision both", path: deprovisionPath, body: `{"email":"a@example.com","person_hash":"` + validHash + `"}`, status: http.StatusBadRequest, message: "exactly one of email or person_hash required"},
		{name: "deprovision bad hash", path: deprovisionPath, body: `{"person_hash":"NOT-A-HASH"}`, status: http.StatusBadRequest, message: "invalid person_hash"},
		{name: "deprovision body app_id", path: deprovisionPath, body: `{"email":"a@example.com","app_id":"urls4irl"}`, status: http.StatusBadRequest, message: "invalid JSON body"},
		{name: "deprovision oversize", path: deprovisionPath, body: `{"email":"` + strings.Repeat("a", maxAdminBodyBytes) + `"}`, status: http.StatusRequestEntityTooLarge, message: "request body too large"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppProvisioning{}
			recorder := newProvisioningRig(fake).post(testCase.path, testCase.body)

			assertResponse(subTest, recorder, testCase.status, `{"error":"`+testCase.message+`"}`+"\n")
			assertNoStore(subTest, recorder)
			if len(fake.calls) != 0 {
				subTest.Fatalf("upstream called %d times for a refused request", len(fake.calls))
			}
		})
	}
}

func TestAppProvisioningForwardsOwnerAndTarget(testInstance *testing.T) {
	validHash := personhash.Hash("a@example.com")
	okAnswer := provisionclient.Passthrough{Status: http.StatusOK, Body: []byte("{}")}
	testCases := []struct {
		name string
		path string
		body string
		want provisionCall
	}{
		{
			name: "provision", path: provisionPath, body: `{"email":"a@example.com"}`,
			want: provisionCall{appID: keyApp, email: "a@example.com"},
		},
		{
			name: "deprovision by hash", path: deprovisionPath, body: `{"person_hash":"` + validHash + `"}`,
			want: provisionCall{appID: keyApp, target: provisionclient.DeprovisionTarget{UserID: "u_" + validHash}},
		},
		{
			name: "deprovision by email", path: deprovisionPath, body: `{"email":"a@example.com"}`,
			want: provisionCall{appID: keyApp, target: provisionclient.DeprovisionTarget{Email: "a@example.com"}},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppProvisioning{answer: okAnswer}
			recorder := newProvisioningRig(fake).post(testCase.path, testCase.body)

			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			if len(fake.calls) != 1 || fake.calls[0] != testCase.want {
				subTest.Fatalf("calls = %+v, want [%+v]", fake.calls, testCase.want)
			}
		})
	}
}

func TestAppProvisioningErrors(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
		wantLevel  string
	}{
		{name: "not configured", err: provisionclient.ErrNotConfigured, wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"provisioning unavailable"}`, wantLevel: "level=ERROR"},
		{name: "wrapped not configured", err: fmt.Errorf("x: %w", provisionclient.ErrNotConfigured), wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"provisioning unavailable"}`, wantLevel: "level=ERROR"},
		{name: "transport error", err: errors.New("provision: request failed: dial tcp: refused"), wantStatus: http.StatusBadGateway, wantBody: `{"error":"provisioning request failed"}`, wantLevel: "level=ERROR"},
		{name: "timeout", err: context.DeadlineExceeded, wantStatus: http.StatusBadGateway, wantBody: `{"error":"provisioning request failed"}`, wantLevel: "level=ERROR"},
		{name: "canceled", err: context.Canceled, wantStatus: http.StatusBadGateway, wantBody: `{"error":"provisioning request failed"}`, wantLevel: "level=DEBUG"},
	}
	for _, path := range []string{provisionPath, deprovisionPath} {
		for _, testCase := range testCases {
			testInstance.Run(path+"/"+testCase.name, func(subTest *testing.T) {
				rig := newProvisioningRig(&fakeAppProvisioning{err: testCase.err})
				recorder := rig.post(path, `{"email":"a@example.com"}`)

				assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
				assertNoStore(subTest, recorder)
				logged := rig.logs.String()
				if !strings.Contains(logged, testCase.wantLevel) {
					subTest.Fatalf("want %s in log: %s", testCase.wantLevel, logged)
				}
				if testCase.wantLevel == "level=DEBUG" && strings.Contains(logged, "level=ERROR") {
					subTest.Fatalf("a canceled request must not log at ERROR: %s", logged)
				}
			})
		}
	}
}

func TestAppProvisioningLogsNeverCarrySecrets(testInstance *testing.T) {
	upstreamBody := `{"token":"` + distinctiveToken + `","password":"` + distinctivePassword + `"}`
	wantHash := personhash.Hash(distinctiveEmail)
	routes := []struct {
		name string
		path string
		body string
	}{
		{name: "provision by email", path: provisionPath, body: `{"email":"` + distinctiveEmail + `"}`},
		{name: "deprovision by email", path: deprovisionPath, body: `{"email":"` + distinctiveEmail + `"}`},
		{name: "deprovision by person_hash", path: deprovisionPath, body: `{"person_hash":"` + wantHash + `"}`},
	}
	testCases := []struct {
		name    string
		answer  provisionclient.Passthrough
		err     error
		wantLog []string
	}{
		{
			name:    "200",
			answer:  provisionclient.Passthrough{Status: http.StatusOK, Body: []byte(upstreamBody)},
			wantLog: []string{"upstream_status=200"},
		},
		{
			name:    "401",
			answer:  provisionclient.Passthrough{Status: http.StatusUnauthorized, Body: []byte(upstreamBody)},
			wantLog: []string{"upstream_status=401", "level=ERROR"},
		},
		{
			name:    "transport error",
			err:     errors.New("provision: request failed"),
			wantLog: []string{"level=ERROR", "error="},
		},
	}
	for _, route := range routes {
		for _, testCase := range testCases {
			testInstance.Run(route.name+"/"+testCase.name, func(subTest *testing.T) {
				rig := newProvisioningRig(&fakeAppProvisioning{answer: testCase.answer, err: testCase.err})
				rig.post(route.path, route.body)

				logged := rig.logs.String()
				if logged == "" || !strings.Contains(logged, "app_id="+keyApp) ||
					!strings.Contains(logged, "person_hash="+wantHash) {
					subTest.Fatalf("positive control failed, log = %q", logged)
				}
				for _, want := range testCase.wantLog {
					if !strings.Contains(logged, want) {
						subTest.Fatalf("log lacks %q: %s", want, logged)
					}
				}
				for _, secret := range []string{distinctiveEmail, distinctiveToken, distinctivePassword} {
					if strings.Contains(logged, secret) {
						subTest.Fatalf("log leaks %q: %s", secret, logged)
					}
				}
			})
		}
	}
}

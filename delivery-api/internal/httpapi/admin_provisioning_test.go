package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

// The production client must satisfy the operator interface.
var _ AdminProvisioning = (*provisionclient.Client)(nil)

const (
	adminBoundApp   = "urls4irl"
	adminUnboundApp = "ghostapp"
)

type adminCall struct {
	method string
	appID  string
	email  string
	userID string
	notify provisionclient.TestNotifyRequest
}

// fakeAdminProvisioning records calls and returns a scripted answer or error.
type fakeAdminProvisioning struct {
	answer provisionclient.Passthrough
	err    error
	calls  []adminCall
}

func (fake *fakeAdminProvisioning) Provision(_ context.Context, appID, email string) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, adminCall{method: "Provision", appID: appID, email: email})
	return fake.answer, fake.err
}

func (fake *fakeAdminProvisioning) Deprovision(
	_ context.Context, appID string, target provisionclient.DeprovisionTarget,
) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, adminCall{method: "Deprovision", appID: appID, email: target.Email, userID: target.UserID})
	return fake.answer, fake.err
}

func (fake *fakeAdminProvisioning) ResetPassword(_ context.Context, appID, email string) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, adminCall{method: "ResetPassword", appID: appID, email: email})
	return fake.answer, fake.err
}

func (fake *fakeAdminProvisioning) ListUsers(context.Context) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, adminCall{method: "ListUsers"})
	return fake.answer, fake.err
}

func (fake *fakeAdminProvisioning) DeleteUser(_ context.Context, userID string) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, adminCall{method: "DeleteUser", userID: userID})
	return fake.answer, fake.err
}

func (fake *fakeAdminProvisioning) TestNotify(
	_ context.Context, request provisionclient.TestNotifyRequest,
) (provisionclient.Passthrough, error) {
	fake.calls = append(fake.calls, adminCall{method: "TestNotify", notify: request})
	return fake.answer, fake.err
}

// fakeBindingChecker answers bound for the apps in bound and records every lookup.
type fakeBindingChecker struct {
	bound map[string]bool
	err   error
	calls []string
}

func (fake *fakeBindingChecker) HasBinding(_ context.Context, appID string) (bool, error) {
	fake.calls = append(fake.calls, appID)
	return fake.bound[appID], fake.err
}

type adminProvisioningRig struct {
	fake    *fakeAdminProvisioning
	checker *fakeBindingChecker
	limiter *fakeResetLimiter
	logs    *bytes.Buffer
	server  *Server
}

// newAdminProvisioningRig builds a server with the fake (nil fake leaves AdminProvisioning an untyped nil)
// and a checker that binds only adminBoundApp. withChecker=false leaves BindingChecker an untyped nil.
func newAdminProvisioningRig(fake *fakeAdminProvisioning, withChecker bool) *adminProvisioningRig {
	rig := &adminProvisioningRig{
		fake:    fake,
		checker: &fakeBindingChecker{bound: map[string]bool{adminBoundApp: true}},
		limiter: &fakeResetLimiter{},
		logs:    &bytes.Buffer{},
	}
	config := ServerConfig{Logger: slog.New(slog.NewTextHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	if fake != nil {
		config.AdminProvisioning = fake
	}
	if withChecker {
		config.BindingChecker = rig.checker
	}
	rig.server = NewServer(config)
	return rig
}

func (rig *adminProvisioningRig) do(method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

var adminOKAnswer = provisionclient.Passthrough{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}

type adminProvisioningRoute struct {
	name   string
	method string
	path   string
	body   string
	want   adminCall
}

func adminProvisioningRoutes() []adminProvisioningRoute {
	return []adminProvisioningRoute{
		{"provision", http.MethodPost, "/v1/admin/provision", `{"app_id":"urls4irl","email":"a@example.com"}`,
			adminCall{method: "Provision", appID: "urls4irl", email: "a@example.com"}},
		{"reset-password", http.MethodPost, "/v1/admin/reset-password", `{"app_id":"urls4irl","email":"a@example.com"}`,
			adminCall{method: "ResetPassword", appID: "urls4irl", email: "a@example.com"}},
		{"deprovision", http.MethodPost, "/v1/admin/deprovision", `{"app_id":"urls4irl","user_id":"u_abc"}`,
			adminCall{method: "Deprovision", appID: "urls4irl", userID: "u_abc"}},
		{"list-users", http.MethodGet, "/v1/admin/users", ``, adminCall{method: "ListUsers"}},
		{"delete-user", http.MethodDelete, "/v1/admin/users/u_abc", ``, adminCall{method: "DeleteUser", userID: "u_abc"}},
		{"test-notify", http.MethodPost, "/v1/admin/test-notify",
			`{"app_id":"urls4irl","recipients":["a@example.com"],"channel":"general","message":"hi","title":"t"}`,
			adminCall{method: "TestNotify", notify: provisionclient.TestNotifyRequest{
				AppID: "urls4irl", Recipients: []string{"a@example.com"}, Channel: "general", Message: "hi", Title: "t"}}},
	}
}

func TestAdminProvisioningRoutesRelayAndSendExpectedFields(testInstance *testing.T) {
	for _, route := range adminProvisioningRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
			recorder := rig.do(route.method, route.path, route.body)
			assertResponse(subTest, recorder, http.StatusOK, `{"ok":true}`)
			assertNoStore(subTest, recorder)
			if len(rig.fake.calls) != 1 || !reflect.DeepEqual(rig.fake.calls[0], route.want) {
				subTest.Fatalf("calls = %+v, want [%+v]", rig.fake.calls, route.want)
			}
			wantLookups := []string(nil)
			if route.name == "provision" || route.name == "test-notify" {
				wantLookups = []string{adminBoundApp}
			}
			if !reflect.DeepEqual(rig.checker.calls, wantLookups) {
				subTest.Fatalf("binding lookups = %v, want %v", rig.checker.calls, wantLookups)
			}
		})
	}
}

func TestAdminProvisioningNilInterfaceIs503(testInstance *testing.T) {
	for _, route := range adminProvisioningRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(nil, true)
			recorder := rig.do(route.method, route.path, route.body)
			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"provisioning unavailable"}`+"\n")
			assertNoStore(subTest, recorder)
		})
	}
}

func TestAdminProvisioningRelaysUpstreamStatuses(testInstance *testing.T) {
	testCases := []struct {
		name       string
		answer     provisionclient.Passthrough
		wantStatus int
		wantRetry  string
	}{
		{"404", provisionclient.Passthrough{Status: http.StatusNotFound, Body: []byte(`{"error":"x"}`)}, http.StatusNotFound, ""},
		{"429 with retry-after", provisionclient.Passthrough{
			Status: http.StatusTooManyRequests, Body: []byte(`{}`), RetryAfter: "30"}, http.StatusTooManyRequests, "30"},
		{"429 with non-numeric retry-after", provisionclient.Passthrough{
			Status: http.StatusTooManyRequests, Body: []byte(`{}`), RetryAfter: "soon"}, http.StatusTooManyRequests, ""},
		{"upstream 401", provisionclient.Passthrough{Status: http.StatusUnauthorized}, http.StatusBadGateway, ""},
		{"upstream 403", provisionclient.Passthrough{Status: http.StatusForbidden}, http.StatusBadGateway, ""},
		{"upstream 302", provisionclient.Passthrough{Status: http.StatusFound}, http.StatusBadGateway, ""},
	}
	for _, route := range adminProvisioningRoutes() {
		for _, testCase := range testCases {
			testInstance.Run(route.name+"/"+testCase.name, func(subTest *testing.T) {
				rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: testCase.answer}, true)
				recorder := rig.do(route.method, route.path, route.body)
				if recorder.Code != testCase.wantStatus {
					subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
				}
				if got := recorder.Header().Get("Retry-After"); got != testCase.wantRetry {
					subTest.Fatalf("Retry-After = %q, want %q", got, testCase.wantRetry)
				}
			})
		}
	}
}

func TestAdminProvisioningTransportErrorIs502(testInstance *testing.T) {
	for _, route := range adminProvisioningRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{err: errors.New("dial tcp: boom")}, true)
			recorder := rig.do(route.method, route.path, route.body)
			assertResponse(subTest, recorder, http.StatusBadGateway, `{"error":"provisioning request failed"}`+"\n")
		})
	}
}

func TestAdminProvisioningBadBodiesAre400WithNoUpstreamCall(testInstance *testing.T) {
	testCases := []struct {
		name string
		path string
		body string
	}{
		{"provision bad json", "/v1/admin/provision", `{`},
		{"provision unknown field user_id", "/v1/admin/provision", `{"app_id":"urls4irl","email":"a@example.com","user_id":"u_x"}`},
		{"provision empty email", "/v1/admin/provision", `{"app_id":"urls4irl","email":""}`},
		{"provision malformed app", "/v1/admin/provision", `{"app_id":"Bad App","email":"a@example.com"}`},
		{"reset unknown field", "/v1/admin/reset-password", `{"app_id":"urls4irl","email":"a@example.com","x":1}`},
		{"reset empty email", "/v1/admin/reset-password", `{"app_id":"urls4irl","email":""}`},
		{"reset malformed app", "/v1/admin/reset-password", `{"app_id":"","email":"a@example.com"}`},
		{"deprovision email field", "/v1/admin/deprovision", `{"app_id":"urls4irl","email":"a@example.com"}`},
		{"deprovision empty user id", "/v1/admin/deprovision", `{"app_id":"urls4irl","user_id":""}`},
		{"deprovision malformed app", "/v1/admin/deprovision", `{"app_id":"Bad App","user_id":"u_x"}`},
		{"test-notify unknown field", "/v1/admin/test-notify", `{"app_id":"urls4irl","extra":true}`},
		{"test-notify malformed app", "/v1/admin/test-notify", `{"app_id":"Bad App"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
			recorder := rig.do(http.MethodPost, testCase.path, testCase.body)
			if recorder.Code != http.StatusBadRequest {
				subTest.Fatalf("status = %d, want 400 (body %s)", recorder.Code, recorder.Body.String())
			}
			if len(rig.fake.calls) != 0 {
				subTest.Fatalf("upstream calls = %+v, want none", rig.fake.calls)
			}
			if len(rig.checker.calls) != 0 {
				subTest.Fatalf("binding lookups = %v, want none (validation runs first)", rig.checker.calls)
			}
		})
	}
}

func TestAdminProvisioningFormatIsNotCheckedDeliverySide(testInstance *testing.T) {
	rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
	recorder := rig.do(http.MethodPost, "/v1/admin/provision", `{"app_id":"urls4irl","email":"not-an-email"}`)
	assertResponse(testInstance, recorder, http.StatusOK, `{"ok":true}`)
	recorder = rig.do(http.MethodPost, "/v1/admin/deprovision", `{"app_id":"urls4irl","user_id":"weird id"}`)
	assertResponse(testInstance, recorder, http.StatusOK, `{"ok":true}`)
	recorder = rig.do(http.MethodDelete, "/v1/admin/users/weird%20id", "")
	assertResponse(testInstance, recorder, http.StatusOK, `{"ok":true}`)
	if len(rig.fake.calls) != 3 || rig.fake.calls[0].email != "not-an-email" ||
		rig.fake.calls[1].userID != "weird id" || rig.fake.calls[2].userID != "weird id" {
		testInstance.Fatalf("calls = %+v, want the values relayed unchanged", rig.fake.calls)
	}
}

func TestAdminDeleteUserEmptyPathValueIs400(testInstance *testing.T) {
	rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
	request := httptest.NewRequest(http.MethodDelete, "/v1/admin/users/x", nil)
	request.SetPathValue("user_id", "")
	recorder := httptest.NewRecorder()
	rig.server.handleAdminDeleteUser(recorder, request)
	if recorder.Code != http.StatusBadRequest || len(rig.fake.calls) != 0 {
		testInstance.Fatalf("status = %d, calls = %+v, want 400 and none", recorder.Code, rig.fake.calls)
	}
}

func TestAdminUserIDDotSegmentsAre400WithNoCall(testInstance *testing.T) {
	for _, userID := range []string{".", ".."} {
		testInstance.Run("delete-user "+userID, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
			request := httptest.NewRequest(http.MethodDelete, "/v1/admin/users/x", nil)
			request.SetPathValue("user_id", userID)
			recorder := httptest.NewRecorder()
			rig.server.handleAdminDeleteUser(recorder, request)
			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"user_id required"}`+"\n")
			if len(rig.fake.calls) != 0 {
				subTest.Fatalf("calls = %+v, want none", rig.fake.calls)
			}
		})
		testInstance.Run("deprovision "+userID, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
			recorder := rig.do(http.MethodPost, "/v1/admin/deprovision", `{"app_id":"urls4irl","user_id":"`+userID+`"}`)
			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"user_id required"}`+"\n")
			if len(rig.fake.calls) != 0 {
				subTest.Fatalf("calls = %+v, want none", rig.fake.calls)
			}
		})
	}
}

func TestAdminGuardedRoutesRefuseUnboundAppBeforeUpstream(testInstance *testing.T) {
	routes := []struct {
		name string
		path string
		body string
	}{
		{"provision", "/v1/admin/provision", `{"app_id":"ghostapp","email":"a@example.com"}`},
		{"test-notify", "/v1/admin/test-notify", `{"app_id":"ghostapp","recipients":["a@example.com"]}`},
	}
	for _, route := range routes {
		testInstance.Run(route.name+" unbound is 404", func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
			recorder := rig.do(http.MethodPost, route.path, route.body)
			assertResponse(subTest, recorder, http.StatusNotFound, `{"error":"app has no approved binding"}`+"\n")
			if len(rig.fake.calls) != 0 || len(rig.checker.calls) != 1 {
				subTest.Fatalf("upstream calls = %+v, lookups = %v; want none and one", rig.fake.calls, rig.checker.calls)
			}
		})
		testInstance.Run(route.name+" checker error is 503", func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
			rig.checker.err = errors.New("db down")
			recorder := rig.do(http.MethodPost, route.path, route.body)
			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"binding check unavailable"}`+"\n")
			if len(rig.fake.calls) != 0 {
				subTest.Fatalf("upstream calls = %+v, want none", rig.fake.calls)
			}
		})
		testInstance.Run(route.name+" nil checker is 503", func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, false)
			recorder := rig.do(http.MethodPost, route.path, strings.ReplaceAll(route.body, adminUnboundApp, adminBoundApp))
			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"binding check unavailable"}`+"\n")
			if len(rig.fake.calls) != 0 {
				subTest.Fatalf("upstream calls = %+v, want none", rig.fake.calls)
			}
		})
	}
}

func TestAdminUnguardedRoutesWorkForUnboundAppAndNilChecker(testInstance *testing.T) {
	routes := []struct {
		name string
		path string
		body string
	}{
		{"reset-password", "/v1/admin/reset-password", `{"app_id":"ghostapp","email":"a@example.com"}`},
		{"deprovision", "/v1/admin/deprovision", `{"app_id":"ghostapp","user_id":"u_abc"}`},
	}
	for _, route := range routes {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, false)
			recorder := rig.do(http.MethodPost, route.path, route.body)
			assertResponse(subTest, recorder, http.StatusOK, `{"ok":true}`)
			if len(rig.checker.calls) != 0 {
				subTest.Fatalf("binding lookups = %v, want none", rig.checker.calls)
			}
		})
	}
}

func TestAdminTestNotifyForBoundAppIsRelayed(testInstance *testing.T) {
	rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
	recorder := rig.do(http.MethodPost, "/v1/admin/test-notify", `{"app_id":"urls4irl"}`)
	assertResponse(testInstance, recorder, http.StatusOK, `{"ok":true}`)
	if len(rig.fake.calls) != 1 || rig.fake.calls[0].notify.AppID != adminBoundApp {
		testInstance.Fatalf("calls = %+v, want one test-notify for the bound app", rig.fake.calls)
	}
}

// The nil-to-[] normalization lives in provisionclient.TestNotify (pinned by its own test); the handler
// relays a missing "recipients" untouched.
func TestAdminTestNotifyMissingRecipientsIsRelayed(testInstance *testing.T) {
	rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
	rig.do(http.MethodPost, "/v1/admin/test-notify", `{"app_id":"urls4irl"}`)
	if len(rig.fake.calls) != 1 {
		testInstance.Fatalf("calls = %+v, want one", rig.fake.calls)
	}
	if recipients := rig.fake.calls[0].notify.Recipients; len(recipients) != 0 {
		testInstance.Fatalf("Recipients = %#v, want none", recipients)
	}
}

func TestAdminProvisioningOversizeAndTrailingBodies(testInstance *testing.T) {
	oversize := `{"app_id":"urls4irl","email":"` + strings.Repeat("a", maxAdminBodyBytes) + `"}`
	paths := []string{
		"/v1/admin/provision", "/v1/admin/reset-password", "/v1/admin/deprovision", "/v1/admin/test-notify",
	}
	testCases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"oversize", oversize, http.StatusRequestEntityTooLarge},
		{"trailing data", `{"app_id":"urls4irl","email":"a@example.com"}{}`, http.StatusBadRequest},
	}
	for _, path := range paths {
		for _, testCase := range testCases {
			testInstance.Run(path+" "+testCase.name, func(subTest *testing.T) {
				rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
				recorder := rig.do(http.MethodPost, path, testCase.body)
				if recorder.Code != testCase.wantStatus {
					subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
				}
				if len(rig.fake.calls) != 0 || len(rig.checker.calls) != 0 {
					subTest.Fatalf("upstream calls = %+v, lookups = %v; want none", rig.fake.calls, rig.checker.calls)
				}
			})
		}
	}
}

func TestAdminProvisioningWrongMethodIs405(testInstance *testing.T) {
	testCases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/admin/provision"},
		{http.MethodGet, "/v1/admin/reset-password"},
		{http.MethodGet, "/v1/admin/deprovision"},
		{http.MethodGet, "/v1/admin/test-notify"},
		{http.MethodPost, "/v1/admin/users"},
		{http.MethodGet, "/v1/admin/users/u_abc"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.method+" "+testCase.path, func(subTest *testing.T) {
			rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
			recorder := rig.do(testCase.method, testCase.path, "")
			if recorder.Code != http.StatusMethodNotAllowed {
				subTest.Fatalf("status = %d, want 405", recorder.Code)
			}
			if len(rig.fake.calls) != 0 {
				subTest.Fatalf("upstream calls = %+v, want none", rig.fake.calls)
			}
		})
	}
}

func TestAdminDeleteUserEmptySegmentThroughMuxMakesNoCall(testInstance *testing.T) {
	rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
	recorder := rig.do(http.MethodDelete, "/v1/admin/users/", "")
	if recorder.Code == http.StatusOK || len(rig.fake.calls) != 0 {
		testInstance.Fatalf("status = %d, calls = %+v, want a refusal and no upstream call", recorder.Code, rig.fake.calls)
	}
}

func TestAdminResetNeverReservesAndWorksWithoutLimiter(testInstance *testing.T) {
	rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: adminOKAnswer}, true)
	// rig.limiter is not wired into the server (nil ResetLimiter) and must stay unused either way.
	recorder := rig.do(http.MethodPost, "/v1/admin/reset-password", `{"app_id":"urls4irl","email":"a@example.com"}`)
	assertResponse(testInstance, recorder, http.StatusOK, `{"ok":true}`)
	if rig.limiter.totalCalls() != 0 {
		testInstance.Fatalf("Reserve calls = %d, want 0", rig.limiter.totalCalls())
	}
}

func TestAdminProvisioningNeverLogsEmailOrBody(testInstance *testing.T) {
	const (
		distinctiveMessage   = "distinctive-message-body-7f3a"
		distinctiveTitle     = "distinctive-title-body-9c1d"
		distinctiveRecipient = "distinctive-recipient-5b2e@example.com"
		distinctiveUserID    = "u_distinctive-4e8f"
	)
	answer := provisionclient.Passthrough{Status: http.StatusOK, Body: []byte(`{"password":"` + distinctivePassword + `"}`)}
	rig := newAdminProvisioningRig(&fakeAdminProvisioning{answer: answer}, true)
	driveAll := func() {
		rig.do(http.MethodPost, "/v1/admin/provision", `{"app_id":"urls4irl","email":"`+distinctiveEmail+`"}`)
		rig.do(http.MethodPost, "/v1/admin/reset-password", `{"app_id":"urls4irl","email":"`+distinctiveEmail+`"}`)
		rig.do(http.MethodPost, "/v1/admin/deprovision", `{"app_id":"urls4irl","user_id":"`+distinctiveUserID+`"}`)
		rig.do(http.MethodDelete, "/v1/admin/users/"+distinctiveUserID, "")
		for _, appID := range []string{"urls4irl", "ghostapp"} {
			rig.do(http.MethodPost, "/v1/admin/test-notify", `{"app_id":"`+appID+`","recipients":["`+distinctiveRecipient+
				`"],"channel":"general","message":"`+distinctiveMessage+`","title":"`+distinctiveTitle+`"}`)
		}
	}
	driveAll()
	rig.fake.err = errors.New("upstream failed")
	driveAll()
	rig.fake.err = nil
	rig.checker.err = errors.New("db down")
	driveAll()
	logs := rig.logs.String()
	secrets := []string{distinctiveEmail, distinctivePassword, distinctiveMessage, distinctiveTitle, distinctiveRecipient}
	// distinctiveUserID is deliberately not a secret: a failed relay logs the request path, which carries it.
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			testInstance.Fatalf("logs contain %q: %s", secret, logs)
		}
	}
}

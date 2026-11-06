package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appteardown"
)

// The production service must satisfy the handler's interface.
var _ AppTeardown = (*appteardown.Service)(nil)

type fakeAppTeardown struct {
	result appteardown.Result
	err    error
	calls  []string
}

func (fake *fakeAppTeardown) Deprovision(_ context.Context, appID string) (appteardown.Result, error) {
	fake.calls = append(fake.calls, appID)
	return fake.result, fake.err
}

func teardownServer(fake *fakeAppTeardown) *Server {
	config := ServerConfig{}
	if fake != nil {
		config.AppTeardown = fake
	}
	return NewServer(config)
}

// postTeardown posts the confirming body (the path app id repeated).
func postTeardown(server *Server, appID string) *httptest.ResponseRecorder {
	return postTeardownBody(server, appID, `{"app_id":"`+appID+`"}`)
}

func postTeardownBody(server *Server, appID, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/apps/"+appID+"/deprovision", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAdminDeprovisionAppRequiresMatchingBodyAppID(testInstance *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"empty object", `{}`},
		{"different app", `{"app_id":"chores4irl"}`},
		{"unknown field", `{"app_id":"urls4irl","extra":1}`},
		{"not json", `nope`},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppTeardown{}
			recorder := postTeardownBody(teardownServer(fake), "urls4irl", testCase.body)
			if recorder.Code != http.StatusBadRequest || len(fake.calls) != 0 {
				subTest.Fatalf("status = %d calls = %v, want 400 and no calls", recorder.Code, fake.calls)
			}
		})
	}
	fake := &fakeAppTeardown{}
	recorder := postTeardownBody(teardownServer(fake), "urls4irl", `{"app_id":"chores4irl"}`)
	if !strings.Contains(recorder.Body.String(), errMsgTeardownConfirm) {
		testInstance.Fatalf("body = %q, want the fixed confirmation message", recorder.Body.String())
	}
}

func TestAdminDeprovisionAppReturnsTheCascadeResult(testInstance *testing.T) {
	fake := &fakeAppTeardown{result: appteardown.Result{
		AppID: "urls4irl", Removed: true, Unbound: false,
		Deleted: appteardown.Deleted{Checks: 1, Routes: 2, Destinations: 3, OrphanedRoutes: 4, OrphanedChecks: 5, HeldMessages: 6},
	}}

	recorder := postTeardown(teardownServer(fake), "urls4irl")

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want 200", recorder.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		testInstance.Fatalf("body: %v", err)
	}
	want := `{"app_id":"urls4irl","deleted":{"checks":1,"destinations":3,"held_messages":6,"orphaned_checks":5,"orphaned_routes":4,"routes":2},"removed":true,"unbound":false}`
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != want {
		testInstance.Fatalf("body = %s, want %s", gotJSON, want)
	}
	if fmt.Sprint(fake.calls) != "[urls4irl]" {
		testInstance.Fatalf("calls = %v", fake.calls)
	}
	if !strings.Contains(recorder.Header().Get("Cache-Control"), "no-store") {
		testInstance.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestAdminDeprovisionAppInvalidIDIs400WithNoCall(testInstance *testing.T) {
	for _, appID := range []string{"Bad-App", "_platform", "-x", strings.Repeat("a", 64)} {
		fake := &fakeAppTeardown{}
		recorder := postTeardown(teardownServer(fake), appID)
		if recorder.Code != http.StatusBadRequest || len(fake.calls) != 0 {
			testInstance.Fatalf("app %q: status = %d calls = %v, want 400 and no calls", appID, recorder.Code, fake.calls)
		}
	}
}

func TestAdminDeprovisionAppMapsErrorsToFixedMessages(testInstance *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"invalid app id", fmt.Errorf("%w: detail", appteardown.ErrInvalidAppID), http.StatusBadRequest, "invalid app_id"},
		{"busy", fmt.Errorf("%w: lock", appteardown.ErrBusy), http.StatusConflict, "publisher mint already in progress, retry"},
		{"upstream", fmt.Errorf("%w: secret-detail", appteardown.ErrUpstream), http.StatusBadGateway, "provisioning-api teardown failed"},
		{"unavailable", fmt.Errorf("%w: secret-detail", appteardown.ErrUnavailable), http.StatusServiceUnavailable, "app teardown unavailable"},
		{"unclassified fails closed", errors.New("boom secret-detail"), http.StatusServiceUnavailable, "app teardown unavailable"},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := postTeardown(teardownServer(&fakeAppTeardown{err: testCase.err}), "urls4irl")
			if recorder.Code != testCase.status {
				subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.status)
			}
			if !strings.Contains(recorder.Body.String(), testCase.message) || strings.Contains(recorder.Body.String(), "secret-detail") {
				subTest.Fatalf("body = %q, want fixed message %q and no cause", recorder.Body.String(), testCase.message)
			}
		})
	}
}

func TestAdminDeprovisionAppRejectsGet(testInstance *testing.T) {
	fake := &fakeAppTeardown{}
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/apps/x/deprovision", nil)
	recorder := httptest.NewRecorder()

	teardownServer(fake).Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed || len(fake.calls) != 0 {
		testInstance.Fatalf("status = %d calls = %v, want 405 and no calls", recorder.Code, fake.calls)
	}
}

func TestAdminDeprovisionAppNilServiceIs503(testInstance *testing.T) {
	recorder := postTeardown(teardownServer(nil), "urls4irl")

	if recorder.Code != http.StatusServiceUnavailable {
		testInstance.Fatalf("status = %d, want 503", recorder.Code)
	}
}

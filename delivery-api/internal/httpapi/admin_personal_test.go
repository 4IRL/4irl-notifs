package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
)

const adminPersonalPath = "/v1/admin/personal-destinations"

func (fake *fakePersonal) AdminList(_ context.Context, personHash string) ([]personal.AdminView, int, error) {
	fake.record("admin-list", "", 0, personal.PersonRef{PersonHash: personHash})
	fake.adminFilter = personHash
	return fake.adminViews, fake.adminTotal, fake.err
}

func (fake *fakePersonal) AdminSetEnabled(
	_ context.Context, id int64, enabled bool, reason string,
) (personal.AdminView, error) {
	fake.record("admin-set-enabled", "", id, personal.PersonRef{})
	fake.adminEnable = &enabled
	fake.adminReason = reason
	return fake.adminView, fake.err
}

func (fake *fakePersonal) AdminDelete(_ context.Context, id int64) error {
	fake.record("admin-delete", "", id, personal.PersonRef{})
	return fake.err
}

func sampleAdminPersonalView() personal.AdminView {
	moment := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	return personal.AdminView{
		ID: 7, PersonHash: samplePerson, Label: "Phone", Type: personal.TypeDiscord,
		Config: map[string]string{}, Secrets: map[string]string{"webhook_url": "****"}, Enabled: true,
		RegisteredByAppID: keyApp, OptedInAppIDs: []string{keyApp}, CreatedAt: moment, UpdatedAt: moment,
	}
}

// serveAdminPersonal calls an operator route; no app key is sent, as for every /v1/admin route.
func serveAdminPersonal(fake *fakePersonal, method, path, body string) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{PersonalDestinations: fake, Logger: slog.New(slog.NewTextHandler(&strings.Builder{}, nil))})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAdminListPersonalShapeTotalAndFilter(testInstance *testing.T) {
	fake := &fakePersonal{adminViews: []personal.AdminView{sampleAdminPersonalView()}, adminTotal: 250}

	recorder := serveAdminPersonal(fake, http.MethodGet, adminPersonalPath+"?person_hash="+samplePerson, "")

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
	for _, fragment := range []string{
		`"personal_destinations":[{"id":7`, `"total":250`, `"opted_in_app_ids":["` + keyApp + `"]`,
		`"registrar_removed":false`, `"webhook_url":"****"`,
	} {
		if !strings.Contains(recorder.Body.String(), fragment) {
			testInstance.Fatalf("body %q lacks %q", recorder.Body.String(), fragment)
		}
	}
	if fake.adminFilter != samplePerson {
		testInstance.Fatalf("filter = %q, want %q", fake.adminFilter, samplePerson)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		testInstance.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func TestAdminListPersonalEmptyIsNeverNull(testInstance *testing.T) {
	recorder := serveAdminPersonal(&fakePersonal{}, http.MethodGet, adminPersonalPath, "")

	if recorder.Body.String() != `{"personal_destinations":[],"total":0}`+"\n" {
		testInstance.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestAdminListPersonalInvalidFilterIs400(testInstance *testing.T) {
	// The real service owns the validation; here a fake that returns its ValidationError proves the mapping.
	fake := &fakePersonal{err: &personal.ValidationError{Message: "invalid person_hash"}}

	recorder := serveAdminPersonal(fake, http.MethodGet, adminPersonalPath+"?person_hash=nope", "")

	if recorder.Code != http.StatusBadRequest {
		testInstance.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdminPatchPersonal(testInstance *testing.T) {
	disabled := sampleAdminPersonalView()
	disabled.Enabled = false
	testCases := []struct {
		name       string
		path       string
		body       string
		script     func(*fakePersonal)
		wantStatus int
		wantEnable *bool
		wantReason string
		wantCalls  int
	}{
		{
			name: "disable with reason", path: adminPersonalPath + "/7", body: `{"enabled":false,"disabled_reason":"abuse"}`,
			script: func(fake *fakePersonal) { fake.adminView = disabled }, wantStatus: http.StatusOK,
			wantEnable: boolPointer(false), wantReason: "abuse", wantCalls: 1,
		},
		{
			name: "enable", path: adminPersonalPath + "/7", body: `{"enabled":true}`,
			script: func(fake *fakePersonal) { fake.adminView = sampleAdminPersonalView() }, wantStatus: http.StatusOK,
			wantEnable: boolPointer(true), wantCalls: 1,
		},
		{
			name: "absent id", path: adminPersonalPath + "/7", body: `{"enabled":false}`,
			script: func(fake *fakePersonal) { fake.err = personal.ErrNotFound }, wantStatus: http.StatusNotFound,
			wantEnable: boolPointer(false), wantCalls: 1,
		},
		{name: "non-numeric id", path: adminPersonalPath + "/abc", body: `{"enabled":false}`, wantStatus: http.StatusBadRequest},
		{name: "leading zero id", path: adminPersonalPath + "/07", body: `{"enabled":false}`, wantStatus: http.StatusBadRequest},
		{name: "missing enabled", path: adminPersonalPath + "/7", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "unknown field", path: adminPersonalPath + "/7", body: `{"enabled":true,"label":"x"}`, wantStatus: http.StatusBadRequest},
		{
			// The 200-character cap lives in the service (its own tests); here its error maps to 400.
			name: "service validation error maps to 400", path: adminPersonalPath + "/7", body: `{"enabled":false,"disabled_reason":"x"}`,
			script:     func(fake *fakePersonal) { fake.err = &personal.ValidationError{Message: "invalid reason"} },
			wantStatus: http.StatusBadRequest, wantEnable: boolPointer(false), wantReason: "x", wantCalls: 1,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakePersonal{adminView: sampleAdminPersonalView()}
			if testCase.script != nil {
				testCase.script(fake)
			}

			recorder := serveAdminPersonal(fake, http.MethodPatch, testCase.path, testCase.body)

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if len(fake.calls) != testCase.wantCalls {
				subTest.Fatalf("calls = %+v, want %d", fake.calls, testCase.wantCalls)
			}
			if testCase.wantEnable != nil && (fake.adminEnable == nil || *fake.adminEnable != *testCase.wantEnable) {
				subTest.Fatalf("enabled passed = %v, want %v", fake.adminEnable, *testCase.wantEnable)
			}
			if fake.adminReason != testCase.wantReason {
				subTest.Fatalf("reason = %q, want %q", fake.adminReason, testCase.wantReason)
			}
		})
	}
}

func TestAdminDeletePersonal(testInstance *testing.T) {
	testCases := []struct {
		name       string
		path       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "deleted", path: adminPersonalPath + "/7", wantStatus: http.StatusOK, wantBody: `{"id":7,"deleted":true}` + "\n"},
		{name: "absent", path: adminPersonalPath + "/7", err: personal.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "non-numeric id", path: adminPersonalPath + "/x", wantStatus: http.StatusBadRequest},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveAdminPersonal(&fakePersonal{err: testCase.err}, http.MethodDelete, testCase.path, "")

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if testCase.wantBody != "" && recorder.Body.String() != testCase.wantBody {
				subTest.Fatalf("body = %q, want %q", recorder.Body.String(), testCase.wantBody)
			}
		})
	}
}

func TestAdminPersonalRoutesFailClosedWithoutService(testInstance *testing.T) {
	server := NewServer(ServerConfig{})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, adminPersonalPath},
		{http.MethodPatch, adminPersonalPath + "/7"},
		{http.MethodDelete, adminPersonalPath + "/7"},
	} {
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"enabled":false}`))
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			testInstance.Fatalf("%s %s = %d, want 503", route.method, route.path, recorder.Code)
		}
	}
}

func TestAdminDeliveryViewTargetKindAndPersonHash(testInstance *testing.T) {
	testCases := []struct {
		name         string
		deliveryName string
		toRecipient  string
		wantKind     string
		wantPerson   *string
	}{
		{name: "operator destination", deliveryName: "b_hook", wantKind: "destination"},
		{name: "recipient", deliveryName: "@recipient", toRecipient: samplePerson, wantKind: "recipient", wantPerson: stringPointer(samplePerson)},
		{name: "personal", deliveryName: "@personal_7", toRecipient: samplePerson, wantKind: "personal", wantPerson: stringPointer(samplePerson)},
		{name: "unparseable personal", deliveryName: "@personal_07x", toRecipient: samplePerson, wantKind: "personal", wantPerson: stringPointer(samplePerson)},
		{name: "recipient with no stored recipient", deliveryName: "@recipient", wantKind: "recipient"},
		{name: "destination ignores a stray recipient", deliveryName: "b_hook", toRecipient: samplePerson, wantKind: "destination"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{listDeliveriesResult: []message.AdminDelivery{{
				Delivery:    message.Delivery{ID: 1, DestinationName: testCase.deliveryName},
				ToRecipient: testCase.toRecipient,
			}}}

			recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/deliveries", "")

			wantKind := `"target_kind":"` + testCase.wantKind + `"`
			wantPerson := `"person_hash":null`
			if testCase.wantPerson != nil {
				wantPerson = `"person_hash":"` + *testCase.wantPerson + `"`
			}
			for _, fragment := range []string{wantKind, wantPerson} {
				if !strings.Contains(recorder.Body.String(), fragment) {
					subTest.Fatalf("body %q lacks %q", recorder.Body.String(), fragment)
				}
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

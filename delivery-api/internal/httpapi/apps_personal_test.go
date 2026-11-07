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
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	personalBasePath = "/v1/apps/" + keyApp + "/personal-destinations"
	personalNotFound = `{"error":"personal destination does not exist"}` + "\n"
	sampleEmail      = "person@example.com"
	samplePerson     = "abcdefghijklmnop"
)

// personalCall records one call into the fake service.
type personalCall struct {
	method string
	appID  string
	id     int64
	person personal.PersonRef
}

// fakePersonal scripts the personal destination service: every method returns err when set, else view.
type fakePersonal struct {
	view    personal.AppView
	views   []personal.AppView
	deleted bool
	err     error
	// forgetErr fails ForgetPersonForApp / ForgetPerson; with forgetErrOnce it clears after one failure.
	forgetErr     error
	forgetErrOnce bool

	// Operator (admin) scripting; see admin_personal_test.go.
	adminViews  []personal.AdminView
	adminTotal  int
	adminView   personal.AdminView
	adminEnable *bool
	adminReason string
	adminFilter string

	calls       []personalCall
	createInput personal.CreateInput
	patch       personal.Patch
}

func samplePersonalView() personal.AppView {
	moment := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	return personal.AppView{
		ID: 7, PersonHash: samplePerson, Label: "Phone", Type: personal.TypeDiscord,
		Config: map[string]string{}, Secrets: map[string]string{"webhook_url": "****"}, Enabled: true,
		RegisteredByAppID: keyApp, ManagedByCaller: true, OptedIn: true, CreatedAt: moment, UpdatedAt: moment,
	}
}

func (fake *fakePersonal) record(method, appID string, id int64, person personal.PersonRef) {
	fake.calls = append(fake.calls, personalCall{method: method, appID: appID, id: id, person: person})
}

func (fake *fakePersonal) Create(_ context.Context, appID string, input personal.CreateInput) (personal.AppView, error) {
	fake.record("create", appID, 0, input.Person)
	fake.createInput = input
	return fake.view, fake.err
}

func (fake *fakePersonal) Lookup(_ context.Context, appID string, person personal.PersonRef) (string, []personal.AppView, error) {
	fake.record("lookup", appID, 0, person)
	return samplePerson, fake.views, fake.err
}

func (fake *fakePersonal) Update(
	_ context.Context, appID string, id int64, person personal.PersonRef, patch personal.Patch,
) (personal.AppView, error) {
	fake.record("update", appID, id, person)
	fake.patch = patch
	return fake.view, fake.err
}

func (fake *fakePersonal) Delete(_ context.Context, appID string, id int64, person personal.PersonRef) error {
	fake.record("delete", appID, id, person)
	return fake.err
}

func (fake *fakePersonal) OptIn(_ context.Context, appID string, id int64, person personal.PersonRef) (personal.AppView, error) {
	fake.record("opt-in", appID, id, person)
	return fake.view, fake.err
}

func (fake *fakePersonal) OptOut(_ context.Context, appID string, id int64, person personal.PersonRef) (personal.AppView, bool, error) {
	fake.record("opt-out", appID, id, person)
	return fake.view, fake.deleted, fake.err
}

func (fake *fakePersonal) ForgetPersonForApp(_ context.Context, appID, personHash string) error {
	fake.record("forget-person-for-app", appID, 0, personal.PersonRef{PersonHash: personHash})
	if fake.forgetErr != nil {
		err := fake.forgetErr
		if fake.forgetErrOnce {
			fake.forgetErr = nil
		}
		return err
	}
	return nil
}

func (fake *fakePersonal) ForgetPerson(_ context.Context, personHash string) error {
	fake.record("forget-person", "", 0, personal.PersonRef{PersonHash: personHash})
	if fake.forgetErr != nil {
		err := fake.forgetErr
		if fake.forgetErrOnce {
			fake.forgetErr = nil
		}
		return err
	}
	return nil
}

func (fake *fakePersonal) totalCalls() int { return len(fake.calls) }

// personalRig is a Server whose key resolves to keyApp, wired to a scripted fakePersonal.
type personalRig struct {
	fake   *fakePersonal
	logs   *bytes.Buffer
	server *Server
}

func newPersonalRig() *personalRig {
	rig := &personalRig{fake: &fakePersonal{view: samplePersonalView()}, logs: &bytes.Buffer{}}
	rig.server = NewServer(ServerConfig{
		PersonalDestinations: rig.fake,
		AppIdentity:          permissiveAppKeys("", ""),
		Logger:               slog.New(slog.NewTextHandler(rig.logs, nil)),
	})
	return rig
}

func (rig *personalRig) serve(method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	authorizeAsApp(request)
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func personalBodyOf(recorder *httptest.ResponseRecorder) string { return recorder.Body.String() }

func TestPersonalRoutesSuccessShapesAndStatuses(testInstance *testing.T) {
	emailBody := `{"email":"` + sampleEmail + `"}`
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		script     func(*fakePersonal)
		wantStatus int
		wantIn     []string
		wantCall   string
	}{
		{
			name: "create", method: http.MethodPost, path: personalBasePath, wantStatus: http.StatusCreated, wantCall: "create",
			body:   `{"email":"` + sampleEmail + `","label":"Phone","type":"discord","secrets":{"webhook_url":"https://x"}}`,
			wantIn: []string{`"personal_destination":{"id":7`, `"managed_by_caller":true`, `"webhook_url":"****"`},
		},
		{
			name: "lookup with none", method: http.MethodPost, path: personalBasePath + "/lookup", body: emailBody,
			wantStatus: http.StatusOK, wantCall: "lookup",
			wantIn: []string{`"person_hash":"` + samplePerson + `"`, `"personal_destinations":[]`},
		},
		{
			name: "lookup with one", method: http.MethodPost, path: personalBasePath + "/lookup", body: emailBody,
			script:     func(fake *fakePersonal) { fake.views = []personal.AppView{samplePersonalView()} },
			wantStatus: http.StatusOK, wantCall: "lookup", wantIn: []string{`"personal_destinations":[{"id":7`},
		},
		{
			name: "patch", method: http.MethodPatch, path: personalBasePath + "/7", body: `{"email":"` + sampleEmail + `","label":"x"}`,
			wantStatus: http.StatusOK, wantCall: "update", wantIn: []string{`"personal_destination":{"id":7`},
		},
		{
			name: "delete", method: http.MethodDelete, path: personalBasePath + "/7", body: emailBody,
			wantStatus: http.StatusOK, wantCall: "delete", wantIn: []string{`{"id":7,"deleted":true}`},
		},
		{
			name: "opt in", method: http.MethodPut, path: personalBasePath + "/7/opt-in", body: emailBody,
			wantStatus: http.StatusOK, wantCall: "opt-in", wantIn: []string{`"opted_in":true`},
		},
		{
			name: "opt out", method: http.MethodDelete, path: personalBasePath + "/7/opt-in", body: emailBody,
			wantStatus: http.StatusOK, wantCall: "opt-out", wantIn: []string{`"personal_destination":{"id":7`},
		},
		{
			name: "opt out that reaped the row", method: http.MethodDelete, path: personalBasePath + "/7/opt-in", body: emailBody,
			script:     func(fake *fakePersonal) { fake.deleted = true; fake.view = personal.AppView{} },
			wantStatus: http.StatusOK, wantCall: "opt-out", wantIn: []string{`{"id":7,"deleted":true}`},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newPersonalRig()
			if testCase.script != nil {
				testCase.script(rig.fake)
			}

			recorder := rig.serve(testCase.method, testCase.path, testCase.body)

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d; body = %s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			for _, fragment := range testCase.wantIn {
				if !strings.Contains(personalBodyOf(recorder), fragment) {
					subTest.Fatalf("body %q lacks %q", personalBodyOf(recorder), fragment)
				}
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				subTest.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
			if len(rig.fake.calls) != 1 || rig.fake.calls[0].method != testCase.wantCall {
				subTest.Fatalf("calls = %+v, want one %q", rig.fake.calls, testCase.wantCall)
			}
			// The app id is only ever the authenticated one, and the email reaches the service verbatim.
			if rig.fake.calls[0].appID != keyApp || rig.fake.calls[0].person.Email != sampleEmail {
				subTest.Fatalf("call = %+v, want app %q and the body's email", rig.fake.calls[0], keyApp)
			}
			if strings.Contains(rig.logs.String(), sampleEmail) || strings.Contains(personalBodyOf(recorder), sampleEmail) {
				subTest.Fatalf("the email leaked into the log or the response; logs = %s", rig.logs.String())
			}
		})
	}
}

func TestPersonalRoutesPassTheBodyFieldsToTheService(testInstance *testing.T) {
	rig := newPersonalRig()

	rig.serve(http.MethodPost, personalBasePath,
		`{"person_hash":"`+samplePerson+`","label":"L","type":"webhook","config":{"a":"b"},"secrets":{"url":"u"},"opt_in":false}`)
	input := rig.fake.createInput
	if input.Person.PersonHash != samplePerson || input.Label != "L" || input.Type != "webhook" ||
		input.Config["a"] != "b" || input.Secrets["url"] != "u" || input.OptIn == nil || *input.OptIn {
		testInstance.Fatalf("create input = %+v, want every body field passed through (opt_in=false explicit)", input)
	}

	rig.serve(http.MethodPatch, personalBasePath+"/7", `{"email":"`+sampleEmail+`","enabled":false,"label":"x","config":{"c":"d"}}`)
	patch := rig.fake.patch
	if patch.Enabled == nil || *patch.Enabled || patch.Label == nil || *patch.Label != "x" || patch.Config["c"] != "d" {
		testInstance.Fatalf("patch = %+v, want enabled=false, label and config passed through", patch)
	}
}

func TestPersonalRoutesRefuseUnknownFieldsAndForeignAppFields(testInstance *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodPost, personalBasePath},
		{http.MethodPost, personalBasePath + "/lookup"},
		{http.MethodPatch, personalBasePath + "/7"},
		{http.MethodDelete, personalBasePath + "/7"},
		{http.MethodPut, personalBasePath + "/7/opt-in"},
		{http.MethodDelete, personalBasePath + "/7/opt-in"},
	}
	for _, route := range routes {
		for _, extra := range []string{`"bogus":1`, `"app_id":"` + otherApp + `"`} {
			testInstance.Run(route.method+" "+route.path+" "+extra, func(subTest *testing.T) {
				rig := newPersonalRig()

				recorder := rig.serve(route.method, route.path, `{"email":"`+sampleEmail+`",`+extra+`}`)

				assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
				if rig.fake.totalCalls() != 0 {
					subTest.Fatalf("the service was called for an unknown field")
				}
			})
		}
	}
}

func TestPersonalRoutesBothOrNeitherPersonIs400(testInstance *testing.T) {
	// The service owns the rule; a fake that applies it proves the handler passes both fields through.
	for _, body := range []string{
		`{"email":"` + sampleEmail + `","person_hash":"` + samplePerson + `"}`,
		`{}`,
	} {
		rig := newPersonalRig()
		rig.fake.err = &personal.ValidationError{Message: "exactly one of email or person_hash required"}

		recorder := rig.serve(http.MethodPost, personalBasePath+"/lookup", body)

		assertResponse(testInstance, recorder, http.StatusBadRequest,
			`{"error":"exactly one of email or person_hash required"}`+"\n")
		if got := rig.fake.calls[0].person; (got.Email != "") != strings.Contains(body, "email") ||
			(got.PersonHash != "") != strings.Contains(body, "person_hash") {
			testInstance.Fatalf("person ref = %+v for body %s, want both fields passed through unmodified", got, body)
		}
	}
}

func TestWritePersonalErrorMapsEverySentinel(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"not found", personal.ErrNotFound, http.StatusNotFound, personalNotFound},
		{"wrapped not found", fmt.Errorf("get: %w", personal.ErrNotFound), http.StatusNotFound, personalNotFound},
		{"managed by other app", personal.ErrManagedByOtherApp, http.StatusForbidden,
			`{"error":"personal destination is managed by another app"}` + "\n"},
		{"disabled by operator", personal.ErrDisabledByOperator, http.StatusConflict,
			`{"error":"personal destination disabled by the operator"}` + "\n"},
		{"limit reached", personal.ErrLimitReached, http.StatusConflict,
			`{"error":"personal destination limit reached"}` + "\n"},
		{"url refused", &personal.ValidationError{Message: "webhook_url must resolve to a public address"}, http.StatusBadRequest,
			`{"error":"webhook_url must resolve to a public address"}` + "\n"},
		{"validation", &personal.ValidationError{Message: "type must be discord or webhook"}, http.StatusBadRequest,
			`{"error":"type must be discord or webhook"}` + "\n"},
		{"url check unavailable", registry.ErrURLCheckUnavailable, http.StatusServiceUnavailable,
			`{"error":"destination url check unavailable"}` + "\n"},
		{"unexpected", errors.New("dial tcp 10.0.0.5:5432: refused"), http.StatusInternalServerError,
			`{"error":"internal error"}` + "\n"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newPersonalRig()
			rig.fake.err = testCase.err

			recorder := rig.serve(http.MethodPatch, personalBasePath+"/7", `{"email":"`+sampleEmail+`","label":"x"}`)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if strings.Contains(recorder.Body.String(), "10.0.0.5") || strings.Contains(rig.logs.String(), sampleEmail) {
				subTest.Fatalf("an internal detail or the email leaked; logs = %s", rig.logs.String())
			}
		})
	}
}

func TestPersonalMalformedIDIsTheSame404AsAnAbsentRow(testInstance *testing.T) {
	// The absent and other-person legs reach the handler as the same sentinel (the live-stack test compares
	// the real bytes); the handler-owned check is that a malformed id answers the identical 404 without a service call.
	rig := newPersonalRig()
	rig.fake.err = personal.ErrNotFound
	absent := rig.serve(http.MethodDelete, personalBasePath+"/7", `{"email":"`+sampleEmail+`"}`)
	otherPerson := rig.serve(http.MethodDelete, personalBasePath+"/7", `{"person_hash":"`+samplePerson+`"}`)
	rig.fake.calls = nil
	malformed := rig.serve(http.MethodDelete, personalBasePath+"/abc", `{"email":"`+sampleEmail+`"}`)

	if absent.Body.String() != otherPerson.Body.String() || absent.Body.String() != malformed.Body.String() ||
		absent.Body.String() != personalNotFound {
		testInstance.Fatalf("bodies differ: %q %q %q", absent.Body.String(), otherPerson.Body.String(), malformed.Body.String())
	}
	if malformed.Code != http.StatusNotFound || rig.fake.totalCalls() != 0 {
		testInstance.Fatalf("malformed id: status %d, calls %d, want 404 without reaching the service",
			malformed.Code, rig.fake.totalCalls())
	}
}

func TestPersonalPatchOutcomesPassTheViewThrough(testInstance *testing.T) {
	// The no-op, label-applied and operator-409 outcomes are the service's (covered in personal's tests);
	// the handler must return the service's view untouched and map the 409.
	reason := "Maintenance."
	operator := personal.DisabledByOperator
	disabled := samplePersonalView()
	disabled.Enabled = false
	disabled.DisabledBy = &operator
	disabled.DisabledReason = &reason

	rig := newPersonalRig()
	rig.fake.view = disabled
	noOp := rig.serve(http.MethodPatch, personalBasePath+"/7", `{"email":"`+sampleEmail+`","enabled":false}`)
	if noOp.Code != http.StatusOK || !strings.Contains(noOp.Body.String(), `"disabled_by":"operator"`) ||
		!strings.Contains(noOp.Body.String(), `"disabled_reason":"Maintenance."`) ||
		!strings.Contains(noOp.Body.String(), `"updated_at":"2026-11-06T12:00:00Z"`) {
		testInstance.Fatalf("no-op disable: status %d body %s, want 200 with the stored view unchanged", noOp.Code, noOp.Body.String())
	}

	relabelled := disabled
	relabelled.Label = "x"
	relabelled.UpdatedAt = relabelled.UpdatedAt.Add(time.Minute)
	rig.fake.view = relabelled
	applied := rig.serve(http.MethodPatch, personalBasePath+"/7", `{"email":"`+sampleEmail+`","enabled":false,"label":"x"}`)
	if applied.Code != http.StatusOK || !strings.Contains(applied.Body.String(), `"label":"x"`) ||
		!strings.Contains(applied.Body.String(), `"updated_at":"2026-11-06T12:01:00Z"`) {
		testInstance.Fatalf("disable plus label: status %d body %s, want 200 with the new label and bumped updated_at",
			applied.Code, applied.Body.String())
	}

	rig.fake.err = personal.ErrDisabledByOperator
	rejected := rig.serve(http.MethodPatch, personalBasePath+"/7", `{"email":"`+sampleEmail+`","enabled":true,"label":"y"}`)
	assertResponse(testInstance, rejected, http.StatusConflict, `{"error":"personal destination disabled by the operator"}`+"\n")
	// The whole combined body reaches the service, which rejects it atomically (label unchanged is the
	// service's guarantee, covered in the personal package tests: TestEnableRules, TestUpdateWithNoChangeLeavesUpdatedAt).
	if rig.fake.patch.Enabled == nil || !*rig.fake.patch.Enabled || rig.fake.patch.Label == nil || *rig.fake.patch.Label != "y" {
		testInstance.Fatalf("patch = %+v, want enabled=true and label=y forwarded together", rig.fake.patch)
	}
}

func TestPersonalRoutesAnswer503WhenTheServiceIsNotWired(testInstance *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodPost, personalBasePath},
		{http.MethodPost, personalBasePath + "/lookup"},
		{http.MethodPatch, personalBasePath + "/7"},
		{http.MethodDelete, personalBasePath + "/7"},
		{http.MethodPut, personalBasePath + "/7/opt-in"},
		{http.MethodDelete, personalBasePath + "/7/opt-in"},
	}
	for _, route := range routes {
		testInstance.Run(route.method+" "+route.path, func(subTest *testing.T) {
			// Untyped nil, and no Authorization header: the 503 comes before the key check.
			server := NewServer(ServerConfig{AppIdentity: permissiveAppKeys("", "")})
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			recorder := httptest.NewRecorder()

			server.Handler().ServeHTTP(recorder, request)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"personal destinations unavailable"}`+"\n")
			if recorder.Header().Get("Cache-Control") != "no-store" {
				subTest.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}

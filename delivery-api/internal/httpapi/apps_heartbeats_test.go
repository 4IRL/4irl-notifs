package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The real heartbeat service must satisfy the app-scoped consumer-side interface.
var _ AppHeartbeats = (*heartbeat.Service)(nil)

const (
	appChecksPath      = "/v1/apps/" + keyApp + "/heartbeats"
	appCheckPath       = appChecksPath + "/7"
	validAppCheckBody  = `{"name":"nightly_backup","period_seconds":86400,"grace_seconds":7200,"destinations":["ops_discord"]}`
	validAppCheckPatch = `{"period_seconds":3600}`

	errBodyCheckNotFound = `{"error":"check not found"}` + "\n"
	errBodyCheckAppIDNot = `{"error":"app_id must be the app's own id"}` + "\n"
	errBodyCheckImmut    = `{"error":"app_id and name are immutable"}` + "\n"
	errBodyHeartbeatsOff = `{"error":"heartbeats unavailable"}` + "\n"
	errBodyInvalidCheck  = `{"error":"invalid check id"}` + "\n"
	errBodyTooLarge      = `{"error":"request body too large"}` + "\n"
)

type appCheckCreateCall struct {
	appID string
	input heartbeat.AppCheckInput
}

type appCheckIDCall struct {
	appID string
	id    int64
}

type appCheckPatchCall struct {
	appID string
	id    int64
	patch heartbeat.CheckPatch
}

// fakeAppHeartbeats is a scripted AppHeartbeats: every method records its arguments and returns the
// scripted result and error.
type fakeAppHeartbeats struct {
	result     heartbeat.StoredCheck
	listResult []heartbeat.StoredCheck
	err        error

	createCalls []appCheckCreateCall
	listCalls   []string
	getCalls    []appCheckIDCall
	patchCalls  []appCheckPatchCall
	deleteCalls []appCheckIDCall
	pauseCalls  []appCheckIDCall
	resumeCalls []appCheckIDCall
}

func (fake *fakeAppHeartbeats) CreateForApp(
	_ context.Context, appID string, input heartbeat.AppCheckInput,
) (heartbeat.StoredCheck, error) {
	fake.createCalls = append(fake.createCalls, appCheckCreateCall{appID: appID, input: input})
	return fake.result, fake.err
}

func (fake *fakeAppHeartbeats) ListForApp(_ context.Context, appID string) ([]heartbeat.StoredCheck, error) {
	fake.listCalls = append(fake.listCalls, appID)
	return fake.listResult, fake.err
}

func (fake *fakeAppHeartbeats) GetForApp(_ context.Context, appID string, id int64) (heartbeat.StoredCheck, error) {
	fake.getCalls = append(fake.getCalls, appCheckIDCall{appID: appID, id: id})
	return fake.result, fake.err
}

func (fake *fakeAppHeartbeats) PatchForApp(
	_ context.Context, appID string, id int64, patch heartbeat.CheckPatch,
) (heartbeat.StoredCheck, error) {
	fake.patchCalls = append(fake.patchCalls, appCheckPatchCall{appID: appID, id: id, patch: patch})
	return fake.result, fake.err
}

func (fake *fakeAppHeartbeats) DeleteForApp(_ context.Context, appID string, id int64) error {
	fake.deleteCalls = append(fake.deleteCalls, appCheckIDCall{appID: appID, id: id})
	return fake.err
}

func (fake *fakeAppHeartbeats) PauseForApp(_ context.Context, appID string, id int64) (heartbeat.StoredCheck, error) {
	fake.pauseCalls = append(fake.pauseCalls, appCheckIDCall{appID: appID, id: id})
	return fake.result, fake.err
}

func (fake *fakeAppHeartbeats) ResumeForApp(_ context.Context, appID string, id int64) (heartbeat.StoredCheck, error) {
	fake.resumeCalls = append(fake.resumeCalls, appCheckIDCall{appID: appID, id: id})
	return fake.result, fake.err
}

func (fake *fakeAppHeartbeats) totalCalls() int {
	return len(fake.createCalls) + len(fake.listCalls) + len(fake.getCalls) + len(fake.patchCalls) +
		len(fake.deleteCalls) + len(fake.pauseCalls) + len(fake.resumeCalls)
}

// sampleAppCheck is sampleCheck owned by the calling app.
func sampleAppCheck() heartbeat.StoredCheck {
	check := sampleCheck()
	owner := keyApp
	check.OwnerKind, check.OwnerAppID = registry.OwnerApp, &owner
	return check
}

const sampleAppCheckJSON = `{"id":7,"app_id":"urls4irl","name":"nightly_backup","period_seconds":86400,"grace_seconds":7200,` +
	`"paused":false,"state":"up","last_ping_at":"2026-10-16T12:00:00Z","last_ping_kind":"success","down_since":null,` +
	`"deadline_at":"2026-10-17T14:00:00Z","destinations":[{"name":"ops_discord","type":"discord","enabled":true}],` +
	`"owner_kind":"app","owner_app_id":"urls4irl","created_at":"2026-10-14T12:00:00Z","updated_at":"2026-10-16T12:00:00Z"}`

func allAppCheckRoutes() []appRoute {
	return []appRoute{
		{name: "create", method: http.MethodPost, path: appChecksPath, body: validAppCheckBody},
		{name: "list", method: http.MethodGet, path: appChecksPath},
		{name: "get", method: http.MethodGet, path: appCheckPath},
		{name: "patch", method: http.MethodPatch, path: appCheckPath, body: validAppCheckPatch},
		{name: "delete", method: http.MethodDelete, path: appCheckPath},
		{name: "pause", method: http.MethodPost, path: appCheckPath + "/pause"},
		{name: "resume", method: http.MethodPost, path: appCheckPath + "/resume"},
	}
}

// appCheckRig is a Server wired to a scripted AppHeartbeats and an identity fake that resolves every key to
// keyApp, on the fixed sampleNow clock.
type appCheckRig struct {
	identity *fakeAppIdentity
	logs     *strings.Builder
	server   *Server
}

func newAppCheckRig(checks *fakeAppHeartbeats) *appCheckRig {
	rig := &appCheckRig{
		identity: &fakeAppIdentity{resolved: appidentity.ResolvedKey{ID: 4, AppID: keyApp, Hint: "dak_abcd"}},
		logs:     &strings.Builder{},
	}
	var appHeartbeats AppHeartbeats
	if checks != nil {
		appHeartbeats = checks
	}
	rig.server = NewServer(ServerConfig{
		AppHeartbeats: appHeartbeats,
		AppIdentity:   rig.identity,
		Logger:        slog.New(slog.NewTextHandler(rig.logs, nil)),
		Now:           func() time.Time { return sampleNow },
	})
	return rig
}

func (rig *appCheckRig) serve(method, path, body, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set(authorizationHeader, authorization)
	}
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAppCheckEndpointsHappyPaths(testInstance *testing.T) {
	testCases := []struct {
		route      appRoute
		wantStatus int
		wantBody   string
	}{
		{route: allAppCheckRoutes()[0], wantStatus: http.StatusCreated, wantBody: `{"check":` + sampleAppCheckJSON + "}\n"},
		{route: allAppCheckRoutes()[1], wantStatus: http.StatusOK, wantBody: `{"checks":[` + sampleAppCheckJSON + "]}\n"},
		{route: allAppCheckRoutes()[2], wantStatus: http.StatusOK, wantBody: `{"check":` + sampleAppCheckJSON + "}\n"},
		{route: allAppCheckRoutes()[3], wantStatus: http.StatusOK, wantBody: `{"check":` + sampleAppCheckJSON + "}\n"},
		{route: allAppCheckRoutes()[4], wantStatus: http.StatusOK, wantBody: `{"id":7,"deleted":true}` + "\n"},
		{route: allAppCheckRoutes()[5], wantStatus: http.StatusOK, wantBody: `{"check":` + sampleAppCheckJSON + "}\n"},
		{route: allAppCheckRoutes()[6], wantStatus: http.StatusOK, wantBody: `{"check":` + sampleAppCheckJSON + "}\n"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.route.name, func(subTest *testing.T) {
			check := sampleAppCheck()
			rig := newAppCheckRig(&fakeAppHeartbeats{result: check, listResult: []heartbeat.StoredCheck{check}})

			recorder := rig.serve(testCase.route.method, testCase.route.path, testCase.route.body, bearerKey)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
		})
	}
}

func TestAppCheckEndpointsReachTheServiceWithTheKeysAppAndTheDecodedFields(testInstance *testing.T) {
	checks := &fakeAppHeartbeats{result: sampleAppCheck()}
	rig := newAppCheckRig(checks)

	rig.serve(http.MethodPost, appChecksPath,
		`{"app_id":"urls4irl","name":"nightly_backup","period_seconds":60,"grace_seconds":30,"destinations":["a","b"]}`, bearerKey)
	rig.serve(http.MethodGet, appChecksPath, "", bearerKey)
	rig.serve(http.MethodGet, appCheckPath, "", bearerKey)
	rig.serve(http.MethodPatch, appCheckPath,
		`{"app_id":"urls4irl","period_seconds":120,"grace_seconds":60,"destinations":["c"]}`, bearerKey)
	rig.serve(http.MethodDelete, appCheckPath, "", bearerKey)
	rig.serve(http.MethodPost, appCheckPath+"/pause", "", bearerKey)
	rig.serve(http.MethodPost, appCheckPath+"/resume", "", bearerKey)

	wantCreate := []appCheckCreateCall{{appID: keyApp, input: heartbeat.AppCheckInput{
		Name: "nightly_backup", PeriodSeconds: 60, GraceSeconds: 30, DestinationNames: []string{"a", "b"},
	}}}
	if !reflect.DeepEqual(checks.createCalls, wantCreate) {
		testInstance.Fatalf("create calls = %+v, want %+v", checks.createCalls, wantCreate)
	}
	if !reflect.DeepEqual(checks.listCalls, []string{keyApp}) {
		testInstance.Fatalf("list calls = %v", checks.listCalls)
	}
	wantID := []appCheckIDCall{{appID: keyApp, id: 7}}
	for name, got := range map[string][]appCheckIDCall{
		"get": checks.getCalls, "delete": checks.deleteCalls, "pause": checks.pauseCalls, "resume": checks.resumeCalls,
	} {
		if !reflect.DeepEqual(got, wantID) {
			testInstance.Fatalf("%s calls = %+v, want %+v", name, got, wantID)
		}
	}
	period, grace := 120, 60
	wantPatch := []appCheckPatchCall{{appID: keyApp, id: 7, patch: heartbeat.CheckPatch{
		PeriodSeconds: &period, GraceSeconds: &grace, DestinationNames: []string{"c"},
	}}}
	if !reflect.DeepEqual(checks.patchCalls, wantPatch) {
		testInstance.Fatalf("patch calls = %+v, want %+v", checks.patchCalls, wantPatch)
	}
}

// A check's body app_id may be omitted, empty or the app's own; anything else is a 400 that never reaches the
// service, on create and on PATCH alike.
func TestAppCheckBodyAppIDMustBeTheCallersOwn(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name: "create foreign app_id", method: http.MethodPost, path: appChecksPath,
			body:       `{"app_id":"chores4irl","name":"n","period_seconds":60,"grace_seconds":30,"destinations":["d"]}`,
			wantStatus: http.StatusBadRequest, wantBody: errBodyCheckAppIDNot,
		},
		{
			name: "create wildcard app_id", method: http.MethodPost, path: appChecksPath,
			body:       `{"app_id":"*","name":"n","period_seconds":60,"grace_seconds":30,"destinations":["d"]}`,
			wantStatus: http.StatusBadRequest, wantBody: errBodyCheckAppIDNot,
		},
		{
			name: "create non-string app_id", method: http.MethodPost, path: appChecksPath,
			body:       `{"app_id":5,"name":"n","period_seconds":60,"grace_seconds":30,"destinations":["d"]}`,
			wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSON,
		},
		{
			name: "patch foreign app_id", method: http.MethodPatch, path: appCheckPath,
			body: `{"app_id":"chores4irl","period_seconds":60}`, wantStatus: http.StatusBadRequest, wantBody: errBodyCheckAppIDNot,
		},
		{
			name: "patch non-string app_id", method: http.MethodPatch, path: appCheckPath,
			body: `{"app_id":5}`, wantStatus: http.StatusBadRequest, wantBody: errBodyCheckAppIDNot,
		},
		{
			name: "patch name", method: http.MethodPatch, path: appCheckPath,
			body: `{"name":"other"}`, wantStatus: http.StatusBadRequest, wantBody: errBodyCheckImmut,
		},
		{
			name: "patch null name", method: http.MethodPatch, path: appCheckPath,
			body: `{"name":null}`, wantStatus: http.StatusBadRequest, wantBody: errBodyCheckImmut,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			checks := &fakeAppHeartbeats{result: sampleAppCheck()}

			recorder := newAppCheckRig(checks).serve(testCase.method, testCase.path, testCase.body, bearerKey)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if checks.totalCalls() != 0 {
				subTest.Fatalf("the service was called for a body the handler refuses")
			}
		})
	}
	for _, accepted := range []string{`{}`, `{"app_id":""}`, `{"app_id":null}`, `{"app_id":"urls4irl"}`} {
		checks := &fakeAppHeartbeats{result: sampleAppCheck()}

		recorder := newAppCheckRig(checks).serve(http.MethodPatch, appCheckPath, accepted, bearerKey)

		if recorder.Code != http.StatusOK || len(checks.patchCalls) != 1 {
			testInstance.Fatalf("PATCH %s: status = %d, calls = %d; want 200 and one call", accepted, recorder.Code, len(checks.patchCalls))
		}
	}
	for _, accepted := range []string{`"app_id":""`, `"app_id":"urls4irl"`, `"app_id":null`} {
		checks := &fakeAppHeartbeats{result: sampleAppCheck()}
		body := `{` + accepted + `,"name":"n","period_seconds":60,"grace_seconds":30,"destinations":["d"]}`

		recorder := newAppCheckRig(checks).serve(http.MethodPost, appChecksPath, body, bearerKey)

		if recorder.Code != http.StatusCreated || len(checks.createCalls) != 1 {
			testInstance.Fatalf("POST %s: status = %d, calls = %d; want 201 and one call", accepted, recorder.Code, len(checks.createCalls))
		}
	}
}

// An app-scoped check body names no owner: sending one is an unknown-field 400 that never reaches the service.
func TestAppCheckEndpointsNeverAcceptAnOwnerFromTheBody(testInstance *testing.T) {
	for _, body := range []string{
		`{"name":"n","period_seconds":60,"grace_seconds":30,"destinations":["d"],"owner_kind":"operator"}`,
		`{"name":"n","period_seconds":60,"grace_seconds":30,"destinations":["d"],"owner_app_id":"chores4irl"}`,
	} {
		checks := &fakeAppHeartbeats{}

		recorder := newAppCheckRig(checks).serve(http.MethodPost, appChecksPath, body, bearerKey)

		assertResponse(testInstance, recorder, http.StatusBadRequest, errBodyInvalidJSON)
		if checks.totalCalls() != 0 {
			testInstance.Fatalf("the service was called for a body that names an owner: %s", body)
		}
	}
	checks := &fakeAppHeartbeats{}
	recorder := newAppCheckRig(checks).serve(http.MethodPatch, appCheckPath, `{"owner_kind":"operator"}`, bearerKey)
	assertResponse(testInstance, recorder, http.StatusBadRequest, errBodyInvalidJSON)
	if checks.totalCalls() != 0 {
		testInstance.Fatalf("the service was called for a patch that names an owner")
	}
}

func TestAppCheckEndpointsRequireTheAppKey(testInstance *testing.T) {
	for _, route := range allAppCheckRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			for _, authorization := range []string{"", "Basic abc", "Bearer", "Bearer not-a-key"} {
				checks := &fakeAppHeartbeats{}
				recorder := newAppCheckRig(checks).serve(route.method, route.path, route.body, authorization)

				assertUnauthorized(subTest, recorder, "app key required")
				if checks.totalCalls() != 0 {
					subTest.Fatalf("the service was called without a key (header %q)", authorization)
				}
			}

			checks := &fakeAppHeartbeats{}
			rig := newAppCheckRig(checks)
			rig.identity.resolveErr = appidentity.ErrInvalidKey
			assertUnauthorized(subTest, rig.serve(route.method, route.path, route.body, bearerKey), "invalid app key")
			if checks.totalCalls() != 0 {
				subTest.Fatalf("the service was called with a key the store refused")
			}
		})
	}
}

func TestAppCheckEndpointsRefuseAnotherAppsPath(testInstance *testing.T) {
	for _, route := range allAppCheckRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			checks := &fakeAppHeartbeats{}
			path := strings.Replace(route.path, "/v1/apps/"+keyApp+"/", "/v1/apps/"+otherApp+"/", 1)

			recorder := newAppCheckRig(checks).serve(route.method, path, route.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusForbidden, errBodyIdentityMismatch)
			if checks.totalCalls() != 0 {
				subTest.Fatalf("the service was called for another app's path")
			}
		})
	}
}

func TestAppCheckEndpointsFailClosed(testInstance *testing.T) {
	for _, route := range allAppCheckRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			// No AppHeartbeats configured.
			recorder := newAppCheckRig(nil).serve(route.method, route.path, route.body, bearerKey)
			assertResponse(subTest, recorder, http.StatusServiceUnavailable, errBodyHeartbeatsOff)

			// No identity service configured.
			noIdentity := NewServer(ServerConfig{AppHeartbeats: &fakeAppHeartbeats{}})
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			request.Header.Set(authorizationHeader, bearerKey)
			recorder = httptest.NewRecorder()
			noIdentity.Handler().ServeHTTP(recorder, request)
			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
		})
	}
}

func TestAppCheckHandlersAnswer503WhenReachedWithoutAnAuthenticatedApp(testInstance *testing.T) {
	handlers := map[string]func(*Server) http.HandlerFunc{
		"create": func(server *Server) http.HandlerFunc { return server.handleAppCreateCheck },
		"list":   func(server *Server) http.HandlerFunc { return server.handleAppListChecks },
		"get":    func(server *Server) http.HandlerFunc { return server.handleAppGetCheck },
		"patch":  func(server *Server) http.HandlerFunc { return server.handleAppUpdateCheck },
		"delete": func(server *Server) http.HandlerFunc { return server.handleAppDeleteCheck },
		"pause":  func(server *Server) http.HandlerFunc { return server.handleAppPauseCheck },
		"resume": func(server *Server) http.HandlerFunc { return server.handleAppResumeCheck },
	}
	for name, handlerOf := range handlers {
		checks := &fakeAppHeartbeats{}
		server := NewServer(ServerConfig{AppHeartbeats: checks})
		request := httptest.NewRequest(http.MethodGet, appCheckPath, strings.NewReader("{}"))
		request.SetPathValue("id", "7")
		recorder := httptest.NewRecorder()

		handlerOf(server)(recorder, request)

		assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
		if checks.totalCalls() != 0 {
			testInstance.Fatalf("%s: the service was called with no authenticated app", name)
		}
	}
}

func TestAppCheckEndpointsMapServiceErrors(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "not found", err: heartbeat.ErrCheckNotFound, wantStatus: http.StatusNotFound, wantBody: errBodyCheckNotFound},
		{name: "wrapped not found", err: fmt.Errorf("wrap: %w", heartbeat.ErrCheckNotFound), wantStatus: http.StatusNotFound, wantBody: errBodyCheckNotFound},
		{name: "exists", err: heartbeat.ErrCheckExists, wantStatus: http.StatusConflict, wantBody: `{"error":"check already exists"}` + "\n"},
		{name: "unknown destination", err: registry.ErrUnknownDestination, wantStatus: http.StatusBadRequest, wantBody: errBodyUnknownDest},
		{name: "check violation", err: registry.ErrCheckViolation, wantStatus: http.StatusBadRequest, wantBody: errBodyConstraint},
		{
			name: "validation", err: &heartbeat.ValidationError{Message: "invalid period_seconds"},
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid period_seconds"}` + "\n",
		},
	}
	for _, testCase := range testCases {
		for _, route := range allAppCheckRoutes() {
			if route.name == "list" && errors.Is(testCase.err, heartbeat.ErrCheckNotFound) {
				continue
			}
			testInstance.Run(testCase.name+"/"+route.name, func(subTest *testing.T) {
				rig := newAppCheckRig(&fakeAppHeartbeats{err: testCase.err})

				recorder := rig.serve(route.method, route.path, route.body, bearerKey)

				assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			})
		}
	}
}

// A database CHECK the Go validators missed is a 400 with a constant message, and the Warn line is how an
// operator finds the drift: the response never carries the error text.
func TestAppCheckConstraintViolationIsLoggedAndTheResponseStaysConstant(testInstance *testing.T) {
	const constraintDetail = "insert heartbeat_check: heartbeat_check_owner_app_id_matches"
	for _, route := range allAppCheckRoutes() {
		rig := newAppCheckRig(&fakeAppHeartbeats{err: fmt.Errorf("%s: %w", constraintDetail, registry.ErrCheckViolation)})

		recorder := rig.serve(route.method, route.path, route.body, bearerKey)

		assertResponse(testInstance, recorder, http.StatusBadRequest, errBodyConstraint)
		logged := rig.logs.String()
		if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "heartbeat request violated a data constraint") {
			testInstance.Fatalf("%s: the constraint violation was not logged at Warn:\n%s", route.name, logged)
		}
		if !strings.Contains(logged, constraintDetail) || !strings.Contains(logged, route.path) {
			testInstance.Fatalf("%s: the log lacks the error text or the path:\n%s", route.name, logged)
		}
		if strings.Contains(recorder.Body.String(), constraintDetail) {
			testInstance.Fatalf("%s: the response leaked the constraint detail: %s", route.name, recorder.Body.String())
		}
	}
}

func TestAppCheckEndpointsMapAGenericErrorTo500WithoutLeakingIt(testInstance *testing.T) {
	const secretErrText = "connection refused to database at 10.0.0.5:9999 with credential tk_secret_xyz"
	for _, route := range allAppCheckRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAppCheckRig(&fakeAppHeartbeats{err: errors.New(secretErrText)})

			recorder := rig.serve(route.method, route.path, route.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if strings.Contains(recorder.Body.String(), "tk_secret_xyz") {
				subTest.Fatalf("the response leaked the error text: %s", recorder.Body.String())
			}
		})
	}
}

func TestAppCheckEndpointsRejectBadInputBeforeTheService(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{name: "malformed json", method: http.MethodPost, path: appChecksPath, body: `{`, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSON},
		{name: "trailing data", method: http.MethodPatch, path: appCheckPath, body: `{} {}`, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSON},
		{
			name: "oversized create", method: http.MethodPost, path: appChecksPath,
			body:       `{"name":"` + strings.Repeat("a", maxAdminBodyBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge, wantBody: errBodyTooLarge,
		},
		{
			name: "oversized patch", method: http.MethodPatch, path: appCheckPath,
			body:       `{"destinations":["` + strings.Repeat("a", maxAdminBodyBytes) + `"]}`,
			wantStatus: http.StatusRequestEntityTooLarge, wantBody: errBodyTooLarge,
		},
		{name: "zero id", method: http.MethodGet, path: appChecksPath + "/0", wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidCheck},
		{name: "signed id", method: http.MethodDelete, path: appChecksPath + "/+7", wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidCheck},
		{name: "leading zero id", method: http.MethodPost, path: appChecksPath + "/07/pause", wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidCheck},
		{name: "non-numeric id", method: http.MethodPost, path: appChecksPath + "/abc/resume", wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidCheck},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			checks := &fakeAppHeartbeats{}

			recorder := newAppCheckRig(checks).serve(testCase.method, testCase.path, testCase.body, bearerKey)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if checks.totalCalls() != 0 {
				subTest.Fatalf("the service was called for a request the handler refuses")
			}
		})
	}
}

func TestAppCheckListIsNeverNull(testInstance *testing.T) {
	recorder := newAppCheckRig(&fakeAppHeartbeats{listResult: nil}).serve(http.MethodGet, appChecksPath, "", bearerKey)

	assertResponse(testInstance, recorder, http.StatusOK, `{"checks":[]}`+"\n")
}

func TestAppCheckResponsesAreNotCacheable(testInstance *testing.T) {
	for _, route := range allAppCheckRoutes() {
		recorder := newAppCheckRig(&fakeAppHeartbeats{result: sampleAppCheck()}).serve(route.method, route.path, route.body, bearerKey)

		if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			testInstance.Fatalf("%s: headers = %v, want no-store and nosniff", route.name, recorder.Header())
		}
	}
}

func TestAppCheckEndpointsRejectUnsupportedMethods(testInstance *testing.T) {
	rig := newAppCheckRig(&fakeAppHeartbeats{})
	for _, request := range []struct{ method, path string }{
		{http.MethodPut, appCheckPath},
		{http.MethodDelete, appChecksPath},
		{http.MethodPatch, appChecksPath},
		{http.MethodGet, appCheckPath + "/pause"},
		{http.MethodDelete, appCheckPath + "/resume"},
	} {
		recorder := rig.serve(request.method, request.path, "", bearerKey)
		if recorder.Code != http.StatusMethodNotAllowed {
			testInstance.Fatalf("%s %s status = %d, want 405", request.method, request.path, recorder.Code)
		}
	}
}

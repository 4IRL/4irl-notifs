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

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The real registry service must satisfy the app-scoped consumer-side interface.
var _ AppRegistry = (*registry.Service)(nil)

// fakeAppRegistry is a scripted AppRegistry. Each method returns its <name>Result and <name>Err and records
// its arguments in <name>Calls.
type fakeAppRegistry struct {
	createResult registry.DestinationView
	createErr    error
	createCalls  []appCreateCall

	listResult []registry.DestinationView
	listErr    error
	listCalls  []string

	getResult registry.DestinationView
	getErr    error
	getCalls  []appNameCall

	updateResult registry.DestinationView
	updateErr    error
	updateCalls  []appUpdateCall

	deleteErr   error
	deleteCalls []appNameCall

	rotateResult registry.DestinationView
	rotateSecret string
	rotateErr    error
	rotateCalls  []appRotateCall

	routeResult      registry.RouteView
	routeListResult  []registry.RouteView
	routeErr         error
	routeCreateCalls []appRouteCreateCall
	routeListCalls   []string
	routeGetCalls    []appRouteIDCall
	routeUpdateCalls []appRouteUpdateCall
	routeDeleteCalls []appRouteIDCall
}

type appCreateCall struct {
	appID string
	input registry.DestinationInput
}

type appNameCall struct{ appID, name string }

type appUpdateCall struct {
	appID, name string
	patch       registry.DestinationPatch
}

type appRotateCall struct {
	appID, name  string
	overlapHours int
}

func (fake *fakeAppRegistry) CreateAppDestination(
	_ context.Context, appID string, input registry.DestinationInput,
) (registry.DestinationView, error) {
	fake.createCalls = append(fake.createCalls, appCreateCall{appID: appID, input: input})
	return fake.createResult, fake.createErr
}

func (fake *fakeAppRegistry) ListAppDestinations(_ context.Context, appID string) ([]registry.DestinationView, error) {
	fake.listCalls = append(fake.listCalls, appID)
	return fake.listResult, fake.listErr
}

func (fake *fakeAppRegistry) GetAppDestination(_ context.Context, appID, name string) (registry.DestinationView, error) {
	fake.getCalls = append(fake.getCalls, appNameCall{appID: appID, name: name})
	return fake.getResult, fake.getErr
}

func (fake *fakeAppRegistry) UpdateAppDestination(
	_ context.Context, appID, name string, patch registry.DestinationPatch,
) (registry.DestinationView, error) {
	fake.updateCalls = append(fake.updateCalls, appUpdateCall{appID: appID, name: name, patch: patch})
	return fake.updateResult, fake.updateErr
}

func (fake *fakeAppRegistry) DeleteAppDestination(_ context.Context, appID, name string) error {
	fake.deleteCalls = append(fake.deleteCalls, appNameCall{appID: appID, name: name})
	return fake.deleteErr
}

func (fake *fakeAppRegistry) RotateAppWebhookSecret(
	_ context.Context, appID, name string, overlapHours int,
) (registry.DestinationView, string, error) {
	fake.rotateCalls = append(fake.rotateCalls, appRotateCall{appID: appID, name: name, overlapHours: overlapHours})
	return fake.rotateResult, fake.rotateSecret, fake.rotateErr
}

func (fake *fakeAppRegistry) totalCalls() int {
	return len(fake.createCalls) + len(fake.listCalls) + len(fake.getCalls) + len(fake.updateCalls) +
		len(fake.deleteCalls) + len(fake.rotateCalls) + len(fake.routeCreateCalls) + len(fake.routeListCalls) +
		len(fake.routeGetCalls) + len(fake.routeUpdateCalls) + len(fake.routeDeleteCalls)
}

// failingAppRegistry returns a fakeAppRegistry whose every method fails with err.
func failingAppRegistry(err error) *fakeAppRegistry {
	return &fakeAppRegistry{
		createErr: err, listErr: err, getErr: err, updateErr: err, deleteErr: err, rotateErr: err, routeErr: err,
	}
}

type appRouteCreateCall struct {
	appID string
	input registry.RouteInput
}

type appRouteIDCall struct {
	appID string
	id    int64
}

type appRouteUpdateCall struct {
	appID string
	id    int64
	patch registry.RoutePatch
}

func (fake *fakeAppRegistry) CreateAppRoute(
	_ context.Context, appID string, input registry.RouteInput,
) (registry.RouteView, error) {
	fake.routeCreateCalls = append(fake.routeCreateCalls, appRouteCreateCall{appID: appID, input: input})
	return fake.routeResult, fake.routeErr
}

func (fake *fakeAppRegistry) ListAppRoutes(_ context.Context, appID string) ([]registry.RouteView, error) {
	fake.routeListCalls = append(fake.routeListCalls, appID)
	return fake.routeListResult, fake.routeErr
}

func (fake *fakeAppRegistry) GetAppRoute(_ context.Context, appID string, id int64) (registry.RouteView, error) {
	fake.routeGetCalls = append(fake.routeGetCalls, appRouteIDCall{appID: appID, id: id})
	return fake.routeResult, fake.routeErr
}

func (fake *fakeAppRegistry) UpdateAppRoute(
	_ context.Context, appID string, id int64, patch registry.RoutePatch,
) (registry.RouteView, error) {
	fake.routeUpdateCalls = append(fake.routeUpdateCalls, appRouteUpdateCall{appID: appID, id: id, patch: patch})
	return fake.routeResult, fake.routeErr
}

func (fake *fakeAppRegistry) DeleteAppRoute(_ context.Context, appID string, id int64) error {
	fake.routeDeleteCalls = append(fake.routeDeleteCalls, appRouteIDCall{appID: appID, id: id})
	return fake.routeErr
}

func sampleAppDestinationView() registry.DestinationView {
	owner := keyApp
	return registry.DestinationView{
		Name:      keyApp + "_hook",
		Type:      "webhook",
		Enabled:   true,
		Config:    map[string]string{},
		Secrets:   map[string]string{"url": "https://hooks.example.com/****", "signing_secret": "****abcd"},
		OwnerKind: registry.OwnerApp, OwnerAppID: &owner,
		CreatedAt: adminTimestamp,
		UpdatedAt: adminTimestamp,
	}
}

const sampleAppDestinationJSON = `{"name":"urls4irl_hook","type":"webhook","enabled":true,"disabled_reason":null,` +
	`"config":{},"secrets":{"signing_secret":"****abcd","url":"https://hooks.example.com/****"},` +
	`"owner_kind":"app","owner_app_id":"urls4irl",` +
	`"created_at":"` + adminTimestampJSON + `","updated_at":"` + adminTimestampJSON + `"}`

const (
	appDestinationsPath      = "/v1/apps/" + keyApp + "/destinations"
	appDestinationPath       = appDestinationsPath + "/" + keyApp + "_hook"
	validAppCreateBody       = `{"name":"urls4irl_hook","type":"webhook","secrets":{"url":"https://hooks.example.com/in","signing_secret":"` + sampleSigningSecret + `"}}`
	sampleSigningSecret      = "ssssssssssssssssssssssssssssssssssssssss"
	validAppPatchBody        = `{"enabled":false}`
	validAppRotateBody       = `{"overlap_hours":24}`
	errBodyNotFound          = `{"error":"destination does not exist"}` + "\n"
	errBodyIdentityMismatch  = `{"error":"app_id does not match app key"}` + "\n"
	errBodyRegistryNotUp     = `{"error":"registry unavailable"}` + "\n"
	errBodyConstraint        = `{"error":"request violates a data constraint"}` + "\n"
	errBodyURLCheckNotUp     = `{"error":"destination url check unavailable"}` + "\n"
	errBodyInvalidOverlapHrs = `{"error":"invalid overlap_hours"}` + "\n"
)

// appRoute is one app-scoped endpoint with a valid request body.
type appRoute struct {
	name   string
	method string
	path   string
	body   string
}

func allAppRoutes() []appRoute {
	return []appRoute{
		{name: "create", method: http.MethodPost, path: appDestinationsPath, body: validAppCreateBody},
		{name: "list", method: http.MethodGet, path: appDestinationsPath},
		{name: "get", method: http.MethodGet, path: appDestinationPath},
		{name: "patch", method: http.MethodPatch, path: appDestinationPath, body: validAppPatchBody},
		{name: "delete", method: http.MethodDelete, path: appDestinationPath},
		{name: "rotate", method: http.MethodPost, path: appDestinationPath + "/rotate-secret", body: validAppRotateBody},
	}
}

// appRig is a Server wired to a scripted AppRegistry and an identity fake that resolves every key to keyApp.
type appRig struct {
	apps     *fakeAppRegistry
	identity *fakeAppIdentity
	logs     *strings.Builder
	server   *Server
}

func newAppRig(apps *fakeAppRegistry) *appRig {
	rig := &appRig{
		apps:     apps,
		identity: &fakeAppIdentity{resolved: appidentity.ResolvedKey{ID: 4, AppID: keyApp, Hint: "dak_abcd"}},
		logs:     &strings.Builder{},
	}
	var appRegistry AppRegistry
	if apps != nil {
		appRegistry = apps
	}
	rig.server = NewServer(ServerConfig{
		AppRegistry: appRegistry,
		AppIdentity: rig.identity,
		Logger:      slog.New(slog.NewTextHandler(rig.logs, nil)),
	})
	return rig
}

func (rig *appRig) serve(method, path, body, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set(authorizationHeader, authorization)
	}
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAppRoutesHappyPaths(testInstance *testing.T) {
	view := sampleAppDestinationView()
	secret := "SECRETsecretSECRETsecretSECRETsecret12345678"
	testCases := []struct {
		route      appRoute
		wantStatus int
		wantBody   string
	}{
		{route: allAppRoutes()[0], wantStatus: http.StatusCreated, wantBody: `{"destination":` + sampleAppDestinationJSON + "}\n"},
		{route: allAppRoutes()[1], wantStatus: http.StatusOK, wantBody: `{"destinations":[` + sampleAppDestinationJSON + "]}\n"},
		{route: allAppRoutes()[2], wantStatus: http.StatusOK, wantBody: `{"destination":` + sampleAppDestinationJSON + "}\n"},
		{route: allAppRoutes()[3], wantStatus: http.StatusOK, wantBody: `{"destination":` + sampleAppDestinationJSON + "}\n"},
		{route: allAppRoutes()[4], wantStatus: http.StatusOK, wantBody: `{"name":"urls4irl_hook","deleted":true}` + "\n"},
		{
			route: allAppRoutes()[5], wantStatus: http.StatusOK,
			wantBody: `{"destination":` + sampleAppDestinationJSON + `,"signing_secret":"` + secret + `"}` + "\n",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.route.name, func(subTest *testing.T) {
			rig := newAppRig(&fakeAppRegistry{
				createResult: view, listResult: []registry.DestinationView{view}, getResult: view,
				updateResult: view, rotateResult: view, rotateSecret: secret,
			})

			recorder := rig.serve(testCase.route.method, testCase.route.path, testCase.route.body, bearerKey)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
		})
	}
}

func TestAppRoutesReachTheServiceWithTheKeysAppAndTheDecodedFields(testInstance *testing.T) {
	apps := &fakeAppRegistry{
		createResult: sampleAppDestinationView(), getResult: sampleAppDestinationView(),
		updateResult: sampleAppDestinationView(), rotateResult: sampleAppDestinationView(),
	}
	rig := newAppRig(apps)

	rig.serve(http.MethodPost, appDestinationsPath,
		`{"name":"urls4irl_hook","type":"webhook","enabled":false,"config":{"a":"b"},"secrets":{"url":"u"}}`, bearerKey)
	rig.serve(http.MethodGet, appDestinationsPath, "", bearerKey)
	rig.serve(http.MethodGet, appDestinationPath, "", bearerKey)
	rig.serve(http.MethodPatch, appDestinationPath, `{"enabled":true,"config":{"topic":"t"},"secrets":{"url":"n"}}`, bearerKey)
	rig.serve(http.MethodDelete, appDestinationPath, "", bearerKey)
	rig.serve(http.MethodPost, appDestinationPath+"/rotate-secret", `{"overlap_hours":48}`, bearerKey)

	disabled, enabled := false, true
	wantCreate := []appCreateCall{{appID: keyApp, input: registry.DestinationInput{
		Name: "urls4irl_hook", Type: "webhook", Enabled: &disabled,
		Config: map[string]string{"a": "b"}, Secrets: map[string]string{"url": "u"},
	}}}
	if !reflect.DeepEqual(apps.createCalls, wantCreate) {
		testInstance.Fatalf("create calls = %+v, want %+v", apps.createCalls, wantCreate)
	}
	if !reflect.DeepEqual(apps.listCalls, []string{keyApp}) {
		testInstance.Fatalf("list calls = %v", apps.listCalls)
	}
	wantName := []appNameCall{{appID: keyApp, name: "urls4irl_hook"}}
	if !reflect.DeepEqual(apps.getCalls, wantName) || !reflect.DeepEqual(apps.deleteCalls, wantName) {
		testInstance.Fatalf("get calls = %+v, delete calls = %+v, want %+v", apps.getCalls, apps.deleteCalls, wantName)
	}
	wantUpdate := []appUpdateCall{{appID: keyApp, name: "urls4irl_hook", patch: registry.DestinationPatch{
		Enabled: &enabled, Config: map[string]string{"topic": "t"}, Secrets: map[string]string{"url": "n"},
	}}}
	if !reflect.DeepEqual(apps.updateCalls, wantUpdate) {
		testInstance.Fatalf("update calls = %+v, want %+v", apps.updateCalls, wantUpdate)
	}
	wantRotate := []appRotateCall{{appID: keyApp, name: "urls4irl_hook", overlapHours: 48}}
	if !reflect.DeepEqual(apps.rotateCalls, wantRotate) {
		testInstance.Fatalf("rotate calls = %+v, want %+v", apps.rotateCalls, wantRotate)
	}
}

func TestAppRoutesNeverAcceptAnOwnerFromTheBody(testInstance *testing.T) {
	for _, body := range []string{
		`{"name":"urls4irl_hook","type":"webhook","owner_kind":"operator"}`,
		`{"name":"urls4irl_hook","type":"webhook","owner_app_id":"chores4irl"}`,
		`{"name":"urls4irl_hook","type":"webhook","app_id":"chores4irl"}`,
	} {
		apps := &fakeAppRegistry{}
		rig := newAppRig(apps)

		recorder := rig.serve(http.MethodPost, appDestinationsPath, body, bearerKey)

		assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
		if apps.totalCalls() != 0 {
			testInstance.Fatalf("the service was called for a body that names an owner: %s", body)
		}
	}
	rig := newAppRig(&fakeAppRegistry{})
	recorder := rig.serve(http.MethodPatch, appDestinationPath, `{"owner_kind":"operator"}`, bearerKey)
	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
}

func TestAppRoutesRequireTheAppKey(testInstance *testing.T) {
	for _, route := range allAppRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			for _, authorization := range []string{"", "Basic abc", "Bearer", "Bearer not-a-key"} {
				apps := &fakeAppRegistry{}
				rig := newAppRig(apps)

				recorder := rig.serve(route.method, route.path, route.body, authorization)

				assertUnauthorized(subTest, recorder, "app key required")
				if apps.totalCalls() != 0 {
					subTest.Fatalf("the service was called without a key (header %q)", authorization)
				}
			}

			apps := &fakeAppRegistry{}
			rig := newAppRig(apps)
			rig.identity.resolveErr = appidentity.ErrInvalidKey
			recorder := rig.serve(route.method, route.path, route.body, bearerKey)
			assertUnauthorized(subTest, recorder, "invalid app key")
			if apps.totalCalls() != 0 {
				subTest.Fatalf("the service was called with a key the store refused")
			}
		})
	}
}

func TestAppRoutesRefuseAnotherAppsPath(testInstance *testing.T) {
	for _, route := range allAppRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			apps := &fakeAppRegistry{}
			rig := newAppRig(apps)
			path := strings.Replace(route.path, "/v1/apps/"+keyApp+"/", "/v1/apps/"+otherApp+"/", 1)

			recorder := rig.serve(route.method, path, route.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusForbidden, errBodyIdentityMismatch)
			if apps.totalCalls() != 0 {
				subTest.Fatalf("the service was called for another app's path")
			}
		})
	}
}

func TestAppRoutesFailClosed(testInstance *testing.T) {
	for _, route := range allAppRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			// No AppRegistry configured.
			recorder := newAppRig(nil).serve(route.method, route.path, route.body, bearerKey)
			assertResponse(subTest, recorder, http.StatusServiceUnavailable, errBodyRegistryNotUp)

			// No identity service configured.
			noIdentity := NewServer(ServerConfig{AppRegistry: &fakeAppRegistry{}})
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			request.Header.Set(authorizationHeader, bearerKey)
			recorder = httptest.NewRecorder()
			noIdentity.Handler().ServeHTTP(recorder, request)
			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
		})
	}
}

func TestAppHandlersAnswer503WhenReachedWithoutAnAuthenticatedApp(testInstance *testing.T) {
	handlers := map[string]func(*Server) http.HandlerFunc{
		"create": func(server *Server) http.HandlerFunc { return server.handleAppCreateDestination },
		"list":   func(server *Server) http.HandlerFunc { return server.handleAppListDestinations },
		"get":    func(server *Server) http.HandlerFunc { return server.handleAppGetDestination },
		"patch":  func(server *Server) http.HandlerFunc { return server.handleAppUpdateDestination },
		"delete": func(server *Server) http.HandlerFunc { return server.handleAppDeleteDestination },
		"rotate": func(server *Server) http.HandlerFunc { return server.handleAppRotateSecret },
	}
	for name, handlerOf := range handlers {
		apps := &fakeAppRegistry{}
		server := NewServer(ServerConfig{AppRegistry: apps})
		request := httptest.NewRequest(http.MethodGet, appDestinationPath, strings.NewReader("{}"))
		request.SetPathValue("name", "urls4irl_hook")
		recorder := httptest.NewRecorder()

		handlerOf(server)(recorder, request)

		assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
		if apps.totalCalls() != 0 {
			testInstance.Fatalf("%s: the service was called with no authenticated app", name)
		}
	}
}

func TestAppRoutesMapServiceErrors(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "validation error", err: &registry.ValidationError{Message: "name must start with urls4irl_"},
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"name must start with urls4irl_"}` + "\n",
		},
		{
			name: "ssrf refusal", err: &registry.ValidationError{Message: "secrets.url must resolve to public addresses"},
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"secrets.url must resolve to public addresses"}` + "\n",
		},
		{name: "not found", err: registry.ErrDestinationNotFound, wantStatus: http.StatusNotFound, wantBody: errBodyNotFound},
		{
			name: "exists", err: registry.ErrDestinationExists,
			wantStatus: http.StatusConflict, wantBody: `{"error":"destination already exists"}` + "\n",
		},
		{
			name: "in use", err: registry.ErrDestinationInUse,
			wantStatus: http.StatusConflict, wantBody: `{"error":"destination is in use"}` + "\n",
		},
		{
			name: "rotation unsupported", err: registry.ErrRotationUnsupported,
			wantStatus: http.StatusUnprocessableEntity, wantBody: `{"error":"destination type does not support rotation"}` + "\n",
		},
		{name: "check violation", err: registry.ErrCheckViolation, wantStatus: http.StatusBadRequest, wantBody: errBodyConstraint},
		{
			name: "url check unavailable", err: registry.ErrURLCheckUnavailable,
			wantStatus: http.StatusServiceUnavailable, wantBody: errBodyURLCheckNotUp,
		},
		{
			name: "wrapped not found", err: fmt.Errorf("wrap: %w", registry.ErrDestinationNotFound),
			wantStatus: http.StatusNotFound, wantBody: errBodyNotFound,
		},
	}
	for _, testCase := range testCases {
		for _, route := range allAppRoutes() {
			// A list never reports a per-destination error other than a store failure; the rest all do.
			if route.name == "list" && errors.Is(testCase.err, registry.ErrDestinationNotFound) {
				continue
			}
			testInstance.Run(testCase.name+"/"+route.name, func(subTest *testing.T) {
				rig := newAppRig(failingAppRegistry(testCase.err))

				recorder := rig.serve(route.method, route.path, route.body, bearerKey)

				assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			})
		}
	}
}

func TestAppRoutesMapAGenericErrorTo500WithoutLeakingIt(testInstance *testing.T) {
	const secretErrText = "connection refused to database at 10.0.0.5:9999 with credential tk_secret_xyz"
	for _, route := range allAppRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAppRig(failingAppRegistry(errors.New(secretErrText)))

			recorder := rig.serve(route.method, route.path, route.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if strings.Contains(recorder.Body.String(), "tk_secret_xyz") {
				subTest.Fatalf("the response leaked the error text: %s", recorder.Body.String())
			}
			if !strings.Contains(rig.logs.String(), secretErrText) {
				subTest.Fatalf("the error was not logged: %s", rig.logs.String())
			}
		})
	}
}

func TestAppRoutesRejectBadInputBeforeTheService(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name: "malformed json", method: http.MethodPost, path: appDestinationsPath, body: `{`,
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid JSON body"}` + "\n",
		},
		{
			name: "trailing data", method: http.MethodPatch, path: appDestinationPath, body: `{} {}`,
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid JSON body"}` + "\n",
		},
		{
			name: "oversized", method: http.MethodPost, path: appDestinationsPath,
			body:       `{"name":"` + strings.Repeat("a", maxAdminBodyBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge, wantBody: `{"error":"request body too large"}` + "\n",
		},
		{
			name: "invalid name", method: http.MethodGet, path: appDestinationsPath + "/BAD%20NAME",
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid name"}` + "\n",
		},
		{
			name: "missing overlap", method: http.MethodPost, path: appDestinationPath + "/rotate-secret", body: `{}`,
			wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidOverlapHrs,
		},
		{
			name: "zero overlap", method: http.MethodPost, path: appDestinationPath + "/rotate-secret", body: `{"overlap_hours":0}`,
			wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidOverlapHrs,
		},
		{
			name: "huge overlap", method: http.MethodPost, path: appDestinationPath + "/rotate-secret", body: `{"overlap_hours":169}`,
			wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidOverlapHrs,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			apps := &fakeAppRegistry{}
			rig := newAppRig(apps)

			recorder := rig.serve(testCase.method, testCase.path, testCase.body, bearerKey)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if apps.totalCalls() != 0 {
				subTest.Fatalf("the service was called for a request the handler refuses")
			}
		})
	}
}

func TestAppListIsNeverNull(testInstance *testing.T) {
	rig := newAppRig(&fakeAppRegistry{listResult: nil})

	recorder := rig.serve(http.MethodGet, appDestinationsPath, "", bearerKey)

	assertResponse(testInstance, recorder, http.StatusOK, `{"destinations":[]}`+"\n")
}

func TestAppResponsesAreNotCacheable(testInstance *testing.T) {
	for _, route := range allAppRoutes() {
		rig := newAppRig(&fakeAppRegistry{})

		recorder := rig.serve(route.method, route.path, route.body, bearerKey)

		if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			testInstance.Fatalf("%s: headers = %v, want no-store and nosniff", route.name, recorder.Header())
		}
	}
}

func TestAppRoutesRejectUnsupportedMethods(testInstance *testing.T) {
	rig := newAppRig(&fakeAppRegistry{})
	for _, request := range []struct{ method, path string }{
		{http.MethodPut, appDestinationPath},
		{http.MethodDelete, appDestinationsPath},
		{http.MethodPatch, appDestinationsPath},
		{http.MethodGet, appDestinationPath + "/rotate-secret"},
	} {
		recorder := rig.serve(request.method, request.path, "", bearerKey)
		if recorder.Code != http.StatusMethodNotAllowed {
			testInstance.Fatalf("%s %s status = %d, want 405", request.method, request.path, recorder.Code)
		}
	}
}

func TestAppRoutesLogsNeverContainRequestSecrets(testInstance *testing.T) {
	rig := newAppRig(failingAppRegistry(errors.New("boom")))

	rig.serve(http.MethodPost, appDestinationsPath, validAppCreateBody, bearerKey)
	rig.serve(http.MethodPost, appDestinationsPath, `{"secrets":{"signing_secret":"`+sampleSigningSecret+`"},"bogus":1}`, bearerKey)

	if strings.Contains(rig.logs.String(), sampleSigningSecret) || strings.Contains(rig.logs.String(), "hooks.example.com") {
		testInstance.Fatalf("the log leaked a request secret: %s", rig.logs.String())
	}
}

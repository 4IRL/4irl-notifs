package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	appRoutesPath      = "/v1/apps/" + keyApp + "/routes"
	appRoutePath       = appRoutesPath + "/7"
	validAppRouteBody  = `{"channel_pattern":"backup_*","destinations":["urls4irl_hook"]}`
	validAppRoutePatch = `{"enabled":false}`

	errBodyRouteNotFound = `{"error":"route does not exist"}` + "\n"
	errBodyPatternNotOwn = `{"error":"app_pattern must be the app's own id"}` + "\n"
	errBodyInvalidRoute  = `{"error":"invalid route id"}` + "\n"
	errBodyUnknownDest   = `{"error":"unknown destination"}` + "\n"
	errBodyInvalidJSON   = `{"error":"invalid JSON body"}` + "\n"
)

func sampleAppRouteView() registry.RouteView {
	owner := keyApp
	return registry.RouteView{
		ID:             7,
		AppPattern:     keyApp,
		ChannelPattern: "backup_*",
		Destinations:   []string{"urls4irl_hook"},
		Enabled:        true,
		OwnerKind:      registry.OwnerApp,
		OwnerAppID:     &owner,
		CreatedAt:      adminTimestamp,
		UpdatedAt:      adminTimestamp,
	}
}

const sampleAppRouteJSON = `{"id":7,"app_pattern":"urls4irl","channel_pattern":"backup_*","destinations":["urls4irl_hook"],` +
	`"enabled":true,"owner_kind":"app","owner_app_id":"urls4irl","created_at":"` + adminTimestampJSON +
	`","updated_at":"` + adminTimestampJSON + `"}`

func allAppRouteRoutes() []appRoute {
	return []appRoute{
		{name: "create", method: http.MethodPost, path: appRoutesPath, body: validAppRouteBody},
		{name: "list", method: http.MethodGet, path: appRoutesPath},
		{name: "get", method: http.MethodGet, path: appRoutePath},
		{name: "patch", method: http.MethodPatch, path: appRoutePath, body: validAppRoutePatch},
		{name: "delete", method: http.MethodDelete, path: appRoutePath},
	}
}

func TestAppRouteEndpointsHappyPaths(testInstance *testing.T) {
	testCases := []struct {
		route      appRoute
		wantStatus int
		wantBody   string
	}{
		{route: allAppRouteRoutes()[0], wantStatus: http.StatusCreated, wantBody: `{"route":` + sampleAppRouteJSON + "}\n"},
		{route: allAppRouteRoutes()[1], wantStatus: http.StatusOK, wantBody: `{"routes":[` + sampleAppRouteJSON + "]}\n"},
		{route: allAppRouteRoutes()[2], wantStatus: http.StatusOK, wantBody: `{"route":` + sampleAppRouteJSON + "}\n"},
		{route: allAppRouteRoutes()[3], wantStatus: http.StatusOK, wantBody: `{"route":` + sampleAppRouteJSON + "}\n"},
		{route: allAppRouteRoutes()[4], wantStatus: http.StatusOK, wantBody: `{"id":7,"deleted":true}` + "\n"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.route.name, func(subTest *testing.T) {
			view := sampleAppRouteView()
			rig := newAppRig(&fakeAppRegistry{routeResult: view, routeListResult: []registry.RouteView{view}})

			recorder := rig.serve(testCase.route.method, testCase.route.path, testCase.route.body, bearerKey)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
		})
	}
}

func TestAppRouteEndpointsReachTheServiceWithTheKeysAppAndTheDecodedFields(testInstance *testing.T) {
	apps := &fakeAppRegistry{routeResult: sampleAppRouteView()}
	rig := newAppRig(apps)

	rig.serve(http.MethodPost, appRoutesPath,
		`{"app_pattern":"urls4irl","channel_pattern":"backup_*","destinations":["urls4irl_hook","ops_ntfy"],"enabled":false}`, bearerKey)
	rig.serve(http.MethodGet, appRoutesPath, "", bearerKey)
	rig.serve(http.MethodGet, appRoutePath, "", bearerKey)
	rig.serve(http.MethodPatch, appRoutePath,
		`{"app_pattern":"urls4irl","channel_pattern":"x_*","destinations":["ops_ntfy"],"enabled":true}`, bearerKey)
	rig.serve(http.MethodDelete, appRoutePath, "", bearerKey)

	disabled, enabled := false, true
	wantCreate := []appRouteCreateCall{{appID: keyApp, input: registry.RouteInput{
		AppPattern: keyApp, ChannelPattern: "backup_*", Destinations: []string{"urls4irl_hook", "ops_ntfy"}, Enabled: &disabled,
	}}}
	if !reflect.DeepEqual(apps.routeCreateCalls, wantCreate) {
		testInstance.Fatalf("create calls = %+v, want %+v", apps.routeCreateCalls, wantCreate)
	}
	if !reflect.DeepEqual(apps.routeListCalls, []string{keyApp}) {
		testInstance.Fatalf("list calls = %v", apps.routeListCalls)
	}
	wantID := []appRouteIDCall{{appID: keyApp, id: 7}}
	if !reflect.DeepEqual(apps.routeGetCalls, wantID) || !reflect.DeepEqual(apps.routeDeleteCalls, wantID) {
		testInstance.Fatalf("get calls = %+v, delete calls = %+v, want %+v", apps.routeGetCalls, apps.routeDeleteCalls, wantID)
	}
	pattern, channel := keyApp, "x_*"
	wantUpdate := []appRouteUpdateCall{{appID: keyApp, id: 7, patch: registry.RoutePatch{
		AppPattern: &pattern, ChannelPattern: &channel, Destinations: []string{"ops_ntfy"}, Enabled: &enabled,
	}}}
	if !reflect.DeepEqual(apps.routeUpdateCalls, wantUpdate) {
		testInstance.Fatalf("update calls = %+v, want %+v", apps.routeUpdateCalls, wantUpdate)
	}
}

// An app-scoped route body names no owner: sending one is an unknown-field 400 that never reaches the service.
func TestAppRouteEndpointsNeverAcceptAnOwnerFromTheBody(testInstance *testing.T) {
	for _, body := range []string{
		`{"channel_pattern":"a","destinations":["x"],"owner_kind":"operator"}`,
		`{"channel_pattern":"a","destinations":["x"],"owner_app_id":"chores4irl"}`,
		`{"channel_pattern":"a","destinations":["x"],"app_id":"chores4irl"}`,
	} {
		apps := &fakeAppRegistry{}
		rig := newAppRig(apps)

		recorder := rig.serve(http.MethodPost, appRoutesPath, body, bearerKey)

		assertResponse(testInstance, recorder, http.StatusBadRequest, errBodyInvalidJSON)
		if apps.totalCalls() != 0 {
			testInstance.Fatalf("the service was called for a body that names an owner: %s", body)
		}
	}
	for _, patchBody := range []string{
		`{"owner_kind":"operator"}`,
		`{"owner_app_id":"chores4irl"}`,
		`{"app_id":"chores4irl"}`,
	} {
		apps := &fakeAppRegistry{}
		recorder := newAppRig(apps).serve(http.MethodPatch, appRoutePath, patchBody, bearerKey)
		assertResponse(testInstance, recorder, http.StatusBadRequest, errBodyInvalidJSON)
		if apps.totalCalls() != 0 {
			testInstance.Fatalf("the service was called for a patch that names an owner: %s", patchBody)
		}
	}
}

func TestAppRouteEndpointsRequireTheAppKey(testInstance *testing.T) {
	for _, route := range allAppRouteRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			for _, authorization := range []string{"", "Basic abc", "Bearer", "Bearer not-a-key"} {
				apps := &fakeAppRegistry{}
				recorder := newAppRig(apps).serve(route.method, route.path, route.body, authorization)

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

func TestAppRouteEndpointsRefuseAnotherAppsPath(testInstance *testing.T) {
	for _, route := range allAppRouteRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			apps := &fakeAppRegistry{}
			path := strings.Replace(route.path, "/v1/apps/"+keyApp+"/", "/v1/apps/"+otherApp+"/", 1)

			recorder := newAppRig(apps).serve(route.method, path, route.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusForbidden, errBodyIdentityMismatch)
			if apps.totalCalls() != 0 {
				subTest.Fatalf("the service was called for another app's path")
			}
		})
	}
}

func TestAppRouteEndpointsFailClosed(testInstance *testing.T) {
	for _, route := range allAppRouteRoutes() {
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

func TestAppRouteHandlersAnswer503WhenReachedWithoutAnAuthenticatedApp(testInstance *testing.T) {
	handlers := map[string]func(*Server) http.HandlerFunc{
		"create": func(server *Server) http.HandlerFunc { return server.handleAppCreateRoute },
		"list":   func(server *Server) http.HandlerFunc { return server.handleAppListRoutes },
		"get":    func(server *Server) http.HandlerFunc { return server.handleAppGetRoute },
		"patch":  func(server *Server) http.HandlerFunc { return server.handleAppUpdateRoute },
		"delete": func(server *Server) http.HandlerFunc { return server.handleAppDeleteRoute },
	}
	for name, handlerOf := range handlers {
		apps := &fakeAppRegistry{}
		server := NewServer(ServerConfig{AppRegistry: apps})
		request := httptest.NewRequest(http.MethodGet, appRoutePath, strings.NewReader("{}"))
		request.SetPathValue("id", "7")
		recorder := httptest.NewRecorder()

		handlerOf(server)(recorder, request)

		assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
		if apps.totalCalls() != 0 {
			testInstance.Fatalf("%s: the service was called with no authenticated app", name)
		}
	}
}

func TestAppRouteEndpointsMapServiceErrors(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "wildcard pattern", err: &registry.ValidationError{Message: "app_pattern must be the app's own id"},
			wantStatus: http.StatusBadRequest, wantBody: errBodyPatternNotOwn,
		},
		{name: "not found", err: registry.ErrRouteNotFound, wantStatus: http.StatusNotFound, wantBody: errBodyRouteNotFound},
		{name: "wrapped not found", err: fmt.Errorf("wrap: %w", registry.ErrRouteNotFound), wantStatus: http.StatusNotFound, wantBody: errBodyRouteNotFound},
		{name: "unknown destination", err: registry.ErrUnknownDestination, wantStatus: http.StatusBadRequest, wantBody: errBodyUnknownDest},
		{
			name: "exists", err: registry.ErrRouteExists,
			wantStatus: http.StatusConflict, wantBody: `{"error":"route already exists"}` + "\n",
		},
		{name: "check violation", err: registry.ErrCheckViolation, wantStatus: http.StatusBadRequest, wantBody: errBodyConstraint},
	}
	for _, testCase := range testCases {
		for _, route := range allAppRouteRoutes() {
			// A list reports no per-route error other than a store failure.
			if route.name == "list" && errors.Is(testCase.err, registry.ErrRouteNotFound) {
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

func TestAppRouteEndpointsMapAGenericErrorTo500WithoutLeakingIt(testInstance *testing.T) {
	const secretErrText = "connection refused to database at 10.0.0.5:9999 with credential tk_secret_xyz"
	for _, route := range allAppRouteRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAppRig(failingAppRegistry(errors.New(secretErrText)))

			recorder := rig.serve(route.method, route.path, route.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if strings.Contains(recorder.Body.String(), "tk_secret_xyz") {
				subTest.Fatalf("the response leaked the error text: %s", recorder.Body.String())
			}
		})
	}
}

func TestAppRouteEndpointsRejectBadInputBeforeTheService(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{name: "malformed json", method: http.MethodPost, path: appRoutesPath, body: `{`, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSON},
		{name: "trailing data", method: http.MethodPatch, path: appRoutePath, body: `{} {}`, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSON},
		{
			name: "oversized", method: http.MethodPost, path: appRoutesPath,
			body:       `{"channel_pattern":"` + strings.Repeat("a", maxAdminBodyBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge, wantBody: `{"error":"request body too large"}` + "\n",
		},
		{name: "zero id", method: http.MethodGet, path: appRoutesPath + "/0", wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidRoute},
		{name: "signed id", method: http.MethodDelete, path: appRoutesPath + "/+7", wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidRoute},
		{name: "leading zero id", method: http.MethodPatch, path: appRoutesPath + "/07", body: `{}`, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidRoute},
		{name: "non-numeric id", method: http.MethodGet, path: appRoutesPath + "/abc", wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidRoute},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			apps := &fakeAppRegistry{}

			recorder := newAppRig(apps).serve(testCase.method, testCase.path, testCase.body, bearerKey)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if apps.totalCalls() != 0 {
				subTest.Fatalf("the service was called for a request the handler refuses")
			}
		})
	}
}

func TestAppRouteListIsNeverNull(testInstance *testing.T) {
	recorder := newAppRig(&fakeAppRegistry{routeListResult: nil}).serve(http.MethodGet, appRoutesPath, "", bearerKey)

	assertResponse(testInstance, recorder, http.StatusOK, `{"routes":[]}`+"\n")
}

func TestAppRouteResponsesAreNotCacheable(testInstance *testing.T) {
	for _, route := range allAppRouteRoutes() {
		recorder := newAppRig(&fakeAppRegistry{}).serve(route.method, route.path, route.body, bearerKey)

		if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			testInstance.Fatalf("%s: headers = %v, want no-store and nosniff", route.name, recorder.Header())
		}
	}
}

func TestAppRouteEndpointsRejectUnsupportedMethods(testInstance *testing.T) {
	rig := newAppRig(&fakeAppRegistry{})
	for _, request := range []struct{ method, path string }{
		{http.MethodPut, appRoutePath},
		{http.MethodDelete, appRoutesPath},
		{http.MethodPatch, appRoutesPath},
		{http.MethodPost, appRoutePath},
	} {
		recorder := rig.serve(request.method, request.path, "", bearerKey)
		if recorder.Code != http.StatusMethodNotAllowed {
			testInstance.Fatalf("%s %s status = %d, want 405", request.method, request.path, recorder.Code)
		}
	}
}

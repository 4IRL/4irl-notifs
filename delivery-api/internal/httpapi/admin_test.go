package httpapi

import (
	"bytes"
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

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The real registry service must satisfy the consumer-side interface.
var _ Registry = (*registry.Service)(nil)

// adminTimestamp is the fixed instant every fake view carries so the JSON
// bodies are exact.
var adminTimestamp = time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)

const adminTimestampJSON = "2026-10-16T12:00:00Z"

// fakeRegistry is a scripted Registry. Each method returns its <name>Result and
// <name>Err and records its arguments in <name>Calls.
type fakeRegistry struct {
	createDestinationResult registry.DestinationView
	createDestinationErr    error
	createDestinationCalls  []registry.DestinationInput

	listDestinationsResult []registry.DestinationView
	listDestinationsErr    error
	listDestinationsCalls  int

	getDestinationResult registry.DestinationView
	getDestinationErr    error
	getDestinationCalls  []string

	updateDestinationResult registry.DestinationView
	updateDestinationErr    error
	updateDestinationCalls  []updateDestinationCall

	deleteDestinationResult bool
	deleteDestinationErr    error
	deleteDestinationCalls  []string

	createRouteResult registry.RouteView
	createRouteErr    error
	createRouteCalls  []registry.RouteInput

	listRoutesResult []registry.RouteView
	listRoutesErr    error
	listRoutesCalls  int

	getRouteResult registry.RouteView
	getRouteErr    error
	getRouteCalls  []int64

	updateRouteResult registry.RouteView
	updateRouteErr    error
	updateRouteCalls  []updateRouteCall

	deleteRouteResult bool
	deleteRouteErr    error
	deleteRouteCalls  []int64
}

type updateDestinationCall struct {
	name  string
	patch registry.DestinationPatch
}

type updateRouteCall struct {
	id    int64
	patch registry.RoutePatch
}

func (fake *fakeRegistry) CreateDestination(_ context.Context, input registry.DestinationInput) (registry.DestinationView, error) {
	fake.createDestinationCalls = append(fake.createDestinationCalls, input)
	return fake.createDestinationResult, fake.createDestinationErr
}

func (fake *fakeRegistry) ListDestinations(context.Context) ([]registry.DestinationView, error) {
	fake.listDestinationsCalls++
	return fake.listDestinationsResult, fake.listDestinationsErr
}

func (fake *fakeRegistry) GetDestination(_ context.Context, name string) (registry.DestinationView, error) {
	fake.getDestinationCalls = append(fake.getDestinationCalls, name)
	return fake.getDestinationResult, fake.getDestinationErr
}

func (fake *fakeRegistry) UpdateDestination(_ context.Context, name string, patch registry.DestinationPatch) (registry.DestinationView, error) {
	fake.updateDestinationCalls = append(fake.updateDestinationCalls, updateDestinationCall{name: name, patch: patch})
	return fake.updateDestinationResult, fake.updateDestinationErr
}

func (fake *fakeRegistry) DeleteDestination(_ context.Context, name string) (bool, error) {
	fake.deleteDestinationCalls = append(fake.deleteDestinationCalls, name)
	return fake.deleteDestinationResult, fake.deleteDestinationErr
}

func (fake *fakeRegistry) CreateRoute(_ context.Context, input registry.RouteInput) (registry.RouteView, error) {
	fake.createRouteCalls = append(fake.createRouteCalls, input)
	return fake.createRouteResult, fake.createRouteErr
}

func (fake *fakeRegistry) ListRoutes(context.Context) ([]registry.RouteView, error) {
	fake.listRoutesCalls++
	return fake.listRoutesResult, fake.listRoutesErr
}

func (fake *fakeRegistry) GetRoute(_ context.Context, id int64) (registry.RouteView, error) {
	fake.getRouteCalls = append(fake.getRouteCalls, id)
	return fake.getRouteResult, fake.getRouteErr
}

func (fake *fakeRegistry) UpdateRoute(_ context.Context, id int64, patch registry.RoutePatch) (registry.RouteView, error) {
	fake.updateRouteCalls = append(fake.updateRouteCalls, updateRouteCall{id: id, patch: patch})
	return fake.updateRouteResult, fake.updateRouteErr
}

func (fake *fakeRegistry) DeleteRoute(_ context.Context, id int64) (bool, error) {
	fake.deleteRouteCalls = append(fake.deleteRouteCalls, id)
	return fake.deleteRouteResult, fake.deleteRouteErr
}

// totalCalls counts every call made on the fake, so tests can assert that a
// rejected request never reached the registry.
func (fake *fakeRegistry) totalCalls() int {
	return len(fake.createDestinationCalls) + fake.listDestinationsCalls +
		len(fake.getDestinationCalls) + len(fake.updateDestinationCalls) +
		len(fake.deleteDestinationCalls) + len(fake.createRouteCalls) +
		fake.listRoutesCalls + len(fake.getRouteCalls) +
		len(fake.updateRouteCalls) + len(fake.deleteRouteCalls)
}

// failingRegistry returns a fakeRegistry whose every method fails with err.
func failingRegistry(err error) *fakeRegistry {
	return &fakeRegistry{
		createDestinationErr: err,
		listDestinationsErr:  err,
		getDestinationErr:    err,
		updateDestinationErr: err,
		deleteDestinationErr: err,
		createRouteErr:       err,
		listRoutesErr:        err,
		getRouteErr:          err,
		updateRouteErr:       err,
		deleteRouteErr:       err,
	}
}

func sampleDestinationView() registry.DestinationView {
	return registry.DestinationView{
		Name:      "ops_ntfy",
		Type:      "ntfy",
		Enabled:   true,
		Config:    map[string]string{"topic": "ops"},
		Secrets:   map[string]string{"token": "****abcd"},
		CreatedAt: adminTimestamp,
		UpdatedAt: adminTimestamp,
	}
}

const sampleDestinationJSON = `{"name":"ops_ntfy","type":"ntfy","enabled":true,"disabled_reason":null,` +
	`"config":{"topic":"ops"},"secrets":{"token":"****abcd"},` +
	`"created_at":"` + adminTimestampJSON + `","updated_at":"` + adminTimestampJSON + `"}`

func sampleRouteView() registry.RouteView {
	return registry.RouteView{
		ID:             7,
		AppPattern:     "*",
		ChannelPattern: "backup_*",
		Destinations:   []string{"ops_ntfy"},
		Enabled:        true,
		CreatedAt:      adminTimestamp,
		UpdatedAt:      adminTimestamp,
	}
}

const sampleRouteJSON = `{"id":7,"app_pattern":"*","channel_pattern":"backup_*","destinations":["ops_ntfy"],` +
	`"enabled":true,"created_at":"` + adminTimestampJSON + `","updated_at":"` + adminTimestampJSON + `"}`

// adminRoute is one admin endpoint with a valid request body.
type adminRoute struct {
	name   string
	method string
	path   string
	body   string
}

const (
	validCreateDestinationBody = `{"name":"ops_ntfy","type":"ntfy","config":{"topic":"ops"},"secrets":{"token":"tk_secret"}}`
	validPatchDestinationBody  = `{"enabled":false}`
	validCreateRouteBody       = `{"app_pattern":"*","channel_pattern":"backup_*","destinations":["ops_ntfy"]}`
	validPatchRouteBody        = `{"enabled":false}`
)

func allAdminRoutes() []adminRoute {
	return []adminRoute{
		{name: "create destination", method: http.MethodPost, path: "/v1/admin/destinations", body: validCreateDestinationBody},
		{name: "list destinations", method: http.MethodGet, path: "/v1/admin/destinations"},
		{name: "get destination", method: http.MethodGet, path: "/v1/admin/destinations/ops_ntfy"},
		{name: "patch destination", method: http.MethodPatch, path: "/v1/admin/destinations/ops_ntfy", body: validPatchDestinationBody},
		{name: "delete destination", method: http.MethodDelete, path: "/v1/admin/destinations/ops_ntfy"},
		{name: "create route", method: http.MethodPost, path: "/v1/admin/routes", body: validCreateRouteBody},
		{name: "list routes", method: http.MethodGet, path: "/v1/admin/routes"},
		{name: "get route", method: http.MethodGet, path: "/v1/admin/routes/7"},
		{name: "patch route", method: http.MethodPatch, path: "/v1/admin/routes/7", body: validPatchRouteBody},
		{name: "delete route", method: http.MethodDelete, path: "/v1/admin/routes/7"},
	}
}

// serveAdmin sends one request to a Server built around fake and returns the
// recorder.
func serveAdmin(fake Registry, logger *slog.Logger, method, path, body string) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{Registry: fake, Logger: logger})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func assertResponse(testInstance *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantBody string) {
	testInstance.Helper()
	if recorder.Code != wantStatus {
		testInstance.Fatalf("status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	if body := recorder.Body.String(); body != wantBody {
		testInstance.Fatalf("body = %q, want %q", body, wantBody)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		testInstance.Fatalf("Content-Type = %q, want application/json", contentType)
	}
}

// TestAdminHappyPaths verifies the status and exact body of every admin route
// on success.
func TestAdminHappyPaths(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name: "create destination", method: http.MethodPost, path: "/v1/admin/destinations",
			body: validCreateDestinationBody, wantStatus: http.StatusCreated,
			wantBody: `{"destination":` + sampleDestinationJSON + "}\n",
		},
		{
			name: "list destinations", method: http.MethodGet, path: "/v1/admin/destinations",
			wantStatus: http.StatusOK, wantBody: `{"destinations":[` + sampleDestinationJSON + "]}\n",
		},
		{
			name: "get destination", method: http.MethodGet, path: "/v1/admin/destinations/ops_ntfy",
			wantStatus: http.StatusOK, wantBody: `{"destination":` + sampleDestinationJSON + "}\n",
		},
		{
			name: "patch destination", method: http.MethodPatch, path: "/v1/admin/destinations/ops_ntfy",
			body: validPatchDestinationBody, wantStatus: http.StatusOK,
			wantBody: `{"destination":` + sampleDestinationJSON + "}\n",
		},
		{
			name: "delete destination", method: http.MethodDelete, path: "/v1/admin/destinations/ops_ntfy",
			wantStatus: http.StatusOK, wantBody: `{"name":"ops_ntfy","deleted":true}` + "\n",
		},
		{
			name: "create route", method: http.MethodPost, path: "/v1/admin/routes",
			body: validCreateRouteBody, wantStatus: http.StatusCreated,
			wantBody: `{"route":` + sampleRouteJSON + "}\n",
		},
		{
			name: "list routes", method: http.MethodGet, path: "/v1/admin/routes",
			wantStatus: http.StatusOK, wantBody: `{"routes":[` + sampleRouteJSON + "]}\n",
		},
		{
			name: "get route", method: http.MethodGet, path: "/v1/admin/routes/7",
			wantStatus: http.StatusOK, wantBody: `{"route":` + sampleRouteJSON + "}\n",
		},
		{
			name: "patch route", method: http.MethodPatch, path: "/v1/admin/routes/7",
			body: validPatchRouteBody, wantStatus: http.StatusOK,
			wantBody: `{"route":` + sampleRouteJSON + "}\n",
		},
		{
			name: "delete route", method: http.MethodDelete, path: "/v1/admin/routes/7",
			wantStatus: http.StatusOK, wantBody: `{"id":7,"deleted":true}` + "\n",
		},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeRegistry{
				createDestinationResult: sampleDestinationView(),
				listDestinationsResult:  []registry.DestinationView{sampleDestinationView()},
				getDestinationResult:    sampleDestinationView(),
				updateDestinationResult: sampleDestinationView(),
				deleteDestinationResult: true,
				createRouteResult:       sampleRouteView(),
				listRoutesResult:        []registry.RouteView{sampleRouteView()},
				getRouteResult:          sampleRouteView(),
				updateRouteResult:       sampleRouteView(),
				deleteRouteResult:       true,
			}

			recorder := serveAdmin(fake, nil, testCase.method, testCase.path, testCase.body)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if fake.totalCalls() != 1 {
				subTest.Fatalf("registry calls = %d, want 1", fake.totalCalls())
			}
		})
	}
}

// TestAdminListsAreNeverNull verifies empty and nil lists encode as [].
func TestAdminListsAreNeverNull(testInstance *testing.T) {
	testCases := []struct {
		name     string
		path     string
		wantBody string
	}{
		{name: "destinations", path: "/v1/admin/destinations", wantBody: `{"destinations":[]}` + "\n"},
		{name: "routes", path: "/v1/admin/routes", wantBody: `{"routes":[]}` + "\n"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveAdmin(&fakeRegistry{}, nil, http.MethodGet, testCase.path, "")

			assertResponse(subTest, recorder, http.StatusOK, testCase.wantBody)
		})
	}
}

// TestAdminDeleteReportsDeletedTrueEvenWhenAbsent verifies the idempotent
// delete convention: a false from the registry is still a 200 deleted:true.
func TestAdminDeleteReportsDeletedTrueEvenWhenAbsent(testInstance *testing.T) {
	testCases := []struct {
		name     string
		path     string
		wantBody string
	}{
		{name: "destination", path: "/v1/admin/destinations/gone", wantBody: `{"name":"gone","deleted":true}` + "\n"},
		{name: "route", path: "/v1/admin/routes/99", wantBody: `{"id":99,"deleted":true}` + "\n"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveAdmin(&fakeRegistry{}, nil, http.MethodDelete, testCase.path, "")

			assertResponse(subTest, recorder, http.StatusOK, testCase.wantBody)
		})
	}
}

// TestAdminRequestsReachRegistryWithDecodedFields verifies the wire fields are
// mapped onto the registry inputs, patches keep absent fields nil, and path
// params are passed through.
func TestAdminRequestsReachRegistryWithDecodedFields(testInstance *testing.T) {
	disabled := false
	pattern := "orders"

	testInstance.Run("create destination", func(subTest *testing.T) {
		fake := &fakeRegistry{}
		body := `{"name":"ops_ntfy","type":"ntfy","enabled":false,"config":{"topic":"ops"},"secrets":{"token":"tk"}}`

		serveAdmin(fake, nil, http.MethodPost, "/v1/admin/destinations", body)

		want := []registry.DestinationInput{{
			Name: "ops_ntfy", Type: "ntfy", Enabled: &disabled,
			Config: map[string]string{"topic": "ops"}, Secrets: map[string]string{"token": "tk"},
		}}
		if !reflect.DeepEqual(fake.createDestinationCalls, want) {
			subTest.Fatalf("calls = %+v, want %+v", fake.createDestinationCalls, want)
		}
	})

	testInstance.Run("patch destination", func(subTest *testing.T) {
		fake := &fakeRegistry{}

		serveAdmin(fake, nil, http.MethodPatch, "/v1/admin/destinations/ops_ntfy", `{"secrets":{"token":"new"}}`)

		want := []updateDestinationCall{{
			name:  "ops_ntfy",
			patch: registry.DestinationPatch{Secrets: map[string]string{"token": "new"}},
		}}
		if !reflect.DeepEqual(fake.updateDestinationCalls, want) {
			subTest.Fatalf("calls = %+v, want %+v", fake.updateDestinationCalls, want)
		}
	})

	testInstance.Run("create route", func(subTest *testing.T) {
		fake := &fakeRegistry{}
		body := `{"app_pattern":"u4i","channel_pattern":"orders","destinations":["a","b"],"enabled":false}`

		serveAdmin(fake, nil, http.MethodPost, "/v1/admin/routes", body)

		want := []registry.RouteInput{{
			AppPattern: "u4i", ChannelPattern: "orders", Destinations: []string{"a", "b"}, Enabled: &disabled,
		}}
		if !reflect.DeepEqual(fake.createRouteCalls, want) {
			subTest.Fatalf("calls = %+v, want %+v", fake.createRouteCalls, want)
		}
	})

	testInstance.Run("patch route", func(subTest *testing.T) {
		fake := &fakeRegistry{}

		serveAdmin(fake, nil, http.MethodPatch, "/v1/admin/routes/12", `{"channel_pattern":"orders"}`)

		want := []updateRouteCall{{id: 12, patch: registry.RoutePatch{ChannelPattern: &pattern}}}
		if !reflect.DeepEqual(fake.updateRouteCalls, want) {
			subTest.Fatalf("calls = %+v, want %+v", fake.updateRouteCalls, want)
		}
	})

	testInstance.Run("get and delete route ids", func(subTest *testing.T) {
		fake := &fakeRegistry{}

		serveAdmin(fake, nil, http.MethodGet, "/v1/admin/routes/3", "")
		serveAdmin(fake, nil, http.MethodDelete, "/v1/admin/routes/4", "")

		if !reflect.DeepEqual(fake.getRouteCalls, []int64{3}) || !reflect.DeepEqual(fake.deleteRouteCalls, []int64{4}) {
			subTest.Fatalf("get = %v, delete = %v", fake.getRouteCalls, fake.deleteRouteCalls)
		}
	})
}

// TestAdminRejectsBadBodies verifies every body-carrying route rejects invalid
// JSON, a trailing value, an unknown field and an empty body with 400 "invalid JSON body", before the registry is called.
func TestAdminRejectsBadBodies(testInstance *testing.T) {
	bodyRoutes := []adminRoute{
		{name: "create destination", method: http.MethodPost, path: "/v1/admin/destinations", body: validCreateDestinationBody},
		{name: "patch destination", method: http.MethodPatch, path: "/v1/admin/destinations/ops_ntfy", body: validPatchDestinationBody},
		{name: "create route", method: http.MethodPost, path: "/v1/admin/routes", body: validCreateRouteBody},
		{name: "patch route", method: http.MethodPatch, path: "/v1/admin/routes/7", body: validPatchRouteBody},
	}
	badBodies := []struct {
		name string
		body func(valid string) string
	}{
		{name: "invalid JSON", body: func(string) string { return `{not json` }},
		{name: "empty body", body: func(string) string { return `` }},
		{name: "trailing second value", body: func(valid string) string { return valid + ` {"enabled":true}` }},
		{name: "trailing closing brace", body: func(valid string) string { return valid + `}` }},
		{name: "trailing garbage", body: func(valid string) string { return valid + ` garbage` }},
		{name: "unknown field", body: func(string) string { return `{"bogus":1}` }},
	}

	for _, route := range bodyRoutes {
		for _, badBody := range badBodies {
			testInstance.Run(route.name+"/"+badBody.name, func(subTest *testing.T) {
				fake := &fakeRegistry{}

				recorder := serveAdmin(fake, nil, route.method, route.path, badBody.body(route.body))

				assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
				if fake.totalCalls() != 0 {
					subTest.Fatalf("registry was called %d times for a rejected body", fake.totalCalls())
				}
			})
		}
	}
}

// TestAdminRejectsOversizedBodiesWith413 verifies every body-carrying route
// answers a body over maxAdminBodyBytes with 413, before the registry is called.
func TestAdminRejectsOversizedBodiesWith413(testInstance *testing.T) {
	bodyRoutes := []adminRoute{
		{name: "create destination", method: http.MethodPost, path: "/v1/admin/destinations"},
		{name: "patch destination", method: http.MethodPatch, path: "/v1/admin/destinations/ops_ntfy"},
		{name: "create route", method: http.MethodPost, path: "/v1/admin/routes"},
		{name: "patch route", method: http.MethodPatch, path: "/v1/admin/routes/7"},
	}
	oversized := `{"config":{"k":"` + strings.Repeat("a", maxAdminBodyBytes) + `"}}`

	for _, route := range bodyRoutes {
		testInstance.Run(route.name, func(subTest *testing.T) {
			fake := &fakeRegistry{}

			recorder := serveAdmin(fake, nil, route.method, route.path, oversized)

			assertResponse(subTest, recorder, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("registry was called %d times for a rejected body", fake.totalCalls())
			}
		})
	}
}

// TestAdminPatchRejectsImmutableDestinationFields verifies the immutable name
// and type cannot be sent in a PATCH: they fail loudly instead of being ignored.
func TestAdminPatchRejectsImmutableDestinationFields(testInstance *testing.T) {
	for _, body := range []string{`{"type":"discord"}`, `{"name":"other"}`} {
		testInstance.Run(body, func(subTest *testing.T) {
			fake := &fakeRegistry{}

			recorder := serveAdmin(fake, nil, http.MethodPatch, "/v1/admin/destinations/ops_ntfy", body)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("registry was called for an immutable field")
			}
		})
	}
}

// TestAdminRejectsBadRouteIDs verifies a non-numeric or non-positive route id
// is 400 "invalid route id" before the body is read or the registry called.
func TestAdminRejectsBadRouteIDs(testInstance *testing.T) {
	methods := []struct {
		method string
		body   string
	}{
		{method: http.MethodGet},
		{method: http.MethodPatch, body: validPatchRouteBody},
		{method: http.MethodDelete},
	}
	badIDs := []string{"abc", "0", "-1", "1.5", "99999999999999999999", "+5", "007"}

	for _, methodCase := range methods {
		for _, badID := range badIDs {
			testInstance.Run(methodCase.method+" "+badID, func(subTest *testing.T) {
				fake := &fakeRegistry{}

				recorder := serveAdmin(fake, nil, methodCase.method, "/v1/admin/routes/"+badID, methodCase.body)

				assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid route id"}`+"\n")
				if fake.totalCalls() != 0 {
					subTest.Fatalf("registry was called for a bad route id")
				}
			})
		}
	}
}

// TestAdminRejectsBadDestinationNames verifies a malformed destination name in
// the path (NUL byte, invalid UTF-8, uppercase) is 400 "invalid name" and never
// reaches the registry or Postgres.
func TestAdminRejectsBadDestinationNames(testInstance *testing.T) {
	methods := []struct {
		method string
		body   string
	}{
		{method: http.MethodGet},
		{method: http.MethodPatch, body: validPatchDestinationBody},
		{method: http.MethodDelete},
	}
	badNames := []string{"%00", "%ff", "Upper", "has-dash"}

	for _, methodCase := range methods {
		for _, badName := range badNames {
			testInstance.Run(methodCase.method+" "+badName, func(subTest *testing.T) {
				fake := &fakeRegistry{}

				recorder := serveAdmin(fake, nil, methodCase.method, "/v1/admin/destinations/"+badName, methodCase.body)

				assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid name"}`+"\n")
				if fake.totalCalls() != 0 {
					subTest.Fatalf("registry was called for a bad destination name")
				}
			})
		}
	}
}

// TestAdminResponsesAreNotCacheable verifies admin responses, success and
// failure, forbid caching and content sniffing.
func TestAdminResponsesAreNotCacheable(testInstance *testing.T) {
	for _, route := range allAdminRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			recorder := serveAdmin(&fakeRegistry{}, nil, route.method, route.path, route.body)

			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				subTest.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

// TestAdminPathValidatedBeforeBody verifies a bad route id wins over a bad body.
func TestAdminPathValidatedBeforeBody(testInstance *testing.T) {
	recorder := serveAdmin(&fakeRegistry{}, nil, http.MethodPatch, "/v1/admin/routes/abc", `{not json`)

	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid route id"}`+"\n")
}

// TestAdminMapsServiceErrors verifies every registry error maps to its status
// and message on every admin route.
func TestAdminMapsServiceErrors(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "validation error", err: &registry.ValidationError{Message: "invalid config.topic"},
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid config.topic"}` + "\n",
		},
		{
			name: "wrapped validation error", err: fmt.Errorf("wrap: %w", &registry.ValidationError{Message: "invalid name"}),
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid name"}` + "\n",
		},
		{
			name: "destination not found", err: registry.ErrDestinationNotFound,
			wantStatus: http.StatusNotFound, wantBody: `{"error":"destination does not exist"}` + "\n",
		},
		{
			name: "route not found", err: registry.ErrRouteNotFound,
			wantStatus: http.StatusNotFound, wantBody: `{"error":"route does not exist"}` + "\n",
		},
		{
			name: "destination exists", err: registry.ErrDestinationExists,
			wantStatus: http.StatusConflict, wantBody: `{"error":"destination already exists"}` + "\n",
		},
		{
			name: "route exists", err: registry.ErrRouteExists,
			wantStatus: http.StatusConflict, wantBody: `{"error":"route already exists"}` + "\n",
		},
		{
			name: "destination in use", err: registry.ErrDestinationInUse,
			wantStatus: http.StatusConflict, wantBody: `{"error":"destination is used by a route"}` + "\n",
		},
		{
			name: "unknown destination", err: registry.ErrUnknownDestination,
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"unknown destination"}` + "\n",
		},
		{
			name: "wrapped sentinel", err: fmt.Errorf("wrap: %w", registry.ErrRouteNotFound),
			wantStatus: http.StatusNotFound, wantBody: `{"error":"route does not exist"}` + "\n",
		},
	}

	for _, testCase := range testCases {
		for _, route := range allAdminRoutes() {
			testInstance.Run(testCase.name+"/"+route.name, func(subTest *testing.T) {
				recorder := serveAdmin(failingRegistry(testCase.err), nil, route.method, route.path, route.body)

				assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			})
		}
	}
}

// TestGenericServiceErrorMapsTo500 verifies a generic registry error is 500
// "internal error" on every admin route, that its text never reaches the body,
// and that it is logged.
func TestGenericServiceErrorMapsTo500(testInstance *testing.T) {
	const secretErrText = "connection refused to database at 10.0.0.5:9999 with credential tk_secret_xyz"

	for _, route := range allAdminRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

			recorder := serveAdmin(failingRegistry(errors.New(secretErrText)), logger, route.method, route.path, route.body)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if strings.Contains(recorder.Body.String(), "tk_secret_xyz") {
				subTest.Fatalf("response body leaked the real error text: %s", recorder.Body.String())
			}
			if !strings.Contains(logBuffer.String(), secretErrText) {
				subTest.Fatalf("log output = %q, want it to contain the real error text", logBuffer.String())
			}
		})
	}
}

// TestAdminLogsNeverContainRequestSecrets verifies the request body, which
// carries the destination secrets, is never written to the log on a failure.
func TestAdminLogsNeverContainRequestSecrets(testInstance *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

	serveAdmin(failingRegistry(errors.New("boom")), logger, http.MethodPost, "/v1/admin/destinations", validCreateDestinationBody)
	serveAdmin(&fakeRegistry{}, logger, http.MethodPost, "/v1/admin/destinations", `{"secrets":{"token":"tk_secret"},"bogus":1}`)

	if strings.Contains(logBuffer.String(), "tk_secret") {
		testInstance.Fatalf("log output leaked a request secret: %s", logBuffer.String())
	}
}

// TestAdminNilRegistryFailsClosed verifies that with no Registry configured
// every admin route is 503 "registry unavailable".
func TestAdminNilRegistryFailsClosed(testInstance *testing.T) {
	for _, route := range allAdminRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			recorder := serveAdmin(nil, nil, route.method, route.path, route.body)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"registry unavailable"}`+"\n")
		})
	}
}

// TestAdminRoutesRejectUnsupportedRequests pins the mux behavior around the
// admin routes: wrong methods are 405 and nothing exists outside /v1/admin.
func TestAdminRoutesRejectUnsupportedRequests(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "PUT destination", method: http.MethodPut, path: "/v1/admin/destinations/x", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE destinations collection", method: http.MethodDelete, path: "/v1/admin/destinations", wantStatus: http.StatusMethodNotAllowed},
		{name: "PATCH routes collection", method: http.MethodPatch, path: "/v1/admin/routes", wantStatus: http.StatusMethodNotAllowed},
		{name: "unknown admin path", method: http.MethodGet, path: "/v1/admin/nope", wantStatus: http.StatusNotFound},
		{name: "destinations outside admin", method: http.MethodGet, path: "/v1/destinations", wantStatus: http.StatusNotFound},
		{name: "routes outside admin", method: http.MethodGet, path: "/v1/routes", wantStatus: http.StatusNotFound},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeRegistry{}

			recorder := serveAdmin(fake, nil, testCase.method, testCase.path, "")

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			if fake.totalCalls() != 0 {
				subTest.Fatalf("registry was called for an unsupported request")
			}
		})
	}
}

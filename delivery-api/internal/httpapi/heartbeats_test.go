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

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The real heartbeat service must satisfy the consumer-side interface.
var _ Heartbeats = (*heartbeat.Service)(nil)

const validCreateCheckBody = `{"app_id":"urls4irl","name":"nightly_backup","period_seconds":86400,"grace_seconds":7200,"destinations":["ops_discord"]}`

// sampleCheckJSON is sampleCheck as seen at sampleNow (up, not late, never down).
const sampleCheckJSON = `{"id":7,"app_id":"urls4irl","name":"nightly_backup","period_seconds":86400,"grace_seconds":7200,` +
	`"paused":false,"state":"up","last_ping_at":"2026-10-16T12:00:00Z","last_ping_kind":"success","down_since":null,"down_reason":null,` +
	`"deadline_at":"2026-10-17T14:00:00Z","destinations":[{"name":"ops_discord","type":"discord","enabled":true}],` +
	`"owner_kind":"operator","owner_app_id":null,"created_at":"2026-10-14T12:00:00Z","updated_at":"2026-10-16T12:00:00Z"}`

// sampleProbeCheckJSON is sampleProbeCheck at sampleNow: a probe check with one
// failure so far, its last result a 503, and a stored access token.
const sampleProbeCheckJSON = `{"id":9,"app_id":"_probe","name":"site_home","period_seconds":300,"grace_seconds":1200,` +
	`"paused":false,"state":"up","last_ping_at":null,"last_ping_kind":null,"down_since":null,"down_reason":null,` +
	`"deadline_at":"2026-10-16T12:25:00Z","destinations":[{"name":"ops_discord","type":"discord","enabled":true}],` +
	`"owner_kind":"operator","owner_app_id":null,"created_at":"2026-10-14T12:00:00Z","updated_at":"2026-10-16T12:00:00Z",` +
	`"probe":{"url":"https://host.example/path?q=1","expected_statuses":[200,204],"body_match":"ok","timeout_seconds":5,` +
	`"interval_seconds":300,"consecutive_failures":1,"failure_threshold":3,"has_access_token":true,` +
	`"last_result":{"at":"2026-10-16T12:00:00Z","status":503,"reason":"http_status"}}}`

const (
	probeDetailSentinel      = "DETAIL-SENTINEL-xyz"
	probeDownDetailSentinel  = "DOWNDETAIL-SENTINEL-xyz"
	probeSealedTokenSentinel = "SEALED-SENTINEL-xyz"
)

// sampleProbeCheck is an up probe check with a failing last result, derived
// period/grace and sentinels in every field the view must never expose.
func sampleProbeCheck() heartbeat.StoredCheck {
	check := sampleCheck()
	check.ID = 9
	check.AppID = "_probe"
	check.Name = "site_home"
	check.PeriodSeconds, check.GraceSeconds = heartbeat.DerivedSchedule(300)
	check.LastPingAt = nil
	check.LastPingKind = nil
	check.Probe = &heartbeat.ProbeState{
		Config: heartbeat.ProbeConfig{
			URL:              "https://host.example/path?q=1",
			ExpectedStatuses: []int{200, 204},
			BodyMatch:        stringPointer("ok"),
			TimeoutSeconds:   5,
			IntervalSeconds:  300,
		},
		HasAccessToken:      true,
		SealedAccessToken:   []byte(probeSealedTokenSentinel),
		ConsecutiveFailures: 1,
		NextProbeAt:         adminTimestamp.Add(time.Hour),
		ConfigVersion:       41,
		LastResult: &heartbeat.ProbeResultRecord{
			At:     adminTimestamp,
			Status: intPointer(503),
			Reason: stringPointer(heartbeat.ReasonHTTPStatus),
			Detail: stringPointer(probeDetailSentinel),
		},
		DownDetail: stringPointer(probeDownDetailSentinel),
	}
	return check
}

// sampleNow is the fixed clock every heartbeat test runs on: a minute after the
// sample check's last ping.
var sampleNow = adminTimestamp.Add(time.Minute)

type patchCall struct {
	id    int64
	patch heartbeat.CheckPatch
}

type pingCall struct {
	appID string
	name  string
}

// fakeHeartbeats is a scripted Heartbeats. Create, Patch, Ping and Fail run the
// real validators first (so the 400 field names are the service's own), then
// return err when set, else the scripted result. Patch merges the patch into
// getResult the way the service does. Every call is recorded.
type fakeHeartbeats struct {
	err error
	// patchErr, when set, fails only Patch (after validation).
	patchErr error

	createResult heartbeat.StoredCheck
	getResult    heartbeat.StoredCheck
	listResult   []heartbeat.StoredCheck
	patchResult  heartbeat.StoredCheck
	deleteResult bool
	pauseResult  heartbeat.StoredCheck
	resumeResult heartbeat.StoredCheck
	pingResult   heartbeat.StoredCheck
	failResult   heartbeat.StoredCheck

	createCalls []heartbeat.NewCheck
	getCalls    []int64
	listCalls   int
	patchCalls  []patchCall
	deleteCalls []int64
	pauseCalls  []int64
	resumeCalls []int64
	pingCalls   []pingCall
	failCalls   []pingCall
}

func (fake *fakeHeartbeats) Create(_ context.Context, check heartbeat.NewCheck) (heartbeat.StoredCheck, error) {
	fake.createCalls = append(fake.createCalls, check)
	if validateErr := heartbeat.ValidateNewCheck(check); validateErr != nil {
		return heartbeat.StoredCheck{}, validateErr
	}
	return fake.createResult, fake.err
}

func (fake *fakeHeartbeats) Get(_ context.Context, id int64) (heartbeat.StoredCheck, error) {
	fake.getCalls = append(fake.getCalls, id)
	return fake.getResult, fake.err
}

func (fake *fakeHeartbeats) List(context.Context) ([]heartbeat.StoredCheck, error) {
	fake.listCalls++
	return fake.listResult, fake.err
}

func (fake *fakeHeartbeats) Patch(_ context.Context, id int64, patch heartbeat.CheckPatch) (heartbeat.StoredCheck, error) {
	fake.patchCalls = append(fake.patchCalls, patchCall{id: id, patch: patch})
	if fake.err != nil {
		// Like the store, the lock (not found, DB failure) comes before validation.
		return heartbeat.StoredCheck{}, fake.err
	}
	if patch.IsEmpty() {
		// Like the service: an empty patch is a plain read.
		return fake.getResult, nil
	}
	if kindErr := heartbeat.ValidateCheckPatchForKind(fake.getResult, patch); kindErr != nil {
		// Like PGStore.patch, the kind guard runs under the lock, before the merge.
		return heartbeat.StoredCheck{}, kindErr
	}
	if validateErr := heartbeat.ValidateCheckUpdate(heartbeat.ApplyPatch(fake.getResult, patch)); validateErr != nil {
		return heartbeat.StoredCheck{}, validateErr
	}
	if fake.patchErr != nil {
		return heartbeat.StoredCheck{}, fake.patchErr
	}
	return fake.patchResult, nil
}

func (fake *fakeHeartbeats) Delete(_ context.Context, id int64) (bool, error) {
	fake.deleteCalls = append(fake.deleteCalls, id)
	return fake.deleteResult, fake.err
}

func (fake *fakeHeartbeats) Pause(_ context.Context, id int64) (heartbeat.StoredCheck, error) {
	fake.pauseCalls = append(fake.pauseCalls, id)
	return fake.pauseResult, fake.err
}

func (fake *fakeHeartbeats) Resume(_ context.Context, id int64) (heartbeat.StoredCheck, error) {
	fake.resumeCalls = append(fake.resumeCalls, id)
	return fake.resumeResult, fake.err
}

func (fake *fakeHeartbeats) Ping(_ context.Context, appID string, name string) (heartbeat.StoredCheck, error) {
	fake.pingCalls = append(fake.pingCalls, pingCall{appID: appID, name: name})
	if validateErr := validatePingNames(appID, name); validateErr != nil {
		return heartbeat.StoredCheck{}, validateErr
	}
	return fake.pingResult, fake.err
}

func (fake *fakeHeartbeats) Fail(_ context.Context, appID string, name string) (heartbeat.StoredCheck, error) {
	fake.failCalls = append(fake.failCalls, pingCall{appID: appID, name: name})
	if validateErr := validatePingNames(appID, name); validateErr != nil {
		return heartbeat.StoredCheck{}, validateErr
	}
	return fake.failResult, fake.err
}

// validatePingNames runs the validators the real service runs before a ping.
func validatePingNames(appID string, name string) error {
	if validateErr := heartbeat.ValidateAppID(appID); validateErr != nil {
		return validateErr
	}
	return heartbeat.ValidateName(name)
}

func (fake *fakeHeartbeats) totalCalls() int {
	return len(fake.createCalls) + len(fake.getCalls) + fake.listCalls + len(fake.patchCalls) +
		len(fake.deleteCalls) + len(fake.pauseCalls) + len(fake.resumeCalls) + len(fake.pingCalls) + len(fake.failCalls)
}

// allResults returns a fake whose every result is check.
func allResults(check heartbeat.StoredCheck) *fakeHeartbeats {
	return &fakeHeartbeats{
		createResult: check, getResult: check, listResult: []heartbeat.StoredCheck{check}, patchResult: check,
		deleteResult: true, pauseResult: check, resumeResult: check, pingResult: check, failResult: check,
	}
}

// failingHeartbeats returns a fake whose every valid call fails with err.
func failingHeartbeats(err error) *fakeHeartbeats {
	return &fakeHeartbeats{err: err}
}

// sampleCheck is an up check last pinged at adminTimestamp with one destination.
func sampleCheck() heartbeat.StoredCheck {
	return heartbeat.StoredCheck{
		ID:            7,
		AppID:         "urls4irl",
		Name:          "nightly_backup",
		PeriodSeconds: 86400,
		GraceSeconds:  7200,
		State:         heartbeat.StateUp,
		AnchorAt:      adminTimestamp,
		LastPingAt:    timePointer(adminTimestamp),
		LastPingKind:  stringPointer(heartbeat.PingSuccess),
		Destinations:  []heartbeat.DestinationRef{{Name: "ops_discord", Type: "discord", Enabled: true}},
		OwnerKind:     registry.OwnerOperator,
		CreatedAt:     adminTimestamp.Add(-48 * time.Hour),
		UpdatedAt:     adminTimestamp,
	}
}

// serveHeartbeats sends one request to a Server built around fake, on the fixed
// sampleNow clock, and returns the recorder.
func serveHeartbeats(fake Heartbeats, logger *slog.Logger, method, path, body string) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{
		Heartbeats:  fake,
		AppIdentity: permissiveAppKeys(path, body),
		Logger:      logger,
		Now:         func() time.Time { return sampleNow },
	})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	authorizeAsApp(request)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// heartbeatRoute is one heartbeat route with a request that is valid for it.
type heartbeatRoute struct {
	name   string
	method string
	path   string
	body   string
}

// allHeartbeatRoutes lists every heartbeat route.
func allHeartbeatRoutes() []heartbeatRoute {
	return []heartbeatRoute{
		{name: "ping", method: http.MethodPost, path: "/v1/heartbeats/urls4irl/nightly_backup"},
		{name: "fail", method: http.MethodPost, path: "/v1/heartbeats/urls4irl/nightly_backup/fail"},
		{name: "list", method: http.MethodGet, path: "/v1/admin/heartbeats"},
		{name: "create", method: http.MethodPost, path: "/v1/admin/heartbeats", body: validCreateCheckBody},
		{name: "get", method: http.MethodGet, path: "/v1/admin/heartbeats/7"},
		{name: "patch", method: http.MethodPatch, path: "/v1/admin/heartbeats/7", body: `{"period_seconds":120}`},
		{name: "delete", method: http.MethodDelete, path: "/v1/admin/heartbeats/7"},
		{name: "pause", method: http.MethodPost, path: "/v1/admin/heartbeats/7/pause"},
		{name: "resume", method: http.MethodPost, path: "/v1/admin/heartbeats/7/resume"},
		{name: "probe create", method: http.MethodPost, path: "/v1/admin/heartbeats", body: probeCreateBody(validProbeJSON, "")},
		{
			name: "probe patch", method: http.MethodPatch, path: "/v1/admin/heartbeats/9",
			body: `{"probe":{"interval_seconds":120,"access_token":{"client_id":"id","client_secret":"secret"}}}`,
		},
	}
}

func TestPingReturnsTheDerivedState(testInstance *testing.T) {
	testCases := []struct {
		name      string
		path      string
		configure func(fake *fakeHeartbeats, check heartbeat.StoredCheck)
		check     heartbeat.StoredCheck
		wantState string
	}{
		{
			name: "ping of an up check", path: "/v1/heartbeats/urls4irl/nightly_backup",
			configure: func(fake *fakeHeartbeats, check heartbeat.StoredCheck) { fake.pingResult = check },
			check:     sampleCheck(), wantState: "up",
		},
		{
			name: "fail ping", path: "/v1/heartbeats/urls4irl/nightly_backup/fail",
			configure: func(fake *fakeHeartbeats, check heartbeat.StoredCheck) { fake.failResult = check },
			check:     withState(sampleCheck(), heartbeat.StateDown), wantState: "down",
		},
		{
			name: "ping of a check already past its period", path: "/v1/heartbeats/urls4irl/nightly_backup",
			configure: func(fake *fakeHeartbeats, check heartbeat.StoredCheck) { fake.pingResult = check },
			check:     withAnchor(sampleCheck(), sampleNow.Add(-25*time.Hour)), wantState: "late",
		},
		{
			name: "ping of a paused check", path: "/v1/heartbeats/urls4irl/nightly_backup",
			configure: func(fake *fakeHeartbeats, check heartbeat.StoredCheck) { fake.pingResult = check },
			check:     withPaused(sampleCheck()), wantState: "up",
		},
		{
			name: "fail ping of a paused check", path: "/v1/heartbeats/urls4irl/nightly_backup/fail",
			configure: func(fake *fakeHeartbeats, check heartbeat.StoredCheck) { fake.failResult = check },
			check:     withPaused(withState(sampleCheck(), heartbeat.StateDown)), wantState: "down",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeHeartbeats{}
			testCase.configure(fake, testCase.check)

			recorder := serveHeartbeats(fake, nil, http.MethodPost, testCase.path, "")

			wantBody := fmt.Sprintf(`{"app_id":"urls4irl","name":"nightly_backup","state":%q}`, testCase.wantState) + "\n"
			assertResponse(subTest, recorder, http.StatusOK, wantBody)
		})
	}
}

func withState(check heartbeat.StoredCheck, state string) heartbeat.StoredCheck {
	check.State = state
	return check
}

func withAnchor(check heartbeat.StoredCheck, anchor time.Time) heartbeat.StoredCheck {
	check.AnchorAt = anchor
	return check
}

func withPaused(check heartbeat.StoredCheck) heartbeat.StoredCheck {
	check.Paused = true
	return check
}

func TestPingPassesTheNamesAndIgnoresTheBody(testInstance *testing.T) {
	fake := allResults(sampleCheck())

	pingRecorder := serveHeartbeats(fake, nil, http.MethodPost, "/v1/heartbeats/urls4irl/nightly_backup", `not json at all`)
	failRecorder := serveHeartbeats(fake, nil, http.MethodPost, "/v1/heartbeats/urls4irl/nightly_backup/fail", `{"bogus":`)

	if pingRecorder.Code != http.StatusOK || failRecorder.Code != http.StatusOK {
		testInstance.Fatalf("statuses = %d and %d, want 200 for both", pingRecorder.Code, failRecorder.Code)
	}
	want := []pingCall{{appID: "urls4irl", name: "nightly_backup"}}
	if !reflect.DeepEqual(fake.pingCalls, want) || !reflect.DeepEqual(fake.failCalls, want) {
		testInstance.Fatalf("ping calls = %v and fail calls = %v, want %v for both", fake.pingCalls, fake.failCalls, want)
	}
}

func TestPingRejectsInvalidPathSegments(testInstance *testing.T) {
	testCases := []struct {
		name     string
		path     string
		wantBody string
	}{
		{name: "uppercase app_id", path: "/v1/heartbeats/UrlS4irl/nightly_backup", wantBody: `{"error":"invalid app_id"}`},
		{name: "dashed app_id", path: "/v1/heartbeats/urls-4irl/nightly_backup", wantBody: `{"error":"invalid app_id"}`},
		{name: "uppercase name", path: "/v1/heartbeats/urls4irl/Nightly", wantBody: `{"error":"invalid name"}`},
		{name: "dashed name", path: "/v1/heartbeats/urls4irl/nightly-backup", wantBody: `{"error":"invalid name"}`},
		{name: "name starting with an underscore", path: "/v1/heartbeats/urls4irl/_nightly", wantBody: `{"error":"invalid name"}`},
		{name: "over-long name", path: "/v1/heartbeats/urls4irl/" + strings.Repeat("a", 64), wantBody: `{"error":"invalid name"}`},
		{name: "NUL byte in name", path: "/v1/heartbeats/urls4irl/ab%00c", wantBody: `{"error":"invalid name"}`},
	}
	for _, testCase := range testCases {
		for _, suffix := range []string{"", "/fail"} {
			testInstance.Run(testCase.name+suffix, func(subTest *testing.T) {
				recorder := serveHeartbeats(allResults(sampleCheck()), nil, http.MethodPost, testCase.path+suffix, "")

				assertResponse(subTest, recorder, http.StatusBadRequest, testCase.wantBody+"\n")
			})
		}
	}
}

func TestPingOfAnUnknownCheckIs404(testInstance *testing.T) {
	for _, path := range []string{"/v1/heartbeats/urls4irl/nightly_backup", "/v1/heartbeats/urls4irl/nightly_backup/fail"} {
		testInstance.Run(path, func(subTest *testing.T) {
			recorder := serveHeartbeats(failingHeartbeats(heartbeat.ErrCheckNotFound), nil, http.MethodPost, path, "")

			assertResponse(subTest, recorder, http.StatusNotFound, `{"error":"check not found"}`+"\n")
		})
	}
}

func TestAdminHeartbeatHappyPaths(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{name: "list", method: http.MethodGet, path: "/v1/admin/heartbeats", wantStatus: 200, wantBody: `{"checks":[` + sampleCheckJSON + `]}`},
		{name: "create", method: http.MethodPost, path: "/v1/admin/heartbeats", body: validCreateCheckBody, wantStatus: 201, wantBody: `{"check":` + sampleCheckJSON + `}`},
		{name: "get", method: http.MethodGet, path: "/v1/admin/heartbeats/7", wantStatus: 200, wantBody: `{"check":` + sampleCheckJSON + `}`},
		{name: "patch", method: http.MethodPatch, path: "/v1/admin/heartbeats/7", body: `{"grace_seconds":3600}`, wantStatus: 200, wantBody: `{"check":` + sampleCheckJSON + `}`},
		{name: "delete", method: http.MethodDelete, path: "/v1/admin/heartbeats/7", wantStatus: 200, wantBody: `{"id":7,"deleted":true}`},
		{name: "pause", method: http.MethodPost, path: "/v1/admin/heartbeats/7/pause", wantStatus: 200, wantBody: `{"check":` + sampleCheckJSON + `}`},
		{name: "resume", method: http.MethodPost, path: "/v1/admin/heartbeats/7/resume", wantStatus: 200, wantBody: `{"check":` + sampleCheckJSON + `}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveHeartbeats(allResults(sampleCheck()), nil, testCase.method, testCase.path, testCase.body)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
		})
	}
}

func TestIdempotentAdminOperationsAnswer200OnRepeat(testInstance *testing.T) {
	paused := withPaused(sampleCheck())
	fake := &fakeHeartbeats{pauseResult: paused, resumeResult: sampleCheck(), deleteResult: false}

	for repeat := 0; repeat < 2; repeat++ {
		assertResponse(testInstance, serveHeartbeats(fake, nil, http.MethodPost, "/v1/admin/heartbeats/7/pause", ""),
			http.StatusOK, `{"check":`+strings.Replace(sampleCheckJSON, `"paused":false`, `"paused":true`, 1)+`}`+"\n")
		assertResponse(testInstance, serveHeartbeats(fake, nil, http.MethodPost, "/v1/admin/heartbeats/7/resume", ""),
			http.StatusOK, `{"check":`+sampleCheckJSON+`}`+"\n")
		assertResponse(testInstance, serveHeartbeats(fake, nil, http.MethodDelete, "/v1/admin/heartbeats/7", ""),
			http.StatusOK, `{"id":7,"deleted":false}`+"\n")
	}
}

func TestListIsNeverNullAndDestinationsAreNeverNull(testInstance *testing.T) {
	empty := serveHeartbeats(&fakeHeartbeats{}, nil, http.MethodGet, "/v1/admin/heartbeats", "")
	assertResponse(testInstance, empty, http.StatusOK, `{"checks":[]}`+"\n")

	check := sampleCheck()
	check.Destinations = nil
	recorder := serveHeartbeats(&fakeHeartbeats{getResult: check}, nil, http.MethodGet, "/v1/admin/heartbeats/7", "")
	if !strings.Contains(recorder.Body.String(), `"destinations":[]`) {
		testInstance.Fatalf("body = %s, want an empty destinations array", recorder.Body.String())
	}
}

func TestCheckViewStates(testInstance *testing.T) {
	downSince := sampleNow.Add(-time.Hour)
	testCases := []struct {
		name           string
		mutate         func(check *heartbeat.StoredCheck)
		wantState      string
		wantDownSince  string
		wantPaused     string
		wantLastPing   string
		wantLastKind   string
		wantDeadlineAt string
	}{
		{
			name: "new check never pinged",
			mutate: func(check *heartbeat.StoredCheck) {
				check.State = heartbeat.StateNew
				check.LastPingAt = nil
				check.LastPingKind = nil
			},
			wantState: `"state":"new"`, wantDownSince: `"down_since":null`, wantPaused: `"paused":false`,
			wantLastPing: `"last_ping_at":null`, wantLastKind: `"last_ping_kind":null`, wantDeadlineAt: `"deadline_at":"2026-10-17T14:00:00Z"`,
		},
		{
			name:      "up check past its period is late",
			mutate:    func(check *heartbeat.StoredCheck) { check.AnchorAt = sampleNow.Add(-25 * time.Hour) },
			wantState: `"state":"late"`, wantDownSince: `"down_since":null`, wantPaused: `"paused":false`,
			wantLastPing: `"last_ping_at":"2026-10-16T12:00:00Z"`, wantLastKind: `"last_ping_kind":"success"`, wantDeadlineAt: `"deadline_at":"2026-10-16T13:01:00Z"`,
		},
		{
			name: "down check reports when it went down",
			mutate: func(check *heartbeat.StoredCheck) {
				check.State = heartbeat.StateDown
				check.DownSince = timePointer(downSince)
				check.DownReason = stringPointer(heartbeat.ReasonFail)
				check.LastPingKind = stringPointer(heartbeat.PingFail)
			},
			wantState: `"state":"down"`, wantDownSince: `"down_since":"2026-10-16T11:01:00Z","down_reason":"fail"`, wantPaused: `"paused":false`,
			wantLastPing: `"last_ping_at":"2026-10-16T12:00:00Z"`, wantLastKind: `"last_ping_kind":"fail"`, wantDeadlineAt: `"deadline_at":"2026-10-17T14:00:00Z"`,
		},
		{
			name: "recovered check does not report the retained down_since",
			mutate: func(check *heartbeat.StoredCheck) {
				check.DownSince = timePointer(downSince)
			},
			wantState: `"state":"up"`, wantDownSince: `"down_since":null`, wantPaused: `"paused":false`,
			wantLastPing: `"last_ping_at":"2026-10-16T12:00:00Z"`, wantLastKind: `"last_ping_kind":"success"`, wantDeadlineAt: `"deadline_at":"2026-10-17T14:00:00Z"`,
		},
		{
			name:      "paused check keeps its derived state and reports paused",
			mutate:    func(check *heartbeat.StoredCheck) { check.Paused = true },
			wantState: `"state":"up"`, wantDownSince: `"down_since":null`, wantPaused: `"paused":true`,
			wantLastPing: `"last_ping_at":"2026-10-16T12:00:00Z"`, wantLastKind: `"last_ping_kind":"success"`, wantDeadlineAt: `"deadline_at":"2026-10-17T14:00:00Z"`,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			check := sampleCheck()
			testCase.mutate(&check)

			recorder := serveHeartbeats(&fakeHeartbeats{getResult: check}, nil, http.MethodGet, "/v1/admin/heartbeats/7", "")

			body := recorder.Body.String()
			for _, want := range []string{
				testCase.wantState, testCase.wantDownSince, testCase.wantPaused,
				testCase.wantLastPing, testCase.wantLastKind, testCase.wantDeadlineAt,
			} {
				if !strings.Contains(body, want) {
					subTest.Fatalf("body = %s, want it to contain %s", body, want)
				}
			}
		})
	}
}

func TestProbeCheckViewMatchesTheContract(testInstance *testing.T) {
	recorder := serveHeartbeats(&fakeHeartbeats{getResult: sampleProbeCheck()}, nil, http.MethodGet, "/v1/admin/heartbeats/9", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"check":`+sampleProbeCheckJSON+`}`+"\n")
}

func TestProbeCheckViewVariants(testInstance *testing.T) {
	testCases := []struct {
		name   string
		mutate func(check *heartbeat.StoredCheck)
		want   []string
	}{
		{
			name:   "never probed",
			mutate: func(check *heartbeat.StoredCheck) { check.Probe.LastResult = nil; check.Probe.ConsecutiveFailures = 0 },
			want:   []string{`"consecutive_failures":0`, `"last_result":null`, `"down_reason":null`},
		},
		{
			name: "success result",
			mutate: func(check *heartbeat.StoredCheck) {
				check.Probe.LastResult.Status = intPointer(200)
				check.Probe.LastResult.Reason = nil
			},
			want: []string{`"last_result":{"at":"2026-10-16T12:00:00Z","status":200,"reason":null}`},
		},
		{
			name: "transport failure",
			mutate: func(check *heartbeat.StoredCheck) {
				check.Probe.LastResult.Status = nil
				check.Probe.LastResult.Reason = stringPointer(heartbeat.ReasonTimeout)
			},
			want: []string{`"last_result":{"at":"2026-10-16T12:00:00Z","status":null,"reason":"timeout"}`},
		},
		{
			name:   "http_status with a null status",
			mutate: func(check *heartbeat.StoredCheck) { check.Probe.LastResult.Status = nil },
			want:   []string{`"last_result":{"at":"2026-10-16T12:00:00Z","status":null,"reason":"http_status"}`},
		},
		{
			name: "down probe check",
			mutate: func(check *heartbeat.StoredCheck) {
				check.State = heartbeat.StateDown
				check.DownSince = timePointer(sampleNow.Add(-time.Minute))
				check.DownReason = stringPointer(heartbeat.ReasonHTTPStatus)
			},
			want: []string{`"state":"down"`, `"down_reason":"http_status"`, `"status":503`},
		},
		{
			name: "non-down check with a stale reason renders null",
			mutate: func(check *heartbeat.StoredCheck) {
				check.DownReason = stringPointer(heartbeat.ReasonHTTPStatus)
			},
			want: []string{`"state":"up"`, `"down_reason":null`},
		},
		{
			name: "down probe check whose result was cleared",
			mutate: func(check *heartbeat.StoredCheck) {
				check.State = heartbeat.StateDown
				check.DownSince = timePointer(sampleNow.Add(-time.Minute))
				check.DownReason = stringPointer(heartbeat.ReasonHTTPStatus)
				check.Probe.LastResult = nil
			},
			want: []string{`"state":"down"`, `"down_reason":"http_status"`, `"last_result":null`},
		},
		{
			name: "no access token and no body match",
			mutate: func(check *heartbeat.StoredCheck) {
				check.Probe.HasAccessToken = false
				check.Probe.Config.BodyMatch = nil
			},
			want: []string{`"body_match":null`, `"has_access_token":false`},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			check := sampleProbeCheck()
			testCase.mutate(&check)

			recorder := serveHeartbeats(&fakeHeartbeats{getResult: check}, nil, http.MethodGet, "/v1/admin/heartbeats/9", "")

			body := recorder.Body.String()
			for _, want := range testCase.want {
				if !strings.Contains(body, want) {
					subTest.Fatalf("body = %s, want it to contain %s", body, want)
				}
			}
		})
	}
}

func TestProbeCheckIsNeverLate(testInstance *testing.T) {
	check := sampleProbeCheck()
	// Far past anchor + period and the derived deadline (anchor + interval*(N+2)).
	check.AnchorAt = sampleNow.Add(-3 * time.Hour)
	fake := &fakeHeartbeats{getResult: check, listResult: []heartbeat.StoredCheck{check}}

	for _, path := range []string{"/v1/admin/heartbeats/9", "/v1/admin/heartbeats"} {
		recorder := serveHeartbeats(fake, nil, http.MethodGet, path, "")

		body := recorder.Body.String()
		if !strings.Contains(body, `"state":"up"`) || strings.Contains(body, `"late"`) {
			testInstance.Fatalf("%s body = %s, want state up and never late", path, body)
		}
	}
}

func TestProbeCheckViewNeverLeaksInternals(testInstance *testing.T) {
	check := sampleProbeCheck()
	fake := &fakeHeartbeats{getResult: check, listResult: []heartbeat.StoredCheck{check}}

	for _, path := range []string{"/v1/admin/heartbeats/9", "/v1/admin/heartbeats"} {
		body := serveHeartbeats(fake, nil, http.MethodGet, path, "").Body.String()
		for _, secret := range []string{
			probeDetailSentinel, probeDownDetailSentinel, probeSealedTokenSentinel,
			"config_version", "next_probe_at", "detail", "sealed",
		} {
			if strings.Contains(body, secret) {
				testInstance.Fatalf("%s body = %s, must not contain %q", path, body, secret)
			}
		}
	}
}

func TestPushCheckViewHasNoProbeKey(testInstance *testing.T) {
	body := serveHeartbeats(&fakeHeartbeats{getResult: sampleCheck()}, nil, http.MethodGet, "/v1/admin/heartbeats/7", "").Body.String()

	if strings.Contains(body, `"probe"`) {
		testInstance.Fatalf("push body = %s, must not contain a probe key", body)
	}
}

func TestCheckViewTimesAreUTC(testInstance *testing.T) {
	zone := time.FixedZone("CEST", 2*60*60)
	check := sampleCheck()
	check.AnchorAt = check.AnchorAt.In(zone)
	check.LastPingAt = timePointer(adminTimestamp.In(zone))
	check.CreatedAt = check.CreatedAt.In(zone)
	check.UpdatedAt = check.UpdatedAt.In(zone)

	recorder := serveHeartbeats(&fakeHeartbeats{getResult: check}, nil, http.MethodGet, "/v1/admin/heartbeats/7", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"check":`+sampleCheckJSON+`}`+"\n")
}

func TestCreatePassesTheDecodedCheckToTheService(testInstance *testing.T) {
	fake := &fakeHeartbeats{createResult: sampleCheck()}

	serveHeartbeats(fake, nil, http.MethodPost, "/v1/admin/heartbeats",
		`{"app_id":"urls4irl","name":"nightly_backup","period_seconds":86400,"grace_seconds":7200,"destinations":["ops_discord","ops_ntfy"]}`)

	want := []heartbeat.NewCheck{{
		AppID: "urls4irl", Name: "nightly_backup", PeriodSeconds: 86400, GraceSeconds: 7200,
		DestinationNames: []string{"ops_discord", "ops_ntfy"},
		OwnerKind:        registry.OwnerOperator,
		ScheduleSupplied: true,
	}}
	if !reflect.DeepEqual(fake.createCalls, want) {
		testInstance.Fatalf("create calls = %+v, want %+v", fake.createCalls, want)
	}
}

func TestGetCheckReportsAnAppOwner(testInstance *testing.T) {
	check := sampleCheck()
	check.OwnerKind = registry.OwnerApp
	check.OwnerAppID = stringPointer("urls4irl")

	recorder := serveHeartbeats(&fakeHeartbeats{getResult: check}, nil, http.MethodGet, "/v1/admin/heartbeats/7", "")

	wantJSON := strings.Replace(sampleCheckJSON, `"owner_kind":"operator","owner_app_id":null`,
		`"owner_kind":"app","owner_app_id":"urls4irl"`, 1)
	assertResponse(testInstance, recorder, http.StatusOK, `{"check":`+wantJSON+`}`+"\n")
}

func TestCreateRejectsEachInvalidFieldByName(testInstance *testing.T) {
	testCases := []struct {
		name     string
		body     string
		wantBody string
	}{
		{name: "missing app_id", body: `{"name":"n","period_seconds":60,"grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid app_id"}`},
		{name: "bad app_id", body: `{"app_id":"Bad Id","name":"n","period_seconds":60,"grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid app_id"}`},
		{name: "missing name", body: `{"app_id":"a","period_seconds":60,"grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid name"}`},
		{name: "bad name", body: `{"app_id":"a","name":"Bad-Name","period_seconds":60,"grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid name"}`},
		{name: "missing period", body: `{"app_id":"a","name":"n","grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid period_seconds"}`},
		{name: "period too small", body: `{"app_id":"a","name":"n","period_seconds":59,"grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid period_seconds"}`},
		{name: "period too large", body: `{"app_id":"a","name":"n","period_seconds":2678401,"grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid period_seconds"}`},
		{name: "missing grace", body: `{"app_id":"a","name":"n","period_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid grace_seconds"}`},
		{name: "grace too small", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":59,"destinations":["d"]}`, wantBody: `{"error":"invalid grace_seconds"}`},
		{name: "grace too large", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":604801,"destinations":["d"]}`, wantBody: `{"error":"invalid grace_seconds"}`},
		{name: "missing destinations", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60}`, wantBody: `{"error":"invalid destinations"}`},
		{name: "empty destinations", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"destinations":[]}`, wantBody: `{"error":"invalid destinations"}`},
		{name: "duplicate destinations", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"destinations":["d","d"]}`, wantBody: `{"error":"invalid destinations"}`},
		{name: "bad destination name", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"destinations":["Bad Name"]}`, wantBody: `{"error":"invalid destinations"}`},
		{name: "too many destinations", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"destinations":[` + quotedNames(21) + `]}`, wantBody: `{"error":"invalid destinations"}`},
		{name: "unknown field", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"destinations":["d"],"bogus":1}`, wantBody: `{"error":"invalid JSON body"}`},
		{name: "not json", body: `nope`, wantBody: `{"error":"invalid JSON body"}`},
		{name: "wrong type", body: `{"app_id":"a","name":"n","period_seconds":"60","grace_seconds":60,"destinations":["d"]}`, wantBody: `{"error":"invalid JSON body"}`},
		{name: "trailing data", body: validCreateCheckBody + `{}`, wantBody: `{"error":"invalid JSON body"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveHeartbeats(allResults(sampleCheck()), nil, http.MethodPost, "/v1/admin/heartbeats", testCase.body)

			assertResponse(subTest, recorder, http.StatusBadRequest, testCase.wantBody+"\n")
		})
	}
}

// quotedNames returns count distinct, valid, comma-joined quoted destination names.
func quotedNames(count int) string {
	names := make([]string, 0, count)
	for index := 0; index < count; index++ {
		names = append(names, fmt.Sprintf(`"dest_%d"`, index))
	}
	return strings.Join(names, ",")
}

func TestCreateBodySizeLimit(testInstance *testing.T) {
	oversized := `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"destinations":["` + strings.Repeat("d", maxAdminBodyBytes) + `"]}`

	recorder := serveHeartbeats(allResults(sampleCheck()), nil, http.MethodPost, "/v1/admin/heartbeats", oversized)

	assertResponse(testInstance, recorder, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`+"\n")
}

func TestPatchPassesOnlyTheNamedFieldsToTheService(testInstance *testing.T) {
	probeToken := heartbeat.AccessToken{ClientID: "id-1", ClientSecret: "secret-1"}
	testCases := []struct {
		name string
		body string
		want heartbeat.CheckPatch
		// stored is the check the fake holds; the zero value means sampleCheck().
		stored heartbeat.StoredCheck
	}{
		{name: "period only", body: `{"period_seconds":120}`, want: heartbeat.CheckPatch{PeriodSeconds: intPointer(120)}},
		{name: "grace only", body: `{"grace_seconds":3600}`, want: heartbeat.CheckPatch{GraceSeconds: intPointer(3600)}},
		{name: "destinations only", body: `{"destinations":["ops_ntfy"]}`, want: heartbeat.CheckPatch{DestinationNames: []string{"ops_ntfy"}}},
		{
			name: "everything", body: `{"period_seconds":300,"grace_seconds":120,"destinations":["ops_discord"]}`,
			want: heartbeat.CheckPatch{PeriodSeconds: intPointer(300), GraceSeconds: intPointer(120), DestinationNames: []string{"ops_discord"}},
		},
		{
			name: "null fields are left alone", body: `{"period_seconds":null,"grace_seconds":120,"destinations":null}`,
			want: heartbeat.CheckPatch{GraceSeconds: intPointer(120)},
		},
		{
			name: "probe interval only", body: `{"probe":{"interval_seconds":120}}`, stored: sampleProbeCheck(),
			want: heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{IntervalSeconds: intPointer(120)}},
		},
		{
			name: "probe every field", stored: sampleProbeCheck(),
			body: `{"probe":{"url":"https://example.com/x","expected_statuses":[200],"body_match":"ok","timeout_seconds":3,"interval_seconds":600}}`,
			want: heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{
				URL: stringPointer("https://example.com/x"), ExpectedStatuses: []int{200}, BodyMatch: stringPointer("ok"),
				TimeoutSeconds: intPointer(3), IntervalSeconds: intPointer(600),
			}},
		},
		{
			name: "probe body_match null clears", body: `{"probe":{"body_match":null}}`, stored: sampleProbeCheck(),
			want: heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{ClearBodyMatch: true}},
		},
		{
			name: "probe body_match set", body: `{"probe":{"body_match":"ok"}}`, stored: sampleProbeCheck(),
			want: heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{BodyMatch: stringPointer("ok")}},
		},
		{
			name: "probe url null is unchanged", body: `{"probe":{"url":null}}`, stored: sampleProbeCheck(),
			want: heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{}},
		},
		{
			name: "probe access_token null clears", body: `{"probe":{"access_token":null}}`, stored: sampleProbeCheck(),
			want: heartbeat.CheckPatch{
				Probe: &heartbeat.ProbePatch{}, AccessToken: heartbeat.AccessTokenPatch{Action: heartbeat.AccessTokenClear},
			},
		},
		{
			name: "probe access_token object replaces", stored: sampleProbeCheck(),
			body: `{"probe":{"access_token":{"client_id":"id-1","client_secret":"secret-1"}}}`,
			want: heartbeat.CheckPatch{
				Probe:       &heartbeat.ProbePatch{},
				AccessToken: heartbeat.AccessTokenPatch{Action: heartbeat.AccessTokenReplace, Token: probeToken},
			},
		},
		{
			name: "probe empty object is a non-nil empty patch", body: `{"probe":{}}`, stored: sampleProbeCheck(),
			want: heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{}},
		},
		{
			name: "probe null period leaves the schedule alone", body: `{"period_seconds":null,"probe":{"interval_seconds":90}}`, stored: sampleProbeCheck(),
			want: heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{IntervalSeconds: intPointer(90)}},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			stored := testCase.stored
			if stored.ID == 0 {
				stored = sampleCheck()
			}
			fake := &fakeHeartbeats{getResult: stored, patchResult: stored}

			recorder := serveHeartbeats(fake, nil, http.MethodPatch, "/v1/admin/heartbeats/7", testCase.body)

			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
			}
			want := []patchCall{{id: 7, patch: testCase.want}}
			if !reflect.DeepEqual(fake.patchCalls, want) {
				subTest.Fatalf("patch calls = %+v, want %+v", fake.patchCalls, want)
			}
			if len(fake.getCalls) != 0 {
				subTest.Fatalf("get calls = %v, want none: the merge must happen inside Patch, not in the handler", fake.getCalls)
			}
		})
	}
}

func TestPatchWithNoMutableFieldChangesNothing(testInstance *testing.T) {
	for _, body := range []string{`{}`, `{"period_seconds":null,"destinations":null}`} {
		testInstance.Run(body, func(subTest *testing.T) {
			fake := &fakeHeartbeats{getResult: sampleCheck()}

			recorder := serveHeartbeats(fake, nil, http.MethodPatch, "/v1/admin/heartbeats/7", body)

			assertResponse(subTest, recorder, http.StatusOK, `{"check":`+sampleCheckJSON+`}`+"\n")
			if len(fake.patchCalls) != 1 || !fake.patchCalls[0].patch.IsEmpty() {
				subTest.Fatalf("patch calls = %+v, want one empty patch", fake.patchCalls)
			}
		})
	}
}

func TestPatchRejectsImmutableFields(testInstance *testing.T) {
	for _, body := range []string{
		`{"app_id":"other"}`,
		`{"name":"other"}`,
		`{"app_id":"urls4irl","name":"nightly_backup"}`,
		`{"app_id":null}`,
		`{"period_seconds":120,"name":"other"}`,
	} {
		testInstance.Run(body, func(subTest *testing.T) {
			fake := allResults(sampleCheck())

			recorder := serveHeartbeats(fake, nil, http.MethodPatch, "/v1/admin/heartbeats/7", body)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"app_id and name are immutable"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called for an immutable-field patch")
			}
		})
	}
}

func TestPatchRejectsEachInvalidFieldByName(testInstance *testing.T) {
	probeStored := sampleProbeCheck()
	longValue := strings.Repeat("a", 257)
	tokenBody := func(token string) string { return `{"probe":{"access_token":` + token + `}}` }
	testCases := []struct {
		name     string
		body     string
		wantBody string
		// stored is the check the fake holds; the zero value means sampleCheck().
		stored heartbeat.StoredCheck
		// fake, when set, replaces the default fake built around stored.
		fake *fakeHeartbeats
		// handlerRejects is true when the handler answers before the service.
		handlerRejects bool
	}{
		{name: "probe null", body: `{"probe":null}`, wantBody: `{"error":"check kind is immutable"}`, stored: probeStored, handlerRejects: true},
		{name: "probe null on a push check", body: `{"probe":null}`, wantBody: `{"error":"check kind is immutable"}`, handlerRejects: true},
		{
			name: "probe null on an unknown check", body: `{"probe":null}`, wantBody: `{"error":"check kind is immutable"}`,
			fake: failingHeartbeats(heartbeat.ErrCheckNotFound), handlerRejects: true,
		},
		{name: "half token", body: tokenBody(`{"client_id":"id"}`), wantBody: `{"error":"invalid probe.access_token"}`, stored: probeStored},
		{name: "empty token object", body: tokenBody(`{}`), wantBody: `{"error":"invalid probe.access_token"}`, stored: probeStored},
		{name: "empty client_id", body: tokenBody(`{"client_id":"","client_secret":"s"}`), wantBody: `{"error":"invalid probe.access_token"}`, stored: probeStored},
		{name: "CR in client_id", body: tokenBody(`{"client_id":"a\rb","client_secret":"s"}`), wantBody: `{"error":"invalid probe.access_token"}`, stored: probeStored},
		{name: "LF in client_secret", body: tokenBody(`{"client_id":"id","client_secret":"a\nb"}`), wantBody: `{"error":"invalid probe.access_token"}`, stored: probeStored},
		{name: "257 byte client_id", body: tokenBody(`{"client_id":"` + longValue + `","client_secret":"s"}`), wantBody: `{"error":"invalid probe.access_token"}`, stored: probeStored},
		{name: "257 byte client_secret", body: tokenBody(`{"client_id":"id","client_secret":"` + longValue + `"}`), wantBody: `{"error":"invalid probe.access_token"}`, stored: probeStored},
		{name: "unknown probe key", body: `{"probe":{"failure_threshold":3}}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "unknown access_token key", body: tokenBody(`{"client_id":"id","client_secret":"s","extra":1}`), wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "probe empty statuses", body: `{"probe":{"expected_statuses":[]}}`, wantBody: `{"error":"invalid probe.expected_statuses"}`, stored: probeStored},
		{name: "probe status 99", body: `{"probe":{"expected_statuses":[99]}}`, wantBody: `{"error":"invalid probe.expected_statuses"}`, stored: probeStored},
		{name: "probe status 600", body: `{"probe":{"expected_statuses":[600]}}`, wantBody: `{"error":"invalid probe.expected_statuses"}`, stored: probeStored},
		{name: "probe timeout 0", body: `{"probe":{"timeout_seconds":0}}`, wantBody: `{"error":"invalid probe.timeout_seconds"}`, stored: probeStored},
		{name: "probe timeout 9", body: `{"probe":{"timeout_seconds":9}}`, wantBody: `{"error":"invalid probe.timeout_seconds"}`, stored: probeStored},
		{name: "probe interval 59", body: `{"probe":{"interval_seconds":59}}`, wantBody: `{"error":"invalid probe.interval_seconds"}`, stored: probeStored},
		{name: "probe interval 0", body: `{"probe":{"interval_seconds":0}}`, wantBody: `{"error":"invalid probe.interval_seconds"}`, stored: probeStored},
		{name: "probe http url", body: `{"probe":{"url":"http://"}}`, wantBody: `{"error":"invalid probe.url"}`, stored: probeStored},
		{name: "probe ip literal url", body: `{"probe":{"url":"https://127.0.0.1/"}}`, wantBody: `{"error":"invalid probe.url"}`, stored: probeStored},
		{name: "probe body_match too long", body: `{"probe":{"body_match":"` + longValue + `"}}`, wantBody: `{"error":"invalid probe.body_match"}`, stored: probeStored},
		{name: "probe a string", body: `{"probe":"yes"}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "probe a bool", body: `{"probe":true}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "probe a number", body: `{"probe":5}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "probe an array", body: `{"probe":[]}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "url a number", body: `{"probe":{"url":5}}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "statuses a string", body: `{"probe":{"expected_statuses":"200"}}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "timeout a string", body: `{"probe":{"timeout_seconds":"5"}}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "timeout a fraction", body: `{"probe":{"timeout_seconds":5.5}}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "interval a fraction", body: `{"probe":{"interval_seconds":300.5}}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "body_match a number", body: `{"probe":{"body_match":5}}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "access_token a string", body: tokenBody(`"tok"`), wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "access_token an array", body: tokenBody(`[]`), wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "client_id a number", body: tokenBody(`{"client_id":1,"client_secret":"s"}`), wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "trailing data", body: `{"probe":{}} {}`, wantBody: errBodyInvalidJSON, stored: probeStored, handlerRejects: true},
		{name: "probe object on a push check", body: `{"probe":{"interval_seconds":120}}`, wantBody: `{"error":"probe config is not allowed on a push check"}`},
		{name: "probe token only on a push check", body: tokenBody(`null`), wantBody: `{"error":"probe config is not allowed on a push check"}`},
		{name: "period on a probe check", body: `{"period_seconds":120}`, wantBody: errBodyDerivedSchedule, stored: probeStored},
		{name: "grace on a probe check", body: `{"grace_seconds":120}`, wantBody: errBodyDerivedSchedule, stored: probeStored},
		{name: "period too small", body: `{"period_seconds":59}`, wantBody: `{"error":"invalid period_seconds"}`},
		{name: "period too large", body: `{"period_seconds":2678401}`, wantBody: `{"error":"invalid period_seconds"}`},
		{name: "grace too small", body: `{"grace_seconds":0}`, wantBody: `{"error":"invalid grace_seconds"}`},
		{name: "grace too large", body: `{"grace_seconds":604801}`, wantBody: `{"error":"invalid grace_seconds"}`},
		{name: "empty destinations", body: `{"destinations":[]}`, wantBody: `{"error":"invalid destinations"}`},
		{name: "duplicate destinations", body: `{"destinations":["d","d"]}`, wantBody: `{"error":"invalid destinations"}`},
		{name: "unknown field", body: `{"bogus":1}`, wantBody: `{"error":"invalid JSON body"}`},
		{name: "not json", body: `nope`, wantBody: `{"error":"invalid JSON body"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := testCase.fake
			if fake == nil {
				stored := testCase.stored
				if stored.ID == 0 {
					stored = sampleCheck()
				}
				fake = allResults(stored)
			}

			recorder := serveHeartbeats(fake, nil, http.MethodPatch, "/v1/admin/heartbeats/7", testCase.body)

			assertResponse(subTest, recorder, http.StatusBadRequest, strings.TrimSuffix(testCase.wantBody, "\n")+"\n")
			if testCase.handlerRejects && fake.totalCalls() != 0 {
				subTest.Fatalf("the service was called for a body the handler refuses: %+v", fake.patchCalls)
			}
		})
	}
}

func TestPatchOfAnUnknownCheckIs404(testInstance *testing.T) {
	fake := failingHeartbeats(heartbeat.ErrCheckNotFound)

	recorder := serveHeartbeats(fake, nil, http.MethodPatch, "/v1/admin/heartbeats/7", `{"period_seconds":120}`)

	assertResponse(testInstance, recorder, http.StatusNotFound, `{"error":"check not found"}`+"\n")
	if len(fake.patchCalls) != 1 {
		testInstance.Fatalf("patch calls = %+v, want exactly one", fake.patchCalls)
	}
}

func TestMalformedCheckIDsAreRejectedBeforeTheService(testInstance *testing.T) {
	idRoutes := []heartbeatRoute{
		{name: "get", method: http.MethodGet, path: "/v1/admin/heartbeats/%s"},
		{name: "patch", method: http.MethodPatch, path: "/v1/admin/heartbeats/%s", body: `{"period_seconds":120}`},
		{name: "delete", method: http.MethodDelete, path: "/v1/admin/heartbeats/%s"},
		{name: "pause", method: http.MethodPost, path: "/v1/admin/heartbeats/%s/pause"},
		{name: "resume", method: http.MethodPost, path: "/v1/admin/heartbeats/%s/resume"},
	}
	for _, rawID := range []string{"0", "-1", "+1", "01", "abc", "1.5", "99999999999999999999"} {
		for _, route := range idRoutes {
			testInstance.Run(route.name+"/"+rawID, func(subTest *testing.T) {
				fake := allResults(sampleCheck())

				recorder := serveHeartbeats(fake, nil, route.method, fmt.Sprintf(route.path, rawID), route.body)

				assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid check id"}`+"\n")
				if fake.totalCalls() != 0 {
					subTest.Fatalf("service was called for a malformed id")
				}
			})
		}
	}
}

func TestPassesTheIDToTheService(testInstance *testing.T) {
	fake := allResults(sampleCheck())

	serveHeartbeats(fake, nil, http.MethodGet, "/v1/admin/heartbeats/42", "")
	serveHeartbeats(fake, nil, http.MethodDelete, "/v1/admin/heartbeats/43", "")
	serveHeartbeats(fake, nil, http.MethodPost, "/v1/admin/heartbeats/44/pause", "")
	serveHeartbeats(fake, nil, http.MethodPost, "/v1/admin/heartbeats/45/resume", "")

	if !reflect.DeepEqual(fake.getCalls, []int64{42}) || !reflect.DeepEqual(fake.deleteCalls, []int64{43}) ||
		!reflect.DeepEqual(fake.pauseCalls, []int64{44}) || !reflect.DeepEqual(fake.resumeCalls, []int64{45}) {
		testInstance.Fatalf("calls = get %v delete %v pause %v resume %v", fake.getCalls, fake.deleteCalls, fake.pauseCalls, fake.resumeCalls)
	}
}

func TestWriteHeartbeatErrorBranches(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "validation", err: &heartbeat.ValidationError{Message: "invalid destinations"}, wantStatus: 400, wantBody: `{"error":"invalid destinations"}`},
		{name: "wrapped validation", err: fmt.Errorf("ctx: %w", &heartbeat.ValidationError{Message: "invalid name"}), wantStatus: 400, wantBody: `{"error":"invalid name"}`},
		{name: "check not found", err: heartbeat.ErrCheckNotFound, wantStatus: 404, wantBody: `{"error":"check not found"}`},
		{name: "wrapped check not found", err: fmt.Errorf("get: %w", heartbeat.ErrCheckNotFound), wantStatus: 404, wantBody: `{"error":"check not found"}`},
		{name: "check exists", err: heartbeat.ErrCheckExists, wantStatus: 409, wantBody: `{"error":"check already exists"}`},
		{name: "probe check ping", err: heartbeat.ErrProbeCheckPing, wantStatus: 409, wantBody: `{"error":"check is a probe check"}`},
		{name: "wrapped probe check ping", err: fmt.Errorf("ping: %w", heartbeat.ErrProbeCheckPing), wantStatus: 409, wantBody: `{"error":"check is a probe check"}`},
		{name: "unknown destination", err: registry.ErrUnknownDestination, wantStatus: 400, wantBody: `{"error":"unknown destination"}`},
		{name: "wrapped unknown destination", err: fmt.Errorf("link: %w", registry.ErrUnknownDestination), wantStatus: 400, wantBody: `{"error":"unknown destination"}`},
		{name: "anything else", err: errors.New("boom"), wantStatus: 500, wantBody: `{"error":"internal error"}`},
		{name: "canceled", err: context.Canceled, wantStatus: 500, wantBody: `{"error":"internal error"}`},
		{name: "deadline", err: fmt.Errorf("wait: %w", context.DeadlineExceeded), wantStatus: 500, wantBody: `{"error":"internal error"}`},
	}
	for _, testCase := range testCases {
		for _, route := range allHeartbeatRoutes() {
			testInstance.Run(testCase.name+"/"+route.name, func(subTest *testing.T) {
				recorder := serveHeartbeats(failingHeartbeats(testCase.err), nil, route.method, route.path, route.body)

				assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
			})
		}
	}
}

func TestUnknownDestinationIsDistinctFromAFieldValidationError(testInstance *testing.T) {
	create := serveHeartbeats(failingHeartbeats(registry.ErrUnknownDestination), nil, http.MethodPost, "/v1/admin/heartbeats", validCreateCheckBody)
	assertResponse(testInstance, create, http.StatusBadRequest, `{"error":"unknown destination"}`+"\n")

	fake := &fakeHeartbeats{getResult: sampleCheck(), patchErr: registry.ErrUnknownDestination}
	update := serveHeartbeats(fake, nil, http.MethodPatch, "/v1/admin/heartbeats/7", `{"destinations":["gone"]}`)
	assertResponse(testInstance, update, http.StatusBadRequest, `{"error":"unknown destination"}`+"\n")

	invalid := serveHeartbeats(&fakeHeartbeats{getResult: sampleCheck()}, nil, http.MethodPatch, "/v1/admin/heartbeats/7", `{"destinations":[]}`)
	assertResponse(testInstance, invalid, http.StatusBadRequest, `{"error":"invalid destinations"}`+"\n")
}

func TestHeartbeatInternalErrorsAreLoggedButNeverReturned(testInstance *testing.T) {
	const secretErrText = "insert heartbeat check: connection refused to 10.0.0.5:5432 with credential tk_secret_xyz (SQLSTATE 08006)"
	for _, route := range allHeartbeatRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

			recorder := serveHeartbeats(failingHeartbeats(errors.New(secretErrText)), logger, route.method, route.path, route.body)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if strings.Contains(recorder.Body.String(), "tk_secret_xyz") || strings.Contains(recorder.Body.String(), "10.0.0.5") {
				subTest.Fatalf("response body leaked the real error text: %s", recorder.Body.String())
			}
			if !strings.Contains(logBuffer.String(), secretErrText) {
				subTest.Fatalf("log output = %q, want it to contain the real error text", logBuffer.String())
			}
		})
	}
}

func TestHeartbeatCanceledRequestsAreNotLoggedAtWarnOrAbove(testInstance *testing.T) {
	for _, contextErr := range []error{context.Canceled, fmt.Errorf("wait: %w", context.Canceled)} {
		var logBuffer bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelWarn}))

		recorder := serveHeartbeats(failingHeartbeats(contextErr), logger, http.MethodPost, "/v1/heartbeats/urls4irl/nightly_backup", "")

		assertResponse(testInstance, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
		if logBuffer.Len() != 0 {
			testInstance.Fatalf("%v logged at warn or above: %s", contextErr, logBuffer.String())
		}
	}
}

func TestHeartbeatDeadlineExceededIsLoggedButNeverReturned(testInstance *testing.T) {
	for _, route := range allHeartbeatRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelWarn}))
			deadlineErr := fmt.Errorf("wait: pool acquire at 10.0.0.5:5432: %w", context.DeadlineExceeded)

			recorder := serveHeartbeats(failingHeartbeats(deadlineErr), logger, route.method, route.path, route.body)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if strings.Contains(recorder.Body.String(), "10.0.0.5") {
				subTest.Fatalf("response body leaked the error text: %s", recorder.Body.String())
			}
			if !strings.Contains(logBuffer.String(), "deadline exceeded") || !strings.Contains(logBuffer.String(), "level=WARN") {
				subTest.Fatalf("log output = %q, want a warn line naming the deadline", logBuffer.String())
			}
		})
	}
}

func TestHeartbeatLogsNeverContainTheRequestBody(testInstance *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

	serveHeartbeats(failingHeartbeats(errors.New("boom")), logger, http.MethodPost, "/v1/admin/heartbeats",
		`{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"destinations":["SECRET-DEST"]}`)
	serveHeartbeats(&fakeHeartbeats{}, logger, http.MethodPost, "/v1/admin/heartbeats", `{"destinations":["SECRET-DEST"],"bogus":1}`)

	if strings.Contains(logBuffer.String(), "SECRET-DEST") {
		testInstance.Fatalf("log output leaked a request body: %s", logBuffer.String())
	}
}

func TestNilHeartbeatsFailsClosed(testInstance *testing.T) {
	for _, route := range allHeartbeatRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			recorder := serveHeartbeats(nil, nil, route.method, route.path, route.body)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"heartbeats unavailable"}`+"\n")
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestHeartbeatResponsesAreNotCacheable(testInstance *testing.T) {
	for _, route := range allHeartbeatRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			recorder := serveHeartbeats(allResults(sampleCheck()), nil, route.method, route.path, route.body)

			if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
				subTest.Fatalf("headers = %v, want no-store and nosniff", recorder.Header())
			}
		})
	}
}

func TestHeartbeatRoutesRejectUnsupportedRequests(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "GET ping", method: http.MethodGet, path: "/v1/heartbeats/urls4irl/nightly_backup", wantStatus: http.StatusMethodNotAllowed},
		{name: "PUT ping", method: http.MethodPut, path: "/v1/heartbeats/urls4irl/nightly_backup", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE ping", method: http.MethodDelete, path: "/v1/heartbeats/urls4irl/nightly_backup", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET fail", method: http.MethodGet, path: "/v1/heartbeats/urls4irl/nightly_backup/fail", wantStatus: http.StatusMethodNotAllowed},
		{name: "PUT fail", method: http.MethodPut, path: "/v1/heartbeats/urls4irl/nightly_backup/fail", wantStatus: http.StatusMethodNotAllowed},
		{name: "ping with an extra segment", method: http.MethodPost, path: "/v1/heartbeats/urls4irl/nightly_backup/fail/more", wantStatus: http.StatusNotFound},
		{name: "ping without a name", method: http.MethodPost, path: "/v1/heartbeats/urls4irl", wantStatus: http.StatusNotFound},
		{name: "PUT admin list", method: http.MethodPut, path: "/v1/admin/heartbeats", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE admin list", method: http.MethodDelete, path: "/v1/admin/heartbeats", wantStatus: http.StatusMethodNotAllowed},
		{name: "PUT admin check", method: http.MethodPut, path: "/v1/admin/heartbeats/7", wantStatus: http.StatusMethodNotAllowed},
		{name: "POST admin check", method: http.MethodPost, path: "/v1/admin/heartbeats/7", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET pause", method: http.MethodGet, path: "/v1/admin/heartbeats/7/pause", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET resume", method: http.MethodGet, path: "/v1/admin/heartbeats/7/resume", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE pause", method: http.MethodDelete, path: "/v1/admin/heartbeats/7/pause", wantStatus: http.StatusMethodNotAllowed},
		{name: "admin ping path", method: http.MethodPost, path: "/v1/admin/heartbeats/urls4irl/nightly_backup/fail", wantStatus: http.StatusNotFound},
		{name: "heartbeat list outside admin", method: http.MethodGet, path: "/v1/heartbeats", wantStatus: http.StatusNotFound},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := allResults(sampleCheck())

			recorder := serveHeartbeats(fake, nil, testCase.method, testCase.path, "")

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called for an unsupported request")
			}
		})
	}
}

func TestPingNamedLikeASuffixStillPings(testInstance *testing.T) {
	// A check literally named "fail" is a plain ping, not a /fail ping.
	fake := allResults(sampleCheck())

	recorder := serveHeartbeats(fake, nil, http.MethodPost, "/v1/heartbeats/urls4irl/fail", "")

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !reflect.DeepEqual(fake.pingCalls, []pingCall{{appID: "urls4irl", name: "fail"}}) || len(fake.failCalls) != 0 {
		testInstance.Fatalf("ping calls = %v, fail calls = %v, want one plain ping", fake.pingCalls, fake.failCalls)
	}
}

func TestHeartbeatBodyReadDeadlineIsSetThenCleared(testInstance *testing.T) {
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	server := NewServer(ServerConfig{Heartbeats: allResults(sampleCheck()), Now: func() time.Time { return sampleNow }})
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/heartbeats", strings.NewReader(validCreateCheckBody))

	server.Handler().ServeHTTP(recorder, request)

	if len(recorder.deadlines) != 2 || recorder.deadlines[0].IsZero() || !recorder.deadlines[1].IsZero() {
		testInstance.Fatalf("deadlines set = %v, want a deadline then a clear", recorder.deadlines)
	}
}

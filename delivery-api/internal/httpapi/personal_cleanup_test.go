package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

const (
	cleanupAdminDeprovisionPath = "/v1/admin/deprovision"
	cleanupEmail                = "cleanup.person@example.com"
)

// eventPersonal and the ordered relays record into one shared event list, so a test can assert the local
// cleanup ran before the relay.
type eventPersonal struct {
	*fakePersonal
	events *[]string
}

func (fake *eventPersonal) ForgetPersonForApp(ctx context.Context, appID, personHash string) error {
	*fake.events = append(*fake.events, "cleanup-app:"+appID+":"+personHash)
	return fake.fakePersonal.ForgetPersonForApp(ctx, appID, personHash)
}

func (fake *eventPersonal) ForgetPerson(ctx context.Context, personHash string) error {
	*fake.events = append(*fake.events, "cleanup-person:"+personHash)
	return fake.fakePersonal.ForgetPerson(ctx, personHash)
}

type eventAppProvisioning struct {
	*fakeAppProvisioning
	events *[]string
}

func (fake *eventAppProvisioning) Deprovision(
	ctx context.Context, appID string, target provisionclient.DeprovisionTarget,
) (provisionclient.Passthrough, error) {
	*fake.events = append(*fake.events, "relay")
	return fake.fakeAppProvisioning.Deprovision(ctx, appID, target)
}

type eventAdminProvisioning struct {
	*fakeAdminProvisioning
	events *[]string
}

func (fake *eventAdminProvisioning) Deprovision(
	ctx context.Context, appID string, target provisionclient.DeprovisionTarget,
) (provisionclient.Passthrough, error) {
	*fake.events = append(*fake.events, "relay")
	return fake.fakeAdminProvisioning.Deprovision(ctx, appID, target)
}

func (fake *eventAdminProvisioning) DeleteUser(ctx context.Context, userID string) (provisionclient.Passthrough, error) {
	*fake.events = append(*fake.events, "relay")
	return fake.fakeAdminProvisioning.DeleteUser(ctx, userID)
}

type cleanupRig struct {
	events   *[]string
	personal *fakePersonal
	app      *fakeAppProvisioning
	admin    *fakeAdminProvisioning
	logs     *bytes.Buffer
	server   *Server
}

// newCleanupRig wires both provisioning relays and (unless withoutPersonal) the personal service.
func newCleanupRig(withoutPersonal bool) *cleanupRig {
	okAnswer := provisionclient.Passthrough{Status: http.StatusOK, Body: []byte("{}")}
	events := []string{}
	rig := &cleanupRig{
		events:   &events,
		personal: &fakePersonal{view: samplePersonalView()},
		app:      &fakeAppProvisioning{answer: okAnswer},
		admin:    &fakeAdminProvisioning{answer: okAnswer},
		logs:     &bytes.Buffer{},
	}
	config := ServerConfig{
		AppProvisioning:   &eventAppProvisioning{fakeAppProvisioning: rig.app, events: rig.events},
		AdminProvisioning: &eventAdminProvisioning{fakeAdminProvisioning: rig.admin, events: rig.events},
		AppIdentity:       &fakeAppIdentity{resolved: appidentity.ResolvedKey{ID: 4, AppID: keyApp, Hint: "dak_abcd"}},
		Logger:            slog.New(slog.NewTextHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if !withoutPersonal {
		config.PersonalDestinations = &eventPersonal{fakePersonal: rig.personal, events: rig.events}
	}
	rig.server = NewServer(config)
	return rig
}

func (rig *cleanupRig) do(method, path, body string, asApp bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if asApp {
		request.Header.Set(authorizationHeader, bearerKey)
	}
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAppDeprovisionCleansUpBeforeTheRelay(testInstance *testing.T) {
	hash := personhash.Hash(cleanupEmail)
	testCases := []struct {
		name string
		body string
	}{
		{name: "email", body: `{"email":"` + cleanupEmail + `"}`},
		{name: "person_hash", body: `{"person_hash":"` + hash + `"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newCleanupRig(false)
			recorder := rig.do(http.MethodPost, deprovisionPath, testCase.body, true)
			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			want := []string{"cleanup-app:" + keyApp + ":" + hash, "relay"}
			if !slices.Equal(*rig.events, want) {
				subTest.Fatalf("events = %v, want %v", *rig.events, want)
			}
		})
	}
}

func TestAppDeprovisionLocalFailureIs503WithoutRelayAndRetryCompletes(testInstance *testing.T) {
	hash := personhash.Hash(cleanupEmail)
	rig := newCleanupRig(false)
	rig.personal.forgetErr = errors.New("db down")
	rig.personal.forgetErrOnce = true
	body := `{"email":"` + cleanupEmail + `"}`

	first := rig.do(http.MethodPost, deprovisionPath, body, true)
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), errMsgPersonalCleanupFailed) {
		testInstance.Fatalf("first = %d %s, want 503 cleanup failed", first.Code, first.Body.String())
	}
	if len(rig.app.calls) != 0 {
		testInstance.Fatalf("relay reached provisioning after a failed cleanup: %+v", rig.app.calls)
	}
	if strings.Contains(first.Body.String(), "db down") || strings.Contains(first.Body.String(), cleanupEmail) {
		testInstance.Fatalf("body leaks detail: %s", first.Body.String())
	}

	second := rig.do(http.MethodPost, deprovisionPath, body, true)
	if second.Code != http.StatusOK {
		testInstance.Fatalf("retry = %d %s, want 200", second.Code, second.Body.String())
	}
	want := []string{
		"cleanup-app:" + keyApp + ":" + hash,
		"cleanup-app:" + keyApp + ":" + hash, "relay",
	}
	if !slices.Equal(*rig.events, want) {
		testInstance.Fatalf("events = %v, want %v", *rig.events, want)
	}
}

func TestAppDeprovisionBadBodyNeverCleansUp(testInstance *testing.T) {
	rig := newCleanupRig(false)
	for _, body := range []string{`{}`, `{"person_hash":"nope"}`} {
		recorder := rig.do(http.MethodPost, deprovisionPath, body, true)
		if recorder.Code != http.StatusBadRequest {
			testInstance.Fatalf("body %s: status = %d, want 400", body, recorder.Code)
		}
	}
	if len(*rig.events) != 0 {
		testInstance.Fatalf("events = %v, want none", *rig.events)
	}
}

func TestAdminDeprovisionCleansUpBeforeTheRelay(testInstance *testing.T) {
	hash := personhash.Hash(cleanupEmail)
	rig := newCleanupRig(false)
	recorder := rig.do(http.MethodPost, cleanupAdminDeprovisionPath,
		`{"app_id":"`+adminBoundApp+`","user_id":"`+personhash.NtfyUserFromHash(hash)+`"}`, false)
	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	want := []string{"cleanup-app:" + adminBoundApp + ":" + hash, "relay"}
	if !slices.Equal(*rig.events, want) {
		testInstance.Fatalf("events = %v, want %v", *rig.events, want)
	}
}

func TestAdminDeprovisionLocalFailureIs503WithoutRelay(testInstance *testing.T) {
	hash := personhash.Hash(cleanupEmail)
	rig := newCleanupRig(false)
	rig.personal.forgetErr = errors.New("db down")
	recorder := rig.do(http.MethodPost, cleanupAdminDeprovisionPath,
		`{"app_id":"`+adminBoundApp+`","user_id":"`+personhash.NtfyUserFromHash(hash)+`"}`, false)
	if recorder.Code != http.StatusServiceUnavailable || len(rig.admin.calls) != 0 {
		testInstance.Fatalf("status = %d, relay calls = %v; want 503 and none", recorder.Code, rig.admin.calls)
	}
}

func TestAdminDeleteUserCleansUpEverythingBeforeTheRelay(testInstance *testing.T) {
	hash := personhash.Hash(cleanupEmail)
	rig := newCleanupRig(false)
	recorder := rig.do(http.MethodDelete, "/v1/admin/users/"+personhash.NtfyUserFromHash(hash), "", false)
	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	want := []string{"cleanup-person:" + hash, "relay"}
	if !slices.Equal(*rig.events, want) {
		testInstance.Fatalf("events = %v, want %v", *rig.events, want)
	}
}

func TestAdminDeleteUserLocalFailureIs503WithoutRelay(testInstance *testing.T) {
	rig := newCleanupRig(false)
	rig.personal.forgetErr = errors.New("db down")
	recorder := rig.do(http.MethodDelete, "/v1/admin/users/"+personhash.NtfyUserFromHash(personhash.Hash(cleanupEmail)), "", false)
	if recorder.Code != http.StatusServiceUnavailable || len(rig.admin.calls) != 0 {
		testInstance.Fatalf("status = %d, relay calls = %v; want 503 and none", recorder.Code, rig.admin.calls)
	}
}

func TestAdminNonHashUserIDRelaysOnlyAndLogsTheSkip(testInstance *testing.T) {
	testCases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name: "deprovision", method: http.MethodPost, path: cleanupAdminDeprovisionPath,
			body: `{"app_id":"` + adminBoundApp + `","user_id":"someone"}`,
		},
		{name: "delete-user", method: http.MethodDelete, path: "/v1/admin/users/someone"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newCleanupRig(false)
			recorder := rig.do(testCase.method, testCase.path, testCase.body, false)
			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			if !slices.Equal(*rig.events, []string{"relay"}) {
				subTest.Fatalf("events = %v, want relay only", *rig.events)
			}
			if !strings.Contains(rig.logs.String(), "personal destination cleanup skipped") {
				subTest.Fatalf("skip not logged: %s", rig.logs.String())
			}
		})
	}
}

func TestDeprovisionWithoutPersonalServiceWarnsAndRelaysAsToday(testInstance *testing.T) {
	hash := personhash.Hash(cleanupEmail)
	testCases := []struct {
		name   string
		method string
		path   string
		body   string
		asApp  bool
	}{
		{name: "app", method: http.MethodPost, path: deprovisionPath, body: `{"email":"` + cleanupEmail + `"}`, asApp: true},
		{
			name: "admin deprovision", method: http.MethodPost, path: cleanupAdminDeprovisionPath,
			body: `{"app_id":"` + adminBoundApp + `","user_id":"` + personhash.NtfyUserFromHash(hash) + `"}`,
		},
		{name: "admin delete-user", method: http.MethodDelete, path: "/v1/admin/users/" + personhash.NtfyUserFromHash(hash)},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newCleanupRig(true)
			recorder := rig.do(testCase.method, testCase.path, testCase.body, testCase.asApp)
			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			if !slices.Equal(*rig.events, []string{"relay"}) {
				subTest.Fatalf("events = %v, want relay only", *rig.events)
			}
			if !strings.Contains(rig.logs.String(), "level=WARN") ||
				!strings.Contains(rig.logs.String(), "personal destination cleanup skipped: service not wired") {
				subTest.Fatalf("warning not logged: %s", rig.logs.String())
			}
		})
	}
}

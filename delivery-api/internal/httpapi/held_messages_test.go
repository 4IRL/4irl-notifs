package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

type fakeHeldSummary struct {
	rows []message.HeldAppSummary
	err  error
}

func (fake *fakeHeldSummary) HeldSummary(context.Context) ([]message.HeldAppSummary, error) {
	return fake.rows, fake.err
}

type fakeUnassignedAlerts struct {
	checks []heartbeat.UnassignedCheck
	total  int
	err    error
	limits []int
}

func (fake *fakeUnassignedAlerts) ListUnassignedAlerts(_ context.Context, limit int) ([]heartbeat.UnassignedCheck, int, error) {
	fake.limits = append(fake.limits, limit)
	return fake.checks, fake.total, fake.err
}

const heldMessagesPath = "/v1/admin/held-messages"

// serveHeldMessages builds a server with the given held dependencies. The
// dependencies are passed as interfaces so a nil one stays an untyped nil.
func serveHeldMessages(messages Messages, summary HeldMessageSummarizer, alerts UnassignedAlertLister) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{Messages: messages, HeldSummary: summary, UnassignedAlerts: alerts})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, heldMessagesPath, nil))
	return recorder
}

func TestHeldMessagesReturnsTheExactBody(testInstance *testing.T) {
	zone := time.FixedZone("PLUS2", 2*60*60)
	summary := &fakeHeldSummary{rows: []message.HeldAppSummary{
		{AppID: "urls4irl", Count: 3, OldestHeldAt: adminTimestamp.In(zone)},
		{AppID: "chores4irl", Count: 1, OldestHeldAt: adminTimestamp.Add(-time.Second)},
	}}
	alerts := &fakeUnassignedAlerts{
		checks: []heartbeat.UnassignedCheck{{CheckID: 7, AppID: "urls4irl", Name: "nightly", PendingAlert: "down"}},
		total:  73,
	}

	recorder := serveHeldMessages(&fakeMessages{}, summary, alerts)

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"apps":[`+
			`{"app_id":"urls4irl","count":3,"oldest_at":"`+adminTimestampJSON+`"},`+
			`{"app_id":"chores4irl","count":1,"oldest_at":"2026-10-16T11:59:59Z"}`+
			`],"unassigned_checks":[`+
			`{"check_id":7,"app_id":"urls4irl","name":"nightly","pending_alert":"down"}`+
			`],"unassigned_total":73}`+"\n")
	if !reflect.DeepEqual(alerts.limits, []int{50}) {
		testInstance.Fatalf("ListUnassignedAlerts limits = %v, want [50]", alerts.limits)
	}
}

func TestHeldMessagesIsNeverNull(testInstance *testing.T) {
	recorder := serveHeldMessages(&fakeMessages{}, &fakeHeldSummary{}, &fakeUnassignedAlerts{})

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"apps":[],"unassigned_checks":[],"unassigned_total":0}`+"\n")
}

func TestHeldMessagesPassesTheUncappedTotalThrough(testInstance *testing.T) {
	checks := make([]heartbeat.UnassignedCheck, 50)
	for index := range checks {
		checks[index] = heartbeat.UnassignedCheck{CheckID: int64(index + 1), AppID: "a", Name: "n", PendingAlert: "down"}
	}

	recorder := serveHeldMessages(&fakeMessages{}, &fakeHeldSummary{}, &fakeUnassignedAlerts{checks: checks, total: 73})

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if got := strings.Count(body, `"check_id"`); got != 50 {
		testInstance.Fatalf("listed %d checks, want 50", got)
	}
	if !strings.Contains(body, `"unassigned_total":73`) {
		testInstance.Fatalf("body = %s, want unassigned_total 73", body)
	}
}

func TestHeldMessagesFailsClosedWhenADependencyIsMissing(testInstance *testing.T) {
	testCases := []struct {
		name    string
		summary HeldMessageSummarizer
		alerts  UnassignedAlertLister
	}{
		{name: "no summary", summary: nil, alerts: &fakeUnassignedAlerts{}},
		{name: "no alerts", summary: &fakeHeldSummary{}, alerts: nil},
		{name: "neither", summary: nil, alerts: nil},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveHeldMessages(&fakeMessages{}, testCase.summary, testCase.alerts)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"held messages unavailable"}`+"\n")
		})
	}
}

func TestHeldMessagesStoreErrorsAnswerAGeneric500(testInstance *testing.T) {
	secret := "pq: relation secret_table failed"
	testCases := []struct {
		name    string
		summary *fakeHeldSummary
		alerts  *fakeUnassignedAlerts
	}{
		{name: "summary fails", summary: &fakeHeldSummary{err: errors.New(secret)}, alerts: &fakeUnassignedAlerts{}},
		{name: "alerts fail", summary: &fakeHeldSummary{}, alerts: &fakeUnassignedAlerts{err: errors.New(secret)}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveHeldMessages(&fakeMessages{}, testCase.summary, testCase.alerts)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if strings.Contains(recorder.Body.String(), secret) {
				subTest.Fatalf("body leaked the error text: %s", recorder.Body.String())
			}
		})
	}
}

func TestHeldMessagesWithoutMessagesServiceAnswers503(testInstance *testing.T) {
	recorder := serveHeldMessages(nil, &fakeHeldSummary{}, &fakeUnassignedAlerts{})

	if recorder.Code != http.StatusServiceUnavailable {
		testInstance.Fatalf("status = %d, want 503 from requireMessages", recorder.Code)
	}
}

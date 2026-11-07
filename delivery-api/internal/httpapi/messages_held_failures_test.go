package httpapi

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

func TestListHeldFailuresReturnsTheExactBody(testInstance *testing.T) {
	zone := time.FixedZone("PLUS2", 2*60*60)
	fake := &fakeMessages{
		listHeldFailuresResult: []message.HeldFailure{
			{
				MessageID: scheduledMessageID, AppID: "urls4irl", Channel: "alerts",
				Reason: message.ReleaseFailedReasonInvalid, FailedAt: adminTimestamp.In(zone),
			},
			{
				MessageID: sampleMessageID, AppID: "chores4irl", Channel: "reminders",
				Reason: message.ReleaseFailedReasonInvalid, FailedAt: adminTimestamp.Add(-time.Second),
			},
		},
		listHeldFailuresTotal: 17,
	}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/held-failures?limit=2", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"failures":[`+
			`{"message_id":"`+scheduledMessageID+`","app_id":"urls4irl","channel":"alerts",`+
			`"reason":"release_invalid","failed_at":"`+adminTimestampJSON+`"},`+
			`{"message_id":"`+sampleMessageID+`","app_id":"chores4irl","channel":"reminders",`+
			`"reason":"release_invalid","failed_at":"2026-10-16T11:59:59Z"}`+
			`],"total":17}`+"\n")
	if !reflect.DeepEqual(fake.listHeldFailuresCalls, []int{2}) {
		testInstance.Fatalf("ListHeldFailures calls = %v, want [2]", fake.listHeldFailuresCalls)
	}
}

func TestListHeldFailuresIsNeverNull(testInstance *testing.T) {
	recorder := serveMessages(&fakeMessages{}, nil, http.MethodGet, "/v1/admin/held-failures", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"failures":[],"total":0}`+"\n")
}

func TestListHeldFailuresLimitParsing(testInstance *testing.T) {
	testCases := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{name: "default", query: "", wantLimit: 50},
		{name: "explicit", query: "?limit=200", wantLimit: 200},
		{name: "non-integer goes out of range", query: "?limit=abc", wantLimit: -1},
		{name: "zero is passed through", query: "?limit=0", wantLimit: 0},
		{name: "over the cap is passed through", query: "?limit=201", wantLimit: 201},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{}

			recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/held-failures"+testCase.query, "")

			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d, want 200 from the fake", recorder.Code)
			}
			if !reflect.DeepEqual(fake.listHeldFailuresCalls, []int{testCase.wantLimit}) {
				subTest.Fatalf("ListHeldFailures calls = %v, want [%d]", fake.listHeldFailuresCalls, testCase.wantLimit)
			}
		})
	}
}

func TestListHeldFailuresRealServiceRejectsBadLimit(testInstance *testing.T) {
	service := message.NewService(message.ServiceConfig{Store: &listStore{}, Resolver: unusedResolver{}})
	for _, query := range []string{"?limit=0", "?limit=201", "?limit=abc"} {
		testInstance.Run(query, func(subTest *testing.T) {
			recorder := serveMessages(service, nil, http.MethodGet, "/v1/admin/held-failures"+query, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid limit"}`+"\n")
		})
	}
}

func TestListHeldFailuresStoreFailureIsInternalError(testInstance *testing.T) {
	const secretErrText = "list held failures: connection refused to 10.0.0.5:5432"
	recorder := serveMessages(
		failingMessages(errors.New(secretErrText)), nil, http.MethodGet, "/v1/admin/held-failures", "")

	assertResponse(testInstance, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
	if strings.Contains(recorder.Body.String(), "10.0.0.5") {
		testInstance.Fatalf("body leaked the underlying error: %s", recorder.Body.String())
	}
}

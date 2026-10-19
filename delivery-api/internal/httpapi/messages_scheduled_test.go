package httpapi

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

const (
	scheduledMessageID = "7b1c2d3e-4f50-4a61-8b72-9c8d7e6f5a4b"
	scheduledAppID     = "urls4irl"
	futureDeliverAt    = "2031-01-02T03:04:05Z"
)

var futureDeliverAtTime = time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)

// scheduledStoredMessage is a message with one scheduled delivery. The delivery
// carries a non-zero NextAttemptAt, which the view must suppress.
func scheduledStoredMessage(deliverAt time.Time) message.StoredMessage {
	stored := sampleStoredMessage()
	stored.ID = scheduledMessageID
	stored.Envelope.DeliverAt = &deliverAt
	stored.Deliveries = []message.Delivery{
		{ID: 21, DestinationName: "a_hook", State: message.StateScheduled, NextAttemptAt: deliverAt},
	}
	return stored
}

func cancelledStoredMessage(cancelledAt time.Time) message.StoredMessage {
	stored := scheduledStoredMessage(futureDeliverAtTime)
	stored.CancelledAt = &cancelledAt
	stored.Deliveries[0].State = message.StateCancelled
	return stored
}

func submitBodyWithDeliverAt(deliverAt string) string {
	return `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1","payload":{"a":1},"deliver_at":` + deliverAt + `}`
}

func TestSubmitPassesDeliverAtToTheService(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage()}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", submitBodyWithDeliverAt(`"2031-01-02T05:04:05+02:00"`))

	if recorder.Code != http.StatusAccepted {
		testInstance.Fatalf("status = %d, want 202; body = %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.submitCalls) != 1 || fake.submitCalls[0].DeliverAt == nil {
		testInstance.Fatalf("Submit calls = %+v, want one with deliver_at set", fake.submitCalls)
	}
	if !fake.submitCalls[0].DeliverAt.Equal(futureDeliverAtTime) {
		testInstance.Fatalf("DeliverAt = %v, want the instant %v", fake.submitCalls[0].DeliverAt, futureDeliverAtTime)
	}
}

func TestSubmitNullDeliverAtIsAnImmediateSend(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage()}

	serveMessages(fake, nil, http.MethodPost, "/v1/messages", submitBodyWithDeliverAt(`null`))

	if len(fake.submitCalls) != 1 || fake.submitCalls[0].DeliverAt != nil {
		testInstance.Fatalf("Submit calls = %+v, want one with a nil deliver_at", fake.submitCalls)
	}
}

func TestSubmitScheduledAnswersAtOnceWithoutAwaiting(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: scheduledStoredMessage(futureDeliverAtTime), submitScheduled: true}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages?wait=10", submitBodyWithDeliverAt(`"`+futureDeliverAt+`"`))

	wantMessage := `{"message_id":"` + scheduledMessageID + `","app_id":"urls4irl","channel":"alerts",` +
		`"idempotency_key":"key-1","critical":true,"status":"scheduled","deliver_at":"` + futureDeliverAt + `",` +
		`"created_at":"` + adminTimestampJSON + `","cancelled_at":null,` +
		`"deliveries":[{"id":21,"destination":"a_hook","state":"scheduled","dead_reason":null,"attempt_count":0,` +
		`"last_status_code":null,"next_attempt_at":null,"delivered_at":null}]}`
	assertResponse(testInstance, recorder, http.StatusAccepted,
		`{"message":`+wantMessage+`,"duplicate":false,"replaced":false}`+"\n")
	if len(fake.awaitCalls) != 0 {
		testInstance.Fatalf("Await calls = %d, want 0 for a scheduled result", len(fake.awaitCalls))
	}
}

func TestSubmitReportsReplaced(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: scheduledStoredMessage(futureDeliverAtTime), submitReplaced: true, submitScheduled: true}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", submitBodyWithDeliverAt(`"`+futureDeliverAt+`"`))

	if recorder.Code != http.StatusAccepted {
		testInstance.Fatalf("status = %d, want 202", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"message_id":"`+scheduledMessageID+`"`) ||
		!strings.HasSuffix(recorder.Body.String(), `"duplicate":false,"replaced":true}`+"\n") {
		testInstance.Fatalf("body = %s, want the original message id with replaced true", recorder.Body.String())
	}
}

func TestSubmitReplaceToAnImmediateSendHonoursWait(testInstance *testing.T) {
	// A replace whose new deliver_at is absent or past releases at once: the result
	// is not scheduled, so ?wait= applies.
	fake := &fakeMessages{
		submitResult: sampleStoredMessage(), submitReplaced: true, awaitResult: sampleStoredMessage(),
	}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages?wait=3", validSubmitBody)

	if !strings.HasSuffix(recorder.Body.String(), `"duplicate":false,"replaced":true}`+"\n") {
		testInstance.Fatalf("body = %s, want replaced true", recorder.Body.String())
	}
	wantCalls := []awaitCall{{appID: "urls4irl", id: sampleMessageID, timeout: 3 * time.Second}}
	if !reflect.DeepEqual(fake.awaitCalls, wantCalls) {
		testInstance.Fatalf("Await calls = %+v, want %+v", fake.awaitCalls, wantCalls)
	}
}

func TestSubmitOverTheScheduledCapIs429(testInstance *testing.T) {
	fake := &fakeMessages{submitErr: message.ErrTooManyScheduled}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", submitBodyWithDeliverAt(`"`+futureDeliverAt+`"`))

	assertResponse(testInstance, recorder, http.StatusTooManyRequests, `{"error":"too many scheduled messages"}`+"\n")
}

func TestSubmitOverTheScheduledTotalCapIs429WithADistinctBody(testInstance *testing.T) {
	fake := &fakeMessages{submitErr: fmt.Errorf("insert: %w", message.ErrScheduledCapacityReached)}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", submitBodyWithDeliverAt(`"`+futureDeliverAt+`"`))

	assertResponse(testInstance, recorder, http.StatusTooManyRequests, `{"error":"scheduled message capacity reached"}`+"\n")
}

// TestSubmitRejectsBadDeliverAt uses the real service over a store that panics if
// reached, so the 400 is proven to come from validation alone.
func TestSubmitRejectsBadDeliverAt(testInstance *testing.T) {
	service := message.NewService(message.ServiceConfig{Store: &listStore{}, Resolver: unusedResolver{}})
	testCases := []struct {
		name     string
		deliver  string
		wantBody string
	}{
		{name: "zero time", deliver: `"0001-01-01T00:00:00Z"`, wantBody: `{"error":"invalid deliver_at"}`},
		{name: "zero time through an offset", deliver: `"0001-01-01T01:00:00+01:00"`, wantBody: `{"error":"invalid deliver_at"}`},
		{name: "year below 1 in UTC", deliver: `"0001-01-01T00:00:00+01:00"`, wantBody: `{"error":"invalid deliver_at"}`},
		{name: "after the upper bound in UTC", deliver: `"9999-12-31T23:59:59-01:00"`, wantBody: `{"error":"invalid deliver_at"}`},
		{name: "five digit year is malformed", deliver: `"10000-01-01T00:00:00Z"`, wantBody: `{"error":"invalid JSON body"}`},
		{name: "not a timestamp", deliver: `"tomorrow"`, wantBody: `{"error":"invalid JSON body"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveMessages(service, nil, http.MethodPost, "/v1/messages", submitBodyWithDeliverAt(testCase.deliver))

			assertResponse(subTest, recorder, http.StatusBadRequest, testCase.wantBody+"\n")
		})
	}
}

func TestMessageViewShowsDeliverAtAndCancelledAtInUTC(testInstance *testing.T) {
	zone := time.FixedZone("PLUS2", 2*60*60)
	cancelledAt := adminTimestamp.Add(time.Hour)
	stored := cancelledStoredMessage(cancelledAt.In(zone))
	stored.Envelope.DeliverAt = timePointer(futureDeliverAtTime.In(zone))
	fake := &fakeMessages{getResult: stored}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/messages/"+scheduledMessageID+"?app_id=urls4irl", "")

	wantMessage := `{"message_id":"` + scheduledMessageID + `","app_id":"urls4irl","channel":"alerts",` +
		`"idempotency_key":"key-1","critical":true,"status":"cancelled","deliver_at":"` + futureDeliverAt + `",` +
		`"created_at":"` + adminTimestampJSON + `","cancelled_at":"2026-10-16T13:00:00Z",` +
		`"deliveries":[{"id":21,"destination":"a_hook","state":"cancelled","dead_reason":null,"attempt_count":0,` +
		`"last_status_code":null,"next_attempt_at":null,"delivered_at":null}]}`
	assertResponse(testInstance, recorder, http.StatusOK, `{"message":`+wantMessage+`}`+"\n")
}

func TestCancelMessageByIDOutcomes(testInstance *testing.T) {
	testCases := []struct {
		name     string
		outcome  message.CancelOutcome
		wantBody string
	}{
		{
			name:     "cancelled now",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusCancelled, Cancelled: true, Transitioned: true, Found: true},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":true,"status":"cancelled"}`,
		},
		{
			name:     "already cancelled",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusCancelled, Cancelled: true, Found: true},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":true,"status":"cancelled"}`,
		},
		{
			name:     "already released",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusDelivered, Found: true},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":false,"status":"delivered"}`,
		},
		{
			name:     "not found",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusNotFound},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":false,"status":"not_found"}`,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{cancelResult: testCase.outcome}

			recorder := serveMessages(fake, nil, http.MethodDelete, "/v1/messages/"+scheduledMessageID+"?app_id=urls4irl", "")

			assertResponse(subTest, recorder, http.StatusOK, testCase.wantBody+"\n")
			wantCalls := []appAndID{{appID: scheduledAppID, id: scheduledMessageID}}
			if !reflect.DeepEqual(fake.cancelCalls, wantCalls) {
				subTest.Fatalf("Cancel calls = %+v, want %+v", fake.cancelCalls, wantCalls)
			}
		})
	}
}

func TestCancelMessageByKeyOutcomes(testInstance *testing.T) {
	testCases := []struct {
		name     string
		outcome  message.CancelOutcome
		wantBody string
	}{
		{
			name:     "cancelled now",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusCancelled, Cancelled: true, Transitioned: true, Found: true},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":true,"status":"cancelled"}`,
		},
		{
			name:     "released",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusPartial, Found: true},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":false,"status":"partial"}`,
		},
		{
			name:     "unknown key echoes an empty id",
			outcome:  message.CancelOutcome{Status: message.StatusNotFound},
			wantBody: `{"message_id":"","cancelled":false,"status":"not_found"}`,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{cancelByKeyResult: testCase.outcome}

			recorder := serveMessages(fake, nil, http.MethodDelete, "/v1/messages?app_id=urls4irl&idempotency_key=key-1", "")

			assertResponse(subTest, recorder, http.StatusOK, testCase.wantBody+"\n")
			wantCalls := []appAndID{{appID: scheduledAppID, id: "key-1"}}
			if !reflect.DeepEqual(fake.cancelByKeyCalls, wantCalls) {
				subTest.Fatalf("CancelByKey calls = %+v, want %+v", fake.cancelByKeyCalls, wantCalls)
			}
		})
	}
}

func TestCancelRequiresItsParamsBeforeTheService(testInstance *testing.T) {
	testCases := []struct {
		name     string
		path     string
		wantBody string
	}{
		{name: "by id without app_id", path: "/v1/messages/" + scheduledMessageID, wantBody: `{"error":"app_id is required"}`},
		{name: "by id with empty app_id", path: "/v1/messages/" + scheduledMessageID + "?app_id=", wantBody: `{"error":"app_id is required"}`},
		{name: "by key without anything", path: "/v1/messages", wantBody: `{"error":"app_id and idempotency_key are required"}`},
		{name: "by key without key", path: "/v1/messages?app_id=urls4irl", wantBody: `{"error":"app_id and idempotency_key are required"}`},
		{name: "by key without app_id", path: "/v1/messages?idempotency_key=key-1", wantBody: `{"error":"app_id and idempotency_key are required"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{}

			recorder := serveMessages(fake, nil, http.MethodDelete, testCase.path, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, testCase.wantBody+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called for a rejected cancel")
			}
		})
	}
}

// TestCancelWithTheRealServiceHandlesMalformedAndMissInputs uses a store that
// panics if reached: a malformed input is a 400 and a non-UUID id is a 200
// not_found, both without touching the store.
func TestCancelWithTheRealServiceHandlesMalformedAndMissInputs(testInstance *testing.T) {
	service := message.NewService(message.ServiceConfig{Store: &listStore{}, Resolver: unusedResolver{}})
	testCases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name: "malformed app_id by id", method: http.MethodDelete, wantStatus: http.StatusBadRequest,
			path: "/v1/messages/" + scheduledMessageID + "?app_id=BAD%20APP", wantBody: `{"error":"invalid app_id"}`,
		},
		{
			name: "malformed app_id by key", method: http.MethodDelete, wantStatus: http.StatusBadRequest,
			path: "/v1/messages?app_id=BAD%20APP&idempotency_key=key-1", wantBody: `{"error":"invalid app_id"}`,
		},
		{
			name: "malformed key", method: http.MethodDelete, wantStatus: http.StatusBadRequest,
			path: "/v1/messages?app_id=urls4irl&idempotency_key=bad%20key", wantBody: `{"error":"invalid idempotency_key"}`,
		},
		{
			name: "non-UUID id", method: http.MethodDelete, wantStatus: http.StatusOK,
			path:     "/v1/messages/not-a-uuid?app_id=urls4irl",
			wantBody: `{"message_id":"not-a-uuid","cancelled":false,"status":"not_found"}`,
		},
		{
			name: "non-UUID admin id", method: http.MethodDelete, wantStatus: http.StatusOK,
			path:     "/v1/admin/scheduled-messages/not-a-uuid",
			wantBody: `{"message_id":"not-a-uuid","cancelled":false,"status":"not_found"}`,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveMessages(service, nil, testCase.method, testCase.path, "")

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
		})
	}
}

func TestAdminCancelMessageReturnsTheOutcomeUnscopedToAnApp(testInstance *testing.T) {
	testCases := []struct {
		name     string
		outcome  message.CancelOutcome
		wantBody string
	}{
		{
			name:     "cancelled now",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusCancelled, Cancelled: true, Transitioned: true, Found: true},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":true,"status":"cancelled"}`,
		},
		{
			name:     "released",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusDead, Found: true},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":false,"status":"dead"}`,
		},
		{
			name:     "not found",
			outcome:  message.CancelOutcome{MessageID: scheduledMessageID, Status: message.StatusNotFound},
			wantBody: `{"message_id":"` + scheduledMessageID + `","cancelled":false,"status":"not_found"}`,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{adminCancelResult: testCase.outcome}

			recorder := serveMessages(fake, nil, http.MethodDelete, "/v1/admin/scheduled-messages/"+scheduledMessageID, "")

			assertResponse(subTest, recorder, http.StatusOK, testCase.wantBody+"\n")
			if !reflect.DeepEqual(fake.adminCancelCalls, []string{scheduledMessageID}) {
				subTest.Fatalf("AdminCancel calls = %v, want [%s]", fake.adminCancelCalls, scheduledMessageID)
			}
			if len(fake.cancelCalls) != 0 || len(fake.cancelByKeyCalls) != 0 {
				subTest.Fatalf("the app-scoped cancel was used for an admin cancel")
			}
		})
	}
}

func TestListScheduledMessagesReturnsTheExactBody(testInstance *testing.T) {
	zone := time.FixedZone("PLUS2", 2*60*60)
	fake := &fakeMessages{
		listScheduledResult: []message.ScheduledMessage{
			{
				MessageID: scheduledMessageID, AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1",
				DeliverAt: futureDeliverAtTime.In(zone), CreatedAt: adminTimestamp.In(zone),
				Destinations: []message.ScheduledDestination{{Name: "ops_discord", Type: "discord"}, {Name: "b_hook", Type: "webhook"}},
			},
			{
				MessageID: sampleMessageID, AppID: "chores4irl", Channel: "reminders", IdempotencyKey: "key-2",
				DeliverAt: futureDeliverAtTime.Add(time.Hour), CreatedAt: adminTimestamp, Recipient: true,
			},
		},
		listScheduledTotal: 312,
	}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/scheduled-messages?limit=2", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"scheduled_messages":[`+
			`{"message_id":"`+scheduledMessageID+`","app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1",`+
			`"deliver_at":"`+futureDeliverAt+`","created_at":"`+adminTimestampJSON+`","recipient":false,`+
			`"destinations":[{"name":"ops_discord","type":"discord"},{"name":"b_hook","type":"webhook"}]},`+
			`{"message_id":"`+sampleMessageID+`","app_id":"chores4irl","channel":"reminders","idempotency_key":"key-2",`+
			`"deliver_at":"2031-01-02T04:04:05Z","created_at":"`+adminTimestampJSON+`","recipient":true,"destinations":[]}`+
			`],"total":312}`+"\n")
	if !reflect.DeepEqual(fake.listScheduledCalls, []int{2}) {
		testInstance.Fatalf("ListScheduled calls = %v, want [2]", fake.listScheduledCalls)
	}
}

func TestListScheduledMessagesIsNeverNull(testInstance *testing.T) {
	fake := &fakeMessages{}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/scheduled-messages", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"scheduled_messages":[],"total":0}`+"\n")
}

func TestListScheduledMessagesLimitParsing(testInstance *testing.T) {
	testCases := []struct {
		name      string
		query     string
		wantLimit int
	}{
		{name: "default", query: "", wantLimit: 50},
		{name: "empty limit", query: "?limit=", wantLimit: 50},
		{name: "explicit", query: "?limit=200", wantLimit: 200},
		{name: "non-integer goes out of range", query: "?limit=abc", wantLimit: -1},
		{name: "overflowing goes out of range", query: "?limit=99999999999999999999", wantLimit: -1},
		{name: "zero is passed through", query: "?limit=0", wantLimit: 0},
		{name: "over the cap is passed through", query: "?limit=201", wantLimit: 201},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{}

			recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/scheduled-messages"+testCase.query, "")

			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d, want 200 from the fake", recorder.Code)
			}
			if !reflect.DeepEqual(fake.listScheduledCalls, []int{testCase.wantLimit}) {
				subTest.Fatalf("ListScheduled calls = %v, want [%d]", fake.listScheduledCalls, testCase.wantLimit)
			}
		})
	}
}

func TestListScheduledMessagesRealServiceRejectsBadLimit(testInstance *testing.T) {
	service := message.NewService(message.ServiceConfig{Store: &listStore{}, Resolver: unusedResolver{}})
	for _, query := range []string{"?limit=0", "?limit=201", "?limit=-5", "?limit=abc"} {
		testInstance.Run(query, func(subTest *testing.T) {
			recorder := serveMessages(service, nil, http.MethodGet, "/v1/admin/scheduled-messages"+query, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid limit"}`+"\n")
		})
	}
}

func TestListDeliveriesRejectsScheduledAndCancelledStates(testInstance *testing.T) {
	service := message.NewService(message.ServiceConfig{Store: &listStore{}, Resolver: unusedResolver{}})
	for _, state := range []string{message.StateScheduled, message.StateCancelled} {
		testInstance.Run(state, func(subTest *testing.T) {
			recorder := serveMessages(service, nil, http.MethodGet, "/v1/admin/deliveries?state="+state, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid state"}`+"\n")
		})
	}
}

func TestCancelAndScheduledResponsesAreNotCacheable(testInstance *testing.T) {
	fake := &fakeMessages{}
	for _, request := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/messages/" + scheduledMessageID + "?app_id=urls4irl"},
		{http.MethodDelete, "/v1/messages?app_id=urls4irl&idempotency_key=key-1"},
		{http.MethodGet, "/v1/admin/scheduled-messages"},
		{http.MethodDelete, "/v1/admin/scheduled-messages/" + scheduledMessageID},
	} {
		recorder := serveMessages(fake, nil, request.method, request.path, "")
		if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			testInstance.Fatalf("%s %s headers = %v, want no-store and nosniff", request.method, request.path, recorder.Header())
		}
	}
}

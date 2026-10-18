package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The real message service must satisfy the consumer-side interface.
var _ Messages = (*message.Service)(nil)

const (
	sampleMessageID = "5a3e0f0c-9d1b-4c3a-8f2e-0123456789ab"
	validSubmitBody = `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1","payload":{"a":1}}`
)

// fakeMessages is a scripted Messages. Each method returns its <name>Result and
// <name>Err and records its arguments in <name>Calls.
type fakeMessages struct {
	submitResult    message.StoredMessage
	submitDuplicate bool
	submitErr       error
	submitCalls     []message.Envelope

	getResult message.StoredMessage
	getErr    error
	getCalls  []appAndID

	getByKeyResult message.StoredMessage
	getByKeyErr    error
	getByKeyCalls  []appAndID

	awaitResult message.StoredMessage
	awaitErr    error
	awaitCalls  []awaitCall

	listDeliveriesResult []message.AdminDelivery
	listDeliveriesErr    error
	listDeliveriesCalls  []listDeliveriesCall

	listAttemptsResult []message.Attempt
	listAttemptsErr    error
	listAttemptsCalls  []int64

	replayResult message.AdminDelivery
	replayErr    error
	replayCalls  []int64
}

type appAndID struct {
	appID string
	id    string
}

type awaitCall struct {
	appID   string
	id      string
	timeout time.Duration
}

type listDeliveriesCall struct {
	state string
	limit int
}

func (fake *fakeMessages) Submit(_ context.Context, envelope message.Envelope) (message.StoredMessage, bool, error) {
	fake.submitCalls = append(fake.submitCalls, envelope)
	return fake.submitResult, fake.submitDuplicate, fake.submitErr
}

func (fake *fakeMessages) Get(_ context.Context, appID string, id string) (message.StoredMessage, error) {
	fake.getCalls = append(fake.getCalls, appAndID{appID: appID, id: id})
	return fake.getResult, fake.getErr
}

func (fake *fakeMessages) GetByKey(_ context.Context, appID string, idempotencyKey string) (message.StoredMessage, error) {
	fake.getByKeyCalls = append(fake.getByKeyCalls, appAndID{appID: appID, id: idempotencyKey})
	return fake.getByKeyResult, fake.getByKeyErr
}

func (fake *fakeMessages) Await(_ context.Context, appID string, id string, timeout time.Duration) (message.StoredMessage, error) {
	fake.awaitCalls = append(fake.awaitCalls, awaitCall{appID: appID, id: id, timeout: timeout})
	return fake.awaitResult, fake.awaitErr
}

func (fake *fakeMessages) ListDeliveries(_ context.Context, state string, limit int) ([]message.AdminDelivery, error) {
	fake.listDeliveriesCalls = append(fake.listDeliveriesCalls, listDeliveriesCall{state: state, limit: limit})
	return fake.listDeliveriesResult, fake.listDeliveriesErr
}

func (fake *fakeMessages) ListAttempts(_ context.Context, deliveryID int64) ([]message.Attempt, error) {
	fake.listAttemptsCalls = append(fake.listAttemptsCalls, deliveryID)
	return fake.listAttemptsResult, fake.listAttemptsErr
}

func (fake *fakeMessages) Replay(_ context.Context, deliveryID int64) (message.AdminDelivery, error) {
	fake.replayCalls = append(fake.replayCalls, deliveryID)
	return fake.replayResult, fake.replayErr
}

func (fake *fakeMessages) totalCalls() int {
	return len(fake.submitCalls) + len(fake.getCalls) + len(fake.getByKeyCalls) + len(fake.awaitCalls) +
		len(fake.listDeliveriesCalls) + len(fake.listAttemptsCalls) + len(fake.replayCalls)
}

func failingMessages(err error) *fakeMessages {
	return &fakeMessages{
		submitErr: err, getErr: err, getByKeyErr: err, awaitErr: err,
		listDeliveriesErr: err, listAttemptsErr: err, replayErr: err,
	}
}

// serveMessages sends one request to a Server built around fake and returns the
// recorder.
func serveMessages(fake Messages, logger *slog.Logger, method, path, body string) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{Messages: fake, Logger: logger})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func intPointer(value int) *int { return &value }

func stringPointer(value string) *string { return &value }

func timePointer(value time.Time) *time.Time { return &value }

// sampleStoredMessage has one delivery per state. The dead and delivered ones
// carry a stale non-zero NextAttemptAt, which the view must suppress.
func sampleStoredMessage() message.StoredMessage {
	return message.StoredMessage{
		ID: sampleMessageID,
		Envelope: message.Envelope{
			AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1", Critical: true,
			Payload: json.RawMessage(`{"secret":"never echoed"}`),
			Display: &message.Display{Title: "never echoed"},
		},
		CreatedAt: adminTimestamp,
		Deliveries: []message.Delivery{
			{
				ID: 11, DestinationName: "a_hook", State: message.StatePending, AttemptCount: 1,
				LastStatusCode: intPointer(503), NextAttemptAt: adminTimestamp.Add(time.Minute),
			},
			{
				ID: 12, DestinationName: "b_hook", State: message.StateDead, AttemptCount: 2,
				DeadReason: stringPointer(message.DeadReasonTerminalStatus), LastStatusCode: intPointer(400),
				NextAttemptAt: adminTimestamp,
			},
			{
				ID: 13, DestinationName: "c_hook", State: message.StateDelivered, AttemptCount: 1,
				LastStatusCode: intPointer(200), DeliveredAt: timePointer(adminTimestamp), NextAttemptAt: adminTimestamp,
			},
		},
	}
}

const (
	pendingDeliveryJSON = `{"id":11,"destination":"a_hook","state":"pending","dead_reason":null,"attempt_count":1,` +
		`"last_status_code":503,"next_attempt_at":"2026-10-16T12:01:00Z","delivered_at":null}`
	deadDeliveryJSON = `{"id":12,"destination":"b_hook","state":"dead","dead_reason":"terminal_status","attempt_count":2,` +
		`"last_status_code":400,"next_attempt_at":null,"delivered_at":null}`
	deliveredDeliveryJSON = `{"id":13,"destination":"c_hook","state":"delivered","dead_reason":null,"attempt_count":1,` +
		`"last_status_code":200,"next_attempt_at":null,"delivered_at":"` + adminTimestampJSON + `"}`
	sampleMessageJSON = `{"message_id":"` + sampleMessageID + `","app_id":"urls4irl","channel":"alerts",` +
		`"idempotency_key":"key-1","critical":true,"status":"pending","created_at":"` + adminTimestampJSON + `",` +
		`"deliveries":[` + pendingDeliveryJSON + `,` + deadDeliveryJSON + `,` + deliveredDeliveryJSON + `]}`
)

func TestSubmitReturns202WithTheMessageView(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage()}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", validSubmitBody)

	assertResponse(testInstance, recorder, http.StatusAccepted, `{"message":`+sampleMessageJSON+`,"duplicate":false}`+"\n")
	if len(fake.awaitCalls) != 0 {
		testInstance.Fatalf("Await calls = %d, want 0 without ?wait", len(fake.awaitCalls))
	}
}

func TestSubmitReportsDuplicate(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage(), submitDuplicate: true}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages?wait=5", validSubmitBody)

	assertResponse(testInstance, recorder, http.StatusAccepted, `{"message":`+sampleMessageJSON+`,"duplicate":true}`+"\n")
	if len(fake.awaitCalls) != 0 {
		testInstance.Fatalf("Await calls = %d, want 0 for a duplicate", len(fake.awaitCalls))
	}
}

func TestSubmitPassesDecodedEnvelopeToTheService(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage()}
	body := `{"app_id":"urls4irl","to":{"destination":"ops_webhook"},"channel":"alerts","idempotency_key":"key-1",` +
		`"critical":true,"payload":{"nested":{"k":[1,2]}},"display":{"title":"T","body":"B","url":"https://example.com","priority":"high"}}`

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", body)

	if recorder.Code != http.StatusAccepted {
		testInstance.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := message.Envelope{
		AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1", ToDestination: "ops_webhook", Critical: true,
		Payload: json.RawMessage(`{"nested":{"k":[1,2]}}`),
		Display: &message.Display{Title: "T", Body: "B", URL: "https://example.com", Priority: "high"},
	}
	if len(fake.submitCalls) != 1 || !reflect.DeepEqual(fake.submitCalls[0], want) {
		testInstance.Fatalf("Submit calls = %+v, want one with %+v", fake.submitCalls, want)
	}
}

func TestSubmitAcceptsARecipientBody(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage()}
	body := `{"app_id":"urls4irl","to":{"recipient":"alice@example.com"},"channel":"alerts","idempotency_key":"key-1",` +
		`"payload":{},"display":{"title":"T"}}`

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", body)

	if recorder.Code != http.StatusAccepted {
		testInstance.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(fake.submitCalls) != 1 || fake.submitCalls[0].ToRecipient != "alice@example.com" {
		testInstance.Fatalf("Submit calls = %+v, want the recipient forwarded to the service", fake.submitCalls)
	}
}

func TestSubmitPassesRecipientAndDeliverAtToValidation(testInstance *testing.T) {
	fake := &fakeMessages{submitErr: &message.ValidationError{Message: "to.recipient must be an email or person_hash"}}
	body := `{"app_id":"urls4irl","to":{"recipient":"someone"},"channel":"alerts","idempotency_key":"key-1",` +
		`"deliver_at":"2027-01-01T00:00:00Z","payload":{}}`

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", body)

	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"to.recipient must be an email or person_hash"}`+"\n")
	if len(fake.submitCalls) != 1 || fake.submitCalls[0].ToRecipient != "someone" || fake.submitCalls[0].DeliverAt == nil {
		testInstance.Fatalf("Submit calls = %+v, want the recipient and deliver_at forwarded", fake.submitCalls)
	}
}

func TestSubmitWaitAwaitsWithTheClampedTimeout(testInstance *testing.T) {
	testCases := []struct {
		name        string
		query       string
		wantTimeout time.Duration
		wantAwait   bool
	}{
		{name: "no wait", query: "", wantAwait: false},
		{name: "empty wait", query: "?wait=", wantAwait: false},
		{name: "zero skips await", query: "?wait=0", wantAwait: false},
		{name: "one second", query: "?wait=1", wantTimeout: time.Second, wantAwait: true},
		{name: "ten seconds", query: "?wait=10", wantTimeout: 10 * time.Second, wantAwait: true},
		{name: "eleven clamps to ten", query: "?wait=11", wantTimeout: 10 * time.Second, wantAwait: true},
		{name: "huge clamps to ten", query: "?wait=999999", wantTimeout: 10 * time.Second, wantAwait: true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{submitResult: sampleStoredMessage(), awaitResult: sampleStoredMessage()}

			recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages"+testCase.query, validSubmitBody)

			if recorder.Code != http.StatusAccepted {
				subTest.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if !testCase.wantAwait {
				if len(fake.awaitCalls) != 0 {
					subTest.Fatalf("Await calls = %d, want 0", len(fake.awaitCalls))
				}
				return
			}
			want := []awaitCall{{appID: "urls4irl", id: sampleMessageID, timeout: testCase.wantTimeout}}
			if !reflect.DeepEqual(fake.awaitCalls, want) {
				subTest.Fatalf("Await calls = %+v, want %+v", fake.awaitCalls, want)
			}
		})
	}
}

func TestSubmitWaitReturnsTheAwaitedView(testInstance *testing.T) {
	submitted := sampleStoredMessage()
	awaited := sampleStoredMessage()
	awaited.Deliveries = awaited.Deliveries[2:]
	fake := &fakeMessages{submitResult: submitted, awaitResult: awaited}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages?wait=2", validSubmitBody)

	if !strings.Contains(recorder.Body.String(), `"status":"delivered"`) || strings.Contains(recorder.Body.String(), `"a_hook"`) {
		testInstance.Fatalf("body = %s, want the awaited (delivered) view", recorder.Body.String())
	}
}

func TestSubmitWaitFailureStillAnswersWithTheCommittedMessage(testInstance *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	fake := &fakeMessages{submitResult: sampleStoredMessage(), awaitErr: message.ErrMessageNotFound}

	recorder := serveMessages(fake, logger, http.MethodPost, "/v1/messages?wait=2", validSubmitBody)

	assertResponse(testInstance, recorder, http.StatusAccepted, `{"message":`+sampleMessageJSON+`,"duplicate":false}`+"\n")
}

func TestSubmitRejectsBadWait(testInstance *testing.T) {
	for _, query := range []string{"?wait=abc", "?wait=-1", "?wait=1.5", "?wait=%201", "?wait=99999999999999999999", "?wait=0x5"} {
		testInstance.Run(query, func(subTest *testing.T) {
			fake := &fakeMessages{submitResult: sampleStoredMessage()}

			recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages"+query, validSubmitBody)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid wait"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called %d times for a rejected wait", fake.totalCalls())
			}
		})
	}
}

func TestSubmitBodySizeLimit(testInstance *testing.T) {
	padded := func(size int) string {
		return `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1","payload":{"k":"` +
			strings.Repeat("a", size) + `"}}`
	}

	testInstance.Run("100 KiB passes", func(subTest *testing.T) {
		fake := &fakeMessages{submitResult: sampleStoredMessage()}
		recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", padded(100<<10))
		if recorder.Code != http.StatusAccepted {
			subTest.Fatalf("status = %d, want 202", recorder.Code)
		}
		if len(fake.submitCalls) != 1 {
			subTest.Fatalf("Submit calls = %d, want 1", len(fake.submitCalls))
		}
	})
	testInstance.Run("over 256 KiB is 413", func(subTest *testing.T) {
		fake := &fakeMessages{submitResult: sampleStoredMessage()}
		recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", padded(maxMessageBodyBytes))
		assertResponse(subTest, recorder, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`+"\n")
		if fake.totalCalls() != 0 {
			subTest.Fatalf("service was called for an oversized body")
		}
	})
}

func TestSubmitRejectsBadBodies(testInstance *testing.T) {
	badBodies := []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{not json`},
		{name: "empty body", body: ``},
		{name: "trailing second value", body: validSubmitBody + ` {}`},
		{name: "trailing garbage", body: validSubmitBody + ` garbage`},
		{name: "unknown top-level field", body: `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"k","payload":{},"bogus":1}`},
		{name: "unknown field in to", body: `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"k","payload":{},"to":{"bogus":1}}`},
		{name: "unknown field in display", body: `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"k","payload":{},"display":{"bogus":1}}`},
		{name: "wrong type for app_id", body: `{"app_id":5,"channel":"alerts","idempotency_key":"k","payload":{}}`},
		{name: "wrong type for critical", body: `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"k","payload":{},"critical":"yes"}`},
		{name: "malformed deliver_at", body: `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"k","payload":{},"deliver_at":"soon"}`},
		{name: "truncated payload", body: `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"k","payload":{"a":`},
	}
	for _, badBody := range badBodies {
		testInstance.Run(badBody.name, func(subTest *testing.T) {
			fake := &fakeMessages{submitResult: sampleStoredMessage()}

			recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", badBody.body)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called %d times for a rejected body", fake.totalCalls())
			}
		})
	}
}

func TestSubmitAcceptsUnknownKeysInsideThePayload(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage()}
	body := `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"k","payload":{"anything":{"goes":true},"app_id":"decoy"}}`

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages", body)

	if recorder.Code != http.StatusAccepted {
		testInstance.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := string(fake.submitCalls[0].Payload); got != `{"anything":{"goes":true},"app_id":"decoy"}` {
		testInstance.Fatalf("payload = %s, want it passed through untouched", got)
	}
	if fake.submitCalls[0].AppID != "urls4irl" {
		testInstance.Fatalf("app_id = %q: a payload key must never override the envelope app_id", fake.submitCalls[0].AppID)
	}
}

func TestGetMessageReturnsTheExactView(testInstance *testing.T) {
	fake := &fakeMessages{getResult: sampleStoredMessage()}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/messages/"+sampleMessageID+"?app_id=urls4irl", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"message":`+sampleMessageJSON+`}`+"\n")
	want := []appAndID{{appID: "urls4irl", id: sampleMessageID}}
	if !reflect.DeepEqual(fake.getCalls, want) {
		testInstance.Fatalf("Get calls = %+v, want %+v", fake.getCalls, want)
	}
}

func TestGetMessageRequiresAppID(testInstance *testing.T) {
	for _, query := range []string{"", "?app_id=", "?other=1"} {
		testInstance.Run("query "+query, func(subTest *testing.T) {
			fake := &fakeMessages{getResult: sampleStoredMessage()}

			recorder := serveMessages(fake, nil, http.MethodGet, "/v1/messages/"+sampleMessageID+query, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"app_id is required"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called for a request with no app_id")
			}
		})
	}
}

func TestGetMessageByKeyReturnsTheExactView(testInstance *testing.T) {
	fake := &fakeMessages{getByKeyResult: sampleStoredMessage()}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/messages?app_id=urls4irl&idempotency_key=key-1", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"message":`+sampleMessageJSON+`}`+"\n")
	want := []appAndID{{appID: "urls4irl", id: "key-1"}}
	if !reflect.DeepEqual(fake.getByKeyCalls, want) {
		testInstance.Fatalf("GetByKey calls = %+v, want %+v", fake.getByKeyCalls, want)
	}
}

func TestGetMessageByKeyRequiresBothParams(testInstance *testing.T) {
	for _, query := range []string{"", "?app_id=urls4irl", "?idempotency_key=key-1", "?app_id=&idempotency_key=key-1", "?app_id=urls4irl&idempotency_key="} {
		testInstance.Run("query "+query, func(subTest *testing.T) {
			fake := &fakeMessages{getByKeyResult: sampleStoredMessage()}

			recorder := serveMessages(fake, nil, http.MethodGet, "/v1/messages"+query, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"app_id and idempotency_key are required"}`+"\n")
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called for an incomplete lookup")
			}
		})
	}
}

func TestListDeliveriesReturnsTheExactBody(testInstance *testing.T) {
	admin := make([]message.AdminDelivery, 0, 3)
	for _, delivery := range sampleStoredMessage().Deliveries {
		delivery.MessageID = sampleMessageID
		delivery.DestinationType = "webhook"
		delivery.CreatedAt = adminTimestamp.Add(-time.Hour)
		delivery.UpdatedAt = adminTimestamp
		admin = append(admin, message.AdminDelivery{Delivery: delivery, AppID: "urls4irl", Channel: "alerts"})
	}
	fake := &fakeMessages{listDeliveriesResult: admin}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/deliveries?state=dead&limit=3", "")

	const adminSuffix = `,"message_id":"` + sampleMessageID + `","app_id":"urls4irl","channel":"alerts",` +
		`"destination_type":"webhook","created_at":"2026-10-16T11:00:00Z","updated_at":"` + adminTimestampJSON + `"}`
	assertResponse(testInstance, recorder, http.StatusOK,
		`{"deliveries":[`+withAdminFields(pendingDeliveryJSON, adminSuffix)+`,`+
			withAdminFields(deadDeliveryJSON, adminSuffix)+`,`+withAdminFields(deliveredDeliveryJSON, adminSuffix)+`]}`+"\n")
	want := []listDeliveriesCall{{state: "dead", limit: 3}}
	if !reflect.DeepEqual(fake.listDeliveriesCalls, want) {
		testInstance.Fatalf("ListDeliveries calls = %+v, want %+v", fake.listDeliveriesCalls, want)
	}
}

// withAdminFields appends the admin-only JSON fields (suffix starts with a comma
// and ends with the closing brace) to an app-facing delivery JSON object.
func withAdminFields(deliveryJSON string, suffix string) string {
	return strings.TrimSuffix(deliveryJSON, "}") + suffix
}

func TestListDeliveriesDefaultsAndLimitParsing(testInstance *testing.T) {
	testCases := []struct {
		name  string
		query string
		want  listDeliveriesCall
	}{
		{name: "no params", query: "", want: listDeliveriesCall{state: "dead", limit: 50}},
		{name: "empty params", query: "?state=&limit=", want: listDeliveriesCall{state: "dead", limit: 50}},
		{name: "explicit state", query: "?state=pending", want: listDeliveriesCall{state: "pending", limit: 50}},
		{name: "explicit limit", query: "?limit=200", want: listDeliveriesCall{state: "dead", limit: 200}},
		{name: "non-integer limit goes out of range", query: "?limit=abc", want: listDeliveriesCall{state: "dead", limit: -1}},
		{name: "fractional limit goes out of range", query: "?limit=1.5", want: listDeliveriesCall{state: "dead", limit: -1}},
		{name: "overflowing limit goes out of range", query: "?limit=99999999999999999999", want: listDeliveriesCall{state: "dead", limit: -1}},
		{name: "zero is passed through", query: "?limit=0", want: listDeliveriesCall{state: "dead", limit: 0}},
		{name: "over the cap is passed through", query: "?limit=201", want: listDeliveriesCall{state: "dead", limit: 201}},
		{name: "unknown state is passed through", query: "?state=bogus", want: listDeliveriesCall{state: "bogus", limit: 50}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{}

			recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/deliveries"+testCase.query, "")

			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d, want 200 from the fake", recorder.Code)
			}
			if !reflect.DeepEqual(fake.listDeliveriesCalls, []listDeliveriesCall{testCase.want}) {
				subTest.Fatalf("ListDeliveries calls = %+v, want %+v", fake.listDeliveriesCalls, testCase.want)
			}
		})
	}
}

func TestListDeliveriesRealServiceRejectsBadStateAndLimit(testInstance *testing.T) {
	service := message.NewService(message.ServiceConfig{Store: &listStore{}, Resolver: unusedResolver{}})
	testCases := []struct {
		query    string
		wantBody string
	}{
		{query: "?limit=abc", wantBody: `{"error":"invalid limit"}`},
		{query: "?limit=0", wantBody: `{"error":"invalid limit"}`},
		{query: "?limit=201", wantBody: `{"error":"invalid limit"}`},
		{query: "?limit=-5", wantBody: `{"error":"invalid limit"}`},
		{query: "?state=bogus", wantBody: `{"error":"invalid state"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.query, func(subTest *testing.T) {
			recorder := serveMessages(service, nil, http.MethodGet, "/v1/admin/deliveries"+testCase.query, "")

			assertResponse(subTest, recorder, http.StatusBadRequest, testCase.wantBody+"\n")
		})
	}
}

// unusedResolver satisfies message.Resolver for tests that never submit.
type unusedResolver struct{}

func (unusedResolver) Resolve(context.Context, string, string, string) ([]registry.ResolvedDestination, error) {
	panic("resolver reached by a test that never submits")
}

// listStore panics if the service ever lets a bad list request reach the store;
// the embedded nil Store provides the methods the test never calls.
type listStore struct{ message.Store }

func (*listStore) ListDeliveries(context.Context, string, int) ([]message.AdminDelivery, error) {
	panic("store reached with an invalid list request")
}

func TestListAttemptsReturnsTheExactBody(testInstance *testing.T) {
	fake := &fakeMessages{listAttemptsResult: []message.Attempt{
		{
			ID: 1, AttemptNumber: 1,
			AttemptRecord: message.AttemptRecord{
				Outcome: message.OutcomeRetryable, StatusCode: intPointer(503), LatencyMS: 120,
				ResponseSnippet: stringPointer("unavailable"), StartedAt: adminTimestamp,
			},
		},
		{
			ID: 2, AttemptNumber: 2,
			AttemptRecord: message.AttemptRecord{
				Outcome: message.OutcomeBlocked, LatencyMS: 0, Error: stringPointer("blocked address"),
				StartedAt: adminTimestamp.Add(time.Minute),
			},
		},
	}}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/admin/deliveries/42/attempts", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"attempts":[`+
			`{"attempt_number":1,"outcome":"retryable","status_code":503,"latency_ms":120,"error":null,`+
			`"response_snippet":"unavailable","started_at":"2026-10-16T12:00:00Z"},`+
			`{"attempt_number":2,"outcome":"blocked","status_code":null,"latency_ms":0,"error":"blocked address",`+
			`"response_snippet":null,"started_at":"2026-10-16T12:01:00Z"}]}`+"\n")
	if !reflect.DeepEqual(fake.listAttemptsCalls, []int64{42}) {
		testInstance.Fatalf("ListAttempts calls = %v, want [42]", fake.listAttemptsCalls)
	}
}

func TestReplayReturnsTheExactBody(testInstance *testing.T) {
	fake := &fakeMessages{replayResult: message.AdminDelivery{
		Delivery: message.Delivery{
			ID: 12, MessageID: sampleMessageID, DestinationName: "b_hook", DestinationType: "webhook",
			State: message.StatePending, AttemptCount: 2, LastStatusCode: intPointer(400),
			NextAttemptAt: adminTimestamp.Add(30 * time.Second),
			CreatedAt:     adminTimestamp.Add(-time.Hour), UpdatedAt: adminTimestamp,
		},
		AppID: "urls4irl", Channel: "alerts",
	}}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/admin/deliveries/12/replay", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"delivery":{"id":12,"destination":"b_hook","state":"pending","dead_reason":null,"attempt_count":2,`+
			`"last_status_code":400,"next_attempt_at":"2026-10-16T12:00:30Z","delivered_at":null,`+
			`"message_id":"`+sampleMessageID+`","app_id":"urls4irl","channel":"alerts","destination_type":"webhook",`+
			`"created_at":"2026-10-16T11:00:00Z","updated_at":"`+adminTimestampJSON+`"}}`+"\n")
	if !reflect.DeepEqual(fake.replayCalls, []int64{12}) {
		testInstance.Fatalf("Replay calls = %v, want [12]", fake.replayCalls)
	}
}

func TestListsAreNeverNull(testInstance *testing.T) {
	fake := &fakeMessages{}

	assertResponse(testInstance, serveMessages(fake, nil, http.MethodGet, "/v1/admin/deliveries", ""),
		http.StatusOK, `{"deliveries":[]}`+"\n")
	assertResponse(testInstance, serveMessages(fake, nil, http.MethodGet, "/v1/admin/deliveries/1/attempts", ""),
		http.StatusOK, `{"attempts":[]}`+"\n")

	empty := sampleStoredMessage()
	empty.Deliveries = nil
	fake.getResult = empty
	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/messages/"+sampleMessageID+"?app_id=urls4irl", "")
	if !strings.Contains(recorder.Body.String(), `"deliveries":[]`) {
		testInstance.Fatalf("body = %s, want an empty (not null) deliveries array", recorder.Body.String())
	}
}

func TestMalformedDeliveryIDsAreRejectedBeforeTheService(testInstance *testing.T) {
	badIDs := []string{"abc", "0", "-1", "+1", "01", "1.5", "1e3", "99999999999999999999", "%20"}
	routes := []struct {
		name   string
		method string
		suffix string
	}{
		{name: "attempts", method: http.MethodGet, suffix: "/attempts"},
		{name: "replay", method: http.MethodPost, suffix: "/replay"},
	}
	for _, route := range routes {
		for _, badID := range badIDs {
			testInstance.Run(route.name+"/"+badID, func(subTest *testing.T) {
				fake := &fakeMessages{}

				recorder := serveMessages(fake, nil, route.method, "/v1/admin/deliveries/"+badID+route.suffix, "")

				assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid delivery id"}`+"\n")
				if fake.totalCalls() != 0 {
					subTest.Fatalf("service was called for a malformed delivery id")
				}
			})
		}
	}
}

// TestWriteMessageErrorBranches covers every mapping of the error chokepoint,
// through each route that can produce it.
func TestWriteMessageErrorBranches(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "validation", err: &message.ValidationError{Message: "invalid channel"}, wantStatus: 400, wantBody: `{"error":"invalid channel"}`},
		{name: "wrapped validation", err: fmt.Errorf("ctx: %w", &message.ValidationError{Message: "invalid app_id"}), wantStatus: 400, wantBody: `{"error":"invalid app_id"}`},
		{name: "unknown destination", err: registry.ErrUnknownDestination, wantStatus: 400, wantBody: `{"error":"unknown destination"}`},
		{name: "destination disabled", err: registry.ErrDestinationDisabled, wantStatus: 409, wantBody: `{"error":"destination disabled"}`},
		{name: "no destination matched", err: message.ErrNoDestinationMatched, wantStatus: 422, wantBody: `{"error":"no destination matched"}`},
		{name: "unsupported type", err: message.ErrUnsupportedDestinationType, wantStatus: 422, wantBody: `{"error":"destination type not yet supported"}`},
		{name: "no publisher", err: registry.ErrNoPublisher, wantStatus: 422, wantBody: `{"error":"app has no ntfy publisher"}`},
		{name: "wrapped no publisher", err: fmt.Errorf("submit: %w", registry.ErrNoPublisher), wantStatus: 422, wantBody: `{"error":"app has no ntfy publisher"}`},
		{name: "message not found", err: message.ErrMessageNotFound, wantStatus: 404, wantBody: `{"error":"message not found"}`},
		{name: "delivery not found", err: message.ErrDeliveryNotFound, wantStatus: 404, wantBody: `{"error":"delivery not found"}`},
		{name: "delivery not dead", err: message.ErrDeliveryNotDead, wantStatus: 409, wantBody: `{"error":"delivery not dead"}`},
		{name: "wrapped sentinel", err: fmt.Errorf("replay: %w", message.ErrDeliveryNotDead), wantStatus: 409, wantBody: `{"error":"delivery not dead"}`},
		{name: "anything else", err: errors.New("boom"), wantStatus: 500, wantBody: `{"error":"internal error"}`},
	}
	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "submit", method: http.MethodPost, path: "/v1/messages", body: validSubmitBody},
		{name: "get", method: http.MethodGet, path: "/v1/messages/" + sampleMessageID + "?app_id=urls4irl"},
		{name: "get by key", method: http.MethodGet, path: "/v1/messages?app_id=urls4irl&idempotency_key=k"},
		{name: "list deliveries", method: http.MethodGet, path: "/v1/admin/deliveries"},
		{name: "attempts", method: http.MethodGet, path: "/v1/admin/deliveries/1/attempts"},
		{name: "replay", method: http.MethodPost, path: "/v1/admin/deliveries/1/replay"},
	}
	for _, testCase := range testCases {
		for _, route := range routes {
			testInstance.Run(testCase.name+"/"+route.name, func(subTest *testing.T) {
				recorder := serveMessages(failingMessages(testCase.err), nil, route.method, route.path, route.body)

				assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
			})
		}
	}
}

func TestInternalErrorsAreLoggedButNeverReturned(testInstance *testing.T) {
	const secretErrText = "insert message: connection refused to 10.0.0.5:5432 with credential tk_secret_xyz (SQLSTATE 08006)"
	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "submit", method: http.MethodPost, path: "/v1/messages", body: validSubmitBody},
		{name: "get", method: http.MethodGet, path: "/v1/messages/" + sampleMessageID + "?app_id=urls4irl"},
		{name: "get by key", method: http.MethodGet, path: "/v1/messages?app_id=urls4irl&idempotency_key=k"},
		{name: "list deliveries", method: http.MethodGet, path: "/v1/admin/deliveries"},
		{name: "attempts", method: http.MethodGet, path: "/v1/admin/deliveries/1/attempts"},
		{name: "replay", method: http.MethodPost, path: "/v1/admin/deliveries/1/replay"},
	}
	for _, route := range routes {
		testInstance.Run(route.name, func(subTest *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

			recorder := serveMessages(failingMessages(errors.New(secretErrText)), logger, route.method, route.path, route.body)

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

// TestContextErrorsAreNotLoggedAtErrorLevel pins that a client abort or request
// timeout answers the generic 500 without writing to the error log, so a client
// that disconnects at will cannot flood it.
func TestContextErrorsAreNotLoggedAtErrorLevel(testInstance *testing.T) {
	for _, contextErr := range []error{context.Canceled, fmt.Errorf("wait: %w", context.DeadlineExceeded)} {
		var logBuffer bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelWarn}))

		recorder := serveMessages(failingMessages(contextErr), logger, http.MethodGet, "/v1/admin/deliveries", "")

		assertResponse(testInstance, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
		if logBuffer.Len() != 0 {
			testInstance.Fatalf("%v logged at warn or above: %s", contextErr, logBuffer.String())
		}
	}
}

// deadlineRecorder records the read deadlines a handler sets through
// http.ResponseController.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (recorder *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	recorder.deadlines = append(recorder.deadlines, deadline)
	return nil
}

// TestBodyReadDeadlineIsSetThenClearedForEveryDecodeJSONCaller proves both the
// message and the admin handlers bound the body read, and that the deadline is
// cleared again so it cannot cut short the work after the decode (a ?wait).
func TestBodyReadDeadlineIsSetThenClearedForEveryDecodeJSONCaller(testInstance *testing.T) {
	testCases := []struct {
		name   string
		server *Server
		path   string
		body   string
	}{
		{
			name: "submit", server: NewServer(ServerConfig{Messages: &fakeMessages{submitResult: sampleStoredMessage()}}),
			path: "/v1/messages", body: validSubmitBody,
		},
		{
			name: "admin", server: NewServer(ServerConfig{Registry: &fakeRegistry{}}),
			path: "/v1/admin/routes", body: validCreateRouteBody,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			request := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(testCase.body))
			started := time.Now()

			testCase.server.Handler().ServeHTTP(recorder, request)

			if len(recorder.deadlines) != 2 {
				subTest.Fatalf("deadlines set = %v, want a deadline then a clear", recorder.deadlines)
			}
			if recorder.deadlines[0].Before(started.Add(bodyReadTimeout)) ||
				recorder.deadlines[0].After(time.Now().Add(bodyReadTimeout)) {
				subTest.Fatalf("first deadline = %v, want about %v from now", recorder.deadlines[0], bodyReadTimeout)
			}
			if !recorder.deadlines[1].IsZero() {
				subTest.Fatalf("second deadline = %v, want the zero time (cleared)", recorder.deadlines[1])
			}
		})
	}
}

func TestMessageLogsNeverContainRequestPayload(testInstance *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	body := `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1","payload":{"card":"4111-SECRET-PAYLOAD"}}`

	serveMessages(failingMessages(errors.New("boom")), logger, http.MethodPost, "/v1/messages", body)
	serveMessages(&fakeMessages{}, logger, http.MethodPost, "/v1/messages", `{"payload":{"card":"4111-SECRET-PAYLOAD"},"bogus":1}`)

	if strings.Contains(logBuffer.String(), "4111-SECRET-PAYLOAD") {
		testInstance.Fatalf("log output leaked a request payload: %s", logBuffer.String())
	}
}

func TestNilMessagesFailsClosed(testInstance *testing.T) {
	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "submit", method: http.MethodPost, path: "/v1/messages", body: validSubmitBody},
		{name: "get", method: http.MethodGet, path: "/v1/messages/" + sampleMessageID + "?app_id=urls4irl"},
		{name: "get by key", method: http.MethodGet, path: "/v1/messages?app_id=urls4irl&idempotency_key=k"},
		{name: "list deliveries", method: http.MethodGet, path: "/v1/admin/deliveries"},
		{name: "attempts", method: http.MethodGet, path: "/v1/admin/deliveries/1/attempts"},
		{name: "replay", method: http.MethodPost, path: "/v1/admin/deliveries/1/replay"},
	}
	for _, route := range routes {
		testInstance.Run(route.name, func(subTest *testing.T) {
			recorder := serveMessages(nil, nil, route.method, route.path, route.body)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"messages unavailable"}`+"\n")
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestMessageResponsesAreNotCacheable(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage(), getResult: sampleStoredMessage()}
	for _, request := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/messages", validSubmitBody},
		{http.MethodGet, "/v1/messages/" + sampleMessageID + "?app_id=urls4irl", ""},
		{http.MethodGet, "/v1/admin/deliveries", ""},
		{http.MethodPost, "/v1/admin/deliveries/1/replay", ""},
		{http.MethodGet, "/v1/messages?app_id=urls4irl", ""},
	} {
		recorder := serveMessages(fake, nil, request.method, request.path, request.body)
		if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
			testInstance.Fatalf("%s %s headers = %v, want no-store and nosniff", request.method, request.path, recorder.Header())
		}
	}
}

func TestMessageViewsNeverEchoPayloadOrDisplay(testInstance *testing.T) {
	fake := &fakeMessages{submitResult: sampleStoredMessage(), getResult: sampleStoredMessage()}

	for _, recorder := range []*httptest.ResponseRecorder{
		serveMessages(fake, nil, http.MethodPost, "/v1/messages", validSubmitBody),
		serveMessages(fake, nil, http.MethodGet, "/v1/messages/"+sampleMessageID+"?app_id=urls4irl", ""),
	} {
		if strings.Contains(recorder.Body.String(), "never echoed") {
			testInstance.Fatalf("body echoed the payload or display: %s", recorder.Body.String())
		}
	}
}

func TestMessageViewTimesAreUTC(testInstance *testing.T) {
	zone := time.FixedZone("CEST", 2*60*60)
	stored := sampleStoredMessage()
	stored.CreatedAt = adminTimestamp.In(zone)
	stored.Deliveries[0].NextAttemptAt = adminTimestamp.Add(time.Minute).In(zone)
	stored.Deliveries[2].DeliveredAt = timePointer(adminTimestamp.In(zone))
	fake := &fakeMessages{getResult: stored}

	recorder := serveMessages(fake, nil, http.MethodGet, "/v1/messages/"+sampleMessageID+"?app_id=urls4irl", "")

	assertResponse(testInstance, recorder, http.StatusOK, `{"message":`+sampleMessageJSON+`}`+"\n")
}

func TestMessageRoutesRejectUnsupportedRequests(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "PUT messages", method: http.MethodPut, path: "/v1/messages", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE message", method: http.MethodDelete, path: "/v1/messages/" + sampleMessageID, wantStatus: http.StatusMethodNotAllowed},
		{name: "POST message by id", method: http.MethodPost, path: "/v1/messages/" + sampleMessageID, wantStatus: http.StatusMethodNotAllowed},
		{name: "POST deliveries list", method: http.MethodPost, path: "/v1/admin/deliveries", wantStatus: http.StatusMethodNotAllowed},
		{name: "GET replay", method: http.MethodGet, path: "/v1/admin/deliveries/1/replay", wantStatus: http.StatusMethodNotAllowed},
		{name: "POST attempts", method: http.MethodPost, path: "/v1/admin/deliveries/1/attempts", wantStatus: http.StatusMethodNotAllowed},
		{name: "DELETE delivery", method: http.MethodDelete, path: "/v1/admin/deliveries/1", wantStatus: http.StatusNotFound},
		{name: "deliveries outside admin", method: http.MethodGet, path: "/v1/deliveries", wantStatus: http.StatusNotFound},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{}

			recorder := serveMessages(fake, nil, testCase.method, testCase.path, "")

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			if fake.totalCalls() != 0 {
				subTest.Fatalf("service was called for an unsupported request")
			}
		})
	}
}

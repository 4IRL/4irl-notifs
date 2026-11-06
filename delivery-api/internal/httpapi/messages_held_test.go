package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

func TestSubmitSetsHoldFromTheRouteNeverTheBody(testInstance *testing.T) {
	testCases := []struct {
		name     string
		path     string
		wantHold bool
	}{
		{name: "app route holds", path: "/v1/messages", wantHold: true},
		{name: "operator route never holds", path: "/v1/admin/messages", wantHold: false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeMessages{submitResult: sampleStoredMessage()}

			recorder := serveMessages(fake, nil, http.MethodPost, testCase.path, validSubmitBody)

			if recorder.Code != http.StatusAccepted || len(fake.submitCalls) != 1 {
				subTest.Fatalf("status %d with %d Submit calls, want 202 and 1", recorder.Code, len(fake.submitCalls))
			}
			if got := fake.submitCalls[0].Hold; got != testCase.wantHold {
				subTest.Fatalf("Envelope.Hold = %v, want %v", got, testCase.wantHold)
			}
		})
	}
}

func TestSubmitRejectsAHoldFieldInTheBodyOnBothRoutes(testInstance *testing.T) {
	for _, field := range []string{`"hold":true`, `"held":true`} {
		for _, path := range []string{"/v1/messages", "/v1/admin/messages"} {
			fake := &fakeMessages{submitResult: sampleStoredMessage()}
			body := strings.Replace(validSubmitBody, "{", "{"+field+",", 1)

			recorder := serveMessages(fake, nil, http.MethodPost, path, body)

			assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
			if len(fake.submitCalls) != 0 {
				testInstance.Fatalf("%s %s: Submit calls = %d, want 0", path, field, len(fake.submitCalls))
			}
		}
	}
}

func TestGetRendersHeldCancelledAndReleaseFailedStatuses(testInstance *testing.T) {
	testCases := []struct {
		name       string
		mutate     func(*message.StoredMessage)
		wantStatus string
	}{
		{name: "held", mutate: func(stored *message.StoredMessage) { stored.HeldAt = timePointer(adminTimestamp) }, wantStatus: "unrouted"},
		{
			name: "cancelled held",
			mutate: func(stored *message.StoredMessage) {
				stored.CancelledAt = timePointer(adminTimestamp)
				stored.HeldCancelledAt = timePointer(adminTimestamp)
			},
			wantStatus: "cancelled",
		},
		{
			name:       "release failed",
			mutate:     func(stored *message.StoredMessage) { stored.ReleaseFailedAt = timePointer(adminTimestamp) },
			wantStatus: "release_failed",
		},
	}
	routes := []string{
		"/v1/messages/" + sampleMessageID + "?app_id=urls4irl",
		"/v1/messages?app_id=urls4irl&idempotency_key=key-1",
	}
	for _, testCase := range testCases {
		for _, route := range routes {
			testInstance.Run(testCase.name+" "+route, func(subTest *testing.T) {
				stored := message.StoredMessage{
					ID:       sampleMessageID,
					Envelope: message.Envelope{AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1"},
				}
				stored.CreatedAt = adminTimestamp
				testCase.mutate(&stored)
				fake := &fakeMessages{getResult: stored, getByKeyResult: stored}

				recorder := serveMessages(fake, nil, http.MethodGet, route, "")

				if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"`+testCase.wantStatus+`"`) {
					subTest.Fatalf("status %d body %s, want 200 with status %q", recorder.Code, recorder.Body.String(), testCase.wantStatus)
				}
			})
		}
	}
}

func TestSubmitHeldReturnsUnroutedAndSkipsAwait(testInstance *testing.T) {
	heldMessage := message.StoredMessage{
		ID:       sampleMessageID,
		Envelope: message.Envelope{AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1"},
		HeldAt:   timePointer(adminTimestamp),
	}
	heldMessage.CreatedAt = adminTimestamp
	fake := &fakeMessages{submitResult: heldMessage, submitHeld: true}

	recorder := serveMessages(fake, nil, http.MethodPost, "/v1/messages?wait=5", validSubmitBody)

	wantBody := `{"message":{"message_id":"` + sampleMessageID + `","app_id":"urls4irl","channel":"alerts",` +
		`"idempotency_key":"key-1","critical":false,"status":"unrouted","deliver_at":null,` +
		`"created_at":"` + adminTimestampJSON + `","cancelled_at":null,"deliveries":[]},` +
		`"duplicate":false,"replaced":false}` + "\n"
	assertResponse(testInstance, recorder, http.StatusAccepted, wantBody)
	if len(fake.awaitCalls) != 0 {
		testInstance.Fatalf("Await calls = %d, want 0 for a held message", len(fake.awaitCalls))
	}
}

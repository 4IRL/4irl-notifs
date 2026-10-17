//go:build integration

package integration

// End-to-end test of the /v1/messages and /v1/admin/deliveries API against the
// live compose stack: the real built binary, its in-process worker and its
// SSRF-guarded production transport (no test knob can reach it). The
// destination's URL points at a loopback port, which the guard must refuse, so
// the whole submit -> queue -> worker -> sink -> transport -> dead-letter ->
// replay path runs without any external receiver.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	messagesPath = "/v1/messages"
	// blockedWebhookURL is a loopback address: the production transport refuses to dial it.
	blockedWebhookURL = "https://127.0.0.1:9/hook"
	messageAppID      = "itest"
	// submitWaitSeconds is the server's maximum ?wait, so a loaded host cannot make the first attempt
	// outlast the wait and fail the dead-letter assertion.
	submitWaitSeconds   = 10
	replayPollTimeout   = 15 * time.Second
	replayPollInterval  = 250 * time.Millisecond
	deadStateName       = "dead"
	blockedAddressState = "blocked_address"
	blockedOutcome      = "blocked"
)

// cleanupKeyPattern guards the SQL cleanup below against interpolating anything
// but the test's own generated idempotency key.
var cleanupKeyPattern = regexp.MustCompile(`^it-[0-9a-f]{16}$`)

// deliveryJSON, messageJSON and the response wrappers mirror the API's JSON.
type deliveryJSON struct {
	ID           int64   `json:"id"`
	Destination  string  `json:"destination"`
	State        string  `json:"state"`
	DeadReason   *string `json:"dead_reason"`
	AttemptCount int     `json:"attempt_count"`
}

type messageJSON struct {
	MessageID  string         `json:"message_id"`
	AppID      string         `json:"app_id"`
	Status     string         `json:"status"`
	Deliveries []deliveryJSON `json:"deliveries"`
}

type submitResponse struct {
	Message   messageJSON `json:"message"`
	Duplicate bool        `json:"duplicate"`
}

type messageResponse struct {
	Message messageJSON `json:"message"`
}

type deliveriesResponse struct {
	Deliveries []deliveryJSON `json:"deliveries"`
}

type deliveryResponse struct {
	Delivery deliveryJSON `json:"delivery"`
}

type attemptJSON struct {
	AttemptNumber   int     `json:"attempt_number"`
	Outcome         string  `json:"outcome"`
	StatusCode      *int    `json:"status_code"`
	ResponseSnippet *string `json:"response_snippet"`
}

type attemptsResponse struct {
	Attempts []attemptJSON `json:"attempts"`
}

// apiRequest sends one request to an absolute API path (query string included)
// with an optional JSON body and returns the status code and raw body.
func apiRequest(testInstance *testing.T, method, path string, body any) (int, []byte) {
	testInstance.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			testInstance.Fatalf("encoding %s %s body: %v", method, path, marshalErr)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, requestErr := http.NewRequestWithContext(context.Background(), method, deliveryURL()+path, requestBody)
	if requestErr != nil {
		testInstance.Fatalf("building %s %s request: %v", method, path, requestErr)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	// The wait=5 submit holds the connection for up to five seconds, so the shared admin
	// client (5 s) is too tight; give it explicit headroom.
	client := &http.Client{Timeout: httpRequestTimeout + submitWaitSeconds*time.Second}
	response, doErr := client.Do(request)
	if doErr != nil {
		testInstance.Fatalf("%s %s: %v", method, path, doErr)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			testInstance.Errorf("closing %s %s body: %v", method, path, closeErr)
		}
	}()
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		testInstance.Fatalf("reading %s %s body: %v", method, path, readErr)
	}
	return response.StatusCode, responseBody
}

func decodeBody(testInstance *testing.T, label string, body []byte, target any) {
	testInstance.Helper()
	if decodeErr := json.Unmarshal(body, target); decodeErr != nil {
		testInstance.Fatalf("%s: decoding %q: %v", label, body, decodeErr)
	}
}

// createDestination creates a destination through the admin API and registers
// its cleanup.
func createDestination(testInstance *testing.T, name string, input map[string]any) {
	testInstance.Helper()
	input["name"] = name
	status, body := adminRequest(testInstance, http.MethodPost, "/destinations", input)
	if status == http.StatusCreated {
		cleanupDestination(testInstance, name)
	}
	requireStatus(testInstance, "POST destination "+name, status, http.StatusCreated, body)
}

// createRoute creates an app "*" route for channel to destinationName and
// registers its cleanup.
func createRoute(testInstance *testing.T, channel, destinationName string) {
	testInstance.Helper()
	status, body := adminRequest(testInstance, http.MethodPost, "/routes", map[string]any{
		"app_pattern":     "*",
		"channel_pattern": channel,
		"destinations":    []string{destinationName},
	})
	requireStatus(testInstance, "POST route "+channel, status, http.StatusCreated, body)
	var decoded routeEnvelope
	decodeBody(testInstance, "created route", body, &decoded)
	cleanupRoute(testInstance, decoded.Route.ID)
}

// registerMessageCleanup deletes the message the test submits under
// idempotencyKey (its deliveries and attempts cascade). Keyed by the
// test-generated key rather than the response's id, so it still cleans up when
// the submit response is lost after the server committed. It is registered
// before the destination and route cleanups, so (LIFO) it runs after them.
func registerMessageCleanup(testInstance *testing.T, idempotencyKey string) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		if !cleanupKeyPattern.MatchString(idempotencyKey) {
			testInstance.Errorf("cleanup: refusing to delete messages for malformed key %q", idempotencyKey)
			return
		}
		psql(testInstance, fmt.Sprintf(
			"delete from messages where app_id = '%s' and idempotency_key = '%s'", messageAppID, idempotencyKey))
	})
}

func submitBody(channel, idempotencyKey string) map[string]any {
	return map[string]any{
		"app_id":          messageAppID,
		"channel":         channel,
		"idempotency_key": idempotencyKey,
		"payload":         map[string]any{"n": 1},
	}
}

func getMessage(testInstance *testing.T, messageID string) messageJSON {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("%s/%s?app_id=%s", messagesPath, messageID, messageAppID), nil)
	requireStatus(testInstance, "GET message", status, http.StatusOK, body)
	var decoded messageResponse
	decodeBody(testInstance, "GET message", body, &decoded)
	return decoded.Message
}

func TestMessagesBlockedAddressReplayAndRejections(testInstance *testing.T) {
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	channel := "it_" + suffix
	idempotencyKey := "it-" + suffix
	destinationName := "itest_" + suffix
	registerMessageCleanup(testInstance, idempotencyKey)
	createDestination(testInstance, destinationName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + suffix + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:signingSecretLength],
		},
	})
	createRoute(testInstance, channel, destinationName)

	// Submit and wait for the first attempt: the production transport refuses the
	// loopback address, so the delivery dead-letters as blocked_address.
	status, body := apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s?wait=%d", messagesPath, submitWaitSeconds), submitBody(channel, idempotencyKey))
	requireStatus(testInstance, "POST message", status, http.StatusAccepted, body)
	var submitted submitResponse
	decodeBody(testInstance, "POST message", body, &submitted)
	messageID := submitted.Message.MessageID
	if submitted.Duplicate {
		testInstance.Fatal("first submit reported duplicate: true")
	}
	if len(submitted.Message.Deliveries) != 1 {
		testInstance.Fatalf("deliveries = %d, want 1 (body: %s)", len(submitted.Message.Deliveries), body)
	}
	delivery := submitted.Message.Deliveries[0]
	if delivery.State != deadStateName || delivery.DeadReason == nil || *delivery.DeadReason != blockedAddressState ||
		delivery.AttemptCount != 1 || delivery.Destination != destinationName {
		testInstance.Fatalf("delivery after wait = %+v, want dead/%s after 1 attempt to %s",
			delivery, blockedAddressState, destinationName)
	}
	if submitted.Message.Status != deadStateName {
		testInstance.Fatalf("message status = %q, want %q", submitted.Message.Status, deadStateName)
	}

	// The same key is a duplicate of the same message.
	status, body = apiRequest(testInstance, http.MethodPost, messagesPath, submitBody(channel, idempotencyKey))
	requireStatus(testInstance, "POST duplicate message", status, http.StatusAccepted, body)
	var duplicate submitResponse
	decodeBody(testInstance, "POST duplicate message", body, &duplicate)
	if !duplicate.Duplicate || duplicate.Message.MessageID != messageID {
		testInstance.Fatalf("re-submit = (duplicate %v, id %q), want (true, %q)",
			duplicate.Duplicate, duplicate.Message.MessageID, messageID)
	}

	// Lookups: by idempotency key, and by id (which requires the calling app_id).
	status, body = apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("%s?app_id=%s&idempotency_key=%s", messagesPath, messageAppID, idempotencyKey), nil)
	requireStatus(testInstance, "GET message by key", status, http.StatusOK, body)
	var byKey messageResponse
	decodeBody(testInstance, "GET message by key", body, &byKey)
	if byKey.Message.MessageID != messageID {
		testInstance.Fatalf("lookup by key id = %q, want %q", byKey.Message.MessageID, messageID)
	}
	status, body = apiRequest(testInstance, http.MethodGet, fmt.Sprintf("%s/%s", messagesPath, messageID), nil)
	requireStatus(testInstance, "GET message without app_id", status, http.StatusBadRequest, body)
	status, body = apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("%s/%s?app_id=someotherapp", messagesPath, messageID), nil)
	requireStatus(testInstance, "GET message as another app", status, http.StatusNotFound, body)

	// The operator dead-letter list includes the delivery.
	status, body = apiRequest(testInstance, http.MethodGet, "/v1/admin/deliveries?state=dead&limit=200", nil)
	requireStatus(testInstance, "GET dead deliveries", status, http.StatusOK, body)
	var dead deliveriesResponse
	decodeBody(testInstance, "GET dead deliveries", body, &dead)
	if !containsDelivery(dead.Deliveries, delivery.ID) {
		testInstance.Fatalf("dead-letter list does not include delivery %d (body: %s)", delivery.ID, body)
	}

	// The delivery log shows a blocked attempt and stores no response snippet.
	attemptsPath := fmt.Sprintf("/v1/admin/deliveries/%d/attempts", delivery.ID)
	status, body = apiRequest(testInstance, http.MethodGet, attemptsPath, nil)
	requireStatus(testInstance, "GET attempts", status, http.StatusOK, body)
	var attempts attemptsResponse
	decodeBody(testInstance, "GET attempts", body, &attempts)
	if len(attempts.Attempts) != 1 || attempts.Attempts[0].Outcome != blockedOutcome ||
		attempts.Attempts[0].ResponseSnippet != nil || attempts.Attempts[0].StatusCode != nil {
		testInstance.Fatalf("attempts = %s, want one blocked attempt with null status and snippet", body)
	}

	// Replay: the delivery is requeued, retried by the worker and dead-letters again.
	status, body = apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("/v1/admin/deliveries/%d/replay", delivery.ID), nil)
	requireStatus(testInstance, "POST replay", status, http.StatusOK, body)
	var replayed deliveryResponse
	decodeBody(testInstance, "POST replay", body, &replayed)
	if replayed.Delivery.ID != delivery.ID {
		testInstance.Fatalf("replayed delivery id = %d, want %d", replayed.Delivery.ID, delivery.ID)
	}
	waitForSecondAttempt(testInstance, messageID, delivery.ID)

	status, body = apiRequest(testInstance, http.MethodGet, attemptsPath, nil)
	requireStatus(testInstance, "GET attempts after replay", status, http.StatusOK, body)
	var afterReplay attemptsResponse
	decodeBody(testInstance, "GET attempts after replay", body, &afterReplay)
	if len(afterReplay.Attempts) != 2 || afterReplay.Attempts[0].AttemptNumber != 1 ||
		afterReplay.Attempts[1].AttemptNumber != 2 || afterReplay.Attempts[1].Outcome != blockedOutcome {
		testInstance.Fatalf("attempts after replay = %s, want attempt numbers 1 and 2, both blocked", body)
	}

	// A replay of a delivery that does not exist is a 404.
	status, body = apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("/v1/admin/deliveries/%d/replay", delivery.ID+1_000_000_000), nil)
	requireStatus(testInstance, "POST replay of an absent delivery", status, http.StatusNotFound, body)

	assertSubmitRejections(testInstance, channel)
}

// waitForSecondAttempt polls until the replayed delivery is dead again with two
// attempts logged.
func waitForSecondAttempt(testInstance *testing.T, messageID string, deliveryID int64) {
	testInstance.Helper()
	deadline := time.Now().Add(replayPollTimeout)
	var last deliveryJSON
	for time.Now().Before(deadline) {
		for _, candidate := range getMessage(testInstance, messageID).Deliveries {
			if candidate.ID == deliveryID {
				last = candidate
			}
		}
		if last.State == deadStateName && last.AttemptCount == 2 {
			return
		}
		time.Sleep(replayPollInterval)
	}
	testInstance.Fatalf("replayed delivery never dead-lettered again after 2 attempts; last = %+v", last)
}

func containsDelivery(deliveries []deliveryJSON, deliveryID int64) bool {
	for _, candidate := range deliveries {
		if candidate.ID == deliveryID {
			return true
		}
	}
	return false
}

// assertSubmitRejections covers the submit-time refusals: an invalid recipient, a channel
// with no route and a destination whose type has no sink yet. None stores a message.
func assertSubmitRejections(testInstance *testing.T, routedChannel string) {
	testInstance.Helper()

	recipientBody := submitBody(routedChannel, "it-recipient-"+randomSuffix(testInstance))
	recipientBody["to"] = map[string]string{"recipient": "not-an-email"}
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath, recipientBody)
	requireStatus(testInstance, "POST message with an invalid recipient", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "POST message with an invalid recipient", body,
		"to.recipient must be an email or person_hash")

	unroutedChannel := "it_" + randomSuffix(testInstance)
	status, body = apiRequest(testInstance, http.MethodPost, messagesPath,
		submitBody(unroutedChannel, "it-unrouted-"+randomSuffix(testInstance)))
	requireStatus(testInstance, "POST message with no route", status, http.StatusUnprocessableEntity, body)
	requireErrorMessage(testInstance, "POST message with no route", body, "no destination matched")

	ntfySuffix := randomSuffix(testInstance)
	ntfyChannel := "it_" + ntfySuffix
	ntfyName := "itest_" + ntfySuffix
	createDestination(testInstance, ntfyName, map[string]any{
		"type":   registry.TypeNtfy,
		"config": map[string]string{"topic": "itest_" + ntfySuffix},
	})
	createRoute(testInstance, ntfyChannel, ntfyName)
	status, body = apiRequest(testInstance, http.MethodPost, messagesPath,
		submitBody(ntfyChannel, "it-ntfy-"+randomSuffix(testInstance)))
	// ntfy is a supported type, so a message without display text is refused by
	// validation (400) before any destination is resolved.
	requireStatus(testInstance, "POST message without display to an ntfy destination", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "POST message without display to an ntfy destination", body,
		"display.title or display.body is required for ntfy and discord destinations")
}

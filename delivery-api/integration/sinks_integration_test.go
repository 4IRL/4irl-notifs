//go:build integration

package integration

// End-to-end test of the ntfy recipient path against the live compose stack:
// provisioning-api provisions a subscriber, delivery-api mints the app's ntfy
// publisher through provisioning-api, a to.recipient envelope is submitted, and
// the title and body are read back from the subscriber's real ntfy topic using
// the subscriber's own token. It also proves an app with no publisher fails
// closed with 422 at submit.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
)

const (
	defaultAPIBaseURL = "http://127.0.0.1:8091"
	defaultNtfyURL    = "http://127.0.0.1:8090"

	sinkChannel          = "all"
	sinkDeliveredState   = "delivered"
	sinkMessageTitle     = "Sink integration title"
	sinkMessageBody      = "Sink integration body"
	ntfyPollTimeout      = 15 * time.Second
	ntfyPollInterval     = 250 * time.Millisecond
	ntfyMessageEventName = "message"
)

// sinkAppIDPattern guards the SQL cleanup below against interpolating anything
// but the test's own generated app id.
var sinkAppIDPattern = regexp.MustCompile(`^it[0-9a-f]{12}$`)

// sinkHTTPClient bounds each call to provisioning-api and ntfy so a hung peer fails the test.
var sinkHTTPClient = &http.Client{Timeout: httpRequestTimeout}

// publisherMintResponse mirrors the publisher view of the mint endpoint. It carries no token.
type publisherMintResponse struct {
	Publisher struct {
		AppID           string `json:"app_id"`
		PublisherUserID string `json:"publisher_user_id"`
		TopicPattern    string `json:"topic_pattern"`
	} `json:"publisher"`
}

// provisionedSubscriber mirrors the fields of provisioning-api's /v1/provision body the test needs.
type provisionedSubscriber struct {
	PersonHash string `json:"person_hash"`
	Token      string `json:"token"`
}

// ntfyEvent is one line of an ntfy JSON stream.
type ntfyEvent struct {
	Event   string `json:"event"`
	Title   string `json:"title"`
	Message string `json:"message"`
}

func provisioningAPIURL() string {
	if value := os.Getenv("NOTIFS_API_URL"); value != "" {
		return value
	}
	return defaultAPIBaseURL
}

func ntfyBaseURL() string {
	if value := os.Getenv("NOTIFS_NTFY_URL"); value != "" {
		return value
	}
	return defaultNtfyURL
}

// provisioningRequest sends one request to provisioning-api and returns the status and raw body.
func provisioningRequest(testInstance *testing.T, method, path string, body any) (int, []byte) {
	testInstance.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			testInstance.Fatalf("encoding %s %s body: %v", method, path, marshalErr)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, requestErr := http.NewRequestWithContext(context.Background(), method, provisioningAPIURL()+path, requestBody)
	if requestErr != nil {
		testInstance.Fatalf("building %s %s request: %v", method, path, requestErr)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, doErr := sinkHTTPClient.Do(request)
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

// deprovisionUser removes an ntfy user through provisioning-api, as provisioning-api's own suite does.
func deprovisionUser(testInstance *testing.T, userID string) {
	testInstance.Helper()
	status, body := provisioningRequest(testInstance, http.MethodDelete, "/v1/users/"+userID, nil)
	if status >= http.StatusInternalServerError {
		testInstance.Errorf("deprovisioning %s status = %d (body: %s)", userID, status, body)
	}
}

// registerSinkAppCleanup deletes the app's messages (deliveries and attempts
// cascade) and its stored publisher row from the delivery database.
func registerSinkAppCleanup(testInstance *testing.T, appID string) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		if !sinkAppIDPattern.MatchString(appID) {
			testInstance.Errorf("cleanup: refusing to delete rows for malformed app id %q", appID)
			return
		}
		psql(testInstance, fmt.Sprintf("delete from messages where app_id = '%s'", appID))
		psql(testInstance, fmt.Sprintf("delete from app_publishers where app_id = '%s'", appID))
	})
}

// readNtfyTopic polls a topic's cache with a bearer token and returns the message events.
func readNtfyTopic(testInstance *testing.T, topic, token string) []ntfyEvent {
	testInstance.Helper()
	request, requestErr := http.NewRequestWithContext(
		context.Background(), http.MethodGet, ntfyBaseURL()+"/"+topic+"/json?poll=1&since=all", nil)
	if requestErr != nil {
		testInstance.Fatalf("building ntfy read request: %v", requestErr)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, doErr := sinkHTTPClient.Do(request)
	if doErr != nil {
		testInstance.Fatalf("reading ntfy topic %s: %v", topic, doErr)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			testInstance.Errorf("closing ntfy read body: %v", closeErr)
		}
	}()
	if response.StatusCode != http.StatusOK {
		testInstance.Fatalf("reading ntfy topic %s status = %d, want 200", topic, response.StatusCode)
	}
	var events []ntfyEvent
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event ntfyEvent
		if decodeErr := json.Unmarshal([]byte(line), &event); decodeErr != nil {
			testInstance.Fatalf("decoding ntfy line %q: %v", line, decodeErr)
		}
		if event.Event == ntfyMessageEventName {
			events = append(events, event)
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		testInstance.Fatalf("scanning ntfy topic %s: %v", topic, scanErr)
	}
	return events
}

func recipientSubmitBody(appID, recipient, idempotencyKey string) map[string]any {
	return map[string]any{
		"app_id":          appID,
		"to":              map[string]any{"recipient": recipient},
		"channel":         sinkChannel,
		"idempotency_key": idempotencyKey,
		"payload":         map[string]any{"n": 1},
		"display":         map[string]any{"title": sinkMessageTitle, "body": sinkMessageBody},
	}
}

func TestRecipientDeliveryThroughNtfy(testInstance *testing.T) {
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	appID := "it" + suffix[:12]
	email := "it-sinks-" + suffix + "@example.com"
	registerSinkAppCleanup(testInstance, appID)

	// Registered first so (LIFO) they run after the delivery-database cleanup.
	subscriberUserID := personhash.NtfyUser(email)
	publisherUserID := appID + "-publisher"
	testInstance.Cleanup(func() { deprovisionUser(testInstance, publisherUserID) })
	testInstance.Cleanup(func() { deprovisionUser(testInstance, subscriberUserID) })

	// 1. Provision the subscriber.
	status, body := provisioningRequest(testInstance, http.MethodPost, "/v1/provision",
		map[string]string{"app_id": appID, "email": email})
	requireStatus(testInstance, "POST provision", status, http.StatusOK, body)
	var subscriber provisionedSubscriber
	decodeBody(testInstance, "POST provision", body, &subscriber)
	if subscriber.Token == "" || subscriber.PersonHash != personhash.Hash(email) {
		testInstance.Fatalf("provision returned person_hash %q (want %q) and token set=%v",
			subscriber.PersonHash, personhash.Hash(email), subscriber.Token != "")
	}

	// 2. Mint the app's publisher through delivery-api.
	status, body = apiRequest(testInstance, http.MethodPost, adminBasePath+"/apps/"+appID+"/ntfy-publisher", nil)
	requireStatus(testInstance, "POST ntfy-publisher", status, http.StatusOK, body)
	var minted publisherMintResponse
	decodeBody(testInstance, "POST ntfy-publisher", body, &minted)
	if minted.Publisher.AppID != appID || minted.Publisher.PublisherUserID != publisherUserID {
		testInstance.Fatalf("minted publisher = %+v, want app %q user %q", minted.Publisher, appID, publisherUserID)
	}

	// 3. Submit a recipient envelope and wait for the first attempt.
	idempotencyKey := "it-" + suffix
	status, body = apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s?wait=%d", messagesPath, submitWaitSeconds), recipientSubmitBody(appID, email, idempotencyKey))
	requireStatus(testInstance, "POST recipient message", status, http.StatusAccepted, body)
	var submitted submitResponse
	decodeBody(testInstance, "POST recipient message", body, &submitted)
	if len(submitted.Message.Deliveries) != 1 {
		testInstance.Fatalf("deliveries = %d, want 1 (body: %s)", len(submitted.Message.Deliveries), body)
	}
	if delivery := submitted.Message.Deliveries[0]; delivery.State != sinkDeliveredState {
		testInstance.Fatalf("delivery = %+v, want state %q (body: %s)", delivery, sinkDeliveredState, body)
	}

	// 4. The subscriber reads the title and body on their own topic with their own token.
	topic := personhash.PersonChannelTopic(appID, subscriber.PersonHash, sinkChannel)
	deadline := time.Now().Add(ntfyPollTimeout)
	var events []ntfyEvent
	for {
		events = readNtfyTopic(testInstance, topic, subscriber.Token)
		if len(events) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(ntfyPollInterval)
	}
	if len(events) != 1 {
		testInstance.Fatalf("ntfy topic %s holds %d messages, want 1", topic, len(events))
	}
	if events[0].Title != sinkMessageTitle || events[0].Message != sinkMessageBody {
		testInstance.Fatalf("ntfy message = (title %q, message %q), want (%q, %q)",
			events[0].Title, events[0].Message, sinkMessageTitle, sinkMessageBody)
	}
}

func TestRecipientWithoutPublisherIsRejected(testInstance *testing.T) {
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	appID := "it" + suffix[:12]
	registerSinkAppCleanup(testInstance, appID)

	status, body := apiRequest(testInstance, http.MethodPost, messagesPath,
		recipientSubmitBody(appID, "it-nopub-"+suffix+"@example.com", "it-"+suffix))
	requireStatus(testInstance, "POST recipient message without publisher", status, http.StatusUnprocessableEntity, body)
	requireErrorMessage(testInstance, "POST recipient message without publisher", body, "app has no ntfy publisher")
}

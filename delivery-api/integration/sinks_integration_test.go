//go:build integration

package integration

// End-to-end test of the ntfy recipient path against the live compose stack:
// delivery-api provisions a subscriber through the app door, mints the app's ntfy
// publisher, a to.recipient envelope is submitted, and
// the title and body are read back from the subscriber's real ntfy topic using
// the subscriber's own token. It also proves an app with no publisher fails
// closed with 422 at submit.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
)

const (
	defaultNtfyURL = "http://127.0.0.1:8090"

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

// sinkHTTPClient bounds each ntfy call so a hung peer fails the test.
var sinkHTTPClient = &http.Client{Timeout: httpRequestTimeout}

// publisherMintResponse mirrors the publisher view of the mint endpoint. It carries no token.
type publisherMintResponse struct {
	Publisher struct {
		AppID           string `json:"app_id"`
		PublisherUserID string `json:"publisher_user_id"`
		TopicPattern    string `json:"topic_pattern"`
	} `json:"publisher"`
}

// provisionedSubscriber mirrors the fields of the app-door /v1/apps/{app_id}/provision body the test needs.
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

func ntfyBaseURL() string {
	if value := os.Getenv("NOTIFS_NTFY_URL"); value != "" {
		return value
	}
	return defaultNtfyURL
}

// deprovisionUser removes an ntfy user through the operator door. The route is idempotent, so an
// unknown user still answers 200; any other status (including a transient 502) fails the cleanup.
func deprovisionUser(testInstance *testing.T, userID string) {
	testInstance.Helper()
	status, body := adminRequest(testInstance, http.MethodDelete, "/users/"+userID, nil)
	if status != http.StatusOK {
		testInstance.Errorf("deprovisioning %s status = %d, want 200 (body: %s)", userID, status, body)
		return
	}
	var deleted struct {
		Deleted bool `json:"deleted"`
	}
	if decodeErr := json.Unmarshal(body, &deleted); decodeErr != nil || !deleted.Deleted {
		testInstance.Errorf("deprovisioning %s body = %s, want deleted: true (decode error: %v)", userID, body, decodeErr)
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
	publisherUserID := appID + "-publisher"
	testInstance.Cleanup(func() { deprovisionUser(testInstance, publisherUserID) })

	// 1. Bind the app (publisher mint refuses an app with no approved binding), then provision the
	// subscriber through the app door with the app's own key (provisionSubscriber registers the
	// subscriber's removal).
	key := appKeyFor(testInstance, appID)
	subscriber := provisionSubscriber(testInstance, appID, key, email)
	if subscriber.PersonHash != personhash.Hash(email) {
		testInstance.Fatalf("provision returned person_hash %q, want %q", subscriber.PersonHash, personhash.Hash(email))
	}

	// 2. Mint the app's publisher through delivery-api.
	status, body := apiRequest(testInstance, http.MethodPost, adminBasePath+"/apps/"+appID+"/ntfy-publisher", nil)
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

	// 5. Removing the publisher (the operator step after POST /v1/deprovision-app) is
	// idempotent and makes later recipient submits fail closed.
	publisherPath := adminBasePath + "/apps/" + appID + "/ntfy-publisher"
	for attempt := 1; attempt <= 2; attempt++ {
		status, body = apiRequest(testInstance, http.MethodDelete, publisherPath, nil)
		requireStatus(testInstance, fmt.Sprintf("DELETE ntfy-publisher #%d", attempt), status, http.StatusNoContent, body)
	}
	status, body = apiRequest(testInstance, http.MethodGet, publisherPath, nil)
	requireStatus(testInstance, "GET ntfy-publisher after DELETE", status, http.StatusNotFound, body)
	status, body = apiRequest(testInstance, http.MethodPost, messagesPath,
		recipientSubmitBody(appID, email, "it-removed-"+suffix))
	requireStatus(testInstance, "POST recipient message after DELETE", status, http.StatusUnprocessableEntity, body)
	requireErrorMessage(testInstance, "POST recipient message after DELETE", body, "app has no ntfy publisher")
}

// routedRecipientBody is recipientSubmitBody with an explicit to.via and optional destination ids.
func routedRecipientBody(appID, recipient, idempotencyKey, via string, ids []int64) map[string]any {
	body := recipientSubmitBody(appID, recipient, idempotencyKey)
	routing := map[string]any{"recipient": recipient, "via": via}
	if ids != nil {
		routing["personal_destination_ids"] = ids
	}
	body["to"] = routing
	return body
}

func TestRecipientWithoutPublisherIsRejected(testInstance *testing.T) {
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	appID := "it" + suffix[:12]
	email := "it-nopub-" + suffix + "@example.com"
	registerSinkAppCleanup(testInstance, appID)

	// Default, explicit ntfy and both (with an id: resolution reaches the publisher check first) all need a publisher.
	cases := map[string]map[string]any{
		"default": recipientSubmitBody(appID, email, "it-default-"+suffix),
		"ntfy":    routedRecipientBody(appID, email, "it-ntfy-"+suffix, "ntfy", nil),
		"both":    routedRecipientBody(appID, email, "it-both-"+suffix, "both", []int64{1}),
	}
	for name, requestBody := range cases {
		status, body := apiRequest(testInstance, http.MethodPost, messagesPath, requestBody)
		requireStatus(testInstance, "POST recipient message without publisher ("+name+")",
			status, http.StatusUnprocessableEntity, body)
		requireErrorMessage(testInstance, "POST recipient message without publisher ("+name+")",
			body, "app has no ntfy publisher")
	}
}

func TestRecipientViaPersonalWorksWithoutPublisher(testInstance *testing.T) {
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	appID := "it" + suffix[:12]
	email := "it-viapers-" + suffix + "@example.com"
	hash := personhash.Hash(email)
	registerSinkAppCleanup(testInstance, appID)
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("delete from personal_destinations where person_hash = '%s'", hash))
	})

	// An unknown id is refused with the generic 422 (the app has no publisher: via=personal never needs one).
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath,
		routedRecipientBody(appID, email, "it-unknown-"+suffix, "personal", []int64{999999999}))
	requireStatus(testInstance, "POST via=personal with an unknown id", status, http.StatusUnprocessableEntity, body)
	requireErrorMessage(testInstance, "POST via=personal with an unknown id", body,
		"personal destination not available for recipient")

	// Register an opted-in destination as the app.
	status, body = personalRequest(testInstance, appID, http.MethodPost, "", map[string]any{
		"email": email, "label": "Phone", "type": "webhook",
		"secrets": map[string]string{
			"url":            publicWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("q", signingSecretLength))[:signingSecretLength],
		},
	})
	requireStatus(testInstance, "register personal destination", status, http.StatusCreated, body)
	var created personalEnvelopeJSON
	decodeBody(testInstance, "created personal destination", body, &created)

	status, body = apiRequest(testInstance, http.MethodPost, messagesPath,
		routedRecipientBody(appID, email, "it-named-"+suffix, "personal", []int64{created.PersonalDestination.ID}))
	requireStatus(testInstance, "POST via=personal with a named destination", status, http.StatusAccepted, body)
	var submitted submitResponse
	decodeBody(testInstance, "POST via=personal", body, &submitted)
	wantName := fmt.Sprintf("@personal_%d", created.PersonalDestination.ID)
	if len(submitted.Message.Deliveries) != 1 || submitted.Message.Deliveries[0].Destination != wantName {
		testInstance.Fatalf("deliveries = %+v, want only %s (body: %s)", submitted.Message.Deliveries, wantName, body)
	}
}

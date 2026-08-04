//go:build integration

// Package integration exercises the provisioning-api against a live
// docker-compose stack (ntfy + provisioning-api). Run with the local stack up:
//
//	docker compose --project-directory . -f docker-compose.yml up -d --build
//	go test -tags integration ./integration/...
//
// The API base URL and the ntfy publish URL are overridable via NOTIFS_API_URL
// and NTFY_URL for non-default port mappings.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personhash"
)

const (
	defaultAPIBaseURL = "http://127.0.0.1:8091"
	defaultNtfyURL    = "http://127.0.0.1:8090"
)

func apiBaseURL() string {
	if value := os.Getenv("NOTIFS_API_URL"); value != "" {
		return value
	}
	return defaultAPIBaseURL
}

func ntfyURL() string {
	if value := os.Getenv("NTFY_URL"); value != "" {
		return value
	}
	return defaultNtfyURL
}

// provisionResponse mirrors the /v1/provision success body.
type provisionResponse struct {
	UserID         string `json:"user_id"`
	AppID          string `json:"app_id"`
	PersonHash     string `json:"person_hash"`
	TopicPattern   string `json:"topic_pattern"`
	BroadcastTopic string `json:"broadcast_topic"`
	Token          string `json:"token"`
}

// provisionAppResponse mirrors the /v1/provision-app success body.
type provisionAppResponse struct {
	AppID           string `json:"app_id"`
	PublisherUserID string `json:"publisher_user_id"`
	TopicPattern    string `json:"topic_pattern"`
	Token           string `json:"token"`
}

// testNotifyResult mirrors one entry of the /v1/test-notify results array.
type testNotifyResult struct {
	Recipient string `json:"recipient"`
	UserID    string `json:"user_id"`
	Topic     string `json:"topic"`
	OK        bool   `json:"ok"`
	MessageID string `json:"message_id"`
	Error     string `json:"error"`
}

// testNotifyResponse mirrors the /v1/test-notify success body.
type testNotifyResponse struct {
	Results []testNotifyResult `json:"results"`
}

// ntfyMessage mirrors the subset of an ntfy cache message this test cares
// about, as served by GET {topic}/json?poll=1&since=all (newline-delimited
// JSON, one message per line).
type ntfyMessage struct {
	ID string `json:"id"`
}

// userListResponse mirrors the /v1/users body.
type userListResponse struct {
	Users []struct {
		UserID        string   `json:"user_id"`
		Apps          []string `json:"apps"`
		TopicPatterns []string `json:"topic_patterns"`
	} `json:"users"`
}

// postJSON issues a POST with a JSON body and returns the status code and body.
func postJSON(t *testing.T, path string, payload map[string]string) (int, []byte) {
	t.Helper()
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		t.Fatalf("marshaling payload: %v", marshalErr)
	}
	response, postErr := http.Post(apiBaseURL()+path, "application/json", bytes.NewReader(encoded))
	if postErr != nil {
		t.Fatalf("POST %s: %v", path, postErr)
	}
	defer closeBody(t, response)
	body := readBody(t, response)
	return response.StatusCode, body
}

// postJSONBody issues a POST with an arbitrary JSON-serializable payload and
// returns the status code and body. Unlike postJSON (which takes a flat
// map[string]string), this supports nested/array fields such as the
// test-notify `recipients` list.
func postJSONBody(t *testing.T, path string, payload any) (int, []byte) {
	t.Helper()
	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		t.Fatalf("marshaling payload: %v", marshalErr)
	}
	response, postErr := http.Post(apiBaseURL()+path, "application/json", bytes.NewReader(encoded))
	if postErr != nil {
		t.Fatalf("POST %s: %v", path, postErr)
	}
	defer closeBody(t, response)
	body := readBody(t, response)
	return response.StatusCode, body
}

// readBody drains and returns a response body.
func readBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	buffer := new(bytes.Buffer)
	if _, copyErr := buffer.ReadFrom(response.Body); copyErr != nil {
		t.Fatalf("reading body: %v", copyErr)
	}
	return buffer.Bytes()
}

// closeBody closes a response body, failing the test if the close errors.
func closeBody(t *testing.T, response *http.Response) {
	t.Helper()
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatalf("closing response body: %v", closeErr)
	}
}

// readStatus issues a poll-all GET against a topic with a bearer token and
// returns the ntfy HTTP status (200 authorized to read, 403/401 denied).
func readStatus(t *testing.T, topic string, token string) int {
	t.Helper()
	request, buildErr := http.NewRequest(http.MethodGet, ntfyURL()+"/"+topic+"/json?poll=1&since=all", nil)
	if buildErr != nil {
		t.Fatalf("building read request: %v", buildErr)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, readErr := http.DefaultClient.Do(request)
	if readErr != nil {
		t.Fatalf("reading %s: %v", topic, readErr)
	}
	defer closeBody(t, response)
	return response.StatusCode
}

// publishStatus publishes a message to a topic with a bearer token and returns
// the ntfy HTTP status (200 authorized, 403/401 denied).
func publishStatus(t *testing.T, topic string, token string) int {
	t.Helper()
	request, buildErr := http.NewRequest(http.MethodPost, ntfyURL()+"/"+topic, strings.NewReader("integration ping"))
	if buildErr != nil {
		t.Fatalf("building publish request: %v", buildErr)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, publishErr := http.DefaultClient.Do(request)
	if publishErr != nil {
		t.Fatalf("publishing to %s: %v", topic, publishErr)
	}
	defer closeBody(t, response)
	return response.StatusCode
}

// publishMessage publishes a message to a topic with a bearer token and
// returns the ntfy HTTP status plus the published message id (empty when the
// publish was rejected or the body did not decode).
func publishMessage(t *testing.T, topic string, token string) (int, string) {
	t.Helper()
	request, buildErr := http.NewRequest(http.MethodPost, ntfyURL()+"/"+topic, strings.NewReader("integration publish "+time.Now().String()))
	if buildErr != nil {
		t.Fatalf("building publish request: %v", buildErr)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, publishErr := http.DefaultClient.Do(request)
	if publishErr != nil {
		t.Fatalf("publishing to %s: %v", topic, publishErr)
	}
	defer closeBody(t, response)
	body := readBody(t, response)
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, ""
	}
	var published ntfyMessage
	if unmarshalErr := json.Unmarshal(body, &published); unmarshalErr != nil {
		t.Fatalf("unmarshaling publish response: %v", unmarshalErr)
	}
	return response.StatusCode, published.ID
}

// topicContainsMessageID polls a topic's cache with a bearer token and
// reports whether messageID appears among the newline-delimited JSON
// messages returned.
func topicContainsMessageID(t *testing.T, topic string, token string, messageID string) bool {
	t.Helper()
	request, buildErr := http.NewRequest(http.MethodGet, ntfyURL()+"/"+topic+"/json?poll=1&since=all", nil)
	if buildErr != nil {
		t.Fatalf("building read request: %v", buildErr)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, readErr := http.DefaultClient.Do(request)
	if readErr != nil {
		t.Fatalf("reading %s: %v", topic, readErr)
	}
	defer closeBody(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("polling %s status = %d, expected 200", topic, response.StatusCode)
	}
	body := readBody(t, response)
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var message ntfyMessage
		if unmarshalErr := json.Unmarshal([]byte(line), &message); unmarshalErr != nil {
			t.Fatalf("unmarshaling cache line %q: %v", line, unmarshalErr)
		}
		if message.ID == messageID {
			return true
		}
	}
	return false
}

// deleteUser removes a user via the API; used for cleanup.
func deleteUser(t *testing.T, userID string) {
	t.Helper()
	request, buildErr := http.NewRequest(http.MethodDelete, apiBaseURL()+"/v1/users/"+userID, nil)
	if buildErr != nil {
		t.Fatalf("building delete request: %v", buildErr)
	}
	response, deleteErr := http.DefaultClient.Do(request)
	if deleteErr != nil {
		t.Fatalf("deleting %s: %v", userID, deleteErr)
	}
	closeBody(t, response)
}

// waitForHealth blocks until the API health endpoint returns 200 or times out.
func waitForHealth(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, getErr := http.Get(apiBaseURL() + "/healthz")
		if getErr == nil {
			closeBody(t, response)
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("provisioning-api health never became ready at %s", apiBaseURL())
}

func TestProvisionGrantsScopedReadOnlyAccess(t *testing.T) {
	waitForHealth(t)
	const email = "itest-scope@example.com"
	const appID = "itestscope"
	personHash := personhash.Hash(email)
	ntfyUserID := personhash.NtfyUser(email)
	t.Cleanup(func() { deleteUser(t, ntfyUserID) })

	status, body := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "email": email})
	if status != http.StatusOK {
		t.Fatalf("provision status = %d, body = %s", status, body)
	}
	var provisioned provisionResponse
	if unmarshalErr := json.Unmarshal(body, &provisioned); unmarshalErr != nil {
		t.Fatalf("unmarshaling provision response: %v", unmarshalErr)
	}
	if provisioned.Token == "" {
		t.Fatal("provision returned an empty token")
	}
	if provisioned.UserID != ntfyUserID {
		t.Fatalf("user_id = %q, expected %q", provisioned.UserID, ntfyUserID)
	}
	wantTopicPattern := appID + "-" + personHash + "-*"
	if provisioned.TopicPattern != wantTopicPattern {
		t.Fatalf("topic_pattern = %q, expected %q", provisioned.TopicPattern, wantTopicPattern)
	}

	ownTopic := appID + "-" + personHash + "-test"
	if inNamespace := readStatus(t, ownTopic, provisioned.Token); inNamespace != http.StatusOK {
		t.Fatalf("in-namespace read status = %d, expected 200", inNamespace)
	}

	otherPersonHash := personhash.Hash("someone-else@example.com")
	crossTopic := appID + "-" + otherPersonHash + "-test"
	if crossPerson := readStatus(t, crossTopic, provisioned.Token); crossPerson != http.StatusForbidden {
		t.Fatalf("cross-person read status = %d, expected 403", crossPerson)
	}

	if published := publishStatus(t, ownTopic, provisioned.Token); published != http.StatusForbidden {
		t.Fatalf("publish with read-only token status = %d, expected 403", published)
	}
}

func TestReprovisionRotatesToken(t *testing.T) {
	waitForHealth(t)
	const email = "itest-rotate@example.com"
	const appID = "itestrotate"
	personHash := personhash.Hash(email)
	ntfyUserID := personhash.NtfyUser(email)
	t.Cleanup(func() { deleteUser(t, ntfyUserID) })

	_, firstBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "email": email})
	var first provisionResponse
	if err := json.Unmarshal(firstBody, &first); err != nil {
		t.Fatalf("unmarshaling first provision: %v", err)
	}

	_, secondBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "email": email})
	var second provisionResponse
	if err := json.Unmarshal(secondBody, &second); err != nil {
		t.Fatalf("unmarshaling second provision: %v", err)
	}

	if first.Token == second.Token {
		t.Fatal("re-provision returned the same token; expected rotation")
	}

	ownTopic := appID + "-" + personHash + "-test"
	if oldToken := readStatus(t, ownTopic, first.Token); oldToken == http.StatusOK {
		t.Fatalf("old token still authorized to read (status %d); expected it revoked", oldToken)
	}
	if newToken := readStatus(t, ownTopic, second.Token); newToken != http.StatusOK {
		t.Fatalf("new token status = %d, expected 200", newToken)
	}
}

func TestDeprovisionLeavesTheWholeFamilyOnLastApp(t *testing.T) {
	waitForHealth(t)
	const email = "itest-deprov@example.com"
	const app1 = "itestdeprovone"
	const app2 = "itestdeprovtwo"
	personHash := personhash.Hash(email)
	ntfyUserID := personhash.NtfyUser(email)
	t.Cleanup(func() { deleteUser(t, ntfyUserID) })

	_, provision1Body := postJSON(t, "/v1/provision", map[string]string{"app_id": app1, "email": email})
	var provisioned1 provisionResponse
	if err := json.Unmarshal(provision1Body, &provisioned1); err != nil {
		t.Fatalf("unmarshaling app1 provision: %v", err)
	}
	status, provision2Body := postJSON(t, "/v1/provision", map[string]string{"app_id": app2, "email": email})
	if status != http.StatusOK {
		t.Fatalf("app2 provision status = %d, body = %s", status, provision2Body)
	}

	// Deprovision from app1: user must remain (still provisioned into app2).
	status, deprov1Body := postJSON(t, "/v1/deprovision", map[string]string{"app_id": app1, "email": email})
	if status != http.StatusOK {
		t.Fatalf("app1 deprovision status = %d, body = %s", status, deprov1Body)
	}

	ownTopic1 := app1 + "-" + personHash + "-test"
	if revoked := readStatus(t, ownTopic1, provisioned1.Token); revoked == http.StatusOK {
		t.Fatalf("app1 token still authorized to read after deprovision (status %d)", revoked)
	}

	if !userIsListed(t, ntfyUserID) {
		t.Fatal("user was removed after deprovisioning from app1; expected it retained (still provisioned into app2)")
	}

	// Deprovision from app2 (the last app): user must be gone entirely.
	status, deprov2Body := postJSON(t, "/v1/deprovision", map[string]string{"app_id": app2, "email": email})
	if status != http.StatusOK {
		t.Fatalf("app2 deprovision status = %d, body = %s", status, deprov2Body)
	}

	if userIsListed(t, ntfyUserID) {
		t.Fatal("user still listed after deprovisioning from their last app; expected the whole family left")
	}
}

// userIsListed reports whether userID appears in GET /v1/users.
func userIsListed(t *testing.T, userID string) bool {
	t.Helper()
	response, getErr := http.Get(apiBaseURL() + "/v1/users")
	if getErr != nil {
		t.Fatalf("GET /v1/users: %v", getErr)
	}
	defer closeBody(t, response)
	var users userListResponse
	if err := json.Unmarshal(readBody(t, response), &users); err != nil {
		t.Fatalf("unmarshaling users: %v", err)
	}
	for _, user := range users.Users {
		if user.UserID == userID {
			return true
		}
	}
	return false
}

// TestDeleteUnknownUserIsIdempotent verifies the full-teardown delete contract:
// DELETE /v1/users/{id} returns 200 {deleted:true} even for an already-gone
// ntfy user (the service swallows ntfycli.ErrNotFound and best-effort
// dual-deletes the person row), rather than the pre-teardown 404. This makes a
// People-row delete idempotent and repeat-safe.
func TestDeleteUnknownUserIsIdempotent(t *testing.T) {
	waitForHealth(t)
	const unknownUserID = "u_00000000000000ge"
	request, buildErr := http.NewRequest(http.MethodDelete, apiBaseURL()+"/v1/users/"+unknownUserID, nil)
	if buildErr != nil {
		t.Fatalf("building request: %v", buildErr)
	}
	response, deleteErr := http.DefaultClient.Do(request)
	if deleteErr != nil {
		t.Fatalf("delete request: %v", deleteErr)
	}
	defer closeBody(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delete unknown user status = %d, expected 200 (idempotent full teardown)", response.StatusCode)
	}
	var responseBody struct {
		UserID  string `json:"user_id"`
		Deleted bool   `json:"deleted"`
	}
	if decodeErr := json.NewDecoder(response.Body).Decode(&responseBody); decodeErr != nil {
		t.Fatalf("decoding response body: %v", decodeErr)
	}
	if responseBody.UserID != unknownUserID || !responseBody.Deleted {
		t.Fatalf("response = %+v, expected user_id=%s deleted=true", responseBody, unknownUserID)
	}
}

func TestProvisionAppPublisherEndToEnd(t *testing.T) {
	waitForHealth(t)
	const appID = "itestpub"
	const email = "itest-pub-subscriber@example.com"
	personHash := personhash.Hash(email)
	subscriberNtfyUserID := personhash.NtfyUser(email)
	publisherNtfyUserID := appID + "-publisher"
	t.Cleanup(func() { deleteUser(t, publisherNtfyUserID) })
	t.Cleanup(func() { deleteUser(t, subscriberNtfyUserID) })

	status, provisionAppBody := postJSON(t, "/v1/provision-app", map[string]string{"app_id": appID})
	if status != http.StatusOK {
		t.Fatalf("provision-app status = %d, body = %s", status, provisionAppBody)
	}
	var provisionedApp provisionAppResponse
	if unmarshalErr := json.Unmarshal(provisionAppBody, &provisionedApp); unmarshalErr != nil {
		t.Fatalf("unmarshaling provision-app response: %v", unmarshalErr)
	}
	if provisionedApp.Token == "" {
		t.Fatal("provision-app returned an empty token")
	}
	if provisionedApp.PublisherUserID != publisherNtfyUserID {
		t.Fatalf("publisher_user_id = %q, expected %q", provisionedApp.PublisherUserID, publisherNtfyUserID)
	}
	wantTopicPattern := appID + "-*"
	if provisionedApp.TopicPattern != wantTopicPattern {
		t.Fatalf("topic_pattern = %q, expected %q", provisionedApp.TopicPattern, wantTopicPattern)
	}

	status, provisionUserBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "email": email})
	if status != http.StatusOK {
		t.Fatalf("provision status = %d, body = %s", status, provisionUserBody)
	}
	var provisionedUser provisionResponse
	if unmarshalErr := json.Unmarshal(provisionUserBody, &provisionedUser); unmarshalErr != nil {
		t.Fatalf("unmarshaling provision response: %v", unmarshalErr)
	}

	subscriberTopic := appID + "-" + personHash + "-alerts"
	publishStatusCode, messageID := publishMessage(t, subscriberTopic, provisionedApp.Token)
	if publishStatusCode != http.StatusOK {
		t.Fatalf("publisher publish to %s status = %d, expected 200", subscriberTopic, publishStatusCode)
	}
	if messageID == "" {
		t.Fatal("publisher publish returned an empty message id")
	}
	if !topicContainsMessageID(t, subscriberTopic, provisionedUser.Token, messageID) {
		t.Fatalf("subscriber token could not read published message %s back on %s", messageID, subscriberTopic)
	}

	anyOtherTopicInNamespace := appID + "-some-other-topic"
	if otherStatus, _ := publishMessage(t, anyOtherTopicInNamespace, provisionedApp.Token); otherStatus != http.StatusOK {
		t.Fatalf("publisher publish to %s status = %d, expected 200", anyOtherTopicInNamespace, otherStatus)
	}

	outsideNamespaceTopic := "otherapp-alerts"
	if outsideStatus, _ := publishMessage(t, outsideNamespaceTopic, provisionedApp.Token); outsideStatus != http.StatusForbidden {
		t.Fatalf("publisher publish outside its namespace status = %d, expected 403", outsideStatus)
	}

	if readAsPublisher := readStatus(t, subscriberTopic, provisionedApp.Token); readAsPublisher != http.StatusForbidden {
		t.Fatalf("publisher read status = %d, expected 403 (write-only, no read)", readAsPublisher)
	}

	status, secondProvisionAppBody := postJSON(t, "/v1/provision-app", map[string]string{"app_id": appID})
	if status != http.StatusOK {
		t.Fatalf("second provision-app status = %d, body = %s", status, secondProvisionAppBody)
	}
	var secondProvisionedApp provisionAppResponse
	if unmarshalErr := json.Unmarshal(secondProvisionAppBody, &secondProvisionedApp); unmarshalErr != nil {
		t.Fatalf("unmarshaling second provision-app response: %v", unmarshalErr)
	}
	if secondProvisionedApp.Token == provisionedApp.Token {
		t.Fatal("repeat provision-app returned the same token; expected an additional token")
	}

	if firstStillWorks, _ := publishMessage(t, subscriberTopic, provisionedApp.Token); firstStillWorks != http.StatusOK {
		t.Fatalf("original publisher token publish status = %d, expected 200 (additional-token semantics)", firstStillWorks)
	}
	if secondWorks, _ := publishMessage(t, subscriberTopic, secondProvisionedApp.Token); secondWorks != http.StatusOK {
		t.Fatalf("new publisher token publish status = %d, expected 200", secondWorks)
	}
}

// TestSendTestNotificationEndToEnd exercises POST /v1/test-notify against the
// live stack: it provisions a real subscriber, sends a test notification, and
// proves (1) the message is actually deliverable on the subscriber's real topic
// and (2) the ephemeral write-only token minted for the publish is revoked
// afterwards (no residual test-notify-labeled token remains on the publisher).
//
// It also documents observed ntfy behavior for a second, never-provisioned
// recipient: the ephemeral publisher's {app_id}-* write grant covers ANY topic
// in the namespace, so ntfy accepts a publish to an unsubscribed topic and the
// per-recipient result is ok:true. Here "delivered" means "accepted by ntfy",
// NOT that any device is subscribed to receive it.
func TestSendTestNotificationEndToEnd(t *testing.T) {
	waitForHealth(t)
	const appID = "itesttestnotify"
	const email = "itest-test-notify-subscriber@example.com"
	personHash := personhash.Hash(email)
	subscriberNtfyUserID := personhash.NtfyUser(email)
	publisherUserID := ntfycli.PublisherUserID(appID)
	// The publisher identity is created on demand by TestNotify's idempotent
	// identity-ensure; clean it up (and the subscriber) after the test.
	t.Cleanup(func() { deleteUser(t, publisherUserID) })
	t.Cleanup(func() { deleteUser(t, subscriberNtfyUserID) })

	// Provision a real subscriber so a concrete person topic and a subscriber
	// read token exist to verify deliverability against.
	status, provisionBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "email": email})
	if status != http.StatusOK {
		t.Fatalf("provision status = %d, body = %s", status, provisionBody)
	}
	var provisionedUser provisionResponse
	if unmarshalErr := json.Unmarshal(provisionBody, &provisionedUser); unmarshalErr != nil {
		t.Fatalf("unmarshaling provision response: %v", unmarshalErr)
	}
	if provisionedUser.Token == "" {
		t.Fatal("provision returned an empty subscriber token")
	}

	// A second recipient: a syntactically valid (16 [a-z2-7]) but
	// never-provisioned person hash, for the partial/never-subscribed case.
	const unprovisionedHash = "aaaaaaaaaaaaaaaa"

	const message = "integration test-notify message"
	status, testNotifyBody := postJSONBody(t, "/v1/test-notify", map[string]any{
		"app_id":     appID,
		"recipients": []string{personHash, unprovisionedHash},
		"channel":    "alerts",
		"message":    message,
	})
	if status != http.StatusOK {
		t.Fatalf("test-notify status = %d, body = %s", status, testNotifyBody)
	}
	var testNotify testNotifyResponse
	if unmarshalErr := json.Unmarshal(testNotifyBody, &testNotify); unmarshalErr != nil {
		t.Fatalf("unmarshaling test-notify response: %v", unmarshalErr)
	}
	if len(testNotify.Results) != 2 {
		t.Fatalf("test-notify results len = %d, expected 2; body = %s", len(testNotify.Results), testNotifyBody)
	}

	// Recipient 0 (a real subscriber): delivered with a non-empty message id on
	// the concrete {app_id}-{hash}-alerts topic, resolved to the subscriber user.
	first := testNotify.Results[0]
	wantTopic := ntfycli.PersonChannelTopic(appID, personHash, "alerts")
	if !first.OK {
		t.Fatalf("recipient[0].ok = false, error = %q; expected delivered", first.Error)
	}
	if first.MessageID == "" {
		t.Fatal("recipient[0] delivered but message_id is empty")
	}
	if first.Topic != wantTopic {
		t.Fatalf("recipient[0].topic = %q, expected %q", first.Topic, wantTopic)
	}
	if first.UserID != subscriberNtfyUserID {
		t.Fatalf("recipient[0].user_id = %q, expected %q", first.UserID, subscriberNtfyUserID)
	}

	// The published message must actually be deliverable on the subscriber's
	// real topic — read it back with the subscriber's own read token.
	if !topicContainsMessageID(t, wantTopic, provisionedUser.Token, first.MessageID) {
		t.Fatalf("subscriber token could not read test-notify message %s back on %s", first.MessageID, wantTopic)
	}

	// Recipient 1 (valid hash, never provisioned): documents observed ntfy
	// behavior. The ephemeral publisher's {app_id}-* write grant covers the
	// unused topic, so ntfy 2xxes the publish and reports ok:true — "delivered"
	// means "accepted by ntfy", not that a device is subscribed to receive it.
	second := testNotify.Results[1]
	wantSecondTopic := ntfycli.PersonChannelTopic(appID, unprovisionedHash, "alerts")
	if second.Topic != wantSecondTopic {
		t.Fatalf("recipient[1].topic = %q, expected %q", second.Topic, wantSecondTopic)
	}
	if !second.OK {
		t.Fatalf("recipient[1].ok = false, error = %q; expected ntfy to accept a publish to an "+
			"unsubscribed but in-namespace topic (ok:true == accepted by ntfy, not device-delivered)", second.Error)
	}
	if second.MessageID == "" {
		t.Fatal("recipient[1] accepted (ok:true) but message_id is empty; ntfy returns an id on a 2xx publish")
	}

	// The ephemeral write-only token minted for the publish must be revoked in
	// the always-run cleanup: NO test-notify-labeled token may remain on the app
	// publisher. Read the raw `ntfy token list` output for the publisher and
	// assert the test-notify label does not appear.
	tokenListing := runNtfyCLIInContainer(t, nil, ntfycli.TokenListArgs(publisherUserID)...)
	if strings.Contains(tokenListing, ntfycli.TestNotifyTokenLabel) {
		t.Fatalf("residual %q-labeled token found on %s after test-notify; the defer'd RemoveToken "+
			"did not run or targeted the wrong token.\ntoken list output:\n%s",
			ntfycli.TestNotifyTokenLabel, publisherUserID, tokenListing)
	}
}

// provisioningAPIContainer is the fixed container name of the provisioning-api
// service (per docker-compose.yml). The service bundles the ntfy CLI and shares
// the ntfy-auth volume with the ntfy server container, so shelling into it lets
// a test edit the same auth database the API and server use.
const provisioningAPIContainer = "4irl-notifs-provisioning-api"

// runNtfyCLIInContainer runs an ntfy CLI subcommand (ntfyArgs) inside the
// provisioning-api container via `docker exec`, failing the test on any non-zero
// exit. hostEnv entries ("NAME=value") are set on the host `docker` process and
// forwarded into the container by name via `-e NAME` (so a secret value never
// lands on the host `docker` argv, mirroring ntfycli's NTFY_PASSWORD convention).
// Using `docker exec` against the fixed container name (rather than
// `docker compose exec`) keeps the invocation independent of the test process's
// working directory, which is provisioning-api/ under `make go-integration-test`.
//
// It returns the combined stdout+stderr of the CLI invocation so a caller can
// assert on the output (e.g. the `token list` listing). Callers that only need
// the run-or-fail behavior may discard the returned string.
func runNtfyCLIInContainer(t *testing.T, hostEnv []string, ntfyArgs ...string) string {
	t.Helper()
	dockerArgs := []string{"exec"}
	for _, entry := range hostEnv {
		name, _, _ := strings.Cut(entry, "=")
		dockerArgs = append(dockerArgs, "-e", name)
	}
	dockerArgs = append(dockerArgs, provisioningAPIContainer, "ntfy")
	dockerArgs = append(dockerArgs, ntfyArgs...)
	command := exec.Command("docker", dockerArgs...)
	command.Env = append(os.Environ(), hostEnv...)
	output, runErr := command.CombinedOutput()
	if runErr != nil {
		t.Fatalf("docker exec ntfy %v failed: %v\noutput: %s", ntfyArgs, runErr, output)
	}
	return string(output)
}

// grantLegacyScopedGrantViaCLI constructs a broadcast-less "legacy" subscriber —
// the shape a subscriber provisioned before per-app-broadcast shipped would have
// — by creating the ntfy user and granting ONLY the scoped per-person pattern
// directly via the ntfy CLI, bypassing POST /v1/provision entirely (so no
// {app_id}-broadcast grant is ever created for this user). It reuses the same
// ntfycli arg builders production uses (UserAddArgs/AccessGrantArgs) so the test
// cannot drift from the real CLI arg shapes, and passes the password out-of-band
// via NTFY_PASSWORD (never on argv), matching ntfycli.AddUser's convention.
func grantLegacyScopedGrantViaCLI(t *testing.T, ntfyUserID string, scopedTopicPattern string) {
	t.Helper()
	const legacyPassword = "legacy-subscriber-password"
	runNtfyCLIInContainer(t, []string{"NTFY_PASSWORD=" + legacyPassword}, ntfycli.UserAddArgs(ntfyUserID)...)
	runNtfyCLIInContainer(t, nil, ntfycli.AccessGrantArgs(ntfyUserID, scopedTopicPattern, ntfycli.PermissionReadOnly)...)
}

// TestProvisionBroadcastEndToEnd proves the per-app broadcast topic end-to-end
// against real ntfy ACL enforcement: the publisher writes {app_id}-broadcast with
// its existing write-only {app_id}-* grant (no publisher change), this app's
// subscriber reads it back (200), and a DIFFERENT app's subscriber is denied
// (403) — confirming Shape A (an authenticated per-user read grant, not ntfy's
// everyone/anonymous grant, so the deny-all posture is preserved).
func TestProvisionBroadcastEndToEnd(t *testing.T) {
	waitForHealth(t)
	const appID = "itestbcast"
	const subscriberEmail = "itest-bcast-subscriber@example.com"
	const otherAppID = "itestbcastother"
	const otherEmail = "itest-bcast-other@example.com"

	subscriberNtfyUserID := personhash.NtfyUser(subscriberEmail)
	otherSubscriberNtfyUserID := personhash.NtfyUser(otherEmail)
	publisherNtfyUserID := appID + "-publisher"
	t.Cleanup(func() { deleteUser(t, publisherNtfyUserID) })
	t.Cleanup(func() { deleteUser(t, subscriberNtfyUserID) })
	t.Cleanup(func() { deleteUser(t, otherSubscriberNtfyUserID) })

	// Provision the app publisher: its write-only {app_id}-* grant already covers
	// {app_id}-broadcast, so no publisher-side change is needed for broadcast.
	status, provisionAppBody := postJSON(t, "/v1/provision-app", map[string]string{"app_id": appID})
	if status != http.StatusOK {
		t.Fatalf("provision-app status = %d, body = %s", status, provisionAppBody)
	}
	var provisionedApp provisionAppResponse
	if unmarshalErr := json.Unmarshal(provisionAppBody, &provisionedApp); unmarshalErr != nil {
		t.Fatalf("unmarshaling provision-app response: %v", unmarshalErr)
	}
	if provisionedApp.Token == "" {
		t.Fatal("provision-app returned an empty token")
	}

	// Provision a subscriber of this app; Provision grants it read on {app_id}-broadcast.
	status, provisionUserBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "email": subscriberEmail})
	if status != http.StatusOK {
		t.Fatalf("provision status = %d, body = %s", status, provisionUserBody)
	}
	var provisionedUser provisionResponse
	if unmarshalErr := json.Unmarshal(provisionUserBody, &provisionedUser); unmarshalErr != nil {
		t.Fatalf("unmarshaling provision response: %v", unmarshalErr)
	}
	wantBroadcastTopic := appID + "-broadcast"
	if provisionedUser.BroadcastTopic != wantBroadcastTopic {
		t.Fatalf("broadcast_topic = %q, expected %q", provisionedUser.BroadcastTopic, wantBroadcastTopic)
	}

	// Provision a subscriber of a DIFFERENT app; it must NOT be able to read this
	// app's broadcast topic (its grants are scoped to the other app's namespace).
	status, otherUserBody := postJSON(t, "/v1/provision", map[string]string{"app_id": otherAppID, "email": otherEmail})
	if status != http.StatusOK {
		t.Fatalf("other-app provision status = %d, body = %s", status, otherUserBody)
	}
	var otherUser provisionResponse
	if unmarshalErr := json.Unmarshal(otherUserBody, &otherUser); unmarshalErr != nil {
		t.Fatalf("unmarshaling other-app provision response: %v", unmarshalErr)
	}
	// Guard the token is present so the 403 below proves an authenticated
	// different-app subscriber is denied (Shape A), not that an anonymous read
	// was rejected for lack of any token under deny-all.
	if otherUser.Token == "" {
		t.Fatal("other-app provision returned an empty token")
	}

	// The publisher publishes the broadcast with its existing write-only grant.
	publishStatusCode, messageID := publishMessage(t, wantBroadcastTopic, provisionedApp.Token)
	if publishStatusCode != http.StatusOK {
		t.Fatalf("publisher publish to %s status = %d, expected 200", wantBroadcastTopic, publishStatusCode)
	}
	if messageID == "" {
		t.Fatal("publisher broadcast publish returned an empty message id")
	}

	// This app's subscriber can read the broadcast back (200 + message present).
	if !topicContainsMessageID(t, wantBroadcastTopic, provisionedUser.Token, messageID) {
		t.Fatalf("subscriber token could not read broadcast message %s back on %s", messageID, wantBroadcastTopic)
	}

	// A different app's subscriber is denied (403) — the Shape A per-user grant
	// scopes the broadcast to this app's subscribers, not to everyone.
	if crossApp := readStatus(t, wantBroadcastTopic, otherUser.Token); crossApp != http.StatusForbidden {
		t.Fatalf("cross-app subscriber read of %s status = %d, expected 403", wantBroadcastTopic, crossApp)
	}
}

// TestDeprovisionLegacySubscriberWithoutBroadcastGrantSucceeds empirically
// verifies the Decision 3 assumption: Deprovision tolerates the broadcast
// ResetAccess on a {app_id}-broadcast topic the user was never granted. Unit
// tests assert this via the fake client; this test proves it against the real
// ntfy CLI by constructing a broadcast-less "legacy" subscriber (only the scoped
// grant, created directly via the CLI — no {app_id}-broadcast grant) and then
// deprovisioning it. A non-200 here would mean real ntfy does NOT classify a
// reset on a never-granted topic as ErrNotFound, contradicting Decision 3.
func TestDeprovisionLegacySubscriberWithoutBroadcastGrantSucceeds(t *testing.T) {
	waitForHealth(t)
	const email = "itest-legacy@example.com"
	const appID = "itestlegacy"
	personHash := personhash.Hash(email)
	ntfyUserID := personhash.NtfyUser(email)
	t.Cleanup(func() { deleteUser(t, ntfyUserID) })

	// Build the legacy subscriber directly via the ntfy CLI: the scoped grant
	// only, no {app_id}-broadcast grant (the pre-this-change shape).
	scopedTopicPattern := ntfycli.TopicPattern(appID, personHash)
	grantLegacyScopedGrantViaCLI(t, ntfyUserID, scopedTopicPattern)

	// Deprovision resets the (existing) scoped grant, then resets the
	// {app_id}-broadcast grant that was never created. Decision 3 requires that
	// second reset to be tolerated (ErrNotFound), so deprovision must return 200.
	status, body := postJSON(t, "/v1/deprovision", map[string]string{"app_id": appID, "email": email})
	if status != http.StatusOK {
		t.Fatalf("deprovision of legacy (broadcast-less) subscriber status = %d, body = %s "+
			"(Decision 3 ErrNotFound-tolerate assumption may not hold against real ntfy)", status, body)
	}
}

// grantBroadcastOnlyGrantViaCLI constructs a "broadcast-only remnant" subscriber
// — the shape a half-torn-down user would have (holds ONLY the shared
// {app_id}-broadcast grant, with NO scoped {app_id}-{personHash}-* grant) — by
// creating the ntfy user and granting ONLY the broadcast topic directly via the
// ntfy CLI, bypassing POST /v1/provision entirely. It mirrors
// grantLegacyScopedGrantViaCLI but targets ntfycli.BroadcastTopicPattern instead
// of the scoped per-person pattern, reusing the same ntfycli arg builders
// production uses (UserAddArgs/AccessGrantArgs) so the test cannot drift from the
// real CLI arg shapes, and passing the password out-of-band via NTFY_PASSWORD.
func grantBroadcastOnlyGrantViaCLI(t *testing.T, ntfyUserID string, broadcastTopic string) {
	t.Helper()
	const remnantPassword = "broadcast-remnant-password"
	runNtfyCLIInContainer(t, []string{"NTFY_PASSWORD=" + remnantPassword}, ntfycli.UserAddArgs(ntfyUserID)...)
	runNtfyCLIInContainer(t, nil, ntfycli.AccessGrantArgs(ntfyUserID, broadcastTopic, ntfycli.PermissionReadOnly)...)
}

// TestDeprovisionAppRemovesBroadcastOnlyRemnantEndToEnd empirically tests the
// CRITICAL review claim about Decision 4: DeprovisionApp selects subscribers via
// userHasAppGrant (scoped OR broadcast). When the cascade hits a broadcast-only
// remnant (holds ONLY {app_id}-broadcast, never held the scoped
// {app_id}-{personHash}-* topic), Deprovision's FIRST (scoped) ResetAccess call
// targets a topic the user was never granted. The reviewer claims real ntfy
// returns ErrNotFound there, aborting the whole cascade with a misleading 404
// (publisher never deleted, registry row never cleaned). The counter-hypothesis
// is that `ntfy access --reset <existing-user> <never-granted-topic>` exits 0
// (success no-op, because "does not exist" → ErrNotFound is only for a MISSING
// USER on a non-zero exit), so the reset is nil and the cascade completes.
//
// This test faithfully reproduces the remnant→DeprovisionApp cascade against the
// real ntfy CLI and asserts the cascade completes (200; publisher deleted;
// remnant deleted). A non-200 here CONFIRMS the CRITICAL.
func TestDeprovisionAppRemovesBroadcastOnlyRemnantEndToEnd(t *testing.T) {
	waitForHealth(t)
	const appID = "itestremnant"
	// The remnant's ntfy user id must be "u_"-shaped so Deprovision derives the
	// scoped pattern ({app_id}-{personHash}-*) exactly as it would in production.
	const remnantEmail = "itest-remnant-subscriber@example.com"
	remnantNtfyUserID := personhash.NtfyUser(remnantEmail)
	publisherNtfyUserID := appID + "-publisher"
	t.Cleanup(func() { deleteUser(t, publisherNtfyUserID) })
	t.Cleanup(func() { deleteUser(t, remnantNtfyUserID) })

	// Create the app publisher (holds the write-only {app_id}-* wildcard grant).
	status, provisionAppBody := postJSON(t, "/v1/provision-app", map[string]string{"app_id": appID})
	if status != http.StatusOK {
		t.Fatalf("provision-app status = %d, body = %s", status, provisionAppBody)
	}
	var provisionedApp provisionAppResponse
	if unmarshalErr := json.Unmarshal(provisionAppBody, &provisionedApp); unmarshalErr != nil {
		t.Fatalf("unmarshaling provision-app response: %v", unmarshalErr)
	}
	if provisionedApp.PublisherUserID != publisherNtfyUserID {
		t.Fatalf("publisher_user_id = %q, expected %q", provisionedApp.PublisherUserID, publisherNtfyUserID)
	}

	// Build the broadcast-only remnant directly via the ntfy CLI: ONLY the shared
	// {app_id}-broadcast grant, and NO scoped {app_id}-{personHash}-* grant. This
	// is the exact half-torn-down shape the CRITICAL is about.
	broadcastTopic := ntfycli.BroadcastTopicPattern(appID)
	grantBroadcastOnlyGrantViaCLI(t, remnantNtfyUserID, broadcastTopic)

	// Both users must be present before the cascade so the assertions below prove
	// the cascade actually removed them (not that they never existed).
	if !userIsListed(t, publisherNtfyUserID) {
		t.Fatalf("publisher %q not listed before deprovision-app", publisherNtfyUserID)
	}
	if !userIsListed(t, remnantNtfyUserID) {
		t.Fatalf("remnant %q not listed before deprovision-app", remnantNtfyUserID)
	}

	// Run the cascade. DeprovisionApp selects the remnant via userHasAppGrant
	// (broadcast match) and calls Deprovision, whose FIRST ResetAccess targets the
	// never-granted scoped topic {app_id}-{personHash}-*. If real ntfy returns
	// ErrNotFound there, this comes back 404 (CRITICAL confirmed); if it is a
	// success no-op, the cascade completes and returns 200 (CRITICAL refuted).
	status, deprovBody := postJSON(t, "/v1/deprovision-app", map[string]string{"app_id": appID})
	if status != http.StatusOK {
		t.Fatalf("deprovision-app of broadcast-only-remnant app status = %d, body = %s "+
			"(CRITICAL would be CONFIRMED: scoped ResetAccess on a never-granted topic "+
			"aborted the cascade)", status, deprovBody)
	}

	// The cascade must have run to completion: the publisher is deleted AND the
	// remnant (whose only grant, broadcast, was reset to zero patterns) is deleted.
	if userIsListed(t, publisherNtfyUserID) {
		t.Fatalf("publisher %q still listed after deprovision-app; cascade did not delete it", publisherNtfyUserID)
	}
	if userIsListed(t, remnantNtfyUserID) {
		t.Fatalf("broadcast-only remnant %q still listed after deprovision-app; cascade did not tear it down", remnantNtfyUserID)
	}
}

func TestConcurrentProvisionsAreSerializedUnderLoad(t *testing.T) {
	waitForHealth(t)
	const appID = "itestconc"
	const userCount = 8

	emails := make([]string, userCount)
	for userIndex := 0; userIndex < userCount; userIndex++ {
		emails[userIndex] = fmt.Sprintf("itest-conc-user-%d@example.com", userIndex)
	}

	var waitGroup sync.WaitGroup
	statuses := make([]int, userCount)
	for userIndex := 0; userIndex < userCount; userIndex++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			status, _ := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "email": emails[index]})
			statuses[index] = status
		}(userIndex)
	}
	waitGroup.Wait()

	for _, email := range emails {
		deleteUser(t, personhash.NtfyUser(email))
	}
	for userIndex, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent provision %d status = %d, expected 200", userIndex, status)
		}
	}
}

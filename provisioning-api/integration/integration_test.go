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
	"strings"
	"sync"
	"testing"
	"time"

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
	UserID       string `json:"user_id"`
	AppID        string `json:"app_id"`
	PersonHash   string `json:"person_hash"`
	TopicPattern string `json:"topic_pattern"`
	Token        string `json:"token"`
}

// provisionAppResponse mirrors the /v1/provision-app success body.
type provisionAppResponse struct {
	AppID           string `json:"app_id"`
	PublisherUserID string `json:"publisher_user_id"`
	TopicPattern    string `json:"topic_pattern"`
	Token           string `json:"token"`
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

	status, body := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": "app-side-id", "email": email})
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

	_, firstBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": "app-side-id", "email": email})
	var first provisionResponse
	if err := json.Unmarshal(firstBody, &first); err != nil {
		t.Fatalf("unmarshaling first provision: %v", err)
	}

	_, secondBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": "app-side-id", "email": email})
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

	_, provision1Body := postJSON(t, "/v1/provision", map[string]string{"app_id": app1, "user_id": "app-side-id", "email": email})
	var provisioned1 provisionResponse
	if err := json.Unmarshal(provision1Body, &provisioned1); err != nil {
		t.Fatalf("unmarshaling app1 provision: %v", err)
	}
	status, provision2Body := postJSON(t, "/v1/provision", map[string]string{"app_id": app2, "user_id": "app-side-id", "email": email})
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

func TestDeleteUnknownUserReturns404(t *testing.T) {
	waitForHealth(t)
	request, buildErr := http.NewRequest(http.MethodDelete, apiBaseURL()+"/v1/users/u_00000000000000ge", nil)
	if buildErr != nil {
		t.Fatalf("building request: %v", buildErr)
	}
	response, deleteErr := http.DefaultClient.Do(request)
	if deleteErr != nil {
		t.Fatalf("delete request: %v", deleteErr)
	}
	defer closeBody(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown user status = %d, expected 404", response.StatusCode)
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

	status, provisionUserBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": "app-side-id", "email": email})
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
			status, _ := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": "app-side-id", "email": emails[index]})
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

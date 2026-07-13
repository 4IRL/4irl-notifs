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
	TopicPattern string `json:"topic_pattern"`
	Token        string `json:"token"`
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

func TestProvisionGrantsScopedPublishAccess(t *testing.T) {
	waitForHealth(t)
	const userID = "itest_scope_user"
	const appID = "itestscope"
	t.Cleanup(func() { deleteUser(t, userID) })

	status, body := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": userID})
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
	if provisioned.TopicPattern != appID+"-*" {
		t.Fatalf("topic_pattern = %q, expected %q", provisioned.TopicPattern, appID+"-*")
	}

	if inNamespace := publishStatus(t, appID+"-alerts", provisioned.Token); inNamespace != http.StatusOK {
		t.Fatalf("in-namespace publish status = %d, expected 200", inNamespace)
	}
	if crossNamespace := publishStatus(t, "otherapp-alerts", provisioned.Token); crossNamespace != http.StatusForbidden {
		t.Fatalf("cross-namespace publish status = %d, expected 403", crossNamespace)
	}
}

func TestReprovisionRotatesToken(t *testing.T) {
	waitForHealth(t)
	const userID = "itest_rotate_user"
	const appID = "itestrotate"
	t.Cleanup(func() { deleteUser(t, userID) })

	_, firstBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": userID})
	var first provisionResponse
	if err := json.Unmarshal(firstBody, &first); err != nil {
		t.Fatalf("unmarshaling first provision: %v", err)
	}

	_, secondBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": userID})
	var second provisionResponse
	if err := json.Unmarshal(secondBody, &second); err != nil {
		t.Fatalf("unmarshaling second provision: %v", err)
	}

	if first.Token == second.Token {
		t.Fatal("re-provision returned the same token; expected rotation")
	}
	if oldToken := publishStatus(t, appID+"-alerts", first.Token); oldToken == http.StatusOK {
		t.Fatalf("old token still authorized (status %d); expected it revoked", oldToken)
	}
	if newToken := publishStatus(t, appID+"-alerts", second.Token); newToken != http.StatusOK {
		t.Fatalf("new token status = %d, expected 200", newToken)
	}
}

func TestDeprovisionRevokesAccessButKeepsUser(t *testing.T) {
	waitForHealth(t)
	const userID = "itest_deprov_user"
	const appID = "itestdeprov"
	t.Cleanup(func() { deleteUser(t, userID) })

	_, provisionBody := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": userID})
	var provisioned provisionResponse
	if err := json.Unmarshal(provisionBody, &provisioned); err != nil {
		t.Fatalf("unmarshaling provision: %v", err)
	}

	status, deprovBody := postJSON(t, "/v1/deprovision", map[string]string{"app_id": appID, "user_id": userID})
	if status != http.StatusOK {
		t.Fatalf("deprovision status = %d, body = %s", status, deprovBody)
	}

	if revoked := publishStatus(t, appID+"-alerts", provisioned.Token); revoked == http.StatusOK {
		t.Fatalf("token still authorized after deprovision (status %d)", revoked)
	}

	response, getErr := http.Get(apiBaseURL() + "/v1/users")
	if getErr != nil {
		t.Fatalf("GET /v1/users: %v", getErr)
	}
	defer closeBody(t, response)
	var users userListResponse
	if err := json.Unmarshal(readBody(t, response), &users); err != nil {
		t.Fatalf("unmarshaling users: %v", err)
	}
	found := false
	for _, user := range users.Users {
		if user.UserID == userID {
			found = true
		}
	}
	if !found {
		t.Fatal("user was removed by deprovision; expected it retained")
	}
}

func TestDeleteUnknownUserReturns404(t *testing.T) {
	waitForHealth(t)
	request, buildErr := http.NewRequest(http.MethodDelete, apiBaseURL()+"/v1/users/itest_ghost_user", nil)
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

func TestConcurrentProvisionsAreSerializedUnderLoad(t *testing.T) {
	waitForHealth(t)
	const appID = "itestconc"
	const userCount = 8

	var waitGroup sync.WaitGroup
	statuses := make([]int, userCount)
	for userIndex := 0; userIndex < userCount; userIndex++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			userID := fmt.Sprintf("itest_conc_user_%d", index)
			status, _ := postJSON(t, "/v1/provision", map[string]string{"app_id": appID, "user_id": userID})
			statuses[index] = status
		}(userIndex)
	}
	waitGroup.Wait()

	for userIndex := 0; userIndex < userCount; userIndex++ {
		deleteUser(t, fmt.Sprintf("itest_conc_user_%d", userIndex))
	}
	for userIndex, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("concurrent provision %d status = %d, expected 200", userIndex, status)
		}
	}
}

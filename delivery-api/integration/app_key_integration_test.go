//go:build integration

package integration

// The app-facing endpoints (/v1/messages and the heartbeat pings) require the calling app's
// delivery key, so every integration test that submits, reads, cancels or pings needs one. This
// file is the harness that obtains it exactly as an app's deploy would on the dev-mode stack: sign
// a GitHub-shaped OIDC token with the throwaway dev key, exchange it (the first exchange files a
// pending request), approve the request through the operator API, and exchange again for the key.
//
// apiRequest uses it automatically: a request to an app-facing path is authenticated as the app
// the request itself names (its ?app_id= query, its heartbeat path segment or its body's app_id),
// so each test keeps reading as the app-facing call it is testing. Tests that must control the
// Authorization header (none, malformed, revoked, another app's) call apiRequestWithAuthorization.

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const (
	// appKeyJTIPrefix marks every replay row the harness writes, so cleanup deletes only its own.
	appKeyJTIPrefix = "it-appkey-jti-"
	// appKeyRepositoryIDSpace keeps a derived GitHub repository id a small positive integer.
	appKeyRepositoryIDSpace = 1_000_000_000_000
)

var (
	// appKeyAppPattern is the app-id rule; an id that fails it cannot be bound, and also makes an
	// id safe to interpolate into the cleanup SQL below.
	appKeyAppPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

	// appKeys caches the key issued to each app for the life of the test that asked for it.
	appKeys = struct {
		sync.Mutex
		byApp map[string]string
	}{byApp: map[string]string{}}
)

// isAppFacingPath reports whether path is an endpoint that requires an app key.
func isAppFacingPath(path string) bool {
	return path == messagesPath || strings.HasPrefix(path, messagesPath+"/") ||
		strings.HasPrefix(path, messagesPath+"?") || strings.HasPrefix(path, heartbeatsPingPath+"/")
}

// assertedAppID returns the app the request names: its ?app_id= query, its heartbeat path
// segment or its body's app_id. A request that names none, or an id no app can have, is
// authenticated as the default message app, so it falls through to the endpoint's own validation.
func assertedAppID(path string, body any) string {
	if parsed, parseErr := url.Parse(path); parseErr == nil {
		if queried := parsed.Query().Get("app_id"); appKeyAppPattern.MatchString(queried) {
			return queried
		}
		if segments := strings.Split(strings.Trim(parsed.Path, "/"), "/"); len(segments) >= 3 &&
			segments[1] == "heartbeats" && appKeyAppPattern.MatchString(segments[2]) {
			return segments[2]
		}
	}
	if fields, isMap := body.(map[string]any); isMap {
		if named, isString := fields["app_id"].(string); isString && appKeyAppPattern.MatchString(named) {
			return named
		}
	}
	return messageAppID
}

// appKeyFor returns a valid delivery key for appID, binding the app first when it has no binding.
// The key lives for the test that asked: its cleanup unbinds the app (cascading its keys) and
// forgets the cached key. The app's workflow identity is derived from its id, so a binding left
// behind by an interrupted run is simply reused.
func appKeyFor(testInstance *testing.T, appID string) string {
	testInstance.Helper()
	appKeys.Lock()
	defer appKeys.Unlock()
	if key, cached := appKeys.byApp[appID]; cached {
		return key
	}
	if !appKeyAppPattern.MatchString(appID) {
		testInstance.Fatalf("cannot bind an app id of the wrong shape: %q", appID)
	}
	key := bindAndIssueKey(testInstance, appID).Key
	appKeys.byApp[appID] = key
	testInstance.Cleanup(func() {
		appKeys.Lock()
		defer appKeys.Unlock()
		delete(appKeys.byApp, appID)
		cleanupAppKeyRows(testInstance, appID)
	})
	return key
}

// bindAndIssueKey runs the exchange, approval and second exchange for appID and returns the issued
// key (the plaintext and its id).
func bindAndIssueKey(testInstance *testing.T, appID string) exchangeResponseJSON {
	testInstance.Helper()
	signer := loadDevOIDCSigner(testInstance)
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(appID))
	repository := "4IRL/" + strings.ReplaceAll(appID, "_", "-")
	repositoryID := fmt.Sprintf("%d", hasher.Sum64()%appKeyRepositoryIDSpace+1)
	exchange := func() (int, []byte) {
		token := signer.sign(testInstance, workflowClaims(repository, repositoryID, identityWorkflow, appKeyJTIPrefix+randomSuffix(testInstance)))
		return exchangeToken(testInstance, appID, token)
	}

	status, body := exchange()
	if status == http.StatusAccepted {
		var pending exchangeResponseJSON
		decodeBody(testInstance, "app key exchange", body, &pending)
		approveIdentityRequest(testInstance, pending.RequestID)
		status, body = exchange()
	}
	requireStatus(testInstance, "app key exchange for "+appID, status, http.StatusOK, body)
	var issued exchangeResponseJSON
	decodeBody(testInstance, "app key exchange", body, &issued)
	if !identityKeyPattern.MatchString(issued.Key) {
		testInstance.Fatalf("exchange for %s returned a key of the wrong shape (status issued: %q)", appID, issued.Status)
	}
	return issued
}

// cleanupAppKeyRows unbinds appID and removes the rows the harness wrote for it.
func cleanupAppKeyRows(testInstance *testing.T, appID string) {
	testInstance.Helper()
	if !appKeyAppPattern.MatchString(appID) {
		testInstance.Errorf("cleanup: refusing to clean up an app id of the wrong shape: %q", appID)
		return
	}
	status, body := apiRequestWithAuthorization(testInstance, "", http.MethodDelete,
		identityAdminPath+"/bindings/"+appID, nil)
	if status != http.StatusOK {
		testInstance.Errorf("cleanup: unbind %s status = %d, body: %s", appID, status, body)
	}
	psql(testInstance, `DELETE FROM app_registration_requests WHERE app_id = '`+appID+`'`)
	psql(testInstance, `DELETE FROM oidc_replay WHERE jti LIKE '`+appKeyJTIPrefix+`%'`)
}

// bearer returns the Authorization header value for key.
func bearer(key string) string {
	return "Bearer " + key
}

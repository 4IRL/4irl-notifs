//go:build integration

package integration

// End-to-end tests of the app identity exchange and the operator identity API against the live
// compose stack: the real binary in dev OIDC mode, trusting the throwaway key set
// scripts/dev-oidc-keys.mjs wrote, with tokens signed by devOIDCSigner. A failure like "invalid
// identity token" on a token that should verify usually means .local/dev-oidc/ was regenerated
// after the container started (it mounted a stale JWKS): rerun make local-up.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	identityExchangePath = "/v1/app-identity/exchange"
	identityAdminPath    = "/v1/admin/app-identity"
	invalidTokenBody     = "invalid identity token"
	identityWorkflow     = ".github/workflows/prod-deploy.yml"
	// identityAppPrefix and identityJTIPrefix mark every row these tests create, so cleanup deletes
	// only their own.
	identityAppPrefix = "itid_"
	identityJTIPrefix = "it-jti-"
	// pendingCap mirrors appidentity.MaxPendingRequests; the test asserts the wire behaviour, so the
	// number is stated here rather than imported.
	pendingCap = 20
)

var (
	identityKeyPattern     = regexp.MustCompile(`^dak_[A-Za-z0-9_-]{43}$`)
	identitySafeAppPattern = regexp.MustCompile(`^itid_[0-9a-f]{16}$`)
)

type identityRequestJSON struct {
	ID               int64   `json:"id"`
	AppID            string  `json:"app_id"`
	GitHubRepository string  `json:"github_repository"`
	RepositoryID     int64   `json:"github_repository_id"`
	WorkflowPath     string  `json:"workflow_path"`
	GitRef           string  `json:"git_ref"`
	Environment      *string `json:"environment"`
	GitHubRunID      string  `json:"github_run_id"`
	Status           string  `json:"status"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type identityKeyJSON struct {
	ID            int64   `json:"id"`
	AppID         string  `json:"app_id"`
	KeyHint       string  `json:"key_hint"`
	State         string  `json:"state"`
	CreatedAt     string  `json:"created_at"`
	SupersededAt  *string `json:"superseded_at"`
	RevokedAt     *string `json:"revoked_at"`
	RevokedReason *string `json:"revoked_reason"`
	LastUsedAt    *string `json:"last_used_at"`
}

type identityBindingJSON struct {
	AppID        string            `json:"app_id"`
	Repository   string            `json:"github_repository"`
	RepositoryID int64             `json:"github_repository_id"`
	OwnerID      int64             `json:"github_owner_id"`
	WorkflowPath string            `json:"workflow_path"`
	GitRef       string            `json:"git_ref"`
	Environment  *string           `json:"environment"`
	ApprovedAt   string            `json:"approved_at"`
	Keys         []identityKeyJSON `json:"keys"`
}

type exchangeResponseJSON struct {
	Status    string `json:"status"`
	RequestID int64  `json:"request_id"`
	AppID     string `json:"app_id"`
	Key       string `json:"key"`
	KeyID     int64  `json:"key_id"`
}

// identityApp is one throwaway app identity a test drives: a unique app id, GitHub repository and
// repository id, so no test can collide with another's bindings or workflow identity.
type identityApp struct {
	appID        string
	repository   string
	repositoryID string
	signer       devOIDCSigner
}

// newIdentityApp registers cleanup first, so a test that fails halfway still removes its rows.
func newIdentityApp(testInstance *testing.T) identityApp {
	testInstance.Helper()
	waitForReady(testInstance)
	appID := identityAppPrefix + randomSuffix(testInstance)
	if !identitySafeAppPattern.MatchString(appID) {
		testInstance.Fatalf("generated app id %q is not the safe test shape", appID)
	}
	var idBytes [8]byte
	if _, readErr := rand.Read(idBytes[:]); readErr != nil {
		testInstance.Fatalf("reading random bytes: %v", readErr)
	}
	// 1e12 keeps the id a small positive integer, unique across runs for all practical purposes.
	repositoryID := binary.BigEndian.Uint64(idBytes[:])%1_000_000_000_000 + 1
	app := identityApp{
		appID:        appID,
		repository:   "4IRL/" + strings.ReplaceAll(appID, "_", "-"),
		repositoryID: fmt.Sprintf("%d", repositoryID),
		signer:       loadDevOIDCSigner(testInstance),
	}
	testInstance.Cleanup(func() { cleanupIdentityRows(testInstance, appID) })
	return app
}

// cleanupIdentityRows deletes everything one app created. appID is checked against the generated
// shape before it is interpolated into SQL. It also deletes every replay row this suite writes
// (the jti prefix, not one app's), which is safe only because the integration packages run
// serially (go test -p 1) and no test here uses t.Parallel.
func cleanupIdentityRows(testInstance *testing.T, appID string) {
	if !identitySafeAppPattern.MatchString(appID) {
		testInstance.Errorf("refusing to clean up an app id of the wrong shape: %q", appID)
		return
	}
	psql(testInstance, `DELETE FROM app_bindings WHERE app_id = '`+appID+`'`)
	psql(testInstance, `DELETE FROM app_registration_requests WHERE app_id = '`+appID+`'`)
	psql(testInstance, `DELETE FROM oidc_replay WHERE jti LIKE '`+identityJTIPrefix+`%'`)
}

// claims returns valid claims for the app's own workflow, with a jti unique to the call.
func (app identityApp) claims(testInstance *testing.T) jwt.MapClaims {
	testInstance.Helper()
	return workflowClaims(app.repository, app.repositoryID, identityWorkflow, identityJTIPrefix+randomSuffix(testInstance))
}

// token signs the app's valid claims, after mutate adjusts them.
func (app identityApp) token(testInstance *testing.T, mutate func(jwt.MapClaims)) string {
	testInstance.Helper()
	claims := app.claims(testInstance)
	if mutate != nil {
		mutate(claims)
	}
	return app.signer.sign(testInstance, claims)
}

// exchange posts a freshly signed token for the app and returns the status and body.
func (app identityApp) exchange(testInstance *testing.T, mutate func(jwt.MapClaims)) (int, []byte) {
	testInstance.Helper()
	return exchangeToken(testInstance, app.appID, app.token(testInstance, mutate))
}

func exchangeToken(testInstance *testing.T, appID string, token string) (int, []byte) {
	testInstance.Helper()
	return apiRequest(testInstance, "POST", identityExchangePath, map[string]any{"app_id": appID, "token": token})
}

// exchangeExpecting posts a fresh token and requires status, returning the decoded body.
func (app identityApp) exchangeExpecting(testInstance *testing.T, wantStatus int, mutate func(jwt.MapClaims)) exchangeResponseJSON {
	testInstance.Helper()
	status, body := app.exchange(testInstance, mutate)
	requireStatus(testInstance, "exchange", status, wantStatus, body)
	var decoded exchangeResponseJSON
	decodeBody(testInstance, "exchange", body, &decoded)
	return decoded
}

func (app identityApp) pendingRequestCount(testInstance *testing.T) string {
	testInstance.Helper()
	return psql(testInstance, `SELECT count(*) FROM app_registration_requests WHERE status = 'pending' AND app_id = '`+app.appID+`'`)
}

func listIdentityRequests(testInstance *testing.T) []identityRequestJSON {
	testInstance.Helper()
	status, body := apiRequest(testInstance, "GET", identityAdminPath+"/requests", nil)
	requireStatus(testInstance, "list requests", status, 200, body)
	var decoded struct {
		Requests []identityRequestJSON `json:"requests"`
	}
	decodeBody(testInstance, "list requests", body, &decoded)
	return decoded.Requests
}

// requestsOf returns the listed requests that belong to appID, in list order.
func requestsOf(requests []identityRequestJSON, appID string) []identityRequestJSON {
	var owned []identityRequestJSON
	for _, listed := range requests {
		if listed.AppID == appID {
			owned = append(owned, listed)
		}
	}
	return owned
}

func listIdentityBindings(testInstance *testing.T) ([]identityBindingJSON, []byte) {
	testInstance.Helper()
	status, body := apiRequest(testInstance, "GET", identityAdminPath+"/bindings", nil)
	requireStatus(testInstance, "list bindings", status, 200, body)
	var decoded struct {
		Bindings []identityBindingJSON `json:"bindings"`
	}
	decodeBody(testInstance, "list bindings", body, &decoded)
	return decoded.Bindings, body
}

// bindingOf returns appID's binding, or nil when it is not bound.
func bindingOf(bindings []identityBindingJSON, appID string) *identityBindingJSON {
	for index := range bindings {
		if bindings[index].AppID == appID {
			return &bindings[index]
		}
	}
	return nil
}

func approveIdentityRequest(testInstance *testing.T, requestID int64) {
	testInstance.Helper()
	status, body := apiRequest(testInstance, "POST", fmt.Sprintf("%s/requests/%d/approve", identityAdminPath, requestID), nil)
	requireStatus(testInstance, "approve", status, 201, body)
}

// bind drives a fresh app through pending and approval, leaving it bound.
func (app identityApp) bind(testInstance *testing.T) {
	testInstance.Helper()
	pending := app.exchangeExpecting(testInstance, 202, nil)
	approveIdentityRequest(testInstance, pending.RequestID)
}

func requireInvalidToken(testInstance *testing.T, label string, status int, body []byte) {
	testInstance.Helper()
	requireStatus(testInstance, label, status, 401, body)
	requireErrorMessage(testInstance, label, body, invalidTokenBody)
}

func TestExchangeLifecycleFromPendingToIssuedSupersedeRevokeAndUnbind(testInstance *testing.T) {
	app := newIdentityApp(testInstance)

	// An unbound app gets 202 and no key; the same claims again refresh the one pending row.
	first := app.exchangeExpecting(testInstance, 202, nil)
	if first.Status != "pending" || first.RequestID == 0 || first.Key != "" {
		testInstance.Fatalf("first exchange = %+v, want a pending request and no key", first)
	}
	again := app.exchangeExpecting(testInstance, 202, nil)
	if again.RequestID != first.RequestID {
		testInstance.Errorf("the same claims filed request %d, then %d: want one pending row", first.RequestID, again.RequestID)
	}
	if count := app.pendingRequestCount(testInstance); count != "1" {
		testInstance.Fatalf("pending rows = %s, want exactly 1 per distinct claim set", count)
	}
	if keys := psql(testInstance, `SELECT count(*) FROM app_keys WHERE app_id = '`+app.appID+`'`); keys != "0" {
		testInstance.Fatalf("keys for an unbound app = %s, want none", keys)
	}

	// The operator sees the GitHub-verified claims.
	listed := requestsOf(listIdentityRequests(testInstance), app.appID)
	if len(listed) != 1 {
		testInstance.Fatalf("listed requests = %+v, want one", listed)
	}
	if request := listed[0]; request.ID != first.RequestID || request.Status != "pending" || request.GitHubRepository != app.repository ||
		fmt.Sprint(request.RepositoryID) != app.repositoryID || request.WorkflowPath != identityWorkflow ||
		request.GitRef != "refs/heads/main" || request.Environment != nil || request.GitHubRunID != "1" {
		testInstance.Errorf("listed request = %+v", request)
	}

	// Approval binds the app, consumes the request, and the next exchange is issued a key.
	approveIdentityRequest(testInstance, first.RequestID)
	if remaining := requestsOf(listIdentityRequests(testInstance), app.appID); len(remaining) != 0 {
		testInstance.Errorf("requests after approval = %+v, want none", remaining)
	}
	issuedFirst := app.exchangeExpecting(testInstance, 200, nil)
	if issuedFirst.Status != "issued" || issuedFirst.AppID != app.appID || issuedFirst.KeyID == 0 || !identityKeyPattern.MatchString(issuedFirst.Key) {
		testInstance.Fatalf("first issue = %+v", issuedFirst)
	}

	// A second issue supersedes the first at once; both are listed, newest first.
	issuedSecond := app.exchangeExpecting(testInstance, 200, nil)
	if issuedSecond.Key == issuedFirst.Key || issuedSecond.KeyID == issuedFirst.KeyID {
		testInstance.Fatalf("second issue = %+v, want a distinct key", issuedSecond)
	}
	bindings, bindingsBody := listIdentityBindings(testInstance)
	binding := bindingOf(bindings, app.appID)
	if binding == nil || binding.Repository != app.repository || binding.WorkflowPath != identityWorkflow ||
		binding.GitRef != "refs/heads/main" || binding.Environment != nil || binding.OwnerID != 4242 {
		testInstance.Fatalf("binding = %+v", binding)
	}
	if len(binding.Keys) != 2 || binding.Keys[0].ID != issuedSecond.KeyID || binding.Keys[1].ID != issuedFirst.KeyID {
		testInstance.Fatalf("keys = %+v, want [second, first]", binding.Keys)
	}
	if binding.Keys[0].State != "active" || binding.Keys[1].State != "superseded" || binding.Keys[1].SupersededAt == nil {
		testInstance.Errorf("key states = %s / %s, want active / superseded", binding.Keys[0].State, binding.Keys[1].State)
	}
	if binding.Keys[0].KeyHint != issuedSecond.Key[:8] {
		testInstance.Errorf("key hint = %q, want the key's first 8 characters", binding.Keys[0].KeyHint)
	}
	// Neither the list nor the database ever holds a plaintext key; the database holds its hash.
	for _, key := range []string{issuedFirst.Key, issuedSecond.Key} {
		if strings.Contains(string(bindingsBody), key) {
			testInstance.Errorf("the bindings list leaks a plaintext key")
		}
	}
	wantHash := sha256.Sum256([]byte(issuedSecond.Key))
	storedHash := psql(testInstance, fmt.Sprintf(`SELECT encode(key_hash, 'hex') FROM app_keys WHERE id = %d`, issuedSecond.KeyID))
	if storedHash != hex.EncodeToString(wantHash[:]) {
		testInstance.Errorf("stored key hash = %s, want sha256 of the key %x", storedHash, wantHash)
	}

	// Past its 24 h grace window the superseded key reads as revoked with reason "superseded".
	psql(testInstance, fmt.Sprintf(`UPDATE app_keys SET superseded_at = now() - interval '25 hours' WHERE id = %d`, issuedFirst.KeyID))
	bindings, _ = listIdentityBindings(testInstance)
	pastGrace := bindingOf(bindings, app.appID).Keys[1]
	if pastGrace.State != "revoked" || pastGrace.RevokedReason == nil || *pastGrace.RevokedReason != "superseded" {
		testInstance.Errorf("a key past its grace window = %+v, want revoked / superseded", pastGrace)
	}

	// Revoking is immediate and idempotent.
	revokePath := fmt.Sprintf("%s/keys/%d/revoke", identityAdminPath, issuedSecond.KeyID)
	var revoked struct {
		Key identityKeyJSON `json:"key"`
	}
	status, body := apiRequest(testInstance, "POST", revokePath, nil)
	requireStatus(testInstance, "revoke", status, 200, body)
	decodeBody(testInstance, "revoke", body, &revoked)
	if revoked.Key.State != "revoked" || revoked.Key.RevokedReason == nil || *revoked.Key.RevokedReason != "operator" || revoked.Key.RevokedAt == nil {
		testInstance.Fatalf("revoked key = %+v", revoked.Key)
	}
	var revokedAgain struct {
		Key identityKeyJSON `json:"key"`
	}
	status, body = apiRequest(testInstance, "POST", revokePath, nil)
	requireStatus(testInstance, "revoke again", status, 200, body)
	decodeBody(testInstance, "revoke again", body, &revokedAgain)
	if revokedAgain.Key.RevokedAt == nil || *revokedAgain.Key.RevokedAt != *revoked.Key.RevokedAt {
		testInstance.Errorf("a second revoke moved the revoke time: %v then %v", revoked.Key.RevokedAt, revokedAgain.Key.RevokedAt)
	}
	status, body = apiRequest(testInstance, "POST", identityAdminPath+"/keys/999999999/revoke", nil)
	requireStatus(testInstance, "revoke unknown key", status, 404, body)

	// Unbinding removes the binding and every key, idempotently; the app is unbound again.
	unbindPath := identityAdminPath + "/bindings/" + app.appID
	status, body = apiRequest(testInstance, "DELETE", unbindPath, nil)
	requireStatus(testInstance, "unbind", status, 200, body)
	if want := fmt.Sprintf(`{"app_id":%q,"deleted":true}`, app.appID); strings.TrimSpace(string(body)) != want {
		testInstance.Errorf("unbind body = %s, want %s", body, want)
	}
	if keys := psql(testInstance, `SELECT count(*) FROM app_keys WHERE app_id = '`+app.appID+`'`); keys != "0" {
		testInstance.Errorf("keys after unbind = %s, want 0", keys)
	}
	if bindings, _ = listIdentityBindings(testInstance); bindingOf(bindings, app.appID) != nil {
		testInstance.Error("the binding is still listed after unbind")
	}
	status, body = apiRequest(testInstance, "DELETE", unbindPath, nil)
	requireStatus(testInstance, "unbind again", status, 200, body)
	if want := fmt.Sprintf(`{"app_id":%q,"deleted":false}`, app.appID); strings.TrimSpace(string(body)) != want {
		testInstance.Errorf("second unbind body = %s, want %s", body, want)
	}
	if afterUnbind := app.exchangeExpecting(testInstance, 202, nil); afterUnbind.Key != "" {
		testInstance.Errorf("an unbound app received a key: %+v", afterUnbind)
	}
}

func TestExchangeWithAnyDifferingBoundFieldIs403AndFilesNothing(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	app.bind(testInstance)

	for _, testCase := range []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{"repository id", func(claims jwt.MapClaims) { claims["repository_id"] = "9" + app.repositoryID }},
		{"owner id", func(claims jwt.MapClaims) { claims["repository_owner_id"] = "999" }},
		{"workflow path", func(claims jwt.MapClaims) {
			claims["workflow_ref"] = app.repository + "/.github/workflows/other.yml@refs/heads/main"
		}},
		{"ref", func(claims jwt.MapClaims) { claims["ref"] = "refs/heads/feature" }},
		{"environment", func(claims jwt.MapClaims) { claims["environment"] = "production" }},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			jti := identityJTIPrefix + randomSuffix(subTest)
			token := app.token(subTest, func(claims jwt.MapClaims) {
				testCase.mutate(claims)
				claims["jti"] = jti
			})
			status, body := exchangeToken(subTest, app.appID, token)

			requireStatus(subTest, "mismatched exchange", status, 403, body)
			requireErrorMessage(subTest, "mismatched exchange", body, "identity does not match the approved binding")
			if strings.Contains(string(body), "dak_") {
				subTest.Errorf("a refused exchange returned a key: %s", body)
			}
			// A refusal rolls the whole transaction back, so the token is not spent: the same token
			// is refused for the same reason again (a 401 here would mean its jti was consumed).
			statusAgain, bodyAgain := exchangeToken(subTest, app.appID, token)
			requireStatus(subTest, "mismatched exchange again", statusAgain, 403, bodyAgain)
			if recorded := psql(subTest, `SELECT count(*) FROM oidc_replay WHERE jti = '`+jti+`'`); recorded != "0" {
				subTest.Errorf("oidc_replay rows for the refused token = %s, want 0", recorded)
			}
			if requests := psql(subTest, `SELECT count(*) FROM app_registration_requests WHERE app_id = '`+app.appID+`'`); requests != "0" {
				subTest.Errorf("requests = %s: a mismatched exchange must file nothing (an app id cannot be taken over through the queue)", requests)
			}
		})
	}
	if keys := psql(testInstance, `SELECT count(*) FROM app_keys WHERE app_id = '`+app.appID+`'`); keys != "0" {
		testInstance.Errorf("keys = %s after only refused exchanges, want 0", keys)
	}
}

func TestExchangeOfAnApprovedEnvironmentBindingNeedsThatEnvironment(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	withEnvironment := func(claims jwt.MapClaims) { claims["environment"] = "production" }
	pending := app.exchangeExpecting(testInstance, 202, withEnvironment)
	approveIdentityRequest(testInstance, pending.RequestID)

	if issued := app.exchangeExpecting(testInstance, 200, withEnvironment); issued.Key == "" {
		testInstance.Error("the matching environment must be issued a key")
	}
	status, body := app.exchange(testInstance, nil)
	requireStatus(testInstance, "exchange with no environment", status, 403, body)
}

func TestExchangeOfAReplayedTokenIs401(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	token := app.token(testInstance, nil)

	status, body := exchangeToken(testInstance, app.appID, token)
	requireStatus(testInstance, "first use", status, 202, body)
	status, body = exchangeToken(testInstance, app.appID, token)
	requireInvalidToken(testInstance, "replayed pending token", status, body)

	approveIdentityRequest(testInstance, app.exchangeExpecting(testInstance, 202, nil).RequestID)
	issuedToken := app.token(testInstance, nil)
	status, body = exchangeToken(testInstance, app.appID, issuedToken)
	requireStatus(testInstance, "first issue", status, 200, body)
	status, body = exchangeToken(testInstance, app.appID, issuedToken)
	requireInvalidToken(testInstance, "replayed issued token", status, body)
	if keys := psql(testInstance, `SELECT count(*) FROM app_keys WHERE app_id = '`+app.appID+`'`); keys != "1" {
		testInstance.Errorf("keys = %s, want 1: a replay must not issue a second key", keys)
	}
}

func TestConcurrentExchangesOfOneTokenIssueExactlyOneKey(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	app.bind(testInstance)
	token := app.token(testInstance, nil)

	const racers = 6
	statuses := make([]int, racers)
	bodies := make([][]byte, racers)
	var group sync.WaitGroup
	for index := range racers {
		group.Add(1)
		go func() {
			defer group.Done()
			statuses[index], bodies[index] = exchangeTokenQuietly(app.appID, token)
		}()
	}
	group.Wait()

	winners, replays := 0, 0
	for index, status := range statuses {
		switch status {
		case 200:
			winners++
		case 401:
			replays++
		default:
			testInstance.Errorf("racer %d status = %d, body %s", index, status, bodies[index])
		}
	}
	if winners != 1 || replays != racers-1 {
		testInstance.Errorf("winners = %d, replays = %d, want 1 and %d: the jti must be consumed in the issuing transaction", winners, replays, racers-1)
	}
	if keys := psql(testInstance, `SELECT count(*) FROM app_keys WHERE app_id = '`+app.appID+`'`); keys != "1" {
		testInstance.Errorf("keys = %s, want exactly 1", keys)
	}
}

func TestConcurrentIssuesLeaveExactlyOneActiveKey(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	app.bind(testInstance)

	const racers = 5
	tokens := make([]string, racers)
	for index := range tokens {
		tokens[index] = app.token(testInstance, nil)
	}
	statuses := make([]int, racers)
	bodies := make([][]byte, racers)
	var group sync.WaitGroup
	for index := range racers {
		group.Add(1)
		go func() {
			defer group.Done()
			statuses[index], bodies[index] = exchangeTokenQuietly(app.appID, tokens[index])
		}()
	}
	group.Wait()

	for index, status := range statuses {
		if status != 200 {
			testInstance.Errorf("racer %d status = %d, body %s", index, status, bodies[index])
		}
	}
	if active := psql(testInstance, `SELECT count(*) FROM app_keys WHERE app_id = '`+app.appID+`' AND superseded_at IS NULL`); active != "1" {
		testInstance.Errorf("active keys = %s, want exactly 1: supersede-then-insert must serialise per app", active)
	}
	if keys := psql(testInstance, `SELECT count(*) FROM app_keys WHERE app_id = '`+app.appID+`'`); keys != fmt.Sprint(racers) {
		testInstance.Errorf("keys = %s, want %d", keys, racers)
	}
}

// exchangeTokenQuietly is exchangeToken for goroutines: it must not call testing.T.FailNow
// off the test goroutine, so a transport failure is reported as status 0 and the error text.
func exchangeTokenQuietly(appID string, token string) (int, []byte) {
	encoded, marshalErr := json.Marshal(map[string]any{"app_id": appID, "token": token})
	if marshalErr != nil {
		return 0, []byte(marshalErr.Error())
	}
	request, requestErr := http.NewRequestWithContext(context.Background(), http.MethodPost,
		deliveryURL()+identityExchangePath, bytes.NewReader(encoded))
	if requestErr != nil {
		return 0, []byte(requestErr.Error())
	}
	request.Header.Set("Content-Type", "application/json")
	response, doErr := adminClient.Do(request)
	if doErr != nil {
		return 0, []byte(doErr.Error())
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return 0, []byte(readErr.Error())
	}
	return response.StatusCode, body
}

func TestExchangeTokenFailuresAre401AndRecordNothing(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	attacker, keyErr := rsa.GenerateKey(rand.Reader, 2048)
	if keyErr != nil {
		testInstance.Fatalf("generate an attacker key: %v", keyErr)
	}
	now := time.Now()
	hmacToken := func(claims jwt.MapClaims) string {
		raw, signErr := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("shared-secret"))
		if signErr != nil {
			testInstance.Fatalf("sign HS256: %v", signErr)
		}
		return raw
	}
	// Every claim-based token carries a jti the test chose, so it can prove the jti stays unspent.
	withClaims := func(jti string, mutate func(jwt.MapClaims)) string {
		return app.token(testInstance, func(claims jwt.MapClaims) {
			claims["jti"] = jti
			mutate(claims)
		})
	}
	keepClaims := func(jti string) jwt.MapClaims {
		claims := app.claims(testInstance)
		claims["jti"] = jti
		return claims
	}

	for _, testCase := range []struct {
		name  string
		token func(jti string) string
	}{
		{"bad signature", func(jti string) string {
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, keepClaims(jti))
			token.Header["kid"] = app.signer.keyID
			raw, signErr := token.SignedString(attacker)
			if signErr != nil {
				testInstance.Fatalf("sign with the attacker key: %v", signErr)
			}
			return raw
		}},
		{"wrong issuer", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) { claims["iss"] = "https://evil.example.com" })
		}},
		{"wrong audience", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) { claims["aud"] = "someone-else" })
		}},
		{"expired", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) { claims["exp"] = now.Add(-10 * time.Minute).Unix() })
		}},
		{"not yet valid", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) { claims["nbf"] = now.Add(10 * time.Minute).Unix() })
		}},
		{"issued in the future", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) { claims["iat"] = now.Add(10 * time.Minute).Unix() })
		}},
		{"non-RS256 algorithm", func(jti string) string { return hmacToken(keepClaims(jti)) }},
		{"missing jti", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) { delete(claims, "jti") })
		}},
		{"missing repository id", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) { delete(claims, "repository_id") })
		}},
		{"workflow_ref of another repository", func(jti string) string {
			return withClaims(jti, func(claims jwt.MapClaims) {
				claims["workflow_ref"] = "someone/else/.github/workflows/x.yml@refs/heads/main"
			})
		}},
		{"garbage", func(string) string { return "not-a-jwt" }},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			jti := identityJTIPrefix + randomSuffix(subTest)
			status, body := exchangeToken(subTest, app.appID, testCase.token(jti))

			requireInvalidToken(subTest, testCase.name, status, body)
			if requests := psql(subTest, `SELECT count(*) FROM app_registration_requests WHERE app_id = '`+app.appID+`'`); requests != "0" {
				subTest.Errorf("requests = %s, want 0: a bad token files nothing", requests)
			}
			if recorded := psql(subTest, `SELECT count(*) FROM oidc_replay WHERE jti = '`+jti+`'`); recorded != "0" {
				subTest.Errorf("oidc_replay rows for the rejected token = %s, want 0: a token that fails a check must not burn its jti", recorded)
			}
		})
	}
}

func TestExchangeBodyValidationIs400(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	token := app.token(testInstance, nil)

	for _, testCase := range []struct {
		name  string
		body  map[string]any
		error string
	}{
		{"empty app id", map[string]any{"app_id": "", "token": token}, "invalid app_id"},
		{"uppercase app id", map[string]any{"app_id": "Urls4irl", "token": token}, "invalid app_id"},
		{"app id too long", map[string]any{"app_id": "a" + strings.Repeat("b", 63), "token": token}, "invalid app_id"},
		{"missing token", map[string]any{"app_id": app.appID}, "token is required"},
		{"unknown field", map[string]any{"app_id": app.appID, "token": token, "extra": true}, "invalid JSON body"},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			status, body := apiRequest(subTest, "POST", identityExchangePath, testCase.body)

			requireStatus(subTest, testCase.name, status, 400, body)
			requireErrorMessage(subTest, testCase.name, body, testCase.error)
		})
	}
	// None of the rejected bodies burned the token: it still exchanges.
	if status, body := exchangeToken(testInstance, app.appID, token); status != 202 {
		testInstance.Errorf("the token a 400 left unspent exchanged with status %d, body %s", status, body)
	}
}

func TestExchangeCapsPendingRequestsAt20(testInstance *testing.T) {
	waitForReady(testInstance)
	// Clear this suite's own leftovers so an aborted earlier run cannot shrink the room.
	psql(testInstance, `DELETE FROM app_registration_requests WHERE app_id LIKE 'itid\_%'`)
	// The cap is global, so pending rows from anywhere else on this stack take room from it.
	foreign, convertErr := strconv.Atoi(psql(testInstance, `SELECT count(*) FROM app_registration_requests WHERE status = 'pending'`))
	if convertErr != nil {
		testInstance.Fatalf("counting foreign pending requests: %v", convertErr)
	}
	if foreign >= pendingCap {
		testInstance.Fatalf("the stack already holds %d pending identity requests from outside this suite (cap %d): "+
			"clear them (or recreate the stack with make local-down && make local-up) and rerun", foreign, pendingCap)
	}

	var apps []identityApp
	var capped identityApp
	reachedCap := false
	for range pendingCap + 5 {
		app := newIdentityApp(testInstance)
		status, body := app.exchange(testInstance, nil)
		if status == 429 {
			requireErrorMessage(testInstance, "cap", body, "too many pending identity requests")
			capped, reachedCap = app, true
			break
		}
		requireStatus(testInstance, "filing below the cap", status, 202, body)
		apps = append(apps, app)
	}
	if !reachedCap {
		testInstance.Fatalf("filed %d pending requests and never hit the cap", len(apps))
	}
	if len(apps) != pendingCap-foreign || len(apps) == 0 {
		testInstance.Fatalf("filed %d requests before the cap, want %d (cap %d minus %d foreign pending rows)",
			len(apps), pendingCap-foreign, pendingCap, foreign)
	}

	if total := psql(testInstance, `SELECT count(*) FROM app_registration_requests WHERE status = 'pending'`); total != fmt.Sprint(pendingCap) {
		testInstance.Errorf("pending rows at the cap = %s, want exactly %d", total, pendingCap)
	}
	if capped.pendingRequestCount(testInstance) != "0" {
		testInstance.Error("the capped request must not be filed")
	}
	// An existing request is refreshed, not refused, even at the cap.
	if refreshed := apps[0].exchangeExpecting(testInstance, 202, nil); refreshed.RequestID == 0 {
		testInstance.Error("re-exchanging an already pending request at the cap must refresh it")
	}
	// A refused exchange does not burn its token; once the operator makes room it succeeds.
	token := capped.token(testInstance, nil)
	status, body := exchangeToken(testInstance, capped.appID, token)
	requireStatus(testInstance, "still capped", status, 429, body)
	listed := requestsOf(listIdentityRequests(testInstance), apps[0].appID)
	if len(listed) != 1 {
		testInstance.Fatalf("listed = %+v", listed)
	}
	statusReject, bodyReject := apiRequest(testInstance, "POST", fmt.Sprintf("%s/requests/%d/reject", identityAdminPath, listed[0].ID), nil)
	requireStatus(testInstance, "reject to make room", statusReject, 200, bodyReject)
	status, body = exchangeToken(testInstance, capped.appID, token)
	requireStatus(testInstance, "after room was made", status, 202, body)
}

func TestIdentityRequestListOrdersPendingFirstAndLimitsRejectedToThirtyDays(testInstance *testing.T) {
	older := newIdentityApp(testInstance)
	rejectedApp := newIdentityApp(testInstance)
	newer := newIdentityApp(testInstance)
	stale := newIdentityApp(testInstance)

	olderPending := older.exchangeExpecting(testInstance, 202, nil)
	rejectedPending := rejectedApp.exchangeExpecting(testInstance, 202, nil)
	status, body := apiRequest(testInstance, "POST", fmt.Sprintf("%s/requests/%d/reject", identityAdminPath, rejectedPending.RequestID), nil)
	requireStatus(testInstance, "reject", status, 200, body)
	newerPending := newer.exchangeExpecting(testInstance, 202, nil)
	// A rejection 31 days old is outside the window.
	staleRequest := stale.exchangeExpecting(testInstance, 202, nil)
	staleStatus, staleBody := apiRequest(testInstance, "POST", fmt.Sprintf("%s/requests/%d/reject", identityAdminPath, staleRequest.RequestID), nil)
	requireStatus(testInstance, "reject the request to age", staleStatus, 200, staleBody)
	psql(testInstance, fmt.Sprintf(`UPDATE app_registration_requests SET decided_at = now() - interval '31 days', updated_at = now() - interval '31 days' WHERE id = %d`, staleRequest.RequestID))

	listed := listIdentityRequests(testInstance)
	var ours []identityRequestJSON
	for _, request := range listed {
		switch request.AppID {
		case older.appID, rejectedApp.appID, newer.appID, stale.appID:
			ours = append(ours, request)
		}
	}
	if len(ours) != 3 {
		testInstance.Fatalf("our listed requests = %+v, want the two pending and the recent rejection only", ours)
	}
	if ours[0].ID != newerPending.RequestID || ours[1].ID != olderPending.RequestID || ours[2].ID != rejectedPending.RequestID {
		testInstance.Errorf("order = %d, %d, %d, want newest pending, older pending, then the rejected one (%d, %d, %d)",
			ours[0].ID, ours[1].ID, ours[2].ID, newerPending.RequestID, olderPending.RequestID, rejectedPending.RequestID)
	}
	if ours[0].Status != "pending" || ours[1].Status != "pending" || ours[2].Status != "rejected" {
		testInstance.Errorf("statuses = %s, %s, %s", ours[0].Status, ours[1].Status, ours[2].Status)
	}
	// Everything pending precedes everything rejected, across all apps listed.
	seenRejected := false
	for _, request := range listed {
		if request.Status == "rejected" {
			seenRejected = true
		} else if seenRejected {
			testInstance.Fatalf("a pending request follows a rejected one in the list")
		}
	}
}

func TestRejectingIsIdempotentAndARejectedRequestDoesNotBlockANewOne(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	pending := app.exchangeExpecting(testInstance, 202, nil)
	rejectPath := fmt.Sprintf("%s/requests/%d/reject", identityAdminPath, pending.RequestID)

	var first, second struct {
		Request identityRequestJSON `json:"request"`
	}
	status, body := apiRequest(testInstance, "POST", rejectPath, nil)
	requireStatus(testInstance, "reject", status, 200, body)
	decodeBody(testInstance, "reject", body, &first)
	status, body = apiRequest(testInstance, "POST", rejectPath, nil)
	requireStatus(testInstance, "reject again", status, 200, body)
	decodeBody(testInstance, "reject again", body, &second)
	if first.Request.Status != "rejected" || !reflect.DeepEqual(second.Request, first.Request) {
		testInstance.Errorf("rejecting twice changed the request: %+v then %+v", first.Request, second.Request)
	}
	status, body = apiRequest(testInstance, "POST", identityAdminPath+"/requests/999999999/reject", nil)
	requireStatus(testInstance, "reject unknown", status, 404, body)

	// Rejection binds nothing and issues nothing; the same claims file a fresh pending request.
	if bindings, _ := listIdentityBindings(testInstance); bindingOf(bindings, app.appID) != nil {
		testInstance.Error("rejecting must never bind the app")
	}
	renewed := app.exchangeExpecting(testInstance, 202, nil)
	if renewed.RequestID == pending.RequestID || renewed.Key != "" {
		testInstance.Errorf("re-request after a rejection = %+v (rejected was %d), want a new pending request", renewed, pending.RequestID)
	}
	if count := app.pendingRequestCount(testInstance); count != "1" {
		testInstance.Errorf("pending rows = %s, want 1", count)
	}
	// An already rejected request can no longer be approved.
	status, body = apiRequest(testInstance, "POST", fmt.Sprintf("%s/requests/%d/approve", identityAdminPath, pending.RequestID), nil)
	requireStatus(testInstance, "approve a rejected request", status, 409, body)
}

func TestApprovingARequestDeletesTheCompetingPendingRequestForTheSameApp(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	original := app.exchangeExpecting(testInstance, 202, nil)
	// A second workflow identity asks for the same app id.
	otherWorkflow := func(claims jwt.MapClaims) {
		claims["workflow_ref"] = app.repository + "/.github/workflows/other.yml@refs/heads/main"
	}
	competing := app.exchangeExpecting(testInstance, 202, otherWorkflow)
	if competing.RequestID == original.RequestID {
		testInstance.Fatalf("two distinct claim sets filed one request: %+v", competing)
	}
	approveIdentityRequest(testInstance, original.RequestID)

	status, body := apiRequest(testInstance, "POST", fmt.Sprintf("%s/requests/%d/approve", identityAdminPath, competing.RequestID), nil)

	// Approving a request deletes every other pending request for the same app id in the same
	// transaction, so the competing request no longer exists (404, not 409 "app already bound").
	requireStatus(testInstance, "approve the deleted competing request", status, 404, body)
	for _, remaining := range requestsOf(listIdentityRequests(testInstance), app.appID) {
		if remaining.ID == competing.RequestID {
			testInstance.Errorf("competing request %d still listed after its sibling was approved: %+v", competing.RequestID, remaining)
		}
	}
	bindings, _ := listIdentityBindings(testInstance)
	if binding := bindingOf(bindings, app.appID); binding == nil || binding.WorkflowPath != identityWorkflow {
		testInstance.Errorf("binding = %+v, want it unchanged", binding)
	}
}

func TestApprovingAWorkflowIdentityBoundToAnotherAppIs409(testInstance *testing.T) {
	owner := newIdentityApp(testInstance)
	owner.bind(testInstance)
	// A different app id presenting the owner's exact workflow identity.
	thief := newIdentityApp(testInstance)
	thief.repository, thief.repositoryID = owner.repository, owner.repositoryID
	pending := thief.exchangeExpecting(testInstance, 202, nil)

	status, body := apiRequest(testInstance, "POST", fmt.Sprintf("%s/requests/%d/approve", identityAdminPath, pending.RequestID), nil)

	requireStatus(testInstance, "approve a bound workflow identity", status, 409, body)
	requireErrorMessage(testInstance, "approve a bound workflow identity", body, "workflow identity already bound")
}

func TestIdentityAdminRoutesValidatePathValues(testInstance *testing.T) {
	waitForReady(testInstance)
	for _, route := range []struct{ method, path string }{
		{"POST", identityAdminPath + "/requests/abc/approve"},
		{"POST", identityAdminPath + "/requests/0/reject"},
		{"POST", identityAdminPath + "/keys/-1/revoke"},
		{"DELETE", identityAdminPath + "/bindings/Bad-App"},
	} {
		status, body := apiRequest(testInstance, route.method, route.path, nil)
		requireStatus(testInstance, route.method+" "+route.path, status, 400, body)
	}
	status, body := apiRequest(testInstance, "POST", identityAdminPath+"/requests/999999999/approve", nil)
	requireStatus(testInstance, "approve unknown request", status, 404, body)
}

func TestExchangeLogsNeverContainTheTokenOrTheKey(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	app.bind(testInstance)
	token := app.token(testInstance, nil)
	status, body := exchangeToken(testInstance, app.appID, token)
	requireStatus(testInstance, "issue", status, 200, body)
	var issued exchangeResponseJSON
	decodeBody(testInstance, "issue", body, &issued)
	if status, body := exchangeToken(testInstance, app.appID, token); status != 401 {
		testInstance.Fatalf("replay status = %d, body %s", status, body)
	}

	containerID, resolveErr := resolveServiceContainer(execRunner, "NOTIFS_DELIVERY_CONTAINER", "delivery-api")
	if resolveErr != nil {
		testInstance.Fatalf("resolve delivery-api container: %v", resolveErr)
	}
	commandCtx, cancelCommand := context.WithTimeout(context.Background(), dockerCommandLimit)
	defer cancelCommand()
	logs, logsErr := exec.CommandContext(commandCtx, "docker", "logs", containerID).CombinedOutput()
	if logsErr != nil {
		testInstance.Fatalf("docker logs: %v", logsErr)
	}

	text := string(logs)
	for _, secret := range []string{token, issued.Key, strings.Split(token, ".")[2]} {
		if strings.Contains(text, secret) {
			testInstance.Fatalf("the delivery-api log contains a token or key")
		}
	}
	var exchangeLines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, `"msg":"app identity exchange"`) && strings.Contains(line, app.appID) {
			exchangeLines = append(exchangeLines, line)
		}
	}
	if len(exchangeLines) < 3 {
		testInstance.Fatalf("exchange log lines for %s = %d, want one per exchange (pending, issued, replay):\n%s", app.appID, len(exchangeLines), strings.Join(exchangeLines, "\n"))
	}
	joined := strings.Join(exchangeLines, "\n")
	for _, want := range []string{`"outcome":"pending"`, `"outcome":"issued"`, `"outcome":"invalid_token"`, `"workflow_path":"` + identityWorkflow + `"`,
		`"repository":"` + app.repository + `"`, `"ref":"refs/heads/main"`, `"run_id":"1"`, `"duration":`} {
		if !strings.Contains(joined, want) {
			testInstance.Errorf("the exchange log lines are missing %s:\n%s", want, joined)
		}
	}
}

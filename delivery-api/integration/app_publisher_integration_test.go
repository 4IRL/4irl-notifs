//go:build integration

package integration

// End-to-end tests of the app-facing publisher mint (POST /v1/apps/{app_id}/ntfy-publisher) and
// set-password (POST /v1/apps/{app_id}/set-password) routes against the live compose stack with
// real dev dak_ keys: sealed storage, rotate, the per-app mint lock (held deterministically from the
// test through its own database connection), the per-app rate limit and the operator exemption.
// App ids are unique per run, so leftover ntfy publisher users or rotate=false tokens are harmless.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
)

const (
	// sealOverheadBytes is the AES-GCM nonce (12) plus tag (16): a sealed token is longer than this.
	sealOverheadBytes = 28
	// busyResponseLimit bounds the refused same-app mint far below the 15s upstream timeout.
	busyResponseLimit = 3 * time.Second
	// simultaneousRotates is the number of concurrent rotate mints in the smoke case.
	simultaneousRotates  = 5
	rateLimitedBody      = "publisher mint rate limit exceeded"
	busyBody             = "publisher mint already in progress"
	mintUnavailableBody  = "publisher minting unavailable"
	operatorMintCount    = 12
	concurrentHTTPBudget = 60 * time.Second
	lockHolderTimeout    = 10 * time.Second
)

func appPublisherPath(appID string) string { return "/v1/apps/" + appID + "/ntfy-publisher" }
func appSetPasswordPath(appID string) string {
	return "/v1/apps/" + appID + "/set-password"
}
func operatorPublisherPath(appID string) string {
	return adminBasePath + "/apps/" + appID + "/ntfy-publisher"
}

// newPublisherApp returns a fresh app id and its key, and registers the app's cleanup. The key is
// obtained first so the cleanup (run LIFO before the key's own cleanup) executes while it is valid.
func newPublisherApp(testInstance *testing.T) (string, string) {
	testInstance.Helper()
	waitForReady(testInstance)
	appID := "it" + randomSuffix(testInstance)[:12]
	key := appKeyFor(testInstance, appID)
	registerPublisherAppCleanup(testInstance, appID)
	return appID, key
}

// registerPublisherAppCleanup removes the app's stored publisher, mint-limiter rows and messages
// and its ntfy publisher user. The ntfy removal ignores 4xx, so its status is not asserted.
func registerPublisherAppCleanup(testInstance *testing.T, appID string) {
	testInstance.Helper()
	testInstance.Cleanup(func() { deprovisionUser(testInstance, appID+"-publisher") })
	testInstance.Cleanup(func() {
		if !sinkAppIDPattern.MatchString(appID) {
			testInstance.Errorf("cleanup: refusing to delete rows for malformed app id %q", appID)
			return
		}
		psql(testInstance, fmt.Sprintf("delete from messages where app_id = '%s'", appID))
		psql(testInstance, fmt.Sprintf("delete from app_publishers where app_id = '%s'", appID))
		psql(testInstance, fmt.Sprintf("delete from app_publisher_mints where app_id = '%s'", appID))
	})
}

func publisherRowCount(testInstance *testing.T, appID string) string {
	testInstance.Helper()
	return psql(testInstance, fmt.Sprintf("select count(*) from app_publishers where app_id = '%s'", appID))
}

func mintRowCount(testInstance *testing.T, appID string) string {
	testInstance.Helper()
	return psql(testInstance, fmt.Sprintf("select count(*) from app_publisher_mints where app_id = '%s'", appID))
}

// requireSealedNotPlaintext proves the stored row exists, is longer than the seal overhead and holds
// no plaintext tk_ token.
func requireSealedNotPlaintext(testInstance *testing.T, appID string) {
	testInstance.Helper()
	if count := publisherRowCount(testInstance, appID); count != "1" {
		testInstance.Fatalf("app_publishers rows for %s = %s, want 1", appID, count)
	}
	sealedLength, parseErr := strconv.Atoi(psql(testInstance,
		fmt.Sprintf("select length(sealed_token) from app_publishers where app_id = '%s'", appID)))
	if parseErr != nil || sealedLength <= sealOverheadBytes {
		testInstance.Fatalf("sealed_token length = %d (parse error %v), want more than %d", sealedLength, parseErr, sealOverheadBytes)
	}
	if position := psql(testInstance,
		fmt.Sprintf("select position('tk_'::bytea in sealed_token) from app_publishers where app_id = '%s'", appID)); position != "0" {
		testInstance.Fatalf("sealed_token contains a plaintext tk_ token at position %s", position)
	}
}

// mintJSON mints with the app's key and returns status, headers and body.
func mintJSON(testInstance *testing.T, key, appID string, body any) (int, http.Header, []byte) {
	testInstance.Helper()
	return apiResponse(testInstance, bearer(key), http.MethodPost, appPublisherPath(appID), body)
}

// provisionSubscriber provisions email in the app through the app's own key and registers its removal.
func provisionSubscriber(testInstance *testing.T, appID, key, email string) provisionedSubscriber {
	testInstance.Helper()
	testInstance.Cleanup(func() { deprovisionUser(testInstance, personhash.NtfyUser(email)) })
	status, _, body := apiResponse(testInstance, bearer(key), http.MethodPost, appProvisionPath(appID), map[string]any{"email": email})
	requireStatus(testInstance, "provision subscriber", status, http.StatusOK, body)
	var subscriber provisionedSubscriber
	decodeBody(testInstance, "provision subscriber", body, &subscriber)
	if subscriber.Token == "" {
		testInstance.Fatalf("provision subscriber returned no token: %s", body)
	}
	subscriber.PersonHash = personhash.Hash(email)
	return subscriber
}

// requireRecipientDelivery submits a to.recipient message with a unique title and requires it to be
// delivered and then readable on the subscriber's own ntfy topic, which proves the app's stored
// publisher token works.
func requireRecipientDelivery(testInstance *testing.T, appID, email string, subscriber provisionedSubscriber) {
	testInstance.Helper()
	title := "publisher-it-" + randomSuffix(testInstance)
	submit := recipientSubmitBody(appID, email, "it-"+title)
	submit["display"] = map[string]any{"title": title, "body": sinkMessageBody}
	status, body := apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s?wait=%d", messagesPath, submitWaitSeconds), submit)
	requireStatus(testInstance, "POST recipient message", status, http.StatusAccepted, body)
	var submitted submitResponse
	decodeBody(testInstance, "POST recipient message", body, &submitted)
	if len(submitted.Message.Deliveries) != 1 || submitted.Message.Deliveries[0].State != sinkDeliveredState {
		testInstance.Fatalf("recipient delivery not delivered (body: %s)", body)
	}
	topic := personhash.PersonChannelTopic(appID, subscriber.PersonHash, sinkChannel)
	deadline := time.Now().Add(ntfyPollTimeout)
	for {
		for _, event := range readNtfyTopic(testInstance, topic, subscriber.Token) {
			if event.Title == title {
				return
			}
		}
		if time.Now().After(deadline) {
			testInstance.Fatalf("ntfy topic %s never held a message titled %q", topic, title)
		}
		time.Sleep(ntfyPollInterval)
	}
}

func decodeMintView(testInstance *testing.T, label string, body []byte) publisherMintView {
	testInstance.Helper()
	var view publisherMintView
	decodeBody(testInstance, label, body, &view)
	return view
}

// publisherMintView is publisherMintResponse plus the timestamps.
type publisherMintView struct {
	Publisher struct {
		AppID           string    `json:"app_id"`
		PublisherUserID string    `json:"publisher_user_id"`
		TopicPattern    string    `json:"topic_pattern"`
		UpdatedAt       time.Time `json:"updated_at"`
	} `json:"publisher"`
}

func TestAppMintsItsPublisherSealed(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)

	status, headers, body := mintJSON(testInstance, key, appID, map[string]any{})
	requireStatus(testInstance, "mint", status, http.StatusOK, body)
	if cacheControl := headers.Get("Cache-Control"); cacheControl != "no-store" {
		testInstance.Errorf("mint Cache-Control = %q, want no-store", cacheControl)
	}
	first := decodeMintView(testInstance, "mint", body)
	if first.Publisher.PublisherUserID != appID+"-publisher" || first.Publisher.AppID != appID {
		testInstance.Errorf("minted publisher = %+v, want app %q user %q", first.Publisher, appID, appID+"-publisher")
	}
	if strings.Contains(string(body), "tk_") {
		testInstance.Errorf("mint response carries a tk_ token: %s", body)
	}
	requireSealedNotPlaintext(testInstance, appID)

	status, _, body = mintJSON(testInstance, key, appID, map[string]any{})
	requireStatus(testInstance, "mint again", status, http.StatusOK, body)
	second := decodeMintView(testInstance, "mint again", body)
	if !second.Publisher.UpdatedAt.After(first.Publisher.UpdatedAt) {
		testInstance.Errorf("updated_at did not advance: %s then %s", first.Publisher.UpdatedAt, second.Publisher.UpdatedAt)
	}
	requireSealedNotPlaintext(testInstance, appID)
}

func TestAppPublisherMintRefusals(testInstance *testing.T) {
	appA, keyA := newPublisherApp(testInstance)
	appB, _ := newPublisherApp(testInstance)

	status, _, body := mintJSON(testInstance, keyA, appB, map[string]any{})
	requireStatus(testInstance, "A's key on B's path", status, http.StatusForbidden, body)

	status, _, body = apiResponse(testInstance, "", http.MethodPost, appPublisherPath(appA), map[string]any{})
	requireStatus(testInstance, "mint without a key", status, http.StatusUnauthorized, body)

	status, _, body = apiResponse(testInstance, bearer(keyA), http.MethodPost, appPublisherPath(appA), nil)
	requireStatus(testInstance, "mint with an empty body", status, http.StatusBadRequest, body)

	status, _, body = mintJSON(testInstance, keyA, appA, map[string]any{"app_id": appA})
	requireStatus(testInstance, "mint with an unknown field", status, http.StatusBadRequest, body)

	if count := publisherRowCount(testInstance, appA); count != "0" {
		testInstance.Errorf("refused mints stored %s publisher rows for %s, want 0", count, appA)
	}
}

func TestRotatedPublisherStillDeliversToRecipients(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)
	email := "it-rotate-" + randomSuffix(testInstance) + "@example.test"
	subscriber := provisionSubscriber(testInstance, appID, key, email)

	status, _, body := mintJSON(testInstance, key, appID, map[string]any{})
	requireStatus(testInstance, "mint", status, http.StatusOK, body)
	requireRecipientDelivery(testInstance, appID, email, subscriber)

	// The post-rotate stored token is what delivers. That the old token was revoked upstream is
	// deliberately not asserted (it needs ntfy token-list internals); rotate=true forwarding is
	// covered by the unit tests (exact upstream body, handler Mint(app, true)).
	status, _, body = mintJSON(testInstance, key, appID, map[string]any{"rotate": true})
	requireStatus(testInstance, "rotate", status, http.StatusOK, body)
	requireSealedNotPlaintext(testInstance, appID)
	requireRecipientDelivery(testInstance, appID, email, subscriber)
}

// holdMintLock begins a transaction on its own connection and takes the app's mint advisory lock
// (the same key delivery-api's try-lock uses), returning a release function that rolls back.
func holdMintLock(testInstance *testing.T, appID string) func() {
	testInstance.Helper()
	if os.Getenv("NOTIFS_DELIVERY_TEST_DSN") == "" {
		testInstance.Fatal("NOTIFS_DELIVERY_TEST_DSN is unset: the busy proof needs the running stack's database " +
			"(run via make delivery-integration-test); a skipped busy proof is no proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockHolderTimeout)
	defer cancel()
	conn, connectErr := pgx.Connect(ctx, pgtest.DSN())
	if connectErr != nil {
		testInstance.Fatalf("connecting to the delivery database: %v", connectErr)
	}
	tx, beginErr := conn.Begin(ctx)
	if beginErr != nil {
		_ = conn.Close(context.Background())
		testInstance.Fatalf("begin: %v", beginErr)
	}
	if _, lockErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		publisher.MintLockPrefix+appID); lockErr != nil {
		_ = conn.Close(context.Background())
		testInstance.Fatalf("taking the advisory lock: %v", lockErr)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), lockHolderTimeout)
		defer releaseCancel()
		if rollbackErr := tx.Rollback(releaseCtx); rollbackErr != nil {
			testInstance.Errorf("releasing the advisory lock: %v", rollbackErr)
		}
		if closeErr := conn.Close(releaseCtx); closeErr != nil {
			testInstance.Errorf("closing the lock connection: %v", closeErr)
		}
	}
	testInstance.Cleanup(release)
	return release
}

func TestSameAppMintWhileLockHeldIsRefusedBusy(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)
	release := holdMintLock(testInstance, appID)

	started := time.Now()
	status, _, body := mintJSON(testInstance, key, appID, map[string]any{})
	if elapsed := time.Since(started); elapsed > busyResponseLimit {
		testInstance.Errorf("busy mint took %s, want a fast refusal under %s", elapsed, busyResponseLimit)
	}
	if status == http.StatusOK {
		testInstance.Fatal("mint returned 200 with the lock held: the test DSN likely points at a different " +
			"database than the running stack")
	}
	requireStatus(testInstance, "app mint while locked", status, http.StatusConflict, body)
	requireErrorMessage(testInstance, "app mint while locked", body, busyBody)
	if count := mintRowCount(testInstance, appID); count != "0" {
		testInstance.Errorf("a busy refusal recorded %s mint rows, want 0", count)
	}

	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), nil)
	requireStatus(testInstance, "operator mint while locked", status, http.StatusConflict, body)
	requireErrorMessage(testInstance, "operator mint while locked", body, busyBody)

	release()

	status, _, body = mintJSON(testInstance, key, appID, map[string]any{})
	requireStatus(testInstance, "app mint after release", status, http.StatusOK, body)
	if count := mintRowCount(testInstance, appID); count != "1" {
		testInstance.Errorf("mint rows after release = %s, want exactly 1", count)
	}
	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), nil)
	requireStatus(testInstance, "operator mint after release", status, http.StatusOK, body)
}

type rotateResult struct {
	status int
	body   string
	err    error
}

// postRotate is a goroutine-safe mint (no t.Fatal off the test goroutine).
func postRotate(client *http.Client, key, appID string) rotateResult {
	request, requestErr := http.NewRequestWithContext(context.Background(), http.MethodPost,
		deliveryURL()+appPublisherPath(appID), bytes.NewReader([]byte(`{"rotate":true}`)))
	if requestErr != nil {
		return rotateResult{err: requestErr}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", bearer(key))
	response, doErr := client.Do(request)
	if doErr != nil {
		return rotateResult{err: doErr}
	}
	defer func() { _ = response.Body.Close() }()
	var buffer bytes.Buffer
	if _, readErr := buffer.ReadFrom(response.Body); readErr != nil {
		return rotateResult{err: readErr}
	}
	return rotateResult{status: response.StatusCode, body: buffer.String()}
}

// TestSimultaneousRotatesLeaveAWorkingToken is a smoke test; the deterministic busy proof is above.
func TestSimultaneousRotatesLeaveAWorkingToken(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)
	email := "it-rotates-" + randomSuffix(testInstance) + "@example.test"
	subscriber := provisionSubscriber(testInstance, appID, key, email)

	client := &http.Client{Timeout: concurrentHTTPBudget}
	results := make([]rotateResult, simultaneousRotates)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index] = postRotate(client, key, appID)
		}()
	}
	group.Wait()

	succeeded := 0
	for index, result := range results {
		if result.err != nil {
			testInstance.Errorf("rotate %d: %v", index, result.err)
			continue
		}
		switch result.status {
		case http.StatusOK:
			succeeded++
		case http.StatusConflict:
			if !strings.Contains(result.body, busyBody) {
				testInstance.Errorf("rotate %d: 409 body %s, want %q", index, result.body, busyBody)
			}
		case http.StatusServiceUnavailable:
			if !strings.Contains(result.body, mintUnavailableBody) {
				testInstance.Errorf("rotate %d: 503 body %s, want %q", index, result.body, mintUnavailableBody)
			}
		default:
			testInstance.Errorf("rotate %d: status %d (body: %s), want 200, 409 or 503", index, result.status, result.body)
		}
	}
	if succeeded < 1 {
		testInstance.Fatalf("no simultaneous rotate succeeded: %+v", results)
	}
	requireSealedNotPlaintext(testInstance, appID)
	requireRecipientDelivery(testInstance, appID, email, subscriber)
}

func TestAppMintRateLimit(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)
	otherApp, otherKey := newPublisherApp(testInstance)

	for attempt := 1; attempt <= publisher.MintRateLimit; attempt++ {
		status, _, body := mintJSON(testInstance, key, appID, map[string]any{})
		requireStatus(testInstance, fmt.Sprintf("mint #%d", attempt), status, http.StatusOK, body)
	}
	status, headers, body := mintJSON(testInstance, key, appID, map[string]any{})
	requireStatus(testInstance, "mint over the limit", status, http.StatusTooManyRequests, body)
	requireErrorMessage(testInstance, "mint over the limit", body, rateLimitedBody)
	if retryAfter, parseErr := strconv.Atoi(headers.Get("Retry-After")); parseErr != nil || retryAfter <= 0 {
		testInstance.Errorf("Retry-After = %q, want a positive integer", headers.Get("Retry-After"))
	}
	if count := mintRowCount(testInstance, appID); count != strconv.Itoa(publisher.MintRateLimit) {
		testInstance.Errorf("mint rows = %s, want %d (the refused one is not recorded)", count, publisher.MintRateLimit)
	}

	status, _, body = mintJSON(testInstance, otherKey, otherApp, map[string]any{})
	requireStatus(testInstance, "another app's mint", status, http.StatusOK, body)
}

func TestOperatorMintsAreNeverRateLimited(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)

	for attempt := 1; attempt <= operatorMintCount; attempt++ {
		status, body := apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), nil)
		requireStatus(testInstance, fmt.Sprintf("operator mint #%d", attempt), status, http.StatusOK, body)
	}
	if count := mintRowCount(testInstance, appID); count != "0" {
		testInstance.Errorf("operator mints recorded %s mint rows, want 0", count)
	}

	// The app's own route counts only its own mints.
	status, _, body := mintJSON(testInstance, key, appID, map[string]any{})
	requireStatus(testInstance, "app mint after operator mints", status, http.StatusOK, body)
	if count := mintRowCount(testInstance, appID); count != "1" {
		testInstance.Errorf("mint rows after one app mint = %s, want 1", count)
	}
}

// ntfyTopicStatusWithBasicAuth polls a topic with an ntfy username and password and returns the status.
func ntfyTopicStatusWithBasicAuth(testInstance *testing.T, topic, username, password string) int {
	testInstance.Helper()
	request, requestErr := http.NewRequestWithContext(
		context.Background(), http.MethodGet, ntfyBaseURL()+"/"+topic+"/json?poll=1&since=all", nil)
	if requestErr != nil {
		testInstance.Fatalf("building ntfy basic-auth request: %v", requestErr)
	}
	request.SetBasicAuth(username, password)
	response, doErr := sinkHTTPClient.Do(request)
	if doErr != nil {
		testInstance.Fatalf("reading ntfy topic %s: %v", topic, doErr)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			testInstance.Errorf("closing ntfy basic-auth body: %v", closeErr)
		}
	}()
	return response.StatusCode
}

type setPasswordJSON struct {
	UserID  string `json:"user_id"`
	AppID   string `json:"app_id"`
	Updated bool   `json:"updated"`
}

func TestAppSetsItsPersonsPassword(testInstance *testing.T) {
	appA, keyA := newPublisherApp(testInstance)
	appB, keyB := newPublisherApp(testInstance)
	email := "it-setpw-" + randomSuffix(testInstance) + "@example.test"
	subscriber := provisionSubscriber(testInstance, appA, keyA, email)
	username := personhash.NtfyUser(email)
	topic := personhash.PersonChannelTopic(appA, subscriber.PersonHash, sinkChannel)
	const newPassword = "it-new-password-1"

	status, _, body := apiResponse(testInstance, bearer(keyA), http.MethodPost, appSetPasswordPath(appA),
		map[string]any{"email": email, "password": newPassword})
	requireStatus(testInstance, "set-password", status, http.StatusOK, body)
	var updated setPasswordJSON
	decodeBody(testInstance, "set-password", body, &updated)
	if !updated.Updated || updated.AppID != appA || updated.UserID != username {
		testInstance.Errorf("set-password answer = %+v, want updated for app %q user %q", updated, appA, username)
	}
	if got := ntfyTopicStatusWithBasicAuth(testInstance, topic, username, newPassword); got != http.StatusOK {
		testInstance.Fatalf("basic-auth read with the new password status = %d, want 200", got)
	}
	if got := ntfyTopicStatusWithBasicAuth(testInstance, topic, username, newPassword+"-wrong"); got != http.StatusUnauthorized && got != http.StatusForbidden {
		testInstance.Errorf("basic-auth read with a wrong password status = %d, want 401 or 403", got)
	}

	status, _, body = apiResponse(testInstance, bearer(keyA), http.MethodPost, appSetPasswordPath(appA),
		map[string]any{"email": email, "password": "1234567"})
	requireStatus(testInstance, "set-password with 7 bytes", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "set-password with 7 bytes", body, "invalid password")

	status, _, body = apiResponse(testInstance, bearer(keyB), http.MethodPost, appSetPasswordPath(appB),
		map[string]any{"email": email, "password": "it-hijack-password"})
	requireStatus(testInstance, "set-password by a stranger app", status, http.StatusNotFound, body)
	requireErrorMessage(testInstance, "set-password by a stranger app", body, "user does not exist")
	if got := ntfyTopicStatusWithBasicAuth(testInstance, topic, username, newPassword); got != http.StatusOK {
		testInstance.Errorf("the stranger's refused call changed the password: basic-auth status = %d, want 200", got)
	}
}

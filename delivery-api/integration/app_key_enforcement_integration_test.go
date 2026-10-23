//go:build integration

package integration

// End-to-end tests of the app-key check on every app-facing endpoint (POST/GET/DELETE
// /v1/messages and the heartbeat pings) against the live compose stack, and of the operator's
// key-free POST /v1/admin/messages. Keys come from the dev token exchange (app_key_integration_test.go).

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	// keyTestAppPrefix starts the ids of the extra apps these tests bind.
	keyTestAppPrefix = "itkey_"
	keyTestCheckName = "nightly"
	// oversizedBodyBytes is just above the 256 KiB submit body cap. The overshoot is kept small
	// on purpose: the server stops reading at the cap and closes the connection after answering,
	// so the little that is left unread fits in socket buffers and the client always gets to read
	// the 413 instead of a connection reset.
	oversizedBodyBytes = 256<<10 + 1<<10
	missingKeyBody     = "app key required"
	invalidKeyBody     = "invalid app key"
	mismatchBody       = "app_id does not match app key"
)

// keyedRoute is one app-facing request for the app it names.
type keyedRoute struct {
	name   string
	method string
	path   string
	body   any
}

// appFacingRoutes lists every app-facing route, each naming appID. messageID and
// idempotencyKey name an existing message of the fixture's app (the by-id and by-key routes
// look it up under whichever app the route names).
func appFacingRoutes(fixture scheduledFixture, appID, messageID, idempotencyKey string) []keyedRoute {
	return []keyedRoute{
		{
			name: "submit", method: http.MethodPost, path: messagesPath,
			body: map[string]any{
				"app_id": appID, "channel": fixture.channel, "idempotency_key": "enf-" + idempotencyKey,
				"payload": map[string]any{"n": 1},
			},
		},
		{name: "get", method: http.MethodGet, path: cancelByIDPath(appID, messageID)},
		{name: "get by key", method: http.MethodGet, path: cancelByKeyPath(appID, idempotencyKey)},
		{name: "cancel", method: http.MethodDelete, path: cancelByIDPath(appID, messageID)},
		{name: "cancel by key", method: http.MethodDelete, path: cancelByKeyPath(appID, idempotencyKey)},
		{name: "ping", method: http.MethodPost, path: fmt.Sprintf("%s/%s/%s", heartbeatsPingPath, appID, keyTestCheckName)},
		{name: "fail ping", method: http.MethodPost, path: fmt.Sprintf("%s/%s/%s/fail", heartbeatsPingPath, appID, keyTestCheckName)},
	}
}

// requireRefusal sends route with authorization and requires status and the error message.
// credential describes the Authorization header for failure messages: never pass the header
// itself, which can carry a real issued key.
func requireRefusal(
	testInstance *testing.T, route keyedRoute, authorization, credential string, wantStatus int, wantMessage string,
) http.Header {
	testInstance.Helper()
	status, header, body := apiResponse(testInstance, authorization, route.method, route.path, route.body)
	label := fmt.Sprintf("%s with %s", route.name, credential)
	requireStatus(testInstance, label, status, wantStatus, body)
	requireErrorMessage(testInstance, label, body, wantMessage)
	return header
}

func TestAppFacingRoutesRequireAValidAppKey(testInstance *testing.T) {
	fixture := newScheduledFixture(testInstance)
	suffix := randomSuffix(testInstance)
	idempotencyKey := "key-" + suffix
	created := fixture.mustSubmit(testInstance, "POST scheduled message", "",
		fixture.body(idempotencyKey, future(scheduledHorizon), 1))
	messageID := created.Message.MessageID
	routes := appFacingRoutes(fixture, fixture.appID, messageID, idempotencyKey)

	unknownKey := "dak_" + strings.Repeat("A", 43)
	for _, route := range routes {
		for _, header := range []string{"", "Basic abc", "Bearer", "Bearer not-a-delivery-key", "Bearer dak_short"} {
			challenge := requireRefusal(testInstance, route, header, fmt.Sprintf("header %q", header),
				http.StatusUnauthorized, missingKeyBody)
			if challenge.Get("WWW-Authenticate") != "Bearer" {
				testInstance.Fatalf("%s with %q: WWW-Authenticate = %q, want Bearer", route.name, header, challenge.Get("WWW-Authenticate"))
			}
		}
		challenge := requireRefusal(testInstance, route, bearer(unknownKey), "an unknown key",
			http.StatusUnauthorized, invalidKeyBody)
		if challenge.Get("WWW-Authenticate") != "Bearer" {
			testInstance.Fatalf("%s with an unknown key: WWW-Authenticate = %q, want Bearer", route.name, challenge.Get("WWW-Authenticate"))
		}
	}

	// Nothing a refused request asked for happened: no message was created for the refused
	// submit, and the message is still scheduled.
	if got := psql(testInstance, fmt.Sprintf("select count(*) from messages where idempotency_key = 'enf-%s'", idempotencyKey)); got != "0" {
		testInstance.Fatalf("messages stored by refused submits = %s, want 0", got)
	}
	requireScheduledView(testInstance, "after the refused requests", getAs(testInstance, fixture.appID, messageID),
		*created.Message.DeliverAt)
}

func TestAppKeyOfAnotherAppCannotActAsThisApp(testInstance *testing.T) {
	fixture := newScheduledFixture(testInstance)
	suffix := randomSuffix(testInstance)
	idempotencyKey := "key-" + suffix
	created := fixture.mustSubmit(testInstance, "POST scheduled message", "",
		fixture.body(idempotencyKey, future(scheduledHorizon), 1))
	messageID := created.Message.MessageID
	deliverAt := *created.Message.DeliverAt

	// App B's key, naming app A anywhere (body, query or path), is a 403.
	otherApp := keyTestAppPrefix + suffix
	otherKey := bearer(appKeyFor(testInstance, otherApp))
	for _, route := range appFacingRoutes(fixture, fixture.appID, messageID, idempotencyKey) {
		requireRefusal(testInstance, route, otherKey, "app B's key", http.StatusForbidden, mismatchBody)
	}
	requireScheduledView(testInstance, "after the cross-app requests", getAs(testInstance, fixture.appID, messageID), deliverAt)
	if got := psql(testInstance, fmt.Sprintf("select count(*) from messages where idempotency_key = 'enf-%s'", idempotencyKey)); got != "0" {
		testInstance.Fatalf("messages stored by the refused cross-app submit = %s, want 0", got)
	}

	// Asking as itself, app B cannot read or cancel app A's message: it is simply not found.
	status, body := apiRequest(testInstance, http.MethodGet, cancelByIDPath(otherApp, messageID), nil)
	requireStatus(testInstance, "app B reads app A's message", status, http.StatusNotFound, body)
	requireCancelOutcome(testInstance, "app B cancels app A's message",
		cancel(testInstance, "DELETE as app B", cancelByIDPath(otherApp, messageID)),
		cancelJSON{MessageID: messageID, Cancelled: false, Status: statusNotFoundName})
	requireScheduledView(testInstance, "after app B's own requests", getAs(testInstance, fixture.appID, messageID), deliverAt)

	// App A's key cannot ping app B's check; app B's can.
	destinationName := "itest_" + suffix
	createDestination(testInstance, destinationName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + suffix + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:signingSecretLength],
		},
	})
	status, body = adminRequest(testInstance, http.MethodPost, heartbeatsAdminPath, map[string]any{
		"app_id": otherApp, "name": keyTestCheckName, "period_seconds": 3600, "grace_seconds": 3600,
		"destinations": []string{destinationName},
	})
	var check checkEnvelope
	if status == http.StatusCreated {
		decodeBody(testInstance, "created heartbeat check", body, &check)
		cleanupHeartbeatCheck(testInstance, check.Check.ID)
	}
	requireStatus(testInstance, "POST heartbeat check", status, http.StatusCreated, body)
	appAKey := bearer(appKeyFor(testInstance, fixture.appID))
	for _, suffixPath := range []string{"", "/fail"} {
		route := keyedRoute{
			name: "app A pings app B's check" + suffixPath, method: http.MethodPost,
			path: fmt.Sprintf("%s/%s/%s%s", heartbeatsPingPath, otherApp, keyTestCheckName, suffixPath),
		}
		requireRefusal(testInstance, route, appAKey, "app A's key", http.StatusForbidden, mismatchBody)
	}
	if got := getCheck(testInstance, check.Check.ID); got.State != "new" || got.LastPingAt != nil {
		testInstance.Fatalf("app B's check after app A's refused pings = %+v, want it still new and never pinged", got)
	}
	if ping := postPing(testInstance, otherApp, keyTestCheckName, ""); ping.State != "up" || ping.AppID != otherApp {
		testInstance.Fatalf("app B's own ping = %+v, want an up state for its check", ping)
	}
}

func TestRevokedAndExpiredAppKeysAreRefused(testInstance *testing.T) {
	waitForReady(testInstance)
	appID := keyTestAppPrefix + randomSuffix(testInstance)
	testInstance.Cleanup(func() { cleanupAppKeyRows(testInstance, appID) })
	probe := keyedRoute{
		name: "cancel by key", method: http.MethodDelete,
		path: cancelByKeyPath(appID, "no-such-key"),
	}
	requireWorks := func(label, key string) {
		testInstance.Helper()
		status, body := apiResponseStatus(testInstance, bearer(key), probe)
		requireStatus(testInstance, label, status, http.StatusOK, body)
	}

	first := bindAndIssueKey(testInstance, appID)
	requireWorks("a freshly issued key", first.Key)

	// A newer key supersedes the first, which keeps working through its 24 h grace.
	second := bindAndIssueKey(testInstance, appID)
	requireWorks("the new key", second.Key)
	requireWorks("a superseded key inside its grace", first.Key)

	// Past the grace the superseded key stops, and the newer one is unaffected.
	psql(testInstance, fmt.Sprintf(
		"update app_keys set superseded_at = now() - interval '25 hours' where id = %d", first.KeyID))
	requireRefusal(testInstance, probe, bearer(first.Key), "the expired key", http.StatusUnauthorized, invalidKeyBody)
	requireWorks("the new key after the old one expired", second.Key)

	// The operator revokes the newer key: it stops at once.
	status, body := apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s/keys/%d/revoke", identityAdminPath, second.KeyID), nil)
	requireStatus(testInstance, "revoke the key", status, http.StatusOK, body)
	requireRefusal(testInstance, probe, bearer(second.Key), "the revoked key", http.StatusUnauthorized, invalidKeyBody)

	// last_used_at was recorded by the successful calls above.
	bindings, _ := listIdentityBindings(testInstance)
	binding := bindingOf(bindings, appID)
	if binding == nil {
		testInstance.Fatalf("app %s has no binding", appID)
	}
	recorded := false
	for _, key := range binding.Keys {
		if key.ID == second.KeyID && key.LastUsedAt != nil {
			recorded = true
		}
	}
	if !recorded {
		testInstance.Fatalf("key %d has no last_used_at after successful calls: %+v", second.KeyID, binding.Keys)
	}
}

// apiResponseStatus sends route with authorization and returns its status and body.
func apiResponseStatus(testInstance *testing.T, authorization string, route keyedRoute) (int, []byte) {
	testInstance.Helper()
	return apiRequestWithAuthorization(testInstance, authorization, route.method, route.path, route.body)
}

func TestSubmitBodyHandlingBehindTheAppKeyCheck(testInstance *testing.T) {
	fixture := newScheduledFixture(testInstance)
	suffix := randomSuffix(testInstance)
	key := bearer(appKeyFor(testInstance, fixture.appID))

	// Over the 256 KiB cap: 413, whoever wrote it.
	oversized := []byte(`{"app_id":"` + fixture.appID + `","payload":"` + strings.Repeat("a", oversizedBodyBytes) + `"}`)
	status, body := apiRequestWithAuthorization(testInstance, key, http.MethodPost, messagesPath, oversized)
	requireStatus(testInstance, "oversized submit", status, http.StatusRequestEntityTooLarge, body)
	requireErrorMessage(testInstance, "oversized submit", body, "request body too large")

	// Malformed JSON under the cap reaches the handler's own 400.
	status, body = apiRequestWithAuthorization(testInstance, key, http.MethodPost, messagesPath,
		[]byte(`{"app_id":"`+fixture.appID+`","channel":`))
	requireStatus(testInstance, "malformed submit", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "malformed submit", body, "invalid JSON body")

	// No asserted app_id is not a mismatch: it falls through to the handler's own 400.
	for _, withoutApp := range []map[string]any{
		{"channel": fixture.channel, "idempotency_key": "noapp-" + suffix, "payload": map[string]any{"n": 1}},
		{"app_id": "", "channel": fixture.channel, "idempotency_key": "noapp-" + suffix, "payload": map[string]any{"n": 1}},
	} {
		status, body = apiRequestWithAuthorization(testInstance, key, http.MethodPost, messagesPath, withoutApp)
		requireStatus(testInstance, "submit without an app_id", status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "submit without an app_id", body, "invalid app_id")
	}

	// A well-formed body that names the caller's own app still submits.
	submitted := fixture.mustSubmit(testInstance, "POST message", "", fixture.body("ok-"+suffix, future(scheduledHorizon), 1))
	if submitted.Message.AppID != fixture.appID || submitted.Message.Status != statusScheduledName {
		testInstance.Fatalf("submitted message = %+v, want a scheduled message of %s", submitted.Message, fixture.appID)
	}
}

func TestAdminMessagesAndTheProbesNeedNoAppKey(testInstance *testing.T) {
	fixture := newScheduledFixture(testInstance)
	suffix := randomSuffix(testInstance)

	// The operator's send-test submits for any app with no key, and is not an app-facing route.
	// With ?wait it holds for the first attempt: the fixture's destination is a loopback URL the
	// production transport refuses, so the one delivery dead-letters as blocked_address.
	status, body := apiRequestWithAuthorization(testInstance, "", http.MethodPost,
		fmt.Sprintf("/v1/admin/messages?wait=%d", submitWaitSeconds), fixture.body("admin-"+suffix, "", 1))
	requireStatus(testInstance, "POST /v1/admin/messages", status, http.StatusAccepted, body)
	var submitted submitResponse
	decodeBody(testInstance, "POST /v1/admin/messages", body, &submitted)
	if submitted.Message.AppID != fixture.appID || submitted.Duplicate {
		testInstance.Fatalf("admin submit = %+v, want a new message for %s", submitted, fixture.appID)
	}
	if len(submitted.Message.Deliveries) != 1 {
		testInstance.Fatalf("admin submit deliveries = %d, want 1 (body: %s)", len(submitted.Message.Deliveries), body)
	}
	delivery := submitted.Message.Deliveries[0]
	if delivery.State != deadStateName || delivery.DeadReason == nil || *delivery.DeadReason != blockedAddressState ||
		delivery.AttemptCount != 1 || !strings.HasPrefix(delivery.Destination, "itest_") {
		testInstance.Fatalf("admin submit delivery = %+v, want dead/%s after 1 attempt to the fixture destination",
			delivery, blockedAddressState)
	}

	// The same body without a key on the app route is refused.
	status, body = apiRequestWithAuthorization(testInstance, "", http.MethodPost, messagesPath,
		fixture.body("admin-"+suffix, "", 1))
	requireStatus(testInstance, "POST /v1/messages without a key", status, http.StatusUnauthorized, body)

	for _, path := range []string{"/healthz", "/readyz", "/v1/admin/deliveries", "/v1/admin/heartbeats", "/v1/admin/scheduled-messages"} {
		status, body = apiRequestWithAuthorization(testInstance, "", http.MethodGet, path, nil)
		requireStatus(testInstance, "GET "+path+" without a key", status, http.StatusOK, body)
	}
}

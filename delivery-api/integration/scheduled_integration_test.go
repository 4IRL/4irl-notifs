//go:build integration

package integration

// End-to-end test of deferred send against the live compose stack: schedule,
// read back, replace, cancel by id and by key, the operator list and cancel, the
// per-app cap and a forced release. Every test owns a unique app_id, schedules
// at most two messages under it (the cap test excepted) and deletes them with
// psql on cleanup, so tests neither count against each other's per-app cap nor
// leave rows behind. The destination's URL is a loopback address the production
// transport refuses, so a released message dead-letters as blocked_address
// without any external receiver.

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	// scheduledAppPrefix starts every app_id these tests submit under.
	scheduledAppPrefix = "itsch_"
	// maxScheduledEnv carries the stack's DELIVERY_MAX_SCHEDULED_PER_APP, forwarded by
	// the Makefile's delivery-integration-test target.
	maxScheduledEnv = "NOTIFS_DELIVERY_MAX_SCHEDULED"
	// minCapForLiveTest is the smallest cap the live suite supports: the admin list test
	// schedules two messages under one app_id, so a cap of 1 would 429 it.
	minCapForLiveTest = 2
	// maxCapForLiveTest is the largest cap the live 429 test will fill: above it the
	// test skips rather than schedule that many messages.
	maxCapForLiveTest = 5
	// scheduledHorizon is how far ahead a test schedules, far beyond the poller.
	scheduledHorizon = 24 * time.Hour
	// scheduledAnswerBound is the longest a scheduled submit may take even with
	// ?wait=10: anything near the wait would mean the wait was not skipped.
	scheduledAnswerBound = 5 * time.Second
	releasePollTimeout   = 20 * time.Second
	releasePollInterval  = 250 * time.Millisecond

	statusScheduledName = "scheduled"
	statusCancelledName = "cancelled"
	statusNotFoundName  = "not_found"
	tooManyScheduledMsg = "too many scheduled messages"
	zeroDeliverAt       = "0001-01-01T00:00:00Z"
	// beyondMaxDeliverAt is after 9999-12-31T23:59:59Z in UTC, reachable only through an offset.
	beyondMaxDeliverAt = "9999-12-31T23:59:59-01:00"
	missingMessageID   = "00000000-0000-4000-8000-000000000000"
)

// scheduledAppPattern and messageIDPattern guard the SQL below against
// interpolating anything but the test's own generated values.
var (
	scheduledAppPattern = regexp.MustCompile(`^itsch_[0-9a-f]{16}$`)
	messageIDPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type cancelJSON struct {
	MessageID string `json:"message_id"`
	Cancelled bool   `json:"cancelled"`
	Status    string `json:"status"`
}

type scheduledDestinationJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type scheduledMessageJSON struct {
	MessageID      string                     `json:"message_id"`
	AppID          string                     `json:"app_id"`
	Channel        string                     `json:"channel"`
	IdempotencyKey string                     `json:"idempotency_key"`
	DeliverAt      string                     `json:"deliver_at"`
	CreatedAt      string                     `json:"created_at"`
	Recipient      bool                       `json:"recipient"`
	Destinations   []scheduledDestinationJSON `json:"destinations"`
}

type scheduledListResponse struct {
	ScheduledMessages []scheduledMessageJSON `json:"scheduled_messages"`
	Total             int                    `json:"total"`
}

// scheduledFixture is one test's own app, channel and routed webhook destination.
type scheduledFixture struct {
	appID   string
	channel string
}

// newScheduledFixture waits for the stack and creates a unique app's routed
// destination. The message cleanup is registered first so (LIFO) it runs after
// the route and destination cleanups.
func newScheduledFixture(testInstance *testing.T) scheduledFixture {
	testInstance.Helper()
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	fixture := scheduledFixture{appID: scheduledAppPrefix + suffix, channel: "it_" + suffix}
	registerAppMessagesCleanup(testInstance, fixture.appID)
	destinationName := "itest_" + suffix
	createDestination(testInstance, destinationName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + suffix + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:signingSecretLength],
		},
	})
	createRoute(testInstance, fixture.channel, destinationName)
	return fixture
}

// registerAppMessagesCleanup deletes every message of the test's own app (its
// deliveries and attempts cascade).
func registerAppMessagesCleanup(testInstance *testing.T, appID string) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		if !scheduledAppPattern.MatchString(appID) {
			testInstance.Errorf("cleanup: refusing to delete messages for malformed app_id %q", appID)
			return
		}
		psql(testInstance, fmt.Sprintf("delete from messages where app_id = '%s'", appID))
	})
}

// body builds a submit body. deliverAt is omitted when empty; version is the
// payload, so a different version is different content.
func (fixture scheduledFixture) body(idempotencyKey, deliverAt string, version int) map[string]any {
	envelope := map[string]any{
		"app_id":          fixture.appID,
		"channel":         fixture.channel,
		"idempotency_key": idempotencyKey,
		"payload":         map[string]any{"version": version},
	}
	if deliverAt != "" {
		envelope["deliver_at"] = deliverAt
	}
	return envelope
}

// future returns a whole-second UTC instant offset from now, as RFC3339.
func future(offset time.Duration) string {
	return time.Now().UTC().Add(offset).Truncate(time.Second).Format(time.RFC3339)
}

// submit posts a message with the given query string ("" or "?wait=10") and
// returns the HTTP status, the decoded 202 body (zero for other statuses) and the
// raw body.
func (fixture scheduledFixture) submit(
	testInstance *testing.T, query string, envelope map[string]any,
) (int, submitResponse, []byte) {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath+query, envelope)
	var decoded submitResponse
	if status == http.StatusAccepted {
		decodeBody(testInstance, "POST message", body, &decoded)
	}
	return status, decoded, body
}

// mustSubmit is submit requiring 202.
func (fixture scheduledFixture) mustSubmit(
	testInstance *testing.T, label, query string, envelope map[string]any,
) submitResponse {
	testInstance.Helper()
	status, decoded, body := fixture.submit(testInstance, query, envelope)
	requireStatus(testInstance, label, status, http.StatusAccepted, body)
	return decoded
}

// getAs reads message messageID as appID.
func getAs(testInstance *testing.T, appID, messageID string) messageJSON {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("%s/%s?app_id=%s", messagesPath, messageID, appID), nil)
	requireStatus(testInstance, "GET message", status, http.StatusOK, body)
	var decoded messageResponse
	decodeBody(testInstance, "GET message", body, &decoded)
	return decoded.Message
}

// requireScheduledView asserts a message view is scheduled for deliverAt with a
// single scheduled delivery whose next_attempt_at is hidden.
func requireScheduledView(testInstance *testing.T, label string, view messageJSON, deliverAt string) {
	testInstance.Helper()
	if view.Status != statusScheduledName || view.DeliverAt == nil || *view.DeliverAt != deliverAt ||
		view.CancelledAt != nil || len(view.Deliveries) != 1 ||
		view.Deliveries[0].State != statusScheduledName || view.Deliveries[0].NextAttemptAt != nil {
		testInstance.Fatalf("%s: view = %+v, want scheduled for %s with one scheduled delivery and no next_attempt_at",
			label, view, deliverAt)
	}
}

// requireCancelledView asserts a message view is cancelled with cancelled_at set and
// its delivery cancelled, with next_attempt_at hidden.
func requireCancelledView(testInstance *testing.T, label string, view messageJSON) {
	testInstance.Helper()
	if view.Status != statusCancelledName || view.CancelledAt == nil || len(view.Deliveries) != 1 ||
		view.Deliveries[0].State != statusCancelledName || view.Deliveries[0].NextAttemptAt != nil {
		testInstance.Fatalf("%s: view = %+v, want cancelled with cancelled_at and one cancelled delivery", label, view)
	}
	if _, parseErr := time.Parse(time.RFC3339, *view.CancelledAt); parseErr != nil {
		testInstance.Fatalf("%s: cancelled_at = %q is not RFC3339: %v", label, *view.CancelledAt, parseErr)
	}
}

// cancel sends one cancel request (path includes the query) and requires a 200.
func cancel(testInstance *testing.T, label, path string) cancelJSON {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodDelete, path, nil)
	requireStatus(testInstance, label, status, http.StatusOK, body)
	var decoded cancelJSON
	decodeBody(testInstance, label, body, &decoded)
	return decoded
}

func requireCancelOutcome(testInstance *testing.T, label string, got, want cancelJSON) {
	testInstance.Helper()
	if got != want {
		testInstance.Fatalf("%s: cancel outcome = %+v, want %+v", label, got, want)
	}
}

func cancelByIDPath(appID, messageID string) string {
	return fmt.Sprintf("%s/%s?app_id=%s", messagesPath, messageID, appID)
}

func cancelByKeyPath(appID, idempotencyKey string) string {
	return fmt.Sprintf("%s?app_id=%s&idempotency_key=%s", messagesPath, appID, url.QueryEscape(idempotencyKey))
}

// forceRelease makes a scheduled message's deliveries due now, as if its
// deliver_at had passed, so the release poller picks it up.
func forceRelease(testInstance *testing.T, messageID string) {
	testInstance.Helper()
	if !messageIDPattern.MatchString(messageID) {
		testInstance.Fatalf("refusing to release malformed message id %q", messageID)
	}
	psql(testInstance, fmt.Sprintf(
		"update message_deliveries set next_attempt_at = now() - interval '1 second' where message_id = '%s'", messageID))
}

// waitForStatus polls until the message reaches want.
func waitForStatus(testInstance *testing.T, appID, messageID, want string) messageJSON {
	testInstance.Helper()
	deadline := time.Now().Add(releasePollTimeout)
	var last messageJSON
	for time.Now().Before(deadline) {
		last = getAs(testInstance, appID, messageID)
		if last.Status == want {
			return last
		}
		time.Sleep(releasePollInterval)
	}
	testInstance.Fatalf("message %s never reached status %q; last = %+v", messageID, want, last)
	return last
}

func TestScheduledSendLifecycle(testInstance *testing.T) {
	fixture := newScheduledFixture(testInstance)
	suffix := randomSuffix(testInstance)
	firstKey, secondKey := "first-"+suffix, "second-"+suffix
	deliverAt := future(scheduledHorizon)
	replacedDeliverAt := future(scheduledHorizon + time.Hour)

	// A future deliver_at answers 202 at once even with the maximum ?wait.
	started := time.Now()
	created := fixture.mustSubmit(testInstance, "POST scheduled message",
		fmt.Sprintf("?wait=%d", submitWaitSeconds), fixture.body(firstKey, deliverAt, 1))
	if elapsed := time.Since(started); elapsed >= scheduledAnswerBound {
		testInstance.Fatalf("scheduled submit with ?wait=%d took %v, want an immediate answer", submitWaitSeconds, elapsed)
	}
	if created.Duplicate || created.Replaced {
		testInstance.Fatalf("first submit = (duplicate %v, replaced %v), want (false, false)", created.Duplicate, created.Replaced)
	}
	messageID := created.Message.MessageID
	requireScheduledView(testInstance, "POST scheduled message", created.Message, deliverAt)

	// Read it back by id and by key.
	requireScheduledView(testInstance, "GET by id", getAs(testInstance, fixture.appID, messageID), deliverAt)
	status, body := apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("%s?app_id=%s&idempotency_key=%s", messagesPath, fixture.appID, firstKey), nil)
	requireStatus(testInstance, "GET by key", status, http.StatusOK, body)
	var byKey messageResponse
	decodeBody(testInstance, "GET by key", body, &byKey)
	if byKey.Message.MessageID != messageID {
		testInstance.Fatalf("lookup by key id = %q, want %q", byKey.Message.MessageID, messageID)
	}

	// An identical retry is a duplicate, not a replace.
	retried := fixture.mustSubmit(testInstance, "POST identical retry", "", fixture.body(firstKey, deliverAt, 1))
	if !retried.Duplicate || retried.Replaced || retried.Message.MessageID != messageID {
		testInstance.Fatalf("identical retry = %+v, want a duplicate of %s", retried, messageID)
	}

	// Different content under the same key replaces the message in place.
	replaced := fixture.mustSubmit(testInstance, "POST replace", "", fixture.body(firstKey, replacedDeliverAt, 2))
	if replaced.Duplicate || !replaced.Replaced || replaced.Message.MessageID != messageID {
		testInstance.Fatalf("replace = %+v, want replaced with the original id %s", replaced, messageID)
	}
	requireScheduledView(testInstance, "POST replace", replaced.Message, replacedDeliverAt)
	scheduledDeliveryID := replaced.Message.Deliveries[0].ID

	// Scheduled rows never appear in the Delivery Log, and its state filter refuses them.
	status, body = apiRequest(testInstance, http.MethodGet, "/v1/admin/deliveries?state=pending&limit=200", nil)
	requireStatus(testInstance, "GET pending deliveries", status, http.StatusOK, body)
	var pending deliveriesResponse
	decodeBody(testInstance, "GET pending deliveries", body, &pending)
	if _, found := findAdminDelivery(pending.Deliveries, scheduledDeliveryID); found {
		testInstance.Fatalf("scheduled delivery %d appears in the pending list", scheduledDeliveryID)
	}
	for _, state := range []string{statusScheduledName, statusCancelledName} {
		status, body = apiRequest(testInstance, http.MethodGet, "/v1/admin/deliveries?state="+state, nil)
		requireStatus(testInstance, "GET deliveries state="+state, status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "GET deliveries state="+state, body, "invalid state")
	}

	// Another app's id cannot cancel it: it is simply not found.
	requireCancelOutcome(testInstance, "cancel as another app",
		cancel(testInstance, "DELETE as another app", cancelByIDPath("someotherapp", messageID)),
		cancelJSON{MessageID: messageID, Cancelled: false, Status: statusNotFoundName})
	requireScheduledView(testInstance, "after the foreign cancel", getAs(testInstance, fixture.appID, messageID), replacedDeliverAt)

	// Cancel by id; a repeat is idempotent.
	wantCancelled := cancelJSON{MessageID: messageID, Cancelled: true, Status: statusCancelledName}
	requireCancelOutcome(testInstance, "cancel by id",
		cancel(testInstance, "DELETE by id", cancelByIDPath(fixture.appID, messageID)), wantCancelled)
	requireCancelledView(testInstance, "after cancel", getAs(testInstance, fixture.appID, messageID))
	requireCancelOutcome(testInstance, "repeat cancel by id",
		cancel(testInstance, "DELETE by id again", cancelByIDPath(fixture.appID, messageID)), wantCancelled)

	// A cancelled message stays cancelled: re-posting its key ignores the content.
	reposted := fixture.mustSubmit(testInstance, "POST after cancel", "", fixture.body(firstKey, deliverAt, 3))
	if !reposted.Duplicate || reposted.Replaced || reposted.Message.Status != statusCancelledName ||
		reposted.Message.MessageID != messageID {
		testInstance.Fatalf("re-post after cancel = %+v, want a duplicate of the cancelled message", reposted)
	}

	// Replaying a cancelled delivery is refused.
	status, body = apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("/v1/admin/deliveries/%d/replay", scheduledDeliveryID), nil)
	requireStatus(testInstance, "POST replay of a cancelled delivery", status, http.StatusConflict, body)

	// Cancel by key on a second message, and the by-key misses.
	second := fixture.mustSubmit(testInstance, "POST second message", "", fixture.body(secondKey, deliverAt, 1))
	requireCancelOutcome(testInstance, "cancel by key",
		cancel(testInstance, "DELETE by key", cancelByKeyPath(fixture.appID, secondKey)),
		cancelJSON{MessageID: second.Message.MessageID, Cancelled: true, Status: statusCancelledName})
	requireCancelOutcome(testInstance, "cancel by an unknown key",
		cancel(testInstance, "DELETE by unknown key", cancelByKeyPath(fixture.appID, "unknown-"+suffix)),
		cancelJSON{MessageID: "", Cancelled: false, Status: statusNotFoundName})
	requireCancelOutcome(testInstance, "cancel an unknown id",
		cancel(testInstance, "DELETE unknown id", cancelByIDPath(fixture.appID, missingMessageID)),
		cancelJSON{MessageID: missingMessageID, Cancelled: false, Status: statusNotFoundName})
	requireCancelOutcome(testInstance, "cancel a non-UUID id",
		cancel(testInstance, "DELETE non-UUID id", cancelByIDPath(fixture.appID, "not-a-uuid")),
		cancelJSON{MessageID: "not-a-uuid", Cancelled: false, Status: statusNotFoundName})

	assertCancelRejections(testInstance, fixture, messageID, firstKey)
	assertDeliverAtRejections(testInstance, fixture, suffix)
}

// assertCancelRejections covers the 400s of both cancel forms.
func assertCancelRejections(testInstance *testing.T, fixture scheduledFixture, messageID, idempotencyKey string) {
	testInstance.Helper()
	testCases := []struct {
		name    string
		path    string
		wantMsg string
	}{
		{name: "by id without app_id", path: fmt.Sprintf("%s/%s", messagesPath, messageID), wantMsg: "app_id is required"},
		{
			name: "by id with a malformed app_id", path: cancelByIDPath("BAD%20APP", messageID),
			wantMsg: "invalid app_id",
		},
		{name: "by key without anything", path: messagesPath, wantMsg: "app_id and idempotency_key are required"},
		{
			name: "by key without a key", path: fmt.Sprintf("%s?app_id=%s", messagesPath, fixture.appID),
			wantMsg: "app_id and idempotency_key are required",
		},
		{
			name: "by key with a malformed app_id", path: cancelByKeyPath("BAD%20APP", idempotencyKey),
			wantMsg: "invalid app_id",
		},
		{
			name: "by key with a malformed key",
			path: fmt.Sprintf("%s?app_id=%s&idempotency_key=bad%%20key", messagesPath, fixture.appID), wantMsg: "invalid idempotency_key",
		},
	}
	for _, testCase := range testCases {
		status, body := apiRequest(testInstance, http.MethodDelete, testCase.path, nil)
		requireStatus(testInstance, "DELETE "+testCase.name, status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "DELETE "+testCase.name, body, testCase.wantMsg)
	}
}

// assertDeliverAtRejections covers the two refused deliver_at values; neither
// stores a message.
func assertDeliverAtRejections(testInstance *testing.T, fixture scheduledFixture, suffix string) {
	testInstance.Helper()
	for index, deliverAt := range []string{zeroDeliverAt, beyondMaxDeliverAt} {
		status, _, body := fixture.submit(testInstance, "", fixture.body("bad-"+strconv.Itoa(index)+"-"+suffix, deliverAt, 1))
		requireStatus(testInstance, "POST deliver_at "+deliverAt, status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "POST deliver_at "+deliverAt, body, "invalid deliver_at")
	}
}

func TestScheduledReplaceToAnImmediateSendHonoursWait(testInstance *testing.T) {
	fixture := newScheduledFixture(testInstance)
	key := "replace-" + randomSuffix(testInstance)

	created := fixture.mustSubmit(testInstance, "POST scheduled message", "", fixture.body(key, future(scheduledHorizon), 1))
	messageID := created.Message.MessageID

	// Dropping deliver_at releases the message at once; ?wait= then waits for the
	// first attempt, which the production transport refuses (blocked_address).
	replaced := fixture.mustSubmit(testInstance, "POST replace to immediate",
		fmt.Sprintf("?wait=%d", submitWaitSeconds), fixture.body(key, "", 2))
	if !replaced.Replaced || replaced.Duplicate || replaced.Message.MessageID != messageID {
		testInstance.Fatalf("replace = %+v, want replaced with the original id %s", replaced, messageID)
	}
	if replaced.Message.Status != deadStateName || len(replaced.Message.Deliveries) != 1 ||
		replaced.Message.Deliveries[0].AttemptCount != 1 {
		testInstance.Fatalf("replace to immediate after wait = %+v, want dead after 1 attempt", replaced.Message)
	}
}

func TestAdminScheduledListAndCancel(testInstance *testing.T) {
	fixture := newScheduledFixture(testInstance)
	suffix := randomSuffix(testInstance)
	laterKey, soonerKey := "later-"+suffix, "sooner-"+suffix

	later := fixture.mustSubmit(testInstance, "POST later message", "", fixture.body(laterKey, future(scheduledHorizon+2*time.Hour), 1))
	sooner := fixture.mustSubmit(testInstance, "POST sooner message", "", fixture.body(soonerKey, future(scheduledHorizon+time.Hour), 1))

	// The list is soonest first, carries destinations and the total, and exposes no
	// person hash.
	list := listScheduled(testInstance, "limit=200")
	laterIndex, soonerIndex := -1, -1
	for index, entry := range list.ScheduledMessages {
		switch entry.MessageID {
		case later.Message.MessageID:
			laterIndex = index
		case sooner.Message.MessageID:
			soonerIndex = index
		}
		if index > 0 {
			previous := parseRFC3339(testInstance, list.ScheduledMessages[index-1].DeliverAt)
			if parseRFC3339(testInstance, entry.DeliverAt).Before(previous) {
				testInstance.Fatalf("list not ordered by deliver_at: %q before %q", list.ScheduledMessages[index-1].DeliverAt, entry.DeliverAt)
			}
		}
	}
	if laterIndex < 0 || soonerIndex < 0 || soonerIndex > laterIndex {
		testInstance.Fatalf("list indexes (sooner %d, later %d), want both present with sooner first", soonerIndex, laterIndex)
	}
	if list.Total < 2 || list.Total < len(list.ScheduledMessages) {
		testInstance.Fatalf("total = %d with %d listed, want at least both of this test's messages", list.Total, len(list.ScheduledMessages))
	}
	entry := list.ScheduledMessages[soonerIndex]
	if entry.AppID != fixture.appID || entry.Channel != fixture.channel || entry.IdempotencyKey != soonerKey ||
		entry.Recipient || len(entry.Destinations) != 1 || entry.Destinations[0].Type != registry.TypeWebhook ||
		entry.Destinations[0].Name == "" {
		testInstance.Fatalf("list entry = %+v, want this test's webhook message", entry)
	}
	for _, rawQuery := range []string{"limit=0", "limit=201", "limit=abc"} {
		status, body := apiRequest(testInstance, http.MethodGet, "/v1/admin/scheduled-messages?"+rawQuery, nil)
		requireStatus(testInstance, "GET scheduled-messages?"+rawQuery, status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "GET scheduled-messages?"+rawQuery, body, "invalid limit")
	}

	// Admin cancel needs no app_id, and a repeat is idempotent.
	adminPath := func(messageID string) string { return "/v1/admin/scheduled-messages/" + messageID }
	wantCancelled := cancelJSON{MessageID: later.Message.MessageID, Cancelled: true, Status: statusCancelledName}
	requireCancelOutcome(testInstance, "admin cancel",
		cancel(testInstance, "DELETE admin cancel", adminPath(later.Message.MessageID)), wantCancelled)
	requireCancelOutcome(testInstance, "repeat admin cancel",
		cancel(testInstance, "DELETE admin cancel again", adminPath(later.Message.MessageID)), wantCancelled)
	requireCancelOutcome(testInstance, "admin cancel of an unknown id",
		cancel(testInstance, "DELETE admin unknown", adminPath(missingMessageID)),
		cancelJSON{MessageID: missingMessageID, Cancelled: false, Status: statusNotFoundName})
	if _, listed := findScheduled(listScheduled(testInstance, "limit=200"), later.Message.MessageID); listed {
		testInstance.Fatalf("cancelled message %s is still listed", later.Message.MessageID)
	}

	// A released message can no longer be cancelled, by either path, and leaves the list.
	forceRelease(testInstance, sooner.Message.MessageID)
	released := waitForStatus(testInstance, fixture.appID, sooner.Message.MessageID, deadStateName)
	wantReleased := cancelJSON{MessageID: sooner.Message.MessageID, Cancelled: false, Status: released.Status}
	requireCancelOutcome(testInstance, "admin cancel of a released message",
		cancel(testInstance, "DELETE admin released", adminPath(sooner.Message.MessageID)), wantReleased)
	requireCancelOutcome(testInstance, "app cancel of a released message",
		cancel(testInstance, "DELETE app released", cancelByIDPath(fixture.appID, sooner.Message.MessageID)), wantReleased)
	if _, listed := findScheduled(listScheduled(testInstance, "limit=200"), sooner.Message.MessageID); listed {
		testInstance.Fatalf("released message %s is still listed", sooner.Message.MessageID)
	}
}

func parseRFC3339(testInstance *testing.T, value string) time.Time {
	testInstance.Helper()
	parsed, parseErr := time.Parse(time.RFC3339Nano, value)
	if parseErr != nil {
		testInstance.Fatalf("%q is not RFC3339: %v", value, parseErr)
	}
	return parsed
}

func listScheduled(testInstance *testing.T, rawQuery string) scheduledListResponse {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodGet, "/v1/admin/scheduled-messages?"+rawQuery, nil)
	requireStatus(testInstance, "GET scheduled-messages", status, http.StatusOK, body)
	var decoded scheduledListResponse
	decodeBody(testInstance, "GET scheduled-messages", body, &decoded)
	return decoded
}

func findScheduled(list scheduledListResponse, messageID string) (scheduledMessageJSON, bool) {
	for _, candidate := range list.ScheduledMessages {
		if candidate.MessageID == messageID {
			return candidate, true
		}
	}
	return scheduledMessageJSON{}, false
}

// TestScheduledSubmitOverTheCapIs429 needs a stack started with a small
// DELIVERY_MAX_SCHEDULED_PER_APP (for example `DELIVERY_MAX_SCHEDULED_PER_APP=2
// make local-up`); on the default stack (cap 1000) it skips.
func TestScheduledSubmitOverTheCapIs429(testInstance *testing.T) {
	capText := os.Getenv(maxScheduledEnv)
	capValue, parseErr := strconv.Atoi(capText)
	if parseErr != nil || capValue < minCapForLiveTest || capValue > maxCapForLiveTest {
		testInstance.Skipf("%s=%q: needs a stack with %d <= DELIVERY_MAX_SCHEDULED_PER_APP <= %d (e.g. "+
			"`DELIVERY_MAX_SCHEDULED_PER_APP=2 make local-up`), then rerun",
			maxScheduledEnv, capText, minCapForLiveTest, maxCapForLiveTest)
	}
	fixture := newScheduledFixture(testInstance)
	suffix := randomSuffix(testInstance)
	deliverAt := future(scheduledHorizon)

	for index := range capValue {
		created := fixture.mustSubmit(testInstance, fmt.Sprintf("POST scheduled message %d", index+1), "",
			fixture.body(fmt.Sprintf("cap-%d-%s", index, suffix), deliverAt, 1))
		requireScheduledView(testInstance, fmt.Sprintf("message %d", index+1), created.Message, deliverAt)
	}

	// One over the cap is refused and stores nothing.
	overKey := "cap-over-" + suffix
	status, _, body := fixture.submit(testInstance, "", fixture.body(overKey, deliverAt, 1))
	requireStatus(testInstance, "POST over the cap", status, http.StatusTooManyRequests, body)
	requireErrorMessage(testInstance, "POST over the cap", body, tooManyScheduledMsg)
	status, body = apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("%s?app_id=%s&idempotency_key=%s", messagesPath, fixture.appID, overKey), nil)
	requireStatus(testInstance, "GET the refused message", status, http.StatusNotFound, body)

	// A replace does not change the count, so it still succeeds at the cap.
	replaced := fixture.mustSubmit(testInstance, "POST replace at the cap", "",
		fixture.body(fmt.Sprintf("cap-0-%s", suffix), deliverAt, 2))
	if !replaced.Replaced {
		testInstance.Fatalf("replace at the cap = %+v, want replaced", replaced)
	}

	// Cancelling frees a slot.
	requireCancelOutcome(testInstance, "cancel to free a slot",
		cancel(testInstance, "DELETE to free a slot", cancelByKeyPath(fixture.appID, fmt.Sprintf("cap-0-%s", suffix))),
		cancelJSON{MessageID: replaced.Message.MessageID, Cancelled: true, Status: statusCancelledName})
	fixture.mustSubmit(testInstance, "POST after a slot is freed", "", fixture.body(overKey, deliverAt, 1))
}

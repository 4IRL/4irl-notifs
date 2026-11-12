//go:build integration

package integration

// Live-stack tests of holding an unrouted app message (DELIVERY_HOLD_UNROUTED, on by default). They
// need a holding stack, so every test starts with requireHoldStack: with
// NOTIFS_DELIVERY_HOLD_UNROUTED=0 (the explicit flag-off run) it skips with a reason; unset or 1 it
// verifies the stack really holds (the container's environment when NOTIFS_DELIVERY_CONTAINER is
// set, plus a behavioural probe that an unrouted app-key submit answers 202, not 422) and fails
// loudly on a mismatch, never skipping. Run them with `make delivery-integration-test` (the cap
// tests skip there) or `make delivery-integration-test-hold` (small caps).
//
// Three passes exist: the plain run (`make delivery-integration-test`, the default flag-on stack, cap
// tests skipped), the hold run with low caps (`make delivery-integration-test-hold`),
// and the hold-off run (`make delivery-integration-test-hold-off`) for the tests whose names end in
// WhenHoldOff, which pin the 422 refusal and call requireHoldOffStack. Those names must not start
// with TestHold, so `-run '^TestHold'` never picks them up.
//
// Isolation: each case owns a unique app_id (and so its own dak_ key) and cancels every message it
// holds in t.Cleanup, then deletes the app's message rows, so a rerun on a reused stack and the
// case order cannot consume or poison the per-app cap. The cap case fills a dedicated app to the
// forwarded NOTIFS_DELIVERY_MAX_HELD.

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	// holdFlagEnv carries the stack's DELIVERY_HOLD_UNROUTED as the Makefile or CI forwards it: unset
	// or 1 for the default holding stack, 0 for the flag-off pass.
	holdFlagEnv = "NOTIFS_DELIVERY_HOLD_UNROUTED"
	// holdMaxHeldEnv carries the stack's DELIVERY_MAX_HELD_PER_APP, forwarded by the Makefile.
	holdMaxHeldEnv = "NOTIFS_DELIVERY_MAX_HELD"
	// holdContainerEnv is the delivery-api container id; unset in CI, where only the probe runs.
	holdContainerEnv = "NOTIFS_DELIVERY_CONTAINER"
	// holdMaxHeldTotalEnv carries the stack's DELIVERY_MAX_HELD_TOTAL, forwarded by the Makefile.
	holdMaxHeldTotalEnv = "NOTIFS_DELIVERY_MAX_HELD_TOTAL"
	// holdMaxCapForLiveTest is the largest cap the live cap test will fill.
	holdMaxCapForLiveTest = 10
	// holdMaxTotalCapForLiveTest is the largest total cap the live total cap test will fill.
	holdMaxTotalCapForLiveTest = 50

	holdAppPrefix = "ithold_"
	// holdProbeApp is the app the start-of-test probe submits under.
	holdProbeApp = "itholdprobe"

	statusUnroutedName = "unrouted"
	tooManyHeldMsg     = "too many held messages"
	// heldCapacityReachedMsg is the service-wide held cap refusal.
	heldCapacityReachedMsg = "held message capacity reached"
	// holdAnswerBound is the longest a held submit may take even with ?wait=5: anything near the
	// wait would mean the wait was not skipped.
	holdAnswerBound = 3 * time.Second
	holdWaitSeconds = 5
)

// holdAppPattern guards the cleanup SQL below against interpolating anything but a generated
// app_id.
var holdAppPattern = regexp.MustCompile(`^(ithold_[0-9a-f]{16}|itholdprobe)$`)

// requireHoldStack skips only on an explicit flag-off run ("0") and otherwise proves the stack holds
// unrouted messages. Unset means the default stack, which holds. A mismatch (for example a stale
// flag-off stack with the variable unset) fails the test; it never results in a skip.
func requireHoldStack(testInstance *testing.T) {
	testInstance.Helper()
	switch flag := os.Getenv(holdFlagEnv); flag {
	case "0":
		testInstance.Skipf("%s=0: the stack holds nothing on purpose; the flag-off path is "+
			"`make delivery-integration-test-hold-off`", holdFlagEnv)
	case "", "1":
	default:
		testInstance.Fatalf("%s=%q: only 0, 1 or unset is valid", holdFlagEnv, flag)
	}
	waitForReady(testInstance)
	if container := os.Getenv(holdContainerEnv); container != "" {
		requireContainerHoldsFlag(testInstance, container)
	}
	probeHoldStack(testInstance)
}

// requireContainerHoldsFlag fails unless the delivery-api container's own environment has
// DELIVERY_HOLD_UNROUTED on.
func requireContainerHoldsFlag(testInstance *testing.T, container string) {
	testInstance.Helper()
	output, runErr := execRunner("docker", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", container)
	if runErr != nil {
		testInstance.Fatalf("%s (unset or 1) but docker inspect of %s failed: %v", holdFlagEnv, container, runErr)
	}
	for _, line := range strings.Split(string(output), "\n") {
		switch strings.TrimSpace(line) {
		case "DELIVERY_HOLD_UNROUTED=1", "DELIVERY_HOLD_UNROUTED=true":
			return
		}
	}
	testInstance.Fatalf("%s (unset or 1) but container %s does not have DELIVERY_HOLD_UNROUTED=1: recreate the stack "+
		"with the flag on (make delivery-integration-test-hold does)", holdFlagEnv, container)
}

// probeHoldStack submits an unrouted app-key message and requires 202 (a flag-off stack answers
// 422), then cancels it.
func probeHoldStack(testInstance *testing.T) {
	testInstance.Helper()
	probe := newHoldApp(testInstance, holdProbeApp)
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath,
		probe.body("probe-"+randomSuffix(testInstance), "it_probe_"+randomSuffix(testInstance)))
	if status != http.StatusAccepted {
		testInstance.Fatalf("%s (unset or 1) but an unrouted app-key submit answered %d (body: %s), want 202: "+
			"the stack was not recreated with DELIVERY_HOLD_UNROUTED=1", holdFlagEnv, status, body)
	}
	var decoded submitResponse
	decodeBody(testInstance, "hold probe", body, &decoded)
	probe.track(decoded.Message.MessageID)
	// A flag-off stack with a catch-all route also answers 202, so require the held status too.
	requireUnroutedView(testInstance, "hold probe", decoded.Message)
}

// requireHoldOffStack skips unless the forwarded flag is exactly "0" and otherwise proves the stack
// refuses unrouted messages. A mismatch fails the test; flag=0 never results in a skip.
func requireHoldOffStack(testInstance *testing.T) {
	testInstance.Helper()
	if os.Getenv(holdFlagEnv) != "0" {
		testInstance.Skipf("%s is not 0: needs a stack with DELIVERY_HOLD_UNROUTED=0 "+
			"(run make delivery-integration-test-hold-off)", holdFlagEnv)
	}
	waitForReady(testInstance)
	if container := os.Getenv(holdContainerEnv); container != "" {
		requireContainerHoldFlagOff(testInstance, container)
	}
	probeHoldOffStack(testInstance)
}

// requireContainerHoldFlagOff fails unless the delivery-api container's own environment has
// DELIVERY_HOLD_UNROUTED=0 or false.
func requireContainerHoldFlagOff(testInstance *testing.T, container string) {
	testInstance.Helper()
	output, runErr := execRunner("docker", "inspect", "-f", "{{range .Config.Env}}{{println .}}{{end}}", container)
	if runErr != nil {
		testInstance.Fatalf("%s=0 but docker inspect of %s failed: %v", holdFlagEnv, container, runErr)
	}
	for _, line := range strings.Split(string(output), "\n") {
		switch strings.TrimSpace(line) {
		case "DELIVERY_HOLD_UNROUTED=0", "DELIVERY_HOLD_UNROUTED=false":
			return
		}
	}
	testInstance.Fatalf("%s=0 but container %s does not have DELIVERY_HOLD_UNROUTED=0: recreate the stack "+
		"with the flag off (make delivery-integration-test-hold-off does)", holdFlagEnv, container)
}

// probeHoldOffStack submits an unrouted app-key message and requires 422 `no destination matched`.
// A 202 means the stack holds: the probe message is cancelled and the test fails, never skips.
func probeHoldOffStack(testInstance *testing.T) {
	testInstance.Helper()
	probe := newHoldApp(testInstance, holdProbeApp)
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath,
		probe.body("probe-off-"+randomSuffix(testInstance), "it_probe_"+randomSuffix(testInstance)))
	if status == http.StatusAccepted {
		var decoded submitResponse
		decodeBody(testInstance, "hold-off probe", body, &decoded)
		probe.track(decoded.Message.MessageID)
	}
	if status != http.StatusUnprocessableEntity {
		testInstance.Fatalf("%s=0 but an unrouted app-key submit answered %d (body: %s), want 422: "+
			"the stack was not recreated with DELIVERY_HOLD_UNROUTED=0", holdFlagEnv, status, body)
	}
	requireErrorMessage(testInstance, "hold-off probe", body, "no destination matched")
}

// holdApp is one test's own app: its key, and the messages it holds so cleanup cancels them.
type holdApp struct {
	appID string
	held  []string
}

// newHoldApp binds appID (issuing its key) and registers the cleanup that cancels every held
// message and deletes the app's message rows. The key's own cleanup, registered first by appKeyFor,
// runs after this one.
func newHoldApp(testInstance *testing.T, appID string) *holdApp {
	testInstance.Helper()
	if !holdAppPattern.MatchString(appID) {
		testInstance.Fatalf("hold app id %q has the wrong shape", appID)
	}
	appKeyFor(testInstance, appID)
	app := &holdApp{appID: appID}
	testInstance.Cleanup(func() {
		for _, messageID := range app.held {
			status, body := apiRequest(testInstance, http.MethodDelete, cancelByIDPath(appID, messageID), nil)
			if status != http.StatusOK {
				testInstance.Errorf("cleanup: cancel held message %s status = %d, body: %s", messageID, status, body)
			}
		}
		psql(testInstance, fmt.Sprintf("delete from messages where app_id = '%s'", appID))
	})
	return app
}

func newUniqueHoldApp(testInstance *testing.T) *holdApp {
	testInstance.Helper()
	return newHoldApp(testInstance, holdAppPrefix+randomSuffix(testInstance))
}

// track remembers a held message for cleanup.
func (app *holdApp) track(messageID string) {
	app.held = append(app.held, messageID)
}

func (app *holdApp) body(idempotencyKey, channel string) map[string]any {
	return map[string]any{
		"app_id":          app.appID,
		"channel":         channel,
		"idempotency_key": idempotencyKey,
		"payload":         map[string]any{"n": 1},
	}
}

// submitHeld posts an unrouted message, requires 202 `unrouted` with no deliveries and tracks it
// for cleanup. query is "" or "?wait=5".
func (app *holdApp) submitHeld(testInstance *testing.T, label, query string, envelope map[string]any) submitResponse {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath+query, envelope)
	requireStatus(testInstance, label, status, http.StatusAccepted, body)
	var decoded submitResponse
	decodeBody(testInstance, label, body, &decoded)
	app.track(decoded.Message.MessageID)
	requireUnroutedView(testInstance, label, decoded.Message)
	return decoded
}

func requireUnroutedView(testInstance *testing.T, label string, view messageJSON) {
	testInstance.Helper()
	if view.Status != statusUnroutedName || len(view.Deliveries) != 0 || view.CancelledAt != nil {
		testInstance.Fatalf("%s: view = %+v, want status %s with no deliveries and no cancelled_at",
			label, view, statusUnroutedName)
	}
}

// heldCount counts the app's held, not-cancelled messages.
func heldCount(testInstance *testing.T, appID string) int {
	testInstance.Helper()
	if !holdAppPattern.MatchString(appID) {
		testInstance.Fatalf("refusing to count held messages for malformed app_id %q", appID)
	}
	text := psql(testInstance, fmt.Sprintf(
		"select count(*) from messages where app_id = '%s' and held_at is not null and held_cancelled_at is null", appID))
	count, parseErr := strconv.Atoi(strings.TrimSpace(text))
	if parseErr != nil {
		testInstance.Fatalf("held count %q is not a number: %v", text, parseErr)
	}
	return count
}

func TestHoldUnroutedSubmitIsAcceptedAsUnrouted(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	channel := "it_" + randomSuffix(testInstance)

	created := app.submitHeld(testInstance, "POST unrouted message", "", app.body("held-"+randomSuffix(testInstance), channel))
	if created.Duplicate || created.Replaced {
		testInstance.Fatalf("first submit = (duplicate %v, replaced %v), want (false, false)", created.Duplicate, created.Replaced)
	}
	requireUnroutedView(testInstance, "GET by id", getAs(testInstance, app.appID, created.Message.MessageID))
}

func TestHoldNamedDestinationErrorsStillRefuse(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	suffix := randomSuffix(testInstance)

	unknown := app.body("held-unknown-"+suffix, "it_"+suffix)
	unknown["to"] = map[string]string{"destination": "itest_absent_" + suffix}
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath, unknown)
	requireStatus(testInstance, "POST to an unknown destination", status, http.StatusBadRequest, body)

	disabledName := "itest_" + suffix
	createDestination(testInstance, disabledName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + suffix + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:signingSecretLength],
		},
	})
	patchStatus, patchBody := adminRequest(testInstance, http.MethodPatch, "/destinations/"+disabledName,
		map[string]any{"enabled": false})
	requireStatus(testInstance, "PATCH destination disabled", patchStatus, http.StatusOK, patchBody)
	disabled := app.body("held-disabled-"+suffix, "it_"+suffix)
	disabled["to"] = map[string]string{"destination": disabledName}
	status, body = apiRequest(testInstance, http.MethodPost, messagesPath, disabled)
	requireStatus(testInstance, "POST to a disabled destination", status, http.StatusConflict, body)
	requireErrorMessage(testInstance, "POST to a disabled destination", body, "destination disabled")
	if count := heldCount(testInstance, app.appID); count != 0 {
		testInstance.Fatalf("held count after the named-destination refusals = %d, want 0", count)
	}
}

func TestHoldOperatorSendTestStillRefusesAnUnroutedChannel(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	status, body := adminRequest(testInstance, http.MethodPost, "/messages",
		app.body("held-admin-"+randomSuffix(testInstance), "it_"+randomSuffix(testInstance)))
	requireStatus(testInstance, "POST /v1/admin/messages with no route", status, http.StatusUnprocessableEntity, body)
	requireErrorMessage(testInstance, "POST /v1/admin/messages with no route", body, "no destination matched")
	if count := heldCount(testInstance, app.appID); count != 0 {
		testInstance.Fatalf("held count after the operator send-test = %d, want 0", count)
	}
}

func TestHoldWaitReturnsImmediately(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	started := time.Now()
	app.submitHeld(testInstance, "POST unrouted with ?wait", fmt.Sprintf("?wait=%d", holdWaitSeconds),
		app.body("held-wait-"+randomSuffix(testInstance), "it_"+randomSuffix(testInstance)))
	if elapsed := time.Since(started); elapsed >= holdAnswerBound {
		testInstance.Fatalf("held submit with ?wait=%d took %v, want an immediate answer", holdWaitSeconds, elapsed)
	}
}

func TestHoldSameKeyReplayReturnsTheStoredMessage(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	key := "held-replay-" + randomSuffix(testInstance)
	channel := "it_" + randomSuffix(testInstance)

	created := app.submitHeld(testInstance, "POST unrouted message", "", app.body(key, channel))
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath, app.body(key, channel))
	requireStatus(testInstance, "POST identical replay", status, http.StatusAccepted, body)
	var replayed submitResponse
	decodeBody(testInstance, "POST identical replay", body, &replayed)
	if !replayed.Duplicate || replayed.Replaced || replayed.Message.MessageID != created.Message.MessageID {
		testInstance.Fatalf("replay = %+v, want a duplicate of %s", replayed, created.Message.MessageID)
	}
	requireUnroutedView(testInstance, "replay", replayed.Message)
	if count := heldCount(testInstance, app.appID); count != 1 {
		testInstance.Fatalf("held count after a replay = %d, want 1", count)
	}
}

func TestHoldCancelOfAnUnscheduledHeldMessage(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	created := app.submitHeld(testInstance, "POST unrouted message", "",
		app.body("held-cancel-"+randomSuffix(testInstance), "it_"+randomSuffix(testInstance)))

	wantCancelled := cancelJSON{MessageID: created.Message.MessageID, Cancelled: true, Status: statusCancelledName}
	requireCancelOutcome(testInstance, "cancel a held message",
		cancel(testInstance, "DELETE held message", cancelByIDPath(app.appID, created.Message.MessageID)), wantCancelled)
	if view := getAs(testInstance, app.appID, created.Message.MessageID); view.Status != statusCancelledName || view.CancelledAt == nil {
		testInstance.Fatalf("held message after cancel = %+v, want cancelled with cancelled_at", view)
	}
	if count := heldCount(testInstance, app.appID); count != 0 {
		testInstance.Fatalf("held count after cancel = %d, want 0", count)
	}
}

func TestHoldBodyHoldFieldIsRefused(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	suffix := randomSuffix(testInstance)
	for _, route := range []struct {
		name string
		send func(envelope map[string]any) (int, []byte)
	}{
		{"POST /v1/messages", func(envelope map[string]any) (int, []byte) {
			return apiRequest(testInstance, http.MethodPost, messagesPath, envelope)
		}},
		{"POST /v1/admin/messages", func(envelope map[string]any) (int, []byte) {
			return adminRequest(testInstance, http.MethodPost, "/messages", envelope)
		}},
	} {
		envelope := app.body("held-field-"+suffix, "it_"+suffix)
		envelope["hold"] = true
		status, body := route.send(envelope)
		requireStatus(testInstance, route.name+" with a hold field", status, http.StatusBadRequest, body)
	}
	if count := heldCount(testInstance, app.appID); count != 0 {
		testInstance.Fatalf("held count after the refused hold fields = %d, want 0", count)
	}
}

// TestHoldPerAppCapIs429 fills a dedicated app to the stack's cap (forwarded as
// NOTIFS_DELIVERY_MAX_HELD, which must equal DELIVERY_MAX_HELD_PER_APP) and expects the next
// message to be refused with 429.
func TestHoldPerAppCapIs429(testInstance *testing.T) {
	requireHoldStack(testInstance)
	capValue := holdCapFromEnv(testInstance, holdMaxHeldEnv, holdMaxCapForLiveTest)
	app := newUniqueHoldApp(testInstance)
	suffix := randomSuffix(testInstance)
	channel := "it_" + suffix

	var firstID string
	for index := range capValue {
		created := app.submitHeld(testInstance, fmt.Sprintf("POST held message %d", index+1), "",
			app.body(fmt.Sprintf("held-cap-%d-%s", index, suffix), channel))
		if index == 0 {
			firstID = created.Message.MessageID
		}
	}

	overKey := "held-cap-over-" + suffix
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath, app.body(overKey, channel))
	requireStatus(testInstance, "POST over the held cap", status, http.StatusTooManyRequests, body)
	requireErrorMessage(testInstance, "POST over the held cap", body, tooManyHeldMsg)

	// A same-key replay does not count against the cap, and cancelling frees a slot.
	replayStatus, replayBody := apiRequest(testInstance, http.MethodPost, messagesPath,
		app.body(fmt.Sprintf("held-cap-0-%s", suffix), channel))
	requireStatus(testInstance, "POST replay at the cap", replayStatus, http.StatusAccepted, replayBody)
	requireCancelOutcome(testInstance, "cancel to free a slot",
		cancel(testInstance, "DELETE to free a slot", cancelByIDPath(app.appID, firstID)),
		cancelJSON{MessageID: firstID, Cancelled: true, Status: statusCancelledName})
	app.submitHeld(testInstance, "POST after a slot is freed", "", app.body(overKey, channel))
}

// TestHoldTotalCapIs429 fills the service-wide held cap (forwarded as
// NOTIFS_DELIVERY_MAX_HELD_TOTAL, which must equal DELIVERY_MAX_HELD_TOTAL) across several apps,
// each under the per-app cap, and expects the next message from a fresh app to be refused with 429.
func TestHoldTotalCapIs429(testInstance *testing.T) {
	requireHoldStack(testInstance)
	perApp := holdCapFromEnv(testInstance, holdMaxHeldEnv, holdMaxCapForLiveTest)
	total := holdCapFromEnv(testInstance, holdMaxHeldTotalEnv, holdMaxTotalCapForLiveTest)
	suffix := randomSuffix(testInstance)
	channel := "it_total_" + suffix

	alreadyHeld, parseErr := strconv.Atoi(psql(testInstance, "SELECT count(*) FROM messages WHERE held_at IS NOT NULL"))
	if parseErr != nil || alreadyHeld > total {
		testInstance.Fatalf("held rows before the test = %d (parse error %v), want at most the total cap %d",
			alreadyHeld, parseErr, total)
	}

	remaining := total - alreadyHeld
	for appIndex := 0; remaining > 0; appIndex++ {
		app := newUniqueHoldApp(testInstance)
		for slot := 0; slot < perApp && remaining > 0; slot++ {
			app.submitHeld(testInstance, fmt.Sprintf("POST held message %d.%d", appIndex, slot), "",
				app.body(fmt.Sprintf("held-total-%d-%d-%s", appIndex, slot, suffix), channel))
			remaining--
		}
	}

	over := newUniqueHoldApp(testInstance)
	status, body := apiRequest(testInstance, http.MethodPost, messagesPath, over.body("held-total-over-"+suffix, channel))
	requireStatus(testInstance, "POST over the total held cap", status, http.StatusTooManyRequests, body)
	requireErrorMessage(testInstance, "POST over the total held cap", body, heldCapacityReachedMsg)
}

// holdCapFromEnv reads a stack cap forwarded by the Makefile or CI. It skips the test when the cap
// is unset or above limit (a stack not started for the cap tests; make delivery-integration-test-hold
// does, and its runner requires the cap tests to pass), and fails on a malformed or below-1 value.
func holdCapFromEnv(testInstance *testing.T, name string, limit int) int {
	testInstance.Helper()
	text := os.Getenv(name)
	if text == "" {
		testInstance.Skipf("%s is unset: the cap test needs the stack's matching cap, between 1 and %d "+
			"(run make delivery-integration-test-hold)", name, limit)
	}
	value, parseErr := strconv.Atoi(text)
	if parseErr != nil || value < 1 {
		testInstance.Fatalf("%s=%q: must be a number of at least 1", name, text)
	}
	if value > limit {
		testInstance.Skipf("%s=%d is above %d: the cap test needs a small cap (run make delivery-integration-test-hold)",
			name, value, limit)
	}
	return value
}

// TestHoldRouteToAnotherAppsDestinationIsHeld is the usable-destination case with the flag on: a
// route whose only destination another app owns matches no usable destination, so the message is
// held rather than refused with 422.
func TestHoldRouteToAnotherAppsDestinationIsHeld(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	suffix := randomSuffix(testInstance)
	ownedName := usableOwnerApp + "_" + suffix
	channel := "it_" + suffix

	// Cleanups run last-in first: the route goes before the destination it links.
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("DELETE FROM destinations WHERE name = '%s'", ownedName))
	})
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("DELETE FROM routes WHERE channel_pattern = '%s'", channel))
	})
	psql(testInstance, fmt.Sprintf(usableInsertDestinationSQL, ownedName, usableOwnerApp))
	psql(testInstance, fmt.Sprintf(usableInsertBadRouteSQL, channel, ownedName))

	app.submitHeld(testInstance, "POST message routed only to another app's destination", "",
		app.body("held-usable-"+suffix, channel))
}

// holdReleaseTimeout is how long the live release test waits: above the held
// releaser's 5s interval, with room for a slow tick.
const (
	holdReleaseTimeout = 20 * time.Second
	holdReleasePoll    = 250 * time.Millisecond
)

// TestHoldReleasedWhenARouteAppears holds a message, then adds a route on its exact unique channel
// (no wildcard, so no other test's held message is released) and waits for the held releaser to
// turn it into deliveries.
func TestHoldReleasedWhenARouteAppears(testInstance *testing.T) {
	requireHoldStack(testInstance)
	app := newUniqueHoldApp(testInstance)
	suffix := randomSuffix(testInstance)
	channel := "it_" + suffix
	destinationName := "itest_" + suffix

	created := app.submitHeld(testInstance, "POST unrouted message", "", app.body("held-release-"+suffix, channel))

	createDestination(testInstance, destinationName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + suffix + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:signingSecretLength],
		},
	})
	createRoute(testInstance, channel, destinationName)

	deadline := time.Now().Add(holdReleaseTimeout)
	var view messageJSON
	for time.Now().Before(deadline) {
		view = getAs(testInstance, app.appID, created.Message.MessageID)
		if view.Status != statusUnroutedName {
			break
		}
		time.Sleep(holdReleasePoll)
	}
	if view.Status == statusUnroutedName {
		testInstance.Fatalf("message %s still %s %v after the route appeared, want it released",
			created.Message.MessageID, view.Status, holdReleaseTimeout)
	}
	// Released: cleanup deletes the app's rows instead of cancelling a message that is no longer held.
	app.held = nil
	if len(view.Deliveries) == 0 {
		testInstance.Fatalf("released message view = %+v, want at least one delivery", view)
	}
}

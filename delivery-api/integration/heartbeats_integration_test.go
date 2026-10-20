//go:build integration

package integration

// End-to-end test of heartbeat checks against the live compose stack: create a
// check through the admin API, ping it, report a failure and watch the checker
// turn the down alert into a delivery to the check's destination. The
// destination is a webhook on a loopback address the production transport
// refuses, so the alert dead-letters as blocked_address without any external
// receiver; the delivery log (delivery plus attempt) is the proof it was sent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	heartbeatsAdminPath = "/heartbeats"
	heartbeatsPingPath  = "/v1/heartbeats"
	// heartbeatAppPrefix starts every app_id these tests register checks under.
	heartbeatAppPrefix = "ithb_"
	heartbeatCheckName = "nightly"
	// heartbeatAlertTimeout covers one full checker interval (30 s) plus the first
	// delivery attempt, with headroom for a loaded host.
	heartbeatAlertTimeout  = 90 * time.Second
	heartbeatAlertInterval = time.Second
)

// heartbeatAppPattern guards the SQL below against interpolating anything but the
// test's own generated app_id.
var heartbeatAppPattern = regexp.MustCompile(`^ithb_[0-9a-f]{16}$`)

type checkDestinationJSON struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

type checkJSON struct {
	ID            int64                  `json:"id"`
	AppID         string                 `json:"app_id"`
	Name          string                 `json:"name"`
	PeriodSeconds int                    `json:"period_seconds"`
	GraceSeconds  int                    `json:"grace_seconds"`
	Paused        bool                   `json:"paused"`
	State         string                 `json:"state"`
	LastPingAt    *string                `json:"last_ping_at"`
	LastPingKind  *string                `json:"last_ping_kind"`
	DownSince     *string                `json:"down_since"`
	DeadlineAt    string                 `json:"deadline_at"`
	Destinations  []checkDestinationJSON `json:"destinations"`
}

type checkEnvelope struct {
	Check checkJSON `json:"check"`
}

type checksEnvelope struct {
	Checks []checkJSON `json:"checks"`
}

type pingJSON struct {
	AppID string `json:"app_id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type deleteCheckJSON struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

// cleanupHeartbeatCheck deletes the check by id. Register it after the
// destination's cleanup: Cleanup runs LIFO, so the check goes first (the
// destination delete is refused while a check still uses it).
func cleanupHeartbeatCheck(testInstance *testing.T, checkID int64) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(context.Background()), adminCleanupTimeout)
		defer cancelCleanup()
		path := fmt.Sprintf("%s/%d", heartbeatsAdminPath, checkID)
		status, body := adminRequestContext(testInstance, cleanupCtx, http.MethodDelete, path, nil)
		if status != http.StatusOK {
			testInstance.Errorf("cleanup: DELETE heartbeat check %d status = %d, body: %s", checkID, status, body)
		}
	})
}

// registerHeartbeatMessagesCleanup deletes every alert message of the test's own
// app (its deliveries and attempts cascade). Register it before the destination
// so (LIFO) it runs after the destination and check cleanups.
func registerHeartbeatMessagesCleanup(testInstance *testing.T, appID string) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		if !heartbeatAppPattern.MatchString(appID) {
			testInstance.Errorf("cleanup: refusing to delete messages for malformed app_id %q", appID)
			return
		}
		psql(testInstance, fmt.Sprintf("delete from messages where app_id = '%s'", appID))
	})
}

func getCheck(testInstance *testing.T, checkID int64) checkJSON {
	testInstance.Helper()
	status, body := adminRequest(testInstance, http.MethodGet, fmt.Sprintf("%s/%d", heartbeatsAdminPath, checkID), nil)
	requireStatus(testInstance, "GET heartbeat check", status, http.StatusOK, body)
	var decoded checkEnvelope
	decodeBody(testInstance, "GET heartbeat check", body, &decoded)
	return decoded.Check
}

// postPing sends one ping (path suffix "" or "/fail") and requires its 200 body.
func postPing(testInstance *testing.T, appID, name, suffix string) pingJSON {
	testInstance.Helper()
	path := fmt.Sprintf("%s/%s/%s%s", heartbeatsPingPath, appID, name, suffix)
	status, body := apiRequest(testInstance, http.MethodPost, path, nil)
	requireStatus(testInstance, "POST "+path, status, http.StatusOK, body)
	var decoded pingJSON
	decodeBody(testInstance, "POST "+path, body, &decoded)
	return decoded
}

// waitForAlertMessage polls for the alert message submitted under idempotencyKey
// and returns it once its first delivery has been attempted (dead-lettered, as
// the loopback destination is refused).
func waitForAlertMessage(testInstance *testing.T, appID, idempotencyKey string) messageJSON {
	testInstance.Helper()
	path := fmt.Sprintf("%s?app_id=%s&idempotency_key=%s", messagesPath, appID, idempotencyKey)
	deadline := time.Now().Add(heartbeatAlertTimeout)
	var last messageJSON
	for time.Now().Before(deadline) {
		status, body := apiRequest(testInstance, http.MethodGet, path, nil)
		if status == http.StatusOK {
			var decoded messageResponse
			decodeBody(testInstance, "GET alert message", body, &decoded)
			last = decoded.Message
			if len(last.Deliveries) == 1 && last.Deliveries[0].State == deadStateName {
				return last
			}
		} else if status != http.StatusNotFound {
			testInstance.Fatalf("GET alert message status = %d, body: %s", status, body)
		}
		time.Sleep(heartbeatAlertInterval)
	}
	testInstance.Fatalf("no dead-lettered alert delivery for key %q within %s (last read: %+v)",
		idempotencyKey, heartbeatAlertTimeout, last)
	return messageJSON{}
}

// patchResult is one PATCH outcome collected off the test goroutine.
type patchResult struct {
	status int
	body   []byte
	err    error
}

// patchCheckAsync sends one PATCH without touching testInstance, so it is safe to
// call from a goroutine (the test helpers call Fatalf, which is not).
func patchCheckAsync(checkID int64, patch map[string]any) patchResult {
	encoded, marshalErr := json.Marshal(patch)
	if marshalErr != nil {
		return patchResult{err: marshalErr}
	}
	request, requestErr := http.NewRequest(http.MethodPatch,
		fmt.Sprintf("%s%s%s/%d", deliveryURL(), adminBasePath, heartbeatsAdminPath, checkID), bytes.NewReader(encoded))
	if requestErr != nil {
		return patchResult{err: requestErr}
	}
	request.Header.Set("Content-Type", "application/json")
	response, doErr := adminClient.Do(request)
	if doErr != nil {
		return patchResult{err: doErr}
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(response.Body)
	return patchResult{status: response.StatusCode, body: body, err: readErr}
}

// TestHeartbeatConcurrentPatchesOfDifferentFieldsAllPersist sends PATCHes that
// each name one different field at the same moment: the server merges under the
// check's row lock, so every field must end up changed.
func TestHeartbeatConcurrentPatchesOfDifferentFieldsAllPersist(testInstance *testing.T) {
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	appID := heartbeatAppPrefix + suffix
	firstDestination := "itest_a" + suffix
	secondDestination := "itest_b" + suffix
	registerHeartbeatMessagesCleanup(testInstance, appID)
	for _, name := range []string{firstDestination, secondDestination} {
		createDestination(testInstance, name, map[string]any{
			"type": registry.TypeWebhook,
			"secrets": map[string]string{
				"url":            blockedWebhookURL,
				"signing_secret": ("sign" + suffix + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:signingSecretLength],
			},
		})
	}
	status, body := adminRequest(testInstance, http.MethodPost, heartbeatsAdminPath, map[string]any{
		"app_id": appID, "name": heartbeatCheckName, "period_seconds": 3600, "grace_seconds": 3600,
		"destinations": []string{firstDestination},
	})
	var created checkEnvelope
	if status == http.StatusCreated {
		decodeBody(testInstance, "created heartbeat check", body, &created)
		cleanupHeartbeatCheck(testInstance, created.Check.ID)
	}
	requireStatus(testInstance, "POST heartbeat check", status, http.StatusCreated, body)

	patches := []map[string]any{
		{"period_seconds": 7200},
		{"grace_seconds": 900},
		{"destinations": []string{firstDestination, secondDestination}},
	}
	results := make([]patchResult, len(patches))
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := range patches {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results[index] = patchCheckAsync(created.Check.ID, patches[index])
		}()
	}
	close(start)
	group.Wait()
	for index, result := range results {
		if result.err != nil {
			testInstance.Fatalf("PATCH %v: %v", patches[index], result.err)
		}
		requireStatus(testInstance, fmt.Sprintf("PATCH %v", patches[index]), result.status, http.StatusOK, result.body)
	}

	got := getCheck(testInstance, created.Check.ID)
	if got.PeriodSeconds != 7200 || got.GraceSeconds != 900 || len(got.Destinations) != 2 {
		testInstance.Fatalf("check after concurrent PATCHes = period %d grace %d destinations %+v, want 7200, 900 and both destinations",
			got.PeriodSeconds, got.GraceSeconds, got.Destinations)
	}
}

func TestHeartbeatCheckPingFailAndDownAlert(testInstance *testing.T) {
	waitForReady(testInstance)

	suffix := randomSuffix(testInstance)
	appID := heartbeatAppPrefix + suffix
	destinationName := "itest_" + suffix
	registerHeartbeatMessagesCleanup(testInstance, appID)
	createDestination(testInstance, destinationName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + suffix + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")[:signingSecretLength],
		},
	})

	// Create through the admin API: a never-pinged check is waiting ("new").
	status, body := adminRequest(testInstance, http.MethodPost, heartbeatsAdminPath, map[string]any{
		"app_id":         appID,
		"name":           heartbeatCheckName,
		"period_seconds": 3600,
		"grace_seconds":  3600,
		"destinations":   []string{destinationName},
	})
	var created checkEnvelope
	if status == http.StatusCreated {
		decodeBody(testInstance, "created heartbeat check", body, &created)
		cleanupHeartbeatCheck(testInstance, created.Check.ID)
	}
	requireStatus(testInstance, "POST heartbeat check", status, http.StatusCreated, body)
	check := created.Check
	if check.State != "new" || check.Paused || check.LastPingAt != nil || check.DownSince != nil {
		testInstance.Fatalf("created check = %+v, want a new, unpaused, never-pinged check", check)
	}
	if len(check.Destinations) != 1 || check.Destinations[0].Name != destinationName ||
		check.Destinations[0].Type != registry.TypeWebhook || !check.Destinations[0].Enabled {
		testInstance.Fatalf("created check destinations = %+v, want just %s", check.Destinations, destinationName)
	}

	// A duplicate create and an unknown destination are refused with their own statuses.
	duplicateStatus, duplicateBody := adminRequest(testInstance, http.MethodPost, heartbeatsAdminPath, map[string]any{
		"app_id": appID, "name": heartbeatCheckName, "period_seconds": 3600, "grace_seconds": 3600,
		"destinations": []string{destinationName},
	})
	requireStatus(testInstance, "duplicate create", duplicateStatus, http.StatusConflict, duplicateBody)
	requireErrorMessage(testInstance, "duplicate create", duplicateBody, "check already exists")
	unknownStatus, unknownBody := adminRequest(testInstance, http.MethodPost, heartbeatsAdminPath, map[string]any{
		"app_id": appID, "name": "other", "period_seconds": 3600, "grace_seconds": 3600,
		"destinations": []string{"no_such_" + suffix},
	})
	requireStatus(testInstance, "create with an unknown destination", unknownStatus, http.StatusBadRequest, unknownBody)
	requireErrorMessage(testInstance, "create with an unknown destination", unknownBody, "unknown destination")

	// App-facing ping surface: unknown check is a 404, the wrong method a 405.
	missingStatus, missingBody := apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s/%s/missing", heartbeatsPingPath, appID), nil)
	requireStatus(testInstance, "ping of an unknown check", missingStatus, http.StatusNotFound, missingBody)
	requireErrorMessage(testInstance, "ping of an unknown check", missingBody, "check not found")
	wrongMethodStatus, wrongMethodBody := apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("%s/%s/%s", heartbeatsPingPath, appID, heartbeatCheckName), nil)
	requireStatus(testInstance, "GET on the ping path", wrongMethodStatus, http.StatusMethodNotAllowed, wrongMethodBody)

	// A success ping brings the check up.
	if ping := postPing(testInstance, appID, heartbeatCheckName, ""); ping.State != "up" ||
		ping.AppID != appID || ping.Name != heartbeatCheckName {
		testInstance.Fatalf("ping response = %+v, want an up state for the check", ping)
	}
	if got := getCheck(testInstance, check.ID); got.State != "up" || got.LastPingKind == nil ||
		*got.LastPingKind != "success" || got.LastPingAt == nil || got.DownSince != nil {
		testInstance.Fatalf("check after ping = %+v, want up with a success ping and no down_since", got)
	}

	// A fail ping takes it down and emits exactly one down alert.
	if ping := postPing(testInstance, appID, heartbeatCheckName, "/fail"); ping.State != "down" {
		testInstance.Fatalf("fail ping response = %+v, want a down state", ping)
	}
	down := getCheck(testInstance, check.ID)
	if down.State != "down" || down.DownSince == nil || down.LastPingKind == nil || *down.LastPingKind != "fail" {
		testInstance.Fatalf("check after fail = %+v, want down with down_since and a fail ping", down)
	}

	// The checker flushes the down alert: one message to the check's destination,
	// attempted and (the destination being a refused loopback address) dead-lettered.
	idempotencyKey := fmt.Sprintf("hb:%d:1:%s", check.ID, destinationName)
	alert := waitForAlertMessage(testInstance, appID, idempotencyKey)
	if alert.AppID != appID || alert.Deliveries[0].Destination != destinationName {
		testInstance.Fatalf("alert message = %+v, want app %s delivered to %s", alert, appID, destinationName)
	}
	attemptsStatus, attemptsBody := apiRequest(testInstance, http.MethodGet,
		fmt.Sprintf("/v1/admin/deliveries/%d/attempts", alert.Deliveries[0].ID), nil)
	requireStatus(testInstance, "GET alert attempts", attemptsStatus, http.StatusOK, attemptsBody)
	var attempts attemptsResponse
	decodeBody(testInstance, "GET alert attempts", attemptsBody, &attempts)
	if len(attempts.Attempts) == 0 || attempts.Attempts[0].Outcome != blockedOutcome {
		testInstance.Fatalf("alert attempts = %+v, want the first attempt blocked", attempts.Attempts)
	}

	// The list shows the check; pause and resume are idempotent.
	listStatus, listBody := adminRequest(testInstance, http.MethodGet, heartbeatsAdminPath, nil)
	requireStatus(testInstance, "GET heartbeat list", listStatus, http.StatusOK, listBody)
	var listed checksEnvelope
	decodeBody(testInstance, "GET heartbeat list", listBody, &listed)
	found := false
	for _, listedCheck := range listed.Checks {
		found = found || listedCheck.ID == check.ID
	}
	if !found {
		testInstance.Fatalf("heartbeat list does not contain check %d", check.ID)
	}
	for _, action := range []string{"pause", "pause", "resume", "resume"} {
		actionStatus, actionBody := adminRequest(testInstance, http.MethodPost,
			fmt.Sprintf("%s/%d/%s", heartbeatsAdminPath, check.ID, action), nil)
		requireStatus(testInstance, action, actionStatus, http.StatusOK, actionBody)
		var acted checkEnvelope
		decodeBody(testInstance, action, actionBody, &acted)
		if wantPaused := action == "pause"; acted.Check.Paused != wantPaused {
			testInstance.Fatalf("%s response paused = %t, want %t", action, acted.Check.Paused, wantPaused)
		}
	}

	// A ping of a paused check is never a 4xx: it is recorded and answers 200 with
	// the derived state, which is also what the operator view then reports.
	pauseStatus, pauseBody := adminRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s/%d/pause", heartbeatsAdminPath, check.ID), nil)
	requireStatus(testInstance, "pause before the paused pings", pauseStatus, http.StatusOK, pauseBody)
	pausedPing := postPing(testInstance, appID, heartbeatCheckName, "")
	if pausedPing.AppID != appID || pausedPing.Name != heartbeatCheckName || pausedPing.State != "up" {
		testInstance.Fatalf("ping of a paused check = %+v, want 200 with the derived up state", pausedPing)
	}
	if got := getCheck(testInstance, check.ID); !got.Paused || got.State != pausedPing.State {
		testInstance.Fatalf("check after a paused ping = %+v, want still paused in state %s", got, pausedPing.State)
	}
	pausedFail := postPing(testInstance, appID, heartbeatCheckName, "/fail")
	if pausedFail.State != "down" {
		testInstance.Fatalf("fail ping of a paused check = %+v, want 200 with the derived down state", pausedFail)
	}
	if got := getCheck(testInstance, check.ID); !got.Paused || got.State != pausedFail.State {
		testInstance.Fatalf("check after a paused fail ping = %+v, want still paused in state %s", got, pausedFail.State)
	}
	resumeStatus, resumeBody := adminRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s/%d/resume", heartbeatsAdminPath, check.ID), nil)
	requireStatus(testInstance, "resume after the paused pings", resumeStatus, http.StatusOK, resumeBody)

	// PATCH changes the schedule, refuses the immutable identity, then delete is idempotent.
	patchStatus, patchBody := adminRequest(testInstance, http.MethodPatch,
		fmt.Sprintf("%s/%d", heartbeatsAdminPath, check.ID), map[string]any{"period_seconds": 7200})
	requireStatus(testInstance, "PATCH period", patchStatus, http.StatusOK, patchBody)
	var patched checkEnvelope
	decodeBody(testInstance, "PATCH period", patchBody, &patched)
	if patched.Check.PeriodSeconds != 7200 || patched.Check.GraceSeconds != 3600 || len(patched.Check.Destinations) != 1 {
		testInstance.Fatalf("patched check = %+v, want period 7200 with grace and destinations kept", patched.Check)
	}
	immutableStatus, immutableBody := adminRequest(testInstance, http.MethodPatch,
		fmt.Sprintf("%s/%d", heartbeatsAdminPath, check.ID), map[string]any{"name": "renamed"})
	requireStatus(testInstance, "PATCH name", immutableStatus, http.StatusBadRequest, immutableBody)
	requireErrorMessage(testInstance, "PATCH name", immutableBody, "app_id and name are immutable")

	for repeat, wantDeleted := range []bool{true, false} {
		deleteStatus, deleteBody := adminRequest(testInstance, http.MethodDelete,
			fmt.Sprintf("%s/%d", heartbeatsAdminPath, check.ID), nil)
		requireStatus(testInstance, fmt.Sprintf("DELETE #%d", repeat+1), deleteStatus, http.StatusOK, deleteBody)
		var deleted deleteCheckJSON
		decodeBody(testInstance, "DELETE heartbeat check", deleteBody, &deleted)
		if deleted.ID != check.ID || deleted.Deleted != wantDeleted {
			testInstance.Fatalf("DELETE #%d = %+v, want id %d deleted %t", repeat+1, deleted, check.ID, wantDeleted)
		}
	}
	goneStatus, goneBody := adminRequest(testInstance, http.MethodGet, fmt.Sprintf("%s/%d", heartbeatsAdminPath, check.ID), nil)
	requireStatus(testInstance, "GET deleted check", goneStatus, http.StatusNotFound, goneBody)
	requireErrorMessage(testInstance, "GET deleted check", goneBody, "check not found")
}

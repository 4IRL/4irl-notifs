//go:build integration

package heartbeat_test

// Probe alert path on real Postgres through the real heartbeat.Service: the alert body a probe
// check's flush renders must survive edits between the transition and the send, and recovery must
// clear the stored detail.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
)

const probeE2EAppID = "billing"

// createProbe creates a probe check through the service (interval seconds) alerting both hooks and
// moves the clock past the startup hold-off so results may take it down.
func (stack *heartbeatStack) createProbe(testInstance *testing.T, intervalSeconds int) heartbeat.StoredCheck {
	testInstance.Helper()
	created, createErr := stack.service.Create(stack.ctx, heartbeat.NewCheck{
		AppID: probeE2EAppID, Name: "site", DestinationNames: []string{e2eFirstHook, e2eSecondHook},
		Probe: &heartbeat.ProbeConfig{
			URL: "https://probe.example.com/health?token=query-secret-value", ExpectedStatuses: []int{200},
			TimeoutSeconds: 5, IntervalSeconds: intervalSeconds,
		},
	})
	if createErr != nil {
		testInstance.Fatalf("create probe: %v", createErr)
	}
	stack.clock.Advance(heartbeat.StartupHoldOff + time.Second)
	return created
}

// failProbeDown records ProbeFailureThreshold timeout results so the probe goes down with detail "boom".
func (stack *heartbeatStack) failProbeDown(testInstance *testing.T, probe heartbeat.StoredCheck) heartbeat.StoredCheck {
	testInstance.Helper()
	var last heartbeat.StoredCheck
	for range heartbeat.ProbeFailureThreshold {
		recorded, applied, recordErr := stack.store.RecordProbeResult(stack.ctx, probe.ID, probe.Probe.ConfigVersion,
			heartbeat.ProbeResult{Reason: stringPtr(heartbeat.ReasonTimeout), Detail: "boom"},
			stack.clock.Now(), stack.service.HoldOffUntil())
		if recordErr != nil || !applied {
			testInstance.Fatalf("record failing result: applied %v err %v", applied, recordErr)
		}
		last = recorded
	}
	if last.State != heartbeat.StateDown {
		testInstance.Fatalf("probe state after %d failures = %q, want down", heartbeat.ProbeFailureThreshold, last.State)
	}
	return last
}

// displayBodies returns the display body of every request from index `from`.
func displayBodies(testInstance *testing.T, requests []hookRequest, from int) []string {
	testInstance.Helper()
	bodies := make([]string, 0, len(requests)-from)
	for _, request := range requests[from:] {
		var decoded struct {
			Display struct {
				Body string `json:"body"`
			} `json:"display"`
		}
		if decodeErr := json.Unmarshal(request.body, &decoded); decodeErr != nil {
			testInstance.Fatalf("decode webhook body %q: %v", request.body, decodeErr)
		}
		bodies = append(bodies, decoded.Display.Body)
	}
	return bodies
}

func requireAllBodies(testInstance *testing.T, bodies []string, want string) {
	testInstance.Helper()
	if len(bodies) != 2 {
		testInstance.Fatalf("bodies = %q, want one per destination", bodies)
	}
	for _, body := range bodies {
		if body != want {
			testInstance.Fatalf("body = %q, want %q", body, want)
		}
	}
}

func TestProbeDownAlertKeepsItsDetailAcrossAnEffectiveEditBeforeTheFlush(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)
	probe := stack.createProbe(testInstance, 300)
	stack.failProbeDown(testInstance, probe)

	newURL := "https://other.example.com/health"
	edited, patchErr := stack.service.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{URL: &newURL}})
	if patchErr != nil {
		testInstance.Fatalf("Patch: %v", patchErr)
	}
	if edited.Probe.LastResult != nil || edited.Probe.ConfigVersion == probe.Probe.ConfigVersion {
		testInstance.Fatalf("the edit was not effective: %+v", edited.Probe)
	}

	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 2)
	requireAllBodies(testInstance, displayBodies(testInstance, stack.receiver.received(), 0),
		"Probe failed (timed out): boom.")
	stack.requireQuiet(testInstance, 2)
}

func TestProbePauseResumeBeforeTheFlushDropsTheAlertAndKeepsDownDetail(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)
	probe := stack.createProbe(testInstance, 300)
	down := stack.failProbeDown(testInstance, probe)

	if _, pauseErr := stack.service.Pause(stack.ctx, probe.ID); pauseErr != nil {
		testInstance.Fatalf("Pause: %v", pauseErr)
	}
	if _, resumeErr := stack.service.Resume(stack.ctx, probe.ID); resumeErr != nil {
		testInstance.Fatalf("Resume: %v", resumeErr)
	}

	after, getErr := stack.service.Get(stack.ctx, probe.ID)
	if getErr != nil {
		testInstance.Fatalf("Get: %v", getErr)
	}
	if after.PendingAlert != nil {
		testInstance.Fatalf("pending alert = %q after pause and resume, want none", *after.PendingAlert)
	}
	if after.Probe.DownDetail == nil || *after.Probe.DownDetail != "boom" ||
		after.DownSince == nil || !after.DownSince.Equal(*down.DownSince) {
		testInstance.Fatalf("pause/resume touched the down evidence: detail %v since %v, want boom / %v",
			after.Probe.DownDetail, after.DownSince, down.DownSince)
	}
	stack.tick(testInstance)
	stack.requireQuiet(testInstance, 0)
}

func TestProbeMissedAlertBodyTimeSurvivesAnEditBeforeTheFlush(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)
	probe := stack.createProbe(testInstance, heartbeat.MinProbeIntervalSeconds)

	// Past period + derived grace, with no result: AdvanceDue takes it down as missed.
	stack.clock.Advance((heartbeat.MinProbeIntervalSeconds * (heartbeat.ProbeFailureThreshold + 2)) * time.Second)
	advanced, advanceErr := stack.store.AdvanceDue(stack.ctx, stack.clock.Now())
	if advanceErr != nil || len(advanced) != 1 || advanced[0].ID != probe.ID {
		testInstance.Fatalf("AdvanceDue = %+v, %v, want the probe", advanced, advanceErr)
	}
	down, getErr := stack.service.Get(stack.ctx, probe.ID)
	if getErr != nil || down.DownSince == nil || down.DownReason == nil || *down.DownReason != heartbeat.ReasonMissed {
		testInstance.Fatalf("probe = %+v, %v, want down as missed", down, getErr)
	}

	timeout := 4
	edited, patchErr := stack.service.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		Probe: &heartbeat.ProbePatch{TimeoutSeconds: &timeout},
	})
	if patchErr != nil {
		testInstance.Fatalf("Patch: %v", patchErr)
	}
	if edited.Probe.ConfigVersion == down.Probe.ConfigVersion {
		testInstance.Fatalf("the edit was not effective: %+v", edited.Probe)
	}

	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 2)
	requireAllBodies(testInstance, displayBodies(testInstance, stack.receiver.received(), 0),
		"Check went down at "+down.DownSince.UTC().Format(time.RFC3339)+"; no probe result was recorded (the prober may be stalled).")
}

func TestProbeRecoveryClearsDownDetailAndSaysProbeSucceeded(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)
	probe := stack.createProbe(testInstance, 300)
	stack.failProbeDown(testInstance, probe)
	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 2)

	recovered, applied, recordErr := stack.store.RecordProbeResult(stack.ctx, probe.ID, probe.Probe.ConfigVersion,
		heartbeat.ProbeResult{Status: intPtr(200)}, stack.clock.Now(), stack.service.HoldOffUntil())
	if recordErr != nil || !applied {
		testInstance.Fatalf("record success: applied %v err %v", applied, recordErr)
	}
	if recovered.State != heartbeat.StateUp || recovered.Probe.DownDetail != nil {
		testInstance.Fatalf("after success: state %q detail %v, want up with no detail", recovered.State, recovered.Probe.DownDetail)
	}

	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 4)
	bodies := displayBodies(testInstance, stack.receiver.received(), 2)
	if len(bodies) != 2 || !strings.HasPrefix(bodies[0], "Probe succeeded at ") || !strings.HasPrefix(bodies[1], "Probe succeeded at ") {
		testInstance.Fatalf("recovery bodies = %q, want Probe succeeded at ...", bodies)
	}
	stack.requireQuiet(testInstance, 4)
}

func TestPatchForAppRefusesProbeFieldsOnAnAppOwnedPushCheck(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)
	owned, createErr := stack.service.CreateForApp(stack.ctx, probeE2EAppID, heartbeat.AppCheckInput{
		Name: "nightly", PeriodSeconds: e2ePeriodSeconds, GraceSeconds: e2eGraceSeconds,
		DestinationNames: []string{e2eFirstHook},
	})
	if createErr != nil {
		testInstance.Fatalf("CreateForApp: %v", createErr)
	}
	url := "https://other.example.com/"
	patches := map[string]heartbeat.CheckPatch{
		"probe": {Probe: &heartbeat.ProbePatch{URL: &url}},
		"token": {AccessToken: heartbeat.AccessTokenPatch{
			Action: heartbeat.AccessTokenReplace, Token: heartbeat.AccessToken{ClientID: "id", ClientSecret: "secret"},
		}},
	}
	for name, patch := range patches {
		_, patchErr := stack.service.PatchForApp(stack.ctx, probeE2EAppID, owned.ID, patch)
		var validation *heartbeat.ValidationError
		if !errors.As(patchErr, &validation) {
			testInstance.Fatalf("PatchForApp with %s = %v, want *ValidationError", name, patchErr)
		}
	}
	after, getErr := stack.service.Get(stack.ctx, owned.ID)
	if getErr != nil {
		testInstance.Fatalf("Get: %v", getErr)
	}
	if after.Probe != nil || after.PeriodSeconds != owned.PeriodSeconds || after.GraceSeconds != owned.GraceSeconds {
		testInstance.Fatalf("the refused patch changed the check: %+v", after)
	}
}

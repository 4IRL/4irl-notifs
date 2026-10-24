//go:build integration

package heartbeat_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
)

func intPtr(value int) *int          { return &value }
func stringPtr(value string) *string { return &value }

// failing is a failing probe result with the given reason, an HTTP status and a detail.
func failing(reason string, status int) heartbeat.ProbeResult {
	return heartbeat.ProbeResult{Status: intPtr(status), Reason: stringPtr(reason), Detail: "boom"}
}

func succeeding() heartbeat.ProbeResult {
	return heartbeat.ProbeResult{Status: intPtr(200)}
}

// record records result at `at` with no hold-off (the hold-off is before every test time).
func (stack fixture) record(
	testInstance *testing.T, probe heartbeat.StoredCheck, version int64, result heartbeat.ProbeResult, at time.Time,
) (heartbeat.StoredCheck, bool) {
	testInstance.Helper()
	got, applied, err := stack.store.RecordProbeResult(stack.ctx, probe.ID, version, result, at, created)
	if err != nil {
		testInstance.Fatalf("record probe result: %v", err)
	}
	return got, applied
}

func (stack fixture) get(testInstance *testing.T, id int64) heartbeat.StoredCheck {
	testInstance.Helper()
	got, err := stack.store.Get(stack.ctx, id)
	if err != nil {
		testInstance.Fatalf("get %d: %v", id, err)
	}
	return got
}

func (stack fixture) claim(testInstance *testing.T, now time.Time, limit int) []heartbeat.ProbeClaim {
	testInstance.Helper()
	claims, err := stack.store.ClaimDueProbes(stack.ctx, now, limit)
	if err != nil {
		testInstance.Fatalf("claim due probes: %v", err)
	}
	return claims
}

func claimIDs(claims []heartbeat.ProbeClaim) []int64 {
	ids := make([]int64, 0, len(claims))
	for _, claim := range claims {
		ids = append(ids, claim.CheckID)
	}
	slices.Sort(ids)
	return ids
}

// dueAt is a moment every probe created at `created` is due at.
var dueAt = created.Add(probeTestIntervalSeconds * time.Second)

func TestProbePatchNoOpLeavesProgressUntouched(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", probeTestToken())
	stack.record(testInstance, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Minute))
	before := stack.get(testInstance, probe.ID)
	sealedBefore := stack.sealedBytes(testInstance, probe.ID)

	reordered := []int{204, 200}
	patched, err := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		Probe: &heartbeat.ProbePatch{ExpectedStatuses: reordered},
	}, created.Add(2*time.Minute))
	if err != nil {
		testInstance.Fatalf("patch: %v", err)
	}

	if patched.Probe.ConsecutiveFailures != 1 || patched.Probe.LastResult == nil ||
		patched.Probe.ConfigVersion != before.Probe.ConfigVersion ||
		!patched.Probe.NextProbeAt.Equal(before.Probe.NextProbeAt) || !patched.AnchorAt.Equal(before.AnchorAt) {
		testInstance.Fatalf("no-op patch changed progress: before %+v after %+v", before.Probe, patched.Probe)
	}
	if !slices.Equal(stack.sealedBytes(testInstance, probe.ID), sealedBefore) {
		testInstance.Fatalf("no-op patch changed the sealed token bytes")
	}
}

func (stack fixture) sealedBytes(testInstance *testing.T, id int64) []byte {
	testInstance.Helper()
	var sealed []byte
	if err := stack.pool.QueryRow(stack.ctx,
		`SELECT sealed_access_headers FROM heartbeat_check_probes WHERE check_id = $1`, id).Scan(&sealed); err != nil {
		testInstance.Fatalf("read sealed bytes: %v", err)
	}
	return sealed
}

func TestProbePatchEffectiveChangesResetProgress(testInstance *testing.T) {
	newURL := "https://other.example.com/health"
	scenarios := []struct {
		name  string
		patch heartbeat.CheckPatch
	}{
		{"url change", heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{URL: &newURL}}},
		{"token replace", heartbeat.CheckPatch{AccessToken: heartbeat.AccessTokenPatch{
			Action: heartbeat.AccessTokenReplace, Token: heartbeat.AccessToken{ClientID: "new-id", ClientSecret: "new-secret"},
		}}},
		{"token clear", heartbeat.CheckPatch{AccessToken: heartbeat.AccessTokenPatch{Action: heartbeat.AccessTokenClear}}},
	}
	for _, scenario := range scenarios {
		testInstance.Run(scenario.name, func(subTest *testing.T) {
			stack := newFixture(subTest)
			probe := stack.probeCheck(subTest, "site", probeTestToken())
			stack.record(subTest, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Minute))
			patchAt := created.Add(2 * time.Minute)

			patched, err := stack.store.Patch(stack.ctx, probe.ID, scenario.patch, patchAt)
			if err != nil {
				subTest.Fatalf("patch: %v", err)
			}

			if patched.Probe.ConsecutiveFailures != 0 || patched.Probe.LastResult != nil || patched.Probe.ConfigVersion != 2 {
				subTest.Fatalf("effective patch must reset progress: %+v", patched.Probe)
			}
			if !patched.AnchorAt.Equal(patchAt) ||
				!patched.Probe.NextProbeAt.Equal(patchAt.Add(probeTestIntervalSeconds*time.Second)) {
				subTest.Fatalf("anchor %v next_probe_at %v, want %v and one interval later",
					patched.AnchorAt, patched.Probe.NextProbeAt, patchAt)
			}
		})
	}
}

func TestProbePatchTokenReplaceAndClearAreSealedUnderTheCheckID(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", probeTestToken())
	replacement := heartbeat.AccessToken{ClientID: "new-id", ClientSecret: "new-secret"}

	replaced, replaceErr := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		AccessToken: heartbeat.AccessTokenPatch{Action: heartbeat.AccessTokenReplace, Token: replacement},
	}, created.Add(time.Minute))
	if replaceErr != nil {
		testInstance.Fatalf("replace: %v", replaceErr)
	}
	if !replaced.Probe.HasAccessToken {
		testInstance.Fatalf("HasAccessToken after replace = false")
	}
	opened, openErr := heartbeat.OpenProbeAccessToken(stack.sealer, probe.ID, stack.sealedBytes(testInstance, probe.ID))
	if openErr != nil || opened != replacement {
		testInstance.Fatalf("opened token = %+v, %v; want the replacement", opened, openErr)
	}

	cleared, clearErr := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		AccessToken: heartbeat.AccessTokenPatch{Action: heartbeat.AccessTokenClear},
	}, created.Add(2*time.Minute))
	if clearErr != nil {
		testInstance.Fatalf("clear: %v", clearErr)
	}
	if cleared.Probe.HasAccessToken || stack.sealedBytes(testInstance, probe.ID) != nil {
		testInstance.Fatalf("token still present after clear")
	}
}

func TestProbePatchIntervalOnlySchedulesFromTheNewInterval(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", probeTestToken())
	sealedBefore := stack.sealedBytes(testInstance, probe.ID)
	patchAt := created.Add(time.Minute)

	patched, err := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		Probe: &heartbeat.ProbePatch{IntervalSeconds: intPtr(3600)},
	}, patchAt)
	if err != nil {
		testInstance.Fatalf("patch: %v", err)
	}

	if !patched.Probe.NextProbeAt.Equal(patchAt.Add(3600 * time.Second)) {
		testInstance.Fatalf("next_probe_at = %v, want now + 3600s", patched.Probe.NextProbeAt)
	}
	wantGrace := 3600 * (heartbeat.ProbeFailureThreshold + 1)
	if patched.PeriodSeconds != 3600 || patched.GraceSeconds != wantGrace {
		testInstance.Fatalf("period/grace = %d/%d, want 3600/%d", patched.PeriodSeconds, patched.GraceSeconds, wantGrace)
	}
	if !slices.Equal(stack.sealedBytes(testInstance, probe.ID), sealedBefore) {
		testInstance.Fatalf("an unchanged-token patch changed the sealed bytes")
	}
	notDue := stack.claim(testInstance, patchAt.Add(3599*time.Second), 10)
	if len(notDue) != 0 {
		testInstance.Fatalf("claim before the new interval = %v, want none", claimIDs(notDue))
	}
	if due := stack.claim(testInstance, patchAt.Add(3600*time.Second), 10); len(due) != 1 {
		testInstance.Fatalf("claim at the new interval = %v, want the probe", claimIDs(due))
	}
}

func TestProbePatchKindGuard(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "alpha")
	push := stack.check(testInstance, "push", "alpha")
	probe := stack.probeCheck(testInstance, "site", nil)
	url := "https://other.example.com/"

	_, derivedErr := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{PeriodSeconds: intPtr(600)}, created)
	var validation *heartbeat.ValidationError
	if !errors.As(derivedErr, &validation) {
		testInstance.Fatalf("probe patch with period error = %v, want *ValidationError", derivedErr)
	}
	for name, patch := range map[string]heartbeat.CheckPatch{
		"probe fields": {Probe: &heartbeat.ProbePatch{URL: &url}},
		"token": {AccessToken: heartbeat.AccessTokenPatch{
			Action: heartbeat.AccessTokenReplace, Token: *probeTestToken(),
		}},
	} {
		_, pushErr := stack.store.Patch(stack.ctx, push.ID, patch, created)
		if !errors.As(pushErr, &validation) {
			testInstance.Fatalf("push patch with %s error = %v, want *ValidationError", name, pushErr)
		}
	}
	if after := stack.get(testInstance, push.ID); after.Probe != nil {
		testInstance.Fatalf("a push check gained probe state")
	}
}

func TestProbePatchDestinationOnlyLeavesProgressUntouched(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)
	stack.destination(testInstance, "extra")
	stack.record(testInstance, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Minute))
	before := stack.get(testInstance, probe.ID)

	patched, err := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		DestinationNames: []string{"dest_site", "extra"},
	}, created.Add(2*time.Minute))
	if err != nil {
		testInstance.Fatalf("patch: %v", err)
	}

	if len(patched.Destinations) != 2 {
		testInstance.Fatalf("destinations = %+v, want 2", patched.Destinations)
	}
	if patched.Probe.ConsecutiveFailures != 1 || patched.Probe.LastResult == nil ||
		patched.Probe.ConfigVersion != before.Probe.ConfigVersion ||
		!patched.Probe.NextProbeAt.Equal(before.Probe.NextProbeAt) || !patched.AnchorAt.Equal(before.AnchorAt) {
		testInstance.Fatalf("destination-only patch changed probe progress: %+v", patched.Probe)
	}
}

func TestProbeEffectivePatchOnADownProbeKeepsStateReasonAndDetail(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)
	for attempt := 1; attempt <= heartbeat.ProbeFailureThreshold; attempt++ {
		stack.record(testInstance, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Duration(attempt)*time.Minute))
	}
	before := stack.get(testInstance, probe.ID)
	newURL := "https://other.example.com/health"

	if _, err := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		Probe: &heartbeat.ProbePatch{URL: &newURL},
	}, created.Add(time.Hour)); err != nil {
		testInstance.Fatalf("patch: %v", err)
	}

	after := stack.get(testInstance, probe.ID)
	if after.State != heartbeat.StateDown || after.DownReason == nil || *after.DownReason != *before.DownReason ||
		after.Probe.DownDetail == nil || *after.Probe.DownDetail != *before.Probe.DownDetail ||
		after.DownSince == nil || !after.DownSince.Equal(*before.DownSince) || after.AlertSeq != before.AlertSeq {
		testInstance.Fatalf("patch touched down state: before %+v after %+v", before, after)
	}
	if after.Probe.ConsecutiveFailures != 0 || after.Probe.LastResult != nil || after.Probe.ConfigVersion != 2 {
		testInstance.Fatalf("patch did not reset progress: %+v", after.Probe)
	}
}

func TestProbeClaimCarriesBodyMatch(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "dest_body")
	config := *probeTestConfig()
	config.BodyMatch = stringPtr("all good")
	if _, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "body", DestinationNames: []string{"dest_body"}, Probe: &config,
	}, created); err != nil {
		testInstance.Fatalf("create: %v", err)
	}

	claims := stack.claim(testInstance, dueAt, 10)
	if len(claims) != 1 || claims[0].Config.BodyMatch == nil || *claims[0].Config.BodyMatch != "all good" {
		testInstance.Fatalf("claims = %+v, want one carrying body_match", claims)
	}
}

func TestProbeResultWritesTakeDownThenRecover(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)

	for attempt := 1; attempt < heartbeat.ProbeFailureThreshold; attempt++ {
		got, applied := stack.record(testInstance, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Duration(attempt)*time.Minute))
		if !applied || got.State != heartbeat.StateNew || got.PendingAlert != nil || got.Probe.ConsecutiveFailures != attempt {
			testInstance.Fatalf("attempt %d: applied %v check %+v", attempt, applied, got)
		}
	}
	down, applied := stack.record(testInstance, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(5*time.Minute))
	if !applied || down.State != heartbeat.StateDown || down.AlertSeq != 1 || pendingOf(down) != heartbeat.AlertDown {
		testInstance.Fatalf("after %d failures: applied %v check %+v", heartbeat.ProbeFailureThreshold, applied, down)
	}
	if down.Probe.DownDetail == nil || *down.Probe.DownDetail != "boom" || down.DownReason == nil || *down.DownReason != heartbeat.ReasonTimeout {
		testInstance.Fatalf("down reason/detail = %v / %v", down.DownReason, down.Probe.DownDetail)
	}

	recovered, recoverApplied := stack.record(testInstance, probe, 1, succeeding(), created.Add(6*time.Minute))
	if !recoverApplied || recovered.State != heartbeat.StateUp || recovered.AlertSeq != 2 ||
		pendingOf(recovered) != heartbeat.AlertRecovered || recovered.Probe.ConsecutiveFailures != 0 ||
		recovered.DownReason != nil || recovered.Probe.DownDetail != nil || recovered.DownSince == nil {
		testInstance.Fatalf("after success: applied %v check %+v", recoverApplied, recovered)
	}
}

func TestProbeOutOfRangeStatusIsPersistedAsNullAndCounts(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)

	var last heartbeat.StoredCheck
	for attempt := 1; attempt <= heartbeat.ProbeFailureThreshold; attempt++ {
		got, applied := stack.record(testInstance, probe, 1, failing(heartbeat.ReasonHTTPStatus, 700), created.Add(time.Duration(attempt)*time.Minute))
		if !applied {
			testInstance.Fatalf("attempt %d with status 700 was not applied", attempt)
		}
		if got.Probe.LastResult == nil || got.Probe.LastResult.Status != nil ||
			got.Probe.LastResult.Reason == nil || *got.Probe.LastResult.Reason != heartbeat.ReasonHTTPStatus ||
			got.Probe.ConsecutiveFailures != attempt {
			testInstance.Fatalf("attempt %d: last result %+v counter %d", attempt, got.Probe.LastResult, got.Probe.ConsecutiveFailures)
		}
		last = got
	}
	if last.State != heartbeat.StateDown || pendingOf(last) != heartbeat.AlertDown {
		testInstance.Fatalf("three out-of-range failures: %+v, want down with a pending down alert", last)
	}
}

func TestProbeStaleResultsAreDiscarded(testInstance *testing.T) {
	scenarios := []struct {
		name   string
		mutate func(t *testing.T, stack fixture, probe heartbeat.StoredCheck)
	}{
		{"pause", func(t *testing.T, stack fixture, probe heartbeat.StoredCheck) {
			if _, err := stack.store.Pause(stack.ctx, probe.ID, created.Add(time.Minute)); err != nil {
				t.Fatalf("pause: %v", err)
			}
		}},
		{"resume", func(t *testing.T, stack fixture, probe heartbeat.StoredCheck) {
			if _, err := stack.store.Pause(stack.ctx, probe.ID, created.Add(time.Minute)); err != nil {
				t.Fatalf("pause: %v", err)
			}
			if _, err := stack.store.Resume(stack.ctx, probe.ID, created.Add(2*time.Minute)); err != nil {
				t.Fatalf("resume: %v", err)
			}
		}},
		{"edit", func(t *testing.T, stack fixture, probe heartbeat.StoredCheck) {
			url := "https://other.example.com/"
			if _, err := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{Probe: &heartbeat.ProbePatch{URL: &url}}, created.Add(time.Minute)); err != nil {
				t.Fatalf("patch: %v", err)
			}
		}},
	}
	for _, scenario := range scenarios {
		testInstance.Run(scenario.name, func(subTest *testing.T) {
			stack := newFixture(subTest)
			probe := stack.probeCheck(subTest, "site", nil)
			scenario.mutate(subTest, stack, probe)
			before := stack.get(subTest, probe.ID)

			for range heartbeat.ProbeFailureThreshold {
				_, applied := stack.record(subTest, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(10*time.Minute))
				if applied {
					subTest.Fatalf("stale-version result was applied")
				}
			}

			after := stack.get(subTest, probe.ID)
			if after.State != before.State || after.AlertSeq != before.AlertSeq || after.PendingAlert != nil ||
				after.Probe.ConsecutiveFailures != before.Probe.ConsecutiveFailures ||
				(after.Probe.LastResult == nil) != (before.Probe.LastResult == nil) || !after.AnchorAt.Equal(before.AnchorAt) {
				subTest.Fatalf("stale results changed the check: before %+v after %+v", before, after)
			}
		})
	}

	testInstance.Run("delete", func(subTest *testing.T) {
		stack := newFixture(subTest)
		probe := stack.probeCheck(subTest, "site", nil)
		if _, err := stack.store.Delete(stack.ctx, probe.ID); err != nil {
			subTest.Fatalf("delete: %v", err)
		}
		got, applied := stack.record(subTest, probe, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Minute))
		if applied || got.ID != 0 {
			subTest.Fatalf("result for a deleted check: applied %v check %+v", applied, got)
		}
	})
}

func TestProbeConcurrentResultWritesKeepACoherentCounter(testInstance *testing.T) {
	stack := newFixture(testInstance)
	for round := range 5 {
		probe := stack.probeCheck(testInstance, "round_"+string(rune('a'+round)), nil)
		var group sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, heartbeat.ProbeFailureThreshold)
		for caller := range errs {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				_, _, errs[caller] = stack.store.RecordProbeResult(
					stack.ctx, probe.ID, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Minute), created)
			}()
		}
		close(start)
		group.Wait()
		for _, callErr := range errs {
			if callErr != nil {
				testInstance.Fatalf("round %d: concurrent write failed: %v", round, callErr)
			}
		}

		after := stack.get(testInstance, probe.ID)
		if after.Probe.ConsecutiveFailures != heartbeat.ProbeFailureThreshold || after.State != heartbeat.StateDown ||
			after.AlertSeq != 1 || pendingOf(after) != heartbeat.AlertDown {
			testInstance.Fatalf("round %d: after = %+v, want counter %d, down and exactly one alert",
				round, after, heartbeat.ProbeFailureThreshold)
		}
	}
}

func TestProbeRecordResultAppliedMatrix(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "alpha")
	push := stack.check(testInstance, "push", "alpha")

	if got, applied := stack.record(testInstance, heartbeat.StoredCheck{ID: absentID}, 1, failing(heartbeat.ReasonTimeout, 0), created); applied || got.ID != 0 {
		testInstance.Fatalf("missing id: applied %v check %+v", applied, got)
	}
	if _, applied := stack.record(testInstance, push, 1, failing(heartbeat.ReasonTimeout, 0), created); applied {
		testInstance.Fatalf("push check result was applied")
	}

	probe := stack.probeCheck(testInstance, "site", nil)
	if _, applied := stack.record(testInstance, probe, 1, heartbeat.ProbeResult{Reason: stringPtr(heartbeat.ReasonMissed)}, created.Add(time.Minute)); applied {
		testInstance.Fatalf("a missed (non-probe) reason was applied")
	}

	// Paused after claim: the claim hands out version 1, the pause bumps it to 2, and the result that
	// carries the post-pause version is stopped by the Paused deny, not by the version check.
	claims := stack.claim(testInstance, dueAt, 10)
	if len(claims) != 1 || claims[0].CheckID != probe.ID {
		testInstance.Fatalf("claim = %v, want the probe", claimIDs(claims))
	}
	if _, err := stack.store.Pause(stack.ctx, probe.ID, dueAt); err != nil {
		testInstance.Fatalf("pause: %v", err)
	}
	paused := stack.get(testInstance, probe.ID)
	if _, applied := stack.record(testInstance, probe, paused.Probe.ConfigVersion, failing(heartbeat.ReasonTimeout, 0), dueAt.Add(time.Second)); applied {
		testInstance.Fatalf("result for a paused check was applied")
	}
	after := stack.get(testInstance, probe.ID)
	if after.Probe.ConsecutiveFailures != 0 || after.Probe.LastResult != nil || !after.AnchorAt.Equal(paused.AnchorAt) {
		testInstance.Fatalf("paused check changed by a denied result: %+v", after)
	}

	// An accepted non-alerting result still returns applied and persists.
	live := stack.probeCheck(testInstance, "live", nil)
	got, applied := stack.record(testInstance, live, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Minute))
	if !applied || got.Probe.ConsecutiveFailures != 1 || got.PendingAlert != nil {
		testInstance.Fatalf("first failure: applied %v check %+v", applied, got)
	}
	if persisted := stack.get(testInstance, live.ID); persisted.Probe.ConsecutiveFailures != 1 || persisted.Probe.LastResult == nil {
		testInstance.Fatalf("first failure not persisted: %+v", persisted.Probe)
	}
}

func TestProbeHoldOffDefersTheTransition(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)
	holdOffUntil := created.Add(10 * time.Minute)

	var last heartbeat.StoredCheck
	for attempt := 1; attempt <= heartbeat.ProbeFailureThreshold; attempt++ {
		got, applied, err := stack.store.RecordProbeResult(stack.ctx, probe.ID, 1,
			failing(heartbeat.ReasonTimeout, 0), created.Add(time.Duration(attempt)*time.Minute), holdOffUntil)
		if err != nil || !applied {
			testInstance.Fatalf("attempt %d: applied %v err %v", attempt, applied, err)
		}
		last = got
	}
	if last.Probe.ConsecutiveFailures != heartbeat.ProbeFailureThreshold || last.State != heartbeat.StateNew ||
		last.Probe.LastResult == nil || last.PendingAlert != nil {
		testInstance.Fatalf("inside the hold-off: %+v, want counter %d, not down, no alert", last, heartbeat.ProbeFailureThreshold)
	}

	transitioned, applied, err := stack.store.RecordProbeResult(stack.ctx, probe.ID, 1,
		failing(heartbeat.ReasonTimeout, 0), holdOffUntil, holdOffUntil)
	if err != nil || !applied || transitioned.State != heartbeat.StateDown || transitioned.AlertSeq != 1 {
		testInstance.Fatalf("at the hold-off: applied %v err %v check %+v", applied, err, transitioned)
	}
	again, _, againErr := stack.store.RecordProbeResult(stack.ctx, probe.ID, 1,
		failing(heartbeat.ReasonTimeout, 0), holdOffUntil.Add(time.Minute), holdOffUntil)
	if againErr != nil || again.AlertSeq != 1 {
		testInstance.Fatalf("a later failure re-alerted: %+v, %v", again, againErr)
	}
}

func TestProbeAdvanceDueTakesStaleProbesDownAsMissed(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stale := stack.probeCheck(testInstance, "stale", nil)
	fresh := stack.probeCheck(testInstance, "fresh", nil)

	freshAt := created.Add(30 * time.Minute)
	for range heartbeat.ProbeFailureThreshold - 1 {
		stack.record(testInstance, fresh, 1, failing(heartbeat.ReasonTimeout, 0), freshAt)
	}

	advanced, err := stack.store.AdvanceDue(stack.ctx, freshAt)
	if err != nil {
		testInstance.Fatalf("advance due: %v", err)
	}
	if len(advanced) != 1 || advanced[0].ID != stale.ID {
		testInstance.Fatalf("advanced = %+v, want only the stale probe", advanced)
	}
	down := stack.get(testInstance, stale.ID)
	if down.State != heartbeat.StateDown || down.DownReason == nil || *down.DownReason != heartbeat.ReasonMissed ||
		down.Probe.DownDetail != nil || down.AlertSeq != 1 {
		testInstance.Fatalf("stale probe after tick: %+v", down)
	}

	overwritten, applied := stack.record(testInstance, stale, 1, failing(heartbeat.ReasonNetwork, 0), freshAt.Add(time.Minute))
	if !applied || overwritten.DownReason == nil || *overwritten.DownReason != heartbeat.ReasonNetwork ||
		overwritten.Probe.DownDetail == nil || overwritten.AlertSeq != 1 {
		testInstance.Fatalf("failing result on a missed probe: applied %v check %+v", applied, overwritten)
	}
}

// waitForBlockedWriter polls pg_stat_activity until a backend is waiting on a lock, so a test knows the
// writer goroutine is parked behind the raw transaction before it continues.
func (stack fixture) waitForBlockedWriter(testInstance *testing.T, blockerPID int32) {
	testInstance.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := stack.pool.QueryRow(stack.ctx,
			`SELECT count(*) FROM pg_stat_activity
WHERE $1::int = ANY(pg_blocking_pids(pid)) AND wait_event_type = 'Lock'`, blockerPID).Scan(&waiting); err != nil {
			testInstance.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	testInstance.Fatalf("no backend ever blocked on the raw transaction's locks (pid %d)", blockerPID)
}

// claimVersusSave holds raw locks on the check (and optionally its probe row), starts writer, waits until
// it is blocked, runs during, then releases the locks and waits for the writer.
func (stack fixture) claimVersusSave(
	testInstance *testing.T, checkID int64, lockProbeRow bool, writer func() error, during func(),
) {
	testInstance.Helper()
	tx, beginErr := stack.pool.Begin(stack.ctx)
	if beginErr != nil {
		testInstance.Fatalf("begin: %v", beginErr)
	}
	released := false
	release := func() {
		if !released {
			released = true
			if rollbackErr := tx.Rollback(stack.ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				testInstance.Errorf("rollback: %v", rollbackErr)
			}
		}
	}
	writerDone := make(chan error, 1)
	writerStarted := false
	defer func() {
		release()
		if writerStarted {
			<-writerDone
		}
	}()
	var blockerPID int32
	if scanErr := tx.QueryRow(stack.ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); scanErr != nil {
		testInstance.Fatalf("read backend pid: %v", scanErr)
	}
	if _, execErr := tx.Exec(stack.ctx, `SELECT id FROM heartbeat_checks WHERE id = $1 FOR UPDATE`, checkID); execErr != nil {
		testInstance.Fatalf("lock parent: %v", execErr)
	}
	if lockProbeRow {
		if _, execErr := tx.Exec(stack.ctx, `SELECT check_id FROM heartbeat_check_probes WHERE check_id = $1 FOR UPDATE`, checkID); execErr != nil {
			testInstance.Fatalf("lock probe row: %v", execErr)
		}
	}

	writerStarted = true
	go func() { writerDone <- writer() }()
	stack.waitForBlockedWriter(testInstance, blockerPID)
	during()
	release()
	writerStarted = false
	if writerErr := <-writerDone; writerErr != nil {
		testInstance.Fatalf("writer: %v", writerErr)
	}
}

func TestProbeClaimVersusConcurrentSave(testInstance *testing.T) {
	tickAt := created.Add(30 * time.Minute)
	writers := []struct {
		name   string
		write  func(stack fixture, id int64) error
		verify func(testInstance *testing.T, stack fixture, id int64)
	}{
		{
			name: "pause",
			write: func(stack fixture, id int64) error {
				_, err := stack.store.Pause(stack.ctx, id, tickAt)
				return err
			},
			verify: func(testInstance *testing.T, stack fixture, id int64) {
				if !stack.get(testInstance, id).Paused {
					testInstance.Fatalf("the pause did not land")
				}
			},
		},
		{
			name: "advance due",
			write: func(stack fixture, id int64) error {
				_, err := stack.store.AdvanceDue(stack.ctx, tickAt)
				return err
			},
			verify: func(testInstance *testing.T, stack fixture, id int64) {
				if stack.get(testInstance, id).State != heartbeat.StateDown {
					testInstance.Fatalf("the tick did not land")
				}
			},
		},
	}
	for _, writer := range writers {
		testInstance.Run(writer.name+" waits on the parent only", func(subTest *testing.T) {
			stack := newFixture(subTest)
			probe := stack.probeCheck(subTest, "site", nil)
			claimedNext := tickAt.Add(probeTestIntervalSeconds * time.Second)

			stack.claimVersusSave(subTest, probe.ID, false,
				func() error { return writer.write(stack, probe.ID) },
				func() {
					if claims := stack.claim(subTest, tickAt, 10); len(claims) != 1 {
						subTest.Fatalf("claim while the writer waits on the parent = %v, want the probe", claimIDs(claims))
					}
					// A committed change to the probe row the writer has not read yet: a probe row read
					// before the parent wait (a join snapshot) would write the stale counter back.
					if _, execErr := stack.pool.Exec(stack.ctx,
						`UPDATE heartbeat_check_probes SET consecutive_failures = 2 WHERE check_id = $1`, probe.ID); execErr != nil {
						subTest.Fatalf("commit counter while the writer waits: %v", execErr)
					}
				})

			after := stack.get(subTest, probe.ID)
			if after.Probe.ConsecutiveFailures != 2 {
				subTest.Fatalf("counter = %d, want 2 (the save wrote a stale probe snapshot)", after.Probe.ConsecutiveFailures)
			}
			if !after.Probe.NextProbeAt.Equal(claimedNext) {
				subTest.Fatalf("next_probe_at = %v, want the claimed %v (the save reverted the claim)", after.Probe.NextProbeAt, claimedNext)
			}
			writer.verify(subTest, stack, probe.ID)
		})

		testInstance.Run(writer.name+" holds the probe row", func(subTest *testing.T) {
			stack := newFixture(subTest)
			probe := stack.probeCheck(subTest, "site", nil)

			stack.claimVersusSave(subTest, probe.ID, true,
				func() error { return writer.write(stack, probe.ID) },
				func() {
					if claims := stack.claim(subTest, tickAt, 10); len(claims) != 0 {
						subTest.Fatalf("claim of a row a save path holds = %v, want none", claimIDs(claims))
					}
				})

			after := stack.get(subTest, probe.ID)
			if !after.Probe.NextProbeAt.Equal(probe.Probe.NextProbeAt) {
				subTest.Fatalf("next_probe_at = %v, want the pre-claim %v", after.Probe.NextProbeAt, probe.Probe.NextProbeAt)
			}
			writer.verify(subTest, stack, probe.ID)
			followUp := stack.claim(subTest, tickAt, 10)
			if writer.name == "pause" {
				if len(followUp) != 0 || !stack.get(subTest, probe.ID).Probe.NextProbeAt.Equal(probe.Probe.NextProbeAt) {
					subTest.Fatalf("follow-up claim of a paused probe = %v, want none and no advance", claimIDs(followUp))
				}
			} else if len(followUp) != 1 {
				subTest.Fatalf("follow-up claim after the tick = %v, want the still-due probe", claimIDs(followUp))
			}
		})
	}
}

func TestProbeClaimSelection(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "alpha")
	stack.check(testInstance, "push", "alpha")
	paused := stack.probeCheck(testInstance, "paused", nil)
	if _, err := stack.store.Pause(stack.ctx, paused.ID, created.Add(time.Minute)); err != nil {
		testInstance.Fatalf("pause: %v", err)
	}
	down := stack.probeCheck(testInstance, "down", nil)
	for attempt := 1; attempt <= heartbeat.ProbeFailureThreshold; attempt++ {
		stack.record(testInstance, down, 1, failing(heartbeat.ReasonTimeout, 0), created.Add(time.Duration(attempt)*time.Minute))
	}
	if stack.get(testInstance, down.ID).State != heartbeat.StateDown {
		testInstance.Fatalf("setup: the probe is not down")
	}

	if early := stack.claim(testInstance, dueAt.Add(-time.Second), 10); len(early) != 0 {
		testInstance.Fatalf("claim before next_probe_at = %v, want none", claimIDs(early))
	}
	first := stack.claim(testInstance, dueAt, 10)
	if got := claimIDs(first); !slices.Equal(got, []int64{down.ID}) {
		testInstance.Fatalf("claim = %v, want only the down probe (%d)", got, down.ID)
	}
	if again := stack.claim(testInstance, dueAt, 10); len(again) != 0 {
		testInstance.Fatalf("second immediate claim = %v, want none", claimIDs(again))
	}
	claimed := stack.get(testInstance, down.ID)
	if !claimed.Probe.NextProbeAt.Equal(dueAt.Add(probeTestIntervalSeconds * time.Second)) {
		testInstance.Fatalf("next_probe_at = %v, want now + interval", claimed.Probe.NextProbeAt)
	}
	if later := stack.claim(testInstance, dueAt.Add(probeTestIntervalSeconds*time.Second), 10); len(later) != 1 {
		testInstance.Fatalf("claim one interval later = %v, want the probe again", claimIDs(later))
	}
}

func TestProbeClaimHonoursLimitEarliestFirst(testInstance *testing.T) {
	stack := newFixture(testInstance)
	ids := make([]int64, 0, 5)
	for index := range 5 {
		probe := stack.probeCheck(testInstance, "limit_"+string(rune('a'+index)), nil)
		ids = append(ids, probe.ID)
		// Spread next_probe_at so the order is unambiguous: the later the index, the later the time.
		if _, err := stack.pool.Exec(stack.ctx,
			`UPDATE heartbeat_check_probes SET next_probe_at = $2 WHERE check_id = $1`,
			probe.ID, dueAt.Add(time.Duration(index)*time.Second)); err != nil {
			testInstance.Fatalf("seed next_probe_at: %v", err)
		}
	}
	now := dueAt.Add(time.Minute)

	claims := stack.claim(testInstance, now, 2)
	if got := claimIDs(claims); !slices.Equal(got, ids[:2]) {
		testInstance.Fatalf("limit 2 claimed %v, want the two earliest %v", got, ids[:2])
	}
	rest := stack.claim(testInstance, now, 10)
	if got := claimIDs(rest); !slices.Equal(got, ids[2:]) {
		testInstance.Fatalf("remaining claim = %v, want %v", got, ids[2:])
	}
}

func TestProbeConcurrentClaimsNeverReturnARowTwice(testInstance *testing.T) {
	stack := newFixture(testInstance)
	for index := range 6 {
		stack.probeCheck(testInstance, "claim_"+string(rune('a'+index)), nil)
	}
	var group sync.WaitGroup
	start := make(chan struct{})
	results := make([][]heartbeat.ProbeClaim, 4)
	errs := make([]error, len(results))
	for caller := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results[caller], errs[caller] = stack.store.ClaimDueProbes(stack.ctx, dueAt, 6)
		}()
	}
	close(start)
	group.Wait()

	seen := map[int64]bool{}
	for caller, claims := range results {
		if errs[caller] != nil {
			testInstance.Fatalf("claim %d: %v", caller, errs[caller])
		}
		for _, claim := range claims {
			if seen[claim.CheckID] {
				testInstance.Fatalf("check %d was claimed twice", claim.CheckID)
			}
			seen[claim.CheckID] = true
		}
	}
	if len(seen) != 6 {
		testInstance.Fatalf("claimed %d distinct rows, want all 6", len(seen))
	}
}

func TestProbeClaimContents(testInstance *testing.T) {
	for _, withToken := range []bool{true, false} {
		name := "without token"
		var token *heartbeat.AccessToken
		if withToken {
			name, token = "with token", probeTestToken()
		}
		testInstance.Run(name, func(subTest *testing.T) {
			stack := newFixture(subTest)
			probe := stack.probeCheck(subTest, "site", token)

			claims := stack.claim(subTest, dueAt, 10)
			if len(claims) != 1 {
				subTest.Fatalf("claims = %v, want 1", claimIDs(claims))
			}
			claim := claims[0]
			stored := stack.get(subTest, probe.ID)
			if claim.CheckID != probe.ID || claim.AppID != stored.AppID || claim.Name != stored.Name ||
				claim.ConfigVersion != stored.Probe.ConfigVersion || claim.ConfigVersion != 1 {
				subTest.Fatalf("claim identity = %+v, stored %+v", claim, stored)
			}
			config := claim.Config
			if config.URL != probeTestURL || !slices.Equal(config.ExpectedStatuses, []int{200, 204}) ||
				config.BodyMatch != nil || config.TimeoutSeconds != 5 || config.IntervalSeconds != probeTestIntervalSeconds {
				subTest.Fatalf("claim config = %+v", config)
			}
			if !withToken {
				if claim.SealedAccessToken != nil {
					subTest.Fatalf("sealed token = %v, want nil", claim.SealedAccessToken)
				}
			} else {
				opened, openErr := heartbeat.OpenProbeAccessToken(stack.sealer, claim.CheckID, claim.SealedAccessToken)
				if openErr != nil || opened != *token {
					subTest.Fatalf("opened claim token = %+v, %v", opened, openErr)
				}
			}
			if _, applied := stack.record(subTest, probe, claim.ConfigVersion, succeeding(), dueAt); !applied {
				subTest.Fatalf("a result recorded with the claimed version was not applied")
			}
		})
	}
}

func TestProbeFirstProbeTimingAfterCreateResumeAndEdit(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)
	interval := probeTestIntervalSeconds * time.Second

	if early := stack.claim(testInstance, created.Add(interval-time.Second), 10); len(early) != 0 {
		testInstance.Fatalf("claim before the first interval after create = %v", claimIDs(early))
	}

	pausedAt := created.Add(time.Minute)
	if _, err := stack.store.Pause(stack.ctx, probe.ID, pausedAt); err != nil {
		testInstance.Fatalf("pause: %v", err)
	}
	resumeAt := created.Add(time.Hour)
	if _, err := stack.store.Resume(stack.ctx, probe.ID, resumeAt); err != nil {
		testInstance.Fatalf("resume: %v", err)
	}
	if early := stack.claim(testInstance, resumeAt.Add(interval-time.Second), 10); len(early) != 0 {
		testInstance.Fatalf("claim before the first interval after resume = %v", claimIDs(early))
	}
	if resumed := stack.get(testInstance, probe.ID); !resumed.Probe.NextProbeAt.Equal(resumeAt.Add(interval)) {
		testInstance.Fatalf("next_probe_at after resume = %v, want %v", resumed.Probe.NextProbeAt, resumeAt.Add(interval))
	}

	editAt := resumeAt.Add(10 * time.Minute)
	newInterval := 900
	if _, err := stack.store.Patch(stack.ctx, probe.ID, heartbeat.CheckPatch{
		Probe: &heartbeat.ProbePatch{IntervalSeconds: &newInterval},
	}, editAt); err != nil {
		testInstance.Fatalf("patch: %v", err)
	}
	editedInterval := time.Duration(newInterval) * time.Second
	if early := stack.claim(testInstance, editAt.Add(editedInterval-time.Second), 10); len(early) != 0 {
		testInstance.Fatalf("claim before the first interval after an edit = %v", claimIDs(early))
	}
	if due := stack.claim(testInstance, editAt.Add(editedInterval), 10); len(due) != 1 {
		testInstance.Fatalf("claim at now + the new interval = %v, want the probe", claimIDs(due))
	}
}

func TestProbeClaimLimitBelowOneIsAValidationError(testInstance *testing.T) {
	stack := newFixture(testInstance)

	for _, limit := range []int{0, -1} {
		_, err := stack.store.ClaimDueProbes(stack.ctx, dueAt, limit)
		var validation *heartbeat.ValidationError
		if !errors.As(err, &validation) {
			testInstance.Fatalf("limit %d error = %v, want *ValidationError", limit, err)
		}
	}
}

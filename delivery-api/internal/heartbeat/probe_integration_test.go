//go:build integration

package heartbeat_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgutil"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	probeTestIntervalSeconds = 300
	probeTestSecretQuery     = "token=query-secret-value"
	probeTestURL             = "https://probe.example.com/health?" + probeTestSecretQuery
	probeTestClientID        = "client-id-plaintext"
	probeTestClientSecret    = "client-secret-plaintext"
)

func probeTestConfig() *heartbeat.ProbeConfig {
	return &heartbeat.ProbeConfig{
		URL:              probeTestURL,
		ExpectedStatuses: []int{204, 200},
		TimeoutSeconds:   5,
		IntervalSeconds:  probeTestIntervalSeconds,
	}
}

func probeTestToken() *heartbeat.AccessToken {
	return &heartbeat.AccessToken{ClientID: probeTestClientID, ClientSecret: probeTestClientSecret}
}

// probeCheck creates a probe check at `created` with its own destination. The caller passes bogus
// period and grace on purpose: the store must derive them from the interval.
func (stack fixture) probeCheck(testInstance *testing.T, name string, token *heartbeat.AccessToken) heartbeat.StoredCheck {
	testInstance.Helper()
	stack.destination(testInstance, "dest_"+name)
	stored, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: name, PeriodSeconds: 7, GraceSeconds: 0,
		DestinationNames: []string{"dest_" + name}, Probe: probeTestConfig(), AccessToken: token,
	}, created)
	if err != nil {
		testInstance.Fatalf("create probe check %s: %v", name, err)
	}
	return stored
}

func TestProbeCreatePersistsRowAndDerivedSchedule(testInstance *testing.T) {
	stack := newFixture(testInstance)

	stored := stack.probeCheck(testInstance, "site", nil)

	if stored.Kind() != heartbeat.KindProbe || stored.Probe == nil {
		testInstance.Fatalf("created check = %+v, want a probe check", stored)
	}
	wantGrace := probeTestIntervalSeconds * (heartbeat.ProbeFailureThreshold + 1)
	if stored.PeriodSeconds != probeTestIntervalSeconds || stored.GraceSeconds != wantGrace {
		testInstance.Fatalf("period/grace = %d/%d, want %d/%d",
			stored.PeriodSeconds, stored.GraceSeconds, probeTestIntervalSeconds, wantGrace)
	}
	probe := stored.Probe
	if probe.Config.URL != probeTestURL || probe.Config.TimeoutSeconds != 5 || probe.Config.IntervalSeconds != probeTestIntervalSeconds {
		testInstance.Fatalf("probe config = %+v", probe.Config)
	}
	if len(probe.Config.ExpectedStatuses) != 2 || probe.Config.ExpectedStatuses[0] != 200 || probe.Config.ExpectedStatuses[1] != 204 {
		testInstance.Fatalf("expected statuses = %v, want ascending [200 204]", probe.Config.ExpectedStatuses)
	}
	wantNext := created.Add(probeTestIntervalSeconds * time.Second)
	if !probe.NextProbeAt.Equal(wantNext) || probe.NextProbeAt.Location() != time.UTC {
		testInstance.Fatalf("next_probe_at = %v, want %v in UTC", probe.NextProbeAt, wantNext)
	}
	if probe.ConfigVersion != 1 || probe.ConsecutiveFailures != 0 || probe.LastResult != nil || probe.HasAccessToken {
		testInstance.Fatalf("fresh probe state = %+v", probe)
	}
}

func TestProbeReadsPopulateProbeOnlyForProbeChecks(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "alpha")
	push := stack.check(testInstance, "push", "alpha")
	probe := stack.probeCheck(testInstance, "site", probeTestToken())

	gotPush, getPushErr := stack.store.Get(stack.ctx, push.ID)
	gotProbe, getProbeErr := stack.store.Get(stack.ctx, probe.ID)
	if getPushErr != nil || getProbeErr != nil {
		testInstance.Fatalf("get: %v / %v", getPushErr, getProbeErr)
	}
	if gotPush.Probe != nil || gotPush.Kind() != heartbeat.KindPush {
		testInstance.Fatalf("push check Probe = %+v, want nil", gotPush.Probe)
	}
	if gotProbe.Probe == nil || !gotProbe.Probe.HasAccessToken || len(gotProbe.Probe.SealedAccessToken) != 0 {
		testInstance.Fatalf("probe check Probe = %+v, want populated with a token flag and no sealed bytes", gotProbe.Probe)
	}

	listed, listErr := stack.store.List(stack.ctx)
	if listErr != nil {
		testInstance.Fatalf("list: %v", listErr)
	}
	byName := map[string]heartbeat.StoredCheck{}
	for _, check := range listed {
		byName[check.Name] = check
	}
	if byName["push"].Probe != nil || byName["site"].Probe == nil {
		testInstance.Fatalf("list probes = push %+v, site %+v", byName["push"].Probe, byName["site"].Probe)
	}
}

func TestProbeDeleteCascadesToProbeRow(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)
	if rows := stack.count(testInstance, "heartbeat_check_probes"); rows != 1 {
		testInstance.Fatalf("probe rows before delete = %d, want 1", rows)
	}

	deleted, deleteErr := stack.store.Delete(stack.ctx, probe.ID)
	if deleteErr != nil || !deleted {
		testInstance.Fatalf("delete = %v, %v", deleted, deleteErr)
	}
	if rows := stack.count(testInstance, "heartbeat_check_probes"); rows != 0 {
		testInstance.Fatalf("probe rows after delete = %d, want 0", rows)
	}
}

func TestProbeSealedTokenIsNotPlaintextAndBindsToTheCheck(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", probeTestToken())
	other := stack.probeCheck(testInstance, "other", nil)

	var sealed []byte
	if scanErr := stack.pool.QueryRow(stack.ctx,
		`SELECT sealed_access_headers FROM heartbeat_check_probes WHERE check_id = $1`, probe.ID).Scan(&sealed); scanErr != nil {
		testInstance.Fatalf("read sealed bytes: %v", scanErr)
	}
	if len(sealed) == 0 || strings.Contains(string(sealed), probeTestClientSecret) || strings.Contains(string(sealed), probeTestClientID) {
		testInstance.Fatalf("sealed bytes = %x, want non-empty ciphertext without the plaintext", sealed)
	}

	opened, openErr := heartbeat.OpenProbeAccessToken(stack.sealer, probe.ID, sealed)
	if openErr != nil || opened.ClientID != probeTestClientID || opened.ClientSecret != probeTestClientSecret {
		testInstance.Fatalf("open = %+v, %v, want the original token", opened, openErr)
	}

	_, wrongErr := heartbeat.OpenProbeAccessToken(stack.sealer, other.ID, sealed)
	if !errors.Is(wrongErr, seal.ErrDecrypt) {
		testInstance.Fatalf("open under another check id error = %v, want seal.ErrDecrypt", wrongErr)
	}
	requireNoLeak(testInstance, wrongErr)

	var otherSealed []byte
	if scanErr := stack.pool.QueryRow(stack.ctx,
		`SELECT sealed_access_headers FROM heartbeat_check_probes WHERE check_id = $1`, other.ID).Scan(&otherSealed); scanErr != nil {
		testInstance.Fatalf("read other sealed bytes: %v", scanErr)
	}
	if otherSealed != nil {
		testInstance.Fatalf("a probe created without a token has sealed bytes %x", otherSealed)
	}
}

func TestProbeCreateWithTokenAndNilSealerFailsClosed(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "alpha")
	unsealed := heartbeat.NewPGStore(stack.pool, nil)

	_, createErr := unsealed.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "site", DestinationNames: []string{"alpha"},
		Probe: probeTestConfig(), AccessToken: probeTestToken(),
	}, created)

	if createErr == nil {
		testInstance.Fatal("create with a token and a nil sealer succeeded, want an error")
	}
	requireNoLeak(testInstance, createErr)
	if checks, probes := stack.count(testInstance, "heartbeat_checks"), stack.count(testInstance, "heartbeat_check_probes"); checks != 0 || probes != 0 {
		testInstance.Fatalf("rows after failed create = %d checks, %d probes, want none", checks, probes)
	}
}

func TestProbeCreateCheckViolationRollsBackTheParent(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "alpha")
	config := probeTestConfig()
	config.TimeoutSeconds = 9 // outside the probe row's 1..8 CHECK; the parent INSERT is valid

	_, createErr := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "site", DestinationNames: []string{"alpha"},
		Probe: config, AccessToken: probeTestToken(),
	}, created)

	if !errors.Is(createErr, registry.ErrCheckViolation) {
		testInstance.Fatalf("create error = %v, want registry.ErrCheckViolation", createErr)
	}
	// The failing statement is the probe-row insert, so the parent INSERT had succeeded inside the
	// transaction before the rollback.
	if !strings.Contains(createErr.Error(), "insert heartbeat probe") {
		testInstance.Fatalf("create error = %v, want it to come from the probe-row insert", createErr)
	}
	requireNoLeak(testInstance, createErr)
	if rows := stack.count(testInstance, "heartbeat_checks"); rows != 0 {
		testInstance.Fatalf("heartbeat_checks rows after rollback = %d, want 0", rows)
	}
}

func TestProbeCheckRejectsPushPings(testInstance *testing.T) {
	for _, kind := range []string{heartbeat.PingSuccess, heartbeat.PingFail} {
		testInstance.Run(kind, func(subTest *testing.T) {
			stack := newFixture(subTest)
			before := stack.probeCheck(subTest, "site", nil)

			_, pingErr := stack.store.RecordPing(stack.ctx, "billing", "site", kind, created.Add(time.Minute))

			if !errors.Is(pingErr, heartbeat.ErrProbeCheckPing) {
				subTest.Fatalf("ping error = %v, want ErrProbeCheckPing", pingErr)
			}
			after, getErr := stack.store.Get(stack.ctx, before.ID)
			if getErr != nil {
				subTest.Fatalf("get: %v", getErr)
			}
			if after.State != before.State || !after.AnchorAt.Equal(before.AnchorAt) ||
				after.Probe.ConsecutiveFailures != before.Probe.ConsecutiveFailures ||
				!after.UpdatedAt.Equal(before.UpdatedAt) || after.AlertSeq != before.AlertSeq ||
				after.LastPingAt != nil || after.PendingAlert != nil {
				subTest.Fatalf("check changed by a rejected ping: before %+v after %+v", before, after)
			}
		})
	}
}

func TestProbePauseBumpsVersionAndResumeResets(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)
	if _, execErr := stack.pool.Exec(stack.ctx,
		`UPDATE heartbeat_check_probes
SET consecutive_failures = 2, last_result_at = $2, last_result_reason = 'timeout', last_result_detail = 'slow'
WHERE check_id = $1`, probe.ID, created.Add(time.Minute)); execErr != nil {
		testInstance.Fatalf("seed failing result: %v", execErr)
	}

	paused, pauseErr := stack.store.Pause(stack.ctx, probe.ID, created.Add(2*time.Minute))
	if pauseErr != nil {
		testInstance.Fatalf("pause: %v", pauseErr)
	}
	if !paused.Paused || paused.Probe.ConfigVersion != 2 {
		testInstance.Fatalf("paused = paused %v version %d, want true and 2", paused.Paused, paused.Probe.ConfigVersion)
	}
	if paused.Probe.ConsecutiveFailures != 2 || paused.Probe.LastResult == nil {
		testInstance.Fatalf("pause must keep the counter and last result: %+v", paused.Probe)
	}
	if !paused.Probe.NextProbeAt.Equal(probe.Probe.NextProbeAt) {
		testInstance.Fatalf("pause moved next_probe_at to %v, want %v", paused.Probe.NextProbeAt, probe.Probe.NextProbeAt)
	}

	resumeAt := created.Add(time.Hour)
	resumed, resumeErr := stack.store.Resume(stack.ctx, probe.ID, resumeAt)
	if resumeErr != nil {
		testInstance.Fatalf("resume: %v", resumeErr)
	}
	if resumed.Paused || resumed.Probe.ConfigVersion != 3 {
		testInstance.Fatalf("resumed = paused %v version %d, want false and 3", resumed.Paused, resumed.Probe.ConfigVersion)
	}
	if resumed.Probe.ConsecutiveFailures != 0 || resumed.Probe.LastResult != nil {
		testInstance.Fatalf("resume must reset the counter and last result: %+v", resumed.Probe)
	}
	if !resumed.AnchorAt.Equal(resumeAt) ||
		!resumed.Probe.NextProbeAt.Equal(resumeAt.Add(probeTestIntervalSeconds*time.Second)) {
		testInstance.Fatalf("resume anchor %v next_probe_at %v, want %v and %v",
			resumed.AnchorAt, resumed.Probe.NextProbeAt, resumeAt, resumeAt.Add(probeTestIntervalSeconds*time.Second))
	}
}

func TestProbeReasonConstantsMatchTheLiveChecks(testInstance *testing.T) {
	stack := newFixture(testInstance)
	probe := stack.probeCheck(testInstance, "site", nil)

	acceptedProbeReasons := 0
	for _, reason := range heartbeat.KnownDownReasons() {
		if _, execErr := stack.pool.Exec(stack.ctx,
			`UPDATE heartbeat_checks SET state = 'down', down_since = $2, down_reason = $3 WHERE id = $1`,
			probe.ID, created, reason); execErr != nil {
			testInstance.Errorf("down_reason %q rejected by the live CHECK: %v", reason, execErr)
		}

		_, resultErr := stack.pool.Exec(stack.ctx,
			`UPDATE heartbeat_check_probes SET last_result_at = $2, last_result_reason = $3 WHERE check_id = $1`,
			probe.ID, created, reason)
		if heartbeat.IsProbeResultReason(reason) {
			acceptedProbeReasons++
			if resultErr != nil {
				testInstance.Errorf("last_result_reason %q rejected by the live CHECK: %v", reason, resultErr)
			}
		} else if pgutil.ErrorCode(resultErr) != pgutil.SQLStateCheckViolation {
			testInstance.Errorf("last_result_reason %q error = %v, want a CHECK violation", reason, resultErr)
		}
	}
	if acceptedProbeReasons != 8 {
		testInstance.Fatalf("probe result reasons = %d, want 8", acceptedProbeReasons)
	}

	_, unknownErr := stack.pool.Exec(stack.ctx,
		`UPDATE heartbeat_checks SET down_reason = 'bogus' WHERE id = $1`, probe.ID)
	if pgutil.ErrorCode(unknownErr) != pgutil.SQLStateCheckViolation {
		testInstance.Fatalf("unknown down_reason error = %v, want a CHECK violation (23514)", unknownErr)
	}
}

// requireNoLeak fails when err's text carries the plaintext token or the URL query string.
func requireNoLeak(testInstance *testing.T, err error) {
	testInstance.Helper()
	for _, secret := range []string{probeTestClientID, probeTestClientSecret, probeTestSecretQuery} {
		if strings.Contains(err.Error(), secret) {
			testInstance.Fatalf("error text %q leaks %q", err.Error(), secret)
		}
	}
}

package heartbeat

import (
	"reflect"
	"testing"
	"time"
)

const testProbeInterval = 300

func probeCheck(state string, failures int) StoredCheck {
	check := newTestCheck(state)
	check.PeriodSeconds = testProbeInterval
	check.GraceSeconds = testProbeInterval * (ProbeFailureThreshold + 1)
	check.Probe = &ProbeState{
		Config: ProbeConfig{
			URL:              "https://example.com/health",
			ExpectedStatuses: []int{200},
			TimeoutSeconds:   5,
			IntervalSeconds:  testProbeInterval,
		},
		ConsecutiveFailures: failures,
		NextProbeAt:         baseTime,
		ConfigVersion:       4,
	}
	if state == StateDown {
		check.DownSince = timePointer(baseTime.Add(-time.Hour))
		check.DownReason = stringPointer(ReasonHTTPStatus)
		check.Probe.DownDetail = stringPointer("old detail")
	}
	return check
}

func failResult(reason string, detail string) ProbeResult {
	return ProbeResult{Reason: stringPointer(reason), Detail: detail}
}

func intPointer(value int) *int { return &value }

func TestApplyProbeResultTable(t *testing.T) {
	now := baseTime.Add(time.Minute)
	noHoldOff := time.Time{}
	cases := []struct {
		name        string
		check       StoredCheck
		result      ProbeResult
		holdOff     time.Time
		wantApplied bool
		wantAlert   string
		wantState   string
		wantFails   int
		wantReason  string // expected DownReason, "" for nil
		wantDetail  string // expected Probe.DownDetail, "" for nil
	}{
		{"new success goes up silently", probeCheck(StateNew, 0), ProbeResult{}, noHoldOff, true, "", StateUp, 0, "", ""},
		{"up success stays up", probeCheck(StateUp, 2), ProbeResult{}, noHoldOff, true, "", StateUp, 0, "", ""},
		{"up failure below N stays up", probeCheck(StateUp, 0), failResult(ReasonTimeout, "x"), noHoldOff, true, "", StateUp, 1, "", ""},
		{"up failure N-1 to N goes down", probeCheck(StateUp, ProbeFailureThreshold-1), failResult(ReasonTimeout, "slow"), noHoldOff, true, AlertDown, StateDown, ProbeFailureThreshold, ReasonTimeout, "slow"},
		{"new failure Nth goes down", probeCheck(StateNew, ProbeFailureThreshold-1), failResult(ReasonNetwork, ""), noHoldOff, true, AlertDown, StateDown, ProbeFailureThreshold, ReasonNetwork, ""},
		{"new failure below N stays new", probeCheck(StateNew, 0), failResult(ReasonNetwork, ""), noHoldOff, true, "", StateNew, 1, "", ""},
		{"down success recovers", probeCheck(StateDown, 5), ProbeResult{}, noHoldOff, true, AlertRecovered, StateUp, 0, "", ""},
		{"down failure different reason keeps both", probeCheck(StateDown, 5), failResult(ReasonTimeout, "new"), noHoldOff, true, "", StateDown, 6, ReasonHTTPStatus, "old detail"},
		{"down failure same reason keeps both", probeCheck(StateDown, 5), failResult(ReasonHTTPStatus, "new"), noHoldOff, true, "", StateDown, 6, ReasonHTTPStatus, "old detail"},
		{"hold-off defers down", probeCheck(StateUp, ProbeFailureThreshold-1), failResult(ReasonTimeout, "x"), now.Add(time.Hour), true, "", StateUp, ProbeFailureThreshold, "", ""},
		{"hold-off boundary transitions", probeCheck(StateUp, ProbeFailureThreshold-1), failResult(ReasonTimeout, "x"), now, true, AlertDown, StateDown, ProbeFailureThreshold, ReasonTimeout, "x"},
		{"hold-off success resets without transition", probeCheck(StateUp, ProbeFailureThreshold), ProbeResult{}, now.Add(time.Hour), true, "", StateUp, 0, "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			after, applied, emitted := ApplyProbeResult(testCase.check, testCase.result, now, testCase.holdOff)
			if applied != testCase.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, testCase.wantApplied)
			}
			assertEmitted(t, testCase.check, after, emitted, testCase.wantAlert)
			if after.State != testCase.wantState {
				t.Fatalf("State = %q, want %q", after.State, testCase.wantState)
			}
			if after.Probe.ConsecutiveFailures != testCase.wantFails {
				t.Fatalf("failures = %d, want %d", after.Probe.ConsecutiveFailures, testCase.wantFails)
			}
			if got := derefString(after.DownReason); got != testCase.wantReason {
				t.Fatalf("DownReason = %q, want %q", got, testCase.wantReason)
			}
			if got := derefString(after.Probe.DownDetail); got != testCase.wantDetail {
				t.Fatalf("DownDetail = %q, want %q", got, testCase.wantDetail)
			}
			if !after.AnchorAt.Equal(now) {
				t.Fatalf("AnchorAt = %v, want %v", after.AnchorAt, now)
			}
			if after.Probe.LastResult == nil || !after.Probe.LastResult.At.Equal(now) {
				t.Fatalf("LastResult = %+v, want At %v", after.Probe.LastResult, now)
			}
			success := testCase.result.Reason == nil
			if success {
				if after.LastPingAt == nil || !after.LastPingAt.Equal(now) || derefString(after.LastPingKind) != PingSuccess {
					t.Fatalf("last ping = %v / %v, want success at now", after.LastPingAt, after.LastPingKind)
				}
			} else if !reflect.DeepEqual(after.LastPingAt, testCase.check.LastPingAt) {
				t.Fatalf("LastPingAt changed on a failure: %v", after.LastPingAt)
			}
			if testCase.wantAlert == AlertRecovered && after.DownSince == nil {
				t.Fatal("DownSince dropped on recovery")
			}
		})
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestApplyProbeResultDownMissedIsOverwritten(t *testing.T) {
	check := probeCheck(StateDown, 0)
	check.DownReason = stringPointer(ReasonMissed)
	check.Probe.DownDetail = nil
	after, applied, emitted := ApplyProbeResult(check, failResult(ReasonTLS, "bad cert"), baseTime.Add(time.Minute), time.Time{})
	if !applied || emitted {
		t.Fatalf("applied/emitted = %v/%v, want true/false", applied, emitted)
	}
	if derefString(after.DownReason) != ReasonTLS || derefString(after.Probe.DownDetail) != "bad cert" {
		t.Fatalf("reason/detail = %q/%q", derefString(after.DownReason), derefString(after.Probe.DownDetail))
	}
}

func TestApplyProbeResultDownUnrecognisedReasonKept(t *testing.T) {
	check := probeCheck(StateDown, 0)
	check.DownReason = stringPointer("mystery")
	after, applied, emitted := ApplyProbeResult(check, failResult(ReasonTLS, "bad cert"), baseTime.Add(time.Minute), time.Time{})
	if !applied || emitted {
		t.Fatalf("applied/emitted = %v/%v, want true/false", applied, emitted)
	}
	if derefString(after.DownReason) != "mystery" || derefString(after.Probe.DownDetail) != "old detail" {
		t.Fatalf("reason/detail = %q/%q", derefString(after.DownReason), derefString(after.Probe.DownDetail))
	}
}

func TestApplyProbeResultStatusRange(t *testing.T) {
	cases := []struct {
		name   string
		status *int
		want   *int
	}{
		{"nil", nil, nil},
		{"100", intPointer(100), intPointer(100)},
		{"599", intPointer(599), intPointer(599)},
		{"600", intPointer(600), nil},
		{"999", intPointer(999), nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := ProbeResult{Status: testCase.status, Reason: stringPointer(ReasonHTTPStatus)}
			after, applied, _ := ApplyProbeResult(probeCheck(StateUp, 0), result, baseTime.Add(time.Minute), time.Time{})
			if !applied {
				t.Fatal("applied = false, want true")
			}
			if after.Probe.ConsecutiveFailures != 1 {
				t.Fatalf("failures = %d, want 1", after.Probe.ConsecutiveFailures)
			}
			if !reflect.DeepEqual(after.Probe.LastResult.Status, testCase.want) {
				t.Fatalf("Status = %v, want %v", after.Probe.LastResult.Status, testCase.want)
			}
		})
	}
}

func TestApplyProbeResultOutOfRangeStatusCanGoDown(t *testing.T) {
	result := ProbeResult{Status: intPointer(999), Reason: stringPointer(ReasonHTTPStatus)}
	after, applied, emitted := ApplyProbeResult(probeCheck(StateUp, ProbeFailureThreshold-1), result, baseTime.Add(time.Minute), time.Time{})
	if !applied || !emitted || after.State != StateDown {
		t.Fatalf("applied/emitted/state = %v/%v/%q", applied, emitted, after.State)
	}
}

func TestApplyProbeResultEmptyDetailStoredAsNil(t *testing.T) {
	after, _, _ := ApplyProbeResult(probeCheck(StateUp, ProbeFailureThreshold-1), failResult(ReasonTimeout, ""), baseTime.Add(time.Minute), time.Time{})
	if after.Probe.LastResult.Detail != nil || after.Probe.DownDetail != nil {
		t.Fatalf("detail pointers = %v / %v, want nil", after.Probe.LastResult.Detail, after.Probe.DownDetail)
	}
	after, _, _ = ApplyProbeResult(probeCheck(StateUp, ProbeFailureThreshold-1), failResult(ReasonTimeout, "  \n "), baseTime.Add(time.Minute), time.Time{})
	if after.Probe.LastResult.Detail != nil && *after.Probe.LastResult.Detail == "" {
		t.Fatal("LastResult.Detail is a pointer to an empty string")
	}
	after, _, _ = ApplyProbeResult(probeCheck(StateUp, ProbeFailureThreshold-1), failResult(ReasonTimeout, "boom"), baseTime.Add(time.Minute), time.Time{})
	if derefString(after.Probe.LastResult.Detail) != "boom" || derefString(after.Probe.DownDetail) != "boom" {
		t.Fatalf("detail = %q / %q, want boom", derefString(after.Probe.LastResult.Detail), derefString(after.Probe.DownDetail))
	}
}

func TestApplyProbeResultSanitisesDetail(t *testing.T) {
	after, _, _ := ApplyProbeResult(probeCheck(StateUp, 0), failResult(ReasonNetwork, "dial https://user:pw@host.example/path?q=1 failed"), baseTime.Add(time.Minute), time.Time{})
	got := derefString(after.Probe.LastResult.Detail)
	if got != SanitizeProbeDetail("dial https://user:pw@host.example/path?q=1 failed") || got == "" {
		t.Fatalf("detail = %q", got)
	}
}

func TestApplyProbeResultDenies(t *testing.T) {
	paused := probeCheck(StateUp, 0)
	paused.Paused = true
	cases := []struct {
		name   string
		check  StoredCheck
		result ProbeResult
	}{
		{"paused", paused, failResult(ReasonTimeout, "")},
		{"push check", newTestCheck(StateUp), failResult(ReasonTimeout, "")},
		{"unknown state", probeCheck("bogus", 0), failResult(ReasonTimeout, "")},
		{"missed reason", probeCheck(StateUp, 0), failResult(ReasonMissed, "")},
		{"fail reason", probeCheck(StateUp, 0), failResult(ReasonFail, "")},
		{"unknown reason", probeCheck(StateUp, 0), failResult("nope", "")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := deepCopyCheck(testCase.check)
			after, applied, emitted := ApplyProbeResult(testCase.check, testCase.result, baseTime.Add(time.Minute), time.Time{})
			if applied || emitted {
				t.Fatalf("applied/emitted = %v/%v, want false/false", applied, emitted)
			}
			if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(testCase.check, before) {
				t.Fatal("denied result changed the check")
			}
		})
	}
}

func deepCopyCheck(check StoredCheck) StoredCheck {
	copied := check
	if check.Probe != nil {
		copied.Probe = cloneProbe(check.Probe)
	}
	return copied
}

func TestHoldOffDefersThenTransitionsOnce(t *testing.T) {
	holdOff := baseTime.Add(10 * time.Minute)
	check := probeCheck(StateUp, 0)
	for step := 1; step <= ProbeFailureThreshold+2; step++ {
		now := baseTime.Add(time.Duration(step) * time.Minute)
		after, applied, emitted := ApplyProbeResult(check, failResult(ReasonTimeout, "x"), now, holdOff)
		if !applied || emitted || after.State != StateUp {
			t.Fatalf("step %d inside hold-off: applied/emitted/state = %v/%v/%q", step, applied, emitted, after.State)
		}
		check = after
	}
	if check.Probe.ConsecutiveFailures != ProbeFailureThreshold+2 {
		t.Fatalf("failures = %d, want %d", check.Probe.ConsecutiveFailures, ProbeFailureThreshold+2)
	}
	first, _, emitted := ApplyProbeResult(check, failResult(ReasonTimeout, "x"), holdOff, holdOff)
	if !emitted || first.State != StateDown {
		t.Fatalf("first after hold-off: emitted/state = %v/%q", emitted, first.State)
	}
	second, applied, emitted := ApplyProbeResult(first, failResult(ReasonTimeout, "x"), holdOff.Add(time.Minute), holdOff)
	if !applied || emitted || second.AlertSeq != first.AlertSeq {
		t.Fatalf("second: applied/emitted = %v/%v, want true/false", applied, emitted)
	}
}

func TestDerivedStateProbeNeverLate(t *testing.T) {
	now := baseTime.Add(time.Duration(testProbeInterval*3) * time.Second)
	if got := DerivedState(probeCheck(StateUp, 0), now); got != StateUp {
		t.Fatalf("probe DerivedState = %q, want up", got)
	}
	push := newTestCheck(StateUp)
	if got := DerivedState(push, baseTime.Add(time.Duration(periodSeconds+1)*time.Second)); got != StateLate {
		t.Fatalf("push DerivedState = %q, want late", got)
	}
}

func TestApplyPingOnProbeIsNoOp(t *testing.T) {
	check := probeCheck(StateNew, 0)
	after, emitted := ApplyPing(check, PingFail, baseTime.Add(time.Minute))
	if emitted || !reflect.DeepEqual(after, check) {
		t.Fatalf("ApplyPing changed a probe check: emitted=%v", emitted)
	}
}

func TestApplyPauseProbeBumpsVersionOnce(t *testing.T) {
	check := probeCheck(StateUp, 0)
	paused := ApplyPause(check)
	if !paused.Paused || paused.Probe.ConfigVersion != check.Probe.ConfigVersion+1 {
		t.Fatalf("paused/version = %v/%d", paused.Paused, paused.Probe.ConfigVersion)
	}
	again := ApplyPause(paused)
	if again.Probe.ConfigVersion != paused.Probe.ConfigVersion {
		t.Fatalf("second pause bumped version to %d", again.Probe.ConfigVersion)
	}
}

func TestApplyResumeProbeResets(t *testing.T) {
	check := probeCheck(StateUp, 2)
	check.Paused = true
	check.Probe.LastResult = &ProbeResultRecord{At: baseTime, Reason: stringPointer(ReasonTimeout)}
	now := baseTime.Add(time.Hour)
	after := ApplyResume(check, now)
	if after.Paused || after.Probe.ConsecutiveFailures != 0 || after.Probe.LastResult != nil {
		t.Fatalf("resume did not reset: %+v", after.Probe)
	}
	if after.Probe.ConfigVersion != check.Probe.ConfigVersion+1 {
		t.Fatalf("version = %d", after.Probe.ConfigVersion)
	}
	if !after.Probe.NextProbeAt.Equal(now.Add(testProbeInterval*time.Second)) || !after.AnchorAt.Equal(now) {
		t.Fatalf("next/anchor = %v/%v", after.Probe.NextProbeAt, after.AnchorAt)
	}
}

func TestApplyResumeUnpausedProbeIsNoOp(t *testing.T) {
	check := probeCheck(StateUp, 2)
	after := ApplyResume(check, baseTime.Add(time.Hour))
	if !reflect.DeepEqual(after, check) {
		t.Fatal("resume of an unpaused probe changed it")
	}
}

func TestApplyResumeDownProbeKeepsDownFields(t *testing.T) {
	check := probeCheck(StateDown, 5)
	check.Paused = true
	after := ApplyResume(check, baseTime.Add(time.Hour))
	if after.State != StateDown || derefString(after.DownReason) != ReasonHTTPStatus || derefString(after.Probe.DownDetail) != "old detail" {
		t.Fatalf("down fields changed: %q %q %q", after.State, derefString(after.DownReason), derefString(after.Probe.DownDetail))
	}
}

func TestApplyProbeResetPushUnchangedAndDownKept(t *testing.T) {
	push := newTestCheck(StateUp)
	if after := ApplyProbeReset(push, baseTime.Add(time.Hour)); !reflect.DeepEqual(after, push) {
		t.Fatal("reset changed a push check")
	}
	down := probeCheck(StateDown, 5)
	after := ApplyProbeReset(down, baseTime.Add(time.Hour))
	if after.State != StateDown || derefString(after.DownReason) != ReasonHTTPStatus || derefString(after.Probe.DownDetail) != "old detail" {
		t.Fatal("reset changed down fields")
	}
}

func TestAdvanceTickProbeMissedClearsDetail(t *testing.T) {
	check := probeCheck(StateUp, 1)
	check.Probe.DownDetail = stringPointer("stale")
	now := Deadline(check).Add(time.Second)
	after, changed := AdvanceTick(check, now)
	if !changed || after.State != StateDown || derefString(after.DownReason) != ReasonMissed || after.Probe.DownDetail != nil {
		t.Fatalf("changed/state/detail = %v/%q/%v", changed, after.State, after.Probe.DownDetail)
	}
}

func TestAdvanceTickProbeFreshAnchorStaysUp(t *testing.T) {
	check := probeCheck(StateUp, ProbeFailureThreshold-1)
	after, changed := AdvanceTick(check, check.AnchorAt.Add(time.Duration(testProbeInterval)*time.Second))
	if changed || after.State != StateUp {
		t.Fatalf("changed/state = %v/%q", changed, after.State)
	}
}

func TestProbeTransitionsAreCopyOnWrite(t *testing.T) {
	downByTick := probeCheck(StateUp, 1)
	resultNow := baseTime.Add(time.Minute)
	cases := []struct {
		name  string
		check StoredCheck
		apply func(StoredCheck) StoredCheck
	}{
		{"pause", probeCheck(StateUp, 0), ApplyPause},
		{"resume", func() StoredCheck { c := probeCheck(StateUp, 2); c.Paused = true; return c }(), func(c StoredCheck) StoredCheck { return ApplyResume(c, resultNow) }},
		{"reset", probeCheck(StateUp, 2), func(c StoredCheck) StoredCheck { return ApplyProbeReset(c, resultNow) }},
		{"tick", downByTick, func(c StoredCheck) StoredCheck {
			next, _ := AdvanceTick(c, Deadline(c).Add(time.Second))
			return next
		}},
		{"result failure", probeCheck(StateUp, 1), func(c StoredCheck) StoredCheck {
			next, _, _ := ApplyProbeResult(c, failResult(ReasonTimeout, "x"), resultNow, time.Time{})
			return next
		}},
		{"result recovery", probeCheck(StateDown, 4), func(c StoredCheck) StoredCheck {
			next, _, _ := ApplyProbeResult(c, ProbeResult{}, resultNow, time.Time{})
			return next
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := deepCopyCheck(testCase.check)
			original := testCase.check.Probe
			after := testCase.apply(testCase.check)
			if !reflect.DeepEqual(testCase.check, before) {
				t.Fatal("input StoredCheck was mutated")
			}
			if !reflect.DeepEqual(*original, *before.Probe) {
				t.Fatal("input ProbeState was mutated")
			}
			if after.Probe == original {
				t.Fatal("returned Probe is the same pointer as the input's")
			}
		})
	}
}

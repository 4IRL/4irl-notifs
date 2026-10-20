package heartbeat

import (
	"testing"
	"time"
)

var (
	baseTime = time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	// period 600 s + grace 300 s: the deadline is baseTime + 900 s.
	periodSeconds = 600
	graceSeconds  = 300
)

func newTestCheck(state string) StoredCheck {
	return StoredCheck{
		ID:            1,
		AppID:         "billing",
		Name:          "nightly",
		PeriodSeconds: periodSeconds,
		GraceSeconds:  graceSeconds,
		State:         state,
		AnchorAt:      baseTime,
	}
}

func downCheck(reason string) StoredCheck {
	check := newTestCheck(StateDown)
	check.DownSince = timePointer(baseTime.Add(-time.Hour))
	check.DownReason = stringPointer(reason)
	return check
}

func pendingOf(check StoredCheck) string {
	if check.PendingAlert == nil {
		return ""
	}
	return *check.PendingAlert
}

func assertEmitted(t *testing.T, before StoredCheck, after StoredCheck, emitted bool, wantAlert string) {
	t.Helper()
	if wantAlert == "" {
		if emitted {
			t.Fatalf("emitted = true, want no alert")
		}
		if after.AlertSeq != before.AlertSeq {
			t.Fatalf("AlertSeq = %d, want unchanged %d", after.AlertSeq, before.AlertSeq)
		}
		return
	}
	if !emitted {
		t.Fatalf("emitted = false, want a %q alert", wantAlert)
	}
	if got := pendingOf(after); got != wantAlert {
		t.Fatalf("PendingAlert = %q, want %q", got, wantAlert)
	}
	if after.AlertSeq != before.AlertSeq+1 {
		t.Fatalf("AlertSeq = %d, want %d", after.AlertSeq, before.AlertSeq+1)
	}
}

func TestApplyPingSuccessNewBecomesUpWithoutAlert(t *testing.T) {
	before := newTestCheck(StateNew)
	now := baseTime.Add(time.Minute)

	after, emitted := ApplyPing(before, PingSuccess, now)

	assertEmitted(t, before, after, emitted, "")
	if after.State != StateUp {
		t.Fatalf("State = %q, want up", after.State)
	}
	if after.LastPingAt == nil || !after.LastPingAt.Equal(now) || after.LastPingKind == nil || *after.LastPingKind != PingSuccess {
		t.Fatalf("last ping = %v / %v, want success at %v", after.LastPingAt, after.LastPingKind, now)
	}
	if !after.AnchorAt.Equal(now) {
		t.Fatalf("AnchorAt = %v, want %v", after.AnchorAt, now)
	}
	if after.PendingAlert != nil {
		t.Fatalf("PendingAlert = %q, want nil", *after.PendingAlert)
	}
}

func TestApplyPingSuccessKeepsUpCheckUpWithoutAlert(t *testing.T) {
	before := newTestCheck(StateUp)
	now := baseTime.Add(time.Minute)

	after, emitted := ApplyPing(before, PingSuccess, now)

	assertEmitted(t, before, after, emitted, "")
	if after.State != StateUp || !after.AnchorAt.Equal(now) {
		t.Fatalf("state/anchor = %q/%v, want up at %v", after.State, after.AnchorAt, now)
	}
}

func TestApplyPingSuccessRecoversDownCheckWithAlert(t *testing.T) {
	for _, reason := range []string{ReasonMissed, ReasonFail} {
		t.Run(reason, func(t *testing.T) {
			before := downCheck(reason)
			before.AlertSeq = 3
			now := baseTime.Add(time.Minute)

			after, emitted := ApplyPing(before, PingSuccess, now)

			assertEmitted(t, before, after, emitted, AlertRecovered)
			if after.State != StateUp {
				t.Fatalf("State = %q, want up", after.State)
			}
			if after.DownReason != nil {
				t.Fatalf("DownReason = %q, want cleared", *after.DownReason)
			}
			if after.DownSince == nil || !after.DownSince.Equal(*before.DownSince) {
				t.Fatalf("DownSince = %v, want kept at %v for the recovery alert", after.DownSince, *before.DownSince)
			}
			if got := ActiveDownSince(after); got != nil {
				t.Fatalf("ActiveDownSince = %v, want nil once up", got)
			}
		})
	}
}

func TestApplyPingFailTakesNewAndUpDownWithAlert(t *testing.T) {
	for _, state := range []string{StateNew, StateUp} {
		t.Run(state, func(t *testing.T) {
			before := newTestCheck(state)
			now := baseTime.Add(time.Minute)

			after, emitted := ApplyPing(before, PingFail, now)

			assertEmitted(t, before, after, emitted, AlertDown)
			if after.State != StateDown {
				t.Fatalf("State = %q, want down", after.State)
			}
			if after.DownReason == nil || *after.DownReason != ReasonFail {
				t.Fatalf("DownReason = %v, want fail", after.DownReason)
			}
			if got := ActiveDownSince(after); got == nil || !got.Equal(now) {
				t.Fatalf("ActiveDownSince = %v, want %v", got, now)
			}
			if after.LastPingKind == nil || *after.LastPingKind != PingFail {
				t.Fatalf("LastPingKind = %v, want fail", after.LastPingKind)
			}
		})
	}
}

func TestApplyPingFailWhileDownEmitsNothing(t *testing.T) {
	before := downCheck(ReasonMissed)
	before.AlertSeq = 2
	now := baseTime.Add(time.Minute)

	after, emitted := ApplyPing(before, PingFail, now)

	assertEmitted(t, before, after, emitted, "")
	if after.State != StateDown || *after.DownReason != ReasonMissed {
		t.Fatalf("state/reason = %q/%q, want down/missed unchanged", after.State, *after.DownReason)
	}
	if after.LastPingAt == nil || !after.LastPingAt.Equal(now) || !after.AnchorAt.Equal(now) {
		t.Fatalf("ping not recorded: last=%v anchor=%v", after.LastPingAt, after.AnchorAt)
	}
}

func TestApplyPingWhilePausedMovesStateWithoutAlert(t *testing.T) {
	tests := []struct {
		name      string
		check     StoredCheck
		kind      string
		wantState string
	}{
		{"down recovers silently", downCheck(ReasonMissed), PingSuccess, StateUp},
		{"new goes up", newTestCheck(StateNew), PingSuccess, StateUp},
		{"up fails silently", newTestCheck(StateUp), PingFail, StateDown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.check
			before.Paused = true
			now := baseTime.Add(time.Minute)

			after, emitted := ApplyPing(before, test.kind, now)

			assertEmitted(t, before, after, emitted, "")
			if after.State != test.wantState {
				t.Fatalf("State = %q, want %q", after.State, test.wantState)
			}
			if after.PendingAlert != nil {
				t.Fatalf("PendingAlert = %q, want nil while paused", *after.PendingAlert)
			}
			if !after.Paused {
				t.Fatal("Paused = false, want still paused")
			}
			if after.LastPingAt == nil || !after.LastPingAt.Equal(now) {
				t.Fatalf("LastPingAt = %v, want %v recorded", after.LastPingAt, now)
			}
		})
	}
}

func TestApplyPingOverwritesAnUnflushedAlert(t *testing.T) {
	// A check went down (unflushed down alert, seq 1), then a success ping
	// arrives before any tick flushed it: the outbox becomes recovered, seq 2.
	before := downCheck(ReasonMissed)
	before.AlertSeq = 1
	before.PendingAlert = stringPointer(AlertDown)

	after, emitted := ApplyPing(before, PingSuccess, baseTime.Add(time.Minute))

	assertEmitted(t, before, after, emitted, AlertRecovered)
	if after.AlertSeq != 2 {
		t.Fatalf("AlertSeq = %d, want 2", after.AlertSeq)
	}
}

func TestApplyPingUnknownKindOrStateIsUnchanged(t *testing.T) {
	before := newTestCheck(StateUp)
	if after, emitted := ApplyPing(before, "bogus", baseTime.Add(time.Minute)); emitted || after.AnchorAt != before.AnchorAt || after.LastPingAt != nil {
		t.Fatalf("unknown kind changed the check: %+v emitted=%v", after, emitted)
	}
	odd := newTestCheck("bogus")
	if after, emitted := ApplyPing(odd, PingSuccess, baseTime.Add(time.Minute)); emitted || after.State != "bogus" || after.LastPingAt != nil {
		t.Fatalf("unknown state changed the check: %+v emitted=%v", after, emitted)
	}
}

func TestApplyPingDoesNotMutateItsInput(t *testing.T) {
	before := downCheck(ReasonFail)
	snapshotSince := *before.DownSince
	snapshotReason := *before.DownReason

	ApplyPing(before, PingSuccess, baseTime.Add(time.Minute))

	if before.State != StateDown || !before.DownSince.Equal(snapshotSince) || *before.DownReason != snapshotReason {
		t.Fatalf("input mutated: %+v", before)
	}
}

func TestAdvanceTickMissedDeadline(t *testing.T) {
	for _, state := range []string{StateNew, StateUp} {
		t.Run(state, func(t *testing.T) {
			before := newTestCheck(state)
			now := Deadline(before).Add(time.Nanosecond)

			after, changed := AdvanceTick(before, now)

			if !changed {
				t.Fatal("changed = false, want the check to go down")
			}
			assertEmitted(t, before, after, changed, AlertDown)
			if after.State != StateDown || after.DownReason == nil || *after.DownReason != ReasonMissed {
				t.Fatalf("state/reason = %q/%v, want down/missed", after.State, after.DownReason)
			}
			if got := ActiveDownSince(after); got == nil || !got.Equal(now) {
				t.Fatalf("ActiveDownSince = %v, want %v", got, now)
			}
		})
	}
}

func TestAdvanceTickDeadlineBoundary(t *testing.T) {
	before := newTestCheck(StateUp)
	deadline := Deadline(before)
	if want := baseTime.Add(900 * time.Second); !deadline.Equal(want) {
		t.Fatalf("Deadline = %v, want %v", deadline, want)
	}

	if _, changed := AdvanceTick(before, deadline.Add(-time.Nanosecond)); changed {
		t.Fatal("just before the deadline went down")
	}
	if _, changed := AdvanceTick(before, deadline); changed {
		t.Fatal("exactly at the deadline went down, want strictly after")
	}
	if _, changed := AdvanceTick(before, deadline.Add(time.Nanosecond)); !changed {
		t.Fatal("just after the deadline did not go down")
	}
}

func TestAdvanceTickLeavesDownAndPausedChecksAlone(t *testing.T) {
	late := baseTime.Add(24 * time.Hour)

	down := downCheck(ReasonFail)
	if after, changed := AdvanceTick(down, late); changed || after.AlertSeq != down.AlertSeq {
		t.Fatalf("down check ticked: %+v changed=%v", after, changed)
	}

	for _, state := range []string{StateNew, StateUp} {
		paused := newTestCheck(state)
		paused.Paused = true
		if after, changed := AdvanceTick(paused, late); changed || after.State != state || after.PendingAlert != nil {
			t.Fatalf("paused %s check ticked: %+v changed=%v", state, after, changed)
		}
	}

	odd := newTestCheck("bogus")
	if _, changed := AdvanceTick(odd, late); changed {
		t.Fatal("unknown state ticked, want unchanged")
	}
}

func TestAdvanceTickOverwritesAnUnflushedAlert(t *testing.T) {
	// An up check with a leftover unflushed recovered alert goes down on a tick:
	// the outbox now holds only the down alert.
	before := newTestCheck(StateUp)
	before.AlertSeq = 4
	before.PendingAlert = stringPointer(AlertRecovered)

	after, changed := AdvanceTick(before, Deadline(before).Add(time.Second))

	assertEmitted(t, before, after, changed, AlertDown)
}

func TestApplyPauseClearsPendingAlert(t *testing.T) {
	before := downCheck(ReasonMissed)
	before.AlertSeq = 1
	before.PendingAlert = stringPointer(AlertDown)

	after := ApplyPause(before)

	if !after.Paused || after.PendingAlert != nil {
		t.Fatalf("paused=%v pending=%v, want paused with the outbox cleared", after.Paused, after.PendingAlert)
	}
	if after.AlertSeq != 1 || after.State != StateDown {
		t.Fatalf("AlertSeq/State = %d/%q, want 1/down untouched", after.AlertSeq, after.State)
	}
	if again := ApplyPause(after); !again.Paused || again.PendingAlert != nil {
		t.Fatalf("pausing a paused check changed it: %+v", again)
	}
}

func TestApplyResumeResetsAnchorWithoutEmitting(t *testing.T) {
	before := newTestCheck(StateUp)
	before.Paused = true
	now := baseTime.Add(2 * time.Hour)

	after := ApplyResume(before, now)

	if after.Paused || !after.AnchorAt.Equal(now) {
		t.Fatalf("paused=%v anchor=%v, want resumed with anchor %v", after.Paused, after.AnchorAt, now)
	}
	if after.AlertSeq != before.AlertSeq || after.PendingAlert != nil || after.State != StateUp {
		t.Fatalf("resume emitted or changed state: %+v", after)
	}
}

func TestApplyResumeOnRunningCheckIsNoOp(t *testing.T) {
	before := newTestCheck(StateUp)

	after := ApplyResume(before, baseTime.Add(2*time.Hour))

	if !after.AnchorAt.Equal(before.AnchorAt) {
		t.Fatalf("resume of a running check moved the anchor to %v", after.AnchorAt)
	}
}

func TestDerivedStateLateBoundary(t *testing.T) {
	up := newTestCheck(StateUp)
	period := time.Duration(periodSeconds) * time.Second
	deadline := Deadline(up)

	tests := []struct {
		name  string
		check StoredCheck
		now   time.Time
		want  string
	}{
		{"up before the period", up, baseTime.Add(period - time.Nanosecond), StateUp},
		{"up exactly at the period", up, baseTime.Add(period), StateUp},
		{"up just past the period", up, baseTime.Add(period + time.Nanosecond), StateLate},
		{"up inside the grace", up, baseTime.Add(period + time.Minute), StateLate},
		{"up exactly at the deadline", up, deadline, StateLate},
		{"up past the deadline before a tick", up, deadline.Add(time.Hour), StateLate},
		{"new never late", newTestCheck(StateNew), deadline.Add(time.Hour), StateNew},
		{"down stays down", downCheck(ReasonFail), deadline.Add(time.Hour), StateDown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DerivedState(test.check, test.now); got != test.want {
				t.Fatalf("DerivedState = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDerivedStateIgnoresPaused(t *testing.T) {
	// Paused is a separate flag: the read-time builder checks it first for display.
	paused := newTestCheck(StateUp)
	paused.Paused = true
	if got := DerivedState(paused, baseTime); got != StateUp {
		t.Fatalf("DerivedState(paused up) = %q, want up", got)
	}
}

func TestActiveDownSinceOnlyWhileDown(t *testing.T) {
	if got := ActiveDownSince(newTestCheck(StateUp)); got != nil {
		t.Fatalf("ActiveDownSince(up) = %v, want nil", got)
	}
	down := downCheck(ReasonMissed)
	if got := ActiveDownSince(down); got == nil || !got.Equal(*down.DownSince) {
		t.Fatalf("ActiveDownSince(down) = %v, want %v", got, *down.DownSince)
	}
}

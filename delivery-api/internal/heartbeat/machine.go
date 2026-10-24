package heartbeat

import (
	"slices"
	"time"
)

// The state machine is a set of pure functions over a StoredCheck and `now`.
// Each returns an updated copy and never touches the input (pointer fields are
// replaced, never written through). The store applies them under a row lock and
// persists the result, so the outbox fields (AlertSeq, PendingAlert) move in the
// same transaction as the state transition that emits the alert.
//
// An unrecognized stored state or ping kind is left unchanged and emits
// nothing: the machine never invents a transition for input it does not know.

// Deadline returns the instant after which an unpaused new or up check goes
// down: AnchorAt plus the period plus the grace.
func Deadline(check StoredCheck) time.Time {
	return check.AnchorAt.Add(time.Duration(check.PeriodSeconds+check.GraceSeconds) * time.Second)
}

// DerivedState returns the read-time state of a check: its stored state, except
// that an up check is "late" once now is strictly past AnchorAt plus the period.
// Paused is a separate flag and does not change the derived state; a display
// that wants paused to win checks Paused first. Late is never stored or alerted.
func DerivedState(check StoredCheck, now time.Time) string {
	if check.Probe != nil {
		// A probe check is driven by results, not a ping clock, so it is never late.
		return check.State
	}
	if check.State == StateUp && now.After(check.AnchorAt.Add(time.Duration(check.PeriodSeconds)*time.Second)) {
		return StateLate
	}
	return check.State
}

// ActiveDownSince returns when a currently-down check went down, or nil when the
// check is not down (DownSince is retained after a recovery for the alert).
func ActiveDownSince(check StoredCheck) *time.Time {
	if check.State != StateDown {
		return nil
	}
	return check.DownSince
}

// ApplyPing records a ping at now and reports whether it emitted an alert. A paused check
// still moves state but emits nothing; an emitted alert overwrites an unflushed one so only
// the latest state is announced.
func ApplyPing(check StoredCheck, kind string, now time.Time) (StoredCheck, bool) {
	if check.Probe != nil {
		// Defence in depth: the store raises ErrProbeCheckPing before getting here.
		return check, false
	}
	next := check
	switch kind {
	case PingSuccess:
		switch check.State {
		case StateNew:
			next.State = StateUp
		case StateUp:
			// Already up: only the ping bookkeeping below changes.
		case StateDown:
			next.State = StateUp
			next.DownReason = nil
		default:
			return check, false
		}
	case PingFail:
		switch check.State {
		case StateNew, StateUp:
			next.State = StateDown
			next.DownSince = timePointer(now)
			next.DownReason = stringPointer(ReasonFail)
		case StateDown:
			// Already down: only the ping bookkeeping below changes.
		default:
			return check, false
		}
	default:
		return check, false
	}

	next.LastPingAt = timePointer(now)
	next.LastPingKind = stringPointer(kind)
	next.AnchorAt = now

	stateChanged := next.State != check.State
	if !stateChanged {
		return next, false
	}
	if check.Paused {
		next.PendingAlert = nil
		return next, false
	}
	if next.State == StateDown {
		return emitAlert(next, AlertDown), true
	}
	if check.State == StateDown {
		return emitAlert(next, AlertRecovered), true
	}
	// new -> up: nothing to announce.
	return next, false
}

// AdvanceTick applies the checker tick at now: an unpaused new or up check
// strictly past its deadline goes down (reason missed) and emits a down alert.
// It reports whether the check changed. Paused and already-down checks never
// transition on a tick.
func AdvanceTick(check StoredCheck, now time.Time) (StoredCheck, bool) {
	if check.Paused || (check.State != StateNew && check.State != StateUp) {
		return check, false
	}
	if !now.After(Deadline(check)) {
		return check, false
	}
	next := check
	next.State = StateDown
	next.DownSince = timePointer(now)
	next.DownReason = stringPointer(ReasonMissed)
	if check.Probe != nil {
		next.Probe = cloneProbe(check.Probe)
		next.Probe.DownDetail = nil
	}
	return emitAlert(next, AlertDown), true
}

// ApplyPause pauses the check and clears the stored pending alert. An alert already read by an
// in-flight Tick flush (one flush duration) can still be announced once. Pausing a paused
// check is a no-op. Pausing a probe check also bumps its config version so an in-flight
// probe result is rejected.
func ApplyPause(check StoredCheck) StoredCheck {
	next := check
	if check.Probe != nil && !check.Paused {
		next.Probe = cloneProbe(check.Probe)
		next.Probe.ConfigVersion++
	}
	next.Paused = true
	next.PendingAlert = nil
	return next
}

// ApplyResume resumes a paused check and restarts its deadline clock at now. It
// emits nothing. Resuming a check that is not paused is a no-op, so it cannot be
// used to push a live deadline out.
func ApplyResume(check StoredCheck, now time.Time) StoredCheck {
	if !check.Paused {
		return check
	}
	next := check
	next.Paused = false
	next.AnchorAt = now
	if check.Probe != nil {
		next = ApplyProbeReset(next, now)
	}
	return next
}

// ApplyProbeReset restarts a probe check's progress at now: it clears the failure counter and
// last result, bumps the config version, schedules the next probe one interval out and restarts
// the anchor. State, DownReason and DownDetail are untouched. A push check is returned
// unchanged. The interval comes from the Probe.Config the check carries, so a caller resetting
// after an edit must first put the merged config on the check.
func ApplyProbeReset(check StoredCheck, now time.Time) StoredCheck {
	if check.Probe == nil {
		return check
	}
	next := check
	next.Probe = cloneProbe(check.Probe)
	next.Probe.ConsecutiveFailures = 0
	next.Probe.LastResult = nil
	next.Probe.ConfigVersion++
	next.Probe.NextProbeAt = now.Add(time.Duration(check.Probe.Config.IntervalSeconds) * time.Second)
	next.AnchorAt = now
	return next
}

// ApplyProbeResult applies one probe result at now and returns the updated check, whether the
// result was applied and whether it emitted an alert. A check goes down once the failure counter
// reaches ProbeFailureThreshold and now is not before holdOffUntil; inside the hold-off the
// counter and last result are recorded but the transition is deferred. A paused check, a push
// check, an unknown stored state or a reason that is not a probe reason is denied: the input
// is returned unchanged with applied and emitted false.
func ApplyProbeResult(check StoredCheck, result ProbeResult, now time.Time, holdOffUntil time.Time) (StoredCheck, bool, bool) {
	if check.Probe == nil || check.Paused {
		return check, false, false
	}
	if check.State != StateNew && check.State != StateUp && check.State != StateDown {
		return check, false, false
	}
	success := result.Reason == nil
	if !success && !IsProbeResultReason(*result.Reason) {
		return check, false, false
	}

	var detail *string
	if sanitised := SanitizeProbeDetail(result.Detail); sanitised != "" {
		detail = stringPointer(sanitised)
	}

	next := check
	next.Probe = cloneProbe(check.Probe)
	next.AnchorAt = now
	next.Probe.LastResult = &ProbeResultRecord{
		At:     now,
		Status: storableProbeStatus(result.Status),
		Reason: copyStringPointer(result.Reason),
		Detail: copyStringPointer(detail),
	}

	if success {
		next.Probe.ConsecutiveFailures = 0
		next.LastPingAt = timePointer(now)
		next.LastPingKind = stringPointer(PingSuccess)
		if check.State == StateDown {
			next.State = StateUp
			next.DownReason = nil
			next.Probe.DownDetail = nil
			return emitAlert(next, AlertRecovered), true, true
		}
		next.State = StateUp
		return next, true, false
	}

	next.Probe.ConsecutiveFailures++
	switch check.State {
	case StateDown:
		if check.DownReason != nil && *check.DownReason == ReasonMissed {
			next.DownReason = stringPointer(*result.Reason)
			next.Probe.DownDetail = copyStringPointer(detail)
		}
		return next, true, false
	default:
		if next.Probe.ConsecutiveFailures < ProbeFailureThreshold || now.Before(holdOffUntil) {
			return next, true, false
		}
		next.State = StateDown
		next.DownSince = timePointer(now)
		next.DownReason = stringPointer(*result.Reason)
		next.Probe.DownDetail = copyStringPointer(detail)
		return emitAlert(next, AlertDown), true, true
	}
}

// storableProbeStatus returns a copy of status, or nil when it is nil or outside 100..599 (the
// range the last_result_status column accepts).
func storableProbeStatus(status *int) *int {
	if status == nil || *status < 100 || *status > 599 {
		return nil
	}
	value := *status
	return &value
}

// cloneProbe returns a copy of state that shares no mutable fields with it.
func cloneProbe(state *ProbeState) *ProbeState {
	clone := *state
	clone.Config.ExpectedStatuses = slices.Clone(state.Config.ExpectedStatuses)
	clone.Config.BodyMatch = copyStringPointer(state.Config.BodyMatch)
	clone.SealedAccessToken = slices.Clone(state.SealedAccessToken)
	if state.LastResult != nil {
		last := *state.LastResult
		last.Status = copyIntPointer(state.LastResult.Status)
		last.Reason = copyStringPointer(state.LastResult.Reason)
		last.Detail = copyStringPointer(state.LastResult.Detail)
		clone.LastResult = &last
	}
	clone.DownDetail = copyStringPointer(state.DownDetail)
	return &clone
}

// copyIntPointer returns a pointer to a copy of value, or nil.
func copyIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// copyStringPointer returns a pointer to a copy of value, or nil.
func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// emitAlert records an emitted alert in the outbox: the next AlertSeq and the
// pending kind, overwriting any earlier unflushed alert.
func emitAlert(check StoredCheck, kind string) StoredCheck {
	check.AlertSeq++
	check.PendingAlert = stringPointer(kind)
	return check
}

// timePointer returns a pointer to a copy of value.
func timePointer(value time.Time) *time.Time { return &value }

// stringPointer returns a pointer to a copy of value.
func stringPointer(value string) *string { return &value }

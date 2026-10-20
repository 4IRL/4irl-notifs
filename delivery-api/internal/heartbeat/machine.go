package heartbeat

import "time"

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
	return emitAlert(next, AlertDown), true
}

// ApplyPause pauses the check and clears the stored pending alert. An alert already read by an
// in-flight Tick flush (one flush duration) can still be announced once. Pausing a paused
// check is a no-op.
func ApplyPause(check StoredCheck) StoredCheck {
	next := check
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
	return next
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

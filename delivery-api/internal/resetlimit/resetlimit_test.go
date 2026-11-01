package resetlimit

import (
	"testing"
	"time"
)

func TestResetTuningConstantsArePinned(testInstance *testing.T) {
	// The literals pin the documented values so a change must be deliberate.
	if AppLimit != 30 || PersonLimit != 3 || Window != time.Hour ||
		LockPrefix != "reset_attempt:" || ReserveTimeout != 3*time.Second {
		testInstance.Fatalf("limits: app=%d person=%d window=%v prefix=%q timeout=%v",
			AppLimit, PersonLimit, Window, LockPrefix, ReserveTimeout)
	}
	if ScopeApp != "app" || ScopePerson != "person" {
		testInstance.Fatalf("scopes: %q %q", ScopeApp, ScopePerson)
	}
}

func TestNewPGStoreUsesTheReserveTimeout(testInstance *testing.T) {
	if got := NewPGStore(nil).reserveTimeout; got != ReserveTimeout {
		testInstance.Fatalf("reserveTimeout = %v, want %v", got, ReserveTimeout)
	}
}

func TestRateLimitedErrorMessage(testInstance *testing.T) {
	if got := (&RateLimitedError{}).Error(); got != "reset rate limit exceeded" {
		testInstance.Fatalf("Error() = %q", got)
	}
}

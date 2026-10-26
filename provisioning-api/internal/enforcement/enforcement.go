// Package enforcement parses the fail-closed "enforce or log-only" rollout
// switches (PROVISIONING_APP_GRANT_MODE, PROVISIONING_INTERNAL_CREDENTIAL_MODE).
// An unset or empty value means Enforce; the log-only rollout is always an
// explicit setting, and any other value is a startup error.
package enforcement

import "fmt"

// Mode is a rollout switch. The zero value is Enforce, so an uninitialized Mode
// fails closed.
type Mode int

const (
	// Enforce rejects a request the check would reject. It is the zero value.
	Enforce Mode = iota
	// LogOnly logs what the check would reject and lets the request proceed.
	LogOnly
)

const (
	enforceValue = "enforce"
	logOnlyValue = "log-only"
)

// Parse converts an environment value to a Mode. Empty means Enforce; only the
// exact lowercase values "enforce" and "log-only" are accepted (no trimming, no
// case folding), and anything else is an error naming envName and the allowed
// values.
func Parse(envName string, value string) (Mode, error) {
	switch value {
	case "", enforceValue:
		return Enforce, nil
	case logOnlyValue:
		return LogOnly, nil
	default:
		return Enforce, fmt.Errorf("%s: invalid value %q (allowed: %q, %q; empty means %q)",
			envName, value, enforceValue, logOnlyValue, enforceValue)
	}
}

// String returns the log/config spelling of the mode. Any value other than
// LogOnly renders as "enforce", matching the fail-closed zero value.
func (mode Mode) String() string {
	if mode == LogOnly {
		return logOnlyValue
	}
	return enforceValue
}

package heartbeat

import (
	"regexp"
)

// namePattern matches a check name: lowercase letters, digits and underscores,
// starting with a letter or digit, 1-63 characters.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// appIDPattern matches an app_id, the same rule as provisioning-api and the
// registry. It intentionally duplicates namePattern and the other packages'
// copies: the rules are independent and may diverge.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// destinationNamePattern matches a destination name, the same rule as the
// registry. It is a per-package duplicate: the rules are independent and may
// diverge.
var destinationNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

const (
	// MinPeriodSeconds bounds a check's period from below.
	MinPeriodSeconds = 60
	// MaxPeriodSeconds bounds a check's period from above (31 days).
	MaxPeriodSeconds = 2_678_400
	// MinGraceSeconds bounds a check's grace from below.
	MinGraceSeconds = 60
	// MaxGraceSeconds bounds a check's grace from above (7 days).
	MaxGraceSeconds = 604_800
	// MinDestinations bounds a check's destination set from below.
	MinDestinations = 1
	// MaxDestinations bounds a check's destination set from above.
	MaxDestinations = 20
)

// ValidateAppID checks an app_id.
func ValidateAppID(appID string) error {
	if !appIDPattern.MatchString(appID) {
		return &ValidationError{Message: "invalid app_id"}
	}
	return nil
}

// ValidateName checks a check name.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return &ValidationError{Message: "invalid name"}
	}
	return nil
}

// ValidatePeriodSeconds checks a period against its inclusive bounds.
func ValidatePeriodSeconds(periodSeconds int) error {
	if periodSeconds < MinPeriodSeconds || periodSeconds > MaxPeriodSeconds {
		return &ValidationError{Message: "invalid period_seconds"}
	}
	return nil
}

// ValidateGraceSeconds checks a grace against its inclusive bounds.
func ValidateGraceSeconds(graceSeconds int) error {
	if graceSeconds < MinGraceSeconds || graceSeconds > MaxGraceSeconds {
		return &ValidationError{Message: "invalid grace_seconds"}
	}
	return nil
}

// ValidateDestinations checks a check's destination names: 1-20 valid, distinct
// destination names. Whether they exist is the store's job.
func ValidateDestinations(names []string) error {
	if len(names) < MinDestinations || len(names) > MaxDestinations {
		return &ValidationError{Message: "invalid destinations"}
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !destinationNamePattern.MatchString(name) || seen[name] {
			return &ValidationError{Message: "invalid destinations"}
		}
		seen[name] = true
	}
	return nil
}

// ValidatePingKind accepts only the two ping kinds; anything else is rejected.
func ValidatePingKind(kind string) error {
	switch kind {
	case PingSuccess, PingFail:
		return nil
	default:
		return &ValidationError{Message: "invalid kind"}
	}
}

// ValidateNewCheck validates every field of a new check, returning the first
// failure in field order.
func ValidateNewCheck(check NewCheck) error {
	if err := ValidateAppID(check.AppID); err != nil {
		return err
	}
	if err := ValidateName(check.Name); err != nil {
		return err
	}
	return ValidateSchedule(check.PeriodSeconds, check.GraceSeconds, check.DestinationNames)
}

// ValidateCheckUpdate validates the mutable fields of a check.
func ValidateCheckUpdate(update CheckUpdate) error {
	return ValidateSchedule(update.PeriodSeconds, update.GraceSeconds, update.DestinationNames)
}

// ValidateSchedule validates the period, grace and destination set shared by
// create and update.
func ValidateSchedule(periodSeconds, graceSeconds int, destinationNames []string) error {
	if err := ValidatePeriodSeconds(periodSeconds); err != nil {
		return err
	}
	if err := ValidateGraceSeconds(graceSeconds); err != nil {
		return err
	}
	return ValidateDestinations(destinationNames)
}

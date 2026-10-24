package heartbeat

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// Validation messages shared by the probe validators.
const (
	messageInvalidAccessToken = "invalid probe.access_token"
	messageDerivedSchedule    = "period_seconds and grace_seconds are derived for probe checks"
	messageProbeOnPush        = "probe config is not allowed on a push check"
	messageProbeOnAppOwned    = "probe config is not allowed on an app-owned check"
)

// namePattern matches a check name: lowercase letters, digits and underscores,
// starting with a letter or digit, 1-63 characters.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

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

// ValidateAppID checks an app_id against the shared slug rule (package appid).
func ValidateAppID(appID string) error {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return &ValidationError{Message: validateErr.Error()}
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
	if check.Probe != nil || check.AccessToken != nil {
		// Fail closed: only an empty or operator owner may carry probe config.
		if check.OwnerKind != "" && check.OwnerKind != registry.OwnerOperator {
			return &ValidationError{Message: messageProbeOnAppOwned}
		}
	}
	if check.Probe == nil {
		if check.AccessToken != nil {
			return &ValidationError{Message: messageInvalidAccessToken}
		}
		return ValidateSchedule(check.PeriodSeconds, check.GraceSeconds, check.DestinationNames)
	}
	if check.PeriodSeconds != 0 || check.GraceSeconds != 0 {
		return &ValidationError{Message: messageDerivedSchedule}
	}
	if err := ValidateProbeConfig(*check.Probe); err != nil {
		return err
	}
	if check.AccessToken != nil {
		if err := ValidateAccessToken(*check.AccessToken); err != nil {
			return err
		}
	}
	period, grace := DerivedSchedule(check.Probe.IntervalSeconds)
	return ValidateSchedule(period, grace, check.DestinationNames)
}

// ValidateCheckUpdate validates the mutable fields of a check, including the
// merged probe config and a replacement Access token.
func ValidateCheckUpdate(update CheckUpdate) error {
	if update.Probe != nil {
		if err := ValidateProbeConfig(*update.Probe); err != nil {
			return err
		}
	}
	switch update.AccessToken.Action {
	case AccessTokenUnchanged, AccessTokenClear:
	case AccessTokenReplace:
		if err := ValidateAccessToken(update.AccessToken.Token); err != nil {
			return err
		}
	default:
		return &ValidationError{Message: messageInvalidAccessToken}
	}
	return ValidateSchedule(update.PeriodSeconds, update.GraceSeconds, update.DestinationNames)
}

// ValidateCheckPatchForKind rejects a patch that crosses the check's kind:
// period/grace on a probe check (they are derived) and probe or token fields on
// a push check. It also makes the kind immutable. Callers run it under the
// store lock, once the current check is known.
func ValidateCheckPatchForKind(current StoredCheck, patch CheckPatch) error {
	if current.Kind() == KindProbe {
		if patch.PeriodSeconds != nil || patch.GraceSeconds != nil {
			return &ValidationError{Message: messageDerivedSchedule}
		}
		return nil
	}
	if patch.Probe != nil || patch.AccessToken.Action != AccessTokenUnchanged {
		return &ValidationError{Message: messageProbeOnPush}
	}
	return nil
}

// DerivedSchedule returns the period and grace a probe check stores for its
// interval: period = interval, grace = interval * (ProbeFailureThreshold + 1).
func DerivedSchedule(intervalSeconds int) (period, grace int) {
	return intervalSeconds, intervalSeconds * (ProbeFailureThreshold + 1)
}

// ValidateProbeConfig checks a probe config against the probe bounds. Messages
// name the field and never echo the value.
func ValidateProbeConfig(config ProbeConfig) error {
	if !validProbeURL(config.URL) {
		return &ValidationError{Message: "invalid probe.url"}
	}
	if !validExpectedStatuses(config.ExpectedStatuses) {
		return &ValidationError{Message: "invalid probe.expected_statuses"}
	}
	if config.BodyMatch != nil && !validBodyMatch(*config.BodyMatch) {
		return &ValidationError{Message: "invalid probe.body_match"}
	}
	if config.IntervalSeconds < MinProbeIntervalSeconds || config.IntervalSeconds > MaxProbeIntervalSeconds {
		return &ValidationError{Message: "invalid probe.interval_seconds"}
	}
	if config.TimeoutSeconds < MinProbeTimeoutSeconds || config.TimeoutSeconds > MaxProbeTimeoutSeconds ||
		config.TimeoutSeconds >= config.IntervalSeconds {
		return &ValidationError{Message: "invalid probe.timeout_seconds"}
	}
	return nil
}

// ValidateAccessToken checks both halves of an Access token: each 1 to
// MaxAccessCredentialBytes printable ASCII bytes (0x21..0x7E).
func ValidateAccessToken(token AccessToken) error {
	if !validAccessCredential(token.ClientID) || !validAccessCredential(token.ClientSecret) {
		return &ValidationError{Message: messageInvalidAccessToken}
	}
	return nil
}

func validAccessCredential(value string) bool {
	if len(value) < 1 || len(value) > MaxAccessCredentialBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7E {
			return false
		}
	}
	return true
}

func validBodyMatch(value string) bool {
	return len(value) >= 1 && len(value) <= MaxBodyMatchBytes &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validExpectedStatuses(statuses []int) bool {
	if len(statuses) < 1 || len(statuses) > MaxExpectedStatuses {
		return false
	}
	seen := make(map[int]bool, len(statuses))
	for _, status := range statuses {
		if status < 100 || status > 599 || seen[status] {
			return false
		}
		seen[status] = true
	}
	return true
}

// validProbeURL enforces the probe URL rules. The literal lower-case scheme
// prefix check matters: the database CHECK is case-sensitive and the stored
// value equals the input. Resolved-address checks belong to the transport guard.
func validProbeURL(raw string) bool {
	if len(raw) > MaxProbeURLBytes || !strings.HasPrefix(raw, "https://") || strings.Contains(raw, "#") {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if _, parseErr := netip.ParseAddr(host); parseErr == nil {
		return false
	}
	return true
}

// probeDetailURLPattern finds http(s) URL substrings inside a detail.
var probeDetailURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]*`)

// SanitizeProbeDetail makes a probe failure detail safe to persist, alert and
// log: any http(s) URL is reduced to scheme+host (userinfo, path, query and
// fragment dropped), control characters become spaces, and the result is cut to
// MaxProbeDetailBytes on a rune boundary. It is the single persistence
// chokepoint for probe details.
func SanitizeProbeDetail(detail string) string {
	redacted := probeDetailURLPattern.ReplaceAllStringFunc(detail, redactURL)
	redacted = strings.ToValidUTF8(redacted, "�")
	redacted = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, redacted)
	redacted = strings.TrimSpace(redacted)
	if len(redacted) <= MaxProbeDetailBytes {
		return redacted
	}
	cut := MaxProbeDetailBytes
	for cut > 0 && !utf8.RuneStart(redacted[cut]) {
		cut--
	}
	return redacted[:cut]
}

// redactURL reduces a matched URL to scheme+host.
func redactURL(raw string) string {
	schemeEnd := strings.Index(raw, "://") + len("://")
	scheme := raw[:schemeEnd]
	authority := raw[schemeEnd:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority = authority[at+1:]
	}
	return scheme + authority
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

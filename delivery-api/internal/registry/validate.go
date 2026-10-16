package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// destinationNamePattern matches a destination name (and an exact app_id):
// lowercase letters, digits and underscores, starting with a letter or digit,
// 1-63 characters.
var destinationNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// appIDPattern matches an exact app_id, the same rule as provisioning-api. It
// intentionally duplicates destinationNamePattern: the two rules are
// independent and may diverge.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// channelExactPattern matches an exact channel: lowercase letters, digits and
// underscores, 1-32 characters, the same rule as provisioning-api.
var channelExactPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

// channelPrefixPattern matches a prefix channel pattern: a valid channel head
// of 1-31 characters followed by a single trailing "*".
var channelPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,30}\*$`)

// ntfyTopicPattern matches an ntfy topic: letters, digits, underscores and
// hyphens, 1-64 characters.
var ntfyTopicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ntfyAccessTokenPattern matches an ntfy access token: "tk_" followed by 29
// lowercase letters or digits.
var ntfyAccessTokenPattern = regexp.MustCompile(`^tk_[a-z0-9]{29}$`)

const (
	// wildcardPattern is the pattern that matches any value.
	wildcardPattern = "*"
	// reservedEveryone is reserved and rejected as an app pattern.
	reservedEveryone = "everyone"
	// maxURLLength is the maximum accepted destination URL length in bytes.
	maxURLLength = 2048
	// httpsScheme is the only URL scheme a destination URL may use.
	httpsScheme = "https"
	// minSigningSecretLength and maxSigningSecretLength bound a webhook
	// signing secret in bytes.
	minSigningSecretLength = 32
	maxSigningSecretLength = 256
	// minRouteDestinations and maxRouteDestinations bound a route's destination
	// set.
	minRouteDestinations = 1
	maxRouteDestinations = 20
	// maxReportedKeyLength bounds how much of a client-chosen unknown key is
	// echoed back in a validation message.
	maxReportedKeyLength = 64
)

// discordWebhookPathPattern matches the escaped path of a Discord webhook:
// "/api/webhooks/" followed by a numeric id and a token of URL-safe
// characters. It starts with "/api/webhooks/" as the spec requires, and the
// stricter tail rejects an empty id or token and percent-encoded traversal.
var discordWebhookPathPattern = regexp.MustCompile(`^/api/webhooks/[0-9]+/[A-Za-z0-9_-]+$`)

// reportedKeyUnsafe matches every character that is not safe to echo back in a
// validation message.
var reportedKeyUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// discordHosts is the set of hosts a Discord webhook URL may use.
var discordHosts = map[string]bool{
	"discord.com":        true,
	"discordapp.com":     true,
	"ptb.discord.com":    true,
	"canary.discord.com": true,
}

// ValidateDestinationName checks a destination name against the immutable
// slug rule (lowercase letters, digits, underscores; 1-63 characters).
func ValidateDestinationName(name string) error {
	if !destinationNamePattern.MatchString(name) {
		return &ValidationError{Message: "invalid name"}
	}
	return nil
}

// ValidateDestinationType accepts only ntfy, discord and webhook; anything
// else is rejected.
func ValidateDestinationType(destinationType string) error {
	switch destinationType {
	case TypeNtfy, TypeDiscord, TypeWebhook:
		return nil
	default:
		return &ValidationError{Message: "invalid type"}
	}
}

// ValidateAppPattern accepts "*" or an exact app_id. The reserved app id
// "everyone" is rejected here as a business rule; the SQL CHECK does not
// enforce it.
func ValidateAppPattern(pattern string) error {
	if pattern == wildcardPattern {
		return nil
	}
	if pattern == reservedEveryone || !appIDPattern.MatchString(pattern) {
		return &ValidationError{Message: "invalid app_pattern"}
	}
	return nil
}

// ValidateChannelPattern accepts "*", an exact channel, or a channel prefix
// ending in a single "*". It mirrors the SQL CHECK on route.channel_pattern.
func ValidateChannelPattern(pattern string) error {
	if pattern == wildcardPattern ||
		channelExactPattern.MatchString(pattern) ||
		channelPrefixPattern.MatchString(pattern) {
		return nil
	}
	return &ValidationError{Message: "invalid channel_pattern"}
}

// ValidateRouteDestinations checks a route's destination names: 1-20 valid
// names with no duplicates.
func ValidateRouteDestinations(names []string) error {
	if len(names) < minRouteDestinations || len(names) > maxRouteDestinations {
		return &ValidationError{Message: "invalid destinations"}
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if ValidateDestinationName(name) != nil || seen[name] {
			return &ValidationError{Message: "invalid destinations"}
		}
		seen[name] = true
	}
	return nil
}

// ValidateDestinationFields validates the config and secrets of a destination
// of the given type. Keys outside the type's allowed set are rejected by name;
// an unknown type is rejected fail-closed.
func ValidateDestinationFields(destinationType string, config, secrets map[string]string) error {
	switch destinationType {
	case TypeNtfy:
		return validateNtfyFields(config, secrets)
	case TypeDiscord:
		return validateDiscordFields(config, secrets)
	case TypeWebhook:
		return validateWebhookFields(config, secrets)
	default:
		return &ValidationError{Message: "invalid type"}
	}
}

// validateNtfyFields requires config.topic and allows an optional
// secrets.access_token.
func validateNtfyFields(config, secrets map[string]string) error {
	if err := rejectUnknownKeys("config", config, "topic"); err != nil {
		return err
	}
	if err := rejectUnknownKeys("secrets", secrets, "access_token"); err != nil {
		return err
	}
	if !ntfyTopicPattern.MatchString(config["topic"]) {
		return &ValidationError{Message: "invalid config.topic"}
	}
	if token, present := secrets["access_token"]; present && !ntfyAccessTokenPattern.MatchString(token) {
		return &ValidationError{Message: "invalid secrets.access_token"}
	}
	return nil
}

// validateDiscordFields requires an empty config and a Discord
// secrets.webhook_url.
func validateDiscordFields(config, secrets map[string]string) error {
	if err := rejectUnknownKeys("config", config); err != nil {
		return err
	}
	if err := rejectUnknownKeys("secrets", secrets, "webhook_url"); err != nil {
		return err
	}
	parsed, ok := parseHTTPSURL(secrets["webhook_url"])
	if !ok || !discordHosts[strings.ToLower(parsed.Host)] || !discordWebhookPathPattern.MatchString(parsed.EscapedPath()) {
		return &ValidationError{Message: "invalid secrets.webhook_url"}
	}
	return nil
}

// validateWebhookFields requires a valid secrets.url and a printable-ASCII
// secrets.signing_secret of 32-256 bytes. It allows the key-rotation overlap
// pair: secrets.signing_secret_previous (same rule as the signing secret) and
// config.previous_secret_expires_at (RFC 3339), which must be set together or
// not at all.
func validateWebhookFields(config, secrets map[string]string) error {
	if err := rejectUnknownKeys("config", config, "previous_secret_expires_at"); err != nil {
		return err
	}
	if err := rejectUnknownKeys("secrets", secrets, "url", "signing_secret", "signing_secret_previous"); err != nil {
		return err
	}
	if _, ok := parseHTTPSURL(secrets["url"]); !ok {
		return &ValidationError{Message: "invalid secrets.url"}
	}
	if !validSigningSecret(secrets["signing_secret"]) {
		return &ValidationError{Message: "invalid secrets.signing_secret"}
	}
	return validateRotationFields(config, secrets)
}

// validateRotationFields checks the optional previous-secret overlap pair of a
// webhook destination.
func validateRotationFields(config, secrets map[string]string) error {
	previousSecret, hasPrevious := secrets["signing_secret_previous"]
	expiresAt, hasExpiry := config["previous_secret_expires_at"]
	if hasPrevious && !validSigningSecret(previousSecret) {
		return &ValidationError{Message: "invalid secrets.signing_secret_previous"}
	}
	if hasPrevious != hasExpiry {
		return &ValidationError{
			Message: "secrets.signing_secret_previous and config.previous_secret_expires_at must be set together",
		}
	}
	if hasExpiry {
		if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
			return &ValidationError{Message: "invalid config.previous_secret_expires_at"}
		}
	}
	return nil
}

// rejectUnknownKeys returns a ValidationError naming the first (in sorted
// order, so the message is deterministic) key of fields that is not allowed.
func rejectUnknownKeys(section string, fields map[string]string, allowed ...string) error {
	var unknown []string
	for key := range fields {
		if !slices.Contains(allowed, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	return &ValidationError{Message: fmt.Sprintf("unknown field %s.%s", section, reportableKey(unknown[0]))}
}

// reportableKey makes a client-chosen key safe to echo in a validation
// message: characters outside [A-Za-z0-9_.-] become "?" and the result is cut
// to 64 bytes.
func reportableKey(key string) string {
	sanitized := reportedKeyUnsafe.ReplaceAllString(key, "?")
	if len(sanitized) > maxReportedKeyLength {
		return sanitized[:maxReportedKeyLength]
	}
	return sanitized
}

// parseHTTPSURL parses rawURL and reports whether it is a static-valid
// destination URL: it parses, is at most 2048 bytes, uses the https scheme,
// has a non-empty host and carries no userinfo. Dial-time SSRF checks are a
// later phase's job.
func parseHTTPSURL(rawURL string) (*url.URL, bool) {
	if len(rawURL) > maxURLLength {
		return nil, false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, false
	}
	if parsed.Scheme != httpsScheme || parsed.Hostname() == "" || parsed.User != nil {
		return nil, false
	}
	return parsed, true
}

// validSigningSecret reports whether secret is 32-256 bytes of printable ASCII
// (0x20 through 0x7e).
func validSigningSecret(secret string) bool {
	if len(secret) < minSigningSecretLength || len(secret) > maxSigningSecretLength {
		return false
	}
	for index := 0; index < len(secret); index++ {
		if secret[index] < 0x20 || secret[index] > 0x7e {
			return false
		}
	}
	return true
}

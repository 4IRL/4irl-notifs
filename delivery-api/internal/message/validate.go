package message

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"time"
)

// appIDPattern matches an app_id: the same rule as provisioning-api and the
// registry, and the SQL CHECK on messages.app_id.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// channelPattern matches an exact channel: lowercase letters, digits and
// underscores, 1-32 characters.
var channelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

// idempotencyKeyPattern matches 1-200 printable, non-space ASCII bytes.
var idempotencyKeyPattern = regexp.MustCompile(`^[\x21-\x7e]{1,200}$`)

// destinationNamePattern matches a registry destination name.
var destinationNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

const (
	// maxDisplayTitleBytes, maxDisplayBodyBytes and maxDisplayURLBytes bound the
	// display fields.
	maxDisplayTitleBytes = 250
	maxDisplayBodyBytes  = 4096
	maxDisplayURLBytes   = 2048

	httpsScheme = "https"
)

// displayPriorities is the accepted display.priority set (ntfy's names).
var displayPriorities = []string{"min", "low", "default", "high", "urgent"}

// ValidateEnvelope checks every envelope rule in a fixed order and returns the
// first failure: app_id, channel, idempotency_key, to, deliver_at, payload,
// display. The order is deterministic so clients see a stable error.
func ValidateEnvelope(envelope Envelope) error {
	if err := ValidateAppID(envelope.AppID); err != nil {
		return err
	}
	if err := ValidateChannel(envelope.Channel); err != nil {
		return err
	}
	if err := ValidateIdempotencyKey(envelope.IdempotencyKey); err != nil {
		return err
	}
	if err := ValidateTo(envelope.ToDestination, envelope.ToRecipient); err != nil {
		return err
	}
	if err := ValidateDeliverAt(envelope.DeliverAt); err != nil {
		return err
	}
	if err := ValidatePayload(envelope.Payload); err != nil {
		return err
	}
	return ValidateDisplay(envelope.Display)
}

// ValidateAppID checks an app_id against the slug rule.
func ValidateAppID(appID string) error {
	if !appIDPattern.MatchString(appID) {
		return &ValidationError{Message: "invalid app_id"}
	}
	return nil
}

// ValidateChannel checks an exact channel name (no wildcards).
func ValidateChannel(channel string) error {
	if !channelPattern.MatchString(channel) {
		return &ValidationError{Message: "invalid channel"}
	}
	return nil
}

// ValidateIdempotencyKey checks the key is 1-200 printable, non-space ASCII
// bytes.
func ValidateIdempotencyKey(idempotencyKey string) error {
	if !idempotencyKeyPattern.MatchString(idempotencyKey) {
		return &ValidationError{Message: "invalid idempotency_key"}
	}
	return nil
}

// ValidateTo checks the delivery target. A recipient is rejected until the ntfy
// sink ships (fail closed: nothing is queued without a sink to consume it);
// otherwise the destination is either empty (route fan-out) or a valid
// destination name. It takes both values together because it must see the
// recipient before judging the destination.
func ValidateTo(toDestination string, toRecipient string) error {
	if toRecipient != "" {
		return &ValidationError{Message: "recipient delivery not yet supported"}
	}
	if toDestination != "" && !destinationNamePattern.MatchString(toDestination) {
		return &ValidationError{Message: "invalid to.destination"}
	}
	return nil
}

// ValidateDeliverAt rejects any scheduled delivery time until phase 6 ships
// scheduling; the column is reserved now.
func ValidateDeliverAt(deliverAt *time.Time) error {
	if deliverAt != nil {
		return &ValidationError{Message: "deliver_at not yet supported"}
	}
	return nil
}

// ValidatePayload checks the payload is a single JSON object no larger than
// MaxPayloadBytes. The service never inspects its contents.
func ValidatePayload(payload json.RawMessage) error {
	if len(payload) > MaxPayloadBytes {
		return &ValidationError{Message: "payload too large"}
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return &ValidationError{Message: "invalid payload"}
	}
	return nil
}

// ValidateDisplay checks the optional display block: field size limits, an
// https url with a host and no userinfo, and a known priority. A nil display is
// valid. Fields are checked in the order title, body, url, priority.
func ValidateDisplay(display *Display) error {
	if display == nil {
		return nil
	}
	if len(display.Title) > maxDisplayTitleBytes {
		return &ValidationError{Message: "invalid display.title"}
	}
	if len(display.Body) > maxDisplayBodyBytes {
		return &ValidationError{Message: "invalid display.body"}
	}
	if display.URL != "" && !validDisplayURL(display.URL) {
		return &ValidationError{Message: "invalid display.url"}
	}
	if display.Priority != "" && !slices.Contains(displayPriorities, display.Priority) {
		return &ValidationError{Message: "invalid display.priority"}
	}
	return nil
}

// validDisplayURL reports whether rawURL is an https URL with a host, no
// userinfo, and at most maxDisplayURLBytes bytes.
func validDisplayURL(rawURL string) bool {
	if len(rawURL) > maxDisplayURLBytes {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == httpsScheme && parsed.Hostname() != "" && parsed.User == nil
}

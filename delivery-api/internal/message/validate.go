package message

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
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

	// unicodeEscapeLength is the byte length of a JSON \uXXXX escape.
	unicodeEscapeLength = 6

	// The UTF-16 surrogate ranges a JSON \u escape may only use as a high+low pair.
	highSurrogateStart = 0xD800
	highSurrogateEnd   = 0xDBFF
	lowSurrogateStart  = 0xDC00
	lowSurrogateEnd    = 0xDFFF
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
// MaxPayloadBytes that Postgres jsonb can store (valid UTF-8, no \u0000 escape,
// no unpaired surrogate escape). The service never inspects its contents.
// jsonb canonicalizes the stored payload (key order, whitespace, duplicate keys
// last-wins), so any later signing must cover the canonical form.
func ValidatePayload(payload json.RawMessage) error {
	if len(payload) > MaxPayloadBytes {
		return &ValidationError{Message: "payload too large"}
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return &ValidationError{Message: "invalid payload"}
	}
	if !utf8.Valid(trimmed) || !unicodeEscapesStorable(trimmed) {
		return &ValidationError{Message: "invalid payload"}
	}
	return nil
}

// unicodeEscapesStorable reports whether every \uXXXX escape in a syntactically
// valid JSON document is one jsonb accepts: not \u0000 and not an unpaired
// surrogate. Scanning the raw bytes is required because json.Unmarshal silently
// replaces a lone surrogate with U+FFFD. A backslash only occurs inside a JSON
// string, where it always starts an escape, so escaped backslashes (the text
// \\u0000) are skipped as a pair and never mistaken for a \u escape.
func unicodeEscapesStorable(document []byte) bool {
	for index := 0; index < len(document); index++ {
		if document[index] != '\\' {
			continue
		}
		if index+1 < len(document) && document[index+1] != 'u' {
			index++
			continue
		}
		codeUnit, parsed := parseUnicodeEscape(document, index)
		if !parsed {
			return false
		}
		index += unicodeEscapeLength - 1
		switch {
		case codeUnit == 0:
			return false
		case codeUnit >= highSurrogateStart && codeUnit <= highSurrogateEnd:
			lowUnit, lowParsed := parseUnicodeEscape(document, index+1)
			if !lowParsed || lowUnit < lowSurrogateStart || lowUnit > lowSurrogateEnd {
				return false
			}
			index += unicodeEscapeLength
		case codeUnit >= lowSurrogateStart && codeUnit <= lowSurrogateEnd:
			return false
		}
	}
	return true
}

// parseUnicodeEscape decodes the \uXXXX escape starting at the backslash at
// start. It reports false when the bytes there are not a complete escape.
func parseUnicodeEscape(document []byte, start int) (uint64, bool) {
	if start+unicodeEscapeLength > len(document) || document[start] != '\\' || document[start+1] != 'u' {
		return 0, false
	}
	codeUnit, parseErr := strconv.ParseUint(string(document[start+2:start+unicodeEscapeLength]), 16, 16)
	if parseErr != nil {
		return 0, false
	}
	return codeUnit, true
}

// ValidateDisplay checks the optional display block: field size limits, text
// that is valid UTF-8 without NUL or control characters (the body may contain
// line breaks), an https url with a host and no userinfo, and a known priority.
// A nil display is valid. Fields are checked in the order title, body, url,
// priority.
func ValidateDisplay(display *Display) error {
	if display == nil {
		return nil
	}
	if len(display.Title) > maxDisplayTitleBytes || !validDisplayText(display.Title, false) {
		return &ValidationError{Message: "invalid display.title"}
	}
	if len(display.Body) > maxDisplayBodyBytes || !validDisplayText(display.Body, true) {
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

// validDisplayText reports whether text is valid UTF-8 with no control
// characters. NUL is a control character, so it can never reach jsonb. Tab, line
// feed and carriage return are allowed only when allowLineBreaks is set.
func validDisplayText(text string, allowLineBreaks bool) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, char := range text {
		if !unicode.IsControl(char) {
			continue
		}
		isLineBreak := char == '\t' || char == '\n' || char == '\r'
		if !allowLineBreaks || !isLineBreak {
			return false
		}
	}
	return true
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

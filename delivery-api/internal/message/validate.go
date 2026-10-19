package message

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// appIDPattern matches an app_id: the same rule as provisioning-api and the
// registry, and the SQL CHECK on messages.app_id.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// channelPattern matches an exact channel: lowercase letters, digits and
// underscores, 1-32 characters.
var channelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

// idempotencyKeyPattern matches 1-200 printable, non-space ASCII bytes.
var idempotencyKeyPattern = regexp.MustCompile(`^[\x21-\x7e]{1,200}$`)

const (
	// maxDisplayTitleBytes, maxDisplayBodyBytes and maxDisplayURLBytes bound the
	// display fields.
	maxDisplayTitleBytes = 250
	maxDisplayBodyBytes  = 4096
	maxDisplayURLBytes   = 2048

	httpsScheme = "https"

	// Postgres numeric (and so jsonb) holds at most numericMaxIntegerDigits digits
	// before the decimal point and numericMaxScale after it; maxExponentDigits
	// bounds the exponent text parsed (a larger one is over both limits anyway).
	numericMaxIntegerDigits = 131072
	numericMaxScale         = 16383
	maxExponentDigits       = 7

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

// ValidateTo checks the delivery target. A destination and a recipient are
// mutually exclusive. A recipient must be a person_hash or an email; otherwise
// the destination is either empty (route fan-out) or a valid destination name.
// It takes both values together because the exclusivity check must see both
// before either is judged on its own.
func ValidateTo(toDestination string, toRecipient string) error {
	if toDestination != "" && toRecipient != "" {
		return &ValidationError{Message: "to.destination and to.recipient cannot both be set"}
	}
	if toRecipient != "" {
		if !personhash.IsHash(toRecipient) && !isValidEmail(toRecipient) {
			return &ValidationError{Message: "to.recipient must be an email or person_hash"}
		}
		return nil
	}
	// The registry owns the destination name rule; only its error is mapped to
	// this API's message.
	if toDestination != "" && registry.ValidateDestinationName(toDestination) != nil {
		return &ValidationError{Message: "invalid to.destination"}
	}
	return nil
}

// maxDeliverAt is the latest accepted deliver_at, compared in UTC. It is the
// largest instant a four-digit year can name, and well inside Postgres
// timestamptz range, so a stored deliver_at can never overflow.
var maxDeliverAt = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// ValidateDeliverAt checks the optional deliver_at. Absent is valid (an
// immediate send). The zero time is rejected, as is any instant whose UTC form
// is before year 1 (it could not be marshalled as RFC3339 JSON later) or after
// 9999-12-31T23:59:59Z; every other time is valid, past or future, with no
// horizon. Whether the time is in the future is the Service's decision, made
// with its injected clock, not this pure validator's.
func ValidateDeliverAt(deliverAt *time.Time) error {
	if deliverAt == nil {
		return nil
	}
	if deliverAt.IsZero() || deliverAt.UTC().Year() < 1 || deliverAt.UTC().After(maxDeliverAt) {
		return &ValidationError{Message: "invalid deliver_at"}
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
	if !utf8.Valid(trimmed) || !unicodeEscapesStorable(trimmed) || !numberLiteralsStorable(trimmed) {
		return &ValidationError{Message: "invalid payload"}
	}
	return nil
}

// numberLiteralsStorable reports whether every number literal in a
// syntactically valid JSON document fits Postgres numeric, which jsonb uses: at
// most numericMaxIntegerDigits digits before the decimal point and
// numericMaxScale after it. A literal such as 1e999999 is valid JSON, so
// json.Valid accepts it, but the insert would fail with "value overflows
// numeric format". The check is an upper bound (it never accepts a literal
// Postgres would refuse). Strings are skipped, including escaped quotes, so
// number-looking text inside a string is never inspected.
func numberLiteralsStorable(document []byte) bool {
	for index := 0; index < len(document); index++ {
		switch char := document[index]; {
		case char == '"':
			index = endOfJSONString(document, index)
		case char == '-' || (char >= '0' && char <= '9'):
			end := index
			for end < len(document) && isNumberLiteralByte(document[end]) {
				end++
			}
			if !numberLiteralStorable(document[index:end]) {
				return false
			}
			index = end - 1
		}
	}
	return true
}

// endOfJSONString returns the index of the closing quote of the string that
// opens at start. The document is known to be valid JSON, so the string is
// terminated; an unterminated one ends at the last byte.
func endOfJSONString(document []byte, start int) int {
	for index := start + 1; index < len(document); index++ {
		switch document[index] {
		case '\\':
			index++
		case '"':
			return index
		}
	}
	return len(document) - 1
}

// isNumberLiteralByte reports whether char can appear inside a JSON number.
func isNumberLiteralByte(char byte) bool {
	return (char >= '0' && char <= '9') || char == '-' || char == '+' || char == '.' || char == 'e' || char == 'E'
}

// numberLiteralStorable checks one JSON number literal against the numeric
// limits: integer digits <= numericMaxIntegerDigits and scale <= numericMaxScale
// once the exponent is applied.
func numberLiteralStorable(literal []byte) bool {
	mantissa, exponentText, hasExponent := strings.Cut(strings.ToLower(string(literal)), "e")
	exponent := 0
	if hasExponent {
		negative := strings.HasPrefix(exponentText, "-")
		digits := strings.TrimLeft(strings.TrimLeft(exponentText, "+-"), "0")
		if len(digits) > maxExponentDigits {
			return false
		}
		if digits != "" {
			parsed, parseErr := strconv.Atoi(digits)
			if parseErr != nil {
				return false
			}
			exponent = parsed
		}
		if negative {
			exponent = -exponent
		}
	}
	integerPart, fractionPart, _ := strings.Cut(strings.TrimPrefix(mantissa, "-"), ".")
	integerDigits := len(strings.TrimLeft(integerPart, "0"))
	scale := len(fractionPart)
	if exponent > 0 {
		integerDigits += exponent
	}
	scale -= exponent
	return integerDigits <= numericMaxIntegerDigits && scale <= numericMaxScale
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

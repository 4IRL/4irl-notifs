package sink

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	userAgent = "4irl-delivery/1"

	headerMessageID      = "X-Delivery-Message-Id"
	headerIdempotencyKey = "X-Delivery-Idempotency-Key"
	headerTimestamp      = "X-Delivery-Timestamp"
	headerSignature      = "X-Delivery-Signature"

	signatureScheme = "v1="

	// maxRetryAfter caps any Retry-After a destination asks for, so a hostile or
	// buggy receiver cannot park a message for longer than a day.
	maxRetryAfter = 24 * time.Hour

	redactedMarker = "[redacted]"
	hostMarker     = "[host]"
)

// errNoClient wraps ErrInvalidDelivery: a sink without a client can never send,
// so retrying cannot help.
var errNoClient = fmt.Errorf("%w: webhook sink has no HTTP client", ErrInvalidDelivery)

// requestError is a transport failure with the destination hostname masked out
// of its text. Unwrap returns the original cause so errors.Is, errors.As and
// transport.IsBlocked still see through it.
type requestError struct {
	message string
	cause   error
}

func (failure *requestError) Error() string { return failure.message }

func (failure *requestError) Unwrap() error { return failure.cause }

// newRequestError wraps cause, replacing every case-insensitive occurrence of
// hostname in its text with a placeholder. DNS and certificate errors quote the
// host they failed on, and a webhook host can be tenant-identifying. An empty
// hostname leaves the text as is.
func newRequestError(cause error, hostname string) *requestError {
	text := cause.Error()
	if hostname != "" {
		hostPattern := regexp.MustCompile("(?i)" + regexp.QuoteMeta(hostname))
		text = hostPattern.ReplaceAllLiteralString(text, hostMarker)
	}
	return &requestError{message: "webhook request failed: " + text, cause: cause}
}

// webhookBody is the JSON body posted to a webhook. Its field order is the wire
// order and is part of the contract with receivers. Receivers must verify the
// signature over the raw bytes they received and never re-serialize the body.
type webhookBody struct {
	MessageID      string           `json:"message_id"`
	AppID          string           `json:"app_id"`
	Channel        string           `json:"channel"`
	IdempotencyKey string           `json:"idempotency_key"`
	Critical       bool             `json:"critical"`
	CreatedAt      time.Time        `json:"created_at"`
	Payload        json.RawMessage  `json:"payload"`
	Display        *message.Display `json:"display,omitempty"`
}

// WebhookSink posts a signed envelope to a generic webhook destination.
//
// Client must be the SSRF-safe client from transport.New: the sink never builds
// an HTTP client of its own, and refuses to send when Client is nil.
type WebhookSink struct {
	Client *http.Client
	// Now supplies the signing timestamp and the previous-secret expiry check.
	Now func() time.Time
}

var _ Sink = (*WebhookSink)(nil)

// NewWebhookSink builds a WebhookSink around client with Now defaulting to time.Now.
func NewWebhookSink(client *http.Client) *WebhookSink {
	return &WebhookSink{Client: client, Now: time.Now}
}

// Deliver signs the envelope and posts it to the destination's URL.
//
// The signature is HMAC-SHA256 over timestamp + "." + body, hex encoded as
// "v1=<hex>". While the destination's previous signing secret is unexpired, a
// second "v1=" signature made with it is appended so receivers can rotate.
//
// The Outcome's Err never contains the webhook URL, its hostname or a secret: a
// *url.Error is reduced to its cause with the hostname masked, which keeps
// errors.Is and transport.IsBlocked working. The ResponseSnippet has the
// destination URL (and its path, query and userinfo) and both signing secrets
// redacted, because receivers often echo the request back in error pages.
// Retry-After is capped at maxRetryAfter (24h).
func (sink *WebhookSink) Deliver(
	ctx context.Context, destination registry.ResolvedDestination, request Request,
) Outcome {
	if sink.Client == nil {
		return Outcome{Err: errNoClient}
	}
	now := time.Now
	if sink.Now != nil {
		now = sink.Now
	}
	currentTime := now()

	signingSecret := destination.Secrets["signing_secret"]
	if signingSecret == "" || destination.Secrets["url"] == "" {
		return Outcome{Err: fmt.Errorf("%w: missing url or signing secret", ErrInvalidDelivery)}
	}
	// These two values become header values; refuse anything that is not
	// printable ASCII so a hostile value can never inject a header.
	if !isHeaderToken(request.MessageID) ||
		message.ValidateIdempotencyKey(request.Envelope.IdempotencyKey) != nil {
		return Outcome{Err: fmt.Errorf("%w: invalid message identifier", ErrInvalidDelivery)}
	}

	body, marshalErr := json.Marshal(webhookBody{
		MessageID:      request.MessageID,
		AppID:          request.Envelope.AppID,
		Channel:        request.Envelope.Channel,
		IdempotencyKey: request.Envelope.IdempotencyKey,
		Critical:       request.Envelope.Critical,
		CreatedAt:      request.CreatedAt.UTC(),
		Payload:        request.Envelope.Payload,
		Display:        request.Envelope.Display,
	})
	if marshalErr != nil {
		// The marshal error can quote payload content, so it is dropped.
		return Outcome{Err: fmt.Errorf("%w: encode body", ErrInvalidDelivery)}
	}

	timestamp := strconv.FormatInt(currentTime.Unix(), 10)
	signatures := []string{sign(signingSecret, timestamp, body)}
	if previousSecretActive(destination, currentTime) {
		signatures = append(signatures, sign(destination.Secrets["signing_secret_previous"], timestamp, body))
	}

	httpRequest, requestErr := http.NewRequestWithContext(
		ctx, http.MethodPost, destination.Secrets["url"], bytes.NewReader(body),
	)
	if requestErr != nil {
		// The parse error embeds the raw URL, which may carry a secret.
		return Outcome{Err: fmt.Errorf("%w: invalid request URL", ErrInvalidDelivery)}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", userAgent)
	httpRequest.Header.Set(headerMessageID, request.MessageID)
	httpRequest.Header.Set(headerIdempotencyKey, request.Envelope.IdempotencyKey)
	httpRequest.Header.Set(headerTimestamp, timestamp)
	httpRequest.Header.Set(headerSignature, strings.Join(signatures, ","))

	response, doErr := sink.Client.Do(httpRequest)
	if doErr != nil {
		return Outcome{Err: newRequestError(transport.Cause(doErr), httpRequest.URL.Hostname())}
	}
	defer func() { _ = response.Body.Close() }()

	snippet := transport.ReadSnippet(response.Body)
	return Outcome{
		StatusCode:      response.StatusCode,
		RetryAfter:      parseRetryAfter(response.Header.Get("Retry-After"), currentTime),
		ResponseSnippet: redactSnippet(snippet, redactionNeedles(destination, httpRequest.URL)),
	}
}

// redactionNeedles lists every string that must not survive in a response
// snippet: the destination URL as configured, its request URI, path, query and
// userinfo, and both signing secrets. A bare "/" path and empty values are
// skipped because they would mangle every snippet. Longest needles come first so
// a full URL is replaced whole before its parts are.
func redactionNeedles(destination registry.ResolvedDestination, parsed *url.URL) []string {
	candidates := []string{
		destination.Secrets["url"],
		parsed.RequestURI(),
		parsed.RawQuery,
		destination.Secrets["signing_secret"],
		destination.Secrets["signing_secret_previous"],
	}
	if len(parsed.Path) > 1 {
		candidates = append(candidates, parsed.Path)
	}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		candidates = append(candidates, parsed.User.Username(), password)
	}

	needles := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		// RequestURI of a root URL is "/", which is not worth redacting.
		if candidate == "" || candidate == "/" {
			continue
		}
		needles = append(needles, candidate)
	}
	slices.SortStableFunc(needles, func(left, right string) int { return len(right) - len(left) })
	return needles
}

// redactSnippet replaces every occurrence of each needle in snippet with a marker.
func redactSnippet(snippet string, needles []string) string {
	for _, needle := range needles {
		snippet = strings.ReplaceAll(snippet, needle, redactedMarker)
	}
	return snippet
}

// sign returns "v1=" plus the hex HMAC-SHA256 of timestamp + "." + body.
func sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return signatureScheme + hex.EncodeToString(mac.Sum(nil))
}

// previousSecretActive reports whether the rotation overlap is still open: the
// previous secret is set and the expiry parses and lies in the future. Anything
// else (missing secret, malformed or past expiry) fails closed to "not active".
func previousSecretActive(destination registry.ResolvedDestination, now time.Time) bool {
	if destination.Secrets["signing_secret_previous"] == "" {
		return false
	}
	expiresAt, parseErr := time.Parse(time.RFC3339, destination.Config["previous_secret_expires_at"])
	if parseErr != nil {
		return false
	}
	return now.Before(expiresAt)
}

// isHeaderToken reports whether value is non-empty printable ASCII with no
// whitespace, the same alphabet idempotency keys are validated against.
// It guards the message ID header; the idempotency key uses message's own rule.
func isHeaderToken(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

// parseRetryAfter parses a Retry-After header as delta-seconds or an HTTP-date.
// Negative values (including ones too large to parse) and dates in the past
// clamp to 0; anything beyond maxRetryAfter, including a number that overflows
// int64 and a date far in the future, is capped at maxRetryAfter. An unparseable
// or empty header gives nil.
func parseRetryAfter(header string, now time.Time) *time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	seconds, secondsErr := strconv.ParseInt(header, 10, 64)
	if secondsErr == nil {
		return durationPointer(clampedSeconds(seconds))
	}
	if errors.Is(secondsErr, strconv.ErrRange) {
		if strings.HasPrefix(header, "-") {
			return durationPointer(0)
		}
		return durationPointer(maxRetryAfter)
	}
	when, dateErr := http.ParseTime(header)
	if dateErr != nil {
		return nil
	}
	return durationPointer(min(max(when.Sub(now), 0), maxRetryAfter))
}

// clampedSeconds converts seconds to a Duration, clamping negatives to 0 and
// anything above maxRetryAfter to maxRetryAfter.
func clampedSeconds(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds > int64(maxRetryAfter/time.Second) {
		return maxRetryAfter
	}
	return time.Duration(seconds) * time.Second
}

// durationPointer returns a pointer to a copy of duration.
func durationPointer(duration time.Duration) *time.Duration { return &duration }

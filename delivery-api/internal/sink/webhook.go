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
	"net"
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

// requestError is a transport failure with the destination hostname masked out
// of its text. Unwrap returns the original cause so errors.Is, errors.As and
// transport.IsBlocked still see through it.
type requestError struct {
	message string
	cause   error
}

func (failure *requestError) Error() string { return failure.message }

func (failure *requestError) Unwrap() error { return failure.cause }

// newRequestError wraps cause with the destination's network identity masked
// out of its text, prefixed "<sinkName> request failed: " ("webhook", "discord"
// or "ntfy"). Every case-insensitive occurrence of hostname becomes a
// placeholder, because DNS and certificate errors quote the host they failed on
// and a webhook host can be tenant-identifying; an empty hostname (ntfy, whose
// origin is a fixed internal address) leaves the text as is. Resolved IPs, ports
// and resolver addresses are dropped as well (see addressFreeText), because the
// attempt log is readable through the admin API and must not reveal what an
// internal name resolved to.
func newRequestError(sinkName string, cause error, hostname string) *requestError {
	text := addressFreeText(cause)
	if hostname != "" {
		hostPattern := regexp.MustCompile("(?i)" + regexp.QuoteMeta(hostname))
		text = hostPattern.ReplaceAllLiteralString(text, hostMarker)
	}
	return &requestError{message: sinkName + " request failed: " + text, cause: cause}
}

// addressFreeText renders cause without network addresses. A *net.OpError is
// reduced to its operation, network and inner error, dropping the remote and
// local address it carries; a *net.DNSError is reduced to its failure text,
// dropping the name and the resolver address ("on 127.0.0.11:53"). Other errors
// keep their own text. The blocked-address error from the SSRF guard has no
// address of its own, so it reads the same as any other refusal.
func addressFreeText(cause error) string {
	var opErr *net.OpError
	if errors.As(cause, &opErr) {
		operation := opErr.Op
		if opErr.Net != "" {
			operation += " " + opErr.Net
		}
		if opErr.Err == nil {
			return operation
		}
		return operation + ": " + addressFreeText(opErr.Err)
	}
	var dnsErr *net.DNSError
	if errors.As(cause, &dnsErr) {
		return "lookup failed: " + dnsErr.Err
	}
	return cause.Error()
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

// WebhookSink posts a signed envelope to a generic webhook destination. Build
// one with NewWebhookSink; the zero value is not usable.
type WebhookSink struct {
	client *http.Client
	// now supplies the signing timestamp and the previous-secret expiry check.
	now func() time.Time
}

var _ Sink = (*WebhookSink)(nil)

// NewWebhookSink builds a WebhookSink around client, which must be the SSRF-safe
// client from transport.New: the sink never builds an HTTP client of its own.
// It panics when client is nil, because a sink that can never send would only
// fail later, at delivery time, so this fails fast at construction. The signing
// clock is time.Now.
func NewWebhookSink(client *http.Client) *WebhookSink {
	if client == nil {
		panic("sink.NewWebhookSink: client is required")
	}
	return &WebhookSink{client: client, now: time.Now}
}

// Deliver signs the envelope and posts it to the destination's URL.
//
// The signature is HMAC-SHA256 over timestamp + "." + body, hex encoded as
// "v1=<hex>". While the destination's previous signing secret is unexpired, a
// second "v1=" signature made with it is appended so receivers can rotate.
//
// The Outcome's Err never contains the webhook URL, its hostname, a resolved IP
// or port, a resolver address or a secret: a *url.Error is reduced to its cause
// with the hostname and addresses masked, which keeps errors.Is and
// transport.IsBlocked working. The ResponseSnippet has the
// destination URL (and its path, query and userinfo) and both signing secrets
// redacted, because receivers often echo the request back in error pages.
// Retry-After is capped at maxRetryAfter (24h).
func (sink *WebhookSink) Deliver(
	ctx context.Context, destination registry.ResolvedDestination, request Request,
) Outcome {
	currentTime := sink.now()

	signingSecret := destination.Secrets["signing_secret"]
	if signingSecret == "" || destination.Secrets["url"] == "" {
		return Outcome{Err: fmt.Errorf("%w: missing url or signing secret", ErrInvalidDelivery)}
	}
	// These two values become header values; refuse anything that is not
	// printable, non-space ASCII so a hostile value can never inject a header.
	if message.ValidateIdempotencyKey(request.MessageID) != nil ||
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

	response, doErr := sink.client.Do(httpRequest)
	if doErr != nil {
		return Outcome{Err: newRequestError("webhook", transport.Cause(doErr), httpRequest.URL.Hostname())}
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

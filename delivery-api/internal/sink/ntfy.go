package sink

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	// defaultNtfyTimeout bounds one publish when NtfyConfig.Timeout is unset.
	defaultNtfyTimeout = 5 * time.Second

	ntfyTopicKey       = "topic"
	ntfyAccessTokenKey = "access_token"
)

// ntfyTopicPattern is the topic shape ntfy accepts. It must stay identical to
// the pattern registry/validate.go applies when a destination is registered.
var ntfyTopicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NtfyConfig configures an NtfySink.
type NtfyConfig struct {
	// BaseURL is the ntfy server origin, e.g. "http://ntfy:80" in-compose. A
	// trailing slash is tolerated. Empty means ntfy is not configured.
	BaseURL string
	// Timeout bounds each publish request. Zero defaults to 5 seconds.
	Timeout time.Duration
}

// NtfySink publishes a message's display text to an ntfy topic. Build one with
// NewNtfySink; the zero value is not usable.
type NtfySink struct {
	baseURL string
	client  *http.Client
}

var _ Sink = (*NtfySink)(nil)

// NewNtfySink builds an NtfySink for config.BaseURL.
//
// It deliberately builds its own plain http.Client instead of using
// transport.New: that client is https-only and SSRF-guarded, so it could never
// reach the internal compose origin (http://ntfy:80). The origin is a fixed,
// operator-set environment value, never an operator-supplied destination URL, so
// the guard has nothing to protect here. Do not point this sink at anything a
// destination or an envelope can influence.
func NewNtfySink(config NtfyConfig) *NtfySink {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultNtfyTimeout
	}
	return &NtfySink{
		baseURL: strings.TrimSuffix(config.BaseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

// Configured reports whether an ntfy origin is set. The caller must not register
// the sink for any destination type when it is false.
func (sink *NtfySink) Configured() bool { return sink.baseURL != "" }

// Deliver publishes the envelope's display to the destination's topic with one
// POST (dispatch owns retries). The body is display.body, or the title when
// there is no body (and then no Title header is sent); Title, Priority and Click
// headers are set only when non-empty, and a non-ASCII title is RFC 2047
// Q-encoded. The Authorization bearer header is sent only when the destination
// has an access_token secret.
//
// A destination without a valid topic, a display with neither title nor body, or
// a header value containing CR or LF is a permanent ErrInvalidDelivery and
// nothing is sent. The Outcome's Err and ResponseSnippet never contain the
// access token. Unlike Discord, no status proves the destination gone: a 401 or
// 403 is terminal through dispatch.Classify and a 429 or 5xx is retryable.
func (sink *NtfySink) Deliver(
	ctx context.Context, destination registry.ResolvedDestination, request Request,
) Outcome {
	topic := destination.Config[ntfyTopicKey]
	if !ntfyTopicPattern.MatchString(topic) {
		return Outcome{Err: fmt.Errorf("%w: missing or invalid topic", ErrInvalidDelivery)}
	}
	display := request.Envelope.Display
	if display == nil || (display.Title == "" && display.Body == "") {
		return Outcome{Err: fmt.Errorf("%w: display has neither title nor body", ErrInvalidDelivery)}
	}
	accessToken := destination.Secrets[ntfyAccessTokenKey]
	if containsLineBreak(display.Title, display.Priority, display.URL, accessToken) {
		return Outcome{Err: fmt.Errorf("%w: header value contains a line break", ErrInvalidDelivery)}
	}

	body := display.Body
	if body == "" {
		body = display.Title
	}
	httpRequest, requestErr := http.NewRequestWithContext(
		ctx, http.MethodPost, sink.baseURL+"/"+topic, strings.NewReader(body),
	)
	if requestErr != nil {
		// The request error embeds the raw URL, which names the topic.
		return Outcome{Err: fmt.Errorf("%w: invalid request URL", ErrInvalidDelivery)}
	}
	httpRequest.Header.Set("User-Agent", userAgent)
	setNtfyHeaders(httpRequest.Header, display, accessToken)

	response, doErr := sink.client.Do(httpRequest)
	if doErr != nil {
		cause := transport.Cause(doErr)
		return Outcome{Err: &requestError{message: "ntfy request failed: " + addressFreeText(cause), cause: cause}}
	}
	defer func() { _ = response.Body.Close() }()

	snippet := transport.ReadSnippet(response.Body)
	if accessToken != "" {
		snippet = redactSnippet(snippet, []string{accessToken})
	}
	outcome := Outcome{StatusCode: response.StatusCode, ResponseSnippet: snippet}
	if response.StatusCode == http.StatusTooManyRequests {
		outcome.RetryAfter = parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
	}
	return outcome
}

// setNtfyHeaders sets each ntfy publish header whose value is non-empty.
func setNtfyHeaders(header http.Header, display *message.Display, accessToken string) {
	if display.Title != "" && display.Body != "" {
		header.Set("Title", encodeHeaderValue(display.Title))
	}
	if display.Priority != "" {
		header.Set("Priority", display.Priority)
	}
	if display.URL != "" {
		header.Set("Click", display.URL)
	}
	if accessToken != "" {
		header.Set("Authorization", "Bearer "+accessToken)
	}
}

// encodeHeaderValue returns value unchanged when it is pure ASCII, else its RFC
// 2047 Q-encoded UTF-8 form, which ntfy decodes; raw non-ASCII bytes in an HTTP
// header are not portable.
func encodeHeaderValue(value string) string {
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x80 {
			return mime.QEncoding.Encode("utf-8", value)
		}
	}
	return value
}

// containsLineBreak reports whether any value holds a CR or LF, which would let
// the value inject another header.
func containsLineBreak(values ...string) bool {
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return true
		}
	}
	return false
}

package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	// discordMaxContentRunes is Discord's limit on a message's "content", counted
	// in runes (not bytes).
	discordMaxContentRunes = 2000
	// discordTruncationMarker ends content that had to be cut to fit the limit.
	discordTruncationMarker = "…"

	// discordUnknownWebhookCode is the error code in the body of the 404 Discord
	// returns for a deleted webhook (Unknown Webhook).
	discordUnknownWebhookCode = 10015

	discordWebhookURLKey = "webhook_url"
)

// discordBody is the JSON body posted to a Discord webhook. Field order is the
// wire order.
type discordBody struct {
	Content         string                 `json:"content"`
	AllowedMentions discordAllowedMentions `json:"allowed_mentions"`
}

// discordAllowedMentions with an empty Parse list suppresses every mention
// (@everyone, @here, roles and users) in the content. Parse must encode as [],
// never null: a missing or null parse would let Discord apply its defaults.
type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

// discordRateLimit is the part of a Discord 429 body the sink reads.
type discordRateLimit struct {
	RetryAfter *float64 `json:"retry_after"`
}

// discordError is the part of a Discord error body the sink reads: the numeric
// error code.
type discordError struct {
	Code *int `json:"code"`
}

// DiscordSink posts a message's display text to a Discord webhook. Build one
// with NewDiscordSink; the zero value is not usable.
type DiscordSink struct {
	client *http.Client
}

var _ Sink = (*DiscordSink)(nil)

// NewDiscordSink builds a DiscordSink around client, which must be the SSRF-safe
// client from transport.New: the sink never builds an HTTP client of its own. It
// panics when client is nil, because a sink that can never send would only fail
// later, at delivery time, so this fails fast at construction.
func NewDiscordSink(client *http.Client) *DiscordSink {
	if client == nil {
		panic("sink.NewDiscordSink: client is required")
	}
	return &DiscordSink{client: client}
}

// Deliver posts the envelope's display as plain message content, "**title**",
// body and url joined by newlines, with every mention suppressed. Content over
// 2000 runes is cut to exactly 2000 ending in "…". It makes one POST per call
// (dispatch owns retries) and asks Discord to wait (wait=true) so a rejected
// message is reported rather than accepted blindly.
//
// A 429 reports the "retry_after" seconds from the JSON body, falling back to the
// Retry-After header, capped at maxRetryAfter. A 401, or a 404 whose body carries
// the Unknown Webhook error code (10015), sets Outcome.Gone: a deleted webhook
// and a webhook whose token no longer matches cannot recover by retrying. Any
// other 404 (for example Unknown Channel, 10003, from a deleted thread_id) is an
// ordinary non-Gone failure.
//
// A destination without a usable webhook_url, or a display with neither title
// nor body, is a permanent ErrInvalidDelivery and nothing is sent. The Outcome's
// Err and ResponseSnippet never contain the webhook URL, its token or its host:
// the snippet has them redacted because Discord and proxies echo the request
// back in error pages.
func (sink *DiscordSink) Deliver(
	ctx context.Context, destination registry.ResolvedDestination, request Request,
) Outcome {
	webhookURL := destination.Secrets[discordWebhookURLKey]
	target, parseErr := url.Parse(webhookURL)
	if webhookURL == "" || parseErr != nil || target.Host == "" {
		// The parse error embeds the raw URL, which carries the webhook token.
		return Outcome{Err: fmt.Errorf("%w: missing or invalid webhook url", ErrInvalidDelivery)}
	}
	query := target.Query()
	query.Set("wait", "true")
	target.RawQuery = query.Encode()

	content := discordContent(request.Envelope.Display)
	if content == "" {
		return Outcome{Err: fmt.Errorf("%w: display has neither title nor body", ErrInvalidDelivery)}
	}
	body, marshalErr := json.Marshal(discordBody{
		Content:         truncateRunes(content, discordMaxContentRunes),
		AllowedMentions: discordAllowedMentions{Parse: []string{}},
	})
	if marshalErr != nil {
		// The marshal error can quote message content, so it is dropped.
		return Outcome{Err: fmt.Errorf("%w: encode body", ErrInvalidDelivery)}
	}

	httpRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if requestErr != nil {
		// The request error embeds the raw URL, which carries the webhook token.
		return Outcome{Err: fmt.Errorf("%w: invalid request URL", ErrInvalidDelivery)}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", userAgent)

	response, doErr := sink.client.Do(httpRequest)
	if doErr != nil {
		return Outcome{Err: newRequestError("discord", transport.Cause(doErr), target.Hostname())}
	}
	defer func() { _ = response.Body.Close() }()

	snippet := transport.ReadSnippet(response.Body)
	outcome := Outcome{
		StatusCode:      response.StatusCode,
		ResponseSnippet: redactSnippet(snippet, discordRedactionNeedles(webhookURL, target)),
		Gone:            discordWebhookIsGone(response.StatusCode, snippet),
	}
	if response.StatusCode == http.StatusTooManyRequests {
		outcome.RetryAfter = discordRetryAfter(snippet, response.Header.Get("Retry-After"))
	}
	return outcome
}

// discordWebhookIsGone reports whether a response proves the webhook can never
// work again. A 401 means the webhook's token no longer matches. A 404 proves it
// only when Discord's error code is Unknown Webhook (10015): Discord also answers
// 404 for a healthy webhook whose thread_id points at a deleted thread (code
// 10003, Unknown Channel), which an operator can fix, so any other 404, and a 404
// whose body does not parse, is an ordinary failure. responseBody is the
// size-capped, not yet redacted snippet; it is only parsed, never returned.
func discordWebhookIsGone(statusCode int, responseBody string) bool {
	switch statusCode {
	case http.StatusUnauthorized:
		return true
	case http.StatusNotFound:
		var failure discordError
		return json.Unmarshal([]byte(responseBody), &failure) == nil &&
			failure.Code != nil && *failure.Code == discordUnknownWebhookCode
	default:
		return false
	}
}

// discordContent renders display as "**title**\nbody\nurl", joining only the
// non-blank parts. It returns "" when display has no renderable text (see
// message.Display.HasRenderableText), since a link alone is not a message.
func discordContent(display *message.Display) string {
	if !display.HasRenderableText() {
		return ""
	}
	parts := make([]string, 0, 3)
	if strings.TrimSpace(display.Title) != "" {
		parts = append(parts, "**"+display.Title+"**")
	}
	if strings.TrimSpace(display.Body) != "" {
		parts = append(parts, display.Body)
	}
	if display.URL != "" {
		parts = append(parts, display.URL)
	}
	return strings.Join(parts, "\n")
}

// truncateRunes returns text unchanged when it has at most limit runes;
// otherwise the first limit-1 runes followed by discordTruncationMarker (one
// rune), so the result has exactly limit runes. It cuts on rune boundaries, never
// inside a multi-byte character.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + discordTruncationMarker
}

// discordRetryAfter reads a 429's wait time: the "retry_after" seconds (a float)
// from the JSON body first, then the Retry-After header. The result is clamped to
// [0, maxRetryAfter]; nil means the response named no wait.
func discordRetryAfter(responseBody string, retryAfterHeader string) *time.Duration {
	var limit discordRateLimit
	if json.Unmarshal([]byte(responseBody), &limit) == nil && limit.RetryAfter != nil {
		seconds := *limit.RetryAfter
		switch {
		case math.IsNaN(seconds) || seconds <= 0:
			return durationPointer(0)
		case seconds >= maxRetryAfter.Seconds():
			return durationPointer(maxRetryAfter)
		default:
			return durationPointer(time.Duration(seconds * float64(time.Second)))
		}
	}
	return parseRetryAfter(retryAfterHeader, time.Now())
}

// discordRedactionNeedles lists every string that must not survive in a response
// snippet: the webhook URL as configured, the URL as sent, its request URI, path,
// token segment and host. A bare "/" and empty values are skipped because they
// would mangle every snippet. Longest needles come first so a full URL is
// replaced whole before its parts are.
func discordRedactionNeedles(webhookURL string, sent *url.URL) []string {
	candidates := []string{
		webhookURL,
		sent.String(),
		sent.RequestURI(),
		sent.Path,
		path.Base(sent.Path),
		sent.Host,
		sent.Hostname(),
	}
	needles := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" || candidate == "/" || candidate == "." {
			continue
		}
		needles = append(needles, candidate)
	}
	slices.SortStableFunc(needles, func(left, right string) int { return len(right) - len(left) })
	return needles
}

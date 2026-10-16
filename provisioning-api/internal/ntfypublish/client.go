// Package ntfypublish is a thin outbound HTTP client that publishes a message
// to a topic on the self-hosted ntfy server and returns the message id ntfy
// assigns to it.
//
// The provisioning-api otherwise talks to ntfy only by shelling the ntfy CLI
// against the shared auth DB (see internal/ntfycli); this package is the sole
// path that makes a real HTTP call to the ntfy server, used by the admin
// "send test notification" flow to publish over an ephemeral, write-only
// bearer token. A Message may carry optional Title, Tags and Click, sent as the
// ntfy Title / Tags / Click headers.
//
// When NTFY_PUBLISH_URL is unset (e.g. a stack without a reachable ntfy origin)
// Config.BaseURL is left empty and Configured reports false so the caller can
// fail fast instead of dialing an empty URL.
package ntfypublish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// defaultTimeout is the HTTP client timeout used when Config.Timeout is unset.
const defaultTimeout = 5 * time.Second

// maxErrorBodySnippetLength bounds how much of a non-2xx response body is
// included in the returned error, so a large/unexpected body never bloats
// logs.
const maxErrorBodySnippetLength = 256

// Config configures a Client. An empty BaseURL means no ntfy publish origin is
// configured; Configured reports false and callers should skip publishing.
type Config struct {
	// BaseURL is the ntfy server origin, e.g. "http://ntfy:80" in-compose. A
	// trailing slash is tolerated (trimmed internally).
	BaseURL string
	// Timeout bounds each publish request. Optional; defaults to 5 seconds.
	Timeout time.Duration
}

// Client publishes messages to the ntfy server over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a Client from config.
func NewClient(config Config) *Client {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &Client{
		baseURL:    strings.TrimSuffix(config.BaseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Configured reports whether a publish origin is configured (BaseURL is
// non-empty). Callers should skip publishing when this is false.
func (client *Client) Configured() bool {
	return client.baseURL != ""
}

// publishResponseBody is the subset of the ntfy publish response this client
// cares about — the id ntfy assigns to the published message.
type publishResponseBody struct {
	ID string `json:"id"`
}

// Message is one notification to publish to ntfy.
type Message struct {
	// Topic is the concrete ntfy topic to publish to.
	Topic string
	// Token is the write-only ntfy bearer token used to authorize the publish.
	Token string
	// Body is the notification message text.
	Body string
	// Title is the optional notification title. Empty sends no Title header; a
	// non-ASCII title is RFC 2047 Q-encoded, which ntfy decodes.
	Title string
	// Tags are optional ntfy tags/emoji short codes, sent comma-joined. Blank
	// entries are skipped; if none remain no Tags header is sent.
	Tags []string
	// Click is the optional URL opened when the notification is tapped. Empty
	// sends no Click header.
	Click string
}

// encodeHeaderValue returns value unchanged when every byte is ASCII, else the
// RFC 2047 Q-encoded form (UTF-8) that ntfy decodes, since raw non-ASCII bytes
// in an HTTP header are not portable.
func encodeHeaderValue(value string) string {
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x80 {
			return mime.QEncoding.Encode("utf-8", value)
		}
	}
	return value
}

// joinNonBlankTags comma-joins tags, skipping empty/whitespace-only entries so
// the result never contains an empty element; it is "" when nothing remains
// (fail closed: no Tags header is sent).
func joinNonBlankTags(tags []string) string {
	nonBlankTags := make([]string, 0, len(tags))
	for _, tag := range tags {
		if strings.TrimSpace(tag) != "" {
			nonBlankTags = append(nonBlankTags, tag)
		}
	}
	return strings.Join(nonBlankTags, ",")
}

// Publish POSTs message.Body to {baseURL}/{message.Topic} with a bearer token,
// treating any 2xx as success and returning the message id ntfy assigns. The
// optional Title, Tags and Click headers are set only when the matching field
// is non-empty. A non-2xx status returns an error identifying the status code
// (with a short body snippet). The bearer token is a write-only ntfy token
// (ntfy is not behind Cloudflare Access, so no CF-Access-Client-* headers are
// sent).
func (client *Client) Publish(ctx context.Context, message Message) (string, error) {
	request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/"+message.Topic, strings.NewReader(message.Body))
	if requestErr != nil {
		return "", fmt.Errorf("ntfy publish: build request: %w", requestErr)
	}
	request.Header.Set("Authorization", "Bearer "+message.Token)
	if message.Title != "" {
		request.Header.Set("Title", encodeHeaderValue(message.Title))
	}
	if joinedTags := joinNonBlankTags(message.Tags); joinedTags != "" {
		request.Header.Set("Tags", joinedTags)
	}
	if message.Click != "" {
		request.Header.Set("Click", message.Click)
	}

	response, doErr := client.httpClient.Do(request)
	if doErr != nil {
		return "", fmt.Errorf("ntfy publish: request failed: %w", doErr)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("ntfy publish failed (%d %s): %s", response.StatusCode, http.StatusText(response.StatusCode), readBodySnippet(response.Body))
	}

	var decoded publishResponseBody
	if decodeErr := json.NewDecoder(response.Body).Decode(&decoded); decodeErr != nil {
		return "", fmt.Errorf("ntfy publish: decode response: %w", decodeErr)
	}
	return decoded.ID, nil
}

// readBodySnippet best-effort reads a short prefix of a response body for
// inclusion in an error message. Body-read problems are never fatal here — an
// empty snippet is returned instead.
func readBodySnippet(body io.Reader) string {
	limitedReader := io.LimitReader(body, maxErrorBodySnippetLength)
	snippetBytes, readErr := io.ReadAll(limitedReader)
	if readErr != nil {
		return ""
	}
	return strings.TrimSpace(string(snippetBytes))
}

// Package ntfypublish is a thin outbound HTTP client that publishes a message
// to a topic on the self-hosted ntfy server and returns the message id ntfy
// assigns to it.
//
// The provisioning-api otherwise talks to ntfy only by shelling the ntfy CLI
// against the shared auth DB (see internal/ntfycli); this package is the sole
// path that makes a real HTTP call to the ntfy server, used by the admin
// "send test notification" flow to publish over an ephemeral, write-only
// bearer token. When NTFY_PUBLISH_URL is unset (e.g. a stack without a
// reachable ntfy origin) Config.BaseURL is left empty and Configured reports
// false so the caller can fail fast instead of dialing an empty URL.
package ntfypublish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// Publish POSTs message to {baseURL}/{topic} with a bearer token, treating any
// 2xx as success and returning the message id ntfy assigns. A non-2xx status
// returns an error identifying the status code (with a short body snippet). The
// bearer token is a write-only ntfy token (ntfy is not behind Cloudflare
// Access, so no CF-Access-Client-* headers are sent).
func (client *Client) Publish(ctx context.Context, topic string, token string, message string) (string, error) {
	request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/"+topic, strings.NewReader(message))
	if requestErr != nil {
		return "", fmt.Errorf("ntfy publish: build request: %w", requestErr)
	}
	request.Header.Set("Authorization", "Bearer "+token)

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

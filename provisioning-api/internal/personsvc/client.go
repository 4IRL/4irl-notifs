// Package personsvc is a thin HTTP client for the person-service Cloudflare
// Worker, which owns a D1 reverse-index mapping personHash -> email.
//
// The provisioning-api is the source of truth for ntfy identity (one ntfy
// user per person, keyed on the email hash); the person-service exists
// solely so operators can resolve a personHash back to the email that
// produced it (support/debugging, never the provisioning hot path). Callers
// in this repo treat every write to person-service as a best-effort,
// ntfy-first/D1-second dual-write: ntfy provisioning is the operation that
// must succeed, and a person-service failure is logged and swallowed by the
// caller rather than failing the provision. When the Worker is not deployed
// (e.g. the local dev stack), Config.BaseURL is left empty and Configured
// reports false so callers skip the dual-write entirely.
package personsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultTimeout is the HTTP client timeout used when Config.HTTPClient is
// nil and Config.Timeout is unset.
const defaultTimeout = 5 * time.Second

// maxErrorBodySnippetLength bounds how much of a non-2xx response body is
// included in the returned error, so a large/unexpected body never bloats
// logs.
const maxErrorBodySnippetLength = 256

// personPath is the person-service endpoint UpsertPerson calls.
const personPath = "/person"

// Config configures a Client. An empty BaseURL means the person-service is
// not deployed/configured; Configured reports false and callers skip the
// dual-write entirely.
type Config struct {
	// BaseURL is the person-service origin, e.g.
	// "https://notifs-people.example.com". A trailing slash is tolerated
	// (trimmed internally).
	BaseURL string
	// AccessClientID is the Cloudflare Access Service-Token id. The
	// CF-Access-Client-Id header is sent only when this is non-empty.
	AccessClientID string
	// AccessClientSecret is the Cloudflare Access Service-Token secret. The
	// CF-Access-Client-Secret header is sent only when this is non-empty.
	AccessClientSecret string
	// HTTPClient is the HTTP client used to make requests. Optional; when
	// nil, a client with Timeout is built.
	HTTPClient *http.Client
	// Timeout bounds each request. Optional; defaults to 5 seconds, and is
	// only used when HTTPClient is nil.
	Timeout time.Duration
}

// Client is a person-service HTTP client.
type Client struct {
	baseURL            string
	accessClientID     string
	accessClientSecret string
	httpClient         *http.Client
}

// NewClient builds a Client from config.
func NewClient(config Config) *Client {
	httpClient := config.HTTPClient
	if httpClient == nil {
		timeout := config.Timeout
		if timeout == 0 {
			timeout = defaultTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:            strings.TrimSuffix(config.BaseURL, "/"),
		accessClientID:     config.AccessClientID,
		accessClientSecret: config.AccessClientSecret,
		httpClient:         httpClient,
	}
}

// Configured reports whether the person-service is deployed/configured
// (BaseURL is non-empty). Callers should skip the dual-write entirely when
// this is false.
func (client *Client) Configured() bool {
	return client.baseURL != ""
}

// upsertPersonRequestBody is the wire shape person-service's PUT /person
// expects.
type upsertPersonRequestBody struct {
	PersonHash string `json:"person_hash"`
	Email      string `json:"email"`
}

// UpsertPerson upserts the personHash -> email mapping via
// PUT {baseURL}/person. Any 2xx status is treated as success; a non-2xx
// status returns an error identifying the status code.
func (client *Client) UpsertPerson(ctx context.Context, personHash string, email string) error {
	requestBody, marshalErr := json.Marshal(upsertPersonRequestBody{PersonHash: personHash, Email: email})
	if marshalErr != nil {
		return fmt.Errorf("person-service: encode request body: %w", marshalErr)
	}

	request, requestErr := http.NewRequestWithContext(ctx, http.MethodPut, client.baseURL+personPath, bytes.NewReader(requestBody))
	if requestErr != nil {
		return fmt.Errorf("person-service: build request: %w", requestErr)
	}
	request.Header.Set("Content-Type", "application/json")
	if client.accessClientID != "" {
		request.Header.Set("CF-Access-Client-Id", client.accessClientID)
	}
	if client.accessClientSecret != "" {
		request.Header.Set("CF-Access-Client-Secret", client.accessClientSecret)
	}

	response, doErr := client.httpClient.Do(request)
	if doErr != nil {
		return fmt.Errorf("person-service: request failed: %w", doErr)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("person-service: unexpected status %d %s: %s", response.StatusCode, http.StatusText(response.StatusCode), readBodySnippet(response.Body))
	}
	return nil
}

// readBodySnippet best-effort reads a short prefix of a response body for
// inclusion in an error message. Body-read problems are never fatal here —
// an empty snippet is returned instead.
func readBodySnippet(body io.Reader) string {
	limitedReader := io.LimitReader(body, maxErrorBodySnippetLength)
	snippetBytes, readErr := io.ReadAll(limitedReader)
	if readErr != nil {
		return ""
	}
	return strings.TrimSpace(string(snippetBytes))
}

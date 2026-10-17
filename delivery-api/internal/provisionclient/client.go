// Package provisionclient is the narrow client delivery-api uses to mint a
// per-app ntfy publisher token through provisioning-api's POST /v1/provision-app.
//
// It talks to a fixed, operator-configured internal origin (for example
// http://provisioning-api:8080 on the compose network), so it deliberately uses
// a bare http.Client rather than the https-only, SSRF-guarded transport the
// outbound sinks share. When no origin is configured Configured reports false
// and MintPublisher fails closed without dialing.
package provisionclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// defaultTimeout is the HTTP client timeout used when Config.Timeout is unset.
	defaultTimeout = 5 * time.Second

	provisionAppPath = "/v1/provision-app"

	// maxResponseBytes caps how much of a mint response is read; a real grant is
	// a few hundred bytes.
	maxResponseBytes = 64 << 10
)

// tokenPattern is the shape of an ntfy access token. A response whose token does
// not match is rejected rather than stored.
var tokenPattern = regexp.MustCompile(`^tk_[a-z0-9]{29}$`)

// ErrNotConfigured means no provisioning-api origin is configured.
var ErrNotConfigured = errors.New("provisioning-api origin is not configured")

// Config configures a Client. An empty BaseURL means no provisioning-api origin
// is configured; Configured reports false and MintPublisher fails closed.
type Config struct {
	// BaseURL is the provisioning-api origin, for example
	// "http://provisioning-api:8080". A trailing slash is tolerated.
	BaseURL string
	// Timeout bounds each request. Optional; defaults to 5 seconds.
	Timeout time.Duration
}

// PublisherGrant is the publisher identity and one-time token provisioning-api
// returns for an app. Token is a secret: it is never logged or put in an error.
type PublisherGrant struct {
	AppID           string `json:"app_id"`
	PublisherUserID string `json:"publisher_user_id"`
	TopicPattern    string `json:"topic_pattern"`
	Token           string `json:"token"`
}

// Client mints publisher tokens through provisioning-api.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New builds a Client from config.
func New(config Config) *Client {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &Client{
		baseURL: strings.TrimSuffix(config.BaseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
			// A redirect is never followed: a 307/308 would re-POST the app_id body to
			// wherever it points. The 3xx response comes back as an ordinary non-2xx
			// status error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Configured reports whether a provisioning-api origin is configured.
func (client *Client) Configured() bool {
	return client.baseURL != ""
}

// provisionAppRequest is the JSON body for provision-app.
type provisionAppRequest struct {
	AppID  string `json:"app_id"`
	Rotate bool   `json:"rotate"`
}

// MintPublisher asks provisioning-api for a publisher token for appID.
//
// rotate is always false. provision-app is an additive mint, so each call
// returns a new token and leaves existing ones valid; rotate:true would revoke
// every publisher-labeled token for the app, including the ones apps hold for
// direct publishing.
//
// A non-2xx status (a redirect included, since none is followed) returns an
// error naming only the status code, never the response body, so nothing the
// server echoes can reach logs or the API. At most 64 KiB of a response is read.
// A token
// that does not look like an ntfy access token is an error. The token is never
// logged or put in an error.
func (client *Client) MintPublisher(ctx context.Context, appID string) (PublisherGrant, error) {
	if !client.Configured() {
		return PublisherGrant{}, ErrNotConfigured
	}
	body, marshalErr := json.Marshal(provisionAppRequest{AppID: appID, Rotate: false})
	if marshalErr != nil {
		return PublisherGrant{}, fmt.Errorf("provision-app: encode request: %w", marshalErr)
	}
	request, requestErr := http.NewRequestWithContext(
		ctx, http.MethodPost, client.baseURL+provisionAppPath, bytes.NewReader(body))
	if requestErr != nil {
		return PublisherGrant{}, fmt.Errorf("provision-app: build request: %w", requestErr)
	}
	request.Header.Set("Content-Type", "application/json")

	response, doErr := client.httpClient.Do(request)
	if doErr != nil {
		return PublisherGrant{}, fmt.Errorf("provision-app: request failed: %w", doErr)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return PublisherGrant{}, fmt.Errorf("provision-app: unexpected status %d", response.StatusCode)
	}

	var grant PublisherGrant
	if decodeErr := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&grant); decodeErr != nil {
		return PublisherGrant{}, errors.New("provision-app: decode response failed")
	}
	if !tokenPattern.MatchString(grant.Token) {
		return PublisherGrant{}, errors.New("provision-app: response token is malformed")
	}
	return grant, nil
}

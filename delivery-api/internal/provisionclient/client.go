// Package provisionclient is the narrow client delivery-api uses to mint a
// per-app ntfy publisher token through provisioning-api's POST /v1/provision-app.
//
// It talks to a fixed, operator-configured internal origin (for example
// http://provisioning-api:8080 on the compose network), so it deliberately uses
// a bare http.Client rather than the https-only, SSRF-guarded transport the
// outbound sinks share. Every request carries the internal credential header
// provisioning-api checks. When no origin or no credential is configured
// Configured reports false and MintPublisher fails closed without dialing.
//
// It also relays POST /v1/provision and POST /v1/deprovision (Provision, Deprovision): those hand the
// upstream status, body, Content-Type and Retry-After back unchanged as a Passthrough, and fail closed
// with ErrNotConfigured the same way. Redirects are never followed and there is no retry.
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
	//
	// Rule: max(15s, 3 x the worst observed provisioning call), rounded up to a whole second. Measured
	// against the local stack on 2026-10-24: 20 sequential POST /v1/provision (min 126ms, mean 141ms,
	// max 163ms), 20 sequential POST /v1/provision-app (min 93ms, mean 100ms, max 116ms) and 5 concurrent
	// POST /v1/provision, which queue behind the ntfy CLI mutex (min 542ms, mean 622ms, max 663ms). The worst
	// observed call was 663ms, so 3 x worst = 2s and the 15s floor wins. Raise it only with a new
	// measurement; there is no automatic retry, so a timeout surfaces to the caller as a failed mint.
	defaultTimeout = 15 * time.Second

	// credentialHeader carries the internal credential provisioning-api checks. Its value is a secret
	// and is never logged, echoed or put in an error.
	credentialHeader = "X-Notifs-Internal-Credential"

	provisionAppPath = "/v1/provision-app"

	// maxResponseBytes caps how much of a response is read, for a mint and for a pass-through; a real
	// grant is a few hundred bytes. A mint truncates at the cap (and then fails to decode); a
	// pass-through over the cap is an error, never a truncated answer.
	maxResponseBytes = 64 << 10
)

// tokenPattern is the shape of an ntfy access token. A response whose token does
// not match is rejected rather than stored.
var tokenPattern = regexp.MustCompile(`^tk_[a-z0-9]{29}$`)

// ErrNotConfigured means no provisioning-api origin is configured.
var ErrNotConfigured = errors.New("provisioning-api origin is not configured")

// Config configures a Client. An empty BaseURL or an empty Credential means the
// client is not usable; Configured reports false and MintPublisher fails closed.
type Config struct {
	// BaseURL is the provisioning-api origin, for example
	// "http://provisioning-api:8080". A trailing slash is tolerated.
	BaseURL string
	// Credential is the internal shared credential sent as X-Notifs-Internal-Credential on every
	// request. It is a secret: never logged, echoed or put in an error.
	Credential string
	// Timeout bounds each request. Optional; defaults to 15 seconds.
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
	credential string
	httpClient *http.Client
}

// New builds a Client from config.
func New(config Config) *Client {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &Client{
		baseURL:    strings.TrimSuffix(config.BaseURL, "/"),
		credential: config.Credential,
		httpClient: &http.Client{
			Timeout: timeout,
			// A redirect is never followed: a 307/308 would re-POST the app_id body to
			// wherever it points. The 3xx response comes back as an ordinary non-2xx
			// status error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Configured reports whether both a provisioning-api origin and the internal
// credential are configured. A client with either missing never dials.
func (client *Client) Configured() bool {
	return client.baseURL != "" && client.credential != ""
}

// newJSONRequest is the single request-building path: every call the client
// makes goes through it, so the credential and Content-Type headers are set in
// one place and later methods inherit them.
func (client *Client) newJSONRequest(ctx context.Context, path string, body []byte) (*http.Request, error) {
	request, requestErr := http.NewRequestWithContext(
		ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(body))
	if requestErr != nil {
		return nil, requestErr
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(credentialHeader, client.credential)
	return request, nil
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
	request, requestErr := client.newJSONRequest(ctx, provisionAppPath, body)
	if requestErr != nil {
		return PublisherGrant{}, fmt.Errorf("provision-app: build request: %w", requestErr)
	}

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

const (
	provisionPath   = "/v1/provision"
	deprovisionPath = "/v1/deprovision"
)

// Passthrough is a provisioning-api answer handed back unchanged: the upstream status, body bytes,
// Content-Type and Retry-After header. Body can carry a one-time secret (a provision response holds the
// token and possibly the password), so it is never logged or put in an error.
//
// ContentType and RetryAfter are relayed verbatim from the upstream and are untrusted: a caller must not
// reflect an arbitrary ContentType (for example text/html would be an XSS risk on this origin) or
// RetryAfter to its own client blindly; validate or force them (application/json, a numeric delay).
type Passthrough struct {
	Status      int
	Body        []byte
	ContentType string
	RetryAfter  string
}

// DeprovisionTarget names the person to remove. Exactly one of Email and UserID is set; UserID is the
// ntfy user id (u_<person_hash>).
type DeprovisionTarget struct {
	Email  string
	UserID string
}

type appEmailRequest struct {
	AppID string `json:"app_id"`
	Email string `json:"email"`
}

type deprovisionByUserIDRequest struct {
	AppID  string `json:"app_id"`
	UserID string `json:"user_id"`
}

// Provision asks provisioning-api to add the person with email to appID and returns its answer as-is.
// A non-2xx status is not an error; only a failure to get an answer is. Error text never contains the
// email, the credential or the response body.
func (client *Client) Provision(ctx context.Context, appID string, email string) (Passthrough, error) {
	body, marshalErr := json.Marshal(appEmailRequest{AppID: appID, Email: email})
	if marshalErr != nil {
		return Passthrough{}, errors.New("provision: encode request failed")
	}
	return client.passthrough(ctx, "provision", provisionPath, body)
}

// Deprovision asks provisioning-api to remove the person from appID and returns its answer as-is.
// Exactly one of target.Email and target.UserID must be set.
func (client *Client) Deprovision(ctx context.Context, appID string, target DeprovisionTarget) (Passthrough, error) {
	if (target.Email == "") == (target.UserID == "") {
		return Passthrough{}, errors.New("deprovision: exactly one of email and user_id is required")
	}
	var payload any
	if target.Email != "" {
		payload = appEmailRequest{AppID: appID, Email: target.Email}
	} else {
		payload = deprovisionByUserIDRequest{AppID: appID, UserID: target.UserID}
	}
	body, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return Passthrough{}, errors.New("deprovision: encode request failed")
	}
	return client.passthrough(ctx, "deprovision", deprovisionPath, body)
}

// passthrough posts body to path and returns the response unchanged. Redirects are not followed and
// there is no retry. A response body over maxResponseBytes is an error, never a truncated pass-through.
func (client *Client) passthrough(ctx context.Context, label string, path string, body []byte) (Passthrough, error) {
	if !client.Configured() {
		return Passthrough{}, ErrNotConfigured
	}
	request, requestErr := client.newJSONRequest(ctx, path, body)
	if requestErr != nil {
		return Passthrough{}, fmt.Errorf("%s: build request failed", label)
	}
	response, doErr := client.httpClient.Do(request)
	if doErr != nil {
		// The *url.Error carries only the method and URL (no body or headers), so it is safe to wrap.
		return Passthrough{}, fmt.Errorf("%s: request failed: %w", label, doErr)
	}
	defer func() { _ = response.Body.Close() }()

	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil {
		return Passthrough{}, fmt.Errorf("%s: read response failed", label)
	}
	if len(responseBody) > maxResponseBytes {
		return Passthrough{}, fmt.Errorf("%s: response body exceeds %d bytes", label, maxResponseBytes)
	}
	return Passthrough{
		Status:      response.StatusCode,
		Body:        responseBody,
		ContentType: response.Header.Get("Content-Type"),
		RetryAfter:  response.Header.Get("Retry-After"),
	}, nil
}

// Package personsvc is the narrow client delivery-api uses to register an app in
// the person-service (D1) people directory through POST /apps.
//
// It is a leaf package (stdlib only) so both the app-identity approval flow and
// the composition root can depend on it. A 409 "already exists" answer counts as
// success, which makes re-approval harmless; every other non-2xx status, timeout
// or transport failure is an error the caller must stop on.
package personsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	// defaultTimeout matches provisioning-api's personsvc client.
	defaultTimeout = 5 * time.Second

	appsPath = "/apps"

	accessClientIDHeader     = "CF-Access-Client-Id"
	accessClientSecretHeader = "CF-Access-Client-Secret"

	// maxResponseBytes caps how much of a response is drained.
	maxResponseBytes = 64 << 10
)

// ErrNotConfigured means no person-service origin is configured.
var ErrNotConfigured = errors.New("person-service origin is not configured")

// Config configures a Client.
type Config struct {
	// BaseURL is the person-service origin. A trailing slash is tolerated. Empty
	// means the client is not usable.
	BaseURL string
	// AccessClientID and AccessClientSecret are the Cloudflare Access service
	// token headers, each sent only when non-empty. The secret is never logged.
	AccessClientID     string
	AccessClientSecret string
	// HTTPClient is optional; when nil a client with Timeout and no redirect
	// following is built. A supplied client is used as is: the caller owns its
	// redirect policy and timeout (a followed redirect would re-POST the body).
	HTTPClient *http.Client
	// Timeout bounds each request. Optional; defaults to 5 seconds.
	Timeout time.Duration
	// Logger receives one start and one end line per CreateApp. Nil uses slog.Default.
	Logger *slog.Logger
}

// Client registers apps in person-service.
type Client struct {
	baseURL      string
	accessID     string
	accessSecret string
	httpClient   *http.Client
	logger       *slog.Logger
}

// NewClient builds a Client from config.
func NewClient(config Config) *Client {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: timeout,
			// A redirect is never followed: it would re-POST the body elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		baseURL:      strings.TrimSuffix(config.BaseURL, "/"),
		accessID:     config.AccessClientID,
		accessSecret: config.AccessClientSecret,
		httpClient:   httpClient,
		logger:       logger,
	}
}

// Configured reports whether a person-service origin is configured.
func (client *Client) Configured() bool {
	return client.baseURL != ""
}

type createAppRequest struct {
	AppID       string `json:"app_id"`
	DisplayName string `json:"display_name"`
}

// CreateApp registers appID in person-service. 2xx and 409 (already exists)
// return nil. Any other status returns an error naming only the status code,
// never the response body.
func (client *Client) CreateApp(ctx context.Context, appID string, displayName string) error {
	if !client.Configured() {
		return ErrNotConfigured
	}
	started := time.Now()
	client.logger.InfoContext(ctx, "personsvc create app start", "app_id", appID)

	status, outcome, callErr := client.post(ctx, appID, displayName)

	attrs := []any{"app_id", appID, "outcome", outcome, "duration", time.Since(started).String()}
	if status != 0 {
		attrs = append(attrs, "status", status)
	}
	if callErr != nil {
		client.logger.ErrorContext(ctx, "personsvc create app end", append(attrs, "error", callErr.Error())...)
		return callErr
	}
	client.logger.InfoContext(ctx, "personsvc create app end", attrs...)
	return nil
}

// post performs the request and returns the received status (0 when none), an
// outcome label and an error.
func (client *Client) post(ctx context.Context, appID string, displayName string) (int, string, error) {
	body, marshalErr := json.Marshal(createAppRequest{AppID: appID, DisplayName: displayName})
	if marshalErr != nil {
		return 0, "failed", fmt.Errorf("person-service create app: encode request: %w", marshalErr)
	}
	request, requestErr := http.NewRequestWithContext(
		ctx, http.MethodPost, client.baseURL+appsPath, bytes.NewReader(body))
	if requestErr != nil {
		return 0, "failed", fmt.Errorf("person-service create app: build request: %w", requestErr)
	}
	request.Header.Set("Content-Type", "application/json")
	if client.accessID != "" {
		request.Header.Set(accessClientIDHeader, client.accessID)
	}
	if client.accessSecret != "" {
		request.Header.Set(accessClientSecretHeader, client.accessSecret)
	}

	response, doErr := client.httpClient.Do(request)
	if doErr != nil {
		return 0, "failed", fmt.Errorf("person-service create app: request failed: %w", doErr)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))

	switch {
	case response.StatusCode == http.StatusConflict:
		return response.StatusCode, "exists", nil
	case response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices:
		return response.StatusCode, "created", nil
	default:
		return response.StatusCode, "failed",
			fmt.Errorf("person-service create app: unexpected status %d", response.StatusCode)
	}
}

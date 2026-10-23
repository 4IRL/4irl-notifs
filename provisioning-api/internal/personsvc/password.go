package personsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// defaultRetryAfterSeconds is used when the Worker's 429 body has a missing or
// invalid retry_after_seconds.
const defaultRetryAfterSeconds = 60

// maxResponseBodyBytes bounds how much of a success body is decoded.
const maxResponseBodyBytes = 64 * 1024

// ClaimMode selects how ClaimPassword treats a missing row and an existing
// stamp (see the person-service password-claim route).
type ClaimMode string

const (
	// ClaimIfUnset creates the row if missing and stamps only a NULL stamp.
	ClaimIfUnset ClaimMode = "if_unset"
	// ClaimAlways creates the row if missing and always overwrites the stamp.
	ClaimAlways ClaimMode = "always"
	// ClaimUpdateOnly never creates a row; it overwrites the stamp of an
	// existing row.
	ClaimUpdateOnly ClaimMode = "update_only"
)

// PasswordClaim is the result of ClaimPassword. An empty string means NULL.
type PasswordClaim struct {
	Claimed       bool
	RowExists     bool
	SetAt         string
	PreviousSetAt string
}

// ErrResetLimitReached is the sentinel a *ResetLimitError satisfies via
// errors.Is.
var ErrResetLimitReached = errors.New("person-service: reset limit reached")

// ResetLimitError is returned by ClaimResetSlot for a Worker 429.
type ResetLimitError struct {
	RetryAfterSeconds int
}

func (limitErr *ResetLimitError) Error() string {
	return fmt.Sprintf("person-service: reset limit reached (retry after %ds)", limitErr.RetryAfterSeconds)
}

// Is makes errors.Is(err, ErrResetLimitReached) hold.
func (limitErr *ResetLimitError) Is(target error) bool {
	return target == ErrResetLimitReached
}

type claimRequestBody struct {
	Email string    `json:"email"`
	Mode  ClaimMode `json:"mode"`
	Stamp string    `json:"password_set_at,omitempty"`
}

// ClaimStampLayout is the layout of a claim stamp: JavaScript's
// Date.toISOString() form, which the Worker requires exactly.
const ClaimStampLayout = "2006-01-02T15:04:05.000Z"

// NewClaimStamp formats a time as a claim stamp (UTC, millisecond precision).
func NewClaimStamp(moment time.Time) string {
	return moment.UTC().Format(ClaimStampLayout)
}

type claimResponseBody struct {
	Claimed       bool    `json:"claimed"`
	RowExists     bool    `json:"row_exists"`
	SetAt         *string `json:"password_set_at"`
	PreviousSetAt *string `json:"previous_set_at"`
}

type releaseClaimRequestBody struct {
	PasswordSetAt string  `json:"password_set_at"`
	RestoreTo     *string `json:"restore_to"`
}

type resetSlotResponseBody struct {
	ResetID string `json:"reset_id"`
}

type resetLimitResponseBody struct {
	RetryAfterSeconds int `json:"retry_after_seconds"`
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ClaimPassword calls POST /person/{hash}/password-claim; the Worker stamps
// with its own clock.
func (client *Client) ClaimPassword(ctx context.Context, personHash string, email string, mode ClaimMode) (PasswordClaim, error) {
	return client.ClaimPasswordWithStamp(ctx, personHash, email, mode, "")
}

// ClaimPasswordWithStamp is ClaimPassword with a client-generated stamp (a
// canonical UTC ISO timestamp, see NewClaimStamp) that the Worker stores
// instead of its own clock, so the caller can release the claim by that stamp
// even if the response is lost. An empty stamp means "Worker clock".
func (client *Client) ClaimPasswordWithStamp(ctx context.Context, personHash string, email string, mode ClaimMode, stamp string) (PasswordClaim, error) {
	var response claimResponseBody
	status, body, sendErr := client.sendJSON(ctx, http.MethodPost, personPath+"/"+url.PathEscape(personHash)+"/password-claim", claimRequestBody{Email: email, Mode: mode, Stamp: stamp})
	if sendErr != nil {
		return PasswordClaim{}, sendErr
	}
	if statusErr := statusError(status, body); statusErr != nil {
		return PasswordClaim{}, statusErr
	}
	if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
		return PasswordClaim{}, fmt.Errorf("person-service: decode claim response: %w", decodeErr)
	}
	return PasswordClaim{
		Claimed:       response.Claimed,
		RowExists:     response.RowExists,
		SetAt:         stringOrEmpty(response.SetAt),
		PreviousSetAt: stringOrEmpty(response.PreviousSetAt),
	}, nil
}

// ReleasePasswordClaim calls DELETE /person/{hash}/password-claim, restoring
// restoreTo (empty = NULL) only if the stamp still equals claim.SetAt.
func (client *Client) ReleasePasswordClaim(ctx context.Context, personHash string, claim PasswordClaim, restoreTo string) error {
	requestBody := releaseClaimRequestBody{PasswordSetAt: claim.SetAt}
	if restoreTo != "" {
		requestBody.RestoreTo = &restoreTo
	}
	return client.sendExpectOK(ctx, http.MethodDelete, personPath+"/"+url.PathEscape(personHash)+"/password-claim", requestBody)
}

// ClaimResetSlot calls POST /person/{hash}/password-resets and returns the
// reset id. A Worker 429 returns a *ResetLimitError.
func (client *Client) ClaimResetSlot(ctx context.Context, personHash string) (string, error) {
	status, body, sendErr := client.sendJSON(ctx, http.MethodPost, personPath+"/"+url.PathEscape(personHash)+"/password-resets", nil)
	if sendErr != nil {
		return "", sendErr
	}
	if status == http.StatusTooManyRequests {
		retryAfter := defaultRetryAfterSeconds
		var limitBody resetLimitResponseBody
		if decodeErr := json.Unmarshal(body, &limitBody); decodeErr == nil && limitBody.RetryAfterSeconds > 0 {
			retryAfter = limitBody.RetryAfterSeconds
		}
		return "", &ResetLimitError{RetryAfterSeconds: retryAfter}
	}
	if statusErr := statusError(status, body); statusErr != nil {
		return "", statusErr
	}
	var response resetSlotResponseBody
	if decodeErr := json.Unmarshal(body, &response); decodeErr != nil {
		return "", fmt.Errorf("person-service: decode reset slot response: %w", decodeErr)
	}
	if response.ResetID == "" {
		return "", errors.New("person-service: reset slot response has no reset_id")
	}
	return response.ResetID, nil
}

// ReleaseResetSlot calls DELETE /person/{hash}/password-resets/{reset_id}.
func (client *Client) ReleaseResetSlot(ctx context.Context, personHash string, resetID string) error {
	return client.sendExpectOK(ctx, http.MethodDelete, personPath+"/"+url.PathEscape(personHash)+"/password-resets/"+url.PathEscape(resetID), nil)
}

// sendExpectOK sends a request and treats any 2xx as success.
func (client *Client) sendExpectOK(ctx context.Context, method string, path string, payload any) error {
	status, body, sendErr := client.sendJSON(ctx, method, path, payload)
	if sendErr != nil {
		return sendErr
	}
	return statusError(status, body)
}

// sendJSON sends payload (nil = no body) and returns the status and a bounded
// response body. Transport failures return an error.
func (client *Client) sendJSON(ctx context.Context, method string, path string, payload any) (int, []byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return 0, nil, fmt.Errorf("person-service: encode request body: %w", marshalErr)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	request, requestErr := http.NewRequestWithContext(ctx, method, client.baseURL+path, bodyReader)
	if requestErr != nil {
		return 0, nil, fmt.Errorf("person-service: build request: %w", requestErr)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client.setAccessHeaders(request)

	response, doErr := client.httpClient.Do(request)
	if doErr != nil {
		return 0, nil, fmt.Errorf("person-service: request failed: %w", doErr)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes))
	if readErr != nil {
		return 0, nil, fmt.Errorf("person-service: read response: %w", readErr)
	}
	return response.StatusCode, body, nil
}

// statusError returns an error for a non-2xx status (with a bounded snippet).
func statusError(status int, body []byte) error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	snippet := body
	if len(snippet) > maxErrorBodySnippetLength {
		snippet = snippet[:maxErrorBodySnippetLength]
	}
	return &StatusError{Status: status, Snippet: string(bytes.TrimSpace(snippet))}
}

// StatusError is a non-2xx response from the Worker: the request was received
// and answered.
type StatusError struct {
	Status  int
	Snippet string
}

func (statusErr *StatusError) Error() string {
	return fmt.Sprintf("person-service: unexpected status %d %s: %s", statusErr.Status, http.StatusText(statusErr.Status), statusErr.Snippet)
}

// IsAmbiguousFailure reports whether err leaves it unknown whether the Worker
// committed the request: a transport error, timeout, truncated or undecodable
// response, or a 5xx (e.g. a gateway timeout after the commit). A 4xx is a
// definite rejection and nil is not a failure. Fails closed to "ambiguous"
// for any error that is not a recognised definite rejection.
func IsAmbiguousFailure(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status >= http.StatusInternalServerError
	}
	return true
}

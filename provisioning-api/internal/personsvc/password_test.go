package personsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capturedRequest struct {
	method          string
	path            string
	body            string
	contentType     string
	accessClientID  string
	accessSecretHdr string
}

func newCapturingServer(t *testing.T, status int, responseBody string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		bodyBytes, _ := io.ReadAll(request.Body)
		captured.method = request.Method
		captured.path = request.URL.Path
		captured.body = string(bodyBytes)
		captured.contentType = request.Header.Get("Content-Type")
		captured.accessClientID = request.Header.Get("CF-Access-Client-Id")
		captured.accessSecretHdr = request.Header.Get("CF-Access-Client-Secret")
		responseWriter.WriteHeader(status)
		_, _ = responseWriter.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func newAccessClient(server *httptest.Server) *Client {
	return NewClient(Config{BaseURL: server.URL, AccessClientID: "id", AccessClientSecret: "secret"})
}

func TestClaimPasswordSendsRequestForEveryModeAndDecodesResponse(t *testing.T) {
	for _, mode := range []ClaimMode{ClaimIfUnset, ClaimAlways, ClaimUpdateOnly} {
		t.Run(string(mode), func(t *testing.T) {
			server, captured := newCapturingServer(t, http.StatusOK,
				`{"claimed":true,"row_exists":true,"password_set_at":"2026-10-22T10:00:00.000Z","previous_set_at":null}`)
			claim, err := newAccessClient(server).ClaimPassword(context.Background(), "76gzqgp4byjl6dje", "alice@example.com", mode)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.method != http.MethodPost || captured.path != "/person/76gzqgp4byjl6dje/password-claim" {
				t.Fatalf("request = %s %s", captured.method, captured.path)
			}
			if captured.contentType != "application/json" || captured.accessClientID != "id" || captured.accessSecretHdr != "secret" {
				t.Fatalf("headers wrong: %+v", captured)
			}
			var body map[string]string
			if decodeErr := json.Unmarshal([]byte(captured.body), &body); decodeErr != nil {
				t.Fatalf("body not JSON: %v", decodeErr)
			}
			if body["email"] != "alice@example.com" || body["mode"] != string(mode) || len(body) != 2 {
				t.Fatalf("body = %v", body)
			}
			expected := PasswordClaim{Claimed: true, RowExists: true, SetAt: "2026-10-22T10:00:00.000Z"}
			if claim != expected {
				t.Fatalf("claim = %+v, expected %+v", claim, expected)
			}
		})
	}
}

func TestClaimPasswordWithStampSendsStampAndPlainClaimOmitsIt(t *testing.T) {
	server, captured := newCapturingServer(t, http.StatusOK,
		`{"claimed":true,"row_exists":true,"password_set_at":"2026-10-22T10:00:00.123Z","previous_set_at":null}`)
	client := newAccessClient(server)

	if _, err := client.ClaimPasswordWithStamp(context.Background(), "76gzqgp4byjl6dje", "a@b.co", ClaimIfUnset, "2026-10-22T10:00:00.123Z"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var body map[string]string
	if decodeErr := json.Unmarshal([]byte(captured.body), &body); decodeErr != nil {
		t.Fatalf("body not JSON: %v", decodeErr)
	}
	if body["password_set_at"] != "2026-10-22T10:00:00.123Z" || body["mode"] != "if_unset" || len(body) != 3 {
		t.Fatalf("body = %v", body)
	}

	if _, err := client.ClaimPassword(context.Background(), "76gzqgp4byjl6dje", "a@b.co", ClaimIfUnset); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(captured.body, "password_set_at") {
		t.Fatalf("a plain claim must not send a stamp, body = %s", captured.body)
	}
}

func TestNewClaimStampIsCanonicalISOWithMilliseconds(t *testing.T) {
	moment := time.Date(2026, 10, 22, 3, 4, 5, 6_789_000, time.FixedZone("PDT", -7*3600))
	if got := NewClaimStamp(moment); got != "2026-10-22T10:04:05.006Z" {
		t.Fatalf("stamp = %q", got)
	}
}

func TestIsAmbiguousFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"transport error", errors.New("person-service: request failed: EOF"), true},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{"500", &StatusError{Status: 500}, true},
		{"504", fmt.Errorf("wrapped: %w", &StatusError{Status: 504}), true},
		{"400", &StatusError{Status: 400}, false},
		{"403", &StatusError{Status: 403}, false},
	}
	for _, testCase := range cases {
		if got := IsAmbiguousFailure(testCase.err); got != testCase.want {
			t.Errorf("%s: IsAmbiguousFailure = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

func TestClaimPasswordDecodesPreviousStampAndNulls(t *testing.T) {
	server, _ := newCapturingServer(t, http.StatusOK,
		`{"claimed":false,"row_exists":false,"password_set_at":null,"previous_set_at":"old"}`)
	claim, err := NewClient(Config{BaseURL: server.URL}).ClaimPassword(context.Background(), "76gzqgp4byjl6dje", "a@b.co", ClaimUpdateOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claim != (PasswordClaim{PreviousSetAt: "old"}) {
		t.Fatalf("claim = %+v", claim)
	}
}

func TestClaimPasswordErrors(t *testing.T) {
	server, _ := newCapturingServer(t, http.StatusInternalServerError, `boom`)
	if _, err := NewClient(Config{BaseURL: server.URL}).ClaimPassword(context.Background(), "h", "e", ClaimAlways); err == nil {
		t.Fatal("expected error for 500")
	}
	badJSON, _ := newCapturingServer(t, http.StatusOK, `not json`)
	if _, err := NewClient(Config{BaseURL: badJSON.URL}).ClaimPassword(context.Background(), "h", "e", ClaimAlways); err == nil {
		t.Fatal("expected error for undecodable body")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if _, err := NewClient(Config{BaseURL: closed.URL}).ClaimPassword(context.Background(), "h", "e", ClaimAlways); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestReleasePasswordClaimBody(t *testing.T) {
	testCases := []struct {
		name      string
		restoreTo string
		expected  string
	}{
		{name: "restore to stamp", restoreTo: "prev", expected: `{"password_set_at":"mine","restore_to":"prev"}`},
		{name: "restore to null", restoreTo: "", expected: `{"password_set_at":"mine","restore_to":null}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server, captured := newCapturingServer(t, http.StatusOK, `{"released":true}`)
			err := newAccessClient(server).ReleasePasswordClaim(context.Background(), "76gzqgp4byjl6dje", PasswordClaim{SetAt: "mine"}, testCase.restoreTo)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.method != http.MethodDelete || captured.path != "/person/76gzqgp4byjl6dje/password-claim" {
				t.Fatalf("request = %s %s", captured.method, captured.path)
			}
			if captured.body != testCase.expected {
				t.Fatalf("body = %s, expected %s", captured.body, testCase.expected)
			}
			if captured.accessClientID != "id" || captured.accessSecretHdr != "secret" {
				t.Fatal("access headers missing")
			}
		})
	}
}

func TestReleasePasswordClaimErrorOn500(t *testing.T) {
	server, _ := newCapturingServer(t, http.StatusInternalServerError, `boom`)
	if err := NewClient(Config{BaseURL: server.URL}).ReleasePasswordClaim(context.Background(), "h", PasswordClaim{SetAt: "x"}, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestClaimResetSlotSuccess(t *testing.T) {
	server, captured := newCapturingServer(t, http.StatusOK, `{"reset_id":"abc-123"}`)
	resetID, err := newAccessClient(server).ClaimResetSlot(context.Background(), "76gzqgp4byjl6dje")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resetID != "abc-123" {
		t.Fatalf("resetID = %q", resetID)
	}
	if captured.method != http.MethodPost || captured.path != "/person/76gzqgp4byjl6dje/password-resets" || captured.body != "" {
		t.Fatalf("request = %+v", captured)
	}
	if captured.accessClientID != "id" || captured.accessSecretHdr != "secret" {
		t.Fatal("access headers missing")
	}
}

func TestClaimResetSlot429MapsToLimitError(t *testing.T) {
	testCases := []struct {
		name     string
		body     string
		expected int
	}{
		{name: "seconds from body", body: `{"error":"reset limit reached","retry_after_seconds":1234}`, expected: 1234},
		{name: "missing", body: `{"error":"reset limit reached"}`, expected: 60},
		{name: "invalid", body: `{"retry_after_seconds":"soon"}`, expected: 60},
		{name: "non-positive", body: `{"retry_after_seconds":0}`, expected: 60},
		{name: "not json", body: `slow down`, expected: 60},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server, _ := newCapturingServer(t, http.StatusTooManyRequests, testCase.body)
			_, err := NewClient(Config{BaseURL: server.URL}).ClaimResetSlot(context.Background(), "76gzqgp4byjl6dje")
			assertLimitError(t, err, testCase.expected)
			assertLimitError(t, fmt.Errorf("wrapped: %w", err), testCase.expected)
		})
	}
}

func assertLimitError(t *testing.T, err error, expectedSeconds int) {
	t.Helper()
	if !errors.Is(err, ErrResetLimitReached) {
		t.Fatalf("errors.Is(ErrResetLimitReached) false for %v", err)
	}
	var limitErr *ResetLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("errors.As(*ResetLimitError) false for %v", err)
	}
	if limitErr.RetryAfterSeconds != expectedSeconds {
		t.Fatalf("RetryAfterSeconds = %d, expected %d", limitErr.RetryAfterSeconds, expectedSeconds)
	}
}

func TestClaimResetSlotOtherFailuresAreNotLimitErrors(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadRequest, http.StatusForbidden} {
		server, _ := newCapturingServer(t, status, `{"retry_after_seconds":5}`)
		_, err := NewClient(Config{BaseURL: server.URL}).ClaimResetSlot(context.Background(), "h")
		if err == nil || errors.Is(err, ErrResetLimitReached) {
			t.Fatalf("status %d: err = %v", status, err)
		}
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	_, err := NewClient(Config{BaseURL: closed.URL}).ClaimResetSlot(context.Background(), "h")
	if err == nil || errors.Is(err, ErrResetLimitReached) {
		t.Fatalf("transport err = %v", err)
	}
	empty, _ := newCapturingServer(t, http.StatusOK, `{}`)
	if _, emptyErr := NewClient(Config{BaseURL: empty.URL}).ClaimResetSlot(context.Background(), "h"); emptyErr == nil {
		t.Fatal("expected error for missing reset_id")
	}
}

func TestReleaseResetSlot(t *testing.T) {
	server, captured := newCapturingServer(t, http.StatusOK, `{"released":true}`)
	if err := newAccessClient(server).ReleaseResetSlot(context.Background(), "76gzqgp4byjl6dje", "abc-123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.method != http.MethodDelete || captured.path != "/person/76gzqgp4byjl6dje/password-resets/abc-123" || captured.body != "" {
		t.Fatalf("request = %+v", captured)
	}
	if captured.accessClientID != "id" || captured.accessSecretHdr != "secret" {
		t.Fatal("access headers missing")
	}

	failing, _ := newCapturingServer(t, http.StatusInternalServerError, `boom`)
	if err := NewClient(Config{BaseURL: failing.URL}).ReleaseResetSlot(context.Background(), "h", "r"); err == nil {
		t.Fatal("expected error for 500")
	}
}

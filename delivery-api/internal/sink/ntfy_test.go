package sink

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	ntfyTestTopic = "ops-alerts"
	ntfyTestToken = "tk_abcdefghijklmnopqrstuvwxyz012"
)

// ntfyDestination builds a resolved ntfy destination. An empty token omits the
// access_token secret entirely.
func ntfyDestination(topic string, token string) registry.ResolvedDestination {
	secrets := map[string]string{}
	if token != "" {
		secrets["access_token"] = token
	}
	return registry.ResolvedDestination{
		ID:      3,
		Name:    "ops-ntfy",
		Type:    registry.TypeNtfy,
		Config:  map[string]string{"topic": topic},
		Secrets: secrets,
	}
}

func ntfyRequest(display *message.Display) Request {
	request := goldenRequest()
	request.Envelope.Display = display
	return request
}

func newTestNtfySink(baseURL string) *NtfySink {
	return NewNtfySink(NtfyConfig{BaseURL: baseURL})
}

func TestNtfySinkImplementsSink(t *testing.T) {
	var _ Sink = (*NtfySink)(nil)
}

func TestNewNtfySinkConfigured(t *testing.T) {
	tests := map[string]bool{
		"":                        false,
		"http://ntfy:80":          true,
		"http://ntfy:80/":         true,
		"https://ntfy.sh.example": true,
	}
	for baseURL, want := range tests {
		if got := NewNtfySink(NtfyConfig{BaseURL: baseURL}).Configured(); got != want {
			t.Errorf("Configured() for %q = %v, want %v", baseURL, got, want)
		}
	}
}

func TestNewNtfySinkDefaultsTimeoutAndHonorsOverride(t *testing.T) {
	if got := NewNtfySink(NtfyConfig{BaseURL: "http://ntfy:80"}).client.Timeout; got != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", got)
	}
	if got := NewNtfySink(NtfyConfig{BaseURL: "http://ntfy:80", Timeout: 2 * time.Second}).client.Timeout; got != 2*time.Second {
		t.Errorf("override timeout = %v, want 2s", got)
	}
}

func TestNtfyDeliverPostsBodyTitleAndTopic(t *testing.T) {
	var gotMethod, gotPath string
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		gotMethod = request.Method
		gotPath = request.URL.Path
		captured.record(request.Header.Clone(), body)
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `{"id":"abc"}`)
	}))
	t.Cleanup(server.Close)
	display := &message.Display{Title: "Backup done", Body: "All 3 worlds saved"}

	outcome := newTestNtfySink(server.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(display),
	)

	requireDelivered(t, outcome)
	if outcome.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", outcome.StatusCode)
	}
	if gotMethod != http.MethodPost || gotPath != "/"+ntfyTestTopic {
		t.Errorf("request = %s %s, want POST /%s", gotMethod, gotPath, ntfyTestTopic)
	}
	if got := string(captured.Body()); got != "All 3 worlds saved" {
		t.Errorf("body = %q, want the display body", got)
	}
	headers := captured.Headers()
	if got := headers.Get("Title"); got != "Backup done" {
		t.Errorf("Title = %q, want %q", got, "Backup done")
	}
	if got := headers.Get("User-Agent"); got != "4irl-delivery/1" {
		t.Errorf("User-Agent = %q, want 4irl-delivery/1", got)
	}
}

func TestNtfyDeliverTitleOnlyUsesTitleAsBodyWithoutTitleHeader(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "{}", nil)

	outcome := newTestNtfySink(server.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Title: "Only a title"}),
	)

	requireDelivered(t, outcome)
	if got := string(captured.Body()); got != "Only a title" {
		t.Errorf("body = %q, want the title", got)
	}
	if _, present := captured.Headers()["Title"]; present {
		t.Errorf("Title header present = %q, want none", captured.Headers().Get("Title"))
	}
}

func TestNtfyDeliverBodyOnlyHasNoTitleHeader(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "{}", nil)

	outcome := newTestNtfySink(server.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Body: "just body"}),
	)

	requireDelivered(t, outcome)
	if got := string(captured.Body()); got != "just body" {
		t.Errorf("body = %q, want just body", got)
	}
	if _, present := captured.Headers()["Title"]; present {
		t.Error("Title header present for a body-only display")
	}
}

func TestNtfyDeliverEncodesNonASCIITitle(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "{}", nil)

	outcome := newTestNtfySink(server.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""),
		ntfyRequest(&message.Display{Title: "Sauvegarde terminée ✓", Body: "ok"}),
	)

	requireDelivered(t, outcome)
	const want = "=?utf-8?q?Sauvegarde_termin=C3=A9e_=E2=9C=93?="
	if got := captured.Headers().Get("Title"); got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
}

func TestNtfyDeliverPassesPriorityAndClickThroughOnlyWhenSet(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "{}", nil)

	outcome := newTestNtfySink(server.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""),
		ntfyRequest(&message.Display{Title: "T", Body: "B", Priority: "urgent", URL: "https://example.test/run/1"}),
	)
	requireDelivered(t, outcome)
	if got := captured.Headers().Get("Priority"); got != "urgent" {
		t.Errorf("Priority = %q, want urgent", got)
	}
	if got := captured.Headers().Get("Click"); got != "https://example.test/run/1" {
		t.Errorf("Click = %q, want the display url", got)
	}

	plainServer, plainCaptured := newCaptureServer(t, http.StatusOK, "{}", nil)
	plainOutcome := newTestNtfySink(plainServer.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Title: "T", Body: "B"}),
	)
	requireDelivered(t, plainOutcome)
	for _, header := range []string{"Priority", "Click"} {
		if _, present := plainCaptured.Headers()[header]; present {
			t.Errorf("%s header present when the display sets none", header)
		}
	}
}

func TestNtfyDeliverSendsBearerTokenOnlyWhenPresent(t *testing.T) {
	withServer, withCaptured := newCaptureServer(t, http.StatusOK, "{}", nil)
	withOutcome := newTestNtfySink(withServer.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ntfyTestToken), ntfyRequest(&message.Display{Title: "T"}),
	)
	requireDelivered(t, withOutcome)
	if got := withCaptured.Headers().Get("Authorization"); got != "Bearer "+ntfyTestToken {
		t.Errorf("Authorization = %q, want a bearer token", got)
	}

	withoutServer, withoutCaptured := newCaptureServer(t, http.StatusOK, "{}", nil)
	withoutOutcome := newTestNtfySink(withoutServer.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Title: "T"}),
	)
	requireDelivered(t, withoutOutcome)
	if _, present := withoutCaptured.Headers()["Authorization"]; present {
		t.Error("Authorization header present without an access_token")
	}
}

func TestNtfyDeliverTrailingSlashBaseURLHasNoDoubleSlash(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		_, _ = io.Copy(io.Discard, request.Body)
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	outcome := newTestNtfySink(server.URL+"/").Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Title: "T"}),
	)

	requireDelivered(t, outcome)
	if gotPath != "/"+ntfyTestTopic {
		t.Errorf("path = %q, want /%s", gotPath, ntfyTestTopic)
	}
}

func TestNtfyDeliverReportsStatusForDispatchToClassify(t *testing.T) {
	for _, status := range []int{
		http.StatusOK, http.StatusAccepted, http.StatusTooManyRequests, http.StatusUnauthorized,
		http.StatusForbidden, http.StatusInternalServerError, http.StatusInsufficientStorage,
	} {
		server, _ := newCaptureServer(t, status, "{}", nil)

		outcome := newTestNtfySink(server.URL).Deliver(
			context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Title: "T"}),
		)

		if outcome.Err != nil || outcome.StatusCode != status {
			t.Errorf("status %d: outcome = %+v, want StatusCode %d and no Err", status, outcome, status)
		}
		if outcome.Gone {
			t.Errorf("status %d: Gone = true, want false (ntfy never proves a destination gone)", status)
		}
	}
}

func TestNtfyDeliverParsesRetryAfterOn429(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusTooManyRequests, "{}", map[string]string{"Retry-After": "7"})

	outcome := newTestNtfySink(server.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Title: "T"}),
	)

	if outcome.RetryAfter == nil || *outcome.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", outcome.RetryAfter)
	}
}

func TestNtfyDeliverRefusesUnbuildableDeliveriesWithoutSending(t *testing.T) {
	tests := map[string]struct {
		destination registry.ResolvedDestination
		display     *message.Display
	}{
		"missing topic":          {ntfyDestination("", ""), &message.Display{Title: "T"}},
		"topic with slash":       {ntfyDestination("a/b", ""), &message.Display{Title: "T"}},
		"topic with space":       {ntfyDestination("a b", ""), &message.Display{Title: "T"}},
		"topic too long":         {ntfyDestination(strings.Repeat("a", 65), ""), &message.Display{Title: "T"}},
		"topic with newline":     {ntfyDestination("abc\n", ""), &message.Display{Title: "T"}},
		"no display":             {ntfyDestination(ntfyTestTopic, ""), nil},
		"empty display":          {ntfyDestination(ntfyTestTopic, ""), &message.Display{}},
		"url only":               {ntfyDestination(ntfyTestTopic, ""), &message.Display{URL: "https://example.test"}},
		"title with CRLF":        {ntfyDestination(ntfyTestTopic, ""), &message.Display{Title: "a\r\nX-Evil: 1", Body: "B"}},
		"title with LF":          {ntfyDestination(ntfyTestTopic, ""), &message.Display{Title: "a\nb", Body: "B"}},
		"priority with CRLF":     {ntfyDestination(ntfyTestTopic, ""), &message.Display{Title: "T", Priority: "high\r\nX: 1"}},
		"click with CRLF":        {ntfyDestination(ntfyTestTopic, ""), &message.Display{Title: "T", URL: "https://x\r\nX: 1"}},
		"access token with LF":   {ntfyDestination(ntfyTestTopic, "tk_abc\nX: 1"), &message.Display{Title: "T"}},
		"access token with CRLF": {ntfyDestination(ntfyTestTopic, "tk_abc\r\nX: 1"), &message.Display{Title: "T"}},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			server, captured := newCaptureServer(t, http.StatusOK, "{}", nil)

			outcome := newTestNtfySink(server.URL).Deliver(context.Background(), testCase.destination, ntfyRequest(testCase.display))

			if !errors.Is(outcome.Err, ErrInvalidDelivery) {
				t.Errorf("Err = %v, want ErrInvalidDelivery", outcome.Err)
			}
			if outcome.StatusCode != 0 {
				t.Errorf("StatusCode = %d, want 0", outcome.StatusCode)
			}
			if captured.Received() {
				t.Error("a request was sent for an unbuildable delivery")
			}
		})
	}
}

func TestNtfyDeliverErrorsNeverContainTheToken(t *testing.T) {
	tests := map[string]registry.ResolvedDestination{
		"bad topic":  ntfyDestination("bad topic", ntfyTestToken),
		"bad header": ntfyDestination(ntfyTestTopic, ntfyTestToken+"\nX: 1"),
	}
	for name, destination := range tests {
		outcome := newTestNtfySink("http://ntfy.invalid").Deliver(
			context.Background(), destination, ntfyRequest(&message.Display{Title: "T"}),
		)
		if outcome.Err == nil {
			t.Fatalf("%s: Err = nil, want an error", name)
		}
		if strings.Contains(outcome.Err.Error(), ntfyTestToken) {
			t.Errorf("%s: Err %q contains the access token", name, outcome.Err)
		}
	}
}

func TestNtfyDeliverRedactsTheTokenFromTheResponseSnippet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":"unauthorized","echo":"`+request.Header.Get("Authorization")+`","token":"`+ntfyTestToken+`"}`)
	}))
	t.Cleanup(server.Close)

	outcome := newTestNtfySink(server.URL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ntfyTestToken), ntfyRequest(&message.Display{Title: "T"}),
	)

	if outcome.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %d, want 401", outcome.StatusCode)
	}
	if strings.Contains(outcome.ResponseSnippet, ntfyTestToken) {
		t.Errorf("snippet %q contains the access token", outcome.ResponseSnippet)
	}
	if !strings.Contains(outcome.ResponseSnippet, redactedMarker) {
		t.Errorf("snippet %q has no redaction marker", outcome.ResponseSnippet)
	}
}

func TestNtfyTransportErrorIsRetryableAndLeaksNoTokenOrAddress(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusOK, "{}", nil)
	baseURL := server.URL
	server.Close()

	outcome := newTestNtfySink(baseURL).Deliver(
		context.Background(), ntfyDestination(ntfyTestTopic, ntfyTestToken), ntfyRequest(&message.Display{Title: "T"}),
	)

	if outcome.Err == nil {
		t.Fatal("Err = nil, want a transport error")
	}
	if errors.Is(outcome.Err, ErrInvalidDelivery) {
		t.Error("a transport failure must stay retryable, not ErrInvalidDelivery")
	}
	if outcome.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0", outcome.StatusCode)
	}
	text := outcome.Err.Error()
	if strings.Contains(text, ntfyTestToken) {
		t.Errorf("Err %q contains the access token", text)
	}
	if strings.Contains(text, strings.TrimPrefix(baseURL, "http://")) {
		t.Errorf("Err %q contains the ntfy address", text)
	}
}

func TestNtfyDeliverHonorsContextCancellation(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusOK, "{}", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome := newTestNtfySink(server.URL).Deliver(
		ctx, ntfyDestination(ntfyTestTopic, ""), ntfyRequest(&message.Display{Title: "T"}),
	)

	if outcome.Err == nil {
		t.Fatal("Err = nil, want a cancellation error")
	}
	if errors.Is(outcome.Err, ErrInvalidDelivery) {
		t.Error("cancellation must stay retryable, not ErrInvalidDelivery")
	}
}

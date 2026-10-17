package sink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	discordTokenSegment = "tOkEn-s3cr3t_segment"
	discordPath         = "/api/webhooks/123456789/" + discordTokenSegment
)

// discordDestination builds a resolved Discord destination posting to webhookURL.
// The sink does not validate the host (registry validation does), so tests point
// it at a plain httptest server.
func discordDestination(webhookURL string) registry.ResolvedDestination {
	return registry.ResolvedDestination{
		ID:      2,
		Name:    "ops",
		Type:    registry.TypeDiscord,
		Config:  map[string]string{},
		Secrets: map[string]string{"webhook_url": webhookURL},
	}
}

func discordRequest(display *message.Display) Request {
	request := goldenRequest()
	request.Envelope.Display = display
	return request
}

func newTestDiscordSink() *DiscordSink { return NewDiscordSink(http.DefaultClient) }

// decodeDiscordContent returns the "content" field of a posted Discord body.
func decodeDiscordContent(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		Content string `json:"content"`
	}
	if unmarshalErr := json.Unmarshal(body, &decoded); unmarshalErr != nil {
		t.Fatalf("body %q is not JSON: %v", body, unmarshalErr)
	}
	return decoded.Content
}

// requireReceivedContent fails unless exactly one request reached the server and
// returns the "content" it carried.
func requireReceivedContent(t *testing.T, captured *capture) string {
	t.Helper()
	if !captured.Received() {
		t.Fatal("no request reached the server")
	}
	return decodeDiscordContent(t, captured.Body())
}

func TestNewDiscordSinkKeepsClient(t *testing.T) {
	if NewDiscordSink(http.DefaultClient).client != http.DefaultClient {
		t.Error("NewDiscordSink did not keep the client it was given")
	}
}

func TestNewDiscordSinkPanicsWithoutClient(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewDiscordSink(nil) did not panic")
		}
	}()

	NewDiscordSink(nil)
}

func TestDiscordDeliverSendsContentWithMentionsSuppressed(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "{}", nil)
	display := &message.Display{Title: "T", Body: "B", URL: "https://x"}

	outcome := newTestDiscordSink().Deliver(
		context.Background(), discordDestination(server.URL+discordPath+"?thread_id=77"), discordRequest(display),
	)

	requireDelivered(t, outcome)
	const wantBody = `{"content":"**T**\nB\nhttps://x","allowed_mentions":{"parse":[]}}`
	if got := string(captured.Body()); got != wantBody {
		t.Errorf("body = %s, want %s", got, wantBody)
	}
	headers := captured.Headers()
	if got := headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := headers.Get("User-Agent"); got != "4irl-delivery/1" {
		t.Errorf("User-Agent = %q, want 4irl-delivery/1", got)
	}
}

func TestDiscordDeliverAddsWaitTrueAndKeepsExistingQuery(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotQuery = request.URL.Query()
		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	for name, webhookURL := range map[string]string{
		"no query":       server.URL + discordPath,
		"existing query": server.URL + discordPath + "?thread_id=77",
	} {
		outcome := newTestDiscordSink().Deliver(
			context.Background(), discordDestination(webhookURL), discordRequest(&message.Display{Title: "T"}),
		)

		requireDelivered(t, outcome)
		if gotPath != discordPath {
			t.Errorf("%s: path = %q, want %q", name, gotPath, discordPath)
		}
		if gotQuery.Get("wait") != "true" {
			t.Errorf("%s: query = %v, want wait=true", name, gotQuery)
		}
		if strings.Contains(webhookURL, "thread_id") && gotQuery.Get("thread_id") != "77" {
			t.Errorf("%s: query = %v lost thread_id=77", name, gotQuery)
		}
	}
}

func TestDiscordDeliverJoinsTheNonEmptyDisplayParts(t *testing.T) {
	testCases := []struct {
		name    string
		display message.Display
		want    string
	}{
		{"title only", message.Display{Title: "T"}, "**T**"},
		{"body only", message.Display{Body: "B"}, "B"},
		{"title and body", message.Display{Title: "T", Body: "B"}, "**T**\nB"},
		{"body and url", message.Display{Body: "B", URL: "https://x"}, "B\nhttps://x"},
		{"title and url", message.Display{Title: "T", URL: "https://x"}, "**T**\nhttps://x"},
		{"priority is not rendered", message.Display{Title: "T", Priority: "urgent"}, "**T**"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			server, captured := newCaptureServer(subTest, http.StatusOK, "{}", nil)
			display := testCase.display

			outcome := newTestDiscordSink().Deliver(
				context.Background(), discordDestination(server.URL+discordPath), discordRequest(&display),
			)

			requireDelivered(subTest, outcome)
			if got := requireReceivedContent(subTest, captured); got != testCase.want {
				subTest.Errorf("content = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestDiscordDeliverTruncatesToTwoThousandRunes(t *testing.T) {
	testCases := []struct {
		name string
		unit string
	}{
		{"ascii", "x"},
		{"two byte runes", "é"},
		{"four byte runes", "😀"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			server, captured := newCaptureServer(subTest, http.StatusOK, "{}", nil)
			display := &message.Display{Body: strings.Repeat(testCase.unit, 3000)}

			outcome := newTestDiscordSink().Deliver(
				context.Background(), discordDestination(server.URL+discordPath), discordRequest(display),
			)

			requireDelivered(subTest, outcome)
			content := requireReceivedContent(subTest, captured)
			if !utf8.ValidString(content) {
				subTest.Fatalf("content is not valid UTF-8, a multi-byte rune was split")
			}
			if got := utf8.RuneCountInString(content); got != 2000 {
				subTest.Errorf("rune count = %d, want 2000", got)
			}
			if want := strings.Repeat(testCase.unit, 1999) + "…"; content != want {
				subTest.Errorf("content does not end in the truncation marker after 1999 runes")
			}
		})
	}
}

func TestDiscordDeliverKeepsContentOfExactlyTwoThousandRunes(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "{}", nil)
	body := strings.Repeat("é", 2000)

	outcome := newTestDiscordSink().Deliver(
		context.Background(), discordDestination(server.URL+discordPath), discordRequest(&message.Display{Body: body}),
	)

	requireDelivered(t, outcome)
	if got := requireReceivedContent(t, captured); got != body {
		t.Errorf("a 2000 rune content was altered (rune count %d)", utf8.RuneCountInString(got))
	}
}

func TestDiscordDeliverReportsStatusOnSuccess(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusOK, `{"id":"1"}`, nil)

	outcome := newTestDiscordSink().Deliver(
		context.Background(), discordDestination(server.URL+discordPath), discordRequest(&message.Display{Title: "T"}),
	)

	requireDelivered(t, outcome)
	if outcome.StatusCode != http.StatusOK || outcome.Gone || outcome.RetryAfter != nil {
		t.Errorf("outcome = %+v, want a plain 200", outcome)
	}
}

func TestDiscordDeliverParsesRetryAfter(t *testing.T) {
	testCases := []struct {
		name    string
		body    string
		headers map[string]string
		want    *time.Duration
	}{
		{"float seconds in the body", `{"message":"rate limited","retry_after":1.5}`, nil, durationPointer(1500 * time.Millisecond)},
		{"body wins over header", `{"retry_after":2}`, map[string]string{"Retry-After": "9"}, durationPointer(2 * time.Second)},
		{"header when there is no body", "", map[string]string{"Retry-After": "3"}, durationPointer(3 * time.Second)},
		{"header when the body is not json", "<html>slow down</html>", map[string]string{"Retry-After": "4"}, durationPointer(4 * time.Second)},
		{"header when the body has no retry_after", `{"message":"slow down"}`, map[string]string{"Retry-After": "5"}, durationPointer(5 * time.Second)},
		{"neither", "", nil, nil},
		{"negative body value clamps to zero", `{"retry_after":-4}`, nil, durationPointer(0)},
		{"huge body value is capped", `{"retry_after":1e12}`, nil, durationPointer(maxRetryAfter)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			server, _ := newCaptureServer(subTest, http.StatusTooManyRequests, testCase.body, testCase.headers)

			outcome := newTestDiscordSink().Deliver(
				context.Background(), discordDestination(server.URL+discordPath), discordRequest(&message.Display{Title: "T"}),
			)

			requireDelivered(subTest, outcome)
			if outcome.StatusCode != http.StatusTooManyRequests || outcome.Gone {
				subTest.Fatalf("outcome = %+v, want a plain 429", outcome)
			}
			switch {
			case testCase.want == nil && outcome.RetryAfter != nil:
				subTest.Errorf("RetryAfter = %v, want nil", *outcome.RetryAfter)
			case testCase.want != nil && outcome.RetryAfter == nil:
				subTest.Errorf("RetryAfter = nil, want %v", *testCase.want)
			case testCase.want != nil && *outcome.RetryAfter != *testCase.want:
				subTest.Errorf("RetryAfter = %v, want %v", *outcome.RetryAfter, *testCase.want)
			}
		})
	}
}

func TestDiscordDeliverIgnoresRetryAfterBodyOnNon429(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusBadRequest, `{"retry_after":5}`, nil)

	outcome := newTestDiscordSink().Deliver(
		context.Background(), discordDestination(server.URL+discordPath), discordRequest(&message.Display{Title: "T"}),
	)

	requireDelivered(t, outcome)
	if outcome.RetryAfter != nil {
		t.Errorf("RetryAfter = %v on a 400, want nil", *outcome.RetryAfter)
	}
}

func TestDiscordDeliverGoneOnlyFor404And401(t *testing.T) {
	testCases := []struct {
		status   int
		wantGone bool
	}{
		{http.StatusNotFound, true},
		{http.StatusUnauthorized, true},
		{http.StatusBadRequest, false},
		{http.StatusForbidden, false},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
		{http.StatusOK, false},
	}
	for _, testCase := range testCases {
		t.Run(http.StatusText(testCase.status), func(subTest *testing.T) {
			server, _ := newCaptureServer(subTest, testCase.status, "{}", nil)

			outcome := newTestDiscordSink().Deliver(
				context.Background(), discordDestination(server.URL+discordPath), discordRequest(&message.Display{Title: "T"}),
			)

			requireDelivered(subTest, outcome)
			if outcome.StatusCode != testCase.status {
				subTest.Errorf("status = %d, want %d", outcome.StatusCode, testCase.status)
			}
			if outcome.Gone != testCase.wantGone {
				subTest.Errorf("Gone = %v, want %v", outcome.Gone, testCase.wantGone)
			}
		})
	}
}

func TestDiscordDeliverRefusesUnbuildableDeliveriesWithoutSending(t *testing.T) {
	titled := &message.Display{Title: "T"}
	testCases := []struct {
		name        string
		webhookURL  func(serverURL string) string
		noSecretKey bool
		display     *message.Display
	}{
		{"missing webhook url", nil, true, titled},
		{"empty webhook url", func(string) string { return "" }, false, titled},
		{"unparseable webhook url", func(string) string { return "http://\x7f/hook" }, false, titled},
		{"relative webhook url", func(string) string { return "/api/webhooks/1/abc" }, false, titled},
		{"nil display", func(serverURL string) string { return serverURL + discordPath }, false, nil},
		{"empty display", func(serverURL string) string { return serverURL + discordPath }, false, &message.Display{}},
		{
			"display with only a url", func(serverURL string) string { return serverURL + discordPath }, false,
			&message.Display{URL: "https://x"},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			server, captured := newCaptureServer(subTest, http.StatusOK, "{}", nil)
			destination := discordDestination("")
			delete(destination.Secrets, "webhook_url")
			if !testCase.noSecretKey {
				destination.Secrets["webhook_url"] = testCase.webhookURL(server.URL)
			}

			outcome := newTestDiscordSink().Deliver(context.Background(), destination, discordRequest(testCase.display))

			if !errors.Is(outcome.Err, ErrInvalidDelivery) {
				subTest.Fatalf("Err = %v, want ErrInvalidDelivery", outcome.Err)
			}
			if outcome.StatusCode != 0 {
				subTest.Errorf("status = %d, want 0", outcome.StatusCode)
			}
			if captured.Received() {
				subTest.Error("a request was sent for a delivery that cannot be built")
			}
		})
	}
}

func TestDiscordDeliverHonorsContextCancellation(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusOK, "{}", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome := newTestDiscordSink().Deliver(
		ctx, discordDestination(server.URL+discordPath), discordRequest(&message.Display{Title: "T"}),
	)

	if !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", outcome.Err)
	}
	if errors.Is(outcome.Err, ErrInvalidDelivery) {
		t.Error("a cancelled context must stay retryable, not invalid")
	}
}

func TestDiscordTransportErrorLeaksNoWebhookIdentity(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusOK, "{}", nil)
	webhookURL := server.URL + discordPath
	parsed, parseErr := url.Parse(webhookURL)
	if parseErr != nil {
		t.Fatalf("parse test server url: %v", parseErr)
	}
	server.Close()

	outcome := newTestDiscordSink().Deliver(
		context.Background(), discordDestination(webhookURL), discordRequest(&message.Display{Title: "T"}),
	)

	if outcome.Err == nil || outcome.StatusCode != 0 {
		t.Fatalf("outcome = %+v, want a transport error", outcome)
	}
	for _, leaked := range []string{webhookURL, discordTokenSegment, discordPath, parsed.Host, parsed.Hostname(), parsed.Port()} {
		if strings.Contains(outcome.Err.Error(), leaked) {
			t.Errorf("error %q leaks %q", outcome.Err, leaked)
		}
	}
}

func TestDiscordTransportErrorMasksAHostnameTheCauseQuotes(t *testing.T) {
	const hostname = "tenant-secret.example.com"
	sentinel := errors.New("sentinel transport failure")
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("lookup " + hostname + ": no such host: " + sentinel.Error())
	})}

	outcome := NewDiscordSink(client).Deliver(
		context.Background(), discordDestination("https://"+hostname+discordPath), discordRequest(&message.Display{Title: "T"}),
	)

	if outcome.Err == nil {
		t.Fatal("Err = nil, want a transport error")
	}
	for _, leaked := range []string{hostname, discordTokenSegment, discordPath} {
		if strings.Contains(outcome.Err.Error(), leaked) {
			t.Errorf("error %q leaks %q", outcome.Err, leaked)
		}
	}
	if !strings.Contains(outcome.Err.Error(), sentinel.Error()) {
		t.Errorf("error %q lost the cause text", outcome.Err)
	}
}

func TestDiscordSnippetRedactsWebhookURLTokenAndHost(t *testing.T) {
	// A 400 whose body echoes the request line, the token on its own, and the
	// full URL it was reached at.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, "bad request to http://"+request.Host+request.URL.RequestURI()+
			" path="+request.URL.Path+" token="+discordTokenSegment+" host="+request.Host)
	}))
	t.Cleanup(server.Close)
	webhookURL := server.URL + discordPath
	parsed, parseErr := url.Parse(webhookURL)
	if parseErr != nil {
		t.Fatalf("parse test server url: %v", parseErr)
	}

	outcome := newTestDiscordSink().Deliver(
		context.Background(), discordDestination(webhookURL), discordRequest(&message.Display{Title: "T"}),
	)

	requireDelivered(t, outcome)
	if outcome.StatusCode != http.StatusBadRequest || outcome.Gone {
		t.Fatalf("outcome = %+v, want a plain 400", outcome)
	}
	for _, leaked := range []string{webhookURL, discordPath, discordTokenSegment, parsed.Host, parsed.Hostname(), "wait=true"} {
		if strings.Contains(outcome.ResponseSnippet, leaked) {
			t.Errorf("snippet %q leaks %q", outcome.ResponseSnippet, leaked)
		}
	}
	if !strings.Contains(outcome.ResponseSnippet, "[redacted]") {
		t.Errorf("snippet %q shows no redaction marker", outcome.ResponseSnippet)
	}
}

func TestDiscordResponseSnippetIsCapped(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusInternalServerError, strings.Repeat("x", 5000), nil)

	outcome := newTestDiscordSink().Deliver(
		context.Background(), discordDestination(server.URL+discordPath), discordRequest(&message.Display{Title: "T"}),
	)

	requireDelivered(t, outcome)
	if len(outcome.ResponseSnippet) != transport.MaxResponseSnippetBytes {
		t.Errorf("snippet length = %d, want %d", len(outcome.ResponseSnippet), transport.MaxResponseSnippetBytes)
	}
}

func TestDiscordSSRFClientBlocksPlainHTTPAndErrorStaysClean(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusOK, "{}", nil)
	sink := NewDiscordSink(transport.New(transport.Config{}))

	// The test server is plain http on loopback: the guarded client must refuse it.
	outcome := sink.Deliver(
		context.Background(), discordDestination(server.URL+discordPath), discordRequest(&message.Display{Title: "T"}),
	)

	if outcome.Err == nil || !transport.IsBlocked(outcome.Err) {
		t.Fatalf("err = %v, want a blocked error", outcome.Err)
	}
	for _, leaked := range []string{server.URL, discordTokenSegment} {
		if strings.Contains(outcome.Err.Error(), leaked) {
			t.Errorf("error %q leaks %q", outcome.Err, leaked)
		}
	}
}

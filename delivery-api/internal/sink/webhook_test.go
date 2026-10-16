package sink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	currentSecret  = "current-secret-0123456789-abcdefghijkl"
	previousSecret = "previous-secret-0123456789-abcdefghijk"

	// goldenBody is the exact body goldenRequest serializes to.
	goldenBody = `{"message_id":"11111111-1111-1111-1111-111111111111","app_id":"app1","channel":"alerts",` +
		`"idempotency_key":"key-1","critical":true,"created_at":"2026-10-16T12:00:00Z",` +
		`"payload":{"a":1},"display":{"title":"T"}}`
	goldenTimestamp = "1790000000"
	// Computed once with: printf '%s' '<ts>.<body>' | openssl dgst -sha256 -hmac '<secret>'
	goldenCurrentSignature  = "v1=b54104479ee5a85a5ea5bcce1ee1c8b104e36627856c703d194af405df7fdae5"
	goldenPreviousSignature = "v1=fb4d91d151c65055b99fa5c4c7de4f05729c65699555afee0d99f8e9d2662be0"
)

var goldenNow = time.Unix(1790000000, 0).UTC()

// capture records the request a test server received. The handler goroutine
// writes it and the test goroutine reads it, so access goes through the mutex.
type capture struct {
	mutex          sync.Mutex
	received       bool
	requestHeaders http.Header
	requestBody    []byte
}

func (captured *capture) record(headers http.Header, body []byte) {
	captured.mutex.Lock()
	defer captured.mutex.Unlock()
	captured.received = true
	captured.requestHeaders = headers
	captured.requestBody = body
}

// Received reports whether any request reached the server.
func (captured *capture) Received() bool {
	captured.mutex.Lock()
	defer captured.mutex.Unlock()
	return captured.received
}

// Headers returns the headers of the last request, or nil when none arrived.
func (captured *capture) Headers() http.Header {
	captured.mutex.Lock()
	defer captured.mutex.Unlock()
	return captured.requestHeaders
}

// Body returns the body of the last request.
func (captured *capture) Body() []byte {
	captured.mutex.Lock()
	defer captured.mutex.Unlock()
	return captured.requestBody
}

// newCaptureServer returns a test server recording the request and answering with status and extra headers.
func newCaptureServer(t *testing.T, status int, responseBody string, responseHeaders map[string]string) (*httptest.Server, *capture) {
	t.Helper()
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		captured.record(request.Header.Clone(), body)
		for name, value := range responseHeaders {
			writer.Header().Set(name, value)
		}
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, responseBody)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func goldenRequest() Request {
	return Request{
		MessageID: "11111111-1111-1111-1111-111111111111",
		CreatedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC),
		Envelope: message.Envelope{
			AppID:          "app1",
			Channel:        "alerts",
			IdempotencyKey: "key-1",
			Critical:       true,
			Payload:        json.RawMessage(`{"a":1}`),
			Display:        &message.Display{Title: "T"},
		},
	}
}

func destinationFor(url string) registry.ResolvedDestination {
	return registry.ResolvedDestination{
		ID:      1,
		Name:    "hook",
		Type:    "webhook",
		Config:  map[string]string{},
		Secrets: map[string]string{"url": url, "signing_secret": currentSecret},
	}
}

func testSink() *WebhookSink {
	sink := NewWebhookSink(http.DefaultClient)
	sink.now = func() time.Time { return goldenNow }
	return sink
}

// requireDelivered fails the test unless the delivery produced a response.
func requireDelivered(t *testing.T, outcome Outcome) {
	t.Helper()
	if outcome.Err != nil {
		t.Fatalf("outcome.Err = %v, want nil", outcome.Err)
	}
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestNewWebhookSinkSetsClockAndClient(t *testing.T) {
	sink := NewWebhookSink(http.DefaultClient)

	if sink.now == nil {
		t.Fatal("NewWebhookSink left the clock nil")
	}
	if sink.client != http.DefaultClient {
		t.Error("NewWebhookSink did not keep the client it was given")
	}
}

func TestNewWebhookSinkPanicsWithoutClient(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewWebhookSink(nil) did not panic")
		}
	}()

	NewWebhookSink(nil)
}

func TestDeliverSendsEveryHeaderAndGoldenSignature(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "ok", nil)

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL+"/hook"), goldenRequest())

	if outcome.Err != nil || outcome.StatusCode != http.StatusOK {
		t.Fatalf("outcome = %+v", outcome)
	}
	want := map[string]string{
		"Content-Type":               "application/json",
		"User-Agent":                 "4irl-delivery/1",
		"X-Delivery-Message-Id":      "11111111-1111-1111-1111-111111111111",
		"X-Delivery-Idempotency-Key": "key-1",
		"X-Delivery-Timestamp":       goldenTimestamp,
		"X-Delivery-Signature":       goldenCurrentSignature,
	}
	for name, value := range want {
		if got := captured.Headers().Get(name); got != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
	if string(captured.Body()) != goldenBody {
		t.Errorf("body = %s\nwant   %s", captured.Body(), goldenBody)
	}
	if outcome.ResponseSnippet != "ok" {
		t.Errorf("snippet = %q", outcome.ResponseSnippet)
	}
}

func TestSignatureChangesWhenBodyByteFlips(t *testing.T) {
	original := sign(currentSecret, goldenTimestamp, []byte(goldenBody))
	if original != goldenCurrentSignature {
		t.Fatalf("sign() = %s, want golden %s", original, goldenCurrentSignature)
	}
	flipped := []byte(goldenBody)
	flipped[len(flipped)-3] ^= 0x01
	if sign(currentSecret, goldenTimestamp, flipped) == original {
		t.Fatal("signature unchanged after flipping a body byte")
	}
	if sign(currentSecret, "1790000001", []byte(goldenBody)) == original {
		t.Fatal("signature unchanged after changing the timestamp")
	}
}

func TestSignatureMatchesSentBodyBytes(t *testing.T) {
	// The HMAC input must be byte-identical to the bytes written to the wire.
	server, captured := newCaptureServer(t, http.StatusOK, "", nil)

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL), goldenRequest())

	requireDelivered(t, outcome)
	got := sign(currentSecret, goldenTimestamp, captured.Body())
	if got != captured.Headers().Get("X-Delivery-Signature") {
		t.Fatalf("signature over received body = %s, header = %s", got, captured.Headers().Get("X-Delivery-Signature"))
	}
}

func TestPreviousSecretSignatureWindow(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt string
		wantSigs  []string
	}{
		{"unexpired adds second signature", "2026-10-17T00:00:00Z", []string{goldenCurrentSignature, goldenPreviousSignature}},
		{"expired", "2020-01-01T00:00:00Z", []string{goldenCurrentSignature}},
		{"expiring exactly now is expired", goldenNow.Format(time.RFC3339), []string{goldenCurrentSignature}},
		{"malformed expiry", "not-a-date", []string{goldenCurrentSignature}},
		{"missing expiry", "", []string{goldenCurrentSignature}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, captured := newCaptureServer(t, http.StatusOK, "", nil)
			destination := destinationFor(server.URL)
			destination.Secrets["signing_secret_previous"] = previousSecret
			if test.expiresAt != "" {
				destination.Config["previous_secret_expires_at"] = test.expiresAt
			}

			outcome := testSink().Deliver(context.Background(), destination, goldenRequest())

			if outcome.Err != nil {
				t.Fatalf("err = %v", outcome.Err)
			}
			if got := captured.Headers().Get("X-Delivery-Signature"); got != strings.Join(test.wantSigs, ",") {
				t.Errorf("signature header = %q, want %q", got, strings.Join(test.wantSigs, ","))
			}
		})
	}
}

func TestPreviousSecretNotUsedWhenSecretMissing(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "", nil)
	destination := destinationFor(server.URL)
	destination.Config["previous_secret_expires_at"] = "2026-10-17T00:00:00Z"

	outcome := testSink().Deliver(context.Background(), destination, goldenRequest())

	requireDelivered(t, outcome)
	if got := captured.Headers().Get("X-Delivery-Signature"); got != goldenCurrentSignature {
		t.Errorf("signature header = %q, want only the current signature", got)
	}
}

func TestBodyKeysAndOmittedDisplay(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "", nil)
	request := goldenRequest()
	request.Envelope.Display = nil

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL), request)

	requireDelivered(t, outcome)
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(captured.Body(), &decoded); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	wantKeys := []string{"message_id", "app_id", "channel", "idempotency_key", "critical", "created_at", "payload"}
	if len(decoded) != len(wantKeys) {
		t.Errorf("body has %d keys, want %d: %s", len(decoded), len(wantKeys), captured.Body())
	}
	for _, key := range wantKeys {
		if _, ok := decoded[key]; !ok {
			t.Errorf("body is missing key %q", key)
		}
	}
	if _, ok := decoded["display"]; ok {
		t.Error("display must be omitted when nil")
	}
}

func TestNilPayloadIsSentAsJSONNull(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "", nil)
	request := goldenRequest()
	request.Envelope.Payload = nil

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL), request)

	requireDelivered(t, outcome)
	if !strings.Contains(string(captured.Body()), `"payload":null`) {
		t.Errorf("body = %s, want it to contain %q", captured.Body(), `"payload":null`)
	}
}

func TestCreatedAtIsSentAsUTCWithFractionalSeconds(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "", nil)
	request := goldenRequest()
	request.CreatedAt = time.Date(2026, 10, 16, 14, 0, 0, 123456789, time.FixedZone("plus-two", 2*60*60))

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL), request)

	requireDelivered(t, outcome)
	want := `"created_at":"2026-10-16T12:00:00.123456789Z"`
	if !strings.Contains(string(captured.Body()), want) {
		t.Errorf("body = %s, want it to contain %s", captured.Body(), want)
	}
}

func TestRetryAfterIsParsed(t *testing.T) {
	httpDate := goldenNow.Add(90 * time.Second).Format(http.TimeFormat)
	farFutureDate := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).Format(http.TimeFormat)
	tests := []struct {
		name   string
		header string
		want   *time.Duration
	}{
		{"delta seconds", "120", durationPointer(120 * time.Second)},
		{"http date", httpDate, durationPointer(90 * time.Second)},
		{"date in the past clamps to zero", goldenNow.Add(-time.Hour).Format(http.TimeFormat), durationPointer(0)},
		{"negative clamps to zero", "-5", durationPointer(0)},
		{"zero", "0", durationPointer(0)},
		{"surrounding whitespace", " 7 ", durationPointer(7 * time.Second)},
		{"exactly the cap", "86400", durationPointer(maxRetryAfter)},
		{"one second over the cap", "86401", durationPointer(maxRetryAfter)},
		{"absurdly large saturates at the cap", "99999999999999999999", durationPointer(maxRetryAfter)},
		{"http date in year 9999 caps", farFutureDate, durationPointer(maxRetryAfter)},
		{"absurdly negative clamps to zero", "-99999999999999999999", durationPointer(0)},
		{"garbage", "soon", nil},
		{"empty", "", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseRetryAfter(test.header, goldenNow)
			switch {
			case got == nil && test.want == nil:
			case got == nil || test.want == nil:
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", test.header, got, test.want)
			case *got != *test.want:
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", test.header, *got, *test.want)
			}
		})
	}
}

func TestMaxRetryAfterIsOneDay(t *testing.T) {
	if maxRetryAfter != 24*time.Hour {
		t.Fatalf("maxRetryAfter = %v, want 24h", maxRetryAfter)
	}
}

func TestDeliverReportsRetryAfterAndStatus(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusTooManyRequests, "slow down", map[string]string{"Retry-After": "120"})

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL), goldenRequest())

	if outcome.Err != nil || outcome.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.RetryAfter == nil || *outcome.RetryAfter != 120*time.Second {
		t.Errorf("RetryAfter = %v, want 120s", outcome.RetryAfter)
	}
	if outcome.ResponseSnippet != "slow down" {
		t.Errorf("snippet = %q", outcome.ResponseSnippet)
	}
}

func TestResponseSnippetIsCapped(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusInternalServerError, strings.Repeat("x", 5000), nil)

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL), goldenRequest())

	requireDelivered(t, outcome)
	if len(outcome.ResponseSnippet) != transport.MaxResponseSnippetBytes {
		t.Errorf("snippet length = %d, want %d", len(outcome.ResponseSnippet), transport.MaxResponseSnippetBytes)
	}
}

func TestResponseSnippetRedactsDestinationAndSecrets(t *testing.T) {
	const (
		urlSecret   = "s3cr3t-path-token"
		pathAndPart = "/hooks/" + urlSecret
		query       = "k=v"
		password    = "p4ssw0rd-userinfo"
	)
	// A receiver that echoes the request line, the full URL it was reached at
	// (userinfo included), and the signing secrets back in its error page.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fullURL := "http://operator:" + password + "@" + request.Host + request.URL.RequestURI()
		writer.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(writer, "Cannot POST "+request.URL.RequestURI()+
			" url="+fullURL+
			" secret="+currentSecret+" previous="+previousSecret+" pw="+password)
	}))
	t.Cleanup(server.Close)
	destinationURL := strings.Replace(server.URL, "http://", "http://operator:"+password+"@", 1) + pathAndPart + "?" + query

	destination := destinationFor(destinationURL)
	destination.Secrets["signing_secret_previous"] = previousSecret

	outcome := testSink().Deliver(context.Background(), destination, goldenRequest())

	requireDelivered(t, outcome)
	if outcome.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", outcome.StatusCode)
	}
	for _, leaked := range []string{destinationURL, pathAndPart, urlSecret, query, currentSecret, previousSecret, password, "operator"} {
		if strings.Contains(outcome.ResponseSnippet, leaked) {
			t.Errorf("snippet %q leaks %q", outcome.ResponseSnippet, leaked)
		}
	}
	if !strings.Contains(outcome.ResponseSnippet, "[redacted]") {
		t.Errorf("snippet %q shows no redaction marker", outcome.ResponseSnippet)
	}
}

func TestTransportErrorLeaksNeitherURLNorSecret(t *testing.T) {
	const urlSecret = "s3cr3t-path-token"
	server, _ := newCaptureServer(t, http.StatusOK, "", nil)
	closedURL := server.URL + "/hooks/" + urlSecret
	server.Close()

	outcome := testSink().Deliver(context.Background(), destinationFor(closedURL), goldenRequest())

	if outcome.Err == nil || outcome.StatusCode != 0 {
		t.Fatalf("outcome = %+v, want a transport error", outcome)
	}
	for _, leaked := range []string{closedURL, urlSecret, currentSecret, "/hooks/"} {
		if strings.Contains(outcome.Err.Error(), leaked) {
			t.Errorf("error %q leaks %q", outcome.Err, leaked)
		}
	}
}

func TestTransportErrorRedactsHostname(t *testing.T) {
	const hostname = "tenant-secret.example.com"
	errSentinel := errors.New("sentinel transport failure")
	tests := []struct {
		name      string
		cause     error
		wantIsErr error
	}{
		{"dns failure", fmt.Errorf("lookup %s: no such host", hostname), nil},
		{"certificate mismatch in mixed case", fmt.Errorf("x509: certificate is valid for other.test, not %s", strings.ToUpper(hostname)), nil},
		{"wrapped sentinel stays matchable", fmt.Errorf("dial %s: %w", hostname, errSentinel), errSentinel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, test.cause
			})}
			sink := NewWebhookSink(client)
			sink.now = func() time.Time { return goldenNow }

			outcome := sink.Deliver(context.Background(), destinationFor("https://"+hostname+"/hook"), goldenRequest())

			if outcome.Err == nil || outcome.StatusCode != 0 {
				t.Fatalf("outcome = %+v, want a transport error", outcome)
			}
			if strings.Contains(strings.ToLower(outcome.Err.Error()), hostname) {
				t.Errorf("error %q leaks the hostname", outcome.Err)
			}
			if !strings.Contains(outcome.Err.Error(), "[host]") {
				t.Errorf("error %q has no host placeholder", outcome.Err)
			}
			if !strings.HasPrefix(outcome.Err.Error(), "webhook request failed: ") {
				t.Errorf("error %q lost its prefix", outcome.Err)
			}
			if test.wantIsErr != nil && !errors.Is(outcome.Err, test.wantIsErr) {
				t.Errorf("errors.Is(%v, %v) = false, want true", outcome.Err, test.wantIsErr)
			}
		})
	}
}

func TestTransportErrorMasksResolvedAddresses(t *testing.T) {
	const hostname = "tenant-secret.example.com"
	resolvedAddr := &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 443}
	tests := []struct {
		name      string
		cause     error
		wantText  string
		wantIsErr error
	}{
		{
			name: "dial failure",
			cause: &net.OpError{
				Op: "dial", Net: "tcp", Addr: resolvedAddr,
				Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
			},
			wantText: "dial tcp: connect: connection refused",
		},
		{
			name: "dns failure names neither host nor resolver",
			cause: &net.OpError{
				Op: "dial", Net: "tcp",
				Err: &net.DNSError{Err: "no such host", Name: hostname, Server: "127.0.0.11:53"},
			},
			wantText: "dial tcp: lookup failed: no such host",
		},
		{
			name:     "bare dns failure",
			cause:    &net.DNSError{Err: "i/o timeout", Name: hostname, Server: "127.0.0.11:53", IsTimeout: true},
			wantText: "lookup failed: i/o timeout",
		},
		{
			name: "blocked address does not name the internal ip",
			cause: &net.OpError{
				Op: "dial", Net: "tcp",
				Addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 8443},
				Err:  transport.ErrBlockedAddress,
			},
			wantText:  "dial tcp: " + transport.ErrBlockedAddress.Error(),
			wantIsErr: transport.ErrBlockedAddress,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, test.cause
			})}

			outcome := NewWebhookSink(client).Deliver(
				context.Background(), destinationFor("https://"+hostname+"/hook"), goldenRequest(),
			)

			if outcome.Err == nil {
				t.Fatal("want a transport error")
			}
			if want := "webhook request failed: " + test.wantText; outcome.Err.Error() != want {
				t.Errorf("error = %q, want %q", outcome.Err, want)
			}
			for _, leaked := range []string{"203.0.113.9", "10.0.0.5", "443", "8443", "127.0.0.11", ":53", hostname} {
				if strings.Contains(outcome.Err.Error(), leaked) {
					t.Errorf("error %q leaks %q", outcome.Err, leaked)
				}
			}
			if test.wantIsErr != nil {
				if !errors.Is(outcome.Err, test.wantIsErr) {
					t.Errorf("errors.Is(%v, %v) = false, want true", outcome.Err, test.wantIsErr)
				}
				if !transport.IsBlocked(outcome.Err) {
					t.Error("transport.IsBlocked = false, want true")
				}
			}
		})
	}
}

func TestSSRFClientBlocksAndErrorStaysClean(t *testing.T) {
	const urlSecret = "s3cr3t-path-token"
	server, _ := newCaptureServer(t, http.StatusOK, "", nil)
	sink := NewWebhookSink(transport.New(transport.Config{}))
	sink.now = func() time.Time { return goldenNow }

	// The test server is plain http on loopback: the guarded client must refuse it.
	outcome := sink.Deliver(context.Background(), destinationFor(server.URL+"/"+urlSecret), goldenRequest())

	if outcome.Err == nil || !transport.IsBlocked(outcome.Err) {
		t.Fatalf("err = %v, want a blocked error", outcome.Err)
	}
	if strings.Contains(outcome.Err.Error(), urlSecret) || strings.Contains(outcome.Err.Error(), server.URL) {
		t.Errorf("error %q leaks the URL", outcome.Err)
	}
}

func TestDeliverFailsClosedOnBadDestination(t *testing.T) {
	const urlSecret = "s3cr3t-path-token"
	tests := []struct {
		name    string
		mutate  func(*registry.ResolvedDestination)
		request func(*Request)
	}{
		{"missing url", func(d *registry.ResolvedDestination) { delete(d.Secrets, "url") }, nil},
		{"missing signing secret", func(d *registry.ResolvedDestination) { delete(d.Secrets, "signing_secret") }, nil},
		{"unparseable url", func(d *registry.ResolvedDestination) { d.Secrets["url"] = "https://exa mple.com/" + urlSecret + "\x7f" }, nil},
		{"header injection in idempotency key", nil, func(r *Request) { r.Envelope.IdempotencyKey = "key\r\nX-Evil: 1" }},
		{"header injection in message id", nil, func(r *Request) { r.MessageID = "id\nX-Evil: 1" }},
		{"empty idempotency key", nil, func(r *Request) { r.Envelope.IdempotencyKey = "" }},
		{"overlong idempotency key", nil, func(r *Request) { r.Envelope.IdempotencyKey = strings.Repeat("k", 201) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, captured := newCaptureServer(t, http.StatusOK, "", nil)
			destination := destinationFor(server.URL)
			request := goldenRequest()
			if test.mutate != nil {
				test.mutate(&destination)
			}
			if test.request != nil {
				test.request(&request)
			}

			outcome := testSink().Deliver(context.Background(), destination, request)

			if outcome.Err == nil || outcome.StatusCode != 0 {
				t.Fatalf("outcome = %+v, want a refusal", outcome)
			}
			if !errors.Is(outcome.Err, ErrInvalidDelivery) {
				t.Errorf("err = %v, want ErrInvalidDelivery", outcome.Err)
			}
			if captured.Received() {
				t.Error("a request reached the server")
			}
			if strings.Contains(outcome.Err.Error(), urlSecret) {
				t.Errorf("error %q leaks the URL", outcome.Err)
			}
		})
	}
}

func TestDeliverRefusesUnencodablePayloadAsPermanent(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "", nil)
	request := goldenRequest()
	// Empty but non-nil: json.Marshal rejects it, unlike a nil payload.
	request.Envelope.Payload = json.RawMessage{}

	outcome := testSink().Deliver(context.Background(), destinationFor(server.URL), request)

	if outcome.Err == nil || outcome.StatusCode != 0 {
		t.Fatalf("outcome = %+v, want a refusal", outcome)
	}
	if !errors.Is(outcome.Err, ErrInvalidDelivery) {
		t.Errorf("err = %v, want ErrInvalidDelivery", outcome.Err)
	}
	if captured.Received() {
		t.Error("a request reached the server")
	}
}

func TestDeliverSignsWithTheRealClockByDefault(t *testing.T) {
	server, captured := newCaptureServer(t, http.StatusOK, "", nil)
	sink := NewWebhookSink(http.DefaultClient)

	outcome := sink.Deliver(context.Background(), destinationFor(server.URL), goldenRequest())

	requireDelivered(t, outcome)
	seconds, err := strconv.ParseInt(captured.Headers().Get("X-Delivery-Timestamp"), 10, 64)
	if err != nil {
		t.Fatalf("timestamp header is not an integer: %v", err)
	}
	if drift := time.Since(time.Unix(seconds, 0)).Abs(); drift > 10*time.Second {
		t.Errorf("timestamp is %v away from now, want within 10s", drift)
	}
}

func TestDeliverHonorsContextCancellation(t *testing.T) {
	server, _ := newCaptureServer(t, http.StatusOK, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome := testSink().Deliver(ctx, destinationFor(server.URL), goldenRequest())

	if outcome.Err == nil {
		t.Fatal("want an error for a cancelled context")
	}
	if !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", outcome.Err)
	}
	if outcome.StatusCode != 0 {
		t.Errorf("status = %d, want 0", outcome.StatusCode)
	}
}

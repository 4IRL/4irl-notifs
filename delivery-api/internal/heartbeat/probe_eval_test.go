package heartbeat

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const probeEvalCheckID int64 = 41

func allowLoopbackAddr(addr netip.Addr) bool { return addr.Unmap().IsLoopback() }

// loopbackProbeClient trusts server's certificate and may dial loopback, as the transport package's
// own tests do.
func loopbackProbeClient(testInstance *testing.T, server *httptest.Server) *http.Client {
	testInstance.Helper()
	serverTransport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		testInstance.Fatalf("httptest client transport is %T, want *http.Transport", server.Client().Transport)
	}
	return transport.New(transport.Config{
		AllowAddr: allowLoopbackAddr,
		TLSConfig: serverTransport.TLSClientConfig,
	})
}

func newProbeServer(testInstance *testing.T, handler http.Handler) *httptest.Server {
	testInstance.Helper()
	server := httptest.NewTLSServer(handler)
	testInstance.Cleanup(server.Close)
	return server
}

func newProbeEvalSealer(testInstance *testing.T) *seal.Sealer {
	testInstance.Helper()
	sealer, sealerErr := seal.New([]byte("0123456789abcdef0123456789abcdef"))
	if sealerErr != nil {
		testInstance.Fatalf("seal.New: %v", sealerErr)
	}
	return sealer
}

func probeEvalClaim(url string, statuses []int, bodyMatch *string, timeoutSeconds int) ProbeClaim {
	return ProbeClaim{
		CheckID:       probeEvalCheckID,
		AppID:         "app",
		Name:          "site",
		ConfigVersion: 1,
		Config: ProbeConfig{
			URL:              url,
			ExpectedStatuses: statuses,
			BodyMatch:        bodyMatch,
			TimeoutSeconds:   timeoutSeconds,
			IntervalSeconds:  60,
		},
	}
}

// assertProbeResult checks reason, status and detail, that the reason is a stored one and that the
// detail leaks nothing about the target or a credential.
func assertProbeResult(testInstance *testing.T, got ProbeResult, wantReason string, wantStatus *int, wantDetail string, forbidden ...string) {
	testInstance.Helper()
	if wantReason == "" {
		if got.Reason != nil {
			testInstance.Fatalf("Reason = %q, want success", *got.Reason)
		}
	} else {
		if got.Reason == nil || *got.Reason != wantReason {
			testInstance.Fatalf("Reason = %v, want %q (detail %q)", got.Reason, wantReason, got.Detail)
		}
		if !IsProbeResultReason(*got.Reason) {
			testInstance.Fatalf("Reason %q is not an accepted probe reason", *got.Reason)
		}
	}
	switch {
	case wantStatus == nil && got.Status != nil:
		testInstance.Fatalf("Status = %d, want nil", *got.Status)
	case wantStatus != nil && (got.Status == nil || *got.Status != *wantStatus):
		testInstance.Fatalf("Status = %v, want %d", got.Status, *wantStatus)
	}
	if got.Detail != wantDetail {
		testInstance.Fatalf("Detail = %q, want %q", got.Detail, wantDetail)
	}
	for _, text := range forbidden {
		if text != "" && strings.Contains(got.Detail, text) {
			testInstance.Fatalf("Detail %q leaks %q", got.Detail, text)
		}
	}
}

func statusPointer(code int) *int { return &code }

func TestRunProbeStatusAndBodyMatching(testInstance *testing.T) {
	var redirectTargetHits atomic.Int64
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ok":
			_, _ = writer.Write([]byte("hello world"))
		case "/redirect":
			http.Redirect(writer, request, "/target", http.StatusMovedPermanently)
		case "/target":
			redirectTargetHits.Add(1)
		case "/down":
			writer.WriteHeader(http.StatusServiceUnavailable)
		case "/edge":
			// The needle ends exactly at byte 65536.
			body := strings.Repeat("a", ProbeBodyLimitBytes-len("needle")) + "needle"
			_, _ = writer.Write([]byte(body))
		case "/past":
			// The needle ends at byte 65537, one past the limit.
			body := strings.Repeat("a", ProbeBodyLimitBytes+1-len("needle")) + "needle"
			_, _ = writer.Write([]byte(body))
		}
	}))
	client := loopbackProbeClient(testInstance, server)

	testCases := []struct {
		name       string
		path       string
		statuses   []int
		bodyMatch  *string
		wantReason string
		wantStatus *int
		wantDetail string
	}{
		{name: "listed 200", path: "/ok", statuses: []int{200}, wantStatus: statusPointer(200)},
		{name: "listed 301 is not followed", path: "/redirect", statuses: []int{301}, wantStatus: statusPointer(301)},
		{
			name: "unlisted 503", path: "/down", statuses: []int{200},
			wantReason: ReasonHTTPStatus, wantStatus: statusPointer(503), wantDetail: "HTTP 503",
		},
		{name: "needle present", path: "/ok", statuses: []int{200}, bodyMatch: stringPointer("world"), wantStatus: statusPointer(200)},
		{
			name: "needle absent", path: "/ok", statuses: []int{200}, bodyMatch: stringPointer("nope"),
			wantReason: ReasonBodyMismatch, wantStatus: statusPointer(200), wantDetail: detailBodyMismatch,
		},
		{name: "needle ending at 64 KiB", path: "/edge", statuses: []int{200}, bodyMatch: stringPointer("needle"), wantStatus: statusPointer(200)},
		{
			name: "needle ending one past 64 KiB", path: "/past", statuses: []int{200}, bodyMatch: stringPointer("needle"),
			wantReason: ReasonBodyMismatch, wantStatus: statusPointer(200), wantDetail: detailBodyMismatch,
		},
		{
			name: "status mismatch wins over body match", path: "/down", statuses: []int{200}, bodyMatch: stringPointer("x"),
			wantReason: ReasonHTTPStatus, wantStatus: statusPointer(503), wantDetail: "HTTP 503",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			claim := probeEvalClaim(server.URL+testCase.path, testCase.statuses, testCase.bodyMatch, 5)
			got := runProbe(context.Background(), client, nil, claim)
			assertProbeResult(testInstance, got, testCase.wantReason, testCase.wantStatus, testCase.wantDetail, server.URL)
		})
	}
	if hits := redirectTargetHits.Load(); hits != 0 {
		testInstance.Fatalf("redirect target requested %d times, want 0", hits)
	}
}

func TestRunProbeDoesNotReadPastTheBodyLimit(testInstance *testing.T) {
	const chunkBytes = 32 << 10
	const chunkCount = 32 << 10 // 1 GiB of chunks, far more than any buffer
	var written atomic.Int64
	handlerDone := make(chan struct{})
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		defer close(handlerDone)
		chunk := []byte(strings.Repeat("a", chunkBytes))
		copy(chunk, "needle")
		for range chunkCount {
			count, writeErr := writer.Write(chunk)
			written.Add(int64(count))
			if writeErr != nil {
				return
			}
		}
	}))
	client := loopbackProbeClient(testInstance, server)

	claim := probeEvalClaim(server.URL, []int{200}, stringPointer("needle"), 5)
	got := runProbe(context.Background(), client, nil, claim)
	assertProbeResult(testInstance, got, "", statusPointer(200), "")

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		testInstance.Fatal("the stream handler is still writing: the body was not closed after the bounded read")
	}
	if total := written.Load(); total >= 16<<20 {
		testInstance.Fatalf("server wrote %d bytes, want far fewer than the stream", total)
	}
}

func TestRunProbeTimeouts(testInstance *testing.T) {
	release := make(chan struct{})
	server := newProbeServer(testInstance, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	testInstance.Cleanup(func() { close(release) })
	client := loopbackProbeClient(testInstance, server)

	testInstance.Run("headers never arrive", func(testInstance *testing.T) {
		claim := probeEvalClaim(server.URL+"/stall-headers", []int{200}, nil, 1)
		got := runProbe(context.Background(), client, nil, claim)
		assertProbeResult(testInstance, got, ReasonTimeout, nil, "timed out after 1s", server.URL)
	})
	testInstance.Run("body stalls under a body match", func(testInstance *testing.T) {
		// The response headers are delivered at once by the round tripper, so the status is known
		// before the deadline no matter how slow the machine is; only the body read can time out.
		stalling := &http.Client{Transport: stalledBodyTransport{}}
		claim := probeEvalClaim("https://stalled.invalid/", []int{200}, stringPointer("x"), 1)
		got := runProbe(context.Background(), stalling, nil, claim)
		assertProbeResult(testInstance, got, ReasonTimeout, statusPointer(200), "timed out after 1s", "stalled.invalid")
	})
}

// stalledBodyTransport answers every request with 200 headers and a body whose Read blocks until
// the request context ends, then fails with that context's error.
type stalledBodyTransport struct{}

func (stalledBodyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       stalledBody{ctx: request.Context()},
		Request:    request,
	}, nil
}

type stalledBody struct{ ctx context.Context }

func (body stalledBody) Read([]byte) (int, error) {
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (stalledBody) Close() error { return nil }

type deadlineRecorder struct {
	next     http.RoundTripper
	deadline atomic.Pointer[time.Time]
}

func (recorder *deadlineRecorder) RoundTrip(request *http.Request) (*http.Response, error) {
	if deadline, ok := request.Context().Deadline(); ok {
		recorder.deadline.Store(&deadline)
	}
	return recorder.next.RoundTrip(request)
}

func TestRunProbeAppliesTheConfiguredTimeoutAsTheRequestDeadline(testInstance *testing.T) {
	server := newProbeServer(testInstance, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := loopbackProbeClient(testInstance, server)
	recorder := &deadlineRecorder{next: base.Transport}
	client := &http.Client{Transport: recorder, Timeout: base.Timeout, CheckRedirect: base.CheckRedirect}

	started := time.Now()
	got := runProbe(context.Background(), client, nil, probeEvalClaim(server.URL, []int{200}, nil, MaxProbeTimeoutSeconds))
	assertProbeResult(testInstance, got, "", statusPointer(200), "")

	deadline := recorder.deadline.Load()
	if deadline == nil {
		testInstance.Fatal("request context carried no deadline")
	}
	remaining := deadline.Sub(started)
	maxTimeout := MaxProbeTimeoutSeconds * time.Second
	if remaining < maxTimeout-time.Second || remaining > maxTimeout+time.Second/2 {
		testInstance.Fatalf("request deadline is %s after the call, want about %s", remaining, maxTimeout)
	}
}

func TestRunProbeTransportFailures(testInstance *testing.T) {
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	trusting := loopbackProbeClient(testInstance, server)

	closedServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closedServer.URL
	closedServer.Close()

	testCases := []struct {
		name       string
		client     *http.Client
		url        string
		wantReason string
		wantDetail string
	}{
		{
			name: "untrusted certificate", client: transport.New(transport.Config{AllowAddr: allowLoopbackAddr}),
			url: server.URL, wantReason: ReasonTLS, wantDetail: detailTLS,
		},
		{
			name: "production policy refuses loopback", client: transport.New(transport.Config{}),
			url: server.URL, wantReason: ReasonBlocked, wantDetail: detailBlocked,
		},
		{
			name: "plain http", client: trusting,
			url: "http://example.com/", wantReason: ReasonScheme, wantDetail: detailScheme,
		},
		{
			name: "closed port", client: trusting,
			url: closedURL, wantReason: ReasonNetwork, wantDetail: detailConnection,
		},
		{
			name: "unbuildable request", client: trusting,
			url: "https://exa mple.com/", wantReason: ReasonNetwork, wantDetail: detailRequestBuild,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			claim := probeEvalClaim(testCase.url, []int{200}, nil, 5)
			got := runProbe(context.Background(), testCase.client, nil, claim)
			assertProbeResult(testInstance, got, testCase.wantReason, nil, testCase.wantDetail, testCase.url, "example.com")
		})
	}
}

// timeoutNetError is a net.Error that reports a timeout but is neither a DNS error nor a deadline.
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "secret.example i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return false }

func TestRunProbeReportsABodyReadFailureAsNetwork(testInstance *testing.T) {
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "1000")
		_, _ = writer.Write([]byte("partial"))
		writer.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	client := loopbackProbeClient(testInstance, server)

	claim := probeEvalClaim(server.URL, []int{200}, stringPointer("never"), 5)
	got := runProbe(context.Background(), client, nil, claim)
	assertProbeResult(testInstance, got, ReasonNetwork, statusPointer(200), detailBodyRead, server.URL)
}

func TestRunProbeRequestShape(testInstance *testing.T) {
	var gotAgent, gotID, gotSecret atomic.Value
	var bodyRead atomic.Bool
	server := newProbeServer(testInstance, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAgent.Store(request.UserAgent())
		gotID.Store(request.Header.Get("CF-Access-Client-Id"))
		gotSecret.Store(request.Header.Get("CF-Access-Client-Secret"))
		_, _ = writer.Write([]byte("hello"))
		bodyRead.Store(true)
	}))
	client := loopbackProbeClient(testInstance, server)

	testInstance.Run("user agent and no access headers without a token", func(testInstance *testing.T) {
		got := runProbe(context.Background(), client, nil, probeEvalClaim(server.URL, []int{200}, nil, 5))
		assertProbeResult(testInstance, got, "", statusPointer(200), "")
		if gotAgent.Load() != "4irl-delivery-probe/1" {
			testInstance.Fatalf("User-Agent = %v, want 4irl-delivery-probe/1", gotAgent.Load())
		}
		if gotID.Load() != "" || gotSecret.Load() != "" {
			testInstance.Fatalf("CF-Access headers sent without a token: %v / %v", gotID.Load(), gotSecret.Load())
		}
	})
	testInstance.Run("cancelled parent context does not abort the probe", func(testInstance *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		cancel()
		got := runProbe(parent, client, nil, probeEvalClaim(server.URL, []int{200}, stringPointer("hello"), 5))
		assertProbeResult(testInstance, got, "", statusPointer(200), "")
	})
}

func TestRunProbePlainHTTPNeverReachesTheServer(testInstance *testing.T) {
	var hits atomic.Int64
	server := newProbeServer(testInstance, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	client := loopbackProbeClient(testInstance, server)
	plainURL := strings.Replace(server.URL, "https://", "http://", 1)

	got := runProbe(context.Background(), client, nil, probeEvalClaim(plainURL, []int{200}, nil, 5))
	assertProbeResult(testInstance, got, ReasonScheme, nil, detailScheme, plainURL)
	if count := hits.Load(); count != 0 {
		testInstance.Fatalf("server saw %d requests over plain http, want 0", count)
	}
}

func TestClassifyProbeError(testInstance *testing.T) {
	testCases := []struct {
		name       string
		err        error
		wantReason string
		wantDetail string
	}{
		{"not https", fmt.Errorf("wrapped: %w", transport.ErrNotHTTPS), ReasonScheme, detailScheme},
		{"blocked", fmt.Errorf("wrapped: %w", transport.ErrBlockedAddress), ReasonBlocked, detailBlocked},
		{"unknown authority", x509.UnknownAuthorityError{}, ReasonTLS, detailTLS},
		{"hostname", x509.HostnameError{Host: "secret.example"}, ReasonTLS, detailTLS},
		{"invalid certificate", x509.CertificateInvalidError{}, ReasonTLS, detailTLS},
		{"record header", tls.RecordHeaderError{Msg: "bad"}, ReasonTLS, detailTLS},
		{"tls alert", tls.AlertError(42), ReasonTLS, detailTLS},
		{"deadline", context.DeadlineExceeded, ReasonTimeout, "timed out after 3s"},
		{"dns timeout", &net.DNSError{Err: "i/o timeout", Name: "secret.example", IsTimeout: true}, ReasonTimeout, "timed out after 3s"},
		{"certificate verification error", &tls.CertificateVerificationError{Err: errors.New("secret.example bad")}, ReasonTLS, detailTLS},
		{"plain net timeout", timeoutNetError{}, ReasonTimeout, "timed out after 3s"},
		{"url error wrapping blocked", &url.Error{Op: "Get", URL: "https://secret.example/?token=abc", Err: transport.ErrBlockedAddress}, ReasonBlocked, detailBlocked},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "secret.example"}, ReasonNetwork, detailDNS},
		{"unrecognised", errors.New("https://secret.example/?token=abc exploded"), ReasonNetwork, detailConnection},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			got := classifyProbeError(testCase.err, 3)
			assertProbeResult(testInstance, got, testCase.wantReason, nil, testCase.wantDetail, "secret.example", "token=abc")
		})
	}
}

func TestRunProbeAccessToken(testInstance *testing.T) {
	sealer := newProbeEvalSealer(testInstance)
	var hits atomic.Int64
	var gotID, gotSecret atomic.Value
	server := newProbeServer(testInstance, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		gotID.Store(request.Header.Get("CF-Access-Client-Id"))
		gotSecret.Store(request.Header.Get("CF-Access-Client-Secret"))
	}))
	client := loopbackProbeClient(testInstance, server)

	mustSeal := func(checkID int64, token AccessToken) []byte {
		sealed, sealErr := sealProbeAccessToken(sealer, checkID, token)
		if sealErr != nil {
			testInstance.Fatalf("sealProbeAccessToken: %v", sealErr)
		}
		return sealed
	}

	testCases := []struct {
		name       string
		sealer     *seal.Sealer
		sealed     []byte
		wantDetail string
	}{
		{"sealed under another check", sealer, mustSeal(probeEvalCheckID+1, AccessToken{ClientID: "id", ClientSecret: "secret-value"}), detailCredentialOpen},
		{"nil sealer", nil, mustSeal(probeEvalCheckID, AccessToken{ClientID: "id", ClientSecret: "secret-value"}), detailCredentialOpen},
		{"unsealable blob", sealer, []byte("not a sealed blob at all, just bytes"), detailCredentialOpen},
		{"empty client id", sealer, mustSeal(probeEvalCheckID, AccessToken{ClientID: "", ClientSecret: "secret-value"}), detailCredentialOpen},
		{"credential with a space", sealer, mustSeal(probeEvalCheckID, AccessToken{ClientID: "id", ClientSecret: "secret value"}), detailCredentialShape},
		{"credential too long", sealer, mustSeal(probeEvalCheckID, AccessToken{ClientID: strings.Repeat("a", MaxAccessCredentialBytes+1), ClientSecret: "s"}), detailCredentialShape},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			before := hits.Load()
			claim := probeEvalClaim(server.URL, []int{200}, nil, 5)
			claim.SealedAccessToken = testCase.sealed
			got := runProbe(context.Background(), client, testCase.sealer, claim)
			assertProbeResult(testInstance, got, ReasonConfig, nil, testCase.wantDetail, "secret", server.URL)
			if after := hits.Load(); after != before {
				testInstance.Fatalf("server saw %d requests, want none", after-before)
			}
		})
	}

	testInstance.Run("valid token sends both headers", func(testInstance *testing.T) {
		claim := probeEvalClaim(server.URL, []int{200}, nil, 5)
		claim.SealedAccessToken = mustSeal(probeEvalCheckID, AccessToken{ClientID: "abc.access", ClientSecret: "s3cret-value"})
		got := runProbe(context.Background(), client, sealer, claim)
		assertProbeResult(testInstance, got, "", statusPointer(200), "")
		if gotID.Load() != "abc.access" || gotSecret.Load() != "s3cret-value" {
			testInstance.Fatalf("headers = %v / %v, want the stored values", gotID.Load(), gotSecret.Load())
		}
	})
}

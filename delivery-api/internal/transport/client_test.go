package transport

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// allowLoopback lets tests reach httptest servers on 127.0.0.1.
func allowLoopback(addr netip.Addr) bool { return addr.Unmap().IsLoopback() }

// trustedTLSConfig returns the TLS config that trusts the httptest server's certificate.
func trustedTLSConfig(testInstance *testing.T, server *httptest.Server) *tls.Config {
	testInstance.Helper()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		testInstance.Fatalf("httptest client transport is %T, want *http.Transport", server.Client().Transport)
	}
	return transport.TLSClientConfig
}

func newLoopbackClient(testInstance *testing.T, server *httptest.Server, config Config) *http.Client {
	testInstance.Helper()
	config.AllowAddr = allowLoopback
	config.TLSConfig = trustedTLSConfig(testInstance, server)
	return New(config)
}

func TestClientRoundTripsA200(testInstance *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	testInstance.Cleanup(server.Close)

	client := newLoopbackClient(testInstance, server, Config{})
	response, err := client.Get(server.URL)
	if err != nil {
		testInstance.Fatalf("Get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		testInstance.Errorf("status = %d, want 200", response.StatusCode)
	}
	if got := ReadSnippet(response.Body); got != "ok" {
		testInstance.Errorf("snippet = %q, want %q", got, "ok")
	}
}

func TestClientDoesNotFollowRedirects(testInstance *testing.T) {
	var targetHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/target", func(writer http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		writer.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/start", func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/target", http.StatusFound)
	})
	server := httptest.NewTLSServer(mux)
	testInstance.Cleanup(server.Close)

	client := newLoopbackClient(testInstance, server, Config{})
	response, err := client.Get(server.URL + "/start")
	if err != nil {
		testInstance.Fatalf("Get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusFound {
		testInstance.Errorf("status = %d, want 302 returned unfollowed", response.StatusCode)
	}
	if response.Header.Get("Location") != "/target" {
		testInstance.Errorf("Location = %q, want /target", response.Header.Get("Location"))
	}
	if hits := targetHits.Load(); hits != 0 {
		testInstance.Errorf("redirect target was hit %d times, want 0", hits)
	}
}

func TestClientDoesNotFollowRedirectToHTTPOrInternalTargets(testInstance *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		internalHits.Add(1)
	}))
	testInstance.Cleanup(internal.Close)

	for _, location := range []string{internal.URL, "http://169.254.169.254/latest/meta-data/"} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, location, http.StatusTemporaryRedirect)
		}))
		client := newLoopbackClient(testInstance, server, Config{})
		response, err := client.Post(server.URL, "application/json", strings.NewReader("{}"))
		if err != nil {
			server.Close()
			testInstance.Fatalf("Post: %v", err)
		}
		if response.StatusCode != http.StatusTemporaryRedirect {
			testInstance.Errorf("status = %d, want 307 returned unfollowed", response.StatusCode)
		}
		_ = response.Body.Close()
		server.Close()
	}
	if hits := internalHits.Load(); hits != 0 {
		testInstance.Errorf("internal redirect target was hit %d times, want 0", hits)
	}
}

func TestClientRejectsPlainHTTPWithoutDialing(testInstance *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	testInstance.Cleanup(server.Close)

	var dials atomic.Int32
	client := New(Config{AllowAddr: func(netip.Addr) bool {
		dials.Add(1)
		return true
	}})
	_, err := client.Get(server.URL)
	if !errors.Is(err, ErrNotHTTPS) {
		testInstance.Fatalf("err = %v, want ErrNotHTTPS", err)
	}
	if !IsBlocked(err) {
		testInstance.Errorf("IsBlocked(%v) = false, want true", err)
	}
	if dials.Load() != 0 || hits.Load() != 0 {
		testInstance.Errorf("plain http made contact: dials=%d hits=%d, want 0", dials.Load(), hits.Load())
	}
}

func TestClientRejectsNonHTTPSchemes(testInstance *testing.T) {
	client := New(Config{})
	for _, target := range []string{"ftp://example.com/", "file:///etc/passwd", "gopher://example.com/"} {
		_, err := client.Get(target)
		if !errors.Is(err, ErrNotHTTPS) {
			testInstance.Errorf("Get(%q) err = %v, want ErrNotHTTPS", target, err)
		}
		if !IsBlocked(err) {
			testInstance.Errorf("IsBlocked(%v) = false for %q, want true", err, target)
		}
	}
}

func TestDefaultConfigBlocksLoopbackTLSServer(testInstance *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	testInstance.Cleanup(server.Close)

	// Trust the test certificate but keep the default address policy.
	client := New(Config{TLSConfig: trustedTLSConfig(testInstance, server)})
	_, err := client.Get(server.URL)
	if err == nil {
		testInstance.Fatal("Get against loopback with the default policy succeeded, want blocked")
	}
	if !IsBlocked(err) {
		testInstance.Errorf("IsBlocked(%v) = false, want true", err)
	}
	if hits.Load() != 0 {
		testInstance.Errorf("blocked request still reached the server %d times", hits.Load())
	}
}

func TestDefaultConfigBlocksDangerousTargets(testInstance *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	testInstance.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		testInstance.Fatalf("split port: %v", err)
	}
	cases := map[string]string{
		"cloud metadata":       "https://169.254.169.254/latest/meta-data/",
		"metadata odd port":    "https://169.254.169.254:8443/",
		"private":              "https://10.0.0.5/",
		"tailnet CGNAT":        "https://100.100.100.100/",
		"loopback literal":     "https://127.0.0.1:" + port + "/",
		"ipv6 loopback":        "https://[::1]:" + port + "/",
		"v4-mapped loopback":   "https://[::ffff:127.0.0.1]:" + port + "/",
		"v4-mapped metadata":   "https://[::ffff:169.254.169.254]/",
		"ipv6 link-local":      "https://[fe80::1]/",
		"hostname to loopback": "https://localhost:" + port + "/",
		"unspecified":          "https://0.0.0.0:" + port + "/",
	}
	needsIPv6 := map[string]bool{
		"ipv6 loopback":      true,
		"v4-mapped loopback": true,
		"v4-mapped metadata": true,
		"ipv6 link-local":    true,
	}
	client := New(Config{})
	for name, target := range cases {
		testInstance.Run(name, func(testInstance *testing.T) {
			if needsIPv6[name] && !ipv6Available() {
				testInstance.Skip("IPv6 is not available in this environment")
			}
			if name == "hostname to loopback" && !localhostResolvesToLoopback() {
				testInstance.Skip("localhost does not resolve to a loopback address in this environment")
			}
			_, err := client.Get(target)
			if err == nil {
				testInstance.Fatalf("Get(%s) succeeded, want blocked", target)
			}
			if !IsBlocked(err) {
				testInstance.Errorf("IsBlocked(%v) = false, want true", err)
			}
		})
	}
}

// ipv6Available reports whether this host can listen on the IPv6 loopback address.
func ipv6Available() bool {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// localhostResolvesToLoopback reports whether the system resolver maps "localhost" to a loopback address.
func localhostResolvesToLoopback() bool {
	addresses, err := net.LookupIP("localhost")
	if err != nil {
		return false
	}
	for _, address := range addresses {
		if address.IsLoopback() {
			return true
		}
	}
	return false
}

func TestNumericHostFormsNeverReachLoopback(testInstance *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	testInstance.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		testInstance.Fatalf("split port: %v", err)
	}

	// Hex, decimal and octal spellings of 127.0.0.1. Depending on the resolver they are blocked or fail to
	// parse; either way the request must error and never reach the loopback server.
	client := New(Config{TLSConfig: trustedTLSConfig(testInstance, server)})
	for _, host := range []string{"0x7f.1", "2130706433", "017700000001"} {
		testInstance.Run(host, func(testInstance *testing.T) {
			if _, err := client.Get("https://" + host + ":" + port + "/"); err == nil {
				testInstance.Errorf("Get with host %q succeeded, want an error", host)
			}
		})
	}
	if count := hits.Load(); count != 0 {
		testInstance.Errorf("loopback server was reached %d times via numeric host forms, want 0", count)
	}
}

func TestAllowAddrReceivesTheResolvedAddress(testInstance *testing.T) {
	if !localhostResolvesToLoopback() {
		testInstance.Skip("localhost does not resolve to a loopback address in this environment")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	testInstance.Cleanup(server.Close)

	// The httptest certificate is issued for example.com, so verify against that name while dialling localhost.
	tlsConfig := trustedTLSConfig(testInstance, server).Clone()
	tlsConfig.ServerName = "example.com"

	var seen atomic.Value
	client := New(Config{
		TLSConfig: tlsConfig,
		AllowAddr: func(addr netip.Addr) bool {
			seen.Store(addr)
			return true
		},
	})
	// "localhost" is a hostname: the hook must see the resolved IP, not the name.
	target := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	response, err := client.Get(target)
	if err != nil {
		testInstance.Fatalf("Get: %v", err)
	}
	_ = response.Body.Close()
	addr, ok := seen.Load().(netip.Addr)
	if !ok || !addr.IsValid() {
		testInstance.Fatalf("AllowAddr was not called with a parsed address: %v", seen.Load())
	}
	if !addr.Unmap().IsLoopback() {
		testInstance.Errorf("AllowAddr saw %s, want a loopback IP", addr)
	}
}

func TestClientDefaults(testInstance *testing.T) {
	testInstance.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	testInstance.Setenv("https_proxy", "http://127.0.0.1:1")

	client := New(Config{})
	if client.Timeout != 9*time.Second {
		testInstance.Errorf("Timeout = %v, want 9s", client.Timeout)
	}
	guarded, ok := client.Transport.(*httpsOnly)
	if !ok {
		testInstance.Fatalf("Transport is %T, want *httpsOnly", client.Transport)
	}
	transport, ok := guarded.next.(*http.Transport)
	if !ok {
		testInstance.Fatalf("inner transport is %T, want *http.Transport", guarded.next)
	}
	if transport.Proxy != nil {
		testInstance.Error("Proxy must be nil so HTTPS_PROXY is ignored")
	}
	if transport.TLSHandshakeTimeout != 5*time.Second {
		testInstance.Errorf("TLSHandshakeTimeout = %v, want 5s", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != 8*time.Second {
		testInstance.Errorf("ResponseHeaderTimeout = %v, want 8s", transport.ResponseHeaderTimeout)
	}
	if transport.MaxResponseHeaderBytes != 16<<10 {
		testInstance.Errorf("MaxResponseHeaderBytes = %d, want %d", transport.MaxResponseHeaderBytes, 16<<10)
	}
	if !transport.DisableCompression {
		testInstance.Error("DisableCompression must be true so a hostile server cannot inflate a response")
	}
	if !transport.ForceAttemptHTTP2 {
		testInstance.Error("ForceAttemptHTTP2 must be true because the custom dialer would otherwise disable HTTP/2")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		testInstance.Error("default TLS config must enforce at least TLS 1.2")
	}
	if client.CheckRedirect == nil {
		testInstance.Fatal("CheckRedirect must be set")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		testInstance.Errorf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
}

func TestReadSnippetCapsAt1024Bytes(testInstance *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("a", 64<<10)))
	}))
	testInstance.Cleanup(server.Close)

	client := newLoopbackClient(testInstance, server, Config{})
	response, err := client.Get(server.URL)
	if err != nil {
		testInstance.Fatalf("Get: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if got := ReadSnippet(response.Body); len(got) != 1024 {
		testInstance.Errorf("len(snippet) = %d, want 1024", len(got))
	}
}

type failingReader struct{ prefix string }

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if reader.prefix == "" {
		return 0, errors.New("connection reset")
	}
	count := copy(buffer, reader.prefix)
	reader.prefix = reader.prefix[count:]
	return count, nil
}

func TestReadSnippetSwallowsReadErrors(testInstance *testing.T) {
	if got := ReadSnippet(&failingReader{prefix: "partial"}); got != "partial" {
		testInstance.Errorf("snippet = %q, want the bytes read before the error", got)
	}
	if got := ReadSnippet(&failingReader{}); got != "" {
		testInstance.Errorf("snippet = %q, want empty", got)
	}
}

func TestReadSnippetIsSafeToStoreAsText(testInstance *testing.T) {
	// Postgres text rejects NUL bytes and invalid UTF-8; a hostile response must not break the log insert.
	input := "ok\x00bad\xff\xfeend"
	got := ReadSnippet(strings.NewReader(input))
	if strings.ContainsRune(got, 0) {
		testInstance.Errorf("snippet %q still contains a NUL byte", got)
	}
	if got != strings.ToValidUTF8(got, "") {
		testInstance.Errorf("snippet %q is not valid UTF-8", got)
	}
	if !strings.HasPrefix(got, "ok") || !strings.HasSuffix(got, "end") {
		testInstance.Errorf("snippet %q lost its valid content", got)
	}
}

func TestClientTimeoutIsNotBlocked(testInstance *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-release:
		case <-request.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	testInstance.Cleanup(server.Close)
	testInstance.Cleanup(func() { close(release) })

	client := newLoopbackClient(testInstance, server, Config{Timeout: 200 * time.Millisecond})
	_, err := client.Get(server.URL)
	if err == nil {
		testInstance.Fatal("Get succeeded, want a timeout")
	}
	var timeoutErr interface{ Timeout() bool }
	if !errors.As(err, &timeoutErr) || !timeoutErr.Timeout() {
		testInstance.Errorf("err = %v, want a timeout error", err)
	}
	if IsBlocked(err) {
		testInstance.Errorf("IsBlocked(%v) = true for a timeout, want false (it is retryable)", err)
	}
}

func TestClientBodyReadTimeout(testInstance *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("x"))
		writer.(http.Flusher).Flush()
		select {
		case <-release:
		case <-request.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	testInstance.Cleanup(server.Close)
	testInstance.Cleanup(func() { close(release) })

	client := newLoopbackClient(testInstance, server, Config{Timeout: 300 * time.Millisecond})
	response, err := client.Get(server.URL)
	if err != nil {
		testInstance.Fatalf("Get: %v, want headers to arrive before the timeout", err)
	}
	defer func() { _ = response.Body.Close() }()

	start := time.Now()
	_, err = io.ReadAll(response.Body)
	if err == nil {
		testInstance.Fatal("ReadAll succeeded, want a body-read timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		testInstance.Errorf("ReadAll took %v, want it bounded by the client timeout", elapsed)
	}
	var timeoutErr interface{ Timeout() bool }
	if !errors.As(err, &timeoutErr) || !timeoutErr.Timeout() {
		testInstance.Errorf("err = %v, want a timeout error", err)
	}
}

func TestCause(testInstance *testing.T) {
	testInstance.Run("strips the request URL from a real failure", func(testInstance *testing.T) {
		client := New(Config{})
		_, err := client.Get("https://169.254.169.254/secret-token-path?key=abc")
		if err == nil {
			testInstance.Fatal("Get succeeded, want blocked")
		}
		if !strings.Contains(err.Error(), "secret-token-path") {
			testInstance.Fatalf("precondition: raw error %q should embed the URL", err)
		}
		cause := Cause(err)
		if strings.Contains(cause.Error(), "secret-token-path") || strings.Contains(cause.Error(), "key=abc") {
			testInstance.Errorf("Cause(err) = %q still leaks the URL", cause)
		}
		if !IsBlocked(cause) {
			testInstance.Errorf("IsBlocked(Cause(err)) = false for %v, want true", cause)
		}
	})
	testInstance.Run("strips the request URL from a constructed url.Error", func(testInstance *testing.T) {
		err := &url.Error{Op: "Post", URL: "https://hooks.example/secret-token-path?key=abc", Err: errors.New("refused")}
		cause := Cause(err)
		if strings.Contains(cause.Error(), "secret-token-path") || strings.Contains(cause.Error(), "key=abc") {
			testInstance.Errorf("Cause(err) = %q still leaks the URL", cause)
		}
	})
	testInstance.Run("nil in nil out", func(testInstance *testing.T) {
		if got := Cause(nil); got != nil {
			testInstance.Errorf("Cause(nil) = %v, want nil", got)
		}
	})
	testInstance.Run("non-url error passes through", func(testInstance *testing.T) {
		original := errors.New("plain failure")
		if got := Cause(original); got != original {
			testInstance.Errorf("Cause(%v) = %v, want the same error", original, got)
		}
	})
	testInstance.Run("wrapped url error is unwrapped", func(testInstance *testing.T) {
		inner := errors.New("refused")
		err := fmt.Errorf("send: %w", &url.Error{Op: "Post", URL: "https://x/secret", Err: inner})
		if got := Cause(err); got != inner {
			testInstance.Errorf("Cause(%v) = %v, want the inner cause", err, got)
		}
	})
}

func TestControlForRejectsMalformedAddresses(testInstance *testing.T) {
	allowEverything := func(netip.Addr) bool { return true }
	control := controlFor(allowEverything)
	for _, address := range []string{"", "garbage", "127.0.0.1", "/tmp/x.sock", "example.com:443", "[::1"} {
		testInstance.Run("malformed "+address, func(testInstance *testing.T) {
			if err := control("tcp", address, nil); !errors.Is(err, ErrBlockedAddress) {
				testInstance.Errorf("control(%q) = %v, want ErrBlockedAddress", address, err)
			}
		})
	}
}

func TestControlForAppliesTheAddressPolicy(testInstance *testing.T) {
	control := controlFor(IsPublicAddr)
	cases := []struct {
		address string
		blocked bool
	}{
		{"8.8.8.8:443", false},
		{"[::ffff:127.0.0.1]:443", true},
		{"169.254.169.254:443", true},
		{"[fe80::1%eth0]:443", true},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.address, func(testInstance *testing.T) {
			err := control("tcp", testCase.address, nil)
			if testCase.blocked && !errors.Is(err, ErrBlockedAddress) {
				testInstance.Errorf("control(%q) = %v, want ErrBlockedAddress", testCase.address, err)
			}
			if !testCase.blocked && err != nil {
				testInstance.Errorf("control(%q) = %v, want nil", testCase.address, err)
			}
		})
	}
}

func TestIsBlockedUnwrapsTransportErrors(testInstance *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain blocked", ErrBlockedAddress, true},
		{"plain not https", ErrNotHTTPS, true},
		{"wrapped with %w", fmt.Errorf("send: %w", ErrBlockedAddress), true},
		{
			"url error around op error",
			&url.Error{Op: "Post", URL: "https://x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: ErrBlockedAddress}},
			true,
		},
		{"url error around not https", &url.Error{Op: "Get", URL: "http://x", Err: ErrNotHTTPS}, true},
		{"unrelated error", errors.New("connection refused"), false},
		{
			"op error with other cause",
			&url.Error{Op: "Post", URL: "https://x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}},
			false,
		},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			if got := IsBlocked(testCase.err); got != testCase.want {
				testInstance.Errorf("IsBlocked(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
		})
	}
}

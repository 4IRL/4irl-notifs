package transport

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// MaxResponseSnippetBytes caps how much of a response body is ever read back for logging.
const MaxResponseSnippetBytes = 1024

const (
	defaultDialTimeout           = 5 * time.Second
	defaultTLSHandshakeTimeout   = 5 * time.Second
	defaultResponseHeaderTimeout = 8 * time.Second
	// defaultTimeout bounds the whole exchange, body read included; it stays under the 10 s shutdown grace.
	defaultTimeout = 9 * time.Second

	maxResponseHeaderBytes = 16 << 10
)

// Config tunes the client. Zero values take the defaults documented on each field.
type Config struct {
	// DialTimeout bounds one connection attempt (default 5 s).
	DialTimeout time.Duration
	// TLSHandshakeTimeout bounds the TLS handshake (default 5 s).
	TLSHandshakeTimeout time.Duration
	// ResponseHeaderTimeout bounds the wait for response headers after the request is written (default 8 s).
	ResponseHeaderTimeout time.Duration
	// Timeout bounds the whole exchange including reading the body (default 9 s).
	Timeout time.Duration
	// AllowAddr decides whether a resolved connection target may be dialled (default IsPublicAddr).
	// WARNING: setting it replaces the SSRF guard's address policy, so any address it accepts can be
	// reached. Only tests (to reach httptest servers on loopback) may set it; production code must not.
	AllowAddr func(netip.Addr) bool
	// TLSConfig overrides the TLS settings (default: TLS 1.2 minimum with system roots).
	// WARNING: setting it can weaken certificate verification and the TLS version floor. Only tests
	// (to trust an httptest certificate) may set it; production code must not.
	TLSConfig *tls.Config
}

// httpsOnly rejects every non-https request before it can reach the underlying transport.
type httpsOnly struct {
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (guard *httpsOnly) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || request.URL.Scheme != "https" {
		// The RoundTripper contract requires the body to be closed even on error.
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, ErrNotHTTPS
	}
	return guard.next.RoundTrip(request)
}

// New builds an HTTP client that only speaks https to public addresses.
//
// The address check runs in net.Dialer.Control, on the IP the dialer has already resolved and is about
// to connect to, so it covers every connection attempt and is immune to DNS rebinding (there is no
// separate lookup an attacker could answer differently). The client ignores proxy environment variables,
// never follows redirects, and bounds every phase of the exchange.
//
// Caller contract:
//   - Build ONE client per process and reuse it: each New allocates its own transport and idle-connection pool.
//   - Close the response body on every status, including 3xx: redirects are returned unfollowed with an open body.
//   - Read bodies only through ReadSnippet and never drain them.
//   - Never log the raw error: it embeds the request URL. Log or store Cause(err) instead.
func New(config Config) *http.Client {
	dialTimeout := durationOr(config.DialTimeout, defaultDialTimeout)
	tlsHandshakeTimeout := durationOr(config.TLSHandshakeTimeout, defaultTLSHandshakeTimeout)
	responseHeaderTimeout := durationOr(config.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	overallTimeout := durationOr(config.Timeout, defaultTimeout)

	allowAddr := config.AllowAddr
	if allowAddr == nil {
		allowAddr = IsPublicAddr
	}

	var tlsConfig *tls.Config
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
	} else {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	dialer := &net.Dialer{
		Timeout: dialTimeout,
		Control: controlFor(allowAddr),
	}

	transport := &http.Transport{
		Proxy:             nil,
		DialContext:       dialer.DialContext,
		ForceAttemptHTTP2: true,
		// A hostile server must not be able to inflate a small response into a huge one.
		DisableCompression:     true,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    tlsHandshakeTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
	}

	return &http.Client{
		Transport: &httpsOnly{next: transport},
		Timeout:   overallTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// controlFor builds the net.Dialer.Control hook that vets the already-resolved dial target.
// It fails closed: anything that is not a "<ip>:<port>" literal accepted by allowAddr is blocked.
func controlFor(allowAddr func(netip.Addr) bool) func(network, address string, rawConn syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrBlockedAddress
		}
		addr, err := netip.ParseAddr(host)
		if err != nil || !allowAddr(addr) {
			return ErrBlockedAddress
		}
		return nil
	}
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// ReadSnippet reads at most MaxResponseSnippetBytes from body for logging and swallows read errors,
// returning whatever was read. The result is safe to store in a Postgres text column: invalid UTF-8
// and NUL bytes are removed.
func ReadSnippet(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, MaxResponseSnippetBytes))
	snippet := strings.ToValidUTF8(string(data), "")
	return strings.ReplaceAll(snippet, "\x00", "")
}

// IsBlocked reports whether err means the request was refused by the SSRF guard (blocked address or
// non-https URL), as opposed to a transient network failure. It sees through *url.Error and *net.OpError.
func IsBlocked(err error) bool {
	return errors.Is(err, ErrBlockedAddress) || errors.Is(err, ErrNotHTTPS)
}

// Cause strips the *url.Error wrapper from err and returns the underlying error, or err unchanged when it
// is not a *url.Error (nil stays nil). A *url.Error embeds the full request URL, and webhook URLs commonly
// carry secrets in the path or query, so sinks must log or store Cause(err) and never the raw error.
func Cause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

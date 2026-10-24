package heartbeat

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

// ProbeBodyLimitBytes bounds how much of a response body a body match reads.
const ProbeBodyLimitBytes = 64 << 10

const (
	probeUserAgent = "4irl-delivery-probe/1"

	headerAccessClientID     = "CF-Access-Client-Id"
	headerAccessClientSecret = "CF-Access-Client-Secret"

	detailBodyMismatch    = "response body did not contain the expected text"
	detailBlocked         = "destination address is not allowed"
	detailScheme          = "URL is not HTTPS"
	detailTLS             = "TLS handshake or certificate verification failed"
	detailDNS             = "DNS lookup failed"
	detailConnection      = "connection failed"
	detailBodyRead        = "response body could not be read"
	detailRequestBuild    = "request could not be built"
	detailCredentialOpen  = "access credential could not be opened"
	detailCredentialShape = "access credential is invalid"
)

// runProbe makes one request for claim and maps the outcome to a ProbeResult. It never returns an
// error: anything it cannot classify is a network failure, never a success. Details are fixed text
// and never carry the URL, an error string or a credential.
func runProbe(ctx context.Context, client *http.Client, sealer *seal.Sealer, claim ProbeClaim) ProbeResult {
	config := claim.Config

	var token *AccessToken
	if claim.SealedAccessToken != nil {
		opened, openErr := OpenProbeAccessToken(sealer, claim.CheckID, claim.SealedAccessToken)
		if openErr != nil {
			return failedProbe(ReasonConfig, nil, detailCredentialOpen)
		}
		if ValidateAccessToken(opened) != nil {
			return failedProbe(ReasonConfig, nil, detailCredentialShape)
		}
		token = &opened
	}

	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	request, buildErr := http.NewRequestWithContext(probeCtx, http.MethodGet, config.URL, nil)
	if buildErr != nil {
		return failedProbe(ReasonNetwork, nil, detailRequestBuild)
	}
	request.Header.Set("User-Agent", probeUserAgent)
	if token != nil {
		request.Header.Set(headerAccessClientID, token.ClientID)
		request.Header.Set(headerAccessClientSecret, token.ClientSecret)
	}

	response, requestErr := client.Do(request)
	if requestErr != nil {
		return classifyProbeError(requestErr, config.TimeoutSeconds)
	}
	defer func() { _ = response.Body.Close() }()

	status := response.StatusCode
	if !slices.Contains(config.ExpectedStatuses, status) {
		return failedProbe(ReasonHTTPStatus, &status, fmt.Sprintf("HTTP %d", status))
	}
	if config.BodyMatch == nil {
		return ProbeResult{Status: &status}
	}

	body, readErr := io.ReadAll(io.LimitReader(response.Body, ProbeBodyLimitBytes))
	if readErr != nil {
		if probeCtx.Err() != nil || isTimeoutError(readErr) {
			return failedProbe(ReasonTimeout, &status, timeoutDetail(config.TimeoutSeconds))
		}
		return failedProbe(ReasonNetwork, &status, detailBodyRead)
	}
	if !bytes.Contains(body, []byte(*config.BodyMatch)) {
		return failedProbe(ReasonBodyMismatch, &status, detailBodyMismatch)
	}
	return ProbeResult{Status: &status}
}

func failedProbe(reason string, status *int, detail string) ProbeResult {
	return ProbeResult{Status: status, Reason: &reason, Detail: detail}
}

func timeoutDetail(timeoutSeconds int) string {
	return fmt.Sprintf("timed out after %ds", timeoutSeconds)
}

func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isTLSError(err error) bool {
	var verifyErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	var alertErr tls.AlertError
	return errors.As(err, &verifyErr) ||
		errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameErr) ||
		errors.As(err, &invalidCert) ||
		errors.As(err, &recordHeader) ||
		errors.As(err, &alertErr)
}

// classifyProbeError maps a transport error to a failed ProbeResult, in the pinned order. Anything
// unrecognised is a network failure.
func classifyProbeError(err error, timeoutSeconds int) ProbeResult {
	switch {
	case errors.Is(err, transport.ErrNotHTTPS):
		return failedProbe(ReasonScheme, nil, detailScheme)
	case errors.Is(err, transport.ErrBlockedAddress):
		return failedProbe(ReasonBlocked, nil, detailBlocked)
	case isTLSError(err):
		return failedProbe(ReasonTLS, nil, detailTLS)
	case isTimeoutError(err):
		return failedProbe(ReasonTimeout, nil, timeoutDetail(timeoutSeconds))
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return failedProbe(ReasonNetwork, nil, detailDNS)
	}
	return failedProbe(ReasonNetwork, nil, detailConnection)
}

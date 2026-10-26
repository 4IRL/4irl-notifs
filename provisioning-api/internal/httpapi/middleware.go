package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/enforcement"
)

const (
	// internalCredentialHeader carries the shared secret that proves a call
	// came from a trusted internal caller (delivery-api). Its value is never
	// logged, echoed or included in an error.
	internalCredentialHeader = "X-Notifs-Internal-Credential"

	// Rejection reasons logged by the credential middleware.
	credentialReasonMissing       = "missing"
	credentialReasonMismatch      = "mismatch"
	credentialReasonNotConfigured = "not_configured"
)

// requireInternalCredential wraps next with the internal-credential check. It
// exempts exactly GET /healthz so the uptime monitors keep working. A request
// fails the check when no credential is configured (not_configured), the
// header is absent (missing), or the header differs from the configured value
// (mismatch). Enforce mode answers 401 and never reaches next; log-only mode
// logs a WARN with the reason, method and path and lets the request proceed.
// The Go zero value of Mode is Enforce, so any mode other than LogOnly rejects.
func (server *Server) requireInternalCredential(next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
			next.ServeHTTP(responseWriter, request)
			return
		}

		reason := server.credentialRejectionReason(request)
		if reason == "" {
			next.ServeHTTP(responseWriter, request)
			return
		}

		if server.credentialMode == enforcement.LogOnly {
			server.logger.Warn("internal credential would reject",
				"reason", reason, "method", request.Method, "path", request.URL.Path)
			next.ServeHTTP(responseWriter, request)
			return
		}

		server.logger.Warn("internal credential rejected",
			"reason", reason, "method", request.Method, "path", request.URL.Path)
		writeJSON(responseWriter, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	})
}

// credentialRejectionReason returns why the request fails the credential check,
// or "" when it carries the exact configured credential. It fails closed: no
// configured credential rejects every request. Both values are hashed before
// the constant-time compare so neither the content nor the length of the
// configured credential leaks through timing.
func (server *Server) credentialRejectionReason(request *http.Request) string {
	if server.internalCredential == "" {
		return credentialReasonNotConfigured
	}
	presented := request.Header.Get(internalCredentialHeader)
	if presented == "" {
		return credentialReasonMissing
	}
	presentedDigest := sha256.Sum256([]byte(presented))
	expectedDigest := sha256.Sum256([]byte(server.internalCredential))
	if subtle.ConstantTimeCompare(presentedDigest[:], expectedDigest[:]) != 1 {
		return credentialReasonMismatch
	}
	return ""
}

// statusRecorder remembers the status code a handler wrote so the completion
// log can report it.
type statusRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
}

// WriteHeader records the first status code written and forwards it.
func (recorder *statusRecorder) WriteHeader(statusCode int) {
	if !recorder.wroteHeader {
		recorder.statusCode = statusCode
		recorder.wroteHeader = true
	}
	recorder.ResponseWriter.WriteHeader(statusCode)
}

// Write records an implicit 200 when no header was written yet, then forwards.
func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if !recorder.wroteHeader {
		recorder.statusCode = http.StatusOK
		recorder.wroteHeader = true
	}
	return recorder.ResponseWriter.Write(body)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}

// logCompletion wraps next with a per-request completion log line carrying only
// method, path, status and duration: never headers, bodies or query values.
func (server *Server) logCompletion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		recorder := &statusRecorder{ResponseWriter: responseWriter, statusCode: http.StatusOK}
		next.ServeHTTP(recorder, request)
		// Docker's healthcheck and the watchdog probe /healthz constantly; a
		// successful probe is noise, a failing one is still logged.
		if request.Method == http.MethodGet && request.URL.Path == "/healthz" &&
			recorder.statusCode >= http.StatusOK && recorder.statusCode < http.StatusMultipleChoices {
			return
		}
		server.logger.Info("request completed",
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.statusCode,
			"duration_ms", float64(time.Since(startedAt))/float64(time.Millisecond),
		)
	})
}

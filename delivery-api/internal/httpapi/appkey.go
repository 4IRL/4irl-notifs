package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
)

// Error messages returned by the app-key middleware.
const (
	errMsgAppKeyRequired = "app key required"
	errMsgInvalidAppKey  = "invalid app key"
	errMsgAppIDMismatch  = "app_id does not match app key"
)

const (
	// authorizationHeader carries the app key as `Bearer dak_...`.
	authorizationHeader = "Authorization"
	// bearerScheme is the one accepted Authorization scheme.
	bearerScheme = "Bearer"
	// wwwAuthenticateHeader names the scheme a 401 asks the caller to use.
	wwwAuthenticateHeader = "WWW-Authenticate"

	// appKeyLookupTimeout bounds the key lookup, so a hung identity store becomes a logged 503
	// instead of a request that hangs with no log line naming the step.
	appKeyLookupTimeout = 3 * time.Second
	// maxLoggedAppIDChars caps a caller-asserted app_id in a log line.
	maxLoggedAppIDChars = 64
)

// appKeyContextKey is the context key under which requireAppKey stores the caller's app id.
type appKeyContextKey struct{}

// callerAppID returns the app id the request's app key resolved to. It is empty when the
// request did not pass through requireAppKey.
func callerAppID(request *http.Request) string {
	appID, _ := request.Context().Value(appKeyContextKey{}).(string)
	return appID
}

// assertedAppIDReader reads the app_id a request asserts about itself: from the query, the
// path or the body, depending on the route. It returns an empty string when none is present
// (not a mismatch: the route's own validation owns that case). ok is false when it already
// wrote the response (an oversized or unreadable body) and the request must stop.
type assertedAppIDReader func(responseWriter http.ResponseWriter, request *http.Request) (appID string, ok bool)

// assertedAppIDFromQuery reads the ?app_id= query of the message status and cancel routes.
func assertedAppIDFromQuery(_ http.ResponseWriter, request *http.Request) (string, bool) {
	return request.URL.Query().Get(queryAppID), true
}

// assertedAppIDFromPath reads the {app_id} path value of the heartbeat ping routes.
func assertedAppIDFromPath(_ http.ResponseWriter, request *http.Request) (string, bool) {
	return request.PathValue(pathValueAppID), true
}

// assertedAppIDFromBody reads the "app_id" of a POST /v1/messages body. It is the only covered
// route whose asserted app_id is in the body, so it is the only one that reads it.
//
// It reads the body under its own maxMessageBodyBytes cap and is the sole 413 authority for the
// route: an oversized body is answered here, with the status and body decodeJSON writes, and the
// handler is never called. An under-cap body is put back as a fresh reader over the buffered
// bytes, so the handler's own decodeJSON sees it unchanged: a malformed body within the cap
// yields no asserted app_id here and the handler's usual 400.
//
// The minimal decode uses encoding/json on the same "app_id" tag the handler's body uses, so a
// duplicate or differently cased key resolves to the same value in both. A body that is not valid
// JSON yields no value at all (json.Unmarshal validates before it decodes), never a partial one.
func assertedAppIDFromBody(responseWriter http.ResponseWriter, request *http.Request) (string, bool) {
	defer boundBodyRead(responseWriter)()

	raw, readErr := io.ReadAll(http.MaxBytesReader(responseWriter, request.Body, maxMessageBodyBytes))
	if readErr != nil {
		var tooLargeErr *http.MaxBytesError
		if errors.As(readErr, &tooLargeErr) {
			writeError(responseWriter, http.StatusRequestEntityTooLarge, errMsgBodyTooLarge)
		} else {
			// Same answer decodeJSON gives any other failed read.
			writeError(responseWriter, http.StatusBadRequest, errMsgInvalidJSONBody)
		}
		return "", false
	}
	request.Body = io.NopCloser(bytes.NewReader(raw))

	var asserted struct {
		AppID string `json:"app_id"`
	}
	if json.Unmarshal(raw, &asserted) != nil {
		return "", true
	}
	return asserted.AppID, true
}

// requireAppKey authenticates the caller by its delivery app key and confines it to its own
// app. It answers:
//   - 503 "identity unavailable" when no AppIdentity is configured or the key lookup itself
//     failed (fail closed: never a pass-through);
//   - 401 "app key required" (with WWW-Authenticate: Bearer) for a missing or malformed
//     `Authorization: Bearer dak_...`;
//   - 401 "invalid app key" for an unknown, revoked or past-grace key;
//   - 403 "app_id does not match app key" when the request asserts a non-empty app_id (read by
//     readAsserted) that differs from the key's. An absent or empty one is no assertion: the
//     request falls through to the handler's own validation, unchanged.
//
// On success the key's app id is in the request context (see callerAppID). Failures are logged
// with the reason, the key hint and the path, never the key.
func (server *Server) requireAppKey(readAsserted assertedAppIDReader, handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		if server.appIdentity == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgIdentityUnavailable)
			return
		}

		key, reason := bearerAppKey(request)
		if reason != "" {
			server.logger.Warn("app key rejected", "reason", reason, "path", request.URL.Path)
			writeUnauthorized(responseWriter, errMsgAppKeyRequired)
			return
		}

		lookupCtx, cancelLookup := context.WithTimeout(request.Context(), appKeyLookupTimeout)
		resolved, resolveErr := server.appIdentity.ResolveKey(lookupCtx, key)
		cancelLookup()
		if resolveErr != nil {
			server.writeAppKeyError(responseWriter, request, key, resolveErr)
			return
		}
		if resolved.AppID == "" {
			// Fail closed: a lookup that succeeds without naming an app authenticates nobody.
			server.logger.Error("app key lookup returned no app", "key_hint", resolved.Hint, "path", request.URL.Path)
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgIdentityUnavailable)
			return
		}

		asserted, ok := readAsserted(responseWriter, request)
		if !ok {
			return
		}
		if asserted != "" && asserted != resolved.AppID {
			server.logger.Warn("app key rejected", "reason", "app_id does not match app key",
				"key_hint", resolved.Hint, "key_app_id", resolved.AppID,
				"asserted_app_id", truncateForLog(asserted), "path", request.URL.Path)
			writeError(responseWriter, http.StatusForbidden, errMsgAppIDMismatch)
			return
		}

		ctx := context.WithValue(request.Context(), appKeyContextKey{}, resolved.AppID)
		handler(responseWriter, request.WithContext(ctx))
	}
}

// truncateForLog bounds a caller-controlled value to maxLoggedAppIDChars runes for a log line.
func truncateForLog(value string) string {
	runes := []rune(value)
	if len(runes) <= maxLoggedAppIDChars {
		return value
	}
	return string(runes[:maxLoggedAppIDChars])
}

// writeAppKeyError maps a failed key lookup: a key the store does not accept is 401, anything
// else (a database failure) is 503 so a broken identity store can never admit a caller.
func (server *Server) writeAppKeyError(responseWriter http.ResponseWriter, request *http.Request, key string, err error) {
	switch {
	case errors.Is(err, appidentity.ErrInvalidKey):
		server.logger.Warn("app key rejected", "reason", err.Error(),
			"key_hint", appidentity.KeyHint(key), "path", request.URL.Path)
		writeUnauthorized(responseWriter, errMsgInvalidAppKey)
	case errors.Is(err, context.Canceled):
		// The client went away: not a server fault, and a client that can disconnect at will
		// must not be able to flood the error log. The response is moot.
		server.logger.Debug("app key lookup ended early", "path", request.URL.Path)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgIdentityUnavailable)
	default:
		server.logger.Error("app key lookup failed", "error", err,
			"key_hint", appidentity.KeyHint(key), "path", request.URL.Path)
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgIdentityUnavailable)
	}
}

// writeUnauthorized answers 401 and names the Bearer scheme the caller must use.
func writeUnauthorized(responseWriter http.ResponseWriter, message string) {
	responseWriter.Header().Set(wwwAuthenticateHeader, bearerScheme)
	writeError(responseWriter, http.StatusUnauthorized, message)
}

// bearerAppKey extracts the app key from `Authorization: Bearer dak_...`. When there is none it
// returns an empty key and the reason, which is safe to log (it never contains the header).
func bearerAppKey(request *http.Request) (key string, rejectReason string) {
	header := request.Header.Get(authorizationHeader)
	if header == "" {
		return "", "missing Authorization header"
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return "", "Authorization header is not a Bearer token"
	}
	if !appidentity.WellFormedKey(token) {
		return "", "Bearer token is not a well-formed app key"
	}
	return token, ""
}

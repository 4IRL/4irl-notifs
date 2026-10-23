package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

const (
	// keyApp is the app the scripted key resolves to; otherApp is another app's id.
	keyApp   = "urls4irl"
	otherApp = "chores4irl"
	// bearerKey is the Authorization header value of a well-formed app key.
	bearerKey = "Bearer " + identityTestKey
)

// permissiveAppKeys returns an identity fake whose key resolves to whichever app the request
// at path with body asserts (the ?app_id= query, the heartbeat path segment, or the body's
// "app_id"; keyApp when it asserts none). It lets the pre-existing message and heartbeat
// tests, which were written before requireAppKey, keep exercising their own handler: the
// middleware authenticates them and never trips the app_id match. The middleware's own
// behaviour is covered below against explicitly scripted fakes.
func permissiveAppKeys(path string, body string) *fakeAppIdentity {
	return &fakeAppIdentity{resolved: appidentity.ResolvedKey{ID: 1, AppID: assertedAppForTest(path, body), Hint: "dak_abcd"}}
}

// authorizeAsApp gives request the well-formed app key header.
func authorizeAsApp(request *http.Request) {
	request.Header.Set(authorizationHeader, bearerKey)
}

// assertedAppForTest finds the app_id a request asserts, or keyApp when it asserts none.
func assertedAppForTest(path string, body string) string {
	if parsed, parseErr := url.Parse(path); parseErr == nil {
		if queried := parsed.Query().Get(queryAppID); queried != "" {
			return queried
		}
		if segments := strings.Split(strings.Trim(parsed.Path, "/"), "/"); len(segments) >= 3 && segments[1] == "heartbeats" {
			return segments[2]
		}
	}
	var asserted struct {
		AppID string `json:"app_id"`
	}
	if jsonErr := json.Unmarshal([]byte(body), &asserted); jsonErr == nil && asserted.AppID != "" {
		return asserted.AppID
	}
	return keyApp
}

// appKeyRoute is one route covered by requireAppKey. pathFormat and bodyFormat take the
// asserted app id as their one %s, so a test can assert the key's app or another one.
type appKeyRoute struct {
	name       string
	method     string
	pathFormat string
	bodyFormat string
	// source names where the route asserts its app_id, for failure messages only.
	source string
}

func (route appKeyRoute) path(appID string) string {
	if !strings.Contains(route.pathFormat, "%s") {
		return route.pathFormat
	}
	return fmt.Sprintf(route.pathFormat, appID)
}

func (route appKeyRoute) body(appID string) string {
	if route.bodyFormat == "" {
		return ""
	}
	return fmt.Sprintf(route.bodyFormat, appID)
}

func appKeyRoutes() []appKeyRoute {
	return []appKeyRoute{
		{
			name: "submit", method: http.MethodPost, pathFormat: "/v1/messages", source: "body",
			bodyFormat: `{"app_id":"%s","channel":"alerts","idempotency_key":"key-1","payload":{"a":1}}`,
		},
		{name: "get", method: http.MethodGet, pathFormat: "/v1/messages/" + sampleMessageID + "?app_id=%s", source: "query"},
		{name: "get by key", method: http.MethodGet, pathFormat: "/v1/messages?app_id=%s&idempotency_key=k", source: "query"},
		{name: "cancel", method: http.MethodDelete, pathFormat: "/v1/messages/" + sampleMessageID + "?app_id=%s", source: "query"},
		{name: "cancel by key", method: http.MethodDelete, pathFormat: "/v1/messages?app_id=%s&idempotency_key=k", source: "query"},
		{name: "ping", method: http.MethodPost, pathFormat: "/v1/heartbeats/%s/nightly_backup", source: "path"},
		{name: "fail ping", method: http.MethodPost, pathFormat: "/v1/heartbeats/%s/nightly_backup/fail", source: "path"},
	}
}

// appKeyRig is a Server wired to scripted fakes, with the logs captured.
type appKeyRig struct {
	messages   *fakeMessages
	heartbeats *fakeHeartbeats
	identity   *fakeAppIdentity
	logs       *bytes.Buffer
	server     *Server
}

// newAppKeyRig builds a Server whose identity fake resolves every key to keyApp (resolved)
// unless the test rescripts rig.identity before serving.
func newAppKeyRig() *appKeyRig {
	rig := &appKeyRig{
		messages:   &fakeMessages{submitResult: sampleStoredMessage(), getResult: sampleStoredMessage(), getByKeyResult: sampleStoredMessage()},
		heartbeats: allResults(sampleCheck()),
		identity:   &fakeAppIdentity{resolved: appidentity.ResolvedKey{ID: 4, AppID: keyApp, Hint: "dak_abcd"}},
		logs:       &bytes.Buffer{},
	}
	rig.rebuild()
	return rig
}

func (rig *appKeyRig) rebuild() {
	var identity AppIdentity
	if rig.identity != nil {
		identity = rig.identity
	}
	rig.server = NewServer(ServerConfig{
		Messages: rig.messages, Heartbeats: rig.heartbeats, AppIdentity: identity,
		Logger: slog.New(slog.NewTextHandler(rig.logs, nil)),
	})
}

func (rig *appKeyRig) serve(method string, path string, body string, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		request.Header.Set(authorizationHeader, authorization)
	}
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func (rig *appKeyRig) handlerCalls() int {
	return rig.messages.totalCalls() + rig.heartbeats.totalCalls()
}

func (rig *appKeyRig) assertNoHandlerCall(testInstance *testing.T) {
	testInstance.Helper()
	if rig.handlerCalls() != 0 {
		testInstance.Fatalf("a service was called (messages %d, heartbeats %d) for a request the key check refused",
			rig.messages.totalCalls(), rig.heartbeats.totalCalls())
	}
}

func assertUnauthorized(testInstance *testing.T, recorder *httptest.ResponseRecorder, wantMessage string) {
	testInstance.Helper()
	assertResponse(testInstance, recorder, http.StatusUnauthorized, `{"error":"`+wantMessage+`"}`+"\n")
	if challenge := recorder.Header().Get("WWW-Authenticate"); challenge != "Bearer" {
		testInstance.Fatalf("WWW-Authenticate = %q, want Bearer", challenge)
	}
}

func TestAppKeyRoutesRequireABearerKey(testInstance *testing.T) {
	authorizations := []struct{ name, header string }{
		{name: "no header", header: ""},
		{name: "wrong scheme", header: "Basic " + identityTestKey},
		{name: "scheme only", header: "Bearer"},
		{name: "empty token", header: "Bearer "},
		{name: "not a delivery key", header: "Bearer tk_not_a_delivery_app_key"},
		{name: "too short", header: "Bearer dak_short"},
		{name: "too long", header: bearerKey + "x"},
		{name: "bad alphabet", header: "Bearer dak_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNO!?"},
		{name: "key without scheme", header: identityTestKey},
	}
	for _, route := range appKeyRoutes() {
		for _, authorization := range authorizations {
			testInstance.Run(route.name+"/"+authorization.name, func(subTest *testing.T) {
				rig := newAppKeyRig()

				recorder := rig.serve(route.method, route.path(keyApp), route.body(keyApp), authorization.header)

				assertUnauthorized(subTest, recorder, "app key required")
				rig.assertNoHandlerCall(subTest)
				if len(rig.identity.resolveCalls) != 0 {
					subTest.Fatalf("resolve calls = %v, want no store lookup for a header that is not a key", rig.identity.resolveCalls)
				}
			})
		}
	}
}

func TestAppKeySchemeIsCaseInsensitive(testInstance *testing.T) {
	rig := newAppKeyRig()

	recorder := rig.serve(http.MethodGet, "/v1/messages/"+sampleMessageID+"?app_id="+keyApp, "", "bearer "+identityTestKey)

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAppKeyRoutesRefuseAKeyTheStoreDoesNotAccept(testInstance *testing.T) {
	// The reason text names a hint (dak_wxyz) different from the presented key's own (dak_abcd),
	// so the two log fields can be asserted separately.
	reasons := []string{
		"unknown", "revoked (key dak_wxyz)", "past supersede grace (key dak_wxyz)", "malformed",
	}
	for _, route := range appKeyRoutes() {
		for _, reason := range reasons {
			testInstance.Run(route.name+"/"+reason, func(subTest *testing.T) {
				rig := newAppKeyRig()
				rig.identity.resolveErr = fmt.Errorf("%w: %s", appidentity.ErrInvalidKey, reason)

				recorder := rig.serve(route.method, route.path(keyApp), route.body(keyApp), bearerKey)

				assertUnauthorized(subTest, recorder, "invalid app key")
				rig.assertNoHandlerCall(subTest)
				if len(rig.identity.resolveCalls) != 1 || rig.identity.resolveCalls[0] != identityTestKey {
					subTest.Fatalf("resolve calls = %v, want one lookup of the presented key", rig.identity.resolveCalls)
				}
				logged := rig.logs.String()
				if !strings.Contains(logged, "app key rejected") || !strings.Contains(logged, "key_hint=dak_abcd") ||
					!strings.Contains(logged, "invalid app key: "+reason) {
					subTest.Fatalf("want the rejection logged with its reason and the key hint: %s", logged)
				}
				if strings.Contains(logged, identityTestKey) {
					subTest.Fatalf("the log must never carry the key: %s", logged)
				}
			})
		}
	}
}

func TestAppKeyRoutesFailClosedWhenTheKeyLookupFails(testInstance *testing.T) {
	failures := []struct {
		name     string
		err      error
		canceled bool
	}{
		{name: "database down", err: errors.New("look up app key: connection refused to 10.0.0.5:5432")},
		{name: "lookup deadline exceeded", err: fmt.Errorf("look up app key: %w", context.DeadlineExceeded)},
		{name: "client went away", err: fmt.Errorf("look up app key: %w", context.Canceled), canceled: true},
	}
	for _, route := range appKeyRoutes() {
		for _, failure := range failures {
			testInstance.Run(route.name+"/"+failure.name, func(subTest *testing.T) {
				rig := newAppKeyRig()
				rig.identity.resolveErr = failure.err

				recorder := rig.serve(route.method, route.path(keyApp), route.body(keyApp), bearerKey)

				assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
				if recorder.Header().Get("WWW-Authenticate") != "" {
					subTest.Fatalf("a store failure is not an authentication challenge")
				}
				rig.assertNoHandlerCall(subTest)
				if strings.Contains(recorder.Body.String(), "10.0.0.5") {
					subTest.Fatalf("the store error leaked into the response: %s", recorder.Body.String())
				}
				logged := rig.logs.String()
				if strings.Contains(logged, identityTestKey) {
					subTest.Fatalf("the log must never carry the key: %s", logged)
				}
				loggedAsError := strings.Contains(logged, "level=ERROR") && strings.Contains(logged, "app key lookup failed")
				if loggedAsError == failure.canceled {
					subTest.Fatalf("lookup failure logged as error = %v, want %v (a canceled request must not log at ERROR): %s",
						loggedAsError, !failure.canceled, logged)
				}
				if !failure.canceled && !strings.Contains(logged, "key_hint=dak_abcd") {
					subTest.Fatalf("want the failure logged with the key hint: %s", logged)
				}
			})
		}
	}
}

func TestAppKeyLookupThatNamesNoAppFailsClosed(testInstance *testing.T) {
	for _, route := range appKeyRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAppKeyRig()
			rig.identity.resolved = appidentity.ResolvedKey{}

			recorder := rig.serve(route.method, route.path(keyApp), route.body(keyApp), bearerKey)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
			rig.assertNoHandlerCall(subTest)
			if !strings.Contains(rig.logs.String(), "level=ERROR") {
				subTest.Fatalf("want the empty lookup logged as an error: %s", rig.logs.String())
			}
		})
	}
}

func TestAppKeyRoutesFailClosedWithoutAnIdentityService(testInstance *testing.T) {
	for _, route := range appKeyRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAppKeyRig()
			rig.identity = nil
			rig.rebuild()

			recorder := rig.serve(route.method, route.path(keyApp), route.body(keyApp), bearerKey)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
			rig.assertNoHandlerCall(subTest)
		})
	}
}

func TestAppKeyRoutesRefuseAnAssertedAppThatIsNotTheKeys(testInstance *testing.T) {
	for _, route := range appKeyRoutes() {
		testInstance.Run(route.name+" asserts the app in its "+route.source, func(subTest *testing.T) {
			rig := newAppKeyRig()

			recorder := rig.serve(route.method, route.path(otherApp), route.body(otherApp), bearerKey)

			assertResponse(subTest, recorder, http.StatusForbidden, `{"error":"app_id does not match app key"}`+"\n")
			rig.assertNoHandlerCall(subTest)
			logged := rig.logs.String()
			if !strings.Contains(logged, "does not match") || strings.Contains(logged, identityTestKey) ||
				!strings.Contains(logged, "asserted_app_id="+otherApp) || !strings.Contains(logged, "key_app_id="+keyApp) {
				subTest.Fatalf("want the mismatch logged with both app ids and without the key: %s", logged)
			}
		})
	}
}

func TestAppKeyRoutesPassTheKeysOwnAppThroughToTheHandler(testInstance *testing.T) {
	for _, route := range appKeyRoutes() {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAppKeyRig()

			recorder := rig.serve(route.method, route.path(keyApp), route.body(keyApp), bearerKey)

			if recorder.Code >= http.StatusBadRequest {
				subTest.Fatalf("status = %d, want the handler's success; body = %s", recorder.Code, recorder.Body.String())
			}
			if rig.handlerCalls() == 0 {
				subTest.Fatalf("the handler was never reached")
			}
		})
	}
}

func TestSubmitBodyIsRestoredForTheHandlerAfterTheKeyCheckReadsIt(testInstance *testing.T) {
	rig := newAppKeyRig()
	body := `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1","critical":true,"payload":{"nested":[1,2,3]}}`

	recorder := rig.serve(http.MethodPost, "/v1/messages", body, bearerKey)

	if recorder.Code != http.StatusAccepted {
		testInstance.Fatalf("status = %d, want 202; body = %s", recorder.Code, recorder.Body.String())
	}
	if len(rig.messages.submitCalls) != 1 {
		testInstance.Fatalf("submit calls = %d, want 1", len(rig.messages.submitCalls))
	}
	envelope := rig.messages.submitCalls[0]
	if envelope.AppID != keyApp || envelope.Channel != "alerts" || envelope.IdempotencyKey != "key-1" || !envelope.Critical ||
		string(envelope.Payload) != `{"nested":[1,2,3]}` {
		testInstance.Fatalf("envelope = %+v, want the whole body decoded by the handler after the key check read it", envelope)
	}
}

func TestSubmitWithoutAnAssertedAppFallsThroughToTheHandlersOwnValidation(testInstance *testing.T) {
	bodies := []struct{ name, body string }{
		{name: "absent", body: `{"channel":"alerts","idempotency_key":"key-1","payload":{}}`},
		{name: "empty", body: `{"app_id":"","channel":"alerts","idempotency_key":"key-1","payload":{}}`},
		{name: "null", body: `{"app_id":null,"channel":"alerts","idempotency_key":"key-1","payload":{}}`},
	}
	for _, testCase := range bodies {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newAppKeyRig()
			rig.messages.submitErr = &message.ValidationError{Message: "invalid app_id"}

			recorder := rig.serve(http.MethodPost, "/v1/messages", testCase.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid app_id"}`+"\n")
			if len(rig.messages.submitCalls) != 1 || rig.messages.submitCalls[0].AppID != "" {
				subTest.Fatalf("submit calls = %+v, want the handler reached once with no app_id (no mismatch, no substitution)",
					rig.messages.submitCalls)
			}
		})
	}
}

func TestQueryRoutesWithoutAnAssertedAppFallThroughToTheirOwn400(testInstance *testing.T) {
	testCases := []struct {
		name     string
		method   string
		path     string
		wantBody string
	}{
		{name: "get no query", method: http.MethodGet, path: "/v1/messages/" + sampleMessageID, wantBody: "app_id is required"},
		{name: "get empty app_id", method: http.MethodGet, path: "/v1/messages/" + sampleMessageID + "?app_id=", wantBody: "app_id is required"},
		{name: "cancel no query", method: http.MethodDelete, path: "/v1/messages/" + sampleMessageID, wantBody: "app_id is required"},
		{name: "get by key no app_id", method: http.MethodGet, path: "/v1/messages?idempotency_key=k", wantBody: "app_id and idempotency_key are required"},
		{name: "cancel by key empty app_id", method: http.MethodDelete, path: "/v1/messages?app_id=&idempotency_key=k", wantBody: "app_id and idempotency_key are required"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newAppKeyRig()

			recorder := rig.serve(testCase.method, testCase.path, "", bearerKey)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"`+testCase.wantBody+`"}`+"\n")
			rig.assertNoHandlerCall(subTest)
		})
	}
}

func TestSubmitWithAMalformedBodyWithinTheCapIsTheHandlers400(testInstance *testing.T) {
	bodies := []struct{ name, body string }{
		{name: "truncated", body: `{"app_id":"chores4irl","channel":`},
		{name: "trailing data after a mismatching app_id", body: `{"app_id":"chores4irl"} {}`},
		{name: "unknown field", body: `{"app_id":"urls4irl","bogus":1}`},
		{name: "wrong type", body: `{"app_id":5}`},
		{name: "not an object", body: `[1]`},
	}
	for _, testCase := range bodies {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newAppKeyRig()

			recorder := rig.serve(http.MethodPost, "/v1/messages", testCase.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
			rig.assertNoHandlerCall(subTest)
		})
	}
}

func TestSubmitBodyOverTheCapIsA413WrittenByTheKeyCheck(testInstance *testing.T) {
	rig := newAppKeyRig()
	padding := strings.Repeat("a", maxMessageBodyBytes)
	body := `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1","payload":{"pad":"` + padding + `"}}`

	recorder := rig.serve(http.MethodPost, "/v1/messages", body, bearerKey)

	assertResponse(testInstance, recorder, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`+"\n")
	rig.assertNoHandlerCall(testInstance)
}

// submitBodyOfSize returns a valid submit body for app urls4irl that is exactly size bytes.
func submitBodyOfSize(testInstance *testing.T, size int) string {
	testInstance.Helper()
	const prefix = `{"app_id":"urls4irl","channel":"alerts","idempotency_key":"key-1","payload":{"pad":"`
	const suffix = `"}}`
	padding := size - len(prefix) - len(suffix)
	if padding < 0 {
		testInstance.Fatalf("size %d is smaller than the body skeleton", size)
	}
	return prefix + strings.Repeat("a", padding) + suffix
}

func TestSubmitBodyAtTheCapIsAcceptedAndOneByteOverIsA413(testInstance *testing.T) {
	rig := newAppKeyRig()

	atCap := rig.serve(http.MethodPost, "/v1/messages", submitBodyOfSize(testInstance, maxMessageBodyBytes), bearerKey)
	if atCap.Code != http.StatusAccepted {
		testInstance.Fatalf("body of exactly the cap: status = %d, want 202; body = %.200s", atCap.Code, atCap.Body.String())
	}
	if len(rig.messages.submitCalls) != 1 {
		testInstance.Fatalf("submit calls = %d, want the handler reached once", len(rig.messages.submitCalls))
	}

	overCap := rig.serve(http.MethodPost, "/v1/messages", submitBodyOfSize(testInstance, maxMessageBodyBytes+1), bearerKey)
	assertResponse(testInstance, overCap, http.StatusRequestEntityTooLarge, `{"error":"request body too large"}`+"\n")
	if len(rig.messages.submitCalls) != 1 {
		testInstance.Fatalf("submit calls = %d, want no further handler call for the oversized body", len(rig.messages.submitCalls))
	}
}

// failingReader fails every read with a plain (not size-limit) error.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset by peer") }

func TestSubmitBodyReadFailureIsTheHandlers400AndNeverReachesIt(testInstance *testing.T) {
	rig := newAppKeyRig()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Body = io.NopCloser(failingReader{})
	authorizeAsApp(request)
	recorder := httptest.NewRecorder()

	rig.server.Handler().ServeHTTP(recorder, request)

	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid JSON body"}`+"\n")
	rig.assertNoHandlerCall(testInstance)
}

func TestSubmitBodyAppIDKeysResolveTheSameWayInTheKeyCheckAndTheHandler(testInstance *testing.T) {
	const tail = `,"channel":"alerts","idempotency_key":"key-1","payload":{}}`
	mismatches := []struct{ name, body string }{
		{name: "differently cased key", body: `{"APP_ID":"chores4irl"` + tail},
		{name: "duplicate key, last wins", body: `{"app_id":"urls4irl","app_id":"chores4irl"` + tail},
		{name: "uppercased value", body: `{"app_id":"URLS4IRL"` + tail},
		{name: "value with a leading space", body: `{"app_id":" urls4irl"` + tail},
	}
	for _, testCase := range mismatches {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newAppKeyRig()

			recorder := rig.serve(http.MethodPost, "/v1/messages", testCase.body, bearerKey)

			assertResponse(subTest, recorder, http.StatusForbidden, `{"error":"app_id does not match app key"}`+"\n")
			rig.assertNoHandlerCall(subTest)
		})
	}

	testInstance.Run("duplicate key, last wins, naming the key's own app", func(subTest *testing.T) {
		rig := newAppKeyRig()

		recorder := rig.serve(http.MethodPost, "/v1/messages", `{"app_id":"chores4irl","app_id":"urls4irl"`+tail, bearerKey)

		if recorder.Code != http.StatusAccepted {
			subTest.Fatalf("status = %d, want 202; body = %s", recorder.Code, recorder.Body.String())
		}
		if len(rig.messages.submitCalls) != 1 || rig.messages.submitCalls[0].AppID != keyApp {
			subTest.Fatalf("submit calls = %+v, want one submit as %s (the key check and the handler agree on the winner)",
				rig.messages.submitCalls, keyApp)
		}
	})
}

func TestDuplicateQueryAppIDsResolveTheSameWayInTheKeyCheckAndTheHandler(testInstance *testing.T) {
	path := "/v1/messages/" + sampleMessageID

	rig := newAppKeyRig()
	own := rig.serve(http.MethodGet, path+"?app_id="+keyApp+"&app_id="+otherApp, "", bearerKey)
	if own.Code != http.StatusOK {
		testInstance.Fatalf("first value is the key's app: status = %d, want 200; body = %s", own.Code, own.Body.String())
	}
	if len(rig.messages.getCalls) != 1 || rig.messages.getCalls[0].appID != keyApp {
		testInstance.Fatalf("get calls = %+v, want one read as %s", rig.messages.getCalls, keyApp)
	}

	foreign := rig.serve(http.MethodGet, path+"?app_id="+otherApp+"&app_id="+keyApp, "", bearerKey)
	assertResponse(testInstance, foreign, http.StatusForbidden, `{"error":"app_id does not match app key"}`+"\n")
	if len(rig.messages.getCalls) != 1 {
		testInstance.Fatalf("get calls = %d, want no read for the foreign first value", len(rig.messages.getCalls))
	}
}

func TestOversizedSubmitWithoutAKeyIsA401NotA413(testInstance *testing.T) {
	rig := newAppKeyRig()
	body := `{"app_id":"urls4irl","payload":"` + strings.Repeat("a", maxMessageBodyBytes) + `"}`

	recorder := rig.serve(http.MethodPost, "/v1/messages", body, "")

	assertUnauthorized(testInstance, recorder, "app key required")
}

func TestKeyCheckResolvesTheCallerAppIntoTheRequestContext(testInstance *testing.T) {
	rig := newAppKeyRig()
	var seen string
	handler := rig.server.requireAppKey(assertedAppIDFromQuery, func(_ http.ResponseWriter, request *http.Request) {
		seen = callerAppID(request)
	})

	request := httptest.NewRequest(http.MethodGet, "/x?app_id="+keyApp, nil)
	authorizeAsApp(request)
	handler(httptest.NewRecorder(), request)

	if seen != keyApp {
		testInstance.Fatalf("callerAppID = %q, want %q", seen, keyApp)
	}
	if callerAppID(httptest.NewRequest(http.MethodGet, "/x", nil)) != "" {
		testInstance.Fatalf("callerAppID of a request that skipped the key check must be empty")
	}
}

// TestAdminMessagesAndOtherRoutesNeedNoKey pins what the key check is NOT applied to: the
// operator submit, the operator delivery and heartbeat endpoints, and the probes.
func TestAdminMessagesAndOtherRoutesNeedNoKey(testInstance *testing.T) {
	routes := []struct {
		name   string
		method string
		path   string
		body   string
		// wantStatus is the route's own answer when it is not a plain 200 or 202.
		wantStatus int
	}{
		{name: "admin submit", method: http.MethodPost, path: "/v1/admin/messages", body: validSubmitBody},
		{name: "exchange", method: http.MethodPost, path: exchangePath, body: exchangeBody, wantStatus: http.StatusAccepted},
		{name: "admin identity requests", method: http.MethodGet, path: identityBase + "/requests"},
		// No readiness checker is configured, so the probe answers its own 503 (never a 401).
		{name: "readyz", method: http.MethodGet, path: "/readyz", wantStatus: http.StatusServiceUnavailable},
		{name: "admin submit for any app", method: http.MethodPost, path: "/v1/admin/messages",
			body: `{"app_id":"chores4irl","channel":"alerts","idempotency_key":"key-1","payload":{}}`},
		{name: "admin deliveries", method: http.MethodGet, path: "/v1/admin/deliveries"},
		{name: "admin scheduled", method: http.MethodGet, path: "/v1/admin/scheduled-messages"},
		{name: "admin heartbeats", method: http.MethodGet, path: "/v1/admin/heartbeats"},
		{name: "healthz", method: http.MethodGet, path: "/healthz"},
	}
	for _, route := range routes {
		testInstance.Run(route.name, func(subTest *testing.T) {
			rig := newAppKeyRig()
			rig.identity.resolveErr = errors.New("the key store must not be consulted")
			rig.identity.exchangeResult = appidentity.ExchangeResult{Status: appidentity.ExchangePending, RequestID: 3}

			recorder := rig.serve(route.method, route.path, route.body, "")

			if route.wantStatus != 0 {
				if recorder.Code != route.wantStatus {
					subTest.Fatalf("status = %d, want %d with no key; body = %s", recorder.Code, route.wantStatus, recorder.Body.String())
				}
			} else if recorder.Code != http.StatusOK && recorder.Code != http.StatusAccepted {
				subTest.Fatalf("status = %d, want a success with no key; body = %s", recorder.Code, recorder.Body.String())
			}
			if len(rig.identity.resolveCalls) != 0 {
				subTest.Fatalf("resolve calls = %v, want none", rig.identity.resolveCalls)
			}
		})
	}
}

func TestAdminMessagesSubmitsOnTheOperatorsBehalfWithWait(testInstance *testing.T) {
	rig := newAppKeyRig()
	rig.messages.awaitResult = sampleStoredMessage()

	recorder := rig.serve(http.MethodPost, "/v1/admin/messages?wait=1",
		`{"app_id":"chores4irl","channel":"alerts","idempotency_key":"key-9","payload":{"a":1}}`, "")

	if recorder.Code != http.StatusAccepted {
		testInstance.Fatalf("status = %d, want 202; body = %s", recorder.Code, recorder.Body.String())
	}
	if len(rig.messages.submitCalls) != 1 || rig.messages.submitCalls[0].AppID != otherApp {
		testInstance.Fatalf("submit calls = %+v, want the operator's asserted app_id kept", rig.messages.submitCalls)
	}
	if len(rig.messages.awaitCalls) != 1 {
		testInstance.Fatalf("await calls = %d, want the wait semantics of /v1/messages", len(rig.messages.awaitCalls))
	}
}

func TestAdminMessagesFailsClosedWithoutMessages(testInstance *testing.T) {
	server := NewServer(ServerConfig{})
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/messages", strings.NewReader(validSubmitBody))
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(recorder, request)

	assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"messages unavailable"}`+"\n")
}

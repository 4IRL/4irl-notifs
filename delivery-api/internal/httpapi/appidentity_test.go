package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
)

// The real identity service must satisfy the consumer-side interface.
var _ AppIdentity = (*appidentity.Service)(nil)

const (
	exchangePath      = "/v1/app-identity/exchange"
	identityBase      = "/v1/admin/app-identity"
	identityTestToken = "aaa.bbb.SECRET-OIDC-TOKEN-SIGNATURE"
	identityTestKey   = "dak_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	exchangeBody      = `{"app_id":"urls4irl","token":"` + identityTestToken + `"}`
)

// fakeAppIdentity is a scripted AppIdentity. Each method returns its field and err, and records
// its arguments.
type fakeAppIdentity struct {
	exchangeResult appidentity.ExchangeResult
	requests       []appidentity.Request
	request        appidentity.Request
	binding        appidentity.Binding
	bindings       []appidentity.BindingWithKeys
	key            appidentity.Key
	deleted        bool
	err            error

	// resolved and resolveErr script ResolveKey, whose presented keys land in resolveCalls. It
	// has its own error and counter so scripting the identity endpoints never changes it.
	resolved     appidentity.ResolvedKey
	resolveErr   error
	resolveCalls []string

	calls          int
	exchangedApp   string
	exchangedToken string
	approvedID     int64
	rejectedID     int64
	revokedID      int64
	unboundApp     string

	exchangedName    string
	registrar        appidentity.WorkflowRegistrar
	registrars       []appidentity.WorkflowRegistrar
	unboundCount     int
	acknowledged     appidentity.BindingWithKeys
	acknowledgedN    int
	approvedPrefix   string
	deletedRegistrar int64
	acknowledgedApp  string
	acknowledgedApps []string
}

func (fake *fakeAppIdentity) ApproveRegistrarRequest(_ context.Context, requestID int64, prefix string) (appidentity.WorkflowRegistrar, error) {
	fake.calls++
	fake.approvedID, fake.approvedPrefix = requestID, prefix
	return fake.registrar, fake.err
}

func (fake *fakeAppIdentity) ListRegistrars(context.Context) ([]appidentity.WorkflowRegistrar, error) {
	fake.calls++
	return fake.registrars, fake.err
}

func (fake *fakeAppIdentity) DeleteRegistrar(_ context.Context, registrarID int64) (bool, int, error) {
	fake.calls++
	fake.deletedRegistrar = registrarID
	return fake.deleted, fake.unboundCount, fake.err
}

func (fake *fakeAppIdentity) AcknowledgeBinding(_ context.Context, appID string) (appidentity.BindingWithKeys, error) {
	fake.calls++
	fake.acknowledgedApp = appID
	return fake.acknowledged, fake.err
}

func (fake *fakeAppIdentity) AcknowledgeAllBindings(_ context.Context, appIDs []string) (int, error) {
	fake.calls++
	fake.acknowledgedApps = appIDs
	return fake.acknowledgedN, fake.err
}

func (fake *fakeAppIdentity) ResolveKey(_ context.Context, key string) (appidentity.ResolvedKey, error) {
	fake.resolveCalls = append(fake.resolveCalls, key)
	return fake.resolved, fake.resolveErr
}

func (fake *fakeAppIdentity) Exchange(_ context.Context, input appidentity.ExchangeInput) (appidentity.ExchangeResult, error) {
	fake.calls++
	fake.exchangedApp, fake.exchangedName, fake.exchangedToken = input.AppID, input.Name, input.Token
	return fake.exchangeResult, fake.err
}

func (fake *fakeAppIdentity) ListRequests(context.Context) ([]appidentity.Request, error) {
	fake.calls++
	return fake.requests, fake.err
}

func (fake *fakeAppIdentity) ApproveRequest(_ context.Context, requestID int64) (appidentity.Binding, error) {
	fake.calls++
	fake.approvedID = requestID
	return fake.binding, fake.err
}

func (fake *fakeAppIdentity) RejectRequest(_ context.Context, requestID int64) (appidentity.Request, error) {
	fake.calls++
	fake.rejectedID = requestID
	return fake.request, fake.err
}

func (fake *fakeAppIdentity) ListBindings(context.Context) ([]appidentity.BindingWithKeys, error) {
	fake.calls++
	return fake.bindings, fake.err
}

func (fake *fakeAppIdentity) RevokeKey(_ context.Context, keyID int64) (appidentity.Key, error) {
	fake.calls++
	fake.revokedID = keyID
	return fake.key, fake.err
}

func (fake *fakeAppIdentity) Unbind(_ context.Context, appID string) (bool, error) {
	fake.calls++
	fake.unboundApp = appID
	return fake.deleted, fake.err
}

func serveIdentity(fake AppIdentity, logger *slog.Logger, method, path, body string) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{AppIdentity: fake, Logger: logger})
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func identityTime() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }

func sampleIdentityRequest(status string) appidentity.Request {
	return appidentity.Request{
		ID: 3, Kind: appidentity.RequestKindApp, AppID: "urls4irl", Repository: "4IRL/urls4irl", RepositoryID: 1001, RepositoryOwnerID: 678,
		WorkflowPath: ".github/workflows/prod-deploy.yml", Ref: "refs/heads/main", Environment: "production",
		GitHubRunID: "run-9", Status: status, CreatedAt: identityTime(), UpdatedAt: identityTime(),
	}
}

func sampleIdentityBinding() appidentity.Binding {
	return appidentity.Binding{
		AppID: "urls4irl", Repository: "4IRL/urls4irl", RepositoryID: 1001, RepositoryOwnerID: 678,
		WorkflowPath: ".github/workflows/prod-deploy.yml", Ref: "refs/heads/main", ApprovedAt: identityTime(),
	}
}

func sampleIdentityKey(state appidentity.KeyState) appidentity.Key {
	return appidentity.Key{ID: 4, AppID: "urls4irl", Hint: "dak_abcd", State: state, CreatedAt: identityTime()}
}

const (
	sampleRequestJSON = `{"id":3,"kind":"app","app_id":"urls4irl","github_repository":"4IRL/urls4irl","github_repository_id":1001,` +
		`"workflow_path":".github/workflows/prod-deploy.yml","git_ref":"refs/heads/main","environment":"production",` +
		`"github_run_id":"run-9","status":"%s","created_at":"2026-10-22T12:00:00Z","updated_at":"2026-10-22T12:00:00Z"}`
	sampleBindingJSON = `{"app_id":"urls4irl","github_repository":"4IRL/urls4irl","github_repository_id":1001,` +
		`"github_owner_id":678,"workflow_path":".github/workflows/prod-deploy.yml","git_ref":"refs/heads/main",` +
		`"environment":null,"approved_at":"2026-10-22T12:00:00Z","registrar":null,"acknowledged_at":null,"keys":%s}`
	sampleActiveKeyJSON = `{"id":4,"app_id":"urls4irl","key_hint":"dak_abcd","state":"active",` +
		`"created_at":"2026-10-22T12:00:00Z","superseded_at":null,"revoked_at":null,"revoked_reason":null,"last_used_at":null}`
)

func TestExchangeHappyPaths(testInstance *testing.T) {
	testCases := []struct {
		name       string
		result     appidentity.ExchangeResult
		wantStatus int
		wantBody   string
	}{
		{
			"pending is 202 with the request id and no key",
			appidentity.ExchangeResult{Status: appidentity.ExchangePending, RequestID: 12},
			http.StatusAccepted, `{"status":"pending","request_id":12}` + "\n",
		},
		{
			"issued is 200 with the key once",
			appidentity.ExchangeResult{Status: appidentity.ExchangeIssued, AppID: "urls4irl", Key: identityTestKey, KeyID: 5},
			http.StatusOK, `{"status":"issued","app_id":"urls4irl","key":"` + identityTestKey + `","key_id":5}` + "\n",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppIdentity{exchangeResult: testCase.result}

			recorder := serveIdentity(fake, nil, http.MethodPost, exchangePath, exchangeBody)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if fake.exchangedApp != "urls4irl" || fake.exchangedToken != identityTestToken {
				subTest.Errorf("service saw app %q token %q, want the decoded body", fake.exchangedApp, fake.exchangedToken)
			}
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Errorf("Cache-Control = %q: a response that can hold a key must never be cached", got)
			}
		})
	}
}

func TestExchangeRejectsBadBodiesWithoutCallingTheService(testInstance *testing.T) {
	testCases := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"not json", `not json`, http.StatusBadRequest, `{"error":"invalid JSON body"}` + "\n"},
		{"empty body", ``, http.StatusBadRequest, `{"error":"invalid JSON body"}` + "\n"},
		{"unknown field", `{"app_id":"urls4irl","token":"x","extra":1}`, http.StatusBadRequest, `{"error":"invalid JSON body"}` + "\n"},
		{"trailing data", exchangeBody + `{}`, http.StatusBadRequest, `{"error":"invalid JSON body"}` + "\n"},
		{"wrong type", `{"app_id":1,"token":"x"}`, http.StatusBadRequest, `{"error":"invalid JSON body"}` + "\n"},
		{
			"oversized", `{"app_id":"urls4irl","token":"` + strings.Repeat("a", maxExchangeBodyBytes) + `"}`,
			http.StatusRequestEntityTooLarge, `{"error":"request body too large"}` + "\n",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppIdentity{}

			recorder := serveIdentity(fake, nil, http.MethodPost, exchangePath, testCase.body)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if fake.calls != 0 {
				subTest.Errorf("service called %d times for a malformed body", fake.calls)
			}
		})
	}
}

func TestIdentityMapsServiceErrors(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		body       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"invalid app_id", http.MethodPost, exchangePath, exchangeBody, &appidentity.ValidationError{Message: "invalid app_id"},
			http.StatusBadRequest, `{"error":"invalid app_id"}`},
		{"missing token", http.MethodPost, exchangePath, exchangeBody, &appidentity.ValidationError{Message: "token is required"},
			http.StatusBadRequest, `{"error":"token is required"}`},
		{"invalid token", http.MethodPost, exchangePath, exchangeBody, fmt.Errorf("%w: token signature is invalid", appidentity.ErrInvalidToken),
			http.StatusUnauthorized, `{"error":"invalid identity token"}`},
		{"replayed token", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrReplayed,
			http.StatusUnauthorized, `{"error":"invalid identity token"}`},
		{"keys unavailable", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrKeysUnavailable,
			http.StatusServiceUnavailable, `{"error":"identity unavailable"}`},
		{"binding mismatch", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrBindingMismatch,
			http.StatusForbidden, `{"error":"identity does not match the approved binding"}`},
		{"pending cap", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrPendingCap,
			http.StatusTooManyRequests, `{"error":"too many pending identity requests"}`},
		{"approve unknown request", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrRequestNotFound,
			http.StatusNotFound, `{"error":"request does not exist"}`},
		{"approve app already bound", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrAppAlreadyBound,
			http.StatusConflict, `{"error":"app already bound"}`},
		{"approve workflow already bound", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrWorkflowIdentityBound,
			http.StatusConflict, `{"error":"workflow identity already bound"}`},
		{"approve app id overlapping a bound app id", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrAppIDOverlap,
			http.StatusConflict, `{"error":"app id overlaps an approved app id"}`},
		{"approve a rejected request", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrRequestNotPending,
			http.StatusConflict, `{"error":"request is not pending"}`},
		{"approve request changed", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrRequestChanged,
			http.StatusConflict, `{"error":"registration request changed during approval"}`},
		{"approve reserved app id", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrAppIDReserved,
			http.StatusConflict, `{"error":"app id is reserved"}`},
		{"approve registry unavailable", http.MethodPost, identityBase + "/requests/9/approve", "",
			fmt.Errorf("%w: boom", appidentity.ErrAppRegistryUnavailable),
			http.StatusBadGateway, `{"error":"app registry unavailable"}`},
		{"approve registry timeout", http.MethodPost, identityBase + "/requests/9/approve", "",
			fmt.Errorf("%w: %w", appidentity.ErrAppRegistryUnavailable, context.DeadlineExceeded),
			http.StatusBadGateway, `{"error":"app registry unavailable"}`},
		{"approve registry not configured", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrAppRegistryNotConfigured,
			http.StatusServiceUnavailable, `{"error":"app registry unavailable"}`},
		{"reject unknown request", http.MethodPost, identityBase + "/requests/9/reject", "", appidentity.ErrRequestNotFound,
			http.StatusNotFound, `{"error":"request does not exist"}`},
		{"revoke unknown key", http.MethodPost, identityBase + "/keys/9/revoke", "", appidentity.ErrKeyNotFound,
			http.StatusNotFound, `{"error":"key does not exist"}`},
		{"unbind malformed app", http.MethodDelete, identityBase + "/bindings/Bad-App", "", &appidentity.ValidationError{Message: "invalid app_id"},
			http.StatusBadRequest, `{"error":"invalid app_id"}`},
		{"list requests store failure", http.MethodGet, identityBase + "/requests", "", errors.New("pq: connection refused"),
			http.StatusInternalServerError, `{"error":"internal error"}`},
		{"list bindings store failure", http.MethodGet, identityBase + "/bindings", "", errors.New("pq: connection refused"),
			http.StatusInternalServerError, `{"error":"internal error"}`},
		{"invalid name", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrInvalidName,
			http.StatusBadRequest, `{"error":"invalid name"}`},
		{"invalid prefix", http.MethodPost, identityBase + "/requests/9/approve-registrar", `{"prefix":"x"}`, appidentity.ErrInvalidPrefix,
			http.StatusBadRequest, `{"error":"invalid prefix"}`},
		{"exchange target", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrExchangeTarget,
			http.StatusBadRequest, `{"error":"send exactly one of app_id or name"}`},
		{"registrar ownership", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrRegistrarOwnership,
			http.StatusForbidden, `{"error":"app id is not owned by this registrar"}`},
		{"registrar app limit", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrRegistrarAppLimit,
			http.StatusConflict, `{"error":"registrar app limit reached"}`},
		{"exchange overlap", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrAppIDOverlap,
			http.StatusConflict, `{"error":"app id overlaps an approved app id"}`},
		{"exchange in registrar prefix", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrAppIDInRegistrarPrefix,
			http.StatusConflict, `{"error":"app id is reserved by a registrar"}`},
		{"exchange binding appeared", http.MethodPost, exchangePath, exchangeBody, appidentity.ErrBindingAppeared,
			http.StatusConflict, `{"error":"registration changed, retry"}`},
		{"exchange binding appeared wrapped", http.MethodPost, exchangePath, `{"name":"a","token":"x"}`,
			fmt.Errorf("lock: %w", appidentity.ErrBindingAppeared),
			http.StatusConflict, `{"error":"registration changed, retry"}`},
		{"approve registrar request", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrRequestIsRegistrar,
			http.StatusConflict, `{"error":"request is a registrar request"}`},
		{"approve-registrar app request", http.MethodPost, identityBase + "/requests/9/approve-registrar", `{"prefix":"hosted_"}`,
			appidentity.ErrRequestNotRegistrar, http.StatusConflict, `{"error":"request is not a registrar request"}`},
		{"approve-registrar prefix taken", http.MethodPost, identityBase + "/requests/9/approve-registrar", `{"prefix":"hosted_"}`,
			appidentity.ErrPrefixTaken, http.StatusConflict, `{"error":"prefix already used"}`},
		{"approve-registrar prefix overlap", http.MethodPost, identityBase + "/requests/9/approve-registrar", `{"prefix":"hosted_"}`,
			appidentity.ErrPrefixOverlap, http.StatusConflict, `{"error":"prefix overlaps an approved app id or registrar"}`},
		{"approve-registrar workflow bound", http.MethodPost, identityBase + "/requests/9/approve-registrar", `{"prefix":"hosted_"}`,
			appidentity.ErrWorkflowIdentityBound, http.StatusConflict, `{"error":"workflow identity already bound"}`},
		{"approve-registrar unknown", http.MethodPost, identityBase + "/requests/9/approve-registrar", `{"prefix":"hosted_"}`,
			appidentity.ErrRequestNotFound, http.StatusNotFound, `{"error":"request does not exist"}`},
		{"approve-registrar not pending", http.MethodPost, identityBase + "/requests/9/approve-registrar", `{"prefix":"hosted_"}`,
			appidentity.ErrRequestNotPending, http.StatusConflict, `{"error":"request is not pending"}`},
		{"approve in registrar prefix", http.MethodPost, identityBase + "/requests/9/approve", "", appidentity.ErrAppIDInRegistrarPrefix,
			http.StatusConflict, `{"error":"app id is reserved by a registrar"}`},
		{"acknowledge unknown binding", http.MethodPost, identityBase + "/bindings/urls4irl/acknowledge", "", appidentity.ErrBindingNotFound,
			http.StatusNotFound, `{"error":"binding does not exist"}`},
		{"acknowledge invalid app", http.MethodPost, identityBase + "/bindings/Bad-App/acknowledge", "", &appidentity.ValidationError{Message: "invalid app_id"},
			http.StatusBadRequest, `{"error":"invalid app_id"}`},
		{"acknowledge-all bad list", http.MethodPost, identityBase + "/bindings/acknowledge-all", `{"app_ids":[]}`,
			&appidentity.ValidationError{Message: "app_ids must list 1 to 200 apps"},
			http.StatusBadRequest, `{"error":"app_ids must list 1 to 200 apps"}`},
		{"exchange store failure", http.MethodPost, exchangePath, exchangeBody, errors.New("pq: connection refused"),
			http.StatusInternalServerError, `{"error":"internal error"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveIdentity(&fakeAppIdentity{err: testCase.err}, nil, testCase.method, testCase.path, testCase.body)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
		})
	}
}

func TestIdentityTokenFailureEndToEndNeverReturnsTheReasonOrLogsTheToken(testInstance *testing.T) {
	const reason = "token signature is invalid"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	verifier := failingVerifier{err: fmt.Errorf("%w: %s", appidentity.ErrInvalidToken, reason)}
	service := appidentity.NewService(appidentity.ServiceConfig{Verifier: verifier, Logger: logger})

	recorder := serveIdentity(service, logger, http.MethodPost, exchangePath, exchangeBody)

	assertResponse(testInstance, recorder, http.StatusUnauthorized, `{"error":"invalid identity token"}`+"\n")
	if strings.Contains(recorder.Body.String(), reason) {
		testInstance.Errorf("the response names the check that failed: %s", recorder.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, reason) || !strings.Contains(logged, `"app_id":"urls4irl"`) {
		testInstance.Errorf("the log must carry the specific reason and the app id: %s", logged)
	}
	if strings.Contains(logged, identityTestToken) || strings.Contains(logged, "SECRET-OIDC") {
		testInstance.Errorf("the log leaks the token: %s", logged)
	}
}

type failingVerifier struct{ err error }

func (verifier failingVerifier) Verify(context.Context, string, appidentity.ReplayRecorder) (appidentity.Claims, error) {
	return appidentity.Claims{}, verifier.err
}

func TestExchangeWithAnUnknownResultStatusFailsClosed(testInstance *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	fake := &fakeAppIdentity{exchangeResult: appidentity.ExchangeResult{Status: "approved", Key: identityTestKey}}

	recorder := serveIdentity(fake, logger, http.MethodPost, exchangePath, exchangeBody)

	assertResponse(testInstance, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
	if strings.Contains(recorder.Body.String(), identityTestKey) {
		testInstance.Errorf("an unknown status must never echo a key: %s", recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "unknown status") || strings.Contains(logs.String(), identityTestKey) {
		testInstance.Errorf("want the unknown status logged without the key: %s", logs.String())
	}
}

func TestIdentityContextErrorsAreLoggedBelowErrorLevel(testInstance *testing.T) {
	for _, testCase := range []struct {
		name      string
		err       error
		wantLevel string
	}{
		{"client went away", fmt.Errorf("begin transaction: %w", context.Canceled), `"level":"DEBUG"`},
		{"deadline exceeded", fmt.Errorf("begin transaction: %w", context.DeadlineExceeded), `"level":"WARN"`},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

			recorder := serveIdentity(&fakeAppIdentity{err: testCase.err}, logger, http.MethodPost, exchangePath, exchangeBody)

			assertResponse(subTest, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
			if !strings.Contains(logs.String(), testCase.wantLevel) || strings.Contains(logs.String(), `"level":"ERROR"`) {
				subTest.Errorf("want a %s line and no ERROR line: %s", testCase.wantLevel, logs.String())
			}
		})
	}
}

func TestIdentityInternalErrorsAreLoggedByPathNotEchoed(testInstance *testing.T) {
	const detail = "pq: password authentication failed for user delivery"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	recorder := serveIdentity(&fakeAppIdentity{err: errors.New(detail)}, logger, http.MethodPost, exchangePath, exchangeBody)

	assertResponse(testInstance, recorder, http.StatusInternalServerError, `{"error":"internal error"}`+"\n")
	if strings.Contains(recorder.Body.String(), "password") {
		testInstance.Errorf("the response echoes the error: %s", recorder.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, detail) || !strings.Contains(logged, exchangePath) {
		testInstance.Errorf("the log is missing the error or the path: %s", logged)
	}
	if strings.Contains(logged, identityTestToken) {
		testInstance.Errorf("the log leaks the request body: %s", logged)
	}
}

func TestIdentityAdminHappyPaths(testInstance *testing.T) {
	testCases := []struct {
		name       string
		method     string
		path       string
		fake       *fakeAppIdentity
		wantStatus int
		wantBody   string
	}{
		{
			"list requests", http.MethodGet, identityBase + "/requests",
			&fakeAppIdentity{requests: []appidentity.Request{sampleIdentityRequest(appidentity.RequestPending)}},
			http.StatusOK, `{"requests":[` + fmt.Sprintf(sampleRequestJSON, "pending") + `]}` + "\n",
		},
		{
			"approve", http.MethodPost, identityBase + "/requests/3/approve",
			&fakeAppIdentity{binding: sampleIdentityBinding()},
			http.StatusCreated, `{"binding":` + fmt.Sprintf(sampleBindingJSON, "[]") + `}` + "\n",
		},
		{
			"reject", http.MethodPost, identityBase + "/requests/3/reject",
			&fakeAppIdentity{request: sampleIdentityRequest(appidentity.RequestRejected)},
			http.StatusOK, `{"request":` + fmt.Sprintf(sampleRequestJSON, "rejected") + `}` + "\n",
		},
		{
			"list bindings", http.MethodGet, identityBase + "/bindings",
			&fakeAppIdentity{bindings: []appidentity.BindingWithKeys{{
				Binding: sampleIdentityBinding(), Keys: []appidentity.Key{sampleIdentityKey(appidentity.KeyActive)},
			}}},
			http.StatusOK, `{"bindings":[` + fmt.Sprintf(sampleBindingJSON, "["+sampleActiveKeyJSON+"]") + `]}` + "\n",
		},
		{
			"revoke key", http.MethodPost, identityBase + "/keys/4/revoke",
			&fakeAppIdentity{key: sampleIdentityKey(appidentity.KeyActive)},
			http.StatusOK, `{"key":` + sampleActiveKeyJSON + `}` + "\n",
		},
		{
			"unbind", http.MethodDelete, identityBase + "/bindings/urls4irl",
			&fakeAppIdentity{deleted: true},
			http.StatusOK, `{"app_id":"urls4irl","deleted":true}` + "\n",
		},
		{
			"unbind of an unbound app is not an error", http.MethodDelete, identityBase + "/bindings/urls4irl",
			&fakeAppIdentity{deleted: false},
			http.StatusOK, `{"app_id":"urls4irl","deleted":false}` + "\n",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			recorder := serveIdentity(testCase.fake, nil, testCase.method, testCase.path, "")

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody)
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestIdentityAdminListsAreNeverNull(testInstance *testing.T) {
	recorder := serveIdentity(&fakeAppIdentity{}, nil, http.MethodGet, identityBase+"/requests", "")
	assertResponse(testInstance, recorder, http.StatusOK, `{"requests":[]}`+"\n")

	recorder = serveIdentity(&fakeAppIdentity{}, nil, http.MethodGet, identityBase+"/bindings", "")
	assertResponse(testInstance, recorder, http.StatusOK, `{"bindings":[]}`+"\n")

	// A binding with no keys lists "keys": [], never null.
	fake := &fakeAppIdentity{bindings: []appidentity.BindingWithKeys{{Binding: sampleIdentityBinding()}}}
	recorder = serveIdentity(fake, nil, http.MethodGet, identityBase+"/bindings", "")
	assertResponse(testInstance, recorder, http.StatusOK, `{"bindings":[`+fmt.Sprintf(sampleBindingJSON, "[]")+`]}`+"\n")
}

func TestIdentityAdminRevokedKeysCarryTheirReason(testInstance *testing.T) {
	revokedAt := identityTime().Add(-time.Hour)
	supersededAt := identityTime().Add(-25 * time.Hour)
	lastUsed := identityTime().Add(-30 * time.Minute)
	key := sampleIdentityKey(appidentity.KeyRevoked)
	key.SupersededAt, key.RevokedAt, key.LastUsedAt, key.RevokedReason = &supersededAt, &revokedAt, &lastUsed, appidentity.RevokedReasonSuperseded

	recorder := serveIdentity(&fakeAppIdentity{key: key}, nil, http.MethodPost, identityBase+"/keys/4/revoke", "")

	assertResponse(testInstance, recorder, http.StatusOK,
		`{"key":{"id":4,"app_id":"urls4irl","key_hint":"dak_abcd","state":"revoked","created_at":"2026-10-22T12:00:00Z",`+
			`"superseded_at":"2026-10-21T11:00:00Z","revoked_at":"2026-10-22T11:00:00Z","revoked_reason":"superseded",`+
			`"last_used_at":"2026-10-22T11:30:00Z"}}`+"\n")
}

func TestIdentityAdminReachesTheServiceWithThePathValues(testInstance *testing.T) {
	fake := &fakeAppIdentity{}

	serveIdentity(fake, nil, http.MethodPost, identityBase+"/requests/31/approve", "")
	serveIdentity(fake, nil, http.MethodPost, identityBase+"/requests/32/reject", "")
	serveIdentity(fake, nil, http.MethodPost, identityBase+"/keys/33/revoke", "")
	serveIdentity(fake, nil, http.MethodDelete, identityBase+"/bindings/urls4irl", "")

	if fake.approvedID != 31 || fake.rejectedID != 32 || fake.revokedID != 33 || fake.unboundApp != "urls4irl" {
		testInstance.Errorf("service saw approve %d reject %d revoke %d unbind %q", fake.approvedID, fake.rejectedID, fake.revokedID, fake.unboundApp)
	}
}

func TestIdentityAdminRejectsMalformedIDsBeforeTheService(testInstance *testing.T) {
	for _, rawID := range []string{"abc", "0", "-1", "01", "1.5", "99999999999999999999", "+1"} {
		for _, route := range []struct{ path, message string }{
			{"/requests/%s/approve", "invalid request id"},
			{"/requests/%s/reject", "invalid request id"},
			{"/keys/%s/revoke", "invalid key id"},
		} {
			testInstance.Run(fmt.Sprintf(route.path, rawID), func(subTest *testing.T) {
				fake := &fakeAppIdentity{}

				recorder := serveIdentity(fake, nil, http.MethodPost, identityBase+fmt.Sprintf(route.path, rawID), "")

				assertResponse(subTest, recorder, http.StatusBadRequest, `{"error":"`+route.message+`"}`+"\n")
				if fake.calls != 0 {
					subTest.Errorf("service called %d times", fake.calls)
				}
			})
		}
	}
}

func TestIdentityRoutesFailClosedWithoutAService(testInstance *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, exchangePath},
		{http.MethodGet, identityBase + "/requests"},
		{http.MethodPost, identityBase + "/requests/1/approve"},
		{http.MethodPost, identityBase + "/requests/1/reject"},
		{http.MethodGet, identityBase + "/bindings"},
		{http.MethodPost, identityBase + "/keys/1/revoke"},
		{http.MethodDelete, identityBase + "/bindings/urls4irl"},
		{http.MethodPost, identityBase + "/requests/1/approve-registrar"},
		{http.MethodGet, identityBase + "/registrars"},
		{http.MethodDelete, identityBase + "/registrars/1"},
		{http.MethodPost, identityBase + "/bindings/urls4irl/acknowledge"},
		{http.MethodPost, identityBase + "/bindings/acknowledge-all"},
	} {
		testInstance.Run(route.method+" "+route.path, func(subTest *testing.T) {
			recorder := serveIdentity(nil, nil, route.method, route.path, exchangeBody)

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"identity unavailable"}`+"\n")
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				subTest.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestIdentityRoutesRejectUnsupportedMethods(testInstance *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, exchangePath},
		{http.MethodPut, exchangePath},
		{http.MethodDelete, exchangePath},
		{http.MethodPost, identityBase + "/requests"},
		{http.MethodGet, identityBase + "/requests/1/approve"},
		{http.MethodGet, identityBase + "/keys/1/revoke"},
		{http.MethodPost, identityBase + "/bindings"},
		{http.MethodGet, identityBase + "/bindings/urls4irl"},
	} {
		testInstance.Run(route.method+" "+route.path, func(subTest *testing.T) {
			fake := &fakeAppIdentity{}

			recorder := serveIdentity(fake, nil, route.method, route.path, "")

			if recorder.Code != http.StatusMethodNotAllowed {
				subTest.Errorf("status = %d, want 405", recorder.Code)
			}
			if fake.calls != 0 {
				subTest.Errorf("service called %d times", fake.calls)
			}
		})
	}
}

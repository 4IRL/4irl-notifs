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

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const publisherPath = "/v1/admin/apps/urls4irl/ntfy-publisher"

// fakePublishers is a Publishers double that returns canned results and counts calls.
type fakePublishers struct {
	view          publisher.PublisherView
	err           error
	mintCalls     int // Mint plus MintAsOperator
	appCalls      int // Mint (app path, rate limited)
	operatorCalls int // MintAsOperator (exempt)
	getCalls      int
	removeCalls   int
	lastAppID     string
	lastRotate    bool
}

func (fake *fakePublishers) Remove(_ context.Context, appID string) error {
	fake.removeCalls++
	fake.lastAppID = appID
	return fake.err
}

func (fake *fakePublishers) MintAsOperator(_ context.Context, appID string, rotate bool) (publisher.PublisherView, error) {
	fake.operatorCalls++
	fake.mintCalls++
	fake.lastRotate = rotate
	fake.lastAppID = appID
	return fake.view, fake.err
}

func (fake *fakePublishers) Mint(_ context.Context, appID string, rotate bool) (publisher.PublisherView, error) {
	fake.appCalls++
	fake.mintCalls++
	fake.lastRotate = rotate
	fake.lastAppID = appID
	return fake.view, fake.err
}

func (fake *fakePublishers) Get(_ context.Context, appID string) (publisher.PublisherView, error) {
	fake.getCalls++
	fake.lastAppID = appID
	return fake.view, fake.err
}

func samplePublisherView() publisher.PublisherView {
	timestamp := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	return publisher.PublisherView{
		AppID:           "urls4irl",
		PublisherUserID: "u_publisher",
		TopicPattern:    "urls4irl-*",
		CreatedAt:       timestamp,
		UpdatedAt:       timestamp,
	}
}

// servePublishers sends one request to a Server built around fake and returns the recorder.
func servePublishers(fake Publishers, logger *slog.Logger, method, path string) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{Publishers: fake, Logger: logger})
	request := httptest.NewRequest(method, path, strings.NewReader(""))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestPublishersHappyPaths(testInstance *testing.T) {
	wantBody := `{"publisher":{"app_id":"urls4irl","publisher_user_id":"u_publisher","topic_pattern":"urls4irl-*",` +
		`"created_at":"2026-10-17T12:00:00Z","updated_at":"2026-10-17T12:00:00Z"}}` + "\n"
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		testInstance.Run(method, func(testInstance *testing.T) {
			fake := &fakePublishers{view: samplePublisherView()}
			recorder := servePublishers(fake, nil, method, publisherPath)
			assertResponse(testInstance, recorder, http.StatusOK, wantBody)
			if fake.lastAppID != "urls4irl" {
				testInstance.Fatalf("app id = %q, want urls4irl", fake.lastAppID)
			}
			if body := recorder.Body.String(); strings.Contains(body, `"token"`) || strings.Contains(body, "tk_") {
				testInstance.Fatalf("response leaks a token: %s", body)
			}
		})
	}
}

func TestPublishersRouteToTheRightMethod(testInstance *testing.T) {
	fake := &fakePublishers{view: samplePublisherView()}
	servePublishers(fake, nil, http.MethodPost, publisherPath)
	if fake.mintCalls != 1 || fake.getCalls != 0 {
		testInstance.Fatalf("POST: mint=%d get=%d, want 1/0", fake.mintCalls, fake.getCalls)
	}
	servePublishers(fake, nil, http.MethodGet, publisherPath)
	if fake.mintCalls != 1 || fake.getCalls != 1 {
		testInstance.Fatalf("GET: mint=%d get=%d, want 1/1", fake.mintCalls, fake.getCalls)
	}
	servePublishers(fake, nil, http.MethodDelete, publisherPath)
	if fake.mintCalls != 1 || fake.getCalls != 1 || fake.removeCalls != 1 {
		testInstance.Fatalf("DELETE: mint=%d get=%d remove=%d, want 1/1/1", fake.mintCalls, fake.getCalls, fake.removeCalls)
	}
}

// serveMintBody POSTs body (nil means no body at all) to the operator mint route.
func serveMintBody(fake Publishers, body *string) *httptest.ResponseRecorder {
	server := NewServer(ServerConfig{Publishers: fake})
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(http.MethodPost, publisherPath, http.NoBody)
	} else {
		request = httptest.NewRequest(http.MethodPost, publisherPath, strings.NewReader(*body))
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestOperatorMintPublisherRotateBody(testInstance *testing.T) {
	str := func(text string) *string { return &text }
	tests := []struct {
		name       string
		body       *string
		wantStatus int
		wantCalls  int
		wantRotate bool
	}{
		{name: "no body is rotate false", body: nil, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "empty body is rotate false", body: str(""), wantStatus: http.StatusOK, wantCalls: 1},
		{name: "rotate true", body: str(`{"rotate":true}`), wantStatus: http.StatusOK, wantCalls: 1, wantRotate: true},
		{name: "rotate false", body: str(`{"rotate":false}`), wantStatus: http.StatusOK, wantCalls: 1},
		{name: "empty object is rotate false", body: str(`{}`), wantStatus: http.StatusOK, wantCalls: 1},
		{name: "unknown field is 400", body: str(`{"rotate":true,"extra":1}`), wantStatus: http.StatusBadRequest},
		{name: "invalid JSON is 400", body: str(`{"rotate":`), wantStatus: http.StatusBadRequest},
		{name: "non-bool rotate is 400", body: str(`{"rotate":"yes"}`), wantStatus: http.StatusBadRequest},
	}
	for _, testCase := range tests {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakePublishers{view: samplePublisherView(), lastRotate: !testCase.wantRotate}
			recorder := serveMintBody(fake, testCase.body)
			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d (%s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if fake.operatorCalls != testCase.wantCalls || fake.appCalls != 0 {
				subTest.Fatalf("MintAsOperator=%d Mint=%d, want %d/0", fake.operatorCalls, fake.appCalls, testCase.wantCalls)
			}
			if testCase.wantCalls == 1 && fake.lastRotate != testCase.wantRotate {
				subTest.Fatalf("rotate = %v, want %v", fake.lastRotate, testCase.wantRotate)
			}
		})
	}
}

func TestPublishersAppNotBoundIs404(testInstance *testing.T) {
	fake := &fakePublishers{err: publisher.ErrAppNotBound}
	recorder := servePublishers(fake, nil, http.MethodPost, publisherPath)
	assertResponse(testInstance, recorder, http.StatusNotFound, `{"error":"app has no approved binding"}`+"\n")
}

// unboundStore is a publisher.Store with no rows; the mint lock is never expected.
type unboundStore struct{}

func (unboundStore) Get(context.Context, string) (publisher.StoredPublisher, error) {
	return publisher.StoredPublisher{}, registry.ErrNoPublisher
}
func (unboundStore) Delete(context.Context, string) error { return nil }
func (unboundStore) WithAppMintLock(context.Context, string, func(publisher.MintTx) error) error {
	return errors.New("mint lock must not be taken")
}

type countingChecker struct{ calls int }

func (checker *countingChecker) HasBinding(context.Context, string) (bool, error) {
	checker.calls++
	return false, nil
}

func TestGetAndDeletePublisherNeverConsultTheBindingChecker(testInstance *testing.T) {
	for _, withChecker := range []bool{true, false} {
		checker := &countingChecker{}
		var bindingChecker publisher.BindingChecker
		if withChecker {
			bindingChecker = checker
		}
		service := publisher.NewService(unboundStore{}, nil, nil, bindingChecker, nil)

		getRecorder := servePublishers(service, nil, http.MethodGet, publisherPath)
		assertResponse(testInstance, getRecorder, http.StatusNotFound, `{"error":"publisher not found"}`+"\n")
		deleteRecorder := servePublishers(service, nil, http.MethodDelete, publisherPath)
		if deleteRecorder.Code != http.StatusNoContent {
			testInstance.Fatalf("DELETE for an unbound app = %d, want 204", deleteRecorder.Code)
		}
		if checker.calls != 0 {
			testInstance.Fatalf("checker consulted %d times by GET/DELETE", checker.calls)
		}
	}
}

func TestPublishersDeleteIs204WithNoBody(testInstance *testing.T) {
	fake := &fakePublishers{}

	recorder := servePublishers(fake, nil, http.MethodDelete, publisherPath)

	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
		testInstance.Fatalf("status/body = %d/%q, want 204 and no body", recorder.Code, recorder.Body.String())
	}
	if fake.lastAppID != "urls4irl" {
		testInstance.Fatalf("app id = %q, want urls4irl", fake.lastAppID)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		testInstance.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestPublishersDeleteMapsErrors(testInstance *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "validation error is 400", err: &publisher.ValidationError{Message: "invalid app_id"},
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid app_id"}` + "\n",
		},
		{
			name: "store failure is a generic 502", err: errors.New("delete app publisher: db down"),
			wantStatus: http.StatusBadGateway, wantBody: `{"error":"provisioning-api mint failed"}` + "\n",
		},
	}
	for _, testCase := range tests {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			recorder := servePublishers(&fakePublishers{err: testCase.err}, nil, http.MethodDelete, publisherPath)
			assertResponse(testInstance, recorder, testCase.wantStatus, testCase.wantBody)
		})
	}
}

func TestPublishersDeleteInvalidAppIDIs400(testInstance *testing.T) {
	service := publisher.NewService(nil, nil, nil, nil, nil)
	recorder := servePublishers(service, nil, http.MethodDelete, "/v1/admin/apps/Bad-App/ntfy-publisher")
	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid app_id"}`+"\n")
}

func TestPublishersMapErrors(testInstance *testing.T) {
	tests := []struct {
		name       string
		method     string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name: "get with no publisher is 404", method: http.MethodGet,
			err:        registry.ErrNoPublisher,
			wantStatus: http.StatusNotFound, wantBody: `{"error":"publisher not found"}` + "\n",
		},
		{
			name: "validation error is 400", method: http.MethodPost,
			err:        &publisher.ValidationError{Message: "invalid app_id"},
			wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid app_id"}` + "\n",
		},
		{
			name: "mint unavailable is 503", method: http.MethodPost,
			err:        publisher.ErrMintUnavailable,
			wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"publisher minting unavailable"}` + "\n",
		},
		{
			name: "wrapped mint unavailable is 503", method: http.MethodPost,
			err:        errors.Join(errors.New("context"), publisher.ErrMintUnavailable),
			wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"publisher minting unavailable"}` + "\n",
		},
		{
			name: "post-mint store failure (wrapped) is 503 and echoes nothing", method: http.MethodPost,
			err:        fmt.Errorf("%w: upsert: %w", publisher.ErrMintUnavailable, errors.New("boom tk_secret")),
			wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"publisher minting unavailable"}` + "\n",
		},
		{
			name: "mint busy is 409", method: http.MethodPost,
			err:        publisher.ErrMintBusy,
			wantStatus: http.StatusConflict, wantBody: `{"error":"publisher mint already in progress"}` + "\n",
		},
		{
			name: "unconfigured provisioning client is 503", method: http.MethodPost,
			err:        provisionclient.ErrNotConfigured,
			wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"publisher minting unavailable"}` + "\n",
		},
		{
			name: "wrapped unconfigured provisioning client is 503", method: http.MethodPost,
			err:        fmt.Errorf("mint publisher: %w", provisionclient.ErrNotConfigured),
			wantStatus: http.StatusServiceUnavailable, wantBody: `{"error":"publisher minting unavailable"}` + "\n",
		},
		{
			name: "minter failure is 502", method: http.MethodPost,
			err:        errors.New("mint publisher: provision-app: unexpected status 500"),
			wantStatus: http.StatusBadGateway, wantBody: `{"error":"provisioning-api mint failed"}` + "\n",
		},
	}
	for _, testCase := range tests {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			fake := &fakePublishers{err: testCase.err}
			recorder := servePublishers(fake, nil, testCase.method, publisherPath)
			assertResponse(testInstance, recorder, testCase.wantStatus, testCase.wantBody)
		})
	}
}

func TestPublishersInvalidAppIDIs400(testInstance *testing.T) {
	// A real publisher.Service validates the app id before touching its store or minter.
	service := publisher.NewService(nil, nil, nil, nil, nil)
	recorder := servePublishers(service, nil, http.MethodPost, "/v1/admin/apps/Bad-App/ntfy-publisher")
	assertResponse(testInstance, recorder, http.StatusBadRequest, `{"error":"invalid app_id"}`+"\n")
}

func TestPublishersMintFailureIsLoggedNotEchoed(testInstance *testing.T) {
	const secretDetail = "upstream said tk_abcdefghijklmnopqrstuvwxyz012"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	fake := &fakePublishers{err: errors.New(secretDetail)}

	recorder := servePublishers(fake, logger, http.MethodPost, publisherPath)

	assertResponse(testInstance, recorder, http.StatusBadGateway, `{"error":"provisioning-api mint failed"}`+"\n")
	if strings.Contains(recorder.Body.String(), "upstream") {
		testInstance.Fatalf("response echoes the error: %s", recorder.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, "provisioning-api mint failed") || !strings.Contains(logged, publisherPath) {
		testInstance.Fatalf("log missing failure or path: %s", logged)
	}
}

func TestPublishersNilFailsClosed(testInstance *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		testInstance.Run(method, func(testInstance *testing.T) {
			recorder := servePublishers(nil, nil, method, publisherPath)
			assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"publisher minting unavailable"}`+"\n")
		})
	}
}

func TestPublishersResponsesAreNotCacheable(testInstance *testing.T) {
	cases := map[string]Publishers{
		"configured": &fakePublishers{view: samplePublisherView()},
		"nil":        nil,
	}
	for name, fake := range cases {
		testInstance.Run(name, func(testInstance *testing.T) {
			recorder := servePublishers(fake, nil, http.MethodGet, publisherPath)
			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				testInstance.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				testInstance.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

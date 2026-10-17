package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const publisherPath = "/v1/admin/apps/urls4irl/ntfy-publisher"

// fakePublishers is a Publishers double that returns canned results and counts calls.
type fakePublishers struct {
	view      publisher.PublisherView
	err       error
	mintCalls int
	getCalls  int
	lastAppID string
}

func (fake *fakePublishers) Mint(_ context.Context, appID string) (publisher.PublisherView, error) {
	fake.mintCalls++
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
	service := publisher.NewService(nil, nil, nil)
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
	for _, method := range []string{http.MethodPost, http.MethodGet} {
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

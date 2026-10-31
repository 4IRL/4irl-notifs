package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appidentity"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
)

const appPublisherPath = "/v1/apps/urls4irl/ntfy-publisher"

type publisherRig struct {
	fake   *fakePublishers
	logs   *bytes.Buffer
	server *Server
}

func newPublisherRig(fake *fakePublishers) *publisherRig {
	rig := &publisherRig{fake: fake, logs: &bytes.Buffer{}}
	var publishers Publishers
	if fake != nil {
		publishers = fake
	}
	rig.server = NewServer(ServerConfig{
		Publishers:  publishers,
		AppIdentity: &fakeAppIdentity{resolved: appidentity.ResolvedKey{ID: 4, AppID: keyApp, Hint: "dak_abcd"}},
		Logger:      slog.New(slog.NewTextHandler(rig.logs, nil)),
	})
	return rig
}

func (rig *publisherRig) post(body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, appPublisherPath, strings.NewReader(body))
	request.Header.Set(authorizationHeader, bearerKey)
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAppMintPublisherNilPublishersIs503BeforeAuth(testInstance *testing.T) {
	rig := newPublisherRig(nil)
	request := httptest.NewRequest(http.MethodPost, appPublisherPath, strings.NewReader("{}"))
	recorder := httptest.NewRecorder()
	rig.server.Handler().ServeHTTP(recorder, request)
	assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"publisher minting unavailable"}`+"\n")
	assertNoStore(testInstance, recorder)
}

func TestAppMintPublisherForwardsRotateAndReturnsTheTokenFreeView(testInstance *testing.T) {
	for _, testCase := range []struct {
		body       string
		wantRotate bool
	}{{`{}`, false}, {`{"rotate":false}`, false}, {`{"rotate":true}`, true}} {
		testInstance.Run(testCase.body, func(subTest *testing.T) {
			rig := newPublisherRig(&fakePublishers{view: samplePublisherView()})

			recorder := rig.post(testCase.body)

			if recorder.Code != http.StatusOK {
				subTest.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if rig.fake.appCalls != 1 || rig.fake.operatorCalls != 0 {
				subTest.Fatalf("Mint=%d MintAsOperator=%d, want 1/0", rig.fake.appCalls, rig.fake.operatorCalls)
			}
			if rig.fake.lastAppID != keyApp || rig.fake.lastRotate != testCase.wantRotate {
				subTest.Fatalf("Mint(%q, %v), want (%q, %v)", rig.fake.lastAppID, rig.fake.lastRotate, keyApp, testCase.wantRotate)
			}
			if body := recorder.Body.String(); strings.Contains(body, `"token"`) || !strings.Contains(body, `"publisher_user_id":"u_publisher"`) {
				subTest.Fatalf("body = %s", body)
			}
			assertNoStore(subTest, recorder)
			if !strings.Contains(rig.logs.String(), "app publisher minted") || !strings.Contains(rig.logs.String(), "app_id="+keyApp) {
				subTest.Fatalf("missing info log: %s", rig.logs.String())
			}
		})
	}
}

func TestAppMintPublisherRejectsBadBodiesBeforeMinting(testInstance *testing.T) {
	for _, testCase := range []struct {
		name, body string
		want       int
	}{
		{"empty", ``, http.StatusBadRequest},
		{"app_id", `{"app_id":"x"}`, http.StatusBadRequest},
		{"bad rotate", `{"rotate":"yes"}`, http.StatusBadRequest},
		{"oversize", `{"rotate":false,"pad":"` + strings.Repeat("a", int(maxAdminBodyBytes)+10) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newPublisherRig(&fakePublishers{view: samplePublisherView()})

			recorder := rig.post(testCase.body)

			if recorder.Code != testCase.want {
				subTest.Fatalf("status = %d, want %d", recorder.Code, testCase.want)
			}
			if rig.fake.mintCalls != 0 {
				subTest.Fatalf("Mint was called for a bad body")
			}
			assertNoStore(subTest, recorder)
		})
	}
}

func TestAppMintPublisherMapsErrors(testInstance *testing.T) {
	boom := errors.New("boom tk_abcdefghijklmnopqrstuvwxyz012")
	for _, testCase := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"validation", &publisher.ValidationError{Message: "invalid app_id"}, 400, `{"error":"invalid app_id"}`},
		{"unavailable", publisher.ErrMintUnavailable, 503, `{"error":"publisher minting unavailable"}`},
		{"post-mint store failure", fmt.Errorf("%w: upsert: %w", publisher.ErrMintUnavailable, boom), 503,
			`{"error":"publisher minting unavailable"}`},
		{"not configured", provisionclient.ErrNotConfigured, 503, `{"error":"publisher minting unavailable"}`},
		{"busy", publisher.ErrMintBusy, 409, `{"error":"publisher mint already in progress"}`},
		{"other", boom, 502, `{"error":"provisioning-api mint failed"}`},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newPublisherRig(&fakePublishers{err: testCase.err})

			recorder := rig.post(`{}`)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
			assertNoStore(subTest, recorder)
			if strings.Contains(recorder.Body.String(), "boom") || strings.Contains(recorder.Body.String(), "tk_") {
				subTest.Fatalf("body echoes the error: %s", recorder.Body.String())
			}
		})
	}
}

func TestAppMintPublisherRateLimitedIs429WithRetryAfter(testInstance *testing.T) {
	rig := newPublisherRig(&fakePublishers{err: &publisher.RateLimitedError{RetryAfter: 90 * time.Second}})

	recorder := rig.post(`{}`)

	assertResponse(testInstance, recorder, http.StatusTooManyRequests, `{"error":"publisher mint rate limit exceeded"}`+"\n")
	if got := recorder.Header().Get("Retry-After"); got != "90" {
		testInstance.Fatalf("Retry-After = %q, want 90", got)
	}
	assertNoStore(testInstance, recorder)
}

func TestPublisherRateLimitRetryAfterRoundsUpAndIsAtLeastOne(testInstance *testing.T) {
	for retryAfter, want := range map[time.Duration]string{time.Millisecond: "1", 1500 * time.Millisecond: "2", 0: "1"} {
		rig := newPublisherRig(&fakePublishers{err: &publisher.RateLimitedError{RetryAfter: retryAfter}})
		recorder := rig.post(`{}`)
		if got := recorder.Header().Get("Retry-After"); got != want {
			testInstance.Fatalf("RetryAfter %v -> %q, want %q", retryAfter, got, want)
		}
	}
}

func TestOperatorMintBusyIs409AndOperatorNeverCallsTheLimitedPath(testInstance *testing.T) {
	fake := &fakePublishers{err: publisher.ErrMintBusy}

	recorder := servePublishers(fake, nil, http.MethodPost, publisherPath)

	assertResponse(testInstance, recorder, http.StatusConflict, `{"error":"publisher mint already in progress"}`+"\n")
	assertNoStore(testInstance, recorder)
	if fake.appCalls != 0 || fake.operatorCalls != 1 {
		testInstance.Fatalf("Mint=%d MintAsOperator=%d, want 0/1", fake.appCalls, fake.operatorCalls)
	}
}

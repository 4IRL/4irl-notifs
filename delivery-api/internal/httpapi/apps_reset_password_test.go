package httpapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/resetlimit"
)

// The production store must satisfy the consumer interface.
var _ ResetLimiter = (*resetlimit.PGStore)(nil)

const resetPasswordPath = "/v1/apps/" + keyApp + "/reset-password"

func resetBody(email string) string { return `{"email":"` + email + `"}` }

func TestAppResetNilLimiterIs503BeforeKeyCheck(testInstance *testing.T) {
	rig := newProvisioningRig(&fakeAppProvisioning{})
	rig.server.resetLimiter = nil

	recorder := rig.post(resetPasswordPath, resetBody("a@example.com"))

	assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"reset limit unavailable"}`+"\n")
	assertNoStore(testInstance, recorder)
	if rig.fake.totalCalls() != 0 {
		testInstance.Fatalf("upstream called %d times", rig.fake.totalCalls())
	}
}

func TestAppResetNilServiceIs503(testInstance *testing.T) {
	recorder := newProvisioningRig(nil).post(resetPasswordPath, resetBody("a@example.com"))
	assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"provisioning unavailable"}`+"\n")
	assertNoStore(testInstance, recorder)
}

func TestAppResetRelaysUpstreamAnswers(testInstance *testing.T) {
	testCases := []struct {
		name       string
		status     int
		body       string
		retryAfter string
	}{
		{name: "200", status: http.StatusOK, body: `{"user_id":"u_x","app_id":"urls4irl","password":"new-pass"}`},
		{name: "400", status: http.StatusBadRequest, body: `{"error":"invalid email"}`},
		{name: "404", status: http.StatusNotFound, body: `{"error":"user does not exist"}`},
		{name: "429", status: http.StatusTooManyRequests, body: `{"error":"too many password changes"}`, retryAfter: "120"},
		{name: "500", status: http.StatusInternalServerError, body: `{"error":"internal error"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppProvisioning{answer: provisionclient.Passthrough{
				Status: testCase.status, Body: []byte(testCase.body), ContentType: "text/html", RetryAfter: testCase.retryAfter,
			}}
			recorder := newProvisioningRig(fake).post(resetPasswordPath, resetBody("a@example.com"))

			if recorder.Code != testCase.status || recorder.Body.String() != testCase.body {
				subTest.Fatalf("got %d %q, want %d %q", recorder.Code, recorder.Body.String(), testCase.status, testCase.body)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				subTest.Fatalf("Content-Type = %q, want application/json", got)
			}
			if got := recorder.Header().Get("Retry-After"); got != testCase.retryAfter {
				subTest.Fatalf("Retry-After = %q, want %q", got, testCase.retryAfter)
			}
			assertNoStore(subTest, recorder)
		})
	}
}

func TestAppResetUpstreamCredentialFailuresBecome502(testInstance *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusFound} {
		testInstance.Run(fmt.Sprint(status), func(subTest *testing.T) {
			fake := &fakeAppProvisioning{answer: provisionclient.Passthrough{Status: status, Body: []byte(`{"secret":"upstream"}`)}}
			recorder := newProvisioningRig(fake).post(resetPasswordPath, resetBody("a@example.com"))

			assertResponse(subTest, recorder, http.StatusBadGateway, `{"error":"provisioning request failed"}`+"\n")
			assertNoStore(subTest, recorder)
		})
	}
}

func TestAppResetRequestValidationNeverReserves(testInstance *testing.T) {
	testCases := []struct {
		name    string
		body    string
		message string
		status  int
	}{
		{name: "empty object", body: `{}`, message: "email required"},
		{name: "empty email", body: resetBody(""), message: "email required"},
		{name: "whitespace email", body: resetBody("   "), message: "email required"},
		{name: "body app_id", body: `{"email":"a@example.com","app_id":"urls4irl"}`, message: "invalid JSON body"},
		{name: "malformed json", body: `{"email":`, message: "invalid JSON body"},
		{name: "oversized", body: `{"email":"` + strings.Repeat("a", maxAdminBodyBytes+1) + `"}`, message: "request body too large", status: http.StatusRequestEntityTooLarge},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newProvisioningRig(&fakeAppProvisioning{})
			recorder := rig.post(resetPasswordPath, testCase.body)

			wantStatus := cmp.Or(testCase.status, http.StatusBadRequest)
			if recorder.Code != wantStatus {
				subTest.Fatalf("status = %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), testCase.message) {
				subTest.Fatalf("body = %q, want it to contain %q", recorder.Body.String(), testCase.message)
			}
			assertNoStore(subTest, recorder)
			if rig.limiter.totalCalls() != 0 || rig.fake.totalCalls() != 0 {
				subTest.Fatalf("reserve %d, upstream %d calls for a refused request", rig.limiter.totalCalls(), rig.fake.totalCalls())
			}
		})
	}
}

func TestAppResetBadlyFormattedEmailStillReservesAndRelays(testInstance *testing.T) {
	rig := newProvisioningRig(&fakeAppProvisioning{answer: provisionclient.Passthrough{
		Status: http.StatusBadRequest, Body: []byte(`{"error":"invalid email"}`),
	}})

	recorder := rig.post(resetPasswordPath, resetBody("not-an-email"))

	if recorder.Code != http.StatusBadRequest {
		testInstance.Fatalf("status = %d, want the upstream 400", recorder.Code)
	}
	if rig.limiter.totalCalls() != 1 || rig.fake.totalCalls() != 1 {
		testInstance.Fatalf("reserve %d, upstream %d calls, want 1 each", rig.limiter.totalCalls(), rig.fake.totalCalls())
	}
}

func TestAppResetForwardsKeyAppAndReservesNormalizedHash(testInstance *testing.T) {
	rig := newProvisioningRig(&fakeAppProvisioning{answer: provisionclient.Passthrough{Status: http.StatusOK, Body: []byte("{}")}})

	rig.post(resetPasswordPath, resetBody("Person@Example.com"))
	rig.post(resetPasswordPath, resetBody("person@example.com"))

	wantHash := personhash.Hash("person@example.com")
	for _, call := range rig.limiter.calls {
		if call != (reserveCall{appID: keyApp, personHash: wantHash}) {
			testInstance.Fatalf("reserve = %+v, want app %q hash %q", call, keyApp, wantHash)
		}
	}
	if len(rig.limiter.calls) != 2 {
		testInstance.Fatalf("reserve calls = %d, want 2", len(rig.limiter.calls))
	}
	want := provisionCall{appID: keyApp, email: "Person@Example.com"}
	if len(rig.fake.calls) != 2 || rig.fake.calls[0] != want {
		testInstance.Fatalf("upstream calls = %+v, want first %+v", rig.fake.calls, want)
	}
}

func TestAppResetRateLimitedIs429WithRoundedRetryAfter(testInstance *testing.T) {
	testCases := []struct {
		wait time.Duration
		want string
	}{
		{wait: time.Millisecond, want: "1"},
		{wait: 1500 * time.Millisecond, want: "2"},
		{wait: 0, want: "1"},
		{wait: 42 * time.Second, want: "42"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.wait.String(), func(subTest *testing.T) {
			fake := &fakeAppProvisioning{}
			rig := newProvisioningRig(fake)
			rig.limiter.err = &resetlimit.RateLimitedError{RetryAfter: testCase.wait, Scope: resetlimit.ScopePerson}

			recorder := rig.post(resetPasswordPath, resetBody("a@example.com"))

			assertResponse(subTest, recorder, http.StatusTooManyRequests, `{"error":"reset rate limit exceeded"}`+"\n")
			assertNoStore(subTest, recorder)
			if got := recorder.Header().Get("Retry-After"); got != testCase.want {
				subTest.Fatalf("Retry-After = %q, want %q", got, testCase.want)
			}
			if fake.totalCalls() != 0 {
				subTest.Fatalf("upstream called %d times for a rate-limited request", fake.totalCalls())
			}
			for _, want := range []string{"level=INFO", "reset attempt rate limited", "app_id=" + keyApp, "scope=person", "retry_after="} {
				if !strings.Contains(rig.logs.String(), want) {
					subTest.Fatalf("log lacks %q: %s", want, rig.logs.String())
				}
			}
		})
	}
}

func TestAppResetLimiterFailuresAre503(testInstance *testing.T) {
	testCases := []struct {
		name      string
		err       error
		wantLog   string
		unwantLog string
	}{
		{name: "store error", err: errors.New("pq: password=hunter2 refused"), wantLog: "level=ERROR"},
		{name: "deadline", err: context.DeadlineExceeded, wantLog: "reset limit store failed"},
		{name: "canceled", err: context.Canceled, wantLog: "level=DEBUG", unwantLog: "level=ERROR"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppProvisioning{}
			rig := newProvisioningRig(fake)
			rig.limiter.err = testCase.err

			recorder := rig.post(resetPasswordPath, resetBody("a@example.com"))

			assertResponse(subTest, recorder, http.StatusServiceUnavailable, `{"error":"reset limit unavailable"}`+"\n")
			assertNoStore(subTest, recorder)
			if got := recorder.Header().Get("Retry-After"); got != "" {
				subTest.Fatalf("Retry-After = %q, want none", got)
			}
			if fake.totalCalls() != 0 {
				subTest.Fatalf("upstream called %d times with the limit store down", fake.totalCalls())
			}
			logged := rig.logs.String()
			if !strings.Contains(logged, testCase.wantLog) || !strings.Contains(logged, "app_id="+keyApp) {
				subTest.Fatalf("log lacks %q or app_id: %s", testCase.wantLog, logged)
			}
			// The store error text is logged (operators need it) but must never reach the response body.
			if testCase.unwantLog != "" && strings.Contains(logged, testCase.unwantLog) {
				subTest.Fatalf("log contains %q: %s", testCase.unwantLog, logged)
			}
			if strings.Contains(recorder.Body.String(), "hunter2") {
				subTest.Fatalf("body leaks database text: %s", recorder.Body.String())
			}
		})
	}
}

func TestAppResetReserveIsNeverUndone(testInstance *testing.T) {
	testCases := []struct {
		name   string
		answer provisionclient.Passthrough
		err    error
	}{
		{name: "404", answer: provisionclient.Passthrough{Status: http.StatusNotFound, Body: []byte("{}")}},
		{name: "500", answer: provisionclient.Passthrough{Status: http.StatusInternalServerError, Body: []byte("{}")}},
		{name: "transport error", err: errors.New("reset-password: request failed")},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newProvisioningRig(&fakeAppProvisioning{answer: testCase.answer, err: testCase.err})
			rig.post(resetPasswordPath, resetBody("a@example.com"))

			if rig.limiter.totalCalls() != 1 {
				subTest.Fatalf("limiter calls = %d, want exactly 1", rig.limiter.totalCalls())
			}
		})
	}
}

func TestAppResetLogsNeverCarrySecrets(testInstance *testing.T) {
	upstreamBody := `{"password":"` + distinctivePassword + `"}`
	wantHash := personhash.Hash(distinctiveEmail)
	testCases := []struct {
		name    string
		answer  provisionclient.Passthrough
		err     error
		limiter error
		wantLog []string
	}{
		{name: "200", answer: provisionclient.Passthrough{Status: http.StatusOK, Body: []byte(upstreamBody)}, wantLog: []string{"upstream_status=200"}},
		{name: "401", answer: provisionclient.Passthrough{Status: http.StatusUnauthorized, Body: []byte(upstreamBody)}, wantLog: []string{"upstream_status=401", "level=ERROR"}},
		{name: "transport error", err: errors.New("reset-password: request failed"), wantLog: []string{"level=ERROR", "error="}},
		{name: "rate limited", limiter: &resetlimit.RateLimitedError{RetryAfter: time.Second, Scope: resetlimit.ScopeApp}, wantLog: []string{"reset attempt rate limited"}},
		{name: "store down", limiter: errors.New("db down"), wantLog: []string{"reset limit store failed"}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newProvisioningRig(&fakeAppProvisioning{answer: testCase.answer, err: testCase.err})
			rig.limiter.err = testCase.limiter
			rig.post(resetPasswordPath, resetBody(distinctiveEmail))

			logged := rig.logs.String()
			for _, want := range append([]string{"app_id=" + keyApp, "person_hash=" + wantHash}, testCase.wantLog...) {
				if !strings.Contains(logged, want) {
					subTest.Fatalf("log lacks %q: %s", want, logged)
				}
			}
			for _, secret := range []string{distinctiveEmail, distinctivePassword} {
				if strings.Contains(logged, secret) {
					subTest.Fatalf("log leaks %q: %s", secret, logged)
				}
			}
		})
	}
}

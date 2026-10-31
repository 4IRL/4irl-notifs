package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

const setPasswordPath = "/v1/apps/" + keyApp + "/set-password"

func setPasswordBody(email string, password string) string {
	return `{"email":"` + email + `","password":"` + password + `"}`
}

func TestAppSetPasswordNilServiceIs503(testInstance *testing.T) {
	recorder := newProvisioningRig(nil).post(setPasswordPath, setPasswordBody("a@example.com", "correct-horse"))
	assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"provisioning unavailable"}`+"\n")
	assertNoStore(testInstance, recorder)
}

func TestAppSetPasswordRelaysUpstreamAnswers(testInstance *testing.T) {
	testCases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "200", status: http.StatusOK, body: `{"user_id":"u_x","app_id":"urls4irl","updated":true}`},
		{name: "400 invalid password", status: http.StatusBadRequest, body: `{"error":"invalid password"}`},
		{name: "404 user does not exist", status: http.StatusNotFound, body: `{"error":"user does not exist"}`},
		{name: "500", status: http.StatusInternalServerError, body: `{"error":"internal error"}`},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppProvisioning{answer: provisionclient.Passthrough{
				Status: testCase.status, Body: []byte(testCase.body), ContentType: "text/html",
			}}
			recorder := newProvisioningRig(fake).post(setPasswordPath, setPasswordBody("a@example.com", "short"))

			if recorder.Code != testCase.status || recorder.Body.String() != testCase.body {
				subTest.Fatalf("got %d %q, want %d %q", recorder.Code, recorder.Body.String(), testCase.status, testCase.body)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				subTest.Fatalf("Content-Type = %q, want application/json", got)
			}
			assertNoStore(subTest, recorder)
		})
	}
}

func TestAppSetPasswordUpstreamCredentialFailuresBecome502(testInstance *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusFound} {
		testInstance.Run(fmt.Sprint(status), func(subTest *testing.T) {
			fake := &fakeAppProvisioning{answer: provisionclient.Passthrough{
				Status: status, Body: []byte(`{"secret":"upstream"}`),
			}}
			recorder := newProvisioningRig(fake).post(setPasswordPath, setPasswordBody("a@example.com", "correct-horse"))

			assertResponse(subTest, recorder, http.StatusBadGateway, `{"error":"provisioning request failed"}`+"\n")
			assertNoStore(subTest, recorder)
		})
	}
}

func TestAppSetPasswordRequestValidation(testInstance *testing.T) {
	testCases := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{name: "empty body object", body: `{}`, status: http.StatusBadRequest, message: "email required"},
		{name: "missing email", body: `{"password":"correct-horse"}`, status: http.StatusBadRequest, message: "email required"},
		{name: "empty email", body: setPasswordBody("", "correct-horse"), status: http.StatusBadRequest, message: "email required"},
		{name: "missing password", body: `{"email":"a@example.com"}`, status: http.StatusBadRequest, message: "password required"},
		{name: "empty password", body: setPasswordBody("a@example.com", ""), status: http.StatusBadRequest, message: "password required"},
		{
			name: "body app_id", body: `{"email":"a@example.com","password":"correct-horse","app_id":"urls4irl"}`,
			status: http.StatusBadRequest, message: "invalid JSON body",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeAppProvisioning{}
			recorder := newProvisioningRig(fake).post(setPasswordPath, testCase.body)

			assertResponse(subTest, recorder, testCase.status, `{"error":"`+testCase.message+`"}`+"\n")
			assertNoStore(subTest, recorder)
			if len(fake.calls) != 0 {
				subTest.Fatalf("upstream called %d times for a refused request", len(fake.calls))
			}
		})
	}
}

func TestAppSetPasswordForwardsOwnerEmailAndPassword(testInstance *testing.T) {
	fake := &fakeAppProvisioning{answer: provisionclient.Passthrough{Status: http.StatusOK, Body: []byte("{}")}}

	recorder := newProvisioningRig(fake).post(setPasswordPath, setPasswordBody("a@example.com", "correct-horse"))

	if recorder.Code != http.StatusOK {
		testInstance.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	want := provisionCall{appID: keyApp, email: "a@example.com", password: "correct-horse"}
	if len(fake.calls) != 1 || fake.calls[0] != want {
		testInstance.Fatalf("calls = %+v, want [%+v]", fake.calls, want)
	}
}

func TestAppSetPasswordLogsNeverCarrySecrets(testInstance *testing.T) {
	upstreamBody := `{"token":"` + distinctiveToken + `"}`
	wantHash := personhash.Hash(distinctiveEmail)
	testCases := []struct {
		name    string
		answer  provisionclient.Passthrough
		err     error
		wantLog []string
	}{
		{
			name:    "200",
			answer:  provisionclient.Passthrough{Status: http.StatusOK, Body: []byte(upstreamBody)},
			wantLog: []string{"upstream_status=200"},
		},
		{
			name:    "401",
			answer:  provisionclient.Passthrough{Status: http.StatusUnauthorized, Body: []byte(upstreamBody)},
			wantLog: []string{"upstream_status=401", "level=ERROR"},
		},
		{
			name:    "transport error",
			err:     errors.New("set-password: request failed"),
			wantLog: []string{"level=ERROR", "error="},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rig := newProvisioningRig(&fakeAppProvisioning{answer: testCase.answer, err: testCase.err})
			rig.post(setPasswordPath, setPasswordBody(distinctiveEmail, distinctivePassword))

			logged := rig.logs.String()
			for _, want := range append([]string{"app_id=" + keyApp, "person_hash=" + wantHash, "set-password"}, testCase.wantLog...) {
				if !strings.Contains(logged, want) {
					subTest.Fatalf("log lacks %q: %s", want, logged)
				}
			}
			for _, secret := range []string{distinctiveEmail, distinctiveToken, distinctivePassword} {
				if strings.Contains(logged, secret) {
					subTest.Fatalf("log leaks %q: %s", secret, logged)
				}
			}
		})
	}
}

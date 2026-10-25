package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	createChecksPath = "/v1/admin/heartbeats"

	errBodyDerivedSchedule = `{"error":"period_seconds and grace_seconds are derived for probe checks"}`
	errBodyInvalidJSONRaw  = `{"error":"invalid JSON body"}`
)

// probeCreateBody builds a probe create body with the given probe object JSON and
// extra top-level members (each starting with a comma).
func probeCreateBody(probeJSON string, extra string) string {
	return `{"app_id":"urls4irl","name":"site_home","destinations":["ops_discord"],"probe":` + probeJSON + extra + `}`
}

const validProbeJSON = `{"url":"https://example.com/health","expected_statuses":[200,204],"timeout_seconds":5,"interval_seconds":300}`

// probeJSONWith returns validProbeJSON with replacement members: it is built from
// the fields so one rule changes at a time.
func probeJSONWith(url, statuses, bodyMatch, timeout, interval, accessToken string) string {
	members := []string{
		`"url":` + url,
		`"expected_statuses":` + statuses,
		`"timeout_seconds":` + timeout,
		`"interval_seconds":` + interval,
	}
	if bodyMatch != "" {
		members = append(members, `"body_match":`+bodyMatch)
	}
	if accessToken != "" {
		members = append(members, `"access_token":`+accessToken)
	}
	return "{" + strings.Join(members, ",") + "}"
}

func probeJSONWithURL(url string) string {
	return probeJSONWith(url, "[200]", "", "5", "300", "")
}

func probeJSONWithToken(token string) string {
	return probeJSONWith(`"https://example.com"`, "[200]", "", "5", "300", token)
}

func TestProbeCreateRejectsEachInvalidRuleByName(testInstance *testing.T) {
	longValue := strings.Repeat("a", 257)
	testCases := []struct {
		name     string
		body     string
		wantBody string
		// handlerRejects is true when the handler answers before the service.
		handlerRejects bool
	}{
		{name: "invalid url", body: probeCreateBody(probeJSONWithURL(`"not a url"`), ""), wantBody: `{"error":"invalid probe.url"}`},
		{name: "http url", body: probeCreateBody(probeJSONWithURL(`"http://example.com"`), ""), wantBody: `{"error":"invalid probe.url"}`},
		{name: "ip literal host", body: probeCreateBody(probeJSONWithURL(`"https://127.0.0.1/"`), ""), wantBody: `{"error":"invalid probe.url"}`},
		{name: "localhost host", body: probeCreateBody(probeJSONWithURL(`"https://localhost/"`), ""), wantBody: `{"error":"invalid probe.url"}`},
		{name: "empty statuses", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[]", "", "5", "300", ""), ""), wantBody: `{"error":"invalid probe.expected_statuses"}`},
		{name: "status below 100", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[99]", "", "5", "300", ""), ""), wantBody: `{"error":"invalid probe.expected_statuses"}`},
		{name: "status above 599", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[600]", "", "5", "300", ""), ""), wantBody: `{"error":"invalid probe.expected_statuses"}`},
		{name: "timeout 0", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "", "0", "300", ""), ""), wantBody: `{"error":"invalid probe.timeout_seconds"}`},
		{name: "timeout 9", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "", "9", "300", ""), ""), wantBody: `{"error":"invalid probe.timeout_seconds"}`},
		{name: "interval 59", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "", "5", "59", ""), ""), wantBody: `{"error":"invalid probe.interval_seconds"}`},
		{name: "interval 86401", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "", "5", "86401", ""), ""), wantBody: `{"error":"invalid probe.interval_seconds"}`},
		{name: "body match too long", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", `"`+longValue+`"`, "5", "300", ""), ""), wantBody: `{"error":"invalid probe.body_match"}`},

		{name: "period present", body: probeCreateBody(validProbeJSON, `,"period_seconds":300`), wantBody: errBodyDerivedSchedule},
		{name: "period zero", body: probeCreateBody(validProbeJSON, `,"period_seconds":0`), wantBody: errBodyDerivedSchedule},
		{name: "period null", body: probeCreateBody(validProbeJSON, `,"period_seconds":null`), wantBody: errBodyDerivedSchedule},
		{name: "grace present", body: probeCreateBody(validProbeJSON, `,"grace_seconds":1200`), wantBody: errBodyDerivedSchedule},
		{name: "grace zero", body: probeCreateBody(validProbeJSON, `,"grace_seconds":0`), wantBody: errBodyDerivedSchedule},
		{name: "grace null", body: probeCreateBody(validProbeJSON, `,"grace_seconds":null`), wantBody: errBodyDerivedSchedule},

		{
			name:     "bad app_id beats schedule presence",
			body:     `{"app_id":"Bad Id","name":"site_home","destinations":["d"],"period_seconds":0,"probe":` + validProbeJSON + `}`,
			wantBody: `{"error":"invalid app_id"}`,
		},
		{
			name:     "bad name beats schedule presence",
			body:     `{"app_id":"urls4irl","name":"Bad-Name","destinations":["d"],"grace_seconds":0,"probe":` + validProbeJSON + `}`,
			wantBody: `{"error":"invalid name"}`,
		},
		{
			name:     "schedule presence beats probe config",
			body:     probeCreateBody(probeJSONWithURL(`"http://example.com"`), `,"period_seconds":300`),
			wantBody: errBodyDerivedSchedule,
		},

		{name: "half token", body: probeCreateBody(probeJSONWithToken(`{"client_id":"id"}`), ""), wantBody: `{"error":"invalid probe.access_token"}`},
		{name: "empty client_id", body: probeCreateBody(probeJSONWithToken(`{"client_id":"","client_secret":"s"}`), ""), wantBody: `{"error":"invalid probe.access_token"}`},
		{name: "CR in client_id", body: probeCreateBody(probeJSONWithToken(`{"client_id":"a\rb","client_secret":"s"}`), ""), wantBody: `{"error":"invalid probe.access_token"}`},
		{name: "LF in client_secret", body: probeCreateBody(probeJSONWithToken(`{"client_id":"id","client_secret":"a\nb"}`), ""), wantBody: `{"error":"invalid probe.access_token"}`},
		{name: "257 byte client_id", body: probeCreateBody(probeJSONWithToken(`{"client_id":"`+longValue+`","client_secret":"s"}`), ""), wantBody: `{"error":"invalid probe.access_token"}`},
		{name: "257 byte client_secret", body: probeCreateBody(probeJSONWithToken(`{"client_id":"id","client_secret":"`+longValue+`"}`), ""), wantBody: `{"error":"invalid probe.access_token"}`},
		{name: "empty token object", body: probeCreateBody(probeJSONWithToken(`{}`), ""), wantBody: `{"error":"invalid probe.access_token"}`},

		{name: "probe empty object", body: probeCreateBody(`{}`, ""), wantBody: `{"error":"invalid probe.url"}`},

		{name: "unknown probe.failure_threshold", body: probeCreateBody(strings.Replace(validProbeJSON, "{", `{"failure_threshold":3,`, 1), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "unknown probe.has_access_token", body: probeCreateBody(strings.Replace(validProbeJSON, "{", `{"has_access_token":true,`, 1), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "unknown probe.consecutive_failures", body: probeCreateBody(strings.Replace(validProbeJSON, "{", `{"consecutive_failures":1,`, 1), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "unknown probe.last_result", body: probeCreateBody(strings.Replace(validProbeJSON, "{", `{"last_result":null,`, 1), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "unknown access_token key", body: probeCreateBody(probeJSONWithToken(`{"client_id":"id","client_secret":"s","extra":1}`), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "unknown top-level key", body: probeCreateBody(validProbeJSON, `,"bogus":1`), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},

		{name: "probe string", body: probeCreateBody(`"yes"`, ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "probe number", body: probeCreateBody(`5`, ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "probe bool", body: probeCreateBody(`true`, ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "probe array", body: probeCreateBody(`[]`, ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "url a number", body: probeCreateBody(probeJSONWithURL(`5`), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "statuses a string", body: probeCreateBody(probeJSONWith(`"https://example.com"`, `"200"`, "", "5", "300", ""), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "statuses hold a string", body: probeCreateBody(probeJSONWith(`"https://example.com"`, `["200"]`, "", "5", "300", ""), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "timeout a string", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "", `"5"`, "300", ""), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "timeout a fraction", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "", "5.5", "300", ""), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "interval a fraction", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "", "5", "300.5", ""), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "body_match a number", body: probeCreateBody(probeJSONWith(`"https://example.com"`, "[200]", "5", "5", "300", ""), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "access_token a string", body: probeCreateBody(probeJSONWithToken(`"tok"`), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "access_token an array", body: probeCreateBody(probeJSONWithToken(`[]`), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "client_id a number", body: probeCreateBody(probeJSONWithToken(`{"client_id":1,"client_secret":"s"}`), ""), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "trailing data", body: probeCreateBody(validProbeJSON, "") + `{}`, wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "period a string", body: probeCreateBody(validProbeJSON, `,"period_seconds":"300"`), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
		{name: "grace a fraction", body: probeCreateBody(validProbeJSON, `,"grace_seconds":1200.5`), wantBody: errBodyInvalidJSONRaw, handlerRejects: true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := allResults(sampleProbeCheck())

			recorder := serveHeartbeats(fake, nil, http.MethodPost, createChecksPath, testCase.body)

			assertResponse(subTest, recorder, http.StatusBadRequest, testCase.wantBody+"\n")
			if testCase.handlerRejects && fake.totalCalls() != 0 {
				subTest.Fatalf("the service was called for a body the handler refuses: %+v", fake.createCalls)
			}
		})
	}
}

func TestPushCreateScheduleFieldTypesAndPresence(testInstance *testing.T) {
	const tail = `,"destinations":["ops_discord"]}`
	testCases := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		{name: "string period", body: `{"app_id":"a","name":"n","period_seconds":"60","grace_seconds":60` + tail, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSONRaw, wantCalls: 0},
		{name: "fraction period", body: `{"app_id":"a","name":"n","period_seconds":60.5,"grace_seconds":60` + tail, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSONRaw, wantCalls: 0},
		{name: "string grace", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":"60"` + tail, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSONRaw, wantCalls: 0},
		{name: "fraction grace", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60.5` + tail, wantStatus: http.StatusBadRequest, wantBody: errBodyInvalidJSONRaw, wantCalls: 0},
		{name: "absent period", body: `{"app_id":"a","name":"n","grace_seconds":60` + tail, wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid period_seconds"}`, wantCalls: 1},
		{name: "null period", body: `{"app_id":"a","name":"n","period_seconds":null,"grace_seconds":60` + tail, wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid period_seconds"}`, wantCalls: 1},
		{name: "absent grace", body: `{"app_id":"a","name":"n","period_seconds":60` + tail, wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid grace_seconds"}`, wantCalls: 1},
		{name: "null grace", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":null` + tail, wantStatus: http.StatusBadRequest, wantBody: `{"error":"invalid grace_seconds"}`, wantCalls: 1},
		{name: "valid pair", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60` + tail, wantStatus: http.StatusCreated, wantBody: `{"check":` + sampleCheckJSON + `}`, wantCalls: 1},
		{name: "null probe is push", body: `{"app_id":"a","name":"n","period_seconds":60,"grace_seconds":60,"probe":null` + tail, wantStatus: http.StatusCreated, wantBody: `{"check":` + sampleCheckJSON + `}`, wantCalls: 1},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := allResults(sampleCheck())

			recorder := serveHeartbeats(fake, nil, http.MethodPost, createChecksPath, testCase.body)

			assertResponse(subTest, recorder, testCase.wantStatus, testCase.wantBody+"\n")
			if fake.totalCalls() != testCase.wantCalls {
				subTest.Fatalf("service calls = %d, want %d", fake.totalCalls(), testCase.wantCalls)
			}
		})
	}
}

func TestCreatePassesAProbeBodyToTheService(testInstance *testing.T) {
	bodyMatch := "ok"
	testCases := []struct {
		name string
		body string
		want heartbeat.NewCheck
	}{
		{
			name: "probe without token",
			body: probeCreateBody(validProbeJSON, ""),
			want: heartbeat.NewCheck{
				AppID: "urls4irl", Name: "site_home", DestinationNames: []string{"ops_discord"},
				OwnerKind: registry.OwnerOperator,
				Probe: &heartbeat.ProbeConfig{
					URL: "https://example.com/health", ExpectedStatuses: []int{200, 204},
					TimeoutSeconds: 5, IntervalSeconds: 300,
				},
			},
		},
		{
			name: "probe with body match and token",
			body: probeCreateBody(
				probeJSONWith(`"https://example.com/health"`, "[200,204]", `"ok"`, "5", "300", `{"client_id":"id","client_secret":"secret"}`), ""),
			want: heartbeat.NewCheck{
				AppID: "urls4irl", Name: "site_home", DestinationNames: []string{"ops_discord"},
				OwnerKind: registry.OwnerOperator,
				Probe: &heartbeat.ProbeConfig{
					URL: "https://example.com/health", ExpectedStatuses: []int{200, 204}, BodyMatch: &bodyMatch,
					TimeoutSeconds: 5, IntervalSeconds: 300,
				},
				AccessToken: &heartbeat.AccessToken{ClientID: "id", ClientSecret: "secret"},
			},
		},
		{
			name: "null access_token",
			body: probeCreateBody(probeJSONWithToken(`null`), ""),
			want: heartbeat.NewCheck{
				AppID: "urls4irl", Name: "site_home", DestinationNames: []string{"ops_discord"},
				OwnerKind: registry.OwnerOperator,
				Probe: &heartbeat.ProbeConfig{
					URL: "https://example.com", ExpectedStatuses: []int{200}, TimeoutSeconds: 5, IntervalSeconds: 300,
				},
			},
		},
		{
			name: "null probe is a push check",
			body: `{"app_id":"urls4irl","name":"nightly_backup","period_seconds":86400,"grace_seconds":7200,"destinations":["ops_discord"],"probe":null}`,
			want: heartbeat.NewCheck{
				AppID: "urls4irl", Name: "nightly_backup", PeriodSeconds: 86400, GraceSeconds: 7200,
				DestinationNames: []string{"ops_discord"}, OwnerKind: registry.OwnerOperator,
				ScheduleSupplied: true,
			},
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fake := &fakeHeartbeats{createResult: sampleProbeCheck()}

			recorder := serveHeartbeats(fake, nil, http.MethodPost, createChecksPath, testCase.body)

			if recorder.Code != http.StatusCreated {
				subTest.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
			}
			if !reflect.DeepEqual(fake.createCalls, []heartbeat.NewCheck{testCase.want}) {
				subTest.Fatalf("create calls = %+v, want %+v", fake.createCalls, []heartbeat.NewCheck{testCase.want})
			}
		})
	}
}

func TestProbeCreateRespondsWithTheProbeView(testInstance *testing.T) {
	recorder := serveHeartbeats(allResults(sampleProbeCheck()), nil, http.MethodPost, createChecksPath,
		probeCreateBody(validProbeJSON, ""))

	assertResponse(testInstance, recorder, http.StatusCreated, `{"check":`+sampleProbeCheckJSON+"}\n")
}

func TestProbeCreateNeverEchoesOrLogsTheCredentials(testInstance *testing.T) {
	const (
		clientID     = "SECRET-CLIENT-ID-9f3"
		clientSecret = "SECRET-CLIENT-SECRET-7c1"
	)
	token := `{"client_id":"` + clientID + `","client_secret":"` + clientSecret + `"}`
	testCases := []struct {
		name       string
		fake       *fakeHeartbeats
		probe      string
		wantStatus int
	}{
		{name: "created", fake: allResults(sampleProbeCheck()), probe: probeJSONWithToken(token), wantStatus: http.StatusCreated},
		{
			name: "bad url", fake: allResults(sampleProbeCheck()),
			probe:      probeJSONWith(`"http://example.com"`, "[200]", "", "5", "300", token),
			wantStatus: http.StatusBadRequest,
		},
		{name: "conflict", fake: failingHeartbeats(heartbeat.ErrCheckExists), probe: probeJSONWithToken(token), wantStatus: http.StatusConflict},
		{name: "internal error", fake: failingHeartbeats(errors.New("boom")), probe: probeJSONWithToken(token), wantStatus: http.StatusInternalServerError},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

			recorder := serveHeartbeats(testCase.fake, logger, http.MethodPost, createChecksPath, probeCreateBody(testCase.probe, ""))

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			for _, secret := range []string{clientID, clientSecret} {
				if strings.Contains(recorder.Body.String(), secret) {
					subTest.Fatalf("response leaked %q: %s", secret, recorder.Body.String())
				}
				if strings.Contains(logBuffer.String(), secret) {
					subTest.Fatalf("log leaked %q: %s", secret, logBuffer.String())
				}
			}
		})
	}
}

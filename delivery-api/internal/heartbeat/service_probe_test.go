package heartbeat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// probeDownCheck is a pending down alert for a probe check with the given down
// reason and detail (nil detail = none).
func probeDownCheck(reason *string, detail *string) StoredCheck {
	check := pendingCheck(AlertDown, "ops_ntfy")
	check.State = StateDown
	check.DownSince = timePointer(downSinceTime)
	check.DownReason = reason
	check.LastPingAt = timePointer(lastPingTime)
	check.Probe = &ProbeState{Config: validProbeConfigForTest(), DownDetail: detail}
	return check
}

func TestTickProbeDownBodies(t *testing.T) {
	testCases := []struct {
		name   string
		check  StoredCheck
		want   string
		reason any
	}{
		{"http_status with detail", probeDownCheck(stringPointer(ReasonHTTPStatus), stringPointer("got 503")), "Probe failed (unexpected HTTP status): got 503.", "http_status"},
		{"http_status no detail", probeDownCheck(stringPointer(ReasonHTTPStatus), nil), "Probe failed (unexpected HTTP status).", "http_status"},
		{"timeout", probeDownCheck(stringPointer(ReasonTimeout), stringPointer("after 5s")), "Probe failed (timed out): after 5s.", "timeout"},
		{"body_mismatch", probeDownCheck(stringPointer(ReasonBodyMismatch), nil), "Probe failed (body did not match).", "body_mismatch"},
		{"blocked", probeDownCheck(stringPointer(ReasonBlocked), nil), "Probe failed (blocked address).", "blocked"},
		{"tls", probeDownCheck(stringPointer(ReasonTLS), nil), "Probe failed (TLS error).", "tls"},
		{"scheme", probeDownCheck(stringPointer(ReasonScheme), nil), "Probe failed (non-HTTPS URL).", "scheme"},
		{"network", probeDownCheck(stringPointer(ReasonNetwork), nil), "Probe failed (network error).", "network"},
		{"config", probeDownCheck(stringPointer(ReasonConfig), nil), "Probe failed (access credential error).", "config"},
		{"missed", probeDownCheck(stringPointer(ReasonMissed), nil), "Check went down at 2026-10-19T08:59:00Z; no probe result was recorded (the prober may be stalled).", "missed"},
		{"unknown reason", probeDownCheck(stringPointer("mystery"), nil), "Check is down; reason unrecognised.", "mystery"},
		{"nil reason", probeDownCheck(nil, nil), "Check is down; reason unrecognised.", nil},
		{"fail is not a probe reason", probeDownCheck(stringPointer(ReasonFail), nil), "Check is down; reason unrecognised.", "fail"},
		{"push nil reason", func() StoredCheck { check := downMissedCheck("ops_ntfy"); check.DownReason = nil; return check }(), "Check is down; reason unrecognised.", nil},
		{"push unknown reason", func() StoredCheck {
			check := downMissedCheck("ops_ntfy")
			check.DownReason = stringPointer("mystery")
			return check
		}(), "Check is down; reason unrecognised.", "mystery"},
		{"push with a probe reason", func() StoredCheck {
			check := downMissedCheck("ops_ntfy")
			check.DownReason = stringPointer(ReasonTimeout)
			return check
		}(), "Check is down; reason unrecognised.", "timeout"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &fakeStore{pending: []StoredCheck{testCase.check}}
			messages := &fakeMessages{}
			service, _ := newTestService(t, store, messages)
			if err := service.Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}
			envelope := messages.submitted()[0]
			if envelope.Display.Body != testCase.want {
				t.Errorf("body = %q, want %q", envelope.Display.Body, testCase.want)
			}
			if strings.Contains(envelope.Display.Body, "No ping since") {
				t.Errorf("body %q must not claim a missing ping", envelope.Display.Body)
			}
			if got := decodePayload(t, envelope)["reason"]; got != testCase.reason {
				t.Errorf("payload reason = %v, want %v", got, testCase.reason)
			}
		})
	}
}

func TestTickProbeRecoveredBody(t *testing.T) {
	check := recoveredCheck("ops_ntfy")
	check.Probe = &ProbeState{Config: validProbeConfigForTest()}
	store := &fakeStore{pending: []StoredCheck{check}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)
	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	want := "Probe succeeded at 2026-10-19T06:30:00Z; down since 2026-10-19T08:59:00Z."
	if got := messages.submitted()[0].Display.Body; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestTickProbeAlertNeverCarriesAURLQueryOrCredential(t *testing.T) {
	const secretQuery = "token=hunter2"
	const credential = "s3cr3t-client-secret"
	detail := "Get https://user:pass@probe.example.com/private/path?" + secretQuery + "#frag refused"
	check := probeDownCheck(stringPointer(ReasonNetwork), stringPointer(detail))
	check.Probe.Config.URL = "https://probe.example.com/health?" + secretQuery
	check.Probe.HasAccessToken = true
	check.Probe.SealedAccessToken = []byte(credential)
	store := &fakeStore{pending: []StoredCheck{check}}
	messages := &fakeMessages{}
	service, logs := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	envelope := messages.submitted()[0]
	if want := "Probe failed (network error): Get https://probe.example.com refused."; envelope.Display.Body != want {
		t.Errorf("body = %q, want %q", envelope.Display.Body, want)
	}
	for _, forbidden := range []string{secretQuery, credential, "user:pass", "private/path", "frag"} {
		for label, haystack := range map[string]string{
			"body": envelope.Display.Body, "title": envelope.Display.Title,
			"payload": string(envelope.Payload), "log": logs.String(),
		} {
			if strings.Contains(haystack, forbidden) {
				t.Errorf("%s contains %q: %s", label, forbidden, haystack)
			}
		}
	}
}

func TestHoldOffUntilIsStartPlusStartupHoldOff(t *testing.T) {
	start := serviceNow.Add(-time.Hour)
	service, _, _ := newServiceStartedAt(t, &fakeStore{}, &fakeMessages{}, start)
	if got, want := service.HoldOffUntil(), start.Add(StartupHoldOff); !got.Equal(want) {
		t.Errorf("HoldOffUntil = %v, want %v", got, want)
	}
}

func TestCreateWithProbeConfig(t *testing.T) {
	store := &fakeStore{}
	service, _ := newTestService(t, store, &fakeMessages{})
	ctx := context.Background()
	config := validProbeConfigForTest()

	badConfig := validProbeConfigForTest()
	badConfig.URL = "http://insecure.example.com"
	_, badErr := service.Create(ctx, NewCheck{AppID: "myapp", Name: "site", DestinationNames: []string{"ops"}, Probe: &badConfig})
	assertValidationMessage(t, badErr, "invalid probe.url")
	if len(store.createCalls) != 0 {
		t.Fatal("an invalid probe check reached the store")
	}

	if _, err := service.Create(ctx, NewCheck{
		AppID: "myapp", Name: "site", DestinationNames: []string{"ops"},
		Probe: &config, AccessToken: &AccessToken{ClientID: "id", ClientSecret: "secret"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.createCalls) != 1 {
		t.Errorf("create calls = %d, want 1", len(store.createCalls))
	}
}

func TestCreateRejectsProbeConfigOnAnAppOwnedCheck(t *testing.T) {
	appOwner := "some_app"
	config := validProbeConfigForTest()
	token := AccessToken{ClientID: "id", ClientSecret: "secret"}
	testCases := []struct {
		name  string
		check NewCheck
	}{
		{"probe", NewCheck{Probe: &config}},
		{"access token", NewCheck{PeriodSeconds: 600, GraceSeconds: 300, AccessToken: &token}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &fakeStore{}
			service, _ := newTestService(t, store, &fakeMessages{})
			check := testCase.check
			check.AppID, check.Name, check.DestinationNames = "myapp", "site", []string{"ops"}
			check.OwnerKind, check.OwnerAppID = registry.OwnerApp, &appOwner
			_, err := service.Create(context.Background(), check)
			assertValidationMessage(t, err, "probe config is not allowed on an app-owned check")
			if len(store.createCalls) != 0 {
				t.Error("an app-owned probe check reached the store")
			}
		})
	}
}

func TestPingAndFailReturnTheProbeCheckSentinelUnwrapped(t *testing.T) {
	store := &fakeStore{pingErr: ErrProbeCheckPing}
	service, _ := newTestService(t, store, &fakeMessages{})
	if _, err := service.Ping(context.Background(), "billing", "nightly"); !errors.Is(err, ErrProbeCheckPing) || err != ErrProbeCheckPing { //nolint:errorlint // asserting the sentinel is returned unwrapped
		t.Errorf("Ping = %v, want ErrProbeCheckPing unwrapped", err)
	}
	if _, err := service.Fail(context.Background(), "billing", "nightly"); !errors.Is(err, ErrProbeCheckPing) || err != ErrProbeCheckPing { //nolint:errorlint // asserting the sentinel is returned unwrapped
		t.Errorf("Fail = %v, want ErrProbeCheckPing unwrapped", err)
	}
}

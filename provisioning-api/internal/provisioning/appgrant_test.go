package provisioning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/enforcement"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
)

const grantCheckLogMessage = "app grant check"

// newGrantService builds a service in the given grant mode over a stateful
// person store that already holds alice's row.
func newGrantService(ntfyClient *fakeNtfyClient, personClient *fakePersonClient, mode enforcement.Mode, logger *slog.Logger) *Service {
	return NewService(ServiceConfig{
		Client:                     ntfyClient,
		GeneratePassword:           func() (string, error) { return "generated-pw", nil },
		GenerateSubscriberPassword: func() (string, error) { return subscriberPassword, nil },
		PersonClient:               personClient,
		Logger:                     logger,
		AppGrantMode:               mode,
	})
}

// hasMutatingCall reports whether any recorded call changed state: a ntfy
// mutation or any person-store call that claims a slot or stamp.
func hasMutatingCall(invocations []string) bool {
	for _, mutatingPrefix := range []string{"ChangePassword(", "ResetAccess(", "DeleteUser(", "ClaimResetSlot(", "ClaimPassword("} {
		if hasInvocationPrefix(invocations, mutatingPrefix) {
			return true
		}
	}
	return false
}

// guardedRoute runs one of the three guarded service methods for alice in the
// "urls4irl" app.
type guardedRoute struct {
	name string
	run  func(service *Service) error
}

var guardedRoutes = []guardedRoute{
	{grantRouteResetPassword, func(service *Service) error {
		_, err := service.ResetPassword(context.Background(), ResetPasswordRequest{AppID: "urls4irl", Email: aliceEmail})
		return err
	}},
	{grantRouteSetPassword, func(service *Service) error {
		_, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "urls4irl", Email: aliceEmail, Password: "sekrit-pw"})
		return err
	}},
	{grantRouteDeprovision, func(service *Service) error {
		return service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser})
	}},
}

// grantExpectation is what one (route, mode, scenario) must do.
type grantExpectation struct {
	rejected  bool // returns ErrPersonNotInApp
	lookupErr bool // returns the scripted lookup error
	decision  string
	reason    string
	wantLevel string // slog level of the decision line: INFO, WARN or ERROR
	mutates   bool   // some state-changing call ran
}

type grantScenario struct {
	name   string
	client func(lookupErr error) *fakeNtfyClient
	expect func(route string, mode enforcement.Mode) grantExpectation
}

func TestAppGrantDecisionForEveryRouteAndMode(t *testing.T) {
	lookupErr := errors.New("ntfy access exploded")
	scenarios := []grantScenario{
		{
			name: "grant held",
			client: func(error) *fakeNtfyClient {
				return &fakeNtfyClient{listUsers: aliceGrantedIn("urls4irl", "chores4irl")}
			},
			expect: func(string, enforcement.Mode) grantExpectation {
				return grantExpectation{decision: "allow", reason: "grant_held", wantLevel: "INFO", mutates: true}
			},
		},
		{
			name: "found but holds no grant for the app",
			client: func(error) *fakeNtfyClient {
				return &fakeNtfyClient{listUsers: aliceGrantedIn("chores4irl")}
			},
			expect: func(_ string, mode enforcement.Mode) grantExpectation {
				if mode == enforcement.LogOnly {
					return grantExpectation{decision: "would_reject", reason: "no_grant", wantLevel: "WARN", mutates: true}
				}
				return grantExpectation{rejected: true, decision: "rejected", reason: "no_grant", wantLevel: "WARN"}
			},
		},
		{
			name: "ntfy user does not exist",
			client: func(error) *fakeNtfyClient {
				return &fakeNtfyClient{}
			},
			expect: func(route string, _ enforcement.Mode) grantExpectation {
				if route == grantRouteDeprovision {
					// The existing already-gone no-op, in either mode: never a rejection.
					return grantExpectation{decision: "already_gone", reason: "user_not_found", wantLevel: "INFO"}
				}
				// Same 404 today's ChangePassword gives an absent user, in either mode.
				return grantExpectation{rejected: true, decision: "rejected", reason: "user_not_found", wantLevel: "WARN"}
			},
		},
		{
			name: "lookup fails",
			client: func(scriptedErr error) *fakeNtfyClient {
				// Only the grant check's lookup (call 0) fails; Deprovision's later
				// last-app lookup sees the fixture.
				return &fakeNtfyClient{listUsers: aliceGrantedIn("urls4irl", "chores4irl"), getUserErrs: []error{scriptedErr}}
			},
			expect: func(_ string, mode enforcement.Mode) grantExpectation {
				if mode == enforcement.LogOnly {
					return grantExpectation{decision: "lookup_failed", reason: "lookup_error", wantLevel: "WARN", mutates: true}
				}
				return grantExpectation{lookupErr: true, decision: "lookup_failed", reason: "lookup_error", wantLevel: "ERROR"}
			},
		},
	}

	for _, route := range guardedRoutes {
		for _, mode := range []enforcement.Mode{enforcement.Enforce, enforcement.LogOnly} {
			for _, scenario := range scenarios {
				t.Run(fmt.Sprintf("%s/%s/%s", route.name, mode, scenario.name), func(t *testing.T) {
					want := scenario.expect(route.name, mode)
					logger, buffer := newJSONLogger()
					ntfyClient := scenario.client(lookupErr)
					personClient := newStatefulPersonClient()
					seedRow(personClient, "earlier-stamp")
					personClient.recordTo = &ntfyClient.invocations
					service := newGrantService(ntfyClient, personClient, mode, logger)

					err := route.run(service)

					switch {
					case want.rejected:
						if !errors.Is(err, ErrPersonNotInApp) || !errors.Is(err, ntfycli.ErrNotFound) {
							t.Fatalf("err = %v, expected ErrPersonNotInApp satisfying ErrNotFound", err)
						}
					case want.lookupErr:
						if !errors.Is(err, lookupErr) {
							t.Fatalf("err = %v, expected the lookup error to propagate (fail closed)", err)
						}
					default:
						if err != nil {
							t.Fatalf("err = %v, expected success", err)
						}
					}
					invocations := snapshotInvocations(&ntfyClient.invocations)
					if got := hasMutatingCall(invocations); got != want.mutates {
						t.Fatalf("mutating call recorded = %v, want %v: %s", got, want.mutates, strings.Join(invocations, " | "))
					}
					if invocations[0] != fmt.Sprintf("GetUser(%s)", aliceNtfyUser) {
						t.Fatalf("the grant lookup must come before anything else, got: %s", strings.Join(invocations, " | "))
					}

					decisions := recordsWithMessage(logRecords(t, buffer), grantCheckLogMessage)
					if len(decisions) != 1 {
						t.Fatalf("expected exactly one %q line, got %v", grantCheckLogMessage, decisions)
					}
					record := decisions[0]
					for field, wantValue := range map[string]string{
						"mode":        mode.String(),
						"route":       route.name,
						"app_id":      "urls4irl",
						"person_hash": aliceHash,
						"decision":    want.decision,
						"reason":      want.reason,
						"level":       want.wantLevel,
					} {
						if record[field] != wantValue {
							t.Fatalf("log field %s = %v, want %q (record %v)", field, record[field], wantValue, record)
						}
					}
					for _, secret := range []string{aliceEmail, "sekrit-pw", subscriberPassword, "generated-pw"} {
						if strings.Contains(buffer.String(), secret) {
							t.Fatalf("log leaked %q: %s", secret, buffer.String())
						}
					}
				})
			}
		}
	}
}

// A rejected request must not burn a reset slot, claim a stamp, take the person
// lock's store calls or touch ntfy: the grant lookup is the only call anywhere.
func TestRejectedRequestMakesNoCallBeyondTheGrantLookup(t *testing.T) {
	for _, route := range guardedRoutes {
		t.Run(route.name, func(t *testing.T) {
			ntfyClient := &fakeNtfyClient{listUsers: aliceGrantedIn("chores4irl")}
			personClient := newStatefulPersonClient()
			seedRow(personClient, "earlier-stamp")
			personClient.recordTo = &ntfyClient.invocations
			service := newGrantService(ntfyClient, personClient, enforcement.Enforce, nil)

			if err := route.run(service); !errors.Is(err, ErrPersonNotInApp) {
				t.Fatalf("err = %v, expected ErrPersonNotInApp", err)
			}
			requireOnlyGrantLookup(t, snapshotInvocations(&ntfyClient.invocations))
			if slotCount(personClient) != 0 {
				t.Fatalf("a rejected request must not claim a reset slot, have %d", slotCount(personClient))
			}
			if stamp, _ := stampOf(personClient); stamp != "earlier-stamp" {
				t.Fatalf("stamp = %q, expected untouched", stamp)
			}
		})
	}
}

func TestErrPersonNotInAppSatisfiesIsWhenWrapped(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", ErrPersonNotInApp)
	if !errors.Is(wrapped, ErrPersonNotInApp) {
		t.Fatal("errors.Is must hold for the sentinel itself, wrapped")
	}
	if !errors.Is(wrapped, ntfycli.ErrNotFound) {
		t.Fatal("errors.Is(ErrNotFound) must hold so the HTTP layer answers 404")
	}
	if errors.Is(ErrPersonNotInApp, ErrTooManyPasswordChanges) {
		t.Fatal("the sentinel must not satisfy unrelated sentinels")
	}
}

func TestAppGrantModeZeroValueIsEnforce(t *testing.T) {
	service := NewService(ServiceConfig{Client: &fakeNtfyClient{listUsers: aliceGrantedIn("chores4irl")}})
	if service.appGrantMode != enforcement.Enforce {
		t.Fatalf("appGrantMode = %v, expected the fail-closed enforce zero value", service.appGrantMode)
	}
	_, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "urls4irl", Email: aliceEmail, Password: "sekrit-pw"})
	if !errors.Is(err, ErrPersonNotInApp) {
		t.Fatalf("err = %v, expected an unconfigured mode to reject", err)
	}
}

// TestResetPasswordIsEmailGlobalAcrossApps mirrors the set-password test: the
// password is one per person, so reset succeeds for any app the person holds a
// grant in, not only the one they were first provisioned into.
func TestResetPasswordIsEmailGlobalAcrossApps(t *testing.T) {
	ntfyClient := &fakeNtfyClient{listUsers: aliceGrantedIn("urls4irl", "tasktracker")}
	service, _ := newResetService(ntfyClient)

	result, err := service.ResetPassword(context.Background(), ResetPasswordRequest{AppID: "tasktracker", Email: aliceEmail})
	if err != nil {
		t.Fatalf("ResetPassword returned unexpected error: %v", err)
	}
	if result.UserID != aliceNtfyUser || result.AppID != "tasktracker" || result.Password != subscriberPassword {
		t.Fatalf("result = %+v", result)
	}
	expectedCall := fmt.Sprintf("ChangePassword(%s,pw=%s)", aliceNtfyUser, subscriberPassword)
	if indexOfInvocation(ntfyClient.invocations, expectedCall) < 0 {
		t.Fatalf("expected %s for the email-derived user, got: %s", expectedCall, strings.Join(ntfyClient.invocations, " | "))
	}
}

// Deprovision via one app revokes only that app's grants: the other app's
// patterns are never touched and the user survives while any pattern remains.
func TestDeprovisionViaOneAppLeavesTheOtherAppsGrantUntouched(t *testing.T) {
	ntfyClient := &fakeNtfyClient{listUsers: aliceGrantedIn("urls4irl", "chores4irl")}
	service := newGrantService(ntfyClient, newStatefulPersonClient(), enforcement.Enforce, nil)

	if err := service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser}); err != nil {
		t.Fatalf("Deprovision returned unexpected error: %v", err)
	}
	joined := strings.Join(ntfyClient.invocations, " | ")
	if !strings.Contains(joined, fmt.Sprintf("ResetAccess(%s,urls4irl-%s-*)", aliceNtfyUser, aliceHash)) {
		t.Fatalf("expected the calling app's grant to be reset: %s", joined)
	}
	if strings.Contains(joined, "chores4irl") || strings.Contains(joined, "DeleteUser(") {
		t.Fatalf("the other app's grant and the user must survive: %s", joined)
	}
}

// DeprovisionApp selects only users that hold the app's grant, so its
// per-subscriber Deprovision calls all pass the grant check in enforce mode.
func TestDeprovisionAppStillTearsDownEverySubscriberInEnforceMode(t *testing.T) {
	const (
		hashTwo   = "abcdefghijklmnop"
		hashThree = "qrstuvwxyzabcdef"
	)
	subscribers := []string{aliceNtfyUser, "u_" + hashTwo, "u_" + hashThree}
	ntfyClient := &fakeNtfyClient{listUsersFunc: grantedThenEmptied("myapp", subscribers...)}
	service := newGrantService(ntfyClient, newStatefulPersonClient(), enforcement.Enforce, nil)

	if err := service.DeprovisionApp(context.Background(), DeprovisionAppRequest{AppID: "myapp"}); err != nil {
		t.Fatalf("DeprovisionApp must succeed in enforce mode, got: %v", err)
	}
	for _, subscriber := range subscribers {
		if indexOfInvocation(ntfyClient.invocations, fmt.Sprintf("DeleteUser(%s)", subscriber)) < 0 {
			t.Fatalf("expected %s torn down, got: %s", subscriber, strings.Join(ntfyClient.invocations, " | "))
		}
	}
}

package provisioning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personsvc"
)

var resetRequestForAlice = ResetPasswordRequest{AppID: "urls4irl", Email: aliceEmail}

// snapshotInvocations copies a fake's invocation slice under the shared record
// mutex so a test can read it while another goroutine is still recording.
func snapshotInvocations(invocations *[]string) []string {
	recordMutex.Lock()
	defer recordMutex.Unlock()
	return append([]string(nil), (*invocations)...)
}

func hasInvocationPrefix(invocations []string, prefix string) bool {
	return indexOfInvocation(invocations, prefix) >= 0
}

// newResetService builds a service over a stateful person store that already
// holds alice's row with a prior stamp (the usual reset case).
func newResetService(ntfyClient *fakeNtfyClient) (*Service, *fakePersonClient) {
	personClient := newStatefulPersonClient()
	seedRow(personClient, "earlier-stamp")
	return newLoggedService(ntfyClient, personClient, nil), personClient
}

func slotCount(personClient *fakePersonClient) int {
	personClient.mutex.Lock()
	defer personClient.mutex.Unlock()
	return len(personClient.resetSlots[aliceHash])
}

func TestResetPasswordReturnsGeneratedPasswordAfterRecordingStampFirst(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	service, personClient := newResetService(ntfyClient)
	personClient.recordTo = &ntfyClient.invocations

	result, err := service.ResetPassword(context.Background(), resetRequestForAlice)
	if err != nil {
		t.Fatalf("ResetPassword returned unexpected error: %v", err)
	}
	if result.UserID != aliceNtfyUser || result.AppID != "urls4irl" || result.Password != subscriberPassword {
		t.Fatalf("result = %+v", result)
	}
	expected := fmt.Sprintf("ClaimResetSlot(%s) | ClaimPassword(%s,alice@example.com,update_only) | ChangePassword(%s,pw=%s)", aliceHash, aliceHash, aliceNtfyUser, subscriberPassword)
	if got := strings.Join(ntfyClient.invocations, " | "); got != expected {
		t.Fatalf("invocations = %s, expected %s", got, expected)
	}
	if stamp, _ := stampOf(personClient); stamp == "earlier-stamp" || stamp == "" {
		t.Fatalf("stamp = %q, expected a fresh stamp", stamp)
	}
	if slotCount(personClient) != 1 {
		t.Fatalf("a successful reset must keep its slot, have %d", slotCount(personClient))
	}
}

func TestResetPasswordSlotFailuresGiveTooManyWithoutAnyNtfyCall(t *testing.T) {
	testCases := []struct {
		name        string
		personFake  func() *fakePersonClient
		wantSeconds int
	}{
		{"unconfigured client", func() *fakePersonClient { return &fakePersonClient{configuredValue: false} }, 60},
		{"true limit", func() *fakePersonClient {
			client := newStatefulPersonClient()
			client.resetSlotErr = &personsvc.ResetLimitError{RetryAfterSeconds: 321}
			return client
		}, 321},
		{"wrapped true limit", func() *fakePersonClient {
			client := newStatefulPersonClient()
			client.resetSlotErr = fmt.Errorf("claim: %w", &personsvc.ResetLimitError{RetryAfterSeconds: 99})
			return client
		}, 99},
		{"limit store outage", func() *fakePersonClient {
			client := newStatefulPersonClient()
			client.resetSlotErr = errors.New("worker down")
			return client
		}, 60},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ntfyClient := &fakeNtfyClient{changePasswordErr: fmt.Errorf("ntfy user: %w", ntfycli.ErrNotFound)}
			service := newLoggedService(ntfyClient, testCase.personFake(), nil)

			_, err := service.ResetPassword(context.Background(), resetRequestForAlice)

			// 429 wins even though the ntfy user does not exist (would be 404).
			if !errors.Is(err, ErrTooManyPasswordChanges) {
				t.Fatalf("err = %v, expected ErrTooManyPasswordChanges", err)
			}
			if errors.Is(err, ntfycli.ErrNotFound) {
				t.Fatalf("limit must take precedence over not found, got %v", err)
			}
			var tooManyErr *TooManyPasswordChangesError
			if !errors.As(err, &tooManyErr) || tooManyErr.RetryAfterSeconds != testCase.wantSeconds {
				t.Fatalf("err = %#v, expected RetryAfterSeconds %d", err, testCase.wantSeconds)
			}
			if len(ntfyClient.invocations) != 0 {
				t.Fatalf("no ntfy call may be made, got %v", ntfyClient.invocations)
			}
		})
	}
}

func TestTooManyPasswordChangesErrorSatisfiesIsAndAsWhenWrapped(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", &TooManyPasswordChangesError{RetryAfterSeconds: 42})
	if !errors.Is(wrapped, ErrTooManyPasswordChanges) {
		t.Fatal("errors.Is must hold for a wrapped error")
	}
	var tooManyErr *TooManyPasswordChangesError
	if !errors.As(wrapped, &tooManyErr) || tooManyErr.RetryAfterSeconds != 42 {
		t.Fatalf("errors.As = %#v", tooManyErr)
	}
}

func TestResetPasswordSixthResetInAnHourIsLimitedAndRunsNoChangePassword(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	service, _ := newResetService(ntfyClient)

	for attempt := 1; attempt <= fakeResetLimit; attempt++ {
		if _, err := service.ResetPassword(context.Background(), resetRequestForAlice); err != nil {
			t.Fatalf("reset %d returned unexpected error: %v", attempt, err)
		}
	}
	changesBefore := len(snapshotInvocations(&ntfyClient.invocations))

	_, err := service.ResetPassword(context.Background(), resetRequestForAlice)

	var tooManyErr *TooManyPasswordChangesError
	if !errors.As(err, &tooManyErr) || tooManyErr.RetryAfterSeconds != 120 {
		t.Fatalf("err = %v, expected the limit error carrying 120 seconds", err)
	}
	if got := len(snapshotInvocations(&ntfyClient.invocations)); got != changesBefore {
		t.Fatalf("the sixth reset ran %d extra ntfy calls", got-changesBefore)
	}
}

func TestResetPasswordStampClaimErrorReleasesSlotAndMakesNoNtfyCall(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	service, personClient := newResetService(ntfyClient)
	claimErr := errors.New("claim boom")
	personClient.claimErr = claimErr

	_, err := service.ResetPassword(context.Background(), resetRequestForAlice)

	if !errors.Is(err, claimErr) || errors.Is(err, ErrTooManyPasswordChanges) {
		t.Fatalf("err = %v, expected the claim error (a 500, not a 429)", err)
	}
	if len(ntfyClient.invocations) != 0 {
		t.Fatalf("no ntfy call may be made, got %v", ntfyClient.invocations)
	}
	if slotCount(personClient) != 0 {
		t.Fatalf("the slot must be released, have %d", slotCount(personClient))
	}
	if stamp, _ := stampOf(personClient); stamp != "earlier-stamp" {
		t.Fatalf("stamp = %q, expected untouched", stamp)
	}
}

func TestResetPasswordNotFoundRestoresStampReleasesSlotAndCreatesNoRow(t *testing.T) {
	t.Run("existing row keeps its prior stamp", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{changePasswordErr: fmt.Errorf("ntfy user: %w: no such user", ntfycli.ErrNotFound)}
		service, personClient := newResetService(ntfyClient)

		_, err := service.ResetPassword(context.Background(), resetRequestForAlice)

		if !errors.Is(err, ntfycli.ErrNotFound) {
			t.Fatalf("err = %v, expected ErrNotFound", err)
		}
		if stamp, _ := stampOf(personClient); stamp != "earlier-stamp" {
			t.Fatalf("stamp = %q, expected the prior stamp restored", stamp)
		}
		if slotCount(personClient) != 0 {
			t.Fatalf("the slot must be released, have %d", slotCount(personClient))
		}
	})
	t.Run("unknown email leaves no person row", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{changePasswordErr: fmt.Errorf("ntfy user: %w: no such user", ntfycli.ErrNotFound)}
		personClient := newStatefulPersonClient()
		service := newLoggedService(ntfyClient, personClient, nil)

		_, err := service.ResetPassword(context.Background(), resetRequestForAlice)

		if !errors.Is(err, ntfycli.ErrNotFound) {
			t.Fatalf("err = %v, expected ErrNotFound", err)
		}
		if len(personClient.rows) != 0 {
			t.Fatalf("no person row may be created, got %d", len(personClient.rows))
		}
		if slotCount(personClient) != 0 {
			t.Fatalf("the slot must be released, have %d", slotCount(personClient))
		}
	})
}

func TestResetPasswordOtherApplyFailureRestoresStampAndReleasesSlot(t *testing.T) {
	changeErr := errors.New("change failed")
	ntfyClient := &fakeNtfyClient{changePasswordErr: changeErr}
	service, personClient := newResetService(ntfyClient)

	_, err := service.ResetPassword(context.Background(), resetRequestForAlice)

	if !errors.Is(err, changeErr) {
		t.Fatalf("err = %v, expected the change error", err)
	}
	if stamp, _ := stampOf(personClient); stamp != "earlier-stamp" {
		t.Fatalf("stamp = %q, expected the prior stamp restored", stamp)
	}
	if slotCount(personClient) != 0 {
		t.Fatalf("the slot must be released, have %d", slotCount(personClient))
	}
}

func TestResetPasswordReleaseDoesNotUndoANewerStamp(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	service, personClient := newResetService(ntfyClient)
	ntfyClient.changePasswordHook = func() error {
		if _, err := personClient.ClaimPassword(context.Background(), aliceHash, aliceEmail, personsvc.ClaimAlways); err != nil {
			t.Errorf("newer claim failed: %v", err)
		}
		return errors.New("change failed")
	}

	if _, err := service.ResetPassword(context.Background(), resetRequestForAlice); err == nil {
		t.Fatal("expected the ChangePassword error")
	}
	if stamp, _ := stampOf(personClient); stamp != "stamp-2" {
		t.Fatalf("stamp = %q, expected the newer stamp-2 to survive", stamp)
	}
}

func TestResetPasswordForRowlessExistingUserRecordsStampAfterwards(t *testing.T) {
	t.Run("record succeeds", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{}
		personClient := newStatefulPersonClient()
		personClient.recordTo = &ntfyClient.invocations
		service := newLoggedService(ntfyClient, personClient, nil)

		if _, err := service.ResetPassword(context.Background(), resetRequestForAlice); err != nil {
			t.Fatalf("ResetPassword returned unexpected error: %v", err)
		}
		expected := fmt.Sprintf("ClaimResetSlot(%s) | ClaimPassword(%s,alice@example.com,update_only) | ChangePassword(%s,pw=%s) | ClaimPassword(%s,alice@example.com,always)", aliceHash, aliceHash, aliceNtfyUser, subscriberPassword, aliceHash)
		if got := strings.Join(ntfyClient.invocations, " | "); got != expected {
			t.Fatalf("invocations = %s, expected %s", got, expected)
		}
		if stamp, exists := stampOf(personClient); !exists || stamp == "" {
			t.Fatalf("expected a stamped row, got %q exists=%v", stamp, exists)
		}
	})
	t.Run("record fails but the password is still returned and the failure logged", func(t *testing.T) {
		logger, buffer := newJSONLogger()
		personClient := newStatefulPersonClient()
		personClient.claimErrByMode = map[personsvc.ClaimMode]error{personsvc.ClaimAlways: errors.New("always boom")}
		service := newLoggedService(&fakeNtfyClient{}, personClient, logger)

		result, err := service.ResetPassword(context.Background(), resetRequestForAlice)

		if err != nil || result.Password != subscriberPassword {
			t.Fatalf("result = %+v err = %v, expected the applied password", result, err)
		}
		failures := recordsWithMessage(logRecords(t, buffer), "password stamp record after change failed")
		if len(failures) != 1 || failures[0]["level"] != "ERROR" || failures[0]["person_hash"] != aliceHash || !strings.Contains(fmt.Sprint(failures[0]["error"]), "always boom") {
			t.Fatalf("expected one Error-level record failure, got %v", failures)
		}
		if slotCount(personClient) != 1 {
			t.Fatalf("an applied reset keeps its slot, have %d", slotCount(personClient))
		}
	})
}

func TestResetPasswordLogsNeverContainThePasswordOrEmail(t *testing.T) {
	logger, buffer := newJSONLogger()
	ntfyClient := &fakeNtfyClient{}
	personClient := newStatefulPersonClient()
	seedRow(personClient, "earlier-stamp")
	service := newLoggedService(ntfyClient, personClient, logger)
	if _, err := service.ResetPassword(context.Background(), resetRequestForAlice); err != nil {
		t.Fatalf("ResetPassword returned unexpected error: %v", err)
	}
	ntfyClient.changePasswordErr = errors.New("change failed")
	personClient.releaseClaimErr = errors.New("release boom")
	if _, err := service.ResetPassword(context.Background(), resetRequestForAlice); err == nil {
		t.Fatal("expected the ChangePassword error")
	}

	logText := buffer.String()
	if logText == "" {
		t.Fatal("expected some log output")
	}
	if strings.Contains(logText, subscriberPassword) || strings.Contains(logText, aliceEmail) {
		t.Fatalf("log leaked a password or email: %s", logText)
	}
}

func TestResetPasswordOutcomeLoggingAndFailedReleaseLogging(t *testing.T) {
	testCases := []struct {
		name        string
		setup       func(ntfyClient *fakeNtfyClient, personClient *fakePersonClient)
		unconfigure bool
		wantOutcome string
	}{
		{"reset", func(*fakeNtfyClient, *fakePersonClient) {}, false, "reset"},
		{"limited", func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			personClient.resetSlotErr = &personsvc.ResetLimitError{RetryAfterSeconds: 10}
		}, false, "limited"},
		{"unconfigured client", func(*fakeNtfyClient, *fakePersonClient) {}, true, "limit_store_unavailable"},
		{"non-limit slot error", func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			personClient.resetSlotErr = errors.New("worker down")
		}, false, "limit_store_unavailable"},
		{"stamp unavailable", func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			personClient.claimErr = errors.New("claim boom")
		}, false, "stamp_unavailable"},
		{"not found", func(ntfyClient *fakeNtfyClient, _ *fakePersonClient) {
			ntfyClient.changePasswordErr = fmt.Errorf("ntfy user: %w", ntfycli.ErrNotFound)
		}, false, "not_found"},
		{"failed", func(ntfyClient *fakeNtfyClient, _ *fakePersonClient) {
			ntfyClient.changePasswordErr = errors.New("change failed")
		}, false, "failed"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			logger, buffer := newJSONLogger()
			ntfyClient := &fakeNtfyClient{}
			personClient := newStatefulPersonClient()
			seedRow(personClient, "earlier-stamp")
			testCase.setup(ntfyClient, personClient)
			personClient.configuredValue = !testCase.unconfigure
			service := newLoggedService(ntfyClient, personClient, logger)

			_, _ = service.ResetPassword(context.Background(), resetRequestForAlice)

			outcomes := recordsWithMessage(logRecords(t, buffer), "password reset")
			if len(outcomes) != 1 {
				t.Fatalf("expected one password reset line, got %v", outcomes)
			}
			if outcomes[0]["outcome"] != testCase.wantOutcome || outcomes[0]["level"] != "INFO" || outcomes[0]["person_hash"] != aliceHash || outcomes[0]["app_id"] != "urls4irl" {
				t.Fatalf("record = %v, expected outcome %q", outcomes[0], testCase.wantOutcome)
			}
		})
	}

	t.Run("failed stamp release and slot release are logged at Error level", func(t *testing.T) {
		logger, buffer := newJSONLogger()
		ntfyClient := &fakeNtfyClient{changePasswordErr: errors.New("change failed")}
		personClient := newStatefulPersonClient()
		seedRow(personClient, "earlier-stamp")
		personClient.releaseClaimErr = errors.New("stamp release boom")
		personClient.releaseSlotErr = errors.New("slot release boom")
		service := newLoggedService(ntfyClient, personClient, logger)

		_, _ = service.ResetPassword(context.Background(), resetRequestForAlice)

		records := logRecords(t, buffer)
		for message, errorText := range map[string]string{"password claim release failed": "stamp release boom", "reset slot release failed": "slot release boom"} {
			matching := recordsWithMessage(records, message)
			if len(matching) != 1 || matching[0]["level"] != "ERROR" || matching[0]["person_hash"] != aliceHash || !strings.Contains(fmt.Sprint(matching[0]["error"]), errorText) {
				t.Fatalf("%s: expected one Error-level line with person_hash and error text, got %v", message, matching)
			}
		}
	})
	t.Run("failed slot release when the stamp claim fails", func(t *testing.T) {
		logger, buffer := newJSONLogger()
		personClient := newStatefulPersonClient()
		personClient.claimErr = errors.New("claim boom")
		personClient.releaseSlotErr = errors.New("slot release boom")
		service := newLoggedService(&fakeNtfyClient{}, personClient, logger)

		_, _ = service.ResetPassword(context.Background(), resetRequestForAlice)

		matching := recordsWithMessage(logRecords(t, buffer), "reset slot release failed")
		if len(matching) != 1 || matching[0]["level"] != "ERROR" || matching[0]["person_hash"] != aliceHash {
			t.Fatalf("expected one Error-level slot release line, got %v", matching)
		}
	})
}

func TestResetWaitsForProvisionOfSamePersonFromItsStampClaim(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var gateOnce sync.Once
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	ntfyClient.addUserHook = func() {
		gateOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	var resetChanged atomic.Bool
	ntfyClient.changePasswordHook = func() error {
		resetChanged.Store(true)
		return nil
	}
	personClient := newStatefulPersonClient()
	service := newLoggedService(ntfyClient, personClient, nil)

	provisionDone := make(chan error, 1)
	go func() {
		_, err := service.Provision(context.Background(), provisionRequestForAlice)
		provisionDone <- err
	}()
	waitForEntered(t, entered)

	resetDone := make(chan error, 1)
	go func() {
		_, err := service.ResetPassword(context.Background(), resetRequestForAlice)
		resetDone <- err
	}()
	waitForLockWaiters(t, service, 2)

	// The slot may have been claimed early, but no stamp claim or password
	// change may run while Provision holds the person lock.
	personInvocations := snapshotInvocations(&personClient.invocations)
	if !hasInvocationPrefix(personInvocations, "ClaimResetSlot("+aliceHash+")") {
		t.Fatalf("Reset should have claimed its slot before waiting for the lock: %v", personInvocations)
	}
	if hasInvocationPrefix(personInvocations, "ClaimPassword("+aliceHash+",alice@example.com,update_only)") {
		t.Fatalf("Reset claimed its stamp while Provision held the lock: %v", personInvocations)
	}
	if resetChanged.Load() {
		t.Fatal("Reset ran ChangePassword while Provision held the lock")
	}

	close(release)
	waitForResult(t, provisionDone, "Provision")
	waitForResult(t, resetDone, "Reset")
	if !resetChanged.Load() {
		t.Fatal("Reset should run once Provision returned")
	}
}

func TestProvisionWaitsForResetOfSamePersonFromItsStampClaim(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var gateOnce sync.Once
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true, users: map[string]bool{aliceNtfyUser: true}}
	ntfyClient.changePasswordHook = func() error {
		gateOnce.Do(func() {
			close(entered)
			<-release
		})
		return nil
	}
	personClient := newStatefulPersonClient()
	seedRow(personClient, "earlier-stamp")
	service := newLoggedService(ntfyClient, personClient, nil)

	resetDone := make(chan error, 1)
	go func() {
		_, err := service.ResetPassword(context.Background(), resetRequestForAlice)
		resetDone <- err
	}()
	waitForEntered(t, entered)

	provisionDone := make(chan error, 1)
	go func() {
		_, err := service.Provision(context.Background(), provisionRequestForAlice)
		provisionDone <- err
	}()
	waitForLockWaiters(t, service, 2)

	if hasInvocationPrefix(snapshotInvocations(&personClient.invocations), "ClaimPassword("+aliceHash+",alice@example.com,if_unset)") {
		t.Fatal("Provision claimed its stamp while Reset held the lock")
	}
	if hasInvocationPrefix(snapshotInvocations(&ntfyClient.invocations), "GrantAccess(") {
		t.Fatal("Provision ran ntfy work while Reset held the lock")
	}

	close(release)
	waitForResult(t, resetDone, "Reset")
	waitForResult(t, provisionDone, "Provision")
}

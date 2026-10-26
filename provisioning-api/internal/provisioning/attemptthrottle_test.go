package provisioning

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAttemptThrottleAllowsLimitThenRefusesUntilTheWindowSlides(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	throttle := &attemptThrottle{now: func() time.Time { return now }}

	for attempt := 1; attempt <= 3; attempt++ {
		if allowed, _ := throttle.allow("key", 3); !allowed {
			t.Fatalf("attempt %d refused, want allowed", attempt)
		}
		now = now.Add(10 * time.Minute)
	}

	allowed, retryAfter := throttle.allow("key", 3)
	if allowed {
		t.Fatal("fourth attempt in the window allowed, want refused")
	}
	// The oldest attempt was at 12:00; now is 12:30, so it leaves at 13:00.
	if retryAfter != 30*time.Minute {
		t.Fatalf("retryAfter = %v, want 30m", retryAfter)
	}

	now = now.Add(31 * time.Minute)
	if allowed, _ := throttle.allow("key", 3); !allowed {
		t.Fatal("attempt after the oldest left the window refused, want allowed")
	}
}

func TestAttemptThrottleRefusedAttemptsAreNotCounted(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	throttle := &attemptThrottle{now: func() time.Time { return now }}
	throttle.allow("key", 1)

	for retry := 0; retry < 5; retry++ {
		now = now.Add(5 * time.Minute)
		if allowed, _ := throttle.allow("key", 1); allowed {
			t.Fatal("attempt inside the window allowed, want refused")
		}
	}

	// 12:25 now; the only counted attempt (12:00) leaves at 13:00 regardless of the retries.
	now = time.Date(2026, 10, 25, 13, 0, 1, 0, time.UTC)
	if allowed, _ := throttle.allow("key", 1); !allowed {
		t.Fatal("retries extended the window, want allowed once the counted attempt left")
	}
}

func TestAttemptThrottleKeysAreIndependent(t *testing.T) {
	throttle := &attemptThrottle{}
	throttle.allow("first", 1)

	if allowed, _ := throttle.allow("second", 1); !allowed {
		t.Fatal("a different key was refused, want allowed")
	}
}

func TestAttemptThrottleSweepsExpiredKeys(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	throttle := &attemptThrottle{now: func() time.Time { return now }}
	for index := 0; index < attemptThrottleSweepThreshold; index++ {
		throttle.allow(fmt.Sprintf("key-%d", index), 1)
	}

	now = now.Add(2 * time.Hour)
	throttle.allow("fresh", 1)

	if tracked := len(throttle.attempts); tracked != 1 {
		t.Fatalf("tracked keys = %d, want only the fresh one", tracked)
	}
}

func TestAttemptThrottleSweepsAtMostOncePerIntervalAndStillFreesExpiredKeys(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	throttle := &attemptThrottle{now: func() time.Time { return now }}
	for index := 0; index < attemptThrottleSweepThreshold; index++ {
		throttle.allow(fmt.Sprintf("old-%d", index), 1)
	}

	// Two hours later every old key has expired; the first new key sweeps them all.
	now = now.Add(2 * time.Hour)
	throttle.allow("first", 1)
	if throttle.sweeps != 1 {
		t.Fatalf("sweeps = %d after the first new key past the threshold, want 1", throttle.sweeps)
	}

	// A flood of distinct live keys inside one minute never sweeps again, even
	// though the map is back above the threshold.
	for index := 0; index < 3*attemptThrottleSweepThreshold; index++ {
		now = now.Add(10 * time.Millisecond)
		throttle.allow(fmt.Sprintf("flood-%d", index), 1)
	}
	if throttle.sweeps != 1 {
		t.Fatalf("sweeps = %d after a flood within one minute, want still 1", throttle.sweeps)
	}

	// Once the interval and the window have both passed, the next new key sweeps
	// again and frees the expired keys.
	now = now.Add(2 * time.Hour)
	throttle.allow("after", 1)
	if throttle.sweeps != 2 {
		t.Fatalf("sweeps = %d after the interval passed, want 2", throttle.sweeps)
	}
	if tracked := len(throttle.attempts); tracked != 1 {
		t.Fatalf("tracked keys = %d, want only the newest one", tracked)
	}
}

func TestResetPasswordOverAttemptLimitIs429WithoutAnyNtfyOrPersonStoreCall(t *testing.T) {
	ntfyClient := &fakeNtfyClient{listUsers: aliceGrantedIn("urls4irl")}
	service, personClient := newResetService(ntfyClient)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	service.resetAttempts.now = func() time.Time { return now }
	// A request for an app the person does not hold is rejected (404) in every attempt.
	otherApp := ResetPasswordRequest{AppID: "otherapp", Email: aliceEmail}

	for attempt := 1; attempt <= resetAttemptLimit; attempt++ {
		_, err := service.ResetPassword(context.Background(), otherApp)
		if !errors.Is(err, ErrPersonNotInApp) {
			t.Fatalf("attempt %d err = %v, want ErrPersonNotInApp", attempt, err)
		}
	}
	callsBefore := len(snapshotInvocations(&ntfyClient.invocations))

	_, err := service.ResetPassword(context.Background(), otherApp)

	var tooManyErr *TooManyPasswordChangesError
	if !errors.As(err, &tooManyErr) {
		t.Fatalf("err = %v, want the 429 error", err)
	}
	if tooManyErr.RetryAfterSeconds != int(attemptThrottleWindow.Seconds()) {
		t.Fatalf("RetryAfterSeconds = %d, want %d", tooManyErr.RetryAfterSeconds, int(attemptThrottleWindow.Seconds()))
	}
	if calls := len(snapshotInvocations(&ntfyClient.invocations)); calls != callsBefore {
		t.Fatalf("the throttled attempt made %d ntfy calls, want none", calls-callsBefore)
	}
	if slotCount(personClient) != 0 {
		t.Fatalf("rejected and throttled attempts claimed %d reset slots, want none", slotCount(personClient))
	}
	// The throttle is per person/app: the app the person does hold is not throttled.
	if _, grantedErr := service.ResetPassword(context.Background(), resetRequestForAlice); grantedErr != nil {
		t.Fatalf("a different app's reset returned %v, want success", grantedErr)
	}
}

func TestResetPasswordRejectedRequestBurnsNoResetSlot(t *testing.T) {
	ntfyClient := &fakeNtfyClient{listUsers: aliceGrantedIn("urls4irl")}
	service, personClient := newResetService(ntfyClient)

	_, err := service.ResetPassword(context.Background(), ResetPasswordRequest{AppID: "otherapp", Email: aliceEmail})

	if !errors.Is(err, ErrPersonNotInApp) {
		t.Fatalf("err = %v, want ErrPersonNotInApp", err)
	}
	requireOnlyGrantLookup(t, snapshotInvocations(&ntfyClient.invocations))
	if slotCount(personClient) != 0 {
		t.Fatalf("a rejected request claimed %d reset slots, want none", slotCount(personClient))
	}
}

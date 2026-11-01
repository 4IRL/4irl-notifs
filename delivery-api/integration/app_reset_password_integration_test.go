//go:build integration

package integration

// End-to-end tests of the app-facing reset-password door (POST /v1/apps/{app_id}/reset-password)
// against the live compose stack with real dev dak_ keys: a real reset through provisioning-api and
// ntfy, the app-scoped grant check, and both door caps. Every person is unique to its test, so the
// upstream per-person limit (5 successful changes per hour) never carries across runs. The
// limit-store-down case is not here: it needs a dropped table, so it runs on an isolated schema in
// internal/httpapi.

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/resetlimit"
)

const (
	doorResetLimitedBody = `{"error":"reset rate limit exceeded"}`
	seededPersonPrefix   = "it-seed-"
)

func appResetPasswordPath(appID string) string {
	return "/v1/apps/" + appID + "/reset-password"
}

type resetPasswordJSON struct {
	UserID   string `json:"user_id"`
	AppID    string `json:"app_id"`
	Password string `json:"password"`
}

// resetRig is one app with one provisioned person whose password is a known value, so a test can
// tell whether a call changed it.
type resetRig struct {
	appID      string
	key        string
	email      string
	username   string
	topic      string
	currentPwd string
}

// newResetRig provisions a unique person in a fresh app and sets a known starting password.
func newResetRig(testInstance *testing.T) *resetRig {
	testInstance.Helper()
	appID, key := newPublisherApp(testInstance)
	return provisionResetPerson(testInstance, appID, key)
}

// provisionResetPerson provisions a unique person in an existing app and sets a known password.
func provisionResetPerson(testInstance *testing.T, appID, key string) *resetRig {
	testInstance.Helper()
	email := "it-reset-" + randomSuffix(testInstance) + "@example.test"
	subscriber := provisionSubscriber(testInstance, appID, key, email)
	rig := &resetRig{
		appID:      appID,
		key:        key,
		email:      email,
		username:   personhash.NtfyUser(email),
		topic:      personhash.PersonChannelTopic(appID, subscriber.PersonHash, sinkChannel),
		currentPwd: "it-start-password-1",
	}
	status, _, body := apiResponse(testInstance, bearer(key), http.MethodPost, appSetPasswordPath(appID),
		map[string]any{"email": email, "password": rig.currentPwd})
	requireStatus(testInstance, "set the starting password", status, http.StatusOK, body)
	rig.requirePasswordWorks(testInstance, "the starting password", rig.currentPwd)
	return rig
}

func (rig *resetRig) requirePasswordWorks(testInstance *testing.T, label, password string) {
	testInstance.Helper()
	if got := ntfyTopicStatusWithBasicAuth(testInstance, rig.topic, rig.username, password); got != http.StatusOK {
		testInstance.Fatalf("%s: basic-auth status = %d, want 200", label, got)
	}
}

func (rig *resetRig) requirePasswordRejected(testInstance *testing.T, label, password string) {
	testInstance.Helper()
	if got := ntfyTopicStatusWithBasicAuth(testInstance, rig.topic, rig.username, password); got != http.StatusUnauthorized && got != http.StatusForbidden {
		testInstance.Fatalf("%s: basic-auth status = %d, want 401 or 403", label, got)
	}
}

// reset calls the door as the rig's own app and returns the response parts.
func (rig *resetRig) reset(testInstance *testing.T) (int, http.Header, []byte) {
	testInstance.Helper()
	return apiResponse(testInstance, bearer(rig.key), http.MethodPost, appResetPasswordPath(rig.appID),
		map[string]any{"email": rig.email})
}

// requireReset resets once, requires a 200 that carries a new password, and returns it.
func (rig *resetRig) requireReset(testInstance *testing.T, label string) string {
	testInstance.Helper()
	password, _ := rig.requireResetWithHeaders(testInstance, label)
	return password
}

func (rig *resetRig) requireResetWithHeaders(testInstance *testing.T, label string) (string, http.Header) {
	testInstance.Helper()
	status, headers, body := rig.reset(testInstance)
	requireStatus(testInstance, label, status, http.StatusOK, body)
	var reset resetPasswordJSON
	decodeBody(testInstance, label, body, &reset)
	if reset.Password == "" || reset.AppID != rig.appID || reset.UserID != rig.username {
		testInstance.Fatalf("%s answer = %+v, want a password for app %q user %q", label, reset, rig.appID, rig.username)
	}
	return reset.Password, headers
}

func TestAppResetsItsPersonsPassword(testInstance *testing.T) {
	rig := newResetRig(testInstance)

	password, headers := rig.requireResetWithHeaders(testInstance, "reset")

	if cacheControl := headers.Get("Cache-Control"); cacheControl != "no-store" {
		testInstance.Errorf("reset Cache-Control = %q, want no-store", cacheControl)
	}
	rig.requirePasswordWorks(testInstance, "the new password", password)
	rig.requirePasswordRejected(testInstance, "the old password", rig.currentPwd)
}

func TestAppCannotResetAPersonNotInIt(testInstance *testing.T) {
	rig := newResetRig(testInstance)
	strangerApp, strangerKey := newPublisherApp(testInstance)

	status, _, body := apiResponse(testInstance, bearer(strangerKey), http.MethodPost, appResetPasswordPath(strangerApp),
		map[string]any{"email": rig.email})

	requireStatus(testInstance, "stranger app reset", status, http.StatusNotFound, body)
	requireErrorMessage(testInstance, "stranger app reset", body, "user does not exist")
	rig.requirePasswordWorks(testInstance, "the person's password after the refused call", rig.currentPwd)
}

func TestAppResetPersonCap(testInstance *testing.T) {
	rig := newResetRig(testInstance)
	last := ""
	for attempt := 1; attempt <= resetlimit.PersonLimit; attempt++ {
		last = rig.requireReset(testInstance, fmt.Sprintf("reset #%d", attempt))
	}

	status, headers, body := rig.reset(testInstance)

	requireStatus(testInstance, "reset over the person cap", status, http.StatusTooManyRequests, body)
	if got := string(body); got != doorResetLimitedBody+"\n" && got != doorResetLimitedBody {
		testInstance.Fatalf("reset over the person cap body = %q, want the door body %s", got, doorResetLimitedBody)
	}
	retryAfter, parseErr := strconv.Atoi(headers.Get("Retry-After"))
	if parseErr != nil || retryAfter < 1 {
		testInstance.Errorf("Retry-After = %q, want a positive integer", headers.Get("Retry-After"))
	}
	rig.requirePasswordWorks(testInstance, "the password after the refused 4th reset", last)
}

func TestAppResetAppCap(testInstance *testing.T) {
	rig := newResetRig(testInstance)
	otherApp, otherKey := newPublisherApp(testInstance)
	otherRig := provisionResetPerson(testInstance, otherApp, otherKey)
	psql(testInstance, fmt.Sprintf(
		"insert into app_reset_attempts (app_id, person_hash) select '%s', '%s' || g from generate_series(1, %d) g",
		rig.appID, seededPersonPrefix, resetlimit.AppLimit))

	status, headers, body := rig.reset(testInstance)

	requireStatus(testInstance, "reset with the app at its cap", status, http.StatusTooManyRequests, body)
	if got := string(body); got != doorResetLimitedBody+"\n" && got != doorResetLimitedBody {
		testInstance.Fatalf("reset over the app cap body = %q, want the door body %s", got, doorResetLimitedBody)
	}
	retryAfter, parseErr := strconv.Atoi(headers.Get("Retry-After"))
	if parseErr != nil || retryAfter < 1 {
		testInstance.Errorf("Retry-After = %q, want a positive integer", headers.Get("Retry-After"))
	}
	rig.requirePasswordWorks(testInstance, "the password after the refused reset (no upstream reset)", rig.currentPwd)

	// Another app's own person is unaffected by this app's cap.
	newPassword := otherRig.requireReset(testInstance, "other app reset")
	otherRig.requirePasswordWorks(testInstance, "the other app's new password", newPassword)
}

//go:build integration

package integration

// End-to-end tests of the operator provisioning routes under /v1/admin (provision, deprovision, reset
// password, list users, delete user, test-notify, the publisher mint with rotate, and the
// deprovision-app cascade) against the live compose stack. Every app id is unique per run. The ntfy
// user listing comes from `ntfy user list` inside the ntfy container (the auth file is the source of
// truth), falling back to the operator GET /v1/admin/users listing when the container cannot be run.

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
)

const (
	adminProvisionPath      = adminBasePath + "/provision"
	adminResetPasswordPath  = adminBasePath + "/reset-password"
	adminUsersPath          = adminBasePath + "/users"
	adminTestNotifyPath     = adminBasePath + "/test-notify"
	adminAppNotBoundMessage = "app has no approved binding"
	ntfyContainerEnv        = "NOTIFS_NTFY_CONTAINER"
	ntfyServiceName         = "ntfy"
)

func adminDeprovisionAppPath(appID string) string {
	return adminBasePath + "/apps/" + appID + "/deprovision"
}

// deprovisionAppBody is the confirmation body the cascade route requires: the app id repeated.
func deprovisionAppBody(appID string) map[string]string {
	return map[string]string{"app_id": appID}
}

type adminProvisionedJSON struct {
	Token string `json:"token"`
}

type adminResetJSON struct {
	UserID   string `json:"user_id"`
	Password string `json:"password"`
}

type adminUsersJSON struct {
	Users []struct {
		UserID string `json:"user_id"`
	} `json:"users"`
}

type deprovisionAppJSON struct {
	AppID   string `json:"app_id"`
	Removed bool   `json:"removed"`
	Unbound bool   `json:"unbound"`
}

// ntfyUserExists reports whether userID is in ntfy's user list. It runs `ntfy user list` in the ntfy
// container; if that cannot run it uses the operator users listing instead.
func ntfyUserExists(testInstance *testing.T, userID string) bool {
	testInstance.Helper()
	containerID, resolveErr := resolveServiceContainer(execRunner, ntfyContainerEnv, ntfyServiceName)
	if resolveErr == nil {
		commandCtx, cancelCommand := context.WithTimeout(context.Background(), dockerCommandLimit)
		defer cancelCommand()
		output, runErr := exec.CommandContext(commandCtx, "docker", "exec", containerID, "ntfy", "user", "list").Output()
		if runErr == nil {
			for _, line := range strings.Split(string(output), "\n") {
				if strings.HasPrefix(line, "user "+userID+" ") {
					return true
				}
			}
			return false
		}
		testInstance.Logf("ntfy user list failed (%v); using the operator users listing", runErr)
	} else {
		testInstance.Logf("resolving the ntfy container failed (%v); using the operator users listing", resolveErr)
	}
	status, body := apiRequest(testInstance, http.MethodGet, adminUsersPath, nil)
	requireStatus(testInstance, "operator list users", status, http.StatusOK, body)
	var listed adminUsersJSON
	decodeBody(testInstance, "operator list users", body, &listed)
	for _, user := range listed.Users {
		if user.UserID == userID {
			return true
		}
	}
	return false
}

func requireNtfyUser(testInstance *testing.T, label, userID string, want bool) {
	testInstance.Helper()
	if got := ntfyUserExists(testInstance, userID); got != want {
		testInstance.Fatalf("%s: ntfy user %s present = %v, want %v", label, userID, got, want)
	}
}

func sealedTokenHex(testInstance *testing.T, appID string) string {
	testInstance.Helper()
	return psql(testInstance, fmt.Sprintf("select encode(sealed_token, 'hex') from app_publishers where app_id = '%s'", appID))
}

// ownedCount counts the rows of table the app owns.
func ownedCount(testInstance *testing.T, table, appID string) string {
	testInstance.Helper()
	return psql(testInstance, fmt.Sprintf("select count(*) from %s where owner_kind = 'app' and owner_app_id = '%s'", table, appID))
}

func bindingAndKeyCount(testInstance *testing.T, appID string) string {
	testInstance.Helper()
	return psql(testInstance, fmt.Sprintf(
		"select (select count(*) from app_bindings where app_id = '%[1]s') || ':' || (select count(*) from app_keys where app_id = '%[1]s')",
		appID))
}

func TestOperatorProvisionListResetDeleteUser(testInstance *testing.T) {
	appID, _ := newPublisherApp(testInstance)
	email := "it-admin-" + randomSuffix(testInstance) + "@example.test"
	username := personhash.NtfyUser(email)
	topic := personhash.PersonChannelTopic(appID, personhash.Hash(email), sinkChannel)
	testInstance.Cleanup(func() { deprovisionUser(testInstance, username) })

	status, headers, body := apiResponse(testInstance, "", http.MethodPost, adminProvisionPath,
		map[string]any{"app_id": appID, "email": email})
	requireStatus(testInstance, "operator provision", status, http.StatusOK, body)
	var provisioned adminProvisionedJSON
	decodeBody(testInstance, "operator provision", body, &provisioned)
	if provisioned.Token == "" {
		testInstance.Fatalf("operator provision returned no token: %s", body)
	}
	if cacheControl := headers.Get("Cache-Control"); cacheControl != "no-store" {
		testInstance.Errorf("operator provision Cache-Control = %q, want no-store", cacheControl)
	}
	requireNtfyUser(testInstance, "after operator provision", username, true)

	status, body = apiRequest(testInstance, http.MethodGet, adminUsersPath, nil)
	requireStatus(testInstance, "operator list users", status, http.StatusOK, body)
	var listed adminUsersJSON
	decodeBody(testInstance, "operator list users", body, &listed)
	found := false
	for _, user := range listed.Users {
		found = found || user.UserID == username
	}
	if !found {
		testInstance.Errorf("operator list users did not include %s", username)
	}

	status, body = apiRequest(testInstance, http.MethodPost, adminResetPasswordPath,
		map[string]any{"app_id": appID, "email": email})
	requireStatus(testInstance, "operator reset password", status, http.StatusOK, body)
	var reset adminResetJSON
	decodeBody(testInstance, "operator reset password", body, &reset)
	if reset.Password == "" || reset.UserID != username {
		testInstance.Fatalf("operator reset answer = %+v, want a password for %s", reset, username)
	}
	if got := ntfyTopicStatusWithBasicAuth(testInstance, topic, username, reset.Password); got != http.StatusOK {
		testInstance.Fatalf("basic-auth with the operator-reset password status = %d, want 200", got)
	}

	status, body = apiRequest(testInstance, http.MethodDelete, adminUsersPath+"/"+username, nil)
	requireStatus(testInstance, "operator delete user", status, http.StatusOK, body)
	requireNtfyUser(testInstance, "after operator delete user", username, false)
}

func TestOperatorRoutesRefuseAnUnboundApp(testInstance *testing.T) {
	waitForReady(testInstance)
	appID := "it" + randomSuffix(testInstance)[:12]
	email := "it-unbound-" + randomSuffix(testInstance) + "@example.test"
	username := personhash.NtfyUser(email)
	publisherUser := appID + "-publisher"
	registerPublisherAppCleanup(testInstance, appID)
	testInstance.Cleanup(func() { deprovisionUser(testInstance, username) })

	status, body := apiRequest(testInstance, http.MethodPost, adminProvisionPath,
		map[string]any{"app_id": appID, "email": email})
	requireStatus(testInstance, "provision for an unbound app", status, http.StatusNotFound, body)
	requireErrorMessage(testInstance, "provision for an unbound app", body, adminAppNotBoundMessage)

	status, body = apiRequest(testInstance, http.MethodPost, adminTestNotifyPath, map[string]any{
		"app_id": appID, "recipients": []string{email}, "channel": sinkChannel, "message": "m", "title": "t",
	})
	requireStatus(testInstance, "test-notify for an unbound app", status, http.StatusNotFound, body)
	requireErrorMessage(testInstance, "test-notify for an unbound app", body, adminAppNotBoundMessage)

	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), nil)
	requireStatus(testInstance, "operator mint for an unbound app", status, http.StatusNotFound, body)
	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), map[string]any{"rotate": true})
	requireStatus(testInstance, "operator rotate for an unbound app", status, http.StatusNotFound, body)

	requireNtfyUser(testInstance, "after the refused provision", username, false)
	requireNtfyUser(testInstance, "after the refused mint and test-notify", publisherUser, false)
	if count := publisherRowCount(testInstance, appID); count != "0" {
		testInstance.Errorf("refused mints stored %s publisher rows for %s, want 0", count, appID)
	}
}

func TestOperatorRotateChangesTheStoredTokenAndReturnsNone(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)
	email := "it-oprotate-" + randomSuffix(testInstance) + "@example.test"
	subscriber := provisionSubscriber(testInstance, appID, key, email)

	status, body := apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), nil)
	requireStatus(testInstance, "operator mint with an empty body", status, http.StatusOK, body)
	requireSealedNotPlaintext(testInstance, appID)
	before := sealedTokenHex(testInstance, appID)
	requireRecipientDelivery(testInstance, appID, email, subscriber)

	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), map[string]any{"rotate": true})
	requireStatus(testInstance, "operator rotate", status, http.StatusOK, body)
	if strings.Contains(string(body), "tk_") {
		testInstance.Errorf("operator rotate response carries a token: %s", body)
	}
	view := decodeMintView(testInstance, "operator rotate", body)
	if view.Publisher.AppID != appID || view.Publisher.PublisherUserID != appID+"-publisher" {
		testInstance.Errorf("operator rotate publisher = %+v, want app %q", view.Publisher, appID)
	}
	requireSealedNotPlaintext(testInstance, appID)
	if after := sealedTokenHex(testInstance, appID); after == before {
		testInstance.Errorf("the stored sealed token did not change after rotate")
	}
	requireRecipientDelivery(testInstance, appID, email, subscriber)

	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), map[string]any{"rotate": "yes"})
	requireStatus(testInstance, "operator mint with a malformed body", status, http.StatusBadRequest, body)
}

func TestOperatorDeprovisionAppRemovesEverythingTheAppOwns(testInstance *testing.T) {
	appID, key := newPublisherApp(testInstance)
	rig := appDestinationsRig{testInstance: testInstance}
	// Residual rows from a failed run: the app's objects are removed by owner, so none can leak.
	testInstance.Cleanup(func() {
		for _, table := range []string{"heartbeat_checks", "routes", "destinations"} {
			psql(testInstance, fmt.Sprintf("delete from %s where owner_kind = 'app' and owner_app_id = '%s'", table, appID))
		}
	})

	// A subscriber whose credentials work before the cascade.
	email := "it-cascade-" + randomSuffix(testInstance) + "@example.test"
	subscriber := provisionSubscriber(testInstance, appID, key, email)
	username := personhash.NtfyUser(email)
	topic := personhash.PersonChannelTopic(appID, subscriber.PersonHash, sinkChannel)
	status, body := apiRequest(testInstance, http.MethodPost, adminResetPasswordPath,
		map[string]any{"app_id": appID, "email": email})
	requireStatus(testInstance, "operator reset password", status, http.StatusOK, body)
	var reset adminResetJSON
	decodeBody(testInstance, "operator reset password", body, &reset)
	if got := ntfyTopicStatusWithBasicAuth(testInstance, topic, username, reset.Password); got != http.StatusOK {
		testInstance.Fatalf("subscriber basic-auth before the cascade status = %d, want 200", got)
	}

	// The app's publisher.
	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), nil)
	requireStatus(testInstance, "operator mint", status, http.StatusOK, body)
	requireSealedNotPlaintext(testInstance, appID)
	requireNtfyUser(testInstance, "before the cascade", appID+"-publisher", true)

	// The app's own destination, route and check, plus an operator route and check that must survive.
	appDestination := appID + "_hook"
	rig.create(appID, appDestination, publicWebhookURL)
	operatorDestination := rig.createOperatorWebhook()
	status, body = rig.onCollection(appID, appID, "routes", http.MethodPost, "", map[string]any{
		"channel_pattern": "it_" + randomSuffix(testInstance), "destinations": []string{appDestination},
	})
	requireStatus(testInstance, "POST app route", status, http.StatusCreated, body)
	status, body = rig.onCollection(appID, appID, "heartbeats", http.MethodPost, "", appCheckBody("owned", appDestination))
	requireStatus(testInstance, "POST app check", status, http.StatusCreated, body)
	operatorRoute := rig.createOperatorRoute(appID, "op_"+randomSuffix(testInstance), appDestination, operatorDestination)
	operatorCheck := rig.createOperatorCheck(appID, "op_check", operatorDestination)
	operatorRouteBefore := routeRow(testInstance, operatorRoute.ID)
	operatorCheckBefore := checkRow(testInstance, operatorCheck.ID)
	for _, table := range []string{"destinations", "routes", "heartbeat_checks"} {
		if count := ownedCount(testInstance, table, appID); count != "1" {
			testInstance.Fatalf("before the cascade the app owns %s %s rows, want 1", count, table)
		}
	}
	if got := bindingAndKeyCount(testInstance, appID); got != "1:1" {
		testInstance.Fatalf("binding and key rows before the cascade = %s, want 1:1", got)
	}

	status, _, body = apiResponse(testInstance, "", http.MethodPost, adminDeprovisionAppPath(appID), deprovisionAppBody(appID))
	requireStatus(testInstance, "deprovision-app", status, http.StatusOK, body)
	var result deprovisionAppJSON
	decodeBody(testInstance, "deprovision-app", body, &result)
	if !result.Removed || !result.Unbound || result.AppID != appID {
		testInstance.Errorf("deprovision-app answer = %+v, want removed and unbound for %s", result, appID)
	}

	if count := publisherRowCount(testInstance, appID); count != "0" {
		testInstance.Errorf("app_publishers rows after the cascade = %s, want 0", count)
	}
	requireNtfyUser(testInstance, "after the cascade", appID+"-publisher", false)
	if got := ntfyTopicStatusWithBasicAuth(testInstance, topic, username, reset.Password); got != http.StatusUnauthorized && got != http.StatusForbidden {
		testInstance.Errorf("subscriber basic-auth after the cascade status = %d, want 401 or 403", got)
	}
	if got := bindingAndKeyCount(testInstance, appID); got != "0:0" {
		testInstance.Errorf("binding and key rows after the cascade = %s, want 0:0", got)
	}
	for _, table := range []string{"destinations", "routes", "heartbeat_checks"} {
		if count := ownedCount(testInstance, table, appID); count != "0" {
			testInstance.Errorf("%s rows the app still owns after the cascade = %s, want 0", table, count)
		}
	}
	keyStatus, _, keyBody := apiResponse(testInstance, bearer(key), http.MethodGet, "/v1/apps/"+appID+"/routes", nil)
	if keyStatus != http.StatusUnauthorized && keyStatus != http.StatusForbidden {
		testInstance.Errorf("the app's old key status = %d, want 401 or 403 (body: %s)", keyStatus, keyBody)
	}

	// Operator-owned objects survive; the operator route lost only its link to the app's destination.
	if got := routeRow(testInstance, operatorRoute.ID); !strings.HasSuffix(got, ":"+operatorDestination) || got == operatorRouteBefore {
		testInstance.Errorf("operator route after the cascade = %q (before %q), want it kept, linked only to %s",
			got, operatorRouteBefore, operatorDestination)
	}
	if got := checkRow(testInstance, operatorCheck.ID); got != operatorCheckBefore {
		testInstance.Errorf("operator check changed by the cascade: %q -> %q", operatorCheckBefore, got)
	}

	// Retry after Unbind: the binding is gone, so this proves provisioning-api's deprovision-app still
	// succeeds for an unbound app (the cascade relies on it), and that every later step is idempotent.
	status, _, body = apiResponse(testInstance, "", http.MethodPost, adminDeprovisionAppPath(appID), deprovisionAppBody(appID))
	requireStatus(testInstance, "second deprovision-app", status, http.StatusOK, body)
	decodeBody(testInstance, "second deprovision-app", body, &result)
	if !result.Removed || result.Unbound {
		testInstance.Errorf("second deprovision-app answer = %+v, want removed and not unbound", result)
	}

	// With the binding gone, minting for the app is refused.
	status, body = apiRequest(testInstance, http.MethodPost, operatorPublisherPath(appID), nil)
	requireStatus(testInstance, "operator mint after the cascade", status, http.StatusNotFound, body)
}

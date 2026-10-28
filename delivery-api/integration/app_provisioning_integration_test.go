//go:build integration

package integration

// End-to-end tests of the app-facing provision and deprovision door
// (/v1/apps/{app_id}/provision and /deprovision) against the live compose stack with real dev dak_
// keys, and of the person-service app row that operator approval now creates. Approval failures
// (person-service down or unconfigured, 502/503) are covered by the unit, pgstore and handler tests
// only; nothing here induces them against the real stack.

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
)

// personServiceScript runs inside the person-service container (which has no host port and no
// guaranteed wget/curl). The method and path arrive as argv, never interpolated into the script.
const personServiceScript = `fetch('http://127.0.0.1:8787'+process.argv[2],{method:process.argv[1]})` +
	`.then(async r=>{console.log(r.status);console.log(await r.text())})` +
	`.catch(e=>{console.error(String(e));process.exit(1)})`

// personServiceRequest sends one request to person-service from inside its container and returns the
// status and body.
func personServiceRequest(testInstance *testing.T, method, path string) (int, string) {
	testInstance.Helper()
	containerID, resolveErr := resolveServiceContainer(execRunner, "NOTIFS_PERSON_SERVICE_CONTAINER", "person-service")
	if resolveErr != nil {
		testInstance.Fatalf("resolve person-service container: %v", resolveErr)
	}
	commandCtx, cancelCommand := context.WithTimeout(context.Background(), dockerCommandLimit)
	defer cancelCommand()
	output, runErr := exec.CommandContext(
		commandCtx, "docker", "exec", containerID, "node", "-e", personServiceScript, method, path,
	).Output()
	if runErr != nil {
		var exitErr *exec.ExitError
		stderr := ""
		if errors.As(runErr, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		testInstance.Fatalf("person-service %s %s failed: %v\nstdout: %s\nstderr: %s", method, path, runErr, output, stderr)
	}
	statusLine, body, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	status, parseErr := strconv.Atoi(strings.TrimSpace(statusLine))
	if parseErr != nil {
		testInstance.Fatalf("person-service %s %s: unparseable status line %q: %v", method, path, statusLine, parseErr)
	}
	return status, body
}

// deletePersonServiceApp removes appID's person-service app row. Only a 200 counts as clean (the
// service answers 200 whether or not the row existed), so a wrong path cannot hide a leak.
func deletePersonServiceApp(testInstance *testing.T, appID string) {
	testInstance.Helper()
	if !appKeyAppPattern.MatchString(appID) {
		testInstance.Errorf("cleanup: refusing to delete a person-service app of the wrong shape: %q", appID)
		return
	}
	status, body := personServiceRequest(testInstance, http.MethodDelete, "/apps/"+appID)
	if status != http.StatusOK {
		testInstance.Errorf("cleanup: delete person-service app %s status = %d, body: %s", appID, status, body)
	}
}

func appProvisionPath(appID string) string   { return "/v1/apps/" + appID + "/provision" }
func appDeprovisionPath(appID string) string { return "/v1/apps/" + appID + "/deprovision" }

type provisionedJSON struct {
	Token string `json:"token"`
}

type deprovisionedJSON struct {
	Removed bool `json:"removed"`
}

func TestApprovalCreatesPersonServiceAppRow(testInstance *testing.T) {
	app := newIdentityApp(testInstance)
	app.bind(testInstance)

	status, body := personServiceRequest(testInstance, http.MethodGet, "/apps")
	requireStatus(testInstance, "list person-service apps", status, http.StatusOK, []byte(body))
	var listed struct {
		Apps []struct {
			AppID       string `json:"app_id"`
			DisplayName string `json:"display_name"`
		} `json:"apps"`
	}
	decodeBody(testInstance, "list person-service apps", []byte(body), &listed)
	found := false
	for _, row := range listed.Apps {
		if row.AppID == app.appID {
			found = true
			if row.DisplayName != app.appID {
				testInstance.Errorf("display_name = %q, want the app id %q", row.DisplayName, app.appID)
			}
		}
	}
	if !found {
		testInstance.Fatalf("approval left no person-service row for %s: %s", app.appID, body)
	}

	// Unbind, then approve the same app again: the existing row (409 upstream) must not fail approval.
	unbindStatus, unbindBody := apiRequest(testInstance, http.MethodDelete, identityAdminPath+"/bindings/"+app.appID, nil)
	requireStatus(testInstance, "unbind", unbindStatus, http.StatusOK, unbindBody)
	app.bind(testInstance)
}

func TestAppProvisionDeprovisionRoundTrip(testInstance *testing.T) {
	waitForReady(testInstance)
	appID := "it_prov_" + randomSuffix(testInstance)
	key := appKeyFor(testInstance, appID)
	email := "it-" + randomSuffix(testInstance) + "@example.test"
	hash := personhash.Hash(email)

	// Remove the provisioned user even when an assertion below stops the test early.
	// Registered after appKeyFor so it runs first (LIFO), while the key is still valid.
	testInstance.Cleanup(func() {
		status, _, body := apiResponse(testInstance, bearer(key), http.MethodPost, appDeprovisionPath(appID), map[string]any{"email": email})
		if status != http.StatusOK && status != http.StatusNotFound {
			testInstance.Errorf("cleanup: deprovision %s status = %d, body: %s", appID, status, body)
		}
	})

	status, headers, body := apiResponse(testInstance, bearer(key), http.MethodPost, appProvisionPath(appID), map[string]any{"email": email})
	requireStatus(testInstance, "provision", status, http.StatusOK, body)
	if cacheControl := headers.Get("Cache-Control"); cacheControl != "no-store" {
		testInstance.Errorf("provision Cache-Control = %q, want no-store", cacheControl)
	}
	var provisioned provisionedJSON
	decodeBody(testInstance, "provision", body, &provisioned)
	if provisioned.Token == "" {
		testInstance.Errorf("provision carried no token: %s", body)
	}

	status, _, body = apiResponse(testInstance, bearer(key), http.MethodPost, appProvisionPath(appID), map[string]any{"email": email})
	requireStatus(testInstance, "provision again", status, http.StatusOK, body)

	status, _, body = apiResponse(testInstance, bearer(key), http.MethodPost, appDeprovisionPath(appID), map[string]any{"person_hash": hash})
	requireStatus(testInstance, "deprovision by person_hash", status, http.StatusOK, body)
	var removed deprovisionedJSON
	decodeBody(testInstance, "deprovision", body, &removed)
	if !removed.Removed {
		testInstance.Errorf("deprovision removed = false: %s", body)
	}

	// The person's last grant was revoked, so the ntfy user is gone and a repeat is still a 200.
	status, _, body = apiResponse(testInstance, bearer(key), http.MethodPost, appDeprovisionPath(appID), map[string]any{"person_hash": hash})
	requireStatus(testInstance, "deprovision again", status, http.StatusOK, body)
}

func TestAppProvisioningIsScopedToTheCallingApp(testInstance *testing.T) {
	waitForReady(testInstance)
	appA := "it_prova_" + randomSuffix(testInstance)
	appB := "it_provb_" + randomSuffix(testInstance)
	keyA := appKeyFor(testInstance, appA)
	keyB := appKeyFor(testInstance, appB)
	email := "it-" + randomSuffix(testInstance) + "@example.test"

	// Registered after appKeyFor so it runs first (LIFO), while the key is still valid.
	testInstance.Cleanup(func() {
		status, _, body := apiResponse(testInstance, bearer(keyA), http.MethodPost, appDeprovisionPath(appA), map[string]any{"email": email})
		if status != http.StatusOK && status != http.StatusNotFound {
			testInstance.Errorf("cleanup: deprovision %s status = %d, body: %s", appA, status, body)
		}
	})

	status, _, body := apiResponse(testInstance, bearer(keyA), http.MethodPost, appProvisionPath(appA), map[string]any{"email": email})
	requireStatus(testInstance, "provision for A", status, http.StatusOK, body)

	// A key cannot act on another app's path.
	status, _, body = apiResponse(testInstance, bearer(keyA), http.MethodPost, appProvisionPath(appB), map[string]any{"email": email})
	requireStatus(testInstance, "A's key on B's path", status, http.StatusForbidden, body)

	// App B cannot remove a person granted only to app A.
	status, _, body = apiResponse(testInstance, bearer(keyB), http.MethodPost, appDeprovisionPath(appB), map[string]any{"person_hash": personhash.Hash(email)})
	requireStatus(testInstance, "B deprovisions A's person", status, http.StatusNotFound, body)
	requireErrorMessage(testInstance, "B deprovisions A's person", body, "user does not exist")

	// No key at all.
	status, _, body = apiResponse(testInstance, "", http.MethodPost, appProvisionPath(appA), map[string]any{"email": email})
	requireStatus(testInstance, "provision without a key", status, http.StatusUnauthorized, body)
}

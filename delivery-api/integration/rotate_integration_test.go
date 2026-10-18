//go:build integration

package integration

// End-to-end tests of POST /v1/admin/destinations/{name}/rotate-secret and the
// per-key config merge of PATCH against the live compose stack.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	rotateOverlapHours  = 24
	rotateExpiryDrift   = 2 * time.Minute
	rotatedSecretLength = 43
)

type rotateEnvelope struct {
	destinationEnvelope
	SigningSecret string `json:"signing_secret"`
}

func TestRotateSecretEndToEnd(testInstance *testing.T) {
	waitForReady(testInstance)
	name, oldSecret, _ := createWebhookDestination(testInstance)

	status, body := adminRequest(testInstance, http.MethodPost, "/destinations/"+name+"/rotate-secret",
		map[string]int{"overlap_hours": rotateOverlapHours})

	requireStatus(testInstance, "POST rotate-secret", status, http.StatusOK, body)
	var rotated rotateEnvelope
	if decodeErr := json.Unmarshal(body, &rotated); decodeErr != nil {
		testInstance.Fatalf("decoding rotate response %q: %v", body, decodeErr)
	}
	if len(rotated.SigningSecret) != rotatedSecretLength || rotated.SigningSecret == oldSecret {
		testInstance.Fatalf("signing_secret = %q", rotated.SigningSecret)
	}
	view := rotated.Destination
	if view.Secrets["signing_secret"] != "****" || view.Secrets["signing_secret_previous"] != "****" {
		testInstance.Fatalf("secrets not masked: %v", view.Secrets)
	}
	expiry, parseErr := time.Parse(time.RFC3339, view.Config["previous_secret_expires_at"])
	if parseErr != nil {
		testInstance.Fatalf("expiry %q: %v", view.Config["previous_secret_expires_at"], parseErr)
	}
	want := time.Now().Add(rotateOverlapHours * time.Hour)
	if drift := expiry.Sub(want); drift > rotateExpiryDrift || drift < -rotateExpiryDrift {
		testInstance.Fatalf("expiry = %s, want about %s", expiry, want)
	}
	// Neither the old nor the new plaintext secret is visible at rest.
	assertSecretsSealedAtRest(testInstance, name, oldSecret)
	assertSecretsSealedAtRest(testInstance, name, rotated.SigningSecret)

	// A config PATCH that omits the expiry key keeps the rotation pair intact.
	patchStatus, patchBody := adminRequest(testInstance, http.MethodPatch, "/destinations/"+name,
		map[string]any{"config": map[string]string{}, "enabled": false})
	requireStatus(testInstance, "PATCH config {}", patchStatus, http.StatusOK, patchBody)
	var patched destinationEnvelope
	if decodeErr := json.Unmarshal(patchBody, &patched); decodeErr != nil {
		testInstance.Fatalf("decoding patch response %q: %v", patchBody, decodeErr)
	}
	if patched.Destination.Config["previous_secret_expires_at"] != view.Config["previous_secret_expires_at"] ||
		patched.Destination.Secrets["signing_secret_previous"] != "****" {
		testInstance.Fatalf("the PATCH dropped the rotation pair: %+v", patched.Destination)
	}

	// A disabled destination still rotates, and stays disabled.
	again, againBody := adminRequest(testInstance, http.MethodPost, "/destinations/"+name+"/rotate-secret",
		map[string]int{"overlap_hours": 1})
	requireStatus(testInstance, "POST rotate-secret (disabled)", again, http.StatusOK, againBody)
	var second rotateEnvelope
	if decodeErr := json.Unmarshal(againBody, &second); decodeErr != nil {
		testInstance.Fatalf("decoding rotate response %q: %v", againBody, decodeErr)
	}
	if second.Destination.Enabled || second.SigningSecret == rotated.SigningSecret {
		testInstance.Fatalf("second rotation: enabled=%v secret reused=%v",
			second.Destination.Enabled, second.SigningSecret == rotated.SigningSecret)
	}
	if strings.Contains(string(againBody), rotated.SigningSecret) {
		testInstance.Fatal("the first rotation's secret leaked into the second response")
	}
}

func TestRotateSecretErrors(testInstance *testing.T) {
	waitForReady(testInstance)
	name, _, _ := createWebhookDestination(testInstance)

	missingStatus, missingBody := adminRequest(testInstance, http.MethodPost,
		"/destinations/itest_"+randomSuffix(testInstance)+"/rotate-secret", map[string]int{"overlap_hours": 1})
	requireStatus(testInstance, "rotate unknown", missingStatus, http.StatusNotFound, missingBody)
	requireErrorMessage(testInstance, "rotate unknown", missingBody, "destination does not exist")

	badStatus, badBody := adminRequest(testInstance, http.MethodPost, "/destinations/"+name+"/rotate-secret",
		map[string]int{"overlap_hours": 0})
	requireStatus(testInstance, "rotate overlap 0", badStatus, http.StatusBadRequest, badBody)
	requireErrorMessage(testInstance, "rotate overlap 0", badBody, "invalid overlap_hours")

	ntfyName := "itest_" + randomSuffix(testInstance)
	createStatus, createBody := adminRequest(testInstance, http.MethodPost, "/destinations", map[string]any{
		"name": ntfyName, "type": "ntfy", "config": map[string]string{"topic": "alerts"},
	})
	if createStatus == http.StatusCreated {
		cleanupDestination(testInstance, ntfyName)
	}
	requireStatus(testInstance, "create ntfy", createStatus, http.StatusCreated, createBody)
	ntfyStatus, ntfyBody := adminRequest(testInstance, http.MethodPost, "/destinations/"+ntfyName+"/rotate-secret",
		map[string]int{"overlap_hours": 1})
	requireStatus(testInstance, "rotate ntfy", ntfyStatus, http.StatusUnprocessableEntity, ntfyBody)
	requireErrorMessage(testInstance, "rotate ntfy", ntfyBody, "destination type does not support rotation")
}

func TestPatchPartialConfigKeepsOtherKeys(testInstance *testing.T) {
	waitForReady(testInstance)
	name := "itest_" + randomSuffix(testInstance)
	createStatus, createBody := adminRequest(testInstance, http.MethodPost, "/destinations", map[string]any{
		"name": name, "type": "ntfy", "config": map[string]string{"topic": "alerts"},
	})
	if createStatus == http.StatusCreated {
		cleanupDestination(testInstance, name)
	}
	requireStatus(testInstance, "create ntfy", createStatus, http.StatusCreated, createBody)

	// An empty config patch is a no-op; the stored topic survives.
	status, body := adminRequest(testInstance, http.MethodPatch, "/destinations/"+name,
		map[string]any{"config": map[string]string{}, "enabled": false})

	requireStatus(testInstance, "PATCH config {}", status, http.StatusOK, body)
	var patched destinationEnvelope
	if decodeErr := json.Unmarshal(body, &patched); decodeErr != nil {
		testInstance.Fatalf("decoding patch response %q: %v", body, decodeErr)
	}
	if patched.Destination.Config["topic"] != "alerts" || patched.Destination.Enabled {
		testInstance.Fatalf("unexpected destination: %+v", patched.Destination)
	}
}

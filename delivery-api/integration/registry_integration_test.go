//go:build integration

package integration

// End-to-end tests of the /v1/admin registry API against the live compose stack
// (see integration_test.go for how the URL and containers are resolved). They
// exercise the real HTTP + Service + PGStore + Postgres path, including
// encryption at rest of destination secrets.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	adminCleanupTimeout = 10 * time.Second
	adminBasePath       = "/v1/admin"
	// plaintextPathSecret is the credential-bearing path segment of the webhook
	// URL; it must never be visible in the database or in an API response.
	plaintextPathSecret = "SECRETPATH"
	signingSecretLength = 40
)

// adminClient bounds each admin request so a hung server fails the test.
var adminClient = &http.Client{Timeout: httpRequestTimeout}

// destinationEnvelope and routeEnvelope mirror the admin response wrappers.
type destinationEnvelope struct {
	Destination registry.DestinationView `json:"destination"`
}

type routeEnvelope struct {
	Route registry.RouteView `json:"route"`
}

type errorEnvelope struct {
	Error string `json:"error"`
}

// randomSuffix returns 16 lowercase hex characters, unique per call.
func randomSuffix(testInstance *testing.T) string {
	testInstance.Helper()
	raw := make([]byte, 8)
	if _, readErr := rand.Read(raw); readErr != nil {
		testInstance.Fatalf("reading random bytes: %v", readErr)
	}
	return hex.EncodeToString(raw)
}

// adminRequest sends one admin API request with an optional JSON body and
// returns the status code and raw response body.
func adminRequest(testInstance *testing.T, method, path string, body any) (int, []byte) {
	testInstance.Helper()
	return adminRequestContext(testInstance, context.Background(), method, path, body)
}

// adminRequestContext is adminRequest with an explicit context, used by cleanup
// so teardown survives test cancellation.
func adminRequestContext(
	testInstance *testing.T, requestCtx context.Context, method, path string, body any,
) (int, []byte) {
	testInstance.Helper()
	var requestBody io.Reader
	if body != nil {
		encoded, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			testInstance.Fatalf("encoding %s %s body: %v", method, path, marshalErr)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, requestErr := http.NewRequestWithContext(requestCtx, method, deliveryURL()+adminBasePath+path, requestBody)
	if requestErr != nil {
		testInstance.Fatalf("building %s %s request: %v", method, path, requestErr)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, doErr := adminClient.Do(request)
	if doErr != nil {
		testInstance.Fatalf("%s %s: %v", method, path, doErr)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			testInstance.Errorf("closing %s %s body: %v", method, path, closeErr)
		}
	}()
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		testInstance.Fatalf("reading %s %s body: %v", method, path, readErr)
	}
	return response.StatusCode, responseBody
}

// requireStatus fails the test unless got equals want, printing the body.
func requireStatus(testInstance *testing.T, label string, got, want int, body []byte) {
	testInstance.Helper()
	if got != want {
		testInstance.Fatalf("%s status = %d, want %d (body: %s)", label, got, want, body)
	}
}

// requireErrorMessage decodes an error body and compares its message.
func requireErrorMessage(testInstance *testing.T, label string, body []byte, want string) {
	testInstance.Helper()
	var decoded errorEnvelope
	if decodeErr := json.Unmarshal(body, &decoded); decodeErr != nil {
		testInstance.Fatalf("%s: decoding error body %q: %v", label, body, decodeErr)
	}
	if decoded.Error != want {
		testInstance.Fatalf("%s error = %q, want %q", label, decoded.Error, want)
	}
}

// cleanupDestination registers a teardown that deletes the named destination
// through the API on a context detached from the test's cancellation. Delete is
// idempotent, so it is safe even if the test already deleted it.
func cleanupDestination(testInstance *testing.T, name string) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(context.Background()), adminCleanupTimeout)
		defer cancelCleanup()
		status, body := adminRequestContext(testInstance, cleanupCtx, http.MethodDelete, "/destinations/"+name, nil)
		if status != http.StatusOK {
			testInstance.Errorf("cleanup: DELETE destination %s status = %d, body: %s", name, status, body)
		}
	})
}

// cleanupRoute is cleanupDestination for a route id. Register it after the
// destination's cleanup: Cleanup runs LIFO, so the route goes first.
func cleanupRoute(testInstance *testing.T, routeID int64) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(context.Background()), adminCleanupTimeout)
		defer cancelCleanup()
		path := fmt.Sprintf("/routes/%d", routeID)
		status, body := adminRequestContext(testInstance, cleanupCtx, http.MethodDelete, path, nil)
		if status != http.StatusOK {
			testInstance.Errorf("cleanup: DELETE route %d status = %d, body: %s", routeID, status, body)
		}
	})
}

// createWebhookDestination creates a webhook destination whose secrets embed
// plaintextPathSecret and a fresh signing secret, registers its cleanup, and
// returns the name, the signing secret and the decoded response.
func createWebhookDestination(testInstance *testing.T) (string, string, registry.DestinationView) {
	testInstance.Helper()
	name := "itest_" + randomSuffix(testInstance)
	// 40 printable-ASCII characters, unique per run so the plaintext check
	// cannot match by coincidence.
	signingSecret := ("sign" + randomSuffix(testInstance) + strings.Repeat("x", signingSecretLength))[:signingSecretLength]

	status, body := adminRequest(testInstance, http.MethodPost, "/destinations", map[string]any{
		"name": name,
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            "https://example.com/hook/" + plaintextPathSecret,
			"signing_secret": signingSecret,
		},
	})
	if status == http.StatusCreated {
		// Register cleanup before asserting anything else so a later failure
		// still removes the row.
		cleanupDestination(testInstance, name)
	}
	requireStatus(testInstance, "POST destination", status, http.StatusCreated, body)

	var decoded destinationEnvelope
	if decodeErr := json.Unmarshal(body, &decoded); decodeErr != nil {
		testInstance.Fatalf("decoding created destination %q: %v", body, decodeErr)
	}
	return name, signingSecret, decoded.Destination
}

func TestRegistryEncryptionAndLifecycle(testInstance *testing.T) {
	waitForReady(testInstance)
	name, signingSecret, created := createWebhookDestination(testInstance)

	if created.Name != name {
		testInstance.Fatalf("created name = %q, want %q", created.Name, name)
	}
	if got := created.Secrets["url"]; got != "https://example.com/****" {
		testInstance.Fatalf("masked secrets.url = %q, want %q", got, "https://example.com/****")
	}
	if got := created.Secrets["signing_secret"]; got != "****" {
		testInstance.Fatalf("masked secrets.signing_secret = %q, want ****", got)
	}

	assertSecretsSealedAtRest(testInstance, name, signingSecret)

	routeStatus, routeBody := adminRequest(testInstance, http.MethodPost, "/routes", map[string]any{
		"app_pattern":     "*",
		"channel_pattern": name + "_*",
		"destinations":    []string{name},
	})
	requireStatus(testInstance, "POST route", routeStatus, http.StatusCreated, routeBody)
	var createdRoute routeEnvelope
	if decodeErr := json.Unmarshal(routeBody, &createdRoute); decodeErr != nil {
		testInstance.Fatalf("decoding created route %q: %v", routeBody, decodeErr)
	}
	cleanupRoute(testInstance, createdRoute.Route.ID)
	routePath := fmt.Sprintf("/routes/%d", createdRoute.Route.ID)

	// A destination a route still references cannot be deleted.
	inUseStatus, inUseBody := adminRequest(testInstance, http.MethodDelete, "/destinations/"+name, nil)
	requireStatus(testInstance, "DELETE in-use destination", inUseStatus, http.StatusConflict, inUseBody)
	requireErrorMessage(testInstance, "DELETE in-use destination", inUseBody, "destination is used by a route")

	deleteRouteStatus, deleteRouteBody := adminRequest(testInstance, http.MethodDelete, routePath, nil)
	requireStatus(testInstance, "DELETE route", deleteRouteStatus, http.StatusOK, deleteRouteBody)

	// Once the route is gone the delete succeeds, and repeating it is a no-op 200.
	for attempt := 1; attempt <= 2; attempt++ {
		status, body := adminRequest(testInstance, http.MethodDelete, "/destinations/"+name, nil)
		requireStatus(testInstance, fmt.Sprintf("DELETE destination attempt %d", attempt), status, http.StatusOK, body)
	}
}

// assertSecretsSealedAtRest reads the stored sealed_secrets column and fails if
// either plaintext secret is visible in its hex or escaped-text form.
func assertSecretsSealedAtRest(testInstance *testing.T, name, signingSecret string) {
	testInstance.Helper()
	// name matches the slug rule, so interpolating it into SQL is safe.
	hexForm := psql(testInstance, fmt.Sprintf("select encode(sealed_secrets,'hex') from destinations where name = '%s'", name))
	escapedForm := psql(testInstance, fmt.Sprintf("select encode(sealed_secrets,'escape') from destinations where name = '%s'", name))
	// The whole row as text, so a plaintext leak into any column (config included) is caught too.
	rowForm := psql(testInstance, fmt.Sprintf("select d::text from destinations d where name = '%s'", name))
	if hexForm == "" || rowForm == "" {
		testInstance.Fatalf("no row found for destination %s", name)
	}
	for label, plaintext := range map[string]string{"path": plaintextPathSecret, "signing secret": signingSecret} {
		if strings.Contains(rowForm, plaintext) {
			testInstance.Fatalf("destination row contains the plaintext %s", label)
		}
		if strings.Contains(escapedForm, plaintext) {
			testInstance.Fatalf("sealed_secrets contains the plaintext %s in raw form", label)
		}
		if strings.Contains(hexForm, hex.EncodeToString([]byte(plaintext))) {
			testInstance.Fatalf("sealed_secrets contains the plaintext %s in hex form", label)
		}
	}
}

func TestRouteWithUnknownDestinationIsRejected(testInstance *testing.T) {
	waitForReady(testInstance)
	// The destination name is never created; the channel pattern stays short
	// because channels are limited to 32 characters.
	missing := "itest_" + randomSuffix(testInstance)

	status, body := adminRequest(testInstance, http.MethodPost, "/routes", map[string]any{
		"app_pattern":     "*",
		"channel_pattern": missing + "_*",
		"destinations":    []string{missing},
	})

	requireStatus(testInstance, "POST route with unknown destination", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "POST route with unknown destination", body, "unknown destination")
}

func TestPatchDestinationTypeIsRejected(testInstance *testing.T) {
	waitForReady(testInstance)
	name, _, _ := createWebhookDestination(testInstance)

	status, body := adminRequest(testInstance, http.MethodPatch, "/destinations/"+name, map[string]string{"type": "discord"})

	requireStatus(testInstance, "PATCH destination type", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "PATCH destination type", body, "invalid JSON body")
}

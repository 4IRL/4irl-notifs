//go:build integration

package integration

// End-to-end tests of the app-scoped destination API (/v1/apps/{app_id}/destinations) against the live
// compose stack, with two app keys from the dev token exchange (app_key_integration_test.go): each app
// manages only its own destinations, another owner's destination is indistinguishable from an absent one,
// names and ntfy topics carry the app's prefix, and an app-supplied URL is SSRF-checked on create, PATCH
// and rotate-secret. URLs below are IP literals, so the checks need no DNS from the container.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	appDestinationsAppPrefix = "itapp_"
	// publicWebhookURL is a public address literal: the registration-time check accepts it without any
	// DNS lookup, and nothing ever dials it in these tests.
	publicWebhookURL  = "https://8.8.8.8/hook/" + plaintextPathSecret
	privateWebhookURL = "https://10.0.0.5/hook"
	notFoundMessage   = "destination does not exist"
	notPublicMessage  = "secrets.url must resolve to public addresses"
	unresolvedMessage = "secrets.url host could not be resolved"
	constraintMessage = "request violates a data constraint"
)

// appDestinationsRig is three bound apps and a path builder for each. appC never owns anything unless a test
// creates something for it.
type appDestinationsRig struct {
	testInstance     *testing.T
	appA, appB, appC string
}

func newAppDestinationsRig(testInstance *testing.T) appDestinationsRig {
	testInstance.Helper()
	waitForReady(testInstance)
	suffix := randomSuffix(testInstance)
	return appDestinationsRig{
		testInstance: testInstance,
		appA:         appDestinationsAppPrefix + "a" + suffix,
		appB:         appDestinationsAppPrefix + "b" + suffix,
		appC:         appDestinationsAppPrefix + "c" + suffix,
	}
}

// request sends one request as app asking for pathApp's collection, authenticated with app's own key.
func (rig appDestinationsRig) request(app, pathApp, method, subpath string, body any) (int, []byte) {
	rig.testInstance.Helper()
	path := "/v1/apps/" + pathApp + "/destinations" + subpath
	return apiRequestWithAuthorization(rig.testInstance, bearer(appKeyFor(rig.testInstance, app)), method, path, body)
}

// as sends a request as app for its own collection.
func (rig appDestinationsRig) as(app, method, subpath string, body any) (int, []byte) {
	rig.testInstance.Helper()
	return rig.request(app, app, method, subpath, body)
}

func webhookBody(name, url string) map[string]any {
	return map[string]any{
		"name": name, "type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            url,
			"signing_secret": ("sign" + strings.Repeat("y", signingSecretLength))[:signingSecretLength],
		},
	}
}

// create creates a webhook destination as app and registers its cleanup.
func (rig appDestinationsRig) create(app, name, url string) registry.DestinationView {
	rig.testInstance.Helper()
	status, body := rig.as(app, http.MethodPost, "", webhookBody(name, url))
	if status == http.StatusCreated {
		cleanupDestination(rig.testInstance, name)
	}
	requireStatus(rig.testInstance, "POST "+name+" as "+app, status, http.StatusCreated, body)
	var created destinationEnvelope
	decodeBody(rig.testInstance, "created destination", body, &created)
	return created.Destination
}

func (rig appDestinationsRig) requireRefused(label string, status int, body []byte, wantStatus int, wantMessage string) {
	rig.testInstance.Helper()
	requireStatus(rig.testInstance, label, status, wantStatus, body)
	requireErrorMessage(rig.testInstance, label, body, wantMessage)
}

// requireSameNotFound sends the request as asApp to each of targets (destinations asApp does not own) and to
// absentName (one that does not exist), and requires every answer to be the same 404 with the same bytes.
func (rig appDestinationsRig) requireSameNotFound(
	label, method, subpath string, body any, asApp string, targets []string, absentName string,
) {
	rig.testInstance.Helper()
	absentStatus, absentBody := rig.as(asApp, method, fmt.Sprintf(subpath, absentName), body)
	rig.requireRefused(label+" "+absentName+" as "+asApp, absentStatus, absentBody, http.StatusNotFound, notFoundMessage)
	for _, target := range targets {
		status, answer := rig.as(asApp, method, fmt.Sprintf(subpath, target), body)
		rig.requireRefused(label+" "+target+" as "+asApp, status, answer, http.StatusNotFound, notFoundMessage)
		if string(answer) != string(absentBody) {
			rig.testInstance.Fatalf("%s as %s: the answer for %s differs from the answer for an absent destination: %s vs %s",
				label, asApp, target, answer, absentBody)
		}
	}
}

func TestAppCreatesAnOwnedDestinationAndSeesOnlyItsOwn(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	// App A's destinations are created in an order that differs from the bytewise order the list must use
	// (underscore 0x5F sorts before a letter: "_a_hook" < "_ab_hook" < "_b_hook").
	nameA := rig.appA + "_a_hook"
	nameAB := rig.appA + "_ab_hook"
	nameFirstCreated := rig.appA + "_b_hook"
	nameB := rig.appB + "_hook"

	rig.create(rig.appA, nameFirstCreated, publicWebhookURL)
	created := rig.create(rig.appA, nameA, publicWebhookURL)
	rig.create(rig.appA, nameAB, publicWebhookURL)

	if created.OwnerKind != registry.OwnerApp || created.OwnerAppID == nil || *created.OwnerAppID != rig.appA {
		testInstance.Fatalf("created owner = %q/%v, want app/%s", created.OwnerKind, created.OwnerAppID, rig.appA)
	}
	if got := psql(testInstance, fmt.Sprintf(
		"select owner_kind || ':' || owner_app_id from destinations where name = '%s'", nameA)); got != "app:"+rig.appA {
		testInstance.Fatalf("stored owner = %q, want app:%s: an app-scoped create must never yield an operator-owned row", got, rig.appA)
	}
	assertSecretsSealedAtRest(testInstance, nameA, plaintextPathSecret)

	// The operator sees it, as app-owned.
	status, body := adminRequest(testInstance, http.MethodGet, "/destinations/"+nameA, nil)
	requireStatus(testInstance, "operator GET", status, http.StatusOK, body)
	var adminView destinationEnvelope
	decodeBody(testInstance, "operator GET", body, &adminView)
	if adminView.Destination.OwnerKind != registry.OwnerApp {
		testInstance.Fatalf("operator view owner kind = %q, want app", adminView.Destination.OwnerKind)
	}

	rig.create(rig.appB, nameB, publicWebhookURL)
	operatorName := "itest_" + randomSuffix(testInstance)
	createDestination(testInstance, operatorName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("z", signingSecretLength))[:signingSecretLength],
		},
	})

	// Each app lists exactly its own destinations, in bytewise name order: not the other app's, not the
	// operator's.
	for app, want := range map[string][]string{
		rig.appA: {nameA, nameAB, nameFirstCreated},
		rig.appB: {nameB},
	} {
		status, body = rig.as(app, http.MethodGet, "", nil)
		requireStatus(testInstance, "list as "+app, status, http.StatusOK, body)
		var listed struct {
			Destinations []registry.DestinationView `json:"destinations"`
		}
		decodeBody(testInstance, "list as "+app, body, &listed)
		var names []string
		for _, listedDestination := range listed.Destinations {
			names = append(names, listedDestination.Name)
		}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			testInstance.Fatalf("list as %s = %s, want exactly %v in that order", app, body, want)
		}
	}

	// An app that owns nothing lists an empty array, never null.
	status, body = rig.as(rig.appC, http.MethodGet, "", nil)
	requireStatus(testInstance, "list as "+rig.appC, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"destinations":[]`) {
		testInstance.Fatalf("list as %s = %s, want an empty destinations array", rig.appC, body)
	}
}

func TestAnotherOwnersDestinationIsNotFoundAndNeverChanged(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	nameA := rig.appA + "_hook"
	rig.create(rig.appA, nameA, publicWebhookURL)
	// B needs a binding, and an absent name inside B's own prefix.
	absent := rig.appB + "_absent"
	operatorName := "itest_" + randomSuffix(testInstance)
	createDestination(testInstance, operatorName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("z", signingSecretLength))[:signingSecretLength],
		},
	})
	// An operator-owned destination whose name starts with A's prefix: A is still not its owner, so it is
	// as unreachable for A as any other owner's destination.
	legacyName := rig.appA + "_legacy"
	absentA := rig.appA + "_absent"
	createDestination(testInstance, legacyName, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("z", signingSecretLength))[:signingSecretLength],
		},
	})
	sealedDigest := func(name string) string {
		return psql(testInstance, fmt.Sprintf("select md5(sealed_secrets) from destinations where name = '%s'", name))
	}
	enabledFlag := func(name string) string {
		return psql(testInstance, fmt.Sprintf("select enabled from destinations where name = '%s'", name))
	}
	sealedBefore := sealedDigest(nameA)
	operatorSealedBefore := sealedDigest(operatorName)
	legacySealedBefore := sealedDigest(legacyName)
	operatorEnabledBefore := enabledFlag(operatorName)
	legacyEnabledBefore := enabledFlag(legacyName)

	requests := []struct {
		label, method, subpath string
		body                   any
	}{
		{label: "GET", method: http.MethodGet, subpath: "/%s"},
		{label: "PATCH", method: http.MethodPatch, subpath: "/%s", body: map[string]any{"enabled": false}},
		{label: "PATCH url", method: http.MethodPatch, subpath: "/%s", body: map[string]any{
			"secrets": map[string]string{"url": publicWebhookURL + "x"}}},
		{label: "rotate", method: http.MethodPost, subpath: "/%s/rotate-secret", body: map[string]int{"overlap_hours": 24}},
		{label: "DELETE", method: http.MethodDelete, subpath: "/%s"},
	}
	for _, request := range requests {
		// B asks for A's destination, for the operator's, and for an absent one: all the same answer.
		rig.requireSameNotFound(request.label, request.method, request.subpath, request.body, rig.appB,
			[]string{nameA, operatorName}, absent)
		// A asks for the operator's destination that merely carries A's prefix, and for an absent one in its
		// own prefix: ownership, not the name prefix, decides, so again the same answer.
		rig.requireSameNotFound(request.label, request.method, request.subpath, request.body, rig.appA,
			[]string{legacyName}, absentA)
	}

	// Nothing of A's or the operator's changed.
	status, body := adminRequest(testInstance, http.MethodGet, "/destinations/"+nameA, nil)
	requireStatus(testInstance, "GET A's destination", status, http.StatusOK, body)
	var view destinationEnvelope
	decodeBody(testInstance, "GET A's destination", body, &view)
	if !view.Destination.Enabled {
		testInstance.Fatalf("B disabled A's destination")
	}
	if got := sealedDigest(nameA); got != sealedBefore {
		testInstance.Fatalf("B changed A's sealed secrets")
	}
	for name, before := range map[string]struct{ sealed, enabled string }{
		operatorName: {operatorSealedBefore, operatorEnabledBefore},
		legacyName:   {legacySealedBefore, legacyEnabledBefore},
	} {
		status, body = adminRequest(testInstance, http.MethodGet, "/destinations/"+name, nil)
		requireStatus(testInstance, "GET the operator's destination "+name, status, http.StatusOK, body)
		if got := sealedDigest(name); got != before.sealed {
			testInstance.Fatalf("an app changed the sealed secrets of the operator's destination %s", name)
		}
		if got := enabledFlag(name); got != before.enabled {
			testInstance.Fatalf("an app changed the enabled flag of the operator's destination %s", name)
		}
	}

	// The operator's destination with A's prefix is not in A's list.
	status, body = rig.as(rig.appA, http.MethodGet, "", nil)
	requireStatus(testInstance, "list as "+rig.appA, status, http.StatusOK, body)
	if strings.Contains(string(body), legacyName) {
		testInstance.Fatalf("A's list contains the operator's destination %s: %s", legacyName, body)
	}

	// B's key cannot name A's collection at all.
	status, body = rig.request(rig.appB, rig.appA, http.MethodGet, "", nil)
	rig.requireRefused("B's key on A's path", status, body, http.StatusForbidden, mismatchBody)
}

func TestAppDestinationLifecycle(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	name := rig.appA + "_hook"
	rig.create(rig.appA, name, publicWebhookURL)

	status, body := rig.as(rig.appA, http.MethodGet, "/"+name, nil)
	requireStatus(testInstance, "GET", status, http.StatusOK, body)
	if strings.Contains(string(body), plaintextPathSecret) {
		testInstance.Fatalf("GET leaked the URL path secret: %s", body)
	}

	status, body = rig.as(rig.appA, http.MethodPatch, "/"+name, map[string]any{"enabled": false})
	requireStatus(testInstance, "PATCH", status, http.StatusOK, body)
	var patched destinationEnvelope
	decodeBody(testInstance, "PATCH", body, &patched)
	if patched.Destination.Enabled {
		testInstance.Fatalf("PATCH did not disable the destination")
	}

	// A new public URL is accepted and sealed.
	status, body = rig.as(rig.appA, http.MethodPatch, "/"+name, map[string]any{
		"secrets": map[string]string{"url": "https://8.8.4.4/new/NEWPATHSECRET"}})
	requireStatus(testInstance, "PATCH url", status, http.StatusOK, body)
	assertSecretsSealedAtRest(testInstance, name, "NEWPATHSECRET")

	// Rotate: the new signing secret is only in this response.
	status, body = rig.as(rig.appA, http.MethodPost, "/"+name+"/rotate-secret", map[string]int{"overlap_hours": 24})
	requireStatus(testInstance, "rotate", status, http.StatusOK, body)
	var rotated rotateEnvelope
	decodeBody(testInstance, "rotate", body, &rotated)
	if len(rotated.SigningSecret) != rotatedSecretLength {
		testInstance.Fatalf("rotated signing_secret = %q", rotated.SigningSecret)
	}
	if rotated.Destination.Secrets["signing_secret"] != "****" {
		testInstance.Fatalf("rotated secrets not masked: %v", rotated.Destination.Secrets)
	}
	status, body = rig.as(rig.appA, http.MethodGet, "/"+name, nil)
	requireStatus(testInstance, "GET after rotate", status, http.StatusOK, body)
	if strings.Contains(string(body), rotated.SigningSecret) {
		testInstance.Fatalf("GET after rotate leaked the new signing secret")
	}
	assertSecretsSealedAtRest(testInstance, name, rotated.SigningSecret)

	// A destination a route still uses cannot be deleted (409, as on the admin path); once the route is
	// gone it can, and then it is gone for the app.
	status, body = adminRequest(testInstance, http.MethodPost, "/routes", map[string]any{
		"app_pattern": rig.appA, "channel_pattern": "it_" + randomSuffix(testInstance), "destinations": []string{name},
	})
	requireStatus(testInstance, "POST route to the app's destination", status, http.StatusCreated, body)
	var route routeEnvelope
	decodeBody(testInstance, "POST route", body, &route)
	// Registered right away (the admin route delete is idempotent) so a failed assertion below cannot leave
	// the route behind to block the destination's own cleanup, which runs after it.
	cleanupRoute(testInstance, route.Route.ID)
	routeCleanup := func() {
		deleteStatus, deleteBody := adminRequest(testInstance, http.MethodDelete, fmt.Sprintf("/routes/%d", route.Route.ID), nil)
		requireStatus(testInstance, "DELETE route", deleteStatus, http.StatusOK, deleteBody)
	}
	status, body = rig.as(rig.appA, http.MethodDelete, "/"+name, nil)
	rig.requireRefused("DELETE a linked destination", status, body, http.StatusConflict, "destination is in use")
	routeCleanup()

	status, body = rig.as(rig.appA, http.MethodDelete, "/"+name, nil)
	requireStatus(testInstance, "DELETE", status, http.StatusOK, body)
	status, body = rig.as(rig.appA, http.MethodGet, "/"+name, nil)
	rig.requireRefused("GET after DELETE", status, body, http.StatusNotFound, notFoundMessage)
	status, body = rig.as(rig.appA, http.MethodDelete, "/"+name, nil)
	rig.requireRefused("second DELETE", status, body, http.StatusNotFound, notFoundMessage)
}

func TestAppDestinationNameAndTopicPrefixRules(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	prefix := rig.appA + "_"
	topicPrefix := rig.appA + "-"

	for _, name := range []string{"hook", rig.appB + "_hook", rig.appA + "hook", prefix} {
		status, body := rig.as(rig.appA, http.MethodPost, "", webhookBody(name, publicWebhookURL))
		rig.requireRefused("create "+name, status, body, http.StatusBadRequest, "name must start with "+prefix)
	}
	if got := psql(testInstance, fmt.Sprintf("select count(*) from destinations where owner_app_id = '%s'", rig.appA)); got != "0" {
		testInstance.Fatalf("destinations stored by refused creates = %s, want 0", got)
	}

	ntfyName := rig.appA + "_push"
	for _, topic := range []string{"alerts", rig.appB + "-alerts", topicPrefix} {
		status, body := rig.as(rig.appA, http.MethodPost, "", map[string]any{
			"name": ntfyName, "type": registry.TypeNtfy, "config": map[string]string{"topic": topic},
		})
		rig.requireRefused("create ntfy topic "+topic, status, body, http.StatusBadRequest,
			"config.topic must start with "+topicPrefix)
	}
	status, body := rig.as(rig.appA, http.MethodPost, "", map[string]any{
		"name": ntfyName, "type": registry.TypeNtfy, "config": map[string]string{"topic": topicPrefix + "alerts"},
	})
	if status == http.StatusCreated {
		cleanupDestination(testInstance, ntfyName)
	}
	requireStatus(testInstance, "create ntfy with the prefixed topic", status, http.StatusCreated, body)

	status, body = rig.as(rig.appA, http.MethodPatch, "/"+ntfyName,
		map[string]any{"config": map[string]string{"topic": rig.appB + "-alerts"}})
	rig.requireRefused("PATCH topic out of the prefix", status, body, http.StatusBadRequest,
		"config.topic must start with "+topicPrefix)
	status, body = rig.as(rig.appA, http.MethodPatch, "/"+ntfyName,
		map[string]any{"config": map[string]string{"topic": topicPrefix + "other"}})
	requireStatus(testInstance, "PATCH topic inside the prefix", status, http.StatusOK, body)
}

func TestAppDestinationURLsAreCheckedOnCreatePatchAndRotate(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	name := rig.appA + "_hook"

	// Create: every non-public literal is refused and nothing is stored.
	for _, url := range []string{
		privateWebhookURL, blockedWebhookURL, "https://169.254.169.254/latest", "https://[::1]/hook", "https://0.0.0.0/hook",
	} {
		status, body := rig.as(rig.appA, http.MethodPost, "", webhookBody(name, url))
		rig.requireRefused("create with "+url, status, body, http.StatusBadRequest, notPublicMessage)
	}
	status, body := rig.as(rig.appA, http.MethodPost, "", webhookBody(name, "https://nonexistent.invalid/hook"))
	rig.requireRefused("create with an unresolvable host", status, body, http.StatusBadRequest, unresolvedMessage)
	if got := psql(testInstance, fmt.Sprintf("select count(*) from destinations where name = '%s'", name)); got != "0" {
		testInstance.Fatalf("destinations stored by refused creates = %s, want 0", got)
	}

	// A refused discord URL: the host allow-list answers before any lookup.
	status, body = rig.as(rig.appA, http.MethodPost, "", map[string]any{
		"name": rig.appA + "_chat", "type": registry.TypeDiscord,
		"secrets": map[string]string{"webhook_url": "https://10.0.0.5/api/webhooks/1/abc"},
	})
	rig.requireRefused("create discord with a private host", status, body, http.StatusBadRequest, "invalid secrets.webhook_url")

	rig.create(rig.appA, name, publicWebhookURL)
	sealedBefore := psql(testInstance, fmt.Sprintf("select md5(sealed_secrets) from destinations where name = '%s'", name))

	// PATCH: a private URL is refused and the stored secrets do not change.
	status, body = rig.as(rig.appA, http.MethodPatch, "/"+name,
		map[string]any{"secrets": map[string]string{"url": privateWebhookURL}})
	rig.requireRefused("PATCH to a private URL", status, body, http.StatusBadRequest, notPublicMessage)
	status, body = rig.as(rig.appA, http.MethodPatch, "/"+name,
		map[string]any{"secrets": map[string]string{"url": "https://nonexistent.invalid/hook"}})
	rig.requireRefused("PATCH to an unresolvable host", status, body, http.StatusBadRequest, unresolvedMessage)
	if got := psql(testInstance, fmt.Sprintf("select md5(sealed_secrets) from destinations where name = '%s'", name)); got != sealedBefore {
		testInstance.Fatalf("a refused PATCH changed the sealed secrets")
	}

	// A PATCH that leaves the URL alone is never URL-checked.
	status, body = rig.as(rig.appA, http.MethodPatch, "/"+name, map[string]any{"enabled": false})
	requireStatus(testInstance, "PATCH of enabled only", status, http.StatusOK, body)

	// The operator is never URL-checked: it may point the app's destination at a private address. The
	// app's rotate-secret then re-checks the stored URL and refuses, changing nothing.
	status, body = adminRequest(testInstance, http.MethodPatch, "/destinations/"+name,
		map[string]any{"secrets": map[string]string{"url": privateWebhookURL}})
	requireStatus(testInstance, "operator PATCH to a private URL", status, http.StatusOK, body)
	sealedBefore = psql(testInstance, fmt.Sprintf("select md5(sealed_secrets) from destinations where name = '%s'", name))

	status, body = rig.as(rig.appA, http.MethodPost, "/"+name+"/rotate-secret", map[string]int{"overlap_hours": 24})
	rig.requireRefused("rotate with a private stored URL", status, body, http.StatusBadRequest, notPublicMessage)
	if got := psql(testInstance, fmt.Sprintf("select md5(sealed_secrets) from destinations where name = '%s'", name)); got != sealedBefore {
		testInstance.Fatalf("a refused rotate changed the sealed secrets")
	}
}

func TestAdminPatchOfAnAppOwnedRoutesPatternIsA400(testInstance *testing.T) {
	waitForReady(testInstance)
	appID := appDestinationsAppPrefix + "r" + randomSuffix(testInstance)
	channel := "it_" + randomSuffix(testInstance)
	testInstance.Cleanup(func() { psql(testInstance, fmt.Sprintf("DELETE FROM routes WHERE channel_pattern = '%s'", channel)) })
	// Seeded with SQL: this test pins the admin API's own mapping of the database CHECK, independent of the
	// app-scoped route API (which refuses such a pattern itself, before the database).
	psql(testInstance, fmt.Sprintf(
		"INSERT INTO routes (app_pattern, channel_pattern, owner_kind, owner_app_id) VALUES ('%s', '%s', 'app', '%s')",
		appID, channel, appID))
	routeID := psql(testInstance, fmt.Sprintf("SELECT id FROM routes WHERE channel_pattern = '%s'", channel))

	status, body := adminRequest(testInstance, http.MethodPatch, "/routes/"+routeID, map[string]any{"app_pattern": "*"})

	requireStatus(testInstance, "admin PATCH of an app-owned route's app_pattern", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "admin PATCH of an app-owned route's app_pattern", body, constraintMessage)
	if got := psql(testInstance, "SELECT app_pattern FROM routes WHERE id = "+routeID); got != appID {
		testInstance.Fatalf("app_pattern = %q, want it unchanged (%s)", got, appID)
	}
}

func TestAppDestinationRoutesRequireTheAppKey(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	path := "/v1/apps/" + rig.appA + "/destinations"
	for _, authorization := range []string{"", "Bearer", "Bearer not-a-delivery-key"} {
		status, body := apiRequestWithAuthorization(testInstance, authorization, http.MethodGet, path, nil)
		rig.requireRefused("GET with header "+authorization, status, body, http.StatusUnauthorized, missingKeyBody)
	}
	status, body := apiRequestWithAuthorization(testInstance, bearer("dak_"+strings.Repeat("A", 43)), http.MethodGet, path, nil)
	rig.requireRefused("GET with an unknown key", status, body, http.StatusUnauthorized, invalidKeyBody)
}

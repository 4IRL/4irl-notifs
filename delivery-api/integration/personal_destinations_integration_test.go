//go:build integration

package integration

// End-to-end test of the app-facing personal destination API (/v1/apps/{app_id}/personal-destinations)
// against the live compose stack, with two bound apps and two people: any app lists a person's
// destinations, only the registering app edits one, every app toggles its own opt-in, another person's
// id reads as absent, and the SSRF check and the type rule refuse bad input. URLs are IP literals, so the
// checks need no DNS from the container.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
)

const (
	personalAppPrefix       = "itpers_"
	personalNotFoundMessage = "personal destination does not exist"
	personalOtherAppMessage = "personal destination is managed by another app"
)

type personalViewJSON struct {
	ID                int64             `json:"id"`
	PersonHash        string            `json:"person_hash"`
	Label             string            `json:"label"`
	Type              string            `json:"type"`
	Secrets           map[string]string `json:"secrets"`
	Enabled           bool              `json:"enabled"`
	RegisteredByAppID string            `json:"registered_by_app_id"`
	ManagedByCaller   bool              `json:"managed_by_caller"`
	OptedIn           bool              `json:"opted_in"`
}

type personalEnvelopeJSON struct {
	PersonalDestination personalViewJSON `json:"personal_destination"`
}

type personalLookupJSON struct {
	PersonHash           string             `json:"person_hash"`
	PersonalDestinations []personalViewJSON `json:"personal_destinations"`
}

func personalRequest(testInstance *testing.T, app, method, subpath string, body any) (int, []byte) {
	testInstance.Helper()
	path := "/v1/apps/" + app + "/personal-destinations" + subpath
	return apiRequestWithAuthorization(testInstance, bearer(appKeyFor(testInstance, app)), method, path, body)
}

func TestPersonalDestinationsAcrossTwoAppsAndTwoPeople(testInstance *testing.T) {
	waitForReady(testInstance)
	suffix := randomSuffix(testInstance)
	appA := personalAppPrefix + "a" + suffix
	appB := personalAppPrefix + "b" + suffix
	emailOne := "it-one-" + suffix + "@example.com"
	emailTwo := "it-two-" + suffix + "@example.com"
	hashOne := personhash.Hash(emailOne)
	hashTwo := personhash.Hash(emailTwo)
	// Rows are keyed by the throwaway people: remove them (opt-ins cascade) whatever the outcome.
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf(
			"delete from personal_destinations where person_hash in ('%s', '%s')", hashOne, hashTwo))
	})

	// Register as app A for person one.
	status, body := personalRequest(testInstance, appA, http.MethodPost, "", map[string]any{
		"email": emailOne, "label": "Phone", "type": "webhook",
		"secrets": map[string]string{
			"url":            publicWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("q", signingSecretLength))[:signingSecretLength],
		},
	})
	requireStatus(testInstance, "A registers", status, http.StatusCreated, body)
	var created personalEnvelopeJSON
	decodeBody(testInstance, "created personal destination", body, &created)
	registered := created.PersonalDestination
	if registered.PersonHash != hashOne || registered.RegisteredByAppID != appA || !registered.ManagedByCaller || !registered.OptedIn {
		testInstance.Fatalf("created view = %+v, want person one's, registered by A, managed by and opted in for the caller", registered)
	}
	if strings.Contains(string(body), plaintextPathSecret) || strings.Contains(string(body), emailOne) {
		testInstance.Fatalf("the create response echoed a secret or the email: %s", body)
	}
	idPath := fmt.Sprintf("/%d", registered.ID)

	// App B sees it, not managing it and not opted in.
	status, body = personalRequest(testInstance, appB, http.MethodPost, "/lookup", map[string]any{"email": emailOne})
	requireStatus(testInstance, "B lookup", status, http.StatusOK, body)
	var lookup personalLookupJSON
	decodeBody(testInstance, "B lookup", body, &lookup)
	if lookup.PersonHash != hashOne || len(lookup.PersonalDestinations) != 1 ||
		lookup.PersonalDestinations[0].ID != registered.ID || lookup.PersonalDestinations[0].ManagedByCaller ||
		lookup.PersonalDestinations[0].OptedIn {
		testInstance.Fatalf("B lookup = %+v, want the one destination with managed_by_caller=false and opted_in=false", lookup)
	}

	// B cannot edit or delete it.
	status, body = personalRequest(testInstance, appB, http.MethodPatch, idPath, map[string]any{"email": emailOne, "label": "Hijack"})
	requireStatus(testInstance, "B PATCH", status, http.StatusForbidden, body)
	requireErrorMessage(testInstance, "B PATCH", body, personalOtherAppMessage)
	status, body = personalRequest(testInstance, appB, http.MethodDelete, idPath, map[string]any{"email": emailOne})
	requireStatus(testInstance, "B DELETE", status, http.StatusForbidden, body)
	requireErrorMessage(testInstance, "B DELETE", body, personalOtherAppMessage)

	// B opts in, and the next lookup shows it.
	status, body = personalRequest(testInstance, appB, http.MethodPut, idPath+"/opt-in", map[string]any{"person_hash": hashOne})
	requireStatus(testInstance, "B opt in", status, http.StatusOK, body)
	status, body = personalRequest(testInstance, appB, http.MethodPost, "/lookup", map[string]any{"person_hash": hashOne})
	requireStatus(testInstance, "B lookup after opt-in", status, http.StatusOK, body)
	decodeBody(testInstance, "B lookup after opt-in", body, &lookup)
	if len(lookup.PersonalDestinations) != 1 || !lookup.PersonalDestinations[0].OptedIn {
		testInstance.Fatalf("B lookup after opt-in = %+v, want opted_in=true", lookup)
	}

	// B opts out again (idempotent shape, the row survives: its registrar is live).
	status, body = personalRequest(testInstance, appB, http.MethodDelete, idPath+"/opt-in", map[string]any{"person_hash": hashOne})
	requireStatus(testInstance, "B opt out", status, http.StatusOK, body)
	var optedOut personalEnvelopeJSON
	decodeBody(testInstance, "B opt out", body, &optedOut)
	if optedOut.PersonalDestination.ID != registered.ID || optedOut.PersonalDestination.OptedIn {
		testInstance.Fatalf("opt-out view = %+v, want the row with opted_in=false", optedOut.PersonalDestination)
	}

	// Person two sees nothing of person one's, and person one's id is a byte-identical 404 for person two.
	status, body = personalRequest(testInstance, appB, http.MethodPost, "/lookup", map[string]any{"email": emailTwo})
	requireStatus(testInstance, "person two lookup", status, http.StatusOK, body)
	decodeBody(testInstance, "person two lookup", body, &lookup)
	if lookup.PersonHash != hashTwo || len(lookup.PersonalDestinations) != 0 {
		testInstance.Fatalf("person two lookup = %+v, want an empty list", lookup)
	}
	foreignStatus, foreignBody := personalRequest(testInstance, appA, http.MethodPatch, idPath, map[string]any{"email": emailTwo, "label": "x"})
	requireStatus(testInstance, "other person's id", foreignStatus, http.StatusNotFound, foreignBody)
	requireErrorMessage(testInstance, "other person's id", foreignBody, personalNotFoundMessage)
	absentStatus, absentBody := personalRequest(testInstance, appA, http.MethodPatch, "/999999999", map[string]any{"email": emailTwo, "label": "x"})
	requireStatus(testInstance, "absent id", absentStatus, http.StatusNotFound, absentBody)
	if string(foreignBody) != string(absentBody) {
		testInstance.Fatalf("other person's id differs from an absent id: %s vs %s", foreignBody, absentBody)
	}

	// Refused input: a private URL (SSRF check) and a ntfy type.
	status, body = personalRequest(testInstance, appA, http.MethodPost, "", map[string]any{
		"email": emailTwo, "label": "Bad", "type": "webhook",
		"secrets": map[string]string{
			"url":            privateWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("r", signingSecretLength))[:signingSecretLength],
		},
	})
	requireStatus(testInstance, "private url", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "private url", body, notPublicMessage)
	status, body = personalRequest(testInstance, appA, http.MethodPost, "", map[string]any{
		"email": emailTwo, "label": "Topic", "type": "ntfy", "secrets": map[string]string{},
	})
	requireStatus(testInstance, "ntfy type", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "ntfy type", body, "type must be discord or webhook")

	// The registering app can still edit and then delete its own.
	status, body = personalRequest(testInstance, appA, http.MethodPatch, idPath, map[string]any{"email": emailOne, "label": "Renamed"})
	requireStatus(testInstance, "A PATCH", status, http.StatusOK, body)
	var patched personalEnvelopeJSON
	decodeBody(testInstance, "A PATCH", body, &patched)
	if patched.PersonalDestination.Label != "Renamed" {
		testInstance.Fatalf("patched label = %q, want Renamed", patched.PersonalDestination.Label)
	}
	status, body = personalRequest(testInstance, appA, http.MethodDelete, idPath, map[string]any{"email": emailOne})
	requireStatus(testInstance, "A DELETE", status, http.StatusOK, body)
	if got := psql(testInstance, fmt.Sprintf("select count(*) from personal_destinations where id = %d", registered.ID)); got != "0" {
		testInstance.Fatalf("rows left after delete = %s, want 0", got)
	}
}

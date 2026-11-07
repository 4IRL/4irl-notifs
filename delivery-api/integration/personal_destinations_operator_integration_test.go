//go:build integration

package integration

// End-to-end test of the operator personal-destination routes (/v1/admin/personal-destinations) against the
// live compose stack: list with the person filter and total, an operator disable that an app cannot undo
// (including after the app first sends its own disable), and the operator delete.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
)

const (
	adminPersonalPath             = "/v1/admin/personal-destinations"
	personalOperatorDisabledError = "personal destination disabled by the operator"
)

type adminPersonalViewJSON struct {
	ID               int64             `json:"id"`
	PersonHash       string            `json:"person_hash"`
	Secrets          map[string]string `json:"secrets"`
	Enabled          bool              `json:"enabled"`
	DisabledBy       *string           `json:"disabled_by"`
	DisabledReason   *string           `json:"disabled_reason"`
	OptedInAppIDs    []string          `json:"opted_in_app_ids"`
	RegistrarRemoved bool              `json:"registrar_removed"`
}

type adminPersonalEnvelopeJSON struct {
	PersonalDestination adminPersonalViewJSON `json:"personal_destination"`
}

type adminPersonalListJSON struct {
	PersonalDestinations []adminPersonalViewJSON `json:"personal_destinations"`
	Total                int                     `json:"total"`
}

func TestOperatorModeratesPersonalDestinations(testInstance *testing.T) {
	waitForReady(testInstance)
	suffix := randomSuffix(testInstance)
	app := personalAppPrefix + "o" + suffix
	email := "it-op-" + suffix + "@example.com"
	hash := personhash.Hash(email)
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("delete from personal_destinations where person_hash = '%s'", hash))
	})

	status, body := personalRequest(testInstance, app, http.MethodPost, "", map[string]any{
		"email": email, "label": "Phone", "type": "webhook",
		"secrets": map[string]string{
			"url":            publicWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("o", signingSecretLength))[:signingSecretLength],
		},
	})
	requireStatus(testInstance, "app registers", status, http.StatusCreated, body)
	var created personalEnvelopeJSON
	decodeBody(testInstance, "created", body, &created)
	idPath := fmt.Sprintf("/%d", created.PersonalDestination.ID)

	// The operator list, filtered to this person: one row, secrets masked, no email anywhere.
	status, body = apiRequest(testInstance, http.MethodGet, adminPersonalPath+"?person_hash="+hash, nil)
	requireStatus(testInstance, "operator list", status, http.StatusOK, body)
	var listed adminPersonalListJSON
	decodeBody(testInstance, "operator list", body, &listed)
	if len(listed.PersonalDestinations) != 1 || listed.Total != 1 || listed.PersonalDestinations[0].ID != created.PersonalDestination.ID ||
		len(listed.PersonalDestinations[0].OptedInAppIDs) != 1 || listed.PersonalDestinations[0].OptedInAppIDs[0] != app {
		testInstance.Fatalf("operator list = %+v, want the one destination opted in for %s", listed, app)
	}
	if strings.Contains(string(body), plaintextPathSecret) || strings.Contains(string(body), email) {
		testInstance.Fatalf("the operator list leaked a secret or the email: %s", body)
	}
	status, body = apiRequest(testInstance, http.MethodGet, adminPersonalPath+"?person_hash=not-a-hash", nil)
	requireStatus(testInstance, "operator list bad filter", status, http.StatusBadRequest, body)

	// Operator disable.
	status, body = apiRequest(testInstance, http.MethodPatch, adminPersonalPath+idPath,
		map[string]any{"enabled": false, "disabled_reason": "abuse"})
	requireStatus(testInstance, "operator disable", status, http.StatusOK, body)
	var disabled adminPersonalEnvelopeJSON
	decodeBody(testInstance, "operator disable", body, &disabled)
	if disabled.PersonalDestination.Enabled || disabled.PersonalDestination.DisabledBy == nil ||
		*disabled.PersonalDestination.DisabledBy != "operator" {
		testInstance.Fatalf("operator disable = %+v, want disabled_by operator", disabled.PersonalDestination)
	}

	// The app disables too: a no-op that answers 200 with the unchanged operator disable.
	status, body = personalRequest(testInstance, app, http.MethodPatch, idPath, map[string]any{"email": email, "enabled": false})
	requireStatus(testInstance, "app disable after operator", status, http.StatusOK, body)
	var afterAppDisable struct {
		PersonalDestination adminPersonalViewJSON `json:"personal_destination"`
	}
	decodeBody(testInstance, "app disable after operator", body, &afterAppDisable)
	if afterAppDisable.PersonalDestination.Enabled || afterAppDisable.PersonalDestination.DisabledBy == nil ||
		*afterAppDisable.PersonalDestination.DisabledBy != "operator" {
		testInstance.Fatalf("app disable view = %+v, want enabled=false", afterAppDisable.PersonalDestination)
	}
	if got := psql(testInstance, fmt.Sprintf("select disabled_by from personal_destinations where id = %d", created.PersonalDestination.ID)); got != "operator" {
		testInstance.Fatalf("disabled_by after the app disable = %q, want operator", got)
	}

	// The app cannot re-enable it.
	status, body = personalRequest(testInstance, app, http.MethodPatch, idPath, map[string]any{"email": email, "enabled": true})
	requireStatus(testInstance, "app enable", status, http.StatusConflict, body)
	requireErrorMessage(testInstance, "app enable", body, personalOperatorDisabledError)

	// The operator can; then delete it, and a repeat delete is absent.
	status, body = apiRequest(testInstance, http.MethodPatch, adminPersonalPath+idPath, map[string]any{"enabled": true})
	requireStatus(testInstance, "operator enable", status, http.StatusOK, body)
	status, body = apiRequest(testInstance, http.MethodDelete, adminPersonalPath+idPath, nil)
	requireStatus(testInstance, "operator delete", status, http.StatusOK, body)
	status, body = apiRequest(testInstance, http.MethodDelete, adminPersonalPath+idPath, nil)
	requireStatus(testInstance, "operator delete again", status, http.StatusNotFound, body)
	status, body = apiRequest(testInstance, http.MethodPatch, adminPersonalPath+"/abc", map[string]any{"enabled": false})
	requireStatus(testInstance, "operator patch non-numeric id", status, http.StatusBadRequest, body)
}

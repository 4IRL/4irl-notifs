//go:build integration

package integration

// End-to-end tests of app identity registrars against the live compose stack with the real
// person-service: a workflow identity is approved once with a reserved prefix, then exchanges bare
// names for per-app dak_ keys. Tokens are signed by devOIDCSigner; each test uses its own random
// prefix (itr_<hex>_) and repository ids, and registers cleanup before creating anything.

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

const (
	registrarTestOwnerID  = "9001"
	registrarAppLimitWire = 50
	registrarPrefixShape  = `^itr_[0-9a-f]{16}_$`
)

var registrarPrefixPattern = regexp.MustCompile(registrarPrefixShape)

type registrarJSON struct {
	ID       int64  `json:"id"`
	Prefix   string `json:"prefix"`
	AppCount int    `json:"app_count"`
	AppLimit int    `json:"app_limit"`
	OwnerID  int64  `json:"github_owner_id"`
}

// registrarRig is one throwaway registrar identity: a unique prefix, repository and repository id.
type registrarRig struct {
	prefix       string
	repository   string
	repositoryID string
	signer       devOIDCSigner
}

// newRegistrarRig registers cleanup first, so a test that fails halfway still removes its rows
// (registrar and its cascaded bindings, pending requests, replay rows) and person-service apps.
func newRegistrarRig(testInstance *testing.T) *registrarRig {
	testInstance.Helper()
	waitForReady(testInstance)
	prefix := "itr_" + randomSuffix(testInstance) + "_"
	if !registrarPrefixPattern.MatchString(prefix) {
		testInstance.Fatalf("generated prefix %q is not the safe test shape", prefix)
	}
	var idBytes [8]byte
	if _, readErr := rand.Read(idBytes[:]); readErr != nil {
		testInstance.Fatalf("reading random bytes: %v", readErr)
	}
	repositoryID := binary.BigEndian.Uint64(idBytes[:])%1_000_000_000_000 + 1
	rig := &registrarRig{
		prefix:       prefix,
		repository:   "4IRL/" + strings.ReplaceAll(strings.TrimSuffix(prefix, "_"), "_", "-"),
		repositoryID: fmt.Sprintf("%d", repositoryID),
		signer:       loadDevOIDCSigner(testInstance),
	}
	testInstance.Cleanup(func() { rig.cleanupRows(testInstance) })
	return rig
}

func (rig *registrarRig) cleanupRows(testInstance *testing.T) {
	if !registrarPrefixPattern.MatchString(rig.prefix) {
		testInstance.Errorf("refusing to clean up a prefix of the wrong shape: %q", rig.prefix)
		return
	}
	psql(testInstance, `DELETE FROM app_bindings WHERE starts_with(app_id, '`+rig.prefix+`')`)
	psql(testInstance, `DELETE FROM app_registrars WHERE prefix = '`+rig.prefix+`'`)
	psql(testInstance, `DELETE FROM app_registration_requests WHERE github_repository_id = `+rig.repositoryID)
	psql(testInstance, `DELETE FROM oidc_replay WHERE jti LIKE '`+identityJTIPrefix+`%'`)
}

func (rig *registrarRig) token(testInstance *testing.T) string {
	testInstance.Helper()
	claims := workflowClaims(rig.repository, rig.repositoryID, identityWorkflow, identityJTIPrefix+randomSuffix(testInstance))
	claims["repository_owner_id"] = registrarTestOwnerID
	return rig.signer.sign(testInstance, claims)
}

// exchangeBody posts an arbitrary exchange body (the token is filled in as "token").
func (rig *registrarRig) exchangeBody(testInstance *testing.T, fields map[string]any) (int, []byte) {
	testInstance.Helper()
	fields["token"] = rig.token(testInstance)
	return apiRequest(testInstance, http.MethodPost, identityExchangePath, fields)
}

// exchangeName posts a bare-name exchange. On 200 it registers removal of the person-service row.
func (rig *registrarRig) exchangeName(testInstance *testing.T, name string) (int, []byte) {
	testInstance.Helper()
	status, body := rig.exchangeBody(testInstance, map[string]any{"name": name})
	if status == http.StatusOK {
		testInstance.Cleanup(func() { deletePersonServiceApp(testInstance, rig.prefix+name) })
	}
	return status, body
}

func (rig *registrarRig) issue(testInstance *testing.T, name string) exchangeResponseJSON {
	testInstance.Helper()
	status, body := rig.exchangeName(testInstance, name)
	requireStatus(testInstance, "registrar exchange "+name, status, http.StatusOK, body)
	var issued exchangeResponseJSON
	decodeBody(testInstance, "registrar exchange "+name, body, &issued)
	if issued.AppID != rig.prefix+name || !identityKeyPattern.MatchString(issued.Key) {
		testInstance.Fatalf("registrar exchange %s answered app_id %q key shape ok=%v, want %q and a dak_ key",
			name, issued.AppID, identityKeyPattern.MatchString(issued.Key), rig.prefix+name)
	}
	return issued
}

// approve files the registrar request with a bare-name exchange, then approves it with prefix.
func (rig *registrarRig) approve(testInstance *testing.T) registrarJSON {
	testInstance.Helper()
	status, body := rig.exchangeName(testInstance, "first")
	requireStatus(testInstance, "registrar request", status, http.StatusAccepted, body)
	var pending exchangeResponseJSON
	decodeBody(testInstance, "registrar request", body, &pending)
	return approveRegistrarRequest(testInstance, pending.RequestID, rig.prefix, http.StatusCreated)
}

func approveRegistrarRequest(testInstance *testing.T, requestID int64, prefix string, wantStatus int) registrarJSON {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s/requests/%d/approve-registrar", identityAdminPath, requestID), map[string]any{"prefix": prefix})
	requireStatus(testInstance, "approve-registrar "+prefix, status, wantStatus, body)
	var approved struct {
		Registrar registrarJSON `json:"registrar"`
	}
	if wantStatus == http.StatusCreated {
		decodeBody(testInstance, "approve-registrar", body, &approved)
	}
	return approved.Registrar
}

func personServiceHasApp(testInstance *testing.T, appID string) bool {
	testInstance.Helper()
	status, body := personServiceRequest(testInstance, http.MethodGet, "/apps")
	requireStatus(testInstance, "list person-service apps", status, http.StatusOK, []byte(body))
	var listed struct {
		Apps []struct {
			AppID string `json:"app_id"`
		} `json:"apps"`
	}
	decodeBody(testInstance, "list person-service apps", []byte(body), &listed)
	for _, row := range listed.Apps {
		if row.AppID == appID {
			return true
		}
	}
	return false
}

// requireKeyWorks asserts the app key authenticates against an app-facing route of its own app.
func requireKeyWorks(testInstance *testing.T, appID string, key string, works bool) {
	testInstance.Helper()
	status, _, body := apiResponse(testInstance, bearer(key), http.MethodGet, "/v1/apps/"+appID+"/routes", nil)
	if works && status != http.StatusOK {
		testInstance.Errorf("key for %s status = %d, want 200 (body: %s)", appID, status, body)
	}
	if !works && status != http.StatusUnauthorized && status != http.StatusForbidden {
		testInstance.Errorf("key for %s status = %d, want 401 or 403 (body: %s)", appID, status, body)
	}
}

func acknowledgeAll(testInstance *testing.T, appIDs []string) int {
	testInstance.Helper()
	status, body := apiRequest(testInstance, http.MethodPost, identityAdminPath+"/bindings/acknowledge-all",
		map[string]any{"app_ids": appIDs})
	requireStatus(testInstance, "acknowledge-all", status, http.StatusOK, body)
	var decoded struct {
		Acknowledged int `json:"acknowledged"`
	}
	decodeBody(testInstance, "acknowledge-all", body, &decoded)
	return decoded.Acknowledged
}

func requireBindingAcknowledged(testInstance *testing.T, appID string, wantAcknowledged bool) *identityBindingJSON {
	testInstance.Helper()
	bindings, _ := listIdentityBindings(testInstance)
	binding := bindingOf(bindings, appID)
	if binding == nil {
		testInstance.Fatalf("binding %s is not listed", appID)
	}
	if binding.Registrar == nil {
		testInstance.Fatalf("binding %s carries no registrar", appID)
	}
	if got := binding.AcknowledgedAt != nil; got != wantAcknowledged {
		testInstance.Errorf("binding %s acknowledged = %v, want %v", appID, got, wantAcknowledged)
	}
	return binding
}

func TestRegistrarLifecycleFromRequestToKeyAndDelete(testInstance *testing.T) {
	rig := newRegistrarRig(testInstance)

	// A bare name from an unknown workflow files a pending registrar request (kind registrar, no app id).
	status, body := rig.exchangeName(testInstance, "first")
	requireStatus(testInstance, "registrar request", status, http.StatusAccepted, body)
	var pending exchangeResponseJSON
	decodeBody(testInstance, "registrar request", body, &pending)
	var filed *identityRequestJSON
	for _, listed := range listIdentityRequests(testInstance) {
		if listed.ID == pending.RequestID {
			request := listed
			filed = &request
		}
	}
	if filed == nil || filed.Kind != "registrar" || filed.AppID != "" || filed.RepositoryID <= 0 {
		testInstance.Fatalf("filed request = %+v, want a registrar request with a null app_id", filed)
	}

	// Plain approve refuses a registrar request; reserved and malformed prefixes are 400 and keep it pending.
	status, body = apiRequest(testInstance, http.MethodPost,
		fmt.Sprintf("%s/requests/%d/approve", identityAdminPath, pending.RequestID), nil)
	requireStatus(testInstance, "plain approve", status, http.StatusConflict, body)
	requireErrorMessage(testInstance, "plain approve", body, "request is a registrar request")
	for _, bad := range []string{"platform_eu_", "platform_", "everyone_", "NoUnderscore", "x"} {
		approveRegistrarRequest(testInstance, pending.RequestID, bad, http.StatusBadRequest)
	}

	registrar := approveRegistrarRequest(testInstance, pending.RequestID, rig.prefix, http.StatusCreated)
	if registrar.Prefix != rig.prefix || registrar.AppCount != 0 || registrar.AppLimit != registrarAppLimitWire || registrar.OwnerID != 9001 {
		testInstance.Fatalf("approved registrar = %+v, want prefix %s, 0 apps, limit %d, owner 9001",
			registrar, rig.prefix, registrarAppLimitWire)
	}
	// The approved request is gone.
	for _, listed := range listIdentityRequests(testInstance) {
		if listed.ID == pending.RequestID {
			testInstance.Errorf("request %d is still listed after approval", pending.RequestID)
		}
	}

	// Exchange again: 200 with app_id = prefix+name, a working key, and a person-service row.
	alpha := rig.issue(testInstance, "alpha")
	alphaID := rig.prefix + "alpha"
	requireKeyWorks(testInstance, alphaID, alpha.Key, true)
	if !personServiceHasApp(testInstance, alphaID) {
		testInstance.Errorf("person-service has no row for %s", alphaID)
	}
	// Re-exchanging the same name issues a fresh key without a second app.
	again := rig.issue(testInstance, "alpha")
	if again.KeyID == alpha.KeyID {
		testInstance.Errorf("second exchange reused key id %d", again.KeyID)
	}
	requireKeyWorks(testInstance, alphaID, again.Key, true)

	// The binding is registrar-created and starts unacknowledged.
	binding := requireBindingAcknowledged(testInstance, alphaID, false)
	if binding.Registrar.Prefix != rig.prefix || binding.Registrar.ID != registrar.ID {
		testInstance.Errorf("binding registrar = %+v, want %d %s", binding.Registrar, registrar.ID, rig.prefix)
	}

	// Acknowledge: explicit, idempotent, keeps the first time.
	ackPath := identityAdminPath + "/bindings/" + alphaID + "/acknowledge"
	status, body = apiRequest(testInstance, http.MethodPost, ackPath, nil)
	requireStatus(testInstance, "acknowledge", status, http.StatusOK, body)
	first := requireBindingAcknowledged(testInstance, alphaID, true).AcknowledgedAt
	status, body = apiRequest(testInstance, http.MethodPost, ackPath, nil)
	requireStatus(testInstance, "acknowledge again", status, http.StatusOK, body)
	if second := requireBindingAcknowledged(testInstance, alphaID, true).AcknowledgedAt; *second != *first {
		testInstance.Errorf("second acknowledge changed the time %s -> %s", *first, *second)
	}
	status, body = apiRequest(testInstance, http.MethodPost, identityAdminPath+"/bindings/"+rig.prefix+"nope/acknowledge", nil)
	requireStatus(testInstance, "acknowledge unknown", status, http.StatusNotFound, body)
	requireErrorMessage(testInstance, "acknowledge unknown", body, "binding does not exist")

	// Acknowledge-all marks only the listed New rows.
	rig.issue(testInstance, "beta")
	rig.issue(testInstance, "gamma")
	betaID := rig.prefix + "beta"
	gammaID := rig.prefix + "gamma"
	if changed := acknowledgeAll(testInstance, []string{alphaID, betaID, betaID, "unknown_" + randomSuffix(testInstance)}); changed != 1 {
		testInstance.Errorf("acknowledge-all changed %d, want 1 (beta; alpha already seen, unknown skipped)", changed)
	}
	if changed := acknowledgeAll(testInstance, []string{alphaID, betaID}); changed != 0 {
		testInstance.Errorf("second acknowledge-all changed %d, want 0", changed)
	}
	requireBindingAcknowledged(testInstance, gammaID, false)
	for _, invalid := range []any{map[string]any{"app_ids": []string{}}, map[string]any{}} {
		status, body = apiRequest(testInstance, http.MethodPost, identityAdminPath+"/bindings/acknowledge-all", invalid)
		requireStatus(testInstance, "acknowledge-all invalid", status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "acknowledge-all invalid", body, "app_ids must list 1 to 200 apps")
	}
	status, body = apiRequest(testInstance, http.MethodPost, identityAdminPath+"/bindings/acknowledge-all",
		map[string]any{"app_ids": []string{"Not Valid"}})
	requireStatus(testInstance, "acknowledge-all bad id", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "acknowledge-all bad id", body, "invalid app_id")

	// Unbind then re-exchange recreates the app, New again.
	status, body = apiRequest(testInstance, http.MethodDelete, identityAdminPath+"/bindings/"+alphaID, nil)
	requireStatus(testInstance, "unbind", status, http.StatusOK, body)
	requireKeyWorks(testInstance, alphaID, again.Key, false)
	recreated := rig.issue(testInstance, "alpha")
	requireKeyWorks(testInstance, alphaID, recreated.Key, true)
	requireBindingAcknowledged(testInstance, alphaID, false)

	// Deprovision (appteardown) on a registrar app removes its binding.
	status, _, body = apiResponse(testInstance, "", http.MethodPost, adminDeprovisionAppPath(betaID), deprovisionAppBody(betaID))
	requireStatus(testInstance, "deprovision-app", status, http.StatusOK, body)
	var deprovisioned deprovisionAppJSON
	decodeBody(testInstance, "deprovision-app", body, &deprovisioned)
	if !deprovisioned.Unbound {
		testInstance.Errorf("deprovision-app answer = %+v, want unbound", deprovisioned)
	}
	if bindings, _ := listIdentityBindings(testInstance); bindingOf(bindings, betaID) != nil {
		testInstance.Errorf("binding %s survived deprovision-app", betaID)
	}

	// Registrar listing counts the remaining apps (alpha, gamma).
	status, body = apiRequest(testInstance, http.MethodGet, identityAdminPath+"/registrars", nil)
	requireStatus(testInstance, "list registrars", status, http.StatusOK, body)
	var listed struct {
		Registrars []registrarJSON `json:"registrars"`
	}
	decodeBody(testInstance, "list registrars", body, &listed)
	var counted *registrarJSON
	for index := range listed.Registrars {
		if listed.Registrars[index].Prefix == rig.prefix {
			counted = &listed.Registrars[index]
		}
	}
	if counted == nil || counted.AppCount != 2 || counted.AppLimit != registrarAppLimitWire {
		testInstance.Fatalf("listed registrar = %+v, want 2 apps and limit %d", counted, registrarAppLimitWire)
	}

	// Deleting the registrar unbinds its apps; their keys stop working. Idempotent.
	deletePath := fmt.Sprintf("%s/registrars/%d", identityAdminPath, registrar.ID)
	status, body = apiRequest(testInstance, http.MethodDelete, deletePath, nil)
	requireStatus(testInstance, "delete registrar", status, http.StatusOK, body)
	var deleted struct {
		Deleted     bool `json:"deleted"`
		UnboundApps int  `json:"unbound_apps"`
	}
	decodeBody(testInstance, "delete registrar", body, &deleted)
	if !deleted.Deleted || deleted.UnboundApps != 2 {
		testInstance.Errorf("delete registrar = %+v, want deleted with 2 unbound apps", deleted)
	}
	requireKeyWorks(testInstance, alphaID, recreated.Key, false)
	status, body = apiRequest(testInstance, http.MethodDelete, deletePath, nil)
	requireStatus(testInstance, "delete registrar again", status, http.StatusOK, body)
	decodeBody(testInstance, "delete registrar again", body, &deleted)
	if deleted.Deleted || deleted.UnboundApps != 0 {
		testInstance.Errorf("second delete = %+v, want deleted=false with 0 unbound", deleted)
	}
	status, body = apiRequest(testInstance, http.MethodDelete, identityAdminPath+"/registrars/abc", nil)
	requireStatus(testInstance, "delete registrar bad id", status, http.StatusBadRequest, body)
	requireErrorMessage(testInstance, "delete registrar bad id", body, "invalid registrar id")
}

func TestRegistrarRefusals(testInstance *testing.T) {
	rig := newRegistrarRig(testInstance)
	rig.approve(testInstance)
	rig.issue(testInstance, "owned")
	ownedID := rig.prefix + "owned"

	// A normal app (bound) asking for an id inside the prefix: 409, nothing filed.
	normal := newIdentityApp(testInstance)
	normal.bind(testInstance)
	for _, inside := range []string{ownedID, rig.prefix + "fresh", strings.TrimSuffix(rig.prefix, "_")} {
		status, body := exchangeToken(testInstance, inside, normal.token(testInstance, nil))
		requireStatus(testInstance, "normal in-prefix "+inside, status, http.StatusConflict, body)
		requireErrorMessage(testInstance, "normal in-prefix", body, "app id is reserved by a registrar")
	}
	// An unbound normal identity gets the same 409 and files no request.
	stranger := newIdentityApp(testInstance)
	status, body := exchangeToken(testInstance, rig.prefix+"stranger", stranger.token(testInstance, nil))
	requireStatus(testInstance, "stranger in-prefix", status, http.StatusConflict, body)
	if count := stranger.pendingRequestCount(testInstance); count != "0" {
		testInstance.Errorf("in-prefix refusal left %s pending requests", count)
	}

	// The registrar's own token sending an in-prefix app_id: 403, not 409.
	status, body = rig.exchangeBody(testInstance, map[string]any{"app_id": ownedID})
	requireStatus(testInstance, "registrar token with app_id", status, http.StatusForbidden, body)
	requireErrorMessage(testInstance, "registrar token with app_id", body, "identity does not match the approved binding")

	// A second registrar cannot reach the first one's id: its name derives into its own prefix, and an
	// app_id naming the first's app is refused as a non-matching identity.
	other := newRegistrarRig(testInstance)
	other.approve(testInstance)
	status, body = other.exchangeBody(testInstance, map[string]any{"app_id": ownedID})
	requireStatus(testInstance, "second registrar with first app_id", status, http.StatusForbidden, body)
	requireErrorMessage(testInstance, "second registrar", body, "identity does not match the approved binding")
	other.issue(testInstance, "owned")
	if !personServiceHasApp(testInstance, other.prefix+"owned") || !personServiceHasApp(testInstance, ownedID) {
		testInstance.Errorf("each registrar should own its own derived app row")
	}
	// A prefix overlapping an approved registrar or app is refused.
	third := newRegistrarRig(testInstance)
	status, body = third.exchangeName(testInstance, "first")
	requireStatus(testInstance, "third request", status, http.StatusAccepted, body)
	var thirdPending exchangeResponseJSON
	decodeBody(testInstance, "third request", body, &thirdPending)
	approveRegistrarRequest(testInstance, thirdPending.RequestID, rig.prefix+"sub_", http.StatusConflict)
	approveRegistrarRequest(testInstance, thirdPending.RequestID, rig.prefix, http.StatusConflict)

	// Both or neither of app_id and name: 400.
	for label, fields := range map[string]map[string]any{
		"both":    {"app_id": ownedID, "name": "owned"},
		"neither": {},
	} {
		status, body = rig.exchangeBody(testInstance, fields)
		requireStatus(testInstance, "exchange "+label, status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "exchange "+label, body, "send exactly one of app_id or name")
	}
	for _, badName := range []string{"Upper", "has-dash", "_lead"} {
		status, body = rig.exchangeBody(testInstance, map[string]any{"name": badName})
		requireStatus(testInstance, "invalid name "+badName, status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "invalid name", body, "invalid name")
	}
}

func TestRegistrarOverlapRefusalCreatesNoPersonServiceRow(testInstance *testing.T) {
	rig := newRegistrarRig(testInstance)
	rig.approve(testInstance)
	rig.issue(testInstance, "ab")

	// hosted_ab and hosted_ab_c cannot both exist (underscore extension).
	status, body := rig.exchangeName(testInstance, "ab_c")
	requireStatus(testInstance, "overlap", status, http.StatusConflict, body)
	requireErrorMessage(testInstance, "overlap", body, "app id overlaps an approved app id")
	if personServiceHasApp(testInstance, rig.prefix+"ab_c") {
		testInstance.Errorf("an overlap refusal created a person-service row")
	}
	if bindings, _ := listIdentityBindings(testInstance); bindingOf(bindings, rig.prefix+"ab_c") != nil {
		testInstance.Errorf("an overlap refusal created a binding")
	}
}

func TestRegistrarAppLimitRefusalCreatesNoPersonServiceRow(testInstance *testing.T) {
	rig := newRegistrarRig(testInstance)
	rig.approve(testInstance)
	// Fill the registrar to its cap with synthetic bindings (no person-service rows), then ask for a
	// 51st name. The registrar's cascade delete in cleanup removes them.
	psql(testInstance, `INSERT INTO app_bindings (app_id, github_repository, github_repository_id, github_owner_id, `+
		`workflow_path, git_ref, environment, approved_at, registrar_id) `+
		`SELECT prefix || 'fill' || g, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, `+
		`environment, now(), id FROM app_registrars, generate_series(1, `+fmt.Sprint(registrarAppLimitWire)+`) AS g `+
		`WHERE prefix = '`+rig.prefix+`'`)

	status, body := rig.exchangeName(testInstance, "overflow")
	requireStatus(testInstance, "app limit", status, http.StatusConflict, body)
	requireErrorMessage(testInstance, "app limit", body, "registrar app limit reached")
	if personServiceHasApp(testInstance, rig.prefix+"overflow") {
		testInstance.Errorf("an app-limit refusal created a person-service row")
	}
	if got := psql(testInstance, `SELECT count(*) FROM app_bindings WHERE app_id = '`+rig.prefix+`overflow'`); got != "0" {
		testInstance.Errorf("an app-limit refusal created %s bindings", got)
	}
}

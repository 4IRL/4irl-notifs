//go:build integration

package integration

// End-to-end tests of the app-scoped route and heartbeat check APIs (/v1/apps/{app_id}/routes and
// /v1/apps/{app_id}/heartbeats) against the live compose stack, with app keys from the dev token exchange
// (app_key_integration_test.go): each app manages only the routes and checks it owns, another owner's object
// (another app's, the operator's, a wildcard route) is indistinguishable from an absent one, a route can
// never match another app's messages, a check can never be registered under another app's id, and a
// destination another app owns can never be linked.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	routeNotFoundMessage      = "route does not exist"
	checkNotFoundMessage      = "check not found"
	patternNotOwnMessage      = "app_pattern must be the app's own id"
	checkAppIDNotOwnMessage   = "app_id must be the app's own id"
	checkImmutableMessage     = "app_id and name are immutable"
	unknownDestinationMessage = "unknown destination"
	invalidJSONBodyMessage    = "invalid JSON body"
	routeExistsMessage        = "route already exists"
	checkExistsMessage        = "check already exists"
	// absentObjectID is an id no serial column reaches in a test run.
	absentObjectID = 999_999_999
)

// onCollection sends one request as app to pathApp's collection ("routes", "heartbeats"), authenticated with
// app's own key.
func (rig appDestinationsRig) onCollection(
	app, pathApp, collection, method, subpath string, body any,
) (int, []byte) {
	rig.testInstance.Helper()
	path := "/v1/apps/" + pathApp + "/" + collection + subpath
	return apiRequestWithAuthorization(rig.testInstance, bearer(appKeyFor(rig.testInstance, app)), method, path, body)
}

// createAppRoute creates a route as app and registers its cleanup. Call it after the destinations it links
// have been created, so (LIFO) the route goes first.
func (rig appDestinationsRig) createAppRoute(app, channel string, destinations ...string) registry.RouteView {
	rig.testInstance.Helper()
	status, body := rig.onCollection(app, app, "routes", http.MethodPost, "", map[string]any{
		"channel_pattern": channel, "destinations": destinations,
	})
	requireStatus(rig.testInstance, "POST route "+channel+" as "+app, status, http.StatusCreated, body)
	var created routeEnvelope
	decodeBody(rig.testInstance, "created route", body, &created)
	cleanupRoute(rig.testInstance, created.Route.ID)
	return created.Route
}

// createOperatorRoute creates an operator-owned route through the admin API and registers its cleanup.
func (rig appDestinationsRig) createOperatorRoute(appPattern, channel string, destinations ...string) registry.RouteView {
	rig.testInstance.Helper()
	status, body := adminRequest(rig.testInstance, http.MethodPost, "/routes", map[string]any{
		"app_pattern": appPattern, "channel_pattern": channel, "destinations": destinations,
	})
	requireStatus(rig.testInstance, "operator POST route "+channel, status, http.StatusCreated, body)
	var created routeEnvelope
	decodeBody(rig.testInstance, "created operator route", body, &created)
	cleanupRoute(rig.testInstance, created.Route.ID)
	return created.Route
}

// createOperatorWebhook creates an operator-owned webhook destination usable by every app.
func (rig appDestinationsRig) createOperatorWebhook() string {
	rig.testInstance.Helper()
	name := "itest_" + randomSuffix(rig.testInstance)
	createDestination(rig.testInstance, name, map[string]any{
		"type": registry.TypeWebhook,
		"secrets": map[string]string{
			"url":            blockedWebhookURL,
			"signing_secret": ("sign" + strings.Repeat("o", signingSecretLength))[:signingSecretLength],
		},
	})
	return name
}

// routeRow is the stored state of route id as one string, for "was not changed" assertions.
func routeRow(testInstance *testing.T, id int64) string {
	testInstance.Helper()
	return psql(testInstance, fmt.Sprintf(
		"SELECT enabled::text || ':' || channel_pattern || ':' || app_pattern || ':' || owner_kind || ':' || coalesce(owner_app_id, '') "+
			"|| ':' || (SELECT coalesce(string_agg(d.name, ',' ORDER BY d.name), '') FROM route_destinations rd "+
			"JOIN destinations d ON d.id = rd.destination_id WHERE rd.route_id = routes.id) FROM routes WHERE id = %d", id))
}

func routePath(id int64) string { return fmt.Sprintf("/%d", id) }

// quotedList renders values as a SQL list body ('a', 'b'). Only values the test generated itself (lowercase
// hex and underscores) may be passed: nothing is escaped.
func quotedList(values ...string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	return strings.Join(quoted, ", ")
}

// sweepRoutesOnChannels registers a cleanup that deletes every route on the given channels. Call it right after
// the destinations are created: cleanups run last-in-first-out, so the per-route cleanups run first, then this
// sweep, then the destinations' own. A refused create that regresses to 201 would otherwise leave a row that
// makes the destination cleanup fail with 409 "in use" and bury the real failure.
func sweepRoutesOnChannels(testInstance *testing.T, channels ...string) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("DELETE FROM routes WHERE channel_pattern IN (%s)", quotedList(channels...)))
	})
}

// sweepChecksOfApps is sweepRoutesOnChannels for the heartbeat checks registered under the given app ids.
func sweepChecksOfApps(testInstance *testing.T, appIDs ...string) {
	testInstance.Helper()
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("DELETE FROM heartbeat_checks WHERE app_id IN (%s)", quotedList(appIDs...)))
	})
}

func TestAppCreatesAnOwnedRouteAndSeesOnlyItsOwn(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	destinationB := rig.appB + "_hook"
	rig.create(rig.appB, destinationB, publicWebhookURL)
	operatorDestination := rig.createOperatorWebhook()
	channel := "it_" + randomSuffix(testInstance)

	route := rig.createAppRoute(rig.appA, channel, destinationA, operatorDestination)

	if route.OwnerKind != registry.OwnerApp || route.OwnerAppID == nil || *route.OwnerAppID != rig.appA {
		testInstance.Fatalf("created owner = %q/%v, want app/%s", route.OwnerKind, route.OwnerAppID, rig.appA)
	}
	if route.AppPattern != rig.appA {
		testInstance.Fatalf("app_pattern = %q, want it defaulted to %s", route.AppPattern, rig.appA)
	}
	if got := psql(testInstance, fmt.Sprintf(
		"SELECT owner_kind || ':' || owner_app_id || ':' || app_pattern FROM routes WHERE id = %d", route.ID)); got != "app:"+rig.appA+":"+rig.appA {
		testInstance.Fatalf("stored owner/pattern = %q, want app:%s:%s: an app-scoped create must never yield an operator-owned "+
			"or wildcard route", got, rig.appA, rig.appA)
	}
	if got := psql(testInstance, fmt.Sprintf("SELECT count(*) FROM routes WHERE channel_pattern = '%s' AND owner_kind <> 'app'", channel)); got != "0" {
		testInstance.Fatalf("operator-owned rows for the app's channel = %s, want 0", got)
	}

	// The operator sees it, as app-owned.
	status, body := adminRequest(testInstance, http.MethodGet, "/routes/"+fmt.Sprint(route.ID), nil)
	requireStatus(testInstance, "operator GET", status, http.StatusOK, body)
	var adminView routeEnvelope
	decodeBody(testInstance, "operator GET", body, &adminView)
	if adminView.Route.OwnerKind != registry.OwnerApp || adminView.Route.OwnerAppID == nil || *adminView.Route.OwnerAppID != rig.appA {
		testInstance.Fatalf("operator view owner = %q/%v, want app/%s", adminView.Route.OwnerKind, adminView.Route.OwnerAppID, rig.appA)
	}

	// Operator routes (one naming the app, one a wildcard) and another app's route never list for the app.
	rig.createOperatorRoute(rig.appA, "op_"+randomSuffix(testInstance), operatorDestination)
	rig.createOperatorRoute("*", "wild_"+randomSuffix(testInstance), operatorDestination)
	rig.createAppRoute(rig.appB, channel, destinationB)
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodGet, "", nil)
	requireStatus(testInstance, "list as "+rig.appA, status, http.StatusOK, body)
	var listed struct {
		Routes []registry.RouteView `json:"routes"`
	}
	decodeBody(testInstance, "list as "+rig.appA, body, &listed)
	if len(listed.Routes) != 1 || listed.Routes[0].ID != route.ID {
		testInstance.Fatalf("list as %s = %s, want exactly route %d", rig.appA, body, route.ID)
	}
	if !strings.Contains(string(body), `"destinations":["`+destinationA+`","`+operatorDestination+`"]`) &&
		!strings.Contains(string(body), `"destinations":["`+operatorDestination+`","`+destinationA+`"]`) {
		testInstance.Fatalf("list as %s = %s, want both linked destinations", rig.appA, body)
	}

	// An app that owns nothing lists an empty array, never null.
	status, body = rig.onCollection(rig.appC, rig.appC, "routes", http.MethodGet, "", nil)
	requireStatus(testInstance, "list as "+rig.appC, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"routes":[]`) {
		testInstance.Fatalf("list as %s = %s, want an empty routes array", rig.appC, body)
	}
}

func TestAnotherOwnersRouteIsNotFoundAndNeverChanged(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	destinationB := rig.appB + "_hook"
	rig.create(rig.appB, destinationB, publicWebhookURL)
	operatorDestination := rig.createOperatorWebhook()
	appRoute := rig.createAppRoute(rig.appA, "it_"+randomSuffix(testInstance), destinationA)
	namedRoute := rig.createOperatorRoute(rig.appA, "opa_"+randomSuffix(testInstance), operatorDestination)
	wildcardRoute := rig.createOperatorRoute("*", "opw_"+randomSuffix(testInstance), operatorDestination)

	type attempt struct {
		label string
		asApp string
		id    int64
	}
	attempts := []attempt{
		{label: "another app's route", asApp: rig.appB, id: appRoute.ID},
		{label: "operator route naming the app", asApp: rig.appA, id: namedRoute.ID},
		{label: "operator wildcard route", asApp: rig.appA, id: wildcardRoute.ID},
		{label: "operator wildcard route", asApp: rig.appB, id: wildcardRoute.ID},
	}
	for _, request := range []struct {
		method string
		body   any
	}{
		{http.MethodGet, nil},
		{http.MethodPatch, map[string]any{"enabled": false}},
		{http.MethodPatch, map[string]any{"channel_pattern": "hijacked", "destinations": []string{destinationB}}},
		{http.MethodDelete, nil},
	} {
		absentStatus, absentBody := rig.onCollection(rig.appB, rig.appB, "routes", request.method, routePath(absentObjectID), request.body)
		rig.requireRefused(request.method+" absent route", absentStatus, absentBody, http.StatusNotFound, routeNotFoundMessage)
		for _, target := range attempts {
			before := routeRow(testInstance, target.id)

			status, body := rig.onCollection(target.asApp, target.asApp, "routes", request.method, routePath(target.id), request.body)

			label := fmt.Sprintf("%s %s as %s", request.method, target.label, target.asApp)
			rig.requireRefused(label, status, body, http.StatusNotFound, routeNotFoundMessage)
			if string(body) != string(absentBody) {
				testInstance.Fatalf("%s: the answer differs from the answer for an absent route: %s vs %s", label, body, absentBody)
			}
			if after := routeRow(testInstance, target.id); after != before {
				testInstance.Fatalf("%s changed the row: %q -> %q", label, before, after)
			}
		}
	}

	// The same ids stay manageable through the operator API (the override): the refusals were ownership, not
	// absence.
	status, body := adminRequest(testInstance, http.MethodGet, "/routes/"+fmt.Sprint(appRoute.ID), nil)
	requireStatus(testInstance, "operator GET of the app's route", status, http.StatusOK, body)
}

func TestAppRoutePatternDestinationAndOwnerRules(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	destinationB := rig.appB + "_hook"
	rig.create(rig.appB, destinationB, publicWebhookURL)
	operatorDestination := rig.createOperatorWebhook()
	channel := "it_" + randomSuffix(testInstance)
	sweepRoutesOnChannels(testInstance, channel, "off_"+channel, "abs_"+channel, "foreign_"+channel, "mixed_"+channel, "own_"+channel)

	// A route's app_pattern is the app's own id: omitted, or equal. A wildcard, another app's id or a glob is a 400
	// and writes nothing.
	for _, pattern := range []string{"*", rig.appB, "itapp_*", strings.ToUpper(rig.appA)} {
		status, body := rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
			"app_pattern": pattern, "channel_pattern": channel, "destinations": []string{destinationA},
		})
		rig.requireRefused("POST with app_pattern "+pattern, status, body, http.StatusBadRequest, patternNotOwnMessage)
	}
	if got := psql(testInstance, fmt.Sprintf("SELECT count(*) FROM routes WHERE channel_pattern = '%s'", channel)); got != "0" {
		testInstance.Fatalf("routes stored by refused creates = %s, want 0", got)
	}
	status, body := rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
		"app_pattern": rig.appA, "channel_pattern": channel, "destinations": []string{destinationA},
	})
	requireStatus(testInstance, "POST with the app's own app_pattern", status, http.StatusCreated, body)
	var created routeEnvelope
	decodeBody(testInstance, "created route", body, &created)
	cleanupRoute(testInstance, created.Route.ID)
	route := created.Route

	// An explicit "enabled": false on create is honoured: the route is stored disabled, never silently live.
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
		"channel_pattern": "off_" + channel, "destinations": []string{destinationA}, "enabled": false,
	})
	requireStatus(testInstance, "POST with enabled false", status, http.StatusCreated, body)
	var createdDisabled routeEnvelope
	decodeBody(testInstance, "created disabled route", body, &createdDisabled)
	cleanupRoute(testInstance, createdDisabled.Route.ID)
	if createdDisabled.Route.Enabled {
		testInstance.Fatalf("a route created with enabled false came back enabled: %s", body)
	}
	if got := psql(testInstance, fmt.Sprintf("SELECT enabled::text FROM routes WHERE id = %d", createdDisabled.Route.ID)); got != "false" {
		testInstance.Fatalf("stored enabled = %q, want false", got)
	}

	// The same holds on PATCH: the pattern can never be widened or moved to another app.
	for _, pattern := range []string{"*", rig.appB, "itapp_*", ""} {
		before := routeRow(testInstance, route.ID)
		status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPatch, routePath(route.ID),
			map[string]any{"app_pattern": pattern})
		rig.requireRefused("PATCH app_pattern "+pattern, status, body, http.StatusBadRequest, patternNotOwnMessage)
		if after := routeRow(testInstance, route.ID); after != before {
			testInstance.Fatalf("PATCH app_pattern %q changed the row: %q -> %q", pattern, before, after)
		}
	}
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPatch, routePath(route.ID),
		map[string]any{"app_pattern": rig.appA, "enabled": false})
	requireStatus(testInstance, "PATCH with the app's own app_pattern", status, http.StatusOK, body)

	// Another app's destination can never be linked, on create or PATCH, and the refusal reads like an absent one.
	absentStatus, absentBody := rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
		"channel_pattern": "abs_" + channel, "destinations": []string{rig.appA + "_absent"},
	})
	rig.requireRefused("POST linking an absent destination", absentStatus, absentBody, http.StatusBadRequest, unknownDestinationMessage)
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
		"channel_pattern": "foreign_" + channel, "destinations": []string{destinationB},
	})
	rig.requireRefused("POST linking another app's destination", status, body, http.StatusBadRequest, unknownDestinationMessage)
	if string(body) != string(absentBody) {
		testInstance.Fatalf("linking another app's destination answers %s, differs from an absent one: %s", body, absentBody)
	}
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
		"channel_pattern": "mixed_" + channel, "destinations": []string{destinationA, destinationB},
	})
	rig.requireRefused("POST linking own and another app's destination", status, body, http.StatusBadRequest, unknownDestinationMessage)
	before := routeRow(testInstance, route.ID)
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPatch, routePath(route.ID),
		map[string]any{"destinations": []string{destinationB}})
	rig.requireRefused("PATCH linking another app's destination", status, body, http.StatusBadRequest, unknownDestinationMessage)
	if after := routeRow(testInstance, route.ID); after != before {
		testInstance.Fatalf("a refused link changed the row: %q -> %q", before, after)
	}
	if got := psql(testInstance, fmt.Sprintf(
		"SELECT count(*) FROM routes WHERE channel_pattern IN ('foreign_%s', 'mixed_%s', 'abs_%s')", channel, channel, channel)); got != "0" {
		testInstance.Fatalf("routes stored by refused links = %s, want 0", got)
	}

	// An operator-owned destination is usable by every app.
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPatch, routePath(route.ID),
		map[string]any{"destinations": []string{operatorDestination}})
	requireStatus(testInstance, "PATCH linking an operator destination", status, http.StatusOK, body)
	var patched routeEnvelope
	decodeBody(testInstance, "patched route", body, &patched)
	if len(patched.Route.Destinations) != 1 || patched.Route.Destinations[0] != operatorDestination || patched.Route.Enabled {
		testInstance.Fatalf("patched route = %+v, want the operator destination and still disabled", patched.Route)
	}

	// A body names no owner and no app_id: both are unknown fields.
	for _, field := range []string{"owner_kind", "owner_app_id", "app_id"} {
		status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
			"channel_pattern": "own_" + channel, "destinations": []string{destinationA}, field: rig.appB,
		})
		rig.requireRefused("POST with "+field, status, body, http.StatusBadRequest, invalidJSONBodyMessage)
	}
	// The same fields on PATCH are unknown too, and change nothing.
	for _, field := range []string{"owner_kind", "owner_app_id", "app_id"} {
		before = routeRow(testInstance, route.ID)
		status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPatch, routePath(route.ID),
			map[string]any{field: rig.appB})
		rig.requireRefused("PATCH with "+field, status, body, http.StatusBadRequest, invalidJSONBodyMessage)
		if after := routeRow(testInstance, route.ID); after != before {
			testInstance.Fatalf("PATCH with %s changed the row: %q -> %q", field, before, after)
		}
	}

	// A duplicate (app_pattern, channel_pattern) is a conflict.
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodPost, "", map[string]any{
		"channel_pattern": channel, "destinations": []string{destinationA},
	})
	rig.requireRefused("POST a duplicate route", status, body, http.StatusConflict, routeExistsMessage)

	// Lifecycle: read, delete (not idempotent: the second delete is a 404), gone.
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodGet, routePath(route.ID), nil)
	requireStatus(testInstance, "GET own route", status, http.StatusOK, body)
	status, body = rig.onCollection(rig.appB, rig.appB, "routes", http.MethodDelete, routePath(route.ID), nil)
	rig.requireRefused("DELETE as another app", status, body, http.StatusNotFound, routeNotFoundMessage)
	if got := psql(testInstance, fmt.Sprintf("SELECT count(*) FROM routes WHERE id = %d", route.ID)); got != "1" {
		testInstance.Fatalf("another app's DELETE removed the route")
	}
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodDelete, routePath(route.ID), nil)
	requireStatus(testInstance, "DELETE own route", status, http.StatusOK, body)
	if string(body) != fmt.Sprintf(`{"id":%d,"deleted":true}`+"\n", route.ID) {
		testInstance.Fatalf("DELETE body = %s", body)
	}
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodDelete, routePath(route.ID), nil)
	rig.requireRefused("second DELETE", status, body, http.StatusNotFound, routeNotFoundMessage)
	status, body = rig.onCollection(rig.appA, rig.appA, "routes", http.MethodGet, routePath(route.ID), nil)
	rig.requireRefused("GET after DELETE", status, body, http.StatusNotFound, routeNotFoundMessage)
}

// appCheckJSON is the check view with its owner.
type appCheckJSON struct {
	checkJSON
	OwnerKind  string  `json:"owner_kind"`
	OwnerAppID *string `json:"owner_app_id"`
}

func appCheckBody(name string, destinations ...string) map[string]any {
	return map[string]any{
		"name": name, "period_seconds": 3600, "grace_seconds": 3600, "destinations": destinations,
	}
}

// createAppCheck creates a check as app and registers its cleanup. Call it after the destinations it links
// have been created, so (LIFO) the check goes first.
func (rig appDestinationsRig) createAppCheck(app, name string, destinations ...string) appCheckJSON {
	rig.testInstance.Helper()
	status, body := rig.onCollection(app, app, "heartbeats", http.MethodPost, "", appCheckBody(name, destinations...))
	requireStatus(rig.testInstance, "POST check "+name+" as "+app, status, http.StatusCreated, body)
	var created struct {
		Check appCheckJSON `json:"check"`
	}
	decodeBody(rig.testInstance, "created check", body, &created)
	cleanupHeartbeatCheck(rig.testInstance, created.Check.ID)
	return created.Check
}

// createOperatorCheck creates an operator-owned check through the admin API and registers its cleanup.
func (rig appDestinationsRig) createOperatorCheck(appID, name string, destinations ...string) appCheckJSON {
	rig.testInstance.Helper()
	body := appCheckBody(name, destinations...)
	body["app_id"] = appID
	status, response := adminRequest(rig.testInstance, http.MethodPost, heartbeatsAdminPath, body)
	requireStatus(rig.testInstance, "operator POST check "+name, status, http.StatusCreated, response)
	var created struct {
		Check appCheckJSON `json:"check"`
	}
	decodeBody(rig.testInstance, "created operator check", response, &created)
	cleanupHeartbeatCheck(rig.testInstance, created.Check.ID)
	return created.Check
}

// checkRow is the stored state of check id as one string, for "was not changed" assertions.
func checkRow(testInstance *testing.T, id int64) string {
	testInstance.Helper()
	return psql(testInstance, fmt.Sprintf(
		"SELECT paused::text || ':' || period_seconds || ':' || grace_seconds || ':' || app_id || ':' || name || ':' || owner_kind "+
			"|| ':' || coalesce(owner_app_id, '') || ':' || (SELECT coalesce(string_agg(d.name, ',' ORDER BY d.name), '') "+
			"FROM heartbeat_check_destinations cd JOIN destinations d ON d.id = cd.destination_id WHERE cd.check_id = heartbeat_checks.id) "+
			"FROM heartbeat_checks WHERE id = %d", id))
}

func TestAppCreatesAnOwnedCheckAndSeesOnlyItsOwn(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	destinationB := rig.appB + "_hook"
	rig.create(rig.appB, destinationB, publicWebhookURL)
	operatorDestination := rig.createOperatorWebhook()

	check := rig.createAppCheck(rig.appA, "nightly", destinationA, operatorDestination)

	if check.OwnerKind != registry.OwnerApp || check.OwnerAppID == nil || *check.OwnerAppID != rig.appA || check.AppID != rig.appA {
		testInstance.Fatalf("created check = %+v, want an app-owned check under %s", check, rig.appA)
	}
	if got := psql(testInstance, fmt.Sprintf(
		"SELECT owner_kind || ':' || owner_app_id || ':' || app_id FROM heartbeat_checks WHERE id = %d", check.ID)); got != "app:"+rig.appA+":"+rig.appA {
		testInstance.Fatalf("stored owner/app_id = %q, want app:%s:%s: an app-scoped create must never yield an operator-owned check",
			got, rig.appA, rig.appA)
	}

	// The operator sees it, as app-owned.
	status, body := adminRequest(testInstance, http.MethodGet, fmt.Sprintf("%s/%d", heartbeatsAdminPath, check.ID), nil)
	requireStatus(testInstance, "operator GET check", status, http.StatusOK, body)
	var adminView struct {
		Check appCheckJSON `json:"check"`
	}
	decodeBody(testInstance, "operator GET check", body, &adminView)
	if adminView.Check.OwnerKind != registry.OwnerApp || adminView.Check.OwnerAppID == nil || *adminView.Check.OwnerAppID != rig.appA {
		testInstance.Fatalf("operator view owner = %q/%v, want app/%s", adminView.Check.OwnerKind, adminView.Check.OwnerAppID, rig.appA)
	}

	// The operator's check under the same app_id, and another app's check, never list for the app.
	rig.createOperatorCheck(rig.appA, "op_nightly", operatorDestination)
	rig.createAppCheck(rig.appB, "nightly", destinationB)
	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodGet, "", nil)
	requireStatus(testInstance, "list as "+rig.appA, status, http.StatusOK, body)
	var listed struct {
		Checks []appCheckJSON `json:"checks"`
	}
	decodeBody(testInstance, "list as "+rig.appA, body, &listed)
	if len(listed.Checks) != 1 || listed.Checks[0].ID != check.ID {
		testInstance.Fatalf("list as %s = %s, want exactly check %d", rig.appA, body, check.ID)
	}
	status, body = rig.onCollection(rig.appC, rig.appC, "heartbeats", http.MethodGet, "", nil)
	requireStatus(testInstance, "list as "+rig.appC, status, http.StatusOK, body)
	if !strings.Contains(string(body), `"checks":[]`) {
		testInstance.Fatalf("list as %s = %s, want an empty checks array", rig.appC, body)
	}

	// Pings keep working on the app's own check, through the ping endpoint and the app's key.
	ping := postPing(testInstance, rig.appA, "nightly", "")
	if ping.AppID != rig.appA || ping.State != "up" {
		testInstance.Fatalf("ping = %+v, want the app's check up", ping)
	}
}

func TestAppCheckAppIDMustBeTheCallersOwn(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	sweepChecksOfApps(testInstance, rig.appA, rig.appB)

	for _, appID := range []string{rig.appB, "*", strings.ToUpper(rig.appA)} {
		body := appCheckBody("nightly", destinationA)
		body["app_id"] = appID
		status, response := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, "", body)
		rig.requireRefused("POST with app_id "+appID, status, response, http.StatusBadRequest, checkAppIDNotOwnMessage)
	}
	if got := psql(testInstance, fmt.Sprintf(
		"SELECT count(*) FROM heartbeat_checks WHERE app_id IN ('%s', '%s')", rig.appA, rig.appB)); got != "0" {
		testInstance.Fatalf("checks stored by refused creates = %s, want 0", got)
	}
	// Omitted, empty and the app's own id are all accepted.
	for index, appID := range []any{nil, "", rig.appA} {
		body := appCheckBody(fmt.Sprintf("accepted_%d", index), destinationA)
		if appID != nil {
			body["app_id"] = appID
		}
		status, response := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, "", body)
		requireStatus(testInstance, fmt.Sprintf("POST with app_id %v", appID), status, http.StatusCreated, response)
		var created struct {
			Check appCheckJSON `json:"check"`
		}
		decodeBody(testInstance, "created check", response, &created)
		cleanupHeartbeatCheck(testInstance, created.Check.ID)
		if created.Check.AppID != rig.appA {
			testInstance.Fatalf("app_id = %q, want %s", created.Check.AppID, rig.appA)
		}
	}

	// The same holds on PATCH; the name stays immutable.
	check := rig.createAppCheck(rig.appA, "nightly", destinationA)
	for _, body := range []map[string]any{
		{"app_id": rig.appB, "period_seconds": 7200},
		{"app_id": "*"},
		{"app_id": 5},
	} {
		before := checkRow(testInstance, check.ID)
		status, response := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPatch, routePath(check.ID), body)
		rig.requireRefused(fmt.Sprintf("PATCH %v", body), status, response, http.StatusBadRequest, checkAppIDNotOwnMessage)
		if after := checkRow(testInstance, check.ID); after != before {
			testInstance.Fatalf("PATCH %v changed the row: %q -> %q", body, before, after)
		}
	}
	status, response := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPatch, routePath(check.ID),
		map[string]any{"name": "renamed"})
	rig.requireRefused("PATCH name", status, response, http.StatusBadRequest, checkImmutableMessage)
	status, response = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPatch, routePath(check.ID),
		map[string]any{"app_id": rig.appA, "period_seconds": 7200})
	requireStatus(testInstance, "PATCH with the app's own app_id", status, http.StatusOK, response)

	// Neither a body owner nor a path-mismatched key can reach another app's namespace.
	for _, field := range []string{"owner_kind", "owner_app_id"} {
		body := appCheckBody("owned", destinationA)
		body[field] = rig.appB
		status, response = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, "", body)
		rig.requireRefused("POST with "+field, status, response, http.StatusBadRequest, invalidJSONBodyMessage)
	}
}

func TestAppCheckDestinationRules(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	destinationB := rig.appB + "_hook"
	rig.create(rig.appB, destinationB, publicWebhookURL)
	operatorDestination := rig.createOperatorWebhook()
	sweepChecksOfApps(testInstance, rig.appA)

	// Another app's destination can never be linked, on create or PATCH; the refusal reads like an absent one.
	absentStatus, absentBody := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, "",
		appCheckBody("absent", rig.appA+"_absent"))
	rig.requireRefused("POST linking an absent destination", absentStatus, absentBody, http.StatusBadRequest, unknownDestinationMessage)
	for label, destinations := range map[string][]string{
		"another app's":            {destinationB},
		"own and another app":      {destinationA, destinationB},
		"operator and another app": {operatorDestination, destinationB},
	} {
		status, body := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, "", appCheckBody("foreign", destinations...))
		rig.requireRefused("POST linking "+label, status, body, http.StatusBadRequest, unknownDestinationMessage)
		if string(body) != string(absentBody) {
			testInstance.Fatalf("POST linking %s answers %s, differs from an absent destination: %s", label, body, absentBody)
		}
	}
	if got := psql(testInstance, fmt.Sprintf("SELECT count(*) FROM heartbeat_checks WHERE app_id = '%s'", rig.appA)); got != "0" {
		testInstance.Fatalf("checks stored by refused links = %s, want 0", got)
	}

	// Own and operator destinations are usable.
	own := rig.createAppCheck(rig.appA, "own", destinationA)
	operator := rig.createAppCheck(rig.appA, "operator", operatorDestination)
	if len(own.Destinations) != 1 || own.Destinations[0].Name != destinationA ||
		len(operator.Destinations) != 1 || operator.Destinations[0].Name != operatorDestination {
		testInstance.Fatalf("destinations = %+v / %+v", own.Destinations, operator.Destinations)
	}

	before := checkRow(testInstance, own.ID)
	status, body := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPatch, routePath(own.ID),
		map[string]any{"destinations": []string{destinationB}})
	rig.requireRefused("PATCH linking another app's destination", status, body, http.StatusBadRequest, unknownDestinationMessage)
	if after := checkRow(testInstance, own.ID); after != before {
		testInstance.Fatalf("a refused link changed the row: %q -> %q", before, after)
	}
	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPatch, routePath(own.ID),
		map[string]any{"destinations": []string{operatorDestination, destinationA}})
	requireStatus(testInstance, "PATCH linking operator and own destinations", status, http.StatusOK, body)

	// A duplicate (app_id, name) is a conflict.
	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, "", appCheckBody("own", destinationA))
	rig.requireRefused("POST a duplicate check", status, body, http.StatusConflict, checkExistsMessage)
}

func TestAnotherOwnersCheckIsNotFoundAndNeverChanged(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	destinationB := rig.appB + "_hook"
	rig.create(rig.appB, destinationB, publicWebhookURL)
	operatorDestination := rig.createOperatorWebhook()
	appCheck := rig.createAppCheck(rig.appA, "nightly", destinationA)
	operatorCheck := rig.createOperatorCheck(rig.appA, "op_nightly", operatorDestination)

	type attempt struct {
		label string
		asApp string
		id    int64
	}
	attempts := []attempt{
		{label: "another app's check", asApp: rig.appB, id: appCheck.ID},
		{label: "operator check under the app's id", asApp: rig.appA, id: operatorCheck.ID},
	}
	for _, request := range []struct {
		name   string
		method string
		suffix string
		body   any
	}{
		{"get", http.MethodGet, "", nil},
		{"patch", http.MethodPatch, "", map[string]any{"period_seconds": 7200}},
		{"patch destinations", http.MethodPatch, "", map[string]any{"destinations": []string{destinationB}}},
		{"empty patch", http.MethodPatch, "", map[string]any{}},
		{"pause", http.MethodPost, "/pause", nil},
		{"resume", http.MethodPost, "/resume", nil},
		{"delete", http.MethodDelete, "", nil},
	} {
		absentStatus, absentBody := rig.onCollection(rig.appB, rig.appB, "heartbeats", request.method,
			routePath(absentObjectID)+request.suffix, request.body)
		rig.requireRefused(request.name+" absent check", absentStatus, absentBody, http.StatusNotFound, checkNotFoundMessage)
		for _, target := range attempts {
			before := checkRow(testInstance, target.id)

			status, body := rig.onCollection(target.asApp, target.asApp, "heartbeats", request.method,
				routePath(target.id)+request.suffix, request.body)

			label := fmt.Sprintf("%s %s as %s", request.name, target.label, target.asApp)
			rig.requireRefused(label, status, body, http.StatusNotFound, checkNotFoundMessage)
			if string(body) != string(absentBody) {
				testInstance.Fatalf("%s: the answer differs from the answer for an absent check: %s vs %s", label, body, absentBody)
			}
			if after := checkRow(testInstance, target.id); after != before {
				testInstance.Fatalf("%s changed the row: %q -> %q", label, before, after)
			}
		}
	}
}

func TestAppCheckLifecycle(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	destinationA := rig.appA + "_hook"
	rig.create(rig.appA, destinationA, publicWebhookURL)
	check := rig.createAppCheck(rig.appA, "nightly", destinationA)
	path := routePath(check.ID)

	status, body := rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodGet, path, nil)
	requireStatus(testInstance, "GET own check", status, http.StatusOK, body)

	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPatch, path, map[string]any{"period_seconds": 7200})
	requireStatus(testInstance, "PATCH own check", status, http.StatusOK, body)
	var patched struct {
		Check appCheckJSON `json:"check"`
	}
	decodeBody(testInstance, "patched check", body, &patched)
	if patched.Check.PeriodSeconds != 7200 || patched.Check.GraceSeconds != 3600 || patched.Check.OwnerKind != registry.OwnerApp {
		testInstance.Fatalf("patched check = %+v, want the period changed and everything else kept", patched.Check)
	}

	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, path+"/pause", nil)
	requireStatus(testInstance, "pause own check", status, http.StatusOK, body)
	if got := psql(testInstance, fmt.Sprintf("SELECT paused::text FROM heartbeat_checks WHERE id = %d", check.ID)); got != "true" {
		testInstance.Fatalf("paused = %s after pause, want true", got)
	}
	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodPost, path+"/resume", nil)
	requireStatus(testInstance, "resume own check", status, http.StatusOK, body)
	if got := psql(testInstance, fmt.Sprintf("SELECT paused::text FROM heartbeat_checks WHERE id = %d", check.ID)); got != "false" {
		testInstance.Fatalf("paused = %s after resume, want false", got)
	}

	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodDelete, path, nil)
	requireStatus(testInstance, "DELETE own check", status, http.StatusOK, body)
	if string(body) != fmt.Sprintf(`{"id":%d,"deleted":true}`+"\n", check.ID) {
		testInstance.Fatalf("DELETE body = %s", body)
	}
	status, body = rig.onCollection(rig.appA, rig.appA, "heartbeats", http.MethodDelete, path, nil)
	rig.requireRefused("second DELETE", status, body, http.StatusNotFound, checkNotFoundMessage)
}

// Every route and heartbeat endpoint takes the app key, and the key's app must be the one in the path.
func TestAppRouteAndHeartbeatEndpointsRequireTheAppKeyOfThePathsApp(testInstance *testing.T) {
	rig := newAppDestinationsRig(testInstance)
	endpoints := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "routes", map[string]any{"channel_pattern": "x", "destinations": []string{"d"}}},
		{http.MethodGet, "routes", nil},
		{http.MethodGet, "routes/1", nil},
		{http.MethodPatch, "routes/1", map[string]any{"enabled": false}},
		{http.MethodDelete, "routes/1", nil},
		{http.MethodPost, "heartbeats", appCheckBody("nightly", "d")},
		{http.MethodGet, "heartbeats", nil},
		{http.MethodGet, "heartbeats/1", nil},
		{http.MethodPatch, "heartbeats/1", map[string]any{"period_seconds": 7200}},
		{http.MethodDelete, "heartbeats/1", nil},
		{http.MethodPost, "heartbeats/1/pause", nil},
		{http.MethodPost, "heartbeats/1/resume", nil},
	}
	for _, endpoint := range endpoints {
		path := "/v1/apps/" + rig.appA + "/" + endpoint.path
		label := endpoint.method + " " + path
		for _, authorization := range []string{"", "Bearer", "Bearer not-a-delivery-key"} {
			status, body := apiRequestWithAuthorization(testInstance, authorization, endpoint.method, path, endpoint.body)
			rig.requireRefused(label+" with header "+authorization, status, body, http.StatusUnauthorized, missingKeyBody)
		}
		status, body := apiRequestWithAuthorization(testInstance, bearer("dak_"+strings.Repeat("A", 43)), endpoint.method, path, endpoint.body)
		rig.requireRefused(label+" with an unknown key", status, body, http.StatusUnauthorized, invalidKeyBody)

		// App B's valid key on app A's path is refused as a mismatch, before anything is read or written.
		status, body = apiRequestWithAuthorization(testInstance, bearer(appKeyFor(testInstance, rig.appB)), endpoint.method, path, endpoint.body)
		rig.requireRefused(label+" with another app's key", status, body, http.StatusForbidden, "app_id does not match app key")
	}
}

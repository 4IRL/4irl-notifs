package registry

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// createAppRoute creates a route for appID on channel linking destinations, failing the test on an error.
func (fixture serviceFixture) createAppRoute(
	testInstance *testing.T, appID, channel string, destinations ...string,
) RouteView {
	testInstance.Helper()
	view, err := fixture.service.CreateAppRoute(context.Background(), appID, RouteInput{
		ChannelPattern: channel, Destinations: destinations,
	})
	if err != nil {
		testInstance.Fatalf("create app route for %q: %v", appID, err)
	}
	return view
}

func TestCreateAppRouteIsOwnedByTheCallingAppAndMatchesOnlyItsMessages(testInstance *testing.T) {
	for _, input := range []RouteInput{
		{ChannelPattern: "backup_*", Destinations: []string{"app_a_hook"}},
		{AppPattern: appA, ChannelPattern: "backup_*", Destinations: []string{"app_a_hook"}},
	} {
		fixture, _ := newAppFixture(testInstance)
		fixture.createAppWebhook(testInstance, appA, "app_a_hook")

		view, err := fixture.service.CreateAppRoute(context.Background(), appA, input)
		if err != nil {
			testInstance.Fatalf("CreateAppRoute(%+v): %v", input, err)
		}

		// The row handed to the store names the owner explicitly: the store treats an empty owner kind
		// as the operator, so anything short of 'app' here would be a fail-open write.
		stored := fixture.store.routes[view.ID]
		if stored.OwnerKind != OwnerApp {
			testInstance.Fatalf("stored owner kind = %q, want %q: an app-scoped create must never yield an operator-owned route",
				stored.OwnerKind, OwnerApp)
		}
		if stored.OwnerAppID == nil || *stored.OwnerAppID != appA {
			testInstance.Fatalf("stored owner app id = %v, want %q", stored.OwnerAppID, appA)
		}
		if stored.AppPattern != appA {
			testInstance.Fatalf("stored app_pattern = %q, want %q (an omitted pattern defaults to the app)", stored.AppPattern, appA)
		}
		if view.OwnerKind != OwnerApp || view.OwnerAppID == nil || *view.OwnerAppID != appA || view.AppPattern != appA {
			testInstance.Fatalf("view = %+v, want an app-owned route for %q", view, appA)
		}
		if !reflect.DeepEqual(view.Destinations, []string{"app_a_hook"}) || !view.Enabled {
			testInstance.Fatalf("view = %+v, want the linked destination and enabled by default", view)
		}
	}
}

func TestCreateAppRouteRefusesAnyPatternButTheAppsOwnWithoutTouchingTheStore(testInstance *testing.T) {
	for _, pattern := range []string{"*", appB, "app_*", "app_a*", "APP_A", " app_a", "app_a "} {
		fixture, _ := newAppFixture(testInstance)
		fixture.createAppWebhook(testInstance, appA, "app_a_hook")
		fixture.store.calls = nil

		_, err := fixture.service.CreateAppRoute(context.Background(), appA, RouteInput{
			AppPattern: pattern, ChannelPattern: "x", Destinations: []string{"app_a_hook"},
		})

		requireValidationError(testInstance, "pattern "+pattern, err, "app_pattern must be the app's own id")
		requireCalls(testInstance, "pattern "+pattern, fixture.store)
		if len(fixture.store.routes) != 0 {
			testInstance.Fatalf("pattern %q: a route was stored", pattern)
		}
	}
}

func TestCreateAppRouteHonoursAnExplicitEnabledFalse(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	disabled := false

	view, err := fixture.service.CreateAppRoute(context.Background(), appA, RouteInput{
		ChannelPattern: "x", Destinations: []string{"app_a_hook"}, Enabled: &disabled,
	})

	if err != nil {
		testInstance.Fatalf("CreateAppRoute: %v", err)
	}
	if view.Enabled || fixture.store.routes[view.ID].Enabled {
		testInstance.Fatalf("view enabled = %v, stored enabled = %v, want a disabled route: an app asking for one must not get a live route",
			view.Enabled, fixture.store.routes[view.ID].Enabled)
	}
}

// The reserved app id "everyone" is refused as an app_pattern on the admin path; an app whose id is that value
// must not get around the rule (the database CHECK does not enforce it).
func TestCreateAppRouteRefusesTheReservedEveryoneAppID(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.store.calls = nil

	_, err := fixture.service.CreateAppRoute(context.Background(), "everyone", RouteInput{
		ChannelPattern: "x", Destinations: []string{"app_a_hook"},
	})

	requireValidationError(testInstance, "reserved id", err, "invalid app_pattern")
	requireCalls(testInstance, "reserved id", fixture.store)
	if len(fixture.store.routes) != 0 {
		testInstance.Fatalf("a route was stored for the reserved id")
	}
}

func TestCreateAppRouteValidatesTheOtherFieldsAndTheCallerID(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	fixture.store.calls = nil

	_, channelErr := fixture.service.CreateAppRoute(context.Background(), appA, RouteInput{
		ChannelPattern: "bad channel!", Destinations: []string{"app_a_hook"},
	})
	_, destinationErr := fixture.service.CreateAppRoute(context.Background(), appA, RouteInput{ChannelPattern: "x"})
	_, appIDErr := fixture.service.CreateAppRoute(context.Background(), "*", RouteInput{
		ChannelPattern: "x", Destinations: []string{"app_a_hook"},
	})

	for label, err := range map[string]error{"channel": channelErr, "destinations": destinationErr, "caller": appIDErr} {
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) {
			testInstance.Fatalf("%s: err = %v, want a validation error", label, err)
		}
	}
	requireCalls(testInstance, "invalid fields", fixture.store)
}

func TestAppRouteMayLinkOperatorAndOwnDestinationsButNeverAnotherApps(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createWebhook(testInstance, "ops_hook")
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")

	if _, err := fixture.service.CreateAppRoute(context.Background(), appA, RouteInput{
		ChannelPattern: "own", Destinations: []string{"app_a_hook", "ops_hook"},
	}); err != nil {
		testInstance.Fatalf("own and operator destinations: %v", err)
	}
	for label, destinations := range map[string][]string{
		"another app's":        {"app_b_hook"},
		"another app's + own":  {"app_a_hook", "app_b_hook"},
		"another app's + ops":  {"ops_hook", "app_b_hook"},
		"absent (same answer)": {"app_a_absent"},
	} {
		_, err := fixture.service.CreateAppRoute(context.Background(), appA, RouteInput{
			ChannelPattern: "foreign", Destinations: destinations,
		})
		if !errors.Is(err, ErrUnknownDestination) {
			testInstance.Fatalf("%s: err = %v, want ErrUnknownDestination", label, err)
		}
	}
	if len(fixture.store.routes) != 1 {
		testInstance.Fatalf("stored routes = %d, want only the one valid create", len(fixture.store.routes))
	}
}

func TestListAppRoutesReturnsOnlyTheCallersOwn(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createWebhook(testInstance, "ops_hook")
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")
	if _, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: appA, ChannelPattern: "operator_made", Destinations: []string{"ops_hook"},
	}); err != nil {
		testInstance.Fatalf("operator route: %v", err)
	}
	first := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")
	second := fixture.createAppRoute(testInstance, appA, "two", "ops_hook")
	fixture.createAppRoute(testInstance, appB, "one", "app_b_hook")

	views, err := fixture.service.ListAppRoutes(context.Background(), appA)

	if err != nil {
		testInstance.Fatalf("ListAppRoutes: %v", err)
	}
	if len(views) != 2 || views[0].ID != first.ID || views[1].ID != second.ID {
		testInstance.Fatalf("listed %+v, want exactly app A's routes %d and %d (not the operator's, not app B's)", views, first.ID, second.ID)
	}
	empty, err := fixture.service.ListAppRoutes(context.Background(), "app_c")
	if err != nil || empty == nil || len(empty) != 0 {
		testInstance.Fatalf("an app with no routes lists %#v, %v, want an empty non-nil slice", empty, err)
	}
}

// ownerBlindRouteStore returns every route from ListRoutesByOwner, as a store that forgot its owner filter.
type ownerBlindRouteStore struct{ *fakeStore }

func (store *ownerBlindRouteStore) ListRoutesByOwner(ctx context.Context, _ string) ([]StoredRoute, error) {
	return store.ListRoutes(ctx)
}

func TestListAppRoutesDropsARowTheStoreReturnedForAnotherOwner(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createWebhook(testInstance, "ops_hook")
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")
	if _, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: "*", ChannelPattern: "operator_made", Destinations: []string{"ops_hook"},
	}); err != nil {
		testInstance.Fatalf("operator route: %v", err)
	}
	fixture.createAppRoute(testInstance, appB, "one", "app_b_hook")
	service := NewService(&ownerBlindRouteStore{fakeStore: fixture.store}, fixture.sealer, nil)

	views, err := service.ListAppRoutes(context.Background(), appA)

	if err != nil || len(views) != 0 {
		testInstance.Fatalf("listed %+v, %v; want nothing: no row is app A's", views, err)
	}
}

// A route the app does not own is indistinguishable from an absent one on every endpoint, and is never changed.
func TestAppRouteAccessToAnythingNotOwnedIsNotFound(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createWebhook(testInstance, "ops_hook")
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")
	operatorRoute, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: appA, ChannelPattern: "operator_made", Destinations: []string{"ops_hook"},
	})
	if err != nil {
		testInstance.Fatalf("operator route: %v", err)
	}
	wildcardRoute, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: "*", ChannelPattern: "everything", Destinations: []string{"ops_hook"},
	})
	if err != nil {
		testInstance.Fatalf("wildcard route: %v", err)
	}
	foreignRoute := fixture.createAppRoute(testInstance, appB, "theirs", "app_b_hook")
	const absentID = 9999
	enabled := false

	for label, id := range map[string]int64{
		"operator route with the app's pattern": operatorRoute.ID,
		"operator wildcard route":               wildcardRoute.ID,
		"another app's route":                   foreignRoute.ID,
		"absent route":                          absentID,
	} {
		before := fixture.store.routes[id]
		ctx := context.Background()
		_, getErr := fixture.service.GetAppRoute(ctx, appA, id)
		_, updateErr := fixture.service.UpdateAppRoute(ctx, appA, id, RoutePatch{Enabled: &enabled})
		deleteErr := fixture.service.DeleteAppRoute(ctx, appA, id)

		for op, err := range map[string]error{"get": getErr, "update": updateErr, "delete": deleteErr} {
			if !errors.Is(err, ErrRouteNotFound) {
				testInstance.Fatalf("%s %s: err = %v, want ErrRouteNotFound", op, label, err)
			}
		}
		if id != absentID && !reflect.DeepEqual(fixture.store.routes[id], before) {
			testInstance.Fatalf("%s: the row was changed or deleted by a non-owner", label)
		}
	}
}

func TestUpdateAppRouteChangesOnlyTheNamedFieldsAndNeverTheOwner(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createWebhook(testInstance, "ops_hook")
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	route := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")
	channel, own, disabled := "two", appA, false

	view, err := fixture.service.UpdateAppRoute(context.Background(), appA, route.ID, RoutePatch{
		AppPattern: &own, ChannelPattern: &channel, Destinations: []string{"ops_hook"}, Enabled: &disabled,
	})

	if err != nil {
		testInstance.Fatalf("UpdateAppRoute: %v", err)
	}
	if view.ChannelPattern != "two" || view.Enabled || !reflect.DeepEqual(view.Destinations, []string{"ops_hook"}) ||
		view.AppPattern != appA || view.OwnerKind != OwnerApp || view.OwnerAppID == nil || *view.OwnerAppID != appA {
		testInstance.Fatalf("view = %+v, want the patched fields and the unchanged owner and pattern", view)
	}
	kept, err := fixture.service.UpdateAppRoute(context.Background(), appA, route.ID, RoutePatch{})
	if err != nil || kept.ChannelPattern != "two" || kept.AppPattern != appA {
		testInstance.Fatalf("an empty patch = %+v, %v, want the route unchanged", kept, err)
	}
}

func TestUpdateAppRouteRefusesAnyPatternButTheAppsOwnBeforeTheStore(testInstance *testing.T) {
	for _, pattern := range []string{"*", appB, "", "app_*"} {
		fixture, _ := newAppFixture(testInstance)
		fixture.createAppWebhook(testInstance, appA, "app_a_hook")
		route := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")
		fixture.store.calls = nil

		_, err := fixture.service.UpdateAppRoute(context.Background(), appA, route.ID, RoutePatch{AppPattern: &pattern})

		requireValidationError(testInstance, "pattern "+pattern, err, "app_pattern must be the app's own id")
		requireCalls(testInstance, "pattern "+pattern, fixture.store)
		if fixture.store.routes[route.ID].AppPattern != appA {
			testInstance.Fatalf("pattern %q: the stored app_pattern changed to %q", pattern, fixture.store.routes[route.ID].AppPattern)
		}
	}
}

func TestUpdateAppRouteMayNotLinkAnotherAppsDestination(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")
	route := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")

	_, err := fixture.service.UpdateAppRoute(context.Background(), appA, route.ID, RoutePatch{
		Destinations: []string{"app_b_hook"},
	})

	if !errors.Is(err, ErrUnknownDestination) {
		testInstance.Fatalf("err = %v, want ErrUnknownDestination", err)
	}
	if !reflect.DeepEqual(fixture.store.routes[route.ID].DestinationNames, []string{"app_a_hook"}) {
		testInstance.Fatalf("destinations = %v, want the route unchanged", fixture.store.routes[route.ID].DestinationNames)
	}
}

// lockRaceRouteStore runs beforeLock just before the inner UpdateRoute loads the row, standing for a change
// that lands between a caller's decision and the row lock.
type lockRaceRouteStore struct {
	*fakeStore
	beforeLock func()
}

func (store *lockRaceRouteStore) UpdateRoute(
	ctx context.Context, id int64, mutate func(StoredRoute) (StoredRoute, error),
) (StoredRoute, error) {
	store.beforeLock()
	return store.fakeStore.UpdateRoute(ctx, id, mutate)
}

// The owner is checked on the row the store locked: a route that stopped being the app's is never written.
func TestUpdateAppRouteRechecksTheOwnerUnderTheRowLock(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	route := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")
	racing := &lockRaceRouteStore{fakeStore: fixture.store, beforeLock: func() {
		row := fixture.store.routes[route.ID]
		row.OwnerKind, row.OwnerAppID = OwnerOperator, nil
		fixture.store.routes[route.ID] = row
	}}
	service := NewService(racing, fixture.sealer, nil)
	disabled := false

	_, err := service.UpdateAppRoute(context.Background(), appA, route.ID, RoutePatch{Enabled: &disabled})

	if !errors.Is(err, ErrRouteNotFound) {
		testInstance.Fatalf("err = %v, want ErrRouteNotFound", err)
	}
	if !fixture.store.routes[route.ID].Enabled {
		testInstance.Fatalf("the route was written although it was no longer the app's")
	}
}

func TestDeleteAppRoute(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	route := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")
	fixture.store.calls = nil

	if err := fixture.service.DeleteAppRoute(context.Background(), appA, route.ID); err != nil {
		testInstance.Fatalf("DeleteAppRoute: %v", err)
	}

	// The owner travels with the delete in the one store call: there is no separate read to race.
	requireCalls(testInstance, "delete", fixture.store, "DeleteRouteOwnedBy")
	if _, stillThere := fixture.store.routes[route.ID]; stillThere {
		testInstance.Fatalf("the route was not deleted")
	}
	if err := fixture.service.DeleteAppRoute(context.Background(), appA, route.ID); !errors.Is(err, ErrRouteNotFound) {
		testInstance.Fatalf("a second delete: err = %v, want ErrRouteNotFound (not the admin's idempotent success)", err)
	}
}

func TestAppRouteMethodsRefuseAMalformedCallerAppID(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	ctx := context.Background()

	_, listErr := fixture.service.ListAppRoutes(ctx, "")
	_, getErr := fixture.service.GetAppRoute(ctx, "*", 1)
	_, updateErr := fixture.service.UpdateAppRoute(ctx, "", 1, RoutePatch{})
	deleteErr := fixture.service.DeleteAppRoute(ctx, "a b", 1)

	for label, err := range map[string]error{"list": listErr, "get": getErr, "update": updateErr, "delete": deleteErr} {
		requireValidationError(testInstance, label, err, "invalid app_id")
	}
	requireCalls(testInstance, "malformed caller", fixture.store)
}

func TestAppRouteStoreErrorsPassThrough(testInstance *testing.T) {
	boom := errors.New("boom")
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	route := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")
	fixture.store.failures = map[string]error{
		"ListRoutesByOwner": boom, "GetRoute": boom, "DeleteRouteOwnedBy": boom, "InsertRoute": boom, "UpdateRoute": boom,
	}
	logsBefore := fixture.logs.String()
	ctx := context.Background()
	disabled := false

	_, listErr := fixture.service.ListAppRoutes(ctx, appA)
	_, getErr := fixture.service.GetAppRoute(ctx, appA, route.ID)
	deleteErr := fixture.service.DeleteAppRoute(ctx, appA, route.ID)
	_, createErr := fixture.service.CreateAppRoute(ctx, appA, RouteInput{
		ChannelPattern: "two", Destinations: []string{"app_a_hook"},
	})
	_, updateErr := fixture.service.UpdateAppRoute(ctx, appA, route.ID, RoutePatch{Enabled: &disabled})

	for label, err := range map[string]error{
		"list": listErr, "get": getErr, "delete": deleteErr, "create": createErr, "update": updateErr,
	} {
		if !errors.Is(err, boom) {
			testInstance.Fatalf("%s: err = %v, want the store error", label, err)
		}
	}
	// A failed write must not be reported as a success in the log.
	logged := fixture.logs.String()
	for _, successLine := range []string{"app route created", "app route updated", "app route deleted"} {
		if strings.Contains(strings.TrimPrefix(logged, logsBefore), successLine) {
			testInstance.Fatalf("a failed call logged %q:\n%s", successLine, logged)
		}
	}
}

func TestAppRouteWritesLogTheAppAndTheRoute(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	logsBefore := fixture.logs.String()
	ctx := context.Background()
	disabled := false

	route := fixture.createAppRoute(testInstance, appA, "one", "app_a_hook")
	_, updateErr := fixture.service.UpdateAppRoute(ctx, appA, route.ID, RoutePatch{Enabled: &disabled})
	deleteErr := fixture.service.DeleteAppRoute(ctx, appA, route.ID)

	if updateErr != nil || deleteErr != nil {
		testInstance.Fatalf("update = %v, delete = %v", updateErr, deleteErr)
	}
	logged := strings.TrimPrefix(fixture.logs.String(), logsBefore)
	for _, line := range []string{"app route created", "app route updated", "app route deleted"} {
		requireLogLine(testInstance, logged, line, "app_id="+appA, "route_id="+strconv.FormatInt(route.ID, 10))
	}
}

// requireLogLine requires one line of logged that contains every fragment, so a fragment cannot be satisfied by a
// different line.
func requireLogLine(testInstance *testing.T, logged string, fragments ...string) {
	testInstance.Helper()
	for _, line := range strings.Split(logged, "\n") {
		matches := true
		for _, fragment := range fragments {
			matches = matches && strings.Contains(line, fragment)
		}
		if matches {
			return
		}
	}
	testInstance.Fatalf("no log line contains all of %q in:\n%s", fragments, logged)
}

// A refused read answers exactly like an absent route; the Warn line is the only place an operator can see an
// app probing ids that belong to someone else.
func TestARefusedForeignGetAppRouteIsLogged(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")
	foreignRoute := fixture.createAppRoute(testInstance, appB, "theirs", "app_b_hook")
	logsBefore := fixture.logs.String()

	_, err := fixture.service.GetAppRoute(context.Background(), appA, foreignRoute.ID)

	if !errors.Is(err, ErrRouteNotFound) {
		testInstance.Fatalf("err = %v, want ErrRouteNotFound", err)
	}
	logged := strings.TrimPrefix(fixture.logs.String(), logsBefore)
	requireLogLine(testInstance, logged, "level=WARN", "app route read refused",
		"app_id="+appA, "route_id="+strconv.FormatInt(foreignRoute.ID, 10))
}

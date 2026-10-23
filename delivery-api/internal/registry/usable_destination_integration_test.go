//go:build integration

package registry_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	usableOwnerApp   = "app_a"
	usableOtherApp   = "app_b"
	usableAppHook    = "app_a_hook"
	usableSharedHook = "shared_hook"
)

// mustInsertAppOwnedDestination inserts a destination owned by ownerApp straight
// through the store. An app can now create one through the app-scoped API, but this test seeds it directly
// so it stays independent of that API.
func mustInsertAppOwnedDestination(
	ctx context.Context, testInstance *testing.T, store *registry.PGStore, name string, ownerApp string,
) registry.StoredDestination {
	testInstance.Helper()
	destination := sampleDestination(name)
	destination.OwnerKind = registry.OwnerApp
	destination.OwnerAppID = &ownerApp
	inserted, err := store.InsertDestination(ctx, destination)
	if err != nil {
		testInstance.Fatalf("insert app-owned destination %s: %v", name, err)
	}
	return inserted
}

func countRows(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, table string) int {
	testInstance.Helper()
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil {
		testInstance.Fatalf("count %s: %v", table, err)
	}
	return rows
}

func TestInsertRouteRefusesDestinationsItsAppMayNotUse(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	mustInsertAppOwnedDestination(ctx, testInstance, store, usableAppHook, usableOwnerApp)
	mustInsertDestination(ctx, testInstance, store, usableSharedHook)

	refused := []struct {
		name         string
		appPattern   string
		destinations []string
	}{
		{"operator wildcard route to an app-owned destination", "*", []string{usableAppHook}},
		{"route of another app to an app-owned destination", usableOtherApp, []string{usableAppHook}},
		{"wildcard route mixing an operator and an app-owned destination", "*", []string{usableSharedHook, usableAppHook}},
	}
	for _, test := range refused {
		_, err := store.InsertRoute(ctx, registry.StoredRoute{
			AppPattern: test.appPattern, ChannelPattern: "*", DestinationNames: test.destinations, Enabled: true,
		})
		if !errors.Is(err, registry.ErrUnknownDestination) {
			testInstance.Fatalf("%s: error = %v, want ErrUnknownDestination (the same as an absent destination)", test.name, err)
		}
	}
	if rows := countRows(ctx, testInstance, pool, "routes"); rows != 0 {
		testInstance.Fatalf("routes rows = %d after refused inserts, want 0", rows)
	}

	allowed := []struct {
		name         string
		appPattern   string
		destinations []string
	}{
		{"operator route whose app_pattern is the owner", usableOwnerApp, []string{usableAppHook}},
		{"owner route also using an operator-owned destination", usableOwnerApp, []string{usableAppHook, usableSharedHook}},
		{"wildcard route to an operator-owned destination", "*", []string{usableSharedHook}},
		{"other app route to an operator-owned destination", usableOtherApp, []string{usableSharedHook}},
	}
	for index, test := range allowed {
		// A distinct channel per case keeps the (app, channel) pairs unique.
		if _, err := store.InsertRoute(ctx, registry.StoredRoute{
			AppPattern: test.appPattern, ChannelPattern: "channel_" + strconv.Itoa(index),
			DestinationNames: test.destinations, Enabled: true,
		}); err != nil {
			testInstance.Fatalf("%s: %v", test.name, err)
		}
	}
}

func TestUpdateRouteRefusesDestinationsItsAppMayNotUse(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)
	mustInsertAppOwnedDestination(ctx, testInstance, store, usableAppHook, usableOwnerApp)
	mustInsertDestination(ctx, testInstance, store, usableSharedHook)
	ownerRoute := mustInsertRoute(ctx, testInstance, store, usableOwnerApp, "*", []string{usableAppHook})
	wildcardRoute := mustInsertRoute(ctx, testInstance, store, "*", "*", []string{usableSharedHook})

	// Widening the owner's route to a wildcard would expose its destination to everyone.
	_, widenErr := store.UpdateRoute(ctx, ownerRoute.ID, func(current registry.StoredRoute) (registry.StoredRoute, error) {
		current.AppPattern = "*"
		return current, nil
	})
	if !errors.Is(widenErr, registry.ErrUnknownDestination) {
		testInstance.Fatalf("widening the pattern: error = %v, want ErrUnknownDestination", widenErr)
	}
	// Adding another app's destination to a wildcard route.
	_, addErr := store.UpdateRoute(ctx, wildcardRoute.ID, func(current registry.StoredRoute) (registry.StoredRoute, error) {
		current.DestinationNames = []string{usableSharedHook, usableAppHook}
		return current, nil
	})
	if !errors.Is(addErr, registry.ErrUnknownDestination) {
		testInstance.Fatalf("adding another app's destination: error = %v, want ErrUnknownDestination", addErr)
	}
	// Re-pointing the owner's route at a different app.
	_, repointErr := store.UpdateRoute(ctx, ownerRoute.ID, func(current registry.StoredRoute) (registry.StoredRoute, error) {
		current.AppPattern = usableOtherApp
		return current, nil
	})
	if !errors.Is(repointErr, registry.ErrUnknownDestination) {
		testInstance.Fatalf("re-pointing at another app: error = %v, want ErrUnknownDestination", repointErr)
	}

	for _, unchanged := range []struct {
		route   registry.StoredRoute
		pattern string
		names   []string
	}{
		{ownerRoute, usableOwnerApp, []string{usableAppHook}},
		{wildcardRoute, "*", []string{usableSharedHook}},
	} {
		got, err := store.GetRoute(ctx, unchanged.route.ID)
		if err != nil || got.AppPattern != unchanged.pattern || strings.Join(got.DestinationNames, ",") != strings.Join(unchanged.names, ",") {
			testInstance.Fatalf("route %d after refused updates = (%+v, %v), want pattern %q and destinations %v",
				unchanged.route.ID, got, err, unchanged.pattern, unchanged.names)
		}
	}

	// The owner's own route can still gain an operator-owned destination.
	if _, err := store.UpdateRoute(ctx, ownerRoute.ID, func(current registry.StoredRoute) (registry.StoredRoute, error) {
		current.DestinationNames = []string{usableAppHook, usableSharedHook}
		return current, nil
	}); err != nil {
		testInstance.Fatalf("owner route gaining an operator-owned destination: %v", err)
	}
}

// newUsableService returns a Service over a real Postgres store with a logger
// writing into the returned buffer.
func newUsableService(
	testInstance *testing.T,
) (context.Context, *registry.Service, *registry.PGStore, *pgxpool.Pool, *bytes.Buffer) {
	testInstance.Helper()
	ctx, store, pool := newTestStore(testInstance)
	sealer, sealErr := seal.New(bytes.Repeat([]byte{7}, seal.KeySize))
	if sealErr != nil {
		testInstance.Fatalf("seal.New: %v", sealErr)
	}
	logs := &bytes.Buffer{}
	service := registry.NewService(store, sealer, slog.New(slog.NewTextHandler(logs, nil)))
	return ctx, service, store, pool, logs
}

func TestResolveNeverReachesAnotherAppsDestinationEvenFromABadRow(testInstance *testing.T) {
	ctx, service, store, pool, logs := newUsableService(testInstance)
	for _, name := range []string{usableAppHook, usableSharedHook} {
		if _, err := service.CreateDestination(ctx, registry.DestinationInput{
			Name: name, Type: registry.TypeDiscord,
			Secrets: map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/token"},
		}); err != nil {
			testInstance.Fatalf("create %s: %v", name, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`UPDATE destinations SET owner_kind = 'app', owner_app_id = $2 WHERE name = $1`,
		usableAppHook, usableOwnerApp); err != nil {
		testInstance.Fatalf("hand an app-owned destination to %s: %v", usableOwnerApp, err)
	}
	operatorRoute := mustInsertRoute(ctx, testInstance, store, "*", "alerts", []string{usableSharedHook})

	// The write path refuses the link, so insert the bad row with SQL.
	if _, err := service.CreateRoute(ctx, registry.RouteInput{
		AppPattern: "*", ChannelPattern: "alerts", Destinations: []string{usableAppHook},
	}); !errors.Is(err, registry.ErrUnknownDestination) {
		testInstance.Fatalf("CreateRoute of a wildcard route to an app-owned destination = %v, want ErrUnknownDestination", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO route_destinations (route_id, destination_id)
SELECT $1, id FROM destinations WHERE name = $2`, operatorRoute.ID, usableAppHook); err != nil {
		testInstance.Fatalf("insert the bad route link directly: %v", err)
	}

	resolved, err := service.Resolve(ctx, usableOtherApp, "", "alerts")
	if err != nil {
		testInstance.Fatalf("resolve for app_b: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != usableSharedHook {
		testInstance.Fatalf("app_b resolved %+v, want only the operator-owned destination", resolved)
	}
	logged := logs.String()
	for _, want := range []string{
		"level=WARN", "destination=" + usableAppHook, "app_id=" + usableOtherApp,
		"route_ids=[" + strconv.FormatInt(operatorRoute.ID, 10) + "]",
	} {
		if !strings.Contains(logged, want) {
			testInstance.Fatalf("the filtered destination was not logged: %q lacks %q", logged, want)
		}
	}

	if _, namedErr := service.Resolve(ctx, usableOtherApp, usableAppHook, "alerts"); !errors.Is(namedErr, registry.ErrUnknownDestination) {
		testInstance.Fatalf("app_b naming app_a's destination = %v, want ErrUnknownDestination", namedErr)
	}
	if _, absentErr := service.Resolve(ctx, usableOtherApp, "app_a_missing", "alerts"); !errors.Is(absentErr, registry.ErrUnknownDestination) {
		testInstance.Fatalf("app_b naming an absent destination = %v, want ErrUnknownDestination", absentErr)
	}

	// The owner reaches its own destination, by name and by the route.
	ownerNamed, err := service.Resolve(ctx, usableOwnerApp, usableAppHook, "alerts")
	if err != nil || len(ownerNamed) != 1 || ownerNamed[0].Name != usableAppHook {
		testInstance.Fatalf("app_a naming its own destination = (%+v, %v)", ownerNamed, err)
	}
	ownerRouted, err := service.Resolve(ctx, usableOwnerApp, "", "alerts")
	if err != nil || len(ownerRouted) != 2 {
		testInstance.Fatalf("app_a routed resolve = (%+v, %v), want both destinations", ownerRouted, err)
	}

	// Disabling the destination must not leak through another app: app_b still sees
	// an unknown name, while the owner sees the real reason.
	disabled := false
	if _, disableErr := service.UpdateDestination(ctx, usableAppHook, registry.DestinationPatch{Enabled: &disabled}); disableErr != nil {
		testInstance.Fatalf("disable %s: %v", usableAppHook, disableErr)
	}
	if _, otherErr := service.Resolve(ctx, usableOtherApp, usableAppHook, "alerts"); !errors.Is(otherErr, registry.ErrUnknownDestination) {
		testInstance.Fatalf("app_b naming app_a's disabled destination = %v, want ErrUnknownDestination", otherErr)
	}
	if _, ownerErr := service.Resolve(ctx, usableOwnerApp, usableAppHook, "alerts"); !errors.Is(ownerErr, registry.ErrDestinationDisabled) {
		testInstance.Fatalf("app_a naming its own disabled destination = %v, want ErrDestinationDisabled", ownerErr)
	}
}

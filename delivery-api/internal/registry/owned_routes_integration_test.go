//go:build integration

package registry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// insertOwnedRoute inserts a route owned by ownerApp (app_pattern = ownerApp, as the CHECK demands).
func insertOwnedRoute(
	ctx context.Context, testInstance *testing.T, store *registry.PGStore, ownerApp, channel string, destinations ...string,
) registry.StoredRoute {
	testInstance.Helper()
	owner := ownerApp
	inserted, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern: ownerApp, ChannelPattern: channel, DestinationNames: destinations, Enabled: true,
		OwnerKind: registry.OwnerApp, OwnerAppID: &owner,
	})
	if err != nil {
		testInstance.Fatalf("insert route owned by %s: %v", ownerApp, err)
	}
	return inserted
}

func TestListRoutesByOwnerReturnsOnlyThatAppsRoutesInIDOrder(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)
	mustInsertDestination(ctx, testInstance, store, usableSharedHook)
	mine := insertOwnedRoute(ctx, testInstance, store, usableOwnerApp, "one", usableSharedHook)
	insertOwnedRoute(ctx, testInstance, store, usableOtherApp, "one", usableSharedHook)
	// An operator route whose app_pattern names the app is still the operator's.
	if _, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern: usableOwnerApp, ChannelPattern: "operator", DestinationNames: []string{usableSharedHook}, Enabled: true,
	}); err != nil {
		testInstance.Fatalf("insert operator route: %v", err)
	}
	also := insertOwnedRoute(ctx, testInstance, store, usableOwnerApp, "two", usableSharedHook)

	listed, err := store.ListRoutesByOwner(ctx, usableOwnerApp)

	if err != nil || len(listed) != 2 || listed[0].ID != mine.ID || listed[1].ID != also.ID {
		testInstance.Fatalf("ListRoutesByOwner = (%+v, %v), want exactly routes %d and %d in order", listed, err, mine.ID, also.ID)
	}
	none, err := store.ListRoutesByOwner(ctx, "nobody")
	if err != nil || none == nil || len(none) != 0 {
		testInstance.Fatalf("ListRoutesByOwner(nobody) = (%#v, %v), want an empty non-nil slice", none, err)
	}
}

func TestDeleteRouteOwnedByRemovesOnlyTheOwnersRoute(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	mustInsertDestination(ctx, testInstance, store, usableSharedHook)
	mine := insertOwnedRoute(ctx, testInstance, store, usableOwnerApp, "one", usableSharedHook)
	operatorRoute, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern: usableOwnerApp, ChannelPattern: "operator", DestinationNames: []string{usableSharedHook}, Enabled: true,
	})
	if err != nil {
		testInstance.Fatalf("insert operator route: %v", err)
	}

	for label, attempt := range map[string]struct {
		id    int64
		appID string
	}{
		"another app":               {id: mine.ID, appID: usableOtherApp},
		"the operator's route":      {id: operatorRoute.ID, appID: usableOwnerApp},
		"an absent route":           {id: 1_000_000, appID: usableOwnerApp},
		"an empty app id":           {id: mine.ID, appID: ""},
		"a wildcard-looking app id": {id: mine.ID, appID: "*"},
	} {
		deleted, deleteErr := store.DeleteRouteOwnedBy(ctx, attempt.id, attempt.appID)
		if deleteErr != nil || deleted {
			testInstance.Fatalf("%s: DeleteRouteOwnedBy = (%v, %v), want (false, nil)", label, deleted, deleteErr)
		}
	}
	if rows := countRows(ctx, testInstance, pool, "routes"); rows != 2 {
		testInstance.Fatalf("routes rows = %d after refused deletes, want 2", rows)
	}

	deleted, err := store.DeleteRouteOwnedBy(ctx, mine.ID, usableOwnerApp)
	if err != nil || !deleted {
		testInstance.Fatalf("owner DeleteRouteOwnedBy = (%v, %v), want (true, nil)", deleted, err)
	}
	if _, getErr := store.GetRoute(ctx, mine.ID); !errors.Is(getErr, registry.ErrRouteNotFound) {
		testInstance.Fatalf("GetRoute after delete = %v, want ErrRouteNotFound", getErr)
	}
}

// The owner is visible to UpdateRoute's mutate on the locked row, which is what lets the service check it
// there.
func TestUpdateRouteHandsMutateTheLockedRowsOwner(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)
	mustInsertDestination(ctx, testInstance, store, usableSharedHook)
	mine := insertOwnedRoute(ctx, testInstance, store, usableOwnerApp, "one", usableSharedHook)

	var seen registry.StoredRoute
	_, err := store.UpdateRoute(ctx, mine.ID, func(locked registry.StoredRoute) (registry.StoredRoute, error) {
		seen = locked
		return locked, nil
	})

	if err != nil || seen.OwnerKind != registry.OwnerApp || seen.OwnerAppID == nil || *seen.OwnerAppID != usableOwnerApp {
		testInstance.Fatalf("mutate saw owner %q/%v (err %v), want app/%s", seen.OwnerKind, seen.OwnerAppID, err, usableOwnerApp)
	}
}

// A route whose app_pattern is not its owner's id is refused by the database CHECK, and surfaces as the
// client-correctable ErrCheckViolation, not a server fault.
func TestAppRoutePatternCheckSurfacesAsErrCheckViolation(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	mustInsertDestination(ctx, testInstance, store, usableSharedHook)
	owner := usableOwnerApp

	_, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern: "*", ChannelPattern: "wild", DestinationNames: []string{usableSharedHook}, Enabled: true,
		OwnerKind: registry.OwnerApp, OwnerAppID: &owner,
	})

	if !errors.Is(err, registry.ErrCheckViolation) {
		testInstance.Fatalf("error = %v, want ErrCheckViolation", err)
	}
	if rows := countRows(ctx, testInstance, pool, "routes"); rows != 0 {
		testInstance.Fatalf("routes rows = %d after the refused insert, want 0", rows)
	}
}

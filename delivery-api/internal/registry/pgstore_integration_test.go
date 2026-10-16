//go:build integration

package registry_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const pgStoreTestBudget = 30 * time.Second

// absentID is far past any id a test inserts.
const absentID int64 = 1_000_000

var errMutateFailed = errors.New("mutate failed")

// newTestStore returns a PGStore over a fresh isolated schema, plus the pool
// for assertions that bypass the store.
func newTestStore(testInstance *testing.T) (context.Context, *registry.PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pgStoreTestBudget)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	return ctx, registry.NewPGStore(pool), pool
}

func sampleDestination(name string) registry.StoredDestination {
	return registry.StoredDestination{
		Name:          name,
		Type:          registry.TypeNtfy,
		Config:        map[string]string{"topic": "alerts_" + name},
		SealedSecrets: []byte{0x01, 0xAA, 0xBB},
		Enabled:       true,
	}
}

func mustInsertDestination(
	ctx context.Context, testInstance *testing.T, store *registry.PGStore, name string,
) registry.StoredDestination {
	testInstance.Helper()
	inserted, err := store.InsertDestination(ctx, sampleDestination(name))
	if err != nil {
		testInstance.Fatalf("insert destination %s: %v", name, err)
	}
	return inserted
}

func mustInsertRoute(
	ctx context.Context, testInstance *testing.T, store *registry.PGStore,
	appPattern string, channelPattern string, destinationNames []string,
) registry.StoredRoute {
	testInstance.Helper()
	inserted, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern:       appPattern,
		ChannelPattern:   channelPattern,
		DestinationNames: destinationNames,
		Enabled:          true,
	})
	if err != nil {
		testInstance.Fatalf("insert route %s/%s: %v", appPattern, channelPattern, err)
	}
	return inserted
}

func destinationNames(destinations []registry.StoredDestination) []string {
	names := make([]string, 0, len(destinations))
	for _, destination := range destinations {
		names = append(names, destination.Name)
	}
	return names
}

func TestInsertAndGetDestination(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	inserted := mustInsertDestination(ctx, testInstance, store, "ops_ntfy")
	if inserted.ID == 0 || inserted.CreatedAt.IsZero() || inserted.UpdatedAt.IsZero() {
		testInstance.Fatalf("insert did not populate id and timestamps: %+v", inserted)
	}

	got, err := store.GetDestination(ctx, "ops_ntfy")
	if err != nil {
		testInstance.Fatalf("get: %v", err)
	}
	if got.ID != inserted.ID || got.Name != "ops_ntfy" || got.Type != registry.TypeNtfy || !got.Enabled {
		testInstance.Fatalf("get = %+v, want the inserted row %+v", got, inserted)
	}
	if !reflect.DeepEqual(got.Config, map[string]string{"topic": "alerts_ops_ntfy"}) {
		testInstance.Fatalf("config = %v", got.Config)
	}
	if !bytes.Equal(got.SealedSecrets, []byte{0x01, 0xAA, 0xBB}) {
		testInstance.Fatalf("sealed secrets = %v", got.SealedSecrets)
	}
	if got.DisabledReason != nil {
		testInstance.Fatalf("disabled reason = %v, want nil", *got.DisabledReason)
	}
}

func TestInsertDestinationWithNilConfigRoundTripsAsEmptyMap(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	destination := sampleDestination("no_config")
	destination.Config = nil
	if _, err := store.InsertDestination(ctx, destination); err != nil {
		testInstance.Fatalf("insert: %v", err)
	}
	got, err := store.GetDestination(ctx, "no_config")
	if err != nil {
		testInstance.Fatalf("get: %v", err)
	}
	if got.Config == nil || len(got.Config) != 0 {
		testInstance.Fatalf("config = %#v, want a non-nil empty map", got.Config)
	}
}

func TestListDestinationsIsOrderedByNameAndNonNil(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	empty, err := store.ListDestinations(ctx)
	if err != nil {
		testInstance.Fatalf("list empty: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		testInstance.Fatalf("empty list = %#v, want a non-nil empty slice", empty)
	}

	for _, name := range []string{"charlie", "alpha", "bravo"} {
		mustInsertDestination(ctx, testInstance, store, name)
	}
	listed, err := store.ListDestinations(ctx)
	if err != nil {
		testInstance.Fatalf("list: %v", err)
	}
	if want := []string{"alpha", "bravo", "charlie"}; !reflect.DeepEqual(destinationNames(listed), want) {
		testInstance.Fatalf("names = %v, want %v", destinationNames(listed), want)
	}
}

func TestInsertDuplicateDestinationIsRejected(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "dup")
	_, err := store.InsertDestination(ctx, sampleDestination("dup"))
	if !errors.Is(err, registry.ErrDestinationExists) {
		testInstance.Fatalf("err = %v, want ErrDestinationExists", err)
	}
}

func TestGetMissingDestination(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	if _, err := store.GetDestination(ctx, "missing"); !errors.Is(err, registry.ErrDestinationNotFound) {
		testInstance.Fatalf("err = %v, want ErrDestinationNotFound", err)
	}
}

func TestGetDestinationsByIDReturnsOnlyRequested(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	first := mustInsertDestination(ctx, testInstance, store, "first")
	mustInsertDestination(ctx, testInstance, store, "second")
	third := mustInsertDestination(ctx, testInstance, store, "third")

	got, err := store.GetDestinationsByID(ctx, []int64{third.ID, first.ID, absentID})
	if err != nil {
		testInstance.Fatalf("get by id: %v", err)
	}
	if want := []string{"first", "third"}; !reflect.DeepEqual(destinationNames(got), want) {
		testInstance.Fatalf("names = %v, want %v", destinationNames(got), want)
	}

	none, err := store.GetDestinationsByID(ctx, nil)
	if err != nil {
		testInstance.Fatalf("get by no ids: %v", err)
	}
	if none == nil || len(none) != 0 {
		testInstance.Fatalf("no ids = %#v, want a non-nil empty slice", none)
	}
}

func TestUpdateDestinationPersistsMutationAndBumpsUpdatedAt(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	inserted := mustInsertDestination(ctx, testInstance, store, "mutable")
	reason := "gone"
	updated, err := store.UpdateDestination(ctx, "mutable",
		func(current registry.StoredDestination) (registry.StoredDestination, error) {
			if current.Name != "mutable" {
				testInstance.Errorf("mutate saw %q, want the locked row", current.Name)
			}
			current.Config = map[string]string{"topic": "changed"}
			current.SealedSecrets = []byte{0x01, 0xCC}
			current.Enabled = false
			current.DisabledReason = &reason
			return current, nil
		})
	if err != nil {
		testInstance.Fatalf("update: %v", err)
	}
	if !updated.UpdatedAt.After(inserted.UpdatedAt) {
		testInstance.Fatalf("updated_at %v did not advance past %v", updated.UpdatedAt, inserted.UpdatedAt)
	}

	got, err := store.GetDestination(ctx, "mutable")
	if err != nil {
		testInstance.Fatalf("get: %v", err)
	}
	if got.Enabled || got.DisabledReason == nil || *got.DisabledReason != reason {
		testInstance.Fatalf("enabled/reason not persisted: %+v", got)
	}
	if !reflect.DeepEqual(got.Config, map[string]string{"topic": "changed"}) ||
		!bytes.Equal(got.SealedSecrets, []byte{0x01, 0xCC}) {
		testInstance.Fatalf("config/secrets not persisted: %+v", got)
	}
	if got.ID != inserted.ID || got.Name != "mutable" || got.Type != registry.TypeNtfy {
		testInstance.Fatalf("immutable fields changed: %+v", got)
	}
	if !got.UpdatedAt.Equal(updated.UpdatedAt) {
		testInstance.Fatalf("returned updated_at %v != stored %v", updated.UpdatedAt, got.UpdatedAt)
	}
}

func TestUpdateDestinationMutateErrorRollsBack(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "rollback")
	_, err := store.UpdateDestination(ctx, "rollback",
		func(current registry.StoredDestination) (registry.StoredDestination, error) {
			current.Enabled = false
			return current, errMutateFailed
		})
	// Identity, not errors.Is: the error must come back unwrapped.
	if err != errMutateFailed {
		testInstance.Fatalf("err = %v, want the mutate error unchanged", err)
	}
	got, getErr := store.GetDestination(ctx, "rollback")
	if getErr != nil {
		testInstance.Fatalf("get: %v", getErr)
	}
	if !got.Enabled {
		testInstance.Fatal("a failed mutate must not persist")
	}
}

func TestUpdateMissingDestination(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	called := false
	_, err := store.UpdateDestination(ctx, "missing",
		func(current registry.StoredDestination) (registry.StoredDestination, error) {
			called = true
			return current, nil
		})
	if !errors.Is(err, registry.ErrDestinationNotFound) {
		testInstance.Fatalf("err = %v, want ErrDestinationNotFound", err)
	}
	if called {
		testInstance.Fatal("mutate must not run for a missing row")
	}
}

func TestDeleteDestinationIsIdempotent(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "doomed")
	deleted, err := store.DeleteDestination(ctx, "doomed")
	if err != nil || !deleted {
		testInstance.Fatalf("first delete = (%v, %v), want (true, nil)", deleted, err)
	}
	deleted, err = store.DeleteDestination(ctx, "doomed")
	if err != nil || deleted {
		testInstance.Fatalf("second delete = (%v, %v), want (false, nil)", deleted, err)
	}
}

func TestDeleteReferencedDestinationIsInUse(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "pinned")
	route := mustInsertRoute(ctx, testInstance, store, "*", "*", []string{"pinned"})

	deleted, err := store.DeleteDestination(ctx, "pinned")
	if !errors.Is(err, registry.ErrDestinationInUse) || deleted {
		testInstance.Fatalf("delete = (%v, %v), want (false, ErrDestinationInUse)", deleted, err)
	}

	if _, routeErr := store.DeleteRoute(ctx, route.ID); routeErr != nil {
		testInstance.Fatalf("delete route: %v", routeErr)
	}
	deleted, err = store.DeleteDestination(ctx, "pinned")
	if err != nil || !deleted {
		testInstance.Fatalf("delete after route removal = (%v, %v), want (true, nil)", deleted, err)
	}
}

func TestInsertRouteResolvesNamesToIDs(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	zulu := mustInsertDestination(ctx, testInstance, store, "zulu")
	alpha := mustInsertDestination(ctx, testInstance, store, "alpha")

	inserted := mustInsertRoute(ctx, testInstance, store, "billing", "backup_*", []string{"zulu", "alpha", "zulu"})
	if inserted.ID == 0 || inserted.CreatedAt.IsZero() {
		testInstance.Fatalf("insert did not populate id and timestamps: %+v", inserted)
	}
	if want := []string{"alpha", "zulu"}; !reflect.DeepEqual(inserted.DestinationNames, want) {
		testInstance.Fatalf("names = %v, want %v (sorted, de-duplicated)", inserted.DestinationNames, want)
	}
	if want := []int64{alpha.ID, zulu.ID}; !reflect.DeepEqual(inserted.DestinationIDs, want) {
		testInstance.Fatalf("ids = %v, want %v", inserted.DestinationIDs, want)
	}

	got, err := store.GetRoute(ctx, inserted.ID)
	if err != nil {
		testInstance.Fatalf("get route: %v", err)
	}
	if !reflect.DeepEqual(got.DestinationNames, inserted.DestinationNames) ||
		got.AppPattern != "billing" || got.ChannelPattern != "backup_*" || !got.Enabled {
		testInstance.Fatalf("get = %+v, want %+v", got, inserted)
	}
}

func TestInsertRouteWithUnknownDestinationInsertsNothing(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "real")
	_, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern:       "*",
		ChannelPattern:   "*",
		DestinationNames: []string{"real", "ghost"},
		Enabled:          true,
	})
	if !errors.Is(err, registry.ErrUnknownDestination) {
		testInstance.Fatalf("err = %v, want ErrUnknownDestination", err)
	}

	var routeCount int
	if scanErr := pool.QueryRow(ctx, "SELECT count(*) FROM routes").Scan(&routeCount); scanErr != nil {
		testInstance.Fatalf("count routes: %v", scanErr)
	}
	if routeCount != 0 {
		testInstance.Fatalf("routes = %d, want 0", routeCount)
	}
}

func TestInsertDuplicateRoutePatternsIsRejected(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "sink")
	mustInsertRoute(ctx, testInstance, store, "app", "chan", []string{"sink"})
	_, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern: "app", ChannelPattern: "chan", DestinationNames: []string{"sink"}, Enabled: true,
	})
	if !errors.Is(err, registry.ErrRouteExists) {
		testInstance.Fatalf("err = %v, want ErrRouteExists", err)
	}
}

func TestGetMissingRoute(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	if _, err := store.GetRoute(ctx, absentID); !errors.Is(err, registry.ErrRouteNotFound) {
		testInstance.Fatalf("err = %v, want ErrRouteNotFound", err)
	}
}

func TestListRoutesReturnsSortedNamesAndIsNonNil(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	empty, err := store.ListRoutes(ctx)
	if err != nil {
		testInstance.Fatalf("list empty: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		testInstance.Fatalf("empty list = %#v, want a non-nil empty slice", empty)
	}

	for _, name := range []string{"b_sink", "a_sink", "c_sink"} {
		mustInsertDestination(ctx, testInstance, store, name)
	}
	first := mustInsertRoute(ctx, testInstance, store, "one", "*", []string{"c_sink", "a_sink"})
	second := mustInsertRoute(ctx, testInstance, store, "two", "*", []string{"b_sink"})

	listed, err := store.ListRoutes(ctx)
	if err != nil {
		testInstance.Fatalf("list: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != first.ID || listed[1].ID != second.ID {
		testInstance.Fatalf("list = %+v, want routes ordered by id", listed)
	}
	if want := []string{"a_sink", "c_sink"}; !reflect.DeepEqual(listed[0].DestinationNames, want) {
		testInstance.Fatalf("names = %v, want %v", listed[0].DestinationNames, want)
	}
}

func TestUpdateRouteReplacesDestinationSetAtomically(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	for _, name := range []string{"old_one", "old_two", "fresh"} {
		mustInsertDestination(ctx, testInstance, store, name)
	}
	route := mustInsertRoute(ctx, testInstance, store, "app", "*", []string{"old_one", "old_two"})

	updated, err := store.UpdateRoute(ctx, route.ID, func(current registry.StoredRoute) (registry.StoredRoute, error) {
		if want := []string{"old_one", "old_two"}; !reflect.DeepEqual(current.DestinationNames, want) {
			testInstance.Errorf("mutate saw names %v, want %v", current.DestinationNames, want)
		}
		current.DestinationNames = []string{"fresh", "old_one"}
		current.Enabled = false
		current.ChannelPattern = "backup_*"
		return current, nil
	})
	if err != nil {
		testInstance.Fatalf("update: %v", err)
	}
	if want := []string{"fresh", "old_one"}; !reflect.DeepEqual(updated.DestinationNames, want) {
		testInstance.Fatalf("names = %v, want %v", updated.DestinationNames, want)
	}
	if !updated.UpdatedAt.After(route.UpdatedAt) {
		testInstance.Fatalf("updated_at %v did not advance past %v", updated.UpdatedAt, route.UpdatedAt)
	}

	got, err := store.GetRoute(ctx, route.ID)
	if err != nil {
		testInstance.Fatalf("get: %v", err)
	}
	if got.Enabled || got.ChannelPattern != "backup_*" ||
		!reflect.DeepEqual(got.DestinationNames, []string{"fresh", "old_one"}) {
		testInstance.Fatalf("update not persisted: %+v", got)
	}
}

func TestUpdateRouteFailuresChangeNothing(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "keep")
	route := mustInsertRoute(ctx, testInstance, store, "app", "*", []string{"keep"})
	other := mustInsertRoute(ctx, testInstance, store, "other", "*", []string{"keep"})

	testCases := []struct {
		name    string
		mutate  func(registry.StoredRoute) (registry.StoredRoute, error)
		wantErr error
	}{
		{
			name: "mutate error is returned unchanged",
			mutate: func(current registry.StoredRoute) (registry.StoredRoute, error) {
				return current, errMutateFailed
			},
			wantErr: errMutateFailed,
		},
		{
			name: "unknown destination",
			mutate: func(current registry.StoredRoute) (registry.StoredRoute, error) {
				current.DestinationNames = []string{"keep", "ghost"}
				return current, nil
			},
			wantErr: registry.ErrUnknownDestination,
		},
		{
			name: "duplicate patterns",
			mutate: func(current registry.StoredRoute) (registry.StoredRoute, error) {
				current.AppPattern = other.AppPattern
				current.ChannelPattern = other.ChannelPattern
				return current, nil
			},
			wantErr: registry.ErrRouteExists,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			_, err := store.UpdateRoute(ctx, route.ID, testCase.mutate)
			if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			got, getErr := store.GetRoute(ctx, route.ID)
			if getErr != nil {
				subTest.Fatalf("get: %v", getErr)
			}
			if got.AppPattern != "app" || got.ChannelPattern != "*" ||
				!reflect.DeepEqual(got.DestinationNames, []string{"keep"}) {
				subTest.Fatalf("a failed update changed the route: %+v", got)
			}
		})
	}
}

func TestUpdateMissingRoute(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	_, err := store.UpdateRoute(ctx, absentID, func(current registry.StoredRoute) (registry.StoredRoute, error) {
		return current, nil
	})
	if !errors.Is(err, registry.ErrRouteNotFound) {
		testInstance.Fatalf("err = %v, want ErrRouteNotFound", err)
	}
}

func TestDeleteRouteIsIdempotentAndKeepsDestinations(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "survivor")
	route := mustInsertRoute(ctx, testInstance, store, "app", "*", []string{"survivor"})

	deleted, err := store.DeleteRoute(ctx, route.ID)
	if err != nil || !deleted {
		testInstance.Fatalf("first delete = (%v, %v), want (true, nil)", deleted, err)
	}
	deleted, err = store.DeleteRoute(ctx, route.ID)
	if err != nil || deleted {
		testInstance.Fatalf("second delete = (%v, %v), want (false, nil)", deleted, err)
	}
	if _, getErr := store.GetDestination(ctx, "survivor"); getErr != nil {
		testInstance.Fatalf("the destination must survive its route: %v", getErr)
	}
}

func TestListEnabledRoutesOmitsDisabled(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "sink")
	enabled := mustInsertRoute(ctx, testInstance, store, "on", "*", []string{"sink"})
	disabled := mustInsertRoute(ctx, testInstance, store, "off", "*", []string{"sink"})
	if _, err := store.UpdateRoute(ctx, disabled.ID,
		func(current registry.StoredRoute) (registry.StoredRoute, error) {
			current.Enabled = false
			return current, nil
		}); err != nil {
		testInstance.Fatalf("disable route: %v", err)
	}

	listed, err := store.ListEnabledRoutes(ctx)
	if err != nil {
		testInstance.Fatalf("list enabled: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != enabled.ID {
		testInstance.Fatalf("enabled routes = %+v, want only %d", listed, enabled.ID)
	}
	if !reflect.DeepEqual(listed[0].DestinationNames, []string{"sink"}) ||
		len(listed[0].DestinationIDs) != 1 {
		testInstance.Fatalf("enabled route not fully loaded: %+v", listed[0])
	}
}

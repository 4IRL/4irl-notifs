//go:build integration

package registry_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	sqlStateLockNotAvailable = "55P03"

	createRejectLinkFunction = `CREATE FUNCTION reject_link() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.destination_id = %d THEN
    RAISE EXCEPTION 'injected link failure';
  END IF;
  RETURN NEW;
END
$$`
	createRejectLinkTrigger = `CREATE TRIGGER reject_link BEFORE INSERT ON route_destinations
FOR EACH ROW EXECUTE FUNCTION reject_link()`

	injectedFailureMessage = "injected link failure"
)

// injectLinkFailure makes every insert into route_destinations that links
// poisonedDestinationID fail, so a store operation fails after its earlier
// statements already ran.
func injectLinkFailure(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, poisonedDestinationID int64,
) {
	testInstance.Helper()
	if _, err := pool.Exec(ctx, fmt.Sprintf(createRejectLinkFunction, poisonedDestinationID)); err != nil {
		testInstance.Fatalf("create fault function: %v", err)
	}
	if _, err := pool.Exec(ctx, createRejectLinkTrigger); err != nil {
		testInstance.Fatalf("create fault trigger: %v", err)
	}
}

// expectLockNotAvailable runs a NOWAIT row lock on another pooled connection
// and fails the test unless Postgres reports the row is already locked.
func expectLockNotAvailable(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, statement string, argument any,
) {
	testInstance.Helper()
	_, err := pool.Exec(ctx, statement, argument)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != sqlStateLockNotAvailable {
		testInstance.Errorf("concurrent NOWAIT lock error = %v, want SQLSTATE %s", err, sqlStateLockNotAvailable)
	}
}

func TestDestinationErrorsDoNotExposeSealedSecrets(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	invalid := sampleDestination("BAD NAME")
	invalid.SealedSecrets = []byte{0x01, 0xAA, 0xBB}
	_, err := store.InsertDestination(ctx, invalid)
	if err == nil {
		testInstance.Fatal("an invalid destination name must be rejected")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		testInstance.Fatalf("the *pgconn.PgError (its Detail can hold the failing row) must not be reachable: %+v", pgErr)
	}
	rendered := fmt.Sprintf("%v %+v", err, err)
	// The name CHECK violation (SQLSTATE 23514) is a client-correctable conflict, so it surfaces as
	// the ErrCheckViolation sentinel: no driver error, hence no row detail, rides along with it.
	if !errors.Is(err, registry.ErrCheckViolation) {
		testInstance.Fatalf("error %q did not come from the name CHECK constraint", rendered)
	}
	if !strings.Contains(rendered, "(constraint ") {
		testInstance.Fatalf("error %q does not name the violated constraint", rendered)
	}
	for _, leaked := range []string{"aabb", `\x01aa`, "Failing row"} {
		if strings.Contains(strings.ToLower(rendered), strings.ToLower(leaked)) {
			testInstance.Fatalf("error %q leaks %q", rendered, leaked)
		}
	}
}

// A NOT NULL violation (SQLSTATE 23502) is not mapped to a sentinel, so it goes through WrapDBError, and its
// PgError Detail ("Failing row contains (...)") carries the whole row. None of it may be reachable.
func TestDestinationNotNullViolationDoesNotExposeTheRow(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	rowCarrying := sampleDestination("rowleakcheck")
	rowCarrying.SealedSecrets = nil
	_, err := store.InsertDestination(ctx, rowCarrying)
	if err == nil {
		testInstance.Fatal("a destination with no sealed secrets must be rejected")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		testInstance.Fatalf("the *pgconn.PgError (its Detail holds the failing row) must not be reachable: %+v", pgErr)
	}
	rendered := fmt.Sprintf("%v %+v", err, err)
	if !strings.Contains(rendered, "SQLSTATE 23502") {
		testInstance.Fatalf("error %q did not come from the NOT NULL constraint", rendered)
	}
	for _, leaked := range []string{"Failing row", "rowleakcheck", "alerts_rowleakcheck"} {
		if strings.Contains(strings.ToLower(rendered), strings.ToLower(leaked)) {
			testInstance.Fatalf("error %q leaks %q", rendered, leaked)
		}
	}
}

func TestInsertRouteFailureAfterInsertRollsBackTheRoute(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	poisoned := mustInsertDestination(ctx, testInstance, store, "poisoned")
	injectLinkFailure(ctx, testInstance, pool, poisoned.ID)

	_, err := store.InsertRoute(ctx, registry.StoredRoute{
		AppPattern: "app", ChannelPattern: "*", DestinationNames: []string{"poisoned"}, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), injectedFailureMessage) {
		testInstance.Fatalf("err = %v, want the injected link failure", err)
	}

	var routeCount int
	if scanErr := pool.QueryRow(ctx, "SELECT count(*) FROM routes").Scan(&routeCount); scanErr != nil {
		testInstance.Fatalf("count routes: %v", scanErr)
	}
	if routeCount != 0 {
		testInstance.Fatalf("routes = %d, want 0: the route insert must roll back with the failed link", routeCount)
	}
}

func TestUpdateRouteFailureAfterClearingLinksRollsBackEverything(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "keep")
	poisoned := mustInsertDestination(ctx, testInstance, store, "poisoned")
	route := mustInsertRoute(ctx, testInstance, store, "app", "*", []string{"keep"})
	injectLinkFailure(ctx, testInstance, pool, poisoned.ID)

	_, err := store.UpdateRoute(ctx, route.ID, func(current registry.StoredRoute) (registry.StoredRoute, error) {
		current.ChannelPattern = "changed"
		current.Enabled = false
		current.DestinationNames = []string{"keep", "poisoned"}
		return current, nil
	})
	if err == nil || !strings.Contains(err.Error(), injectedFailureMessage) {
		testInstance.Fatalf("err = %v, want the injected link failure", err)
	}

	got, getErr := store.GetRoute(ctx, route.ID)
	if getErr != nil {
		testInstance.Fatalf("get: %v", getErr)
	}
	if got.ChannelPattern != "*" || !got.Enabled || !reflect.DeepEqual(got.DestinationNames, []string{"keep"}) {
		testInstance.Fatalf("route changed despite the failed update: %+v", got)
	}
}

func TestUpdatesHoldRowLocksWhileMutating(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "locked")
	route := mustInsertRoute(ctx, testInstance, store, "app", "*", []string{"locked"})

	testInstance.Run("destination", func(subTest *testing.T) {
		_, err := store.UpdateDestination(ctx, "locked",
			func(current registry.StoredDestination) (registry.StoredDestination, error) {
				expectLockNotAvailable(ctx, subTest, pool,
					"SELECT 1 FROM destinations WHERE name = $1 FOR UPDATE NOWAIT", "locked")
				return current, nil
			})
		if err != nil {
			subTest.Fatalf("update: %v", err)
		}
	})
	testInstance.Run("route", func(subTest *testing.T) {
		_, err := store.UpdateRoute(ctx, route.ID,
			func(current registry.StoredRoute) (registry.StoredRoute, error) {
				expectLockNotAvailable(ctx, subTest, pool,
					"SELECT 1 FROM routes WHERE id = $1 FOR UPDATE NOWAIT", route.ID)
				return current, nil
			})
		if err != nil {
			subTest.Fatalf("update: %v", err)
		}
	})
}

func TestNamesSortBytewiseNotByLocale(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	// Bytewise "a_z" < "ab" ('_' is 0x5F, 'b' is 0x62); glibc locales ignore the
	// underscore and put "ab" first.
	mustInsertDestination(ctx, testInstance, store, "ab")
	mustInsertDestination(ctx, testInstance, store, "a_z")
	want := []string{"a_z", "ab"}

	listed, err := store.ListDestinations(ctx)
	if err != nil {
		testInstance.Fatalf("list destinations: %v", err)
	}
	if !reflect.DeepEqual(destinationNames(listed), want) {
		testInstance.Fatalf("destination names = %v, want %v", destinationNames(listed), want)
	}

	route := mustInsertRoute(ctx, testInstance, store, "app", "*", []string{"ab", "a_z"})
	if !reflect.DeepEqual(route.DestinationNames, want) {
		testInstance.Fatalf("route destination names = %v, want %v", route.DestinationNames, want)
	}
	got, err := store.GetRoute(ctx, route.ID)
	if err != nil {
		testInstance.Fatalf("get route: %v", err)
	}
	if !reflect.DeepEqual(got.DestinationNames, want) {
		testInstance.Fatalf("loaded route destination names = %v, want %v", got.DestinationNames, want)
	}
}

func TestRouteWithoutDestinationsLoadsWithEmptyNonNilSlices(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	noEnabled, err := store.ListEnabledRoutes(ctx)
	if err != nil {
		testInstance.Fatalf("list enabled (none): %v", err)
	}
	if noEnabled == nil || len(noEnabled) != 0 {
		testInstance.Fatalf("no enabled routes = %#v, want a non-nil empty slice", noEnabled)
	}

	testCases := []struct {
		name             string
		appPattern       string
		destinationNames []string
	}{
		{name: "nil names", appPattern: "nil_names", destinationNames: nil},
		{name: "empty names", appPattern: "empty_names", destinationNames: []string{}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			inserted := mustInsertRoute(ctx, subTest, store, testCase.appPattern, "*", testCase.destinationNames)
			assertEmptyDestinations(subTest, "insert", inserted)

			got, getErr := store.GetRoute(ctx, inserted.ID)
			if getErr != nil {
				subTest.Fatalf("get: %v", getErr)
			}
			assertEmptyDestinations(subTest, "get", got)

			for listName, list := range map[string]func(context.Context) ([]registry.StoredRoute, error){
				"list":         store.ListRoutes,
				"list enabled": store.ListEnabledRoutes,
			} {
				routes, listErr := list(ctx)
				if listErr != nil {
					subTest.Fatalf("%s: %v", listName, listErr)
				}
				found := false
				for _, route := range routes {
					if route.ID == inserted.ID {
						found = true
						assertEmptyDestinations(subTest, listName, route)
					}
				}
				if !found {
					subTest.Fatalf("%s did not return route %d", listName, inserted.ID)
				}
			}
		})
	}
}

func assertEmptyDestinations(testInstance *testing.T, operation string, route registry.StoredRoute) {
	testInstance.Helper()
	if route.DestinationNames == nil || len(route.DestinationNames) != 0 ||
		route.DestinationIDs == nil || len(route.DestinationIDs) != 0 {
		testInstance.Fatalf("%s: names = %#v, ids = %#v, want non-nil empty slices",
			operation, route.DestinationNames, route.DestinationIDs)
	}
}

func TestUpdateDestinationIgnoresImmutableFields(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	inserted := mustInsertDestination(ctx, testInstance, store, "immutable")
	updated, err := store.UpdateDestination(ctx, "immutable",
		func(current registry.StoredDestination) (registry.StoredDestination, error) {
			current.ID += 100
			current.Name = "renamed"
			current.Type = registry.TypeWebhook
			current.CreatedAt = current.CreatedAt.AddDate(-1, 0, 0)
			return current, nil
		})
	if err != nil {
		testInstance.Fatalf("update: %v", err)
	}
	if updated.ID != inserted.ID || updated.Name != "immutable" || updated.Type != registry.TypeNtfy ||
		!updated.CreatedAt.Equal(inserted.CreatedAt) {
		testInstance.Fatalf("immutable fields changed: %+v, want %+v", updated, inserted)
	}

	if _, getErr := store.GetDestination(ctx, "renamed"); !errors.Is(getErr, registry.ErrDestinationNotFound) {
		testInstance.Fatalf("get renamed = %v, want ErrDestinationNotFound", getErr)
	}
	got, getErr := store.GetDestination(ctx, "immutable")
	if getErr != nil {
		testInstance.Fatalf("get: %v", getErr)
	}
	if got.ID != inserted.ID || got.Type != registry.TypeNtfy || !got.CreatedAt.Equal(inserted.CreatedAt) {
		testInstance.Fatalf("immutable fields changed in storage: %+v", got)
	}
}

func TestUpdateReferencedDestinationKeepsRouteLinks(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	mustInsertDestination(ctx, testInstance, store, "linked")
	route := mustInsertRoute(ctx, testInstance, store, "app", "*", []string{"linked"})

	if _, err := store.UpdateDestination(ctx, "linked",
		func(current registry.StoredDestination) (registry.StoredDestination, error) {
			current.Config = map[string]string{"topic": "changed"}
			return current, nil
		}); err != nil {
		testInstance.Fatalf("update referenced destination: %v", err)
	}

	got, err := store.GetRoute(ctx, route.ID)
	if err != nil {
		testInstance.Fatalf("get route: %v", err)
	}
	if !reflect.DeepEqual(got.DestinationNames, []string{"linked"}) || len(got.DestinationIDs) != 1 {
		testInstance.Fatalf("route lost its destination: %+v", got)
	}
}

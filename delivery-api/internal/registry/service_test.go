package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	testDiscordURL    = "https://discord.com/api/webhooks/123456/abcDEF_-tokenSECRET"
	testDiscordMasked = "https://discord.com/****"
	testNtfyToken     = "tk_abcdefghijklmnopqrstuvwxyzabc"
	testWebhookURL    = "https://hooks.example.com/in/secret-path-xyz"
)

var testSigningSecret = strings.Repeat("s", 40)

// fakeStore is an in-memory Store with a log of the methods called on it.
type fakeStore struct {
	destinations map[string]StoredDestination
	routes       map[int64]StoredRoute
	nextDestID   int64
	nextRouteID  int64
	calls        []string
	deleteErr    error
	// returnUnordered makes GetDestinationsByID ignore the name-order contract.
	returnUnordered bool
	// failures maps a recorded call name to the error that call returns.
	failures map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		destinations: map[string]StoredDestination{},
		routes:       map[int64]StoredRoute{},
	}
}

func (store *fakeStore) record(call string) { store.calls = append(store.calls, call) }

func (store *fakeStore) InsertDestination(_ context.Context, destination StoredDestination) (StoredDestination, error) {
	store.record("InsertDestination")
	if _, exists := store.destinations[destination.Name]; exists {
		return StoredDestination{}, ErrDestinationExists
	}
	store.nextDestID++
	destination.ID = store.nextDestID
	destination.CreatedAt = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	destination.UpdatedAt = destination.CreatedAt
	store.destinations[destination.Name] = destination
	return destination, nil
}

func (store *fakeStore) sortedDestinations() []StoredDestination {
	destinations := []StoredDestination{}
	for _, destination := range store.destinations {
		destinations = append(destinations, destination)
	}
	slices.SortFunc(destinations, func(left, right StoredDestination) int { return strings.Compare(left.Name, right.Name) })
	return destinations
}

func (store *fakeStore) ListDestinations(context.Context) ([]StoredDestination, error) {
	store.record("ListDestinations")
	if failure := store.failures["ListDestinations"]; failure != nil {
		return nil, failure
	}
	return store.sortedDestinations(), nil
}

func (store *fakeStore) GetDestination(_ context.Context, name string) (StoredDestination, error) {
	store.record("GetDestination")
	if failure := store.failures["GetDestination"]; failure != nil {
		return StoredDestination{}, failure
	}
	destination, found := store.destinations[name]
	if !found {
		return StoredDestination{}, ErrDestinationNotFound
	}
	return destination, nil
}

func (store *fakeStore) GetDestinationsByID(_ context.Context, ids []int64) ([]StoredDestination, error) {
	store.record("GetDestinationsByID")
	if failure := store.failures["GetDestinationsByID"]; failure != nil {
		return nil, failure
	}
	destinations := []StoredDestination{}
	for _, destination := range store.sortedDestinations() {
		if slices.Contains(ids, destination.ID) {
			destinations = append(destinations, destination)
		}
	}
	if store.returnUnordered {
		// Id order, not name order: proves the service orders the result itself.
		slices.SortFunc(destinations, func(left, right StoredDestination) int { return int(left.ID - right.ID) })
	}
	return destinations, nil
}

func (store *fakeStore) UpdateDestination(
	_ context.Context, name string, mutate func(StoredDestination) (StoredDestination, error),
) (StoredDestination, error) {
	store.record("UpdateDestination")
	current, found := store.destinations[name]
	if !found {
		return StoredDestination{}, ErrDestinationNotFound
	}
	mutated, mutateErr := mutate(current)
	if mutateErr != nil {
		return StoredDestination{}, mutateErr
	}
	mutated.ID, mutated.Name, mutated.Type = current.ID, current.Name, current.Type
	store.destinations[name] = mutated
	return mutated, nil
}

func (store *fakeStore) DeleteDestination(_ context.Context, name string) (bool, error) {
	store.record("DeleteDestination")
	if store.deleteErr != nil {
		return false, store.deleteErr
	}
	_, existed := store.destinations[name]
	delete(store.destinations, name)
	return existed, nil
}

func (store *fakeStore) ListDestinationsByOwner(_ context.Context, appID string) ([]StoredDestination, error) {
	store.record("ListDestinationsByOwner")
	if failure := store.failures["ListDestinationsByOwner"]; failure != nil {
		return nil, failure
	}
	owned := []StoredDestination{}
	for _, destination := range store.sortedDestinations() {
		if ownedByApp(destination, appID) {
			owned = append(owned, destination)
		}
	}
	return owned, nil
}

func (store *fakeStore) DeleteDestinationOwnedBy(_ context.Context, name, appID string) (bool, error) {
	store.record("DeleteDestinationOwnedBy")
	if store.deleteErr != nil {
		return false, store.deleteErr
	}
	destination, found := store.destinations[name]
	if !found || !ownedByApp(destination, appID) {
		return false, nil
	}
	delete(store.destinations, name)
	return true, nil
}

// resolveNames mirrors PGStore's resolveDestinationIDs: a name that does not
// exist, or whose destination the linking app pattern may not use, is unknown.
func (store *fakeStore) resolveNames(names []string, appPattern string) ([]int64, error) {
	ids := []int64{}
	for _, name := range names {
		destination, found := store.destinations[name]
		if !found || !destination.UsableBy(appPattern) {
			return nil, ErrUnknownDestination
		}
		ids = append(ids, destination.ID)
	}
	return ids, nil
}

func (store *fakeStore) InsertRoute(_ context.Context, route StoredRoute) (StoredRoute, error) {
	store.record("InsertRoute")
	if failure := store.failures["InsertRoute"]; failure != nil {
		return StoredRoute{}, failure
	}
	ids, resolveErr := store.resolveNames(route.DestinationNames, route.AppPattern)
	if resolveErr != nil {
		return StoredRoute{}, resolveErr
	}
	store.nextRouteID++
	route.ID = store.nextRouteID
	route.DestinationIDs = ids
	route.CreatedAt = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	route.UpdatedAt = route.CreatedAt
	store.routes[route.ID] = route
	return route, nil
}

func (store *fakeStore) sortedRoutes(enabledOnly bool) []StoredRoute {
	routes := []StoredRoute{}
	for _, route := range store.routes {
		if !enabledOnly || route.Enabled {
			routes = append(routes, route)
		}
	}
	slices.SortFunc(routes, func(left, right StoredRoute) int { return int(left.ID - right.ID) })
	return routes
}

func (store *fakeStore) ListRoutes(context.Context) ([]StoredRoute, error) {
	store.record("ListRoutes")
	if failure := store.failures["ListRoutes"]; failure != nil {
		return nil, failure
	}
	return store.sortedRoutes(false), nil
}

func (store *fakeStore) GetRoute(_ context.Context, id int64) (StoredRoute, error) {
	store.record("GetRoute")
	if failure := store.failures["GetRoute"]; failure != nil {
		return StoredRoute{}, failure
	}
	route, found := store.routes[id]
	if !found {
		return StoredRoute{}, ErrRouteNotFound
	}
	return route, nil
}

func (store *fakeStore) UpdateRoute(
	_ context.Context, id int64, mutate func(StoredRoute) (StoredRoute, error),
) (StoredRoute, error) {
	store.record("UpdateRoute")
	if failure := store.failures["UpdateRoute"]; failure != nil {
		return StoredRoute{}, failure
	}
	current, found := store.routes[id]
	if !found {
		return StoredRoute{}, ErrRouteNotFound
	}
	mutated, mutateErr := mutate(current)
	if mutateErr != nil {
		return StoredRoute{}, mutateErr
	}
	ids, resolveErr := store.resolveNames(mutated.DestinationNames, mutated.AppPattern)
	if resolveErr != nil {
		return StoredRoute{}, resolveErr
	}
	mutated.ID, mutated.DestinationIDs = id, ids
	store.routes[id] = mutated
	return mutated, nil
}

func (store *fakeStore) DeleteRoute(_ context.Context, id int64) (bool, error) {
	store.record("DeleteRoute")
	_, existed := store.routes[id]
	delete(store.routes, id)
	return existed, nil
}

func (store *fakeStore) ListRoutesByOwner(_ context.Context, appID string) ([]StoredRoute, error) {
	store.record("ListRoutesByOwner")
	if failure := store.failures["ListRoutesByOwner"]; failure != nil {
		return nil, failure
	}
	owned := []StoredRoute{}
	for _, route := range store.sortedRoutes(false) {
		if routeOwnedByApp(route, appID) {
			owned = append(owned, route)
		}
	}
	return owned, nil
}

func (store *fakeStore) DeleteRouteOwnedBy(_ context.Context, id int64, appID string) (bool, error) {
	store.record("DeleteRouteOwnedBy")
	if failure := store.failures["DeleteRouteOwnedBy"]; failure != nil {
		return false, failure
	}
	route, found := store.routes[id]
	if !found || !routeOwnedByApp(route, appID) {
		return false, nil
	}
	delete(store.routes, id)
	return true, nil
}

func (store *fakeStore) ListEnabledRoutes(context.Context) ([]StoredRoute, error) {
	store.record("ListEnabledRoutes")
	if failure := store.failures["ListEnabledRoutes"]; failure != nil {
		return nil, failure
	}
	return store.sortedRoutes(true), nil
}

func newTestSealer(t *testing.T) *seal.Sealer {
	t.Helper()
	sealer, err := seal.New(bytes.Repeat([]byte{7}, seal.KeySize))
	if err != nil {
		t.Fatalf("seal.New: %v", err)
	}
	return sealer
}

type serviceFixture struct {
	service *Service
	store   *fakeStore
	sealer  *seal.Sealer
	logs    *bytes.Buffer
}

func newServiceFixture(t *testing.T) serviceFixture {
	t.Helper()
	store := newFakeStore()
	sealer := newTestSealer(t)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	return serviceFixture{service: NewService(store, sealer, logger), store: store, sealer: sealer, logs: logs}
}

func (fixture serviceFixture) openSecrets(t *testing.T, name string) map[string]string {
	t.Helper()
	stored, found := fixture.store.destinations[name]
	if !found {
		t.Fatalf("destination %q not in store", name)
	}
	plaintext, err := fixture.sealer.Open(stored.SealedSecrets, []byte("delivery-api/destination/v1:"+name))
	if err != nil {
		t.Fatalf("open sealed secrets of %q: %v", name, err)
	}
	secrets := map[string]string{}
	if err := json.Unmarshal(plaintext, &secrets); err != nil {
		t.Fatalf("unmarshal secrets: %v", err)
	}
	return secrets
}

func (fixture serviceFixture) createDiscord(t *testing.T, name string) DestinationView {
	t.Helper()
	view, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: name, Type: TypeDiscord, Secrets: map[string]string{"webhook_url": testDiscordURL},
	})
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return view
}

func (fixture serviceFixture) createWebhook(t *testing.T, name string) DestinationView {
	t.Helper()
	view, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: name, Type: TypeWebhook,
		Secrets: map[string]string{"url": testWebhookURL, "signing_secret": testSigningSecret},
	})
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return view
}

func (fixture serviceFixture) createRoute(t *testing.T, appPattern, channelPattern string, destinations ...string) RouteView {
	t.Helper()
	view, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: appPattern, ChannelPattern: channelPattern, Destinations: destinations,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	return view
}

func TestNewServiceNilLoggerDefaults(t *testing.T) {
	service := NewService(newFakeStore(), newTestSealer(t), nil)
	if service.logger != slog.Default() {
		t.Fatal("nil logger was not defaulted to slog.Default()")
	}
}

func TestCreateDestinationSealsAndMasks(t *testing.T) {
	fixture := newServiceFixture(t)
	view := fixture.createDiscord(t, "ops_alerts")

	if view.Name != "ops_alerts" || view.Type != TypeDiscord || !view.Enabled || view.DisabledReason != nil {
		t.Fatalf("unexpected view: %+v", view)
	}
	if view.Secrets["webhook_url"] != testDiscordMasked {
		t.Fatalf("secrets not masked: %v", view.Secrets)
	}
	if view.OwnerKind != OwnerOperator || view.OwnerAppID != nil {
		t.Fatalf("admin-created destination owner = %q/%v, want operator/nil", view.OwnerKind, view.OwnerAppID)
	}
	if stored := fixture.store.destinations["ops_alerts"]; stored.OwnerKind != OwnerOperator || stored.OwnerAppID != nil {
		t.Fatalf("stored destination owner = %q/%v, want operator/nil", stored.OwnerKind, stored.OwnerAppID)
	}
	if view.Config == nil || len(view.Config) != 0 {
		t.Fatalf("config must be an empty non-nil map, got %#v", view.Config)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(encoded), "abcDEF") || !strings.Contains(string(encoded), `"config":{}`) {
		t.Fatalf("view JSON leaks the secret or lacks an empty config: %s", encoded)
	}

	stored := fixture.store.destinations["ops_alerts"]
	if bytes.Contains(stored.SealedSecrets, []byte("abcDEF")) || bytes.Contains(stored.SealedSecrets, []byte("discord.com")) {
		t.Fatal("stored blob contains the plaintext URL")
	}
	if got := fixture.openSecrets(t, "ops_alerts"); got["webhook_url"] != testDiscordURL || len(got) != 1 {
		t.Fatalf("opened secrets = %v", got)
	}
	if _, err := fixture.sealer.Open(stored.SealedSecrets, []byte("delivery-api/destination/v1:other")); !errors.Is(err, seal.ErrDecrypt) {
		t.Fatalf("blob opened under the wrong AAD: %v", err)
	}
}

func TestCreateDestinationNormalisesNilMaps(t *testing.T) {
	fixture := newServiceFixture(t)
	view, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.Secrets == nil || len(view.Secrets) != 0 {
		t.Fatalf("secrets must be an empty non-nil map, got %#v", view.Secrets)
	}
	if got := fixture.openSecrets(t, "phone"); len(got) != 0 {
		t.Fatalf("opened secrets = %v", got)
	}
	if string(mustMarshal(t, fixture.store.destinations["phone"].Config)) != `{"topic":"alerts"}` {
		t.Fatal("config not stored")
	}
}

func TestCreateDestinationEnabledFalse(t *testing.T) {
	fixture := newServiceFixture(t)
	disabled := false
	view, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "phone", Type: TypeNtfy, Enabled: &disabled, Config: map[string]string{"topic": "alerts"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if view.Enabled {
		t.Fatal("destination created enabled despite Enabled=false")
	}
}

func TestCreateDestinationValidationLeavesStoreUntouched(t *testing.T) {
	tests := map[string]DestinationInput{
		"bad name":      {Name: "Bad Name", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"}},
		"bad type":      {Name: "ops", Type: "sms"},
		"bad fields":    {Name: "ops", Type: TypeDiscord, Secrets: map[string]string{"webhook_url": "http://discord.com/x"}},
		"unknown field": {Name: "ops", Type: TypeNtfy, Config: map[string]string{"topic": "alerts", "extra": "x"}},
	}
	for label, input := range tests {
		t.Run(label, func(t *testing.T) {
			fixture := newServiceFixture(t)
			_, err := fixture.service.CreateDestination(context.Background(), input)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if len(fixture.store.calls) != 0 {
				t.Fatalf("store was called: %v", fixture.store.calls)
			}
		})
	}
}

func TestCreateDestinationDuplicate(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	_, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "ops", Type: TypeDiscord, Secrets: map[string]string{"webhook_url": testDiscordURL},
	})
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("want ErrDestinationExists, got %v", err)
	}
}

func TestGetAndListReturnMaskedViews(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createWebhook(t, "zeta")
	fixture.createDiscord(t, "alpha")

	got, err := fixture.service.GetDestination(context.Background(), "zeta")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Secrets["url"] != "https://hooks.example.com/****" || got.Secrets["signing_secret"] != "****" {
		t.Fatalf("get secrets not masked: %v", got.Secrets)
	}

	listed, err := fixture.service.ListDestinations(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 || listed[0].Name != "alpha" || listed[1].Name != "zeta" {
		t.Fatalf("unexpected list: %+v", listed)
	}
	encoded := string(mustMarshal(t, listed))
	for _, leaked := range []string{"abcDEF", "secret-path-xyz", testSigningSecret} {
		if strings.Contains(encoded, leaked) {
			t.Fatalf("list leaks %q: %s", leaked, encoded)
		}
	}
}

func TestListDestinationsEmptyIsNonNil(t *testing.T) {
	fixture := newServiceFixture(t)
	listed, err := fixture.service.ListDestinations(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed == nil || len(listed) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", listed)
	}
}

func TestGetDestinationNotFound(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.GetDestination(context.Background(), "missing"); !errors.Is(err, ErrDestinationNotFound) {
		t.Fatalf("want ErrDestinationNotFound, got %v", err)
	}
}

func TestUnopenableBlobStillListsWithErrorMask(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "good")
	fixture.createDiscord(t, "broken")
	broken := fixture.store.destinations["broken"]
	broken.SealedSecrets = bytes.Repeat([]byte{0xFF}, 64)
	fixture.store.destinations["broken"] = broken

	listed, err := fixture.service.ListDestinations(context.Background())
	if err != nil {
		t.Fatalf("list must not fail: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("want 2 rows, got %d", len(listed))
	}
	if got := listed[0].Secrets; len(got) != 1 || got["error"] != "****" {
		t.Fatalf("broken row secrets = %v", got)
	}
	if listed[1].Secrets["webhook_url"] != testDiscordMasked {
		t.Fatalf("good row secrets = %v", listed[1].Secrets)
	}
	logged := fixture.logs.String()
	if !strings.Contains(logged, "broken") || strings.Contains(logged, "abcDEF") {
		t.Fatalf("log must name the destination and omit secrets: %s", logged)
	}
}

func TestUpdateDestinationMergesSecretsPerKey(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createWebhook(t, "hook")
	before := fixture.store.destinations["hook"].SealedSecrets

	newSecret := strings.Repeat("n", 48)
	view, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{
		Secrets: map[string]string{"signing_secret": newSecret},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got := fixture.openSecrets(t, "hook")
	if got["url"] != testWebhookURL || got["signing_secret"] != newSecret {
		t.Fatalf("merged secrets = %v", got)
	}
	if bytes.Equal(before, fixture.store.destinations["hook"].SealedSecrets) {
		t.Fatal("sealed bytes did not change after a secrets patch")
	}
	if view.Secrets["signing_secret"] != "****" || view.Secrets["url"] != "https://hooks.example.com/****" {
		t.Fatalf("view not masked: %v", view.Secrets)
	}
}

func TestUpdateDestinationRevalidatesMergedResult(t *testing.T) {
	tests := map[string]struct {
		name        string
		patch       DestinationPatch
		wantMessage string
	}{
		"bad url": {
			name:        "hook",
			patch:       DestinationPatch{Secrets: map[string]string{"url": "http://insecure.example.com/x"}},
			wantMessage: "invalid secrets.url",
		},
		"key of another type": {
			name:        "hook",
			patch:       DestinationPatch{Secrets: map[string]string{"access_token": testNtfyToken}},
			wantMessage: "unknown field secrets.access_token",
		},
		"config on a type without config": {
			name:        "hook",
			patch:       DestinationPatch{Config: map[string]string{"topic": "alerts"}},
			wantMessage: "unknown field config.topic",
		},
		"previous secret without expiry": {
			name:        "hook",
			patch:       DestinationPatch{Secrets: map[string]string{"signing_secret_previous": strings.Repeat("p", 48)}},
			wantMessage: "secrets.signing_secret_previous and config.previous_secret_expires_at must be set together",
		},
		"expiry without previous secret": {
			name:        "hook",
			patch:       DestinationPatch{Config: map[string]string{"previous_secret_expires_at": "2026-10-20T12:00:00Z"}},
			wantMessage: "secrets.signing_secret_previous and config.previous_secret_expires_at must be set together",
		},
	}
	for label, test := range tests {
		t.Run(label, func(t *testing.T) {
			fixture := newServiceFixture(t)
			fixture.createWebhook(t, "hook")
			before := fixture.store.destinations["hook"]
			_, err := fixture.service.UpdateDestination(context.Background(), test.name, test.patch)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if test.wantMessage != "" && validationErr.Message != test.wantMessage {
				t.Fatalf("message = %q, want %q", validationErr.Message, test.wantMessage)
			}
			after := fixture.store.destinations["hook"]
			if !bytes.Equal(before.SealedSecrets, after.SealedSecrets) || before.Enabled != after.Enabled {
				t.Fatal("a rejected patch changed the stored row")
			}
		})
	}
}

func TestUpdateDestinationUsesStoredTypeForValidation(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	view, err := fixture.service.UpdateDestination(context.Background(), "phone", DestinationPatch{
		Secrets: map[string]string{"access_token": testNtfyToken},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if view.Secrets["access_token"] != "****" || view.Config["topic"] != "alerts" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if got := fixture.openSecrets(t, "phone"); got["access_token"] != testNtfyToken {
		t.Fatalf("opened secrets = %v", got)
	}
}

func TestUpdateDestinationConfigOnlyKeepsSealedBytes(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
		Secrets: map[string]string{"access_token": testNtfyToken},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := fixture.store.destinations["phone"].SealedSecrets
	view, err := fixture.service.UpdateDestination(context.Background(), "phone", DestinationPatch{
		Config: map[string]string{"topic": "other-topic"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if view.Config["topic"] != "other-topic" {
		t.Fatalf("config not updated: %v", view.Config)
	}
	if !bytes.Equal(before, fixture.store.destinations["phone"].SealedSecrets) {
		t.Fatal("a config-only patch resealed the secrets")
	}
}

func TestUpdateDestinationEmptySecretsPatchKeepsSealedBytes(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
		Secrets: map[string]string{"access_token": testNtfyToken},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := fixture.store.destinations["phone"].SealedSecrets
	view, err := fixture.service.UpdateDestination(context.Background(), "phone", DestinationPatch{
		Secrets: map[string]string{},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if view.Secrets["access_token"] != "****" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if !bytes.Equal(before, fixture.store.destinations["phone"].SealedSecrets) {
		t.Fatal("an empty secrets patch resealed the secrets")
	}
}

func TestUpdateDestinationEnabledOnlyNeverTouchesSecrets(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	stored := fixture.store.destinations["ops"]
	reason := "auto-disabled after repeated failures"
	stored.Enabled = false
	stored.DisabledReason = &reason
	stored.SealedSecrets = bytes.Repeat([]byte{0xAB}, 64) // unopenable: proves no open happens
	fixture.store.destinations["ops"] = stored

	enabled := true
	view, err := fixture.service.UpdateDestination(context.Background(), "ops", DestinationPatch{Enabled: &enabled})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	after := fixture.store.destinations["ops"]
	if !after.Enabled || after.DisabledReason != nil || view.DisabledReason != nil || !view.Enabled {
		t.Fatalf("enable did not clear the reason: %+v", after)
	}
	if !bytes.Equal(after.SealedSecrets, stored.SealedSecrets) {
		t.Fatal("an enabled-only patch changed the sealed bytes")
	}
}

func TestUpdateDestinationDisable(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	disabled := false
	view, err := fixture.service.UpdateDestination(context.Background(), "ops", DestinationPatch{Enabled: &disabled})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if view.Enabled || fixture.store.destinations["ops"].Enabled {
		t.Fatal("destination still enabled")
	}
}

func TestUpdateDestinationRotationFieldsSetTogether(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createWebhook(t, "hook")
	previous := strings.Repeat("p", 48)
	expiry := "2026-10-20T12:00:00Z"

	view, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{
		Config:  map[string]string{"previous_secret_expires_at": expiry},
		Secrets: map[string]string{"signing_secret_previous": previous},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got := fixture.openSecrets(t, "hook")
	if got["signing_secret_previous"] != previous || got["signing_secret"] != testSigningSecret || got["url"] != testWebhookURL {
		t.Fatalf("opened secrets = %v", got)
	}
	if view.Config["previous_secret_expires_at"] != expiry || view.Secrets["signing_secret_previous"] != "****" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if stored := fixture.store.destinations["hook"].Config["previous_secret_expires_at"]; stored != expiry {
		t.Fatalf("stored expiry = %q, want %q", stored, expiry)
	}
}

func TestUpdateDestinationEmptyConfigPatchKeepsRotationPair(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createWebhook(t, "hook")
	if _, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{
		Config:  map[string]string{"previous_secret_expires_at": "2026-10-20T12:00:00Z"},
		Secrets: map[string]string{"signing_secret_previous": strings.Repeat("p", 48)},
	}); err != nil {
		t.Fatalf("set rotation pair: %v", err)
	}
	before := fixture.store.destinations["hook"]

	for label, patchConfig := range map[string]map[string]string{"empty": {}, "nil": nil} {
		view, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{
			Config:  patchConfig,
			Secrets: map[string]string{},
		})
		if err != nil {
			t.Fatalf("%s config patch: %v", label, err)
		}
		after := fixture.store.destinations["hook"]
		if !maps.Equal(before.Config, after.Config) || !bytes.Equal(before.SealedSecrets, after.SealedSecrets) {
			t.Fatalf("%s config patch changed the stored row", label)
		}
		if view.Config["previous_secret_expires_at"] != "2026-10-20T12:00:00Z" {
			t.Fatalf("%s config patch dropped the expiry: %v", label, view.Config)
		}
	}
}

func TestUpdateDestinationConfigMergesPerKey(t *testing.T) {
	expiry := "2026-10-20T12:00:00Z"
	previous := strings.Repeat("p", 48)
	tests := map[string]struct {
		setup       func(t *testing.T, fixture serviceFixture)
		name        string
		patch       DestinationPatch
		wantConfig  map[string]string
		wantMessage string
	}{
		"partial config patch keeps the other keys": {
			setup: func(t *testing.T, fixture serviceFixture) {
				if _, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
					Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
				}); err != nil {
					t.Fatalf("create: %v", err)
				}
			},
			name:       "phone",
			patch:      DestinationPatch{Config: map[string]string{"topic": "renamed"}},
			wantConfig: map[string]string{"topic": "renamed"},
		},
		"webhook mid-rotation patched with the expiry key only keeps the previous secret": {
			setup: func(t *testing.T, fixture serviceFixture) {
				fixture.createWebhook(t, "hook")
				if _, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{
					Config:  map[string]string{"previous_secret_expires_at": expiry},
					Secrets: map[string]string{"signing_secret_previous": previous},
				}); err != nil {
					t.Fatalf("set rotation pair: %v", err)
				}
			},
			name:       "hook",
			patch:      DestinationPatch{Config: map[string]string{"previous_secret_expires_at": "2026-10-21T12:00:00Z"}},
			wantConfig: map[string]string{"previous_secret_expires_at": "2026-10-21T12:00:00Z"},
		},
		"unknown key is still rejected": {
			setup:       func(t *testing.T, fixture serviceFixture) { fixture.createWebhook(t, "hook") },
			name:        "hook",
			patch:       DestinationPatch{Config: map[string]string{"bogus": "x"}},
			wantMessage: "unknown field config.bogus",
		},
		"merged result breaking the pair rule is rejected": {
			setup:       func(t *testing.T, fixture serviceFixture) { fixture.createWebhook(t, "hook") },
			name:        "hook",
			patch:       DestinationPatch{Config: map[string]string{"previous_secret_expires_at": expiry}},
			wantMessage: "secrets.signing_secret_previous and config.previous_secret_expires_at must be set together",
		},
	}
	for label, test := range tests {
		t.Run(label, func(t *testing.T) {
			fixture := newServiceFixture(t)
			test.setup(t, fixture)
			before := fixture.store.destinations[test.name]

			view, err := fixture.service.UpdateDestination(context.Background(), test.name, test.patch)

			after := fixture.store.destinations[test.name]
			if test.wantMessage != "" {
				var validationErr *ValidationError
				if !errors.As(err, &validationErr) || validationErr.Message != test.wantMessage {
					t.Fatalf("err = %v, want validation error %q", err, test.wantMessage)
				}
				if !maps.Equal(before.Config, after.Config) || !bytes.Equal(before.SealedSecrets, after.SealedSecrets) {
					t.Fatal("a rejected patch changed the stored row")
				}
				return
			}
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if !maps.Equal(view.Config, test.wantConfig) || !maps.Equal(after.Config, test.wantConfig) {
				t.Fatalf("config = %v (stored %v), want %v", view.Config, after.Config, test.wantConfig)
			}
		})
	}
}

func TestUpdateDestinationConfigPatchKeepsUnpatchedKeys(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createWebhook(t, "hook")
	if _, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{
		Config:  map[string]string{"previous_secret_expires_at": "2026-10-20T12:00:00Z"},
		Secrets: map[string]string{"signing_secret_previous": strings.Repeat("p", 48)},
	}); err != nil {
		t.Fatalf("set rotation pair: %v", err)
	}
	// An unrelated secrets-only patch must keep the config pair too.
	if _, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{
		Secrets: map[string]string{"url": "https://hooks.example.com/in/other"},
	}); err != nil {
		t.Fatalf("secrets patch: %v", err)
	}
	if got := fixture.store.destinations["hook"].Config["previous_secret_expires_at"]; got != "2026-10-20T12:00:00Z" {
		t.Fatalf("expiry = %q after an unrelated patch", got)
	}
}

func TestDisableDestination(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	stored := fixture.store.destinations["ops"]
	stored.SealedSecrets = bytes.Repeat([]byte{0xAB}, 64) // unopenable: proves no open or reseal happens
	fixture.store.destinations["ops"] = stored

	if err := fixture.service.DisableDestination(context.Background(), "ops", stored.ID, "webhook returned 410 Gone"); err != nil {
		t.Fatalf("disable: %v", err)
	}

	after := fixture.store.destinations["ops"]
	if after.Enabled {
		t.Fatal("destination still enabled")
	}
	if after.DisabledReason == nil || *after.DisabledReason != "webhook returned 410 Gone" {
		t.Fatalf("disabled reason = %v", after.DisabledReason)
	}
	if !bytes.Equal(after.SealedSecrets, stored.SealedSecrets) {
		t.Fatal("disabling changed the sealed bytes")
	}
}

func TestDisableDestinationMissing(t *testing.T) {
	fixture := newServiceFixture(t)

	err := fixture.service.DisableDestination(context.Background(), "missing", 1, "gone")

	// Identity comparison is deliberate: the contract is that the sentinel is returned unchanged (not wrapped).
	if err != ErrDestinationNotFound {
		t.Fatalf("want ErrDestinationNotFound returned unchanged, got %v", err)
	}
}

// A 410 from the old endpoint must not disable a destination that was deleted and re-created under the same name
// while the attempt was in flight.
func TestDisableDestinationLeavesAReplacedDestinationAlone(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	resolvedID := fixture.store.destinations["ops"].ID
	if _, err := fixture.service.DeleteDestination(context.Background(), "ops"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	fixture.createDiscord(t, "ops")
	replacement := fixture.store.destinations["ops"]
	if replacement.ID == resolvedID {
		t.Fatalf("test setup: the re-created destination kept id %d", resolvedID)
	}

	err := fixture.service.DisableDestination(context.Background(), "ops", resolvedID, "webhook returned 410 Gone")

	// Identity comparison is deliberate: the contract is that the sentinel is returned unchanged (not wrapped).
	if err != ErrDestinationNotFound {
		t.Fatalf("want ErrDestinationNotFound returned unchanged, got %v", err)
	}
	after := fixture.store.destinations["ops"]
	if !after.Enabled || after.DisabledReason != nil {
		t.Fatalf("the replacement was disabled: enabled=%v reason=%v", after.Enabled, after.DisabledReason)
	}
}

func TestUpdateDestinationErrors(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.UpdateDestination(context.Background(), "missing", DestinationPatch{}); !errors.Is(err, ErrDestinationNotFound) {
		t.Fatalf("want ErrDestinationNotFound, got %v", err)
	}

	fixture.createDiscord(t, "ops")
	broken := fixture.store.destinations["ops"]
	broken.SealedSecrets = bytes.Repeat([]byte{0xAB}, 64)
	fixture.store.destinations["ops"] = broken
	_, err := fixture.service.UpdateDestination(context.Background(), "ops", DestinationPatch{
		Secrets: map[string]string{"webhook_url": testDiscordURL},
	})
	if err == nil || !strings.Contains(err.Error(), "ops") || strings.Contains(err.Error(), "abcDEF") {
		t.Fatalf("want an error naming the destination only, got %v", err)
	}
	if !errors.Is(err, seal.ErrDecrypt) && !errors.Is(err, seal.ErrUnsupportedVersion) {
		t.Fatalf("error does not wrap the seal sentinel: %v", err)
	}
}

func TestDeleteDestination(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")

	existed, err := fixture.service.DeleteDestination(context.Background(), "ops")
	if err != nil || !existed {
		t.Fatalf("delete existing: existed=%v err=%v", existed, err)
	}
	existed, err = fixture.service.DeleteDestination(context.Background(), "ops")
	if err != nil || existed {
		t.Fatalf("delete absent must be idempotent: existed=%v err=%v", existed, err)
	}

	fixture.store.deleteErr = ErrDestinationInUse
	if _, err = fixture.service.DeleteDestination(context.Background(), "ops"); !errors.Is(err, ErrDestinationInUse) {
		t.Fatalf("want ErrDestinationInUse, got %v", err)
	}
}

func TestCreateRoute(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	view := fixture.createRoute(t, "urls4irl", "backup_*", "ops")

	if view.ID == 0 || view.AppPattern != "urls4irl" || view.ChannelPattern != "backup_*" || !view.Enabled {
		t.Fatalf("unexpected view: %+v", view)
	}
	if len(view.Destinations) != 1 || view.Destinations[0] != "ops" {
		t.Fatalf("destinations = %v", view.Destinations)
	}
	if view.OwnerKind != OwnerOperator || view.OwnerAppID != nil {
		t.Fatalf("admin-created route owner = %q/%v, want operator/nil", view.OwnerKind, view.OwnerAppID)
	}
	if stored := fixture.store.routes[view.ID]; stored.OwnerKind != OwnerOperator || stored.OwnerAppID != nil {
		t.Fatalf("stored route owner = %q/%v, want operator/nil", stored.OwnerKind, stored.OwnerAppID)
	}
}

func TestCreateRouteValidationLeavesStoreUntouched(t *testing.T) {
	tests := map[string]RouteInput{
		"bad app pattern":     {AppPattern: "Bad App", ChannelPattern: "*", Destinations: []string{"ops"}},
		"reserved app":        {AppPattern: "everyone", ChannelPattern: "*", Destinations: []string{"ops"}},
		"bad channel pattern": {AppPattern: "*", ChannelPattern: "a*b", Destinations: []string{"ops"}},
		"no destinations":     {AppPattern: "*", ChannelPattern: "*"},
		"duplicate names":     {AppPattern: "*", ChannelPattern: "*", Destinations: []string{"ops", "ops"}},
		"bad destination":     {AppPattern: "*", ChannelPattern: "*", Destinations: []string{"Bad Name"}},
	}
	for label, input := range tests {
		t.Run(label, func(t *testing.T) {
			fixture := newServiceFixture(t)
			_, err := fixture.service.CreateRoute(context.Background(), input)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if len(fixture.store.calls) != 0 {
				t.Fatalf("store was called: %v", fixture.store.calls)
			}
		})
	}
}

func TestCreateRouteStoreErrorsPassThrough(t *testing.T) {
	fixture := newServiceFixture(t)
	_, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: "*", ChannelPattern: "*", Destinations: []string{"ghost"},
	})
	if !errors.Is(err, ErrUnknownDestination) {
		t.Fatalf("want ErrUnknownDestination, got %v", err)
	}
}

func TestCreateRouteEnabledFalse(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	disabled := false
	view, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: "*", ChannelPattern: "*", Destinations: []string{"ops"}, Enabled: &disabled,
	})
	if err != nil || view.Enabled {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

func TestGetAndListRoutes(t *testing.T) {
	fixture := newServiceFixture(t)
	empty, err := fixture.service.ListRoutes(context.Background())
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v err=%v", empty, err)
	}

	fixture.createDiscord(t, "ops")
	created := fixture.createRoute(t, "*", "*", "ops")
	got, err := fixture.service.GetRoute(context.Background(), created.ID)
	if err != nil || got.ID != created.ID || len(got.Destinations) != 1 {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	listed, err := fixture.service.ListRoutes(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %+v err=%v", listed, err)
	}
	if _, err = fixture.service.GetRoute(context.Background(), 999); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("want ErrRouteNotFound, got %v", err)
	}
}

func TestRouteViewNeverHasNilDestinations(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.store.routes[1] = StoredRoute{ID: 1, AppPattern: "*", ChannelPattern: "*", Enabled: true}
	got, err := fixture.service.GetRoute(context.Background(), 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Destinations == nil || !strings.Contains(string(mustMarshal(t, got)), `"destinations":[]`) {
		t.Fatalf("destinations must marshal as []: %#v", got.Destinations)
	}
}

func TestUpdateRoute(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	fixture.createWebhook(t, "hook")
	route := fixture.createRoute(t, "urls4irl", "backup_*", "ops")

	channel := "alerts"
	disabled := false
	view, err := fixture.service.UpdateRoute(context.Background(), route.ID, RoutePatch{
		ChannelPattern: &channel, Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if view.ChannelPattern != "alerts" || view.AppPattern != "urls4irl" || view.Enabled ||
		len(view.Destinations) != 1 || view.Destinations[0] != "ops" {
		t.Fatalf("patch without destinations must keep them: %+v", view)
	}

	app := "*"
	view, err = fixture.service.UpdateRoute(context.Background(), route.ID, RoutePatch{
		AppPattern: &app, Destinations: []string{"hook", "ops"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if view.AppPattern != "*" || len(view.Destinations) != 2 || view.Destinations[0] != "hook" {
		t.Fatalf("unexpected view: %+v", view)
	}
}

func TestUpdateRouteValidatesBeforeStore(t *testing.T) {
	badApp := "Bad App"
	badChannel := "a*b"
	tests := map[string]RoutePatch{
		"bad app":           {AppPattern: &badApp},
		"bad channel":       {ChannelPattern: &badChannel},
		"empty destination": {Destinations: []string{}},
		"bad destination":   {Destinations: []string{"Bad Name"}},
	}
	for label, patch := range tests {
		t.Run(label, func(t *testing.T) {
			fixture := newServiceFixture(t)
			_, err := fixture.service.UpdateRoute(context.Background(), 1, patch)
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if len(fixture.store.calls) != 0 {
				t.Fatalf("store was called: %v", fixture.store.calls)
			}
		})
	}
}

func TestUpdateRouteNotFound(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.UpdateRoute(context.Background(), 42, RoutePatch{}); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("want ErrRouteNotFound, got %v", err)
	}
}

func TestDeleteRoute(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	route := fixture.createRoute(t, "*", "*", "ops")
	existed, err := fixture.service.DeleteRoute(context.Background(), route.ID)
	if err != nil || !existed {
		t.Fatalf("existed=%v err=%v", existed, err)
	}
	existed, err = fixture.service.DeleteRoute(context.Background(), route.ID)
	if err != nil || existed {
		t.Fatalf("existed=%v err=%v", existed, err)
	}
}

func TestResolveNamedDestination(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")

	resolved, err := fixture.service.Resolve(context.Background(), "urls4irl", "ops", "backup_done")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "ops" || resolved[0].Type != TypeDiscord ||
		resolved[0].Secrets["webhook_url"] != testDiscordURL {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved[0].Config == nil {
		t.Fatal("config must be non-nil")
	}
}

func TestResolveNamedMissingAndDisabled(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.Resolve(context.Background(), "urls4irl", "ghost", "c"); !errors.Is(err, ErrUnknownDestination) {
		t.Fatalf("want ErrUnknownDestination, got %v", err)
	}

	fixture.createDiscord(t, "ops")
	disabled := false
	if _, err := fixture.service.UpdateDestination(context.Background(), "ops", DestinationPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := fixture.service.Resolve(context.Background(), "urls4irl", "ops", "c"); !errors.Is(err, ErrDestinationDisabled) {
		t.Fatalf("want ErrDestinationDisabled, got %v", err)
	}
}

func TestResolveByRoutesUnionsDedupesAndOrders(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.store.returnUnordered = true
	fixture.createWebhook(t, "zeta")
	fixture.createDiscord(t, "alpha")
	fixture.createDiscord(t, "off")
	fixture.createDiscord(t, "unrelated")
	disabled := false
	if _, err := fixture.service.UpdateDestination(context.Background(), "off", DestinationPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable destination: %v", err)
	}
	fixture.createRoute(t, "urls4irl", "backup_*", "zeta", "alpha")
	fixture.createRoute(t, "*", "*", "alpha", "off")
	disabledRoute, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: "urls4irl", ChannelPattern: "*", Destinations: []string{"unrelated"}, Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("create disabled route: %v", err)
	}
	if disabledRoute.Enabled {
		t.Fatal("route should be disabled")
	}
	fixture.createRoute(t, "otherapp", "*", "unrelated")

	resolved, err := fixture.service.Resolve(context.Background(), "urls4irl", "", "backup_done")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	names := []string{}
	for _, destination := range resolved {
		names = append(names, destination.Name)
	}
	if strings.Join(names, ",") != "alpha,zeta" {
		t.Fatalf("resolved names = %v", names)
	}
	if resolved[1].Secrets["url"] != testWebhookURL || resolved[1].Secrets["signing_secret"] != testSigningSecret {
		t.Fatalf("secrets not unsealed: %v", resolved[1].Secrets)
	}
}

func TestResolveNoMatchReturnsEmptyNonNil(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	fixture.createRoute(t, "otherapp", "*", "ops")

	resolved, err := fixture.service.Resolve(context.Background(), "urls4irl", "", "backup_done")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved == nil || len(resolved) != 0 {
		t.Fatalf("want empty non-nil slice, got %#v", resolved)
	}
}

// createAppADestination creates the Discord destination "app_a_hook" and marks
// it as owned by app_a, the way a direct SQL insert would, since no API can
// create an app-owned destination yet.
func (fixture serviceFixture) createAppADestination(t *testing.T) {
	t.Helper()
	fixture.createDiscord(t, "app_a_hook")
	ownerApp := "app_a"
	stored := fixture.store.destinations["app_a_hook"]
	stored.OwnerKind = OwnerApp
	stored.OwnerAppID = &ownerApp
	fixture.store.destinations["app_a_hook"] = stored
}

func TestResolveNamedAppOwnedDestinationOnlyForItsOwner(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createAppADestination(t)
	fixture.createDiscord(t, "shared")

	owned, err := fixture.service.Resolve(context.Background(), "app_a", "app_a_hook", "c")
	if err != nil || len(owned) != 1 || owned[0].Name != "app_a_hook" {
		t.Fatalf("owner resolve = %+v, %v", owned, err)
	}
	for _, other := range []string{"app_b", "", "*", "app_a*"} {
		if _, err := fixture.service.Resolve(context.Background(), other, "app_a_hook", "c"); !errors.Is(err, ErrUnknownDestination) {
			t.Fatalf("app %q resolving another app's destination: want ErrUnknownDestination, got %v", other, err)
		}
	}
	for _, appID := range []string{"app_a", "app_b"} {
		if shared, err := fixture.service.Resolve(context.Background(), appID, "shared", "c"); err != nil || len(shared) != 1 {
			t.Fatalf("app %q resolving an operator-owned destination = %+v, %v", appID, shared, err)
		}
	}
}

func TestResolveNamedUnusableDestinationReadsAsUnknownEvenWhenDisabled(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createAppADestination(t)
	disabled := false
	if _, err := fixture.service.UpdateDestination(context.Background(), "app_a_hook", DestinationPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	// The disabled state of another app's destination must not leak.
	if _, err := fixture.service.Resolve(context.Background(), "app_b", "app_a_hook", "c"); !errors.Is(err, ErrUnknownDestination) {
		t.Fatalf("other app: want ErrUnknownDestination, got %v", err)
	}
	if _, err := fixture.service.Resolve(context.Background(), "app_a", "app_a_hook", "c"); !errors.Is(err, ErrDestinationDisabled) {
		t.Fatalf("owner: want ErrDestinationDisabled, got %v", err)
	}
}

func TestResolveNamedDeniesAnUnknownOwnerKind(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	stored := fixture.store.destinations["ops"]
	stored.OwnerKind = ""
	fixture.store.destinations["ops"] = stored

	if _, err := fixture.service.Resolve(context.Background(), "app_a", "ops", "c"); !errors.Is(err, ErrUnknownDestination) {
		t.Fatalf("empty owner kind: want ErrUnknownDestination, got %v", err)
	}
}

func TestResolveByRoutesDropsDestinationsTheAppMayNotUseAndLogsThem(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createAppADestination(t)
	fixture.createDiscord(t, "shared")
	fixture.createRoute(t, "*", "*", "shared")
	// A bad row that write-time validation would refuse: an operator "*" route
	// linked to app A's destination, inserted behind the service's back.
	badRoute := fixture.createRoute(t, "app_a", "*", "shared")
	stored := fixture.store.routes[badRoute.ID]
	stored.AppPattern = "*"
	stored.ChannelPattern = "alerts"
	stored.DestinationIDs = []int64{fixture.store.destinations["app_a_hook"].ID, fixture.store.destinations["shared"].ID}
	fixture.store.routes[badRoute.ID] = stored

	resolved, err := fixture.service.Resolve(context.Background(), "app_b", "", "alerts")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Name != "shared" {
		t.Fatalf("app_b resolved %+v, want only the operator-owned destination", resolved)
	}
	logged := fixture.logs.String()
	for _, want := range []string{"level=WARN", "app_a_hook", "route_ids=[" + strconv.FormatInt(badRoute.ID, 10) + "]", "app_id=app_b"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log %q lacks %q", logged, want)
		}
	}

	owner, err := fixture.service.Resolve(context.Background(), "app_a", "", "alerts")
	if err != nil {
		t.Fatalf("resolve for owner: %v", err)
	}
	names := []string{}
	for _, destination := range owner {
		names = append(names, destination.Name)
	}
	if strings.Join(names, ",") != "app_a_hook,shared" {
		t.Fatalf("owner resolved %v, want its own destination too", names)
	}
}

func TestCreateRouteAppOwnedDestinationRule(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createAppADestination(t)

	for _, pattern := range []string{"*", "app_b"} {
		_, err := fixture.service.CreateRoute(context.Background(), RouteInput{
			AppPattern: pattern, ChannelPattern: "*", Destinations: []string{"app_a_hook"},
		})
		if !errors.Is(err, ErrUnknownDestination) {
			t.Fatalf("app_pattern %q: want ErrUnknownDestination, got %v", pattern, err)
		}
	}
	if len(fixture.store.routes) != 0 {
		t.Fatalf("a refused route was stored: %+v", fixture.store.routes)
	}
	view, err := fixture.service.CreateRoute(context.Background(), RouteInput{
		AppPattern: "app_a", ChannelPattern: "*", Destinations: []string{"app_a_hook"},
	})
	if err != nil || view.OwnerKind != OwnerOperator {
		t.Fatalf("operator route for the owner app = %+v, %v", view, err)
	}
}

func TestUpdateRouteAppOwnedDestinationRule(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createAppADestination(t)
	fixture.createDiscord(t, "shared")
	route := fixture.createRoute(t, "app_a", "*", "app_a_hook")

	widened := "*"
	if _, err := fixture.service.UpdateRoute(context.Background(), route.ID, RoutePatch{AppPattern: &widened}); !errors.Is(err, ErrUnknownDestination) {
		t.Fatalf("widening the pattern: want ErrUnknownDestination, got %v", err)
	}
	if stored := fixture.store.routes[route.ID]; stored.AppPattern != "app_a" {
		t.Fatalf("a refused patch changed the stored pattern to %q", stored.AppPattern)
	}

	shared := fixture.createRoute(t, "*", "*", "shared")
	if _, err := fixture.service.UpdateRoute(context.Background(), shared.ID, RoutePatch{Destinations: []string{"shared", "app_a_hook"}}); !errors.Is(err, ErrUnknownDestination) {
		t.Fatalf("adding another app's destination to a * route: want ErrUnknownDestination, got %v", err)
	}
	narrowed := "app_a"
	if _, err := fixture.service.UpdateRoute(context.Background(), shared.ID, RoutePatch{AppPattern: &narrowed, Destinations: []string{"shared", "app_a_hook"}}); err != nil {
		t.Fatalf("narrowing to the owner and adding its destination: %v", err)
	}
}

func TestResolveUnsealFailureNamesDestinationNotSecret(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	fixture.createRoute(t, "*", "*", "ops")

	// A blob sealed for another name does not open under "ops".
	other, err := fixture.sealer.Seal([]byte(`{"webhook_url":"`+testDiscordURL+`"}`), []byte("delivery-api/destination/v1:other"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	moved := fixture.store.destinations["ops"]
	moved.SealedSecrets = other
	fixture.store.destinations["ops"] = moved

	for _, destinationName := range []string{"", "ops"} {
		_, err = fixture.service.Resolve(context.Background(), "urls4irl", destinationName, "c")
		if err == nil || !strings.Contains(err.Error(), "ops") || strings.Contains(err.Error(), "abcDEF") {
			t.Fatalf("name %q: want an error naming the destination only, got %v", destinationName, err)
		}
		if !errors.Is(err, seal.ErrDecrypt) {
			t.Fatalf("name %q: error does not wrap seal.ErrDecrypt: %v", destinationName, err)
		}
	}
}

func TestResolveRejectsSecretsThatAreNotAJSONObject(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	sealed, err := fixture.sealer.Seal([]byte(`["not","an","object"]`), []byte("delivery-api/destination/v1:ops"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	stored := fixture.store.destinations["ops"]
	stored.SealedSecrets = sealed
	fixture.store.destinations["ops"] = stored

	_, err = fixture.service.Resolve(context.Background(), "urls4irl", "ops", "c")
	if err == nil || !strings.Contains(err.Error(), "ops") || strings.Contains(err.Error(), `"not"`) {
		t.Fatalf("want an error naming the destination without payload text, got %v", err)
	}
	if !errors.Is(err, errMalformedSecrets) {
		t.Fatalf("error does not wrap errMalformedSecrets: %v", err)
	}
}

func TestResolveTreatsSealedNullAsEmptySecrets(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	sealed, err := fixture.sealer.Seal([]byte(`null`), []byte("delivery-api/destination/v1:phone"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	stored := fixture.store.destinations["phone"]
	stored.SealedSecrets = sealed
	fixture.store.destinations["phone"] = stored

	resolved, err := fixture.service.Resolve(context.Background(), "urls4irl", "phone", "c")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved[0].Secrets == nil || len(resolved[0].Secrets) != 0 {
		t.Fatalf("want empty non-nil secrets, got %#v", resolved[0].Secrets)
	}
}

func TestResolveSkipsUnneededStoreCalls(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.createDiscord(t, "ops")
	fixture.createRoute(t, "otherapp", "*", "ops")

	fixture.store.calls = nil
	if _, err := fixture.service.Resolve(context.Background(), "urls4irl", "ops", "c"); err != nil {
		t.Fatalf("resolve named: %v", err)
	}
	if strings.Join(fixture.store.calls, ",") != "GetDestination" {
		t.Fatalf("named resolve calls = %v", fixture.store.calls)
	}

	fixture.store.calls = nil
	if _, err := fixture.service.Resolve(context.Background(), "urls4irl", "", "c"); err != nil {
		t.Fatalf("resolve by route: %v", err)
	}
	if strings.Join(fixture.store.calls, ",") != "ListEnabledRoutes" {
		t.Fatalf("no-match resolve calls = %v", fixture.store.calls)
	}
}

func TestServicePropagatesStoreErrorsUnchanged(t *testing.T) {
	storeFailure := errors.New("store unavailable")
	testCases := []struct {
		name   string
		failAt string
		call   func(fixture serviceFixture) error
	}{
		{name: "ListDestinations", failAt: "ListDestinations", call: func(fixture serviceFixture) error {
			_, err := fixture.service.ListDestinations(context.Background())
			return err
		}},
		{name: "ListRoutes", failAt: "ListRoutes", call: func(fixture serviceFixture) error {
			_, err := fixture.service.ListRoutes(context.Background())
			return err
		}},
		{name: "GetRoute", failAt: "GetRoute", call: func(fixture serviceFixture) error {
			_, err := fixture.service.GetRoute(context.Background(), 1)
			return err
		}},
		{name: "GetDestination", failAt: "GetDestination", call: func(fixture serviceFixture) error {
			_, err := fixture.service.GetDestination(context.Background(), "ops")
			return err
		}},
		{name: "Resolve ListEnabledRoutes", failAt: "ListEnabledRoutes", call: func(fixture serviceFixture) error {
			_, err := fixture.service.Resolve(context.Background(), "urls4irl", "", "c")
			return err
		}},
		{name: "Resolve GetDestinationsByID", failAt: "GetDestinationsByID", call: func(fixture serviceFixture) error {
			_, err := fixture.service.Resolve(context.Background(), "urls4irl", "", "c")
			return err
		}},
		{name: "Resolve named GetDestination", failAt: "GetDestination", call: func(fixture serviceFixture) error {
			_, err := fixture.service.Resolve(context.Background(), "urls4irl", "ops", "c")
			return err
		}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(subTest *testing.T) {
			fixture := newServiceFixture(subTest)
			fixture.createDiscord(subTest, "ops")
			fixture.createRoute(subTest, "urls4irl", "*", "ops")
			fixture.store.failures = map[string]error{testCase.failAt: storeFailure}

			err := testCase.call(fixture)

			if err != storeFailure {
				subTest.Fatalf("error = %v, want the store error returned unchanged", err)
			}
			if errors.Is(err, ErrUnknownDestination) {
				subTest.Fatalf("error = %v, must not be mapped to ErrUnknownDestination", err)
			}
		})
	}
}

func TestUpdateDestinationConfigAndSecretsTogether(t *testing.T) {
	fixture := newServiceFixture(t)
	if _, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
		Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	view, err := fixture.service.UpdateDestination(context.Background(), "phone", DestinationPatch{
		Config:  map[string]string{"topic": "other-topic"},
		Secrets: map[string]string{"access_token": testNtfyToken},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if view.Config["topic"] != "other-topic" || view.Secrets["access_token"] != "****" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if got := fixture.openSecrets(t, "phone"); got["access_token"] != testNtfyToken {
		t.Fatalf("opened secrets = %v", got)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}

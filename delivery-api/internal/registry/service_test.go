package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
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

func (store *fakeStore) resolveNames(names []string) ([]int64, error) {
	ids := []int64{}
	for _, name := range names {
		destination, found := store.destinations[name]
		if !found {
			return nil, ErrUnknownDestination
		}
		ids = append(ids, destination.ID)
	}
	return ids, nil
}

func (store *fakeStore) InsertRoute(_ context.Context, route StoredRoute) (StoredRoute, error) {
	store.record("InsertRoute")
	ids, resolveErr := store.resolveNames(route.DestinationNames)
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
	current, found := store.routes[id]
	if !found {
		return StoredRoute{}, ErrRouteNotFound
	}
	mutated, mutateErr := mutate(current)
	if mutateErr != nil {
		return StoredRoute{}, mutateErr
	}
	ids, resolveErr := store.resolveNames(mutated.DestinationNames)
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
		name  string
		patch DestinationPatch
	}{
		"bad url": {
			name:  "hook",
			patch: DestinationPatch{Secrets: map[string]string{"url": "http://insecure.example.com/x"}},
		},
		"key of another type": {
			name:  "hook",
			patch: DestinationPatch{Secrets: map[string]string{"access_token": testNtfyToken}},
		},
		"config on a type without config": {
			name:  "hook",
			patch: DestinationPatch{Config: map[string]string{"topic": "alerts"}},
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

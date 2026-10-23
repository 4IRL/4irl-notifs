package registry

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"strings"
	"testing"
)

const (
	appA = "app_a"
	appB = "app_b"
)

// stubURLChecker is a scripted DestinationURLChecker. It logs each call into the fake store's call log, so a
// test can assert the check ran before any store write or row lock.
type stubURLChecker struct {
	store   *fakeStore
	err     error
	calls   []stubCheckCall
	onCheck func()
}

type stubCheckCall struct {
	destinationType string
	secrets         map[string]string
}

func (checker *stubURLChecker) CheckDestinationSecrets(
	_ context.Context, destinationType string, secrets map[string]string,
) error {
	checker.store.record("CheckDestinationSecrets")
	checker.calls = append(checker.calls, stubCheckCall{destinationType: destinationType, secrets: maps.Clone(secrets)})
	if checker.onCheck != nil {
		checker.onCheck()
	}
	return checker.err
}

// newAppFixture is a service fixture whose service has a stub URL checker.
func newAppFixture(testInstance *testing.T) (serviceFixture, *stubURLChecker) {
	testInstance.Helper()
	fixture := newServiceFixture(testInstance)
	checker := &stubURLChecker{store: fixture.store}
	fixture.service = NewService(fixture.store, fixture.sealer,
		slog.New(slog.NewTextHandler(fixture.logs, nil)), WithURLChecker(checker))
	return fixture, checker
}

func webhookInput(name string) DestinationInput {
	return DestinationInput{
		Name: name, Type: TypeWebhook,
		Secrets: map[string]string{"url": testWebhookURL, "signing_secret": testSigningSecret},
	}
}

func (fixture serviceFixture) createAppWebhook(testInstance *testing.T, appID, name string) DestinationView {
	testInstance.Helper()
	view, err := fixture.service.CreateAppDestination(context.Background(), appID, webhookInput(name))
	if err != nil {
		testInstance.Fatalf("create app webhook %q for %q: %v", name, appID, err)
	}
	return view
}

// createAppNtfy creates the ntfy destination app_a_push, with the topic app_a-alerts, owned by appA.
func (fixture serviceFixture) createAppNtfy(testInstance *testing.T) DestinationView {
	testInstance.Helper()
	view, err := fixture.service.CreateAppDestination(context.Background(), appA, DestinationInput{
		Name: "app_a_push", Type: TypeNtfy, Config: map[string]string{"topic": "app_a-alerts"},
	})
	if err != nil {
		testInstance.Fatalf("create app ntfy: %v", err)
	}
	return view
}

func requireCalls(testInstance *testing.T, label string, store *fakeStore, want ...string) {
	testInstance.Helper()
	if !reflect.DeepEqual(store.calls, want) {
		testInstance.Fatalf("%s: store calls = %v, want %v", label, store.calls, want)
	}
}

func requireValidationError(testInstance *testing.T, label string, err error, wantMessage string) {
	testInstance.Helper()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Message != wantMessage {
		testInstance.Fatalf("%s: err = %v, want a validation error %q", label, err, wantMessage)
	}
}

func TestCreateAppDestinationIsOwnedByTheCallingAppAndNeverTheOperator(testInstance *testing.T) {
	inputs := []DestinationInput{
		webhookInput("app_a_hook"),
		{Name: "app_a_chat", Type: TypeDiscord, Secrets: map[string]string{"webhook_url": testDiscordURL}},
		{Name: "app_a_push", Type: TypeNtfy, Config: map[string]string{"topic": "app_a-alerts"}},
	}
	for _, input := range inputs {
		testInstance.Run(input.Type, func(testInstance *testing.T) {
			fixture, _ := newAppFixture(testInstance)

			view, err := fixture.service.CreateAppDestination(context.Background(), appA, input)
			if err != nil {
				testInstance.Fatalf("CreateAppDestination: %v", err)
			}

			// The row handed to the store names the owner explicitly: the store treats an empty owner
			// kind as the operator, so anything short of 'app' here would be a fail-open write.
			stored := fixture.store.destinations[input.Name]
			if stored.OwnerKind != OwnerApp {
				testInstance.Fatalf("stored owner kind = %q, want %q", stored.OwnerKind, OwnerApp)
			}
			if stored.OwnerAppID == nil || *stored.OwnerAppID != appA {
				testInstance.Fatalf("stored owner app id = %v, want %q", stored.OwnerAppID, appA)
			}
			if stored.UsableBy("someone_else") || !stored.UsableBy(appA) {
				testInstance.Fatalf("an app-owned row must be usable by its owner only")
			}
			if view.OwnerKind != OwnerApp || view.OwnerAppID == nil || *view.OwnerAppID != appA {
				testInstance.Fatalf("view owner = %q/%v, want app/%q", view.OwnerKind, view.OwnerAppID, appA)
			}
		})
	}
}

func TestCreateAppDestinationRefusesAMalformedCallerAppID(testInstance *testing.T) {
	for _, appID := range []string{"", "Upper", "has space", "a-b", strings.Repeat("a", 64)} {
		fixture, checker := newAppFixture(testInstance)

		_, err := fixture.service.CreateAppDestination(context.Background(), appID, webhookInput("app_a_hook"))

		requireValidationError(testInstance, "app id "+appID, err, "invalid app_id")
		requireCalls(testInstance, "app id "+appID, fixture.store)
		if len(checker.calls) != 0 {
			testInstance.Fatalf("the URL checker ran for a malformed caller app id")
		}
	}
}

func TestCreateAppDestinationEnforcesTheNamePrefix(testInstance *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "hook", want: "name must start with app_a_"},
		{name: "app_b_hook", want: "name must start with app_a_"},
		{name: "app_ahook", want: "name must start with app_a_"},
		{name: "app_a_", want: "name must start with app_a_"},
		{name: "xapp_a_hook", want: "name must start with app_a_"},
		{name: "APP_A_hook", want: "invalid name"},
	} {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			fixture, checker := newAppFixture(testInstance)

			_, err := fixture.service.CreateAppDestination(context.Background(), appA, webhookInput(test.name))

			requireValidationError(testInstance, test.name, err, test.want)
			requireCalls(testInstance, test.name, fixture.store)
			if len(checker.calls) != 0 {
				testInstance.Fatalf("the URL checker ran for a name the prefix rule refuses")
			}
		})
	}
}

// An app id that is a prefix of another app's id owns part of that app's name space as far as the prefix
// rule goes: app "app" may create "app_a_hook". The rule cannot tell the two apart, but ownership does:
// the row is the caller's, nothing of it reaches app_a, and app_a can no longer create that name.
func TestAppIDPrefixOverlapNeverCrossesTheOwnerBoundary(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	ctx := context.Background()

	fixture.createAppWebhook(testInstance, "app", "app_a_hook")

	stored := fixture.store.destinations["app_a_hook"]
	if stored.OwnerAppID == nil || *stored.OwnerAppID != "app" {
		testInstance.Fatalf("owner = %v, want the creating app %q", stored.OwnerAppID, "app")
	}
	if _, err := fixture.service.GetAppDestination(ctx, appA, "app_a_hook"); !errors.Is(err, ErrDestinationNotFound) {
		testInstance.Fatalf("app_a Get of the squatted name err = %v, want not found", err)
	}
	if listed, err := fixture.service.ListAppDestinations(ctx, appA); err != nil || len(listed) != 0 {
		testInstance.Fatalf("app_a List = %v, %v; want empty", listed, err)
	}
	if _, err := fixture.service.CreateAppDestination(ctx, appA, webhookInput("app_a_hook")); !errors.Is(err, ErrDestinationExists) {
		testInstance.Fatalf("app_a create of the squatted name err = %v, want exists", err)
	}
	if _, err := fixture.service.Resolve(ctx, appA, "app_a_hook", ""); !errors.Is(err, ErrUnknownDestination) {
		testInstance.Fatalf("app_a Resolve of the squatted name err = %v, want unknown destination", err)
	}
	resolved, err := fixture.service.Resolve(ctx, "app", "app_a_hook", "")
	if err != nil || len(resolved) != 1 {
		testInstance.Fatalf("owner Resolve = %v, %v; want its own destination", resolved, err)
	}
}

func TestCreateAppDestinationEnforcesTheNtfyTopicPrefix(testInstance *testing.T) {
	for _, test := range []struct {
		topic   string
		wantErr string
	}{
		{topic: "app_a-alerts"},
		{topic: "alerts", wantErr: "config.topic must start with app_a-"},
		{topic: "app_b-alerts", wantErr: "config.topic must start with app_a-"},
		{topic: "app_a-", wantErr: "config.topic must start with app_a-"},
		{topic: "app_a_alerts", wantErr: "config.topic must start with app_a-"},
		{topic: "", wantErr: "config.topic must start with app_a-"},
	} {
		testInstance.Run("topic "+test.topic, func(testInstance *testing.T) {
			fixture, _ := newAppFixture(testInstance)
			config := map[string]string{"topic": test.topic}
			if test.topic == "" {
				config = nil
			}

			_, err := fixture.service.CreateAppDestination(context.Background(), appA,
				DestinationInput{Name: "app_a_push", Type: TypeNtfy, Config: config})

			if test.wantErr == "" {
				if err != nil {
					testInstance.Fatalf("CreateAppDestination: %v", err)
				}
				return
			}
			// A missing topic is refused by the field rules before the prefix rule is reached.
			if test.topic == "" {
				requireValidationError(testInstance, "no topic", err, "invalid config.topic")
			} else {
				requireValidationError(testInstance, test.topic, err, test.wantErr)
			}
			requireCalls(testInstance, test.topic, fixture.store)
		})
	}
}

func TestCreateAppDestinationRunsTheURLCheckAfterValidationAndBeforeTheInsert(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)

	fixture.createAppWebhook(testInstance, appA, "app_a_hook")

	requireCalls(testInstance, "create", fixture.store, "CheckDestinationSecrets", "InsertDestination")
	if len(checker.calls) != 1 || checker.calls[0].destinationType != TypeWebhook ||
		checker.calls[0].secrets["url"] != testWebhookURL {
		testInstance.Fatalf("checker calls = %+v, want one webhook check of the URL", checker.calls)
	}

	discordFixture, discordChecker := newAppFixture(testInstance)
	_, err := discordFixture.service.CreateAppDestination(context.Background(), appA, DestinationInput{
		Name: "app_a_chat", Type: TypeDiscord, Secrets: map[string]string{"webhook_url": testDiscordURL},
	})
	if err != nil {
		testInstance.Fatalf("create discord: %v", err)
	}
	if len(discordChecker.calls) != 1 || discordChecker.calls[0].destinationType != TypeDiscord {
		testInstance.Fatalf("discord checker calls = %+v, want one discord check", discordChecker.calls)
	}
}

func TestCreateAppDestinationNeedsNoURLCheckForNtfy(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)

	fixture.createAppNtfy(testInstance)

	if len(checker.calls) != 0 {
		testInstance.Fatalf("checker calls = %+v, want none for an ntfy destination", checker.calls)
	}
}

func TestCreateAppDestinationRefusedByTheURLCheckWritesNothing(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	checker.err = &ValidationError{Message: "secrets.url must resolve to public addresses"}

	_, err := fixture.service.CreateAppDestination(context.Background(), appA, webhookInput("app_a_hook"))

	requireValidationError(testInstance, "refused URL", err, "secrets.url must resolve to public addresses")
	requireCalls(testInstance, "refused URL", fixture.store, "CheckDestinationSecrets")
	if strings.Contains(fixture.logs.String(), "secret-path-xyz") {
		testInstance.Fatalf("the log carries the URL path: %s", fixture.logs.String())
	}
	if !strings.Contains(fixture.logs.String(), "app destination url refused") {
		testInstance.Fatalf("the refusal was not logged: %s", fixture.logs.String())
	}
}

func TestCreateAppDestinationValidatesFieldsBeforeTheURLCheck(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)

	// A discord URL on a host outside the discord allow-list is refused by ValidateDestinationFields,
	// which must run before any DNS lookup.
	_, err := fixture.service.CreateAppDestination(context.Background(), appA, DestinationInput{
		Name: "app_a_chat", Type: TypeDiscord,
		Secrets: map[string]string{"webhook_url": "https://internal.example.com/api/webhooks/1/abc"},
	})

	requireValidationError(testInstance, "non-discord host", err, "invalid secrets.webhook_url")
	if len(checker.calls) != 0 {
		testInstance.Fatalf("the URL checker ran before field validation: %+v", checker.calls)
	}
	requireCalls(testInstance, "non-discord host", fixture.store)
}

func TestCreateAppDestinationFailsClosedWithoutAURLChecker(testInstance *testing.T) {
	fixture := newServiceFixture(testInstance)

	_, err := fixture.service.CreateAppDestination(context.Background(), appA, webhookInput("app_a_hook"))

	if !errors.Is(err, ErrURLCheckUnavailable) {
		testInstance.Fatalf("err = %v, want ErrURLCheckUnavailable", err)
	}
	requireCalls(testInstance, "no checker", fixture.store)

	// An ntfy destination carries no URL, so it needs no checker.
	if _, err := fixture.service.CreateAppDestination(context.Background(), appA, DestinationInput{
		Name: "app_a_push", Type: TypeNtfy, Config: map[string]string{"topic": "app_a-alerts"},
	}); err != nil {
		testInstance.Fatalf("ntfy create without a checker: %v", err)
	}
}

func TestOperatorDestinationWritesNeverCallTheURLChecker(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	ctx := context.Background()

	// Operator create of every URL-bearing type, with a URL the app path would resolve.
	if _, err := fixture.service.CreateDestination(ctx, webhookInput("ops_hook")); err != nil {
		testInstance.Fatalf("operator create webhook: %v", err)
	}
	if _, err := fixture.service.CreateDestination(ctx, DestinationInput{
		Name: "ops_chat", Type: TypeDiscord, Secrets: map[string]string{"webhook_url": testDiscordURL},
	}); err != nil {
		testInstance.Fatalf("operator create discord: %v", err)
	}
	// Operator PATCH that changes the URL secret, of its own destination and of an app-owned one.
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	checker.calls = nil
	for _, name := range []string{"ops_hook", "app_a_hook"} {
		if _, err := fixture.service.UpdateDestination(ctx, name, DestinationPatch{
			Secrets: map[string]string{"url": "https://other.example.com/in/new"},
		}); err != nil {
			testInstance.Fatalf("operator patch of %q: %v", name, err)
		}
	}
	// Operator rotate.
	if _, _, err := fixture.service.RotateWebhookSecret(ctx, "ops_hook", 24); err != nil {
		testInstance.Fatalf("operator rotate: %v", err)
	}

	if len(checker.calls) != 0 {
		testInstance.Fatalf("the URL checker ran for an operator write: %+v", checker.calls)
	}
}

func TestListAppDestinationsReturnsOnlyTheCallersOwn(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	ctx := context.Background()
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	fixture.createAppNtfy(testInstance)
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")
	fixture.createWebhook(testInstance, "ops_hook")

	listed, err := fixture.service.ListAppDestinations(ctx, appA)
	if err != nil {
		testInstance.Fatalf("ListAppDestinations: %v", err)
	}
	names := []string{}
	for _, view := range listed {
		names = append(names, view.Name)
		if view.OwnerKind != OwnerApp || view.OwnerAppID == nil || *view.OwnerAppID != appA {
			testInstance.Fatalf("listed %q owned by %q/%v", view.Name, view.OwnerKind, view.OwnerAppID)
		}
	}
	if !reflect.DeepEqual(names, []string{"app_a_hook", "app_a_push"}) {
		testInstance.Fatalf("listed = %v, want only app_a's destinations", names)
	}

	empty, err := fixture.service.ListAppDestinations(ctx, "app_c")
	if err != nil || empty == nil || len(empty) != 0 {
		testInstance.Fatalf("List of an app with none = %v, %v; want an empty non-nil slice", empty, err)
	}
}

func TestListAppDestinationsDropsARowTheStoreReturnedForAnotherOwner(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	fixture.createWebhook(testInstance, "ops_hook")
	fixture.store.destinations["app_a_hook"] = StoredDestination{
		ID: 99, Name: "app_a_hook", Type: TypeWebhook, OwnerKind: OwnerOperator,
		SealedSecrets: fixture.store.destinations["ops_hook"].SealedSecrets,
	}
	misbehaving := &ownerBlindStore{fakeStore: fixture.store}
	service := NewService(misbehaving, fixture.sealer, slog.New(slog.NewTextHandler(fixture.logs, nil)))

	listed, err := service.ListAppDestinations(context.Background(), appA)

	if err != nil || len(listed) != 0 {
		testInstance.Fatalf("listed = %v, %v; want the rows owned by another owner dropped", listed, err)
	}
}

// ownerBlindStore returns every destination from ListDestinationsByOwner, as a store that forgot its
// owner filter would.
type ownerBlindStore struct{ *fakeStore }

func (store *ownerBlindStore) ListDestinationsByOwner(ctx context.Context, _ string) ([]StoredDestination, error) {
	return store.ListDestinations(ctx)
}

func TestAppDestinationAccessToAnythingNotOwnedIsNotFound(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	ctx := context.Background()
	fixture.createAppWebhook(testInstance, appB, "app_b_hook")
	fixture.createWebhook(testInstance, "ops_hook")
	// An operator-owned destination whose name starts with the caller's prefix: ownership, not the name
	// prefix, decides access.
	fixture.createWebhook(testInstance, "app_a_legacy")
	fixture.store.calls = nil
	checker.calls = nil
	before := maps.Clone(fixture.store.destinations)

	notOwned := []string{"app_b_hook", "ops_hook", "app_a_legacy", "app_a_absent"}
	for _, name := range notOwned {
		operations := map[string]func() error{
			"get": func() error { _, err := fixture.service.GetAppDestination(ctx, appA, name); return err },
			"patch": func() error {
				_, err := fixture.service.UpdateAppDestination(ctx, appA, name, DestinationPatch{
					Secrets: map[string]string{"url": "https://other.example.com/in/new"},
				})
				return err
			},
			"delete": func() error { return fixture.service.DeleteAppDestination(ctx, appA, name) },
			"rotate": func() error {
				_, _, err := fixture.service.RotateAppWebhookSecret(ctx, appA, name, 24)
				return err
			},
		}
		for operation, run := range operations {
			if err := run(); !errors.Is(err, ErrDestinationNotFound) {
				testInstance.Fatalf("%s of %q err = %v, want ErrDestinationNotFound", operation, name, err)
			}
		}
	}

	if len(checker.calls) != 0 {
		testInstance.Fatalf("the URL checker ran for a destination the caller does not own: %+v", checker.calls)
	}
	for _, call := range fixture.store.calls {
		if call == "UpdateDestination" {
			testInstance.Fatalf("a row lock was taken for a destination the caller does not own: %v", fixture.store.calls)
		}
	}
	if !reflect.DeepEqual(fixture.store.destinations, before) {
		testInstance.Fatalf("a destination the caller does not own was changed")
	}

	listed, listErr := fixture.service.ListAppDestinations(ctx, appA)
	if listErr != nil || len(listed) != 0 {
		testInstance.Fatalf("ListAppDestinations = %v, %v; want none (the prefixed operator row is not app_a's)",
			listed, listErr)
	}
	_, createErr := fixture.service.CreateAppDestination(ctx, appA, webhookInput("app_a_legacy"))
	if !errors.Is(createErr, ErrDestinationExists) {
		testInstance.Fatalf("create of an occupied prefixed name err = %v, want ErrDestinationExists", createErr)
	}
	if !reflect.DeepEqual(fixture.store.destinations, before) {
		testInstance.Fatalf("the refused create changed a destination")
	}
}

func TestUpdateAppDestinationChecksTheURLOnlyWhenTheEffectiveURLChanges(testInstance *testing.T) {
	newURL := "https://other.example.com/in/new"
	for _, test := range []struct {
		name      string
		patch     DestinationPatch
		wantCheck bool
	}{
		{name: "url changed", patch: DestinationPatch{Secrets: map[string]string{"url": newURL}}, wantCheck: true},
		{name: "same url", patch: DestinationPatch{Secrets: map[string]string{"url": testWebhookURL}}},
		{name: "signing secret only", patch: DestinationPatch{Secrets: map[string]string{"signing_secret": strings.Repeat("n", 40)}}},
		{name: "enabled only", patch: DestinationPatch{Enabled: new(false)}},
		{name: "empty", patch: DestinationPatch{}},
	} {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			fixture, checker := newAppFixture(testInstance)
			fixture.createAppWebhook(testInstance, appA, "app_a_hook")
			checker.calls = nil
			fixture.store.calls = nil

			if _, err := fixture.service.UpdateAppDestination(context.Background(), appA, "app_a_hook", test.patch); err != nil {
				testInstance.Fatalf("UpdateAppDestination: %v", err)
			}

			if got := len(checker.calls) == 1; got != test.wantCheck {
				testInstance.Fatalf("checked = %v (%d calls), want %v", got, len(checker.calls), test.wantCheck)
			}
			if test.wantCheck {
				requireCalls(testInstance, "patch", fixture.store, "GetDestination", "CheckDestinationSecrets", "UpdateDestination")
				// The check sees the effective secrets: the new URL on top of the stored signing secret.
				if got := checker.calls[0]; got.destinationType != TypeWebhook || got.secrets["url"] != newURL ||
					got.secrets["signing_secret"] != testSigningSecret {
					testInstance.Fatalf("checker saw %+v, want the merged secrets with the new URL", got)
				}
				if opened := fixture.openSecrets(testInstance, "app_a_hook"); opened["url"] != newURL {
					testInstance.Fatalf("stored url = %q, want %q", opened["url"], newURL)
				}
			}
		})
	}
}

func TestUpdateAppDestinationDiscordURLChecksTheWebhookURLKey(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	ctx := context.Background()
	if _, err := fixture.service.CreateAppDestination(ctx, appA, DestinationInput{
		Name: "app_a_chat", Type: TypeDiscord, Secrets: map[string]string{"webhook_url": testDiscordURL},
	}); err != nil {
		testInstance.Fatalf("create discord: %v", err)
	}
	checker.calls = nil
	newURL := "https://discord.com/api/webhooks/987654/newTOKEN"

	if _, err := fixture.service.UpdateAppDestination(ctx, appA, "app_a_chat", DestinationPatch{
		Secrets: map[string]string{"webhook_url": newURL},
	}); err != nil {
		testInstance.Fatalf("UpdateAppDestination: %v", err)
	}

	if len(checker.calls) != 1 || checker.calls[0].destinationType != TypeDiscord ||
		checker.calls[0].secrets["webhook_url"] != newURL {
		testInstance.Fatalf("checker calls = %+v, want one discord check of the new URL", checker.calls)
	}

	// A host outside the discord allow-list is refused by field validation, before any lookup.
	checker.calls = nil
	_, err := fixture.service.UpdateAppDestination(ctx, appA, "app_a_chat", DestinationPatch{
		Secrets: map[string]string{"webhook_url": "https://internal.example.com/api/webhooks/1/abc"},
	})
	requireValidationError(testInstance, "non-discord host", err, "invalid secrets.webhook_url")
	if len(checker.calls) != 0 {
		testInstance.Fatalf("the URL checker ran for an invalid discord URL: %+v", checker.calls)
	}
}

func TestUpdateAppDestinationRefusedByTheURLCheckChangesNothing(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	checker.err = &ValidationError{Message: "secrets.url host could not be resolved"}
	sealedBefore := fixture.store.destinations["app_a_hook"].SealedSecrets
	fixture.store.calls = nil

	_, err := fixture.service.UpdateAppDestination(context.Background(), appA, "app_a_hook", DestinationPatch{
		Secrets: map[string]string{"url": "https://other.example.com/in/new"},
	})

	requireValidationError(testInstance, "refused URL", err, "secrets.url host could not be resolved")
	requireCalls(testInstance, "refused patch", fixture.store, "GetDestination", "CheckDestinationSecrets")
	if !reflect.DeepEqual(fixture.store.destinations["app_a_hook"].SealedSecrets, sealedBefore) {
		testInstance.Fatalf("the sealed secrets changed although the URL check refused the patch")
	}
}

func TestUpdateAppDestinationFailsClosedWithoutAURLChecker(testInstance *testing.T) {
	fixture := newServiceFixture(testInstance)
	fixture.store.destinations["app_a_hook"] = fixtureAppWebhookRow(testInstance, fixture, appA, "app_a_hook")

	_, err := fixture.service.UpdateAppDestination(context.Background(), appA, "app_a_hook", DestinationPatch{
		Secrets: map[string]string{"url": "https://other.example.com/in/new"},
	})

	if !errors.Is(err, ErrURLCheckUnavailable) {
		testInstance.Fatalf("err = %v, want ErrURLCheckUnavailable", err)
	}
}

// fixtureAppWebhookRow builds a stored app-owned webhook row directly, for tests of a service with no checker.
func fixtureAppWebhookRow(testInstance *testing.T, fixture serviceFixture, appID, name string) StoredDestination {
	testInstance.Helper()
	sealed, err := fixture.service.sealSecrets(name, map[string]string{"url": testWebhookURL, "signing_secret": testSigningSecret})
	if err != nil {
		testInstance.Fatalf("seal: %v", err)
	}
	owner := appID
	return StoredDestination{
		ID: 7, Name: name, Type: TypeWebhook, Config: map[string]string{}, SealedSecrets: sealed,
		Enabled: true, OwnerKind: OwnerApp, OwnerAppID: &owner,
	}
}

func TestUpdateAppDestinationEnforcesTheTopicPrefix(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	ctx := context.Background()
	fixture.createAppNtfy(testInstance)

	_, err := fixture.service.UpdateAppDestination(ctx, appA, "app_a_push", DestinationPatch{
		Config: map[string]string{"topic": "app_b-alerts"},
	})
	requireValidationError(testInstance, "foreign topic", err, "config.topic must start with app_a-")
	if got := fixture.store.destinations["app_a_push"].Config["topic"]; got != "app_a-alerts" {
		testInstance.Fatalf("topic = %q, want it unchanged", got)
	}

	view, err := fixture.service.UpdateAppDestination(ctx, appA, "app_a_push", DestinationPatch{
		Config: map[string]string{"topic": "app_a-other"},
	})
	if err != nil || view.Config["topic"] != "app_a-other" {
		testInstance.Fatalf("own-prefix topic patch = %+v, %v; want it applied", view, err)
	}
}

func TestUpdateAppDestinationOfOtherFieldsIgnoresAnOperatorSetTopic(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	ctx := context.Background()
	fixture.createAppNtfy(testInstance)
	// The operator may edit an app-owned destination, including to a topic outside the app's prefix.
	if _, err := fixture.service.UpdateDestination(ctx, "app_a_push", DestinationPatch{
		Config: map[string]string{"topic": "ops-alerts"},
	}); err != nil {
		testInstance.Fatalf("operator patch: %v", err)
	}

	view, err := fixture.service.UpdateAppDestination(ctx, appA, "app_a_push", DestinationPatch{Enabled: new(false)})

	if err != nil || view.Enabled {
		testInstance.Fatalf("disable = %+v, %v; want the app able to disable its destination", view, err)
	}
}

// The owner is checked again under the row lock: a destination that stopped being the app's between the
// pre-check and the lock (deleted and re-created by the operator, say) is never written.
func TestAppWritesRecheckTheOwnerUnderTheRowLock(testInstance *testing.T) {
	for _, test := range []struct {
		name string
		run  func(fixture serviceFixture) error
	}{
		{name: "patch", run: func(fixture serviceFixture) error {
			_, err := fixture.service.UpdateAppDestination(context.Background(), appA, "app_a_hook", DestinationPatch{
				Secrets: map[string]string{"url": "https://other.example.com/in/new"},
			})
			return err
		}},
		{name: "rotate", run: func(fixture serviceFixture) error {
			_, _, err := fixture.service.RotateAppWebhookSecret(context.Background(), appA, "app_a_hook", 24)
			return err
		}},
	} {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			fixture, checker := newAppFixture(testInstance)
			fixture.createAppWebhook(testInstance, appA, "app_a_hook")
			checker.onCheck = func() {
				// Between the check and the lock the row becomes the operator's.
				row := fixture.store.destinations["app_a_hook"]
				row.OwnerKind, row.OwnerAppID = OwnerOperator, nil
				fixture.store.destinations["app_a_hook"] = row
			}
			sealedBefore := fixture.store.destinations["app_a_hook"].SealedSecrets

			err := test.run(fixture)

			if !errors.Is(err, ErrDestinationNotFound) {
				testInstance.Fatalf("err = %v, want ErrDestinationNotFound", err)
			}
			if !reflect.DeepEqual(fixture.store.destinations["app_a_hook"].SealedSecrets, sealedBefore) {
				testInstance.Fatalf("the row was written although it was no longer the app's")
			}
		})
	}
}

// The row identity is checked under the lock too: a destination deleted and re-created by the same app
// between the pre-check and the lock is a different row, and a decision made on the old one (the SSRF
// check, the type) must never be applied to it.
func TestAppWritesRecheckTheRowIdentityUnderTheRowLock(testInstance *testing.T) {
	for _, test := range []struct {
		name string
		run  func(fixture serviceFixture) error
	}{
		{name: "patch", run: func(fixture serviceFixture) error {
			_, err := fixture.service.UpdateAppDestination(context.Background(), appA, "app_a_hook", DestinationPatch{
				Secrets: map[string]string{"url": "https://other.example.com/in/new"},
			})
			return err
		}},
		{name: "rotate", run: func(fixture serviceFixture) error {
			_, _, err := fixture.service.RotateAppWebhookSecret(context.Background(), appA, "app_a_hook", 24)
			return err
		}},
	} {
		testInstance.Run(test.name, func(testInstance *testing.T) {
			fixture, checker := newAppFixture(testInstance)
			fixture.createAppWebhook(testInstance, appA, "app_a_hook")
			originalID := fixture.store.destinations["app_a_hook"].ID
			var replacementSealed []byte
			checker.onCheck = func() {
				// Between the check and the lock the row is replaced by a new row of the same app and name.
				row := fixture.store.destinations["app_a_hook"]
				fixture.store.nextDestID++
				row.ID = fixture.store.nextDestID
				fixture.store.destinations["app_a_hook"] = row
				replacementSealed = row.SealedSecrets
			}

			err := test.run(fixture)

			if !errors.Is(err, ErrDestinationNotFound) {
				testInstance.Fatalf("err = %v, want ErrDestinationNotFound", err)
			}
			replacement := fixture.store.destinations["app_a_hook"]
			if replacement.ID == originalID {
				testInstance.Fatalf("test setup: the replacement kept id %d", originalID)
			}
			if !reflect.DeepEqual(replacement.SealedSecrets, replacementSealed) {
				testInstance.Fatalf("the replacement row was written with a decision made on the old row")
			}
		})
	}
}

func TestRotateAppWebhookSecretChecksTheStoredURLBeforeTheLock(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	checker.calls = nil
	fixture.store.calls = nil

	view, secret, err := fixture.service.RotateAppWebhookSecret(context.Background(), appA, "app_a_hook", 24)

	if err != nil {
		testInstance.Fatalf("RotateAppWebhookSecret: %v", err)
	}
	requireCalls(testInstance, "rotate", fixture.store, "GetDestination", "CheckDestinationSecrets", "UpdateDestination")
	if len(checker.calls) != 1 || checker.calls[0].secrets["url"] != testWebhookURL {
		testInstance.Fatalf("checker calls = %+v, want one check of the stored URL", checker.calls)
	}
	if opened := fixture.openSecrets(testInstance, "app_a_hook"); opened["signing_secret"] != secret ||
		opened["signing_secret_previous"] != testSigningSecret {
		testInstance.Fatalf("rotation did not move the secrets: %v", opened)
	}
	if view.OwnerKind != OwnerApp {
		testInstance.Fatalf("view owner kind = %q, want app", view.OwnerKind)
	}
	if strings.Contains(strings.Join(mapValues(view.Secrets), " "), secret) {
		testInstance.Fatalf("the rotated view carries the plaintext secret: %v", view.Secrets)
	}
}

func mapValues(values map[string]string) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func TestRotateAppWebhookSecretRefusedByTheURLCheckRotatesNothing(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")
	checker.err = &ValidationError{Message: "secrets.url must resolve to public addresses"}
	sealedBefore := fixture.store.destinations["app_a_hook"].SealedSecrets
	fixture.store.calls = nil

	_, _, err := fixture.service.RotateAppWebhookSecret(context.Background(), appA, "app_a_hook", 24)

	requireValidationError(testInstance, "rotate refused", err, "secrets.url must resolve to public addresses")
	requireCalls(testInstance, "rotate refused", fixture.store, "GetDestination", "CheckDestinationSecrets")
	if !reflect.DeepEqual(fixture.store.destinations["app_a_hook"].SealedSecrets, sealedBefore) {
		testInstance.Fatalf("the secrets were rotated although the URL check refused")
	}
}

func TestRotateAppWebhookSecretOfANonWebhookIsUnsupportedWithoutAURLCheck(testInstance *testing.T) {
	fixture, checker := newAppFixture(testInstance)
	fixture.createAppNtfy(testInstance)

	_, _, err := fixture.service.RotateAppWebhookSecret(context.Background(), appA, "app_a_push", 24)

	if !errors.Is(err, ErrRotationUnsupported) {
		testInstance.Fatalf("err = %v, want ErrRotationUnsupported", err)
	}
	if len(checker.calls) != 0 {
		testInstance.Fatalf("checker calls = %+v, want none", checker.calls)
	}
}

func TestDeleteAppDestination(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	ctx := context.Background()
	fixture.createAppWebhook(testInstance, appA, "app_a_hook")

	if err := fixture.service.DeleteAppDestination(ctx, appA, "app_a_hook"); err != nil {
		testInstance.Fatalf("delete own: %v", err)
	}
	if _, present := fixture.store.destinations["app_a_hook"]; present {
		testInstance.Fatalf("the destination is still stored")
	}
	if err := fixture.service.DeleteAppDestination(ctx, appA, "app_a_hook"); !errors.Is(err, ErrDestinationNotFound) {
		testInstance.Fatalf("delete of an absent destination err = %v, want not found", err)
	}

	fixture.createAppWebhook(testInstance, appA, "app_a_other")
	fixture.store.deleteErr = ErrDestinationInUse
	if err := fixture.service.DeleteAppDestination(ctx, appA, "app_a_other"); !errors.Is(err, ErrDestinationInUse) {
		testInstance.Fatalf("delete of a linked destination err = %v, want in use", err)
	}
}

func TestAppDestinationViewsMaskSecretsAsAdminOnesDo(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	ctx := context.Background()
	created := fixture.createAppWebhook(testInstance, appA, "app_a_hook")

	got, err := fixture.service.GetAppDestination(ctx, appA, "app_a_hook")
	if err != nil {
		testInstance.Fatalf("GetAppDestination: %v", err)
	}
	admin, err := fixture.service.GetDestination(ctx, "app_a_hook")
	if err != nil {
		testInstance.Fatalf("GetDestination: %v", err)
	}
	if !reflect.DeepEqual(got, admin) || !reflect.DeepEqual(created.Secrets, admin.Secrets) {
		testInstance.Fatalf("app view %+v differs from the admin view %+v", got, admin)
	}
	for key, value := range got.Secrets {
		if value == testWebhookURL || value == testSigningSecret {
			testInstance.Fatalf("secret %q is not masked: %q", key, value)
		}
	}
}

func TestAppDestinationStoreErrorsPassThrough(testInstance *testing.T) {
	fixture, _ := newAppFixture(testInstance)
	boom := errors.New("store down")
	fixture.store.failures = map[string]error{"ListDestinationsByOwner": boom, "GetDestination": boom}

	if _, err := fixture.service.ListAppDestinations(context.Background(), appA); !errors.Is(err, boom) {
		testInstance.Fatalf("list err = %v, want the store error", err)
	}
	if _, err := fixture.service.GetAppDestination(context.Background(), appA, "app_a_hook"); !errors.Is(err, boom) {
		testInstance.Fatalf("get err = %v, want the store error", err)
	}
}

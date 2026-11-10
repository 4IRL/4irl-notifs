package personal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	appA          = "app_a"
	appB          = "app_b"
	aliceEmail    = "alice@example.com"
	bobEmail      = "bob@example.com"
	discordURL    = "https://discord.com/api/webhooks/123/abc"
	otherDiscord  = "https://discord.com/api/webhooks/456/def"
	webhookURL    = "https://hooks.example.com/in"
	signingSecret = "0123456789abcdef0123456789abcdef"
	fakeClockStep = time.Minute
)

// fakeStore is an in-memory Store that mirrors PGStore's contracts: Insert caps
// and opts in, Update locks, skips the write when nothing changed and bumps
// updated_at otherwise, OptOut reaps a registrar-removed row left with no opt-ins.
type fakeStore struct {
	sealer  *seal.Sealer
	rows    map[int64]*Destination
	optIns  map[int64]map[string]bool
	nextID  int64
	clock   time.Time
	updates int
	// optedInErr, when set, makes IsOptedIn fail.
	optedInErr error
}

func newFakeStore(sealer *seal.Sealer) *fakeStore {
	return &fakeStore{
		sealer: sealer, rows: map[int64]*Destination{}, optIns: map[int64]map[string]bool{},
		nextID: 1, clock: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
}

func (store *fakeStore) tick() time.Time {
	store.clock = store.clock.Add(fakeClockStep)
	return store.clock
}

func (store *fakeStore) Insert(_ context.Context, input NewDestination) (Destination, error) {
	count := 0
	for _, row := range store.rows {
		if row.PersonHash == input.PersonHash && row.RegisteredByAppID == input.RegisteredByAppID {
			count++
		}
	}
	if count >= MaxPerPersonPerApp {
		return Destination{}, ErrLimitReached
	}
	id := store.nextID
	store.nextID++
	sealed, sealErr := SealSecrets(store.sealer, id, input.Secrets)
	if sealErr != nil {
		return Destination{}, sealErr
	}
	now := store.tick()
	row := &Destination{
		ID: id, PersonHash: input.PersonHash, RegisteredByAppID: input.RegisteredByAppID,
		Label: input.Label, Type: input.Type, Config: configOrEmpty(input.Config),
		SealedSecrets: sealed, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	store.rows[id] = row
	store.optIns[id] = map[string]bool{}
	if input.OptIn {
		store.optIns[id][input.RegisteredByAppID] = true
	}
	return *row, nil
}

func (store *fakeStore) Get(_ context.Context, id int64) (Destination, error) {
	row, found := store.rows[id]
	if !found {
		return Destination{}, ErrNotFound
	}
	return *row, nil
}

func (store *fakeStore) ListByPerson(_ context.Context, personHash string) ([]Destination, error) {
	listed := []Destination{}
	for _, row := range store.rows {
		if row.PersonHash == personHash {
			listed = append(listed, *row)
		}
	}
	slices.SortFunc(listed, func(left, right Destination) int { return int(right.ID - left.ID) })
	return listed, nil
}

func (store *fakeStore) ListForAdmin(_ context.Context, personHash string) ([]Destination, int, error) {
	listed := []Destination{}
	for _, row := range store.rows {
		if personHash == "" || row.PersonHash == personHash {
			listed = append(listed, *row)
		}
	}
	slices.SortFunc(listed, func(left, right Destination) int { return int(right.ID - left.ID) })
	return listed, len(listed), nil
}

func (store *fakeStore) Update(
	_ context.Context, id int64, mutate func(Destination) (Destination, error),
) (Destination, error) {
	row, found := store.rows[id]
	if !found {
		return Destination{}, ErrNotFound
	}
	mutated, mutateErr := mutate(*row)
	if mutateErr != nil {
		return Destination{}, mutateErr
	}
	if mutableFieldsEqual(*row, mutated) {
		return *row, nil
	}
	store.updates++
	row.Label, row.Config, row.SealedSecrets = mutated.Label, mutated.Config, mutated.SealedSecrets
	row.Enabled, row.DisabledBy, row.DisabledReason = mutated.Enabled, mutated.DisabledBy, mutated.DisabledReason
	row.UpdatedAt = store.tick()
	return *row, nil
}

func (store *fakeStore) Delete(_ context.Context, id int64) (bool, error) {
	_, found := store.rows[id]
	delete(store.rows, id)
	delete(store.optIns, id)
	return found, nil
}

func (store *fakeStore) DeleteManaged(_ context.Context, id int64, appID, personHash string) (bool, error) {
	row, found := store.rows[id]
	if !found || row.PersonHash != personHash || row.RegisteredByAppID != appID || row.RegistrarRemovedAt != nil {
		return false, nil
	}
	delete(store.rows, id)
	delete(store.optIns, id)
	return true, nil
}

func (store *fakeStore) OptIn(_ context.Context, appID string, id int64) error {
	if _, found := store.rows[id]; !found {
		return ErrNotFound
	}
	store.optIns[id][appID] = true
	return nil
}

func (store *fakeStore) OptOut(_ context.Context, appID string, id int64) (bool, error) {
	row, found := store.rows[id]
	if !found {
		return false, ErrNotFound
	}
	delete(store.optIns[id], appID)
	if row.RegistrarRemovedAt != nil && len(store.optIns[id]) == 0 {
		delete(store.rows, id)
		delete(store.optIns, id)
		return true, nil
	}
	return false, nil
}

func (store *fakeStore) OptedInAppIDs(_ context.Context, id int64) ([]string, error) {
	appIDs := []string{}
	for appID := range store.optIns[id] {
		appIDs = append(appIDs, appID)
	}
	return appIDs, nil
}

func (store *fakeStore) IsOptedIn(_ context.Context, appID string, id int64) (bool, error) {
	if store.optedInErr != nil {
		return false, store.optedInErr
	}
	return store.optIns[id][appID], nil
}

func (store *fakeStore) ListEnabledOptedIn(_ context.Context, appID, personHash string) ([]Destination, error) {
	var out []Destination
	for id, row := range store.rows {
		if row.PersonHash == personHash && row.Enabled && store.optIns[id][appID] {
			out = append(out, *row)
		}
	}
	slices.SortFunc(out, func(left, right Destination) int { return int(left.ID - right.ID) })
	return out, nil
}

func (store *fakeStore) ListEnabledOptedInByIDs(ctx context.Context, appID, personHash string, ids []int64) ([]Destination, error) {
	all, listErr := store.ListEnabledOptedIn(ctx, appID, personHash)
	if listErr != nil {
		return nil, listErr
	}
	var out []Destination
	for _, row := range all {
		if slices.Contains(ids, row.ID) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (store *fakeStore) DisableBySystem(_ context.Context, id int64, reason string) (bool, error) {
	row, found := store.rows[id]
	if !found || !row.Enabled {
		return false, nil
	}
	system := DisabledBySystem
	row.Enabled, row.DisabledBy, row.DisabledReason = false, &system, &reason
	row.UpdatedAt = store.tick()
	return true, nil
}

func (store *fakeStore) ForgetPersonForApp(_ context.Context, appID, personHash string) (int64, int64, error) {
	var optIns, destinations int64
	freed := map[int64]bool{}
	for id, row := range store.rows {
		if row.PersonHash == personHash && store.optIns[id][appID] {
			delete(store.optIns[id], appID)
			freed[id] = true
			optIns++
		}
	}
	for id, row := range store.rows {
		if row.PersonHash != personHash || len(store.optIns[id]) > 0 {
			continue
		}
		if row.RegistrarRemovedAt != nil || (freed[id] && row.RegisteredByAppID == appID) {
			delete(store.rows, id)
			delete(store.optIns, id)
			destinations++
		}
	}
	return optIns, destinations, nil
}

func (store *fakeStore) ForgetPerson(_ context.Context, personHash string) (int64, error) {
	var removed int64
	for id, row := range store.rows {
		if row.PersonHash == personHash {
			delete(store.rows, id)
			delete(store.optIns, id)
			removed++
		}
	}
	return removed, nil
}

// stubChecker is a scripted DestinationURLChecker.
type stubChecker struct {
	err   error
	calls int
}

func (checker *stubChecker) CheckDestinationSecrets(context.Context, string, map[string]string) error {
	checker.calls++
	return checker.err
}

type fixture struct {
	service *Service
	store   *fakeStore
	checker *stubChecker
}

func newFixture(testInstance *testing.T) *fixture {
	testInstance.Helper()
	sealer := testSealer(testInstance)
	store := newFakeStore(sealer)
	checker := &stubChecker{}
	service := NewService(store, sealer, slog.New(slog.NewTextHandler(io.Discard, nil)), WithURLChecker(checker))
	return &fixture{service: service, store: store, checker: checker}
}

func (fix *fixture) create(testInstance *testing.T, email string) AppView {
	testInstance.Helper()
	view, createErr := fix.service.Create(context.Background(), appA, CreateInput{
		Person: PersonRef{Email: email}, Label: "Phone", Type: TypeDiscord,
		Secrets: map[string]string{"webhook_url": discordURL},
	})
	if createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}
	return view
}

func validationMessage(err error) string {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Message
	}
	return ""
}

func boolPointer(value bool) *bool { return &value }

func stringPointer(value string) *string { return &value }

func TestPersonRefResolve(testInstance *testing.T) {
	aliceHash := personhash.Hash(aliceEmail)
	testCases := []struct {
		name    string
		ref     PersonRef
		want    string
		message string
	}{
		{name: "email", ref: PersonRef{Email: " Alice@Example.com "}, want: aliceHash},
		{name: "hash", ref: PersonRef{PersonHash: aliceHash}, want: aliceHash},
		{name: "both", ref: PersonRef{Email: aliceEmail, PersonHash: aliceHash}, message: msgExactlyOnePerson},
		{name: "neither", ref: PersonRef{}, message: msgExactlyOnePerson},
		{name: "bad email", ref: PersonRef{Email: "nope"}, message: msgInvalidEmail},
		{name: "bad hash", ref: PersonRef{PersonHash: "ABC"}, message: msgInvalidPerson},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			got, resolveErr := testCase.ref.Resolve()
			if testCase.message != "" {
				if validationMessage(resolveErr) != testCase.message {
					testInstance.Fatalf("err = %v, want message %q", resolveErr, testCase.message)
				}
				return
			}
			if resolveErr != nil || got != testCase.want {
				testInstance.Fatalf("Resolve = %q, %v; want %q", got, resolveErr, testCase.want)
			}
		})
	}
}

func TestCreateValidation(testInstance *testing.T) {
	testCases := []struct {
		name    string
		input   CreateInput
		message string
		wantErr error
	}{
		{
			name: "ntfy refused",
			input: CreateInput{
				Person: PersonRef{Email: aliceEmail}, Label: "x", Type: registry.TypeNtfy,
				Config: map[string]string{"topic": "t"},
			},
			message: "type must be discord or webhook",
		},
		{
			name: "unknown type refused",
			input: CreateInput{
				Person: PersonRef{Email: aliceEmail}, Label: "x", Type: "sms",
			},
			message: "type must be discord or webhook",
		},
		{
			name: "empty label",
			input: CreateInput{
				Person: PersonRef{Email: aliceEmail}, Label: "  ", Type: TypeDiscord,
				Secrets: map[string]string{"webhook_url": discordURL},
			},
			message: msgInvalidLabel,
		},
		{
			name: "control character in label",
			input: CreateInput{
				Person: PersonRef{Email: aliceEmail}, Label: "a\nb", Type: TypeDiscord,
				Secrets: map[string]string{"webhook_url": discordURL},
			},
			message: msgInvalidLabel,
		},
		{
			name: "label too long",
			input: CreateInput{
				Person: PersonRef{Email: aliceEmail}, Label: strings.Repeat("x", 65), Type: TypeDiscord,
				Secrets: map[string]string{"webhook_url": discordURL},
			},
			message: msgInvalidLabel,
		},
		{
			name: "no person",
			input: CreateInput{
				Label: "x", Type: TypeDiscord, Secrets: map[string]string{"webhook_url": discordURL},
			},
			message: msgExactlyOnePerson,
		},
		{
			name: "registry field rule",
			input: CreateInput{
				Person: PersonRef{Email: aliceEmail}, Label: "x", Type: TypeDiscord,
				Secrets: map[string]string{"webhook_url": "http://discord.com/x"},
			},
			message: "invalid secrets.webhook_url",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			fix := newFixture(testInstance)
			_, createErr := fix.service.Create(context.Background(), appA, testCase.input)
			// Registry rule failures arrive converted to *personal.ValidationError too.
			message := validationMessage(createErr)
			if message != testCase.message {
				testInstance.Fatalf("err = %v, want message %q", createErr, testCase.message)
			}
			if len(fix.store.rows) != 0 || fix.checker.calls != 0 {
				testInstance.Fatalf("rows=%d checker calls=%d: nothing may be written", len(fix.store.rows), fix.checker.calls)
			}
		})
	}
}

func TestCreateTrimsLabelAndOptsIn(testInstance *testing.T) {
	fix := newFixture(testInstance)
	view, createErr := fix.service.Create(context.Background(), appA, CreateInput{
		Person: PersonRef{Email: aliceEmail}, Label: "  Phone  ", Type: TypeDiscord,
		Secrets: map[string]string{"webhook_url": discordURL},
	})
	if createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}
	if view.Label != "Phone" || !view.OptedIn || !view.ManagedByCaller || view.PersonHash != personhash.Hash(aliceEmail) {
		testInstance.Fatalf("view = %+v", view)
	}
	if view.Secrets["webhook_url"] != "https://discord.com/****" {
		testInstance.Fatalf("secrets = %v", view.Secrets)
	}
}

func TestCreateOptInFalse(testInstance *testing.T) {
	fix := newFixture(testInstance)
	view, createErr := fix.service.Create(context.Background(), appA, CreateInput{
		Person: PersonRef{Email: aliceEmail}, Label: "Phone", Type: TypeDiscord,
		Secrets: map[string]string{"webhook_url": discordURL}, OptIn: boolPointer(false),
	})
	if createErr != nil || view.OptedIn {
		testInstance.Fatalf("view = %+v, err = %v; want not opted in", view, createErr)
	}
}

func TestCreateURLChecks(testInstance *testing.T) {
	refused := &registry.ValidationError{Message: "secrets.url resolves to a private address"}
	testCases := []struct {
		name        string
		checker     error
		noChecker   bool
		wantErr     error
		wantMessage string
		wantCalled  bool
	}{
		{name: "private address refused", checker: refused, wantMessage: refused.Message, wantCalled: true},
		{name: "no checker fails closed", noChecker: true, wantErr: registry.ErrURLCheckUnavailable},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			fix := newFixture(testInstance)
			fix.checker.err = testCase.checker
			service := fix.service
			if testCase.noChecker {
				service = NewService(fix.store, testSealer(testInstance), nil)
			}
			_, createErr := service.Create(context.Background(), appA, CreateInput{
				Person: PersonRef{Email: aliceEmail}, Label: "Hook", Type: TypeWebhook,
				Secrets: map[string]string{"url": webhookURL, "signing_secret": signingSecret},
			})
			if testCase.wantErr != nil && !errors.Is(createErr, testCase.wantErr) {
				testInstance.Fatalf("err = %v, want %v", createErr, testCase.wantErr)
			}
			if testCase.wantMessage != "" && validationMessage(createErr) != testCase.wantMessage {
				testInstance.Fatalf("err = %v, want validation message %q", createErr, testCase.wantMessage)
			}
			if len(fix.store.rows) != 0 {
				testInstance.Fatalf("a refused create wrote %d rows", len(fix.store.rows))
			}
			if (fix.checker.calls > 0) != testCase.wantCalled {
				testInstance.Fatalf("checker calls = %d", fix.checker.calls)
			}
		})
	}
}

func TestCreateLimit(testInstance *testing.T) {
	fix := newFixture(testInstance)
	for range MaxPerPersonPerApp {
		fix.create(testInstance, aliceEmail)
	}
	_, createErr := fix.service.Create(context.Background(), appA, CreateInput{
		Person: PersonRef{Email: aliceEmail}, Label: "over the cap", Type: TypeDiscord,
		Secrets: map[string]string{"webhook_url": discordURL},
	})
	if !errors.Is(createErr, ErrLimitReached) {
		testInstance.Fatalf("err = %v, want ErrLimitReached", createErr)
	}
	fix.create(testInstance, bobEmail) // another person is unaffected
	// another registering app has its own allowance for the same person
	if _, otherAppErr := fix.service.Create(context.Background(), appB, CreateInput{
		Person: PersonRef{Email: aliceEmail}, Label: "other app", Type: TypeDiscord,
		Secrets: map[string]string{"webhook_url": discordURL},
	}); otherAppErr != nil {
		testInstance.Fatalf("other app create err = %v, want nil", otherAppErr)
	}
}

func TestLookupSeesOtherAppsRegistration(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	fix.create(testInstance, bobEmail)

	hash, views, lookupErr := fix.service.Lookup(context.Background(), appB, PersonRef{Email: aliceEmail})
	if lookupErr != nil || hash != personhash.Hash(aliceEmail) || len(views) != 1 {
		testInstance.Fatalf("Lookup = %q, %v, %v", hash, views, lookupErr)
	}
	if views[0].ID != created.ID || views[0].ManagedByCaller || views[0].OptedIn || views[0].RegisteredByAppID != appA {
		testInstance.Fatalf("view for app B = %+v", views[0])
	}
	_, empty, emptyErr := fix.service.Lookup(context.Background(), appB, PersonRef{Email: "nobody@example.com"})
	if emptyErr != nil || empty == nil || len(empty) != 0 {
		testInstance.Fatalf("empty lookup = %#v, want empty non-nil", empty)
	}
}

func TestUpdateAndDeleteOwnership(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	alice := PersonRef{Email: aliceEmail}
	label := stringPointer("New")

	testCases := []struct {
		name    string
		appID   string
		id      int64
		person  PersonRef
		wantErr error
	}{
		{name: "registrar ok", appID: appA, id: created.ID, person: alice},
		{name: "other app", appID: appB, id: created.ID, person: alice, wantErr: ErrManagedByOtherApp},
		{name: "other person", appID: appA, id: created.ID, person: PersonRef{Email: bobEmail}, wantErr: ErrNotFound},
		{name: "other person and other app is not found", appID: appB, id: created.ID, person: PersonRef{Email: bobEmail}, wantErr: ErrNotFound},
		{name: "absent id", appID: appA, id: 999, person: alice, wantErr: ErrNotFound},
	}
	for _, testCase := range testCases {
		testInstance.Run("update "+testCase.name, func(testInstance *testing.T) {
			_, updateErr := fix.service.Update(context.Background(), testCase.appID, testCase.id, testCase.person, Patch{Label: label})
			if !errors.Is(updateErr, testCase.wantErr) {
				testInstance.Fatalf("err = %v, want %v", updateErr, testCase.wantErr)
			}
		})
	}
	for _, testCase := range testCases[1:] {
		testInstance.Run("delete "+testCase.name, func(testInstance *testing.T) {
			deleteErr := fix.service.Delete(context.Background(), testCase.appID, testCase.id, testCase.person)
			if !errors.Is(deleteErr, testCase.wantErr) {
				testInstance.Fatalf("err = %v, want %v", deleteErr, testCase.wantErr)
			}
			if _, stillThere := fix.store.rows[created.ID]; !stillThere {
				testInstance.Fatal("a refused delete removed the row")
			}
		})
	}
	if deleteErr := fix.service.Delete(context.Background(), appA, created.ID, alice); deleteErr != nil {
		testInstance.Fatalf("registrar delete: %v", deleteErr)
	}
	if _, stillThere := fix.store.rows[created.ID]; stillThere {
		testInstance.Fatal("row survived a registrar delete")
	}
}

func TestPersonMismatchIndistinguishableFromAbsent(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	bob := PersonRef{Email: bobEmail}
	ctx := context.Background()

	_, otherPersonErr := fix.service.OptIn(ctx, appA, created.ID, bob)
	_, absentErr := fix.service.OptIn(ctx, appA, 999, bob)
	if !errors.Is(otherPersonErr, ErrNotFound) || otherPersonErr.Error() != absentErr.Error() {
		testInstance.Fatalf("other person = %v, absent = %v", otherPersonErr, absentErr)
	}
	if _, _, optOutErr := fix.service.OptOut(ctx, appA, created.ID, bob); !errors.Is(optOutErr, ErrNotFound) {
		testInstance.Fatalf("OptOut other person = %v", optOutErr)
	}
}

func TestOptInOptOutByOtherApp(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	alice := PersonRef{Email: aliceEmail}
	ctx := context.Background()

	for range 2 { // idempotent
		view, optErr := fix.service.OptIn(ctx, appB, created.ID, alice)
		if optErr != nil || !view.OptedIn || view.ManagedByCaller {
			testInstance.Fatalf("OptIn = %+v, %v", view, optErr)
		}
	}
	for range 2 {
		view, deleted, optErr := fix.service.OptOut(ctx, appB, created.ID, alice)
		if optErr != nil || deleted || view.OptedIn {
			testInstance.Fatalf("OptOut = %+v, deleted=%v, %v", view, deleted, optErr)
		}
	}
	if !fix.store.optIns[created.ID][appA] {
		testInstance.Fatal("app B's opt-out removed app A's opt-in")
	}
}

func TestOptInOptOutOnDisabledAndRegistrarRemovedRows(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	alice := PersonRef{Email: aliceEmail}
	ctx := context.Background()
	operator := DisabledByOperator
	row := fix.store.rows[created.ID]
	row.Enabled, row.DisabledBy = false, &operator
	removedAt := fix.store.clock
	row.RegistrarRemovedAt = &removedAt

	if view, optErr := fix.service.OptIn(ctx, appB, created.ID, alice); optErr != nil || !view.OptedIn || view.ManagedByCaller {
		testInstance.Fatalf("OptIn on disabled+removed row = %+v, %v", view, optErr)
	}
	if _, deleted, optErr := fix.service.OptOut(ctx, appB, created.ID, alice); optErr != nil || deleted {
		testInstance.Fatalf("OptOut leaving app A's opt-in: deleted=%v err=%v", deleted, optErr)
	}
}

func TestOptOutReapsRegistrarRemovedRowOnLastOptIn(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	alice := PersonRef{Email: aliceEmail}
	ctx := context.Background()
	if _, optErr := fix.service.OptIn(ctx, appB, created.ID, alice); optErr != nil {
		testInstance.Fatalf("OptIn: %v", optErr)
	}
	removedAt := fix.store.clock
	fix.store.rows[created.ID].RegistrarRemovedAt = &removedAt
	delete(fix.store.optIns[created.ID], appA) // app A's teardown removed its opt-in

	view, deleted, optErr := fix.service.OptOut(ctx, appB, created.ID, alice)
	if optErr != nil || !deleted || view.ID != 0 {
		testInstance.Fatalf("last opt-out = %+v, deleted=%v, %v; want reaped", view, deleted, optErr)
	}
	if _, stillThere := fix.store.rows[created.ID]; stillThere {
		testInstance.Fatal("row survived its last opt-out")
	}
	if _, _, repeatErr := fix.service.OptOut(ctx, appB, created.ID, alice); !errors.Is(repeatErr, ErrNotFound) {
		testInstance.Fatalf("repeat after reap = %v, want ErrNotFound", repeatErr)
	}
}

func TestOptOutNeverDeletesLiveRegistrarRow(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	view, deleted, optErr := fix.service.OptOut(context.Background(), appA, created.ID, PersonRef{Email: aliceEmail})
	if optErr != nil || deleted || view.ID != created.ID || view.OptedIn {
		testInstance.Fatalf("OptOut = %+v, deleted=%v, %v", view, deleted, optErr)
	}
	if _, stillThere := fix.store.rows[created.ID]; !stillThere {
		testInstance.Fatal("a live-registrar row was deleted by its last opt-out")
	}
}

func TestEnableRules(testInstance *testing.T) {
	alice := PersonRef{Email: aliceEmail}
	ctx := context.Background()

	testInstance.Run("app cannot re-enable operator-disabled row", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		created := fix.create(testInstance, aliceEmail)
		if _, adminErr := fix.service.AdminSetEnabled(ctx, created.ID, false, "abuse"); adminErr != nil {
			testInstance.Fatalf("AdminSetEnabled: %v", adminErr)
		}
		_, updateErr := fix.service.Update(ctx, appA, created.ID, alice, Patch{Enabled: boolPointer(true)})
		if !errors.Is(updateErr, ErrDisabledByOperator) {
			testInstance.Fatalf("err = %v, want ErrDisabledByOperator", updateErr)
		}
	})

	testInstance.Run("app can re-enable system-disabled row", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		created := fix.create(testInstance, aliceEmail)
		system, reason := DisabledBySystem, "Endpoint returned 410 Gone."
		row := fix.store.rows[created.ID]
		row.Enabled, row.DisabledBy, row.DisabledReason = false, &system, &reason
		view, updateErr := fix.service.Update(ctx, appA, created.ID, alice, Patch{Enabled: boolPointer(true)})
		if updateErr != nil || !view.Enabled || view.DisabledBy != nil || view.DisabledReason != nil {
			testInstance.Fatalf("view = %+v, err = %v", view, updateErr)
		}
	})

	testInstance.Run("app disable on enabled row records app", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		created := fix.create(testInstance, aliceEmail)
		view, updateErr := fix.service.Update(ctx, appA, created.ID, alice, Patch{Enabled: boolPointer(false)})
		if updateErr != nil || view.Enabled || view.DisabledBy == nil || *view.DisabledBy != DisabledByApp || view.DisabledReason != nil {
			testInstance.Fatalf("view = %+v, err = %v", view, updateErr)
		}
	})

	testInstance.Run("operator disable then app disable then app enable keeps operator", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		created := fix.create(testInstance, aliceEmail)
		if _, adminErr := fix.service.AdminSetEnabled(ctx, created.ID, false, "abuse"); adminErr != nil {
			testInstance.Fatalf("AdminSetEnabled: %v", adminErr)
		}
		before := *fix.store.rows[created.ID]
		view, disableErr := fix.service.Update(ctx, appA, created.ID, alice, Patch{Enabled: boolPointer(false)})
		if disableErr != nil {
			testInstance.Fatalf("app disable on disabled row: %v", disableErr)
		}
		if view.DisabledBy == nil || *view.DisabledBy != DisabledByOperator ||
			view.DisabledReason == nil || *view.DisabledReason != "abuse" || !view.UpdatedAt.Equal(before.UpdatedAt) {
			testInstance.Fatalf("no-op disable changed the row: %+v", view)
		}
		if _, enableErr := fix.service.Update(ctx, appA, created.ID, alice, Patch{Enabled: boolPointer(true)}); !errors.Is(enableErr, ErrDisabledByOperator) {
			testInstance.Fatalf("enable err = %v, want ErrDisabledByOperator", enableErr)
		}
		stored := fix.store.rows[created.ID]
		if stored.DisabledBy == nil || *stored.DisabledBy != DisabledByOperator || *stored.DisabledReason != "abuse" {
			testInstance.Fatalf("operator disable lost: %+v", stored)
		}
	})

	testInstance.Run("no-op disable with label applies label and keeps disable fields", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		created := fix.create(testInstance, aliceEmail)
		if _, adminErr := fix.service.AdminSetEnabled(ctx, created.ID, false, "abuse"); adminErr != nil {
			testInstance.Fatalf("AdminSetEnabled: %v", adminErr)
		}
		before := *fix.store.rows[created.ID]
		view, updateErr := fix.service.Update(ctx, appA, created.ID, alice,
			Patch{Enabled: boolPointer(false), Label: stringPointer("Renamed")})
		if updateErr != nil || view.Label != "Renamed" || !view.UpdatedAt.After(before.UpdatedAt) ||
			view.DisabledBy == nil || *view.DisabledBy != DisabledByOperator || *view.DisabledReason != "abuse" {
			testInstance.Fatalf("view = %+v, err = %v", view, updateErr)
		}
	})

	testInstance.Run("enable of operator-disabled row with other fields writes nothing", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		created := fix.create(testInstance, aliceEmail)
		if _, adminErr := fix.service.AdminSetEnabled(ctx, created.ID, false, "abuse"); adminErr != nil {
			testInstance.Fatalf("AdminSetEnabled: %v", adminErr)
		}
		before := *fix.store.rows[created.ID]
		updatesBefore := fix.store.updates
		checksBefore := fix.checker.calls
		_, updateErr := fix.service.Update(ctx, appA, created.ID, alice, Patch{
			Enabled: boolPointer(true), Label: stringPointer("Sneaky"),
			Secrets: map[string]string{"webhook_url": otherDiscord},
		})
		if !errors.Is(updateErr, ErrDisabledByOperator) {
			testInstance.Fatalf("err = %v, want ErrDisabledByOperator", updateErr)
		}
		after := fix.store.rows[created.ID]
		if after.Label != before.Label || !after.UpdatedAt.Equal(before.UpdatedAt) || fix.store.updates != updatesBefore ||
			fix.checker.calls != checksBefore {
			testInstance.Fatalf("rejected patch wrote or checked: label=%q updates=%d checks=%d", after.Label, fix.store.updates, fix.checker.calls)
		}
	})
}

func TestCheckOrderPersonThenRegistrarThenOperator(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	ctx := context.Background()
	if _, adminErr := fix.service.AdminSetEnabled(ctx, created.ID, false, ""); adminErr != nil {
		testInstance.Fatalf("AdminSetEnabled: %v", adminErr)
	}
	enable := Patch{Enabled: boolPointer(true)}
	if _, updateErr := fix.service.Update(ctx, appB, created.ID, PersonRef{Email: bobEmail}, enable); !errors.Is(updateErr, ErrNotFound) {
		testInstance.Fatalf("wrong person first = %v", updateErr)
	}
	if _, updateErr := fix.service.Update(ctx, appB, created.ID, PersonRef{Email: aliceEmail}, enable); !errors.Is(updateErr, ErrManagedByOtherApp) {
		testInstance.Fatalf("registrar second = %v", updateErr)
	}
	if _, updateErr := fix.service.Update(ctx, appA, created.ID, PersonRef{Email: aliceEmail}, enable); !errors.Is(updateErr, ErrDisabledByOperator) {
		testInstance.Fatalf("operator third = %v", updateErr)
	}
}

func TestRegistrarRemovedRowIsNotManageable(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	removedAt := fix.store.clock
	fix.store.rows[created.ID].RegistrarRemovedAt = &removedAt
	alice := PersonRef{Email: aliceEmail}
	if _, updateErr := fix.service.Update(context.Background(), appA, created.ID, alice, Patch{Label: stringPointer("x")}); !errors.Is(updateErr, ErrManagedByOtherApp) {
		testInstance.Fatalf("update = %v", updateErr)
	}
	if deleteErr := fix.service.Delete(context.Background(), appA, created.ID, alice); !errors.Is(deleteErr, ErrManagedByOtherApp) {
		testInstance.Fatalf("delete = %v", deleteErr)
	}
	_, views, viewsErr := fix.service.Lookup(context.Background(), appA, alice)
	if viewsErr != nil || len(views) != 1 {
		testInstance.Fatalf("Lookup = %v, %v", views, viewsErr)
	}
	if views[0].ManagedByCaller {
		testInstance.Fatal("managed_by_caller true for a registrar-removed row")
	}
}

func TestUpdateSecretsMergeAndURLRecheck(testInstance *testing.T) {
	fix := newFixture(testInstance)
	ctx := context.Background()
	alice := PersonRef{Email: aliceEmail}
	view, createErr := fix.service.Create(ctx, appA, CreateInput{
		Person: alice, Label: "Hook", Type: TypeWebhook,
		Secrets: map[string]string{"url": webhookURL, "signing_secret": signingSecret},
	})
	if createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}
	callsAfterCreate := fix.checker.calls

	// A signing-secret change keeps the URL and does not re-run the SSRF check.
	newSecret := strings.Repeat("z", 40)
	if _, updateErr := fix.service.Update(ctx, appA, view.ID, alice, Patch{Secrets: map[string]string{"signing_secret": newSecret}}); updateErr != nil {
		testInstance.Fatalf("secret change: %v", updateErr)
	}
	if fix.checker.calls != callsAfterCreate {
		testInstance.Fatalf("checker ran for a non-URL change (%d calls)", fix.checker.calls)
	}
	opened, openErr := OpenSecrets(fix.store.sealer, view.ID, fix.store.rows[view.ID].SealedSecrets)
	if openErr != nil || opened["signing_secret"] != newSecret || opened["url"] != webhookURL {
		testInstance.Fatalf("merge result = %v, %v", opened, openErr)
	}

	// A URL change re-runs it, and a refusal writes nothing.
	fix.checker.err = &registry.ValidationError{Message: "secrets.url resolves to a private address"}
	sealedBefore := slices.Clone(fix.store.rows[view.ID].SealedSecrets)
	_, refusedErr := fix.service.Update(ctx, appA, view.ID, alice, Patch{Secrets: map[string]string{"url": "https://10.0.0.1/x"}})
	if refusedErr == nil || fix.checker.calls != callsAfterCreate+1 {
		testInstance.Fatalf("err = %v, checker calls = %d", refusedErr, fix.checker.calls)
	}
	if !slices.Equal(sealedBefore, fix.store.rows[view.ID].SealedSecrets) {
		testInstance.Fatal("a refused URL change altered the stored secrets")
	}

	// The same URL again is not re-checked.
	fix.checker.err = nil
	if _, updateErr := fix.service.Update(ctx, appA, view.ID, alice, Patch{Secrets: map[string]string{"url": webhookURL}}); updateErr != nil {
		testInstance.Fatalf("same URL: %v", updateErr)
	}
	if fix.checker.calls != callsAfterCreate+1 {
		testInstance.Fatalf("checker ran for an unchanged URL (%d calls)", fix.checker.calls)
	}
}

func TestUpdateURLChangeWithoutCheckerFailsClosed(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	service := NewService(fix.store, fix.store.sealer, nil)
	_, updateErr := service.Update(context.Background(), appA, created.ID, PersonRef{Email: aliceEmail},
		Patch{Secrets: map[string]string{"webhook_url": otherDiscord}})
	if !errors.Is(updateErr, registry.ErrURLCheckUnavailable) {
		testInstance.Fatalf("err = %v, want ErrURLCheckUnavailable", updateErr)
	}
}

func TestUpdateWithNoChangeLeavesUpdatedAt(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	view, updateErr := fix.service.Update(context.Background(), appA, created.ID, PersonRef{Email: aliceEmail},
		Patch{Label: stringPointer("Phone"), Enabled: boolPointer(true)})
	if updateErr != nil || !view.UpdatedAt.Equal(created.UpdatedAt) {
		testInstance.Fatalf("view = %+v, err = %v; updated_at must not move", view, updateErr)
	}
}

func TestViewsNeverContainSecretValues(testInstance *testing.T) {
	fix := newFixture(testInstance)
	ctx := context.Background()
	created, createErr := fix.service.Create(ctx, appA, CreateInput{
		Person: PersonRef{Email: aliceEmail}, Label: "Hook", Type: TypeWebhook,
		Secrets: map[string]string{"url": webhookURL + "/secret-path", "signing_secret": signingSecret},
	})
	if createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}
	_, lookedUp, lookupErr := fix.service.Lookup(ctx, appB, PersonRef{Email: aliceEmail})
	admin, _, adminErr := fix.service.AdminList(ctx, "")
	if lookupErr != nil || adminErr != nil || len(lookedUp) != 1 || len(admin) != 1 {
		testInstance.Fatalf("lookup = %v (%v), admin = %v (%v)", lookedUp, lookupErr, admin, adminErr)
	}
	for name, secrets := range map[string]map[string]string{
		"create": created.Secrets, "lookup": lookedUp[0].Secrets, "admin": admin[0].Secrets,
	} {
		if _, failed := secrets[secretsErrorKey]; failed || len(secrets) != 2 {
			testInstance.Fatalf("%s secrets = %v, want the url and signing_secret keys masked", name, secrets)
		}
		rendered := strings.Join(mapValues(secrets), "|")
		if strings.Contains(rendered, signingSecret) || strings.Contains(rendered, "secret-path") {
			testInstance.Fatalf("%s view leaks a secret: %q", name, rendered)
		}
		if secrets["url"] != "https://hooks.example.com/****" || secrets["signing_secret"] != maskedValue {
			testInstance.Fatalf("%s secrets = %v, want masked values", name, secrets)
		}
	}
}

func TestInvalidInputBranches(testInstance *testing.T) {
	fix := newFixture(testInstance)
	ctx := context.Background()
	created := fix.create(testInstance, aliceEmail)
	alice := PersonRef{Email: aliceEmail}

	if _, createErr := fix.service.Create(ctx, "", CreateInput{Person: alice}); validationMessage(createErr) == "" {
		testInstance.Fatalf("empty app id Create err = %v, want a validation error", createErr)
	}
	if _, optErr := fix.service.OptIn(ctx, "", created.ID, alice); validationMessage(optErr) == "" {
		testInstance.Fatalf("empty app id OptIn err = %v, want a validation error", optErr)
	}
	updatesBefore := fix.store.updates
	if _, labelErr := fix.service.Update(ctx, appA, created.ID, alice, Patch{Label: stringPointer("\t")}); validationMessage(labelErr) != msgInvalidLabel {
		testInstance.Fatalf("invalid label Update err = %v", labelErr)
	}
	if _, fieldsErr := fix.service.Update(ctx, appA, created.ID, alice,
		Patch{Secrets: map[string]string{"bogus": "x"}}); validationMessage(fieldsErr) == "" {
		testInstance.Fatalf("invalid merged secrets Update err = %v, want a validation error", fieldsErr)
	}
	if fix.store.updates != updatesBefore {
		testInstance.Fatal("a rejected Update wrote")
	}
	if _, reasonErr := fix.service.AdminSetEnabled(ctx, created.ID, false, "bad\x07reason"); validationMessage(reasonErr) != msgInvalidReason {
		testInstance.Fatalf("control character reason err = %v", reasonErr)
	}
}

func TestConfigOnlyPatchMergesPerKey(testInstance *testing.T) {
	fix := newFixture(testInstance)
	ctx := context.Background()
	alice := PersonRef{Email: aliceEmail}
	view, createErr := fix.service.Create(ctx, appA, CreateInput{
		Person: alice, Label: "Hook", Type: TypeWebhook,
		Secrets: map[string]string{"url": webhookURL, "signing_secret": signingSecret},
	})
	if createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}
	expiry := "2026-12-01T00:00:00Z"
	previous := strings.Repeat("p", 40)
	updated, updateErr := fix.service.Update(ctx, appA, view.ID, alice, Patch{
		Config:  map[string]string{"previous_secret_expires_at": expiry},
		Secrets: map[string]string{"signing_secret_previous": previous},
	})
	if updateErr != nil || updated.Config["previous_secret_expires_at"] != expiry || len(updated.Secrets) != 3 {
		testInstance.Fatalf("merge = %+v, %v", updated, updateErr)
	}
}

func TestMutableFieldsEqual(testInstance *testing.T) {
	app, operator := DisabledByApp, DisabledByOperator
	appCopy := DisabledByApp
	reason, otherReason := "a", "b"
	base := Destination{Label: "x", Config: map[string]string{}, SealedSecrets: []byte{1, 2}, Enabled: true}
	testCases := []struct {
		name   string
		mutate func(Destination) Destination
		equal  bool
	}{
		{name: "identical", mutate: func(dest Destination) Destination { return dest }, equal: true},
		{name: "nil vs empty config", mutate: func(dest Destination) Destination { dest.Config = nil; return dest }, equal: true},
		{name: "label", mutate: func(dest Destination) Destination { dest.Label = "y"; return dest }},
		{name: "config", mutate: func(dest Destination) Destination { dest.Config = map[string]string{"k": "v"}; return dest }},
		{name: "sealed bytes", mutate: func(dest Destination) Destination { dest.SealedSecrets = []byte{1, 3}; return dest }},
		{name: "enabled", mutate: func(dest Destination) Destination { dest.Enabled = false; return dest }},
		{name: "disabled_by set", mutate: func(dest Destination) Destination { dest.DisabledBy = &app; return dest }},
		{name: "disabled_reason set", mutate: func(dest Destination) Destination { dest.DisabledReason = &reason; return dest }},
		{name: "disabled_by nil vs set copy", mutate: func(dest Destination) Destination { dest.DisabledBy = &appCopy; return dest }},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			if got := mutableFieldsEqual(base, testCase.mutate(base)); got != testCase.equal {
				testInstance.Fatalf("mutableFieldsEqual = %v, want %v", got, testCase.equal)
			}
		})
	}
	left, right := base, base
	left.DisabledBy, right.DisabledBy = &app, &appCopy
	left.DisabledReason, right.DisabledReason = &reason, &reason
	if !mutableFieldsEqual(left, right) {
		testInstance.Fatal("equal pointee values behind different pointers must compare equal")
	}
	right.DisabledBy = &operator
	if mutableFieldsEqual(left, right) {
		testInstance.Fatal("different disabled_by values must differ")
	}
	right.DisabledBy, right.DisabledReason = &app, &otherReason
	if mutableFieldsEqual(left, right) {
		testInstance.Fatal("different disabled_reason values must differ")
	}
}

func mapValues(values map[string]string) []string {
	collected := []string{}
	for _, value := range values {
		collected = append(collected, value)
	}
	return collected
}

func TestAdminOperations(testInstance *testing.T) {
	fix := newFixture(testInstance)
	ctx := context.Background()
	first := fix.create(testInstance, aliceEmail)
	fix.create(testInstance, bobEmail)
	if _, optErr := fix.service.OptIn(ctx, appB, first.ID, PersonRef{Email: aliceEmail}); optErr != nil {
		testInstance.Fatalf("OptIn: %v", optErr)
	}

	all, total, listErr := fix.service.AdminList(ctx, "")
	if listErr != nil || total != 2 || len(all) != 2 {
		testInstance.Fatalf("AdminList = %d rows, total %d, %v", len(all), total, listErr)
	}
	filtered, total, filterErr := fix.service.AdminList(ctx, personhash.Hash(aliceEmail))
	if filterErr != nil {
		testInstance.Fatalf("filtered AdminList: %v", filterErr)
	}
	if total != 1 || len(filtered) != 1 || !slices.Equal(filtered[0].OptedInAppIDs, []string{appA, appB}) || filtered[0].RegistrarRemoved {
		testInstance.Fatalf("filtered = %+v total %d", filtered, total)
	}
	if _, _, badErr := fix.service.AdminList(ctx, "NOT-A-HASH"); validationMessage(badErr) != msgInvalidPerson {
		testInstance.Fatalf("bad filter err = %v", badErr)
	}

	disabled, disableErr := fix.service.AdminSetEnabled(ctx, first.ID, false, "  spam  ")
	if disableErr != nil || disabled.Enabled || *disabled.DisabledBy != DisabledByOperator || *disabled.DisabledReason != "spam" {
		testInstance.Fatalf("disable = %+v, %v", disabled, disableErr)
	}
	enabled, enableErr := fix.service.AdminSetEnabled(ctx, first.ID, true, "")
	if enableErr != nil || !enabled.Enabled || enabled.DisabledBy != nil || enabled.DisabledReason != nil {
		testInstance.Fatalf("enable = %+v, %v", enabled, enableErr)
	}
	if _, reasonErr := fix.service.AdminSetEnabled(ctx, first.ID, false, strings.Repeat("x", 201)); validationMessage(reasonErr) != msgInvalidReason {
		testInstance.Fatalf("long reason err = %v", reasonErr)
	}
	if _, absentErr := fix.service.AdminSetEnabled(ctx, 999, false, ""); !errors.Is(absentErr, ErrNotFound) {
		testInstance.Fatalf("absent = %v", absentErr)
	}
	if deleteErr := fix.service.AdminDelete(ctx, first.ID); deleteErr != nil {
		testInstance.Fatalf("AdminDelete: %v", deleteErr)
	}
	if deleteErr := fix.service.AdminDelete(ctx, first.ID); !errors.Is(deleteErr, ErrNotFound) {
		testInstance.Fatalf("second AdminDelete = %v", deleteErr)
	}
}

func TestAdminViewMarksRegistrarRemoved(testInstance *testing.T) {
	fix := newFixture(testInstance)
	created := fix.create(testInstance, aliceEmail)
	removedAt := fix.store.clock
	fix.store.rows[created.ID].RegistrarRemovedAt = &removedAt
	views, _, listErr := fix.service.AdminList(context.Background(), "")
	if listErr != nil || len(views) != 1 {
		testInstance.Fatalf("AdminList = %v, %v", views, listErr)
	}
	if !views[0].RegistrarRemoved {
		testInstance.Fatal("registrar_removed false")
	}
}

func TestForgetPersonForAppAndForgetPerson(testInstance *testing.T) {
	fix := newFixture(testInstance)
	ctx := context.Background()
	aliceHash := personhash.Hash(aliceEmail)
	own := fix.create(testInstance, aliceEmail)
	if forgetErr := fix.service.ForgetPersonForApp(ctx, appA, aliceHash); forgetErr != nil {
		testInstance.Fatalf("ForgetPersonForApp: %v", forgetErr)
	}
	if _, stillThere := fix.store.rows[own.ID]; stillThere {
		testInstance.Fatal("a row registered by the forgetting app with only its opt-in survived")
	}
	if validationMessage(fix.service.ForgetPersonForApp(ctx, appA, "bad")) != msgInvalidPerson {
		testInstance.Fatal("invalid hash not rejected")
	}
	if validationMessage(fix.service.ForgetPersonForApp(ctx, "BAD APP", aliceHash)) == "" {
		testInstance.Fatal("invalid app id not rejected")
	}
	kept := fix.create(testInstance, aliceEmail)
	if forgetErr := fix.service.ForgetPerson(ctx, aliceHash); forgetErr != nil {
		testInstance.Fatalf("ForgetPerson: %v", forgetErr)
	}
	if _, stillThere := fix.store.rows[kept.ID]; stillThere {
		testInstance.Fatal("ForgetPerson left a row")
	}
	if validationMessage(fix.service.ForgetPerson(ctx, "bad")) != msgInvalidPerson {
		testInstance.Fatal("invalid hash not rejected by ForgetPerson")
	}
}

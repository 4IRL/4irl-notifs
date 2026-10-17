package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	testMasterKey = "0123456789abcdef0123456789abcdef"
	firstToken    = "tk_abcdefghijklmnopqrstuvwxyz012"
	secondToken   = "tk_zyxwvutsrqponmlkjihgfedcba210"
)

// fakeStore is an in-memory Store.
type fakeStore struct {
	rows        map[string]StoredPublisher
	upsertCalls int
	deleteCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]StoredPublisher{}}
}

func (store *fakeStore) Upsert(_ context.Context, publisher StoredPublisher) (StoredPublisher, error) {
	store.upsertCalls++
	now := time.Date(2026, 10, 17, 12, 0, store.upsertCalls, 0, time.UTC)
	if existing, found := store.rows[publisher.AppID]; found {
		publisher.CreatedAt = existing.CreatedAt
	} else {
		publisher.CreatedAt = now
	}
	publisher.UpdatedAt = now
	store.rows[publisher.AppID] = publisher
	return publisher, nil
}

func (store *fakeStore) Get(_ context.Context, appID string) (StoredPublisher, error) {
	publisher, found := store.rows[appID]
	if !found {
		return StoredPublisher{}, registry.ErrNoPublisher
	}
	return publisher, nil
}

func (store *fakeStore) Delete(_ context.Context, appID string) error {
	store.deleteCalls++
	delete(store.rows, appID)
	return nil
}

// fakeMinter returns queued grants, or err when set.
type fakeMinter struct {
	grants []provisionclient.PublisherGrant
	err    error
	calls  []string
}

func (minter *fakeMinter) MintPublisher(_ context.Context, appID string) (provisionclient.PublisherGrant, error) {
	minter.calls = append(minter.calls, appID)
	if minter.err != nil {
		return provisionclient.PublisherGrant{}, minter.err
	}
	grant := minter.grants[0]
	if len(minter.grants) > 1 {
		minter.grants = minter.grants[1:]
	}
	return grant, nil
}

func grantFor(appID, token string) provisionclient.PublisherGrant {
	return provisionclient.PublisherGrant{
		AppID: appID, PublisherUserID: "app-" + appID, TopicPattern: appID + "-*", Token: token,
	}
}

func newSealer(testInstance *testing.T) *seal.Sealer {
	testInstance.Helper()
	sealer, sealerErr := seal.New([]byte(testMasterKey))
	if sealerErr != nil {
		testInstance.Fatalf("seal.New: %v", sealerErr)
	}
	return sealer
}

func TestMintSealsTokenWithAppBoundAADAndUpserts(testInstance *testing.T) {
	store := newFakeStore()
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
	sealer := newSealer(testInstance)
	service := NewService(store, minter, sealer)

	view, mintErr := service.Mint(context.Background(), "urls4irl")
	if mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	if view.AppID != "urls4irl" || view.PublisherUserID != "app-urls4irl" || view.TopicPattern != "urls4irl-*" {
		testInstance.Errorf("view = %+v", view)
	}
	if !reflect.DeepEqual(minter.calls, []string{"urls4irl"}) {
		testInstance.Errorf("minter calls = %v, want [urls4irl]", minter.calls)
	}
	stored, found := store.rows["urls4irl"]
	if !found {
		testInstance.Fatal("nothing stored for urls4irl")
	}
	if bytes.Contains(stored.SealedToken, []byte(firstToken)) {
		testInstance.Error("stored blob contains the plaintext token")
	}
	opened, openErr := sealer.Open(stored.SealedToken, []byte("delivery-api/app-publisher/v1:urls4irl"))
	if openErr != nil || string(opened) != firstToken {
		testInstance.Errorf("Open with the documented AAD = %q, %v; want the token", opened, openErr)
	}
}

func TestMintTwiceReplacesSealedToken(testInstance *testing.T) {
	store := newFakeStore()
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{
		grantFor("urls4irl", firstToken), grantFor("urls4irl", secondToken),
	}}
	service := NewService(store, minter, newSealer(testInstance))

	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr != nil {
		testInstance.Fatalf("first Mint: %v", mintErr)
	}
	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr != nil {
		testInstance.Fatalf("second Mint: %v", mintErr)
	}

	if len(store.rows) != 1 {
		testInstance.Errorf("stored rows = %d, want 1", len(store.rows))
	}
	token, tokenErr := service.Token(context.Background(), "urls4irl")
	if tokenErr != nil || token != secondToken {
		testInstance.Errorf("Token = %q, %v; want the second token", token, tokenErr)
	}
}

func TestTokenOpensStoredToken(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))
	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	token, tokenErr := service.Token(context.Background(), "urls4irl")
	if tokenErr != nil || token != firstToken {
		testInstance.Errorf("Token = %q, %v; want %q", token, tokenErr, firstToken)
	}
}

func TestTokenRejectsBlobSealedForAnotherApp(testInstance *testing.T) {
	store := newFakeStore()
	sealer := newSealer(testInstance)
	otherAppBlob, sealErr := sealer.Seal([]byte(firstToken), []byte("delivery-api/app-publisher/v1:otherapp"))
	if sealErr != nil {
		testInstance.Fatalf("Seal: %v", sealErr)
	}
	store.rows["urls4irl"] = StoredPublisher{AppID: "urls4irl", SealedToken: otherAppBlob}
	service := NewService(store, nil, sealer)

	token, tokenErr := service.Token(context.Background(), "urls4irl")
	if tokenErr == nil {
		testInstance.Fatalf("Token = %q, want an error for a blob sealed under another app's AAD", token)
	}
	if token != "" {
		testInstance.Errorf("token = %q returned alongside an error", token)
	}
	if errors.Is(tokenErr, registry.ErrNoPublisher) {
		testInstance.Error("a decrypt failure must not read as ErrNoPublisher")
	}
	if strings.Contains(tokenErr.Error(), firstToken) {
		testInstance.Error("error leaks the token")
	}
}

func TestUnknownAppHasNoPublisher(testInstance *testing.T) {
	service := NewService(newFakeStore(), nil, newSealer(testInstance))

	if _, tokenErr := service.Token(context.Background(), "ghost"); !errors.Is(tokenErr, registry.ErrNoPublisher) {
		testInstance.Errorf("Token err = %v, want ErrNoPublisher", tokenErr)
	}
	if _, getErr := service.Get(context.Background(), "ghost"); !errors.Is(getErr, registry.ErrNoPublisher) {
		testInstance.Errorf("Get err = %v, want ErrNoPublisher", getErr)
	}
}

func TestGetReturnsTheMintedViewWithoutAToken(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))
	minted, mintErr := service.Mint(context.Background(), "urls4irl")
	if mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	got, getErr := service.Get(context.Background(), "urls4irl")
	if getErr != nil {
		testInstance.Fatalf("Get: %v", getErr)
	}
	if !reflect.DeepEqual(got, minted) {
		testInstance.Errorf("Get = %+v, want the minted view %+v", got, minted)
	}
	encoded, marshalErr := json.Marshal(got)
	if marshalErr != nil {
		testInstance.Fatalf("Marshal: %v", marshalErr)
	}
	if strings.Contains(string(encoded), firstToken) {
		testInstance.Errorf("Get view JSON %s exposes the token", encoded)
	}
}

func TestMintReturnsTheUpsertErrorUnchanged(testInstance *testing.T) {
	storeErr := errors.New("db down")
	service := NewService(&failingStore{err: storeErr},
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))

	_, mintErr := service.Mint(context.Background(), "urls4irl")
	if mintErr != storeErr { //nolint:errorlint // the contract is that the error is returned unchanged
		testInstance.Errorf("Mint err = %v, want the upsert error returned unchanged", mintErr)
	}
}

func TestResolveRecipientBuildsAnNtfyDestinationFromTheAppToken(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))
	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	got, resolveErr := service.ResolveRecipient(context.Background(), "urls4irl", "76gzqgp4byjl6dje", "all")
	if resolveErr != nil {
		testInstance.Fatalf("ResolveRecipient: %v", resolveErr)
	}
	want := registry.ResolvedDestination{
		ID:      0,
		Name:    registry.RecipientDestinationName,
		Type:    registry.TypeNtfy,
		Config:  map[string]string{"topic": "urls4irl-76gzqgp4byjl6dje-all"},
		Secrets: map[string]string{"access_token": firstToken},
	}
	if !reflect.DeepEqual(got, want) {
		testInstance.Errorf("ResolveRecipient = %+v, want %+v", got, want)
	}
}

func TestResolveRecipientWithoutPublisherIsErrNoPublisher(testInstance *testing.T) {
	service := NewService(newFakeStore(), nil, newSealer(testInstance))

	_, resolveErr := service.ResolveRecipient(context.Background(), "ghost", "76gzqgp4byjl6dje", "all")
	if !errors.Is(resolveErr, registry.ErrNoPublisher) {
		testInstance.Errorf("ResolveRecipient err = %v, want ErrNoPublisher", resolveErr)
	}
}

func TestResolveRecipientRejectsANonHashRecipient(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store,
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))
	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	for _, recipient := range []string{"alice@example.com", "", "76GZQGP4BYJL6DJE", "short"} {
		got, resolveErr := service.ResolveRecipient(context.Background(), "urls4irl", recipient, "all")
		if resolveErr == nil {
			testInstance.Errorf("ResolveRecipient(%q) = %+v, want an error (fail closed)", recipient, got)
			continue
		}
		if errors.Is(resolveErr, registry.ErrNoPublisher) {
			testInstance.Errorf("ResolveRecipient(%q): a bad recipient must not read as ErrNoPublisher", recipient)
		}
		if got.Secrets != nil {
			testInstance.Errorf("ResolveRecipient(%q) returned secrets alongside an error", recipient)
		}
	}
}

func TestHasPublisher(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))

	has, hasErr := service.HasPublisher(context.Background(), "urls4irl")
	if hasErr != nil || has {
		testInstance.Errorf("before Mint: HasPublisher = %v, %v; want false, nil", has, hasErr)
	}
	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}
	has, hasErr = service.HasPublisher(context.Background(), "urls4irl")
	if hasErr != nil || !has {
		testInstance.Errorf("after Mint: HasPublisher = %v, %v; want true, nil", has, hasErr)
	}
}

func TestHasPublisherPropagatesStoreFailure(testInstance *testing.T) {
	storeErr := errors.New("db down")
	service := NewService(&failingStore{err: storeErr}, nil, newSealer(testInstance))

	has, hasErr := service.HasPublisher(context.Background(), "urls4irl")
	if !errors.Is(hasErr, storeErr) || has {
		testInstance.Errorf("HasPublisher = %v, %v; want false and the store error", has, hasErr)
	}
}

// failingStore fails every call with err.
type failingStore struct{ err error }

func (store *failingStore) Upsert(context.Context, StoredPublisher) (StoredPublisher, error) {
	return StoredPublisher{}, store.err
}

func (store *failingStore) Get(context.Context, string) (StoredPublisher, error) {
	return StoredPublisher{}, store.err
}

func (store *failingStore) Delete(context.Context, string) error { return store.err }

func TestRemoveDeletesThePublisherSoRecipientSubmitsFailClosed(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store,
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))
	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	if removeErr := service.Remove(context.Background(), "urls4irl"); removeErr != nil {
		testInstance.Fatalf("Remove: %v", removeErr)
	}

	has, hasErr := service.HasPublisher(context.Background(), "urls4irl")
	if hasErr != nil || has {
		testInstance.Errorf("HasPublisher after Remove = %v, %v; want false, nil", has, hasErr)
	}
	if _, getErr := service.Get(context.Background(), "urls4irl"); !errors.Is(getErr, registry.ErrNoPublisher) {
		testInstance.Errorf("Get after Remove err = %v, want registry.ErrNoPublisher", getErr)
	}
}

func TestRemoveIsIdempotent(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store, nil, newSealer(testInstance))

	for attempt := 0; attempt < 2; attempt++ {
		if removeErr := service.Remove(context.Background(), "urls4irl"); removeErr != nil {
			testInstance.Fatalf("Remove attempt %d on an app with no publisher: %v", attempt, removeErr)
		}
	}
	if store.deleteCalls != 2 {
		testInstance.Errorf("store delete calls = %d, want 2", store.deleteCalls)
	}
}

func TestRemoveRejectsInvalidAppIDBeforeTheStore(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store, nil, newSealer(testInstance))

	removeErr := service.Remove(context.Background(), "Bad-App")

	var validationErr *ValidationError
	if !errors.As(removeErr, &validationErr) {
		testInstance.Fatalf("err = %v, want *ValidationError", removeErr)
	}
	if store.deleteCalls != 0 {
		testInstance.Errorf("store delete calls = %d for an invalid app_id, want 0", store.deleteCalls)
	}
}

func TestRemovePropagatesStoreFailure(testInstance *testing.T) {
	storeErr := errors.New("db down")
	service := NewService(&failingStore{err: storeErr}, nil, newSealer(testInstance))

	if removeErr := service.Remove(context.Background(), "urls4irl"); !errors.Is(removeErr, storeErr) {
		testInstance.Errorf("Remove err = %v, want the store error", removeErr)
	}
}

func TestMintRejectsInvalidAppIDBeforeCallingMinter(testInstance *testing.T) {
	for _, appID := range []string{"", "Bad-App", "_leading", "has space", "UPPER", strings.Repeat("a", 64)} {
		testInstance.Run("app_id "+appID, func(subTest *testing.T) {
			store := newFakeStore()
			minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
			service := NewService(store, minter, newSealer(subTest))

			_, mintErr := service.Mint(context.Background(), appID)

			var validationErr *ValidationError
			if !errors.As(mintErr, &validationErr) {
				subTest.Fatalf("err = %v, want *ValidationError", mintErr)
			}
			if len(minter.calls) != 0 {
				subTest.Errorf("minter called %d times for an invalid app_id", len(minter.calls))
			}
			if len(store.rows) != 0 {
				subTest.Error("a row was stored for an invalid app_id")
			}
		})
	}
}

func TestMintAcceptsBoundaryAppIDs(testInstance *testing.T) {
	for _, appID := range []string{"a", "0app", "my_app_2", strings.Repeat("a", 63)} {
		testInstance.Run(appID, func(subTest *testing.T) {
			minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor(appID, firstToken)}}
			service := NewService(newFakeStore(), minter, newSealer(subTest))
			if _, mintErr := service.Mint(context.Background(), appID); mintErr != nil {
				subTest.Fatalf("Mint(%q): %v", appID, mintErr)
			}
		})
	}
}

func TestMintReturnsMinterErrorAndStoresNothing(testInstance *testing.T) {
	store := newFakeStore()
	minterErr := errors.New("provisioning-api unreachable")
	service := NewService(store, &fakeMinter{err: minterErr}, newSealer(testInstance))

	_, mintErr := service.Mint(context.Background(), "urls4irl")
	if !errors.Is(mintErr, minterErr) {
		testInstance.Errorf("err = %v, want the minter error", mintErr)
	}
	if store.upsertCalls != 0 || len(store.rows) != 0 {
		testInstance.Errorf("store touched after a minter error: %d upserts, %d rows", store.upsertCalls, len(store.rows))
	}
}

func TestMintWithoutMinterIsUnavailable(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store, nil, newSealer(testInstance))

	_, mintErr := service.Mint(context.Background(), "urls4irl")
	if !errors.Is(mintErr, ErrMintUnavailable) {
		testInstance.Errorf("err = %v, want ErrMintUnavailable", mintErr)
	}
	if len(store.rows) != 0 {
		testInstance.Error("a row was stored with no minter")
	}
}

func TestMintRejectsGrantForAnotherApp(testInstance *testing.T) {
	store := newFakeStore()
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("otherapp", firstToken)}}
	service := NewService(store, minter, newSealer(testInstance))

	if _, mintErr := service.Mint(context.Background(), "urls4irl"); mintErr == nil {
		testInstance.Fatal("Mint stored a grant minted for a different app")
	}
	if len(store.rows) != 0 {
		testInstance.Error("a row was stored for a mismatched grant")
	}
}

func TestPublisherViewHasNoTokenField(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance))
	view, mintErr := service.Mint(context.Background(), "urls4irl")
	if mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	viewType := reflect.TypeOf(view)
	for index := range viewType.NumField() {
		fieldName := strings.ToLower(viewType.Field(index).Name)
		if strings.Contains(fieldName, "token") || strings.Contains(fieldName, "seal") {
			testInstance.Errorf("PublisherView has secret-bearing field %q", viewType.Field(index).Name)
		}
	}
	encoded, marshalErr := json.Marshal(view)
	if marshalErr != nil {
		testInstance.Fatalf("Marshal: %v", marshalErr)
	}
	for _, key := range []string{"app_id", "publisher_user_id", "topic_pattern", "created_at", "updated_at"} {
		if !strings.Contains(string(encoded), `"`+key+`"`) {
			testInstance.Errorf("view JSON %s lacks snake_case key %q", encoded, key)
		}
	}
	if strings.Contains(string(encoded), firstToken) || strings.Contains(strings.ToLower(string(encoded)), "token") {
		testInstance.Errorf("view JSON %s exposes token material", encoded)
	}
}

func TestStoredPublisherNeverSerializesSealedToken(testInstance *testing.T) {
	encoded, marshalErr := json.Marshal(StoredPublisher{AppID: "urls4irl", SealedToken: []byte("sealed-bytes")})
	if marshalErr != nil {
		testInstance.Fatalf("Marshal: %v", marshalErr)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "sealed") {
		testInstance.Errorf("StoredPublisher JSON %s includes the sealed token", encoded)
	}
}

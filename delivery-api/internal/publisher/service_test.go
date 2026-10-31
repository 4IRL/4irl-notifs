package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
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
	mutex       sync.Mutex
	rows        map[string]StoredPublisher
	upsertCalls int
	deleteCalls int

	// Mint lock and slot scripting. held is the per-app try-lock.
	held         map[string]bool
	lockCalls    int
	lockErr      error // returned before fn, as PGStore does for a begin/lock failure
	commitErr    error // returned after fn succeeded
	reserveCalls int
	reserveFn    func(appID string) error
	upsertErr    error
	upsertWait   bool // Upsert blocks until its ctx ends
	upsertCtxs   []ctxState
}

// ctxState is what an Upsert saw of its context at call time.
type ctxState struct {
	err         error
	deadline    time.Time
	hasDeadline bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]StoredPublisher{}, held: map[string]bool{}}
}

// WithAppMintLock is a per-app try-lock: held -> ErrMintBusy without calling fn; fn's error still
// "commits"; the lock is released by defer, so a panic in fn releases it too.
func (store *fakeStore) WithAppMintLock(_ context.Context, appID string, fn func(tx MintTx) error) error {
	store.mutex.Lock()
	store.lockCalls++
	if store.lockErr != nil {
		store.mutex.Unlock()
		return store.lockErr
	}
	if store.held[appID] {
		store.mutex.Unlock()
		return ErrMintBusy
	}
	store.held[appID] = true
	store.mutex.Unlock()
	defer func() {
		store.mutex.Lock()
		delete(store.held, appID)
		store.mutex.Unlock()
	}()
	if fnErr := fn(fakeMintTx{store: store}); fnErr != nil {
		return fnErr
	}
	return store.commitErr
}

type fakeMintTx struct{ store *fakeStore }

func (tx fakeMintTx) ReserveMintSlot(_ context.Context, appID string, _ int, _ time.Duration) error {
	tx.store.mutex.Lock()
	tx.store.reserveCalls++
	reserveFn := tx.store.reserveFn
	tx.store.mutex.Unlock()
	if reserveFn != nil {
		return reserveFn(appID)
	}
	return nil
}

func (tx fakeMintTx) Upsert(ctx context.Context, publisher StoredPublisher) (StoredPublisher, error) {
	tx.store.mutex.Lock()
	deadline, hasDeadline := ctx.Deadline()
	tx.store.upsertCtxs = append(tx.store.upsertCtxs, ctxState{err: ctx.Err(), deadline: deadline, hasDeadline: hasDeadline})
	upsertErr, upsertWait := tx.store.upsertErr, tx.store.upsertWait
	tx.store.mutex.Unlock()
	if upsertWait {
		<-ctx.Done()
		return StoredPublisher{}, ctx.Err()
	}
	if upsertErr != nil {
		return StoredPublisher{}, upsertErr
	}
	return tx.store.upsertRow(publisher)
}

func (store *fakeStore) upsertRow(publisher StoredPublisher) (StoredPublisher, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
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
	store.mutex.Lock()
	defer store.mutex.Unlock()
	publisher, found := store.rows[appID]
	if !found {
		return StoredPublisher{}, registry.ErrNoPublisher
	}
	return publisher, nil
}

func (store *fakeStore) Delete(_ context.Context, appID string) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.deleteCalls++
	delete(store.rows, appID)
	return nil
}

// fakeMinter returns queued grants, or err when set.
type fakeMinter struct {
	grants  []provisionclient.PublisherGrant
	err     error
	calls   []string
	rotates []bool
}

func (minter *fakeMinter) MintPublisher(_ context.Context, appID string, rotate bool) (provisionclient.PublisherGrant, error) {
	minter.calls = append(minter.calls, appID)
	minter.rotates = append(minter.rotates, rotate)
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
	service := NewService(store, minter, sealer, nil)

	view, mintErr := service.Mint(context.Background(), "urls4irl", false)
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
	service := NewService(store, minter, newSealer(testInstance), nil)

	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
		testInstance.Fatalf("first Mint: %v", mintErr)
	}
	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
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
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)
	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
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
	service := NewService(store, nil, sealer, nil)

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
	service := NewService(newFakeStore(), nil, newSealer(testInstance), nil)

	if _, tokenErr := service.Token(context.Background(), "ghost"); !errors.Is(tokenErr, registry.ErrNoPublisher) {
		testInstance.Errorf("Token err = %v, want ErrNoPublisher", tokenErr)
	}
	if _, getErr := service.Get(context.Background(), "ghost"); !errors.Is(getErr, registry.ErrNoPublisher) {
		testInstance.Errorf("Get err = %v, want ErrNoPublisher", getErr)
	}
}

func TestGetReturnsTheMintedViewWithoutAToken(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)
	minted, mintErr := service.Mint(context.Background(), "urls4irl", false)
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

func TestMintWrapsTheUpsertErrorAsUnavailableKeepingTheCause(testInstance *testing.T) {
	storeErr := errors.New("db down")
	service := NewService(&failingStore{err: storeErr},
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)

	_, mintErr := service.Mint(context.Background(), "urls4irl", false)
	if !errors.Is(mintErr, ErrMintUnavailable) || !errors.Is(mintErr, storeErr) {
		testInstance.Errorf("Mint err = %v, want ErrMintUnavailable wrapping the upsert error", mintErr)
	}
}

func TestResolveRecipientBuildsAnNtfyDestinationFromTheAppToken(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)
	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
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
	service := NewService(newFakeStore(), nil, newSealer(testInstance), nil)

	_, resolveErr := service.ResolveRecipient(context.Background(), "ghost", "76gzqgp4byjl6dje", "all")
	if !errors.Is(resolveErr, registry.ErrNoPublisher) {
		testInstance.Errorf("ResolveRecipient err = %v, want ErrNoPublisher", resolveErr)
	}
}

func TestResolveRecipientRejectsANonHashRecipient(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store,
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)
	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
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
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)

	has, hasErr := service.HasPublisher(context.Background(), "urls4irl")
	if hasErr != nil || has {
		testInstance.Errorf("before Mint: HasPublisher = %v, %v; want false, nil", has, hasErr)
	}
	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}
	has, hasErr = service.HasPublisher(context.Background(), "urls4irl")
	if hasErr != nil || !has {
		testInstance.Errorf("after Mint: HasPublisher = %v, %v; want true, nil", has, hasErr)
	}
}

func TestHasPublisherPropagatesStoreFailure(testInstance *testing.T) {
	storeErr := errors.New("db down")
	service := NewService(&failingStore{err: storeErr}, nil, newSealer(testInstance), nil)

	has, hasErr := service.HasPublisher(context.Background(), "urls4irl")
	if !errors.Is(hasErr, storeErr) || has {
		testInstance.Errorf("HasPublisher = %v, %v; want false and the store error", has, hasErr)
	}
}

// failingStore fails every call with err.
type failingStore struct{ err error }

func (store *failingStore) Get(context.Context, string) (StoredPublisher, error) {
	return StoredPublisher{}, store.err
}

func (store *failingStore) Delete(context.Context, string) error { return store.err }

func (store *failingStore) WithAppMintLock(_ context.Context, _ string, fn func(tx MintTx) error) error {
	return fn(failingMintTx{err: store.err})
}

type failingMintTx struct{ err error }

func (failingMintTx) ReserveMintSlot(context.Context, string, int, time.Duration) error { return nil }

func (tx failingMintTx) Upsert(context.Context, StoredPublisher) (StoredPublisher, error) {
	return StoredPublisher{}, tx.err
}

func TestRemoveDeletesThePublisherSoRecipientSubmitsFailClosed(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store,
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)
	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
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
	service := NewService(store, nil, newSealer(testInstance), nil)

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
	service := NewService(store, nil, newSealer(testInstance), nil)

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
	service := NewService(&failingStore{err: storeErr}, nil, newSealer(testInstance), nil)

	if removeErr := service.Remove(context.Background(), "urls4irl"); !errors.Is(removeErr, storeErr) {
		testInstance.Errorf("Remove err = %v, want the store error", removeErr)
	}
}

func TestMintRejectsInvalidAppIDBeforeCallingMinter(testInstance *testing.T) {
	for _, appID := range []string{"", "Bad-App", "_leading", "has space", "UPPER", strings.Repeat("a", 64)} {
		testInstance.Run("app_id "+appID, func(subTest *testing.T) {
			store := newFakeStore()
			minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
			service := NewService(store, minter, newSealer(subTest), nil)

			_, mintErr := service.Mint(context.Background(), appID, false)

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
			service := NewService(newFakeStore(), minter, newSealer(subTest), nil)
			if _, mintErr := service.Mint(context.Background(), appID, false); mintErr != nil {
				subTest.Fatalf("Mint(%q): %v", appID, mintErr)
			}
		})
	}
}

func TestMintReturnsMinterErrorAndStoresNothing(testInstance *testing.T) {
	store := newFakeStore()
	minterErr := errors.New("provisioning-api unreachable")
	service := NewService(store, &fakeMinter{err: minterErr}, newSealer(testInstance), nil)

	_, mintErr := service.Mint(context.Background(), "urls4irl", false)
	if !errors.Is(mintErr, minterErr) {
		testInstance.Errorf("err = %v, want the minter error", mintErr)
	}
	if store.upsertCalls != 0 || len(store.rows) != 0 {
		testInstance.Errorf("store touched after a minter error: %d upserts, %d rows", store.upsertCalls, len(store.rows))
	}
}

func TestMintWithoutMinterIsUnavailable(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store, nil, newSealer(testInstance), nil)

	_, mintErr := service.Mint(context.Background(), "urls4irl", false)
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
	service := NewService(store, minter, newSealer(testInstance), nil)

	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr == nil {
		testInstance.Fatal("Mint stored a grant minted for a different app")
	}
	if len(store.rows) != 0 {
		testInstance.Error("a row was stored for a mismatched grant")
	}
}

const rotateErrorLine = "publisher rotate attempted but new token not stored"

func rotateServiceFor(testInstance *testing.T, store Store, minter Minter) (*Service, *bytes.Buffer) {
	testInstance.Helper()
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	return NewService(store, minter, newSealer(testInstance), logger), &logBuffer
}

func TestMintForwardsRotateToTheMinter(testInstance *testing.T) {
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
	service, _ := rotateServiceFor(testInstance, newFakeStore(), minter)

	for _, rotate := range []bool{true, false} {
		if _, mintErr := service.Mint(context.Background(), "urls4irl", rotate); mintErr != nil {
			testInstance.Fatalf("Mint(rotate=%v): %v", rotate, mintErr)
		}
	}
	if !reflect.DeepEqual(minter.rotates, []bool{true, false}) {
		testInstance.Errorf("minter rotates = %v, want [true false]", minter.rotates)
	}
}

func TestMintRotateFailureLogsOnlyWhenRotating(testInstance *testing.T) {
	minterErr := errors.New("upstream down")
	storeErr := errors.New("db down")
	cases := []struct {
		name  string
		store Store
		mint  *fakeMinter
		stage string
	}{
		{"minter", newFakeStore(), &fakeMinter{err: minterErr}, "stage=minter"},
		{"app_mismatch", newFakeStore(),
			&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("otherapp", firstToken)}}, "stage=app_mismatch"},
		{"upsert", &failingStore{err: storeErr},
			&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, "stage=upsert"},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			service, logBuffer := rotateServiceFor(subTest, testCase.store, testCase.mint)
			if _, mintErr := service.Mint(context.Background(), "urls4irl", true); mintErr == nil {
				subTest.Fatal("Mint(rotate=true) succeeded, want an error")
			}
			logged := logBuffer.String()
			for _, want := range []string{rotateErrorLine, "level=ERROR", "app_id=urls4irl", testCase.stage} {
				if !strings.Contains(logged, want) {
					subTest.Errorf("log %q lacks %q", logged, want)
				}
			}
			if strings.Contains(logged, firstToken) {
				subTest.Error("log leaks the token")
			}
		})
		testInstance.Run(testCase.name+"/no-rotate", func(subTest *testing.T) {
			fresh := *testCase.mint
			service, logBuffer := rotateServiceFor(subTest, testCase.store, &fresh)
			if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr == nil {
				subTest.Fatal("Mint(rotate=false) succeeded, want an error")
			}
			if strings.Contains(logBuffer.String(), rotateErrorLine) {
				subTest.Errorf("rotate=false logged the rotate error: %s", logBuffer.String())
			}
		})
	}
}

func TestMintRotateWithInvalidAppIDDoesNotLogOrCallTheMinter(testInstance *testing.T) {
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
	service, logBuffer := rotateServiceFor(testInstance, newFakeStore(), minter)

	if _, mintErr := service.Mint(context.Background(), "Bad App!", true); mintErr == nil {
		testInstance.Fatal("Mint accepted an invalid app id")
	}
	if len(minter.calls) != 0 {
		testInstance.Errorf("minter called %v for an invalid app id", minter.calls)
	}
	if strings.Contains(logBuffer.String(), rotateErrorLine) {
		testInstance.Errorf("invalid app id logged the rotate error: %s", logBuffer.String())
	}
}

func TestPublisherViewHasNoTokenField(testInstance *testing.T) {
	service := NewService(newFakeStore(),
		&fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}, newSealer(testInstance), nil)
	view, mintErr := service.Mint(context.Background(), "urls4irl", false)
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

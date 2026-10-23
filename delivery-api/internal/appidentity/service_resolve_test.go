package appidentity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeKeyStore is a scripted Store for the key lookup path. Only ResolveKey and TouchKey are
// scripted; the rest fail loudly because ResolveKey must never reach them.
type fakeKeyStore struct {
	resolved   ResolvedKey
	resolveErr error
	touchErr   error

	resolveCalls []string
	resolvedAt   time.Time
	touchedIDs   []int64
	touchedAt    time.Time
}

func (fake *fakeKeyStore) ResolveKey(_ context.Context, key string, now time.Time) (ResolvedKey, error) {
	fake.resolveCalls = append(fake.resolveCalls, key)
	fake.resolvedAt = now
	return fake.resolved, fake.resolveErr
}

func (fake *fakeKeyStore) TouchKey(_ context.Context, keyID int64, now time.Time) (bool, error) {
	fake.touchedIDs = append(fake.touchedIDs, keyID)
	fake.touchedAt = now
	return true, fake.touchErr
}

func (fake *fakeKeyStore) Exchange(context.Context, func(tx ExchangeTx) error) error {
	return errors.New("not used by key resolution")
}

func (fake *fakeKeyStore) DeleteExpiredReplay(context.Context, time.Time) (int64, error) {
	return 0, errors.New("not used by key resolution")
}

func newResolveService(store Store, logs *syncBuffer) *Service {
	return NewService(ServiceConfig{
		Store: store, Admin: nil, Verifier: &fakeVerifier{},
		Logger: textLogger(logs), Now: func() time.Time { return testNow },
	})
}

func TestResolveKeyReturnsTheAppAndTouchesTheKey(testInstance *testing.T) {
	store := &fakeKeyStore{resolved: ResolvedKey{ID: 9, AppID: "urls4irl", Hint: "dak_abcd"}}
	service := newResolveService(store, &syncBuffer{})

	resolved, err := service.ResolveKey(context.Background(), exchangeTestKey)
	if err != nil {
		testInstance.Fatalf("ResolveKey: %v", err)
	}

	if resolved != store.resolved {
		testInstance.Errorf("resolved = %+v, want %+v", resolved, store.resolved)
	}
	if len(store.resolveCalls) != 1 || store.resolveCalls[0] != exchangeTestKey || !store.resolvedAt.Equal(testNow) {
		testInstance.Errorf("resolve calls = %v at %v, want the presented key at the service clock", store.resolveCalls, store.resolvedAt)
	}
	if len(store.touchedIDs) != 1 || store.touchedIDs[0] != 9 || !store.touchedAt.Equal(testNow) {
		testInstance.Errorf("touched = %v at %v, want key 9 at the service clock", store.touchedIDs, store.touchedAt)
	}
}

func TestResolveKeyPassesAnInvalidKeyThroughUntouched(testInstance *testing.T) {
	invalid := fmt.Errorf("%w: revoked (key dak_abcd)", ErrInvalidKey)
	store := &fakeKeyStore{resolveErr: invalid}
	service := newResolveService(store, &syncBuffer{})

	resolved, err := service.ResolveKey(context.Background(), exchangeTestKey)

	if !errors.Is(err, ErrInvalidKey) {
		testInstance.Fatalf("err = %v, want one wrapping ErrInvalidKey", err)
	}
	if resolved != (ResolvedKey{}) {
		testInstance.Errorf("resolved = %+v, want the zero value on a denial", resolved)
	}
	if len(store.touchedIDs) != 0 {
		testInstance.Errorf("touched = %v, want no last_used_at write for an invalid key", store.touchedIDs)
	}
}

func TestResolveKeyStoreFailureIsNotAnInvalidKey(testInstance *testing.T) {
	store := &fakeKeyStore{resolveErr: errScripted}
	service := newResolveService(store, &syncBuffer{})

	_, err := service.ResolveKey(context.Background(), exchangeTestKey)

	if !errors.Is(err, errScripted) || errors.Is(err, ErrInvalidKey) {
		testInstance.Fatalf("err = %v, want the store failure unchanged and not ErrInvalidKey", err)
	}
}

func TestResolveKeyTouchFailureIsLoggedAndTheKeyStillResolves(testInstance *testing.T) {
	store := &fakeKeyStore{
		resolved: ResolvedKey{ID: 9, AppID: "urls4irl", Hint: "dak_abcd"}, touchErr: errScripted,
	}
	logs := &syncBuffer{}
	service := newResolveService(store, logs)

	resolved, err := service.ResolveKey(context.Background(), exchangeTestKey)

	if err != nil || resolved.AppID != "urls4irl" {
		testInstance.Fatalf("resolved = %+v, err = %v, want the key to resolve despite the touch failure", resolved, err)
	}
	logged := logs.String()
	if !strings.Contains(logged, "last_used_at not recorded") || !strings.Contains(logged, "dak_abcd") {
		testInstance.Errorf("want the touch failure logged with the key hint: %s", logged)
	}
	if strings.Contains(logged, exchangeTestKey) {
		testInstance.Errorf("the log must never carry the key: %s", logged)
	}
}

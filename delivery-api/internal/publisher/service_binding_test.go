package publisher

import (
	"context"
	"errors"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

// allowAll is a permissive BindingChecker: every app is bound.
type allowAll struct{}

func (allowAll) HasBinding(context.Context, string) (bool, error) { return true, nil }

// scriptedChecker answers each HasBinding call from answers (the last answer repeats).
// err fails every call, or only call number errOnCall (1-based) when errOnCall is set.
type scriptedChecker struct {
	answers   []bool
	err       error
	errOnCall int
	calls     int
}

func (checker *scriptedChecker) HasBinding(context.Context, string) (bool, error) {
	index := min(checker.calls, len(checker.answers)-1)
	checker.calls++
	if checker.err != nil && (checker.errOnCall == 0 || checker.errOnCall == checker.calls) {
		return false, checker.err
	}
	return checker.answers[index], nil
}

func TestMintBindingGuard(testInstance *testing.T) {
	lookupErr := errors.New("binding lookup down")
	cases := []struct {
		name         string
		checker      BindingChecker
		wantErr      error
		wantMinted   bool
		wantLockUsed bool
	}{
		{name: "bound proceeds", checker: &scriptedChecker{answers: []bool{true}}, wantMinted: true, wantLockUsed: true},
		{name: "unbound is refused", checker: &scriptedChecker{answers: []bool{false}}, wantErr: ErrAppNotBound},
		{name: "lookup error is unavailable", checker: &scriptedChecker{err: lookupErr, answers: []bool{true}}, wantErr: ErrMintUnavailable},
		{name: "nil checker is unavailable", checker: nil, wantErr: ErrMintUnavailable},
	}
	for _, testCase := range cases {
		for _, operator := range []bool{false, true} {
			name := testCase.name + " app"
			if operator {
				name = testCase.name + " operator"
			}
			testInstance.Run(name, func(subTest *testing.T) {
				store := newFakeStore()
				minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
				service := NewService(store, minter, newSealer(subTest), testCase.checker, nil)
				if testCase.wantErr != nil {
					// Refusals happen before the in-flight slot: with the cap full they must still be the
					// binding error, never the in-flight-cap error.
					service.mintSlots = make(chan struct{}, 1)
					service.mintSlots <- struct{}{}
				}

				var mintErr error
				if operator {
					_, mintErr = service.MintAsOperator(context.Background(), "urls4irl", false)
				} else {
					_, mintErr = service.Mint(context.Background(), "urls4irl", false)
				}

				if testCase.wantErr == nil && mintErr != nil {
					subTest.Fatalf("mint: %v", mintErr)
				}
				if testCase.wantErr != nil && !errors.Is(mintErr, testCase.wantErr) {
					subTest.Fatalf("mint err = %v, want %v", mintErr, testCase.wantErr)
				}
				if (len(minter.calls) == 1) != testCase.wantMinted {
					subTest.Fatalf("minter calls = %d, want minted=%v", len(minter.calls), testCase.wantMinted)
				}
				if (store.lockCalls > 0) != testCase.wantLockUsed || (!testCase.wantMinted && store.reserveCalls != 0) {
					subTest.Fatalf("lock calls = %d reserve = %d, want lock used=%v", store.lockCalls, store.reserveCalls, testCase.wantLockUsed)
				}
				wantHeld := 0
				if testCase.wantErr != nil {
					wantHeld = 1 // only the pre-filled slot; the refusal neither took nor freed one
				}
				if len(service.mintSlots) != wantHeld {
					subTest.Fatalf("in-flight slots held = %d, want %d", len(service.mintSlots), wantHeld)
				}
			})
		}
	}
}

func TestMintBadAppIDIsValidatedBeforeAnyBindingLookup(testInstance *testing.T) {
	checker := &scriptedChecker{answers: []bool{true}}
	service := NewService(newFakeStore(), &fakeMinter{}, newSealer(testInstance), checker, nil)

	_, mintErr := service.Mint(context.Background(), "Bad-App", false)

	var validation *ValidationError
	if !errors.As(mintErr, &validation) || checker.calls != 0 {
		testInstance.Fatalf("err = %v, lookups = %d, want ValidationError and no lookup", mintErr, checker.calls)
	}
}

func TestMintRechecksTheBindingInsideTheLock(testInstance *testing.T) {
	store := newFakeStore()
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
	checker := &scriptedChecker{answers: []bool{true, false}} // bound at the pre-check, unbound under the lock
	service := NewService(store, minter, newSealer(testInstance), checker, nil)

	_, mintErr := service.MintAsOperator(context.Background(), "urls4irl", true)

	if !errors.Is(mintErr, ErrAppNotBound) {
		testInstance.Fatalf("mint err = %v, want ErrAppNotBound", mintErr)
	}
	if len(minter.calls) != 0 || store.reserveCalls != 0 || store.upsertCalls != 0 {
		testInstance.Fatalf("minter=%d reserve=%d upsert=%d, want all zero", len(minter.calls), store.reserveCalls, store.upsertCalls)
	}
	if checker.calls != 2 || len(service.mintSlots) != 0 || len(store.held) != 0 {
		testInstance.Fatalf("lookups=%d slots=%d held=%d, want 2 lookups and everything released",
			checker.calls, len(service.mintSlots), len(store.held))
	}
}

func TestMintFailsClosedWhenTheInLockRecheckErrors(testInstance *testing.T) {
	store := newFakeStore()
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
	checker := &scriptedChecker{answers: []bool{true}, err: errors.New("binding lookup down"), errOnCall: 2}
	service := NewService(store, minter, newSealer(testInstance), checker, nil)

	_, mintErr := service.MintAsOperator(context.Background(), "urls4irl", false)

	if !errors.Is(mintErr, ErrMintUnavailable) {
		testInstance.Fatalf("mint err = %v, want ErrMintUnavailable", mintErr)
	}
	if checker.calls != 2 || len(minter.calls) != 0 || store.reserveCalls != 0 || len(service.mintSlots) != 0 || len(store.held) != 0 {
		testInstance.Fatalf("lookups=%d minter=%d reserve=%d slots=%d held=%d, want 2 lookups, no mint, all released",
			checker.calls, len(minter.calls), store.reserveCalls, len(service.mintSlots), len(store.held))
	}
}

func TestRemoveExclusiveDeletesUnderTheLock(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store, nil, newSealer(testInstance), nil, nil)
	if _, upsertErr := store.upsertRow(StoredPublisher{AppID: "urls4irl"}); upsertErr != nil {
		testInstance.Fatalf("seed: %v", upsertErr)
	}

	for attempt := 0; attempt < 2; attempt++ { // second pass: idempotent for a missing row
		if removeErr := service.RemoveExclusive(context.Background(), "urls4irl"); removeErr != nil {
			testInstance.Fatalf("RemoveExclusive attempt %d: %v", attempt, removeErr)
		}
	}
	if _, found := store.rows["urls4irl"]; found || store.lockCalls != 2 {
		testInstance.Fatalf("row present=%v lockCalls=%d, want deleted under 2 lock acquisitions", found, store.lockCalls)
	}
}

func TestRemoveExclusiveIsBusyWhenAMintHoldsTheLock(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store, nil, newSealer(testInstance), nil, nil)
	if _, upsertErr := store.upsertRow(StoredPublisher{AppID: "urls4irl"}); upsertErr != nil {
		testInstance.Fatalf("seed: %v", upsertErr)
	}
	store.held["urls4irl"] = true

	removeErr := service.RemoveExclusive(context.Background(), "urls4irl")

	if !errors.Is(removeErr, ErrMintBusy) || store.deleteCalls != 0 {
		testInstance.Fatalf("err = %v deleteCalls = %d, want ErrMintBusy and no delete", removeErr, store.deleteCalls)
	}
}

func TestRemoveExclusiveRejectsAnInvalidID(testInstance *testing.T) {
	store := newFakeStore()
	service := NewService(store, nil, newSealer(testInstance), nil, nil)

	removeErr := service.RemoveExclusive(context.Background(), "Bad-App")

	var validation *ValidationError
	if !errors.As(removeErr, &validation) || store.lockCalls != 0 {
		testInstance.Fatalf("err = %v lockCalls = %d, want ValidationError before the lock", removeErr, store.lockCalls)
	}
}

// flipChecker is bound until unbind is called, standing in for appidentity.Service after the cascade's Unbind.
type flipChecker struct{ unbound bool }

func (checker *flipChecker) HasBinding(context.Context, string) (bool, error) {
	return !checker.unbound, nil
}

// The cascade order is Unbind, then RemoveExclusive. A mint that runs after Unbind must be refused and leave
// no publisher row, so the cascade never reports removed while a publisher exists.
func TestMintAfterCascadeUnbindIsRefusedAndLeavesNoRow(testInstance *testing.T) {
	store := newFakeStore()
	minter := &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
	checker := &flipChecker{}
	service := NewService(store, minter, newSealer(testInstance), checker, nil)
	if _, upsertErr := store.upsertRow(StoredPublisher{AppID: "urls4irl"}); upsertErr != nil {
		testInstance.Fatalf("seed: %v", upsertErr)
	}

	checker.unbound = true
	if removeErr := service.RemoveExclusive(context.Background(), "urls4irl"); removeErr != nil {
		testInstance.Fatalf("RemoveExclusive: %v", removeErr)
	}
	_, mintErr := service.MintAsOperator(context.Background(), "urls4irl", false)

	if !errors.Is(mintErr, ErrAppNotBound) {
		testInstance.Fatalf("mint err = %v, want ErrAppNotBound", mintErr)
	}
	if _, found := store.rows["urls4irl"]; found || len(minter.calls) != 0 {
		testInstance.Fatalf("row present=%v minter calls=%d, want none", found, len(minter.calls))
	}
}

func TestRemoveExclusiveFailsClosedWithoutAStore(testInstance *testing.T) {
	service := NewService(nil, nil, newSealer(testInstance), nil, nil)

	if removeErr := service.RemoveExclusive(context.Background(), "urls4irl"); !errors.Is(removeErr, ErrMintUnavailable) {
		testInstance.Fatalf("err = %v, want ErrMintUnavailable", removeErr)
	}
}

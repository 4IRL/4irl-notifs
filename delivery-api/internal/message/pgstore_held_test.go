package message

import (
	"context"
	"errors"
	"testing"
	"time"
)

// scriptedHeldTx returns an insertHeldFunc that yields results in order (the
// last repeats) and counts its calls.
func scriptedHeldTx(results []error, stored StoredMessage, calls *int) insertHeldFunc {
	return func(context.Context, Envelope, HeldCaps, time.Time, []byte, []byte) (StoredMessage, bool, error) {
		index := min(*calls, len(results)-1)
		*calls++
		if results[index] != nil {
			return StoredMessage{}, false, results[index]
		}
		return stored, false, nil
	}
}

func TestInsertHeldMessageRetriesWhenTheConflictVanishes(testInstance *testing.T) {
	heldCaps := HeldCaps{PerApp: 5, Total: 10}
	envelope := validEnvelope()
	now := fixtureNow

	testInstance.Run("vanished once then stored", func(subTest *testing.T) {
		calls := 0
		want := StoredMessage{ID: existingMessageID}
		store := &PGStore{insertHeldTx: scriptedHeldTx([]error{ErrMessageNotFound, nil}, want, &calls)}

		got, duplicate, err := store.InsertHeldMessage(context.Background(), envelope, heldCaps, now)
		if err != nil || duplicate || got.ID != want.ID || calls != 2 {
			subTest.Fatalf("InsertHeldMessage = (%+v, %v, %v) after %d calls, want the stored message after 2", got, duplicate, err, calls)
		}
	})
	testInstance.Run("vanished on every attempt", func(subTest *testing.T) {
		calls := 0
		store := &PGStore{insertHeldTx: scriptedHeldTx([]error{ErrMessageNotFound}, StoredMessage{}, &calls)}

		_, _, err := store.InsertHeldMessage(context.Background(), envelope, heldCaps, now)
		if err == nil || errors.Is(err, ErrMessageNotFound) || calls != maxInsertAttempts {
			subTest.Fatalf("InsertHeldMessage error = %v after %d calls, want a non-NotFound error after %d",
				err, calls, maxInsertAttempts)
		}
	})
	testInstance.Run("other errors return at once", func(subTest *testing.T) {
		calls := 0
		store := &PGStore{insertHeldTx: scriptedHeldTx([]error{ErrTooManyHeld}, StoredMessage{}, &calls)}

		_, _, err := store.InsertHeldMessage(context.Background(), envelope, heldCaps, now)
		if !errors.Is(err, ErrTooManyHeld) || calls != 1 {
			subTest.Fatalf("InsertHeldMessage error = %v after %d calls, want ErrTooManyHeld after 1", err, calls)
		}
	})
}

func TestInsertHeldMessageRejectsBadArguments(testInstance *testing.T) {
	store := &PGStore{insertHeldTx: func(context.Context, Envelope, HeldCaps, time.Time, []byte, []byte) (StoredMessage, bool, error) {
		testInstance.Fatal("the transaction must not run")
		return StoredMessage{}, false, nil
	}}
	testCases := []struct {
		name string
		caps HeldCaps
		now  time.Time
	}{
		{name: "per-app cap zero", caps: HeldCaps{PerApp: 0, Total: 1}, now: fixtureNow},
		{name: "total cap zero", caps: HeldCaps{PerApp: 1, Total: 0}, now: fixtureNow},
		{name: "zero now", caps: HeldCaps{PerApp: 1, Total: 1}},
	}
	for _, testCase := range testCases {
		if _, _, err := store.InsertHeldMessage(context.Background(), validEnvelope(), testCase.caps, testCase.now); err == nil {
			testInstance.Fatalf("%s: error = nil, want a refusal", testCase.name)
		}
	}
}

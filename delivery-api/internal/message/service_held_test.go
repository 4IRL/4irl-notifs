package message

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// heldInsertCall records one InsertHeldMessage call.
type heldInsertCall struct {
	envelope Envelope
	caps     HeldCaps
	now      time.Time
}

// heldStoreScript holds the scripted results of InsertHeldMessage; it is
// embedded in fakeServiceStore.
type heldStoreScript struct {
	heldInsertCalls []heldInsertCall
	heldInsertErr   error
	// heldInsertDuplicate makes InsertHeldMessage report an existing key.
	heldInsertDuplicate bool
}

func (fake *fakeServiceStore) InsertHeldMessage(
	_ context.Context, envelope Envelope, caps HeldCaps, now time.Time,
) (StoredMessage, bool, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.heldInsertCalls = append(fake.heldInsertCalls, heldInsertCall{envelope: envelope, caps: caps, now: now})
	if fake.heldInsertErr != nil {
		return StoredMessage{}, false, fake.heldInsertErr
	}
	heldAt := now
	return StoredMessage{
		ID: "11111111-1111-1111-1111-111111111111", Envelope: envelope, HeldAt: &heldAt,
	}, fake.heldInsertDuplicate, nil
}

// holdFixture is a service fixture with the hold flag on and an unrouted
// resolver (a route mode submit resolves to nothing).
func holdFixture() serviceFixture {
	fixture := newServiceFixture()
	fixture.service.holdUnrouted = true
	fixture.resolver.result = nil
	return fixture
}

func holdEnvelope() Envelope {
	envelope := validEnvelope()
	envelope.Hold = true
	return envelope
}

func assertNothingHeldOrInserted(testInstance *testing.T, fixture serviceFixture) {
	testInstance.Helper()
	if len(fixture.store.heldInsertCalls) != 0 || len(fixture.store.insertCalls) != 0 ||
		len(fixture.store.scheduledInsertCalls) != 0 || fixture.wakes.calls() != 0 {
		testInstance.Fatalf("held/insert/scheduled calls = %d/%d/%d, wakes = %d; want none",
			len(fixture.store.heldInsertCalls), len(fixture.store.insertCalls),
			len(fixture.store.scheduledInsertCalls), fixture.wakes.calls())
	}
}

func TestSubmitHoldsAnUnroutedMessageWithoutWaking(testInstance *testing.T) {
	fixture := holdFixture()

	stored, outcome, err := fixture.service.Submit(context.Background(), holdEnvelope())
	if err != nil || !outcome.Held || outcome.Duplicate || outcome.Replaced || outcome.Scheduled {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want a fresh hold", err, outcome)
	}
	if stored.ID == "" || stored.HeldAt == nil {
		testInstance.Fatalf("stored = %+v, want a held message", stored)
	}
	if len(fixture.store.heldInsertCalls) != 1 {
		testInstance.Fatalf("held insert calls = %d, want 1", len(fixture.store.heldInsertCalls))
	}
	call := fixture.store.heldInsertCalls[0]
	if call.caps.PerApp != DefaultMaxHeldPerApp || call.caps.Total != DefaultMaxHeldTotal || !call.now.Equal(fixtureNow) {
		testInstance.Fatalf("held insert call = %+v, want the service caps and clock", call)
	}
	if len(fixture.store.insertCalls) != 0 || fixture.wakes.calls() != 0 {
		testInstance.Fatalf("insert calls = %d, wakes = %d; want 0 and 0", len(fixture.store.insertCalls), fixture.wakes.calls())
	}
}

func TestSubmitHoldOfAScheduledUnroutedMessageIsStillHeld(testInstance *testing.T) {
	fixture := holdFixture()
	envelope := holdEnvelope()
	envelope.DeliverAt = timePointer(scheduledAt())

	_, outcome, err := fixture.service.Submit(context.Background(), envelope)
	if err != nil || !outcome.Held || outcome.Scheduled {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want a hold, not a schedule", err, outcome)
	}
	if len(fixture.store.scheduledInsertCalls) != 0 || len(fixture.store.heldInsertCalls) != 1 {
		testInstance.Fatalf("scheduled/held inserts = %d/%d, want 0/1",
			len(fixture.store.scheduledInsertCalls), len(fixture.store.heldInsertCalls))
	}
}

func TestSubmitHoldDuplicateIsNotHeld(testInstance *testing.T) {
	fixture := holdFixture()
	fixture.store.heldInsertDuplicate = true

	_, outcome, err := fixture.service.Submit(context.Background(), holdEnvelope())
	if err != nil || !outcome.Duplicate || outcome.Held {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want Duplicate true and Held false", err, outcome)
	}
	if fixture.wakes.calls() != 0 {
		testInstance.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
	}
}

func TestSubmitSameKeyReplayOfTerminalHeldStatesIsADuplicate(testInstance *testing.T) {
	stamp := fixtureNow
	deliverAt := fixtureNow.Add(time.Hour)
	testCases := []struct {
		name  string
		state StoredMessage
	}{
		{name: "cancelled held", state: StoredMessage{CancelledAt: &stamp, HeldCancelledAt: &stamp}},
		{name: "release failed", state: StoredMessage{ReleaseFailedAt: &stamp}},
		{name: "held and scheduled", state: StoredMessage{HeldAt: &stamp}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := holdFixture()
			stored := testCase.state
			stored.ID = existingMessageID
			stored.Envelope = holdEnvelope()
			fixture.store.getByKeyPresent = true
			fixture.store.getByKeyResult = stored

			changed := holdEnvelope()
			changed.Payload = []byte(`{"changed": true}`)
			changed.DeliverAt = &deliverAt
			got, outcome, err := fixture.service.Submit(context.Background(), changed)
			if err != nil || !outcome.Duplicate || outcome.Held || got.ID != existingMessageID {
				subTest.Fatalf("Submit = (%+v, outcome %+v, err %v), want the stored message as a duplicate", got, outcome, err)
			}
			assertNothingHeldOrInserted(subTest, fixture)
		})
	}
}

func TestSubmitSameKeyReplayOfAHeldMessageIsADuplicateNotHeld(testInstance *testing.T) {
	fixture := holdFixture()
	heldAt := fixtureNow
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = StoredMessage{ID: existingMessageID, Envelope: holdEnvelope(), HeldAt: &heldAt}

	changed := holdEnvelope()
	changed.Payload = []byte(`{"changed": true}`)
	_, outcome, err := fixture.service.Submit(context.Background(), changed)
	if err != nil || !outcome.Duplicate || outcome.Held {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want Duplicate true and Held false", err, outcome)
	}
	assertNothingHeldOrInserted(testInstance, fixture)
}

func TestSubmitWithoutHoldOptInOrFlagKeepsNoDestinationMatched(testInstance *testing.T) {
	testCases := []struct {
		name string
		flag bool
		hold bool
	}{
		{name: "flag off", flag: false, hold: true},
		{name: "hold not set by the route", flag: true, hold: false},
		{name: "both off", flag: false, hold: false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := holdFixture()
			fixture.service.holdUnrouted = testCase.flag
			envelope := holdEnvelope()
			envelope.Hold = testCase.hold

			_, _, err := fixture.service.Submit(context.Background(), envelope)
			if !errors.Is(err, ErrNoDestinationMatched) {
				subTest.Fatalf("Submit error = %v, want ErrNoDestinationMatched", err)
			}
			assertNothingHeldOrInserted(subTest, fixture)
		})
	}
}

func TestSubmitNeverHoldsOtherResolutionFailures(testInstance *testing.T) {
	testInstance.Run("named destination errors", func(subTest *testing.T) {
		for _, resolveErr := range []error{registry.ErrUnknownDestination, registry.ErrDestinationDisabled} {
			fixture := holdFixture()
			fixture.resolver.err = resolveErr
			envelope := holdEnvelope()
			envelope.ToDestination = "gone"

			_, _, err := fixture.service.Submit(context.Background(), envelope)
			if !errors.Is(err, resolveErr) {
				subTest.Fatalf("Submit error = %v, want %v", err, resolveErr)
			}
			assertNothingHeldOrInserted(subTest, fixture)
		}
	})
	testInstance.Run("named destination resolving to nothing", func(subTest *testing.T) {
		fixture := holdFixture()
		envelope := holdEnvelope()
		envelope.ToDestination = "ops_webhook"

		_, _, err := fixture.service.Submit(context.Background(), envelope)
		if !errors.Is(err, ErrNoDestinationMatched) {
			subTest.Fatalf("Submit error = %v, want ErrNoDestinationMatched", err)
		}
		assertNothingHeldOrInserted(subTest, fixture)
	})
	testInstance.Run("unsupported destination type", func(subTest *testing.T) {
		fixture := holdFixture()
		fixture.resolver.result = []registry.ResolvedDestination{{ID: 9, Name: "pager", Type: registry.TypeDiscord}}

		_, _, err := fixture.service.Submit(context.Background(), holdEnvelope())
		if !errors.Is(err, ErrUnsupportedDestinationType) {
			subTest.Fatalf("Submit error = %v, want ErrUnsupportedDestinationType", err)
		}
		assertNothingHeldOrInserted(subTest, fixture)
	})
	testInstance.Run("recipient mode", func(subTest *testing.T) {
		fixture := newRecipientFixture()
		fixture.service.holdUnrouted = true
		fixture.publishers.has = false
		envelope := recipientEnvelope("alice@example.com")
		envelope.Hold = true

		_, _, err := fixture.service.Submit(context.Background(), envelope)
		if err == nil {
			subTest.Fatal("Submit error = nil, want the recipient refusal")
		}
		if len(fixture.store.heldInsertCalls) != 0 || len(fixture.store.insertCalls) != 0 {
			subTest.Fatalf("held/insert calls = %d/%d, want none",
				len(fixture.store.heldInsertCalls), len(fixture.store.insertCalls))
		}
	})
}

func TestSubmitHoldsWhenEveryRouteDestinationIsDisabled(testInstance *testing.T) {
	// The resolver reports a matched-but-disabled route as an empty result,
	// which is the same zero-match branch as no route at all.
	fixture := holdFixture()
	fixture.resolver.result = []registry.ResolvedDestination{}

	_, outcome, err := fixture.service.Submit(context.Background(), holdEnvelope())
	if err != nil || !outcome.Held {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want a hold", err, outcome)
	}
}

func TestSubmitReplaceResolvingToNoMatchNeverHolds(testInstance *testing.T) {
	fixture := holdFixture().withExisting(storedAs(scheduledEnvelope(), StateScheduled))
	envelope := scheduledEnvelope()
	envelope.Hold = true
	envelope.Display = &Display{Title: "changed", Body: "changed"}

	_, _, err := fixture.service.Submit(context.Background(), envelope)
	if !errors.Is(err, ErrNoDestinationMatched) {
		testInstance.Fatalf("Submit error = %v, want ErrNoDestinationMatched", err)
	}
	if len(fixture.store.replaceCalls) != 0 {
		testInstance.Fatalf("replace calls = %d, want 0", len(fixture.store.replaceCalls))
	}
	assertNothingHeldOrInserted(testInstance, fixture)
}

func TestSubmitHeldCapErrorsPassThrough(testInstance *testing.T) {
	for _, capErr := range []error{ErrTooManyHeld, ErrHeldCapacityReached} {
		fixture := holdFixture()
		fixture.store.heldInsertErr = capErr

		_, outcome, err := fixture.service.Submit(context.Background(), holdEnvelope())
		if !errors.Is(err, capErr) || outcome.Held {
			testInstance.Fatalf("Submit = (%v, outcome %+v), want %v and no hold", err, outcome, capErr)
		}
		if fixture.wakes.calls() != 0 {
			testInstance.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
		}
	}
}

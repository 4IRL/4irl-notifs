package message

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// fakePersonalTargets is a scripted PersonalTargets that records its calls.
type fakePersonalTargets struct {
	targets []personal.Target
	err     error
	calls   []string
}

func (fake *fakePersonalTargets) OptedInTargets(_ context.Context, appID, personHash string) ([]personal.Target, error) {
	fake.calls = append(fake.calls, appID+"/"+personHash)
	return fake.targets, fake.err
}

// newPersonalFixture returns a recipientFixture whose service also fans out to personal destinations.
func newPersonalFixture(supported map[string]bool, fake *fakePersonalTargets) recipientFixture {
	fixture := newRecipientFixture()
	fixture.service = NewService(ServiceConfig{
		Store:           fixture.store,
		Resolver:        fixture.resolver,
		Publishers:      fixture.publishers,
		PersonalTargets: fake,
		SupportedTypes:  supported,
		Wake:            fixture.wakes.wake,
		PollInterval:    time.Millisecond,
	})
	return fixture
}

var allSinks = map[string]bool{
	registry.TypeWebhook: true, registry.TypeNtfy: true, registry.TypeDiscord: true,
}

func TestSubmitRecipientFansOutToOptedInPersonalTargets(testInstance *testing.T) {
	fake := &fakePersonalTargets{targets: []personal.Target{
		{ID: 4, Type: registry.TypeDiscord}, {ID: 9, Type: registry.TypeWebhook},
	}}
	fixture := newPersonalFixture(allSinks, fake)

	if _, _, err := fixture.service.Submit(context.Background(), recipientEnvelope("alice@example.com")); err != nil {
		testInstance.Fatalf("Submit error = %v", err)
	}
	want := []DeliveryTarget{
		{DestinationName: registry.RecipientDestinationName, DestinationType: registry.TypeNtfy},
		{DestinationName: "@personal_4", DestinationType: registry.TypeDiscord},
		{DestinationName: "@personal_9", DestinationType: registry.TypeWebhook},
	}
	if got := fixture.store.insertCalls[0].targets; !reflect.DeepEqual(got, want) {
		testInstance.Fatalf("targets = %+v, want %+v", got, want)
	}
	if wantCall := "urls4irl/" + personhash.Hash("alice@example.com"); !reflect.DeepEqual(fake.calls, []string{wantCall}) {
		testInstance.Fatalf("lookups = %v, want [%s]", fake.calls, wantCall)
	}
}

func TestSubmitRecipientWithNoOptedInTargetsIsNtfyOnly(testInstance *testing.T) {
	fixture := newPersonalFixture(allSinks, &fakePersonalTargets{})

	if _, _, err := fixture.service.Submit(context.Background(), recipientEnvelope("alice@example.com")); err != nil {
		testInstance.Fatalf("Submit error = %v", err)
	}
	if got := fixture.store.insertCalls[0].targets; len(got) != 1 || got[0].DestinationName != registry.RecipientDestinationName {
		testInstance.Fatalf("targets = %+v, want the ntfy target only", got)
	}
}

func TestSubmitRecipientPersonalFailuresStoreNothing(testInstance *testing.T) {
	lookupErr := errors.New("personal lookup failed")
	testCases := []struct {
		name      string
		supported map[string]bool
		fake      *fakePersonalTargets
		wantErr   error
	}{
		{
			name: "lookup error", supported: allSinks,
			fake: &fakePersonalTargets{err: lookupErr}, wantErr: lookupErr,
		},
		{
			name:      "personal type without a sink",
			supported: map[string]bool{registry.TypeNtfy: true, registry.TypeWebhook: true},
			fake:      &fakePersonalTargets{targets: []personal.Target{{ID: 1, Type: registry.TypeDiscord}}},
			wantErr:   ErrUnsupportedDestinationType,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newPersonalFixture(testCase.supported, testCase.fake)

			_, _, err := fixture.service.Submit(context.Background(), recipientEnvelope("alice@example.com"))

			if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("error = %v, want %v", err, testCase.wantErr)
			}
			if len(fixture.store.insertCalls) != 0 || fixture.wakes.calls() != 0 {
				subTest.Fatalf("insert/wake = %d/%d, want none", len(fixture.store.insertCalls), fixture.wakes.calls())
			}
		})
	}
}

func TestSubmitDuplicateRecipientShortCircuitsBeforePersonalLookup(testInstance *testing.T) {
	fake := &fakePersonalTargets{err: errors.New("must not be called")}
	fixture := newPersonalFixture(allSinks, fake)
	fixture.publishers.has = false
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = StoredMessage{ID: "22222222-2222-2222-2222-222222222222", Envelope: validEnvelope()}

	_, outcome, err := fixture.service.Submit(context.Background(), recipientEnvelope("alice@example.com"))
	if err != nil || !outcome.Duplicate {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want a duplicate", err, outcome)
	}
	if len(fake.calls) != 0 || len(fixture.publishers.calls) != 0 {
		testInstance.Fatalf("personal/publisher lookups = %d/%d, want none", len(fake.calls), len(fixture.publishers.calls))
	}
}

func scheduledRecipientFixture(fake *fakePersonalTargets) (recipientFixture, Envelope) {
	fixture := newPersonalFixture(allSinks, fake)
	storedEnvelope := scheduledEnvelope()
	storedEnvelope.ToRecipient = personhash.Hash("alice@example.com")
	storedEnvelope.Display = &Display{Title: "T", Body: "B"}
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = storedAs(storedEnvelope, StateScheduled)
	fixture.store.replaceReplaced = true
	return fixture, storedEnvelope
}

func TestSubmitReplacedScheduledRecipientRecomputesPersonalTargets(testInstance *testing.T) {
	fake := &fakePersonalTargets{targets: []personal.Target{{ID: 7, Type: registry.TypeWebhook}}}
	fixture, storedEnvelope := scheduledRecipientFixture(fake)
	changed := storedEnvelope
	changed.Channel = "changed"

	_, outcome, err := fixture.service.Submit(context.Background(), changed)
	if err != nil || !outcome.Replaced {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want replaced", err, outcome)
	}
	if len(fake.calls) != 1 {
		testInstance.Fatalf("personal lookups = %d, want 1", len(fake.calls))
	}
	targets := fixture.store.replaceCalls[0].targets
	if len(targets) != 2 || targets[0].DestinationName != registry.RecipientDestinationName ||
		targets[1].DestinationName != "@personal_7" {
		testInstance.Fatalf("replace targets = %+v, want @recipient then @personal_7", targets)
	}
}

func TestSubmitReplaceWithPersonalLookupErrorChangesNothing(testInstance *testing.T) {
	lookupErr := errors.New("personal lookup failed")
	fixture, storedEnvelope := scheduledRecipientFixture(&fakePersonalTargets{err: lookupErr})
	changed := storedEnvelope
	changed.Channel = "changed"

	_, _, err := fixture.service.Submit(context.Background(), changed)

	if !errors.Is(err, lookupErr) {
		testInstance.Fatalf("error = %v, want the lookup error", err)
	}
	if len(fixture.store.replaceCalls) != 0 || len(fixture.store.insertCalls) != 0 || fixture.wakes.calls() != 0 {
		testInstance.Fatalf("replace/insert/wake = %d/%d/%d, want none",
			len(fixture.store.replaceCalls), len(fixture.store.insertCalls), fixture.wakes.calls())
	}
}

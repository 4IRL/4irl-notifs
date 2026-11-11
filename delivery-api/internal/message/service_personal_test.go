package message

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
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

func (fake *fakePersonalTargets) SelectedTargets(_ context.Context, appID, personHash string, ids []int64) ([]personal.Target, error) {
	fake.calls = append(fake.calls, fmt.Sprintf("selected:%s/%s/%v", appID, personHash, ids))
	var selected []personal.Target
	for _, target := range fake.targets {
		if slices.Contains(ids, target.ID) {
			selected = append(selected, target)
		}
	}
	return selected, fake.err
}

// newPersonalFixture returns a recipientFixture whose service can also deliver to named personal destinations.
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

// routedRecipientEnvelope is a recipient envelope with the given routing.
func routedRecipientEnvelope(via RecipientVia, ids ...int64) Envelope {
	envelope := recipientEnvelope("alice@example.com")
	envelope.ToRecipientVia = via
	envelope.ToPersonalDestinationIDs = ids
	return envelope
}

var ntfyRecipientTarget = DeliveryTarget{DestinationName: registry.RecipientDestinationName, DestinationType: registry.TypeNtfy}

func threeDestinations() *fakePersonalTargets {
	return &fakePersonalTargets{targets: []personal.Target{
		{ID: 4, Type: registry.TypeDiscord}, {ID: 9, Type: registry.TypeWebhook}, {ID: 12, Type: registry.TypeWebhook},
	}}
}

func TestSubmitRecipientViaBothTargetsRecipientPlusNamedIDsOnly(testInstance *testing.T) {
	fake := threeDestinations()
	fixture := newPersonalFixture(allSinks, fake)

	if _, _, submitErr := fixture.service.Submit(context.Background(), routedRecipientEnvelope(RecipientViaBoth, 4, 9)); submitErr != nil {
		testInstance.Fatalf("Submit error = %v", submitErr)
	}
	want := []DeliveryTarget{
		ntfyRecipientTarget,
		{DestinationName: "@personal_4", DestinationType: registry.TypeDiscord},
		{DestinationName: "@personal_9", DestinationType: registry.TypeWebhook},
	}
	if got := fixture.store.insertCalls[0].targets; !reflect.DeepEqual(got, want) {
		testInstance.Fatalf("targets = %+v, want %+v (the unnamed destination 12 must not be included)", got, want)
	}
	if wantCall := "selected:urls4irl/" + personhash.Hash("alice@example.com") + "/[4 9]"; !reflect.DeepEqual(fake.calls, []string{wantCall}) {
		testInstance.Fatalf("lookups = %v, want [%s]", fake.calls, wantCall)
	}
}

// TestResolveRecipientTargetsDeniesAnUnknownVia pins the fail-closed default
// branch: an enum member the resolver does not know yields a 400 validation
// error, no targets and no store or personal lookup. Validation normally rejects
// it first, so the resolver is called directly.
func TestResolveRecipientTargetsDeniesAnUnknownVia(testInstance *testing.T) {
	fake := threeDestinations()
	fixture := newPersonalFixture(allSinks, fake)

	targets, resolveErr := fixture.service.resolveRecipientTargets(context.Background(), routedRecipientEnvelope(RecipientVia("future"), 4))

	var validationErr *ValidationError
	if !errors.As(resolveErr, &validationErr) || validationErr.Message != errMsgInvalidVia {
		testInstance.Fatalf("error = %v, want ValidationError invalid to.via", resolveErr)
	}
	if len(targets) != 0 || len(fake.calls) != 0 {
		testInstance.Fatalf("targets = %+v, lookups = %v, want none", targets, fake.calls)
	}
}

func TestSubmitRecipientViaNtfyOrUnsetIsNtfyOnlyAndNeverQueriesPersonal(testInstance *testing.T) {
	for _, via := range []RecipientVia{"", RecipientViaNtfy} {
		testInstance.Run("via="+string(via), func(subTest *testing.T) {
			fake := threeDestinations()
			fixture := newPersonalFixture(allSinks, fake)

			if _, _, submitErr := fixture.service.Submit(context.Background(), routedRecipientEnvelope(via)); submitErr != nil {
				subTest.Fatalf("Submit error = %v", submitErr)
			}
			if got := fixture.store.insertCalls[0].targets; !reflect.DeepEqual(got, []DeliveryTarget{ntfyRecipientTarget}) {
				subTest.Fatalf("targets = %+v, want the ntfy target only", got)
			}
			if len(fake.calls) != 0 {
				subTest.Fatalf("personal lookups = %v, want none", fake.calls)
			}
		})
	}
}

func TestSubmitRecipientViaPersonalTargetsOnlyNamedIDsWithoutNtfyLeg(testInstance *testing.T) {
	fake := threeDestinations()
	fixture := newPersonalFixture(allSinks, fake)
	fixture.publishers.has = false

	if _, _, submitErr := fixture.service.Submit(context.Background(), routedRecipientEnvelope(RecipientViaPersonal, 12)); submitErr != nil {
		testInstance.Fatalf("Submit error = %v", submitErr)
	}
	want := []DeliveryTarget{{DestinationName: "@personal_12", DestinationType: registry.TypeWebhook}}
	if got := fixture.store.insertCalls[0].targets; !reflect.DeepEqual(got, want) {
		testInstance.Fatalf("targets = %+v, want %+v", got, want)
	}
	if len(fixture.publishers.calls) != 0 {
		testInstance.Fatalf("publisher lookups = %v, want none", fixture.publishers.calls)
	}
}

func TestSubmitRecipientViaPersonalWithNilPublishersAndNoNtfySinkSucceeds(testInstance *testing.T) {
	fake := threeDestinations()
	fixture := newRecipientFixture()
	fixture.service = NewService(ServiceConfig{
		Store:           fixture.store,
		Resolver:        fixture.resolver,
		PersonalTargets: fake,
		SupportedTypes:  map[string]bool{registry.TypeWebhook: true},
		Wake:            fixture.wakes.wake,
		PollInterval:    time.Millisecond,
	})

	if _, _, submitErr := fixture.service.Submit(context.Background(), routedRecipientEnvelope(RecipientViaPersonal, 9)); submitErr != nil {
		testInstance.Fatalf("Submit error = %v, want success without publishers or an ntfy sink", submitErr)
	}
	want := []DeliveryTarget{{DestinationName: "@personal_9", DestinationType: registry.TypeWebhook}}
	if got := fixture.store.insertCalls[0].targets; !reflect.DeepEqual(got, want) {
		testInstance.Fatalf("targets = %+v, want %+v", got, want)
	}
}

func TestSubmitRecipientViaPersonalStillRequiresRenderableDisplay(testInstance *testing.T) {
	fake := threeDestinations()
	fixture := newPersonalFixture(allSinks, fake)
	envelope := routedRecipientEnvelope(RecipientViaPersonal, 4)
	envelope.Display = nil

	_, _, submitErr := fixture.service.Submit(context.Background(), envelope)

	var validationErr *ValidationError
	if !errors.As(submitErr, &validationErr) {
		testInstance.Fatalf("error = %v, want a *ValidationError", submitErr)
	}
	if len(fake.calls) != 0 || len(fixture.store.insertCalls) != 0 {
		testInstance.Fatalf("lookups/inserts = %d/%d, want none", len(fake.calls), len(fixture.store.insertCalls))
	}
}

func TestSubmitRecipientNtfyLegStillRequiresAPublisher(testInstance *testing.T) {
	for _, via := range []RecipientVia{RecipientViaNtfy, RecipientViaBoth} {
		testInstance.Run("via="+string(via), func(subTest *testing.T) {
			fake := &fakePersonalTargets{err: errors.New("must not be called")}
			fixture := newPersonalFixture(allSinks, fake)
			fixture.publishers.has = false

			var ids []int64
			if via == RecipientViaBoth {
				ids = []int64{4}
			}
			_, _, submitErr := fixture.service.Submit(context.Background(), routedRecipientEnvelope(via, ids...))

			if !errors.Is(submitErr, registry.ErrNoPublisher) {
				subTest.Fatalf("error = %v, want ErrNoPublisher before any personal lookup", submitErr)
			}
			if len(fake.calls) != 0 {
				subTest.Fatalf("personal lookups = %v, want none", fake.calls)
			}
		})
	}
}

func TestSubmitRecipientPersonalFailuresStoreNothing(testInstance *testing.T) {
	lookupErr := errors.New("personal lookup failed")
	testCases := []struct {
		name      string
		supported map[string]bool
		fake      *fakePersonalTargets
		ids       []int64
		wantErr   error
	}{
		{
			name: "lookup error propagates unchanged", supported: allSinks,
			fake: &fakePersonalTargets{err: lookupErr}, ids: []int64{4}, wantErr: lookupErr,
		},
		{
			name: "one id missing", supported: allSinks,
			fake: threeDestinations(), ids: []int64{4, 999}, wantErr: ErrPersonalDestinationUnavailable,
		},
		{
			name: "nil personals", supported: allSinks,
			fake: nil, ids: []int64{4}, wantErr: ErrPersonalDestinationUnavailable,
		},
		{
			name:      "selected type without a sink",
			supported: map[string]bool{registry.TypeNtfy: true, registry.TypeWebhook: true},
			fake:      &fakePersonalTargets{targets: []personal.Target{{ID: 1, Type: registry.TypeDiscord}}},
			ids:       []int64{1},
			wantErr:   ErrPersonalDestinationUnavailable,
		},
	}
	for _, via := range []RecipientVia{RecipientViaPersonal, RecipientViaBoth} {
		for _, testCase := range testCases {
			testInstance.Run(string(via)+"/"+testCase.name, func(subTest *testing.T) {
				var fixture recipientFixture
				if testCase.fake == nil {
					fixture = newRecipientFixture()
				} else {
					fixture = newPersonalFixture(testCase.supported, testCase.fake)
				}

				_, _, submitErr := fixture.service.Submit(context.Background(), routedRecipientEnvelope(via, testCase.ids...))

				if !errors.Is(submitErr, testCase.wantErr) {
					subTest.Fatalf("error = %v, want %v", submitErr, testCase.wantErr)
				}
				if testCase.wantErr == lookupErr && errors.Is(submitErr, ErrPersonalDestinationUnavailable) {
					subTest.Fatalf("a store error must not be reported as the 422 sentinel: %v", submitErr)
				}
				if errors.Is(submitErr, ErrUnsupportedDestinationType) {
					subTest.Fatalf("error = %v, must not reveal an unsupported type", submitErr)
				}
				if len(fixture.store.insertCalls) != 0 || fixture.wakes.calls() != 0 {
					subTest.Fatalf("insert/wake = %d/%d, want none", len(fixture.store.insertCalls), fixture.wakes.calls())
				}
			})
		}
	}
}

func TestSubmitDuplicateRecipientShortCircuitsBeforePersonalLookup(testInstance *testing.T) {
	fake := &fakePersonalTargets{err: errors.New("must not be called")}
	fixture := newPersonalFixture(allSinks, fake)
	fixture.publishers.has = false
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = StoredMessage{ID: "22222222-2222-2222-2222-222222222222", Envelope: validEnvelope()}

	_, outcome, submitErr := fixture.service.Submit(context.Background(), routedRecipientEnvelope(RecipientViaBoth, 4))
	if submitErr != nil || !outcome.Duplicate {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want a duplicate", submitErr, outcome)
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
	storedEnvelope.ToRecipientVia = RecipientViaBoth
	storedEnvelope.ToPersonalDestinationIDs = []int64{7}
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = storedAs(storedEnvelope, StateScheduled)
	fixture.store.replaceReplaced = true
	return fixture, storedEnvelope
}

func TestSubmitReplacedScheduledRecipientRecomputesPersonalTargets(testInstance *testing.T) {
	fake := &fakePersonalTargets{targets: []personal.Target{
		{ID: 7, Type: registry.TypeWebhook}, {ID: 8, Type: registry.TypeWebhook},
	}}
	fixture, storedEnvelope := scheduledRecipientFixture(fake)
	changed := storedEnvelope
	changed.ToRecipientVia = RecipientViaPersonal
	changed.ToPersonalDestinationIDs = []int64{8}

	_, outcome, err := fixture.service.Submit(context.Background(), changed)
	if err != nil || !outcome.Replaced {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want replaced", err, outcome)
	}
	if len(fake.calls) != 1 {
		testInstance.Fatalf("personal lookups = %d, want 1", len(fake.calls))
	}
	want := []DeliveryTarget{{DestinationName: "@personal_8", DestinationType: registry.TypeWebhook}}
	if targets := fixture.store.replaceCalls[0].targets; !reflect.DeepEqual(targets, want) {
		testInstance.Fatalf("replace targets = %+v, want %+v (the new routing)", targets, want)
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

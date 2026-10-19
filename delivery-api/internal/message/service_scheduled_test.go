package message

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	existingMessageID = "22222222-2222-2222-2222-222222222222"
	otherMessageID    = "33333333-3333-3333-3333-333333333333"
)

// scheduledAt is the deliver_at of the stored scheduled messages in these tests,
// an hour after the fixture clock.
func scheduledAt() time.Time { return fixtureNow.Add(time.Hour) }

// scheduledEnvelope is a valid envelope scheduled for scheduledAt.
func scheduledEnvelope() Envelope {
	envelope := validEnvelope()
	deliverAt := scheduledAt()
	envelope.DeliverAt = &deliverAt
	// The payload is spelled the way jsonb prints it (sorted keys, spaces).
	envelope.Payload = json.RawMessage(`{"a": 1, "b": [1, 2]}`)
	return envelope
}

// storedAs returns the message as the store would hold it after envelope was
// submitted, with its deliveries in state.
func storedAs(envelope Envelope, state string) StoredMessage {
	return StoredMessage{
		ID:         existingMessageID,
		Envelope:   envelope,
		Deliveries: []Delivery{{State: state}},
	}
}

// withExisting scripts GetMessageByKey to return message.
func (fixture serviceFixture) withExisting(message StoredMessage) serviceFixture {
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = message
	return fixture
}

func TestSubmitClassifiesDeliverAtWithTheServiceClock(testInstance *testing.T) {
	testCases := []struct {
		name          string
		deliverAt     *time.Time
		wantScheduled bool
	}{
		{name: "one second after now is scheduled", deliverAt: timePointer(fixtureNow.Add(time.Second)), wantScheduled: true},
		{name: "equal to now is immediate", deliverAt: timePointer(fixtureNow)},
		{name: "before now is immediate", deliverAt: timePointer(fixtureNow.Add(-time.Hour))},
		{name: "absent is immediate"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newServiceFixture()
			envelope := validEnvelope()
			envelope.DeliverAt = testCase.deliverAt

			stored, outcome, err := fixture.service.Submit(context.Background(), envelope)
			if err != nil || outcome.Duplicate || outcome.Replaced {
				subTest.Fatalf("Submit = (%v, outcome %+v), want a new message", err, outcome)
			}
			if outcome.Scheduled != testCase.wantScheduled || stored.ID == "" {
				subTest.Fatalf("outcome %+v, want Scheduled %v", outcome, testCase.wantScheduled)
			}
			store := fixture.store
			if testCase.wantScheduled {
				if len(store.insertCalls) != 0 || len(store.scheduledInsertCalls) != 1 {
					subTest.Fatalf("insert/scheduled insert = %d/%d, want 0/1",
						len(store.insertCalls), len(store.scheduledInsertCalls))
				}
				wantCaps := ScheduledCaps{PerApp: fixtureMaxScheduled, Total: fixtureMaxScheduledTotal}
				if got := store.scheduledInsertCalls[0].caps; got != wantCaps {
					subTest.Fatalf("caps passed to the store = %+v, want %+v", got, wantCaps)
				}
				if fixture.wakes.calls() != 0 {
					subTest.Fatalf("Wake calls = %d, want 0 for a scheduled message", fixture.wakes.calls())
				}
				return
			}
			if len(store.insertCalls) != 1 || len(store.scheduledInsertCalls) != 0 {
				subTest.Fatalf("insert/scheduled insert = %d/%d, want 1/0",
					len(store.insertCalls), len(store.scheduledInsertCalls))
			}
			if fixture.wakes.calls() != 1 {
				subTest.Fatalf("Wake calls = %d, want 1 for an immediate message", fixture.wakes.calls())
			}
		})
	}
}

func TestSubmitReplacesAScheduledMessageWithDifferentContent(testInstance *testing.T) {
	fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), StateScheduled))
	envelope := scheduledEnvelope()
	envelope.Channel = "changed"
	fixture.store.replaceReplaced = true

	stored, outcome, err := fixture.service.Submit(context.Background(), envelope)
	if err != nil {
		testInstance.Fatalf("Submit error = %v", err)
	}
	if !outcome.Replaced || outcome.Duplicate || !outcome.Scheduled {
		testInstance.Fatalf("outcome = %+v, want replaced and still scheduled", outcome)
	}
	if stored.Envelope.Channel != "changed" {
		testInstance.Fatalf("stored channel = %q, want the replaced content", stored.Envelope.Channel)
	}
	if len(fixture.store.replaceCalls) != 1 {
		testInstance.Fatalf("replace calls = %d, want 1", len(fixture.store.replaceCalls))
	}
	call := fixture.store.replaceCalls[0]
	if call.envelope.Channel != "changed" || len(call.targets) != 1 || !call.now.Equal(fixtureNow) {
		testInstance.Fatalf("replace call = %+v, want the new envelope, re-resolved targets and the service clock", call)
	}
	if len(fixture.resolver.calls) != 1 {
		testInstance.Fatalf("resolver calls = %d, want 1 (a replace re-resolves)", len(fixture.resolver.calls))
	}
	if len(fixture.store.insertCalls) != 0 || len(fixture.store.scheduledInsertCalls) != 0 {
		testInstance.Fatalf("a replace must not insert")
	}
	if fixture.wakes.calls() != 0 {
		testInstance.Fatalf("Wake calls = %d, want 0 while the message stays scheduled", fixture.wakes.calls())
	}
}

func TestSubmitReplaceToAnImmediateTimeWakesExactlyOnce(testInstance *testing.T) {
	fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), StateScheduled))
	envelope := scheduledEnvelope()
	envelope.DeliverAt = nil
	released := scheduledMessageFor(envelope, StatePending)
	fixture.store.replaceResult = &released
	fixture.store.replaceReplaced = true

	_, outcome, err := fixture.service.Submit(context.Background(), envelope)
	if err != nil || !outcome.Replaced || outcome.Duplicate || outcome.Scheduled {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want replaced and released", err, outcome)
	}
	if fixture.wakes.calls() != 1 {
		testInstance.Fatalf("Wake calls = %d, want exactly 1", fixture.wakes.calls())
	}
}

func TestSubmitIdenticalRetryOfAScheduledMessageIsADuplicateWithoutResolving(testInstance *testing.T) {
	aliceHash := personhash.Hash("alice@example.com")
	testCases := []struct {
		name   string
		stored func() Envelope
		retry  func(envelope *Envelope)
	}{
		{name: "same content", retry: func(*Envelope) {}},
		{
			name:  "reordered payload keys and extra whitespace",
			retry: func(envelope *Envelope) { envelope.Payload = json.RawMessage(" {\n \"b\" : [ 1,\t2 ] , \"a\":1 } ") },
		},
		{
			name: "deliver_at in another offset",
			retry: func(envelope *Envelope) {
				shifted := scheduledAt().In(time.FixedZone("plus5", 5*3600))
				envelope.DeliverAt = &shifted
			},
		},
		{
			name: "deliver_at with sub-microsecond nanoseconds",
			retry: func(envelope *Envelope) {
				precise := scheduledAt().Add(999 * time.Nanosecond)
				envelope.DeliverAt = &precise
			},
		},
		{
			name: "nil display equals absent",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.Display = nil
				return envelope
			},
			retry: func(envelope *Envelope) { envelope.Display = nil },
		},
		{
			name: "stored nil display equals a retried empty display",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.Display = nil
				return envelope
			},
			retry: func(envelope *Envelope) { envelope.Display = &Display{} },
		},
		{
			name: "stored empty display equals a retried nil display",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.Display = &Display{}
				return envelope
			},
			retry: func(envelope *Envelope) { envelope.Display = nil },
		},
		{
			name: "payload numbers jsonb rewrites compare equal",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.Payload = json.RawMessage(`{"a": 100, "b": 0.0000001, "c": 0, "d": 0.01}`)
				return envelope
			},
			retry: func(envelope *Envelope) {
				envelope.Payload = json.RawMessage(`{"a":1e2,"b":1e-7,"c":-0,"d":1E-2}`)
			},
		},
		{
			name: "equal display",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.Display = &Display{Title: "T", Body: "B", Priority: "high"}
				return envelope
			},
			retry: func(envelope *Envelope) { envelope.Display = &Display{Title: "T", Body: "B", Priority: "high"} },
		},
		{
			name: "email recipient against its stored hash",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.ToRecipient = aliceHash
				return envelope
			},
			retry: func(envelope *Envelope) { envelope.ToRecipient = " Alice@Example.com " },
		},
		{
			name: "same destination",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.ToDestination = "ops_webhook"
				return envelope
			},
			retry: func(envelope *Envelope) { envelope.ToDestination = "ops_webhook" },
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			storedEnvelope := scheduledEnvelope()
			if testCase.stored != nil {
				storedEnvelope = testCase.stored()
			}
			fixture := newServiceFixture().withExisting(storedAs(storedEnvelope, StateScheduled))
			// Resolution would now fail: an identical retry must never reach it.
			fixture.resolver.err = registry.ErrDestinationDisabled
			retry := storedEnvelope
			testCase.retry(&retry)

			stored, outcome, err := fixture.service.Submit(context.Background(), retry)
			if err != nil || !outcome.Duplicate || outcome.Replaced || !outcome.Scheduled {
				subTest.Fatalf("Submit = (%v, outcome %+v), want a scheduled duplicate", err, outcome)
			}
			if stored.ID != existingMessageID {
				subTest.Fatalf("stored id = %q, want the original", stored.ID)
			}
			if len(fixture.resolver.calls) != 0 || len(fixture.store.replaceCalls) != 0 || fixture.wakes.calls() != 0 {
				subTest.Fatalf("resolve/replace/wake = %d/%d/%d, want none",
					len(fixture.resolver.calls), len(fixture.store.replaceCalls), fixture.wakes.calls())
			}
		})
	}
}

func TestSubmitAChangeInAnyComparedFieldIsAReplace(testInstance *testing.T) {
	testCases := []struct {
		name   string
		stored func() Envelope
		change func(envelope *Envelope)
	}{
		{name: "channel", change: func(envelope *Envelope) { envelope.Channel = "other" }},
		{name: "destination", change: func(envelope *Envelope) { envelope.ToDestination = "ops_webhook" }},
		{name: "critical", change: func(envelope *Envelope) { envelope.Critical = true }},
		{
			name: "deliver_at by one microsecond",
			change: func(envelope *Envelope) {
				later := scheduledAt().Add(time.Microsecond)
				envelope.DeliverAt = &later
			},
		},
		{name: "deliver_at removed", change: func(envelope *Envelope) { envelope.DeliverAt = nil }},
		{name: "payload value", change: func(envelope *Envelope) { envelope.Payload = json.RawMessage(`{"a":2,"b":[1,2]}`) }},
		{name: "payload key", change: func(envelope *Envelope) { envelope.Payload = json.RawMessage(`{"c":1,"b":[1,2]}`) }},
		{name: "payload extra key", change: func(envelope *Envelope) { envelope.Payload = json.RawMessage(`{"a":1,"b":[1,2],"c":0}`) }},
		{
			name: "integer beyond float64 precision",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.Payload = json.RawMessage(`{"id":9007199254740992}`)
				return envelope
			},
			change: func(envelope *Envelope) { envelope.Payload = json.RawMessage(`{"id":9007199254740993}`) },
		},
		{name: "display added", change: func(envelope *Envelope) { envelope.Display = &Display{Title: "T"} }},
		{
			name: "display changed",
			stored: func() Envelope {
				envelope := scheduledEnvelope()
				envelope.Display = &Display{Title: "T"}
				return envelope
			},
			change: func(envelope *Envelope) { envelope.Display = &Display{Title: "U"} },
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			storedEnvelope := scheduledEnvelope()
			if testCase.stored != nil {
				storedEnvelope = testCase.stored()
			}
			fixture := newServiceFixture().withExisting(storedAs(storedEnvelope, StateScheduled))
			fixture.store.replaceReplaced = true
			changed := storedEnvelope
			testCase.change(&changed)

			_, outcome, err := fixture.service.Submit(context.Background(), changed)
			if err != nil || !outcome.Replaced || outcome.Duplicate {
				subTest.Fatalf("Submit = (%v, outcome %+v), want replaced", err, outcome)
			}
			if len(fixture.store.replaceCalls) != 1 || len(fixture.resolver.calls) != 1 {
				subTest.Fatalf("replace/resolve calls = %d/%d, want 1/1",
					len(fixture.store.replaceCalls), len(fixture.resolver.calls))
			}
		})
	}
}

func TestSubmitChangedRecipientIsAReplace(testInstance *testing.T) {
	fixture := newRecipientFixture()
	storedEnvelope := scheduledEnvelope()
	storedEnvelope.ToRecipient = personhash.Hash("alice@example.com")
	storedEnvelope.Display = &Display{Title: "T", Body: "B"}
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = storedAs(storedEnvelope, StateScheduled)
	fixture.store.replaceReplaced = true

	changed := storedEnvelope
	changed.ToRecipient = "bob@example.com"
	_, outcome, err := fixture.service.Submit(context.Background(), changed)
	if err != nil || !outcome.Replaced {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want replaced", err, outcome)
	}
	if got, want := fixture.store.replaceCalls[0].envelope.ToRecipient, personhash.Hash("bob@example.com"); got != want {
		testInstance.Fatalf("replaced ToRecipient = %q, want the hash %q", got, want)
	}
}

func TestSubmitReleasedOrCancelledKeyIsADuplicateWithTheStoredContent(testInstance *testing.T) {
	for _, state := range []string{StatePending, StateDelivered, StateDead, StateCancelled} {
		testInstance.Run(state, func(subTest *testing.T) {
			fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), state))
			fixture.resolver.err = registry.ErrDestinationDisabled
			envelope := scheduledEnvelope()
			envelope.Channel = "changed"

			stored, outcome, err := fixture.service.Submit(context.Background(), envelope)
			if err != nil || !outcome.Duplicate || outcome.Replaced {
				subTest.Fatalf("Submit = (%v, outcome %+v), want a duplicate", err, outcome)
			}
			if stored.Envelope.Channel != "alerts" {
				subTest.Fatalf("stored channel = %q, want the stored content, not the new one", stored.Envelope.Channel)
			}
			if len(fixture.resolver.calls) != 0 || len(fixture.store.replaceCalls) != 0 || fixture.wakes.calls() != 0 {
				subTest.Fatalf("resolve/replace/wake = %d/%d/%d, want none",
					len(fixture.resolver.calls), len(fixture.store.replaceCalls), fixture.wakes.calls())
			}
		})
	}
}

func TestSubmitReplaceThatLosesTheRaceIsADuplicateWithTheStoredContent(testInstance *testing.T) {
	for _, state := range []string{StatePending, StateCancelled} {
		testInstance.Run(state, func(subTest *testing.T) {
			fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), StateScheduled))
			winner := storedAs(scheduledEnvelope(), state)
			fixture.store.replaceResult = &winner
			fixture.store.replaceReplaced = false
			envelope := scheduledEnvelope()
			envelope.Channel = "changed"

			stored, outcome, err := fixture.service.Submit(context.Background(), envelope)
			if err != nil || !outcome.Duplicate || outcome.Replaced {
				subTest.Fatalf("Submit = (%v, outcome %+v), want a duplicate and no error", err, outcome)
			}
			if stored.Envelope.Channel != "alerts" {
				subTest.Fatalf("stored channel = %q, want the stored content", stored.Envelope.Channel)
			}
			if fixture.wakes.calls() != 0 {
				subTest.Fatalf("Wake calls = %d, want 0 when nothing was stored", fixture.wakes.calls())
			}
		})
	}
}

// keySequenceStore answers GetMessageByKey from a script, one entry per call
// (the last repeats): a nil message is ErrMessageNotFound.
type keySequenceStore struct {
	*fakeServiceStore
	script []*StoredMessage
	calls  int
}

func (store *keySequenceStore) GetMessageByKey(context.Context, string, string) (StoredMessage, error) {
	index := min(store.calls, len(store.script)-1)
	store.calls++
	if store.script[index] == nil {
		return StoredMessage{}, ErrMessageNotFound
	}
	return *store.script[index], nil
}

func TestSubmitReplaceThatFindsNoRowRereadsTheKey(testInstance *testing.T) {
	scheduled := storedAs(scheduledEnvelope(), StateScheduled)
	winner := storedAs(scheduledEnvelope(), StatePending)
	winner.Envelope.Channel = "winner"
	envelope := scheduledEnvelope()
	envelope.Channel = "changed"

	testInstance.Run("another submit stored the key", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.replaceErr = ErrMessageNotFound
		sequenced := &keySequenceStore{fakeServiceStore: fixture.store, script: []*StoredMessage{&scheduled, &winner}}
		service := NewService(ServiceConfig{
			Store: sequenced, Resolver: fixture.resolver, Now: func() time.Time { return fixtureNow },
			SupportedTypes: map[string]bool{registry.TypeWebhook: true}, Wake: fixture.wakes.wake,
		})

		stored, outcome, err := service.Submit(context.Background(), envelope)
		if err != nil || !outcome.Duplicate || outcome.Replaced {
			subTest.Fatalf("Submit = (%v, outcome %+v), want a duplicate and no error", err, outcome)
		}
		if stored.Envelope.Channel != "winner" {
			subTest.Fatalf("stored channel = %q, want the stored content", stored.Envelope.Channel)
		}
		if fixture.wakes.calls() != 0 {
			subTest.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
		}
	})

	testInstance.Run("the message is gone", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.replaceErr = ErrMessageNotFound
		sequenced := &keySequenceStore{fakeServiceStore: fixture.store, script: []*StoredMessage{&scheduled, nil}}
		service := NewService(ServiceConfig{
			Store: sequenced, Resolver: fixture.resolver, Now: func() time.Time { return fixtureNow },
			SupportedTypes: map[string]bool{registry.TypeWebhook: true}, Wake: fixture.wakes.wake,
		})

		_, outcome, err := service.Submit(context.Background(), envelope)
		if err != nil || outcome.Duplicate || outcome.Replaced {
			subTest.Fatalf("Submit = (%v, outcome %+v), want it stored as a new message", err, outcome)
		}
		if len(fixture.store.scheduledInsertCalls) != 1 {
			subTest.Fatalf("scheduled insert calls = %d, want 1", len(fixture.store.scheduledInsertCalls))
		}
	})
}

func TestSubmitReplaceResolutionFailureReturnsTheSubmitErrorAndStoresNothing(testInstance *testing.T) {
	for _, resolveErr := range []error{registry.ErrUnknownDestination, registry.ErrDestinationDisabled} {
		testInstance.Run(resolveErr.Error(), func(subTest *testing.T) {
			fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), StateScheduled))
			fixture.resolver.err = resolveErr
			envelope := scheduledEnvelope()
			envelope.ToDestination = "gone"

			_, _, err := fixture.service.Submit(context.Background(), envelope)
			if !errors.Is(err, resolveErr) {
				subTest.Fatalf("Submit error = %v, want %v", err, resolveErr)
			}
			if len(fixture.store.replaceCalls) != 0 || len(fixture.store.insertCalls) != 0 ||
				len(fixture.store.scheduledInsertCalls) != 0 || fixture.wakes.calls() != 0 {
				subTest.Fatalf("a failed replace must touch nothing")
			}
		})
	}
}

func TestSubmitLostScheduledInsertRaceIsADuplicateWithoutWake(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.scheduledInsertDuplicate = true

	_, outcome, err := fixture.service.Submit(context.Background(), scheduledEnvelope())
	if err != nil || !outcome.Duplicate || outcome.Replaced {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want a duplicate and no error", err, outcome)
	}
	if fixture.wakes.calls() != 0 {
		testInstance.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
	}
}

func TestSubmitCapOnlyRefusesANewScheduledSubmit(testInstance *testing.T) {
	testInstance.Run("new scheduled submit at the cap", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.scheduledInsertErr = ErrTooManyScheduled

		_, _, err := fixture.service.Submit(context.Background(), scheduledEnvelope())
		if !errors.Is(err, ErrTooManyScheduled) {
			subTest.Fatalf("Submit error = %v, want ErrTooManyScheduled", err)
		}
		if fixture.wakes.calls() != 0 {
			subTest.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
		}
	})
	testInstance.Run("immediate submit never reaches the cap", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.scheduledInsertErr = ErrTooManyScheduled

		if _, _, err := fixture.service.Submit(context.Background(), validEnvelope()); err != nil {
			subTest.Fatalf("Submit error = %v, want an immediate submit at the cap to succeed", err)
		}
		if len(fixture.store.scheduledInsertCalls) != 0 {
			subTest.Fatalf("scheduled insert calls = %d, want 0", len(fixture.store.scheduledInsertCalls))
		}
	})
	testInstance.Run("replace is exempt", func(subTest *testing.T) {
		fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), StateScheduled))
		fixture.store.scheduledInsertErr = ErrTooManyScheduled
		fixture.store.replaceReplaced = true
		envelope := scheduledEnvelope()
		envelope.Channel = "changed"

		_, outcome, err := fixture.service.Submit(context.Background(), envelope)
		if err != nil || !outcome.Replaced {
			subTest.Fatalf("Submit = (%v, outcome %+v), want a replace at the cap to succeed", err, outcome)
		}
	})
}

func TestSubmitValidatesDeliverAtBeforeTheStore(testInstance *testing.T) {
	fixture := newServiceFixture()
	envelope := validEnvelope()
	zero := time.Time{}
	envelope.DeliverAt = &zero

	_, _, err := fixture.service.Submit(context.Background(), envelope)
	wantValidationMessage(testInstance, err, "invalid deliver_at")
	if fixture.store.getByKeyCalls != 0 {
		testInstance.Fatalf("store lookups = %d, want 0", fixture.store.getByKeyCalls)
	}
}

func TestNewServiceDefaultsTheScheduledCap(testInstance *testing.T) {
	store := &fakeServiceStore{}
	service := NewService(ServiceConfig{Store: store, Resolver: &fakeResolver{
		result: []registry.ResolvedDestination{webhookDestination},
	}, SupportedTypes: map[string]bool{registry.TypeWebhook: true}, Now: func() time.Time { return fixtureNow }})

	if _, _, err := service.Submit(context.Background(), scheduledEnvelope()); err != nil {
		testInstance.Fatalf("Submit error = %v", err)
	}
	wantCaps := ScheduledCaps{PerApp: DefaultMaxScheduledPerApp, Total: DefaultMaxScheduledTotal}
	if got := store.scheduledInsertCalls[0].caps; got != wantCaps {
		testInstance.Fatalf("caps = %+v, want the defaults %+v", got, wantCaps)
	}
}

func TestSubmitTotalCapOnlyRefusesANewScheduledSubmit(testInstance *testing.T) {
	testInstance.Run("new scheduled submit at the total cap", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.scheduledInsertErr = ErrScheduledCapacityReached

		_, _, err := fixture.service.Submit(context.Background(), scheduledEnvelope())
		if !errors.Is(err, ErrScheduledCapacityReached) || errors.Is(err, ErrTooManyScheduled) {
			subTest.Fatalf("Submit error = %v, want ErrScheduledCapacityReached only", err)
		}
		if fixture.wakes.calls() != 0 {
			subTest.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
		}
	})
	testInstance.Run("immediate submit never reaches the cap", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.scheduledInsertErr = ErrScheduledCapacityReached

		if _, _, err := fixture.service.Submit(context.Background(), validEnvelope()); err != nil {
			subTest.Fatalf("Submit error = %v, want an immediate submit at the total cap to succeed", err)
		}
	})
	testInstance.Run("replace is exempt", func(subTest *testing.T) {
		fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), StateScheduled))
		fixture.store.scheduledInsertErr = ErrScheduledCapacityReached
		fixture.store.replaceReplaced = true
		envelope := scheduledEnvelope()
		envelope.Channel = "changed"

		_, outcome, err := fixture.service.Submit(context.Background(), envelope)
		if err != nil || !outcome.Replaced {
			subTest.Fatalf("Submit = (%v, outcome %+v), want a replace at the total cap to succeed", err, outcome)
		}
	})
}

func TestMaxScheduledTotalFromEnv(testInstance *testing.T) {
	testCases := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{name: "empty gives the default", value: "", want: 100000},
		{name: "smallest", value: "1", want: 1},
		{name: "explicit", value: "2500", want: 2500},
		{name: "largest", value: "10000000", want: 10000000},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-3", wantErr: true},
		{name: "above the maximum", value: "10000001", wantErr: true},
		{name: "not a number", value: "abc", wantErr: true},
		{name: "with spaces", value: " 5 ", wantErr: true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			got, err := MaxScheduledTotalFromEnv(func(name string) string {
				if name != "DELIVERY_MAX_SCHEDULED_TOTAL" {
					subTest.Fatalf("read env %q, want DELIVERY_MAX_SCHEDULED_TOTAL", name)
				}
				return testCase.value
			})
			if testCase.wantErr {
				if err == nil {
					subTest.Fatalf("MaxScheduledTotalFromEnv(%q) = %d, want an error", testCase.value, got)
				}
				if testCase.value != "" && strings.Contains(err.Error(), strings.TrimSpace(testCase.value)) {
					subTest.Fatalf("error %q must not contain the raw value %q", err, testCase.value)
				}
				if !strings.Contains(err.Error(), "DELIVERY_MAX_SCHEDULED_TOTAL") {
					subTest.Fatalf("error %q should name the variable", err)
				}
				return
			}
			if err != nil || got != testCase.want {
				subTest.Fatalf("MaxScheduledTotalFromEnv(%q) = (%d, %v), want %d", testCase.value, got, err, testCase.want)
			}
		})
	}
}

func TestMaxScheduledFromEnv(testInstance *testing.T) {
	testCases := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{name: "empty gives the default", value: "", want: 1000},
		{name: "smallest", value: "1", want: 1},
		{name: "explicit", value: "250", want: 250},
		{name: "largest", value: "100000", want: 100000},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-3", wantErr: true},
		{name: "above the maximum", value: "100001", wantErr: true},
		{name: "not a number", value: "abc", wantErr: true},
		{name: "with spaces", value: " 5 ", wantErr: true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			got, err := MaxScheduledFromEnv(func(name string) string {
				if name != "DELIVERY_MAX_SCHEDULED_PER_APP" {
					subTest.Fatalf("read env %q, want DELIVERY_MAX_SCHEDULED_PER_APP", name)
				}
				return testCase.value
			})
			if testCase.wantErr {
				if err == nil {
					subTest.Fatalf("MaxScheduledFromEnv(%q) = %d, want an error", testCase.value, got)
				}
				if testCase.value != "" && strings.Contains(err.Error(), strings.TrimSpace(testCase.value)) {
					subTest.Fatalf("error %q must not contain the raw value %q", err, testCase.value)
				}
				if !strings.Contains(err.Error(), "DELIVERY_MAX_SCHEDULED_PER_APP") {
					subTest.Fatalf("error %q should name the variable", err)
				}
				return
			}
			if err != nil || got != testCase.want {
				subTest.Fatalf("MaxScheduledFromEnv(%q) = (%d, %v), want %d", testCase.value, got, err, testCase.want)
			}
		})
	}
}

func TestDefaultMaxScheduledMatchesTheComposeDefault(testInstance *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		testInstance.Fatal("cannot locate this test file")
	}
	// This file is <repo>/delivery-api/internal/message/<file>.
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	caps := []struct {
		envVar      string
		codeDefault int
	}{
		{envVar: MaxScheduledEnvVar, codeDefault: DefaultMaxScheduledPerApp},
		{envVar: MaxScheduledTotalEnvVar, codeDefault: DefaultMaxScheduledTotal},
	}
	for _, scheduledCap := range caps {
		// The pass-through line of the cap in a compose file, capturing its default.
		pattern := regexp.MustCompile(
			`(?m)^\s*- ` + scheduledCap.envVar + `=\$\{` + scheduledCap.envVar + `:-(\d+)\}\s*$`)
		for _, composeName := range []string{"docker-compose.yml", "docker-compose.prod.yml"} {
			composeText, readErr := os.ReadFile(filepath.Join(repoRoot, composeName))
			if readErr != nil {
				testInstance.Fatalf("read %s: %v", composeName, readErr)
			}
			matches := pattern.FindAllStringSubmatch(string(composeText), -1)
			if len(matches) != 1 {
				testInstance.Fatalf("%s has %d %s default lines, want exactly 1", composeName, len(matches), scheduledCap.envVar)
			}
			if want := strconv.Itoa(scheduledCap.codeDefault); matches[0][1] != want {
				testInstance.Errorf("%s defaults %s to %s, want the code default %s",
					composeName, scheduledCap.envVar, matches[0][1], want)
			}
		}
	}
}

func TestCancelIsScopedToTheAppAndValidatesInputs(testInstance *testing.T) {
	testInstance.Run("non-UUID id is not_found with no store call", func(subTest *testing.T) {
		fixture := newServiceFixture()
		outcome, err := fixture.service.Cancel(context.Background(), "urls4irl", "not-a-uuid")
		if err != nil || outcome.Found || outcome.Cancelled || outcome.Status != StatusNotFound || outcome.MessageID != "not-a-uuid" {
			subTest.Fatalf("Cancel = (%+v, %v), want not_found echoing the id", outcome, err)
		}
		if len(fixture.store.cancelCalls) != 0 {
			subTest.Fatalf("store cancel calls = %d, want 0", len(fixture.store.cancelCalls))
		}
	})

	testInstance.Run("malformed app_id is a validation error", func(subTest *testing.T) {
		fixture := newServiceFixture()
		_, err := fixture.service.Cancel(context.Background(), "Bad App", existingMessageID)
		wantValidationMessage(subTest, err, "invalid app_id")
		_, err = fixture.service.Cancel(context.Background(), "", existingMessageID)
		wantValidationMessage(subTest, err, "invalid app_id")
		if len(fixture.store.cancelCalls) != 0 {
			subTest.Fatalf("store cancel calls = %d, want 0", len(fixture.store.cancelCalls))
		}
	})

	testInstance.Run("passes the app and the service clock to the store", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.cancelResult = CancelOutcome{
			MessageID: existingMessageID, Status: StatusCancelled, Cancelled: true, Found: true,
		}
		outcome, err := fixture.service.Cancel(context.Background(), "urls4irl", existingMessageID)
		if err != nil || !outcome.Cancelled || outcome.Status != StatusCancelled || outcome.MessageID != existingMessageID {
			subTest.Fatalf("Cancel = (%+v, %v), want cancelled", outcome, err)
		}
		want := []cancelCall{{kind: "app", id: existingMessageID, appID: "urls4irl", now: fixtureNow}}
		if !reflect.DeepEqual(fixture.store.cancelCalls, want) {
			subTest.Fatalf("cancel calls = %+v, want %+v", fixture.store.cancelCalls, want)
		}
	})

	testInstance.Run("another app's message is not_found", func(subTest *testing.T) {
		fixture := newServiceFixture()
		// The store reports a miss for a message that belongs to another app.
		fixture.store.cancelResult = CancelOutcome{}
		outcome, err := fixture.service.Cancel(context.Background(), "urls4irl", otherMessageID)
		if err != nil || outcome.Found || outcome.Cancelled || outcome.Status != StatusNotFound || outcome.MessageID != otherMessageID {
			subTest.Fatalf("Cancel = (%+v, %v), want not_found echoing the id", outcome, err)
		}
		if fixture.store.cancelCalls[0].kind != "app" || fixture.store.cancelCalls[0].appID != "urls4irl" {
			subTest.Fatalf("cancel call = %+v, want the app-scoped store method", fixture.store.cancelCalls[0])
		}
	})
}

func TestCancelReportsTheStoredStatus(testInstance *testing.T) {
	testCases := []struct {
		name          string
		stored        CancelOutcome
		wantStatus    string
		wantCancelled bool
	}{
		{
			name:       "cancelled now",
			stored:     CancelOutcome{MessageID: existingMessageID, Status: StatusCancelled, Cancelled: true, Found: true},
			wantStatus: StatusCancelled, wantCancelled: true,
		},
		{
			name:       "already cancelled",
			stored:     CancelOutcome{MessageID: existingMessageID, Status: StatusCancelled, Cancelled: true, Found: true},
			wantStatus: StatusCancelled, wantCancelled: true,
		},
		{
			name:       "released message",
			stored:     CancelOutcome{MessageID: existingMessageID, Status: StatusDelivered, Found: true},
			wantStatus: StatusDelivered,
		},
		{
			name:       "pending message",
			stored:     CancelOutcome{MessageID: existingMessageID, Status: StatusPending, Found: true},
			wantStatus: StatusPending,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newServiceFixture()
			fixture.store.cancelResult = testCase.stored

			outcome, err := fixture.service.Cancel(context.Background(), "urls4irl", existingMessageID)
			if err != nil || outcome.Status != testCase.wantStatus || outcome.Cancelled != testCase.wantCancelled || !outcome.Found {
				subTest.Fatalf("Cancel = (%+v, %v), want status %q cancelled %v",
					outcome, err, testCase.wantStatus, testCase.wantCancelled)
			}
		})
	}
}

func TestCancelLogsOnlyWhenTheCallCancelledTheMessage(testInstance *testing.T) {
	calls := map[string]func(service *Service) (CancelOutcome, error){
		"app": func(service *Service) (CancelOutcome, error) {
			return service.Cancel(context.Background(), "urls4irl", existingMessageID)
		},
		"key": func(service *Service) (CancelOutcome, error) {
			return service.CancelByKey(context.Background(), "urls4irl", "key-1")
		},
		"admin": func(service *Service) (CancelOutcome, error) {
			return service.AdminCancel(context.Background(), existingMessageID)
		},
	}
	stores := []struct {
		name     string
		stored   CancelOutcome
		wantLogs int
	}{
		{
			name: "cancelled by this call",
			stored: CancelOutcome{
				MessageID: existingMessageID, Status: StatusCancelled, Cancelled: true, Transitioned: true, Found: true,
			},
			wantLogs: 1,
		},
		{
			name:   "already cancelled (idempotent repeat)",
			stored: CancelOutcome{MessageID: existingMessageID, Status: StatusCancelled, Cancelled: true, Found: true},
		},
		{
			name:   "already released",
			stored: CancelOutcome{MessageID: existingMessageID, Status: StatusDelivered, Found: true},
		},
		{name: "not found"},
	}
	for callName, call := range calls {
		for _, storeCase := range stores {
			testInstance.Run(callName+"/"+storeCase.name, func(subTest *testing.T) {
				var logBuffer bytes.Buffer
				store := &fakeServiceStore{}
				store.cancelResult = storeCase.stored
				service := NewService(ServiceConfig{
					Store:          store,
					Resolver:       &fakeResolver{},
					SupportedTypes: map[string]bool{registry.TypeWebhook: true},
					Now:            func() time.Time { return fixtureNow },
					Logger:         slog.New(slog.NewTextHandler(&logBuffer, nil)),
				})

				if _, err := call(service); err != nil {
					subTest.Fatalf("cancel error = %v", err)
				}
				if got := strings.Count(logBuffer.String(), "message cancel"); got != storeCase.wantLogs {
					subTest.Fatalf("'message cancel' logged %d times, want %d (log: %q)", got, storeCase.wantLogs, logBuffer.String())
				}
			})
		}
	}
}

func TestCancelPropagatesStoreErrors(testInstance *testing.T) {
	storeErr := errors.New("db down")
	fixture := newServiceFixture()
	fixture.store.cancelErr = storeErr

	if _, err := fixture.service.Cancel(context.Background(), "urls4irl", existingMessageID); !errors.Is(err, storeErr) {
		testInstance.Fatalf("Cancel error = %v, want %v", err, storeErr)
	}
	if _, err := fixture.service.CancelByKey(context.Background(), "urls4irl", "key-1"); !errors.Is(err, storeErr) {
		testInstance.Fatalf("CancelByKey error = %v, want %v", err, storeErr)
	}
	if _, err := fixture.service.AdminCancel(context.Background(), existingMessageID); !errors.Is(err, storeErr) {
		testInstance.Fatalf("AdminCancel error = %v, want %v", err, storeErr)
	}
}

func TestCancelByKey(testInstance *testing.T) {
	testInstance.Run("unknown key echoes an empty message_id", func(subTest *testing.T) {
		fixture := newServiceFixture()
		outcome, err := fixture.service.CancelByKey(context.Background(), "urls4irl", "no-such-key")
		if err != nil || outcome.Found || outcome.Cancelled || outcome.Status != StatusNotFound || outcome.MessageID != "" {
			subTest.Fatalf("CancelByKey = (%+v, %v), want not_found with an empty id", outcome, err)
		}
		want := []cancelCall{{kind: "key", appID: "urls4irl", key: "no-such-key", now: fixtureNow}}
		if !reflect.DeepEqual(fixture.store.cancelCalls, want) {
			subTest.Fatalf("cancel calls = %+v, want %+v", fixture.store.cancelCalls, want)
		}
	})

	testInstance.Run("cancels a scheduled message", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.cancelResult = CancelOutcome{
			MessageID: existingMessageID, Status: StatusCancelled, Cancelled: true, Found: true,
		}
		outcome, err := fixture.service.CancelByKey(context.Background(), "urls4irl", "key-1")
		if err != nil || !outcome.Cancelled || outcome.MessageID != existingMessageID {
			subTest.Fatalf("CancelByKey = (%+v, %v), want cancelled with the message id", outcome, err)
		}
	})

	testInstance.Run("malformed inputs never reach the store", func(subTest *testing.T) {
		fixture := newServiceFixture()
		_, err := fixture.service.CancelByKey(context.Background(), "Bad App", "key-1")
		wantValidationMessage(subTest, err, "invalid app_id")
		_, err = fixture.service.CancelByKey(context.Background(), "urls4irl", "")
		wantValidationMessage(subTest, err, "invalid idempotency_key")
		_, err = fixture.service.CancelByKey(context.Background(), "urls4irl", "has space")
		wantValidationMessage(subTest, err, "invalid idempotency_key")
		if len(fixture.store.cancelCalls) != 0 {
			subTest.Fatalf("store cancel calls = %d, want 0", len(fixture.store.cancelCalls))
		}
	})
}

func TestAdminCancelIsNotScopedToAnApp(testInstance *testing.T) {
	testInstance.Run("reaches the unscoped store method", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.cancelResult = CancelOutcome{
			MessageID: otherMessageID, Status: StatusCancelled, Cancelled: true, Found: true,
		}
		outcome, err := fixture.service.AdminCancel(context.Background(), otherMessageID)
		if err != nil || !outcome.Cancelled || outcome.MessageID != otherMessageID {
			subTest.Fatalf("AdminCancel = (%+v, %v), want cancelled", outcome, err)
		}
		want := []cancelCall{{kind: "admin", id: otherMessageID, now: fixtureNow}}
		if !reflect.DeepEqual(fixture.store.cancelCalls, want) {
			subTest.Fatalf("cancel calls = %+v, want %+v", fixture.store.cancelCalls, want)
		}
	})

	testInstance.Run("non-UUID id is not_found with no store call", func(subTest *testing.T) {
		fixture := newServiceFixture()
		outcome, err := fixture.service.AdminCancel(context.Background(), "nope")
		if err != nil || outcome.Found || outcome.Status != StatusNotFound || outcome.MessageID != "nope" {
			subTest.Fatalf("AdminCancel = (%+v, %v), want not_found echoing the id", outcome, err)
		}
		if len(fixture.store.cancelCalls) != 0 {
			subTest.Fatalf("store cancel calls = %d, want 0", len(fixture.store.cancelCalls))
		}
	})

	testInstance.Run("unknown id is not_found", func(subTest *testing.T) {
		fixture := newServiceFixture()
		outcome, err := fixture.service.AdminCancel(context.Background(), otherMessageID)
		if err != nil || outcome.Found || outcome.Status != StatusNotFound || outcome.MessageID != otherMessageID {
			subTest.Fatalf("AdminCancel = (%+v, %v), want not_found echoing the id", outcome, err)
		}
	})
}

func TestResubmittingACancelledKeyIsADuplicateWithStatusCancelled(testInstance *testing.T) {
	fixture := newServiceFixture().withExisting(storedAs(scheduledEnvelope(), StateCancelled))
	envelope := scheduledEnvelope()
	envelope.Channel = "changed"

	stored, outcome, err := fixture.service.Submit(context.Background(), envelope)
	if err != nil || !outcome.Duplicate || outcome.Scheduled {
		testInstance.Fatalf("Submit = (%v, outcome %+v), want a duplicate that is not scheduled", err, outcome)
	}
	if got := DeriveStatus(stored.Deliveries); got != StatusCancelled {
		testInstance.Fatalf("status = %q, want %q", got, StatusCancelled)
	}
}

func TestListScheduledValidatesLimitAndPassesTotalThrough(testInstance *testing.T) {
	testInstance.Run("rejects limits outside 1..200 before the store", func(subTest *testing.T) {
		fixture := newServiceFixture()
		for _, limit := range []int{0, -1, MaxListLimit + 1} {
			_, _, err := fixture.service.ListScheduled(context.Background(), limit)
			wantValidationMessage(subTest, err, "invalid limit")
		}
		if len(fixture.store.listScheduledCalls) != 0 {
			subTest.Fatalf("store list calls = %d, want 0", len(fixture.store.listScheduledCalls))
		}
	})

	testInstance.Run("accepts the boundaries and passes total through", func(subTest *testing.T) {
		fixture := newServiceFixture()
		fixture.store.listScheduledResult = []ScheduledMessage{{MessageID: existingMessageID, AppID: "urls4irl"}}
		fixture.store.listScheduledTotal = 312
		for _, limit := range []int{1, DefaultListLimit, MaxListLimit} {
			listed, total, err := fixture.service.ListScheduled(context.Background(), limit)
			if err != nil || total != 312 || len(listed) != 1 {
				subTest.Fatalf("ListScheduled(%d) = (%d messages, total %d, %v), want 1 message and total 312",
					limit, len(listed), total, err)
			}
		}
		if want := []int{1, DefaultListLimit, MaxListLimit}; !reflect.DeepEqual(fixture.store.listScheduledCalls, want) {
			subTest.Fatalf("store limits = %v, want %v", fixture.store.listScheduledCalls, want)
		}
	})

	testInstance.Run("never returns nil without an error", func(subTest *testing.T) {
		fixture := newServiceFixture()
		listed, total, err := fixture.service.ListScheduled(context.Background(), DefaultListLimit)
		if err != nil || listed == nil || total != 0 {
			subTest.Fatalf("ListScheduled = (%v, %d, %v), want an empty non-nil slice", listed, total, err)
		}
	})

	testInstance.Run("propagates store errors", func(subTest *testing.T) {
		storeErr := errors.New("db down")
		fixture := newServiceFixture()
		fixture.store.listScheduledErr = storeErr
		if _, _, err := fixture.service.ListScheduled(context.Background(), DefaultListLimit); !errors.Is(err, storeErr) {
			subTest.Fatalf("ListScheduled error = %v, want %v", err, storeErr)
		}
	})
}

func timePointer(value time.Time) *time.Time { return &value }

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
)

// personalTestID is the personal destination id every unit-test claim names.
const personalTestID int64 = 42

type personalArguments struct {
	id                int64
	appID, personHash string
}

type personalDisableCall struct {
	id     int64
	reason string
}

// fakePersonal resolves any personal id to a webhook destination unless err is set.
type fakePersonal struct {
	mutex      sync.Mutex
	err        error
	seen       []personalArguments
	disabled   []personalDisableCall
	disableErr error
}

func (fake *fakePersonal) ResolvePersonal(
	_ context.Context, id int64, appID, personHash string,
) (registry.ResolvedDestination, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.seen = append(fake.seen, personalArguments{id, appID, personHash})
	if fake.err != nil {
		return registry.ResolvedDestination{}, fake.err
	}
	return registry.ResolvedDestination{
		ID:      id,
		Name:    personal.PersonalDeliveryName(id),
		Type:    registry.TypeWebhook,
		Config:  map[string]string{},
		Secrets: map[string]string{"url": secretURL, "signing_secret": secretSigning},
	}, nil
}

func (fake *fakePersonal) DisablePersonal(_ context.Context, id int64, reason string) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.disabled = append(fake.disabled, personalDisableCall{id, reason})
	return fake.disableErr
}

func (fake *fakePersonal) disableCalls() []personalDisableCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]personalDisableCall(nil), fake.disabled...)
}

func (fake *fakePersonal) argumentsSeen() []personalArguments {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]personalArguments(nil), fake.seen...)
}

func newPersonalHarness(t *testing.T, mutate func(config *WorkerConfig)) (*harness, *fakePersonal) {
	t.Helper()
	fake := &fakePersonal{}
	harnessInstance := newHarness(t, func(config *WorkerConfig) {
		config.Personal = fake
		if mutate != nil {
			mutate(config)
		}
	})
	return harnessInstance, fake
}

// newPersonalClaim is a claim for a personal delivery: no destination id, the personal name and the
// person hash on the envelope.
func newPersonalClaim(deliveryID int64) message.Claim {
	claim := newClaim(deliveryID, personal.PersonalDeliveryName(personalTestID))
	claim.Delivery.DestinationID = nil
	claim.Message.Envelope.ToRecipient = testPersonHash
	return claim
}

func TestWorkerDeliversAPersonalRowThroughTheSink(t *testing.T) {
	harnessInstance, fake := newPersonalHarness(t, nil)
	harnessInstance.queue.add(newPersonalClaim(300))
	harnessInstance.start()
	defer harnessInstance.stop(t)

	result := harnessInstance.waitForRecords(t, 1)[0]
	if result.NextState != message.StateDelivered || result.Record.Outcome != message.OutcomeSuccess {
		t.Fatalf("result = %+v, want delivered/success", result)
	}
	want := personalArguments{id: personalTestID, appID: "urls4irl", personHash: testPersonHash}
	if seen := fake.argumentsSeen(); len(seen) != 1 || seen[0] != want {
		t.Errorf("ResolvePersonal arguments = %+v, want [%+v]", seen, want)
	}
	if got := harnessInstance.resolver.resolveCallCount(); got != 0 {
		t.Errorf("Resolver.Resolve calls = %d, want 0", got)
	}
	if got := harnessInstance.sink.callCount(); got != 1 {
		t.Errorf("webhook sink calls = %d, want 1", got)
	}
}

func TestWorkerDeadLettersAPersonalRowByResolutionOutcome(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		nilService bool
		wantReason string
		wantText   string
	}{
		{name: "row absent or person mismatch", err: fmt.Errorf("wrapped: %w", personal.ErrNotFound),
			wantReason: message.DeadReasonDestinationDeleted, wantText: errorTextDestinationDeleted},
		{name: "row disabled", err: personal.ErrDisabled,
			wantReason: message.DeadReasonDestinationDisabled, wantText: errorTextDestinationDisabled},
		{name: "opt-in withdrawn", err: personal.ErrNotOptedIn,
			wantReason: message.DeadReasonOptedOut, wantText: errorTextOptedOut},
		{name: "no personal resolver", nilService: true,
			wantReason: message.DeadReasonUnsupportedType, wantText: errorTextUnsupportedType},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harnessInstance, fake := newPersonalHarness(t, func(config *WorkerConfig) {
				if testCase.nilService {
					config.Personal = nil
				}
			})
			fake.err = testCase.err
			harnessInstance.queue.add(newPersonalClaim(301))
			harnessInstance.start()
			defer harnessInstance.stop(t)

			result := harnessInstance.waitForRecords(t, 1)[0]
			if result.NextState != message.StateDead || result.DeadReason == nil ||
				*result.DeadReason != testCase.wantReason {
				t.Fatalf("result = %+v, want dead/%s", result, testCase.wantReason)
			}
			record := result.Record
			if record.Outcome != message.OutcomeTerminal || record.StatusCode != nil ||
				record.Error == nil || *record.Error == "" || *record.Error != testCase.wantText {
				t.Errorf("record = %+v, want terminal, no status and error %q", record, testCase.wantText)
			}
			if got := harnessInstance.sink.callCount(); got != 0 {
				t.Errorf("webhook sink calls = %d, want 0", got)
			}
			if got := harnessInstance.resolver.resolveCallCount(); got != 0 {
				t.Errorf("Resolver.Resolve calls = %d, want 0", got)
			}
		})
	}
}

func TestWorkerRoutesAnUnparseablePersonalNameToDeletedNeverTheRegistry(t *testing.T) {
	for _, name := range []string{"@personal_", "@personal_abc", "@personal_007", "@personal_1x"} {
		t.Run(name, func(t *testing.T) {
			harnessInstance, fake := newPersonalHarness(t, nil)
			claim := newClaim(302, name)
			claim.Message.Envelope.ToRecipient = testPersonHash
			harnessInstance.queue.add(claim)
			harnessInstance.start()
			defer harnessInstance.stop(t)

			result := harnessInstance.waitForRecords(t, 1)[0]
			if result.NextState != message.StateDead || result.DeadReason == nil ||
				*result.DeadReason != message.DeadReasonDestinationDeleted {
				t.Fatalf("result = %+v, want dead/%s", result, message.DeadReasonDestinationDeleted)
			}
			if got := harnessInstance.resolver.resolveCallCount(); got != 0 {
				t.Errorf("Resolver.Resolve calls = %d, want 0 (never mis-routed to the registry)", got)
			}
			if seen := fake.argumentsSeen(); len(seen) != 0 {
				t.Errorf("ResolvePersonal calls = %+v, want none", seen)
			}
			if got := harnessInstance.sink.callCount(); got != 0 {
				t.Errorf("webhook sink calls = %d, want 0", got)
			}
		})
	}
}

func TestWorkerLeavesTheLeaseWhenPersonalResolutionFailsUnexpectedly(t *testing.T) {
	harnessInstance, fake := newPersonalHarness(t, nil)
	fake.err = errors.New("store unavailable")
	broken := newPersonalClaim(303)
	spent := newPersonalClaim(304)
	spent.Delivery.BudgetStartedAt = testClockStart.Add(-2 * MaxBudgetAge)
	harnessInstance.queue.add(broken, spent)
	harnessInstance.start()

	results := harnessInstance.waitForRecords(t, 1)
	harnessInstance.stop(t)
	if len(results) != 1 || results[0].DeadReason == nil || *results[0].DeadReason != message.DeadReasonBudgetExhausted {
		t.Fatalf("results = %+v, want one dead/%s", results, message.DeadReasonBudgetExhausted)
	}
	if after := harnessInstance.queue.recordedResults(); len(after) != 1 {
		t.Errorf("recorded %d attempts after stop, want still 1", len(after))
	}
	if got := harnessInstance.sink.callCount(); got != 0 {
		t.Errorf("webhook sink calls = %d, want 0", got)
	}
}

func TestWorkerDisablesOnlyThePersonalDestinationOnGone(t *testing.T) {
	harnessInstance, fake := newPersonalHarness(t, nil)
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{Gone: true}
	}
	harnessInstance.queue.add(newPersonalClaim(305))
	harnessInstance.start()
	defer harnessInstance.stop(t)

	result := harnessInstance.waitForRecords(t, 1)[0]
	if result.Record.Outcome != message.OutcomeGone {
		t.Fatalf("outcome = %q, want %q", result.Record.Outcome, message.OutcomeGone)
	}
	waitFor(t, "the personal disable", func() bool { return len(fake.disableCalls()) >= 1 })
	harnessInstance.stop(t)
	want := personalDisableCall{id: personalTestID, reason: "Endpoint returned 410 Gone."}
	if calls := fake.disableCalls(); len(calls) != 1 || calls[0] != want {
		t.Errorf("DisablePersonal calls = %+v, want [%+v]", calls, want)
	}
	if calls := harnessInstance.resolver.disableCalls(); len(calls) != 0 {
		t.Errorf("DisableDestination calls = %+v, want none for a personal delivery", calls)
	}
}

func TestWorkerRecordsAPersonalGoneEvenWhenTheDisableFails(t *testing.T) {
	harnessInstance, fake := newPersonalHarness(t, nil)
	fake.disableErr = errors.New("store unavailable")
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{Gone: true}
	}
	harnessInstance.queue.add(newPersonalClaim(307))
	harnessInstance.start()
	defer harnessInstance.stop(t)

	result := harnessInstance.waitForRecords(t, 1)[0]
	if result.NextState != message.StateDead || result.Record.Outcome != message.OutcomeGone {
		t.Fatalf("result = %+v, want dead/gone recorded despite the failed disable", result)
	}
	waitFor(t, "the personal disable attempt", func() bool { return len(fake.disableCalls()) >= 1 })
}

func TestWorkerGoneRegistryDeliveryNeverDisablesAPersonalDestination(t *testing.T) {
	harnessInstance, fake := newPersonalHarness(t, nil)
	harnessInstance.sink.deliver = func(context.Context, registry.ResolvedDestination, sink.Request) sink.Outcome {
		return sink.Outcome{Gone: true}
	}
	harnessInstance.queue.add(newClaim(306, "ops-alerts"))
	harnessInstance.start()
	defer harnessInstance.stop(t)

	harnessInstance.waitForRecords(t, 1)
	waitFor(t, "the registry disable", func() bool { return len(harnessInstance.resolver.disableCalls()) >= 1 })
	harnessInstance.stop(t)
	if calls := fake.disableCalls(); len(calls) != 0 {
		t.Errorf("DisablePersonal calls = %+v, want none", calls)
	}
}

//go:build integration

package dispatch_test

// In-process end-to-end tests of the operator moderation path on real Postgres: an operator disable or
// delete of a personal destination (through the real personal.Service admin methods) dead-letters its
// pending and scheduled deliveries at send, with no request to the endpoint; plus the store's
// to_recipient column on the operator delivery list.

import (
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// submitScheduled submits a recipient message scheduled an hour out and returns it.
func (live *fanoutPipeline) submitScheduled(testInstance *testing.T, key string) message.StoredMessage {
	testInstance.Helper()
	deliverAt := time.Now().Add(time.Hour)
	envelope := live.envelope(key, fanoutAlice)
	envelope.DeliverAt = &deliverAt
	stored, outcome, submitErr := live.messages.Submit(live.ctx, envelope)
	if submitErr != nil || outcome.Duplicate || !outcome.Scheduled {
		testInstance.Fatalf("Submit(%s) = (outcome %+v, %v), want a new scheduled message", key, outcome, submitErr)
	}
	return stored
}

// requirePersonalState asserts the message's personal delivery is in the given state right now.
func (live *fanoutPipeline) requirePersonalState(testInstance *testing.T, messageID, wantState string) {
	testInstance.Helper()
	stored, getErr := live.store.GetMessage(live.ctx, messageID)
	if getErr != nil {
		testInstance.Fatalf("GetMessage: %v", getErr)
	}
	for _, delivery := range stored.Deliveries {
		if personal.IsPersonalDeliveryName(delivery.DestinationName) {
			if delivery.State != wantState {
				testInstance.Fatalf("message %s personal delivery state = %q, want %q", messageID, delivery.State, wantState)
			}
			return
		}
	}
	testInstance.Fatalf("message %s has no personal delivery", messageID)
}

// waitPersonalDead waits until the message's personal delivery is dead and returns it.
func (live *fanoutPipeline) waitPersonalDead(testInstance *testing.T, messageID string) message.Delivery {
	testInstance.Helper()
	deadline := time.Now().Add(pipelineWaitTimeout)
	var last message.Delivery
	for time.Now().Before(deadline) {
		stored, getErr := live.store.GetMessage(live.ctx, messageID)
		if getErr != nil {
			testInstance.Fatalf("GetMessage: %v", getErr)
		}
		for _, delivery := range stored.Deliveries {
			if personal.IsPersonalDeliveryName(delivery.DestinationName) {
				last = delivery
				if delivery.State == message.StateDead {
					return delivery
				}
			}
		}
		time.Sleep(pipelinePollInterval)
	}
	testInstance.Fatalf("timed out waiting for the personal delivery to die; last = %+v", last)
	return last
}

func TestPipelineOperatorModerationDeadLettersPendingAndScheduledPersonalDeliveries(testInstance *testing.T) {
	testCases := []struct {
		name       string
		moderate   func(*testing.T, *fanoutPipeline, int64)
		wantReason string
	}{
		{
			name: "operator disable",
			moderate: func(subTest *testing.T, live *fanoutPipeline, id int64) {
				view, disableErr := live.personalService.AdminSetEnabled(live.ctx, id, false, "abuse")
				if disableErr != nil || view.Enabled || view.DisabledBy == nil || *view.DisabledBy != personal.DisabledByOperator {
					subTest.Fatalf("AdminSetEnabled = (%+v, %v), want an operator-disabled view", view, disableErr)
				}
			},
			wantReason: message.DeadReasonDestinationDisabled,
		},
		{
			name: "operator delete",
			moderate: func(subTest *testing.T, live *fanoutPipeline, id int64) {
				if deleteErr := live.personalService.AdminDelete(live.ctx, id); deleteErr != nil {
					subTest.Fatalf("AdminDelete = %v", deleteErr)
				}
			},
			wantReason: message.DeadReasonDestinationDeleted,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			live := startFanoutPipeline(subTest)
			destination := live.register(subTest, fanoutAlice, fanoutApp, "/alice-mine", true)
			personalName := personal.PersonalDeliveryName(destination.ID)

			// One delivery stays scheduled; the other is a pending one waiting out a retry delay.
			scheduled := live.submitScheduled(subTest, "operator-scheduled")
			pending := live.submitScheduled(subTest, "operator-pending")
			// The worker's claim compares next_attempt_at with its own (shifted) clock, so the row waits
			// until the clock is advanced below.
			tag, execErr := live.pool.Exec(live.ctx,
				`UPDATE message_deliveries SET state = 'pending', next_attempt_at = now() + interval '1 hour'
				 WHERE message_id = $1::text::uuid AND destination_name = $2`, pending.ID, personalName)
			if execErr != nil || tag.RowsAffected() != 1 {
				subTest.Fatalf("make the delivery pending: rows %d, err %v", tag.RowsAffected(), execErr)
			}
			live.requirePersonalState(subTest, scheduled.ID, message.StateScheduled)
			live.requirePersonalState(subTest, pending.ID, message.StatePending)

			testCase.moderate(subTest, live, destination.ID)
			live.clock.Advance(2 * time.Hour)
			live.runReleaserOnce(subTest)

			for _, messageID := range []string{scheduled.ID, pending.ID} {
				dead := live.waitPersonalDead(subTest, messageID)
				if dead.DeadReason == nil || *dead.DeadReason != testCase.wantReason {
					subTest.Fatalf("message %s dead_reason = %v, want %q", messageID, dead.DeadReason, testCase.wantReason)
				}
			}
			time.Sleep(pipelineQuietPeriod)
			if paths := live.webhookPaths(); len(paths) != 0 {
				subTest.Fatalf("receiver saw %v, want no requests", paths)
			}
		})
	}
}

// TestAdminDeliveryListCarriesTheMessageRecipient proves the m.to_recipient column of the operator delivery
// query on real rows: a personal and a recipient delivery carry the person hash.
func TestAdminDeliveryListCarriesTheMessageRecipient(testInstance *testing.T) {
	live := startFanoutPipeline(testInstance)
	destination := live.register(testInstance, fanoutAlice, fanoutApp, "/alice-mine", true)
	if _, disableErr := live.personalService.AdminSetEnabled(live.ctx, destination.ID, false, ""); disableErr != nil {
		testInstance.Fatalf("AdminSetEnabled: %v", disableErr)
	}
	// The destination is disabled before submit, so build the personal delivery by hand: a disabled
	// destination is not a fan-out target.
	hashedEnvelope := live.envelope("operator-list-1", personhash.Hash(fanoutAlice))
	stored, duplicate, insertErr := live.store.InsertMessage(live.ctx, hashedEnvelope,
		[]message.DeliveryTarget{
			{DestinationName: registry.RecipientDestinationName, DestinationType: registry.TypeNtfy},
			{DestinationName: personal.PersonalDeliveryName(destination.ID), DestinationType: personal.TypeWebhook},
		})
	if insertErr != nil || duplicate {
		testInstance.Fatalf("InsertMessage = (duplicate %v, %v)", duplicate, insertErr)
	}

	wantPerson := personhash.Hash(fanoutAlice)
	deadline := time.Now().Add(pipelineWaitTimeout)
	seen := map[string]string{}
	for time.Now().Before(deadline) && len(seen) < 2 {
		for _, state := range []string{message.StateDead, message.StateDelivered} {
			listed, listErr := live.store.ListDeliveries(live.ctx, state, 50)
			if listErr != nil {
				testInstance.Fatalf("ListDeliveries(%s): %v", state, listErr)
			}
			for _, delivery := range listed {
				if delivery.MessageID == stored.ID {
					seen[delivery.DestinationName] = delivery.ToRecipient
				}
			}
		}
		time.Sleep(pipelinePollInterval)
	}
	for _, name := range []string{registry.RecipientDestinationName, personal.PersonalDeliveryName(destination.ID)} {
		if got, found := seen[name]; !found || got != wantPerson {
			testInstance.Fatalf("to_recipient for %s = (%q, found %v), want %q; seen = %v", name, got, found, wantPerson, seen)
		}
	}
}

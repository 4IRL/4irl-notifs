//go:build integration

package message_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

func heldEnvelopeFor(appID string, idempotencyKey string) message.Envelope {
	envelope := sampleEnvelope(idempotencyKey)
	envelope.AppID = appID
	envelope.Hold = true
	return envelope
}

func heldCaps(perApp int, total int) message.HeldCaps {
	return message.HeldCaps{PerApp: perApp, Total: total}
}

func TestInsertHeldMessageStoresHeldAtAndNoDeliveries(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)

	stored, duplicate, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(5, 10), scheduleBase)
	if err != nil || duplicate {
		testInstance.Fatalf("InsertHeldMessage = (duplicate %v, err %v), want a fresh hold", duplicate, err)
	}
	if stored.HeldAt == nil || !stored.HeldAt.Equal(scheduleBase) {
		testInstance.Fatalf("HeldAt = %v, want %v", stored.HeldAt, scheduleBase)
	}
	if len(stored.Deliveries) != 0 || countRows(ctx, testInstance, pool, "message_deliveries") != 0 {
		testInstance.Fatalf("deliveries = %+v, want none stored", stored.Deliveries)
	}
	if got := message.DeriveStatus(stored.Deliveries, stored.State()); got != message.StatusUnrouted {
		testInstance.Fatalf("status = %q, want %q", got, message.StatusUnrouted)
	}
}

func TestInsertHeldMessageSameKeyReplayIsADuplicateAndNeverGrowsTheCount(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	first, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-1"), heldCaps(1, 1), scheduleBase)
	if err != nil {
		testInstance.Fatalf("first hold: %v", err)
	}

	changed := heldEnvelopeFor("u4i", "held-1")
	changed.Channel = "other_channel"
	again, duplicate, err := store.InsertHeldMessage(ctx, changed, heldCaps(1, 1), scheduleBase.Add(time.Hour))
	if err != nil || !duplicate || again.ID != first.ID || again.Envelope.Channel != first.Envelope.Channel {
		testInstance.Fatalf("replay at both caps = id %q, duplicate %v, err %v; want the original, true, nil", again.ID, duplicate, err)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != 1 {
		testInstance.Fatalf("messages rows = %d, want 1", got)
	}
}

func TestHeldPerAppCapIsNeverExceededUnderConcurrentInserts(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	const capacity = 3
	const racers = capacity + 5

	errs := make([]error, racers)
	raceActions := make([]func(), 0, racers)
	for index := range racers {
		raceActions = append(raceActions, func() {
			_, _, errs[index] = store.InsertHeldMessage(
				ctx, heldEnvelopeFor("u4i", fmt.Sprintf("held-cap-%d", index)), heldCaps(capacity, roomyTotalCap), scheduleBase)
		})
	}
	raceAll(raceActions...)

	stored, refused := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			stored++
		case errors.Is(err, message.ErrTooManyHeld):
			refused++
		default:
			testInstance.Errorf("racer %d: unexpected error %v", index, err)
		}
	}
	if stored != capacity || refused != racers-capacity {
		testInstance.Errorf("stored %d, refused %d; want %d and %d", stored, refused, capacity, racers-capacity)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != capacity {
		testInstance.Errorf("messages rows = %d, want exactly the cap (%d)", got, capacity)
	}

	// Another app keeps its own allowance.
	if _, _, err := store.InsertHeldMessage(
		ctx, heldEnvelopeFor("other_app", "held-other"), heldCaps(capacity, roomyTotalCap), scheduleBase); err != nil {
		testInstance.Errorf("another app's hold at the first app's cap: %v", err)
	}
}

func TestHeldTotalCapIsNeverExceededUnderConcurrentInserts(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	const totalCap = 3
	const racers = totalCap + 5

	// Every racer is its own app, so only the total cap can stop them.
	errs := make([]error, racers)
	raceActions := make([]func(), 0, racers)
	for index := range racers {
		raceActions = append(raceActions, func() {
			_, _, errs[index] = store.InsertHeldMessage(
				ctx, heldEnvelopeFor(fmt.Sprintf("race_app_%d", index), "held-race"), heldCaps(100, totalCap), scheduleBase)
		})
	}
	raceAll(raceActions...)

	stored, refused := 0, 0
	for index, err := range errs {
		switch {
		case err == nil:
			stored++
		case errors.Is(err, message.ErrHeldCapacityReached):
			refused++
		default:
			testInstance.Errorf("racer %d: unexpected error %v", index, err)
		}
	}
	if stored != totalCap || refused != racers-totalCap {
		testInstance.Errorf("stored %d, refused %d; want %d and %d", stored, refused, totalCap, racers-totalCap)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != totalCap {
		testInstance.Errorf("messages rows = %d, want exactly the total cap (%d)", got, totalCap)
	}
}

func TestHeldPerAppCapIsReportedBeforeTheTotalCap(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)
	caps := heldCaps(1, 1)

	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("app_a", "order-1"), caps, scheduleBase); err != nil {
		testInstance.Fatalf("first hold: %v", err)
	}
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("app_a", "order-2"), caps, scheduleBase); !errors.Is(err, message.ErrTooManyHeld) {
		testInstance.Errorf("app at both caps err = %v, want ErrTooManyHeld", err)
	}
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("app_b", "order-3"), caps, scheduleBase); !errors.Is(err, message.ErrHeldCapacityReached) {
		testInstance.Errorf("other app at the total cap err = %v, want ErrHeldCapacityReached", err)
	}
}

func TestScheduledAndUnroutedMessageIsHeldAndCountsAgainstHeldCapsOnly(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	deliverAt := scheduleBase.Add(24 * time.Hour)

	envelope := heldEnvelopeFor("u4i", "held-future")
	envelope.DeliverAt = &deliverAt
	stored, _, err := store.InsertHeldMessage(ctx, envelope, heldCaps(2, 5), scheduleBase)
	if err != nil || stored.HeldAt == nil || stored.Envelope.DeliverAt == nil || !stored.Envelope.DeliverAt.Equal(deliverAt) {
		testInstance.Fatalf("held future message = %+v, err %v; want held with deliver_at kept", stored, err)
	}

	// The held message does not use the scheduled caps: a scheduled insert at a
	// cap of one still succeeds.
	if _, _, err := store.InsertScheduledMessage(
		ctx, scheduledEnvelope("scheduled-1", deliverAt), []message.DeliveryTarget{pager}, message.ScheduledCaps{PerApp: 1, Total: 1}); err != nil {
		testInstance.Errorf("scheduled insert beside a held message: %v", err)
	}
	// And a scheduled message does not use the held caps: a second hold fits
	// (the scheduled message is not counted) and only the third hits the cap of two.
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-2"), heldCaps(2, 5), scheduleBase); err != nil {
		testInstance.Errorf("second hold beside a scheduled message: %v", err)
	}
	if _, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor("u4i", "held-3"), heldCaps(2, 5), scheduleBase); !errors.Is(err, message.ErrTooManyHeld) {
		testInstance.Errorf("third hold err = %v, want ErrTooManyHeld", err)
	}
}

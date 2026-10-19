//go:build integration

package message_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// switchableResolver resolves every envelope to whatever destinations it was
// last set to, so a test can change what a re-submit resolves to.
type switchableResolver struct {
	mutex        sync.Mutex
	destinations []registry.ResolvedDestination
	calls        int
}

func (resolver *switchableResolver) set(targets ...message.DeliveryTarget) {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	resolver.destinations = resolver.destinations[:0]
	for _, target := range targets {
		resolver.destinations = append(resolver.destinations, registry.ResolvedDestination{
			ID: *target.DestinationID, Name: target.DestinationName, Type: target.DestinationType,
		})
	}
}

func (resolver *switchableResolver) Resolve(context.Context, string, string, string) ([]registry.ResolvedDestination, error) {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	resolver.calls++
	return slices.Clone(resolver.destinations), nil
}

func (resolver *switchableResolver) resolveCalls() int {
	resolver.mutex.Lock()
	defer resolver.mutex.Unlock()
	return resolver.calls
}

// alwaysPublisher reports every app as having an ntfy publisher token.
type alwaysPublisher struct{}

func (alwaysPublisher) HasPublisher(context.Context, string) (bool, error) { return true, nil }

// scheduledServiceFixture is a Service over the real store with a pinned clock,
// a small per-app cap and a wake counter.
type scheduledServiceFixture struct {
	ctx      context.Context
	service  *message.Service
	resolver *switchableResolver
	pool     *pgxpool.Pool
	pager    message.DeliveryTarget
	audit    message.DeliveryTarget
	extra    message.DeliveryTarget
	wakes    *atomic.Int32
}

const (
	scheduledServiceCap      = 3
	scheduledServiceTotalCap = 5
)

func newScheduledServiceFixture(testInstance *testing.T) scheduledServiceFixture {
	testInstance.Helper()
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	extra := seedTarget(ctx, testInstance, registryStore, "extra")
	resolver := &switchableResolver{}
	resolver.set(pager)
	var wakes atomic.Int32
	service := message.NewService(message.ServiceConfig{
		Store:              store,
		Resolver:           resolver,
		Publishers:         alwaysPublisher{},
		SupportedTypes:     map[string]bool{registry.TypeNtfy: true},
		Wake:               func() { wakes.Add(1) },
		Now:                func() time.Time { return scheduleBase },
		MaxScheduledPerApp: scheduledServiceCap,
		MaxScheduledTotal:  scheduledServiceTotalCap,
	})
	return scheduledServiceFixture{
		ctx: ctx, service: service, resolver: resolver, pool: pool,
		pager: pager, audit: audit, extra: extra, wakes: &wakes,
	}
}

// scheduledSubmit is a scheduled envelope for a destination-addressed message.
func scheduledSubmit(idempotencyKey string, deliverAt time.Time) message.Envelope {
	envelope := scheduledEnvelope(idempotencyKey, deliverAt)
	envelope.ToDestination = ""
	return envelope
}

func (fixture scheduledServiceFixture) mustSubmit(
	testInstance *testing.T, envelope message.Envelope,
) (message.StoredMessage, message.SubmitOutcome) {
	testInstance.Helper()
	stored, outcome, err := fixture.service.Submit(fixture.ctx, envelope)
	if err != nil {
		testInstance.Fatalf("Submit %s: %v", envelope.IdempotencyKey, err)
	}
	return stored, outcome
}

func (fixture scheduledServiceFixture) addressing(
	testInstance *testing.T, messageID string,
) (toDestination *string, toRecipient *string, nonNull int) {
	testInstance.Helper()
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT to_destination, to_recipient, num_nonnulls(to_destination, to_recipient) FROM messages WHERE id = $1::text::uuid`,
		messageID).Scan(&toDestination, &toRecipient, &nonNull); err != nil {
		testInstance.Fatalf("read addressing: %v", err)
	}
	return toDestination, toRecipient, nonNull
}

func TestServiceReplaceFromNToMTargetsLeavesExactlyM(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	fixture.resolver.set(fixture.pager, fixture.audit)
	deliverAt := scheduleBase.Add(time.Hour)

	first, outcome := fixture.mustSubmit(testInstance, scheduledSubmit("n-to-m", deliverAt))
	if outcome.Duplicate || outcome.Replaced || !outcome.Scheduled || len(first.Deliveries) != 2 {
		testInstance.Fatalf("first submit = outcome %+v with %d deliveries, want a new scheduled message with 2",
			outcome, len(first.Deliveries))
	}

	// N=2 down to M=1: a content change re-resolves to a single destination.
	fixture.resolver.set(fixture.extra)
	changed := scheduledSubmit("n-to-m", deliverAt)
	changed.Channel = "second"
	second, outcome := fixture.mustSubmit(testInstance, changed)
	if !outcome.Replaced || outcome.Duplicate || second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) {
		testInstance.Fatalf("second submit = %+v (id %s), want replaced with id %s kept", outcome, second.ID, first.ID)
	}
	if names := deliveryNames(second); !slices.Equal(names, []string{"extra"}) || !allDeliveriesIn(second, message.StateScheduled) {
		testInstance.Fatalf("deliveries after 2->1 = %+v, want exactly [extra] scheduled", second.Deliveries)
	}

	// M=1 up to 3.
	fixture.resolver.set(fixture.pager, fixture.audit, fixture.extra)
	changedAgain := scheduledSubmit("n-to-m", deliverAt)
	changedAgain.Channel = "third"
	third, outcome := fixture.mustSubmit(testInstance, changedAgain)
	if !outcome.Replaced || third.ID != first.ID {
		testInstance.Fatalf("third submit = %+v (id %s), want replaced with id %s kept", outcome, third.ID, first.ID)
	}
	if names := deliveryNames(third); !slices.Equal(names, []string{"audit", "extra", "pager"}) {
		testInstance.Fatalf("deliveries after 1->3 = %v, want [audit extra pager]", names)
	}
	if total := countRows(fixture.ctx, testInstance, fixture.pool, "message_deliveries"); total != 3 {
		testInstance.Fatalf("message_deliveries rows = %d, want 3 (old rows swapped out)", total)
	}
	if total := countRows(fixture.ctx, testInstance, fixture.pool, "messages"); total != 1 {
		testInstance.Fatalf("messages rows = %d, want 1", total)
	}
	requireDeliveryTimesMatchDeliverAt(fixture.ctx, testInstance, fixture.pool, first.ID)
	if fixture.wakes.Load() != 0 {
		testInstance.Fatalf("Wake calls = %d, want 0: nothing was ever due", fixture.wakes.Load())
	}
}

func TestServiceReplaceChangesChannelCriticalAndSwitchesAddressing(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	deliverAt := scheduleBase.Add(time.Hour)

	original, _ := fixture.mustSubmit(testInstance, scheduledSubmit("addressing", deliverAt))
	if destination, recipient, count := fixture.addressing(testInstance, original.ID); destination != nil || recipient != nil || count != 0 {
		// A route-mode submit names neither: both columns stay null.
		testInstance.Fatalf("route-mode addressing = (%v, %v, %d), want both null", destination, recipient, count)
	}

	// Switch to a named destination, a new channel and critical=false.
	toDestination := scheduledSubmit("addressing", deliverAt)
	toDestination.ToDestination = "pager"
	toDestination.Channel = "other_channel"
	toDestination.Critical = false
	stored, outcome := fixture.mustSubmit(testInstance, toDestination)
	if !outcome.Replaced || stored.ID != original.ID {
		testInstance.Fatalf("replace to destination = %+v (id %s), want replaced with id kept", outcome, stored.ID)
	}
	destination, recipient, count := fixture.addressing(testInstance, original.ID)
	if destination == nil || *destination != "pager" || recipient != nil || count != 1 {
		testInstance.Fatalf("addressing after route->destination = (%v, %v, %d), want (pager, nil, 1)", destination, recipient, count)
	}
	if stored.Envelope.Channel != "other_channel" || stored.Envelope.Critical {
		testInstance.Fatalf("stored envelope = %+v, want channel other_channel and critical=false", stored.Envelope)
	}

	// Switch to a recipient (an email, stored as its hash), critical again.
	toRecipient := scheduledSubmit("addressing", deliverAt)
	toRecipient.Channel = "other_channel"
	toRecipient.ToRecipient = "Alice@Example.com"
	toRecipient.Critical = true
	toRecipient.Display = &message.Display{Title: "Hi", Body: "There"}
	stored, outcome = fixture.mustSubmit(testInstance, toRecipient)
	if !outcome.Replaced || stored.ID != original.ID {
		testInstance.Fatalf("replace to recipient = %+v (id %s), want replaced with id kept", outcome, stored.ID)
	}
	hash := personhash.Hash("alice@example.com")
	destination, recipient, count = fixture.addressing(testInstance, original.ID)
	if destination != nil || recipient == nil || *recipient != hash || count != 1 {
		testInstance.Fatalf("addressing after destination->recipient = (%v, %v, %d), want (nil, hash, 1)", destination, recipient, count)
	}
	if len(stored.Deliveries) != 1 || stored.Deliveries[0].DestinationID != nil ||
		stored.Deliveries[0].DestinationName != registry.RecipientDestinationName || !stored.Envelope.Critical {
		testInstance.Fatalf("recipient deliveries = %+v (critical %v), want one @recipient delivery", stored.Deliveries, stored.Envelope.Critical)
	}

	// An identical retry of the recipient message is a duplicate even though the
	// submit names the email and the store holds the hash.
	callsBefore := fixture.resolver.resolveCalls()
	_, outcome = fixture.mustSubmit(testInstance, toRecipient)
	if !outcome.Duplicate || outcome.Replaced {
		testInstance.Fatalf("identical recipient retry = %+v, want a duplicate", outcome)
	}

	// And back to a destination.
	backToDestination := scheduledSubmit("addressing", deliverAt)
	backToDestination.ToDestination = "pager"
	stored, outcome = fixture.mustSubmit(testInstance, backToDestination)
	if !outcome.Replaced || stored.ID != original.ID {
		testInstance.Fatalf("replace back to destination = %+v, want replaced", outcome)
	}
	destination, recipient, count = fixture.addressing(testInstance, original.ID)
	if destination == nil || *destination != "pager" || recipient != nil || count != 1 {
		testInstance.Fatalf("addressing after recipient->destination = (%v, %v, %d), want (pager, nil, 1)", destination, recipient, count)
	}
	if names := deliveryNames(stored); !slices.Equal(names, []string{"pager"}) || stored.Deliveries[0].DestinationID == nil {
		testInstance.Fatalf("deliveries after recipient->destination = %+v, want one pager delivery with an id", stored.Deliveries)
	}
	if fixture.resolver.resolveCalls() != callsBefore+1 {
		testInstance.Fatalf("resolver calls = %d, want %d: only the changed submit re-resolves (a recipient never does)",
			fixture.resolver.resolveCalls(), callsBefore+1)
	}
}

func TestServiceEqualityMatchesTheStoredJSONBAndMicroseconds(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	deliverAt := scheduleBase.Add(time.Hour)
	original := scheduledSubmit("equal", deliverAt)
	original.Payload = json.RawMessage(`{"zeta":[1,2,{"x":true}],"alpha":"a b","id":9007199254740992}`)
	first, _ := fixture.mustSubmit(testInstance, original)
	callsBefore := fixture.resolver.resolveCalls()

	reorderedWithOffsetAndNanos := original
	reorderedWithOffsetAndNanos.Payload = json.RawMessage("{\n  \"id\" : 9007199254740992,\n \"alpha\":\"a b\",\"zeta\":[ 1, 2, {\"x\" : true} ] }")
	shifted := deliverAt.In(time.FixedZone("plus9", 9*3600)).Add(123 * time.Nanosecond)
	reorderedWithOffsetAndNanos.DeliverAt = &shifted
	stored, outcome := fixture.mustSubmit(testInstance, reorderedWithOffsetAndNanos)
	if !outcome.Duplicate || outcome.Replaced || stored.ID != first.ID {
		testInstance.Fatalf("equivalent retry = %+v, want a duplicate of the stored message", outcome)
	}
	if fixture.resolver.resolveCalls() != callsBefore {
		testInstance.Fatalf("an equivalent retry resolved targets")
	}

	// A payload that differs only in an integer beyond float64 precision changed.
	bigInteger := original
	bigInteger.Payload = json.RawMessage(`{"zeta":[1,2,{"x":true}],"alpha":"a b","id":9007199254740993}`)
	stored, outcome = fixture.mustSubmit(testInstance, bigInteger)
	if !outcome.Replaced || stored.ID != first.ID {
		testInstance.Fatalf("big-integer change = %+v, want replaced", outcome)
	}
	decoder := json.NewDecoder(bytes.NewReader(stored.Envelope.Payload))
	decoder.UseNumber()
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		testInstance.Fatalf("decode stored payload: %v", err)
	}
	if got := fmt.Sprint(decoded["id"]); got != "9007199254740993" {
		testInstance.Fatalf("stored id = %s, want 9007199254740993 (no float64 rounding)", got)
	}
}

func TestServiceReplaceToAnImmediateTimeStoresPendingAndWakesOnce(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	first, _ := fixture.mustSubmit(testInstance, scheduledSubmit("release-now", scheduleBase.Add(48*time.Hour)))

	immediate := scheduledSubmit("release-now", scheduleBase.Add(48*time.Hour))
	immediate.DeliverAt = nil
	immediate.Channel = "now"
	stored, outcome := fixture.mustSubmit(testInstance, immediate)
	if !outcome.Replaced || outcome.Scheduled || stored.ID != first.ID {
		testInstance.Fatalf("replace to immediate = %+v (id %s), want replaced, not scheduled, id kept", outcome, stored.ID)
	}
	if !allDeliveriesIn(stored, message.StatePending) {
		testInstance.Fatalf("deliveries = %+v, want all pending", stored.Deliveries)
	}
	if fixture.wakes.Load() != 1 {
		testInstance.Fatalf("Wake calls = %d, want exactly 1", fixture.wakes.Load())
	}
}

func TestServiceConcurrentSubmitsOfOneKeyNeverErrorAndStoreOneMessage(testInstance *testing.T) {
	testCases := []struct {
		name       string
		vary       bool
		deliverAt  func() *time.Time
		wantStatus string
	}{
		{
			name:       "identical scheduled content",
			deliverAt:  func() *time.Time { value := scheduleBase.Add(time.Hour); return &value },
			wantStatus: message.StatusScheduled,
		},
		{
			name:       "different scheduled content",
			vary:       true,
			deliverAt:  func() *time.Time { value := scheduleBase.Add(time.Hour); return &value },
			wantStatus: message.StatusScheduled,
		},
		{name: "immediate", deliverAt: func() *time.Time { return nil }, wantStatus: message.StatusPending},
	}
	const submitters = 8
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newScheduledServiceFixture(subTest)
			outcomes := make([]message.SubmitOutcome, submitters)
			errs := make([]error, submitters)
			actions := make([]func(), 0, submitters)
			for index := range submitters {
				actions = append(actions, func() {
					envelope := scheduledSubmit("concurrent", scheduleBase)
					envelope.DeliverAt = testCase.deliverAt()
					if testCase.vary {
						envelope.Channel = fmt.Sprintf("chan_%d", index)
					}
					_, outcomes[index], errs[index] = fixture.service.Submit(fixture.ctx, envelope)
				})
			}
			raceAll(actions...)

			created := 0
			for index, err := range errs {
				if err != nil {
					subTest.Fatalf("submitter %d error = %v, want none", index, err)
				}
				if !outcomes[index].Duplicate && !outcomes[index].Replaced {
					created++
				}
			}
			if created != 1 {
				subTest.Fatalf("%d submitters created the message, want exactly 1 (outcomes %+v)", created, outcomes)
			}
			if total := countRows(fixture.ctx, subTest, fixture.pool, "messages"); total != 1 {
				subTest.Fatalf("messages rows = %d, want 1", total)
			}
			stored, err := fixture.service.GetByKey(fixture.ctx, "u4i", "concurrent")
			if err != nil {
				subTest.Fatalf("GetByKey error = %v", err)
			}
			if got := message.DeriveStatus(stored.Deliveries); got != testCase.wantStatus || len(stored.Deliveries) != 1 {
				subTest.Fatalf("stored status = %q with %d deliveries, want %q with 1", got, len(stored.Deliveries), testCase.wantStatus)
			}
			if testCase.wantStatus == message.StatusScheduled && fixture.wakes.Load() != 0 {
				subTest.Fatalf("Wake calls = %d, want 0 for a scheduled message", fixture.wakes.Load())
			}
			// One insert, one wake: a submitter that loses the race stored nothing.
			if testCase.wantStatus == message.StatusPending && fixture.wakes.Load() != 1 {
				subTest.Fatalf("Wake calls = %d, want exactly 1 for %d concurrent immediate submits",
					fixture.wakes.Load(), submitters)
			}
		})
	}
}

func TestServiceCapRefusesOnlyNewScheduledSubmits(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	deliverAt := scheduleBase.Add(time.Hour)
	for index := range scheduledServiceCap {
		fixture.mustSubmit(testInstance, scheduledSubmit(fmt.Sprintf("cap-%d", index), deliverAt))
	}

	_, _, err := fixture.service.Submit(fixture.ctx, scheduledSubmit("cap-over", deliverAt))
	if !errors.Is(err, message.ErrTooManyScheduled) {
		testInstance.Fatalf("scheduled submit at the cap error = %v, want ErrTooManyScheduled", err)
	}
	if total := countRows(fixture.ctx, testInstance, fixture.pool, "messages"); total != scheduledServiceCap {
		testInstance.Fatalf("messages rows = %d, want %d (nothing stored over the cap)", total, scheduledServiceCap)
	}

	immediate := scheduledSubmit("cap-immediate", deliverAt)
	immediate.DeliverAt = nil
	if _, outcome := fixture.mustSubmit(testInstance, immediate); outcome.Scheduled || outcome.Duplicate {
		testInstance.Fatalf("immediate submit at the cap = %+v, want a new immediate message", outcome)
	}

	changed := scheduledSubmit("cap-0", deliverAt)
	changed.Channel = "changed"
	if _, outcome := fixture.mustSubmit(testInstance, changed); !outcome.Replaced {
		testInstance.Fatalf("replace at the cap = %+v, want replaced", outcome)
	}

	// A cancel frees a slot.
	if cancelled, cancelErr := fixture.service.CancelByKey(fixture.ctx, "u4i", "cap-1"); cancelErr != nil || !cancelled.Cancelled {
		testInstance.Fatalf("CancelByKey = (%+v, %v), want cancelled", cancelled, cancelErr)
	}
	if _, outcome := fixture.mustSubmit(testInstance, scheduledSubmit("cap-over", deliverAt)); !outcome.Scheduled || outcome.Duplicate {
		testInstance.Fatalf("submit after a cancel = %+v, want a new scheduled message", outcome)
	}
}

func TestServiceTotalCapRefusesNewScheduledSubmitsFromAnyApp(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	deliverAt := scheduleBase.Add(time.Hour)
	submitAs := func(appID string, key string) error {
		envelope := scheduledSubmit(key, deliverAt)
		envelope.AppID = appID
		_, _, err := fixture.service.Submit(fixture.ctx, envelope)
		return err
	}

	// Two apps together reach the total cap while each stays under its own.
	for index := range scheduledServiceCap {
		if err := submitAs("app_a", fmt.Sprintf("total-a-%d", index)); err != nil {
			testInstance.Fatalf("app_a submit %d: %v", index, err)
		}
	}
	for index := range scheduledServiceTotalCap - scheduledServiceCap {
		if err := submitAs("app_b", fmt.Sprintf("total-b-%d", index)); err != nil {
			testInstance.Fatalf("app_b submit %d: %v", index, err)
		}
	}

	if err := submitAs("app_c", "total-c"); !errors.Is(err, message.ErrScheduledCapacityReached) {
		testInstance.Fatalf("third app at the total cap error = %v, want ErrScheduledCapacityReached", err)
	}
	if err := submitAs("app_a", "total-a-over"); !errors.Is(err, message.ErrTooManyScheduled) {
		testInstance.Fatalf("app at both caps error = %v, want ErrTooManyScheduled", err)
	}
	if total := countRows(fixture.ctx, testInstance, fixture.pool, "messages"); total != scheduledServiceTotalCap {
		testInstance.Fatalf("messages rows = %d, want %d (nothing stored over the cap)", total, scheduledServiceTotalCap)
	}

	// An immediate submit and a replace are not new scheduled messages.
	immediate := scheduledSubmit("total-immediate", deliverAt)
	immediate.AppID = "app_c"
	immediate.DeliverAt = nil
	if _, outcome := fixture.mustSubmit(testInstance, immediate); outcome.Scheduled || outcome.Duplicate {
		testInstance.Fatalf("immediate submit at the total cap = %+v, want a new immediate message", outcome)
	}
	changed := scheduledSubmit("total-b-0", deliverAt)
	changed.AppID = "app_b"
	changed.Channel = "changed"
	if _, outcome := fixture.mustSubmit(testInstance, changed); !outcome.Replaced {
		testInstance.Fatalf("replace at the total cap = %+v, want replaced", outcome)
	}
}

func TestServiceCancelAndResubmitEndToEnd(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	deliverAt := scheduleBase.Add(time.Hour)
	first, _ := fixture.mustSubmit(testInstance, scheduledSubmit("cancel-flow", deliverAt))

	// Another app can neither cancel nor see it.
	foreign, err := fixture.service.Cancel(fixture.ctx, "other_app", first.ID)
	if err != nil || foreign.Found || foreign.Cancelled || foreign.Status != message.StatusNotFound || foreign.MessageID != first.ID {
		testInstance.Fatalf("foreign Cancel = (%+v, %v), want not_found echoing the id", foreign, err)
	}
	if got := mustGetStatus(testInstance, fixture, first.ID); got != message.StatusScheduled {
		testInstance.Fatalf("status after a foreign cancel = %q, want scheduled", got)
	}

	cancelled, err := fixture.service.Cancel(fixture.ctx, "u4i", first.ID)
	if err != nil || !cancelled.Cancelled || !cancelled.Transitioned ||
		cancelled.Status != message.StatusCancelled || cancelled.MessageID != first.ID {
		testInstance.Fatalf("Cancel = (%+v, %v), want cancelled by this call", cancelled, err)
	}
	again, err := fixture.service.CancelByKey(fixture.ctx, "u4i", "cancel-flow")
	if err != nil || !again.Cancelled || again.Transitioned || again.Status != message.StatusCancelled || again.MessageID != first.ID {
		testInstance.Fatalf("CancelByKey after a cancel = (%+v, %v), want cancelled (idempotent)", again, err)
	}
	adminAgain, err := fixture.service.AdminCancel(fixture.ctx, first.ID)
	if err != nil || !adminAgain.Cancelled {
		testInstance.Fatalf("AdminCancel after a cancel = (%+v, %v), want cancelled (idempotent)", adminAgain, err)
	}
	unknownKey, err := fixture.service.CancelByKey(fixture.ctx, "u4i", "no-such-key")
	if err != nil || unknownKey.Found || unknownKey.Status != message.StatusNotFound || unknownKey.MessageID != "" {
		testInstance.Fatalf("CancelByKey unknown = (%+v, %v), want not_found with an empty id", unknownKey, err)
	}

	// Re-posting a cancelled key is a duplicate with the stored (cancelled) message.
	changed := scheduledSubmit("cancel-flow", deliverAt)
	changed.Channel = "changed"
	stored, outcome := fixture.mustSubmit(testInstance, changed)
	if !outcome.Duplicate || outcome.Replaced || outcome.Scheduled || stored.ID != first.ID ||
		message.DeriveStatus(stored.Deliveries) != message.StatusCancelled || stored.CancelledAt == nil {
		testInstance.Fatalf("resubmit of a cancelled key = %+v (status %q), want a cancelled duplicate",
			outcome, message.DeriveStatus(stored.Deliveries))
	}
	if stored.Envelope.Channel != "alerts" {
		testInstance.Fatalf("stored channel = %q, want the original content", stored.Envelope.Channel)
	}
}

func TestServiceAdminCancelIsUnscopedAndReleasedMessagesReportTheirStatus(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	deliverAt := scheduleBase.Add(time.Hour)
	scheduled, _ := fixture.mustSubmit(testInstance, scheduledSubmit("admin-flow", deliverAt))

	cancelled, err := fixture.service.AdminCancel(fixture.ctx, scheduled.ID)
	if err != nil || !cancelled.Cancelled || cancelled.MessageID != scheduled.ID {
		testInstance.Fatalf("AdminCancel = (%+v, %v), want cancelled without naming an app", cancelled, err)
	}

	immediate := scheduledSubmit("released-flow", deliverAt)
	immediate.DeliverAt = nil
	released, _ := fixture.mustSubmit(testInstance, immediate)
	for name, cancel := range map[string]func() (message.CancelOutcome, error){
		"app": func() (message.CancelOutcome, error) { return fixture.service.Cancel(fixture.ctx, "u4i", released.ID) },
		"key": func() (message.CancelOutcome, error) {
			return fixture.service.CancelByKey(fixture.ctx, "u4i", "released-flow")
		},
		"admin": func() (message.CancelOutcome, error) { return fixture.service.AdminCancel(fixture.ctx, released.ID) },
	} {
		outcome, cancelErr := cancel()
		if cancelErr != nil || outcome.Cancelled || !outcome.Found || outcome.Status != message.StatusPending ||
			outcome.MessageID != released.ID {
			testInstance.Fatalf("%s cancel of a released message = (%+v, %v), want cancelled=false with status pending", name, outcome, cancelErr)
		}
	}
	if got := mustGetStatus(testInstance, fixture, released.ID); got != message.StatusPending {
		testInstance.Fatalf("released status after cancels = %q, want pending", got)
	}
}

func TestServiceListScheduledReportsTheTotalBeyondTheLimit(testInstance *testing.T) {
	fixture := newScheduledServiceFixture(testInstance)
	for index := range scheduledServiceCap {
		fixture.mustSubmit(testInstance, scheduledSubmit(fmt.Sprintf("list-%d", index),
			scheduleBase.Add(time.Duration(index+1)*time.Hour)))
	}

	listed, total, err := fixture.service.ListScheduled(fixture.ctx, 2)
	if err != nil || total != scheduledServiceCap || len(listed) != 2 {
		testInstance.Fatalf("ListScheduled(2) = (%d rows, total %d, %v), want 2 rows and total %d",
			len(listed), total, err, scheduledServiceCap)
	}
	if listed[0].IdempotencyKey != "list-0" || listed[1].IdempotencyKey != "list-1" {
		testInstance.Fatalf("order = [%s %s], want soonest first", listed[0].IdempotencyKey, listed[1].IdempotencyKey)
	}
}

func mustGetStatus(testInstance *testing.T, fixture scheduledServiceFixture, id string) string {
	testInstance.Helper()
	stored, err := fixture.service.Get(fixture.ctx, "u4i", id)
	if err != nil {
		testInstance.Fatalf("Get %s: %v", id, err)
	}
	return message.DeriveStatus(stored.Deliveries)
}

//go:build integration

package dispatch_test

// In-process end-to-end tests of hold -> route -> release -> delivery on real
// Postgres, using the pipeline harness with the message service's hold setting
// explicitly on (a hand-built ServiceConfig is off by default; only the
// DELIVERY_HOLD_UNROUTED env parse defaults to on).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const pipelineHeldChannel = "held_alerts"

// submitHeld submits an app-key (Hold) message on the unrouted held channel and
// requires that it was held.
func (live *pipeline) submitHeld(
	testInstance *testing.T, idempotencyKey string, deliverAt *time.Time,
) message.StoredMessage {
	testInstance.Helper()
	stored, outcome, submitErr := live.messages.Submit(live.ctx, message.Envelope{
		AppID:          pipelineAppID,
		Channel:        pipelineHeldChannel,
		IdempotencyKey: idempotencyKey,
		Hold:           true,
		DeliverAt:      deliverAt,
		Payload:        json.RawMessage(`{"order":42}`),
		Display:        &message.Display{Title: "Hello", Priority: "high"},
	})
	if submitErr != nil || outcome.Duplicate || !outcome.Held {
		testInstance.Fatalf("Submit = (outcome %+v, %v), want a new held message", outcome, submitErr)
	}
	return stored
}

// createHeldChannelRoute routes the held channel to the pipeline webhook.
func (live *pipeline) createHeldChannelRoute(testInstance *testing.T, destination string) {
	testInstance.Helper()
	if _, routeErr := live.registryService.CreateRoute(live.ctx, registry.RouteInput{
		AppPattern: "*", ChannelPattern: pipelineHeldChannel, Destinations: []string{destination},
	}); routeErr != nil {
		testInstance.Fatalf("create held channel route: %v", routeErr)
	}
}

// releaseHeldOnce runs the real held releaser through RunReleaser until its
// immediate first run has finished, then stops it, so the run's wake has
// happened when this returns.
func (live *pipeline) releaseHeldOnce(testInstance *testing.T) {
	testInstance.Helper()
	signaling := &callSignalingReleaser{
		inner:    dispatch.ReleaserFunc(live.messages.ReleaseHeld),
		finished: make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(live.ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatch.RunReleaser(ctx, dispatch.ReleaserConfig{
			Kind: "held", Releaser: signaling, Interval: time.Hour, BatchSize: dispatch.DefaultReleaseBatchSize,
			Now: live.clock.Now, Wake: live.worker.Wake, Logger: slog.New(slog.DiscardHandler),
		})
	}()
	select {
	case <-signaling.finished:
	case <-time.After(pipelineWaitTimeout):
		testInstance.Fatal("the held releaser did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(pipelineWaitTimeout):
		testInstance.Fatal("the held releaser did not stop")
	}
}

func (live *pipeline) getMessage(testInstance *testing.T, messageID string) message.StoredMessage {
	testInstance.Helper()
	stored, getErr := live.messages.Get(live.ctx, live.appID, messageID)
	if getErr != nil {
		testInstance.Fatalf("Get message: %v", getErr)
	}
	return stored
}

func TestPipelineDeliversAHeldMessageOnceARouteAppears(testInstance *testing.T) {
	live := startPipelineHolding(testInstance, func(int) int { return http.StatusOK }, false, true)
	held := live.submitHeld(testInstance, "held-1", nil)

	// No route yet: a release run leaves it held and nothing is sent.
	live.releaseHeldOnce(testInstance)
	if stored := live.getMessage(testInstance, held.ID); stored.HeldAt == nil || stored.ReleasedAt != nil {
		testInstance.Fatalf("before the route: HeldAt=%v ReleasedAt=%v, want held", stored.HeldAt, stored.ReleasedAt)
	}

	live.createHeldChannelRoute(testInstance, pipelineDestination)
	live.releaseHeldOnce(testInstance)
	live.waitForDelivery(testInstance, held.ID, "delivered after release", deliveredAfterAttempts(1))

	stored := live.getMessage(testInstance, held.ID)
	if stored.ReleasedAt == nil || stored.HeldAt != nil {
		testInstance.Fatalf("after release: HeldAt=%v ReleasedAt=%v, want released and not held", stored.HeldAt, stored.ReleasedAt)
	}
	if got := len(live.receiver.received()); got != 1 {
		testInstance.Fatalf("receiver saw %d requests, want exactly 1", got)
	}
}

func TestPipelineKeepsAHeldMessageHeldWhenTheRouteReachesOnlyADisabledDestination(testInstance *testing.T) {
	live := startPipelineHolding(testInstance, func(int) int { return http.StatusOK }, false, true)
	held := live.submitHeld(testInstance, "held-disabled-1", nil)

	disabled := false
	if _, updateErr := live.registryService.UpdateDestination(live.ctx, pipelineDestination, registry.DestinationPatch{
		Enabled: &disabled,
	}); updateErr != nil {
		testInstance.Fatalf("disable destination: %v", updateErr)
	}
	live.createHeldChannelRoute(testInstance, pipelineDestination)

	live.releaseHeldOnce(testInstance)
	stored := live.getMessage(testInstance, held.ID)
	if stored.HeldAt == nil || stored.ReleasedAt != nil || len(stored.Deliveries) != 0 {
		testInstance.Fatalf("HeldAt=%v ReleasedAt=%v deliveries=%+v, want still held with no deliveries",
			stored.HeldAt, stored.ReleasedAt, stored.Deliveries)
	}
	if got := len(live.receiver.received()); got != 0 {
		testInstance.Fatalf("receiver saw %d requests, want 0", got)
	}

	// Positive control: enabling the destination lets the same releaser release it, so the
	// held state above came from the disabled destination and not from an idle releaser.
	enabled := true
	if _, updateErr := live.registryService.UpdateDestination(live.ctx, pipelineDestination, registry.DestinationPatch{
		Enabled: &enabled,
	}); updateErr != nil {
		testInstance.Fatalf("enable destination: %v", updateErr)
	}
	live.releaseHeldOnce(testInstance)
	live.waitForDelivery(testInstance, held.ID, "delivered once enabled", deliveredAfterAttempts(1))
}

func TestPipelineReleasesHeldMessagesOldestFirst(testInstance *testing.T) {
	live := startPipelineHolding(testInstance, func(int) int { return http.StatusOK }, false, true)
	ids := make([]string, 0, 3)
	for _, key := range []string{"held-order-1", "held-order-2", "held-order-3"} {
		ids = append(ids, live.submitHeld(testInstance, key, nil).ID)
	}
	live.createHeldChannelRoute(testInstance, pipelineDestination)

	// Batch limit 1: each call releases exactly the oldest remaining message.
	for released := range ids {
		count, releaseErr := live.messages.ReleaseHeld(live.ctx, live.clock.Now(), 1)
		if releaseErr != nil || count != 1 {
			testInstance.Fatalf("ReleaseHeld #%d = (%d, %v), want (1, nil)", released+1, count, releaseErr)
		}
		for index, id := range ids {
			stored := live.getMessage(testInstance, id)
			wantReleased := index <= released
			if (stored.ReleasedAt != nil) != wantReleased {
				testInstance.Fatalf("after call %d: message %d released=%v, want %v", released+1, index+1, stored.ReleasedAt != nil, wantReleased)
			}
		}
	}
}

func TestPipelineSchedulesAReleasedHeldMessageForItsFutureDeliverAt(testInstance *testing.T) {
	live := startPipelineHolding(testInstance, func(int) int { return http.StatusOK }, false, true)
	deliverAt := time.Now().Add(time.Hour)
	held := live.submitHeld(testInstance, "held-future-1", &deliverAt)
	live.createHeldChannelRoute(testInstance, pipelineDestination)

	live.releaseHeldOnce(testInstance)
	live.waitForDelivery(testInstance, held.ID, "scheduled after release", stateIs(message.StateScheduled))
	time.Sleep(pipelineQuietPeriod)
	if got := len(live.receiver.received()); got != 0 {
		testInstance.Fatalf("receiver saw %d requests before deliver_at, want 0", got)
	}

	live.clock.Advance(2 * time.Hour)
	live.runReleaserOnce(testInstance)
	live.waitForDelivery(testInstance, held.ID, "delivered after deliver_at", deliveredAfterAttempts(1))
}

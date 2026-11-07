//go:build integration

package dispatch_test

// In-process end-to-end tests of personal deliveries on real Postgres: the real
// personal.Service and PGStore, message.PGStore and dispatch.Worker, and the real
// webhook sink talking to an httptest TLS receiver. The message is hand-built
// with a @personal_<id> delivery because nothing creates one yet (Step 6).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	personalPipelineApp    = "pipeapp"
	personalPipelineSecret = "personal-signing-secret-0123456789abcdef"
	personalPipelineEmail  = "carol@example.com"
)

type personalPipeline struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	store    *message.PGStore
	receiver *receiver
	person   string
	created  personal.Destination
}

// startPersonalPipeline wires the worker with a personal resolver and registers one
// enabled webhook personal destination opted in for personalPipelineApp.
func startPersonalPipeline(testInstance *testing.T, respond func(int) int) *personalPipeline {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pipelineTestTimeout)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	logger := slog.New(slog.DiscardHandler)

	sealer, sealErr := seal.New([]byte("0123456789abcdef0123456789abcdef"))
	if sealErr != nil {
		testInstance.Fatalf("seal.New: %v", sealErr)
	}
	registryService := registry.NewService(registry.NewPGStore(pool), sealer, logger)
	personalStore := personal.NewPGStore(pool, sealer)
	personalService := personal.NewService(personalStore, sealer, logger)
	store := message.NewPGStore(pool)
	webhook := newReceiver(testInstance, respond)

	worker := dispatch.NewWorker(dispatch.WorkerConfig{
		Queue:    store,
		Resolver: registryService,
		Personal: personalService,
		Sinks: map[string]sink.Sink{
			registry.TypeWebhook: sink.NewWebhookSink(transport.New(webhook.trustedConfig())),
		},
		Logger:       logger,
		Concurrency:  2,
		PollInterval: pipelineWorkerPoll,
		Random:       func() float64 { return 0 },
	})

	person := personhash.Hash(personalPipelineEmail)
	created, insertErr := personalStore.Insert(ctx, personal.NewDestination{
		PersonHash: person, RegisteredByAppID: personalPipelineApp, Label: "Phone", Type: personal.TypeWebhook,
		Secrets: map[string]string{"url": webhook.server.URL + "/personal", "signing_secret": personalPipelineSecret},
		OptIn:   true,
	})
	if insertErr != nil {
		testInstance.Fatalf("insert personal destination: %v", insertErr)
	}

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(workerCtx)
	}()
	testInstance.Cleanup(func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(pipelineWaitTimeout):
			testInstance.Error("worker did not stop")
		}
	})
	return &personalPipeline{
		ctx: ctx, pool: pool, store: store, receiver: webhook, person: person, created: created,
	}
}

// insertPersonalMessage stores a message with one @personal_<id> delivery, as Step 6's
// fan-out will.
func (live *personalPipeline) insertPersonalMessage(testInstance *testing.T, key string) message.StoredMessage {
	testInstance.Helper()
	stored, duplicate, insertErr := live.store.InsertMessage(live.ctx, message.Envelope{
		AppID:          personalPipelineApp,
		Channel:        pipelineChannel,
		IdempotencyKey: key,
		ToRecipient:    live.person,
		Payload:        json.RawMessage(`{"order":42}`),
		Display:        &message.Display{Title: "Hello"},
	}, []message.DeliveryTarget{{
		DestinationName: personal.PersonalDeliveryName(live.created.ID),
		DestinationType: personal.TypeWebhook,
	}})
	if insertErr != nil || duplicate {
		testInstance.Fatalf("InsertMessage = (duplicate %v, %v), want a new message", duplicate, insertErr)
	}
	return stored
}

func (live *personalPipeline) waitForState(
	testInstance *testing.T, messageID string, condition func(message.Delivery) bool,
) message.Delivery {
	testInstance.Helper()
	deadline := time.Now().Add(pipelineWaitTimeout)
	var last message.Delivery
	for time.Now().Before(deadline) {
		stored, getErr := live.store.GetMessage(live.ctx, messageID)
		if getErr != nil {
			testInstance.Fatalf("GetMessage: %v", getErr)
		}
		if len(stored.Deliveries) == 1 {
			last = stored.Deliveries[0]
			if condition(last) {
				return last
			}
		}
		time.Sleep(pipelinePollInterval)
	}
	testInstance.Fatalf("timed out waiting for the delivery state; last = %+v", last)
	return last
}

func (live *personalPipeline) exec(testInstance *testing.T, statement string, args ...any) {
	testInstance.Helper()
	if _, execErr := live.pool.Exec(live.ctx, statement, args...); execErr != nil {
		testInstance.Fatalf("exec %q: %v", statement, execErr)
	}
}

func (live *personalPipeline) requireDeadWithoutRequest(
	testInstance *testing.T, delivery message.Delivery, wantReason string,
) {
	testInstance.Helper()
	if delivery.DeadReason == nil || *delivery.DeadReason != wantReason {
		testInstance.Fatalf("dead_reason = %v, want %q", delivery.DeadReason, wantReason)
	}
	time.Sleep(pipelineQuietPeriod)
	if requests := live.receiver.received(); len(requests) != 0 {
		testInstance.Fatalf("receiver saw %d requests, want 0", len(requests))
	}
	attempts, listErr := live.store.ListAttempts(live.ctx, delivery.ID)
	if listErr != nil {
		testInstance.Fatalf("ListAttempts: %v", listErr)
	}
	if len(attempts) != 1 || attempts[0].Error == nil || *attempts[0].Error == "" {
		testInstance.Fatalf("attempts = %+v, want one attempt with a non-empty error text", attempts)
	}
}

func TestPipelineDeliversAPersonalWebhookSigned(testInstance *testing.T) {
	live := startPersonalPipeline(testInstance, func(int) int { return http.StatusOK })
	stored := live.insertPersonalMessage(testInstance, "personal-1")

	live.waitForState(testInstance, stored.ID, stateIs(message.StateDelivered))

	requests := live.receiver.received()
	if len(requests) != 1 {
		testInstance.Fatalf("receiver saw %d requests, want 1", len(requests))
	}
	request := requests[0]
	if request.method != http.MethodPost || request.path != "/personal" {
		testInstance.Fatalf("request = %s %s, want POST /personal", request.method, request.path)
	}
	timestamp := request.header.Get("X-Delivery-Timestamp")
	if _, parseErr := strconv.ParseInt(timestamp, 10, 64); parseErr != nil {
		testInstance.Fatalf("X-Delivery-Timestamp = %q, want unix seconds", timestamp)
	}
	if got, want := request.header.Get("X-Delivery-Signature"),
		expectedSignature(personalPipelineSecret, timestamp, request.body); got != want {
		testInstance.Fatalf("X-Delivery-Signature = %q, want %q (signed with the personal destination's own secret)", got, want)
	}
}

func TestPipelineDeadLettersAPersonalDeliveryOptedOut(testInstance *testing.T) {
	live := startPersonalPipeline(testInstance, func(int) int { return http.StatusOK })
	live.exec(testInstance, `DELETE FROM personal_destination_opt_ins WHERE personal_destination_id = $1`, live.created.ID)
	stored := live.insertPersonalMessage(testInstance, "personal-optout")

	delivery := live.waitForState(testInstance, stored.ID, stateIs(message.StateDead))
	live.requireDeadWithoutRequest(testInstance, delivery, message.DeadReasonOptedOut)
}

func TestPipelineDeadLettersADisabledPersonalDelivery(testInstance *testing.T) {
	live := startPersonalPipeline(testInstance, func(int) int { return http.StatusOK })
	live.exec(testInstance,
		`UPDATE personal_destinations SET enabled = false, disabled_by = 'operator' WHERE id = $1`, live.created.ID)
	stored := live.insertPersonalMessage(testInstance, "personal-disabled")

	delivery := live.waitForState(testInstance, stored.ID, stateIs(message.StateDead))
	live.requireDeadWithoutRequest(testInstance, delivery, message.DeadReasonDestinationDisabled)
}

func TestPipelineDeadLettersADeletedPersonalDelivery(testInstance *testing.T) {
	live := startPersonalPipeline(testInstance, func(int) int { return http.StatusOK })
	live.exec(testInstance, `DELETE FROM personal_destinations WHERE id = $1`, live.created.ID)
	stored := live.insertPersonalMessage(testInstance, "personal-deleted")

	delivery := live.waitForState(testInstance, stored.ID, stateIs(message.StateDead))
	live.requireDeadWithoutRequest(testInstance, delivery, message.DeadReasonDestinationDeleted)
}

func TestPipelineDeliversWhenOnlyTheRegistrarWasRemoved(testInstance *testing.T) {
	live := startPersonalPipeline(testInstance, func(int) int { return http.StatusOK })
	live.exec(testInstance,
		`UPDATE personal_destinations SET registrar_removed_at = now() WHERE id = $1`, live.created.ID)
	stored := live.insertPersonalMessage(testInstance, "personal-registrar-removed")

	live.waitForState(testInstance, stored.ID, stateIs(message.StateDelivered))
	if requests := live.receiver.received(); len(requests) != 1 {
		testInstance.Fatalf("receiver saw %d requests, want 1", len(requests))
	}
}

func TestPipelineGoneDisablesOnlyThePersonalDestination(testInstance *testing.T) {
	live := startPersonalPipeline(testInstance, func(int) int { return http.StatusGone })
	stored := live.insertPersonalMessage(testInstance, "personal-gone")

	delivery := live.waitForState(testInstance, stored.ID, stateIs(message.StateDead))
	if delivery.DeadReason == nil || *delivery.DeadReason != message.DeadReasonGone {
		testInstance.Fatalf("dead_reason = %v, want %q", delivery.DeadReason, message.DeadReasonGone)
	}
	var enabled bool
	var disabledBy, disabledReason *string
	deadline := time.Now().Add(pipelineWaitTimeout)
	for time.Now().Before(deadline) {
		if scanErr := live.pool.QueryRow(live.ctx,
			`SELECT enabled, disabled_by, disabled_reason FROM personal_destinations WHERE id = $1`,
			live.created.ID).Scan(&enabled, &disabledBy, &disabledReason); scanErr != nil {
			testInstance.Fatalf("read personal destination: %v", scanErr)
		}
		if !enabled {
			break
		}
		time.Sleep(pipelinePollInterval)
	}
	if enabled || disabledBy == nil || *disabledBy != "system" ||
		disabledReason == nil || *disabledReason != "Endpoint returned 410 Gone." {
		testInstance.Fatalf("personal destination = (enabled %v, by %v, reason %v), want system-disabled with the 410 reason",
			enabled, disabledBy, disabledReason)
	}
}

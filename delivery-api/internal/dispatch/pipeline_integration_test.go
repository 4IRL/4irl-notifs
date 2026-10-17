//go:build integration

package dispatch_test

// In-process end-to-end tests of the delivery pipeline on real Postgres: the real
// registry.Service (with sealed secrets), message.Service and PGStore, the real
// dispatch.Worker and the real sink.WebhookSink over transport.New, talking to an
// httptest TLS receiver. Only the clock, the jitter source and (except in the
// guard test) the address policy are swapped, so every state transition in the
// plan is exercised for real rather than through fakes.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	pipelineAppID           = "u4i"
	pipelineChannel         = "alerts"
	pipelineDestination     = "pipeline_hook"
	pipelineSigningSecret   = "pipeline-signing-secret-0123456789abcdef"
	pipelineTestTimeout     = 60 * time.Second
	pipelineWaitTimeout     = 15 * time.Second
	pipelinePollInterval    = 25 * time.Millisecond
	pipelineWorkerPoll      = 20 * time.Millisecond
	pipelineQuietPeriod     = 300 * time.Millisecond
	pipelineClockAdvance    = time.Minute
	pipelineGoneDisableText = "destination reported gone (410 or equivalent)"
)

// shiftedClock is the worker's clock: real time plus an offset a test advances to
// make a backed-off retry due without waiting.
type shiftedClock struct {
	offset atomic.Int64
}

func (clock *shiftedClock) Now() time.Time {
	return time.Now().Add(time.Duration(clock.offset.Load()))
}

func (clock *shiftedClock) Advance(duration time.Duration) {
	clock.offset.Add(int64(duration))
}

// receivedRequest is one request the receiver saw.
type receivedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

// receiver is a TLS webhook endpoint whose response status is chosen per request
// number (1-based) by respond.
type receiver struct {
	server   *httptest.Server
	mutex    sync.Mutex
	requests []receivedRequest
	respond  func(requestNumber int) int
}

func newReceiver(testInstance *testing.T, respond func(requestNumber int) int) *receiver {
	testInstance.Helper()
	webhook := &receiver{respond: respond}
	webhook.server = httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			testInstance.Errorf("receiver: reading request body: %v", readErr)
		}
		webhook.mutex.Lock()
		webhook.requests = append(webhook.requests, receivedRequest{
			method: request.Method, path: request.URL.Path, header: request.Header.Clone(), body: body,
		})
		requestNumber := len(webhook.requests)
		respond := webhook.respond
		webhook.mutex.Unlock()
		responseWriter.WriteHeader(respond(requestNumber))
	}))
	testInstance.Cleanup(webhook.server.Close)
	return webhook
}

func (webhook *receiver) received() []receivedRequest {
	webhook.mutex.Lock()
	defer webhook.mutex.Unlock()
	return append([]receivedRequest(nil), webhook.requests...)
}

// trustedConfig returns a transport config that trusts the receiver's certificate
// and allows its loopback address. Only tests may build one.
func (webhook *receiver) trustedConfig() transport.Config {
	roots := x509.NewCertPool()
	roots.AddCert(webhook.server.Certificate())
	return transport.Config{
		AllowAddr: func(netip.Addr) bool { return true },
		TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}
}

// pipeline is a running worker over an isolated, fully migrated schema with one
// webhook destination and a route to it.
type pipeline struct {
	ctx             context.Context
	registryService *registry.Service
	messages        *message.Service
	receiver        *receiver
	worker          *dispatch.Worker
	clock           *shiftedClock
}

// startPipeline wires the real stack and starts the worker. guarded true uses the
// production transport (zero Config), so the SSRF guard applies to the receiver's
// loopback address; false uses receiver.trustedConfig.
func startPipeline(testInstance *testing.T, respond func(requestNumber int) int, guarded bool) *pipeline {
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
	store := message.NewPGStore(pool)
	webhook := newReceiver(testInstance, respond)

	transportConfig := webhook.trustedConfig()
	if guarded {
		transportConfig = transport.Config{}
	}
	clock := &shiftedClock{}
	worker := dispatch.NewWorker(dispatch.WorkerConfig{
		Queue:        store,
		Resolver:     registryService,
		Sinks:        map[string]sink.Sink{registry.TypeWebhook: sink.NewWebhookSink(transport.New(transportConfig))},
		Logger:       logger,
		Concurrency:  2,
		PollInterval: pipelineWorkerPoll,
		Now:          clock.Now,
		Random:       func() float64 { return 0 },
	})
	messageService := message.NewService(message.ServiceConfig{
		Store:          store,
		Resolver:       registryService,
		SupportedTypes: map[string]bool{registry.TypeWebhook: true},
		Wake:           worker.Wake,
		Logger:         logger,
	})

	if _, createErr := registryService.CreateDestination(ctx, registry.DestinationInput{
		Name: pipelineDestination,
		Type: registry.TypeWebhook,
		Secrets: map[string]string{
			"url":            webhook.server.URL + "/hook",
			"signing_secret": pipelineSigningSecret,
		},
	}); createErr != nil {
		testInstance.Fatalf("create destination: %v", createErr)
	}
	if _, routeErr := registryService.CreateRoute(ctx, registry.RouteInput{
		AppPattern: "*", ChannelPattern: pipelineChannel, Destinations: []string{pipelineDestination},
	}); routeErr != nil {
		testInstance.Fatalf("create route: %v", routeErr)
	}

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(workerCtx)
	}()
	// Registered after the pool's cleanup, so (LIFO) the worker stops before the pool closes.
	testInstance.Cleanup(func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(pipelineWaitTimeout):
			testInstance.Error("worker did not stop")
		}
	})

	return &pipeline{
		ctx: ctx, registryService: registryService, messages: messageService,
		receiver: webhook, worker: worker, clock: clock,
	}
}

// submit queues one message through the real service.
func (live *pipeline) submit(testInstance *testing.T, idempotencyKey string) message.StoredMessage {
	testInstance.Helper()
	stored, duplicate, submitErr := live.messages.Submit(live.ctx, message.Envelope{
		AppID:          pipelineAppID,
		Channel:        pipelineChannel,
		IdempotencyKey: idempotencyKey,
		Critical:       true,
		Payload:        json.RawMessage(`{"order":42}`),
		Display:        &message.Display{Title: "Hello", Priority: "high"},
	})
	if submitErr != nil || duplicate {
		testInstance.Fatalf("Submit = (duplicate %v, %v), want a new message", duplicate, submitErr)
	}
	if len(stored.Deliveries) != 1 {
		testInstance.Fatalf("Submit queued %d deliveries, want 1", len(stored.Deliveries))
	}
	return stored
}

// waitForDelivery polls the message until its single delivery satisfies condition
// and returns that delivery.
func (live *pipeline) waitForDelivery(
	testInstance *testing.T, messageID, description string, condition func(message.Delivery) bool,
) message.Delivery {
	testInstance.Helper()
	deadline := time.Now().Add(pipelineWaitTimeout)
	var last message.Delivery
	for time.Now().Before(deadline) {
		stored, getErr := live.messages.Get(live.ctx, pipelineAppID, messageID)
		if getErr != nil {
			testInstance.Fatalf("Get message: %v", getErr)
		}
		if len(stored.Deliveries) == 1 {
			last = stored.Deliveries[0]
			if condition(last) {
				return last
			}
		}
		time.Sleep(pipelinePollInterval)
	}
	testInstance.Fatalf("timed out waiting for %s; last delivery = %+v", description, last)
	return last
}

func stateIs(state string) func(message.Delivery) bool {
	return func(delivery message.Delivery) bool { return delivery.State == state }
}

func pendingAfterAttempts(count int) func(message.Delivery) bool {
	return func(delivery message.Delivery) bool {
		return delivery.State == message.StatePending && delivery.AttemptCount == count
	}
}

func deliveredAfterAttempts(count int) func(message.Delivery) bool {
	return func(delivery message.Delivery) bool {
		return delivery.State == message.StateDelivered && delivery.AttemptCount == count
	}
}

func deadAfterAttempts(count int) func(message.Delivery) bool {
	return func(delivery message.Delivery) bool {
		return delivery.State == message.StateDead && delivery.AttemptCount == count
	}
}

func (live *pipeline) attempts(testInstance *testing.T, deliveryID int64) []message.Attempt {
	testInstance.Helper()
	attempts, listErr := live.messages.ListAttempts(live.ctx, deliveryID)
	if listErr != nil {
		testInstance.Fatalf("ListAttempts: %v", listErr)
	}
	return attempts
}

// requireAttemptOutcomes checks the delivery log's outcomes and status codes in
// order, and that attempt numbers are 1..n.
func requireAttemptOutcomes(testInstance *testing.T, attempts []message.Attempt, wantOutcomes []string, wantStatuses []int) {
	testInstance.Helper()
	if len(attempts) != len(wantOutcomes) {
		testInstance.Fatalf("attempts = %d, want %d", len(attempts), len(wantOutcomes))
	}
	for index, attempt := range attempts {
		if attempt.AttemptNumber != index+1 {
			testInstance.Fatalf("attempt %d number = %d, want %d", index, attempt.AttemptNumber, index+1)
		}
		if attempt.Outcome != wantOutcomes[index] {
			testInstance.Fatalf("attempt %d outcome = %q, want %q", index, attempt.Outcome, wantOutcomes[index])
		}
		if attempt.StatusCode == nil || *attempt.StatusCode != wantStatuses[index] {
			testInstance.Fatalf("attempt %d status = %v, want %d", index, attempt.StatusCode, wantStatuses[index])
		}
	}
}

// expectedSignature recomputes the webhook signature the receiver must see.
func expectedSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestPipelineDeliversASignedWebhook(testInstance *testing.T) {
	live := startPipeline(testInstance, func(int) int { return http.StatusOK }, false)
	stored := live.submit(testInstance, "deliver-1")

	delivery := live.waitForDelivery(testInstance, stored.ID, "delivered", deliveredAfterAttempts(1))

	requests := live.receiver.received()
	if len(requests) != 1 {
		testInstance.Fatalf("receiver saw %d requests, want 1", len(requests))
	}
	request := requests[0]
	if request.method != http.MethodPost || request.path != "/hook" {
		testInstance.Fatalf("request = %s %s, want POST /hook", request.method, request.path)
	}
	wantHeaders := map[string]string{
		"Content-Type":               "application/json",
		"User-Agent":                 "4irl-delivery/1",
		"X-Delivery-Message-Id":      stored.ID,
		"X-Delivery-Idempotency-Key": "deliver-1",
	}
	for name, want := range wantHeaders {
		if got := request.header.Get(name); got != want {
			testInstance.Fatalf("header %s = %q, want %q", name, got, want)
		}
	}
	timestamp := request.header.Get("X-Delivery-Timestamp")
	if _, parseErr := strconv.ParseInt(timestamp, 10, 64); parseErr != nil {
		testInstance.Fatalf("X-Delivery-Timestamp = %q, want unix seconds", timestamp)
	}
	if got, want := request.header.Get("X-Delivery-Signature"), expectedSignature(pipelineSigningSecret, timestamp, request.body); got != want {
		testInstance.Fatalf("X-Delivery-Signature = %q, want %q (HMAC over timestamp.body with the destination secret)", got, want)
	}

	var body struct {
		MessageID      string          `json:"message_id"`
		AppID          string          `json:"app_id"`
		Channel        string          `json:"channel"`
		IdempotencyKey string          `json:"idempotency_key"`
		Critical       bool            `json:"critical"`
		Payload        json.RawMessage `json:"payload"`
		Display        message.Display `json:"display"`
	}
	if decodeErr := json.Unmarshal(request.body, &body); decodeErr != nil {
		testInstance.Fatalf("decoding webhook body %q: %v", request.body, decodeErr)
	}
	if body.MessageID != stored.ID || body.AppID != pipelineAppID || body.Channel != pipelineChannel ||
		body.IdempotencyKey != "deliver-1" || !body.Critical || string(body.Payload) != `{"order":42}` ||
		body.Display.Title != "Hello" || body.Display.Priority != "high" {
		testInstance.Fatalf("webhook body = %+v, want the submitted envelope", body)
	}

	requireAttemptOutcomes(testInstance, live.attempts(testInstance, delivery.ID),
		[]string{message.OutcomeSuccess}, []int{http.StatusOK})
}

func TestPipelineRetriesAfterA503ThenDelivers(testInstance *testing.T) {
	live := startPipeline(testInstance, func(requestNumber int) int {
		if requestNumber == 1 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}, false)
	stored := live.submit(testInstance, "retry-1")

	pending := live.waitForDelivery(testInstance, stored.ID, "one failed attempt", pendingAfterAttempts(1))
	if pending.LastStatusCode == nil || *pending.LastStatusCode != http.StatusServiceUnavailable {
		testInstance.Fatalf("last status after the first attempt = %v, want 503", pending.LastStatusCode)
	}
	if !pending.NextAttemptAt.After(live.clock.Now()) {
		testInstance.Fatalf("next_attempt_at = %s, want it in the future (backoff)", pending.NextAttemptAt)
	}

	// The retry is backed off, so it must not be sent before the clock passes next_attempt_at.
	time.Sleep(pipelineQuietPeriod)
	if got := len(live.receiver.received()); got != 1 {
		testInstance.Fatalf("receiver saw %d requests before the backoff elapsed, want 1", got)
	}

	live.clock.Advance(pipelineClockAdvance)
	live.worker.Wake()
	delivery := live.waitForDelivery(testInstance, stored.ID, "delivered after the retry", deliveredAfterAttempts(2))

	requests := live.receiver.received()
	if len(requests) != 2 {
		testInstance.Fatalf("receiver saw %d requests, want 2", len(requests))
	}
	for index, request := range requests {
		if got := request.header.Get("X-Delivery-Message-Id"); got != stored.ID {
			testInstance.Fatalf("request %d message id = %q, want %q", index, got, stored.ID)
		}
		if got := request.header.Get("X-Delivery-Idempotency-Key"); got != "retry-1" {
			testInstance.Fatalf("request %d idempotency key = %q, want retry-1 (receivers dedupe on it)", index, got)
		}
	}
	requireAttemptOutcomes(testInstance, live.attempts(testInstance, delivery.ID),
		[]string{message.OutcomeRetryable, message.OutcomeSuccess}, []int{http.StatusServiceUnavailable, http.StatusOK})
}

func TestPipelineGoneDeadLettersAndDisablesTheDestination(testInstance *testing.T) {
	live := startPipeline(testInstance, func(int) int { return http.StatusGone }, false)
	stored := live.submit(testInstance, "gone-1")

	delivery := live.waitForDelivery(testInstance, stored.ID, "dead after a 410", deadAfterAttempts(1))
	if delivery.DeadReason == nil || *delivery.DeadReason != message.DeadReasonGone {
		testInstance.Fatalf("dead_reason = %v, want %q", delivery.DeadReason, message.DeadReasonGone)
	}

	// The disable runs just after the attempt is recorded, so poll for it.
	deadline := time.Now().Add(pipelineWaitTimeout)
	for {
		view, getErr := live.registryService.GetDestination(live.ctx, pipelineDestination)
		if getErr != nil {
			testInstance.Fatalf("GetDestination: %v", getErr)
		}
		if !view.Enabled {
			if view.DisabledReason == nil || *view.DisabledReason != pipelineGoneDisableText {
				testInstance.Fatalf("disabled_reason = %v, want %q", view.DisabledReason, pipelineGoneDisableText)
			}
			break
		}
		if time.Now().After(deadline) {
			testInstance.Fatal("destination was not disabled after a 410")
		}
		time.Sleep(pipelinePollInterval)
	}
	requireAttemptOutcomes(testInstance, live.attempts(testInstance, delivery.ID),
		[]string{message.OutcomeGone}, []int{http.StatusGone})
}

func TestPipelineReplayOfADeadDeliveryGoesBackToTheReceiver(testInstance *testing.T) {
	live := startPipeline(testInstance, func(requestNumber int) int {
		if requestNumber == 1 {
			return http.StatusBadRequest
		}
		return http.StatusOK
	}, false)
	stored := live.submit(testInstance, "replay-1")

	dead := live.waitForDelivery(testInstance, stored.ID, "dead after a 400", deadAfterAttempts(1))
	if dead.DeadReason == nil || *dead.DeadReason != message.DeadReasonTerminalStatus {
		testInstance.Fatalf("dead_reason = %v, want %q", dead.DeadReason, message.DeadReasonTerminalStatus)
	}
	if got := len(live.receiver.received()); got != 1 {
		testInstance.Fatalf("receiver saw %d requests for a terminal failure, want 1 (no retry)", got)
	}

	replayed, replayErr := live.messages.Replay(live.ctx, dead.ID)
	if replayErr != nil {
		testInstance.Fatalf("Replay: %v", replayErr)
	}
	if replayed.State != message.StatePending || replayed.AttemptCount != 1 {
		testInstance.Fatalf("replayed = %+v, want pending with the cumulative attempt count kept", replayed)
	}

	delivery := live.waitForDelivery(testInstance, stored.ID, "delivered after the replay", deliveredAfterAttempts(2))
	if got := len(live.receiver.received()); got != 2 {
		testInstance.Fatalf("receiver saw %d requests, want 2 (original + replay)", got)
	}
	requireAttemptOutcomes(testInstance, live.attempts(testInstance, delivery.ID),
		[]string{message.OutcomeTerminal, message.OutcomeSuccess}, []int{http.StatusBadRequest, http.StatusOK})
}

// TestPipelineDefaultTransportBlocksALoopbackReceiver runs the production
// transport (no test knobs) against a receiver on loopback: the SSRF guard must
// refuse the dial, the delivery must dead-letter as blocked_address, and the
// receiver must never see a request.
func TestPipelineDefaultTransportBlocksALoopbackReceiver(testInstance *testing.T) {
	live := startPipeline(testInstance, func(int) int { return http.StatusOK }, true)
	stored := live.submit(testInstance, "blocked-1")

	delivery := live.waitForDelivery(testInstance, stored.ID, "dead after the guard refused", deadAfterAttempts(1))
	if delivery.DeadReason == nil || *delivery.DeadReason != message.DeadReasonBlockedAddress {
		testInstance.Fatalf("dead_reason = %v, want %q", delivery.DeadReason, message.DeadReasonBlockedAddress)
	}
	if got := len(live.receiver.received()); got != 0 {
		testInstance.Fatalf("receiver saw %d requests, want 0 (the guard must refuse before any bytes are sent)", got)
	}

	attempts := live.attempts(testInstance, delivery.ID)
	if len(attempts) != 1 || attempts[0].Outcome != message.OutcomeBlocked || attempts[0].StatusCode != nil ||
		attempts[0].ResponseSnippet != nil {
		testInstance.Fatalf("attempts = %+v, want one blocked attempt with no status and no snippet", attempts)
	}
}

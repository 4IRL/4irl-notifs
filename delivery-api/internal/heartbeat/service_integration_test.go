//go:build integration

package heartbeat_test

// End-to-end test of the heartbeat alert path on real Postgres: the real
// heartbeat.Service and PGStore, message.Service, registry.Service and
// dispatch.Worker, delivering to an httptest webhook receiver. Only the
// heartbeat clock is shifted, so a ping, a missed deadline, a tick and a recovery
// run for real without waiting out the period.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	e2eTestTimeout   = 60 * time.Second
	e2eWaitTimeout   = 15 * time.Second
	e2ePollInterval  = 25 * time.Millisecond
	e2eQuietPeriod   = 400 * time.Millisecond
	e2eSigningSecret = "heartbeat-signing-secret-0123456789abcdef"
	e2ePeriodSeconds = 60
	e2eGraceSeconds  = 60
	e2eFirstHook     = "hb_hook_a"
	e2eSecondHook    = "hb_hook_b"
	e2eAppID         = "billing"
	e2eCheckName     = "nightly"
)

// shiftedClock is real time plus an offset a test advances to pass the deadline
// without waiting.
type shiftedClock struct {
	offset atomic.Int64
}

func (clock *shiftedClock) Now() time.Time {
	return time.Now().Add(time.Duration(clock.offset.Load()))
}

func (clock *shiftedClock) Advance(duration time.Duration) {
	clock.offset.Add(int64(duration))
}

// hookRequest is one webhook call the receiver saw.
type hookRequest struct {
	path string
	body []byte
}

type hookReceiver struct {
	server   *httptest.Server
	mutex    sync.Mutex
	requests []hookRequest
}

func newHookReceiver(testInstance *testing.T) *hookReceiver {
	testInstance.Helper()
	receiver := &hookReceiver{}
	receiver.server = httptest.NewTLSServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			testInstance.Errorf("receiver: reading body: %v", readErr)
		}
		receiver.mutex.Lock()
		receiver.requests = append(receiver.requests, hookRequest{path: request.URL.Path, body: body})
		receiver.mutex.Unlock()
		responseWriter.WriteHeader(http.StatusOK)
	}))
	testInstance.Cleanup(receiver.server.Close)
	return receiver
}

func (receiver *hookReceiver) received() []hookRequest {
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()
	return append([]hookRequest(nil), receiver.requests...)
}

// waitForRequests blocks until the receiver has seen at least count requests.
func (receiver *hookReceiver) waitForRequests(testInstance *testing.T, count int) {
	testInstance.Helper()
	deadline := time.Now().Add(e2eWaitTimeout)
	for len(receiver.received()) < count {
		if time.Now().After(deadline) {
			testInstance.Fatalf("timed out waiting for %d webhook requests, saw %d", count, len(receiver.received()))
		}
		time.Sleep(e2ePollInterval)
	}
}

// heartbeatStack is the live heartbeat service over the real delivery pipeline.
type heartbeatStack struct {
	ctx      context.Context
	service  *heartbeat.Service
	store    *heartbeat.PGStore
	messages *message.Service
	receiver *hookReceiver
	clock    *shiftedClock
}

func startHeartbeatStack(testInstance *testing.T) *heartbeatStack {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eTestTimeout)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	logger := slog.New(slog.DiscardHandler)

	sealer, sealErr := seal.New([]byte("0123456789abcdef0123456789abcdef"))
	if sealErr != nil {
		testInstance.Fatalf("seal.New: %v", sealErr)
	}
	registryService := registry.NewService(registry.NewPGStore(pool), sealer, logger)
	messageStore := message.NewPGStore(pool)
	receiver := newHookReceiver(testInstance)

	roots := x509.NewCertPool()
	roots.AddCert(receiver.server.Certificate())
	guardedClient := transport.New(transport.Config{
		AllowAddr: func(netip.Addr) bool { return true },
		TLSConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	})
	worker := dispatch.NewWorker(dispatch.WorkerConfig{
		Queue:        messageStore,
		Resolver:     registryService,
		Sinks:        map[string]sink.Sink{registry.TypeWebhook: sink.NewWebhookSink(guardedClient)},
		Logger:       logger,
		Concurrency:  2,
		PollInterval: 20 * time.Millisecond,
		Random:       func() float64 { return 0 },
	})
	messageService := message.NewService(message.ServiceConfig{
		Store:          messageStore,
		Resolver:       registryService,
		SupportedTypes: map[string]bool{registry.TypeWebhook: true},
		Wake:           worker.Wake,
		Logger:         logger,
	})

	for _, hook := range []string{e2eFirstHook, e2eSecondHook} {
		if _, createErr := registryService.CreateDestination(ctx, registry.DestinationInput{
			Name: hook,
			Type: registry.TypeWebhook,
			Secrets: map[string]string{
				"url":            receiver.server.URL + "/" + hook,
				"signing_secret": e2eSigningSecret,
			},
		}); createErr != nil {
			testInstance.Fatalf("create destination %s: %v", hook, createErr)
		}
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
		case <-time.After(e2eWaitTimeout):
			testInstance.Error("worker did not stop")
		}
	})

	clock := &shiftedClock{}
	store := heartbeat.NewPGStore(pool)
	return &heartbeatStack{
		ctx: ctx,
		service: heartbeat.NewService(heartbeat.ServiceConfig{
			Store: store, Messages: messageService, Now: clock.Now, Logger: logger,
		}),
		store:    store,
		messages: messageService,
		receiver: receiver,
		clock:    clock,
	}
}

// requirePathsAndType asserts the receiver's requests from index `from` hit
// each hook exactly once and carry the given heartbeat payload type and reason
// ("" means the payload's reason must be null).
func requirePathsAndType(testInstance *testing.T, requests []hookRequest, from int, wantType string, wantReason string) {
	testInstance.Helper()
	window := requests[from:]
	if len(window) != 2 {
		testInstance.Fatalf("requests from %d = %d, want 2 (one per destination)", from, len(window))
	}
	paths := make([]string, 0, len(window))
	for _, request := range window {
		paths = append(paths, request.path)
		var body struct {
			Channel string `json:"channel"`
			Payload struct {
				Type   string  `json:"type"`
				AppID  string  `json:"app_id"`
				Check  string  `json:"check"`
				Reason *string `json:"reason"`
			} `json:"payload"`
		}
		if decodeErr := json.Unmarshal(request.body, &body); decodeErr != nil {
			testInstance.Fatalf("decode webhook body %q: %v", request.body, decodeErr)
		}
		if body.Channel != "heartbeat" || body.Payload.Type != wantType ||
			body.Payload.AppID != e2eAppID || body.Payload.Check != e2eCheckName {
			testInstance.Fatalf("webhook body = %s, want a %s alert for %s/%s", request.body, wantType, e2eAppID, e2eCheckName)
		}
		gotReason := ""
		if body.Payload.Reason != nil {
			gotReason = *body.Payload.Reason
		}
		if gotReason != wantReason || (wantReason == "") != (body.Payload.Reason == nil) {
			testInstance.Fatalf("webhook payload reason = %v, want %q: %s", body.Payload.Reason, wantReason, request.body)
		}
	}
	sort.Strings(paths)
	if paths[0] != "/"+e2eFirstHook || paths[1] != "/"+e2eSecondHook {
		testInstance.Fatalf("paths = %v, want one call to each hook", paths)
	}
}

// tick runs one checker pass and fails the test on an error.
func (stack *heartbeatStack) tick(testInstance *testing.T) {
	testInstance.Helper()
	if tickErr := stack.service.Tick(stack.ctx); tickErr != nil {
		testInstance.Fatalf("Tick: %v", tickErr)
	}
}

// requireQuiet runs several more ticks over the quiet period and asserts the
// receiver saw no more than wantTotal requests.
func (stack *heartbeatStack) requireQuiet(testInstance *testing.T, wantTotal int) {
	testInstance.Helper()
	for range 3 {
		stack.clock.Advance(time.Second)
		stack.tick(testInstance)
		time.Sleep(e2eQuietPeriod / 3)
	}
	if got := len(stack.receiver.received()); got != wantTotal {
		testInstance.Fatalf("receiver saw %d requests after further ticks, want exactly %d", got, wantTotal)
	}
	pending, listErr := stack.store.ListPendingAlerts(stack.ctx)
	if listErr != nil {
		testInstance.Fatalf("ListPendingAlerts: %v", listErr)
	}
	if len(pending) != 0 {
		testInstance.Fatalf("pending alerts = %d, want 0", len(pending))
	}
}

func TestMissedPingDeliversOneDownAlertAndOneRecoveryAlert(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)

	created, createErr := stack.service.Create(stack.ctx, heartbeat.NewCheck{
		AppID: e2eAppID, Name: e2eCheckName, PeriodSeconds: e2ePeriodSeconds, GraceSeconds: e2eGraceSeconds,
		DestinationNames: []string{e2eFirstHook, e2eSecondHook},
	})
	if createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}

	// Ping at T, then advance past period + grace: nothing is down yet before that.
	if _, pingErr := stack.service.Ping(stack.ctx, e2eAppID, e2eCheckName); pingErr != nil {
		testInstance.Fatalf("Ping: %v", pingErr)
	}
	stack.tick(testInstance)
	stack.requireQuiet(testInstance, 0)

	stack.clock.Advance((e2ePeriodSeconds + e2eGraceSeconds + 5) * time.Second)
	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, stack.receiver.received(), 0, "heartbeat.down", "missed")
	stack.requireQuiet(testInstance, 2)

	down, getErr := stack.service.Get(stack.ctx, created.ID)
	if getErr != nil {
		testInstance.Fatalf("Get: %v", getErr)
	}
	if down.State != heartbeat.StateDown || down.PendingAlert != nil {
		testInstance.Fatalf("check = state %q pending %v, want down with the outbox empty", down.State, down.PendingAlert)
	}

	// A ping recovers it: exactly one recovery alert per destination.
	if _, pingErr := stack.service.Ping(stack.ctx, e2eAppID, e2eCheckName); pingErr != nil {
		testInstance.Fatalf("recovery Ping: %v", pingErr)
	}
	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 4)
	requirePathsAndType(testInstance, stack.receiver.received(), 2, "heartbeat.recovered", "")
	stack.requireQuiet(testInstance, 4)
}

func TestFailPingDeliversOneDownAlertWithoutWaitingForTheDeadline(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)

	if _, createErr := stack.service.Create(stack.ctx, heartbeat.NewCheck{
		AppID: e2eAppID, Name: e2eCheckName, PeriodSeconds: e2ePeriodSeconds, GraceSeconds: e2eGraceSeconds,
		DestinationNames: []string{e2eFirstHook, e2eSecondHook},
	}); createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}
	if _, failErr := stack.service.Fail(stack.ctx, e2eAppID, e2eCheckName); failErr != nil {
		testInstance.Fatalf("Fail: %v", failErr)
	}
	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, stack.receiver.received(), 0, "heartbeat.down", "fail")
	stack.requireQuiet(testInstance, 2)
}

// flakyMessages fails the Submit to one destination once, then delegates every
// Submit to the real message service.
type flakyMessages struct {
	real          heartbeat.Messages
	failOnce      string
	alreadyFailed atomic.Bool
}

func (flaky *flakyMessages) Submit(ctx context.Context, envelope message.Envelope) (message.StoredMessage, message.SubmitOutcome, error) {
	if envelope.ToDestination == flaky.failOnce && flaky.alreadyFailed.CompareAndSwap(false, true) {
		return message.StoredMessage{}, message.SubmitOutcome{}, errors.New("submit failed once")
	}
	return flaky.real.Submit(ctx, envelope)
}

// A transient Submit failure to the first destination does not stop the flush:
// the second destination still receives its alert, and the alert stays pending.
// The retry resubmits every destination with the same keys, so the destination
// that already succeeded is deduplicated by the real message service and gets
// exactly one delivery.
func TestRetryAfterATransientSubmitFailureDeliversEachDestinationExactlyOnce(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)
	flaky := &flakyMessages{real: stack.messages, failOnce: e2eFirstHook}
	service := heartbeat.NewService(heartbeat.ServiceConfig{
		Store: stack.store, Messages: flaky, Now: stack.clock.Now, Logger: slog.New(slog.DiscardHandler),
	})

	if _, createErr := service.Create(stack.ctx, heartbeat.NewCheck{
		AppID: e2eAppID, Name: e2eCheckName, PeriodSeconds: e2ePeriodSeconds, GraceSeconds: e2eGraceSeconds,
		DestinationNames: []string{e2eFirstHook, e2eSecondHook},
	}); createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}
	if _, failErr := service.Fail(stack.ctx, e2eAppID, e2eCheckName); failErr != nil {
		testInstance.Fatalf("Fail: %v", failErr)
	}

	if tickErr := service.Tick(stack.ctx); tickErr == nil {
		testInstance.Fatal("first Tick returned nil, want the transient Submit error")
	}
	pending, listErr := stack.store.ListPendingAlerts(stack.ctx)
	if listErr != nil || len(pending) != 1 {
		testInstance.Fatalf("pending after the failed flush = %v (err %v), want the one alert kept", pending, listErr)
	}
	// The failing first destination must not have stopped the second one.
	stack.receiver.waitForRequests(testInstance, 1)
	if first := stack.receiver.received()[0]; first.path != "/"+e2eSecondHook {
		testInstance.Fatalf("first delivery went to %q, want the second destination %q", first.path, "/"+e2eSecondHook)
	}

	if tickErr := service.Tick(stack.ctx); tickErr != nil {
		testInstance.Fatalf("retry Tick: %v", tickErr)
	}
	stack.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, stack.receiver.received(), 0, "heartbeat.down", "fail")
	// A third pass proves nothing more is pending or re-sent.
	time.Sleep(e2eQuietPeriod)
	if got := len(stack.receiver.received()); got != 2 {
		testInstance.Fatalf("receiver saw %d requests, want exactly 2 (no duplicate for the destination that already succeeded)", got)
	}
}

// A check that was already overdue when the service started (its anchor is hours
// old, as after an outage) is not marked down while the startup hold-off runs,
// and goes down on the first tick once the hold-off has elapsed.
func TestOverdueCheckIsNotAdvancedUntilTheStartupHoldOffEnds(testInstance *testing.T) {
	stack := startHeartbeatStack(testInstance)

	created, createErr := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: e2eAppID, Name: e2eCheckName, PeriodSeconds: e2ePeriodSeconds, GraceSeconds: e2eGraceSeconds,
		DestinationNames: []string{e2eFirstHook, e2eSecondHook},
	}, stack.clock.Now().Add(-3*time.Hour))
	if createErr != nil {
		testInstance.Fatalf("Create: %v", createErr)
	}

	// Inside the hold-off: well overdue, but nothing goes down.
	stack.clock.Advance(heartbeat.StartupHoldOff / 2)
	stack.tick(testInstance)
	stack.requireQuiet(testInstance, 0)
	held, getErr := stack.service.Get(stack.ctx, created.ID)
	if getErr != nil {
		testInstance.Fatalf("Get: %v", getErr)
	}
	if held.State == heartbeat.StateDown {
		testInstance.Fatalf("check went down inside the startup hold-off")
	}

	// After the hold-off: the overdue check goes down, one alert per destination.
	stack.clock.Advance(heartbeat.StartupHoldOff)
	stack.tick(testInstance)
	stack.receiver.waitForRequests(testInstance, 2)
	requirePathsAndType(testInstance, stack.receiver.received(), 0, "heartbeat.down", "missed")
	stack.requireQuiet(testInstance, 2)
}

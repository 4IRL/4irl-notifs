//go:build integration

package dispatch_test

// In-process end-to-end tests of the to.recipient fan-out on real Postgres: the real
// message.Service (with personal.Service as its PersonalTargets), worker, releaser,
// ntfy sink and webhook sink. An httptest ntfy server and one TLS webhook receiver
// (distinguished by request path) stand in for the endpoints.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/transport"
)

const (
	fanoutApp      = "pipeapp"
	fanoutOtherApp = "otherapp"
	fanoutAlice    = "alice@example.com"
	fanoutBob      = "bob@example.com"
)

type fanoutPipeline struct {
	ctx             context.Context
	pool            *pgxpool.Pool
	personalService *personal.Service
	store           *message.PGStore
	personalStore   *personal.PGStore
	messages        *message.Service
	worker          *dispatch.Worker
	clock           *shiftedClock
	webhook         *receiver

	ntfyMutex    sync.Mutex
	ntfyRequests []receivedRequest
}

func startFanoutPipeline(testInstance *testing.T) *fanoutPipeline {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pipelineTestTimeout)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	logger := slog.New(slog.DiscardHandler)

	sealer, sealErr := seal.New([]byte("0123456789abcdef0123456789abcdef"))
	if sealErr != nil {
		testInstance.Fatalf("seal.New: %v", sealErr)
	}
	live := &fanoutPipeline{ctx: ctx, clock: &shiftedClock{}}
	live.webhook = newReceiver(testInstance, func(int) int { return http.StatusOK })

	ntfyServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			testInstance.Errorf("ntfy: reading request body: %v", readErr)
		}
		live.ntfyMutex.Lock()
		live.ntfyRequests = append(live.ntfyRequests, receivedRequest{
			method: request.Method, path: request.URL.Path, header: request.Header.Clone(), body: body,
		})
		live.ntfyMutex.Unlock()
		responseWriter.WriteHeader(http.StatusOK)
	}))
	testInstance.Cleanup(ntfyServer.Close)

	registryService := registry.NewService(registry.NewPGStore(pool), sealer, logger)
	live.personalStore = personal.NewPGStore(pool, sealer)
	personalService := personal.NewService(live.personalStore, sealer, logger)
	live.store = message.NewPGStore(pool)
	live.pool = pool
	live.personalService = personalService
	publishers := publisher.NewService(publisher.NewPGStore(pool), pipelineMinter{}, sealer, pipelineBound{}, nil)
	if _, mintErr := publishers.Mint(ctx, fanoutApp, false); mintErr != nil {
		testInstance.Fatalf("Mint: %v", mintErr)
	}

	live.worker = dispatch.NewWorker(dispatch.WorkerConfig{
		Queue:      live.store,
		Resolver:   registryService,
		Recipients: publishers,
		Personal:   personalService,
		Sinks: map[string]sink.Sink{
			registry.TypeNtfy:    sink.NewNtfySink(sink.NtfyConfig{BaseURL: ntfyServer.URL}),
			registry.TypeWebhook: sink.NewWebhookSink(transport.New(live.webhook.trustedConfig())),
		},
		Logger:       logger,
		Concurrency:  2,
		PollInterval: pipelineWorkerPoll,
		Now:          live.clock.Now,
		Random:       func() float64 { return 0 },
	})
	live.messages = message.NewService(message.ServiceConfig{
		Store:           live.store,
		Resolver:        registryService,
		Publishers:      publishers,
		PersonalTargets: personalService,
		SupportedTypes:  map[string]bool{registry.TypeNtfy: true, registry.TypeWebhook: true},
		Wake:            live.worker.Wake,
		Logger:          logger,
	})

	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		live.worker.Run(workerCtx)
	}()
	testInstance.Cleanup(func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(pipelineWaitTimeout):
			testInstance.Error("worker did not stop")
		}
	})
	return live
}

// register stores one enabled webhook personal destination served at path.
func (live *fanoutPipeline) register(
	testInstance *testing.T, email, registrar, path string, optIn bool,
) personal.Destination {
	testInstance.Helper()
	created, insertErr := live.personalStore.Insert(live.ctx, personal.NewDestination{
		PersonHash: personhash.Hash(email), RegisteredByAppID: registrar, Label: path, Type: personal.TypeWebhook,
		Secrets: map[string]string{"url": live.webhook.server.URL + path, "signing_secret": personalPipelineSecret},
		OptIn:   optIn,
	})
	if insertErr != nil {
		testInstance.Fatalf("insert personal destination %s: %v", path, insertErr)
	}
	return created
}

func (live *fanoutPipeline) envelope(key, email string) message.Envelope {
	return message.Envelope{
		AppID: fanoutApp, Channel: pipelineChannel, IdempotencyKey: key, ToRecipient: email,
		Payload: json.RawMessage(`{"order":42}`), Display: &message.Display{Title: "Hello", Body: "Body"},
	}
}

func (live *fanoutPipeline) waitAllDelivered(testInstance *testing.T, messageID string, wantDeliveries int) []message.Delivery {
	testInstance.Helper()
	deadline := time.Now().Add(pipelineWaitTimeout)
	var last []message.Delivery
	for time.Now().Before(deadline) {
		stored, getErr := live.store.GetMessage(live.ctx, messageID)
		if getErr != nil {
			testInstance.Fatalf("GetMessage: %v", getErr)
		}
		last = stored.Deliveries
		delivered := len(last) == wantDeliveries
		for _, delivery := range last {
			delivered = delivered && delivery.State == message.StateDelivered
		}
		if delivered {
			return last
		}
		time.Sleep(pipelinePollInterval)
	}
	testInstance.Fatalf("timed out waiting for %d delivered deliveries; last = %+v", wantDeliveries, last)
	return last
}

func (live *fanoutPipeline) ntfySeen() []receivedRequest {
	live.ntfyMutex.Lock()
	defer live.ntfyMutex.Unlock()
	return append([]receivedRequest(nil), live.ntfyRequests...)
}

func (live *fanoutPipeline) webhookPaths() []string {
	var paths []string
	for _, request := range live.webhook.received() {
		paths = append(paths, request.path)
	}
	sort.Strings(paths)
	return paths
}

// requireDeliveryNames asserts the delivery names, ignoring the store's read order.
func requireDeliveryNames(testInstance *testing.T, deliveries []message.Delivery, want ...string) {
	testInstance.Helper()
	got := make([]string, 0, len(deliveries))
	for _, delivery := range deliveries {
		got = append(got, delivery.DestinationName)
	}
	sort.Strings(got)
	sort.Strings(want)
	requireStrings(testInstance, "delivery names", got, want)
}

func requireStrings(testInstance *testing.T, label string, got, want []string) {
	testInstance.Helper()
	if len(got) != len(want) {
		testInstance.Fatalf("%s = %v, want %v", label, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			testInstance.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestPipelineFansARecipientMessageOutToOptedInPersonalDestinations(testInstance *testing.T) {
	live := startFanoutPipeline(testInstance)
	mine := live.register(testInstance, fanoutAlice, fanoutApp, "/alice-mine", true)
	live.register(testInstance, fanoutAlice, fanoutOtherApp, "/alice-other-app", true)
	live.register(testInstance, fanoutBob, fanoutApp, "/bob-mine", true)

	stored, outcome, submitErr := live.messages.Submit(live.ctx, live.envelope("fanout-1", fanoutAlice))
	if submitErr != nil || outcome.Duplicate {
		testInstance.Fatalf("Submit = (outcome %+v, %v), want a new message", outcome, submitErr)
	}
	requireDeliveryNames(testInstance, stored.Deliveries,
		registry.RecipientDestinationName, personal.PersonalDeliveryName(mine.ID))

	live.waitAllDelivered(testInstance, stored.ID, 2)
	time.Sleep(pipelineQuietPeriod)

	if got := len(live.ntfySeen()); got != 1 {
		testInstance.Fatalf("ntfy saw %d requests, want 1", got)
	}
	requireStrings(testInstance, "webhook paths", live.webhookPaths(), []string{"/alice-mine"})

	// Bob's own message reaches Bob's endpoint, so "nothing for Bob" above was not an empty lookup.
	bobStored, _, bobErr := live.messages.Submit(live.ctx, live.envelope("fanout-2", fanoutBob))
	if bobErr != nil {
		testInstance.Fatalf("Submit(bob) = %v", bobErr)
	}
	live.waitAllDelivered(testInstance, bobStored.ID, 2)
	time.Sleep(pipelineQuietPeriod)
	requireStrings(testInstance, "webhook paths after bob", live.webhookPaths(), []string{"/alice-mine", "/bob-mine"})
}

func TestPipelineScheduledRecipientFanOutIsFrozenAtSubmit(testInstance *testing.T) {
	live := startFanoutPipeline(testInstance)
	first := live.register(testInstance, fanoutAlice, fanoutApp, "/alice-first", true)

	deliverAt := time.Now().Add(time.Hour)
	envelope := live.envelope("fanout-scheduled-1", fanoutAlice)
	envelope.DeliverAt = &deliverAt
	stored, outcome, submitErr := live.messages.Submit(live.ctx, envelope)
	if submitErr != nil || outcome.Duplicate || !outcome.Scheduled {
		testInstance.Fatalf("Submit = (outcome %+v, %v), want a new scheduled message", outcome, submitErr)
	}
	if len(stored.Deliveries) != 2 {
		testInstance.Fatalf("deliveries = %+v, want @recipient plus the first destination", stored.Deliveries)
	}
	for _, delivery := range stored.Deliveries {
		if delivery.State != message.StateScheduled {
			testInstance.Fatalf("delivery %s state = %q before release, want scheduled", delivery.DestinationName, delivery.State)
		}
	}

	// Added after submit and before release: it must never receive this message.
	live.register(testInstance, fanoutAlice, fanoutApp, "/alice-late", true)

	live.clock.Advance(2 * time.Hour)
	live.runReleaserOnce(testInstance)
	deliveries := live.waitAllDelivered(testInstance, stored.ID, 2)
	time.Sleep(pipelineQuietPeriod)

	requireDeliveryNames(testInstance, deliveries,
		registry.RecipientDestinationName, personal.PersonalDeliveryName(first.ID))
	if got := len(live.ntfySeen()); got != 1 {
		testInstance.Fatalf("ntfy saw %d requests, want 1", got)
	}
	requireStrings(testInstance, "webhook paths", live.webhookPaths(), []string{"/alice-first"})
}

// runReleaserOnce runs the real releaser's immediate first pass on the shifted clock.
func (live *fanoutPipeline) runReleaserOnce(testInstance *testing.T) {
	testInstance.Helper()
	signaling := &callSignalingReleaser{inner: live.store, finished: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(live.ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		dispatch.RunReleaser(ctx, dispatch.ReleaserConfig{
			Releaser: signaling, Interval: time.Hour, BatchSize: dispatch.DefaultReleaseBatchSize,
			Now: live.clock.Now, Wake: live.worker.Wake, Logger: slog.New(slog.DiscardHandler),
		})
	}()
	select {
	case <-signaling.finished:
	case <-time.After(pipelineWaitTimeout):
		testInstance.Fatal("the releaser did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(pipelineWaitTimeout):
		testInstance.Fatal("the releaser did not stop")
	}
}

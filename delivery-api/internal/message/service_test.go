package message

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The real registry service must satisfy the consumer-side Resolver.
var _ Resolver = (*registry.Service)(nil)

// fakeServiceStore is a scripted Store. It embeds the interface so the methods
// the service never calls panic on a nil receiver if they ever are.
type fakeServiceStore struct {
	Store

	mutex sync.Mutex

	insertCalls []insertCall
	insertErr   error
	// insertDuplicate makes InsertMessage report a lost insert race.
	insertDuplicate bool

	// getResults is consumed one per GetMessage call; the last repeats.
	getResults []StoredMessage
	getErr     error
	getCalls   int

	// getByKeyPresent makes GetMessageByKey return getByKeyResult; otherwise it
	// reports ErrMessageNotFound, as the real store does for an unknown key.
	getByKeyPresent bool
	getByKeyResult  StoredMessage
	getByKeyErr     error
	getByKeyCalls   int

	listResult []Delivery
	listErr    error
	listCalls  []listCall

	attemptsResult []Attempt
	attemptsErr    error

	replayResult Delivery
	replayErr    error
	replayCalls  []replayCall
}

type insertCall struct {
	envelope Envelope
	targets  []DeliveryTarget
}

type listCall struct {
	state string
	limit int
}

type replayCall struct {
	deliveryID int64
	now        time.Time
}

func (fake *fakeServiceStore) InsertMessage(_ context.Context, envelope Envelope, targets []DeliveryTarget) (StoredMessage, bool, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.insertCalls = append(fake.insertCalls, insertCall{envelope: envelope, targets: targets})
	if fake.insertErr != nil {
		return StoredMessage{}, false, fake.insertErr
	}
	return StoredMessage{ID: "11111111-1111-1111-1111-111111111111", Envelope: envelope}, fake.insertDuplicate, nil
}

func (fake *fakeServiceStore) GetMessage(context.Context, string) (StoredMessage, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.getCalls++
	if fake.getErr != nil {
		return StoredMessage{}, fake.getErr
	}
	index := min(fake.getCalls-1, len(fake.getResults)-1)
	return fake.getResults[index], nil
}

func (fake *fakeServiceStore) GetMessageByKey(context.Context, string, string) (StoredMessage, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.getByKeyCalls++
	if fake.getByKeyErr != nil {
		return StoredMessage{}, fake.getByKeyErr
	}
	if !fake.getByKeyPresent {
		return StoredMessage{}, ErrMessageNotFound
	}
	return fake.getByKeyResult, nil
}

func (fake *fakeServiceStore) ListDeliveries(_ context.Context, state string, limit int) ([]Delivery, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.listCalls = append(fake.listCalls, listCall{state: state, limit: limit})
	return fake.listResult, fake.listErr
}

func (fake *fakeServiceStore) ListAttempts(context.Context, int64) ([]Attempt, error) {
	return fake.attemptsResult, fake.attemptsErr
}

func (fake *fakeServiceStore) Replay(_ context.Context, deliveryID int64, now time.Time) (Delivery, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.replayCalls = append(fake.replayCalls, replayCall{deliveryID: deliveryID, now: now})
	return fake.replayResult, fake.replayErr
}

// fakeResolver is a scripted Resolver.
type fakeResolver struct {
	result []registry.ResolvedDestination
	err    error
	calls  []resolveCall
}

type resolveCall struct {
	appID       string
	destination string
	channel     string
}

func (fake *fakeResolver) Resolve(_ context.Context, appID, destinationName, channel string) ([]registry.ResolvedDestination, error) {
	fake.calls = append(fake.calls, resolveCall{appID: appID, destination: destinationName, channel: channel})
	return fake.result, fake.err
}

// wakeCounter counts Wake calls.
type wakeCounter struct {
	mutex sync.Mutex
	count int
}

func (counter *wakeCounter) wake() {
	counter.mutex.Lock()
	defer counter.mutex.Unlock()
	counter.count++
}

func (counter *wakeCounter) calls() int {
	counter.mutex.Lock()
	defer counter.mutex.Unlock()
	return counter.count
}

var webhookDestination = registry.ResolvedDestination{
	ID: 7, Name: "ops_webhook", Type: registry.TypeWebhook,
	Secrets: map[string]string{"url": "https://hooks.example.com/secret"},
}

type serviceFixture struct {
	service  *Service
	store    *fakeServiceStore
	resolver *fakeResolver
	wakes    *wakeCounter
}

func newServiceFixture() serviceFixture {
	store := &fakeServiceStore{}
	resolver := &fakeResolver{result: []registry.ResolvedDestination{webhookDestination}}
	wakes := &wakeCounter{}
	service := NewService(ServiceConfig{
		Store:          store,
		Resolver:       resolver,
		SupportedTypes: map[string]bool{registry.TypeWebhook: true},
		Wake:           wakes.wake,
		PollInterval:   time.Millisecond,
	})
	return serviceFixture{service: service, store: store, resolver: resolver, wakes: wakes}
}

func TestSubmitStoresResolvedTargetsAndWakesOnce(testInstance *testing.T) {
	fixture := newServiceFixture()
	envelope := validEnvelope()
	envelope.ToDestination = "ops_webhook"

	stored, duplicate, err := fixture.service.Submit(context.Background(), envelope)
	if err != nil || duplicate {
		testInstance.Fatalf("Submit = (%v, duplicate %v), want a new message", err, duplicate)
	}
	if stored.ID == "" {
		testInstance.Fatalf("stored id is empty")
	}
	wantDestinationID := int64(7)
	wantTargets := []DeliveryTarget{{DestinationID: &wantDestinationID, DestinationName: "ops_webhook", DestinationType: registry.TypeWebhook}}
	if len(fixture.store.insertCalls) != 1 || !reflect.DeepEqual(fixture.store.insertCalls[0].targets, wantTargets) {
		testInstance.Fatalf("insert calls = %+v, want one with targets %+v", fixture.store.insertCalls, wantTargets)
	}
	wantResolve := []resolveCall{{appID: "urls4irl", destination: "ops_webhook", channel: "alerts"}}
	if !reflect.DeepEqual(fixture.resolver.calls, wantResolve) {
		testInstance.Fatalf("resolve calls = %+v, want %+v", fixture.resolver.calls, wantResolve)
	}
	if fixture.wakes.calls() != 1 {
		testInstance.Fatalf("Wake calls = %d, want 1", fixture.wakes.calls())
	}
}

func TestSubmitRouteModeResolvesWithEmptyDestination(testInstance *testing.T) {
	fixture := newServiceFixture()
	if _, _, err := fixture.service.Submit(context.Background(), validEnvelope()); err != nil {
		testInstance.Fatalf("Submit error = %v", err)
	}
	if got := fixture.resolver.calls[0].destination; got != "" {
		testInstance.Fatalf("resolver destination = %q, want empty (route mode)", got)
	}
}

func TestSubmitInvalidEnvelopeNeverTouchesStoreOrResolver(testInstance *testing.T) {
	fixture := newServiceFixture()
	envelope := validEnvelope()
	envelope.Channel = "Bad Channel"

	_, _, err := fixture.service.Submit(context.Background(), envelope)
	wantValidationMessage(testInstance, err, "invalid channel")
	if fixture.store.getByKeyCalls != 0 || len(fixture.store.insertCalls) != 0 || len(fixture.resolver.calls) != 0 {
		testInstance.Fatalf("store find/insert = %d/%d, resolver = %d; want no calls",
			fixture.store.getByKeyCalls, len(fixture.store.insertCalls), len(fixture.resolver.calls))
	}
	if fixture.wakes.calls() != 0 {
		testInstance.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
	}
}

func TestSubmitDuplicateReturnsOriginalWithoutResolveInsertOrWake(testInstance *testing.T) {
	fixture := newServiceFixture()
	original := StoredMessage{ID: "22222222-2222-2222-2222-222222222222", Envelope: validEnvelope()}
	fixture.store.getByKeyResult = original
	fixture.store.getByKeyPresent = true
	// The duplicate must be served even though resolution would now fail.
	fixture.resolver.err = registry.ErrDestinationDisabled

	envelope := validEnvelope()
	envelope.Channel = "changed"
	stored, duplicate, err := fixture.service.Submit(context.Background(), envelope)
	if err != nil || !duplicate {
		testInstance.Fatalf("Submit = (%v, duplicate %v), want a duplicate", err, duplicate)
	}
	if stored.ID != original.ID || stored.Envelope.Channel != "alerts" {
		testInstance.Fatalf("stored = %+v, want the original message", stored)
	}
	if len(fixture.resolver.calls) != 0 || len(fixture.store.insertCalls) != 0 || fixture.wakes.calls() != 0 {
		testInstance.Fatalf("resolve/insert/wake = %d/%d/%d, want none",
			len(fixture.resolver.calls), len(fixture.store.insertCalls), fixture.wakes.calls())
	}
}

func TestSubmitLostInsertRaceIsADuplicateWithoutWake(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.insertDuplicate = true

	_, duplicate, err := fixture.service.Submit(context.Background(), validEnvelope())
	if err != nil || !duplicate {
		testInstance.Fatalf("Submit = (%v, duplicate %v), want a duplicate", err, duplicate)
	}
	if fixture.wakes.calls() != 0 {
		testInstance.Fatalf("Wake calls = %d, want 0", fixture.wakes.calls())
	}
}

func TestSubmitResolutionFailuresStoreNothing(testInstance *testing.T) {
	testCases := []struct {
		name       string
		resolved   []registry.ResolvedDestination
		resolveErr error
		wantErr    error
	}{
		{name: "unknown destination", resolveErr: registry.ErrUnknownDestination, wantErr: registry.ErrUnknownDestination},
		{name: "disabled destination", resolveErr: registry.ErrDestinationDisabled, wantErr: registry.ErrDestinationDisabled},
		{name: "nothing matched", resolved: []registry.ResolvedDestination{}, wantErr: ErrNoDestinationMatched},
		{name: "nil result is nothing matched", resolved: nil, wantErr: ErrNoDestinationMatched},
		{
			name:     "unsupported type",
			resolved: []registry.ResolvedDestination{{ID: 3, Name: "ops_ntfy", Type: registry.TypeNtfy}},
			wantErr:  ErrUnsupportedDestinationType,
		},
		{
			name: "one unsupported among supported",
			resolved: []registry.ResolvedDestination{
				webhookDestination, {ID: 3, Name: "zz_ntfy", Type: registry.TypeNtfy},
			},
			wantErr: ErrUnsupportedDestinationType,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newServiceFixture()
			fixture.resolver.result = testCase.resolved
			fixture.resolver.err = testCase.resolveErr

			_, _, err := fixture.service.Submit(context.Background(), validEnvelope())
			if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("error = %v, want %v", err, testCase.wantErr)
			}
			if len(fixture.store.insertCalls) != 0 || fixture.wakes.calls() != 0 {
				subTest.Fatalf("insert/wake = %d/%d, want none", len(fixture.store.insertCalls), fixture.wakes.calls())
			}
		})
	}
}

func TestSubmitFailsClosedWithoutSupportedTypes(testInstance *testing.T) {
	store := &fakeServiceStore{}
	resolver := &fakeResolver{result: []registry.ResolvedDestination{webhookDestination}}
	service := NewService(ServiceConfig{Store: store, Resolver: resolver})

	_, _, err := service.Submit(context.Background(), validEnvelope())
	if !errors.Is(err, ErrUnsupportedDestinationType) {
		testInstance.Fatalf("error = %v, want ErrUnsupportedDestinationType", err)
	}
}

func TestSubmitHumanFacingDestinationsRequireARenderableDisplay(testInstance *testing.T) {
	const wantMessage = "display.title or display.body is required for ntfy and discord destinations"
	testCases := []struct {
		name            string
		destinationType string
		display         *Display
		wantRefused     bool
	}{
		{name: "ntfy without display", destinationType: registry.TypeNtfy, display: nil, wantRefused: true},
		{name: "discord without display", destinationType: registry.TypeDiscord, display: nil, wantRefused: true},
		{name: "ntfy with an empty display", destinationType: registry.TypeNtfy, display: &Display{}, wantRefused: true},
		{
			name: "discord with only a url and priority", destinationType: registry.TypeDiscord,
			display: &Display{URL: "https://example.com", Priority: "high"}, wantRefused: true,
		},
		{
			name: "ntfy with a whitespace-only title", destinationType: registry.TypeNtfy,
			display: &Display{Title: "   "}, wantRefused: true,
		},
		{
			name: "discord with a whitespace-only body", destinationType: registry.TypeDiscord,
			display: &Display{Body: "\n\t\r "}, wantRefused: true,
		},
		{
			name: "discord with a whitespace-only title and body and a url", destinationType: registry.TypeDiscord,
			display: &Display{Title: " ", Body: "\n", URL: "https://example.com"}, wantRefused: true,
		},
		{name: "discord with a title", destinationType: registry.TypeDiscord, display: &Display{Title: "T"}},
		{
			name: "ntfy with a blank title but a real body", destinationType: registry.TypeNtfy,
			display: &Display{Title: " ", Body: "B"},
		},
		{name: "ntfy with a body", destinationType: registry.TypeNtfy, display: &Display{Body: "B"}},
		{name: "webhook without display", destinationType: registry.TypeWebhook, display: nil},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newServiceFixture()
			service := NewService(ServiceConfig{
				Store: fixture.store,
				Resolver: &fakeResolver{result: []registry.ResolvedDestination{
					{ID: 3, Name: "dest", Type: testCase.destinationType},
				}},
				SupportedTypes: map[string]bool{
					registry.TypeWebhook: true, registry.TypeNtfy: true, registry.TypeDiscord: true,
				},
			})
			envelope := validEnvelope()
			envelope.Display = testCase.display

			_, _, err := service.Submit(context.Background(), envelope)

			if testCase.wantRefused {
				wantValidationMessage(subTest, err, wantMessage)
				if len(fixture.store.insertCalls) != 0 {
					subTest.Fatalf("insert calls = %d, want none", len(fixture.store.insertCalls))
				}
				return
			}
			if err != nil {
				subTest.Fatalf("Submit error = %v, want success", err)
			}
			if len(fixture.store.insertCalls) != 1 {
				subTest.Fatalf("insert calls = %d, want 1", len(fixture.store.insertCalls))
			}
		})
	}
}

// fakePublishers is a scripted Publishers.
type fakePublishers struct {
	has   bool
	err   error
	calls []string
}

func (fake *fakePublishers) HasPublisher(_ context.Context, appID string) (bool, error) {
	fake.calls = append(fake.calls, appID)
	return fake.has, fake.err
}

// recipientFixture is a service with ntfy supported and a publisher present.
type recipientFixture struct {
	service    *Service
	store      *fakeServiceStore
	resolver   *fakeResolver
	publishers *fakePublishers
	wakes      *wakeCounter
}

func newRecipientFixture() recipientFixture {
	store := &fakeServiceStore{}
	resolver := &fakeResolver{result: []registry.ResolvedDestination{webhookDestination}}
	publishers := &fakePublishers{has: true}
	wakes := &wakeCounter{}
	service := NewService(ServiceConfig{
		Store:          store,
		Resolver:       resolver,
		Publishers:     publishers,
		SupportedTypes: map[string]bool{registry.TypeWebhook: true, registry.TypeNtfy: true},
		Wake:           wakes.wake,
		PollInterval:   time.Millisecond,
	})
	return recipientFixture{service: service, store: store, resolver: resolver, publishers: publishers, wakes: wakes}
}

func recipientEnvelope(recipient string) Envelope {
	envelope := validEnvelope()
	envelope.ToRecipient = recipient
	envelope.Display = &Display{Title: "T", Body: "B"}
	return envelope
}

func TestSubmitRecipientEmailIsStoredAsItsHash(testInstance *testing.T) {
	fixture := newRecipientFixture()

	_, duplicate, err := fixture.service.Submit(context.Background(), recipientEnvelope(" Alice@Example.com "))
	if err != nil || duplicate {
		testInstance.Fatalf("Submit = (%v, duplicate %v), want a new message", err, duplicate)
	}
	if len(fixture.store.insertCalls) != 1 {
		testInstance.Fatalf("insert calls = %d, want 1", len(fixture.store.insertCalls))
	}
	call := fixture.store.insertCalls[0]
	if want := personhash.Hash("alice@example.com"); call.envelope.ToRecipient != want {
		testInstance.Fatalf("stored ToRecipient = %q, want the hash %q (the email must never reach the store)",
			call.envelope.ToRecipient, want)
	}
	wantTargets := []DeliveryTarget{{
		DestinationID: nil, DestinationName: registry.RecipientDestinationName, DestinationType: registry.TypeNtfy,
	}}
	if !reflect.DeepEqual(call.targets, wantTargets) {
		testInstance.Fatalf("targets = %+v, want %+v", call.targets, wantTargets)
	}
	if len(fixture.resolver.calls) != 0 {
		testInstance.Fatalf("resolver calls = %d, want 0: a recipient bypasses the registry", len(fixture.resolver.calls))
	}
	if !reflect.DeepEqual(fixture.publishers.calls, []string{"urls4irl"}) {
		testInstance.Fatalf("publisher lookups = %v, want one for urls4irl", fixture.publishers.calls)
	}
	if fixture.wakes.calls() != 1 {
		testInstance.Fatalf("Wake calls = %d, want 1", fixture.wakes.calls())
	}
}

func TestSubmitRecipientHashIsStoredUnchanged(testInstance *testing.T) {
	fixture := newRecipientFixture()
	const hash = "76gzqgp4byjl6dje"

	if _, _, err := fixture.service.Submit(context.Background(), recipientEnvelope(hash)); err != nil {
		testInstance.Fatalf("Submit error = %v", err)
	}
	if got := fixture.store.insertCalls[0].envelope.ToRecipient; got != hash {
		testInstance.Fatalf("stored ToRecipient = %q, want %q", got, hash)
	}
}

func TestSubmitRecipientFailuresStoreNothing(testInstance *testing.T) {
	storeErr := errors.New("publisher lookup failed")
	testCases := []struct {
		name       string
		configure  func(fixture recipientFixture) *Service
		mutate     func(envelope *Envelope)
		wantErr    error
		wantFields string
	}{
		{
			name:      "no publisher",
			configure: func(fixture recipientFixture) *Service { fixture.publishers.has = false; return fixture.service },
			wantErr:   registry.ErrNoPublisher,
		},
		{
			name: "nil Publishers fails closed",
			configure: func(fixture recipientFixture) *Service {
				return NewService(ServiceConfig{
					Store: fixture.store, Resolver: fixture.resolver,
					SupportedTypes: map[string]bool{registry.TypeNtfy: true},
				})
			},
			wantErr: registry.ErrNoPublisher,
		},
		{
			name:      "publisher lookup failure",
			configure: func(fixture recipientFixture) *Service { fixture.publishers.err = storeErr; return fixture.service },
			wantErr:   storeErr,
		},
		{
			name: "ntfy not supported",
			configure: func(fixture recipientFixture) *Service {
				return NewService(ServiceConfig{
					Store: fixture.store, Resolver: fixture.resolver, Publishers: fixture.publishers,
					SupportedTypes: map[string]bool{registry.TypeWebhook: true},
				})
			},
			wantErr: ErrUnsupportedDestinationType,
		},
		{
			name:      "topic over 64 characters",
			configure: func(fixture recipientFixture) *Service { return fixture.service },
			mutate: func(envelope *Envelope) {
				envelope.AppID = strings.Repeat("a", 30)
				envelope.Channel = strings.Repeat("c", 17)
			},
			wantFields: "app_id and channel are too long for a recipient topic",
		},
		{
			name:       "missing display",
			configure:  func(fixture recipientFixture) *Service { return fixture.service },
			mutate:     func(envelope *Envelope) { envelope.Display = nil },
			wantFields: "display.title or display.body is required for ntfy and discord destinations",
		},
		{
			name:       "whitespace-only title and body",
			configure:  func(fixture recipientFixture) *Service { return fixture.service },
			mutate:     func(envelope *Envelope) { envelope.Display = &Display{Title: "  ", Body: "\n\t"} },
			wantFields: "display.title or display.body is required for ntfy and discord destinations",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newRecipientFixture()
			service := testCase.configure(fixture)
			envelope := recipientEnvelope("alice@example.com")
			if testCase.mutate != nil {
				testCase.mutate(&envelope)
			}

			_, _, err := service.Submit(context.Background(), envelope)

			if testCase.wantFields != "" {
				wantValidationMessage(subTest, err, testCase.wantFields)
			} else if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("error = %v, want %v", err, testCase.wantErr)
			}
			if len(fixture.store.insertCalls) != 0 || fixture.wakes.calls() != 0 {
				subTest.Fatalf("insert/wake = %d/%d, want none", len(fixture.store.insertCalls), fixture.wakes.calls())
			}
		})
	}
}

func TestSubmitRecipientTopicOfExactly64CharactersIsAccepted(testInstance *testing.T) {
	fixture := newRecipientFixture()
	envelope := recipientEnvelope("alice@example.com")
	envelope.AppID = strings.Repeat("a", 30)
	envelope.Channel = strings.Repeat("c", 16)

	if _, _, err := fixture.service.Submit(context.Background(), envelope); err != nil {
		testInstance.Fatalf("Submit error = %v, want success for a 64-character topic", err)
	}
}

func TestSubmitDuplicateRecipientShortCircuitsBeforePublisherCheck(testInstance *testing.T) {
	fixture := newRecipientFixture()
	fixture.publishers.has = false
	fixture.store.getByKeyPresent = true
	fixture.store.getByKeyResult = StoredMessage{ID: "22222222-2222-2222-2222-222222222222", Envelope: validEnvelope()}

	_, duplicate, err := fixture.service.Submit(context.Background(), recipientEnvelope("alice@example.com"))
	if err != nil || !duplicate {
		testInstance.Fatalf("Submit = (%v, duplicate %v), want a duplicate", err, duplicate)
	}
	if len(fixture.publishers.calls) != 0 || len(fixture.store.insertCalls) != 0 {
		testInstance.Fatalf("publisher lookups/inserts = %d/%d, want none",
			len(fixture.publishers.calls), len(fixture.store.insertCalls))
	}
}

func TestSubmitCopiesSupportedTypes(testInstance *testing.T) {
	supported := map[string]bool{registry.TypeWebhook: true}
	service := NewService(ServiceConfig{
		Store: &fakeServiceStore{}, Resolver: &fakeResolver{result: []registry.ResolvedDestination{webhookDestination}},
		SupportedTypes: supported,
	})
	delete(supported, registry.TypeWebhook)

	if _, _, err := service.Submit(context.Background(), validEnvelope()); err != nil {
		testInstance.Fatalf("Submit error = %v, want success: the service must hold its own copy", err)
	}
}

func TestSubmitPropagatesStoreErrors(testInstance *testing.T) {
	storeErr := errors.New("boom")
	findFixture := newServiceFixture()
	findFixture.store.getByKeyErr = storeErr
	if _, _, err := findFixture.service.Submit(context.Background(), validEnvelope()); !errors.Is(err, storeErr) {
		testInstance.Fatalf("find error = %v, want %v", err, storeErr)
	}

	insertFixture := newServiceFixture()
	insertFixture.store.insertErr = storeErr
	if _, _, err := insertFixture.service.Submit(context.Background(), validEnvelope()); !errors.Is(err, storeErr) {
		testInstance.Fatalf("insert error = %v, want %v", err, storeErr)
	}
	if insertFixture.wakes.calls() != 0 {
		testInstance.Fatalf("Wake calls = %d after a failed insert, want 0", insertFixture.wakes.calls())
	}
}

func messageFor(deliveries ...Delivery) StoredMessage {
	return StoredMessage{
		ID:         "33333333-3333-3333-3333-333333333333",
		Envelope:   Envelope{AppID: "urls4irl", Channel: "alerts", IdempotencyKey: "key-1"},
		Deliveries: deliveries,
	}
}

func TestGetIsScopedToTheCallingApp(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}
	id := "33333333-3333-3333-3333-333333333333"

	stored, err := fixture.service.Get(context.Background(), "urls4irl", id)
	if err != nil || stored.ID != id {
		testInstance.Fatalf("Get own = (%+v, %v), want the message", stored, err)
	}

	stored, err = fixture.service.Get(context.Background(), "other_app", id)
	if !errors.Is(err, ErrMessageNotFound) {
		testInstance.Fatalf("Get another app's message error = %v, want ErrMessageNotFound", err)
	}
	if !reflect.DeepEqual(stored, StoredMessage{}) {
		testInstance.Fatalf("Get another app's message leaked %+v", stored)
	}
}

func TestGetRejectsMalformedAppIDBeforeTheStore(testInstance *testing.T) {
	fixture := newServiceFixture()
	_, err := fixture.service.Get(context.Background(), "Bad App", "33333333-3333-3333-3333-333333333333")
	wantValidationMessage(testInstance, err, "invalid app_id")
	if fixture.store.getCalls != 0 {
		testInstance.Fatalf("GetMessage calls = %d, want 0", fixture.store.getCalls)
	}
}

func TestGetPropagatesNotFound(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getErr = ErrMessageNotFound
	if _, err := fixture.service.Get(context.Background(), "urls4irl", "x"); !errors.Is(err, ErrMessageNotFound) {
		testInstance.Fatalf("error = %v, want ErrMessageNotFound", err)
	}
}

func TestGetByKeyRejectsMalformedInputsBeforeTheStore(testInstance *testing.T) {
	testCases := []struct {
		name        string
		appID       string
		key         string
		wantMessage string
	}{
		{name: "malformed app_id", appID: "Bad App", key: "key-1", wantMessage: "invalid app_id"},
		{name: "empty app_id", appID: "", key: "key-1", wantMessage: "invalid app_id"},
		{name: "malformed key", appID: "urls4irl", key: "has space", wantMessage: "invalid idempotency_key"},
		{name: "empty key", appID: "urls4irl", key: "", wantMessage: "invalid idempotency_key"},
		{name: "NUL key", appID: "urls4irl", key: "a\x00b", wantMessage: "invalid idempotency_key"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newServiceFixture()
			_, err := fixture.service.GetByKey(context.Background(), testCase.appID, testCase.key)
			wantValidationMessage(subTest, err, testCase.wantMessage)
			if fixture.store.getByKeyCalls != 0 {
				subTest.Fatalf("GetMessageByKey calls = %d, want 0", fixture.store.getByKeyCalls)
			}
		})
	}
}

func TestGetByKeyReachesTheStoreWhenValid(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getByKeyResult = messageFor()
	fixture.store.getByKeyPresent = true
	stored, err := fixture.service.GetByKey(context.Background(), "urls4irl", "key-1")
	if err != nil || stored.Envelope.AppID != "urls4irl" || fixture.store.getByKeyCalls != 1 {
		testInstance.Fatalf("GetByKey = (%+v, %v), calls %d", stored, err, fixture.store.getByKeyCalls)
	}

	fixture.store.getByKeyErr = ErrMessageNotFound
	if _, err = fixture.service.GetByKey(context.Background(), "urls4irl", "key-1"); !errors.Is(err, ErrMessageNotFound) {
		testInstance.Fatalf("error = %v, want ErrMessageNotFound", err)
	}
}

func TestAwaitReturnsEarlyOnceAttempted(testInstance *testing.T) {
	fixture := newServiceFixture()
	pending := messageFor(Delivery{State: StatePending})
	attempted := messageFor(Delivery{State: StatePending, AttemptCount: 1})
	fixture.store.getResults = []StoredMessage{pending, pending, attempted}

	started := time.Now()
	stored, err := fixture.service.Await(context.Background(), "urls4irl", "id", 5*time.Second)
	if err != nil {
		testInstance.Fatalf("Await error = %v", err)
	}
	if stored.Deliveries[0].AttemptCount != 1 {
		testInstance.Fatalf("Await returned %+v, want the attempted read", stored.Deliveries[0])
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		testInstance.Fatalf("Await took %v, want an early return", elapsed)
	}
	if fixture.store.getCalls != 3 {
		testInstance.Fatalf("GetMessage calls = %d, want 3", fixture.store.getCalls)
	}
}

func TestAwaitReturnsWhenNoLongerPending(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StateDead})}

	if _, err := fixture.service.Await(context.Background(), "urls4irl", "id", 5*time.Second); err != nil {
		testInstance.Fatalf("Await error = %v", err)
	}
	if fixture.store.getCalls != 1 {
		testInstance.Fatalf("GetMessage calls = %d, want 1 (already settled)", fixture.store.getCalls)
	}
}

func TestAwaitWaitsForEveryDelivery(testInstance *testing.T) {
	fixture := newServiceFixture()
	oneDone := messageFor(Delivery{State: StateDelivered, AttemptCount: 1}, Delivery{State: StatePending})
	bothDone := messageFor(Delivery{State: StateDelivered, AttemptCount: 1}, Delivery{State: StatePending, AttemptCount: 1})
	fixture.store.getResults = []StoredMessage{oneDone, bothDone}

	if _, err := fixture.service.Await(context.Background(), "urls4irl", "id", 5*time.Second); err != nil {
		testInstance.Fatalf("Await error = %v", err)
	}
	if fixture.store.getCalls != 2 {
		testInstance.Fatalf("GetMessage calls = %d, want 2", fixture.store.getCalls)
	}
}

func TestAwaitReturnsLastReadAtTheTimeout(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}

	started := time.Now()
	stored, err := fixture.service.Await(context.Background(), "urls4irl", "id", 30*time.Millisecond)
	if err != nil {
		testInstance.Fatalf("Await error = %v", err)
	}
	if stored.Deliveries[0].State != StatePending {
		testInstance.Fatalf("Await returned %+v, want the still-pending read", stored.Deliveries[0])
	}
	if elapsed := time.Since(started); elapsed < 30*time.Millisecond {
		testInstance.Fatalf("Await returned after %v, want at least the 30ms timeout", elapsed)
	}
}

func TestAwaitNonPositiveTimeoutReadsOnce(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}

	if _, err := fixture.service.Await(context.Background(), "urls4irl", "id", 0); err != nil {
		testInstance.Fatalf("Await error = %v", err)
	}
	if fixture.store.getCalls != 1 {
		testInstance.Fatalf("GetMessage calls = %d, want 1", fixture.store.getCalls)
	}
}

func TestAwaitIsScopedToTheCallingApp(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}

	_, err := fixture.service.Await(context.Background(), "other_app", "id", time.Second)
	if !errors.Is(err, ErrMessageNotFound) {
		testInstance.Fatalf("error = %v, want ErrMessageNotFound", err)
	}
}

func TestAwaitReturnsLastReadWithErrorOnContextCancel(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stored, err := fixture.service.Await(ctx, "urls4irl", "id", 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		testInstance.Fatalf("error = %v, want context.Canceled", err)
	}
	if stored.ID == "" {
		testInstance.Fatalf("Await returned no last read alongside the cancel error")
	}
}

func TestAwaitReturnsLastReadWithErrorWhenAPollFails(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}
	failingStore := &getFailsAfterFirstStore{fakeServiceStore: fixture.store}
	service := NewService(ServiceConfig{Store: failingStore, Resolver: fixture.resolver, PollInterval: time.Millisecond})

	stored, err := service.Await(context.Background(), "urls4irl", "id", 5*time.Second)
	if !errors.Is(err, ErrMessageNotFound) {
		testInstance.Fatalf("error = %v, want ErrMessageNotFound", err)
	}
	if stored.ID == "" {
		testInstance.Fatalf("Await returned no last read alongside the poll error")
	}
}

// getFailsAfterFirstStore serves the first GetMessage and then reports the
// message deleted (as a retention sweep would).
type getFailsAfterFirstStore struct {
	*fakeServiceStore
}

func (store *getFailsAfterFirstStore) GetMessage(ctx context.Context, id string) (StoredMessage, error) {
	if store.getCalls >= 1 {
		return StoredMessage{}, ErrMessageNotFound
	}
	return store.fakeServiceStore.GetMessage(ctx, id)
}

func (fake *fakeServiceStore) getCallCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.getCalls
}

// TestAwaitReadsOnceMoreAtTheDeadline pins that the result is never staler than
// the deadline: the poll interval is an hour, so only the first read and the
// final deadline read happen, and the second one is what comes back.
func TestAwaitReadsOnceMoreAtTheDeadline(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{
		messageFor(Delivery{State: StatePending}),
		messageFor(Delivery{State: StatePending, AttemptCount: 1}),
	}
	service := NewService(ServiceConfig{Store: fixture.store, Resolver: fixture.resolver, PollInterval: time.Hour})

	stored, err := service.Await(context.Background(), "urls4irl", "id", 20*time.Millisecond)
	if err != nil {
		testInstance.Fatalf("Await error = %v", err)
	}
	if stored.Deliveries[0].AttemptCount != 1 {
		testInstance.Fatalf("Await returned %+v, want the final deadline read", stored.Deliveries[0])
	}
	if calls := fixture.store.getCallCount(); calls != 2 {
		testInstance.Fatalf("GetMessage calls = %d, want 2 (first read + deadline read)", calls)
	}
}

func TestAwaitReturnsLastGoodReadWhenTheDeadlineReadFails(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}
	failingStore := &getFailsAfterFirstStore{fakeServiceStore: fixture.store}
	service := NewService(ServiceConfig{Store: failingStore, Resolver: fixture.resolver, PollInterval: time.Hour})

	stored, err := service.Await(context.Background(), "urls4irl", "id", 20*time.Millisecond)
	if !errors.Is(err, ErrMessageNotFound) {
		testInstance.Fatalf("error = %v, want ErrMessageNotFound", err)
	}
	if stored.ID == "" {
		testInstance.Fatalf("Await returned no last good read alongside the deadline-read error")
	}
}

// TestAwaitBoundsConcurrentPollers proves a second Await, finding every poll
// slot taken, returns its first read at once without polling, and that the slot
// is released when the first one ends.
func TestAwaitBoundsConcurrentPollers(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.getResults = []StoredMessage{messageFor(Delivery{State: StatePending})}
	// An hour-long poll interval keeps the first Await parked in its select, so
	// the store read count is stable and the test needs no timing assumptions.
	service := NewService(ServiceConfig{
		Store: fixture.store, Resolver: fixture.resolver, PollInterval: time.Hour, MaxConcurrentAwaits: 1,
	})

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Await(firstCtx, "urls4irl", "id", time.Hour)
		firstDone <- err
	}()
	waitForCondition(testInstance, "the first Await to take the poll slot", func() bool {
		return len(service.awaitSlots) == 1
	})

	callsBefore := fixture.store.getCallCount()
	secondDone := make(chan error, 1)
	go func() {
		stored, err := service.Await(context.Background(), "urls4irl", "id", time.Hour)
		if err == nil && stored.ID == "" {
			err = errors.New("second Await returned no read")
		}
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			testInstance.Fatalf("second Await error = %v, want its first read with a nil error", err)
		}
	case <-time.After(10 * time.Second):
		testInstance.Fatalf("second Await did not return while every poll slot was taken")
	}
	if calls := fixture.store.getCallCount() - callsBefore; calls != 1 {
		testInstance.Fatalf("second Await made %d store reads, want exactly its first read", calls)
	}

	cancelFirst()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			testInstance.Fatalf("first Await error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		testInstance.Fatalf("first Await did not return after its context was cancelled")
	}
	if held := len(service.awaitSlots); held != 0 {
		testInstance.Fatalf("poll slots held = %d after Await returned, want 0", held)
	}
}

// waitForCondition polls condition until it holds, failing the test after a
// generous bound.
func waitForCondition(testInstance *testing.T, description string, condition func() bool) {
	testInstance.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			testInstance.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNewServiceRequiresStoreAndResolver(testInstance *testing.T) {
	testCases := []struct {
		name   string
		config ServiceConfig
	}{
		{name: "nil store", config: ServiceConfig{Resolver: &fakeResolver{}}},
		{name: "nil resolver", config: ServiceConfig{Store: &fakeServiceStore{}}},
		{name: "both nil", config: ServiceConfig{}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			defer func() {
				if recover() == nil {
					subTest.Fatalf("NewService did not panic")
				}
			}()
			NewService(testCase.config)
		})
	}
}

func TestListDeliveriesValidatesStateAndLimit(testInstance *testing.T) {
	testCases := []struct {
		name        string
		state       string
		limit       int
		wantMessage string
	}{
		{name: "bogus state", state: "bogus", limit: 10, wantMessage: "invalid state"},
		{name: "empty state", state: "", limit: 10, wantMessage: "invalid state"},
		{name: "uppercase state", state: "DEAD", limit: 10, wantMessage: "invalid state"},
		{name: "zero limit", state: StateDead, limit: 0, wantMessage: "invalid limit"},
		{name: "negative limit", state: StateDead, limit: -1, wantMessage: "invalid limit"},
		{name: "limit one over", state: StateDead, limit: MaxListLimit + 1, wantMessage: "invalid limit"},
		{name: "state checked before limit", state: "bogus", limit: 0, wantMessage: "invalid state"},
		{name: "minimum limit", state: StatePending, limit: 1},
		{name: "maximum limit", state: StateDelivered, limit: MaxListLimit},
		{name: "default limit", state: StateDead, limit: DefaultListLimit},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newServiceFixture()
			deliveries, err := fixture.service.ListDeliveries(context.Background(), testCase.state, testCase.limit)
			wantValidationMessage(subTest, err, testCase.wantMessage)
			if testCase.wantMessage != "" {
				if len(fixture.store.listCalls) != 0 {
					subTest.Fatalf("store ListDeliveries calls = %d, want 0", len(fixture.store.listCalls))
				}
				return
			}
			if deliveries == nil {
				subTest.Fatalf("deliveries = nil, want a non-nil slice")
			}
			wantCalls := []listCall{{state: testCase.state, limit: testCase.limit}}
			if !reflect.DeepEqual(fixture.store.listCalls, wantCalls) {
				subTest.Fatalf("store calls = %+v, want %+v", fixture.store.listCalls, wantCalls)
			}
		})
	}
}

func TestListDeliveriesAndAttemptsNeverReturnNil(testInstance *testing.T) {
	fixture := newServiceFixture()
	deliveries, err := fixture.service.ListDeliveries(context.Background(), StateDead, 10)
	if err != nil || deliveries == nil || len(deliveries) != 0 {
		testInstance.Fatalf("ListDeliveries = (%v, %v), want an empty non-nil slice", deliveries, err)
	}
	attempts, err := fixture.service.ListAttempts(context.Background(), 1)
	if err != nil || attempts == nil || len(attempts) != 0 {
		testInstance.Fatalf("ListAttempts = (%v, %v), want an empty non-nil slice", attempts, err)
	}
}

func TestListAttemptsPropagatesNotFound(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixture.store.attemptsErr = ErrDeliveryNotFound
	if _, err := fixture.service.ListAttempts(context.Background(), 9); !errors.Is(err, ErrDeliveryNotFound) {
		testInstance.Fatalf("error = %v, want ErrDeliveryNotFound", err)
	}
}

func TestReplayWakesOnceWithTheServiceClock(testInstance *testing.T) {
	fixture := newServiceFixture()
	fixed := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return fixed }
	fixture.store.replayResult = Delivery{ID: 9, State: StatePending}

	replayed, err := fixture.service.Replay(context.Background(), 9)
	if err != nil || replayed.ID != 9 {
		testInstance.Fatalf("Replay = (%+v, %v)", replayed, err)
	}
	wantCalls := []replayCall{{deliveryID: 9, now: fixed}}
	if !reflect.DeepEqual(fixture.store.replayCalls, wantCalls) {
		testInstance.Fatalf("store calls = %+v, want %+v", fixture.store.replayCalls, wantCalls)
	}
	if fixture.wakes.calls() != 1 {
		testInstance.Fatalf("Wake calls = %d, want 1", fixture.wakes.calls())
	}
}

func TestReplayFailureDoesNotWake(testInstance *testing.T) {
	for _, replayErr := range []error{ErrDeliveryNotFound, ErrDeliveryNotDead, errors.New("boom")} {
		fixture := newServiceFixture()
		fixture.store.replayErr = replayErr
		if _, err := fixture.service.Replay(context.Background(), 9); !errors.Is(err, replayErr) {
			testInstance.Fatalf("error = %v, want %v", err, replayErr)
		}
		if fixture.wakes.calls() != 0 {
			testInstance.Fatalf("Wake calls = %d after %v, want 0", fixture.wakes.calls(), replayErr)
		}
	}
}

func TestNewServiceDefaults(testInstance *testing.T) {
	service := NewService(ServiceConfig{Store: &fakeServiceStore{}, Resolver: &fakeResolver{}})
	if service.pollInterval != 100*time.Millisecond {
		testInstance.Fatalf("pollInterval = %v, want 100ms", service.pollInterval)
	}
	service.wake() // the default must be a callable no-op
	if service.logger == nil || service.now == nil {
		testInstance.Fatalf("logger/now defaults missing")
	}
}

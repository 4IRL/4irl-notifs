package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// lockedBuffer is a bytes.Buffer safe for a slog handler writing from several
// goroutines.
type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (locked *lockedBuffer) Write(data []byte) (int, error) {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.Write(data)
}

func (locked *lockedBuffer) String() string {
	locked.mutex.Lock()
	defer locked.mutex.Unlock()
	return locked.buffer.String()
}

// fakeStore implements the Store methods the service tests use; calling any
// other method panics on the nil embedded interface.
type fakeStore struct {
	Store
	mutex sync.Mutex

	advanceErr  error
	listErr     error
	clearErr    error
	pending     []StoredCheck
	advancedAt  []time.Time
	cleared     []clearCall
	pingCalls   []pingCall
	createCalls []time.Time

	lifecycleErr   error
	lifecycleCalls []lifecycleCall
}

type lifecycleCall struct {
	op  string
	id  int64
	now time.Time
}

type clearCall struct {
	id  int64
	seq int
}

type pingCall struct {
	appID string
	name  string
	kind  string
	now   time.Time
}

func (store *fakeStore) AdvanceDue(_ context.Context, now time.Time) ([]StoredCheck, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.advancedAt = append(store.advancedAt, now)
	return nil, store.advanceErr
}

func (store *fakeStore) ListPendingAlerts(context.Context) ([]StoredCheck, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.listErr != nil {
		return nil, store.listErr
	}
	return append([]StoredCheck(nil), store.pending...), nil
}

func (store *fakeStore) ClearPendingAlert(_ context.Context, id int64, alertSeq int) (bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.cleared = append(store.cleared, clearCall{id: id, seq: alertSeq})
	return store.clearErr == nil, store.clearErr
}

func (store *fakeStore) RecordPing(_ context.Context, appID string, name string, kind string, now time.Time) (StoredCheck, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.pingCalls = append(store.pingCalls, pingCall{appID: appID, name: name, kind: kind, now: now})
	return StoredCheck{AppID: appID, Name: name}, nil
}

func (store *fakeStore) Create(_ context.Context, check NewCheck, now time.Time) (StoredCheck, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.createCalls = append(store.createCalls, now)
	return StoredCheck{AppID: check.AppID, Name: check.Name}, nil
}

func (store *fakeStore) Patch(_ context.Context, id int64, _ CheckPatch, now time.Time) (StoredCheck, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.lifecycleCalls = append(store.lifecycleCalls, lifecycleCall{op: "patch", id: id, now: now})
	return StoredCheck{ID: id}, store.lifecycleErr
}

func (store *fakeStore) Pause(_ context.Context, id int64, now time.Time) (StoredCheck, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.lifecycleCalls = append(store.lifecycleCalls, lifecycleCall{op: "pause", id: id, now: now})
	return StoredCheck{ID: id}, store.lifecycleErr
}

func (store *fakeStore) Resume(_ context.Context, id int64, now time.Time) (StoredCheck, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.lifecycleCalls = append(store.lifecycleCalls, lifecycleCall{op: "resume", id: id, now: now})
	return StoredCheck{ID: id}, store.lifecycleErr
}

func (store *fakeStore) Get(_ context.Context, id int64) (StoredCheck, error) {
	return StoredCheck{ID: id}, store.lifecycleErr
}

func (store *fakeStore) List(context.Context) ([]StoredCheck, error) {
	return []StoredCheck{{ID: 1}}, store.lifecycleErr
}

func (store *fakeStore) Delete(context.Context, int64) (bool, error) {
	return store.lifecycleErr == nil, store.lifecycleErr
}

func (store *fakeStore) clearedCalls() []clearCall {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return append([]clearCall(nil), store.cleared...)
}

// fakeMessages records every Submit and answers from a per-destination error.
// A successful Submit is deduplicated by idempotency key, like the real message
// service, so `deliveries` holds each key that was actually delivered once.
type fakeMessages struct {
	mutex     sync.Mutex
	envelopes []message.Envelope
	errByDest map[string]error
	delivered map[string]bool
	// deliveries lists the idempotency keys of the first successful Submit of each.
	deliveries []string
}

func (fake *fakeMessages) Submit(_ context.Context, envelope message.Envelope) (message.StoredMessage, message.SubmitOutcome, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.envelopes = append(fake.envelopes, envelope)
	if submitErr := fake.errByDest[envelope.ToDestination]; submitErr != nil {
		return message.StoredMessage{}, message.SubmitOutcome{}, submitErr
	}
	if fake.delivered == nil {
		fake.delivered = map[string]bool{}
	}
	if !fake.delivered[envelope.IdempotencyKey] {
		fake.delivered[envelope.IdempotencyKey] = true
		fake.deliveries = append(fake.deliveries, envelope.IdempotencyKey)
	}
	return message.StoredMessage{}, message.SubmitOutcome{}, nil
}

func (fake *fakeMessages) deliveredKeys() []string {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]string(nil), fake.deliveries...)
}

func (fake *fakeMessages) submitted() []message.Envelope {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]message.Envelope(nil), fake.envelopes...)
}

func (fake *fakeMessages) setError(destination string, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if fake.errByDest == nil {
		fake.errByDest = map[string]error{}
	}
	fake.errByDest[destination] = err
}

var (
	serviceNow    = time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)
	lastPingTime  = time.Date(2026, time.October, 19, 6, 30, 0, 0, time.UTC)
	downSinceTime = time.Date(2026, time.October, 19, 8, 59, 0, 0, time.UTC)
)

// testClock is a settable clock.
type testClock struct {
	mutex   sync.Mutex
	current time.Time
}

func (clock *testClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.current
}

func (clock *testClock) Set(at time.Time) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.current = at
}

// newServiceStartedAt builds a service whose clock reads `start` at
// construction, so its startup hold-off runs from `start` to
// start+StartupHoldOff. The returned clock can then be moved.
func newServiceStartedAt(t *testing.T, store *fakeStore, messages *fakeMessages, start time.Time) (*Service, *testClock, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	clock := &testClock{current: start}
	service := NewService(ServiceConfig{
		Store:    store,
		Messages: messages,
		Now:      clock.Now,
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
	})
	return service, clock, logs
}

// newTestService returns a service that is past its startup hold-off and whose
// clock reads serviceNow.
func newTestService(t *testing.T, store *fakeStore, messages *fakeMessages) (*Service, *lockedBuffer) {
	t.Helper()
	service, clock, logs := newServiceStartedAt(t, store, messages, serviceNow.Add(-StartupHoldOff))
	clock.Set(serviceNow)
	return service, logs
}

func destinations(names ...string) []DestinationRef {
	refs := make([]DestinationRef, 0, len(names))
	for _, name := range names {
		refs = append(refs, DestinationRef{Name: name, Type: registry.TypeNtfy, Enabled: true})
	}
	return refs
}

// pendingCheck is check 7 (billing/nightly, period 600 s, grace 300 s) holding
// the given alert as alert number 3.
func pendingCheck(kind string, destinationNames ...string) StoredCheck {
	pending := kind
	return StoredCheck{
		ID:            7,
		AppID:         "billing",
		Name:          "nightly",
		PeriodSeconds: 600,
		GraceSeconds:  300,
		AlertSeq:      3,
		PendingAlert:  &pending,
		Destinations:  destinations(destinationNames...),
	}
}

func downMissedCheck(destinationNames ...string) StoredCheck {
	check := pendingCheck(AlertDown, destinationNames...)
	check.State = StateDown
	check.DownSince = timePointer(downSinceTime)
	check.DownReason = stringPointer(ReasonMissed)
	check.LastPingAt = timePointer(lastPingTime)
	check.LastPingKind = stringPointer(PingSuccess)
	return check
}

func recoveredCheck(destinationNames ...string) StoredCheck {
	check := pendingCheck(AlertRecovered, destinationNames...)
	check.State = StateUp
	check.DownSince = timePointer(downSinceTime)
	check.DownReason = nil
	check.LastPingAt = timePointer(lastPingTime)
	check.LastPingKind = stringPointer(PingSuccess)
	return check
}

// decodePayload unmarshals an envelope payload into a generic map.
func decodePayload(t *testing.T, envelope message.Envelope) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("payload %s is not a JSON object: %v", envelope.Payload, err)
	}
	return payload
}

func TestTickAdvancesAtTheServiceClock(t *testing.T) {
	store := &fakeStore{}
	service, _ := newTestService(t, store, &fakeMessages{})
	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(store.advancedAt) != 1 || !store.advancedAt[0].Equal(serviceNow) {
		t.Errorf("AdvanceDue calls = %v, want one at %v", store.advancedAt, serviceNow)
	}
}

func TestTickSubmitsDownAlertToEveryDestinationThenClears(t *testing.T) {
	store := &fakeStore{pending: []StoredCheck{downMissedCheck("ops_ntfy", "pager")}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	envelopes := messages.submitted()
	if len(envelopes) != 2 {
		t.Fatalf("submits = %d, want 2", len(envelopes))
	}
	for index, destination := range []string{"ops_ntfy", "pager"} {
		envelope := envelopes[index]
		if envelope.AppID != "billing" || envelope.Channel != "heartbeat" || envelope.ToDestination != destination {
			t.Errorf("envelope %d routing = %q/%q/%q", index, envelope.AppID, envelope.Channel, envelope.ToDestination)
		}
		if want := "hb:7:3:" + destination; envelope.IdempotencyKey != want {
			t.Errorf("envelope %d key = %q, want %q", index, envelope.IdempotencyKey, want)
		}
		if !envelope.Critical {
			t.Errorf("envelope %d critical = false, want true for a down alert", index)
		}
		if envelope.ToRecipient != "" || envelope.DeliverAt != nil {
			t.Errorf("envelope %d must be immediate and destination-addressed: %+v", index, envelope)
		}
		if envelope.Display == nil {
			t.Fatalf("envelope %d has no display", index)
		}
		if envelope.Display.Title != "Heartbeat down: billing/nightly" {
			t.Errorf("title = %q", envelope.Display.Title)
		}
		if want := "No ping since 2026-10-19T06:30:00Z (period 10m0s, grace 5m0s)."; envelope.Display.Body != want {
			t.Errorf("body = %q, want %q", envelope.Display.Body, want)
		}
		if envelope.Display.Priority != "high" || envelope.Display.URL != "" {
			t.Errorf("display = %+v, want priority high and no url", envelope.Display)
		}
		payload := decodePayload(t, envelope)
		want := map[string]any{
			"type": "heartbeat.down", "app_id": "billing", "check": "nightly", "reason": "missed",
			"last_ping_at": "2026-10-19T06:30:00Z", "down_since": "2026-10-19T08:59:00Z",
			"period_seconds": float64(600), "grace_seconds": float64(300),
		}
		if !reflect.DeepEqual(payload, want) {
			t.Errorf("payload = %v, want %v", payload, want)
		}
	}
	if cleared := store.clearedCalls(); len(cleared) != 1 || cleared[0] != (clearCall{id: 7, seq: 3}) {
		t.Errorf("cleared = %v, want exactly [{7 3}]", cleared)
	}
}

func TestTickDownBodyForANeverPingedCheckSaysCreation(t *testing.T) {
	check := downMissedCheck("ops_ntfy")
	check.LastPingAt = nil
	check.LastPingKind = nil
	store := &fakeStore{pending: []StoredCheck{check}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	envelope := messages.submitted()[0]
	if want := "No ping since creation (period 10m0s, grace 5m0s)."; envelope.Display.Body != want {
		t.Errorf("body = %q, want %q", envelope.Display.Body, want)
	}
	if payload := decodePayload(t, envelope); payload["last_ping_at"] != nil {
		t.Errorf("last_ping_at = %v, want null", payload["last_ping_at"])
	}
}

func TestTickDownBodyForAFailPingReportsTheFailure(t *testing.T) {
	check := downMissedCheck("ops_ntfy")
	check.DownReason = stringPointer(ReasonFail)
	check.LastPingKind = stringPointer(PingFail)
	store := &fakeStore{pending: []StoredCheck{check}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	envelope := messages.submitted()[0]
	if want := "Failure reported at 2026-10-19T06:30:00Z."; envelope.Display.Body != want {
		t.Errorf("body = %q, want %q", envelope.Display.Body, want)
	}
	if payload := decodePayload(t, envelope); payload["reason"] != "fail" {
		t.Errorf("reason = %v, want fail", payload["reason"])
	}
}

func TestTickSubmitsRecoveryAlertThenClears(t *testing.T) {
	store := &fakeStore{pending: []StoredCheck{recoveredCheck("ops_ntfy", "pager")}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	envelopes := messages.submitted()
	if len(envelopes) != 2 {
		t.Fatalf("submits = %d, want 2", len(envelopes))
	}
	for index, destination := range []string{"ops_ntfy", "pager"} {
		envelope := envelopes[index]
		if want := "hb:7:3:" + destination; envelope.IdempotencyKey != want {
			t.Errorf("envelope %d key = %q, want %q", index, envelope.IdempotencyKey, want)
		}
		if envelope.Critical {
			t.Errorf("envelope %d critical = true, want false for a recovery", index)
		}
		if envelope.Channel != "heartbeat" || envelope.AppID != "billing" || envelope.ToDestination != destination {
			t.Errorf("envelope %d routing = %q/%q/%q", index, envelope.AppID, envelope.Channel, envelope.ToDestination)
		}
		if envelope.Display.Title != "Heartbeat recovered: billing/nightly" || envelope.Display.Priority != "default" {
			t.Errorf("display = %+v", envelope.Display)
		}
		if want := "Ping received at 2026-10-19T06:30:00Z; down since 2026-10-19T08:59:00Z."; envelope.Display.Body != want {
			t.Errorf("body = %q, want %q", envelope.Display.Body, want)
		}
		payload := decodePayload(t, envelope)
		want := map[string]any{
			"type": "heartbeat.recovered", "app_id": "billing", "check": "nightly", "reason": nil,
			"last_ping_at": "2026-10-19T06:30:00Z", "down_since": "2026-10-19T08:59:00Z",
			"period_seconds": float64(600), "grace_seconds": float64(300),
		}
		if !reflect.DeepEqual(payload, want) {
			t.Errorf("payload = %v, want %v", payload, want)
		}
		if reason, present := payload["reason"]; !present || reason != nil {
			t.Errorf("reason = %v (present %v), want an explicit null", reason, present)
		}
	}
	if cleared := store.clearedCalls(); len(cleared) != 1 || cleared[0] != (clearCall{id: 7, seq: 3}) {
		t.Errorf("cleared = %v, want exactly [{7 3}]", cleared)
	}
}

func TestTickSkipsADisabledDestinationAndStillClears(t *testing.T) {
	check := downMissedCheck("ops_ntfy", "pager")
	check.Destinations[0].Enabled = false
	store := &fakeStore{pending: []StoredCheck{check}}
	messages := &fakeMessages{}
	service, logs := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	envelopes := messages.submitted()
	if len(envelopes) != 1 || envelopes[0].ToDestination != "pager" {
		t.Fatalf("submits = %+v, want only the enabled destination", envelopes)
	}
	if len(store.clearedCalls()) != 1 {
		t.Errorf("the alert must clear once the enabled destination succeeded")
	}
	if !strings.Contains(logs.String(), "ops_ntfy") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("the skipped destination must be logged at warn:\n%s", logs.String())
	}
}

func TestTickSkipsPermanentSubmitRejectionsAndStillClears(t *testing.T) {
	rejections := map[string]error{
		"disabled at submit":       registry.ErrDestinationDisabled,
		"gone at submit":           registry.ErrUnknownDestination,
		"no sink for the type":     message.ErrUnsupportedDestinationType,
		"wrapped no sink for type": fmt.Errorf("resolve: %w", message.ErrUnsupportedDestinationType),
	}
	for name, rejection := range rejections {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{pending: []StoredCheck{downMissedCheck("ops_ntfy", "pager")}}
			messages := &fakeMessages{}
			messages.setError("ops_ntfy", rejection)
			service, logs := newTestService(t, store, messages)

			if err := service.Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v, want nil: a permanent rejection is skipped", err)
			}
			if got := len(messages.submitted()); got != 2 {
				t.Errorf("submits = %d, want 2: the other destination must still be tried", got)
			}
			if len(store.clearedCalls()) != 1 {
				t.Errorf("the alert must clear once every other destination succeeded or was skipped")
			}
			if !strings.Contains(logs.String(), "destination skipped") {
				t.Errorf("the skipped destination must be logged:\n%s", logs.String())
			}
		})
	}
}

func TestTickTransientSubmitErrorKeepsTheAlertPendingAndRetriesWithTheSameKeys(t *testing.T) {
	store := &fakeStore{pending: []StoredCheck{downMissedCheck("ops_ntfy", "pager")}}
	messages := &fakeMessages{}
	messages.setError("pager", errors.New("database unavailable"))
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err == nil {
		t.Fatal("Tick returned nil, want the transient error")
	}
	if cleared := store.clearedCalls(); len(cleared) != 0 {
		t.Fatalf("cleared = %v, want nothing: the alert must stay pending", cleared)
	}
	firstKeys := keysOf(messages.submitted())

	messages.setError("pager", nil)
	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	secondKeys := keysOf(messages.submitted()[len(firstKeys):])
	for _, key := range secondKeys {
		if !slices.Contains(firstKeys, key) {
			t.Errorf("retry key %q was not used on the first attempt (%v): a new key would duplicate delivery", key, firstKeys)
		}
	}
	if want := []string{"hb:7:3:ops_ntfy", "hb:7:3:pager"}; fmt.Sprint(secondKeys) != fmt.Sprint(want) {
		t.Errorf("retry keys = %v, want %v", secondKeys, want)
	}
	if len(store.clearedCalls()) != 1 {
		t.Errorf("the retry must clear the alert once, got %v", store.clearedCalls())
	}
}

func TestTickFailingFirstDestinationDoesNotStopTheSecondAndKeepsTheAlertPending(t *testing.T) {
	store := &fakeStore{pending: []StoredCheck{downMissedCheck("ops_ntfy", "pager")}}
	messages := &fakeMessages{}
	failure := errors.New("ntfy unavailable")
	messages.setError("ops_ntfy", failure)
	service, _ := newTestService(t, store, messages)

	tickErr := service.Tick(context.Background())
	if !errors.Is(tickErr, failure) {
		t.Fatalf("Tick = %v, want an error wrapping %v", tickErr, failure)
	}
	if !strings.Contains(tickErr.Error(), `"ops_ntfy"`) {
		t.Errorf("Tick error %q should name the failed destination", tickErr)
	}
	if got := keysOf(messages.submitted()); fmt.Sprint(got) != fmt.Sprint([]string{"hb:7:3:ops_ntfy", "hb:7:3:pager"}) {
		t.Fatalf("submitted keys = %v, want both destinations tried in order", got)
	}
	if got := messages.deliveredKeys(); fmt.Sprint(got) != fmt.Sprint([]string{"hb:7:3:pager"}) {
		t.Errorf("delivered = %v, want only the second destination", got)
	}
	if cleared := store.clearedCalls(); len(cleared) != 0 {
		t.Fatalf("cleared = %v, want nothing: the alert must stay pending", cleared)
	}

	// The failure resolves: the next tick resubmits both with the same keys, the
	// already-delivered one is deduplicated, and the alert clears.
	messages.setError("ops_ntfy", nil)
	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if got := messages.deliveredKeys(); fmt.Sprint(got) != fmt.Sprint([]string{"hb:7:3:pager", "hb:7:3:ops_ntfy"}) {
		t.Errorf("delivered = %v, want each destination exactly once, the missing one added", got)
	}
	if cleared := store.clearedCalls(); len(cleared) != 1 || cleared[0] != (clearCall{id: 7, seq: 3}) {
		t.Errorf("cleared = %v, want exactly [{7 3}]", cleared)
	}
}

func TestTickJoinsEveryDestinationFailureOfACheck(t *testing.T) {
	store := &fakeStore{pending: []StoredCheck{downMissedCheck("ops_ntfy", "pager")}}
	messages := &fakeMessages{}
	firstFailure, secondFailure := errors.New("first down"), errors.New("second down")
	messages.setError("ops_ntfy", firstFailure)
	messages.setError("pager", secondFailure)
	service, _ := newTestService(t, store, messages)

	tickErr := service.Tick(context.Background())
	if !errors.Is(tickErr, firstFailure) || !errors.Is(tickErr, secondFailure) {
		t.Fatalf("Tick = %v, want both destination errors joined", tickErr)
	}
	if len(store.clearedCalls()) != 0 {
		t.Errorf("cleared = %v, want nothing", store.clearedCalls())
	}
}

func TestTickHoldsOffAdvancingUntilOneGraceWindowAfterStart(t *testing.T) {
	if StartupHoldOff != time.Duration(MinGraceSeconds)*time.Second {
		t.Fatalf("StartupHoldOff = %v, want one grace window (%d s)", StartupHoldOff, MinGraceSeconds)
	}
	start := serviceNow
	holdOffEnd := start.Add(StartupHoldOff)
	store := &fakeStore{}
	service, clock, _ := newServiceStartedAt(t, store, &fakeMessages{}, start)

	for _, before := range []time.Time{start, start.Add(StartupHoldOff / 2), holdOffEnd.Add(-time.Nanosecond)} {
		clock.Set(before)
		if err := service.Tick(context.Background()); err != nil {
			t.Fatalf("Tick at %v: %v", before, err)
		}
	}
	if len(store.advancedAt) != 0 {
		t.Fatalf("AdvanceDue ran at %v during the hold-off, want no calls", store.advancedAt)
	}

	clock.Set(holdOffEnd)
	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick at the end of the hold-off: %v", err)
	}
	if len(store.advancedAt) != 1 || !store.advancedAt[0].Equal(holdOffEnd) {
		t.Fatalf("AdvanceDue calls = %v, want one at %v (the boundary advances)", store.advancedAt, holdOffEnd)
	}

	clock.Set(holdOffEnd.Add(time.Hour))
	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("later Tick: %v", err)
	}
	if len(store.advancedAt) != 2 {
		t.Errorf("AdvanceDue calls = %v, want it to keep running after the hold-off", store.advancedAt)
	}
}

func TestTickStillFlushesPendingAlertsDuringTheHoldOff(t *testing.T) {
	store := &fakeStore{pending: []StoredCheck{downMissedCheck("ops_ntfy")}}
	messages := &fakeMessages{}
	service, _, _ := newServiceStartedAt(t, store, messages, serviceNow)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(store.advancedAt) != 0 {
		t.Errorf("AdvanceDue ran during the hold-off: %v", store.advancedAt)
	}
	if len(messages.submitted()) != 1 || len(store.clearedCalls()) != 1 {
		t.Errorf("submits = %d, cleared = %v, want the already-pending alert flushed", len(messages.submitted()), store.clearedCalls())
	}
}

func TestTickHoldOffStoreListErrorsStillReturn(t *testing.T) {
	storeErr := errors.New("store unavailable")
	store := &fakeStore{listErr: storeErr}
	service, _, _ := newServiceStartedAt(t, store, &fakeMessages{}, serviceNow)
	if err := service.Tick(context.Background()); !errors.Is(err, storeErr) {
		t.Fatalf("Tick = %v, want %v", err, storeErr)
	}
}

func TestTickOneChecksFailureDoesNotStopTheNext(t *testing.T) {
	failing := downMissedCheck("broken")
	failing.ID = 7
	healthy := recoveredCheck("pager")
	healthy.ID = 8
	store := &fakeStore{pending: []StoredCheck{failing, healthy}}
	messages := &fakeMessages{}
	messages.setError("broken", errors.New("boom"))
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err == nil {
		t.Fatal("Tick returned nil, want the flush error")
	}
	cleared := store.clearedCalls()
	if len(cleared) != 1 || cleared[0].id != 8 {
		t.Errorf("cleared = %v, want only the healthy check (8)", cleared)
	}
}

func TestTickAllDestinationsDisabledStillClears(t *testing.T) {
	check := downMissedCheck("ops_ntfy")
	check.Destinations[0].Enabled = false
	store := &fakeStore{pending: []StoredCheck{check}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(messages.submitted()) != 0 || len(store.clearedCalls()) != 1 {
		t.Errorf("submits = %d, cleared = %v, want none and one clear", len(messages.submitted()), store.clearedCalls())
	}
}

func TestTickUnknownPendingKindIsNotSubmittedOrCleared(t *testing.T) {
	store := &fakeStore{pending: []StoredCheck{pendingCheck("mystery", "ops_ntfy")}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err == nil {
		t.Fatal("Tick returned nil, want an error for an unknown alert kind")
	}
	if len(messages.submitted()) != 0 || len(store.clearedCalls()) != 0 {
		t.Errorf("an unknown kind must fail closed: submits %d, cleared %v", len(messages.submitted()), store.clearedCalls())
	}
}

func TestTickStoreErrorsAreReturned(t *testing.T) {
	storeErr := errors.New("store unavailable")
	t.Run("advance", func(t *testing.T) {
		store := &fakeStore{advanceErr: storeErr, pending: []StoredCheck{downMissedCheck("ops_ntfy")}}
		messages := &fakeMessages{}
		service, _ := newTestService(t, store, messages)
		if err := service.Tick(context.Background()); !errors.Is(err, storeErr) {
			t.Fatalf("Tick = %v, want %v", err, storeErr)
		}
		if len(messages.submitted()) != 0 {
			t.Error("a failed AdvanceDue must not flush")
		}
	})
	t.Run("list", func(t *testing.T) {
		store := &fakeStore{listErr: storeErr}
		service, _ := newTestService(t, store, &fakeMessages{})
		if err := service.Tick(context.Background()); !errors.Is(err, storeErr) {
			t.Fatalf("Tick = %v, want %v", err, storeErr)
		}
	})
	t.Run("clear", func(t *testing.T) {
		store := &fakeStore{clearErr: storeErr, pending: []StoredCheck{downMissedCheck("ops_ntfy")}}
		service, _ := newTestService(t, store, &fakeMessages{})
		if err := service.Tick(context.Background()); !errors.Is(err, storeErr) {
			t.Fatalf("Tick = %v, want %v", err, storeErr)
		}
	})
}

func TestNewServicePanicsWithoutItsDependencies(t *testing.T) {
	assertPanics := func(name string, config ServiceConfig) {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewService did not panic")
				}
			}()
			NewService(config)
		})
	}
	assertPanics("nil store", ServiceConfig{Messages: &fakeMessages{}})
	assertPanics("nil messages", ServiceConfig{Store: &fakeStore{}})
}

func TestPingAndFailRecordTheMatchingKindAtTheCallTime(t *testing.T) {
	store := &fakeStore{}
	service, _ := newTestService(t, store, &fakeMessages{})

	if _, err := service.Ping(context.Background(), "billing", "nightly"); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if _, err := service.Fail(context.Background(), "billing", "nightly"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	want := []pingCall{
		{appID: "billing", name: "nightly", kind: PingSuccess, now: serviceNow},
		{appID: "billing", name: "nightly", kind: PingFail, now: serviceNow},
	}
	if fmt.Sprint(store.pingCalls) != fmt.Sprint(want) {
		t.Errorf("ping calls = %v, want %v", store.pingCalls, want)
	}
}

func TestPingRejectsInvalidNamesBeforeTouchingTheStore(t *testing.T) {
	store := &fakeStore{}
	service, _ := newTestService(t, store, &fakeMessages{})

	_, appErr := service.Ping(context.Background(), "Bad App", "nightly")
	assertValidationMessage(t, appErr, "invalid app_id")
	_, nameErr := service.Fail(context.Background(), "billing", "bad-name")
	assertValidationMessage(t, nameErr, "invalid name")
	if len(store.pingCalls) != 0 {
		t.Errorf("store pings = %d, want 0", len(store.pingCalls))
	}
}

func TestCreateValidatesThenStoresAtTheServiceClock(t *testing.T) {
	store := &fakeStore{}
	service, _ := newTestService(t, store, &fakeMessages{})

	_, invalidErr := service.Create(context.Background(), NewCheck{
		AppID: "billing", Name: "nightly", PeriodSeconds: 5, GraceSeconds: 300, DestinationNames: []string{"ops_ntfy"},
	})
	assertValidationMessage(t, invalidErr, "invalid period_seconds")
	if len(store.createCalls) != 0 {
		t.Fatal("an invalid check reached the store")
	}

	if _, err := service.Create(context.Background(), NewCheck{
		AppID: "billing", Name: "nightly", PeriodSeconds: 600, GraceSeconds: 300, DestinationNames: []string{"ops_ntfy"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(store.createCalls) != 1 || !store.createCalls[0].Equal(serviceNow) {
		t.Errorf("create calls = %v, want one at %v", store.createCalls, serviceNow)
	}
}

func TestPauseAndResumeUseTheServiceClock(t *testing.T) {
	store := &fakeStore{}
	service, _ := newTestService(t, store, &fakeMessages{})
	ctx := context.Background()

	if _, err := service.Pause(ctx, 4); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := service.Resume(ctx, 4); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	want := []lifecycleCall{
		{op: "pause", id: 4, now: serviceNow}, {op: "resume", id: 4, now: serviceNow},
	}
	if !reflect.DeepEqual(store.lifecycleCalls, want) {
		t.Errorf("calls = %v, want %v", store.lifecycleCalls, want)
	}
}

func TestPatchUsesTheServiceClockAndLeavesAnEmptyPatchAsARead(t *testing.T) {
	store := &fakeStore{}
	service, _ := newTestService(t, store, &fakeMessages{})
	ctx := context.Background()
	period := 600

	if _, err := service.Patch(ctx, 4, CheckPatch{PeriodSeconds: &period}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if _, err := service.Patch(ctx, 4, CheckPatch{}); err != nil {
		t.Fatalf("empty Patch: %v", err)
	}
	want := []lifecycleCall{{op: "patch", id: 4, now: serviceNow}}
	if !reflect.DeepEqual(store.lifecycleCalls, want) {
		t.Errorf("calls = %v, want only the non-empty patch to reach the store: %v", store.lifecycleCalls, want)
	}
}

func TestApplyPatchKeepsEveryFieldThePatchLeavesNil(t *testing.T) {
	current := StoredCheck{
		PeriodSeconds: 3600, GraceSeconds: 300,
		Destinations: []DestinationRef{{Name: "ops"}, {Name: "pager"}},
	}
	period, grace := 120, 60
	testCases := []struct {
		name  string
		patch CheckPatch
		want  CheckUpdate
	}{
		{"nothing", CheckPatch{}, CheckUpdate{PeriodSeconds: 3600, GraceSeconds: 300, DestinationNames: []string{"ops", "pager"}}},
		{"period", CheckPatch{PeriodSeconds: &period}, CheckUpdate{PeriodSeconds: 120, GraceSeconds: 300, DestinationNames: []string{"ops", "pager"}}},
		{"grace", CheckPatch{GraceSeconds: &grace}, CheckUpdate{PeriodSeconds: 3600, GraceSeconds: 60, DestinationNames: []string{"ops", "pager"}}},
		{"destinations", CheckPatch{DestinationNames: []string{"backup"}}, CheckUpdate{PeriodSeconds: 3600, GraceSeconds: 300, DestinationNames: []string{"backup"}}},
		{"everything", CheckPatch{PeriodSeconds: &period, GraceSeconds: &grace, DestinationNames: []string{"x"}}, CheckUpdate{PeriodSeconds: 120, GraceSeconds: 60, DestinationNames: []string{"x"}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ApplyPatch(current, testCase.patch); !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("ApplyPatch = %+v, want %+v", got, testCase.want)
			}
		})
	}
	if !(CheckPatch{}).IsEmpty() || (CheckPatch{PeriodSeconds: &period}).IsEmpty() ||
		(CheckPatch{DestinationNames: []string{}}).IsEmpty() {
		t.Errorf("IsEmpty must be true only for a patch naming nothing (an empty destination list is named)")
	}
}

func TestReadsAndDeletePassStoreErrorsThrough(t *testing.T) {
	store := &fakeStore{lifecycleErr: ErrCheckNotFound}
	service, _ := newTestService(t, store, &fakeMessages{})
	ctx := context.Background()

	if _, err := service.Get(ctx, 4); !errors.Is(err, ErrCheckNotFound) {
		t.Errorf("Get = %v, want ErrCheckNotFound", err)
	}
	if _, err := service.List(ctx); !errors.Is(err, ErrCheckNotFound) {
		t.Errorf("List = %v, want the store error", err)
	}
	if _, err := service.Delete(ctx, 4); !errors.Is(err, ErrCheckNotFound) {
		t.Errorf("Delete = %v, want the store error", err)
	}
	if _, err := service.Pause(ctx, 4); !errors.Is(err, ErrCheckNotFound) {
		t.Errorf("Pause = %v, want ErrCheckNotFound", err)
	}
}

func TestTickRendersAnAlertWhoseTimestampsWereNotRecorded(t *testing.T) {
	check := recoveredCheck("ops_ntfy")
	check.LastPingAt = nil
	check.DownSince = nil
	store := &fakeStore{pending: []StoredCheck{check}}
	messages := &fakeMessages{}
	service, _ := newTestService(t, store, messages)

	if err := service.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if want := "Ping received at unknown; down since unknown."; messages.submitted()[0].Display.Body != want {
		t.Errorf("body = %q, want %q", messages.submitted()[0].Display.Body, want)
	}
}

func keysOf(envelopes []message.Envelope) []string {
	keys := make([]string, 0, len(envelopes))
	for _, envelope := range envelopes {
		keys = append(keys, envelope.IdempotencyKey)
	}
	return keys
}

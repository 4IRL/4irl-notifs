package message

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// pairResolver is a Resolver scripted per (app, channel) pair.
type pairResolver struct {
	mutex   sync.Mutex
	results map[HeldPair][]registry.ResolvedDestination
	errs    map[HeldPair]error
	calls   []HeldPair
}

func (fake *pairResolver) Resolve(_ context.Context, appID, _, channel string) ([]registry.ResolvedDestination, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	pair := HeldPair{AppID: appID, Channel: channel}
	fake.calls = append(fake.calls, pair)
	if err := fake.errs[pair]; err != nil {
		return nil, err
	}
	return fake.results[pair], nil
}

var (
	ntfyDestination    = registry.ResolvedDestination{ID: 3, Name: "ops_ntfy", Type: registry.TypeNtfy}
	releasePair        = HeldPair{AppID: "app_one", Channel: "alerts"}
	releaseNow         = time.Date(2026, time.November, 7, 12, 0, 0, 0, time.UTC)
	releaseSecretTitle = "secret-display-title"
)

type releaseHarness struct {
	service  *Service
	store    *fakeServiceStore
	resolver *pairResolver
	logs     *bytes.Buffer
	wakes    *wakeCounter
}

func newReleaseHarness() *releaseHarness {
	store := &fakeServiceStore{}
	resolver := &pairResolver{
		results: map[HeldPair][]registry.ResolvedDestination{releasePair: {webhookDestination}},
		errs:    map[HeldPair]error{},
	}
	logs := &bytes.Buffer{}
	wakes := &wakeCounter{}
	service := NewService(ServiceConfig{
		Store:    store,
		Resolver: resolver,
		SupportedTypes: map[string]bool{
			registry.TypeWebhook: true, registry.TypeNtfy: true,
		},
		Wake:   wakes.wake,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	store.heldPairsPages = [][]HeldPair{{releasePair}}
	return &releaseHarness{service: service, store: store, resolver: resolver, logs: logs, wakes: wakes}
}

func heldCandidate(messageID string, display *Display) StoredMessage {
	heldAt := releaseNow.Add(-time.Hour)
	return StoredMessage{
		ID:       messageID,
		HeldAt:   &heldAt,
		Envelope: Envelope{AppID: releasePair.AppID, Channel: releasePair.Channel, Display: display},
	}
}

func (harness *releaseHarness) lines(level string) []string {
	var matched []string
	for _, line := range strings.Split(harness.logs.String(), "\n") {
		if strings.Contains(line, "level="+level+" ") {
			matched = append(matched, line)
		}
	}
	return matched
}

func (harness *releaseHarness) requireNoWarnOrError(testInstance *testing.T) {
	testInstance.Helper()
	if warns, errs := harness.lines("WARN"), harness.lines("ERROR"); len(warns)+len(errs) != 0 {
		testInstance.Fatalf("unexpected WARN/ERROR lines: %v %v", warns, errs)
	}
}

func TestReleaseHeldClassifiesEachMessage(testInstance *testing.T) {
	notFound := errors.New("database unavailable")
	testCases := []struct {
		name         string
		destination  registry.ResolvedDestination
		display      *Display
		releaseOut   HeldReleaseResult
		releaseErr   error
		failSkipped  bool
		failErr      error
		wantCount    int64
		wantReleases int
		wantFails    int
		wantInfo     string
		wantWarn     bool
		wantError    bool
	}{
		{name: "released", destination: webhookDestination, releaseOut: HeldReleased,
			wantCount: 1, wantReleases: 1, wantInfo: "held messages released"},
		{name: "scheduled counts as released", destination: webhookDestination, releaseOut: HeldScheduled,
			wantCount: 1, wantReleases: 1, wantInfo: "held messages released"},
		{name: "skipped is not counted", destination: webhookDestination, releaseOut: HeldSkipped,
			wantCount: 0, wantReleases: 1},
		{name: "unknown result fails closed", destination: webhookDestination, releaseOut: HeldReleaseResult("bogus"),
			wantCount: 0, wantReleases: 1, wantError: true},
		{name: "store error stays held", destination: webhookDestination, releaseErr: notFound,
			wantCount: 0, wantReleases: 1, wantWarn: true},
		{name: "validation error is terminal", destination: ntfyDestination,
			wantCount: 1, wantFails: 1, wantInfo: "held message release failed"},
		{name: "fail skipped is not counted", destination: ntfyDestination, failSkipped: true,
			wantCount: 0, wantFails: 1},
		{name: "fail store error stays held", destination: ntfyDestination, failErr: notFound,
			wantCount: 0, wantFails: 1, wantWarn: true},
		{name: "ntfy with display releases", destination: ntfyDestination, display: &Display{Title: "hi"},
			releaseOut: HeldReleased, wantCount: 1, wantReleases: 1, wantInfo: "held messages released"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			harness := newReleaseHarness()
			harness.resolver.results[releasePair] = []registry.ResolvedDestination{testCase.destination}
			harness.store.releasableResult = []StoredMessage{heldCandidate("m1", testCase.display)}
			if testCase.releaseOut != "" {
				harness.store.releaseResults = map[string]HeldReleaseResult{"m1": testCase.releaseOut}
			}
			if testCase.releaseErr != nil {
				harness.store.releaseErrs = map[string]error{"m1": testCase.releaseErr}
			}
			if testCase.failSkipped {
				harness.store.failSkipped = map[string]bool{"m1": true}
			}
			if testCase.failErr != nil {
				harness.store.failErrs = map[string]error{"m1": testCase.failErr}
			}

			count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10)
			if err != nil {
				subTest.Fatalf("ReleaseHeld error = %v", err)
			}
			if count != testCase.wantCount {
				subTest.Fatalf("count = %d, want %d", count, testCase.wantCount)
			}
			if got := len(harness.store.releaseCalls); got != testCase.wantReleases {
				subTest.Fatalf("release calls = %d, want %d", got, testCase.wantReleases)
			}
			if got := len(harness.store.failCalls); got != testCase.wantFails {
				subTest.Fatalf("fail calls = %d, want %d", got, testCase.wantFails)
			}
			for _, call := range harness.store.failCalls {
				if call.reason != ReleaseFailedReasonInvalid {
					subTest.Fatalf("fail reason = %q", call.reason)
				}
			}
			infoLines := harness.lines("INFO")
			if testCase.wantInfo == "" && len(infoLines) != 0 {
				subTest.Fatalf("unexpected INFO lines: %v", infoLines)
			}
			if testCase.wantInfo != "" && !strings.Contains(strings.Join(infoLines, "\n"), testCase.wantInfo) {
				subTest.Fatalf("INFO lines %v lack %q", infoLines, testCase.wantInfo)
			}
			if got := len(harness.lines("WARN")) > 0; got != testCase.wantWarn {
				subTest.Fatalf("warn logged = %v, want %v", got, testCase.wantWarn)
			}
			if got := len(harness.lines("ERROR")) > 0; got != testCase.wantError {
				subTest.Fatalf("error logged = %v, want %v", got, testCase.wantError)
			}
		})
	}
}

func TestReleaseHeldStoreErrorsPropagate(testInstance *testing.T) {
	storeErr := errors.New("database unavailable")
	testCases := []struct {
		name  string
		setup func(store *fakeServiceStore)
	}{
		{name: "list held pairs", setup: func(store *fakeServiceStore) { store.heldPairsErr = storeErr }},
		{name: "list releasable held", setup: func(store *fakeServiceStore) { store.releasableErr = storeErr }},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			harness := newReleaseHarness()
			harness.store.releasableResult = []StoredMessage{heldCandidate("m1", nil)}
			testCase.setup(harness.store)

			count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10)
			if !errors.Is(err, storeErr) || count != 0 {
				subTest.Fatalf("ReleaseHeld = (%d, %v), want (0, %v)", count, err, storeErr)
			}
			if len(harness.store.releaseCalls) != 0 || len(harness.store.failCalls) != 0 {
				subTest.Fatalf("store written after a list error")
			}
		})
	}
}

func TestReleaseHeldNonPositiveLimitDoesNothing(testInstance *testing.T) {
	for _, limit := range []int{0, -1} {
		harness := newReleaseHarness()
		count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, limit)
		if count != 0 || err != nil {
			testInstance.Fatalf("limit %d: ReleaseHeld = (%d, %v), want (0, nil)", limit, count, err)
		}
		if len(harness.store.heldPairsCalls) != 0 {
			testInstance.Fatalf("limit %d: ListHeldPairs called", limit)
		}
	}
}

func TestReleaseHeldWakesOnlyForPendingRelease(testInstance *testing.T) {
	harness := newReleaseHarness()
	harness.store.releasableResult = []StoredMessage{heldCandidate("m1", nil)}
	harness.store.releaseResults = map[string]HeldReleaseResult{"m1": HeldScheduled}
	if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10); err != nil {
		testInstance.Fatal(err)
	}
	if harness.wakes.calls() != 0 {
		testInstance.Fatalf("wakes = %d after a scheduled release, want 0", harness.wakes.calls())
	}
	harness.store.releaseResults = nil
	if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10); err != nil {
		testInstance.Fatal(err)
	}
	if harness.wakes.calls() != 1 {
		testInstance.Fatalf("wakes = %d after a pending release, want 1", harness.wakes.calls())
	}
}

func TestReleaseHeldNoRouteYetIsDebugOnly(testInstance *testing.T) {
	for _, name := range []string{"empty result", "no destination matched"} {
		testInstance.Run(name, func(subTest *testing.T) {
			harness := newReleaseHarness()
			if name == "empty result" {
				harness.resolver.results[releasePair] = []registry.ResolvedDestination{}
			} else {
				harness.resolver.errs[releasePair] = ErrNoDestinationMatched
			}

			count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10)
			if err != nil || count != 0 {
				subTest.Fatalf("ReleaseHeld = (%d, %v), want (0, nil)", count, err)
			}
			harness.requireNoWarnOrError(subTest)
			debugLines := harness.lines("DEBUG")
			if len(debugLines) != 1 ||
				!strings.Contains(debugLines[0], "app_id=app_one") || !strings.Contains(debugLines[0], "channel=alerts") {
				subTest.Fatalf("DEBUG lines = %v, want one with app_id= and channel=", debugLines)
			}
			if len(harness.store.releasableCalls) != 0 {
				subTest.Fatalf("ListReleasableHeld called for an unroutable pair")
			}
		})
	}
}

func TestReleaseHeldManyUnroutedPairsNeverWarn(testInstance *testing.T) {
	harness := newReleaseHarness()
	page := make([]HeldPair, 0, heldReleasePairPage)
	for index := 0; index < heldReleasePairPage; index++ {
		page = append(page, HeldPair{AppID: fmt.Sprintf("app_%04d", index), Channel: "alerts"})
	}
	harness.store.heldPairsPages = [][]HeldPair{page}
	for batch := 0; batch < 30; batch++ {
		if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10); err != nil {
			testInstance.Fatal(err)
		}
	}
	harness.requireNoWarnOrError(testInstance)
	if len(harness.resolver.calls) != 30*heldReleasePairPage {
		testInstance.Fatalf("resolve calls = %d", len(harness.resolver.calls))
	}
}

func TestReleaseHeldPairFaultsStayHeldWithThrottledWarn(testInstance *testing.T) {
	testCases := []struct {
		name       string
		resolved   []registry.ResolvedDestination
		resolveErr error
	}{
		{name: "no publisher", resolveErr: registry.ErrNoPublisher},
		{name: "unseal style error", resolveErr: errors.New("unseal: bad key")},
		{name: "unsupported type", resolved: []registry.ResolvedDestination{
			{ID: 9, Name: "d", Type: registry.TypeDiscord}}},
		{name: "mixed human facing and unsupported", resolved: []registry.ResolvedDestination{
			ntfyDestination, {ID: 9, Name: "d", Type: registry.TypeDiscord}}},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			harness := newReleaseHarness()
			harness.resolver.results[releasePair] = testCase.resolved
			harness.resolver.errs[releasePair] = testCase.resolveErr
			harness.store.releasableResult = []StoredMessage{heldCandidate("m1", nil)}

			for attempt := 0; attempt < 2; attempt++ {
				count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10)
				if err != nil || count != 0 {
					subTest.Fatalf("ReleaseHeld = (%d, %v), want (0, nil)", count, err)
				}
			}
			warns := harness.lines("WARN")
			if len(warns) != 1 {
				subTest.Fatalf("WARN lines = %v, want exactly one inside the throttle interval", warns)
			}
			for _, field := range []string{"app_id=app_one", "channel=alerts", "error="} {
				if !strings.Contains(warns[0], field) {
					subTest.Fatalf("WARN %q lacks %s", warns[0], field)
				}
			}
			if len(harness.store.releaseCalls) != 0 || len(harness.store.failCalls) != 0 {
				subTest.Fatalf("store written for a faulty pair")
			}

			if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow.Add(11*time.Minute), 10); err != nil {
				subTest.Fatal(err)
			}
			if got := len(harness.lines("WARN")); got != 2 {
				subTest.Fatalf("WARN lines after the interval = %d, want 2", got)
			}
		})
	}
}

func TestReleaseHeldResolvesEachPairOnceAndFailureDoesNotBlock(testInstance *testing.T) {
	harness := newReleaseHarness()
	harness.store.releasableResult = []StoredMessage{
		heldCandidate("m1", nil), heldCandidate("m2", nil), heldCandidate("m3", nil),
	}
	harness.store.releaseErrs = map[string]error{"m1": errors.New("boom")}

	count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10)
	if err != nil || count != 2 {
		testInstance.Fatalf("ReleaseHeld = (%d, %v), want (2, nil)", count, err)
	}
	if len(harness.resolver.calls) != 1 {
		testInstance.Fatalf("resolve calls = %d, want 1 for a pair with three messages", len(harness.resolver.calls))
	}
	if len(harness.store.releaseCalls) != 3 {
		testInstance.Fatalf("release calls = %d, want 3", len(harness.store.releaseCalls))
	}
}

func TestReleaseHeldSkipsDirectAddressedRows(testInstance *testing.T) {
	for _, field := range []string{"to_destination", "to_recipient"} {
		testInstance.Run(field, func(subTest *testing.T) {
			harness := newReleaseHarness()
			addressed := heldCandidate("addressed", nil)
			if field == "to_destination" {
				addressed.Envelope.ToDestination = "ops_webhook"
			} else {
				addressed.Envelope.ToRecipient = "person"
			}
			harness.store.releasableResult = []StoredMessage{addressed, heldCandidate("m2", nil)}

			count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10)
			if err != nil || count != 1 {
				subTest.Fatalf("ReleaseHeld = (%d, %v), want (1, nil)", count, err)
			}
			if len(harness.store.releaseCalls) != 1 || harness.store.releaseCalls[0].messageID != "m2" {
				subTest.Fatalf("release calls = %+v, want only m2", harness.store.releaseCalls)
			}
			if len(harness.store.failCalls) != 0 {
				subTest.Fatalf("addressed row was failed")
			}
			if got := len(harness.lines("WARN")); got != 1 {
				subTest.Fatalf("WARN lines = %d, want 1", got)
			}
		})
	}
}

func TestReleaseHeldDirectAddressWarnIsThrottled(testInstance *testing.T) {
	harness := newReleaseHarness()
	addressed := heldCandidate("addressed", nil)
	addressed.Envelope.ToDestination = "ops_webhook"
	harness.store.releasableResult = []StoredMessage{addressed}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10); err != nil {
			testInstance.Fatal(err)
		}
	}
	if got := len(harness.lines("WARN")); got != 1 {
		testInstance.Fatalf("WARN lines at the same now = %d, want 1", got)
	}
	if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow.Add(11*time.Minute), 10); err != nil {
		testInstance.Fatal(err)
	}
	if got := len(harness.lines("WARN")); got != 2 {
		testInstance.Fatalf("WARN lines after the interval = %d, want 2", got)
	}
}

func TestReleaseOneHeldUnsupportedDestinationStaysHeld(testInstance *testing.T) {
	harness := newReleaseHarness()
	discord := []registry.ResolvedDestination{{ID: 9, Name: "d", Type: registry.TypeDiscord}}

	outcome, err := harness.service.releaseOneHeld(context.Background(), heldCandidate("m1", nil), discord, releaseNow)
	if err != nil {
		testInstance.Fatalf("releaseOneHeld error = %v", err)
	}
	if outcome != heldOutcomeStayed {
		testInstance.Fatalf("outcome = %v, want heldOutcomeStayed", outcome)
	}
	if got := len(harness.lines("WARN")); got != 1 {
		testInstance.Fatalf("WARN lines = %d, want 1", got)
	}
	if len(harness.store.failCalls) != 0 {
		testInstance.Fatalf("FailHeldRelease called for an unsupported destination")
	}
}

func TestReleaseHeldUnknownResultErrorIsThrottled(testInstance *testing.T) {
	harness := newReleaseHarness()
	harness.store.releasableResult = []StoredMessage{heldCandidate("m1", nil)}
	harness.store.releaseResults = map[string]HeldReleaseResult{"m1": HeldReleaseResult("bogus")}
	for batch := 0; batch < 2; batch++ {
		if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10); err != nil {
			testInstance.Fatal(err)
		}
	}
	if got := len(harness.lines("ERROR")); got != 1 {
		testInstance.Fatalf("ERROR lines = %d, want 1", got)
	}
}

func TestReleaseHeldCursorPagesAndWraps(testInstance *testing.T) {
	harness := newReleaseHarness()
	fullPage := make([]HeldPair, 0, heldReleasePairPage)
	for index := 0; index < heldReleasePairPage; index++ {
		fullPage = append(fullPage, HeldPair{AppID: fmt.Sprintf("app_%04d", index), Channel: "alerts"})
	}
	harness.store.heldPairsPages = [][]HeldPair{fullPage, {releasePair}}

	for batch := 0; batch < 3; batch++ {
		if _, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10); err != nil {
			testInstance.Fatal(err)
		}
	}
	calls := harness.store.heldPairsCalls
	if len(calls) != 3 {
		testInstance.Fatalf("ListHeldPairs calls = %d, want 3", len(calls))
	}
	if calls[0].after != (HeldPair{}) || calls[0].limit != heldReleasePairPage {
		testInstance.Fatalf("first call = %+v", calls[0])
	}
	if calls[1].after != fullPage[len(fullPage)-1] {
		testInstance.Fatalf("second call after = %+v, want the last pair of the full page", calls[1].after)
	}
	if calls[2].after != (HeldPair{}) {
		testInstance.Fatalf("third call after = %+v, want a wrap to the start after a short page", calls[2].after)
	}
}

func TestReleaseHeldCancelledContext(testInstance *testing.T) {
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	testCases := []struct {
		name        string
		ctx         func() context.Context
		arrange     func(harness *releaseHarness)
		messages    []StoredMessage
		wantCount   int64
		wantFails   int
		wantRelease int
	}{
		{name: "cancelled before the call", ctx: cancelled, arrange: func(*releaseHarness) {}},
		{
			name: "resolver returns canceled", ctx: context.Background,
			arrange: func(harness *releaseHarness) { harness.resolver.errs[releasePair] = context.Canceled },
		},
		{
			name: "release returns canceled", ctx: context.Background,
			messages: []StoredMessage{heldCandidate("m1", nil)},
			arrange: func(harness *releaseHarness) {
				harness.store.releaseErrs = map[string]error{"m1": context.Canceled}
			},
			wantRelease: 1,
		},
		{
			name: "fail returns canceled", ctx: context.Background,
			messages: []StoredMessage{heldCandidate("m1", nil)},
			arrange: func(harness *releaseHarness) {
				harness.resolver.results[releasePair] = []registry.ResolvedDestination{ntfyDestination}
				harness.store.failErrs = map[string]error{"m1": context.Canceled}
			},
			wantFails: 1,
		},
		{
			name: "cancel after an earlier release keeps the count", ctx: context.Background,
			messages: []StoredMessage{heldCandidate("m1", nil), heldCandidate("m2", nil)},
			arrange: func(harness *releaseHarness) {
				harness.store.releaseErrs = map[string]error{"m2": context.Canceled}
			},
			wantCount: 1, wantRelease: 2,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			harness := newReleaseHarness()
			harness.store.releasableResult = testCase.messages
			testCase.arrange(harness)

			count, err := harness.service.ReleaseHeld(testCase.ctx(), releaseNow, 10)
			if !errors.Is(err, context.Canceled) {
				subTest.Fatalf("error = %v, want context.Canceled", err)
			}
			if count != testCase.wantCount {
				subTest.Fatalf("count = %d, want %d", count, testCase.wantCount)
			}
			harness.requireNoWarnOrError(subTest)
			if got := len(harness.store.releaseCalls); got != testCase.wantRelease {
				subTest.Fatalf("release calls = %d, want %d", got, testCase.wantRelease)
			}
			if got := len(harness.store.failCalls); got != testCase.wantFails {
				subTest.Fatalf("fail calls = %d, want %d", got, testCase.wantFails)
			}
			switch testCase.name {
			case "cancelled before the call":
				if len(harness.store.heldPairsCalls) != 0 {
					subTest.Fatalf("store called with a cancelled context")
				}
			case "resolver returns canceled":
				if len(harness.store.releasableCalls) != 0 {
					subTest.Fatalf("ListReleasableHeld called after a resolver cancel")
				}
			case "release returns canceled":
				if len(harness.store.failCalls) != 0 {
					subTest.Fatalf("FailHeldRelease called after a release cancel")
				}
			}
			if len(harness.lines("INFO")) != 0 && testCase.wantCount == 0 {
				subTest.Fatalf("INFO lines on a cancel with nothing released: %v", harness.lines("INFO"))
			}
		})
	}
}

func TestReleaseHeldLogFields(testInstance *testing.T) {
	harness := newReleaseHarness()
	harness.resolver.results[releasePair] = []registry.ResolvedDestination{ntfyDestination}
	titled := heldCandidate("ok", &Display{Title: releaseSecretTitle})
	invalid := heldCandidate("bad", nil)
	invalid.Envelope.Payload = []byte(`{"secret":"payload-text"}`)
	harness.store.releasableResult = []StoredMessage{titled, invalid}

	count, err := harness.service.ReleaseHeld(context.Background(), releaseNow, 10)
	if err != nil || count != 2 {
		testInstance.Fatalf("ReleaseHeld = (%d, %v), want (2, nil)", count, err)
	}
	logs := harness.logs.String()
	for _, want := range []string{
		"released=1", "failed=1", "oldest_held_age=1h0m0s", "message_id=bad",
		"app_id=app_one", "channel=alerts", "reason=release_invalid",
	} {
		if !strings.Contains(logs, want) {
			testInstance.Fatalf("logs lack %q:\n%s", want, logs)
		}
	}
	for _, forbidden := range []string{releaseSecretTitle, "payload-text"} {
		if strings.Contains(logs, forbidden) {
			testInstance.Fatalf("logs leak %q:\n%s", forbidden, logs)
		}
	}
}

func TestResolveTargetsMixedListPrecedence(testInstance *testing.T) {
	humanFacing := ntfyDestination
	unsupported := registry.ResolvedDestination{ID: 9, Name: "zz_discord", Type: registry.TypeDiscord}
	testCases := []struct {
		name     string
		resolved []registry.ResolvedDestination
		wantErr  func(error) bool
	}{
		{
			name:     "human facing first: its display check wins",
			resolved: []registry.ResolvedDestination{humanFacing, unsupported},
			wantErr: func(err error) bool {
				var validationErr *ValidationError
				return errors.As(err, &validationErr)
			},
		},
		{
			name:     "unsupported first: the type check wins",
			resolved: []registry.ResolvedDestination{unsupported, humanFacing},
			wantErr:  func(err error) bool { return errors.Is(err, ErrUnsupportedDestinationType) },
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			service := NewService(ServiceConfig{
				Store:          &fakeServiceStore{},
				Resolver:       &fakeResolver{result: testCase.resolved},
				SupportedTypes: map[string]bool{registry.TypeNtfy: true},
			})
			_, err := service.resolveTargets(context.Background(), Envelope{AppID: "app_one", Channel: "alerts"})
			if !testCase.wantErr(err) {
				subTest.Fatalf("error = %v", err)
			}
		})
	}
}

package publisher

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
)

const waitLimit = 5 * time.Second

// gatedMinter holds every mint in flight until release is closed (or sends to release). It records
// whether the ctx it received can be canceled.
type gatedMinter struct {
	mutex       sync.Mutex
	entered     chan string
	release     chan struct{}
	calls       int
	cancelables []bool
	err         error
	panicValue  any
}

func newGatedMinter() *gatedMinter {
	return &gatedMinter{entered: make(chan string, 16), release: make(chan struct{})}
}

func (minter *gatedMinter) MintPublisher(ctx context.Context, appID string, _ bool) (provisionclient.PublisherGrant, error) {
	minter.mutex.Lock()
	minter.calls++
	minter.cancelables = append(minter.cancelables, ctx.Done() != nil)
	minter.mutex.Unlock()
	minter.entered <- appID
	<-minter.release
	if minter.panicValue != nil {
		panic(minter.panicValue)
	}
	if minter.err != nil {
		return provisionclient.PublisherGrant{}, minter.err
	}
	return grantFor(appID, firstToken), nil
}

func (minter *gatedMinter) callCount() int {
	minter.mutex.Lock()
	defer minter.mutex.Unlock()
	return minter.calls
}

func waitEntered(testInstance *testing.T, minter *gatedMinter) string {
	testInstance.Helper()
	select {
	case appID := <-minter.entered:
		return appID
	case <-time.After(waitLimit):
		testInstance.Fatal("timed out waiting for a mint to reach the minter")
		return ""
	}
}

type mintResult struct {
	view PublisherView
	err  error
}

func mintAsync(service *Service, appID string) <-chan mintResult {
	done := make(chan mintResult, 1)
	go func() {
		view, err := service.Mint(context.Background(), appID, false)
		done <- mintResult{view: view, err: err}
	}()
	return done
}

func waitResult(testInstance *testing.T, done <-chan mintResult) mintResult {
	testInstance.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(waitLimit):
		testInstance.Fatal("timed out waiting for a mint to finish")
		return mintResult{}
	}
}

func okMinter() *fakeMinter {
	return &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}}
}

func TestMintTuningConstantsArePinned(testInstance *testing.T) {
	// MaxInFlightMints must stay below the pgxpool default MaxConns (max(4, NumCPU)); raise the
	// pool's MaxConns before raising the cap.
	if MaxInFlightMints != 2 || MintBeginTimeout != 3*time.Second || MintCommitTimeout != 5*time.Second ||
		MintPersistTimeout != 5*time.Second || MintRateLimit != 10 || MintRateWindow != time.Hour {
		testInstance.Fatal("a mint tuning constant changed")
	}
	service := NewService(newFakeStore(), nil, newSealer(testInstance), nil)
	if cap(service.mintSlots) != MaxInFlightMints || service.persistTimeout != MintPersistTimeout {
		testInstance.Fatalf("defaults: cap=%d persist=%v", cap(service.mintSlots), service.persistTimeout)
	}
}

func TestOverlappingMintForOneAppIsRefusedWithBusy(testInstance *testing.T) {
	store := newFakeStore()
	minter := newGatedMinter()
	service, _ := rotateServiceFor(testInstance, store, minter)

	first := mintAsync(service, "urls4irl")
	waitEntered(testInstance, minter)

	_, secondErr := service.Mint(context.Background(), "urls4irl", false)
	if !errors.Is(secondErr, ErrMintBusy) {
		testInstance.Fatalf("second Mint err = %v, want ErrMintBusy", secondErr)
	}
	if minter.callCount() != 1 || store.reserveCalls != 1 {
		testInstance.Fatalf("minter calls=%d reserve calls=%d, want 1/1 (refused mint reaches neither)", minter.callCount(), store.reserveCalls)
	}

	close(minter.release)
	if result := waitResult(testInstance, first); result.err != nil {
		testInstance.Fatalf("first Mint: %v", result.err)
	}
	if _, thirdErr := service.Mint(context.Background(), "urls4irl", false); thirdErr != nil {
		testInstance.Fatalf("third Mint after release: %v", thirdErr)
	}
}

func TestMintsForDifferentAppsRunTogether(testInstance *testing.T) {
	minter := newGatedMinter()
	service, _ := rotateServiceFor(testInstance, newFakeStore(), minter)

	first := mintAsync(service, "appa")
	second := mintAsync(service, "appb")
	waitEntered(testInstance, minter)
	waitEntered(testInstance, minter)

	close(minter.release)
	for _, done := range []<-chan mintResult{first, second} {
		if result := waitResult(testInstance, done); result.err != nil {
			testInstance.Fatalf("Mint: %v", result.err)
		}
	}
}

func TestLockAndCapSlotAreReleasedOnEveryExitPath(testInstance *testing.T) {
	cases := []struct {
		name  string
		setup func(store *fakeStore) Minter
	}{
		{"minter error", func(*fakeStore) Minter { return &fakeMinter{err: errors.New("boom")} }},
		{"upsert error", func(store *fakeStore) Minter { store.upsertErr = errors.New("db"); return okMinter() }},
		{"grant for another app", func(*fakeStore) Minter {
			return &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("otherapp", firstToken)}}
		}},
		{"rate limited", func(store *fakeStore) Minter {
			store.reserveFn = func(string) error { return &RateLimitedError{RetryAfter: time.Second} }
			return okMinter()
		}},
		{"busy", func(store *fakeStore) Minter { store.held["urls4irl"] = true; return okMinter() }},
		{"minter panic", func(*fakeStore) Minter {
			minter := newGatedMinter()
			minter.panicValue = "kaboom"
			close(minter.release)
			return minter
		}},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			store := newFakeStore()
			service, _ := rotateServiceFor(subTest, store, testCase.setup(store))
			service.mintSlots = make(chan struct{}, 1)

			func() {
				defer func() { _ = recover() }()
				_, _ = service.Mint(context.Background(), "urls4irl", false)
			}()

			store.held = map[string]bool{}
			store.reserveFn = nil
			store.upsertErr = nil
			service.minter = okMinter()
			if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr != nil {
				subTest.Fatalf("following Mint (lock and the single cap slot must be free): %v", mintErr)
			}
		})
	}
}

func TestLockFailureIsUnavailableWithoutCallingTheMinter(testInstance *testing.T) {
	store := newFakeStore()
	store.lockErr = fmt.Errorf("%w: begin: %w", ErrMintUnavailable, context.DeadlineExceeded)
	minter := okMinter()
	service, logs := rotateServiceFor(testInstance, store, minter)

	_, mintErr := service.Mint(context.Background(), "urls4irl", false)

	if !errors.Is(mintErr, ErrMintUnavailable) || len(minter.calls) != 0 {
		testInstance.Fatalf("err=%v minter calls=%v", mintErr, minter.calls)
	}
	if logged := logs.String(); !strings.Contains(logged, "level=ERROR") || !strings.Contains(logged, "app_id=urls4irl") {
		testInstance.Fatalf("want an ERROR with app_id: %s", logged)
	}
}

func TestRateLimitedMintDoesNotCallTheMinterAndLogsAppAndRetryAfter(testInstance *testing.T) {
	store := newFakeStore()
	attempts := map[string]int{}
	store.reserveFn = func(appID string) error {
		attempts[appID]++
		if attempts[appID] > MintRateLimit {
			return &RateLimitedError{RetryAfter: 90 * time.Second}
		}
		return nil
	}
	minter := &fakeMinter{err: errors.New("upstream down")}
	service, logs := rotateServiceFor(testInstance, store, minter)

	for range MintRateLimit {
		_, _ = service.Mint(context.Background(), "urls4irl", false) // failed attempts count too
	}
	_, mintErr := service.Mint(context.Background(), "urls4irl", false)

	var rateLimited *RateLimitedError
	if !errors.As(mintErr, &rateLimited) || rateLimited.RetryAfter < time.Second {
		testInstance.Fatalf("err = %v, want *RateLimitedError", mintErr)
	}
	if len(minter.calls) != MintRateLimit {
		testInstance.Fatalf("minter calls = %d, want %d", len(minter.calls), MintRateLimit)
	}
	if logged := logs.String(); !strings.Contains(logged, "app_id=urls4irl") || !strings.Contains(logged, "retry_after=1m30s") {
		testInstance.Fatalf("429 log missing app_id/retry_after: %s", logged)
	}
	// Limits are per app (the scripted counter is keyed by app).
	if _, otherErr := service.Mint(context.Background(), "otherapp", false); errors.As(otherErr, &rateLimited) {
		testInstance.Fatalf("another app was limited: %v", otherErr)
	}
}

func TestLimitStoreErrorFailsClosed(testInstance *testing.T) {
	store := newFakeStore()
	store.reserveFn = func(string) error { return errors.New("db down") }
	minter := okMinter()
	service, _ := rotateServiceFor(testInstance, store, minter)

	_, mintErr := service.Mint(context.Background(), "urls4irl", false)

	if !errors.Is(mintErr, ErrMintUnavailable) || len(minter.calls) != 0 {
		testInstance.Fatalf("err=%v minter calls=%v", mintErr, minter.calls)
	}
}

func TestOperatorMintIsExemptFromTheLimitButTakesTheLock(testInstance *testing.T) {
	store := newFakeStore()
	store.reserveFn = func(string) error { return &RateLimitedError{RetryAfter: time.Second} }
	service, _ := rotateServiceFor(testInstance, store, &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("urls4irl", firstToken)}})

	for range 25 {
		if _, mintErr := service.MintAsOperator(context.Background(), "urls4irl", false); mintErr != nil {
			testInstance.Fatalf("MintAsOperator: %v", mintErr)
		}
	}
	if store.reserveCalls != 0 {
		testInstance.Fatalf("operator mints reserved %d slots, want 0", store.reserveCalls)
	}
	var rateLimited *RateLimitedError
	if _, appErr := service.Mint(context.Background(), "urls4irl", false); !errors.As(appErr, &rateLimited) || store.reserveCalls != 1 {
		testInstance.Fatalf("app path: err=%v reserve calls=%d, want limited with 1 reserve", appErr, store.reserveCalls)
	}

	store.held["urls4irl"] = true
	if _, busyErr := service.MintAsOperator(context.Background(), "urls4irl", false); !errors.Is(busyErr, ErrMintBusy) {
		testInstance.Fatalf("operator mint while held: err = %v, want ErrMintBusy", busyErr)
	}
}

func TestMintSurvivesClientDisconnectDuringTheUpstreamCall(testInstance *testing.T) {
	store := newFakeStore()
	minter := newGatedMinter()
	service, _ := rotateServiceFor(testInstance, store, minter)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan mintResult, 1)
	go func() {
		view, err := service.Mint(ctx, "urls4irl", false)
		done <- mintResult{view: view, err: err}
	}()
	waitEntered(testInstance, minter)

	cancel()
	close(minter.release)

	if result := waitResult(testInstance, done); result.err != nil {
		testInstance.Fatalf("Mint after disconnect: %v", result.err)
	}
	if minter.cancelables[0] {
		testInstance.Fatal("the minter received a cancelable ctx; want a detached one")
	}
	if _, found := store.rows["urls4irl"]; !found {
		testInstance.Fatal("the new token was not stored")
	}
	state := store.upsertCtxs[0]
	if state.err != nil || !state.hasDeadline || time.Until(state.deadline) > MintPersistTimeout {
		testInstance.Fatalf("Upsert ctx: err=%v deadline=%v/%v, want live with deadline <= %v", state.err, state.hasDeadline, state.deadline, MintPersistTimeout)
	}
}

func TestPersistTimeoutFailsAsUnavailableAndLogsTheUpsertStage(testInstance *testing.T) {
	store := newFakeStore()
	store.upsertWait = true
	service, logs := rotateServiceFor(testInstance, store, okMinter())
	service.persistTimeout = 10 * time.Millisecond

	_, mintErr := service.Mint(context.Background(), "urls4irl", true)

	if !errors.Is(mintErr, ErrMintUnavailable) || !errors.Is(mintErr, context.DeadlineExceeded) {
		testInstance.Fatalf("err = %v, want ErrMintUnavailable wrapping the deadline", mintErr)
	}
	if logged := logs.String(); !strings.Contains(logged, "stage=upsert") || !strings.Contains(logged, "app_id=urls4irl") {
		testInstance.Fatalf("rotate ERROR missing: %s", logged)
	}
}

func TestPostMintFailuresAreUnavailableButMinterAndMismatchAreNot(testInstance *testing.T) {
	storeErr := errors.New("db")
	upsertFail := newFakeStore()
	upsertFail.upsertErr = storeErr
	commitFail := newFakeStore()
	commitFail.commitErr = storeErr
	for name, store := range map[string]*fakeStore{"upsert": upsertFail, "commit": commitFail} {
		service, _ := rotateServiceFor(testInstance, store, okMinter())
		_, mintErr := service.Mint(context.Background(), "urls4irl", false)
		if !errors.Is(mintErr, ErrMintUnavailable) || !errors.Is(mintErr, storeErr) {
			testInstance.Errorf("%s failure: err = %v, want ErrMintUnavailable wrapping the cause", name, mintErr)
		}
	}
	for name, minter := range map[string]Minter{
		"minter":   &fakeMinter{err: errors.New("up")},
		"mismatch": &fakeMinter{grants: []provisionclient.PublisherGrant{grantFor("otherapp", firstToken)}},
	} {
		service, _ := rotateServiceFor(testInstance, newFakeStore(), minter)
		if _, mintErr := service.Mint(context.Background(), "urls4irl", false); mintErr == nil || errors.Is(mintErr, ErrMintUnavailable) {
			testInstance.Errorf("%s failure: err = %v, want a plain error", name, mintErr)
		}
	}
}

func TestCommitFailureLogsTheRotateErrorOnlyForRotate(testInstance *testing.T) {
	for _, rotate := range []bool{true, false} {
		store := newFakeStore()
		store.commitErr = errors.New("commit failed")
		service, logs := rotateServiceFor(testInstance, store, okMinter())

		_, mintErr := service.Mint(context.Background(), "urls4irl", rotate)

		if !errors.Is(mintErr, ErrMintUnavailable) {
			testInstance.Fatalf("rotate=%v: err = %v", rotate, mintErr)
		}
		logged := logs.String()
		if rotate != (strings.Contains(logged, "stage=commit") && strings.Contains(logged, "app_id=urls4irl")) {
			testInstance.Fatalf("rotate=%v: log = %s", rotate, logged)
		}
		if strings.Contains(logged, firstToken) {
			testInstance.Fatalf("log contains the token: %s", logged)
		}
	}
}

func TestInFlightCapRefusesOverflowMintsOnBothPaths(testInstance *testing.T) {
	store := newFakeStore()
	minter := newGatedMinter()
	service, logs := rotateServiceFor(testInstance, store, minter)
	service.mintSlots = make(chan struct{}, 1)

	first := mintAsync(service, "appa")
	waitEntered(testInstance, minter)

	_, appErr := service.Mint(context.Background(), "appb", false)
	_, operatorErr := service.MintAsOperator(context.Background(), "appc", false)

	if !errors.Is(appErr, ErrMintUnavailable) || !errors.Is(operatorErr, ErrMintUnavailable) {
		testInstance.Fatalf("overflow errs = %v, %v, want ErrMintUnavailable", appErr, operatorErr)
	}
	if minter.callCount() != 1 || store.lockCalls != 1 || store.reserveCalls != 1 {
		testInstance.Fatalf("minter=%d lock=%d reserve=%d, want 1/1/1 (refused mints touch nothing)",
			minter.callCount(), store.lockCalls, store.reserveCalls)
	}
	if logged := logs.String(); !strings.Contains(logged, "reason=\"in-flight cap\"") || !strings.Contains(logged, "app_id=appb") {
		testInstance.Fatalf("cap log missing: %s", logged)
	}

	close(minter.release)
	waitResult(testInstance, first)
	if _, mintErr := service.Mint(context.Background(), "appd", false); mintErr != nil {
		testInstance.Fatalf("Mint after release: %v", mintErr)
	}
}

func TestServiceWithoutNewServiceRefusesEveryMint(testInstance *testing.T) {
	minter := okMinter()
	service := &Service{store: newFakeStore(), minter: minter, sealer: newSealer(testInstance), logger: slog.Default()}

	_, mintErr := service.Mint(context.Background(), "urls4irl", false)

	if !errors.Is(mintErr, ErrMintUnavailable) || len(minter.calls) != 0 {
		testInstance.Fatalf("err=%v minter calls=%v, want a closed refusal", mintErr, minter.calls)
	}
}

func TestInvalidAppAndNilMinterNeverTouchTheCapOrTheStore(testInstance *testing.T) {
	store := newFakeStore()
	service, logs := rotateServiceFor(testInstance, store, nil)
	service.mintSlots = make(chan struct{}, 1)
	service.mintSlots <- struct{}{} // full: a wrongly ordered cap check would log a cap refusal

	if _, mintErr := service.Mint(context.Background(), "Bad App", false); mintErr == nil {
		testInstance.Fatal("invalid app id succeeded")
	}
	if _, mintErr := service.Mint(context.Background(), "urls4irl", false); !errors.Is(mintErr, ErrMintUnavailable) {
		testInstance.Fatalf("nil minter err = %v", mintErr)
	}
	if logs.Len() != 0 {
		testInstance.Fatalf("the cap was consulted: %s", logs.String())
	}
	<-service.mintSlots
	if len(service.mintSlots) != 0 || store.lockCalls != 0 || store.reserveCalls != 0 {
		testInstance.Fatalf("cap=%d lock=%d reserve=%d, want all zero", len(service.mintSlots), store.lockCalls, store.reserveCalls)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

// TestSealFailureIsUnavailableReleasesTheLockAndLogsTheStage makes Seal fail by swapping the package
// level crypto/rand.Reader (no test in this package is parallel).
func TestSealFailureIsUnavailableReleasesTheLockAndLogsTheStage(testInstance *testing.T) {
	store := newFakeStore()
	service, logs := rotateServiceFor(testInstance, store, okMinter())
	service.mintSlots = make(chan struct{}, 1)
	original := rand.Reader
	rand.Reader = failingReader{}
	_, mintErr := service.Mint(context.Background(), "urls4irl", true)
	rand.Reader = original

	if !errors.Is(mintErr, ErrMintUnavailable) || !strings.Contains(mintErr.Error(), "entropy unavailable") {
		testInstance.Fatalf("err = %v, want ErrMintUnavailable keeping the cause", mintErr)
	}
	if !strings.Contains(logs.String(), "stage=seal") || strings.Contains(logs.String(), firstToken) {
		testInstance.Fatalf("log = %s", logs.String())
	}
	if _, followErr := service.Mint(context.Background(), "urls4irl", false); followErr != nil {
		testInstance.Fatalf("following Mint (lock and cap slot must be free): %v", followErr)
	}
}

// A pre-filled cap turns a wrongly ordered cap check into an extra refusal, so these prove invalid
// ids and a missing minter are answered before the cap is looked at.
func TestInvalidAppIDIsRefusedBeforeTheCap(testInstance *testing.T) {
	service, logs := rotateServiceFor(testInstance, newFakeStore(), okMinter())
	service.mintSlots = make(chan struct{}, 1)
	service.mintSlots <- struct{}{}

	_, mintErr := service.Mint(context.Background(), "Bad App", false)

	var validation *ValidationError
	if !errors.As(mintErr, &validation) || strings.Contains(logs.String(), "in-flight cap") {
		testInstance.Fatalf("err=%v log=%s, want a validation error with the cap never consulted", mintErr, logs.String())
	}
}

func TestBusyRefusalIsLoggedWithTheApp(testInstance *testing.T) {
	store := newFakeStore()
	store.held["urls4irl"] = true
	service, logs := rotateServiceFor(testInstance, store, okMinter())

	_, mintErr := service.Mint(context.Background(), "urls4irl", false)

	if !errors.Is(mintErr, ErrMintBusy) || !strings.Contains(logs.String(), "app_id=urls4irl") {
		testInstance.Fatalf("err=%v log=%s", mintErr, logs.String())
	}
}

package appteardown

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
)

// recorder is one ordered log shared by every fake, plus per-step scripted failures that are consumed
// once (so a repeat of the call succeeds).
type recorder struct {
	calls         []string
	upstreamErr   error
	upstreamState int
	unbindErr     error
	unbindResult  bool
	removeErr     error
	deleteErr     error
	cancel        context.CancelFunc // cancelled inside the upstream fake
	ctxErrs       map[string]error   // ctx.Err() observed by each later step
	deadlines     map[string]time.Time
}

func newRecorder() *recorder {
	return &recorder{upstreamState: 200, unbindResult: true, ctxErrs: map[string]error{}, deadlines: map[string]time.Time{}}
}

// noteDeadline records the deadline a later step's context carries (zero time when it has none).
func (rec *recorder) noteDeadline(step string, ctx context.Context) {
	deadline, _ := ctx.Deadline()
	rec.deadlines[step] = deadline
}

func (rec *recorder) DeprovisionApp(_ context.Context, appID string) (provisionclient.Passthrough, error) {
	rec.calls = append(rec.calls, "upstream:"+appID)
	if rec.cancel != nil {
		rec.cancel()
	}
	err := rec.upstreamErr
	rec.upstreamErr = nil
	return provisionclient.Passthrough{Status: rec.upstreamState}, err
}

func (rec *recorder) Unbind(ctx context.Context, _ string) (bool, error) {
	rec.calls = append(rec.calls, "unbind")
	rec.ctxErrs["unbind"] = ctx.Err()
	rec.noteDeadline("unbind", ctx)
	err := rec.unbindErr
	rec.unbindErr = nil
	return rec.unbindResult, err
}

func (rec *recorder) RemoveExclusive(ctx context.Context, _ string) error {
	rec.calls = append(rec.calls, "remove")
	rec.ctxErrs["remove"] = ctx.Err()
	rec.noteDeadline("remove", ctx)
	err := rec.removeErr
	rec.removeErr = nil
	return err
}

func (rec *recorder) DeleteOwnedObjects(ctx context.Context, _ string) (Deleted, error) {
	rec.calls = append(rec.calls, "delete")
	rec.ctxErrs["delete"] = ctx.Err()
	rec.noteDeadline("delete", ctx)
	err := rec.deleteErr
	rec.deleteErr = nil
	return Deleted{Checks: 1, Routes: 2, Destinations: 3, OrphanedRoutes: 4, OrphanedChecks: 5, HeldMessages: 6}, err
}

func newTestService(rec *recorder) *Service {
	return NewService(ServiceConfig{Provisioner: rec, Binder: rec, Publisher: rec, Store: rec})
}

func TestDeprovisionRunsPhasesInOrder(testInstance *testing.T) {
	rec := newRecorder()

	result, err := newTestService(rec).Deprovision(context.Background(), "urls4irl")

	if err != nil {
		testInstance.Fatalf("Deprovision: %v", err)
	}
	want := []string{"upstream:urls4irl", "unbind", "remove", "delete"}
	if !slices.Equal(rec.calls, want) {
		testInstance.Fatalf("calls = %v, want %v", rec.calls, want)
	}
	if !result.Removed || !result.Unbound || result.AppID != "urls4irl" || result.Deleted.Destinations != 3 ||
		result.Deleted.OrphanedRoutes != 4 || result.Deleted.OrphanedChecks != 5 || result.Deleted.HeldMessages != 6 {
		testInstance.Fatalf("result = %+v", result)
	}
}

func TestDeprovisionInvalidAppIDMakesNoCalls(testInstance *testing.T) {
	for _, appID := range []string{"", "Bad-App", "_platform", "-x", "a b"} {
		rec := newRecorder()
		_, err := newTestService(rec).Deprovision(context.Background(), appID)
		if !errors.Is(err, ErrInvalidAppID) || len(rec.calls) != 0 {
			testInstance.Fatalf("app %q: err = %v calls = %v, want ErrInvalidAppID and no calls", appID, err, rec.calls)
		}
	}
}

func TestDeprovisionUnboundAppStillCompletes(testInstance *testing.T) {
	rec := newRecorder()
	rec.unbindResult = false

	result, err := newTestService(rec).Deprovision(context.Background(), "urls4irl")

	if err != nil || !result.Removed || result.Unbound {
		testInstance.Fatalf("result = %+v err = %v, want removed:true unbound:false", result, err)
	}
}

func TestDeprovisionUpstreamFailureKeepsEverything(testInstance *testing.T) {
	cases := map[string]func(*recorder){
		"transport error": func(rec *recorder) { rec.upstreamErr = errors.New("dial: boom") },
		"500":             func(rec *recorder) { rec.upstreamState = 500 },
		"404":             func(rec *recorder) { rec.upstreamState = 404 },
		"302":             func(rec *recorder) { rec.upstreamState = 302 },
	}
	for name, arrange := range cases {
		testInstance.Run(name, func(subTest *testing.T) {
			rec := newRecorder()
			arrange(rec)

			_, err := newTestService(rec).Deprovision(context.Background(), "urls4irl")

			if !errors.Is(err, ErrUpstream) {
				subTest.Fatalf("err = %v, want ErrUpstream", err)
			}
			if !slices.Equal(rec.calls, []string{"upstream:urls4irl"}) {
				subTest.Fatalf("calls = %v, want only the upstream call", rec.calls)
			}
		})
	}
}

func TestDeprovisionLaterFailureStopsTheChainAndARepeatCompletes(testInstance *testing.T) {
	cases := []struct {
		name      string
		arrange   func(*recorder)
		wantErr   error
		wantCalls []string
	}{
		{"unbind", func(rec *recorder) { rec.unbindErr = errors.New("db") }, ErrUnavailable,
			[]string{"upstream:urls4irl", "unbind"}},
		{"remove store failure", func(rec *recorder) { rec.removeErr = publisher.ErrMintUnavailable }, ErrUnavailable,
			[]string{"upstream:urls4irl", "unbind", "remove"}},
		{"remove busy", func(rec *recorder) { rec.removeErr = publisher.ErrMintBusy }, ErrBusy,
			[]string{"upstream:urls4irl", "unbind", "remove"}},
		{"delete", func(rec *recorder) { rec.deleteErr = errors.New("db") }, ErrUnavailable,
			[]string{"upstream:urls4irl", "unbind", "remove", "delete"}},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rec := newRecorder()
			testCase.arrange(rec)
			service := newTestService(rec)

			_, err := service.Deprovision(context.Background(), "urls4irl")

			if !errors.Is(err, testCase.wantErr) || !slices.Equal(rec.calls, testCase.wantCalls) {
				subTest.Fatalf("err = %v calls = %v, want %v %v", err, rec.calls, testCase.wantErr, testCase.wantCalls)
			}

			rec.calls = nil
			result, repeatErr := service.Deprovision(context.Background(), "urls4irl")
			if repeatErr != nil || !result.Removed {
				subTest.Fatalf("repeat: %+v %v, want success", result, repeatErr)
			}
			if !slices.Equal(rec.calls, []string{"upstream:urls4irl", "unbind", "remove", "delete"}) {
				subTest.Fatalf("repeat calls = %v, want the full chain", rec.calls)
			}
		})
	}
}

func TestDeprovisionBusyStopsBeforeTheObjectDelete(testInstance *testing.T) {
	rec := newRecorder()
	rec.removeErr = publisher.ErrMintBusy

	_, err := newTestService(rec).Deprovision(context.Background(), "urls4irl")

	if !errors.Is(err, ErrBusy) || slices.Contains(rec.calls, "delete") {
		testInstance.Fatalf("err = %v calls = %v, want ErrBusy and no delete", err, rec.calls)
	}
}

func TestDeprovisionCancelAfterUpstreamStillCompletes(testInstance *testing.T) {
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	rec.cancel = cancel

	result, err := newTestService(rec).Deprovision(ctx, "urls4irl")

	if err != nil || !result.Removed {
		testInstance.Fatalf("result = %+v err = %v, want completion despite cancel", result, err)
	}
	if !slices.Equal(rec.calls, []string{"upstream:urls4irl", "unbind", "remove", "delete"}) {
		testInstance.Fatalf("calls = %v, want the full chain", rec.calls)
	}
	for step, ctxErr := range rec.ctxErrs {
		if ctxErr != nil {
			testInstance.Fatalf("%s saw a cancelled context: %v", step, ctxErr)
		}
	}
	for _, step := range []string{"unbind", "remove", "delete"} {
		deadline := rec.deadlines[step]
		remaining := time.Until(deadline)
		if deadline.IsZero() || remaining <= 0 || remaining > teardownTimeout {
			testInstance.Fatalf("%s deadline remaining = %v (zero=%v), want within (0, %v]",
				step, remaining, deadline.IsZero(), teardownTimeout)
		}
	}
}

func TestDeprovisionWrappedMintBusyIsBusy(testInstance *testing.T) {
	rec := newRecorder()
	rec.removeErr = fmt.Errorf("remove publisher: %w", publisher.ErrMintBusy)

	_, err := newTestService(rec).Deprovision(context.Background(), "urls4irl")

	if !errors.Is(err, ErrBusy) {
		testInstance.Fatalf("err = %v, want ErrBusy", err)
	}
}

func TestDeprovisionNilDependencyFailsClosed(testInstance *testing.T) {
	cases := []struct {
		name   string
		mutate func(config *ServiceConfig)
	}{
		{"nil publisher", func(config *ServiceConfig) { config.Publisher = nil }},
		{"nil provisioner", func(config *ServiceConfig) { config.Provisioner = nil }},
		{"nil binder", func(config *ServiceConfig) { config.Binder = nil }},
		{"nil store", func(config *ServiceConfig) { config.Store = nil }},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			rec := newRecorder()
			config := ServiceConfig{Provisioner: rec, Binder: rec, Publisher: rec, Store: rec}
			testCase.mutate(&config)
			service := NewService(config)

			_, err := service.Deprovision(context.Background(), "urls4irl")

			if !errors.Is(err, ErrUnavailable) || len(rec.calls) != 0 {
				subTest.Fatalf("err = %v calls = %v, want ErrUnavailable and no calls", err, rec.calls)
			}
		})
	}
}

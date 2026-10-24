//go:build integration

package heartbeat_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const pgStoreTestBudget = 60 * time.Second

// absentID is far past any id a test inserts.
const absentID int64 = 1_000_000

var created = time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)

type fixture struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	store    *heartbeat.PGStore
	registry *registry.PGStore
	sealer   *seal.Sealer
}

func newFixture(testInstance *testing.T) fixture {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pgStoreTestBudget)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	sealer, sealErr := seal.New([]byte("0123456789abcdef0123456789abcdef"))
	if sealErr != nil {
		testInstance.Fatalf("seal.New: %v", sealErr)
	}
	return fixture{
		ctx:      ctx,
		pool:     pool,
		store:    heartbeat.NewPGStore(pool, sealer),
		registry: registry.NewPGStore(pool),
		sealer:   sealer,
	}
}

func (stack fixture) destination(testInstance *testing.T, name string) registry.StoredDestination {
	testInstance.Helper()
	inserted, err := stack.registry.InsertDestination(stack.ctx, registry.StoredDestination{
		Name:          name,
		Type:          registry.TypeNtfy,
		Config:        map[string]string{"topic": "alerts_" + name},
		SealedSecrets: []byte{0x01},
		Enabled:       true,
	})
	if err != nil {
		testInstance.Fatalf("insert destination %s: %v", name, err)
	}
	return inserted
}

// check creates a 600 s period, 300 s grace check at `created` (deadline
// created + 15 min) delivering to the named destinations.
func (stack fixture) check(testInstance *testing.T, name string, destinations ...string) heartbeat.StoredCheck {
	testInstance.Helper()
	stored, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: name, PeriodSeconds: 600, GraceSeconds: 300, DestinationNames: destinations,
	}, created)
	if err != nil {
		testInstance.Fatalf("create check %s: %v", name, err)
	}
	return stored
}

func (stack fixture) count(testInstance *testing.T, table string) int {
	testInstance.Helper()
	var rows int
	if err := stack.pool.QueryRow(stack.ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil {
		testInstance.Fatalf("count %s: %v", table, err)
	}
	return rows
}

func pendingOf(check heartbeat.StoredCheck) string {
	if check.PendingAlert == nil {
		return ""
	}
	return *check.PendingAlert
}

// afterDeadline is a moment strictly past a fresh check's deadline.
func afterDeadline() time.Time { return created.Add(15*time.Minute + time.Second) }

func TestCreateAndRead(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "zeta")
	stack.destination(testInstance, "alpha")

	stored := stack.check(testInstance, "nightly", "zeta", "alpha")

	if stored.State != heartbeat.StateNew || stored.Paused || stored.AlertSeq != 0 || stored.PendingAlert != nil {
		testInstance.Fatalf("fresh check = %+v, want new, unpaused, no alerts", stored)
	}
	if !stored.AnchorAt.Equal(created) || !stored.CreatedAt.Equal(created) || !stored.UpdatedAt.Equal(created) {
		testInstance.Fatalf("timestamps = anchor %v created %v updated %v, want all %v",
			stored.AnchorAt, stored.CreatedAt, stored.UpdatedAt, created)
	}
	if stored.LastPingAt != nil || stored.LastPingKind != nil || stored.DownSince != nil || stored.DownReason != nil {
		testInstance.Fatalf("fresh check has ping/down fields set: %+v", stored)
	}
	want := []heartbeat.DestinationRef{
		{Name: "alpha", Type: registry.TypeNtfy, Enabled: true},
		{Name: "zeta", Type: registry.TypeNtfy, Enabled: true},
	}
	if !slices.Equal(stored.Destinations, want) {
		testInstance.Fatalf("destinations = %+v, want %+v (name order)", stored.Destinations, want)
	}

	byID, err := stack.store.Get(stack.ctx, stored.ID)
	if err != nil || byID.Name != "nightly" {
		testInstance.Fatalf("Get = (%+v, %v), want the check", byID, err)
	}
	if _, err = stack.store.Get(stack.ctx, absentID); !errors.Is(err, heartbeat.ErrCheckNotFound) {
		testInstance.Fatalf("Get(absent) error = %v, want ErrCheckNotFound", err)
	}
}

func TestCreateOwnerDefaultsToOperatorAndAppOwnerRoundTrips(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")

	operatorOwned := stack.check(testInstance, "nightly", "ops")
	if operatorOwned.OwnerKind != registry.OwnerOperator || operatorOwned.OwnerAppID != nil {
		testInstance.Fatalf("default check owner = %q/%v, want operator/nil", operatorOwned.OwnerKind, operatorOwned.OwnerAppID)
	}

	ownerApp := "billing"
	appOwned, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "weekly", PeriodSeconds: 600, GraceSeconds: 300,
		DestinationNames: []string{"ops"}, OwnerKind: registry.OwnerApp, OwnerAppID: &ownerApp,
	}, created)
	if err != nil {
		testInstance.Fatalf("create app-owned check: %v", err)
	}
	if appOwned.OwnerKind != registry.OwnerApp || appOwned.OwnerAppID == nil || *appOwned.OwnerAppID != ownerApp {
		testInstance.Fatalf("app-owned check owner = %q/%v, want app/%s", appOwned.OwnerKind, appOwned.OwnerAppID, ownerApp)
	}

	pinged, err := stack.store.RecordPing(stack.ctx, "billing", "weekly", heartbeat.PingSuccess, created)
	if err != nil || pinged.OwnerKind != registry.OwnerApp {
		testInstance.Fatalf("RecordPing = (%+v, %v), want the owner to survive a state change", pinged, err)
	}
}

func TestCreateDuplicateReturnsErrCheckExists(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.check(testInstance, "nightly", "ops")

	_, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "nightly", PeriodSeconds: 60, GraceSeconds: 60, DestinationNames: []string{"ops"},
	}, created)

	if !errors.Is(err, heartbeat.ErrCheckExists) {
		testInstance.Fatalf("duplicate create error = %v, want ErrCheckExists", err)
	}
	if rows := stack.count(testInstance, "heartbeat_checks"); rows != 1 {
		testInstance.Fatalf("heartbeat_checks rows = %d, want 1", rows)
	}
}

func TestCreateUnknownDestinationLeavesNoPartialRow(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")

	_, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "nightly", PeriodSeconds: 60, GraceSeconds: 60,
		DestinationNames: []string{"ops", "ghost"},
	}, created)

	if !errors.Is(err, registry.ErrUnknownDestination) {
		testInstance.Fatalf("create error = %v, want ErrUnknownDestination", err)
	}
	if rows := stack.count(testInstance, "heartbeat_checks"); rows != 0 {
		testInstance.Fatalf("heartbeat_checks rows = %d, want 0 (no partial row)", rows)
	}
	if rows := stack.count(testInstance, "heartbeat_check_destinations"); rows != 0 {
		testInstance.Fatalf("heartbeat_check_destinations rows = %d, want 0", rows)
	}
}

// appOwnedDestination inserts a destination owned by ownerApp (its name must
// start with "<ownerApp>_"). The app-scoped destination API exists, but this fixture seeds the row directly to
// stay independent of it.
func (stack fixture) appOwnedDestination(testInstance *testing.T, name string, ownerApp string) {
	testInstance.Helper()
	if _, err := stack.registry.InsertDestination(stack.ctx, registry.StoredDestination{
		Name:          name,
		Type:          registry.TypeNtfy,
		Config:        map[string]string{"topic": "alerts_" + name},
		SealedSecrets: []byte{0x01},
		Enabled:       true,
		OwnerKind:     registry.OwnerApp,
		OwnerAppID:    &ownerApp,
	}); err != nil {
		testInstance.Fatalf("insert app-owned destination %s: %v", name, err)
	}
}

func TestCreateRefusesAnotherAppsDestination(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.appOwnedDestination(testInstance, "other_hook", "other")
	stack.appOwnedDestination(testInstance, "billing_hook", "billing")

	_, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "nightly", PeriodSeconds: 60, GraceSeconds: 60,
		DestinationNames: []string{"ops", "other_hook"},
	}, created)
	if !errors.Is(err, registry.ErrUnknownDestination) {
		testInstance.Fatalf("create error = %v, want ErrUnknownDestination (the same as an absent destination)", err)
	}
	if rows := stack.count(testInstance, "heartbeat_checks"); rows != 0 {
		testInstance.Fatalf("heartbeat_checks rows = %d, want 0 (no partial row)", rows)
	}
	if rows := stack.count(testInstance, "heartbeat_check_destinations"); rows != 0 {
		testInstance.Fatalf("heartbeat_check_destinations rows = %d, want 0", rows)
	}

	// The check's own app, and operator-owned destinations, stay usable.
	stored := stack.check(testInstance, "nightly", "ops", "billing_hook")
	if len(stored.Destinations) != 2 {
		testInstance.Fatalf("destinations = %+v, want ops and billing_hook", stored.Destinations)
	}
}

func TestPatchRefusesAnotherAppsDestination(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.appOwnedDestination(testInstance, "other_hook", "other")
	stack.appOwnedDestination(testInstance, "billing_hook", "billing")
	stored := stack.check(testInstance, "nightly", "ops")

	_, err := stack.store.Patch(stack.ctx, stored.ID,
		heartbeat.CheckPatch{DestinationNames: []string{"ops", "other_hook"}}, created)
	if !errors.Is(err, registry.ErrUnknownDestination) {
		testInstance.Fatalf("patch error = %v, want ErrUnknownDestination", err)
	}
	reread, getErr := stack.store.Get(stack.ctx, stored.ID)
	if getErr != nil || len(reread.Destinations) != 1 || reread.Destinations[0].Name != "ops" {
		testInstance.Fatalf("check after refused patch = (%+v, %v), want destinations unchanged", reread, getErr)
	}

	patched, patchErr := stack.store.Patch(stack.ctx, stored.ID,
		heartbeat.CheckPatch{DestinationNames: []string{"ops", "billing_hook"}}, created)
	if patchErr != nil || len(patched.Destinations) != 2 {
		testInstance.Fatalf("patch to the check's own destination = (%+v, %v)", patched, patchErr)
	}
}

func TestCreateWithoutDestinationsFailsClosed(testInstance *testing.T) {
	stack := newFixture(testInstance)

	_, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: "billing", Name: "nightly", PeriodSeconds: 60, GraceSeconds: 60,
	}, created)

	var validationErr *heartbeat.ValidationError
	if !errors.As(err, &validationErr) {
		testInstance.Fatalf("create with no destinations error = %v, want *ValidationError", err)
	}
	if rows := stack.count(testInstance, "heartbeat_checks"); rows != 0 {
		testInstance.Fatalf("heartbeat_checks rows = %d, want 0", rows)
	}
}

func TestListOrdersByAppThenName(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	for _, spec := range [][2]string{{"billing", "b_job"}, {"alpha", "z_job"}, {"billing", "a_job"}} {
		if _, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
			AppID: spec[0], Name: spec[1], PeriodSeconds: 60, GraceSeconds: 60, DestinationNames: []string{"ops"},
		}, created); err != nil {
			testInstance.Fatalf("create %v: %v", spec, err)
		}
	}

	listed, err := stack.store.List(stack.ctx)
	if err != nil {
		testInstance.Fatalf("List: %v", err)
	}
	var got []string
	for _, check := range listed {
		got = append(got, check.AppID+"/"+check.Name)
	}
	if want := []string{"alpha/z_job", "billing/a_job", "billing/b_job"}; !slices.Equal(got, want) {
		testInstance.Fatalf("List order = %v, want %v", got, want)
	}

	empty := newFixture(testInstance)
	none, err := empty.store.List(empty.ctx)
	if err != nil || none == nil || len(none) != 0 {
		testInstance.Fatalf("List on empty = (%v, %v), want a non-nil empty slice", none, err)
	}
}

func TestPatchKeepsFieldsItDoesNotNameAndValidatesTheMergedResult(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.destination(testInstance, "pager")
	stored := stack.check(testInstance, "nightly", "ops", "pager")
	later := created.Add(time.Hour)
	period := 3600

	patched, err := stack.store.Patch(stack.ctx, stored.ID, heartbeat.CheckPatch{PeriodSeconds: &period}, later)
	if err != nil {
		testInstance.Fatalf("Patch: %v", err)
	}
	if patched.PeriodSeconds != 3600 || patched.GraceSeconds != 300 || !patched.UpdatedAt.Equal(later) || len(patched.Destinations) != 2 {
		testInstance.Fatalf("patched = %+v, want only the period changed", patched)
	}

	tooSmall := 10
	_, err = stack.store.Patch(stack.ctx, stored.ID, heartbeat.CheckPatch{GraceSeconds: &tooSmall}, later)
	var validationErr *heartbeat.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Message != "invalid grace_seconds" {
		testInstance.Fatalf("Patch(invalid grace) error = %v, want invalid grace_seconds", err)
	}
	_, err = stack.store.Patch(stack.ctx, stored.ID, heartbeat.CheckPatch{DestinationNames: []string{"ghost"}}, later)
	if !errors.Is(err, registry.ErrUnknownDestination) {
		testInstance.Fatalf("Patch(unknown destination) error = %v, want ErrUnknownDestination", err)
	}
	afterFailures, err := stack.store.Get(stack.ctx, stored.ID)
	if err != nil || afterFailures.PeriodSeconds != 3600 || afterFailures.GraceSeconds != 300 || len(afterFailures.Destinations) != 2 {
		testInstance.Fatalf("failed patches changed the check: (%+v, %v)", afterFailures, err)
	}

	if _, err = stack.store.Patch(stack.ctx, absentID, heartbeat.CheckPatch{PeriodSeconds: &period}, later); !errors.Is(err, heartbeat.ErrCheckNotFound) {
		testInstance.Fatalf("Patch(absent) error = %v, want ErrCheckNotFound", err)
	}
}

// TestConcurrentPatchesOfDifferentFieldsAllSurvive races patches that each name a
// different field: the merge reads the row under its lock, so none may revert
// another (a read-merge-write outside the lock would write stale values back).
func TestConcurrentPatchesOfDifferentFieldsAllSurvive(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.destination(testInstance, "pager")
	stack.destination(testInstance, "backup")
	period, grace := 7200, 900
	for round := range 8 {
		stored := stack.check(testInstance, "patchrace_"+string(rune('a'+round)), "ops")
		patches := []heartbeat.CheckPatch{
			{PeriodSeconds: &period},
			{GraceSeconds: &grace},
			{DestinationNames: []string{"pager", "backup"}},
		}
		var group sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, len(patches))
		for index := range patches {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				_, errs[index] = stack.store.Patch(stack.ctx, stored.ID, patches[index], created.Add(time.Hour))
			}()
		}
		close(start)
		group.Wait()
		for _, patchErr := range errs {
			if patchErr != nil {
				testInstance.Fatalf("round %d: concurrent patch failed: %v", round, patchErr)
			}
		}

		after, err := stack.store.Get(stack.ctx, stored.ID)
		if err != nil {
			testInstance.Fatalf("round %d: get: %v", round, err)
		}
		names := []string{}
		for _, destination := range after.Destinations {
			names = append(names, destination.Name)
		}
		if after.PeriodSeconds != period || after.GraceSeconds != grace || !slices.Equal(names, []string{"backup", "pager"}) {
			testInstance.Fatalf("round %d: after = period %d grace %d destinations %v, want 7200, 900 and [backup pager]",
				round, after.PeriodSeconds, after.GraceSeconds, names)
		}
	}
}

func TestDeleteIsIdempotentAndCascadesDestinationLinks(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stored := stack.check(testInstance, "nightly", "ops")
	if links := stack.count(testInstance, "heartbeat_check_destinations"); links != 1 {
		testInstance.Fatalf("links before delete = %d, want 1", links)
	}

	deleted, err := stack.store.Delete(stack.ctx, stored.ID)
	if err != nil || !deleted {
		testInstance.Fatalf("Delete = (%v, %v), want (true, nil)", deleted, err)
	}
	if links := stack.count(testInstance, "heartbeat_check_destinations"); links != 0 {
		testInstance.Fatalf("links after delete = %d, want 0 (cascade)", links)
	}
	deleted, err = stack.store.Delete(stack.ctx, stored.ID)
	if err != nil || deleted {
		testInstance.Fatalf("second Delete = (%v, %v), want (false, nil)", deleted, err)
	}
	if _, err = stack.registry.GetDestination(stack.ctx, "ops"); err != nil {
		testInstance.Fatalf("the destination did not survive the check delete: %v", err)
	}
}

func TestDeletingDestinationInUseByACheckIsRefused(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stored := stack.check(testInstance, "nightly", "ops")

	deleted, err := stack.registry.DeleteDestination(stack.ctx, "ops")

	if !errors.Is(err, registry.ErrDestinationInUse) || deleted {
		testInstance.Fatalf("DeleteDestination = (%v, %v), want (false, ErrDestinationInUse)", deleted, err)
	}
	if err.Error() != "destination is in use" {
		testInstance.Fatalf("error text = %q, want the reference-agnostic %q", err.Error(), "destination is in use")
	}
	if _, err = stack.registry.GetDestination(stack.ctx, "ops"); err != nil {
		testInstance.Fatalf("the destination did not survive: %v", err)
	}
	survivor, err := stack.store.Get(stack.ctx, stored.ID)
	if err != nil || len(survivor.Destinations) != 1 || survivor.Destinations[0].Name != "ops" {
		testInstance.Fatalf("the check or its link did not survive: (%+v, %v)", survivor, err)
	}
}

func TestRecordPingAndRecovery(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stored := stack.check(testInstance, "nightly", "ops")

	pinged, err := stack.store.RecordPing(stack.ctx, "billing", "nightly", heartbeat.PingSuccess, created.Add(time.Minute))
	if err != nil {
		testInstance.Fatalf("RecordPing: %v", err)
	}
	if pinged.State != heartbeat.StateUp || pinged.AlertSeq != 0 || pinged.PendingAlert != nil {
		testInstance.Fatalf("after first ping = %+v, want up with no alert", pinged)
	}
	if pinged.LastPingAt == nil || !pinged.LastPingAt.Equal(created.Add(time.Minute)) || !pinged.AnchorAt.Equal(created.Add(time.Minute)) {
		testInstance.Fatalf("ping not recorded: %+v", pinged)
	}
	if len(pinged.Destinations) != 1 {
		testInstance.Fatalf("destinations = %+v, want the joined destination", pinged.Destinations)
	}

	failed, err := stack.store.RecordPing(stack.ctx, "billing", "nightly", heartbeat.PingFail, created.Add(2*time.Minute))
	if err != nil {
		testInstance.Fatalf("RecordPing(fail): %v", err)
	}
	if failed.State != heartbeat.StateDown || failed.AlertSeq != 1 || pendingOf(failed) != heartbeat.AlertDown ||
		failed.DownReason == nil || *failed.DownReason != heartbeat.ReasonFail {
		testInstance.Fatalf("after fail ping = %+v, want down/fail with alert 1", failed)
	}

	again, err := stack.store.RecordPing(stack.ctx, "billing", "nightly", heartbeat.PingFail, created.Add(3*time.Minute))
	if err != nil || again.AlertSeq != 1 {
		testInstance.Fatalf("fail while down = (%+v, %v), want no new alert", again, err)
	}

	recovered, err := stack.store.RecordPing(stack.ctx, "billing", "nightly", heartbeat.PingSuccess, created.Add(4*time.Minute))
	if err != nil {
		testInstance.Fatalf("RecordPing(recover): %v", err)
	}
	if recovered.State != heartbeat.StateUp || recovered.AlertSeq != 2 || pendingOf(recovered) != heartbeat.AlertRecovered {
		testInstance.Fatalf("after recovery = %+v, want up with recovered alert 2", recovered)
	}
	if recovered.ID != stored.ID {
		testInstance.Fatalf("ping hit a different check")
	}

	if _, err = stack.store.RecordPing(stack.ctx, "billing", "ghost", heartbeat.PingSuccess, created); !errors.Is(err, heartbeat.ErrCheckNotFound) {
		testInstance.Fatalf("RecordPing(unknown check) error = %v, want ErrCheckNotFound", err)
	}
	var validationErr *heartbeat.ValidationError
	if _, err = stack.store.RecordPing(stack.ctx, "billing", "nightly", "start", created); !errors.As(err, &validationErr) {
		testInstance.Fatalf("RecordPing(bad kind) error = %v, want *ValidationError", err)
	}
}

func TestAdvanceDueOnlyTakesDownDueUnpausedChecks(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	due := stack.check(testInstance, "due", "ops")
	stack.check(testInstance, "not_due", "ops")
	paused := stack.check(testInstance, "paused", "ops")
	alreadyDown := stack.check(testInstance, "already_down", "ops")

	if _, err := stack.store.RecordPing(stack.ctx, "billing", "not_due", heartbeat.PingSuccess, afterDeadline()); err != nil {
		testInstance.Fatalf("ping not_due: %v", err)
	}
	if _, err := stack.store.Pause(stack.ctx, paused.ID, created.Add(time.Minute)); err != nil {
		testInstance.Fatalf("pause: %v", err)
	}
	if _, err := stack.store.RecordPing(stack.ctx, "billing", "already_down", heartbeat.PingFail, created.Add(time.Minute)); err != nil {
		testInstance.Fatalf("fail already_down: %v", err)
	}

	tickAt := afterDeadline().Add(time.Second)
	advanced, err := stack.store.AdvanceDue(stack.ctx, tickAt)
	if err != nil {
		testInstance.Fatalf("AdvanceDue: %v", err)
	}
	if len(advanced) != 1 || advanced[0].ID != due.ID {
		testInstance.Fatalf("AdvanceDue advanced %+v, want only the due check", advanced)
	}
	got := advanced[0]
	if got.State != heartbeat.StateDown || got.AlertSeq != 1 || pendingOf(got) != heartbeat.AlertDown ||
		got.DownReason == nil || *got.DownReason != heartbeat.ReasonMissed || len(got.Destinations) != 1 {
		testInstance.Fatalf("advanced check = %+v, want down/missed alert 1 with destinations", got)
	}

	pausedAfter, err := stack.store.Get(stack.ctx, paused.ID)
	if err != nil || pausedAfter.State != heartbeat.StateNew || pausedAfter.PendingAlert != nil {
		testInstance.Fatalf("paused tick moved the check: (%+v, %v)", pausedAfter, err)
	}
	downAfter, err := stack.store.Get(stack.ctx, alreadyDown.ID)
	if err != nil || downAfter.AlertSeq != 1 {
		testInstance.Fatalf("already-down check re-alerted: (%+v, %v)", downAfter, err)
	}

	second, err := stack.store.AdvanceDue(stack.ctx, tickAt.Add(time.Minute))
	if err != nil || len(second) != 0 {
		testInstance.Fatalf("second AdvanceDue = (%+v, %v), want nothing (no repeat alert)", second, err)
	}
}

func TestAdvanceDueDeadlineBoundaryIsStrict(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.check(testInstance, "nightly", "ops")
	deadline := created.Add(15 * time.Minute)

	atDeadline, err := stack.store.AdvanceDue(stack.ctx, deadline)
	if err != nil || len(atDeadline) != 0 {
		testInstance.Fatalf("AdvanceDue at the deadline = (%+v, %v), want nothing", atDeadline, err)
	}
	pastDeadline, err := stack.store.AdvanceDue(stack.ctx, deadline.Add(time.Microsecond))
	if err != nil || len(pastDeadline) != 1 {
		testInstance.Fatalf("AdvanceDue just past the deadline = (%+v, %v), want the check", pastDeadline, err)
	}
}

func TestPausedPingMovesStateWithoutAlertAndResumeResetsAnchor(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stored := stack.check(testInstance, "nightly", "ops")
	if _, err := stack.store.RecordPing(stack.ctx, "billing", "nightly", heartbeat.PingFail, created.Add(time.Minute)); err != nil {
		testInstance.Fatalf("fail ping: %v", err)
	}
	if _, err := stack.store.Pause(stack.ctx, stored.ID, created.Add(2*time.Minute)); err != nil {
		testInstance.Fatalf("pause: %v", err)
	}

	pinged, err := stack.store.RecordPing(stack.ctx, "billing", "nightly", heartbeat.PingSuccess, created.Add(3*time.Minute))
	if err != nil {
		testInstance.Fatalf("ping while paused: %v", err)
	}
	if pinged.State != heartbeat.StateUp || !pinged.Paused || pinged.PendingAlert != nil {
		testInstance.Fatalf("ping while paused = %+v, want up, still paused, no pending alert", pinged)
	}
	if pinged.AlertSeq != 1 {
		testInstance.Fatalf("AlertSeq = %d, want 1 (the pre-pause alert only)", pinged.AlertSeq)
	}

	resumeAt := created.Add(time.Hour)
	resumed, err := stack.store.Resume(stack.ctx, stored.ID, resumeAt)
	if err != nil {
		testInstance.Fatalf("resume: %v", err)
	}
	if resumed.Paused || !resumed.AnchorAt.Equal(resumeAt) || resumed.PendingAlert != nil || resumed.AlertSeq != 1 {
		testInstance.Fatalf("resumed = %+v, want unpaused, anchor %v, nothing emitted", resumed, resumeAt)
	}

	if _, err = stack.store.Pause(stack.ctx, absentID, created); !errors.Is(err, heartbeat.ErrCheckNotFound) {
		testInstance.Fatalf("Pause(absent) error = %v, want ErrCheckNotFound", err)
	}
	if _, err = stack.store.Resume(stack.ctx, absentID, created); !errors.Is(err, heartbeat.ErrCheckNotFound) {
		testInstance.Fatalf("Resume(absent) error = %v, want ErrCheckNotFound", err)
	}
}

// TestConcurrentCallsEmitExactlyOneAlert races repeated simultaneous callers on
// one due check; every round must end with exactly one alert_seq increment.
func TestConcurrentCallsEmitExactlyOneAlert(testInstance *testing.T) {
	scenarios := []struct {
		name string
		call func(stack fixture, check heartbeat.StoredCheck) error
	}{
		{"two ticks", func(stack fixture, _ heartbeat.StoredCheck) error {
			_, err := stack.store.AdvanceDue(stack.ctx, afterDeadline())
			return err
		}},
		{"two fail pings", func(stack fixture, check heartbeat.StoredCheck) error {
			_, err := stack.store.RecordPing(stack.ctx, check.AppID, check.Name, heartbeat.PingFail, created.Add(time.Minute))
			return err
		}},
	}
	for _, scenario := range scenarios {
		testInstance.Run(scenario.name, func(subTest *testing.T) {
			stack := newFixture(subTest)
			stack.destination(subTest, "ops")
			for round := range 5 {
				stored := stack.check(subTest, "round_"+string(rune('a'+round)), "ops")
				var group sync.WaitGroup
				start := make(chan struct{})
				errs := make([]error, 4)
				for caller := range errs {
					group.Add(1)
					go func() {
						defer group.Done()
						<-start
						errs[caller] = scenario.call(stack, stored)
					}()
				}
				close(start)
				group.Wait()
				for _, callErr := range errs {
					if callErr != nil {
						subTest.Fatalf("round %d: concurrent call failed: %v", round, callErr)
					}
				}

				after, err := stack.store.Get(stack.ctx, stored.ID)
				if err != nil {
					subTest.Fatalf("round %d: get: %v", round, err)
				}
				if after.State != heartbeat.StateDown || after.AlertSeq != 1 || pendingOf(after) != heartbeat.AlertDown {
					subTest.Fatalf("round %d: after = %+v, want down with exactly alert_seq 1", round, after)
				}
			}
		})
	}
}

// TestConcurrentFailPingAndTickEmitExactlyOneAlert races a fail ping against a
// tick on one due check: whichever lands first takes the check down, and the
// other must find it already down and emit nothing.
func TestConcurrentFailPingAndTickEmitExactlyOneAlert(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	for round := range 8 {
		stored := stack.check(testInstance, "failrace_"+string(rune('a'+round)), "ops")
		var group sync.WaitGroup
		start := make(chan struct{})
		var pingErr, tickErr error
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, pingErr = stack.store.RecordPing(stack.ctx, stored.AppID, stored.Name, heartbeat.PingFail, afterDeadline())
		}()
		go func() {
			defer group.Done()
			<-start
			_, tickErr = stack.store.AdvanceDue(stack.ctx, afterDeadline())
		}()
		close(start)
		group.Wait()
		if pingErr != nil || tickErr != nil {
			testInstance.Fatalf("round %d: ping = %v, tick = %v", round, pingErr, tickErr)
		}

		after, err := stack.store.Get(stack.ctx, stored.ID)
		if err != nil {
			testInstance.Fatalf("round %d: get: %v", round, err)
		}
		if after.State != heartbeat.StateDown || after.AlertSeq != 1 || pendingOf(after) != heartbeat.AlertDown {
			testInstance.Fatalf("round %d: after = %+v, want down with exactly alert_seq 1", round, after)
		}
	}
}

// TestConcurrentPingAndTickSerialize races a success ping
// against a tick on one due check. Either the ping lands first (the check is
// up, and the tick finds it no longer due) or the tick lands first (down, then
// recovered): never two alerts of the same kind.
func TestConcurrentPingAndTickSerialize(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	for round := range 8 {
		stored := stack.check(testInstance, "race_"+string(rune('a'+round)), "ops")
		var group sync.WaitGroup
		start := make(chan struct{})
		var pingErr, tickErr error
		pingAt := afterDeadline()
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, pingErr = stack.store.RecordPing(stack.ctx, stored.AppID, stored.Name, heartbeat.PingSuccess, pingAt)
		}()
		go func() {
			defer group.Done()
			<-start
			_, tickErr = stack.store.AdvanceDue(stack.ctx, pingAt)
		}()
		close(start)
		group.Wait()
		if pingErr != nil || tickErr != nil {
			testInstance.Fatalf("round %d: ping = %v, tick = %v", round, pingErr, tickErr)
		}

		after, err := stack.store.Get(stack.ctx, stored.ID)
		if err != nil {
			testInstance.Fatalf("round %d: get: %v", round, err)
		}
		switch after.AlertSeq {
		case 0:
			if after.State != heartbeat.StateUp || after.PendingAlert != nil {
				testInstance.Fatalf("round %d: ping first, but state = %+v", round, after)
			}
		case 2:
			if after.State != heartbeat.StateUp || pendingOf(after) != heartbeat.AlertRecovered {
				testInstance.Fatalf("round %d: tick then ping, but state = %+v", round, after)
			}
		default:
			testInstance.Fatalf("round %d: alert_seq = %d, want 0 (ping won) or 2 (tick then ping)", round, after.AlertSeq)
		}
	}
}

func TestPendingAlertOverwrittenByRecoveryBeforeFlush(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stored := stack.check(testInstance, "nightly", "ops")

	// The tick takes the check down; the flush hits a transient Submit error so
	// pending_alert='down' stays unflushed.
	if advanced, err := stack.store.AdvanceDue(stack.ctx, afterDeadline()); err != nil || len(advanced) != 1 {
		testInstance.Fatalf("AdvanceDue = (%+v, %v), want the check", advanced, err)
	}
	pendingBefore, err := stack.store.ListPendingAlerts(stack.ctx)
	if err != nil || len(pendingBefore) != 1 || pendingOf(pendingBefore[0]) != heartbeat.AlertDown || pendingBefore[0].AlertSeq != 1 {
		testInstance.Fatalf("pending after down = (%+v, %v), want one down alert seq 1", pendingBefore, err)
	}

	// A success ping arrives before the flush is retried.
	if _, err = stack.store.RecordPing(stack.ctx, stored.AppID, stored.Name, heartbeat.PingSuccess, afterDeadline().Add(time.Minute)); err != nil {
		testInstance.Fatalf("recovery ping: %v", err)
	}

	// Simulate the checker's flush loop: every pending alert it sees is "submitted"
	// and cleared by its own alert_seq.
	var submitted []string
	pending, err := stack.store.ListPendingAlerts(stack.ctx)
	if err != nil {
		testInstance.Fatalf("ListPendingAlerts: %v", err)
	}
	for _, check := range pending {
		submitted = append(submitted, pendingOf(check))
		if cleared, clearErr := stack.store.ClearPendingAlert(stack.ctx, check.ID, check.AlertSeq); clearErr != nil || !cleared {
			testInstance.Fatalf("ClearPendingAlert = (%v, %v), want (true, nil)", cleared, clearErr)
		}
	}
	if !slices.Equal(submitted, []string{heartbeat.AlertRecovered}) {
		testInstance.Fatalf("submitted alerts = %v, want exactly the recovery (never both)", submitted)
	}
	if len(pending) != 1 || pending[0].AlertSeq != 2 {
		testInstance.Fatalf("pending = %+v, want one alert with a fresh alert_seq 2", pending)
	}

	// Nothing is left to announce, on this or any later flush.
	if again, listErr := stack.store.ListPendingAlerts(stack.ctx); listErr != nil || len(again) != 0 {
		testInstance.Fatalf("pending after flush = (%+v, %v), want none", again, listErr)
	}
}

func TestClearPendingAlertWithStaleSeqLeavesNewerAlertIntact(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stored := stack.check(testInstance, "nightly", "ops")
	if _, err := stack.store.RecordPing(stack.ctx, stored.AppID, stored.Name, heartbeat.PingFail, created.Add(time.Minute)); err != nil {
		testInstance.Fatalf("fail ping: %v", err)
	}
	// The flush read alert_seq 1, then a recovery bumped it to 2 before it cleared.
	if _, err := stack.store.RecordPing(stack.ctx, stored.AppID, stored.Name, heartbeat.PingSuccess, created.Add(2*time.Minute)); err != nil {
		testInstance.Fatalf("recovery ping: %v", err)
	}

	cleared, err := stack.store.ClearPendingAlert(stack.ctx, stored.ID, 1)
	if err != nil || cleared {
		testInstance.Fatalf("ClearPendingAlert(stale) = (%v, %v), want (false, nil)", cleared, err)
	}
	intact, err := stack.store.Get(stack.ctx, stored.ID)
	if err != nil || pendingOf(intact) != heartbeat.AlertRecovered || intact.AlertSeq != 2 {
		testInstance.Fatalf("newer alert was disturbed: (%+v, %v)", intact, err)
	}

	cleared, err = stack.store.ClearPendingAlert(stack.ctx, stored.ID, 2)
	if err != nil || !cleared {
		testInstance.Fatalf("ClearPendingAlert(current) = (%v, %v), want (true, nil)", cleared, err)
	}
	cleared, err = stack.store.ClearPendingAlert(stack.ctx, stored.ID, 2)
	if err != nil || cleared {
		testInstance.Fatalf("second ClearPendingAlert = (%v, %v), want (false, nil)", cleared, err)
	}
}

func TestPauseClearsAStalePendingAlert(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stored := stack.check(testInstance, "nightly", "ops")

	// The check goes down and the flush fails transiently: pending_alert='down'.
	if _, err := stack.store.AdvanceDue(stack.ctx, afterDeadline()); err != nil {
		testInstance.Fatalf("AdvanceDue: %v", err)
	}
	if pending, err := stack.store.ListPendingAlerts(stack.ctx); err != nil || len(pending) != 1 {
		testInstance.Fatalf("pending before pause = (%+v, %v), want one", pending, err)
	}

	paused, err := stack.store.Pause(stack.ctx, stored.ID, afterDeadline().Add(time.Second))
	if err != nil {
		testInstance.Fatalf("Pause: %v", err)
	}
	if paused.PendingAlert != nil || !paused.Paused || paused.AlertSeq != 1 {
		testInstance.Fatalf("paused = %+v, want the outbox cleared by the pause", paused)
	}

	// Neither an immediate flush attempt nor a later tick nor the resume announces it.
	if pending, err := stack.store.ListPendingAlerts(stack.ctx); err != nil || len(pending) != 0 {
		testInstance.Fatalf("pending after pause = (%+v, %v), want none", pending, err)
	}
	if advanced, err := stack.store.AdvanceDue(stack.ctx, afterDeadline().Add(24*time.Hour)); err != nil || len(advanced) != 0 {
		testInstance.Fatalf("tick while paused = (%+v, %v), want nothing", advanced, err)
	}
	if _, err = stack.store.Resume(stack.ctx, stored.ID, afterDeadline().Add(25*time.Hour)); err != nil {
		testInstance.Fatalf("Resume: %v", err)
	}
	if pending, err := stack.store.ListPendingAlerts(stack.ctx); err != nil || len(pending) != 0 {
		testInstance.Fatalf("pending after resume = (%+v, %v), want none", pending, err)
	}
	resumed, err := stack.store.Get(stack.ctx, stored.ID)
	if err != nil || resumed.State != heartbeat.StateDown || resumed.AlertSeq != 1 {
		testInstance.Fatalf("resumed = (%+v, %v), want still down with no new alert", resumed, err)
	}
}

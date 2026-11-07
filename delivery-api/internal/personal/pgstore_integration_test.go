//go:build integration

package personal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	storeTestBudget = 60 * time.Second
	personOne       = "abcdefghijklmnop"
	personTwo       = "bcdefghijklmnopq"
	raceIterations  = 40
)

func newTestStore(ctx context.Context, testInstance *testing.T) (*PGStore, *pgxpool.Pool, *seal.Sealer) {
	testInstance.Helper()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	sealer, sealerErr := seal.New(bytes.Repeat([]byte{0x24}, seal.KeySize))
	if sealerErr != nil {
		testInstance.Fatalf("seal.New: %v", sealerErr)
	}
	return NewPGStore(pool, sealer), pool, sealer
}

func newDestination(personHash, appID, label string, optIn bool) NewDestination {
	return NewDestination{
		PersonHash: personHash, RegisteredByAppID: appID, Label: label, Type: TypeDiscord,
		Config:  map[string]string{},
		Secrets: map[string]string{"url": "https://discord.com/api/webhooks/1/" + label},
		OptIn:   optIn,
	}
}

func mustInsert(ctx context.Context, testInstance *testing.T, store *PGStore, input NewDestination) Destination {
	testInstance.Helper()
	inserted, insertErr := store.Insert(ctx, input)
	if insertErr != nil {
		testInstance.Fatalf("Insert %q: %v", input.Label, insertErr)
	}
	return inserted
}

func markRegistrarRemoved(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, id int64) {
	testInstance.Helper()
	if _, execErr := pool.Exec(ctx,
		`UPDATE personal_destinations SET registrar_removed_at = now() WHERE id = $1`, id); execErr != nil {
		testInstance.Fatalf("mark registrar removed: %v", execErr)
	}
}

func TestInsertSealsSecretsUnderTheFinalID(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, sealer := newTestStore(ctx, testInstance)

	first := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "first", true))
	second := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "second", false))

	got, getErr := store.Get(ctx, first.ID)
	if getErr != nil {
		testInstance.Fatalf("Get: %v", getErr)
	}
	if got.PersonHash != personOne || got.Label != "first" || got.Type != TypeDiscord || !got.Enabled || got.DisabledBy != nil {
		testInstance.Fatalf("round trip = %+v", got)
	}
	secrets, openErr := OpenSecrets(sealer, got.ID, got.SealedSecrets)
	if openErr != nil || secrets["url"] != "https://discord.com/api/webhooks/1/first" {
		testInstance.Fatalf("OpenSecrets = (%v, %v)", secrets, openErr)
	}
	if _, crossErr := OpenSecrets(sealer, second.ID, got.SealedSecrets); !errors.Is(crossErr, seal.ErrDecrypt) {
		testInstance.Fatalf("opening row %d secrets as row %d error = %v, want ErrDecrypt", first.ID, second.ID, crossErr)
	}
	if optedIn, optErr := store.IsOptedIn(ctx, "urls4irl", first.ID); optErr != nil || !optedIn {
		testInstance.Fatalf("IsOptedIn(first) = (%v, %v), want true", optedIn, optErr)
	}
	if optedIn, optErr := store.IsOptedIn(ctx, "urls4irl", second.ID); optErr != nil || optedIn {
		testInstance.Fatalf("IsOptedIn(second) = (%v, %v), want false", optedIn, optErr)
	}
}

func TestInsertRefusesNtfyType(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	input := newDestination(personOne, "urls4irl", "ntfy", true)
	input.Type = "ntfy"
	if _, insertErr := store.Insert(ctx, input); !errors.Is(insertErr, registry.ErrCheckViolation) {
		testInstance.Fatalf("Insert of an ntfy personal destination error = %v, want a CHECK violation", insertErr)
	}
	if count, countErr := store.CountByPerson(ctx, personOne); countErr != nil || count != 0 {
		testInstance.Fatalf("CountByPerson = (%d, %v), want 0 after a rolled-back insert", count, countErr)
	}
}

func TestListEnabledOptedInFiltersByAppPersonAndEnabled(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	wanted := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "wanted", true))
	disabled := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "disabled", true))
	mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "no-optin", false))
	otherApp := mustInsert(ctx, testInstance, store, newDestination(personOne, "chores4irl", "other-app", true))
	mustInsert(ctx, testInstance, store, newDestination(personTwo, "urls4irl", "other-person", true))
	if _, disableErr := store.DisableBySystem(ctx, disabled.ID, "Endpoint returned 410 Gone."); disableErr != nil {
		testInstance.Fatalf("DisableBySystem: %v", disableErr)
	}
	// A second opt-in from urls4irl on a chores4irl-registered row makes it listed too.
	if optErr := store.OptIn(ctx, "urls4irl", otherApp.ID); optErr != nil {
		testInstance.Fatalf("OptIn: %v", optErr)
	}

	listed, listErr := store.ListEnabledOptedIn(ctx, "urls4irl", personOne)
	if listErr != nil {
		testInstance.Fatalf("ListEnabledOptedIn: %v", listErr)
	}
	gotIDs := make([]int64, 0, len(listed))
	for _, destination := range listed {
		gotIDs = append(gotIDs, destination.ID)
	}
	if want := []int64{wanted.ID, otherApp.ID}; !slices.Equal(gotIDs, want) {
		testInstance.Fatalf("ListEnabledOptedIn ids = %v, want %v", gotIDs, want)
	}

	chores, choresErr := store.ListEnabledOptedIn(ctx, "chores4irl", personOne)
	if choresErr != nil || len(chores) != 1 || chores[0].ID != otherApp.ID {
		testInstance.Fatalf("ListEnabledOptedIn(chores4irl) = (%v, %v), want only the chores4irl row", chores, choresErr)
	}
}

func TestListByPersonNewestFirstAndAdminListTotal(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	older := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "older", true))
	newer := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "newer", true))
	mustInsert(ctx, testInstance, store, newDestination(personTwo, "urls4irl", "elsewhere", true))

	listed, listErr := store.ListByPerson(ctx, personOne)
	if listErr != nil || len(listed) != 2 || listed[0].ID != newer.ID || listed[1].ID != older.ID {
		testInstance.Fatalf("ListByPerson = (%v, %v), want newest first", listed, listErr)
	}
	all, total, adminErr := store.ListForAdmin(ctx, "")
	if adminErr != nil || len(all) != 3 || total != 3 {
		testInstance.Fatalf("ListForAdmin all = (%d rows, total %d, %v), want 3 and 3", len(all), total, adminErr)
	}
	filtered, filteredTotal, filteredErr := store.ListForAdmin(ctx, personTwo)
	if filteredErr != nil || len(filtered) != 1 || filteredTotal != 1 {
		testInstance.Fatalf("ListForAdmin person = (%d rows, total %d, %v), want 1 and 1", len(filtered), filteredTotal, filteredErr)
	}
}

func TestConcurrentCreatesNeverExceedThePersonCap(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	for index := 0; index < MaxPerPersonPerApp-1; index++ {
		mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", fmt.Sprintf("seed%d", index), true))
	}

	const racers = 4
	errs := make([]error, racers)
	var group sync.WaitGroup
	start := make(chan struct{})
	for index := range racers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, errs[index] = store.Insert(ctx, newDestination(personOne, "urls4irl", fmt.Sprintf("racer%d", index), true))
		}()
	}
	close(start)
	group.Wait()

	succeeded, limited := 0, 0
	for _, insertErr := range errs {
		switch {
		case insertErr == nil:
			succeeded++
		case errors.Is(insertErr, ErrLimitReached):
			limited++
		default:
			testInstance.Errorf("unexpected insert error: %v", insertErr)
		}
	}
	if succeeded != 1 || limited != racers-1 {
		testInstance.Fatalf("succeeded=%d limited=%d, want 1 and %d", succeeded, limited, racers-1)
	}
	if count, countErr := store.CountByPerson(ctx, personOne); countErr != nil || count != MaxPerPersonPerApp {
		testInstance.Fatalf("CountByPerson = (%d, %v), want %d", count, countErr, MaxPerPersonPerApp)
	}
	// the cap is per registering app: another app still registers for the same person
	mustInsert(ctx, testInstance, store, newDestination(personOne, "otherapp", "other-app-row", true))
}

func TestOptOutReapsOnlyARegistrarRemovedRowOnItsLastOptOut(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, pool, _ := newTestStore(ctx, testInstance)

	live := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "live", true))
	reaped, optOutErr := store.OptOut(ctx, "urls4irl", live.ID)
	if optOutErr != nil || reaped {
		testInstance.Fatalf("OptOut on a live-registrar row = (%v, %v), want (false, nil)", reaped, optOutErr)
	}
	if _, getErr := store.Get(ctx, live.ID); getErr != nil {
		testInstance.Fatalf("live-registrar row was deleted by an opt-out: %v", getErr)
	}
	if again, againErr := store.OptOut(ctx, "urls4irl", live.ID); againErr != nil || again {
		testInstance.Fatalf("repeat OptOut = (%v, %v), want idempotent (false, nil)", again, againErr)
	}

	orphaned := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "orphaned", true))
	if optErr := store.OptIn(ctx, "chores4irl", orphaned.ID); optErr != nil {
		testInstance.Fatalf("OptIn: %v", optErr)
	}
	markRegistrarRemoved(ctx, testInstance, pool, orphaned.ID)

	if reapedFirst, firstErr := store.OptOut(ctx, "urls4irl", orphaned.ID); firstErr != nil || reapedFirst {
		testInstance.Fatalf("first OptOut = (%v, %v), want (false, nil): another app is still opted in", reapedFirst, firstErr)
	}
	if reapedLast, lastErr := store.OptOut(ctx, "chores4irl", orphaned.ID); lastErr != nil || !reapedLast {
		testInstance.Fatalf("last OptOut = (%v, %v), want (true, nil)", reapedLast, lastErr)
	}
	if _, getErr := store.Get(ctx, orphaned.ID); !errors.Is(getErr, ErrNotFound) {
		testInstance.Fatalf("Get after the reap error = %v, want ErrNotFound", getErr)
	}
	if _, repeatErr := store.OptOut(ctx, "chores4irl", orphaned.ID); !errors.Is(repeatErr, ErrNotFound) {
		testInstance.Fatalf("OptOut after the reap error = %v, want ErrNotFound", repeatErr)
	}
	if optInErr := store.OptIn(ctx, "chores4irl", orphaned.ID); !errors.Is(optInErr, ErrNotFound) {
		testInstance.Fatalf("OptIn after the reap error = %v, want ErrNotFound", optInErr)
	}
}

func TestReapOrphansDeletesOnlyOrphans(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, pool, _ := newTestStore(ctx, testInstance)

	orphan := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "orphan", false))
	stillUsed := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "used", true))
	live := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "live", false))
	markRegistrarRemoved(ctx, testInstance, pool, orphan.ID)
	markRegistrarRemoved(ctx, testInstance, pool, stillUsed.ID)

	reaped, reapErr := store.ReapOrphans(ctx, []int64{orphan.ID, stillUsed.ID, live.ID})
	if reapErr != nil || !slices.Equal(reaped, []int64{orphan.ID}) {
		testInstance.Fatalf("ReapOrphans = (%v, %v), want [%d]", reaped, reapErr, orphan.ID)
	}
	if empty, emptyErr := store.ReapOrphans(ctx, nil); emptyErr != nil || len(empty) != 0 {
		testInstance.Fatalf("ReapOrphans(nil) = (%v, %v), want nothing", empty, emptyErr)
	}
	if _, getErr := store.Get(ctx, orphan.ID); !errors.Is(getErr, ErrNotFound) {
		testInstance.Fatalf("orphan survived the reap: %v", getErr)
	}
	mustGet(ctx, testInstance, store, stillUsed.ID)
	mustGet(ctx, testInstance, store, live.ID)
}

// TestOptInRacingTheReapNeverErrorsOrLosesTheOptIn runs an opt-in by app B
// against the last opt-out of app A on a registrar-removed row. Either B's
// opt-in commits first (row survives with B opted in) or the reap wins and B gets
// ErrNotFound. Never another error, never a cascaded-away opt-in.
func TestOptInRacingTheReapNeverErrorsOrLosesTheOptIn(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, pool, _ := newTestStore(ctx, testInstance)

	for iteration := range raceIterations {
		// A person holds at most MaxPerPersonPerApp rows per app, so each iteration uses its own person.
		personHash := rewriteToBase32(fmt.Sprintf("race%012d", iteration))
		row := mustInsert(ctx, testInstance, store, newDestination(personHash, "urls4irl", "race", true))
		markRegistrarRemoved(ctx, testInstance, pool, row.ID)

		var optInErr, optOutErr error
		var reaped bool
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			optInErr = store.OptIn(ctx, "chores4irl", row.ID)
		}()
		go func() {
			defer group.Done()
			<-start
			reaped, optOutErr = store.OptOut(ctx, "urls4irl", row.ID)
		}()
		close(start)
		group.Wait()

		if optOutErr != nil {
			testInstance.Fatalf("iteration %d: OptOut error: %v", iteration, optOutErr)
		}
		switch {
		case optInErr == nil:
			if reaped {
				testInstance.Fatalf("iteration %d: opt-in committed but the row was reaped (opt-in cascaded away)", iteration)
			}
			if optedIn, checkErr := store.IsOptedIn(ctx, "chores4irl", row.ID); checkErr != nil || !optedIn {
				testInstance.Fatalf("iteration %d: opt-in lost: (%v, %v)", iteration, optedIn, checkErr)
			}
		case errors.Is(optInErr, ErrNotFound):
			if !reaped {
				testInstance.Fatalf("iteration %d: opt-in saw no row but nothing was reaped", iteration)
			}
		default:
			testInstance.Fatalf("iteration %d: OptIn error = %v, want nil or ErrNotFound", iteration, optInErr)
		}
	}
}

// TestSameAppOptInRacingItsOptOutNeverDeadlocks races an app's opt-in against
// that same app's opt-out of a registrar-removed row.
func TestSameAppOptInRacingItsOptOutNeverDeadlocks(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, pool, _ := newTestStore(ctx, testInstance)

	for iteration := range raceIterations {
		personHash := rewriteToBase32(fmt.Sprintf("same%012d", iteration))
		row := mustInsert(ctx, testInstance, store, newDestination(personHash, "urls4irl", "same", true))
		markRegistrarRemoved(ctx, testInstance, pool, row.ID)

		var optInErr, optOutErr error
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			optInErr = store.OptIn(ctx, "urls4irl", row.ID)
		}()
		go func() {
			defer group.Done()
			<-start
			_, optOutErr = store.OptOut(ctx, "urls4irl", row.ID)
		}()
		close(start)
		group.Wait()

		if optOutErr != nil {
			testInstance.Fatalf("iteration %d: OptOut error: %v", iteration, optOutErr)
		}
		if optInErr != nil && !errors.Is(optInErr, ErrNotFound) {
			testInstance.Fatalf("iteration %d: OptIn error = %v, want nil or ErrNotFound (no deadlock)", iteration, optInErr)
		}
		// Whichever order won, A's last opt-out leaves nothing behind.
		if _, getErr := store.Get(ctx, row.ID); !errors.Is(getErr, ErrNotFound) {
			testInstance.Fatalf("iteration %d: row survived the last opt-out: %v", iteration, getErr)
		}
	}
}

func TestDisableBySystemOnlyTransitionsEnabledRows(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	enabled := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "enabled", true))
	operator := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "operator", true))
	byApp := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "app", true))

	disable := func(id int64, by DisabledBy, reason *string) {
		testInstance.Helper()
		_, updateErr := store.Update(ctx, id, func(current Destination) (Destination, error) {
			current.Enabled = false
			current.DisabledBy = &by
			current.DisabledReason = reason
			return current, nil
		})
		if updateErr != nil {
			testInstance.Fatalf("Update disable: %v", updateErr)
		}
	}
	operatorReason := "abuse"
	disable(operator.ID, DisabledByOperator, &operatorReason)
	disable(byApp.ID, DisabledByApp, nil)

	const goneReason = "Endpoint returned 410 Gone."
	if transitioned, disableErr := store.DisableBySystem(ctx, enabled.ID, goneReason); disableErr != nil || !transitioned {
		testInstance.Fatalf("DisableBySystem(enabled) = (%v, %v), want (true, nil)", transitioned, disableErr)
	}
	got := mustGet(ctx, testInstance, store, enabled.ID)
	if got.Enabled || got.DisabledBy == nil || *got.DisabledBy != DisabledBySystem ||
		got.DisabledReason == nil || *got.DisabledReason != goneReason {
		testInstance.Fatalf("system-disabled row = %+v", got)
	}

	for label, id := range map[string]int64{"operator": operator.ID, "app": byApp.ID, "already system": enabled.ID} {
		before := mustGet(ctx, testInstance, store, id)
		transitioned, disableErr := store.DisableBySystem(ctx, id, goneReason)
		if disableErr != nil || transitioned {
			testInstance.Errorf("DisableBySystem(%s-disabled) = (%v, %v), want (false, nil)", label, transitioned, disableErr)
		}
		after := mustGet(ctx, testInstance, store, id)
		if after.DisabledBy == nil || before.DisabledBy == nil || after.Enabled ||
			!after.UpdatedAt.Equal(before.UpdatedAt) || *after.DisabledBy != *before.DisabledBy ||
			!equalStringPointers(after.DisabledReason, before.DisabledReason) {
			testInstance.Errorf("%s-disabled row changed: before %+v after %+v", label, before, after)
		}
	}
}

func TestUpdateRollsBackOnMutateErrorAndDeleteCascadesOptIns(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	row := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "before", true))
	sentinel := errors.New("rejected")
	if _, updateErr := store.Update(ctx, row.ID, func(current Destination) (Destination, error) {
		current.Label = "after"
		return current, sentinel
	}); !errors.Is(updateErr, sentinel) {
		testInstance.Fatalf("Update error = %v, want the mutate error", updateErr)
	}
	if got := mustGet(ctx, testInstance, store, row.ID); got.Label != "before" {
		testInstance.Fatalf("label = %q after a rejected update, want before", got.Label)
	}
	if _, updateErr := store.Update(ctx, row.ID+999, func(current Destination) (Destination, error) {
		return current, nil
	}); !errors.Is(updateErr, ErrNotFound) {
		testInstance.Fatalf("Update of a missing row error = %v, want ErrNotFound", updateErr)
	}

	if deleted, deleteErr := store.Delete(ctx, row.ID); deleteErr != nil || !deleted {
		testInstance.Fatalf("Delete = (%v, %v), want (true, nil)", deleted, deleteErr)
	}
	if appIDs, listErr := store.OptedInAppIDs(ctx, row.ID); listErr != nil || len(appIDs) != 0 {
		testInstance.Fatalf("OptedInAppIDs after delete = (%v, %v), want none", appIDs, listErr)
	}
	if deleted, deleteErr := store.Delete(ctx, row.ID); deleteErr != nil || deleted {
		testInstance.Fatalf("second Delete = (%v, %v), want (false, nil)", deleted, deleteErr)
	}
}

func TestUpdateSkipsTheWriteWhenNothingChangedAndBumpsOtherwise(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	row := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "same", true))
	unchanged, unchangedErr := store.Update(ctx, row.ID, func(current Destination) (Destination, error) {
		return current, nil
	})
	if unchangedErr != nil || !unchanged.UpdatedAt.Equal(row.UpdatedAt) {
		testInstance.Fatalf("unchanged Update = (%v, %v), updated_at must stay %v", unchanged.UpdatedAt, unchangedErr, row.UpdatedAt)
	}
	changed, changedErr := store.Update(ctx, row.ID, func(current Destination) (Destination, error) {
		current.Label = "different"
		return current, nil
	})
	if changedErr != nil || changed.Label != "different" || !changed.UpdatedAt.After(row.UpdatedAt) {
		testInstance.Fatalf("changed Update = (%+v, %v), updated_at must move past %v", changed, changedErr, row.UpdatedAt)
	}
}

func TestDeleteManagedChecksPersonRegistrarAndRemoval(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, pool, _ := newTestStore(ctx, testInstance)

	row := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "managed", true))
	for _, attempt := range []struct {
		name       string
		appID      string
		personHash string
	}{
		{name: "other app", appID: "other", personHash: personOne},
		{name: "other person", appID: "urls4irl", personHash: personTwo},
	} {
		if deleted, deleteErr := store.DeleteManaged(ctx, row.ID, attempt.appID, attempt.personHash); deleteErr != nil || deleted {
			testInstance.Fatalf("%s: DeleteManaged = (%v, %v), want (false, nil)", attempt.name, deleted, deleteErr)
		}
	}
	markRegistrarRemoved(ctx, testInstance, pool, row.ID)
	if deleted, deleteErr := store.DeleteManaged(ctx, row.ID, "urls4irl", personOne); deleteErr != nil || deleted {
		testInstance.Fatalf("registrar removed: DeleteManaged = (%v, %v), want (false, nil)", deleted, deleteErr)
	}
	live := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "live", true))
	if deleted, deleteErr := store.DeleteManaged(ctx, live.ID, "urls4irl", personOne); deleteErr != nil || !deleted {
		testInstance.Fatalf("registrar: DeleteManaged = (%v, %v), want (true, nil)", deleted, deleteErr)
	}
}

func mustGet(ctx context.Context, testInstance *testing.T, store *PGStore, id int64) Destination {
	testInstance.Helper()
	destination, getErr := store.Get(ctx, id)
	if getErr != nil {
		testInstance.Fatalf("Get %d: %v", id, getErr)
	}
	return destination
}

func equalStringPointers(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// TestInsertFailsClosedWithoutASealer proves a store built with no sealer
// writes nothing.
func TestInsertFailsClosedWithoutASealer(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	_, pool, _ := newTestStore(ctx, testInstance)
	unsealed := NewPGStore(pool, nil)

	if _, insertErr := unsealed.Insert(ctx, newDestination(personOne, "urls4irl", "x", true)); insertErr == nil {
		testInstance.Fatal("Insert with no sealer succeeded")
	}
	if count, countErr := unsealed.CountByPerson(ctx, personOne); countErr != nil || count != 0 {
		testInstance.Fatalf("CountByPerson = (%d, %v), want 0", count, countErr)
	}
}

func TestOptedInAppIDsAreSorted(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	row := mustInsert(ctx, testInstance, store, newDestination(personOne, "urls4irl", "sorted", true))
	for _, appID := range []string{"zeta", "alpha_two", "alpha"} {
		if optErr := store.OptIn(ctx, appID, row.ID); optErr != nil {
			testInstance.Fatalf("OptIn %s: %v", appID, optErr)
		}
	}
	appIDs, listErr := store.OptedInAppIDs(ctx, row.ID)
	if want := []string{"alpha", "alpha_two", "urls4irl", "zeta"}; listErr != nil || !slices.Equal(appIDs, want) {
		testInstance.Fatalf("OptedInAppIDs = (%v, %v), want %v", appIDs, listErr, want)
	}
}

func rowExists(ctx context.Context, testInstance *testing.T, store *PGStore, id int64) bool {
	testInstance.Helper()
	_, getErr := store.Get(ctx, id)
	if getErr != nil && !errors.Is(getErr, ErrNotFound) {
		testInstance.Fatalf("Get %d: %v", id, getErr)
	}
	return getErr == nil
}

// TestForgetPersonForAppScopesDeletesToTheCallingApp covers the four cases of the plan's test (d) plus
// the person scoping.
func TestForgetPersonForAppScopesDeletesToTheCallingApp(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, pool, _ := newTestStore(ctx, testInstance)

	// Registered by B with opt_in:false, no opt-ins at all: untouched.
	bNoOptIn := mustInsert(ctx, testInstance, store, newDestination(personOne, "app_b", "b-none", false))
	// Registered by A, only opt-in is A's: deleted.
	aOwn := mustInsert(ctx, testInstance, store, newDestination(personOne, "app_a", "a-own", true))
	// Registered by B (live), only opt-in is A's: opt-in goes, the row lingers.
	bLive := mustInsert(ctx, testInstance, store, newDestination(personOne, "app_b", "b-live", false))
	if optErr := store.OptIn(ctx, "app_a", bLive.ID); optErr != nil {
		testInstance.Fatalf("OptIn: %v", optErr)
	}
	// Registrar removed earlier, only opt-in is A's: deleted.
	orphaned := mustInsert(ctx, testInstance, store, newDestination(personOne, "app_c", "orphan", false))
	if optErr := store.OptIn(ctx, "app_a", orphaned.ID); optErr != nil {
		testInstance.Fatalf("OptIn: %v", optErr)
	}
	markRegistrarRemoved(ctx, testInstance, pool, orphaned.ID)
	// Registered by A and still used by B: A's opt-in goes, the row stays.
	aShared := mustInsert(ctx, testInstance, store, newDestination(personOne, "app_a", "a-shared", true))
	if optErr := store.OptIn(ctx, "app_b", aShared.ID); optErr != nil {
		testInstance.Fatalf("OptIn: %v", optErr)
	}
	// Another person's row registered by A: untouched.
	otherPerson := mustInsert(ctx, testInstance, store, newDestination(personTwo, "app_a", "other", true))

	optIns, destinations, forgetErr := store.ForgetPersonForApp(ctx, "app_a", personOne)
	if forgetErr != nil {
		testInstance.Fatalf("ForgetPersonForApp: %v", forgetErr)
	}
	if optIns != 4 || destinations != 2 {
		testInstance.Fatalf("counts = %d opt-ins, %d destinations; want 4 and 2", optIns, destinations)
	}
	if !rowExists(ctx, testInstance, store, bNoOptIn.ID) {
		testInstance.Fatal("another app's opt_in:false registration was deleted")
	}
	if rowExists(ctx, testInstance, store, aOwn.ID) {
		testInstance.Fatal("a row A registered with only A's opt-in survived")
	}
	if !rowExists(ctx, testInstance, store, bLive.ID) {
		testInstance.Fatal("a live other-registrar row was deleted")
	}
	if optedIn, _ := store.IsOptedIn(ctx, "app_a", bLive.ID); optedIn {
		testInstance.Fatal("A's opt-in on B's row survived")
	}
	if rowExists(ctx, testInstance, store, orphaned.ID) {
		testInstance.Fatal("a registrar-removed row left with no opt-ins survived")
	}
	if !rowExists(ctx, testInstance, store, aShared.ID) {
		testInstance.Fatal("a row still used by B was deleted")
	}
	if !rowExists(ctx, testInstance, store, otherPerson.ID) {
		testInstance.Fatal("another person's row was touched")
	}
}

func TestForgetPersonDeletesAllTheirRowsOnly(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, pool, _ := newTestStore(ctx, testInstance)

	first := mustInsert(ctx, testInstance, store, newDestination(personOne, "app_a", "one", true))
	second := mustInsert(ctx, testInstance, store, newDestination(personOne, "app_b", "two", false))
	keep := mustInsert(ctx, testInstance, store, newDestination(personTwo, "app_a", "keep", true))

	removed, forgetErr := store.ForgetPerson(ctx, personOne)
	if forgetErr != nil || removed != 2 {
		testInstance.Fatalf("ForgetPerson = %d, %v; want 2", removed, forgetErr)
	}
	if rowExists(ctx, testInstance, store, first.ID) || rowExists(ctx, testInstance, store, second.ID) {
		testInstance.Fatal("a row of the person survived")
	}
	if !rowExists(ctx, testInstance, store, keep.ID) {
		testInstance.Fatal("another person's row was deleted")
	}
	var optIns int
	if scanErr := pool.QueryRow(ctx,
		`SELECT count(*) FROM personal_destination_opt_ins WHERE personal_destination_id = $1`, first.ID).Scan(&optIns); scanErr != nil || optIns != 0 {
		testInstance.Fatalf("opt-ins left for a deleted row: %d, %v", optIns, scanErr)
	}
}

// TestOptInRacingForgetPersonForAppNeverErrorsOrLosesTheOptIn races an opt-in PUT for another app against
// the ForgetPersonForApp reap of a row registered by the forgetting app.
func TestOptInRacingForgetPersonForAppNeverErrorsOrLosesTheOptIn(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	for iteration := range raceIterations {
		personHash := rewriteToBase32(fmt.Sprintf("forg%012d", iteration))
		row := mustInsert(ctx, testInstance, store, newDestination(personHash, "app_a", "race", true))

		var optInErr, forgetErr error
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			optInErr = store.OptIn(ctx, "app_c", row.ID)
		}()
		go func() {
			defer group.Done()
			<-start
			_, _, forgetErr = store.ForgetPersonForApp(ctx, "app_a", personHash)
		}()
		close(start)
		group.Wait()

		if forgetErr != nil {
			testInstance.Fatalf("iteration %d: ForgetPersonForApp error: %v", iteration, forgetErr)
		}
		switch {
		case optInErr == nil:
			// The opt-in committed before the reap's re-check: the row survives with it.
			if optedIn, checkErr := store.IsOptedIn(ctx, "app_c", row.ID); checkErr != nil || !optedIn || !rowExists(ctx, testInstance, store, row.ID) {
				testInstance.Fatalf("iteration %d: opt-in or row lost (%v, %v)", iteration, optedIn, checkErr)
			}
		case errors.Is(optInErr, ErrNotFound):
			if rowExists(ctx, testInstance, store, row.ID) {
				testInstance.Fatalf("iteration %d: ErrNotFound but the row exists", iteration)
			}
		default:
			testInstance.Fatalf("iteration %d: OptIn error = %v, want nil or ErrNotFound", iteration, optInErr)
		}
	}
}

// TestSameAppOptInRacingItsOwnForgetNeverDeadlocks is the same-app case: the PUT is for the app being
// forgotten, contending on the same opt-in key.
func TestSameAppOptInRacingItsOwnForgetNeverDeadlocks(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	defer cancel()
	store, _, _ := newTestStore(ctx, testInstance)

	for iteration := range raceIterations {
		personHash := rewriteToBase32(fmt.Sprintf("fsam%012d", iteration))
		row := mustInsert(ctx, testInstance, store, newDestination(personHash, "app_a", "race", true))

		var optInErr, forgetErr error
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			optInErr = store.OptIn(ctx, "app_a", row.ID)
		}()
		go func() {
			defer group.Done()
			<-start
			_, _, forgetErr = store.ForgetPersonForApp(ctx, "app_a", personHash)
		}()
		close(start)
		group.Wait()

		if forgetErr != nil {
			testInstance.Fatalf("iteration %d: ForgetPersonForApp error: %v", iteration, forgetErr)
		}
		if optInErr != nil && !errors.Is(optInErr, ErrNotFound) {
			testInstance.Fatalf("iteration %d: OptIn error = %v, want nil or ErrNotFound (no 40P01)", iteration, optInErr)
		}
		// The PUT either lost (no row) or landed first and was then removed by the forget: nothing remains.
		if rowExists(ctx, testInstance, store, row.ID) {
			testInstance.Fatalf("iteration %d: the forgotten app's row survived", iteration)
		}
	}
}

// rewriteToBase32 maps any character outside [a-z2-7] to a valid one so a
// synthetic person hash satisfies the person_hash CHECK.
func rewriteToBase32(value string) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	mapped := []byte(value)
	for index, char := range mapped {
		mapped[index] = alphabet[int(char)%len(alphabet)]
	}
	return string(mapped)
}

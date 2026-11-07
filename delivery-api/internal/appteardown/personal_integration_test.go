//go:build integration

package appteardown

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	personalTestBudget = 60 * time.Second
	personalRaceRounds = 40
	personalPerson     = "abcdefghijklmnop"
	appAlpha           = "alphaapp"
	appBeta            = "betaapp"
	appGamma           = "gammaapp"
)

func newPersonalStores(ctx context.Context, testInstance *testing.T) (*PGStore, *personal.PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	sealer, sealerErr := seal.New(bytes.Repeat([]byte{0x24}, seal.KeySize))
	if sealerErr != nil {
		testInstance.Fatalf("seal.New: %v", sealerErr)
	}
	return NewPGStore(pool), personal.NewPGStore(pool, sealer), pool
}

func insertPersonal(
	ctx context.Context, testInstance *testing.T, store *personal.PGStore, personHash, registrar string, optIn bool,
) personal.Destination {
	testInstance.Helper()
	inserted, insertErr := store.Insert(ctx, personal.NewDestination{
		PersonHash: personHash, RegisteredByAppID: registrar, Label: "Phone", Type: personal.TypeDiscord,
		Config:  map[string]string{},
		Secrets: map[string]string{"webhook_url": "https://discord.com/api/webhooks/1/x"},
		OptIn:   optIn,
	})
	if insertErr != nil {
		testInstance.Fatalf("insert personal destination: %v", insertErr)
	}
	return inserted
}

func personalRowExists(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, id int64) bool {
	testInstance.Helper()
	return count(ctx, testInstance, pool, `SELECT count(*) FROM personal_destinations WHERE id = $1`, id) == 1
}

func mustOptIn(ctx context.Context, testInstance *testing.T, store *personal.PGStore, appID string, id int64) {
	testInstance.Helper()
	if optErr := store.OptIn(ctx, appID, id); optErr != nil {
		testInstance.Fatalf("OptIn %s: %v", appID, optErr)
	}
}

// (a) a row another app still uses survives with registrar_removed_at set and no app can manage it; a
// sole-app row is deleted; the counts are reported.
func TestTeardownKeepsSharedPersonalRowAndDeletesSoleAppRow(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), personalTestBudget)
	defer cancel()
	teardown, personalStore, pool := newPersonalStores(ctx, testInstance)

	shared := insertPersonal(ctx, testInstance, personalStore, personalPerson, appAlpha, true)
	mustOptIn(ctx, testInstance, personalStore, appBeta, shared.ID)
	sole := insertPersonal(ctx, testInstance, personalStore, personalPerson, appAlpha, true)
	unrelated := insertPersonal(ctx, testInstance, personalStore, personalPerson, appBeta, true)

	deleted, err := teardown.DeleteOwnedObjects(ctx, appAlpha)
	if err != nil {
		testInstance.Fatalf("DeleteOwnedObjects: %v", err)
	}
	if deleted.PersonalOptIns != 2 || deleted.PersonalDestinations != 1 {
		testInstance.Fatalf("deleted = %+v, want 2 opt-ins and 1 destination", deleted)
	}
	if personalRowExists(ctx, testInstance, pool, sole.ID) {
		testInstance.Fatal("sole-app row survived")
	}
	survivor, getErr := personalStore.Get(ctx, shared.ID)
	if getErr != nil || survivor.RegistrarRemovedAt == nil {
		testInstance.Fatalf("shared row = %+v, %v; want present with registrar_removed_at set", survivor, getErr)
	}
	if removed, deleteErr := personalStore.DeleteManaged(ctx, shared.ID, appAlpha, personalPerson); deleteErr != nil || removed {
		testInstance.Fatalf("a registrar-removed row was deletable by its old registrar: %v, %v", removed, deleteErr)
	}
	if optedIn, checkErr := personalStore.IsOptedIn(ctx, appBeta, shared.ID); checkErr != nil || !optedIn {
		testInstance.Fatalf("beta lost its opt-in: %v, %v", optedIn, checkErr)
	}
	if optedIn, checkErr := personalStore.IsOptedIn(ctx, appAlpha, shared.ID); checkErr != nil || optedIn {
		testInstance.Fatalf("alpha opt-in survived its teardown: %v, %v", optedIn, checkErr)
	}
	if !personalRowExists(ctx, testInstance, pool, unrelated.ID) {
		testInstance.Fatal("another app's row was touched")
	}
}

// (b) row X registered by A with B opted in: tear down A, then B: X is gone, and B's teardown counts it.
func TestTeardownSequenceReapsRowOrphanedByEarlierRegistrar(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), personalTestBudget)
	defer cancel()
	teardown, personalStore, pool := newPersonalStores(ctx, testInstance)

	row := insertPersonal(ctx, testInstance, personalStore, personalPerson, appAlpha, true)
	mustOptIn(ctx, testInstance, personalStore, appBeta, row.ID)

	if _, err := teardown.DeleteOwnedObjects(ctx, appAlpha); err != nil {
		testInstance.Fatalf("teardown alpha: %v", err)
	}
	if !personalRowExists(ctx, testInstance, pool, row.ID) {
		testInstance.Fatal("row gone after the first teardown although beta still uses it")
	}
	deleted, err := teardown.DeleteOwnedObjects(ctx, appBeta)
	if err != nil {
		testInstance.Fatalf("teardown beta: %v", err)
	}
	if personalRowExists(ctx, testInstance, pool, row.ID) {
		testInstance.Fatal("row survived the second teardown with zero opt-ins and a removed registrar")
	}
	if deleted.PersonalOptIns != 1 || deleted.PersonalDestinations != 1 {
		testInstance.Fatalf("deleted = %+v, want 1 opt-in and 1 destination", deleted)
	}
	if left := count(ctx, testInstance, pool, `SELECT count(*) FROM personal_destination_opt_ins`); left != 0 {
		testInstance.Fatalf("%d opt-ins left", left)
	}
}

// (c) tear down A, then B opts out of X: X is gone and the opt-out reports the reap.
func TestTeardownThenOptOutReapsTheRow(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), personalTestBudget)
	defer cancel()
	teardown, personalStore, pool := newPersonalStores(ctx, testInstance)

	row := insertPersonal(ctx, testInstance, personalStore, personalPerson, appAlpha, true)
	mustOptIn(ctx, testInstance, personalStore, appBeta, row.ID)
	if _, err := teardown.DeleteOwnedObjects(ctx, appAlpha); err != nil {
		testInstance.Fatalf("teardown alpha: %v", err)
	}
	reaped, optOutErr := personalStore.OptOut(ctx, appBeta, row.ID)
	if optOutErr != nil || !reaped {
		testInstance.Fatalf("OptOut = %v, %v; want reaped", reaped, optOutErr)
	}
	if personalRowExists(ctx, testInstance, pool, row.ID) {
		testInstance.Fatal("row survived the last opt-out")
	}
}

// A teardown of an app with no personal rows reports zeros and leaves other apps' rows alone.
func TestTeardownWithoutPersonalRowsReportsZeros(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), personalTestBudget)
	defer cancel()
	teardown, personalStore, pool := newPersonalStores(ctx, testInstance)

	row := insertPersonal(ctx, testInstance, personalStore, personalPerson, appBeta, true)
	deleted, err := teardown.DeleteOwnedObjects(ctx, appAlpha)
	if err != nil || deleted != (Deleted{}) {
		testInstance.Fatalf("deleted = %+v, %v; want zero", deleted, err)
	}
	if !personalRowExists(ctx, testInstance, pool, row.ID) {
		testInstance.Fatal("another app's row was removed")
	}
}

// Race (a): an opt-in PUT racing the teardown reap either lands first (the row and the opt-in survive when
// the PUT is for another app) or gets ErrNotFound; never a raw error, a deadlock or a cascaded opt-in.
func TestOptInRacingTeardownNeverErrorsOrLosesTheOptIn(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), personalTestBudget)
	defer cancel()
	teardown, personalStore, pool := newPersonalStores(ctx, testInstance)

	for round := range personalRaceRounds {
		personHash := personalRaceHash("tear", round)
		row := insertPersonal(ctx, testInstance, personalStore, personHash, appAlpha, true)

		var optInErr, teardownErr error
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			optInErr = personalStore.OptIn(ctx, appGamma, row.ID)
		}()
		go func() {
			defer group.Done()
			<-start
			_, teardownErr = teardown.DeleteOwnedObjects(ctx, appAlpha)
		}()
		close(start)
		group.Wait()

		if teardownErr != nil {
			testInstance.Fatalf("round %d: teardown error: %v", round, teardownErr)
		}
		switch {
		case optInErr == nil:
			// The PUT committed first or after the delete of the row's opt-in but before the reap: gamma's
			// opt-in must be intact on a surviving row.
			optedIn, checkErr := personalStore.IsOptedIn(ctx, appGamma, row.ID)
			if checkErr != nil || !optedIn || !personalRowExists(ctx, testInstance, pool, row.ID) {
				testInstance.Fatalf("round %d: PUT succeeded but its opt-in or row is gone (%v, %v)", round, optedIn, checkErr)
			}
		case errors.Is(optInErr, personal.ErrNotFound):
			if personalRowExists(ctx, testInstance, pool, row.ID) {
				testInstance.Fatalf("round %d: ErrNotFound but the row exists", round)
			}
		default:
			testInstance.Fatalf("round %d: OptIn error = %v, want nil or ErrNotFound", round, optInErr)
		}
	}
}

// Race (a), same app: the PUT is for the app being torn down, so it contends on the same opt-in key. Either
// order is legal (the teardown removes the app's opt-in anyway); neither may fail with an error.
func TestSameAppOptInRacingItsOwnTeardownNeverDeadlocks(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), personalTestBudget)
	defer cancel()
	teardown, personalStore, pool := newPersonalStores(ctx, testInstance)

	for round := range personalRaceRounds {
		personHash := personalRaceHash("same", round)
		row := insertPersonal(ctx, testInstance, personalStore, personHash, appAlpha, true)
		mustOptIn(ctx, testInstance, personalStore, appBeta, row.ID)

		var optInErr, teardownErr error
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			optInErr = personalStore.OptIn(ctx, appAlpha, row.ID)
		}()
		go func() {
			defer group.Done()
			<-start
			_, teardownErr = teardown.DeleteOwnedObjects(ctx, appAlpha)
		}()
		close(start)
		group.Wait()

		if teardownErr != nil {
			testInstance.Fatalf("round %d: teardown error: %v", round, teardownErr)
		}
		if optInErr != nil && !errors.Is(optInErr, personal.ErrNotFound) {
			testInstance.Fatalf("round %d: OptIn error = %v, want nil or ErrNotFound (no 40P01)", round, optInErr)
		}
		// Beta still uses the row, so it survives; alpha's opt-in is gone either way.
		if !personalRowExists(ctx, testInstance, pool, row.ID) {
			testInstance.Fatalf("round %d: the row beta still uses was deleted", round)
		}
		// Alpha's own opt-in may legitimately exist afterwards: a PUT that ran after the teardown committed
		// re-opts in on the surviving row (in production the app's key is revoked before the teardown, so no
		// such PUT can arrive). Only errors, deadlocks and a lost beta opt-in are failures here.
		if optedIn, checkErr := personalStore.IsOptedIn(ctx, appBeta, row.ID); checkErr != nil || !optedIn {
			testInstance.Fatalf("round %d: beta opt-in lost (%v, %v)", round, optedIn, checkErr)
		}
	}
}

// personalRaceHash builds a valid person hash (16 chars of [a-z2-7]) unique per prefix and round.
func personalRaceHash(prefix string, round int) string {
	digits := fmt.Sprintf("%02d", round)
	mapped := make([]byte, 0, 2)
	for _, digit := range []byte(digits) {
		mapped = append(mapped, 'a'+(digit-'0'))
	}
	return (prefix + "aaaaaaaaaaaaaaaa")[:14] + string(mapped)
}

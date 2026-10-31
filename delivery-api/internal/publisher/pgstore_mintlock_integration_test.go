//go:build integration

package publisher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

const mintLockTestBudget = 30 * time.Second

func newMintLockStore(testInstance *testing.T) (context.Context, *PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), mintLockTestBudget)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	return ctx, NewPGStore(pool), pool
}

func countMintRows(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, appID string) int {
	testInstance.Helper()
	var count int
	if scanErr := pool.QueryRow(ctx, `SELECT count(*) FROM app_publisher_mints WHERE app_id = $1`, appID).Scan(&count); scanErr != nil {
		testInstance.Fatalf("count mints: %v", scanErr)
	}
	return count
}

func samplePublisherRow(appID string) StoredPublisher {
	return StoredPublisher{AppID: appID, PublisherUserID: "app-" + appID, TopicPattern: appID + "-*", SealedToken: []byte{0x01}}
}

func TestMintLockTryLockRefusesWhileHeldAndNotOtherApps(testInstance *testing.T) {
	ctx, store, pool := newMintLockStore(testInstance)
	holder, beginErr := pool.Begin(ctx)
	if beginErr != nil {
		testInstance.Fatalf("Begin: %v", beginErr)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, lockErr := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, MintLockPrefix+"urls4irl"); lockErr != nil {
		testInstance.Fatalf("holder lock: %v", lockErr)
	}

	called := false
	busyErr := store.WithAppMintLock(ctx, "urls4irl", func(MintTx) error { called = true; return nil })
	if !errors.Is(busyErr, ErrMintBusy) || called {
		testInstance.Fatalf("held lock: err=%v fn called=%v, want ErrMintBusy without calling fn", busyErr, called)
	}
	if otherErr := store.WithAppMintLock(ctx, "otherapp", func(MintTx) error { return nil }); otherErr != nil {
		testInstance.Fatalf("a different app was refused: %v", otherErr)
	}

	if rollbackErr := holder.Rollback(ctx); rollbackErr != nil {
		testInstance.Fatalf("holder rollback: %v", rollbackErr)
	}
	if freeErr := store.WithAppMintLock(ctx, "urls4irl", func(MintTx) error { return nil }); freeErr != nil {
		testInstance.Fatalf("after release: %v", freeErr)
	}
}

func TestMintLockIsReleasedAfterFnErrorAndPanic(testInstance *testing.T) {
	ctx, store, pool := newMintLockStore(testInstance)
	fnErr := errors.New("fn failed")
	if gotErr := store.WithAppMintLock(ctx, "urls4irl", func(MintTx) error { return fnErr }); !errors.Is(gotErr, fnErr) {
		testInstance.Fatalf("err = %v, want fn's error", gotErr)
	}
	func() {
		defer func() { _ = recover() }()
		_ = store.WithAppMintLock(ctx, "urls4irl", func(MintTx) error { panic("kaboom") })
	}()

	if followErr := store.WithAppMintLock(ctx, "urls4irl", func(MintTx) error { return nil }); followErr != nil {
		testInstance.Fatalf("following lock: %v", followErr)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		testInstance.Fatalf("AcquiredConns = %d, want 0 (no leaked connection)", acquired)
	}
}

func TestMintSlotRowPersistsAfterFnErrorAndNotAfterPanic(testInstance *testing.T) {
	ctx, store, pool := newMintLockStore(testInstance)
	fnErr := errors.New("mint failed")

	_ = store.WithAppMintLock(ctx, "urls4irl", func(tx MintTx) error {
		if reserveErr := tx.ReserveMintSlot(ctx, "urls4irl", MintRateLimit, MintRateWindow); reserveErr != nil {
			return reserveErr
		}
		return fnErr
	})
	if got := countMintRows(ctx, testInstance, pool, "urls4irl"); got != 1 {
		testInstance.Fatalf("slot rows after fn error = %d, want 1", got)
	}

	func() {
		defer func() { _ = recover() }()
		_ = store.WithAppMintLock(ctx, "otherapp", func(tx MintTx) error {
			_ = tx.ReserveMintSlot(ctx, "otherapp", MintRateLimit, MintRateWindow)
			panic("kaboom")
		})
	}()
	if got := countMintRows(ctx, testInstance, pool, "otherapp"); got != 0 {
		testInstance.Fatalf("slot rows after panic = %d, want 0 (rolled back)", got)
	}
}

func TestMintSlotRowSurvivesASQLLevelUpsertFailure(testInstance *testing.T) {
	ctx, store, pool := newMintLockStore(testInstance)

	gotErr := store.WithAppMintLock(ctx, "urls4irl", func(tx MintTx) error {
		if reserveErr := tx.ReserveMintSlot(ctx, "urls4irl", MintRateLimit, MintRateWindow); reserveErr != nil {
			return reserveErr
		}
		_, upsertErr := tx.Upsert(ctx, samplePublisherRow("Bad-App")) // violates the app_id CHECK
		return upsertErr
	})

	if gotErr == nil {
		testInstance.Fatal("the failing upsert returned no error")
	}
	if got := countMintRows(ctx, testInstance, pool, "urls4irl"); got != 1 {
		testInstance.Fatalf("slot rows = %d, want 1 (the savepoint keeps the transaction alive)", got)
	}
}

func TestMintTransactionNeedsOnlyOneConnection(testInstance *testing.T) {
	ctx, _, pool := newMintLockStore(testInstance)
	config := pool.Config().Copy()
	config.MaxConns = 1
	capped, poolErr := pgxpool.NewWithConfig(ctx, config)
	if poolErr != nil {
		testInstance.Fatalf("capped pool: %v", poolErr)
	}
	testInstance.Cleanup(capped.Close)
	store := NewPGStore(capped)
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	mintErr := store.WithAppMintLock(runCtx, "urls4irl", func(tx MintTx) error {
		if reserveErr := tx.ReserveMintSlot(runCtx, "urls4irl", MintRateLimit, MintRateWindow); reserveErr != nil {
			return reserveErr
		}
		_, upsertErr := tx.Upsert(runCtx, samplePublisherRow("urls4irl"))
		return upsertErr
	})

	if mintErr != nil {
		testInstance.Fatalf("mint on a one-connection pool: %v (deadlock or failure)", mintErr)
	}
}

func TestReserveMintSlotPrunesLimitsAndRoundsRetryAfterUp(testInstance *testing.T) {
	ctx, store, pool := newMintLockStore(testInstance)
	if _, insertErr := pool.Exec(ctx,
		`INSERT INTO app_publisher_mints (app_id, minted_at) SELECT 'urls4irl', now() - interval '2 hours' FROM generate_series(1, 10)`); insertErr != nil {
		testInstance.Fatalf("seed old rows: %v", insertErr)
	}

	reserve := func() error {
		return store.WithAppMintLock(ctx, "urls4irl", func(tx MintTx) error {
			return tx.ReserveMintSlot(ctx, "urls4irl", MintRateLimit, MintRateWindow)
		})
	}
	for attempt := range MintRateLimit {
		if reserveErr := reserve(); reserveErr != nil {
			testInstance.Fatalf("attempt %d (old rows must be pruned and free the slots): %v", attempt+1, reserveErr)
		}
	}
	var rateLimited *RateLimitedError
	if limitedErr := reserve(); !errors.As(limitedErr, &rateLimited) || rateLimited.RetryAfter < time.Second {
		testInstance.Fatalf("11th attempt err = %v, want *RateLimitedError with RetryAfter >= 1s", limitedErr)
	}
	if got := countMintRows(ctx, testInstance, pool, "urls4irl"); got != MintRateLimit {
		testInstance.Fatalf("rows = %d, want %d (a refused attempt is not recorded)", got, MintRateLimit)
	}

	// The oldest in-window row ages out in 0.5s: the whole-second RetryAfter must round up to 1s.
	if _, updateErr := pool.Exec(ctx,
		`UPDATE app_publisher_mints SET minted_at = now() - interval '59 minutes 59.5 seconds'
WHERE ctid = (SELECT ctid FROM app_publisher_mints WHERE app_id = 'urls4irl' ORDER BY minted_at LIMIT 1)`); updateErr != nil {
		testInstance.Fatalf("backdate: %v", updateErr)
	}
	if limitedErr := reserve(); !errors.As(limitedErr, &rateLimited) || rateLimited.RetryAfter != time.Second {
		testInstance.Fatalf("near-expiry err = %v, want RetryAfter == 1s", limitedErr)
	}
}

func TestDeleteExpiredPublisherMintsDeletesOnlyOldRowsAndRefusesANonPositiveWindow(testInstance *testing.T) {
	ctx, store, pool := newMintLockStore(testInstance)
	if _, insertErr := pool.Exec(ctx,
		`INSERT INTO app_publisher_mints (app_id, minted_at) VALUES ('urls4irl', now() - interval '2 hours'), ('urls4irl', now() - interval '3 hours'), ('urls4irl', now())`); insertErr != nil {
		testInstance.Fatalf("seed: %v", insertErr)
	}

	for _, window := range []time.Duration{0, -time.Hour} {
		if _, refusedErr := store.DeleteExpiredPublisherMints(ctx, time.Now(), window); refusedErr == nil {
			testInstance.Fatalf("window %v was accepted", window)
		}
	}
	if got := countMintRows(ctx, testInstance, pool, "urls4irl"); got != 3 {
		testInstance.Fatalf("rows after refused sweeps = %d, want 3", got)
	}

	deleted, deleteErr := store.DeleteExpiredPublisherMints(ctx, time.Now(), MintRateWindow)
	if deleteErr != nil || deleted != 2 {
		testInstance.Fatalf("deleted=%d err=%v, want 2", deleted, deleteErr)
	}
	if got := countMintRows(ctx, testInstance, pool, "urls4irl"); got != 1 {
		testInstance.Fatalf("rows left = %d, want 1", got)
	}
}

func TestMintCommitIsDetachedFromACanceledRequestContext(testInstance *testing.T) {
	for _, fnFails := range []bool{false, true} {
		name := "fn succeeds"
		if fnFails {
			name = "fn fails"
		}
		testInstance.Run(name, func(subTest *testing.T) {
			ctx, store, pool := newMintLockStore(subTest)
			requestCtx, cancelRequest := context.WithCancel(ctx)
			defer cancelRequest()
			fnErr := errors.New("mint failed")

			gotErr := store.WithAppMintLock(requestCtx, "urls4irl", func(tx MintTx) error {
				if reserveErr := tx.ReserveMintSlot(ctx, "urls4irl", MintRateLimit, MintRateWindow); reserveErr != nil {
					return reserveErr
				}
				if _, upsertErr := tx.Upsert(ctx, samplePublisherRow("urls4irl")); upsertErr != nil {
					return upsertErr
				}
				cancelRequest()
				if fnFails {
					return fnErr
				}
				return nil
			})

			if fnFails != errors.Is(gotErr, fnErr) || (!fnFails && gotErr != nil) {
				subTest.Fatalf("err = %v (fnFails=%v)", gotErr, fnFails)
			}
			if got := countMintRows(ctx, subTest, pool, "urls4irl"); got != 1 {
				subTest.Fatalf("slot rows = %d, want 1", got)
			}
			var publishers int
			if scanErr := pool.QueryRow(ctx, `SELECT count(*) FROM app_publishers WHERE app_id = 'urls4irl'`).Scan(&publishers); scanErr != nil {
				subTest.Fatalf("count publishers: %v", scanErr)
			}
			if publishers != 1 {
				subTest.Fatalf("app_publishers rows = %d, want 1 (the commit survived the canceled request)", publishers)
			}
		})
	}
}

func TestMintBeginTimeoutReturnsUnavailableWhenThePoolIsExhausted(testInstance *testing.T) {
	ctx, _, pool := newMintLockStore(testInstance)
	config := pool.Config().Copy()
	config.MaxConns = 1
	capped, poolErr := pgxpool.NewWithConfig(ctx, config)
	if poolErr != nil {
		testInstance.Fatalf("capped pool: %v", poolErr)
	}
	testInstance.Cleanup(capped.Close)
	held, acquireErr := capped.Acquire(ctx)
	if acquireErr != nil {
		testInstance.Fatalf("Acquire: %v", acquireErr)
	}
	defer held.Release()
	store := NewPGStore(capped)
	store.beginTimeout = 20 * time.Millisecond

	called := false
	started := time.Now()
	mintErr := store.WithAppMintLock(ctx, "urls4irl", func(MintTx) error { called = true; return nil })

	if !errors.Is(mintErr, ErrMintUnavailable) || called || time.Since(started) > 5*time.Second {
		testInstance.Fatalf("err=%v called=%v after %v, want a prompt ErrMintUnavailable without fn", mintErr, called, time.Since(started))
	}
}

func TestPGStoreMintTimeoutDefaultsArePinned(testInstance *testing.T) {
	store := NewPGStore(nil)
	// The literals pin the documented values; TestMintTuningConstantsArePinned ties the constants to them.
	if store.beginTimeout != 3*time.Second || store.commitTimeout != 5*time.Second {
		testInstance.Fatalf("defaults: begin=%v commit=%v, want 3s / 5s", store.beginTimeout, store.commitTimeout)
	}
}

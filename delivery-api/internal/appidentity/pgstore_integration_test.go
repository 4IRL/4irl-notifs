//go:build integration

package appidentity

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

const (
	storeTestBudget = 60 * time.Second
	// lockObserveWindow is how long the lock test waits to prove a second exchange is blocked.
	lockObserveWindow = 300 * time.Millisecond
)

var errRollbackOnPurpose = errors.New("roll back on purpose")

func newTestStore(testInstance *testing.T) (context.Context, *PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), storeTestBudget)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	return ctx, NewPGStore(pool), pool
}

func testClaims(repositoryID int64, environment string) Claims {
	return Claims{
		Repository: "4IRL/urls4irl", RepositoryID: repositoryID, RepositoryOwnerID: 678,
		WorkflowPath: ".github/workflows/prod-deploy.yml", Ref: "refs/heads/main",
		Environment: environment, RunID: "run-1", JTI: "jti", ExpiresAt: testNow,
	}
}

func insertTestBinding(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, appID string, claims Claims) {
	testInstance.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO app_bindings (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())`,
		appID, claims.Repository, claims.RepositoryID, claims.RepositoryOwnerID, claims.WorkflowPath, claims.Ref,
		environmentArgument(claims.Environment)); err != nil {
		testInstance.Fatalf("insert binding: %v", err)
	}
}

func countRows(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, query string, arguments ...any) int {
	testInstance.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, arguments...).Scan(&count); err != nil {
		testInstance.Fatalf("count %q: %v", query, err)
	}
	return count
}

func TestRecordJTIIsOnceOnlyAndRollsBackWithItsTransaction(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	// Recorded inside a transaction that fails: nothing persists, so the jti is still usable.
	err := store.Exchange(ctx, func(tx ExchangeTx) error {
		fresh, recordErr := tx.RecordJTI(ctx, "jti-rolled-back", testNow)
		if recordErr != nil || !fresh {
			testInstance.Errorf("first record = %v, %v; want true, nil", fresh, recordErr)
		}
		again, againErr := tx.RecordJTI(ctx, "jti-rolled-back", testNow)
		if againErr != nil || again {
			testInstance.Errorf("second record in the same transaction = %v, %v; want false, nil", again, againErr)
		}
		return errRollbackOnPurpose
	})
	if !errors.Is(err, errRollbackOnPurpose) {
		testInstance.Fatalf("Exchange err = %v, want the callback's error unchanged", err)
	}
	var freshAfterRollback bool
	if exchangeErr := store.Exchange(ctx, func(tx ExchangeTx) error {
		var recordErr error
		freshAfterRollback, recordErr = tx.RecordJTI(ctx, "jti-rolled-back", testNow)
		return recordErr
	}); exchangeErr != nil {
		testInstance.Fatalf("Exchange: %v", exchangeErr)
	}
	if !freshAfterRollback {
		testInstance.Error("a jti recorded in a rolled-back transaction must be usable again")
	}

	// Committed above: now a replay.
	var replayed bool
	if exchangeErr := store.Exchange(ctx, func(tx ExchangeTx) error {
		fresh, recordErr := tx.RecordJTI(ctx, "jti-rolled-back", testNow)
		replayed = !fresh
		return recordErr
	}); exchangeErr != nil {
		testInstance.Fatalf("Exchange: %v", exchangeErr)
	}
	if !replayed {
		testInstance.Error("a committed jti must read as a replay")
	}
}

func TestConcurrentExchangesOfOneJTIHaveExactlyOneWinner(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	var winners atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			err := store.Exchange(ctx, func(tx ExchangeTx) error {
				fresh, recordErr := tx.RecordJTI(ctx, "contended", testNow)
				if fresh {
					winners.Add(1)
				}
				return recordErr
			})
			if err != nil {
				testInstance.Errorf("Exchange: %v", err)
			}
		}()
	}
	group.Wait()

	if got := winners.Load(); got != 1 {
		testInstance.Fatalf("winners = %d, want exactly 1", got)
	}
}

func TestLockBinding(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "production")
	insertTestBinding(ctx, testInstance, pool, "urls4irl", claims)
	insertTestBinding(ctx, testInstance, pool, "chores4irl", testClaims(2, ""))

	testInstance.Run("an unbound app is not found", func(subTest *testing.T) {
		_ = store.Exchange(ctx, func(tx ExchangeTx) error {
			_, found, err := tx.LockBinding(ctx, "nobody")
			if err != nil || found {
				subTest.Errorf("LockBinding = found %v, err %v; want not found", found, err)
			}
			return nil
		})
	})

	testInstance.Run("a bound app returns its binding, with no environment as empty", func(subTest *testing.T) {
		_ = store.Exchange(ctx, func(tx ExchangeTx) error {
			binding, found, err := tx.LockBinding(ctx, "urls4irl")
			if err != nil || !found {
				subTest.Fatalf("LockBinding = found %v, err %v", found, err)
			}
			if !binding.Matches(claims) || binding.AppID != "urls4irl" || binding.Repository != "4IRL/urls4irl" || binding.ApprovedAt.IsZero() {
				subTest.Errorf("binding = %+v", binding)
			}
			unscoped, _, _ := tx.LockBinding(ctx, "chores4irl")
			if unscoped.Environment != "" {
				subTest.Errorf("environment = %q, want empty for a NULL column", unscoped.Environment)
			}
			return nil
		})
	})

	testInstance.Run("a second exchange for the same app waits for the first", func(subTest *testing.T) {
		holding := make(chan struct{})
		release := make(chan struct{})
		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			_ = store.Exchange(ctx, func(tx ExchangeTx) error {
				if _, _, err := tx.LockBinding(ctx, "urls4irl"); err != nil {
					subTest.Errorf("first LockBinding: %v", err)
				}
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding

		secondReturned := make(chan struct{})
		go func() {
			defer close(secondReturned)
			_ = store.Exchange(ctx, func(tx ExchangeTx) error {
				_, _, err := tx.LockBinding(ctx, "urls4irl")
				return err
			})
		}()
		select {
		case <-secondReturned:
			subTest.Error("the second exchange got the binding while the first still held it")
		case <-time.After(lockObserveWindow):
		}
		close(release)
		select {
		case <-secondReturned:
		case <-time.After(testWaitBudget):
			subTest.Error("the second exchange never got the binding after the first finished")
		}
		<-firstDone
	})

	testInstance.Run("a different app is not blocked", func(subTest *testing.T) {
		holding := make(chan struct{})
		release := make(chan struct{})
		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			_ = store.Exchange(ctx, func(tx ExchangeTx) error {
				_, _, _ = tx.LockBinding(ctx, "urls4irl")
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding
		otherReturned := make(chan struct{})
		go func() {
			defer close(otherReturned)
			_ = store.Exchange(ctx, func(tx ExchangeTx) error {
				_, _, err := tx.LockBinding(ctx, "chores4irl")
				return err
			})
		}()
		select {
		case <-otherReturned:
		case <-time.After(testWaitBudget):
			subTest.Error("locking another app's binding must not wait")
		}
		close(release)
		<-firstDone
	})
}

func TestFilePendingRequest(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	file := func(appID string, claims Claims, now time.Time) (int64, error) {
		var requestID int64
		err := store.Exchange(ctx, func(tx ExchangeTx) error {
			var fileErr error
			requestID, fileErr = tx.FilePendingRequest(ctx, appID, claims, now)
			return fileErr
		})
		return requestID, err
	}

	testInstance.Run("the same app and claims refresh one row, with or without an environment", func(subTest *testing.T) {
		for _, environment := range []string{"", "production"} {
			claims := testClaims(100, environment)
			first, err := file("refresh_app", claims, testNow)
			if err != nil {
				subTest.Fatalf("first file: %v", err)
			}
			claims.RunID = "run-2"
			second, err := file("refresh_app", claims, testNow.Add(time.Minute))
			if err != nil {
				subTest.Fatalf("second file: %v", err)
			}
			if first != second {
				subTest.Errorf("environment %q: ids %d and %d, want the same pending row", environment, first, second)
			}

			var runID string
			var updatedAt, createdAt time.Time
			if scanErr := pool.QueryRow(ctx,
				`SELECT github_run_id, updated_at, created_at FROM app_registration_requests WHERE id = $1`, first,
			).Scan(&runID, &updatedAt, &createdAt); scanErr != nil {
				subTest.Fatalf("read request: %v", scanErr)
			}
			if runID != "run-2" || !updatedAt.Equal(testNow.Add(time.Minute)) || !createdAt.Equal(testNow) {
				subTest.Errorf("run %q updated %v created %v; want run-2, refreshed updated_at, original created_at", runID, updatedAt, createdAt)
			}
		}
		if rows := countRows(ctx, subTest, pool, `SELECT count(*) FROM app_registration_requests WHERE app_id = 'refresh_app'`); rows != 2 {
			subTest.Errorf("rows = %d, want 2 (one per distinct claim set)", rows)
		}
	})

	testInstance.Run("different claims or a different app are their own request", func(subTest *testing.T) {
		base, err := file("distinct_app", testClaims(200, ""), testNow)
		if err != nil {
			subTest.Fatalf("file: %v", err)
		}
		otherRepo, _ := file("distinct_app", testClaims(201, ""), testNow)
		otherApp, _ := file("distinct_other", testClaims(200, ""), testNow)
		otherEnv, _ := file("distinct_app", testClaims(200, "staging"), testNow)
		ids := map[int64]bool{base: true, otherRepo: true, otherApp: true, otherEnv: true}
		if len(ids) != 4 {
			subTest.Errorf("ids = %v, want 4 distinct requests", ids)
		}
	})

	testInstance.Run("a rejected request does not block a new pending one for the same claims", func(subTest *testing.T) {
		claims := testClaims(300, "")
		first, err := file("rejected_app", claims, testNow)
		if err != nil {
			subTest.Fatalf("file: %v", err)
		}
		if _, execErr := pool.Exec(ctx,
			`UPDATE app_registration_requests SET status = 'rejected', decided_at = now() WHERE id = $1`, first); execErr != nil {
			subTest.Fatalf("reject: %v", execErr)
		}
		second, err := file("rejected_app", claims, testNow)
		if err != nil {
			subTest.Fatalf("file again: %v", err)
		}
		if second == first {
			subTest.Error("the rejected row must not be refreshed; a new pending row is expected")
		}
		var status string
		if scanErr := pool.QueryRow(ctx, `SELECT status FROM app_registration_requests WHERE id = $1`, first).Scan(&status); scanErr != nil || status != "rejected" {
			subTest.Errorf("the rejected row changed: status %q (%v)", status, scanErr)
		}
	})
}

func TestFilePendingRequestEnforcesTheCapExactly(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	fileOne := func(index int) error {
		return store.Exchange(ctx, func(tx ExchangeTx) error {
			_, err := tx.FilePendingRequest(ctx, "capped_"+strconv.Itoa(index), testClaims(int64(1000+index), ""), testNow)
			return err
		})
	}

	// Concurrent first requests must not overshoot the cap. The test database allows 20
	// connections in all, so at most cappedWorkers of them run at once: still plenty to race
	// the count-then-insert, without exhausting the server's connection slots.
	const cappedWorkers = 6
	slots := make(chan struct{}, cappedWorkers)
	var succeeded, refused atomic.Int32
	// Which 20 of the racing requests win the cap is up to the scheduler, so the test records the
	// winners instead of assuming any particular one filed.
	var winnersMutex sync.Mutex
	var winnerIndexes []int
	var group sync.WaitGroup
	for index := range MaxPendingRequests + 10 {
		group.Add(1)
		go func() {
			defer group.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			switch err := fileOne(index); {
			case err == nil:
				succeeded.Add(1)
				winnersMutex.Lock()
				winnerIndexes = append(winnerIndexes, index)
				winnersMutex.Unlock()
			case errors.Is(err, ErrPendingCap):
				refused.Add(1)
			default:
				testInstance.Errorf("request %d: %v", index, err)
			}
		}()
	}
	group.Wait()
	if succeeded.Load() != MaxPendingRequests || refused.Load() != 10 {
		testInstance.Fatalf("succeeded %d, refused %d; want %d and 10", succeeded.Load(), refused.Load(), MaxPendingRequests)
	}
	if rows := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registration_requests WHERE status = 'pending'`); rows != MaxPendingRequests {
		testInstance.Fatalf("pending rows = %d, want exactly %d", rows, MaxPendingRequests)
	}

	// At the cap, re-filing an existing request is a refresh, not a refusal.
	winner := winnerIndexes[0]
	winnerApp := "capped_" + strconv.Itoa(winner)
	var refreshedID int64
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		var fileErr error
		refreshedID, fileErr = tx.FilePendingRequest(ctx, winnerApp, testClaims(int64(1000+winner), ""), testNow.Add(time.Minute))
		return fileErr
	}); err != nil || refreshedID == 0 {
		testInstance.Errorf("refreshing an existing request at the cap = %d, %v; want it to succeed", refreshedID, err)
	}

	// A new one is refused, and rejecting a pending request frees a slot.
	if err := fileOne(500); !errors.Is(err, ErrPendingCap) {
		testInstance.Fatalf("a new request at the cap err = %v, want ErrPendingCap", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE app_registration_requests SET status = 'rejected', decided_at = now() WHERE app_id = $1`, winnerApp); err != nil {
		testInstance.Fatalf("reject one: %v", err)
	}
	if err := fileOne(500); err != nil {
		testInstance.Errorf("a new request after a rejection freed a slot: %v", err)
	}
}

func TestIssueKey(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "production")
	insertTestBinding(ctx, testInstance, pool, "urls4irl", claims)
	insertTestBinding(ctx, testInstance, pool, "chores4irl", testClaims(2, ""))

	issue := func(appID string, runID string, now time.Time) IssuedKey {
		testInstance.Helper()
		var issued IssuedKey
		if err := store.Exchange(ctx, func(tx ExchangeTx) error {
			var issueErr error
			issued, issueErr = tx.IssueKey(ctx, appID, runID, now)
			return issueErr
		}); err != nil {
			testInstance.Fatalf("IssueKey: %v", err)
		}
		return issued
	}
	supersededAt := func(keyID int64) *time.Time {
		testInstance.Helper()
		var moment *time.Time
		if err := pool.QueryRow(ctx, `SELECT superseded_at FROM app_keys WHERE id = $1`, keyID).Scan(&moment); err != nil {
			testInstance.Fatalf("read superseded_at: %v", err)
		}
		return moment
	}

	first := issue("urls4irl", "run-1", testNow)
	if !WellFormedKey(first.Plaintext) || first.Hint != first.Plaintext[:8] || first.ID == 0 {
		testInstance.Fatalf("first key = %+v", first)
	}

	// Only the hash and hint are stored, never the plaintext.
	var storedHash []byte
	var storedHint, storedRun string
	if err := pool.QueryRow(ctx, `SELECT key_hash, key_hint, github_run_id FROM app_keys WHERE id = $1`, first.ID).
		Scan(&storedHash, &storedHint, &storedRun); err != nil {
		testInstance.Fatalf("read key: %v", err)
	}
	if string(storedHash) != string(HashKey(first.Plaintext)) || storedHint != first.Hint || storedRun != "run-1" {
		testInstance.Errorf("stored hash/hint/run = %x/%q/%q", storedHash, storedHint, storedRun)
	}
	if len(storedHint) != len(KeyPrefix)+keyHintRandomChars {
		testInstance.Errorf("stored hint %q is not just the prefix and %d characters", storedHint, keyHintRandomChars)
	}
	if supersededAt(first.ID) != nil {
		testInstance.Error("the newest key must not be superseded")
	}

	// A second issue supersedes the first at that moment and leaves itself active.
	second := issue("urls4irl", "run-2", testNow.Add(time.Hour))
	if second.ID == first.ID || second.Plaintext == first.Plaintext {
		testInstance.Fatalf("second key = %+v, want a distinct key", second)
	}
	if moment := supersededAt(first.ID); moment == nil || !moment.Equal(testNow.Add(time.Hour)) {
		testInstance.Errorf("first key superseded_at = %v, want %v", moment, testNow.Add(time.Hour))
	}
	if supersededAt(second.ID) != nil {
		testInstance.Error("the second key must be active")
	}

	// A third issue supersedes only the second; the first keeps its original supersede time.
	third := issue("urls4irl", "run-3", testNow.Add(2*time.Hour))
	if moment := supersededAt(first.ID); moment == nil || !moment.Equal(testNow.Add(time.Hour)) {
		testInstance.Errorf("first key superseded_at moved to %v", moment)
	}
	if moment := supersededAt(second.ID); moment == nil || !moment.Equal(testNow.Add(2*time.Hour)) {
		testInstance.Errorf("second key superseded_at = %v, want %v", moment, testNow.Add(2*time.Hour))
	}

	// Another app's keys are untouched.
	otherKey := issue("chores4irl", "run-9", testNow)
	issue("urls4irl", "run-4", testNow.Add(3*time.Hour))
	if supersededAt(otherKey.ID) != nil {
		testInstance.Error("issuing for one app superseded another app's key")
	}
	if supersededAt(third.ID) == nil {
		testInstance.Error("the third key should have been superseded by the fourth")
	}

	// A revoked key is left alone: it is not marked superseded.
	if _, err := pool.Exec(ctx,
		`UPDATE app_keys SET revoked_at = now(), revoked_reason = 'operator' WHERE id = $1`, otherKey.ID); err != nil {
		testInstance.Fatalf("revoke: %v", err)
	}
	issue("chores4irl", "run-10", testNow.Add(time.Hour))
	if supersededAt(otherKey.ID) != nil {
		testInstance.Error("a revoked key must not be marked superseded")
	}
}

func TestIssueKeyIsAtomicWithItsTransaction(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))

	var first IssuedKey
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		var issueErr error
		first, issueErr = tx.IssueKey(ctx, "urls4irl", "run-1", testNow)
		return issueErr
	}); err != nil {
		testInstance.Fatalf("first issue: %v", err)
	}

	// Supersede-then-insert, then a failure: both must roll back together.
	err := store.Exchange(ctx, func(tx ExchangeTx) error {
		if _, issueErr := tx.IssueKey(ctx, "urls4irl", "run-2", testNow.Add(time.Hour)); issueErr != nil {
			return issueErr
		}
		return errRollbackOnPurpose
	})
	if !errors.Is(err, errRollbackOnPurpose) {
		testInstance.Fatalf("Exchange err = %v", err)
	}

	if keys := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_keys`); keys != 1 {
		testInstance.Errorf("keys = %d, want 1: the failed exchange must not leave a second key", keys)
	}
	if superseded := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_keys WHERE id = $1 AND superseded_at IS NOT NULL`, first.ID); superseded != 0 {
		testInstance.Error("the failed exchange left the first key superseded")
	}
}

func TestIssueKeyForAnUnboundAppFails(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	err := store.Exchange(ctx, func(tx ExchangeTx) error {
		_, issueErr := tx.IssueKey(ctx, "nobody", "run-1", testNow)
		return issueErr
	})

	if err == nil {
		testInstance.Fatal("a key was issued to an app with no binding")
	}
	if keys := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_keys`); keys != 0 {
		testInstance.Errorf("keys = %d, want 0", keys)
	}
}

func TestResolveKeyFollowsTheValidityRule(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))

	issue := func(now time.Time) IssuedKey {
		testInstance.Helper()
		var issued IssuedKey
		if err := store.Exchange(ctx, func(tx ExchangeTx) error {
			var issueErr error
			issued, issueErr = tx.IssueKey(ctx, "urls4irl", "run", now)
			return issueErr
		}); err != nil {
			testInstance.Fatalf("IssueKey: %v", err)
		}
		return issued
	}
	first := issue(testNow)

	testInstance.Run("an active key resolves to its app", func(subTest *testing.T) {
		resolved, err := store.ResolveKey(ctx, first.Plaintext, testNow.Add(48*time.Hour))
		if err != nil {
			subTest.Fatalf("ResolveKey: %v", err)
		}
		if resolved.ID != first.ID || resolved.AppID != "urls4irl" || resolved.Hint != first.Hint {
			subTest.Errorf("resolved = %+v, want key %d of urls4irl", resolved, first.ID)
		}
	})

	second := issue(testNow.Add(time.Hour)) // supersedes first at testNow+1h

	testInstance.Run("a superseded key works inside 24h", func(subTest *testing.T) {
		if _, err := store.ResolveKey(ctx, first.Plaintext, testNow.Add(time.Hour+23*time.Hour+59*time.Minute)); err != nil {
			subTest.Errorf("superseded 23h59m: %v", err)
		}
	})
	testInstance.Run("a superseded key stops past 24h", func(subTest *testing.T) {
		for _, offset := range []time.Duration{24 * time.Hour, 24*time.Hour + time.Minute} {
			_, err := store.ResolveKey(ctx, first.Plaintext, testNow.Add(time.Hour+offset))
			if !errors.Is(err, ErrInvalidKey) {
				subTest.Errorf("superseded %v ago: err = %v, want ErrInvalidKey", offset, err)
			}
		}
	})
	testInstance.Run("the newer key keeps working after the older one lapses", func(subTest *testing.T) {
		if _, err := store.ResolveKey(ctx, second.Plaintext, testNow.Add(72*time.Hour)); err != nil {
			subTest.Errorf("active second key: %v", err)
		}
	})

	testInstance.Run("a revoked key is invalid at once", func(subTest *testing.T) {
		if _, err := pool.Exec(ctx,
			`UPDATE app_keys SET revoked_at = now(), revoked_reason = 'operator' WHERE id = $1`, second.ID); err != nil {
			subTest.Fatalf("revoke: %v", err)
		}
		_, err := store.ResolveKey(ctx, second.Plaintext, testNow.Add(2*time.Hour))
		if !errors.Is(err, ErrInvalidKey) {
			subTest.Fatalf("err = %v, want ErrInvalidKey", err)
		}
		if strings.Contains(err.Error(), second.Plaintext) {
			subTest.Error("the error echoes the plaintext key")
		}
	})

	testInstance.Run("an unknown well-formed key and a malformed one are invalid", func(subTest *testing.T) {
		unknown, newErr := NewKey()
		if newErr != nil {
			subTest.Fatalf("NewKey: %v", newErr)
		}
		for label, key := range map[string]string{"unknown": unknown, "malformed": "dak_short", "empty": ""} {
			_, err := store.ResolveKey(ctx, key, testNow)
			if !errors.Is(err, ErrInvalidKey) {
				subTest.Errorf("%s: err = %v, want ErrInvalidKey", label, err)
			}
		}
	})

	testInstance.Run("unbinding the app invalidates its keys", func(subTest *testing.T) {
		third := issue(testNow.Add(3 * time.Hour))
		if _, err := pool.Exec(ctx, `DELETE FROM app_bindings WHERE app_id = 'urls4irl'`); err != nil {
			subTest.Fatalf("unbind: %v", err)
		}
		if _, err := store.ResolveKey(ctx, third.Plaintext, testNow.Add(4*time.Hour)); !errors.Is(err, ErrInvalidKey) {
			subTest.Errorf("err = %v, want ErrInvalidKey after unbind", err)
		}
	})
}

func TestResolveKeyReturnsDatabaseFailuresUnwrapped(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	key, newErr := NewKey()
	if newErr != nil {
		testInstance.Fatalf("NewKey: %v", newErr)
	}
	pool.Close()

	_, err := store.ResolveKey(ctx, key, testNow)

	if err == nil {
		testInstance.Fatal("ResolveKey with a closed pool succeeded")
	}
	if errors.Is(err, ErrInvalidKey) {
		testInstance.Errorf("a database failure must not read as an invalid key (it is a 503, not a 401): %v", err)
	}
}

func TestTouchKeyWritesAtMostOncePerMinute(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))
	var issued IssuedKey
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		var issueErr error
		issued, issueErr = tx.IssueKey(ctx, "urls4irl", "run", testNow)
		return issueErr
	}); err != nil {
		testInstance.Fatalf("IssueKey: %v", err)
	}

	steps := []struct {
		label     string
		at        time.Time
		wantWrite bool
	}{
		{"the first use writes", testNow, true},
		{"the same instant does not", testNow, false},
		{"59 seconds later does not", testNow.Add(59 * time.Second), false},
		{"a full minute later writes", testNow.Add(time.Minute), true},
		{"a second after that does not", testNow.Add(time.Minute + time.Second), false},
		{"another minute later writes", testNow.Add(2*time.Minute + time.Second), true},
	}
	for _, step := range steps {
		wrote, err := store.TouchKey(ctx, issued.ID, step.at)
		if err != nil || wrote != step.wantWrite {
			testInstance.Errorf("%s: wrote = %v, err = %v; want wrote %v", step.label, wrote, err, step.wantWrite)
		}
	}

	var lastUsed time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM app_keys WHERE id = $1`, issued.ID).Scan(&lastUsed); err != nil {
		testInstance.Fatalf("read last_used_at: %v", err)
	}
	if want := testNow.Add(2*time.Minute + time.Second); !lastUsed.Equal(want) {
		testInstance.Errorf("last_used_at = %v, want %v", lastUsed, want)
	}
	if wrote, err := store.TouchKey(ctx, issued.ID+1000, testNow); err != nil || wrote {
		testInstance.Errorf("touching an absent key = %v, %v; want false, nil", wrote, err)
	}
}

func TestDeleteExpiredReplayDeletesOnlyExpiredRows(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	for jti, expiresAt := range map[string]time.Time{
		"long-gone":   testNow.Add(-time.Hour),
		"just-gone":   testNow.Add(-time.Second),
		"exactly-now": testNow,
		"still-valid": testNow.Add(time.Second),
		"much-later":  testNow.Add(time.Hour),
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO oidc_replay (jti, expires_at) VALUES ($1, $2)`, jti, expiresAt); err != nil {
			testInstance.Fatalf("insert %s: %v", jti, err)
		}
	}

	deleted, err := store.DeleteExpiredReplay(ctx, testNow)
	if err != nil || deleted != 2 {
		testInstance.Fatalf("deleted = %d, err = %v; want 2 (strictly before now)", deleted, err)
	}
	if remaining := countRows(ctx, testInstance, pool, `SELECT count(*) FROM oidc_replay`); remaining != 3 {
		testInstance.Errorf("remaining = %d, want 3", remaining)
	}
	if again, againErr := store.DeleteExpiredReplay(ctx, testNow); againErr != nil || again != 0 {
		testInstance.Errorf("second sweep deleted %d (%v), want 0", again, againErr)
	}
}

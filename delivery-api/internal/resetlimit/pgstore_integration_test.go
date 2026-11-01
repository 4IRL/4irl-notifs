//go:build integration

package resetlimit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
)

const resetTestBudget = 30 * time.Second

func newResetStore(testInstance *testing.T) (context.Context, *PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), resetTestBudget)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	return ctx, NewPGStore(pool), pool
}

func countRows(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, appID, personHash string) int {
	testInstance.Helper()
	var count int
	query := `SELECT count(*) FROM app_reset_attempts WHERE app_id = $1 AND ($2 = '' OR person_hash = $2)`
	if scanErr := pool.QueryRow(ctx, query, appID, personHash).Scan(&count); scanErr != nil {
		testInstance.Fatalf("count rows: %v", scanErr)
	}
	return count
}

// seed inserts rows attempted ageSeconds ago.
func seed(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, appID, personHash string, rows int, ageSeconds float64) {
	testInstance.Helper()
	if _, insertErr := pool.Exec(ctx,
		`INSERT INTO app_reset_attempts (app_id, person_hash, attempted_at)
SELECT $1, $2, clock_timestamp() - $3::float8 * interval '1 second' FROM generate_series(1, $4)`,
		appID, personHash, ageSeconds, rows); insertErr != nil {
		testInstance.Fatalf("seed: %v", insertErr)
	}
}

func asRateLimited(testInstance *testing.T, err error) *RateLimitedError {
	testInstance.Helper()
	var rateLimited *RateLimitedError
	if !errors.As(err, &rateLimited) {
		testInstance.Fatalf("err = %v, want *RateLimitedError", err)
	}
	return rateLimited
}

func TestReservePersonCapAdmitsThreeAndRefusesTheFourthWithoutRecording(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	for attempt := 1; attempt <= PersonLimit; attempt++ {
		if reserveErr := store.Reserve(ctx, "urls4irl", "person-a"); reserveErr != nil {
			testInstance.Fatalf("attempt %d: %v", attempt, reserveErr)
		}
		if got := countRows(ctx, testInstance, pool, "urls4irl", "person-a"); got != attempt {
			testInstance.Fatalf("rows after attempt %d = %d", attempt, got)
		}
	}
	rateLimited := asRateLimited(testInstance, store.Reserve(ctx, "urls4irl", "person-a"))
	if rateLimited.Scope != ScopePerson || rateLimited.RetryAfter < time.Second {
		testInstance.Fatalf("refusal = %+v, want person scope with RetryAfter >= 1s", rateLimited)
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", "person-a"); got != PersonLimit {
		testInstance.Fatalf("rows = %d, want %d (a refusal records nothing)", got, PersonLimit)
	}
}

func TestReserveAppCapAdmits30AndRefusesThe31stWithoutRecording(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	for person := range AppLimit {
		if reserveErr := store.Reserve(ctx, "urls4irl", fmt.Sprintf("person-%d", person)); reserveErr != nil {
			testInstance.Fatalf("person %d: %v", person, reserveErr)
		}
	}
	rateLimited := asRateLimited(testInstance, store.Reserve(ctx, "urls4irl", "person-new"))
	if rateLimited.Scope != ScopeApp {
		testInstance.Fatalf("scope = %q, want app", rateLimited.Scope)
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", ""); got != AppLimit {
		testInstance.Fatalf("rows = %d, want %d", got, AppLimit)
	}
}

func TestReserveCapsAreIsolatedPerAppAndPerPerson(testInstance *testing.T) {
	ctx, store, _ := newResetStore(testInstance)
	for range PersonLimit {
		if reserveErr := store.Reserve(ctx, "urls4irl", "person-a"); reserveErr != nil {
			testInstance.Fatalf("fill person: %v", reserveErr)
		}
	}
	if otherAppErr := store.Reserve(ctx, "otherapp", "person-a"); otherAppErr != nil {
		testInstance.Fatalf("a different app was refused: %v", otherAppErr)
	}
	if otherPersonErr := store.Reserve(ctx, "urls4irl", "person-b"); otherPersonErr != nil {
		testInstance.Fatalf("a different person was refused: %v", otherPersonErr)
	}
}

func TestReserveBothCapsReachedReportsAppScopeAndTheLargerWait(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	// Person rows (3) are the app's oldest-but-one; app oldest row is older, so the app wait is shorter.
	seed(ctx, testInstance, pool, "urls4irl", "person-a", PersonLimit, 600)        // expire in ~3000s
	seed(ctx, testInstance, pool, "urls4irl", "other", AppLimit-PersonLimit, 3000) // app oldest expires in ~600s

	rateLimited := asRateLimited(testInstance, store.Reserve(ctx, "urls4irl", "person-a"))
	if rateLimited.Scope != ScopeApp {
		testInstance.Fatalf("scope = %q, want app when both are reached", rateLimited.Scope)
	}
	// The app's oldest row is the 3000s-old one (~600s wait); the person's oldest is 600s old (~3000s).
	if rateLimited.RetryAfter < 2990*time.Second || rateLimited.RetryAfter > 3010*time.Second {
		testInstance.Fatalf("RetryAfter = %v, want the larger (person) wait of ~3000s", rateLimited.RetryAfter)
	}
}

func TestReserveRetryAfterFollowsThePersonOldestRowWhenOnlyThePersonCapIsReached(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	seed(ctx, testInstance, pool, "urls4irl", "person-a", PersonLimit, 300) // person wait ~3300s
	seed(ctx, testInstance, pool, "urls4irl", "other", 5, 3000)             // app oldest wait ~600s, not full

	rateLimited := asRateLimited(testInstance, store.Reserve(ctx, "urls4irl", "person-a"))
	if rateLimited.Scope != ScopePerson {
		testInstance.Fatalf("scope = %q, want person", rateLimited.Scope)
	}
	if rateLimited.RetryAfter < 3290*time.Second || rateLimited.RetryAfter > 3310*time.Second {
		testInstance.Fatalf("RetryAfter = %v, want ~3300s (the person's oldest row, not the app's ~600s)", rateLimited.RetryAfter)
	}
}

// The person's rows are a subset of the app's, so the app's oldest row never expires later than the
// person's. The app wait is therefore the answer whenever only the app cap is reached, and it must
// not be replaced by the person's (here much longer) wait.
func TestReserveRetryAfterFollowsTheAppOldestRowWhenOnlyTheAppCapIsReached(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	seed(ctx, testInstance, pool, "urls4irl", "other", AppLimit-1, 3000) // app oldest wait ~600s
	seed(ctx, testInstance, pool, "urls4irl", "person-a", 1, 10)         // person wait would be ~3590s

	rateLimited := asRateLimited(testInstance, store.Reserve(ctx, "urls4irl", "person-a"))
	if rateLimited.Scope != ScopeApp || rateLimited.RetryAfter < 590*time.Second || rateLimited.RetryAfter > 610*time.Second {
		testInstance.Fatalf("refusal = %+v, want app scope with ~600s", rateLimited)
	}
}

func TestReservePrunesOldRowsAndRoundsRetryAfterUpToOneSecond(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	seed(ctx, testInstance, pool, "urls4irl", "person-a", 10, 2*3600) // all outside the window
	for attempt := 1; attempt <= PersonLimit; attempt++ {
		if reserveErr := store.Reserve(ctx, "urls4irl", "person-a"); reserveErr != nil {
			testInstance.Fatalf("attempt %d (old rows must stop counting): %v", attempt, reserveErr)
		}
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", ""); got != PersonLimit {
		testInstance.Fatalf("rows = %d, want %d (old rows pruned)", got, PersonLimit)
	}
	// The oldest in-window row ages out in 0.5s: RetryAfter must round up to exactly 1s.
	if _, updateErr := pool.Exec(ctx,
		`UPDATE app_reset_attempts SET attempted_at = now() - interval '59 minutes 59.5 seconds'
WHERE ctid = (SELECT ctid FROM app_reset_attempts WHERE app_id = 'urls4irl' ORDER BY attempted_at LIMIT 1)`); updateErr != nil {
		testInstance.Fatalf("backdate: %v", updateErr)
	}
	rateLimited := asRateLimited(testInstance, store.Reserve(ctx, "urls4irl", "person-a"))
	if rateLimited.RetryAfter != time.Second {
		testInstance.Fatalf("RetryAfter = %v, want 1s", rateLimited.RetryAfter)
	}
}

// newPooledResetStore returns a store over a pool of maxConns connections (the VPS runs 4), so a
// burst of concurrent callers queues on the pool instead of exhausting the server's connection slots.
func newPooledResetStore(testInstance *testing.T, maxConns int32) (context.Context, *PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, _, pool := newResetStore(testInstance)
	config := pool.Config().Copy()
	config.MaxConns = maxConns
	limited, poolErr := pgxpool.NewWithConfig(ctx, config)
	if poolErr != nil {
		testInstance.Fatalf("limited pool: %v", poolErr)
	}
	testInstance.Cleanup(limited.Close)
	return ctx, NewPGStore(limited), pool
}

func TestReserveConcurrentCallsForOnePersonAdmitExactlyPersonLimit(testInstance *testing.T) {
	ctx, store, pool := newPooledResetStore(testInstance, 4)
	admitted := runConcurrent(ctx, testInstance, store, 12, func(int) (string, string) { return "urls4irl", "person-a" })
	if admitted != PersonLimit {
		testInstance.Fatalf("admitted = %d, want %d", admitted, PersonLimit)
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", "person-a"); got != PersonLimit {
		testInstance.Fatalf("rows = %d, want %d", got, PersonLimit)
	}
}

func TestReserveConcurrentCallsForDistinctPeopleAdmitExactlyAppLimit(testInstance *testing.T) {
	ctx, store, pool := newPooledResetStore(testInstance, 4)
	callers := AppLimit + 10
	admitted := runConcurrent(ctx, testInstance, store, callers, func(index int) (string, string) {
		return "urls4irl", fmt.Sprintf("person-%d", index)
	})
	if admitted != AppLimit {
		testInstance.Fatalf("admitted = %d, want %d", admitted, AppLimit)
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", ""); got != AppLimit {
		testInstance.Fatalf("rows = %d, want %d", got, AppLimit)
	}
}

// runConcurrent runs callers Reserve calls at once and returns how many were admitted. Any error that
// is not a *RateLimitedError is reported as a test error.
func runConcurrent(
	ctx context.Context, testInstance *testing.T, store *PGStore, callers int, target func(int) (string, string),
) int {
	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	admitted := 0
	start := make(chan struct{})
	for index := range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			appID, personHash := target(index)
			<-start
			reserveErr := store.Reserve(ctx, appID, personHash)
			var rateLimited *RateLimitedError
			mutex.Lock()
			defer mutex.Unlock()
			if reserveErr == nil {
				admitted++
			} else if !errors.As(reserveErr, &rateLimited) {
				testInstance.Errorf("caller %d: unexpected error: %v", index, reserveErr)
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	return admitted
}

func TestReserveFailsWithANonRateLimitErrorWhenTheTableIsMissing(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	if _, dropErr := pool.Exec(ctx, `DROP TABLE app_reset_attempts`); dropErr != nil {
		testInstance.Fatalf("drop: %v", dropErr)
	}
	reserveErr := store.Reserve(ctx, "urls4irl", "person-a")
	var rateLimited *RateLimitedError
	if reserveErr == nil || errors.As(reserveErr, &rateLimited) {
		testInstance.Fatalf("err = %v, want a non-rate-limit error", reserveErr)
	}
}

func TestReserveFailsWithANonRateLimitErrorWhenTheContextIsCanceled(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	reserveErr := store.Reserve(canceled, "urls4irl", "person-a")
	var rateLimited *RateLimitedError
	if reserveErr == nil || errors.As(reserveErr, &rateLimited) {
		testInstance.Fatalf("err = %v, want a non-rate-limit error", reserveErr)
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", ""); got != 0 {
		testInstance.Fatalf("rows = %d, want 0", got)
	}
}

func cappedStore(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool) (*PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	config := pool.Config().Copy()
	config.MaxConns = 1
	capped, poolErr := pgxpool.NewWithConfig(ctx, config)
	if poolErr != nil {
		testInstance.Fatalf("capped pool: %v", poolErr)
	}
	testInstance.Cleanup(capped.Close)
	return NewPGStore(capped), capped
}

func TestReserveReturnsPromptlyWhenThePoolIsExhausted(testInstance *testing.T) {
	ctx, _, pool := newResetStore(testInstance)
	store, capped := cappedStore(ctx, testInstance, pool)
	held, acquireErr := capped.Acquire(ctx)
	if acquireErr != nil {
		testInstance.Fatalf("Acquire: %v", acquireErr)
	}
	defer held.Release()
	store.reserveTimeout = 50 * time.Millisecond

	started := time.Now()
	reserveErr := store.Reserve(ctx, "urls4irl", "person-a")

	var rateLimited *RateLimitedError
	if reserveErr == nil || errors.As(reserveErr, &rateLimited) || time.Since(started) > 5*time.Second {
		testInstance.Fatalf("err=%v after %v, want a prompt non-rate-limit error", reserveErr, time.Since(started))
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", ""); got != 0 {
		testInstance.Fatalf("rows = %d, want 0", got)
	}
}

func TestReserveNeedsOnlyOneConnectionAndReleasesIt(testInstance *testing.T) {
	ctx, _, pool := newResetStore(testInstance)
	store, capped := cappedStore(ctx, testInstance, pool)
	for attempt := 1; attempt <= 2; attempt++ {
		if reserveErr := store.Reserve(ctx, "urls4irl", "person-a"); reserveErr != nil {
			testInstance.Fatalf("reserve %d on a one-connection pool: %v", attempt, reserveErr)
		}
	}
	if acquired := capped.Stat().AcquiredConns(); acquired != 0 {
		testInstance.Fatalf("AcquiredConns = %d, want 0", acquired)
	}
}

func TestDeleteExpiredResetAttemptsDeletesOnlyOldRowsAndRefusesANonPositiveWindow(testInstance *testing.T) {
	ctx, store, pool := newResetStore(testInstance)
	seed(ctx, testInstance, pool, "urls4irl", "person-a", 2, 2*3600)
	seed(ctx, testInstance, pool, "urls4irl", "person-a", 1, 0)

	for _, window := range []time.Duration{0, -time.Hour} {
		if _, refusedErr := store.DeleteExpiredResetAttempts(ctx, time.Now(), window); refusedErr == nil {
			testInstance.Fatalf("window %v was accepted", window)
		}
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", ""); got != 3 {
		testInstance.Fatalf("rows after refused sweeps = %d, want 3", got)
	}
	deleted, deleteErr := store.DeleteExpiredResetAttempts(ctx, time.Now(), Window)
	if deleteErr != nil || deleted != 2 {
		testInstance.Fatalf("deleted=%d err=%v, want 2", deleted, deleteErr)
	}
	if got := countRows(ctx, testInstance, pool, "urls4irl", ""); got != 1 {
		testInstance.Fatalf("rows left = %d, want 1", got)
	}
}

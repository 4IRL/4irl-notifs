//go:build integration

package appidentity

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// insertRequest inserts a registration request row directly, so a test controls its status and
// timestamps. environment "" is stored as NULL.
func insertRequest(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool,
	appID string, claims Claims, status string, updatedAt time.Time,
) int64 {
	testInstance.Helper()
	var decidedAt *time.Time
	if status == RequestRejected {
		decidedAt = &updatedAt
	}
	var requestID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO app_registration_requests
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment,
   github_run_id, status, created_at, updated_at, decided_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'run-x', $8, $9, $9, $10) RETURNING id`,
		appID, claims.Repository, claims.RepositoryID, claims.RepositoryOwnerID, claims.WorkflowPath, claims.Ref,
		environmentArgument(claims.Environment), status, updatedAt, decidedAt).Scan(&requestID); err != nil {
		testInstance.Fatalf("insert request: %v", err)
	}
	return requestID
}

// issueTestKey issues a key for appID at now through the exchange transaction.
func issueTestKey(ctx context.Context, testInstance *testing.T, store *PGStore, appID string, now time.Time) IssuedKey {
	testInstance.Helper()
	var issued IssuedKey
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		var issueErr error
		issued, issueErr = tx.IssueKey(ctx, appID, "run", now)
		return issueErr
	}); err != nil {
		testInstance.Fatalf("issue key: %v", err)
	}
	return issued
}

func TestListRequestsOrdersPendingFirstAndDropsOldRejections(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")

	oldRejected := insertRequest(ctx, testInstance, pool, "oldrejected", claims, RequestRejected, testNow.Add(-RejectedRequestWindow-time.Hour))
	edgeRejected := insertRequest(ctx, testInstance, pool, "edgerejected", claims, RequestRejected, testNow.Add(-RejectedRequestWindow))
	recentRejected := insertRequest(ctx, testInstance, pool, "recentrejected", claims, RequestRejected, testNow.Add(-time.Hour))
	olderPending := insertRequest(ctx, testInstance, pool, "olderpending", claims, RequestPending, testNow.Add(-48*time.Hour))
	newerPending := insertRequest(ctx, testInstance, pool, "newerpending", claims, RequestPending, testNow.Add(-time.Minute))

	listed, err := store.ListRequests(ctx, testNow)
	if err != nil {
		testInstance.Fatalf("ListRequests: %v", err)
	}

	var gotIDs []int64
	for _, request := range listed {
		gotIDs = append(gotIDs, request.ID)
	}
	// Pending first (newest first), then rejected (newest first); a rejection exactly at the
	// window's edge is still listed, one older than it is not.
	wantIDs := []int64{newerPending, olderPending, recentRejected, edgeRejected}
	if len(gotIDs) != len(wantIDs) {
		testInstance.Fatalf("listed ids %v, want %v (old rejection %d must be dropped)", gotIDs, wantIDs, oldRejected)
	}
	for index := range wantIDs {
		if gotIDs[index] != wantIDs[index] {
			testInstance.Fatalf("listed ids %v, want %v", gotIDs, wantIDs)
		}
	}
	if first := listed[0]; first.AppID != "newerpending" || first.Status != RequestPending || first.DecidedAt != nil ||
		first.Repository != "4IRL/urls4irl" || first.RepositoryID != 1 || first.WorkflowPath != testWorkflow {
		testInstance.Errorf("first request = %+v", first)
	}
	if rejected := listed[2]; rejected.DecidedAt == nil {
		testInstance.Error("a rejected request must carry decided_at")
	}
}

func TestListRequestsIsEmptyNotNil(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	listed, err := store.ListRequests(ctx, testNow)

	if err != nil || listed == nil || len(listed) != 0 {
		testInstance.Errorf("ListRequests = %v, %v, want an empty non-nil list", listed, err)
	}
}

func TestDeleteExpiredRejectedRequestsDeletesOnlyRejectionsOlderThanTheWindow(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")
	oldRejected := insertRequest(ctx, testInstance, pool, "oldrejected", claims, RequestRejected, testNow.Add(-RejectedRequestWindow-time.Hour))
	edgeRejected := insertRequest(ctx, testInstance, pool, "edgerejected", claims, RequestRejected, testNow.Add(-RejectedRequestWindow))
	recentRejected := insertRequest(ctx, testInstance, pool, "recentrejected", claims, RequestRejected, testNow.Add(-time.Hour))
	// A pending request is never swept, however old.
	oldPending := insertRequest(ctx, testInstance, pool, "oldpending", claims, RequestPending, testNow.Add(-2*RejectedRequestWindow))

	deleted, err := store.DeleteExpiredRejectedRequests(ctx, testNow)

	if err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteExpiredRejectedRequests = %d, %v, want 1 row", deleted, err)
	}
	for _, kept := range []int64{edgeRejected, recentRejected, oldPending} {
		if count := countRows(ctx, testInstance, pool,
			`SELECT count(*) FROM app_registration_requests WHERE id = $1`, kept); count != 1 {
			testInstance.Errorf("request %d was deleted, want it kept", kept)
		}
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_registration_requests WHERE id = $1`, oldRejected); count != 0 {
		testInstance.Error("the rejection older than the window was kept")
	}
	if again, againErr := store.DeleteExpiredRejectedRequests(ctx, testNow); againErr != nil || again != 0 {
		testInstance.Errorf("second sweep = %d, %v, want 0", again, againErr)
	}
}

func TestDeleteStalePendingRequestsDeletesOnlyPendingRowsOlderThanTheTTL(testInstance *testing.T) {
	const pendingTTL = 7 * 24 * time.Hour
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")
	stalePending := insertRequest(ctx, testInstance, pool, "stalepending", claims, RequestPending, testNow.Add(-pendingTTL-time.Hour))
	edgePending := insertRequest(ctx, testInstance, pool, "edgepending", claims, RequestPending, testNow.Add(-pendingTTL))
	youngPending := insertRequest(ctx, testInstance, pool, "youngpending", claims, RequestPending, testNow.Add(-time.Hour))
	// A rejection is the other sweeper's job: it is kept here however old it is.
	oldRejected := insertRequest(ctx, testInstance, pool, "oldrejected", claims, RequestRejected, testNow.Add(-10*pendingTTL))
	insertTestBinding(ctx, testInstance, pool, "boundapp", testClaims(2, ""))

	deleted, err := store.DeleteStalePendingRequests(ctx, testNow, pendingTTL)

	if err != nil || deleted != 1 {
		testInstance.Fatalf("DeleteStalePendingRequests = %d, %v, want 1 row", deleted, err)
	}
	for _, kept := range []int64{edgePending, youngPending, oldRejected} {
		if count := countRows(ctx, testInstance, pool,
			`SELECT count(*) FROM app_registration_requests WHERE id = $1`, kept); count != 1 {
			testInstance.Errorf("request %d was deleted, want it kept", kept)
		}
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_registration_requests WHERE id = $1`, stalePending); count != 0 {
		testInstance.Error("the pending request older than the TTL was kept")
	}
	if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings WHERE app_id = 'boundapp'`); count != 1 {
		testInstance.Error("the sweep removed an approved binding")
	}
	if again, againErr := store.DeleteStalePendingRequests(ctx, testNow, pendingTTL); againErr != nil || again != 0 {
		testInstance.Errorf("second sweep = %d, %v, want 0", again, againErr)
	}
}

func TestDeleteStalePendingRequestsRefusesANonPositiveTTLAndDeletesNothing(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	pending := insertRequest(ctx, testInstance, pool, "pendingapp", testClaims(1, ""), RequestPending, testNow.Add(-time.Hour))

	for _, ttl := range []time.Duration{0, -time.Hour} {
		if deleted, err := store.DeleteStalePendingRequests(ctx, testNow, ttl); err == nil || deleted != 0 {
			testInstance.Errorf("ttl %v: DeleteStalePendingRequests = %d, %v, want 0 rows and an error", ttl, deleted, err)
		}
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_registration_requests WHERE id = $1`, pending); count != 1 {
		testInstance.Error("a refused sweep still deleted the pending request")
	}
}

func TestDeleteStalePendingRequestsKeepsARequestTheAppRefreshed(testInstance *testing.T) {
	const pendingTTL = 7 * 24 * time.Hour
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")
	insertRequest(ctx, testInstance, pool, "refreshedapp", claims, RequestPending, testNow.Add(-2*pendingTTL))

	// A repeat exchange with the same claims refreshes the one row, which resets its age.
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		_, fileErr := tx.FilePendingRequest(ctx, "refreshedapp", claims, testNow)
		return fileErr
	}); err != nil {
		testInstance.Fatalf("refresh: %v", err)
	}

	if deleted, err := store.DeleteStalePendingRequests(ctx, testNow, pendingTTL); err != nil || deleted != 0 {
		testInstance.Fatalf("DeleteStalePendingRequests = %d, %v, want 0 rows for a refreshed request", deleted, err)
	}
}

func TestDeleteStalePendingRequestsFreesTheGlobalPendingCap(testInstance *testing.T) {
	const pendingTTL = 7 * 24 * time.Hour
	ctx, store, pool := newTestStore(testInstance)
	for index := range MaxPendingRequests {
		insertRequest(ctx, testInstance, pool, "stale_"+strconv.Itoa(index), testClaims(int64(100+index), ""),
			RequestPending, testNow.Add(-pendingTTL-time.Hour))
	}
	fileNew := func() error {
		return store.Exchange(ctx, func(tx ExchangeTx) error {
			_, fileErr := tx.FilePendingRequest(ctx, "newcomer", testClaims(9999, ""), testNow)
			return fileErr
		})
	}
	if err := fileNew(); !errors.Is(err, ErrPendingCap) {
		testInstance.Fatalf("a new request at the cap err = %v, want ErrPendingCap", err)
	}

	if deleted, err := store.DeleteStalePendingRequests(ctx, testNow, pendingTTL); err != nil || deleted != MaxPendingRequests {
		testInstance.Fatalf("DeleteStalePendingRequests = %d, %v, want %d rows", deleted, err, MaxPendingRequests)
	}

	if err := fileNew(); err != nil {
		testInstance.Errorf("a new request after the stale ones were swept: %v", err)
	}
}

func TestApproveRequestCreatesTheBindingAndDeletesTheRequest(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	withEnvironment := testClaims(1, "production")
	withoutEnvironment := testClaims(2, "")
	withEnvironmentID := insertRequest(ctx, testInstance, pool, "urls4irl", withEnvironment, RequestPending, testNow)
	withoutEnvironmentID := insertRequest(ctx, testInstance, pool, "chores4irl", withoutEnvironment, RequestPending, testNow)

	binding, err := store.ApproveRequest(ctx, withEnvironmentID, "urls4irl", testNow)
	if err != nil {
		testInstance.Fatalf("ApproveRequest: %v", err)
	}
	if binding.AppID != "urls4irl" || binding.RepositoryID != 1 || binding.RepositoryOwnerID != 678 ||
		binding.WorkflowPath != testWorkflow || binding.Ref != "refs/heads/main" ||
		binding.Environment != "production" || !binding.ApprovedAt.Equal(testNow) {
		testInstance.Errorf("binding = %+v", binding)
	}
	if _, err := store.ApproveRequest(ctx, withoutEnvironmentID, "chores4irl", testNow); err != nil {
		testInstance.Fatalf("ApproveRequest (no environment): %v", err)
	}

	if requests := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registration_requests`); requests != 0 {
		testInstance.Errorf("requests left = %d, want 0: approval deletes the request", requests)
	}
	var environment *string
	if err := pool.QueryRow(ctx, `SELECT environment FROM app_bindings WHERE app_id = 'chores4irl'`).Scan(&environment); err != nil || environment != nil {
		testInstance.Errorf("no-environment binding stored environment %v (err %v), want NULL", environment, err)
	}

	// The approved binding matches the claims it was approved from, so the exchange issues.
	var locked Binding
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		var found bool
		var lockErr error
		locked, found, lockErr = tx.LockBinding(ctx, "urls4irl")
		if lockErr == nil && !found {
			lockErr = errors.New("binding not found")
		}
		return lockErr
	}); err != nil || !locked.Matches(withEnvironment) {
		testInstance.Errorf("approved binding does not match its own claims: %+v (err %v)", locked, err)
	}
}

// remainingRequestIDs returns the ids of every registration request row, in id order.
func remainingRequestIDs(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool) []int64 {
	testInstance.Helper()
	rows, err := pool.Query(ctx, `SELECT id FROM app_registration_requests ORDER BY id`)
	if err != nil {
		testInstance.Fatalf("list request ids: %v", err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var requestID int64
		if scanErr := rows.Scan(&requestID); scanErr != nil {
			testInstance.Fatalf("scan request id: %v", scanErr)
		}
		ids = append(ids, requestID)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		testInstance.Fatalf("list request ids: %v", rowsErr)
	}
	return ids
}

func TestGetRequestReadsOneRequestWithoutLockingIt(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	requestID := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(1, "production"), RequestPending, testNow)

	request, err := store.GetRequest(ctx, requestID)

	if err != nil || request.ID != requestID || request.AppID != "urls4irl" || request.Status != RequestPending || request.Environment != "production" {
		testInstance.Fatalf("GetRequest = %+v, %v", request, err)
	}
	if _, err := store.GetRequest(ctx, 987654); !errors.Is(err, ErrRequestNotFound) {
		testInstance.Errorf("GetRequest(unknown) err = %v, want ErrRequestNotFound", err)
	}
}

func TestApproveRequestRefusesARequestDeletedOrDecidedAfterItWasRead(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	deleted := insertRequest(ctx, testInstance, pool, "gone", testClaims(1, ""), RequestPending, testNow)
	decided := insertRequest(ctx, testInstance, pool, "decided", testClaims(2, ""), RequestPending, testNow)
	for _, requestID := range []int64{deleted, decided} {
		if _, err := store.GetRequest(ctx, requestID); err != nil {
			testInstance.Fatalf("GetRequest(%d): %v", requestID, err)
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM app_registration_requests WHERE id = $1`, deleted); err != nil {
		testInstance.Fatalf("delete request: %v", err)
	}
	if _, err := store.RejectRequest(ctx, decided, testNow); err != nil {
		testInstance.Fatalf("RejectRequest: %v", err)
	}

	if _, err := store.ApproveRequest(ctx, deleted, "gone", testNow); !errors.Is(err, ErrRequestNotFound) {
		testInstance.Errorf("deleted request err = %v, want ErrRequestNotFound", err)
	}
	if _, err := store.ApproveRequest(ctx, decided, "decided", testNow); !errors.Is(err, ErrRequestNotPending) {
		testInstance.Errorf("decided request err = %v, want ErrRequestNotPending", err)
	}
	if bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings`); bindings != 0 {
		testInstance.Errorf("bindings = %d, want 0", bindings)
	}
}

func TestApproveRequestDeletesSiblingPendingRequestsAndKeepsUnrelatedOnes(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	approved := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(1, ""), RequestPending, testNow)
	// Pending siblings that can never be approved once urls4irl is bound: the same app id under
	// other claims, and another app asking for the same workflow identity.
	sameAppID := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(5, ""), RequestPending, testNow)
	sameWorkflow := insertRequest(ctx, testInstance, pool, "chores4irl", testClaims(1, ""), RequestPending, testNow)
	// Unrelated: another app, and the same workflow file in another environment (a different identity).
	otherApp := insertRequest(ctx, testInstance, pool, "other", testClaims(9, ""), RequestPending, testNow)
	otherEnvironment := insertRequest(ctx, testInstance, pool, "staging", testClaims(1, "staging"), RequestPending, testNow)
	// A decision already made stays, even when it shares the app id or the workflow identity.
	rejectedSameAppID := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(7, ""), RequestRejected, testNow)
	rejectedSameWorkflow := insertRequest(ctx, testInstance, pool, "old", testClaims(1, ""), RequestRejected, testNow)

	if _, err := store.ApproveRequest(ctx, approved, "urls4irl", testNow); err != nil {
		testInstance.Fatalf("ApproveRequest: %v", err)
	}

	got := remainingRequestIDs(ctx, testInstance, pool)
	want := []int64{otherApp, otherEnvironment, rejectedSameAppID, rejectedSameWorkflow}
	if len(got) != len(want) {
		testInstance.Fatalf("remaining requests %v, want %v (sibling ids %d and %d must be gone)",
			got, want, sameAppID, sameWorkflow)
	}
	for index := range want {
		if got[index] != want[index] {
			testInstance.Fatalf("remaining requests %v, want %v", got, want)
		}
	}
}

func TestApproveRequestSiblingDeleteMatchesTheEnvironmentExactly(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	approved := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(1, "production"), RequestPending, testNow)
	sameWorkflow := insertRequest(ctx, testInstance, pool, "chores4irl", testClaims(1, "production"), RequestPending, testNow)
	noEnvironment := insertRequest(ctx, testInstance, pool, "staging", testClaims(1, ""), RequestPending, testNow)

	if _, err := store.ApproveRequest(ctx, approved, "urls4irl", testNow); err != nil {
		testInstance.Fatalf("ApproveRequest: %v", err)
	}

	got := remainingRequestIDs(ctx, testInstance, pool)
	if len(got) != 1 || got[0] != noEnvironment {
		testInstance.Fatalf("remaining requests %v, want only %d (sibling %d must be gone)", got, noEnvironment, sameWorkflow)
	}
}

func TestApproveRequestRefusesWithoutChangingAnything(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))

	sameApp := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(5, ""), RequestPending, testNow)
	// A different app asking for the workflow identity urls4irl already holds.
	sameWorkflow := insertRequest(ctx, testInstance, pool, "chores4irl", testClaims(1, ""), RequestPending, testNow)
	rejected := insertRequest(ctx, testInstance, pool, "later", testClaims(9, ""), RequestRejected, testNow)

	for _, testCase := range []struct {
		name      string
		requestID int64
		appID     string
		want      error
	}{
		{"app already bound", sameApp, "urls4irl", ErrAppAlreadyBound},
		{"workflow identity bound to another app", sameWorkflow, "chores4irl", ErrWorkflowIdentityBound},
		{"already rejected", rejected, "later", ErrRequestNotPending},
		{"unknown id", 987654, "urls4irl", ErrRequestNotFound},
		{"app id differs under lock", sameApp, "someone_else", ErrRequestChanged},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			_, err := store.ApproveRequest(ctx, testCase.requestID, testCase.appID, testNow)

			if !errors.Is(err, testCase.want) {
				subTest.Fatalf("err = %v, want %v", err, testCase.want)
			}
			if bindings := countRows(ctx, subTest, pool, `SELECT count(*) FROM app_bindings`); bindings != 1 {
				subTest.Errorf("bindings = %d, want 1: a refused approval must change nothing", bindings)
			}
			if requests := countRows(ctx, subTest, pool, `SELECT count(*) FROM app_registration_requests`); requests != 3 {
				subTest.Errorf("requests = %d, want all 3 kept", requests)
			}
		})
	}
}

func TestApproveRequestRefusesAMismatchedAppIDOnAFreshRequestWithNoCompetingBinding(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	requestID := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(1, ""), RequestPending, testNow)

	if _, err := store.ApproveRequest(ctx, requestID, "someone_else", testNow); !errors.Is(err, ErrRequestChanged) {
		testInstance.Fatalf("err = %v, want ErrRequestChanged", err)
	}

	request, getErr := store.GetRequest(ctx, requestID)
	if getErr != nil || request.Status != RequestPending {
		testInstance.Errorf("request = %+v, %v, want it still pending", request, getErr)
	}
	if bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings`); bindings != 0 {
		testInstance.Errorf("bindings = %d, want 0", bindings)
	}
}

func TestApproveRequestRefusesAnAppIDThatOverlapsABoundOneByAnUnderscoreExtension(testInstance *testing.T) {
	for _, testCase := range []struct {
		name     string
		bound    string
		approved string
		want     error
	}{
		{"extension of a bound id", "urls", "urls_beta", ErrAppIDOverlap},
		{"deeper extension of a bound id", "urls", "urls_a_b", ErrAppIDOverlap},
		{"bound id is an extension of the approved id", "urls_beta", "urls", ErrAppIDOverlap},
		{"a longer name without the underscore is a different namespace", "urls", "urls4irl", nil},
		{"a shorter name without the underscore is a different namespace", "urls4irl", "urls", nil},
		{"an underscore in the bound id is not a wildcard", "a_b", "axb_c", nil},
		{"an underscore in the approved id is not a wildcard", "axb", "a_b_c", nil},
		{"an unrelated id", "chores", "urls", nil},
		{"the same id is already bound, not an overlap", "urls", "urls", ErrAppAlreadyBound},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			ctx, store, pool := newTestStore(subTest)
			insertTestBinding(ctx, subTest, pool, testCase.bound, testClaims(1, ""))
			requestID := insertRequest(ctx, subTest, pool, testCase.approved, testClaims(2, ""), RequestPending, testNow)

			binding, err := store.ApproveRequest(ctx, requestID, testCase.approved, testNow)

			if testCase.want == nil {
				if err != nil || binding.AppID != testCase.approved {
					subTest.Fatalf("ApproveRequest = %+v, %v, want the binding approved", binding, err)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				subTest.Fatalf("err = %v, want %v", err, testCase.want)
			}
			if bindings := countRows(ctx, subTest, pool, `SELECT count(*) FROM app_bindings`); bindings != 1 {
				subTest.Errorf("bindings = %d, want 1: a refused approval must bind nothing", bindings)
			}
			if got := remainingRequestIDs(ctx, subTest, pool); len(got) != 1 || got[0] != requestID {
				subTest.Errorf("requests left = %v, want the refused request %d kept", got, requestID)
			}
		})
	}
}

// Approval deletes the approved app's pending siblings and refuses an overlapping app id; the two
// rules must hold together: a refused approval deletes nothing, and the overlapping request that
// the sibling delete deliberately leaves alone (it is a different app id) is still refused later.
func TestApproveRequestSiblingDeleteAndAppIDOverlapHoldTogether(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	approved := insertRequest(ctx, testInstance, pool, "urls", testClaims(1, ""), RequestPending, testNow)
	sibling := insertRequest(ctx, testInstance, pool, "urls", testClaims(5, ""), RequestPending, testNow)
	overlapping := insertRequest(ctx, testInstance, pool, "urls_beta", testClaims(2, ""), RequestPending, testNow)
	overlappingSibling := insertRequest(ctx, testInstance, pool, "urls_beta", testClaims(6, ""), RequestPending, testNow)

	if _, err := store.ApproveRequest(ctx, approved, "urls", testNow); err != nil {
		testInstance.Fatalf("ApproveRequest(urls): %v", err)
	}
	// Siblings of the approved app are gone; the other app id's requests are untouched.
	want := []int64{overlapping, overlappingSibling}
	got := remainingRequestIDs(ctx, testInstance, pool)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		testInstance.Fatalf("after approving urls, requests = %v, want %v (sibling %d gone)", got, want, sibling)
	}

	// urls_beta now overlaps the bound urls, so approving it is refused and deletes nothing, not
	// even its own sibling.
	if _, err := store.ApproveRequest(ctx, overlapping, "urls_beta", testNow); !errors.Is(err, ErrAppIDOverlap) {
		testInstance.Fatalf("ApproveRequest(urls_beta) err = %v, want ErrAppIDOverlap", err)
	}
	got = remainingRequestIDs(ctx, testInstance, pool)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		testInstance.Fatalf("after the refused approval, requests = %v, want %v unchanged", got, want)
	}
	if bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings`); bindings != 1 {
		testInstance.Errorf("bindings = %d, want only urls", bindings)
	}
}

func TestConcurrentApprovalsOfOverlappingAppIDsHaveExactlyOneWinner(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	requestIDs := []int64{
		insertRequest(ctx, testInstance, pool, "urls", testClaims(1, ""), RequestPending, testNow),
		insertRequest(ctx, testInstance, pool, "urls_beta", testClaims(2, ""), RequestPending, testNow),
	}
	appIDs := []string{"urls", "urls_beta"}

	results := make([]error, len(requestIDs))
	var group sync.WaitGroup
	for index, requestID := range requestIDs {
		group.Add(1)
		go func() {
			defer group.Done()
			_, results[index] = store.ApproveRequest(ctx, requestID, appIDs[index], testNow)
		}()
	}
	group.Wait()

	winners, overlaps := 0, 0
	for _, result := range results {
		switch {
		case result == nil:
			winners++
		case errors.Is(result, ErrAppIDOverlap):
			overlaps++
		default:
			testInstance.Errorf("unexpected approval error: %v", result)
		}
	}
	if winners != 1 || overlaps != 1 {
		testInstance.Errorf("winners = %d, overlaps = %d, want exactly one of each: %v", winners, overlaps, results)
	}
	if bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings`); bindings != 1 {
		testInstance.Errorf("bindings = %d, want 1", bindings)
	}
}

func TestConcurrentApprovalsOfOneRequestHaveExactlyOneWinner(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	requestID := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(1, ""), RequestPending, testNow)

	const racers = 6
	results := make([]error, racers)
	var group sync.WaitGroup
	for index := range racers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, results[index] = store.ApproveRequest(ctx, requestID, "urls4irl", testNow)
		}()
	}
	group.Wait()

	winners := 0
	for _, result := range results {
		switch {
		case result == nil:
			winners++
		case errors.Is(result, ErrRequestNotFound):
		default:
			testInstance.Errorf("loser err = %v, want ErrRequestNotFound", result)
		}
	}
	if winners != 1 {
		testInstance.Errorf("winners = %d, want exactly 1", winners)
	}
	if bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings`); bindings != 1 {
		testInstance.Errorf("bindings = %d, want 1", bindings)
	}
}

func TestRejectRequestIsIdempotent(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	requestID := insertRequest(ctx, testInstance, pool, "urls4irl", testClaims(1, ""), RequestPending, testNow.Add(-time.Hour))

	rejected, err := store.RejectRequest(ctx, requestID, testNow)
	if err != nil {
		testInstance.Fatalf("RejectRequest: %v", err)
	}
	if rejected.Status != RequestRejected || rejected.DecidedAt == nil || !rejected.DecidedAt.Equal(testNow) || !rejected.UpdatedAt.Equal(testNow) {
		testInstance.Errorf("rejected = %+v", rejected)
	}

	again, err := store.RejectRequest(ctx, requestID, testNow.Add(time.Hour))
	if err != nil {
		testInstance.Fatalf("RejectRequest again: %v", err)
	}
	if again.Status != RequestRejected || !again.DecidedAt.Equal(testNow) || !again.UpdatedAt.Equal(testNow) {
		testInstance.Errorf("a second rejection changed the request: %+v", again)
	}

	if _, err := store.RejectRequest(ctx, 987654, testNow); !errors.Is(err, ErrRequestNotFound) {
		testInstance.Errorf("RejectRequest of an unknown id err = %v, want ErrRequestNotFound", err)
	}
	if bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings`); bindings != 0 {
		testInstance.Errorf("bindings = %d: rejecting must never bind", bindings)
	}
}

func TestARejectedRequestDoesNotBlockANewPendingOneWithTheSameClaims(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")
	requestID := insertRequest(ctx, testInstance, pool, "urls4irl", claims, RequestPending, testNow)
	if _, err := store.RejectRequest(ctx, requestID, testNow); err != nil {
		testInstance.Fatalf("RejectRequest: %v", err)
	}

	var newID int64
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		var fileErr error
		newID, fileErr = tx.FilePendingRequest(ctx, "urls4irl", claims, testNow.Add(time.Minute))
		return fileErr
	}); err != nil {
		testInstance.Fatalf("FilePendingRequest after a rejection: %v", err)
	}

	if newID == requestID {
		testInstance.Error("the new request must be a new row, not the rejected one")
	}
	listed, err := store.ListRequests(ctx, testNow.Add(time.Minute))
	if err != nil || len(listed) != 2 || listed[0].ID != newID || listed[0].Status != RequestPending || listed[1].Status != RequestRejected {
		testInstance.Errorf("listed = %+v (err %v), want the new pending request, then the rejected one", listed, err)
	}
}

func TestListBindingsReturnsKeysNewestFirstWithDerivedStates(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, "production"))
	insertTestBinding(ctx, testInstance, pool, "chores4irl", testClaims(2, ""))

	// urls4irl: pastGrace (superseded 25h ago) -> inGrace (superseded 1h ago) -> active.
	pastGrace := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-26*time.Hour))
	inGrace := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-25*time.Hour))
	active := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-time.Hour))
	revoked := issueTestKey(ctx, testInstance, store, "chores4irl", testNow.Add(-time.Hour))
	if _, err := store.RevokeKey(ctx, revoked.ID, testNow); err != nil {
		testInstance.Fatalf("RevokeKey: %v", err)
	}

	listed, err := store.ListBindings(ctx, testNow)
	if err != nil {
		testInstance.Fatalf("ListBindings: %v", err)
	}

	if len(listed) != 2 || listed[0].Binding.AppID != "chores4irl" || listed[1].Binding.AppID != "urls4irl" {
		testInstance.Fatalf("listed = %+v, want chores4irl then urls4irl", listed)
	}
	if listed[1].Binding.Environment != "production" || listed[0].Binding.Environment != "" {
		testInstance.Errorf("environments = %q / %q", listed[1].Binding.Environment, listed[0].Binding.Environment)
	}
	keys := listed[1].Keys
	if len(keys) != 3 || keys[0].ID != active.ID || keys[1].ID != inGrace.ID || keys[2].ID != pastGrace.ID {
		testInstance.Fatalf("urls4irl keys = %+v, want newest first: active, inGrace, pastGrace", keys)
	}
	if keys[0].State != KeyActive || keys[1].State != KeySuperseded || keys[2].State != KeyRevoked {
		testInstance.Errorf("states = %s/%s/%s, want active/superseded/revoked", keys[0].State, keys[1].State, keys[2].State)
	}
	if keys[2].RevokedReason != RevokedReasonSuperseded || keys[2].RevokedAt == nil ||
		!keys[2].RevokedAt.Equal(testNow.Add(-25*time.Hour).Add(SupersededGrace)) {
		testInstance.Errorf("a key past its grace window = %+v, want revoked with reason superseded at the window's end", keys[2])
	}
	for _, key := range keys {
		if key.Hint == "" || key.Hint[:4] != KeyPrefix || key.AppID != "urls4irl" {
			testInstance.Errorf("key = %+v", key)
		}
	}
	if chores := listed[0].Keys; len(chores) != 1 || chores[0].State != KeyRevoked || chores[0].RevokedReason != RevokedReasonOperator {
		testInstance.Errorf("chores4irl keys = %+v, want the operator-revoked key", chores)
	}
}

func TestListBindingsOfAnUnboundWorldIsEmptyNotNil(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	listed, err := store.ListBindings(ctx, testNow)
	if err != nil || listed == nil || len(listed) != 0 {
		testInstance.Errorf("ListBindings = %v, %v, want an empty non-nil list", listed, err)
	}

	// A binding with no keys lists an empty, non-nil key list.
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))
	listed, err = store.ListBindings(ctx, testNow)
	if err != nil || len(listed) != 1 || listed[0].Keys == nil || len(listed[0].Keys) != 0 {
		testInstance.Errorf("ListBindings = %+v, %v, want one binding with an empty non-nil key list", listed, err)
	}
}

func TestRevokeKeyStopsTheKeyAtOnceAndIsIdempotent(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))
	issued := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-time.Hour))
	if _, err := store.ResolveKey(ctx, issued.Plaintext, testNow); err != nil {
		testInstance.Fatalf("the fresh key must resolve before it is revoked: %v", err)
	}

	revoked, err := store.RevokeKey(ctx, issued.ID, testNow)
	if err != nil {
		testInstance.Fatalf("RevokeKey: %v", err)
	}
	if revoked.State != KeyRevoked || revoked.RevokedReason != RevokedReasonOperator || revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(testNow) {
		testInstance.Errorf("revoked = %+v", revoked)
	}
	if _, err := store.ResolveKey(ctx, issued.Plaintext, testNow); !errors.Is(err, ErrInvalidKey) {
		testInstance.Errorf("a revoked key resolved: err = %v, want ErrInvalidKey immediately", err)
	}

	again, err := store.RevokeKey(ctx, issued.ID, testNow.Add(time.Hour))
	if err != nil || again.State != KeyRevoked || !again.RevokedAt.Equal(testNow) {
		testInstance.Errorf("a second revoke = %+v, %v, want the first revoke time kept", again, err)
	}
	if _, err := store.RevokeKey(ctx, 987654, testNow); !errors.Is(err, ErrKeyNotFound) {
		testInstance.Errorf("RevokeKey of an unknown id err = %v, want ErrKeyNotFound", err)
	}
}

func TestRevokeKeyOfASupersededKeyAndOfAKeyPastItsGraceWindow(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))
	pastGrace := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-30*time.Hour))
	inGrace := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-2*time.Hour))
	issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-time.Hour))

	// Inside its grace window a superseded key can still be revoked early.
	revoked, err := store.RevokeKey(ctx, inGrace.ID, testNow)
	if err != nil || revoked.State != KeyRevoked || revoked.RevokedReason != RevokedReasonOperator {
		testInstance.Errorf("revoking a superseded key = %+v, %v", revoked, err)
	}
	if _, err := store.ResolveKey(ctx, inGrace.Plaintext, testNow); !errors.Is(err, ErrInvalidKey) {
		testInstance.Errorf("the early-revoked superseded key still resolves: %v", err)
	}

	// A key that already stopped working keeps reading "superseded": revoking it is a no-op.
	if _, err := pool.Exec(ctx, `UPDATE app_keys SET superseded_at = $2 WHERE id = $1`, pastGrace.ID, testNow.Add(-25*time.Hour)); err != nil {
		testInstance.Fatalf("set superseded_at: %v", err)
	}
	unchanged, err := store.RevokeKey(ctx, pastGrace.ID, testNow)
	if err != nil || unchanged.State != KeyRevoked || unchanged.RevokedReason != RevokedReasonSuperseded {
		testInstance.Errorf("revoking a key past its grace window = %+v, %v, want it unchanged", unchanged, err)
	}
	if revokedRows := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_keys WHERE id = $1 AND revoked_at IS NOT NULL`, pastGrace.ID); revokedRows != 0 {
		testInstance.Error("revoking a key past its grace window must not stamp a revoke on it")
	}
}

func TestUnbindInvalidatesEveryKeyAndIsIdempotent(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))
	insertTestBinding(ctx, testInstance, pool, "chores4irl", testClaims(2, ""))
	first := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-time.Hour))
	second := issueTestKey(ctx, testInstance, store, "urls4irl", testNow)
	other := issueTestKey(ctx, testInstance, store, "chores4irl", testNow)

	deleted, err := store.Unbind(ctx, "urls4irl")
	if err != nil || !deleted {
		testInstance.Fatalf("Unbind = %v, %v, want deleted", deleted, err)
	}

	for _, key := range []IssuedKey{first, second} {
		if _, err := store.ResolveKey(ctx, key.Plaintext, testNow); !errors.Is(err, ErrInvalidKey) {
			testInstance.Errorf("key %s still resolves after unbind: %v", key.Hint, err)
		}
	}
	if _, err := store.ResolveKey(ctx, other.Plaintext, testNow); err != nil {
		testInstance.Errorf("another app's key must be untouched: %v", err)
	}

	again, err := store.Unbind(ctx, "urls4irl")
	if err != nil || again {
		testInstance.Errorf("a second Unbind = %v, %v, want deleted=false and no error", again, err)
	}
}

func TestUnbindKeepsTheAppOwnedObjects(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))
	if _, err := pool.Exec(ctx,
		`INSERT INTO destinations (name, type, owner_kind, owner_app_id, config, sealed_secrets)
VALUES ('urls4irl_hook', 'webhook', 'app', 'urls4irl', '{}', '\x00')`); err != nil {
		testInstance.Fatalf("seed an app-owned destination: %v", err)
	}

	if deleted, err := store.Unbind(ctx, "urls4irl"); err != nil || !deleted {
		testInstance.Fatalf("Unbind = %v, %v", deleted, err)
	}

	if owned := countRows(ctx, testInstance, pool, `SELECT count(*) FROM destinations WHERE owner_app_id = 'urls4irl'`); owned != 1 {
		testInstance.Errorf("owned destinations = %d, want 1: unbinding keeps the app's objects", owned)
	}
}

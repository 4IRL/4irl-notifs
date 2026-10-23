//go:build integration

package appidentity

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

const raceIterations = 25

func TestFilePendingRequestAfterAnApprovalCommittedReportsTheBinding(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")
	requestID := insertRequest(ctx, testInstance, pool, "urls4irl", claims, RequestPending, testNow)

	// Deterministic interleaving: the exchange reads the binding (none), an approval then commits
	// in full, and only then does the exchange go to file its request.
	var fileErr error
	exchangeErr := store.Exchange(ctx, func(tx ExchangeTx) error {
		if _, found, lockErr := tx.LockBinding(ctx, "urls4irl"); lockErr != nil || found {
			return fmt.Errorf("LockBinding found = %v, err = %v, want no binding yet", found, lockErr)
		}
		if _, approveErr := store.ApproveRequest(ctx, requestID, testNow); approveErr != nil {
			return fmt.Errorf("ApproveRequest: %w", approveErr)
		}
		_, fileErr = tx.FilePendingRequest(ctx, "urls4irl", claims, testNow)
		return fileErr
	})

	if exchangeErr == nil || !errors.Is(fileErr, ErrBindingAppeared) {
		testInstance.Fatalf("exchange err = %v, FilePendingRequest err = %v, want ErrBindingAppeared", exchangeErr, fileErr)
	}
	if pending := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registration_requests WHERE app_id = 'urls4irl'`); pending != 0 {
		testInstance.Errorf("requests for the bound app = %d, want 0: nothing may be filed after the approval", pending)
	}
}

func TestApprovalRacingAnExchangeNeverLeavesAPendingRequestForABoundApp(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	for iteration := range raceIterations {
		appID := fmt.Sprintf("raceapp_%d", iteration)
		claims := testClaims(int64(iteration+1), "")
		requestID := insertRequest(ctx, testInstance, pool, appID, claims, RequestPending, testNow)

		start := make(chan struct{})
		var group sync.WaitGroup
		var approveErr, exchangeErr error
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, approveErr = store.ApproveRequest(ctx, requestID, testNow)
		}()
		go func() {
			defer group.Done()
			<-start
			exchangeErr = store.Exchange(ctx, func(tx ExchangeTx) error {
				_, found, lockErr := tx.LockBinding(ctx, appID)
				if lockErr != nil || found {
					return lockErr
				}
				_, fileErr := tx.FilePendingRequest(ctx, appID, claims, testNow)
				if errors.Is(fileErr, ErrBindingAppeared) {
					return nil
				}
				return fileErr
			})
		}()
		close(start)
		group.Wait()

		if approveErr != nil || exchangeErr != nil {
			testInstance.Fatalf("iteration %d: approve err = %v, exchange err = %v", iteration, approveErr, exchangeErr)
		}
		if bound := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings WHERE app_id = $1`, appID); bound != 1 {
			testInstance.Fatalf("iteration %d: bindings = %d, want 1", iteration, bound)
		}
		if pending := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registration_requests WHERE app_id = $1`, appID); pending != 0 {
			testInstance.Fatalf("iteration %d: %d request rows left for a bound app, want 0", iteration, pending)
		}
	}
}

func TestApprovalRacingARejectionHasExactlyOneOutcome(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	for iteration := range raceIterations {
		appID := fmt.Sprintf("raceapp_%d", iteration)
		requestID := insertRequest(ctx, testInstance, pool, appID, testClaims(int64(iteration+1), ""), RequestPending, testNow)

		start := make(chan struct{})
		var group sync.WaitGroup
		var approveErr, rejectErr error
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, approveErr = store.ApproveRequest(ctx, requestID, testNow)
		}()
		go func() {
			defer group.Done()
			<-start
			_, rejectErr = store.RejectRequest(ctx, requestID, testNow)
		}()
		close(start)
		group.Wait()

		bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings WHERE app_id = $1`, appID)
		rejected := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registration_requests WHERE id = $1 AND status = 'rejected'`, requestID)
		switch {
		case approveErr == nil && errors.Is(rejectErr, ErrRequestNotFound) && bindings == 1 && rejected == 0:
			// Approved first: the request is gone, so the late rejection finds nothing.
		case rejectErr == nil && errors.Is(approveErr, ErrRequestNotPending) && bindings == 0 && rejected == 1:
			// Rejected first: the late approval is refused and binds nothing.
		default:
			testInstance.Fatalf("iteration %d: approve err = %v, reject err = %v, bindings = %d, rejected rows = %d: want exactly one of approved or rejected",
				iteration, approveErr, rejectErr, bindings, rejected)
		}
	}
}

func TestTheSupersedeGraceWindowEndsExactlyAfter24Hours(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	insertTestBinding(ctx, testInstance, pool, "urls4irl", testClaims(1, ""))
	insertTestBinding(ctx, testInstance, pool, "chores4irl", testClaims(2, ""))

	// urls4irl's old key was superseded exactly 24 h ago: no longer valid. chores4irl's was
	// superseded a microsecond later: still valid for that microsecond.
	expired := issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-25*time.Hour))
	issueTestKey(ctx, testInstance, store, "urls4irl", testNow.Add(-SupersededGrace))
	lastMoment := issueTestKey(ctx, testInstance, store, "chores4irl", testNow.Add(-25*time.Hour))
	issueTestKey(ctx, testInstance, store, "chores4irl", testNow.Add(-SupersededGrace+time.Microsecond))

	if _, err := store.ResolveKey(ctx, expired.Plaintext, testNow); !errors.Is(err, ErrInvalidKey) {
		testInstance.Errorf("a key superseded exactly 24h ago resolved: err = %v, want ErrInvalidKey", err)
	}
	unchanged, err := store.RevokeKey(ctx, expired.ID, testNow)
	if err != nil || unchanged.State != KeyRevoked || unchanged.RevokedReason != RevokedReasonSuperseded {
		testInstance.Errorf("revoking a key at the exact boundary = %+v, %v, want a no-op that reads superseded", unchanged, err)
	}
	if stamped := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_keys WHERE id = $1 AND revoked_at IS NOT NULL`, expired.ID); stamped != 0 {
		testInstance.Error("the no-op revoke at the boundary stamped a revoke on the key")
	}

	if _, err := store.ResolveKey(ctx, lastMoment.Plaintext, testNow); err != nil {
		testInstance.Errorf("a key superseded 24h minus 1us ago must still resolve: %v", err)
	}
	revoked, err := store.RevokeKey(ctx, lastMoment.ID, testNow)
	if err != nil || revoked.RevokedReason != RevokedReasonOperator {
		testInstance.Errorf("revoking a key inside the window = %+v, %v, want an operator revoke", revoked, err)
	}
}

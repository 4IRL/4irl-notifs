//go:build integration

package appidentity

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

const concurrentCreators = 8

func TestConcurrentFirstCreationOfOneNameYieldsExactlyOneBinding(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, ""), "hosted_")

	for iteration := range raceIterations {
		appID := fmt.Sprintf("hosted_race%d", iteration)
		start := make(chan struct{})
		results := make([]error, concurrentCreators)
		var group sync.WaitGroup
		for index := range concurrentCreators {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				results[index] = createRegistrarApp(ctx, store, registrar, appID)
			}()
		}
		close(start)
		group.Wait()

		succeeded, appeared := 0, 0
		for _, result := range results {
			switch {
			case result == nil:
				succeeded++
			case errors.Is(result, ErrBindingAppeared):
				appeared++
			default:
				testInstance.Fatalf("iteration %d: unexpected error %v", iteration, result)
			}
		}
		if succeeded != 1 || appeared != concurrentCreators-1 {
			testInstance.Fatalf("iteration %d: %d created, %d ErrBindingAppeared, want 1 and %d",
				iteration, succeeded, appeared, concurrentCreators-1)
		}
		if bound := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings WHERE app_id = $1`, appID); bound != 1 {
			testInstance.Fatalf("iteration %d: bindings = %d, want 1", iteration, bound)
		}
	}
}

func TestRegistrarApprovalRacingANormalApprovalOfAnOverlappingIDHasOneWinner(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	for iteration := range raceIterations {
		prefix := fmt.Sprintf("pfx%d_", iteration)
		appID := prefix + "blog"
		registrarRequest := insertRegistrarRequest(ctx, testInstance, pool, testClaims(int64(1000+iteration), ""), RequestPending)
		appRequest := insertRequest(ctx, testInstance, pool, appID, testClaims(int64(2000+iteration), ""), RequestPending, testNow)

		start := make(chan struct{})
		var group sync.WaitGroup
		var registrarErr, appErr error
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, registrarErr = store.ApproveRegistrarRequest(ctx, registrarRequest, prefix, testNow)
		}()
		go func() {
			defer group.Done()
			<-start
			_, appErr = store.ApproveRequest(ctx, appRequest, appID, testNow)
		}()
		close(start)
		group.Wait()

		registrars := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registrars WHERE prefix = $1`, prefix)
		bindings := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings WHERE app_id = $1`, appID)
		switch {
		// The registrar won: the app request was swept to rejected (the approval then finds it not
		// pending) or, were it still pending, refused for sitting inside the prefix.
		case registrarErr == nil && (errors.Is(appErr, ErrRequestNotPending) || errors.Is(appErr, ErrAppIDInRegistrarPrefix)) &&
			registrars == 1 && bindings == 0:
		case appErr == nil && errors.Is(registrarErr, ErrPrefixOverlap) && registrars == 0 && bindings == 1:
		default:
			testInstance.Fatalf("iteration %d: registrar err = %v, app err = %v, registrars = %d, bindings = %d: want exactly one winner",
				iteration, registrarErr, appErr, registrars, bindings)
		}
	}
}

func TestDeleteRegistrarRacingBindingCreationNeverOrphansOrViolatesAnFK(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	for iteration := range raceIterations {
		claims := testClaims(int64(1+iteration), "")
		registrar := approveTestRegistrar(ctx, testInstance, pool, store, claims, fmt.Sprintf("del%d_", iteration))
		appID := fmt.Sprintf("del%d_app", iteration)

		start := make(chan struct{})
		var group sync.WaitGroup
		var createErr, deleteErr error
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			createErr = createRegistrarApp(ctx, store, registrar, appID)
		}()
		go func() {
			defer group.Done()
			<-start
			_, _, deleteErr = store.DeleteRegistrar(ctx, registrar.ID)
		}()
		close(start)
		group.Wait()

		if deleteErr != nil || (createErr != nil && !errors.Is(createErr, ErrRegistrarNotFound)) {
			testInstance.Fatalf("iteration %d: create err = %v, delete err = %v, want nil/ErrRegistrarNotFound and nil",
				iteration, createErr, deleteErr)
		}
		if bound := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings WHERE registrar_id IS NOT NULL`); bound != 0 {
			testInstance.Fatalf("iteration %d: %d registrar bindings left after the registrar was deleted", iteration, bound)
		}
	}
}

func TestRegistrarApprovalRacingFilingInsideThePrefixLeavesNoPendingRequestThere(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	for iteration := range raceIterations {
		prefix := fmt.Sprintf("file%d_", iteration)
		appID := prefix + "blog"
		registrarRequest := insertRegistrarRequest(ctx, testInstance, pool, testClaims(int64(1000+iteration), ""), RequestPending)
		appClaims := testClaims(int64(2000+iteration), "")

		start := make(chan struct{})
		var group sync.WaitGroup
		var approveErr, fileErr error
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, approveErr = store.ApproveRegistrarRequest(ctx, registrarRequest, prefix, testNow)
		}()
		go func() {
			defer group.Done()
			<-start
			fileErr = store.Exchange(ctx, func(tx ExchangeTx) error {
				_, filingErr := tx.FilePendingRequest(ctx, appID, appClaims, testNow)
				return filingErr
			})
		}()
		close(start)
		group.Wait()

		if approveErr != nil || (fileErr != nil && !errors.Is(fileErr, ErrAppIDInRegistrarPrefix)) {
			testInstance.Fatalf("iteration %d: approve err = %v, file err = %v", iteration, approveErr, fileErr)
		}
		if pending := countRows(ctx, testInstance, pool,
			`SELECT count(*) FROM app_registration_requests WHERE status = 'pending' AND app_id = $1`, appID); pending != 0 {
			testInstance.Fatalf("iteration %d: %d pending request(s) left inside the registrar prefix", iteration, pending)
		}
	}
}

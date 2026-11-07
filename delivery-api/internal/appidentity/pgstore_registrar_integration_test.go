//go:build integration

package appidentity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// insertRegistrarRequest inserts a pending kind "registrar" request (NULL app_id) directly.
func insertRegistrarRequest(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, claims Claims, status string,
) int64 {
	testInstance.Helper()
	var decidedAt *time.Time
	if status == RequestRejected {
		decidedAt = &testNow
	}
	var requestID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO app_registration_requests
  (kind, app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment,
   github_run_id, status, created_at, updated_at, decided_at)
VALUES ('registrar', NULL, $1, $2, $3, $4, $5, $6, 'run-x', $7, $8, $8, $9) RETURNING id`,
		claims.Repository, claims.RepositoryID, claims.RepositoryOwnerID, claims.WorkflowPath, claims.Ref,
		environmentArgument(claims.Environment), status, testNow, decidedAt).Scan(&requestID); err != nil {
		testInstance.Fatalf("insert registrar request: %v", err)
	}
	return requestID
}

// approveTestRegistrar files a registrar request for claims and approves it with prefix.
func approveTestRegistrar(
	ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, store *PGStore, claims Claims, prefix string,
) WorkflowRegistrar {
	testInstance.Helper()
	requestID := insertRegistrarRequest(ctx, testInstance, pool, claims, RequestPending)
	registrar, err := store.ApproveRegistrarRequest(ctx, requestID, prefix, testNow)
	if err != nil {
		testInstance.Fatalf("ApproveRegistrarRequest(%q): %v", prefix, err)
	}
	return registrar
}

// createRegistrarApp runs CreateRegistrarBinding in its own exchange transaction.
func createRegistrarApp(ctx context.Context, store *PGStore, registrar WorkflowRegistrar, appID string) error {
	return store.Exchange(ctx, func(tx ExchangeTx) error {
		return tx.CreateRegistrarBinding(ctx, registrar, appID, testNow)
	})
}

func mustCreateRegistrarApp(
	ctx context.Context, testInstance *testing.T, store *PGStore, registrar WorkflowRegistrar, appID string,
) {
	testInstance.Helper()
	if err := createRegistrarApp(ctx, store, registrar, appID); err != nil {
		testInstance.Fatalf("CreateRegistrarBinding(%q): %v", appID, err)
	}
}

func TestApproveRegistrarRequestCreatesTheRegistrarAndSweepsTheQueue(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "production")
	otherClaims := testClaims(2, "")
	requestID := insertRegistrarRequest(ctx, testInstance, pool, claims, RequestPending)
	// A second pending registrar request would need a different identity, so a sibling is an app
	// request of the same identity.
	siblingID := insertRequest(ctx, testInstance, pool, "somethingelse", claims, RequestPending, testNow)
	covered := insertRequest(ctx, testInstance, pool, "hosted_blog", otherClaims, RequestPending, testNow)
	coveredStem := insertRequest(ctx, testInstance, pool, "hosted", testClaims(3, ""), RequestPending, testNow)
	unrelated := insertRequest(ctx, testInstance, pool, "hostedother", testClaims(4, ""), RequestPending, testNow)
	rejectedAlready := insertRequest(ctx, testInstance, pool, "hosted_old", testClaims(5, ""), RequestRejected, testNow.Add(-time.Hour))

	registrar, err := store.ApproveRegistrarRequest(ctx, requestID, "hosted_", testNow)
	if err != nil {
		testInstance.Fatalf("ApproveRegistrarRequest: %v", err)
	}

	if registrar.Prefix != "hosted_" || registrar.RepositoryID != 1 || registrar.Environment != "production" ||
		registrar.AppCount != 0 || registrar.AppLimit != RegistrarAppLimit || !registrar.ApprovedAt.Equal(testNow) ||
		!registrar.Matches(claims) {
		testInstance.Errorf("registrar = %+v", registrar)
	}
	for _, gone := range []int64{requestID, siblingID} {
		if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registration_requests WHERE id = $1`, gone); count != 0 {
			testInstance.Errorf("request %d still exists, want it deleted with the approval", gone)
		}
	}
	for _, swept := range []int64{covered, coveredStem} {
		if count := countRows(ctx, testInstance, pool,
			`SELECT count(*) FROM app_registration_requests WHERE id = $1 AND status = 'rejected' AND decided_at = $2`,
			swept, testNow); count != 1 {
			testInstance.Errorf("request %d inside the prefix was not rejected at the approval time", swept)
		}
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_registration_requests WHERE id = $1 AND status = 'pending'`, unrelated); count != 1 {
		testInstance.Error("a request outside the prefix must stay pending")
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_registration_requests WHERE id = $1 AND decided_at = $2`, rejectedAlready, testNow.Add(-time.Hour)); count != 1 {
		testInstance.Error("an already rejected request must keep its decision time")
	}
}

func TestApproveRegistrarRequestRefusals(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	existing := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(10, ""), "hosted_")
	insertTestBinding(ctx, testInstance, pool, "chores", testClaims(11, ""))
	insertTestBinding(ctx, testInstance, pool, "solo_app", testClaims(12, ""))
	insertTestBinding(ctx, testInstance, pool, "appstem", testClaims(13, ""))

	testCases := []struct {
		label  string
		prefix string
		claims Claims
		want   error
	}{
		{"the same prefix", "hosted_", testClaims(20, ""), ErrPrefixTaken},
		{"a prefix inside an existing prefix", "hosted_eu_", testClaims(21, ""), ErrPrefixOverlap},
		{"a prefix that merely shares letters with an existing prefix", "host_", testClaims(22, ""), nil},
		{"a prefix that is a shorter extension of an existing prefix", "hosted_e_", testClaims(23, ""), ErrPrefixOverlap},
		{"a prefix equal to a bound app id stem", "chores_", testClaims(24, ""), ErrPrefixOverlap},
		{"a prefix under a bound app id", "solo_app_x_", testClaims(25, ""), ErrPrefixOverlap},
		{"a prefix a bound app id starts with", "app_", testClaims(26, ""), nil},
		{"a prefix a bound app id lives under", "appstem_", testClaims(27, ""), ErrPrefixOverlap},
		{"an invalid prefix", "Bad-", testClaims(28, ""), ErrInvalidPrefix},
		{"a platform prefix", "platform_eu_", testClaims(29, ""), ErrInvalidPrefix},
		{"the everyone stem", "everyone_", testClaims(30, ""), ErrInvalidPrefix},
		{"no trailing underscore", "hosted2", testClaims(31, ""), ErrInvalidPrefix},
		{"an identity that is already a registrar", "fresh_", testClaims(10, ""), ErrWorkflowIdentityBound},
		{"an identity that is a normal binding", "other_", testClaims(11, ""), ErrWorkflowIdentityBound},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			requestID := insertRegistrarRequest(ctx, subTest, pool, testCase.claims, RequestPending)
			_, err := store.ApproveRegistrarRequest(ctx, requestID, testCase.prefix, testNow)
			if !errors.Is(err, testCase.want) && !(testCase.want == nil && err == nil) {
				subTest.Fatalf("err = %v, want %v", err, testCase.want)
			}
			if testCase.want != nil {
				if count := countRows(ctx, subTest, pool,
					`SELECT count(*) FROM app_registration_requests WHERE id = $1 AND status = 'pending'`, requestID); count != 1 {
					subTest.Error("a refused approval must leave the request pending")
				}
				if count := countRows(ctx, subTest, pool, `SELECT count(*) FROM app_registrars`); count != 1 {
					subTest.Errorf("registrars = %d, want only the first one", count)
				}
			} else {
				// Not an overlap: clean up so later cases still see exactly the first registrar.
				if _, execErr := pool.Exec(ctx, `DELETE FROM app_registrars WHERE id <> $1`, existing.ID); execErr != nil {
					subTest.Fatalf("cleanup: %v", execErr)
				}
			}
		})
	}

	testInstance.Run("a request that is not pending", func(subTest *testing.T) {
		requestID := insertRegistrarRequest(ctx, subTest, pool, testClaims(40, ""), RequestRejected)
		if _, err := store.ApproveRegistrarRequest(ctx, requestID, "fresh_", testNow); !errors.Is(err, ErrRequestNotPending) {
			subTest.Errorf("err = %v, want ErrRequestNotPending", err)
		}
	})
	testInstance.Run("an app request", func(subTest *testing.T) {
		requestID := insertRequest(ctx, subTest, pool, "freshapp", testClaims(41, ""), RequestPending, testNow)
		if _, err := store.ApproveRegistrarRequest(ctx, requestID, "fresh_", testNow); !errors.Is(err, ErrRequestNotRegistrar) {
			subTest.Errorf("err = %v, want ErrRequestNotRegistrar", err)
		}
	})
	testInstance.Run("an unknown request", func(subTest *testing.T) {
		if _, err := store.ApproveRegistrarRequest(ctx, 999999, "fresh_", testNow); !errors.Is(err, ErrRequestNotFound) {
			subTest.Errorf("err = %v, want ErrRequestNotFound", err)
		}
	})
}

func TestNormalApproveRequestRefusesRegistrarCases(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrarClaims := testClaims(1, "")
	approveTestRegistrar(ctx, testInstance, pool, store, registrarClaims, "hosted_")

	testInstance.Run("a registrar-kind request", func(subTest *testing.T) {
		requestID := insertRegistrarRequest(ctx, subTest, pool, testClaims(2, ""), RequestPending)
		if _, err := store.ApproveRequest(ctx, requestID, "", testNow); !errors.Is(err, ErrRequestIsRegistrar) {
			subTest.Errorf("err = %v, want ErrRequestIsRegistrar", err)
		}
	})
	testInstance.Run("an app id inside the prefix", func(subTest *testing.T) {
		requestID := insertRequest(ctx, subTest, pool, "hosted_blog", testClaims(3, ""), RequestPending, testNow)
		if _, err := store.ApproveRequest(ctx, requestID, "hosted_blog", testNow); !errors.Is(err, ErrAppIDInRegistrarPrefix) {
			subTest.Errorf("err = %v, want ErrAppIDInRegistrarPrefix", err)
		}
	})
	testInstance.Run("an app id equal to the stem", func(subTest *testing.T) {
		requestID := insertRequest(ctx, subTest, pool, "hosted", testClaims(4, ""), RequestPending, testNow)
		if _, err := store.ApproveRequest(ctx, requestID, "hosted", testNow); !errors.Is(err, ErrAppIDInRegistrarPrefix) {
			subTest.Errorf("err = %v, want ErrAppIDInRegistrarPrefix", err)
		}
	})
	testInstance.Run("a registrar identity", func(subTest *testing.T) {
		requestID := insertRequest(ctx, subTest, pool, "normalapp", registrarClaims, RequestPending, testNow)
		if _, err := store.ApproveRequest(ctx, requestID, "normalapp", testNow); !errors.Is(err, ErrWorkflowIdentityBound) {
			subTest.Errorf("err = %v, want ErrWorkflowIdentityBound", err)
		}
	})
	testInstance.Run("a normal approval acknowledges the binding", func(subTest *testing.T) {
		requestID := insertRequest(ctx, subTest, pool, "normalapp", testClaims(5, ""), RequestPending, testNow)
		binding, err := store.ApproveRequest(ctx, requestID, "normalapp", testNow)
		if err != nil || binding.AcknowledgedAt == nil || !binding.AcknowledgedAt.Equal(testNow) || binding.RegistrarID != nil {
			subTest.Fatalf("binding = %+v, err = %v, want acknowledged_at = approved_at and no registrar", binding, err)
		}
		if count := countRows(ctx, subTest, pool,
			`SELECT count(*) FROM app_bindings WHERE app_id = 'normalapp' AND acknowledged_at = approved_at`); count != 1 {
			subTest.Error("the stored acknowledged_at must equal approved_at")
		}
	})
}

func TestApproveRegistrarRequestRefusesAPrefixThatContainsAnExistingOne(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, ""), "hosted_eu_")

	requestID := insertRegistrarRequest(ctx, testInstance, pool, testClaims(2, ""), RequestPending)
	if _, err := store.ApproveRegistrarRequest(ctx, requestID, "hosted_", testNow); !errors.Is(err, ErrPrefixOverlap) {
		testInstance.Errorf("err = %v, want ErrPrefixOverlap (a new prefix that is a prefix of an existing one)", err)
	}
}

func TestFilePendingRequestRefusesRegistrarCases(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrarClaims := testClaims(1, "")
	approveTestRegistrar(ctx, testInstance, pool, store, registrarClaims, "hosted_")

	file := func(appID string, claims Claims) error {
		return store.Exchange(ctx, func(tx ExchangeTx) error {
			_, err := tx.FilePendingRequest(ctx, appID, claims, testNow)
			return err
		})
	}
	if err := file("hosted_blog", testClaims(2, "")); !errors.Is(err, ErrAppIDInRegistrarPrefix) {
		testInstance.Errorf("in-prefix app id: err = %v, want ErrAppIDInRegistrarPrefix", err)
	}
	if err := file("hosted", testClaims(2, "")); !errors.Is(err, ErrAppIDInRegistrarPrefix) {
		testInstance.Errorf("stem app id: err = %v, want ErrAppIDInRegistrarPrefix", err)
	}
	if err := file("normalapp", registrarClaims); !errors.Is(err, ErrBindingMismatch) {
		testInstance.Errorf("registrar identity: err = %v, want ErrBindingMismatch", err)
	}
	if err := file("normalapp", testClaims(2, "")); err != nil {
		testInstance.Errorf("an unrelated request must still be filed: %v", err)
	}
	if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registration_requests WHERE status = 'pending'`); count != 1 {
		testInstance.Errorf("pending requests = %d, want only the unrelated one", count)
	}
}

func TestFilePendingRegistrarRequest(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")

	fileRegistrar := func(filedClaims Claims) (int64, error) {
		var requestID int64
		err := store.Exchange(ctx, func(tx ExchangeTx) error {
			var fileErr error
			requestID, fileErr = tx.FilePendingRegistrarRequest(ctx, filedClaims, testNow)
			return fileErr
		})
		return requestID, err
	}

	first, err := fileRegistrar(claims)
	if err != nil || first == 0 {
		testInstance.Fatalf("first file = %d, %v", first, err)
	}
	refreshed := claims
	refreshed.RunID = "run-2"
	second, err := fileRegistrar(refreshed)
	if err != nil || second != first {
		testInstance.Fatalf("repeat file = %d, %v, want the same request id %d", second, err, first)
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_registration_requests WHERE kind = 'registrar' AND app_id IS NULL AND status = 'pending'`); count != 1 {
		testInstance.Fatalf("pending registrar rows = %d, want 1", count)
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_registration_requests WHERE id = $1 AND github_run_id = 'run-2'`, first); count != 1 {
		testInstance.Error("the repeat must refresh the run id")
	}

	listed, err := store.ListRequests(ctx, testNow)
	if err != nil || len(listed) != 1 || listed[0].Kind != RequestKindRegistrar || listed[0].AppID != "" {
		testInstance.Errorf("ListRequests = %+v, %v, want one registrar request with an empty app id", listed, err)
	}
	got, err := store.GetRequest(ctx, first)
	if err != nil || got.Kind != RequestKindRegistrar || got.AppID != "" {
		testInstance.Errorf("GetRequest = %+v, %v", got, err)
	}

	testInstance.Run("identity became a normal binding", func(subTest *testing.T) {
		insertTestBinding(ctx, subTest, pool, "normalapp", testClaims(2, ""))
		if _, fileErr := fileRegistrar(testClaims(2, "")); !errors.Is(fileErr, ErrBindingMismatch) {
			subTest.Errorf("err = %v, want ErrBindingMismatch", fileErr)
		}
	})
	testInstance.Run("identity became a registrar", func(subTest *testing.T) {
		approveTestRegistrar(ctx, subTest, pool, store, testClaims(3, ""), "hosted_")
		if _, fileErr := fileRegistrar(testClaims(3, "")); !errors.Is(fileErr, ErrBindingMismatch) {
			subTest.Errorf("err = %v, want ErrBindingMismatch", fileErr)
		}
	})
}

func TestFilePendingRegistrarRequestHonorsTheSharedCap(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	for index := range MaxPendingRequests {
		insertRequest(ctx, testInstance, pool, fmt.Sprintf("capapp%d", index), testClaims(int64(100+index), ""), RequestPending, testNow)
	}
	err := store.Exchange(ctx, func(tx ExchangeTx) error {
		_, fileErr := tx.FilePendingRegistrarRequest(ctx, testClaims(1, ""), testNow)
		return fileErr
	})
	if !errors.Is(err, ErrPendingCap) {
		testInstance.Errorf("err = %v, want ErrPendingCap", err)
	}
}

func TestCreateRegistrarBinding(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, "production"), "hosted_")
	insertTestBinding(ctx, testInstance, pool, "chores", testClaims(2, ""))

	mustCreateRegistrarApp(ctx, testInstance, store, registrar, "hosted_blog")

	bindings, err := store.ListBindings(ctx, testNow)
	if err != nil || len(bindings) != 2 {
		testInstance.Fatalf("ListBindings = %+v, %v", bindings, err)
	}
	var created, normal Binding
	for _, listed := range bindings {
		if listed.Binding.AppID == "hosted_blog" {
			created = listed.Binding
		} else {
			normal = listed.Binding
		}
	}
	if created.RegistrarID == nil || *created.RegistrarID != registrar.ID || created.RegistrarPrefix != "hosted_" ||
		created.AcknowledgedAt != nil || created.RepositoryID != 1 || created.Environment != "production" ||
		!created.Matches(testClaims(1, "production")) {
		testInstance.Errorf("registrar binding = %+v", created)
	}
	if normal.RegistrarID != nil || normal.RegistrarPrefix != "" || normal.AcknowledgedAt == nil {
		testInstance.Errorf("normal binding = %+v, want no registrar and an acknowledged_at", normal)
	}

	var locked Binding
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		var found bool
		var lockErr error
		locked, found, lockErr = tx.LockBinding(ctx, "hosted_blog")
		if lockErr == nil && !found {
			lockErr = errors.New("not found")
		}
		return lockErr
	}); err != nil || locked.RegistrarID == nil || locked.RegistrarPrefix != "hosted_" || locked.AcknowledgedAt != nil {
		testInstance.Errorf("LockBinding = %+v, %v, want registrar id, prefix and no acknowledged_at", locked, err)
	}

	testInstance.Run("already bound", func(subTest *testing.T) {
		if err := createRegistrarApp(ctx, store, registrar, "hosted_blog"); !errors.Is(err, ErrBindingAppeared) {
			subTest.Errorf("err = %v, want ErrBindingAppeared", err)
		}
	})
	testInstance.Run("underscore overlap with a bound app", func(subTest *testing.T) {
		if err := createRegistrarApp(ctx, store, registrar, "hosted_blog_x"); !errors.Is(err, ErrAppIDOverlap) {
			subTest.Errorf("err = %v, want ErrAppIDOverlap", err)
		}
	})
	testInstance.Run("an id outside the prefix", func(subTest *testing.T) {
		if err := createRegistrarApp(ctx, store, registrar, "elsewhere"); !errors.Is(err, ErrRegistrarOwnership) {
			subTest.Errorf("err = %v, want ErrRegistrarOwnership", err)
		}
	})
	testInstance.Run("the registrar vanished", func(subTest *testing.T) {
		ghost := registrar
		ghost.ID = registrar.ID + 1000
		if err := createRegistrarApp(ctx, store, ghost, "hosted_ghost"); !errors.Is(err, ErrRegistrarNotFound) {
			subTest.Errorf("err = %v, want ErrRegistrarNotFound", err)
		}
	})
}

func TestCreateRegistrarBindingStopsAtTheAppLimit(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, ""), "hosted_")
	for index := range RegistrarAppLimit {
		mustCreateRegistrarApp(ctx, testInstance, store, registrar, fmt.Sprintf("hosted_app%02d", index))
	}

	if err := createRegistrarApp(ctx, store, registrar, "hosted_onetoomany"); !errors.Is(err, ErrRegistrarAppLimit) {
		testInstance.Fatalf("err = %v, want ErrRegistrarAppLimit", err)
	}
	if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings WHERE registrar_id = $1`, registrar.ID); count != RegistrarAppLimit {
		testInstance.Errorf("registrar apps = %d, want %d", count, RegistrarAppLimit)
	}
}

func TestListAndDeleteRegistrars(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	empty, err := store.ListRegistrars(ctx)
	if err != nil || empty == nil || len(empty) != 0 {
		testInstance.Fatalf("ListRegistrars = %v, %v, want an empty non-nil list", empty, err)
	}

	second := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(2, ""), "zed_")
	first := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, "production"), "hosted_")
	mustCreateRegistrarApp(ctx, testInstance, store, first, "hosted_a")
	mustCreateRegistrarApp(ctx, testInstance, store, first, "hosted_b")
	issueTestKey(ctx, testInstance, store, "hosted_a", testNow)

	listed, err := store.ListRegistrars(ctx)
	if err != nil || len(listed) != 2 || listed[0].Prefix != "hosted_" || listed[1].Prefix != "zed_" {
		testInstance.Fatalf("ListRegistrars = %+v, %v, want sorted by prefix", listed, err)
	}
	if listed[0].AppCount != 2 || listed[1].AppCount != 0 || listed[0].AppLimit != RegistrarAppLimit ||
		listed[0].Environment != "production" || listed[0].ID != first.ID {
		testInstance.Errorf("listed = %+v", listed)
	}

	deleted, unbound, err := store.DeleteRegistrar(ctx, first.ID)
	if err != nil || deleted.ID != first.ID || deleted.Prefix != first.Prefix || unbound != 2 {
		testInstance.Fatalf("DeleteRegistrar = %+v, %d, %v, want the first registrar, 2, nil", deleted, unbound, err)
	}
	if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_bindings`); count != 0 {
		testInstance.Errorf("bindings = %d, want the registrar's apps cascade-deleted", count)
	}
	if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_keys`); count != 0 {
		testInstance.Errorf("keys = %d, want the apps' keys cascade-deleted", count)
	}
	_, unbound, err = store.DeleteRegistrar(ctx, first.ID)
	if !errors.Is(err, ErrRegistrarNotFound) || unbound != 0 {
		testInstance.Errorf("repeat DeleteRegistrar = %d, %v, want 0, ErrRegistrarNotFound", unbound, err)
	}
	if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM app_registrars WHERE id = $1`, second.ID); count != 1 {
		testInstance.Error("another registrar must be untouched")
	}
}

func TestAcknowledgeBinding(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, ""), "hosted_")
	mustCreateRegistrarApp(ctx, testInstance, store, registrar, "hosted_blog")
	issueTestKey(ctx, testInstance, store, "hosted_blog", testNow)

	first, err := store.AcknowledgeBinding(ctx, "hosted_blog", testNow)
	if err != nil || first.Binding.AcknowledgedAt == nil || !first.Binding.AcknowledgedAt.Equal(testNow) ||
		first.Binding.RegistrarPrefix != "hosted_" || len(first.Keys) != 1 {
		testInstance.Fatalf("AcknowledgeBinding = %+v, %v", first, err)
	}
	again, err := store.AcknowledgeBinding(ctx, "hosted_blog", testNow.Add(time.Hour))
	if err != nil || again.Binding.AcknowledgedAt == nil || !again.Binding.AcknowledgedAt.Equal(testNow) {
		testInstance.Errorf("repeat AcknowledgeBinding = %+v, %v, want the first time kept", again, err)
	}
	if _, err := store.AcknowledgeBinding(ctx, "nosuchapp", testNow); !errors.Is(err, ErrBindingNotFound) {
		testInstance.Errorf("unknown app: err = %v, want ErrBindingNotFound", err)
	}
}

func TestAcknowledgeAllBindingsChangesOnlyTheListedUnseenRegistrarRows(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, ""), "hosted_")
	for _, name := range []string{"hosted_a", "hosted_b", "hosted_c", "hosted_seen"} {
		mustCreateRegistrarApp(ctx, testInstance, store, registrar, name)
	}
	insertTestBinding(ctx, testInstance, pool, "normalapp", testClaims(2, ""))
	if _, err := store.AcknowledgeBinding(ctx, "hosted_seen", testNow.Add(-time.Hour)); err != nil {
		testInstance.Fatalf("setup acknowledge: %v", err)
	}

	changed, err := store.AcknowledgeAllBindings(ctx,
		[]string{"hosted_a", "hosted_b", "hosted_seen", "normalapp", "nosuchapp"}, testNow)
	if err != nil || changed != 2 {
		testInstance.Fatalf("AcknowledgeAllBindings = %d, %v, want 2", changed, err)
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_bindings WHERE app_id = 'hosted_c' AND acknowledged_at IS NULL`); count != 1 {
		testInstance.Error("an unlisted unseen registrar row must stay New")
	}
	if count := countRows(ctx, testInstance, pool,
		`SELECT count(*) FROM app_bindings WHERE app_id = 'hosted_seen' AND acknowledged_at = $1`, testNow.Add(-time.Hour)); count != 1 {
		testInstance.Error("an already seen row must keep its first time")
	}
	repeat, err := store.AcknowledgeAllBindings(ctx, []string{"hosted_a", "hosted_b"}, testNow.Add(time.Hour))
	if err != nil || repeat != 0 {
		testInstance.Errorf("repeat = %d, %v, want 0", repeat, err)
	}
}

func TestHasNormalBindingForIdentity(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, ""), "hosted_")
	mustCreateRegistrarApp(ctx, testInstance, store, registrar, "hosted_blog")
	insertTestBinding(ctx, testInstance, pool, "normalapp", testClaims(2, ""))

	has := func(claims Claims) bool {
		var result bool
		if err := store.Exchange(ctx, func(tx ExchangeTx) error {
			var hasErr error
			result, hasErr = tx.HasNormalBindingForIdentity(ctx, claims)
			return hasErr
		}); err != nil {
			testInstance.Fatalf("HasNormalBindingForIdentity: %v", err)
		}
		return result
	}
	if !has(testClaims(2, "")) {
		testInstance.Error("a normal binding's identity must be reported")
	}
	if has(testClaims(1, "")) {
		testInstance.Error("a registrar's identity (copied by its apps) is not a normal binding")
	}
	if has(testClaims(3, "")) {
		testInstance.Error("an unknown identity is not a normal binding")
	}
}

func TestFindRegistrarByClaims(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(1, "production"), "hosted_")

	find := func(claims Claims) *WorkflowRegistrar {
		var found *WorkflowRegistrar
		if err := store.Exchange(ctx, func(tx ExchangeTx) error {
			var findErr error
			found, findErr = tx.FindRegistrarByClaims(ctx, claims)
			return findErr
		}); err != nil {
			testInstance.Fatalf("FindRegistrarByClaims: %v", err)
		}
		return found
	}
	if found := find(testClaims(1, "production")); found == nil || found.ID != registrar.ID || found.Prefix != "hosted_" {
		testInstance.Errorf("found = %+v, want the registrar", found)
	}
	if found := find(testClaims(1, "")); found != nil {
		testInstance.Errorf("a claim with another environment matched %+v", found)
	}
	if found := find(testClaims(2, "production")); found != nil {
		testInstance.Errorf("a claim with another repository matched %+v", found)
	}
}

func TestReadRegistrarExchange(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")
	claims.JTI = "read-jti"
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, claims, "hosted_")
	mustCreateRegistrarApp(ctx, testInstance, store, registrar, "hosted_blog")
	otherRegistrar := approveTestRegistrar(ctx, testInstance, pool, store, testClaims(2, ""), "other_")
	mustCreateRegistrarApp(ctx, testInstance, store, otherRegistrar, "other_site")
	insertTestBinding(ctx, testInstance, pool, "hosted_manual", testClaims(3, ""))
	insertTestBinding(ctx, testInstance, pool, "normalapp", testClaims(4, ""))

	read := func(readClaims Claims, name string) RegistrarExchangeState {
		state, err := store.ReadRegistrarExchange(ctx, readClaims, name)
		if err != nil {
			testInstance.Fatalf("ReadRegistrarExchange: %v", err)
		}
		return state
	}

	state := read(claims, "new")
	if state.Registrar == nil || state.Registrar.ID != registrar.ID || state.NormalBinding || state.Derived != DerivedAppAbsent ||
		state.AppCount != 1 || state.Overlaps || state.JTIRecorded {
		testInstance.Errorf("new name state = %+v", state)
	}
	if state := read(claims, "blog"); state.Derived != DerivedAppOwned || state.Overlaps {
		testInstance.Errorf("owned name state = %+v", state)
	}
	if state := read(claims, "manual"); state.Derived != DerivedAppForeign {
		testInstance.Errorf("a normal app: state = %+v, want DerivedAppForeign", state)
	}
	if state := read(claims, "blog_x"); !state.Overlaps || state.Derived != DerivedAppAbsent {
		testInstance.Errorf("an underscore-extension must report an overlap: %+v", state)
	}

	normalState := read(testClaims(4, ""), "x")
	if normalState.Registrar != nil || !normalState.NormalBinding || normalState.AppCount != 0 {
		testInstance.Errorf("normal identity state = %+v", normalState)
	}
	unknown := read(testClaims(9, ""), "x")
	if unknown.Registrar != nil || unknown.NormalBinding {
		testInstance.Errorf("unknown identity state = %+v", unknown)
	}

	// The read never records the jti.
	if count := countRows(ctx, testInstance, pool, `SELECT count(*) FROM oidc_replay`); count != 0 {
		testInstance.Fatalf("oidc_replay rows = %d after reads, want 0: the read must not write", count)
	}
	if err := store.Exchange(ctx, func(tx ExchangeTx) error {
		fresh, recordErr := tx.RecordJTI(ctx, "read-jti", testNow.Add(time.Hour))
		if recordErr == nil && !fresh {
			recordErr = errors.New("jti was not fresh")
		}
		return recordErr
	}); err != nil {
		testInstance.Fatalf("record jti: %v", err)
	}
	if state := read(claims, "new"); !state.JTIRecorded {
		testInstance.Error("JTIRecorded must be true once a transaction recorded the jti")
	}
}

func TestReadRegistrarExchangeReportsTheCap(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	claims := testClaims(1, "")
	registrar := approveTestRegistrar(ctx, testInstance, pool, store, claims, "hosted_")
	for index := range RegistrarAppLimit {
		mustCreateRegistrarApp(ctx, testInstance, store, registrar, fmt.Sprintf("hosted_app%02d", index))
	}

	state, err := store.ReadRegistrarExchange(ctx, claims, "more")
	if err != nil || state.AppCount != RegistrarAppLimit {
		testInstance.Fatalf("state = %+v, %v, want AppCount %d", state, err, RegistrarAppLimit)
	}
}

func TestRoleExclusionIsEnforcedByTheStoreInBothDirections(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	testInstance.Run("a registrar identity cannot become a normal app", func(subTest *testing.T) {
		claims := testClaims(1, "")
		approveTestRegistrar(ctx, subTest, pool, store, claims, "hosted_")
		requestID := insertRequest(ctx, subTest, pool, "normalapp", claims, RequestPending, testNow)
		if _, err := store.ApproveRequest(ctx, requestID, "normalapp", testNow); !errors.Is(err, ErrWorkflowIdentityBound) {
			subTest.Errorf("err = %v, want ErrWorkflowIdentityBound", err)
		}
	})
	testInstance.Run("a normal app identity cannot become a registrar", func(subTest *testing.T) {
		claims := testClaims(2, "")
		insertTestBinding(ctx, subTest, pool, "normalapp2", claims)
		requestID := insertRegistrarRequest(ctx, subTest, pool, claims, RequestPending)
		if _, err := store.ApproveRegistrarRequest(ctx, requestID, "second_", testNow); !errors.Is(err, ErrWorkflowIdentityBound) {
			subTest.Errorf("err = %v, want ErrWorkflowIdentityBound", err)
		}
	})
}

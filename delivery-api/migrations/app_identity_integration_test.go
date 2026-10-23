//go:build integration

package migrations_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	appIdentityVersion = 8

	insertBindingSQL = `INSERT INTO app_bindings
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())`
	insertRequestSQL = `INSERT INTO app_registration_requests
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, github_run_id, status, decided_at)
VALUES ($1, 'o/r', $2, 7, $3, 'refs/heads/main', $4, '1', $5, $6)`
	insertKeySQL = `INSERT INTO app_keys (app_id, key_hash, key_hint, revoked_at, revoked_reason) VALUES ($1, $2, 'dak_abcd', $3, $4)`
)

var (
	testHash        = []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	testWorkflow    = ".github/workflows/prod-deploy.yml"
	oneHourAgo      = time.Now().Add(-time.Hour)
	productionEnv   = "production"
	emptyStringText = ""
)

func mustExec(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, statement string, arguments ...any) {
	testInstance.Helper()
	if _, err := pool.Exec(ctx, statement, arguments...); err != nil {
		testInstance.Fatalf("setup %q: %v", firstLine(statement), err)
	}
}

func firstLine(statement string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(statement), "\n")
	return line
}

// TestAppIdentityAppliesOverAnExistingSchema runs 0008 on top of a populated 0007 schema:
// it only adds tables, so nothing already stored changes and the new tables start empty.
func TestAppIdentityAppliesOverAnExistingSchema(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, appIdentityVersion-1)

	mustExec(ctx, testInstance, pool, insertDestinationSQL, "ops_ntfy", "ntfy", placeholderSealedSecrets)

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	if _, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler)); runErr != nil {
		testInstance.Fatalf("apply 0008 over a 0007 schema: %v", runErr)
	}

	for _, table := range []string{"app_bindings", "app_registration_requests", "app_keys", "oidc_replay"} {
		var rows int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&rows); err != nil {
			testInstance.Fatalf("count %s: %v", table, err)
		}
		if rows != 0 {
			testInstance.Errorf("%s has %d rows after the migration, want 0", table, rows)
		}
	}
	var destinations int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM destinations").Scan(&destinations); err != nil || destinations != 1 {
		testInstance.Errorf("destinations = %d (%v), want the 1 pre-existing row untouched", destinations, err)
	}
}

func TestAppBindingConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	// Order matters: later cases collide with earlier valid inserts on purpose.
	testCases := []struct {
		label       string
		appID       string
		repository  string
		repoID      int64
		ownerID     int64
		workflow    string
		ref         string
		environment *string
		wantState   string
	}{
		{label: "a binding with an environment inserts", appID: "urls4irl", repository: "o/r", repoID: 1, ownerID: 2, workflow: testWorkflow, ref: "refs/heads/main", environment: &productionEnv},
		{label: "a binding with no environment inserts", appID: "chores4irl", repository: "o/c", repoID: 3, ownerID: 2, workflow: testWorkflow, ref: "refs/heads/main"},
		{label: "the same app_id again is rejected", appID: "urls4irl", repository: "o/r2", repoID: 9, ownerID: 2, workflow: testWorkflow, ref: "refs/heads/main", wantState: sqlStateUniqueViolation},
		{label: "the same workflow identity for a second app is rejected", appID: "other", repository: "o/r", repoID: 1, ownerID: 2, workflow: testWorkflow, ref: "refs/heads/main", environment: &productionEnv, wantState: sqlStateUniqueViolation},
		{label: "the same workflow identity with no environment is rejected too (NULLS NOT DISTINCT)", appID: "other", repository: "o/c", repoID: 3, ownerID: 2, workflow: testWorkflow, ref: "refs/heads/main", wantState: sqlStateUniqueViolation},
		{label: "another environment of the same workflow is a different identity", appID: "other_staging", repository: "o/r", repoID: 1, ownerID: 2, workflow: testWorkflow, ref: "refs/heads/main", environment: stringPointer("staging")},
		{label: "another ref is a different identity", appID: "other_branch", repository: "o/r", repoID: 1, ownerID: 2, workflow: testWorkflow, ref: "refs/heads/release", environment: &productionEnv},
		{label: "an uppercase app_id is rejected", appID: "Upper", repository: "o/x", repoID: 20, ownerID: 2, workflow: testWorkflow, ref: "r", wantState: sqlStateCheckViolation},
		{label: "an empty app_id is rejected", appID: "", repository: "o/x", repoID: 21, ownerID: 2, workflow: testWorkflow, ref: "r", wantState: sqlStateCheckViolation},
		{label: "an empty repository is rejected", appID: "empty_repo", repository: "", repoID: 22, ownerID: 2, workflow: testWorkflow, ref: "r", wantState: sqlStateCheckViolation},
		{label: "a zero repository id is rejected", appID: "zero_repo", repository: "o/x", repoID: 0, ownerID: 2, workflow: testWorkflow, ref: "r", wantState: sqlStateCheckViolation},
		{label: "a zero owner id is rejected", appID: "zero_owner", repository: "o/x", repoID: 23, ownerID: 0, workflow: testWorkflow, ref: "r", wantState: sqlStateCheckViolation},
		{label: "an empty workflow path is rejected", appID: "empty_wf", repository: "o/x", repoID: 24, ownerID: 2, workflow: "", ref: "r", wantState: sqlStateCheckViolation},
		{label: "an empty ref is rejected", appID: "empty_ref", repository: "o/x", repoID: 25, ownerID: 2, workflow: testWorkflow, ref: "", wantState: sqlStateCheckViolation},
		{label: "an empty-string environment is rejected (absent is NULL)", appID: "empty_env", repository: "o/x", repoID: 26, ownerID: 2, workflow: testWorkflow, ref: "r", environment: &emptyStringText, wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertBindingSQL, testCase.appID, testCase.repository, testCase.repoID,
				testCase.ownerID, testCase.workflow, testCase.ref, testCase.environment)
			expectSQLState(subTest, "insert binding "+testCase.appID, err, testCase.wantState)
		})
	}
}

func TestAppRegistrationRequestConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	pending, rejected := "pending", "rejected"
	decided := &oneHourAgo

	// Order matters: later cases collide with earlier valid inserts on purpose.
	testCases := []struct {
		label       string
		appID       string
		repoID      int64
		environment *string
		status      string
		decidedAt   *time.Time
		wantState   string
	}{
		{label: "a pending request with no environment inserts", appID: "urls4irl", repoID: 1, status: pending},
		{label: "the same claims pending again are rejected (NULLS NOT DISTINCT)", appID: "urls4irl", repoID: 1, status: pending, wantState: sqlStateUniqueViolation},
		{label: "the same claims with an environment is a different pending request", appID: "urls4irl", repoID: 1, environment: &productionEnv, status: pending},
		{label: "the same claims with that environment again are rejected", appID: "urls4irl", repoID: 1, environment: &productionEnv, status: pending, wantState: sqlStateUniqueViolation},
		{label: "another app asking with the same claims is its own request", appID: "other", repoID: 1, status: pending},
		{label: "a rejected request with the same claims inserts", appID: "urls4irl", repoID: 1, status: rejected, decidedAt: decided},
		{label: "a second rejected request with the same claims inserts (only pending is unique)", appID: "urls4irl", repoID: 1, status: rejected, decidedAt: decided},
		{label: "a status outside pending and rejected is rejected", appID: "bad_status", repoID: 2, status: "approved", wantState: sqlStateCheckViolation},
		{label: "a rejected request with no decided_at is rejected", appID: "no_decided", repoID: 3, status: rejected, wantState: sqlStateCheckViolation},
		{label: "a pending request with a decided_at is rejected", appID: "early_decided", repoID: 4, status: pending, decidedAt: decided, wantState: sqlStateCheckViolation},
		{label: "an uppercase app_id is rejected", appID: "Upper", repoID: 5, status: pending, wantState: sqlStateCheckViolation},
		{label: "a zero repository id is rejected", appID: "zero_repo", repoID: 0, status: pending, wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertRequestSQL, testCase.appID, testCase.repoID, testWorkflow,
				testCase.environment, testCase.status, testCase.decidedAt)
			expectSQLState(subTest, "insert request "+testCase.appID, err, testCase.wantState)
		})
	}
}

func TestAppKeyConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	mustExec(ctx, testInstance, pool, insertBindingSQL, "urls4irl", "o/r", int64(1), int64(2), testWorkflow, "refs/heads/main", nil)

	operator, unbound := "operator", "unbound"
	revokedAt := &oneHourAgo
	shortHash := []byte("too-short")

	// Order matters: the first case claims testHash for the later duplicate case.
	testCases := []struct {
		label     string
		appID     string
		hash      []byte
		revokedAt *time.Time
		reason    *string
		wantState string
	}{
		{label: "an active key inserts", appID: "urls4irl", hash: testHash},
		{label: "a duplicate key hash is rejected", appID: "urls4irl", hash: testHash, wantState: sqlStateUniqueViolation},
		{label: "a key for an app with no binding is rejected", appID: "unbound_app", hash: []byte("fedcba9876543210fedcba9876543210"), wantState: sqlStateForeignKeyViolation},
		{label: "a hash that is not 32 bytes is rejected", appID: "urls4irl", hash: shortHash, wantState: sqlStateCheckViolation},
		{label: "a revoked key with an operator reason inserts", appID: "urls4irl", hash: []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), revokedAt: revokedAt, reason: &operator},
		{label: "a revoked key with an unbound reason inserts", appID: "urls4irl", hash: []byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), revokedAt: revokedAt, reason: &unbound},
		{label: "a revoked key with an unknown reason is rejected", appID: "urls4irl", hash: []byte("cccccccccccccccccccccccccccccccc"), revokedAt: revokedAt, reason: stringPointer("superseded"), wantState: sqlStateCheckViolation},
		{label: "a revoked key with no reason is rejected", appID: "urls4irl", hash: []byte("dddddddddddddddddddddddddddddddd"), revokedAt: revokedAt, wantState: sqlStateCheckViolation},
		{label: "a reason with no revoked_at is rejected", appID: "urls4irl", hash: []byte("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"), reason: &operator, wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertKeySQL, testCase.appID, testCase.hash, testCase.revokedAt, testCase.reason)
			expectSQLState(subTest, "insert key", err, testCase.wantState)
		})
	}
}

// TestAppKeysAreDeletedWithTheirBinding pins ON DELETE CASCADE: unbinding an app removes its
// keys, so none can authenticate afterwards.
func TestAppKeysAreDeletedWithTheirBinding(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	mustExec(ctx, testInstance, pool, insertBindingSQL, "urls4irl", "o/r", int64(1), int64(2), testWorkflow, "refs/heads/main", nil)
	mustExec(ctx, testInstance, pool, insertKeySQL, "urls4irl", testHash, nil, nil)

	mustExec(ctx, testInstance, pool, `DELETE FROM app_bindings WHERE app_id = 'urls4irl'`)

	var keys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_keys`).Scan(&keys); err != nil || keys != 0 {
		testInstance.Fatalf("keys after deleting the binding = %d (%v), want 0", keys, err)
	}
}

func TestOIDCReplayConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	testCases := []struct {
		label     string
		jti       string
		wantState string
	}{
		{label: "a first jti inserts", jti: "jti-1"},
		{label: "the same jti again is rejected", jti: "jti-1", wantState: sqlStateUniqueViolation},
		{label: "another jti inserts", jti: "jti-2"},
		{label: "an empty jti is rejected", jti: "", wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, `INSERT INTO oidc_replay (jti, expires_at) VALUES ($1, now())`, testCase.jti)
			expectSQLState(subTest, "insert jti "+testCase.jti, err, testCase.wantState)
		})
	}
}

//go:build integration

package migrations_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	appRegistrarsVersion = 13

	insertRegistrarSQL = `INSERT INTO app_registrars
  (prefix, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at)
VALUES ($1, 'o/r', $2, 7, $3, 'refs/heads/main', $4, now())`
	insertRegistrarBindingSQL = `INSERT INTO app_bindings
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at, registrar_id)
VALUES ($1, 'o/r', $2, 7, $3, 'refs/heads/main', NULL, now(), $4)`
	insertKindRequestSQL = `INSERT INTO app_registration_requests
  (kind, app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, github_run_id)
VALUES ($1, $2, 'o/r', $3, 7, $4, 'refs/heads/main', NULL, '1')`
	insertBindingWithAckSQL = `INSERT INTO app_bindings
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at, acknowledged_at)
VALUES ($1, 'o/r', $2, 7, $3, 'refs/heads/main', NULL, now(), $4)`
)

// TestAppRegistrarsAppliesOverAPopulatedSchema runs 0013 over a table that already holds a
// normal binding: it migrates, the binding is marked acknowledged at its approval time and
// has no registrar.
func TestAppRegistrarsAppliesOverAPopulatedSchema(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, appRegistrarsVersion-1)

	mustExec(ctx, testInstance, pool, `INSERT INTO app_bindings
  (app_id, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, environment, approved_at)
VALUES ('urls4irl', 'o/r', 1, 2, $1, 'refs/heads/main', NULL, now() - interval '1 day')`, testWorkflow)

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	if _, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler)); runErr != nil {
		testInstance.Fatalf("apply 0013 over a populated 0012 schema: %v", runErr)
	}

	var acknowledgedAtMatches, registrarIsNull bool
	if err := pool.QueryRow(ctx,
		`SELECT acknowledged_at = approved_at, registrar_id IS NULL FROM app_bindings WHERE app_id = 'urls4irl'`,
	).Scan(&acknowledgedAtMatches, &registrarIsNull); err != nil {
		testInstance.Fatalf("read migrated binding: %v", err)
	}
	if !acknowledgedAtMatches || !registrarIsNull {
		testInstance.Errorf("migrated binding: acknowledged_at = approved_at is %v, registrar_id NULL is %v; want both true",
			acknowledgedAtMatches, registrarIsNull)
	}
}

func TestAppRegistrarConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	// Order matters: later cases collide with earlier valid inserts on purpose.
	testCases := []struct {
		label       string
		prefix      string
		repoID      int64
		environment *string
		wantState   string
	}{
		{label: "a valid prefix inserts", prefix: "hosted_", repoID: 1},
		{label: "a duplicate prefix is rejected", prefix: "hosted_", repoID: 2, wantState: sqlStateUniqueViolation},
		{label: "a duplicate workflow identity is rejected", prefix: "other_", repoID: 1, wantState: sqlStateUniqueViolation},
		{label: "the same identity in another environment is distinct", prefix: "other_", repoID: 1, environment: &productionEnv},
		{label: "a prefix without the trailing underscore is rejected", prefix: "hosted", repoID: 3, wantState: sqlStateCheckViolation},
		{label: "an uppercase prefix is rejected", prefix: "Hosted_", repoID: 4, wantState: sqlStateCheckViolation},
		{label: "the platform_ prefix is rejected", prefix: "platform_", repoID: 5, wantState: sqlStateCheckViolation},
		{label: "a platform_ extension is rejected", prefix: "platform_eu_", repoID: 6, wantState: sqlStateCheckViolation},
		{label: "the everyone_ prefix is rejected", prefix: "everyone_", repoID: 7, wantState: sqlStateCheckViolation},
		{label: "a 33-char prefix is rejected", prefix: strings.Repeat("a", 32) + "_", repoID: 8, wantState: sqlStateCheckViolation},
		{label: "a 32-char prefix inserts", prefix: strings.Repeat("a", 31) + "_", repoID: 9},
		{label: "a prefix that is only an underscore is rejected", prefix: "_", repoID: 10, wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertRegistrarSQL, testCase.prefix, testCase.repoID, testWorkflow, testCase.environment)
			expectSQLState(subTest, "insert registrar "+testCase.prefix, err, testCase.wantState)
		})
	}
}

func TestAppBindingsAcknowledgedAndIdentityRules(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var registrarID int64
	if err := pool.QueryRow(ctx, `INSERT INTO app_registrars
  (prefix, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, approved_at)
VALUES ('hosted_', 'o/r', 1, 7, $1, 'refs/heads/main', now()) RETURNING id`, testWorkflow).Scan(&registrarID); err != nil {
		testInstance.Fatalf("seed registrar: %v", err)
	}
	now := time.Now()

	testCases := []struct {
		label     string
		statement string
		arguments []any
		wantState string
	}{
		{label: "a normal binding with acknowledged_at inserts", statement: insertBindingWithAckSQL,
			arguments: []any{"normal_one", int64(10), testWorkflow, &now}},
		{label: "a second normal binding with the same identity is rejected", statement: insertBindingWithAckSQL,
			arguments: []any{"normal_two", int64(10), testWorkflow, &now}, wantState: sqlStateUniqueViolation},
		{label: "a normal binding with no acknowledged_at is rejected", statement: insertBindingWithAckSQL,
			arguments: []any{"normal_unseen", int64(11), testWorkflow, nil}, wantState: sqlStateCheckViolation},
		{label: "a registrar binding inserts with no acknowledged_at", statement: insertRegistrarBindingSQL,
			arguments: []any{"hosted_a", int64(1), testWorkflow, registrarID}},
		{label: "a second registrar binding with the same identity inserts", statement: insertRegistrarBindingSQL,
			arguments: []any{"hosted_b", int64(1), testWorkflow, registrarID}},
		{label: "a registrar binding for a missing registrar is rejected", statement: insertRegistrarBindingSQL,
			arguments: []any{"hosted_c", int64(1), testWorkflow, registrarID + 999}, wantState: sqlStateForeignKeyViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, testCase.statement, testCase.arguments...)
			expectSQLState(subTest, testCase.label, err, testCase.wantState)
		})
	}
}

func TestDeletingARegistrarDeletesItsBindingsAndKeysOnly(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var registrarID int64
	if err := pool.QueryRow(ctx, `INSERT INTO app_registrars
  (prefix, github_repository, github_repository_id, github_owner_id, workflow_path, git_ref, approved_at)
VALUES ('hosted_', 'o/r', 1, 7, $1, 'refs/heads/main', now()) RETURNING id`, testWorkflow).Scan(&registrarID); err != nil {
		testInstance.Fatalf("seed registrar: %v", err)
	}
	mustExec(ctx, testInstance, pool, insertRegistrarBindingSQL, "hosted_a", int64(1), testWorkflow, registrarID)
	mustExec(ctx, testInstance, pool, insertKeySQL, "hosted_a", testHash, nil, nil)
	mustExec(ctx, testInstance, pool, insertBindingSQL, "urls4irl", "o/u", int64(2), int64(2), testWorkflow, "refs/heads/main", nil)
	mustExec(ctx, testInstance, pool, insertKeySQL, "urls4irl", []byte("fedcba9876543210fedcba9876543210"), nil, nil)

	mustExec(ctx, testInstance, pool, `DELETE FROM app_registrars WHERE id = $1`, registrarID)

	var bindings, keys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_bindings`).Scan(&bindings); err != nil || bindings != 1 {
		testInstance.Fatalf("bindings after deleting the registrar = %d (%v), want only the normal one", bindings, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_keys WHERE app_id = 'hosted_a'`).Scan(&keys); err != nil || keys != 0 {
		testInstance.Fatalf("registrar app keys after delete = %d (%v), want 0", keys, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app_keys WHERE app_id = 'urls4irl'`).Scan(&keys); err != nil || keys != 1 {
		testInstance.Fatalf("normal app keys after delete = %d (%v), want 1", keys, err)
	}
}

func TestAppRegistrationRequestKindConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	// Order matters: later cases collide with earlier valid inserts on purpose.
	testCases := []struct {
		label     string
		kind      string
		appID     *string
		repoID    int64
		wantState string
	}{
		{label: "an app request with an app_id inserts", kind: "app", appID: stringPointer("urls4irl"), repoID: 1},
		{label: "a registrar request with no app_id inserts", kind: "registrar", repoID: 2},
		{label: "a second pending registrar request for the same identity is rejected", kind: "registrar", repoID: 2, wantState: sqlStateUniqueViolation},
		{label: "a registrar request for another identity inserts", kind: "registrar", repoID: 3},
		{label: "a registrar request with an app_id is rejected", kind: "registrar", appID: stringPointer("urls4irl"), repoID: 4, wantState: sqlStateCheckViolation},
		{label: "an app request with no app_id is rejected", kind: "app", repoID: 5, wantState: sqlStateCheckViolation},
		{label: "an unknown kind is rejected", kind: "other", appID: stringPointer("urls4irl"), repoID: 6, wantState: sqlStateCheckViolation},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			_, err := pool.Exec(ctx, insertKindRequestSQL, testCase.kind, testCase.appID, testCase.repoID, testWorkflow)
			expectSQLState(subTest, testCase.label, err, testCase.wantState)
		})
	}

	testInstance.Run("a request inserted without kind defaults to app", func(subTest *testing.T) {
		mustExec(ctx, subTest, pool, insertRequestSQL, "default_kind", int64(50), testWorkflow, nil, "pending", nil)
		var kind string
		if err := pool.QueryRow(ctx, `SELECT kind FROM app_registration_requests WHERE app_id = 'default_kind'`).Scan(&kind); err != nil || kind != "app" {
			subTest.Fatalf("kind = %q (%v), want app", kind, err)
		}
	})
}

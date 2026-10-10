//go:build integration

package integration

// These tests exercise delivery-api against a live docker-compose stack
// (delivery-postgres + delivery-migrate + delivery-api). Prefer make
// delivery-integration-test, which resolves the stack's URL and container ids
// and passes them in via NOTIFS_DELIVERY_URL, NOTIFS_DELIVERY_CONTAINER and
// NOTIFS_DELIVERY_DB_CONTAINER. Running by hand:
//
//	docker compose --project-directory . -f docker-compose.yml up -d --build
//	go test -p 1 -tags integration ./...
//
// The base URL is overridable via NOTIFS_DELIVERY_URL for non-default port
// mappings; the containers are taken from their env vars, else looked up with
// docker compose ps -q (outside make in a worktree, export COMPOSE_PROJECT_NAME
// from .worktree.env so that lookup finds the worktree's own stack).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	defaultDeliveryURL = "http://127.0.0.1:8300"
	readyTimeout       = 30 * time.Second
	readyPollInterval  = 500 * time.Millisecond
	httpRequestTimeout = 5 * time.Second
	dockerCommandLimit = 60 * time.Second
)

// readyzClient bounds each /readyz request so a hung server cannot outlast the
// readiness budget.
var readyzClient = &http.Client{Timeout: httpRequestTimeout}

// readyzBody mirrors the /readyz response.
type readyzBody struct {
	Status                string `json:"status"`
	SchemaVersion         int    `json:"schema_version"`
	ExpectedSchemaVersion int    `json:"expected_schema_version"`
	LastMigrationFailure  *struct {
		Version  int    `json:"version"`
		Error    string `json:"error"`
		FailedAt string `json:"failed_at"`
	} `json:"last_migration_failure"`
}

func deliveryURL() string {
	if value := os.Getenv("NOTIFS_DELIVERY_URL"); value != "" {
		return value
	}
	return defaultDeliveryURL
}

// execRunner is the real commandRunner behind resolveServiceContainer. It lives
// in this tagged file because container.go is built without the integration
// tag, and an unexported symbol used only here would be flagged unused by lint.
func execRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// waitForReady blocks until /readyz returns 200 or times out.
func waitForReady(testInstance *testing.T) {
	testInstance.Helper()
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		response, getErr := readyzClient.Get(deliveryURL() + "/readyz")
		if getErr == nil {
			closeErr := response.Body.Close()
			if closeErr != nil {
				testInstance.Fatalf("closing /readyz body: %v", closeErr)
			}
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(readyPollInterval)
	}
	testInstance.Fatalf("delivery-api /readyz never became ready at %s", deliveryURL())
}

// fetchReadyz GETs /readyz, requires a 200 and decodes the body.
func fetchReadyz(testInstance *testing.T) readyzBody {
	testInstance.Helper()
	response, getErr := readyzClient.Get(deliveryURL() + "/readyz")
	if getErr != nil {
		testInstance.Fatalf("GET /readyz: %v", getErr)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			testInstance.Errorf("closing /readyz body: %v", closeErr)
		}
	}()
	if response.StatusCode != http.StatusOK {
		testInstance.Fatalf("/readyz status = %d, want 200", response.StatusCode)
	}
	var body readyzBody
	if decodeErr := json.NewDecoder(response.Body).Decode(&body); decodeErr != nil {
		testInstance.Fatalf("decoding /readyz body: %v", decodeErr)
	}
	return body
}

// psql runs one query in the delivery-postgres container and returns its
// trimmed output (tuples only, unaligned).
func psql(testInstance *testing.T, query string) string {
	testInstance.Helper()
	containerID, resolveErr := resolveServiceContainer(execRunner, "NOTIFS_DELIVERY_DB_CONTAINER", "delivery-postgres")
	if resolveErr != nil {
		testInstance.Fatalf("resolve delivery-postgres container: %v", resolveErr)
	}
	commandCtx, cancelCommand := context.WithTimeout(context.Background(), dockerCommandLimit)
	defer cancelCommand()
	output, runErr := exec.CommandContext(
		commandCtx, "docker", "exec", containerID, "psql", "-U", "delivery", "-d", "delivery", "-tAc", query,
	).CombinedOutput()
	if runErr != nil {
		testInstance.Fatalf("psql %q failed: %v\noutput: %s", query, runErr, output)
	}
	return strings.TrimSpace(string(output))
}

func TestReadyzReportsCurrentSchema(testInstance *testing.T) {
	waitForReady(testInstance)

	body := fetchReadyz(testInstance)

	if body.Status != "ok" {
		testInstance.Fatalf("status = %q, want ok", body.Status)
	}
	if body.SchemaVersion != body.ExpectedSchemaVersion {
		testInstance.Fatalf("schema_version = %d, want it to equal expected_schema_version = %d",
			body.SchemaVersion, body.ExpectedSchemaVersion)
	}
	if want := embeddedSchemaVersion(testInstance); body.ExpectedSchemaVersion != want {
		testInstance.Fatalf("expected_schema_version = %d, want the highest embedded migration version %d",
			body.ExpectedSchemaVersion, want)
	}
}

func TestMigrationsRecorded(testInstance *testing.T) {
	waitForReady(testInstance)

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load embedded migrations: %v", loadErr)
	}
	for _, migration := range loaded {
		query := fmt.Sprintf("select count(*) from schema_migrations where version = %d", migration.Version)
		if got := psql(testInstance, query); got != "1" {
			testInstance.Fatalf("schema_migrations rows for version %d = %q, want 1", migration.Version, got)
		}
	}
}

// embeddedSchemaVersion returns the highest version among the embedded
// migrations, which is the schema version the binary expects.
func embeddedSchemaVersion(testInstance *testing.T) int {
	testInstance.Helper()
	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load embedded migrations: %v", loadErr)
	}
	return migrate.ExpectedVersion(loaded)
}

func TestMigrateIsIdempotent(testInstance *testing.T) {
	waitForReady(testInstance)
	const countQuery = "select count(*) from schema_migrations"
	before := psql(testInstance, countQuery)

	composeArgs, argsErr := composeBaseArgs()
	if argsErr != nil {
		testInstance.Fatalf("compose args: %v", argsErr)
	}
	// --no-deps: the stack is already up; do not let compose reconcile (and
	// possibly recreate) delivery-postgres just to rerun the migrate job.
	composeArgs = append(composeArgs, "run", "--rm", "--no-deps", "delivery-migrate")
	commandCtx, cancelCommand := context.WithTimeout(context.Background(), dockerCommandLimit)
	defer cancelCommand()
	output, runErr := exec.CommandContext(commandCtx, "docker", composeArgs...).CombinedOutput()
	if runErr != nil {
		testInstance.Fatalf("docker compose run delivery-migrate failed: %v\noutput: %s", runErr, output)
	}

	if after := psql(testInstance, countQuery); after != before {
		testInstance.Fatalf("schema_migrations count changed from %s to %s after rerunning migrate", before, after)
	}
}

func TestFailedMigrationIsReported(testInstance *testing.T) {
	waitForReady(testInstance)
	// Relies on /readyz reporting the newest qualifying row (ORDER BY id DESC).
	// The pre-delete below self-heals rows left by a run killed before Cleanup.
	const cleanup = "delete from schema_migration_failures where error in ('integration-probe','stale')"
	psql(testInstance, cleanup)
	testInstance.Cleanup(func() { psql(testInstance, cleanup) })

	// Simulates a newer image's migrate failing while this binary keeps serving.
	psql(testInstance, "insert into schema_migration_failures (version, error) values (2, 'integration-probe')")
	assertFailureProbe(testInstance)

	// A stale failure (version not above the applied schema version) is
	// excluded by the filter, so it must not displace the newer failure.
	psql(testInstance, "insert into schema_migration_failures (version, error) values (1, 'stale')")
	assertFailureProbe(testInstance)
}

// assertFailureProbe checks /readyz still serves 200 and reports the version 2
// integration-probe failure as the last migration failure.
func assertFailureProbe(testInstance *testing.T) {
	testInstance.Helper()
	body := fetchReadyz(testInstance)
	if body.LastMigrationFailure == nil {
		testInstance.Fatalf("last_migration_failure = null, want version 2 integration-probe")
	}
	if body.LastMigrationFailure.Version != 2 || body.LastMigrationFailure.Error != "integration-probe" {
		testInstance.Fatalf("last_migration_failure = %+v, want version 2 / integration-probe", *body.LastMigrationFailure)
	}
}

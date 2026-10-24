//go:build integration

package migrations_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/migrations"
)

const (
	probesVersion = 9

	insertPushCheckSQL = `INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at)
VALUES ('billing', $1, 60, 60, now()) RETURNING id`

	insertProbeSQL = `INSERT INTO heartbeat_check_probes
(check_id, url, expected_statuses, body_match, timeout_seconds, interval_seconds, consecutive_failures, next_probe_at,
 last_result_at, last_result_status, last_result_reason, last_result_detail, down_detail)
VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8::timestamptz, $9, $10, $11, $12)`
)

// probeRow is one probe-table insert; validProbeRow returns an accepted row and each
// case overrides the field under test.
type probeRow struct {
	url              string
	expectedStatuses []*int32
	bodyMatch        *string
	timeoutSeconds   int
	intervalSeconds  int
	failures         int
	lastResultAt     *string
	lastResultStatus *int
	lastResultReason *string
	lastResultDetail *string
	downDetail       *string
}

func validProbeRow() probeRow {
	return probeRow{
		url:              "https://example.com/health",
		expectedStatuses: statusList(200),
		timeoutSeconds:   5,
		intervalSeconds:  60,
	}
}

func statusList(statuses ...int32) []*int32 {
	list := make([]*int32, 0, len(statuses))
	for index := range statuses {
		list = append(list, &statuses[index])
	}
	return list
}

// TestProbesApplyOverExistingSchema loads the 0008 schema with push checks (one down
// with reason missed), applies 0009, and checks the rows survive untouched.
func TestProbesApplyOverExistingSchema(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPoolUpTo(ctx, testInstance, probesVersion-1)

	if _, err := pool.Exec(ctx,
		`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at)
VALUES ('billing', 'nightly', 3600, 3600, now())`); err != nil {
		testInstance.Fatalf("insert new check in the 0008 schema: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, state, anchor_at, down_since, down_reason)
VALUES ('billing', 'late', 3600, 3600, 'down', now(), now(), 'missed')`); err != nil {
		testInstance.Fatalf("insert down check in the 0008 schema: %v", err)
	}

	loaded, loadErr := migrate.Load(migrations.FS)
	if loadErr != nil {
		testInstance.Fatalf("load migrations: %v", loadErr)
	}
	applied, runErr := migrate.Run(ctx, migrate.NewPGStore(pool), loaded, slog.New(slog.DiscardHandler))
	if runErr != nil {
		testInstance.Fatalf("apply 0009 over existing rows: %v", runErr)
	}
	if want := migrate.ExpectedVersion(loaded) - (probesVersion - 1); applied != want {
		testInstance.Fatalf("applied %d migrations over the 0008 schema, want %d", applied, want)
	}

	var rows, downMissed int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE state = 'down' AND down_reason = 'missed') FROM heartbeat_checks`,
	).Scan(&rows, &downMissed); err != nil {
		testInstance.Fatalf("count checks: %v", err)
	}
	if rows != 2 || downMissed != 1 {
		testInstance.Fatalf("checks after upgrade: %d rows, %d down/missed; want 2 and 1", rows, downMissed)
	}
	var probeRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM heartbeat_check_probes`).Scan(&probeRows); err != nil {
		testInstance.Fatalf("count probes: %v", err)
	}
	if probeRows != 0 {
		testInstance.Fatalf("probe rows after upgrade = %d, want 0", probeRows)
	}
}

func TestHeartbeatProbeConstraints(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	someTime := "2026-10-24T12:00:00Z"
	longURL := "https://example.com/" + strings.Repeat("a", 2048-len("https://example.com/"))
	override := func(change func(*probeRow)) probeRow {
		row := validProbeRow()
		change(&row)
		return row
	}
	oneEleven := make([]int32, 0, 11)
	for status := int32(200); status < 211; status++ {
		oneEleven = append(oneEleven, status)
	}
	tenStatuses := statusList(oneEleven[:10]...)

	testCases := []struct {
		label     string
		row       probeRow
		wantState string
	}{
		{label: "minimal row accepted", row: validProbeRow()},
		{label: "url at 2048 bytes accepted", row: override(func(r *probeRow) { r.url = longURL })},
		{label: "url over 2048 bytes rejected", row: override(func(r *probeRow) { r.url = longURL + "a" }), wantState: sqlStateCheckViolation},
		{label: "http url rejected", row: override(func(r *probeRow) { r.url = "http://example.com/" }), wantState: sqlStateCheckViolation},
		{label: "upper-case scheme rejected", row: override(func(r *probeRow) { r.url = "HTTPS://example.com/" }), wantState: sqlStateCheckViolation},
		{label: "ten statuses accepted", row: override(func(r *probeRow) { r.expectedStatuses = tenStatuses })},
		{label: "status bounds 100 and 599 accepted", row: override(func(r *probeRow) { r.expectedStatuses = statusList(100, 599) })},
		{label: "eleven statuses rejected", row: override(func(r *probeRow) { r.expectedStatuses = statusList(oneEleven...) }), wantState: sqlStateCheckViolation},
		{label: "zero statuses rejected", row: override(func(r *probeRow) { r.expectedStatuses = statusList() }), wantState: sqlStateCheckViolation},
		{label: "status 99 rejected", row: override(func(r *probeRow) { r.expectedStatuses = statusList(99) }), wantState: sqlStateCheckViolation},
		{label: "status 600 rejected", row: override(func(r *probeRow) { r.expectedStatuses = statusList(200, 600) }), wantState: sqlStateCheckViolation},
		{label: "NULL status element rejected", row: override(func(r *probeRow) { r.expectedStatuses = []*int32{nil, int32Pointer(200)} }), wantState: sqlStateCheckViolation},
		{label: "body match of one byte accepted", row: override(func(r *probeRow) { r.bodyMatch = strPtr("a") })},
		{label: "body match of 256 bytes accepted", row: override(func(r *probeRow) { r.bodyMatch = strPtr(strings.Repeat("a", 256)) })},
		{label: "empty body match rejected", row: override(func(r *probeRow) { r.bodyMatch = strPtr("") }), wantState: sqlStateCheckViolation},
		{label: "body match over 256 bytes rejected", row: override(func(r *probeRow) { r.bodyMatch = strPtr(strings.Repeat("a", 257)) }), wantState: sqlStateCheckViolation},
		{label: "timeout 1 accepted", row: override(func(r *probeRow) { r.timeoutSeconds = 1 })},
		{label: "timeout 8 accepted", row: override(func(r *probeRow) { r.timeoutSeconds = 8 })},
		{label: "timeout 0 rejected", row: override(func(r *probeRow) { r.timeoutSeconds = 0 }), wantState: sqlStateCheckViolation},
		{label: "timeout 9 rejected", row: override(func(r *probeRow) { r.timeoutSeconds = 9 }), wantState: sqlStateCheckViolation},
		{label: "interval 86400 accepted", row: override(func(r *probeRow) { r.intervalSeconds = 86400 })},
		{label: "interval 59 rejected", row: override(func(r *probeRow) { r.intervalSeconds = 59 }), wantState: sqlStateCheckViolation},
		{label: "interval 86401 rejected", row: override(func(r *probeRow) { r.intervalSeconds = 86401 }), wantState: sqlStateCheckViolation},
		{label: "timeout equal to a small interval rejected", row: override(func(r *probeRow) { r.timeoutSeconds = 8; r.intervalSeconds = 8 }), wantState: sqlStateCheckViolation},
		{label: "zero failures accepted", row: override(func(r *probeRow) { r.failures = 0 })},
		{label: "negative failures rejected", row: override(func(r *probeRow) { r.failures = -1 }), wantState: sqlStateCheckViolation},
		{label: "full last result accepted", row: override(func(r *probeRow) {
			r.lastResultAt, r.lastResultStatus, r.lastResultReason, r.lastResultDetail = &someTime, intPointer(503), strPtr("http_status"), strPtr("bad gateway")
		})},
		{label: "last result with only a time accepted", row: override(func(r *probeRow) { r.lastResultAt = &someTime })},
		{label: "last result status 99 rejected", row: override(func(r *probeRow) { r.lastResultAt, r.lastResultStatus = &someTime, intPointer(99) }), wantState: sqlStateCheckViolation},
		{label: "last result status 600 rejected", row: override(func(r *probeRow) { r.lastResultAt, r.lastResultStatus = &someTime, intPointer(600) }), wantState: sqlStateCheckViolation},
		{label: "last result reason missed rejected", row: override(func(r *probeRow) { r.lastResultAt, r.lastResultReason = &someTime, strPtr("missed") }), wantState: sqlStateCheckViolation},
		{label: "last result reason fail rejected", row: override(func(r *probeRow) { r.lastResultAt, r.lastResultReason = &someTime, strPtr("fail") }), wantState: sqlStateCheckViolation},
		{label: "last result detail of 256 bytes accepted", row: override(func(r *probeRow) { r.lastResultAt, r.lastResultDetail = &someTime, strPtr(strings.Repeat("a", 256)) })},
		{label: "last result detail over 256 bytes rejected", row: override(func(r *probeRow) { r.lastResultAt, r.lastResultDetail = &someTime, strPtr(strings.Repeat("a", 257)) }), wantState: sqlStateCheckViolation},
		{label: "status without a result time rejected", row: override(func(r *probeRow) { r.lastResultStatus = intPointer(200) }), wantState: sqlStateCheckViolation},
		{label: "reason without a result time rejected", row: override(func(r *probeRow) { r.lastResultReason = strPtr("timeout") }), wantState: sqlStateCheckViolation},
		{label: "detail without a result time rejected", row: override(func(r *probeRow) { r.lastResultDetail = strPtr("x") }), wantState: sqlStateCheckViolation},
		{label: "down detail of 256 bytes accepted", row: override(func(r *probeRow) { r.downDetail = strPtr(strings.Repeat("a", 256)) })},
		{label: "down detail over 256 bytes rejected", row: override(func(r *probeRow) { r.downDetail = strPtr(strings.Repeat("a", 257)) }), wantState: sqlStateCheckViolation},
	}
	for index, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			var checkID int64
			if err := pool.QueryRow(ctx, insertPushCheckSQL, fmt.Sprintf("probe_case_%d", index)).Scan(&checkID); err != nil {
				subTest.Fatalf("insert parent check: %v", err)
			}
			_, err := pool.Exec(ctx, insertProbeSQL, checkID, testCase.row.url, testCase.row.expectedStatuses,
				testCase.row.bodyMatch, testCase.row.timeoutSeconds, testCase.row.intervalSeconds, testCase.row.failures,
				testCase.row.lastResultAt, testCase.row.lastResultStatus, testCase.row.lastResultReason,
				testCase.row.lastResultDetail, testCase.row.downDetail)
			expectSQLState(subTest, "insert probe "+testCase.label, err, testCase.wantState)
		})
	}
}

func TestHeartbeatProbeRelations(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	var probeCheckID, pushCheckID int64
	if err := pool.QueryRow(ctx, insertPushCheckSQL, "probe_parent").Scan(&probeCheckID); err != nil {
		testInstance.Fatalf("insert probe parent: %v", err)
	}
	if err := pool.QueryRow(ctx, insertPushCheckSQL, "push_only").Scan(&pushCheckID); err != nil {
		testInstance.Fatalf("insert push check: %v", err)
	}
	row := validProbeRow()
	insert := func(checkID int64) error {
		_, err := pool.Exec(ctx, insertProbeSQL, checkID, row.url, row.expectedStatuses, row.bodyMatch,
			row.timeoutSeconds, row.intervalSeconds, row.failures, row.lastResultAt, row.lastResultStatus,
			row.lastResultReason, row.lastResultDetail, row.downDetail)
		return err
	}
	if err := insert(probeCheckID); err != nil {
		testInstance.Fatalf("insert probe row: %v", err)
	}

	testInstance.Run("second probe row for the same check rejected", func(subTest *testing.T) {
		expectSQLState(subTest, "duplicate probe row", insert(probeCheckID), sqlStateUniqueViolation)
	})
	testInstance.Run("probe row for an absent check rejected", func(subTest *testing.T) {
		expectSQLState(subTest, "probe row for absent check", insert(probeCheckID+pushCheckID+1000), sqlStateForeignKeyViolation)
	})
	testInstance.Run("deleting the check cascades its probe row", func(subTest *testing.T) {
		if _, err := pool.Exec(ctx, `DELETE FROM heartbeat_checks WHERE id = $1`, probeCheckID); err != nil {
			subTest.Fatalf("delete probe check: %v", err)
		}
		var probeRows int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM heartbeat_check_probes WHERE check_id = $1`, probeCheckID).Scan(&probeRows); err != nil {
			subTest.Fatalf("count probe rows: %v", err)
		}
		if probeRows != 0 {
			subTest.Errorf("probe rows after deleting the check = %d, want 0", probeRows)
		}
	})
	testInstance.Run("deleting a push check with no probe row works", func(subTest *testing.T) {
		if _, err := pool.Exec(ctx, `DELETE FROM heartbeat_checks WHERE id = $1`, pushCheckID); err != nil {
			subTest.Fatalf("delete push check: %v", err)
		}
	})
}

func TestHeartbeatProbeSchemaShape(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)

	testInstance.Run("only bytea column is sealed", func(subTest *testing.T) {
		rows, err := pool.Query(ctx,
			`SELECT column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'heartbeat_check_probes' AND data_type = 'bytea'`)
		if err != nil {
			subTest.Fatalf("query bytea columns: %v", err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if scanErr := rows.Scan(&name); scanErr != nil {
				subTest.Fatalf("scan column name: %v", scanErr)
			}
			names = append(names, name)
		}
		if rows.Err() != nil {
			subTest.Fatalf("read bytea columns: %v", rows.Err())
		}
		if len(names) != 1 || !strings.HasPrefix(names[0], "sealed_") {
			subTest.Fatalf("bytea columns = %v, want exactly one with prefix sealed_", names)
		}
	})
	testInstance.Run("next probe index exists", func(subTest *testing.T) {
		var definition string
		err := pool.QueryRow(ctx,
			`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'heartbeat_check_probes_next_probe_at_idx'`,
		).Scan(&definition)
		if err != nil {
			subTest.Fatalf("read index definition: %v", err)
		}
		if !strings.Contains(definition, "(next_probe_at)") {
			subTest.Errorf("index definition %q does not cover (next_probe_at)", definition)
		}
	})
}

func int32Pointer(value int32) *int32 { return &value }

func intPointer(value int) *int { return &value }

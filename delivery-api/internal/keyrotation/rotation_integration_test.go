//go:build integration

package keyrotation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	oldKeyHex   = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	newKeyHex   = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
	otherKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	rotationTestBudget = 60 * time.Second
)

// sealedRowRef names one seeded sealed row.
type sealedRowRef struct {
	table string
	key   string
}

type rotationFixture struct {
	pool     *pgxpool.Pool
	oldSeal  *seal.Sealer
	newSeal  *seal.Sealer // primary only
	dualSeal *seal.Sealer // primary new, previous old
	// plaintexts maps "table/key" to the seeded plaintext.
	plaintexts map[string]string
	probeIDs   []int64
	nullProbe  int64
}

func mustKey(testInstance *testing.T, keyHex string) []byte {
	testInstance.Helper()
	key, decodeErr := hex.DecodeString(keyHex)
	if decodeErr != nil {
		testInstance.Fatalf("decode key: %v", decodeErr)
	}
	return key
}

func newFixture(ctx context.Context, testInstance *testing.T) *rotationFixture {
	testInstance.Helper()
	oldSeal, oldErr := seal.New(mustKey(testInstance, oldKeyHex))
	newSeal, newErr := seal.New(mustKey(testInstance, newKeyHex))
	dualSeal, dualErr := seal.NewWithPrevious(mustKey(testInstance, newKeyHex), mustKey(testInstance, oldKeyHex))
	if joinErr := errors.Join(oldErr, newErr, dualErr); joinErr != nil {
		testInstance.Fatalf("build sealers: %v", joinErr)
	}
	fixture := &rotationFixture{
		pool:       pgtest.IsolatedPool(ctx, testInstance),
		oldSeal:    oldSeal,
		newSeal:    newSeal,
		dualSeal:   dualSeal,
		plaintexts: map[string]string{},
	}
	for _, name := range []string{"dest_a", "dest_b", "dest_c"} {
		fixture.seedDestination(ctx, testInstance, name, fixture.oldSeal)
	}
	for _, appID := range []string{"app_a", "app_b"} {
		fixture.seedPublisher(ctx, testInstance, appID, fixture.oldSeal)
	}
	for index := 0; index < 2; index++ {
		fixture.probeIDs = append(fixture.probeIDs, fixture.seedProbe(ctx, testInstance, fmt.Sprintf("probe%d", index), fixture.oldSeal))
	}
	fixture.nullProbe = fixture.seedProbe(ctx, testInstance, "probe_null", nil)
	return fixture
}

func (fixture *rotationFixture) exec(ctx context.Context, testInstance *testing.T, sql string, args ...any) {
	testInstance.Helper()
	if _, execErr := fixture.pool.Exec(ctx, sql, args...); execErr != nil {
		testInstance.Fatalf("exec %q: %v", sql, execErr)
	}
}

func (fixture *rotationFixture) seedDestination(ctx context.Context, testInstance *testing.T, name string, sealer *seal.Sealer) {
	testInstance.Helper()
	plaintext := "plain-dest-secret-" + name
	blob, sealErr := sealer.Seal([]byte(plaintext), registry.DestinationSecretsAAD(name))
	if sealErr != nil {
		testInstance.Fatalf("seal: %v", sealErr)
	}
	fixture.plaintexts["destinations/"+name] = plaintext
	fixture.exec(ctx, testInstance, `INSERT INTO destinations (name, type, sealed_secrets) VALUES ($1, 'webhook', $2)`, name, blob)
}

func (fixture *rotationFixture) seedPublisher(ctx context.Context, testInstance *testing.T, appID string, sealer *seal.Sealer) {
	testInstance.Helper()
	plaintext := "plain-publisher-token-" + appID
	blob, sealErr := sealer.Seal([]byte(plaintext), publisher.TokenAAD(appID))
	if sealErr != nil {
		testInstance.Fatalf("seal: %v", sealErr)
	}
	fixture.plaintexts["app_publishers/"+appID] = plaintext
	fixture.exec(ctx, testInstance,
		`INSERT INTO app_publishers (app_id, publisher_user_id, topic_pattern, sealed_token) VALUES ($1, 'user', 'topic-*', $2)`,
		appID, blob)
}

// seedProbe inserts a push check with a probe row; a nil sealer seeds NULL credentials.
func (fixture *rotationFixture) seedProbe(ctx context.Context, testInstance *testing.T, name string, sealer *seal.Sealer) int64 {
	testInstance.Helper()
	var checkID int64
	if insertErr := fixture.pool.QueryRow(ctx,
		`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at)
VALUES ('billing', $1, 60, 60, now()) RETURNING id`, name).Scan(&checkID); insertErr != nil {
		testInstance.Fatalf("insert check: %v", insertErr)
	}
	var blob []byte
	if sealer != nil {
		plaintext := "plain-probe-header-" + name
		var sealErr error
		blob, sealErr = sealer.Seal([]byte(plaintext), heartbeat.ProbeAccessAAD(checkID))
		if sealErr != nil {
			testInstance.Fatalf("seal: %v", sealErr)
		}
		fixture.plaintexts["heartbeat_check_probes/"+strconv.FormatInt(checkID, 10)] = plaintext
	}
	fixture.exec(ctx, testInstance,
		`INSERT INTO heartbeat_check_probes (check_id, url, expected_statuses, timeout_seconds, interval_seconds, next_probe_at, sealed_access_headers)
VALUES ($1, 'https://example.com/health', '{200}', 5, 60, now(), $2)`, checkID, blob)
	return checkID
}

func tableByName(testInstance *testing.T, name string) SealedTable {
	testInstance.Helper()
	for _, table := range Registry() {
		if table.Table == name {
			return table
		}
	}
	testInstance.Fatalf("no registry table %q", name)
	return SealedTable{}
}

func (fixture *rotationFixture) sealedValue(ctx context.Context, testInstance *testing.T, table SealedTable, rowKey string) []byte {
	testInstance.Helper()
	var blob []byte
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s::text = $1`, table.SealedColumn, table.Table, table.KeyColumn)
	if readErr := fixture.pool.QueryRow(ctx, query, rowKey).Scan(&blob); readErr != nil {
		testInstance.Fatalf("read %s/%s: %v", table.Table, rowKey, readErr)
	}
	return blob
}

func (fixture *rotationFixture) setSealedValue(ctx context.Context, testInstance *testing.T, table SealedTable, rowKey string, blob []byte) {
	testInstance.Helper()
	statement := fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE %s::text = $2`, table.Table, table.SealedColumn, table.KeyColumn)
	fixture.exec(ctx, testInstance, statement, blob, rowKey)
}

// allSealedRows returns every seeded sealed row's table and key.
func (fixture *rotationFixture) allSealedRows() []sealedRowRef {
	var rows []sealedRowRef
	for name := range fixture.plaintexts {
		table, key, _ := strings.Cut(name, "/")
		rows = append(rows, sealedRowRef{table: table, key: key})
	}
	return rows
}

// assertOpensUnder asserts the row opens under sealer with the wanted key.
func (fixture *rotationFixture) assertOpensUnder(ctx context.Context, testInstance *testing.T, sealer *seal.Sealer, tableName, rowKey string, want seal.KeyUsed) {
	testInstance.Helper()
	table := tableByName(testInstance, tableName)
	aad, aadErr := table.AAD(rowKey)
	if aadErr != nil {
		testInstance.Fatalf("aad: %v", aadErr)
	}
	plaintext, used, openErr := sealer.OpenReportingKey(fixture.sealedValue(ctx, testInstance, table, rowKey), aad)
	if openErr != nil {
		testInstance.Fatalf("open %s/%s: %v", tableName, rowKey, openErr)
	}
	if used != want {
		testInstance.Fatalf("%s/%s opened with key %v, want %v", tableName, rowKey, used, want)
	}
	if string(plaintext) != fixture.plaintexts[tableName+"/"+rowKey] {
		testInstance.Fatalf("%s/%s plaintext changed by rotation", tableName, rowKey)
	}
}

// assertSealedAtRest mirrors assertSecretsSealedAtRest with a pool: the known
// plaintext must be absent from the column's hex and escape encodings and from
// the row text.
func (fixture *rotationFixture) assertSealedAtRest(ctx context.Context, testInstance *testing.T, tableName, rowKey string) {
	testInstance.Helper()
	table := tableByName(testInstance, tableName)
	plaintext := fixture.plaintexts[tableName+"/"+rowKey]
	var hexForm, escapeForm, rowText string
	query := fmt.Sprintf(`SELECT encode(%[1]s, 'hex'), encode(%[1]s, 'escape'), %[2]s::text FROM %[2]s WHERE %[3]s::text = $1`,
		table.SealedColumn, table.Table, table.KeyColumn)
	if readErr := fixture.pool.QueryRow(ctx, query, rowKey).Scan(&hexForm, &escapeForm, &rowText); readErr != nil {
		testInstance.Fatalf("at-rest read %s/%s: %v", tableName, rowKey, readErr)
	}
	for label, form := range map[string]string{"hex": hexForm, "escape": escapeForm, "row text": rowText} {
		if strings.Contains(form, plaintext) || strings.Contains(form, hex.EncodeToString([]byte(plaintext))) {
			testInstance.Fatalf("%s/%s: plaintext visible in %s", tableName, rowKey, label)
		}
	}
}

func (fixture *rotationFixture) assertAllUnder(ctx context.Context, testInstance *testing.T, sealer *seal.Sealer, want seal.KeyUsed) {
	testInstance.Helper()
	for _, row := range fixture.allSealedRows() {
		fixture.assertOpensUnder(ctx, testInstance, sealer, row.table, row.key, want)
	}
}

func (fixture *rotationFixture) snapshotBlobs(ctx context.Context, testInstance *testing.T) map[string][]byte {
	testInstance.Helper()
	blobs := map[string][]byte{}
	for _, row := range fixture.allSealedRows() {
		blobs[row.table+"/"+row.key] = fixture.sealedValue(ctx, testInstance, tableByName(testInstance, row.table), row.key)
	}
	return blobs
}

func (fixture *rotationFixture) probeState(ctx context.Context, testInstance *testing.T) string {
	testInstance.Helper()
	var state string
	if scanErr := fixture.pool.QueryRow(ctx,
		`SELECT string_agg(check_id::text || ':' || config_version::text || ':' || next_probe_at::text, ',' ORDER BY check_id)
FROM heartbeat_check_probes`).Scan(&state); scanErr != nil {
		testInstance.Fatalf("probe state: %v", scanErr)
	}
	return state
}

func (fixture *rotationFixture) destinationUpdatedAt(ctx context.Context, testInstance *testing.T) string {
	testInstance.Helper()
	var state string
	if scanErr := fixture.pool.QueryRow(ctx,
		`SELECT (SELECT string_agg(updated_at::text, ',' ORDER BY name) FROM destinations) || '|' ||
(SELECT string_agg(updated_at::text, ',' ORDER BY app_id) FROM app_publishers)`).Scan(&state); scanErr != nil {
		testInstance.Fatalf("updated_at state: %v", scanErr)
	}
	return state
}

const sealedRowCount = 7 // 3 destinations + 2 publishers + 2 sealed probes

func TestRotateHappyPath(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)
	probeBefore := fixture.probeState(ctx, testInstance)
	updatedBefore := fixture.destinationUpdatedAt(ctx, testInstance)

	report, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal})
	if rotateErr != nil {
		testInstance.Fatalf("Rotate: %v", rotateErr)
	}

	fixture.assertAllUnder(ctx, testInstance, fixture.newSeal, seal.KeyPrimary)
	if nullBlob := fixture.sealedValue(ctx, testInstance, tableByName(testInstance, "heartbeat_check_probes"), strconv.FormatInt(fixture.nullProbe, 10)); nullBlob != nil {
		testInstance.Fatalf("NULL probe row now holds a value")
	}
	probeCounts := report.Tables["heartbeat_check_probes"]
	if probeCounts.SkippedNull != 1 || probeCounts.Rotated != 2 || probeCounts.Scanned != 3 {
		testInstance.Fatalf("probe counts = %+v, want scanned 3 rotated 2 skipped_null 1", probeCounts)
	}
	if report.Tables["destinations"].Rotated != 3 || report.Tables["app_publishers"].Rotated != 2 {
		testInstance.Fatalf("counts = %+v", report.Tables)
	}
	if got := fixture.probeState(ctx, testInstance); got != probeBefore {
		testInstance.Fatalf("probe config_version/next_probe_at changed: %q -> %q", probeBefore, got)
	}
	if got := fixture.destinationUpdatedAt(ctx, testInstance); got != updatedBefore {
		testInstance.Fatalf("updated_at changed: %q -> %q", updatedBefore, got)
	}
	for _, row := range fixture.allSealedRows() {
		fixture.assertSealedAtRest(ctx, testInstance, row.table, row.key)
	}
	if _, verifyErr := Verify(ctx, fixture.pool, fixture.dualSeal, nil); verifyErr != nil {
		testInstance.Fatalf("Verify: %v", verifyErr)
	}
}

func failOnNth(failingCall int) func(context.Context, SealedTable, string) error {
	calls := 0
	hook := func(_ context.Context, _ SealedTable, _ string) error {
		calls++
		if calls == failingCall {
			return errors.New("injected failure")
		}
		return nil
	}
	return hook
}

func TestRotateMidRunFailure(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	hook := failOnNth(3)
	if _, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, BeforeUpdate: hook}); rotateErr == nil {
		testInstance.Fatal("Rotate returned nil, want the injected failure")
	} else if errors.Is(rotateErr, ErrIncomplete) {
		testInstance.Fatalf("hook failure must not be ErrIncomplete: %v", rotateErr)
	}

	// destinations are walked in name order and the third update fails, so
	// dest_a and dest_b are on the new key and every other row on the old one.
	for _, row := range fixture.allSealedRows() {
		want := seal.KeyPrevious
		if row.table == "destinations" && (row.key == "dest_a" || row.key == "dest_b") {
			want = seal.KeyPrimary
		}
		fixture.assertOpensUnder(ctx, testInstance, fixture.dualSeal, row.table, row.key, want)
	}
}

func TestRotateRerunCompletesRemainder(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	hook := failOnNth(3)
	if _, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, BeforeUpdate: hook}); rotateErr == nil {
		testInstance.Fatal("Rotate returned nil, want the injected failure")
	}
	// destinations are walked in name order: dest_a and dest_b were rotated.
	doneBefore := map[string][]byte{}
	for _, name := range []string{"dest_a", "dest_b"} {
		fixture.assertOpensUnder(ctx, testInstance, fixture.dualSeal, "destinations", name, seal.KeyPrimary)
		doneBefore[name] = fixture.sealedValue(ctx, testInstance, tableByName(testInstance, "destinations"), name)
	}

	report, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal})
	if rotateErr != nil {
		testInstance.Fatalf("rerun: %v", rotateErr)
	}
	already, rotated := 0, 0
	for _, counts := range report.Tables {
		already += counts.AlreadyCurrent
		rotated += counts.Rotated
	}
	if already != 2 || rotated != sealedRowCount-2 {
		testInstance.Fatalf("rerun already_current=%d rotated=%d, want 2 and %d", already, rotated, sealedRowCount-2)
	}
	for name, blob := range doneBefore {
		if after := fixture.sealedValue(ctx, testInstance, tableByName(testInstance, "destinations"), name); !bytes.Equal(blob, after) {
			testInstance.Fatalf("rerun rewrote already-rotated %s", name)
		}
	}
	fixture.assertAllUnder(ctx, testInstance, fixture.newSeal, seal.KeyPrimary)
	if _, verifyErr := Verify(ctx, fixture.pool, fixture.dualSeal, nil); verifyErr != nil {
		testInstance.Fatalf("Verify: %v", verifyErr)
	}
}

func TestRotateConcurrentChange(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	destinations := tableByName(testInstance, "destinations")
	var concurrentBlob []byte
	hook := func(hookCtx context.Context, table SealedTable, rowKey string) error {
		if table.Table != "destinations" || rowKey != "dest_b" {
			return nil
		}
		var sealErr error
		concurrentBlob, sealErr = fixture.oldSeal.Seal([]byte("rewritten-concurrently"), registry.DestinationSecretsAAD("dest_b"))
		if sealErr != nil {
			return sealErr
		}
		_, execErr := fixture.pool.Exec(hookCtx, `UPDATE destinations SET sealed_secrets = $1 WHERE name = 'dest_b'`, concurrentBlob)
		return execErr
	}

	report, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, BeforeUpdate: hook})
	if !errors.Is(rotateErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want ErrIncomplete", rotateErr)
	}
	if got := fixture.sealedValue(ctx, testInstance, destinations, "dest_b"); !bytes.Equal(got, concurrentBlob) {
		testInstance.Fatal("concurrently written value was overwritten")
	}
	want := RowIssue{Table: "destinations", RowKey: "dest_b", Reason: ReasonChangedConcurrently}
	if len(report.Issues) != 1 || report.Issues[0] != want {
		testInstance.Fatalf("issues = %+v, want [%+v]", report.Issues, want)
	}
}

// assertBadRowReported puts a bad blob into dest_b, rotates, and asserts the row
// is reported with wantReason, left untouched, and every other row rotated.
func assertBadRowReported(testInstance *testing.T, makeBlob func(testInstance *testing.T, fixture *rotationFixture) []byte, wantReason Reason) {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)
	destinations := tableByName(testInstance, "destinations")
	bad := makeBlob(testInstance, fixture)
	fixture.setSealedValue(ctx, testInstance, destinations, "dest_b", bad)

	report, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal})
	if !errors.Is(rotateErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want ErrIncomplete", rotateErr)
	}
	want := RowIssue{Table: "destinations", RowKey: "dest_b", Reason: wantReason}
	if len(report.Issues) != 1 || report.Issues[0] != want {
		testInstance.Fatalf("issues = %+v, want [%+v]", report.Issues, want)
	}
	if got := fixture.sealedValue(ctx, testInstance, destinations, "dest_b"); !bytes.Equal(got, bad) {
		testInstance.Fatal("bad row was modified")
	}
	delete(fixture.plaintexts, "destinations/dest_b")
	fixture.assertAllUnder(ctx, testInstance, fixture.newSeal, seal.KeyPrimary)
}

func sealOrFatal(testInstance *testing.T, sealer *seal.Sealer, plaintext string, aad []byte) []byte {
	testInstance.Helper()
	blob, sealErr := sealer.Seal([]byte(plaintext), aad)
	if sealErr != nil {
		testInstance.Fatalf("seal: %v", sealErr)
	}
	return blob
}

func TestRotateWrongOldKey(testInstance *testing.T) {
	assertBadRowReported(testInstance, func(testInstance *testing.T, _ *rotationFixture) []byte {
		other, otherErr := seal.New(mustKey(testInstance, otherKeyHex))
		if otherErr != nil {
			testInstance.Fatalf("seal.New: %v", otherErr)
		}
		return sealOrFatal(testInstance, other, "x", registry.DestinationSecretsAAD("dest_b"))
	}, ReasonDecryptFailed)
}

func TestRotateWrongAAD(testInstance *testing.T) {
	assertBadRowReported(testInstance, func(testInstance *testing.T, fixture *rotationFixture) []byte {
		return sealOrFatal(testInstance, fixture.oldSeal, "x", registry.DestinationSecretsAAD("dest_c"))
	}, ReasonDecryptFailed)
}

func TestRotateMalformedBlob(testInstance *testing.T) {
	assertBadRowReported(testInstance, func(*testing.T, *rotationFixture) []byte { return []byte{0x01, 0x02, 0x03} }, ReasonMalformed)
}

func TestRotateUnsupportedVersion(testInstance *testing.T) {
	assertBadRowReported(testInstance, func(testInstance *testing.T, fixture *rotationFixture) []byte {
		blob := sealOrFatal(testInstance, fixture.oldSeal, "x", registry.DestinationSecretsAAD("dest_b"))
		blob[0] = 0x02
		return blob
	}, ReasonUnsupportedVersion)
}

func TestVerifyReportsOrphans(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	// Rotate everything, then put one row back under the old key and break another.
	if _, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal}); rotateErr != nil {
		testInstance.Fatalf("Rotate: %v", rotateErr)
	}
	if _, verifyErr := Verify(ctx, fixture.pool, fixture.dualSeal, nil); verifyErr != nil {
		testInstance.Fatalf("Verify on a fully rotated database: %v", verifyErr)
	}
	destinations := tableByName(testInstance, "destinations")
	oldBlob, sealErr := fixture.oldSeal.Seal([]byte("old"), registry.DestinationSecretsAAD("dest_a"))
	if sealErr != nil {
		testInstance.Fatalf("seal: %v", sealErr)
	}
	fixture.setSealedValue(ctx, testInstance, destinations, "dest_a", oldBlob)
	fixture.setSealedValue(ctx, testInstance, destinations, "dest_c", []byte{0x01})
	before := fixture.snapshotBlobs(ctx, testInstance)

	report, verifyErr := Verify(ctx, fixture.pool, fixture.dualSeal, nil)
	if !errors.Is(verifyErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want ErrIncomplete", verifyErr)
	}
	want := []RowIssue{
		{Table: "destinations", RowKey: "dest_a", Reason: ReasonDecryptFailed},
		{Table: "destinations", RowKey: "dest_c", Reason: ReasonMalformed},
	}
	if len(report.Issues) != 2 || report.Issues[0] != want[0] || report.Issues[1] != want[1] {
		testInstance.Fatalf("issues = %+v, want %+v", report.Issues, want)
	}
	for key, blob := range fixture.snapshotBlobs(ctx, testInstance) {
		if !bytes.Equal(blob, before[key]) {
			testInstance.Fatalf("Verify modified %s", key)
		}
	}
}

func TestRotateRequiresPreviousKey(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)
	hookCalled := false
	hook := func(context.Context, SealedTable, string) error { hookCalled = true; return nil }

	for name, sealer := range map[string]*seal.Sealer{"single key": fixture.newSeal, "nil": nil} {
		before := fixture.snapshotBlobs(ctx, testInstance)
		report, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: sealer, BeforeUpdate: hook})
		if rotateErr == nil {
			testInstance.Fatalf("%s: Rotate returned nil, want a refusal", name)
		}
		if errors.Is(rotateErr, ErrIncomplete) {
			testInstance.Fatalf("%s: refusal must not be ErrIncomplete: %v", name, rotateErr)
		}
		if len(report.Tables) != 0 {
			testInstance.Fatalf("%s: refused run read tables: %+v", name, report.Tables)
		}
		for key, blob := range fixture.snapshotBlobs(ctx, testInstance) {
			if !bytes.Equal(blob, before[key]) {
				testInstance.Fatalf("%s: refused run modified %s", name, key)
			}
		}
	}
	if hookCalled {
		testInstance.Fatal("refused run reached the update hook")
	}
}

// missingTable is a SealedTable whose relation does not exist, so reading it
// fails with a database error.
func missingTable() SealedTable {
	return SealedTable{
		Table:        "no_such_sealed_table",
		KeyColumn:    "id",
		SealedColumn: "sealed",
		AAD:          func(rowKey string) ([]byte, error) { return []byte(rowKey), nil },
	}
}

func TestRotateDatabaseErrorAborts(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	_, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, Logger: logger, Tables: []SealedTable{missingTable()}})
	if rotateErr == nil || errors.Is(rotateErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want a plain database error", rotateErr)
	}
	if !strings.Contains(logs.String(), "key rotation aborted") || !strings.Contains(logs.String(), "outcome=aborted") {
		testInstance.Fatalf("expected abort log lines: %s", logs.String())
	}
}

func TestRotateCanceledContextAborts(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	_, rotateErr := Rotate(canceledCtx, fixture.pool, Options{Sealer: fixture.dualSeal, Logger: logger})
	if rotateErr == nil || errors.Is(rotateErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want a plain context error", rotateErr)
	}
	if !strings.Contains(logs.String(), "key rotation aborted") {
		testInstance.Fatalf("expected an abort log line: %s", logs.String())
	}
}

func TestVerifyDatabaseErrorAborts(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	_, verifyErr := verifyTables(ctx, fixture.pool, fixture.dualSeal, logger, []SealedTable{missingTable()})
	if verifyErr == nil || errors.Is(verifyErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want a plain database error", verifyErr)
	}
	if !strings.Contains(logs.String(), "key rotation aborted") {
		testInstance.Fatalf("expected an abort log line: %s", logs.String())
	}

	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, canceledErr := Verify(canceledCtx, fixture.pool, fixture.dualSeal, nil); canceledErr == nil || errors.Is(canceledErr, ErrIncomplete) {
		testInstance.Fatalf("canceled Verify err = %v, want a plain context error", canceledErr)
	}
}

// destinationsRejectingAAD is the destinations table with an AAD function that
// refuses dest_b.
func destinationsRejectingAAD(testInstance *testing.T) SealedTable {
	testInstance.Helper()
	table := tableByName(testInstance, "destinations")
	realAAD := table.AAD
	table.AAD = func(rowKey string) ([]byte, error) {
		if rowKey == "dest_b" {
			return nil, errors.New("rejected row key")
		}
		return realAAD(rowKey)
	}
	return table
}

func TestRotateAADErrorRecordsIssueAndContinues(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	report, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, Tables: []SealedTable{destinationsRejectingAAD(testInstance)}})
	if !errors.Is(rotateErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want ErrIncomplete", rotateErr)
	}
	want := RowIssue{Table: "destinations", RowKey: "dest_b", Reason: ReasonMalformed}
	if len(report.Issues) != 1 || report.Issues[0] != want {
		testInstance.Fatalf("issues = %+v, want [%+v]", report.Issues, want)
	}
	counts := report.Tables["destinations"]
	if counts.Rotated != 2 || counts.Failed != 1 || counts.Scanned != 3 {
		testInstance.Fatalf("counts = %+v, want scanned 3 rotated 2 failed 1", counts)
	}
	fixture.assertOpensUnder(ctx, testInstance, fixture.newSeal, "destinations", "dest_a", seal.KeyPrimary)
	fixture.assertOpensUnder(ctx, testInstance, fixture.newSeal, "destinations", "dest_c", seal.KeyPrimary)
	fixture.assertOpensUnder(ctx, testInstance, fixture.oldSeal, "destinations", "dest_b", seal.KeyPrimary)
}

func TestVerifyAADErrorRecordsIssueAndContinues(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	if _, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal}); rotateErr != nil {
		testInstance.Fatalf("Rotate: %v", rotateErr)
	}
	report, verifyErr := verifyTables(ctx, fixture.pool, fixture.dualSeal, nil, []SealedTable{destinationsRejectingAAD(testInstance)})
	if !errors.Is(verifyErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want ErrIncomplete", verifyErr)
	}
	want := RowIssue{Table: "destinations", RowKey: "dest_b", Reason: ReasonMalformed}
	if len(report.Issues) != 1 || report.Issues[0] != want {
		testInstance.Fatalf("issues = %+v, want [%+v]", report.Issues, want)
	}
	if counts := report.Tables["destinations"]; counts.AlreadyCurrent != 2 || counts.Failed != 1 {
		testInstance.Fatalf("counts = %+v, want already_current 2 failed 1", counts)
	}
}

// byteaEscape renders blob in Postgres's bytea escape text form.
func byteaEscape(blob []byte) string {
	var builder strings.Builder
	for _, value := range blob {
		switch {
		case value == '\\':
			builder.WriteString(`\\`)
		case value < 0x20 || value > 0x7e:
			fmt.Fprintf(&builder, `\%03o`, value)
		default:
			builder.WriteByte(value)
		}
	}
	return builder.String()
}

func TestRotateRedaction(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, testInstance)

	destinations := tableByName(testInstance, "destinations")
	other, otherErr := seal.New(mustKey(testInstance, otherKeyHex))
	if otherErr != nil {
		testInstance.Fatalf("seal.New: %v", otherErr)
	}
	wrongKeyBlob := sealOrFatal(testInstance, other, "plain-wrong-key-secret", registry.DestinationSecretsAAD("dest_b"))
	fixture.setSealedValue(ctx, testInstance, destinations, "dest_b", wrongKeyBlob)
	fixture.setSealedValue(ctx, testInstance, destinations, "dest_c", []byte("plain-malformed-secret"))
	secrets := []string{"plain-wrong-key-secret", "plain-malformed-secret"}
	for _, plaintext := range fixture.plaintexts {
		secrets = append(secrets, plaintext)
	}
	blobsBefore := fixture.snapshotBlobs(ctx, testInstance)
	blobsBefore["wrong"] = wrongKeyBlob
	blobsBefore["malformed"] = []byte("plain-malformed-secret")

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	report, rotateErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, Logger: logger})
	if !errors.Is(rotateErr, ErrIncomplete) {
		testInstance.Fatalf("err = %v, want ErrIncomplete", rotateErr)
	}
	// The plain ASCII value is not a sealed blob, so it is reported as malformed.
	wantIssues := []RowIssue{
		{Table: "destinations", RowKey: "dest_b", Reason: ReasonDecryptFailed},
		{Table: "destinations", RowKey: "dest_c", Reason: ReasonMalformed},
	}
	if len(report.Issues) != 2 || report.Issues[0] != wantIssues[0] || report.Issues[1] != wantIssues[1] {
		testInstance.Fatalf("issues = %+v, want %+v", report.Issues, wantIssues)
	}
	for _, row := range fixture.allSealedRows() {
		if row.table == "destinations" && (row.key == "dest_b" || row.key == "dest_c") {
			continue
		}
		fixture.assertOpensUnder(ctx, testInstance, fixture.newSeal, row.table, row.key, seal.KeyPrimary)
	}

	// A second run aborted by a failing hook adds a wrapped error to scan.
	// Put dest_a back under the old key so the run has a row to rotate.
	reseeded := sealOrFatal(testInstance, fixture.oldSeal, "plain-reseeded-secret", registry.DestinationSecretsAAD("dest_a"))
	fixture.setSealedValue(ctx, testInstance, destinations, "dest_a", reseeded)
	secrets = append(secrets, "plain-reseeded-secret")
	blobsBefore["reseeded"] = reseeded
	hook := failOnNth(1)
	_, abortErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, Logger: logger, BeforeUpdate: hook})
	if abortErr == nil || errors.Is(abortErr, ErrIncomplete) {
		testInstance.Fatalf("abort run err = %v, want the injected failure", abortErr)
	}
	if !strings.Contains(logs.String(), "key rotation aborted") {
		testInstance.Fatalf("expected an abort log line: %s", logs.String())
	}

	forbidden := append([]string{}, secrets...)
	for _, keyHex := range []string{oldKeyHex, newKeyHex, otherKeyHex} {
		forbidden = append(forbidden, keyHex, string(mustKey(testInstance, keyHex)))
	}
	blobsAfter := fixture.snapshotBlobs(ctx, testInstance)
	for _, blobs := range []map[string][]byte{blobsBefore, blobsAfter} {
		for _, blob := range blobs {
			forbidden = append(forbidden, hex.EncodeToString(blob), base64.StdEncoding.EncodeToString(blob),
				base64.RawStdEncoding.EncodeToString(blob), byteaEscape(blob), `\x`+hex.EncodeToString(blob))
		}
	}
	output := logs.String() + "\n" + rotateErr.Error() + "\n" + abortErr.Error()
	for _, needle := range forbidden {
		if needle != "" && strings.Contains(output, needle) {
			testInstance.Fatalf("logs or error leak %d-byte sensitive value", len(needle))
		}
	}
	if !strings.Contains(logs.String(), "row_key=dest_b") {
		testInstance.Fatalf("expected a per-row warning in logs: %s", logs.String())
	}
}

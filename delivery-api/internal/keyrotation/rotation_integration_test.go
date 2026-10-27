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

func mustKey(t *testing.T, keyHex string) []byte {
	t.Helper()
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	return key
}

func newFixture(ctx context.Context, t *testing.T) *rotationFixture {
	t.Helper()
	oldSeal, oldErr := seal.New(mustKey(t, oldKeyHex))
	newSeal, newErr := seal.New(mustKey(t, newKeyHex))
	dualSeal, dualErr := seal.NewWithPrevious(mustKey(t, newKeyHex), mustKey(t, oldKeyHex))
	if err := errors.Join(oldErr, newErr, dualErr); err != nil {
		t.Fatalf("build sealers: %v", err)
	}
	fixture := &rotationFixture{
		pool:       pgtest.IsolatedPool(ctx, t),
		oldSeal:    oldSeal,
		newSeal:    newSeal,
		dualSeal:   dualSeal,
		plaintexts: map[string]string{},
	}
	for _, name := range []string{"dest_a", "dest_b", "dest_c"} {
		fixture.seedDestination(ctx, t, name, fixture.oldSeal)
	}
	for _, appID := range []string{"app_a", "app_b"} {
		fixture.seedPublisher(ctx, t, appID, fixture.oldSeal)
	}
	for index := 0; index < 2; index++ {
		fixture.probeIDs = append(fixture.probeIDs, fixture.seedProbe(ctx, t, fmt.Sprintf("probe%d", index), fixture.oldSeal))
	}
	fixture.nullProbe = fixture.seedProbe(ctx, t, "probe_null", nil)
	return fixture
}

func (fixture *rotationFixture) exec(ctx context.Context, t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := fixture.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (fixture *rotationFixture) seedDestination(ctx context.Context, t *testing.T, name string, sealer *seal.Sealer) {
	t.Helper()
	plaintext := "plain-dest-secret-" + name
	blob, err := sealer.Seal([]byte(plaintext), registry.DestinationSecretsAAD(name))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	fixture.plaintexts["destinations/"+name] = plaintext
	fixture.exec(ctx, t, `INSERT INTO destinations (name, type, sealed_secrets) VALUES ($1, 'webhook', $2)`, name, blob)
}

func (fixture *rotationFixture) seedPublisher(ctx context.Context, t *testing.T, appID string, sealer *seal.Sealer) {
	t.Helper()
	plaintext := "plain-publisher-token-" + appID
	blob, err := sealer.Seal([]byte(plaintext), publisher.TokenAAD(appID))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	fixture.plaintexts["app_publishers/"+appID] = plaintext
	fixture.exec(ctx, t,
		`INSERT INTO app_publishers (app_id, publisher_user_id, topic_pattern, sealed_token) VALUES ($1, 'user', 'topic-*', $2)`,
		appID, blob)
}

// seedProbe inserts a push check with a probe row; a nil sealer seeds NULL credentials.
func (fixture *rotationFixture) seedProbe(ctx context.Context, t *testing.T, name string, sealer *seal.Sealer) int64 {
	t.Helper()
	var checkID int64
	if err := fixture.pool.QueryRow(ctx,
		`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at)
VALUES ('billing', $1, 60, 60, now()) RETURNING id`, name).Scan(&checkID); err != nil {
		t.Fatalf("insert check: %v", err)
	}
	var blob []byte
	if sealer != nil {
		plaintext := "plain-probe-header-" + name
		var sealErr error
		blob, sealErr = sealer.Seal([]byte(plaintext), heartbeat.ProbeAccessAAD(checkID))
		if sealErr != nil {
			t.Fatalf("seal: %v", sealErr)
		}
		fixture.plaintexts["heartbeat_check_probes/"+strconv.FormatInt(checkID, 10)] = plaintext
	}
	fixture.exec(ctx, t,
		`INSERT INTO heartbeat_check_probes (check_id, url, expected_statuses, timeout_seconds, interval_seconds, next_probe_at, sealed_access_headers)
VALUES ($1, 'https://example.com/health', '{200}', 5, 60, now(), $2)`, checkID, blob)
	return checkID
}

func tableByName(t *testing.T, name string) SealedTable {
	t.Helper()
	for _, table := range Registry() {
		if table.Table == name {
			return table
		}
	}
	t.Fatalf("no registry table %q", name)
	return SealedTable{}
}

func (fixture *rotationFixture) sealedValue(ctx context.Context, t *testing.T, table SealedTable, rowKey string) []byte {
	t.Helper()
	var blob []byte
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s::text = $1`, table.SealedColumn, table.Table, table.KeyColumn)
	if err := fixture.pool.QueryRow(ctx, query, rowKey).Scan(&blob); err != nil {
		t.Fatalf("read %s/%s: %v", table.Table, rowKey, err)
	}
	return blob
}

func (fixture *rotationFixture) setSealedValue(ctx context.Context, t *testing.T, table SealedTable, rowKey string, blob []byte) {
	t.Helper()
	statement := fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE %s::text = $2`, table.Table, table.SealedColumn, table.KeyColumn)
	fixture.exec(ctx, t, statement, blob, rowKey)
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
func (fixture *rotationFixture) assertOpensUnder(ctx context.Context, t *testing.T, sealer *seal.Sealer, tableName, rowKey string, want seal.KeyUsed) {
	t.Helper()
	table := tableByName(t, tableName)
	aad, aadErr := table.AAD(rowKey)
	if aadErr != nil {
		t.Fatalf("aad: %v", aadErr)
	}
	plaintext, used, openErr := sealer.OpenReportingKey(fixture.sealedValue(ctx, t, table, rowKey), aad)
	if openErr != nil {
		t.Fatalf("open %s/%s: %v", tableName, rowKey, openErr)
	}
	if used != want {
		t.Fatalf("%s/%s opened with key %v, want %v", tableName, rowKey, used, want)
	}
	if string(plaintext) != fixture.plaintexts[tableName+"/"+rowKey] {
		t.Fatalf("%s/%s plaintext changed by rotation", tableName, rowKey)
	}
}

// assertSealedAtRest mirrors assertSecretsSealedAtRest with a pool: the known
// plaintext must be absent from the column's hex and escape encodings and from
// the row text.
func (fixture *rotationFixture) assertSealedAtRest(ctx context.Context, t *testing.T, tableName, rowKey string) {
	t.Helper()
	table := tableByName(t, tableName)
	plaintext := fixture.plaintexts[tableName+"/"+rowKey]
	var hexForm, escapeForm, rowText string
	query := fmt.Sprintf(`SELECT encode(%[1]s, 'hex'), encode(%[1]s, 'escape'), %[2]s::text FROM %[2]s WHERE %[3]s::text = $1`,
		table.SealedColumn, table.Table, table.KeyColumn)
	if err := fixture.pool.QueryRow(ctx, query, rowKey).Scan(&hexForm, &escapeForm, &rowText); err != nil {
		t.Fatalf("at-rest read %s/%s: %v", tableName, rowKey, err)
	}
	for label, form := range map[string]string{"hex": hexForm, "escape": escapeForm, "row text": rowText} {
		if strings.Contains(form, plaintext) || strings.Contains(form, hex.EncodeToString([]byte(plaintext))) {
			t.Fatalf("%s/%s: plaintext visible in %s", tableName, rowKey, label)
		}
	}
}

func (fixture *rotationFixture) assertAllUnder(ctx context.Context, t *testing.T, sealer *seal.Sealer, want seal.KeyUsed) {
	t.Helper()
	for _, row := range fixture.allSealedRows() {
		fixture.assertOpensUnder(ctx, t, sealer, row.table, row.key, want)
	}
}

func (fixture *rotationFixture) snapshotBlobs(ctx context.Context, t *testing.T) map[string][]byte {
	t.Helper()
	blobs := map[string][]byte{}
	for _, row := range fixture.allSealedRows() {
		blobs[row.table+"/"+row.key] = fixture.sealedValue(ctx, t, tableByName(t, row.table), row.key)
	}
	return blobs
}

func (fixture *rotationFixture) probeState(ctx context.Context, t *testing.T) string {
	t.Helper()
	var state string
	if err := fixture.pool.QueryRow(ctx,
		`SELECT string_agg(check_id::text || ':' || config_version::text || ':' || next_probe_at::text, ',' ORDER BY check_id)
FROM heartbeat_check_probes`).Scan(&state); err != nil {
		t.Fatalf("probe state: %v", err)
	}
	return state
}

func (fixture *rotationFixture) destinationUpdatedAt(ctx context.Context, t *testing.T) string {
	t.Helper()
	var state string
	if err := fixture.pool.QueryRow(ctx,
		`SELECT (SELECT string_agg(updated_at::text, ',' ORDER BY name) FROM destinations) || '|' ||
(SELECT string_agg(updated_at::text, ',' ORDER BY app_id) FROM app_publishers)`).Scan(&state); err != nil {
		t.Fatalf("updated_at state: %v", err)
	}
	return state
}

const sealedRowCount = 7 // 3 destinations + 2 publishers + 2 sealed probes

func TestRotateHappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)
	probeBefore := fixture.probeState(ctx, t)
	updatedBefore := fixture.destinationUpdatedAt(ctx, t)

	report, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	fixture.assertAllUnder(ctx, t, fixture.newSeal, seal.KeyPrimary)
	if nullBlob := fixture.sealedValue(ctx, t, tableByName(t, "heartbeat_check_probes"), strconv.FormatInt(fixture.nullProbe, 10)); nullBlob != nil {
		t.Fatalf("NULL probe row now holds a value")
	}
	probeCounts := report.Tables["heartbeat_check_probes"]
	if probeCounts.SkippedNull != 1 || probeCounts.Rotated != 2 || probeCounts.Scanned != 3 {
		t.Fatalf("probe counts = %+v, want scanned 3 rotated 2 skipped_null 1", probeCounts)
	}
	if report.Tables["destinations"].Rotated != 3 || report.Tables["app_publishers"].Rotated != 2 {
		t.Fatalf("counts = %+v", report.Tables)
	}
	if got := fixture.probeState(ctx, t); got != probeBefore {
		t.Fatalf("probe config_version/next_probe_at changed: %q -> %q", probeBefore, got)
	}
	if got := fixture.destinationUpdatedAt(ctx, t); got != updatedBefore {
		t.Fatalf("updated_at changed: %q -> %q", updatedBefore, got)
	}
	for _, row := range fixture.allSealedRows() {
		fixture.assertSealedAtRest(ctx, t, row.table, row.key)
	}
	if _, verifyErr := Verify(ctx, fixture.pool, fixture.dualSeal, nil); verifyErr != nil {
		t.Fatalf("Verify: %v", verifyErr)
	}
}

func failOnNth(failingCall int) (func(context.Context, SealedTable, string) error, *int) {
	calls := 0
	hook := func(_ context.Context, _ SealedTable, _ string) error {
		calls++
		if calls == failingCall {
			return errors.New("injected failure")
		}
		return nil
	}
	return hook, &calls
}

func TestRotateMidRunFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)

	hook, _ := failOnNth(3)
	if _, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, BeforeUpdate: hook}); err == nil {
		t.Fatal("Rotate returned nil, want the injected failure")
	} else if errors.Is(err, ErrIncomplete) {
		t.Fatalf("hook failure must not be ErrIncomplete: %v", err)
	}

	// destinations are walked in name order and the third update fails, so
	// dest_a and dest_b are on the new key and every other row on the old one.
	for _, row := range fixture.allSealedRows() {
		want := seal.KeyPrevious
		if row.table == "destinations" && (row.key == "dest_a" || row.key == "dest_b") {
			want = seal.KeyPrimary
		}
		fixture.assertOpensUnder(ctx, t, fixture.dualSeal, row.table, row.key, want)
	}
}

func TestRotateRerunCompletesRemainder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)

	hook, _ := failOnNth(3)
	if _, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, BeforeUpdate: hook}); err == nil {
		t.Fatal("Rotate returned nil, want the injected failure")
	}
	// destinations are walked in name order: dest_a and dest_b were rotated.
	doneBefore := map[string][]byte{}
	for _, name := range []string{"dest_a", "dest_b"} {
		fixture.assertOpensUnder(ctx, t, fixture.dualSeal, "destinations", name, seal.KeyPrimary)
		doneBefore[name] = fixture.sealedValue(ctx, t, tableByName(t, "destinations"), name)
	}

	report, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal})
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	already, rotated := 0, 0
	for _, counts := range report.Tables {
		already += counts.AlreadyCurrent
		rotated += counts.Rotated
	}
	if already != 2 || rotated != sealedRowCount-2 {
		t.Fatalf("rerun already_current=%d rotated=%d, want 2 and %d", already, rotated, sealedRowCount-2)
	}
	for name, blob := range doneBefore {
		if after := fixture.sealedValue(ctx, t, tableByName(t, "destinations"), name); !bytes.Equal(blob, after) {
			t.Fatalf("rerun rewrote already-rotated %s", name)
		}
	}
	fixture.assertAllUnder(ctx, t, fixture.newSeal, seal.KeyPrimary)
	if _, verifyErr := Verify(ctx, fixture.pool, fixture.dualSeal, nil); verifyErr != nil {
		t.Fatalf("Verify: %v", verifyErr)
	}
}

func TestRotateConcurrentChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)

	destinations := tableByName(t, "destinations")
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

	report, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, BeforeUpdate: hook})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err = %v, want ErrIncomplete", err)
	}
	if got := fixture.sealedValue(ctx, t, destinations, "dest_b"); !bytes.Equal(got, concurrentBlob) {
		t.Fatal("concurrently written value was overwritten")
	}
	want := RowIssue{Table: "destinations", RowKey: "dest_b", Reason: ReasonChangedConcurrently}
	if len(report.Issues) != 1 || report.Issues[0] != want {
		t.Fatalf("issues = %+v, want [%+v]", report.Issues, want)
	}
}

// assertBadRowReported puts a bad blob into dest_b, rotates, and asserts the row
// is reported with wantReason, left untouched, and every other row rotated.
func assertBadRowReported(t *testing.T, makeBlob func(t *testing.T, fixture *rotationFixture) []byte, wantReason Reason) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)
	destinations := tableByName(t, "destinations")
	bad := makeBlob(t, fixture)
	fixture.setSealedValue(ctx, t, destinations, "dest_b", bad)

	report, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err = %v, want ErrIncomplete", err)
	}
	want := RowIssue{Table: "destinations", RowKey: "dest_b", Reason: wantReason}
	if len(report.Issues) != 1 || report.Issues[0] != want {
		t.Fatalf("issues = %+v, want [%+v]", report.Issues, want)
	}
	if got := fixture.sealedValue(ctx, t, destinations, "dest_b"); !bytes.Equal(got, bad) {
		t.Fatal("bad row was modified")
	}
	delete(fixture.plaintexts, "destinations/dest_b")
	fixture.assertAllUnder(ctx, t, fixture.newSeal, seal.KeyPrimary)
}

func sealOrFatal(t *testing.T, sealer *seal.Sealer, plaintext string, aad []byte) []byte {
	t.Helper()
	blob, err := sealer.Seal([]byte(plaintext), aad)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return blob
}

func TestRotateWrongOldKey(t *testing.T) {
	assertBadRowReported(t, func(t *testing.T, _ *rotationFixture) []byte {
		other, err := seal.New(mustKey(t, otherKeyHex))
		if err != nil {
			t.Fatalf("seal.New: %v", err)
		}
		return sealOrFatal(t, other, "x", registry.DestinationSecretsAAD("dest_b"))
	}, ReasonDecryptFailed)
}

func TestRotateWrongAAD(t *testing.T) {
	assertBadRowReported(t, func(t *testing.T, fixture *rotationFixture) []byte {
		return sealOrFatal(t, fixture.oldSeal, "x", registry.DestinationSecretsAAD("dest_c"))
	}, ReasonDecryptFailed)
}

func TestRotateMalformedBlob(t *testing.T) {
	assertBadRowReported(t, func(*testing.T, *rotationFixture) []byte { return []byte{0x01, 0x02, 0x03} }, ReasonMalformed)
}

func TestRotateUnsupportedVersion(t *testing.T) {
	assertBadRowReported(t, func(t *testing.T, fixture *rotationFixture) []byte {
		blob := sealOrFatal(t, fixture.oldSeal, "x", registry.DestinationSecretsAAD("dest_b"))
		blob[0] = 0x02
		return blob
	}, ReasonUnsupportedVersion)
}

func TestVerifyReportsOrphans(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)

	// Rotate everything, then put one row back under the old key and break another.
	if _, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal}); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := Verify(ctx, fixture.pool, fixture.dualSeal, nil); err != nil {
		t.Fatalf("Verify on a fully rotated database: %v", err)
	}
	destinations := tableByName(t, "destinations")
	oldBlob, sealErr := fixture.oldSeal.Seal([]byte("old"), registry.DestinationSecretsAAD("dest_a"))
	if sealErr != nil {
		t.Fatalf("seal: %v", sealErr)
	}
	fixture.setSealedValue(ctx, t, destinations, "dest_a", oldBlob)
	fixture.setSealedValue(ctx, t, destinations, "dest_c", []byte{0x01})
	before := fixture.snapshotBlobs(ctx, t)

	report, err := Verify(ctx, fixture.pool, fixture.dualSeal, nil)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err = %v, want ErrIncomplete", err)
	}
	want := []RowIssue{
		{Table: "destinations", RowKey: "dest_a", Reason: ReasonDecryptFailed},
		{Table: "destinations", RowKey: "dest_c", Reason: ReasonMalformed},
	}
	if len(report.Issues) != 2 || report.Issues[0] != want[0] || report.Issues[1] != want[1] {
		t.Fatalf("issues = %+v, want %+v", report.Issues, want)
	}
	for key, blob := range fixture.snapshotBlobs(ctx, t) {
		if !bytes.Equal(blob, before[key]) {
			t.Fatalf("Verify modified %s", key)
		}
	}
}

func TestRotateRequiresPreviousKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)
	hookCalled := false
	hook := func(context.Context, SealedTable, string) error { hookCalled = true; return nil }

	for name, sealer := range map[string]*seal.Sealer{"single key": fixture.newSeal, "nil": nil} {
		before := fixture.snapshotBlobs(ctx, t)
		report, err := Rotate(ctx, fixture.pool, Options{Sealer: sealer, BeforeUpdate: hook})
		if err == nil {
			t.Fatalf("%s: Rotate returned nil, want a refusal", name)
		}
		if errors.Is(err, ErrIncomplete) {
			t.Fatalf("%s: refusal must not be ErrIncomplete: %v", name, err)
		}
		if len(report.Tables) != 0 {
			t.Fatalf("%s: refused run read tables: %+v", name, report.Tables)
		}
		for key, blob := range fixture.snapshotBlobs(ctx, t) {
			if !bytes.Equal(blob, before[key]) {
				t.Fatalf("%s: refused run modified %s", name, key)
			}
		}
	}
	if hookCalled {
		t.Fatal("refused run reached the update hook")
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

func TestRotateRedaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTestBudget)
	defer cancel()
	fixture := newFixture(ctx, t)

	destinations := tableByName(t, "destinations")
	other, otherErr := seal.New(mustKey(t, otherKeyHex))
	if otherErr != nil {
		t.Fatalf("seal.New: %v", otherErr)
	}
	wrongKeyBlob := sealOrFatal(t, other, "plain-wrong-key-secret", registry.DestinationSecretsAAD("dest_b"))
	fixture.setSealedValue(ctx, t, destinations, "dest_b", wrongKeyBlob)
	fixture.setSealedValue(ctx, t, destinations, "dest_c", []byte("plain-malformed-secret"))
	secrets := []string{"plain-wrong-key-secret", "plain-malformed-secret"}
	for _, plaintext := range fixture.plaintexts {
		secrets = append(secrets, plaintext)
	}
	blobsBefore := fixture.snapshotBlobs(ctx, t)
	blobsBefore["wrong"] = wrongKeyBlob
	blobsBefore["malformed"] = []byte("plain-malformed-secret")

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	report, err := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, Logger: logger})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("err = %v, want ErrIncomplete", err)
	}
	// The plain ASCII value is not a sealed blob, so it is reported as malformed.
	wantIssues := []RowIssue{
		{Table: "destinations", RowKey: "dest_b", Reason: ReasonDecryptFailed},
		{Table: "destinations", RowKey: "dest_c", Reason: ReasonMalformed},
	}
	if len(report.Issues) != 2 || report.Issues[0] != wantIssues[0] || report.Issues[1] != wantIssues[1] {
		t.Fatalf("issues = %+v, want %+v", report.Issues, wantIssues)
	}
	for _, row := range fixture.allSealedRows() {
		if row.table == "destinations" && (row.key == "dest_b" || row.key == "dest_c") {
			continue
		}
		fixture.assertOpensUnder(ctx, t, fixture.newSeal, row.table, row.key, seal.KeyPrimary)
	}

	// A second run aborted by a failing hook adds a wrapped error to scan.
	// Put dest_a back under the old key so the run has a row to rotate.
	reseeded := sealOrFatal(t, fixture.oldSeal, "plain-reseeded-secret", registry.DestinationSecretsAAD("dest_a"))
	fixture.setSealedValue(ctx, t, destinations, "dest_a", reseeded)
	secrets = append(secrets, "plain-reseeded-secret")
	blobsBefore["reseeded"] = reseeded
	hook, _ := failOnNth(1)
	_, abortErr := Rotate(ctx, fixture.pool, Options{Sealer: fixture.dualSeal, Logger: logger, BeforeUpdate: hook})
	if abortErr == nil || errors.Is(abortErr, ErrIncomplete) {
		t.Fatalf("abort run err = %v, want the injected failure", abortErr)
	}
	if !strings.Contains(logs.String(), "key rotation aborted") {
		t.Fatalf("expected an abort log line: %s", logs.String())
	}

	forbidden := append([]string{}, secrets...)
	for _, keyHex := range []string{oldKeyHex, newKeyHex, otherKeyHex} {
		forbidden = append(forbidden, keyHex, string(mustKey(t, keyHex)))
	}
	blobsAfter := fixture.snapshotBlobs(ctx, t)
	for _, blobs := range []map[string][]byte{blobsBefore, blobsAfter} {
		for _, blob := range blobs {
			forbidden = append(forbidden, hex.EncodeToString(blob), base64.StdEncoding.EncodeToString(blob),
				base64.RawStdEncoding.EncodeToString(blob), byteaEscape(blob), `\x`+hex.EncodeToString(blob))
		}
	}
	output := logs.String() + "\n" + err.Error() + "\n" + abortErr.Error()
	for _, needle := range forbidden {
		if needle != "" && strings.Contains(output, needle) {
			t.Fatalf("logs or error leak %d-byte sensitive value", len(needle))
		}
	}
	if !strings.Contains(logs.String(), "row_key=dest_b") {
		t.Fatalf("expected a per-row warning in logs: %s", logs.String())
	}
}

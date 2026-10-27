package keyrotation

import (
	"bytes"
	"slices"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

var realColumns = []SealedColumn{
	{Table: "destinations", Column: "sealed_secrets"},
	{Table: "app_publishers", Column: "sealed_token"},
	{Table: "heartbeat_check_probes", Column: "sealed_access_headers"},
}

func TestCoverageGapsRealRegistryHasNone(t *testing.T) {
	missing, stale := CoverageGaps(realColumns, Registry())
	if len(missing) != 0 || len(stale) != 0 {
		t.Fatalf("missing=%v stale=%v, want none", missing, stale)
	}
}

func TestCoverageGapsReportsMissingEntry(t *testing.T) {
	tables := Registry()[1:]
	missing, stale := CoverageGaps(realColumns, tables)
	want := []SealedColumn{{Table: "destinations", Column: "sealed_secrets"}}
	if !slices.Equal(missing, want) || len(stale) != 0 {
		t.Fatalf("missing=%v stale=%v, want missing=%v", missing, stale, want)
	}
}

func TestCoverageGapsReportsSyntheticExtraColumn(t *testing.T) {
	found := append(slices.Clone(realColumns), SealedColumn{Table: "synthetic_table", Column: "sealed_extra"})
	missing, stale := CoverageGaps(found, Registry())
	want := []SealedColumn{{Table: "synthetic_table", Column: "sealed_extra"}}
	if !slices.Equal(missing, want) || len(stale) != 0 {
		t.Fatalf("missing=%v stale=%v, want missing=%v", missing, stale, want)
	}
}

func TestCoverageGapsReportsMisspeltRegistryEntryAsStale(t *testing.T) {
	tables := Registry()
	tables[0].SealedColumn = "sealed_secretz"
	missing, stale := CoverageGaps(realColumns, tables)
	wantMissing := []SealedColumn{{Table: "destinations", Column: "sealed_secrets"}}
	wantStale := []SealedColumn{{Table: "destinations", Column: "sealed_secretz"}}
	if !slices.Equal(missing, wantMissing) || !slices.Equal(stale, wantStale) {
		t.Fatalf("missing=%v stale=%v, want missing=%v stale=%v", missing, stale, wantMissing, wantStale)
	}
}

func TestRegistryShape(t *testing.T) {
	tables := Registry()
	if len(tables) != 3 {
		t.Fatalf("registry has %d entries, want 3", len(tables))
	}
	wantNullable := map[string]bool{"destinations": false, "app_publishers": false, "heartbeat_check_probes": true}
	for _, table := range tables {
		if table.Nullable != wantNullable[table.Table] {
			t.Errorf("%s Nullable=%v, want %v", table.Table, table.Nullable, wantNullable[table.Table])
		}
	}
}

func TestRegistryAADMatchesOwningPackages(t *testing.T) {
	want := map[string][]byte{
		"destinations":           registry.DestinationSecretsAAD("sample-dest"),
		"app_publishers":         publisher.TokenAAD("sample-app"),
		"heartbeat_check_probes": heartbeat.ProbeAccessAAD(42),
	}
	rowKeys := map[string]string{
		"destinations":           "sample-dest",
		"app_publishers":         "sample-app",
		"heartbeat_check_probes": "42",
	}
	for _, table := range Registry() {
		got, err := table.AAD(rowKeys[table.Table])
		if err != nil {
			t.Fatalf("%s AAD: %v", table.Table, err)
		}
		if !bytes.Equal(got, want[table.Table]) {
			t.Errorf("%s AAD=%q, want %q", table.Table, got, want[table.Table])
		}
	}
}

func TestProbeAADRejectsNonIntegerRowKey(t *testing.T) {
	for _, table := range Registry() {
		if table.Table != "heartbeat_check_probes" {
			continue
		}
		for _, badKey := range []string{"", "abc", "1.5", "12x"} {
			if _, err := table.AAD(badKey); err == nil {
				t.Errorf("AAD(%q) succeeded, want error", badKey)
			}
		}
	}
}

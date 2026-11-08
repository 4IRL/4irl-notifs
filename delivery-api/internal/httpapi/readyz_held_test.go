package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// scriptedHeldReporter returns a fixed snapshot and counts calls.
type scriptedHeldReporter struct {
	snapshot HeldSnapshot
	calls    int
}

func (reporter *scriptedHeldReporter) HeldSnapshot(context.Context) HeldSnapshot {
	reporter.calls++
	return reporter.snapshot
}

var heldTestNow = time.Date(2026, time.November, 8, 17, 0, 0, 0, time.UTC)

// serveHeldReadyz serves GET /readyz with the given reporter (nil leaves the
// field an untyped nil) and returns the code, status and raw held JSON ("" when
// the key is absent).
func serveHeldReadyz(
	testInstance *testing.T, reporter *scriptedHeldReporter, readiness *fakeReadinessChecker,
) (int, string, string) {
	testInstance.Helper()
	config := ServerConfig{Readiness: readiness, Now: func() time.Time { return heldTestNow }}
	if reporter != nil {
		config.Held = reporter
	}
	recorder := httptest.NewRecorder()
	NewServer(config).Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body struct {
		Status string          `json:"status"`
		Held   json.RawMessage `json:"held"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		testInstance.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, body.Status, string(body.Held)
}

func readinessFor(mismatch bool) *fakeReadinessChecker {
	expected := 1
	if mismatch {
		expected = 2
	}
	return &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 1, ExpectedSchemaVersion: expected}}
}

func TestReadyzHeldBlock(testInstance *testing.T) {
	oldest := heldTestNow.Add(-5 * time.Hour)
	future := heldTestNow.Add(time.Hour)
	asOf := heldTestNow.Add(-10 * time.Second)
	testCases := []struct {
		name     string
		snapshot HeldSnapshot
		want     string
	}{
		{
			name:     "empty",
			snapshot: HeldSnapshot{Available: true, AsOf: asOf},
			want: `{"available":true,"stale":false,"as_of":"2026-11-08T16:59:50Z","total":0,` +
				`"messages":{"count":0,"oldest_held_at":null,"oldest_age_seconds":null},` +
				`"unassigned_alerts":{"count":0},"apps":[],"apps_more":0}`,
		},
		{
			name: "non-zero with apps and overflow",
			snapshot: HeldSnapshot{
				Available: true, AsOf: asOf, HeldMessages: 5, OldestHeldAt: &oldest, UnassignedAlerts: 2,
				Apps:     []HeldAppCount{{AppID: "billing", Count: 4}, {AppID: "u4i", Count: 1}},
				AppsMore: 3,
			},
			want: `{"available":true,"stale":false,"as_of":"2026-11-08T16:59:50Z","total":7,` +
				`"messages":{"count":5,"oldest_held_at":"2026-11-08T12:00:00Z","oldest_age_seconds":18000},` +
				`"unassigned_alerts":{"count":2},"apps":[{"app_id":"billing","count":4},{"app_id":"u4i","count":1}],"apps_more":3}`,
		},
		{
			name:     "oldest in the future clamps age to zero",
			snapshot: HeldSnapshot{Available: true, AsOf: asOf, HeldMessages: 1, OldestHeldAt: &future},
			want: `{"available":true,"stale":false,"as_of":"2026-11-08T16:59:50Z","total":1,` +
				`"messages":{"count":1,"oldest_held_at":"2026-11-08T18:00:00Z","oldest_age_seconds":0},` +
				`"unassigned_alerts":{"count":0},"apps":[],"apps_more":0}`,
		},
		{
			name:     "stale snapshot",
			snapshot: HeldSnapshot{Available: true, Stale: true, AsOf: asOf},
			want: `{"available":true,"stale":true,"as_of":"2026-11-08T16:59:50Z","total":0,` +
				`"messages":{"count":0,"oldest_held_at":null,"oldest_age_seconds":null},` +
				`"unassigned_alerts":{"count":0},"apps":[],"apps_more":0}`,
		},
		{
			name:     "unavailable carries no counts",
			snapshot: HeldSnapshot{Available: false, HeldMessages: 99},
			want:     `{"available":false}`,
		},
	}
	for _, mismatch := range []bool{false, true} {
		for _, testCase := range testCases {
			name := testCase.name
			wantCode, wantStatus := http.StatusOK, statusOK
			if mismatch {
				name += " (schema mismatch)"
				wantCode, wantStatus = http.StatusServiceUnavailable, statusSchemaMismatch
			}
			testInstance.Run(name, func(testInstance *testing.T) {
				code, status, held := serveHeldReadyz(testInstance, &scriptedHeldReporter{snapshot: testCase.snapshot}, readinessFor(mismatch))
				if code != wantCode || status != wantStatus {
					testInstance.Fatalf("code, status = %d, %q, want %d, %q", code, status, wantCode, wantStatus)
				}
				if held != testCase.want {
					testInstance.Fatalf("held = %s, want %s", held, testCase.want)
				}
			})
		}
	}
}

func TestReadyzHeldNeverChangesStatus(testInstance *testing.T) {
	snapshots := []HeldSnapshot{
		{Available: true, AsOf: heldTestNow},
		{Available: true, AsOf: heldTestNow, HeldMessages: 100000, UnassignedAlerts: 5000},
		{Available: false},
	}
	for _, mismatch := range []bool{false, true} {
		wantCode, wantStatus := http.StatusOK, statusOK
		if mismatch {
			wantCode, wantStatus = http.StatusServiceUnavailable, statusSchemaMismatch
		}
		for _, snapshot := range snapshots {
			code, status, _ := serveHeldReadyz(testInstance, &scriptedHeldReporter{snapshot: snapshot}, readinessFor(mismatch))
			if code != wantCode || status != wantStatus {
				testInstance.Errorf("snapshot %+v: code, status = %d, %q, want %d, %q", snapshot, code, status, wantCode, wantStatus)
			}
		}
	}
}

func TestReadyzHeldNilReporterOmitsKey(testInstance *testing.T) {
	for _, mismatch := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		NewServer(ServerConfig{Readiness: readinessFor(mismatch)}).Handler().ServeHTTP(
			recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if strings.Contains(recorder.Body.String(), `"held"`) {
			testInstance.Fatalf("mismatch=%v: body %s carries a held key with no reporter", mismatch, recorder.Body.String())
		}
	}
}

// nilSafeHeldReporter tolerates a nil receiver and reports unavailable.
type nilSafeHeldReporter struct{}

func (reporter *nilSafeHeldReporter) HeldSnapshot(context.Context) HeldSnapshot {
	return HeldSnapshot{Available: false}
}

// A typed-nil reporter is outside the documented contract (Held must be an
// untyped nil when absent): the server cannot detect it, so it calls the
// reporter and renders whatever it returns. This pins that behaviour for a
// nil-safe receiver (the key is present, not omitted).
func TestReadyzHeldTypedNilReporterIsCalledNotOmitted(testInstance *testing.T) {
	var reporter *nilSafeHeldReporter
	recorder := httptest.NewRecorder()
	NewServer(ServerConfig{Readiness: readinessFor(false), Held: reporter}).Handler().ServeHTTP(
		recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body struct {
		Held json.RawMessage `json:"held"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		testInstance.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	if recorder.Code != http.StatusOK || string(body.Held) != `{"available":false}` {
		testInstance.Fatalf("code, held = %d, %s", recorder.Code, body.Held)
	}
}

func TestReadyzHeldNotCalledWhenUnavailable(testInstance *testing.T) {
	testCases := map[string]*fakeReadinessChecker{
		"checker error": {err: errors.New("database down")},
	}
	for name, readiness := range testCases {
		testInstance.Run(name, func(testInstance *testing.T) {
			reporter := &scriptedHeldReporter{snapshot: HeldSnapshot{Available: true}}
			recorder := httptest.NewRecorder()
			NewServer(ServerConfig{Readiness: readiness, Held: reporter}).Handler().ServeHTTP(
				recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), `"held"`) {
				testInstance.Fatalf("code, body = %d, %s, want 503 without held", recorder.Code, recorder.Body.String())
			}
			if reporter.calls != 0 {
				testInstance.Fatalf("reporter called %d times, want 0", reporter.calls)
			}
		})
	}

	testInstance.Run("nil checker", func(testInstance *testing.T) {
		reporter := &scriptedHeldReporter{snapshot: HeldSnapshot{Available: true}}
		recorder := httptest.NewRecorder()
		NewServer(ServerConfig{Held: reporter}).Handler().ServeHTTP(
			recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), `"held"`) {
			testInstance.Fatalf("code, body = %d, %s, want 503 without held", recorder.Code, recorder.Body.String())
		}
		if reporter.calls != 0 {
			testInstance.Fatalf("reporter called %d times, want 0", reporter.calls)
		}
	})
}

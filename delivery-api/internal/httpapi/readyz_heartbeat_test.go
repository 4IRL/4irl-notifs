package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
)

// readyzBody is the decoded /readyz body fields these tests care about.
type readyzBody struct {
	Status           string `json:"status"`
	HeartbeatChecker struct {
		LastTickAt *string `json:"last_tick_at"`
		Stale      bool    `json:"stale"`
	} `json:"heartbeat_checker"`
	HeartbeatProber struct {
		LastTickAt *string `json:"last_tick_at"`
		Stale      bool    `json:"stale"`
	} `json:"heartbeat_prober"`
}

// readyzWithTrackers serves GET /readyz with independent checker and prober
// trackers. A negative age means never ticked; a nil-tracker case is covered
// separately.
func readyzWithTrackers(testInstance *testing.T, checkerAgo, proberAgo time.Duration, mismatch bool) (int, readyzBody) {
	testInstance.Helper()
	now := time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)
	newTracker := func(ago time.Duration) *heartbeat.Freshness {
		freshness := heartbeat.NewFreshness(func() time.Time { return now })
		if ago >= 0 {
			freshness.Record(now.Add(-ago))
		}
		return freshness
	}
	expected := 1
	if mismatch {
		expected = 2
	}
	server := NewServer(ServerConfig{
		Readiness:        &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 1, ExpectedSchemaVersion: expected}},
		HeartbeatChecker: newTracker(checkerAgo),
		HeartbeatProber:  newTracker(proberAgo),
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body readyzBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		testInstance.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, body
}

func TestReadyzHeartbeatProberStaleness(testInstance *testing.T) {
	testCases := []struct {
		name       string
		tickedAgo  time.Duration
		wantStale  bool
		wantTickAt string
	}{
		{"never ticked", -1, true, ""},
		{"right after a tick", 0, false, "2026-10-19T09:00:00Z"},
		{"exactly 90 s old is not yet stale", 90 * time.Second, false, "2026-10-19T08:58:30Z"},
		{"one nanosecond past 90 s is stale", 90*time.Second + time.Nanosecond, true, "2026-10-19T08:58:29Z"},
	}
	for _, mismatch := range []bool{false, true} {
		for _, testCase := range testCases {
			name := testCase.name
			wantCode := http.StatusOK
			wantStatus := statusOK
			if mismatch {
				name += " (schema mismatch)"
				wantCode = http.StatusServiceUnavailable
				wantStatus = statusSchemaMismatch
			}
			testInstance.Run(name, func(testInstance *testing.T) {
				code, body := readyzWithTrackers(testInstance, 0, testCase.tickedAgo, mismatch)
				if code != wantCode || body.Status != wantStatus {
					testInstance.Fatalf("code, status = %d, %q, want %d, %q", code, body.Status, wantCode, wantStatus)
				}
				if body.HeartbeatProber.Stale != testCase.wantStale {
					testInstance.Errorf("stale = %v, want %v", body.HeartbeatProber.Stale, testCase.wantStale)
				}
				gotTickAt := ""
				if body.HeartbeatProber.LastTickAt != nil {
					gotTickAt = *body.HeartbeatProber.LastTickAt
				}
				if gotTickAt != testCase.wantTickAt {
					testInstance.Errorf("last_tick_at = %q, want %q", gotTickAt, testCase.wantTickAt)
				}
			})
		}
	}
}

func TestReadyzHeartbeatTrackersAreIndependent(testInstance *testing.T) {
	staleAgo := 91 * time.Second
	testInstance.Run("stale checker with fresh prober", func(testInstance *testing.T) {
		_, body := readyzWithTrackers(testInstance, staleAgo, 0, false)
		if !body.HeartbeatChecker.Stale || body.HeartbeatProber.Stale {
			testInstance.Errorf("checker stale = %v, prober stale = %v, want true, false", body.HeartbeatChecker.Stale, body.HeartbeatProber.Stale)
		}
	})
	testInstance.Run("fresh checker with stale prober", func(testInstance *testing.T) {
		_, body := readyzWithTrackers(testInstance, 0, staleAgo, false)
		if body.HeartbeatChecker.Stale || !body.HeartbeatProber.Stale {
			testInstance.Errorf("checker stale = %v, prober stale = %v, want false, true", body.HeartbeatChecker.Stale, body.HeartbeatProber.Stale)
		}
	})
}

func TestReadyzHeartbeatProberNilTrackerIsStale(testInstance *testing.T) {
	server := NewServer(ServerConfig{
		Readiness: &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 1, ExpectedSchemaVersion: 1}},
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	const want = `"heartbeat_prober":{"last_tick_at":null,"stale":true}`
	if !strings.Contains(recorder.Body.String(), want) {
		testInstance.Fatalf("body %q does not contain %s", recorder.Body.String(), want)
	}
	if recorder.Code != http.StatusOK {
		testInstance.Errorf("code = %d, want %d", recorder.Code, http.StatusOK)
	}
}

// readyzWithChecker serves GET /readyz for a checker whose last tick was
// tickedAgo before "now" (never ticked when tickedAgo is negative), and a
// database whose schema is current or, when mismatch is true, behind.
func readyzWithChecker(testInstance *testing.T, tickedAgo time.Duration, mismatch bool) (int, readyzBody) {
	testInstance.Helper()
	now := time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)
	freshness := heartbeat.NewFreshness(func() time.Time { return now })
	if tickedAgo >= 0 {
		freshness.Record(now.Add(-tickedAgo))
	}
	expected := 1
	if mismatch {
		expected = 2
	}
	server := NewServer(ServerConfig{
		Readiness:        &fakeReadinessChecker{readiness: Readiness{SchemaVersion: 1, ExpectedSchemaVersion: expected}},
		HeartbeatChecker: freshness,
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body readyzBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		testInstance.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, body
}

func TestReadyzHeartbeatCheckerStaleness(testInstance *testing.T) {
	testCases := []struct {
		name       string
		tickedAgo  time.Duration
		wantStale  bool
		wantTickAt string
	}{
		{"never ticked", -1, true, ""},
		{"right after a tick", 0, false, "2026-10-19T09:00:00Z"},
		{"29 s old", 29 * time.Second, false, "2026-10-19T08:59:31Z"},
		{"exactly 90 s old is not yet stale", 90 * time.Second, false, "2026-10-19T08:58:30Z"},
		{"one nanosecond past 90 s is stale", 90*time.Second + time.Nanosecond, true, "2026-10-19T08:58:29Z"},
		{"91 s old", 91 * time.Second, true, "2026-10-19T08:58:29Z"},
	}
	for _, mismatch := range []bool{false, true} {
		for _, testCase := range testCases {
			name := testCase.name
			wantCode := http.StatusOK
			wantStatus := statusOK
			if mismatch {
				name += " (schema mismatch)"
				wantCode = http.StatusServiceUnavailable
				wantStatus = statusSchemaMismatch
			}
			testInstance.Run(name, func(testInstance *testing.T) {
				code, body := readyzWithChecker(testInstance, testCase.tickedAgo, mismatch)
				// Staleness is reported but never changes the status or HTTP code.
				if code != wantCode || body.Status != wantStatus {
					testInstance.Fatalf("code, status = %d, %q, want %d, %q", code, body.Status, wantCode, wantStatus)
				}
				if body.HeartbeatChecker.Stale != testCase.wantStale {
					testInstance.Errorf("stale = %v, want %v", body.HeartbeatChecker.Stale, testCase.wantStale)
				}
				gotTickAt := ""
				if body.HeartbeatChecker.LastTickAt != nil {
					gotTickAt = *body.HeartbeatChecker.LastTickAt
				}
				if gotTickAt != testCase.wantTickAt {
					testInstance.Errorf("last_tick_at = %q, want %q", gotTickAt, testCase.wantTickAt)
				}
			})
		}
	}
}

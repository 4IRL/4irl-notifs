//go:build integration

package integration

// Live-stack test of GET /v1/admin/held-messages. It is unflagged: held rows and unassigned checks
// are seeded straight through psql, so it runs in both the flag-off and the flag-on stacks. The
// endpoint is operator-only through the Cloudflare Access /v1/admin split, so no app-token rejection
// is asserted here. The database is shared (the releaser and sweeper may run), so assertions touch
// only this test's own app ids.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type heldAppJSON struct {
	AppID    string `json:"app_id"`
	Count    int    `json:"count"`
	OldestAt string `json:"oldest_at"`
}

type unassignedCheckJSON struct {
	CheckID      int64  `json:"check_id"`
	AppID        string `json:"app_id"`
	Name         string `json:"name"`
	PendingAlert string `json:"pending_alert"`
}

type heldMessagesJSON struct {
	Apps             *[]heldAppJSON         `json:"apps"`
	UnassignedChecks *[]unassignedCheckJSON `json:"unassigned_checks"`
	UnassignedTotal  int                    `json:"unassigned_total"`
}

func TestHeldMessagesReportsHeldCountsAndUnassignedChecks(testInstance *testing.T) {
	heldApp := "ithm_" + randomSuffix(testInstance)
	checkApp := "ithb_" + randomSuffix(testInstance)
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("delete from messages where app_id = '%s'", heldApp))
		psql(testInstance, fmt.Sprintf("delete from heartbeat_checks where app_id = '%s'", checkApp))
	})
	// Two held rows on distinct channels with distinct held_at; the older one is oldest_at.
	psql(testInstance, fmt.Sprintf(
		`insert into messages (app_id, channel, idempotency_key, payload, held_at) values
('%[1]s', 'chan_0', 'key-0', '{}'::jsonb, '2026-01-02 03:04:05+00'),
('%[1]s', 'chan_1', 'key-1', '{}'::jsonb, '2026-01-03 03:04:05+00')`, heldApp))
	// 51 checks with a pending alert and no destination link.
	psql(testInstance, fmt.Sprintf(
		`insert into heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at, created_at, updated_at, pending_alert)
select '%s', 'check_' || lpad(n::text, 3, '0'), 600, 300, now(), now(), now(), 'down' from generate_series(1, 51) as n`,
		checkApp))

	status, body := apiRequest(testInstance, http.MethodGet, "/v1/admin/held-messages", nil)
	requireStatus(testInstance, "GET held-messages", status, http.StatusOK, body)
	var decoded heldMessagesJSON
	decodeBody(testInstance, "GET held-messages", body, &decoded)

	if decoded.Apps == nil || decoded.UnassignedChecks == nil {
		testInstance.Fatalf("arrays must not be null; body: %s", body)
	}
	var mine *heldAppJSON
	for index, row := range *decoded.Apps {
		if row.AppID == heldApp {
			mine = &(*decoded.Apps)[index]
		}
	}
	if mine == nil || mine.Count != 2 {
		testInstance.Fatalf("held app row = %+v, want count 2; body: %s", mine, body)
	}
	oldest, parseErr := time.Parse(time.RFC3339, mine.OldestAt)
	if parseErr != nil || !strings.HasSuffix(mine.OldestAt, "Z") ||
		!oldest.Equal(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)) {
		testInstance.Fatalf("oldest_at = %q (%v), want 2026-01-02T03:04:05Z", mine.OldestAt, parseErr)
	}

	if len(*decoded.UnassignedChecks) != 50 {
		testInstance.Fatalf("unassigned_checks has %d elements, want the 50 cap", len(*decoded.UnassignedChecks))
	}
	if decoded.UnassignedTotal < 51 {
		testInstance.Fatalf("unassigned_total = %d, want at least 51", decoded.UnassignedTotal)
	}
	for _, check := range *decoded.UnassignedChecks {
		if check.PendingAlert == "" || check.CheckID == 0 || check.AppID == "" || check.Name == "" {
			testInstance.Errorf("check = %+v, want every field set", check)
		}
	}
}

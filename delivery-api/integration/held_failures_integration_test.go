//go:build integration

package integration

// Live-stack test of GET /v1/admin/held-failures. It is unflagged: release-failed rows are seeded
// straight through psql (held_at NULL, release_failed_at set), so it runs in both the flag-off and
// the flag-on stacks. The endpoint is operator-only through the Cloudflare Access /v1/admin split,
// so no app-token rejection is asserted here.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type heldFailureJSON struct {
	MessageID string `json:"message_id"`
	AppID     string `json:"app_id"`
	Channel   string `json:"channel"`
	Reason    string `json:"reason"`
	FailedAt  string `json:"failed_at"`
}

type heldFailuresJSON struct {
	Failures []heldFailureJSON `json:"failures"`
	Total    int               `json:"total"`
}

func TestHeldFailuresListsReleaseFailedMessagesNewestFirst(testInstance *testing.T) {
	appID := "itfail_" + randomSuffix(testInstance)
	testInstance.Cleanup(func() {
		psql(testInstance, fmt.Sprintf("delete from messages where app_id = '%s'", appID))
	})
	for index, ageSeconds := range []int{2, 1} {
		psql(testInstance, fmt.Sprintf(
			`insert into messages (app_id, channel, idempotency_key, payload, release_failed_at, release_failed_reason)
values ('%s', 'chan_%d', 'key-%d', '{"secret":"never-listed"}'::jsonb, now() - interval '%d seconds', 'release_invalid')`,
			appID, index, index, ageSeconds))
	}

	status, body := apiRequest(testInstance, http.MethodGet, "/v1/admin/held-failures?limit=200", nil)
	requireStatus(testInstance, "GET held-failures", status, http.StatusOK, body)
	var decoded heldFailuresJSON
	decodeBody(testInstance, "GET held-failures", body, &decoded)

	if strings.Contains(string(body), "never-listed") || strings.Contains(string(body), "payload") ||
		strings.Contains(string(body), "idempotency") || strings.Contains(string(body), "key-0") {
		testInstance.Fatalf("body carries a payload or idempotency key: %s", body)
	}
	if decoded.Total < 2 {
		testInstance.Fatalf("total = %d, want at least 2", decoded.Total)
	}
	var mine []heldFailureJSON
	for _, failure := range decoded.Failures {
		if failure.AppID == appID {
			mine = append(mine, failure)
		}
	}
	if len(mine) != 2 {
		testInstance.Fatalf("listed %d failures for %s, want 2; body: %s", len(mine), appID, body)
	}
	if mine[0].Channel != "chan_1" || mine[1].Channel != "chan_0" {
		testInstance.Fatalf("channels = [%s %s], want newest first [chan_1 chan_0]", mine[0].Channel, mine[1].Channel)
	}
	for _, failure := range mine {
		if failure.Reason != "release_invalid" || !messageIDPattern.MatchString(failure.MessageID) {
			testInstance.Errorf("failure = %+v, want reason release_invalid and a uuid message_id", failure)
		}
		if _, parseErr := time.Parse(time.RFC3339, failure.FailedAt); parseErr != nil || !strings.HasSuffix(failure.FailedAt, "Z") {
			testInstance.Errorf("failed_at = %q, want RFC3339 UTC", failure.FailedAt)
		}
	}

	status, body = apiRequest(testInstance, http.MethodGet, "/v1/admin/held-failures?limit=1", nil)
	requireStatus(testInstance, "GET held-failures?limit=1", status, http.StatusOK, body)
	var limited heldFailuresJSON
	decodeBody(testInstance, "GET held-failures?limit=1", body, &limited)
	if len(limited.Failures) != 1 || limited.Total < 2 {
		testInstance.Fatalf("limit=1 returned %d failures, total %d; want 1 failure and total at least 2",
			len(limited.Failures), limited.Total)
	}
}

func TestHeldFailuresRejectsBadLimits(testInstance *testing.T) {
	for _, rawQuery := range []string{"limit=0", "limit=201", "limit=abc"} {
		status, body := apiRequest(testInstance, http.MethodGet, "/v1/admin/held-failures?"+rawQuery, nil)
		requireStatus(testInstance, "GET held-failures?"+rawQuery, status, http.StatusBadRequest, body)
		requireErrorMessage(testInstance, "GET held-failures?"+rawQuery, body, "invalid limit")
	}
}

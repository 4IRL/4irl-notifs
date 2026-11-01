//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/provisionclient"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/resetlimit"
)

// With the limit table really gone, the reset route fails closed: the store-down 503 and no upstream
// call. This runs against an isolated schema, never the shared compose database, so a test that dies
// mid-way cannot break other suites.
func TestAppResetWithTheLimitTableMissingIs503AndNeverRelays(testInstance *testing.T) {
	ctx := context.Background()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	rig := newProvisioningRig(&fakeAppProvisioning{answer: provisionclient.Passthrough{
		Status: http.StatusOK, Body: []byte(`{"user_id":"u_x","app_id":"urls4irl","password":"new-pass"}`),
	}})
	rig.server.resetLimiter = resetlimit.NewPGStore(pool)

	// Control: with the table present the call is admitted and relayed.
	admitted := rig.post(resetPasswordPath, resetBody("a@example.com"))
	if admitted.Code != http.StatusOK || rig.fake.totalCalls() != 1 {
		testInstance.Fatalf("control: status %d, upstream calls %d, want 200 and 1: %s",
			admitted.Code, rig.fake.totalCalls(), admitted.Body.String())
	}

	if _, dropErr := pool.Exec(ctx, `DROP TABLE app_reset_attempts`); dropErr != nil {
		testInstance.Fatalf("drop app_reset_attempts: %v", dropErr)
	}

	recorder := rig.post(resetPasswordPath, resetBody("a@example.com"))

	assertResponse(testInstance, recorder, http.StatusServiceUnavailable, `{"error":"reset limit unavailable"}`+"\n")
	assertNoStore(testInstance, recorder)
	if rig.fake.totalCalls() != 1 {
		testInstance.Fatalf("upstream calls = %d after the drop, want still 1 (no reset attempted)", rig.fake.totalCalls())
	}
}

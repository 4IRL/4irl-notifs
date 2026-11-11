package heartbeat

import (
	"context"
	"testing"
)

func TestListUnassignedAlertsRejectsANonPositiveLimitBeforeTouchingTheDatabase(testInstance *testing.T) {
	store := &PGStore{}
	for _, limit := range []int{0, -1} {
		checks, total, listErr := store.ListUnassignedAlerts(context.Background(), limit)
		if listErr == nil {
			testInstance.Fatalf("limit %d: want an error, got checks=%v total=%d", limit, checks, total)
		}
	}
}

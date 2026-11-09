//go:build integration

package heartbeat_test

import (
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// typedDestination inserts a destination of destinationType with the given enabled flag.
func (stack fixture) typedDestination(
	testInstance *testing.T, name string, destinationType string, enabled bool,
) registry.StoredDestination {
	testInstance.Helper()
	config := map[string]string{"url": "https://example.test/" + name}
	if destinationType == registry.TypeNtfy {
		config = map[string]string{"topic": "alerts_" + name}
	}
	inserted, err := stack.registry.InsertDestination(stack.ctx, registry.StoredDestination{
		Name:          name,
		Type:          destinationType,
		Config:        config,
		SealedSecrets: []byte{0x01},
		Enabled:       enabled,
	})
	if err != nil {
		testInstance.Fatalf("insert destination %s: %v", name, err)
	}
	return inserted
}

// zeroDestinationCheck inserts a check with no destination links by raw SQL (the
// API can create one now, but this keeps the fixture independent of the service
// path under test). appOwned makes it an app-owned check.
func (stack fixture) zeroDestinationCheck(testInstance *testing.T, name string, appOwned bool) {
	testInstance.Helper()
	statement := `INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at, created_at, updated_at)
VALUES ('billing', $1, 600, 300, $2, $2, $2)`
	if appOwned {
		statement = `INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at, created_at, updated_at, owner_kind, owner_app_id)
VALUES ('billing', $1, 600, 300, $2, $2, $2, 'app', 'billing')`
	}
	if _, execErr := stack.pool.Exec(stack.ctx, statement, name, created); execErr != nil {
		testInstance.Fatalf("insert zero-destination check %s: %v", name, execErr)
	}
}

// setPending marks the named check as having an unsent down alert.
func (stack fixture) setPending(testInstance *testing.T, name string) {
	testInstance.Helper()
	if _, execErr := stack.pool.Exec(stack.ctx,
		`UPDATE heartbeat_checks SET pending_alert = 'down' WHERE name = $1`, name); execErr != nil {
		testInstance.Fatalf("set pending_alert on %s: %v", name, execErr)
	}
}

func TestCountUnassignedAlerts(testInstance *testing.T) {
	stack := newFixture(testInstance) // supported set: {webhook: true}
	stack.typedDestination(testInstance, "off_one", registry.TypeWebhook, false)
	stack.typedDestination(testInstance, "off_two", registry.TypeWebhook, false)
	stack.typedDestination(testInstance, "ntfy_on", registry.TypeNtfy, true)
	stack.typedDestination(testInstance, "hook_on", registry.TypeWebhook, true)

	stack.zeroDestinationCheck(testInstance, "zero_operator", false)
	stack.zeroDestinationCheck(testInstance, "zero_app", true)
	stack.check(testInstance, "all_disabled", "off_one", "off_two")
	stack.check(testInstance, "unsupported_only", "ntfy_on")
	stack.check(testInstance, "mixed", "off_one", "hook_on")
	stack.check(testInstance, "deliverable", "hook_on")
	stack.zeroDestinationCheck(testInstance, "zero_no_alert", false)

	for _, name := range []string{
		"zero_operator", "zero_app", "all_disabled", "unsupported_only", "mixed", "deliverable",
	} {
		stack.setPending(testInstance, name)
	}

	got, err := stack.store.CountUnassignedAlerts(stack.ctx)
	if err != nil || got != 4 {
		testInstance.Fatalf("CountUnassignedAlerts = (%d, %v), want 4", got, err)
	}

	// A nil supported set counts every pending check as unassigned.
	nilSetStore := heartbeat.NewPGStore(stack.pool, stack.sealer, nil)
	gotNil, err := nilSetStore.CountUnassignedAlerts(stack.ctx)
	if err != nil || gotNil != 6 {
		testInstance.Fatalf("CountUnassignedAlerts(nil set) = (%d, %v), want 6", gotNil, err)
	}
}

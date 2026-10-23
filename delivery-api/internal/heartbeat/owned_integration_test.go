//go:build integration

package heartbeat_test

import (
	"errors"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	ownerBilling = "billing"
	ownerOther   = "other"
)

// ownedCheck creates a check under app_id appID owned by ownerApp (the check's own app when ownerApp is its
// app_id, the database's CHECK allows nothing else for an app owner).
func (stack fixture) ownedCheck(
	testInstance *testing.T, appID, name, ownerApp string, destinations ...string,
) heartbeat.StoredCheck {
	testInstance.Helper()
	owner := ownerApp
	stored, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: appID, Name: name, PeriodSeconds: 600, GraceSeconds: 300, DestinationNames: destinations,
		OwnerKind: registry.OwnerApp, OwnerAppID: &owner,
	}, created)
	if err != nil {
		testInstance.Fatalf("create check %s owned by %s: %v", name, ownerApp, err)
	}
	return stored
}

func TestListByOwnerReturnsOnlyThatAppsChecksInNameOrder(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.ownedCheck(testInstance, ownerBilling, "zeta", ownerBilling, "ops")
	stack.ownedCheck(testInstance, ownerBilling, "alpha", ownerBilling, "ops")
	stack.ownedCheck(testInstance, ownerOther, "alpha", ownerOther, "ops")
	// An operator check under the same app_id is the operator's, however it is named.
	stack.check(testInstance, "operator_nightly", "ops")

	listed, err := stack.store.ListByOwner(stack.ctx, ownerBilling)

	if err != nil || len(listed) != 2 || listed[0].Name != "alpha" || listed[1].Name != "zeta" {
		testInstance.Fatalf("ListByOwner = (%+v, %v), want billing's alpha and zeta in name order", listed, err)
	}
	none, err := stack.store.ListByOwner(stack.ctx, "nobody")
	if err != nil || none == nil || len(none) != 0 {
		testInstance.Fatalf("ListByOwner(nobody) = (%#v, %v), want an empty non-nil slice", none, err)
	}
}

// Every *OwnedBy write takes the owner into the row lock (or the one DELETE statement): a check owned by
// anyone else, or absent, is not found and is never touched.
func TestOwnedByWritesRefuseAnyoneButTheOwnerAndChangeNothing(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	mine := stack.ownedCheck(testInstance, ownerBilling, "weekly", ownerBilling, "ops")
	operatorOwned := stack.check(testInstance, "nightly", "ops")
	period := 3600

	for label, target := range map[string]struct {
		id    int64
		appID string
	}{
		"another app":          {id: mine.ID, appID: ownerOther},
		"the operator's check": {id: operatorOwned.ID, appID: ownerBilling},
		"an absent check":      {id: absentID, appID: ownerBilling},
		"an empty app id":      {id: mine.ID, appID: ""},
	} {
		_, patchErr := stack.store.PatchOwnedBy(stack.ctx, target.id, target.appID, heartbeat.CheckPatch{PeriodSeconds: &period}, created)
		_, pauseErr := stack.store.PauseOwnedBy(stack.ctx, target.id, target.appID, created)
		_, resumeErr := stack.store.ResumeOwnedBy(stack.ctx, target.id, target.appID, created)
		deleted, deleteErr := stack.store.DeleteOwnedBy(stack.ctx, target.id, target.appID)

		for op, err := range map[string]error{"patch": patchErr, "pause": pauseErr, "resume": resumeErr} {
			if !errors.Is(err, heartbeat.ErrCheckNotFound) {
				testInstance.Fatalf("%s %s: err = %v, want ErrCheckNotFound", op, label, err)
			}
		}
		if deleteErr != nil || deleted {
			testInstance.Fatalf("delete %s = (%v, %v), want (false, nil)", label, deleted, deleteErr)
		}
	}
	reread, err := stack.store.Get(stack.ctx, mine.ID)
	if err != nil || reread.PeriodSeconds != 600 || reread.Paused {
		testInstance.Fatalf("the owner's check = (%+v, %v), want it untouched", reread, err)
	}
	operatorReread, err := stack.store.Get(stack.ctx, operatorOwned.ID)
	if err != nil || operatorReread.PeriodSeconds != 600 || operatorReread.Paused {
		testInstance.Fatalf("the operator's check = (%+v, %v), want it untouched", operatorReread, err)
	}

	patched, err := stack.store.PatchOwnedBy(stack.ctx, mine.ID, ownerBilling, heartbeat.CheckPatch{PeriodSeconds: &period}, created)
	if err != nil || patched.PeriodSeconds != period || patched.OwnerKind != registry.OwnerApp {
		testInstance.Fatalf("owner PatchOwnedBy = (%+v, %v), want the period changed and the owner kept", patched, err)
	}
	paused, err := stack.store.PauseOwnedBy(stack.ctx, mine.ID, ownerBilling, created)
	if err != nil || !paused.Paused {
		testInstance.Fatalf("owner PauseOwnedBy = (%+v, %v), want paused", paused, err)
	}
	resumed, err := stack.store.ResumeOwnedBy(stack.ctx, mine.ID, ownerBilling, created)
	if err != nil || resumed.Paused {
		testInstance.Fatalf("owner ResumeOwnedBy = (%+v, %v), want resumed", resumed, err)
	}
	deleted, err := stack.store.DeleteOwnedBy(stack.ctx, mine.ID, ownerBilling)
	if err != nil || !deleted {
		testInstance.Fatalf("owner DeleteOwnedBy = (%v, %v), want (true, nil)", deleted, err)
	}
}

// An owned patch still refuses a destination the check's app may not use, atomically with the owner lock.
func TestPatchOwnedByRefusesAnotherAppsDestination(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	stack.appOwnedDestination(testInstance, "other_hook", ownerOther)
	mine := stack.ownedCheck(testInstance, ownerBilling, "weekly", ownerBilling, "ops")

	_, err := stack.store.PatchOwnedBy(stack.ctx, mine.ID, ownerBilling,
		heartbeat.CheckPatch{DestinationNames: []string{"other_hook"}}, created)

	if !errors.Is(err, registry.ErrUnknownDestination) {
		testInstance.Fatalf("error = %v, want ErrUnknownDestination", err)
	}
	reread, getErr := stack.store.Get(stack.ctx, mine.ID)
	if getErr != nil || len(reread.Destinations) != 1 || reread.Destinations[0].Name != "ops" {
		testInstance.Fatalf("check after the refused patch = (%+v, %v), want destinations unchanged", reread, getErr)
	}
}

// An app-owned check whose app_id is not its owner's id is refused by the database CHECK, and surfaces as
// the client-correctable registry.ErrCheckViolation, not a server fault.
func TestAppCheckAppIDCheckSurfacesAsErrCheckViolation(testInstance *testing.T) {
	stack := newFixture(testInstance)
	stack.destination(testInstance, "ops")
	owner := ownerOther

	_, err := stack.store.Create(stack.ctx, heartbeat.NewCheck{
		AppID: ownerBilling, Name: "weekly", PeriodSeconds: 600, GraceSeconds: 300, DestinationNames: []string{"ops"},
		OwnerKind: registry.OwnerApp, OwnerAppID: &owner,
	}, created)

	if !errors.Is(err, registry.ErrCheckViolation) {
		testInstance.Fatalf("error = %v, want registry.ErrCheckViolation", err)
	}
	if rows := stack.count(testInstance, "heartbeat_checks"); rows != 0 {
		testInstance.Fatalf("heartbeat_checks rows = %d after the refused create, want 0", rows)
	}
}

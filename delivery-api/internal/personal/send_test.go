package personal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolvePersonal(testInstance *testing.T) {
	ctx := context.Background()
	fix := newFixture(testInstance)
	view := fix.create(testInstance, aliceEmail)
	otherPerson := fix.create(testInstance, bobEmail)

	resolved, resolveErr := fix.service.ResolvePersonal(ctx, view.ID, appA, view.PersonHash)
	if resolveErr != nil {
		testInstance.Fatalf("ResolvePersonal: %v", resolveErr)
	}
	if resolved.ID != view.ID || resolved.Type != TypeDiscord || resolved.Name != PersonalDeliveryName(view.ID) ||
		resolved.Secrets["webhook_url"] != discordURL {
		testInstance.Fatalf("resolved = %+v", resolved)
	}

	if _, notFoundErr := fix.service.ResolvePersonal(ctx, 999, appA, view.PersonHash); !errors.Is(notFoundErr, ErrNotFound) {
		testInstance.Fatalf("absent id err = %v, want ErrNotFound", notFoundErr)
	}
	if _, mismatchErr := fix.service.ResolvePersonal(ctx, view.ID, appA, otherPerson.PersonHash); !errors.Is(mismatchErr, ErrNotFound) {
		testInstance.Fatalf("person mismatch err = %v, want ErrNotFound", mismatchErr)
	}
	if _, optErr := fix.service.ResolvePersonal(ctx, view.ID, appB, view.PersonHash); !errors.Is(optErr, ErrNotOptedIn) {
		testInstance.Fatalf("not opted in err = %v, want ErrNotOptedIn", optErr)
	}

	// A registrar-removed row that is still enabled and opted in resolves.
	removedAt := time.Now()
	fix.store.rows[view.ID].RegistrarRemovedAt = &removedAt
	if _, removedErr := fix.service.ResolvePersonal(ctx, view.ID, appA, view.PersonHash); removedErr != nil {
		testInstance.Fatalf("registrar-removed row err = %v, want nil", removedErr)
	}

	// A disabled row is ErrDisabled even when it is not opted in.
	if disableErr := fix.service.DisablePersonal(ctx, view.ID, "x"); disableErr != nil {
		testInstance.Fatalf("DisablePersonal: %v", disableErr)
	}
	if _, disabledErr := fix.service.ResolvePersonal(ctx, view.ID, appA, view.PersonHash); !errors.Is(disabledErr, ErrDisabled) {
		testInstance.Fatalf("disabled err = %v, want ErrDisabled", disabledErr)
	}
}

func TestResolvePersonalUnknownStateErrorsAreRaw(testInstance *testing.T) {
	ctx := context.Background()
	sentinels := []error{ErrNotFound, ErrDisabled, ErrNotOptedIn}
	assertRaw := func(testInstance *testing.T, resolveErr error) {
		testInstance.Helper()
		if resolveErr == nil {
			testInstance.Fatal("err = nil, want a raw error")
		}
		for _, sentinel := range sentinels {
			if errors.Is(resolveErr, sentinel) {
				testInstance.Fatalf("err = %v wraps sentinel %v, want a raw error", resolveErr, sentinel)
			}
		}
	}

	testInstance.Run("opt-in lookup fails", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		view := fix.create(testInstance, aliceEmail)
		fix.store.optedInErr = errors.New("db down")
		_, resolveErr := fix.service.ResolvePersonal(ctx, view.ID, appA, view.PersonHash)
		assertRaw(testInstance, resolveErr)
	})
	testInstance.Run("sealed secrets cannot be opened", func(testInstance *testing.T) {
		fix := newFixture(testInstance)
		view := fix.create(testInstance, aliceEmail)
		fix.store.rows[view.ID].SealedSecrets = []byte("not a sealed blob")
		_, resolveErr := fix.service.ResolvePersonal(ctx, view.ID, appA, view.PersonHash)
		assertRaw(testInstance, resolveErr)
	})
}

func TestOptedInTargets(testInstance *testing.T) {
	ctx := context.Background()
	fix := newFixture(testInstance)
	first := fix.create(testInstance, aliceEmail)
	second := fix.create(testInstance, aliceEmail)
	disabled := fix.create(testInstance, aliceEmail)
	other := fix.create(testInstance, bobEmail)
	if disableErr := fix.service.DisablePersonal(ctx, disabled.ID, "x"); disableErr != nil {
		testInstance.Fatalf("DisablePersonal: %v", disableErr)
	}

	targets, listErr := fix.service.OptedInTargets(ctx, appA, first.PersonHash)
	if listErr != nil {
		testInstance.Fatalf("OptedInTargets: %v", listErr)
	}
	if len(targets) != 2 || targets[0].ID != first.ID || targets[1].ID != second.ID || targets[0].Type != TypeDiscord {
		testInstance.Fatalf("targets = %+v, want [%d %d] (disabled and other-person rows excluded)", targets, first.ID, second.ID)
	}
	if forOther, otherErr := fix.service.OptedInTargets(ctx, appB, first.PersonHash); otherErr != nil || len(forOther) != 0 {
		testInstance.Fatalf("other app targets = %+v, %v, want none", forOther, otherErr)
	}
	if forBob, bobErr := fix.service.OptedInTargets(ctx, appA, other.PersonHash); bobErr != nil || len(forBob) != 1 {
		testInstance.Fatalf("bob targets = %+v, %v, want one", forBob, bobErr)
	}
}

func TestSelectedTargets(testInstance *testing.T) {
	ctx := context.Background()
	fix := newFixture(testInstance)
	first := fix.create(testInstance, aliceEmail)
	second := fix.create(testInstance, aliceEmail)
	disabled := fix.create(testInstance, aliceEmail)
	other := fix.create(testInstance, bobEmail)
	if disableErr := fix.service.DisablePersonal(ctx, disabled.ID, "x"); disableErr != nil {
		testInstance.Fatalf("DisablePersonal: %v", disableErr)
	}

	ids := []int64{other.ID, disabled.ID, second.ID, first.ID, 9999}
	targets, selectErr := fix.service.SelectedTargets(ctx, appA, first.PersonHash, ids)
	if selectErr != nil {
		testInstance.Fatalf("SelectedTargets: %v", selectErr)
	}
	if len(targets) != 2 || targets[0].ID != first.ID || targets[1].ID != second.ID || targets[0].Type != TypeDiscord {
		testInstance.Fatalf("targets = %+v, want only [%d %d]", targets, first.ID, second.ID)
	}
	if forOther, otherErr := fix.service.SelectedTargets(ctx, appB, first.PersonHash, ids); otherErr != nil || len(forOther) != 0 {
		testInstance.Fatalf("other app targets = %+v, %v, want none", forOther, otherErr)
	}
	if none, noneErr := fix.service.SelectedTargets(ctx, appA, first.PersonHash, nil); noneErr != nil || len(none) != 0 {
		testInstance.Fatalf("no ids targets = %+v, %v, want none", none, noneErr)
	}
}

func TestDisablePersonalOnlyTransitionsEnabledRows(testInstance *testing.T) {
	ctx := context.Background()
	fix := newFixture(testInstance)
	view := fix.create(testInstance, aliceEmail)

	enabledView := fix.create(testInstance, bobEmail)
	if disableErr := fix.service.DisablePersonal(ctx, enabledView.ID, "Endpoint returned 410 Gone."); disableErr != nil {
		testInstance.Fatalf("DisablePersonal(enabled): %v", disableErr)
	}
	enabledRow := fix.store.rows[enabledView.ID]
	if enabledRow.Enabled || enabledRow.DisabledBy == nil || *enabledRow.DisabledBy != DisabledBySystem ||
		enabledRow.DisabledReason == nil || *enabledRow.DisabledReason != "Endpoint returned 410 Gone." {
		testInstance.Fatalf("enabled row after DisablePersonal = %+v, want system-disabled with the reason", enabledRow)
	}

	operator := DisabledByOperator
	reason := "operator reason"
	row := fix.store.rows[view.ID]
	row.Enabled, row.DisabledBy, row.DisabledReason = false, &operator, &reason

	if disableErr := fix.service.DisablePersonal(ctx, view.ID, "Endpoint returned 410 Gone."); disableErr != nil {
		testInstance.Fatalf("DisablePersonal: %v", disableErr)
	}
	if *row.DisabledBy != DisabledByOperator || *row.DisabledReason != reason {
		testInstance.Fatalf("operator disable overwritten: %v %q", *row.DisabledBy, *row.DisabledReason)
	}
	if absentErr := fix.service.DisablePersonal(ctx, 999, "x"); absentErr != nil {
		testInstance.Fatalf("absent id err = %v, want nil", absentErr)
	}
}

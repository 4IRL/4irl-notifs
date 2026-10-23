package heartbeat

import (
	"context"
	"slices"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// The app-scoped methods below serve /v1/apps/{app_id}/heartbeats. appID is the caller's identity from its
// app key, never a value the request body supplies. Every check they create is owner_kind 'app' with
// owner_app_id appID and app_id appID (the ping identity), so an app can only ever create checks under its
// own id; every one of them confines the call to checks that app owns: a check owned by someone else (the
// operator or another app) is ErrCheckNotFound, exactly like an absent one, so an app cannot probe for
// existence. The destinations a check alerts must be usable by the app (operator-owned, or the app's own);
// the store enforces that atomically with the write and reports anything else as
// registry.ErrUnknownDestination. The checker and its alert flush are unchanged.

// AppCheckInput is the request to create a check as an app. It carries no app_id and no owner: both are the
// calling app's.
type AppCheckInput struct {
	Name             string
	PeriodSeconds    int
	GraceSeconds     int
	DestinationNames []string
}

// CreateForApp validates and persists a check owned by app appID, in state new, its deadline clock starting
// now. The check's app_id and owner are set here from appID, explicitly: nothing in the input can make the
// row operator-owned or register it under another app's ping identity.
func (service *Service) CreateForApp(ctx context.Context, appID string, input AppCheckInput) (StoredCheck, error) {
	ownerAppID := appID
	check := NewCheck{
		AppID:            appID,
		Name:             input.Name,
		PeriodSeconds:    input.PeriodSeconds,
		GraceSeconds:     input.GraceSeconds,
		DestinationNames: slices.Clone(input.DestinationNames),
		OwnerKind:        registry.OwnerApp,
		OwnerAppID:       &ownerAppID,
	}
	// ValidateNewCheck validates the app id too, so a malformed caller id never reaches the store.
	if validateErr := ValidateNewCheck(check); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	created, createErr := service.store.Create(ctx, check, service.now())
	if createErr != nil {
		return StoredCheck{}, createErr
	}
	service.logger.InfoContext(ctx, "app heartbeat check created",
		"app_id", appID, "check_id", created.ID, "check", created.Name)
	return created, nil
}

// ListForApp returns the checks app appID owns, ordered by name, never nil.
func (service *Service) ListForApp(ctx context.Context, appID string) ([]StoredCheck, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return nil, validateErr
	}
	stored, listErr := service.store.ListByOwner(ctx, appID)
	if listErr != nil {
		return nil, listErr
	}
	checks := make([]StoredCheck, 0, len(stored))
	for _, check := range stored {
		// Defence in depth: the store already filtered by owner; a row that is not this app's is
		// dropped rather than listed.
		if !ownedByApp(check, appID) {
			continue
		}
		checks = append(checks, check)
	}
	return checks, nil
}

// GetForApp returns the check when app appID owns it, and ErrCheckNotFound when it is absent or owned by
// anyone else.
func (service *Service) GetForApp(ctx context.Context, appID string, id int64) (StoredCheck, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	stored, getErr := service.store.Get(ctx, id)
	if getErr != nil {
		return StoredCheck{}, getErr
	}
	if !ownedByApp(stored, appID) {
		// The caller sees the same 404 as for an absent check; the log is where an operator can tell an
		// app probing another owner's ids from a plain miss.
		service.logger.WarnContext(ctx, "app heartbeat check read refused: not the caller's check",
			"app_id", appID, "check_id", id)
		return StoredCheck{}, ErrCheckNotFound
	}
	return stored, nil
}

// PatchForApp is Patch for a check app appID owns. The owner is part of the store's row lock, so a check
// owned by anyone else is ErrCheckNotFound and is never changed. A patch naming no field changes nothing and
// returns the current check (still only when the app owns it).
func (service *Service) PatchForApp(
	ctx context.Context, appID string, id int64, patch CheckPatch,
) (StoredCheck, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	if patch.IsEmpty() {
		return service.GetForApp(ctx, appID, id)
	}
	patched, patchErr := service.store.PatchOwnedBy(ctx, id, appID, patch, service.now())
	if patchErr != nil {
		return StoredCheck{}, patchErr
	}
	service.logger.InfoContext(ctx, "app heartbeat check updated", "app_id", appID, "check_id", id)
	return patched, nil
}

// DeleteForApp removes the check when app appID owns it, in one statement that carries the owner. It
// returns ErrCheckNotFound when the check is absent or owned by anyone else.
func (service *Service) DeleteForApp(ctx context.Context, appID string, id int64) error {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return validateErr
	}
	deleted, deleteErr := service.store.DeleteOwnedBy(ctx, id, appID)
	if deleteErr != nil {
		return deleteErr
	}
	if !deleted {
		return ErrCheckNotFound
	}
	service.logger.InfoContext(ctx, "app heartbeat check deleted", "app_id", appID, "check_id", id)
	return nil
}

// PauseForApp pauses the check app appID owns and clears any unflushed alert.
func (service *Service) PauseForApp(ctx context.Context, appID string, id int64) (StoredCheck, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	paused, pauseErr := service.store.PauseOwnedBy(ctx, id, appID, service.now())
	if pauseErr != nil {
		return StoredCheck{}, pauseErr
	}
	// A paused check raises no alert, so an app pausing one is worth an audit line.
	service.logger.InfoContext(ctx, "app heartbeat check paused", "app_id", appID, "check_id", id)
	return paused, nil
}

// ResumeForApp resumes the paused check app appID owns and restarts its deadline clock.
func (service *Service) ResumeForApp(ctx context.Context, appID string, id int64) (StoredCheck, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	resumed, resumeErr := service.store.ResumeOwnedBy(ctx, id, appID, service.now())
	if resumeErr != nil {
		return StoredCheck{}, resumeErr
	}
	service.logger.InfoContext(ctx, "app heartbeat check resumed", "app_id", appID, "check_id", id)
	return resumed, nil
}

// ownedByApp reports whether check is owned by the app appID. It fails closed: an empty appID, an
// operator-owned check, an unknown owner kind and an app owner with no owner id are all not owned.
func ownedByApp(check StoredCheck, appID string) bool {
	return appID != "" &&
		check.OwnerKind == registry.OwnerApp &&
		check.OwnerAppID != nil &&
		*check.OwnerAppID == appID
}

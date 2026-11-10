package personal

import (
	"context"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// ResolvePersonal re-resolves a personal destination at send time for a
// delivery of appID addressed to personHash. It returns ErrNotFound when the row
// is absent or belongs to another person (no oracle), ErrDisabled when it is
// disabled and ErrNotOptedIn when appID has no opt-in. A row whose registrar
// was removed is still resolved: only the opt-in and enabled flag gate sending.
// Any other error means the state is unknown.
func (service *Service) ResolvePersonal(
	ctx context.Context, id int64, appID, personHash string,
) (registry.ResolvedDestination, error) {
	stored, getErr := service.store.Get(ctx, id)
	if getErr != nil {
		return registry.ResolvedDestination{}, getErr
	}
	if stored.PersonHash != personHash {
		return registry.ResolvedDestination{}, ErrNotFound
	}
	if !stored.Enabled {
		return registry.ResolvedDestination{}, ErrDisabled
	}
	optedIn, optErr := service.store.IsOptedIn(ctx, appID, id)
	if optErr != nil {
		return registry.ResolvedDestination{}, optErr
	}
	if !optedIn {
		return registry.ResolvedDestination{}, ErrNotOptedIn
	}
	secrets, openErr := OpenSecrets(service.sealer, id, stored.SealedSecrets)
	if openErr != nil {
		return registry.ResolvedDestination{}, openErr
	}
	return registry.ResolvedDestination{
		ID:      stored.ID,
		Name:    PersonalDeliveryName(stored.ID),
		Type:    stored.Type,
		Config:  cloneMap(stored.Config),
		Secrets: secrets,
	}, nil
}

// Target is one personal destination a message fans out to: just the id (which
// names the delivery) and the sink type. No secrets are read to build it.
type Target struct {
	ID   int64
	Type string
}

// OptedInTargets returns the person's enabled destinations that appID is opted
// in for, ordered by id. The caller snapshots the result at submit time; the
// worker rechecks enabled and opt-in at send time.
func (service *Service) OptedInTargets(ctx context.Context, appID, personHash string) ([]Target, error) {
	rows, listErr := service.store.ListEnabledOptedIn(ctx, appID, personHash)
	if listErr != nil {
		return nil, listErr
	}
	targets := make([]Target, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, Target{ID: row.ID, Type: row.Type})
	}
	return targets, nil
}

// SelectedTargets returns only the named destinations that are the person's,
// enabled and opted in for appID, ordered by id. Ineligible ids are absent, so
// the caller compares len(result) with its distinct id count.
func (service *Service) SelectedTargets(ctx context.Context, appID, personHash string, ids []int64) ([]Target, error) {
	if len(ids) == 0 {
		return []Target{}, nil
	}
	rows, listErr := service.store.ListEnabledOptedInByIDs(ctx, appID, personHash, ids)
	if listErr != nil {
		return nil, listErr
	}
	targets := make([]Target, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, Target{ID: row.ID, Type: row.Type})
	}
	return targets, nil
}

// DisablePersonal disables an enabled personal destination as 'system' with
// reason. Only an enabled row transitions, so an operator or app disable is
// never overwritten. An absent row is not an error.
func (service *Service) DisablePersonal(ctx context.Context, id int64, reason string) error {
	changed, storeErr := service.store.DisableBySystem(ctx, id, reason)
	if storeErr != nil {
		return storeErr
	}
	if changed {
		service.logger.WarnContext(ctx, "personal destination disabled by the system",
			"id", id, "reason", reason)
	}
	return nil
}

package registry

import (
	"context"
	"maps"
	"strings"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
)

// DestinationURLChecker is the registration-time SSRF check an app-scoped destination write runs on the
// app-supplied URL. *URLChecker satisfies it; service tests inject a stub.
type DestinationURLChecker interface {
	// CheckDestinationSecrets refuses a destination whose URL secret does not resolve to public addresses.
	// It may spend seconds on DNS, so callers invoke it before any transaction or row lock.
	CheckDestinationSecrets(ctx context.Context, destinationType string, secrets map[string]string) error
}

// Compile-time check that the real checker satisfies the service's interface.
var _ DestinationURLChecker = (*URLChecker)(nil)

// Option configures a Service at construction.
type Option func(*Service)

// WithURLChecker sets the registration-time URL checker app-scoped destination writes use. Callers pass an
// untyped nil (or omit the option) when there is none: a typed-nil pointer would be a non-nil interface.
func WithURLChecker(checker DestinationURLChecker) Option {
	return func(service *Service) { service.urlChecker = checker }
}

// The app-scoped destination methods below serve /v1/apps/{app_id}/destinations. appID is the caller's
// identity from its app key, never a value the request body supplies. Every one of them confines the call to
// destinations that app owns: a destination owned by someone else (the operator or another app) is
// ErrDestinationNotFound, exactly like an absent one, so an app cannot probe for existence. They reuse the
// admin path's secret handling (seal, mask and open stay in sealSecrets, MaskSecrets and openSecrets).

// CreateAppDestination validates and stores a destination owned by app appID. The name must start with
// "<appID>_" and an ntfy topic with "<appID>-". An app-supplied URL (webhook url, discord webhook_url) must
// resolve to public addresses, checked before anything is written. The row is always owner_kind 'app' with
// owner_app_id appID, set here explicitly: nothing in the input can make it operator-owned.
func (service *Service) CreateAppDestination(
	ctx context.Context, appID string, input DestinationInput,
) (DestinationView, error) {
	if err := validateOwnerAppID(appID); err != nil {
		return DestinationView{}, err
	}
	if err := ValidateDestinationName(input.Name); err != nil {
		return DestinationView{}, err
	}
	if err := ValidateDestinationType(input.Type); err != nil {
		return DestinationView{}, err
	}
	if err := validateAppDestinationName(appID, input.Name); err != nil {
		return DestinationView{}, err
	}
	config := cloneMap(input.Config)
	secrets := cloneMap(input.Secrets)
	if err := ValidateDestinationFields(input.Type, config, secrets); err != nil {
		return DestinationView{}, err
	}
	if err := validateAppTopic(appID, input.Type, config); err != nil {
		return DestinationView{}, err
	}
	// The DNS lookup can take seconds, so it runs before the insert, never inside a transaction.
	if err := service.checkAppDestinationURL(ctx, appID, input.Name, input.Type, secrets); err != nil {
		return DestinationView{}, err
	}
	sealed, sealErr := service.sealSecrets(input.Name, secrets)
	if sealErr != nil {
		return DestinationView{}, sealErr
	}
	ownerAppID := appID
	stored, storeErr := service.store.InsertDestination(ctx, StoredDestination{
		Name:          input.Name,
		Type:          input.Type,
		Config:        config,
		SealedSecrets: sealed,
		Enabled:       input.Enabled == nil || *input.Enabled,
		OwnerKind:     OwnerApp,
		OwnerAppID:    &ownerAppID,
	})
	if storeErr != nil {
		return DestinationView{}, storeErr
	}
	service.logger.InfoContext(ctx, "app destination created",
		"app_id", appID, "destination", stored.Name, "type", stored.Type)
	return service.destinationView(ctx, stored), nil
}

// ListAppDestinations returns the destinations app appID owns, with masked secrets, never nil.
func (service *Service) ListAppDestinations(ctx context.Context, appID string) ([]DestinationView, error) {
	if err := validateOwnerAppID(appID); err != nil {
		return nil, err
	}
	stored, storeErr := service.store.ListDestinationsByOwner(ctx, appID)
	if storeErr != nil {
		return nil, storeErr
	}
	views := make([]DestinationView, 0, len(stored))
	for _, destination := range stored {
		// Defence in depth: the store already filtered by owner; a row that is not this app's is
		// dropped rather than listed.
		if !ownedByApp(destination, appID) {
			continue
		}
		views = append(views, service.destinationView(ctx, destination))
	}
	return views, nil
}

// GetAppDestination returns the named destination with masked secrets when app appID owns it, and
// ErrDestinationNotFound when it is absent or owned by anyone else.
func (service *Service) GetAppDestination(ctx context.Context, appID, name string) (DestinationView, error) {
	stored, getErr := service.getOwnedDestination(ctx, appID, name)
	if getErr != nil {
		return DestinationView{}, getErr
	}
	return service.destinationView(ctx, stored), nil
}

// UpdateAppDestination applies patch to the named destination when app appID owns it. When the patch
// changes the effective URL secret (url or webhook_url), the new URL is validated and SSRF-checked first,
// outside any lock; the owner check, the merge, the re-validation and the write then run atomically in the
// store's row lock, where the owner is checked again. A topic change of an ntfy destination must keep the
// "<appID>-" prefix.
func (service *Service) UpdateAppDestination(
	ctx context.Context, appID, name string, patch DestinationPatch,
) (DestinationView, error) {
	current, getErr := service.getOwnedDestination(ctx, appID, name)
	if getErr != nil {
		return DestinationView{}, getErr
	}
	if err := service.checkPatchedURL(ctx, appID, current, patch); err != nil {
		return DestinationView{}, err
	}
	stored, storeErr := service.store.UpdateDestination(ctx, name,
		func(locked StoredDestination) (StoredDestination, error) {
			if !sameOwnedRow(locked, current, appID) {
				return StoredDestination{}, ErrDestinationNotFound
			}
			if _, touchesTopic := patch.Config["topic"]; touchesTopic {
				if err := validateAppTopic(appID, locked.Type, patch.Config); err != nil {
					return StoredDestination{}, err
				}
			}
			return service.applyDestinationPatch(locked, patch)
		})
	if storeErr != nil {
		return DestinationView{}, storeErr
	}
	service.logger.InfoContext(ctx, "app destination updated", "app_id", appID, "destination", stored.Name)
	return service.destinationView(ctx, stored), nil
}

// DeleteAppDestination removes the named destination when app appID owns it, in one statement that carries
// the owner. It returns ErrDestinationNotFound when the destination is absent or owned by anyone else, and
// ErrDestinationInUse when a route or heartbeat check still references it.
func (service *Service) DeleteAppDestination(ctx context.Context, appID, name string) error {
	if err := validateOwnerAppID(appID); err != nil {
		return err
	}
	deleted, storeErr := service.store.DeleteDestinationOwnedBy(ctx, name, appID)
	if storeErr != nil {
		return storeErr
	}
	if !deleted {
		return ErrDestinationNotFound
	}
	service.logger.InfoContext(ctx, "app destination deleted", "app_id", appID, "destination", name)
	return nil
}

// RotateAppWebhookSecret rotates the signing secret of the named webhook destination when app appID owns it.
// The stored URL is re-checked against the registration-time SSRF rule first (outside any lock), so a host
// that has since moved to a private address cannot keep being signed for; the rotation itself then runs in
// the store's row lock, where the owner is checked again. See RotateWebhookSecret for the rotation contract.
func (service *Service) RotateAppWebhookSecret(
	ctx context.Context, appID, name string, overlapHours int,
) (DestinationView, string, error) {
	current, getErr := service.getOwnedDestination(ctx, appID, name)
	if getErr != nil {
		return DestinationView{}, "", getErr
	}
	if current.Type == TypeWebhook {
		secrets, openErr := service.openSecrets(current.Name, current.SealedSecrets)
		if openErr != nil {
			return DestinationView{}, "", openErr
		}
		if err := service.checkAppDestinationURL(ctx, appID, name, current.Type, secrets); err != nil {
			return DestinationView{}, "", err
		}
	}
	var newSecret string
	stored, storeErr := service.store.UpdateDestination(ctx, name,
		func(locked StoredDestination) (StoredDestination, error) {
			if !sameOwnedRow(locked, current, appID) {
				return StoredDestination{}, ErrDestinationNotFound
			}
			rotated, secret, rotateErr := service.rotateSecret(locked, overlapHours)
			if rotateErr != nil {
				return StoredDestination{}, rotateErr
			}
			newSecret = secret
			return rotated, nil
		})
	if storeErr != nil {
		return DestinationView{}, "", storeErr
	}
	service.logger.InfoContext(ctx, "app destination secret rotated", "app_id", appID, "destination", stored.Name)
	return service.destinationView(ctx, stored), newSecret, nil
}

// getOwnedDestination loads the named destination and returns ErrDestinationNotFound unless app appID
// owns it. A foreign destination and an absent one are the same error, so the caller cannot tell them apart.
func (service *Service) getOwnedDestination(ctx context.Context, appID, name string) (StoredDestination, error) {
	if err := validateOwnerAppID(appID); err != nil {
		return StoredDestination{}, err
	}
	stored, storeErr := service.store.GetDestination(ctx, name)
	if storeErr != nil {
		return StoredDestination{}, storeErr
	}
	if !ownedByApp(stored, appID) {
		return StoredDestination{}, ErrDestinationNotFound
	}
	return stored, nil
}

// checkPatchedURL runs the registration-time URL check for a patch, but only when the patch changes the
// effective URL secret of the destination. It validates the merged fields first (the discord host allow-list
// lives there), so only a statically valid URL is ever resolved.
func (service *Service) checkPatchedURL(
	ctx context.Context, appID string, current StoredDestination, patch DestinationPatch,
) error {
	urlKey := destinationURLSecretKey(current.Type)
	if urlKey == "" {
		return nil
	}
	newURL, patchesURL := patch.Secrets[urlKey]
	if !patchesURL {
		return nil
	}
	secrets, openErr := service.openSecrets(current.Name, current.SealedSecrets)
	if openErr != nil {
		return openErr
	}
	if secrets[urlKey] == newURL {
		return nil
	}
	config := cloneMap(current.Config)
	maps.Copy(config, patch.Config)
	maps.Copy(secrets, patch.Secrets)
	if err := ValidateDestinationFields(current.Type, config, secrets); err != nil {
		return err
	}
	return service.checkAppDestinationURL(ctx, appID, current.Name, current.Type, secrets)
}

// checkAppDestinationURL runs the registration-time SSRF check on the effective secrets of an app-owned
// destination. A type without a URL (ntfy) needs none. With no checker configured a URL-bearing type fails
// closed with ErrURLCheckUnavailable.
func (service *Service) checkAppDestinationURL(
	ctx context.Context, appID, name, destinationType string, secrets map[string]string,
) error {
	if destinationURLSecretKey(destinationType) == "" {
		return nil
	}
	if service.urlChecker == nil {
		service.logger.ErrorContext(ctx, "app destination url check skipped: no checker configured; refused",
			"app_id", appID, "destination", name, "type", destinationType)
		return ErrURLCheckUnavailable
	}
	if err := service.urlChecker.CheckDestinationSecrets(ctx, destinationType, secrets); err != nil {
		// The checker's message names the field, never the URL; the URL is not logged here either.
		service.logger.WarnContext(ctx, "app destination url refused",
			"app_id", appID, "destination", name, "type", destinationType, "reason", err.Error())
		return err
	}
	return nil
}

// ownedByApp reports whether destination is owned by the app appID. It fails closed: an empty appID, an
// operator-owned destination, an unknown owner kind and an app owner with no owner id are all not owned.
func ownedByApp(destination StoredDestination, appID string) bool {
	return appID != "" &&
		destination.OwnerKind == OwnerApp &&
		destination.OwnerAppID != nil &&
		*destination.OwnerAppID == appID
}

// sameOwnedRow reports whether the row locked under the store's row lock is still the app's row that the
// pre-lock check (current) decided on: same owner, same id and same type. A row deleted and re-created
// between the pre-check and the lock is a different row, so no decision made on the old one may be applied.
func sameOwnedRow(locked, current StoredDestination, appID string) bool {
	return ownedByApp(locked, appID) && locked.ID == current.ID && locked.Type == current.Type
}

// validateOwnerAppID refuses a caller app id that is not a well-formed app id, so an empty or hostile id
// never reaches a name-prefix comparison or a query.
func validateOwnerAppID(appID string) error {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return &ValidationError{Message: validateErr.Error()}
	}
	return nil
}

// validateAppDestinationName requires an app-owned destination name to start with "<appID>_" and carry at
// least one more character beyond it. That is stricter than the destinations owner-prefix CHECK, which
// allows the bare prefix.
func validateAppDestinationName(appID, name string) error {
	prefix := appID + "_"
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return &ValidationError{Message: "name must start with " + prefix}
	}
	return nil
}

// validateAppTopic requires the config.topic of an app-owned ntfy destination to start with "<appID>-". Any
// other destination type has no topic and passes.
func validateAppTopic(appID, destinationType string, config map[string]string) error {
	if destinationType != TypeNtfy {
		return nil
	}
	prefix := appID + "-"
	if topic := config["topic"]; !strings.HasPrefix(topic, prefix) || len(topic) == len(prefix) {
		return &ValidationError{Message: "config.topic must start with " + prefix}
	}
	return nil
}

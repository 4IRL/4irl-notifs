package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

const (
	// secretsAADPrefix prefixes the additional authenticated data of every sealed
	// destination secret blob. The destination name follows it, so a blob cannot
	// be moved onto another row.
	secretsAADPrefix = "delivery-api/destination/v1:"
	// secretsErrorKey is the masked-secrets key returned for a destination whose
	// sealed blob cannot be opened.
	secretsErrorKey = "error"
	// signingSecretBytes is the entropy of a generated webhook signing secret.
	signingSecretBytes = 32
)

// errMalformedSecrets means a sealed blob opened but did not hold a JSON object
// of string values. It deliberately carries no payload text.
var errMalformedSecrets = errors.New("sealed secrets are not a JSON object of strings")

// DestinationInput is the request to create a destination. Enabled nil means
// enabled.
type DestinationInput struct {
	Name    string
	Type    string
	Enabled *bool
	Config  map[string]string
	Secrets map[string]string
}

// DestinationPatch is a partial update of a destination. A nil field is kept:
// Enabled nil leaves the flag, Config nil keeps the stored config, and Secrets
// nil keeps every secret. A non-nil Config and a non-nil Secrets are each
// merged key by key into the stored map: listed keys are replaced, every other
// key is kept, and a key cannot be removed. A nil or empty map changes nothing.
type DestinationPatch struct {
	Enabled *bool
	Config  map[string]string
	Secrets map[string]string
}

// RouteInput is the request to create a route. Enabled nil means enabled.
type RouteInput struct {
	AppPattern     string
	ChannelPattern string
	Destinations   []string
	Enabled        *bool
}

// RoutePatch is a partial update of a route. A nil field is kept; a non-nil
// Destinations replaces the whole destination set.
type RoutePatch struct {
	AppPattern     *string
	ChannelPattern *string
	Destinations   []string
	Enabled        *bool
}

// Service orchestrates destination and route CRUD and route resolution. It is
// the only place where destination secrets exist in plaintext: they are sealed
// before they reach the Store, masked before they reach a client, and unsealed
// only by Resolve for the sinks.
type Service struct {
	store  Store
	sealer *seal.Sealer
	logger *slog.Logger
	// now is the clock behind secret-rotation expiry; tests replace it.
	now func() time.Time
}

// NewService builds a Service over store and sealer. A nil logger defaults to
// slog.Default().
func NewService(store Store, sealer *seal.Sealer, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, sealer: sealer, logger: logger, now: time.Now}
}

// CreateDestination validates, seals the secrets and stores a new destination.
// A validation failure returns a *ValidationError and never calls the store.
func (service *Service) CreateDestination(ctx context.Context, input DestinationInput) (DestinationView, error) {
	if err := ValidateDestinationName(input.Name); err != nil {
		return DestinationView{}, err
	}
	if err := ValidateDestinationType(input.Type); err != nil {
		return DestinationView{}, err
	}
	config := cloneMap(input.Config)
	secrets := cloneMap(input.Secrets)
	if err := ValidateDestinationFields(input.Type, config, secrets); err != nil {
		return DestinationView{}, err
	}
	sealed, sealErr := service.sealSecrets(input.Name, secrets)
	if sealErr != nil {
		return DestinationView{}, sealErr
	}
	stored, storeErr := service.store.InsertDestination(ctx, StoredDestination{
		Name:          input.Name,
		Type:          input.Type,
		Config:        config,
		SealedSecrets: sealed,
		Enabled:       input.Enabled == nil || *input.Enabled,
	})
	if storeErr != nil {
		return DestinationView{}, storeErr
	}
	return service.destinationView(ctx, stored), nil
}

// ListDestinations returns every destination with masked secrets. A row whose
// sealed blob cannot be opened is still listed, with an error-masked secrets
// map, and the failure is logged.
func (service *Service) ListDestinations(ctx context.Context) ([]DestinationView, error) {
	stored, storeErr := service.store.ListDestinations(ctx)
	if storeErr != nil {
		return nil, storeErr
	}
	views := make([]DestinationView, 0, len(stored))
	for _, destination := range stored {
		views = append(views, service.destinationView(ctx, destination))
	}
	return views, nil
}

// GetDestination returns the named destination with masked secrets, or
// ErrDestinationNotFound.
func (service *Service) GetDestination(ctx context.Context, name string) (DestinationView, error) {
	stored, storeErr := service.store.GetDestination(ctx, name)
	if storeErr != nil {
		return DestinationView{}, storeErr
	}
	return service.destinationView(ctx, stored), nil
}

// UpdateDestination applies patch to the named destination atomically in the
// store. A patch that touches the config or secrets opens the current secrets,
// merges per key, re-validates the result against the stored type and reseals
// when secrets changed; a patch of only Enabled never opens the secrets.
// Enabling a destination clears its disabled reason.
func (service *Service) UpdateDestination(
	ctx context.Context, name string, patch DestinationPatch,
) (DestinationView, error) {
	stored, storeErr := service.store.UpdateDestination(ctx, name,
		func(current StoredDestination) (StoredDestination, error) {
			return service.applyDestinationPatch(current, patch)
		})
	if storeErr != nil {
		return DestinationView{}, storeErr
	}
	return service.destinationView(ctx, stored), nil
}

// RotateWebhookSecret rotates the signing secret of the named webhook
// destination. Inside the store's row-locked update it moves signing_secret to
// signing_secret_previous, sets config.previous_secret_expires_at to now plus
// overlapHours, generates a new secret, re-validates the result and reseals it
// in that one write. It returns the masked view and the new plaintext secret,
// which exists nowhere else; the plaintext is returned only after the commit.
// A destination that is not a webhook returns ErrRotationUnsupported and a
// missing one ErrDestinationNotFound; nothing is written on any error.
func (service *Service) RotateWebhookSecret(
	ctx context.Context, name string, overlapHours int,
) (DestinationView, string, error) {
	var newSecret string
	stored, storeErr := service.store.UpdateDestination(ctx, name,
		func(current StoredDestination) (StoredDestination, error) {
			rotated, secret, rotateErr := service.rotateSecret(current, overlapHours)
			if rotateErr != nil {
				return StoredDestination{}, rotateErr
			}
			newSecret = secret
			return rotated, nil
		})
	if storeErr != nil {
		return DestinationView{}, "", storeErr
	}
	return service.destinationView(ctx, stored), newSecret, nil
}

// rotateSecret is the RotateWebhookSecret mutate step: it runs inside the
// store's row lock.
func (service *Service) rotateSecret(
	current StoredDestination, overlapHours int,
) (StoredDestination, string, error) {
	if current.Type != TypeWebhook {
		return StoredDestination{}, "", ErrRotationUnsupported
	}
	secrets, openErr := service.openSecrets(current.Name, current.SealedSecrets)
	if openErr != nil {
		return StoredDestination{}, "", openErr
	}
	newSecret, generateErr := generateSigningSecret()
	if generateErr != nil {
		return StoredDestination{}, "", generateErr
	}
	config := cloneMap(current.Config)
	secrets["signing_secret_previous"] = secrets["signing_secret"]
	secrets["signing_secret"] = newSecret
	expiresAt := service.now().UTC().Add(time.Duration(overlapHours) * time.Hour)
	config["previous_secret_expires_at"] = expiresAt.Format(time.RFC3339)
	if err := ValidateDestinationFields(current.Type, config, secrets); err != nil {
		return StoredDestination{}, "", err
	}
	sealed, sealErr := service.sealSecrets(current.Name, secrets)
	if sealErr != nil {
		return StoredDestination{}, "", sealErr
	}
	current.Config = config
	current.SealedSecrets = sealed
	return current, newSecret, nil
}

// generateSigningSecret returns 32 random bytes as unpadded base64url (43
// characters).
func generateSigningSecret() (string, error) {
	random := make([]byte, signingSecretBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate signing secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

// DisableDestination disables the named destination, but only while its ID is
// still destinationID, and records reason as its disabled reason. It is for the
// delivery path (a webhook answering 410 Gone), not the admin API: it neither
// opens nor reseals the secrets. The ID check keeps a destination deleted and
// re-created under the same name since the caller resolved it from being
// disabled by the old endpoint's answer. A missing destination, or one whose ID
// no longer matches, returns ErrDestinationNotFound unchanged and writes
// nothing.
func (service *Service) DisableDestination(
	ctx context.Context, name string, destinationID int64, reason string,
) error {
	_, storeErr := service.store.UpdateDestination(ctx, name,
		func(current StoredDestination) (StoredDestination, error) {
			if current.ID != destinationID {
				return StoredDestination{}, ErrDestinationNotFound
			}
			current.Enabled = false
			current.DisabledReason = &reason
			return current, nil
		})
	return storeErr
}

// DeleteDestination removes the named destination and reports whether it
// existed. A destination a route still references returns ErrDestinationInUse.
func (service *Service) DeleteDestination(ctx context.Context, name string) (bool, error) {
	return service.store.DeleteDestination(ctx, name)
}

// CreateRoute validates the patterns and destination names, then stores the
// route. A validation failure returns a *ValidationError and never calls the
// store.
func (service *Service) CreateRoute(ctx context.Context, input RouteInput) (RouteView, error) {
	if err := ValidateAppPattern(input.AppPattern); err != nil {
		return RouteView{}, err
	}
	if err := ValidateChannelPattern(input.ChannelPattern); err != nil {
		return RouteView{}, err
	}
	if err := ValidateRouteDestinations(input.Destinations); err != nil {
		return RouteView{}, err
	}
	stored, storeErr := service.store.InsertRoute(ctx, StoredRoute{
		AppPattern:       input.AppPattern,
		ChannelPattern:   input.ChannelPattern,
		DestinationNames: slices.Clone(input.Destinations),
		Enabled:          input.Enabled == nil || *input.Enabled,
	})
	if storeErr != nil {
		return RouteView{}, storeErr
	}
	return routeView(stored), nil
}

// ListRoutes returns every route ordered by id, never nil.
func (service *Service) ListRoutes(ctx context.Context) ([]RouteView, error) {
	stored, storeErr := service.store.ListRoutes(ctx)
	if storeErr != nil {
		return nil, storeErr
	}
	views := make([]RouteView, 0, len(stored))
	for _, route := range stored {
		views = append(views, routeView(route))
	}
	return views, nil
}

// GetRoute returns the route, or ErrRouteNotFound.
func (service *Service) GetRoute(ctx context.Context, id int64) (RouteView, error) {
	stored, storeErr := service.store.GetRoute(ctx, id)
	if storeErr != nil {
		return RouteView{}, storeErr
	}
	return routeView(stored), nil
}

// UpdateRoute validates the fields present in patch before the store is called,
// then applies them atomically. Destinations nil keeps the current set.
func (service *Service) UpdateRoute(ctx context.Context, id int64, patch RoutePatch) (RouteView, error) {
	if patch.AppPattern != nil {
		if err := ValidateAppPattern(*patch.AppPattern); err != nil {
			return RouteView{}, err
		}
	}
	if patch.ChannelPattern != nil {
		if err := ValidateChannelPattern(*patch.ChannelPattern); err != nil {
			return RouteView{}, err
		}
	}
	if patch.Destinations != nil {
		if err := ValidateRouteDestinations(patch.Destinations); err != nil {
			return RouteView{}, err
		}
	}
	stored, storeErr := service.store.UpdateRoute(ctx, id, func(current StoredRoute) (StoredRoute, error) {
		if patch.AppPattern != nil {
			current.AppPattern = *patch.AppPattern
		}
		if patch.ChannelPattern != nil {
			current.ChannelPattern = *patch.ChannelPattern
		}
		if patch.Destinations != nil {
			current.DestinationNames = slices.Clone(patch.Destinations)
		}
		if patch.Enabled != nil {
			current.Enabled = *patch.Enabled
		}
		return current, nil
	})
	if storeErr != nil {
		return RouteView{}, storeErr
	}
	return routeView(stored), nil
}

// DeleteRoute removes the route and reports whether it existed.
func (service *Service) DeleteRoute(ctx context.Context, id int64) (bool, error) {
	return service.store.DeleteRoute(ctx, id)
}

// Resolve returns the destinations an envelope fires, with secrets unsealed.
// With a destinationName it returns exactly that destination: a missing one is
// ErrUnknownDestination and a disabled one ErrDestinationDisabled. With an
// empty destinationName it returns the enabled destinations of every enabled
// route matching (appID, channel), de-duplicated and ordered by name; no match
// is an empty non-nil slice. An unseal failure returns an error naming the
// destination and never the secret. The result carries plaintext secrets and
// must go to sinks only.
func (service *Service) Resolve(
	ctx context.Context, appID, destinationName, channel string,
) ([]ResolvedDestination, error) {
	if destinationName != "" {
		return service.resolveNamed(ctx, destinationName)
	}
	routes, routesErr := service.store.ListEnabledRoutes(ctx)
	if routesErr != nil {
		return nil, routesErr
	}
	destinationIDs := MatchRoutes(routes, appID, channel)
	resolved := []ResolvedDestination{}
	if len(destinationIDs) == 0 {
		return resolved, nil
	}
	stored, storeErr := service.store.GetDestinationsByID(ctx, destinationIDs)
	if storeErr != nil {
		return nil, storeErr
	}
	slices.SortStableFunc(stored, func(left, right StoredDestination) int {
		return strings.Compare(left.Name, right.Name)
	})
	for _, destination := range stored {
		if !destination.Enabled {
			continue
		}
		resolvedDestination, resolveErr := service.unseal(destination)
		if resolveErr != nil {
			return nil, resolveErr
		}
		resolved = append(resolved, resolvedDestination)
	}
	return resolved, nil
}

// resolveNamed resolves the one destination an envelope names explicitly.
func (service *Service) resolveNamed(ctx context.Context, name string) ([]ResolvedDestination, error) {
	stored, storeErr := service.store.GetDestination(ctx, name)
	if storeErr != nil {
		if errors.Is(storeErr, ErrDestinationNotFound) {
			return nil, ErrUnknownDestination
		}
		return nil, storeErr
	}
	if !stored.Enabled {
		return nil, ErrDestinationDisabled
	}
	resolved, resolveErr := service.unseal(stored)
	if resolveErr != nil {
		return nil, resolveErr
	}
	return []ResolvedDestination{resolved}, nil
}

// unseal opens a stored destination's secrets into a ResolvedDestination.
func (service *Service) unseal(stored StoredDestination) (ResolvedDestination, error) {
	secrets, openErr := service.openSecrets(stored.Name, stored.SealedSecrets)
	if openErr != nil {
		return ResolvedDestination{}, openErr
	}
	return ResolvedDestination{
		ID:      stored.ID,
		Name:    stored.Name,
		Type:    stored.Type,
		Config:  cloneMap(stored.Config),
		Secrets: secrets,
	}, nil
}

// applyDestinationPatch is the UpdateDestination mutate step: it runs inside
// the store's row lock.
func (service *Service) applyDestinationPatch(
	current StoredDestination, patch DestinationPatch,
) (StoredDestination, error) {
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
		if *patch.Enabled {
			current.DisabledReason = nil
		}
	}
	if patch.Config == nil && patch.Secrets == nil {
		return current, nil
	}

	secrets, openErr := service.openSecrets(current.Name, current.SealedSecrets)
	if openErr != nil {
		return StoredDestination{}, openErr
	}
	config := cloneMap(current.Config)
	maps.Copy(config, patch.Config)
	maps.Copy(secrets, patch.Secrets)
	if err := ValidateDestinationFields(current.Type, config, secrets); err != nil {
		return StoredDestination{}, err
	}
	current.Config = config
	if len(patch.Secrets) > 0 {
		sealed, sealErr := service.sealSecrets(current.Name, secrets)
		if sealErr != nil {
			return StoredDestination{}, sealErr
		}
		current.SealedSecrets = sealed
	}
	return current, nil
}

// destinationAAD returns the additional authenticated data that binds a sealed
// blob to the destination name.
func destinationAAD(name string) []byte {
	return []byte(secretsAADPrefix + name)
}

// sealSecrets JSON-encodes secrets and seals them for the named destination.
func (service *Service) sealSecrets(name string, secrets map[string]string) ([]byte, error) {
	plaintext, marshalErr := json.Marshal(secrets)
	if marshalErr != nil {
		return nil, fmt.Errorf("encode secrets of destination %q", name)
	}
	sealed, sealErr := service.sealer.Seal(plaintext, destinationAAD(name))
	if sealErr != nil {
		return nil, fmt.Errorf("seal secrets of destination %q: %w", name, sealErr)
	}
	return sealed, nil
}

// openSecrets opens and decodes a sealed blob. Its errors name the destination
// and wrap only the seal sentinels, never plaintext or payload text.
func (service *Service) openSecrets(name string, sealed []byte) (map[string]string, error) {
	plaintext, openErr := service.sealer.Open(sealed, destinationAAD(name))
	if openErr != nil {
		return nil, fmt.Errorf("open secrets of destination %q: %w", name, openErr)
	}
	secrets := map[string]string{}
	if decodeErr := json.Unmarshal(plaintext, &secrets); decodeErr != nil {
		return nil, fmt.Errorf("open secrets of destination %q: %w", name, errMalformedSecrets)
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	return secrets, nil
}

// destinationView builds the public view of stored: masked secrets, and an
// error-masked map (plus a logged error) when the blob cannot be opened.
func (service *Service) destinationView(ctx context.Context, stored StoredDestination) DestinationView {
	maskedSecrets := map[string]string{secretsErrorKey: maskedValue}
	secrets, openErr := service.openSecrets(stored.Name, stored.SealedSecrets)
	if openErr != nil {
		service.logger.ErrorContext(ctx, "destination secrets could not be opened",
			"destination", stored.Name, "error", openErr)
	} else {
		maskedSecrets = MaskSecrets(secrets)
	}
	return DestinationView{
		Name:           stored.Name,
		Type:           stored.Type,
		Enabled:        stored.Enabled,
		DisabledReason: stored.DisabledReason,
		Config:         cloneMap(stored.Config),
		Secrets:        maskedSecrets,
		CreatedAt:      stored.CreatedAt,
		UpdatedAt:      stored.UpdatedAt,
	}
}

// routeView builds the public view of a stored route, never with nil
// destinations.
func routeView(stored StoredRoute) RouteView {
	destinations := append([]string{}, stored.DestinationNames...)
	return RouteView{
		ID:             stored.ID,
		AppPattern:     stored.AppPattern,
		ChannelPattern: stored.ChannelPattern,
		Destinations:   destinations,
		Enabled:        stored.Enabled,
		CreatedAt:      stored.CreatedAt,
		UpdatedAt:      stored.UpdatedAt,
	}
}

// cloneMap copies values into a new non-nil map, so a nil input never marshals
// as JSON null and callers never share a map with the service.
func cloneMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	maps.Copy(cloned, values)
	return cloned
}

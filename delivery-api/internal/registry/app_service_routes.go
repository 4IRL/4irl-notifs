package registry

import (
	"context"
	"slices"
)

// errMsgAppPatternNotOwn is the client-safe message for an app-scoped route whose app_pattern is anything
// but the calling app's own id (a wildcard and another app's id included).
const errMsgAppPatternNotOwn = "app_pattern must be the app's own id"

// The app-scoped route methods below serve /v1/apps/{app_id}/routes. appID is the caller's identity from its
// app key, never a value the request body supplies. Every route they create is owner_kind 'app' with
// owner_app_id appID and an app_pattern equal to appID, so it can only ever match that app's own messages,
// and every one of them confines the call to routes that app owns: a route owned by someone else (the
// operator or another app) is ErrRouteNotFound, exactly like an absent one, so an app cannot probe for
// existence. The destinations a route links must be usable by the app (operator-owned, or the app's own);
// the store enforces that atomically with the write and reports anything else as ErrUnknownDestination.

// CreateAppRoute validates and stores a route owned by app appID. input.AppPattern may be empty (it then
// defaults to appID) or equal appID; anything else, a wildcard included, is a *ValidationError. The row is
// always owner_kind 'app' with owner_app_id appID, set here explicitly: nothing in the input can make it
// operator-owned or match another app.
func (service *Service) CreateAppRoute(ctx context.Context, appID string, input RouteInput) (RouteView, error) {
	if err := validateOwnerAppID(appID); err != nil {
		return RouteView{}, err
	}
	if input.AppPattern != "" && input.AppPattern != appID {
		return RouteView{}, &ValidationError{Message: errMsgAppPatternNotOwn}
	}
	// The pattern is the app's own id, which must also pass the admin path's app_pattern rules (the reserved
	// id "everyone" is refused there, and the database CHECK does not enforce that).
	if err := ValidateAppPattern(appID); err != nil {
		return RouteView{}, err
	}
	if err := ValidateChannelPattern(input.ChannelPattern); err != nil {
		return RouteView{}, err
	}
	if err := ValidateRouteDestinations(input.Destinations); err != nil {
		return RouteView{}, err
	}
	ownerAppID := appID
	stored, storeErr := service.store.InsertRoute(ctx, StoredRoute{
		AppPattern:       appID,
		ChannelPattern:   input.ChannelPattern,
		DestinationNames: slices.Clone(input.Destinations),
		Enabled:          input.Enabled == nil || *input.Enabled,
		OwnerKind:        OwnerApp,
		OwnerAppID:       &ownerAppID,
	})
	if storeErr != nil {
		return RouteView{}, storeErr
	}
	service.logger.InfoContext(ctx, "app route created",
		"app_id", appID, "route_id", stored.ID, "channel_pattern", stored.ChannelPattern)
	return routeView(stored), nil
}

// ListAppRoutes returns the routes app appID owns, never nil.
func (service *Service) ListAppRoutes(ctx context.Context, appID string) ([]RouteView, error) {
	if err := validateOwnerAppID(appID); err != nil {
		return nil, err
	}
	stored, storeErr := service.store.ListRoutesByOwner(ctx, appID)
	if storeErr != nil {
		return nil, storeErr
	}
	views := make([]RouteView, 0, len(stored))
	for _, route := range stored {
		// Defence in depth: the store already filtered by owner; a row that is not this app's is
		// dropped rather than listed.
		if !routeOwnedByApp(route, appID) {
			continue
		}
		views = append(views, routeView(route))
	}
	return views, nil
}

// GetAppRoute returns the route when app appID owns it, and ErrRouteNotFound when it is absent or owned by
// anyone else.
func (service *Service) GetAppRoute(ctx context.Context, appID string, id int64) (RouteView, error) {
	if err := validateOwnerAppID(appID); err != nil {
		return RouteView{}, err
	}
	stored, storeErr := service.store.GetRoute(ctx, id)
	if storeErr != nil {
		return RouteView{}, storeErr
	}
	if !routeOwnedByApp(stored, appID) {
		// The caller sees the same 404 as for an absent route; the log is where an operator can tell an
		// app probing another owner's ids from a plain miss.
		service.logger.WarnContext(ctx, "app route read refused: not the caller's route",
			"app_id", appID, "route_id", id)
		return RouteView{}, ErrRouteNotFound
	}
	return routeView(stored), nil
}

// UpdateAppRoute applies patch to the route when app appID owns it. A patch app_pattern must equal appID
// (a wildcard or another app's id is a *ValidationError), so an update can never widen the route to other
// apps' messages. The ownership check runs inside the store's row lock, on the locked row, so it cannot be
// raced; a route owned by anyone else, or absent, is ErrRouteNotFound.
func (service *Service) UpdateAppRoute(
	ctx context.Context, appID string, id int64, patch RoutePatch,
) (RouteView, error) {
	if err := validateOwnerAppID(appID); err != nil {
		return RouteView{}, err
	}
	if patch.AppPattern != nil && *patch.AppPattern != appID {
		return RouteView{}, &ValidationError{Message: errMsgAppPatternNotOwn}
	}
	if err := validateRoutePatch(patch); err != nil {
		return RouteView{}, err
	}
	stored, storeErr := service.store.UpdateRoute(ctx, id, func(locked StoredRoute) (StoredRoute, error) {
		if !routeOwnedByApp(locked, appID) {
			return StoredRoute{}, ErrRouteNotFound
		}
		return applyRoutePatch(locked, patch), nil
	})
	if storeErr != nil {
		return RouteView{}, storeErr
	}
	service.logger.InfoContext(ctx, "app route updated", "app_id", appID, "route_id", stored.ID)
	return routeView(stored), nil
}

// DeleteAppRoute removes the route when app appID owns it, in one statement that carries the owner. It
// returns ErrRouteNotFound when the route is absent or owned by anyone else.
func (service *Service) DeleteAppRoute(ctx context.Context, appID string, id int64) error {
	if err := validateOwnerAppID(appID); err != nil {
		return err
	}
	deleted, storeErr := service.store.DeleteRouteOwnedBy(ctx, id, appID)
	if storeErr != nil {
		return storeErr
	}
	if !deleted {
		return ErrRouteNotFound
	}
	service.logger.InfoContext(ctx, "app route deleted", "app_id", appID, "route_id", id)
	return nil
}

// routeOwnedByApp reports whether route is owned by the app appID. It fails closed: an empty appID, an
// operator-owned route, an unknown owner kind and an app owner with no owner id are all not owned.
func routeOwnedByApp(route StoredRoute, appID string) bool {
	return appID != "" &&
		route.OwnerKind == OwnerApp &&
		route.OwnerAppID != nil &&
		*route.OwnerAppID == appID
}

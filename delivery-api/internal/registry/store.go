package registry

import "context"

// Store persists destinations and routes. Implementations return the sentinel
// errors from types.go and never expose sealed secrets through an error.
type Store interface {
	// InsertDestination persists a new destination and returns it with its id and
	// timestamps. A duplicate name returns ErrDestinationExists.
	InsertDestination(ctx context.Context, destination StoredDestination) (StoredDestination, error)
	// ListDestinations returns every destination ordered by name, never nil.
	ListDestinations(ctx context.Context) ([]StoredDestination, error)
	// GetDestination returns the named destination or ErrDestinationNotFound.
	GetDestination(ctx context.Context, name string) (StoredDestination, error)
	// GetDestinationsByID returns the destinations with the given ids, ordered by
	// name. Ids that match no row are omitted. Service.Resolve re-sorts by name
	// anyway so ordering does not depend on the Store implementation.
	GetDestinationsByID(ctx context.Context, ids []int64) ([]StoredDestination, error)
	// UpdateDestination runs mutate on the locked row and persists the row it
	// returns. A mutate error rolls back and is returned unchanged.
	UpdateDestination(
		ctx context.Context, name string, mutate func(StoredDestination) (StoredDestination, error),
	) (StoredDestination, error)
	// DeleteDestination removes the named destination and reports whether a row
	// existed. A destination a route or heartbeat check still references returns ErrDestinationInUse.
	DeleteDestination(ctx context.Context, name string) (bool, error)
	// InsertRoute persists a route, resolving route.DestinationNames to ids. An
	// unknown name returns ErrUnknownDestination and inserts nothing.
	InsertRoute(ctx context.Context, route StoredRoute) (StoredRoute, error)
	// ListRoutes returns every route ordered by id, never nil.
	ListRoutes(ctx context.Context) ([]StoredRoute, error)
	// GetRoute returns the route or ErrRouteNotFound.
	GetRoute(ctx context.Context, id int64) (StoredRoute, error)
	// UpdateRoute runs mutate on the locked route and persists the result,
	// replacing the destination set from DestinationNames atomically.
	UpdateRoute(
		ctx context.Context, id int64, mutate func(StoredRoute) (StoredRoute, error),
	) (StoredRoute, error)
	// DeleteRoute removes the route and reports whether a row existed.
	DeleteRoute(ctx context.Context, id int64) (bool, error)
	// ListEnabledRoutes returns only the enabled routes, ordered by id.
	ListEnabledRoutes(ctx context.Context) ([]StoredRoute, error)
}

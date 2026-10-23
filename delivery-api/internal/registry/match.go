package registry

import (
	"cmp"
	"slices"
	"strings"
)

// MatchPattern reports whether value matches pattern: "*" matches anything, a
// trailing "*" is a prefix match, and any other pattern must equal value.
func MatchPattern(pattern, value string) bool {
	if pattern == wildcardPattern {
		return true
	}
	if prefix, found := strings.CutSuffix(pattern, wildcardPattern); found {
		return strings.HasPrefix(value, prefix)
	}
	return pattern == value
}

// DestinationMatch is one destination a message fires, with the ids of the
// enabled routes that matched it, so a later filter can name the route.
type DestinationMatch struct {
	DestinationID int64
	// RouteIDs are the matching routes' ids in route order, without repeats.
	RouteIDs []int64
}

// MatchRoutes returns the destinations that fire for (appID, channel): one
// entry per distinct destination across every enabled route whose app and
// channel patterns match, in ascending DestinationID order, each carrying the
// ids of the matching routes that name it. Disabled routes are ignored. The
// result is never nil.
func MatchRoutes(routes []StoredRoute, appID, channel string) []DestinationMatch {
	routeIDsByDestination := make(map[int64][]int64)
	for _, route := range routes {
		if !route.Enabled ||
			!MatchPattern(route.AppPattern, appID) ||
			!MatchPattern(route.ChannelPattern, channel) {
			continue
		}
		for _, destinationID := range route.DestinationIDs {
			if !slices.Contains(routeIDsByDestination[destinationID], route.ID) {
				routeIDsByDestination[destinationID] = append(routeIDsByDestination[destinationID], route.ID)
			}
		}
	}
	matches := make([]DestinationMatch, 0, len(routeIDsByDestination))
	for destinationID, routeIDs := range routeIDsByDestination {
		matches = append(matches, DestinationMatch{DestinationID: destinationID, RouteIDs: routeIDs})
	}
	slices.SortFunc(matches, func(left, right DestinationMatch) int {
		return cmp.Compare(left.DestinationID, right.DestinationID)
	})
	return matches
}

// DestinationUsableBy reports whether a destination with the given owner may
// carry traffic for appID: an operator-owned destination is usable by every
// app, an app-owned one only by the app that owns it. It fails closed: an
// empty or unknown owner kind, an operator owner carrying an owner id (an
// inconsistent row), an app owner with no owner id, and an empty appID
// against an app-owned destination are all unusable. appID is compared
// literally, so a route's wildcard pattern ("*", "prefix*") never matches an
// app-owned destination.
func DestinationUsableBy(ownerKind string, ownerAppID *string, appID string) bool {
	switch ownerKind {
	case OwnerOperator:
		return ownerAppID == nil
	case OwnerApp:
		return ownerAppID != nil && *ownerAppID != "" && *ownerAppID == appID
	default:
		return false
	}
}

// UsableBy reports whether the destination may carry traffic for appID; see
// DestinationUsableBy.
func (destination StoredDestination) UsableBy(appID string) bool {
	return DestinationUsableBy(destination.OwnerKind, destination.OwnerAppID, appID)
}

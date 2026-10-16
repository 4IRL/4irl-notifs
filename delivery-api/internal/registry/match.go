package registry

import (
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

// MatchRoutes returns the ids of the destinations that fire for (appID,
// channel): the union of DestinationIDs across every enabled route whose app
// and channel patterns match, de-duplicated and in ascending order. Disabled
// routes are ignored. The result is never nil.
func MatchRoutes(routes []StoredRoute, appID, channel string) []int64 {
	seen := make(map[int64]bool)
	destinationIDs := []int64{}
	for _, route := range routes {
		if !route.Enabled ||
			!MatchPattern(route.AppPattern, appID) ||
			!MatchPattern(route.ChannelPattern, channel) {
			continue
		}
		for _, destinationID := range route.DestinationIDs {
			if !seen[destinationID] {
				seen[destinationID] = true
				destinationIDs = append(destinationIDs, destinationID)
			}
		}
	}
	slices.Sort(destinationIDs)
	return destinationIDs
}

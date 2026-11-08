package heartbeat

import "sort"

// Deliverable reports whether destination can deliver: it is enabled AND its
// type is mapped to true in supportedTypes. A nil or empty set means nothing is
// deliverable (fail closed), and a type mapped to false is not supported.
//
// It is the one rule shared by the consumers: the heartbeat flush, the readyz
// held count and the unassigned checks listing. It matches message.Service's
// supportedTypes check in routeTargets. It is deliberately enabled AND
// supported type only: it does not apply registry.DestinationUsableBy's owner
// check, so a destination message.Submit would reject as
// registry.ErrUnknownDestination still counts as deliverable and the readyz
// count can rarely undercount (creation validates ownership, so this is an
// edge case).
func Deliverable(destination DestinationRef, supportedTypes map[string]bool) bool {
	return destination.Enabled && supportedTypes[destination.Type]
}

// supportedTypeList returns the sorted keys of supportedTypes whose value is
// true, for SQL binding. It is never nil: an empty slice when none are
// supported.
func supportedTypeList(supportedTypes map[string]bool) []string {
	list := make([]string, 0, len(supportedTypes))
	for destinationType, supported := range supportedTypes {
		if supported {
			list = append(list, destinationType)
		}
	}
	sort.Strings(list)
	return list
}

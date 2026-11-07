package personal

import (
	"strconv"
	"strings"
)

// deliveryNamePrefix starts every personal delivery_name. '@' can never start an
// operator or app destination name, so the two namespaces cannot collide.
const deliveryNamePrefix = "@personal_"

// PersonalDeliveryName returns the destination_name of the delivery row for
// personal destination id. It is the only formatter of that name.
//
//nolint:revive // the plan's contract name; personal.PersonalDeliveryName reads clearly at call sites
func PersonalDeliveryName(id int64) string {
	return deliveryNamePrefix + strconv.FormatInt(id, 10)
}

// ParsePersonalDeliveryName is the only parser of a personal delivery name. It
// accepts the canonical form alone: the prefix plus a positive decimal id with no
// sign, no leading zero, no trailing text and no int64 overflow.
func ParsePersonalDeliveryName(name string) (int64, bool) {
	digits, found := strings.CutPrefix(name, deliveryNamePrefix)
	if !found {
		return 0, false
	}
	id, parseErr := strconv.ParseInt(digits, 10, 64)
	if parseErr != nil || id <= 0 {
		return 0, false
	}
	if strconv.FormatInt(id, 10) != digits {
		return 0, false
	}
	return id, true
}

// IsPersonalDeliveryName reports whether name is in the personal namespace, even
// when it is not canonical (such a row is still a personal row, for display).
func IsPersonalDeliveryName(name string) bool {
	return strings.HasPrefix(name, deliveryNamePrefix)
}

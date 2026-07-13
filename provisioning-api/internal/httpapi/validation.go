package httpapi

import "regexp"

// appIDPattern matches a valid app_id: lowercase letters, digits, and
// underscores only (no hyphens), 1-63 characters.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// userIDPattern matches a valid user_id: lowercase letters, digits,
// underscores, and hyphens, 1-63 characters.
var userIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// reservedEveryone is the reserved identifier meaning "every user"/"every
// app"; it is rejected as an app_id or user_id value.
const reservedEveryone = "everyone"

// reservedWildcardUserID is ntfy's synthetic anonymous-user identifier; it is
// rejected as a user_id value.
const reservedWildcardUserID = "*"

// validateAppID reports whether appID is a well-formed, non-reserved app_id.
func validateAppID(appID string) bool {
	if appID == reservedEveryone {
		return false
	}
	return appIDPattern.MatchString(appID)
}

// validateUserID reports whether userID is a well-formed, non-reserved
// user_id.
func validateUserID(userID string) bool {
	if userID == reservedEveryone || userID == reservedWildcardUserID {
		return false
	}
	return userIDPattern.MatchString(userID)
}

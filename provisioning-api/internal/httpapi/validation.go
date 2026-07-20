package httpapi

import (
	"regexp"
	"strings"
	"unicode"
)

// appIDPattern matches a valid app_id: lowercase letters, digits, and
// underscores only (no hyphens), 1-63 characters.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// userIDPattern matches a valid user_id: lowercase letters, digits,
// underscores, and hyphens, 1-63 characters.
var userIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ntfyUserIDPattern matches a derived ntfy username: "u_" followed by a
// 16-character person hash (lowercase RFC 4648 base32 alphabet).
var ntfyUserIDPattern = regexp.MustCompile(`^u_[a-z2-7]{16}$`)

// reservedEveryone is the reserved identifier meaning "every user"/"every
// app"; it is rejected as an app_id or user_id value.
const reservedEveryone = "everyone"

// reservedWildcardUserID is ntfy's synthetic anonymous-user identifier; it is
// rejected as a user_id value.
const reservedWildcardUserID = "*"

// maxEmailLength is the stack-wide maximum accepted email length.
const maxEmailLength = 254

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

// validateEmail reports whether email is well-formed per the stack-wide
// rule: after trimming surrounding whitespace and lowercasing, the address
// must be non-empty, at most 254 characters, contain no internal
// whitespace, and contain exactly one "@" with a non-empty local part and a
// non-empty domain part.
func validateEmail(email string) bool {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || len(normalized) > maxEmailLength {
		return false
	}
	if strings.IndexFunc(normalized, unicode.IsSpace) != -1 {
		return false
	}
	if strings.Count(normalized, "@") != 1 {
		return false
	}
	localPart, domainPart, _ := strings.Cut(normalized, "@")
	return localPart != "" && domainPart != ""
}

// validateNtfyUserID reports whether ntfyUserID matches the derived-username
// shape ("u_" + 16-character person hash) produced by personhash.NtfyUser.
func validateNtfyUserID(ntfyUserID string) bool {
	return ntfyUserIDPattern.MatchString(ntfyUserID)
}

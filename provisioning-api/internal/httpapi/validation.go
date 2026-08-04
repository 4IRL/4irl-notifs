package httpapi

import (
	"regexp"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/validation"
)

// appIDPattern matches a valid app_id: lowercase letters, digits, and
// underscores only (no hyphens), 1-63 characters.
var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// channelPattern matches a valid channel: lowercase letters, digits, and
// underscores only (no hyphens, so the "{app_id}-{person_hash}-{channel}"
// topic segments stay unambiguous), 1-32 characters.
var channelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,31}$`)

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

// maxMessageLength is the maximum accepted test-notification message length.
const maxMessageLength = 4096

// maxRecipients is the maximum number of recipients accepted in a single
// test-notification request, bounding the per-request publish fan-out.
const maxRecipients = 100

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

// validateEmail reports whether email is well-formed, delegating to the
// shared stack-wide rule in the internal/validation leaf package.
func validateEmail(email string) bool {
	return validation.IsValidEmail(email)
}

// validateChannel reports whether channel is a well-formed notification
// channel: 1-32 characters of lowercase letters, digits, and underscores (no
// hyphens, so the topic segments stay unambiguous).
func validateChannel(channel string) bool {
	return channelPattern.MatchString(channel)
}

// validateMessage reports whether message is within the maximum accepted
// length. An empty message is valid; the handler applies the default first.
func validateMessage(message string) bool {
	return len(message) <= maxMessageLength
}

// validateRecipientsCount reports whether recipients is within the maximum
// accepted count. An empty slice is within bound; the handler applies the
// non-empty check separately first.
func validateRecipientsCount(recipients []string) bool {
	return len(recipients) <= maxRecipients
}

// validateNtfyUserID reports whether ntfyUserID matches the derived-username
// shape ("u_" + 16-character person hash) produced by personhash.NtfyUser.
func validateNtfyUserID(ntfyUserID string) bool {
	return ntfyUserIDPattern.MatchString(ntfyUserID)
}

// Package ntfycli builds and executes commands against the documented ntfy
// CLI (`ntfy user` / `ntfy access` / `ntfy token`), which edits the auth
// database shared with the ntfy server over a docker volume.
package ntfycli

// TopicPattern returns the scoped per-person topic ACL pattern:
// "{app_id}-{personHash}-*". This is the per-person namespace granted to a
// subscriber's ntfy user; the app-wide wildcard ("{app_id}-*") now belongs
// to publisher identities, arriving in a later phase.
func TopicPattern(appID string, personHash string) string {
	return appID + "-" + personHash + "-*"
}

// Permission is an ntfy access-grant permission level.
type Permission string

// PermissionReadOnly is the read-only grant given to a subscriber's scoped
// per-person topic pattern; write-only arrives with the publisher split.
const PermissionReadOnly Permission = "ro"

// UserAddArgs builds the CLI arguments creating a user. The password is
// supplied out-of-band via the NTFY_PASSWORD environment variable.
func UserAddArgs(userID string) []string {
	return []string{"user", "add", userID}
}

// UserDeleteArgs builds the CLI arguments deleting a user (and, server-side,
// the user's ACL entries and tokens).
func UserDeleteArgs(userID string) []string {
	return []string{"user", "del", userID}
}

// UserListArgs builds the CLI arguments listing all users with their grants.
func UserListArgs() []string {
	return []string{"user", "list"}
}

// AccessGrantArgs builds the CLI arguments granting permission to userID on
// topicPattern.
func AccessGrantArgs(userID string, topicPattern string, permission Permission) []string {
	return []string{"access", userID, topicPattern, string(permission)}
}

// AccessResetArgs builds the CLI arguments revoking userID's access to
// topicPattern.
func AccessResetArgs(userID string, topicPattern string) []string {
	return []string{"access", "--reset", userID, topicPattern}
}

// TokenAddArgs builds the CLI arguments creating a never-expiring access
// token labeled with label. Subscriber tokens are labeled with the app_id.
func TokenAddArgs(userID string, label string) []string {
	return []string{"token", "add", "--label", label, userID}
}

// TokenListArgs builds the CLI arguments listing a user's tokens.
func TokenListArgs(userID string) []string {
	return []string{"token", "list", userID}
}

// TokenRemoveArgs builds the CLI arguments removing a single token.
func TokenRemoveArgs(userID string, token string) []string {
	return []string{"token", "remove", userID, token}
}

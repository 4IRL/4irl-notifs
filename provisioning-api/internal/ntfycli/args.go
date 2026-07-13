// Package ntfycli builds and executes commands against the documented ntfy
// CLI (`ntfy user` / `ntfy access` / `ntfy token`), which edits the auth
// database shared with the ntfy server over a docker volume.
package ntfycli

// TopicPattern returns the wildcard topic ACL pattern covering every channel
// of the given app, per the "{app_id}-{channel}" topic-namespace convention.
func TopicPattern(appID string) string {
	return appID + "-*"
}

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

// AccessGrantArgs builds the CLI arguments granting read-write access to the
// app's wildcard topic pattern.
func AccessGrantArgs(userID string, appID string) []string {
	return []string{"access", userID, TopicPattern(appID), "rw"}
}

// AccessResetArgs builds the CLI arguments revoking the user's access to the
// app's wildcard topic pattern.
func AccessResetArgs(userID string, appID string) []string {
	return []string{"access", "--reset", userID, TopicPattern(appID)}
}

// TokenAddArgs builds the CLI arguments creating a never-expiring access
// token labeled with the app it was provisioned for.
func TokenAddArgs(userID string, appID string) []string {
	return []string{"token", "add", "--label", appID, userID}
}

// TokenListArgs builds the CLI arguments listing a user's tokens.
func TokenListArgs(userID string) []string {
	return []string{"token", "list", userID}
}

// TokenRemoveArgs builds the CLI arguments removing a single token.
func TokenRemoveArgs(userID string, token string) []string {
	return []string{"token", "remove", userID, token}
}

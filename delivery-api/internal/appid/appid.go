// Package appid holds the one definition of the app_id slug rule: lowercase letters, digits
// and underscores, starting with a letter or digit, 1-63 characters. The same rule is the SQL
// CHECK on every app-scoped column and the one provisioning-api enforces.
//
// It is a leaf package (standard library only) so every package that validates an app id can
// import it without an import cycle. Each of those packages keeps its own ValidationError type
// and wraps ErrInvalid in it.
package appid

import (
	"errors"
	"regexp"
)

// ErrInvalid means a value is not a well-formed app id. Its message is the client-safe text
// the delivery API returns for an invalid app id.
var ErrInvalid = errors.New("invalid app_id")

// pattern is the app_id slug rule; keep it identical to the SQL CHECKs in the migrations.
var pattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,62}$`)

// Valid reports whether appID is a well-formed app id.
func Valid(appID string) bool {
	return pattern.MatchString(appID)
}

// ValidateAppID returns ErrInvalid when appID is not a well-formed app id, and nil otherwise.
func ValidateAppID(appID string) error {
	if !Valid(appID) {
		return ErrInvalid
	}
	return nil
}

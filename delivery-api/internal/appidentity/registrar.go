package appidentity

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
)

const (
	// platformPrefix is the namespace of shared destinations (`_platform.json`); no registrar
	// prefix may start with it.
	platformPrefix = "platform_"
	// everyoneStem is the app id the people directory reserves; no registrar stem may equal it.
	everyoneStem = "everyone"
)

var (
	prefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,30}_$`)
	namePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)
)

// ValidatePrefix checks a registrar prefix: 2-32 chars ending in "_", not starting with
// "platform_" and whose stem is not "everyone". The error wraps ErrInvalidPrefix.
func ValidatePrefix(prefix string) error {
	if !prefixPattern.MatchString(prefix) ||
		strings.HasPrefix(prefix, platformPrefix) ||
		strings.TrimSuffix(prefix, "_") == everyoneStem {
		return ErrInvalidPrefix
	}
	return nil
}

// ValidateName checks a registrar's bare app name against the name grammar. The error wraps
// ErrInvalidName.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

// DeriveAppID returns prefix+name after checking that it is a valid app id. The error wraps
// ErrInvalidName.
func DeriveAppID(prefix string, name string) (string, error) {
	if nameErr := ValidateName(name); nameErr != nil {
		return "", nameErr
	}
	derived := prefix + name
	if validateErr := appid.ValidateAppID(derived); validateErr != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidName, validateErr.Error())
	}
	return derived, nil
}

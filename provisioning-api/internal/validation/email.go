// Package validation holds stack-wide input-validation rules shared across the
// provisioning-api. It is a leaf package with no internal dependencies so both
// the httpapi (request-level validation) and provisioning (recipient
// resolution) packages can import it without creating an import cycle.
package validation

import (
	"strings"
	"unicode"
)

// maxEmailLength is the stack-wide maximum accepted email length.
const maxEmailLength = 254

// IsValidEmail reports whether email is well-formed per the stack-wide rule:
// after trimming surrounding whitespace and lowercasing, the address must be
// non-empty, at most 254 characters, contain no internal whitespace, and
// contain exactly one "@" with a non-empty local part and a non-empty domain
// part.
func IsValidEmail(email string) bool {
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

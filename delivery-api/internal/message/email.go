package message

import (
	"strings"
	"unicode"
)

// maxEmailLength is the stack-wide maximum accepted email length.
const maxEmailLength = 254

// isValidEmail reports whether email is well-formed per the stack-wide rule:
// after trimming surrounding whitespace and lowercasing, the address must be
// non-empty, at most 254 bytes, contain no internal whitespace, and contain
// exactly one "@" with a non-empty local part and a non-empty domain part.
//
// It ports provisioning-api internal/validation.IsValidEmail in behavior. Go
// internal/ rules block importing it across modules, so the rule is duplicated
// here, following the repo's duplication convention; keep the two in step.
func isValidEmail(email string) bool {
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

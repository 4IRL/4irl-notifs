// Client-side validation mirroring the provisioning-api's server-side rules
// (internal/httpapi/validation.go) so the UI can give immediate feedback. The
// server remains the source of truth; these only prevent obviously-invalid
// submissions.

const APP_ID_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;
const RESERVED_EVERYONE = 'everyone';
const MAX_EMAIL_LENGTH = 254;

/** Reports whether appId is a well-formed, non-reserved app identifier. */
export function isValidAppId(appId: string): boolean {
  if (appId === RESERVED_EVERYONE) {
    return false;
  }
  return APP_ID_PATTERN.test(appId);
}

/**
 * Reports whether email is well-formed per the stack-wide rule: after
 * trimming surrounding whitespace and lowercasing, the address must be
 * non-empty, at most 254 characters, contain no internal whitespace, and
 * contain exactly one "@" with a non-empty local part and a non-empty
 * domain part.
 */
export function isValidEmail(email: string): boolean {
  const normalized = email.trim().toLowerCase();
  if (normalized === '' || normalized.length > MAX_EMAIL_LENGTH) {
    return false;
  }
  if (/\s/.test(normalized)) {
    return false;
  }
  const parts = normalized.split('@');
  if (parts.length !== 2) {
    return false;
  }
  const [localPart, domainPart] = parts;
  return localPart !== '' && domainPart !== '';
}

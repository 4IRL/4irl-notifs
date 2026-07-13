// Client-side validation mirroring the provisioning-api's server-side rules
// (internal/httpapi/validation.go) so the UI can give immediate feedback. The
// server remains the source of truth; these only prevent obviously-invalid
// submissions.

const APP_ID_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;
const USER_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{0,62}$/;
const RESERVED_EVERYONE = 'everyone';
const RESERVED_WILDCARD = '*';

/** Reports whether appId is a well-formed, non-reserved app identifier. */
export function isValidAppId(appId: string): boolean {
  if (appId === RESERVED_EVERYONE) {
    return false;
  }
  return APP_ID_PATTERN.test(appId);
}

/** Reports whether userId is a well-formed, non-reserved user identifier. */
export function isValidUserId(userId: string): boolean {
  if (userId === RESERVED_EVERYONE || userId === RESERVED_WILDCARD) {
    return false;
  }
  return USER_ID_PATTERN.test(userId);
}

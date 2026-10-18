// Client-side validation mirroring the provisioning-api's server-side rules
// (internal/httpapi/validation.go) so the UI can give immediate feedback. The
// server remains the source of truth; these only prevent obviously-invalid
// submissions.

const APP_ID_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;
const CHANNEL_PATTERN = /^[a-z0-9][a-z0-9_]{0,31}$/;
const RESERVED_EVERYONE = 'everyone';
const MAX_EMAIL_LENGTH = 254;
// Mirrors the server's maxTitleLength (internal/httpapi/validation.go): a byte limit, not a rune limit.
const MAX_TITLE_BYTES = 256;
// Unicode category Cc is exactly what Go's unicode.IsControl rejects (C0, DEL, C1).
const CONTROL_CHARACTER_PATTERN = /\p{Cc}/u;
const TEXT_ENCODER = new TextEncoder();

/** Reports whether appId is a well-formed, non-reserved app identifier. */
export function isValidAppId(appId: string): boolean {
  if (appId === RESERVED_EVERYONE) {
    return false;
  }
  return APP_ID_PATTERN.test(appId);
}

/**
 * Reports whether channel is a well-formed ntfy channel segment, mirroring the
 * server-side rule (internal/httpapi/validation.go `channelPattern`): 1–32
 * characters, lowercase alphanumerics plus underscore, no leading underscore
 * and no hyphens (so topic segments stay unambiguous). A UX pre-check only —
 * the server remains the source of truth.
 */
export function isValidChannel(channel: string): boolean {
  return CHANNEL_PATTERN.test(channel);
}

/**
 * Reports whether title would be accepted as a test-notification title,
 * mirroring the server-side rules (internal/httpapi/validation.go
 * `validateTitleLength` / `validateTitleCharacters`): at most 256 UTF-8 bytes
 * and no control characters (C0, DEL, C1). A UX pre-check only — the server
 * remains the source of truth.
 */
export function isValidTitle(title: string): boolean {
  return (
    TEXT_ENCODER.encode(title).length <= MAX_TITLE_BYTES && !CONTROL_CHARACTER_PATTERN.test(title)
  );
}

const DESTINATION_NAME_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;
const NTFY_TOPIC_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;
// Printable ASCII (space through tilde), 32-256 characters, as delivery-api requires.
const SIGNING_SECRET_PATTERN = /^[\x20-\x7E]{32,256}$/;
const HTTPS_PREFIX = 'https://';

/** Mirrors delivery-api's destination-name slug rule. A UX pre-check only. */
export function isValidDestinationName(name: string): boolean {
  return DESTINATION_NAME_PATTERN.test(name);
}

const ROUTE_APP_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;
const ROUTE_CHANNEL_EXACT_PATTERN = /^[a-z0-9][a-z0-9_]{0,31}$/;
const ROUTE_CHANNEL_PREFIX_PATTERN = /^[a-z0-9][a-z0-9_]{0,30}\*$/;
const RESERVED_ROUTE_APP = 'everyone';

/** Mirrors delivery-api's route app pattern: `*` or an app id (not `everyone`). */
export function isValidRouteAppPattern(pattern: string): boolean {
  if (pattern === '*') {
    return true;
  }
  return pattern !== RESERVED_ROUTE_APP && ROUTE_APP_PATTERN.test(pattern);
}

/** Mirrors delivery-api's route channel pattern: `*`, an exact channel or a `prefix*`. */
export function isValidRouteChannelPattern(pattern: string): boolean {
  return (
    pattern === '*' ||
    ROUTE_CHANNEL_EXACT_PATTERN.test(pattern) ||
    ROUTE_CHANNEL_PREFIX_PATTERN.test(pattern)
  );
}

/** Mirrors delivery-api's ntfy topic rule. A UX pre-check only. */
export function isValidNtfyTopic(topic: string): boolean {
  return NTFY_TOPIC_PATTERN.test(topic);
}

/** Mirrors delivery-api's webhook signing-secret rule (32-256 printable ASCII). */
export function isValidSigningSecret(secret: string): boolean {
  return SIGNING_SECRET_PATTERN.test(secret);
}

/** Reports whether value looks like an https URL; the server does the full parse. */
export function isHttpsUrl(value: string): boolean {
  return value.startsWith(HTTPS_PREFIX) && value.length > HTTPS_PREFIX.length;
}

/** True when value has leading or trailing whitespace. */
export function hasSurroundingWhitespace(value: string): boolean {
  return value !== value.trim();
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

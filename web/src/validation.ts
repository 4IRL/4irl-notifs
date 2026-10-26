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

const HEARTBEAT_ID_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;

/** Mirrors delivery-api's heartbeat check app_id rule (no reserved names). A UX pre-check only. */
export function isValidHeartbeatAppId(appId: string): boolean {
  return HEARTBEAT_ID_PATTERN.test(appId);
}

/** Mirrors delivery-api's heartbeat check name rule. A UX pre-check only. */
export function isValidHeartbeatName(name: string): boolean {
  return HEARTBEAT_ID_PATTERN.test(name);
}

/** Heartbeat period bounds in seconds, mirroring delivery-api (1 minute to 31 days). */
export const HEARTBEAT_PERIOD_RANGE_SECONDS = { min: 60, max: 2_678_400 } as const;
/** Heartbeat grace bounds in seconds, mirroring delivery-api (1 minute to 7 days). */
export const HEARTBEAT_GRACE_RANGE_SECONDS = { min: 60, max: 604_800 } as const;
/** The most alert destinations one check may have. */
export const MAX_HEARTBEAT_DESTINATIONS = 20;

// Website probe limits. Mirrors delivery-api heartbeat/validate.go. A UX pre-check only.
export const PROBE_URL_MAX_BYTES = 2048;
export const PROBE_INTERVAL_RANGE_SECONDS = { min: 60, max: 86_400 } as const;
export const PROBE_TIMEOUT_RANGE_SECONDS = { min: 1, max: 8 } as const;
export const PROBE_MAX_EXPECTED_STATUSES = 10;
export const PROBE_BODY_MATCH_MAX_BYTES = 256;
export const PROBE_ACCESS_CREDENTIAL_MAX_BYTES = 256;

const PROBE_STATUS_PATTERN = /^\d{3}$/;
const PROBE_STATUS_RANGE = { min: 100, max: 599 } as const;
const IPV4_HOST_PATTERN = /^\d+(\.\d+){3}$/;
const WHITESPACE_PATTERN = /\s/;
const URL_AUTHORITY_END_PATTERN = /[/?#]/;
const LOCALHOST = 'localhost';
const LOCALHOST_SUFFIX = '.localhost';

export type ProbeUrlProblem = 'empty' | 'scheme' | 'tooLong' | 'malformed';

/**
 * Mirrors delivery-api's probe URL rule: literal lowercase `https://` prefix, at
 * most 2048 bytes, no `#`, no userinfo, a non-empty host that is not
 * `localhost`/`*.localhost` or an IP literal. Callers pass the trimmed value.
 */
export function probeUrlProblem(value: string): ProbeUrlProblem | null {
  if (value.trim() === '') {
    return 'empty';
  }
  if (!value.startsWith(HTTPS_PREFIX)) {
    return 'scheme';
  }
  if (TEXT_ENCODER.encode(value).length > PROBE_URL_MAX_BYTES) {
    return 'tooLong';
  }
  if (value.includes('#') || WHITESPACE_PATTERN.test(value)) {
    return 'malformed';
  }
  const rest = value.slice(HTTPS_PREFIX.length);
  const authorityEnd = rest.search(URL_AUTHORITY_END_PATTERN);
  const authority = authorityEnd === -1 ? rest : rest.slice(0, authorityEnd);
  if (authority.includes('@') || authority.startsWith('[')) {
    return 'malformed';
  }
  const host = authority.split(':')[0].toLowerCase();
  if (
    host === '' ||
    host === LOCALHOST ||
    host.endsWith(LOCALHOST_SUFFIX) ||
    IPV4_HOST_PATTERN.test(host)
  ) {
    return 'malformed';
  }
  return null;
}

/** Parses a comma-separated list of 1..10 distinct HTTP status codes (100..599). */
export function parseExpectedStatuses(
  text: string,
): { ok: true; statuses: number[] } | { ok: false } {
  const parts = text.split(',').map((part) => part.trim());
  if (parts.length < 1 || parts.length > PROBE_MAX_EXPECTED_STATUSES) {
    return { ok: false };
  }
  const statuses: number[] = [];
  for (const part of parts) {
    if (!PROBE_STATUS_PATTERN.test(part)) {
      return { ok: false };
    }
    const status = Number(part);
    if (status < PROBE_STATUS_RANGE.min || status > PROBE_STATUS_RANGE.max) {
      return { ok: false };
    }
    if (statuses.includes(status)) {
      return { ok: false };
    }
    statuses.push(status);
  }
  return { ok: true, statuses };
}

/**
 * Body-match text must be 1..256 bytes, non-blank and free of NUL. The caller
 * treats an empty string as "no match" and does not call this for it.
 */
export function bodyMatchProblem(value: string): 'invalid' | null {
  if (
    value.trim() === '' ||
    value.includes('\u0000') ||
    TEXT_ENCODER.encode(value).length > PROBE_BODY_MATCH_MAX_BYTES
  ) {
    return 'invalid';
  }
  return null;
}

/**
 * One Access credential half: 1..256 bytes, every char 0x21..0x7E. Callers check
 * the blank/half-filled pair first and only call this on non-empty halves.
 */
export function accessCredentialProblem(value: string): 'invalid' | null {
  if (value.length < 1 || value.length > PROBE_ACCESS_CREDENTIAL_MAX_BYTES) {
    return 'invalid';
  }
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 0x21 || code > 0x7e) {
      return 'invalid';
    }
  }
  return null;
}

/** Probe timeout must be a whole number of seconds in 1..8. */
export function probeTimeoutProblem(value: number): 'range' | null {
  return Number.isInteger(value) &&
    value >= PROBE_TIMEOUT_RANGE_SECONDS.min &&
    value <= PROBE_TIMEOUT_RANGE_SECONDS.max
    ? null
    : 'range';
}

/** Probe interval must be a whole number of seconds in 60..86400. */
export function probeIntervalProblem(value: number): 'range' | null {
  return Number.isInteger(value) &&
    value >= PROBE_INTERVAL_RANGE_SECONDS.min &&
    value <= PROBE_INTERVAL_RANGE_SECONDS.max
    ? null
    : 'range';
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

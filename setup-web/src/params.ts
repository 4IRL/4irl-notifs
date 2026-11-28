// URL contract for the setup page. Every constant mirrors a server-side rule
// (provisioning-api, ntfycli) so the page can reject malformed links up front.
// The server remains the source of truth, as in web/src/validation.ts.

/**
 * Mirrors provisioning-api/internal/httpapi/validation.go `appIDPattern`:
 * lowercase alphanumerics plus underscore, 1-63 chars, no leading underscore.
 */
const APP_ID_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;

/**
 * Mirrors provisioning-api/internal/httpapi/validation.go `ntfyUserIDPattern`:
 * `u_` followed by 16 lowercase RFC 4648 base32 characters (personhash.Hash).
 */
const NTFY_USER_PATTERN = /^u_[a-z2-7]{16}$/;

/**
 * Mirrors provisioning-api/internal/httpapi/validation.go `reservedEveryone`,
 * the reserved app literal checked first: `everyone` is never a real app.
 */
const RESERVED_EVERYONE = 'everyone';

/** Maximum length of the optional `name` display value, after trimming. */
export const NAME_MAX_LENGTH = 64;

/** Unicode control (Cc) and format (Cf) characters, rejected in `name`. */
const INVISIBLE_CHARACTERS = /[\p{Cc}\p{Cf}]/u;

/**
 * The shared ntfy server (constant, no trailing slash). The Go side has no
 * single constant; the base URL is the `NTFY_BASE_URL` default in
 * docker-compose.prod.yml.
 */
export const NTFY_SERVER = 'https://notifs.4irl.app';

/** The only domain (apex and any subdomain) the optional `return` link may point at. */
const RETURN_URL_DOMAIN = '4irl.app';

/**
 * Maximum length of the optional `return` URL, checked on the raw value and on the
 * normalized href. Capacity arithmetic: QrHandoff encodes the link at level M, whose
 * byte-mode ceiling at version 40 is 2331 bytes. Worst case for the other params:
 * origin + path ~40, `app` 63, `user` 18, `name` 64 four-byte code points percent-encoded
 * (12 chars each = 768), labels ~25, total ~915. searchParams.set encodes each character
 * of the carried URL to at most 3 characters, so 400 adds at most 1200 (~2115 total,
 * under 2331 with margin). The 3x bound holds only for the normalized href.
 */
const RETURN_URL_MAX_LENGTH = 400;

const USER_PREFIX_LENGTH = 'u_'.length;

export type SetupParams =
  | {
      ok: true;
      app: string;
      user: string;
      displayName: string;
      nameIsFallback: boolean;
      returnUrl: string | null;
      personalTopic: string;
      broadcastTopic: string;
    }
  | { ok: false };

/**
 * Mirrors ntfycli `PersonChannelTopic`: `{app}-{hash}-all`, where hash is the
 * user id without its `u_` prefix.
 */
function personalTopicFor({ app, user }: { app: string; user: string }): string {
  return `${app}-${user.slice(USER_PREFIX_LENGTH)}-all`;
}

/** Mirrors ntfycli `BroadcastTopicPattern`: `{app}-broadcast`. */
function broadcastTopicFor(app: string): string {
  return `${app}-broadcast`;
}

/**
 * Fail closed: returns the normalized href only when every check passes, else null.
 * Compares URL.hostname (lowercased, punycoded), never the raw string.
 */
function validatedReturnUrl(raw: string | null): string | null {
  if (raw === null || raw.length === 0 || raw.length > RETURN_URL_MAX_LENGTH) {
    return null;
  }
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  const hostAllowed =
    url.hostname === RETURN_URL_DOMAIN || url.hostname.endsWith(`.${RETURN_URL_DOMAIN}`);
  if (
    url.protocol !== 'https:' ||
    url.username !== '' ||
    url.password !== '' ||
    url.port !== '' ||
    !hostAllowed ||
    url.href.length > RETURN_URL_MAX_LENGTH
  ) {
    return null;
  }
  return url.href;
}

/**
 * Parses and validates the setup link's query string. Only the first
 * occurrence of a repeated param is considered. Returns `{ ok: false }` when
 * `app` or `user` is missing or invalid; no raw param value is ever returned
 * in that case. `name` is optional and falls back to `app`.
 */
export function parseSetupParams(search: string): SetupParams {
  const query = new URLSearchParams(search);
  const app = query.get('app');
  const user = query.get('user');

  if (app === null || app === RESERVED_EVERYONE || !APP_ID_PATTERN.test(app)) {
    return { ok: false };
  }
  if (user === null || !NTFY_USER_PATTERN.test(user)) {
    return { ok: false };
  }

  const name = (query.get('name') ?? '').trim();
  // Control (Cc) and format (Cf, incl. bidi overrides such as U+202E) characters could
  // spoof the page's text, so such a name is invalid and falls back to `app`.
  const nameIsFallback =
    name.length === 0 || [...name].length > NAME_MAX_LENGTH || INVISIBLE_CHARACTERS.test(name);

  return {
    ok: true,
    app,
    user,
    displayName: nameIsFallback ? app : name,
    nameIsFallback,
    returnUrl: validatedReturnUrl(query.get('return')),
    personalTopic: personalTopicFor({ app, user }),
    broadcastTopic: broadcastTopicFor(app),
  };
}

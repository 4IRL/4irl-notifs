// person-service: a standalone Cloudflare Worker owning the D1 reverse-index
// `person` table (person_hash -> email). Exposes a tiny HTTP API for
// upserting and looking up that mapping.
//
// This Worker performs NO authentication/authorization itself. In
// production, Cloudflare Access enforces access control at the edge in
// front of this Worker's route/custom domain (see wrangler.toml and
// ARCHITECTURE.md "Authentication (Cloudflare Zero Trust)") — every request that reaches `fetch` below
// is already assumed to have passed that edge gate.

export interface Env {
  DB: D1Database;
}

/** A `person` row as stored in D1 and returned on the wire (snake_case). */
interface PersonRecord {
  person_hash: string;
  email: string;
  created_at: string;
}

/** An `app` registry row as stored in D1 and returned on the wire (snake_case). */
interface AppRecord {
  app_id: string;
  display_name: string;
  description: string | null;
  created_at: string;
}

const PERSON_HASH_PATTERN = /^[a-z2-7]{16}$/;
const MAX_EMAIL_LENGTH = 254;

// Mirrors provisioning-api's validateAppID (internal/httpapi/validation.go):
// lowercase letters, digits, and underscores only (no hyphens), 1-63 chars,
// and the reserved value "everyone" is rejected. Kept in sync deliberately so
// an app_id valid in one service is valid in the other.
const APP_ID_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;
const RESERVED_APP_ID = 'everyone';

/** Reports whether appId is a well-formed, non-reserved app_id. */
function isValidAppId(appId: string): boolean {
  return appId !== RESERVED_APP_ID && APP_ID_PATTERN.test(appId);
}

/**
 * Validates an email address per this stack's shared, permissive rule:
 * non-empty after trim, at most 254 characters, no internal whitespace, and
 * exactly one `@` splitting a non-empty local part from a non-empty domain
 * part (a dot in the domain is deliberately NOT required). Case does not
 * affect validity — the same normalization (trim + lowercase) this function
 * applies internally is also what gets persisted to storage.
 */
export function isValidEmail(rawEmail: string): boolean {
  const email = rawEmail.trim().toLowerCase();
  if (email.length === 0 || email.length > MAX_EMAIL_LENGTH) {
    return false;
  }
  if (/\s/.test(email)) {
    return false;
  }
  const atIndex = email.indexOf('@');
  if (atIndex <= 0 || atIndex !== email.lastIndexOf('@')) {
    return false;
  }
  const domain = email.slice(atIndex + 1);
  return domain.length > 0;
}

/** Trims and lowercases an email address for storage/lookup — the single normalization rule for this stack. */
function normalizeEmail(rawEmail: string): string {
  return rawEmail.trim().toLowerCase();
}

/** Builds a JSON response with the given status and Content-Type header. */
function jsonResponse({ body, status }: { body: unknown; status: number }): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** Builds a JSON `{ error }` response with the given status. */
function errorResponse({ message, status }: { message: string; status: number }): Response {
  return jsonResponse({ body: { error: message }, status });
}

/** Handles `PUT /person` — idempotent upsert of a person_hash -> email mapping. */
async function handlePutPerson({ request, env }: { request: Request; env: Env }): Promise<Response> {
  let parsedBody: unknown;
  try {
    parsedBody = await request.json();
  } catch {
    return errorResponse({ message: 'invalid JSON body', status: 400 });
  }

  const { person_hash: personHash, email: rawEmail } = (parsedBody ?? {}) as {
    person_hash?: unknown;
    email?: unknown;
  };

  if (typeof personHash !== 'string' || !PERSON_HASH_PATTERN.test(personHash)) {
    return errorResponse({ message: 'invalid person_hash', status: 400 });
  }
  if (typeof rawEmail !== 'string' || !isValidEmail(rawEmail)) {
    return errorResponse({ message: 'invalid email', status: 400 });
  }

  const normalizedEmail = normalizeEmail(rawEmail);
  const createdAt = new Date().toISOString();

  // ON CONFLICT preserves the original created_at (it's excluded from the
  // SET clause) while updating email; RETURNING hands back the row exactly
  // as it now stands in storage, whether this was an insert or an update.
  const row = await env.DB.prepare(
    `INSERT INTO person (person_hash, email, created_at)
     VALUES (?1, ?2, ?3)
     ON CONFLICT(person_hash) DO UPDATE SET email = excluded.email
     RETURNING person_hash, email, created_at`,
  )
    .bind(personHash, normalizedEmail, createdAt)
    .first<PersonRecord>();

  if (!row) {
    return errorResponse({ message: 'upsert failed', status: 500 });
  }

  return jsonResponse({ body: row, status: 200 });
}

/** Handles `GET /person?email=` — lookup by normalized email. */
async function handleGetPerson({ url, env }: { url: URL; env: Env }): Promise<Response> {
  const emailParam = url.searchParams.get('email');
  if (emailParam === null || !isValidEmail(emailParam)) {
    return errorResponse({ message: 'invalid email', status: 400 });
  }

  const normalizedEmail = normalizeEmail(emailParam);
  const row = await env.DB.prepare('SELECT person_hash, email, created_at FROM person WHERE email = ?1')
    .bind(normalizedEmail)
    .first<PersonRecord>();

  if (!row) {
    return errorResponse({ message: 'person not found', status: 404 });
  }

  return jsonResponse({ body: row, status: 200 });
}

/** Handles `GET /people` — enumerates every person, ordered by created_at then person_hash. */
async function handleGetPeople({ env }: { env: Env }): Promise<Response> {
  const { results } = await env.DB.prepare(
    'SELECT person_hash, email, created_at FROM person ORDER BY created_at ASC, person_hash ASC',
  ).all<PersonRecord>();

  return jsonResponse({ body: { people: results ?? [] }, status: 200 });
}

/** Handles `DELETE /person/{hash}` — idempotent removal of a person row.
 *  Returns 200 whether or not the row existed, so a caller (the provisioning-api
 *  dual-delete) can tear a person down without first checking existence, and a
 *  repeat delete is safe. */
async function handleDeletePerson({
  personHash,
  env,
}: {
  personHash: string;
  env: Env;
}): Promise<Response> {
  if (!PERSON_HASH_PATTERN.test(personHash)) {
    return errorResponse({ message: 'invalid person_hash', status: 400 });
  }

  // One batch (a single transaction) so the person and their reset history
  // are removed together.
  await env.DB.batch([
    env.DB.prepare('DELETE FROM person WHERE person_hash = ?1').bind(personHash),
    env.DB.prepare('DELETE FROM password_reset WHERE person_hash = ?1').bind(personHash),
  ]);
  return jsonResponse({ body: { person_hash: personHash, deleted: true }, status: 200 });
}

const CLAIM_MODES = ['if_unset', 'always', 'update_only'] as const;
type ClaimMode = (typeof CLAIM_MODES)[number];

const RESET_LIMIT = 5;
const RESET_WINDOW_MS = 3_600_000;

/** Parses a request body as JSON, returning undefined when it is not valid JSON. */
async function readJsonBody(request: Request): Promise<unknown | undefined> {
  try {
    return await request.json();
  } catch {
    return undefined;
  }
}

const CLIENT_STAMP_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;

/** True for a canonical `toISOString()` UTC timestamp (exact round trip). */
function isValidClientStamp(value: unknown): value is string {
  if (typeof value !== 'string' || !CLIENT_STAMP_PATTERN.test(value)) {
    return false;
  }
  const parsed = new Date(value);
  return !Number.isNaN(parsed.getTime()) && parsed.toISOString() === value;
}

/** Handles `POST /person/{hash}/password-claim` — atomically records
 *  password_set_at per the claim mode and reports the stamp it replaced.
 *  The previous-stamp read and the write run in one D1 batch (a single
 *  transaction), so concurrent claims cannot both win. */
async function handlePasswordClaim({
  personHash,
  request,
  env,
}: {
  personHash: string;
  request: Request;
  env: Env;
}): Promise<Response> {
  const parsedBody = await readJsonBody(request);
  const {
    email: rawEmail,
    mode,
    password_set_at: clientStamp,
  } = (parsedBody ?? {}) as { email?: unknown; mode?: unknown; password_set_at?: unknown };

  if (typeof rawEmail !== 'string' || !isValidEmail(rawEmail)) {
    return errorResponse({ message: 'invalid email', status: 400 });
  }
  if (typeof mode !== 'string' || !(CLAIM_MODES as readonly string[]).includes(mode)) {
    return errorResponse({ message: 'invalid mode', status: 400 });
  }
  if (clientStamp !== undefined && !isValidClientStamp(clientStamp)) {
    return errorResponse({ message: 'invalid password_set_at', status: 400 });
  }
  const claimMode = mode as ClaimMode;

  const normalizedEmail = normalizeEmail(rawEmail);
  // A client-supplied stamp lets the caller release the claim even if the
  // response is lost; absent, the Worker stamps with its own clock.
  const now = clientStamp ?? new Date().toISOString();

  const readPrevious = env.DB.prepare('SELECT password_set_at FROM person WHERE person_hash = ?1').bind(
    personHash,
  );
  let write: D1PreparedStatement;
  if (claimMode === 'update_only') {
    write = env.DB.prepare('UPDATE person SET password_set_at = ?2 WHERE person_hash = ?1').bind(personHash, now);
  } else {
    // if_unset only overwrites a NULL stamp; always overwrites unconditionally.
    // Email is only written when the row is created (the INSERT branch).
    const conflictGuard = claimMode === 'if_unset' ? 'WHERE person.password_set_at IS NULL' : '';
    write = env.DB.prepare(
      `INSERT INTO person (person_hash, email, created_at, password_set_at)
       VALUES (?1, ?2, ?3, ?3)
       ON CONFLICT(person_hash) DO UPDATE SET password_set_at = excluded.password_set_at ${conflictGuard}`,
    ).bind(personHash, normalizedEmail, now);
  }

  const [previousResult] = await env.DB.batch<{ password_set_at: string | null }>([readPrevious, write]);
  const previousRow = previousResult?.results[0];
  const rowExisted = previousRow !== undefined;
  const previousStamp = previousRow?.password_set_at ?? null;

  const claimed = claimMode === 'update_only' ? rowExisted : !(rowExisted && previousStamp !== null && claimMode === 'if_unset');
  let currentStamp: string | null = null;
  if (claimed) {
    currentStamp = now;
  } else if (rowExisted) {
    currentStamp = previousStamp;
  }

  return jsonResponse({
    body: {
      claimed,
      row_exists: claimMode === 'update_only' ? rowExisted : true,
      password_set_at: currentStamp,
      previous_set_at: claimed ? previousStamp : null,
    },
    status: 200,
  });
}

/** Handles `DELETE /person/{hash}/password-claim` — restores restore_to only
 *  if the column still equals password_set_at, so a newer claim is never undone. */
async function handlePasswordClaimRelease({
  personHash,
  request,
  env,
}: {
  personHash: string;
  request: Request;
  env: Env;
}): Promise<Response> {
  const parsedBody = await readJsonBody(request);
  const { password_set_at: passwordSetAt, restore_to: restoreTo } = (parsedBody ?? {}) as {
    password_set_at?: unknown;
    restore_to?: unknown;
  };

  if (typeof passwordSetAt !== 'string' || passwordSetAt.length === 0) {
    return errorResponse({ message: 'invalid password_set_at', status: 400 });
  }
  if (restoreTo !== null && typeof restoreTo !== 'string') {
    return errorResponse({ message: 'invalid restore_to', status: 400 });
  }

  const row = await env.DB.prepare(
    `UPDATE person SET password_set_at = ?3
     WHERE person_hash = ?1 AND password_set_at = ?2
     RETURNING person_hash`,
  )
    .bind(personHash, passwordSetAt, restoreTo)
    .first<{ person_hash: string }>();

  return jsonResponse({ body: { released: row !== null }, status: 200 });
}

/** Handles `POST /person/{hash}/password-resets` — claims one reset slot only
 *  if fewer than RESET_LIMIT slots exist in the rolling window. The count and
 *  the insert are a single statement, so parallel requests cannot exceed it. */
async function handleClaimResetSlot({ personHash, env }: { personHash: string; env: Env }): Promise<Response> {
  const nowMs = Date.now();
  const windowStart = nowMs - RESET_WINDOW_MS;
  const resetId = crypto.randomUUID();

  const [, insertResult, oldestResult] = await env.DB.batch<{ reset_id?: string; oldest?: number | null }>([
    env.DB.prepare('DELETE FROM password_reset WHERE person_hash = ?1 AND reset_at <= ?2').bind(
      personHash,
      windowStart,
    ),
    env.DB.prepare(
      `INSERT INTO password_reset (reset_id, person_hash, reset_at)
       SELECT ?1, ?2, ?3
       WHERE (SELECT COUNT(*) FROM password_reset WHERE person_hash = ?2 AND reset_at > ?4) < ?5
       RETURNING reset_id`,
    ).bind(resetId, personHash, nowMs, windowStart, RESET_LIMIT),
    env.DB.prepare(
      'SELECT MIN(reset_at) AS oldest FROM password_reset WHERE person_hash = ?1 AND reset_at > ?2',
    ).bind(personHash, windowStart),
  ]);

  if ((insertResult?.results.length ?? 0) > 0) {
    return jsonResponse({ body: { reset_id: resetId }, status: 200 });
  }

  const oldest = oldestResult?.results[0]?.oldest ?? nowMs;
  const retryAfterSeconds = Math.max(1, Math.ceil((oldest + RESET_WINDOW_MS - nowMs) / 1000));
  return jsonResponse({ body: { error: 'reset limit reached', retry_after_seconds: retryAfterSeconds }, status: 429 });
}

/** Handles `DELETE /person/{hash}/password-resets/{reset_id}` — idempotently
 *  frees one slot so a failed reset does not count against the limit. */
async function handleReleaseResetSlot({
  personHash,
  resetId,
  env,
}: {
  personHash: string;
  resetId: string;
  env: Env;
}): Promise<Response> {
  const row = await env.DB.prepare(
    'DELETE FROM password_reset WHERE reset_id = ?1 AND person_hash = ?2 RETURNING reset_id',
  )
    .bind(resetId, personHash)
    .first<{ reset_id: string }>();
  return jsonResponse({ body: { released: row !== null }, status: 200 });
}

/** Handles `GET /apps` — enumerates every registered app, ordered by created_at then app_id. */
async function handleGetApps({ env }: { env: Env }): Promise<Response> {
  const { results } = await env.DB.prepare(
    'SELECT app_id, display_name, description, created_at FROM app ORDER BY created_at ASC, app_id ASC',
  ).all<AppRecord>();

  return jsonResponse({ body: { apps: results ?? [] }, status: 200 });
}

/** Handles `POST /apps` — registers a new app. A duplicate app_id is a 409
 *  (registration is a create, not an upsert — unlike PUT /person). */
async function handlePostApp({ request, env }: { request: Request; env: Env }): Promise<Response> {
  let parsedBody: unknown;
  try {
    parsedBody = await request.json();
  } catch {
    return errorResponse({ message: 'invalid JSON body', status: 400 });
  }

  const {
    app_id: appId,
    display_name: displayName,
    description,
  } = (parsedBody ?? {}) as {
    app_id?: unknown;
    display_name?: unknown;
    description?: unknown;
  };

  if (typeof appId !== 'string' || !isValidAppId(appId)) {
    return errorResponse({ message: 'invalid app_id', status: 400 });
  }
  if (typeof displayName !== 'string' || displayName.trim().length === 0) {
    return errorResponse({ message: 'invalid display_name', status: 400 });
  }
  if (description !== undefined && description !== null && typeof description !== 'string') {
    return errorResponse({ message: 'invalid description', status: 400 });
  }

  const normalizedDescription = typeof description === 'string' ? description : null;
  const createdAt = new Date().toISOString();

  // ON CONFLICT DO NOTHING + RETURNING: a first insert returns the row; a
  // duplicate app_id returns no row (null), which we map to 409 without a
  // separate existence query or brittle constraint-error string matching.
  const row = await env.DB.prepare(
    `INSERT INTO app (app_id, display_name, description, created_at)
     VALUES (?1, ?2, ?3, ?4)
     ON CONFLICT(app_id) DO NOTHING
     RETURNING app_id, display_name, description, created_at`,
  )
    .bind(appId, displayName.trim(), normalizedDescription, createdAt)
    .first<AppRecord>();

  if (!row) {
    return errorResponse({ message: 'app already exists', status: 409 });
  }

  return jsonResponse({ body: row, status: 201 });
}

/** Handles `PATCH /apps/{app_id}` — edits display_name and/or description.
 *  app_id is immutable (never read from the body). A field omitted from the
 *  body is left unchanged; `description: null` explicitly clears it. */
async function handlePatchApp({
  appId,
  request,
  env,
}: {
  appId: string;
  request: Request;
  env: Env;
}): Promise<Response> {
  if (!isValidAppId(appId)) {
    return errorResponse({ message: 'invalid app_id', status: 400 });
  }

  let parsedBody: unknown;
  try {
    parsedBody = await request.json();
  } catch {
    return errorResponse({ message: 'invalid JSON body', status: 400 });
  }
  // A body that parses as valid JSON but isn't an object (e.g. `42` or `"x"`)
  // must not reach the `in` checks below — `in` throws a TypeError on a
  // non-object operand. Coerce any non-object to an empty patch (a no-op edit)
  // rather than crashing the handler with a 500.
  const body: { display_name?: unknown; description?: unknown } =
    typeof parsedBody === 'object' && parsedBody !== null
      ? (parsedBody as { display_name?: unknown; description?: unknown })
      : {};

  const existing = await env.DB.prepare(
    'SELECT app_id, display_name, description, created_at FROM app WHERE app_id = ?1',
  )
    .bind(appId)
    .first<AppRecord>();
  if (!existing) {
    return errorResponse({ message: 'app not found', status: 404 });
  }

  let displayName = existing.display_name;
  if ('display_name' in body) {
    if (typeof body.display_name !== 'string' || body.display_name.trim().length === 0) {
      return errorResponse({ message: 'invalid display_name', status: 400 });
    }
    displayName = body.display_name.trim();
  }

  let description = existing.description;
  if ('description' in body) {
    if (body.description !== null && typeof body.description !== 'string') {
      return errorResponse({ message: 'invalid description', status: 400 });
    }
    description = body.description;
  }

  const row = await env.DB.prepare(
    `UPDATE app SET display_name = ?1, description = ?2 WHERE app_id = ?3
     RETURNING app_id, display_name, description, created_at`,
  )
    .bind(displayName, description, appId)
    .first<AppRecord>();

  if (!row) {
    return errorResponse({ message: 'update failed', status: 500 });
  }

  return jsonResponse({ body: row, status: 200 });
}

/** Handles `DELETE /apps/{app_id}` — idempotent removal of a registry row.
 *  Returns 200 whether or not the row existed (mirrors DELETE /person/{hash}),
 *  so the provisioning-api cascade's registry cleanup is safe to repeat. */
async function handleDeleteApp({ appId, env }: { appId: string; env: Env }): Promise<Response> {
  if (!isValidAppId(appId)) {
    return errorResponse({ message: 'invalid app_id', status: 400 });
  }

  await env.DB.prepare('DELETE FROM app WHERE app_id = ?1').bind(appId).run();
  return jsonResponse({ body: { app_id: appId, deleted: true }, status: 200 });
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname === '/person') {
      if (request.method === 'PUT') {
        return handlePutPerson({ request, env });
      }
      if (request.method === 'GET') {
        return handleGetPerson({ url, env });
      }
      return errorResponse({ message: 'method not allowed', status: 405 });
    }

    if (url.pathname.startsWith('/person/')) {
      const [personHash = '', subresource, resetId, ...extraSegments] = url.pathname
        .slice('/person/'.length)
        .split('/');

      if (subresource === undefined) {
        if (request.method === 'DELETE') {
          return handleDeletePerson({ personHash, env });
        }
        return errorResponse({ message: 'method not allowed', status: 405 });
      }

      if (extraSegments.length > 0 || (resetId !== undefined && subresource !== 'password-resets')) {
        return errorResponse({ message: 'not found', status: 404 });
      }
      if (subresource !== 'password-claim' && subresource !== 'password-resets') {
        return errorResponse({ message: 'not found', status: 404 });
      }
      if (!PERSON_HASH_PATTERN.test(personHash)) {
        return errorResponse({ message: 'invalid person_hash', status: 400 });
      }

      if (subresource === 'password-claim') {
        if (request.method === 'POST') {
          return handlePasswordClaim({ personHash, request, env });
        }
        if (request.method === 'DELETE') {
          return handlePasswordClaimRelease({ personHash, request, env });
        }
        return errorResponse({ message: 'method not allowed', status: 405 });
      }

      if (resetId === undefined) {
        if (request.method === 'POST') {
          return handleClaimResetSlot({ personHash, env });
        }
        return errorResponse({ message: 'method not allowed', status: 405 });
      }
      if (request.method === 'DELETE') {
        return handleReleaseResetSlot({ personHash, resetId, env });
      }
      return errorResponse({ message: 'method not allowed', status: 405 });
    }

    if (url.pathname === '/people') {
      if (request.method === 'GET') {
        return handleGetPeople({ env });
      }
      return errorResponse({ message: 'method not allowed', status: 405 });
    }

    if (url.pathname === '/apps') {
      if (request.method === 'GET') {
        return handleGetApps({ env });
      }
      if (request.method === 'POST') {
        return handlePostApp({ request, env });
      }
      return errorResponse({ message: 'method not allowed', status: 405 });
    }

    if (url.pathname.startsWith('/apps/')) {
      const appId = url.pathname.slice('/apps/'.length);
      if (request.method === 'PATCH') {
        return handlePatchApp({ appId, request, env });
      }
      if (request.method === 'DELETE') {
        return handleDeleteApp({ appId, env });
      }
      return errorResponse({ message: 'method not allowed', status: 405 });
    }

    return errorResponse({ message: 'not found', status: 404 });
  },
};

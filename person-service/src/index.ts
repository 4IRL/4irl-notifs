// person-service: a standalone Cloudflare Worker owning the D1 reverse-index
// `person` table (person_hash -> email). Exposes a tiny HTTP API for
// upserting and looking up that mapping.
//
// This Worker performs NO authentication/authorization itself. In
// production, Cloudflare Access enforces access control at the edge in
// front of this Worker's route/custom domain (see wrangler.toml and
// docs/deploy-runbook.md Wave 2) — every request that reaches `fetch` below
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

const PERSON_HASH_PATTERN = /^[a-z2-7]{16}$/;
const MAX_EMAIL_LENGTH = 254;

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

    if (url.pathname === '/people') {
      if (request.method === 'GET') {
        return handleGetPeople({ env });
      }
      return errorResponse({ message: 'method not allowed', status: 405 });
    }

    return errorResponse({ message: 'not found', status: 404 });
  },
};

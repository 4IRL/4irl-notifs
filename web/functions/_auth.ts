// Cloudflare Access JWT validation for the admin UI's Pages Function proxy.
//
// Cloudflare Access, sitting in front of the whole notifs-admin hostname,
// CHALLENGES same-origin fetch POSTs (302 → login + `Www-Authenticate:
// Cloudflare-Access`, ignoring the valid CF_Authorization cookie it honors for
// GETs) — breaking provisioning (POST → GET downgrade → 405). The fix: the
// operator adds an Access **Bypass** policy on the `/v1` and `/people` paths so
// those paths are no longer edge-challenged, and THIS Function validates the
// Access JWT itself. See deploy-runbook §6b.

import { createRemoteJWKSet, jwtVerify } from 'jose';
import type { JWTVerifyGetKey } from 'jose';

import { jsonError } from './_proxy';
import type { Env } from './_proxy';

// Module-memoized JWKS resolvers, keyed by Access team domain, so the team's
// signing keys are cached across requests (workerd keeps the module instance
// warm) rather than refetched from the certs endpoint on every call.
const jwksByTeamDomain = new Map<string, ReturnType<typeof createRemoteJWKSet>>();

function remoteJwksFor(teamDomain: string): ReturnType<typeof createRemoteJWKSet> {
  let jwks = jwksByTeamDomain.get(teamDomain);
  if (jwks === undefined) {
    jwks = createRemoteJWKSet(new URL(`https://${teamDomain}/cdn-cgi/access/certs`));
    jwksByTeamDomain.set(teamDomain, jwks);
  }
  return jwks;
}

/** Extracts the `CF_Authorization` token from a raw `Cookie` header value. */
function tokenFromCookie(cookieHeader: string | null): string | null {
  if (cookieHeader === null) {
    return null;
  }
  for (const pair of cookieHeader.split(';')) {
    const trimmed = pair.trim();
    const eq = trimmed.indexOf('=');
    if (eq === -1) {
      continue;
    }
    if (trimmed.slice(0, eq) === 'CF_Authorization') {
      return trimmed.slice(eq + 1) || null;
    }
  }
  return null;
}

/**
 * Verifies the caller's Cloudflare Access JWT and returns the authenticated
 * admin's email (for audit forwarding) or an error `Response` to short-circuit.
 *
 * ENFORCEMENT GATING (fail closed): JWT auth is DISABLED only when
 * `env.DISABLE_ACCESS_AUTH === 'true'` — the explicit local-dev / `wrangler
 * pages dev` opt-out (no Access in front of the local Function); it returns
 * `{ ok: true, email: null }` without inspecting any token. Otherwise auth is
 * ENFORCED: if `ACCESS_JWT_AUD` or `ACCESS_TEAM_DOMAIN` is empty this fails
 * CLOSED with `500 {error:'proxy misconfigured'}` (it does NOT silently disable
 * auth). PRODUCTION leaves `DISABLE_ACCESS_AUTH` unset, so a missing
 * `ACCESS_JWT_AUD` blocks the API (500) rather than leaving it open — even if
 * the operator adds the Access Bypass on `/v1` and `/people` before the vars
 * are live.
 *
 * @param getKey - optional jose key resolver, used as a TEST SEAM so specs can
 *   supply a local JWKS. In production it defaults to a module-memoized
 *   `createRemoteJWKSet` pointed at the team's certs endpoint.
 */
export async function authenticateAdmin({
  request,
  env,
  getKey,
}: {
  request: Request;
  env: Env;
  getKey?: JWTVerifyGetKey;
}): Promise<{ ok: true; email: string | null } | { ok: false; response: Response }> {
  // Auth disabled (local dev only): explicit opt-out → allow, no email to
  // forward. Never set DISABLE_ACCESS_AUTH in production.
  if (env.DISABLE_ACCESS_AUTH === 'true') {
    return { ok: true, email: null };
  }
  // Enforced (default). Both vars MUST be configured: without them we can't
  // verify the audience or build the issuer / fetch the JWKS. Fail CLOSED with a
  // 500 (a deploy misconfiguration, not a caller error) rather than fail open —
  // symmetric handling for a missing ACCESS_JWT_AUD or ACCESS_TEAM_DOMAIN.
  if (!env.ACCESS_JWT_AUD || !env.ACCESS_TEAM_DOMAIN) {
    return { ok: false, response: jsonError({ status: 500, error: 'proxy misconfigured' }) };
  }

  // Prefer the header Access injects; fall back to the CF_Authorization cookie.
  const token =
    request.headers.get('Cf-Access-Jwt-Assertion') ??
    tokenFromCookie(request.headers.get('Cookie'));
  if (token === null) {
    return { ok: false, response: jsonError({ status: 401, error: 'unauthorized' }) };
  }

  const keyResolver = getKey ?? remoteJwksFor(env.ACCESS_TEAM_DOMAIN);
  try {
    const { payload } = await jwtVerify(token, keyResolver, {
      issuer: `https://${env.ACCESS_TEAM_DOMAIN}`,
      audience: env.ACCESS_JWT_AUD,
    });
    return { ok: true, email: (payload.email as string) ?? null };
  } catch {
    // Bad signature, expired, wrong iss/aud, or malformed → treat all as 401.
    return { ok: false, response: jsonError({ status: 401, error: 'unauthorized' }) };
  }
}

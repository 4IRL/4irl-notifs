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
 * ENFORCEMENT GATING: when `env.ACCESS_JWT_AUD` is empty/unset, JWT auth is
 * DISABLED and this returns `{ ok: true, email: null }` without inspecting any
 * token — this is the local-dev / `wrangler pages dev` path (no Access in front
 * of the local Function). PRODUCTION MUST set `ACCESS_JWT_AUD` (and
 * `ACCESS_TEAM_DOMAIN`), and the operator MUST NOT add the Access Bypass on
 * `/v1` and `/people` until `ACCESS_JWT_AUD` is set — otherwise those paths
 * would be briefly unauthenticated (edge challenge removed, Function auth off).
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
  // Auth disabled (local dev): no AUD configured → allow, no email to forward.
  if (!env.ACCESS_JWT_AUD) {
    return { ok: true, email: null };
  }
  // AUD is set but the team domain is missing: we can't build the issuer or
  // fetch the JWKS — a deploy misconfiguration, not a caller error.
  if (!env.ACCESS_TEAM_DOMAIN) {
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

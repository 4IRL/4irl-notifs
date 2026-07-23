// Shared Cloudflare Pages Function proxy helper for the admin UI.
//
// The admin SPA (notifs-admin.4irl.app) calls same-origin relative paths
// (`/v1/*` and `/people`); these Functions forward each request server-side to
// the Access-gated backends (notifs-api / notifs-people) using the proxy's
// Cloudflare Access **service token**, so the browser never makes a
// cross-origin request. See the design doc + plan under
// ~/code/plans/4irl-notifs/open/admin-ui-same-origin/.

import { authenticateAdmin } from './_auth';

/** Runtime bindings configured on the notifs-admin Pages project (NOT
 *  build-time VITE vars). The two URLs are plaintext vars; the two client
 *  credentials are encrypted Pages secrets. */
export interface Env {
  PROVISIONING_API_URL: string;
  PERSON_SERVICE_URL: string;
  PROXY_ACCESS_CLIENT_ID: string;
  PROXY_ACCESS_CLIENT_SECRET: string;
  /** Cloudflare Access team domain (e.g. `urls4irl.cloudflareaccess.com`) used
   *  as the JWKS host and the expected JWT issuer. Runtime Pages plaintext var;
   *  empty locally. */
  ACCESS_TEAM_DOMAIN: string;
  /** The notifs-admin Access application's Application Audience (AUD) tag — the
   *  expected JWT audience. Runtime Pages plaintext var; empty locally DISABLES
   *  JWT auth (see `_auth.ts` enforcement gating). */
  ACCESS_JWT_AUD: string;
}

/** Builds a JSON error response with the standard `{ error }` shape. */
export function jsonError({ status, error }: { status: number; error: string }): Response {
  return new Response(JSON.stringify({ error }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * Proxies an incoming same-origin admin request to an Access-gated backend,
 * authenticating with the proxy's Cloudflare Access service token.
 */
export async function proxyTo({
  request,
  upstreamBase,
  env,
}: {
  request: Request;
  upstreamBase: string;
  env: Env;
}): Promise<Response> {
  // Misconfiguration guard: without the service-token credentials every backend
  // call would be rejected by Access, so fail fast and never touch the network.
  // An unset upstream base (PROVISIONING_API_URL / PERSON_SERVICE_URL) is the
  // same class of deploy misconfiguration — guard it here so it surfaces an
  // accurate `500 proxy misconfigured` rather than a misleading
  // `502 upstream unreachable` after a doomed fetch to an empty/relative URL.
  // Each condition is checked so any one missing binding trips it.
  if (!env.PROXY_ACCESS_CLIENT_ID || !env.PROXY_ACCESS_CLIENT_SECRET || !upstreamBase) {
    return jsonError({ status: 500, error: 'proxy misconfigured' });
  }

  // Validate the caller's Cloudflare Access JWT (the CF_Authorization session
  // token) BEFORE any backend call. Access no longer edge-challenges these
  // paths (the operator adds a path-based Access **Bypass** on /v1 and /people
  // so same-origin POSTs aren't downgraded to a login redirect), so the Function
  // is now the authenticator. When ACCESS_JWT_AUD is unset this is a no-op
  // (auth disabled for local dev). See `_auth.ts` + deploy-runbook §6b.
  const auth = await authenticateAdmin({ request, env });
  if (!auth.ok) {
    return auth.response;
  }

  const url = new URL(request.url);
  // Trim trailing slashes on the base so an operator typo (e.g. a
  // `PROVISIONING_API_URL` ending in `/`) can't produce a double-slash path
  // that 404s. Mirrors `personClient.ts`'s `baseUrl.replace(/\/+$/, '')`.
  const trimmedUpstreamBase = upstreamBase.replace(/\/+$/, '');
  const upstreamUrl = `${trimmedUpstreamBase}${url.pathname}${url.search}`;

  // Build a fresh header set — do NOT forward the inbound Cookie (the admin
  // app's Access session cookie is not valid for the backend; the service token
  // is the backend's auth).
  const headers = new Headers();
  const contentType = request.headers.get('Content-Type');
  if (contentType !== null) {
    headers.set('Content-Type', contentType);
  }
  headers.set('CF-Access-Client-Id', env.PROXY_ACCESS_CLIENT_ID);
  headers.set('CF-Access-Client-Secret', env.PROXY_ACCESS_CLIENT_SECRET);
  if (auth.email !== null) {
    // Forwarded for audit; the backends read no user-identity header today.
    // This email comes from the **signature-verified** Access JWT (issuer +
    // audience checked against the team certs JWKS in `authenticateAdmin`), NOT
    // from an unverified inbound header — so it is trustworthy. (When
    // ACCESS_JWT_AUD is unset, auth is disabled and `email` is null, so no
    // audit header is forwarded — matching the pre-JWT behavior for local dev.)
    // The inbound Cookie is still never forwarded upstream (service token is the
    // backend's auth).
    headers.set('Cf-Access-Authenticated-User-Email', auth.email);
  }

  // Buffer the body rather than streaming `request.body`. The unit tests run
  // under Vitest/Node (undici), where a streaming request body requires
  // `duplex: 'half'`; a plain byte buffer is also trivial to assert against a
  // mocked fetch. workerd does not require `duplex` here either. No `duplex`
  // option is used anywhere in this file.
  const method = request.method;
  const body = method === 'GET' || method === 'HEAD' ? undefined : await request.arrayBuffer();

  let response: Response;
  try {
    // `redirect: 'manual'` is what makes an upstream Access-login redirect
    // observable instead of auto-followed — its only purpose here.
    response = await fetch(upstreamUrl, { method, headers, body, redirect: 'manual' });
  } catch {
    // Network / DNS / timeout reaching the backend.
    return jsonError({ status: 502, error: 'upstream unreachable' });
  }

  // A 3xx / opaqueredirect / 401 / 403 from the backend is Cloudflare Access
  // rejecting the proxy (login redirect or denied), never an app-level response
  // (the backends only emit 400/404/405/500 at the app layer — any 401/403 the
  // proxy observes is Access, e.g. the service token missing from the backend's
  // Service-Auth policy). Normalize all of them to a clean 502 rather than
  // re-echoing an Access login page/rejection to the SPA — the exact bug class
  // this proxy exists to fix.
  const isRedirect = response.status >= 300 && response.status < 400;
  // Defensive: any runtime that yields a filtered `opaqueredirect` response
  // (status 0) instead of preserving the 3xx status is still an Access redirect.
  // workers-types narrows `Response.type` to `'error' | 'default'` (workerd with
  // `redirect: 'manual'` preserves the real 3xx status, caught above, and never
  // produces `opaqueredirect`), so the literal comparison needs a string cast.
  const isOpaqueRedirect = (response.type as string) === 'opaqueredirect';
  const isAccessDenied = response.status === 401 || response.status === 403;
  if (isRedirect || isOpaqueRedirect || isAccessDenied) {
    return jsonError({ status: 502, error: 'upstream auth failed' });
  }

  // Otherwise stream the full upstream Response through unchanged — status,
  // body, and all upstream headers pass through verbatim (no allowlist). This
  // is safe because both backends are stateless internal services that never
  // emit origin-affecting headers (no Set-Cookie, no caching directives).
  return response;
}

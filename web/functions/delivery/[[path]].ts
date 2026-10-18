import { proxyTo } from '../_proxy';
import type { Env } from '../_proxy';
import { jsonError } from '../_http';

// Catch-all Pages Function for `/delivery/*` — the same-origin route to the
// delivery-api. delivery-api has no auth of its own, so this Function (a) forwards
// ONLY an explicit allowlist, (b) guards every non-GET against cross-site requests,
// then (c) hands off to `proxyTo`, which authenticates the Access JWT and strips the
// `/delivery` prefix. Order is allowlist -> guard -> proxyTo, so the guard runs
// BEFORE auth (an unauthenticated cross-site POST is 403, not 401).

const PREFIX = '/delivery';
const ADMIN_PREFIX = `${PREFIX}/v1/admin/`;
const MESSAGES_PATH = `${PREFIX}/v1/messages`;
const READYZ_PATH = `${PREFIX}/readyz`;

function notFound(): Response {
  return jsonError({ status: 404, error: 'not found' });
}

/** True when the request targets an allowlisted (method, path) pair. The pathname
 *  is the WHATWG-normalized one (dot-segments and `%2e` already resolved); encoded
 *  slashes/backslashes and empty segments are refused outright, never forwarded. */
function isAllowlisted({ method, pathname }: { method: string; pathname: string }): boolean {
  const lowered = pathname.toLowerCase();
  if (lowered.includes('%2f') || lowered.includes('%5c') || pathname.includes('//')) {
    return false;
  }
  if (pathname === READYZ_PATH) {
    return method === 'GET';
  }
  if (pathname === MESSAGES_PATH) {
    return method === 'POST';
  }
  // Any method, but only on a segment boundary with at least one segment after it.
  return pathname.startsWith(ADMIN_PREFIX) && pathname.length > ADMIN_PREFIX.length;
}

/** Cross-site guard for non-GET requests; returns a refusal response or null. */
function crossSiteGuard(request: Request): Response | null {
  if (request.method === 'GET') {
    return null;
  }
  const sameOrigin = request.headers.get('Sec-Fetch-Site') === 'same-origin';
  const hasCustomHeader = request.headers.has('X-Requested-With');
  if (!sameOrigin && !hasCustomHeader) {
    return jsonError({ status: 403, error: 'cross-site request refused' });
  }
  if (request.body !== null) {
    const contentType = (request.headers.get('Content-Type') ?? '').toLowerCase();
    const mediaType = contentType.split(';')[0].trim();
    if (mediaType !== 'application/json') {
      return jsonError({ status: 415, error: 'unsupported media type' });
    }
  }
  return null;
}

export const onRequest: PagesFunction<Env> = async (context) => {
  const { request, env } = context;
  const { pathname } = new URL(request.url);

  if (!isAllowlisted({ method: request.method, pathname })) {
    return notFound();
  }
  const refusal = crossSiteGuard(request);
  if (refusal !== null) {
    return refusal;
  }
  return proxyTo({
    request,
    upstreamBase: env.DELIVERY_API_URL,
    env,
    stripPrefix: PREFIX,
  });
};

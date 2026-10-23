import { proxyTo } from '../_proxy';
import type { Env } from '../_proxy';
import { jsonError } from '../_http';

// Catch-all Pages Function for `/delivery/*` — the same-origin route to the
// delivery-api. The app-facing routes (`/v1/messages`, heartbeat pings) need a per-app
// key this Function never holds, so it never forwards them: the operator's send-test
// goes to `/v1/admin/messages`. This Function (a) forwards
// ONLY an explicit allowlist, (b) guards every non-GET against cross-site requests,
// then (c) hands off to `proxyTo`, which authenticates the Access JWT and strips the
// `/delivery` prefix. Order is allowlist -> guard -> proxyTo, so the guard runs
// BEFORE auth (an unauthenticated cross-site POST is 403, not 401).

const PREFIX = '/delivery';
const ADMIN_PREFIX = `${PREFIX}/v1/admin/`;
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
  // Any method, but only on a segment boundary with at least one segment after it.
  return pathname.startsWith(ADMIN_PREFIX) && pathname.length > ADMIN_PREFIX.length;
}

/**
 * True when the request carries at least one byte of body. `request.body` cannot
 * answer this: workerd gives a body-less DELETE or POST an empty stream, never null
 * (only GET and HEAD get null), so the first chunk is read from a clone instead.
 */
async function hasBodyBytes(request: Request): Promise<boolean> {
  const stream = request.clone().body;
  if (stream === null) {
    return false;
  }
  const reader = stream.getReader();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        return false;
      }
      if (value.byteLength > 0) {
        return true;
      }
    }
  } finally {
    // Not awaited: cancelling one branch of a cloned stream only settles once the other
    // branch (the original body, still to be read by the proxy) finishes or is cancelled.
    void reader.cancel().catch(() => undefined);
  }
}

/** Cross-site guard for non-GET requests; returns a refusal response or null. */
async function crossSiteGuard(request: Request): Promise<Response | null> {
  if (request.method === 'GET') {
    return null;
  }
  const sameOrigin = request.headers.get('Sec-Fetch-Site') === 'same-origin';
  const hasCustomHeader = request.headers.has('X-Requested-With');
  if (!sameOrigin && !hasCustomHeader) {
    return jsonError({ status: 403, error: 'cross-site request refused' });
  }
  // A declared Content-Type or any real body must be JSON; a body-less DELETE or
  // POST (delete, pause, resume, replay) declares neither and passes.
  const declaredType = request.headers.get('Content-Type');
  if (declaredType !== null || (await hasBodyBytes(request))) {
    const mediaType = (declaredType ?? '').toLowerCase().split(';')[0].trim();
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
  const refusal = await crossSiteGuard(request);
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

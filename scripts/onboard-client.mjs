// Pluggable management target and HTTP client for declarative app onboarding.
//
// Step-8 seam: every caller above this module (manifest diffing, plan, apply, prune) depends
// only on the management API interface returned by `createAdminApi(http)` — list/create/patch/
// delete for destinations, routes and checks. None of that code ever builds a URL path, reads
// `ONBOARD_URL`, or knows about Cloudflare Access headers; it only calls the interface's methods.
// When generic-delivery master Step 8 ships app-managed destinations and checks, the only change
// needed is a new `createAppScopedApi(http)` (the app's own endpoint and token, reached through
// the same `resolveTarget`/`createHttp`) swapped in wherever `createAdminApi` is constructed
// today. Manifest parsing, `computePlan` and `apply`'s action loop stay untouched.
//
// `createAppApi(http)` is the one piece of this seam already app-facing today: `verify` always
// calls it with the app's own token (`ONBOARD_APP_ACCESS_ID`/`ONBOARD_APP_ACCESS_SECRET`), never
// the operator token.

// `URL.hostname` always returns the bracketed form for an IPv6 literal ('[::1]', never '::1'),
// so only the bracketed form is ever reachable here.
const LOOPBACK_HOSTS = new Set(['127.0.0.1', '[::1]', 'localhost']);
const DEFAULT_TIMEOUT_MS = 15000;

/** Thrown for any non-2xx HTTP response. Never carries a credential value. */
export class ApiError extends Error {
  constructor({ status, method, path, message }) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.method = method;
    this.path = path;
  }
}

function isLoopbackHost(hostname) {
  return LOOPBACK_HOSTS.has(hostname);
}

/**
 * Resolves the management (or, with idVar/secretVar overridden, app-facing) target from env:
 * the base URL plus an optional credential pair. Fails closed before any network call — every
 * rejection below is a thrown `Error`, never a fetch.
 *
 *   plan/apply:  resolveTarget({ env })
 *   verify:      resolveTarget({ env, idVar: 'ONBOARD_APP_ACCESS_ID', secretVar: 'ONBOARD_APP_ACCESS_SECRET' })
 */
export function resolveTarget({
  env = process.env,
  idVar = 'ONBOARD_ACCESS_ID',
  secretVar = 'ONBOARD_ACCESS_SECRET',
} = {}) {
  const urlText = env.ONBOARD_URL;
  if (!urlText) {
    throw new Error('ONBOARD_URL is required (no default)');
  }
  let url;
  try {
    url = new URL(urlText);
  } catch {
    throw new Error(`ONBOARD_URL is not a valid URL: ${urlText}`);
  }
  const loopback = isLoopbackHost(url.hostname);
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) {
    throw new Error(
      `ONBOARD_URL must be https:, or http: to a loopback address (127.0.0.1, ::1, localhost): ${urlText}`,
    );
  }

  const id = env[idVar];
  const secret = env[secretVar];
  const idSet = id !== undefined && id !== '';
  const secretSet = secret !== undefined && secret !== '';
  if (idSet !== secretSet) {
    throw new Error(`both or neither of ${idVar}/${secretVar} must be set`);
  }
  if (!loopback && !idSet) {
    throw new Error(`${idVar}/${secretVar} are required for a non-loopback ONBOARD_URL`);
  }

  return {
    baseUrl: url.origin,
    credentials: idSet ? { id, secret } : null,
  };
}

/**
 * The injected-fetch HTTP layer: timeout, Cloudflare Access headers and error wrapping, all in
 * one place so `createAdminApi`/`createAppApi` never touch `fetch` directly.
 */
export function createHttp({
  baseUrl,
  credentials,
  fetchImpl = fetch,
  timeoutMs = DEFAULT_TIMEOUT_MS,
}) {
  const trimmedBaseUrl = baseUrl.replace(/\/+$/, '');

  async function request({ method, path, body }) {
    const headers = { Accept: 'application/json' };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    if (credentials) {
      headers['CF-Access-Client-Id'] = credentials.id;
      headers['CF-Access-Client-Secret'] = credentials.secret;
    }

    let response;
    try {
      response = await fetchImpl(`${trimmedBaseUrl}${path}`, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(timeoutMs),
      });
    } catch (err) {
      throw new Error(`${method} ${path} failed: ${err.message}`);
    }

    const payload = await response.json().catch(() => null);
    if (!response.ok) {
      const message =
        payload && typeof payload.error === 'string' ? payload.error : `HTTP ${response.status}`;
      throw new ApiError({ status: response.status, method, path, message });
    }
    return payload;
  }

  return { request };
}

function encodeSegment(value) {
  return encodeURIComponent(String(value));
}

/** The management API interface — the Step-8 seam's contract (see top-of-file comment). */
export function createAdminApi(http) {
  return {
    async listDestinations() {
      const payload = await http.request({ method: 'GET', path: '/v1/admin/destinations' });
      return payload?.destinations ?? [];
    },
    async createDestination(body) {
      const payload = await http.request({ method: 'POST', path: '/v1/admin/destinations', body });
      return payload?.destination;
    },
    async patchDestination(name, body) {
      const payload = await http.request({
        method: 'PATCH',
        path: `/v1/admin/destinations/${encodeSegment(name)}`,
        body,
      });
      return payload?.destination;
    },
    async deleteDestination(name) {
      await http.request({
        method: 'DELETE',
        path: `/v1/admin/destinations/${encodeSegment(name)}`,
      });
    },

    async listRoutes() {
      const payload = await http.request({ method: 'GET', path: '/v1/admin/routes' });
      return payload?.routes ?? [];
    },
    async createRoute(body) {
      const payload = await http.request({ method: 'POST', path: '/v1/admin/routes', body });
      return payload?.route;
    },
    async patchRoute(id, body) {
      const payload = await http.request({
        method: 'PATCH',
        path: `/v1/admin/routes/${encodeSegment(id)}`,
        body,
      });
      return payload?.route;
    },
    async deleteRoute(id) {
      await http.request({ method: 'DELETE', path: `/v1/admin/routes/${encodeSegment(id)}` });
    },

    async listChecks() {
      const payload = await http.request({ method: 'GET', path: '/v1/admin/heartbeats' });
      return payload?.checks ?? [];
    },
    async createCheck(body) {
      const payload = await http.request({ method: 'POST', path: '/v1/admin/heartbeats', body });
      return payload?.check;
    },
    async patchCheck(id, body) {
      const payload = await http.request({
        method: 'PATCH',
        path: `/v1/admin/heartbeats/${encodeSegment(id)}`,
        body,
      });
      return payload?.check;
    },
    async deleteCheck(id) {
      await http.request({ method: 'DELETE', path: `/v1/admin/heartbeats/${encodeSegment(id)}` });
    },
  };
}

/** The app-facing calls used by `verify`: a real test send, and a real heartbeat ping. */
export function createAppApi(http) {
  return {
    async sendMessage(body) {
      return http.request({ method: 'POST', path: '/v1/messages?wait=10', body });
    },
    async pingCheck(appId, name) {
      return http.request({
        method: 'POST',
        path: `/v1/heartbeats/${encodeSegment(appId)}/${encodeSegment(name)}`,
      });
    },
  };
}

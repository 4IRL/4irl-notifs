// Pluggable management target and HTTP client for declarative app onboarding.
//
// Seam: every caller above this module (manifest diffing, plan, apply, prune) depends only on the
// management API interface returned by `createAdminApi(http)` (operator, `/v1/admin`) or
// `createAppScopedApi(http, appId)` (an app's own deploy, `/v1/apps/{app_id}`, with its delivery
// key) — list/create/patch/delete for destinations, routes and checks. None of that code ever
// builds a URL path, reads `ONBOARD_URL`, or knows about Cloudflare Access headers; it only calls
// the interface's methods. The CLI swaps one factory for the other at its single construction
// site; manifest parsing, `computePlan` and `apply`'s action loop are the same for both.
//
// `createAppApi(http)` is the other app-facing piece of this seam: `verify` always
// calls it with the app's own token (`ONBOARD_APP_ACCESS_ID`/`ONBOARD_APP_ACCESS_SECRET`), never
// the operator token, and with the app's delivery key (`ONBOARD_APP_KEY`, sent by `createHttp` as
// `Authorization: Bearer`): every app-facing delivery-api endpoint requires it.

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
 * The injected-fetch HTTP layer: timeout, Cloudflare Access headers, the app key and error
 * wrapping, all in one place so `createAdminApi`/`createAppApi` never touch `fetch` directly.
 * `appKey` (a delivery `dak_…` app key) is sent as `Authorization: Bearer`, alongside the Access
 * headers: Access is the network gate, the key is the app's identity. Absent, no `Authorization`
 * header is sent (the operator admin API takes none).
 */
export function createHttp({
  baseUrl,
  credentials,
  appKey = null,
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
    if (appKey) {
      headers.Authorization = `Bearer ${appKey}`;
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
      // `fetch` quotes the offending header value in the TypeError it throws for an invalid
      // header (the Bearer key, the Access secret), so a TypeError never has its text surfaced.
      // The one exception is the bare `fetch failed` undici raises for a network failure, which
      // carries no request data and is the useful text.
      if (err instanceof TypeError && err.message !== 'fetch failed') {
        throw new Error(
          `${method} ${path} failed: request could not be built (invalid header value or URL)`,
        );
      }
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

/**
 * The management API interface — the seam's contract (see top-of-file comment). The admin and
 * app-scoped endpoints mirror each other 1:1 under a different prefix, so both factories share this
 * one implementation and differ only in `prefix`. `filterRows` (default: keep everything) is applied
 * to every list result.
 */
function createManagementApi(http, prefix, { filterRows = (rows) => rows } = {}) {
  return {
    async listDestinations() {
      const payload = await http.request({ method: 'GET', path: `${prefix}/destinations` });
      return filterRows(payload?.destinations ?? []);
    },
    async createDestination(body) {
      const payload = await http.request({ method: 'POST', path: `${prefix}/destinations`, body });
      return payload?.destination;
    },
    async patchDestination(name, body) {
      const payload = await http.request({
        method: 'PATCH',
        path: `${prefix}/destinations/${encodeSegment(name)}`,
        body,
      });
      return payload?.destination;
    },
    async deleteDestination(name) {
      await http.request({
        method: 'DELETE',
        path: `${prefix}/destinations/${encodeSegment(name)}`,
      });
    },

    async listRoutes() {
      const payload = await http.request({ method: 'GET', path: `${prefix}/routes` });
      return filterRows(payload?.routes ?? []);
    },
    async createRoute(body) {
      const payload = await http.request({ method: 'POST', path: `${prefix}/routes`, body });
      return payload?.route;
    },
    async patchRoute(id, body) {
      const payload = await http.request({
        method: 'PATCH',
        path: `${prefix}/routes/${encodeSegment(id)}`,
        body,
      });
      return payload?.route;
    },
    async deleteRoute(id) {
      await http.request({ method: 'DELETE', path: `${prefix}/routes/${encodeSegment(id)}` });
    },

    async listChecks() {
      const payload = await http.request({ method: 'GET', path: `${prefix}/heartbeats` });
      return filterRows(payload?.checks ?? []);
    },
    async createCheck(body) {
      const payload = await http.request({ method: 'POST', path: `${prefix}/heartbeats`, body });
      return payload?.check;
    },
    async patchCheck(id, body) {
      const payload = await http.request({
        method: 'PATCH',
        path: `${prefix}/heartbeats/${encodeSegment(id)}`,
        body,
      });
      return payload?.check;
    },
    async deleteCheck(id) {
      await http.request({ method: 'DELETE', path: `${prefix}/heartbeats/${encodeSegment(id)}` });
    },
  };
}

/** The operator management API (`/v1/admin/...`). */
export function createAdminApi(http) {
  return createManagementApi(http, '/v1/admin');
}

/**
 * The app-scoped management API (`/v1/apps/{app_id}/...`), the same interface as `createAdminApi`.
 * The server derives ownership from the app key `http` sends, so every list returns only the
 * app's own rows; as defense in depth every list fails closed and keeps a row only when it says it
 * is app-owned by this app (`owner_kind === 'app'` and `owner_app_id === appId`), so a
 * misbehaving server (or an operator-owned row with no owner fields) can never make `--prune`
 * target another owner's object. `appId` is encoded into the path and must be non-empty: an
 * empty one would collapse the path onto a different route.
 */
export function createAppScopedApi(http, appId) {
  if (typeof appId !== 'string' || appId === '') {
    throw new Error('createAppScopedApi requires a non-empty app id');
  }
  return createManagementApi(http, `/v1/apps/${encodeSegment(appId)}`, {
    filterRows: (rows) =>
      rows.filter((row) => row.owner_kind === 'app' && row.owner_app_id === appId),
  });
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

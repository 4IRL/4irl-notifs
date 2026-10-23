import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { onRequest } from './[[path]]';
import { invoke, jsonResponse, makeEnv } from '../test-helpers';

const ORIGIN = 'https://notifs-admin.4irl.app';
const UPSTREAM = 'https://notifs-send.4irl.app';
const SAME_ORIGIN = { 'Sec-Fetch-Site': 'same-origin' };
const JSON_HEADERS = { 'Content-Type': 'application/json' };

describe('delivery proxy route', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function call({
    path,
    init = {},
    env = makeEnv(),
  }: {
    path: string;
    init?: RequestInit;
    env?: ReturnType<typeof makeEnv>;
  }): Promise<Response> {
    return invoke({ onRequest, request: new Request(`${ORIGIN}${path}`, init), env });
  }

  describe('forwarding', () => {
    it('maps GET /delivery/readyz to upstream /readyz', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { status: 'ok' } }));
      const response = await call({ path: '/delivery/readyz' });
      expect(response.status).toBe(200);
      expect(fetchMock.mock.calls[0][0]).toBe(`${UPSTREAM}/readyz`);
    });

    it('keeps the query string on an admin GET', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: [] }));
      await call({ path: '/delivery/v1/admin/deliveries?state=dead&limit=50' });
      expect(fetchMock.mock.calls[0][0]).toBe(
        `${UPSTREAM}/v1/admin/deliveries?state=dead&limit=50`,
      );
    });

    it.each(['POST', 'PATCH', 'DELETE'])(
      'forwards a %s with JSON body, Content-Type and service-token headers',
      async (method) => {
        fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
        await call({
          path: '/delivery/v1/admin/destinations/hook',
          init: {
            method,
            headers: { ...JSON_HEADERS, ...SAME_ORIGIN },
            body: JSON.stringify({ enabled: false }),
          },
        });
        const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
        expect(url).toBe(`${UPSTREAM}/v1/admin/destinations/hook`);
        expect(init.method).toBe(method);
        const headers = init.headers as Headers;
        expect(headers.get('Content-Type')).toBe('application/json');
        expect(headers.get('CF-Access-Client-Id')).toBe('id');
        expect(headers.get('CF-Access-Client-Secret')).toBe('sec');
        expect(new TextDecoder().decode(init.body as ArrayBuffer)).toBe('{"enabled":false}');
      },
    );

    it('forwards POST /delivery/v1/admin/messages?wait=10 to /v1/admin/messages', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 202, body: {} }));
      await call({
        path: '/delivery/v1/admin/messages?wait=10',
        init: { method: 'POST', headers: { ...JSON_HEADERS, ...SAME_ORIGIN }, body: '{}' },
      });
      expect(fetchMock.mock.calls[0][0]).toBe(`${UPSTREAM}/v1/admin/messages?wait=10`);
    });

    it.each([409, 422, 503])('passes a %s upstream body through unchanged', async (status) => {
      fetchMock.mockResolvedValue(jsonResponse({ status, body: { error: 'upstream says no' } }));
      const response = await call({ path: '/delivery/readyz' });
      expect(response.status).toBe(status);
      expect(await response.json()).toEqual({ error: 'upstream says no' });
    });

    it('returns 500 proxy misconfigured when DELIVERY_API_URL is empty', async () => {
      const response = await call({
        path: '/delivery/readyz',
        env: makeEnv({ DELIVERY_API_URL: '' }),
      });
      expect(response.status).toBe(500);
      expect(await response.json()).toEqual({ error: 'proxy misconfigured' });
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('body-less DELETE and replay POST are allowed with Sec-Fetch-Site: same-origin', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
      await call({
        path: '/delivery/v1/admin/routes/7',
        init: { method: 'DELETE', headers: SAME_ORIGIN },
      });
      await call({
        path: '/delivery/v1/admin/deliveries/9/replay',
        init: { method: 'POST', headers: SAME_ORIGIN },
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    it('forwards GET /delivery/v1/admin/scheduled-messages with its query string', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
      await call({ path: '/delivery/v1/admin/scheduled-messages?limit=50' });
      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe(`${UPSTREAM}/v1/admin/scheduled-messages?limit=50`);
      expect(init.method).toBe('GET');
    });

    it('forwards a body-less DELETE /delivery/v1/admin/scheduled-messages/{id}', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
      await call({
        path: '/delivery/v1/admin/scheduled-messages/0b1f6c2e-7a4d-4f4e-9c1a-2d5e8f3a9b10',
        init: { method: 'DELETE', headers: { 'X-Requested-With': 'admin-ui' } },
      });
      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe(
        `${UPSTREAM}/v1/admin/scheduled-messages/0b1f6c2e-7a4d-4f4e-9c1a-2d5e8f3a9b10`,
      );
      expect(init.method).toBe('DELETE');
    });

    it('forwards the app identity admin paths: a body-less approve POST, and revoke and unbind', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 201, body: {} }));
      await call({
        path: '/delivery/v1/admin/app-identity/requests/7/approve',
        init: { method: 'POST', headers: { 'X-Requested-With': 'admin-ui' } },
      });
      await call({
        path: '/delivery/v1/admin/app-identity/keys/3/revoke',
        init: { method: 'POST', headers: SAME_ORIGIN },
      });
      await call({
        path: '/delivery/v1/admin/app-identity/bindings/urls4irl',
        init: { method: 'DELETE', headers: SAME_ORIGIN },
      });
      await call({ path: '/delivery/v1/admin/app-identity/requests' });
      const forwarded = fetchMock.mock.calls.map((callArgs) => {
        const [url, init] = callArgs as [string, RequestInit];
        return `${init.method} ${url}`;
      });
      expect(forwarded).toEqual([
        `POST ${UPSTREAM}/v1/admin/app-identity/requests/7/approve`,
        `POST ${UPSTREAM}/v1/admin/app-identity/keys/3/revoke`,
        `DELETE ${UPSTREAM}/v1/admin/app-identity/bindings/urls4irl`,
        `GET ${UPSTREAM}/v1/admin/app-identity/requests`,
      ]);
    });

    it('refuses an app identity approve POST that carries no cross-site guard header', async () => {
      const response = await call({
        path: '/delivery/v1/admin/app-identity/requests/7/approve',
        init: { method: 'POST' },
      });
      expect(response.status).toBe(403);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('never forwards the app-facing exchange endpoint', async () => {
      const response = await call({
        path: '/delivery/v1/app-identity/exchange',
        init: { method: 'POST', headers: { ...JSON_HEADERS, ...SAME_ORIGIN }, body: '{}' },
      });
      expect(response.status).toBe(404);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    const heartbeatRows: Array<{ method: string; path: string; body?: string }> = [
      { method: 'GET', path: '/delivery/v1/admin/heartbeats' },
      { method: 'GET', path: '/delivery/v1/admin/heartbeats/7' },
      { method: 'POST', path: '/delivery/v1/admin/heartbeats', body: '{"app_id":"a"}' },
      { method: 'PATCH', path: '/delivery/v1/admin/heartbeats/7', body: '{"period_seconds":120}' },
      { method: 'DELETE', path: '/delivery/v1/admin/heartbeats/7' },
      { method: 'POST', path: '/delivery/v1/admin/heartbeats/7/pause' },
      { method: 'POST', path: '/delivery/v1/admin/heartbeats/7/resume' },
    ];

    it.each(heartbeatRows)(
      'forwards $method $path to the upstream admin route',
      async ({ method, path, body }) => {
        fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
        const response = await call({
          path,
          init: {
            method,
            headers: method === 'GET' ? {} : { ...JSON_HEADERS, ...SAME_ORIGIN },
            ...(body === undefined ? {} : { body }),
          },
        });
        expect(response.status).toBe(200);
        const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
        expect(url).toBe(`${UPSTREAM}${path.replace('/delivery', '')}`);
        expect(init.method).toBe(method);
      },
    );

    it('resolves an empty segment plus a dot-segment to an admin path, never a ping path', async () => {
      // `admin//..` collapses back to `admin/`, so this stays under the admin prefix
      // (upstream answers 404 for it: there is no admin ping route).
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
      await call({
        path: '/delivery/v1/admin//../heartbeats/urls4irl/nightly_backup',
        init: { method: 'POST', headers: { ...JSON_HEADERS, ...SAME_ORIGIN } },
      });
      const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe(`${UPSTREAM}/v1/admin/heartbeats/urls4irl/nightly_backup`);
    });

    it('accepts X-Requested-With in place of Sec-Fetch-Site', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
      await call({
        path: '/delivery/v1/admin/routes/7',
        init: { method: 'DELETE', headers: { 'X-Requested-With': 'admin-ui' } },
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
  });

  describe('negative allowlist', () => {
    const rows: Array<{ method: string; path: string }> = [
      { method: 'GET', path: '/delivery/v1/messages' },
      { method: 'POST', path: '/delivery/v1/messages' },
      { method: 'POST', path: '/delivery/v1/messages?wait=10' },
      { method: 'GET', path: '/delivery/healthz' },
      { method: 'GET', path: '/delivery/other' },
      { method: 'GET', path: '/delivery' },
      { method: 'GET', path: '/delivery/v1/adminx' },
      { method: 'GET', path: '/delivery/v1/adminx/destinations' },
      { method: 'GET', path: '/delivery/v1/admin' },
      { method: 'GET', path: '/delivery/v1/admin/' },
      { method: 'GET', path: '/delivery/v1/admin/../messages' },
      { method: 'GET', path: '/delivery/v1/admin/%2e%2e/x' },
      { method: 'GET', path: '/delivery/v1/admin/%2E%2E/x' },
      { method: 'GET', path: '//delivery/v1/admin/x' },
      { method: 'GET', path: '/delivery//v1/admin/x' },
      { method: 'GET', path: '/delivery/v1//admin/x' },
      { method: 'GET', path: '/delivery/v1/admin//x' },
      { method: 'GET', path: '/delivery/v1//messages' },
      { method: 'GET', path: '/delivery/v1/admin/a%2fb' },
      { method: 'GET', path: '/delivery/v1/admin/a%2Fb' },
      { method: 'GET', path: '/delivery/v1/admin/a%5Cb' },
      { method: 'GET', path: '/delivery/v1/admin/a%5cb' },
      { method: 'GET', path: '/delivery/v1/admin%2fdestinations' },
      { method: 'GET', path: '/delivery/v1/admin%2Fdestinations' },
      { method: 'PUT', path: '/delivery/v1/messages' },
      { method: 'DELETE', path: '/delivery/v1/messages' },
      { method: 'PATCH', path: '/delivery/v1/messages' },
      { method: 'POST', path: '/delivery/readyz' },
      { method: 'POST', path: '/delivery/v1/messages/abc' },
      { method: 'GET', path: '/delivery/v1/messages/abc' },
      // Pings are app-to-Tunnel only: the admin proxy never forwards them.
      { method: 'POST', path: '/delivery/v1/heartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/heartbeats/urls4irl/nightly_backup/fail' },
      { method: 'GET', path: '/delivery/v1/heartbeats/urls4irl/nightly_backup' },
      { method: 'GET', path: '/delivery/v1/heartbeats' },
      { method: 'POST', path: '/delivery/v1/heartbeats' },
      { method: 'GET', path: '/delivery/v1/admin/heartbeats%2f7' },
      { method: 'GET', path: '/delivery/v1/adminheartbeats' },
      // An admin-prefix path must never reach the app-facing ping paths: dot-segments
      // resolve to a non-allowlisted path, and encoded slashes/backslashes are refused.
      { method: 'POST', path: '/delivery/v1/admin/../heartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/../heartbeats/urls4irl/nightly_backup/fail' },
      { method: 'POST', path: '/delivery/v1/admin/%2e%2e/heartbeats/urls4irl/nightly_backup/fail' },
      { method: 'POST', path: '/delivery/v1/admin/%2E%2E/heartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/.%2e/heartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/./../heartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/x/../../heartbeats/urls4irl/nightly_backup' },
      {
        method: 'POST',
        path: '/delivery/v1/admin/heartbeats/../../heartbeats/urls4irl/nightly_backup',
      },
      { method: 'POST', path: '/delivery/v1/admin/..%2fheartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/..%2Fheartbeats/urls4irl/nightly_backup/fail' },
      { method: 'POST', path: '/delivery/v1/admin/%2e%2e%2fheartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/..%5cheartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/..\\heartbeats/urls4irl/nightly_backup' },
      { method: 'POST', path: '/delivery/v1/admin/%2e%2e\\heartbeats/urls4irl/nightly_backup' },
      // The app-scoped surface (`/v1/apps/{app_id}/...`) needs the calling app's own delivery
      // key, so the admin proxy never forwards it either.
      { method: 'GET', path: '/delivery/v1/apps/urls4irl/destinations' },
      { method: 'POST', path: '/delivery/v1/apps/urls4irl/destinations' },
      { method: 'DELETE', path: '/delivery/v1/apps/urls4irl/routes/1' },
      { method: 'GET', path: '/delivery/v1/apps/urls4irl/checks' },
      // The app-facing exchange stays unreachable from an admin-prefix path: dot-segments
      // resolve out of the admin prefix, and encoded slashes are refused.
      { method: 'POST', path: '/delivery/v1/app-identity/exchange' },
      { method: 'POST', path: '/delivery/v1/admin/../app-identity/exchange' },
      { method: 'POST', path: '/delivery/v1/admin/%2e%2e/app-identity/exchange' },
      { method: 'POST', path: '/delivery/v1/admin/%2E%2E/app-identity/exchange' },
      { method: 'POST', path: '/delivery/v1/admin/..%2fapp-identity/exchange' },
      { method: 'POST', path: '/delivery/v1/admin/%2e%2e%2fapp-identity/exchange' },
      { method: 'POST', path: '/delivery/v1/admin/..%2Fapp-identity/exchange' },
    ];

    it.each(rows)('$method $path -> 404 with no upstream fetch', async ({ method, path }) => {
      const init: RequestInit = { method, headers: { ...SAME_ORIGIN, 'X-Requested-With': 'x' } };
      const response = await call({ path, init });
      expect(response.status).toBe(404);
      expect(await response.json()).toEqual({ error: 'not found' });
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('an unauthenticated non-allowlisted path is 404, not 401', async () => {
      const response = await call({
        path: '/delivery/other',
        env: makeEnv({
          DISABLE_ACCESS_AUTH: '',
          ACCESS_JWT_AUD: 'aud',
          ACCESS_TEAM_DOMAIN: 'team.cloudflareaccess.com',
        }),
      });
      expect(response.status).toBe(404);
    });
  });

  describe('cross-site guard', () => {
    const adminPath = '/delivery/v1/admin/destinations';

    it('refuses a cross-site Sec-Fetch-Site', async () => {
      const response = await call({
        path: adminPath,
        init: {
          method: 'POST',
          headers: { ...JSON_HEADERS, 'Sec-Fetch-Site': 'cross-site' },
          body: '{}',
        },
      });
      expect(response.status).toBe(403);
      expect(await response.json()).toEqual({ error: 'cross-site request refused' });
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('refuses a non-GET with no guard header at all', async () => {
      const response = await call({
        path: adminPath,
        init: { method: 'POST', headers: JSON_HEADERS, body: '{}' },
      });
      expect(response.status).toBe(403);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('refuses a text/plain body with 415', async () => {
      const response = await call({
        path: adminPath,
        init: {
          method: 'POST',
          headers: { ...SAME_ORIGIN, 'Content-Type': 'text/plain' },
          body: '{}',
        },
      });
      expect(response.status).toBe(415);
      expect(await response.json()).toEqual({ error: 'unsupported media type' });
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('refuses a body with no Content-Type with 415', async () => {
      const response = await call({
        path: adminPath,
        init: { method: 'POST', headers: SAME_ORIGIN, body: new Uint8Array([123, 125]) },
      });
      expect(response.status).toBe(415);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    // workerd hands a handler an empty ReadableStream (never null) as the body of a
    // body-less DELETE or POST, while Node's undici reports null for the same call, so
    // only a stream-bodied request reproduces what the deployed Function sees.
    function streamOf(bytes: number[]): ReadableStream<Uint8Array> {
      return new ReadableStream<Uint8Array>({
        start(controller) {
          if (bytes.length > 0) {
            controller.enqueue(new Uint8Array(bytes));
          }
          controller.close();
        },
      });
    }

    it.each([
      ['DELETE', '/delivery/v1/admin/heartbeats/7'],
      ['POST', '/delivery/v1/admin/heartbeats/7/pause'],
      ['POST', '/delivery/v1/admin/heartbeats/7/resume'],
      ['POST', '/delivery/v1/admin/deliveries/9/replay'],
      ['DELETE', '/delivery/v1/admin/routes/7'],
    ])(
      'forwards a body-less %s %s whose body is an empty stream (the workerd shape)',
      async (method, path) => {
        fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
        const response = await call({
          path,
          init: {
            method,
            headers: { ...SAME_ORIGIN, 'X-Requested-With': 'admin-ui', 'Content-Length': '0' },
            body: streamOf([]),
            duplex: 'half',
          } as RequestInit,
        });
        expect(response.status).toBe(200);
        expect(fetchMock).toHaveBeenCalledTimes(1);
      },
    );

    it('still refuses a non-empty stream body with no Content-Type with 415', async () => {
      const response = await call({
        path: adminPath,
        init: {
          method: 'POST',
          headers: SAME_ORIGIN,
          body: streamOf([123, 125]),
          duplex: 'half',
        } as RequestInit,
      });
      expect(response.status).toBe(415);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('still refuses a text/plain Content-Type even when the stream body is empty', async () => {
      const response = await call({
        path: adminPath,
        init: {
          method: 'POST',
          headers: { ...SAME_ORIGIN, 'Content-Type': 'text/plain' },
          body: streamOf([]),
          duplex: 'half',
        } as RequestInit,
      });
      expect(response.status).toBe(415);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('accepts application/json with a charset parameter', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
      const response = await call({
        path: adminPath,
        init: {
          method: 'POST',
          headers: { ...SAME_ORIGIN, 'Content-Type': 'application/json; charset=utf-8' },
          body: '{}',
        },
      });
      expect(response.status).toBe(200);
    });

    it.each(['HEAD', 'OPTIONS'])('refuses %s without guard headers', async (method) => {
      const response = await call({ path: adminPath, init: { method } });
      expect(response.status).toBe(403);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('allows a GET without any guard headers', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: [] }));
      const response = await call({ path: adminPath });
      expect(response.status).toBe(200);
    });
  });

  describe('JWT auth enabled', () => {
    const TEAM_DOMAIN = 'urls4irl.cloudflareaccess.com';
    const authEnv = () =>
      makeEnv({ DISABLE_ACCESS_AUTH: '', ACCESS_JWT_AUD: 'aud', ACCESS_TEAM_DOMAIN: TEAM_DOMAIN });

    it('returns 401 without a JWT for an allowlisted GET', async () => {
      const response = await call({ path: '/delivery/readyz', env: authEnv() });
      expect(response.status).toBe(401);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('returns 401 without a JWT for a same-origin non-GET', async () => {
      const response = await call({
        path: '/delivery/v1/admin/routes/1',
        init: { method: 'DELETE', headers: SAME_ORIGIN },
        env: authEnv(),
      });
      expect(response.status).toBe(401);
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it('returns 403 (guard wins over auth) for an unauthenticated cross-site POST', async () => {
      const response = await call({
        path: '/delivery/v1/admin/destinations',
        init: {
          method: 'POST',
          headers: { ...JSON_HEADERS, 'Sec-Fetch-Site': 'cross-site' },
          body: '{}',
        },
        env: authEnv(),
      });
      expect(response.status).toBe(403);
      expect(fetchMock).not.toHaveBeenCalled();
    });
  });
});

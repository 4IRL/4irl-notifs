import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { proxyTo, type Env } from './_proxy';

const UPSTREAM = 'https://notifs-api.4irl.app';

describe('proxyTo', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function makeEnv(overrides: Partial<Env> = {}): Env {
    return {
      PROVISIONING_API_URL: 'https://notifs-api.4irl.app',
      PERSON_SERVICE_URL: 'https://notifs-people.4irl.app',
      PROXY_ACCESS_CLIENT_ID: 'id',
      PROXY_ACCESS_CLIENT_SECRET: 'sec',
      ...overrides,
    };
  }

  function jsonResponse({ status, body }: { status: number; body: unknown }): Response {
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  it('forwards a GET with service-token headers and returns the upstream response unchanged', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    const request = new Request('https://notifs-admin.4irl.app/v1/users', {
      method: 'GET',
      headers: {
        'Cf-Access-Authenticated-User-Email': 'admin@x.com',
        // The admin-app Access session cookie must NOT be forwarded upstream.
        Cookie: 'CF_Authorization=inbound-cookie',
      },
    });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-api.4irl.app/v1/users');
    // A regression that drops `redirect: 'manual'` must fail a core case, not
    // only the dedicated 3xx case below.
    expect(calledInit).toMatchObject({ method: 'GET', redirect: 'manual' });

    const headers = calledInit.headers as Headers;
    expect(headers.get('CF-Access-Client-Id')).toBe('id');
    expect(headers.get('CF-Access-Client-Secret')).toBe('sec');
    expect(headers.get('Cf-Access-Authenticated-User-Email')).toBe('admin@x.com');
    expect(headers.get('Cookie')).toBeNull();

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ users: [] });
  });

  it('omits the user-email header entirely when the inbound request lacks it', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    // No `Cf-Access-Authenticated-User-Email` on the inbound request.
    const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' });

    await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    const [, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = calledInit.headers as Headers;
    // The header must be truly absent (null), never the literal string 'null'
    // from an unguarded `.set(name, request.headers.get(name))`. Covers the
    // `!== null` guard in `_proxy.ts`.
    expect(headers.get('Cf-Access-Authenticated-User-Email')).toBeNull();
    expect(headers.has('Cf-Access-Authenticated-User-Email')).toBe(false);
  });

  it('buffers and forwards a POST JSON body (via arrayBuffer) and content-type unchanged', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { ok: true } }));
    const payload = JSON.stringify({ app_id: 'urls4irl', email: 'alice@example.com' });
    const request = new Request('https://notifs-admin.4irl.app/v1/provision', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: payload,
    });

    await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-api.4irl.app/v1/provision');
    expect((calledInit.headers as Headers).get('Content-Type')).toBe('application/json');
    const forwardedBody = new TextDecoder().decode(calledInit.body as ArrayBuffer);
    expect(forwardedBody).toBe(payload);
  });

  it('passes a non-2xx app response (404) through unchanged', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 404, body: { error: 'not found' } }));
    const request = new Request('https://notifs-admin.4irl.app/v1/users/u_missing', {
      method: 'GET',
    });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: 'not found' });
  });

  it('preserves the querystring on the upstream URL', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    const request = new Request('https://notifs-admin.4irl.app/v1/users?x=1', { method: 'GET' });

    await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    expect(fetchMock.mock.calls[0][0]).toBe('https://notifs-api.4irl.app/v1/users?x=1');
  });

  it('normalizes an upstream 302 login redirect to 502 {error: "upstream auth failed"}', async () => {
    // `redirect: 'manual'` is what makes this observable instead of auto-followed.
    fetchMock.mockResolvedValue(new Response(null, { status: 302 }));
    const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    expect(response.status).toBe(502);
    expect(await response.json()).toEqual({ error: 'upstream auth failed' });
  });

  it.each([401, 403])(
    'normalizes an upstream %i to 502 {error: "upstream auth failed"}',
    async (status) => {
      fetchMock.mockResolvedValue(jsonResponse({ status, body: { error: 'denied' } }));
      const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' });

      const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

      expect(response.status).toBe(502);
      expect(await response.json()).toEqual({ error: 'upstream auth failed' });
    },
  );

  it('returns 500 {error: "proxy misconfigured"} and never calls fetch when the client id is empty', async () => {
    const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' });

    const response = await proxyTo({
      request,
      upstreamBase: UPSTREAM,
      env: makeEnv({ PROXY_ACCESS_CLIENT_ID: '', PROXY_ACCESS_CLIENT_SECRET: 'sec' }),
    });

    expect(response.status).toBe(500);
    expect(await response.json()).toEqual({ error: 'proxy misconfigured' });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('returns 500 {error: "proxy misconfigured"} and never calls fetch when the client secret is empty', async () => {
    const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' });

    const response = await proxyTo({
      request,
      upstreamBase: UPSTREAM,
      env: makeEnv({ PROXY_ACCESS_CLIENT_ID: 'id', PROXY_ACCESS_CLIENT_SECRET: '' }),
    });

    expect(response.status).toBe(500);
    expect(await response.json()).toEqual({ error: 'proxy misconfigured' });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('returns 500 {error: "proxy misconfigured"} and never calls fetch when the upstream base is empty', async () => {
    const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' });

    const response = await proxyTo({
      request,
      upstreamBase: '',
      env: makeEnv(),
    });

    expect(response.status).toBe(500);
    expect(await response.json()).toEqual({ error: 'proxy misconfigured' });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('returns 502 {error: "upstream unreachable"} when the upstream fetch rejects', async () => {
    fetchMock.mockRejectedValue(new Error('network down'));
    const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    expect(response.status).toBe(502);
    expect(await response.json()).toEqual({ error: 'upstream unreachable' });
  });
});

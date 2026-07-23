// @vitest-environment node
//
// Runs in the node environment (not the project-default jsdom): jose's webapi
// build fails `instanceof Uint8Array` checks under jsdom's separate realm, and
// this Pages-Function proxy is pure Worker/Node code that needs no DOM.
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { SignJWT, exportJWK, generateKeyPair } from 'jose';
import type { JWK } from 'jose';

import { proxyTo } from './_proxy';
import { jsonResponse, makeEnv } from './test-helpers';

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

  it('forwards a GET with service-token headers and returns the upstream response unchanged', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    const request = new Request('https://notifs-admin.4irl.app/v1/users', {
      method: 'GET',
      headers: {
        // An inbound email header must NOT be forwarded — the audit email now
        // comes only from a validated JWT (auth is disabled here via makeEnv's
        // DISABLE_ACCESS_AUTH:'true'), so this inbound value is ignored, not trusted.
        'Cf-Access-Authenticated-User-Email': 'spoofed@x.com',
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
    // Auth disabled (DISABLE_ACCESS_AUTH:'true') → no validated email → header not
    // set, and the unverified inbound email header is dropped, never forwarded.
    expect(headers.get('Cf-Access-Authenticated-User-Email')).toBeNull();
    expect(headers.get('Cookie')).toBeNull();

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ users: [] });
  });

  it('forwards a HEAD without a body (skips arrayBuffer) and returns the upstream response', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    const request = new Request('https://notifs-admin.4irl.app/v1/users', { method: 'HEAD' });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: makeEnv() });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-api.4irl.app/v1/users');
    // Like GET, a HEAD must never buffer a request body — the
    // `method === 'GET' || method === 'HEAD'` guard leaves `body` undefined so
    // `request.arrayBuffer()` is skipped (a HEAD Request has no body to read).
    expect(calledInit.method).toBe('HEAD');
    expect(calledInit.body).toBeUndefined();

    expect(response.status).toBe(200);
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

  it('normalizes a filtered opaqueredirect response to 502 {error: "upstream auth failed"}', async () => {
    // Defensive branch: some runtimes surface an Access login redirect as a
    // filtered `opaqueredirect` response (status 0, `type: 'opaqueredirect'`)
    // instead of preserving the real 3xx status. workers-types narrows
    // `Response.type` to `'error' | 'default'` so this shape isn't constructible
    // via `new Response(...)`; mock a Response-like object to exercise the
    // `(response.type as string) === 'opaqueredirect'` guard in `_proxy.ts`.
    const opaqueRedirect = { status: 0, type: 'opaqueredirect' } as unknown as Response;
    fetchMock.mockResolvedValue(opaqueRedirect);
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

// Integration: with ACCESS_JWT_AUD set, proxyTo enforces the Access JWT before
// any backend call. proxyTo does not expose the `getKey` seam, so the team certs
// JWKS is served through the same mocked global fetch (createRemoteJWKSet fetches
// it), consistent with how `authenticateAdmin` resolves keys in production.
describe('proxyTo with JWT auth enabled', () => {
  const TEAM_DOMAIN = 'urls4irl.cloudflareaccess.com';
  const ISSUER = `https://${TEAM_DOMAIN}`;
  const AUD = 'proxy-aud-tag';
  const KID = 'proxy-test-key';
  const CERTS_URL = `${ISSUER}/cdn-cgi/access/certs`;
  const UPSTREAM_URL = 'https://notifs-api.4irl.app/v1/users';

  const fetchMock = vi.fn();
  let privateKey: CryptoKey;
  let publicJwk: JWK;

  beforeAll(async () => {
    const pair = await generateKeyPair('RS256', { extractable: true });
    privateKey = pair.privateKey;
    publicJwk = { ...(await exportJWK(pair.publicKey)), kid: KID, alg: 'RS256' };
  });

  beforeEach(() => {
    fetchMock.mockReset();
    // Serve the JWKS for the certs endpoint; everything else is the upstream.
    fetchMock.mockImplementation((url: string) => {
      if (url === CERTS_URL) {
        return Promise.resolve(jsonResponse({ status: 200, body: { keys: [publicJwk] } }));
      }
      return Promise.resolve(jsonResponse({ status: 200, body: { users: [] } }));
    });
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // makeEnv defaults DISABLE_ACCESS_AUTH:'true'; clear it to exercise enforcement.
  const jwtEnv = () =>
    makeEnv({ ACCESS_TEAM_DOMAIN: TEAM_DOMAIN, ACCESS_JWT_AUD: AUD, DISABLE_ACCESS_AUTH: '' });

  const signToken = ({ audience = AUD, email = 'admin@x.com' } = {}) =>
    new SignJWT({ email })
      .setProtectedHeader({ alg: 'RS256', kid: KID })
      .setIssuer(ISSUER)
      .setAudience(audience)
      .setSubject('sub')
      .setExpirationTime('1h')
      .sign(privateKey);

  const upstreamCalls = () =>
    fetchMock.mock.calls.filter(([calledUrl]) => calledUrl === UPSTREAM_URL);

  it('proxies through when the Access JWT is valid and forwards the JWT email upstream', async () => {
    const token = await signToken({ email: 'admin@x.com' });
    const request = new Request(UPSTREAM_URL, {
      method: 'GET',
      headers: { 'Cf-Access-Jwt-Assertion': token },
    });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: jwtEnv() });

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ users: [] });
    expect(upstreamCalls()).toHaveLength(1);
    const [, calledInit] = upstreamCalls()[0] as [string, RequestInit];
    // The audit email now comes from the signature-verified JWT, not an inbound header.
    expect((calledInit.headers as Headers).get('Cf-Access-Authenticated-User-Email')).toBe(
      'admin@x.com',
    );
  });

  it('returns 401 and never calls the upstream when the JWT is missing', async () => {
    const request = new Request(UPSTREAM_URL, { method: 'GET' });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: jwtEnv() });

    expect(response.status).toBe(401);
    expect(await response.json()).toEqual({ error: 'unauthorized' });
    expect(upstreamCalls()).toHaveLength(0);
  });

  it('returns 401 and never calls the upstream when the JWT is invalid (wrong audience)', async () => {
    const token = await signToken({ audience: 'wrong-aud' });
    const request = new Request(UPSTREAM_URL, {
      method: 'GET',
      headers: { 'Cf-Access-Jwt-Assertion': token },
    });

    const response = await proxyTo({ request, upstreamBase: UPSTREAM, env: jwtEnv() });

    expect(response.status).toBe(401);
    expect(await response.json()).toEqual({ error: 'unauthorized' });
    expect(upstreamCalls()).toHaveLength(0);
  });
});

// @vitest-environment node
//
// Runs in the node environment (not the project-default jsdom): jose's webapi
// build fails `instanceof Uint8Array` checks under jsdom's separate realm, and
// this Pages-Function code is pure Worker/Node code that needs no DOM.
import { beforeAll, describe, expect, it } from 'vitest';
import { SignJWT, createLocalJWKSet, exportJWK, generateKeyPair } from 'jose';
import type { JWK, JWTVerifyGetKey } from 'jose';

import { authenticateAdmin } from './_auth';
import { makeEnv } from './test-helpers';

const TEAM_DOMAIN = 'urls4irl.cloudflareaccess.com';
const ISSUER = `https://${TEAM_DOMAIN}`;
const AUD = 'test-aud-tag';
const KID = 'auth-test-key';

let privateKey: CryptoKey;
let attackerPrivateKey: CryptoKey;
let getKey: JWTVerifyGetKey;

/** Signs a test Access JWT, with per-claim overrides for the failure cases. */
async function signToken({
  issuer = ISSUER,
  audience = AUD,
  email = 'admin@example.com',
  expirationTime = '1h',
  key = privateKey,
}: {
  issuer?: string;
  audience?: string;
  email?: string;
  expirationTime?: string | number | Date;
  key?: CryptoKey;
} = {}): Promise<string> {
  return new SignJWT({ email })
    .setProtectedHeader({ alg: 'RS256', kid: KID })
    .setIssuer(issuer)
    .setAudience(audience)
    .setSubject('user-sub')
    .setExpirationTime(expirationTime)
    .sign(key);
}

/** Builds an inbound Request carrying the token in the given source. */
function requestWith({
  header,
  cookie,
}: {
  header?: string;
  cookie?: string;
} = {}): Request {
  const headers = new Headers();
  if (header !== undefined) {
    headers.set('Cf-Access-Jwt-Assertion', header);
  }
  if (cookie !== undefined) {
    headers.set('Cookie', cookie);
  }
  return new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET', headers });
}

function enabledEnv(overrides = {}) {
  // makeEnv defaults DISABLE_ACCESS_AUTH:'true'; clear it to exercise enforcement.
  return makeEnv({
    ACCESS_TEAM_DOMAIN: TEAM_DOMAIN,
    ACCESS_JWT_AUD: AUD,
    DISABLE_ACCESS_AUTH: '',
    ...overrides,
  });
}

beforeAll(async () => {
  const pair = await generateKeyPair('RS256', { extractable: true });
  privateKey = pair.privateKey;
  const publicJwk: JWK = { ...(await exportJWK(pair.publicKey)), kid: KID, alg: 'RS256' };
  getKey = createLocalJWKSet({ keys: [publicJwk] });

  // A second, unrelated keypair whose public key is deliberately NOT added to the
  // JWKS above — used to forge a well-formed token that must fail signature verification.
  const attackerPair = await generateKeyPair('RS256', { extractable: true });
  attackerPrivateKey = attackerPair.privateKey;
});

describe('authenticateAdmin', () => {
  it('accepts a valid token (correct iss + aud, not expired) and extracts the email claim', async () => {
    const token = await signToken({ email: 'alice@example.com' });
    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: enabledEnv(),
      getKey,
    });

    expect(result).toEqual({ ok: true, email: 'alice@example.com' });
  });

  it('returns null email when the verified token carries no email claim', async () => {
    // `email` intentionally omitted from the payload.
    const token = await new SignJWT({})
      .setProtectedHeader({ alg: 'RS256', kid: KID })
      .setIssuer(ISSUER)
      .setAudience(AUD)
      .setSubject('user-sub')
      .setExpirationTime('1h')
      .sign(privateKey);

    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: enabledEnv(),
      getKey,
    });

    expect(result).toEqual({ ok: true, email: null });
  });

  it('rejects a request with no token (no header, no cookie) as 401 unauthorized', async () => {
    const result = await authenticateAdmin({ request: requestWith(), env: enabledEnv(), getKey });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
    expect(await result.response.json()).toEqual({ error: 'unauthorized' });
  });

  it('rejects an empty CF_Authorization cookie value as 401 (treated as no token)', async () => {
    // `CF_Authorization=` with an empty value: tokenFromCookie coerces the empty
    // string to null (the `|| null` in _auth.ts), so the request hits the
    // no-token gate and is rejected with the standard 401 rather than the empty
    // string ever reaching jwtVerify. The assertion pins that 401 rejection.
    const result = await authenticateAdmin({
      request: requestWith({ cookie: 'CF_Authorization=; other=1' }),
      env: enabledEnv(),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
    expect(await result.response.json()).toEqual({ error: 'unauthorized' });
  });

  it('accepts a token supplied only in the CF_Authorization cookie', async () => {
    const token = await signToken();
    const result = await authenticateAdmin({
      request: requestWith({ cookie: `CF_Authorization=${token}; other=1` }),
      env: enabledEnv(),
      getKey,
    });

    expect(result).toEqual({ ok: true, email: 'admin@example.com' });
  });

  it('prefers the Cf-Access-Jwt-Assertion header over the cookie', async () => {
    const headerToken = await signToken({ email: 'from-header@example.com' });
    const cookieToken = await signToken({ email: 'from-cookie@example.com' });
    const result = await authenticateAdmin({
      request: requestWith({ header: headerToken, cookie: `CF_Authorization=${cookieToken}` }),
      env: enabledEnv(),
      getKey,
    });

    expect(result).toEqual({ ok: true, email: 'from-header@example.com' });
  });

  it('rejects an expired token as 401', async () => {
    const token = await signToken({ expirationTime: new Date(Date.now() - 3600_000) });
    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: enabledEnv(),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
    expect(await result.response.json()).toEqual({ error: 'unauthorized' });
  });

  it('rejects a token with the wrong audience as 401', async () => {
    const token = await signToken({ audience: 'some-other-aud' });
    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: enabledEnv(),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
  });

  it('rejects a token with the wrong issuer as 401', async () => {
    const token = await signToken({ issuer: 'https://evil.cloudflareaccess.com' });
    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: enabledEnv(),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
  });

  it('rejects a malformed/garbage token as 401', async () => {
    const result = await authenticateAdmin({
      request: requestWith({ header: 'not.a.real.jwt' }),
      env: enabledEnv(),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
  });

  it('rejects a token with correct claims but signed by an attacker key (not in the JWKS) as 401', async () => {
    // Correct issuer/audience/exp and the legitimate KID, but signed with a private
    // key whose public half is absent from the JWKS. Proves the signature itself is
    // cryptographically verified, not just the claims.
    const token = await signToken({ key: attackerPrivateKey });
    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: enabledEnv(),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
    expect(await result.response.json()).toEqual({ error: 'unauthorized' });
  });

  it('rejects an HS256 (symmetric) alg-confusion token as 401 — the RS256 allowlist blocks it', async () => {
    // Correct issuer/audience/exp and the legitimate KID, but signed with the
    // symmetric HS256 algorithm instead of RS256. A verifier without an algorithm
    // allowlist could be tricked into treating the RSA public key bytes as an HMAC
    // secret (the classic alg-confusion attack). The `algorithms: ['RS256']`
    // allowlist in _auth.ts rejects HS256 outright before any HMAC check, so this
    // forged token must fail with the standard 401.
    const secret = new TextEncoder().encode('rsa-public-key-material-as-hmac-secret');
    const token = await new SignJWT({ email: 'attacker@example.com' })
      .setProtectedHeader({ alg: 'HS256', kid: KID })
      .setIssuer(ISSUER)
      .setAudience(AUD)
      .setSubject('user-sub')
      .setExpirationTime('1h')
      .sign(secret);

    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: enabledEnv(),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(401);
    expect(await result.response.json()).toEqual({ error: 'unauthorized' });
  });

  it('is disabled when DISABLE_ACCESS_AUTH is "true" — returns ok:true, email:null with no verification', async () => {
    // A token that WOULD fail (garbage) proves no verification is attempted,
    // even with ACCESS_JWT_AUD + ACCESS_TEAM_DOMAIN fully configured.
    const result = await authenticateAdmin({
      request: requestWith({ header: 'garbage' }),
      env: makeEnv({
        ACCESS_TEAM_DOMAIN: TEAM_DOMAIN,
        ACCESS_JWT_AUD: AUD,
        DISABLE_ACCESS_AUTH: 'true',
      }),
      getKey,
    });

    expect(result).toEqual({ ok: true, email: null });
  });

  it('fails CLOSED (500) when ACCESS_JWT_AUD is empty and auth is NOT disabled', async () => {
    // A token that WOULD fail (garbage) proves it never reaches verification —
    // an empty AUD without the disable flag blocks the API rather than opening it.
    const result = await authenticateAdmin({
      request: requestWith({ header: 'garbage' }),
      env: makeEnv({
        ACCESS_TEAM_DOMAIN: TEAM_DOMAIN,
        ACCESS_JWT_AUD: '',
        DISABLE_ACCESS_AUTH: '',
      }),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(500);
    expect(await result.response.json()).toEqual({ error: 'proxy misconfigured' });
  });

  it('returns 500 proxy misconfigured when ACCESS_JWT_AUD is set but ACCESS_TEAM_DOMAIN is empty', async () => {
    const token = await signToken();
    const result = await authenticateAdmin({
      request: requestWith({ header: token }),
      env: makeEnv({ ACCESS_TEAM_DOMAIN: '', ACCESS_JWT_AUD: AUD, DISABLE_ACCESS_AUTH: '' }),
      getKey,
    });

    expect(result.ok).toBe(false);
    if (result.ok) throw new Error('expected failure');
    expect(result.response.status).toBe(500);
    expect(await result.response.json()).toEqual({ error: 'proxy misconfigured' });
  });
});

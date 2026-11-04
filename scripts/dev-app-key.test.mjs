import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, before, after, describe, it } from 'node:test';

import { buildJwks } from './dev-oidc-keys.mjs';
import { deliveryRequest, mintAppKey, signDevOidcToken, unbindApp } from './dev-app-key.mjs';

const DELIVERY_URL = 'http://delivery.test';
const NOW = 1_800_000_000;
const KEY = `dak_${'A'.repeat(43)}`;

function decode(part) {
  return JSON.parse(Buffer.from(part, 'base64url').toString('utf8'));
}

function jsonResponse(status, body) {
  return new Response(JSON.stringify(body), { status });
}

describe('dev-app-key', () => {
  const realFetch = globalThis.fetch;
  let keyDir;
  let publicKey;
  let kid;

  before(() => {
    keyDir = fs.mkdtempSync(path.join(os.tmpdir(), 'dev-app-key-test-'));
    const pair = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 });
    publicKey = pair.publicKey;
    const jwks = buildJwks(publicKey);
    kid = jwks.keys[0].kid;
    fs.writeFileSync(
      path.join(keyDir, 'private.pem'),
      pair.privateKey.export({ type: 'pkcs8', format: 'pem' }),
    );
    fs.writeFileSync(path.join(keyDir, 'jwks.json'), JSON.stringify(jwks));
  });

  after(() => {
    fs.rmSync(keyDir, { recursive: true, force: true });
  });

  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  describe('signDevOidcToken', () => {
    it('signs an RS256 token with the JWKS kid and the workflow claims', () => {
      const token = signDevOidcToken({ appId: 'app_one', now: NOW, keyDir });
      const [headerPart, claimsPart, signaturePart] = token.split('.');
      const header = decode(headerPart);
      const claims = decode(claimsPart);
      assert.equal(header.alg, 'RS256');
      assert.equal(header.kid, kid);
      assert.equal(claims.aud, 'notifs-delivery');
      assert.equal(claims.iat, NOW - 60);
      assert.equal(claims.nbf, NOW - 60);
      assert.equal(claims.exp, NOW + 600);
      for (const claim of [
        'iss',
        'repository',
        'repository_id',
        'repository_owner_id',
        'workflow_ref',
        'ref',
        'run_id',
      ]) {
        assert.ok(claims[claim], `missing claim ${claim}`);
      }
      const verified = crypto.verify(
        'RSA-SHA256',
        Buffer.from(`${headerPart}.${claimsPart}`),
        publicKey,
        Buffer.from(signaturePart, 'base64url'),
      );
      assert.equal(verified, true);
    });

    it('uses a unique jti per call', () => {
      const first = decode(signDevOidcToken({ appId: 'app_one', keyDir }).split('.')[1]);
      const second = decode(signDevOidcToken({ appId: 'app_one', keyDir }).split('.')[1]);
      assert.notEqual(first.jti, second.jti);
    });

    it('derives stable ids per app and distinct ids between apps', () => {
      const claimsOf = (appId) =>
        decode(signDevOidcToken({ appId, now: NOW, keyDir }).split('.')[1]);
      const one = claimsOf('app_one');
      const oneAgain = claimsOf('app_one');
      const two = claimsOf('app_two');
      assert.equal(one.repository_id, oneAgain.repository_id);
      assert.equal(one.repository_owner_id, oneAgain.repository_owner_id);
      assert.notEqual(one.repository_id, two.repository_id);
      assert.notEqual(one.repository_owner_id, two.repository_owner_id);
    });

    it('names the key dir when the key pair is unreadable', () => {
      assert.throws(
        () => signDevOidcToken({ appId: 'app_one', keyDir: path.join(keyDir, 'missing') }),
        /cannot read the dev OIDC key pair/,
      );
    });
  });

  describe('mintAppKey', () => {
    it('exchanges, approves and exchanges again', async () => {
      const calls = [];
      let approved = false;
      globalThis.fetch = async (url, init) => {
        const pathname = new URL(url).pathname;
        calls.push(`${init.method} ${pathname}`);
        if (pathname.endsWith('/approve')) {
          approved = true;
          return jsonResponse(201, {});
        }
        return approved
          ? jsonResponse(200, { key: KEY })
          : jsonResponse(202, { request_id: 'req1' });
      };
      const key = await mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir });
      assert.equal(key, KEY);
      assert.match(key, /^dak_[A-Za-z0-9_-]{43}$/);
      assert.deepEqual(calls, [
        'POST /v1/app-identity/exchange',
        'POST /v1/admin/app-identity/requests/req1/approve',
        'POST /v1/app-identity/exchange',
      ]);
    });

    it('returns an existing binding key with no approve call', async () => {
      const calls = [];
      globalThis.fetch = async (url) => {
        calls.push(new URL(url).pathname);
        return jsonResponse(200, { key: KEY });
      };
      const key = await mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir });
      assert.equal(key, KEY);
      assert.deepEqual(calls, ['/v1/app-identity/exchange']);
    });

    it('rejects a non-2xx exchange with the status in the message', async () => {
      globalThis.fetch = async () => jsonResponse(403, { error: 'nope' });
      await assert.rejects(
        mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir }),
        /403/,
      );
    });

    it('rejects when approve answers non-201', async () => {
      globalThis.fetch = async (url) =>
        new URL(url).pathname.endsWith('/approve')
          ? jsonResponse(409, { error: 'conflict' })
          : jsonResponse(202, { request_id: 'req1' });
      await assert.rejects(
        mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir }),
        /approve: 409 conflict/,
      );
    });

    it('rejects when the second exchange is still 202', async () => {
      globalThis.fetch = async (url) =>
        new URL(url).pathname.endsWith('/approve')
          ? jsonResponse(201, {})
          : jsonResponse(202, { request_id: 'req1' });
      await assert.rejects(
        mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir }),
        /exchange: 202/,
      );
    });

    it('rejects a 202 without a request_id and a 200 without a string key', async () => {
      globalThis.fetch = async () => new Response('not json', { status: 202 });
      await assert.rejects(
        mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir }),
        /missing request_id/,
      );
      globalThis.fetch = async () => jsonResponse(200, { key: 5 });
      await assert.rejects(
        mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir }),
        /no string key/,
      );
    });

    it('does not echo the response payload in an error', async () => {
      globalThis.fetch = async () => jsonResponse(403, { error: 'nope', key: KEY });
      await assert.rejects(
        mintAppKey({ deliveryUrl: DELIVERY_URL, appId: 'app_one', keyDir }),
        (error) => error.message.includes('nope') && !error.message.includes(KEY),
      );
    });
  });

  describe('deliveryRequest', () => {
    it('sends Content-Type and a JSON body only when given a body', async () => {
      const inits = [];
      globalThis.fetch = async (_url, init) => {
        inits.push(init);
        return jsonResponse(200, {});
      };
      await deliveryRequest({
        deliveryUrl: DELIVERY_URL,
        method: 'POST',
        pathname: '/x',
        body: { a: 1 },
      });
      await deliveryRequest({ deliveryUrl: DELIVERY_URL, method: 'POST', pathname: '/y' });
      assert.deepEqual(inits[0].headers, { 'Content-Type': 'application/json' });
      assert.equal(inits[0].body, '{"a":1}');
      assert.deepEqual(inits[1].headers, {});
      assert.equal(inits[1].body, undefined);
    });

    it('returns a null payload for a non-JSON response', async () => {
      globalThis.fetch = async () => new Response('plain', { status: 502 });
      const result = await deliveryRequest({
        deliveryUrl: DELIVERY_URL,
        method: 'GET',
        pathname: '/z',
      });
      assert.deepEqual(result, { status: 502, payload: null });
    });
  });

  describe('unbindApp', () => {
    it('resolves with the deleted flag on 200', async () => {
      globalThis.fetch = async () => jsonResponse(200, { deleted: true });
      assert.deepEqual(await unbindApp({ deliveryUrl: DELIVERY_URL, appId: 'app_one' }), {
        deleted: true,
      });
      globalThis.fetch = async () => jsonResponse(200, { deleted: false });
      assert.deepEqual(await unbindApp({ deliveryUrl: DELIVERY_URL, appId: 'app_one' }), {
        deleted: false,
      });
    });

    it('tolerates a 404', async () => {
      globalThis.fetch = async () => jsonResponse(404, { error: 'none' });
      assert.deepEqual(await unbindApp({ deliveryUrl: DELIVERY_URL, appId: 'app_one' }), {
        deleted: false,
      });
    });

    it('throws on a 500', async () => {
      globalThis.fetch = async () => jsonResponse(500, { error: 'boom' });
      await assert.rejects(unbindApp({ deliveryUrl: DELIVERY_URL, appId: 'app_one' }), /500/);
    });

    it('throws on a network error', async () => {
      globalThis.fetch = async () => {
        throw new Error('connection refused');
      };
      await assert.rejects(
        unbindApp({ deliveryUrl: DELIVERY_URL, appId: 'app_one' }),
        /connection refused/,
      );
    });
  });
});

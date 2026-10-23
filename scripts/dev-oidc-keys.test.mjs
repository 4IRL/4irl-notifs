import assert from 'node:assert/strict';
import { createPrivateKey, createPublicKey, createSign, createVerify } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, describe, it } from 'node:test';

import {
  DEFAULT_DIR,
  JWKS_FILE,
  PRIVATE_KEY_FILE,
  buildJwks,
  ensureDevOidcKeys,
  thumbprint,
} from './dev-oidc-keys.mjs';

const roots = [];

after(() => {
  for (const root of roots) fs.rmSync(root, { recursive: true, force: true });
});

function tmpDir() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'dev-oidc-test-'));
  roots.push(root);
  return path.join(root, 'dev-oidc');
}

function mode(file) {
  return fs.statSync(file).mode & 0o777;
}

describe('thumbprint', () => {
  it('matches the RFC 7638 section 3.1 example', () => {
    const n =
      '0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw';
    assert.equal(thumbprint({ e: 'AQAB', n }), 'NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs');
  });
});

describe('ensureDevOidcKeys', () => {
  it('defaults to the gitignored .local/dev-oidc under the repo root', () => {
    assert.ok(DEFAULT_DIR.endsWith(path.join('.local', 'dev-oidc')));
  });

  it('writes a private key (0600) and a JWKS (0644) whose key verifies what the private key signs', () => {
    const dir = tmpDir();
    const result = ensureDevOidcKeys({ dir });

    assert.equal(result.created, true);
    assert.equal(result.jwksWritten, true);
    assert.equal(mode(path.join(dir, PRIVATE_KEY_FILE)), 0o600);
    assert.equal(mode(path.join(dir, JWKS_FILE)), 0o644);

    const jwks = JSON.parse(fs.readFileSync(path.join(dir, JWKS_FILE), 'utf8'));
    assert.equal(jwks.keys.length, 1);
    const [jwk] = jwks.keys;
    assert.equal(jwk.kty, 'RSA');
    assert.equal(jwk.alg, 'RS256');
    assert.equal(jwk.use, 'sig');
    assert.equal(jwk.kid, thumbprint(jwk));
    assert.equal(jwk.kid, result.kid);
    assert.equal(jwk.e, 'AQAB');
    assert.ok(jwk.n.length >= 342, 'a 2048-bit modulus is 342 base64url characters');
    assert.equal(jwk.d, undefined, 'the JWKS must hold only the public key');

    const signature = createSign('RSA-SHA256')
      .update('payload')
      .sign(createPrivateKey(fs.readFileSync(path.join(dir, PRIVATE_KEY_FILE))));
    const verified = createVerify('RSA-SHA256')
      .update('payload')
      .verify(createPublicKey({ key: jwk, format: 'jwk' }), signature);
    assert.equal(verified, true);
  });

  it('is idempotent: a second run reuses the key and leaves both files untouched', () => {
    const dir = tmpDir();
    ensureDevOidcKeys({ dir });
    const keyBefore = fs.readFileSync(path.join(dir, PRIVATE_KEY_FILE), 'utf8');
    const jwksBefore = fs.readFileSync(path.join(dir, JWKS_FILE), 'utf8');
    const inodeBefore = fs.statSync(path.join(dir, JWKS_FILE)).ino;

    const second = ensureDevOidcKeys({ dir });

    assert.equal(second.created, false);
    assert.equal(second.jwksWritten, false);
    assert.equal(fs.readFileSync(path.join(dir, PRIVATE_KEY_FILE), 'utf8'), keyBefore);
    assert.equal(fs.readFileSync(path.join(dir, JWKS_FILE), 'utf8'), jwksBefore);
    assert.equal(fs.statSync(path.join(dir, JWKS_FILE)).ino, inodeBefore);
  });

  it('rewrites a drifted or missing JWKS in place from the existing private key', () => {
    const dir = tmpDir();
    const first = ensureDevOidcKeys({ dir });
    const jwksPath = path.join(dir, JWKS_FILE);
    const keyBefore = fs.readFileSync(path.join(dir, PRIVATE_KEY_FILE), 'utf8');
    const inodeBefore = fs.statSync(jwksPath).ino;

    fs.writeFileSync(jwksPath, '{"keys":[]}');
    const drifted = ensureDevOidcKeys({ dir });
    assert.equal(drifted.created, false);
    assert.equal(drifted.jwksWritten, true);
    assert.equal(drifted.kid, first.kid);
    assert.equal(
      fs.statSync(jwksPath).ino,
      inodeBefore,
      'rewritten in place so a bind mount stays valid',
    );
    assert.equal(JSON.parse(fs.readFileSync(jwksPath, 'utf8')).keys[0].kid, first.kid);

    fs.rmSync(jwksPath);
    const missing = ensureDevOidcKeys({ dir });
    assert.equal(missing.jwksWritten, true);
    assert.equal(mode(jwksPath), 0o644);
    assert.equal(fs.readFileSync(path.join(dir, PRIVATE_KEY_FILE), 'utf8'), keyBefore);
  });

  it('restores permissions that drifted', () => {
    const dir = tmpDir();
    ensureDevOidcKeys({ dir });
    fs.chmodSync(path.join(dir, PRIVATE_KEY_FILE), 0o644);
    fs.chmodSync(path.join(dir, JWKS_FILE), 0o600);
    ensureDevOidcKeys({ dir });
    assert.equal(mode(path.join(dir, PRIVATE_KEY_FILE)), 0o600);
    assert.equal(mode(path.join(dir, JWKS_FILE)), 0o644);
  });

  it('refuses to replace an unreadable private key', () => {
    const dir = tmpDir();
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, PRIVATE_KEY_FILE), 'not a pem');

    assert.throws(() => ensureDevOidcKeys({ dir }), /not a readable private key/);
    assert.equal(fs.readFileSync(path.join(dir, PRIVATE_KEY_FILE), 'utf8'), 'not a pem');
    assert.equal(fs.existsSync(path.join(dir, JWKS_FILE)), false);
  });
});

describe('buildJwks', () => {
  it('derives the kid from the public key, so the same key always has the same kid', () => {
    const dir = tmpDir();
    ensureDevOidcKeys({ dir });
    const privateKey = createPrivateKey(fs.readFileSync(path.join(dir, PRIVATE_KEY_FILE)));
    const once = buildJwks(createPublicKey(privateKey));
    const twice = buildJwks(createPublicKey(privateKey));
    assert.deepEqual(once, twice);
  });
});

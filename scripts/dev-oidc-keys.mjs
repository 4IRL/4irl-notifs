#!/usr/bin/env node
/**
 * Writes a throwaway RSA key pair for the local and CI delivery stacks, which cannot get real
 * GitHub Actions OIDC tokens:
 *
 *   .local/dev-oidc/private.pem  (0600)  signs test tokens; read only by the integration harness
 *   .local/dev-oidc/jwks.json    (0644)  the matching public key set, mounted into delivery-api,
 *                                        which trusts it only when DELIVERY_OIDC_DEV_MODE=1
 *
 * The directory is gitignored and dockerignored: never commit or bake in a key. Re-running is
 * safe and idempotent. An existing private key is reused (so a running stack's mounted JWKS keeps
 * matching what the harness signs with) and jwks.json is rewritten in place only if it drifted,
 * keeping its inode so a bind mount does not go stale. A private key that cannot be read is an
 * error, never silently replaced.
 *
 * Usage: node scripts/dev-oidc-keys.mjs [--dir <path>]
 */
import { createHash, createPrivateKey, createPublicKey, generateKeyPairSync } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { REPO_ROOT } from './ports.mjs';

export const DEFAULT_DIR = path.join(REPO_ROOT, '.local', 'dev-oidc');
export const PRIVATE_KEY_FILE = 'private.pem';
export const JWKS_FILE = 'jwks.json';
const MODULUS_BITS = 2048;

/** RFC 7638 JWK thumbprint (SHA-256, base64url) of an RSA public JWK: a stable key id. */
export function thumbprint({ e, n }) {
  const canonical = JSON.stringify({ e, kty: 'RSA', n });
  return createHash('sha256').update(canonical).digest('base64url');
}

/** The JWKS document (one RS256 signature key, kid = thumbprint) for a Node public KeyObject. */
export function buildJwks(publicKey) {
  const { n, e } = publicKey.export({ format: 'jwk' });
  return { keys: [{ kty: 'RSA', kid: thumbprint({ e, n }), use: 'sig', alg: 'RS256', n, e }] };
}

function readJson(file) {
  try {
    return JSON.parse(fs.readFileSync(file, 'utf8'));
  } catch {
    return null;
  }
}

/**
 * Ensures dir holds a private key and its JWKS. Returns { dir, keyPath, jwksPath, kid, created,
 * jwksWritten }: created is true when a new key pair was generated.
 */
export function ensureDevOidcKeys({ dir = DEFAULT_DIR } = {}) {
  const keyPath = path.join(dir, PRIVATE_KEY_FILE);
  const jwksPath = path.join(dir, JWKS_FILE);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });

  let privateKey;
  let created = false;
  if (fs.existsSync(keyPath)) {
    try {
      privateKey = createPrivateKey(fs.readFileSync(keyPath));
    } catch (error) {
      throw new Error(
        `${keyPath} exists but is not a readable private key (${error.message}); delete ${dir} and rerun`,
      );
    }
  } else {
    ({ privateKey } = generateKeyPairSync('rsa', { modulusLength: MODULUS_BITS }));
    fs.writeFileSync(keyPath, privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
    created = true;
  }
  // The mode option applies only when a file is created; make the permissions explicit.
  fs.chmodSync(keyPath, 0o600);

  const jwks = buildJwks(createPublicKey(privateKey));
  const current = readJson(jwksPath);
  const jwksWritten = JSON.stringify(current) !== JSON.stringify(jwks);
  if (jwksWritten) {
    fs.writeFileSync(jwksPath, `${JSON.stringify(jwks, null, 2)}\n`, { mode: 0o644 });
  }
  // Readable by the unprivileged user inside the delivery-api container.
  fs.chmodSync(jwksPath, 0o644);

  return { dir, keyPath, jwksPath, kid: jwks.keys[0].kid, created, jwksWritten };
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === '--dir' && argv[index + 1]) {
      options.dir = path.resolve(argv[index + 1]);
      index += 1;
    } else {
      throw new Error(`unknown argument ${argv[index]}; usage: dev-oidc-keys.mjs [--dir <path>]`);
    }
  }
  return options;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const result = ensureDevOidcKeys(parseArgs(process.argv.slice(2)));
    const state = result.created ? 'created' : 'reused';
    console.log(`dev OIDC keys ${state} in ${result.dir} (kid ${result.kid})`);
  } catch (error) {
    console.error(`dev-oidc-keys: ${error.message}`);
    process.exit(1);
  }
}

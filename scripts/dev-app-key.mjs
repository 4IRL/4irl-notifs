// Shared dev helper: mints a delivery app key (`dak_...`) for an app against a local or CI
// delivery-api stack that runs OIDC dev mode, the way delivery-api/integration's
// `bindAndIssueKey` does in Go. It signs a GitHub-shaped OIDC token with the throwaway key
// `make local-up` generated in .local/dev-oidc/, exchanges it (the first exchange files a pending
// request), approves the request through the operator API, and exchanges again for the key.
// Production never does this; the operator API is open only on a loopback stack.
//
// No CLI entry point: callers (the onboard integration test, the smoke test) import it.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

import { DEFAULT_DIR, JWKS_FILE, PRIVATE_KEY_FILE } from './dev-oidc-keys.mjs';

const REQUEST_TIMEOUT_MS = 15000;
const GITHUB_ISSUER = 'https://token.actions.githubusercontent.com';
const OIDC_AUDIENCE = 'notifs-delivery';
const OIDC_WORKFLOW = '.github/workflows/prod-deploy.yml';
const OIDC_REF = 'refs/heads/main';
const REPOSITORY_ID_SPACE = 1_000_000_000_000n;
const OWNER_ID_SPACE = 1_000_000_000_000n;
const FNV_OFFSET_BASIS = 0xcbf29ce484222325n;
const FNV_PRIME = 0x100000001b3n;
const UINT64_MASK = 0xffffffffffffffffn;

function fnv1a64(text) {
  let hash = FNV_OFFSET_BASIS;
  for (const byte of Buffer.from(text)) {
    hash ^= BigInt(byte);
    hash = (hash * FNV_PRIME) & UINT64_MASK;
  }
  return hash;
}

/** Go's scheme: FNV-1a 64 of the app id, modulo 1e12, plus 1. */
function repositoryIdFor(appId) {
  return String((fnv1a64(appId) % REPOSITORY_ID_SPACE) + 1n);
}

/** A stable per-app owner id from a separate hash domain, so two apps never share one. */
function ownerIdFor(appId) {
  return String((fnv1a64(`owner:${appId}`) % OWNER_ID_SPACE) + 1n);
}

function base64url(value) {
  return Buffer.from(typeof value === 'string' ? value : JSON.stringify(value)).toString(
    'base64url',
  );
}

/** Signs a GitHub-shaped OIDC token for appId's throwaway workflow identity with the dev key. */
export function signDevOidcToken({ appId, now, keyDir = DEFAULT_DIR }) {
  let privateKey;
  let jwks;
  try {
    privateKey = crypto.createPrivateKey(fs.readFileSync(path.join(keyDir, PRIVATE_KEY_FILE)));
    jwks = JSON.parse(fs.readFileSync(path.join(keyDir, JWKS_FILE), 'utf8'));
  } catch (error) {
    throw new Error(
      `cannot read the dev OIDC key pair in ${keyDir} (${error.message}): ` +
        'run make dev-oidc-keys and make local-up',
    );
  }
  const repositoryName = `4IRL/${appId.replaceAll('_', '-')}`;
  const issuedAt = now ?? Math.floor(Date.now() / 1000);
  const header = { alg: 'RS256', typ: 'JWT', kid: jwks.keys[0].kid };
  const claims = {
    iss: GITHUB_ISSUER,
    aud: OIDC_AUDIENCE,
    iat: issuedAt - 60,
    nbf: issuedAt - 60,
    exp: issuedAt + 600,
    jti: `dev-app-key-${crypto.randomUUID()}`,
    repository: repositoryName,
    repository_id: repositoryIdFor(appId),
    repository_owner_id: ownerIdFor(appId),
    workflow_ref: `${repositoryName}/${OIDC_WORKFLOW}@${OIDC_REF}`,
    ref: OIDC_REF,
    run_id: '1',
  };
  const signingInput = `${base64url(header)}.${base64url(claims)}`;
  const signature = crypto.sign('RSA-SHA256', Buffer.from(signingInput), privateKey);
  return `${signingInput}.${signature.toString('base64url')}`;
}

/** One JSON request to delivery-api. `payload` is null for a non-JSON response. */
export async function deliveryRequest({ deliveryUrl, method, pathname, body }) {
  const response = await fetch(`${deliveryUrl}${pathname}`, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  });
  return { status: response.status, payload: await response.json().catch(() => null) };
}

/** Status plus only the response's `error` string, never the whole payload (it could hold a key). */
export function errorSummary({ status, payload }) {
  return typeof payload?.error === 'string' ? `${status} ${payload.error}` : `${status}`;
}

/** Binds appId to its throwaway workflow identity (exchange -> approve) and returns its delivery
 * key from a second exchange. Reuses an existing binding of the same identity (an interrupted
 * earlier run), which answers the first exchange with a key at once. */
export async function mintAppKey({ deliveryUrl, appId, keyDir }) {
  const exchange = () =>
    deliveryRequest({
      deliveryUrl,
      method: 'POST',
      pathname: '/v1/app-identity/exchange',
      body: { app_id: appId, token: signDevOidcToken({ appId, keyDir }) },
    });
  let result = await exchange();
  if (result.status === 202) {
    const requestId = result.payload?.request_id;
    if (!requestId) {
      throw new Error('exchange: 202 missing request_id');
    }
    const approved = await deliveryRequest({
      deliveryUrl,
      method: 'POST',
      pathname: `/v1/admin/app-identity/requests/${requestId}/approve`,
    });
    if (approved.status !== 201) {
      throw new Error(`approve: ${errorSummary(approved)}`);
    }
    result = await exchange();
  }
  if (result.status !== 200) {
    throw new Error(`exchange: ${errorSummary(result)}`);
  }
  if (typeof result.payload?.key !== 'string') {
    throw new Error('exchange: 200 response has no string key');
  }
  return result.payload.key;
}

/** Unbinds appId (revoking its keys). 200 (either `deleted` value) and 404 are success; any other
 * status, a network error or a timeout throws: cleanup is not best-effort. */
export async function unbindApp({ deliveryUrl, appId }) {
  const result = await deliveryRequest({
    deliveryUrl,
    method: 'DELETE',
    pathname: `/v1/admin/app-identity/bindings/${appId}`,
  });
  if (result.status === 404) {
    return { deleted: false };
  }
  if (result.status !== 200) {
    throw new Error(`unbind ${appId}: ${errorSummary(result)}`);
  }
  return { deleted: result.payload?.deleted === true };
}

// Live integration test for scripts/onboard.mjs against a real local delivery-api stack
// (`make local-up`). Skipped unless ONBOARD_INTEGRATION_URL is set, so `node --test
// "scripts/*.test.mjs"` (`make scripts-test`) stays green with no stack running;
// `make onboard-integration-test` sets ONBOARD_INTEGRATION_URL (+ ONBOARD_INTEGRATION_PROVISIONING_URL)
// and runs this file alone.
//
// Drives `main()` exactly like the CLI does: real fetch (no faked deps), a real manifest file on
// disk. ONBOARD_INTEGRATION_URL is expected to be a loopback origin (e.g. http://127.0.0.1:8300),
// so no ONBOARD_ACCESS_ID/SECRET or ONBOARD_APP_ACCESS_ID/SECRET is needed — see
// onboard-client.mjs's resolveTarget fail-closed rules.
//
// ntfy/server.yml runs `auth-default-access: deny-all`, so a real delivery to the owned ntfy
// destination needs a real write grant on its topic, not just a reachable server. This test mints
// one the same way `make notif-smoke-test` does: POST /v1/provision-app on provisioning-api grants
// the app's publisher identity write-only access to "<app_id>-*" (provisioning-api/internal/ntfycli
// `PublisherTopicPattern`), so the manifest's owned destination topic is "<app_id>-push" and its
// `secrets.access_token` resolves to that minted token. /v1/deprovision-app removes the publisher
// identity again during cleanup.
//
// `verify` sends and pings through the app-facing endpoints, which require the app's delivery key
// (ONBOARD_APP_KEY). This test gets one the way the stack's dev mode allows and production never
// does for an operator-run verify: it signs a GitHub-shaped OIDC token with the throwaway key
// `make local-up` generated in .local/dev-oidc/, exchanges it (the first exchange files a pending
// request), approves the request through the operator API, and exchanges again for the key.
//
// Two flows are covered: the operator mode (`main(['plan', <manifest>])`, `/v1/admin`) and the app
// repo mode (`--manifest <path> --target app`, `/v1/apps/{app_id}` with that key), plus the
// negative case that one app's key cannot apply another app's manifest.
import crypto from 'node:crypto';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, afterEach, before, describe, it } from 'node:test';

import { DEFAULT_DIR, JWKS_FILE, PRIVATE_KEY_FILE } from './dev-oidc-keys.mjs';
import { main } from './onboard.mjs';

const INTEGRATION_URL = process.env.ONBOARD_INTEGRATION_URL;
const PROVISIONING_URL = process.env.ONBOARD_INTEGRATION_PROVISIONING_URL;
const PROVISIONING_TIMEOUT_MS = 15000;
const IDENTITY_TIMEOUT_MS = 15000;

const GITHUB_ISSUER = 'https://token.actions.githubusercontent.com';
const OIDC_AUDIENCE = 'notifs-delivery';
const OIDC_WORKFLOW = '.github/workflows/prod-deploy.yml';
const OIDC_REF = 'refs/heads/main';
// Made-up GitHub ids for the throwaway workflow identity this test binds; not real accounts.
const OIDC_REPOSITORY_ID = '8800000001';
const OIDC_OWNER_ID = '8800000002';

const APP_ID = 'onboard_it';
// A second app id, deliberately not prefixed by APP_ID, whose manifest the first app's key must not
// be able to apply.
const OTHER_APP_ID = 'onboardx_it';
// An app repo keeps its manifest anywhere under any name; the --target app test uses this one.
const APP_REPO_MANIFEST_FILE = 'notifs.json';
const DESTINATION_NAME = `${APP_ID}_push`;
const DESTINATION_TOPIC = `${APP_ID}-push`; // must start with "<app_id>-" to match PublisherTopicPattern
const NTFY_TOKEN_ENV_VAR = 'ONBOARD_IT_NTFY_TOKEN';
const CHANNEL = 'it';
const HEARTBEAT_NAME = 'nightly_backup';

function manifestPathFor(dir, fileName = `${APP_ID}.json`) {
  return path.join(dir, fileName);
}

function writeFullManifest(dir, fileName) {
  const manifest = {
    app_id: APP_ID,
    destinations: [
      {
        name: DESTINATION_NAME,
        type: 'ntfy',
        config: { topic: DESTINATION_TOPIC },
        secrets: { access_token: { env: NTFY_TOKEN_ENV_VAR } },
      },
    ],
    routes: [{ channel: CHANNEL, destinations: [DESTINATION_NAME] }],
    heartbeats: [
      { name: HEARTBEAT_NAME, period: '1h', grace: '1h', destinations: [DESTINATION_NAME] },
    ],
  };
  fs.writeFileSync(manifestPathFor(dir, fileName), JSON.stringify(manifest));
}

function writeEmptyManifest(dir, fileName) {
  fs.writeFileSync(manifestPathFor(dir, fileName), JSON.stringify({ app_id: APP_ID }));
}

function envFor(ntfyToken) {
  return { ONBOARD_URL: INTEGRATION_URL, [NTFY_TOKEN_ENV_VAR]: ntfyToken };
}

/** Mints (or reuses) the app's ntfy publisher identity via provisioning-api, returning the
 * write-only token the manifest's destination resolves as its `access_token` secret. */
async function provisionPublisherToken() {
  const response = await fetch(`${PROVISIONING_URL}/v1/provision-app`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ app_id: APP_ID }),
    signal: AbortSignal.timeout(PROVISIONING_TIMEOUT_MS),
  });
  if (!response.ok) {
    throw new Error(`provision-app failed: ${response.status} ${await response.text()}`);
  }
  const body = await response.json();
  return body.token;
}

/** Removes the app's publisher identity (and its registry row) via provisioning-api. Best-effort:
 * swallows a failure so the caller's own cleanup still runs when the server is unreachable. */
async function deprovisionPublisher() {
  try {
    await fetch(`${PROVISIONING_URL}/v1/deprovision-app`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ app_id: APP_ID }),
      signal: AbortSignal.timeout(PROVISIONING_TIMEOUT_MS),
    });
  } catch {
    // Best-effort cleanup; the test itself already reported the real failure.
  }
}

function base64url(value) {
  return Buffer.from(typeof value === 'string' ? value : JSON.stringify(value)).toString(
    'base64url',
  );
}

/** Signs a GitHub-shaped OIDC token for APP_ID's throwaway workflow identity with the dev key. */
function signDevOidcToken() {
  let privateKey;
  let jwks;
  try {
    privateKey = crypto.createPrivateKey(fs.readFileSync(path.join(DEFAULT_DIR, PRIVATE_KEY_FILE)));
    jwks = JSON.parse(fs.readFileSync(path.join(DEFAULT_DIR, JWKS_FILE), 'utf8'));
  } catch (error) {
    throw new Error(
      `cannot read the dev OIDC key pair in ${DEFAULT_DIR} (${error.message}): ` +
        'run make dev-oidc-keys and make local-up',
    );
  }
  const repository = `4IRL/${APP_ID.replaceAll('_', '-')}`;
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: 'RS256', typ: 'JWT', kid: jwks.keys[0].kid };
  const claims = {
    iss: GITHUB_ISSUER,
    aud: OIDC_AUDIENCE,
    iat: now - 60,
    nbf: now - 60,
    exp: now + 600,
    jti: `onboard-it-${crypto.randomUUID()}`,
    repository,
    repository_id: OIDC_REPOSITORY_ID,
    repository_owner_id: OIDC_OWNER_ID,
    workflow_ref: `${repository}/${OIDC_WORKFLOW}@${OIDC_REF}`,
    ref: OIDC_REF,
    run_id: '1',
  };
  const signingInput = `${base64url(header)}.${base64url(claims)}`;
  const signature = crypto.sign('RSA-SHA256', Buffer.from(signingInput), privateKey);
  return `${signingInput}.${signature.toString('base64url')}`;
}

async function identityRequest({ method, pathname, body }) {
  const response = await fetch(`${INTEGRATION_URL}${pathname}`, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(IDENTITY_TIMEOUT_MS),
  });
  return { status: response.status, payload: await response.json().catch(() => null) };
}

/** Binds APP_ID to the throwaway workflow identity (exchange -> approve) and returns its delivery
 * key from a second exchange. Reuses an existing binding of the same identity (an interrupted
 * earlier run), which answers the first exchange with a key at once. */
async function mintAppKey() {
  const exchange = () =>
    identityRequest({
      method: 'POST',
      pathname: '/v1/app-identity/exchange',
      body: { app_id: APP_ID, token: signDevOidcToken() },
    });
  let result = await exchange();
  if (result.status === 202) {
    const approved = await identityRequest({
      method: 'POST',
      pathname: `/v1/admin/app-identity/requests/${result.payload.request_id}/approve`,
    });
    assert.equal(approved.status, 201, `approve: ${JSON.stringify(approved.payload)}`);
    result = await exchange();
  }
  assert.equal(result.status, 200, `exchange: ${JSON.stringify(result.payload)}`);
  return result.payload.key;
}

/** The operator's view of one registry resource (`destinations`, `routes`, `heartbeats`), with the
 * server-side `owner_kind`/`owner_app_id` that only the server can set. */
async function adminList(resource) {
  const result = await identityRequest({ method: 'GET', pathname: `/v1/admin/${resource}` });
  assert.equal(result.status, 200, `list ${resource}: ${JSON.stringify(result.payload)}`);
  return result.payload[resource === 'heartbeats' ? 'checks' : resource];
}

/** Unbinds APP_ID (revoking its keys). Best-effort, like the other cleanup. */
async function unbindApp() {
  try {
    await identityRequest({
      method: 'DELETE',
      pathname: `/v1/admin/app-identity/bindings/${APP_ID}`,
    });
  } catch {
    // Best-effort cleanup; the test itself already reported the real failure.
  }
}

/** Captures console.log/console.error output around `fn`, restoring both afterward even if `fn`
 * throws, same pattern as onboard.test.mjs's captureOutput. */
async function captureOutput(fn) {
  const stdout = [];
  const stderr = [];
  const originalLog = console.log;
  const originalError = console.error;
  console.log = (...args) => stdout.push(args.join(' '));
  console.error = (...args) => stderr.push(args.join(' '));
  try {
    await fn();
  } finally {
    console.log = originalLog;
    console.error = originalError;
  }
  return { stdout: stdout.join('\n'), stderr: stderr.join('\n') };
}

/** Runs `apply --prune` against an empty onboard_it manifest in `dir`, leaving no onboard_it*
 * destination/route/check on the server. Safe to call repeatedly: an already-clean server reports
 * `No changes.`. Swallows a thrown error so the `after` hook below still runs its own cleanup
 * even if the server is unreachable. */
async function pruneToEmpty(dir, ntfyToken) {
  writeEmptyManifest(dir);
  process.exitCode = undefined;
  try {
    await captureOutput(() =>
      main(['apply', manifestPathFor(dir), '--prune'], { env: envFor(ntfyToken) }),
    );
  } catch {
    // Best-effort cleanup; the test itself already reported the real failure.
  }
  process.exitCode = undefined;
}

describe(
  'onboard.mjs live integration (local delivery-api stack)',
  { skip: INTEGRATION_URL ? false : 'ONBOARD_INTEGRATION_URL is not set' },
  () => {
    let dir;
    let appDir;
    let ntfyToken;
    let appKey;

    before(async () => {
      dir = fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-onboard-it-'));
      // The --target app tests keep their manifests apart from `dir`: the operator cleanup in
      // `after` runs `apply --prune` there, whose directory lint refuses any stray sibling file.
      appDir = fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-onboard-it-app-'));
      ntfyToken = await provisionPublisherToken();
      appKey = await mintAppKey();
      // A previous failed or killed run may have left onboard_it* objects behind (the cleanup in
      // `after` never ran); start from a clean server so they cannot turn a create into a 409.
      await pruneToEmpty(dir, ntfyToken);
    });

    after(async () => {
      // Cleans up after itself so repeated runs start clean, even when the test below failed
      // partway through, before reaching its own apply --prune step.
      await pruneToEmpty(dir, ntfyToken);
      await deprovisionPublisher();
      await unbindApp();
      fs.rmSync(dir, { recursive: true, force: true });
      fs.rmSync(appDir, { recursive: true, force: true });
    });

    afterEach(() => {
      process.exitCode = undefined;
    });

    it('plans, applies, re-applies with no changes, verifies and prunes', async () => {
      writeFullManifest(dir);
      const manifestPath = manifestPathFor(dir);
      const deps = { env: envFor(ntfyToken) };

      const planResult = await captureOutput(() => main(['plan', manifestPath], deps));
      assert.equal(process.exitCode, undefined, planResult.stderr);
      assert.match(planResult.stdout, /create/);

      const applyResult = await captureOutput(() => main(['apply', manifestPath], deps));
      assert.equal(process.exitCode, undefined, applyResult.stderr);

      process.exitCode = undefined;
      const reapplyResult = await captureOutput(() => main(['apply', manifestPath], deps));
      assert.equal(process.exitCode, undefined, reapplyResult.stderr);
      assert.match(reapplyResult.stdout, /No changes\./);

      process.exitCode = undefined;
      // Only verify calls the app-facing endpoints, so only it needs the app's delivery key.
      const verifyDeps = { env: { ...deps.env, ONBOARD_APP_KEY: appKey } };
      const verifyResult = await captureOutput(() => main(['verify', manifestPath], verifyDeps));
      assert.equal(process.exitCode, undefined, `${verifyResult.stdout}\n${verifyResult.stderr}`);
      assert.match(verifyResult.stdout, new RegExp(`^${CHANNEL}: pass$`, 'm'));
      assert.match(verifyResult.stdout, new RegExp(`^${HEARTBEAT_NAME}: pass$`, 'm'));

      process.exitCode = undefined;
      writeEmptyManifest(dir);
      const pruneResult = await captureOutput(() => main(['apply', manifestPath, '--prune'], deps));
      assert.equal(process.exitCode, undefined, pruneResult.stderr);

      // Confirm the server really holds no onboard_it* objects now: a plan against the same
      // empty manifest with --prune reports nothing left to delete or unmanaged.
      process.exitCode = undefined;
      const confirmResult = await captureOutput(() =>
        main(['plan', manifestPath, '--prune'], deps),
      );
      assert.equal(process.exitCode, undefined, confirmResult.stderr);
      assert.match(confirmResult.stdout, /No changes\./);
    });

    it('runs from an app repo with --target app: plan, apply, no-op re-apply, verify and prune against /v1/apps only', async () => {
      // Start from a clean slate: a leftover operator-owned onboard_it_push from an earlier failed
      // or killed run would turn the app-owned create into a spurious 409.
      await pruneToEmpty(dir, ntfyToken);
      writeFullManifest(appDir, APP_REPO_MANIFEST_FILE);
      const manifestPath = manifestPathFor(appDir, APP_REPO_MANIFEST_FILE);
      const appArgs = (command, ...extra) => [
        command,
        '--manifest',
        manifestPath,
        '--target',
        'app',
        ...extra,
      ];
      const deps = { env: { ...envFor(ntfyToken), ONBOARD_APP_KEY: appKey } };

      const lintResult = await captureOutput(() =>
        main(['lint', '--manifest', manifestPath, '--target', 'app'], deps),
      );
      assert.equal(process.exitCode, undefined, lintResult.stdout);

      const planResult = await captureOutput(() => main(appArgs('plan'), deps));
      assert.equal(process.exitCode, undefined, planResult.stderr);
      assert.match(planResult.stdout, /create/);

      const applyResult = await captureOutput(() => main(appArgs('apply'), deps));
      assert.equal(process.exitCode, undefined, applyResult.stderr);

      // Everything the app's key created is app-owned, by the key's identity, not by the manifest.
      const destinations = await adminList('destinations');
      const created = destinations.find((entry) => entry.name === DESTINATION_NAME);
      assert.equal(created?.owner_kind, 'app');
      assert.equal(created?.owner_app_id, APP_ID);
      const createdRoute = (await adminList('routes')).find(
        (entry) => entry.channel_pattern === CHANNEL && entry.app_pattern === APP_ID,
      );
      assert.equal(createdRoute?.owner_kind, 'app');
      assert.equal(createdRoute?.owner_app_id, APP_ID);
      const createdCheck = (await adminList('heartbeats')).find(
        (entry) => entry.name === HEARTBEAT_NAME && entry.app_id === APP_ID,
      );
      assert.equal(createdCheck?.owner_kind, 'app');
      assert.equal(createdCheck?.owner_app_id, APP_ID);

      process.exitCode = undefined;
      const reapplyResult = await captureOutput(() => main(appArgs('apply'), deps));
      assert.equal(process.exitCode, undefined, reapplyResult.stderr);
      assert.match(reapplyResult.stdout, /No changes\./);

      process.exitCode = undefined;
      const verifyResult = await captureOutput(() => main(appArgs('verify'), deps));
      assert.equal(process.exitCode, undefined, `${verifyResult.stdout}\n${verifyResult.stderr}`);
      assert.match(verifyResult.stdout, new RegExp(`^${CHANNEL}: pass$`, 'm'));
      assert.match(verifyResult.stdout, new RegExp(`^${HEARTBEAT_NAME}: pass$`, 'm'));

      process.exitCode = undefined;
      writeEmptyManifest(appDir, APP_REPO_MANIFEST_FILE);
      const pruneResult = await captureOutput(() => main(appArgs('apply', '--prune'), deps));
      assert.equal(process.exitCode, undefined, pruneResult.stderr);

      process.exitCode = undefined;
      const confirmResult = await captureOutput(() => main(appArgs('plan', '--prune'), deps));
      assert.equal(process.exitCode, undefined, confirmResult.stderr);
      assert.match(confirmResult.stdout, /No changes\./);
      const remaining = await adminList('destinations');
      assert.equal(
        remaining.some((entry) => entry.name === DESTINATION_NAME),
        false,
      );
    });

    it("refuses another app's manifest with the app key's 403 and creates nothing", async () => {
      const otherManifestPath = manifestPathFor(appDir, `${OTHER_APP_ID}.json`);
      fs.writeFileSync(
        otherManifestPath,
        JSON.stringify({
          app_id: OTHER_APP_ID,
          destinations: [
            {
              name: `${OTHER_APP_ID}_push`,
              type: 'ntfy',
              config: { topic: `${OTHER_APP_ID}-push` },
              secrets: { access_token: { env: NTFY_TOKEN_ENV_VAR } },
            },
          ],
          routes: [{ channel: CHANNEL, destinations: [`${OTHER_APP_ID}_push`] }],
        }),
      );
      const deps = { env: { ...envFor(ntfyToken), ONBOARD_APP_KEY: appKey } };

      await assert.rejects(
        captureOutput(() =>
          main(['apply', '--manifest', otherManifestPath, '--target', 'app'], deps),
        ),
        /403 .*app_id does not match app key/,
      );

      const destinations = await adminList('destinations');
      assert.equal(
        destinations.some((entry) => entry.name.startsWith(`${OTHER_APP_ID}_`)),
        false,
      );
    });
  },
);

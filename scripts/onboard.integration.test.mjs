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
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, afterEach, before, describe, it } from 'node:test';

import { main } from './onboard.mjs';

const INTEGRATION_URL = process.env.ONBOARD_INTEGRATION_URL;
const PROVISIONING_URL = process.env.ONBOARD_INTEGRATION_PROVISIONING_URL;
const PROVISIONING_TIMEOUT_MS = 15000;

const APP_ID = 'onboard_it';
const DESTINATION_NAME = `${APP_ID}_push`;
const DESTINATION_TOPIC = `${APP_ID}-push`; // must start with "<app_id>-" to match PublisherTopicPattern
const NTFY_TOKEN_ENV_VAR = 'ONBOARD_IT_NTFY_TOKEN';
const CHANNEL = 'it';
const HEARTBEAT_NAME = 'nightly_backup';

function manifestPathFor(dir) {
  return path.join(dir, `${APP_ID}.json`);
}

function writeFullManifest(dir) {
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
  fs.writeFileSync(manifestPathFor(dir), JSON.stringify(manifest));
}

function writeEmptyManifest(dir) {
  fs.writeFileSync(manifestPathFor(dir), JSON.stringify({ app_id: APP_ID }));
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
    let ntfyToken;

    before(async () => {
      dir = fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-onboard-it-'));
      ntfyToken = await provisionPublisherToken();
    });

    after(async () => {
      // Cleans up after itself so repeated runs start clean, even when the test below failed
      // partway through, before reaching its own apply --prune step.
      await pruneToEmpty(dir, ntfyToken);
      await deprovisionPublisher();
      fs.rmSync(dir, { recursive: true, force: true });
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
      const verifyResult = await captureOutput(() => main(['verify', manifestPath], deps));
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
  },
);

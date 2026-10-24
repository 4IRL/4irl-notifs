#!/usr/bin/env node
/**
 * Step 1 of the reusable `notifs-app` GitHub action (`.github/actions/notifs-app/action.yml`):
 * trades the workflow run's GitHub Actions OIDC token for an app delivery key (`dak_...`).
 *
 *   1. Requests an OIDC token for `audience` from `$ACTIONS_ID_TOKEN_REQUEST_URL` (the calling job
 *      needs `permissions: id-token: write`).
 *   2. POSTs `{app_id, token}` to `<delivery_url>/v1/app-identity/exchange`, with the Cloudflare
 *      Access service-token headers (the network gate) and a 10 s timeout.
 *   3. Handles the result:
 *        202  the first deploy filed a pending identity request: fail with `::error::` naming the
 *             request id ("awaits operator approval in the admin UI"). No key exists yet.
 *        200  a key was issued: `::add-mask::` it BEFORE anything else touches it, then write the
 *             step outputs `key` and `key_id` to `$GITHUB_OUTPUT`.
 *        else fail with the status and the server's error text.
 *
 * The OIDC token and the key are never logged: both are masked the moment they are in hand, error
 * messages are built only from the status and the server's `error` field, and the key is written
 * only to `$GITHUB_OUTPUT` (never echoed). No npm dependencies.
 *
 * Environment (the action maps its inputs onto these):
 *   NOTIFS_APP_ID, NOTIFS_DELIVERY_URL, NOTIFS_ACCESS_CLIENT_ID, NOTIFS_ACCESS_CLIENT_SECRET,
 *   NOTIFS_AUDIENCE (default `notifs-delivery`, matching DELIVERY_OIDC_AUDIENCE's server default),
 *   plus the runner's ACTIONS_ID_TOKEN_REQUEST_URL / ACTIONS_ID_TOKEN_REQUEST_TOKEN / GITHUB_OUTPUT.
 *
 * `run(deps)` is what tests drive: `deps` swaps `env`, `fetchImpl`, `stdout`, `appendOutput` and
 * `timeoutMs`.
 */
import { appendFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { resolveTarget } from './onboard-client.mjs';
import { NAME_PATTERN as APP_ID_PATTERN } from './onboard-manifest.mjs';

export const DEFAULT_AUDIENCE = 'notifs-delivery';
export const EXCHANGE_PATH = '/v1/app-identity/exchange';
export const REQUEST_TIMEOUT_MS = 10000;

const APP_KEY_PATTERN = /^dak_[A-Za-z0-9_-]{43}$/;
const ERROR_TEXT_LIMIT = 200;

/** A failure with a message safe to print (never contains the OIDC token or a key). */
export class ExchangeError extends Error {
  constructor(message) {
    super(message);
    this.name = 'ExchangeError';
  }
}

function required(env, name) {
  const value = (env[name] ?? '').trim();
  if (value === '') throw new ExchangeError(`${name} is required`);
  return value;
}

/** Workflow-command escaping for the message part of `::error::` / `::add-mask::`. */
function escapeCommand(text) {
  return String(text).replace(/%/g, '%25').replace(/\r/g, '%0D').replace(/\n/g, '%0A');
}

function maskLine(value) {
  return `::add-mask::${escapeCommand(value)}\n`;
}

function errorLine(message) {
  return `::error::${escapeCommand(message)}\n`;
}

/**
 * Turns a timeout/abort (`TimeoutError` from `AbortSignal.timeout`, `AbortError`) into the
 * "<what> timed out" ExchangeError; any other error is left to the caller. Used by every catch
 * around a response-body read too, because the same signal aborts a hung body.
 */
function rethrowIfTimeout(err, what, timeoutMs) {
  if (err?.name === 'TimeoutError' || err?.name === 'AbortError') {
    throw new ExchangeError(`${what} timed out after ${timeoutMs / 1000}s`);
  }
}

/**
 * fetch with a hard timeout; a hang or abort becomes an ExchangeError naming `what`. Redirects are
 * never followed (`redirect: 'manual'`): the exchange carries the Access secret and the OIDC token,
 * and Cloudflare Access answers a refused service token with a 302 to its login page, which then
 * lands in the caller's non-200 branch as "returned 302".
 */
async function timedFetch(fetchImpl, url, init, { what, timeoutMs }) {
  try {
    return await fetchImpl(url, {
      ...init,
      redirect: 'manual',
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch (err) {
    rethrowIfTimeout(err, what, timeoutMs);
    // fetch quotes the offending header value in the TypeError it throws for an invalid header
    // (the Access secret, the Bearer token), so a TypeError never has its text surfaced. The one
    // exception is the bare `fetch failed` undici raises for a network failure, which carries no
    // request data (same rule as onboard-client.mjs).
    if (err instanceof TypeError && err.message !== 'fetch failed') {
      throw new ExchangeError(
        `${what} failed: request could not be built (invalid header value or URL)`,
      );
    }
    throw new ExchangeError(`${what} failed: ${err?.message ?? err}`);
  }
}

/** The server's `error` text for a non-2xx response, bounded; falls back to the raw body. */
async function serverErrorText(response, { what, timeoutMs }) {
  let text = '';
  try {
    text = await response.text();
  } catch (err) {
    rethrowIfTimeout(err, what, timeoutMs);
    return '(unreadable response body)';
  }
  try {
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed.error === 'string' && parsed.error !== '') {
      return parsed.error.slice(0, ERROR_TEXT_LIMIT);
    }
  } catch {
    // not JSON: fall through to the raw text
  }
  const trimmed = text.trim();
  return trimmed === '' ? '(empty response body)' : trimmed.slice(0, ERROR_TEXT_LIMIT);
}

/**
 * Requests the GitHub OIDC token for `audience`. The audience is URL-encoded and appended to the
 * runner-provided request URL (which already carries a query string on hosted runners).
 */
export async function fetchOidcToken({ env, audience, fetchImpl, timeoutMs }) {
  const requestUrl = (env.ACTIONS_ID_TOKEN_REQUEST_URL ?? '').trim();
  const requestToken = (env.ACTIONS_ID_TOKEN_REQUEST_TOKEN ?? '').trim();
  if (requestUrl === '' || requestToken === '') {
    throw new ExchangeError(
      'no GitHub OIDC token is available: add `permissions: id-token: write` to the calling job',
    );
  }
  const separator = requestUrl.includes('?') ? '&' : '?';
  const url = `${requestUrl}${separator}audience=${encodeURIComponent(audience)}`;
  const response = await timedFetch(
    fetchImpl,
    url,
    { headers: { Authorization: `Bearer ${requestToken}`, Accept: 'application/json' } },
    { what: 'OIDC token request', timeoutMs },
  );
  if (!response.ok) {
    throw new ExchangeError(`OIDC token request returned ${response.status}`);
  }
  let body;
  try {
    body = await response.json();
  } catch (err) {
    rethrowIfTimeout(err, 'OIDC token request', timeoutMs);
    throw new ExchangeError('OIDC token request returned a non-JSON body');
  }
  const token = body?.value;
  if (typeof token !== 'string' || token === '') {
    throw new ExchangeError('OIDC token request returned no token');
  }
  return token;
}

/**
 * Runs the whole exchange. Writes workflow commands to `stdout` and, on success, `key=`/`key_id=`
 * to the step output sink. Returns the process exit code (0 issued, 1 anything else).
 */
export async function run({
  env = process.env,
  fetchImpl = globalThis.fetch,
  stdout = (text) => process.stdout.write(text),
  appendOutput = (text) => appendFileSync(requiredOutputFile(env), text),
  timeoutMs = REQUEST_TIMEOUT_MS,
} = {}) {
  try {
    const appId = required(env, 'NOTIFS_APP_ID');
    if (!APP_ID_PATTERN.test(appId)) {
      throw new ExchangeError(`NOTIFS_APP_ID is not a valid app id: ${appId}`);
    }
    const audience = (env.NOTIFS_AUDIENCE ?? '').trim() || DEFAULT_AUDIENCE;

    // Reuses onboard's fail-closed target rules: https (or loopback http) and a complete Access
    // credential pair for any non-loopback URL. The credential vars are named after the action
    // inputs so the rules' error text names what the app author actually set. Trimmed so a pasted
    // trailing newline cannot make the header invalid.
    let target;
    try {
      target = resolveTarget({
        env: {
          ONBOARD_URL: env.NOTIFS_DELIVERY_URL,
          access_client_id: (env.NOTIFS_ACCESS_CLIENT_ID ?? '').trim(),
          access_client_secret: (env.NOTIFS_ACCESS_CLIENT_SECRET ?? '').trim(),
        },
        idVar: 'access_client_id',
        secretVar: 'access_client_secret',
      });
    } catch (err) {
      throw new ExchangeError(err.message.replaceAll('ONBOARD_URL', 'delivery_url'));
    }
    // Defense in depth: the caller should pass a repository secret (already masked by the runner),
    // but mask the Access secret here too so it can never print if they passed a plain value.
    if (target.credentials) stdout(maskLine(target.credentials.secret));

    const oidcToken = await fetchOidcToken({ env, audience, fetchImpl, timeoutMs });
    stdout(maskLine(oidcToken));

    const headers = { 'Content-Type': 'application/json', Accept: 'application/json' };
    if (target.credentials) {
      headers['CF-Access-Client-Id'] = target.credentials.id;
      headers['CF-Access-Client-Secret'] = target.credentials.secret;
    }
    const response = await timedFetch(
      fetchImpl,
      `${target.baseUrl}${EXCHANGE_PATH}`,
      { method: 'POST', headers, body: JSON.stringify({ app_id: appId, token: oidcToken }) },
      { what: 'identity exchange', timeoutMs },
    );

    if (response.status === 202) {
      const body = await response.json().catch((err) => {
        rethrowIfTimeout(err, 'identity exchange', timeoutMs);
        return {};
      });
      // Server-controlled: print only an integer, never an arbitrary value.
      const requestId = Number.isInteger(body?.request_id) ? body.request_id : 'unknown';
      throw new ExchangeError(
        `identity request #${requestId} for app "${appId}" awaits operator approval in the admin UI; ` +
          're-run this deploy once it is approved',
      );
    }
    if (response.status !== 200) {
      const text = await serverErrorText(response, { what: 'identity exchange', timeoutMs });
      throw new ExchangeError(`identity exchange returned ${response.status}: ${text}`);
    }

    let body;
    try {
      body = await response.json();
    } catch (err) {
      rethrowIfTimeout(err, 'identity exchange', timeoutMs);
      throw new ExchangeError('identity exchange returned 200 with a non-JSON body');
    }
    const key = body?.key;
    if (typeof key !== 'string' || !APP_KEY_PATTERN.test(key)) {
      // Do not mask-and-echo a malformed value; say only that the shape is wrong.
      throw new ExchangeError('identity exchange returned 200 without a valid app key');
    }
    // Mask FIRST: nothing below (or any later step) may print the key unmasked.
    stdout(maskLine(key));
    const keyId = Number.isInteger(body.key_id) ? String(body.key_id) : '';
    appendOutput(`key=${key}\nkey_id=${keyId}\n`);
    stdout(`identity exchange issued a key for app "${appId}" (key id ${keyId || 'unknown'})\n`);
    return 0;
  } catch (err) {
    stdout(errorLine(err?.message || 'identity exchange failed'));
    return 1;
  }
}

function requiredOutputFile(env) {
  const file = (env.GITHUB_OUTPUT ?? '').trim();
  if (file === '') throw new ExchangeError('GITHUB_OUTPUT is not set (run this inside Actions)');
  return file;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  run().then((code) => {
    process.exitCode = code;
  });
}

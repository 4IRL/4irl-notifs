// End-to-end smoke test through delivery-api's front door (no direct provisioning-api calls):
// bind the smoke app with a dev `dak_` key, provision a subscriber through the app door, mint the
// app's sealed ntfy publisher, deliver a `to.recipient` message through POST /v1/messages, read it
// back from the subscriber's own topic with the subscriber's own token, then clean everything up.
//
// Run with `make notif-smoke-test` (needs `make local-up`). Importing this module has no side
// effects: the entry point runs only when the file is executed directly, and `runSmokeTest` takes
// injectable `fetchImpl`, `env`, `mint`, `unbind`, `sleep`, `now` and `log`.
import crypto from 'node:crypto';
import { pathToFileURL } from 'node:url';

import { mintAppKey, unbindApp } from './dev-app-key.mjs';

export const SMOKE_APP_ID = 'smoketest';
export const SMOKE_EMAIL = 'smoketest@example.com';
export const SMOKE_CHANNEL = 'all';
export const SMOKE_TITLE = '4IRL smoke test';
export const SUBMIT_WAIT_SECONDS = 10;

const READY_TIMEOUT_MS = 30000;
const READY_INTERVAL_MS = 1000;
const REQUEST_TIMEOUT_MS = 15000;
const SUBMIT_EXTRA_TIMEOUT_MS = 15000;
const NTFY_POLL_TIMEOUT_MS = 10000;
const NTFY_POLL_INTERVAL_MS = 500;
const BASE32_ALPHABET = 'abcdefghijklmnopqrstuvwxyz234567';
const PERSON_HASH_LENGTH = 16;

/** lowercase(base32(sha256(trim+lowercase(email))))[:16], the server's person hash. */
export function personHash(email) {
  const digest = crypto.createHash('sha256').update(email.trim().toLowerCase()).digest();
  let bits = '';
  for (const byte of digest) {
    bits += byte.toString(2).padStart(8, '0');
  }
  let encoded = '';
  for (let index = 0; index + 5 <= bits.length; index += 5) {
    encoded += BASE32_ALPHABET[Number.parseInt(bits.slice(index, index + 5), 2)];
  }
  return encoded.slice(0, PERSON_HASH_LENGTH);
}

/** The ntfy user the server stores for an email: `u_` + person hash. */
export function derivedUserId(email) {
  return `u_${personHash(email)}`;
}

/** The person-channel topic: `{appId}-{hash}-{channel}`. */
export function personChannelTopic({ appId, hash, channel }) {
  return `${appId}-${hash}-${channel}`;
}

/** The POST /v1/messages envelope for a to.recipient message. */
export function buildEnvelope({ appId, email, channel, idempotencyKey, body }) {
  return {
    app_id: appId,
    to: { recipient: email },
    channel,
    idempotency_key: idempotencyKey,
    payload: { n: 1 },
    display: { title: SMOKE_TITLE, body },
  };
}

/** The final PASS/FAIL line. */
export function resultLine({ ok, id, topic, step, detail }) {
  return ok ? `PASS: ${id} delivered on ${topic}` : `FAIL: ${step}: ${detail}`;
}

/** Status plus only the response's `error` string, never the whole payload (it could hold a key). */
function summarize(result) {
  return typeof result.payload?.error === 'string'
    ? `${result.status} ${result.payload.error}`
    : `${result.status}`;
}

class StepError extends Error {
  constructor(step, detail) {
    super(`${step}: ${detail}`);
    this.step = step;
    this.detail = detail;
  }
}

function defaultSleep(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

export async function runSmokeTest({
  fetchImpl = globalThis.fetch,
  env = process.env,
  mint = mintAppKey,
  unbind = unbindApp,
  sleep = defaultSleep,
  now = Date.now,
  log = console.log,
} = {}) {
  for (const name of ['NOTIFS_DELIVERY_URL', 'NOTIFS_NTFY_URL']) {
    if (!env[name]) {
      log(`FAIL: ${name} is not set`);
      return 1;
    }
  }
  const deliveryUrl = env.NOTIFS_DELIVERY_URL;
  const ntfyUrl = env.NOTIFS_NTFY_URL;

  async function call(method, url, { token, body, timeoutMs = REQUEST_TIMEOUT_MS } = {}) {
    const headers = {};
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    if (token) {
      headers.Authorization = `Bearer ${token}`;
    }
    const response = await fetchImpl(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(timeoutMs),
    });
    const text = await response.text().catch(() => '');
    let payload = null;
    try {
      payload = JSON.parse(text);
    } catch {
      payload = null;
    }
    return { status: response.status, payload, text };
  }

  const state = { dak: null, provisioned: false, userId: null };
  let failure = null;
  let passId = null;
  let topic = null;
  const failStep = (step, detail) => {
    if (!failure) {
      failure = { step, detail };
    }
  };

  try {
    // 1. Wait for readiness.
    const readyDeadline = now() + READY_TIMEOUT_MS;
    let ready = false;
    while (!ready) {
      try {
        ready = (await call('GET', `${deliveryUrl}/readyz`)).status === 200;
      } catch {
        ready = false;
      }
      if (!ready) {
        if (now() >= readyDeadline) {
          throw new StepError(
            'readyz',
            `delivery-api not ready at ${deliveryUrl}; run make local-up first`,
          );
        }
        await sleep(READY_INTERVAL_MS);
      }
    }

    // 2. Pre-clean a remnant of an interrupted earlier run; never fails the run by itself.
    try {
      const preClean = await call(
        'POST',
        `${deliveryUrl}/v1/admin/apps/${SMOKE_APP_ID}/deprovision`,
        {
          body: { app_id: SMOKE_APP_ID },
        },
      );
      log(`pre-clean teardown: ${summarize(preClean)}`);
    } catch (error) {
      log(`pre-clean teardown: ${error.message}`);
    }
    try {
      await unbind({ deliveryUrl, appId: SMOKE_APP_ID });
      log('pre-clean unbind: ok');
    } catch (error) {
      log(`pre-clean unbind: ${error.message}`);
    }

    // 3. Mint the app key.
    try {
      state.dak = await mint({ deliveryUrl, appId: SMOKE_APP_ID });
    } catch (error) {
      throw new StepError('mint app key', error.message);
    }

    // 4. Provision the subscriber through the app door.
    const provisioned = await call('POST', `${deliveryUrl}/v1/apps/${SMOKE_APP_ID}/provision`, {
      token: state.dak,
      body: { email: SMOKE_EMAIL },
    });
    if (provisioned.status === 200) {
      state.provisioned = true;
    }
    if (provisioned.status !== 200 || typeof provisioned.payload?.token !== 'string') {
      throw new StepError('provision', summarize(provisioned));
    }
    state.userId = provisioned.payload.user_id ?? null;
    const subscriberToken = provisioned.payload.token;
    const hash = provisioned.payload.person_hash ?? personHash(SMOKE_EMAIL);

    // 5. Mint the sealed publisher; the door must never return a token.
    const publisher = await call(
      'POST',
      `${deliveryUrl}/v1/admin/apps/${SMOKE_APP_ID}/ntfy-publisher`,
    );
    if (publisher.status !== 200) {
      throw new StepError('mint publisher', summarize(publisher));
    }
    if (!publisher.payload || typeof publisher.payload !== 'object') {
      throw new StepError('mint publisher', '200 response is not a JSON object');
    }
    if (
      publisher.payload.publisher == null ||
      typeof publisher.payload.publisher !== 'object' ||
      'token' in publisher.payload ||
      'token' in publisher.payload.publisher
    ) {
      throw new StepError('mint publisher', 'response is missing publisher or carries a token key');
    }

    // 6. Deliver a to.recipient message.
    const nowMs = now();
    const messageBody = `notif-smoke-test ${Math.floor(nowMs / 1000)}`;
    const submitted = await call('POST', `${deliveryUrl}/v1/messages?wait=${SUBMIT_WAIT_SECONDS}`, {
      token: state.dak,
      body: buildEnvelope({
        appId: SMOKE_APP_ID,
        email: SMOKE_EMAIL,
        channel: SMOKE_CHANNEL,
        idempotencyKey: `smoke-${nowMs}`,
        body: messageBody,
      }),
      timeoutMs: SUBMIT_WAIT_SECONDS * 1000 + SUBMIT_EXTRA_TIMEOUT_MS,
    });
    if (submitted.status !== 202) {
      throw new StepError('submit message', summarize(submitted));
    }
    const deliveries = submitted.payload?.message?.deliveries;
    if (!Array.isArray(deliveries) || deliveries.length !== 1) {
      throw new StepError(
        'submit message',
        `expected exactly 1 delivery, got ${deliveries?.length}`,
      );
    }
    if (deliveries[0].state !== 'delivered') {
      throw new StepError('submit message', `delivery state ${deliveries[0].state}`);
    }
    passId = submitted.payload.message.id ?? deliveries[0].id ?? 'message';

    // 7. Read it back from the subscriber's own topic with the subscriber's own token.
    topic = personChannelTopic({ appId: SMOKE_APP_ID, hash, channel: SMOKE_CHANNEL });
    const pollDeadline = now() + NTFY_POLL_TIMEOUT_MS;
    let found = false;
    for (;;) {
      try {
        const polled = await call('GET', `${ntfyUrl}/${topic}/json?poll=1&since=all`, {
          token: subscriberToken,
        });
        found = polled.text
          .split('\n')
          .filter((line) => line.trim() !== '')
          .some((line) => {
            try {
              const event = JSON.parse(line);
              return event.title === SMOKE_TITLE && event.message === messageBody;
            } catch {
              return false;
            }
          });
      } catch {
        found = false;
      }
      if (found || now() >= pollDeadline) {
        break;
      }
      await sleep(NTFY_POLL_INTERVAL_MS);
    }
    if (!found) {
      throw new StepError('read subscriber topic', `message not found on ${topic}`);
    }
  } catch (error) {
    if (error instanceof StepError) {
      failStep(error.step, error.detail);
    } else {
      failStep('unexpected', error.message);
    }
  }

  // 8. Cleanup: every step runs; each must answer 200.
  async function cleanupStep(label, action) {
    try {
      const result = await action();
      if (result.status !== 200) {
        log(`cleanup ${label}: ${summarize(result)}`);
        failStep(`cleanup ${label}`, summarize(result));
      } else {
        log(`cleanup ${label}: 200`);
      }
    } catch (error) {
      log(`cleanup ${label}: ${error.message}`);
      failStep(`cleanup ${label}`, error.message);
    }
  }

  if (state.dak && state.provisioned) {
    await cleanupStep('app deprovision', () =>
      call('POST', `${deliveryUrl}/v1/apps/${SMOKE_APP_ID}/deprovision`, {
        token: state.dak,
        body: { email: SMOKE_EMAIL },
      }),
    );
  } else {
    log(
      `cleanup app deprovision: skipped: ${state.dak ? 'provision did not succeed' : 'no app key'}`,
    );
  }
  const cleanupUserId = state.userId ?? derivedUserId(SMOKE_EMAIL);
  await cleanupStep('delete user', () =>
    call('DELETE', `${deliveryUrl}/v1/admin/users/${cleanupUserId}`),
  );
  await cleanupStep('operator teardown', () =>
    call('POST', `${deliveryUrl}/v1/admin/apps/${SMOKE_APP_ID}/deprovision`, {
      body: { app_id: SMOKE_APP_ID },
    }),
  );
  try {
    await unbind({ deliveryUrl, appId: SMOKE_APP_ID });
    log('cleanup unbind: ok');
  } catch (error) {
    log(`cleanup unbind: ${error.message}`);
    failStep('cleanup unbind', error.message);
  }

  if (failure) {
    log(resultLine({ ok: false, step: failure.step, detail: failure.detail }));
    return 1;
  }
  log(resultLine({ ok: true, id: passId, topic }));
  return 0;
}

const invokedDirectly =
  process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href;
if (invokedDirectly) {
  process.exitCode = await runSmokeTest();
}

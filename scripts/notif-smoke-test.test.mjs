import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import crypto from 'node:crypto';
import { describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  SMOKE_APP_ID,
  SMOKE_EMAIL,
  buildEnvelope,
  derivedUserId,
  personChannelTopic,
  personHash,
  resultLine,
  runSmokeTest,
} from './notif-smoke-test.mjs';

const DELIVERY = 'http://delivery.test';
const NTFY = 'http://ntfy.test';
const ENV = { NOTIFS_DELIVERY_URL: DELIVERY, NOTIFS_NTFY_URL: NTFY };
const DAK = `dak_${'B'.repeat(43)}`;
const SUBSCRIBER_TOKEN = 'tk_subscribersecret';

// Independent RFC 4648 base32 of the sha256 digest (separate from the module's encoder).
function independentHash(email) {
  const digest = crypto.createHash('sha256').update(email.trim().toLowerCase()).digest();
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let value = 0n;
  for (const byte of digest) {
    value = (value << 8n) | BigInt(byte);
  }
  // 256 bits -> pad to 260 bits (52 chars of 5 bits)
  value <<= 4n;
  let out = '';
  for (let index = 51; index >= 0; index -= 1) {
    out += alphabet[Number((value >> BigInt(index * 5)) & 31n)];
  }
  return out.toLowerCase().slice(0, 16);
}

function response(status, body) {
  return new Response(typeof body === 'string' ? body : JSON.stringify(body), { status });
}

/** A stubbed delivery/ntfy world. `overrides` maps "METHOD path" to a response or a function. */
function makeWorld({ overrides = {}, ntfy = null } = {}) {
  const requests = [];
  let ntfyPolls = 0;
  const logs = [];
  const hash = personHash(SMOKE_EMAIL);
  let messageBody = null;
  const defaults = {
    'GET /readyz': () => response(200, { ok: true }),
    [`POST /v1/admin/apps/${SMOKE_APP_ID}/deprovision`]: () => response(200, {}),
    [`POST /v1/apps/${SMOKE_APP_ID}/provision`]: () =>
      response(200, { user_id: `u_${hash}`, person_hash: hash, token: SUBSCRIBER_TOKEN }),
    [`POST /v1/admin/apps/${SMOKE_APP_ID}/ntfy-publisher`]: () =>
      response(200, {
        publisher: { app_id: SMOKE_APP_ID, publisher_user_id: 'smoketest-publisher' },
      }),
    'POST /v1/messages': (request) => {
      messageBody = JSON.parse(request.body).display.body;
      return response(202, {
        message: { id: 'msg_1', deliveries: [{ id: 'del_1', state: 'delivered' }] },
      });
    },
    [`POST /v1/apps/${SMOKE_APP_ID}/deprovision`]: () => response(200, {}),
    [`DELETE /v1/admin/users/u_${hash}`]: () => response(200, {}),
  };
  const fetchImpl = async (url, init) => {
    const parsed = new URL(url);
    const request = {
      method: init.method,
      path: parsed.pathname,
      url,
      headers: init.headers,
      body: init.body,
    };
    requests.push(request);
    if (parsed.origin === NTFY) {
      ntfyPolls += 1;
      if (ntfy) {
        return response(200, ntfy({ poll: ntfyPolls, messageBody }));
      }
      const line = JSON.stringify({
        event: 'message',
        title: '4IRL smoke test',
        message: messageBody,
      });
      return response(200, `${line}\n`);
    }
    const key = `${init.method} ${parsed.pathname}`;
    const handler = overrides[key] ?? defaults[key];
    if (!handler) {
      return response(500, { error: `unstubbed ${key}` });
    }
    return typeof handler === 'function' ? handler(request) : handler;
  };
  return { fetchImpl, requests, logs, log: (line) => logs.push(line) };
}

/** A fake clock: `sleep` advances `now`, so deadlines are reached without real waiting. */
function makeClock() {
  let current = 1_000_000;
  return {
    now: () => current,
    sleep: async (milliseconds) => {
      current += milliseconds;
    },
  };
}

async function run(world, extra = {}) {
  const clock = makeClock();
  return runSmokeTest({
    fetchImpl: world.fetchImpl,
    env: ENV,
    mint: async () => DAK,
    unbind: async () => ({ deleted: true }),
    sleep: clock.sleep,
    now: clock.now,
    log: world.log,
    ...extra,
  });
}

function sentOf(world) {
  return world.requests.map((request) => `${request.method} ${request.path}`);
}

/** A handler that answers `first` on its first call and `later` afterwards. */
function firstThen(first, later) {
  let calls = 0;
  return () => {
    calls += 1;
    const chosen = calls === 1 ? first : later;
    return typeof chosen === 'function' ? chosen() : chosen;
  };
}

const ADMIN_DEPROVISION = `POST /v1/admin/apps/${SMOKE_APP_ID}/deprovision`;
const APP_DEPROVISION = `POST /v1/apps/${SMOKE_APP_ID}/deprovision`;
const PUBLISHER = `POST /v1/admin/apps/${SMOKE_APP_ID}/ntfy-publisher`;
const PROVISION = `POST /v1/apps/${SMOKE_APP_ID}/provision`;
const DELETE_USER = `DELETE /v1/admin/users/u_${personHash(SMOKE_EMAIL)}`;

describe('pure helpers', () => {
  it('computes the person hash independently of the fixture encoder', () => {
    assert.equal(personHash(SMOKE_EMAIL), independentHash(SMOKE_EMAIL));
    assert.equal(personHash('  SmokeTest@Example.com '), independentHash(SMOKE_EMAIL));
    assert.match(personHash(SMOKE_EMAIL), /^[a-z2-7]{16}$/);
  });

  it('derives u_ + the person hash', () => {
    assert.equal(derivedUserId(SMOKE_EMAIL), `u_${independentHash(SMOKE_EMAIL)}`);
  });

  it('builds the topic as app-hash-channel', () => {
    assert.equal(
      personChannelTopic({ appId: 'smoketest', hash: 'abc', channel: 'all' }),
      'smoketest-abc-all',
    );
  });

  it('builds the envelope with exactly the contract keys', () => {
    const envelope = buildEnvelope({
      appId: 'smoketest',
      email: SMOKE_EMAIL,
      channel: 'all',
      idempotencyKey: 'smoke-1',
      body: 'b',
    });
    assert.deepEqual(Object.keys(envelope).sort(), [
      'app_id',
      'channel',
      'display',
      'idempotency_key',
      'payload',
      'to',
    ]);
    assert.deepEqual(envelope.to, { recipient: SMOKE_EMAIL });
    assert.deepEqual(envelope.display, { title: '4IRL smoke test', body: 'b' });
  });

  it('formats the result line', () => {
    assert.equal(resultLine({ ok: true, id: 'm', topic: 't' }), 'PASS: m delivered on t');
    assert.equal(resultLine({ ok: false, step: 's', detail: '500 x' }), 'FAIL: s: 500 x');
  });
});

describe('runSmokeTest', () => {
  it('passes on the happy path and never touches provisioning-api or logs secrets', async () => {
    const world = makeWorld();
    assert.equal(await run(world), 0);
    assert.match(world.logs.at(-1), /^PASS: msg_1 delivered on smoketest-/);
    const everything = world.logs.join('\n');
    assert.ok(!everything.includes(DAK));
    assert.ok(!everything.includes(SUBSCRIBER_TOKEN));
    assert.ok(
      world.requests.every(
        (request) => request.url.startsWith(DELIVERY) || request.url.startsWith(NTFY),
      ),
    );
  });

  it('fails naming the variable when either env var is unset', async () => {
    for (const name of ['NOTIFS_DELIVERY_URL', 'NOTIFS_NTFY_URL']) {
      const world = makeWorld();
      const env = { ...ENV };
      delete env[name];
      assert.equal(await run(world, { env }), 1);
      assert.match(world.logs.join('\n'), new RegExp(name));
      assert.equal(world.requests.length, 0);
    }
  });

  it('still cleans up and exits 1 when the message step fails', async () => {
    const world = makeWorld({
      overrides: { 'POST /v1/messages': response(422, { error: 'app has no ntfy publisher' }) },
    });
    assert.equal(await run(world), 1);
    const sent = world.requests.map((request) => `${request.method} ${request.path}`);
    assert.ok(sent.includes(`POST /v1/apps/${SMOKE_APP_ID}/deprovision`));
    assert.ok(sent.includes(`DELETE /v1/admin/users/${derivedUserId(SMOKE_EMAIL)}`));
    assert.match(world.logs.at(-1), /^FAIL: submit message: 422/);
  });

  const cleanupCases = [
    {
      label: 'app deprovision',
      key: APP_DEPROVISION,
      override: () => response(500, { error: 'boom' }),
      later: [DELETE_USER, ADMIN_DEPROVISION],
    },
    {
      label: 'delete user',
      key: DELETE_USER,
      override: () => response(500, { error: 'boom' }),
      later: [ADMIN_DEPROVISION],
    },
    {
      // The first admin deprovision is the pre-clean; only the cleanup one fails.
      label: 'operator teardown',
      key: ADMIN_DEPROVISION,
      override: firstThen(response(200, {}), () => response(500, { error: 'boom' })),
      later: [],
    },
  ];
  for (const { label, key, override, later } of cleanupCases) {
    it(`exits 1 on a non-200 cleanup ${label}, and still runs the later steps`, async () => {
      const world = makeWorld({ overrides: { [key]: override } });
      let unbound = 0;
      const code = await run(world, {
        unbind: async () => {
          unbound += 1;
          return { deleted: true };
        },
      });
      assert.equal(code, 1);
      assert.equal(unbound, 2); // pre-clean + cleanup
      const sent = sentOf(world);
      for (const laterKey of later) {
        assert.ok(sent.includes(laterKey), `${laterKey} did not run`);
      }
      assert.equal(world.logs.at(-1), `FAIL: cleanup ${label}: 500 boom`);
    });
  }

  it('exits 1 when unbind throws in cleanup', async () => {
    const world = makeWorld();
    let calls = 0;
    const code = await run(world, {
      unbind: async () => {
        calls += 1;
        if (calls > 1) {
          throw new Error('unbind smoketest: 500');
        }
        return { deleted: true };
      },
    });
    assert.equal(code, 1);
    assert.match(world.logs.at(-1), /^FAIL: cleanup unbind/);
  });

  it('skips the app deprovision but deletes the derived user when mint fails', async () => {
    const world = makeWorld();
    const code = await run(world, {
      mint: async () => {
        throw new Error('exchange: 403');
      },
    });
    assert.equal(code, 1);
    assert.match(world.logs.join('\n'), /cleanup app deprovision: skipped: no app key/);
    const sent = world.requests.map((request) => `${request.method} ${request.path}`);
    assert.ok(!sent.includes(`POST /v1/apps/${SMOKE_APP_ID}/deprovision`));
    assert.ok(sent.includes(`DELETE /v1/admin/users/u_${independentHash(SMOKE_EMAIL)}`));
  });

  it('skips the app deprovision but deletes the derived user when provision fails', async () => {
    const world = makeWorld({
      overrides: { [`POST /v1/apps/${SMOKE_APP_ID}/provision`]: response(403, { error: 'no' }) },
    });
    assert.equal(await run(world), 1);
    assert.match(world.logs.join('\n'), /cleanup app deprovision: skipped/);
    const sent = world.requests.map((request) => `${request.method} ${request.path}`);
    assert.ok(sent.includes(`DELETE /v1/admin/users/u_${independentHash(SMOKE_EMAIL)}`));
  });

  it('fails when the publisher mint response carries a token key', async () => {
    const world = makeWorld({
      overrides: {
        [`POST /v1/admin/apps/${SMOKE_APP_ID}/ntfy-publisher`]: response(200, {
          publisher: { app_id: SMOKE_APP_ID, token: 'tk_leak' },
        }),
      },
    });
    assert.equal(await run(world), 1);
    assert.ok(!world.logs.join('\n').includes('tk_leak'));
    assert.match(world.logs.at(-1), /^FAIL: mint publisher/);
  });

  it('fails when the publisher mint response carries a top-level token key', async () => {
    const world = makeWorld({
      overrides: {
        [PUBLISHER]: response(200, { publisher: { app_id: SMOKE_APP_ID }, token: 'tk_leak' }),
      },
    });
    assert.equal(await run(world), 1);
    assert.ok(!world.logs.join('\n').includes('tk_leak'));
    assert.match(world.logs.at(-1), /^FAIL: mint publisher/);
  });

  it('fails when the publisher mint response has no publisher key', async () => {
    const world = makeWorld({ overrides: { [PUBLISHER]: response(200, { ok: true }) } });
    assert.equal(await run(world), 1);
    assert.match(world.logs.at(-1), /^FAIL: mint publisher/);
  });

  it('fails with the status when the publisher mint is non-200', async () => {
    const world = makeWorld({ overrides: { [PUBLISHER]: response(500, { error: 'nope' }) } });
    assert.equal(await run(world), 1);
    assert.match(world.logs.at(-1), /^FAIL: mint publisher: 500 nope/);
  });

  it('still runs the app deprovision when provision answers 200 without a token', async () => {
    const world = makeWorld({
      overrides: { [PROVISION]: response(200, { user_id: `u_${personHash(SMOKE_EMAIL)}` }) },
    });
    assert.equal(await run(world), 1);
    assert.ok(sentOf(world).includes(APP_DEPROVISION));
    assert.match(world.logs.at(-1), /^FAIL: provision/);
  });

  it('sends the right credentials and reads the subscriber topic with the subscriber token', async () => {
    const world = makeWorld();
    assert.equal(await run(world), 0);
    const bearerDak = `Bearer ${DAK}`;
    for (const request of world.requests) {
      const key = `${request.method} ${request.path}`;
      if (request.url.startsWith(NTFY)) {
        continue;
      }
      if ([PROVISION, 'POST /v1/messages', APP_DEPROVISION].includes(key)) {
        assert.equal(request.headers.Authorization, bearerDak, key);
      } else {
        assert.equal(request.headers.Authorization, undefined, key);
      }
    }
    const polls = world.requests.filter((request) => request.url.startsWith(NTFY));
    assert.ok(polls.length >= 1);
    const topic = `${SMOKE_APP_ID}-${personHash(SMOKE_EMAIL)}-all`;
    for (const poll of polls) {
      const parsed = new URL(poll.url);
      assert.equal(parsed.pathname, `/${topic}/json`);
      assert.equal(parsed.search, '?poll=1&since=all');
      assert.equal(poll.headers.Authorization, `Bearer ${SUBSCRIBER_TOKEN}`);
    }
  });

  it('submits the envelope with wait=10 and the body the topic read-back requires', async () => {
    const world = makeWorld();
    assert.equal(await run(world), 0);
    const submit = world.requests.find((request) => request.path === '/v1/messages');
    assert.equal(new URL(submit.url).search, '?wait=10');
    const sent = JSON.parse(submit.body);
    assert.match(sent.idempotency_key, /^smoke-\d+$/);
    assert.deepEqual(
      sent,
      buildEnvelope({
        appId: SMOKE_APP_ID,
        email: SMOKE_EMAIL,
        channel: 'all',
        idempotencyKey: sent.idempotency_key,
        body: sent.display.body,
      }),
    );
    assert.equal(sent.to.recipient, SMOKE_EMAIL);
  });

  const deliveryCases = [
    { name: 'zero deliveries', deliveries: [] },
    {
      name: 'two deliveries',
      deliveries: [
        { id: 'd1', state: 'delivered' },
        { id: 'd2', state: 'delivered' },
      ],
    },
    { name: 'a non-delivered state', deliveries: [{ id: 'd1', state: 'failed' }] },
  ];
  for (const { name, deliveries } of deliveryCases) {
    it(`fails the message step on ${name} and still cleans up`, async () => {
      const world = makeWorld({
        overrides: {
          'POST /v1/messages': response(202, { message: { id: 'msg_1', deliveries } }),
        },
      });
      assert.equal(await run(world), 1);
      assert.match(world.logs.at(-1), /^FAIL: submit message: /);
      assert.ok(sentOf(world).includes(APP_DEPROVISION));
      assert.ok(sentOf(world).includes(DELETE_USER));
    });
  }

  it('fails reading the subscriber topic when the message never appears, and cleans up', async () => {
    const world = makeWorld({ ntfy: () => '' });
    assert.equal(await run(world), 1);
    assert.match(world.logs.at(-1), /^FAIL: read subscriber topic: /);
    assert.ok(sentOf(world).includes(APP_DEPROVISION));
    assert.ok(sentOf(world).includes(DELETE_USER));
  });

  it('fails reading the subscriber topic when the title or body differs', async () => {
    const world = makeWorld({
      ntfy: () => `${JSON.stringify({ event: 'message', title: 'other', message: 'x' })}\n`,
    });
    assert.equal(await run(world), 1);
    assert.match(world.logs.at(-1), /^FAIL: read subscriber topic: /);
  });

  it('retries the topic poll until the message shows up', async () => {
    const world = makeWorld({
      ntfy: ({ poll, messageBody }) =>
        poll < 3
          ? ''
          : `${JSON.stringify({ event: 'message', title: '4IRL smoke test', message: messageBody })}\n`,
    });
    assert.equal(await run(world), 0);
    assert.equal(world.requests.filter((request) => request.url.startsWith(NTFY)).length, 3);
  });

  it('fails readyz with the make local-up hint once the fake clock passes the deadline', async () => {
    const world = makeWorld({ overrides: { 'GET /readyz': response(503, {}) } });
    assert.equal(await run(world), 1);
    assert.equal(
      world.logs.at(-1),
      `FAIL: readyz: delivery-api not ready at ${DELIVERY}; run make local-up first`,
    );
  });

  it('tolerates a non-200 pre-clean teardown', async () => {
    const world = makeWorld({
      overrides: {
        [ADMIN_DEPROVISION]: firstThen(response(500, { error: 'old' }), response(200, {})),
      },
    });
    assert.equal(await run(world), 0);
    assert.match(world.logs.join('\n'), /pre-clean teardown: 500 old/);
  });

  it('tolerates a thrown pre-clean teardown', async () => {
    const world = makeWorld({
      overrides: {
        [ADMIN_DEPROVISION]: firstThen(
          () => {
            throw new Error('socket hang up');
          },
          response(200, {}),
        ),
      },
    });
    assert.equal(await run(world), 0);
    assert.match(world.logs.join('\n'), /pre-clean teardown: socket hang up/);
  });

  it('tolerates a thrown pre-clean unbind', async () => {
    const world = makeWorld();
    let calls = 0;
    const code = await run(world, {
      unbind: async () => {
        calls += 1;
        if (calls === 1) {
          throw new Error('unbind smoketest: 500');
        }
        return { deleted: true };
      },
    });
    assert.equal(code, 0);
    assert.match(world.logs.join('\n'), /pre-clean unbind: unbind smoketest: 500/);
  });

  it('never logs the dak or any token on a failing run', async () => {
    const world = makeWorld({
      overrides: {
        [PUBLISHER]: response(200, { publisher: { token: 'tk_leak' } }),
        [APP_DEPROVISION]: response(500, { error: 'boom' }),
      },
    });
    assert.equal(await run(world), 1);
    const everything = world.logs.join('\n');
    for (const secret of [DAK, SUBSCRIBER_TOKEN, 'tk_leak']) {
      assert.ok(!everything.includes(secret), secret);
    }
  });
});

describe('CLI entry', () => {
  it('exits 1 and names the missing variable when the env is unset', () => {
    const script = fileURLToPath(new URL('./notif-smoke-test.mjs', import.meta.url));
    const result = spawnSync('node', [script], {
      encoding: 'utf8',
      env: { PATH: process.env.PATH },
    });
    assert.equal(result.status, 1);
    assert.match(result.stdout, /FAIL: NOTIFS_DELIVERY_URL is not set/);
  });
});

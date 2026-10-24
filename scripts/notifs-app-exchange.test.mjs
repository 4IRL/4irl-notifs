import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, describe, it } from 'node:test';

import { REPO_ROOT } from './ports.mjs';
import {
  DEFAULT_AUDIENCE,
  EXCHANGE_PATH,
  REQUEST_TIMEOUT_MS,
  run,
} from './notifs-app-exchange.mjs';

const OIDC_REQUEST_URL = 'https://pipelines.actions.example/idtoken?api-version=2.0';
const OIDC_REQUEST_TOKEN = 'runner-request-token';
const OIDC_JWT = 'eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.signature-part';
const KEY = `dak_${'AbC-_'.repeat(8)}${'xyz'}`; // 4 + 43 chars
const DELIVERY_URL = 'https://delivery.example.com';
const ACCESS_ID = 'access-id.access';
const ACCESS_SECRET = 'access-secret-value';

const tmpRoots = [];

after(() => {
  for (const root of tmpRoots) fs.rmSync(root, { recursive: true, force: true });
});

function baseEnv(overrides = {}) {
  return {
    NOTIFS_APP_ID: 'chores',
    NOTIFS_DELIVERY_URL: DELIVERY_URL,
    NOTIFS_ACCESS_CLIENT_ID: ACCESS_ID,
    NOTIFS_ACCESS_CLIENT_SECRET: ACCESS_SECRET,
    ACTIONS_ID_TOKEN_REQUEST_URL: OIDC_REQUEST_URL,
    ACTIONS_ID_TOKEN_REQUEST_TOKEN: OIDC_REQUEST_TOKEN,
    GITHUB_OUTPUT: '/unused',
    ...overrides,
  };
}

function json(status, body) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * A scripted fake fetch: records every call, answers the OIDC request with `oidc` (default: a
 * token) and the exchange with `exchange`. A function answer receives the call and may return a
 * promise (used to model a hang that only an abort signal ends). `events` is ONE ordered log of
 * every stdout write, output write and fetch call, so tests can assert what happened before what.
 */
function makeHarness({ oidc, exchange, env } = {}) {
  const calls = [];
  const stdout = [];
  const outputs = [];
  const events = [];
  const fetchImpl = async (url, init) => {
    calls.push({ url: String(url), init });
    events.push({ type: 'fetch', url: String(url) });
    const isExchange = String(url).endsWith(EXCHANGE_PATH);
    const answer = isExchange ? exchange : (oidc ?? (() => json(200, { value: OIDC_JWT })));
    return typeof answer === 'function' ? answer({ url, init }) : answer;
  };
  const deps = {
    env: baseEnv(env),
    fetchImpl,
    stdout: (text) => {
      stdout.push(text);
      events.push({ type: 'stdout', text });
    },
    appendOutput: (text) => {
      outputs.push(text);
      events.push({ type: 'output', text });
    },
  };
  return { calls, stdout, outputs, events, deps, text: () => stdout.join('') };
}

function eventIndex(harness, predicate) {
  return harness.events.findIndex(predicate);
}

function maskIndex(harness, value) {
  return eventIndex(
    harness,
    (event) => event.type === 'stdout' && event.text === `::add-mask::${value}\n`,
  );
}

/** Everything the run printed except `::add-mask::` lines (which carry the masked values). */
function unmaskedText(harness) {
  return harness.stdout.filter((line) => !line.startsWith('::add-mask::')).join('');
}

function hangUntilAborted({ init }) {
  return new Promise((_, reject) => {
    init.signal.addEventListener('abort', () => reject(init.signal.reason));
  });
}

/** For a mocked `AbortSignal.timeout` that hands out already-aborted signals. */
function rejectWithAbortReason({ init }) {
  return Promise.reject(init.signal.reason);
}

/** A response whose body read fails the way an aborted/timed-out stream does. */
function bodyTimesOut(status) {
  const fail = async () => {
    throw new DOMException('The operation was aborted due to timeout', 'TimeoutError');
  };
  return { ok: status >= 200 && status < 300, status, json: fail, text: fail };
}

describe('run: key issued (200)', () => {
  it('masks the key before writing outputs and never echoes it', async () => {
    const h = makeHarness({
      exchange: json(200, { status: 'issued', app_id: 'chores', key: KEY, key_id: 7 }),
    });

    const code = await run(h.deps);

    assert.equal(code, 0);
    assert.deepEqual(h.outputs, [`key=${KEY}\nkey_id=7\n`]);
    const keyMask = maskIndex(h, KEY);
    const outputWrite = eventIndex(h, (event) => event.type === 'output');
    assert.ok(keyMask >= 0, 'key must be masked');
    assert.ok(keyMask < outputWrite, 'the key must be masked before it is written to the output');
    for (const [i, event] of h.events.entries()) {
      if (i !== keyMask && event.type === 'stdout') {
        assert.ok(!event.text.includes(KEY), `stdout event ${i} leaks the key: ${event.text}`);
      }
    }
  });

  it('masks the OIDC token as soon as it is received, before the exchange, and never prints it otherwise', async () => {
    const h = makeHarness({ exchange: json(200, { key: KEY, key_id: 1 }) });

    await run(h.deps);

    const oidcMask = maskIndex(h, OIDC_JWT);
    const exchangeFetch = eventIndex(
      h,
      (event) => event.type === 'fetch' && event.url.endsWith(EXCHANGE_PATH),
    );
    assert.ok(oidcMask >= 0, 'the OIDC token must be masked');
    assert.ok(oidcMask < exchangeFetch, 'the OIDC token must be masked before it is sent');
    assert.ok(!unmaskedText(h).includes(OIDC_JWT));
  });

  it('also masks the Access client secret before any request, and never prints it otherwise', async () => {
    const h = makeHarness({ exchange: json(200, { key: KEY, key_id: 1 }) });

    await run(h.deps);

    const secretMask = maskIndex(h, ACCESS_SECRET);
    assert.ok(secretMask >= 0, 'the Access secret must be masked');
    assert.ok(secretMask < eventIndex(h, (event) => event.type === 'fetch'));
    assert.ok(!unmaskedText(h).includes(ACCESS_SECRET));
  });

  it('requests the OIDC token for the default audience with the runner request token', async () => {
    const h = makeHarness({ exchange: json(200, { key: KEY, key_id: 1 }) });

    await run(h.deps);

    const [oidcCall] = h.calls;
    assert.equal(oidcCall.url, `${OIDC_REQUEST_URL}&audience=${DEFAULT_AUDIENCE}`);
    assert.equal(DEFAULT_AUDIENCE, 'notifs-delivery');
    assert.equal(oidcCall.init.headers.Authorization, `Bearer ${OIDC_REQUEST_TOKEN}`);
  });

  it('requests a non-default, URL-encoded audience when one is given', async () => {
    const h = makeHarness({
      env: { NOTIFS_AUDIENCE: 'my audience/1' },
      exchange: json(200, { key: KEY, key_id: 1 }),
    });

    await run(h.deps);

    assert.equal(h.calls[0].url, `${OIDC_REQUEST_URL}&audience=my%20audience%2F1`);
  });

  it('falls back to the default audience when the input is blank', async () => {
    const h = makeHarness({
      env: { NOTIFS_AUDIENCE: '  ' },
      exchange: json(200, { key: KEY, key_id: 1 }),
    });

    await run(h.deps);

    assert.ok(h.calls[0].url.endsWith('&audience=notifs-delivery'));
  });

  it('starts the audience query when the runner URL has none', async () => {
    const h = makeHarness({
      env: { ACTIONS_ID_TOKEN_REQUEST_URL: 'https://pipelines.actions.example/idtoken' },
      exchange: json(200, { key: KEY, key_id: 1 }),
    });

    await run(h.deps);

    assert.equal(
      h.calls[0].url,
      'https://pipelines.actions.example/idtoken?audience=notifs-delivery',
    );
  });

  it('posts {app_id, token} to the exchange endpoint with the Access headers and a timeout signal', async () => {
    const h = makeHarness({ exchange: json(200, { key: KEY, key_id: 1 }) });

    await run(h.deps);

    const exchangeCall = h.calls[1];
    assert.equal(exchangeCall.url, `${DELIVERY_URL}${EXCHANGE_PATH}`);
    assert.equal(exchangeCall.init.method, 'POST');
    assert.deepEqual(JSON.parse(exchangeCall.init.body), { app_id: 'chores', token: OIDC_JWT });
    assert.equal(exchangeCall.init.headers['CF-Access-Client-Id'], ACCESS_ID);
    assert.equal(exchangeCall.init.headers['CF-Access-Client-Secret'], ACCESS_SECRET);
    assert.equal(exchangeCall.init.headers['Content-Type'], 'application/json');
    assert.ok(exchangeCall.init.signal instanceof AbortSignal);
    assert.ok(!('Authorization' in exchangeCall.init.headers), 'the exchange has no bearer');
    assert.equal(REQUEST_TIMEOUT_MS, 10000);
  });

  it('trims the Access id and secret before they become headers', async () => {
    const h = makeHarness({
      env: {
        NOTIFS_ACCESS_CLIENT_ID: `${ACCESS_ID}\n`,
        NOTIFS_ACCESS_CLIENT_SECRET: ` ${ACCESS_SECRET}\r\n`,
      },
      exchange: json(200, { key: KEY, key_id: 1 }),
    });

    assert.equal(await run(h.deps), 0);
    assert.equal(h.calls[1].init.headers['CF-Access-Client-Id'], ACCESS_ID);
    assert.equal(h.calls[1].init.headers['CF-Access-Client-Secret'], ACCESS_SECRET);
  });

  it('allows a loopback http URL without Access credentials', async () => {
    const h = makeHarness({
      env: {
        NOTIFS_DELIVERY_URL: 'http://127.0.0.1:8081',
        NOTIFS_ACCESS_CLIENT_ID: '',
        NOTIFS_ACCESS_CLIENT_SECRET: '',
      },
      exchange: json(200, { key: KEY, key_id: 1 }),
    });

    assert.equal(await run(h.deps), 0);
    assert.ok(!('CF-Access-Client-Id' in h.calls[1].init.headers));
  });

  it('writes the key to the GITHUB_OUTPUT file by default', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-exchange-test-'));
    tmpRoots.push(root);
    const outputFile = path.join(root, 'github_output');
    const h = makeHarness({
      env: { GITHUB_OUTPUT: outputFile },
      exchange: json(200, { key: KEY, key_id: 3 }),
    });
    delete h.deps.appendOutput;

    assert.equal(await run(h.deps), 0);
    assert.equal(fs.readFileSync(outputFile, 'utf8'), `key=${KEY}\nkey_id=3\n`);
  });
});

describe('run: pending (202)', () => {
  it('fails with the request id and the awaits-approval text, issuing no key', async () => {
    const h = makeHarness({ exchange: json(202, { status: 'pending', request_id: 42 }) });

    const code = await run(h.deps);

    assert.equal(code, 1);
    assert.deepEqual(h.outputs, []);
    const errorLine = h.stdout.find((line) => line.startsWith('::error::'));
    assert.match(errorLine, /identity request #42/);
    assert.match(errorLine, /awaits operator approval in the admin UI/);
  });

  it('still fails clearly when the 202 body is not JSON', async () => {
    const h = makeHarness({ exchange: new Response('accepted', { status: 202 }) });

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /::error::identity request #unknown .*awaits operator approval/);
  });

  it('prints only an integer request id, never an arbitrary server value', async () => {
    for (const requestId of ['7; rm -rf', { nested: 1 }, 1.5, null]) {
      const h = makeHarness({ exchange: json(202, { request_id: requestId }) });

      assert.equal(await run(h.deps), 1);
      assert.match(h.text(), /::error::identity request #unknown /);
    }
  });
});

describe('run: server refusals', () => {
  for (const [status, message] of [
    [401, 'invalid identity token'],
    [403, 'identity does not match the approved binding'],
    [429, 'too many pending identity requests'],
    [400, 'invalid app_id'],
  ]) {
    it(`fails with the status and the server's error text for ${status}`, async () => {
      const h = makeHarness({ exchange: json(status, { error: message }) });

      const code = await run(h.deps);

      assert.equal(code, 1);
      assert.deepEqual(h.outputs, []);
      assert.ok(h.text().includes(`::error::identity exchange returned ${status}: ${message}`));
    });
  }

  it('falls back to a bounded raw body when the error is not JSON', async () => {
    const h = makeHarness({ exchange: new Response('x'.repeat(500), { status: 502 }) });

    assert.equal(await run(h.deps), 1);
    const errorLine = h.stdout.find((line) => line.startsWith('::error::'));
    assert.match(errorLine, /returned 502: x{200}\n$/);
  });

  it('never prints the OIDC token in an error', async () => {
    const h = makeHarness({ exchange: json(401, { error: 'invalid identity token' }) });

    await run(h.deps);

    assert.ok(!unmaskedText(h).includes(OIDC_JWT));
    assert.ok(!unmaskedText(h).includes(ACCESS_SECRET));
  });

  it('rejects a 200 without a well-formed key, without masking or writing it', async () => {
    const h = makeHarness({ exchange: json(200, { key: 'not-a-key\nkey_id=9', key_id: 1 }) });

    assert.equal(await run(h.deps), 1);
    assert.deepEqual(h.outputs, []);
    assert.ok(!h.text().includes('not-a-key'));
    assert.match(h.text(), /::error::identity exchange returned 200 without a valid app key/);
  });
});

describe('run: redirects are never followed', () => {
  it('fails on a 302 from the exchange with one request, naming the status', async () => {
    const h = makeHarness({
      exchange: new Response(null, {
        status: 302,
        headers: { Location: 'https://elsewhere.example/login' },
      }),
    });

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /::error::identity exchange returned 302: /);
    assert.deepEqual(h.outputs, []);
    const exchangeCalls = h.calls.filter((call) => call.url.endsWith(EXCHANGE_PATH));
    assert.equal(exchangeCalls.length, 1, 'the redirect target must never be requested');
    assert.equal(h.calls.length, 2);
  });

  it('asks fetch not to follow redirects on both the OIDC and the exchange request', async () => {
    const h = makeHarness({ exchange: json(200, { key: KEY, key_id: 1 }) });

    await run(h.deps);

    assert.equal(h.calls.length, 2);
    for (const call of h.calls) assert.equal(call.init.redirect, 'manual', call.url);
  });
});

describe('run: timeouts', () => {
  it('fails a hanging exchange at the timeout', async () => {
    const h = makeHarness({ exchange: hangUntilAborted });
    h.deps.timeoutMs = 25;

    const code = await run(h.deps);

    assert.equal(code, 1);
    assert.match(h.text(), /::error::identity exchange timed out after/);
    assert.deepEqual(h.outputs, []);
  });

  it('fails a hanging OIDC token request at the timeout, before any exchange', async () => {
    const h = makeHarness({ oidc: hangUntilAborted });
    h.deps.timeoutMs = 25;

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /::error::OIDC token request timed out after/);
    assert.equal(h.calls.length, 1);
  });

  it('requests a 10 s signal for both calls when no timeout is injected', async (t) => {
    const requested = [];
    t.mock.method(AbortSignal, 'timeout', (ms) => {
      requested.push(ms);
      return new AbortController().signal;
    });
    const h = makeHarness({ exchange: json(200, { key: KEY, key_id: 1 }) });

    assert.equal(await run(h.deps), 0);
    assert.deepEqual(requested, [10000, 10000]);
  });

  it('reports the default timeout as "after 10s" for each call', async (t) => {
    t.mock.method(AbortSignal, 'timeout', () =>
      AbortSignal.abort(
        new DOMException('The operation was aborted due to timeout', 'TimeoutError'),
      ),
    );

    // The mocked signals are already aborted: a fake that honours the signal fails at once, and
    // one that ignores it (the default OIDC answer) lets the run reach the next call.
    const oidcHang = makeHarness({ oidc: rejectWithAbortReason });
    assert.equal(await run(oidcHang.deps), 1);
    assert.match(oidcHang.text(), /::error::OIDC token request timed out after 10s/);

    const exchangeHang = makeHarness({ exchange: rejectWithAbortReason });
    assert.equal(await run(exchangeHang.deps), 1);
    assert.match(exchangeHang.text(), /::error::identity exchange timed out after 10s/);
  });

  for (const [label, status, field] of [
    ['a 200 body', 200, 'exchange'],
    ['a 202 body', 202, 'exchange'],
    ['a non-2xx error body', 500, 'exchange'],
    ['the OIDC token body', 200, 'oidc'],
  ]) {
    it(`reports a timeout while reading ${label} as a timeout, not a bad body`, async () => {
      const h = makeHarness({ [field]: () => bodyTimesOut(status) });
      h.deps.timeoutMs = 3000;

      assert.equal(await run(h.deps), 1);
      const expected =
        field === 'oidc' ? 'OIDC token request timed out' : 'identity exchange timed out';
      assert.match(h.text(), new RegExp(`::error::${expected} after 3s`));
      assert.ok(!/non-JSON|unknown|unreadable/.test(h.text()), h.text());
    });
  }

  it('reports a network failure by message only', async () => {
    const h = makeHarness({
      exchange: () => {
        throw new TypeError('fetch failed');
      },
    });

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /::error::identity exchange failed: fetch failed/);
  });

  it('never echoes a header value quoted by a TypeError', async () => {
    const secret = 'topsecretvalue\nsecond-line';
    const h = makeHarness({
      env: { NOTIFS_ACCESS_CLIENT_SECRET: secret },
      exchange: () => {
        throw new TypeError(`Headers.append: "${secret}" is an invalid header value.`);
      },
    });

    assert.equal(await run(h.deps), 1);
    assert.match(
      h.text(),
      /::error::identity exchange failed: request could not be built \(invalid header value or URL\)/,
    );
    assert.ok(!unmaskedText(h).includes('topsecretvalue'));
    assert.ok(!unmaskedText(h).includes('second-line'));
  });
});

describe('run: configuration and OIDC failures', () => {
  it('tells the caller to grant id-token: write when the runner gives no OIDC URL', async () => {
    const h = makeHarness({ env: { ACTIONS_ID_TOKEN_REQUEST_URL: '' } });

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /permissions: id-token: write/);
    assert.equal(h.calls.length, 0);
  });

  it('fails when the OIDC request is refused', async () => {
    const h = makeHarness({ oidc: () => json(403, { message: 'nope' }) });

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /::error::OIDC token request returned 403/);
  });

  it('fails when the OIDC response has no token', async () => {
    const h = makeHarness({ oidc: () => json(200, {}) });

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /::error::OIDC token request returned no token/);
  });

  it('refuses a missing or malformed app id before any network call', async () => {
    for (const NOTIFS_APP_ID of ['', 'Bad App']) {
      const h = makeHarness({ env: { NOTIFS_APP_ID } });
      assert.equal(await run(h.deps), 1);
      assert.match(h.text(), /NOTIFS_APP_ID/);
      assert.equal(h.calls.length, 0);
    }
  });

  it('refuses a non-loopback http URL and a missing Access pair, naming the action inputs', async () => {
    const insecure = makeHarness({ env: { NOTIFS_DELIVERY_URL: 'http://delivery.example.com' } });
    assert.equal(await run(insecure.deps), 1);
    assert.match(insecure.text(), /delivery_url must be https:/);
    assert.equal(insecure.calls.length, 0);

    const noAccess = makeHarness({
      env: { NOTIFS_ACCESS_CLIENT_ID: '', NOTIFS_ACCESS_CLIENT_SECRET: '' },
    });
    assert.equal(await run(noAccess.deps), 1);
    assert.match(
      noAccess.text(),
      /access_client_id\/access_client_secret are required for a non-loopback delivery_url/,
    );
    assert.equal(noAccess.calls.length, 0);

    const halfAccess = makeHarness({ env: { NOTIFS_ACCESS_CLIENT_SECRET: '' } });
    assert.equal(await run(halfAccess.deps), 1);
    assert.match(
      halfAccess.text(),
      /both or neither of access_client_id\/access_client_secret must be set/,
    );
    assert.ok(!/NOTIFS_ACCESS_CLIENT|ONBOARD_URL/.test(halfAccess.text()));
  });

  it('fails when GITHUB_OUTPUT is unset and a key was issued, printing the key only as a mask', async () => {
    const h = makeHarness({
      env: { GITHUB_OUTPUT: '' },
      exchange: json(200, { key: KEY, key_id: 1 }),
    });
    delete h.deps.appendOutput;

    assert.equal(await run(h.deps), 1);
    assert.match(h.text(), /GITHUB_OUTPUT is not set/);
    assert.ok(maskIndex(h, KEY) >= 0, 'the key is masked before the write is attempted');
    assert.ok(!unmaskedText(h).includes(KEY.slice(4)), 'the key must only appear on its mask line');
  });
});

describe('run: other failure shapes (table)', () => {
  const cases = [
    {
      name: 'response.text() throws on a non-2xx',
      exchange: () => ({
        ok: false,
        status: 500,
        text: async () => {
          throw new Error('stream reset');
        },
      }),
      match: /::error::identity exchange returned 500: \(unreadable response body\)/,
    },
    {
      name: 'an empty non-2xx body',
      exchange: () => new Response('', { status: 503 }),
      match: /::error::identity exchange returned 503: \(empty response body\)/,
    },
    {
      name: 'a JSON error whose `error` is not a string',
      exchange: () => json(500, { error: { nested: true } }),
      match: /::error::identity exchange returned 500: \{"error":\{"nested":true\}\}/,
    },
    {
      name: 'a 200 with a non-JSON body',
      exchange: () => new Response('<html>login</html>', { status: 200 }),
      match: /::error::identity exchange returned 200 with a non-JSON body/,
    },
    {
      name: 'an OIDC response with a non-JSON body',
      oidc: () => new Response('<html>', { status: 200 }),
      match: /::error::OIDC token request returned a non-JSON body/,
      calls: 1,
    },
    {
      name: 'an OIDC network error',
      oidc: () => {
        throw new TypeError('fetch failed');
      },
      match: /::error::OIDC token request failed: fetch failed/,
      calls: 1,
    },
    {
      name: 'a missing runner request token with the URL present',
      env: { ACTIONS_ID_TOKEN_REQUEST_TOKEN: '' },
      match: /permissions: id-token: write/,
      calls: 0,
    },
    {
      name: 'a 204 response (fails closed)',
      exchange: () => new Response(null, { status: 204 }),
      match: /::error::identity exchange returned 204: \(empty response body\)/,
    },
  ];

  for (const { name, exchange, oidc, env, match, calls = 2 } of cases) {
    it(`fails with a clear message for ${name}`, async () => {
      const h = makeHarness({ exchange, oidc, env });

      assert.equal(await run(h.deps), 1);
      assert.match(h.text(), match);
      assert.deepEqual(h.outputs, []);
      assert.equal(h.calls.length, calls);
    });
  }

  it('leaves key_id empty, but still issues the key, when key_id is not an integer', async () => {
    const h = makeHarness({ exchange: json(200, { key: KEY, key_id: '7' }) });

    assert.equal(await run(h.deps), 0);
    assert.deepEqual(h.outputs, [`key=${KEY}\nkey_id=\n`]);
    assert.match(h.text(), /\(key id unknown\)/);
  });
});

describe('.github/actions/notifs-app/action.yml', () => {
  const githubDir = path.join(REPO_ROOT, '.github');
  const actionText = fs.readFileSync(
    path.join(githubDir, 'actions', 'notifs-app', 'action.yml'),
    'utf8',
  );

  /** The `- ` steps under `  steps:`, each with its id, name and `env:` mapping. */
  function parseSteps(text) {
    const afterSteps = text.split(/^ {2}steps:[ \t]*$/m)[1];
    assert.ok(afterSteps !== undefined, 'action.yml has no `  steps:` line');
    return afterSteps
      .split(/^(?= {4}- )/m)
      .filter((chunk) => chunk.trim() !== '')
      .map((chunk) => ({
        id: /^(?: {4}- | {6})id:\s*['"]?([\w-]+)['"]?\s*$/m.exec(chunk)?.[1],
        name: /^(?: {4}- | {6})name:\s*['"]?(.+?)['"]?\s*$/m.exec(chunk)?.[1],
        env: parseEnv(chunk),
      }));
  }

  function parseEnv(chunk) {
    const lines = chunk.split('\n');
    const start = lines.findIndex((line) => /^\s*env:\s*$/.test(line));
    if (start < 0) return {};
    const envIndent = /^\s*/.exec(lines[start])[0].length;
    const env = {};
    for (const line of lines.slice(start + 1)) {
      if (line.trim() === '') continue;
      if (/^\s*/.exec(line)[0].length <= envIndent) break;
      const match = /^\s*([A-Za-z_][A-Za-z0-9_]*):\s*(.*?)\s*$/.exec(line);
      if (match) env[match[1]] = match[2];
    }
    return env;
  }

  /** `${{ inputs.app_id }}` (any quoting/spacing) -> `inputs.app_id`; otherwise the raw value. */
  function expression(value) {
    const match = /^['"]?\$\{\{\s*(.+?)\s*\}\}['"]?$/.exec(value ?? '');
    return match ? match[1] : value;
  }

  /** Lines that contain `${{` inside a `run:` scalar, in any YAML form (block, folded, inline). */
  function runExpressionLines(text) {
    const offending = [];
    const lines = text.split('\n');
    let runIndent = null;
    for (const [i, line] of lines.entries()) {
      const indent = /^ */.exec(line)[0].length;
      if (runIndent !== null) {
        if (line.trim() === '' || indent > runIndent) {
          if (line.includes('${{')) offending.push(`${i + 1}: ${line.trim()}`);
          continue;
        }
        runIndent = null;
      }
      const match = /^( *(?:- )?)run:(.*)$/.exec(line);
      if (!match) continue;
      runIndent = match[1].length;
      if (match[2].includes('${{')) offending.push(`${i + 1}: ${line.trim()}`);
    }
    return offending;
  }

  function stepNamed(steps, pattern) {
    const step = steps.find((candidate) => pattern.test(candidate.name ?? ''));
    assert.ok(step, `no step named ${pattern}`);
    return step;
  }

  it('pins every remote `uses:` in every action and workflow to a full commit SHA', () => {
    const files = [];
    const actionsDir = path.join(githubDir, 'actions');
    for (const entry of fs.readdirSync(actionsDir, { withFileTypes: true })) {
      if (entry.isDirectory()) files.push(path.join(actionsDir, entry.name, 'action.yml'));
    }
    const workflowsDir = path.join(githubDir, 'workflows');
    for (const name of fs.readdirSync(workflowsDir)) {
      if (name.endsWith('.yml')) files.push(path.join(workflowsDir, name));
    }

    let checked = 0;
    for (const file of files) {
      const text = fs
        .readFileSync(file, 'utf8')
        .split('\n')
        .filter((line) => !/^\s*#/.test(line))
        .join('\n');
      for (const [, ref] of text.matchAll(/\buses\s*:\s*['"]?([^\s,'"}]+)/g)) {
        // `./.github/workflows/x.yml` reusable-workflow calls are local to the same commit.
        if (ref.startsWith('./')) continue;
        checked += 1;
        assert.match(
          ref,
          /^[^@\s]+@[0-9a-f]{40}$/,
          `${path.basename(file)}: ${ref} is not SHA-pinned`,
        );
      }
    }
    assert.ok(checked > 0);
  });

  it('declares the audience input defaulting to notifs-delivery and threads it to the exchange step', () => {
    assert.match(
      actionText,
      /^ {2}audience:[ \t]*\n(?: {4}.*\n)*? {4}default:\s*['"]?notifs-delivery['"]?\s*$/m,
    );
    const exchange = parseSteps(actionText).find((step) => step.id === 'exchange');
    assert.ok(exchange, 'no step with id: exchange');
    assert.equal(expression(exchange.env.NOTIFS_AUDIENCE), 'inputs.audience');
  });

  it('maps every input the exchange script reads from the id: exchange step', () => {
    const exchange = parseSteps(actionText).find((step) => step.id === 'exchange');
    assert.ok(exchange, 'no step with id: exchange');

    assert.deepEqual(
      Object.fromEntries(Object.entries(exchange.env).map(([k, v]) => [k, expression(v)])),
      {
        NOTIFS_APP_ID: 'inputs.app_id',
        NOTIFS_DELIVERY_URL: 'inputs.delivery_url',
        NOTIFS_ACCESS_CLIENT_ID: 'inputs.access_client_id',
        NOTIFS_ACCESS_CLIENT_SECRET: 'inputs.access_client_secret',
        NOTIFS_AUDIENCE: 'inputs.audience',
      },
    );
  });

  it('maps the onboard.mjs environment in the Apply and Plan steps', () => {
    const steps = parseSteps(actionText);
    for (const pattern of [/^Apply the manifest/, /^Plan the manifest/]) {
      const { env } = stepNamed(steps, pattern);
      assert.equal(expression(env.ONBOARD_URL), 'inputs.delivery_url', `${pattern}`);
      assert.equal(expression(env.ONBOARD_APP_ACCESS_ID), 'inputs.access_client_id', `${pattern}`);
      assert.equal(
        expression(env.ONBOARD_APP_ACCESS_SECRET),
        'inputs.access_client_secret',
        `${pattern}`,
      );
      assert.equal(expression(env.ONBOARD_APP_KEY), 'steps.exchange.outputs.key', `${pattern}`);
    }
  });

  it('runs the scripts relative to $GITHUB_ACTION_PATH and installs no npm packages', () => {
    assert.match(
      actionText,
      /\$GITHUB_ACTION_PATH\/\.\.\/\.\.\/\.\.\/scripts\/notifs-app-exchange\.mjs/,
    );
    assert.match(actionText, /\$GITHUB_ACTION_PATH\/\.\.\/\.\.\/\.\.\/scripts\/onboard\.mjs/);
    assert.ok(
      !/\b(npm|npx|yarn|pnpm)\b/.test(actionText),
      'the action must not use a package manager',
    );
  });

  it('only imports node: builtins and relative files in the scripts the action runs', () => {
    const scripts = [
      'notifs-app-exchange.mjs',
      'onboard.mjs',
      'onboard-client.mjs',
      'onboard-manifest.mjs',
      'onboard-plan.mjs',
      'ports.mjs',
    ];
    for (const script of scripts) {
      const text = fs.readFileSync(path.join(REPO_ROOT, 'scripts', script), 'utf8');
      const specifiers = [
        ...[...text.matchAll(/^\s*(?:import|export)\b[^'"`;]*?\bfrom\s*['"]([^'"]+)['"]/gms)].map(
          (m) => m[1],
        ),
        ...[...text.matchAll(/^\s*import\s*['"]([^'"]+)['"]/gm)].map((m) => m[1]),
        ...[...text.matchAll(/\bimport\(\s*['"]([^'"]+)['"]\s*\)/g)].map((m) => m[1]),
        ...[...text.matchAll(/\brequire\(\s*['"]([^'"]+)['"]\s*\)/g)].map((m) => m[1]),
      ];
      for (const specifier of specifiers) {
        assert.ok(
          specifier.startsWith('node:') || specifier.startsWith('./'),
          `${script} imports ${specifier}: not a node: builtin or a relative file`,
        );
      }
    }
  });

  it('never interpolates an expression directly into a run script', () => {
    assert.deepEqual(runExpressionLines(actionText), []);
  });

  it('the run-script scanner flags every YAML scalar form', () => {
    for (const source of [
      '    - run: |\n        echo ${{ inputs.x }}\n',
      '      run: |-\n        echo ${{ inputs.x }}\n',
      '      run: >-\n        echo\n        ${{ inputs.x }}\n',
      '      run: echo ${{ inputs.x }}\n',
      '      - run: echo ${{ inputs.x }}\n',
      '      run:\n        echo ${{ inputs.x }}\n',
    ]) {
      assert.equal(runExpressionLines(source).length, 1, source);
    }
    assert.deepEqual(
      runExpressionLines(
        '      run: |\n        echo "$X"\n      env:\n        X: ${{ inputs.x }}\n      if: ${{ true }}\n',
      ),
      [],
    );
  });
});

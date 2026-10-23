import assert from 'node:assert/strict';
import { describe, it } from 'node:test';

import {
  ApiError,
  createAdminApi,
  createAppApi,
  createAppScopedApi,
  createHttp,
  resolveTarget,
} from './onboard-client.mjs';

// Mirrors web/src/api/deliveryClient.test.ts's jsonResponse helper: a real `Response` so the
// production code's `response.json()` / `response.ok` / `response.status` all behave for real.
function jsonResponse({ status = 200, body }) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** A recording fake fetchImpl that replays queued responses in call order. */
function createRecordingFetch(responses) {
  const queue = [...responses];
  const calls = [];
  async function fetchImpl(url, init) {
    calls.push({
      url,
      method: init.method,
      headers: { ...init.headers },
      body: init.body,
      signal: init.signal,
    });
    const next = queue.shift();
    if (next === undefined) {
      throw new Error(`createRecordingFetch: no queued response for call ${calls.length}`);
    }
    return next;
  }
  fetchImpl.calls = calls;
  return fetchImpl;
}

describe('resolveTarget', () => {
  it('throws before any fetch call when ONBOARD_URL is not set', () => {
    assert.throws(() => resolveTarget({ env: {} }));
  });

  it('rejects a non-loopback http: URL', () => {
    assert.throws(() => resolveTarget({ env: { ONBOARD_URL: 'http://notifs-delivery.example' } }));
  });

  it('rejects a non-loopback https: URL with no credentials', () => {
    assert.throws(() => resolveTarget({ env: { ONBOARD_URL: 'https://notifs-delivery.example' } }));
  });

  it('rejects exactly one of the ID/secret pair, loopback or not', () => {
    assert.throws(() =>
      resolveTarget({
        env: { ONBOARD_URL: 'http://127.0.0.1:8091', ONBOARD_ACCESS_ID: 'id-only' },
      }),
    );
    assert.throws(() =>
      resolveTarget({
        env: {
          ONBOARD_URL: 'https://notifs-delivery.example',
          ONBOARD_ACCESS_SECRET: 'secret-only',
        },
      }),
    );
  });

  it('treats an empty-string credential the same as unset, not as "set"', () => {
    assert.throws(() =>
      resolveTarget({
        env: {
          ONBOARD_URL: 'http://127.0.0.1:8091',
          ONBOARD_ACCESS_ID: 'id-only',
          ONBOARD_ACCESS_SECRET: '',
        },
      }),
    );
    const target = resolveTarget({
      env: {
        ONBOARD_URL: 'http://127.0.0.1:8091',
        ONBOARD_ACCESS_ID: '',
        ONBOARD_ACCESS_SECRET: '',
      },
    });
    assert.equal(target.credentials, null);
  });

  it('accepts a loopback URL with no credentials', () => {
    const target = resolveTarget({ env: { ONBOARD_URL: 'http://127.0.0.1:8091' } });
    assert.equal(target.baseUrl, 'http://127.0.0.1:8091');
    assert.equal(target.credentials, null);
  });

  it('accepts loopback hosts (localhost, [::1]) with both credentials set', () => {
    const target = resolveTarget({
      env: {
        ONBOARD_URL: 'http://localhost:8091',
        ONBOARD_ACCESS_ID: 'id-value',
        ONBOARD_ACCESS_SECRET: 'secret-value',
      },
    });
    assert.deepEqual(target.credentials, { id: 'id-value', secret: 'secret-value' });

    const ipv6Target = resolveTarget({ env: { ONBOARD_URL: 'http://[::1]:8091' } });
    assert.equal(ipv6Target.credentials, null);
  });

  it('accepts a non-loopback https: URL with both credentials', () => {
    const target = resolveTarget({
      env: {
        ONBOARD_URL: 'https://notifs-delivery.example',
        ONBOARD_ACCESS_ID: 'id-value',
        ONBOARD_ACCESS_SECRET: 'secret-value',
      },
    });
    assert.equal(target.baseUrl, 'https://notifs-delivery.example');
    assert.deepEqual(target.credentials, { id: 'id-value', secret: 'secret-value' });
  });

  it('reads the app-token env var pair when idVar/secretVar are overridden (the verify seam)', () => {
    const target = resolveTarget({
      env: {
        ONBOARD_URL: 'https://notifs-delivery.example',
        ONBOARD_APP_ACCESS_ID: 'app-id',
        ONBOARD_APP_ACCESS_SECRET: 'app-secret',
      },
      idVar: 'ONBOARD_APP_ACCESS_ID',
      secretVar: 'ONBOARD_APP_ACCESS_SECRET',
    });
    assert.deepEqual(target.credentials, { id: 'app-id', secret: 'app-secret' });
  });
});

describe('createHttp headers', () => {
  it('sends no CF-Access-* headers for a loopback target with no credentials', async () => {
    const fetchImpl = createRecordingFetch([jsonResponse({ body: { destinations: [] } })]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    await http.request({ method: 'GET', path: '/v1/admin/destinations' });
    const [call] = fetchImpl.calls;
    assert.equal(call.headers['CF-Access-Client-Id'], undefined);
    assert.equal(call.headers['CF-Access-Client-Secret'], undefined);
    assert.equal(call.headers.Accept, 'application/json');
  });

  it('sends both CF-Access-* headers on every call once credentials are set', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ body: { destinations: [] } }),
      jsonResponse({ body: { routes: [] } }),
    ]);
    const http = createHttp({
      baseUrl: 'http://127.0.0.1:8091',
      credentials: { id: 'id-1', secret: 'secret-1' },
      fetchImpl,
    });
    await http.request({ method: 'GET', path: '/v1/admin/destinations' });
    await http.request({ method: 'GET', path: '/v1/admin/routes' });
    assert.equal(fetchImpl.calls.length, 2);
    for (const call of fetchImpl.calls) {
      assert.equal(call.headers['CF-Access-Client-Id'], 'id-1');
      assert.equal(call.headers['CF-Access-Client-Secret'], 'secret-1');
    }
  });

  it('sends Authorization: Bearer <key> alongside the CF-Access-* headers when an app key is given', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ body: { state: 'up' } }),
      jsonResponse({ body: { state: 'up' } }),
    ]);
    const http = createHttp({
      baseUrl: 'https://notifs.example.test',
      credentials: { id: 'id-1', secret: 'secret-1' },
      appKey: 'dak_test-app-key',
      fetchImpl,
    });
    await http.request({ method: 'POST', path: '/v1/heartbeats/myapp/nightly' });
    await http.request({ method: 'POST', path: '/v1/heartbeats/myapp/nightly' });
    for (const call of fetchImpl.calls) {
      assert.equal(call.headers.Authorization, 'Bearer dak_test-app-key');
      assert.equal(call.headers['CF-Access-Client-Id'], 'id-1');
      assert.equal(call.headers['CF-Access-Client-Secret'], 'secret-1');
    }
  });

  it('sends no Authorization header when no app key is given', async () => {
    const fetchImpl = createRecordingFetch([jsonResponse({ body: { destinations: [] } })]);
    const http = createHttp({
      baseUrl: 'http://127.0.0.1:8091',
      credentials: { id: 'id-1', secret: 'secret-1' },
      fetchImpl,
    });
    await http.request({ method: 'GET', path: '/v1/admin/destinations' });
    assert.equal(fetchImpl.calls[0].headers.Authorization, undefined);
  });

  it('sends Content-Type: application/json only when the request has a body', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ body: { destinations: [] } }),
      jsonResponse({ status: 201, body: { destination: { name: 'app_ops' } } }),
    ]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    await http.request({ method: 'GET', path: '/v1/admin/destinations' });
    await http.request({
      method: 'POST',
      path: '/v1/admin/destinations',
      body: { name: 'app_ops' },
    });
    assert.equal(fetchImpl.calls[0].headers['Content-Type'], undefined);
    assert.equal(fetchImpl.calls[1].headers['Content-Type'], 'application/json');
  });
});

describe('createHttp error handling', () => {
  it('wraps a 409 JSON error body into an ApiError carrying status, method and path', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ status: 409, body: { error: 'route already exists' } }),
    ]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    await assert.rejects(
      http.request({ method: 'POST', path: '/v1/admin/routes', body: {} }),
      (err) => {
        assert.ok(err instanceof ApiError);
        assert.equal(err.status, 409);
        assert.equal(err.method, 'POST');
        assert.equal(err.path, '/v1/admin/routes');
        assert.equal(err.message, 'route already exists');
        return true;
      },
    );
  });

  it('falls back to "HTTP <status>" for a non-JSON error body', async () => {
    const fetchImpl = createRecordingFetch([new Response('not json', { status: 502 })]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    await assert.rejects(http.request({ method: 'GET', path: '/v1/admin/destinations' }), (err) => {
      assert.equal(err.message, 'HTTP 502');
      return true;
    });
  });

  it('never leaks a credential secret value into an ApiError message or toString()', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ status: 500, body: { error: 'boom' } }),
    ]);
    const http = createHttp({
      baseUrl: 'https://notifs-delivery.example',
      credentials: { id: 'id-value', secret: 'super-secret-value' },
      fetchImpl,
    });
    await assert.rejects(http.request({ method: 'GET', path: '/v1/admin/destinations' }), (err) => {
      assert.ok(!err.message.includes('super-secret-value'));
      assert.ok(!String(err).includes('super-secret-value'));
      return true;
    });
  });

  it('rejects a request exceeding timeoutMs with an error naming the method and path', async () => {
    const fetchImpl = (_url, init) =>
      new Promise((_resolve, reject) => {
        init.signal.addEventListener('abort', () => {
          reject(new DOMException('The operation was aborted.', 'AbortError'));
        });
      });
    const http = createHttp({
      baseUrl: 'http://127.0.0.1:8091',
      credentials: null,
      fetchImpl,
      timeoutMs: 10,
    });
    await assert.rejects(http.request({ method: 'GET', path: '/v1/admin/destinations' }), (err) => {
      assert.match(err.message, /GET/);
      assert.match(err.message, /\/v1\/admin\/destinations/);
      return true;
    });
  });
});

describe('createAdminApi', () => {
  it('URL-encodes destination names in paths', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ body: { destination: { name: 'a/b', enabled: false } } }),
    ]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    const api = createAdminApi(http);
    await api.patchDestination('a/b', { enabled: false });
    assert.equal(fetchImpl.calls[0].url, 'http://127.0.0.1:8091/v1/admin/destinations/a%2Fb');
    assert.equal(fetchImpl.calls[0].method, 'PATCH');
  });

  it('passes a check PATCH body through untouched, never sending app_id/name', async () => {
    const fetchImpl = createRecordingFetch([jsonResponse({ body: { check: {} } })]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    const api = createAdminApi(http);
    const body = { period_seconds: 120, grace_seconds: 60, destinations: ['myapp_ops'] };
    await api.patchCheck(7, body);
    assert.equal(fetchImpl.calls[0].url, 'http://127.0.0.1:8091/v1/admin/heartbeats/7');
    assert.deepEqual(JSON.parse(fetchImpl.calls[0].body), body);
  });

  it('round-trips destinations/routes/checks list+create through the expected admin paths', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ body: { destinations: [{ name: 'myapp_ops' }] } }),
      jsonResponse({ status: 201, body: { destination: { name: 'myapp_ops' } } }),
      jsonResponse({ body: { routes: [] } }),
      jsonResponse({ status: 201, body: { route: { id: 1 } } }),
      jsonResponse({ body: { checks: [] } }),
      jsonResponse({ status: 201, body: { check: { id: 2 } } }),
    ]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    const api = createAdminApi(http);

    assert.deepEqual(await api.listDestinations(), [{ name: 'myapp_ops' }]);
    assert.deepEqual(await api.createDestination({ name: 'myapp_ops', type: 'discord' }), {
      name: 'myapp_ops',
    });
    assert.deepEqual(await api.listRoutes(), []);
    assert.deepEqual(await api.createRoute({ channel_pattern: 'ops' }), { id: 1 });
    assert.deepEqual(await api.listChecks(), []);
    assert.deepEqual(await api.createCheck({ name: 'nightly_backup' }), { id: 2 });

    assert.deepEqual(
      fetchImpl.calls.map((call) => `${call.method} ${call.url}`),
      [
        'GET http://127.0.0.1:8091/v1/admin/destinations',
        'POST http://127.0.0.1:8091/v1/admin/destinations',
        'GET http://127.0.0.1:8091/v1/admin/routes',
        'POST http://127.0.0.1:8091/v1/admin/routes',
        'GET http://127.0.0.1:8091/v1/admin/heartbeats',
        'POST http://127.0.0.1:8091/v1/admin/heartbeats',
      ],
    );
  });

  it('deletes destinations, routes and checks at the expected paths', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ body: { name: 'myapp_ops', deleted: true } }),
      jsonResponse({ body: { id: 1, deleted: true } }),
      jsonResponse({ body: { id: 2, deleted: true } }),
    ]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    const api = createAdminApi(http);
    await api.deleteDestination('myapp_ops');
    await api.deleteRoute(1);
    await api.deleteCheck(2);
    assert.deepEqual(
      fetchImpl.calls.map((call) => `${call.method} ${call.url}`),
      [
        'DELETE http://127.0.0.1:8091/v1/admin/destinations/myapp_ops',
        'DELETE http://127.0.0.1:8091/v1/admin/routes/1',
        'DELETE http://127.0.0.1:8091/v1/admin/heartbeats/2',
      ],
    );
  });
});

describe('createAppScopedApi', () => {
  const APP_KEY = 'dak_0000000000000000000000000000000000000000000';
  // The owner fields the server puts on every app-scoped row it returns.
  const OWNED_BY_MYAPP = { owner_kind: 'app', owner_app_id: 'myapp' };

  function appHttp(fetchImpl) {
    return createHttp({
      baseUrl: 'http://127.0.0.1:8091',
      credentials: { id: 'app-id', secret: 'app-secret' },
      appKey: APP_KEY,
      fetchImpl,
    });
  }

  it('reaches every resource under /v1/apps/{app_id}, never /v1/admin, with the bearer key and Access headers', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ body: { destinations: [{ name: 'myapp_ops', ...OWNED_BY_MYAPP }] } }),
      jsonResponse({ status: 201, body: { destination: { name: 'myapp_ops' } } }),
      jsonResponse({ body: { destination: { name: 'myapp_ops' } } }),
      jsonResponse({ body: { name: 'myapp_ops', deleted: true } }),
      jsonResponse({ body: { routes: [] } }),
      jsonResponse({ status: 201, body: { route: { id: 1 } } }),
      jsonResponse({ body: { route: { id: 1 } } }),
      jsonResponse({ body: { id: 1, deleted: true } }),
      jsonResponse({ body: { checks: [] } }),
      jsonResponse({ status: 201, body: { check: { id: 2 } } }),
      jsonResponse({ body: { check: { id: 2 } } }),
      jsonResponse({ body: { id: 2, deleted: true } }),
    ]);
    const api = createAppScopedApi(appHttp(fetchImpl), 'myapp');

    assert.deepEqual(await api.listDestinations(), [{ name: 'myapp_ops', ...OWNED_BY_MYAPP }]);
    assert.deepEqual(await api.createDestination({ name: 'myapp_ops' }), { name: 'myapp_ops' });
    assert.deepEqual(await api.patchDestination('myapp_ops', { enabled: false }), {
      name: 'myapp_ops',
    });
    await api.deleteDestination('myapp_ops');
    assert.deepEqual(await api.listRoutes(), []);
    assert.deepEqual(await api.createRoute({ channel_pattern: 'ops' }), { id: 1 });
    assert.deepEqual(await api.patchRoute(1, { enabled: false }), { id: 1 });
    await api.deleteRoute(1);
    assert.deepEqual(await api.listChecks(), []);
    assert.deepEqual(await api.createCheck({ name: 'nightly_backup' }), { id: 2 });
    assert.deepEqual(await api.patchCheck(2, { period_seconds: 120 }), { id: 2 });
    await api.deleteCheck(2);

    assert.deepEqual(
      fetchImpl.calls.map((call) => `${call.method} ${call.url}`),
      [
        'GET http://127.0.0.1:8091/v1/apps/myapp/destinations',
        'POST http://127.0.0.1:8091/v1/apps/myapp/destinations',
        'PATCH http://127.0.0.1:8091/v1/apps/myapp/destinations/myapp_ops',
        'DELETE http://127.0.0.1:8091/v1/apps/myapp/destinations/myapp_ops',
        'GET http://127.0.0.1:8091/v1/apps/myapp/routes',
        'POST http://127.0.0.1:8091/v1/apps/myapp/routes',
        'PATCH http://127.0.0.1:8091/v1/apps/myapp/routes/1',
        'DELETE http://127.0.0.1:8091/v1/apps/myapp/routes/1',
        'GET http://127.0.0.1:8091/v1/apps/myapp/heartbeats',
        'POST http://127.0.0.1:8091/v1/apps/myapp/heartbeats',
        'PATCH http://127.0.0.1:8091/v1/apps/myapp/heartbeats/2',
        'DELETE http://127.0.0.1:8091/v1/apps/myapp/heartbeats/2',
      ],
    );
    for (const call of fetchImpl.calls) {
      assert.ok(!call.url.includes('/v1/admin'), call.url);
      assert.equal(call.headers.Authorization, `Bearer ${APP_KEY}`);
      assert.equal(call.headers['CF-Access-Client-Id'], 'app-id');
      assert.equal(call.headers['CF-Access-Client-Secret'], 'app-secret');
    }
  });

  it('URL-encodes the app id and passes bodies through untouched', async () => {
    const fetchImpl = createRecordingFetch([jsonResponse({ body: { check: {} } })]);
    const api = createAppScopedApi(appHttp(fetchImpl), 'my app');
    const body = { period_seconds: 120, grace_seconds: 60, destinations: ['myapp_ops'] };
    await api.patchCheck(7, body);
    assert.equal(fetchImpl.calls[0].url, 'http://127.0.0.1:8091/v1/apps/my%20app/heartbeats/7');
    assert.deepEqual(JSON.parse(fetchImpl.calls[0].body), body);
  });

  it('rejects an empty or non-string app id before building any path', () => {
    const http = appHttp(createRecordingFetch([]));
    for (const bad of ['', undefined, null, 7]) {
      assert.throws(() => createAppScopedApi(http, bad), /non-empty app id/);
    }
  });

  it('surfaces a non-2xx as an ApiError with the server message', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ status: 409, body: { error: 'destination already exists' } }),
    ]);
    const api = createAppScopedApi(appHttp(fetchImpl), 'myapp');
    await assert.rejects(api.createDestination({ name: 'myapp_ops' }), (err) => {
      assert.ok(err instanceof ApiError);
      assert.equal(err.status, 409);
      assert.equal(err.message, 'destination already exists');
      return true;
    });
  });

  it('never surfaces the bearer key or Access secret when fetch rejects with a header TypeError', async () => {
    // The real fetch quotes the whole header value in the TypeError it throws for an invalid one.
    const fetchImpl = async () => {
      throw new TypeError(
        `Headers.append: "Bearer ${APP_KEY}" is an invalid header value. ` +
          'CF-Access-Client-Secret: app-secret',
      );
    };
    const api = createAppScopedApi(appHttp(fetchImpl), 'myapp');
    await assert.rejects(api.listDestinations(), (err) => {
      assert.match(
        err.message,
        /^GET \/v1\/apps\/myapp\/destinations failed: request could not be built/,
      );
      for (const secret of [APP_KEY, 'app-secret']) {
        assert.ok(!err.message.includes(secret), err.message);
        assert.ok(!String(err).includes(secret));
        assert.ok(!JSON.stringify(err).includes(secret));
        assert.ok(!String(err.stack).includes(secret));
      }
      return true;
    });
  });

  it('keeps the useful text of a plain network failure', async () => {
    const fetchImpl = async () => {
      throw new TypeError('fetch failed');
    };
    const api = createAppScopedApi(appHttp(fetchImpl), 'myapp');
    await assert.rejects(
      api.listDestinations(),
      /GET \/v1\/apps\/myapp\/destinations failed: fetch failed/,
    );
  });

  it('fails closed: keeps only rows owned by this app, in all three lists', async () => {
    const operatorOwned = { owner_kind: 'operator', owner_app_id: null };
    const fetchImpl = createRecordingFetch([
      jsonResponse({
        body: {
          destinations: [
            { name: 'myapp_ops', ...OWNED_BY_MYAPP },
            { name: 'other_ops', owner_kind: 'app', owner_app_id: 'other' },
            { name: 'operator_owned', ...operatorOwned },
            // An operator row mislabelled with this app's id is still not app-owned.
            { name: 'operator_with_app_id', owner_kind: 'operator', owner_app_id: 'myapp' },
            { name: 'unlabelled' },
          ],
        },
      }),
      jsonResponse({
        body: {
          routes: [
            { id: 1, ...OWNED_BY_MYAPP },
            { id: 2, owner_kind: 'app', owner_app_id: 'other' },
            { id: 3, ...operatorOwned },
          ],
        },
      }),
      jsonResponse({
        body: {
          checks: [
            { id: 4, owner_kind: 'app', owner_app_id: 'other' },
            { id: 5, ...OWNED_BY_MYAPP },
            { id: 6, ...operatorOwned },
            { id: 7 },
          ],
        },
      }),
    ]);
    const api = createAppScopedApi(appHttp(fetchImpl), 'myapp');

    assert.deepEqual(
      (await api.listDestinations()).map((row) => row.name),
      ['myapp_ops'],
    );
    assert.deepEqual(
      (await api.listRoutes()).map((row) => row.id),
      [1],
    );
    assert.deepEqual(
      (await api.listChecks()).map((row) => row.id),
      [5],
    );
  });

  it('drops an operator-owned row (null owner_app_id) so --prune can never target it', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({
        body: {
          destinations: [{ name: 'shared_ops', owner_kind: 'operator', owner_app_id: null }],
        },
      }),
    ]);
    const api = createAppScopedApi(appHttp(fetchImpl), 'myapp');

    assert.deepEqual(await api.listDestinations(), []);
  });

  it('leaves the admin API lists unfiltered', async () => {
    const rows = [{ name: 'a', owner_app_id: 'other' }, { name: 'b' }];
    const fetchImpl = createRecordingFetch([jsonResponse({ body: { destinations: rows } })]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    assert.deepEqual(await createAdminApi(http).listDestinations(), rows);
  });
});

describe('createAppApi', () => {
  it('sends the test message to /v1/messages?wait=10', async () => {
    const fetchImpl = createRecordingFetch([
      jsonResponse({ status: 202, body: { message_id: 'm1', deliveries: [] } }),
    ]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    const api = createAppApi(http);
    const body = { app_id: 'myapp', channel: 'ops' };
    const result = await api.sendMessage(body);
    assert.equal(fetchImpl.calls[0].url, 'http://127.0.0.1:8091/v1/messages?wait=10');
    assert.equal(fetchImpl.calls[0].method, 'POST');
    assert.deepEqual(JSON.parse(fetchImpl.calls[0].body), body);
    assert.deepEqual(result, { message_id: 'm1', deliveries: [] });
  });

  it('pings a heartbeat check at /v1/heartbeats/{app_id}/{name}, never /fail', async () => {
    const fetchImpl = createRecordingFetch([jsonResponse({ body: { state: 'up' } })]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    const api = createAppApi(http);
    await api.pingCheck('myapp', 'nightly_backup');
    assert.equal(
      fetchImpl.calls[0].url,
      'http://127.0.0.1:8091/v1/heartbeats/myapp/nightly_backup',
    );
    assert.equal(fetchImpl.calls[0].method, 'POST');
    assert.equal(fetchImpl.calls[0].body, undefined);
    assert.ok(!fetchImpl.calls[0].url.includes('/fail'));
  });

  it('URL-encodes the app id and check name in the ping path', async () => {
    const fetchImpl = createRecordingFetch([jsonResponse({ body: { state: 'up' } })]);
    const http = createHttp({ baseUrl: 'http://127.0.0.1:8091', credentials: null, fetchImpl });
    const api = createAppApi(http);
    await api.pingCheck('my app', 'night/backup');
    assert.equal(
      fetchImpl.calls[0].url,
      'http://127.0.0.1:8091/v1/heartbeats/my%20app/night%2Fbackup',
    );
  });
});

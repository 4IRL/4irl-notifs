import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';

import { ApiError, resolveTarget } from './onboard-client.mjs';
import { main, parseArgs, runApply, runPlan, runVerify } from './onboard.mjs';

afterEach(() => {
  process.exitCode = undefined;
});

function tmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-onboard-cli-'));
}

function writeManifest(dir, fileName, manifest) {
  fs.writeFileSync(path.join(dir, fileName), JSON.stringify(manifest));
}

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
  return { stdout, stderr };
}

/**
 * A stateful in-memory fake of the management API (`createAdminApi`) plus the app-facing API
 * (`createAppApi`), enforcing the same semantics `onboard.mjs` depends on: `409` on a duplicate
 * create key, `400` for a route/check referencing an unknown destination, `409 destination is in
 * use` on a delete still referenced by a route/check, per-key config/secrets merge on PATCH
 * (never a wholesale replace), auto-incrementing ids for routes/checks on create, and masked
 * secret values on list.
 */
function createFakeApi() {
  const destinations = new Map();
  const routes = new Map();
  const checks = new Map();
  let nextRouteId = 1;
  let nextCheckId = 1;
  const calls = [];
  const dropPatch = { destination: 0, route: 0, check: 0 };
  const sendMessageQueue = [];
  const pingCheckQueue = [];
  let pendingMidRunSeed = null;
  let failCreateDestination = null;

  function apiError(status, message) {
    return new ApiError({ status, method: 'FAKE', path: '(fake)', message });
  }

  function maskSecrets(secrets) {
    const masked = {};
    for (const key of Object.keys(secrets ?? {})) masked[key] = '****';
    return masked;
  }

  function destinationView(d) {
    return {
      name: d.name,
      type: d.type,
      enabled: d.enabled,
      config: { ...d.config },
      secrets: maskSecrets(d.secrets),
    };
  }

  function routeView(r) {
    return {
      id: r.id,
      app_pattern: r.app_pattern,
      channel_pattern: r.channel_pattern,
      destinations: [...r.destinations],
      enabled: r.enabled,
    };
  }

  function checkDestinationViews(names) {
    return names.map((name) => {
      const d = destinations.get(name);
      return { name, type: d?.type ?? 'unknown', enabled: d?.enabled ?? true };
    });
  }

  function checkView(c) {
    return {
      id: c.id,
      app_id: c.app_id,
      name: c.name,
      period_seconds: c.period_seconds,
      grace_seconds: c.grace_seconds,
      destinations: checkDestinationViews(c.destinations),
    };
  }

  function destinationInUse(name) {
    for (const route of routes.values()) {
      if (route.destinations.includes(name)) return true;
    }
    for (const check of checks.values()) {
      if (check.destinations.includes(name)) return true;
    }
    return false;
  }

  function assertDestinationsExist(names) {
    for (const name of names) {
      if (!destinations.has(name)) throw apiError(400, `unknown destination: ${name}`);
    }
  }

  const adminApi = {
    async listDestinations() {
      calls.push({ method: 'listDestinations' });
      const result = [...destinations.values()].map(destinationView);
      if (pendingMidRunSeed) {
        destinations.set(pendingMidRunSeed.name, pendingMidRunSeed.data);
        pendingMidRunSeed = null;
      }
      return result;
    },
    async createDestination(body) {
      calls.push({ method: 'createDestination', body });
      if (failCreateDestination && failCreateDestination.name === body.name) {
        const err = failCreateDestination.error;
        failCreateDestination = null;
        throw err;
      }
      if (destinations.has(body.name)) {
        throw apiError(409, `destination already exists: ${body.name}`);
      }
      destinations.set(body.name, {
        name: body.name,
        type: body.type,
        enabled: body.enabled,
        config: { ...body.config },
        secrets: { ...body.secrets },
      });
      return destinationView(destinations.get(body.name));
    },
    async patchDestination(name, body) {
      calls.push({ method: 'patchDestination', name, body });
      const existing = destinations.get(name);
      if (!existing) throw apiError(404, `destination not found: ${name}`);
      if (dropPatch.destination > 0) {
        dropPatch.destination -= 1;
        return destinationView(existing);
      }
      if (body.enabled !== undefined) existing.enabled = body.enabled;
      if (body.config !== undefined) existing.config = { ...existing.config, ...body.config };
      if (body.secrets !== undefined) existing.secrets = { ...existing.secrets, ...body.secrets };
      return destinationView(existing);
    },
    async deleteDestination(name) {
      calls.push({ method: 'deleteDestination', name });
      if (!destinations.has(name)) throw apiError(404, `destination not found: ${name}`);
      if (destinationInUse(name)) throw apiError(409, `destination is in use: ${name}`);
      destinations.delete(name);
    },

    async listRoutes() {
      calls.push({ method: 'listRoutes' });
      return [...routes.values()].map(routeView);
    },
    async createRoute(body) {
      calls.push({ method: 'createRoute', body });
      for (const route of routes.values()) {
        if (
          route.app_pattern === body.app_pattern &&
          route.channel_pattern === body.channel_pattern
        ) {
          throw apiError(409, 'route already exists');
        }
      }
      assertDestinationsExist(body.destinations);
      const id = nextRouteId;
      nextRouteId += 1;
      const route = {
        id,
        app_pattern: body.app_pattern,
        channel_pattern: body.channel_pattern,
        destinations: [...body.destinations],
        enabled: body.enabled,
      };
      routes.set(id, route);
      return routeView(route);
    },
    async patchRoute(id, body) {
      calls.push({ method: 'patchRoute', id, body });
      const existing = routes.get(id);
      if (!existing) throw apiError(404, 'route not found');
      if (dropPatch.route > 0) {
        dropPatch.route -= 1;
        return routeView(existing);
      }
      if (body.destinations !== undefined) {
        assertDestinationsExist(body.destinations);
        existing.destinations = [...body.destinations];
      }
      if (body.enabled !== undefined) existing.enabled = body.enabled;
      return routeView(existing);
    },
    async deleteRoute(id) {
      calls.push({ method: 'deleteRoute', id });
      if (!routes.has(id)) throw apiError(404, 'route not found');
      routes.delete(id);
    },

    async listChecks() {
      calls.push({ method: 'listChecks' });
      return [...checks.values()].map(checkView);
    },
    async createCheck(body) {
      calls.push({ method: 'createCheck', body });
      for (const check of checks.values()) {
        if (check.app_id === body.app_id && check.name === body.name) {
          throw apiError(409, 'check already exists');
        }
      }
      assertDestinationsExist(body.destinations);
      const id = nextCheckId;
      nextCheckId += 1;
      const check = {
        id,
        app_id: body.app_id,
        name: body.name,
        period_seconds: body.period_seconds,
        grace_seconds: body.grace_seconds,
        destinations: [...body.destinations],
      };
      checks.set(id, check);
      return checkView(check);
    },
    async patchCheck(id, body) {
      calls.push({ method: 'patchCheck', id, body });
      const existing = checks.get(id);
      if (!existing) throw apiError(404, 'check not found');
      if (dropPatch.check > 0) {
        dropPatch.check -= 1;
        return checkView(existing);
      }
      if (body.period_seconds !== undefined) existing.period_seconds = body.period_seconds;
      if (body.grace_seconds !== undefined) existing.grace_seconds = body.grace_seconds;
      if (body.destinations !== undefined) {
        assertDestinationsExist(body.destinations);
        existing.destinations = [...body.destinations];
      }
      return checkView(existing);
    },
    async deleteCheck(id) {
      calls.push({ method: 'deleteCheck', id });
      if (!checks.has(id)) throw apiError(404, 'check not found');
      checks.delete(id);
    },
  };

  const appApi = {
    async sendMessage(body) {
      calls.push({ method: 'sendMessage', body });
      if (sendMessageQueue.length > 0) {
        const next = sendMessageQueue.shift();
        if (next.throws) throw next.throws;
        return next.result;
      }
      return { message: { deliveries: [{ destination: 'dest', state: 'delivered' }] } };
    },
    async pingCheck(appId, name) {
      calls.push({ method: 'pingCheck', appId, name });
      if (pingCheckQueue.length > 0) {
        const next = pingCheckQueue.shift();
        if (next.throws) throw next.throws;
        return next.result;
      }
      return { state: 'up' };
    },
  };

  return {
    adminApi,
    appApi,
    calls,
    apiError,
    seedDestination(d) {
      destinations.set(d.name, { config: {}, secrets: {}, enabled: true, ...d });
    },
    seedRoute(r) {
      const id = r.id ?? nextRouteId;
      if (id >= nextRouteId) nextRouteId = id + 1;
      routes.set(id, { enabled: true, ...r, id });
    },
    seedCheck(c) {
      const id = c.id ?? nextCheckId;
      if (id >= nextCheckId) nextCheckId = id + 1;
      checks.set(id, { ...c, id });
    },
    seedAfterNextList(data) {
      pendingMidRunSeed = {
        name: data.name,
        data: { config: {}, secrets: {}, enabled: true, ...data },
      };
    },
    failNextCreateDestination(name, error) {
      failCreateDestination = { name, error };
    },
    dropNextDestinationPatch() {
      dropPatch.destination += 1;
    },
    queueSendMessage(entry) {
      sendMessageQueue.push(entry);
    },
    queuePingCheck(entry) {
      pingCheckQueue.push(entry);
    },
  };
}

// A syntactically valid but made-up app key: the fake app API never sends it anywhere.
const TEST_APP_KEY = 'dak_0000000000000000000000000000000000000000000';

/** Builds `main`'s `deps`: real `resolveTarget`/`createHttp` (spied, so the real fail-closed
 * rules and timeout wiring still run) but `createAdminApi`/`createAppApi` always return the
 * fake, regardless of the dummy `http` they are handed. */
function buildDeps(fake, { env = {}, now } = {}) {
  const resolveTargetCalls = [];
  const createHttpCalls = [];
  const createAppScopedApiCalls = [];
  return {
    env: { ONBOARD_URL: 'http://127.0.0.1:9999', ONBOARD_APP_KEY: TEST_APP_KEY, ...env },
    now: now ?? (() => new Date('2026-01-01T00:00:00.000Z')),
    fetchImpl: () => {
      throw new Error('onboard.mjs must never call fetch directly in tests');
    },
    resolveTarget: (opts) => {
      resolveTargetCalls.push(opts);
      return resolveTarget(opts);
    },
    createHttp: (opts) => {
      createHttpCalls.push(opts);
      return {
        request: () => {
          throw new Error('onboard.mjs must never call http.request in tests');
        },
      };
    },
    createAdminApi: () => fake.adminApi,
    // The app-scoped API has the same interface as the admin one; the fake serves both.
    createAppScopedApi: (http, appId) => {
      createAppScopedApiCalls.push({ appId });
      return fake.adminApi;
    },
    createAppApi: () => fake.appApi,
    resolveTargetCalls,
    createHttpCalls,
    createAppScopedApiCalls,
  };
}

function appManifest(overrides = {}) {
  return {
    app_id: 'myapp',
    destinations: [
      {
        name: 'myapp_ops',
        type: 'discord',
        secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
      },
    ],
    routes: [{ channel: 'ops', destinations: ['myapp_ops'] }],
    heartbeats: [
      { name: 'nightly_backup', period: '24h', grace: '1h', destinations: ['myapp_ops'] },
    ],
    ...overrides,
  };
}

describe('onboard.mjs apply: create from empty and re-run', () => {
  it('creates everything in dependency order, and a second apply makes zero write calls', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const first = await captureOutput(() => main(['apply', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(first.stdout.join('\n').includes('3 to create'));
    assert.deepEqual(
      fake.calls.map((c) => c.method).filter((m) => m.startsWith('create')),
      ['createDestination', 'createRoute', 'createCheck'],
    );

    fake.calls.length = 0;
    const second = await captureOutput(() => main(['apply', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(second.stdout.includes('No changes.'));
    const writeCalls = fake.calls.filter((c) =>
      [
        'createDestination',
        'patchDestination',
        'deleteDestination',
        'createRoute',
        'patchRoute',
        'deleteRoute',
        'createCheck',
        'patchCheck',
        'deleteCheck',
      ].includes(c.method),
    );
    assert.equal(writeCalls.length, 0);
  });
});

describe('onboard.mjs apply: 409 recovery', () => {
  it('recovers a 409 create with a PATCH carrying the full manifest secrets, independent of --resend-secrets', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    fake.seedAfterNextList({
      name: 'myapp_ops',
      type: 'discord',
      enabled: true,
      config: {},
      secrets: { webhook_url: 'stale-concurrent-value' },
    });
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    await captureOutput(() => main(['apply', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, undefined);

    const createCall = fake.calls.find((c) => c.method === 'createDestination');
    assert.ok(createCall);
    const patchCall = fake.calls.find(
      (c) => c.method === 'patchDestination' && c.name === 'myapp_ops',
    );
    assert.ok(patchCall, 'expected a recovery PATCH after the 409');
    assert.deepEqual(patchCall.body.secrets, { webhook_url: 'wh-secret-value' });
  });
});

describe('onboard.mjs blocking actions', () => {
  it('plan exits 0 and prints "! blocking"; apply on the same fixture makes zero write calls and exits 1', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    fake.seedDestination({
      name: 'myapp_ops',
      type: 'webhook',
      enabled: true,
      config: {},
      secrets: { url: 'x', signing_secret: 'y' },
    });
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const planOut = await captureOutput(() => main(['plan', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(planOut.stdout.join('\n').includes('! blocking'));

    fake.calls.length = 0;
    const applyOut = await captureOutput(() => main(['apply', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, 1);
    assert.ok(applyOut.stdout.join('\n').includes('! blocking'));
    const writeCalls = fake.calls.filter(
      (c) =>
        c.method.startsWith('create') ||
        c.method.startsWith('patch') ||
        c.method.startsWith('delete'),
    );
    assert.equal(writeCalls.length, 0);
  });

  it('exits 1 for a missing secret env var, before any request', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: {} });
    await assert.rejects(
      main(['plan', path.join(dir, 'myapp.json')], deps),
      /missing secret env var/,
    );
    assert.equal(fake.calls.length, 0);
  });

  it('exits 1 when verify targets the platform manifest', async () => {
    const dir = tmpDir();
    writeManifest(dir, '_platform.json', { platform: true, destinations: [], routes: [] });
    const fake = createFakeApi();
    const deps = buildDeps(fake, {});
    const out = await captureOutput(() => main(['verify', path.join(dir, '_platform.json')], deps));
    assert.equal(process.exitCode, 1);
    assert.ok(out.stderr.join('\n').includes('verify needs an app manifest'));
    assert.equal(fake.calls.length, 0);
  });
});

describe('onboard.mjs invalid manifest', () => {
  it('exits 1 for an invalid manifest before any request, for plan, apply and verify', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', { app_id: 'myapp', bogus_key: true });
    const fake = createFakeApi();
    const deps = buildDeps(fake, {});
    for (const command of ['plan', 'apply', 'verify']) {
      fake.calls.length = 0;
      await assert.rejects(main([command, path.join(dir, 'myapp.json')], deps), /invalid manifest/);
      assert.equal(fake.calls.length, 0);
    }
  });
});

describe('onboard.mjs apply --prune', () => {
  it('deletes an owned check, route and destination in that order, touches nothing owned by another app, and never hits "destination is in use"', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', {
      app_id: 'myapp',
      destinations: [],
      routes: [],
      heartbeats: [],
    });
    writeManifest(dir, 'other.json', {
      app_id: 'other',
      destinations: [],
      routes: [],
      heartbeats: [],
    });
    const fake = createFakeApi();
    fake.seedDestination({
      name: 'myapp_ops',
      type: 'discord',
      enabled: true,
      config: {},
      secrets: {},
    });
    fake.seedDestination({
      name: 'other_ops',
      type: 'discord',
      enabled: true,
      config: {},
      secrets: {},
    });
    fake.seedRoute({
      app_pattern: 'myapp',
      channel_pattern: 'ops',
      destinations: ['myapp_ops'],
      enabled: true,
    });
    fake.seedCheck({
      app_id: 'myapp',
      name: 'nightly_backup',
      period_seconds: 86400,
      grace_seconds: 3600,
      destinations: ['myapp_ops'],
    });
    const deps = buildDeps(fake, {});

    await captureOutput(() => main(['apply', path.join(dir, 'myapp.json'), '--prune'], deps));
    assert.equal(process.exitCode, undefined);

    const deleteOrder = fake.calls
      .filter((c) => c.method.startsWith('delete'))
      .map((c) => c.method);
    assert.deepEqual(deleteOrder, ['deleteCheck', 'deleteRoute', 'deleteDestination']);
    const deletedDestinations = fake.calls
      .filter((c) => c.method === 'deleteDestination')
      .map((c) => c.name);
    assert.deepEqual(deletedDestinations, ['myapp_ops']);
  });

  it('refuses on an app_id prefix collision before any listing or request, but plans normally without --prune', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'my.json', { app_id: 'my', destinations: [], routes: [], heartbeats: [] });
    writeManifest(dir, 'my_app.json', {
      app_id: 'my_app',
      destinations: [],
      routes: [],
      heartbeats: [],
    });
    const fake = createFakeApi();
    const deps = buildDeps(fake, {});

    const pruneOut = await captureOutput(() =>
      main(['plan', path.join(dir, 'my.json'), '--prune'], deps),
    );
    assert.equal(process.exitCode, 1);
    assert.ok(pruneOut.stdout.join('\n').includes('collide'));
    assert.equal(fake.calls.length, 0);

    process.exitCode = undefined;
    const normalOut = await captureOutput(() => main(['plan', path.join(dir, 'my.json')], deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(normalOut.stdout.join('\n').includes('No changes.'));
  });
});

describe('onboard.mjs knownAppIds wiring', () => {
  it('builds knownAppIds from every sibling manifest, so a longer-prefix app id is never touched', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'my.json', { app_id: 'my', destinations: [], routes: [], heartbeats: [] });
    writeManifest(dir, 'my_app.json', {
      app_id: 'my_app',
      destinations: [],
      routes: [],
      heartbeats: [],
    });
    const fake = createFakeApi();
    fake.seedDestination({
      name: 'my_app_ops',
      type: 'discord',
      enabled: true,
      config: {},
      secrets: {},
    });
    const deps = buildDeps(fake, {});

    const out = await captureOutput(() => main(['plan', path.join(dir, 'my.json')], deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(!out.stdout.join('\n').includes('my_app_ops'));
    assert.ok(out.stdout.join('\n').includes('No changes.'));
  });

  it('excludes a sibling manifest that fails to parse from knownAppIds with no error; the Prune lint gate still catches it', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    fs.writeFileSync(path.join(dir, 'broken.json'), '{ not valid json');
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const out = await captureOutput(() => main(['plan', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(out.stdout.join('\n').includes('3 to create'));

    fake.calls.length = 0;
    const pruneOut = await captureOutput(() =>
      main(['plan', path.join(dir, 'myapp.json'), '--prune'], deps),
    );
    assert.equal(process.exitCode, 1);
    assert.equal(fake.calls.length, 0);
  });
});

describe('onboard.mjs apply failure handling', () => {
  function multiDestinationManifest() {
    return {
      app_id: 'myapp',
      destinations: [
        { name: 'myapp_a', type: 'discord', secrets: { webhook_url: { env: 'MYAPP_A_WEBHOOK' } } },
        { name: 'myapp_b', type: 'discord', secrets: { webhook_url: { env: 'MYAPP_B_WEBHOOK' } } },
        { name: 'myapp_c', type: 'discord', secrets: { webhook_url: { env: 'MYAPP_C_WEBHOOK' } } },
      ],
      routes: [],
      heartbeats: [],
    };
  }

  it('stops at a mid-run ApiError and reports completed/failed/not-attempted counts', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', multiDestinationManifest());
    const fake = createFakeApi();
    fake.failNextCreateDestination('myapp_b', fake.apiError(500, 'boom'));
    const deps = buildDeps(fake, {
      env: { MYAPP_A_WEBHOOK: 'a', MYAPP_B_WEBHOOK: 'b', MYAPP_C_WEBHOOK: 'c' },
    });

    const out = await captureOutput(() => main(['apply', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, 1);
    const stderr = out.stderr.join('\n');
    assert.ok(stderr.includes('failed: create-destination myapp_b: 500 boom'));
    assert.ok(stderr.includes('completed: 1, failed: 1, not attempted: 1'));
  });

  it('exits 1 with "apply did not converge" when a PATCH is silently dropped', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    fake.seedDestination({
      name: 'myapp_ops',
      type: 'discord',
      enabled: false,
      config: {},
      secrets: { webhook_url: 'wh-secret-value' },
    });
    fake.seedRoute({
      app_pattern: 'myapp',
      channel_pattern: 'ops',
      destinations: ['myapp_ops'],
      enabled: true,
    });
    fake.seedCheck({
      app_id: 'myapp',
      name: 'nightly_backup',
      period_seconds: 86400,
      grace_seconds: 3600,
      destinations: ['myapp_ops'],
    });
    fake.dropNextDestinationPatch();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const out = await captureOutput(() => main(['apply', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, 1);
    assert.ok(out.stderr.join('\n').includes('apply did not converge'));
  });
});

describe('onboard.mjs verify', () => {
  it('passes a channel only when every delivery is delivered, fails a dead delivery, and skips a prefix route', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', {
      app_id: 'myapp',
      destinations: [
        {
          name: 'myapp_ops',
          type: 'discord',
          secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
        },
      ],
      routes: [
        { channel: 'ops', destinations: ['myapp_ops'] },
        { channel: 'alerts*', destinations: ['myapp_ops'] },
      ],
      heartbeats: [],
    });
    const fake = createFakeApi();
    fake.queueSendMessage({
      result: { message: { deliveries: [{ destination: 'myapp_ops', state: 'delivered' }] } },
    });
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const out = await captureOutput(() => main(['verify', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, undefined);
    const stdout = out.stdout.join('\n');
    assert.ok(stdout.includes('ops: pass'));
    assert.ok(stdout.includes('alerts*: skipped (pattern route)'));
  });

  it('fails a channel with a dead delivery result', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', {
      app_id: 'myapp',
      destinations: [
        {
          name: 'myapp_ops',
          type: 'discord',
          secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
        },
      ],
      routes: [{ channel: 'ops', destinations: ['myapp_ops'] }],
      heartbeats: [],
    });
    const fake = createFakeApi();
    fake.queueSendMessage({
      result: {
        message: {
          deliveries: [
            { destination: 'myapp_ops', state: 'dead', dead_reason: 'boom', last_status_code: 500 },
          ],
        },
      },
    });
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const out = await captureOutput(() => main(['verify', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, 1);
    assert.ok(
      out.stdout
        .join('\n')
        .includes('ops: fail: myapp_ops state=dead dead_reason=boom last_status_code=500'),
    );
  });

  it('fails a heartbeat 404 and warns with the correct down-at time only for a pinged heartbeat', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', {
      app_id: 'myapp',
      destinations: [
        {
          name: 'myapp_ops',
          type: 'discord',
          secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
        },
      ],
      routes: [],
      heartbeats: [
        { name: 'good_check', period: '24h', grace: '1h', destinations: ['myapp_ops'] },
        { name: 'bad_check', period: '1h', grace: '5m', destinations: ['myapp_ops'] },
      ],
    });
    const fake = createFakeApi();
    fake.queuePingCheck({ result: { state: 'up' } });
    fake.queuePingCheck({ throws: fake.apiError(404, 'not found') });
    const fixedNow = new Date('2026-01-01T00:00:00.000Z');
    const deps = buildDeps(fake, {
      env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' },
      now: () => fixedNow,
    });

    const out = await captureOutput(() => main(['verify', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, 1);
    const stdout = out.stdout.join('\n');
    assert.ok(stdout.includes('good_check: pass'));
    assert.ok(stdout.includes('bad_check: failed: 404 not found'));
    const expectedDownAt = new Date(fixedNow.getTime() + (86400 + 3600) * 1000).toISOString();
    assert.ok(
      stdout.includes(
        `warning: pinged good_check; it goes down at ${expectedDownAt} unless myapp pings it`,
      ),
    );
    assert.ok(!stdout.includes('warning: pinged bad_check'));
  });

  it('does not stop after an ApiError on the first of two channels, and still reports the second', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', {
      app_id: 'myapp',
      destinations: [
        {
          name: 'myapp_ops',
          type: 'discord',
          secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
        },
      ],
      routes: [
        { channel: 'ops', destinations: ['myapp_ops'] },
        { channel: 'alerts', destinations: ['myapp_ops'] },
      ],
      heartbeats: [],
    });
    const fake = createFakeApi();
    fake.queueSendMessage({ throws: fake.apiError(500, 'boom') });
    fake.queueSendMessage({
      result: { message: { deliveries: [{ destination: 'myapp_ops', state: 'delivered' }] } },
    });
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const out = await captureOutput(() => main(['verify', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, 1);
    const stdout = out.stdout.join('\n');
    assert.ok(stdout.includes('ops: failed: 500 boom'));
    assert.ok(stdout.includes('alerts: pass'));
  });
});

describe('onboard.mjs resolveManifest notices in plan/apply/verify output', () => {
  it('plan prints "env <name>: no overrides" exactly once for an unmatched --env, and not at all once the env has a matching block', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const unmatched = await captureOutput(() =>
      main(['plan', path.join(dir, 'myapp.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.equal(unmatched.stdout.filter((line) => line === 'env staging: no overrides').length, 1);
    assert.ok(!unmatched.stdout.join('\n').includes('wh-secret-value'));

    writeManifest(
      dir,
      'myapp.json',
      appManifest({
        environments: { staging: { heartbeats: { nightly_backup: { period: '12h' } } } },
      }),
    );
    const matched = await captureOutput(() =>
      main(['plan', path.join(dir, 'myapp.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.ok(!matched.stdout.join('\n').includes('no overrides'));
  });

  it('plan and apply never print a notice for a platform manifest, even with a matching --env flag', async () => {
    const dir = tmpDir();
    writeManifest(dir, '_platform.json', { platform: true, destinations: [], routes: [] });
    const fake = createFakeApi();
    const deps = buildDeps(fake, {});

    const planOut = await captureOutput(() =>
      main(['plan', path.join(dir, '_platform.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.ok(!planOut.stdout.join('\n').includes('no overrides'));

    const applyOut = await captureOutput(() =>
      main(['apply', path.join(dir, '_platform.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.ok(!applyOut.stdout.join('\n').includes('no overrides'));
  });

  it('apply prints "env <name>: no overrides" exactly once for an unmatched --env, and not at all once the env has a matching block', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const unmatched = await captureOutput(() =>
      main(['apply', path.join(dir, 'myapp.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.equal(unmatched.stdout.filter((line) => line === 'env staging: no overrides').length, 1);
    assert.ok(!unmatched.stdout.join('\n').includes('wh-secret-value'));

    writeManifest(
      dir,
      'myapp.json',
      appManifest({
        environments: { staging: { heartbeats: { nightly_backup: { period: '12h' } } } },
      }),
    );
    const matched = await captureOutput(() =>
      main(['apply', path.join(dir, 'myapp.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.ok(!matched.stdout.join('\n').includes('no overrides'));
  });

  it('verify prints "env <name>: no overrides" exactly once for an unmatched --env, and not at all once the env has a matching block', async () => {
    const dir = tmpDir();
    const baseManifest = {
      app_id: 'myapp',
      destinations: [
        {
          name: 'myapp_ops',
          type: 'discord',
          secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
        },
      ],
      routes: [{ channel: 'ops', destinations: ['myapp_ops'] }],
      heartbeats: [],
    };
    writeManifest(dir, 'myapp.json', baseManifest);
    const fake = createFakeApi();
    fake.queueSendMessage({
      result: { message: { deliveries: [{ destination: 'myapp_ops', state: 'delivered' }] } },
    });
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    const unmatched = await captureOutput(() =>
      main(['verify', path.join(dir, 'myapp.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.equal(unmatched.stdout.filter((line) => line === 'env staging: no overrides').length, 1);
    assert.ok(!unmatched.stdout.join('\n').includes('wh-secret-value'));

    writeManifest(dir, 'myapp.json', { ...baseManifest, environments: { staging: {} } });
    fake.queueSendMessage({
      result: { message: { deliveries: [{ destination: 'myapp_ops', state: 'delivered' }] } },
    });
    const matched = await captureOutput(() =>
      main(['verify', path.join(dir, 'myapp.json'), '--env', 'staging'], deps),
    );
    assert.equal(process.exitCode, undefined);
    assert.ok(!matched.stdout.join('\n').includes('no overrides'));
  });
});

describe('onboard.mjs manual steps', () => {
  it('names the env vars and the app id, and never prints a resolved secret value', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', {
      app_id: 'myapp',
      destinations: [
        {
          name: 'myapp_ops',
          type: 'discord',
          secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
        },
        {
          name: 'myapp_hook',
          type: 'webhook',
          secrets: { url: { env: 'MYAPP_HOOK_URL' }, signing_secret: { env: 'MYAPP_HOOK_SECRET' } },
        },
      ],
      routes: [],
      heartbeats: [
        { name: 'nightly_backup', period: '24h', grace: '1h', destinations: ['myapp_ops'] },
      ],
    });
    const fake = createFakeApi();
    const secretValue = 'super-secret-value-should-never-print';
    const deps = buildDeps(fake, {
      env: {
        MYAPP_OPS_WEBHOOK: secretValue,
        MYAPP_HOOK_URL: 'https://example.com/hook',
        MYAPP_HOOK_SECRET: secretValue,
      },
    });

    const out = await captureOutput(() => main(['plan', path.join(dir, 'myapp.json')], deps));
    const combined = [...out.stdout, ...out.stderr].join('\n');
    assert.ok(combined.includes('MYAPP_OPS_WEBHOOK'));
    assert.ok(combined.includes('MYAPP_HOOK_SECRET'));
    assert.ok(combined.includes('myapp'));
    assert.ok(!combined.includes(secretValue));
  });
});

describe('onboard.mjs management target resolution', () => {
  it('plan/apply succeed without app-access vars; only verify resolves them', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    await captureOutput(() => main(['plan', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, undefined);
    for (const call of deps.resolveTargetCalls) {
      assert.notEqual(call.idVar, 'ONBOARD_APP_ACCESS_ID');
    }
  });

  it('verify hands ONBOARD_APP_KEY to createHttp, and plan/apply never do', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    await captureOutput(() => main(['plan', path.join(dir, 'myapp.json')], deps));
    await captureOutput(() => main(['verify', path.join(dir, 'myapp.json')], deps));
    const appKeys = deps.createHttpCalls.map((c) => c.appKey);
    assert.deepEqual(appKeys, [undefined, TEST_APP_KEY]);
  });

  it('verify fails closed without ONBOARD_APP_KEY before any target is resolved or request made', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });
    for (const missing of [undefined, '', '   ']) {
      if (missing === undefined) {
        delete deps.env.ONBOARD_APP_KEY;
      } else {
        deps.env.ONBOARD_APP_KEY = missing;
      }
      await assert.rejects(
        main(['verify', path.join(dir, 'myapp.json')], deps),
        /ONBOARD_APP_KEY is required/,
      );
    }
    assert.equal(deps.resolveTargetCalls.length, 0);
    assert.equal(deps.createHttpCalls.length, 0);
    assert.equal(fake.calls.length, 0);
  });

  it('verify reports the missing app key before an unset manifest secret', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    // MYAPP_OPS_WEBHOOK is deliberately unset, and so is the app key.
    const deps = buildDeps(fake, {});
    delete deps.env.ONBOARD_APP_KEY;

    await assert.rejects(
      main(['verify', path.join(dir, 'myapp.json')], deps),
      /ONBOARD_APP_KEY is required/,
    );
  });

  it('uses the 15000ms default timeout for plan/apply and 25000ms for verify', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: { MYAPP_OPS_WEBHOOK: 'wh-secret-value' } });

    await captureOutput(() => main(['plan', path.join(dir, 'myapp.json')], deps));
    await captureOutput(() => main(['verify', path.join(dir, 'myapp.json')], deps));
    const timeouts = deps.createHttpCalls.map((c) => c.timeoutMs);
    assert.deepEqual(timeouts, [15000, 25000]);
  });
});

describe('onboard.mjs usage errors', () => {
  it('rejects --prune/--resend-secrets/an unknown flag on verify and lint before any target or request', async () => {
    const fake = createFakeApi();
    const deps = buildDeps(fake, {});
    await assert.rejects(main(['verify', 'whatever.json', '--prune'], deps));
    await assert.rejects(main(['verify', 'whatever.json', '--resend-secrets'], deps));
    await assert.rejects(main(['verify', 'whatever.json', '--bogus'], deps));
    await assert.rejects(main(['lint', 'apps', '--prune'], deps));
    assert.equal(fake.calls.length, 0);
    assert.equal(deps.resolveTargetCalls.length, 0);
    assert.equal(deps.createHttpCalls.length, 0);
  });
});

describe('onboard.mjs lint dispatch', () => {
  it('prints "N manifests" and exits 0 on a clean fixture, and one line per error on a dirty one', async () => {
    const cleanDir = tmpDir();
    writeManifest(cleanDir, 'myapp.json', appManifest());
    const cleanOut = await captureOutput(() => main(['lint', cleanDir]));
    assert.equal(process.exitCode, undefined);
    assert.ok(cleanOut.stdout.includes('1 manifests'));

    const dirtyDir = tmpDir();
    writeManifest(dirtyDir, 'myapp.json', { app_id: 'myapp', bogus: true });
    const dirtyOut = await captureOutput(() => main(['lint', dirtyDir]));
    assert.equal(process.exitCode, 1);
    assert.ok(dirtyOut.stdout.some((line) => line.includes('unknown key')));
  });

  it('never resolves a secret or reaches the network, even with absent secret env vars and no ONBOARD_* set', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: {} });

    const out = await captureOutput(() => main(['lint', dir], deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(out.stdout.includes('1 manifests'));
    assert.equal(fake.calls.length, 0);
  });
});

/** Writes `apps/myapp.json`-shaped files into a fresh dir and returns the path of the manifest
 * given by `fileName`: an app repo keeps its manifest anywhere, under any name. */
function appRepoManifest(manifest = appManifest(), fileName = 'notifs.json') {
  const dir = tmpDir();
  writeManifest(dir, fileName, manifest);
  return { dir, manifestPath: path.join(dir, fileName) };
}

const APP_ENV = { MYAPP_OPS_WEBHOOK: 'wh-secret-value' };

describe('onboard.mjs parseArgs: --manifest / --target app', () => {
  it('parses the app target for plan/apply/verify and lint', () => {
    assert.deepEqual(
      parseArgs(['apply', '--manifest', 'm.json', '--target', 'app', '--env', 'prod', '--prune']),
      {
        command: 'apply',
        manifestPath: 'm.json',
        envName: 'prod',
        prune: true,
        resendSecrets: false,
        target: 'app',
      },
    );
    assert.equal(parseArgs(['verify', '--target', 'app', '--manifest', 'm.json']).target, 'app');
    assert.deepEqual(parseArgs(['lint', '--manifest', 'm.json', '--target', 'app']), {
      command: 'lint',
      manifestPath: 'm.json',
      target: 'app',
    });
  });

  it('keeps the operator form unchanged: positional manifest, target operator', () => {
    assert.equal(parseArgs(['plan', 'apps/myapp.json']).target, 'operator');
    assert.deepEqual(parseArgs(['lint', 'apps']), { command: 'lint', dir: 'apps' });
    assert.deepEqual(parseArgs(['lint']), { command: 'lint', dir: 'apps' });
  });

  it('rejects every inconsistent combination as a usage error', () => {
    // Each argv is paired with the message fragment it must be rejected for, so a combination
    // refused for the wrong reason cannot pass.
    const bad = [
      [['plan', '--manifest', 'm.json'], '--manifest requires --target app'],
      [['plan', '--target', 'app'], '--target app requires --manifest'],
      [['plan', '--manifest', 'm.json', '--target', 'operator'], '--target must be "app"'],
      [['plan', 'm.json', '--manifest', 'm.json', '--target', 'app'], 'not a positional'],
      [['plan', 'm.json', '--target', 'app'], '--target app requires --manifest'],
      [['plan', '--manifest'], '--manifest requires a value'],
      [['plan', '--manifest', '--target', 'app'], '--manifest requires a value'],
      [['plan', '--manifest', 'm.json', '--target'], '--target requires a value'],
      [['lint', '--manifest', 'm.json'], 'lint --manifest requires --target app'],
      [['lint', '--target', 'app'], 'lint --manifest requires --target app'],
      [
        ['lint', 'apps', '--manifest', 'm.json', '--target', 'app'],
        'lint --manifest takes no directory argument',
      ],
      [
        ['lint', '--manifest', 'm.json', '--target', 'app', '--prune'],
        'lint takes no flags other than',
      ],
      [
        ['verify', '--manifest', 'm.json', '--target', 'app', '--prune'],
        '--prune/--resend-secrets are not valid with verify',
      ],
    ];
    for (const [argv, fragment] of bad) {
      assert.throws(() => parseArgs(argv), new RegExp(fragment), argv.join(' '));
    }
  });

  it('rejects a repeated --manifest or --target as a usage error', () => {
    const repeated = [
      [
        ['plan', '--manifest', 'a.json', '--manifest', 'b.json', '--target', 'app'],
        '--manifest given more than once',
      ],
      [
        ['apply', '--manifest', 'a.json', '--target', 'app', '--target', 'app'],
        '--target given more than once',
      ],
      [
        ['verify', '--target', 'app', '--manifest', 'a.json', '--manifest', 'b.json'],
        '--manifest given more than once',
      ],
      [
        ['lint', '--manifest', 'a.json', '--manifest', 'b.json', '--target', 'app'],
        '--manifest given more than once',
      ],
      [
        ['lint', '--manifest', 'a.json', '--target', 'app', '--target', 'app'],
        '--target given more than once',
      ],
    ];
    for (const [argv, fragment] of repeated) {
      assert.throws(() => parseArgs(argv), new RegExp(fragment), argv.join(' '));
    }
  });
});

describe('onboard.mjs --target app', () => {
  it('plans and applies through the app-scoped API with the app key and app Access token, never the admin API', async () => {
    const { manifestPath } = appRepoManifest();
    const fake = createFakeApi();
    const deps = buildDeps(fake, {
      env: { ...APP_ENV, ONBOARD_APP_ACCESS_ID: 'app-id', ONBOARD_APP_ACCESS_SECRET: 'app-secret' },
    });
    deps.createAdminApi = () => {
      throw new Error('--target app must never construct the admin API');
    };

    await captureOutput(() => main(['plan', '--manifest', manifestPath, '--target', 'app'], deps));
    await captureOutput(() => main(['apply', '--manifest', manifestPath, '--target', 'app'], deps));
    assert.equal(process.exitCode, undefined);

    assert.deepEqual(deps.createAppScopedApiCalls, [{ appId: 'myapp' }, { appId: 'myapp' }]);
    for (const call of deps.resolveTargetCalls) {
      assert.equal(call.idVar, 'ONBOARD_APP_ACCESS_ID');
      assert.equal(call.secretVar, 'ONBOARD_APP_ACCESS_SECRET');
    }
    for (const call of deps.createHttpCalls) {
      assert.equal(call.appKey, TEST_APP_KEY);
      assert.deepEqual(call.credentials, { id: 'app-id', secret: 'app-secret' });
      assert.equal(call.timeoutMs, 15000);
    }
    assert.equal(deps.createHttpCalls.length, 2);
    assert.deepEqual(
      fake.calls.map((c) => c.method).filter((m) => m.startsWith('create')),
      ['createDestination', 'createRoute', 'createCheck'],
    );
  });

  it('re-running apply is a no-op plan with zero write calls', async () => {
    const { manifestPath } = appRepoManifest();
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: APP_ENV });
    const argv = ['apply', '--manifest', manifestPath, '--target', 'app'];

    await captureOutput(() => main(argv, deps));
    fake.calls.length = 0;
    const second = await captureOutput(() => main(argv, deps));
    assert.equal(process.exitCode, undefined);
    assert.ok(second.stdout.includes('No changes.'));
    const writes = fake.calls.filter((c) => /^(create|patch|delete)/.test(c.method));
    assert.equal(writes.length, 0);
  });

  it('refuses to run without ONBOARD_APP_KEY before resolving any target or making any request', async () => {
    const { manifestPath } = appRepoManifest();
    for (const command of ['plan', 'apply', 'verify']) {
      for (const missing of [undefined, '', '   ']) {
        const fake = createFakeApi();
        const deps = buildDeps(fake, { env: APP_ENV });
        if (missing === undefined) {
          delete deps.env.ONBOARD_APP_KEY;
        } else {
          deps.env.ONBOARD_APP_KEY = missing;
        }
        await assert.rejects(
          main([command, '--manifest', manifestPath, '--target', 'app'], deps),
          /ONBOARD_APP_KEY is required for --target app/,
        );
        assert.equal(deps.resolveTargetCalls.length, 0, command);
        assert.equal(deps.createHttpCalls.length, 0, command);
        assert.equal(deps.createAppScopedApiCalls.length, 0, command);
        assert.equal(fake.calls.length, 0, command);
      }
    }
  });

  it('requires the app Access token pair for a non-loopback URL, and an operator pair does not satisfy it', async () => {
    const { manifestPath } = appRepoManifest();
    const remote = { ...APP_ENV, ONBOARD_URL: 'https://notifs.example.test' };
    const argv = ['plan', '--manifest', manifestPath, '--target', 'app'];

    const operatorPairOnly = buildDeps(createFakeApi(), {
      env: { ...remote, ONBOARD_ACCESS_ID: 'op-id', ONBOARD_ACCESS_SECRET: 'op-secret' },
    });
    await assert.rejects(
      main(argv, operatorPairOnly),
      /ONBOARD_APP_ACCESS_ID\/ONBOARD_APP_ACCESS_SECRET are required for a non-loopback/,
    );
    assert.equal(operatorPairOnly.createHttpCalls.length, 0);

    const halfSet = buildDeps(createFakeApi(), {
      env: { ...remote, ONBOARD_APP_ACCESS_ID: 'app-id' },
    });
    await assert.rejects(main(argv, halfSet), /both or neither of ONBOARD_APP_ACCESS_ID/);

    const complete = buildDeps(createFakeApi(), {
      env: { ...remote, ONBOARD_APP_ACCESS_ID: 'app-id', ONBOARD_APP_ACCESS_SECRET: 'app-secret' },
    });
    await captureOutput(() => main(argv, complete));
    assert.equal(process.exitCode, undefined);
    assert.equal(complete.createHttpCalls[0].baseUrl, 'https://notifs.example.test');
  });

  it('still requires the app key on a loopback URL', async () => {
    const { manifestPath } = appRepoManifest();
    const deps = buildDeps(createFakeApi(), {
      env: { ...APP_ENV, ONBOARD_URL: 'http://localhost:8300' },
    });
    delete deps.env.ONBOARD_APP_KEY;
    await assert.rejects(
      main(['plan', '--manifest', manifestPath, '--target', 'app'], deps),
      /ONBOARD_APP_KEY is required/,
    );
  });

  it('refuses the platform manifest', async () => {
    const { manifestPath } = appRepoManifest({ platform: true }, '_platform.json');
    const deps = buildDeps(createFakeApi(), {});
    for (const command of ['plan', 'apply']) {
      await assert.rejects(
        main([command, '--manifest', manifestPath, '--target', 'app'], deps),
        /takes an app manifest/,
      );
    }
    assert.equal(deps.createHttpCalls.length, 0);
  });

  it('verify sends with the app key and the app Access token', async () => {
    const { manifestPath } = appRepoManifest();
    const fake = createFakeApi();
    fake.queueSendMessage({
      result: { message: { deliveries: [{ destination: 'myapp_ops', state: 'delivered' }] } },
    });
    fake.queuePingCheck({ result: { state: 'up' } });
    const deps = buildDeps(fake, { env: APP_ENV });

    const out = await captureOutput(() =>
      main(['verify', '--manifest', manifestPath, '--target', 'app'], deps),
    );
    assert.equal(process.exitCode, undefined, out.stderr.join('\n'));
    assert.equal(deps.createHttpCalls.length, 1);
    assert.equal(deps.createHttpCalls[0].appKey, TEST_APP_KEY);
    assert.equal(deps.createHttpCalls[0].timeoutMs, 25000);
  });

  it('--prune deletes only what the manifest no longer declares and skips the directory lint gate', async () => {
    const { dir, manifestPath } = appRepoManifest();
    // A stray file an app repo may well keep next to its manifest: lintDirectory would refuse the
    // run over it; --target app must not look at siblings at all.
    fs.writeFileSync(path.join(dir, 'notes.txt'), 'not a manifest');
    const fake = createFakeApi();
    fake.seedDestination({ name: 'myapp_old', type: 'discord', secrets: { webhook_url: 'x' } });
    const deps = buildDeps(fake, { env: APP_ENV });

    const out = await captureOutput(() =>
      main(['apply', '--manifest', manifestPath, '--target', 'app', '--prune'], deps),
    );
    assert.equal(process.exitCode, undefined, out.stderr.join('\n'));
    assert.deepEqual(
      fake.calls.filter((c) => c.method === 'deleteDestination').map((c) => c.name),
      ['myapp_old'],
    );

    // The same sibling makes the operator-mode prune refuse, proving the gate really was skipped.
    const operatorDir = tmpDir();
    writeManifest(operatorDir, 'myapp.json', appManifest());
    fs.writeFileSync(path.join(operatorDir, 'notes.txt'), 'not a manifest');
    const operatorDeps = buildDeps(createFakeApi(), { env: APP_ENV });
    await captureOutput(() =>
      main(['apply', path.join(operatorDir, 'myapp.json'), '--prune'], operatorDeps),
    );
    assert.equal(process.exitCode, 1);
    assert.equal(operatorDeps.createHttpCalls.length, 0);
  });

  it('does not block on a `use` reference to an operator-owned destination, and says why', async () => {
    const { manifestPath } = appRepoManifest(
      appManifest({
        destinations: [{ use: 'platform_alerts' }],
        routes: [{ channel: 'ops', destinations: ['platform_alerts'] }],
        heartbeats: [],
      }),
    );
    const fake = createFakeApi();
    const deps = buildDeps(fake, {});
    const out = await captureOutput(() =>
      main(['plan', '--manifest', manifestPath, '--target', 'app'], deps),
    );
    const stdout = out.stdout.join('\n');
    assert.ok(!stdout.includes('blocking'), stdout);
    assert.ok(stdout.includes('shared destinations (platform_alerts)'), stdout);
    assert.ok(stdout.includes('+ create route'), stdout);
  });

  it('prints no operator-only manual steps and never the key or a secret', async () => {
    const { manifestPath } = appRepoManifest();
    const deps = buildDeps(createFakeApi(), { env: APP_ENV });
    const out = await captureOutput(() =>
      main(['apply', '--manifest', manifestPath, '--target', 'app'], deps),
    );
    const combined = `${out.stdout.join('\n')}\n${out.stderr.join('\n')}`;
    assert.ok(!combined.includes('Cloudflare Access service token'), combined);
    assert.ok(!combined.includes(TEST_APP_KEY));
    assert.ok(!combined.includes('wh-secret-value'));
    assert.ok(combined.includes('Manual steps:'));
  });

  it('turns a 409 on a name another owner holds into an actionable error without leaking secrets', async () => {
    const { manifestPath } = appRepoManifest();
    const fake = createFakeApi();
    fake.failNextCreateDestination('myapp_ops', fake.apiError(409, 'destination already exists'));
    const deps = buildDeps(fake, { env: APP_ENV });

    const out = await captureOutput(() =>
      main(['apply', '--manifest', manifestPath, '--target', 'app'], deps),
    );
    assert.equal(process.exitCode, 1);
    const stderr = out.stderr.join('\n');
    assert.ok(
      stderr.includes('failed: create-destination myapp_ops: 409 destination already exists'),
    );
    assert.ok(stderr.includes('does not own'), stderr);
    assert.ok(!stderr.includes(TEST_APP_KEY));
    assert.ok(!stderr.includes('wh-secret-value'));
  });

  it('adds a hint to a 400 SSRF refusal and a 503 URL-check outage', async () => {
    for (const [status, message, hint] of [
      [400, 'secrets.webhook_url must resolve to public addresses', 'public addresses'],
      [503, 'destination url check unavailable', 'retry the deploy'],
    ]) {
      const { manifestPath } = appRepoManifest();
      const fake = createFakeApi();
      fake.failNextCreateDestination('myapp_ops', fake.apiError(status, message));
      const deps = buildDeps(fake, { env: APP_ENV });
      const out = await captureOutput(() =>
        main(['apply', '--manifest', manifestPath, '--target', 'app'], deps),
      );
      assert.equal(process.exitCode, 1);
      const stderr = out.stderr.join('\n');
      assert.ok(stderr.includes(`${status} ${message}`), stderr);
      assert.ok(stderr.includes(hint), stderr);
      process.exitCode = undefined;
    }
  });

  it('reports a rejected key during listing with the status and a mint-a-new-key hint', async () => {
    const { manifestPath } = appRepoManifest();
    const deps = buildDeps(createFakeApi(), { env: APP_ENV });
    deps.createAppScopedApi = () => ({
      listDestinations: async () => {
        throw new ApiError({ status: 401, method: 'GET', path: '/x', message: 'invalid app key' });
      },
      listRoutes: async () => [],
      listChecks: async () => [],
    });
    await assert.rejects(
      main(['plan', '--manifest', manifestPath, '--target', 'app'], deps),
      /401 invalid app key \(.*redeploy to mint a new one\)/,
    );
  });

  it('blames the app key only for the delivery-api own 401/403 texts, and stays neutral on a non-JSON refusal', async () => {
    const { manifestPath } = appRepoManifest();
    const cases = [
      [401, 'invalid app key', /redeploy to mint a new one/, /Cloudflare Access/],
      [401, 'app key required', /redeploy to mint a new one/, /Cloudflare Access/],
      [403, 'app_id does not match app key', /does not belong to this manifest app_id/, /Access/],
      [
        401,
        'HTTP 401',
        /app key or the Cloudflare Access token \(ONBOARD_APP_ACCESS_ID\/SECRET\)/,
        /redeploy to mint/,
      ],
      [
        403,
        'HTTP 403',
        /app key or the Cloudflare Access token \(ONBOARD_APP_ACCESS_ID\/SECRET\)/,
        /does not belong to this manifest/,
      ],
    ];
    for (const [status, message, expected, unexpected] of cases) {
      const deps = buildDeps(createFakeApi(), { env: APP_ENV });
      deps.createAppScopedApi = () => ({
        listDestinations: async () => {
          throw new ApiError({ status, method: 'GET', path: '/x', message });
        },
        listRoutes: async () => [],
        listChecks: async () => [],
      });
      await assert.rejects(
        main(['plan', '--manifest', manifestPath, '--target', 'app'], deps),
        (err) => {
          assert.ok(err.message.startsWith(`${status} ${message} (`), err.message);
          assert.match(err.message, expected);
          assert.doesNotMatch(err.message, unexpected);
          return true;
        },
        `${status} ${message}`,
      );
    }
  });

  it('gives a 409 on a DELETE its own hint: still referenced, not a name clash', async () => {
    const { manifestPath } = appRepoManifest();
    const fake = createFakeApi();
    fake.seedDestination({ name: 'myapp_old', type: 'discord', secrets: { webhook_url: 'x' } });
    const deps = buildDeps(fake, { env: APP_ENV });
    deps.createAppScopedApi = () => ({
      ...fake.adminApi,
      deleteDestination: async (name) => {
        throw new ApiError({
          status: 409,
          method: 'DELETE',
          path: `/v1/apps/myapp/destinations/${name}`,
          message: 'destination is in use',
        });
      },
    });

    const out = await captureOutput(() =>
      main(['apply', '--manifest', manifestPath, '--target', 'app', '--prune'], deps),
    );
    assert.equal(process.exitCode, 1);
    const stderr = out.stderr.join('\n');
    assert.ok(stderr.includes('failed: delete-destination myapp_old: 409 destination is in use'));
    assert.ok(stderr.includes('still referenced by a route or check this app cannot see'), stderr);
    assert.ok(!stderr.includes('rename it in the manifest'), stderr);
  });

  it('treats a delete of an object that vanished concurrently as already deleted (app target only)', async () => {
    // The object is removed, then the server answers 404, as a racing deploy would cause.
    function vanishingApi(fake) {
      return {
        ...fake.adminApi,
        deleteDestination: async (name) => {
          await fake.adminApi.deleteDestination(name);
          throw new ApiError({
            status: 404,
            method: 'DELETE',
            path: `/x/destinations/${name}`,
            message: 'destination not found',
          });
        },
      };
    }

    const { manifestPath } = appRepoManifest();
    const fake = createFakeApi();
    fake.seedDestination({ name: 'myapp_old', type: 'discord', secrets: { webhook_url: 'x' } });
    const deps = buildDeps(fake, { env: APP_ENV });
    deps.createAppScopedApi = () => vanishingApi(fake);
    const out = await captureOutput(() =>
      main(['apply', '--manifest', manifestPath, '--target', 'app', '--prune'], deps),
    );
    assert.equal(process.exitCode, undefined, out.stderr.join('\n'));
    assert.ok(
      out.stderr.some((line) => line.includes('delete-destination myapp_old: already deleted')),
      out.stderr.join('\n'),
    );
    assert.ok(!out.stderr.some((line) => line.includes('failed:')), out.stderr.join('\n'));

    // Operator mode keeps failing the apply on the same 404.
    const operatorDir = tmpDir();
    writeManifest(operatorDir, 'myapp.json', appManifest());
    const operatorFake = createFakeApi();
    operatorFake.seedDestination({
      name: 'myapp_old',
      type: 'discord',
      secrets: { webhook_url: 'x' },
    });
    const operatorDeps = buildDeps(operatorFake, { env: APP_ENV });
    operatorDeps.createAdminApi = () => vanishingApi(operatorFake);
    const operatorOut = await captureOutput(() =>
      main(['apply', path.join(operatorDir, 'myapp.json'), '--prune'], operatorDeps),
    );
    assert.equal(process.exitCode, 1);
    assert.ok(
      operatorOut.stderr.some((line) => line.includes('failed: delete-destination myapp_old: 404')),
      operatorOut.stderr.join('\n'),
    );
  });

  it('surfaces a null manifest file as the friendly invalid-manifest error, not a TypeError', async () => {
    const dir = tmpDir();
    fs.writeFileSync(path.join(dir, 'notifs.json'), 'null');
    const deps = buildDeps(createFakeApi(), { env: APP_ENV });
    for (const command of ['plan', 'apply']) {
      await assert.rejects(
        main([command, '--manifest', path.join(dir, 'notifs.json'), '--target', 'app'], deps),
        /invalid manifest: \(root\): manifest must be a JSON object/,
        command,
      );
    }
    assert.equal(deps.createHttpCalls.length, 0);
  });

  it('rejects a malformed ONBOARD_APP_KEY before any target is resolved or request made, without echoing it', async () => {
    const { manifestPath } = appRepoManifest();
    const secretPart = 'SECRETPARTOFKEY';
    const badKeys = [
      `dak_${secretPart}\nabcdefghijklmnopqrstuvwxyzabcdefghij`,
      `dak_${secretPart}`,
      `dak_${'0'.repeat(42)}`,
      `dak_${'0'.repeat(44)}`,
      `xyz_${'0'.repeat(43)}`,
      `dak_${'0'.repeat(42)}!`,
    ];
    for (const command of ['plan', 'apply', 'verify']) {
      for (const badKey of badKeys) {
        const fake = createFakeApi();
        const deps = buildDeps(fake, { env: { ...APP_ENV, ONBOARD_APP_KEY: badKey } });
        await assert.rejects(
          main([command, '--manifest', manifestPath, '--target', 'app'], deps),
          (err) => {
            assert.match(err.message, /ONBOARD_APP_KEY is not a valid app delivery key/);
            assert.ok(!err.message.includes(badKey.trim()), err.message);
            assert.ok(!err.message.includes(secretPart), err.message);
            return true;
          },
          `${command} ${JSON.stringify(badKey)}`,
        );
        assert.equal(deps.resolveTargetCalls.length, 0, command);
        assert.equal(deps.createHttpCalls.length, 0, command);
        assert.equal(deps.createAppScopedApiCalls.length, 0, command);
        assert.equal(fake.calls.length, 0, command);
      }
    }
  });

  it('fails closed on an unknown target passed to runPlan/runApply/runVerify, and keeps an unset one operator', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const manifestPath = path.join(dir, 'myapp.json');
    const manifest = appManifest();

    for (const run of [runPlan, runApply, runVerify]) {
      for (const target of ['bogus', '', null, 'APP', 'admin']) {
        const fake = createFakeApi();
        const deps = buildDeps(fake, { env: APP_ENV });
        await assert.rejects(
          run({ manifest, manifestPath, options: { target }, deps }),
          /unknown onboard target/,
          `${run.name} ${String(target)}`,
        );
        assert.equal(deps.resolveTargetCalls.length, 0);
        assert.equal(deps.createHttpCalls.length, 0);
        assert.equal(fake.calls.length, 0);
      }
    }

    // `undefined` is the existing direct-caller default and still means the operator target.
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: APP_ENV });
    await captureOutput(() => runPlan({ manifest, manifestPath, options: {}, deps }));
    assert.ok(fake.calls.some((c) => c.method === 'listDestinations'));
    assert.equal(deps.createAppScopedApiCalls.length, 0);
  });

  it('leaves operator-mode error text exactly as before', async () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', appManifest());
    const fake = createFakeApi();
    fake.failNextCreateDestination('myapp_ops', fake.apiError(409, 'destination already exists'));
    const deps = buildDeps(fake, { env: APP_ENV });
    const out = await captureOutput(() => main(['apply', path.join(dir, 'myapp.json')], deps));
    assert.equal(process.exitCode, 1);
    assert.ok(
      out.stderr
        .join('\n')
        .includes('failed: create-destination myapp_ops: 409 destination already exists\n'),
    );
    assert.equal(deps.createAppScopedApiCalls.length, 0);
  });
});

describe('onboard.mjs lint --manifest --target app', () => {
  function writeSiblingConflict() {
    const dir = tmpDir();
    // appc.json is valid and owns appc_ops. appd.json reuses the exact same name by mistake: a
    // cross-manifest conflict only lintDirectory can see.
    writeManifest(dir, 'appc.json', {
      app_id: 'appc',
      destinations: [{ name: 'appc_ops', type: 'discord', secrets: { webhook_url: { env: 'C' } } }],
      routes: [{ channel: 'ops', destinations: ['appc_ops'] }],
    });
    writeManifest(dir, 'appd.json', {
      app_id: 'appd',
      destinations: [{ name: 'appc_ops', type: 'discord', secrets: { webhook_url: { env: 'D' } } }],
      routes: [{ channel: 'ops', destinations: ['appc_ops'] }],
    });
    return dir;
  }

  it('runs the single-file schema check only: a sibling conflict that fails lint <dir> does not fail it', async () => {
    const dir = writeSiblingConflict();

    const dirOut = await captureOutput(() => main(['lint', dir]));
    assert.equal(process.exitCode, 1);
    assert.ok(dirOut.stdout.some((line) => line.includes('more than one file')));

    process.exitCode = undefined;
    const fake = createFakeApi();
    const deps = buildDeps(fake, { env: {} });
    const out = await captureOutput(() =>
      main(['lint', '--manifest', path.join(dir, 'appc.json'), '--target', 'app'], deps),
    );
    assert.equal(process.exitCode, undefined, out.stdout.join('\n'));
    assert.deepEqual(out.stdout, ['1 manifest']);
    assert.equal(deps.resolveTargetCalls.length, 0);
    assert.equal(deps.createHttpCalls.length, 0);
    assert.equal(fake.calls.length, 0);
  });

  it('still fails the manifest itself on a schema error, whatever the file is called', async () => {
    const { manifestPath } = appRepoManifest({ app_id: 'myapp', bogus: true }, 'anything.json');
    const out = await captureOutput(() =>
      main(['lint', '--manifest', manifestPath, '--target', 'app']),
    );
    assert.equal(process.exitCode, 1);
    assert.ok(out.stdout.some((line) => line.startsWith(`${manifestPath}: bogus: unknown key`)));
  });

  it('accepts a differently named valid manifest, and fails an unreadable or platform one', async () => {
    const ok = appRepoManifest(appManifest(), 'deploy-notifs.json');
    await captureOutput(() => main(['lint', '--manifest', ok.manifestPath, '--target', 'app']));
    assert.equal(process.exitCode, undefined);

    const missing = path.join(tmpDir(), 'absent.json');
    const missingOut = await captureOutput(() =>
      main(['lint', '--manifest', missing, '--target', 'app']),
    );
    assert.equal(process.exitCode, 1);
    assert.ok(missingOut.stdout.some((line) => line.includes('unreadable or invalid JSON')));

    process.exitCode = undefined;
    const platform = appRepoManifest({ platform: true }, '_platform.json');
    const platformOut = await captureOutput(() =>
      main(['lint', '--manifest', platform.manifestPath, '--target', 'app']),
    );
    assert.equal(process.exitCode, 1);
    assert.ok(platformOut.stdout.some((line) => line.includes('takes an app manifest')));
  });
});

describe('onboard.mjs docs cross-check', () => {
  // Run via `make scripts-test` / `make test` (cwd = repo root), per the Makefile's
  // `node --test "scripts/*.test.mjs"`. `knownEnvVars`/`knownFlags` are scraped from this
  // CLI's own source rather than hardcoded, so the guide and the CLI can never silently drift.
  it('every ONBOARD_* env var and --flag named in the manifest-onboarding guide section is known to this CLI', () => {
    const scriptsDir = path.dirname(fileURLToPath(import.meta.url));
    const guideText = fs.readFileSync(
      path.join(scriptsDir, '..', 'docs', 'app-integration-guide.md'),
      'utf8',
    );
    const sectionStart = guideText.indexOf('## Onboarding with a manifest (recommended)');
    assert.ok(sectionStart >= 0, 'guide is missing the "Onboarding with a manifest" section');
    const nextHeadingIndex = guideText.indexOf('\n## ', sectionStart + 1);
    const section =
      nextHeadingIndex >= 0
        ? guideText.slice(sectionStart, nextHeadingIndex)
        : guideText.slice(sectionStart);

    const clientSource = fs.readFileSync(path.join(scriptsDir, 'onboard-client.mjs'), 'utf8');
    const cliSource = fs.readFileSync(path.join(scriptsDir, 'onboard.mjs'), 'utf8');
    const knownEnvVars = new Set(clientSource.match(/ONBOARD_[A-Z_]+/g));
    const knownFlags = new Set(cliSource.match(/--[a-z-]+/g) ?? []);

    const taggedTokens = section.match(/`(ONBOARD_[A-Z_]+|--[a-z-]+)`/g) ?? [];
    assert.ok(
      taggedTokens.length > 0,
      'expected at least one backticked ONBOARD_*/--flag token in the guide section',
    );
    for (const tagged of taggedTokens) {
      const token = tagged.slice(1, -1);
      const known = token.startsWith('--') ? knownFlags.has(token) : knownEnvVars.has(token);
      assert.ok(known, `${token} is named in the guide but not known to scripts/onboard.mjs`);
    }
  });
});

import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';

import { ApiError, resolveTarget } from './onboard-client.mjs';
import { main } from './onboard.mjs';

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

/** Builds `main`'s `deps`: real `resolveTarget`/`createHttp` (spied, so the real fail-closed
 * rules and timeout wiring still run) but `createAdminApi`/`createAppApi` always return the
 * fake, regardless of the dummy `http` they are handed. */
function buildDeps(fake, { env = {}, now } = {}) {
  const resolveTargetCalls = [];
  const createHttpCalls = [];
  return {
    env: { ONBOARD_URL: 'http://127.0.0.1:9999', ...env },
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
    createAppApi: () => fake.appApi,
    resolveTargetCalls,
    createHttpCalls,
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

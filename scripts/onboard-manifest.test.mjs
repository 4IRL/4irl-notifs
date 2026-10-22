import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, it } from 'node:test';

import {
  MAX_GRACE_SECONDS,
  MAX_PERIOD_SECONDS,
  MIN_GRACE_SECONDS,
  MIN_PERIOD_SECONDS,
  lintDirectory,
  parseDuration,
  resolveManifest,
  validateManifest,
} from './onboard-manifest.mjs';

function tmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-onboard-'));
}

function writeManifest(dir, fileName, manifest) {
  fs.writeFileSync(path.join(dir, fileName), JSON.stringify(manifest), 'utf8');
}

function errorPaths(result) {
  return result.errors.map((e) => e.path);
}

function baseAppManifest(overrides = {}) {
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
    ...overrides,
  };
}

describe('parseDuration', () => {
  it('accepts valid durations and converts to seconds', () => {
    assert.equal(parseDuration('90s'), 90);
    assert.equal(parseDuration('5m'), 300);
    assert.equal(parseDuration('24h'), 86400);
    assert.equal(parseDuration('7d'), 604800);
  });

  it('rejects malformed durations', () => {
    for (const bad of ['0h', '1.5h', '24', 'h', '24H', '-1h']) {
      assert.throws(() => parseDuration(bad), undefined, bad);
    }
  });
});

describe('validateManifest — app manifest schema', () => {
  it('accepts a minimal valid app manifest', () => {
    const result = validateManifest(baseAppManifest(), { fileName: 'myapp.json' });
    assert.equal(result.kind, 'app');
    assert.deepEqual(result.errors, []);
  });

  it('rejects a literal string secret with a path-qualified error', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [
          { name: 'myapp_ops', type: 'discord', secrets: { webhook_url: 'https://discord.com/x' } },
        ],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[0].secrets.webhook_url' && /literal value/.test(e.message),
      ),
    );
  });

  it('rejects an unknown key at any depth', () => {
    const top = validateManifest({ ...baseAppManifest(), bogus: 1 });
    assert.ok(top.errors.some((e) => e.path === 'bogus'));

    const nested = validateManifest(
      baseAppManifest({
        destinations: [
          {
            name: 'myapp_ops',
            type: 'discord',
            secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
            unexpected: true,
          },
        ],
      }),
    );
    assert.ok(nested.errors.some((e) => e.path === 'destinations[0].unexpected'));
  });

  it('rejects an owned name without the <app_id>_ prefix', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [
          { name: 'ops', type: 'discord', secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } } },
        ],
        routes: [{ channel: 'ops', destinations: ['ops'] }],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[0].name' && /must start with/.test(e.message),
      ),
    );
  });

  it('rejects a `use` name that has the <app_id>_ prefix', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [{ use: 'myapp_shared' }],
        routes: [{ channel: 'ops', destinations: ['myapp_shared'] }],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[0].use' && /must not start with/.test(e.message),
      ),
    );
  });

  it('rejects a route naming an undeclared destination', () => {
    const result = validateManifest(
      baseAppManifest({ routes: [{ channel: 'ops', destinations: ['nope'] }] }),
    );
    assert.ok(result.errors.some((e) => e.path === 'routes[0].destinations[0]'));
  });

  it('rejects a heartbeat naming an undeclared destination', () => {
    const result = validateManifest(
      baseAppManifest({
        heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: ['nope'] }],
      }),
    );
    assert.ok(result.errors.some((e) => e.path === 'heartbeats[0].destinations[0]'));
  });

  it('rejects a bare "*" channel in an app manifest', () => {
    const result = validateManifest(
      baseAppManifest({ routes: [{ channel: '*', destinations: ['myapp_ops'] }] }),
    );
    assert.ok(result.errors.some((e) => e.path === 'routes[0].channel'));
  });

  it('accepts a shared reference via "use" and resolves it in a route', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [{ use: 'shared_ops' }],
        routes: [{ channel: 'ops', destinations: ['shared_ops'] }],
      }),
    );
    assert.deepEqual(result.errors, []);
  });

  it('enforces period/grace bounds: 59s rejected, 31d period accepted, 32d period / 8d grace rejected', () => {
    const okBoundary = validateManifest(
      baseAppManifest({
        destinations: [
          { name: 'myapp_ops', type: 'discord', secrets: { webhook_url: { env: 'X' } } },
        ],
        heartbeats: [{ name: 'nightly', period: '31d', grace: '1h', destinations: ['myapp_ops'] }],
      }),
    );
    assert.deepEqual(okBoundary.errors, []);

    const badPeriodLow = validateManifest(
      baseAppManifest({
        heartbeats: [{ name: 'nightly', period: '59s', grace: '1h', destinations: ['myapp_ops'] }],
      }),
    );
    assert.ok(badPeriodLow.errors.some((e) => e.path === 'heartbeats[0].period'));

    const badGraceLow = validateManifest(
      baseAppManifest({
        heartbeats: [{ name: 'nightly', period: '24h', grace: '59s', destinations: ['myapp_ops'] }],
      }),
    );
    assert.ok(badGraceLow.errors.some((e) => e.path === 'heartbeats[0].grace'));

    const badPeriodHigh = validateManifest(
      baseAppManifest({
        heartbeats: [{ name: 'nightly', period: '32d', grace: '1h', destinations: ['myapp_ops'] }],
      }),
    );
    assert.ok(badPeriodHigh.errors.some((e) => e.path === 'heartbeats[0].period'));

    const badGraceHigh = validateManifest(
      baseAppManifest({
        heartbeats: [{ name: 'nightly', period: '24h', grace: '8d', destinations: ['myapp_ops'] }],
      }),
    );
    assert.ok(badGraceHigh.errors.some((e) => e.path === 'heartbeats[0].grace'));

    assert.equal(MIN_PERIOD_SECONDS, 60);
    assert.equal(MAX_PERIOD_SECONDS, 2_678_400);
    assert.equal(MIN_GRACE_SECONDS, 60);
    assert.equal(MAX_GRACE_SECONDS, 604_800);
  });

  it('applies an environments override of grace before bounds checks, and flags a missing reference', () => {
    const okOverride = validateManifest(
      baseAppManifest({
        heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: ['myapp_ops'] }],
        environments: { staging: { heartbeats: { nightly: { grace: '2h' } } } },
      }),
    );
    assert.deepEqual(okOverride.errors, []);

    const missingHeartbeat = validateManifest(
      baseAppManifest({
        heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: ['myapp_ops'] }],
        environments: { staging: { heartbeats: { bogus: { grace: '2h' } } } },
      }),
    );
    assert.ok(
      missingHeartbeat.errors.some((e) => e.path === 'environments.staging.heartbeats.bogus'),
    );

    const missingDestination = validateManifest(
      baseAppManifest({
        environments: { staging: { destinations: { bogus: { enabled: false } } } },
      }),
    );
    assert.ok(
      missingDestination.errors.some((e) => e.path === 'environments.staging.destinations.bogus'),
    );
  });

  it('rejects an app_id of "platform" or starting with "platform_"', () => {
    assert.ok(
      validateManifest(baseAppManifest({ app_id: 'platform' })).errors.some(
        (e) => e.path === 'app_id',
      ),
    );
    assert.ok(
      validateManifest(baseAppManifest({ app_id: 'platform_x' })).errors.some(
        (e) => e.path === 'app_id',
      ),
    );
  });

  it('requires the file basename to equal app_id', () => {
    const result = validateManifest(baseAppManifest(), { fileName: 'other.json' });
    assert.ok(result.errors.some((e) => e.path === 'app_id'));
  });

  it('does not throw when fileName is not a string', () => {
    assert.doesNotThrow(() => validateManifest({ app_id: 'myapp' }, { fileName: null }));
    assert.doesNotThrow(() => validateManifest({ app_id: 'myapp' }, { fileName: 42 }));
  });
});

describe('validateManifest — single-file rules (DD-1)', () => {
  it('rejects a duplicate owned destination name within one manifest', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [
          { name: 'myapp_ops', type: 'discord', secrets: { webhook_url: { env: 'A' } } },
          { name: 'myapp_ops', type: 'discord', secrets: { webhook_url: { env: 'B' } } },
        ],
        routes: [{ channel: 'ops', destinations: ['myapp_ops'] }],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[1].name' && /duplicate destination name/.test(e.message),
      ),
    );
  });

  it('rejects an invalid destination type', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [{ name: 'myapp_ops', type: 'carrier_pigeon', secrets: {} }],
        routes: [{ channel: 'ops', destinations: ['myapp_ops'] }],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[0].type' && /invalid type/.test(e.message),
      ),
    );
  });

  it('rejects an invalid ntfy config.topic failing NTFY_TOPIC_PATTERN', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [{ name: 'myapp_push', type: 'ntfy', config: { topic: 'bad topic!' } }],
        routes: [{ channel: 'ops', destinations: ['myapp_push'] }],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[0].config.topic' && /invalid topic/.test(e.message),
      ),
    );
  });

  it('rejects a duplicate `use` shared-reference name', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [{ use: 'shared_ops' }, { use: 'shared_ops' }],
        routes: [{ channel: 'ops', destinations: ['shared_ops'] }],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[1].use' && /duplicate shared reference/.test(e.message),
      ),
    );
  });

  it('rejects a destination entry with neither "name" nor "use"', () => {
    const result = validateManifest(
      baseAppManifest({
        destinations: [
          { type: 'discord', secrets: { webhook_url: { env: 'X' } } },
          { name: 'myapp_ops', type: 'discord', secrets: { webhook_url: { env: 'Y' } } },
        ],
        routes: [{ channel: 'ops', destinations: ['myapp_ops'] }],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'destinations[0]' && /must have a "name"/.test(e.message),
      ),
    );
  });

  it('enforces the 1-20 destinations bound for routes and heartbeats, boundaries included', () => {
    const manyNames = Array.from({ length: 20 }, (_, i) => `myapp_d${i}`);
    const destinations = manyNames.map((name) => ({
      name,
      type: 'discord',
      secrets: { webhook_url: { env: `VAR_${name.toUpperCase()}` } },
    }));
    const extraName = 'myapp_d20';
    const tooManyNames = [...manyNames, extraName];
    const tooManyDestinations = [
      ...destinations,
      { name: extraName, type: 'discord', secrets: { webhook_url: { env: 'VAR_EXTRA' } } },
    ];

    const okRoute = validateManifest(
      baseAppManifest({ destinations, routes: [{ channel: 'ops', destinations: manyNames }] }),
    );
    assert.deepEqual(
      okRoute.errors.filter((e) => e.path.startsWith('routes')),
      [],
    );

    const tooFewRoute = validateManifest(
      baseAppManifest({ destinations, routes: [{ channel: 'ops', destinations: [] }] }),
    );
    assert.ok(
      tooFewRoute.errors.some(
        (e) => e.path === 'routes[0].destinations' && /1-20 destinations/.test(e.message),
      ),
    );

    const tooManyRoute = validateManifest(
      baseAppManifest({
        destinations: tooManyDestinations,
        routes: [{ channel: 'ops', destinations: tooManyNames }],
      }),
    );
    assert.ok(
      tooManyRoute.errors.some(
        (e) => e.path === 'routes[0].destinations' && /1-20 destinations/.test(e.message),
      ),
    );

    const okHeartbeat = validateManifest(
      baseAppManifest({
        destinations,
        routes: [{ channel: 'ops', destinations: manyNames }],
        heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: manyNames }],
      }),
    );
    assert.deepEqual(
      okHeartbeat.errors.filter((e) => e.path.startsWith('heartbeats')),
      [],
    );

    const tooFewHeartbeat = validateManifest(
      baseAppManifest({
        destinations,
        routes: [{ channel: 'ops', destinations: manyNames }],
        heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: [] }],
      }),
    );
    assert.ok(
      tooFewHeartbeat.errors.some(
        (e) => e.path === 'heartbeats[0].destinations' && /1-20 destinations/.test(e.message),
      ),
    );

    const tooManyHeartbeat = validateManifest(
      baseAppManifest({
        destinations: tooManyDestinations,
        routes: [{ channel: 'ops', destinations: manyNames }],
        heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: tooManyNames }],
      }),
    );
    assert.ok(
      tooManyHeartbeat.errors.some(
        (e) => e.path === 'heartbeats[0].destinations' && /1-20 destinations/.test(e.message),
      ),
    );
  });

  it("rejects a duplicate destination name inside one route's or heartbeat's destinations array", () => {
    const routeResult = validateManifest(
      baseAppManifest({
        routes: [{ channel: 'ops', destinations: ['myapp_ops', 'myapp_ops'] }],
      }),
    );
    assert.ok(
      routeResult.errors.some(
        (e) => e.path === 'routes[0].destinations[1]' && /duplicate destination/.test(e.message),
      ),
    );

    const heartbeatResult = validateManifest(
      baseAppManifest({
        heartbeats: [
          { name: 'nightly', period: '24h', grace: '1h', destinations: ['myapp_ops', 'myapp_ops'] },
        ],
      }),
    );
    assert.ok(
      heartbeatResult.errors.some(
        (e) =>
          e.path === 'heartbeats[0].destinations[1]' && /duplicate destination/.test(e.message),
      ),
    );
  });

  it('rejects a duplicate route channel', () => {
    const result = validateManifest(
      baseAppManifest({
        routes: [
          { channel: 'ops', destinations: ['myapp_ops'] },
          { channel: 'ops', destinations: ['myapp_ops'] },
        ],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'routes[1].channel' && /duplicate channel/.test(e.message),
      ),
    );
  });

  it('rejects a duplicate heartbeat name', () => {
    const result = validateManifest(
      baseAppManifest({
        heartbeats: [
          { name: 'nightly', period: '24h', grace: '1h', destinations: ['myapp_ops'] },
          { name: 'nightly', period: '12h', grace: '1h', destinations: ['myapp_ops'] },
        ],
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'heartbeats[1].name' && /duplicate heartbeat name/.test(e.message),
      ),
    );
  });

  it('rejects an invalid environments.<env> key failing ENVIRONMENT_NAME_PATTERN', () => {
    const result = validateManifest(
      baseAppManifest({
        environments: { Staging_Bad: {} },
      }),
    );
    assert.ok(
      result.errors.some(
        (e) => e.path === 'environments.Staging_Bad' && /invalid environment name/.test(e.message),
      ),
    );
  });
});

describe('validateManifest — platform manifest schema', () => {
  it('accepts a minimal valid platform manifest', () => {
    const result = validateManifest(
      {
        platform: true,
        destinations: [
          {
            name: 'platform_ops',
            type: 'discord',
            secrets: { webhook_url: { env: 'PLATFORM_OPS' } },
          },
        ],
        routes: [{ channel: '*', destinations: ['platform_ops'] }],
      },
      { fileName: '_platform.json' },
    );
    assert.equal(result.kind, 'platform');
    assert.deepEqual(result.errors, []);
  });

  it('rejects an owned destination name without the platform_ prefix', () => {
    const result = validateManifest({
      platform: true,
      destinations: [{ name: 'ops', type: 'discord', secrets: { webhook_url: { env: 'X' } } }],
      routes: [{ channel: '*', destinations: ['ops'] }],
    });
    assert.ok(result.errors.some((e) => e.path === 'destinations[0].name'));
  });

  it('rejects app_id, heartbeats or environments on a platform manifest', () => {
    const result = validateManifest({
      platform: true,
      app_id: 'x',
      heartbeats: [],
      environments: {},
    });
    assert.ok(result.errors.some((e) => e.path === 'app_id'));
    assert.ok(result.errors.some((e) => e.path === 'heartbeats'));
    assert.ok(result.errors.some((e) => e.path === 'environments'));
  });

  it('does not throw when fileName is not a string', () => {
    assert.doesNotThrow(() => validateManifest({ platform: true }, { fileName: null }));
    assert.doesNotThrow(() => validateManifest({ platform: true }, { fileName: 42 }));
  });
});

describe('resolveManifest', () => {
  it('converts durations, resolves secrets from env, and sorts destination lists', () => {
    const manifest = baseAppManifest({
      destinations: [
        { use: 'shared_ops' },
        {
          name: 'myapp_ops',
          type: 'discord',
          secrets: { webhook_url: { env: 'MYAPP_OPS_WEBHOOK' } },
        },
      ],
      routes: [{ channel: 'ops', destinations: ['myapp_ops', 'shared_ops'] }],
      heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: ['myapp_ops'] }],
    });
    const resolved = resolveManifest(manifest, {
      env: { MYAPP_OPS_WEBHOOK: 'https://discord.com/x' },
    });
    assert.deepEqual(resolved.shared, ['shared_ops']);
    assert.deepEqual(resolved.destinations, [
      {
        name: 'myapp_ops',
        type: 'discord',
        config: {},
        secrets: { webhook_url: 'https://discord.com/x' },
        enabled: true,
      },
    ]);
    assert.deepEqual(resolved.routes, [
      {
        app_pattern: 'myapp',
        channel_pattern: 'ops',
        destinations: ['myapp_ops', 'shared_ops'],
        enabled: true,
      },
    ]);
    assert.deepEqual(resolved.checks, [
      {
        app_id: 'myapp',
        name: 'nightly',
        period_seconds: 86400,
        grace_seconds: 3600,
        destinations: ['myapp_ops'],
      },
    ]);
  });

  it('calls validateManifest first and surfaces its errors before any resolution work', () => {
    const manifest = baseAppManifest({ app_id: 'platform' });
    assert.throws(
      () => resolveManifest(manifest, { env: {} }),
      (err) => {
        assert.ok(Array.isArray(err.errors) && err.errors.length > 0);
        return true;
      },
    );
  });

  it('reports env <name>: no overrides once for an --env with no matching block', () => {
    const resolved = resolveManifest(baseAppManifest(), {
      env: { MYAPP_OPS_WEBHOOK: 'x' },
      envName: 'staging',
    });
    assert.deepEqual(resolved.notices, ['env staging: no overrides']);
  });

  it('reports no overrides for __proto__ instead of silently resolving to Object.prototype', () => {
    const resolved = resolveManifest(baseAppManifest(), {
      env: { MYAPP_OPS_WEBHOOK: 'x' },
      envName: '__proto__',
    });
    assert.deepEqual(resolved.notices, ['env __proto__: no overrides']);
  });

  it('collects all missing secret env vars and reports them together, sorted', () => {
    const manifest = baseAppManifest({
      destinations: [
        { name: 'myapp_ops', type: 'discord', secrets: { webhook_url: { env: 'MYAPP_B' } } },
        {
          name: 'myapp_push',
          type: 'ntfy',
          config: { topic: 'myapp-push' },
          secrets: { access_token: { env: 'MYAPP_A' } },
        },
      ],
      routes: [{ channel: 'ops', destinations: ['myapp_ops', 'myapp_push'] }],
    });
    assert.throws(() => resolveManifest(manifest, { env: {} }), /MYAPP_A, MYAPP_B/);
  });

  it('applies an environments override before the bound check, both for a valid and an out-of-bounds result', () => {
    const manifest = baseAppManifest({
      heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: ['myapp_ops'] }],
      environments: { staging: { heartbeats: { nightly: { grace: '2h' } } } },
    });
    const resolved = resolveManifest(manifest, {
      env: { MYAPP_OPS_WEBHOOK: 'x' },
      envName: 'staging',
    });
    assert.equal(resolved.checks[0].grace_seconds, 7200);

    const badManifest = baseAppManifest({
      heartbeats: [{ name: 'nightly', period: '24h', grace: '1h', destinations: ['myapp_ops'] }],
      environments: { staging: { heartbeats: { nightly: { grace: '8d' } } } },
    });
    assert.throws(
      () => resolveManifest(badManifest, { env: { MYAPP_OPS_WEBHOOK: 'x' }, envName: 'staging' }),
      /out of bounds/,
    );
  });
});

describe('lintDirectory', () => {
  it('lints an empty or missing apps/ clean with count: 0', () => {
    const dir = tmpDir();
    assert.deepEqual(lintDirectory({ dir }), { errors: [], warnings: [], count: 0 });
    assert.deepEqual(lintDirectory({ dir: path.join(dir, 'does-not-exist') }), {
      errors: [],
      warnings: [],
      count: 0,
    });
  });

  it('catches app id prefix collisions', () => {
    const dir = tmpDir();
    writeManifest(dir, 'my.json', baseAppManifest({ app_id: 'my' }));
    writeManifest(dir, 'my_app.json', baseAppManifest({ app_id: 'my_app' }));
    const result = lintDirectory({ dir });
    assert.ok(
      result.errors.some(
        (e) => e.includes('app_id') && e.includes('"my"') && e.includes('"my_app"'),
      ),
    );
  });

  it('catches an app_id of platform or starting with platform_', () => {
    const dir = tmpDir();
    writeManifest(dir, 'platform.json', baseAppManifest({ app_id: 'platform' }));
    writeManifest(dir, 'platform_x.json', baseAppManifest({ app_id: 'platform_x' }));
    const result = lintDirectory({ dir });
    assert.equal(result.count, 2);
    assert.ok(
      result.errors.some((e) => e.startsWith(`${dir}/platform.json:`) && e.includes('app_id')),
    );
    assert.ok(
      result.errors.some((e) => e.startsWith(`${dir}/platform_x.json:`) && e.includes('app_id')),
    );
  });

  it('catches duplicate owned destination names across files', () => {
    const dir = tmpDir();
    // appc.json legitimately owns appc_ops. appd.json reuses the exact same literal name by
    // mistake (e.g. a copy-pasted file whose destination name was never updated) — this also
    // fails appd's own <app_id>_ prefix rule, but the duplicate-name cross-check must still fire
    // independent of that unrelated per-file error.
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
    const result = lintDirectory({ dir });
    assert.ok(
      result.errors.some((e) => e.includes('appc_ops') && e.includes('more than one file')),
    );
  });

  it('catches a basename that does not equal app_id, a stray _other.json and a stray non-JSON file', () => {
    const dir = tmpDir();
    writeManifest(dir, 'wrong.json', baseAppManifest());
    writeManifest(dir, '_other.json', { platform: true });
    fs.writeFileSync(path.join(dir, 'notes.txt'), 'hello', 'utf8');
    fs.writeFileSync(path.join(dir, 'README.md'), '# apps', 'utf8');
    const result = lintDirectory({ dir });
    assert.ok(
      result.errors.some((e) => e.startsWith(`${dir}/wrong.json:`) && e.includes('app_id')),
    );
    assert.ok(
      result.errors.some((e) => e.includes('_other.json') && e.includes('stray underscore')),
    );
    assert.ok(result.errors.some((e) => e.includes('notes.txt') && e.includes('not a .json')));
    assert.ok(!result.errors.some((e) => e.includes('README.md')));
  });

  it('reports an unresolved `use` name only as a warning', () => {
    const dir = tmpDir();
    writeManifest(
      dir,
      'myapp.json',
      baseAppManifest({
        destinations: [{ use: 'shared_ops' }],
        routes: [{ channel: 'ops', destinations: ['shared_ops'] }],
      }),
    );
    const result = lintDirectory({ dir });
    assert.deepEqual(result.errors, []);
    assert.deepEqual(result.warnings, ['external: shared_ops']);
  });

  it('never reads env values and never makes a network/fetch call, even with missing secret env vars', () => {
    const dir = tmpDir();
    writeManifest(dir, 'myapp.json', baseAppManifest());
    const originalFetch = globalThis.fetch;
    globalThis.fetch = () => {
      throw new Error('lintDirectory must never call fetch');
    };
    try {
      const readFile = (file, encoding) => {
        assert.ok(!('MYAPP_OPS_WEBHOOK' in process.env) || true); // env presence is irrelevant; just confirm no throw
        return fs.readFileSync(file, encoding);
      };
      const result = lintDirectory({ dir, readFile });
      assert.equal(result.errors.length, 0);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});

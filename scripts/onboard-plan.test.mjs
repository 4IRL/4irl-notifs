import assert from 'node:assert/strict';
import { describe, it } from 'node:test';

import { computePlan, formatPlan, ownedServerState } from './onboard-plan.mjs';

function destinationView(overrides = {}) {
  return {
    name: 'myapp_ops',
    type: 'discord',
    enabled: true,
    disabled_reason: null,
    config: {},
    secrets: { webhook_url: 'https://discord.com/****' },
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function routeView(overrides = {}) {
  return {
    id: 1,
    app_pattern: 'myapp',
    channel_pattern: 'ops',
    destinations: ['myapp_ops'],
    enabled: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function checkView(overrides = {}) {
  return {
    id: 1,
    app_id: 'myapp',
    name: 'nightly_backup',
    period_seconds: 86400,
    grace_seconds: 3600,
    paused: false,
    state: 'up',
    last_ping_at: null,
    last_ping_kind: null,
    down_since: null,
    deadline_at: '2026-01-02T01:00:00Z',
    destinations: [{ name: 'myapp_ops', type: 'discord', enabled: true }],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function desiredDestination(overrides = {}) {
  return {
    name: 'myapp_ops',
    type: 'discord',
    config: {},
    secrets: { webhook_url: 'https://discord.com/real-webhook' },
    enabled: true,
    ...overrides,
  };
}

function desiredRoute(overrides = {}) {
  return {
    app_pattern: 'myapp',
    channel_pattern: 'ops',
    destinations: ['myapp_ops'],
    enabled: true,
    ...overrides,
  };
}

function desiredCheck(overrides = {}) {
  return {
    app_id: 'myapp',
    name: 'nightly_backup',
    period_seconds: 86400,
    grace_seconds: 3600,
    destinations: ['myapp_ops'],
    ...overrides,
  };
}

function desired(overrides = {}) {
  return {
    destinations: [desiredDestination()],
    shared: [],
    routes: [desiredRoute()],
    checks: [desiredCheck()],
    notices: [],
    ...overrides,
  };
}

const appScope = { kind: 'app', appId: 'myapp', knownAppIds: ['myapp'] };

describe('ownedServerState', () => {
  it('scopes an app to its own routes, checks and <app_id>_-prefixed destinations', () => {
    const server = {
      destinations: [
        destinationView({ name: 'myapp_ops' }),
        destinationView({ name: 'other_ops' }),
      ],
      routes: [routeView({ app_pattern: 'myapp' }), routeView({ app_pattern: 'other', id: 2 })],
      checks: [checkView({ app_id: 'myapp' }), checkView({ app_id: 'other', id: 2 })],
    };
    const owned = ownedServerState(server, appScope);
    assert.deepEqual(
      owned.destinations.map((d) => d.name),
      ['myapp_ops'],
    );
    assert.deepEqual(
      owned.routes.map((r) => r.app_pattern),
      ['myapp'],
    );
    assert.deepEqual(
      owned.checks.map((c) => c.app_id),
      ['myapp'],
    );
  });

  it('scopes the platform to platform_-prefixed destinations and wildcard routes, with no checks', () => {
    const server = {
      destinations: [
        destinationView({ name: 'platform_alerts' }),
        destinationView({ name: 'myapp_ops' }),
      ],
      routes: [routeView({ app_pattern: '*' }), routeView({ app_pattern: 'myapp', id: 2 })],
      checks: [checkView()],
    };
    const owned = ownedServerState(server, { kind: 'platform', knownAppIds: ['myapp'] });
    assert.deepEqual(
      owned.destinations.map((d) => d.name),
      ['platform_alerts'],
    );
    assert.deepEqual(
      owned.routes.map((r) => r.app_pattern),
      ['*'],
    );
    assert.deepEqual(owned.checks, []);
  });

  it('treats a null knownAppIds the same as missing — owns nothing rather than throwing', () => {
    const server = {
      destinations: [destinationView({ name: 'myapp_ops' })],
      routes: [],
      checks: [],
    };
    const scope = { kind: 'app', appId: 'myapp', knownAppIds: null };
    assert.deepEqual(ownedServerState(server, scope).destinations, []);
  });

  it('attributes a prefix-colliding destination to the longer known app id, never the shorter one', () => {
    const server = {
      destinations: [destinationView({ name: 'my_app_ops' })],
      routes: [],
      checks: [],
    };
    const scope = { kind: 'app', appId: 'my', knownAppIds: ['my', 'my_app'] };
    assert.deepEqual(ownedServerState(server, scope).destinations, []);

    const longerScope = { kind: 'app', appId: 'my_app', knownAppIds: ['my', 'my_app'] };
    assert.deepEqual(
      ownedServerState(server, longerScope).destinations.map((d) => d.name),
      ['my_app_ops'],
    );
  });
});

describe('computePlan — ownership exclusion', () => {
  it('never emits an action for another app, a wildcard route or a shared destination, with or without prune', () => {
    const server = {
      destinations: [
        destinationView({ name: 'myapp_ops' }),
        destinationView({ name: 'other_ops' }),
        destinationView({ name: 'shared_ops' }),
      ],
      routes: [
        routeView({ app_pattern: 'myapp' }),
        routeView({ app_pattern: 'other', id: 2 }),
        routeView({ app_pattern: '*', channel_pattern: '*', id: 3, destinations: ['shared_ops'] }),
      ],
      checks: [checkView({ app_id: 'myapp' }), checkView({ app_id: 'other', id: 2 })],
    };
    const d = desired({ routes: [desiredRoute({ destinations: ['myapp_ops', 'shared_ops'] })] });

    for (const prune of [false, true]) {
      const actions = computePlan(d, server, appScope, { prune });
      const targets = actions.map((a) => a.target);
      assert.ok(!targets.includes('other_ops'));
      assert.ok(!targets.includes('shared_ops'));
      assert.ok(!targets.some((t) => t.startsWith('other/')));
      assert.ok(!targets.some((t) => t.startsWith('*/')));
      assert.ok(!actions.some((a) => a.type.endsWith('-check') && a.target.startsWith('other/')));
    }
  });

  it('excludes a server destination owned by a longer-prefix known app id from unmanaged/delete', () => {
    const server = {
      destinations: [destinationView({ name: 'my_app_ops', type: 'discord' })],
      routes: [],
      checks: [],
    };
    const scope = { kind: 'app', appId: 'my', knownAppIds: ['my', 'my_app'] };
    const d = desired({ destinations: [], routes: [], checks: [] });

    for (const prune of [false, true]) {
      const actions = computePlan(d, server, scope, { prune });
      assert.ok(!actions.some((a) => a.target === 'my_app_ops'));
    }
  });
});

describe('computePlan — destination diffing', () => {
  it('creates a destination that does not exist on the server', () => {
    const actions = computePlan(desired(), { destinations: [], routes: [], checks: [] }, appScope);
    assert.deepEqual(
      actions.find((a) => a.type === 'create-destination'),
      {
        type: 'create-destination',
        target: 'myapp_ops',
        body: {
          name: 'myapp_ops',
          type: 'discord',
          enabled: true,
          config: {},
          secrets: { webhook_url: 'https://discord.com/real-webhook' },
        },
      },
    );
  });

  it('produces no action when only the masked secret differs and resendSecrets is unset', () => {
    const server = { destinations: [destinationView()], routes: [], checks: [] };
    const d = desired({ routes: [], checks: [] });
    const actions = computePlan(d, server, appScope);
    assert.deepEqual(actions, []);
  });

  it('with resendSecrets, updates every owned existing destination with all manifest secret keys', () => {
    const server = {
      destinations: [destinationView(), destinationView({ name: 'other_ops' })],
      routes: [],
      checks: [],
    };
    const d = desired({ routes: [], checks: [] });
    const actions = computePlan(d, server, appScope, { resendSecrets: true });
    assert.equal(actions.length, 1);
    assert.equal(actions[0].type, 'update-destination');
    assert.equal(actions[0].target, 'myapp_ops');
    assert.deepEqual(actions[0].body.secrets, { webhook_url: 'https://discord.com/real-webhook' });
  });

  it('with resendSecrets, does not crash on a desired destination with an empty secrets object', () => {
    const server = { destinations: [destinationView({ secrets: {} })], routes: [], checks: [] };
    const d = desired({
      destinations: [desiredDestination({ secrets: {} })],
      routes: [],
      checks: [],
    });
    const actions = computePlan(d, server, appScope, { resendSecrets: true });
    assert.equal(actions.length, 1);
    assert.equal(actions[0].type, 'update-destination');
    assert.deepEqual(actions[0].body.secrets, {});
    assert.deepEqual(actions[0].changes, [{ field: 'secrets', after: ' (resent)' }]);
  });

  it('updates enabled and changed config keys together', () => {
    const server = {
      destinations: [
        destinationView({
          name: 'myapp_push',
          type: 'ntfy',
          config: { topic: 'old-topic' },
          secrets: {},
          enabled: true,
        }),
      ],
      routes: [],
      checks: [],
    };
    const d = desired({
      destinations: [
        desiredDestination({
          name: 'myapp_push',
          type: 'ntfy',
          config: { topic: 'new-topic' },
          secrets: {},
          enabled: false,
        }),
      ],
      routes: [],
      checks: [],
    });
    const actions = computePlan(d, server, appScope);
    assert.equal(actions.length, 1);
    assert.deepEqual(actions[0].body, { enabled: false, config: { topic: 'new-topic' } });
  });

  it('flags a type change as a blocking replace-required', () => {
    const server = {
      destinations: [destinationView({ type: 'ntfy', config: { topic: 't' } })],
      routes: [],
      checks: [],
    };
    const actions = computePlan(desired({ routes: [], checks: [] }), server, appScope);
    assert.deepEqual(actions, [
      { type: 'replace-required', target: 'myapp_ops', reason: 'type: ntfy → discord' },
    ]);
  });

  it('flags a server-only config key as a blocking replace-required', () => {
    const server = {
      destinations: [destinationView({ type: 'ntfy', config: { topic: 't', extra: 'x' } })],
      routes: [],
      checks: [],
    };
    const d = desired({
      destinations: [desiredDestination({ type: 'ntfy', config: { topic: 't' }, secrets: {} })],
      routes: [],
      checks: [],
    });
    const actions = computePlan(d, server, appScope);
    assert.equal(actions.length, 1);
    assert.equal(actions[0].type, 'replace-required');
    assert.match(actions[0].reason, /config\.extra/);
  });

  it('flags a server-only secrets key as a blocking replace-required', () => {
    const server = {
      destinations: [destinationView({ secrets: { webhook_url: '****', extra_secret: '****' } })],
      routes: [],
      checks: [],
    };
    const actions = computePlan(desired({ routes: [], checks: [] }), server, appScope);
    assert.equal(actions.length, 1);
    assert.equal(actions[0].type, 'replace-required');
    assert.match(actions[0].reason, /secrets\.extra_secret/);
  });

  it('flags a missing shared destination as blocking', () => {
    const server = { destinations: [], routes: [], checks: [] };
    const d = desired({ destinations: [], shared: ['shared_ops'], routes: [], checks: [] });
    const actions = computePlan(d, server, appScope);
    assert.deepEqual(actions, [
      {
        type: 'missing-shared',
        target: 'shared_ops',
        reason: 'shared destination "shared_ops" does not exist on the server',
      },
    ]);
  });

  it('does not flag a present shared destination', () => {
    const server = {
      destinations: [destinationView({ name: 'shared_ops' })],
      routes: [],
      checks: [],
    };
    const d = desired({ destinations: [], shared: ['shared_ops'], routes: [], checks: [] });
    const actions = computePlan(d, server, appScope);
    assert.deepEqual(actions, []);
  });

  describe('webhook rotation overlap exception', () => {
    function webhookServer(configOverrides = {}, secretOverrides = {}) {
      return destinationView({
        name: 'myapp_hooks',
        type: 'webhook',
        config: { previous_secret_expires_at: '2026-02-01T00:00:00Z', ...configOverrides },
        secrets: {
          url: '****',
          signing_secret: '****',
          signing_secret_previous: '****',
          ...secretOverrides,
        },
      });
    }

    function webhookDesired() {
      return desiredDestination({
        name: 'myapp_hooks',
        type: 'webhook',
        config: {},
        secrets: { url: 'https://real', signing_secret: 'real-secret' },
      });
    }

    it('produces no replace-required when only the rotation overlap pair is server-only', () => {
      const server = { destinations: [webhookServer()], routes: [], checks: [] };
      const d = desired({ destinations: [webhookDesired()], routes: [], checks: [] });
      const actions = computePlan(d, server, appScope);
      assert.deepEqual(actions, []);
    });

    it('still blocks on an unrelated server-only key on the same webhook destination', () => {
      const server = { destinations: [webhookServer({ unexpected: 'x' })], routes: [], checks: [] };
      const d = desired({ destinations: [webhookDesired()], routes: [], checks: [] });
      const actions = computePlan(d, server, appScope);
      assert.equal(actions.length, 1);
      assert.equal(actions[0].type, 'replace-required');
      assert.match(actions[0].reason, /config\.unexpected/);
    });

    it('still blocks on an unrelated server-only secrets key on the same webhook destination', () => {
      const server = {
        destinations: [webhookServer({}, { other_key: '****' })],
        routes: [],
        checks: [],
      };
      const d = desired({ destinations: [webhookDesired()], routes: [], checks: [] });
      const actions = computePlan(d, server, appScope);
      assert.equal(actions.length, 1);
      assert.equal(actions[0].type, 'replace-required');
      assert.match(actions[0].reason, /secrets\.other_key/);
    });

    it('is not applied to a non-webhook destination carrying the same key names', () => {
      const server = {
        destinations: [
          destinationView({
            name: 'myapp_ops',
            type: 'ntfy',
            config: { topic: 't', previous_secret_expires_at: '2026-02-01T00:00:00Z' },
          }),
        ],
        routes: [],
        checks: [],
      };
      const d = desired({
        destinations: [desiredDestination({ type: 'ntfy', config: { topic: 't' }, secrets: {} })],
        routes: [],
        checks: [],
      });
      const actions = computePlan(d, server, appScope);
      assert.equal(actions.length, 1);
      assert.equal(actions[0].type, 'replace-required');
      assert.match(actions[0].reason, /config\.previous_secret_expires_at/);
    });
  });
});

describe('computePlan — route and check diffing', () => {
  it('creates a route or check that does not exist on the server', () => {
    const actions = computePlan(
      desired({ destinations: [] }),
      { destinations: [], routes: [], checks: [] },
      appScope,
    );
    assert.ok(actions.some((a) => a.type === 'create-route' && a.target === 'myapp/ops'));
    assert.ok(
      actions.some((a) => a.type === 'create-check' && a.target === 'myapp/nightly_backup'),
    );
  });

  it('produces no update when only the order of destinations differs', () => {
    const server = {
      destinations: [],
      routes: [routeView({ destinations: ['b_dest', 'a_dest'] })],
      checks: [
        checkView({
          destinations: [
            { name: 'b_dest', type: 'discord', enabled: true },
            { name: 'a_dest', type: 'discord', enabled: true },
          ],
        }),
      ],
    };
    const d = desired({
      destinations: [],
      routes: [desiredRoute({ destinations: ['a_dest', 'b_dest'] })],
      checks: [desiredCheck({ destinations: ['a_dest', 'b_dest'] })],
    });
    const actions = computePlan(d, server, appScope);
    assert.deepEqual(actions, []);
  });

  it('a route update always carries the full destination list and enabled, even when only enabled changed', () => {
    const server = {
      destinations: [],
      routes: [routeView({ enabled: true, destinations: ['myapp_ops'] })],
      checks: [],
    };
    const d = desired({ destinations: [], routes: [desiredRoute({ enabled: false })], checks: [] });
    const actions = computePlan(d, server, appScope);
    assert.equal(actions.length, 1);
    assert.deepEqual(actions[0].body, { destinations: ['myapp_ops'], enabled: false });
  });

  it('a check update always carries period_seconds, grace_seconds and the full destination list, never app_id/name', () => {
    const server = {
      destinations: [],
      routes: [],
      checks: [checkView({ period_seconds: 86400, grace_seconds: 3600 })],
    };
    const d = desired({
      destinations: [],
      routes: [],
      checks: [desiredCheck({ grace_seconds: 7200 })],
    });
    const actions = computePlan(d, server, appScope);
    assert.equal(actions.length, 1);
    assert.deepEqual(actions[0].body, {
      period_seconds: 86400,
      grace_seconds: 7200,
      destinations: ['myapp_ops'],
    });
    assert.ok(!('app_id' in actions[0].body));
    assert.ok(!('name' in actions[0].body));
  });
});

describe('computePlan — unmanaged and prune ordering', () => {
  it('flags owned-but-undeclared objects as unmanaged when not pruning, in checks, routes, destinations order', () => {
    const server = {
      destinations: [destinationView({ name: 'myapp_stale' })],
      routes: [routeView({ channel_pattern: 'stale' })],
      checks: [checkView({ name: 'stale_check' })],
    };
    const d = desired({ destinations: [], routes: [], checks: [] });
    const actions = computePlan(d, server, appScope);
    assert.deepEqual(
      actions.map((a) => a.type),
      ['unmanaged-check', 'unmanaged-route', 'unmanaged-destination'],
    );
  });

  it('with prune, emits deletes in checks, routes, destinations order, after creates/updates', () => {
    const server = {
      destinations: [destinationView({ name: 'myapp_stale' })],
      routes: [routeView({ channel_pattern: 'stale' })],
      checks: [checkView({ name: 'stale_check' })],
    };
    const d = desired();
    const actions = computePlan(d, server, appScope, { prune: true });
    const types = actions.map((a) => a.type);
    assert.deepEqual(types, [
      'create-destination',
      'create-route',
      'create-check',
      'delete-check',
      'delete-route',
      'delete-destination',
    ]);
  });
});

describe('formatPlan', () => {
  it('renders exactly "No changes." for an empty action list', () => {
    assert.equal(formatPlan([]), 'No changes.');
  });

  it('never prints a resolved secret value', () => {
    const server = { destinations: [destinationView()], routes: [], checks: [] };
    const d = desired({ routes: [], checks: [] });
    const actions = computePlan(d, server, appScope, { resendSecrets: true });
    const text = formatPlan(actions);
    assert.ok(!text.includes('https://discord.com/real-webhook'));
    assert.match(text, /secrets: webhook_url \(resent\)/);
  });

  it('prints a summary line with create/update/delete/unmanaged counts', () => {
    const server = { destinations: [], routes: [], checks: [] };
    const actions = computePlan(desired(), server, appScope);
    const text = formatPlan(actions);
    assert.match(text, /3 to create, 0 to update, 0 to delete, 0 unmanaged$/);
  });
});

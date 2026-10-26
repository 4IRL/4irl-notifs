import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from './client';
import { createDeliveryApiClient } from './deliveryClient';

describe('delivery api client', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
  });

  function jsonResponse({ status = 200, body }: { status?: number; body: unknown }): Response {
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  function makeClient() {
    return createDeliveryApiClient({ baseUrl: 'https://admin.test/', fetchImpl: fetchMock });
  }

  function lastCall(): { url: string; init: RequestInit; headers: Record<string, string> } {
    const [url, init] = fetchMock.mock.calls[fetchMock.mock.calls.length - 1] as [
      string,
      RequestInit,
    ];
    return { url, init, headers: (init.headers ?? {}) as Record<string, string> };
  }

  const destinationWire = {
    name: 'ops_webhook',
    type: 'webhook',
    enabled: true,
    disabled_reason: null,
    config: { url: 'https://example.com/hook' },
    secrets: { signing_secret: '****' },
    owner_kind: 'operator',
    owner_app_id: null,
    created_at: '2026-10-17T10:00:00Z',
    updated_at: '2026-10-17T11:00:00Z',
  };

  const routeWire = {
    id: 7,
    app_pattern: 'urls4irl',
    channel_pattern: '*',
    destinations: ['ops_webhook'],
    enabled: true,
    owner_kind: 'operator',
    owner_app_id: null,
    created_at: '2026-10-17T10:00:00Z',
    updated_at: '2026-10-17T11:00:00Z',
  };

  const deliveryWire = {
    id: 42,
    message_id: 'msg-1',
    app_id: 'urls4irl',
    channel: 'alerts',
    destination: 'ops_webhook',
    destination_type: 'webhook',
    state: 'dead',
    dead_reason: 'exhausted',
    attempt_count: 5,
    last_status_code: 500,
    next_attempt_at: null,
    delivered_at: null,
    created_at: '2026-10-17T10:00:00Z',
    updated_at: '2026-10-17T11:00:00Z',
  };

  it('listDestinations GETs through the /delivery prefix and maps rows', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ body: { destinations: [destinationWire] } }));

    const destinations = await makeClient().listDestinations();

    const { url, init, headers } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/destinations');
    expect(init.method).toBe('GET');
    expect(init.credentials).toBe('include');
    expect(headers['X-Requested-With']).toBeUndefined();
    expect(destinations).toEqual([
      {
        name: 'ops_webhook',
        type: 'webhook',
        enabled: true,
        disabledReason: null,
        config: { url: 'https://example.com/hook' },
        secrets: { signing_secret: '****' },
        ownerKind: 'operator',
        ownerAppId: null,
        createdAt: '2026-10-17T10:00:00Z',
        updatedAt: '2026-10-17T11:00:00Z',
      },
    ]);
  });

  describe('owner mapping', () => {
    const checkWireBase = {
      id: 1,
      app_id: 'chores4irl',
      name: 'daily',
      period_seconds: 60,
      grace_seconds: 60,
      paused: false,
      state: 'up',
      last_ping_at: null,
      last_ping_kind: null,
      down_since: null,
      deadline_at: '2026-10-19T20:02:00Z',
      destinations: [],
      created_at: '2026-10-17T10:00:00Z',
      updated_at: '2026-10-17T10:00:00Z',
    };

    /** Every list call the client has, with its wire row (owner fields removed) and reader. */
    const lists = [
      {
        name: 'destinations',
        wire: destinationWire,
        respond: (row: unknown) => ({ destinations: [row] }),
        read: () => makeClient().listDestinations(),
      },
      {
        name: 'routes',
        wire: routeWire,
        respond: (row: unknown) => ({ routes: [row] }),
        read: () => makeClient().listRoutes(),
      },
      {
        name: 'checks',
        wire: checkWireBase,
        respond: (row: unknown) => ({ checks: [row] }),
        read: () => makeClient().listHeartbeats(),
      },
    ];

    function withOwner({ wire, owner }: { wire: object; owner: object }): Record<string, unknown> {
      const bare: Record<string, unknown> = { ...wire };
      delete bare.owner_kind;
      delete bare.owner_app_id;
      return { ...bare, ...owner };
    }

    it.each(lists)('maps an app owner on $name', async ({ wire, respond, read }) => {
      const row = withOwner({ wire, owner: { owner_kind: 'app', owner_app_id: 'chores4irl' } });
      fetchMock.mockResolvedValueOnce(jsonResponse({ body: respond(row) }));

      expect((await read())[0]).toMatchObject({ ownerKind: 'app', ownerAppId: 'chores4irl' });
    });

    it.each(lists)('maps an operator owner on $name', async ({ wire, respond, read }) => {
      const row = withOwner({ wire, owner: { owner_kind: 'operator', owner_app_id: null } });
      fetchMock.mockResolvedValueOnce(jsonResponse({ body: respond(row) }));

      expect((await read())[0]).toMatchObject({ ownerKind: 'operator', ownerAppId: null });
    });

    const malformedOwners: Array<[string, object]> = [
      ['absent fields', {}],
      ['an unrecognised kind', { owner_kind: 'robot', owner_app_id: null }],
      ['an empty kind', { owner_kind: '', owner_app_id: null }],
      ['a null kind', { owner_kind: null, owner_app_id: null }],
      ['an app owner without an id', { owner_kind: 'app', owner_app_id: null }],
      ['an app owner with an empty id', { owner_kind: 'app', owner_app_id: '' }],
      ['an operator owner carrying an app id', { owner_kind: 'operator', owner_app_id: 'x' }],
    ];

    describe.each(lists)('$name', ({ wire, respond, read }) => {
      it.each(malformedOwners)('reads %s as unknown, never operator', async (_label, owner) => {
        fetchMock.mockResolvedValueOnce(
          jsonResponse({ body: respond(withOwner({ wire, owner })) }),
        );

        const [row] = await read();

        expect(row.ownerKind).toBe('unknown');
        expect(row.ownerAppId).toBeNull();
      });
    });
  });

  it('coerces null config and secrets to empty maps', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        body: { destinations: [{ ...destinationWire, config: null, secrets: null }] },
      }),
    );

    const [destination] = await makeClient().listDestinations();

    expect(destination.config).toEqual({});
    expect(destination.secrets).toEqual({});
  });

  it('createDestination POSTs the body with guard headers', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 201, body: { destination: destinationWire } }),
    );

    const created = await makeClient().createDestination({
      name: 'ops_webhook',
      type: 'webhook',
      enabled: false,
      config: { url: 'https://example.com/hook' },
      secrets: { signing_secret: 's3cret' },
    });

    const { url, init, headers } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/destinations');
    expect(init.method).toBe('POST');
    expect(headers['X-Requested-With']).toBeTruthy();
    expect(headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(init.body as string)).toEqual({
      name: 'ops_webhook',
      type: 'webhook',
      enabled: false,
      config: { url: 'https://example.com/hook' },
      secrets: { signing_secret: 's3cret' },
    });
    expect(created.name).toBe('ops_webhook');
  });

  it('updateDestination with only enabled sends exactly {"enabled":false}', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ body: { destination: destinationWire } }));

    await makeClient().updateDestination({ name: 'ops_webhook', enabled: false });

    const { url, init } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/destinations/ops_webhook');
    expect(init.method).toBe('PATCH');
    expect(init.body).toBe('{"enabled":false}');
  });

  it('updateDestination sends only provided keys and never name or type', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ body: { destination: destinationWire } }));

    await makeClient().updateDestination({
      name: 'ops_webhook',
      config: { url: 'https://example.com/new' },
    });

    const body = JSON.parse(lastCall().init.body as string);
    expect(body).toEqual({ config: { url: 'https://example.com/new' } });
    expect(body).not.toHaveProperty('name');
    expect(body).not.toHaveProperty('type');
  });

  it('deleteDestination DELETEs without a body or Content-Type', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ body: { name: 'ops_webhook', deleted: true } }));

    await makeClient().deleteDestination({ name: 'ops_webhook' });

    const { url, init, headers } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/destinations/ops_webhook');
    expect(init.method).toBe('DELETE');
    expect(init.body).toBeUndefined();
    expect(headers['Content-Type']).toBeUndefined();
    expect(headers['X-Requested-With']).toBeTruthy();
  });

  it('rotateSecret POSTs overlap_hours and returns the one-time secret', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ body: { destination: destinationWire, signing_secret: 'new-secret' } }),
    );

    const result = await makeClient().rotateSecret({ name: 'ops_webhook', overlapHours: 24 });

    const { url, init } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/destinations/ops_webhook/rotate-secret');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ overlap_hours: 24 });
    expect(result.signingSecret).toBe('new-secret');
    expect(result.destination.name).toBe('ops_webhook');
  });

  it('sendTest posts the exact envelope with ?wait=10 and a fresh key per call', async () => {
    fetchMock.mockImplementation(async () =>
      jsonResponse({
        status: 201,
        body: {
          message: {
            message_id: 'msg-9',
            status: 'delivered',
            deliveries: [
              {
                id: 1,
                destination: 'ops_webhook',
                state: 'delivered',
                dead_reason: null,
                attempt_count: 1,
                last_status_code: 200,
                next_attempt_at: null,
                delivered_at: '2026-10-17T12:00:00Z',
              },
            ],
          },
          duplicate: false,
        },
      }),
    );
    const client = makeClient();

    const summary = await client.sendTest({ destination: 'ops_webhook' });
    const first = lastCall();
    await client.sendTest({ destination: 'ops_webhook' });
    const second = lastCall();

    expect(first.url).toBe('https://admin.test/delivery/v1/admin/messages?wait=10');
    expect(first.init.method).toBe('POST');
    const firstBody = JSON.parse(first.init.body as string);
    const secondBody = JSON.parse(second.init.body as string);
    expect(firstBody).toEqual({
      app_id: 'admin_test',
      to: { destination: 'ops_webhook' },
      channel: 'test',
      idempotency_key: firstBody.idempotency_key,
      payload: { source: 'admin-ui' },
      display: { title: 'Test from 4IRL admin', body: 'Test delivery to ops_webhook' },
    });
    expect(firstBody.idempotency_key).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    );
    expect(secondBody.idempotency_key).not.toBe(firstBody.idempotency_key);
    expect(summary).toEqual({
      messageId: 'msg-9',
      status: 'delivered',
      deliveries: [
        {
          id: 1,
          destination: 'ops_webhook',
          state: 'delivered',
          deadReason: null,
          attemptCount: 1,
          lastStatusCode: 200,
          nextAttemptAt: null,
          deliveredAt: '2026-10-17T12:00:00Z',
        },
      ],
    });
  });

  it('route methods use the route id in the path and map rows', async () => {
    const client = makeClient();

    fetchMock.mockResolvedValue(jsonResponse({ body: { routes: [routeWire] } }));
    const routes = await client.listRoutes();
    expect(lastCall().url).toBe('https://admin.test/delivery/v1/admin/routes');
    expect(routes[0]).toMatchObject({ id: 7, appPattern: 'urls4irl', channelPattern: '*' });

    fetchMock.mockResolvedValue(jsonResponse({ status: 201, body: { route: routeWire } }));
    await client.createRoute({
      appPattern: 'urls4irl',
      channelPattern: '*',
      destinations: ['ops_webhook'],
    });
    expect(JSON.parse(lastCall().init.body as string)).toEqual({
      app_pattern: 'urls4irl',
      channel_pattern: '*',
      destinations: ['ops_webhook'],
    });

    fetchMock.mockResolvedValue(jsonResponse({ body: { route: routeWire } }));
    await client.updateRoute({ id: 7, enabled: false });
    expect(lastCall().url).toBe('https://admin.test/delivery/v1/admin/routes/7');
    expect(lastCall().init.method).toBe('PATCH');
    expect(lastCall().init.body).toBe('{"enabled":false}');

    fetchMock.mockResolvedValue(jsonResponse({ body: { id: 7, deleted: true } }));
    await client.deleteRoute({ id: 7 });
    expect(lastCall().url).toBe('https://admin.test/delivery/v1/admin/routes/7');
    expect(lastCall().init.method).toBe('DELETE');
  });

  it('listDeliveries defaults the limit to 50 and maps the enriched fields', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ body: { deliveries: [deliveryWire] } }));

    const deliveries = await makeClient().listDeliveries({ state: 'dead' });

    expect(lastCall().url).toBe(
      'https://admin.test/delivery/v1/admin/deliveries?state=dead&limit=50',
    );
    expect(deliveries[0]).toMatchObject({
      id: 42,
      messageId: 'msg-1',
      appId: 'urls4irl',
      channel: 'alerts',
      destinationType: 'webhook',
      deadReason: 'exhausted',
      attemptCount: 5,
      lastStatusCode: 500,
    });
  });

  it('listDeliveries honors an explicit limit', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ body: { deliveries: [] } }));

    await makeClient().listDeliveries({ state: 'pending', limit: 10 });

    expect(lastCall().url).toContain('state=pending&limit=10');
  });

  it('listAttempts and replayDelivery hit the delivery paths', async () => {
    const client = makeClient();

    fetchMock.mockResolvedValue(
      jsonResponse({
        body: {
          attempts: [
            {
              attempt_number: 1,
              outcome: 'retry',
              status_code: 500,
              latency_ms: 120,
              error: null,
              response_snippet: 'boom',
              started_at: '2026-10-17T10:00:00Z',
            },
          ],
        },
      }),
    );
    const attempts = await client.listAttempts({ deliveryId: 42 });
    expect(lastCall().url).toBe('https://admin.test/delivery/v1/admin/deliveries/42/attempts');
    expect(attempts[0]).toEqual({
      attemptNumber: 1,
      outcome: 'retry',
      statusCode: 500,
      latencyMs: 120,
      error: null,
      responseSnippet: 'boom',
      startedAt: '2026-10-17T10:00:00Z',
    });

    fetchMock.mockResolvedValue(
      jsonResponse({ body: { delivery: { ...deliveryWire, state: 'pending' } } }),
    );
    const replayed = await client.replayDelivery({ deliveryId: 42 });
    const { url, init, headers } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/deliveries/42/replay');
    expect(init.method).toBe('POST');
    expect(init.body).toBeUndefined();
    expect(headers['X-Requested-With']).toBeTruthy();
    expect(headers['Content-Type']).toBeUndefined();
    expect(replayed.state).toBe('pending');
  });

  it('listScheduledMessages maps the wire shape and coerces a null list to []', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        body: {
          scheduled_messages: [
            {
              message_id: 'msg-9',
              app_id: 'chores4irl',
              channel: 'reminder',
              idempotency_key: 'chore-1',
              deliver_at: '2026-10-18T19:00:00Z',
              created_at: '2026-10-17T21:30:00Z',
              recipient: false,
              destinations: [{ name: 'ops_discord', type: 'discord' }],
            },
            {
              message_id: 'msg-10',
              app_id: 'chores4irl',
              channel: 'reminder',
              idempotency_key: 'chore-2',
              deliver_at: '2026-10-19T19:00:00Z',
              created_at: '2026-10-17T21:31:00Z',
              recipient: true,
              destinations: null,
            },
          ],
          total: 312,
        },
      }),
    );

    const list = await makeClient().listScheduledMessages({ limit: 50 });

    const { url, init } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/scheduled-messages?limit=50');
    expect(init.method).toBe('GET');
    expect(list.total).toBe(312);
    expect(list.items[0]).toEqual({
      messageId: 'msg-9',
      appId: 'chores4irl',
      channel: 'reminder',
      idempotencyKey: 'chore-1',
      deliverAt: '2026-10-18T19:00:00Z',
      createdAt: '2026-10-17T21:30:00Z',
      recipient: false,
      destinations: [{ name: 'ops_discord', type: 'discord' }],
    });
    expect(list.items[1].recipient).toBe(true);
    expect(list.items[1].destinations).toEqual([]);
  });

  it('listScheduledMessages defaults the limit and coerces a null list', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ body: { scheduled_messages: null, total: 0 } }));

    const list = await makeClient().listScheduledMessages({});

    expect(lastCall().url).toContain('/v1/admin/scheduled-messages?limit=50');
    expect(list).toEqual({ items: [], total: 0 });
  });

  it('cancelScheduledMessage sends DELETE and maps the outcome', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ body: { message_id: 'msg-9', cancelled: true, status: 'cancelled' } }),
    );

    const result = await makeClient().cancelScheduledMessage({ id: 'msg-9' });

    const { url, init, headers } = lastCall();
    expect(url).toBe('https://admin.test/delivery/v1/admin/scheduled-messages/msg-9');
    expect(init.method).toBe('DELETE');
    expect(init.body).toBeUndefined();
    expect(headers['X-Requested-With']).toBeTruthy();
    expect(result).toEqual({ messageId: 'msg-9', cancelled: true, status: 'cancelled' });
  });

  it('cancelScheduledMessage surfaces a rejected request as ApiError', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 502, body: { error: 'upstream down' } }));

    await expect(makeClient().cancelScheduledMessage({ id: 'msg-9' })).rejects.toMatchObject({
      constructor: ApiError,
      status: 502,
      message: 'upstream down',
    });
  });

  it('throws ApiError with the server message on failure', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 409, body: { error: 'destination already exists' } }),
    );

    await expect(
      makeClient().createDestination({ name: 'a', type: 'ntfy', config: {}, secrets: {} }),
    ).rejects.toMatchObject({
      constructor: ApiError,
      status: 409,
      message: 'destination already exists',
    });
  });

  it('throws ApiError with the request failed (N) fallback when the body is not JSON', async () => {
    fetchMock.mockResolvedValue(new Response('<html>', { status: 502 }));

    await expect(makeClient().listRoutes()).rejects.toMatchObject({
      status: 502,
      message: 'request failed (502)',
    });
  });

  describe('heartbeats', () => {
    const checkWire = {
      id: 12,
      app_id: 'minecrafting',
      name: 'daily_report',
      period_seconds: 86400,
      grace_seconds: 7200,
      paused: false,
      state: 'late',
      last_ping_at: '2026-10-18T18:02:00Z',
      last_ping_kind: 'fail',
      down_since: null,
      deadline_at: '2026-10-19T20:02:00Z',
      destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
      owner_kind: 'operator',
      owner_app_id: null,
      created_at: '2026-10-17T10:00:00Z',
      updated_at: '2026-10-18T18:02:00Z',
    };

    const checkSummary = {
      id: 12,
      appId: 'minecrafting',
      name: 'daily_report',
      periodSeconds: 86400,
      graceSeconds: 7200,
      paused: false,
      state: 'late',
      lastPingAt: '2026-10-18T18:02:00Z',
      lastPingKind: 'fail',
      downSince: null,
      deadlineAt: '2026-10-19T20:02:00Z',
      destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
      ownerKind: 'operator',
      ownerAppId: null,
      downReason: null,
      probe: null,
      createdAt: '2026-10-17T10:00:00Z',
      updatedAt: '2026-10-18T18:02:00Z',
    };

    it('listHeartbeats GETs the admin list and maps snake_case to camelCase', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ body: { checks: [checkWire] } }));

      const checks = await makeClient().listHeartbeats();

      const { url, init, headers } = lastCall();
      expect(url).toBe('https://admin.test/delivery/v1/admin/heartbeats');
      expect(init.method).toBe('GET');
      expect(headers['X-Requested-With']).toBeUndefined();
      expect(checks).toEqual([checkSummary]);
    });

    it('listHeartbeats maps a null list and null destinations to empty arrays', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ body: { checks: null } }));
      expect(await makeClient().listHeartbeats()).toEqual([]);

      fetchMock.mockResolvedValueOnce(
        jsonResponse({ body: { checks: [{ ...checkWire, destinations: null }] } }),
      );
      const [check] = await makeClient().listHeartbeats();
      expect(check.destinations).toEqual([]);
    });

    it('createHeartbeat POSTs the snake_case body with the CSRF header', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 201, body: { check: checkWire } }));

      const created = await makeClient().createHeartbeat({
        kind: 'push',
        appId: 'minecrafting',
        name: 'daily_report',
        periodSeconds: 86400,
        graceSeconds: 7200,
        destinations: ['ops_discord'],
      });

      const { url, init, headers } = lastCall();
      expect(url).toBe('https://admin.test/delivery/v1/admin/heartbeats');
      expect(init.method).toBe('POST');
      expect(headers['X-Requested-With']).toBe('admin-ui');
      expect(JSON.parse(init.body as string)).toEqual({
        app_id: 'minecrafting',
        name: 'daily_report',
        period_seconds: 86400,
        grace_seconds: 7200,
        destinations: ['ops_discord'],
      });
      expect(created).toEqual(checkSummary);
    });

    it('updateHeartbeat PATCHes only the provided fields and never sends app_id or name', async () => {
      fetchMock.mockImplementation(() =>
        Promise.resolve(jsonResponse({ body: { check: checkWire } })),
      );

      await makeClient().updateHeartbeat({ id: 12, graceSeconds: 3600 });

      const { url, init } = lastCall();
      expect(url).toBe('https://admin.test/delivery/v1/admin/heartbeats/12');
      expect(init.method).toBe('PATCH');
      expect(lastCall().headers['X-Requested-With']).toBe('admin-ui');
      expect(JSON.parse(init.body as string)).toEqual({ grace_seconds: 3600 });

      await makeClient().updateHeartbeat({
        id: 12,
        periodSeconds: 60,
        graceSeconds: 120,
        destinations: ['a', 'b'],
      });
      expect(JSON.parse(lastCall().init.body as string)).toEqual({
        period_seconds: 60,
        grace_seconds: 120,
        destinations: ['a', 'b'],
      });
    });

    it('deleteHeartbeat DELETEs by id', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ body: { id: 12, deleted: true } }));

      await makeClient().deleteHeartbeat({ id: 12 });

      const { url, init, headers } = lastCall();
      expect(url).toBe('https://admin.test/delivery/v1/admin/heartbeats/12');
      expect(init.method).toBe('DELETE');
      expect(headers['X-Requested-With']).toBe('admin-ui');
      expect(init.body).toBeUndefined();
    });

    it('pauseHeartbeat and resumeHeartbeat POST the action path and map the check', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ body: { check: { ...checkWire, paused: true } } }),
      );
      const paused = await makeClient().pauseHeartbeat({ id: 12 });
      expect(lastCall().url).toBe('https://admin.test/delivery/v1/admin/heartbeats/12/pause');
      expect(lastCall().init.method).toBe('POST');
      expect(lastCall().headers['X-Requested-With']).toBe('admin-ui');
      expect(paused.paused).toBe(true);

      fetchMock.mockResolvedValue(jsonResponse({ body: { check: checkWire } }));
      const resumed = await makeClient().resumeHeartbeat({ id: 12 });
      expect(lastCall().url).toBe('https://admin.test/delivery/v1/admin/heartbeats/12/resume');
      expect(lastCall().init.method).toBe('POST');
      expect(resumed.paused).toBe(false);
    });

    it('throws ApiError with the server message on a heartbeat failure', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ status: 404, body: { error: 'heartbeat check not found' } }),
      );

      await expect(makeClient().pauseHeartbeat({ id: 99 })).rejects.toMatchObject({
        constructor: ApiError,
        status: 404,
        message: 'heartbeat check not found',
      });
    });
  });

  describe('app identities', () => {
    const requestWire = {
      id: 7,
      app_id: 'urls4irl',
      github_repository: '4IRL/urls4irl',
      github_repository_id: 812345678,
      workflow_path: '.github/workflows/prod-deploy.yml',
      git_ref: 'refs/heads/main',
      environment: 'production',
      github_run_id: '14022',
      status: 'pending',
      created_at: '2026-10-21T18:04:00Z',
      updated_at: '2026-10-21T18:04:00Z',
    };

    const requestSummary = {
      id: 7,
      appId: 'urls4irl',
      githubRepository: '4IRL/urls4irl',
      githubRepositoryId: 812345678,
      workflowPath: '.github/workflows/prod-deploy.yml',
      gitRef: 'refs/heads/main',
      environment: 'production',
      githubRunId: '14022',
      status: 'pending',
      createdAt: '2026-10-21T18:04:00Z',
      updatedAt: '2026-10-21T18:04:00Z',
    };

    const keyWire = {
      id: 3,
      app_id: 'urls4irl',
      key_hint: 'dak_7Q2m',
      state: 'active',
      created_at: '2026-10-21T16:40:00Z',
      superseded_at: null,
      revoked_at: null,
      revoked_reason: null,
      last_used_at: '2026-10-21T18:00:00Z',
    };

    const keySummary = {
      id: 3,
      appId: 'urls4irl',
      keyHint: 'dak_7Q2m',
      state: 'active',
      createdAt: '2026-10-21T16:40:00Z',
      supersededAt: null,
      revokedAt: null,
      revokedReason: null,
      lastUsedAt: '2026-10-21T18:00:00Z',
    };

    const bindingWire = {
      app_id: 'urls4irl',
      github_repository: '4IRL/urls4irl',
      github_repository_id: 812345678,
      github_owner_id: 55,
      workflow_path: '.github/workflows/prod-deploy.yml',
      git_ref: 'refs/heads/main',
      environment: null,
      approved_at: '2026-10-21T18:06:00Z',
      keys: [keyWire],
    };

    const bindingSummary = {
      appId: 'urls4irl',
      githubRepository: '4IRL/urls4irl',
      githubRepositoryId: 812345678,
      githubOwnerId: 55,
      workflowPath: '.github/workflows/prod-deploy.yml',
      gitRef: 'refs/heads/main',
      environment: null,
      approvedAt: '2026-10-21T18:06:00Z',
      keys: [keySummary],
    };

    it('listIdentityRequests GETs the admin list and maps snake_case to camelCase', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ body: { requests: [requestWire] } }));

      const requests = await makeClient().listIdentityRequests();

      const { url, init, headers } = lastCall();
      expect(url).toBe('https://admin.test/delivery/v1/admin/app-identity/requests');
      expect(init.method).toBe('GET');
      expect(headers['X-Requested-With']).toBeUndefined();
      expect(requests).toEqual([requestSummary]);
    });

    it('listIdentityRequests maps a null list to an empty array and a missing environment to null', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ body: { requests: null } }));
      expect(await makeClient().listIdentityRequests()).toEqual([]);

      const { environment: omitted, ...withoutEnvironment } = requestWire;
      expect(omitted).toBe('production');
      fetchMock.mockResolvedValueOnce(jsonResponse({ body: { requests: [withoutEnvironment] } }));
      const [mapped] = await makeClient().listIdentityRequests();
      expect(mapped.environment).toBeNull();
    });

    it('maps an unrecognised request status or key state to unknown (fail closed)', async () => {
      fetchMock.mockResolvedValueOnce(
        jsonResponse({ body: { requests: [{ ...requestWire, status: 'approved' }] } }),
      );
      const [request] = await makeClient().listIdentityRequests();
      expect(request.status).toBe('unknown');

      fetchMock.mockResolvedValueOnce(
        jsonResponse({
          body: { bindings: [{ ...bindingWire, keys: [{ ...keyWire, state: 'frozen' }] }] },
        }),
      );
      const [binding] = await makeClient().listIdentityBindings();
      expect(binding.keys[0].state).toBe('unknown');
    });

    it('approveIdentityRequest POSTs the approve path and maps the binding', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ status: 201, body: { binding: { ...bindingWire, keys: [] } } }),
      );

      const binding = await makeClient().approveIdentityRequest({ id: 7 });

      const { url, init, headers } = lastCall();
      expect(url).toBe('https://admin.test/delivery/v1/admin/app-identity/requests/7/approve');
      expect(init.method).toBe('POST');
      expect(headers['X-Requested-With']).toBe('admin-ui');
      expect(init.body).toBeUndefined();
      expect(binding).toEqual({ ...bindingSummary, keys: [] });
    });

    it('rejectIdentityRequest POSTs the reject path and maps the request', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ body: { request: { ...requestWire, status: 'rejected' } } }),
      );

      const rejected = await makeClient().rejectIdentityRequest({ id: 7 });

      expect(lastCall().url).toBe(
        'https://admin.test/delivery/v1/admin/app-identity/requests/7/reject',
      );
      expect(lastCall().init.method).toBe('POST');
      expect(lastCall().headers['X-Requested-With']).toBe('admin-ui');
      expect(rejected).toEqual({ ...requestSummary, status: 'rejected' });
    });

    it('listIdentityBindings GETs the admin list and maps bindings with their keys', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ body: { bindings: [bindingWire] } }));

      const bindings = await makeClient().listIdentityBindings();

      expect(lastCall().url).toBe('https://admin.test/delivery/v1/admin/app-identity/bindings');
      expect(lastCall().init.method).toBe('GET');
      expect(bindings).toEqual([bindingSummary]);
    });

    it('listIdentityBindings maps null bindings and null keys to empty arrays', async () => {
      fetchMock.mockResolvedValueOnce(jsonResponse({ body: { bindings: null } }));
      expect(await makeClient().listIdentityBindings()).toEqual([]);

      fetchMock.mockResolvedValueOnce(
        jsonResponse({ body: { bindings: [{ ...bindingWire, keys: null }] } }),
      );
      const [binding] = await makeClient().listIdentityBindings();
      expect(binding.keys).toEqual([]);
    });

    it('never maps a key value, even one a misbehaving server sends', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            bindings: [{ ...bindingWire, keys: [{ ...keyWire, key: 'dak_SECRETSECRETSECRET' }] }],
          },
        }),
      );

      const bindings = await makeClient().listIdentityBindings();

      expect(JSON.stringify(bindings)).not.toContain('SECRETSECRET');
      expect(Object.keys(bindings[0].keys[0]).sort()).toEqual(Object.keys(keySummary).sort());
    });

    it('revokeIdentityKey POSTs the revoke path and maps the key', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            key: {
              ...keyWire,
              state: 'revoked',
              revoked_at: '2026-10-22T09:00:00Z',
              revoked_reason: 'operator',
            },
          },
        }),
      );

      const revoked = await makeClient().revokeIdentityKey({ id: 3 });

      expect(lastCall().url).toBe(
        'https://admin.test/delivery/v1/admin/app-identity/keys/3/revoke',
      );
      expect(lastCall().init.method).toBe('POST');
      expect(lastCall().headers['X-Requested-With']).toBe('admin-ui');
      expect(revoked).toEqual({
        ...keySummary,
        state: 'revoked',
        revokedAt: '2026-10-22T09:00:00Z',
        revokedReason: 'operator',
      });
    });

    it('unbindApp DELETEs the binding path and maps the result', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ body: { app_id: 'urls4irl', deleted: true } }));

      const result = await makeClient().unbindApp({ appId: 'urls4irl' });

      expect(lastCall().url).toBe(
        'https://admin.test/delivery/v1/admin/app-identity/bindings/urls4irl',
      );
      expect(lastCall().init.method).toBe('DELETE');
      expect(lastCall().headers['X-Requested-With']).toBe('admin-ui');
      expect(result).toEqual({ appId: 'urls4irl', deleted: true });
    });

    it('unbindApp percent-encodes the app id and maps deleted:false', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ body: { app_id: 'a b', deleted: false } }));

      const result = await makeClient().unbindApp({ appId: 'a b' });

      expect(lastCall().url).toBe(
        'https://admin.test/delivery/v1/admin/app-identity/bindings/a%20b',
      );
      expect(result).toEqual({ appId: 'a b', deleted: false });
    });

    it('throws ApiError with the server message on a 409 approve', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ status: 409, body: { error: 'request is not pending' } }),
      );

      await expect(makeClient().approveIdentityRequest({ id: 7 })).rejects.toMatchObject({
        constructor: ApiError,
        status: 409,
        message: 'request is not pending',
      });
    });
  });

  describe('getReadiness', () => {
    it('maps 200 ok', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            status: 'ok',
            schema_version: 5,
            expected_schema_version: 5,
            last_migration_failure: null,
            heartbeat_checker: { last_tick_at: '2026-10-19T09:00:00Z', stale: false },
          },
        }),
      );

      expect(await makeClient().getReadiness()).toEqual({
        kind: 'ok',
        schemaVersion: 5,
        expectedSchemaVersion: 5,
        lastMigrationFailure: null,
        heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
      });
      expect(lastCall().url).toBe('https://admin.test/delivery/readyz');
    });

    it('maps a stale heartbeat checker with a last tick', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            status: 'ok',
            schema_version: 5,
            expected_schema_version: 5,
            last_migration_failure: null,
            heartbeat_checker: { last_tick_at: '2026-10-19T08:50:00Z', stale: true },
          },
        }),
      );

      expect(await makeClient().getReadiness()).toMatchObject({
        kind: 'ok',
        heartbeatChecker: { lastTickAt: '2026-10-19T08:50:00Z', stale: true },
      });
    });

    it('maps a never-ticked heartbeat checker (null last tick, stale)', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            status: 'ok',
            schema_version: 5,
            expected_schema_version: 5,
            last_migration_failure: null,
            heartbeat_checker: { last_tick_at: null, stale: true },
          },
        }),
      );

      expect(await makeClient().getReadiness()).toMatchObject({
        heartbeatChecker: { lastTickAt: null, stale: true },
      });
    });

    it('treats stale:false without a last tick as stale (fail closed)', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            status: 'ok',
            schema_version: 5,
            expected_schema_version: 5,
            last_migration_failure: null,
            heartbeat_checker: { last_tick_at: null, stale: false },
          },
        }),
      );

      expect(await makeClient().getReadiness()).toMatchObject({
        heartbeatChecker: { lastTickAt: null, stale: true },
      });
    });

    it('treats an absent heartbeat_checker as stale instead of throwing', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            status: 'ok',
            schema_version: 5,
            expected_schema_version: 5,
            last_migration_failure: null,
          },
        }),
      );

      expect(await makeClient().getReadiness()).toMatchObject({
        kind: 'ok',
        heartbeatChecker: { lastTickAt: null, stale: true },
      });
    });

    it('treats a malformed heartbeat_checker as stale', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            status: 'ok',
            schema_version: 5,
            expected_schema_version: 5,
            last_migration_failure: null,
            heartbeat_checker: { last_tick_at: 12, stale: 'no' },
          },
        }),
      );

      expect(await makeClient().getReadiness()).toMatchObject({
        heartbeatChecker: { lastTickAt: null, stale: true },
      });
    });

    it('maps a recorded migration failure on a 200', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          body: {
            status: 'ok',
            schema_version: 5,
            expected_schema_version: 5,
            last_migration_failure: {
              version: 6,
              error: 'boom',
              failed_at: '2026-10-17T09:00:00Z',
            },
          },
        }),
      );

      const result = await makeClient().getReadiness();

      expect(result).toMatchObject({
        kind: 'ok',
        lastMigrationFailure: { version: 6, error: 'boom', failedAt: '2026-10-17T09:00:00Z' },
      });
    });

    it('maps a 503 schema_mismatch with versions', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({
          status: 503,
          body: {
            status: 'schema_mismatch',
            schema_version: 4,
            expected_schema_version: 5,
            last_migration_failure: null,
            heartbeat_checker: { last_tick_at: null, stale: true },
          },
        }),
      );

      expect(await makeClient().getReadiness()).toEqual({
        kind: 'schema_mismatch',
        schemaVersion: 4,
        expectedSchemaVersion: 5,
        lastMigrationFailure: null,
        heartbeatChecker: { lastTickAt: null, stale: true },
      });
    });

    it('maps a 503 unavailable', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 503, body: { status: 'unavailable' } }));

      expect(await makeClient().getReadiness()).toEqual({ kind: 'unavailable' });
    });

    it('maps other statuses to unreachable with the body error or a fallback', async () => {
      fetchMock.mockResolvedValueOnce(
        jsonResponse({ status: 502, body: { error: 'bad gateway' } }),
      );
      expect(await makeClient().getReadiness()).toEqual({
        kind: 'unreachable',
        message: 'bad gateway',
      });

      fetchMock.mockResolvedValueOnce(new Response('oops', { status: 500 }));
      expect(await makeClient().getReadiness()).toEqual({
        kind: 'unreachable',
        message: 'request failed (500)',
      });
    });

    it('maps a network error to unreachable without throwing', async () => {
      fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));

      expect(await makeClient().getReadiness()).toEqual({
        kind: 'unreachable',
        message: 'Failed to fetch',
      });
    });
  });
});

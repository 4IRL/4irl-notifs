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
    created_at: '2026-10-17T10:00:00Z',
    updated_at: '2026-10-17T11:00:00Z',
  };

  const routeWire = {
    id: 7,
    app_pattern: 'urls4irl',
    channel_pattern: '*',
    destinations: ['ops_webhook'],
    enabled: true,
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
        createdAt: '2026-10-17T10:00:00Z',
        updatedAt: '2026-10-17T11:00:00Z',
      },
    ]);
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

    expect(first.url).toBe('https://admin.test/delivery/v1/messages?wait=10');
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

  describe('getReadiness', () => {
    it('maps 200 ok', async () => {
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

      expect(await makeClient().getReadiness()).toEqual({
        kind: 'ok',
        schemaVersion: 5,
        expectedSchemaVersion: 5,
        lastMigrationFailure: null,
      });
      expect(lastCall().url).toBe('https://admin.test/delivery/readyz');
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
          },
        }),
      );

      expect(await makeClient().getReadiness()).toEqual({
        kind: 'schema_mismatch',
        schemaVersion: 4,
        expectedSchemaVersion: 5,
        lastMigrationFailure: null,
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

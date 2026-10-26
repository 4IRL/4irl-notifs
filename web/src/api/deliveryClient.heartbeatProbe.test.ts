import { beforeEach, describe, expect, it, vi } from 'vitest';

import { createDeliveryApiClient, isProbeCheck } from './deliveryClient';

describe('delivery api client: website probe checks', () => {
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

  function lastBody(): Record<string, unknown> {
    return JSON.parse(lastCall().init.body as string) as Record<string, unknown>;
  }

  const baseWire = {
    id: 12,
    app_id: 'minecrafting',
    name: 'site',
    period_seconds: 120,
    grace_seconds: 60,
    paused: false,
    state: 'up',
    last_ping_at: null,
    last_ping_kind: null,
    down_since: null,
    deadline_at: '2026-10-19T20:02:00Z',
    destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
    owner_kind: 'operator',
    owner_app_id: null,
    created_at: '2026-10-17T10:00:00Z',
    updated_at: '2026-10-18T18:02:00Z',
  };

  const probeWire = {
    url: 'https://example.com/health',
    expected_statuses: [200, 204],
    body_match: 'ok',
    timeout_seconds: 5,
    interval_seconds: 60,
    consecutive_failures: 2,
    failure_threshold: 3,
    has_access_token: true,
    last_result: { at: '2026-10-18T18:01:00Z', status: 503, reason: 'status' },
  };

  async function listOne(wire: Record<string, unknown>) {
    fetchMock.mockResolvedValue(jsonResponse({ body: { checks: [wire] } }));
    const [check] = await makeClient().listHeartbeats();
    return check;
  }

  async function listProbe(probeOverrides: Record<string, unknown>) {
    const check = await listOne({ ...baseWire, probe: { ...probeWire, ...probeOverrides } });
    if (check.probe === null) {
      throw new Error('expected a probe check');
    }
    return check.probe;
  }

  describe('wire mapping', () => {
    it('maps a push wire with no probe or down_reason key to null for both', async () => {
      const check = await listOne(baseWire);
      expect(check.probe).toBeNull();
      expect(check.downReason).toBeNull();
      expect(isProbeCheck(check)).toBe(false);
    });

    it('maps a probe wire fully', async () => {
      const check = await listOne({ ...baseWire, down_reason: 'status', probe: probeWire });
      expect(isProbeCheck(check)).toBe(true);
      expect(check.downReason).toBe('status');
      expect(check.probe).toEqual({
        url: 'https://example.com/health',
        expectedStatuses: [200, 204],
        bodyMatch: 'ok',
        timeoutSeconds: 5,
        intervalSeconds: 60,
        consecutiveFailures: 2,
        failureThreshold: 3,
        hasAccessToken: true,
        lastResult: { at: '2026-10-18T18:01:00Z', status: 503, reason: 'status' },
      });
    });

    it('maps a null last_result, null probe and null expected_statuses', async () => {
      expect((await listProbe({ last_result: null })).lastResult).toBeNull();
      expect((await listProbe({ expected_statuses: null })).expectedStatuses).toEqual([]);
      expect((await listProbe({ body_match: null })).bodyMatch).toBeNull();
      const check = await listOne({ ...baseWire, probe: null });
      expect(check.probe).toBeNull();
    });

    it.each([
      ['absent', undefined],
      ['zero', 0],
      ['negative', -1],
      ['fractional', 2.5],
      ['string', '3'],
    ])('maps a %s failure_threshold to null', async (_label, value) => {
      const probe = await listProbe({ failure_threshold: value });
      expect(probe.failureThreshold).toBeNull();
    });

    it.each([
      ['absent', undefined],
      ['string true', 'true'],
      ['one', 1],
      ['false', false],
    ])('maps a %s has_access_token to false', async (_label, value) => {
      const probe = await listProbe({ has_access_token: value });
      expect(probe.hasAccessToken).toBe(false);
    });

    it('keeps a null last_result status as null and drops a non-numeric status', async () => {
      const nullStatus = await listProbe({
        last_result: { at: '2026-10-18T18:01:00Z', status: null, reason: 'timeout' },
      });
      expect(nullStatus.lastResult).toEqual({
        at: '2026-10-18T18:01:00Z',
        status: null,
        reason: 'timeout',
      });
      const oddStatus = await listProbe({
        last_result: { at: '2026-10-18T18:01:00Z', status: '200', reason: 5 },
      });
      expect(oddStatus.lastResult).toEqual({
        at: '2026-10-18T18:01:00Z',
        status: null,
        reason: null,
      });
    });

    it('preserves an unknown down_reason and probe reason as raw strings', async () => {
      const check = await listOne({
        ...baseWire,
        down_reason: 'exploded',
        probe: { ...probeWire, last_result: { at: 'x', status: null, reason: 'melted' } },
      });
      expect(check.downReason).toBe('exploded');
      expect(check.probe?.lastResult?.reason).toBe('melted');
    });
  });

  describe('createHeartbeat', () => {
    it('sends exactly the five push keys and no probe key', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 201, body: { check: baseWire } }));
      await makeClient().createHeartbeat({
        kind: 'push',
        appId: 'minecrafting',
        name: 'daily',
        destinations: ['ops_discord'],
        periodSeconds: 86400,
        graceSeconds: 7200,
      });
      expect(lastBody()).toEqual({
        app_id: 'minecrafting',
        name: 'daily',
        period_seconds: 86400,
        grace_seconds: 7200,
        destinations: ['ops_discord'],
      });
      expect(Object.keys(lastBody()).sort()).toEqual(
        ['app_id', 'destinations', 'grace_seconds', 'name', 'period_seconds'].sort(),
      );
    });

    it('sends a probe body without period/grace, body_match or access_token when absent', async () => {
      fetchMock.mockResolvedValue(
        jsonResponse({ status: 201, body: { check: { ...baseWire, probe: probeWire } } }),
      );
      const created = await makeClient().createHeartbeat({
        kind: 'probe',
        appId: 'minecrafting',
        name: 'site',
        destinations: ['ops_discord'],
        probe: {
          url: 'https://example.com/health',
          expectedStatuses: [200],
          bodyMatch: null,
          timeoutSeconds: 5,
          intervalSeconds: 60,
        },
      });
      const body = lastBody();
      expect(body).not.toHaveProperty('period_seconds');
      expect(body).not.toHaveProperty('grace_seconds');
      expect(body).toEqual({
        app_id: 'minecrafting',
        name: 'site',
        destinations: ['ops_discord'],
        probe: {
          url: 'https://example.com/health',
          expected_statuses: [200],
          timeout_seconds: 5,
          interval_seconds: 60,
        },
      });
      expect(created.probe?.url).toBe('https://example.com/health');
    });

    it('includes body_match and access_token when given', async () => {
      fetchMock.mockResolvedValue(jsonResponse({ status: 201, body: { check: baseWire } }));
      await makeClient().createHeartbeat({
        kind: 'probe',
        appId: 'minecrafting',
        name: 'site',
        destinations: [],
        probe: {
          url: 'https://example.com/',
          expectedStatuses: [200],
          bodyMatch: 'ok',
          timeoutSeconds: 5,
          intervalSeconds: 60,
        },
        accessToken: { clientId: 'id', clientSecret: 'secret' },
      });
      expect(lastBody().probe).toEqual({
        url: 'https://example.com/',
        expected_statuses: [200],
        timeout_seconds: 5,
        interval_seconds: 60,
        body_match: 'ok',
        access_token: { client_id: 'id', client_secret: 'secret' },
      });
    });
  });

  describe('updateHeartbeat', () => {
    beforeEach(() => {
      fetchMock.mockImplementation(() =>
        Promise.resolve(jsonResponse({ body: { check: baseWire } })),
      );
    });

    it('sends no access_token key (and no probe key) when accessToken is undefined', async () => {
      await makeClient().updateHeartbeat({ id: 12, probe: { timeoutSeconds: 4 } });
      expect(lastBody()).toEqual({ probe: { timeout_seconds: 4 } });
      expect(lastBody().probe).not.toHaveProperty('access_token');
    });

    it('sends access_token: null inside probe to clear', async () => {
      await makeClient().updateHeartbeat({ id: 12, accessToken: null });
      expect(lastBody()).toEqual({ probe: { access_token: null } });
    });

    it('sends a client_id/client_secret pair to replace', async () => {
      await makeClient().updateHeartbeat({
        id: 12,
        accessToken: { clientId: 'id', clientSecret: 'secret' },
      });
      expect(lastBody()).toEqual({
        probe: { access_token: { client_id: 'id', client_secret: 'secret' } },
      });
    });

    it('sends body_match: null to clear the body match', async () => {
      await makeClient().updateHeartbeat({ id: 12, probe: { bodyMatch: null } });
      expect(lastBody()).toEqual({ probe: { body_match: null } });
    });

    it('sends only the defined probe subfields', async () => {
      await makeClient().updateHeartbeat({
        id: 12,
        probe: {
          url: 'https://example.com/new',
          expectedStatuses: [200, 301],
          intervalSeconds: 300,
          timeoutSeconds: undefined,
        },
      });
      expect(lastBody()).toEqual({
        probe: {
          url: 'https://example.com/new',
          expected_statuses: [200, 301],
          interval_seconds: 300,
        },
      });
    });

    it('never sends a probe key on a push PATCH', async () => {
      await makeClient().updateHeartbeat({ id: 12, periodSeconds: 600, probe: {} });
      expect(lastBody()).toEqual({ period_seconds: 600 });
    });

    it('never sends read-only view fields', async () => {
      await makeClient().updateHeartbeat({
        id: 12,
        probe: { url: 'https://example.com/', intervalSeconds: 60 },
        accessToken: { clientId: 'id', clientSecret: 'secret' },
      });
      const serialised = lastCall().init.body as string;
      for (const key of [
        'failure_threshold',
        'has_access_token',
        'consecutive_failures',
        'last_result',
      ]) {
        expect(serialised).not.toContain(key);
      }
    });
  });
});

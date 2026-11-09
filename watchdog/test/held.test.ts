import { env } from 'cloudflare:test';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { parseHeldBlock } from '../src/held';
import { probeTarget } from '../src/probe';
import { TARGETS } from '../src/targets';
import {
  DELIVERY_URL,
  MINUTE,
  MORNING,
  buildEnv,
  healthyDelivery,
  healthyRoutes,
  heldBlock,
  heldDelivery,
  readStoredState,
  runScheduled,
  spyKv,
  stubFetch,
} from './harness';

describe('parseHeldBlock', () => {
  it('parses a full available block', () => {
    const block = heldBlock({
      stale: true,
      total: 999,
      as_of: '2026-10-19T10:00:00Z',
      unassigned_alerts: { count: 2 },
      apps: [
        { app_id: 'urls4irl', count: 2 },
        { app_id: 'chores', count: 1 },
      ],
      apps_more: 4,
    });
    expect(parseHeldBlock(block)).toEqual({
      stale: true,
      messages: 3,
      oldestAgeSeconds: 3600,
      unassigned: 2,
      apps: [
        { appId: 'urls4irl', count: 2 },
        { appId: 'chores', count: 1 },
      ],
      appsMore: 4,
    });
  });

  it('defaults absent optional groups to 0, [] and null', () => {
    expect(parseHeldBlock({ available: true })).toEqual({
      stale: false,
      messages: 0,
      oldestAgeSeconds: null,
      unassigned: 0,
      apps: [],
      appsMore: 0,
    });
  });

  it('accepts a null oldest_age_seconds', () => {
    const block = { available: true, messages: { count: 0, oldest_held_at: null, oldest_age_seconds: null } };
    expect(parseHeldBlock(block)?.oldestAgeSeconds).toBeNull();
  });

  it('keeps stale:true with its counts and ignores a disagreeing total', () => {
    const parsed = parseHeldBlock(heldBlock({ stale: true, total: 0 }));
    expect(parsed?.stale).toBe(true);
    expect(parsed?.messages).toBe(3);
  });

  it('returns null for an unavailable block, never zero counts', () => {
    expect(parseHeldBlock({ available: false })).toBeNull();
    expect(parseHeldBlock({ available: 'true' })).toBeNull();
    expect(parseHeldBlock({})).toBeNull();
  });

  const malformed: Array<[string, unknown]> = [
    ['null', null],
    ['a string', 'held'],
    ['an array', []],
    ['a number', 7],
    ['non-boolean stale', heldBlock({ stale: 'yes' })],
    ['negative message count', heldBlock({ messages: { count: -1 } })],
    ['fractional message count', heldBlock({ messages: { count: 1.5 } })],
    ['string message count', heldBlock({ messages: { count: '3' } })],
    ['messages missing count', heldBlock({ messages: {} })],
    ['messages not an object', heldBlock({ messages: 3 })],
    ['messages null', heldBlock({ messages: null })],
    ['negative unassigned count', heldBlock({ unassigned_alerts: { count: -2 } })],
    ['unassigned_alerts not an object', heldBlock({ unassigned_alerts: [] })],
    ['string oldest_age_seconds', heldBlock({ messages: { count: 1, oldest_age_seconds: '60' } })],
    ['negative oldest_age_seconds', heldBlock({ messages: { count: 1, oldest_age_seconds: -5 } })],
    ['fractional oldest_age_seconds', heldBlock({ messages: { count: 1, oldest_age_seconds: 1.5 } })],
    ['apps not an array', heldBlock({ apps: { app_id: 'a', count: 1 } })],
    ['app entry missing app_id', heldBlock({ apps: [{ count: 1 }] })],
    ['app entry empty app_id', heldBlock({ apps: [{ app_id: '', count: 1 }] })],
    ['app entry bad count', heldBlock({ apps: [{ app_id: 'a', count: -1 }] })],
    ['app entry not an object', heldBlock({ apps: ['a'] })],
    ['fractional apps_more', heldBlock({ apps_more: 1.5 })],
    ['negative apps_more', heldBlock({ apps_more: -1 })],
    ['string apps_more', heldBlock({ apps_more: '2' })],
  ];
  it.each(malformed)('returns null for %s', (_label, value) => {
    expect(parseHeldBlock(value)).toBeNull();
  });
});

describe('held block probe', () => {
  beforeEach(async () => {
    await env.WATCHDOG_STATE.delete('state');
    vi.spyOn(console, 'log').mockImplementation(() => {});
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    vi.spyOn(console, 'error').mockImplementation(() => {});
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const deliveryTarget = TARGETS[0]!;

  const mismatch = () =>
    Response.json(
      {
        status: 'schema_mismatch',
        heartbeat_checker: { last_tick_at: new Date(MORNING).toISOString(), stale: false },
        held: heldBlock(),
      },
      { status: 503 },
    );

  async function probe(delivery: ReturnType<typeof heldDelivery>) {
    stubFetch({ ...healthyRoutes(MORNING), delivery });
    return probeTarget({ target: deliveryTarget, env: buildEnv({ kv: spyKv().kv }), now: MORNING });
  }

  it('carries a valid block on a 200 ok body through the probe result', async () => {
    const result = await probe(heldDelivery({ now: MORNING, held: heldBlock() }));
    expect(result.verdict.ok).toBe(true);
    expect(result.verdict.detail).toBeNull();
    expect(result.held).toEqual(parseHeldBlock(heldBlock()));
  });

  it('does not leak a snapshot into later probes through the shared OK verdict', async () => {
    await probe(heldDelivery({ now: MORNING, held: heldBlock() }));
    const result = await probe(healthyDelivery(MORNING));
    expect(result.held).toBeUndefined();
    expect(result.verdict).toEqual({ ok: true, detail: null });
  });

  it.each([
    ['an absent block', undefined],
    ['available:false', { available: false }],
    ['a malformed block', { available: true, messages: { count: -1 } }],
  ])('yields no held for %s and leaves the verdict ok', async (_label, held) => {
    const result = await probe(heldDelivery({ now: MORNING, held }));
    expect(result.verdict).toEqual({ ok: true, detail: null });
    expect(result.held).toBeUndefined();
  });

  it('yields no held on a 503 schema_mismatch body and keeps the DOWN verdict', async () => {
    const result = await probe(mismatch);
    expect(result.verdict.ok).toBe(false);
    expect(result.verdict.detail).toBe('HTTP 503; readiness status schema_mismatch');
    expect(result.held).toBeUndefined();
  });

  it('yields no held for an invalid JSON body', async () => {
    const result = await probe(() => new Response('not json', { status: 200 }));
    expect(result.verdict.ok).toBe(false);
    expect(result.held).toBeUndefined();
  });

  it('drops held data from a timed-out probe', async () => {
    vi.useFakeTimers();
    stubFetch({
      ...healthyRoutes(MORNING),
      delivery: (call) =>
        new Promise<Response>((_resolve, reject) => {
          call.init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
        }),
    });
    const pending = probeTarget({ target: deliveryTarget, env: buildEnv({ kv: spyKv().kv }), now: MORNING });
    await vi.advanceTimersByTimeAsync(10_100);
    const result = await pending;
    expect(result.verdict.ok).toBe(false);
    expect(result.held).toBeUndefined();
  });

  it('does not change the delivery verdict end to end: a held block posts only the held alert and stores held once', async () => {
    const withHeld = stubFetch({ ...healthyRoutes(MORNING), delivery: heldDelivery({ now: MORNING, held: heldBlock() }) });
    const kv = spyKv();
    await runScheduled({ at: MORNING, workerEnv: buildEnv({ kv: kv.kv }) });
    expect(withHeld.discordPosts()).toHaveLength(1);
    expect(withHeld.discordPosts()[0]?.content).toContain('HELD');
    expect(withHeld.discordPosts()[0]?.content).not.toContain('DOWN');
    expect(withHeld.callsTo(DELIVERY_URL)).toHaveLength(1);
    expect(kv.put).toHaveBeenCalledTimes(1);
    const stored = await readStoredState();
    expect(Object.keys(stored ?? {})).toEqual(['version', 'targets', 'last_summary_date', 'held']);

    const later = MORNING + 5 * MINUTE;
    const second = stubFetch({ ...healthyRoutes(later), delivery: heldDelivery({ now: later, held: heldBlock() }) });
    const kvTwo = spyKv();
    await runScheduled({ at: later, workerEnv: buildEnv({ kv: kvTwo.kv }) });
    expect(second.discordPosts()).toHaveLength(0);
    expect(kvTwo.put).not.toHaveBeenCalled();
  });

  it('keeps the DOWN line unchanged for a 503 schema_mismatch body carrying a held block', async () => {
    const stub = stubFetch({ ...healthyRoutes(MORNING), delivery: mismatch });
    await runScheduled({ at: MORNING, workerEnv: buildEnv({ kv: spyKv().kv }) });
    expect(stub.discordPosts().map((post) => post.content)).toEqual([
      '🔴 **DOWN** `delivery-api`: HTTP 503; readiness status schema_mismatch',
    ]);
  });

  it('does not flip the delivery-api verdict to down for a malformed block', async () => {
    const stub = stubFetch({
      ...healthyRoutes(MORNING),
      delivery: heldDelivery({ now: MORNING, held: { available: true, messages: 'many' } }),
    });
    await runScheduled({ at: MORNING, workerEnv: buildEnv({ kv: spyKv().kv }) });
    expect(stub.discordPosts()).toHaveLength(0);
    expect((await readStoredState())?.targets['delivery-api']?.status).toBe('up');
  });
});

import { env } from 'cloudflare:test';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MockInstance } from 'vitest';
import { formatDuration, truncateContent } from '../src/format';
import {
  ACCESS_ID,
  ACCESS_SECRET,
  DELIVERY_URL,
  MINUTE,
  MORNING,
  NTFY_URL,
  PROVISIONING_URL,
  WEBHOOK_URL,
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
import type { Routes } from './harness';

let errorSpy: MockInstance<typeof console.error>;
let warnSpy: MockInstance<typeof console.warn>;
let logSpy: MockInstance<typeof console.log>;

// Storage isolation is per test FILE, so clear the state key before every test.
beforeEach(async () => {
  await env.WATCHDOG_STATE.delete('state');
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
  warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
  logSpy = vi.spyOn(console, 'log').mockImplementation(() => {});
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function consoleOutput(): string {
  return [...errorSpy.mock.calls, ...warnSpy.mock.calls, ...logSpy.mock.calls].flat().map(String).join('\n');
}

function logLines(): string[] {
  return logSpy.mock.calls.flat().map(String);
}

function warnLines(): string[] {
  return warnSpy.mock.calls.flat().map(String);
}

/** Time to let a retried probe's own 10 s timeout elapse, with margin over the retry timer offset. */
const RETRY_TIMEOUT_ADVANCE_MS = 10_100;

/** Fails with `status` on the first call and answers with `after` on every later call. */
function failThenRecover({ status, after }: { status: number; after: Routes['ntfy'] }): Routes['ntfy'] {
  let calls = 0;
  return (call) => {
    calls += 1;
    return calls === 1 ? new Response('boom', { status }) : after(call);
  };
}

/** Runs the scheduled handler once with a fresh spy KV over the shared real KV state. */
async function run({ at, routes, kvOptions }: { at: number; routes: Routes; kvOptions?: Parameters<typeof spyKv>[0] }) {
  const stub = stubFetch(routes);
  const kv = spyKv(kvOptions);
  await runScheduled({ at, workerEnv: buildEnv({ kv: kv.kv }) });
  return { stub, kv };
}

function downDelivery(status: number): Routes['delivery'] {
  return () => new Response('boom', { status });
}

describe('quiet runs', () => {
  it('records an up target silently on first run, then does nothing while nothing changes', async () => {
    const first = await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    expect(first.stub.discordPosts()).toHaveLength(0);
    expect(first.kv.get).toHaveBeenCalledTimes(1);
    expect(first.kv.put).toHaveBeenCalledTimes(1);
    const stored = await readStoredState();
    expect(Object.keys(stored?.targets ?? {})).toEqual(['delivery-api', 'provisioning-api', 'ntfy']);
    expect(stored?.targets['delivery-api']?.status).toBe('up');

    const second = await run({ at: MORNING + 5 * MINUTE, routes: healthyRoutes(MORNING + 5 * MINUTE) });
    expect(second.stub.discordPosts()).toHaveLength(0);
    expect(second.kv.get).toHaveBeenCalledTimes(1);
    expect(second.kv.put).not.toHaveBeenCalled();
  });
});

describe('state transitions', () => {
  it('posts once on up to down, stays quiet while down, and posts recovery with the duration', async () => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING) });

    const downAt = MORNING + 5 * MINUTE;
    const down = await run({ at: downAt, routes: { ...healthyRoutes(downAt), provisioning: downDelivery(502) } });
    const posts = down.stub.discordPosts();
    expect(posts).toHaveLength(1);
    expect(posts[0]?.content).toBe('🔴 **DOWN** `provisioning-api`: HTTP 502');
    expect(posts[0]?.allowed_mentions).toEqual({ parse: [] });
    expect(down.kv.put).toHaveBeenCalledTimes(1);

    const stillDownAt = MORNING + 10 * MINUTE;
    const stillDown = await run({
      at: stillDownAt,
      routes: { ...healthyRoutes(stillDownAt), provisioning: downDelivery(503) },
    });
    expect(stillDown.stub.discordPosts()).toHaveLength(0);
    expect(stillDown.kv.put).not.toHaveBeenCalled();

    const recoveredAt = MORNING + 25 * MINUTE;
    const recovered = await run({ at: recoveredAt, routes: healthyRoutes(recoveredAt) });
    expect(recovered.stub.discordPosts().map((post) => post.content)).toEqual([
      '🟢 **RECOVERED** `provisioning-api` after 20 min',
    ]);
    expect(recovered.kv.put).toHaveBeenCalledTimes(1);
    expect((await readStoredState())?.targets['provisioning-api']?.status).toBe('up');
  });

  it('posts a down target that has no prior record, and records an up target silently', async () => {
    const result = await run({
      at: MORNING,
      routes: { ...healthyRoutes(MORNING), ntfy: () => new Response('nope', { status: 500 }) },
    });
    expect(result.stub.discordPosts().map((post) => post.content)).toEqual(['🔴 **DOWN** `ntfy`: HTTP 500']);
    const stored = await readStoredState();
    expect(stored?.targets['ntfy']).toEqual({
      status: 'down',
      since: new Date(MORNING).toISOString(),
      detail: 'HTTP 500',
    });
    expect(stored?.targets['delivery-api']?.status).toBe('up');
  });

  it('groups several transitions of one run into a single message', async () => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    const at = MORNING + 5 * MINUTE;
    const result = await run({
      at,
      routes: { ...healthyRoutes(at), delivery: downDelivery(500), provisioning: downDelivery(502) },
    });
    expect(result.stub.discordPosts()).toHaveLength(1);
    expect(result.stub.discordPosts()[0]?.content).toBe(
      '🔴 **DOWN** `delivery-api`: HTTP 500\n🔴 **DOWN** `provisioning-api`: HTTP 502',
    );
  });

  it('does not write when only the failure detail of a still-down target changes', async () => {
    await run({ at: MORNING, routes: { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }) } });
    const at = MORNING + 5 * MINUTE;
    const result = await run({ at, routes: { ...healthyRoutes(at), ntfy: () => new Response('x', { status: 503 }) } });
    expect(result.stub.discordPosts()).toHaveLength(0);
    expect(result.kv.put).not.toHaveBeenCalled();
  });
});

describe('probe verdicts', () => {
  async function firstDetail(routes: Routes, target: string): Promise<string | null | undefined> {
    await run({ at: MORNING, routes });
    return (await readStoredState())?.targets[target]?.detail;
  }

  it('treats delivery-api 200 ok with a stale heartbeat checker as down, naming the age', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      delivery: () =>
        Response.json({
          status: 'ok',
          heartbeat_checker: { last_tick_at: new Date(MORNING - 7 * MINUTE).toISOString(), stale: true },
        }),
    };
    expect(await firstDetail(routes, 'delivery-api')).toBe('heartbeat checker stale (last run 7 min ago)');
  });

  it('treats a heartbeat checker that never ran as stale', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      delivery: () => Response.json({ status: 'ok', heartbeat_checker: { last_tick_at: null, stale: true } }),
    };
    expect(await firstDetail(routes, 'delivery-api')).toBe('heartbeat checker stale (never ran)');
  });

  it('fails closed when the heartbeat_checker field is missing or malformed', async () => {
    const missing = { ...healthyRoutes(MORNING), delivery: () => Response.json({ status: 'ok' }) };
    expect(await firstDetail(missing, 'delivery-api')).toBe('heartbeat checker missing');
  });

  it('names a non-ok readiness status alongside a stale checker', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      delivery: () =>
        Response.json(
          { status: 'schema_mismatch', heartbeat_checker: { last_tick_at: null, stale: true } },
          { status: 503 },
        ),
    };
    expect(await firstDetail(routes, 'delivery-api')).toBe(
      'HTTP 503; readiness status schema_mismatch; heartbeat checker stale (never ran)',
    );
  });

  describe('heartbeat prober field', () => {
    const freshChecker = () => ({ last_tick_at: new Date(MORNING - 10_000).toISOString(), stale: false });
    const staleChecker = () => ({ last_tick_at: new Date(MORNING - 7 * MINUTE).toISOString(), stale: true });

    function deliveryWith(body: Record<string, unknown>, init?: ResponseInit): Routes {
      return { ...healthyRoutes(MORNING), delivery: () => Response.json(body, init) };
    }

    async function healthyWith(prober: unknown): Promise<string | null | undefined> {
      return firstDetail(
        deliveryWith({ status: 'ok', heartbeat_checker: freshChecker(), heartbeat_prober: prober }),
        'delivery-api',
      );
    }

    it('passes a fresh prober', async () => {
      const detail = await healthyWith({ last_tick_at: new Date(MORNING - 10_000).toISOString(), stale: false });
      expect(detail ?? null).toBeNull();
    });

    it('passes when the field is explicitly absent', async () => {
      const detail = await firstDetail(
        deliveryWith({ status: 'ok', heartbeat_checker: freshChecker() }),
        'delivery-api',
      );
      expect(detail ?? null).toBeNull();
    });

    it('names the age of a stale prober', async () => {
      const detail = await healthyWith({ last_tick_at: new Date(MORNING - 7 * MINUTE).toISOString(), stale: true });
      expect(detail).toBe('heartbeat prober stale (last run 7 min ago)');
    });

    it('treats a stale prober with no time as never ran', async () => {
      expect(await healthyWith({ last_tick_at: null, stale: true })).toBe('heartbeat prober stale (never ran)');
    });

    it.each([
      ['null', null],
      ['an array', []],
      ['a string', 'x'],
      ['an empty object', {}],
      ['a non-boolean stale', { stale: 'no' }],
    ])('fails closed on a malformed prober (%s)', async (_name, prober) => {
      expect(await healthyWith(prober)).toBe('heartbeat prober state unknown');
    });

    it('joins a stale checker and a stale prober', async () => {
      const detail = await firstDetail(
        deliveryWith({
          status: 'ok',
          heartbeat_checker: staleChecker(),
          heartbeat_prober: { last_tick_at: null, stale: true },
        }),
        'delivery-api',
      );
      expect(detail).toBe('heartbeat checker stale (last run 7 min ago); heartbeat prober stale (never ran)');
    });

    it('names a stale prober on a schema_mismatch 503', async () => {
      const detail = await firstDetail(
        deliveryWith(
          {
            status: 'schema_mismatch',
            heartbeat_checker: freshChecker(),
            heartbeat_prober: { last_tick_at: null, stale: true },
          },
          { status: 503 },
        ),
        'delivery-api',
      );
      expect(detail).toBe('HTTP 503; readiness status schema_mismatch; heartbeat prober stale (never ran)');
    });
  });

  it('treats an Access login redirect on an Access target as down with access denied', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      delivery: () => new Response(null, { status: 302, headers: { location: 'https://example.cloudflareaccess.com/' } }),
    };
    expect(await firstDetail(routes, 'delivery-api')).toBe('access denied (HTTP 302)');
  });

  it('treats a 403 from an Access target as access denied', async () => {
    const routes = { ...healthyRoutes(MORNING), provisioning: () => new Response('no', { status: 403 }) };
    expect(await firstDetail(routes, 'provisioning-api')).toBe('access denied (HTTP 403)');
  });

  it('labels a redirect from the non-Access ntfy target as an unexpected redirect', async () => {
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response(null, { status: 301 }) };
    expect(await firstDetail(routes, 'ntfy')).toBe('unexpected redirect (HTTP 301)');
  });

  it('treats provisioning-api non-200 as down', async () => {
    const routes = { ...healthyRoutes(MORNING), provisioning: downDelivery(500) };
    expect(await firstDetail(routes, 'provisioning-api')).toBe('HTTP 500');
  });

  it.each([
    ['503', () => new Response('x', { status: 503 }), 'HTTP 503'],
    ['404 (older delivery-api)', () => new Response('x', { status: 404 }), 'HTTP 404'],
    ['200 status unavailable', () => Response.json({ status: 'unavailable' }), 'provisioning status unavailable'],
    ['200 non-JSON body', () => new Response('ok'), 'invalid JSON response'],
    ['200 non-object JSON body', () => Response.json([]), 'invalid JSON response'],
    ['200 body without status', () => Response.json({}), 'provisioning status missing'],
  ])('treats provisioning-api %s as down', async (_name, provisioning, detail) => {
    const routes = { ...healthyRoutes(MORNING), provisioning };
    expect(await firstDetail(routes, 'provisioning-api')).toBe(detail);
  });

  it.each([
    ['healthy:false', () => Response.json({ healthy: false }), 'reported unhealthy'],
    ['missing healthy', () => Response.json({ other: true }), 'reported unhealthy'],
    ['non-boolean healthy', () => Response.json({ healthy: 'true' }), 'reported unhealthy'],
    ['non-JSON body', () => new Response('healthy'), 'invalid JSON response'],
  ])('treats ntfy 200 with %s as down', async (_label, ntfy, expectedDetail) => {
    const routes = { ...healthyRoutes(MORNING), ntfy };
    expect(await firstDetail(routes, 'ntfy')).toBe(expectedDetail);
    expect((await readStoredState())?.targets['ntfy']?.status).toBe('down');
  });

  it('treats a network error as down without leaking the error message', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      ntfy: () => {
        throw new TypeError('connect ECONNREFUSED 10.0.0.1');
      },
    };
    expect(await firstDetail(routes, 'ntfy')).toBe('request failed (TypeError)');
  });

  it('treats a probe that outlives 10 s as down with a timeout detail', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    let aborted = false;
    const hang: Routes['ntfy'] = ({ init }) =>
      new Promise<Response>((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => {
          aborted = true;
          reject(new DOMException('aborted', 'AbortError'));
        });
      });
    const stub = stubFetch({ ...healthyRoutes(MORNING), ntfy: hang });
    const kv = spyKv();
    const pending = runScheduled({ at: MORNING, workerEnv: buildEnv({ kv: kv.kv }) });
    await vi.advanceTimersByTimeAsync(9_999);
    expect(aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(aborted).toBe(true);
    // The retry hangs too: it needs its own 10 s before the target is reported down. The margin
    // keeps the test independent of the zero-delay retry timer firing a millisecond into the tick.
    await vi.advanceTimersByTimeAsync(RETRY_TIMEOUT_ADVANCE_MS);
    await pending;
    expect(stub.callsTo(NTFY_URL)).toHaveLength(2);
    expect(logLines()).toContainEqual(
      expect.stringMatching(
        /^watchdog: probe id=ntfy attempt=2 ok=false http=none duration_ms=\d+ detail="timeout after 10 s"$/,
      ),
    );
    expect(stub.discordPosts().map((post) => post.content)).toEqual(['🔴 **DOWN** `ntfy`: timeout after 10 s']);
  });

  it('reports a response body that stalls past 10 s as a timeout, not as invalid JSON', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const stalledBody: Routes['ntfy'] = ({ init }) =>
      new Response(
        new ReadableStream({
          start(controller) {
            init.signal?.addEventListener('abort', () => controller.error(new DOMException('aborted', 'AbortError')));
          },
        }),
      );
    const stub = stubFetch({ ...healthyRoutes(MORNING), ntfy: stalledBody });
    const kv = spyKv();
    const pending = runScheduled({ at: MORNING, workerEnv: buildEnv({ kv: kv.kv }) });
    await vi.advanceTimersByTimeAsync(10_000);
    await vi.advanceTimersByTimeAsync(RETRY_TIMEOUT_ADVANCE_MS);
    await pending;
    expect(stub.discordPosts().map((post) => post.content)).toEqual(['🔴 **DOWN** `ntfy`: timeout after 10 s']);
  });

  it('sends every probe with redirect manual', async () => {
    const { stub } = await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    for (const url of [DELIVERY_URL, PROVISIONING_URL, NTFY_URL]) {
      expect(stub.callsTo(url)[0]?.init.redirect).toBe('manual');
    }
  });
});

describe('Access headers and secrets', () => {
  it('attaches Access headers only to the Access targets', async () => {
    const { stub } = await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    for (const url of [DELIVERY_URL, PROVISIONING_URL]) {
      const headers = new Headers(stub.callsTo(url)[0]?.init.headers);
      expect(headers.get('CF-Access-Client-Id')).toBe(ACCESS_ID);
      expect(headers.get('CF-Access-Client-Secret')).toBe(ACCESS_SECRET);
    }
    const ntfyHeaders = new Headers(stub.callsTo(NTFY_URL)[0]?.init.headers);
    expect(ntfyHeaders.get('CF-Access-Client-Id')).toBeNull();
    expect(ntfyHeaders.get('CF-Access-Client-Secret')).toBeNull();
  });

  it('never posts or logs the secrets or the webhook URL, even when everything fails', async () => {
    const routes: Routes = {
      delivery: downDelivery(500),
      provisioning: downDelivery(500),
      ntfy: () => {
        throw new Error(`failed for ${ACCESS_SECRET}`);
      },
      discord: () => new Response('denied', { status: 500 }),
    };
    const { stub } = await run({ at: MORNING, routes, kvOptions: { getThrows: true } });
    const everything = JSON.stringify(stub.discordPosts()) + consoleOutput();
    for (const secret of [ACCESS_ID, ACCESS_SECRET, 'webhook-token-abc', WEBHOOK_URL]) {
      expect(everything).not.toContain(secret);
    }
  });

  it('does not log the webhook URL when the Discord request itself throws with it in the message', async () => {
    const routes: Routes = {
      ...healthyRoutes(MORNING),
      ntfy: () => new Response('x', { status: 500 }),
      discord: () => {
        throw new TypeError(`fetch failed for ${WEBHOOK_URL}`);
      },
    };
    await run({ at: MORNING, routes });
    expect(errorSpy).toHaveBeenCalled();
    const logged = consoleOutput();
    expect(logged).not.toContain(WEBHOOK_URL);
    expect(logged).not.toContain('webhook-token-abc');
  });

  it('logs one config error and does nothing when DISCORD_WEBHOOK_URL is missing', async () => {
    const stub = stubFetch(healthyRoutes(MORNING));
    const kv = spyKv();
    await runScheduled({ at: MORNING, workerEnv: buildEnv({ kv: kv.kv, DISCORD_WEBHOOK_URL: undefined }) });
    expect(errorSpy).toHaveBeenCalledTimes(1);
    expect(stub.calls).toHaveLength(0);
    expect(kv.get).not.toHaveBeenCalled();
    expect(kv.put).not.toHaveBeenCalled();
  });
});

describe('failure handling', () => {
  it('prefixes a warning and posts every down target when the KV read throws, then writes', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      delivery: downDelivery(500),
      ntfy: () => new Response('x', { status: 500 }),
    };
    const { stub, kv } = await run({ at: MORNING, routes, kvOptions: { getThrows: true } });
    expect(stub.discordPosts().map((post) => post.content)).toEqual([
      '⚠️ Previous state unavailable, reporting all failing targets.\n' +
        '🔴 **DOWN** `delivery-api`: HTTP 500\n🔴 **DOWN** `ntfy`: HTTP 500',
    ]);
    expect(kv.get).toHaveBeenCalledTimes(1);
    expect(kv.put).toHaveBeenCalledTimes(1);
  });

  it('does not write when the KV read throws, every target is up and nothing is posted', async () => {
    await env.WATCHDOG_STATE.put('state', JSON.stringify({ version: 1, targets: {}, last_summary_date: '2026-10-19' }));
    const { stub, kv } = await run({ at: MORNING, routes: healthyRoutes(MORNING), kvOptions: { getThrows: true } });
    expect(stub.discordPosts()).toHaveLength(0);
    expect(kv.get).toHaveBeenCalledTimes(1);
    expect(kv.put).not.toHaveBeenCalled();
  });

  it('still writes after a throwing KV read when a target is down and the post is made', async () => {
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }) };
    const { stub, kv } = await run({ at: MORNING, routes, kvOptions: { getThrows: true } });
    expect(stub.discordPosts()).toHaveLength(1);
    expect(kv.put).toHaveBeenCalledTimes(1);
  });

  it('still repairs an unparseable stored state when every target is up and nothing is posted', async () => {
    await env.WATCHDOG_STATE.put('state', '{not json');
    const { stub, kv } = await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    expect(stub.discordPosts()).toHaveLength(0);
    expect(kv.put).toHaveBeenCalledTimes(1);
    expect((await readStoredState())?.targets['ntfy']?.status).toBe('up');
    expect(logLines()).toContainEqual(expect.stringMatching(/^watchdog: run state=invalid .* kv_write=written /));
  });

  it('treats an unparseable stored state as unavailable and repairs it', async () => {
    await env.WATCHDOG_STATE.put('state', '{not json');
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }) };
    const { stub, kv } = await run({ at: MORNING, routes });
    expect(stub.discordPosts()[0]?.content.startsWith('⚠️ Previous state unavailable')).toBe(true);
    expect(kv.put).toHaveBeenCalledTimes(1);
    expect((await readStoredState())?.targets['ntfy']?.status).toBe('down');
  });

  it('treats a delivered post as delivered even if releasing the response body throws', async () => {
    const delivered = {
      ok: true,
      status: 204,
      body: {
        cancel: () => {
          throw new Error('cancel failed');
        },
      },
    } as unknown as Response;
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }), discord: () => delivered };
    const { kv } = await run({ at: MORNING, routes });
    expect(kv.put).toHaveBeenCalledTimes(1);
  });

  it('gives up on a Discord post that hangs past 10 s and writes no state', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const hang: Routes['discord'] = ({ init }) =>
      new Promise<Response>((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    const stub = stubFetch({ ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }), discord: hang });
    const kv = spyKv();
    const pending = runScheduled({ at: MORNING, workerEnv: buildEnv({ kv: kv.kv }) });
    await vi.advanceTimersByTimeAsync(10_000);
    await pending;
    expect(stub.discordPosts()).toHaveLength(1);
    expect(kv.put).not.toHaveBeenCalled();
  });

  it('still posts when the KV write fails', async () => {
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }) };
    const { stub, kv } = await run({ at: MORNING, routes, kvOptions: { putThrows: true } });
    expect(stub.discordPosts()).toHaveLength(1);
    expect(kv.put).toHaveBeenCalledTimes(1);
    expect(errorSpy).toHaveBeenCalled();
  });

  it.each([
    ['a non-2xx answer', () => new Response('nope', { status: 500 })],
    [
      'a throw',
      () => {
        throw new TypeError('network down');
      },
    ],
  ])('does not write state when the Discord post fails with %s, and retries on the next run', async (_label, failing) => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    const at = MORNING + 5 * MINUTE;
    const down = { ...healthyRoutes(at), ntfy: () => new Response('x', { status: 500 }) };

    const failed = await run({ at, routes: { ...down, discord: failing } });
    expect(failed.stub.discordPosts()).toHaveLength(1);
    expect(failed.kv.put).not.toHaveBeenCalled();
    expect((await readStoredState())?.targets['ntfy']?.status).toBe('up');

    const retryAt = at + 5 * MINUTE;
    const retried = await run({
      at: retryAt,
      routes: { ...healthyRoutes(retryAt), ntfy: () => new Response('x', { status: 500 }) },
    });
    expect(retried.stub.discordPosts().map((post) => post.content)).toEqual(['🔴 **DOWN** `ntfy`: HTTP 500']);
    expect(retried.kv.put).toHaveBeenCalledTimes(1);
  });
});

describe('retry before alerting', () => {
  it('stays quiet when the first attempt fails and the retry passes, and warns in the log', async () => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    const at = MORNING + 5 * MINUTE;
    const routes = {
      ...healthyRoutes(at),
      delivery: failThenRecover({ status: 520, after: healthyDelivery(at) }),
    };
    const result = await run({ at, routes });
    expect(result.stub.discordPosts()).toHaveLength(0);
    expect(result.stub.callsTo(DELIVERY_URL)).toHaveLength(2);
    expect(result.kv.put).not.toHaveBeenCalled();
    expect((await readStoredState())?.targets['delivery-api']?.status).toBe('up');
    expect(warnLines()).toContain('watchdog: probe recovered on retry id=delivery-api first_detail="HTTP 520"');
    expect(logLines()).toContainEqual(
      expect.stringMatching(/^watchdog: probe id=delivery-api attempt=2 ok=true http=200 duration_ms=\d+$/),
    );
  });

  it('retries an Access 403 first attempt and stays quiet when the retry passes', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      provisioning: failThenRecover({ status: 403, after: () => Response.json({ status: 'ok' }) }),
    };
    const result = await run({ at: MORNING, routes });
    expect(result.stub.discordPosts()).toHaveLength(0);
    expect(result.stub.callsTo(PROVISIONING_URL)).toHaveLength(2);
    expect(logLines()).toContainEqual(
      expect.stringMatching(
        /^watchdog: probe id=provisioning-api attempt=1 ok=false http=403 duration_ms=\d+ detail="access denied \(HTTP 403\)"$/,
      ),
    );
  });

  it('alerts with the retry verdict when both attempts fail', async () => {
    let calls = 0;
    const routes = {
      ...healthyRoutes(MORNING),
      delivery: () => new Response('boom', { status: ++calls === 1 ? 520 : 503 }),
    };
    const result = await run({ at: MORNING, routes });
    expect(result.stub.callsTo(DELIVERY_URL)).toHaveLength(2);
    expect(result.stub.discordPosts().map((post) => post.content)).toEqual([
      '🔴 **DOWN** `delivery-api`: HTTP 503',
    ]);
    expect(warnLines().join('\n')).not.toContain('recovered on retry');
  });

  it('retries only the failing target, once, and never a passing one', async () => {
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }) };
    const { stub } = await run({ at: MORNING, routes });
    expect(stub.callsTo(NTFY_URL)).toHaveLength(2);
    expect(stub.callsTo(DELIVERY_URL)).toHaveLength(1);
    expect(stub.callsTo(PROVISIONING_URL)).toHaveLength(1);
  });

  it.each([
    ['no override', undefined],
    ['a non-numeric override', 'abc'],
    ['a negative override', '-5'],
    ['an empty override', ''],
    ['a blank override', '   '],
    ['an override above the 60 s ceiling', '60001'],
  ])('waits 30 s before the retry with %s', async (_label, override) => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const stub = stubFetch({
      ...healthyRoutes(MORNING),
      ntfy: failThenRecover({ status: 500, after: () => Response.json({ healthy: true }) }),
    });
    const kv = spyKv();
    const pending = runScheduled({
      at: MORNING,
      workerEnv: buildEnv({ kv: kv.kv, PROBE_RETRY_DELAY_MS: override }),
    });
    await vi.advanceTimersByTimeAsync(29_999);
    expect(stub.callsTo(NTFY_URL)).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    await pending;
    expect(stub.callsTo(NTFY_URL)).toHaveLength(2);
    expect(stub.discordPosts()).toHaveLength(0);
  });

  it.each([
    ['500', 500],
    ['60000', 60_000],
  ])('honours a valid PROBE_RETRY_DELAY_MS override of %s ms', async (override, delayMs) => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const stub = stubFetch({
      ...healthyRoutes(MORNING),
      ntfy: failThenRecover({ status: 500, after: () => Response.json({ healthy: true }) }),
    });
    const kv = spyKv();
    const pending = runScheduled({
      at: MORNING,
      workerEnv: buildEnv({ kv: kv.kv, PROBE_RETRY_DELAY_MS: override }),
    });
    await vi.advanceTimersByTimeAsync(delayMs - 1);
    expect(stub.callsTo(NTFY_URL)).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    await pending;
    expect(stub.callsTo(NTFY_URL)).toHaveLength(2);
  });
});

describe('probe and run logging', () => {
  it('logs one line per probe attempt with the status and duration', async () => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    const probeLines = logLines().filter((line) => line.startsWith('watchdog: probe '));
    expect(probeLines).toHaveLength(3);
    expect(probeLines).toContainEqual(
      expect.stringMatching(/^watchdog: probe id=delivery-api attempt=1 ok=true http=200 duration_ms=\d+$/),
    );
    expect(probeLines).toContainEqual(
      expect.stringMatching(/^watchdog: probe id=ntfy attempt=1 ok=true http=200 duration_ms=\d+$/),
    );
  });

  it('logs the failure detail and both attempts of a probe that stays down', async () => {
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }) };
    await run({ at: MORNING, routes });
    const lines = logLines();
    for (const attempt of [1, 2]) {
      expect(lines).toContainEqual(
        expect.stringMatching(
          new RegExp(`^watchdog: probe id=ntfy attempt=${attempt} ok=false http=500 duration_ms=\\d+ detail="HTTP 500"$`),
        ),
      );
    }
  });

  it('logs http=none when no response arrived', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      ntfy: () => {
        throw new TypeError('connect ECONNREFUSED 10.0.0.1');
      },
    };
    await run({ at: MORNING, routes });
    expect(logLines()).toContainEqual(
      expect.stringMatching(
        /^watchdog: probe id=ntfy attempt=1 ok=false http=none duration_ms=\d+ detail="request failed \(TypeError\)"$/,
      ),
    );
  });

  it('logs a run summary: written on the first run, unchanged on the next', async () => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    expect(logLines()).toContainEqual(
      expect.stringMatching(
        /^watchdog: run state=loaded up=3 down=0 transitions=0 discord=none kv_write=written duration_ms=\d+ held=absent held_alert=closed$/,
      ),
    );
    logSpy.mockClear();
    const at = MORNING + 5 * MINUTE;
    await run({ at, routes: healthyRoutes(at) });
    expect(logLines()).toContainEqual(
      expect.stringMatching(/^watchdog: run state=loaded up=3 down=0 transitions=0 discord=none kv_write=unchanged /),
    );
  });

  it('logs discord=failed and kv_write=skipped when the post fails', async () => {
    const routes = {
      ...healthyRoutes(MORNING),
      ntfy: () => new Response('x', { status: 500 }),
      discord: () => new Response('no', { status: 500 }),
    };
    await run({ at: MORNING, routes });
    expect(logLines()).toContainEqual(
      expect.stringMatching(
        /^watchdog: run state=loaded up=2 down=1 transitions=1 discord=failed kv_write=skipped duration_ms=\d+ held=absent held_alert=closed$/,
      ),
    );
  });

  it('logs state=read_failed when the KV read throws', async () => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING), kvOptions: { getThrows: true } });
    expect(logLines()).toContainEqual(
      expect.stringMatching(/^watchdog: run state=read_failed up=3 down=0 transitions=0 discord=none kv_write=skipped /),
    );
  });

  it('logs kv_write=failed when the post went out but the write threw', async () => {
    const routes = { ...healthyRoutes(MORNING), ntfy: () => new Response('x', { status: 500 }) };
    await run({ at: MORNING, routes, kvOptions: { putThrows: true } });
    expect(logLines()).toContainEqual(
      expect.stringMatching(/^watchdog: run state=loaded up=2 down=1 transitions=1 discord=posted kv_write=failed /),
    );
  });
});

describe('daily summary', () => {
  const day = (hour: number, minute = 0) => Date.UTC(2026, 9, 19, hour, minute, 0);

  it('posts once per UTC date at or after 16:00 and not again that day', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });

    const early = await run({ at: day(15, 55), routes: healthyRoutes(day(15, 55)) });
    expect(early.stub.discordPosts()).toHaveLength(0);

    const due = await run({ at: day(16), routes: healthyRoutes(day(16)) });
    expect(due.stub.discordPosts().map((post) => post.content)).toEqual([
      '📋 Daily watchdog summary: 3/3 up\n' +
        '🟢 `delivery-api` up since 10-19 09:00\n' +
        '🟢 `provisioning-api` up since 10-19 09:00\n' +
        '🟢 `ntfy` up since 10-19 09:00',
    ]);
    expect(due.kv.put).toHaveBeenCalledTimes(1);
    expect((await readStoredState())?.last_summary_date).toBe('2026-10-19');

    const later = await run({ at: day(16, 5), routes: healthyRoutes(day(16, 5)) });
    expect(later.stub.discordPosts()).toHaveLength(0);
    expect(later.kv.put).not.toHaveBeenCalled();

    const nextDay = Date.UTC(2026, 9, 20, 16, 0, 0);
    const again = await run({ at: nextDay, routes: healthyRoutes(nextDay) });
    expect(again.stub.discordPosts()).toHaveLength(1);
    expect((await readStoredState())?.last_summary_date).toBe('2026-10-20');
  });

  it('does not post a summary while the state is unreadable, so it cannot repeat every run', async () => {
    const result = await run({ at: day(16), routes: healthyRoutes(day(16)), kvOptions: { getThrows: true } });
    expect(result.stub.discordPosts()).toHaveLength(0);
  });

  it('retries a failed summary post on the next run', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const failed = await run({
      at: day(16),
      routes: { ...healthyRoutes(day(16)), discord: () => new Response('x', { status: 500 }) },
    });
    expect(failed.kv.put).not.toHaveBeenCalled();
    expect((await readStoredState())?.last_summary_date).toBeNull();

    const retried = await run({ at: day(16, 5), routes: healthyRoutes(day(16, 5)) });
    expect(retried.stub.discordPosts()).toHaveLength(1);
    expect((await readStoredState())?.last_summary_date).toBe('2026-10-19');
  });

  it('shares the run message with state changes and lists down targets', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const routes = { ...healthyRoutes(day(16)), ntfy: () => new Response('x', { status: 500 }) };
    const result = await run({ at: day(16), routes });
    expect(result.stub.discordPosts()).toHaveLength(1);
    expect(result.stub.discordPosts()[0]?.content).toBe(
      '🔴 **DOWN** `ntfy`: HTTP 500\n\n' +
        '📋 Daily watchdog summary: 2/3 up\n' +
        '🟢 `delivery-api` up since 10-19 09:00\n' +
        '🟢 `provisioning-api` up since 10-19 09:00\n' +
        '🔴 `ntfy` down since 10-19 16:00: HTTP 500',
    );
  });
});

describe('held messages', () => {
  const day = (hour: number, minute = 0) => Date.UTC(2026, 9, 19, hour, minute, 0);
  const nextDay16 = Date.UTC(2026, 9, 20, 16, 0, 0);

  const ALERT_LINES =
    '🟠 **HELD** 3 app messages waiting, oldest 1 h: add a route or destination\n↳ `urls4irl` 3';
  const SUMMARY_9AM =
    '📋 Daily watchdog summary: 3/3 up\n' +
    '🟢 `delivery-api` up since 10-19 09:00\n' +
    '🟢 `provisioning-api` up since 10-19 09:00\n' +
    '🟢 `ntfy` up since 10-19 09:00';

  function zeroBlock(overrides: Record<string, unknown> = {}): Record<string, unknown> {
    return heldBlock({
      total: 0,
      messages: { count: 0, oldest_held_at: null, oldest_age_seconds: null },
      unassigned_alerts: { count: 0 },
      apps: [],
      ...overrides,
    });
  }

  function withHeld(
    at: number,
    held: unknown,
    overrides: Partial<Routes> = {},
    status = 200,
    bodyStatus = 'ok',
  ): Routes {
    return { ...healthyRoutes(at), delivery: heldDelivery({ now: at, held, status, bodyStatus }), ...overrides };
  }

  async function storedRaw(): Promise<string | null> {
    return env.WATCHDOG_STATE.get('state');
  }

  async function storedHeldJson(): Promise<string> {
    return JSON.stringify((await readStoredState())?.held);
  }

  async function openAlertAt(at: number): Promise<void> {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    await run({ at, routes: withHeld(at, heldBlock()) });
  }

  function allContent(stub: { discordPosts: () => Array<{ content: string }> }): string {
    return stub.discordPosts().map((post) => post.content).join('\n');
  }

  it('ignores an older /readyz without a held block: no line, no key, no extra write', async () => {
    await run({ at: MORNING, routes: healthyRoutes(MORNING) });
    const second = await run({ at: MORNING + 5 * MINUTE, routes: healthyRoutes(MORNING + 5 * MINUTE) });
    expect(second.stub.discordPosts()).toHaveLength(0);
    expect(second.kv.put).not.toHaveBeenCalled();
    expect((await readStoredState())?.held).toBeUndefined();
    expect(await storedRaw()).not.toContain('held');
  });

  it('posts the first alert once and stays quiet on later runs with the same stored document', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const first = await run({ at: day(10), routes: withHeld(day(10), heldBlock()) });
    expect(first.stub.discordPosts().map((post) => post.content)).toEqual([ALERT_LINES]);
    expect(first.kv.put).toHaveBeenCalledTimes(1);
    expect((await readStoredState())?.held).toEqual({
      alert_since: '2026-10-19T10:00:00.000Z',
      last_reminder_date: '2026-10-18',
      zero_seen_at: null,
    });

    const rawBefore = await storedRaw();
    for (const step of [1, 2, 3]) {
      const at = day(10) + step * 5 * MINUTE;
      const quiet = await run({ at, routes: withHeld(at, heldBlock()) });
      expect(quiet.stub.discordPosts()).toHaveLength(0);
      expect(quiet.kv.put).not.toHaveBeenCalled();
    }
    expect(await storedRaw()).toBe(rawBefore);
  });

  it('puts held lines and a DOWN line in one message: change lines, then the held section', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const routes = withHeld(day(10), heldBlock(), { ntfy: () => new Response('x', { status: 500 }) });
    const result = await run({ at: day(10), routes });
    expect(result.stub.discordPosts().map((post) => post.content)).toEqual([
      '🔴 **DOWN** `ntfy`: HTTP 500\n\n' + ALERT_LINES,
    ]);
  });

  it('puts the held alert before the daily summary in the same message', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const result = await run({ at: day(16), routes: withHeld(day(16), heldBlock()) });
    expect(result.stub.discordPosts().map((post) => post.content)).toEqual([ALERT_LINES + '\n\n' + SUMMARY_9AM]);
  });

  it('posts the daily reminder once on the next date at 16:00', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    await run({ at: day(12), routes: withHeld(day(12), heldBlock()) });
    expect((await readStoredState())?.held?.last_reminder_date).toBe('2026-10-19');

    const sameDay = await run({ at: day(16, 5), routes: withHeld(day(16, 5), heldBlock()) });
    expect(allContent(sameDay.stub)).not.toContain('STILL HELD');

    const reminder = await run({ at: nextDay16, routes: withHeld(nextDay16, heldBlock()) });
    const content = reminder.stub.discordPosts()[0]?.content ?? '';
    expect(reminder.stub.discordPosts()).toHaveLength(1);
    expect(content).toContain(
      '⏰ **STILL HELD** (since 10-19 12:00) 3 app messages waiting, oldest 1 h: add a route or destination\n' +
        '↳ `urls4irl` 3\n' +
        'ℹ️ Held messages are deleted after 7 days (default).',
    );
    expect((await readStoredState())?.held?.last_reminder_date).toBe('2026-10-20');

    const again = await run({ at: nextDay16 + 5 * MINUTE, routes: withHeld(nextDay16 + 5 * MINUTE, heldBlock()) });
    expect(allContent(again.stub)).not.toContain('STILL HELD');
    expect(again.kv.put).not.toHaveBeenCalled();
  });

  it('posts the recovery once after two consecutive fresh zero runs and removes held from KV', async () => {
    await openAlertAt(day(12));
    const firstZero = await run({ at: day(12, 5), routes: withHeld(day(12, 5), zeroBlock()) });
    expect(firstZero.stub.discordPosts()).toHaveLength(0);
    // A zero-streak write (nothing posted) goes through the normal write path.
    expect(firstZero.kv.put).toHaveBeenCalledTimes(1);
    expect((await readStoredState())?.held?.zero_seen_at).toBe('2026-10-19T12:05:00.000Z');

    const secondZero = await run({ at: day(12, 10), routes: withHeld(day(12, 10), zeroBlock()) });
    expect(secondZero.stub.discordPosts().map((post) => post.content)).toEqual(['🟢 **HELD CLEARED** after 10 min']);
    expect((await readStoredState())?.held).toBeUndefined();
    expect(await storedRaw()).not.toContain('held');

    const third = await run({ at: day(12, 15), routes: withHeld(day(12, 15), zeroBlock()) });
    expect(third.stub.discordPosts()).toHaveLength(0);
    expect(third.kv.put).not.toHaveBeenCalled();
  });

  it('leaves held byte-identical and posts no held line for a non-200, a timeout, available:false and malformed blocks', async () => {
    await openAlertAt(day(12));
    const heldBefore = await storedHeldJson();
    const cases: Array<{ name: string; routes: (at: number) => Routes }> = [
      { name: 'non-200', routes: (at) => ({ ...healthyRoutes(at), delivery: downDelivery(500) }) },
      { name: 'unavailable', routes: (at) => withHeld(at, { available: false }) },
      { name: 'malformed', routes: (at) => withHeld(at, heldBlock({ messages: { count: 'many' } })) },
      { name: 'schema_mismatch', routes: (at) => withHeld(at, zeroBlock(), {}, 503, 'schema_mismatch') },
    ];
    for (const [index, { name, routes }] of cases.entries()) {
      const at = day(12, 5 + index * 5);
      const result = await run({ at, routes: routes(at) });
      expect(allContent(result.stub), name).not.toContain('HELD');
      expect(await storedHeldJson(), name).toBe(heldBefore);
    }
  });

  it('leaves held byte-identical when the delivery probe times out', async () => {
    await openAlertAt(day(12));
    const heldBefore = await storedHeldJson();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    const hang: Routes['delivery'] = ({ init }) =>
      new Promise((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    const at = day(12, 5);
    const stub = stubFetch({ ...healthyRoutes(at), delivery: hang });
    const kv = spyKv();
    const pending = runScheduled({ at, workerEnv: buildEnv({ kv: kv.kv }) });
    await vi.advanceTimersByTimeAsync(10_000);
    await vi.advanceTimersByTimeAsync(RETRY_TIMEOUT_ADVANCE_MS);
    await pending;
    expect(allContent(stub)).not.toContain('HELD');
    expect(await storedHeldJson()).toBe(heldBefore);
  });

  it('decides a stale block on its counts and adds the stale line', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const result = await run({ at: day(10), routes: withHeld(day(10), heldBlock({ stale: true })) });
    expect(result.stub.discordPosts().map((post) => post.content)).toEqual([
      ALERT_LINES + '\n⚠️ Held counts are stale (delivery-api could not refresh them).',
    ]);
  });

  it('writes no state when the first alert post fails, and posts the same alert next run', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const failed = await run({
      at: day(10),
      routes: withHeld(day(10), heldBlock(), { discord: () => new Response('no', { status: 500 }) }),
    });
    expect(failed.kv.put).not.toHaveBeenCalled();
    expect((await readStoredState())?.held).toBeUndefined();

    const retried = await run({ at: day(10, 5), routes: withHeld(day(10, 5), heldBlock()) });
    expect(retried.stub.discordPosts().map((post) => post.content)).toEqual([ALERT_LINES]);
    expect((await readStoredState())?.held?.alert_since).toBe('2026-10-19T10:05:00.000Z');
  });

  it('keeps the alert open when the recovery post fails', async () => {
    await openAlertAt(day(12));
    await run({ at: day(12, 5), routes: withHeld(day(12, 5), zeroBlock()) });
    const failed = await run({
      at: day(12, 10),
      routes: withHeld(day(12, 10), zeroBlock(), { discord: () => new Response('no', { status: 500 }) }),
    });
    expect(failed.kv.put).not.toHaveBeenCalled();
    expect((await readStoredState())?.held).toBeDefined();
  });

  it('posts no held line when the state read throws', async () => {
    const result = await run({
      at: day(10),
      routes: withHeld(day(10), heldBlock()),
      kvOptions: { getThrows: true },
    });
    expect(allContent(result.stub)).not.toContain('HELD');
    expect(result.kv.put).not.toHaveBeenCalled();
  });

  it('posts no held line and writes no held for a stored invalid state', async () => {
    await env.WATCHDOG_STATE.put('state', 'not json at all');
    const result = await run({ at: day(10), routes: withHeld(day(10), heldBlock()) });
    expect(allContent(result.stub)).not.toContain('HELD');
    expect((await readStoredState())?.held).toBeUndefined();
  });

  it('repairs an invalid state on a DOWN post without held (accepted one-time reset)', async () => {
    await env.WATCHDOG_STATE.put('state', 'not json at all');
    const routes = withHeld(day(10), heldBlock(), { ntfy: () => new Response('x', { status: 500 }) });
    const result = await run({ at: day(10), routes });
    expect(allContent(result.stub)).toContain('DOWN');
    expect(allContent(result.stub)).not.toContain('HELD');
    expect((await readStoredState())?.held).toBeUndefined();
  });

  it('does not close the alert on stale all-zero blocks, and a fresh zero afterwards starts the streak', async () => {
    await openAlertAt(day(12));
    const heldBefore = await storedHeldJson();
    for (const step of [1, 2]) {
      const at = day(12) + step * 5 * MINUTE;
      const result = await run({ at, routes: withHeld(at, zeroBlock({ stale: true })) });
      expect(result.stub.discordPosts()).toHaveLength(0);
      expect(result.kv.put).not.toHaveBeenCalled();
      expect(await storedHeldJson()).toBe(heldBefore);
    }
    const at = day(12, 15);
    const fresh = await run({ at, routes: withHeld(at, zeroBlock()) });
    expect(fresh.stub.discordPosts()).toHaveLength(0);
    expect((await readStoredState())?.held?.zero_seen_at).toBe('2026-10-19T12:15:00.000Z');
  });

  it('keeps the worst-case combined message within 2000 characters and keeps the STILL HELD headline', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const apps = Array.from({ length: 20 }, (_unused, index) => ({
      app_id: `${String.fromCharCode(97 + index)}`.repeat(40),
      count: 100 - index,
    }));
    const block = heldBlock({
      stale: true,
      total: 400,
      messages: { count: 300, oldest_held_at: '2026-10-19T09:00:00Z', oldest_age_seconds: 200_000 },
      unassigned_alerts: { count: 40 },
      apps,
      apps_more: 15,
    });
    await run({ at: day(12), routes: withHeld(day(12), block) });
    const routes = withHeld(nextDay16, block, {
      provisioning: () => new Response('x', { status: 500 }),
      ntfy: () => new Response('x', { status: 500 }),
    });
    const result = await run({ at: nextDay16, routes });
    const content = result.stub.discordPosts()[0]?.content ?? '';
    expect(content.length).toBeLessThanOrEqual(2000);
    expect(content).toContain('STILL HELD');
    expect(content).toContain('DOWN');
    expect(content).toContain('Daily watchdog summary');
  });

  it('keeps one get and at most one put per run across alert, reminder and recovery', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const sequence: Array<{ at: number; held: unknown }> = [
      { at: day(12), held: heldBlock() },
      { at: day(12, 5), held: heldBlock() },
      { at: nextDay16, held: heldBlock() },
      { at: nextDay16 + 5 * MINUTE, held: zeroBlock() },
      { at: nextDay16 + 10 * MINUTE, held: zeroBlock() },
    ];
    for (const { at, held } of sequence) {
      const { kv } = await run({ at, routes: withHeld(at, held) });
      expect(kv.get).toHaveBeenCalledTimes(1);
      expect(kv.put.mock.calls.length).toBeLessThanOrEqual(1);
    }
    expect((await readStoredState())?.held).toBeUndefined();
  });

  it('logs held=ok/stale/none and held_alert=open/closed on the run line', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    logSpy.mockClear();
    await run({ at: day(10), routes: withHeld(day(10), heldBlock()) });
    expect(logLines()).toContainEqual(expect.stringMatching(/^watchdog: run .* held=ok held_alert=open$/));
    logSpy.mockClear();
    await run({ at: day(10, 5), routes: withHeld(day(10, 5), heldBlock({ stale: true })) });
    expect(logLines()).toContainEqual(expect.stringMatching(/^watchdog: run .* held=stale held_alert=open$/));
    logSpy.mockClear();
    await run({ at: day(10, 10), routes: withHeld(day(10, 10), heldBlock()), kvOptions: { getThrows: true } });
    expect(logLines()).toContainEqual(expect.stringMatching(/^watchdog: run .* held=none held_alert=closed$/));
  });

  it('never puts the webhook URL or Access secrets in held lines', async () => {
    await run({ at: day(9), routes: healthyRoutes(day(9)) });
    const result = await run({ at: day(10), routes: withHeld(day(10), heldBlock()) });
    const content = allContent(result.stub);
    expect(content).toContain('HELD');
    expect(content).not.toContain(WEBHOOK_URL);
    expect(content).not.toContain('webhook-token-abc');
    expect(content).not.toContain(ACCESS_SECRET);
    expect(content).not.toContain(ACCESS_ID);
  });
});

describe('write budget', () => {
  it('never performs more than one put per run, so 288 runs cannot exceed 288 writes', async () => {
    let totalPuts = 0;
    for (let runIndex = 0; runIndex < 288; runIndex += 1) {
      const at = MORNING + runIndex * 5 * MINUTE;
      // Worst case: ntfy flaps every run, so every run changes state and posts.
      const flapping = runIndex % 2 === 0 ? () => new Response('x', { status: 500 }) : () => Response.json({ healthy: true });
      const { kv } = await run({ at, routes: { ...healthyRoutes(at), ntfy: flapping } });
      expect(kv.get.mock.calls.length).toBe(1);
      expect(kv.put.mock.calls.length).toBeLessThanOrEqual(1);
      totalPuts += kv.put.mock.calls.length;
    }
    expect(totalPuts).toBeLessThanOrEqual(288);
  });
});

describe('format helpers', () => {
  it('formats durations', () => {
    expect(formatDuration(30_000)).toBe('1 min');
    expect(formatDuration(20 * MINUTE)).toBe('20 min');
    expect(formatDuration(60 * MINUTE)).toBe('1 h');
    expect(formatDuration(125 * MINUTE)).toBe('2 h 5 min');
    expect(formatDuration((50 * 60 + 30) * MINUTE)).toBe('2 d 2 h');
  });

  it('truncates to the 2000 character Discord limit without splitting a surrogate pair', () => {
    expect(truncateContent('short')).toBe('short');
    // Index 1998 is the high surrogate of the first emoji, so a naive slice(0, 1999) would split it.
    const long = 'a'.repeat(1998) + '😀😀';
    const cut = truncateContent(long);
    expect(cut.length).toBeLessThanOrEqual(2000);
    expect(cut).toBe('a'.repeat(1998) + '…');
  });
});

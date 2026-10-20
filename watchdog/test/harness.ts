import { createScheduledController, env } from 'cloudflare:test';
import { vi } from 'vitest';
import worker from '../src/index';
import type { Env } from '../src/types';

export const WEBHOOK_URL = 'https://discord.test/api/webhooks/123456/webhook-token-abc';
export const ACCESS_ID = 'access-id-123';
export const ACCESS_SECRET = 'access-secret-456';

export const DELIVERY_URL = 'https://notifs-delivery.4irl.app/readyz';
export const PROVISIONING_URL = 'https://notifs-api.4irl.app/healthz';
export const NTFY_URL = 'https://notifs.4irl.app/v1/health';

export const MINUTE = 60_000;
/** A UTC instant well before the 16:00 daily-summary hour. */
export const MORNING = Date.UTC(2026, 9, 19, 10, 0, 0);

export interface FetchCall {
  url: string;
  init: RequestInit;
}

export type Route = (call: FetchCall) => Response | Promise<Response>;

export interface Routes {
  delivery: Route;
  provisioning: Route;
  ntfy: Route;
  discord: Route;
}

export function healthyDelivery(now: number): Route {
  return () =>
    Response.json({
      status: 'ok',
      heartbeat_checker: { last_tick_at: new Date(now - 10_000).toISOString(), stale: false },
    });
}

export function healthyRoutes(now: number): Routes {
  return {
    delivery: healthyDelivery(now),
    provisioning: () => new Response('ok'),
    ntfy: () => Response.json({ healthy: true }),
    discord: () => new Response(null, { status: 204 }),
  };
}

export interface FetchStub {
  calls: FetchCall[];
  /** Parsed JSON bodies of every request that went to the Discord webhook. */
  discordPosts: () => Array<{ content: string; allowed_mentions: { parse: string[] } }>;
  callsTo: (url: string) => FetchCall[];
}

/** Replaces the global fetch with a router over the four outbound hosts the Worker may call. */
export function stubFetch(routes: Routes): FetchStub {
  const calls: FetchCall[] = [];
  const routeFor = (url: string): Route => {
    if (url === DELIVERY_URL) return routes.delivery;
    if (url === PROVISIONING_URL) return routes.provisioning;
    if (url === NTFY_URL) return routes.ntfy;
    if (url === WEBHOOK_URL) return routes.discord;
    throw new Error(`unexpected outbound fetch to ${url}`);
  };
  vi.stubGlobal('fetch', async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const call = { url: String(input), init };
    calls.push(call);
    return routeFor(call.url)(call);
  });
  return {
    calls,
    callsTo: (url) => calls.filter((call) => call.url === url),
    discordPosts: () =>
      calls
        .filter((call) => call.url === WEBHOOK_URL)
        .map((call) => JSON.parse(String(call.init.body))),
  };
}

export interface KvSpy {
  kv: KVNamespace;
  get: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
}

/** Wraps the real miniflare KV binding so tests can count reads/writes and force failures. */
export function spyKv({ getThrows = false, putThrows = false } = {}): KvSpy {
  const real = env.WATCHDOG_STATE;
  const get = vi.fn(async (key: string) => {
    if (getThrows) throw new Error('kv read unavailable');
    return real.get(key);
  });
  const put = vi.fn(async (key: string, value: string) => {
    if (putThrows) throw new Error('kv write unavailable');
    return real.put(key, value);
  });
  return { kv: { get, put } as unknown as KVNamespace, get, put };
}

export function buildEnv({ kv, ...overrides }: { kv: KVNamespace } & Partial<Env>): Env {
  return {
    WATCHDOG_STATE: kv,
    DISCORD_WEBHOOK_URL: WEBHOOK_URL,
    CF_ACCESS_CLIENT_ID: ACCESS_ID,
    CF_ACCESS_CLIENT_SECRET: ACCESS_SECRET,
    ...overrides,
  };
}

/** Invokes the real exported `scheduled` handler once, at the given scheduled time. */
export async function runScheduled({
  at,
  workerEnv,
}: {
  at: number;
  workerEnv: Env;
}): Promise<void> {
  const controller = createScheduledController({ scheduledTime: at, cron: '*/5 * * * *' });
  await worker.scheduled(controller, workerEnv);
}

export async function readStoredState(): Promise<{
  targets: Record<string, { status: string; since: string; detail: string | null }>;
  last_summary_date: string | null;
} | null> {
  const raw = await env.WATCHDOG_STATE.get('state');
  return raw === null ? null : JSON.parse(raw);
}

import { formatDuration } from './format';
import { parseHeldBlock } from './held';
import type { ProbeVerdict, Target } from './types';

const STATUS_OK = 200;
const MAX_ECHOED_VALUE_LENGTH = 40;

const OK: ProbeVerdict = { ok: true, detail: null };

function down(detail: string): ProbeVerdict {
  return { ok: false, detail };
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

async function readJson(response: Response): Promise<unknown> {
  try {
    return await response.json();
  } catch {
    return undefined;
  }
}

/** A short, safe rendering of a remote value for a failure detail. */
function describeValue(value: unknown): string {
  if (typeof value !== 'string' || value === '') {
    return 'missing';
  }
  return value.slice(0, MAX_ECHOED_VALUE_LENGTH);
}

function staleDetail({
  label,
  lastTickAt,
  now,
}: {
  label: string;
  lastTickAt: unknown;
  now: number;
}): string {
  const lastTickMs = typeof lastTickAt === 'string' ? Date.parse(lastTickAt) : Number.NaN;
  if (Number.isNaN(lastTickMs)) {
    return `${label} stale (never ran)`;
  }
  return `${label} stale (last run ${formatDuration(now - lastTickMs)} ago)`;
}

/**
 * delivery-api `/readyz`: ok only when the HTTP code is 200, `status` is `ok`, the heartbeat
 * checker is fresh and, when the body reports one, the heartbeat prober is fresh. Staleness never
 * changes the HTTP code (it would fail the compose healthcheck), so the watchdog is the one that
 * interprets it. Fails closed on any missing or malformed checker field; an absent
 * `heartbeat_prober` property is tolerated (an older delivery-api), a present malformed one
 * (including `null`) is not.
 */
async function evaluateDeliveryApi({
  response,
  now,
}: {
  response: Response;
  now: number;
}): Promise<ProbeVerdict> {
  const problems: string[] = [];
  if (response.status !== STATUS_OK) {
    problems.push(`HTTP ${response.status}`);
  }
  const body = asRecord(await readJson(response));
  if (body === null) {
    if (response.status === STATUS_OK) {
      problems.push('invalid JSON response');
    }
    return problems.length === 0 ? OK : down(problems.join('; '));
  }
  if (body.status !== 'ok') {
    problems.push(`readiness status ${describeValue(body.status)}`);
  }
  const checker = asRecord(body.heartbeat_checker);
  if (checker === null) {
    problems.push('heartbeat checker missing');
  } else if (checker.stale === true) {
    problems.push(staleDetail({ label: 'heartbeat checker', lastTickAt: checker.last_tick_at, now }));
  } else if (checker.stale !== false) {
    problems.push('heartbeat checker state unknown');
  }
  if (Object.hasOwn(body, 'heartbeat_prober')) {
    const prober = asRecord(body.heartbeat_prober);
    if (prober === null) {
      problems.push('heartbeat prober state unknown');
    } else if (prober.stale === true) {
      problems.push(staleDetail({ label: 'heartbeat prober', lastTickAt: prober.last_tick_at, now }));
    } else if (prober.stale !== false) {
      problems.push('heartbeat prober state unknown');
    }
  }
  const verdict = problems.length === 0 ? OK : down(problems.join('; '));
  const held = response.status === STATUS_OK && Object.hasOwn(body, 'held') ? parseHeldBlock(body.held) : null;
  // Return a new object: the shared OK constant must never carry a snapshot.
  return held === null ? verdict : { ...verdict, held };
}

/**
 * delivery-api `/healthz/provisioning` (delivery-api's own reachability check of provisioning-api):
 * ok iff HTTP 200 and the JSON body has `status` exactly `ok`. Fails closed on a malformed body or
 * any other status.
 */
async function evaluateProvisioningApi({ response }: { response: Response }): Promise<ProbeVerdict> {
  if (response.status !== STATUS_OK) {
    return down(`HTTP ${response.status}`);
  }
  const body = asRecord(await readJson(response));
  if (body === null) {
    return down('invalid JSON response');
  }
  return body.status === 'ok' ? OK : down(`provisioning status ${describeValue(body.status)}`);
}

/** ntfy `/v1/health`: ok iff HTTP 200 and `healthy` is exactly the boolean `true`. */
async function evaluateNtfy({ response }: { response: Response }): Promise<ProbeVerdict> {
  if (response.status !== STATUS_OK) {
    return down(`HTTP ${response.status}`);
  }
  const body = asRecord(await readJson(response));
  if (body === null) {
    return down('invalid JSON response');
  }
  return body.healthy === true ? OK : down('reported unhealthy');
}

const URLS4IRL_COMPONENTS = ['postgres', 'session_redis', 'metrics_redis'] as const;
const URLS4IRL_UP = 'up';
const URLS4IRL_KNOWN_BAD_VALUES = ['down', 'not configured'];
const STATUS_UNAUTHORIZED = 401;
const STATUS_RATE_LIMITED = 429;
const STATUS_UNAVAILABLE = 503;

/** `unhealthy: <key>=<value>, ...` for the components not exactly `up`, or null when all are up. */
function urls4irlUnhealthyDetail(components: Record<string, unknown>): string | null {
  const parts: string[] = [];
  for (const key of URLS4IRL_COMPONENTS) {
    if (!Object.hasOwn(components, key)) {
      parts.push(`${key}=missing`);
      continue;
    }
    const value = components[key];
    if (value === URLS4IRL_UP) {
      continue;
    }
    const shown = typeof value === 'string' && URLS4IRL_KNOWN_BAD_VALUES.includes(value) ? value : 'invalid';
    parts.push(`${key}=${shown}`);
  }
  return parts.length === 0 ? null : `unhealthy: ${parts.join(', ')}`;
}

/**
 * urls4irl `GET https://urls.4irl.app/health/deep` with header `X-Health-Token` (handler: `urls4irl`
 * repo `backend/system/routes.py`, `health_deep()`). Ok iff HTTP 200 and a JSON object whose
 * `status` is exactly `Success` and whose `components.postgres`, `components.session_redis` and
 * `components.metrics_redis` are all exactly `up`. A 401 means the Worker's `HEALTH_CHECK_TOKEN` is
 * wrong or urls4irl's secret is unset, and a 429 means repeated bad tokens: both are setup problems,
 * reported as such rather than as an outage. The app answers within 7 s, under `PROBE_TIMEOUT_MS`.
 * Only the component keys and their fixed values are echoed, never `message` or any other field.
 */
async function evaluateUrls4irl({ response }: { response: Response }): Promise<ProbeVerdict> {
  if (response.status === STATUS_UNAUTHORIZED) {
    return down('unauthorized: check HEALTH_CHECK_TOKEN');
  }
  if (response.status === STATUS_RATE_LIMITED) {
    return down('rate limited (HTTP 429): check HEALTH_CHECK_TOKEN');
  }
  if (response.status === STATUS_UNAVAILABLE) {
    const components = asRecord(asRecord(await readJson(response))?.components);
    const detail = components === null ? null : urls4irlUnhealthyDetail(components);
    return down(detail ?? 'HTTP 503');
  }
  if (response.status !== STATUS_OK) {
    return down(`HTTP ${response.status}`);
  }
  const body = asRecord(await readJson(response));
  if (body === null) {
    return down('invalid JSON response');
  }
  if (body.status !== 'Success') {
    return down(`urls4irl status ${describeValue(body.status)}`);
  }
  const components = asRecord(body.components);
  if (components === null) {
    return down('components missing');
  }
  const detail = urls4irlUnhealthyDetail(components);
  return detail === null ? OK : down(detail);
}

/**
 * The probe list. Adding another app's health URL later means adding an entry here; the run logic
 * is target-agnostic. Keep `delivery-api` first (`held.test.ts` reads `TARGETS[0]`).
 */
export const TARGETS: readonly Target[] = [
  {
    id: 'delivery-api',
    url: 'https://notifs-delivery.4irl.app/readyz',
    access: true,
    evaluate: evaluateDeliveryApi,
  },
  {
    id: 'provisioning-api',
    url: 'https://notifs-delivery.4irl.app/healthz/provisioning',
    access: true,
    evaluate: evaluateProvisioningApi,
  },
  {
    id: 'ntfy',
    url: 'https://notifs.4irl.app/v1/health',
    access: false,
    evaluate: evaluateNtfy,
  },
  {
    id: 'urls4irl',
    url: 'https://urls.4irl.app/health/deep',
    access: false,
    healthToken: true,
    evaluate: evaluateUrls4irl,
  },
];

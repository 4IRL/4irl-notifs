import { formatDuration } from './format';
import { parseHeldBlock } from './held';
import type { HealthTokenSecret, ProbeVerdict, Target } from './types';

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

const COMPONENT_UP = 'up';
const KNOWN_BAD_COMPONENT_VALUES = ['down', 'not configured'];
/** A reported component key is echoed only when it is a plain snake_case name; others show as `unnamed`. */
const SAFE_COMPONENT_KEY = /^[a-z][a-z0-9_]{0,39}$/;
/** Unhealthy components listed in one detail; the rest are counted as `+N more`. */
export const DEEP_HEALTH_COMPONENTS_SHOWN = 10;
const STATUS_UNAUTHORIZED = 401;
const STATUS_RATE_LIMITED = 429;
const STATUS_UNAVAILABLE = 503;

/** `<key>=<value>` for a component that is not exactly `up`, or null when it is. */
function componentPart({ key, value }: { key: string; value: unknown }): string | null {
  if (value === COMPONENT_UP) {
    return null;
  }
  const shownKey = SAFE_COMPONENT_KEY.test(key) ? key : 'unnamed';
  const shownValue = typeof value === 'string' && KNOWN_BAD_COMPONENT_VALUES.includes(value) ? value : 'invalid';
  return `${shownKey}=${shownValue}`;
}

/**
 * `unhealthy: <key>=<value>, ...` for the components not exactly `up`, or null when all are up. The
 * required components come first, in declared order (`missing` when absent), then every other
 * component the app reports, in body order, so a component an app adds later is judged too.
 */
function unhealthyComponentsDetail({
  components,
  requiredComponents,
}: {
  components: Record<string, unknown>;
  requiredComponents: readonly string[];
}): string | null {
  const parts: string[] = [];
  for (const key of requiredComponents) {
    const part = Object.hasOwn(components, key) ? componentPart({ key, value: components[key] }) : `${key}=missing`;
    if (part !== null) {
      parts.push(part);
    }
  }
  for (const [key, value] of Object.entries(components)) {
    const part = requiredComponents.includes(key) ? null : componentPart({ key, value });
    if (part !== null) {
      parts.push(part);
    }
  }
  if (parts.length === 0) {
    return null;
  }
  const hidden = parts.length - DEEP_HEALTH_COMPONENTS_SHOWN;
  const more = hidden > 0 ? `, +${hidden} more` : '';
  return `unhealthy: ${parts.slice(0, DEEP_HEALTH_COMPONENTS_SHOWN).join(', ')}${more}`;
}

/**
 * The 4IRL deep-health contract (`docs/app-integration-guide.md`, "Outside-the-VPS deep health"):
 * `GET <url>` with header `X-Health-Token`. Ok iff HTTP 200 and a JSON object whose `status` is
 * exactly `Success` and whose `components` map has every required component, and every other
 * reported one, exactly `up`. A 503 names the unhealthy components. A 401 means the Worker's token
 * secret is wrong or the app's own secret is unset, and a 429 means repeated bad tokens: both are
 * setup problems, reported as such rather than as an outage. Only component keys and fixed values
 * are echoed, never `message` or any other field.
 */
async function evaluateDeepHealth({
  response,
  tokenSecret,
  requiredComponents,
}: {
  response: Response;
  tokenSecret: HealthTokenSecret;
  requiredComponents: readonly string[];
}): Promise<ProbeVerdict> {
  if (response.status === STATUS_UNAUTHORIZED) {
    return down(`unauthorized: check ${tokenSecret}`);
  }
  if (response.status === STATUS_RATE_LIMITED) {
    return down(`rate limited (HTTP 429): check ${tokenSecret}`);
  }
  if (response.status === STATUS_UNAVAILABLE) {
    const components = asRecord(asRecord(await readJson(response))?.components);
    const detail = components === null ? null : unhealthyComponentsDetail({ components, requiredComponents });
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
    return down(`deep health status ${describeValue(body.status)}`);
  }
  const components = asRecord(body.components);
  if (components === null) {
    return down('components missing');
  }
  const detail = unhealthyComponentsDetail({ components, requiredComponents });
  return detail === null ? OK : down(detail);
}

/**
 * A target for an app that implements the deep-health contract. `tokenSecret` is that app's own
 * `HEALTH_TOKEN_<APP>` secret, and `requiredComponents` the components that must be present (any
 * other reported component is judged as well).
 */
export function deepHealthTarget({
  id,
  url,
  tokenSecret,
  requiredComponents,
}: {
  id: string;
  url: string;
  tokenSecret: HealthTokenSecret;
  requiredComponents: readonly [string, ...string[]];
}): Target {
  return {
    id,
    url,
    access: false,
    tokenSecret,
    evaluate: ({ response }) => evaluateDeepHealth({ response, tokenSecret, requiredComponents }),
  };
}

/**
 * The probe list; the run logic is target-agnostic. An app sharing the hub's VPS joins with one
 * `deepHealthTarget` entry plus its `HEALTH_TOKEN_<APP>` field in `Env` and Worker secret
 * (`docs/app-integration-guide.md`). Keep `delivery-api` first (`held.test.ts` reads `TARGETS[0]`).
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
  // Handler: `urls4irl` repo `backend/system/routes.py`, `health_deep()`; it answers within 7 s,
  // under `PROBE_TIMEOUT_MS`.
  deepHealthTarget({
    id: 'urls4irl',
    url: 'https://urls.4irl.app/health/deep',
    tokenSecret: 'HEALTH_TOKEN_URLS4IRL',
    requiredComponents: ['postgres', 'session_redis', 'metrics_redis'],
  }),
];

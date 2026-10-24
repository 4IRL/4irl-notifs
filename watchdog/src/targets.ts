import { formatDuration } from './format';
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
  return problems.length === 0 ? OK : down(problems.join('; '));
}

/** provisioning-api `/healthz`: ok iff HTTP 200. */
async function evaluateProvisioningApi({ response }: { response: Response }): Promise<ProbeVerdict> {
  return response.status === STATUS_OK ? OK : down(`HTTP ${response.status}`);
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

/**
 * The probe list. Adding another app's health URL later means adding an entry here; the run logic
 * is target-agnostic.
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
    url: 'https://notifs-api.4irl.app/healthz',
    access: true,
    evaluate: evaluateProvisioningApi,
  },
  {
    id: 'ntfy',
    url: 'https://notifs.4irl.app/v1/health',
    access: false,
    evaluate: evaluateNtfy,
  },
];

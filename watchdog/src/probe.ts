import type { Env, ProbeResult, ProbeVerdict, Target } from './types';

export const PROBE_TIMEOUT_MS = 10_000;
const PROBE_TIMEOUT_SECONDS = PROBE_TIMEOUT_MS / 1000;

const STATUS_REDIRECT_MIN = 300;
const STATUS_REDIRECT_MAX = 399;
const STATUS_UNAUTHORIZED = 401;
const STATUS_FORBIDDEN = 403;

function timeoutVerdict(): ProbeVerdict {
  return { ok: false, detail: `timeout after ${PROBE_TIMEOUT_SECONDS} s` };
}

/** Cloudflare Access service-token headers; attached only to targets with `access: true`. */
function buildHeaders({ target, env }: { target: Target; env: Env }): Headers {
  const headers = new Headers();
  if (target.access && env.CF_ACCESS_CLIENT_ID && env.CF_ACCESS_CLIENT_SECRET) {
    headers.set('CF-Access-Client-Id', env.CF_ACCESS_CLIENT_ID);
    headers.set('CF-Access-Client-Secret', env.CF_ACCESS_CLIENT_SECRET);
  }
  return headers;
}

/**
 * Failures that are decided from the status line alone: a redirect (an Access login redirect for
 * Access-protected targets) or, for Access targets, a 401/403. Returns null when the response
 * should be judged by the target's own `evaluate`.
 */
function classifyStatus({ target, status }: { target: Target; status: number }): ProbeVerdict | null {
  if (status >= STATUS_REDIRECT_MIN && status <= STATUS_REDIRECT_MAX) {
    const label = target.access ? 'access denied' : 'unexpected redirect';
    return { ok: false, detail: `${label} (HTTP ${status})` };
  }
  if (target.access && (status === STATUS_UNAUTHORIZED || status === STATUS_FORBIDDEN)) {
    return { ok: false, detail: `access denied (HTTP ${status})` };
  }
  return null;
}

/** Releases an unread response body; never throws. */
export async function discardBody(response: Response): Promise<void> {
  try {
    await response.body?.cancel();
  } catch {
    // Nothing to do: the body is only being released.
  }
}

/**
 * Probes one target. Never throws: a network error, a timeout or a malformed body becomes a down
 * verdict. The timeout covers the response body read as well as the request itself, and
 * `redirect: "manual"` keeps an Access login redirect from being followed into a 200 login page.
 */
export async function probeTarget({
  target,
  env,
  now,
}: {
  target: Target;
  env: Env;
  now: number;
}): Promise<ProbeResult> {
  const startedAt = Date.now();
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), PROBE_TIMEOUT_MS);
  let httpStatus: number | null = null;
  const finish = (verdict: ProbeVerdict): ProbeResult => ({
    id: target.id,
    verdict,
    httpStatus,
    durationMs: Date.now() - startedAt,
  });
  try {
    const response = await fetch(target.url, {
      method: 'GET',
      headers: buildHeaders({ target, env }),
      redirect: 'manual',
      signal: abort.signal,
    });
    httpStatus = response.status;
    const decided = classifyStatus({ target, status: response.status });
    if (decided !== null) {
      await discardBody(response);
      return finish(decided);
    }
    const verdict = await target.evaluate({ response, now });
    // A body read that was cut off by the timeout is swallowed by the evaluators' JSON parsing, so
    // the abort flag, not the verdict text, decides whether this was a timeout.
    return finish(abort.signal.aborted ? timeoutVerdict() : verdict);
  } catch (error) {
    if (abort.signal.aborted) {
      return finish(timeoutVerdict());
    }
    const name = error instanceof Error ? error.name : 'Error';
    return finish({ ok: false, detail: `request failed (${name})` });
  } finally {
    clearTimeout(timer);
  }
}

const DEFAULT_RETRY_DELAY_MS = 2_000;

/** The override must be a finite number between 0 and the probe timeout; anything else gets the default. */
function retryDelayMs(env: Env): number {
  const raw = env.PROBE_RETRY_DELAY_MS?.trim();
  if (raw === undefined || raw === '') {
    return DEFAULT_RETRY_DELAY_MS;
  }
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed >= 0 && parsed <= PROBE_TIMEOUT_MS ? parsed : DEFAULT_RETRY_DELAY_MS;
}

function describeDetail(verdict: ProbeVerdict): string {
  return JSON.stringify(verdict.detail ?? 'unknown failure');
}

/**
 * One line per attempt: enough to see afterwards what each probe returned and how long it took.
 * Logging convention: `watchdog: <event> key=value ...`, one line per event, no secrets or URLs.
 */
function logProbe({ result, attempt }: { result: ProbeResult; attempt: number }): void {
  const detail = result.verdict.ok ? '' : ` detail=${describeDetail(result.verdict)}`;
  console.log(
    `watchdog: probe id=${result.id} attempt=${attempt} ok=${result.verdict.ok} ` +
      `http=${result.httpStatus ?? 'none'} duration_ms=${result.durationMs}${detail}`,
  );
}

/**
 * Probes a target and, when the first attempt fails, probes it once more after a short pause, so one
 * transient failure (a stray 520, a dropped connection) does not alert. The retry's verdict is the
 * one reported. Both attempts are logged, and a failure that the retry cleared is logged as a warning.
 */
export async function probeWithRetry({
  target,
  env,
  now,
}: {
  target: Target;
  env: Env;
  now: number;
}): Promise<ProbeResult> {
  const first = await probeTarget({ target, env, now });
  logProbe({ result: first, attempt: 1 });
  if (first.verdict.ok) {
    return first;
  }
  await new Promise<void>((resolve) => setTimeout(resolve, retryDelayMs(env)));
  const second = await probeTarget({ target, env, now });
  logProbe({ result: second, attempt: 2 });
  if (second.verdict.ok) {
    console.warn(`watchdog: probe recovered on retry id=${target.id} first_detail=${describeDetail(first.verdict)}`);
  }
  return second;
}

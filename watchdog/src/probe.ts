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
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), PROBE_TIMEOUT_MS);
  try {
    const response = await fetch(target.url, {
      method: 'GET',
      headers: buildHeaders({ target, env }),
      redirect: 'manual',
      signal: abort.signal,
    });
    const decided = classifyStatus({ target, status: response.status });
    if (decided !== null) {
      await discardBody(response);
      return { id: target.id, verdict: decided };
    }
    const verdict = await target.evaluate({ response, now });
    // A body read that was cut off by the timeout is swallowed by the evaluators' JSON parsing, so
    // the abort flag, not the verdict text, decides whether this was a timeout.
    return { id: target.id, verdict: abort.signal.aborted ? timeoutVerdict() : verdict };
  } catch (error) {
    if (abort.signal.aborted) {
      return { id: target.id, verdict: timeoutVerdict() };
    }
    const name = error instanceof Error ? error.name : 'Error';
    return { id: target.id, verdict: { ok: false, detail: `request failed (${name})` } };
  } finally {
    clearTimeout(timer);
  }
}

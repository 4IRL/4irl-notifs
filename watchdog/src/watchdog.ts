import { formatDuration, formatSince, truncateContent } from './format';
import { PROBE_TIMEOUT_MS, discardBody, probeTarget } from './probe';
import { readState, writeState } from './state';
import type { StateRead } from './state';
import { TARGETS } from './targets';
import type { Env, ProbeResult, TargetState, WatchdogState } from './types';

/** The first run at or after this UTC hour on a new UTC date also posts the daily summary. */
export const SUMMARY_HOUR_UTC = 16;

const UNKNOWN_STATE_WARNING = '⚠️ Previous state unavailable, reporting all failing targets.';

interface Reconciled {
  next: WatchdogState;
  changeLines: string[];
}

/**
 * Compares this run's probe results with the previous state. A target with no prior record that is
 * down is reported as newly down; one that is up is recorded silently. A target that stays down
 * keeps its original `since` and `detail`, so a changing detail never causes a write by itself.
 */
function reconcile({
  previous,
  results,
  nowIso,
  nowMs,
}: {
  previous: WatchdogState | null;
  results: readonly ProbeResult[];
  nowIso: string;
  nowMs: number;
}): Reconciled {
  const targets: Record<string, TargetState> = {};
  const changeLines: string[] = [];
  for (const { id, verdict } of results) {
    const prior = previous?.targets[id];
    if (!verdict.ok) {
      if (prior?.status === 'down') {
        targets[id] = prior;
        continue;
      }
      const detail = verdict.detail ?? 'unknown failure';
      targets[id] = { status: 'down', since: nowIso, detail };
      changeLines.push(`🔴 **DOWN** \`${id}\`: ${detail}`);
      continue;
    }
    if (prior?.status === 'up') {
      targets[id] = prior;
      continue;
    }
    targets[id] = { status: 'up', since: nowIso, detail: null };
    if (prior?.status === 'down') {
      const elapsed = formatDuration(nowMs - Date.parse(prior.since));
      changeLines.push(`🟢 **RECOVERED** \`${id}\` after ${elapsed}`);
    }
  }
  return {
    next: { version: 1, targets, last_summary_date: previous?.last_summary_date ?? null },
    changeLines,
  };
}

function summaryBlock(targets: Record<string, TargetState>): string {
  const entries = Object.entries(targets);
  const upCount = entries.filter(([, state]) => state.status === 'up').length;
  const lines = [`📋 Daily watchdog summary: ${upCount}/${entries.length} up`];
  for (const [id, state] of entries) {
    const since = formatSince(state.since);
    lines.push(
      state.status === 'up'
        ? `🟢 \`${id}\` up since ${since}`
        : `🔴 \`${id}\` down since ${since}: ${state.detail ?? 'unknown failure'}`,
    );
  }
  return lines.join('\n');
}

/** One message per run. Returns false on a non-2xx answer or a throw; the webhook URL is never logged. */
async function postToDiscord({ webhookUrl, content }: { webhookUrl: string; content: string }): Promise<boolean> {
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), PROBE_TIMEOUT_MS);
  try {
    const response = await fetch(webhookUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: truncateContent(content), allowed_mentions: { parse: [] } }),
      signal: abort.signal,
    });
    await discardBody(response);
    if (!response.ok) {
      console.error(`watchdog: Discord post failed with HTTP ${response.status}`);
    }
    return response.ok;
  } catch (error) {
    console.error('watchdog: Discord post threw', error instanceof Error ? error.name : 'Error');
    return false;
  } finally {
    clearTimeout(timer);
  }
}

function buildMessage({
  stateRead,
  changeLines,
  summary,
}: {
  stateRead: StateRead;
  changeLines: readonly string[];
  summary: string | null;
}): string | null {
  const sections: string[] = [];
  if (changeLines.length > 0) {
    const header = stateRead.kind === 'unavailable' ? [UNKNOWN_STATE_WARNING] : [];
    sections.push([...header, ...changeLines].join('\n'));
  }
  if (summary !== null) {
    sections.push(summary);
  }
  return sections.length === 0 ? null : sections.join('\n\n');
}

/**
 * One cron run: probe every target while reading the state key once, work out what changed, post at
 * most one Discord message, then write the state at most once. If the Discord post fails the state
 * is not written, so the next run retries the same transitions and summary. If the KV read threw and
 * nothing was posted, the state is not written either.
 */
export async function runWatchdog({ env, now }: { env: Env; now: number }): Promise<void> {
  const webhookUrl = env.DISCORD_WEBHOOK_URL;
  if (!webhookUrl) {
    console.error('watchdog: DISCORD_WEBHOOK_URL is not configured; skipping this run');
    return;
  }
  if (!env.CF_ACCESS_CLIENT_ID || !env.CF_ACCESS_CLIENT_SECRET) {
    console.warn('watchdog: Cloudflare Access credentials are not configured; Access-protected probes will fail');
  }

  const [stateRead, results] = await Promise.all([
    readState(env.WATCHDOG_STATE),
    Promise.all(TARGETS.map((target) => probeTarget({ target, env, now }))),
  ]);

  const previous = stateRead.kind === 'loaded' ? stateRead.state : null;
  const nowIso = new Date(now).toISOString();
  const { next, changeLines } = reconcile({ previous, results, nowIso, nowMs: now });

  const today = nowIso.slice(0, 10);
  // The summary is only decided on a run that could read the state: with an unreadable state the
  // last summary date is unknown and the summary would otherwise repeat on every run after 16:00.
  const summaryDue =
    stateRead.kind === 'loaded' &&
    new Date(now).getUTCHours() >= SUMMARY_HOUR_UTC &&
    next.last_summary_date !== today;
  if (summaryDue) {
    next.last_summary_date = today;
  }

  const content = buildMessage({
    stateRead,
    changeLines,
    summary: summaryDue ? summaryBlock(next.targets) : null,
  });
  if (content !== null) {
    const posted = await postToDiscord({ webhookUrl, content });
    if (!posted) {
      return;
    }
  } else if (stateRead.kind === 'unavailable' && stateRead.reason === 'read_failed') {
    // A transient KV read failure with nothing to post: `next` was reconciled against no previous
    // state, so writing it would reset last_summary_date and every up target's `since`.
    return;
  }

  const serialized = JSON.stringify(next);
  const unchanged = stateRead.kind === 'loaded' && stateRead.raw === serialized;
  if (!unchanged) {
    await writeState({ kv: env.WATCHDOG_STATE, serialized });
  }
}

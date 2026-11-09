import type { HeldState, TargetState, WatchdogState } from './types';

export const STATE_KEY = 'state';

export type StateRead =
  /** `state` is null when nothing is stored yet (first run). `raw` is what was read, for change detection. */
  | { kind: 'loaded'; state: WatchdogState | null; raw: string | null }
  /** `read_failed`: the KV read threw. `invalid`: the stored value is not a valid state document. */
  | { kind: 'unavailable'; reason: 'read_failed' | 'invalid' };

export function emptyState(): WatchdogState {
  return { version: 1, targets: {}, last_summary_date: null };
}

function isTargetState(value: unknown): value is TargetState {
  if (typeof value !== 'object' || value === null) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    (candidate.status === 'up' || candidate.status === 'down') &&
    typeof candidate.since === 'string' &&
    !Number.isNaN(Date.parse(candidate.since)) &&
    (candidate.detail === null || typeof candidate.detail === 'string')
  );
}

const HELD_MALFORMED_WARNING = 'watchdog: stored held state is malformed, dropping it';
const ISO_DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;

function isParseableTimestamp(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value));
}

/** A `YYYY-MM-DD` that is a real calendar date (rejects roll-overs such as 2026-02-30). */
function isRealDate(value: string): boolean {
  const parsed = Date.parse(value);
  return !Number.isNaN(parsed) && new Date(parsed).toISOString().slice(0, 10) === value;
}

function isHeldState(value: unknown): value is HeldState {
  if (typeof value !== 'object' || value === null) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    isParseableTimestamp(candidate.alert_since) &&
    typeof candidate.last_reminder_date === 'string' &&
    ISO_DATE_PATTERN.test(candidate.last_reminder_date) &&
    isRealDate(candidate.last_reminder_date) &&
    (candidate.zero_seen_at === null || isParseableTimestamp(candidate.zero_seen_at))
  );
}

/** Parses a stored state document; returns null for anything that is not a valid version-1 state. */
export function parseState(raw: string): WatchdogState | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== 'object' || parsed === null) {
    return null;
  }
  const candidate = parsed as Record<string, unknown>;
  if (candidate.version !== 1) {
    return null;
  }
  const summaryDate = candidate.last_summary_date;
  if (summaryDate !== null && typeof summaryDate !== 'string') {
    return null;
  }
  const rawTargets = candidate.targets;
  if (typeof rawTargets !== 'object' || rawTargets === null) {
    return null;
  }
  const targets: Record<string, TargetState> = {};
  for (const [id, value] of Object.entries(rawTargets)) {
    if (!isTargetState(value)) {
      return null;
    }
    targets[id] = { status: value.status, since: value.since, detail: value.detail };
  }
  const state: WatchdogState = { version: 1, targets, last_summary_date: summaryDate };
  if (candidate.held !== undefined) {
    if (isHeldState(candidate.held)) {
      const { alert_since, last_reminder_date, zero_seen_at } = candidate.held;
      state.held = { alert_since, last_reminder_date, zero_seen_at };
    } else {
      console.warn(HELD_MALFORMED_WARNING);
    }
  }
  return state;
}

/** Exactly one `get` per run. A throw or an unparseable value is reported as unavailable. */
export async function readState(kv: KVNamespace): Promise<StateRead> {
  let raw: string | null;
  try {
    raw = await kv.get(STATE_KEY);
  } catch (error) {
    console.error('watchdog: KV state read failed', error instanceof Error ? error.name : 'Error');
    return { kind: 'unavailable', reason: 'read_failed' };
  }
  if (raw === null) {
    return { kind: 'loaded', state: null, raw: null };
  }
  const state = parseState(raw);
  if (state === null) {
    console.error('watchdog: stored state is not a valid version-1 document');
    return { kind: 'unavailable', reason: 'invalid' };
  }
  return { kind: 'loaded', state, raw };
}

/** A failed write is logged and never propagated (the post, if any, has already gone out); returns whether it succeeded. */
export async function writeState({ kv, serialized }: { kv: KVNamespace; serialized: string }): Promise<boolean> {
  try {
    await kv.put(STATE_KEY, serialized);
    return true;
  } catch (error) {
    console.error('watchdog: KV state write failed', error instanceof Error ? error.name : 'Error');
    return false;
  }
}

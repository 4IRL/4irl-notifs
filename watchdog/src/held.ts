import { formatDuration, formatSince } from './format';
import type { HeldApp, HeldSnapshot, HeldState } from './types';

export const HELD_ALERT_AGE_SECONDS = 900;
export const HELD_ALERT_MESSAGE_COUNT = 25;
export const HELD_ALERT_UNASSIGNED_COUNT = 1;
export const HELD_APPS_SHOWN = 10;
export const HELD_APP_ID_MAX = 40;
export const HELD_REMINDER_MIN_LEAD_HOURS = 6;

const MS_PER_SECOND = 1000;
const MS_PER_HOUR = 3_600_000;
const MS_PER_DAY = 86_400_000;
const ELLIPSIS = '…';
const UNNAMED_APP = '(unnamed)';
const ALERT_PREFIX = '🟠 **HELD**';
const STALE_LINE = '⚠️ Held counts are stale (delivery-api could not refresh them).';
const TTL_NOTE_LINE = 'ℹ️ Held messages are deleted after 7 days (default).';
const MESSAGES_SUFFIX = ': add a route or destination';
const UNASSIGNED_SUFFIX = ': assign the check a destination';

function plural({ count, singular, pluralForm }: { count: number; singular: string; pluralForm: string }): string {
  return count === 1 ? singular : pluralForm;
}

function sanitiseAppId(appId: string): string {
  // eslint-disable-next-line no-control-regex
  const cleaned = appId.replace(/`/g, '').replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, ' ').trim();
  if (cleaned === '') {
    return UNNAMED_APP;
  }
  const chars = Array.from(cleaned);
  if (chars.length <= HELD_APP_ID_MAX) {
    return cleaned;
  }
  return `${chars.slice(0, HELD_APP_ID_MAX - 1).join('')}${ELLIPSIS}`;
}

function appsLine(snapshot: HeldSnapshot): string | null {
  if (snapshot.apps.length === 0) {
    return null;
  }
  const shown = snapshot.apps.slice(0, HELD_APPS_SHOWN);
  const parts = shown.map((app) => `\`${sanitiseAppId(app.appId)}\` ${app.count}`);
  const more = snapshot.apps.length - shown.length + snapshot.appsMore;
  if (more > 0) {
    parts.push(`+${more} more`);
  }
  return `↳ ${parts.join(', ')}`;
}

/** The held section for a first alert or a reminder; `prefix` is the only difference between them. */
function heldLines({ snapshot, prefix, reminder }: { snapshot: HeldSnapshot; prefix: string; reminder: boolean }): string[] {
  const lines: string[] = [];
  if (snapshot.messages > 0) {
    const noun = plural({ count: snapshot.messages, singular: 'app message', pluralForm: 'app messages' });
    const oldest =
      snapshot.oldestAgeSeconds === null ? '' : `, oldest ${formatDuration(snapshot.oldestAgeSeconds * MS_PER_SECOND)}`;
    lines.push(`${prefix} ${snapshot.messages} ${noun} waiting${oldest}${MESSAGES_SUFFIX}`);
    const apps = appsLine(snapshot);
    if (apps !== null) {
      lines.push(apps);
    }
    if (reminder) {
      lines.push(TTL_NOTE_LINE);
    }
  }
  if (snapshot.unassigned > 0) {
    const noun = plural({ count: snapshot.unassigned, singular: 'heartbeat alert', pluralForm: 'heartbeat alerts' });
    lines.push(`${prefix} ${snapshot.unassigned} ${noun} waiting for a destination${UNASSIGNED_SUFFIX}`);
  }
  if (snapshot.stale) {
    lines.push(STALE_LINE);
  }
  return lines;
}

function triggersAlert(snapshot: HeldSnapshot): boolean {
  return (
    snapshot.unassigned >= HELD_ALERT_UNASSIGNED_COUNT ||
    snapshot.messages >= HELD_ALERT_MESSAGE_COUNT ||
    (snapshot.oldestAgeSeconds !== null && snapshot.oldestAgeSeconds >= HELD_ALERT_AGE_SECONDS)
  );
}

/** UTC date before the one `nowMs` falls on; derived from the clock value so it cannot throw. */
function dateBefore(nowMs: number): string {
  return new Date(nowMs - MS_PER_DAY).toISOString().slice(0, 10);
}

/** The reminder date stored at alert time (Decision rule 2): the previous date only with enough lead before today's reminder instant. */
function initialReminderDate({
  nowMs,
  today,
  hourUtc,
  reminderHourUtc,
}: {
  nowMs: number;
  today: string;
  hourUtc: number;
  reminderHourUtc: number;
}): string {
  const reminderInstantMs = Date.parse(today) + reminderHourUtc * MS_PER_HOUR;
  const enoughLead = reminderInstantMs - nowMs >= HELD_REMINDER_MIN_LEAD_HOURS * MS_PER_HOUR;
  return hourUtc < reminderHourUtc && enoughLead ? dateBefore(nowMs) : today;
}

/**
 * Pure held-alert decision: first alert, daily reminder, recovery after two consecutive fresh
 * all-zero runs. `nowMs` and `today` come from the cron's scheduled time, never the clock.
 */
export function decideHeld({
  previous,
  snapshot,
  nowIso,
  nowMs,
  today,
  hourUtc,
  reminderHourUtc,
}: {
  previous: HeldState | undefined;
  snapshot: HeldSnapshot | undefined;
  nowIso: string;
  nowMs: number;
  today: string;
  hourUtc: number;
  reminderHourUtc: number;
}): { next: HeldState | undefined; lines: string[] } {
  if (snapshot === undefined) {
    return { next: previous, lines: [] };
  }
  if (previous === undefined) {
    if (!triggersAlert(snapshot)) {
      return { next: undefined, lines: [] };
    }
    return {
      next: {
        alert_since: nowIso,
        last_reminder_date: initialReminderDate({ nowMs, today, hourUtc, reminderHourUtc }),
        zero_seen_at: null,
      },
      lines: heldLines({ snapshot, prefix: ALERT_PREFIX, reminder: false }),
    };
  }
  if (snapshot.messages + snapshot.unassigned === 0) {
    if (snapshot.stale) {
      return { next: previous, lines: [] };
    }
    if (previous.zero_seen_at === null) {
      return { next: { ...previous, zero_seen_at: nowIso }, lines: [] };
    }
    const elapsed = formatDuration(nowMs - Date.parse(previous.alert_since));
    return { next: undefined, lines: [`🟢 **HELD CLEARED** after ${elapsed}`] };
  }
  const next: HeldState = { ...previous, zero_seen_at: null };
  if (hourUtc >= reminderHourUtc && previous.last_reminder_date !== today) {
    next.last_reminder_date = today;
    const prefix = `⏰ **STILL HELD** (since ${formatSince(previous.alert_since)})`;
    return { next, lines: heldLines({ snapshot, prefix, reminder: true }) };
  }
  return { next, lines: [] };
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

/** Reads `{count}` from an optional group: absent => 0, present but malformed => null. */
function readGroupCount(group: unknown): number | null {
  if (group === undefined) {
    return 0;
  }
  const record = asRecord(group);
  if (record === null || !isCount(record.count)) {
    return null;
  }
  return record.count;
}

function readApps(value: unknown): HeldApp[] | null {
  if (value === undefined) {
    return [];
  }
  if (!Array.isArray(value)) {
    return null;
  }
  const apps: HeldApp[] = [];
  for (const entry of value) {
    const record = asRecord(entry);
    if (record === null || typeof record.app_id !== 'string' || record.app_id === '' || !isCount(record.count)) {
      return null;
    }
    apps.push({ appId: record.app_id, count: record.count });
  }
  return apps;
}

/**
 * Validates delivery-api's `/readyz` `held` block. Fails closed: returns null (held state left
 * untouched) for an unavailable block or any malformed field, never zero counts. `total` and
 * `as_of` are ignored; the total is recomputed from the parts.
 */
export function parseHeldBlock(value: unknown): HeldSnapshot | null {
  const block = asRecord(value);
  if (block === null || block.available !== true) {
    return null;
  }
  if (block.stale !== undefined && typeof block.stale !== 'boolean') {
    return null;
  }
  const messages = readGroupCount(block.messages);
  const unassigned = readGroupCount(block.unassigned_alerts);
  const apps = readApps(block.apps);
  if (messages === null || unassigned === null || apps === null) {
    return null;
  }
  let oldestAgeSeconds: number | null = null;
  const messagesGroup = asRecord(block.messages);
  const rawAge = messagesGroup?.oldest_age_seconds;
  if (rawAge !== undefined && rawAge !== null) {
    if (!isCount(rawAge)) {
      return null;
    }
    oldestAgeSeconds = rawAge;
  }
  const rawMore = block.apps_more;
  if (rawMore !== undefined && !isCount(rawMore)) {
    return null;
  }
  return {
    stale: block.stale === true,
    messages,
    oldestAgeSeconds,
    unassigned,
    apps,
    appsMore: rawMore ?? 0,
  };
}

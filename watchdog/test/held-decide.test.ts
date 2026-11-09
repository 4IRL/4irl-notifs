import { describe, expect, it } from 'vitest';
import { DISCORD_CONTENT_LIMIT } from '../src/format';
import { HELD_APPS_SHOWN, HELD_APP_ID_MAX, decideHeld } from '../src/held';
import type { HeldSnapshot, HeldState } from '../src/types';

const REMINDER_HOUR = 16;

function snap(overrides: Partial<HeldSnapshot> = {}): HeldSnapshot {
  return { stale: false, messages: 0, oldestAgeSeconds: null, unassigned: 0, apps: [], appsMore: 0, ...overrides };
}

function run({
  at,
  previous,
  snapshot,
  reminderHourUtc = REMINDER_HOUR,
}: {
  at: string;
  previous?: HeldState;
  snapshot?: HeldSnapshot;
  reminderHourUtc?: number;
}) {
  const date = new Date(at);
  return decideHeld({
    previous,
    snapshot,
    nowIso: date.toISOString(),
    nowMs: date.getTime(),
    today: date.toISOString().slice(0, 10),
    hourUtc: date.getUTCHours(),
    reminderHourUtc,
  });
}

function open(overrides: Partial<HeldState> = {}): HeldState {
  return { alert_since: '2026-11-08T12:00:00.000Z', last_reminder_date: '2026-11-08', zero_seen_at: null, ...overrides };
}

describe('decideHeld no snapshot', () => {
  it('leaves a closed state closed', () => {
    expect(run({ at: '2026-11-09T12:00:00Z' })).toEqual({ next: undefined, lines: [] });
  });
  it('leaves an open alert as it was', () => {
    const previous = open({ zero_seen_at: '2026-11-09T11:00:00.000Z' });
    expect(run({ at: '2026-11-09T12:00:00Z', previous })).toEqual({ next: previous, lines: [] });
  });
});

describe('decideHeld first alert thresholds', () => {
  const NOW = '2026-11-09T12:00:00Z';
  const cases: Array<[string, HeldSnapshot, boolean]> = [
    ['all zero', snap(), false],
    ['below every threshold', snap({ messages: 24, oldestAgeSeconds: 899 }), false],
    ['age 899', snap({ messages: 1, oldestAgeSeconds: 899 }), false],
    ['age 900', snap({ messages: 1, oldestAgeSeconds: 900 }), true],
    ['24 messages', snap({ messages: 24, oldestAgeSeconds: 10 }), false],
    ['25 messages', snap({ messages: 25, oldestAgeSeconds: 10 }), true],
    ['0 unassigned', snap({ unassigned: 0 }), false],
    ['1 unassigned', snap({ unassigned: 1 }), true],
    ['stale but over threshold', snap({ stale: true, messages: 30 }), true],
  ];
  it.each(cases)('%s', (_label, snapshot, alerts) => {
    const result = run({ at: NOW, snapshot });
    if (alerts) {
      expect(result.next).toEqual({
        alert_since: '2026-11-09T12:00:00.000Z',
        last_reminder_date: '2026-11-09',
        zero_seen_at: null,
      });
      expect(result.lines.length).toBeGreaterThan(0);
    } else {
      expect(result).toEqual({ next: undefined, lines: [] });
    }
  });
});

describe('decideHeld last_reminder_date at alert time', () => {
  const snapshot = snap({ unassigned: 1 });
  const cases: Array<[string, string, string]> = [
    ['09:59', '2026-11-09T09:59:00Z', '2026-11-08'],
    ['10:00 exactly', '2026-11-09T10:00:00Z', '2026-11-08'],
    ['10:01', '2026-11-09T10:01:00Z', '2026-11-09'],
    ['15:55', '2026-11-09T15:55:00Z', '2026-11-09'],
    ['16:00', '2026-11-09T16:00:00Z', '2026-11-09'],
    ['23:00', '2026-11-09T23:00:00Z', '2026-11-09'],
    ['00:30 (previous date across a month boundary)', '2026-12-01T00:30:00Z', '2026-11-30'],
  ];
  it.each(cases)('alert at %s', (_label, at, expected) => {
    expect(run({ at, snapshot }).next?.last_reminder_date).toBe(expected);
  });

  it('reminder fires at 16:00 the same day after a 09:59 or 10:00 alert', () => {
    for (const alertAt of ['2026-11-09T09:59:00Z', '2026-11-09T10:00:00Z']) {
      const previous = run({ at: alertAt, snapshot }).next;
      const result = run({ at: '2026-11-09T16:00:00Z', previous, snapshot });
      expect(result.lines.length).toBeGreaterThan(0);
      expect(result.next?.last_reminder_date).toBe('2026-11-09');
    }
  });

  it('no reminder at 16:00 the same day after a 10:01, 15:55 or 16:00 alert', () => {
    for (const alertAt of ['2026-11-09T10:01:00Z', '2026-11-09T15:55:00Z', '2026-11-09T16:00:00Z']) {
      const previous = run({ at: alertAt, snapshot }).next;
      const result = run({ at: '2026-11-09T16:00:00Z', previous, snapshot });
      expect(result.lines).toEqual([]);
    }
  });

  it('honours reminderHourUtc as a parameter', () => {
    expect(run({ at: '2026-11-09T02:00:00Z', snapshot, reminderHourUtc: 8 }).next?.last_reminder_date).toBe('2026-11-08');
    expect(run({ at: '2026-11-09T02:01:00Z', snapshot, reminderHourUtc: 8 }).next?.last_reminder_date).toBe('2026-11-09');
    expect(run({ at: '2026-11-09T14:01:00Z', snapshot, reminderHourUtc: 20 }).next?.last_reminder_date).toBe('2026-11-09');
    expect(run({ at: '2026-11-09T14:00:00Z', snapshot, reminderHourUtc: 20 }).next?.last_reminder_date).toBe('2026-11-08');
  });
});

describe('decideHeld open alert with total > 0', () => {
  const snapshot = snap({ messages: 3, oldestAgeSeconds: 60 });
  it('no reminder before the reminder hour', () => {
    const previous = open({ last_reminder_date: '2026-11-08' });
    const result = run({ at: '2026-11-09T15:59:00Z', previous, snapshot });
    expect(result).toEqual({ next: previous, lines: [] });
  });
  it('one reminder at the reminder hour on a later date, date updated', () => {
    const previous = open({ last_reminder_date: '2026-11-08' });
    const result = run({ at: '2026-11-09T16:00:00Z', previous, snapshot });
    expect(result.lines.length).toBeGreaterThan(0);
    expect(result.next).toEqual({ ...previous, last_reminder_date: '2026-11-09' });
  });
  it('no second reminder the same date', () => {
    const previous = open({ last_reminder_date: '2026-11-09' });
    const result = run({ at: '2026-11-09T17:00:00Z', previous, snapshot });
    expect(result).toEqual({ next: previous, lines: [] });
  });
  it('stays open below the threshold and posts no new alert', () => {
    const previous = open({ last_reminder_date: '2026-11-09' });
    const result = run({ at: '2026-11-09T12:00:00Z', previous, snapshot: snap({ messages: 1, oldestAgeSeconds: 1 }) });
    expect(result).toEqual({ next: previous, lines: [] });
  });
  it('decides a stale non-zero snapshot on its counts', () => {
    const previous = open({ last_reminder_date: '2026-11-08' });
    const result = run({ at: '2026-11-09T16:00:00Z', previous, snapshot: snap({ stale: true, messages: 2 }) });
    expect(result.next?.last_reminder_date).toBe('2026-11-09');
    expect(result.lines.length).toBeGreaterThan(0);
  });
  it('clears zero_seen_at', () => {
    const previous = open({ zero_seen_at: '2026-11-09T11:00:00.000Z', last_reminder_date: '2026-11-09' });
    const result = run({ at: '2026-11-09T12:00:00Z', previous, snapshot });
    expect(result.next).toEqual({ ...previous, zero_seen_at: null });
    expect(result.lines).toEqual([]);
  });
});

describe('decideHeld recovery', () => {
  const zero = snap();
  it('first fresh zero sets zero_seen_at with no lines', () => {
    const previous = open();
    const result = run({ at: '2026-11-09T12:00:00Z', previous, snapshot: zero });
    expect(result).toEqual({ next: { ...previous, zero_seen_at: '2026-11-09T12:00:00.000Z' }, lines: [] });
  });
  it('second consecutive fresh zero posts recovery and closes', () => {
    const previous = open({ zero_seen_at: '2026-11-09T12:00:00.000Z' });
    const result = run({ at: '2026-11-09T12:05:00Z', previous, snapshot: zero });
    expect(result.next).toBeUndefined();
    expect(result.lines).toEqual(['🟢 **HELD CLEARED** after 24 h 5 min']);
  });
  it('fresh zero, stale non-zero, fresh zero does not close', () => {
    let state: HeldState | undefined = open();
    state = run({ at: '2026-11-09T12:00:00Z', previous: state, snapshot: zero }).next;
    state = run({ at: '2026-11-09T12:05:00Z', previous: state, snapshot: snap({ stale: true, messages: 1 }) }).next;
    expect(state?.zero_seen_at).toBeNull();
    const last = run({ at: '2026-11-09T12:10:00Z', previous: state, snapshot: zero });
    expect(last.next).toBeDefined();
    expect(last.lines).toEqual([]);
  });
  it('fresh zero, no snapshot, fresh zero closes', () => {
    let state: HeldState | undefined = open();
    state = run({ at: '2026-11-09T12:00:00Z', previous: state, snapshot: zero }).next;
    state = run({ at: '2026-11-09T12:05:00Z', previous: state }).next;
    const last = run({ at: '2026-11-09T12:10:00Z', previous: state, snapshot: zero });
    expect(last.next).toBeUndefined();
    expect(last.lines).toHaveLength(1);
  });
  it('a stale zero never advances the streak and leaves zero_seen_at as it was', () => {
    const stale = snap({ stale: true });
    const fresh = open();
    expect(run({ at: '2026-11-09T12:00:00Z', previous: fresh, snapshot: stale })).toEqual({ next: fresh, lines: [] });
    const seen = open({ zero_seen_at: '2026-11-09T12:00:00.000Z' });
    expect(run({ at: '2026-11-09T12:05:00Z', previous: seen, snapshot: stale })).toEqual({ next: seen, lines: [] });
  });
  it('fresh zero, stale zero, fresh zero does not close on the stale run', () => {
    let state: HeldState | undefined = open();
    state = run({ at: '2026-11-09T12:00:00Z', previous: state, snapshot: zero }).next;
    const staleRun = run({ at: '2026-11-09T12:05:00Z', previous: state, snapshot: snap({ stale: true }) });
    expect(staleRun.next).toEqual(state);
    expect(staleRun.lines).toEqual([]);
    const last = run({ at: '2026-11-09T12:10:00Z', previous: staleRun.next, snapshot: zero });
    expect(last.next).toBeUndefined();
    expect(last.lines).toHaveLength(1);
  });
});

describe('decideHeld lines', () => {
  const ALERT_AT = '2026-11-09T12:00:00Z';
  const REMINDER_AT = '2026-11-09T16:00:00Z';
  const prior = open({ last_reminder_date: '2026-11-08' });

  function alertLines(snapshot: HeldSnapshot): string[] {
    return run({ at: ALERT_AT, snapshot }).lines;
  }
  function reminderLines(snapshot: HeldSnapshot): string[] {
    return run({ at: REMINDER_AT, previous: prior, snapshot }).lines;
  }

  it('alert with messages, oldest and apps', () => {
    const lines = alertLines(
      snap({ messages: 3, oldestAgeSeconds: 1200, apps: [{ appId: 'u4i', count: 2 }, { appId: 'chores', count: 1 }] }),
    );
    expect(lines).toEqual([
      '🟠 **HELD** 3 app messages waiting, oldest 20 min: add a route or destination',
      '↳ `u4i` 2, `chores` 1',
    ]);
  });
  it('singular and plural', () => {
    expect(alertLines(snap({ messages: 1, oldestAgeSeconds: 900 }))[0]).toBe(
      '🟠 **HELD** 1 app message waiting, oldest 15 min: add a route or destination',
    );
    expect(alertLines(snap({ unassigned: 1 }))).toEqual([
      '🟠 **HELD** 1 heartbeat alert waiting for a destination: assign the check a destination',
    ]);
    expect(alertLines(snap({ unassigned: 2 }))).toEqual([
      '🟠 **HELD** 2 heartbeat alerts waiting for a destination: assign the check a destination',
    ]);
  });
  it('null oldest age drops the clause', () => {
    expect(alertLines(snap({ messages: 25 }))[0]).toBe(
      '🟠 **HELD** 25 app messages waiting: add a route or destination',
    );
  });
  it('reminder uses the STILL HELD prefix with the since time and the 7-day note', () => {
    const lines = reminderLines(snap({ messages: 2, oldestAgeSeconds: 7200, apps: [{ appId: 'a', count: 2 }] }));
    expect(lines).toEqual([
      '⏰ **STILL HELD** (since 11-08 12:00) 2 app messages waiting, oldest 2 h: add a route or destination',
      '↳ `a` 2',
      'ℹ️ Held messages are deleted after 7 days (default).',
    ]);
  });
  it('the 7-day note never appears on the first alert, an unassigned-only reminder or recovery', () => {
    expect(alertLines(snap({ messages: 25 })).join('\n')).not.toContain('7 days');
    const unassignedOnly = reminderLines(snap({ unassigned: 2 }));
    expect(unassignedOnly).toEqual([
      '⏰ **STILL HELD** (since 11-08 12:00) 2 heartbeat alerts waiting for a destination: assign the check a destination',
    ]);
    const recovery = run({
      at: '2026-11-09T12:05:00Z',
      previous: open({ zero_seen_at: '2026-11-09T12:00:00.000Z' }),
      snapshot: snap(),
    });
    expect(recovery.lines.join('\n')).not.toContain('7 days');
  });
  it('both held: order is messages, per-app, 7-day note, unassigned, stale (reminder)', () => {
    const lines = reminderLines(
      snap({ stale: true, messages: 2, oldestAgeSeconds: 60, unassigned: 1, apps: [{ appId: 'a', count: 2 }] }),
    );
    expect(lines).toEqual([
      '⏰ **STILL HELD** (since 11-08 12:00) 2 app messages waiting, oldest 1 min: add a route or destination',
      '↳ `a` 2',
      'ℹ️ Held messages are deleted after 7 days (default).',
      '⏰ **STILL HELD** (since 11-08 12:00) 1 heartbeat alert waiting for a destination: assign the check a destination',
      '⚠️ Held counts are stale (delivery-api could not refresh them).',
    ]);
  });
  it('both held: order is messages, per-app, unassigned, stale (alert)', () => {
    const lines = alertLines(
      snap({ stale: true, messages: 25, oldestAgeSeconds: 60, unassigned: 1, apps: [{ appId: 'a', count: 25 }] }),
    );
    expect(lines).toEqual([
      '🟠 **HELD** 25 app messages waiting, oldest 1 min: add a route or destination',
      '↳ `a` 25',
      '🟠 **HELD** 1 heartbeat alert waiting for a destination: assign the check a destination',
      '⚠️ Held counts are stale (delivery-api could not refresh them).',
    ]);
  });
  it('the stale line appears only when stale', () => {
    expect(alertLines(snap({ messages: 25 })).join('\n')).not.toContain('stale');
    expect(alertLines(snap({ messages: 25, stale: true })).at(-1)).toContain('stale');
  });
  it('caps apps at HELD_APPS_SHOWN with +K more combining remainder and appsMore', () => {
    const apps = Array.from({ length: 12 }, (_, index) => ({ appId: `app${index}`, count: 1 }));
    const line = alertLines(snap({ messages: 25, apps, appsMore: 5 }))[1] ?? '';
    expect(line.startsWith('↳ `app0` 1, `app1` 1')).toBe(true);
    expect(line).toContain('`app9` 1');
    expect(line).not.toContain('app10');
    expect(line.endsWith(', +7 more')).toBe(true);
  });
  it('+K more from appsMore alone, and none when K is 0', () => {
    expect(alertLines(snap({ messages: 25, apps: [{ appId: 'a', count: 25 }], appsMore: 3 }))[1]).toBe(
      '↳ `a` 25, +3 more',
    );
    expect(alertLines(snap({ messages: 25, apps: [{ appId: 'a', count: 25 }] }))[1]).toBe('↳ `a` 25');
  });
  it('removes backticks and replaces control characters and newlines with a space', () => {
    const line = alertLines(
      snap({ messages: 25, apps: [{ appId: 'a`b', count: 1 }, { appId: 'c\nd\te', count: 1 }] }),
    )[1];
    expect(line).toBe('↳ `ab` 1, `c d e` 1');
  });
  it('renders an id blank after sanitising as (unnamed)', () => {
    expect(alertLines(snap({ messages: 25, apps: [{ appId: '`', count: 4 }] }))[1]).toBe('↳ `(unnamed)` 4');
  });
  it('cuts a long id to HELD_APP_ID_MAX ending with an ellipsis within that length', () => {
    const line = alertLines(snap({ messages: 25, apps: [{ appId: 'x'.repeat(100), count: 1 }] }))[1];
    expect(line).toBe(`↳ \`${'x'.repeat(HELD_APP_ID_MAX - 1)}…\` 1`);
    const exact = alertLines(snap({ messages: 25, apps: [{ appId: 'y'.repeat(HELD_APP_ID_MAX), count: 1 }] }))[1];
    expect(exact).toBe(`↳ \`${'y'.repeat(HELD_APP_ID_MAX)}\` 1`);
  });
  it('cuts an astral id by code points without a lone surrogate', () => {
    const line = alertLines(snap({ messages: 25, apps: [{ appId: '😀'.repeat(45), count: 1 }] }))[1];
    expect(line).toBe(`↳ \`${'😀'.repeat(HELD_APP_ID_MAX - 1)}…\` 1`);
  });
  it('catches up a missed reminder after a multi-day gap, once', () => {
    const previous = open({ last_reminder_date: '2026-11-05' });
    const result = run({ at: '2026-11-09T17:00:00Z', previous, snapshot: snap({ messages: 2 }) });
    expect(result.next?.last_reminder_date).toBe('2026-11-09');
    expect(result.lines.length).toBeGreaterThan(0);
  });
  it('a section for 20 apps with 40-char ids stays well under the Discord limit', () => {
    const apps = Array.from({ length: 20 }, (_, index) => ({ appId: `${index}`.padEnd(60, 'z'), count: 1 }));
    const text = reminderLines(snap({ stale: true, messages: 99, oldestAgeSeconds: 99999, unassigned: 3, apps, appsMore: 9 })).join('\n');
    expect(text.length).toBeLessThan(DISCORD_CONTENT_LIMIT / 2);
  });
});

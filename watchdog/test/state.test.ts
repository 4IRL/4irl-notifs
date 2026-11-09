import { afterEach, describe, expect, it, vi } from 'vitest';
import { parseState } from '../src/state';

const HELD_WARNING = 'watchdog: stored held state is malformed, dropping it';

const VALID_HELD = {
  alert_since: '2026-11-09T10:00:00.000Z',
  last_reminder_date: '2026-11-08',
  zero_seen_at: null,
};

function stateJson({ held }: { held?: unknown }): string {
  const document: Record<string, unknown> = {
    version: 1,
    targets: { 'delivery-api': { status: 'up', since: '2026-11-09T09:00:00.000Z', detail: null } },
    last_summary_date: '2026-11-08',
  };
  if (held !== undefined) {
    document.held = held;
  }
  return JSON.stringify(document);
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('parseState held', () => {
  it('loads a document without held and adds no held key', () => {
    const state = parseState(stateJson({}));
    expect(state).not.toBeNull();
    expect(state).not.toHaveProperty('held');
  });

  it('round-trips a valid held byte-for-byte in contract key order', () => {
    const raw = stateJson({ held: VALID_HELD });
    const state = parseState(raw);
    expect(state?.held).toEqual(VALID_HELD);
    expect(JSON.stringify(state)).toBe(raw);
  });

  it('keeps a non-null zero_seen_at', () => {
    const held = { ...VALID_HELD, zero_seen_at: '2026-11-09T11:00:00.000Z' };
    expect(parseState(stateJson({ held }))?.held).toEqual(held);
  });

  it('drops extra fields of held', () => {
    const state = parseState(stateJson({ held: { ...VALID_HELD, extra: 1 } }));
    expect(state?.held).toEqual(VALID_HELD);
  });

  const malformed: Array<[string, unknown]> = [
    ['a string', 'held'],
    ['null', null],
    ['an array', []],
    ['alert_since missing', { last_reminder_date: '2026-11-08', zero_seen_at: null }],
    ['alert_since not a string', { ...VALID_HELD, alert_since: 5 }],
    ['alert_since unparseable', { ...VALID_HELD, alert_since: 'not-a-date' }],
    ['last_reminder_date not a string', { ...VALID_HELD, last_reminder_date: 20261108 }],
    ['last_reminder_date not a date', { ...VALID_HELD, last_reminder_date: 'not-a-date' }],
    ['last_reminder_date wrong format', { ...VALID_HELD, last_reminder_date: '2026-11-8' }],
    ['last_reminder_date impossible', { ...VALID_HELD, last_reminder_date: '2026-13-45' }],
    ['last_reminder_date rolls over', { ...VALID_HELD, last_reminder_date: '2026-02-30' }],
    ['zero_seen_at undefined', { alert_since: VALID_HELD.alert_since, last_reminder_date: '2026-11-08' }],
    ['zero_seen_at a number', { ...VALID_HELD, zero_seen_at: 3 }],
    ['zero_seen_at unparseable', { ...VALID_HELD, zero_seen_at: 'not-a-date' }],
  ];

  it.each(malformed)('drops a malformed held (%s) with a warning and still loads', (_label, held) => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const state = parseState(stateJson({ held }));
    expect(state).not.toBeNull();
    expect(state).not.toHaveProperty('held');
    expect(state?.last_summary_date).toBe('2026-11-08');
    expect(Object.keys(state?.targets ?? {})).toEqual(['delivery-api']);
    expect(warn).toHaveBeenCalledWith(HELD_WARNING);
  });
});

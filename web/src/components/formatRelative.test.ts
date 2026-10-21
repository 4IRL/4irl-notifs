import { describe, expect, it } from 'vitest';

import { formatDuration, formatRelativePast } from './formatRelative';

describe('formatRelativePast', () => {
  const now = Date.parse('2026-10-18T12:00:00Z');
  const SECOND = 1000;
  const MINUTE = 60 * SECOND;
  const HOUR = 60 * MINUTE;
  const DAY = 24 * HOUR;

  function ago(offsetMs: number): string {
    return formatRelativePast({ at: new Date(now - offsetMs).toISOString(), now });
  }

  it.each([
    ['at now', 0, 'just now'],
    ['in the future', -5 * SECOND, 'just now'],
    ['under a second', 400, 'just now'],
    ['12 s', 12 * SECOND, '12 s ago'],
    ['59 s', 59 * SECOND, '59 s ago'],
    ['exactly 1 min', MINUTE, '1 min ago'],
    ['119 s (rounds down)', 119 * SECOND, '1 min ago'],
    ['59 min', 59 * MINUTE, '59 min ago'],
    ['exactly 1 h', HOUR, '1 h ago'],
    ['20 h 30 min', 20 * HOUR + 30 * MINUTE, '20 h ago'],
    ['23 h 59 min', 23 * HOUR + 59 * MINUTE, '23 h ago'],
    ['exactly 24 h', DAY, '1 day ago'],
    ['47 h', 47 * HOUR, '1 day ago'],
    ['3 days', 3 * DAY, '3 days ago'],
  ])('%s', (_name, offset, expected) => {
    expect(ago(offset)).toBe(expected);
  });

  it('reads an unparseable timestamp as just now', () => {
    expect(formatRelativePast({ at: 'not-a-date', now })).toBe('just now');
  });
});

describe('formatDuration', () => {
  it.each([
    [60, '1 min'],
    [300, '5 min'],
    [3600, '1 h'],
    [7200, '2 h'],
    [86400, '24 h'],
    [90000, '25 h'],
    [172800, '2 d'],
    [2_678_400, '31 d'],
    [5400, '90 min'],
    [90, '90 s'],
    [61, '61 s'],
  ])('%i seconds reads as %s', (seconds, expected) => {
    expect(formatDuration({ seconds })).toBe(expected);
  });
});

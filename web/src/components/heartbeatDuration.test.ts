import { describe, expect, it } from 'vitest';

import { DURATION_UNITS, splitDuration, toSeconds } from './heartbeatDuration';

describe('splitDuration', () => {
  it.each([
    [60, '1', 'minutes'],
    [5400, '90', 'minutes'],
    [3600, '1', 'hours'],
    [7200, '2', 'hours'],
    // One day stays in hours, like the list's "24 h"; days start at two.
    [86_400, '24', 'hours'],
    [172_800, '2', 'days'],
    [259_200, '3', 'days'],
    [90_000, '25', 'hours'],
    [2_678_400, '31', 'days'],
  ] as const)('shows %i s as %s %s (largest unit that divides evenly)', (seconds, value, unit) => {
    expect(splitDuration(seconds)).toEqual({ value, unit });
  });

  it.each([
    [90, '90'],
    [61, '61'],
    [150, '150'],
  ] as const)('shows %i s, which is not a whole minute, in seconds', (seconds, value) => {
    expect(splitDuration(seconds)).toEqual({ value, unit: 'seconds' });
  });

  it.each([60, 61, 90, 150, 3600, 5400, 7200, 86_400, 90_000, 604_800, 2_678_400])(
    'round-trips %i s back to the same seconds',
    (seconds) => {
      expect(toSeconds(splitDuration(seconds))).toBe(seconds);
    },
  );
});

describe('toSeconds', () => {
  it.each([
    ['24', 'hours', 86_400],
    ['2', 'days', 172_800],
    ['1', 'minutes', 60],
    ['90', 'seconds', 90],
    ['60', 'seconds', 60],
    [' 3 ', 'hours', 10_800],
    ['0.5', 'hours', 1800],
    ['0.1', 'hours', 360],
  ] as const)('converts %s %s to %i s', (value, unit, seconds) => {
    expect(toSeconds({ value, unit })).toBe(seconds);
  });

  it.each([
    ['', 'hours'],
    ['   ', 'hours'],
    ['abc', 'hours'],
    ['Infinity', 'hours'],
    ['0.0001', 'minutes'],
    ['0.5', 'seconds'],
  ] as const)('rejects %j %s', (value, unit) => {
    expect(toSeconds({ value, unit })).toBeNull();
  });

  it('offers seconds first, then minutes, hours and days', () => {
    expect(DURATION_UNITS.map((entry) => entry.unit)).toEqual([
      'seconds',
      'minutes',
      'hours',
      'days',
    ]);
  });
});

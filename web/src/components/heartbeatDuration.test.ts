import { describe, expect, it } from 'vitest';

import { splitDuration, toSeconds } from './heartbeatDuration';

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

  it('falls back to a fractional minutes value for a duration that is not a whole minute', () => {
    expect(splitDuration(90)).toEqual({ value: '1.5', unit: 'minutes' });
  });

  it.each([60, 90, 3600, 5400, 7200, 86_400, 90_000, 604_800, 2_678_400])(
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
  ] as const)('rejects %j %s', (value, unit) => {
    expect(toSeconds({ value, unit })).toBeNull();
  });
});

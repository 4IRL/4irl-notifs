import { describe, expect, it } from 'vitest';

import { heldCountFor, heldTotal } from './heldCounts';
import type { HeldCounts } from './heldCounts';

const ready: HeldCounts = {
  status: 'ready',
  byApp: new Map([
    ['a', 2],
    ['b', 3],
  ]),
  unassignedTotal: 1,
};

describe('heldCountFor', () => {
  it('returns the app count when ready', () => {
    expect(heldCountFor({ held: ready, appId: 'a' })).toBe(2);
  });

  it('returns 0 for a ready snapshot with no entry for the app', () => {
    expect(heldCountFor({ held: ready, appId: 'missing' })).toBe(0);
  });

  it('returns 0 while loading or unavailable', () => {
    expect(heldCountFor({ held: { status: 'loading' }, appId: 'a' })).toBe(0);
    expect(heldCountFor({ held: { status: 'unavailable' }, appId: 'a' })).toBe(0);
  });
});

describe('heldTotal', () => {
  it('sums every app in a ready snapshot', () => {
    expect(heldTotal(ready)).toBe(5);
  });

  it('is 0 while loading or unavailable', () => {
    expect(heldTotal({ status: 'loading' })).toBe(0);
    expect(heldTotal({ status: 'unavailable' })).toBe(0);
  });
});

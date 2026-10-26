import { describe, expect, it } from 'vitest';

import type { HeartbeatCheck, HeartbeatProbe, HeartbeatProbeResult } from '../api/deliveryClient';
import {
  TICK_MS,
  liveStateOf,
  needsReload,
  nullWindowExpired,
  probePausedWasOf,
  rowStateOf,
  turnedLateLocally,
} from './heartbeatRowState';

const NOW = Date.parse('2026-10-26T12:00:00Z');
const INTERVAL = 300;
const MS = 1000;

function pushCheck(overrides: Partial<HeartbeatCheck> = {}): HeartbeatCheck {
  return {
    id: 1,
    appId: 'app',
    name: 'job',
    periodSeconds: 600,
    graceSeconds: 60,
    paused: false,
    state: 'up',
    lastPingAt: null,
    lastPingKind: null,
    downSince: null,
    deadlineAt: new Date(NOW + 600 * MS).toISOString(),
    destinations: [],
    downReason: null,
    probe: null,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ownerKind: 'app',
    ownerAppId: 'app',
    ...overrides,
  };
}

function probeCheck({
  check = {},
  probe = {},
}: {
  check?: Partial<HeartbeatCheck>;
  probe?: Partial<HeartbeatProbe>;
} = {}): HeartbeatCheck {
  const result: HeartbeatProbeResult = {
    at: new Date(NOW - 10 * MS).toISOString(),
    status: 200,
    reason: null,
  };
  return pushCheck({
    // Derived backstop values: long in the past, as a probe's can be.
    deadlineAt: new Date(NOW - 3600 * MS).toISOString(),
    graceSeconds: 30,
    probe: {
      url: 'https://example.com/',
      expectedStatuses: [200],
      bodyMatch: null,
      timeoutSeconds: 5,
      intervalSeconds: INTERVAL,
      consecutiveFailures: 0,
      failureThreshold: 3,
      hasAccessToken: false,
      lastResult: result,
      ...probe,
    },
    ...check,
  });
}

describe('liveStateOf for a probe', () => {
  it.each([
    ['new', 'waiting'],
    ['up', 'up'],
    ['down', 'down'],
    ['late', 'down'],
    ['weird', 'down'],
  ] as const)('maps state %s to %s', (state, expected) => {
    const check = probeCheck({ check: { state: state as HeartbeatCheck['state'] } });
    expect(liveStateOf({ check, now: NOW })).toBe(expected);
  });

  it('reads up for an up probe whose deadline and late boundary are long past', () => {
    expect(liveStateOf({ check: probeCheck(), now: NOW })).toBe('up');
  });

  it('is paused when paused regardless of state', () => {
    const check = probeCheck({ check: { paused: true, state: 'down' } });
    expect(rowStateOf({ check, now: NOW })).toBe('paused');
  });
});

describe('liveStateOf for a push check (unchanged)', () => {
  it('reads late once past the grace boundary', () => {
    const check = pushCheck({ deadlineAt: new Date(NOW - 1 * MS).toISOString() });
    expect(liveStateOf({ check, now: NOW })).toBe('late');
  });

  it('reads up before the boundary', () => {
    expect(liveStateOf({ check: pushCheck(), now: NOW })).toBe('up');
  });

  it('fails closed on an unknown state', () => {
    const check = pushCheck({ state: 'weird' as HeartbeatCheck['state'] });
    expect(liveStateOf({ check, now: NOW })).toBe('down');
  });
});

describe('turnedLateLocally', () => {
  it.each(['new', 'up', 'down', 'late', 'weird'])('is false for a probe in state %s', (state) => {
    const check = probeCheck({ check: { state: state as HeartbeatCheck['state'] } });
    expect(turnedLateLocally({ check, now: NOW })).toBe(false);
  });

  it('is true for a push check that crossed its late boundary', () => {
    const check = pushCheck({ deadlineAt: new Date(NOW - 1 * MS).toISOString() });
    expect(turnedLateLocally({ check, now: NOW })).toBe(true);
  });
});

describe('nullWindowExpired', () => {
  const window = INTERVAL * MS + TICK_MS;

  it.each([
    ['inside the window', NOW + window - 1, false],
    ['exactly at the boundary', NOW + window, false],
    ['one ms past the boundary', NOW + window + 1, true],
  ] as const)('%s', (_label, now, expected) => {
    expect(nullWindowExpired({ nullSeenAt: NOW, intervalSeconds: INTERVAL, now })).toBe(expected);
  });
});

describe('needsReload for a probe', () => {
  const window = INTERVAL * MS + TICK_MS;

  it('never reloads a paused probe', () => {
    const check = probeCheck({ check: { paused: true, state: 'down' } });
    expect(needsReload({ check, now: NOW })).toBe(false);
  });

  it('reloads a down probe', () => {
    expect(needsReload({ check: probeCheck({ check: { state: 'down' } }), now: NOW })).toBe(true);
  });

  it('does not reload a fresh up probe even with a passed deadlineAt', () => {
    expect(needsReload({ check: probeCheck(), now: NOW })).toBe(false);
  });

  it('reloads a stale up probe', () => {
    const at = new Date(NOW - (INTERVAL * MS + 1)).toISOString();
    const check = probeCheck({ probe: { lastResult: { at, status: 200, reason: null } } });
    expect(needsReload({ check, now: NOW })).toBe(true);
  });

  it('reloads on an unparsable result time', () => {
    const check = probeCheck({
      probe: { lastResult: { at: 'garbage', status: 200, reason: null } },
    });
    expect(needsReload({ check, now: NOW })).toBe(true);
  });

  it('does not reload a null last result when no window was recorded', () => {
    const check = probeCheck({ probe: { lastResult: null } });
    expect(needsReload({ check, now: NOW })).toBe(false);
  });

  it.each([
    ['inside the window', window - 1, false],
    ['exactly at the boundary', window, false],
    ['past the window', window + 1, true],
  ] as const)('null last result %s', (_label, elapsed, expected) => {
    const check = probeCheck({ probe: { lastResult: null } });
    expect(needsReload({ check, now: NOW + elapsed, nullSeenAt: NOW })).toBe(expected);
  });
});

describe('needsReload for a push check (unchanged)', () => {
  it('reloads once past the deadline', () => {
    const check = pushCheck({ deadlineAt: new Date(NOW - 1 * MS).toISOString() });
    expect(needsReload({ check, now: NOW })).toBe(true);
  });

  it('does not reload a healthy check', () => {
    expect(needsReload({ check: pushCheck(), now: NOW })).toBe(false);
  });

  it('ignores nullSeenAt', () => {
    expect(needsReload({ check: pushCheck(), now: NOW, nullSeenAt: 0 })).toBe(false);
  });
});

describe('probePausedWasOf', () => {
  it.each([
    ['up', 'up'],
    ['down', 'down'],
    ['new', null],
    ['weird', null],
  ] as const)('state %s gives %s', (state, expected) => {
    const check = probeCheck({ check: { state: state as HeartbeatCheck['state'] } });
    expect(probePausedWasOf(check)).toBe(expected);
  });
});

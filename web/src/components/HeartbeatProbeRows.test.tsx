import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type {
  DeliveryApiClient,
  HeartbeatCheck,
  HeartbeatProbe,
  HeartbeatProbeResult,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { setCompactViewport } from '../test-viewport';
import { Heartbeats } from './Heartbeats';
import type { DestinationsLoadState } from './RoutesSection';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const READY_DESTINATIONS: DestinationsLoadState = { status: 'ready', destinations: [] };
const NOW_ISO = '2026-10-26T12:00:00Z';
const NOW_MS = Date.parse(NOW_ISO);
const TICK = 30_000;
const WIDE_COLUMNS = 7;
const COMPACT_COLUMNS = 6;

// App id and name are chosen so they can never contain "ping" or "late".
const APP_ID = 'zqapp';

function isoAgo({ seconds, from = NOW_MS }: { seconds: number; from?: number }): string {
  return new Date(from - seconds * 1000).toISOString();
}

function result(overrides: Partial<HeartbeatProbeResult> = {}): HeartbeatProbeResult {
  return { at: isoAgo({ seconds: 60 }), status: 200, reason: null, ...overrides };
}

function buildProbe(overrides: Partial<HeartbeatProbe> = {}): HeartbeatProbe {
  return {
    url: 'https://host.example/health',
    expectedStatuses: [200],
    bodyMatch: null,
    timeoutSeconds: 5,
    intervalSeconds: 60,
    consecutiveFailures: 0,
    failureThreshold: 3,
    hasAccessToken: false,
    lastResult: result(),
    ...overrides,
  };
}

function buildCheck(overrides: Partial<HeartbeatCheck> & { id: number }): HeartbeatCheck {
  return {
    appId: APP_ID,
    name: `zq${overrides.id}`,
    periodSeconds: 86400,
    graceSeconds: 7200,
    paused: false,
    state: 'up',
    lastPingAt: '2026-10-18T08:01:00Z',
    lastPingKind: 'success',
    downSince: null,
    deadlineAt: '2099-01-01T10:00:00Z',
    destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
    ownerKind: 'operator',
    ownerAppId: null,
    downReason: null,
    probe: null,
    createdAt: '2026-10-17T10:00:00Z',
    updatedAt: '2026-10-18T08:01:00Z',
    ...overrides,
  };
}

/** A probe row; its derived backstop fields are long past, as the server sends them. */
function buildProbeCheck({
  id = 1,
  check = {},
  probe = {},
}: {
  id?: number;
  check?: Partial<HeartbeatCheck>;
  probe?: Partial<HeartbeatProbe>;
} = {}): HeartbeatCheck {
  return buildCheck({
    id,
    periodSeconds: 60,
    graceSeconds: 30,
    lastPingAt: null,
    lastPingKind: null,
    deadlineAt: isoAgo({ seconds: 3600 }),
    probe: buildProbe(probe),
    ...check,
  });
}

function buildClient(overrides: Partial<DeliveryApiClient> = {}): DeliveryApiClient {
  return {
    getReadiness: vi.fn(),
    listDestinations: vi.fn().mockResolvedValue([]),
    createDestination: vi.fn(),
    updateDestination: vi.fn(),
    deleteDestination: vi.fn(),
    rotateSecret: vi.fn(),
    sendTest: vi.fn(),
    listRoutes: vi.fn().mockResolvedValue([]),
    createRoute: vi.fn(),
    updateRoute: vi.fn(),
    deleteRoute: vi.fn(),
    listDeliveries: vi.fn().mockResolvedValue([]),
    listAttempts: vi.fn().mockResolvedValue([]),
    replayDelivery: vi.fn(),
    listScheduledMessages: vi.fn().mockResolvedValue({ items: [], total: 0 }),
    cancelScheduledMessage: vi.fn(),
    listHeartbeats: vi.fn().mockResolvedValue([]),
    createHeartbeat: vi.fn(),
    updateHeartbeat: vi.fn(),
    deleteHeartbeat: vi.fn().mockResolvedValue(undefined),
    pauseHeartbeat: vi.fn(),
    resumeHeartbeat: vi.fn(),
    listIdentityRequests: vi.fn().mockResolvedValue([]),
    approveIdentityRequest: vi.fn(),
    rejectIdentityRequest: vi.fn(),
    listIdentityBindings: vi.fn().mockResolvedValue([]),
    revokeIdentityKey: vi.fn(),
    unbindApp: vi.fn(),
    ...overrides,
  };
}

function renderSection(client: DeliveryApiClient) {
  return render(<Heartbeats client={client} destinationsState={READY_DESTINATIONS} />);
}

function rowOf(id: number): HTMLElement {
  const row = document.querySelector<HTMLElement>(`tr[data-check-id="${id}"]`);
  if (row === null) {
    throw new Error(`row ${id} not found`);
  }
  return row;
}

/** A row's cell by column: wide is Check, State, Owner, Last, Schedule, Alerts, Actions. */
function cellOf({ row, index }: { row: HTMLElement; index: number }): HTMLElement {
  return row.querySelectorAll('td')[index] as HTMLElement;
}

const STATE = 1;
const LAST = 3;
const SCHEDULE = 4;

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

async function renderAt(checks: HeartbeatCheck[]) {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(NOW_ISO));
  const view = renderSection(buildClient({ listHeartbeats: vi.fn().mockResolvedValue(checks) }));
  await advance(0);
  return view;
}

interface StateCase {
  name: string;
  check?: Partial<HeartbeatCheck>;
  probe?: Partial<HeartbeatProbe>;
  state: string[];
  notState?: Array<string | RegExp>;
  last?: string[];
}

const SINCE = '2026-10-26T11:30:00Z';

const STATE_CASES: StateCase[] = [
  {
    name: 'never-probed new row',
    check: { state: 'new' },
    probe: { lastResult: null },
    state: ['New', 'first probe'],
  },
  {
    name: 'up with a successful result',
    probe: { lastResult: result({ at: isoAgo({ seconds: 60 }) }) },
    state: ['Up'],
    notState: [/failures/, 'Awaiting next probe'],
    last: ['OK · HTTP 200 · probed 1 min ago'],
  },
  {
    name: 'up with 2 of 3 failures',
    probe: {
      consecutiveFailures: 2,
      lastResult: result({ status: 503, reason: 'http_status' }),
    },
    state: ['Up', '2 of 3 failures'],
    last: ['HTTP 503'],
  },
  {
    name: 'new with a failed result',
    check: { state: 'new' },
    probe: {
      consecutiveFailures: 1,
      lastResult: result({ status: null, reason: 'timeout' }),
    },
    state: ['New', '1 of 3 failures'],
    last: ['Timed out'],
  },
  {
    name: 'up with no result yet',
    probe: { lastResult: null },
    state: ['Up', 'Awaiting next probe'],
  },
  {
    name: 'down with a matching result',
    check: { state: 'down', downReason: 'timeout', downSince: SINCE },
    probe: { lastResult: result({ status: null, reason: 'timeout' }) },
    state: ['Down', 'Timed out · since 11:30'],
  },
  {
    name: 'down with no result (reason still shown)',
    check: { state: 'down', downReason: 'tls', downSince: SINCE },
    probe: { lastResult: null },
    state: ['Down', 'TLS error · since 11:30'],
  },
  {
    name: 'down with a null downReason uses the generic line',
    check: { state: 'down', downReason: null, downSince: SINCE },
    probe: { lastResult: null },
    state: ['Down', `${strings.probeReasonUnknown} · since 11:30`],
  },
  {
    name: 'down with downSince null shows the reason alone',
    check: { state: 'down', downReason: 'timeout', downSince: null },
    probe: { lastResult: result({ status: null, reason: 'timeout' }) },
    state: ['Down', 'Timed out'],
    notState: [/since/, /undefined/],
  },
  {
    name: 'down with downSince null and no result',
    check: { state: 'down', downReason: 'timeout', downSince: null },
    probe: { lastResult: null },
    state: ['Down', 'Timed out'],
    notState: [/since/, /undefined/],
  },
  {
    name: 'down http_status takes the code from a matching result',
    check: { state: 'down', downReason: 'http_status', downSince: SINCE },
    probe: { lastResult: result({ status: 503, reason: 'http_status' }) },
    state: ['HTTP 503 · since 11:30'],
  },
  {
    name: 'down http_status with a timeout result has no code',
    check: { state: 'down', downReason: 'http_status', downSince: SINCE },
    probe: { lastResult: result({ status: null, reason: 'timeout' }) },
    state: ['Unexpected HTTP status · since 11:30'],
    notState: [/\d{3}/],
    last: ['Timed out'],
  },
  {
    name: 'down http_status with a successful 200 result shows no 200',
    check: { state: 'down', downReason: 'http_status', downSince: SINCE },
    probe: { lastResult: result({ status: 200, reason: null }) },
    state: ['Unexpected HTTP status · since 11:30'],
    notState: [/200/],
  },
  {
    name: 'down http_status with no result',
    check: { state: 'down', downReason: 'http_status', downSince: SINCE },
    probe: { lastResult: null },
    state: ['Unexpected HTTP status · since 11:30'],
  },
  {
    name: 'down http_status and downSince null',
    check: { state: 'down', downReason: 'http_status', downSince: null },
    probe: { lastResult: result({ status: null, reason: 'timeout' }) },
    state: ['Unexpected HTTP status'],
    notState: [/since/, /\d{3}/],
  },
  {
    name: 'paused up',
    check: { paused: true, state: 'up' },
    probe: { consecutiveFailures: 2, lastResult: result({ status: 503, reason: 'http_status' }) },
    state: ['Paused', 'was up'],
    notState: [/failures/, /HTTP/],
  },
  {
    name: 'paused down',
    check: { paused: true, state: 'down', downReason: 'timeout', downSince: SINCE },
    state: ['Paused', 'was down'],
    notState: [/Timed out/, /since/],
  },
  {
    name: 'paused new',
    check: { paused: true, state: 'new' },
    probe: { lastResult: null },
    state: ['Paused'],
    notState: [/was/, /first probe/],
  },
  {
    name: 'paused unknown state',
    check: { paused: true, state: 'weird' as HeartbeatCheck['state'] },
    state: ['Paused'],
    notState: [/was/],
  },
];

describe('Heartbeats probe rows: State and Last result cells', () => {
  it.each(STATE_CASES)('$name', async ({ check, probe, state, notState = [], last = [] }) => {
    await renderAt([buildProbeCheck({ check, probe })]);
    const stateText = cellOf({ row: rowOf(1), index: STATE }).textContent ?? '';
    for (const expected of state) {
      expect(stateText).toContain(expected);
    }
    for (const forbidden of notState) {
      if (typeof forbidden === 'string') {
        expect(stateText).not.toContain(forbidden);
      } else {
        expect(stateText).not.toMatch(forbidden);
      }
    }
    const lastText = cellOf({ row: rowOf(1), index: LAST }).textContent ?? '';
    for (const expected of last) {
      expect(lastText).toContain(expected);
    }
  });

  it('shows the empty marker in the Last cell without a result', async () => {
    await renderAt([buildProbeCheck({ probe: { lastResult: null } })]);
    expect(cellOf({ row: rowOf(1), index: LAST })).toHaveTextContent(strings.noValue);
  });

  it('shows no progress text when the failure threshold is unknown', async () => {
    await renderAt([
      buildProbeCheck({
        probe: {
          consecutiveFailures: 2,
          failureThreshold: null,
          lastResult: result({ status: 503, reason: 'http_status' }),
        },
      }),
    ]);
    expect(rowOf(1).textContent).not.toMatch(/\d+ of \d+/);
    expect(cellOf({ row: rowOf(1), index: STATE })).toHaveTextContent(/^Up$/);
  });

  it('never reads Late or shows a ping or waiting-deadline line, even past the deadline', async () => {
    await renderAt([
      buildProbeCheck({ id: 1, probe: {} }),
      buildProbeCheck({ id: 2, check: { state: 'new' }, probe: { lastResult: null } }),
      buildProbeCheck({ id: 3, check: { paused: true } }),
    ]);
    for (const id of [1, 2, 3]) {
      expect(rowOf(id).textContent).not.toMatch(/late/i);
      const cells = [STATE, LAST].map((index) => cellOf({ row: rowOf(id), index }).textContent);
      expect(cells.join(' ')).not.toMatch(/ping/i);
      expect(rowOf(id).textContent).not.toContain('down if none by');
    }
  });

  it('shows the Schedule as the interval and timeout, never grace', async () => {
    await renderAt([
      buildProbeCheck({ id: 1, probe: { intervalSeconds: 300, timeoutSeconds: 8 } }),
      buildCheck({ id: 2, periodSeconds: 86400, graceSeconds: 7200 }),
    ]);
    const probeSchedule = cellOf({ row: rowOf(1), index: SCHEDULE });
    expect(probeSchedule).toHaveTextContent('every 5 min');
    expect(probeSchedule).toHaveTextContent('timeout 8 s');
    expect(probeSchedule.textContent).not.toMatch(/grace/);
    const pushSchedule = cellOf({ row: rowOf(2), index: SCHEDULE });
    expect(pushSchedule).toHaveTextContent('every 24 h');
    expect(pushSchedule).toHaveTextContent('grace 2 h');
  });

  it('renames the column to Last ping / probe', async () => {
    await renderAt([buildProbeCheck()]);
    expect(screen.getByRole('columnheader', { name: 'Last ping / probe' })).toBeInTheDocument();
  });

  it('counts down probe rows in the Down summary', async () => {
    await renderAt([
      buildProbeCheck({ id: 1, check: { state: 'down', downReason: 'timeout', downSince: SINCE } }),
      buildProbeCheck({ id: 2 }),
    ]);
    expect(screen.getAllByRole('status')[0]).toHaveTextContent('1 of 2 checks down');
  });
});

describe('Heartbeats probe rows: URL line', () => {
  it('shows host and path only and never leaks userinfo, query or fragment', async () => {
    await renderAt([
      buildProbeCheck({
        probe: { url: 'https://zq-user:zq-pw@host.example/p?token=SECRET#frag' },
      }),
    ]);
    const line = rowOf(1).querySelector('.heartbeats__url');
    expect(line).not.toBeNull();
    expect(line?.textContent).toBe('host.example/p');
    expect(line).not.toHaveAttribute('title');
    for (const secret of ['zq-user', 'zq-pw', 'token=SECRET', '#frag']) {
      expect(rowOf(1).innerHTML).not.toContain(secret);
    }
    // The lead cell title stays "appId / name".
    expect(cellOf({ row: rowOf(1), index: 0 })).toHaveAttribute('title', `${APP_ID} / zq1`);
  });

  it('has no URL line on a push row', async () => {
    await renderAt([buildCheck({ id: 1 })]);
    expect(rowOf(1).querySelector('.heartbeats__url')).toBeNull();
  });
});

describe('Heartbeats probe rows: Cloudflare Access hint', () => {
  const HINT_REJECTED = strings.probeAccessHintTokenRejected;
  const HINT_MISSING = strings.probeAccessHintTokenMissing;

  function row403({
    hasAccessToken,
    status = 403,
    reason = 'http_status',
    paused = false,
  }: {
    hasAccessToken: boolean;
    status?: number | null;
    reason?: string | null;
    paused?: boolean;
  }): HeartbeatCheck {
    return buildProbeCheck({
      check: { paused },
      probe: { hasAccessToken, lastResult: result({ status, reason }) },
    });
  }

  it('shows the expired-or-revoked line for a 403 with credentials set, wrapped', async () => {
    await renderAt([row403({ hasAccessToken: true })]);
    const hint = within(cellOf({ row: rowOf(1), index: LAST })).getByText(HINT_REJECTED);
    expect(hint).toHaveClass('heartbeats__sub', 'heartbeats__sub--wrap');
    expect(hint).not.toHaveAttribute('title');
    expect(hint.textContent).toContain('may have expired or been revoked');
    expect(hint.textContent).toContain('Edit the check and use Replace.');
  });

  it('shows the add-credentials line for a 403 without credentials', async () => {
    await renderAt([row403({ hasAccessToken: false })]);
    const hint = within(cellOf({ row: rowOf(1), index: LAST })).getByText(HINT_MISSING);
    expect(hint).toHaveClass('heartbeats__sub--wrap');
    expect(hint.textContent).toContain('Edit the check and add credentials.');
  });

  it.each([
    ['401', { status: 401 }],
    ['500', { status: 500 }],
    ['503', { status: 503 }],
    ['a config reason', { status: 403, reason: 'config' }],
    ['a successful result', { status: 200, reason: null }],
    ['a null status', { status: null }],
    ['a paused row', { paused: true }],
  ])('shows no hint for %s', async (_name, overrides) => {
    await renderAt([
      row403({ hasAccessToken: true, ...overrides }),
      row403({ hasAccessToken: false, ...overrides }),
    ]);
    expect(screen.queryByText(HINT_REJECTED)).toBeNull();
    expect(screen.queryByText(HINT_MISSING)).toBeNull();
  });

  it('shows no hint without a result', async () => {
    await renderAt([buildProbeCheck({ probe: { hasAccessToken: true, lastResult: null } })]);
    expect(screen.queryByText(HINT_REJECTED)).toBeNull();
  });

  it('shows the hint in the compact layout too', async () => {
    setCompactViewport(true);
    await renderAt([row403({ hasAccessToken: true })]);
    expect(within(rowOf(1)).getByText(HINT_REJECTED)).toBeInTheDocument();
  });

  it('never contains a credential or the URL query', () => {
    for (const text of [HINT_REJECTED, HINT_MISSING]) {
      expect(text).not.toMatch(/token=|secret|\?/i);
    }
  });
});

describe('Heartbeats probe rows: compact layout', () => {
  it('renders 6 columns with the URL line inside the Check cell', async () => {
    setCompactViewport(true);
    await renderAt([buildProbeCheck()]);
    expect(rowOf(1).querySelectorAll('td')).toHaveLength(COMPACT_COLUMNS);
    expect(cellOf({ row: rowOf(1), index: 0 }).querySelector('.heartbeats__url')).toHaveTextContent(
      'host.example/health',
    );
  });

  it('renders 7 columns in the wide layout', async () => {
    await renderAt([buildProbeCheck()]);
    expect(rowOf(1).querySelectorAll('td')).toHaveLength(WIDE_COLUMNS);
  });
});

describe('Heartbeats probe rows: pause notice', () => {
  it('uses the probe notice in the wide confirm dialog and keeps the push notice for push rows', async () => {
    const user = userEvent.setup();
    const probeRow = buildProbeCheck({ id: 1 });
    const pushRow = buildCheck({ id: 2 });
    renderSection(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([probeRow, pushRow]) }));

    await user.click(await screen.findByRole('button', { name: `Pause ${APP_ID}/zq1` }));
    const probeDialog = screen.getByRole('dialog', { name: strings.pauseHeartbeatDialogTitle });
    expect(within(probeDialog).getByText(strings.pauseProbeNotice)).toBeInTheDocument();
    expect(probeDialog.textContent).not.toContain('Pings');
    await user.click(screen.getByRole('button', { name: `Keep running ${APP_ID}/zq1` }));

    await user.click(screen.getByRole('button', { name: `Pause ${APP_ID}/zq2` }));
    const pushDialog = screen.getByRole('dialog', { name: strings.pauseHeartbeatDialogTitle });
    expect(within(pushDialog).getByText(strings.pauseHeartbeatNotice)).toBeInTheDocument();
  });

  it('uses the probe notice in the compact menu confirm', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    renderSection(
      buildClient({ listHeartbeats: vi.fn().mockResolvedValue([buildProbeCheck({ id: 1 })]) }),
    );
    const row = `${APP_ID}/zq1`;
    const kebab = await screen.findByRole('button', { name: `Actions for ${row}` });
    kebab.focus();
    await user.keyboard('{Enter}');
    await user.click(screen.getByRole('menuitem', { name: `Pause ${row}` }));
    const confirm = screen.getByRole('dialog', { name: strings.pauseHeartbeatDialogTitle });
    expect(confirm).toHaveTextContent(strings.pauseProbeNotice);
    expect(confirm.textContent).not.toContain('Pings');
  });
});

describe('Heartbeats probe rows: tick', () => {
  const nullProbeRow = (): HeartbeatCheck =>
    buildProbeCheck({ check: { state: 'new' }, probe: { lastResult: null } });

  async function setup(client: DeliveryApiClient) {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(NOW_ISO));
    const view = renderSection(client);
    await advance(0);
    return view;
  }

  async function ticks(count: number) {
    for (let index = 0; index < count; index += 1) {
      await advance(TICK);
    }
  }

  it('starts no second load while a reload is in flight for a down probe', async () => {
    const down = buildProbeCheck({ check: { state: 'down', downReason: 'timeout' } });
    let release!: (value: HeartbeatCheck[]) => void;
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([down])
      .mockImplementationOnce(
        () =>
          new Promise<HeartbeatCheck[]>((resolve) => {
            release = resolve;
          }),
      );
    await setup(buildClient({ listHeartbeats }));
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    await ticks(3);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    release([down]);
    await advance(0);
  });

  it('does not reload a fresh up probe', async () => {
    const fresh = buildProbeCheck({
      probe: { intervalSeconds: 300, lastResult: result({ at: isoAgo({ seconds: 5 }) }) },
    });
    const listHeartbeats = vi.fn().mockResolvedValue([fresh]);
    await setup(buildClient({ listHeartbeats }));
    await ticks(2);
    expect(listHeartbeats).toHaveBeenCalledTimes(1);
  });

  it('reloads a probe past lastResult.at + interval once per tick at most', async () => {
    const stale = buildProbeCheck({
      probe: { intervalSeconds: 60, lastResult: result({ at: isoAgo({ seconds: 61 }) }) },
    });
    const listHeartbeats = vi.fn().mockResolvedValue([stale]);
    await setup(buildClient({ listHeartbeats }));
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(3);
  });

  it('reloads a null-result probe on the fourth tick after arming, then a full window later', async () => {
    const listHeartbeats = vi.fn().mockResolvedValue([nullProbeRow()]);
    await setup(buildClient({ listHeartbeats }));

    // Tick 1 arms the window; ticks 2-4 are inside it (the comparison is strict).
    for (let tick = 1; tick <= 4; tick += 1) {
      await ticks(1);
      expect(listHeartbeats, `tick ${tick}`).toHaveBeenCalledTimes(1);
    }
    // The fourth tick after the arming tick reloads.
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    // List still null: not again until the fourth tick after that reload.
    for (let tick = 1; tick <= 3; tick += 1) {
      await ticks(1);
      expect(listHeartbeats, `after reload, tick ${tick}`).toHaveBeenCalledTimes(2);
    }
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(3);
  });

  it('picks up a result that lands after the arming tick on the window reload', async () => {
    const landedAt = NOW_MS + TICK + 20_000;
    const listHeartbeats = vi.fn().mockImplementation(async () => {
      if (Date.now() < landedAt) {
        return [nullProbeRow()];
      }
      return [
        buildProbeCheck({
          probe: { lastResult: result({ at: new Date(landedAt).toISOString() }) },
        }),
      ];
    });
    await setup(buildClient({ listHeartbeats }));
    await ticks(4);
    expect(listHeartbeats).toHaveBeenCalledTimes(1);
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    expect(within(rowOf(1)).getByText(/OK · HTTP 200/)).toBeInTheDocument();
    expect(within(rowOf(1)).queryByText(strings.heartbeatFirstProbe)).toBeNull();
  });

  const FAR_RESULT = buildProbeCheck({
    probe: { lastResult: result({ at: '2099-01-01T00:00:00Z' }) },
  });

  it.each([
    ['gains a result', () => [FAR_RESULT]],
    ['is deleted', () => []],
    [
      'is paused',
      () => [
        buildProbeCheck({ check: { state: 'new', paused: true }, probe: { lastResult: null } }),
      ],
    ],
  ])(
    'forgets the window of a row that %s, so a later null starts a fresh one',
    async (_n, away) => {
      let current: HeartbeatCheck[] = [nullProbeRow()];
      const listHeartbeats = vi.fn().mockImplementation(async () => current);
      await setup(buildClient({ listHeartbeats }));
      // Tick 1 arms the entry; two more ticks stay inside the window.
      await ticks(3);
      expect(listHeartbeats).toHaveBeenCalledTimes(1);

      current = away();
      fireEvent.click(screen.getByRole('button', { name: strings.heartbeatsRefresh }));
      await advance(0);
      expect(listHeartbeats).toHaveBeenCalledTimes(2);
      // The tick sees the row gone, resulted or paused and prunes its entry.
      await ticks(1);

      current = [nullProbeRow()];
      fireEvent.click(screen.getByRole('button', { name: strings.heartbeatsRefresh }));
      await advance(0);
      expect(listHeartbeats).toHaveBeenCalledTimes(3);
      // A stale entry (armed 120 s earlier) would reload on the very next tick. A fresh window
      // is armed on this tick and does not fire until the fourth tick after it.
      for (let tick = 1; tick <= 4; tick += 1) {
        await ticks(1);
        expect(listHeartbeats, `tick ${tick}`).toHaveBeenCalledTimes(3);
      }
      await ticks(1);
      expect(listHeartbeats).toHaveBeenCalledTimes(4);
    },
  );

  it('neither reloads nor re-arms while hidden, then reloads once on the first visible tick', async () => {
    const listHeartbeats = vi.fn().mockResolvedValue([nullProbeRow()]);
    const { container } = await setup(buildClient({ listHeartbeats }));

    container.hidden = true;
    await ticks(8);
    expect(listHeartbeats).toHaveBeenCalledTimes(1);

    container.hidden = false;
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    // The reload re-armed the window: nothing more for the next three ticks.
    await ticks(3);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
  });

  it('re-arms an expired window on a tick an in-flight load suppresses', async () => {
    let release!: (value: HeartbeatCheck[]) => void;
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([nullProbeRow()])
      .mockImplementationOnce(
        () =>
          new Promise<HeartbeatCheck[]>((resolve) => {
            release = resolve;
          }),
      )
      .mockResolvedValue([nullProbeRow()]);
    await setup(buildClient({ listHeartbeats }));

    // Ticks 1-4 arm and wait; a Refresh load then stays in flight across tick 5.
    await ticks(4);
    fireEvent.click(screen.getByRole('button', { name: strings.heartbeatsRefresh }));
    await advance(0);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);

    // Tick 5: expired, but the in-flight load suppresses a second call; the entry re-arms.
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    release([nullProbeRow()]);
    await advance(0);

    // The next null reload is the fourth tick after the re-arm.
    for (let tick = 1; tick <= 3; tick += 1) {
      await ticks(1);
      expect(listHeartbeats, `tick ${tick}`).toHaveBeenCalledTimes(2);
    }
    await ticks(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(3);
  });
});

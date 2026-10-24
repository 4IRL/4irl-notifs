import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { DeliveryApiClient, DestinationSummary, HeartbeatCheck } from '../api/deliveryClient';
import { strings } from '../strings';
import { Heartbeats } from './Heartbeats';
import type { DestinationsLoadState } from './RoutesSection';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const READY_DESTINATIONS: DestinationsLoadState = { status: 'ready', destinations: [] };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function buildCheck(overrides: Partial<HeartbeatCheck> & { id: number }): HeartbeatCheck {
  return {
    appId: 'urls4irl',
    name: `check_${overrides.id}`,
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
    createdAt: '2026-10-17T10:00:00Z',
    updatedAt: '2026-10-18T08:01:00Z',
    ...overrides,
  };
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

function renderSection(
  client: DeliveryApiClient,
  destinationsState: DestinationsLoadState = READY_DESTINATIONS,
) {
  return render(<Heartbeats client={client} destinationsState={destinationsState} />);
}

function label(check: HeartbeatCheck): string {
  return `${check.appId}/${check.name}`;
}

function rowOf(id: number): HTMLElement {
  const row = document.querySelector<HTMLElement>(`tr[data-check-id="${id}"]`);
  if (row === null) {
    throw new Error(`row ${id} not found`);
  }
  return row;
}

function button(name: string): HTMLElement {
  return screen.getByRole('button', { name });
}

function heading(): HTMLElement {
  return screen.getByRole('heading', { name: strings.heartbeatsHeading });
}

/** The Down-count live region (the first role=status, above the action-result line). */
function summary(): HTMLElement {
  return screen.getAllByRole('status')[0];
}

function resultLine(): HTMLElement {
  return screen.getAllByRole('status')[1];
}

const CHECK_1 = buildCheck({ id: 1 });
const CHECK_2 = buildCheck({ id: 2 });
const CHECK_3 = buildCheck({ id: 3 });

describe('Heartbeats list states', () => {
  it('loads on mount, shows loading first, then the state, check, last ping, schedule and destination columns', async () => {
    const list = deferred<HeartbeatCheck[]>();
    const client = buildClient({ listHeartbeats: vi.fn().mockReturnValue(list.promise) });
    renderSection(client);

    expect(screen.getByText(strings.heartbeatsLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();

    list.resolve([CHECK_1]);
    await screen.findByRole('table');

    expect(screen.queryByText(strings.heartbeatsLoading)).toBeNull();
    for (const column of [
      strings.columnHeartbeatState,
      strings.columnHeartbeatCheck,
      strings.columnHeartbeatLastPing,
      strings.columnHeartbeatSchedule,
      strings.columnHeartbeatAlertsTo,
    ]) {
      expect(screen.getByRole('columnheader', { name: column })).toBeInTheDocument();
    }
    expect(
      screen.getByRole('columnheader', { name: strings.columnHeartbeatActions }),
    ).toBeInTheDocument();
    expect(client.listHeartbeats).toHaveBeenCalledTimes(1);
  });

  it('renders the empty state', async () => {
    renderSection(buildClient());

    expect(await screen.findByText(strings.heartbeatsEmpty)).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(summary()).toBeEmptyDOMElement();
  });

  it('renders a load failure as an alert, distinct from the empty state', async () => {
    const client = buildClient({
      listHeartbeats: vi.fn().mockRejectedValue(new ApiError({ message: 'boom', status: 502 })),
    });
    renderSection(client);

    expect((await screen.findByRole('alert')).textContent).toBe('boom');
    expect(screen.queryByText(strings.heartbeatsEmpty)).toBeNull();
    expect(screen.queryByText(strings.heartbeatsLoading)).toBeNull();
  });

  it('falls back to the generic load message for a non-API failure', async () => {
    const client = buildClient({ listHeartbeats: vi.fn().mockRejectedValue(new Error('network')) });
    renderSection(client);

    expect((await screen.findByRole('alert')).textContent).toBe(strings.heartbeatsLoadError);
  });

  it('Refresh with rows shows the loading state, hides the table and clears the result line', async () => {
    const user = userEvent.setup();
    const reload = deferred<HeartbeatCheck[]>();
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1])
      .mockResolvedValueOnce([{ ...CHECK_1, paused: true }])
      .mockReturnValueOnce(reload.promise);
    const pauseHeartbeat = vi.fn().mockResolvedValue({ ...CHECK_1, paused: true });
    renderSection(buildClient({ listHeartbeats, pauseHeartbeat }));

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    await user.click(button(`Confirm pause ${label(CHECK_1)}`));
    await waitFor(() => expect(resultLine()).not.toBeEmptyDOMElement());

    await user.click(button(strings.heartbeatsRefresh));

    expect(screen.getByText(strings.heartbeatsLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
    expect(resultLine()).toBeEmptyDOMElement();

    reload.resolve([CHECK_1]);
    await screen.findByRole('table');
    expect(screen.queryByText(strings.heartbeatsLoading)).toBeNull();
  });

  it('Refresh recovers from an error', async () => {
    const user = userEvent.setup();
    const listHeartbeats = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ message: 'boom', status: 502 }))
      .mockResolvedValue([CHECK_1]);
    renderSection(buildClient({ listHeartbeats }));
    await screen.findByRole('alert');

    await user.click(button(strings.heartbeatsRefresh));

    await screen.findByRole('table');
    expect(screen.queryByRole('alert')).toBeNull();
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
  });

  it('a stale overlapping load never overwrites a newer one', async () => {
    const user = userEvent.setup();
    const first = deferred<HeartbeatCheck[]>();
    const second = deferred<HeartbeatCheck[]>();
    const listHeartbeats = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    renderSection(buildClient({ listHeartbeats }));

    await user.click(button(strings.heartbeatsRefresh));
    second.resolve([buildCheck({ id: 2, name: 'newer' })]);
    await screen.findByText('newer');

    first.resolve([buildCheck({ id: 1, name: 'older' })]);
    await act(async () => {
      await first.promise;
    });

    expect(screen.getByText('newer')).toBeInTheDocument();
    expect(screen.queryByText('older')).toBeNull();
  });
});

describe('Heartbeats row rendering', () => {
  const NOW = '2026-10-18T14:00:00Z';

  async function renderAtNow(checks: HeartbeatCheck[]) {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(NOW));
    const client = buildClient({ listHeartbeats: vi.fn().mockResolvedValue(checks) });
    renderSection(client);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    return client;
  }

  const UP = buildCheck({ id: 1, name: 'nightly_backup', deadlineAt: '2026-10-19T10:00:00Z' });
  const LATE = buildCheck({
    id: 2,
    name: 'agent_5m',
    periodSeconds: 3600,
    graceSeconds: 3600,
    lastPingAt: '2026-10-18T13:53:00Z',
    deadlineAt: '2026-10-18T14:53:00Z',
  });
  const DOWN_MISSED = buildCheck({
    id: 3,
    name: 'daily_report',
    state: 'down',
    downSince: '2026-10-18T12:00:00Z',
    lastPingAt: '2026-10-17T18:02:00Z',
    deadlineAt: '2026-10-18T12:00:00Z',
  });
  const DOWN_FAIL = buildCheck({
    id: 4,
    name: 'hourly_gauge',
    state: 'down',
    downSince: '2026-10-18T13:59:00Z',
    lastPingAt: '2026-10-18T13:59:00Z',
    lastPingKind: 'fail',
    periodSeconds: 3600,
    graceSeconds: 900,
    deadlineAt: '2026-10-18T15:15:00Z',
  });
  const PAUSED = buildCheck({
    id: 5,
    name: 'metrics_flush',
    paused: true,
    periodSeconds: 60,
    graceSeconds: 300,
    deadlineAt: '2026-10-19T08:06:00Z',
  });
  const WAITING = buildCheck({
    id: 6,
    appId: 'chores4irl',
    name: 'reaper',
    state: 'new',
    lastPingAt: null,
    lastPingKind: null,
    deadlineAt: '2026-10-19T16:00:00Z',
  });

  it('renders every state as a text pill with its sub-line', async () => {
    await renderAtNow([UP, LATE, DOWN_MISSED, DOWN_FAIL, PAUSED, WAITING]);

    expect(within(rowOf(1)).getByText(strings.heartbeatStateUp)).toBeInTheDocument();

    const late = within(rowOf(2));
    expect(late.getByText(strings.heartbeatStateLate)).toBeInTheDocument();
    expect(late.getByText('down in 53 min')).toBeInTheDocument();

    const missed = within(rowOf(3));
    expect(missed.getByText(strings.heartbeatStateDown)).toBeInTheDocument();
    expect(missed.getByText('since 12:00')).toBeInTheDocument();

    const failed = within(rowOf(4));
    expect(failed.getByText(strings.heartbeatStateDown)).toBeInTheDocument();
    expect(failed.getByText(strings.heartbeatReportedFailure)).toBeInTheDocument();

    const paused = within(rowOf(5));
    expect(paused.getByText(strings.heartbeatStatePaused)).toBeInTheDocument();
    expect(paused.getByText('was up')).toBeInTheDocument();

    const waiting = within(rowOf(6));
    expect(waiting.getByText(strings.heartbeatStateWaiting)).toBeInTheDocument();
    expect(waiting.getByText(strings.heartbeatWaitingSub)).toBeInTheDocument();
  });

  it('shows what a paused check was: down, late or waiting', async () => {
    await renderAtNow([
      buildCheck({ id: 1, paused: true, state: 'down' }),
      { ...LATE, id: 2, paused: true },
      buildCheck({ id: 3, paused: true, state: 'new', lastPingAt: null, lastPingKind: null }),
    ]);

    expect(within(rowOf(1)).getByText('was down')).toBeInTheDocument();
    expect(within(rowOf(2)).getByText('was late')).toBeInTheDocument();
    expect(within(rowOf(3)).getByText('was waiting')).toBeInTheDocument();
  });

  it('renders the last ping in a time element, with relative text and a fail sub-line', async () => {
    await renderAtNow([UP, DOWN_FAIL]);

    const time = rowOf(1).querySelector('time');
    expect(time).toHaveAttribute('datetime', '2026-10-18T08:01:00Z');
    expect(time).toHaveTextContent('2026-10-18 08:01');
    expect(within(rowOf(1)).getByText('5 h ago')).toBeInTheDocument();

    const fail = within(rowOf(4)).getByText('fail ping · 1 min ago');
    expect(fail).toHaveClass('heartbeats__fail');
  });

  it('renders a never-pinged check as a dash with no time element and the down deadline', async () => {
    await renderAtNow([WAITING]);

    const row = rowOf(6);
    expect(row.querySelector('time')).toBeNull();
    expect(within(row).getByText(strings.noValue)).toBeInTheDocument();
    expect(within(row).getByText('down if none by 2026-10-19 16:00')).toBeInTheDocument();
  });

  it('does not promise a down deadline for a paused never-pinged check', async () => {
    await renderAtNow([{ ...WAITING, paused: true }]);

    expect(within(rowOf(6)).queryByText(/down if none by/)).toBeNull();
  });

  it('renders the compact schedule and the alert destinations with their types', async () => {
    await renderAtNow([
      buildCheck({
        id: 1,
        periodSeconds: 86400,
        graceSeconds: 7200,
        destinations: [
          { name: 'ops_discord', type: 'discord', enabled: true },
          { name: 'mc_hook', type: 'webhook', enabled: false },
        ],
      }),
      buildCheck({ id: 2, periodSeconds: 300, graceSeconds: 600 }),
    ]);

    const first = within(rowOf(1));
    expect(first.getByText('every 24 h')).toBeInTheDocument();
    expect(first.getByText('grace 2 h')).toBeInTheDocument();
    expect(first.getByText('ops_discord')).toBeInTheDocument();
    expect(first.getByText('discord')).toBeInTheDocument();
    expect(first.getByText('mc_hook')).toBeInTheDocument();
    expect(first.getByText('webhook (disabled)')).toBeInTheDocument();
    expect(within(rowOf(2)).getByText('every 5 min')).toBeInTheDocument();
    expect(within(rowOf(2)).getByText('grace 10 min')).toBeInTheDocument();
  });

  it('counts only Down rows in the summary (Late excluded)', async () => {
    await renderAtNow([UP, LATE, DOWN_MISSED, DOWN_FAIL, PAUSED, WAITING]);

    expect(summary()).toHaveTextContent('2 of 6 checks down');
  });

  it('excludes a paused check from the Down count even when its stored state is down', async () => {
    await renderAtNow([DOWN_MISSED, { ...DOWN_FAIL, paused: true }]);

    expect(summary()).toHaveTextContent('1 of 2 checks down');
  });

  it('never hides a server-reported late state behind the local clock', async () => {
    await renderAtNow([{ ...UP, state: 'late' }]);

    expect(within(rowOf(1)).getByText(strings.heartbeatStateLate)).toBeInTheDocument();
  });

  it('fails closed to Down, and counts it, for a state this build does not know', async () => {
    await renderAtNow([{ ...UP, state: 'quarantined' as HeartbeatCheck['state'] }]);

    expect(within(rowOf(1)).getByText(strings.heartbeatStateDown)).toBeInTheDocument();
    expect(summary()).toHaveTextContent('1 of 1 check down');
  });

  it('renders no state sub-line for a Down check with neither a failure ping nor a down-since', async () => {
    await renderAtNow([{ ...DOWN_MISSED, downSince: null }]);

    // Check is the pinned first column; State is the second.
    const stateCell = within(rowOf(3)).getAllByRole('cell')[1];
    expect(within(stateCell).getByText(strings.heartbeatStateDown)).toBeInTheDocument();
    expect(stateCell.querySelector('.heartbeats__sub')).toBeNull();
  });

  it('keeps the summary distinct from the action-result line', async () => {
    await renderAtNow([UP]);

    expect(screen.getAllByRole('status')).toHaveLength(2);
    expect(summary()).toHaveTextContent('0 of 1 check down');
    expect(resultLine()).toBeEmptyDOMElement();
  });
});

describe('Heartbeats owner column', () => {
  const APP_OWNED = buildCheck({
    id: 11,
    appId: 'chores4irl',
    name: 'daily',
    ownerKind: 'app',
    ownerAppId: 'chores4irl',
  });
  const OPERATOR_OWNED = buildCheck({ id: 12, appId: 'chores4irl', name: 'manual' });
  const UNKNOWN_OWNED = buildCheck({
    id: 13,
    appId: 'chores4irl',
    name: 'mystery',
    ownerKind: 'unknown',
    ownerAppId: null,
  });
  const managedNote = strings.ownerManagedNote({ appId: 'chores4irl' });

  async function renderOwned() {
    const client = buildClient({
      listHeartbeats: vi.fn().mockResolvedValue([APP_OWNED, OPERATOR_OWNED, UNKNOWN_OWNED]),
    });
    renderSection(client);
    await screen.findByRole('table');
    return client;
  }

  it('has an Owner column header after Check, with Check as the pinned first column', async () => {
    await renderOwned();

    const headers = screen.getAllByRole('columnheader').map((header) => header.textContent);
    expect(headers.slice(0, 3)).toEqual([
      strings.columnHeartbeatCheck,
      strings.columnHeartbeatState,
      strings.columnOwner,
    ]);
    // The first column carries the pin hook, in the header and in every row.
    expect(screen.getAllByRole('columnheader')[0]).toHaveClass('admin-table__lead');
    expect(within(rowOf(11)).getAllByRole('cell')[0]).toHaveClass('admin-table__lead');
  });

  it('shows a text badge with a hidden Owner prefix per row, distinct from the check app id', async () => {
    await renderOwned();

    const appCell = within(rowOf(11)).getByText(strings.ownerApp({ appId: 'chores4irl' }));
    expect(appCell).toHaveTextContent(strings.ownerApp({ appId: 'chores4irl' }));
    expect(appCell.parentElement).toHaveTextContent(
      `${strings.ownerPrefix}${strings.ownerApp({ appId: 'chores4irl' })}`,
    );
    // The Check column still shows the bare app id, outside any Owner badge.
    expect(within(rowOf(11)).getByText('chores4irl')).not.toHaveTextContent(strings.ownerPrefix);

    const operatorBadge = within(rowOf(12)).getByText(strings.ownerOperator);
    expect(operatorBadge.parentElement).toHaveTextContent(
      `${strings.ownerPrefix}${strings.ownerOperator}`,
    );

    const unknownBadge = within(rowOf(13)).getByText(strings.ownerUnknown);
    expect(unknownBadge.parentElement).toHaveTextContent(
      `${strings.ownerPrefix}${strings.ownerUnknown}`,
    );
    expect(within(rowOf(13)).queryByText(strings.ownerOperator)).toBeNull();
  });

  it('notes app-owned and unknown rows, not operator rows', async () => {
    await renderOwned();

    // The line under the buttons is the short form, never the long text, and has no `title`.
    expect(within(rowOf(11)).getByText(strings.ownerManagedInlineNote)).not.toHaveAttribute(
      'title',
    );
    expect(within(rowOf(11)).queryByText(managedNote)).toBeNull();
    expect(within(rowOf(13)).getByText(strings.ownerUnknownInlineNote)).toBeInTheDocument();
    expect(within(rowOf(13)).queryByText(strings.ownerUnknownNote)).toBeNull();
    expect(within(rowOf(12)).queryByText(strings.ownerManagedInlineNote)).toBeNull();
    expect(within(rowOf(12)).queryByText(strings.ownerUnknownInlineNote)).toBeNull();
  });

  it('repeats the note as the Delete and Pause confirm notice on an app-owned row only', async () => {
    const user = userEvent.setup();
    await renderOwned();

    await user.click(button(`Delete ${label(APP_OWNED)}`));
    expect(within(rowOf(11)).getAllByText(managedNote)).toHaveLength(1);
    expect(within(rowOf(11)).getAllByText(strings.ownerManagedInlineNote)).toHaveLength(1);
    await user.click(button(`Cancel Delete ${label(APP_OWNED)}`));

    await user.click(button(`Pause ${label(APP_OWNED)}`));
    expect(
      within(rowOf(11)).getByText(`${strings.pauseHeartbeatNotice} ${managedNote}`),
    ).toBeInTheDocument();
    await user.click(button(`Keep running ${label(APP_OWNED)}`));

    // An unknown owner is never treated as safe: its confirms warn too.
    await user.click(button(`Delete ${label(UNKNOWN_OWNED)}`));
    expect(within(rowOf(13)).getAllByText(strings.ownerUnknownNote)).toHaveLength(1);
    await user.click(button(`Cancel Delete ${label(UNKNOWN_OWNED)}`));
    await user.click(button(`Pause ${label(UNKNOWN_OWNED)}`));
    expect(
      within(rowOf(13)).getByText(`${strings.pauseHeartbeatNotice} ${strings.ownerUnknownNote}`),
    ).toBeInTheDocument();
    await user.click(button(`Keep running ${label(UNKNOWN_OWNED)}`));

    // The operator's own row confirms with no owner note, and Pause shows only its own notice.
    await user.click(button(`Pause ${label(OPERATOR_OWNED)}`));
    expect(within(rowOf(12)).getByText(strings.pauseHeartbeatNotice)).toBeInTheDocument();
    await user.click(button(`Keep running ${label(OPERATOR_OWNED)}`));
    await user.click(button(`Delete ${label(OPERATOR_OWNED)}`));
    expect(button(`Confirm delete ${label(OPERATOR_OWNED)}`)).toBeInTheDocument();
    expect(rowOf(12).querySelector('.confirm-button__notice')).toBeNull();
  });

  it('keeps an open editor row spanning the whole table', async () => {
    const user = userEvent.setup();
    await renderOwned();

    await user.click(button(`Edit ${label(APP_OWNED)}`));

    const headerCount = screen.getAllByRole('columnheader').length;
    const editorCell = screen.getByRole('form', { name: /^Edit / }).closest('td');
    expect(editorCell).toHaveAttribute('colspan', String(headerCount));
    // The form sits in the pinned wrapper, so it fits the scroll wrapper and wraps normally.
    expect(editorCell).toHaveClass('admin-table__editor-cell');
    expect(editorCell?.querySelector('.admin-table__editor')).not.toBeNull();
  });
});

describe('Heartbeats tick', () => {
  const NOW = '2026-10-18T14:00:00Z';

  async function advance(ms: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }

  async function setup(client: DeliveryApiClient) {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(NOW));
    const view = renderSection(client);
    await advance(0);
    return view;
  }

  const TICK = 30_000;

  // Up until 14:10 (deadline 15:10 minus a 1 h grace), late until 15:10.
  const SOON_LATE = buildCheck({
    id: 1,
    name: 'soon_late',
    periodSeconds: 3600,
    graceSeconds: 3600,
    lastPingAt: '2026-10-18T13:10:00Z',
    deadlineAt: '2026-10-18T15:10:00Z',
  });

  it('re-derives late client-side from deadline minus grace, even if the server still says up', async () => {
    // The server keeps answering `up` (e.g. a skewed clock): the local derivation still shows Late.
    const client = buildClient({ listHeartbeats: vi.fn().mockResolvedValue([SOON_LATE]) });
    await setup(client);
    expect(within(rowOf(1)).getByText(strings.heartbeatStateUp)).toBeInTheDocument();
    expect(within(rowOf(1)).getByText('50 min ago')).toBeInTheDocument();

    // 14:10:30 is past the late boundary (14:10) but well before the deadline.
    await advance(10 * 60_000 + TICK);

    expect(within(rowOf(1)).getByText(strings.heartbeatStateLate)).toBeInTheDocument();
    expect(within(rowOf(1)).getByText('down in 59 min')).toBeInTheDocument();
  });

  it('does not use period alone as the late boundary when period differs from grace', async () => {
    // period 1 h, grace 5 h: late only after anchor + 1 h, not after deadline - period.
    const check = buildCheck({
      id: 1,
      periodSeconds: 3600,
      graceSeconds: 5 * 3600,
      lastPingAt: '2026-10-18T14:00:00Z',
      deadlineAt: '2026-10-18T20:00:00Z',
    });
    const client = buildClient({ listHeartbeats: vi.fn().mockResolvedValue([check]) });
    await setup(client);

    // 15:00:30 is past anchor + period (15:00) but before deadline - period (19:00).
    await advance(60 * 60_000 + 30_000);

    expect(within(rowOf(1)).getByText(strings.heartbeatStateLate)).toBeInTheDocument();
  });

  it('quietly reloads once when a live row first turns Late and shows the server state', async () => {
    const serverLate = { ...SOON_LATE, state: 'late' as const };
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([SOON_LATE])
      .mockResolvedValue([serverLate]);
    await setup(buildClient({ listHeartbeats }));

    // 14:10:00 is not yet past the late boundary; no reload.
    await advance(10 * 60_000);
    expect(listHeartbeats).toHaveBeenCalledTimes(1);

    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(strings.heartbeatsLoading)).toBeNull();
    expect(within(rowOf(1)).getByText(strings.heartbeatStateLate)).toBeInTheDocument();

    // A server-reported late row is not reloaded again before its deadline.
    await advance(30 * 60_000);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
  });

  it('does not repeat the Late reload for the same row while the server keeps reporting up', async () => {
    const listHeartbeats = vi.fn().mockResolvedValue([SOON_LATE]);
    await setup(buildClient({ listHeartbeats }));

    await advance(10 * 60_000 + TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);

    // 14:40 is still before the 15:10 deadline.
    await advance(30 * 60_000);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    expect(within(rowOf(1)).getByText(strings.heartbeatStateLate)).toBeInTheDocument();
  });

  it('reloads again when a ping starts a new lateness episode for the same row', async () => {
    // The reload at 14:10:30 shows a ping at 14:10: the next boundary is 15:10 (deadline 16:10).
    const pinged = {
      ...SOON_LATE,
      lastPingAt: '2026-10-18T14:10:00Z',
      deadlineAt: '2026-10-18T16:10:00Z',
    };
    const listHeartbeats = vi.fn().mockResolvedValueOnce([SOON_LATE]).mockResolvedValue([pinged]);
    await setup(buildClient({ listHeartbeats }));

    await advance(10 * 60_000 + TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    expect(within(rowOf(1)).getByText(strings.heartbeatStateUp)).toBeInTheDocument();

    await advance(60 * 60_000);
    expect(listHeartbeats).toHaveBeenCalledTimes(3);
  });

  it('reloads a live row once it is past its deadline, until the server moves it on', async () => {
    const serverLate = { ...SOON_LATE, state: 'late' as const };
    const down = { ...SOON_LATE, state: 'down' as const, downSince: '2026-10-18T15:10:00Z' };
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([SOON_LATE])
      .mockResolvedValueOnce([serverLate])
      .mockResolvedValue([down]);
    await setup(buildClient({ listHeartbeats }));

    // Late reload at 14:10:30; nothing more up to and including the 15:10:00 deadline.
    await advance(70 * 60_000);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);

    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(3);
    expect(screen.queryByText(strings.heartbeatsLoading)).toBeNull();
    expect(within(rowOf(1)).getByText(strings.heartbeatStateDown)).toBeInTheDocument();
    expect(summary()).toHaveTextContent('1 of 1 check down');
  });

  it('clears a recovered Down row after the next tick reload', async () => {
    const down = buildCheck({ id: 1, state: 'down', downSince: '2026-10-18T12:00:00Z' });
    const listHeartbeats = vi.fn().mockResolvedValueOnce([down]).mockResolvedValue([CHECK_1]);
    await setup(buildClient({ listHeartbeats }));
    expect(within(rowOf(1)).getByText(strings.heartbeatStateDown)).toBeInTheDocument();

    await advance(TICK - 1);
    expect(listHeartbeats).toHaveBeenCalledTimes(1);

    await advance(1);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(strings.heartbeatsLoading)).toBeNull();
    expect(within(rowOf(1)).getByText(strings.heartbeatStateUp)).toBeInTheDocument();
    expect(summary()).toHaveTextContent('0 of 1 check down');
  });

  it('does not reload while nothing is Late, Down or past its deadline', async () => {
    const serverLate = { ...SOON_LATE, id: 2, state: 'late' as const };
    const listHeartbeats = vi.fn().mockResolvedValue([
      CHECK_1,
      // Server-reported late and still before its deadline (15:10).
      serverLate,
      buildCheck({ id: 3, paused: true, deadlineAt: '2026-10-18T10:00:00Z' }),
      buildCheck({ id: 4, state: 'new', lastPingAt: null, lastPingKind: null }),
    ]);
    await setup(buildClient({ listHeartbeats }));

    await advance(50 * 60_000);

    expect(listHeartbeats).toHaveBeenCalledTimes(1);
  });

  it('keeps reloading each tick while a row is Down, never overlapping an in-flight reload', async () => {
    const down = buildCheck({ id: 1, state: 'down', downSince: '2026-10-18T12:00:00Z' });
    const slow = deferred<HeartbeatCheck[]>();
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([down])
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValue([down]);
    await setup(buildClient({ listHeartbeats }));

    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);

    // The second reload is still in flight: four more ticks start nothing.
    await advance(4 * TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);

    await act(async () => {
      slow.resolve([down]);
      await slow.promise;
    });
    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(3);
  });

  it('does not reload while the section is hidden, and resumes when it is shown', async () => {
    const down = buildCheck({ id: 1, state: 'down', downSince: '2026-10-18T12:00:00Z' });
    const listHeartbeats = vi.fn().mockResolvedValue([down]);
    const { container } = await setup(buildClient({ listHeartbeats }));

    container.hidden = true;
    await advance(5 * TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(1);

    container.hidden = false;
    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
  });

  it('does not reload while the document is hidden, and resumes when it is visible', async () => {
    const down = buildCheck({ id: 1, state: 'down', downSince: '2026-10-18T12:00:00Z' });
    const listHeartbeats = vi.fn().mockResolvedValue([down]);
    await setup(buildClient({ listHeartbeats }));

    const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
    try {
      await advance(5 * TICK);
      expect(listHeartbeats).toHaveBeenCalledTimes(1);
    } finally {
      hidden.mockRestore();
    }

    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
  });

  it('stops reloading once unmounted', async () => {
    const down = buildCheck({ id: 1, state: 'down', downSince: '2026-10-18T12:00:00Z' });
    const listHeartbeats = vi.fn().mockResolvedValue([down]);
    const { unmount } = await setup(buildClient({ listHeartbeats }));

    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);

    unmount();
    await advance(5 * TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);
  });

  it('ignores a tick reload result for a row deleted while it was in flight', async () => {
    const down = buildCheck({ id: 1, state: 'down', downSince: '2026-10-18T12:00:00Z' });
    const other = buildCheck({ id: 2 });
    const slow = deferred<HeartbeatCheck[]>();
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([down, other])
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValue([down]);
    await setup(buildClient({ listHeartbeats }));

    await advance(TICK);
    expect(listHeartbeats).toHaveBeenCalledTimes(2);

    fireEvent.click(button(`Delete ${label(other)}`));
    fireEvent.click(button(`Confirm delete ${label(other)}`));
    await advance(0);
    expect(document.querySelector('tr[data-check-id="2"]')).toBeNull();

    // The stale tick reload still lists the deleted row; it must not bring it back.
    await act(async () => {
      slow.resolve([down, other]);
      await slow.promise;
    });
    expect(document.querySelector('tr[data-check-id="2"]')).toBeNull();
  });

  it('keeps the old rows when a quiet reload fails', async () => {
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([SOON_LATE])
      .mockRejectedValue(new ApiError({ message: 'boom', status: 502 }));
    await setup(buildClient({ listHeartbeats }));

    await advance(71 * 60_000);

    // The Late reload at 14:10:30, then the 15:10:30 and 15:11:00 ticks past the deadline.
    expect(listHeartbeats).toHaveBeenCalledTimes(4);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(rowOf(1)).toBeInTheDocument();
  });

  it('only changes the summary live region when the Down count changes, not on every tick', async () => {
    const down = buildCheck({ id: 2, state: 'down', downSince: '2026-10-18T13:00:00Z' });
    const serverLate = { ...SOON_LATE, state: 'late' as const };
    const downNow = buildCheck({ id: 1, name: 'soon_late', state: 'down' });
    let rowOneDown = false;
    let loads = 0;
    const listHeartbeats = vi.fn((): Promise<HeartbeatCheck[]> => {
      loads += 1;
      if (loads === 1) {
        return Promise.resolve([SOON_LATE, down]);
      }
      return Promise.resolve(rowOneDown ? [downNow, down] : [serverLate, down]);
    });
    await setup(buildClient({ listHeartbeats }));
    const region = summary();
    expect(region).toHaveTextContent('1 of 2 checks down');

    // Collect in the callback: mutation records are delivered in a microtask,
    // so takeRecords() alone would miss everything already delivered.
    const records: MutationRecord[] = [];
    const observer = new MutationObserver((batch) => records.push(...batch));
    observer.observe(region, { childList: true, characterData: true, subtree: true });

    // Ticks (each reloading, as a row is Down) that re-render relative times and
    // even flip a row to Late change nothing here: the Down count is still 1.
    await advance(TICK);
    await advance(TICK);
    await advance(11 * 60_000);
    expect(within(rowOf(1)).getByText(strings.heartbeatStateLate)).toBeInTheDocument();
    expect([...records, ...observer.takeRecords()]).toHaveLength(0);
    expect(region).toHaveTextContent('1 of 2 checks down');

    // Once the server turns the row Down the count changes, so the text does.
    rowOneDown = true;
    await advance(TICK);
    expect(region).toHaveTextContent('2 of 2 checks down');
    expect([...records, ...observer.takeRecords()].length).toBeGreaterThan(0);
    observer.disconnect();
  });
});

describe('Heartbeats pause, resume and delete', () => {
  it('pause needs a confirm, then shows Resume with focus and an announcement', async () => {
    const user = userEvent.setup();
    const pauseHeartbeat = vi.fn().mockResolvedValue({ ...CHECK_1, paused: true });
    // The reload after the pause sees the server's paused state.
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1, CHECK_2])
      .mockResolvedValue([{ ...CHECK_1, paused: true }, CHECK_2]);
    renderSection(buildClient({ listHeartbeats, pauseHeartbeat }));

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    expect(pauseHeartbeat).not.toHaveBeenCalled();
    expect(screen.getByText(strings.pauseHeartbeatNotice)).toBeInTheDocument();

    await user.click(button(`Confirm pause ${label(CHECK_1)}`));

    expect(pauseHeartbeat).toHaveBeenCalledWith({ id: 1 });
    const resume = await screen.findByRole('button', { name: `Resume ${label(CHECK_1)}` });
    await waitFor(() => expect(resume).toHaveFocus());
    expect(within(rowOf(1)).getByText(strings.heartbeatStatePaused)).toBeInTheDocument();
    expect(resultLine()).toHaveTextContent(
      `Paused ${label(CHECK_1)}. Its alerts are suppressed until you resume it.`,
    );
  });

  it('Keep running dismisses the confirm without a request and restores trigger focus', async () => {
    const user = userEvent.setup();
    const pauseHeartbeat = vi.fn();
    renderSection(
      buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]), pauseHeartbeat }),
    );

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    await user.click(button(`Keep running ${label(CHECK_1)}`));

    expect(pauseHeartbeat).not.toHaveBeenCalled();
    expect(button(`Pause ${label(CHECK_1)}`)).toHaveFocus();
  });

  it.each([
    { verb: 'Pause', dismiss: (name: string) => `Keep running ${name}` },
    { verb: 'Delete', dismiss: (name: string) => `Cancel Delete ${name}` },
  ])(
    'an open $verb confirm hides the row’s other buttons, leaves row 2 alone and restores them on dismiss and Escape',
    async ({ verb, dismiss }) => {
      const user = userEvent.setup();
      renderSection(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1, CHECK_2]) }));
      const name = label(CHECK_1);
      const rowButtons = [`Edit ${name}`, `Pause ${name}`, `Delete ${name}`];
      const present = () =>
        rowButtons.filter(
          (rowButton) => screen.queryByRole('button', { name: rowButton }) !== null,
        );

      await user.click(await screen.findByRole('button', { name: `${verb} ${name}` }));
      expect(button(`Confirm ${verb.toLowerCase()} ${name}`)).toHaveFocus();
      expect(present()).toEqual([]);
      // The hidden buttons stay mounted, so the row's focus keys still resolve.
      expect(screen.getByLabelText(`Edit ${name}`)).toHaveAttribute('hidden');
      for (const other of [`Edit ${label(CHECK_2)}`, `Pause ${label(CHECK_2)}`]) {
        expect(button(other)).toBeVisible();
      }

      await user.click(button(dismiss(name)));
      expect(present()).toEqual(rowButtons);
      expect(button(`${verb} ${name}`)).toHaveFocus();

      await user.click(button(`${verb} ${name}`));
      expect(present()).toEqual([]);
      await user.keyboard('{Escape}');
      expect(present()).toEqual(rowButtons);
      expect(button(`${verb} ${name}`)).toHaveFocus();
    },
  );

  it('an open Delete confirm on a paused check hides Resume too, and Escape brings it back', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listHeartbeats: vi.fn().mockResolvedValue([{ ...CHECK_1, paused: true }, CHECK_2]),
      }),
    );
    const name = label(CHECK_1);

    await user.click(await screen.findByRole('button', { name: `Delete ${name}` }));
    for (const hidden of [`Edit ${name}`, `Resume ${name}`]) {
      expect(screen.queryByRole('button', { name: hidden }), hidden).toBeNull();
    }
    expect(screen.getByLabelText(`Resume ${name}`)).toHaveAttribute('hidden');
    await user.keyboard('{Escape}');

    for (const shown of [`Edit ${name}`, `Resume ${name}`, `Delete ${name}`]) {
      expect(button(shown)).toBeVisible();
    }
  });

  it('confirming a delete runs it once and keeps the row’s buttons in view', async () => {
    const user = userEvent.setup();
    const deleting = deferred<undefined>();
    const deleteHeartbeat = vi.fn().mockReturnValue(deleting.promise);
    renderSection(
      buildClient({
        listHeartbeats: vi.fn().mockResolvedValue([CHECK_1, CHECK_2]),
        deleteHeartbeat,
      }),
    );

    await user.click(await screen.findByRole('button', { name: `Delete ${label(CHECK_1)}` }));
    await user.click(button(`Confirm delete ${label(CHECK_1)}`));

    expect(deleteHeartbeat).toHaveBeenCalledTimes(1);
    expect(deleteHeartbeat).toHaveBeenCalledWith({ id: 1 });
    expect(button(`Edit ${label(CHECK_1)}`)).toBeVisible();
    expect(button(`Pause ${label(CHECK_1)}`)).toBeVisible();
  });

  it('gives two rows mid-confirm distinguishable dismiss buttons', async () => {
    const user = userEvent.setup();
    renderSection(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1, CHECK_2]) }));

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    await user.click(button(`Pause ${label(CHECK_2)}`));

    for (const check of [CHECK_1, CHECK_2]) {
      const dismiss = button(`Keep running ${label(check)}`);
      expect(dismiss).toHaveTextContent(strings.keepHeartbeatRunning);
    }
    expect(screen.queryByRole('button', { name: strings.keepHeartbeatRunning })).toBeNull();

    // Delete's dismiss is distinguishable too. (An open pair hides its row's other buttons, so
    // the Pause confirms are dismissed first.)
    for (const check of [CHECK_1, CHECK_2]) {
      await user.click(button(`Keep running ${label(check)}`));
    }
    await user.click(button(`Delete ${label(CHECK_1)}`));
    await user.click(button(`Delete ${label(CHECK_2)}`));
    expect(button(`Cancel Delete ${label(CHECK_1)}`)).toHaveTextContent(strings.cancelAction);
    expect(button(`Cancel Delete ${label(CHECK_2)}`)).toHaveTextContent(strings.cancelAction);
    expect(screen.queryByRole('button', { name: strings.cancelAction })).toBeNull();
  });

  it('resume is a single click and moves focus to the row Pause trigger', async () => {
    const user = userEvent.setup();
    const paused = { ...CHECK_1, paused: true };
    const resumeHeartbeat = vi.fn().mockResolvedValue(CHECK_1);
    const listHeartbeats = vi.fn().mockResolvedValueOnce([paused]).mockResolvedValue([CHECK_1]);
    renderSection(buildClient({ listHeartbeats, resumeHeartbeat }));

    await user.click(await screen.findByRole('button', { name: `Resume ${label(CHECK_1)}` }));

    expect(resumeHeartbeat).toHaveBeenCalledWith({ id: 1 });
    const pause = await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` });
    await waitFor(() => expect(pause).toHaveFocus());
    expect(resultLine()).toHaveTextContent(`Resumed ${label(CHECK_1)}.`);
  });

  it('a pause in flight blocks resume on the same row', async () => {
    const user = userEvent.setup();
    const pausing = deferred<HeartbeatCheck>();
    const pauseHeartbeat = vi.fn().mockReturnValue(pausing.promise);
    const resumeHeartbeat = vi.fn();
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1])
      // The server has already applied the pause when the refresh lands.
      .mockResolvedValue([{ ...CHECK_1, paused: true }]);
    renderSection(buildClient({ listHeartbeats, pauseHeartbeat, resumeHeartbeat }));

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    await user.click(button(`Confirm pause ${label(CHECK_1)}`));
    expect(pauseHeartbeat).toHaveBeenCalledTimes(1);

    await user.click(button(strings.heartbeatsRefresh));
    const resume = await screen.findByRole('button', { name: /^Resum/ });
    expect(resume).toHaveAttribute('aria-disabled', 'true');
    await user.click(resume);
    expect(resumeHeartbeat).not.toHaveBeenCalled();

    pausing.resolve({ ...CHECK_1, paused: true });
    await waitFor(() =>
      expect(button(`Resume ${label(CHECK_1)}`)).not.toHaveAttribute('aria-disabled'),
    );
  });

  it('does not steal focus from another row when a pause settles', async () => {
    const user = userEvent.setup();
    const pausing = deferred<HeartbeatCheck>();
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1, CHECK_2])
      .mockResolvedValue([{ ...CHECK_1, paused: true }, CHECK_2]);
    renderSection(
      buildClient({ listHeartbeats, pauseHeartbeat: vi.fn().mockReturnValue(pausing.promise) }),
    );

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    await user.click(button(`Confirm pause ${label(CHECK_1)}`));
    // The operator moves on to row 2 while row 1's pause is still in flight.
    act(() => button(`Pause ${label(CHECK_2)}`).focus());

    pausing.resolve({ ...CHECK_1, paused: true });
    await screen.findByRole('button', { name: `Resume ${label(CHECK_1)}` });

    expect(button(`Pause ${label(CHECK_2)}`)).toHaveFocus();
  });

  it('does not steal focus from another row when a delete settles', async () => {
    const user = userEvent.setup();
    const deleting = deferred<undefined>();
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1, CHECK_2, CHECK_3])
      .mockResolvedValue([CHECK_2, CHECK_3]);
    renderSection(
      buildClient({ listHeartbeats, deleteHeartbeat: vi.fn().mockReturnValue(deleting.promise) }),
    );

    await user.click(await screen.findByRole('button', { name: `Delete ${label(CHECK_1)}` }));
    await user.click(button(`Confirm delete ${label(CHECK_1)}`));
    act(() => button(`Pause ${label(CHECK_3)}`).focus());

    deleting.resolve(undefined);
    await waitFor(() => expect(document.querySelector('tr[data-check-id="1"]')).toBeNull());

    expect(button(`Pause ${label(CHECK_3)}`)).toHaveFocus();
  });

  it('ignores a pause response for a row that was deleted meanwhile', async () => {
    const user = userEvent.setup();
    const pausing = deferred<HeartbeatCheck>();
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1, CHECK_2])
      .mockResolvedValue([CHECK_2]);
    renderSection(
      buildClient({ listHeartbeats, pauseHeartbeat: vi.fn().mockReturnValue(pausing.promise) }),
    );

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    await user.click(button(`Confirm pause ${label(CHECK_1)}`));
    await user.click(button(`Delete ${label(CHECK_1)}`));
    await user.click(button(`Confirm delete ${label(CHECK_1)}`));
    await waitFor(() => expect(document.querySelector('tr[data-check-id="1"]')).toBeNull());
    expect(resultLine()).toHaveTextContent(`Deleted ${label(CHECK_1)}.`);

    pausing.resolve({ ...CHECK_1, paused: true });
    await act(async () => {
      await pausing.promise;
    });

    expect(resultLine()).toHaveTextContent(`Deleted ${label(CHECK_1)}.`);
  });

  it('shows a pause failure as a row error and keeps the Pause trigger', async () => {
    const user = userEvent.setup();
    const pauseHeartbeat = vi
      .fn()
      .mockRejectedValue(new ApiError({ message: 'delivery service unavailable', status: 503 }));
    renderSection(
      buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]), pauseHeartbeat }),
    );

    await user.click(await screen.findByRole('button', { name: `Pause ${label(CHECK_1)}` }));
    await user.click(button(`Confirm pause ${label(CHECK_1)}`));

    expect((await screen.findByRole('alert')).textContent).toBe(
      'Could not pause: delivery service unavailable',
    );
    expect(button(`Pause ${label(CHECK_1)}`)).toBeInTheDocument();
  });

  it('shows a resume failure and falls back to the generic message for non-API errors', async () => {
    const user = userEvent.setup();
    const resumeHeartbeat = vi.fn().mockRejectedValue(new Error('network'));
    renderSection(
      buildClient({
        listHeartbeats: vi.fn().mockResolvedValue([{ ...CHECK_1, paused: true }]),
        resumeHeartbeat,
      }),
    );

    await user.click(await screen.findByRole('button', { name: `Resume ${label(CHECK_1)}` }));

    expect((await screen.findByRole('alert')).textContent).toBe(
      `Could not resume: ${strings.genericError}`,
    );
  });

  it('delete needs a confirm, removes the row and moves focus to the next row trigger', async () => {
    const user = userEvent.setup();
    const deleteHeartbeat = vi.fn().mockResolvedValue(undefined);
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1, CHECK_2, CHECK_3])
      .mockResolvedValue([CHECK_1, CHECK_3]);
    renderSection(buildClient({ listHeartbeats, deleteHeartbeat }));

    await user.click(await screen.findByRole('button', { name: `Delete ${label(CHECK_2)}` }));
    expect(deleteHeartbeat).not.toHaveBeenCalled();
    await user.click(button(`Confirm delete ${label(CHECK_2)}`));

    expect(deleteHeartbeat).toHaveBeenCalledWith({ id: 2 });
    await waitFor(() => expect(document.querySelector('tr[data-check-id="2"]')).toBeNull());
    await waitFor(() => expect(button(`Pause ${label(CHECK_3)}`)).toHaveFocus());
    expect(resultLine()).toHaveTextContent(`Deleted ${label(CHECK_2)}.`);
  });

  it('delete of the last row focuses the previous row, and a paused neighbour its Resume', async () => {
    const user = userEvent.setup();
    const pausedFirst = { ...CHECK_1, paused: true };
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([pausedFirst, CHECK_2])
      .mockResolvedValue([pausedFirst]);
    renderSection(buildClient({ listHeartbeats }));

    await user.click(await screen.findByRole('button', { name: `Delete ${label(CHECK_2)}` }));
    await user.click(button(`Confirm delete ${label(CHECK_2)}`));

    await waitFor(() => expect(button(`Resume ${label(CHECK_1)}`)).toHaveFocus());
  });

  it('delete of the only row moves focus to the heading', async () => {
    const user = userEvent.setup();
    const listHeartbeats = vi.fn().mockResolvedValueOnce([CHECK_1]).mockResolvedValue([]);
    renderSection(buildClient({ listHeartbeats }));

    await user.click(await screen.findByRole('button', { name: `Delete ${label(CHECK_1)}` }));
    await user.click(button(`Confirm delete ${label(CHECK_1)}`));

    await waitFor(() => expect(heading()).toHaveFocus());
    expect(await screen.findByText(strings.heartbeatsEmpty)).toBeInTheDocument();
  });

  it('shows a delete failure as a row error and keeps the row', async () => {
    const user = userEvent.setup();
    const deleteHeartbeat = vi
      .fn()
      .mockRejectedValue(new ApiError({ message: 'not allowed', status: 403 }));
    renderSection(
      buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]), deleteHeartbeat }),
    );

    await user.click(await screen.findByRole('button', { name: `Delete ${label(CHECK_1)}` }));
    await user.click(button(`Confirm delete ${label(CHECK_1)}`));

    expect((await screen.findByRole('alert')).textContent).toBe('Could not delete: not allowed');
    expect(rowOf(1)).toBeInTheDocument();
  });

  it('closes the editor of a row that is deleted', async () => {
    const user = userEvent.setup();
    // The row comes back on a later load: a stale editingId would re-open its editor.
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1, CHECK_2])
      .mockResolvedValueOnce([CHECK_2])
      .mockResolvedValue([CHECK_1, CHECK_2]);
    renderSection(buildClient({ listHeartbeats }), FULL_DESTINATIONS);

    await user.click(await screen.findByRole('button', { name: `Edit ${label(CHECK_1)}` }));
    expect(screen.getByRole('form', { name: `Edit ${label(CHECK_1)}` })).toBeInTheDocument();
    await user.click(button(`Delete ${label(CHECK_1)}`));
    await user.click(button(`Confirm delete ${label(CHECK_1)}`));

    await waitFor(() => expect(document.querySelector('tr[data-check-id="1"]')).toBeNull());
    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();

    await user.click(button(strings.heartbeatsRefresh));
    await waitFor(() => expect(rowOf(1)).toBeInTheDocument());
    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();
    expect(button(`Edit ${label(CHECK_1)}`)).toHaveAttribute('aria-expanded', 'false');
  });
});

function destination(
  name: string,
  overrides: Partial<DestinationSummary> = {},
): DestinationSummary {
  return {
    name,
    type: 'ntfy',
    enabled: true,
    disabledReason: null,
    config: { topic: 'family_alerts' },
    secrets: {},
    ownerKind: 'operator',
    ownerAppId: null,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ...overrides,
  };
}

const FULL_DESTINATIONS: DestinationsLoadState = {
  status: 'ready',
  destinations: [
    destination('ops_discord', { type: 'discord' }),
    destination('family_ntfy'),
    destination('mc_hook', { type: 'webhook', enabled: false }),
  ],
};

function addForm(): HTMLElement {
  return screen.getByRole('form', { name: strings.addHeartbeatHeading });
}

function submitButton(): HTMLElement {
  const submit = screen
    .getAllByRole('button', { name: strings.addHeartbeatAction })
    .find((element) => element.getAttribute('type') === 'submit');
  if (submit === undefined) {
    throw new Error('submit button not found');
  }
  return submit;
}

function checkbox(name: string): HTMLElement {
  // The label is "<name> <type> <marks>"; jsdom's name computation drops the
  // space before the small-text span, so match the prefix rather than a word edge.
  return screen.getByRole('checkbox', { name: new RegExp(`^${name}`) });
}

async function fillAddForm({
  user,
  appId,
  name,
}: {
  user: ReturnType<typeof userEvent.setup>;
  appId: string;
  name: string;
}) {
  // user.type rejects an empty string, so an empty value just leaves the field blank.
  if (appId !== '') {
    await user.type(screen.getByLabelText(strings.heartbeatAppIdLabel), appId);
  }
  if (name !== '') {
    await user.type(screen.getByLabelText(strings.heartbeatNameLabel), name);
  }
}

async function setDuration({
  user,
  label: fieldLabel,
  unitLabel,
  value,
  unit,
}: {
  user: ReturnType<typeof userEvent.setup>;
  label: string;
  unitLabel: string;
  value: string;
  unit: string;
}) {
  const input = screen.getByLabelText(fieldLabel);
  await user.clear(input);
  if (value !== '') {
    await user.type(input, value);
  }
  await user.selectOptions(screen.getByLabelText(unitLabel), unit);
}

describe('Heartbeats add check', () => {
  const hintCases: Array<[string, DestinationsLoadState, string]> = [
    ['loading', { status: 'loading', destinations: [] }, strings.addHeartbeatHintLoading],
    ['error', { status: 'error', destinations: [] }, strings.addHeartbeatHintError],
    ['empty', { status: 'ready', destinations: [] }, strings.addHeartbeatHintEmpty],
  ];

  it.each(hintCases)('is aria-disabled with a %s hint', async (_label, destinationsState, hint) => {
    const user = userEvent.setup();
    renderSection(buildClient(), destinationsState);
    await screen.findByText(strings.heartbeatsEmpty);

    const add = button(strings.addHeartbeatAction);
    expect(add).toHaveAttribute('aria-disabled', 'true');
    const describedBy = add.getAttribute('aria-describedby');
    expect(describedBy).not.toBeNull();
    expect(document.getElementById(describedBy ?? '')?.textContent).toBe(hint);

    await user.click(add);
    expect(screen.queryByRole('form', { name: strings.addHeartbeatHeading })).toBeNull();
  });

  it('is enabled with no hint once destinations are ready and non-empty', async () => {
    renderSection(buildClient(), FULL_DESTINATIONS);
    await screen.findByText(strings.heartbeatsEmpty);

    const add = button(strings.addHeartbeatAction);
    expect(add).not.toHaveAttribute('aria-disabled');
    expect(add).not.toHaveAttribute('aria-describedby');
    expect(document.getElementById('heartbeats-add-hint')).toBeNull();
  });

  it('opens focused on the App ID, with the default period and grace and destination marks', async () => {
    const user = userEvent.setup();
    renderSection(buildClient(), FULL_DESTINATIONS);
    await user.click(button(strings.addHeartbeatAction));

    expect(screen.getByLabelText(strings.heartbeatAppIdLabel)).toHaveFocus();
    expect(screen.getByLabelText(strings.heartbeatPeriodLabel)).toHaveValue(24);
    expect(screen.getByLabelText(strings.heartbeatPeriodUnitLabel)).toHaveValue('hours');
    expect(screen.getByLabelText(strings.heartbeatGraceLabel)).toHaveValue(2);
    expect(screen.getByLabelText(strings.heartbeatGraceUnitLabel)).toHaveValue('hours');
    expect(screen.getByText(strings.heartbeatPeriodHint)).toBeInTheDocument();
    expect(screen.getByText(strings.heartbeatGraceHint)).toBeInTheDocument();
    expect(within(addForm()).getByText(strings.heartbeatDestinationsLegend)).toBeInTheDocument();
    expect(checkbox('ops_discord').closest('label')).toHaveTextContent('ops_discord discord');
    expect(checkbox('mc_hook').closest('label')).toHaveTextContent(
      `webhook ${strings.routeDestinationDisabledMark}`,
    );
    expect(checkbox('ops_discord')).not.toBeChecked();
  });

  it('creates a check, closes the card, shows the row and returns focus to Add', async () => {
    const user = userEvent.setup();
    const created = buildCheck({
      id: 7,
      appId: 'minecrafting',
      name: 'daily_report',
      state: 'new',
      lastPingAt: null,
      lastPingKind: null,
    });
    const createHeartbeat = vi.fn().mockResolvedValue(created);
    const listHeartbeats = vi.fn().mockResolvedValueOnce([]).mockResolvedValue([created]);
    renderSection(buildClient({ createHeartbeat, listHeartbeats }), FULL_DESTINATIONS);
    await screen.findByText(strings.heartbeatsEmpty);

    await user.click(button(strings.addHeartbeatAction));
    await fillAddForm({ user, appId: '  minecrafting ', name: 'daily_report' });
    await user.click(checkbox('family_ntfy'));
    await user.click(checkbox('ops_discord'));
    await user.click(submitButton());

    await waitFor(() =>
      expect(createHeartbeat).toHaveBeenCalledWith({
        appId: 'minecrafting',
        name: 'daily_report',
        periodSeconds: 86_400,
        graceSeconds: 7200,
        // Sent in the list's display order, not click order.
        destinations: ['ops_discord', 'family_ntfy'],
      }),
    );
    await waitFor(() =>
      expect(screen.queryByRole('form', { name: strings.addHeartbeatHeading })).toBeNull(),
    );
    expect(within(rowOf(7)).getByText(strings.heartbeatStateWaiting)).toBeInTheDocument();
    expect(resultLine()).toHaveTextContent(`Added minecrafting/daily_report.`);
    await waitFor(() => expect(button(strings.addHeartbeatAction)).toHaveFocus());
  });

  it('converts the unit selects to seconds', async () => {
    const user = userEvent.setup();
    const createHeartbeat = vi.fn().mockResolvedValue(CHECK_1);
    renderSection(buildClient({ createHeartbeat }), FULL_DESTINATIONS);
    await user.click(button(strings.addHeartbeatAction));
    await fillAddForm({ user, appId: 'urls4irl', name: 'nightly' });
    await setDuration({
      user,
      label: strings.heartbeatPeriodLabel,
      unitLabel: strings.heartbeatPeriodUnitLabel,
      value: '2',
      unit: 'days',
    });
    await setDuration({
      user,
      label: strings.heartbeatGraceLabel,
      unitLabel: strings.heartbeatGraceUnitLabel,
      value: '90',
      unit: 'minutes',
    });
    await user.click(checkbox('family_ntfy'));
    await user.click(submitButton());

    await waitFor(() =>
      expect(createHeartbeat).toHaveBeenCalledWith({
        appId: 'urls4irl',
        name: 'nightly',
        periodSeconds: 172_800,
        graceSeconds: 5400,
        destinations: ['family_ntfy'],
      }),
    );
  });

  describe('submit-time validation', () => {
    type Case = {
      name: string;
      appId?: string;
      checkName?: string;
      period?: [string, string];
      grace?: [string, string];
      destinations?: string[];
      message: string;
    };
    const cases: Case[] = [
      { name: 'a bad app id', appId: 'Bad-App', message: strings.invalidHeartbeatAppId },
      { name: 'an empty app id', appId: '', message: strings.invalidHeartbeatAppId },
      { name: 'a bad check name', checkName: '_nope', message: strings.invalidHeartbeatName },
      {
        name: 'a period of zero',
        period: ['0', 'minutes'],
        message: strings.invalidHeartbeatPeriod,
      },
      { name: 'an empty period', period: ['', 'hours'], message: strings.invalidHeartbeatPeriod },
      {
        name: 'a period over 31 days',
        period: ['32', 'days'],
        message: strings.invalidHeartbeatPeriod,
      },
      {
        name: 'a period under a minute',
        period: ['0.5', 'minutes'],
        message: strings.invalidHeartbeatPeriod,
      },
      { name: 'a grace of zero', grace: ['0', 'hours'], message: strings.invalidHeartbeatGrace },
      { name: 'a grace over 7 days', grace: ['8', 'days'], message: strings.invalidHeartbeatGrace },
      { name: 'no destination', destinations: [], message: strings.heartbeatNeedsDestination },
    ];

    it.each(cases)('blocks $name', async (testCase) => {
      const user = userEvent.setup();
      const createHeartbeat = vi.fn();
      renderSection(buildClient({ createHeartbeat }), FULL_DESTINATIONS);
      await user.click(button(strings.addHeartbeatAction));
      await fillAddForm({
        user,
        appId: testCase.appId ?? 'urls4irl',
        name: testCase.checkName ?? 'nightly',
      });
      if (testCase.period !== undefined) {
        await setDuration({
          user,
          label: strings.heartbeatPeriodLabel,
          unitLabel: strings.heartbeatPeriodUnitLabel,
          value: testCase.period[0],
          unit: testCase.period[1],
        });
      }
      if (testCase.grace !== undefined) {
        await setDuration({
          user,
          label: strings.heartbeatGraceLabel,
          unitLabel: strings.heartbeatGraceUnitLabel,
          value: testCase.grace[0],
          unit: testCase.grace[1],
        });
      }
      for (const name of testCase.destinations ?? ['ops_discord']) {
        await user.click(checkbox(name));
      }
      await user.click(submitButton());

      expect(within(addForm()).getByRole('alert')).toHaveTextContent(testCase.message);
      expect(createHeartbeat).not.toHaveBeenCalled();
    });

    it.each([
      ['period of 1 minute', ['1', 'minutes'], ['2', 'hours'], 60, 7200],
      ['period of 31 days', ['31', 'days'], ['2', 'hours'], 2_678_400, 7200],
      ['grace of 1 minute', ['24', 'hours'], ['1', 'minutes'], 86_400, 60],
      ['grace of 7 days', ['24', 'hours'], ['7', 'days'], 86_400, 604_800],
    ] as const)('accepts a %s', async (_label, period, grace, periodSeconds, graceSeconds) => {
      const user = userEvent.setup();
      const createHeartbeat = vi.fn().mockResolvedValue(CHECK_1);
      renderSection(buildClient({ createHeartbeat }), FULL_DESTINATIONS);
      await user.click(button(strings.addHeartbeatAction));
      await fillAddForm({ user, appId: 'urls4irl', name: 'edges' });
      await setDuration({
        user,
        label: strings.heartbeatPeriodLabel,
        unitLabel: strings.heartbeatPeriodUnitLabel,
        value: period[0],
        unit: period[1],
      });
      await setDuration({
        user,
        label: strings.heartbeatGraceLabel,
        unitLabel: strings.heartbeatGraceUnitLabel,
        value: grace[0],
        unit: grace[1],
      });
      await user.click(checkbox('ops_discord'));
      await user.click(submitButton());

      await waitFor(() =>
        expect(createHeartbeat).toHaveBeenCalledWith(
          expect.objectContaining({ periodSeconds, graceSeconds }),
        ),
      );
    });

    it('blocks more than 20 destinations', async () => {
      const user = userEvent.setup();
      const createHeartbeat = vi.fn();
      const many: DestinationsLoadState = {
        status: 'ready',
        destinations: Array.from({ length: 21 }, (_unused, index) =>
          destination(`dest_${String(index).padStart(2, '0')}`),
        ),
      };
      renderSection(buildClient({ createHeartbeat }), many);
      await user.click(button(strings.addHeartbeatAction));
      await fillAddForm({ user, appId: 'urls4irl', name: 'nightly' });
      for (const entry of many.destinations) {
        await user.click(checkbox(entry.name));
      }
      await user.click(submitButton());

      expect(within(addForm()).getByRole('alert')).toHaveTextContent(
        strings.heartbeatTooManyDestinations,
      );
      expect(createHeartbeat).not.toHaveBeenCalled();
    });
  });

  it('shows a server 409 in the form alert and keeps the form open', async () => {
    const user = userEvent.setup();
    const createHeartbeat = vi
      .fn()
      .mockRejectedValue(new ApiError({ message: 'check already exists', status: 409 }));
    renderSection(buildClient({ createHeartbeat }), FULL_DESTINATIONS);
    await user.click(button(strings.addHeartbeatAction));
    await fillAddForm({ user, appId: 'urls4irl', name: 'nightly' });
    await user.click(checkbox('ops_discord'));
    await user.click(submitButton());

    expect(await within(addForm()).findByRole('alert')).toHaveTextContent('check already exists');
    expect(addForm()).toBeInTheDocument();
    expect(screen.getByLabelText(strings.heartbeatNameLabel)).toHaveValue('nightly');
  });

  it('shows a generic message for a non-API failure', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({ createHeartbeat: vi.fn().mockRejectedValue(new Error('network')) }),
      FULL_DESTINATIONS,
    );
    await user.click(button(strings.addHeartbeatAction));
    await fillAddForm({ user, appId: 'urls4irl', name: 'nightly' });
    await user.click(checkbox('ops_discord'));
    await user.click(submitButton());

    expect(await within(addForm()).findByRole('alert')).toHaveTextContent(strings.genericError);
  });

  it('guards a create in flight: one request, a busy submit, and no way to leave the form', async () => {
    const user = userEvent.setup();
    const creating = deferred<HeartbeatCheck>();
    const createHeartbeat = vi.fn().mockReturnValue(creating.promise);
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1])
      .mockResolvedValue([CHECK_1, CHECK_2]);
    renderSection(buildClient({ createHeartbeat, listHeartbeats }), FULL_DESTINATIONS);
    await screen.findByRole('button', { name: `Edit ${label(CHECK_1)}` });
    await user.click(button(strings.addHeartbeatAction));
    await fillAddForm({ user, appId: 'urls4irl', name: 'check_2' });
    await user.click(checkbox('ops_discord'));
    await user.click(submitButton());
    await user.click(screen.getByRole('button', { name: strings.addingHeartbeat }));

    expect(createHeartbeat).toHaveBeenCalledTimes(1);
    const busy = screen.getByRole('button', { name: strings.addingHeartbeat });
    expect(busy).toHaveAttribute('aria-disabled', 'true');

    // Cancel, the Add toggle and the row Edit triggers are inert until it settles.
    await user.click(button(strings.cancelAction));
    await user.click(button(strings.addHeartbeatAction));
    await user.click(button(`Edit ${label(CHECK_1)}`));
    expect(addForm()).toBeInTheDocument();
    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();

    await act(async () => {
      creating.resolve(CHECK_2);
      await creating.promise;
    });
    await waitFor(() => expect(screen.queryByRole('form', { name: /^Add/ })).toBeNull());
    expect(rowOf(2)).toBeInTheDocument();
  });

  it('the Add toggle can still close an open card when the destinations are no longer ready', async () => {
    const user = userEvent.setup();
    const client = buildClient();
    const { rerender } = renderSection(client, FULL_DESTINATIONS);
    await user.click(button(strings.addHeartbeatAction));

    rerender(
      <Heartbeats client={client} destinationsState={{ status: 'loading', destinations: [] }} />,
    );
    const toggle = screen
      .getAllByRole('button', { name: strings.addHeartbeatAction })
      .find((element) => element.hasAttribute('aria-expanded'));
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(toggle).not.toHaveAttribute('aria-disabled');
    await user.click(toggle as HTMLElement);

    expect(screen.queryByRole('form', { name: strings.addHeartbeatHeading })).toBeNull();
  });

  it('keeps a selected destination that left the shared list, marked not found', async () => {
    const user = userEvent.setup();
    const client = buildClient();
    const { rerender } = renderSection(client, FULL_DESTINATIONS);
    await user.click(button(strings.addHeartbeatAction));
    await user.click(checkbox('ops_discord'));

    rerender(
      <Heartbeats
        client={client}
        destinationsState={{ status: 'ready', destinations: [destination('family_ntfy')] }}
      />,
    );

    expect(checkbox('ops_discord')).toBeChecked();
    expect(checkbox('ops_discord').closest('label')).toHaveTextContent(
      strings.heartbeatDestinationNotFoundMark,
    );
  });

  it('Cancel closes the card and returns focus to Add', async () => {
    const user = userEvent.setup();
    renderSection(buildClient(), FULL_DESTINATIONS);
    await user.click(button(strings.addHeartbeatAction));
    await user.click(button(strings.cancelAction));

    expect(screen.queryByRole('form', { name: strings.addHeartbeatHeading })).toBeNull();
    await waitFor(() => expect(button(strings.addHeartbeatAction)).toHaveFocus());
    expect(button(strings.addHeartbeatAction)).toHaveAttribute('aria-expanded', 'false');
  });

  it('the destination checkboxes track a destinations change while the card is open', async () => {
    const user = userEvent.setup();
    const client = buildClient();
    const { rerender } = renderSection(client, FULL_DESTINATIONS);
    await user.click(button(strings.addHeartbeatAction));
    await user.click(checkbox('ops_discord'));
    expect(checkbox('ops_discord').closest('label')).not.toHaveTextContent(
      strings.routeDestinationDisabledMark,
    );

    rerender(
      <Heartbeats
        client={client}
        destinationsState={{
          status: 'ready',
          destinations: [
            destination('ops_discord', { type: 'discord', enabled: false }),
            destination('family_ntfy'),
          ],
        }}
      />,
    );

    expect(checkbox('ops_discord').closest('label')).toHaveTextContent(
      strings.routeDestinationDisabledMark,
    );
    // The selection survives, and a removed destination's box is gone.
    expect(checkbox('ops_discord')).toBeChecked();
    expect(screen.queryByRole('checkbox', { name: /^mc_hook/ })).toBeNull();
  });
});

describe('Heartbeats edit check', () => {
  const EDITED = buildCheck({
    id: 1,
    periodSeconds: 86_400,
    graceSeconds: 7200,
    destinations: [{ name: 'ops_discord', type: 'discord', enabled: true }],
  });

  async function openEditor({
    checks = [EDITED],
    client,
    destinationsState = FULL_DESTINATIONS,
    target = EDITED,
  }: {
    checks?: HeartbeatCheck[];
    client?: Partial<DeliveryApiClient>;
    destinationsState?: DestinationsLoadState;
    target?: HeartbeatCheck;
  } = {}) {
    const user = userEvent.setup();
    const built = buildClient({
      listHeartbeats: vi.fn().mockResolvedValue(checks),
      ...client,
    });
    renderSection(built, destinationsState);
    await user.click(await screen.findByRole('button', { name: `Edit ${label(target)}` }));
    return { user, client: built };
  }

  function editForm(target: HeartbeatCheck = EDITED): HTMLElement {
    return screen.getByRole('form', { name: `Edit ${label(target)}` });
  }

  it('opens inline in a colSpan row focused on Period, with read-only ids and pre-selected units', async () => {
    await openEditor();

    const form = editForm();
    // The editor spans every column: State, Check, Owner, Last ping, Schedule, Alerts to, Actions.
    expect(screen.getAllByRole('columnheader')).toHaveLength(7);
    expect(form.closest('td')).toHaveAttribute('colspan', '7');
    expect(form.closest('tr')).not.toHaveAttribute('data-check-id');
    expect(screen.getByLabelText(strings.heartbeatPeriodLabel)).toHaveFocus();
    // 86400 s is shown as 24 hours (days start at two), and 24 hours converts back to 86400 s.
    expect(screen.getByLabelText(strings.heartbeatPeriodLabel)).toHaveValue(24);
    expect(screen.getByLabelText(strings.heartbeatPeriodUnitLabel)).toHaveValue('hours');
    expect(screen.getByLabelText(strings.heartbeatGraceLabel)).toHaveValue(2);
    expect(screen.getByLabelText(strings.heartbeatGraceUnitLabel)).toHaveValue('hours');
    expect(
      within(form).getByText(strings.heartbeatEditingNotice({ check: label(EDITED) })),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText(strings.heartbeatAppIdLabel)).toBeNull();
    expect(screen.queryByLabelText(strings.heartbeatNameLabel)).toBeNull();
    expect(checkbox('ops_discord')).toBeChecked();
    expect(checkbox('family_ntfy')).not.toBeChecked();
    expect(button(`Close ${label(EDITED)}`)).toHaveAttribute('aria-expanded', 'true');
  });

  it('sends only the changed field, then closes, shows the saved row and refocuses the Edit trigger', async () => {
    const updated = { ...EDITED, graceSeconds: 10_800 };
    const updateHeartbeat = vi.fn().mockResolvedValue(updated);
    const { user } = await openEditor({ client: { updateHeartbeat } });

    await setDuration({
      user,
      label: strings.heartbeatGraceLabel,
      unitLabel: strings.heartbeatGraceUnitLabel,
      value: '3',
      unit: 'hours',
    });
    await user.click(button(strings.saveHeartbeat));

    await waitFor(() => expect(updateHeartbeat).toHaveBeenCalledTimes(1));
    const body = updateHeartbeat.mock.calls[0][0] as Record<string, unknown>;
    expect(body.id).toBe(1);
    expect(body.graceSeconds).toBe(10_800);
    expect(body.periodSeconds).toBeUndefined();
    expect(body.destinations).toBeUndefined();
    await waitFor(() => expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull());
    expect(resultLine()).toHaveTextContent(`Saved ${label(EDITED)}.`);
    await waitFor(() => expect(button(`Edit ${label(EDITED)}`)).toHaveFocus());
  });

  it('sends the full destination set when it changed', async () => {
    const updateHeartbeat = vi.fn().mockResolvedValue(EDITED);
    const { user } = await openEditor({ client: { updateHeartbeat } });

    await user.click(checkbox('family_ntfy'));
    await user.click(button(strings.saveHeartbeat));

    await waitFor(() => expect(updateHeartbeat).toHaveBeenCalledTimes(1));
    const body = updateHeartbeat.mock.calls[0][0] as Record<string, unknown>;
    expect(body.destinations).toEqual(['ops_discord', 'family_ntfy']);
    expect(body.periodSeconds).toBeUndefined();
    expect(body.graceSeconds).toBeUndefined();
  });

  it('an untouched Save sends nothing, closes, and refocuses the Edit trigger', async () => {
    const updateHeartbeat = vi.fn();
    const { user } = await openEditor({ client: { updateHeartbeat } });

    await user.click(button(strings.saveHeartbeat));

    expect(updateHeartbeat).not.toHaveBeenCalled();
    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();
    await waitFor(() => expect(button(`Edit ${label(EDITED)}`)).toHaveFocus());
  });

  it('Close without saving closes the row and refocuses the Edit trigger', async () => {
    const { user } = await openEditor();

    await user.click(button(strings.closeEditAction));

    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();
    await waitFor(() => expect(button(`Edit ${label(EDITED)}`)).toHaveFocus());
  });

  it('the row trigger toggles the editor closed again', async () => {
    const { user } = await openEditor();

    await user.click(button(`Close ${label(EDITED)}`));

    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();
    expect(button(`Edit ${label(EDITED)}`)).toHaveAttribute('aria-expanded', 'false');
  });

  it('round-trips an untouched duration back to the exact stored seconds', async () => {
    const odd = buildCheck({ id: 1, periodSeconds: 5400, graceSeconds: 90 });
    const updateHeartbeat = vi.fn().mockResolvedValue(odd);
    const { user } = await openEditor({ checks: [odd], target: odd, client: { updateHeartbeat } });

    expect(screen.getByLabelText(strings.heartbeatPeriodLabel)).toHaveValue(90);
    expect(screen.getByLabelText(strings.heartbeatPeriodUnitLabel)).toHaveValue('minutes');
    expect(screen.getByLabelText(strings.heartbeatGraceLabel)).toHaveValue(90);
    expect(screen.getByLabelText(strings.heartbeatGraceUnitLabel)).toHaveValue('seconds');
    await user.click(button(strings.saveHeartbeat));

    expect(updateHeartbeat).not.toHaveBeenCalled();
  });

  it.each([
    [
      'period',
      strings.heartbeatPeriodLabel,
      strings.heartbeatPeriodUnitLabel,
      '0',
      'minutes',
      strings.invalidHeartbeatPeriod,
    ],
    [
      'period',
      strings.heartbeatPeriodLabel,
      strings.heartbeatPeriodUnitLabel,
      '32',
      'days',
      strings.invalidHeartbeatPeriod,
    ],
    [
      'grace',
      strings.heartbeatGraceLabel,
      strings.heartbeatGraceUnitLabel,
      '8',
      'days',
      strings.invalidHeartbeatGrace,
    ],
  ])('blocks an out-of-range %s', async (_which, fieldLabel, unitLabel, value, unit, message) => {
    const updateHeartbeat = vi.fn();
    const { user } = await openEditor({ client: { updateHeartbeat } });

    await setDuration({ user, label: fieldLabel, unitLabel, value, unit });
    await user.click(button(strings.saveHeartbeat));

    expect(within(editForm()).getByRole('alert')).toHaveTextContent(message);
    expect(updateHeartbeat).not.toHaveBeenCalled();
  });

  it('blocks unticking every destination', async () => {
    const updateHeartbeat = vi.fn();
    const { user } = await openEditor({ client: { updateHeartbeat } });

    await user.click(checkbox('ops_discord'));
    await user.click(button(strings.saveHeartbeat));

    expect(within(editForm()).getByRole('alert')).toHaveTextContent(
      strings.heartbeatNeedsDestination,
    );
    expect(updateHeartbeat).not.toHaveBeenCalled();
  });

  it('keeps an unknown destination selected, marked not found, and omits destinations when only the grace changed', async () => {
    const stale = buildCheck({
      id: 1,
      destinations: [
        { name: 'ops_discord', type: 'discord', enabled: true },
        { name: 'old_hook', type: 'webhook', enabled: true },
      ],
    });
    const updateHeartbeat = vi.fn().mockResolvedValue(stale);
    const { user } = await openEditor({
      checks: [stale],
      target: stale,
      client: { updateHeartbeat },
    });

    expect(checkbox('old_hook')).toBeChecked();
    expect(checkbox('old_hook').closest('label')).toHaveTextContent(
      strings.heartbeatDestinationNotFoundMark,
    );
    expect(checkbox('ops_discord').closest('label')).not.toHaveTextContent(
      strings.heartbeatDestinationNotFoundMark,
    );

    // Editing something else does not send destinations at all.
    await setDuration({
      user,
      label: strings.heartbeatGraceLabel,
      unitLabel: strings.heartbeatGraceUnitLabel,
      value: '3',
      unit: 'hours',
    });
    await user.click(button(strings.saveHeartbeat));
    await waitFor(() => expect(updateHeartbeat).toHaveBeenCalledTimes(1));
    expect(
      (updateHeartbeat.mock.calls[0][0] as Record<string, unknown>).destinations,
    ).toBeUndefined();
  });

  it('sends the stale name back with the set when another destination is added', async () => {
    const stale = buildCheck({
      id: 1,
      destinations: [
        { name: 'ops_discord', type: 'discord', enabled: true },
        { name: 'old_hook', type: 'webhook', enabled: true },
      ],
    });
    const updateHeartbeat = vi.fn().mockResolvedValue(stale);
    const { user } = await openEditor({
      checks: [stale],
      target: stale,
      client: { updateHeartbeat },
    });

    await user.click(checkbox('family_ntfy'));
    await user.click(button(strings.saveHeartbeat));

    await waitFor(() => expect(updateHeartbeat).toHaveBeenCalledTimes(1));
    expect((updateHeartbeat.mock.calls[0][0] as Record<string, unknown>).destinations).toEqual([
      'ops_discord',
      'family_ntfy',
      'old_hook',
    ]);
  });

  it('keeps an unticked stale destination visible so it can be re-ticked', async () => {
    const stale = buildCheck({
      id: 1,
      destinations: [{ name: 'old_hook', type: 'webhook', enabled: true }],
    });
    const { user } = await openEditor({ checks: [stale], target: stale });

    await user.click(checkbox('old_hook'));
    expect(checkbox('old_hook')).not.toBeChecked();
    await user.click(checkbox('old_hook'));
    expect(checkbox('old_hook')).toBeChecked();
  });

  it('does not call a destination not found while the destinations list is still loading', async () => {
    await openEditor({
      destinationsState: { status: 'loading', destinations: [] },
    });

    expect(checkbox('ops_discord')).toBeChecked();
    expect(checkbox('ops_discord').closest('label')).toHaveTextContent('ops_discord discord');
    expect(checkbox('ops_discord').closest('label')).not.toHaveTextContent(
      strings.heartbeatDestinationNotFoundMark,
    );
    // Edit stays available while Add is blocked.
    expect(button(strings.addHeartbeatAction)).toHaveAttribute('aria-disabled', 'true');
  });

  it('shows a server error in the form alert and keeps the editor open', async () => {
    const updateHeartbeat = vi
      .fn()
      .mockRejectedValue(new ApiError({ message: 'unknown destination', status: 400 }));
    const { user } = await openEditor({ client: { updateHeartbeat } });

    await user.click(checkbox('family_ntfy'));
    await user.click(button(strings.saveHeartbeat));

    expect(await within(editForm()).findByRole('alert')).toHaveTextContent('unknown destination');
    expect(editForm()).toBeInTheDocument();
  });

  it('guards a save in flight: one request, a busy Save, and no way to leave or swap the editor', async () => {
    const saving = deferred<HeartbeatCheck>();
    const updateHeartbeat = vi.fn().mockReturnValue(saving.promise);
    const { user } = await openEditor({
      checks: [EDITED, CHECK_2],
      client: { updateHeartbeat },
    });

    await setDuration({
      user,
      label: strings.heartbeatGraceLabel,
      unitLabel: strings.heartbeatGraceUnitLabel,
      value: '3',
      unit: 'hours',
    });
    await user.click(button(strings.saveHeartbeat));
    await user.click(screen.getByRole('button', { name: strings.savingHeartbeat }));

    expect(updateHeartbeat).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: strings.savingHeartbeat })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
    await user.click(button(strings.closeEditAction));
    await user.click(button(`Edit ${label(CHECK_2)}`));
    await user.click(button(strings.addHeartbeatAction));
    expect(editForm()).toBeInTheDocument();
    expect(screen.getAllByRole('form')).toHaveLength(1);

    await act(async () => {
      saving.resolve({ ...EDITED, graceSeconds: 10_800 });
      await saving.promise;
    });
    await waitFor(() => expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull());
    await waitFor(() => expect(button(`Edit ${label(EDITED)}`)).toHaveFocus());
  });

  it('a save that lands after the editor was swapped does not close the new editor', async () => {
    // The row's editor is closed by a delete of that row; a late save for it must
    // not close or refocus an editor opened since.
    const saving = deferred<HeartbeatCheck>();
    const updateHeartbeat = vi.fn().mockReturnValue(saving.promise);
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([EDITED, CHECK_2])
      .mockResolvedValue([CHECK_2]);
    const { user } = await openEditor({
      client: {
        updateHeartbeat,
        listHeartbeats,
        deleteHeartbeat: vi.fn().mockResolvedValue(undefined),
      },
    });

    await user.click(checkbox('family_ntfy'));
    await user.click(button(strings.saveHeartbeat));
    await user.click(button(`Delete ${label(EDITED)}`));
    await user.click(button(`Confirm delete ${label(EDITED)}`));
    await waitFor(() => expect(document.querySelector('tr[data-check-id="1"]')).toBeNull());
    await user.click(button(`Edit ${label(CHECK_2)}`));
    expect(screen.getByRole('form', { name: `Edit ${label(CHECK_2)}` })).toBeInTheDocument();

    await act(async () => {
      saving.resolve({ ...EDITED, destinations: [] });
      await saving.promise;
    });

    expect(screen.getByRole('form', { name: `Edit ${label(CHECK_2)}` })).toBeInTheDocument();
    expect(resultLine()).not.toHaveTextContent(`Saved ${label(EDITED)}`);
  });

  it('keeps the typed values and the original baseline across a quiet reload of the list', async () => {
    const reloaded = { ...EDITED, periodSeconds: 172_800 };
    const updateHeartbeat = vi.fn().mockResolvedValue({ ...EDITED, graceSeconds: 10_800 });
    const pauseHeartbeat = vi.fn().mockResolvedValue({ ...CHECK_2, paused: true });
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([EDITED, CHECK_2])
      .mockResolvedValue([reloaded, { ...CHECK_2, paused: true }]);
    const { user } = await openEditor({
      checks: [EDITED, CHECK_2],
      client: { updateHeartbeat, listHeartbeats, pauseHeartbeat },
    });

    await setDuration({
      user,
      label: strings.heartbeatGraceLabel,
      unitLabel: strings.heartbeatGraceUnitLabel,
      value: '3',
      unit: 'hours',
    });
    // Pausing another row reloads the list quietly; the check being edited now
    // reports a different period.
    await user.click(button(`Pause ${label(CHECK_2)}`));
    await user.click(button(`Confirm pause ${label(CHECK_2)}`));
    await waitFor(() => expect(listHeartbeats).toHaveBeenCalledTimes(2));
    await screen.findByRole('button', { name: `Resume ${label(CHECK_2)}` });

    expect(screen.getByLabelText(strings.heartbeatGraceLabel)).toHaveValue(3);
    expect(screen.getByLabelText(strings.heartbeatPeriodLabel)).toHaveValue(24);
    await user.click(button(strings.saveHeartbeat));

    await waitFor(() => expect(updateHeartbeat).toHaveBeenCalledTimes(1));
    const body = updateHeartbeat.mock.calls[0][0] as Record<string, unknown>;
    expect(body.graceSeconds).toBe(10_800);
    expect(body.periodSeconds).toBeUndefined();
  });

  it('a loud Refresh closes the open editor instead of re-opening it on a new baseline', async () => {
    const { user } = await openEditor();
    expect(editForm()).toBeInTheDocument();

    await user.click(button(strings.heartbeatsRefresh));

    await waitFor(() => expect(button(`Edit ${label(EDITED)}`)).toBeInTheDocument());
    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();
    expect(button(`Edit ${label(EDITED)}`)).toHaveAttribute('aria-expanded', 'false');
  });

  it('opening edit on one row closes the add card and any other open editor', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1, CHECK_2]) }),
      FULL_DESTINATIONS,
    );

    await user.click(button(strings.addHeartbeatAction));
    expect(addForm()).toBeInTheDocument();
    await user.click(await screen.findByRole('button', { name: `Edit ${label(CHECK_1)}` }));
    expect(screen.queryByRole('form', { name: strings.addHeartbeatHeading })).toBeNull();
    expect(screen.getByRole('form', { name: `Edit ${label(CHECK_1)}` })).toBeInTheDocument();

    await user.click(button(`Edit ${label(CHECK_2)}`));
    expect(screen.queryByRole('form', { name: `Edit ${label(CHECK_1)}` })).toBeNull();
    expect(screen.getByRole('form', { name: `Edit ${label(CHECK_2)}` })).toBeInTheDocument();
    expect(screen.getAllByRole('form')).toHaveLength(1);
  });

  it('opening the add card closes an open editor', async () => {
    const { user } = await openEditor();
    expect(editForm()).toBeInTheDocument();

    await user.click(button(strings.addHeartbeatAction));

    expect(screen.queryByRole('form', { name: /^Edit / })).toBeNull();
    expect(addForm()).toBeInTheDocument();
    expect(screen.getAllByRole('form')).toHaveLength(1);
  });
});

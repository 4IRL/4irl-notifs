import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type {
  AttemptSummary,
  DeliveryApiClient,
  DeliverySummary,
  HeldFailure,
  HeldFailuresList,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { useTimeZone } from '../test-timezone';
import { setCompactViewport } from '../test-viewport';
import { DeliveryLog } from './DeliveryLog';

afterEach(() => {
  cleanup();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function buildDelivery(overrides: Partial<DeliverySummary> & { id: number }): DeliverySummary {
  return {
    messageId: '01JA9X0000000000000007Q',
    appId: 'chores4irl',
    channel: 'digest',
    destination: 'chores_hook',
    destinationType: 'webhook',
    state: 'dead',
    deadReason: 'budget_exhausted',
    attemptCount: 8,
    lastStatusCode: 503,
    nextAttemptAt: null,
    deliveredAt: null,
    createdAt: '2026-10-17T07:00:00Z',
    updatedAt: '2026-10-17T08:12:44Z',
    targetKind: 'destination',
    personHash: null,
    ...overrides,
  };
}

function buildAttempt(overrides: Partial<AttemptSummary> & { attemptNumber: number }) {
  return {
    outcome: 'retryable',
    statusCode: 503,
    latencyMs: 1204,
    error: null,
    responseSnippet: null,
    startedAt: '2026-10-17T07:40:02Z',
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
    deleteHeartbeat: vi.fn(),
    pauseHeartbeat: vi.fn(),
    resumeHeartbeat: vi.fn(),
    listIdentityRequests: vi.fn().mockResolvedValue([]),
    approveIdentityRequest: vi.fn(),
    rejectIdentityRequest: vi.fn(),
    listIdentityBindings: vi.fn().mockResolvedValue([]),
    listRegistrars: vi.fn().mockResolvedValue([]),
    revokeIdentityKey: vi.fn(),
    unbindApp: vi.fn(),
    approveRegistrarRequest: vi.fn(),
    deleteRegistrar: vi.fn(),
    acknowledgeBinding: vi.fn(),
    acknowledgeAllBindings: vi.fn(),
    listPersonalDestinations: vi.fn().mockResolvedValue({ personalDestinations: [], total: 0 }),
    setPersonalDestinationEnabled: vi.fn(),
    deletePersonalDestination: vi.fn(),
    getHeldMessages: vi
      .fn()
      .mockResolvedValue({ apps: [], unassignedChecks: [], unassignedTotal: 0 }),
    listHeldFailures: vi.fn().mockResolvedValue({ items: [], total: 0 }),
    ...overrides,
  };
}

const TWO_DEAD = [
  buildDelivery({ id: 412 }),
  buildDelivery({ id: 409, destination: 'ops_discord' }),
];

describe('DeliveryLog', () => {
  it('loads the dead deliveries by default and renders the columns', async () => {
    const client = buildClient({ listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD) });
    render(<DeliveryLog client={client} />);

    expect(screen.getByText(strings.deliveryLogLoading)).toBeInTheDocument();
    expect(await screen.findByText('ops_discord')).toBeInTheDocument();
    expect(client.listDeliveries).toHaveBeenCalledWith({ state: 'dead' });
    expect(screen.getByRole('button', { name: strings.deliveryLogFilterDead })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    expect(screen.getAllByText('budget exhausted')).toHaveLength(2);
    const updated = screen.getAllByText('2026-10-17 08:12 UTC');
    expect(updated).toHaveLength(2);
    expect(updated[0].tagName).toBe('TIME');
    expect(updated[0]).toHaveAttribute('datetime', '2026-10-17T08:12:44.000Z');
    expect(updated[0]).toHaveAttribute('title', '2026-10-17T08:12:44.000Z');
    expect(screen.getAllByText('503').length).toBeGreaterThan(0);
  });

  it('requests the selected state and shows a per-filter empty text', async () => {
    const user = userEvent.setup();
    const client = buildClient({ listDeliveries: vi.fn().mockResolvedValue([]) });
    render(<DeliveryLog client={client} />);

    expect(await screen.findByText('No dead deliveries.')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterDelivered }));

    expect(await screen.findByText('No delivered deliveries.')).toBeInTheDocument();
    expect(client.listDeliveries).toHaveBeenLastCalledWith({ state: 'delivered' });
    expect(screen.getByText(/Showing the newest 50 delivered deliveries/)).toBeInTheDocument();
  });

  it('shows a load failure as an alert, distinct from empty and loading', async () => {
    const client = buildClient({
      listDeliveries: vi
        .fn()
        .mockRejectedValue(new ApiError({ message: 'upstream down', status: 502 })),
    });
    render(<DeliveryLog client={client} />);

    expect(await screen.findByRole('alert')).toHaveTextContent('upstream down');
    expect(screen.queryByText('No dead deliveries.')).not.toBeInTheDocument();
  });

  it('shows the loading state on a filter change and discards a superseded response', async () => {
    const user = userEvent.setup();
    const slowDead = deferred<DeliverySummary[]>();
    const listDeliveries = vi
      .fn()
      .mockReturnValueOnce(slowDead.promise)
      .mockResolvedValueOnce([
        buildDelivery({ id: 7, state: 'pending', deadReason: null, destination: 'family_ntfy' }),
      ]);
    render(<DeliveryLog client={buildClient({ listDeliveries })} />);

    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterPending }));
    expect(await screen.findByText('family_ntfy')).toBeInTheDocument();

    slowDead.resolve(TWO_DEAD);
    await waitFor(() => expect(screen.queryByText('ops_discord')).not.toBeInTheDocument());
    expect(screen.getByText('family_ntfy')).toBeInTheDocument();
  });

  it('shows the loading state while Refresh is in flight', async () => {
    const user = userEvent.setup();
    const second = deferred<DeliverySummary[]>();
    const listDeliveries = vi
      .fn()
      .mockResolvedValueOnce(TWO_DEAD)
      .mockReturnValueOnce(second.promise);
    render(<DeliveryLog client={buildClient({ listDeliveries })} />);

    await screen.findByText('ops_discord');
    await user.click(screen.getByRole('button', { name: strings.deliveryLogRefresh }));
    expect(screen.getByText(strings.deliveryLogLoading)).toBeInTheDocument();
    expect(screen.queryByText('ops_discord')).not.toBeInTheDocument();

    second.resolve([]);
    expect(await screen.findByText('No dead deliveries.')).toBeInTheDocument();
  });

  it('expands attempts, renders a snippet as literal text, and collapses on Refresh', async () => {
    const user = userEvent.setup();
    const snippet = '<script>alert(1)</script>';
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
      listAttempts: vi
        .fn()
        .mockResolvedValue([
          buildAttempt({ attemptNumber: 1, responseSnippet: snippet }),
          buildAttempt({ attemptNumber: 2, statusCode: null, error: 'timeout awaiting headers' }),
        ]),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.showAttempts({ id: 412 }) }),
    );
    const attempts = await screen.findByRole('table', {
      name: strings.attemptsTableLabel({ id: 412 }),
    });
    expect(client.listAttempts).toHaveBeenCalledWith({ deliveryId: 412 });
    const pre = within(attempts).getByText(snippet);
    expect(pre.tagName).toBe('PRE');
    expect(attempts.querySelector('script')).toBeNull();
    expect(within(attempts).getByText('timeout awaiting headers')).toBeInTheDocument();
    expect(within(attempts).getAllByText('1,204 ms')).toHaveLength(2);
    // Attempt times keep their seconds.
    expect(within(attempts).getAllByText('07:40:02')).toHaveLength(2);

    await user.click(screen.getByRole('button', { name: strings.deliveryLogRefresh }));
    await screen.findByRole('button', { name: strings.showAttempts({ id: 412 }) });
    expect(
      screen.queryByRole('table', { name: strings.attemptsTableLabel({ id: 412 }) }),
    ).toBeNull();

    // A collapsed row re-fetches on the next expand.
    await user.click(screen.getByRole('button', { name: strings.showAttempts({ id: 412 }) }));
    await waitFor(() => expect(client.listAttempts).toHaveBeenCalledTimes(2));
  });

  it('shows an attempts load error inside the expanded row', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
      listAttempts: vi
        .fn()
        .mockRejectedValue(new ApiError({ message: 'attempts down', status: 500 })),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.showAttempts({ id: 412 }) }),
    );

    expect(await screen.findByText('attempts down')).toBeInTheDocument();
  });

  it('discards a superseded attempts response', async () => {
    const user = userEvent.setup();
    const slow = deferred<AttemptSummary[]>();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
      listAttempts: vi
        .fn()
        .mockReturnValueOnce(slow.promise)
        .mockResolvedValueOnce([buildAttempt({ attemptNumber: 1, error: 'second row error' })]),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.showAttempts({ id: 412 }) }),
    );
    await user.click(screen.getByRole('button', { name: strings.showAttempts({ id: 409 }) }));
    expect(await screen.findByText('second row error')).toBeInTheDocument();

    slow.resolve([buildAttempt({ attemptNumber: 1, error: 'stale first row error' })]);
    await waitFor(() => expect(client.listAttempts).toHaveBeenCalledTimes(2));
    expect(screen.queryByText('stale first row error')).not.toBeInTheDocument();
    expect(screen.getByText('second row error')).toBeInTheDocument();
  });

  it('offers no Replay on a destination_deleted row and shows the hint instead', async () => {
    const client = buildClient({
      listDeliveries: vi
        .fn()
        .mockResolvedValue([buildDelivery({ id: 5, deadReason: 'destination_deleted' })]),
    });
    render(<DeliveryLog client={client} />);

    expect(await screen.findByText(strings.destinationWasDeleted)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: strings.replayDelivery({ id: 5 }) })).toBeNull();
  });

  it('marks a personal delivery with a Personal pill, endpoint number, type and person hash', async () => {
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue([
        buildDelivery({
          id: 21,
          destination: '@personal_12',
          targetKind: 'personal',
          personHash: '76gzqgp4byjl6dje',
          deadReason: 'opted_out',
        }),
        buildDelivery({ id: 22 }),
      ]),
    });
    render(<DeliveryLog client={client} />);

    expect(await screen.findByText(strings.deliveryPersonalPill)).toBeInTheDocument();
    expect(screen.getByText('#12')).toBeInTheDocument();
    expect(screen.getByText('Webhook · 76gzqgp4byjl6dje')).toBeInTheDocument();
    expect(screen.queryByText('@personal_12')).toBeNull();
    // The operator row is unchanged and carries no marker.
    expect(screen.getByText('chores_hook')).toBeInTheDocument();
    expect(screen.getAllByText(strings.deliveryPersonalPill)).toHaveLength(1);
  });

  it('shows opted out with no Replay and the person opted out hint', async () => {
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue([
        buildDelivery({
          id: 30,
          destination: '@personal_4',
          targetKind: 'personal',
          personHash: 'k3m2xq7wd4r5nb2a',
          deadReason: 'opted_out',
        }),
      ]),
    });
    render(<DeliveryLog client={client} />);

    expect(await screen.findByText('opted out')).toBeInTheDocument();
    expect(screen.getByText(strings.personOptedOut)).toBeInTheDocument();
    expect(screen.queryByText(strings.destinationWasDeleted)).toBeNull();
    expect(screen.queryByRole('button', { name: strings.replayDelivery({ id: 30 }) })).toBeNull();
  });

  it('keeps Replay on a destination_disabled row and shows no opted-out hint', async () => {
    const client = buildClient({
      listDeliveries: vi
        .fn()
        .mockResolvedValue([buildDelivery({ id: 31, deadReason: 'destination_disabled' })]),
    });
    render(<DeliveryLog client={client} />);

    expect(
      await screen.findByRole('button', { name: strings.replayDelivery({ id: 31 }) }),
    ).toBeInTheDocument();
    expect(screen.queryByText(strings.personOptedOut)).toBeNull();
    expect(screen.queryByText(strings.destinationWasDeleted)).toBeNull();
  });

  it('offers no Replay on non-dead rows', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi
        .fn()
        .mockResolvedValue([buildDelivery({ id: 8, state: 'pending', deadReason: null })]),
    });
    render(<DeliveryLog client={client} />);

    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterPending }));
    await screen.findByRole('button', { name: strings.showAttempts({ id: 8 }) });
    expect(screen.queryByRole('button', { name: strings.replayDelivery({ id: 8 }) })).toBeNull();
  });

  it('gives each open replay confirm a per-row dismiss name', async () => {
    const user = userEvent.setup();
    const client = buildClient({ listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD) });
    render(<DeliveryLog client={client} />);

    const subjects = [strings.replayDelivery({ id: 412 }), strings.replayDelivery({ id: 409 })];
    for (const subject of subjects) {
      await user.click(await screen.findByRole('button', { name: subject }));
    }

    for (const subject of subjects) {
      const dismiss = screen.getByRole('button', {
        name: strings.cancelActionNamed({ subject }),
      });
      expect(dismiss).toHaveTextContent(strings.cancelAction);
    }
    expect(screen.queryByRole('button', { name: strings.cancelAction })).toBeNull();
  });

  it('replays: removes the row, announces above the table, focuses the next row', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValueOnce(TWO_DEAD).mockResolvedValue([TWO_DEAD[1]]),
      replayDelivery: vi.fn().mockResolvedValue(
        buildDelivery({
          id: 412,
          state: 'pending',
          deadReason: null,
          nextAttemptAt: '2026-10-17T08:30:15Z',
        }),
      ),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.replayDelivery({ id: 412 }) }),
    );
    const dialog = screen.getByRole('dialog', { name: strings.replayDeliveryDialogTitle });
    expect(within(dialog).getByText('412')).toBeInTheDocument();
    expect(within(dialog).getByText(strings.replayDeliveryNotice)).toBeInTheDocument();
    expect(
      within(dialog).getByRole('button', {
        name: strings.cancelActionNamed({ subject: strings.replayDelivery({ id: 412 }) }),
      }),
    ).toHaveFocus();
    await user.click(
      within(dialog).getByRole('button', { name: strings.confirmReplayDelivery({ id: 412 }) }),
    );

    expect(client.replayDelivery).toHaveBeenCalledTimes(1);
    expect(client.replayDelivery).toHaveBeenCalledWith({ deliveryId: 412 });
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Replay queued — next attempt 08:30:15',
    );
    expect(screen.queryByRole('button', { name: strings.showAttempts({ id: 412 }) })).toBeNull();
    await waitFor(() =>
      expect(screen.getByRole('button', { name: strings.showAttempts({ id: 409 }) })).toHaveFocus(),
    );
    expect(screen.getByRole('status')).not.toHaveFocus();
    const status = screen.getByRole('status');
    const table = screen.getByRole('table');
    expect(status.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('does not re-insert a replayed row from an earlier in-flight reload', async () => {
    const user = userEvent.setup();
    const staleReload = deferred<DeliverySummary[]>();
    const listDeliveries = vi
      .fn()
      .mockResolvedValueOnce(TWO_DEAD)
      .mockReturnValueOnce(staleReload.promise)
      .mockResolvedValue([TWO_DEAD[1]]);
    const replay = deferred<DeliverySummary>();
    const client = buildClient({
      listDeliveries,
      replayDelivery: vi.fn().mockReturnValue(replay.promise),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.replayDelivery({ id: 412 }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmReplayDelivery({ id: 412 }) }),
    );
    // Starts a reload that stays in flight while the replay completes.
    await user.click(screen.getByRole('button', { name: strings.deliveryLogRefresh }));
    await waitFor(() => expect(listDeliveries).toHaveBeenCalledTimes(2));
    replay.resolve(
      buildDelivery({ id: 412, state: 'pending', deadReason: null, nextAttemptAt: null }),
    );
    await waitFor(() => expect(listDeliveries).toHaveBeenCalledTimes(3));

    staleReload.resolve(TWO_DEAD);
    await staleReload.promise;

    await waitFor(() =>
      expect(screen.queryByRole('button', { name: strings.showAttempts({ id: 412 }) })).toBeNull(),
    );
    expect(screen.getByRole('button', { name: strings.showAttempts({ id: 409 }) })).toBeVisible();
  });

  it('focuses the section heading when the last row is replayed', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue([buildDelivery({ id: 412 })]),
      replayDelivery: vi
        .fn()
        .mockResolvedValue(buildDelivery({ id: 412, state: 'pending', deadReason: null })),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.replayDelivery({ id: 412 }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmReplayDelivery({ id: 412 }) }),
    );

    await waitFor(() =>
      expect(screen.getByRole('heading', { name: strings.deliveryLogHeading })).toHaveFocus(),
    );
    expect(screen.getByRole('status')).toHaveTextContent('Replay queued');
  });

  it('shows Replaying… in flight, ignores a second click, and keeps focus on the trigger', async () => {
    const user = userEvent.setup();
    const pending = deferred<DeliverySummary>();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
      replayDelivery: vi.fn().mockReturnValue(pending.promise),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.replayDelivery({ id: 412 }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmReplayDelivery({ id: 412 }) }),
    );

    const busy = screen.getAllByRole('button', { name: strings.replayingDelivery })[0];
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await waitFor(() => expect(busy).toHaveFocus());
    await user.click(busy);
    expect(client.replayDelivery).toHaveBeenCalledTimes(1);

    pending.resolve(buildDelivery({ id: 412, state: 'pending', deadReason: null }));
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: strings.replayingDelivery })).toBeNull(),
    );
  });

  it('shows a 409 inline in the row and keeps the row', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
      replayDelivery: vi
        .fn()
        .mockRejectedValue(new ApiError({ message: 'delivery not dead', status: 409 })),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.replayDelivery({ id: 412 }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmReplayDelivery({ id: 412 }) }),
    );

    expect(await screen.findByRole('alert')).toHaveTextContent('delivery not dead');
    expect(
      screen.getByRole('button', { name: strings.replayDelivery({ id: 412 }) }),
    ).toBeInTheDocument();
  });
});

describe('DeliveryLog times in a non-UTC zone', () => {
  useTimeZone('America/Los_Angeles');

  it('shows the updated time in the viewer zone and attempt times without a zone', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue([buildDelivery({ id: 412 })]),
      listAttempts: vi.fn().mockResolvedValue([buildAttempt({ attemptNumber: 1 })]),
    });
    render(<DeliveryLog client={client} />);

    const updated = await screen.findByText('2026-10-17 01:12 PDT');
    expect(updated.tagName).toBe('TIME');
    expect(updated).toHaveAttribute('datetime', '2026-10-17T08:12:44.000Z');
    expect(updated).toHaveAttribute('title', '2026-10-17T08:12:44.000Z');

    await user.click(screen.getByRole('button', { name: strings.showAttempts({ id: 412 }) }));
    const attempts = await screen.findByRole('table', {
      name: strings.attemptsTableLabel({ id: 412 }),
    });
    const attemptTime = within(attempts).getByText('00:40:02');
    expect(attemptTime.tagName).toBe('TIME');
    expect(attemptTime).toHaveAttribute('title', '2026-10-17T07:40:02.000Z');
  });
});

describe('DeliveryLog filter menu', () => {
  const FUNNEL = { name: strings.deliveryLogFiltersMenu };

  it('roomy: keeps the three aria-pressed buttons and no funnel', async () => {
    render(<DeliveryLog client={buildClient()} />);

    await screen.findByText('No dead deliveries.');
    expect(screen.getAllByRole('button', { pressed: true })).toHaveLength(1);
    expect(screen.getByRole('button', { name: strings.deliveryLogFilterPending })).toHaveAttribute(
      'aria-pressed',
      'false',
    );
    expect(screen.queryByRole('button', FUNNEL)).toBeNull();
  });

  it('compact: shows no filter buttons until the funnel is activated', async () => {
    const user = userEvent.setup();
    setCompactViewport(true);
    render(<DeliveryLog client={buildClient()} />);

    await screen.findByText('No dead deliveries.');
    expect(screen.queryByRole('button', { name: strings.deliveryLogFilterPending })).toBeNull();
    const funnel = screen.getByRole('button', FUNNEL);
    expect(funnel).toHaveAttribute('aria-haspopup', 'menu');
    expect(funnel).toHaveAttribute('aria-expanded', 'false');
    expect(funnel.querySelector('svg')).toHaveAttribute('aria-hidden', 'true');
    expect(screen.queryByRole('menu')).toBeNull();

    await user.click(funnel);

    expect(funnel).toHaveAttribute('aria-expanded', 'true');
    const items = within(screen.getByRole('menu')).getAllByRole('menuitemradio');
    expect(items.map((item) => item.textContent)).toEqual([
      strings.deliveryLogFilterDead,
      strings.deliveryLogFilterPending,
      strings.deliveryLogFilterDelivered,
      strings.deliveryLogFilterReleaseFailed,
    ]);
    expect(items.map((item) => item.getAttribute('aria-checked'))).toEqual([
      'true',
      'false',
      'false',
      'false',
    ]);
  });

  it('compact: selecting reloads with that state, closes and refocuses the funnel', async () => {
    const user = userEvent.setup();
    setCompactViewport(true);
    const client = buildClient();
    render(<DeliveryLog client={client} />);

    await screen.findByText('No dead deliveries.');
    await user.click(screen.getByRole('button', FUNNEL));
    await user.click(screen.getByRole('menuitemradio', { name: strings.deliveryLogFilterPending }));

    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.getByRole('button', FUNNEL)).toHaveFocus();
    expect(await screen.findByText('No pending deliveries.')).toBeInTheDocument();
    expect(client.listDeliveries).toHaveBeenLastCalledWith({ state: 'pending' });
    expect(screen.getByText(strings.deliveryLogHint({ state: 'pending' }))).toBeInTheDocument();

    await user.click(screen.getByRole('button', FUNNEL));
    expect(
      screen.getByRole('menuitemradio', { name: strings.deliveryLogFilterPending }),
    ).toHaveAttribute('aria-checked', 'true');
  });

  it('compact: the keyboard opens, moves, selects and escapes', async () => {
    const user = userEvent.setup();
    setCompactViewport(true);
    const client = buildClient();
    render(<DeliveryLog client={client} />);

    await screen.findByText('No dead deliveries.');
    const funnel = screen.getByRole('button', FUNNEL);
    funnel.focus();
    await user.keyboard('{Enter}');
    expect(
      screen.getByRole('menuitemradio', { name: strings.deliveryLogFilterDead }),
    ).toHaveFocus();

    await user.keyboard('{ArrowDown}');
    expect(
      screen.getByRole('menuitemradio', { name: strings.deliveryLogFilterPending }),
    ).toHaveFocus();

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(funnel).toHaveFocus();

    // The first ArrowDown opens the menu on the current choice (Dead); two more reach Delivered.
    await user.keyboard('{ArrowDown}');
    await user.keyboard('{ArrowDown}{ArrowDown}{Enter}');
    expect(await screen.findByText('No delivered deliveries.')).toBeInTheDocument();
    expect(client.listDeliveries).toHaveBeenLastCalledWith({ state: 'delivered' });
    expect(funnel).toHaveFocus();
  });

  it('compact: flags a non-default filter on the funnel and describes it with the hint', async () => {
    const user = userEvent.setup();
    setCompactViewport(true);
    render(<DeliveryLog client={buildClient()} />);

    await screen.findByText('No dead deliveries.');
    const funnel = screen.getByRole('button', FUNNEL);
    expect(funnel).not.toHaveAttribute('data-filter-active');
    expect(funnel).not.toHaveAttribute('aria-describedby');

    await user.click(funnel);
    await user.click(
      screen.getByRole('menuitemradio', { name: strings.deliveryLogFilterDelivered }),
    );

    await screen.findByText('No delivered deliveries.');
    expect(funnel).toHaveAttribute('data-filter-active', 'true');
    const hint = screen.getByText(strings.deliveryLogHint({ state: 'delivered' }));
    expect(hint.id).not.toBe('');
    expect(funnel).toHaveAttribute('aria-describedby', hint.id);

    await user.click(funnel);
    await user.click(screen.getByRole('menuitemradio', { name: strings.deliveryLogFilterDead }));
    await screen.findByText('No dead deliveries.');
    expect(funnel).not.toHaveAttribute('data-filter-active');
    expect(funnel).not.toHaveAttribute('aria-describedby');
  });
});

function buildFailure(overrides: Partial<HeldFailure> = {}): HeldFailure {
  return {
    messageId: '01JA9X0000000000000007Q',
    appId: 'urls4irl',
    channel: 'alerts',
    reason: 'release_invalid',
    failedAt: '2026-11-09T08:12:00Z',
    ...overrides,
  };
}

function failuresList(items: HeldFailure[], total = items.length): HeldFailuresList {
  return { items, total };
}

const RELEASE_FAILED = { name: strings.deliveryLogFilterReleaseFailed };

async function openReleaseFailed(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', RELEASE_FAILED));
}

async function confirmReplay({
  user,
  id,
}: {
  user: ReturnType<typeof userEvent.setup>;
  id: number;
}) {
  await user.click(await screen.findByRole('button', { name: strings.replayDelivery({ id }) }));
  const dialog = screen.getByRole('dialog', { name: strings.replayDeliveryDialogTitle });
  await user.click(
    within(dialog).getByRole('button', { name: strings.confirmReplayDelivery({ id }) }),
  );
}

describe('DeliveryLog Release failed filter', () => {
  it('roomy: adds the chip last, labels the group, and reads failures not deliveries', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listHeldFailures: vi.fn().mockResolvedValue(failuresList([buildFailure()])),
    });
    render(<DeliveryLog client={client} />);

    await screen.findByText('No dead deliveries.');
    const group = screen.getByRole('group', { name: 'Filter deliveries' });
    const chips = within(group).getAllByRole('button');
    expect(chips[chips.length - 1]).toHaveTextContent(strings.deliveryLogFilterReleaseFailed);
    expect(client.listDeliveries).toHaveBeenCalledTimes(1);

    await openReleaseFailed(user);

    expect(screen.getByRole('button', RELEASE_FAILED)).toHaveAttribute('aria-pressed', 'true');
    expect(
      await screen.findByRole('table', { name: strings.releaseFailuresTableLabel }),
    ).toBeInTheDocument();
    expect(client.listHeldFailures).toHaveBeenCalledTimes(1);
    expect(client.listDeliveries).toHaveBeenCalledTimes(1);
  });

  it('compact: the menu offers it and selecting it reads failures', async () => {
    const user = userEvent.setup();
    setCompactViewport(true);
    const client = buildClient({
      listHeldFailures: vi.fn().mockResolvedValue(failuresList([buildFailure()])),
    });
    render(<DeliveryLog client={client} />);

    await screen.findByText('No dead deliveries.');
    await user.click(screen.getByRole('button', { name: strings.deliveryLogFiltersMenu }));
    await user.click(screen.getByRole('menuitemradio', RELEASE_FAILED));

    expect(
      await screen.findByRole('table', { name: strings.releaseFailuresTableLabel }),
    ).toBeInTheDocument();
    expect(client.listHeldFailures).toHaveBeenCalledTimes(1);
    expect(client.listDeliveries).toHaveBeenCalledTimes(1);
  });

  it('renders the reduced table with reason copy, no Replay and no attempts expander', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listHeldFailures: vi.fn().mockResolvedValue(
        failuresList([
          buildFailure({ appId: 'urls4irl', channel: 'alerts' }),
          buildFailure({
            messageId: '01JA9X0000000000000008R',
            appId: 'mc_backups',
            channel: 'backup',
            reason: 'mystery_reason',
          }),
        ]),
      ),
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
    });
    render(<DeliveryLog client={client} />);

    await screen.findByText('ops_discord');
    await openReleaseFailed(user);

    const table = await screen.findByRole('table', { name: strings.releaseFailuresTableLabel });
    expect(table.closest('.delivery-log__scroll')).not.toBeNull();
    expect(
      within(table)
        .getAllByRole('columnheader')
        .map((header) => header.textContent),
    ).toEqual([
      strings.columnLogAppChannel,
      strings.columnReleaseReason,
      strings.columnReleaseFailed,
    ]);
    const [invalidRow, unknownRow] = within(table).getAllByRole('row').slice(1);
    expect(within(invalidRow).getByText(strings.releaseReasonInvalid)).toBeInTheDocument();
    expect(within(invalidRow).getByText('release_invalid')).toBeInTheDocument();
    expect(within(unknownRow).getByText('mystery_reason')).toBeInTheDocument();
    expect(within(unknownRow).queryByText(strings.releaseReasonInvalid)).toBeNull();
    expect(within(unknownRow).getByText('mc_backups')).toBeInTheDocument();
    expect(within(invalidRow).getByText('2026-11-09 08:12 UTC')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /attempts/i })).toBeNull();
    expect(screen.queryByRole('button', { name: /replay/i })).toBeNull();
    expect(screen.queryByText('ops_discord')).toBeNull();
  });

  it('says the list is truncated when it shows fewer than the total', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listHeldFailures: vi
        .fn()
        .mockResolvedValue(
          failuresList([buildFailure(), buildFailure({ messageId: '01JA9X0000000000000008R' })], 5),
        ),
    });
    render(<DeliveryLog client={client} />);

    await screen.findByText('No dead deliveries.');
    await openReleaseFailed(user);

    expect(
      await screen.findByText('Showing the newest 2 of 5 release failures.'),
    ).toBeInTheDocument();
  });

  it('shows loading, then the empty text pointing at Routes and Destinations', async () => {
    const user = userEvent.setup();
    const pending = deferred<HeldFailuresList>();
    const client = buildClient({ listHeldFailures: vi.fn().mockReturnValue(pending.promise) });
    render(<DeliveryLog client={client} />);

    await screen.findByText('No dead deliveries.');
    await openReleaseFailed(user);
    expect(screen.getByText(strings.releaseFailuresLoading)).toBeInTheDocument();
    expect(screen.queryByText(strings.deliveryLogLoading)).toBeNull();

    await act(async () => {
      pending.resolve(failuresList([]));
      await pending.promise;
    });
    expect(screen.getByText(strings.releaseFailuresEmpty)).toBeInTheDocument();
    expect(screen.queryByText(strings.releaseFailuresLoading)).toBeNull();
    expect(screen.queryByText(strings.deliveryLogEmpty({ state: 'release_failed' }))).toBeNull();
  });

  it.each([
    ['a 404', new ApiError({ message: 'not found', status: 404 })],
    ['a network failure', new TypeError('Failed to fetch')],
  ])(
    'shows a neutral unavailable text on %s, never empty or an alert',
    async (_label, rejection) => {
      const user = userEvent.setup();
      const client = buildClient({ listHeldFailures: vi.fn().mockRejectedValue(rejection) });
      render(<DeliveryLog client={client} />);

      await screen.findByText('No dead deliveries.');
      await openReleaseFailed(user);

      const text = await screen.findByText(strings.releaseFailuresUnavailable);
      expect(text.tagName).toBe('P');
      expect(screen.queryByRole('alert')).toBeNull();
      expect(screen.queryByText(strings.releaseFailuresEmpty)).toBeNull();
      expect(strings.releaseFailuresUnavailable).not.toBe(strings.releaseFailuresEmpty);
    },
  );

  it('keeps the hint paragraph on releaseFailuresHint in every state, and describes the menu', async () => {
    const user = userEvent.setup();
    setCompactViewport(true);
    const first = deferred<HeldFailuresList>();
    const listHeldFailures = vi
      .fn()
      .mockReturnValueOnce(first.promise)
      .mockRejectedValueOnce(new ApiError({ message: 'x', status: 404 }))
      .mockResolvedValueOnce(failuresList([]))
      .mockResolvedValue(failuresList([buildFailure()]));
    render(<DeliveryLog client={buildClient({ listHeldFailures })} />);

    await screen.findByText('No dead deliveries.');
    const funnel = screen.getByRole('button', { name: strings.deliveryLogFiltersMenu });
    const refresh = () => screen.getByRole('button', { name: strings.deliveryLogRefresh });
    function expectHint() {
      const hint = screen.getByText(strings.releaseFailuresHint);
      expect(funnel).toHaveAttribute('aria-describedby', hint.id);
      expect(screen.queryByText(/release_failed/)).toBeNull();
      expect(screen.queryByText(strings.deliveryLogReplayHint)).toBeNull();
    }

    await user.click(funnel);
    await user.click(screen.getByRole('menuitemradio', RELEASE_FAILED));
    expect(screen.getByText(strings.releaseFailuresLoading)).toBeInTheDocument();
    expectHint();

    await act(async () => {
      first.resolve(failuresList([]));
      await first.promise;
    });
    expect(screen.getByText(strings.releaseFailuresEmpty)).toBeInTheDocument();
    expectHint();

    await user.click(refresh());
    await screen.findByText(strings.releaseFailuresUnavailable);
    expectHint();

    await user.click(refresh());
    await screen.findByText(strings.releaseFailuresEmpty);
    expectHint();

    await user.click(refresh());
    await screen.findByRole('table', { name: strings.releaseFailuresTableLabel });
    expectHint();
  });

  it('Refresh log re-fetches the failures and clears the previous result while loading', async () => {
    const user = userEvent.setup();
    const reload = deferred<HeldFailuresList>();
    const listHeldFailures = vi
      .fn()
      .mockResolvedValueOnce(failuresList([buildFailure()]))
      .mockReturnValueOnce(reload.promise);
    const client = buildClient({ listHeldFailures });
    render(<DeliveryLog client={client} />);

    await screen.findByText('No dead deliveries.');
    await openReleaseFailed(user);
    await screen.findByRole('table', { name: strings.releaseFailuresTableLabel });

    await user.click(screen.getByRole('button', { name: strings.deliveryLogRefresh }));

    expect(listHeldFailures).toHaveBeenCalledTimes(2);
    expect(client.listDeliveries).toHaveBeenCalledTimes(1);
    expect(screen.getByText(strings.releaseFailuresLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.getByRole('button', { name: strings.deliveryLogRefresh })).toHaveAttribute(
      'aria-busy',
      'true',
    );

    await act(async () => {
      reload.resolve(failuresList([]));
      await reload.promise;
    });
    expect(screen.getByText(strings.releaseFailuresEmpty)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: strings.deliveryLogRefresh })).not.toHaveAttribute(
      'aria-busy',
    );
  });

  it('shows only loading on re-entry after an earlier unavailable or ready result', async () => {
    const user = userEvent.setup();
    const third = deferred<HeldFailuresList>();
    const listHeldFailures = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ message: 'x', status: 503 }))
      .mockReturnValueOnce(third.promise);
    render(<DeliveryLog client={buildClient({ listHeldFailures })} />);

    await screen.findByText('No dead deliveries.');
    await openReleaseFailed(user);
    await screen.findByText(strings.releaseFailuresUnavailable);

    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterPending }));
    await screen.findByText('No pending deliveries.');
    await openReleaseFailed(user);

    expect(screen.getByText(strings.releaseFailuresLoading)).toBeInTheDocument();
    expect(screen.queryByText(strings.releaseFailuresUnavailable)).toBeNull();

    await act(async () => {
      third.resolve(failuresList([buildFailure()]));
      await third.promise;
    });
    expect(
      screen.getByRole('table', { name: strings.releaseFailuresTableLabel }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterDead }));
    await screen.findByText('No dead deliveries.');
  });

  it('resets the expanded attempts and the replay result when selected', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
      listAttempts: vi.fn().mockResolvedValue([buildAttempt({ attemptNumber: 1 })]),
    });
    render(<DeliveryLog client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.showAttempts({ id: 412 }) }),
    );
    expect(
      screen.getByRole('button', { name: strings.hideAttempts({ id: 412 }) }),
    ).toBeInTheDocument();

    await openReleaseFailed(user);
    await screen.findByText(strings.releaseFailuresEmpty);
    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterDead }));

    expect(
      await screen.findByRole('button', { name: strings.showAttempts({ id: 412 }) }),
    ).toHaveAttribute('aria-expanded', 'false');
  });

  it('clears a shown replay result when Release failed is selected', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValueOnce(TWO_DEAD).mockResolvedValue([TWO_DEAD[1]]),
      replayDelivery: vi
        .fn()
        .mockResolvedValue(buildDelivery({ id: 412, state: 'pending', deadReason: null })),
    });
    render(<DeliveryLog client={client} />);

    await confirmReplay({ user, id: 412 });
    expect(await screen.findByRole('status')).toHaveTextContent('Replay queued');

    await openReleaseFailed(user);
    await screen.findByText(strings.releaseFailuresEmpty);
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('ignores a slow listDeliveries that resolves after switching to Release failed', async () => {
    const user = userEvent.setup();
    const slowDeliveries = deferred<DeliverySummary[]>();
    const pendingFailures = deferred<HeldFailuresList>();
    const client = buildClient({
      listDeliveries: vi.fn().mockReturnValue(slowDeliveries.promise),
      listHeldFailures: vi.fn().mockReturnValue(pendingFailures.promise),
    });
    render(<DeliveryLog client={client} />);

    await openReleaseFailed(user);
    expect(screen.getByText(strings.releaseFailuresLoading)).toBeInTheDocument();

    await act(async () => {
      slowDeliveries.resolve(TWO_DEAD);
      await slowDeliveries.promise;
    });

    expect(screen.getByText(strings.releaseFailuresLoading)).toBeInTheDocument();
    expect(screen.queryByText('ops_discord')).toBeNull();
    expect(screen.queryByText(strings.releaseFailuresEmpty)).toBeNull();
    expect(screen.queryByText(strings.releaseFailuresUnavailable)).toBeNull();
    expect(screen.getByRole('button', { name: strings.deliveryLogRefresh })).toHaveAttribute(
      'aria-busy',
      'true',
    );

    await act(async () => {
      pendingFailures.resolve(failuresList([]));
      await pendingFailures.promise;
    });
    expect(screen.getByText(strings.releaseFailuresEmpty)).toBeInTheDocument();
  });

  it('ignores a slow listHeldFailures that resolves after switching away', async () => {
    const user = userEvent.setup();
    const slowFailures = deferred<HeldFailuresList>();
    const client = buildClient({
      listHeldFailures: vi.fn().mockReturnValue(slowFailures.promise),
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
    });
    render(<DeliveryLog client={client} />);

    await screen.findByText('ops_discord');
    await openReleaseFailed(user);
    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterDead }));
    await screen.findByText('ops_discord');

    await act(async () => {
      slowFailures.resolve(failuresList([buildFailure({ appId: 'ghost_app' })]));
      await slowFailures.promise;
    });

    expect(screen.queryByText('ghost_app')).toBeNull();
    expect(screen.queryByRole('table', { name: strings.releaseFailuresTableLabel })).toBeNull();
    expect(screen.getByText('ops_discord')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: strings.deliveryLogRefresh })).not.toHaveAttribute(
      'aria-busy',
    );
  });

  it('never leaves Refresh busy after a superseded listDeliveries', async () => {
    const user = userEvent.setup();
    const never = deferred<DeliverySummary[]>();
    const client = buildClient({
      listDeliveries: vi.fn().mockReturnValueOnce(never.promise).mockResolvedValue(TWO_DEAD),
    });
    render(<DeliveryLog client={client} />);

    await openReleaseFailed(user);
    await screen.findByText(strings.releaseFailuresEmpty);
    expect(screen.getByRole('button', { name: strings.deliveryLogRefresh })).not.toHaveAttribute(
      'aria-busy',
    );

    await user.click(screen.getByRole('button', { name: strings.deliveryLogFilterDead }));
    await screen.findByText('ops_discord');
    expect(screen.getByRole('button', { name: strings.deliveryLogRefresh })).not.toHaveAttribute(
      'aria-busy',
    );

    await act(async () => {
      never.resolve([]);
      await never.promise;
    });
    expect(screen.getByText('ops_discord')).toBeInTheDocument();
  });

  it('a replay finishing on another chip neither reloads release_failed nor shows a result', async () => {
    const user = userEvent.setup();
    const replay = deferred<DeliverySummary>();
    const listDeliveries = vi.fn().mockResolvedValue(TWO_DEAD);
    const client = buildClient({
      listDeliveries,
      replayDelivery: vi.fn().mockReturnValue(replay.promise),
      listHeldFailures: vi.fn().mockResolvedValue(failuresList([buildFailure()])),
    });
    render(<DeliveryLog client={client} />);

    await confirmReplay({ user, id: 412 });
    await openReleaseFailed(user);
    await screen.findByRole('table', { name: strings.releaseFailuresTableLabel });
    const deliveryCalls = listDeliveries.mock.calls.length;

    await act(async () => {
      replay.resolve(buildDelivery({ id: 412, state: 'pending', deadReason: null }));
      await replay.promise;
    });

    expect(listDeliveries).toHaveBeenCalledTimes(deliveryCalls);
    expect(listDeliveries).not.toHaveBeenCalledWith({ state: 'release_failed' });
    expect(screen.queryByText(/Replay queued/)).toBeNull();
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
    expect(
      screen.getByRole('table', { name: strings.releaseFailuresTableLabel }),
    ).toBeInTheDocument();
    expect(client.listHeldFailures).toHaveBeenCalledTimes(1);
  });

  it('a failed replay on another chip shows nothing in the failures view', async () => {
    const user = userEvent.setup();
    const replay = deferred<DeliverySummary>();
    const client = buildClient({
      listDeliveries: vi.fn().mockResolvedValue(TWO_DEAD),
      replayDelivery: vi.fn().mockReturnValue(replay.promise),
      listHeldFailures: vi.fn().mockResolvedValue(failuresList([buildFailure()])),
    });
    render(<DeliveryLog client={client} />);

    await confirmReplay({ user, id: 412 });
    await openReleaseFailed(user);
    await screen.findByRole('table', { name: strings.releaseFailuresTableLabel });

    await act(async () => {
      replay.reject(new ApiError({ message: 'replay boom', status: 500 }));
      await replay.promise.catch(() => undefined);
    });

    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByText('replay boom')).toBeNull();
    expect(
      screen.getByRole('table', { name: strings.releaseFailuresTableLabel }),
    ).toBeInTheDocument();
  });

  it('a replay resolved on the same chip still shows the result and reloads', async () => {
    const user = userEvent.setup();
    const listDeliveries = vi.fn().mockResolvedValueOnce(TWO_DEAD).mockResolvedValue([TWO_DEAD[1]]);
    const client = buildClient({
      listDeliveries,
      replayDelivery: vi
        .fn()
        .mockResolvedValue(buildDelivery({ id: 412, state: 'pending', deadReason: null })),
    });
    render(<DeliveryLog client={client} />);

    await confirmReplay({ user, id: 412 });

    expect(await screen.findByRole('status')).toHaveTextContent('Replay queued');
    await waitFor(() => expect(listDeliveries).toHaveBeenCalledTimes(2));
    expect(listDeliveries).toHaveBeenLastCalledWith({ state: 'dead' });
  });
});

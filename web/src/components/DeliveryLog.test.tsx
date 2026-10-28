import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { AttemptSummary, DeliveryApiClient, DeliverySummary } from '../api/deliveryClient';
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
    revokeIdentityKey: vi.fn(),
    unbindApp: vi.fn(),
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
    ]);
    expect(items.map((item) => item.getAttribute('aria-checked'))).toEqual([
      'true',
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

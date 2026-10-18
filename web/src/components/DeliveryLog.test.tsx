import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { AttemptSummary, DeliveryApiClient, DeliverySummary } from '../api/deliveryClient';
import { strings } from '../strings';
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
    expect(screen.getAllByText('2026-10-17 08:12')).toHaveLength(2);
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
    expect(
      screen.getByRole('button', { name: strings.confirmReplayDelivery({ id: 412 }) }),
    ).toHaveFocus();
    await user.click(
      screen.getByRole('button', { name: strings.confirmReplayDelivery({ id: 412 }) }),
    );

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

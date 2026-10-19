import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type {
  CancelScheduledMessageResult,
  DeliveryApiClient,
  ScheduledMessageSummary,
  ScheduledMessagesList,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { formatRelativeFuture } from './formatRelative';
import { PendingSends } from './PendingSends';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
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

function buildScheduled(
  overrides: Partial<ScheduledMessageSummary> & { messageId: string },
): ScheduledMessageSummary {
  return {
    appId: 'urls4irl',
    channel: 'digest',
    idempotencyKey: `key-${overrides.messageId}`,
    deliverAt: '2099-01-01T08:00:00Z',
    createdAt: '2026-10-17T21:30:00Z',
    recipient: false,
    destinations: [{ name: 'ops_discord', type: 'discord' }],
    ...overrides,
  };
}

function listOf(items: ScheduledMessageSummary[], total = items.length): ScheduledMessagesList {
  return { items, total };
}

function cancelled(id: string): CancelScheduledMessageResult {
  return { messageId: id, cancelled: true, status: 'cancelled' };
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
    listScheduledMessages: vi.fn().mockResolvedValue(listOf([])),
    cancelScheduledMessage: vi.fn(),
    ...overrides,
  };
}

const ROW_A = buildScheduled({ messageId: 'a' });
const ROW_B = buildScheduled({ messageId: 'b' });
const ROW_C = buildScheduled({ messageId: 'c' });

function trigger(key: string): HTMLElement {
  return screen.getByRole('button', { name: strings.cancelSendNamed({ key }) });
}

function confirmButton(key: string): HTMLElement {
  return screen.getByRole('button', { name: strings.confirmCancelSendNamed({ key }) });
}

function heading(): HTMLElement {
  return screen.getByRole('heading', { name: strings.pendingSendsHeading });
}

function status(): HTMLElement {
  return screen.getByRole('status');
}

async function cancelRow({ user, key }: { user: ReturnType<typeof userEvent.setup>; key: string }) {
  await user.click(await screen.findByRole('button', { name: strings.cancelSendNamed({ key }) }));
  await user.click(confirmButton(key));
}

describe('formatRelativeFuture', () => {
  const now = Date.parse('2026-10-18T12:00:00Z');

  function relative(offsetMs: number): string {
    return formatRelativeFuture({ deliverAt: new Date(now + offsetMs).toISOString(), now });
  }

  const SECOND = 1000;
  const MINUTE = 60 * SECOND;
  const HOUR = 60 * MINUTE;
  const DAY = 24 * HOUR;

  it.each([
    ['at now', 0, 'due now'],
    ['in the past', -5 * SECOND, 'due now'],
    ['1s ahead', SECOND, 'in <1 min'],
    ['59s ahead', 59 * SECOND, 'in <1 min'],
    ['exactly 1 min', MINUTE, 'in 1 min'],
    ['89s ahead (minutes round down)', 89 * SECOND, 'in 1 min'],
    ['59.5 min ahead (still under an hour)', 59.5 * MINUTE, 'in 59 min'],
    ['exactly 1 h (minutes omitted)', HOUR, 'in 1 h'],
    ['1 h 1 min', HOUR + MINUTE, 'in 1 h 1 min'],
    ['4 h 55 min', 4 * HOUR + 55 * MINUTE, 'in 4 h 55 min'],
    ['23 h 59 min', 23 * HOUR + 59 * MINUTE, 'in 23 h 59 min'],
    ['exactly 24 h', DAY, 'in 1 day'],
    ['47 h 59 min (singular day)', 47 * HOUR + 59 * MINUTE, 'in 1 day'],
    ['exactly 48 h', 2 * DAY, 'in 2 days'],
    ['14 days', 14 * DAY, 'in 14 days'],
  ])('%s', (_name, offset, expected) => {
    expect(relative(offset)).toBe(expected);
  });

  it('reads an unparseable deliver_at as due now', () => {
    expect(formatRelativeFuture({ deliverAt: 'not-a-date', now })).toBe('due now');
  });
});

describe('PendingSends', () => {
  it('loads on mount and renders the loading state, columns, pills and time elements', async () => {
    const list = deferred<ScheduledMessagesList>();
    const client = buildClient({ listScheduledMessages: vi.fn().mockReturnValue(list.promise) });
    render(<PendingSends client={client} />);

    expect(screen.getByText(strings.pendingSendsLoading)).toBeInTheDocument();
    expect(client.listScheduledMessages).toHaveBeenCalledWith({ limit: 50 });

    list.resolve(
      listOf([
        buildScheduled({
          messageId: 'a',
          appId: 'chores4irl',
          channel: 'reminder',
          idempotencyKey: 'chore-1',
          deliverAt: '2099-10-18T19:00:00Z',
          createdAt: '2026-10-17T20:15:44Z',
        }),
        buildScheduled({
          messageId: 'b',
          recipient: true,
          destinations: [],
          deliverAt: '2099-11-18T19:00:00Z',
        }),
      ]),
    );

    expect(await screen.findByText('chore-1')).toBeInTheDocument();
    for (const column of [
      strings.columnPendingState,
      strings.columnLogAppChannel,
      strings.columnPendingDestinations,
      strings.columnPendingDeliverAt,
      strings.columnPendingCreated,
    ]) {
      expect(screen.getByRole('columnheader', { name: column })).toBeInTheDocument();
    }
    expect(screen.getByText('chores4irl')).toBeInTheDocument();
    expect(screen.getByText('reminder')).toBeInTheDocument();
    expect(screen.getByText('ops_discord')).toBeInTheDocument();
    expect(screen.getByText('discord')).toBeInTheDocument();
    expect(screen.getByText('2026-10-17 20:15')).toBeInTheDocument();
    expect(screen.getAllByText(strings.pendingStateScheduled)).toHaveLength(2);
    expect(screen.queryByText(strings.pendingStateDue)).toBeNull();
    // The recipient row lists the personal topic, not a destination.
    expect(screen.getByText(strings.pendingRecipientName)).toBeInTheDocument();
    expect(screen.getByText(strings.pendingRecipientDetail)).toBeInTheDocument();

    const time = screen.getByText('2099-10-18 19:00 UTC');
    expect(time.tagName).toBe('TIME');
    expect(time).toHaveAttribute('datetime', '2099-10-18T19:00:00Z');
    expect(status()).toBeEmptyDOMElement();
  });

  it('shows the empty state', async () => {
    render(<PendingSends client={buildClient()} />);

    expect(await screen.findByText(strings.pendingSendsEmpty)).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('shows a load failure as an alert, distinct from empty', async () => {
    const client = buildClient({
      listScheduledMessages: vi
        .fn()
        .mockRejectedValue(new ApiError({ message: 'upstream down', status: 502 })),
    });
    render(<PendingSends client={client} />);

    expect(await screen.findByRole('alert')).toHaveTextContent('upstream down');
    expect(screen.queryByText(strings.pendingSendsEmpty)).toBeNull();
  });

  it('falls back to the generic load error for a non-ApiError rejection', async () => {
    const client = buildClient({
      listScheduledMessages: vi.fn().mockRejectedValue(new TypeError('network')),
    });
    render(<PendingSends client={client} />);

    expect(await screen.findByRole('alert')).toHaveTextContent(strings.pendingSendsLoadError);
  });

  it('says the list is truncated when total exceeds the rows', async () => {
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A, ROW_B], 312)),
    });
    render(<PendingSends client={client} />);

    expect(await screen.findByText('Showing the 2 soonest of 312 pending sends.')).toBeVisible();
  });

  it('does not say truncated when every row is shown', async () => {
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A, ROW_B])),
    });
    render(<PendingSends client={client} />);

    await screen.findByText('key-a');
    expect(screen.queryByText(/soonest of/)).toBeNull();
  });

  it('shows the loading state while Refresh is in flight and clears the result line', async () => {
    const user = userEvent.setup();
    const second = deferred<ScheduledMessagesList>();
    const client = buildClient({
      listScheduledMessages: vi
        .fn()
        .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
        .mockResolvedValueOnce(listOf([ROW_B]))
        .mockReturnValueOnce(second.promise),
      cancelScheduledMessage: vi.fn().mockResolvedValue(cancelled('a')),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });
    await waitFor(() => expect(status()).toHaveTextContent('Cancelled send key-a.'));
    await waitFor(() => expect(screen.queryByText('key-a')).toBeNull());

    await user.click(screen.getByRole('button', { name: strings.pendingSendsRefresh }));
    expect(screen.getByText(strings.pendingSendsLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
    expect(status()).toBeEmptyDOMElement();

    second.resolve(listOf([]));
    expect(await screen.findByText(strings.pendingSendsEmpty)).toBeInTheDocument();
  });

  it('needs two activations: Keep restores the trigger with focus and sends no request', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A])),
    });
    render(<PendingSends client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.cancelSendNamed({ key: 'key-a' }) }),
    );
    expect(client.cancelScheduledMessage).not.toHaveBeenCalled();
    expect(confirmButton('key-a')).toHaveFocus();
    expect(confirmButton('key-a')).toHaveTextContent(strings.confirmCancelSend);
    // The notice names the idempotency key.
    expect(screen.getByText(strings.cancelSendNotice({ key: 'key-a' }))).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: strings.keepSend }));

    expect(trigger('key-a')).toHaveFocus();
    expect(trigger('key-a')).toHaveTextContent(strings.cancelSend);
    expect(client.cancelScheduledMessage).not.toHaveBeenCalled();
  });

  it('cancels: sends the id, announces the outcome and reloads in place without the loading state', async () => {
    const user = userEvent.setup();
    const reload = deferred<ScheduledMessagesList>();
    const client = buildClient({
      listScheduledMessages: vi
        .fn()
        .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
        .mockReturnValueOnce(reload.promise),
      cancelScheduledMessage: vi.fn().mockResolvedValue(cancelled('a')),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });

    expect(client.cancelScheduledMessage).toHaveBeenCalledWith({ id: 'a' });
    await waitFor(() =>
      expect(status()).toHaveTextContent('Cancelled send key-a. It will not be delivered.'),
    );
    // The reload is still in flight: the rows stay mounted (no loading state) and
    // no Cancel send trigger is offered for the cancelled row in between.
    expect(screen.queryByText(strings.pendingSendsLoading)).toBeNull();
    expect(screen.queryByRole('button', { name: new RegExp('key-a') })).toBeNull();
    expect(screen.queryByText('key-a')).toBeNull();
    expect(trigger('key-b')).toBeInTheDocument();

    reload.resolve(listOf([ROW_B]));
    await waitFor(() => expect(client.listScheduledMessages).toHaveBeenCalledTimes(2));
    expect(trigger('key-b')).toBeInTheDocument();
    expect(screen.queryByText(strings.pendingSendsLoading)).toBeNull();
  });

  it('announces a not_found outcome as already gone', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listScheduledMessages: vi
        .fn()
        .mockResolvedValueOnce(listOf([ROW_A]))
        .mockResolvedValue(listOf([])),
      cancelScheduledMessage: vi
        .fn()
        .mockResolvedValue({ messageId: 'a', cancelled: false, status: 'not_found' }),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });

    await waitFor(() =>
      expect(status()).toHaveTextContent('Already gone key-a: it was released or removed.'),
    );
    expect(screen.queryByRole('alert')).toBeNull();
    await waitFor(() => expect(screen.queryByText('key-a')).toBeNull());
  });

  it.each(['pending', 'delivered', 'dead', 'partial'])(
    'announces a lost race to release (%s) as already released, not a row error',
    async (releasedStatus) => {
      const user = userEvent.setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A]))
          .mockResolvedValue(listOf([])),
        cancelScheduledMessage: vi
          .fn()
          .mockResolvedValue({ messageId: 'a', cancelled: false, status: releasedStatus }),
      });
      render(<PendingSends client={client} />);

      await cancelRow({ user, key: 'key-a' });

      await waitFor(() =>
        expect(status()).toHaveTextContent(
          'Already released key-a: this message is now in the Delivery log.',
        ),
      );
      expect(screen.queryByRole('alert')).toBeNull();
      await waitFor(() => expect(screen.queryByText('key-a')).toBeNull());
    },
  );

  it('shows an ApiError rejection as a row error and keeps the row and its trigger', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A, ROW_B])),
      cancelScheduledMessage: vi
        .fn()
        .mockRejectedValue(new ApiError({ message: 'delivery service unavailable', status: 503 })),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });

    const rowA = (await screen.findByRole('alert')).closest('tr') as HTMLElement;
    expect(within(rowA).getByText('key-a')).toBeInTheDocument();
    expect(within(rowA).getByRole('alert')).toHaveTextContent(
      'Could not cancel: delivery service unavailable',
    );
    const retryTrigger = within(rowA).getByRole('button', {
      name: strings.cancelSendNamed({ key: 'key-a' }),
    });
    expect(retryTrigger).not.toHaveAttribute('aria-disabled');
    expect(trigger('key-b')).toBeInTheDocument();
    expect(status()).toBeEmptyDOMElement();
    expect(client.listScheduledMessages).toHaveBeenCalledTimes(1);
  });

  it('shows the generic error in the row for a non-ApiError rejection and keeps the row', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A])),
      cancelScheduledMessage: vi.fn().mockRejectedValue(new TypeError('network')),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });

    expect(await screen.findByRole('alert')).toHaveTextContent(
      strings.cancelSendFailed({ message: strings.genericError }),
    );
    expect(trigger('key-a')).toBeInTheDocument();
  });

  it('is aria-disabled with an accessible name carrying the key while cancelling, and a double activation sends one request', async () => {
    const user = userEvent.setup();
    const inFlight = deferred<CancelScheduledMessageResult>();
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A])),
      cancelScheduledMessage: vi.fn().mockReturnValue(inFlight.promise),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });

    const busy = screen.getByRole('button', {
      name: strings.cancellingSendNamed({ key: 'key-a' }),
    });
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    expect(busy).toHaveTextContent(strings.cancellingSend);
    expect(
      busy.getAttribute('aria-label')?.startsWith(strings.cancellingSend.replace('…', '')),
    ).toBe(true);
    await waitFor(() => expect(busy).toHaveFocus());
    await user.click(busy);
    // The Confirm button is named `Confirm cancel send key-a`, so query that name.
    expect(
      screen.queryByRole('button', { name: strings.confirmCancelSendNamed({ key: 'key-a' }) }),
    ).toBeNull();
    expect(client.cancelScheduledMessage).toHaveBeenCalledTimes(1);

    inFlight.resolve(cancelled('a'));
    await waitFor(() => expect(status()).toHaveTextContent('Cancelled send key-a.'));
  });

  it('sends one request when Confirm is activated twice before the busy state renders', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A])),
      cancelScheduledMessage: vi
        .fn()
        .mockReturnValue(deferred<CancelScheduledMessageResult>().promise),
    });
    render(<PendingSends client={client} />);

    await user.click(
      await screen.findByRole('button', { name: strings.cancelSendNamed({ key: 'key-a' }) }),
    );
    const confirm = confirmButton('key-a');
    // Both activations land in one batch: the second sees the same stale handler.
    act(() => {
      confirm.click();
      confirm.click();
    });

    expect(client.cancelScheduledMessage).toHaveBeenCalledTimes(1);
  });

  it('starts no list request when a cancel response arrives after unmount', async () => {
    const user = userEvent.setup();
    const inFlight = deferred<CancelScheduledMessageResult>();
    const client = buildClient({
      listScheduledMessages: vi.fn().mockResolvedValue(listOf([ROW_A])),
      cancelScheduledMessage: vi.fn().mockReturnValue(inFlight.promise),
    });
    const { unmount } = render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });
    expect(client.listScheduledMessages).toHaveBeenCalledTimes(1);
    unmount();

    await act(async () => {
      inFlight.resolve(cancelled('a'));
      await new Promise((done) => setTimeout(done, 0));
    });

    expect(client.listScheduledMessages).toHaveBeenCalledTimes(1);
  });

  it('lets two rows be cancelled at once and keeps the last outcome in the result line', async () => {
    const user = userEvent.setup();
    const first = deferred<CancelScheduledMessageResult>();
    const second = deferred<CancelScheduledMessageResult>();
    const client = buildClient({
      listScheduledMessages: vi
        .fn()
        .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
        .mockResolvedValue(listOf([])),
      cancelScheduledMessage: vi
        .fn()
        .mockReturnValueOnce(first.promise)
        .mockReturnValueOnce(second.promise),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });
    await cancelRow({ user, key: 'key-b' });
    expect(client.cancelScheduledMessage).toHaveBeenCalledTimes(2);

    second.resolve(cancelled('b'));
    await waitFor(() => expect(status()).toHaveTextContent('Cancelled send key-b.'));
    first.resolve({ messageId: 'a', cancelled: false, status: 'delivered' });
    await waitFor(() => expect(status()).toHaveTextContent('Already released key-a'));
  });

  it('does not clear the result line when a new cancel starts', async () => {
    const user = userEvent.setup();
    const second = deferred<CancelScheduledMessageResult>();
    const client = buildClient({
      listScheduledMessages: vi
        .fn()
        .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
        .mockResolvedValue(listOf([ROW_B])),
      cancelScheduledMessage: vi
        .fn()
        .mockResolvedValueOnce(cancelled('a'))
        .mockReturnValueOnce(second.promise),
    });
    render(<PendingSends client={client} />);

    await cancelRow({ user, key: 'key-a' });
    await waitFor(() => expect(status()).toHaveTextContent('Cancelled send key-a.'));
    await cancelRow({ user, key: 'key-b' });

    expect(status()).toHaveTextContent('Cancelled send key-a.');
    second.resolve(cancelled('b'));
    await waitFor(() => expect(status()).toHaveTextContent('Cancelled send key-b.'));
  });

  describe('focus handoff after a cancel', () => {
    it('moves focus to the next row trigger when the cancelled row held focus', async () => {
      const user = userEvent.setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B, ROW_C]))
          .mockResolvedValue(listOf([ROW_A, ROW_C])),
        cancelScheduledMessage: vi.fn().mockResolvedValue(cancelled('b')),
      });
      render(<PendingSends client={client} />);

      await cancelRow({ user, key: 'key-b' });

      await waitFor(() => expect(trigger('key-c')).toHaveFocus());
      expect(document.body).not.toHaveFocus();
    });

    it('falls back to the previous row when the cancelled row was last', async () => {
      const user = userEvent.setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
          .mockResolvedValue(listOf([ROW_A])),
        cancelScheduledMessage: vi.fn().mockResolvedValue(cancelled('b')),
      });
      render(<PendingSends client={client} />);

      await cancelRow({ user, key: 'key-b' });

      await waitFor(() => expect(trigger('key-a')).toHaveFocus());
    });

    it('focuses the heading when the list empties', async () => {
      const user = userEvent.setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A]))
          .mockResolvedValue(listOf([])),
        cancelScheduledMessage: vi.fn().mockResolvedValue(cancelled('a')),
      });
      render(<PendingSends client={client} />);

      await cancelRow({ user, key: 'key-a' });

      await waitFor(() => expect(heading()).toHaveFocus());
      expect(await screen.findByText(strings.pendingSendsEmpty)).toBeInTheDocument();
    });

    it('hands focus off for every cancelled outcome, not only cancelled:true', async () => {
      const user = userEvent.setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
          .mockResolvedValue(listOf([ROW_B])),
        cancelScheduledMessage: vi
          .fn()
          .mockResolvedValue({ messageId: 'a', cancelled: false, status: 'delivered' }),
      });
      render(<PendingSends client={client} />);

      await cancelRow({ user, key: 'key-a' });

      await waitFor(() => expect(trigger('key-b')).toHaveFocus());
    });

    it('hands focus off when it was lost to <body>', async () => {
      const user = userEvent.setup();
      const inFlight = deferred<CancelScheduledMessageResult>();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
          .mockResolvedValue(listOf([ROW_B])),
        cancelScheduledMessage: vi.fn().mockReturnValue(inFlight.promise),
      });
      render(<PendingSends client={client} />);

      await cancelRow({ user, key: 'key-a' });
      await act(async () => {
        (document.activeElement as HTMLElement).blur();
      });
      expect(document.body).toHaveFocus();

      inFlight.resolve(cancelled('a'));

      await waitFor(() => expect(trigger('key-b')).toHaveFocus());
    });

    it('leaves focus on another row open confirm when a concurrent cancel response arrives', async () => {
      const user = userEvent.setup();
      const cancelA = deferred<CancelScheduledMessageResult>();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B, ROW_C]))
          .mockResolvedValue(listOf([ROW_B, ROW_C])),
        cancelScheduledMessage: vi.fn().mockReturnValue(cancelA.promise),
      });
      render(<PendingSends client={client} />);

      await cancelRow({ user, key: 'key-a' });
      await user.click(trigger('key-b'));
      expect(confirmButton('key-b')).toHaveFocus();

      cancelA.resolve(cancelled('a'));

      await waitFor(() => expect(status()).toHaveTextContent('Cancelled send key-a.'));
      await waitFor(() => expect(client.listScheduledMessages).toHaveBeenCalledTimes(2));
      await waitFor(() => expect(screen.queryByText('key-a')).toBeNull());
      // Focus stays on B's open confirm, which also survived the reload.
      expect(confirmButton('key-b')).toHaveFocus();
    });

    it('falls back to the next mounted neighbour when the nearest trigger is not registered', async () => {
      const user = userEvent.setup();
      const cancelA = deferred<CancelScheduledMessageResult>();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B, ROW_C]))
          .mockResolvedValue(listOf([ROW_B, ROW_C])),
        cancelScheduledMessage: vi.fn().mockReturnValue(cancelA.promise),
      });
      render(<PendingSends client={client} />);

      // B has an open confirm (no trigger mounted), so C is the next mounted neighbour.
      await user.click(
        await screen.findByRole('button', { name: strings.cancelSendNamed({ key: 'key-b' }) }),
      );
      await cancelRow({ user, key: 'key-a' });
      cancelA.resolve(cancelled('a'));

      await waitFor(() => expect(trigger('key-c')).toHaveFocus());
      expect(confirmButton('key-b')).toBeInTheDocument();
    });

    it('falls back to the heading when no neighbour trigger is mounted', async () => {
      const user = userEvent.setup();
      const cancelA = deferred<CancelScheduledMessageResult>();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
          .mockResolvedValue(listOf([ROW_B])),
        cancelScheduledMessage: vi.fn().mockReturnValue(cancelA.promise),
      });
      render(<PendingSends client={client} />);

      await user.click(
        await screen.findByRole('button', { name: strings.cancelSendNamed({ key: 'key-b' }) }),
      );
      await cancelRow({ user, key: 'key-a' });
      cancelA.resolve(cancelled('a'));

      await waitFor(() => expect(heading()).toHaveFocus());
      expect(confirmButton('key-b')).toBeInTheDocument();
    });
  });

  describe('with fake timers', () => {
    const NOW = '2026-10-18T14:00:00Z';

    async function flush() {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
    }

    async function advance(ms: number) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
      });
    }

    function setup() {
      vi.useFakeTimers();
      vi.setSystemTime(new Date(NOW));
    }

    it('updates the relative text on the 30s tick and marks a row Due once its time passes', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValue(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:05:10Z' })]),
          ),
      });
      render(<PendingSends client={client} />);
      await flush();

      expect(screen.getByText('in 5 min')).toBeInTheDocument();
      expect(screen.getByText(strings.pendingStateScheduled)).toBeInTheDocument();

      await advance(30_000);
      expect(screen.getByText('in 4 min')).toBeInTheDocument();
      // Nothing due yet: the tick does not reload.
      expect(client.listScheduledMessages).toHaveBeenCalledTimes(1);

      await advance(5 * 60_000);
      expect(screen.getByText(strings.pendingStateDue)).toBeInTheDocument();
      expect(screen.getByText(strings.relativeDueNow)).toBeInTheDocument();
      expect(screen.getByText(strings.pendingReleasing)).toBeInTheDocument();
    });

    it('quietly reloads on a tick with a due row so released rows leave the list', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:00:45Z' }), ROW_B]),
          )
          .mockResolvedValue(listOf([ROW_B])),
      });
      render(<PendingSends client={client} />);
      await flush();

      await advance(30_000);
      expect(client.listScheduledMessages).toHaveBeenCalledTimes(1);

      await advance(30_000);
      expect(client.listScheduledMessages).toHaveBeenCalledTimes(2);
      expect(screen.queryByText('key-a')).toBeNull();
      expect(screen.getByText('key-b')).toBeInTheDocument();
      // A quiet reload never shows the loading state.
      expect(screen.queryByText(strings.pendingSendsLoading)).toBeNull();
      // The removed row was not focused: no focus move and no status line.
      expect(status()).toBeEmptyDOMElement();
    });

    it('moves focus to the neighbour and writes the Released line when the reload drops the focused row', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:00:45Z' }), ROW_B]),
          )
          .mockResolvedValue(listOf([ROW_B])),
      });
      render(<PendingSends client={client} />);
      await flush();

      act(() => {
        trigger('key-a').focus();
      });
      expect(trigger('key-a')).toHaveFocus();

      await advance(60_000);

      expect(screen.queryByText('key-a')).toBeNull();
      expect(trigger('key-b')).toHaveFocus();
      expect(status()).toHaveTextContent('Released key-a: it is no longer pending.');
    });

    it('moves focus from an open confirm on the dropped row, falling back to the heading when none remain', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:00:45Z' })]),
          )
          .mockResolvedValue(listOf([])),
      });
      render(<PendingSends client={client} />);
      await flush();

      fireEvent.click(trigger('key-a'));
      expect(confirmButton('key-a')).toHaveFocus();

      await advance(60_000);

      expect(heading()).toHaveFocus();
      expect(status()).toHaveTextContent('Released key-a: it is no longer pending.');
      expect(screen.getByText(strings.pendingSendsEmpty)).toBeInTheDocument();
    });

    it('moves no focus and writes no line when the reload drops a row while focus is elsewhere', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:00:45Z' }), ROW_B]),
          )
          .mockResolvedValue(listOf([ROW_B])),
      });
      render(<PendingSends client={client} />);
      await flush();

      act(() => {
        trigger('key-b').focus();
      });

      await advance(60_000);

      expect(screen.queryByText('key-a')).toBeNull();
      expect(trigger('key-b')).toHaveFocus();
      expect(status()).toBeEmptyDOMElement();
    });

    it('keeps the rows and an open confirm, with no alert, when a tick reload fails', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:00:45Z' }), ROW_B]),
          )
          .mockRejectedValue(new ApiError({ message: 'upstream down', status: 502 })),
      });
      render(<PendingSends client={client} />);
      await flush();

      fireEvent.click(trigger('key-b'));
      expect(confirmButton('key-b')).toBeInTheDocument();

      await advance(60_000);

      expect(client.listScheduledMessages).toHaveBeenCalledTimes(2);
      expect(screen.queryByRole('alert')).toBeNull();
      expect(screen.getByText('key-a')).toBeInTheDocument();
      expect(confirmButton('key-b')).toBeInTheDocument();
    });

    it('keeps the rows, with no alert, when the post-cancel reload fails', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([ROW_A, ROW_B]))
          .mockRejectedValue(new ApiError({ message: 'upstream down', status: 502 })),
        cancelScheduledMessage: vi.fn().mockResolvedValue(cancelled('a')),
      });
      render(<PendingSends client={client} />);
      await flush();

      fireEvent.click(trigger('key-a'));
      fireEvent.click(confirmButton('key-a'));
      await flush();

      expect(client.listScheduledMessages).toHaveBeenCalledTimes(2);
      expect(screen.queryByRole('alert')).toBeNull();
      expect(screen.queryByText('key-a')).toBeNull();
      expect(trigger('key-b')).toBeInTheDocument();
      expect(status()).toHaveTextContent('Cancelled send key-a.');
    });

    it('writes no Released line for a dropped row whose cancel is in flight', async () => {
      setup();
      const inFlight = deferred<CancelScheduledMessageResult>();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:00:45Z' }), ROW_B]),
          )
          .mockResolvedValue(listOf([ROW_B])),
        cancelScheduledMessage: vi.fn().mockReturnValue(inFlight.promise),
      });
      render(<PendingSends client={client} />);
      await flush();

      fireEvent.click(trigger('key-a'));
      fireEvent.click(confirmButton('key-a'));
      const busy = screen.getByRole('button', {
        name: strings.cancellingSendNamed({ key: 'key-a' }),
      });
      expect(busy).toHaveFocus();

      await advance(60_000);

      // The tick reload dropped row A while its cancel is pending: no stray line.
      expect(client.listScheduledMessages).toHaveBeenCalledTimes(2);
      expect(screen.queryByText('key-a')).toBeNull();
      expect(status()).toBeEmptyDOMElement();

      inFlight.resolve(cancelled('a'));
      await flush();
      expect(status()).toHaveTextContent('Cancelled send key-a.');
    });

    it('lets the newest reload win: an older in-flight list does not re-insert a cancelled row', async () => {
      setup();
      const older = deferred<ScheduledMessagesList>();
      const newest = deferred<ScheduledMessagesList>();
      const dueA = buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T14:00:45Z' });
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValueOnce(listOf([dueA, ROW_B]))
          .mockReturnValueOnce(older.promise)
          .mockReturnValueOnce(newest.promise),
        cancelScheduledMessage: vi.fn().mockResolvedValue(cancelled('a')),
      });
      render(<PendingSends client={client} />);
      await flush();

      // The 60s tick starts a quiet reload that stays in flight.
      await advance(60_000);
      expect(client.listScheduledMessages).toHaveBeenCalledTimes(2);

      fireEvent.click(trigger('key-a'));
      fireEvent.click(confirmButton('key-a'));
      await flush();
      expect(client.listScheduledMessages).toHaveBeenCalledTimes(3);

      newest.resolve(listOf([ROW_B]));
      await flush();
      older.resolve(listOf([dueA, ROW_B]));
      await flush();

      expect(screen.queryByText('key-a')).toBeNull();
      expect(screen.getByText('key-b')).toBeInTheDocument();
    });

    it('clears the tick interval on unmount', async () => {
      setup();
      const client = buildClient({
        listScheduledMessages: vi
          .fn()
          .mockResolvedValue(
            listOf([buildScheduled({ messageId: 'a', deliverAt: '2026-10-18T13:59:00Z' })]),
          ),
      });
      const { unmount } = render(<PendingSends client={client} />);
      await flush();

      await advance(30_000);
      const callsBefore = (client.listScheduledMessages as ReturnType<typeof vi.fn>).mock.calls
        .length;
      expect(callsBefore).toBeGreaterThan(1);

      unmount();
      await advance(5 * 60_000);

      expect(client.listScheduledMessages).toHaveBeenCalledTimes(callsBefore);
      expect(vi.getTimerCount()).toBe(0);
    });
  });
});

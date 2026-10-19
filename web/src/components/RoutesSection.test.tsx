import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { DeliveryApiClient, DestinationSummary, RouteSummary } from '../api/deliveryClient';
import { strings } from '../strings';
import { DeliveryRegistry } from './DeliveryRegistry';
import { RoutesSection } from './RoutesSection';
import type { DestinationsLoadState } from './RoutesSection';

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

function buildDestination(name: string, enabled = true): DestinationSummary {
  return {
    name,
    type: 'ntfy',
    enabled,
    disabledReason: null,
    config: { topic: 'family_alerts' },
    secrets: {},
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
  };
}

function buildRoute(overrides: Partial<RouteSummary> & { id: number }): RouteSummary {
  return {
    appPattern: '*',
    channelPattern: 'alerts*',
    destinations: ['ops_discord'],
    enabled: true,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ...overrides,
  };
}

function buildClient(overrides: Partial<DeliveryApiClient> = {}): DeliveryApiClient {
  return {
    getReadiness: vi.fn(),
    listDestinations: vi.fn().mockResolvedValue([]),
    createDestination: vi.fn(),
    updateDestination: vi.fn(),
    deleteDestination: vi.fn().mockResolvedValue(undefined),
    rotateSecret: vi.fn(),
    sendTest: vi.fn(),
    listRoutes: vi.fn().mockResolvedValue([]),
    createRoute: vi.fn(),
    updateRoute: vi.fn(),
    deleteRoute: vi.fn().mockResolvedValue(undefined),
    listDeliveries: vi.fn().mockResolvedValue([]),
    listAttempts: vi.fn().mockResolvedValue([]),
    replayDelivery: vi.fn(),
    listScheduledMessages: vi.fn().mockResolvedValue({ items: [], total: 0 }),
    cancelScheduledMessage: vi.fn(),
    ...overrides,
  };
}

const READY_DESTINATIONS: DestinationsLoadState = {
  status: 'ready',
  destinations: [
    buildDestination('ops_discord'),
    buildDestination('family_ntfy'),
    buildDestination('chores_hook', false),
  ],
};

function renderSection({
  client,
  destinationsState = READY_DESTINATIONS,
}: {
  client: DeliveryApiClient;
  destinationsState?: DestinationsLoadState;
}) {
  return render(
    <RoutesSection client={client} destinationsState={destinationsState} refreshSignal={0} />,
  );
}

function button(name: string) {
  return screen.getByRole('button', { name });
}

describe('RoutesSection list', () => {
  it('shows loading, then rows with chips and status', async () => {
    const client = buildClient({
      listRoutes: vi
        .fn()
        .mockResolvedValue([
          buildRoute({ id: 1, destinations: ['ops_discord', 'family_ntfy'] }),
          buildRoute({ id: 2, appPattern: 'chores4irl', enabled: false }),
        ]),
    });
    renderSection({ client });
    expect(screen.getByText(strings.routesLoading)).toBeTruthy();
    expect(await screen.findByRole('button', { name: 'Edit route 1' })).toBeTruthy();
    expect(screen.getByText('family_ntfy')).toBeTruthy();
    expect(screen.getByText(strings.routeEnabled)).toBeTruthy();
    expect(screen.getByText(strings.routeOff)).toBeTruthy();
    expect(screen.getByText(strings.routesHint)).toBeTruthy();
  });

  it('shows the empty and error states', async () => {
    renderSection({ client: buildClient() });
    expect(await screen.findByText(strings.routesEmpty)).toBeTruthy();
    cleanup();
    renderSection({
      client: buildClient({
        listRoutes: vi
          .fn()
          .mockRejectedValue(new ApiError({ message: 'routes down', status: 500 })),
      }),
    });
    expect((await screen.findByRole('alert')).textContent).toBe('routes down');
  });

  it('never renders a superseded routes response', async () => {
    const slow = deferred<RouteSummary[]>();
    const listRoutes = vi
      .fn()
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce([buildRoute({ id: 7 })]);
    const client = buildClient({ listRoutes });
    const view = render(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={0} />,
    );
    view.rerender(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={1} />,
    );
    expect(await screen.findByRole('button', { name: 'Edit route 7' })).toBeTruthy();
    slow.resolve([buildRoute({ id: 99 })]);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByRole('button', { name: 'Edit route 99' })).toBeNull();
  });
});

describe('add route', () => {
  const hintCases: Array<[string, DestinationsLoadState, string]> = [
    ['loading', { status: 'loading', destinations: [] }, strings.addRouteHintLoading],
    ['error', { status: 'error', destinations: [] }, strings.addRouteHintError],
    ['empty', { status: 'ready', destinations: [] }, strings.addRouteHintEmpty],
  ];
  it.each(hintCases)('is aria-disabled with a %s hint', async (_label, destinationsState, hint) => {
    const user = userEvent.setup();
    renderSection({ client: buildClient(), destinationsState });
    const add = button(strings.addRouteAction);
    expect(add.getAttribute('aria-disabled')).toBe('true');
    const describedBy = add.getAttribute('aria-describedby');
    expect(describedBy).not.toBeNull();
    expect(document.getElementById(describedBy ?? '')?.textContent).toBe(hint);
    await user.click(add);
    expect(screen.queryByRole('form', { name: strings.addRouteHeading })).toBeNull();
  });

  it('opens focused on the first field and creates a route', async () => {
    const user = userEvent.setup();
    const createRoute = vi.fn().mockResolvedValue(buildRoute({ id: 3 }));
    const listRoutes = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([buildRoute({ id: 3, appPattern: 'chores4irl' })]);
    renderSection({ client: buildClient({ createRoute, listRoutes }) });
    await screen.findByText(strings.routesEmpty);
    await user.click(button(strings.addRouteAction));
    const app = screen.getByLabelText(strings.routeAppLabel);
    expect(document.activeElement).toBe(app);
    await user.type(app, '  chores4irl ');
    await user.type(screen.getByLabelText(strings.routeChannelLabel), 'digest');
    await user.click(screen.getByLabelText('family_ntfy'));
    expect(screen.getByLabelText(/chores_hook/).closest('label')?.textContent).toContain(
      strings.routeDestinationDisabledMark,
    );
    await user.click(
      screen
        .getAllByRole('button', { name: strings.addRouteAction })
        .find((element) => element.getAttribute('type') === 'submit') as HTMLElement,
    );
    await waitFor(() =>
      expect(createRoute).toHaveBeenCalledWith({
        appPattern: 'chores4irl',
        channelPattern: 'digest',
        destinations: ['family_ntfy'],
        enabled: true,
      }),
    );
    expect(await screen.findByRole('button', { name: 'Edit route 3' })).toBeTruthy();
    expect(screen.queryByLabelText(strings.routeAppLabel)).toBeNull();
  });

  it.each([
    ['bad app', 'Everyone', 'digest', true, strings.invalidRouteAppPattern],
    ['reserved app', 'everyone', 'digest', true, strings.invalidRouteAppPattern],
    ['bad channel', '*', '*alerts', true, strings.invalidRouteChannelPattern],
    ['no destination', '*', 'digest', false, strings.routeNeedsDestination],
  ])('blocks %s before any request', async (_label, app, channel, pick, message) => {
    const user = userEvent.setup();
    const createRoute = vi.fn();
    renderSection({ client: buildClient({ createRoute }) });
    await screen.findByText(strings.routesEmpty);
    await user.click(button(strings.addRouteAction));
    await user.type(screen.getByLabelText(strings.routeAppLabel), app);
    await user.type(screen.getByLabelText(strings.routeChannelLabel), channel);
    if (pick) {
      await user.click(screen.getByLabelText('ops_discord'));
    }
    const submit = screen
      .getAllByRole('button', { name: strings.addRouteAction })
      .find((element) => element.getAttribute('type') === 'submit');
    await user.click(submit as HTMLElement);
    expect((await screen.findByRole('alert')).textContent).toBe(message);
    expect(createRoute).not.toHaveBeenCalled();
  });

  it.each([
    [409, 'route already exists'],
    [400, 'unknown destination'],
  ])('shows a %s server error inline', async (status, message) => {
    const user = userEvent.setup();
    const createRoute = vi.fn().mockRejectedValue(new ApiError({ message, status }));
    renderSection({ client: buildClient({ createRoute }) });
    await screen.findByText(strings.routesEmpty);
    await user.click(button(strings.addRouteAction));
    await user.type(screen.getByLabelText(strings.routeAppLabel), '*');
    await user.type(screen.getByLabelText(strings.routeChannelLabel), '*');
    await user.click(screen.getByLabelText('ops_discord'));
    const submit = screen
      .getAllByRole('button', { name: strings.addRouteAction })
      .find((element) => element.getAttribute('type') === 'submit');
    await user.click(submit as HTMLElement);
    expect((await screen.findByRole('alert')).textContent).toBe(message);
    expect(screen.getByLabelText(strings.routeAppLabel)).toBeTruthy();
  });
});

describe('edit route', () => {
  async function openEditor(client: DeliveryApiClient) {
    const user = userEvent.setup();
    renderSection({ client });
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    return user;
  }

  it('focuses the first field and returns focus to the trigger on close', async () => {
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
    });
    const user = await openEditor(client);
    expect(document.activeElement).toBe(screen.getByLabelText(strings.routeAppLabel));
    await user.click(button('Close'));
    await waitFor(() => expect(document.activeElement).toBe(button('Edit route 1')));
  });

  it('sends only the pattern when only the pattern changed', async () => {
    const updateRoute = vi.fn().mockResolvedValue(buildRoute({ id: 1 }));
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
      updateRoute,
    });
    const user = await openEditor(client);
    const app = screen.getByLabelText(strings.routeAppLabel);
    await user.clear(app);
    await user.type(app, 'chores4irl');
    await user.click(button('Save route 1'));
    await waitFor(() =>
      expect(updateRoute).toHaveBeenCalledWith({ id: 1, appPattern: 'chores4irl' }),
    );
  });

  it('sends the full destination set when only destinations changed', async () => {
    const updateRoute = vi.fn().mockResolvedValue(buildRoute({ id: 1 }));
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
      updateRoute,
    });
    const user = await openEditor(client);
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(button('Save route 1'));
    await waitFor(() =>
      expect(updateRoute).toHaveBeenCalledWith({
        id: 1,
        destinations: ['ops_discord', 'family_ntfy'],
      }),
    );
  });

  it('sends nothing for an untouched save and closes', async () => {
    const updateRoute = vi.fn();
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
      updateRoute,
    });
    const user = await openEditor(client);
    await user.click(button('Save route 1'));
    expect(updateRoute).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByLabelText(strings.routeAppLabel)).toBeNull());
  });

  it('a second Save click while in flight sends no second request', async () => {
    const pending = deferred<RouteSummary>();
    const updateRoute = vi.fn().mockReturnValue(pending.promise);
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
      updateRoute,
    });
    const user = await openEditor(client);
    await user.click(screen.getByLabelText(/Enabled/));
    await user.click(button('Save route 1'));
    const saving = await screen.findByRole('button', { name: strings.savingRoute });
    expect(saving.getAttribute('aria-disabled')).toBe('true');
    await user.click(saving);
    expect(updateRoute).toHaveBeenCalledTimes(1);
    pending.resolve(buildRoute({ id: 1, enabled: false }));
    await waitFor(() => expect(screen.queryByLabelText(strings.routeAppLabel)).toBeNull());
  });

  it('a routes refresh mid-edit never overwrites typed values', async () => {
    const listRoutes = vi.fn().mockResolvedValue([buildRoute({ id: 1 })]);
    const client = buildClient({ listRoutes });
    const user = userEvent.setup();
    const view = render(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={0} />,
    );
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    await user.type(screen.getByLabelText(strings.routeAppLabel), 'x');
    listRoutes.mockResolvedValue([buildRoute({ id: 1, appPattern: 'server_changed' })]);
    view.rerender(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={1} />,
    );
    await waitFor(() => expect(listRoutes).toHaveBeenCalledTimes(2));
    await screen.findByText('server_changed');
    expect((screen.getByLabelText(strings.routeAppLabel) as HTMLInputElement).value).toBe('*x');
  });
});

describe('delete route', () => {
  it('moves focus to the next row after the confirm, and a second click is a no-op', async () => {
    const user = userEvent.setup();
    const pending = deferred<undefined>();
    const deleteRoute = vi.fn().mockReturnValue(pending.promise);
    const listRoutes = vi
      .fn()
      .mockResolvedValueOnce([buildRoute({ id: 1 }), buildRoute({ id: 2 })])
      .mockResolvedValueOnce([buildRoute({ id: 2 })]);
    renderSection({ client: buildClient({ deleteRoute, listRoutes }) });
    await user.click(await screen.findByRole('button', { name: 'Delete route 1' }));
    await user.click(button('Confirm delete route 1'));
    const busy = await screen.findByRole('button', { name: strings.deletingRoute });
    expect(busy.getAttribute('aria-disabled')).toBe('true');
    expect(document.activeElement).toBe(busy);
    await user.click(busy);
    expect(deleteRoute).toHaveBeenCalledTimes(1);
    pending.resolve(undefined);
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Edit route 1' })).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(button('Edit route 2')));
  });

  it('moves focus to the heading when no row remains', async () => {
    const user = userEvent.setup();
    const listRoutes = vi
      .fn()
      .mockResolvedValueOnce([buildRoute({ id: 1 })])
      .mockResolvedValueOnce([]);
    renderSection({ client: buildClient({ listRoutes }) });
    await user.click(await screen.findByRole('button', { name: 'Delete route 1' }));
    await user.click(button('Confirm delete route 1'));
    await waitFor(() =>
      expect(document.activeElement).toBe(
        screen.getByRole('heading', { name: strings.routesHeading }),
      ),
    );
  });

  it('shows a failed delete inline', async () => {
    const user = userEvent.setup();
    const deleteRoute = vi.fn().mockRejectedValue(new ApiError({ message: 'boom', status: 500 }));
    const listRoutes = vi.fn().mockResolvedValue([buildRoute({ id: 1 })]);
    renderSection({ client: buildClient({ deleteRoute, listRoutes }) });
    await user.click(await screen.findByRole('button', { name: 'Delete route 1' }));
    await user.click(button('Confirm delete route 1'));
    expect((await screen.findByRole('alert')).textContent).toBe('boom');
    expect(button('Delete route 1')).toBeTruthy();
  });
});

describe('destination delete refreshes routes', () => {
  it('route chips drop the deleted destination once the delete settles', async () => {
    const user = userEvent.setup();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([buildDestination('ops_discord'), buildDestination('family_ntfy')])
      .mockResolvedValueOnce([buildDestination('family_ntfy')]);
    const listRoutes = vi
      .fn()
      .mockResolvedValueOnce([buildRoute({ id: 1, destinations: ['ops_discord', 'family_ntfy'] })])
      .mockResolvedValueOnce([buildRoute({ id: 1, destinations: ['family_ntfy'] })]);
    const client = buildClient({ listDestinations, listRoutes });
    render(<DeliveryRegistry client={client} />);
    await user.click(await screen.findByRole('button', { name: 'Delete ops_discord' }));
    await user.click(button('Confirm delete ops_discord'));
    await waitFor(() => expect(listRoutes).toHaveBeenCalledTimes(2));
    await waitFor(() => {
      const row = button('Edit route 1').closest('tr');
      expect(row?.textContent).not.toContain('ops_discord');
      expect(row?.textContent).toContain('family_ntfy');
    });
  });
});

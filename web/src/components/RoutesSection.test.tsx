import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
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
    ownerKind: 'operator',
    ownerAppId: null,
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
    ownerKind: 'operator',
    ownerAppId: null,
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

describe('owner column', () => {
  const managedNote = strings.ownerManagedNote({ appId: 'chores4irl' });

  async function renderOwned() {
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([
        buildRoute({ id: 1 }),
        buildRoute({
          id: 2,
          appPattern: 'chores4irl',
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        buildRoute({ id: 3, ownerKind: 'unknown', ownerAppId: null }),
      ]),
    });
    renderSection({ client });
    await screen.findByRole('button', { name: 'Edit route 1' });
    return client;
  }

  function rowOf(id: number): HTMLElement {
    // By label, not role: an open confirm dialog makes the page behind it inert.
    const row = screen.getByLabelText(`Edit route ${id}`).closest('tr');
    if (row === null) {
      throw new Error(`row ${id} not found`);
    }
    return row;
  }

  it('has an Owner column header after Channel', async () => {
    await renderOwned();

    const headers = screen.getAllByRole('columnheader').map((header) => header.textContent);
    expect(headers.slice(0, 3)).toEqual([
      strings.columnRouteApp,
      strings.columnRouteChannel,
      strings.columnOwner,
    ]);
  });

  it('shows Operator, App · <id> and Unknown badges with a hidden Owner prefix', async () => {
    await renderOwned();

    const operatorBadge = within(rowOf(1)).getByText(strings.ownerOperator);
    expect(operatorBadge.parentElement).toHaveTextContent(
      `${strings.ownerPrefix}${strings.ownerOperator}`,
    );
    const appBadge = within(rowOf(2)).getByText(strings.ownerApp({ appId: 'chores4irl' }));
    expect(appBadge.parentElement).toHaveTextContent(
      `${strings.ownerPrefix}${strings.ownerApp({ appId: 'chores4irl' })}`,
    );
    const unknownBadge = within(rowOf(3)).getByText(strings.ownerUnknown);
    expect(unknownBadge.parentElement).toHaveTextContent(
      `${strings.ownerPrefix}${strings.ownerUnknown}`,
    );
    expect(within(rowOf(3)).queryByText(strings.ownerOperator)).toBeNull();
  });

  it('notes app-owned and unknown rows, not operator rows', async () => {
    await renderOwned();

    // The line under the buttons is the short form, never the long text, and has no `title`.
    expect(within(rowOf(2)).getByText(strings.ownerManagedInlineNote)).not.toHaveAttribute('title');
    expect(within(rowOf(2)).queryByText(managedNote)).toBeNull();
    expect(within(rowOf(3)).getByText(strings.ownerUnknownInlineNote)).toBeInTheDocument();
    expect(within(rowOf(3)).queryByText(strings.ownerUnknownNote)).toBeNull();
    expect(within(rowOf(1)).queryByText(strings.ownerManagedInlineNote)).toBeNull();
    expect(within(rowOf(1)).queryByText(strings.ownerUnknownInlineNote)).toBeNull();
  });

  it('repeats the note as the Delete confirm notice on an app-owned row only', async () => {
    const user = userEvent.setup();
    await renderOwned();

    const cases = [
      { id: 2, note: managedNote },
      // An unknown owner is never treated as safe: its confirm warns too.
      { id: 3, note: strings.ownerUnknownNote },
    ];
    for (const { id, note } of cases) {
      await user.click(button(`Delete route ${id}`));
      expect(within(screen.getByRole('dialog')).getAllByText(note)).toHaveLength(1);
      await user.click(button(strings.cancelActionNamed({ subject: `Delete route ${id}` })));
    }
    expect(within(rowOf(2)).getAllByText(strings.ownerManagedInlineNote)).toHaveLength(1);

    // The operator's own row confirms with no notice at all.
    await user.click(button('Delete route 1'));
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).getByRole('button', { name: 'Confirm delete route 1' }),
    ).toBeInTheDocument();
    expect(dialog.querySelector('.modal__notice')).toBeNull();
  });

  it('edits in a dialog and leaves no editor row in the table', async () => {
    const user = userEvent.setup();
    await renderOwned();

    await user.click(button('Edit route 2'));

    const dialog = screen.getByRole('dialog', { name: strings.editRoute({ id: 2 }) });
    expect(within(dialog).getByLabelText(strings.routeAppLabel)).toBeInTheDocument();
    expect(dialog.closest('tr')).toBeNull();
  });

  it('pins the App column in the header and in every row, never the Actions cell', async () => {
    await renderOwned();

    const headers = screen.getAllByRole('columnheader');
    expect(headers[0]).toHaveClass('admin-table__lead');
    expect(within(rowOf(1)).getAllByRole('cell')[0]).toHaveClass('admin-table__lead');
    expect(headers[headers.length - 1]).not.toHaveClass('admin-table__lead');
    expect(screen.getByRole('table')).toHaveClass('admin-table');
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
    const formClose = screen
      .getByRole('dialog')
      .querySelector<HTMLElement>('.destination-form__button--ghost');
    expect(formClose).not.toBeNull();
    await user.click(formClose as HTMLElement);
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

  it('opens one dialog with no form aria-label, stamped with its row', async () => {
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
    });
    await openEditor(client);

    const dialog = screen.getByRole('dialog', { name: strings.editRoute({ id: 1 }) });
    expect(screen.getAllByRole('dialog')).toHaveLength(1);
    expect(dialog).toHaveAttribute('data-owner-row-attribute', 'data-route-id');
    expect(dialog).toHaveAttribute('data-owner-row-id', '1');
    expect(screen.queryByRole('form', { name: strings.editRoute({ id: 1 }) })).toBeNull();
  });

  it('Escape on a clean dialog closes it without the guard and returns focus to Edit', async () => {
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
    });
    const user = await openEditor(client);

    await user.keyboard('{Escape}');

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByText(strings.discardChangesMessage)).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(button('Edit route 1')));
  });

  it('a dirty dialog shows the discard guard; Keep editing returns, Discard closes', async () => {
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
    });
    const user = await openEditor(client);
    await user.click(screen.getByLabelText('family_ntfy'));

    await user.keyboard('{Escape}');
    expect(screen.getByText(strings.discardChangesMessage)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: strings.keepEditing }));
    expect(screen.getByLabelText('family_ntfy')).toBeChecked();

    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: strings.discardChanges }));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('reverting every edit to the baseline makes the dialog clean again', async () => {
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
    });
    const user = await openEditor(client);
    const app = screen.getByLabelText(strings.routeAppLabel);
    await user.type(app, 'x');
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(screen.getByLabelText(/Enabled/));
    await user.clear(app);
    await user.type(app, '*');
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(screen.getByLabelText(/Enabled/));

    await user.keyboard('{Escape}');

    expect(screen.queryByText(strings.discardChangesMessage)).toBeNull();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('keeps the dialog open with a notice and Save disabled when a refresh drops the route', async () => {
    const listRoutes = vi.fn().mockResolvedValue([buildRoute({ id: 1 })]);
    const client = buildClient({ listRoutes });
    const user = userEvent.setup();
    const view = render(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={0} />,
    );
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    await user.click(screen.getByLabelText('family_ntfy'));
    listRoutes.mockResolvedValue([]);
    view.rerender(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={1} />,
    );

    expect(await screen.findByText(strings.editRowGone)).toBeInTheDocument();
    expect(button('Save route 1')).toBeDisabled();
    expect(screen.getByLabelText('family_ntfy')).toBeChecked();
  });

  it('opening Edit drops the Add form and what was typed in it', async () => {
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
    });
    const user = userEvent.setup();
    renderSection({ client });
    await user.click(await screen.findByRole('button', { name: strings.addRouteAction }));
    await user.type(screen.getByLabelText(strings.routeAppLabel), 'typed');

    await user.click(button('Edit route 1'));

    expect(screen.queryByRole('form', { name: strings.addRouteHeading })).toBeNull();
    expect(screen.getAllByLabelText(strings.routeAppLabel)).toHaveLength(1);
  });

  it('a save whose own refresh was superseded still judges the newest list: row dropped, no close', async () => {
    const stale = deferred<RouteSummary[]>();
    const listRoutes = vi
      .fn()
      .mockResolvedValueOnce([buildRoute({ id: 1 })])
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValue([]);
    const updateRoute = vi.fn().mockResolvedValue(buildRoute({ id: 1 }));
    const client = buildClient({ listRoutes, updateRoute });
    const user = userEvent.setup();
    const view = render(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={0} />,
    );
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(button('Save route 1'));
    // The save's refresh (second call) hangs; a newer one lands first and drops the row.
    await waitFor(() => expect(listRoutes).toHaveBeenCalledTimes(2));
    view.rerender(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={1} />,
    );
    await waitFor(() => expect(listRoutes).toHaveBeenCalledTimes(3));
    stale.resolve([buildRoute({ id: 1 })]);

    expect(await screen.findByText(strings.editRowGone)).toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: strings.editRoute({ id: 1 }) })).toBeInTheDocument();
  });

  it('a save that lands after the row was dropped by a refresh does not close the dialog', async () => {
    const pending = deferred<RouteSummary>();
    const listRoutes = vi.fn().mockResolvedValue([buildRoute({ id: 1 })]);
    const updateRoute = vi.fn().mockReturnValue(pending.promise);
    const client = buildClient({ listRoutes, updateRoute });
    const user = userEvent.setup();
    const view = render(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={0} />,
    );
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(button('Save route 1'));
    listRoutes.mockResolvedValue([]);
    view.rerender(
      <RoutesSection client={client} destinationsState={READY_DESTINATIONS} refreshSignal={1} />,
    );
    expect(await screen.findByText(strings.editRowGone)).toBeInTheDocument();

    pending.resolve(buildRoute({ id: 1 }));
    await waitFor(() => expect(updateRoute).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(listRoutes).toHaveBeenCalledTimes(3));

    expect(screen.getByRole('dialog', { name: strings.editRoute({ id: 1 }) })).toBeInTheDocument();
    expect(screen.getByText(strings.editRowGone)).toBeInTheDocument();
  });

  it('a save that lands after the dialog moved to another route does not close that dialog', async () => {
    const pending = deferred<RouteSummary>();
    const listRoutes = vi.fn().mockResolvedValue([buildRoute({ id: 1 }), buildRoute({ id: 2 })]);
    const updateRoute = vi.fn().mockReturnValue(pending.promise);
    const client = buildClient({ listRoutes, updateRoute });
    const user = userEvent.setup();
    renderSection({ client });
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(button('Save route 1'));
    // Production keeps the page behind the dialog inert; jsdom does not, so this stands in for
    // any path that moves the section to another route while the save is in flight.
    await user.click(button('Edit route 2'));
    expect(screen.getByRole('dialog', { name: strings.editRoute({ id: 2 }) })).toBeInTheDocument();

    pending.resolve(buildRoute({ id: 1 }));
    await waitFor(() => expect(listRoutes).toHaveBeenCalledTimes(2));

    expect(screen.getByRole('dialog', { name: strings.editRoute({ id: 2 }) })).toBeInTheDocument();
  });

  it('after a save the next dialog opens clean: open 1 dirty, save, open 2, Escape closes', async () => {
    const listRoutes = vi.fn().mockResolvedValue([buildRoute({ id: 1 }), buildRoute({ id: 2 })]);
    const updateRoute = vi.fn().mockResolvedValue(buildRoute({ id: 1 }));
    const client = buildClient({ listRoutes, updateRoute });
    const user = userEvent.setup();
    renderSection({ client });
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(button('Save route 1'));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());

    await user.click(button('Edit route 2'));
    expect(screen.getByRole('dialog', { name: strings.editRoute({ id: 2 }) })).toBeInTheDocument();
    await user.keyboard('{Escape}');

    expect(screen.queryByText(strings.discardChangesMessage)).toBeNull();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('a failed save keeps the dialog open with the error and the typed values', async () => {
    const updateRoute = vi.fn().mockRejectedValue(new Error('boom'));
    const client = buildClient({
      listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]),
      updateRoute,
    });
    const user = await openEditor(client);
    await user.click(screen.getByLabelText('family_ntfy'));
    await user.click(button('Save route 1'));

    await waitFor(() => expect(updateRoute).toHaveBeenCalled());
    const dialog = screen.getByRole('dialog', { name: strings.editRoute({ id: 1 }) });
    expect(await within(dialog).findByRole('alert')).toBeInTheDocument();
    expect(screen.getByLabelText('family_ntfy')).toBeChecked();
  });
});

describe('delete route', () => {
  it('confirms in a dialog naming the route, keeps the row buttons, and runs the delete once', async () => {
    const user = userEvent.setup();
    const listRoutes = vi.fn().mockResolvedValue([buildRoute({ id: 1 }), buildRoute({ id: 2 })]);
    const client = buildClient({ listRoutes });
    renderSection({ client });
    await user.click(await screen.findByRole('button', { name: 'Delete route 1' }));

    const dialog = screen.getByRole('dialog', { name: strings.deleteRouteDialogTitle });
    expect(within(dialog).getByText(strings.dialogItemRoute)).toBeInTheDocument();
    expect(within(dialog).getByText('route 1')).toBeInTheDocument();
    expect(
      within(dialog).getByRole('button', {
        name: strings.cancelActionNamed({ subject: 'Delete route 1' }),
      }),
    ).toHaveFocus();
    expect(screen.getByRole('button', { name: 'Edit route 1', hidden: true })).toBeVisible();

    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(button('Delete route 1')).toHaveFocus();
    expect(client.deleteRoute).not.toHaveBeenCalled();

    await user.click(button('Delete route 1'));
    await user.click(button('Confirm delete route 1'));
    expect(client.deleteRoute).toHaveBeenCalledTimes(1);
    expect(client.deleteRoute).toHaveBeenCalledWith({ id: 1 });
  });

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

/** Owns the destinations state the way App does, since DeliveryRegistry receives it as props. */
function RegistryHost({ client }: { client: DeliveryApiClient }) {
  const [destinationsState, setDestinationsState] = useState<DestinationsLoadState>({
    status: 'loading',
    destinations: [],
  });
  return (
    <DeliveryRegistry
      client={client}
      destinationsState={destinationsState}
      onDestinationsStateChange={setDestinationsState}
    />
  );
}

describe('DeliveryRegistry destinations reporting', () => {
  it('reports the initial load and every later refresh through onDestinationsStateChange', async () => {
    const user = userEvent.setup();
    const onDestinationsStateChange = vi.fn();
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([buildDestination('ops_discord')])
      .mockResolvedValue([buildDestination('ops_discord', false)]);
    const updateDestination = vi.fn().mockResolvedValue(buildDestination('ops_discord', false));
    render(
      <DeliveryRegistry
        client={buildClient({ listDestinations, updateDestination })}
        destinationsState={{ status: 'ready', destinations: [] }}
        onDestinationsStateChange={onDestinationsStateChange}
      />,
    );

    expect(onDestinationsStateChange).toHaveBeenCalledWith({ status: 'loading', destinations: [] });
    await waitFor(() =>
      expect(onDestinationsStateChange).toHaveBeenLastCalledWith({
        status: 'ready',
        destinations: [expect.objectContaining({ name: 'ops_discord', enabled: true })],
      }),
    );

    await user.click(await screen.findByRole('button', { name: 'Disable ops_discord' }));
    await user.click(button('Confirm disable ops_discord'));

    await waitFor(() =>
      expect(onDestinationsStateChange).toHaveBeenLastCalledWith({
        status: 'ready',
        destinations: [expect.objectContaining({ name: 'ops_discord', enabled: false })],
      }),
    );
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
    render(<RegistryHost client={client} />);
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

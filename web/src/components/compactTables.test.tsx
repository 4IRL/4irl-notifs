import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { UserEvent } from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type {
  DeliveryApiClient,
  DestinationSummary,
  HeartbeatCheck,
  IdentityBinding,
  IdentityKey,
  IdentityRequest,
  RouteSummary,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { accessibleNameOf, labelInNameViolations } from '../test-labels';
import { setCompactViewport } from '../test-viewport';
import { AppIdentities } from './AppIdentities';
import { DestinationsSection } from './DestinationsSection';
import { Heartbeats } from './Heartbeats';
import { RoutesSection } from './RoutesSection';
import type { DestinationsLoadState } from './RoutesSection';

// The narrow layout (viewport <= 30rem) swaps every admin table row's inline buttons for one
// kebab menu. These tests drive each of the four tables through its menu, by keyboard where
// it matters, and check the roomy layout still has its inline buttons and no kebab.

function buildClient(overrides: Partial<DeliveryApiClient> = {}): DeliveryApiClient {
  return {
    getReadiness: vi.fn(),
    listDestinations: vi.fn().mockResolvedValue([]),
    createDestination: vi.fn(),
    updateDestination: vi.fn().mockResolvedValue(undefined),
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

function kebab(row: string): HTMLElement {
  return screen.getByRole('button', { name: `Actions for ${row}` });
}

function allKebabs(): HTMLElement[] {
  return screen.queryAllByRole('button', { name: /^Actions for / });
}

/** Opens a row's menu with the keyboard, as a keyboard user would. */
async function openMenu({ user, row }: { user: UserEvent; row: string }): Promise<HTMLElement> {
  kebab(row).focus();
  await user.keyboard('{Enter}');
  return screen.getByRole('menu', { name: `Actions for ${row}` });
}

async function chooseItem({ user, name }: { user: UserEvent; name: string }) {
  await user.click(screen.getByRole('menuitem', { name }));
}

function columnTitles(): string[] {
  return screen
    .getAllByRole('columnheader')
    .map((header) => header.getAttribute('aria-label') ?? header.textContent ?? '');
}

// ---------------------------------------------------------------------------------------
// Destinations
// ---------------------------------------------------------------------------------------

function buildDestination(
  overrides: Partial<DestinationSummary> & { name: string },
): DestinationSummary {
  return {
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

const APP_OWNED = {
  ownerKind: 'app',
  ownerAppId: 'chores4irl',
} as const;

function renderDestinations(client: DeliveryApiClient) {
  return render(<DestinationsSection client={client} />);
}

describe('Destinations table, narrow layout', () => {
  beforeEach(() => {
    setCompactViewport(true);
  });

  it('has one kebab per row and no inline buttons, with state before type and target and no Owner column', async () => {
    const client = buildClient({
      listDestinations: vi
        .fn()
        .mockResolvedValue([
          buildDestination({ name: 'family_ntfy' }),
          buildDestination({ name: 'chores_hook', enabled: false }),
        ]),
    });
    renderDestinations(client);
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });

    expect(allKebabs()).toHaveLength(2);
    expect(
      screen.queryAllByRole('button', {
        name: /^(Send test to|Edit|Close|Disable|Enable|Delete) /,
      }),
    ).toHaveLength(0);
    expect(columnTitles()).toEqual([
      strings.columnDestName,
      strings.columnDestStatus,
      strings.columnDestType,
      strings.columnDestTarget,
      strings.columnDestActions,
    ]);
    // The kebab is the whole Actions cell.
    expect(kebab('family_ntfy').closest('td')?.children).toHaveLength(1);
  });

  it('offers every action in the menu: Send test, Edit, Disable and Delete (Enable when off)', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listDestinations: vi
        .fn()
        .mockResolvedValue([
          buildDestination({ name: 'family_ntfy' }),
          buildDestination({ name: 'chores_hook', enabled: false }),
        ]),
    });
    renderDestinations(client);
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });

    await openMenu({ user, row: 'family_ntfy' });
    // The menu is named for its row, so the items read as the bare verb; each one's
    // accessible name still carries the row.
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Test',
      'Edit',
      'Disable',
      'Delete',
    ]);
    expect(screen.getAllByRole('menuitem').map((item) => accessibleNameOf(item))).toEqual([
      'Send test to family_ntfy',
      'Edit family_ntfy',
      'Disable family_ntfy',
      'Delete family_ntfy',
    ]);
    await user.keyboard('{Escape}');

    await openMenu({ user, row: 'chores_hook' });
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Test',
      'Edit',
      'Enable',
      'Delete',
    ]);
    expect(screen.getAllByRole('menuitem').map((item) => accessibleNameOf(item))).toEqual([
      'Send test to chores_hook',
      'Edit chores_hook',
      'Enable chores_hook',
      'Delete chores_hook',
    ]);
    // A disabled destination cannot be tested.
    expect(screen.getByRole('menuitem', { name: 'Send test to chores_hook' })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
  });

  it('send test runs from the menu and its result reads in the row, not in the menu', async () => {
    const user = userEvent.setup();
    const sendTest = vi.fn().mockResolvedValue({
      deliveries: [{ state: 'delivered', lastStatusCode: 200, deadReason: null }],
    });
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'family_ntfy' })]),
      sendTest,
    });
    renderDestinations(client);
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });

    await openMenu({ user, row: 'family_ntfy' });
    await chooseItem({ user, name: 'Send test to family_ntfy' });

    expect(sendTest).toHaveBeenCalledWith({ destination: 'family_ntfy' });
    expect(screen.queryByRole('menu')).toBeNull();
    const row = kebab('family_ntfy').closest('tr') as HTMLElement;
    expect(await within(row).findByText(strings.testStateDelivered)).toBeInTheDocument();
    expect(within(row).getByText('HTTP 200')).toBeInTheDocument();
    expect(kebab('family_ntfy')).toHaveFocus();
  });

  it('Disable asks for confirmation inside the menu, then disables', async () => {
    const user = userEvent.setup();
    const updateDestination = vi.fn().mockResolvedValue(undefined);
    const client = buildClient({
      listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'family_ntfy' })]),
      updateDestination,
    });
    renderDestinations(client);
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });

    await openMenu({ user, row: 'family_ntfy' });
    await chooseItem({ user, name: 'Disable family_ntfy' });

    expect(updateDestination).not.toHaveBeenCalled();
    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.getByRole('dialog', { name: 'Disable destination?' })).toHaveTextContent(
      'family_ntfy',
    );
    // Cancel closes the dialog and returns to the kebab.
    await user.click(screen.getByRole('button', { name: 'Cancel Disable family_ntfy' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(kebab('family_ntfy')).toHaveFocus();
    expect(updateDestination).not.toHaveBeenCalled();

    await openMenu({ user, row: 'family_ntfy' });

    await chooseItem({ user, name: 'Disable family_ntfy' });
    await user.click(screen.getByRole('button', { name: 'Confirm disable family_ntfy' }));

    await waitFor(() =>
      expect(updateDestination).toHaveBeenCalledWith({ name: 'family_ntfy', enabled: false }),
    );
    expect(updateDestination).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('Enable is a single selection', async () => {
    const user = userEvent.setup();
    const updateDestination = vi.fn().mockResolvedValue(undefined);
    const client = buildClient({
      listDestinations: vi
        .fn()
        .mockResolvedValue([buildDestination({ name: 'chores_hook', enabled: false })]),
      updateDestination,
    });
    renderDestinations(client);
    await screen.findByRole('button', { name: 'Actions for chores_hook' });

    await openMenu({ user, row: 'chores_hook' });
    await chooseItem({ user, name: 'Enable chores_hook' });

    await waitFor(() =>
      expect(updateDestination).toHaveBeenCalledWith({ name: 'chores_hook', enabled: true }),
    );
  });

  it('Delete confirms inside the menu with the owner note, then hands focus to the next kebab', async () => {
    const user = userEvent.setup();
    const deleteDestination = vi.fn().mockResolvedValue(undefined);
    const listDestinations = vi
      .fn()
      .mockResolvedValueOnce([
        buildDestination({ name: 'family_ntfy', ...APP_OWNED }),
        buildDestination({ name: 'ops_discord' }),
      ])
      .mockResolvedValue([buildDestination({ name: 'ops_discord' })]);
    renderDestinations(buildClient({ listDestinations, deleteDestination }));
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });

    const menu = await openMenu({ user, row: 'family_ntfy' });
    // The short owner note is the menu's first, non-interactive line (the long one is for the confirm).
    expect(menu.firstElementChild).toHaveAttribute('role', 'presentation');
    expect(menu.firstElementChild).toHaveTextContent(strings.ownerManagedInlineNote);
    expect(menu).not.toHaveTextContent(strings.ownerManagedNote({ appId: 'chores4irl' }));

    await chooseItem({ user, name: 'Delete family_ntfy' });
    expect(screen.getByRole('dialog', { name: 'Delete destination?' })).toHaveTextContent(
      strings.ownerManagedNote({ appId: 'chores4irl' }),
    );
    expect(deleteDestination).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Confirm delete family_ntfy' }));

    await waitFor(() => expect(deleteDestination).toHaveBeenCalledWith({ name: 'family_ntfy' }));
    expect(deleteDestination).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(kebab('ops_discord')).toHaveFocus());
    expect(screen.queryByRole('button', { name: 'Actions for family_ntfy' })).toBeNull();
  });

  it('Escape in the confirm dialog closes it without firing, back on the kebab', async () => {
    const user = userEvent.setup();
    const deleteDestination = vi.fn();
    renderDestinations(
      buildClient({
        listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'family_ntfy' })]),
        deleteDestination,
      }),
    );
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });
    await openMenu({ user, row: 'family_ntfy' });
    await chooseItem({ user, name: 'Delete family_ntfy' });

    expect(screen.getByRole('dialog', { name: 'Delete destination?' })).toBeInTheDocument();
    await user.keyboard('{Escape}');

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab('family_ntfy')).toHaveFocus();
    expect(deleteDestination).not.toHaveBeenCalled();
  });

  it('a failed action leaves the server error on the kebab and in the menu', async () => {
    const user = userEvent.setup();
    const deleteDestination = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 500, message: 'delete blew up' }));
    renderDestinations(
      buildClient({
        listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'family_ntfy' })]),
        deleteDestination,
      }),
    );
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });
    await openMenu({ user, row: 'family_ntfy' });
    await chooseItem({ user, name: 'Delete family_ntfy' });
    await user.click(screen.getByRole('button', { name: 'Confirm delete family_ntfy' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('delete blew up');
    expect(kebab('family_ntfy')).toHaveAttribute('data-has-error', 'true');
    expect(kebab('family_ntfy')).toHaveFocus();

    const menu = await openMenu({ user, row: 'family_ntfy' });
    expect(menu).toHaveTextContent('delete blew up');
  });

  it('Edit opens the dialog and moves focus into it; the menu keeps offering Edit', async () => {
    const user = userEvent.setup();
    renderDestinations(
      buildClient({
        listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'family_ntfy' })]),
      }),
    );
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });

    await openMenu({ user, row: 'family_ntfy' });
    await chooseItem({ user, name: 'Edit family_ntfy' });

    const dialog = screen.getByRole('dialog', { name: 'Edit destination family_ntfy' });
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
    expect(dialog.closest('tr')).toBeNull();
  });
});

describe('Destinations table, roomy layout', () => {
  it('keeps the inline buttons and has no kebab', async () => {
    renderDestinations(
      buildClient({
        listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'family_ntfy' })]),
      }),
    );

    expect(await screen.findByRole('button', { name: 'Send test to family_ntfy' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Delete family_ntfy' })).toBeVisible();
    expect(allKebabs()).toHaveLength(0);
    expect(columnTitles().slice(0, 5)).toEqual([
      strings.columnDestName,
      strings.columnOwner,
      strings.columnDestType,
      strings.columnDestTarget,
      strings.columnDestStatus,
    ]);
  });

  it('swaps layouts while mounted, never rendering both sets of controls', async () => {
    setCompactViewport(true);
    renderDestinations(
      buildClient({
        listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'family_ntfy' })]),
      }),
    );
    await screen.findByRole('button', { name: 'Actions for family_ntfy' });
    expect(screen.queryByRole('button', { name: 'Delete family_ntfy' })).toBeNull();

    setCompactViewport(false);
    expect(await screen.findByRole('button', { name: 'Delete family_ntfy' })).toBeInTheDocument();
    expect(allKebabs()).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------------------

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

const ROUTES_READY: DestinationsLoadState = {
  status: 'ready',
  destinations: [
    buildDestination({ name: 'ops_discord' }),
    buildDestination({ name: 'family_ntfy' }),
  ],
};

function renderRoutes(client: DeliveryApiClient) {
  return render(
    <RoutesSection client={client} destinationsState={ROUTES_READY} refreshSignal={0} />,
  );
}

describe('Routes table, narrow layout', () => {
  beforeEach(() => {
    setCompactViewport(true);
  });

  it('has one kebab per row, no inline buttons, and state before the destinations, with no Owner column', async () => {
    renderRoutes(
      buildClient({
        listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 }), buildRoute({ id: 2 })]),
      }),
    );
    await screen.findByRole('button', { name: 'Actions for route 1' });

    expect(allKebabs()).toHaveLength(2);
    expect(screen.queryAllByRole('button', { name: /^(Edit|Close|Delete) route/ })).toHaveLength(0);
    expect(columnTitles()).toEqual([
      strings.columnRouteApp,
      strings.columnRouteChannel,
      strings.columnRouteStatus,
      strings.columnRouteDestinations,
      strings.columnRouteActions,
    ]);
  });

  it('Edit opens the editor with focus in it; Delete confirms inside the menu', async () => {
    const user = userEvent.setup();
    const deleteRoute = vi.fn().mockResolvedValue(undefined);
    renderRoutes(
      buildClient({
        listRoutes: vi
          .fn()
          .mockResolvedValueOnce([buildRoute({ id: 1 }), buildRoute({ id: 2 })])
          .mockResolvedValue([buildRoute({ id: 2 })]),
        deleteRoute,
      }),
    );
    await screen.findByRole('button', { name: 'Actions for route 1' });

    await openMenu({ user, row: 'route 1' });
    await chooseItem({ user, name: 'Edit route 1' });
    expect(screen.getByRole('dialog', { name: strings.editRoute({ id: 1 }) })).toBeInTheDocument();
    expect(document.activeElement).toBe(screen.getByLabelText(strings.routeAppLabel));

    // Closing the dialog hands focus back to the kebab.
    await user.keyboard('{Escape}');
    await waitFor(() => expect(kebab('route 1')).toHaveFocus());

    await openMenu({ user, row: 'route 1' });
    await chooseItem({ user, name: 'Delete route 1' });
    expect(screen.getByRole('dialog', { name: 'Delete route?' })).toBeInTheDocument();
    expect(deleteRoute).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Confirm delete route 1' }));

    await waitFor(() => expect(deleteRoute).toHaveBeenCalledWith({ id: 1 }));
    await waitFor(() => expect(kebab('route 2')).toHaveFocus());
  });

  it('shows the short owner note in the menu for an app-owned route', async () => {
    const user = userEvent.setup();
    renderRoutes(
      buildClient({
        listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1, ...APP_OWNED })]),
      }),
    );
    await screen.findByRole('button', { name: 'Actions for route 1' });

    const menu = await openMenu({ user, row: 'route 1' });

    expect(menu.firstElementChild).toHaveTextContent(strings.ownerManagedInlineNote);
    expect(menu).not.toHaveTextContent(strings.ownerManagedNote({ appId: 'chores4irl' }));
  });
});

describe('Routes table, roomy layout', () => {
  it('keeps the inline buttons and has no kebab', async () => {
    renderRoutes(buildClient({ listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]) }));

    expect(await screen.findByRole('button', { name: 'Edit route 1' })).toBeInTheDocument();
    expect(allKebabs()).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------------------
// Heartbeats
// ---------------------------------------------------------------------------------------

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
    downReason: null,
    probe: null,
    createdAt: '2026-10-17T10:00:00Z',
    updatedAt: '2026-10-18T08:01:00Z',
    ...overrides,
  };
}

const HEARTBEAT_DESTINATIONS: DestinationsLoadState = {
  status: 'ready',
  destinations: [buildDestination({ name: 'ops_discord', type: 'discord' })],
};

function renderHeartbeats(client: DeliveryApiClient) {
  return render(<Heartbeats client={client} destinationsState={HEARTBEAT_DESTINATIONS} />);
}

const CHECK_1 = buildCheck({ id: 1 });
const CHECK_2 = buildCheck({ id: 2 });
const ROW_1 = 'urls4irl/check_1';
const ROW_2 = 'urls4irl/check_2';

describe('Heartbeats table, narrow layout', () => {
  beforeEach(() => {
    setCompactViewport(true);
  });

  it('has one kebab per row and no inline row buttons', async () => {
    renderHeartbeats(
      buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1, CHECK_2]) }),
    );
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });

    expect(allKebabs()).toHaveLength(2);
    expect(
      screen.queryAllByRole('button', { name: /^(Edit|Close|Pause|Resume|Delete) urls4irl/ }),
    ).toHaveLength(0);
    // The section's own buttons are not row actions and stay.
    expect(screen.getByRole('button', { name: strings.addHeartbeatAction })).toBeInTheDocument();
    expect(columnTitles().slice(0, 3)).toEqual([
      strings.columnHeartbeatCheck,
      strings.columnHeartbeatState,
      strings.columnHeartbeatLastPing,
    ]);
    expect(columnTitles()).not.toContain(strings.columnOwner);
  });

  it('Pause confirms inside the menu with its notice, then the menu offers Resume, one step', async () => {
    const user = userEvent.setup();
    const pauseHeartbeat = vi.fn().mockResolvedValue({ ...CHECK_1, paused: true });
    const resumeHeartbeat = vi.fn().mockResolvedValue(CHECK_1);
    // The server's view follows the calls made so far, however many reloads follow each one.
    const listHeartbeats = vi.fn().mockImplementation(async () => {
      const paused = pauseHeartbeat.mock.calls.length > resumeHeartbeat.mock.calls.length;
      return [paused ? { ...CHECK_1, paused: true } : CHECK_1, CHECK_2];
    });
    renderHeartbeats(buildClient({ listHeartbeats, pauseHeartbeat, resumeHeartbeat }));
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });

    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Pause ${ROW_1}` });
    expect(screen.getByRole('dialog', { name: 'Pause check?' })).toHaveTextContent(
      strings.pauseHeartbeatNotice,
    );
    expect(pauseHeartbeat).not.toHaveBeenCalled();
    // Keep running closes the dialog and returns to the kebab.
    await user.click(screen.getByRole('button', { name: `Keep running ${ROW_1}` }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(kebab(ROW_1)).toHaveFocus();

    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Pause ${ROW_1}` });
    await user.click(screen.getByRole('button', { name: `Confirm pause ${ROW_1}` }));

    await waitFor(() => expect(pauseHeartbeat).toHaveBeenCalledWith({ id: 1 }));
    expect(pauseHeartbeat).toHaveBeenCalledTimes(1);
    // Focus is on the row's kebab (it holds the Pause and Resume focus keys).
    await waitFor(() => expect(kebab(ROW_1)).toHaveFocus());
    expect(await screen.findByText(strings.heartbeatStatePaused)).toBeInTheDocument();

    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Resume ${ROW_1}` });
    await waitFor(() => expect(resumeHeartbeat).toHaveBeenCalledWith({ id: 1 }));
    await waitFor(() => expect(kebab(ROW_1)).toHaveFocus());
  });

  it('Delete confirms inside the menu and hands focus to the neighbour kebab', async () => {
    const user = userEvent.setup();
    const deleteHeartbeat = vi.fn().mockResolvedValue(undefined);
    const listHeartbeats = vi
      .fn()
      .mockResolvedValueOnce([CHECK_1, CHECK_2])
      .mockResolvedValue([CHECK_2]);
    renderHeartbeats(buildClient({ listHeartbeats, deleteHeartbeat }));
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });

    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Delete ${ROW_1}` });
    await user.click(screen.getByRole('button', { name: `Confirm delete ${ROW_1}` }));

    await waitFor(() => expect(deleteHeartbeat).toHaveBeenCalledWith({ id: 1 }));
    expect(deleteHeartbeat).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(kebab(ROW_2)).toHaveFocus());
  });

  it('puts the short owner note first in the menu and the long one in the pause and delete confirms', async () => {
    const user = userEvent.setup();
    const owned = buildCheck({ id: 1, ...APP_OWNED });
    renderHeartbeats(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([owned]) }));
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });
    const note = strings.ownerManagedNote({ appId: 'chores4irl' });

    const menu = await openMenu({ user, row: ROW_1 });
    expect(menu.firstElementChild).toHaveTextContent(strings.ownerManagedInlineNote);
    expect(menu).not.toHaveTextContent(note);

    await chooseItem({ user, name: `Pause ${ROW_1}` });
    expect(screen.getByRole('dialog', { name: 'Pause check?' })).toHaveTextContent(note);
    await user.keyboard('{Escape}');
    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Delete ${ROW_1}` });
    expect(screen.getByRole('dialog', { name: 'Delete check?' })).toHaveTextContent(note);
  });

  it('Edit opens the dialog with focus in it and returns focus to the kebab on close', async () => {
    const user = userEvent.setup();
    renderHeartbeats(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]) }));
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });

    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Edit ${ROW_1}` });

    const dialog = screen.getByRole('dialog', { name: `Edit ${ROW_1}` });
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
    await user.keyboard('{Escape}');
    await waitFor(() => expect(kebab(ROW_1)).toHaveFocus());
  });

  it('a failed pause puts the server error on the kebab and in the menu', async () => {
    const user = userEvent.setup();
    const pauseHeartbeat = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 500, message: 'checker is down' }));
    renderHeartbeats(
      buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]), pauseHeartbeat }),
    );
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });
    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Pause ${ROW_1}` });
    await user.click(screen.getByRole('button', { name: `Confirm pause ${ROW_1}` }));

    expect(await screen.findByRole('alert')).toHaveTextContent('checker is down');
    expect(kebab(ROW_1)).toHaveAttribute('data-has-error', 'true');
    const menu = await openMenu({ user, row: ROW_1 });
    expect(menu).toHaveTextContent('checker is down');
  });
});

describe('Heartbeats table, roomy layout', () => {
  it('keeps the inline buttons and has no kebab', async () => {
    renderHeartbeats(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]) }));

    expect(await screen.findByRole('button', { name: `Pause ${ROW_1}` })).toBeInTheDocument();
    expect(allKebabs()).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------------------
// App identities
// ---------------------------------------------------------------------------------------

function buildRequest(overrides: Partial<IdentityRequest> & { id: number }): IdentityRequest {
  return {
    appId: `app_${overrides.id}`,
    githubRepository: `4IRL/app_${overrides.id}`,
    githubRepositoryId: 812345600 + overrides.id,
    workflowPath: '.github/workflows/prod-deploy.yml',
    gitRef: 'refs/heads/main',
    environment: 'production',
    githubRunId: '14022',
    status: 'pending',
    createdAt: '2026-10-21T18:04:00Z',
    updatedAt: '2026-10-21T18:04:00Z',
    ...overrides,
  };
}

function buildKey(overrides: Partial<IdentityKey> & { id: number; appId: string }): IdentityKey {
  return {
    keyHint: `dak_k${overrides.id}ab`,
    state: 'active',
    createdAt: '2026-10-21T16:40:00Z',
    supersededAt: null,
    revokedAt: null,
    revokedReason: null,
    lastUsedAt: null,
    ...overrides,
  };
}

function buildBinding(
  overrides: Partial<IdentityBinding> & { appId: string; keys?: IdentityKey[] },
): IdentityBinding {
  return {
    githubRepository: `4IRL/${overrides.appId}`,
    githubRepositoryId: 812345678,
    githubOwnerId: 55,
    workflowPath: '.github/workflows/prod-deploy.yml',
    gitRef: 'refs/heads/main',
    environment: 'production',
    approvedAt: '2026-10-19T09:12:00Z',
    keys: [],
    ...overrides,
  };
}

const KEY_ACTIVE = buildKey({ id: 11, appId: 'tasktracker', keyHint: 'dak_7Q2m' });
const KEY_SUPERSEDED = buildKey({
  id: 12,
  appId: 'tasktracker',
  keyHint: 'dak_Kf09',
  state: 'superseded',
});
const KEY_REVOKED = buildKey({
  id: 13,
  appId: 'tasktracker',
  keyHint: 'dak_a1Zx',
  state: 'revoked',
});
const BINDING = buildBinding({
  appId: 'tasktracker',
  keys: [KEY_ACTIVE, KEY_SUPERSEDED, KEY_REVOKED],
});

function renderIdentities(client: DeliveryApiClient) {
  return render(<AppIdentities client={client} />);
}

describe('App identities tables, narrow layout', () => {
  beforeEach(() => {
    setCompactViewport(true);
  });

  it('gives a pending request a kebab with Approve and Reject, and no inline buttons', async () => {
    const user = userEvent.setup();
    renderIdentities(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([buildRequest({ id: 1 })]),
      }),
    );
    await screen.findByRole('button', { name: 'Actions for app_1' });

    expect(screen.queryByRole('button', { name: /^(Approve|Reject) identity request/ })).toBeNull();
    await openMenu({ user, row: 'app_1' });
    expect(screen.getAllByRole('menuitem').map((item) => item.getAttribute('aria-label'))).toEqual([
      'Approve identity request for app_1',
      'Reject identity request for app_1',
    ]);
  });

  it('Approve is one selection', async () => {
    const user = userEvent.setup();
    const approveIdentityRequest = vi.fn().mockResolvedValue(buildBinding({ appId: 'app_1' }));
    renderIdentities(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([buildRequest({ id: 1 })])
          .mockResolvedValue([]),
        approveIdentityRequest,
      }),
    );
    await screen.findByRole('button', { name: 'Actions for app_1' });

    await openMenu({ user, row: 'app_1' });
    await chooseItem({ user, name: 'Approve identity request for app_1' });

    await waitFor(() => expect(approveIdentityRequest).toHaveBeenCalledWith({ id: 1 }));
    expect(approveIdentityRequest).toHaveBeenCalledTimes(1);
  });

  it('Reject confirms inside the menu with its notice, then rejects', async () => {
    const user = userEvent.setup();
    const rejectIdentityRequest = vi
      .fn()
      .mockResolvedValue(buildRequest({ id: 1, status: 'rejected' }));
    renderIdentities(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([buildRequest({ id: 1 })]),
        rejectIdentityRequest,
      }),
    );
    await screen.findByRole('button', { name: 'Actions for app_1' });

    await openMenu({ user, row: 'app_1' });
    await chooseItem({ user, name: 'Reject identity request for app_1' });
    expect(screen.getByRole('dialog', { name: 'Reject identity request?' })).toHaveTextContent(
      strings.rejectIdentityNotice,
    );
    expect(rejectIdentityRequest).not.toHaveBeenCalled();
    await user.click(
      screen.getByRole('button', { name: 'Confirm reject identity request for app_1' }),
    );

    await waitFor(() => expect(rejectIdentityRequest).toHaveBeenCalledWith({ id: 1 }));
    expect(rejectIdentityRequest).toHaveBeenCalledTimes(1);
  });

  it('a rejected or unrecognised request has no kebab; the unknown status explains itself under its pill', async () => {
    renderIdentities(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValue([
            buildRequest({ id: 1, status: 'rejected' }),
            buildRequest({ id: 2, status: 'mystery' as IdentityRequest['status'] }),
          ]),
      }),
    );
    await screen.findByText(strings.identityRequestRejected);

    expect(allKebabs()).toHaveLength(0);
    expect(screen.getByText(strings.identityRequestUnknownSub)).toBeInTheDocument();
  });

  it('puts Unbind and every revocable key in the binding menu, and no Revoke in the key rows', async () => {
    const user = userEvent.setup();
    renderIdentities(buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([BINDING]) }));
    await screen.findByRole('button', { name: 'Actions for tasktracker' });

    expect(screen.queryByRole('button', { name: /^(Revoke|Unbind)/ })).toBeNull();
    // The key rows still read in full: pill, hint, last use.
    expect(screen.getByText('dak_7Q2m…')).toBeInTheDocument();

    await openMenu({ user, row: 'tasktracker' });
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Revoke key dak_7Q2m for tasktracker',
      'Revoke key dak_Kf09 for tasktracker',
      'Unbind tasktracker',
    ]);
  });

  it('Revoke key and Unbind confirm inside the menu, then run', async () => {
    const user = userEvent.setup();
    const revokeIdentityKey = vi.fn().mockResolvedValue({ ...KEY_ACTIVE, state: 'revoked' });
    const unbindApp = vi.fn().mockResolvedValue({ deleted: true });
    renderIdentities(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING]),
        revokeIdentityKey,
        unbindApp,
      }),
    );
    await screen.findByRole('button', { name: 'Actions for tasktracker' });

    await openMenu({ user, row: 'tasktracker' });
    await chooseItem({ user, name: 'Revoke key dak_7Q2m for tasktracker' });
    expect(screen.getByRole('dialog', { name: 'Revoke key?' })).toHaveTextContent(
      strings.revokeKeyNotice,
    );
    expect(revokeIdentityKey).not.toHaveBeenCalled();
    await user.click(
      screen.getByRole('button', { name: 'Confirm revoke key dak_7Q2m for tasktracker' }),
    );
    await waitFor(() => expect(revokeIdentityKey).toHaveBeenCalledWith({ id: 11 }));
    expect(revokeIdentityKey).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(kebab('tasktracker')).toHaveFocus());

    await openMenu({ user, row: 'tasktracker' });
    await chooseItem({ user, name: 'Unbind tasktracker' });
    expect(screen.getByRole('dialog', { name: 'Unbind app?' })).toHaveTextContent(
      strings.unbindAppNotice,
    );
    await user.click(screen.getByRole('button', { name: 'Confirm unbind tasktracker' }));
    await waitFor(() => expect(unbindApp).toHaveBeenCalledWith({ appId: 'tasktracker' }));
    expect(unbindApp).toHaveBeenCalledTimes(1);
  });

  it('a failed revoke shows its error on the app kebab and in the menu', async () => {
    const user = userEvent.setup();
    const revokeIdentityKey = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 500, message: 'revoke refused' }));
    renderIdentities(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING]),
        revokeIdentityKey,
      }),
    );
    await screen.findByRole('button', { name: 'Actions for tasktracker' });
    await openMenu({ user, row: 'tasktracker' });
    await chooseItem({ user, name: 'Revoke key dak_7Q2m for tasktracker' });
    await user.click(
      screen.getByRole('button', { name: 'Confirm revoke key dak_7Q2m for tasktracker' }),
    );

    expect(await screen.findByRole('alert')).toHaveTextContent('revoke refused');
    expect(kebab('tasktracker')).toHaveAttribute('data-has-error', 'true');
    const menu = await openMenu({ user, row: 'tasktracker' });
    expect(menu).toHaveTextContent('revoke refused');
  });
});

describe('App identities tables, roomy layout', () => {
  it('keeps the inline buttons and has no kebab', async () => {
    renderIdentities(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([buildRequest({ id: 1 })]),
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING]),
      }),
    );

    expect(
      await screen.findByRole('button', { name: 'Approve identity request for app_1' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Unbind tasktracker' })).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'Revoke key dak_7Q2m for tasktracker' }),
    ).toBeInTheDocument();
    expect(allKebabs()).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------------------
// Row action labels: a row already names its object, so an inline button shows the bare verb;
// its accessible name keeps the object, and always contains the visible text (WCAG 2.5.3).
// ---------------------------------------------------------------------------------------

/** What each button of a table row shows, and what it is called to assistive technology. */
function rowButtons(row: HTMLElement): { visible: string[]; names: string[] } {
  const buttons = within(row).getAllByRole('button');
  return {
    visible: buttons.map((button) => button.textContent ?? ''),
    names: buttons.map((button) => accessibleNameOf(button)),
  };
}

/** The dialog's X shows a glyph, not a word, so its "Close" name is exempt from label-in-name. */
function exceptModalClose(buttons: HTMLElement[]): HTMLElement[] {
  return buttons.filter((button) => !button.classList.contains('modal__close'));
}

function rowOfButton(name: string): HTMLElement {
  return screen.getByRole('button', { name }).closest('tr') as HTMLElement;
}

describe('Row action labels, roomy layout', () => {
  it('Destinations: the row buttons read Test, Edit, Disable, Delete and keep the destination in their names', async () => {
    const user = userEvent.setup();
    renderDestinations(
      buildClient({
        listDestinations: vi
          .fn()
          .mockResolvedValue([
            buildDestination({ name: 'family_ntfy' }),
            buildDestination({ name: 'chores_hook', enabled: false }),
          ]),
      }),
    );
    await screen.findByRole('button', { name: 'Send test to family_ntfy' });

    const enabled = rowButtons(rowOfButton('Send test to family_ntfy'));
    expect(enabled.visible).toEqual(['Test', 'Edit', 'Disable', 'Delete']);
    expect(enabled.names).toEqual([
      'Send test to family_ntfy',
      'Edit family_ntfy',
      'Disable family_ntfy',
      'Delete family_ntfy',
    ]);
    const disabled = rowButtons(rowOfButton('Send test to chores_hook'));
    expect(disabled.visible).toEqual(['Test', 'Edit', 'Enable', 'Delete']);
    expect(disabled.names).toEqual([
      'Send test to chores_hook',
      'Edit chores_hook',
      'Enable chores_hook',
      'Delete chores_hook',
    ]);
    expect(labelInNameViolations(screen.getAllByRole('button'))).toEqual([]);

    // The open dialog leaves the row's Edit label alone; closing returns to the row.
    await user.click(screen.getByRole('button', { name: 'Edit family_ntfy' }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    await user.keyboard('{Escape}');

    // Each confirm dialog: the verb is still short and the name still carries the destination.
    for (const verb of ['Disable', 'Delete']) {
      await user.click(screen.getByRole('button', { name: `${verb} family_ntfy` }));
      const dialog = screen.getByRole('dialog');
      const pairButtons = within(dialog)
        .getAllByRole('button')
        .filter((button) => /^(Confirm|Cancel)/.test(button.textContent ?? ''));
      expect(pairButtons.map((button) => button.textContent)).toEqual([
        'Cancel',
        `Confirm ${verb.toLowerCase()}`,
      ]);
      expect(pairButtons.map((button) => accessibleNameOf(button))).toEqual([
        `Cancel ${verb} family_ntfy`,
        `Confirm ${verb.toLowerCase()} family_ntfy`,
      ]);
      expect(labelInNameViolations(pairButtons)).toEqual([]);
      await user.click(screen.getByRole('button', { name: `Cancel ${verb} family_ntfy` }));
    }
  });

  it('Routes: the row buttons read Edit and Delete and keep the route in their names', async () => {
    const user = userEvent.setup();
    renderRoutes(buildClient({ listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]) }));
    await screen.findByRole('button', { name: 'Edit route 1' });

    const idle = rowButtons(rowOfButton('Edit route 1'));
    expect(idle.visible).toEqual(['Edit', 'Delete']);
    expect(idle.names).toEqual(['Edit route 1', 'Delete route 1']);

    await user.click(screen.getByRole('button', { name: 'Edit route 1' }));
    // Edit opens a dialog: the row's buttons are unchanged, never a Close toggle.
    expect(screen.getByRole('dialog', { name: 'Edit route 1' })).toBeInTheDocument();
    const editing = rowButtons(rowOfButton('Edit route 1'));
    expect(editing.visible[0]).toBe('Edit');
    expect(editing.names[0]).toBe('Edit route 1');
    await user.keyboard('{Escape}');

    // The dialog's buttons keep the short verb and the route in their names.
    await user.click(screen.getByRole('button', { name: 'Delete route 1' }));
    const pair = rowButtons(screen.getByRole('dialog'));
    expect(pair.visible).toEqual(expect.arrayContaining(['Confirm delete', 'Cancel']));
    expect(pair.names).toEqual(
      expect.arrayContaining(['Confirm delete route 1', 'Cancel Delete route 1']),
    );
    // The dialog's X shows a glyph, not a word, so its "Close" name is exempt.
    expect(
      labelInNameViolations(
        screen
          .getAllByRole('button')
          .filter((button) => !button.classList.contains('modal__close')),
      ),
    ).toEqual([]);
  });

  it('Heartbeats: the row buttons already read as bare verbs and every name contains its text', async () => {
    const user = userEvent.setup();
    renderHeartbeats(
      buildClient({
        listHeartbeats: vi.fn().mockResolvedValue([CHECK_1, buildCheck({ id: 2, paused: true })]),
      }),
    );
    await screen.findByRole('button', { name: `Pause ${ROW_1}` });

    const running = rowButtons(rowOfButton(`Pause ${ROW_1}`));
    expect(running.visible).toEqual(['Edit', 'Pause', 'Delete']);
    expect(running.names).toEqual([`Edit ${ROW_1}`, `Pause ${ROW_1}`, `Delete ${ROW_1}`]);
    const paused = rowButtons(rowOfButton(`Resume ${ROW_2}`));
    expect(paused.visible).toEqual(['Edit', 'Resume', 'Delete']);
    expect(paused.names).toEqual([`Edit ${ROW_2}`, `Resume ${ROW_2}`, `Delete ${ROW_2}`]);
    expect(labelInNameViolations(screen.getAllByRole('button'))).toEqual([]);

    await user.click(screen.getByRole('button', { name: `Pause ${ROW_1}` }));
    const confirming = rowButtons(screen.getByRole('dialog'));
    // The dialog's X shows a glyph (named Close); the pair keeps the short verbs.
    expect(confirming.visible).toEqual(expect.arrayContaining(['Confirm pause', 'Keep running']));
    expect(confirming.names).toEqual(
      expect.arrayContaining([`Confirm pause ${ROW_1}`, `Keep running ${ROW_1}`]),
    );
    expect(labelInNameViolations(exceptModalClose(screen.getAllByRole('button')))).toEqual([]);
  });

  it('App identities: every row button name contains its visible text', async () => {
    const user = userEvent.setup();
    renderIdentities(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([buildRequest({ id: 1 })]),
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING]),
      }),
    );
    await screen.findByRole('button', { name: 'Approve identity request for app_1' });

    const request = rowButtons(rowOfButton('Approve identity request for app_1'));
    expect(request.visible).toEqual(['Approve', 'Reject']);
    expect(request.names).toEqual([
      'Approve identity request for app_1',
      'Reject identity request for app_1',
    ]);
    const binding = rowButtons(rowOfButton('Unbind tasktracker'));
    expect(binding.visible).toEqual(['Revoke', 'Revoke', 'Unbind']);
    expect(binding.names).toEqual([
      'Revoke key dak_7Q2m for tasktracker',
      'Revoke key dak_Kf09 for tasktracker',
      'Unbind tasktracker',
    ]);
    expect(labelInNameViolations(screen.getAllByRole('button'))).toEqual([]);

    await user.click(screen.getByRole('button', { name: 'Reject identity request for app_1' }));
    expect(labelInNameViolations(exceptModalClose(screen.getAllByRole('button')))).toEqual([]);
    await user.click(
      screen.getByRole('button', { name: 'Cancel reject identity request for app_1' }),
    );
    await user.click(screen.getByRole('button', { name: 'Unbind tasktracker' }));
    expect(labelInNameViolations(exceptModalClose(screen.getAllByRole('button')))).toEqual([]);
  });
});

// ---------------------------------------------------------------------------------------
// Owner badge: under the first cell's value on the narrow layout, its own column when roomy
// ---------------------------------------------------------------------------------------

interface OwnerCase {
  name: string;
  owner: Pick<DestinationSummary, 'ownerKind' | 'ownerAppId'>;
  kind: 'operator' | 'app' | 'unknown';
  label: string;
  /** The short note on the menu's first line; null when the row shows none. */
  inline: string | null;
  /** The long notice in the destructive confirms; null when the row shows none. */
  long: string | null;
}

const OWNER_CASES: OwnerCase[] = [
  {
    name: 'operator',
    owner: { ownerKind: 'operator', ownerAppId: null },
    kind: 'operator',
    label: strings.ownerOperator,
    inline: null,
    long: null,
  },
  {
    name: 'app-owned',
    owner: { ownerKind: 'app', ownerAppId: 'chores4irl' },
    kind: 'app',
    label: strings.ownerApp({ appId: 'chores4irl' }),
    inline: strings.ownerManagedInlineNote,
    long: strings.ownerManagedNote({ appId: 'chores4irl' }),
  },
  {
    name: 'unknown',
    owner: { ownerKind: 'unknown', ownerAppId: null },
    kind: 'unknown',
    label: strings.ownerUnknown,
    inline: strings.ownerUnknownInlineNote,
    long: strings.ownerUnknownNote,
  },
  {
    // Fail closed: an operator owner that names an app is inconsistent, never the operator.
    name: 'operator owner with an app id',
    owner: { ownerKind: 'operator', ownerAppId: 'chores4irl' },
    kind: 'unknown',
    label: strings.ownerUnknown,
    inline: strings.ownerUnknownInlineNote,
    long: strings.ownerUnknownNote,
  },
  {
    // Fail closed: an app owner with no app id is never shown as the operator.
    name: 'app owner without an app id',
    owner: { ownerKind: 'app', ownerAppId: null },
    kind: 'unknown',
    label: strings.ownerUnknown,
    inline: strings.ownerUnknownInlineNote,
    long: strings.ownerUnknownNote,
  },
];

function rowOfKebab(label: string): HTMLElement {
  return kebab(label).closest('tr') as HTMLElement;
}

/** The narrow row's only owner badge sits in its first cell, on its own line under the value. */
function expectOwnerUnderValue({
  row,
  value,
  ownerCase,
}: {
  row: HTMLElement;
  value: string;
  ownerCase: OwnerCase;
}) {
  const cells = within(row).getAllByRole('cell');
  const [first] = cells;
  const badges = row.querySelectorAll('.owner-badge');
  expect(badges).toHaveLength(1);
  const [badge] = badges;
  expect(first).toContainElement(badge as HTMLElement);
  expect(badge).toHaveClass(`owner-badge--${ownerCase.kind}`);
  expect(badge).toHaveTextContent(`${strings.ownerPrefix}${ownerCase.label}`);
  // A block line of its own, after the value, so it never shares a line with it.
  expect(badge.parentElement).toHaveClass('admin-table__owner-line');
  expect(badge.parentElement?.parentElement).toBe(first);
  expect(first.textContent).toBe(`${value}${strings.ownerPrefix}${ownerCase.label}`);
  if (ownerCase.kind !== 'operator') {
    expect(row.querySelector('.owner-badge--operator')).toBeNull();
  }
  // The short note belongs to the menu on the narrow layout, not to the row itself.
  if (ownerCase.inline !== null) {
    expect(row).not.toHaveTextContent(ownerCase.inline);
  }
  // No Owner column: the row has exactly the header's columns (Actions included).
  expect(screen.queryByRole('columnheader', { name: strings.columnOwner })).toBeNull();
  expect(cells).toHaveLength(columnTitles().length);
}

/** The menu's first line is the short note (absent for the operator); the confirm keeps the long one. */
async function expectMenuAndConfirmNotes({
  user,
  row,
  deleteName,
  ownerCase,
}: {
  user: UserEvent;
  row: string;
  deleteName: string;
  ownerCase: OwnerCase;
}) {
  const menu = await openMenu({ user, row });
  if (ownerCase.inline === null || ownerCase.long === null) {
    expect(menu.querySelector('.row-menu__note')).toBeNull();
    expect(menu.firstElementChild).not.toHaveAttribute('role', 'presentation');
  } else {
    expect(menu.firstElementChild).toHaveAttribute('role', 'presentation');
    expect(menu.firstElementChild).toHaveTextContent(ownerCase.inline);
    expect(menu).not.toHaveTextContent(ownerCase.long);
  }
  await chooseItem({ user, name: deleteName });
  const confirm = screen.getByRole('dialog');
  if (ownerCase.long === null) {
    expect(confirm).not.toHaveTextContent(strings.ownerManagedNote({ appId: 'chores4irl' }));
    expect(confirm).not.toHaveTextContent(strings.ownerUnknownNote);
  } else {
    expect(confirm).toHaveTextContent(ownerCase.long);
  }
}

describe('Owner badge, narrow layout', () => {
  beforeEach(() => {
    setCompactViewport(true);
  });

  describe.each(OWNER_CASES)('$name row', (ownerCase) => {
    it('Destinations: badge under the name, no Owner column, notes in the menu and confirm', async () => {
      const user = userEvent.setup();
      renderDestinations(
        buildClient({
          listDestinations: vi
            .fn()
            .mockResolvedValue([buildDestination({ name: 'row_a', ...ownerCase.owner })]),
        }),
      );
      await screen.findByRole('button', { name: 'Actions for row_a' });

      expectOwnerUnderValue({ row: rowOfKebab('row_a'), value: 'row_a', ownerCase });
      await expectMenuAndConfirmNotes({
        user,
        row: 'row_a',
        deleteName: 'Delete row_a',
        ownerCase,
      });
    });

    it('Routes: badge under the App value, no Owner column, notes in the menu and confirm', async () => {
      const user = userEvent.setup();
      renderRoutes(
        buildClient({
          listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1, ...ownerCase.owner })]),
        }),
      );
      await screen.findByRole('button', { name: 'Actions for route 1' });

      expectOwnerUnderValue({ row: rowOfKebab('route 1'), value: '*', ownerCase });
      await expectMenuAndConfirmNotes({
        user,
        row: 'route 1',
        deleteName: 'Delete route 1',
        ownerCase,
      });
    });

    it('Heartbeats: badge under the Check value, no Owner column, notes in the menu and confirm', async () => {
      const user = userEvent.setup();
      renderHeartbeats(
        buildClient({
          listHeartbeats: vi.fn().mockResolvedValue([buildCheck({ id: 1, ...ownerCase.owner })]),
        }),
      );
      await screen.findByRole('button', { name: `Actions for ${ROW_1}` });

      expectOwnerUnderValue({
        row: rowOfKebab(ROW_1),
        value: `urls4irl${strings.heartbeatCheckSeparator}check_1`,
        ownerCase,
      });
      await expectMenuAndConfirmNotes({
        user,
        row: ROW_1,
        deleteName: `Delete ${ROW_1}`,
        ownerCase,
      });
    });
  });

  it('the Destinations edit dialog adds no editor row to the narrow table', async () => {
    const user = userEvent.setup();
    renderDestinations(
      buildClient({
        listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'row_a' })]),
      }),
    );
    await screen.findByRole('button', { name: 'Actions for row_a' });
    await openMenu({ user, row: 'row_a' });
    await chooseItem({ user, name: 'Edit row_a' });
    expect(screen.getByRole('dialog', { name: 'Edit destination row_a' })).toBeInTheDocument();
    expect(document.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(columnTitles()).toHaveLength(5);
  });

  it('the Routes edit dialog adds no editor row to the narrow table', async () => {
    const user = userEvent.setup();
    renderRoutes(buildClient({ listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]) }));
    await screen.findByRole('button', { name: 'Actions for route 1' });
    await openMenu({ user, row: 'route 1' });
    await chooseItem({ user, name: 'Edit route 1' });
    expect(document.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(columnTitles()).toHaveLength(5);
  });

  it('the Heartbeats edit dialog adds no editor row to the narrow table', async () => {
    const user = userEvent.setup();
    renderHeartbeats(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]) }));
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });
    await openMenu({ user, row: ROW_1 });
    await chooseItem({ user, name: `Edit ${ROW_1}` });
    expect(document.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(columnTitles()).toHaveLength(6);
  });
});

describe('Owner badge, roomy layout', () => {
  /** The roomy row keeps its Owner column, with the one badge in it and none under the value. */
  function expectOwnerColumn({
    row,
    ownerColumn,
    ownerCase,
  }: {
    row: HTMLElement;
    ownerColumn: number;
    ownerCase: OwnerCase;
  }) {
    expect(screen.getByRole('columnheader', { name: strings.columnOwner })).toBeInTheDocument();
    expect(row.querySelectorAll('.owner-badge')).toHaveLength(1);
    expect(row.querySelectorAll('.admin-table__owner-line')).toHaveLength(0);
    const cell = within(row).getAllByRole('cell')[ownerColumn];
    expect(cell.querySelector('.owner-badge')).toHaveTextContent(
      `${strings.ownerPrefix}${ownerCase.label}`,
    );
  }

  describe.each(OWNER_CASES)('$name row', (ownerCase) => {
    it('Destinations: Owner is its own second column', async () => {
      renderDestinations(
        buildClient({
          listDestinations: vi
            .fn()
            .mockResolvedValue([buildDestination({ name: 'row_a', ...ownerCase.owner })]),
        }),
      );
      const row = (await screen.findByText('row_a')).closest('tr') as HTMLElement;
      expectOwnerColumn({ row, ownerColumn: 1, ownerCase });
    });

    it('Routes: Owner is its own third column', async () => {
      renderRoutes(
        buildClient({
          listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1, ...ownerCase.owner })]),
        }),
      );
      const row = (await screen.findByText('alerts*')).closest('tr') as HTMLElement;
      expectOwnerColumn({ row, ownerColumn: 2, ownerCase });
    });

    it('Heartbeats: Owner is its own third column', async () => {
      renderHeartbeats(
        buildClient({
          listHeartbeats: vi.fn().mockResolvedValue([buildCheck({ id: 1, ...ownerCase.owner })]),
        }),
      );
      const row = (await screen.findByText('check_1')).closest('tr') as HTMLElement;
      expectOwnerColumn({ row, ownerColumn: 2, ownerCase });
    });
  });

  it('Destinations, Routes and Heartbeats all edit in dialogs without an extra table row', async () => {
    const user = userEvent.setup();

    const destinations = renderDestinations(
      buildClient({
        listDestinations: vi.fn().mockResolvedValue([buildDestination({ name: 'row_a' })]),
      }),
    );
    await user.click(await screen.findByRole('button', { name: 'Edit row_a' }));
    expect(screen.getByRole('dialog', { name: 'Edit destination row_a' })).toBeInTheDocument();
    expect(document.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(columnTitles()).toHaveLength(6);
    destinations.unmount();

    const routes = renderRoutes(
      buildClient({ listRoutes: vi.fn().mockResolvedValue([buildRoute({ id: 1 })]) }),
    );
    await user.click(await screen.findByRole('button', { name: 'Edit route 1' }));
    expect(screen.getByRole('dialog', { name: 'Edit route 1' })).toBeInTheDocument();
    expect(columnTitles()).toHaveLength(6);
    routes.unmount();

    renderHeartbeats(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]) }));
    await user.click(await screen.findByRole('button', { name: `Edit ${ROW_1}` }));
    expect(screen.getByRole('dialog', { name: `Edit ${ROW_1}` })).toBeInTheDocument();
    expect(columnTitles()).toHaveLength(7);
  });

  it('never carries the badge twice when the layout swaps while mounted', async () => {
    setCompactViewport(true);
    renderHeartbeats(buildClient({ listHeartbeats: vi.fn().mockResolvedValue([CHECK_1]) }));
    await screen.findByRole('button', { name: `Actions for ${ROW_1}` });
    expect(document.querySelectorAll('.owner-badge')).toHaveLength(1);

    setCompactViewport(false);
    await screen.findByRole('button', { name: `Pause ${ROW_1}` });
    expect(document.querySelectorAll('.owner-badge')).toHaveLength(1);
    expect(document.querySelectorAll('.admin-table__owner-line')).toHaveLength(0);
  });
});

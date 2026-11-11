import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { DeliveryApiClient, PersonalDestinationSummary } from '../api/deliveryClient';
import { strings } from '../strings';
import { accessibleNameOf, labelInNameViolations } from '../test-labels';
import { setCompactViewport } from '../test-viewport';
import { PersonalDestinations } from './PersonalDestinations';

afterEach(() => {
  cleanup();
});

function buildRow(
  overrides: Partial<PersonalDestinationSummary> & { id: number },
): PersonalDestinationSummary {
  return {
    personHash: '76gzqgp4byjl6dje',
    label: `Endpoint ${overrides.id}`,
    type: 'discord',
    config: {},
    secrets: { webhook_url: 'https://discord.com/****' },
    enabled: true,
    disabledBy: null,
    disabledReason: null,
    registeredByAppId: 'urls4irl',
    registrarRemoved: false,
    optedInAppIds: ['urls4irl'],
    createdAt: '2026-10-21T18:04:00Z',
    updatedAt: '2026-10-21T18:04:00Z',
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

function listOf(rows: PersonalDestinationSummary[], total = rows.length) {
  return { personalDestinations: rows, total };
}

const HASH = '76gzqgp4byjl6dje';
const ROW_1 = buildRow({ id: 1, label: 'Alpha' });
const ROW_2 = buildRow({ id: 2, label: 'Beta' });
const ROW_3 = buildRow({ id: 3, label: 'Gamma' });

function name(row: PersonalDestinationSummary): string {
  return strings.personalRowName({ label: row.label, hash: row.personHash });
}

function summary(): HTMLElement {
  return screen.getAllByRole('status')[0];
}

function resultLine(): HTMLElement {
  return screen.getAllByRole('status')[1];
}

function rowOf(id: number): HTMLElement {
  const row = document.querySelector<HTMLElement>(`tr[data-personal-id="${id}"]`);
  if (row === null) {
    throw new Error(`row ${id} not found`);
  }
  return row;
}

/** The compact row's expand button (the kebab is a separate control). */
function expandOf(id: number): HTMLElement {
  const toggle = rowOf(id).querySelector<HTMLElement>('button[aria-controls^="personal-details-"]');
  if (toggle === null) {
    throw new Error(`expand control for row ${id} not found`);
  }
  return toggle;
}

describe('PersonalDestinations, roomy layout', () => {
  it('shows a loading line, then the rows with hash, label, id, target and badges', async () => {
    const client = buildClient({
      listPersonalDestinations: vi.fn().mockResolvedValue(
        listOf([
          ROW_1,
          buildRow({
            id: 2,
            label: 'Beta',
            type: 'webhook',
            secrets: { url: 'https://hooks.example.net/****' },
            registrarRemoved: true,
            registeredByAppId: 'gone-app',
            optedInAppIds: [],
          }),
        ]),
      ),
    });
    render(<PersonalDestinations client={client} />);
    expect(screen.getByText(strings.personalLoading)).toBeInTheDocument();

    await screen.findByText('Alpha');
    expect(within(rowOf(1)).getByText(HASH)).toBeInTheDocument();
    expect(within(rowOf(1)).getByText('#1')).toBeInTheDocument();
    expect(within(rowOf(1)).getByText('https://discord.com/****')).toBeInTheDocument();
    expect(
      within(rowOf(1)).getByText(strings.personalRegisteredByPrefix.trim()),
    ).toBeInTheDocument();
    expect(within(rowOf(2)).getByText('https://hooks.example.net/****')).toBeInTheDocument();
    expect(within(rowOf(2)).getByText(strings.personalAppRemoved)).toBeInTheDocument();
    expect(within(rowOf(2)).getByText('gone-app')).toBeInTheDocument();
    expect(within(rowOf(2)).getByText(strings.personalNoApps)).toBeInTheDocument();
    expect(summary()).toHaveTextContent('2 personal destinations, 0 disabled');
    // The person is identified by hash only: the hash is shown verbatim on
    // every row and nothing on the page looks like an email address.
    expect(screen.getAllByText(HASH)).toHaveLength(2);
    expect(document.body.textContent).not.toMatch(/\S+@\S+/);
  });

  it('shows the empty card and a zero summary when there are none', async () => {
    render(<PersonalDestinations client={buildClient()} />);
    expect(await screen.findByText(strings.personalEmpty)).toBeInTheDocument();
    expect(summary()).toHaveTextContent(strings.personalSummaryEmpty);
  });

  it('shows the load error with Retry, and Retry reloads', async () => {
    const user = userEvent.setup();
    const listPersonalDestinations = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'boom' }))
      .mockResolvedValue(listOf([ROW_1]));
    render(<PersonalDestinations client={buildClient({ listPersonalDestinations })} />);

    expect(await screen.findByRole('alert')).toHaveTextContent(
      strings.personalLoadFailed({ message: 'boom' }),
    );
    await user.click(screen.getByRole('button', { name: strings.personalRetry }));
    expect(await screen.findByText('Alpha')).toBeInTheDocument();
    expect(listPersonalDestinations).toHaveBeenCalledTimes(2);
  });

  it('reads the truncation in the summary and hint only when total exceeds the rows shown', async () => {
    const rows = [
      ROW_1,
      buildRow({ id: 2, label: 'Beta', enabled: false, disabledBy: 'operator' }),
    ];
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf(rows, 5)),
        })}
      />,
    );
    await screen.findByText('Alpha');
    expect(summary()).toHaveTextContent('5 personal destinations, 1 disabled among the newest 2');
    expect(screen.getByText(strings.personalTruncatedHint({ shown: 2, total: 5 }))).toBeVisible();
  });

  it('explains each disabled reason', async () => {
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi
            .fn()
            .mockResolvedValue(
              listOf([
                buildRow({ id: 1, enabled: false, disabledBy: 'operator', disabledReason: 'spam' }),
                buildRow({ id: 2, enabled: false, disabledBy: 'operator' }),
                buildRow({ id: 3, enabled: false, disabledBy: 'system' }),
                buildRow({ id: 4, enabled: false, disabledBy: 'app' }),
                buildRow({ id: 5, enabled: false, disabledBy: null }),
              ]),
            ),
        })}
      />,
    );
    await screen.findByText('spam');
    expect(screen.getByText(strings.destinationDisabledNoReason)).toBeInTheDocument();
    expect(screen.getByText(strings.personalDisabledBySystem)).toBeInTheDocument();
    expect(screen.getByText(strings.personalDisabledByApp)).toBeInTheDocument();
    expect(screen.getByText(strings.personalDisabledUnknown)).toBeInTheDocument();
  });

  it('disables through a dialog and announces the result, landing focus on Enable', async () => {
    const user = userEvent.setup();
    const setPersonalDestinationEnabled = vi.fn().mockResolvedValue(undefined);
    const listPersonalDestinations = vi
      .fn()
      .mockResolvedValueOnce(listOf([ROW_1]))
      .mockResolvedValue(
        listOf([buildRow({ id: 1, label: 'Alpha', enabled: false, disabledBy: 'operator' })]),
      );
    render(
      <PersonalDestinations
        client={buildClient({ listPersonalDestinations, setPersonalDestinationEnabled })}
      />,
    );
    await screen.findByText('Alpha');

    await user.click(
      screen.getByRole('button', { name: strings.disablePersonal({ row: name(ROW_1) }) }),
    );
    expect(
      screen.getByRole('dialog', { name: strings.disablePersonalDialogTitle }),
    ).toHaveTextContent(strings.disablePersonalNotice);
    expect(setPersonalDestinationEnabled).not.toHaveBeenCalled();
    await user.click(
      screen.getByRole('button', { name: strings.confirmDisablePersonal({ row: name(ROW_1) }) }),
    );

    await waitFor(() =>
      expect(setPersonalDestinationEnabled).toHaveBeenCalledWith({ id: 1, enabled: false }),
    );
    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(
        strings.personalDisabledResult({ label: 'Alpha', id: 1 }),
      ),
    );
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: strings.enablePersonal({ row: name(ROW_1) }) }),
      ).toHaveFocus(),
    );
  });

  it('enables with no confirm dialog', async () => {
    const user = userEvent.setup();
    const disabledRow = buildRow({ id: 1, label: 'Alpha', enabled: false, disabledBy: 'operator' });
    const setPersonalDestinationEnabled = vi.fn().mockResolvedValue(undefined);
    const listPersonalDestinations = vi
      .fn()
      .mockResolvedValueOnce(listOf([disabledRow]))
      .mockResolvedValue(listOf([ROW_1]));
    render(
      <PersonalDestinations
        client={buildClient({ listPersonalDestinations, setPersonalDestinationEnabled })}
      />,
    );
    await screen.findByText('Alpha');
    await user.click(
      screen.getByRole('button', { name: strings.enablePersonal({ row: name(ROW_1) }) }),
    );

    expect(screen.queryByRole('dialog')).toBeNull();
    await waitFor(() =>
      expect(setPersonalDestinationEnabled).toHaveBeenCalledWith({ id: 1, enabled: true }),
    );
    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(
        strings.personalEnabledResult({ label: 'Alpha', id: 1 }),
      ),
    );
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: strings.disablePersonal({ row: name(ROW_1) }) }),
      ).toHaveFocus(),
    );
  });

  it('deletes after a notice naming the opted-in apps, and focus moves to the next row', async () => {
    const user = userEvent.setup();
    const deletePersonalDestination = vi.fn().mockResolvedValue(undefined);
    const listPersonalDestinations = vi
      .fn()
      .mockResolvedValueOnce(listOf([ROW_1, ROW_2]))
      .mockResolvedValue(listOf([ROW_2]));
    render(
      <PersonalDestinations
        client={buildClient({ listPersonalDestinations, deletePersonalDestination })}
      />,
    );
    await screen.findByText('Alpha');

    await user.click(
      screen.getByRole('button', { name: strings.deletePersonal({ row: name(ROW_1) }) }),
    );
    expect(
      screen.getByRole('dialog', { name: strings.deletePersonalDialogTitle }),
    ).toHaveTextContent(strings.deletePersonalNotice({ apps: ['urls4irl'] }));
    await user.click(
      screen.getByRole('button', { name: strings.confirmDeletePersonal({ row: name(ROW_1) }) }),
    );

    await waitFor(() => expect(deletePersonalDestination).toHaveBeenCalledWith({ id: 1 }));
    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(
        strings.personalDeletedResult({ label: 'Alpha', id: 1 }),
      ),
    );
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: strings.disablePersonal({ row: name(ROW_2) }) }),
      ).toHaveFocus(),
    );
  });

  it('uses the no-apps notice when nothing is opted in, and focus falls to the heading for the last row', async () => {
    const user = userEvent.setup();
    const lonely = buildRow({ id: 1, label: 'Alpha', optedInAppIds: [] });
    const listPersonalDestinations = vi
      .fn()
      .mockResolvedValueOnce(listOf([lonely]))
      .mockResolvedValue(listOf([]));
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations,
          deletePersonalDestination: vi.fn().mockResolvedValue(undefined),
        })}
      />,
    );
    await screen.findByText('Alpha');
    await user.click(
      screen.getByRole('button', { name: strings.deletePersonal({ row: name(lonely) }) }),
    );
    expect(screen.getByRole('dialog')).toHaveTextContent(strings.deletePersonalNoAppsNotice);
    await user.click(
      screen.getByRole('button', { name: strings.confirmDeletePersonal({ row: name(lonely) }) }),
    );
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: strings.personalHeading })).toHaveFocus(),
    );
  });

  it('a failed action shows a per-row alert, keeps it across the reload, returns focus, and clears it on the next action', async () => {
    const user = userEvent.setup();
    const deletePersonalDestination = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'nope' }))
      .mockResolvedValue(undefined);
    const listPersonalDestinations = vi.fn().mockResolvedValue(listOf([ROW_1, ROW_2]));
    render(
      <PersonalDestinations
        client={buildClient({ listPersonalDestinations, deletePersonalDestination })}
      />,
    );
    await screen.findByText('Alpha');
    const deleteButton = () =>
      screen.getByRole('button', { name: strings.deletePersonal({ row: name(ROW_1) }) });
    await user.click(deleteButton());
    await user.click(
      screen.getByRole('button', { name: strings.confirmDeletePersonal({ row: name(ROW_1) }) }),
    );

    const alert = await within(rowOf(1)).findByRole('alert');
    expect(alert).toHaveTextContent(strings.personalDeleteFailed);
    await waitFor(() => expect(listPersonalDestinations).toHaveBeenCalledTimes(2));
    expect(within(rowOf(1)).getByRole('alert')).toBeInTheDocument();
    expect(within(rowOf(2)).queryByRole('alert')).toBeNull();
    await waitFor(() => expect(deleteButton()).toHaveFocus());
    expect(deleteButton()).not.toHaveAttribute('aria-disabled', 'true');

    await user.click(deleteButton());
    await user.click(
      screen.getByRole('button', { name: strings.confirmDeletePersonal({ row: name(ROW_1) }) }),
    );
    await waitFor(() => expect(deletePersonalDestination).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(within(rowOf(1)).queryByRole('alert')).toBeNull());
  });

  it('a 404 is announced in the status region with no row alert, and the list reloads', async () => {
    const user = userEvent.setup();
    const listPersonalDestinations = vi
      .fn()
      .mockResolvedValueOnce(listOf([ROW_1, ROW_2]))
      .mockResolvedValue(listOf([ROW_2]));
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations,
          deletePersonalDestination: vi
            .fn()
            .mockRejectedValue(new ApiError({ status: 404, message: 'gone' })),
        })}
      />,
    );
    await screen.findByText('Alpha');
    await user.click(
      screen.getByRole('button', { name: strings.deletePersonal({ row: name(ROW_1) }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmDeletePersonal({ row: name(ROW_1) }) }),
    );

    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(strings.personalGone({ label: 'Alpha', id: 1 })),
    );
    expect(screen.queryByRole('alert')).toBeNull();
    await waitFor(() => expect(screen.queryByText('Alpha')).toBeNull());
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: strings.disablePersonal({ row: name(ROW_2) }) }),
      ).toHaveFocus(),
    );
  });

  it('a 404 whose reload also removes the neighbour lands focus on the heading', async () => {
    const user = userEvent.setup();
    const listPersonalDestinations = vi
      .fn()
      .mockResolvedValueOnce(listOf([ROW_1, ROW_2]))
      .mockResolvedValue(listOf([]));
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations,
          deletePersonalDestination: vi
            .fn()
            .mockRejectedValue(new ApiError({ status: 404, message: 'gone' })),
        })}
      />,
    );
    await screen.findByText('Alpha');
    await user.click(
      screen.getByRole('button', { name: strings.deletePersonal({ row: name(ROW_1) }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmDeletePersonal({ row: name(ROW_1) }) }),
    );

    await waitFor(() => expect(screen.queryByText('Beta')).toBeNull());
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: strings.personalHeading })).toHaveFocus(),
    );
  });

  it('row buttons read the bare verb and keep the label and hash in their names', async () => {
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi
            .fn()
            .mockResolvedValue(
              listOf([
                ROW_1,
                buildRow({ id: 2, label: 'Beta', enabled: false, disabledBy: 'app' }),
              ]),
            ),
        })}
      />,
    );
    await screen.findByText('Alpha');

    const buttons = (id: number) =>
      within(rowOf(id))
        .getAllByRole('button')
        .map((button) => [button.textContent, accessibleNameOf(button)]);
    expect(buttons(1)).toEqual([
      ['Disable', `Disable personal destination Alpha of ${HASH}`],
      ['Delete', `Delete personal destination Alpha of ${HASH}`],
    ]);
    expect(buttons(2)).toEqual([
      ['Enable', `Enable personal destination Beta of ${HASH}`],
      ['Delete', `Delete personal destination Beta of ${HASH}`],
    ]);
    expect(labelInNameViolations(screen.getAllByRole('button'))).toEqual([]);
  });

  it('cancelling a dialog returns focus to its trigger', async () => {
    const user = userEvent.setup();
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1])),
        })}
      />,
    );
    await screen.findByText('Alpha');
    const trigger = screen.getByRole('button', {
      name: strings.deletePersonal({ row: name(ROW_1) }),
    });
    await user.click(trigger);
    await user.click(
      screen.getByRole('button', {
        name: strings.cancelActionNamed({ subject: strings.deletePersonal({ row: name(ROW_1) }) }),
      }),
    );
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it('disables only the acting row while its request is in flight', async () => {
    const user = userEvent.setup();
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1, ROW_2, ROW_3])),
          deletePersonalDestination: vi.fn().mockReturnValue(pending),
        })}
      />,
    );
    await screen.findByText('Alpha');
    await user.click(
      screen.getByRole('button', { name: strings.deletePersonal({ row: name(ROW_1) }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmDeletePersonal({ row: name(ROW_1) }) }),
    );

    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: strings.disablePersonal({ row: name(ROW_1) }) }),
      ).toHaveAttribute('aria-disabled', 'true'),
    );
    expect(
      screen.getByRole('button', { name: strings.disablePersonal({ row: name(ROW_2) }) }),
    ).not.toHaveAttribute('aria-disabled', 'true');
    release();
    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(
        strings.personalDeletedResult({ label: 'Alpha', id: 1 }),
      ),
    );
  });

  it('a 404 on disable is announced with no row alert', async () => {
    const user = userEvent.setup();
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi
            .fn()
            .mockResolvedValueOnce(listOf([ROW_1, ROW_2]))
            .mockResolvedValue(listOf([ROW_2])),
          setPersonalDestinationEnabled: vi
            .fn()
            .mockRejectedValue(new ApiError({ status: 404, message: 'gone' })),
        })}
      />,
    );
    await screen.findByText('Alpha');
    await user.click(
      screen.getByRole('button', { name: strings.disablePersonal({ row: name(ROW_1) }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmDisablePersonal({ row: name(ROW_1) }) }),
    );
    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(strings.personalGone({ label: 'Alpha', id: 1 })),
    );
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('a failed disable shows a row alert and returns focus to the Disable button', async () => {
    const user = userEvent.setup();
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1])),
          setPersonalDestinationEnabled: vi
            .fn()
            .mockRejectedValue(new ApiError({ status: 500, message: 'x' })),
        })}
      />,
    );
    await screen.findByText('Alpha');
    const trigger = screen.getByRole('button', {
      name: strings.disablePersonal({ row: name(ROW_1) }),
    });
    await user.click(trigger);
    await user.click(
      screen.getByRole('button', { name: strings.confirmDisablePersonal({ row: name(ROW_1) }) }),
    );
    expect(await within(rowOf(1)).findByRole('alert')).toHaveTextContent(
      strings.personalDisableFailed,
    );
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(trigger).not.toHaveAttribute('aria-disabled', 'true');
  });

  it('reloads quietly when the refresh signal changes', async () => {
    const listPersonalDestinations = vi.fn().mockResolvedValue(listOf([ROW_1]));
    const client = buildClient({ listPersonalDestinations });
    const { rerender } = render(<PersonalDestinations client={client} refreshSignal={0} />);
    await screen.findByText('Alpha');
    expect(listPersonalDestinations).toHaveBeenCalledTimes(1);

    rerender(<PersonalDestinations client={client} refreshSignal={1} />);
    await waitFor(() => expect(listPersonalDestinations).toHaveBeenCalledTimes(2));
    expect(screen.queryByText(strings.personalLoading)).toBeNull();
  });
});

describe('PersonalDestinations, narrow layout', () => {
  it('renders one control set: a kebab named for the row, with target, id and reason in the lead cell', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    const disabledRow = buildRow({
      id: 2,
      label: 'Beta',
      enabled: false,
      disabledBy: 'operator',
      disabledReason: 'spam',
    });
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1, disabledRow])),
        })}
      />,
    );
    await screen.findByRole('button', { name: `Actions for ${name(ROW_1)}` });

    expect(screen.queryByRole('button', { name: /^(Disable|Enable|Delete) personal/ })).toBeNull();
    // Collapsed: hash, status, label and id only.
    expect(within(rowOf(1)).getByText('#1')).toBeVisible();
    expect(within(rowOf(1)).getByText(HASH)).toBeVisible();
    expect(within(rowOf(1)).getByText(strings.destinationEnabled)).toBeVisible();
    expect(within(rowOf(1)).getByText('Alpha')).toBeVisible();
    expect(within(rowOf(1)).getByText('https://discord.com/****')).not.toBeVisible();
    expect(
      within(rowOf(1)).getByText('urls4irl', { selector: '.personal__chips *' }),
    ).not.toBeVisible();
    expect(within(rowOf(2)).getByText('spam')).not.toBeVisible();

    await user.click(screen.getByRole('button', { name: `Actions for ${name(ROW_1)}` }));
    expect(screen.getAllByRole('menuitem').map((item) => item.getAttribute('aria-label'))).toEqual([
      strings.disablePersonal({ row: name(ROW_1) }),
      strings.deletePersonal({ row: name(ROW_1) }),
    ]);
  });

  it('expands on tap with aria-expanded, revealing the target, type, registrar and apps', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    const disabledRow = buildRow({
      id: 2,
      label: 'Beta',
      enabled: false,
      disabledBy: 'operator',
      disabledReason: 'spam',
      registrarRemoved: true,
      optedInAppIds: [],
    });
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1, disabledRow])),
        })}
      />,
    );
    await screen.findByText('Alpha');
    const toggle = expandOf(1);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    const controlled = document.getElementById(toggle.getAttribute('aria-controls') ?? '');
    expect(controlled).not.toBeNull();

    await user.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(within(rowOf(1)).getByText('https://discord.com/****')).toBeVisible();
    // The registrar badge and the opted-in chip both show the app.
    const badges = within(rowOf(1)).getAllByText('urls4irl', {
      selector: '.owner-badge',
      exact: false,
    });
    expect(badges).toHaveLength(2);
    badges.forEach((badge) => expect(badge).toBeVisible());
    expect(
      within(rowOf(1)).getByText(strings.personalRegisteredByPrefix.trim(), { exact: false }),
    ).toBeInTheDocument();
    expect(within(rowOf(1)).getByText('Discord')).toBeVisible();
    expect(within(rowOf(2)).getByText('spam')).not.toBeVisible();

    // The second row opens independently, with its registrar-removed, no-apps and reason lines.
    await user.click(expandOf(2));
    expect(within(rowOf(2)).getByText('spam')).toBeVisible();
    expect(within(rowOf(2)).getByText(strings.personalAppRemoved)).toBeVisible();
    expect(within(rowOf(2)).getByText(strings.personalNoApps)).toBeVisible();

    await user.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(within(rowOf(1)).getByText('https://discord.com/****')).not.toBeVisible();
  });

  it('expands from the keyboard and keeps the compact header to hash and actions', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1])),
        })}
      />,
    );
    await screen.findByText('Alpha');
    expect(screen.getAllByRole('columnheader')).toHaveLength(2);
    expandOf(1).focus();
    await user.keyboard('{Enter}');
    expect(expandOf(1)).toHaveAttribute('aria-expanded', 'true');
    await user.keyboard(' ');
    expect(expandOf(1)).toHaveAttribute('aria-expanded', 'false');
  });

  it('keeps the expand control and the kebab separate: a kebab tap never toggles expansion', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1])),
        })}
      />,
    );
    const kebab = await screen.findByRole('button', { name: `Actions for ${name(ROW_1)}` });
    await user.click(kebab);
    expect(expandOf(1)).toHaveAttribute('aria-expanded', 'false');
  });

  it('keeps a row expanded across a quiet reload and drops it when the row disappears', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    const listPersonalDestinations = vi
      .fn()
      .mockResolvedValueOnce(listOf([ROW_1, ROW_2]))
      .mockResolvedValueOnce(listOf([ROW_1]))
      .mockResolvedValue(listOf([ROW_1, ROW_2]));
    const client = buildClient({ listPersonalDestinations });
    const { rerender } = render(<PersonalDestinations client={client} refreshSignal={0} />);
    await screen.findByText('Alpha');
    await user.click(expandOf(1));
    await user.click(expandOf(2));

    rerender(<PersonalDestinations client={client} refreshSignal={1} />);
    await waitFor(() => expect(listPersonalDestinations).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(document.querySelector('tr[data-personal-id="2"]')).toBeNull());
    expect(expandOf(1)).toHaveAttribute('aria-expanded', 'true');

    // Row 2 comes back collapsed.
    rerender(<PersonalDestinations client={client} refreshSignal={2} />);
    await waitFor(() => expect(document.querySelector('tr[data-personal-id="2"]')).not.toBeNull());
    expect(expandOf(2)).toHaveAttribute('aria-expanded', 'false');
  });

  it('disables from the menu, then focuses the kebab', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    const setPersonalDestinationEnabled = vi.fn().mockResolvedValue(undefined);
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1])),
          setPersonalDestinationEnabled,
        })}
      />,
    );
    const kebab = await screen.findByRole('button', { name: `Actions for ${name(ROW_1)}` });
    await user.click(kebab);
    await user.click(
      screen.getByRole('menuitem', { name: strings.disablePersonal({ row: name(ROW_1) }) }),
    );
    expect(screen.getByRole('dialog', { name: strings.disablePersonalDialogTitle })).toBeVisible();
    await user.click(
      screen.getByRole('button', { name: strings.confirmDisablePersonal({ row: name(ROW_1) }) }),
    );

    await waitFor(() =>
      expect(setPersonalDestinationEnabled).toHaveBeenCalledWith({ id: 1, enabled: false }),
    );
    await waitFor(() =>
      expect(screen.getByRole('button', { name: `Actions for ${name(ROW_1)}` })).toHaveFocus(),
    );
  });

  it('shows a failed action once, in the lead cell, even while the row is collapsed', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([ROW_1])),
          setPersonalDestinationEnabled: vi
            .fn()
            .mockRejectedValue(new ApiError({ status: 500, message: 'x' })),
        })}
      />,
    );
    await user.click(await screen.findByRole('button', { name: `Actions for ${name(ROW_1)}` }));
    await user.click(
      screen.getByRole('menuitem', { name: strings.disablePersonal({ row: name(ROW_1) }) }),
    );
    await user.click(
      screen.getByRole('button', { name: strings.confirmDisablePersonal({ row: name(ROW_1) }) }),
    );

    const alerts = await screen.findAllByRole('alert');
    expect(alerts).toHaveLength(1);
    expect(alerts[0]).toHaveTextContent(strings.personalDisableFailed);
    expect(alerts[0]).toBeVisible();
    expect(expandOf(1)).toHaveAttribute('aria-expanded', 'false');
    await waitFor(() =>
      expect(screen.getByRole('button', { name: `Actions for ${name(ROW_1)}` })).toHaveFocus(),
    );
    expect(screen.getAllByRole('alert')).toHaveLength(1);
  });

  it('puts the no-apps delete notice in the menu dialog', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    const lonely = buildRow({ id: 1, label: 'Alpha', optedInAppIds: [] });
    render(
      <PersonalDestinations
        client={buildClient({
          listPersonalDestinations: vi.fn().mockResolvedValue(listOf([lonely])),
        })}
      />,
    );
    await user.click(await screen.findByRole('button', { name: `Actions for ${name(lonely)}` }));
    await user.click(
      screen.getByRole('menuitem', { name: strings.deletePersonal({ row: name(lonely) }) }),
    );
    expect(screen.getByRole('dialog')).toHaveTextContent(strings.deletePersonalNoAppsNotice);
  });
});

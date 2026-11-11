import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type {
  DeliveryApiClient,
  IdentityBinding,
  IdentityKey,
  IdentityRegistrar,
  IdentityRequest,
} from '../api/deliveryClient';
import { strings } from '../strings';
import { useTimeZone } from '../test-timezone';
import { setCompactViewport } from '../test-viewport';
import { AppIdentities } from './AppIdentities';

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

function buildRequest(overrides: Partial<IdentityRequest> & { id: number }): IdentityRequest {
  return {
    kind: 'app',
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
    registrar: null,
    acknowledgedAt: '2026-10-19T09:12:00Z',
    keys: [],
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

function renderSection(client: DeliveryApiClient) {
  return render(<AppIdentities client={client} />);
}

function button(name: string): HTMLElement {
  return screen.getByRole('button', { name });
}

function requestRow(id: number): HTMLElement {
  const row = document.querySelector<HTMLElement>(`tr[data-request-id="${id}"]`);
  if (row === null) {
    throw new Error(`request row ${id} not found`);
  }
  return row;
}

function bindingRow(appId: string): HTMLElement {
  const row = document.querySelector<HTMLElement>(`tr[data-app-id="${appId}"]`);
  if (row === null) {
    throw new Error(`binding row ${appId} not found`);
  }
  return row;
}

function requestsHeading(): HTMLElement {
  return screen.getByRole('heading', { name: strings.identitiesRequestsHeading });
}

function bindingsHeading(): HTMLElement {
  return screen.getByRole('heading', { name: strings.identitiesBindingsHeading });
}

/** The pending-count live region (first role=status), above the action-result line. */
function summary(): HTMLElement {
  return screen.getAllByRole('status')[0];
}

function resultLine(): HTMLElement {
  return screen.getAllByRole('status')[1];
}

const REQUEST_1 = buildRequest({ id: 1 });
const REQUEST_2 = buildRequest({ id: 2 });
const REQUEST_3 = buildRequest({ id: 3 });

const KEY_ACTIVE = buildKey({
  id: 11,
  appId: 'tasktracker',
  keyHint: 'dak_7Q2m',
  lastUsedAt: '2026-10-21T17:58:00Z',
});
const KEY_SUPERSEDED = buildKey({
  id: 12,
  appId: 'tasktracker',
  keyHint: 'dak_Kf09',
  state: 'superseded',
  supersededAt: '2026-10-21T16:40:00Z',
});
const KEY_REVOKED = buildKey({
  id: 13,
  appId: 'tasktracker',
  keyHint: 'dak_a1Zx',
  state: 'revoked',
  revokedAt: '2026-10-20T09:00:00Z',
  revokedReason: 'operator',
});
const BINDING_TASKTRACKER = buildBinding({
  appId: 'tasktracker',
  keys: [KEY_ACTIVE, KEY_SUPERSEDED, KEY_REVOKED],
});
const BINDING_SECOND = buildBinding({
  appId: 'urls4irl',
  keys: [buildKey({ id: 21, appId: 'urls4irl', keyHint: 'dak_Zz11' })],
});
const BINDING_THIRD = buildBinding({ appId: 'worldwide' });

describe('AppIdentities list states', () => {
  it('loads on mount, shows loading first, then both tables', async () => {
    const requests = deferred<IdentityRequest[]>();
    const client = buildClient({
      listIdentityRequests: vi.fn().mockReturnValue(requests.promise),
      listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]),
    });
    renderSection(client);

    expect(screen.getByText(strings.identitiesLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();

    requests.resolve([REQUEST_1]);
    expect(await screen.findAllByRole('table')).toHaveLength(2);

    expect(screen.queryByText(strings.identitiesLoading)).toBeNull();
    for (const column of [
      strings.columnIdentityStatus,
      strings.columnIdentityRequestedBy,
      strings.columnIdentityRequested,
      strings.columnIdentityBoundTo,
      strings.columnIdentityKeys,
    ]) {
      expect(screen.getByRole('columnheader', { name: column })).toBeInTheDocument();
    }
    expect(client.listIdentityRequests).toHaveBeenCalledTimes(1);
    expect(client.listIdentityBindings).toHaveBeenCalledTimes(1);
  });

  it('words the empty states differently for no requests and no approved apps', async () => {
    renderSection(buildClient());

    expect(await screen.findByText(strings.identitiesRequestsEmpty)).toBeInTheDocument();
    expect(screen.getByText(strings.identitiesBindingsEmpty)).toBeInTheDocument();
    expect(strings.identitiesRequestsEmpty).not.toBe(strings.identitiesBindingsEmpty);
    expect(screen.queryByRole('table')).toBeNull();
    expect(summary()).toBeEmptyDOMElement();
  });

  it('shows only the empty state that applies', async () => {
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([BINDING_SECOND]) }),
    );

    expect(await screen.findByText(strings.identitiesRequestsEmpty)).toBeInTheDocument();
    expect(screen.queryByText(strings.identitiesBindingsEmpty)).toBeNull();
    expect(screen.getAllByRole('table')).toHaveLength(1);
  });

  it('renders a load failure as an alert with Retry, and Retry recovers', async () => {
    const user = userEvent.setup();
    const listIdentityRequests = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ message: '503 delivery-api unavailable', status: 503 }))
      .mockResolvedValue([REQUEST_1]);
    renderSection(buildClient({ listIdentityRequests }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(
      strings.identitiesLoadFailed({ message: '503 delivery-api unavailable' }),
    );
    expect(screen.queryByText(strings.identitiesRequestsEmpty)).toBeNull();
    expect(screen.queryByRole('table')).toBeNull();

    await user.click(within(alert).getByRole('button', { name: strings.identitiesRetry }));

    expect(await screen.findAllByRole('table')).not.toHaveLength(0);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(listIdentityRequests).toHaveBeenCalledTimes(2);
  });

  it('falls back to a generic reason for a non-API failure', async () => {
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockRejectedValue(new Error('network')) }),
    );

    expect((await screen.findByRole('alert')).textContent).toContain(
      strings.identitiesLoadFailed({ message: strings.identitiesLoadFailedFallback }),
    );
  });

  it('Refresh reloads both lists', async () => {
    const user = userEvent.setup();
    const client = buildClient();
    renderSection(client);
    await screen.findByText(strings.identitiesRequestsEmpty);

    await user.click(button(strings.identitiesRefresh));

    await screen.findByText(strings.identitiesRequestsEmpty);
    expect(client.listIdentityRequests).toHaveBeenCalledTimes(2);
    expect(client.listIdentityBindings).toHaveBeenCalledTimes(2);
  });
});

describe('AppIdentities requests', () => {
  it('pins the App column of both tables, leading the requests table, never the Actions cell', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]),
      }),
    );

    const [requestsTable, bindingsTable] = await screen.findAllByRole('table');
    for (const [table, leadHeader] of [
      [requestsTable, strings.columnIdentityAsksFor],
      [bindingsTable, strings.columnIdentityApp],
    ] as const) {
      expect(table).toHaveClass('admin-table');
      const headers = within(table).getAllByRole('columnheader');
      expect(headers[0]).toHaveTextContent(leadHeader);
      expect(headers[0]).toHaveClass('admin-table__lead');
      expect(headers[headers.length - 1]).not.toHaveClass('admin-table__lead');
    }
    expect(within(requestRow(1)).getAllByRole('cell')[0]).toHaveClass('admin-table__lead');
    expect(within(bindingRow('tasktracker')).getAllByRole('cell')[0]).toHaveClass(
      'admin-table__lead',
    );
  });

  it('announces the pending count and marks a pending row with the repo, workflow and ref', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValue([REQUEST_1, REQUEST_2, buildRequest({ id: 3, status: 'rejected' })]),
      }),
    );

    await screen.findAllByRole('table');
    expect(summary()).toHaveTextContent('2 requests awaiting approval');
    expect(summary()).toHaveAttribute('aria-live', 'polite');
    expect(resultLine()).toHaveAttribute('aria-live', 'polite');

    const row = requestRow(1);
    expect(row).toHaveClass('app-identities__row--pending');
    expect(within(row).getByText('Pending')).toBeInTheDocument();
    expect(within(row).getByText('4IRL/app_1')).toBeInTheDocument();
    expect(within(row).getByText(/\.github\/workflows\/prod-deploy\.yml/)).toBeInTheDocument();
    expect(
      within(row).getByText('refs/heads/main · environment production · run 14022'),
    ).toBeInTheDocument();
    const requested = within(row).getByText('2026-10-21 18:04 UTC');
    expect(requested.tagName).toBe('TIME');
    expect(requested).toHaveAttribute('datetime', '2026-10-21T18:04:00.000Z');
    expect(requested).toHaveAttribute('title', '2026-10-21T18:04:00.000Z');
  });

  it('uses the singular summary for exactly one request', async () => {
    renderSection(buildClient({ listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]) }));

    await screen.findAllByRole('table');
    expect(summary()).toHaveTextContent('1 request awaiting approval');
  });

  it('says so when a request has no environment', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValue([buildRequest({ id: 1, environment: null })]),
      }),
    );

    expect(
      await screen.findByText('refs/heads/main · no environment · run 14022'),
    ).toBeInTheDocument();
  });

  it('names Approve and Reject after the app', async () => {
    renderSection(buildClient({ listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]) }));

    await screen.findAllByRole('table');
    expect(button('Approve identity request for app_1')).toBeInTheDocument();
    expect(button('Reject identity request for app_1')).toBeInTheDocument();
  });

  it('shows a rejected request already loaded as only a Rejected pill, with no Approve', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValue([buildRequest({ id: 1, status: 'rejected' })]),
      }),
    );

    await screen.findAllByRole('table');
    const row = requestRow(1);
    expect(within(row).queryByRole('button')).toBeNull();
    expect(within(row).getAllByText(strings.identityRequestRejected)).toHaveLength(2);
    expect(screen.queryByRole('button', { name: /^Approve/ })).toBeNull();
    expect(summary()).toBeEmptyDOMElement();
  });

  it('shows an unrecognised request status as an explicit unknown state with no actions', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValue([buildRequest({ id: 1, status: 'unknown' })]),
      }),
    );

    await screen.findAllByRole('table');
    const row = requestRow(1);
    expect(within(row).getByText(strings.identityRequestUnknown)).toBeInTheDocument();
    expect(within(row).queryByRole('button')).toBeNull();
    expect(within(row).queryByText(strings.identityRequestPending)).toBeNull();
    expect(summary()).toBeEmptyDOMElement();
  });
});

describe('AppIdentities approve', () => {
  it('approves, removes the row, adds the binding and hands focus to the neighbour row', async () => {
    const user = userEvent.setup();
    const approved = buildBinding({ appId: 'app_1', githubRepository: '4IRL/app_1' });
    const approve = vi.fn().mockResolvedValue(approved);
    const listIdentityRequests = vi
      .fn()
      .mockResolvedValueOnce([REQUEST_1, REQUEST_2])
      .mockResolvedValue([REQUEST_2]);
    const listIdentityBindings = vi.fn().mockResolvedValueOnce([]).mockResolvedValue([approved]);
    renderSection(
      buildClient({
        listIdentityRequests,
        listIdentityBindings,
        approveIdentityRequest: approve,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));

    await waitFor(() => expect(button('Approve identity request for app_2')).toHaveFocus());
    expect(approve).toHaveBeenCalledWith({ id: 1 });
    expect(document.querySelector('tr[data-request-id="1"]')).toBeNull();
    expect(bindingRow('app_1')).toBeInTheDocument();
    expect(screen.queryByText(strings.identitiesBindingsEmpty)).toBeNull();
    expect(resultLine()).toHaveTextContent(
      strings.identityApproved({ app: 'app_1', repository: '4IRL/app_1' }),
    );
    expect(summary()).toHaveTextContent('1 request awaiting approval');
    expect(within(bindingRow('app_1')).getByText(strings.identityNoKeys)).toBeInTheDocument();
  });

  it('hands focus to the previous request when the last row is approved', async () => {
    const user = userEvent.setup();
    const approved = buildBinding({ appId: 'app_2' });
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1, REQUEST_2]),
        approveIdentityRequest: vi.fn().mockResolvedValue(approved),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_2'));

    await waitFor(() => expect(button('Approve identity request for app_1')).toHaveFocus());
  });

  it('skips a rejected neighbour (it has no control) when picking where focus goes', async () => {
    const user = userEvent.setup();
    const rejected = buildRequest({ id: 2, status: 'rejected' });
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1, rejected, REQUEST_3]),
        approveIdentityRequest: vi.fn().mockResolvedValue(buildBinding({ appId: 'app_1' })),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));

    await waitFor(() => expect(button('Approve identity request for app_3')).toHaveFocus());
  });

  it('falls back to the requests heading when no request with a control remains', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
        approveIdentityRequest: vi.fn().mockResolvedValue(buildBinding({ appId: 'app_1' })),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));

    await waitFor(() => expect(requestsHeading()).toHaveFocus());
  });

  it('shows the server error on a 409, leaves the request listed and keeps focus on the row', async () => {
    const user = userEvent.setup();
    const rejection = deferred<IdentityBinding>();
    const approve = vi.fn().mockReturnValue(rejection.promise);
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1, REQUEST_2]),
        approveIdentityRequest: approve,
      }),
    );
    await screen.findAllByRole('table');
    const approveButton = button('Approve identity request for app_1');

    await user.click(approveButton);
    // Focus is lost while the request is in flight, so only the failure can bring it back.
    act(() => {
      approveButton.blur();
    });
    expect(document.body).toHaveFocus();
    rejection.reject(new ApiError({ message: 'request is not pending', status: 409 }));

    const row = requestRow(1);
    expect(await within(row).findByRole('alert')).toHaveTextContent(
      strings.identityApproveFailed({ message: 'request is not pending' }),
    );
    expect(row).toBeInTheDocument();
    expect(within(row).getByRole('button', { name: 'Approve identity request for app_1' })).toBe(
      approveButton,
    );
    await waitFor(() => expect(approveButton).toHaveFocus());
    expect(approveButton).not.toHaveAttribute('aria-disabled');
    expect(resultLine()).toBeEmptyDOMElement();
    expect(approve).toHaveBeenCalledTimes(1);
  });

  it('shows the server reason when an approve is refused for overlapping an approved app id', async () => {
    const user = userEvent.setup();
    const approve = vi
      .fn()
      .mockRejectedValue(
        new ApiError({ message: 'app id overlaps an approved app id', status: 409 }),
      );
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1, REQUEST_2]),
        approveIdentityRequest: approve,
      }),
    );
    await screen.findAllByRole('table');
    const approveButton = button('Approve identity request for app_1');

    await user.click(approveButton);

    const row = requestRow(1);
    expect(await within(row).findByRole('alert')).toHaveTextContent(
      strings.identityApproveFailed({ message: 'app id overlaps an approved app id' }),
    );
    // The request stays listed so the operator can reject it, and nothing was approved.
    expect(row).toBeInTheDocument();
    expect(requestRow(2)).toBeInTheDocument();
    expect(resultLine()).toBeEmptyDOMElement();
    expect(approve).toHaveBeenCalledTimes(1);
  });

  it('falls back to the generic message for a non-API approve failure', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
        approveIdentityRequest: vi.fn().mockRejectedValue(new Error('network')),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));

    expect(await within(requestRow(1)).findByRole('alert')).toHaveTextContent(
      strings.identityApproveFailed({ message: strings.genericError }),
    );
  });

  it('blocks a second click while an approve is in flight', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityBinding>();
    const approve = vi.fn().mockReturnValue(pending.promise);
    renderSection(
      buildClient({
        // The reload after the approve sees the server's new state: no request left.
        listIdentityRequests: vi.fn().mockResolvedValueOnce([REQUEST_1]).mockResolvedValue([]),
        approveIdentityRequest: approve,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));
    const busy = button('Approving identity request for app_1');
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await user.click(busy);
    await user.click(busy);

    expect(approve).toHaveBeenCalledTimes(1);
    // Reject is blocked too while the request is being approved.
    const reject = button('Reject identity request for app_1');
    expect(reject).toHaveAttribute('aria-disabled', 'true');
    await user.click(reject);
    expect(screen.queryByRole('button', { name: /^Confirm reject/ })).toBeNull();

    pending.resolve(buildBinding({ appId: 'app_1' }));
    await waitFor(() => expect(document.querySelector('tr[data-request-id="1"]')).toBeNull());
  });
});

describe('AppIdentities reject', () => {
  it('shows the notice and sends nothing on Cancel', async () => {
    const user = userEvent.setup();
    const reject = vi.fn();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
        rejectIdentityRequest: reject,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Reject identity request for app_1'));
    expect(screen.getByText(strings.rejectIdentityNotice)).toBeInTheDocument();
    await user.click(button('Cancel reject identity request for app_1'));

    expect(reject).not.toHaveBeenCalled();
    expect(button('Reject identity request for app_1')).toHaveFocus();
    expect(screen.queryByText(strings.rejectIdentityNotice)).toBeNull();
  });

  it('replaces the whole Actions cell with a Rejected pill and moves focus to the requests heading', async () => {
    const user = userEvent.setup();
    const reject = vi.fn().mockResolvedValue({ ...REQUEST_1, status: 'rejected' });
    renderSection(
      buildClient({
        // The reload after the reject sees the server's new state: the request is rejected.
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REQUEST_1, REQUEST_2])
          .mockResolvedValue([{ ...REQUEST_1, status: 'rejected' }, REQUEST_2]),
        rejectIdentityRequest: reject,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Reject identity request for app_1'));
    await user.click(button('Confirm reject identity request for app_1'));

    await waitFor(() => expect(requestsHeading()).toHaveFocus());
    expect(reject).toHaveBeenCalledWith({ id: 1 });
    const row = requestRow(1);
    expect(within(row).queryByRole('button')).toBeNull();
    expect(within(row).getAllByText(strings.identityRequestRejected)).toHaveLength(2);
    expect(row).not.toHaveClass('app-identities__row--pending');
    expect(resultLine()).toHaveTextContent(strings.identityRejected({ app: 'app_1' }));
    expect(summary()).toHaveTextContent('1 request awaiting approval');
    // The other request is untouched.
    expect(button('Approve identity request for app_2')).toBeInTheDocument();
  });

  it('shows the server error on a failed reject and keeps the row actionable', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
        rejectIdentityRequest: vi
          .fn()
          .mockRejectedValue(new ApiError({ message: 'request not found', status: 404 })),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Reject identity request for app_1'));
    await user.click(button('Confirm reject identity request for app_1'));

    expect(await within(requestRow(1)).findByRole('alert')).toHaveTextContent(
      strings.identityRejectFailed({ message: 'request not found' }),
    );
    expect(button('Approve identity request for app_1')).toBeInTheDocument();
    expect(button('Reject identity request for app_1')).not.toHaveAttribute('aria-disabled');
  });

  it('confirms in a dialog naming the app, leaves Approve and the other request alone, closes on Cancel and Escape, and rejects once on Confirm', async () => {
    const user = userEvent.setup();
    const reject = vi.fn().mockResolvedValue({ ...REQUEST_1, status: 'rejected' });
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1, REQUEST_2]),
        rejectIdentityRequest: reject,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Reject identity request for app_1'));
    const dialog = screen.getByRole('dialog', { name: strings.rejectIdentityDialogTitle });
    expect(within(dialog).getByText(strings.dialogItemApp)).toBeInTheDocument();
    expect(within(dialog).getByText('app_1')).toBeInTheDocument();
    expect(within(dialog).getByText(strings.rejectIdentityNotice)).toBeInTheDocument();
    expect(button('Cancel reject identity request for app_1')).toHaveFocus();
    // The row's other buttons no longer step aside.
    expect(button('Approve identity request for app_1')).toBeInTheDocument();
    expect(button('Approve identity request for app_2')).toBeVisible();

    await user.click(button('Cancel reject identity request for app_1'));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(button('Reject identity request for app_1')).toHaveFocus();

    await user.click(button('Reject identity request for app_1'));
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(button('Reject identity request for app_1')).toHaveFocus();
    expect(reject).not.toHaveBeenCalled();

    await user.click(button('Reject identity request for app_1'));
    await user.dblClick(button('Confirm reject identity request for app_1'));
    expect(reject).toHaveBeenCalledTimes(1);
    expect(reject).toHaveBeenCalledWith({ id: 1 });
  });

  it('blocks a second reject while one is in flight', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityRequest>();
    const reject = vi.fn().mockReturnValue(pending.promise);
    const approve = vi.fn();
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REQUEST_1])
          .mockResolvedValue([{ ...REQUEST_1, status: 'rejected' }]),
        rejectIdentityRequest: reject,
        approveIdentityRequest: approve,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Reject identity request for app_1'));
    await user.click(button('Confirm reject identity request for app_1'));
    const busy = button('Rejecting identity request for app_1');
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await user.click(busy);

    expect(reject).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('button', { name: /^Confirm reject/ })).toBeNull();
    // Approve is blocked while the request is being rejected.
    expect(button('Approve identity request for app_1')).toHaveAttribute('aria-disabled', 'true');
    await user.click(button('Approve identity request for app_1'));
    expect(approve).not.toHaveBeenCalled();
    pending.resolve({ ...REQUEST_1, status: 'rejected' });
    await waitFor(() => expect(within(requestRow(1)).queryByRole('button')).toBeNull());
  });
});

describe('AppIdentities bindings and keys', () => {
  it('lists each key with its state, hint only, and when it was last used', async () => {
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]) }),
    );

    await screen.findByRole('table');
    const row = bindingRow('tasktracker');
    expect(within(row).getByText('4IRL/tasktracker')).toBeInTheDocument();
    expect(within(row).getByText('Active')).toBeInTheDocument();
    expect(within(row).getByText('Superseded')).toBeInTheDocument();
    expect(within(row).getByText('Revoked')).toBeInTheDocument();
    expect(within(row).getByText('dak_7Q2m…')).toBeInTheDocument();
    expect(within(row).getByText(/^issued 2026-10-21 16:40 UTC · last used /)).toBeInTheDocument();
    // The superseded key stops working 24 h after it was superseded.
    expect(
      within(row).getByText('stops working 2026-10-22 16:40 UTC · never used'),
    ).toBeInTheDocument();
    expect(within(row).getByText('revoked 2026-10-20 09:00 UTC by operator')).toBeInTheDocument();
  });

  it('never shows a full key, even one smuggled onto a key object', async () => {
    const leaky = {
      ...KEY_ACTIVE,
      key: 'dak_SECRETSECRETSECRETSECRETSECRETSECRET123',
    } as IdentityKey;
    renderSection(
      buildClient({
        listIdentityBindings: vi
          .fn()
          .mockResolvedValue([{ ...BINDING_TASKTRACKER, keys: [leaky, KEY_SUPERSEDED] }]),
      }),
    );

    await screen.findByRole('table');
    expect(document.body.textContent ?? '').not.toContain('SECRETSECRET');
    expect(document.body.textContent ?? '').not.toMatch(/dak_[A-Za-z0-9_-]{9,}/);
    expect(within(bindingRow('tasktracker')).getByText('dak_7Q2m…')).toBeInTheDocument();
  });

  it('offers Revoke on active and superseded keys but not on a revoked one', async () => {
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]) }),
    );

    await screen.findByRole('table');
    expect(button('Revoke key dak_7Q2m for tasktracker')).toBeInTheDocument();
    expect(button('Revoke key dak_Kf09 for tasktracker')).toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Revoke key dak_a1Zx for tasktracker' }),
    ).toBeNull();
    expect(button('Unbind tasktracker')).toBeInTheDocument();
  });

  it('shows an unrecognised key state as an explicit unknown state, never as active', async () => {
    const odd = buildKey({ id: 31, appId: 'tasktracker', keyHint: 'dak_Odd1', state: 'unknown' });
    renderSection(
      buildClient({
        listIdentityBindings: vi
          .fn()
          .mockResolvedValue([buildBinding({ appId: 'tasktracker', keys: [odd] })]),
      }),
    );

    await screen.findByRole('table');
    const row = bindingRow('tasktracker');
    expect(within(row).getByText(strings.identityKeyUnknown)).toBeInTheDocument();
    expect(within(row).queryByText(strings.identityKeyActive)).toBeNull();
  });

  it("revokes after a confirm, swaps the button for a Revoked pill and focuses that app's Unbind", async () => {
    const user = userEvent.setup();
    const revokedKey: IdentityKey = {
      ...KEY_ACTIVE,
      state: 'revoked',
      revokedAt: '2026-10-22T09:00:00Z',
      revokedReason: 'operator',
    };
    const revoke = vi.fn().mockResolvedValue(revokedKey);
    renderSection(
      buildClient({
        // The reload after the revoke sees the server's new state: the key is revoked.
        listIdentityBindings: vi
          .fn()
          .mockResolvedValueOnce([BINDING_TASKTRACKER, BINDING_SECOND])
          .mockResolvedValue([
            { ...BINDING_TASKTRACKER, keys: [revokedKey, KEY_SUPERSEDED, KEY_REVOKED] },
            BINDING_SECOND,
          ]),
        revokeIdentityKey: revoke,
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Revoke key dak_7Q2m for tasktracker'));
    expect(screen.getByText(strings.revokeKeyNotice)).toBeInTheDocument();
    await user.click(button('Confirm revoke key dak_7Q2m for tasktracker'));

    await waitFor(() => expect(button('Unbind tasktracker')).toHaveFocus());
    expect(revoke).toHaveBeenCalledWith({ id: 11 });
    expect(
      screen.queryByRole('button', { name: 'Revoke key dak_7Q2m for tasktracker' }),
    ).toBeNull();
    expect(within(bindingRow('tasktracker')).getAllByText('Revoked')).toHaveLength(2);
    expect(resultLine()).toHaveTextContent(
      strings.identityKeyRevokedResult({ hint: 'dak_7Q2m', app: 'tasktracker' }),
    );
    // The other app's key is untouched.
    expect(button('Revoke key dak_Zz11 for urls4irl')).toBeInTheDocument();
  });

  it('sends nothing when a revoke is cancelled', async () => {
    const user = userEvent.setup();
    const revoke = vi.fn();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]),
        revokeIdentityKey: revoke,
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Revoke key dak_7Q2m for tasktracker'));
    await user.click(button('Cancel revoke key dak_7Q2m for tasktracker'));

    expect(revoke).not.toHaveBeenCalled();
    expect(button('Revoke key dak_7Q2m for tasktracker')).toHaveFocus();
  });

  it('names the key and the app in the revoke dialog, and the app in the unbind dialog', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]) }),
    );
    await screen.findByRole('table');

    await user.click(button('Revoke key dak_7Q2m for tasktracker'));
    const revoke = screen.getByRole('dialog', { name: strings.revokeKeyDialogTitle });
    expect(within(revoke).getByText(strings.dialogItemKey)).toBeInTheDocument();
    expect(within(revoke).getByText('dak_7Q2m for tasktracker')).toBeInTheDocument();
    expect(within(revoke).getByText(strings.revokeKeyNotice)).toBeInTheDocument();
    await user.click(button('Cancel revoke key dak_7Q2m for tasktracker'));

    await user.click(button('Unbind tasktracker'));
    const unbind = screen.getByRole('dialog', { name: strings.unbindAppDialogTitle });
    expect(within(unbind).getByText('tasktracker')).toBeInTheDocument();
    expect(within(unbind).getByText(strings.unbindAppNotice)).toBeInTheDocument();
  });

  it('shows the server error on a failed revoke and keeps the key revocable', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]),
        revokeIdentityKey: vi
          .fn()
          .mockRejectedValue(new ApiError({ message: 'key not found', status: 404 })),
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Revoke key dak_7Q2m for tasktracker'));
    await user.click(button('Confirm revoke key dak_7Q2m for tasktracker'));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      strings.identityRevokeFailed({ message: 'key not found' }),
    );
    expect(button('Revoke key dak_7Q2m for tasktracker')).not.toHaveAttribute('aria-disabled');
  });

  it('blocks a second click while a revoke is in flight', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityKey>();
    const revoke = vi.fn().mockReturnValue(pending.promise);
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]),
        revokeIdentityKey: revoke,
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Revoke key dak_7Q2m for tasktracker'));
    await user.click(button('Confirm revoke key dak_7Q2m for tasktracker'));
    const busy = button('Revoking key dak_7Q2m for tasktracker');
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await user.click(busy);

    expect(revoke).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('button', { name: /^Confirm revoke/ })).toBeNull();
    pending.resolve({ ...KEY_ACTIVE, state: 'revoked', revokedReason: 'operator' });
    await waitFor(() => expect(button('Unbind tasktracker')).toHaveFocus());
  });
});

describe('AppIdentities unbind', () => {
  it("unbinds after a confirm, removes the row and focuses the neighbour row's first control", async () => {
    const user = userEvent.setup();
    const unbind = vi.fn().mockResolvedValue({ appId: 'tasktracker', deleted: true });
    renderSection(
      buildClient({
        // The reload after the unbind sees the server's new state: the binding is gone.
        listIdentityBindings: vi
          .fn()
          .mockResolvedValueOnce([BINDING_TASKTRACKER, BINDING_SECOND])
          .mockResolvedValue([BINDING_SECOND]),
        unbindApp: unbind,
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Unbind tasktracker'));
    expect(screen.getByText(strings.unbindAppNotice)).toBeInTheDocument();
    await user.click(button('Confirm unbind tasktracker'));

    await waitFor(() => expect(button('Revoke key dak_Zz11 for urls4irl')).toHaveFocus());
    expect(unbind).toHaveBeenCalledWith({ appId: 'tasktracker' });
    expect(document.querySelector('tr[data-app-id="tasktracker"]')).toBeNull();
    expect(resultLine()).toHaveTextContent(strings.identityUnbound({ app: 'tasktracker' }));
  });

  it("focuses the neighbour's Unbind when the neighbour has no revocable key", async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING_THIRD, BINDING_SECOND]),
        unbindApp: vi.fn().mockResolvedValue({ appId: 'urls4irl', deleted: true }),
      }),
    );
    await screen.findByRole('table');

    // Unbinding the last row hands focus to the previous row, which has no keys.
    await user.click(button('Unbind urls4irl'));
    await user.click(button('Confirm unbind urls4irl'));

    await waitFor(() => expect(button('Unbind worldwide')).toHaveFocus());
  });

  it('falls back to the bindings heading when the last binding is unbound, and shows the empty state', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValueOnce([BINDING_THIRD]).mockResolvedValue([]),
        unbindApp: vi.fn().mockResolvedValue({ appId: 'worldwide', deleted: true }),
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Unbind worldwide'));
    await user.click(button('Confirm unbind worldwide'));

    await waitFor(() => expect(bindingsHeading()).toHaveFocus());
    expect(screen.getByText(strings.identitiesBindingsEmpty)).toBeInTheDocument();
  });

  it('says when the app was already unbound', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING_THIRD]),
        unbindApp: vi.fn().mockResolvedValue({ appId: 'worldwide', deleted: false }),
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Unbind worldwide'));
    await user.click(button('Confirm unbind worldwide'));

    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(strings.identityAlreadyUnbound({ app: 'worldwide' })),
    );
  });

  it('shows the server error on a failed unbind and keeps the row', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([BINDING_THIRD]),
        unbindApp: vi
          .fn()
          .mockRejectedValue(new ApiError({ message: 'identity unavailable', status: 503 })),
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Unbind worldwide'));
    await user.click(button('Confirm unbind worldwide'));

    expect(await within(bindingRow('worldwide')).findByRole('alert')).toHaveTextContent(
      strings.identityUnbindFailed({ message: 'identity unavailable' }),
    );
    expect(button('Unbind worldwide')).not.toHaveAttribute('aria-disabled');
  });

  it('blocks a second click while an unbind is in flight', async () => {
    const user = userEvent.setup();
    const pending = deferred<{ appId: string; deleted: boolean }>();
    const unbind = vi.fn().mockReturnValue(pending.promise);
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValueOnce([BINDING_THIRD]).mockResolvedValue([]),
        unbindApp: unbind,
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Unbind worldwide'));
    await user.click(button('Confirm unbind worldwide'));
    const busy = button('Unbinding worldwide');
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await user.click(busy);

    expect(unbind).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('button', { name: /^Confirm unbind/ })).toBeNull();
    pending.resolve({ appId: 'worldwide', deleted: true });
    await waitFor(() => expect(document.querySelector('tr[data-app-id="worldwide"]')).toBeNull());
  });
});

describe('AppIdentities focus ownership and quiet reloads', () => {
  it('does not steal focus from another row when an approve settles', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityBinding>();
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REQUEST_1, REQUEST_2, REQUEST_3])
          .mockResolvedValue([REQUEST_2, REQUEST_3]),
        approveIdentityRequest: vi.fn().mockReturnValue(pending.promise),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));
    // The operator moves on to another row while the approve is in flight.
    act(() => {
      button('Approve identity request for app_3').focus();
    });
    pending.resolve(buildBinding({ appId: 'app_1' }));

    await waitFor(() => expect(document.querySelector('tr[data-request-id="1"]')).toBeNull());
    // The neighbour (app_2) would have got focus had the guard not held.
    expect(button('Approve identity request for app_3')).toHaveFocus();
  });

  it('reloads quietly: the server state wins, nothing flashes loading and an open confirm survives', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityBinding>();
    const filedMeanwhile = buildRequest({ id: 4 });
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REQUEST_1, REQUEST_2])
          .mockResolvedValue([REQUEST_2, filedMeanwhile]),
        approveIdentityRequest: vi.fn().mockReturnValue(pending.promise),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));
    await user.click(button('Reject identity request for app_2'));
    expect(button('Confirm reject identity request for app_2')).toBeInTheDocument();
    pending.resolve(buildBinding({ appId: 'app_1' }));

    // The reload brought in a request filed meanwhile.
    expect(await screen.findByText('4IRL/app_4')).toBeInTheDocument();
    expect(screen.queryByText(strings.identitiesLoading)).toBeNull();
    // A loud reload would have unmounted the tables and closed this confirm.
    const dialog = screen.getByRole('dialog', { name: strings.rejectIdentityDialogTitle });
    expect(within(dialog).getByText('app_2')).toBeInTheDocument();
    expect(button('Confirm reject identity request for app_2')).toBeInTheDocument();
    expect(button('Cancel reject identity request for app_2')).toHaveFocus();
  });

  it('closes an open reject dialog without firing when a reload drops the request', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityBinding>();
    const reject = vi.fn();
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REQUEST_1, REQUEST_2])
          .mockResolvedValue([REQUEST_1]),
        approveIdentityRequest: vi.fn().mockReturnValue(pending.promise),
        rejectIdentityRequest: reject,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));
    await user.click(button('Reject identity request for app_2'));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    pending.resolve(buildBinding({ appId: 'app_1' }));

    await waitFor(() => expect(document.querySelector('tr[data-request-id="2"]')).toBeNull());
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(reject).not.toHaveBeenCalled();
  });

  it('keeps the lists and shows no alert when the reload after a mutation fails', async () => {
    const user = userEvent.setup();
    const listIdentityRequests = vi
      .fn()
      .mockResolvedValueOnce([REQUEST_1, REQUEST_2])
      .mockRejectedValue(new ApiError({ message: 'reload failed', status: 503 }));
    renderSection(
      buildClient({
        listIdentityRequests,
        approveIdentityRequest: vi.fn().mockResolvedValue(buildBinding({ appId: 'app_1' })),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));

    await waitFor(() => expect(listIdentityRequests).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(resultLine()).toHaveTextContent('Approved app_1'));
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByText(strings.identitiesLoading)).toBeNull();
    expect(button('Approve identity request for app_2')).toBeInTheDocument();
    expect(bindingRow('app_1')).toBeInTheDocument();
  });
});

describe('AppIdentities key and request text', () => {
  function renderKey(overrides: Partial<IdentityKey>) {
    const identityKey = buildKey({
      id: 41,
      appId: 'tasktracker',
      keyHint: 'dak_Tx41',
      ...overrides,
    });
    return renderSection(
      buildClient({
        listIdentityBindings: vi
          .fn()
          .mockResolvedValue([buildBinding({ appId: 'tasktracker', keys: [identityKey] })]),
      }),
    );
  }

  it.each([
    ['operator', 'revoked 2026-10-20 09:00 UTC by operator'],
    ['unbound', 'revoked 2026-10-20 09:00 UTC when unbound'],
    ['superseded', 'revoked 2026-10-20 09:00 UTC after its 24 h grace period'],
    ['made-up-reason', 'revoked 2026-10-20 09:00 UTC made-up-reason'],
    [null, 'revoked 2026-10-20 09:00 UTC'],
  ])('words a key revoked for %s', async (reason, expected) => {
    renderKey({ state: 'revoked', revokedAt: '2026-10-20T09:00:00Z', revokedReason: reason });

    expect(await screen.findByText(expected)).toBeInTheDocument();
  });

  it('falls back to the issue time for a revoked key without a revoke time', async () => {
    renderKey({ state: 'revoked', revokedAt: null, revokedReason: 'operator' });

    expect(await screen.findByText('revoked 2026-10-21 16:40 UTC by operator')).toBeInTheDocument();
  });

  it.each([null, 'not-a-date'])(
    'falls back to the issue text for a superseded key with supersededAt %s',
    async (supersededAt) => {
      renderKey({ state: 'superseded', supersededAt });

      expect(
        await screen.findByText('issued 2026-10-21 16:40 UTC · never used'),
      ).toBeInTheDocument();
    },
  );

  it('words last used exactly relative to now', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    try {
      vi.setSystemTime(new Date('2026-10-21T18:00:00Z'));
      renderKey({ lastUsedAt: '2026-10-21T17:58:00Z' });

      expect(
        await screen.findByText(
          `issued 2026-10-21 16:40 UTC · last used ${strings.relativeMinutesAgo({ minutes: 2 })}`,
        ),
      ).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it('still offers Revoke on a key in an unknown state', async () => {
    renderKey({ state: 'unknown', keyHint: 'dak_Odd1' });

    expect(await screen.findByText(strings.identityKeyUnknown)).toBeInTheDocument();
    expect(button('Revoke key dak_Odd1 for tasktracker')).toBeInTheDocument();
  });

  it('explains how rejected requests behave once one is listed', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValue([buildRequest({ id: 1, status: 'rejected' })]),
      }),
    );

    expect(await screen.findByText(strings.identitiesRejectedNote)).toBeInTheDocument();
  });
});

describe('AppIdentities action races', () => {
  it('ignores a revoke that settles after its app was unbound', async () => {
    const user = userEvent.setup();
    const revoking = deferred<IdentityKey>();
    renderSection(
      buildClient({
        listIdentityBindings: vi
          .fn()
          .mockResolvedValueOnce([BINDING_TASKTRACKER, BINDING_SECOND])
          .mockResolvedValue([BINDING_SECOND]),
        revokeIdentityKey: vi.fn().mockReturnValue(revoking.promise),
        unbindApp: vi.fn().mockResolvedValue({ appId: 'tasktracker', deleted: true }),
      }),
    );
    await screen.findByRole('table');

    await user.click(button('Revoke key dak_7Q2m for tasktracker'));
    await user.click(button('Confirm revoke key dak_7Q2m for tasktracker'));
    await user.click(button('Unbind tasktracker'));
    await user.click(button('Confirm unbind tasktracker'));
    await waitFor(() => expect(document.querySelector('tr[data-app-id="tasktracker"]')).toBeNull());

    revoking.resolve({ ...KEY_ACTIVE, state: 'revoked', revokedReason: 'operator' });
    await act(async () => {
      await revoking.promise;
    });

    expect(resultLine()).toHaveTextContent(strings.identityUnbound({ app: 'tasktracker' }));
    expect(document.querySelector('tr[data-app-id="tasktracker"]')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('replaces, never duplicates, an app that is already bound and keeps the list sorted', async () => {
    const user = userEvent.setup();
    const reload = deferred<IdentityBinding[]>();
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([buildRequest({ id: 1, appId: 'urls4irl' })])
          .mockResolvedValue([]),
        // Held open so the optimistic list, not the reload, is what is on screen.
        listIdentityBindings: vi
          .fn()
          .mockResolvedValueOnce([BINDING_THIRD, BINDING_SECOND])
          .mockReturnValue(reload.promise),
        approveIdentityRequest: vi
          .fn()
          .mockResolvedValue(buildBinding({ appId: 'urls4irl', keys: [] })),
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for urls4irl'));

    await waitFor(() => expect(document.querySelector('tr[data-request-id="1"]')).toBeNull());
    const rows = Array.from(document.querySelectorAll('tr[data-app-id]')).map((row) =>
      row.getAttribute('data-app-id'),
    );
    expect(rows).toEqual(['urls4irl', 'worldwide']);
    // The approved binding replaced the one that had a key.
    expect(within(bindingRow('urls4irl')).getByText(strings.identityNoKeys)).toBeInTheDocument();
  });

  it('Refresh clears the result line and reloads', async () => {
    const user = userEvent.setup();
    const client = buildClient({
      listIdentityRequests: vi.fn().mockResolvedValueOnce([REQUEST_1]).mockResolvedValue([]),
      approveIdentityRequest: vi.fn().mockResolvedValue(buildBinding({ appId: 'app_1' })),
    });
    renderSection(client);
    await screen.findAllByRole('table');
    await user.click(button('Approve identity request for app_1'));
    await waitFor(() => expect(resultLine()).toHaveTextContent('Approved app_1'));

    await user.click(button(strings.identitiesRefresh));

    await waitFor(() => expect(resultLine()).toBeEmptyDOMElement());
    await screen.findByText(strings.identitiesRequestsEmpty);
  });
});

describe('AppIdentities times in a non-UTC zone', () => {
  useTimeZone('America/Los_Angeles');

  it('shows the request row time in the local zone with the UTC instant as its title', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValue([buildRequest({ id: 1, createdAt: '2026-10-21T18:04:00Z' })]),
      }),
    );

    const requested = await screen.findByText('2026-10-21 11:04 PDT');
    expect(requested.tagName).toBe('TIME');
    expect(requested).toHaveAttribute('datetime', '2026-10-21T18:04:00.000Z');
    expect(requested).toHaveAttribute('title', '2026-10-21T18:04:00.000Z');
  });

  it('shows the approved time and every key sub-line time with the PDT abbreviation', async () => {
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([BINDING_TASKTRACKER]) }),
    );

    await screen.findByRole('table');
    const row = bindingRow('tasktracker');
    expect(within(row).getByText('approved 2026-10-19 02:12 PDT')).toBeInTheDocument();
    expect(within(row).getByText(/^issued 2026-10-21 09:40 PDT · last used /)).toBeInTheDocument();
    expect(
      within(row).getByText('stops working 2026-10-22 09:40 PDT · never used'),
    ).toBeInTheDocument();
    expect(within(row).getByText('revoked 2026-10-20 02:00 PDT by operator')).toBeInTheDocument();
  });
});

describe('AppIdentities refresh signal and approval callback', () => {
  it('loads once on mount and reloads exactly once more when the signal changes', async () => {
    const client = buildClient();
    const { rerender } = render(<AppIdentities client={client} refreshSignal={0} />);
    await screen.findByText(strings.identitiesRequestsEmpty);
    expect(client.listIdentityRequests).toHaveBeenCalledTimes(1);
    expect(client.listIdentityBindings).toHaveBeenCalledTimes(1);

    rerender(<AppIdentities client={client} refreshSignal={1} />);

    await waitFor(() => expect(client.listIdentityRequests).toHaveBeenCalledTimes(2));
    expect(client.listIdentityBindings).toHaveBeenCalledTimes(2);
  });

  it('calls onBindingApproved once on a successful approval', async () => {
    const user = userEvent.setup();
    const onBindingApproved = vi.fn();
    render(
      <AppIdentities
        client={buildClient({
          listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
          approveIdentityRequest: vi.fn().mockResolvedValue(buildBinding({ appId: 'app_1' })),
        })}
        onBindingApproved={onBindingApproved}
      />,
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));

    await waitFor(() => expect(onBindingApproved).toHaveBeenCalledTimes(1));
  });

  it('does not call onBindingApproved when the approval fails with a 409', async () => {
    const user = userEvent.setup();
    const onBindingApproved = vi.fn();
    render(
      <AppIdentities
        client={buildClient({
          listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
          approveIdentityRequest: vi
            .fn()
            .mockRejectedValue(new ApiError({ message: 'request is not pending', status: 409 })),
        })}
        onBindingApproved={onBindingApproved}
      />,
    );
    await screen.findAllByRole('table');

    await user.click(button('Approve identity request for app_1'));

    expect(await within(requestRow(1)).findByRole('alert')).toBeInTheDocument();
    expect(onBindingApproved).not.toHaveBeenCalled();
  });
});

function buildRegistrar(overrides: Partial<IdentityRegistrar> & { id: number }): IdentityRegistrar {
  return {
    prefix: 'hosted_',
    githubRepository: '4IRL/hosting',
    githubRepositoryId: 900100,
    githubOwnerId: 55,
    workflowPath: '.github/workflows/create-app.yml',
    gitRef: 'refs/heads/main',
    environment: 'production',
    approvedAt: '2026-10-22T09:00:00Z',
    appCount: 3,
    appLimit: 25,
    ...overrides,
  };
}

const REGISTRAR_REQUEST = buildRequest({
  id: 7,
  kind: 'registrar',
  appId: null,
  githubRepository: '4IRL/hosting',
});
const REGISTRAR_HOSTED = buildRegistrar({ id: 1 });
const APPROVE_REGISTRAR = strings.approveRegistrarNamed({ repository: '4IRL/hosting' });

function registrarRow(id: number): HTMLElement {
  const row = document.querySelector<HTMLElement>(`tr[data-registrar-id="${id}"]`);
  if (row === null) {
    throw new Error(`registrar row ${id} not found`);
  }
  return row;
}

describe('AppIdentities registrars', () => {
  it('lists registrars with the server cap', async () => {
    renderSection(buildClient({ listRegistrars: vi.fn().mockResolvedValue([REGISTRAR_HOSTED]) }));
    await screen.findByRole('heading', { name: strings.identitiesRegistrarsHeading });
    const row = registrarRow(1);
    expect(row).toHaveTextContent('hosted_');
    expect(row).toHaveTextContent('3');
    expect(row).toHaveTextContent('25');
  });

  it('shows the empty registrars state', async () => {
    renderSection(buildClient());
    expect(await screen.findByText(strings.identitiesRegistrarsEmpty)).toBeInTheDocument();
  });

  it('offers registrar approve and reject named from the repository', async () => {
    renderSection(
      buildClient({ listIdentityRequests: vi.fn().mockResolvedValue([REGISTRAR_REQUEST]) }),
    );
    await screen.findAllByRole('table');
    expect(button(APPROVE_REGISTRAR)).toBeInTheDocument();
    expect(
      button(strings.rejectRegistrarNamed({ repository: '4IRL/hosting' })),
    ).toBeInTheDocument();
  });

  it('shows an unknown request kind with no actions', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([buildRequest({ id: 9, kind: 'unknown' })]),
      }),
    );
    await screen.findAllByRole('table');
    expect(within(requestRow(9)).queryByRole('button')).toBeNull();
    expect(within(requestRow(9)).getAllByText(strings.identityUnknownRequestPill)).toHaveLength(1);
  });

  it('refuses platform_ and a bad grammar client-side, sending nothing', async () => {
    const user = userEvent.setup();
    const approve = vi.fn();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REGISTRAR_REQUEST]),
        approveRegistrarRequest: approve,
      }),
    );
    await screen.findAllByRole('table');
    await user.click(button(APPROVE_REGISTRAR));
    const dialog = screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle });
    const input = within(dialog).getByLabelText(strings.approveRegistrarPrefixLabel);
    expect(input).toHaveFocus();

    await user.type(input, 'platform_x_');
    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));
    expect(within(dialog).getByText(strings.prefixReservedPlatform)).toBeInTheDocument();

    await user.clear(input);
    await user.type(input, 'Bad');
    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));
    expect(within(dialog).getByText(strings.prefixInvalidGrammar)).toBeInTheDocument();
    expect(approve).not.toHaveBeenCalled();
  });

  it('approves with a trimmed prefix, lists the registrar and focuses its delete', async () => {
    const user = userEvent.setup();
    const approve = vi.fn().mockResolvedValue(REGISTRAR_HOSTED);
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REGISTRAR_REQUEST])
          .mockResolvedValue([]),
        listRegistrars: vi.fn().mockResolvedValueOnce([]).mockResolvedValue([REGISTRAR_HOSTED]),
        approveRegistrarRequest: approve,
      }),
    );
    await screen.findAllByRole('table');
    await user.click(button(APPROVE_REGISTRAR));
    const dialog = screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle });
    await user.type(
      within(dialog).getByLabelText(strings.approveRegistrarPrefixLabel),
      '  hosted_ ',
    );
    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));

    await waitFor(() => expect(approve).toHaveBeenCalledWith({ id: 7, prefix: 'hosted_' }));
    await waitFor(() => expect(registrarRow(1)).toBeInTheDocument());
    await waitFor(() =>
      expect(button(strings.deleteRegistrarNamed({ prefix: 'hosted_' }))).toHaveFocus(),
    );
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('keeps a prefix-class error under the field and closes on a request-class error', async () => {
    const user = userEvent.setup();
    const approve = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ message: 'prefix already used', status: 409 }))
      .mockRejectedValueOnce(new ApiError({ message: 'request is not pending', status: 409 }));
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REGISTRAR_REQUEST]),
        approveRegistrarRequest: approve,
      }),
    );
    await screen.findAllByRole('table');
    await user.click(button(APPROVE_REGISTRAR));
    const dialog = screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle });
    const input = within(dialog).getByLabelText(strings.approveRegistrarPrefixLabel);
    await user.type(input, 'hosted_');
    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));
    expect(await within(dialog).findByText('prefix already used')).toBeInTheDocument();
    expect(input).toHaveFocus();

    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    // A row-gone error goes to the result region (error variant), never under the field.
    expect(requestsHeading()).toHaveFocus();
    expect(resultLine()).toHaveClass('app-identities__result--error');
    expect(resultLine()).toHaveTextContent('request is not pending');
  });

  it('puts a workflow-already-bound error on the row and refocuses the approve trigger', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REGISTRAR_REQUEST]),
        approveRegistrarRequest: vi
          .fn()
          .mockRejectedValue(
            new ApiError({ message: 'workflow identity already bound', status: 409 }),
          ),
      }),
    );
    await screen.findAllByRole('table');
    await user.click(button(APPROVE_REGISTRAR));
    const dialog = screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle });
    await user.type(within(dialog).getByLabelText(strings.approveRegistrarPrefixLabel), 'hosted_');
    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(within(requestRow(7)).getByRole('alert')).toHaveTextContent(
      'workflow identity already bound',
    );
    expect(button(APPROVE_REGISTRAR)).toHaveFocus();
  });

  it('ignores Escape while the approve is in flight and shows Approving…', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityRegistrar>();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REGISTRAR_REQUEST]),
        approveRegistrarRequest: vi.fn().mockReturnValue(pending.promise),
      }),
    );
    await screen.findAllByRole('table');
    await user.click(button(APPROVE_REGISTRAR));
    const dialog = screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle });
    await user.type(within(dialog).getByLabelText(strings.approveRegistrarPrefixLabel), 'hosted_');
    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));

    const busy = await within(dialog).findByRole('button', { name: strings.approvingRegistrar });
    expect(busy).toHaveAttribute('aria-disabled', 'true');
    await user.keyboard('{Escape}');
    expect(screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle })).toBeVisible();
    await act(async () => {
      pending.resolve(REGISTRAR_HOSTED);
    });
  });

  it('ignores a hashchange while the approve is in flight', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityRegistrar>();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REGISTRAR_REQUEST]),
        approveRegistrarRequest: vi.fn().mockReturnValue(pending.promise),
      }),
    );
    await screen.findAllByRole('table');
    await user.click(button(APPROVE_REGISTRAR));
    const dialog = screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle });
    await user.type(within(dialog).getByLabelText(strings.approveRegistrarPrefixLabel), 'hosted_');
    await user.click(within(dialog).getByRole('button', { name: strings.approveRegistrarConfirm }));
    await within(dialog).findByRole('button', { name: strings.approvingRegistrar });

    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle })).toBeVisible();
    await act(async () => {
      pending.resolve(REGISTRAR_HOSTED);
    });
  });

  it('returns focus to the trigger on Cancel and on Escape (restoreFocus stays true)', async () => {
    const user = userEvent.setup();
    const approve = vi.fn();
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REGISTRAR_REQUEST]),
        approveRegistrarRequest: approve,
      }),
    );
    await screen.findAllByRole('table');

    await user.click(button(APPROVE_REGISTRAR));
    await user.click(
      within(screen.getByRole('dialog', { name: strings.approveRegistrarDialogTitle })).getByRole(
        'button',
        { name: 'Cancel' },
      ),
    );
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(button(APPROVE_REGISTRAR)).toHaveFocus();

    await user.click(button(APPROVE_REGISTRAR));
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(button(APPROVE_REGISTRAR)).toHaveFocus();
    expect(approve).not.toHaveBeenCalled();
  });

  it('rejects a registrar request with registrar wording and focuses the requests heading', async () => {
    const user = userEvent.setup();
    const reject = vi.fn().mockResolvedValue({ ...REGISTRAR_REQUEST, status: 'rejected' });
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REGISTRAR_REQUEST])
          .mockResolvedValue([{ ...REGISTRAR_REQUEST, status: 'rejected' }]),
        rejectIdentityRequest: reject,
      }),
    );
    await screen.findAllByRole('table');
    const repository = '4IRL/hosting';
    await user.click(button(strings.rejectRegistrarNamed({ repository })));
    await user.click(button(strings.confirmRejectRegistrarNamed({ repository })));

    await waitFor(() => expect(reject).toHaveBeenCalledWith({ id: 7 }));
    await waitFor(() => expect(requestsHeading()).toHaveFocus());
    expect(resultLine()).toHaveTextContent(strings.identityRegistrarRejected({ repository }));
    expect(document.body.textContent).not.toContain('null');
  });

  it('moves focus to the neighbouring registrar request when an app request is approved', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityRequests: vi
          .fn()
          .mockResolvedValueOnce([REQUEST_1, REGISTRAR_REQUEST])
          .mockResolvedValue([REGISTRAR_REQUEST]),
        approveIdentityRequest: vi.fn().mockResolvedValue(buildBinding({ appId: 'app_1' })),
      }),
    );
    await screen.findAllByRole('table');
    await user.click(button('Approve identity request for app_1'));

    await waitFor(() => expect(button(APPROVE_REGISTRAR)).toHaveFocus());
  });

  it('focuses the neighbour registrar Delete after a delete, or the heading for the last one', async () => {
    const user = userEvent.setup();
    const second = buildRegistrar({ id: 2, prefix: 'other_' });
    renderSection(
      buildClient({
        listRegistrars: vi
          .fn()
          .mockResolvedValueOnce([REGISTRAR_HOSTED, second])
          .mockResolvedValue([second]),
        deleteRegistrar: vi.fn().mockResolvedValue({ id: 1, deleted: true, unboundApps: 0 }),
      }),
    );
    await screen.findByRole('heading', { name: strings.identitiesRegistrarsHeading });
    await user.click(button(strings.deleteRegistrarNamed({ prefix: 'hosted_' })));
    await user.click(button(strings.confirmDeleteRegistrarNamed({ prefix: 'hosted_' })));
    await waitFor(() =>
      expect(button(strings.deleteRegistrarNamed({ prefix: 'other_' }))).toHaveFocus(),
    );
  });

  it('focuses the Registrars heading after deleting the last registrar', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listRegistrars: vi.fn().mockResolvedValueOnce([REGISTRAR_HOSTED]).mockResolvedValue([]),
        deleteRegistrar: vi.fn().mockResolvedValue({ id: 1, deleted: true, unboundApps: 0 }),
      }),
    );
    await screen.findByRole('heading', { name: strings.identitiesRegistrarsHeading });
    await user.click(button(strings.deleteRegistrarNamed({ prefix: 'hosted_' })));
    await user.click(button(strings.confirmDeleteRegistrarNamed({ prefix: 'hosted_' })));
    await waitFor(() =>
      expect(
        screen.getByRole('heading', { name: strings.identitiesRegistrarsHeading }),
      ).toHaveFocus(),
    );
  });

  it('shows a whole-section error with Retry when listRegistrars rejects', async () => {
    renderSection(buildClient({ listRegistrars: vi.fn().mockRejectedValue(new Error('network')) }));
    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(button(strings.identitiesRetry)).toBeInTheDocument();
  });

  it('shows a registrar row error when a delete fails', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listRegistrars: vi.fn().mockResolvedValue([REGISTRAR_HOSTED]),
        deleteRegistrar: vi.fn().mockRejectedValue(new ApiError({ message: 'boom', status: 500 })),
      }),
    );
    await screen.findByRole('heading', { name: strings.identitiesRegistrarsHeading });
    await user.click(button(strings.deleteRegistrarNamed({ prefix: 'hosted_' })));
    await user.click(button(strings.confirmDeleteRegistrarNamed({ prefix: 'hosted_' })));
    expect(await within(registrarRow(1)).findByRole('alert')).toHaveTextContent('boom');
  });

  it('announces the neutral already-deleted result when deleted is false', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listRegistrars: vi.fn().mockResolvedValueOnce([REGISTRAR_HOSTED]).mockResolvedValue([]),
        deleteRegistrar: vi.fn().mockResolvedValue({ id: 1, deleted: false, unboundApps: 0 }),
      }),
    );
    await screen.findByRole('heading', { name: strings.identitiesRegistrarsHeading });
    await user.click(button(strings.deleteRegistrarNamed({ prefix: 'hosted_' })));
    await user.click(button(strings.confirmDeleteRegistrarNamed({ prefix: 'hosted_' })));
    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(
        strings.registrarAlreadyDeleted({ prefix: 'hosted_' }),
      ),
    );
  });

  it('deletes a registrar after a confirm and reports the unbound count', async () => {
    const user = userEvent.setup();
    const remove = vi.fn().mockResolvedValue({ id: 1, deleted: true, unboundApps: 3 });
    renderSection(
      buildClient({
        listRegistrars: vi.fn().mockResolvedValueOnce([REGISTRAR_HOSTED]).mockResolvedValue([]),
        deleteRegistrar: remove,
      }),
    );
    await screen.findByRole('heading', { name: strings.identitiesRegistrarsHeading });
    await user.click(button(strings.deleteRegistrarNamed({ prefix: 'hosted_' })));
    await user.click(button(strings.confirmDeleteRegistrarNamed({ prefix: 'hosted_' })));

    await waitFor(() => expect(remove).toHaveBeenCalledWith({ id: 1 }));
    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(
        strings.registrarDeleted({ prefix: 'hosted_', count: 3 }),
      ),
    );
  });
});

const HOSTED_REGISTRAR_REF = { id: 1, prefix: 'hosted_' };
const NEW_A = buildBinding({
  appId: 'hosted_a',
  registrar: HOSTED_REGISTRAR_REF,
  acknowledgedAt: null,
});
const NEW_B = buildBinding({
  appId: 'hosted_b',
  registrar: HOSTED_REGISTRAR_REF,
  acknowledgedAt: null,
});
const SEEN_C = buildBinding({
  appId: 'hosted_c',
  registrar: HOSTED_REGISTRAR_REF,
  acknowledgedAt: '2026-10-22T10:00:00Z',
});
const MARK_ALL = strings.markAllSeenAria;

/** The acknowledged copy of a binding, as the server would return it. */
function seen(binding: IdentityBinding): IdentityBinding {
  return { ...binding, acknowledgedAt: '2026-10-22T11:00:00Z' };
}

describe('AppIdentities registrar app markers', () => {
  it('marks a new registrar app with text, and leaves seen and normal rows unmarked', async () => {
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, SEEN_C, BINDING_THIRD]),
      }),
    );
    await screen.findByRole('table');

    const newRow = bindingRow('hosted_a');
    expect(newRow).toHaveClass('app-identities__row--new');
    expect(newRow).toHaveTextContent(`${strings.identityStatusLabel}${strings.identityNewMark}`);
    expect(newRow).toHaveTextContent(
      `${strings.identityCreatedByLabel}${strings.identityViaRegistrar({ prefix: 'hosted_' })}`,
    );
    expect(newRow).toHaveTextContent('created automatically');
    expect(newRow).not.toHaveTextContent('approved');

    const seenRow = bindingRow('hosted_c');
    expect(seenRow).not.toHaveClass('app-identities__row--new');
    expect(seenRow).not.toHaveTextContent(strings.identityNewMark);
    expect(seenRow).toHaveTextContent(strings.identityViaRegistrar({ prefix: 'hosted_' }));
    expect(seenRow).toHaveTextContent('created automatically');

    const normalRow = bindingRow('worldwide');
    expect(normalRow).not.toHaveClass('app-identities__row--new');
    expect(normalRow).not.toHaveTextContent(strings.identityNewMark);
    expect(normalRow).not.toHaveTextContent('via ');
    expect(normalRow).not.toHaveTextContent('Created by registrar');
    expect(normalRow).toHaveTextContent('approved');
  });

  it('appends the registrar creates-it-again suffix to the Unbind notice of a registrar app only', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([SEEN_C, BINDING_THIRD]) }),
    );
    await screen.findByRole('table');

    await user.click(button('Unbind hosted_c'));
    expect(
      screen.getByText(strings.unbindRegistrarAppNotice({ prefix: 'hosted_' })),
    ).toHaveTextContent('Its registrar hosted_ creates it again on the next deploy.');
    await user.click(button('Cancel unbind hosted_c'));

    await user.click(button('Unbind worldwide'));
    expect(screen.getByText(strings.unbindAppNotice)).toBeInTheDocument();
    expect(screen.queryByText(/creates it again/)).toBeNull();
  });

  it('words the summary for 0, 1 and many of each part, omitting a zero part', async () => {
    const pendingMany = [REQUEST_1, REQUEST_2];
    const cases: Array<{
      requests: IdentityRequest[];
      bindings: IdentityBinding[];
      text: string;
    }> = [
      { requests: [], bindings: [SEEN_C], text: '' },
      { requests: [REQUEST_1], bindings: [], text: '1 request awaiting approval' },
      { requests: pendingMany, bindings: [], text: '2 requests awaiting approval' },
      { requests: [], bindings: [NEW_A], text: '1 new app' },
      { requests: [], bindings: [NEW_A, NEW_B], text: '2 new apps' },
      {
        requests: [REQUEST_1],
        bindings: [NEW_A],
        text: '1 request awaiting approval · 1 new app',
      },
      {
        requests: pendingMany,
        bindings: [NEW_A, NEW_B],
        text: '2 requests awaiting approval · 2 new apps',
      },
    ];
    for (const scenario of cases) {
      cleanup();
      renderSection(
        buildClient({
          listIdentityRequests: vi.fn().mockResolvedValue(scenario.requests),
          listIdentityBindings: vi.fn().mockResolvedValue(scenario.bindings),
        }),
      );
      await screen.findByRole('heading', { name: strings.identitiesBindingsHeading });
      expect(summary().textContent).toBe(scenario.text);
    }
  });

  it('renders the new-apps part inside aria-live=off', async () => {
    renderSection(
      buildClient({
        listIdentityRequests: vi.fn().mockResolvedValue([REQUEST_1]),
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A]),
      }),
    );
    await screen.findByRole('table', { name: strings.identitiesBindingsHeading });
    const part = within(summary()).getByText(/1 new app/);
    expect(part).toHaveAttribute('aria-live', 'off');
    expect(summary()).toHaveAttribute('aria-live', 'polite');
    expect(within(part).queryByText(/awaiting approval/)).toBeNull();
  });
});

describe('AppIdentities Mark seen', () => {
  it('keeps the marker after a failed acknowledge and shows the row error', async () => {
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A]),
        acknowledgeBinding: vi
          .fn()
          .mockRejectedValue(new ApiError({ status: 500, message: 'boom' })),
      }),
    );
    await screen.findByRole('table');
    await user.click(button(strings.markSeenNamed({ app: 'hosted_a' })));

    expect(await within(bindingRow('hosted_a')).findByRole('alert')).toHaveTextContent('boom');
    expect(bindingRow('hosted_a')).toHaveClass('app-identities__row--new');
    expect(bindingRow('hosted_a')).toHaveTextContent(strings.identityNewMark);
  });

  it('removes the marker, moves focus to Unbind and announces in the result region (wide)', async () => {
    const user = userEvent.setup();
    const acknowledge = vi.fn().mockResolvedValue(seen(NEW_A));
    renderSection(
      buildClient({
        listIdentityBindings: vi
          .fn()
          .mockResolvedValueOnce([NEW_A])
          .mockResolvedValue([seen(NEW_A)]),
        acknowledgeBinding: acknowledge,
      }),
    );
    await screen.findByRole('table');
    expect(summary().textContent).toBe('1 new app');

    await user.click(button(strings.markSeenNamed({ app: 'hosted_a' })));

    await waitFor(() => expect(button('Unbind hosted_a')).toHaveFocus());
    expect(acknowledge).toHaveBeenCalledWith({ appId: 'hosted_a' });
    expect(bindingRow('hosted_a')).not.toHaveTextContent(strings.identityNewMark);
    expect(bindingRow('hosted_a')).not.toHaveClass('app-identities__row--new');
    expect(resultLine()).toHaveTextContent(strings.identityMarkedSeen({ app: 'hosted_a' }));
    // The summary updates silently: the result region is the only announcement.
    expect(summary().textContent).toBe('');
  });

  it('puts Mark seen in the kebab and returns focus to the kebab trigger (compact)', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, SEEN_C]),
        acknowledgeBinding: vi.fn().mockResolvedValue(seen(NEW_A)),
      }),
    );
    await screen.findByRole('table');
    expect(screen.queryByRole('button', { name: 'Mark hosted_a seen' })).toBeNull();

    await user.click(button('Actions for hosted_a'));
    await user.click(screen.getByRole('menuitem', { name: 'Mark hosted_a seen' }));

    await waitFor(() => expect(button('Actions for hosted_a')).toHaveFocus());
    expect(bindingRow('hosted_a')).not.toHaveTextContent(strings.identityNewMark);
    expect(resultLine()).toHaveTextContent(strings.identityMarkedSeen({ app: 'hosted_a' }));

    await user.click(button('Actions for hosted_a'));
    expect(screen.queryByRole('menuitem', { name: 'Mark hosted_a seen' })).toBeNull();
  });

  it('treats a 404 as a neutral result, not a row error, and reloads', async () => {
    const user = userEvent.setup();
    const list = vi.fn().mockResolvedValueOnce([NEW_A, SEEN_C]).mockResolvedValue([SEEN_C]);
    renderSection(
      buildClient({
        listIdentityBindings: list,
        acknowledgeBinding: vi
          .fn()
          .mockRejectedValue(new ApiError({ status: 404, message: 'binding does not exist' })),
      }),
    );
    await screen.findByRole('table');
    await user.click(button('Mark hosted_a seen'));

    await waitFor(() =>
      expect(resultLine()).toHaveTextContent(strings.identityNoLongerBound({ app: 'hosted_a' })),
    );
    await waitFor(() => expect(document.querySelector('tr[data-app-id="hosted_a"]')).toBeNull());
    expect(screen.queryByRole('alert')?.textContent ?? '').toBe('');
    expect(resultLine()).not.toHaveClass('app-identities__result--error');
  });

  it('is aria-disabled while another ack is in flight and cannot start a second', async () => {
    const user = userEvent.setup();
    const pending = deferred<IdentityBinding>();
    const acknowledge = vi.fn().mockReturnValue(pending.promise);
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, NEW_B]),
        acknowledgeBinding: acknowledge,
        acknowledgeAllBindings: vi.fn(),
      }),
    );
    await screen.findByRole('table');
    await user.click(button('Mark hosted_a seen'));

    await waitFor(() => expect(button(MARK_ALL)).toHaveAttribute('aria-disabled', 'true'));
    expect(button('Mark hosted_b seen')).toHaveAttribute('aria-disabled', 'true');
    await user.click(button('Mark hosted_b seen'));
    await user.click(button(MARK_ALL));
    expect(acknowledge).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending.resolve(seen(NEW_A));
    });
  });
});

describe('AppIdentities Mark all seen', () => {
  it('is shown only with 2 or more New rows', async () => {
    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, NEW_B, SEEN_C]) }),
    );
    await screen.findByRole('table');
    expect(button(MARK_ALL)).toHaveTextContent(strings.markAllSeen);
    cleanup();

    renderSection(
      buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, SEEN_C]) }),
    );
    await screen.findByRole('table');
    expect(screen.queryByRole('button', { name: MARK_ALL })).toBeNull();
    cleanup();

    renderSection(buildClient({ listIdentityBindings: vi.fn().mockResolvedValue([SEEN_C]) }));
    await screen.findByRole('table');
    expect(screen.queryByRole('button', { name: MARK_ALL })).toBeNull();
  });

  it('sends exactly the displayed New ids, announces the server count and focuses the heading', async () => {
    const user = userEvent.setup();
    // The server skipped one (seen/unbound meanwhile): the count differs from the 2 displayed.
    const acknowledgeAll = vi.fn().mockResolvedValue(1);
    renderSection(
      buildClient({
        listIdentityBindings: vi
          .fn()
          .mockResolvedValueOnce([NEW_A, NEW_B, SEEN_C, BINDING_THIRD])
          .mockResolvedValue([seen(NEW_A), seen(NEW_B), SEEN_C, BINDING_THIRD]),
        acknowledgeAllBindings: acknowledgeAll,
      }),
    );
    await screen.findByRole('table');
    expect(summary().textContent).toBe('2 new apps');
    await user.click(button(MARK_ALL));

    await waitFor(() => expect(bindingsHeading()).toHaveFocus());
    expect(acknowledgeAll).toHaveBeenCalledTimes(1);
    expect(acknowledgeAll).toHaveBeenCalledWith({ appIds: ['hosted_a', 'hosted_b'] });
    expect(resultLine()).toHaveTextContent(strings.identityMarkedAllSeen({ count: 1 }));
    expect(summary().textContent).toBe('');
    expect(screen.queryByRole('button', { name: MARK_ALL })).toBeNull();
    expect(document.querySelector('.app-identities__row--new')).toBeNull();
  });

  it('is aria-disabled with every per-row Mark seen while it is in flight', async () => {
    const user = userEvent.setup();
    const pending = deferred<number>();
    const acknowledge = vi.fn();
    const acknowledgeAll = vi.fn().mockReturnValue(pending.promise);
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, NEW_B]),
        acknowledgeBinding: acknowledge,
        acknowledgeAllBindings: acknowledgeAll,
      }),
    );
    await screen.findByRole('table');
    await user.click(button(MARK_ALL));

    await waitFor(() => expect(button(MARK_ALL)).toHaveAttribute('aria-disabled', 'true'));
    expect(button('Mark hosted_a seen')).toHaveAttribute('aria-disabled', 'true');
    expect(button('Mark hosted_b seen')).toHaveAttribute('aria-disabled', 'true');
    await user.click(button('Mark hosted_a seen'));
    await user.click(button(MARK_ALL));
    expect(acknowledge).not.toHaveBeenCalled();
    expect(acknowledgeAll).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending.resolve(2);
    });
  });

  it('gates the compact kebab items while it is in flight', async () => {
    setCompactViewport(true);
    const user = userEvent.setup();
    const pending = deferred<number>();
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, NEW_B]),
        acknowledgeAllBindings: vi.fn().mockReturnValue(pending.promise),
      }),
    );
    await screen.findByRole('table');
    await user.click(button(MARK_ALL));
    await waitFor(() => expect(button(MARK_ALL)).toHaveAttribute('aria-disabled', 'true'));

    await user.click(button('Actions for hosted_a'));
    expect(screen.getByRole('menuitem', { name: 'Mark hosted_a seen' })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
    await act(async () => {
      pending.resolve(2);
    });
  });

  it('shows a failure inline above the table, keeps the page and focus, and clears on retry', async () => {
    const user = userEvent.setup();
    const acknowledgeAll = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'upstream down' }))
      .mockResolvedValue(2);
    renderSection(
      buildClient({
        listIdentityBindings: vi.fn().mockResolvedValue([NEW_A, NEW_B]),
        acknowledgeAllBindings: acknowledgeAll,
      }),
    );
    await screen.findByRole('table');
    await user.click(button(MARK_ALL));

    const alert = await screen.findByText(/upstream down/);
    expect(alert).toHaveTextContent(
      strings.identityMarkAllSeenFailed({ message: 'upstream down' }),
    );
    expect(alert.closest('[role="alert"]')).not.toBeNull();
    // Directly above the Approved apps table; the load-failure slot is not used.
    const table = screen.getByRole('table', { name: strings.identitiesBindingsHeading });
    expect(alert.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByText(/Could not load app identities/)).toBeNull();
    expect(bindingRow('hosted_a')).toHaveTextContent(strings.identityNewMark);
    expect(bindingRow('hosted_b')).toHaveTextContent(strings.identityNewMark);
    expect(button(MARK_ALL)).toHaveFocus();
    expect(button(MARK_ALL)).not.toHaveAttribute('aria-disabled', 'true');

    await user.click(button(MARK_ALL));
    await waitFor(() => expect(screen.queryByText(/upstream down/)).toBeNull());
    expect(acknowledgeAll).toHaveBeenCalledTimes(2);
  });
});

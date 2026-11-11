import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import App from './App';
import { ApiError } from './api/client';
import type { ApiClient } from './api/client';
import type { DeliveryApiClient, DestinationSummary, HeldMessages } from './api/deliveryClient';
import type { PersonApiClient } from './api/personClient';
import { strings } from './strings';

// buildFakeClient returns an ApiClient test double with vi.fn() methods,
// overridable per test.
function buildFakeClient(overrides: Partial<ApiClient> = {}): ApiClient {
  return {
    provision: vi.fn().mockResolvedValue({
      userId: 'u_abcdefgh23456777',
      appId: 'urls4irl',
      personHash: 'abcdefgh23456777',
      topicPattern: 'urls4irl-abcdefgh23456777-*',
      token: 'tk_generated',
    }),
    resetPassword: vi.fn().mockResolvedValue({
      userId: 'u_abcdefgh23456777',
      appId: 'urls4irl',
      password: 'abcd-efgh-ijkm-npqr',
    }),
    deprovision: vi.fn().mockResolvedValue(undefined),
    listUsers: vi.fn().mockResolvedValue([]),
    deleteUser: vi.fn().mockResolvedValue(undefined),
    provisionApp: vi.fn().mockImplementation(({ appId }: { appId: string }) =>
      Promise.resolve({
        appId,
        publisherUserId: `${appId}-publisher`,
        topicPattern: `${appId}-*`,
        createdAt: '2026-11-03T10:00:00Z',
        updatedAt: '2026-11-03T11:00:00Z',
      }),
    ),
    deprovisionApp: vi.fn().mockResolvedValue(undefined),
    testNotify: vi.fn().mockResolvedValue({ results: [] }),
    ...overrides,
  };
}

// buildFakePersonClient returns a PersonApiClient test double with vi.fn()
// methods, overridable per test.
function buildFakePersonClient(overrides: Partial<PersonApiClient> = {}): PersonApiClient {
  return {
    listPeople: vi.fn().mockResolvedValue([]),
    listApps: vi.fn().mockResolvedValue([]),
    createApp: vi.fn().mockResolvedValue({
      appId: 'urls4irl',
      displayName: 'URLs4IRL',
      description: null,
      createdAt: '2026-07-25T10:00:00Z',
    }),
    updateApp: vi.fn().mockResolvedValue({
      appId: 'urls4irl',
      displayName: 'URLs4IRL',
      description: null,
      createdAt: '2026-07-25T10:00:00Z',
    }),
    deleteApp: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
}

// buildFakeDeliveryClient returns a full DeliveryApiClient test double with
// vi.fn() methods that default to benign values, overridable per test.
function buildFakeDeliveryClient(overrides: Partial<DeliveryApiClient> = {}): DeliveryApiClient {
  return {
    getReadiness: vi.fn().mockResolvedValue({
      kind: 'ok',
      schemaVersion: 4,
      expectedSchemaVersion: 4,
      heartbeatChecker: { lastTickAt: '2026-10-19T09:00:00Z', stale: false },
      lastMigrationFailure: null,
    }),
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

// The Users table and the Send-test-notification table both render subscriber
// identifiers/emails, so a document-wide text query can match either. Scope
// user-row / email lookups to a specific section (found by its heading) so a
// coincidental match in the send-test table never makes these queries ambiguous.
function sectionByHeading(name: string): HTMLElement {
  const section = screen.getByRole('heading', { name }).closest('section');
  if (section === null) {
    throw new Error(`section for heading "${name}" not found`);
  }
  return section;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('App', () => {
  it('renders the app title and both sections', async () => {
    render(<App client={buildFakeClient()} />);

    expect(screen.getByRole('heading', { name: strings.appTitle })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: strings.provisionHeading })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: strings.usersHeading })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(strings.usersEmpty)).toBeInTheDocument());
  });

  it('loads and lists users on mount', async () => {
    const client = buildFakeClient({
      listUsers: vi.fn().mockResolvedValue([
        {
          userId: 'u_abcdefgh23456777',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-abcdefgh23456777-*'],
        },
      ]),
    });
    render(<App client={client} />);

    expect(
      await within(sectionByHeading(strings.usersHeading)).findByText('u_abcdefgh23456777'),
    ).toBeInTheDocument();
    expect(client.listUsers).toHaveBeenCalled();
  });

  it('provisioning a user shows the token and refreshes the user list', async () => {
    const listUsers = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockResolvedValue([
        {
          userId: 'u_abcdefgh23456777',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-abcdefgh23456777-*'],
        },
      ]);
    const client = buildFakeClient({ listUsers });
    render(<App client={client} />);

    await userEvent.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await userEvent.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await userEvent.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('tk_generated')).toBeInTheDocument();
    await waitFor(() =>
      expect(client.provision).toHaveBeenCalledWith({
        appId: 'urls4irl',
        email: 'alice@example.com',
      }),
    );
    // list refreshed: mount call + post-provision refresh
    await waitFor(() => expect(listUsers.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it('provision for an app with no approved binding shows the try-again copy and refreshes nothing', async () => {
    const listUsers = vi.fn().mockResolvedValue([]);
    const provision = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 404, message: 'app has no approved binding' }));
    const personClient = buildFakePersonClient();
    const client = buildFakeClient({ listUsers, provision });
    render(<App client={client} personClient={personClient} />);
    await waitFor(() => expect(personClient.listPeople).toHaveBeenCalledTimes(1));

    await userEvent.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await userEvent.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await userEvent.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(
      await screen.findByText(strings.appNotBoundRetryError({ appId: 'urls4irl' })),
    ).toBeInTheDocument();
    expect(screen.queryByText(/add it again/)).not.toBeInTheDocument();
    expect(listUsers).toHaveBeenCalledTimes(1);
    expect(personClient.listPeople).toHaveBeenCalledTimes(1);
  });

  it('provision shows a 404 with any other message unchanged', async () => {
    const provision = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 404, message: 'publisher not found' }));
    render(<App client={buildFakeClient({ provision })} />);

    await userEvent.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await userEvent.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await userEvent.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('publisher not found')).toBeInTheDocument();
  });

  describe('test-notify errors', () => {
    async function sendTest(testNotify: ApiClient['testNotify']) {
      const client = buildFakeClient({
        listUsers: vi
          .fn()
          .mockResolvedValue([
            { userId: 'u_76gzqgp4byjl6dje', apps: ['urls4irl'], topicPatterns: ['urls4irl-*'] },
          ]),
        testNotify,
      });
      render(<App client={client} />);
      await userEvent.click(
        await screen.findByRole('checkbox', {
          name: strings.sendTestSelectAria({ who: 'u_76gzqgp4byjl6dje' }),
        }),
      );
      await userEvent.click(screen.getByRole('button', { name: strings.sendTestAction }));
    }

    it('shows the try-again copy for a not-bound 404', async () => {
      await sendTest(
        vi
          .fn()
          .mockRejectedValue(new ApiError({ status: 404, message: 'app has no approved binding' })),
      );

      expect(
        await screen.findByText(strings.appNotBoundRetryError({ appId: 'urls4irl' })),
      ).toBeInTheDocument();
    });

    it('shows a 404 with any other message unchanged', async () => {
      await sendTest(
        vi.fn().mockRejectedValue(new ApiError({ status: 404, message: 'publisher not found' })),
      );

      expect(await screen.findByText('publisher not found')).toBeInTheDocument();
    });
  });

  it('deleting a user calls the client and refreshes', async () => {
    const listUsers = vi
      .fn()
      .mockResolvedValueOnce([{ userId: 'u_abcdefgh23456777', apps: [], topicPatterns: [] }])
      .mockResolvedValue([]);
    const client = buildFakeClient({ listUsers });
    render(<App client={client} />);

    const row = (await screen.findByText('u_abcdefgh23456777')).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deleteAction} u_abcdefgh23456777` }),
    );
    await userEvent.click(
      screen.getByRole('button', {
        name: `${strings.confirmDeleteAction} u_abcdefgh23456777`,
      }),
    );

    await waitFor(() =>
      expect(client.deleteUser).toHaveBeenCalledWith({ userId: 'u_abcdefgh23456777' }),
    );
  });

  it('shows the ApiError message when deleting a user fails', async () => {
    const listUsers = vi
      .fn()
      .mockResolvedValue([{ userId: 'u_abcdefgh23456777', apps: [], topicPatterns: [] }]);
    const deleteUser = vi
      .fn()
      .mockRejectedValue(
        new ApiError({ status: 404, message: 'user u_abcdefgh23456777 does not exist' }),
      );
    const client = buildFakeClient({ listUsers, deleteUser });
    render(<App client={client} />);

    const row = (await screen.findByText('u_abcdefgh23456777')).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deleteAction} u_abcdefgh23456777` }),
    );
    await userEvent.click(
      screen.getByRole('button', {
        name: `${strings.confirmDeleteAction} u_abcdefgh23456777`,
      }),
    );

    expect(await screen.findByText('user u_abcdefgh23456777 does not exist')).toBeInTheDocument();
  });

  it('shows the generic error message when a deprovision fails with a non-ApiError', async () => {
    const listUsers = vi.fn().mockResolvedValue([
      {
        userId: 'u_abcdefgh23456777',
        apps: ['urls4irl'],
        topicPatterns: ['urls4irl-abcdefgh23456777-*'],
      },
    ]);
    const deprovision = vi.fn().mockRejectedValue(new Error('network down'));
    const client = buildFakeClient({ listUsers, deprovision });
    render(<App client={client} />);

    const row = (
      await within(sectionByHeading(strings.usersHeading)).findByText('u_abcdefgh23456777')
    ).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deprovisionAction} urls4irl` }),
    );
    await userEvent.click(
      screen.getByRole('button', { name: `${strings.confirmDeprovisionAction} urls4irl` }),
    );

    expect(await screen.findByText(strings.genericError)).toBeInTheDocument();
  });

  it('shows the not-found-or-no-grant wording when a deprovision fails with a 404', async () => {
    const listUsers = vi.fn().mockResolvedValue([
      {
        userId: 'u_abcdefgh23456777',
        apps: ['urls4irl'],
        topicPatterns: ['urls4irl-abcdefgh23456777-*'],
      },
    ]);
    const deprovision = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 404, message: 'user does not exist' }));
    const client = buildFakeClient({ listUsers, deprovision });
    render(<App client={client} />);

    const row = (
      await within(sectionByHeading(strings.usersHeading)).findByText('u_abcdefgh23456777')
    ).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deprovisionAction} urls4irl` }),
    );
    await userEvent.click(
      screen.getByRole('button', { name: `${strings.confirmDeprovisionAction} urls4irl` }),
    );

    expect(await screen.findByText(strings.deprovisionNotFound)).toBeInTheDocument();
    expect(screen.queryByText('user does not exist')).not.toBeInTheDocument();
  });

  it('shows the generic error message when the initial user load fails', async () => {
    const listUsers = vi.fn().mockRejectedValue(new Error('network down'));
    const client = buildFakeClient({ listUsers });
    render(<App client={client} />);

    expect(await screen.findByText(strings.genericError)).toBeInTheDocument();
  });

  it('clears the action error once a later action succeeds', async () => {
    const listUsers = vi.fn().mockResolvedValue([
      {
        userId: 'u_abcdefgh23456777',
        apps: ['urls4irl'],
        topicPatterns: ['urls4irl-abcdefgh23456777-*'],
      },
    ]);
    const deprovision = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'ntfy CLI failed' }))
      .mockResolvedValue(undefined);
    const client = buildFakeClient({ listUsers, deprovision });
    render(<App client={client} />);

    const row = (
      await within(sectionByHeading(strings.usersHeading)).findByText('u_abcdefgh23456777')
    ).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    // Each deprovision is now a two-step confirm (trigger → confirm); the
    // ConfirmButton reverts to its trigger after firing.
    async function clickDeprovision() {
      await userEvent.click(
        within(row!).getByRole('button', { name: `${strings.deprovisionAction} urls4irl` }),
      );
      await userEvent.click(
        screen.getByRole('button', { name: `${strings.confirmDeprovisionAction} urls4irl` }),
      );
    }

    await clickDeprovision();
    expect(await screen.findByText('ntfy CLI failed')).toBeInTheDocument();

    await clickDeprovision();
    await waitFor(() => expect(screen.queryByText('ntfy CLI failed')).not.toBeInTheDocument());
  });

  it('renders the people table with data when a personClient is supplied', async () => {
    const personClient = buildFakePersonClient({
      listPeople: vi.fn().mockResolvedValue([
        {
          personHash: '76gzqgp4byjl6dje',
          email: 'alice@example.com',
          createdAt: '2026-07-19T18:12:03Z',
        },
      ]),
    });
    render(<App client={buildFakeClient()} personClient={personClient} />);

    expect(screen.getByRole('heading', { name: strings.peopleHeading })).toBeInTheDocument();
    expect(await screen.findByText('alice@example.com')).toBeInTheDocument();
    expect(personClient.listPeople).toHaveBeenCalled();
  });

  it('does not render the people section when no personClient is supplied', async () => {
    render(<App client={buildFakeClient()} />);

    await waitFor(() => expect(screen.getByText(strings.usersEmpty)).toBeInTheDocument());
    expect(screen.queryByRole('heading', { name: strings.peopleHeading })).not.toBeInTheDocument();
  });

  it('shows the people error but keeps the users table working when the person service fails', async () => {
    const personClient = buildFakePersonClient({
      listPeople: vi.fn().mockRejectedValue(new ApiError({ status: 503, message: 'worker down' })),
    });
    const client = buildFakeClient({
      listUsers: vi
        .fn()
        .mockResolvedValue([{ userId: 'u_abcdefgh23456777', apps: [], topicPatterns: [] }]),
    });
    render(<App client={client} personClient={personClient} />);

    expect(await screen.findByText('worker down')).toBeInTheDocument();
    expect(await screen.findByText('u_abcdefgh23456777')).toBeInTheDocument();
    // The people-load failure must never populate the shared error banner.
    expect(screen.queryByText(strings.genericError)).not.toBeInTheDocument();
  });

  it('refreshes the people list after a successful provision', async () => {
    const personClient = buildFakePersonClient();
    const client = buildFakeClient();
    render(<App client={client} personClient={personClient} />);

    await waitFor(() => expect(personClient.listPeople).toHaveBeenCalledTimes(1));

    await userEvent.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await userEvent.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await userEvent.click(screen.getByRole('button', { name: strings.provisionAction }));

    await waitFor(() => expect(personClient.listPeople).toHaveBeenCalledTimes(2));
  });

  it('shows the email in the users table when the person client resolves the matching person', async () => {
    const listUsers = vi.fn().mockResolvedValue([
      {
        userId: 'u_76gzqgp4byjl6dje',
        apps: ['urls4irl'],
        topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
      },
    ]);
    const client = buildFakeClient({ listUsers });
    const personClient = buildFakePersonClient({
      listPeople: vi.fn().mockResolvedValue([
        {
          personHash: '76gzqgp4byjl6dje',
          email: 'alice@example.com',
          createdAt: '2026-07-19T18:12:03Z',
        },
      ]),
    });
    render(<App client={client} personClient={personClient} />);

    // The email is expected in both the Users table (via emailByPersonHash)
    // and the People table (its own email column) once people load. Scope each
    // assertion to its section — the send-test table also renders the resolved
    // email, so a document-wide count is no longer the right signal.
    await waitFor(() =>
      expect(
        within(sectionByHeading(strings.usersHeading)).getByText('alice@example.com'),
      ).toBeInTheDocument(),
    );
    expect(
      within(sectionByHeading(strings.peopleHeading)).getByText('alice@example.com'),
    ).toBeInTheDocument();
    expect(screen.queryByText('u_76gzqgp4byjl6dje')).not.toBeInTheDocument();
  });

  it('resets a person password from the Users list with the row app and joined email, leaving the banner and lists alone', async () => {
    const user = userEvent.setup();
    const listUsers = vi.fn().mockResolvedValue([
      {
        userId: 'u_76gzqgp4byjl6dje',
        apps: ['urls4irl'],
        topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
      },
    ]);
    const client = buildFakeClient({ listUsers });
    const personClient = buildFakePersonClient({
      listPeople: vi.fn().mockResolvedValue([
        {
          personHash: '76gzqgp4byjl6dje',
          email: 'alice@example.com',
          createdAt: '2026-07-19T18:12:03Z',
        },
      ]),
    });
    render(<App client={client} personClient={personClient} />);
    const users = sectionByHeading(strings.usersHeading);

    await user.click(
      await within(users).findByRole('button', {
        name: strings.resetPasswordTriggerNamed({ user: 'alice@example.com' }),
      }),
    );
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', {
        name: strings.resetPasswordConfirmNamed({ user: 'alice@example.com' }),
      }),
    );

    expect(
      await within(screen.getByRole('dialog')).findByDisplayValue('abcd-efgh-ijkm-npqr'),
    ).toBeInTheDocument();
    expect(client.resetPassword).toHaveBeenCalledWith({
      appId: 'urls4irl',
      email: 'alice@example.com',
    });
    expect(listUsers).toHaveBeenCalledTimes(1);
    expect(personClient.listPeople).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('backs out of the Users reset-password confirm on Escape without resetting or revealing a password', async () => {
    const user = userEvent.setup();
    const client = buildFakeClient({
      listUsers: vi.fn().mockResolvedValue([
        {
          userId: 'u_76gzqgp4byjl6dje',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
        },
      ]),
    });
    const personClient = buildFakePersonClient({
      listPeople: vi.fn().mockResolvedValue([
        {
          personHash: '76gzqgp4byjl6dje',
          email: 'alice@example.com',
          createdAt: '2026-07-19T18:12:03Z',
        },
      ]),
    });
    render(<App client={client} personClient={personClient} />);
    const users = sectionByHeading(strings.usersHeading);
    const triggerName = strings.resetPasswordTriggerNamed({ user: 'alice@example.com' });

    await user.click(await within(users).findByRole('button', { name: triggerName }));
    expect(
      within(screen.getByRole('dialog')).getByText(strings.resetPasswordNotice),
    ).toBeInTheDocument();
    await user.keyboard('{Escape}');

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByText(strings.resetPasswordNotice)).not.toBeInTheDocument();
    expect(within(users).getByRole('button', { name: triggerName })).toHaveFocus();
    expect(client.resetPassword).not.toHaveBeenCalled();
    expect(screen.queryByDisplayValue('abcd-efgh-ijkm-npqr')).not.toBeInTheDocument();
  });

  it('offers no Reset action when the email is not known (no person client)', async () => {
    const client = buildFakeClient({
      listUsers: vi.fn().mockResolvedValue([
        {
          userId: 'u_76gzqgp4byjl6dje',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
        },
      ]),
    });
    render(<App client={client} />);

    await waitFor(() =>
      expect(
        within(sectionByHeading(strings.usersHeading)).getByText(strings.resetPasswordUnavailable),
      ).toBeInTheDocument(),
    );
    expect(screen.queryByRole('button', { name: /^Reset password for/ })).not.toBeInTheDocument();
  });

  it('renders raw userIds in the users table when no personClient is supplied', async () => {
    const client = buildFakeClient({
      listUsers: vi
        .fn()
        .mockResolvedValue([{ userId: 'u_abcdefgh23456777', apps: [], topicPatterns: [] }]),
    });
    render(<App client={client} />);

    expect(await screen.findByText('u_abcdefgh23456777')).toBeInTheDocument();
  });

  it('deletes a person via full teardown (deleteUser with the derived ntfy id) after confirming', async () => {
    const client = buildFakeClient();
    const personClient = buildFakePersonClient({
      listPeople: vi.fn().mockResolvedValue([
        {
          personHash: '76gzqgp4byjl6dje',
          email: 'alice@example.com',
          createdAt: '2026-07-19T18:12:03Z',
        },
      ]),
    });
    render(<App client={client} personClient={personClient} />);

    const row = (await screen.findByText('alice@example.com')).closest('tr');
    if (row === null) {
      throw new Error('person row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deleteAction} alice@example.com` }),
    );
    await userEvent.click(
      screen.getByRole('button', {
        name: `${strings.confirmDeleteAction} alice@example.com`,
      }),
    );

    await waitFor(() =>
      expect(client.deleteUser).toHaveBeenCalledWith({ userId: 'u_76gzqgp4byjl6dje' }),
    );
  });

  it('reloads the personal destinations after a person is deleted', async () => {
    const client = buildFakeClient();
    const deliveryClient = buildFakeDeliveryClient();
    const personClient = buildFakePersonClient({
      listPeople: vi.fn().mockResolvedValue([
        {
          personHash: '76gzqgp4byjl6dje',
          email: 'alice@example.com',
          createdAt: '2026-07-19T18:12:03Z',
        },
      ]),
    });
    render(
      <App
        client={client}
        personClient={personClient}
        deliveryClient={deliveryClient}
        personalDestinationsEnabled
      />,
    );
    // The section mounts with the Delivery tab; go back to People to delete.
    await userEvent.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await waitFor(() => expect(deliveryClient.listPersonalDestinations).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole('tab', { name: strings.tabProvisioning }));

    const row = (await screen.findByText('alice@example.com')).closest('tr');
    if (row === null) {
      throw new Error('person row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deleteAction} alice@example.com` }),
    );
    await userEvent.click(
      screen.getByRole('button', {
        name: `${strings.confirmDeleteAction} alice@example.com`,
      }),
    );

    await waitFor(() => expect(client.deleteUser).toHaveBeenCalled());
    await waitFor(() => expect(deliveryClient.listPersonalDestinations).toHaveBeenCalledTimes(2));
  });

  it('does not render the Apps section unless appsEnabled is set', async () => {
    const personClient = buildFakePersonClient();
    render(<App client={buildFakeClient()} personClient={personClient} />);

    await waitFor(() => expect(personClient.listPeople).toHaveBeenCalled());
    expect(screen.queryByRole('heading', { name: strings.appsHeading })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: strings.addAppHeading })).not.toBeInTheDocument();
    expect(personClient.listApps).not.toHaveBeenCalled();
  });

  it('renders and loads the Apps section when appsEnabled', async () => {
    const personClient = buildFakePersonClient({
      listApps: vi.fn().mockResolvedValue([
        {
          appId: 'urls4irl',
          displayName: 'URLs4IRL',
          description: 'Shared URL app',
          createdAt: '2026-07-25T10:00:00Z',
        },
      ]),
    });
    render(<App client={buildFakeClient()} personClient={personClient} appsEnabled />);

    expect(screen.getByRole('heading', { name: strings.appsHeading })).toBeInTheDocument();
    expect(await screen.findByText('URLs4IRL')).toBeInTheDocument();
    expect(personClient.listApps).toHaveBeenCalled();
  });

  it('shows the live subscriber count for a registered app derived from the users list', async () => {
    const client = buildFakeClient({
      listUsers: vi.fn().mockResolvedValue([
        {
          userId: 'u_76gzqgp4byjl6dje',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
        },
        {
          userId: 'u_abcdefgh23456777',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-abcdefgh23456777-*'],
        },
        // Publisher identity — must NOT be counted as a subscriber.
        { userId: 'urls4irl-publisher', apps: ['urls4irl'], topicPatterns: ['urls4irl-*'] },
      ]),
    });
    const personClient = buildFakePersonClient({
      listApps: vi.fn().mockResolvedValue([
        {
          appId: 'urls4irl',
          displayName: 'URLs4IRL',
          description: null,
          createdAt: '2026-07-25T10:00:00Z',
        },
      ]),
    });
    render(<App client={client} personClient={personClient} appsEnabled />);

    const appRow = (await screen.findByText('URLs4IRL')).closest('tr');
    if (appRow === null) {
      throw new Error('app row not found');
    }
    // Two "u_" subscribers, publisher excluded.
    expect(within(appRow).getByText('2')).toBeInTheDocument();
  });

  it('adds an app: mints first, then registers metadata, and shows the minted confirmation', async () => {
    const client = buildFakeClient();
    const personClient = buildFakePersonClient();
    render(<App client={client} personClient={personClient} appsEnabled />);
    await waitFor(() => expect(client.listUsers).toHaveBeenCalled());
    await waitFor(() => expect(personClient.listApps).toHaveBeenCalled());
    const listUsersCallsBefore = vi.mocked(client.listUsers).mock.calls.length;
    const listAppsCallsBefore = vi.mocked(personClient.listApps).mock.calls.length;

    const addSection = screen
      .getByRole('heading', { name: strings.addAppHeading })
      .closest('section');
    if (addSection === null) {
      throw new Error('add-app section not found');
    }
    const form = within(addSection);
    await userEvent.type(form.getByLabelText(strings.appIdLabel), 'tasktracker');
    await userEvent.type(form.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await userEvent.click(form.getByRole('button', { name: strings.addAppAction }));

    await waitFor(() =>
      expect(personClient.createApp).toHaveBeenCalledWith({
        appId: 'tasktracker',
        displayName: 'Task Tracker',
        description: undefined,
      }),
    );
    expect(client.provisionApp).toHaveBeenCalledWith({ appId: 'tasktracker' });
    expect(vi.mocked(client.provisionApp).mock.invocationCallOrder[0]).toBeLessThan(
      vi.mocked(personClient.createApp).mock.invocationCallOrder[0],
    );
    expect(
      await screen.findByText(strings.publisherMintedResult({ appId: 'tasktracker' })),
    ).toBeInTheDocument();
    expect(screen.queryByText(strings.appAlreadyRegisteredNote)).not.toBeInTheDocument();
    await waitFor(() =>
      expect(vi.mocked(client.listUsers).mock.calls.length).toBe(listUsersCallsBefore + 1),
    );
    await waitFor(() =>
      expect(vi.mocked(personClient.listApps).mock.calls.length).toBe(listAppsCallsBefore + 1),
    );
  });

  async function submitAddApp(): Promise<void> {
    const addSection = screen
      .getByRole('heading', { name: strings.addAppHeading })
      .closest('section');
    if (addSection === null) {
      throw new Error('add-app section not found');
    }
    const form = within(addSection);
    await userEvent.type(form.getByLabelText(strings.appIdLabel), 'tasktracker');
    await userEvent.type(form.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await userEvent.click(form.getByRole('button', { name: strings.addAppAction }));
  }

  it('add app on an already-registered app: updates it, mints, and shows the note', async () => {
    const client = buildFakeClient();
    const personClient = buildFakePersonClient({
      createApp: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 409, message: 'app already exists' })),
    });
    render(<App client={client} personClient={personClient} appsEnabled />);
    await waitFor(() => expect(client.listUsers).toHaveBeenCalled());
    await waitFor(() => expect(personClient.listApps).toHaveBeenCalled());
    const listUsersCallsBefore = vi.mocked(client.listUsers).mock.calls.length;
    const listAppsCallsBefore = vi.mocked(personClient.listApps).mock.calls.length;

    await submitAddApp();

    expect(
      await screen.findByText(strings.publisherMintedResult({ appId: 'tasktracker' })),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(vi.mocked(client.listUsers).mock.calls.length).toBe(listUsersCallsBefore + 1),
    );
    await waitFor(() =>
      expect(vi.mocked(personClient.listApps).mock.calls.length).toBe(listAppsCallsBefore + 1),
    );
    expect(personClient.updateApp).toHaveBeenCalledWith({
      appId: 'tasktracker',
      displayName: 'Task Tracker',
      description: undefined,
    });
    expect(client.provisionApp).toHaveBeenCalledWith({ appId: 'tasktracker' });
    const addSection = screen
      .getByRole('heading', { name: strings.addAppHeading })
      .closest('section');
    if (addSection === null) {
      throw new Error('add-app section not found');
    }
    expect(within(addSection).getByRole('status')).toHaveTextContent(
      strings.appAlreadyRegisteredNote,
    );
    expect(within(addSection).queryByRole('alert')).not.toBeInTheDocument();
  });

  it('add app with a non-409 createApp failure after a mint shows the prefixed error', async () => {
    const client = buildFakeClient();
    const personClient = buildFakePersonClient({
      createApp: vi.fn().mockRejectedValue(new ApiError({ status: 500, message: 'registry down' })),
    });
    render(<App client={client} personClient={personClient} appsEnabled />);

    await submitAddApp();

    expect(
      await screen.findByText(`${strings.publisherMintedButRegisterFailed}registry down`),
    ).toBeInTheDocument();
    expect(personClient.updateApp).not.toHaveBeenCalled();
    expect(client.provisionApp).toHaveBeenCalledTimes(1);
  });

  it('add app with a not-bound mint 404 shows the add-it-again copy and touches no registry or user list', async () => {
    const client = buildFakeClient({
      provisionApp: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 404, message: 'app has no approved binding' })),
    });
    const personClient = buildFakePersonClient();
    render(<App client={client} personClient={personClient} appsEnabled />);
    await waitFor(() => expect(client.listUsers).toHaveBeenCalled());
    const listUsersCallsBefore = vi.mocked(client.listUsers).mock.calls.length;

    await submitAddApp();

    expect(
      await screen.findByText(strings.appNotBoundError({ appId: 'tasktracker' })),
    ).toBeInTheDocument();
    expect(personClient.createApp).not.toHaveBeenCalled();
    expect(personClient.updateApp).not.toHaveBeenCalled();
    expect(vi.mocked(client.listUsers).mock.calls.length).toBe(listUsersCallsBefore);
  });

  it.each([
    { status: 409, message: 'publisher mint in progress' },
    { status: 503, message: 'upstream unavailable' },
    { status: 404, message: 'publisher not found' },
  ])(
    'add app with a mint $status shows the server message and never registers',
    async (failure) => {
      const client = buildFakeClient({
        provisionApp: vi.fn().mockRejectedValue(new ApiError(failure)),
      });
      const personClient = buildFakePersonClient();
      render(<App client={client} personClient={personClient} appsEnabled />);
      await waitFor(() => expect(client.listUsers).toHaveBeenCalled());
      const listUsersCallsBefore = vi.mocked(client.listUsers).mock.calls.length;

      await submitAddApp();

      expect(await screen.findByText(failure.message)).toBeInTheDocument();
      expect(vi.mocked(client.listUsers).mock.calls.length).toBe(listUsersCallsBefore);
      expect(
        screen.queryByText(strings.appNotBoundError({ appId: 'tasktracker' })),
      ).not.toBeInTheDocument();
      expect(personClient.createApp).not.toHaveBeenCalled();
      expect(personClient.updateApp).not.toHaveBeenCalled();
    },
  );

  it('add app 409 then updateApp failure after a mint shows the prefixed error and refreshes apps', async () => {
    const client = buildFakeClient();
    const personClient = buildFakePersonClient({
      createApp: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 409, message: 'app already exists' })),
      updateApp: vi.fn().mockRejectedValue(new ApiError({ status: 500, message: 'update failed' })),
    });
    render(<App client={client} personClient={personClient} appsEnabled />);
    await waitFor(() => expect(personClient.listApps).toHaveBeenCalled());
    const listAppsCallsBefore = vi.mocked(personClient.listApps).mock.calls.length;

    await submitAddApp();

    expect(
      await screen.findByText(`${strings.publisherMintedButRegisterFailed}update failed`),
    ).toBeInTheDocument();
    expect(client.provisionApp).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(vi.mocked(personClient.listApps).mock.calls.length).toBeGreaterThan(
        listAppsCallsBefore,
      ),
    );
  });

  describe('re-mint token failures', () => {
    async function remintWith(provisionApp: ApiClient['provisionApp']) {
      const client = buildFakeClient({ provisionApp });
      const personClient = buildFakePersonClient({
        listApps: vi.fn().mockResolvedValue([
          {
            appId: 'urls4irl',
            displayName: 'URLs4IRL',
            description: null,
            createdAt: '2026-07-25T10:00:00Z',
          },
        ]),
      });
      render(<App client={client} personClient={personClient} appsEnabled />);
      await userEvent.click(
        await screen.findByRole('button', { name: `${strings.editAction} URLs4IRL` }),
      );
      const dialog = screen.getByRole('dialog');
      await userEvent.click(
        within(dialog).getByRole('button', { name: strings.remintTokenAction }),
      );
      return dialog;
    }

    it('shows the try-again copy for a not-bound 404', async () => {
      const dialog = await remintWith(
        vi
          .fn()
          .mockRejectedValue(new ApiError({ status: 404, message: 'app has no approved binding' })),
      );

      await waitFor(() =>
        expect(within(dialog).getByRole('alert')).toHaveTextContent(
          strings.appNotBoundRetryError({ appId: 'urls4irl' }),
        ),
      );
    });

    it('shows a 404 with any other message unchanged', async () => {
      const dialog = await remintWith(
        vi.fn().mockRejectedValue(new ApiError({ status: 404, message: 'publisher not found' })),
      );

      await waitFor(() =>
        expect(within(dialog).getByRole('alert')).toHaveTextContent('publisher not found'),
      );
    });
  });

  it('removes an app via the cascade after confirming', async () => {
    const client = buildFakeClient();
    const personClient = buildFakePersonClient({
      listApps: vi.fn().mockResolvedValue([
        {
          appId: 'urls4irl',
          displayName: 'URLs4IRL',
          description: null,
          createdAt: '2026-07-25T10:00:00Z',
        },
      ]),
    });
    render(<App client={client} personClient={personClient} appsEnabled />);

    const appRow = (await screen.findByText('URLs4IRL')).closest('tr');
    if (appRow === null) {
      throw new Error('app row not found');
    }
    await userEvent.click(
      within(appRow).getByRole('button', { name: `${strings.removeAction} URLs4IRL` }),
    );
    await userEvent.click(
      screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
    );

    await waitFor(() => expect(client.deprovisionApp).toHaveBeenCalledWith({ appId: 'urls4irl' }));
  });

  describe('remove app failures', () => {
    const urls4irlApp = {
      appId: 'urls4irl',
      displayName: 'URLs4IRL',
      description: null,
      createdAt: '2026-07-25T10:00:00Z',
    };

    async function removeApp({
      rejection,
    }: {
      rejection: unknown;
    }): Promise<{ client: ApiClient; personClient: PersonApiClient }> {
      const client = buildFakeClient({ deprovisionApp: vi.fn().mockRejectedValue(rejection) });
      const personClient = buildFakePersonClient({
        listApps: vi.fn().mockResolvedValue([urls4irlApp]),
      });
      render(<App client={client} personClient={personClient} appsEnabled />);
      await screen.findByText('URLs4IRL');
      vi.mocked(client.listUsers).mockClear();
      vi.mocked(personClient.listApps).mockClear();
      await userEvent.click(
        screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` }),
      );
      await userEvent.click(
        screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
      );
      return { client, personClient };
    }

    it.each([400, 502])('shows a %i message and refreshes nothing', async (status) => {
      const { client, personClient } = await removeApp({
        rejection: new ApiError({ status, message: `server said ${status}` }),
      });

      expect(await screen.findByText(`server said ${status}`)).toBeInTheDocument();
      expect(client.listUsers).not.toHaveBeenCalled();
      expect(personClient.listApps).not.toHaveBeenCalled();
    });

    it('shows the generic error for a non-ApiError rejection and refreshes nothing', async () => {
      const { client, personClient } = await removeApp({ rejection: new Error('network') });

      expect(await screen.findByText(strings.genericError)).toBeInTheDocument();
      expect(client.listUsers).not.toHaveBeenCalled();
      expect(personClient.listApps).not.toHaveBeenCalled();
    });

    it.each([409, 503])('shows a %i message and refreshes apps and users', async (status) => {
      const { client, personClient } = await removeApp({
        rejection: new ApiError({ status, message: `server said ${status}` }),
      });

      expect(await screen.findByText(`server said ${status}`)).toBeInTheDocument();
      await waitFor(() => expect(client.listUsers).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(personClient.listApps).toHaveBeenCalledTimes(1));
    });

    it('disables Remove while pending, makes one request, then announces Removed', async () => {
      let resolveFirst: () => void = () => undefined;
      const client = buildFakeClient({
        deprovisionApp: vi.fn().mockReturnValue(
          new Promise<void>((resolve) => {
            resolveFirst = resolve;
          }),
        ),
      });
      const personClient = buildFakePersonClient({
        // A fresh array per fetch, as the real client returns.
        listApps: vi.fn().mockImplementation(() => Promise.resolve([{ ...urls4irlApp }])),
      });
      render(<App client={client} personClient={personClient} appsEnabled />);
      await screen.findByText('URLs4IRL');
      await userEvent.click(
        screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` }),
      );
      await userEvent.click(
        screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
      );

      expect(await screen.findByRole('button', { name: 'Removing URLs4IRL' })).toHaveAttribute(
        'aria-disabled',
        'true',
      );
      expect(client.deprovisionApp).toHaveBeenCalledTimes(1);
      resolveFirst();
      expect(await screen.findByText('Removed urls4irl.')).toBeInTheDocument();
      expect(client.deprovisionApp).toHaveBeenCalledTimes(1);
    });
  });

  it('lists an in-use-but-unregistered app under Unprovisioned apps with its subscriber count', async () => {
    const client = buildFakeClient({
      listUsers: vi.fn().mockResolvedValue([
        {
          userId: 'u_76gzqgp4byjl6dje',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
        },
      ]),
    });
    // Registry is empty, so urls4irl is a "ghost" app (in use, unregistered).
    const personClient = buildFakePersonClient({ listApps: vi.fn().mockResolvedValue([]) });
    render(<App client={client} personClient={personClient} appsEnabled />);

    // Registry section is empty…
    expect(await screen.findByText(strings.appsEmpty)).toBeInTheDocument();

    // …but the Unprovisioned-apps section surfaces urls4irl with 1 subscriber.
    const section = screen
      .getByRole('heading', { name: strings.unregisteredAppsHeading })
      .closest('section');
    if (section === null) {
      throw new Error('unprovisioned-apps section not found');
    }
    const row = within(section).getByText('urls4irl').closest('tr');
    if (row === null) {
      throw new Error('ghost app row not found');
    }
    expect(within(row).getByText('1')).toBeInTheDocument();
  });

  it('sends a test notification: picks the app, selects a user, and renders the results panel', async () => {
    const testNotify = vi.fn().mockResolvedValue({
      results: [
        {
          recipient: '76gzqgp4byjl6dje',
          userId: 'u_76gzqgp4byjl6dje',
          topic: 'urls4irl-76gzqgp4byjl6dje-all',
          ok: true,
          messageId: 'VkT2p9wQ',
          error: '',
        },
      ],
    });
    const client = buildFakeClient({
      listUsers: vi.fn().mockResolvedValue([
        {
          userId: 'u_76gzqgp4byjl6dje',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
        },
      ]),
      testNotify,
    });
    render(<App client={client} />);

    // Scope every query to the Send-test section so the mirrored userId text in
    // the Users table below can never satisfy a locator by accident.
    const section = (await screen.findByRole('heading', { name: strings.sendTestHeading })).closest(
      'section',
    );
    if (section === null) {
      throw new Error('send-test section not found');
    }
    const sendTest = within(section);

    // targetApp defaults to the only sorted option (urls4irl) once users load.
    await waitFor(() =>
      expect(
        (sendTest.getByLabelText(strings.sendTestTargetAppLabel) as HTMLSelectElement).value,
      ).toBe('urls4irl'),
    );

    await userEvent.click(
      sendTest.getByRole('checkbox', {
        name: strings.sendTestSelectAria({ who: 'u_76gzqgp4byjl6dje' }),
      }),
    );
    await userEvent.click(sendTest.getByRole('button', { name: strings.sendTestAction }));

    await waitFor(() =>
      expect(testNotify).toHaveBeenCalledWith({
        appId: 'urls4irl',
        recipients: ['76gzqgp4byjl6dje'],
        channel: strings.sendTestDefaultChannel,
        message: strings.sendTestDefaultMessage,
        title: 'urls4irl test',
      }),
    );

    const results = await sendTest.findByRole('status');
    expect(within(results).getByText(strings.sendTestDelivered)).toBeInTheDocument();
  });

  it('sends the registry display name as the test notification title', async () => {
    const testNotify = vi.fn().mockResolvedValue({ results: [] });
    const client = buildFakeClient({
      listUsers: vi.fn().mockResolvedValue([
        {
          userId: 'u_76gzqgp4byjl6dje',
          apps: ['urls4irl'],
          topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
        },
      ]),
      testNotify,
    });
    const personClient = buildFakePersonClient({
      listApps: vi.fn().mockResolvedValue([
        {
          appId: 'urls4irl',
          displayName: 'URLs 4 IRL',
          description: null,
          createdAt: '2026-07-25T10:00:00Z',
        },
      ]),
    });
    render(<App client={client} personClient={personClient} appsEnabled />);

    const section = (await screen.findByRole('heading', { name: strings.sendTestHeading })).closest(
      'section',
    );
    if (section === null) {
      throw new Error('send-test section not found');
    }
    const sendTest = within(section);

    // The registry loads asynchronously; wait for the hint to pick up the
    // display name before submitting so the title is the registry-backed one.
    expect(
      await sendTest.findByText(strings.sendTestFieldHint({ title: 'URLs 4 IRL test' })),
    ).toBeInTheDocument();

    await userEvent.click(
      await sendTest.findByRole('checkbox', {
        name: strings.sendTestSelectAria({ who: 'u_76gzqgp4byjl6dje' }),
      }),
    );
    await userEvent.click(sendTest.getByRole('button', { name: strings.sendTestAction }));

    await waitFor(() =>
      expect(testNotify).toHaveBeenCalledWith({
        appId: 'urls4irl',
        recipients: ['76gzqgp4byjl6dje'],
        channel: strings.sendTestDefaultChannel,
        message: strings.sendTestDefaultMessage,
        title: 'URLs 4 IRL test',
      }),
    );
  });
});

describe('App Delivery tab', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/');
  });

  afterEach(() => {
    window.history.replaceState(null, '', '/');
  });

  function tab(name: string) {
    return screen.getByRole('tab', { name });
  }

  it('renders no tab bar with both flags off, even with a client', () => {
    render(<App client={buildFakeClient()} deliveryClient={buildFakeDeliveryClient()} />);

    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
    expect(screen.getByRole('main')).toContainElement(
      screen.getByRole('heading', { name: strings.provisionHeading }),
    );
  });

  it('renders no tab bar with a flag on but no client', () => {
    render(<App client={buildFakeClient()} destinationsEnabled deliveryLogEnabled />);

    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
  });

  it.each([
    ['destinationsEnabled', { destinationsEnabled: true }],
    ['deliveryLogEnabled', { deliveryLogEnabled: true }],
    ['pendingSendsEnabled', { pendingSendsEnabled: true }],
  ])('renders the tab bar when only %s is on', (_name, flags) => {
    render(
      <App client={buildFakeClient()} deliveryClient={buildFakeDeliveryClient()} {...flags} />,
    );

    expect(screen.getByRole('tablist')).toBeInTheDocument();
    expect(tab(strings.tabProvisioning)).toHaveAttribute('aria-selected', 'true');
  });

  it('makes no delivery request until the Delivery tab is first shown', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    // Let the provisioning fetches settle so a stray delivery call would show.
    await screen.findByText(strings.usersEmpty);
    expect(deliveryClient.getReadiness).not.toHaveBeenCalled();

    await user.click(tab(strings.tabDelivery));
    await waitFor(() => expect(deliveryClient.getReadiness).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(strings.deliveryStatusReady)).toBeInTheDocument();

    await user.click(tab(strings.tabProvisioning));
    await user.click(tab(strings.tabDelivery));
    expect(deliveryClient.getReadiness).toHaveBeenCalledTimes(1);
  });

  it('opens Delivery directly when the page loads on #delivery', async () => {
    window.history.replaceState(null, '', '/#delivery');
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    expect(tab(strings.tabDelivery)).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByText(strings.deliveryStatusReady)).toBeInTheDocument();
  });

  it('keeps a provision-token reveal and typed input across tab switches', async () => {
    const user = userEvent.setup();
    render(
      <App
        client={buildFakeClient()}
        deliveryClient={buildFakeDeliveryClient()}
        destinationsEnabled
      />,
    );

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));
    expect(await screen.findByText('tk_generated')).toBeInTheDocument();
    await user.type(screen.getByLabelText(strings.appIdLabel), 'half');

    await user.click(tab(strings.tabDelivery));
    expect(document.getElementById('app-panel-provisioning')).toHaveAttribute('hidden');
    await user.click(tab(strings.tabProvisioning));

    expect(screen.getByText('tk_generated')).toBeInTheDocument();
    expect(screen.getByLabelText(strings.appIdLabel)).toHaveValue('urls4irlhalf');
  });

  it('shows the provisioning error banner in the Provisioning panel only', async () => {
    const user = userEvent.setup();
    const client = buildFakeClient({
      listUsers: vi.fn().mockRejectedValue(new ApiError({ message: 'users down', status: 500 })),
    });
    render(<App client={client} deliveryClient={buildFakeDeliveryClient()} destinationsEnabled />);

    const banner = await screen.findByText('users down');
    const provisioningPanel = screen.getByRole('tabpanel', { name: strings.tabProvisioning });
    expect(provisioningPanel).toContainElement(banner);

    await user.click(tab(strings.tabDelivery));
    expect(screen.getByRole('tabpanel', { name: strings.tabDelivery })).not.toContainElement(
      banner,
    );
  });

  it('keeps a delivery-api outage out of the shared provisioning error banner', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient({
      getReadiness: vi.fn().mockResolvedValue({ kind: 'unreachable', message: 'upstream down' }),
    });
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    await user.click(tab(strings.tabDelivery));
    const delivery = screen.getByRole('tabpanel', { name: strings.tabDelivery });
    expect(await within(delivery).findByRole('alert')).toHaveTextContent('upstream down');
    const provisioningPanel = document.getElementById('app-panel-provisioning');
    expect(provisioningPanel).not.toBeNull();
    expect(
      within(provisioningPanel as HTMLElement).queryByRole('alert', { hidden: true }),
    ).not.toBeInTheDocument();
  });

  it('shows the status card only when destinations are enabled', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} deliveryLogEnabled />);

    await user.click(tab(strings.tabDelivery));

    expect(screen.queryByText(strings.deliveryStatusHeading)).not.toBeInTheDocument();
    expect(deliveryClient.getReadiness).not.toHaveBeenCalled();
  });

  it('lists destinations only after the Delivery tab is first shown', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    await screen.findByText(strings.usersEmpty);
    expect(deliveryClient.listDestinations).not.toHaveBeenCalled();

    await user.click(tab(strings.tabDelivery));
    expect(await screen.findByText(strings.destinationsEmpty)).toBeInTheDocument();
    expect(deliveryClient.listDestinations).toHaveBeenCalledTimes(1);
  });

  it('lists deliveries only after the Delivery tab is first shown, gated by deliveryLogEnabled', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} deliveryLogEnabled />);

    await screen.findByText(strings.usersEmpty);
    expect(deliveryClient.listDeliveries).not.toHaveBeenCalled();

    await user.click(tab(strings.tabDelivery));
    expect(await screen.findByText('No dead deliveries.')).toBeInTheDocument();
    expect(deliveryClient.listDeliveries).toHaveBeenCalledTimes(1);
    expect(deliveryClient.listDestinations).not.toHaveBeenCalled();
  });

  it('does not show the Delivery Log when only destinations are enabled', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    await user.click(tab(strings.tabDelivery));
    await screen.findByText(strings.destinationsEmpty);

    expect(deliveryClient.listDeliveries).not.toHaveBeenCalled();
  });

  it('lists pending sends only after the Delivery tab is first shown, gated by pendingSendsEnabled', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} pendingSendsEnabled />);

    await screen.findByText(strings.usersEmpty);
    expect(deliveryClient.listScheduledMessages).not.toHaveBeenCalled();

    await user.click(tab(strings.tabDelivery));
    expect(await screen.findByText(strings.pendingSendsEmpty)).toBeInTheDocument();
    expect(deliveryClient.listScheduledMessages).toHaveBeenCalledTimes(1);
    // Only this flag is on: the other Delivery sections and their requests stay away.
    expect(deliveryClient.listDeliveries).not.toHaveBeenCalled();
    expect(deliveryClient.listDestinations).not.toHaveBeenCalled();
    expect(screen.queryByRole('heading', { name: strings.deliveryLogHeading })).toBeNull();
  });

  it('does not show or request Pending sends when its flag is off', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} deliveryLogEnabled />);

    await user.click(tab(strings.tabDelivery));
    await screen.findByText('No dead deliveries.');

    expect(screen.queryByRole('heading', { name: strings.pendingSendsHeading })).toBeNull();
    expect(deliveryClient.listScheduledMessages).not.toHaveBeenCalled();
  });

  it('mounts Pending sends directly before the Delivery Log', async () => {
    const user = userEvent.setup();
    render(
      <App
        client={buildFakeClient()}
        deliveryClient={buildFakeDeliveryClient()}
        deliveryLogEnabled
        pendingSendsEnabled
      />,
    );

    await user.click(tab(strings.tabDelivery));
    const pending = await screen.findByRole('heading', { name: strings.pendingSendsHeading });
    const log = await screen.findByRole('heading', { name: strings.deliveryLogHeading });

    expect(pending.compareDocumentPosition(log) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('keeps a destinations load failure out of the shared provisioning error banner', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient({
      listDestinations: vi
        .fn()
        .mockRejectedValue(new ApiError({ message: 'destinations down', status: 502 })),
    });
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    await user.click(tab(strings.tabDelivery));

    const delivery = screen.getByRole('tabpanel', { name: strings.tabDelivery });
    expect(await within(delivery).findByText('destinations down')).toBeInTheDocument();
    const provisioningPanel = document.getElementById('app-panel-provisioning');
    expect(
      within(provisioningPanel as HTMLElement).queryByText('destinations down'),
    ).not.toBeInTheDocument();
  });

  describe('App identities section', () => {
    it('mounts on the Delivery tab on its own flag, after the status card and before Destinations', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App
          client={buildFakeClient()}
          deliveryClient={deliveryClient}
          destinationsEnabled
          appIdentitiesEnabled
        />,
      );

      await screen.findByText(strings.usersEmpty);
      expect(deliveryClient.listIdentityRequests).not.toHaveBeenCalled();

      await user.click(tab(strings.tabDelivery));
      const statusCard = await screen.findByRole('heading', {
        name: strings.deliveryStatusHeading,
      });
      const identities = await screen.findByRole('heading', { name: strings.identitiesHeading });
      const destinations = await screen.findByRole('heading', {
        name: strings.destinationsHeading,
      });

      expect(
        statusCard.compareDocumentPosition(identities) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(
        identities.compareDocumentPosition(destinations) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(await screen.findByText(strings.identitiesRequestsEmpty)).toBeInTheDocument();
      expect(deliveryClient.listIdentityRequests).toHaveBeenCalledTimes(1);
      expect(deliveryClient.listIdentityBindings).toHaveBeenCalledTimes(1);
    });

    it('is absent when the flag is off', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />,
      );

      await user.click(tab(strings.tabDelivery));
      await screen.findByRole('heading', { name: strings.routesHeading });

      expect(screen.queryByRole('heading', { name: strings.identitiesHeading })).toBeNull();
      expect(deliveryClient.listIdentityRequests).not.toHaveBeenCalled();
    });

    it('shows the Delivery tab with only its flag on, without needing Destinations', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App client={buildFakeClient()} deliveryClient={deliveryClient} appIdentitiesEnabled />,
      );

      await user.click(tab(strings.tabDelivery));

      expect(
        await screen.findByRole('heading', { name: strings.identitiesHeading }),
      ).toBeInTheDocument();
      expect(screen.queryByRole('heading', { name: strings.routesHeading })).toBeNull();
    });
  });

  describe('Personal destinations section', () => {
    it('renders and loads when its flag is on, with no other flag', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App
          client={buildFakeClient()}
          deliveryClient={deliveryClient}
          personalDestinationsEnabled
        />,
      );

      await user.click(tab(strings.tabDelivery));

      expect(
        await screen.findByRole('heading', { name: strings.personalHeading }),
      ).toBeInTheDocument();
      expect(deliveryClient.listPersonalDestinations).toHaveBeenCalledTimes(1);
    });

    it('renders nothing and never calls the client when the flag is off', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />,
      );

      await user.click(tab(strings.tabDelivery));
      await screen.findByRole('heading', { name: strings.routesHeading });

      expect(screen.queryByRole('heading', { name: strings.personalHeading })).toBeNull();
      expect(deliveryClient.listPersonalDestinations).not.toHaveBeenCalled();
    });
  });

  describe('Heartbeats section', () => {
    function destination(name: string, enabled = true): DestinationSummary {
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

    it('mounts between Routes and Pending sends, and only after the Delivery tab is first shown', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App
          client={buildFakeClient()}
          deliveryClient={deliveryClient}
          destinationsEnabled
          heartbeatsEnabled
          pendingSendsEnabled
        />,
      );

      await screen.findByText(strings.usersEmpty);
      expect(deliveryClient.listHeartbeats).not.toHaveBeenCalled();

      await user.click(tab(strings.tabDelivery));
      const routes = await screen.findByRole('heading', { name: strings.routesHeading });
      const heartbeats = await screen.findByRole('heading', { name: strings.heartbeatsHeading });
      const pending = await screen.findByRole('heading', { name: strings.pendingSendsHeading });

      expect(
        routes.compareDocumentPosition(heartbeats) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(
        heartbeats.compareDocumentPosition(pending) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(await screen.findByText(strings.heartbeatsEmpty)).toBeInTheDocument();
      expect(deliveryClient.listHeartbeats).toHaveBeenCalledTimes(1);
    });

    it('is absent when the flag is off', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />,
      );

      await user.click(tab(strings.tabDelivery));
      await screen.findByRole('heading', { name: strings.routesHeading });

      expect(screen.queryByRole('heading', { name: strings.heartbeatsHeading })).toBeNull();
      expect(deliveryClient.listHeartbeats).not.toHaveBeenCalled();
    });

    it('never mounts without destinationsEnabled, and does not even show the tab bar for it', async () => {
      const deliveryClient = buildFakeDeliveryClient();
      render(<App client={buildFakeClient()} deliveryClient={deliveryClient} heartbeatsEnabled />);

      await screen.findByText(strings.usersEmpty);

      expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
      expect(screen.queryByRole('heading', { name: strings.heartbeatsHeading })).toBeNull();
      expect(deliveryClient.listHeartbeats).not.toHaveBeenCalled();
    });

    it('never mounts in a Delivery tab opened for another section when destinationsEnabled is off', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient();
      render(
        <App
          client={buildFakeClient()}
          deliveryClient={deliveryClient}
          heartbeatsEnabled
          pendingSendsEnabled
        />,
      );

      await user.click(tab(strings.tabDelivery));
      await screen.findByRole('heading', { name: strings.pendingSendsHeading });

      expect(screen.queryByRole('heading', { name: strings.heartbeatsHeading })).toBeNull();
      expect(deliveryClient.listHeartbeats).not.toHaveBeenCalled();
    });

    it('shares the one destinations fetch: a destination mutation adds only the Destinations refresh', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient({
        listDestinations: vi
          .fn()
          .mockResolvedValueOnce([destination('ops_discord')])
          .mockResolvedValue([destination('ops_discord', false)]),
        updateDestination: vi.fn().mockResolvedValue(destination('ops_discord', false)),
      });
      render(
        <App
          client={buildFakeClient()}
          deliveryClient={deliveryClient}
          destinationsEnabled
          heartbeatsEnabled
        />,
      );

      await user.click(tab(strings.tabDelivery));
      await screen.findByText(strings.heartbeatsEmpty);
      await user.click(await screen.findByRole('button', { name: 'Disable ops_discord' }));
      // Mounting Destinations, Routes and Heartbeats together made exactly one fetch.
      expect(deliveryClient.listDestinations).toHaveBeenCalledTimes(1);
      await user.click(screen.getByRole('button', { name: 'Confirm disable ops_discord' }));

      // The disable swaps in Enable once the refreshed (disabled) list lands.
      expect(await screen.findByRole('button', { name: 'Enable ops_discord' })).toBeInTheDocument();
      expect(deliveryClient.updateDestination).toHaveBeenCalledTimes(1);
      // One initial load plus the Destinations section's own refresh; Heartbeats adds none.
      expect(deliveryClient.listDestinations).toHaveBeenCalledTimes(2);
    });

    it('the open add form tracks a destination mutation: disabling marks its checkbox "(disabled)"', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient({
        listDestinations: vi
          .fn()
          .mockResolvedValueOnce([destination('ops_discord')])
          .mockResolvedValue([destination('ops_discord', false)]),
        updateDestination: vi.fn().mockResolvedValue(destination('ops_discord', false)),
      });
      render(
        <App
          client={buildFakeClient()}
          deliveryClient={deliveryClient}
          destinationsEnabled
          heartbeatsEnabled
        />,
      );

      await user.click(tab(strings.tabDelivery));
      await screen.findByText(strings.heartbeatsEmpty);
      const add = await screen.findByRole('button', { name: strings.addHeartbeatAction });
      await waitFor(() => expect(add).not.toHaveAttribute('aria-disabled'));
      await user.click(add);
      const form = screen.getByRole('form', { name: strings.addHeartbeatHeading });
      const box = within(form).getByRole('checkbox', { name: /^ops_discord/ });
      expect(box.closest('label')).not.toHaveTextContent(strings.routeDestinationDisabledMark);

      await user.click(screen.getByRole('button', { name: 'Disable ops_discord' }));
      await user.click(screen.getByRole('button', { name: 'Confirm disable ops_discord' }));

      await screen.findByRole('button', { name: 'Enable ops_discord' });
      await waitFor(() =>
        expect(
          within(form)
            .getByRole('checkbox', { name: /^ops_discord/ })
            .closest('label'),
        ).toHaveTextContent(strings.routeDestinationDisabledMark),
      );
    });

    it('Add check is gated on the shared destinations state and re-blocks when the last one is deleted', async () => {
      const user = userEvent.setup();
      const deliveryClient = buildFakeDeliveryClient({
        listDestinations: vi
          .fn()
          .mockResolvedValueOnce([destination('ops_discord')])
          .mockResolvedValue([]),
      });
      render(
        <App
          client={buildFakeClient()}
          deliveryClient={deliveryClient}
          destinationsEnabled
          heartbeatsEnabled
        />,
      );

      await user.click(tab(strings.tabDelivery));
      await screen.findByText(strings.heartbeatsEmpty);
      const add = await screen.findByRole('button', { name: strings.addHeartbeatAction });
      await waitFor(() => expect(add).not.toHaveAttribute('aria-disabled'));

      await user.click(
        screen.getByRole('button', { name: strings.deleteDestination({ name: 'ops_discord' }) }),
      );
      await user.click(
        screen.getByRole('button', {
          name: strings.confirmDeleteDestination({ name: 'ops_discord' }),
        }),
      );

      await waitFor(() =>
        expect(
          screen.queryByRole('button', {
            name: strings.deleteDestination({ name: 'ops_discord' }),
          }),
        ).toBeNull(),
      );
      const addAfter = screen.getByRole('button', { name: strings.addHeartbeatAction });
      expect(addAfter).not.toHaveAttribute('aria-disabled');
      expect(addAfter).not.toHaveAttribute('aria-describedby');
    });
  });
});

describe('App delivery refresh after Remove App and binding approval', () => {
  const urls4irlApp = {
    appId: 'urls4irl',
    displayName: 'URLs4IRL',
    description: null,
    createdAt: '2026-07-25T10:00:00Z',
  };
  const pendingRequest = {
    id: 1,
    kind: 'app' as const,
    appId: 'app_1',
    githubRepository: '4IRL/app_1',
    githubRepositoryId: 812345601,
    workflowPath: '.github/workflows/prod-deploy.yml',
    gitRef: 'refs/heads/main',
    environment: 'production',
    githubRunId: '14022',
    status: 'pending' as const,
    createdAt: '2026-10-21T18:04:00Z',
    updatedAt: '2026-10-21T18:04:00Z',
  };

  beforeEach(() => {
    window.history.replaceState(null, '', '/');
  });

  afterEach(() => {
    window.history.replaceState(null, '', '/');
  });

  /** Renders every Delivery section, visits the Delivery tab, then returns to Provisioning. */
  async function renderVisited({
    client,
    deliveryClient,
    personClient,
  }: {
    client: ApiClient;
    deliveryClient: DeliveryApiClient;
    personClient: PersonApiClient;
  }) {
    const user = userEvent.setup();
    render(
      <App
        client={client}
        personClient={personClient}
        deliveryClient={deliveryClient}
        appsEnabled
        destinationsEnabled
        heartbeatsEnabled
        appIdentitiesEnabled
      />,
    );
    await screen.findByText('URLs4IRL');
    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await screen.findByText(strings.heartbeatsEmpty);
    await screen.findByText(strings.identitiesRequestsEmpty);
    await user.click(screen.getByRole('tab', { name: strings.tabProvisioning }));
    return user;
  }

  async function removeUrls4irl(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` }));
    await user.click(
      screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
    );
  }

  /** Lets any stray follow-up fetch land so an "exactly N" count assertion is meaningful. */
  async function settle() {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
  }

  function deliveryFetchCounts(deliveryClient: DeliveryApiClient) {
    return [
      vi.mocked(deliveryClient.listDestinations).mock.calls.length,
      vi.mocked(deliveryClient.listRoutes).mock.calls.length,
      vi.mocked(deliveryClient.listHeartbeats).mock.calls.length,
      vi.mocked(deliveryClient.listIdentityRequests).mock.calls.length,
      vi.mocked(deliveryClient.listIdentityBindings).mock.calls.length,
    ];
  }

  it('re-requests destinations, routes, heartbeats and identities after a successful removal', async () => {
    const deliveryClient = buildFakeDeliveryClient();
    const user = await renderVisited({
      client: buildFakeClient(),
      deliveryClient,
      personClient: buildFakePersonClient({
        listApps: vi.fn().mockImplementation(() => Promise.resolve([{ ...urls4irlApp }])),
      }),
    });
    expect(deliveryFetchCounts(deliveryClient)).toEqual([1, 1, 1, 1, 1]);

    await removeUrls4irl(user);

    await waitFor(() => expect(deliveryFetchCounts(deliveryClient)).toEqual([2, 2, 2, 2, 2]));
    await settle();
    expect(deliveryFetchCounts(deliveryClient)).toEqual([2, 2, 2, 2, 2]);
  });

  it.each([400, 502])('re-requests none of the lists after a %i failure', async (status) => {
    const client = buildFakeClient({
      deprovisionApp: vi
        .fn()
        .mockRejectedValue(new ApiError({ status, message: `said ${status}` })),
    });
    const deliveryClient = buildFakeDeliveryClient();
    const user = await renderVisited({
      client,
      deliveryClient,
      personClient: buildFakePersonClient({ listApps: vi.fn().mockResolvedValue([urls4irlApp]) }),
    });

    await removeUrls4irl(user);

    expect(await screen.findByText(`said ${status}`)).toBeInTheDocument();
    await settle();
    expect(deliveryFetchCounts(deliveryClient)).toEqual([1, 1, 1, 1, 1]);
  });

  it.each([409, 503])('re-requests all four lists after a %i failure', async (status) => {
    const client = buildFakeClient({
      deprovisionApp: vi
        .fn()
        .mockRejectedValue(new ApiError({ status, message: `said ${status}` })),
    });
    const deliveryClient = buildFakeDeliveryClient();
    const personClient = buildFakePersonClient({
      listApps: vi.fn().mockImplementation(() => Promise.resolve([{ ...urls4irlApp }])),
    });
    const user = await renderVisited({ client, deliveryClient, personClient });

    await removeUrls4irl(user);

    await waitFor(() => expect(deliveryFetchCounts(deliveryClient)).toEqual([2, 2, 2, 2, 2]));
    await settle();
    expect(deliveryFetchCounts(deliveryClient)).toEqual([2, 2, 2, 2, 2]);
    expect(personClient.listApps).toHaveBeenCalledTimes(2);
    expect(client.listUsers).toHaveBeenCalledTimes(2);
  });

  it('refreshes the Apps list when a binding is approved', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient({
      listIdentityRequests: vi.fn().mockResolvedValue([pendingRequest]),
      approveIdentityRequest: vi.fn().mockResolvedValue({
        appId: 'app_1',
        githubRepository: '4IRL/app_1',
        githubRepositoryId: 812345601,
        githubOwnerId: 55,
        workflowPath: '.github/workflows/prod-deploy.yml',
        gitRef: 'refs/heads/main',
        environment: 'production',
        approvedAt: '2026-10-21T18:10:00Z',
        registrar: null,
        acknowledgedAt: '2026-10-21T18:10:00Z',
        keys: [],
      }),
    });
    const personClient = buildFakePersonClient();
    render(
      <App
        client={buildFakeClient()}
        personClient={personClient}
        deliveryClient={deliveryClient}
        appsEnabled
        appIdentitiesEnabled
      />,
    );
    await user.click(await screen.findByRole('tab', { name: strings.tabDelivery }));
    const approve = await screen.findByRole('button', {
      name: 'Approve identity request for app_1',
    });
    const before = vi.mocked(personClient.listApps).mock.calls.length;

    await user.click(approve);

    await waitFor(() =>
      expect(vi.mocked(personClient.listApps).mock.calls.length).toBe(before + 1),
    );
  });
});

describe('App held snapshot', () => {
  const urls4irlApp = {
    appId: 'urls4irl',
    displayName: 'URLs4IRL',
    description: null,
    createdAt: '2026-07-25T10:00:00Z',
  };

  beforeEach(() => {
    window.history.replaceState(null, '', '/');
  });

  afterEach(() => {
    window.history.replaceState(null, '', '/');
  });

  function deferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (error: unknown) => void;
    const promise = new Promise<T>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }

  function heldPayload({
    counts,
    unassignedTotal = 0,
  }: {
    counts: Record<string, number>;
    unassignedTotal?: number;
  }): HeldMessages {
    return {
      apps: Object.entries(counts).map(([appId, count]) => ({
        appId,
        count,
        oldestAt: '2026-11-01T00:00:00Z',
      })),
      unassignedChecks: [],
      unassignedTotal,
    };
  }

  // Hidden headings are included: the Provisioning panel is hidden while the Delivery tab is open.
  function unprovisioned(): HTMLElement {
    const section = screen
      .getByRole('heading', { name: strings.unregisteredAppsHeading, hidden: true })
      .closest('section');
    if (section === null) {
      throw new Error('unprovisioned section not found');
    }
    return section;
  }

  function appsPersonClient(apps: unknown[] = [urls4irlApp]) {
    return buildFakePersonClient({
      listApps: vi
        .fn()
        .mockImplementation(() => Promise.resolve(apps.map((app) => ({ ...(app as object) })))),
    } as Partial<PersonApiClient>);
  }

  function renderApps({
    deliveryClient,
    personClient = appsPersonClient(),
    client = buildFakeClient(),
    destinationsEnabled = false,
  }: {
    deliveryClient?: DeliveryApiClient;
    personClient?: PersonApiClient;
    client?: ApiClient;
    destinationsEnabled?: boolean;
  }) {
    render(
      <App
        client={client}
        personClient={personClient}
        deliveryClient={deliveryClient}
        appsEnabled
        destinationsEnabled={destinationsEnabled}
      />,
    );
  }

  async function settle() {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
  }

  async function clickCardRefresh(user: ReturnType<typeof userEvent.setup>) {
    const button = screen.getByRole('button', { name: strings.deliveryStatusRefresh });
    await waitFor(() => expect(button).not.toHaveAttribute('aria-disabled', 'true'));
    await user.click(button);
  }

  function cardRow(label: string): HTMLElement {
    const row = screen.getByText(label, { selector: 'dt' }).parentElement;
    if (row === null) {
      throw new Error(`card row ${label} not found`);
    }
    return row;
  }

  it('shows loading, then the count in the Apps column', async () => {
    const held = deferred<HeldMessages>();
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi.fn().mockReturnValue(held.promise),
    });
    renderApps({ deliveryClient });

    await screen.findByText('URLs4IRL');
    expect(
      within(sectionByHeading(strings.appsHeading)).getAllByText(strings.heldLoading).length,
    ).toBeGreaterThan(0);

    await act(async () => {
      held.resolve(heldPayload({ counts: { urls4irl: 3 } }));
    });

    expect((await screen.findAllByText(strings.heldPill({ count: 3 }))).length).toBeGreaterThan(0);
  });

  it.each([
    { name: '404', rejection: new ApiError({ status: 404, message: 'not found' }) },
    { name: '503', rejection: new ApiError({ status: 503, message: 'down' }) },
    { name: 'network failure', rejection: new TypeError('network down') },
  ])('degrades a $name to unavailable with no banner', async ({ rejection }) => {
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi.fn().mockRejectedValue(rejection),
    });
    renderApps({ deliveryClient });

    expect(
      (await screen.findAllByText(strings.heldUnavailableHidden.trim())).length,
    ).toBeGreaterThan(0);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText(strings.heldZeroSub)).not.toBeInTheDocument();
  });

  it('fetches held counts on load when the Apps section is enabled', async () => {
    const deliveryClient = buildFakeDeliveryClient();
    renderApps({ deliveryClient });

    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(1));
  });

  it('with only the Delivery tab enabled, fetches only after the tab is clicked', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    await screen.findByText(strings.usersEmpty);
    expect(deliveryClient.getHeldMessages).not.toHaveBeenCalled();

    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(1));
  });

  it('makes no held fetch and shows no Held column without a delivery client', async () => {
    renderApps({});

    await screen.findByText('URLs4IRL');
    expect(screen.queryByText(strings.columnHeld)).not.toBeInTheDocument();
  });

  it('re-fetches once after removing an app', async () => {
    const deliveryClient = buildFakeDeliveryClient();
    renderApps({ deliveryClient });
    await screen.findByText('URLs4IRL');
    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(1));

    await userEvent.click(screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` }));
    await userEvent.click(
      screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
    );

    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(2));
    await settle();
    expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(2);
  });

  it('re-fetches once after adding an app', async () => {
    const deliveryClient = buildFakeDeliveryClient();
    renderApps({ deliveryClient });
    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(1));

    const form = within(sectionByHeading(strings.addAppHeading));
    await userEvent.type(form.getByLabelText(strings.appIdLabel), 'tasktracker');
    await userEvent.type(form.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await userEvent.click(form.getByRole('button', { name: strings.addAppAction }));

    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(2));
    await settle();
    expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(2);
  });

  it('re-fetches once on the status card Refresh', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient();
    renderApps({ deliveryClient, destinationsEnabled: true });
    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await screen.findByText(strings.deliveryStatusReady);

    await clickCardRefresh(user);

    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(2));
    await settle();
    expect(deliveryClient.getHeldMessages).toHaveBeenCalledTimes(2);
  });

  it('never lets a stale slow response overwrite a newer one', async () => {
    const user = userEvent.setup();
    const slow = deferred<HeldMessages>();
    const getHeldMessages = vi
      .fn()
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValue(heldPayload({ counts: { urls4irl: 5 } }));
    renderApps({
      deliveryClient: buildFakeDeliveryClient({ getHeldMessages }),
      destinationsEnabled: true,
    });
    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await screen.findByText(strings.deliveryStatusReady);

    await clickCardRefresh(user);
    expect((await screen.findAllByText(strings.heldPill({ count: 5 }))).length).toBeGreaterThan(0);

    await act(async () => {
      slow.resolve(heldPayload({ counts: { urls4irl: 1 } }));
    });
    await settle();

    expect(screen.getAllByText(strings.heldPill({ count: 5 })).length).toBeGreaterThan(0);
    expect(screen.queryByText(strings.heldPill({ count: 1 }))).not.toBeInTheDocument();
  });

  it('feeds the Apps column and the status card from the same snapshot', async () => {
    const user = userEvent.setup();
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi
        .fn()
        .mockResolvedValue(heldPayload({ counts: { urls4irl: 3 }, unassignedTotal: 4 })),
    });
    renderApps({ deliveryClient, destinationsEnabled: true });
    expect((await screen.findAllByText(strings.heldPill({ count: 3 }))).length).toBeGreaterThan(0);

    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));

    await waitFor(() => expect(cardRow(strings.heldMessagesLabel)).toHaveTextContent('3'));
    expect(cardRow(strings.unassignedAlertsLabel)).toHaveTextContent('4');
  });

  it('shows held-only apps under Unprovisioned with 0 subscribers, never None while held loads', async () => {
    const held = deferred<HeldMessages>();
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi.fn().mockReturnValue(held.promise),
    });
    renderApps({ deliveryClient, personClient: appsPersonClient([urls4irlApp]) });
    await screen.findByText('URLs4IRL');

    expect(within(unprovisioned()).getByText(strings.unregisteredAppsLoading)).toBeInTheDocument();
    expect(
      within(unprovisioned()).queryByText(strings.unregisteredAppsEmpty),
    ).not.toBeInTheDocument();

    await act(async () => {
      held.resolve(heldPayload({ counts: { ghost: 2, urls4irl: 1 } }));
    });

    const row = (await within(unprovisioned()).findByText('ghost')).closest('tr');
    if (row === null) {
      throw new Error('ghost row not found');
    }
    expect(within(row).getByText('0')).toBeInTheDocument();
    expect(within(row).getByText(strings.heldPill({ count: 2 }))).toBeInTheDocument();
    // A held id that IS registered never appears there.
    expect(within(unprovisioned()).queryByText('urls4irl')).not.toBeInTheDocument();
  });

  it('adds no held-only rows while listApps rejects, and uses the claim-free empty copy', async () => {
    const personClient = buildFakePersonClient({
      listApps: vi.fn().mockRejectedValue(new Error('apps down')),
    });
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi.fn().mockResolvedValue(heldPayload({ counts: { ghost: 2 } })),
    });
    renderApps({ deliveryClient, personClient });

    expect(
      await within(unprovisioned()).findByText(strings.unregisteredAppsEmpty),
    ).toBeInTheDocument();
    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalled());
    await settle();
    expect(within(unprovisioned()).queryByText('ghost')).not.toBeInTheDocument();
    expect(
      within(unprovisioned()).queryByText(strings.unregisteredAppsEmptyHeldReady),
    ).not.toBeInTheDocument();
  });

  it('shows the extended empty copy when held is ready, nothing is held and the apps list loaded', async () => {
    renderApps({
      deliveryClient: buildFakeDeliveryClient(),
      personClient: appsPersonClient([]),
    });

    expect(
      await within(unprovisioned()).findByText(strings.unregisteredAppsEmptyHeldReady),
    ).toBeInTheDocument();
  });

  it('shows no held-only row until listApps settles', async () => {
    const apps = deferred<unknown[]>();
    const personClient = buildFakePersonClient({
      listApps: vi.fn().mockReturnValue(apps.promise),
    } as Partial<PersonApiClient>);
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi.fn().mockResolvedValue(heldPayload({ counts: { ghost: 2 } })),
    });
    renderApps({ deliveryClient, personClient });
    await waitFor(() => expect(deliveryClient.getHeldMessages).toHaveBeenCalled());
    await settle();
    expect(within(unprovisioned()).queryByText('ghost')).not.toBeInTheDocument();

    await act(async () => {
      apps.resolve([]);
    });

    expect(await within(unprovisioned()).findByText('ghost')).toBeInTheDocument();
  });

  it('hides a just-removed app from Unprovisioned while the held re-fetch is pending', async () => {
    const refetch = deferred<HeldMessages>();
    const getHeldMessages = vi
      .fn()
      .mockResolvedValueOnce(heldPayload({ counts: { urls4irl: 2, other: 3 } }))
      .mockReturnValueOnce(refetch.promise);
    const appsRefetch = deferred<unknown[]>();
    const otherApp = { ...urls4irlApp, appId: 'other', displayName: 'Other' };
    const listApps = vi
      .fn()
      .mockResolvedValueOnce([{ ...urls4irlApp }, { ...otherApp }])
      .mockReturnValueOnce(appsRefetch.promise);
    const personClient = buildFakePersonClient({ listApps });
    renderApps({ deliveryClient: buildFakeDeliveryClient({ getHeldMessages }), personClient });
    expect((await screen.findAllByText(strings.heldPill({ count: 3 }))).length).toBeGreaterThan(0);

    await userEvent.click(screen.getByRole('button', { name: `${strings.removeAction} URLs4IRL` }));
    await userEvent.click(
      screen.getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
    );
    await waitFor(() => expect(listApps).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(getHeldMessages).toHaveBeenCalledTimes(2));

    // Record every committed state of the Unprovisioned section from here on.
    const sawRemovedApp: boolean[] = [];
    const observer = new MutationObserver(() => {
      const text = screen
        .queryByRole('heading', { name: strings.unregisteredAppsHeading, hidden: true })
        ?.closest('section')?.textContent;
      sawRemovedApp.push(text?.includes('urls4irl') ?? false);
    });
    observer.observe(document.body, { childList: true, subtree: true, characterData: true });

    // listApps settles first (explicitly), while the held re-fetch is still pending.
    await act(async () => {
      appsRefetch.resolve([{ ...otherApp }]);
    });
    await settle();

    expect(within(unprovisioned()).queryByText('urls4irl')).not.toBeInTheDocument();
    expect(within(unprovisioned()).getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    expect(screen.queryByText(strings.unregisteredAppsLoading)).not.toBeInTheDocument();
    // The Apps column keeps the previous snapshot: no "Checking…", the old count stays.
    const appsSection = within(sectionByHeading(strings.appsHeading));
    expect(appsSection.queryByText(strings.heldLoading)).not.toBeInTheDocument();
    expect(appsSection.getAllByText(strings.heldPill({ count: 3 })).length).toBeGreaterThan(0);

    // Every committed state while the re-fetch was pending omitted the removed app.
    observer.disconnect();
    expect(sawRemovedApp.length).toBeGreaterThan(0);
    expect(sawRemovedApp).not.toContain(true);

    await act(async () => {
      refetch.resolve(heldPayload({ counts: { urls4irl: 2, other: 3 } }));
    });

    expect(await within(unprovisioned()).findByText('urls4irl')).toBeInTheDocument();
  });

  it('card Refresh hides held-only rows while pending and keeps the old counts', async () => {
    const user = userEvent.setup();
    const refetch = deferred<HeldMessages>();
    const getHeldMessages = vi
      .fn()
      .mockResolvedValueOnce(heldPayload({ counts: { ghost: 2, urls4irl: 1 } }))
      .mockReturnValueOnce(refetch.promise);
    renderApps({
      deliveryClient: buildFakeDeliveryClient({ getHeldMessages }),
      destinationsEnabled: true,
    });
    expect(await within(unprovisioned()).findByText('ghost')).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await screen.findByText(strings.deliveryStatusReady);
    expect(cardRow(strings.heldMessagesLabel)).toHaveTextContent('3');

    await clickCardRefresh(user);
    await waitFor(() => expect(getHeldMessages).toHaveBeenCalledTimes(2));
    await settle();

    expect(within(unprovisioned()).queryByText('ghost')).not.toBeInTheDocument();
    // The Provisioning panel is hidden while the Delivery tab is open.
    const appsHeading = screen.getByRole('heading', { name: strings.appsHeading, hidden: true });
    const appsSection = within(appsHeading.closest('section') as HTMLElement);
    expect(appsSection.queryByText(strings.heldLoading)).not.toBeInTheDocument();
    expect(appsSection.getAllByText(strings.heldPill({ count: 1 })).length).toBeGreaterThan(0);
    expect(cardRow(strings.heldMessagesLabel)).toHaveTextContent('3');
    expect(cardRow(strings.heldMessagesLabel)).not.toHaveTextContent(
      strings.deliveryStatusChecking,
    );

    await act(async () => {
      refetch.resolve(heldPayload({ counts: { ghost: 2, urls4irl: 1 } }));
    });

    expect(await within(unprovisioned()).findByText('ghost')).toBeInTheDocument();
  });

  it('returns the extended empty copy once a re-fetch settles with nothing held', async () => {
    const user = userEvent.setup();
    const refetch = deferred<HeldMessages>();
    const getHeldMessages = vi
      .fn()
      .mockResolvedValueOnce(heldPayload({ counts: {} }))
      .mockReturnValueOnce(refetch.promise);
    renderApps({
      deliveryClient: buildFakeDeliveryClient({ getHeldMessages }),
      personClient: appsPersonClient([]),
      destinationsEnabled: true,
    });
    expect(
      await within(unprovisioned()).findByText(strings.unregisteredAppsEmptyHeldReady),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await screen.findByText(strings.deliveryStatusReady);

    await clickCardRefresh(user);
    await waitFor(() => expect(getHeldMessages).toHaveBeenCalledTimes(2));
    expect(within(unprovisioned()).getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    expect(
      within(unprovisioned()).queryByText(strings.unregisteredAppsEmptyHeldReady),
    ).not.toBeInTheDocument();

    await act(async () => {
      refetch.resolve(heldPayload({ counts: {} }));
    });

    expect(
      await within(unprovisioned()).findByText(strings.unregisteredAppsEmptyHeldReady),
    ).toBeInTheDocument();
  });

  it('keeps held-only rows hidden until the NEWEST of two overlapping re-fetches settles', async () => {
    const user = userEvent.setup();
    const first = deferred<HeldMessages>();
    const second = deferred<HeldMessages>();
    const getHeldMessages = vi
      .fn()
      .mockResolvedValueOnce(heldPayload({ counts: {} }))
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    renderApps({
      deliveryClient: buildFakeDeliveryClient({ getHeldMessages }),
      personClient: appsPersonClient([]),
      destinationsEnabled: true,
    });
    expect(
      await within(unprovisioned()).findByText(strings.unregisteredAppsEmptyHeldReady),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));
    await screen.findByText(strings.deliveryStatusReady);

    await clickCardRefresh(user);
    // A pending re-fetch suppresses the "nothing is waiting" claim.
    expect(within(unprovisioned()).getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    await clickCardRefresh(user);
    await waitFor(() => expect(getHeldMessages).toHaveBeenCalledTimes(3));

    await act(async () => {
      first.resolve(heldPayload({ counts: { ghost: 2 } }));
    });
    await settle();
    expect(within(unprovisioned()).queryByText('ghost')).not.toBeInTheDocument();
    // heldRefreshing stays true until the newest settles: still the claim-free copy.
    expect(within(unprovisioned()).getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    expect(
      within(unprovisioned()).queryByText(strings.unregisteredAppsEmptyHeldReady),
    ).not.toBeInTheDocument();

    await act(async () => {
      second.resolve(heldPayload({ counts: { ghost: 2 } }));
    });
    expect(await within(unprovisioned()).findByText('ghost')).toBeInTheDocument();
  });

  it('renders the card held loading text in the first render after the tab is clicked', async () => {
    const user = userEvent.setup();
    const held = deferred<HeldMessages>();
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi.fn().mockReturnValue(held.promise),
    });
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);
    await screen.findByText(strings.usersEmpty);

    await user.click(screen.getByRole('tab', { name: strings.tabDelivery }));

    expect(await screen.findByText(strings.heldMessagesLabel)).toBeInTheDocument();
    expect(cardRow(strings.heldMessagesLabel)).toHaveTextContent(strings.deliveryStatusChecking);
    expect(cardRow(strings.unassignedAlertsLabel)).toHaveTextContent(
      strings.deliveryStatusChecking,
    );
  });

  it('renders the card held loading text, not a crash, on a direct #delivery load', async () => {
    window.history.replaceState(null, '', '/#delivery');
    const held = deferred<HeldMessages>();
    const deliveryClient = buildFakeDeliveryClient({
      getHeldMessages: vi.fn().mockReturnValue(held.promise),
    });
    render(<App client={buildFakeClient()} deliveryClient={deliveryClient} destinationsEnabled />);

    expect(await screen.findByText(strings.heldMessagesLabel)).toBeInTheDocument();
    expect(cardRow(strings.heldMessagesLabel)).toHaveTextContent(strings.deliveryStatusChecking);
    expect(cardRow(strings.unassignedAlertsLabel)).toHaveTextContent(
      strings.deliveryStatusChecking,
    );
  });
});

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import App from './App';
import { ApiError } from './api/client';
import type { ApiClient } from './api/client';
import type { DeliveryApiClient, DestinationSummary } from './api/deliveryClient';
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
    provisionApp: vi.fn().mockResolvedValue({
      appId: 'urls4irl',
      publisherUserId: 'urls4irl-publisher',
      topicPattern: 'urls4irl-*',
      token: 'tk_publisher',
    }),
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
      within(row).getByRole('button', {
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
      within(row).getByRole('button', {
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
      within(row).getByRole('button', { name: `${strings.confirmDeprovisionAction} urls4irl` }),
    );

    expect(await screen.findByText(strings.genericError)).toBeInTheDocument();
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
        within(row!).getByRole('button', { name: `${strings.confirmDeprovisionAction} urls4irl` }),
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
      within(users).getByRole('button', {
        name: strings.resetPasswordConfirmNamed({ user: 'alice@example.com' }),
      }),
    );

    expect(await within(users).findByDisplayValue('abcd-efgh-ijkm-npqr')).toBeInTheDocument();
    expect(client.resetPassword).toHaveBeenCalledWith({
      appId: 'urls4irl',
      email: 'alice@example.com',
    });
    expect(listUsers).toHaveBeenCalledTimes(1);
    expect(personClient.listPeople).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
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
      within(row).getByRole('button', {
        name: `${strings.confirmDeleteAction} alice@example.com`,
      }),
    );

    await waitFor(() =>
      expect(client.deleteUser).toHaveBeenCalledWith({ userId: 'u_76gzqgp4byjl6dje' }),
    );
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

  it('adds an app: registers metadata, mints the publisher token, and reveals it', async () => {
    const client = buildFakeClient();
    const personClient = buildFakePersonClient();
    render(<App client={client} personClient={personClient} appsEnabled />);

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
    expect(await screen.findByText('tk_publisher')).toBeInTheDocument();
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
      within(appRow).getByRole('button', { name: `${strings.confirmRemoveAction} URLs4IRL` }),
    );

    await waitFor(() => expect(client.deprovisionApp).toHaveBeenCalledWith({ appId: 'urls4irl' }));
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

  describe('Heartbeats section', () => {
    function destination(name: string, enabled = true): DestinationSummary {
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

      await waitFor(() => expect(add).toHaveAttribute('aria-disabled', 'true'));
      expect(document.getElementById(add.getAttribute('aria-describedby') ?? '')).toHaveTextContent(
        strings.addHeartbeatHintEmpty,
      );
    });
  });
});

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import App from './App';
import { ApiError } from './api/client';
import type { ApiClient } from './api/client';
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

    expect(await screen.findByText('u_abcdefgh23456777')).toBeInTheDocument();
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

    const row = (await screen.findByText('u_abcdefgh23456777')).closest('tr');
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

    const row = (await screen.findByText('u_abcdefgh23456777')).closest('tr');
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
    // and the People table (its own email column) once people load.
    await waitFor(() => expect(screen.getAllByText('alice@example.com')).toHaveLength(2));
    expect(screen.queryByText('u_76gzqgp4byjl6dje')).not.toBeInTheDocument();
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
});

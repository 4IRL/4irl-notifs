import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import App from './App';
import { ApiError } from './api/client';
import type { ApiClient } from './api/client';
import { strings } from './strings';

// buildFakeClient returns an ApiClient test double with vi.fn() methods,
// overridable per test.
function buildFakeClient(overrides: Partial<ApiClient> = {}): ApiClient {
  return {
    provision: vi.fn().mockResolvedValue({
      userId: 'alice',
      appId: 'urls4irl',
      topicPattern: 'urls4irl-*',
      token: 'tk_generated',
    }),
    deprovision: vi.fn().mockResolvedValue(undefined),
    listUsers: vi.fn().mockResolvedValue([]),
    deleteUser: vi.fn().mockResolvedValue(undefined),
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
      listUsers: vi
        .fn()
        .mockResolvedValue([
          { userId: 'alice', apps: ['urls4irl'], topicPatterns: ['urls4irl-*'] },
        ]),
    });
    render(<App client={client} />);

    expect(await screen.findByText('alice')).toBeInTheDocument();
    expect(client.listUsers).toHaveBeenCalled();
  });

  it('provisioning a user shows the token and refreshes the user list', async () => {
    const listUsers = vi
      .fn()
      .mockResolvedValueOnce([])
      .mockResolvedValue([{ userId: 'alice', apps: ['urls4irl'], topicPatterns: ['urls4irl-*'] }]);
    const client = buildFakeClient({ listUsers });
    render(<App client={client} />);

    await userEvent.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await userEvent.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await userEvent.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('tk_generated')).toBeInTheDocument();
    await waitFor(() =>
      expect(client.provision).toHaveBeenCalledWith({ appId: 'urls4irl', userId: 'alice' }),
    );
    // list refreshed: mount call + post-provision refresh
    await waitFor(() => expect(listUsers.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it('deleting a user calls the client and refreshes', async () => {
    const listUsers = vi
      .fn()
      .mockResolvedValueOnce([{ userId: 'alice', apps: [], topicPatterns: [] }])
      .mockResolvedValue([]);
    const client = buildFakeClient({ listUsers });
    render(<App client={client} />);

    const row = (await screen.findByText('alice')).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deleteAction} alice` }),
    );

    await waitFor(() => expect(client.deleteUser).toHaveBeenCalledWith({ userId: 'alice' }));
  });

  it('shows the ApiError message when deleting a user fails', async () => {
    const listUsers = vi.fn().mockResolvedValue([{ userId: 'alice', apps: [], topicPatterns: [] }]);
    const deleteUser = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 404, message: 'user alice does not exist' }));
    const client = buildFakeClient({ listUsers, deleteUser });
    render(<App client={client} />);

    const row = (await screen.findByText('alice')).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deleteAction} alice` }),
    );

    expect(await screen.findByText('user alice does not exist')).toBeInTheDocument();
  });

  it('shows the generic error message when a deprovision fails with a non-ApiError', async () => {
    const listUsers = vi
      .fn()
      .mockResolvedValue([{ userId: 'alice', apps: ['urls4irl'], topicPatterns: ['urls4irl-*'] }]);
    const deprovision = vi.fn().mockRejectedValue(new Error('network down'));
    const client = buildFakeClient({ listUsers, deprovision });
    render(<App client={client} />);

    const row = (await screen.findByText('alice')).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    await userEvent.click(
      within(row).getByRole('button', { name: `${strings.deprovisionAction} urls4irl` }),
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
    const listUsers = vi
      .fn()
      .mockResolvedValue([{ userId: 'alice', apps: ['urls4irl'], topicPatterns: ['urls4irl-*'] }]);
    const deprovision = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'ntfy CLI failed' }))
      .mockResolvedValue(undefined);
    const client = buildFakeClient({ listUsers, deprovision });
    render(<App client={client} />);

    const row = (await screen.findByText('alice')).closest('tr');
    if (row === null) {
      throw new Error('user row not found');
    }
    const deprovisionButton = within(row).getByRole('button', {
      name: `${strings.deprovisionAction} urls4irl`,
    });

    await userEvent.click(deprovisionButton);
    expect(await screen.findByText('ntfy CLI failed')).toBeInTheDocument();

    await userEvent.click(deprovisionButton);
    await waitFor(() => expect(screen.queryByText('ntfy CLI failed')).not.toBeInTheDocument());
  });
});

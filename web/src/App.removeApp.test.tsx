import { render, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import App from './App';
import { ApiError } from './api/client';
import type { ApiClient } from './api/client';
import type { PersonApiClient } from './api/personClient';

// AppsTable is replaced by a probe that captures the `onRemoveApp` prop, so the handler can be
// driven directly (the busy ConfirmButton refuses a second UI click).
type RemoveHandler = (params: { appId: string }) => Promise<boolean>;
const captured: { onRemoveApp: RemoveHandler | null } = { onRemoveApp: null };

vi.mock('./components/AppsTable', () => ({
  AppsTable: (props: { onRemoveApp: RemoveHandler }) => {
    captured.onRemoveApp = props.onRemoveApp;
    return null;
  },
}));

function buildClient(overrides: Partial<ApiClient> = {}): ApiClient {
  return {
    provision: vi.fn(),
    resetPassword: vi.fn(),
    deprovision: vi.fn(),
    listUsers: vi.fn().mockResolvedValue([]),
    deleteUser: vi.fn(),
    provisionApp: vi.fn(),
    deprovisionApp: vi.fn().mockResolvedValue(undefined),
    testNotify: vi.fn(),
    ...overrides,
  };
}

function buildPersonClient(): PersonApiClient {
  return {
    listPeople: vi.fn().mockResolvedValue([]),
    listApps: vi.fn().mockImplementation(() => Promise.resolve([])),
    createApp: vi.fn(),
    updateApp: vi.fn(),
    deleteApp: vi.fn(),
  };
}

async function mountApp(client: ApiClient, personClient: PersonApiClient): Promise<RemoveHandler> {
  render(<App client={client} personClient={personClient} appsEnabled />);
  await waitFor(() => expect(captured.onRemoveApp).not.toBeNull());
  await waitFor(() => expect(client.listUsers).toHaveBeenCalled());
  await waitFor(() => expect(personClient.listApps).toHaveBeenCalled());
  return (params) => (captured.onRemoveApp as RemoveHandler)(params);
}

beforeEach(() => {
  captured.onRemoveApp = null;
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('App handleRemoveApp outcome', () => {
  it('ignores a second call for an app already in flight, then resolves true for the first', async () => {
    let resolveRequest: () => void = () => undefined;
    const client = buildClient({
      deprovisionApp: vi.fn().mockReturnValue(
        new Promise<void>((resolve) => {
          resolveRequest = resolve;
        }),
      ),
    });
    const remove = await mountApp(client, buildPersonClient());

    const first = remove({ appId: 'urls4irl' });
    const second = remove({ appId: 'urls4irl' });

    await expect(second).resolves.toBe(false);
    expect(client.deprovisionApp).toHaveBeenCalledTimes(1);
    resolveRequest();
    await expect(first).resolves.toBe(true);
  });

  it('resolves true on success and refreshes apps and users', async () => {
    const client = buildClient();
    const personClient = buildPersonClient();
    const remove = await mountApp(client, personClient);
    vi.mocked(client.listUsers).mockClear();
    vi.mocked(personClient.listApps).mockClear();

    await expect(remove({ appId: 'urls4irl' })).resolves.toBe(true);

    expect(client.deprovisionApp).toHaveBeenCalledWith({ appId: 'urls4irl' });
    expect(client.listUsers).toHaveBeenCalledTimes(1);
    expect(personClient.listApps).toHaveBeenCalledTimes(1);
  });

  it.each([
    { label: '400', rejection: new ApiError({ status: 400, message: 'bad' }), refreshes: false },
    { label: '502', rejection: new ApiError({ status: 502, message: 'bad' }), refreshes: false },
    { label: '409', rejection: new ApiError({ status: 409, message: 'busy' }), refreshes: true },
    { label: '503', rejection: new ApiError({ status: 503, message: 'down' }), refreshes: true },
    { label: 'plain Error', rejection: new Error('network'), refreshes: false },
  ])('resolves false (never rejects) on $label', async ({ rejection, refreshes }) => {
    const client = buildClient({ deprovisionApp: vi.fn().mockRejectedValue(rejection) });
    const personClient = buildPersonClient();
    const remove = await mountApp(client, personClient);
    vi.mocked(client.listUsers).mockClear();
    vi.mocked(personClient.listApps).mockClear();

    await expect(remove({ appId: 'urls4irl' })).resolves.toBe(false);

    // The handler awaits any refresh before resolving, so these counts are final.
    expect(client.listUsers).toHaveBeenCalledTimes(refreshes ? 1 : 0);
    expect(personClient.listApps).toHaveBeenCalledTimes(refreshes ? 1 : 0);
  });
});

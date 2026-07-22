import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { invoke, jsonResponse, makeEnv } from '../test-helpers';
import { onRequest } from './[[path]]';

describe('v1 catch-all proxy route', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('forwards a GET /v1/users to the provisioning API', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));

    await invoke({
      onRequest,
      request: new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' }),
      env: makeEnv(),
    });

    expect(fetchMock.mock.calls[0][0]).toBe('https://notifs-api.4irl.app/v1/users');
  });

  it('forwards a DELETE /v1/users/u_abc to the provisioning API (catch-all path)', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { deleted: true } }));

    await invoke({
      onRequest,
      request: new Request('https://notifs-admin.4irl.app/v1/users/u_abc', { method: 'DELETE' }),
      env: makeEnv(),
    });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-api.4irl.app/v1/users/u_abc');
    expect(calledInit.method).toBe('DELETE');
  });
});

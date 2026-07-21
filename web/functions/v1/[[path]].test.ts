import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Env } from '../_proxy';
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

  function makeEnv(): Env {
    return {
      PROVISIONING_API_URL: 'https://notifs-api.4irl.app',
      PERSON_SERVICE_URL: 'https://notifs-people.4irl.app',
      PROXY_ACCESS_CLIENT_ID: 'id',
      PROXY_ACCESS_CLIENT_SECRET: 'sec',
    };
  }

  function jsonResponse({ status, body }: { status: number; body: unknown }): Response {
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  function invoke({ request, env }: { request: Request; env: Env }): Response | Promise<Response> {
    return onRequest({ request, env } as Parameters<typeof onRequest>[0]);
  }

  it('forwards a GET /v1/users to the provisioning API', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));

    await invoke({
      request: new Request('https://notifs-admin.4irl.app/v1/users', { method: 'GET' }),
      env: makeEnv(),
    });

    expect(fetchMock.mock.calls[0][0]).toBe('https://notifs-api.4irl.app/v1/users');
  });

  it('forwards a DELETE /v1/users/u_abc to the provisioning API (catch-all path)', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { deleted: true } }));

    await invoke({
      request: new Request('https://notifs-admin.4irl.app/v1/users/u_abc', { method: 'DELETE' }),
      env: makeEnv(),
    });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-api.4irl.app/v1/users/u_abc');
    expect(calledInit.method).toBe('DELETE');
  });
});

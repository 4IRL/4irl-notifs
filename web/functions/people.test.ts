import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Env } from './_proxy';
import { onRequest } from './people';

describe('people proxy route', () => {
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

  it('forwards GET /people to the person service with the service-token headers', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { people: [] } }));

    await invoke({
      request: new Request('https://notifs-admin.4irl.app/people', { method: 'GET' }),
      env: makeEnv(),
    });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-people.4irl.app/people');
    const headers = calledInit.headers as Headers;
    expect(headers.get('CF-Access-Client-Id')).toBe('id');
    expect(headers.get('CF-Access-Client-Secret')).toBe('sec');
  });
});

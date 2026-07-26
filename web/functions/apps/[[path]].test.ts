import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { onRequest } from './[[path]]';
import { invoke, jsonResponse, makeEnv } from '../test-helpers';

describe('apps proxy route', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('forwards GET /apps to the person service with the service-token headers', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { apps: [] } }));

    await invoke({
      onRequest,
      request: new Request('https://notifs-admin.4irl.app/apps', { method: 'GET' }),
      env: makeEnv(),
    });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-people.4irl.app/apps');
    const headers = calledInit.headers as Headers;
    expect(headers.get('CF-Access-Client-Id')).toBe('id');
    expect(headers.get('CF-Access-Client-Secret')).toBe('sec');
  });

  it('preserves the pathname when forwarding a per-app request (PATCH /apps/:id)', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          app_id: 'urls4irl',
          display_name: 'URLs4IRL',
          description: null,
          created_at: '2026-07-25T10:00:00Z',
        },
      }),
    );

    await invoke({
      onRequest,
      request: new Request('https://notifs-admin.4irl.app/apps/urls4irl', {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ display_name: 'URLs4IRL' }),
      }),
      env: makeEnv(),
    });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledUrl).toBe('https://notifs-people.4irl.app/apps/urls4irl');
    expect(calledInit.method).toBe('PATCH');
  });
});

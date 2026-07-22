import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { onRequest } from './people';
import { invoke, jsonResponse, makeEnv } from './test-helpers';

describe('people proxy route', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('forwards GET /people to the person service with the service-token headers', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { people: [] } }));

    await invoke({
      onRequest,
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

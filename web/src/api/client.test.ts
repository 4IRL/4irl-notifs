import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError, createApiClient } from './client';

describe('api client', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  function jsonResponse({ status, body }: { status: number; body: unknown }): Response {
    return new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  it('provision POSTs the pair and returns the parsed token result', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          user_id: 'alice',
          app_id: 'urls4irl',
          topic_pattern: 'urls4irl-*',
          token: 'tk_abc',
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const result = await client.provision({ appId: 'urls4irl', userId: 'alice' });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/v1/provision');
    expect(calledInit).toMatchObject({ method: 'POST' });
    expect(JSON.parse(calledInit.body as string)).toEqual({ app_id: 'urls4irl', user_id: 'alice' });
    expect(result).toEqual({
      userId: 'alice',
      appId: 'urls4irl',
      topicPattern: 'urls4irl-*',
      token: 'tk_abc',
    });
  });

  it('deprovision POSTs the pair', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 200, body: { user_id: 'alice', app_id: 'urls4irl', removed: true } }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.deprovision({ appId: 'urls4irl', userId: 'alice' });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/v1/deprovision');
    expect(JSON.parse(calledInit.body as string)).toEqual({ app_id: 'urls4irl', user_id: 'alice' });
  });

  it('listUsers GETs and maps the users array', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          users: [
            { user_id: 'alice', apps: ['urls4irl'], topic_patterns: ['urls4irl-*'] },
            { user_id: 'bob', apps: [], topic_patterns: [] },
          ],
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const users = await client.listUsers();

    expect(fetchMock.mock.calls[0][0]).toBe('https://api.test/v1/users');
    expect(users).toEqual([
      { userId: 'alice', apps: ['urls4irl'], topicPatterns: ['urls4irl-*'] },
      { userId: 'bob', apps: [], topicPatterns: [] },
    ]);
  });

  it('deleteUser DELETEs the user by id', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 200, body: { user_id: 'alice', deleted: true } }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.deleteUser({ userId: 'alice' });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/v1/users/alice');
    expect(calledInit).toMatchObject({ method: 'DELETE' });
  });

  it('throws ApiError carrying the server error message on non-2xx', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 400, body: { error: 'invalid app_id' } }));
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await expect(client.provision({ appId: 'BAD', userId: 'alice' })).rejects.toMatchObject({
      name: 'ApiError',
      status: 400,
      message: 'invalid app_id',
    });
    await expect(client.provision({ appId: 'BAD', userId: 'alice' })).rejects.toBeInstanceOf(
      ApiError,
    );
  });

  it('defaults the base URL to same-origin root', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    const client = createApiClient({ fetchImpl: fetchMock });

    await client.listUsers();

    expect(fetchMock.mock.calls[0][0]).toBe('/v1/users');
  });
});

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError, createApiClient, isAppNotBound } from './client';

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

  it('provision POSTs app_id/email and returns the parsed token result', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          user_id: 'u_76gzqgp4byjl6dje',
          app_id: 'urls4irl',
          person_hash: '76gzqgp4byjl6dje',
          topic_pattern: 'urls4irl-76gzqgp4byjl6dje-*',
          broadcast_topic: 'urls4irl-broadcast',
          token: 'tk_abc',
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const result = await client.provision({
      appId: 'urls4irl',
      email: 'alice@example.com',
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/delivery/v1/admin/provision');
    expect(calledInit).toMatchObject({ method: 'POST' });
    expect(JSON.parse(calledInit.body as string)).toEqual({
      app_id: 'urls4irl',
      email: 'alice@example.com',
    });
    expect(result).toEqual({
      userId: 'u_76gzqgp4byjl6dje',
      appId: 'urls4irl',
      personHash: '76gzqgp4byjl6dje',
      topicPattern: 'urls4irl-76gzqgp4byjl6dje-*',
      broadcastTopic: 'urls4irl-broadcast',
      token: 'tk_abc',
    });
  });

  it('provision maps the optional password when the API returns one', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          user_id: 'u_76gzqgp4byjl6dje',
          app_id: 'urls4irl',
          person_hash: '76gzqgp4byjl6dje',
          topic_pattern: 'urls4irl-76gzqgp4byjl6dje-*',
          broadcast_topic: 'urls4irl-broadcast',
          token: 'tk_abc',
          password: 'abcd-efgh-ijkm-npqr',
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const result = await client.provision({ appId: 'urls4irl', email: 'alice@example.com' });

    expect(result.password).toBe('abcd-efgh-ijkm-npqr');
  });

  it('provision leaves password undefined when the API omits it', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          user_id: 'u_76gzqgp4byjl6dje',
          app_id: 'urls4irl',
          person_hash: '76gzqgp4byjl6dje',
          topic_pattern: 'urls4irl-76gzqgp4byjl6dje-*',
          broadcast_topic: 'urls4irl-broadcast',
          token: 'tk_abc',
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const result = await client.provision({ appId: 'urls4irl', email: 'alice@example.com' });

    expect(result.password).toBeUndefined();
    expect('password' in result).toBe(false);
  });

  it('deprovision POSTs the pair', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 200, body: { user_id: 'alice', app_id: 'urls4irl', removed: true } }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.deprovision({ appId: 'urls4irl', userId: 'alice' });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/delivery/v1/admin/deprovision');
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

    expect(fetchMock.mock.calls[0][0]).toBe('https://api.test/delivery/v1/admin/users');
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
    expect(calledUrl).toBe('https://api.test/delivery/v1/admin/users/alice');
    expect(calledInit).toMatchObject({ method: 'DELETE' });
  });

  it('deleteUser URL-encodes a user id with reserved characters', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: {} }));
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.deleteUser({ userId: 'a b+c' });

    expect(fetchMock.mock.calls[0][0]).toBe('https://api.test/delivery/v1/admin/users/a%20b%2Bc');
  });

  it('sends X-Requested-With: admin-ui on every non-GET and omits it on GET', async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        jsonResponse({
          status: 200,
          body: { users: [], results: [], user_id: 'u', app_id: 'a', password: 'p', token: 't' },
        }),
      ),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.provision({ appId: 'a', email: 'x@y.z' });
    await client.resetPassword({ appId: 'a', email: 'x@y.z' });
    await client.deprovision({ appId: 'a', userId: 'u' });
    await client.deleteUser({ userId: 'u' });
    await client.testNotify({
      appId: 'a',
      recipients: ['r'],
      channel: 'c',
      message: 'm',
      title: 't',
    });
    await client.listUsers();

    const inits = fetchMock.mock.calls.map((call) => call[1] as RequestInit);
    expect(inits).toHaveLength(6);
    for (const init of inits.slice(0, 5)) {
      expect((init.headers as Record<string, string>)['X-Requested-With']).toBe('admin-ui');
    }
    const getHeaders = (inits[5].headers ?? {}) as Record<string, string>;
    expect(getHeaders['X-Requested-With']).toBeUndefined();
  });

  it('provisionApp POSTs app_id/rotate and returns the parsed publisher token result', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          app_id: 'urls4irl',
          publisher_user_id: 'urls4irl-publisher',
          topic_pattern: 'urls4irl-*',
          token: 'tk_pub',
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const result = await client.provisionApp({ appId: 'urls4irl', rotate: true });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/v1/provision-app');
    expect(calledInit).toMatchObject({ method: 'POST' });
    expect(JSON.parse(calledInit.body as string)).toEqual({ app_id: 'urls4irl', rotate: true });
    expect(result).toEqual({
      appId: 'urls4irl',
      publisherUserId: 'urls4irl-publisher',
      topicPattern: 'urls4irl-*',
      token: 'tk_pub',
    });
  });

  it('provisionApp defaults rotate to false when omitted', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          app_id: 'urls4irl',
          publisher_user_id: 'urls4irl-publisher',
          topic_pattern: 'urls4irl-*',
          token: 'tk_pub',
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.provisionApp({ appId: 'urls4irl' });

    const [, calledInit] = fetchMock.mock.calls[0];
    expect(JSON.parse(calledInit.body as string)).toEqual({ app_id: 'urls4irl', rotate: false });
  });

  it('deprovisionApp POSTs the app_id', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 200, body: { app_id: 'urls4irl', removed: true } }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.deprovisionApp({ appId: 'urls4irl' });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/v1/deprovision-app');
    expect(calledInit).toMatchObject({ method: 'POST' });
    expect(JSON.parse(calledInit.body as string)).toEqual({ app_id: 'urls4irl' });
  });

  it('testNotify POSTs the snake_case body and maps the results to camelCase', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          results: [
            {
              recipient: '76gzqgp4byjl6dje',
              user_id: 'u_76gzqgp4byjl6dje',
              topic: 'urls4irl-76gzqgp4byjl6dje-all',
              ok: true,
              message_id: 'VkT2p9wQ',
              error: '',
            },
            {
              recipient: 'alice@example.com',
              user_id: 'u_76gzqgp4byjl6dje',
              topic: 'urls4irl-76gzqgp4byjl6dje-all',
              ok: false,
              message_id: '',
              error: 'ntfy publish failed (503)',
            },
          ],
        },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const result = await client.testNotify({
      appId: 'urls4irl',
      recipients: ['76gzqgp4byjl6dje', 'alice@example.com'],
      channel: 'all',
      message: 'Test notification from 4IRL admin',
      title: 'Chore Reaper test',
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/delivery/v1/admin/test-notify');
    expect(calledInit).toMatchObject({ method: 'POST' });
    expect(JSON.parse(calledInit.body as string)).toEqual({
      app_id: 'urls4irl',
      recipients: ['76gzqgp4byjl6dje', 'alice@example.com'],
      channel: 'all',
      message: 'Test notification from 4IRL admin',
      title: 'Chore Reaper test',
    });
    expect(result).toEqual({
      results: [
        {
          recipient: '76gzqgp4byjl6dje',
          userId: 'u_76gzqgp4byjl6dje',
          topic: 'urls4irl-76gzqgp4byjl6dje-all',
          ok: true,
          messageId: 'VkT2p9wQ',
          error: '',
        },
        {
          recipient: 'alice@example.com',
          userId: 'u_76gzqgp4byjl6dje',
          topic: 'urls4irl-76gzqgp4byjl6dje-all',
          ok: false,
          messageId: '',
          error: 'ntfy publish failed (503)',
        },
      ],
    });
  });

  it('testNotify throws ApiError carrying the server error message on 400', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 400, body: { error: 'invalid channel' } }));
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await expect(
      client.testNotify({
        appId: 'urls4irl',
        recipients: ['76gzqgp4byjl6dje'],
        channel: 'BAD',
        message: 'hi',
        title: 'urls4irl test',
      }),
    ).rejects.toMatchObject({
      name: 'ApiError',
      status: 400,
      message: 'invalid channel',
    });
  });

  it('throws ApiError carrying the server error message on non-2xx', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 400, body: { error: 'invalid app_id' } }));
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await expect(
      client.provision({ appId: 'BAD', email: 'alice@example.com' }),
    ).rejects.toMatchObject({
      name: 'ApiError',
      status: 400,
      message: 'invalid app_id',
    });
    await expect(
      client.provision({ appId: 'BAD', email: 'alice@example.com' }),
    ).rejects.toBeInstanceOf(ApiError);
  });

  it('resetPassword POSTs app_id/email and maps the result', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: { user_id: 'u_abc', app_id: 'urls4irl', password: 'abcd-efgh-ijkm-npqr' },
      }),
    );
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    const result = await client.resetPassword({ appId: 'urls4irl', email: 'alice@example.com' });

    expect(result).toEqual({
      userId: 'u_abc',
      appId: 'urls4irl',
      password: 'abcd-efgh-ijkm-npqr',
    });
    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://api.test/delivery/v1/admin/reset-password');
    expect(calledInit.method).toBe('POST');
    expect(JSON.parse(calledInit.body as string)).toEqual({
      app_id: 'urls4irl',
      email: 'alice@example.com',
    });
  });

  it('resetPassword failures carry the status and Retry-After seconds, never a password', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: 'too many password changes' }), {
        status: 429,
        headers: { 'Content-Type': 'application/json', 'Retry-After': '42' },
      }),
    );
    const client = createApiClient({ fetchImpl: fetchMock });

    await expect(
      client.resetPassword({ appId: 'urls4irl', email: 'alice@example.com' }),
    ).rejects.toMatchObject({
      name: 'ApiError',
      status: 429,
      message: 'too many password changes',
      retryAfterSeconds: 42,
    });
  });

  it.each([null, 'soon', '0', '-5', '1.5', ''])(
    'ApiError leaves retryAfterSeconds unset for Retry-After %j',
    async (header) => {
      fetchMock.mockResolvedValue(
        new Response(JSON.stringify({ error: 'too many password changes' }), {
          status: 429,
          headers: header === null ? {} : { 'Retry-After': header },
        }),
      );
      const client = createApiClient({ fetchImpl: fetchMock });

      const failure = await client
        .resetPassword({ appId: 'urls4irl', email: 'alice@example.com' })
        .catch((rejection: unknown) => rejection);

      expect(failure).toBeInstanceOf(ApiError);
      expect((failure as ApiError).retryAfterSeconds).toBeUndefined();
    },
  );

  it("sends credentials so the admin app's Access session cookie reaches the same-origin proxy Function", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    const client = createApiClient({ baseUrl: 'https://api.test', fetchImpl: fetchMock });

    await client.listUsers();

    const [, calledInit] = fetchMock.mock.calls[0];
    expect(calledInit).toMatchObject({ credentials: 'include' });
  });

  it('defaults the base URL to same-origin root', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { users: [] } }));
    const client = createApiClient({ fetchImpl: fetchMock });

    await client.listUsers();

    expect(fetchMock.mock.calls[0][0]).toBe('/delivery/v1/admin/users');
  });

  describe('isAppNotBound', () => {
    it('is true for a 404 with the not-bound message', () => {
      expect(
        isAppNotBound(new ApiError({ status: 404, message: 'app has no approved binding' })),
      ).toBe(true);
    });

    it('is false for a 404 with another message', () => {
      expect(isAppNotBound(new ApiError({ status: 404, message: 'publisher not found' }))).toBe(
        false,
      );
    });

    it('is false for a 409 with the not-bound message', () => {
      expect(
        isAppNotBound(new ApiError({ status: 409, message: 'app has no approved binding' })),
      ).toBe(false);
    });

    it('is false for a non-ApiError', () => {
      expect(isAppNotBound(new Error('app has no approved binding'))).toBe(false);
    });
  });
});

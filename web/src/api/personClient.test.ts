import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from './client';
import { createPersonApiClient } from './personClient';

describe('person api client', () => {
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

  it('listPeople GETs /people and maps the people array', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          people: [
            {
              person_hash: '76gzqgp4byjl6dje',
              email: 'alice@example.com',
              created_at: '2026-07-19T18:12:03Z',
            },
          ],
        },
      }),
    );
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    const people = await client.listPeople();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://person.test/people');
    expect(calledInit).toMatchObject({ method: 'GET', credentials: 'include' });
    expect(people).toEqual([
      {
        personHash: '76gzqgp4byjl6dje',
        email: 'alice@example.com',
        createdAt: '2026-07-19T18:12:03Z',
      },
    ]);
  });

  it('returns an empty array when the server reports no people', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { people: [] } }));
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    const people = await client.listPeople();

    expect(people).toEqual([]);
  });

  it('throws ApiError carrying the server error message on non-2xx', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 503, body: { error: 'worker unavailable' } }),
    );
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    await expect(client.listPeople()).rejects.toMatchObject({
      name: 'ApiError',
      status: 503,
      message: 'worker unavailable',
    });
    await expect(client.listPeople()).rejects.toBeInstanceOf(ApiError);
  });

  it('falls back to a generic status message on non-2xx with no wire error', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 500, body: {} }));
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    await expect(client.listPeople()).rejects.toMatchObject({
      name: 'ApiError',
      status: 500,
      message: 'request failed (500)',
    });
  });

  it('trims a trailing slash on the base URL', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { people: [] } }));
    const client = createPersonApiClient({
      baseUrl: 'https://person.test/',
      fetchImpl: fetchMock,
    });

    await client.listPeople();

    expect(fetchMock.mock.calls[0][0]).toBe('https://person.test/people');
  });
});

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

  it('defaults the base URL to same-origin root', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ status: 200, body: { people: [] } }));
    const client = createPersonApiClient({ fetchImpl: fetchMock });

    await client.listPeople();

    expect(fetchMock.mock.calls[0][0]).toBe('/people');
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

  it('listApps GETs /apps and maps the apps array', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          apps: [
            {
              app_id: 'urls4irl',
              display_name: 'URLs4IRL',
              description: 'Shared URL app',
              created_at: '2026-07-19T18:12:03Z',
            },
          ],
        },
      }),
    );
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    const apps = await client.listApps();

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://person.test/apps');
    expect(calledInit).toMatchObject({ method: 'GET', credentials: 'include' });
    expect(apps).toEqual([
      {
        appId: 'urls4irl',
        displayName: 'URLs4IRL',
        description: 'Shared URL app',
        createdAt: '2026-07-19T18:12:03Z',
      },
    ]);
  });

  it('createApp POSTs /apps with snake_case metadata and returns the mapped app', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 201,
        body: {
          app_id: 'tasktracker',
          display_name: 'Task Tracker',
          description: null,
          created_at: '2026-07-25T10:00:00Z',
        },
      }),
    );
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    const app = await client.createApp({ appId: 'tasktracker', displayName: 'Task Tracker' });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://person.test/apps');
    expect(calledInit).toMatchObject({ method: 'POST' });
    expect(JSON.parse(calledInit.body as string)).toEqual({
      app_id: 'tasktracker',
      display_name: 'Task Tracker',
    });
    expect(app).toEqual({
      appId: 'tasktracker',
      displayName: 'Task Tracker',
      description: null,
      createdAt: '2026-07-25T10:00:00Z',
    });
  });

  it('updateApp PATCHes only the fields provided, omitting untouched ones', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          app_id: 'urls4irl',
          display_name: 'New Name',
          description: 'kept',
          created_at: '2026-07-19T18:12:03Z',
        },
      }),
    );
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    await client.updateApp({ appId: 'urls4irl', displayName: 'New Name' });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://person.test/apps/urls4irl');
    expect(calledInit).toMatchObject({ method: 'PATCH' });
    expect(JSON.parse(calledInit.body as string)).toEqual({ display_name: 'New Name' });
  });

  it('updateApp sends description: null to explicitly clear it', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        status: 200,
        body: {
          app_id: 'urls4irl',
          display_name: 'Name',
          description: null,
          created_at: '2026-07-19T18:12:03Z',
        },
      }),
    );
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    await client.updateApp({ appId: 'urls4irl', description: null });

    const [, calledInit] = fetchMock.mock.calls[0];
    expect(JSON.parse(calledInit.body as string)).toEqual({ description: null });
  });

  it('deleteApp DELETEs the app by id', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ status: 200, body: { app_id: 'urls4irl', deleted: true } }),
    );
    const client = createPersonApiClient({ baseUrl: 'https://person.test', fetchImpl: fetchMock });

    await client.deleteApp({ appId: 'urls4irl' });

    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe('https://person.test/apps/urls4irl');
    expect(calledInit).toMatchObject({ method: 'DELETE' });
  });
});

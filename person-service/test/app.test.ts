import { env, SELF } from 'cloudflare:test';
import { beforeEach, describe, expect, it } from 'vitest';

// Storage isolation is per test FILE (see person.test.ts), so clear the app
// table before every test to keep rows from leaking between it() blocks.
beforeEach(async () => {
  await env.DB.exec('DELETE FROM app');
});

/** Fetches from the running Worker with a JSON body, mirroring how a caller would. */
async function fetchJson({
  path,
  method,
  body,
}: {
  path: string;
  method: string;
  body?: unknown;
}): Promise<Response> {
  return SELF.fetch(`http://person-service.local${path}`, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

interface AppBody {
  app_id: string;
  display_name: string;
  description: string | null;
  created_at: string;
}

describe('POST /apps', () => {
  it('registers a new app and returns 201 with created_at set', async () => {
    const response = await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'urls4irl', display_name: 'URLs4IRL', description: 'Shared URL app' },
    });

    expect(response.status).toBe(201);
    expect(response.headers.get('Content-Type')).toBe('application/json');
    const payload = (await response.json()) as AppBody;
    expect(payload.app_id).toBe('urls4irl');
    expect(payload.display_name).toBe('URLs4IRL');
    expect(payload.description).toBe('Shared URL app');
    expect(payload.created_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
  });

  it('stores a null description when omitted', async () => {
    const response = await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'tasktracker', display_name: 'Task Tracker' },
    });
    expect(response.status).toBe(201);
    const payload = (await response.json()) as AppBody;
    expect(payload.description).toBeNull();
  });

  it('rejects a duplicate app_id with 409', async () => {
    await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'dupe', display_name: 'First' },
    });
    const response = await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'dupe', display_name: 'Second' },
    });
    expect(response.status).toBe(409);
    expect(await response.json()).toEqual({ error: 'app already exists' });
  });

  it('rejects an invalid app_id with 400', async () => {
    const response = await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'Has-Hyphens', display_name: 'Nope' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid app_id' });
  });

  it('rejects the reserved app_id "everyone" with 400', async () => {
    const response = await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'everyone', display_name: 'Nope' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid app_id' });
  });

  it('rejects a missing or empty display_name with 400', async () => {
    const response = await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'nodisplay', display_name: '   ' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid display_name' });
  });

  it('rejects invalid JSON bodies with 400', async () => {
    const response = await SELF.fetch('http://person-service.local/apps', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{not valid json',
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid JSON body' });
  });
});

describe('GET /apps', () => {
  it('returns an empty list when the table is empty', async () => {
    const response = await SELF.fetch('http://person-service.local/apps');
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ apps: [] });
  });

  it('returns all apps ordered by created_at ASC, app_id ASC', async () => {
    await fetchJson({ path: '/apps', method: 'POST', body: { app_id: 'aaa', display_name: 'A' } });
    await fetchJson({ path: '/apps', method: 'POST', body: { app_id: 'zzz', display_name: 'Z' } });

    const response = await SELF.fetch('http://person-service.local/apps');
    expect(response.status).toBe(200);
    const payload = (await response.json()) as { apps: AppBody[] };
    expect(payload.apps).toHaveLength(2);
    expect(payload.apps[0]?.app_id).toBe('aaa');
    expect(payload.apps[1]?.app_id).toBe('zzz');
  });
});

describe('PATCH /apps/{app_id}', () => {
  it('updates display_name and description and returns 200 with the updated row', async () => {
    await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'editme', display_name: 'Old', description: 'old desc' },
    });

    const response = await fetchJson({
      path: '/apps/editme',
      method: 'PATCH',
      body: { display_name: 'New', description: 'new desc' },
    });
    expect(response.status).toBe(200);
    const payload = (await response.json()) as AppBody;
    expect(payload.display_name).toBe('New');
    expect(payload.description).toBe('new desc');
  });

  it('leaves fields unchanged when omitted', async () => {
    await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'partial', display_name: 'Keep', description: 'keep desc' },
    });

    const response = await fetchJson({
      path: '/apps/partial',
      method: 'PATCH',
      body: { display_name: 'Changed' },
    });
    expect(response.status).toBe(200);
    const payload = (await response.json()) as AppBody;
    expect(payload.display_name).toBe('Changed');
    expect(payload.description).toBe('keep desc');
  });

  it('clears the description when explicitly set to null', async () => {
    await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'clearme', display_name: 'Name', description: 'to be cleared' },
    });

    const response = await fetchJson({
      path: '/apps/clearme',
      method: 'PATCH',
      body: { description: null },
    });
    expect(response.status).toBe(200);
    const payload = (await response.json()) as AppBody;
    expect(payload.description).toBeNull();
  });

  it('returns 404 for an unknown app_id', async () => {
    const response = await fetchJson({
      path: '/apps/ghost',
      method: 'PATCH',
      body: { display_name: 'Nope' },
    });
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: 'app not found' });
  });

  it('rejects an empty display_name with 400', async () => {
    await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'emptyname', display_name: 'Name' },
    });
    const response = await fetchJson({
      path: '/apps/emptyname',
      method: 'PATCH',
      body: { display_name: '  ' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid display_name' });
  });

  it('rejects an invalid app_id in the URL with 400', async () => {
    const response = await fetchJson({
      path: '/apps/Bad-Id',
      method: 'PATCH',
      body: { display_name: 'Nope' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid app_id' });
  });

  it('treats a valid-JSON-but-non-object body as a no-op edit (no crash)', async () => {
    await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'scalarbody', display_name: 'Keep', description: 'keep desc' },
    });

    const response = await SELF.fetch('http://person-service.local/apps/scalarbody', {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: '42',
    });
    expect(response.status).toBe(200);
    const payload = (await response.json()) as AppBody;
    expect(payload.display_name).toBe('Keep');
    expect(payload.description).toBe('keep desc');
  });
});

describe('DELETE /apps/{app_id}', () => {
  it('removes an app and returns 200', async () => {
    await fetchJson({
      path: '/apps',
      method: 'POST',
      body: { app_id: 'removeme', display_name: 'Bye' },
    });
    const response = await fetchJson({ path: '/apps/removeme', method: 'DELETE' });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ app_id: 'removeme', deleted: true });

    const list = (await (await SELF.fetch('http://person-service.local/apps')).json()) as {
      apps: AppBody[];
    };
    expect(list.apps).toHaveLength(0);
  });

  it('is idempotent: deleting an absent app still returns 200', async () => {
    const response = await fetchJson({ path: '/apps/neverexisted', method: 'DELETE' });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ app_id: 'neverexisted', deleted: true });
  });

  it('rejects an invalid app_id with 400', async () => {
    const response = await fetchJson({ path: '/apps/Bad-Id', method: 'DELETE' });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid app_id' });
  });
});

describe('/apps routing', () => {
  it('returns 405 for /apps with an unsupported method', async () => {
    const response = await SELF.fetch('http://person-service.local/apps', { method: 'DELETE' });
    expect(response.status).toBe(405);
    expect(await response.json()).toEqual({ error: 'method not allowed' });
  });

  it('returns 405 for /apps/{id} with an unsupported method', async () => {
    const response = await SELF.fetch('http://person-service.local/apps/something', {
      method: 'GET',
    });
    expect(response.status).toBe(405);
    expect(await response.json()).toEqual({ error: 'method not allowed' });
  });
});

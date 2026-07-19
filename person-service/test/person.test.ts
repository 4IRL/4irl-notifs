import { env, SELF } from 'cloudflare:test';
import { beforeEach, describe, expect, it } from 'vitest';

import { isValidEmail } from '../src/index';

// @cloudflare/vitest-pool-workers storage isolation is scoped per test FILE,
// not per individual test (the older per-test `isolatedStorage`/`singleWorker`
// options were removed when the package moved to the Vitest 4 plugin API; see
// https://developers.cloudflare.com/workers/testing/vitest-integration/isolation-and-concurrency/).
// D1 rows written by one `it()` block are otherwise still visible to the
// next, so clear the table explicitly before every test.
beforeEach(async () => {
  await env.DB.exec('DELETE FROM person');
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

describe('PUT /person', () => {
  it('inserts a new person and returns 200 with created_at set', async () => {
    const response = await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: 'abcdefgh23456777', email: 'Person@Example.com' },
    });

    expect(response.status).toBe(200);
    expect(response.headers.get('Content-Type')).toBe('application/json');
    const payload = (await response.json()) as { person_hash: string; email: string; created_at: string };
    expect(payload.person_hash).toBe('abcdefgh23456777');
    expect(payload.email).toBe('person@example.com');
    expect(payload.created_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
  });

  it('upsert is idempotent: a second PUT with the same hash preserves created_at while updating the email', async () => {
    const hash = 'upsertaaaa234567';
    const insertResponse = await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: hash, email: 'original@example.com' },
    });
    expect(insertResponse.status).toBe(200);
    const inserted = (await insertResponse.json()) as { created_at: string };

    const updateResponse = await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: hash, email: '  Updated@Example.com  ' },
    });
    expect(updateResponse.status).toBe(200);
    const updated = (await updateResponse.json()) as { email: string; created_at: string; person_hash: string };
    expect(updated.person_hash).toBe(hash);
    expect(updated.email).toBe('updated@example.com');
    expect(updated.created_at).toBe(inserted.created_at);
  });

  it('rejects invalid JSON bodies with 400', async () => {
    const response = await SELF.fetch('http://person-service.local/person', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: '{not valid json',
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid JSON body' });
  });

  it('rejects a person_hash that does not match ^[a-z2-7]{16}$', async () => {
    const response = await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: 'TOO-SHORT', email: 'valid@example.com' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid person_hash' });
  });

  it('rejects a person_hash containing digits outside 2-7 (e.g. 0, 1, 8, 9)', async () => {
    const response = await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: 'abcdefgh01234567', email: 'valid@example.com' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid person_hash' });
  });

  it('rejects an invalid email with 400', async () => {
    const response = await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: 'validhash234567a', email: 'not-an-email' },
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid email' });
  });
});

describe('GET /person', () => {
  it('looks up a person by normalized email (case-insensitive, trimmed)', async () => {
    const hash = 'lookupaaaa234567';
    await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: hash, email: 'findme@example.com' },
    });

    const response = await SELF.fetch(
      `http://person-service.local/person?email=${encodeURIComponent('  FindMe@Example.com  ')}`,
    );
    expect(response.status).toBe(200);
    const payload = (await response.json()) as { person_hash: string; email: string };
    expect(payload.person_hash).toBe(hash);
    expect(payload.email).toBe('findme@example.com');
  });

  it('returns 400 when the email query param is missing', async () => {
    const response = await SELF.fetch('http://person-service.local/person');
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid email' });
  });

  it('returns 400 when the email query param is invalid', async () => {
    const response = await SELF.fetch('http://person-service.local/person?email=not-an-email');
    expect(response.status).toBe(400);
    expect(await response.json()).toEqual({ error: 'invalid email' });
  });

  it('returns 404 when no person matches the email', async () => {
    const response = await SELF.fetch(
      'http://person-service.local/person?email=nobody-here@example.com',
    );
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: 'person not found' });
  });
});

describe('GET /people', () => {
  it('returns an empty list when the table is empty', async () => {
    const response = await SELF.fetch('http://person-service.local/people');
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({ people: [] });
  });

  it('returns all people ordered by created_at ASC, person_hash ASC', async () => {
    // Insert in alphabetical hash order so the assertion holds regardless of
    // whether the two inserts land in the same millisecond (created_at tie) —
    // person_hash ASC as the secondary key agrees with insertion order either
    // way, keeping this deterministic instead of timing-dependent.
    await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: 'aaaaaaaaaa234567', email: 'a@example.com' },
    });
    await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: 'zzzzzzzzzz234567', email: 'z@example.com' },
    });

    const response = await SELF.fetch('http://person-service.local/people');
    expect(response.status).toBe(200);
    const payload = (await response.json()) as {
      people: Array<{ person_hash: string; email: string; created_at: string }>;
    };
    expect(payload.people).toHaveLength(2);
    expect(payload.people[0]?.person_hash).toBe('aaaaaaaaaa234567');
    expect(payload.people[1]?.person_hash).toBe('zzzzzzzzzz234567');
  });
});

describe('routing', () => {
  it('returns 404 for an unknown path', async () => {
    const response = await SELF.fetch('http://person-service.local/unknown');
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({ error: 'not found' });
  });

  it('returns 405 for a known path with the wrong method', async () => {
    const response = await SELF.fetch('http://person-service.local/person', { method: 'DELETE' });
    expect(response.status).toBe(405);
    expect(await response.json()).toEqual({ error: 'method not allowed' });
  });

  it('returns 405 for /people with a non-GET method', async () => {
    const response = await SELF.fetch('http://person-service.local/people', { method: 'POST' });
    expect(response.status).toBe(405);
    expect(await response.json()).toEqual({ error: 'method not allowed' });
  });
});

describe('isValidEmail', () => {
  it('accepts a plain valid email', () => {
    expect(isValidEmail('a@b.com')).toBe(true);
  });

  it('is case-insensitive by design (validity does not depend on case)', () => {
    expect(isValidEmail('A@B.COM')).toBe(true);
  });

  it('rejects an empty string', () => {
    expect(isValidEmail('')).toBe(false);
  });

  it('rejects a missing @', () => {
    expect(isValidEmail('nobody-example.com')).toBe(false);
  });

  it('rejects an empty local part', () => {
    expect(isValidEmail('@example.com')).toBe(false);
  });

  it('rejects an empty domain part', () => {
    expect(isValidEmail('nobody@')).toBe(false);
  });

  it('rejects whitespace inside the address', () => {
    expect(isValidEmail('no body@example.com')).toBe(false);
  });

  it('rejects an address over 254 characters', () => {
    const longLocal = 'a'.repeat(250);
    expect(isValidEmail(`${longLocal}@b.com`)).toBe(false);
  });

  it('accepts an address at exactly 254 characters', () => {
    // local (243) + "@" (1) + "b.com" (5) = 249; pad to exactly 254.
    const local = 'a'.repeat(248);
    const address = `${local}@b.com`;
    expect(address).toHaveLength(254);
    expect(isValidEmail(address)).toBe(true);
  });

  it('does not require a dot in the domain', () => {
    expect(isValidEmail('user@localhost')).toBe(true);
  });
});

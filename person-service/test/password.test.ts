import { env, SELF } from 'cloudflare:test';
import { beforeEach, describe, expect, it } from 'vitest';

// Storage is isolated per test FILE, not per test (see person.test.ts), so
// clear both tables before every test.
beforeEach(async () => {
  await env.DB.exec('DELETE FROM person');
  await env.DB.exec('DELETE FROM password_reset');
});

const HASH = 'claimaaaa2345677';
const OTHER_HASH = 'claimbbbb2345677';
const EMAIL = 'claim@example.com';
const HOUR_MS = 3_600_000;

interface ClaimBody {
  claimed: boolean;
  row_exists: boolean;
  password_set_at: string | null;
  previous_set_at: string | null;
}

/** Fetches from the running Worker with an optional JSON body. */
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

async function claim({
  mode,
  hash = HASH,
  email = EMAIL,
}: {
  mode: string;
  hash?: string;
  email?: string;
}): Promise<{ status: number; body: ClaimBody }> {
  const response = await fetchJson({
    path: `/person/${hash}/password-claim`,
    method: 'POST',
    body: { email, mode },
  });
  return { status: response.status, body: (await response.json()) as ClaimBody };
}

async function release({
  passwordSetAt,
  restoreTo,
  hash = HASH,
}: {
  passwordSetAt: string;
  restoreTo: string | null;
  hash?: string;
}): Promise<{ status: number; body: { released: boolean } }> {
  const response = await fetchJson({
    path: `/person/${hash}/password-claim`,
    method: 'DELETE',
    body: { password_set_at: passwordSetAt, restore_to: restoreTo },
  });
  return { status: response.status, body: (await response.json()) as { released: boolean } };
}

async function storedStamp(hash = HASH): Promise<string | null | undefined> {
  const row = await env.DB.prepare('SELECT password_set_at FROM person WHERE person_hash = ?1')
    .bind(hash)
    .first<{ password_set_at: string | null }>();
  return row === null ? undefined : row.password_set_at;
}

async function claimSlot(hash = HASH): Promise<Response> {
  return fetchJson({ path: `/person/${hash}/password-resets`, method: 'POST' });
}

async function backdateAllResets({ hash = HASH, toMsAgo }: { hash?: string; toMsAgo: number }): Promise<void> {
  await env.DB.prepare('UPDATE password_reset SET reset_at = ?2 WHERE person_hash = ?1')
    .bind(hash, Date.now() - toMsAgo)
    .run();
}

describe('POST /person/{hash}/password-claim', () => {
  it('if_unset on a missing row creates and stamps it', async () => {
    const { status, body } = await claim({ mode: 'if_unset' });
    expect(status).toBe(200);
    expect(body.claimed).toBe(true);
    expect(body.row_exists).toBe(true);
    expect(body.previous_set_at).toBeNull();
    expect(body.password_set_at).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    expect(await storedStamp()).toBe(body.password_set_at);
  });

  it('if_unset on an existing unset row claims it with previous_set_at null', async () => {
    await fetchJson({ path: '/person', method: 'PUT', body: { person_hash: HASH, email: EMAIL } });
    const { body } = await claim({ mode: 'if_unset' });
    expect(body.claimed).toBe(true);
    expect(body.previous_set_at).toBeNull();
    expect(await storedStamp()).toBe(body.password_set_at);
  });

  it('if_unset on a set row returns claimed:false and leaves the stamp untouched', async () => {
    const first = await claim({ mode: 'if_unset' });
    const second = await claim({ mode: 'if_unset' });
    expect(second.body.claimed).toBe(false);
    expect(second.body.row_exists).toBe(true);
    expect(second.body.password_set_at).toBe(first.body.password_set_at);
    expect(second.body.previous_set_at).toBeNull();
    expect(await storedStamp()).toBe(first.body.password_set_at);
  });

  it('always on a missing row creates and stamps it', async () => {
    const { body } = await claim({ mode: 'always' });
    expect(body.claimed).toBe(true);
    expect(body.previous_set_at).toBeNull();
    expect(await storedStamp()).toBe(body.password_set_at);
  });

  it('always overwrites a set stamp and returns the replaced one as previous_set_at', async () => {
    await fetchJson({ path: '/person', method: 'PUT', body: { person_hash: HASH, email: EMAIL } });
    await env.DB.prepare('UPDATE person SET password_set_at = ?2 WHERE person_hash = ?1')
      .bind(HASH, '2020-01-01T00:00:00.000Z')
      .run();
    const { body } = await claim({ mode: 'always' });
    expect(body.claimed).toBe(true);
    expect(body.previous_set_at).toBe('2020-01-01T00:00:00.000Z');
    expect(body.password_set_at).not.toBe('2020-01-01T00:00:00.000Z');
    expect(await storedStamp()).toBe(body.password_set_at);
  });

  it('update_only on a missing row creates no row', async () => {
    const { status, body } = await claim({ mode: 'update_only' });
    expect(status).toBe(200);
    expect(body).toEqual({ claimed: false, row_exists: false, password_set_at: null, previous_set_at: null });
    expect(await storedStamp()).toBeUndefined();
  });

  it('update_only on an existing row overwrites the stamp', async () => {
    await fetchJson({ path: '/person', method: 'PUT', body: { person_hash: HASH, email: EMAIL } });
    await env.DB.prepare('UPDATE person SET password_set_at = ?2 WHERE person_hash = ?1')
      .bind(HASH, '2020-01-01T00:00:00.000Z')
      .run();
    const { body } = await claim({ mode: 'update_only' });
    expect(body.claimed).toBe(true);
    expect(body.row_exists).toBe(true);
    expect(body.previous_set_at).toBe('2020-01-01T00:00:00.000Z');
    expect(await storedStamp()).toBe(body.password_set_at);
  });

  it('N parallel if_unset claims on an unset person give exactly one claimed:true', async () => {
    const results = await Promise.all(Array.from({ length: 10 }, () => claim({ mode: 'if_unset' })));
    expect(results.filter((result) => result.body.claimed)).toHaveLength(1);
    expect(results.every((result) => result.status === 200)).toBe(true);
  });

  it('rejects an invalid mode, email and hash with 400', async () => {
    const badMode = await fetchJson({
      path: `/person/${HASH}/password-claim`,
      method: 'POST',
      body: { email: EMAIL, mode: 'nope' },
    });
    expect(badMode.status).toBe(400);
    expect(await badMode.json()).toEqual({ error: 'invalid mode' });

    const badEmail = await fetchJson({
      path: `/person/${HASH}/password-claim`,
      method: 'POST',
      body: { email: 'not-an-email', mode: 'always' },
    });
    expect(badEmail.status).toBe(400);
    expect(await badEmail.json()).toEqual({ error: 'invalid email' });

    const badHash = await fetchJson({
      path: '/person/TOO-SHORT/password-claim',
      method: 'POST',
      body: { email: EMAIL, mode: 'always' },
    });
    expect(badHash.status).toBe(400);
    expect(await badHash.json()).toEqual({ error: 'invalid person_hash' });

    const badJson = await SELF.fetch(`http://person-service.local/person/${HASH}/password-claim`, {
      method: 'POST',
      body: 'not json',
    });
    expect(badJson.status).toBe(400);
  });

  it('stores a valid client-supplied stamp and echoes it', async () => {
    const stamp = '2026-10-22T10:00:00.123Z';
    const response = await fetchJson({
      path: `/person/${HASH}/password-claim`,
      method: 'POST',
      body: { email: EMAIL, mode: 'if_unset', password_set_at: stamp },
    });
    expect(response.status).toBe(200);
    const body = (await response.json()) as ClaimBody;
    expect(body.claimed).toBe(true);
    expect(body.password_set_at).toBe(stamp);
    expect(await storedStamp()).toBe(stamp);
  });

  it('uses a client-supplied stamp for always and update_only too', async () => {
    await claim({ mode: 'if_unset' });
    const stamp = '2030-01-02T03:04:05.006Z';
    const response = await fetchJson({
      path: `/person/${HASH}/password-claim`,
      method: 'POST',
      body: { email: EMAIL, mode: 'update_only', password_set_at: stamp },
    });
    expect(((await response.json()) as ClaimBody).password_set_at).toBe(stamp);
    expect(await storedStamp()).toBe(stamp);
  });

  it('rejects a malformed client-supplied stamp with 400 and writes nothing', async () => {
    const malformed: unknown[] = [
      '',
      'not-a-date',
      '2026-10-22',
      '2026-10-22T10:00:00Z',
      '2026-10-22T10:00:00.123+00:00',
      '2026-13-45T10:00:00.123Z',
      '2026-02-30T10:00:00.123Z',
      ' 2026-10-22T10:00:00.123Z',
      1760000000000,
      null,
      {},
    ];
    for (const badStamp of malformed) {
      const response = await fetchJson({
        path: `/person/${HASH}/password-claim`,
        method: 'POST',
        body: { email: EMAIL, mode: 'if_unset', password_set_at: badStamp },
      });
      expect(response.status, JSON.stringify(badStamp)).toBe(400);
      expect(await response.json()).toEqual({ error: 'invalid password_set_at' });
    }
    expect(await storedStamp()).toBeUndefined();
  });

  it('falls back to the Worker clock when no stamp is supplied', async () => {
    const before = Date.now();
    const { body } = await claim({ mode: 'if_unset' });
    const stamp = Date.parse(body.password_set_at as string);
    expect(stamp).toBeGreaterThanOrEqual(before);
    expect(stamp).toBeLessThanOrEqual(Date.now());
  });

  it('returns 405 for other methods', async () => {
    const response = await fetchJson({ path: `/person/${HASH}/password-claim`, method: 'GET' });
    expect(response.status).toBe(405);
  });
});

describe('DELETE /person/{hash}/password-claim', () => {
  it('restores restore_to when the stamp still matches', async () => {
    await fetchJson({ path: '/person', method: 'PUT', body: { person_hash: HASH, email: EMAIL } });
    await env.DB.prepare('UPDATE person SET password_set_at = ?2 WHERE person_hash = ?1')
      .bind(HASH, '2020-01-01T00:00:00.000Z')
      .run();
    const { body: claimed } = await claim({ mode: 'always' });

    const { status, body } = await release({
      passwordSetAt: claimed.password_set_at as string,
      restoreTo: claimed.previous_set_at,
    });
    expect(status).toBe(200);
    expect(body).toEqual({ released: true });
    expect(await storedStamp()).toBe('2020-01-01T00:00:00.000Z');
  });

  it('restore_to null clears the stamp', async () => {
    const { body: claimed } = await claim({ mode: 'if_unset' });
    const { body } = await release({ passwordSetAt: claimed.password_set_at as string, restoreTo: null });
    expect(body.released).toBe(true);
    expect(await storedStamp()).toBeNull();
  });

  it('a stale stamp leaves the newer one in place', async () => {
    const { body: first } = await claim({ mode: 'always' });
    await env.DB.prepare('UPDATE person SET password_set_at = ?2 WHERE person_hash = ?1')
      .bind(HASH, '2030-01-01T00:00:00.000Z')
      .run();

    const { body } = await release({ passwordSetAt: first.password_set_at as string, restoreTo: null });
    expect(body.released).toBe(false);
    expect(await storedStamp()).toBe('2030-01-01T00:00:00.000Z');
  });

  it('is a no-op on a missing row', async () => {
    const { status, body } = await release({ passwordSetAt: '2020-01-01T00:00:00.000Z', restoreTo: null });
    expect(status).toBe(200);
    expect(body.released).toBe(false);
    expect(await storedStamp()).toBeUndefined();
  });

  it('rejects an invalid body with 400', async () => {
    const missingStamp = await fetchJson({
      path: `/person/${HASH}/password-claim`,
      method: 'DELETE',
      body: { restore_to: null },
    });
    expect(missingStamp.status).toBe(400);

    const badRestore = await fetchJson({
      path: `/person/${HASH}/password-claim`,
      method: 'DELETE',
      body: { password_set_at: '2020-01-01T00:00:00.000Z', restore_to: 5 },
    });
    expect(badRestore.status).toBe(400);

    const badHash = await fetchJson({
      path: '/person/TOO-SHORT/password-claim',
      method: 'DELETE',
      body: { password_set_at: '2020-01-01T00:00:00.000Z', restore_to: null },
    });
    expect(badHash.status).toBe(400);
  });
});

describe('POST /person/{hash}/password-resets', () => {
  it('allows 5 slots then returns 429 with retry_after_seconds near the oldest slot expiry', async () => {
    for (let index = 0; index < 5; index += 1) {
      const response = await claimSlot();
      expect(response.status).toBe(200);
      expect(((await response.json()) as { reset_id: string }).reset_id).toMatch(/^[0-9a-f-]{36}$/);
    }
    // Leave about 1,200 s of the window.
    await backdateAllResets({ toMsAgo: HOUR_MS - 1_200_000 });

    const limited = await claimSlot();
    expect(limited.status).toBe(429);
    const payload = (await limited.json()) as { error: string; retry_after_seconds: number };
    expect(payload.error).toBe('reset limit reached');
    expect(Math.abs(payload.retry_after_seconds - 1200)).toBeLessThanOrEqual(1);
  });

  it('returns at least 1 when the oldest slot has about 500 ms left', async () => {
    for (let index = 0; index < 5; index += 1) {
      await claimSlot();
    }
    await backdateAllResets({ toMsAgo: HOUR_MS - 60_000 });
    // Make one slot expire in well under a second of remaining time.
    await env.DB.prepare('UPDATE password_reset SET reset_at = ?2 WHERE person_hash = ?1 AND reset_id = (SELECT reset_id FROM password_reset WHERE person_hash = ?1 LIMIT 1)')
      .bind(HASH, Date.now() - HOUR_MS + 500)
      .run();
    const limited = await claimSlot();
    expect(limited.status).toBe(429);
    expect(((await limited.json()) as { retry_after_seconds: number }).retry_after_seconds).toBe(1);
  });

  it('succeeds again once the slots are older than an hour and prunes them', async () => {
    for (let index = 0; index < 5; index += 1) {
      await claimSlot();
    }
    await backdateAllResets({ toMsAgo: HOUR_MS + 1_000 });

    const response = await claimSlot();
    expect(response.status).toBe(200);
    const count = await env.DB.prepare('SELECT COUNT(*) AS total FROM password_reset WHERE person_hash = ?1')
      .bind(HASH)
      .first<{ total: number }>();
    expect(count?.total).toBe(1);
  });

  it('counts each person separately', async () => {
    for (let index = 0; index < 5; index += 1) {
      await claimSlot();
    }
    expect((await claimSlot()).status).toBe(429);
    expect((await claimSlot(OTHER_HASH)).status).toBe(200);
  });

  it('N parallel claims for one hash give exactly 5 x 200 and the rest 429', async () => {
    const responses = await Promise.all(Array.from({ length: 12 }, () => claimSlot()));
    const statuses = responses.map((response) => response.status);
    expect(statuses.filter((status) => status === 200)).toHaveLength(5);
    expect(statuses.filter((status) => status === 429)).toHaveLength(7);
  });

  it('rejects an invalid hash with 400 and a wrong method with 405', async () => {
    const badHash = await fetchJson({ path: '/person/TOO-SHORT/password-resets', method: 'POST' });
    expect(badHash.status).toBe(400);
    const wrongMethod = await fetchJson({ path: `/person/${HASH}/password-resets`, method: 'GET' });
    expect(wrongMethod.status).toBe(405);
  });
});

describe('DELETE /person/{hash}/password-resets/{reset_id}', () => {
  it('frees capacity and is idempotent', async () => {
    const resetIds: string[] = [];
    for (let index = 0; index < 5; index += 1) {
      resetIds.push(((await (await claimSlot()).json()) as { reset_id: string }).reset_id);
    }
    expect((await claimSlot()).status).toBe(429);

    const first = await fetchJson({ path: `/person/${HASH}/password-resets/${resetIds[0]}`, method: 'DELETE' });
    expect(first.status).toBe(200);
    expect(await first.json()).toEqual({ released: true });
    expect((await claimSlot()).status).toBe(200);

    const second = await fetchJson({ path: `/person/${HASH}/password-resets/${resetIds[0]}`, method: 'DELETE' });
    expect(second.status).toBe(200);
    expect(await second.json()).toEqual({ released: false });
  });

  it('does not release another person\'s slot', async () => {
    const { reset_id: resetId } = (await (await claimSlot()).json()) as { reset_id: string };
    const response = await fetchJson({ path: `/person/${OTHER_HASH}/password-resets/${resetId}`, method: 'DELETE' });
    expect(await response.json()).toEqual({ released: false });
  });

  it('rejects an invalid hash with 400', async () => {
    const response = await fetchJson({ path: '/person/TOO-SHORT/password-resets/abc', method: 'DELETE' });
    expect(response.status).toBe(400);
  });

  it('rejects a wrong method on the reset-slot route with 405', async () => {
    const response = await fetchJson({ path: `/person/${HASH}/password-resets/abc`, method: 'GET' });
    expect(response.status).toBe(405);
  });

  it('returns 404 for an extra path segment after the reset id', async () => {
    const response = await fetchJson({ path: `/person/${HASH}/password-resets/abc/extra`, method: 'POST' });
    expect(response.status).toBe(404);
  });
});

describe('interaction with the rest of the person API', () => {
  it('PUT /person re-upsert keeps password_set_at', async () => {
    const { body } = await claim({ mode: 'if_unset' });
    const putResponse = await fetchJson({
      path: '/person',
      method: 'PUT',
      body: { person_hash: HASH, email: 'changed@example.com' },
    });
    expect(putResponse.status).toBe(200);
    expect(await storedStamp()).toBe(body.password_set_at);
  });

  it('GET /people and GET /person do not expose password_set_at', async () => {
    await claim({ mode: 'if_unset' });
    const people = (await (await SELF.fetch('http://person-service.local/people')).json()) as {
      people: Record<string, unknown>[];
    };
    expect(people.people).toHaveLength(1);
    expect(Object.keys(people.people[0] ?? {}).sort()).toEqual(['created_at', 'email', 'person_hash']);

    const person = (await (
      await SELF.fetch(`http://person-service.local/person?email=${EMAIL}`)
    ).json()) as Record<string, unknown>;
    expect(Object.keys(person).sort()).toEqual(['created_at', 'email', 'person_hash']);
  });

  it('deleting a person removes their reset rows and the stamp, but not another person\'s', async () => {
    await claim({ mode: 'if_unset' });
    await claimSlot();
    await claimSlot(OTHER_HASH);

    const response = await fetchJson({ path: `/person/${HASH}`, method: 'DELETE' });
    expect(response.status).toBe(200);

    expect(await storedStamp()).toBeUndefined();
    const mine = await env.DB.prepare('SELECT COUNT(*) AS total FROM password_reset WHERE person_hash = ?1')
      .bind(HASH)
      .first<{ total: number }>();
    const theirs = await env.DB.prepare('SELECT COUNT(*) AS total FROM password_reset WHERE person_hash = ?1')
      .bind(OTHER_HASH)
      .first<{ total: number }>();
    expect(mine?.total).toBe(0);
    expect(theirs?.total).toBe(1);
  });

  it('returns 404 for an unknown person sub-resource', async () => {
    const response = await fetchJson({ path: `/person/${HASH}/unknown`, method: 'POST' });
    expect(response.status).toBe(404);
  });
});

// Typed client for the person-service HTTP contract (Cloudflare Worker
// "person-service"). The wire shape is snake_case JSON; this module maps it
// to camelCase TypeScript at the boundary so components never touch
// snake_case, mirroring client.ts's provisioning-api client.

import { ApiError } from './client';

/** A provisioned person as recorded in the reverse-index. */
export interface PersonSummary {
  personHash: string;
  email: string;
  createdAt: string;
}

/** Configuration for createPersonApiClient. */
export interface PersonApiClientConfig {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
}

/** Wire shape of the people list response. */
interface PeopleResponseWire {
  people: Array<{ person_hash: string; email: string; created_at: string }>;
}

/** The person-service API surface consumed by the admin UI. */
export interface PersonApiClient {
  listPeople(): Promise<PersonSummary[]>;
}

const DEFAULT_BASE_URL = '';

/** Builds a PersonApiClient bound to a base URL and fetch implementation. */
export function createPersonApiClient({
  baseUrl = DEFAULT_BASE_URL,
  fetchImpl = fetch,
}: PersonApiClientConfig = {}): PersonApiClient {
  const trimmedBaseUrl = baseUrl.replace(/\/+$/, '');

  async function request({ path, method }: { path: string; method: string }): Promise<unknown> {
    const response = await fetchImpl(`${trimmedBaseUrl}${path}`, {
      method,
      // The admin UI calls the person service same-origin: in production via a
      // Cloudflare Pages Function that proxies `/people` to the person service
      // server-side (default empty baseUrl → relative `/people`); locally the
      // request stays on the dev origin. `credentials: 'include'` sends the
      // admin app's own Access session cookie to the same-origin Function.
      credentials: 'include',
    });

    const payload = (await response.json().catch(() => null)) as { error?: string } | null;
    if (!response.ok) {
      const message =
        payload && typeof payload.error === 'string'
          ? payload.error
          : `request failed (${response.status})`;
      throw new ApiError({ status: response.status, message });
    }
    return payload;
  }

  return {
    async listPeople(): Promise<PersonSummary[]> {
      const wire = (await request({ path: '/people', method: 'GET' })) as PeopleResponseWire;
      return wire.people.map((person) => ({
        personHash: person.person_hash,
        email: person.email,
        createdAt: person.created_at,
      }));
    },
  };
}

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

/** A registered app as recorded in the app registry. */
export interface AppSummary {
  appId: string;
  displayName: string;
  description: string | null;
  createdAt: string;
}

/** Parameters for registering a new app. */
export interface CreateAppParams {
  appId: string;
  displayName: string;
  description?: string | null;
}

/** Parameters for editing an app's metadata. An omitted field is left
 *  unchanged; `description: null` explicitly clears it. app_id is immutable. */
export interface UpdateAppParams {
  appId: string;
  displayName?: string;
  description?: string | null;
}

/** Identifies an app for deletion. */
export interface AppIdParam {
  appId: string;
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

/** Wire shape of an app row (snake_case). */
interface AppWire {
  app_id: string;
  display_name: string;
  description: string | null;
  created_at: string;
}

/** Wire shape of the apps list response. */
interface AppsResponseWire {
  apps: AppWire[];
}

/** Maps a snake_case app wire row to the camelCase AppSummary. */
function mapApp(wire: AppWire): AppSummary {
  return {
    appId: wire.app_id,
    displayName: wire.display_name,
    description: wire.description,
    createdAt: wire.created_at,
  };
}

/** The person-service API surface consumed by the admin UI. */
export interface PersonApiClient {
  listPeople(): Promise<PersonSummary[]>;
  listApps(): Promise<AppSummary[]>;
  createApp(params: CreateAppParams): Promise<AppSummary>;
  updateApp(params: UpdateAppParams): Promise<AppSummary>;
  deleteApp(param: AppIdParam): Promise<void>;
}

const DEFAULT_BASE_URL = '';

/** Builds a PersonApiClient bound to a base URL and fetch implementation. */
export function createPersonApiClient({
  baseUrl = DEFAULT_BASE_URL,
  fetchImpl = fetch,
}: PersonApiClientConfig = {}): PersonApiClient {
  const trimmedBaseUrl = baseUrl.replace(/\/+$/, '');

  async function request({
    path,
    method,
    body,
  }: {
    path: string;
    method: string;
    body?: unknown;
  }): Promise<unknown> {
    const response = await fetchImpl(`${trimmedBaseUrl}${path}`, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      // The admin UI calls the person service same-origin: in production via
      // Cloudflare Pages Functions that proxy `/people` and `/apps` to the
      // person service server-side (default empty baseUrl → relative path);
      // locally the request stays on the dev origin. `credentials: 'include'`
      // sends the admin app's own Access session cookie to the same-origin
      // Function.
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

    async listApps(): Promise<AppSummary[]> {
      const wire = (await request({ path: '/apps', method: 'GET' })) as AppsResponseWire;
      return wire.apps.map(mapApp);
    },

    async createApp({ appId, displayName, description }: CreateAppParams): Promise<AppSummary> {
      const wire = (await request({
        path: '/apps',
        method: 'POST',
        body: { app_id: appId, display_name: displayName, description },
      })) as AppWire;
      return mapApp(wire);
    },

    async updateApp({ appId, displayName, description }: UpdateAppParams): Promise<AppSummary> {
      // Only include fields the caller actually set: an omitted key leaves that
      // field unchanged server-side, while `description: null` passes through to
      // clear it. app_id is never sent in the body (it is immutable).
      const body: Record<string, unknown> = {};
      if (displayName !== undefined) {
        body.display_name = displayName;
      }
      if (description !== undefined) {
        body.description = description;
      }
      const wire = (await request({
        path: `/apps/${appId}`,
        method: 'PATCH',
        body,
      })) as AppWire;
      return mapApp(wire);
    },

    async deleteApp({ appId }: AppIdParam): Promise<void> {
      await request({ path: `/apps/${appId}`, method: 'DELETE' });
    },
  };
}

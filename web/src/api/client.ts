// Typed client for the provisioning-api HTTP contract
// (provisioning-api/internal/httpapi). The wire shape is snake_case Go JSON;
// this module maps it to camelCase TypeScript at the boundary so components
// never touch snake_case.

/** A provisioned user with the apps derived from their wildcard topic grants. */
export interface UserSummary {
  userId: string;
  apps: string[];
  topicPatterns: string[];
}

/** The result of a successful provision: the app-labeled ntfy access token. */
export interface ProvisionResult {
  userId: string;
  appId: string;
  topicPattern: string;
  token: string;
}

/** An app/user pair identifying a provision or deprovision target. */
export interface AppUserPair {
  appId: string;
  userId: string;
}

/** Identifies a user for deletion. */
export interface UserIdParam {
  userId: string;
}

/** Configuration for createApiClient. */
export interface ApiClientConfig {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
}

/** Error thrown for non-2xx API responses, carrying the HTTP status. */
export class ApiError extends Error {
  readonly status: number;

  constructor({ status, message }: { status: number; message: string }) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

/** Wire shape of the users list response. */
interface UsersResponseWire {
  users: Array<{ user_id: string; apps: string[]; topic_patterns: string[] }>;
}

/** Wire shape of a provision response. */
interface ProvisionResponseWire {
  user_id: string;
  app_id: string;
  topic_pattern: string;
  token: string;
}

const DEFAULT_BASE_URL = '';

/** The provisioning API surface consumed by the admin UI. */
export interface ApiClient {
  provision(pair: AppUserPair): Promise<ProvisionResult>;
  deprovision(pair: AppUserPair): Promise<void>;
  listUsers(): Promise<UserSummary[]>;
  deleteUser(param: UserIdParam): Promise<void>;
}

/** Builds an ApiClient bound to a base URL and fetch implementation. */
export function createApiClient({
  baseUrl = DEFAULT_BASE_URL,
  fetchImpl = fetch,
}: ApiClientConfig = {}): ApiClient {
  async function request({
    path,
    method,
    body,
  }: {
    path: string;
    method: string;
    body?: unknown;
  }): Promise<unknown> {
    const response = await fetchImpl(`${baseUrl}${path}`, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
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
    async provision({ appId, userId }: AppUserPair): Promise<ProvisionResult> {
      const wire = (await request({
        path: '/v1/provision',
        method: 'POST',
        body: { app_id: appId, user_id: userId },
      })) as ProvisionResponseWire;
      return {
        userId: wire.user_id,
        appId: wire.app_id,
        topicPattern: wire.topic_pattern,
        token: wire.token,
      };
    },

    async deprovision({ appId, userId }: AppUserPair): Promise<void> {
      await request({
        path: '/v1/deprovision',
        method: 'POST',
        body: { app_id: appId, user_id: userId },
      });
    },

    async listUsers(): Promise<UserSummary[]> {
      const wire = (await request({ path: '/v1/users', method: 'GET' })) as UsersResponseWire;
      return wire.users.map((user) => ({
        userId: user.user_id,
        apps: user.apps,
        topicPatterns: user.topic_patterns,
      }));
    },

    async deleteUser({ userId }: UserIdParam): Promise<void> {
      await request({ path: `/v1/users/${userId}`, method: 'DELETE' });
    },
  };
}

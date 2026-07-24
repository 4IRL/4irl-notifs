// Shared test helpers for the Pages Function proxy specs (`_proxy.test.ts`,
// `people.test.ts`, `v1/[[path]].test.ts`).

import type { Env } from './_proxy';

/** Builds a test `Env` with the standard proxy bindings, overridable per field. */
export function makeEnv(overrides: Partial<Env> = {}): Env {
  return {
    PROVISIONING_API_URL: 'https://notifs-api.4irl.app',
    PERSON_SERVICE_URL: 'https://notifs-people.4irl.app',
    PROXY_ACCESS_CLIENT_ID: 'id',
    PROXY_ACCESS_CLIENT_SECRET: 'sec',
    ACCESS_TEAM_DOMAIN: '',
    ACCESS_JWT_AUD: '',
    // Auth DISABLED by default so existing proxy-mechanics specs run without a
    // JWT; a test opts into enforcement by overriding DISABLE_ACCESS_AUTH to ''
    // (plus ACCESS_JWT_AUD + ACCESS_TEAM_DOMAIN).
    DISABLE_ACCESS_AUTH: 'true',
    ...overrides,
  };
}

/** Builds a JSON `Response` with the given status and body. */
export function jsonResponse({ status, body }: { status: number; body: unknown }): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** Invokes a Pages Function `onRequest` handler with a minimal test context. */
export function invoke({
  onRequest,
  request,
  env,
}: {
  onRequest: PagesFunction<Env>;
  request: Request;
  env: Env;
}): Response | Promise<Response> {
  return onRequest({ request, env } as Parameters<typeof onRequest>[0]);
}

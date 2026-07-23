// Shared HTTP response helpers for the admin UI's Pages Functions.
//
// Lives in its own module (imported by both `_proxy.ts` and `_auth.ts`) so the
// two can share `jsonError` without a runtime import cycle — `_proxy` imports
// `authenticateAdmin` from `_auth`, and `_auth`'s only remaining reference back
// to `_proxy` is the type-only `Env` import (erased at compile time).

/** Builds a JSON error response with the standard `{ error }` shape. */
export function jsonError({ status, error }: { status: number; error: string }): Response {
  return new Response(JSON.stringify({ error }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

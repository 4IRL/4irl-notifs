/// <reference types="@cloudflare/vitest-plugin/types" />
import type { D1Migration } from '@cloudflare/vitest-plugin';

// Augments the ambient `Cloudflare.Env` namespace (declared as an empty
// merge target by @cloudflare/workers-types) with the bindings this Worker's
// test environment provides. The `cloudflare:test` module types `env` as
// `Cloudflare.Env`, so this is the supported way to type it.
declare global {
  namespace Cloudflare {
    interface Env {
      DB: D1Database;
      TEST_MIGRATIONS: D1Migration[];
    }
  }
}

export {};

/// <reference types="@cloudflare/vitest-pool-workers/types" />
import type { D1Migration } from '@cloudflare/vitest-pool-workers';

// Augments the ambient `Cloudflare.Env` namespace (declared as an empty
// merge target by @cloudflare/workers-types) with the bindings this Worker's
// test environment provides. @cloudflare/vitest-pool-workers 0.18.6's
// `cloudflare:test` module types `env` as `Cloudflare.Env`, so this is the
// supported way to type it for this version (superseding the older
// `declare module "cloudflare:test" { interface ProvidedEnv { ... } }`
// pattern from earlier docs/versions).
declare global {
  namespace Cloudflare {
    interface Env {
      DB: D1Database;
      TEST_MIGRATIONS: D1Migration[];
    }
  }
}

export {};

/// <reference types="@cloudflare/vitest-pool-workers/types" />

// Augments the ambient `Cloudflare.Env` namespace so `env` from `cloudflare:test` exposes the
// KV binding declared in wrangler.toml (same pattern as person-service on 0.18.6). Secrets are
// not bindings in the test environment: each test builds its own env object around this KV.
declare global {
  namespace Cloudflare {
    interface Env {
      WATCHDOG_STATE: KVNamespace;
    }
  }
}

export {};

/// <reference types="@cloudflare/vitest-plugin/types" />

// Augments the ambient `Cloudflare.Env` namespace so `env` from `cloudflare:test` exposes the
// KV binding declared in wrangler.toml (the plugin's `Cloudflare.Env` augmentation pattern). Secrets are
// not bindings in the test environment: each test builds its own env object around this KV.
declare global {
  namespace Cloudflare {
    interface Env {
      WATCHDOG_STATE: KVNamespace;
    }
  }
}

export {};

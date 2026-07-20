import { cloudflareTest, readD1Migrations } from '@cloudflare/vitest-pool-workers';
import { defineConfig } from 'vitest/config';

// NOTE: @cloudflare/vitest-pool-workers 0.18.6 ships the newer "v4" plugin-based
// config API (`cloudflareTest` as a Vite plugin + `defineConfig` from
// `vitest/config`), not the `defineWorkersProject`/`@cloudflare/vitest-pool-workers/config`
// API from older docs/examples — that subpath no longer exists in this version's
// package.json "exports". This file uses the API that actually ships in the
// pinned version.
const migrations = await readD1Migrations('./migrations');

export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: './wrangler.toml' },
      miniflare: {
        bindings: { TEST_MIGRATIONS: migrations },
      },
    }),
  ],
  test: {
    setupFiles: ['./test/apply-migrations.ts'],
  },
});

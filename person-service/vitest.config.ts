import { cloudflareTest, readD1Migrations } from '@cloudflare/vitest-plugin';
import { defineConfig } from 'vitest/config';

// NOTE: @cloudflare/vitest-plugin (the renamed workers-pool package) ships the
// plugin-based config API: `cloudflareTest` as a Vite plugin with `defineConfig`
// from `vitest/config`. This file uses the API that actually ships.
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

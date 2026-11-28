import { cloudflareTest } from '@cloudflare/vitest-plugin';
import { defineConfig } from 'vitest/config';

// @cloudflare/vitest-plugin (the renamed successor of the old workers-pool package) uses the plugin-based config API
// (`cloudflareTest` as a Vite plugin + `defineConfig` from `vitest/config`), the same
// as person-service. The KV binding comes from wrangler.toml; tests build their
// own env object (secrets included) and call the real `scheduled` handler.
export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: './wrangler.toml' },
    }),
  ],
});

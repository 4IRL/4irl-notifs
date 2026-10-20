import { cloudflareTest } from '@cloudflare/vitest-pool-workers';
import { defineConfig } from 'vitest/config';

// @cloudflare/vitest-pool-workers 0.18.6 uses the plugin-based config API
// (`cloudflareTest` as a Vite plugin + `defineConfig` from `vitest/config`), the same
// shape person-service uses. The KV binding comes from wrangler.toml; tests build their
// own env object (secrets included) and call the real `scheduled` handler.
export default defineConfig({
  plugins: [
    cloudflareTest({
      wrangler: { configPath: './wrangler.toml' },
    }),
  ],
});

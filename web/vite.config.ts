/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    // Dev-only: mirrors the production same-origin shape locally. In production
    // the SPA calls relative `/v1/*` and `/people`, served by same-origin
    // Cloudflare Pages Functions (see web/functions/) that proxy to the
    // backends — so there is no cross-origin CORS in production. This dev-server
    // proxy reproduces that same-origin behavior for `npm run dev` and has no
    // effect on the production build.
    proxy: {
      '/v1': 'http://127.0.0.1:8091',
    },
  },
  test: {
    environment: 'jsdom',
    globals: false,
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}', 'functions/**/*.test.ts'],
  },
});

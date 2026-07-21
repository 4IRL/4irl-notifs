/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    // Dev-only: makes `npm run dev` same-origin with the local provisioning-api
    // so the browser never needs a CORS header. Production CORS is handled
    // entirely by the Cloudflare Access application config (see
    // docs/deploy-runbook.md); this proxy has no effect on that build.
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

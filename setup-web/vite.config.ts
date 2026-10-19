/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { setupWebPorts } from './worktree-ports.ts';

const { webPort } = setupWebPorts();

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // No `/v1` or `/delivery` dev proxy (unlike web/): the setup page makes no API calls.
  server: {
    port: webPort,
  },
  test: {
    environment: 'jsdom',
    globals: false,
    setupFiles: ['./src/test-setup.ts'],
    include: ['src/**/*.test.{ts,tsx}', 'worktree-ports.test.ts', 'pages-headers.test.ts'],
  },
});

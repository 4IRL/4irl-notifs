import { defineConfig, devices } from '@playwright/test';
import { webPorts } from './worktree-ports.ts';

const { e2ePort } = webPorts();

// E2E config for the admin UI. Tests run against the production build served
// by `vite preview`; the provisioning API is mocked at the network layer
// (page.route) since the real API is Cloudflare Access-gated.
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'list',
  use: {
    baseURL: `http://127.0.0.1:${e2ePort}`,
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  webServer: {
    // --host 127.0.0.1 is explicit: on Node 23 `localhost` resolves to ::1
    // first, so an unpinned vite preview binds IPv6-only and the 127.0.0.1
    // health-check URL below would never come up.
    command: `npm run build && npm run preview -- --host 127.0.0.1 --port ${e2ePort} --strictPort`,
    url: `http://127.0.0.1:${e2ePort}`,
    // Never attach to another worktree's preview server.
    reuseExistingServer: false,
    timeout: 120000,
    // Bakes the people + apps views into the e2e production build; the person
    // service is mocked per-test via page.route (there is no real Worker).
    env: { VITE_PEOPLE_ENABLED: 'true', VITE_APPS_ENABLED: 'true' },
  },
});

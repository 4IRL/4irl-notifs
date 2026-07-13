import { defineConfig, devices } from '@playwright/test';

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
    baseURL: 'http://127.0.0.1:4173',
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
    command: 'npm run build && npm run preview -- --host 127.0.0.1 --port 4173 --strictPort',
    url: 'http://127.0.0.1:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 120000,
  },
});

import { defineConfig, devices } from '@playwright/test';
import { setupWebPorts } from './worktree-ports.ts';

const { e2ePort } = setupWebPorts();

// E2E config for the public setup page. Tests run against the production build served
// by `vite preview`; the page makes no API calls, so nothing is mocked. Each spec group
// is tagged with the projects it applies to and every project greps for its own tag
// plus `@all`, so no spec shows up as skipped in a project it was never meant for.
// The clipboard specs assume the default headless browsers, whose clipboard is private to
// each browser: in a headed run the OS clipboard is shared and parallel workers would race.
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
    permissions: ['clipboard-read', 'clipboard-write'],
  },
  // `devices['iPhone 15']` carries `defaultBrowserType: 'webkit'`. The clipboard permission
  // grant above is Chromium's CDP permission model and WebKit does not honour it the same
  // way, so both mobile projects set `browserName: 'chromium'` explicitly, which wins over
  // the device's default and keeps only its viewport, UA and touch emulation.
  projects: [
    {
      name: 'desktop-chromium',
      grep: /@desktop|@all/,
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'iphone',
      grep: /@iphone|@all/,
      use: { ...devices['iPhone 15'], browserName: 'chromium' },
    },
    {
      name: 'android',
      grep: /@android|@all/,
      use: { ...devices['Pixel 7'], browserName: 'chromium' },
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
  },
});

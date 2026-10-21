import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

import { expect } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import { parseHeadersFile } from '../pages-headers.ts';
import { strings } from '../src/strings.ts';

// The valid fixture every spec uses, plus the values the page must derive from it.
// Written out literally on purpose: these are the URL contract, so they must not be
// computed with the same helpers the page uses.
export const VALID_LINK = '/?app=tasktracker&user=u_v4sf4e5teivpe3zi&name=Chore%20Reaper';
// The static <title> in index.html; the page never changes it.
export const PAGE_TITLE = 'Set up notifications · 4IRL';
export const NAMELESS_LINK = '/?app=tasktracker&user=u_v4sf4e5teivpe3zi';
export const FIXTURE = {
  displayName: 'Chore Reaper',
  user: 'u_v4sf4e5teivpe3zi',
  server: 'https://notifs.4irl.app',
  personalTopic: 'tasktracker-v4sf4e5teivpe3zi-all',
  broadcastTopic: 'tasktracker-broadcast',
  personalHref: 'ntfy://notifs.4irl.app/tasktracker-v4sf4e5teivpe3zi-all',
  broadcastHref: 'ntfy://notifs.4irl.app/tasktracker-broadcast',
} as const;

type PlatformName = 'iphone' | 'android';

const PLATFORM_TAB_LABEL: Record<PlatformName, string> = {
  iphone: strings.tabIphone,
  android: strings.tabAndroid,
};

export function tabFor({ page, platform }: { page: Page; platform: PlatformName }): Locator {
  return page.getByRole('tab', { name: PLATFORM_TAB_LABEL[platform] });
}

// includeHidden: a panel the page marks `hidden` must still be addressable so a spec
// can assert it is not visible.
export function panelFor({ page, platform }: { page: Page; platform: PlatformName }): Locator {
  return page.getByRole('tabpanel', { name: PLATFORM_TAB_LABEL[platform], includeHidden: true });
}

// Visibility, not just the accessibility tree: PlatformTabs.css must make `hidden` win.
// toBeHidden() alone also passes for a locator that matches nothing, so the panel must
// additionally exist and carry the `hidden` attribute.
export async function expectPanelHidden({
  page,
  platform,
}: {
  page: Page;
  platform: PlatformName;
}): Promise<void> {
  const panel = panelFor({ page, platform });
  await expect(panel).toHaveAttribute('hidden', '');
  await expect(panel).toBeHidden();
}

// Every Copy button under `scope` (a panel, or the whole page).
export function copyButtons({ scope }: { scope: Page | Locator }): Locator {
  return scope.getByRole('button', { name: new RegExp(`^${strings.copyButton}`) });
}

// The displayed value and the display-name badge have no role to query by.
export function fieldValue({ field }: { field: Locator }): Locator {
  return field.locator('.setup-page__value');
}

export function nameBadge({ page }: { page: Page }): Locator {
  return page.locator('.setup-page__badge');
}

// A labelled value with its own Copy button, scoped to one panel.
export function copyFieldFor({ scope, label }: { scope: Locator; label: string }): Locator {
  return scope
    .locator('.setup-page__copy-field')
    .filter({ has: scope.page().getByText(label, { exact: true }) });
}

export function ntfyLinks({ page }: { page: Page }): Locator {
  return page.locator('a[href^="ntfy://"]');
}

async function readClipboard({ page }: { page: Page }): Promise<string> {
  return page.evaluate(() => navigator.clipboard.readText());
}

// Clicks a field's Copy button and proves the click is what put `value` on the clipboard:
// the clipboard is emptied first so a value left by an earlier test cannot satisfy it.
export async function expectFieldCopies({
  page,
  field,
  value,
}: {
  page: Page;
  field: Locator;
  value: string;
}): Promise<void> {
  await expect(fieldValue({ field })).toHaveText(value);
  await page.evaluate(() => navigator.clipboard.writeText(''));

  await field.getByRole('button').click();

  await expect(field.getByRole('button')).toHaveText(strings.copiedButton);
  expect(await readClipboard({ page })).toBe(value);
}

// Screenshots are lazy-loaded, so each one is scrolled into view before its decoded
// size is polled. Only that the file loads is asserted, never what it looks like.
export async function expectImagesLoaded({
  scope,
  expectedCount,
}: {
  scope: Locator;
  expectedCount: number;
}): Promise<void> {
  const images = scope.locator('img');
  await expect(images).toHaveCount(expectedCount);
  for (const image of await images.all()) {
    await image.scrollIntoViewIfNeeded();
    await expect
      .poll(() => image.evaluate((element) => (element as HTMLImageElement).naturalWidth))
      .toBeGreaterThan(0);
  }
}

declare global {
  interface Window {
    __cspViolations?: string[];
  }
}

const DIST_HEADERS = fileURLToPath(new URL('../dist/_headers', import.meta.url));

// `vite preview` ignores `_headers`, so the built `dist/_headers` (what Cloudflare Pages
// would apply to `/*`) is replayed onto every same-origin response. Read lazily because
// the webServer builds `dist/` before the first test runs.
export async function serveWithShippedHeaders({
  page,
  baseURL,
}: {
  page: Page;
  baseURL: string;
}): Promise<Record<string, string>> {
  const shipped = parseHeadersFile(await readFile(DIST_HEADERS, 'utf8'))['/*'];
  if (shipped === undefined) {
    throw new Error('dist/_headers has no /* rule');
  }
  const lowercased = Object.fromEntries(
    Object.entries(shipped).map(([name, value]) => [name.toLowerCase(), value]),
  );
  await page.route(`${baseURL}/**`, async (route) => {
    const response = await route.fetch();
    await route.fulfill({ response, headers: { ...response.headers(), ...lowercased } });
  });
  return shipped;
}

// Records every securitypolicyviolation event in the page, from before any page script runs.
export async function recordCspViolations({ page }: { page: Page }): Promise<void> {
  await page.addInitScript(() => {
    window.__cspViolations = [];
    document.addEventListener('securitypolicyviolation', (event) => {
      window.__cspViolations?.push(`${event.violatedDirective} ${event.blockedURI}`);
    });
  });
}

export async function readCspViolations({ page }: { page: Page }): Promise<string[]> {
  // Violation events are queued tasks: yield one macrotask so a just-raised one is recorded.
  return page.evaluate(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
    return window.__cspViolations ?? [];
  });
}

// Console errors and uncaught page errors. Chromium also reports CSP refusals as console errors.
export function collectConsoleProblems({ page }: { page: Page }): string[] {
  const problems: string[] = [];
  page.on('console', (message) => {
    if (message.type() === 'error') {
      problems.push(`console.error: ${message.text()}`);
    }
  });
  page.on('pageerror', (error) => {
    problems.push(`pageerror: ${error.message}`);
  });
  return problems;
}

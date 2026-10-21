import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

import { strings } from '../src/strings.ts';
import {
  FIXTURE,
  NAMELESS_LINK,
  PAGE_TITLE,
  VALID_LINK,
  collectConsoleProblems,
  copyButtons,
  copyFieldFor,
  expectFieldCopies,
  expectImagesLoaded,
  expectPanelHidden,
  fieldValue,
  nameBadge,
  ntfyLinks,
  panelFor,
  readCspViolations,
  recordCspViolations,
  serveWithShippedHeaders,
  tabFor,
} from './support.ts';

// Every group is tagged with the projects it runs in (see playwright.config.ts).
// `@iphone` / `@android` / `@desktop` pin the group to the project that emulates that
// device; `@all` runs in every project.

const IOS_FIELDS = [
  { label: strings.labelDefaultServer, value: FIXTURE.server },
  { label: strings.labelUsername, value: FIXTURE.user },
  { label: strings.labelPersonalTopic, value: FIXTURE.personalTopic },
  { label: strings.labelBroadcastTopic, value: FIXTURE.broadcastTopic },
];

const ANDROID_TOPIC_FIELDS = [
  { label: strings.labelPersonalTopic, value: FIXTURE.personalTopic },
  { label: strings.labelBroadcastTopic, value: FIXTURE.broadcastTopic },
];

test.describe('iPhone', { tag: '@iphone' }, () => {
  test('loads with the iPhone tab selected and the Android panel hidden', async ({ page }) => {
    await page.goto(VALID_LINK);

    await expect(tabFor({ page, platform: 'iphone' })).toHaveAttribute('aria-selected', 'true');
    await expect(tabFor({ page, platform: 'android' })).toHaveAttribute('aria-selected', 'false');
    await expect(page.getByText(strings.hintDetected(strings.tabIphone))).toBeVisible();
    await expect(panelFor({ page, platform: 'iphone' })).toBeVisible();
    await expectPanelHidden({ page, platform: 'android' });
    await expect(page.getByRole('img', { name: strings.qrAriaLabel })).toHaveCount(0);
    await expect(ntfyLinks({ page })).toHaveCount(0);
    await expect(nameBadge({ page })).toHaveText(FIXTURE.displayName);
  });

  for (const { label, value } of IOS_FIELDS) {
    test(`copy button puts the ${label} value on the clipboard`, async ({ page }) => {
      await page.goto(VALID_LINK);
      const field = copyFieldFor({ scope: panelFor({ page, platform: 'iphone' }), label });

      await expectFieldCopies({ page, field, value });
    });
  }

  test('the iPhone screenshots load', async ({ page }) => {
    await page.goto(VALID_LINK);
    await expectImagesLoaded({ scope: panelFor({ page, platform: 'iphone' }), expectedCount: 2 });
  });

  test('switching to Android shows copy-only steps with no ntfy:// links', async ({ page }) => {
    await page.goto(VALID_LINK);

    await tabFor({ page, platform: 'android' }).click();

    await expect(page.getByText(strings.hintShowing(strings.tabAndroid))).toBeVisible();
    const android = panelFor({ page, platform: 'android' });
    await expect(android).toBeVisible();
    await expectPanelHidden({ page, platform: 'iphone' });
    // One-tap links need the ntfy app on this very device, which is an iPhone here.
    await expect(ntfyLinks({ page })).toHaveCount(0);
    for (const { label, value } of ANDROID_TOPIC_FIELDS) {
      const field = copyFieldFor({ scope: android, label });
      await expect(fieldValue({ field })).toHaveText(value);
      await expect(field.getByRole('button')).toBeVisible();
    }
    await expectImagesLoaded({ scope: android, expectedCount: 1 });
  });
});

test.describe('Android', { tag: '@android' }, () => {
  test('loads with the Android tab selected and both ntfy:// links', async ({ page }) => {
    await page.goto(VALID_LINK);
    const android = panelFor({ page, platform: 'android' });

    await expect(tabFor({ page, platform: 'android' })).toHaveAttribute('aria-selected', 'true');
    await expect(tabFor({ page, platform: 'iphone' })).toHaveAttribute('aria-selected', 'false');
    await expect(page.getByText(strings.hintDetected(strings.tabAndroid))).toBeVisible();
    await expect(android).toBeVisible();
    await expectPanelHidden({ page, platform: 'iphone' });
    await expect(page.getByRole('img', { name: strings.qrAriaLabel })).toHaveCount(0);

    await expect(ntfyLinks({ page })).toHaveCount(2);
    await expect(
      android.getByRole('link', { name: strings.androidOneTapPersonal }),
    ).toHaveAttribute('href', FIXTURE.personalHref);
    await expect(
      android.getByRole('link', { name: strings.androidOneTapBroadcast }),
    ).toHaveAttribute('href', FIXTURE.broadcastHref);
    for (const { label } of ANDROID_TOPIC_FIELDS) {
      await expect(copyFieldFor({ scope: android, label }).getByRole('button')).toBeVisible();
    }
  });

  for (const { label, value } of ANDROID_TOPIC_FIELDS) {
    test(`copy button puts the ${label} value on the clipboard`, async ({ page }) => {
      await page.goto(VALID_LINK);
      const field = copyFieldFor({ scope: panelFor({ page, platform: 'android' }), label });

      await expectFieldCopies({ page, field, value });
    });
  }

  test('the Android screenshot loads', async ({ page }) => {
    await page.goto(VALID_LINK);
    await expectImagesLoaded({
      scope: panelFor({ page, platform: 'android' }),
      expectedCount: 1,
    });
  });

  test('switching to iPhone removes the ntfy:// links and switching back restores them', async ({
    page,
  }) => {
    await page.goto(VALID_LINK);

    await tabFor({ page, platform: 'iphone' }).click();

    await expect(page.getByText(strings.hintShowing(strings.tabIphone))).toBeVisible();
    await expect(panelFor({ page, platform: 'iphone' })).toBeVisible();
    await expectPanelHidden({ page, platform: 'android' });
    // The one-tap anchors only render while Android is the shown tab.
    await expect(ntfyLinks({ page })).toHaveCount(0);
    await expectImagesLoaded({ scope: panelFor({ page, platform: 'iphone' }), expectedCount: 2 });

    await tabFor({ page, platform: 'android' }).click();

    await expect(page.getByText(strings.hintDetected(strings.tabAndroid))).toBeVisible();
    await expect(ntfyLinks({ page })).toHaveCount(2);
  });
});

test.describe('desktop', { tag: '@desktop' }, () => {
  test('shows the QR and no steps until a platform is picked', async ({ page }) => {
    await page.goto(VALID_LINK);

    const qr = page.getByRole('img', { name: strings.qrAriaLabel });
    await expect(qr).toBeVisible();
    await expect(qr.locator('svg')).toBeVisible();
    await expect(page.getByText(strings.hintPickPhone)).toBeVisible();
    await expect(tabFor({ page, platform: 'iphone' })).toHaveAttribute('aria-selected', 'false');
    await expect(tabFor({ page, platform: 'android' })).toHaveAttribute('aria-selected', 'false');
    await expectPanelHidden({ page, platform: 'iphone' });
    await expectPanelHidden({ page, platform: 'android' });
    await expect(page.locator('.setup-page__steps')).toHaveCount(0);
    await expect(copyButtons({ scope: page })).toHaveCount(0);
    await expect(ntfyLinks({ page })).toHaveCount(0);
  });

  test('picking Android shows two topic copy buttons and zero ntfy:// links', async ({ page }) => {
    await page.goto(VALID_LINK);
    const urlBefore = page.url();

    await tabFor({ page, platform: 'android' }).click();

    await expect(page.getByText(strings.hintShowing(strings.tabAndroid))).toBeVisible();
    const android = panelFor({ page, platform: 'android' });
    await expect(android).toBeVisible();
    await expectPanelHidden({ page, platform: 'iphone' });
    await expect(ntfyLinks({ page })).toHaveCount(0);
    for (const { label, value } of ANDROID_TOPIC_FIELDS) {
      const field = copyFieldFor({ scope: android, label });
      await expect(fieldValue({ field })).toHaveText(value);
      await expect(field.getByRole('button')).toBeVisible();
    }
    // Two topics, plus the Service URL and Username fields of the log-in step.
    await expect(copyButtons({ scope: android })).toHaveCount(4);
    await expect(page.getByRole('img', { name: strings.qrAriaLabel })).toBeVisible();
    // The choice is page state only; it is never written to the URL.
    expect(page.url()).toBe(urlBefore);
  });

  test('picking iPhone shows the iPhone steps and a working copy button', async ({ page }) => {
    await page.goto(VALID_LINK);

    await tabFor({ page, platform: 'iphone' }).click();

    await expect(page.getByText(strings.hintShowing(strings.tabIphone))).toBeVisible();
    const iphone = panelFor({ page, platform: 'iphone' });
    await expect(iphone).toBeVisible();
    await expectPanelHidden({ page, platform: 'android' });
    await expect(ntfyLinks({ page })).toHaveCount(0);
    await expect(copyButtons({ scope: iphone })).toHaveCount(4);

    const field = copyFieldFor({ scope: iphone, label: strings.labelPersonalTopic });
    await expectFieldCopies({ page, field, value: FIXTURE.personalTopic });
    await expectImagesLoaded({ scope: iphone, expectedCount: 2 });
  });
});

test.describe('invalid link', { tag: '@all' }, () => {
  // `leaked` lists every raw parameter value the page must not echo anywhere in its markup,
  // including a valid value paired with an invalid one.
  const INVALID_LINKS = [
    { name: 'no parameters', link: '/', leaked: [] },
    { name: 'missing user', link: '/?app=tasktracker', leaked: ['tasktracker'] },
    { name: 'missing app', link: `/?user=${FIXTURE.user}`, leaked: [FIXTURE.user] },
    {
      name: 'reserved app',
      link: `/?app=everyone&user=${FIXTURE.user}`,
      leaked: ['everyone', FIXTURE.user],
    },
    {
      name: 'malformed user',
      link: '/?app=tasktracker&user=u_notvalid',
      leaked: ['tasktracker', 'u_notvalid'],
    },
  ];

  for (const { name, link, leaked } of INVALID_LINKS) {
    test(`${name} shows the alert and no copy buttons`, async ({ page }) => {
      await page.goto(link);

      const alert = page.getByRole('alert');
      await expect(alert).toBeVisible();
      await expect(alert.getByRole('heading', { name: strings.errorHeading })).toBeVisible();
      await expect(copyButtons({ scope: page })).toHaveCount(0);
      await expect(page.getByRole('tablist')).toHaveCount(0);
      await expect(page.getByRole('img', { name: strings.qrAriaLabel })).toHaveCount(0);
      await expect(ntfyLinks({ page })).toHaveCount(0);
      // Whole serialized document, not just visible text: attributes and the title count too.
      const markup = await page.content();
      for (const value of leaked) {
        expect(markup, value).not.toContain(value);
      }
    });
  }
});

test.describe('missing name', { tag: '@all' }, () => {
  test('falls back to the app id as a monospace badge', async ({ page }) => {
    await page.goto(NAMELESS_LINK);

    const badge = nameBadge({ page });
    await expect(badge).toHaveText('tasktracker');
    await expect(badge).toHaveClass(/setup-page__badge--mono/);
    await expect(page.getByRole('tablist')).toBeVisible();
  });
});

test.describe('install card', { tag: '@all' }, () => {
  // Host prefixes, not our own constants: this pins that the buttons go to the real stores.
  const STORE_HOSTS = [
    { name: strings.googlePlayName, hrefStart: 'https://play.google.com/' },
    { name: strings.fDroidName, hrefStart: 'https://f-droid.org/' },
    { name: strings.appStoreName, hrefStart: 'https://apps.apple.com/' },
  ];

  function storeLinks({ page }: { page: Page }) {
    return page.getByRole('link', { name: new RegExp(`^${strings.getNtfyOn}`) });
  }

  test('shows the three store links in order, opening in a new tab', async ({ page }) => {
    await page.goto(VALID_LINK);

    await expect(
      page.getByRole('heading', { level: 2, name: strings.installCardHeading }),
    ).toBeVisible();
    const links = storeLinks({ page });
    await expect(links).toHaveCount(STORE_HOSTS.length);
    for (const [index, { name, hrefStart }] of STORE_HOSTS.entries()) {
      const link = links.nth(index);
      await expect(link).toHaveText(name);
      await expect(link).toHaveAttribute('href', new RegExp(`^${hrefStart}`));
      await expect(link).toHaveAttribute('target', '_blank');
      await expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    }
  });

  test('stays on one row, tappable and unclipped, at 320px wide', async ({ page }) => {
    await page.setViewportSize({ width: 320, height: 640 });
    await page.goto(VALID_LINK);

    const links = await storeLinks({ page }).all();
    expect(links).toHaveLength(STORE_HOSTS.length);
    const boxes = [];
    for (const link of links) {
      await expect(link).toBeVisible();
      const box = await link.boundingBox();
      if (box === null) throw new Error('store link has no bounding box');
      boxes.push(box);
      // The visible label is never clipped by its button.
      expect(await link.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    }
    // Never wraps: every button shares one top edge.
    expect(new Set(boxes.map((box) => Math.round(box.y))).size).toBe(1);
    // Comfortable tap target.
    for (const box of boxes) {
      expect(box.height).toBeGreaterThanOrEqual(44);
    }
    // And the row does not push the page wider than the screen.
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth,
      ),
    ).toBe(true);
  });
});

test.describe('content security policy', { tag: '@all' }, () => {
  // A request can still be in flight (route.fetch then route.fulfill) when a spec's last
  // assertion passes. Closing the page under it disposes the fetched response and fails the
  // handler with "Fetch response has been disposed", so wait for running handlers to finish.
  test.afterEach(async ({ page }) => {
    await page.unrouteAll({ behavior: 'wait' });
  });

  test('production _headers produce no CSP violation or console error', async ({
    page,
    baseURL,
  }) => {
    if (baseURL === undefined) throw new Error('baseURL is not configured');
    const shipped = await serveWithShippedHeaders({ page, baseURL });
    await recordCspViolations({ page });
    const problems = collectConsoleProblems({ page });

    const response = await page.goto(VALID_LINK);

    // The policy really is attached to the document the browser enforces.
    for (const [name, value] of Object.entries(shipped)) {
      expect(await response?.headerValue(name), name).toBe(value);
    }
    // Drive every state the page can reach: both platforms, both panels' copy buttons and
    // screenshots, and (on Android) the ntfy:// anchors.
    for (const platform of ['iphone', 'android'] as const) {
      await tabFor({ page, platform }).click();
      const panel = panelFor({ page, platform });
      await expect(panel).toBeVisible();
      await expectImagesLoaded({ scope: panel, expectedCount: platform === 'iphone' ? 2 : 1 });
      // By class, not role+name: a clicked button's accessible name changes to "Copied!",
      // which would shift every later index of a name-based locator list.
      for (const button of await panel.locator('.setup-page__copy').all()) {
        await button.click();
        await expect(button).toHaveText(strings.copiedButton);
      }
    }
    await expect(page.locator('#root')).not.toBeEmpty();

    expect(await readCspViolations({ page })).toEqual([]);
    expect(problems).toEqual([]);
  });

  test('a hostile name renders as literal text with no injected element or violation', async ({
    page,
    baseURL,
  }) => {
    if (baseURL === undefined) throw new Error('baseURL is not configured');
    await serveWithShippedHeaders({ page, baseURL });
    await recordCspViolations({ page });
    const problems = collectConsoleProblems({ page });
    const hostileName = `<img src="/nope.png" onerror="document.title='pwned'">`;

    await page.goto(
      `/?app=tasktracker&user=${FIXTURE.user}&name=${encodeURIComponent(hostileName)}`,
    );

    await expect(nameBadge({ page })).toHaveText(hostileName);
    await expect(page.locator('img[src="/nope.png"]')).toHaveCount(0);
    await expect(page).toHaveTitle(PAGE_TITLE);
    expect(await readCspViolations({ page })).toEqual([]);
    expect(problems).toEqual([]);
  });

  test('the probe detects a violation: an injected inline script is blocked', async ({
    page,
    baseURL,
  }) => {
    if (baseURL === undefined) throw new Error('baseURL is not configured');
    await serveWithShippedHeaders({ page, baseURL });
    await recordCspViolations({ page });
    const problems = collectConsoleProblems({ page });
    await page.goto(VALID_LINK);

    await page.evaluate(() => {
      const script = document.createElement('script');
      script.textContent = 'document.title = "injected"';
      document.body.appendChild(script);
    });

    await expect
      .poll(() => readCspViolations({ page }))
      .toEqual([expect.stringMatching(/^script-src/)]);
    await expect(page).not.toHaveTitle('injected');
    // Chromium also reports the refusal as a console error, so the collector the main CSP
    // test relies on is proven to fire.
    expect(problems).toEqual([expect.stringContaining('Content Security Policy')]);
  });
});

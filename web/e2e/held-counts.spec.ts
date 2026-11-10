import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import {
  appWire,
  expectNoPageOverflow,
  heldAppWire,
  heldFailureWire,
  mockDelivery,
  mockProvisioning,
  unassignedCheckWire,
} from './support/adminMocks';
import type { DeliveryMock, DeliveryMockSeed } from './support/adminMocks';

// Held-message counts in the admin UI (hold-and-release P6): the Apps Held column, the
// Unprovisioned apps held-only row, the delivery-api status card rows and the Delivery log
// "Release failed" filter, in ready, empty and unavailable conditions, at desktop and phone width.

const HELD_PATH = '/delivery/v1/admin/held-messages';
const FAILURES_PATH = '/delivery/v1/admin/held-failures';
const LONG_APP_ID = 'a-very-long-application-identifier-that-keeps-going-and-going';
const LONG_CHANNEL = 'a-very-long-channel-name-for-the-held-failure-table-row';
const LONG_REASON = 'no-deliverable-destination-was-found-for-this-app-and-channel-at-release';

const APPS = [
  appWire({ appId: 'urls4irl', displayName: 'URLs4IRL' }),
  appWire({ appId: 'tasktracker', displayName: 'Task Tracker' }),
];

async function setup({
  page,
  seed = {},
}: {
  page: Page;
  seed?: DeliveryMockSeed;
}): Promise<DeliveryMock> {
  const mock = await mockDelivery(page, seed);
  await mockProvisioning(page, { apps: APPS });
  return mock;
}

function appsSection(page: Page): Locator {
  return page.locator('section.apps-table');
}

function unprovisionedSection(page: Page): Locator {
  return page.locator('section.unregistered-apps');
}

function statusCard(page: Page): Locator {
  return page.getByRole('region', { name: 'delivery-api' });
}

/** The dd of a status-card fact row, found by its dt label. */
function factValue({ card, label }: { card: Locator; label: string }): Locator {
  return card.locator('div', { has: card.page().locator('dt', { hasText: label }) }).locator('dd');
}

function heldReads(mock: DeliveryMock): number {
  return mock.reads.filter((request) => request.pathname === HELD_PATH).length;
}

async function expectUnavailableCell(cell: Locator): Promise<void> {
  await expect(cell).toContainText('—');
  const hidden = cell.locator('.visually-hidden');
  await expect(hidden).toBeAttached();
  await expect(hidden).toHaveText(/unavailable/);
  await expect(cell).not.toHaveText(/^0$/);
}

test.describe('held counts on desktop', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
  });

  test('Apps shows a pill for held messages and a plain 0 otherwise; held-only app is unprovisioned', async ({
    page,
  }) => {
    const mock = await setup({
      page,
      seed: {
        heldMessages: {
          apps: [
            heldAppWire({ appId: 'urls4irl', count: 3 }),
            heldAppWire({ appId: 'ghostapp', count: 2 }),
          ],
          unassigned_checks: [],
          unassigned_total: 0,
        },
      },
    });

    await page.goto('/');
    const apps = appsSection(page);
    await expect(apps.getByRole('columnheader', { name: 'Held' })).toBeVisible();
    const urls4irlRow = apps.getByRole('row', { name: /urls4irl/ });
    await expect(urls4irlRow.locator('.held-count__pill')).toHaveText('3 held');
    const taskRow = apps.getByRole('row', { name: /tasktracker/ });
    await expect(taskRow.locator('.held-count')).toHaveText('0');
    await expect(taskRow.locator('.held-count__pill')).toHaveCount(0);

    const unprovisioned = unprovisionedSection(page);
    const ghostRow = unprovisioned.getByRole('row', { name: /ghostapp/ });
    await expect(ghostRow).toBeVisible();
    // Subscribers 0, then the held pill.
    await expect(ghostRow.getByRole('cell').nth(1)).toHaveText('0');
    await expect(ghostRow.locator('.held-count__pill')).toHaveText('2 held');
    expect(mock.unmocked).toEqual([]);
  });

  test('a 503 from held-messages shows the unavailable state on Apps and the status card only', async ({
    page,
  }) => {
    const mock = await setup({
      page,
      seed: {
        heldMessagesStatus: 503,
        heldFailures: {
          failures: [heldFailureWire({ messageId: 'm-1', appId: 'urls4irl' })],
          total: 1,
        },
      },
    });

    await page.goto('/');
    const apps = appsSection(page);
    await expectUnavailableCell(apps.getByRole('row', { name: /urls4irl/ }).locator('.held-count'));
    await expectUnavailableCell(
      apps.getByRole('row', { name: /tasktracker/ }).locator('.held-count'),
    );
    await expect(page.getByRole('alert')).toHaveCount(0);

    await page.getByRole('tab', { name: 'Delivery' }).click();
    const card = statusCard(page);
    await expect(card.getByText('Ready', { exact: true })).toBeVisible();
    await expectUnavailableCell(factValue({ card, label: 'Held messages' }));
    await expectUnavailableCell(factValue({ card, label: 'Unassigned heartbeat alerts' }));
    await expect(card.getByRole('alert')).toHaveCount(0);

    // Release failures degrade independently: this source still loads.
    const log = page.getByRole('region', { name: 'Delivery log' });
    await log.getByRole('button', { name: 'Release failed', exact: true }).click();
    await expect(log.getByRole('table', { name: 'Release failures' })).toBeVisible();
    await expect(log.getByText('Release failures are unavailable on this backend.')).toHaveCount(0);
    expect(mock.unmocked).toEqual([]);
  });

  test('a 404 from held-failures shows unavailable text while held counts still render', async ({
    page,
  }) => {
    const mock = await setup({
      page,
      seed: {
        heldFailuresStatus: 404,
        heldMessages: {
          apps: [heldAppWire({ appId: 'urls4irl', count: 3 })],
          unassigned_checks: [],
          unassigned_total: 0,
        },
      },
    });

    await page.goto('/');
    await expect(
      appsSection(page)
        .getByRole('row', { name: /urls4irl/ })
        .locator('.held-count__pill'),
    ).toHaveText('3 held');

    await page.getByRole('tab', { name: 'Delivery' }).click();
    const log = page.getByRole('region', { name: 'Delivery log' });
    await log.getByRole('button', { name: 'Release failed', exact: true }).click();
    await expect(log.getByText('Release failures are unavailable on this backend.')).toBeVisible();
    await expect(page.getByRole('alert')).toHaveCount(0);
    await expect(factValue({ card: statusCard(page), label: 'Held messages' })).toHaveText('3');
    expect(mock.unmocked).toEqual([]);
  });

  test('status card held rows render with readiness unreachable, and 73 unassigned beats the 50-row list', async ({
    page,
  }) => {
    const checks = Array.from({ length: 50 }, (_, index) =>
      unassignedCheckWire({ checkId: index + 1, name: `check_${index + 1}` }),
    );
    const mock = await setup({
      page,
      seed: {
        readiness: { status: 500, body: { error: 'boom' } },
        heldMessages: {
          apps: [heldAppWire({ appId: 'urls4irl', count: 3 })],
          unassigned_checks: checks,
          unassigned_total: 73,
        },
      },
    });

    await page.goto('/#delivery');
    const card = statusCard(page);
    await expect(card.getByText('Unreachable', { exact: true })).toBeVisible();
    await expect(factValue({ card, label: 'Held messages' })).toHaveText('3');
    await expect(factValue({ card, label: 'Unassigned heartbeat alerts' })).toHaveText('73');
    expect(mock.unmocked).toEqual([]);
  });

  test('status card Refresh issues a second held-messages read', async ({ page }) => {
    const mock = await setup({ page });

    await page.goto('/#delivery');
    const card = statusCard(page);
    await expect(factValue({ card, label: 'Held messages' })).toHaveText('0');
    await expect.poll(() => heldReads(mock)).toBe(1);

    await card.getByRole('button', { name: 'Refresh status', exact: true }).click();
    await expect.poll(() => heldReads(mock)).toBe(2);
    expect(mock.unmocked).toEqual([]);
  });

  test('Release failed: empty state', async ({ page }) => {
    const emptyMock = await setup({ page });
    await page.goto('/#delivery');
    const log = page.getByRole('region', { name: 'Delivery log' });
    await log.getByRole('button', { name: 'Release failed', exact: true }).click();
    await expect(log.getByText(/^No release failures\./)).toBeVisible();
    expect(emptyMock.reads.some((request) => request.pathname === FAILURES_PATH)).toBe(true);
    expect(emptyMock.unmocked).toEqual([]);
  });

  test('Release failed: a truncated list says how many are shown of the total', async ({
    page,
  }) => {
    const failures = Array.from({ length: 50 }, (_, index) =>
      heldFailureWire({ messageId: `message-${index + 1}` }),
    );
    const mock = await setup({ page, seed: { heldFailures: { failures, total: 63 } } });

    await page.goto('/#delivery');
    const log = page.getByRole('region', { name: 'Delivery log' });
    await log.getByRole('button', { name: 'Release failed', exact: true }).click();
    await expect(log.getByText('Showing the newest 50 of 63 release failures.')).toBeVisible();
    await expect(
      log.getByRole('table', { name: 'Release failures' }).locator('tbody tr'),
    ).toHaveCount(50);
    expect(mock.unmocked).toEqual([]);
  });
});

test.describe('held counts at 390px', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

  test('Apps lead-cell sub-line, held-only row and unavailable text stay on one screen', async ({
    page,
  }) => {
    const mock = await setup({
      page,
      seed: {
        heldMessages: {
          apps: [
            heldAppWire({ appId: 'urls4irl', count: 3 }),
            heldAppWire({ appId: LONG_APP_ID, count: 2 }),
          ],
          unassigned_checks: [],
          unassigned_total: 0,
        },
      },
    });

    await page.goto('/');
    const apps = appsSection(page);
    await expect(
      apps.getByRole('row', { name: /urls4irl/ }).locator('.held-count__pill'),
    ).toHaveText('3 held');
    await expect(apps.getByRole('row', { name: /tasktracker/ }).locator('.held-count')).toHaveText(
      '0 held',
    );

    const unprovisioned = unprovisionedSection(page);
    const ghostRow = unprovisioned.getByRole('row', { name: new RegExp(LONG_APP_ID) });
    await expect(ghostRow.locator('.held-count__pill')).toHaveText('2 held');
    await expect(ghostRow.getByRole('cell').last().locator('.held-count__pill')).toBeVisible();
    await expect(unprovisioned.getByRole('group', { name: 'Unprovisioned apps' })).toBeVisible();
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });

  test('unavailable sub-line carries a hidden word of at most 1px', async ({ page }) => {
    const mock = await setup({ page, seed: { heldMessagesStatus: 503 } });

    await page.goto('/');
    const cell = appsSection(page)
      .getByRole('row', { name: /urls4irl/ })
      .locator('.held-count');
    await expect(cell).toContainText('Held: —');
    const hidden = cell.locator('.visually-hidden');
    await expect(hidden).toBeAttached();
    const box = await hidden.boundingBox();
    expect(box).not.toBeNull();
    expect(box?.width ?? Infinity).toBeLessThanOrEqual(1);
    expect(box?.height ?? Infinity).toBeLessThanOrEqual(1);
    await expect(page.getByRole('alert')).toHaveCount(0);
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });

  test('the Release failed filter menu item works and its table stays inside its scroll wrapper', async ({
    page,
  }) => {
    const mock = await setup({
      page,
      seed: {
        heldFailures: {
          failures: [
            heldFailureWire({
              messageId: 'message-1',
              appId: LONG_APP_ID,
              channel: LONG_CHANNEL,
              reason: LONG_REASON,
            }),
          ],
          total: 1,
        },
      },
    });

    await page.goto('/#delivery');
    const log = page.getByRole('region', { name: 'Delivery log' });
    await log.getByRole('button', { name: 'Filter deliveries', exact: true }).click();
    await page.getByRole('menuitemradio', { name: 'Release failed', exact: true }).click();

    const table = log.getByRole('table', { name: 'Release failures' });
    await expect(table).toBeVisible();
    await expect(table.getByText(LONG_APP_ID)).toBeVisible();
    // The wrapper holds the table (it scrolls or the long ids wrap, never the page).
    const wrapperBox = await log.locator('.delivery-log__scroll').boundingBox();
    expect((wrapperBox?.x ?? 0) + (wrapperBox?.width ?? Infinity)).toBeLessThanOrEqual(390);
    await expect(log.locator('.delivery-log__scroll').getByRole('table')).toBeVisible();
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });
});

import { expect, test } from '@playwright/test';

import {
  deliveryWire,
  destinationWire,
  expectDialogInViewport,
  expectNoPageOverflow,
  expectTouchTarget,
  heartbeatWire,
  mockDelivery,
  mockProvisioning,
  openRowMenu,
  personalDestinationWire,
  rowKebab,
} from './support/adminMocks';

// Operator flows for personal destinations that the critical-flow suite in admin.spec.ts does not
// cover: the empty card, the truncation hint, a failed action, the Delivery Log marker, the
// exclusion from operator pickers, and the phone layout. The API is mocked at the network layer.

const HASH = '76gzqgp4byjl6dje';
const ROW_NAME = `Alpha of ${HASH}`;

test.describe('personal destinations', () => {
  test.beforeEach(async ({ page }) => {
    await mockProvisioning(page);
  });

  test('shows the empty card when there are none', async ({ page }) => {
    const mock = await mockDelivery(page);
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Personal destinations' });

    await expect(section.getByText(/^No personal destinations yet\./)).toBeVisible();
    await expect(section.getByText('0 personal destinations')).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('a load failure shows an error and Retry recovers', async ({ page }) => {
    const mock = await mockDelivery(page, {
      personalDestinations: [personalDestinationWire({ id: 1, label: 'Alpha' })],
    });
    let failures = 1;
    await page.route('**/delivery/v1/admin/personal-destinations', async (route) => {
      if (failures > 0) {
        failures -= 1;
        await route.fulfill({
          status: 503,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'delivery-api unavailable' }),
        });
        return;
      }
      await route.fallback();
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Personal destinations' });
    await expect(section.getByRole('alert')).toContainText(
      'Could not load personal destinations: delivery-api unavailable.',
    );
    await section.getByRole('button', { name: 'Retry', exact: true }).click();
    await expect(
      section.getByRole('button', {
        name: `Disable personal destination ${ROW_NAME}`,
        exact: true,
      }),
    ).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('a total beyond the loaded rows shows the truncation hint and the scoped summary', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      personalDestinations: [
        personalDestinationWire({ id: 1, label: 'Alpha' }),
        personalDestinationWire({ id: 2, label: 'Beta', enabled: false }),
      ],
      personalDestinationsTotal: 250,
    });
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Personal destinations' });

    await expect(
      section.getByText('250 personal destinations, 1 disabled among the newest 2'),
    ).toBeVisible();
    await expect(section.getByText('Showing the newest 2 of 250.')).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('a failed disable shows a row alert, keeps the row enabled and returns focus', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      personalDestinations: [personalDestinationWire({ id: 1, label: 'Alpha' })],
    });
    await page.route('**/delivery/v1/admin/personal-destinations/1', async (route) => {
      if (route.request().method() === 'PATCH') {
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'boom' }),
        });
        return;
      }
      await route.fallback();
    });
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Personal destinations' });
    const disable = section.getByRole('button', {
      name: `Disable personal destination ${ROW_NAME}`,
      exact: true,
    });

    await disable.click();
    await page
      .getByRole('button', {
        name: `Confirm disable personal destination ${ROW_NAME}`,
        exact: true,
      })
      .click();

    await expect(section.getByRole('alert')).toHaveText(
      'Could not disable the personal destination. Try again.',
    );
    await expect(disable).toBeVisible();
    await expect(disable).toBeFocused();
    expect(mock.unmocked).toEqual([]);
  });

  test('the Delivery Log marks personal deliveries by text and offers no Replay on opted out', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      deliveries: {
        dead: [
          deliveryWire({
            id: 12,
            targetKind: 'personal',
            personHash: HASH,
            destination: '@personal_12',
            deadReason: 'opted_out',
          }),
          deliveryWire({ id: 13 }),
        ],
      },
    });
    await page.goto('/#delivery');
    const log = page.getByRole('region', { name: 'Delivery log' });

    const personalRow = log.getByRole('row').filter({ hasText: '#12' });
    await expect(personalRow.getByText('Personal', { exact: true })).toBeVisible();
    await expect(personalRow.getByText(`Webhook · ${HASH}`)).toBeVisible();
    await expect(personalRow.getByText('opted out', { exact: true })).toBeVisible();
    await expect(personalRow.getByText('person opted out')).toBeVisible();
    await expect(log.getByRole('button', { name: 'Replay delivery 12', exact: true })).toHaveCount(
      0,
    );

    // The operator row keeps its name, its type and its Replay.
    const operatorRow = log.getByRole('row').filter({ hasText: 'hook1' });
    await expect(operatorRow.getByText('Personal', { exact: true })).toHaveCount(0);
    await expect(
      log.getByRole('button', { name: 'Replay delivery 13', exact: true }),
    ).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('personal rows never appear in the Destinations table, route checkboxes or heartbeat pickers', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
      heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })],
      personalDestinations: [
        personalDestinationWire({ id: 1, label: 'Zebra' }),
        personalDestinationWire({ id: 2, label: 'Yak', type: 'webhook' }),
      ],
    });
    await page.goto('/#delivery');
    const personal = page.getByRole('region', { name: 'Personal destinations' });
    await expect(personal.getByRole('cell', { name: /^Zebra/ })).toBeVisible();

    const destinationsTable = page.locator('.destinations__table');
    await expect(destinationsTable.getByRole('cell', { name: 'hook1', exact: true })).toBeVisible();
    await expect(destinationsTable.getByText('Zebra')).toHaveCount(0);
    await expect(destinationsTable.getByText('Yak')).toHaveCount(0);
    await expect(destinationsTable.getByText('@personal_1')).toHaveCount(0);

    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    const routeForm = page.getByRole('form', { name: 'Add route' });
    await expect(routeForm.getByRole('checkbox', { name: 'hook1' })).toBeVisible();
    await expect(routeForm.getByRole('checkbox', { name: /Zebra|Yak|@personal/ })).toHaveCount(0);

    const heartbeats = page.getByRole('region', { name: 'Heartbeats' });
    await heartbeats.getByRole('button', { name: 'Add check', exact: true }).click();
    const heartbeatForm = heartbeats.getByRole('form', { name: 'Add check' });
    await expect(heartbeatForm.getByText('hook1')).toBeVisible();
    await expect(heartbeatForm.getByText('Zebra')).toHaveCount(0);
    await expect(heartbeatForm.getByText('Yak')).toHaveCount(0);
    expect(mock.unmocked).toEqual([]);
  });

  test.describe('at 390px', () => {
    test.beforeEach(async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
    });

    test('a long label wraps and keeps the kebab in view, collapsed and expanded', async ({
      page,
    }) => {
      await mockDelivery(page, {
        personalDestinations: [
          personalDestinationWire({ id: 1, label: 'Long label '.repeat(8).trim() }),
        ],
      });
      await page.goto('/#delivery');
      const section = page.getByRole('region', { name: 'Personal destinations' });
      const expand = section.locator('button[aria-controls^="personal-details-"]').first();
      await expect(expand).toBeVisible();
      await expectNoPageOverflow(page);
      const kebab = section.getByRole('button', { name: /^Actions for / });
      const viewportWidth = page.viewportSize()?.width ?? 0;
      const collapsedBox = await kebab.boundingBox();
      expect((collapsedBox?.x ?? 0) + (collapsedBox?.width ?? 0)).toBeLessThanOrEqual(
        viewportWidth,
      );
      await expand.click();
      await expectNoPageOverflow(page);
      const expandedBox = await kebab.boundingBox();
      expect((expandedBox?.x ?? 0) + (expandedBox?.width ?? 0)).toBeLessThanOrEqual(viewportWidth);
    });

    test('no page overflow, a 44px kebab and dialogs that fit', async ({ page }) => {
      const mock = await mockDelivery(page, {
        personalDestinations: [
          personalDestinationWire({ id: 1, label: 'Alpha' }),
          personalDestinationWire({ id: 2, label: 'Beta', type: 'webhook' }),
        ],
        deliveries: {
          dead: [
            deliveryWire({
              id: 12,
              targetKind: 'personal',
              personHash: HASH,
              destination: '@personal_12',
              deadReason: 'opted_out',
            }),
          ],
        },
      });
      await page.goto('/#delivery');
      const section = page.getByRole('region', { name: 'Personal destinations' });
      await expect(section.getByText('Alpha', { exact: true })).toBeVisible();

      await expectNoPageOverflow(page);
      await expectTouchTarget(rowKebab({ page, row: ROW_NAME }));

      // Tap to expand: the collapsed row hides the target and chips; the expand control is a
      // 44px target, separate from the kebab, and the page still does not overflow.
      const expand = section.locator('button[aria-controls^="personal-details-"]').first();
      await expectTouchTarget(expand);
      await expect(expand).toHaveAttribute('aria-expanded', 'false');
      await expect(section.getByText('https://discord.com/****')).toBeHidden();
      await expand.click();
      await expect(expand).toHaveAttribute('aria-expanded', 'true');
      await expect(section.getByText('https://discord.com/****')).toBeVisible();
      await expect(section.getByText('Discord', { exact: true })).toBeVisible();
      await expect(section.locator('.personal__chips').first()).toBeVisible();
      await expectNoPageOverflow(page);
      await expand.click();
      await expect(expand).toHaveAttribute('aria-expanded', 'false');
      await expect(section.getByText('https://discord.com/****')).toBeHidden();
      // The kebab is its own control: opening its menu never toggles the row.
      await rowKebab({ page, row: ROW_NAME }).click();
      await expect(page.getByRole('menu')).toBeVisible();
      await expect(expand).toHaveAttribute('aria-expanded', 'false');
      await page.keyboard.press('Escape');
      await expect(
        page.getByRole('region', { name: 'Delivery log' }).getByText('Personal', { exact: true }),
      ).toBeVisible();

      const menu = await openRowMenu({ page, row: ROW_NAME });
      await menu
        .getByRole('menuitem', { name: `Delete personal destination ${ROW_NAME}`, exact: true })
        .click();
      await expectDialogInViewport(page);
      await page
        .getByRole('button', {
          name: `Cancel Delete personal destination ${ROW_NAME}`,
          exact: true,
        })
        .click();
      await expect(page.getByRole('dialog')).toHaveCount(0);
      await expect(rowKebab({ page, row: ROW_NAME })).toBeFocused();
      await expectNoPageOverflow(page);
      expect(mock.mutations).toEqual([]);
      expect(mock.unmocked).toEqual([]);
    });
  });
});

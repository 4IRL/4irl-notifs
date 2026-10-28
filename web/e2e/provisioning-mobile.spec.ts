import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import {
  appWire,
  destinationWire,
  expectCellsCenteredInRow,
  expectNoPageOverflow,
  expectTouchTarget,
  mockDelivery,
  mockProvisioning,
  rowMenu,
} from './support/adminMocks';
import type { ProvisioningMock } from './support/adminMocks';

const PERSON_HASH = '76gzqgp4byjl6dje';
const USER_ID = `u_${PERSON_HASH}`;
const EMAIL = 'alice@example.com';

const SEED = {
  users: [{ user_id: USER_ID, apps: ['urls4irl'], topic_patterns: [`urls4irl-${PERSON_HASH}-*`] }],
  people: [{ person_hash: PERSON_HASH, email: EMAIL, created_at: '2026-07-19T18:12:03Z' }],
  apps: [appWire({ appId: 'urls4irl', displayName: 'URLs4IRL', description: 'Shared URL app' })],
};

type TableName = 'users' | 'people' | 'apps';

/** Each Provisioning table's section: Users and People share a row label (the email). */
function section(page: Page, name: TableName): Locator {
  return page.locator(`section.${name}-table`);
}

async function setup(
  page: Page,
  viewport: { width: number; height: number },
): Promise<ProvisioningMock> {
  await page.setViewportSize(viewport);
  await mockDelivery(page);
  const mock = await mockProvisioning(page, SEED);
  await page.goto('/');
  await expect(section(page, 'users').getByRole('heading', { name: 'Users' })).toBeVisible();
  await expect(section(page, 'people').getByRole('heading', { name: 'People' })).toBeVisible();
  await expect(section(page, 'apps').getByRole('heading', { name: 'Apps' })).toBeVisible();
  return mock;
}

function kebabIn({ page, name, row }: { page: Page; name: TableName; row: string }): Locator {
  return section(page, name).getByRole('button', { name: `Actions for ${row}`, exact: true });
}

async function openMenuIn({
  page,
  name,
  row,
}: {
  page: Page;
  name: TableName;
  row: string;
}): Promise<Locator> {
  await kebabIn({ page, name, row }).click();
  const menu = rowMenu({ page, row });
  await expect(menu).toBeVisible();
  return menu;
}

test.describe('Provisioning at 420px', () => {
  let mock: ProvisioningMock;

  test.beforeEach(async ({ page }) => {
    mock = await setup(page, { width: 420, height: 900 });
  });

  test('one kebab per row, no inline buttons, no page overflow', async ({ page }) => {
    for (const name of ['users', 'people', 'apps'] as const) {
      await expect(section(page, name).locator('tbody tr')).toHaveCount(1);
      await expect(section(page, name).locator('tbody .row-menu__trigger')).toHaveCount(1);
      await expect(section(page, name).locator('tbody button:not(.row-menu__trigger)')).toHaveCount(
        0,
      );
    }
    await expectNoPageOverflow(page);
  });

  test('menu items are touch targets and the menu is keyboard navigable', async ({ page }) => {
    const menu = await openMenuIn({ page, name: 'users', row: EMAIL });
    const items = menu.getByRole('menuitem');
    // Reset password, Deprovision urls4irl, Delete.
    await expect(items).toHaveCount(3);
    for (let index = 0; index < 3; index += 1) {
      await expectTouchTarget(items.nth(index));
    }
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();

    await kebabIn({ page, name: 'users', row: EMAIL }).focus();
    await page.keyboard.press('Enter');
    await expect(menu).toBeVisible();
    await page.keyboard.press('End');
    await expect(
      menu.getByRole('menuitem', { name: `Delete ${EMAIL}`, exact: true }),
    ).toBeFocused();
    await page.keyboard.press('Home');
    await expect(items.first()).toBeFocused();
  });

  test('Escape returns focus to the kebab', async ({ page }) => {
    for (const { name, row } of [
      { name: 'users', row: EMAIL },
      { name: 'people', row: EMAIL },
      { name: 'apps', row: 'URLs4IRL' },
    ] as const) {
      const menu = await openMenuIn({ page, name, row });
      await page.keyboard.press('Escape');
      await expect(menu).toBeHidden();
      await expect(kebabIn({ page, name, row })).toBeFocused();
    }
  });

  const CONFIRM_CASES = [
    {
      label: 'Users Deprovision',
      name: 'users',
      row: EMAIL,
      item: `Deprovision urls4irl for ${EMAIL}`,
      title: 'Deprovision app access?',
      cancel: `Cancel Deprovision urls4irl for ${EMAIL}`,
      confirm: 'Confirm deprovision urls4irl',
      request: 'POST /v1/deprovision',
      body: { app_id: 'urls4irl', user_id: USER_ID },
    },
    {
      label: 'Users Delete',
      name: 'users',
      row: EMAIL,
      item: `Delete ${EMAIL}`,
      title: 'Delete user?',
      cancel: `Cancel Delete ${EMAIL}`,
      confirm: `Confirm delete ${EMAIL}`,
      request: `DELETE /v1/users/${USER_ID}`,
      body: null,
    },
    {
      label: 'People Delete',
      name: 'people',
      row: EMAIL,
      item: `Delete ${EMAIL}`,
      title: 'Delete person?',
      cancel: `Cancel Delete ${EMAIL}`,
      confirm: `Confirm delete ${EMAIL}`,
      // A person delete is a full teardown: it deletes the person's ntfy user.
      request: `DELETE /v1/users/${USER_ID}`,
      body: null,
    },
    {
      label: 'Apps Remove',
      name: 'apps',
      row: 'URLs4IRL',
      item: 'Remove URLs4IRL',
      title: 'Remove app?',
      cancel: 'Cancel Remove URLs4IRL',
      confirm: 'Confirm remove URLs4IRL',
      request: 'POST /v1/deprovision-app',
      body: { app_id: 'urls4irl' },
    },
  ] as const;

  for (const confirmCase of CONFIRM_CASES) {
    test(`${confirmCase.label}: Cancel sends nothing, Confirm sends the request`, async ({
      page,
    }) => {
      const { name, row } = confirmCase;

      let menu = await openMenuIn({ page, name, row });
      await menu.getByRole('menuitem', { name: confirmCase.item, exact: true }).click();
      const dialog = page.getByRole('dialog', { name: confirmCase.title, exact: true });
      await expect(dialog).toBeVisible();
      await dialog.getByRole('button', { name: confirmCase.cancel, exact: true }).click();
      await expect(dialog).toBeHidden();
      await expect(kebabIn({ page, name, row })).toBeFocused();
      expect(mock.mutations).toEqual([]);

      menu = await openMenuIn({ page, name, row });
      await menu.getByRole('menuitem', { name: confirmCase.item, exact: true }).click();
      await dialog.getByRole('button', { name: confirmCase.confirm, exact: true }).click();
      await expect(dialog).toBeHidden();
      await expect
        .poll(() => mock.mutations.map((request) => `${request.method} ${request.pathname}`))
        .toEqual([confirmCase.request]);
      expect(mock.mutations[0].body).toEqual(confirmCase.body);
      expect(mock.unmocked).toEqual([]);
    });
  }

  test('Apps kebab Edit opens the dialog; Escape returns focus to the same kebab', async ({
    page,
  }) => {
    const menu = await openMenuIn({ page, name: 'apps', row: 'URLs4IRL' });
    await menu.getByRole('menuitem', { name: 'Edit URLs4IRL', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Edit app urls4irl', exact: true });
    await expect(dialog).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(kebabIn({ page, name: 'apps', row: 'URLs4IRL' })).toBeFocused();
  });
});

test.describe('Provisioning at 1280px', () => {
  test.beforeEach(async ({ page }) => {
    await setup(page, { width: 1280, height: 900 });
  });

  test('inline buttons, no kebab', async ({ page }) => {
    await expect(page.locator('.row-menu__trigger')).toHaveCount(0);
    const users = section(page, 'users');
    await expect(
      users.getByRole('button', { name: `Reset password for ${EMAIL}`, exact: true }),
    ).toBeVisible();
    await expect(users.getByRole('button', { name: 'Deprovision urls4irl' })).toBeVisible();
    await expect(users.getByRole('button', { name: `Delete ${EMAIL}`, exact: true })).toBeVisible();
    await expect(
      section(page, 'people').getByRole('button', { name: `Delete ${EMAIL}`, exact: true }),
    ).toBeVisible();
    const apps = section(page, 'apps');
    await expect(apps.getByRole('button', { name: 'Edit URLs4IRL', exact: true })).toBeVisible();
    await expect(apps.getByRole('button', { name: 'Remove URLs4IRL', exact: true })).toBeVisible();
    await expectNoPageOverflow(page);
  });
});

for (const viewport of [
  { name: '420px', width: 420, height: 900 },
  { name: '1280px', width: 1280, height: 900 },
]) {
  test.describe(`row centring at ${viewport.name}`, () => {
    test('Provisioning tables centre every cell on the row', async ({ page }) => {
      await setup(page, viewport);
      for (const name of ['users', 'people', 'apps'] as const) {
        await expectCellsCenteredInRow({ row: section(page, name).locator('tbody tr').first() });
      }
    });

    test('Delivery Destinations table centres every cell on the row', async ({ page }) => {
      await page.setViewportSize(viewport);
      await mockProvisioning(page);
      await mockDelivery(page, {
        destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
      });
      await page.goto('/#delivery');
      await expectCellsCenteredInRow({
        row: page.locator('.destinations__table tbody tr').first(),
      });
    });
  });
}

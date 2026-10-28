import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import { expectNoPageOverflow, expectTouchTarget, mockDelivery } from './support/adminMocks';

const REFRESH_NAMES = [
  'Refresh status',
  'Refresh app identities',
  'Refresh heartbeats',
  'Refresh pending sends',
  'Refresh log',
];

const FUNNEL_NAME = 'Filter deliveries';

async function setup(page: Page, width: number): Promise<void> {
  await page.setViewportSize({ width, height: 900 });
  await mockDelivery(page);
  await page.goto('/#delivery');
  await expect(page.getByRole('heading', { name: 'Delivery log' })).toBeVisible();
}

function funnel(page: Page): Locator {
  return page.getByRole('button', { name: FUNNEL_NAME, exact: true });
}

function menu(page: Page): Locator {
  return page.getByRole('menu', { name: FUNNEL_NAME });
}

/** The funnel's accent dot: the computed `::after` content and background. */
async function dot(trigger: Locator): Promise<{ content: string; background: string }> {
  return trigger.evaluate((element) => {
    const style = getComputedStyle(element, '::after');
    return { content: style.content, background: style.backgroundColor };
  });
}

async function accentColor(page: Page): Promise<string> {
  // Resolve the --accent custom property to an rgb() string the way the browser paints it.
  return page.evaluate(() => {
    const probe = document.createElement('span');
    probe.style.color = 'var(--accent)';
    document.body.appendChild(probe);
    const color = getComputedStyle(probe).color;
    probe.remove();
    return color;
  });
}

test.describe('Mobile chrome at 420px', () => {
  test.beforeEach(async ({ page }) => {
    await setup(page, 420);
  });

  test('refresh buttons are icon-only with a 44px touch target', async ({ page }) => {
    for (const name of REFRESH_NAMES) {
      const button = page.getByRole('button', { name, exact: true });
      await expect(button).toBeVisible();
      expect((await button.innerText()).trim()).toBe('');
      await expect(button.locator('svg')).toHaveCount(1);
      await expectTouchTarget(button);
    }
    await expectNoPageOverflow(page);
  });

  test('the text filter buttons are replaced by the funnel', async ({ page }) => {
    await expect(page.getByRole('group', { name: 'Filter by state' })).toHaveCount(0);
    await expect(funnel(page)).toBeVisible();
    await expectTouchTarget(funnel(page));
    await expectNoPageOverflow(page);
  });

  test('mouse: open, select a non-default filter, focus returns, dot shows', async ({ page }) => {
    const trigger = funnel(page);
    await expect(trigger).not.toHaveAttribute('data-filter-active', /.*/);
    const initial = await dot(trigger);
    expect(initial.content).toBe('none');

    await trigger.click();
    await expect(menu(page)).toBeVisible();
    await expect(trigger).toHaveAttribute('aria-expanded', 'true');
    await expect(menu(page).getByRole('menuitemradio', { name: 'Dead' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
    await expect(menu(page).getByRole('menuitemradio', { name: 'Dead' })).toBeFocused();

    await menu(page).getByRole('menuitemradio', { name: 'Pending' }).click();
    await expect(menu(page)).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await expect(trigger).toHaveAttribute('data-filter-active', 'true');

    const active = await dot(trigger);
    expect(active.content).not.toBe('none');
    expect(active.background).toBe(await accentColor(page));
    await expectNoPageOverflow(page);

    // Back to the default: the dot is gone.
    await trigger.click();
    await menu(page).getByRole('menuitemradio', { name: 'Dead' }).click();
    await expect(trigger).not.toHaveAttribute('data-filter-active', /.*/);
    expect((await dot(trigger)).content).toBe('none');
  });

  test('keyboard: open, arrows, Home/End, Escape returns focus', async ({ page }) => {
    const trigger = funnel(page);
    await trigger.focus();
    await page.keyboard.press('ArrowDown');
    const items = menu(page).getByRole('menuitemradio');
    await expect(items).toHaveCount(3);
    await expect(items.nth(0)).toBeFocused();

    await page.keyboard.press('ArrowDown');
    await expect(items.nth(1)).toBeFocused();
    await page.keyboard.press('End');
    await expect(items.nth(2)).toBeFocused();
    await page.keyboard.press('ArrowDown');
    await expect(items.nth(0)).toBeFocused();
    await page.keyboard.press('ArrowUp');
    await expect(items.nth(2)).toBeFocused();
    await page.keyboard.press('Home');
    await expect(items.nth(0)).toBeFocused();

    await page.keyboard.press('Escape');
    await expect(menu(page)).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await expect(trigger).toHaveAttribute('aria-expanded', 'false');
  });

  test('keyboard: Enter on an item selects it and returns focus', async ({ page }) => {
    const trigger = funnel(page);
    await trigger.focus();
    await page.keyboard.press('Enter');
    await expect(menu(page)).toBeVisible();
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
    await expect(menu(page)).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await expect(trigger).toHaveAttribute('data-filter-active', 'true');
  });

  test('ArrowUp opens on the last item', async ({ page }) => {
    await funnel(page).focus();
    await page.keyboard.press('ArrowUp');
    await expect(menu(page).getByRole('menuitemradio').nth(2)).toBeFocused();
  });

  test('Tab dismisses the menu', async ({ page }) => {
    await funnel(page).click();
    await expect(menu(page)).toBeVisible();
    await page.keyboard.press('Tab');
    await expect(menu(page)).toHaveCount(0);
  });

  test('a press outside dismisses the menu without changing the filter', async ({ page }) => {
    const trigger = funnel(page);
    await trigger.click();
    await expect(menu(page)).toBeVisible();
    await page.getByRole('heading', { name: 'Delivery log' }).click();
    await expect(menu(page)).toHaveCount(0);
    await expect(trigger).not.toHaveAttribute('data-filter-active', /.*/);
  });

  test('the popover stays inside the viewport', async ({ page }) => {
    await funnel(page).click();
    const box = await menu(page).boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(420);
    await expectNoPageOverflow(page);
  });
});

test.describe('Mobile chrome at 1280px', () => {
  test.beforeEach(async ({ page }) => {
    await setup(page, 1280);
  });

  test('refresh buttons show text, with no icon', async ({ page }) => {
    for (const name of REFRESH_NAMES) {
      const button = page.getByRole('button', { name, exact: true });
      await expect(button).toBeVisible();
      await expect(button).toHaveText(name);
      await expect(button.locator('svg')).toHaveCount(0);
    }
  });

  test('the filter is the text button group, with no funnel', async ({ page }) => {
    await expect(funnel(page)).toHaveCount(0);
    const group = page.getByRole('group', { name: 'Filter by state' });
    await expect(group).toBeVisible();
    await expect(group.getByRole('button', { name: 'Dead' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    await expectNoPageOverflow(page);
  });
});

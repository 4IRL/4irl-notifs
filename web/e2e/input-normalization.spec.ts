import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import { destinationWire, mockDelivery, mockProvisioning } from './support/adminMocks';

// Input normalization in a real browser: lowercase-as-you-type on the add forms (useLowercaseInput)
// and https:// prepending on the probe URL (withHttpsScheme). The APIs are mocked at the network
// layer, so no stack is needed.

const VIEWPORTS = [
  { label: '1280px', width: 1280, height: 900 },
  { label: '420px', width: 420, height: 900 },
];

async function selection(input: Locator): Promise<number | null> {
  return input.evaluate((element) => (element as HTMLInputElement).selectionStart);
}

/** Types mixed case, then checks the value, the caret and typing after a mid-value edit. */
async function expectLowercaseTyping(input: Locator): Promise<void> {
  await input.click();
  await input.pressSequentially('Urls4IRL');
  await expect(input).toHaveValue('urls4irl');
  expect(await selection(input)).toBe('urls4irl'.length);

  // Mid-value: move the caret left, type a capital; the next character must land after it.
  await input.fill('');
  await input.pressSequentially('ab');
  await input.press('ArrowLeft');
  await input.pressSequentially('XY');
  await expect(input).toHaveValue('axyb');
  expect(await selection(input)).toBe(3);

  // Paste-like insertion mid-value keeps the caret right after the inserted text.
  await input.fill('');
  await input.pressSequentially('ad');
  await input.press('ArrowLeft');
  await input.evaluate(() => document.execCommand('insertText', false, 'BC'));
  await expect(input).toHaveValue('abcd');
  expect(await selection(input)).toBe(3);
  await input.pressSequentially('Z');
  await expect(input).toHaveValue('abczd');
}

/** Holds an IME composition open via CDP, returning helpers to update and commit it. */
async function startComposition({ page, input }: { page: Page; input: Locator }) {
  await input.click();
  const client = await page.context().newCDPSession(page);
  return {
    async update(text: string) {
      await client.send('Input.imeSetComposition', {
        text,
        selectionStart: text.length,
        selectionEnd: text.length,
      });
    },
    async commit(text: string) {
      await client.send('Input.insertText', { text });
    },
  };
}

for (const viewport of VIEWPORTS) {
  test.describe(`input normalization at ${viewport.label}`, () => {
    test.beforeEach(async ({ page }) => {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
    });

    test('add app: lowercase typing, caret and posted body', async ({ page }) => {
      const mock = await mockProvisioning(page);
      await mockDelivery(page);
      await page.goto('/');
      const addSection = page
        .locator('section')
        .filter({ has: page.getByRole('heading', { name: 'Add an app' }) });
      const appId = addSection.getByLabel('App ID');
      await expectLowercaseTyping(appId);
      await appId.fill('');
      await appId.pressSequentially('TaskTracker');
      await addSection.getByLabel('Display name').fill('Task Tracker');
      await addSection.getByRole('button', { name: 'Add app' }).click();
      await expect.poll(() => mock.mutations.length).toBeGreaterThan(0);
      const create = mock.mutations.find((request) => request.pathname === '/apps');
      expect((create?.body as { app_id: string }).app_id).toBe('tasktracker');
    });

    test('provision form: lowercase typing, caret and posted body', async ({ page }) => {
      const mock = await mockProvisioning(page);
      await mockDelivery(page);
      const provisions: unknown[] = [];
      await page.route('**/v1/provision', async (route) => {
        provisions.push(route.request().postDataJSON());
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            user_id: 'u_1',
            app_id: 'urls4irl',
            password: 'pw-e2e-0123456789',
          }),
        });
      });
      await page.goto('/');
      const section = page
        .locator('section')
        .filter({ has: page.getByRole('heading', { name: 'Provision a user into an app' }) });
      const appId = section.getByLabel('App ID');
      await expectLowercaseTyping(appId);
      await appId.fill('');
      await appId.pressSequentially('Urls4IRL');
      await section.getByLabel('Email').fill('alice@example.com');
      await section.getByRole('button', { name: 'Provision', exact: true }).click();
      await expect.poll(() => provisions.length).toBe(1);
      expect((provisions[0] as { app_id: string }).app_id).toBe('urls4irl');
      expect(mock.unmocked).toEqual([]);
    });

    test('add destination: lowercase name, caret and posted body', async ({ page }) => {
      await mockProvisioning(page);
      const mock = await mockDelivery(page);
      await page.goto('/#delivery');
      await page.getByRole('button', { name: 'Add destination', exact: true }).click();
      const form = page.getByRole('region', { name: 'Add destination' });
      const name = form.getByLabel('Name', { exact: true });
      await expectLowercaseTyping(name);
      await name.fill('');
      await name.pressSequentially('Hook1');
      await form.getByLabel('Type', { exact: true }).selectOption('webhook');
      await form.getByLabel('Webhook URL').fill('https://hooks.example.com/in');
      await form.getByLabel('Signing secret').fill('s'.repeat(40));
      await form.getByRole('button', { name: 'Add destination', exact: true }).click();
      await expect.poll(() => mock.mutations.length).toBe(1);
      const create = mock.mutations.find(
        (request) =>
          request.method === 'POST' && request.pathname === '/delivery/v1/admin/destinations',
      );
      expect((create?.body as { name: string }).name).toBe('hook1');
    });

    test('add route: lowercase app and channel patterns and posted body', async ({ page }) => {
      await mockProvisioning(page);
      const mock = await mockDelivery(page, {
        destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
      });
      await page.goto('/#delivery');
      await page.getByRole('button', { name: 'Add route', exact: true }).click();
      const form = page.getByRole('form', { name: 'Add route' });
      const app = form.getByLabel('App pattern');
      const channel = form.getByLabel('Channel pattern');
      await expectLowercaseTyping(app);
      await app.fill('');
      await app.pressSequentially('Urls4IRL');
      await channel.pressSequentially('Alerts*');
      await expect(channel).toHaveValue('alerts*');
      await form.getByRole('checkbox', { name: 'hook1' }).check();
      await form.getByRole('button', { name: 'Add route', exact: true }).click();
      await expect.poll(() => mock.mutations.length).toBe(1);
      const create = mock.mutations.find(
        (request) => request.pathname === '/delivery/v1/admin/routes',
      );
      expect(create?.body).toMatchObject({ app_pattern: 'urls4irl', channel_pattern: 'alerts*' });
    });

    test.describe('heartbeat add form', () => {
      async function openForm(page: Page) {
        await mockProvisioning(page);
        const mock = await mockDelivery(page, {
          destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
        });
        await page.goto('/#delivery');
        const section = page.getByRole('region', { name: 'Heartbeats' });
        await expect(section.getByRole('button', { name: 'Add check', exact: true })).toBeEnabled();
        await section.getByRole('button', { name: 'Add check', exact: true }).click();
        const form = section.getByRole('form', { name: 'Add check' });
        return { mock, section, form };
      }

      async function fillProbe({ form, url }: { form: Locator; url: string }) {
        await form.getByRole('radio', { name: 'Website probe' }).check();
        await form.getByLabel('App ID').pressSequentially('MineCrafting');
        await form.getByLabel('Check name').pressSequentially('Site_Up');
        await form.getByRole('checkbox', { name: /^hook1/ }).check();
        await form.getByLabel('URL').pressSequentially(url);
      }

      test('lowercase app id and check name, and the posted body', async ({ page }) => {
        const { mock, section, form } = await openForm(page);
        await expectLowercaseTyping(form.getByLabel('App ID'));
        await expectLowercaseTyping(form.getByLabel('Check name'));
        await form.getByLabel('App ID').fill('');
        await form.getByLabel('Check name').fill('');
        await form.getByLabel('App ID').pressSequentially('MineCrafting');
        await form.getByLabel('Check name').pressSequentially('Daily_Report');
        await form.getByRole('checkbox', { name: /^hook1/ }).check();
        await form.getByRole('button', { name: 'Add check', exact: true }).click();
        await expect(section.getByText('Added minecrafting/daily_report.')).toBeVisible();
        const create = mock.mutations.find((request) => request.method === 'POST');
        expect(create?.body).toMatchObject({ app_id: 'minecrafting', name: 'daily_report' });
      });

      test('probe URL: bare host + Tab prepends https:// and posts it', async ({ page }) => {
        const { mock, section, form } = await openForm(page);
        await fillProbe({ form, url: 'Status.Example.com/health' });
        await form.getByLabel('URL').press('Tab');
        await expect(form.getByLabel('URL')).toHaveValue('https://Status.Example.com/health');
        await form.getByRole('button', { name: 'Add check', exact: true }).click();
        await expect(section.getByText('Added minecrafting/site_up.')).toBeVisible();
        const create = mock.mutations.find((request) => request.method === 'POST');
        expect((create?.body as { probe: { url: string } }).probe.url).toBe(
          'https://Status.Example.com/health',
        );
      });

      test('probe URL: bare host + Enter (no blur) posts the prefixed URL', async ({ page }) => {
        const { mock, section, form } = await openForm(page);
        await fillProbe({ form, url: 'status.example.com/health' });
        await form.getByLabel('URL').press('Enter');
        await expect(section.getByText('Added minecrafting/site_up.')).toBeVisible();
        const create = mock.mutations.find((request) => request.method === 'POST');
        expect((create?.body as { probe: { url: string } }).probe.url).toBe(
          'https://status.example.com/health',
        );
      });

      test('probe URL: Tab then Save click posts the prefixed URL', async ({ page }) => {
        const { mock, section, form } = await openForm(page);
        await fillProbe({ form, url: 'status.example.com' });
        await page.keyboard.press('Tab');
        await form.getByRole('button', { name: 'Add check', exact: true }).click();
        await expect(section.getByText('Added minecrafting/site_up.')).toBeVisible();
        const create = mock.mutations.find((request) => request.method === 'POST');
        expect((create?.body as { probe: { url: string } }).probe.url).toBe(
          'https://status.example.com',
        );
      });

      test('probe URL: http:// stays and shows the scheme error', async ({ page }) => {
        const { mock, form } = await openForm(page);
        await fillProbe({ form, url: 'http://example.com' });
        await form.getByLabel('URL').press('Tab');
        await expect(form.getByLabel('URL')).toHaveValue('http://example.com');
        await form.getByRole('button', { name: 'Add check', exact: true }).click();
        const url = form.getByLabel('URL');
        await expect(url).toHaveAttribute('aria-invalid', 'true');
        await expect(url).toBeFocused();
        expect(mock.mutations).toEqual([]);
      });
    });

    test('IME composition is left alone until it ends, then lowercased', async ({ page }) => {
      await mockProvisioning(page);
      await mockDelivery(page);
      await page.goto('/#delivery');
      await page.getByRole('button', { name: 'Add destination', exact: true }).click();
      const form = page.getByRole('region', { name: 'Add destination' });
      const name = form.getByLabel('Name', { exact: true });
      const ime = await startComposition({ page, input: name });
      await ime.update('ABC');
      await expect(name).toHaveValue('ABC');
      await ime.commit('ABC');
      await expect(name).toHaveValue('abc');
      expect(await selection(name)).toBe(3);
    });

    test('a submit while composing (compositionend never fires) posts a lowercase name', async ({
      page,
    }) => {
      await mockProvisioning(page);
      const mock = await mockDelivery(page);
      await page.goto('/#delivery');
      await page.getByRole('button', { name: 'Add destination', exact: true }).click();
      const form = page.getByRole('region', { name: 'Add destination' });
      const name = form.getByLabel('Name', { exact: true });
      const ime = await startComposition({ page, input: name });
      await ime.update('HOOK');
      await form.getByLabel('Type', { exact: true }).selectOption('webhook');
      await form.getByLabel('Webhook URL').fill('https://hooks.example.com/in');
      await form.getByLabel('Signing secret').fill('s'.repeat(40));
      await form.getByRole('button', { name: 'Add destination', exact: true }).click();
      await expect.poll(() => mock.mutations.length).toBe(1);
      const create = mock.mutations.find(
        (request) =>
          request.method === 'POST' && request.pathname === '/delivery/v1/admin/destinations',
      );
      expect((create?.body as { name: string }).name).toBe('hook');
    });
  });
}

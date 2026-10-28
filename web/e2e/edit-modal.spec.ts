import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import {
  MINTED_TOKEN,
  appWire,
  destinationWire,
  expectDialogInViewport,
  expectNoPageOverflow,
  expectTouchTarget,
  heartbeatWire,
  mockDelivery,
  mockProvisioning,
  openRowMenu,
  rowKebab,
  routeWire,
} from './support/adminMocks';
import type { RecordedRequest } from './support/adminMocks';

// The shared edit dialog in a real browser, on a desktop and a 420px phone viewport: opening from
// the row, initial focus, Save, the discard guard, the in-dialog confirm steps, focus return, the
// focus trap and the inert states that jsdom cannot enforce. Both APIs are mocked at the network
// layer, so no stack is needed.

const VIEWPORTS = [
  { name: 'desktop', width: 1280, height: 900, compact: false },
  { name: 'mobile 420px', width: 420, height: 900, compact: true },
] as const;

const ROTATED_SECRET = 'rotated_signing_secret_e2e_0123456789abcdef';
const TAB_PRESSES = 10;

interface Fixture {
  mutations: RecordedRequest[];
  unmocked: string[];
}

/** One of the four edit forms: how to reach it, what it focuses, how to dirty it and what a save does. */
interface FormCase {
  name: string;
  dialogName: string;
  /** Opens the page with this form's row and returns the request logs to assert on. */
  setup: (page: Page) => Promise<Fixture>;
  /** The control that opened the dialog and takes focus back when it closes. */
  trigger: (args: { page: Page; compact: boolean }) => Locator;
  open: (args: { page: Page; compact: boolean }) => Promise<void>;
  initialFocus: (dialog: Locator) => Locator;
  /** Changes one field and leaves focus on it. */
  edit: (dialog: Locator) => Promise<void>;
  save: (dialog: Locator) => Locator;
  expectSaved: (args: { page: Page; mutations: RecordedRequest[] }) => Promise<void>;
}

function patches(mutations: RecordedRequest[]): RecordedRequest[] {
  return mutations.filter((request) => request.method === 'PATCH');
}

/** Opens a row's editor: its Edit button on a roomy table, its kebab menu on a compact one. */
async function openFromRow({
  page,
  compact,
  row,
  editName,
}: {
  page: Page;
  compact: boolean;
  row: string;
  editName: string;
}): Promise<void> {
  if (compact) {
    const menu = await openRowMenu({ page, row });
    await menu.getByRole('menuitem', { name: editName, exact: true }).click();
  } else {
    await page.getByRole('button', { name: editName, exact: true }).click();
  }
}

function rowTrigger({
  page,
  compact,
  row,
  editName,
}: {
  page: Page;
  compact: boolean;
  row: string;
  editName: string;
}): Locator {
  return compact
    ? rowKebab({ page, row })
    : page.getByRole('button', { name: editName, exact: true });
}

const HEARTBEAT_ROW = 'urls4irl/nightly_backup';

async function setupHeartbeat(page: Page): Promise<Fixture> {
  await mockProvisioning(page);
  const mock = await mockDelivery(page, {
    destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
    heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })],
  });
  await page.goto('/#delivery');
  await expect(page.locator('.heartbeats__table tbody tr')).toHaveCount(1);
  return mock;
}

const HEARTBEAT_CASE: FormCase = {
  name: 'heartbeat',
  dialogName: `Edit ${HEARTBEAT_ROW}`,
  setup: setupHeartbeat,
  trigger: ({ page, compact }) =>
    rowTrigger({ page, compact, row: HEARTBEAT_ROW, editName: `Edit ${HEARTBEAT_ROW}` }),
  open: ({ page, compact }) =>
    openFromRow({ page, compact, row: HEARTBEAT_ROW, editName: `Edit ${HEARTBEAT_ROW}` }),
  initialFocus: (dialog) => dialog.getByLabel('Period', { exact: true }),
  edit: async (dialog) => {
    await dialog.getByLabel('Grace', { exact: true }).fill('3');
  },
  save: (dialog) => dialog.getByRole('button', { name: 'Save', exact: true }),
  expectSaved: async ({ page, mutations }) => {
    expect(patches(mutations)).toHaveLength(1);
    expect(patches(mutations)[0].pathname).toBe('/delivery/v1/admin/heartbeats/1');
    expect(patches(mutations)[0].body).toEqual({ grace_seconds: 10800 });
    await expect(page.locator('.heartbeats__table tbody tr')).toContainText('grace 3 h');
    await expect(page.getByText('Saved urls4irl/nightly_backup.')).toBeVisible();
  },
};

const ROUTE_CASE: FormCase = {
  name: 'route',
  dialogName: 'Edit route 1',
  setup: async (page) => {
    await mockProvisioning(page);
    const mock = await mockDelivery(page, {
      destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
      routes: [routeWire({ id: 1 })],
    });
    await page.goto('/#delivery');
    await expect(page.locator('.routes__table tbody tr')).toHaveCount(1);
    return mock;
  },
  trigger: ({ page, compact }) =>
    rowTrigger({ page, compact, row: 'route 1', editName: 'Edit route 1' }),
  open: ({ page, compact }) =>
    openFromRow({ page, compact, row: 'route 1', editName: 'Edit route 1' }),
  initialFocus: (dialog) => dialog.getByLabel('App pattern'),
  edit: async (dialog) => {
    await dialog.getByLabel('Channel pattern').fill('alerts_new*');
  },
  save: (dialog) => dialog.getByRole('button', { name: 'Save route 1', exact: true }),
  expectSaved: async ({ page, mutations }) => {
    expect(patches(mutations)).toHaveLength(1);
    expect(patches(mutations)[0].pathname).toBe('/delivery/v1/admin/routes/1');
    expect(patches(mutations)[0].body).toMatchObject({ channel_pattern: 'alerts_new*' });
    await expect(page.locator('.routes__table tbody tr')).toContainText('alerts_new*');
  },
};

const DESTINATION_CASE: FormCase = {
  name: 'destination',
  dialogName: 'Edit destination family_ntfy',
  setup: async (page) => {
    await mockProvisioning(page);
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({
          name: 'family_ntfy',
          type: 'ntfy',
          config: { topic: 'family' },
          secrets: { access_token: '****' },
        }),
      ],
    });
    await page.goto('/#delivery');
    await expect(page.locator('.destinations__table tbody tr')).toHaveCount(1);
    return mock;
  },
  trigger: ({ page, compact }) =>
    rowTrigger({ page, compact, row: 'family_ntfy', editName: 'Edit family_ntfy' }),
  open: ({ page, compact }) =>
    openFromRow({ page, compact, row: 'family_ntfy', editName: 'Edit family_ntfy' }),
  initialFocus: (dialog) => dialog.getByLabel('ntfy topic'),
  edit: async (dialog) => {
    await dialog.getByLabel('ntfy topic').fill('family-new');
  },
  save: (dialog) => dialog.getByRole('button', { name: 'Save family_ntfy', exact: true }),
  expectSaved: async ({ page, mutations }) => {
    expect(patches(mutations)).toHaveLength(1);
    expect(patches(mutations)[0].pathname).toBe('/delivery/v1/admin/destinations/family_ntfy');
    expect(patches(mutations)[0].body).toEqual({ config: { topic: 'family-new' } });
    await expect(page.locator('.destinations__table tbody tr')).toContainText('topic: family-new');
  },
};

async function setupApp(page: Page): Promise<Fixture> {
  await mockDelivery(page);
  const mock = await mockProvisioning(page, {
    apps: [appWire({ appId: 'urls4irl', displayName: 'URLs4IRL', description: 'Shared URL app' })],
  });
  await page.goto('/');
  await expect(page.locator('section.apps-table tbody tr')).toContainText('URLs4IRL');
  return mock;
}

const APP_ROW = 'URLs4IRL';

const APP_CASE: FormCase = {
  name: 'app',
  dialogName: 'Edit app urls4irl',
  setup: setupApp,
  // Prefix matches: the Save case renames the app, which renames its Edit button and its kebab.
  trigger: ({ page, compact }) =>
    compact
      ? page.getByRole('button', { name: /^Actions for URLs4IRL/ })
      : page.getByRole('button', { name: /^Edit URLs4IRL/ }),
  open: ({ page, compact }) =>
    openFromRow({ page, compact, row: APP_ROW, editName: `Edit ${APP_ROW}` }),
  initialFocus: (dialog) => dialog.getByLabel('Display name'),
  edit: async (dialog) => {
    await dialog.getByLabel('Display name').fill('URLs4IRL Renamed');
  },
  save: (dialog) => dialog.getByRole('button', { name: 'Save', exact: true }),
  expectSaved: async ({ page, mutations }) => {
    expect(patches(mutations)).toHaveLength(1);
    expect(patches(mutations)[0].pathname).toBe('/apps/urls4irl');
    expect(patches(mutations)[0].body).toEqual({
      display_name: 'URLs4IRL Renamed',
      description: 'Shared URL app',
    });
    await expect(page.locator('section.apps-table tbody tr')).toContainText('URLs4IRL Renamed');
    // Announced after the dialog has closed (the live region sits under the lifted inert).
    await expect(page.getByRole('status').filter({ hasText: 'Saved urls4irl.' })).toBeVisible();
  },
};

const CASES = [HEARTBEAT_CASE, ROUTE_CASE, DESTINATION_CASE, APP_CASE];

/** Presses `key` repeatedly and asserts focus stays in the dialog (and out of `inertSelector`). */
async function expectFocusTrapped({
  page,
  key,
  inertSelector = null,
}: {
  page: Page;
  key: 'Tab' | 'Shift+Tab';
  inertSelector?: string | null;
}): Promise<void> {
  for (let press = 0; press < TAB_PRESSES; press += 1) {
    await page.keyboard.press(key);
    const where = await page.evaluate((inert) => {
      const dialog = document.querySelector('[role="dialog"]');
      const active = document.activeElement;
      return {
        inside: dialog !== null && active !== null && dialog.contains(active),
        inInert: inert !== null && active !== null && active.closest(inert) !== null,
      };
    }, inertSelector);
    expect(where.inside, `${key} press ${press + 1} left the dialog`).toBe(true);
    expect(where.inInert, `${key} press ${press + 1} reached inert content`).toBe(false);
  }
}

/** Asserts a locator's box lies wholly inside the viewport. */
async function expectWithinViewport({ page, locator }: { page: Page; locator: Locator }) {
  await expect(locator).toBeVisible();
  const box = await locator.boundingBox();
  const viewport = page.viewportSize();
  if (box === null || viewport === null) {
    throw new Error('element or viewport has no size');
  }
  expect(box.x).toBeGreaterThanOrEqual(-1);
  expect(box.y).toBeGreaterThanOrEqual(-1);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 1);
  expect(box.y + box.height).toBeLessThanOrEqual(viewport.height + 1);
}

/** A reference to the focused element, to prove later that focus came back to the same node. */
async function focusedHandle(page: Page) {
  return page.evaluateHandle(() => document.activeElement);
}

async function isStillFocused({
  page,
  handle,
}: {
  page: Page;
  handle: Awaited<ReturnType<typeof focusedHandle>>;
}) {
  return page.evaluate((element) => element === document.activeElement, handle);
}

/** Two animation frames: lets a deferred focus move (an effect, a timer) land before asserting. */
async function settle(page: Page): Promise<void> {
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
      }),
  );
}

for (const viewport of VIEWPORTS) {
  test.describe(`edit dialog at ${viewport.name}`, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } });
    const { compact } = viewport;

    for (const formCase of CASES) {
      test.describe(`${formCase.name} form`, () => {
        test('opens focused on its first field, saves, closes and hands focus back', async ({
          page,
        }) => {
          const fixture = await formCase.setup(page);
          await formCase.open({ page, compact });
          const dialog = page.getByRole('dialog', { name: formCase.dialogName, exact: true });
          await expect(dialog).toBeVisible();
          await expect(formCase.initialFocus(dialog)).toBeFocused();
          await expectDialogInViewport(page);

          const dialogBox = await dialog.boundingBox();
          if (dialogBox === null) {
            throw new Error('dialog has no bounding box');
          }
          if (compact) {
            // A full-height sheet: it fills the phone viewport and every control is a touch target.
            expect(dialogBox.width).toBeGreaterThanOrEqual(viewport.width - 1);
            expect(dialogBox.height).toBeGreaterThanOrEqual(viewport.height - 1);
            await expectTouchTarget(formCase.save(dialog));
            // Both the header X and the form's own Close (its cancel) are named "Close".
            const closers = dialog.getByRole('button', { name: 'Close', exact: true });
            expect(await closers.count()).toBeGreaterThanOrEqual(2);
            for (const closer of await closers.all()) {
              await expectTouchTarget(closer);
            }
            await expectNoPageOverflow(page);
          } else {
            expect(dialogBox.width).toBeLessThan(viewport.width);
          }

          await formCase.edit(dialog);
          await formCase.save(dialog).click();
          await expect(dialog).toBeHidden();
          await formCase.expectSaved({ page, mutations: fixture.mutations });
          await expect(formCase.trigger({ page, compact })).toBeFocused();
          expect(fixture.unmocked).toEqual([]);
        });

        test('Escape on a dirty form shows the guard; Keep editing restores focus; Discard sends nothing', async ({
          page,
        }) => {
          const fixture = await formCase.setup(page);
          await formCase.open({ page, compact });
          const dialog = page.getByRole('dialog', { name: formCase.dialogName, exact: true });
          await expect(dialog).toBeVisible();

          // A clean form closes straight away on Escape.
          await page.keyboard.press('Escape');
          await expect(dialog).toBeHidden();
          await expect(formCase.trigger({ page, compact })).toBeFocused();

          await formCase.open({ page, compact });
          await expect(dialog).toBeVisible();
          await formCase.edit(dialog);
          const editedField = await focusedHandle(page);
          // The form reports "dirty" to the dialog from a passive effect; let it land so the
          // Escape below is judged against the edited state, not the pre-edit one.
          await settle(page);
          await page.keyboard.press('Escape');

          const keep = dialog.getByRole('button', { name: 'Keep editing', exact: true });
          const discard = dialog.getByRole('button', { name: 'Discard changes', exact: true });
          await expect(dialog.getByRole('alert')).toHaveText('Discard unsaved changes?');
          await expect(keep).toBeFocused();
          await expect(discard).toBeVisible();
          // The form is inert behind the guard: Tab and Shift+Tab cycle among the guard's buttons.
          await expectFocusTrapped({ page, key: 'Tab', inertSelector: '.edit-modal' });
          await expectFocusTrapped({ page, key: 'Shift+Tab', inertSelector: '.edit-modal' });
          await keep.focus();

          // Keep editing: the guard goes, and focus returns to the field that had it.
          await keep.click();
          await expect(discard).toBeHidden();
          await expect.poll(() => isStillFocused({ page, handle: editedField })).toBe(true);
          expect(fixture.mutations).toEqual([]);

          // Escape again, then Discard: the dialog closes with no request, focus on the trigger.
          await page.keyboard.press('Escape');
          await expect(discard).toBeVisible();
          await discard.click();
          await expect(dialog).toBeHidden();
          await expect(formCase.trigger({ page, compact })).toBeFocused();
          expect(fixture.mutations).toEqual([]);
          expect(fixture.unmocked).toEqual([]);
        });

        test('Tab and Shift+Tab never leave the dialog and the background does not scroll', async ({
          page,
        }) => {
          const fixture = await formCase.setup(page);
          await formCase.open({ page, compact });
          const dialog = page.getByRole('dialog', { name: formCase.dialogName, exact: true });
          await expect(dialog).toBeVisible();

          await expectFocusTrapped({ page, key: 'Tab' });
          await expectFocusTrapped({ page, key: 'Shift+Tab' });

          // The page behind stays put under wheel, keys and focus movement.
          // Make the page scrollable first, so an unlocked background would visibly move.
          await page.evaluate(() => {
            document.body.style.minHeight = '4000px';
            window.scrollTo(0, 300);
          });
          const scrollBefore = await page.evaluate(() => window.scrollY);
          expect(scrollBefore).toBeGreaterThan(0);
          await page.mouse.move(viewport.width - 4, 4);
          await page.mouse.wheel(0, 800);
          await page.keyboard.press('PageDown');
          await settle(page);
          expect(await page.evaluate(() => window.scrollY)).toBe(scrollBefore);

          if (!compact) {
            // A click on the backdrop dismisses a clean form and hands focus back.
            await page.mouse.click(4, 4);
            await expect(dialog).toBeHidden();
            await expect(formCase.trigger({ page, compact })).toBeFocused();
          }
          expect(fixture.unmocked).toEqual([]);
        });
      });
    }

    test.describe('destination Rotate', () => {
      async function openWebhook(page: Page): Promise<{ fixture: Fixture; dialog: Locator }> {
        await mockProvisioning(page);
        const fixture = await mockDelivery(page, {
          destinations: [
            destinationWire({
              name: 'hook1',
              type: 'webhook',
              secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
            }),
          ],
        });
        await page.goto('/#delivery');
        await expect(page.locator('.destinations__table tbody tr')).toHaveCount(1);
        await openFromRow({ page, compact, row: 'hook1', editName: 'Edit hook1' });
        const dialog = page.getByRole('dialog', { name: 'Edit destination hook1', exact: true });
        await expect(dialog).toBeVisible();
        return { fixture, dialog };
      }

      test('the confirm step is in the dialog: Escape and Cancel only cancel it and focus returns to Rotate', async ({
        page,
      }) => {
        const { fixture, dialog } = await openWebhook(page);
        const rotate = dialog.getByRole('button', { name: 'Rotate hook1', exact: true });
        const confirm = dialog.getByRole('button', { name: 'Confirm rotate hook1', exact: true });
        const cancel = dialog.getByRole('button', { name: 'Cancel', exact: true });

        await rotate.click();
        await expect(confirm).toBeVisible();
        await expect(cancel).toBeFocused();
        // No second dialog is stacked, and the form is inert behind the confirm.
        await expect(page.getByRole('dialog')).toHaveCount(1);
        await expectFocusTrapped({ page, key: 'Tab', inertSelector: '.edit-modal' });
        await expectFocusTrapped({ page, key: 'Shift+Tab', inertSelector: '.edit-modal' });

        await page.keyboard.press('Escape');
        await expect(confirm).toBeHidden();
        await expect(dialog).toBeVisible();
        await expect(rotate).toBeFocused();

        await rotate.click();
        await expect(confirm).toBeVisible();
        await cancel.click();
        await expect(confirm).toBeHidden();
        await expect(rotate).toBeFocused();
        expect(fixture.mutations).toEqual([]);

        if (compact) {
          // The message and both buttons are visible together, even with the body scrolled down.
          await rotate.click();
          await dialog.locator('.modal__body').evaluate((element) => {
            element.scrollTop = element.scrollHeight;
          });
          await expectWithinViewport({ page, locator: dialog.getByRole('alert') });
          await expectWithinViewport({ page, locator: confirm });
          await expectWithinViewport({ page, locator: cancel });
          await expectTouchTarget(confirm);
          await expectTouchTarget(cancel);
          await cancel.click();
        }
        expect(fixture.unmocked).toEqual([]);
      });

      test('a clean Rotate closes the dialog and the secret appears in its row with focus on it; reopening Edit keeps focus in the dialog', async ({
        page,
      }) => {
        const { fixture, dialog } = await openWebhook(page);
        await dialog.getByRole('button', { name: 'Rotate hook1', exact: true }).click();
        await dialog.getByRole('button', { name: 'Confirm rotate hook1', exact: true }).click();

        await expect(dialog).toBeHidden();
        await expect(page.getByText(ROTATED_SECRET)).toBeVisible();
        await expect(
          page.locator('.destinations__reveal-cell .destinations__reveal'),
        ).toBeFocused();
        const rotates = fixture.mutations.filter((request) =>
          request.pathname.endsWith('/rotate-secret'),
        );
        expect(rotates).toHaveLength(1);
        expect(rotates[0].body).toEqual({ overlap_hours: 24 });

        // Reopen Edit while the reveal is still live in the row: the dialog takes focus and a
        // deferred focus move (the reveal's own effect, the dialog's open effect) must not
        // push it back behind the inert page or onto the body.
        await openFromRow({ page, compact, row: 'hook1', editName: 'Edit hook1' });
        const reopened = page.getByRole('dialog', { name: 'Edit destination hook1', exact: true });
        await expect(reopened).toBeVisible();
        await expect(reopened.locator(':focus')).toHaveCount(1);
        await settle(page);
        await expect(reopened.locator(':focus')).toHaveCount(1);
        const focusedInside = await page.evaluate(() => {
          const active = document.activeElement;
          return active !== document.body && active?.closest('[role="dialog"]') !== null;
        });
        expect(focusedInside).toBe(true);
        expect(fixture.unmocked).toEqual([]);
      });

      test('Rotate with typed edits keeps the dialog open with the reveal inside it', async ({
        page,
      }) => {
        const { fixture, dialog } = await openWebhook(page);
        await dialog.getByLabel('Webhook URL').fill('https://hooks.example.com/changed');
        await dialog.getByRole('button', { name: 'Rotate hook1', exact: true }).click();
        await dialog.getByRole('button', { name: 'Confirm rotate hook1', exact: true }).click();

        await expect(dialog.getByText(ROTATED_SECRET)).toBeVisible();
        await expect(dialog.locator('.destinations__reveal')).toBeFocused();
        await expect(dialog).toBeVisible();
        // Nothing was saved: the typed URL is still unsaved and guarded.
        expect(fixture.mutations.filter((request) => request.method === 'PATCH')).toEqual([]);
        await page.keyboard.press('Escape');
        await expect(dialog.getByRole('alert')).toHaveText('Discard unsaved changes?');
        await dialog.getByRole('button', { name: 'Keep editing', exact: true }).click();
        await expect(dialog).toBeVisible();
        expect(fixture.unmocked).toEqual([]);
      });
    });

    test.describe('app token', () => {
      async function openApp(page: Page): Promise<{ fixture: Fixture; dialog: Locator }> {
        const fixture = await setupApp(page);
        await APP_CASE.open({ page, compact });
        const dialog = page.getByRole('dialog', { name: 'Edit app urls4irl', exact: true });
        await expect(dialog).toBeVisible();
        return { fixture, dialog };
      }

      test('Revoke & re-mint goes through the in-dialog confirm and focus returns to it', async ({
        page,
      }) => {
        const { fixture, dialog } = await openApp(page);
        const trigger = dialog.getByRole('button', { name: 'Revoke & re-mint', exact: true });
        const confirm = dialog.getByRole('button', {
          name: 'Confirm revoke & re-mint',
          exact: true,
        });
        const cancel = dialog.getByRole('button', { name: 'Cancel', exact: true });

        await trigger.click();
        await expect(confirm).toBeVisible();
        await expect(cancel).toBeFocused();
        await expect(page.getByRole('dialog')).toHaveCount(1);
        await expectFocusTrapped({ page, key: 'Tab', inertSelector: '.edit-modal' });
        await expectFocusTrapped({ page, key: 'Shift+Tab', inertSelector: '.edit-modal' });

        // Escape cancels only the confirm step.
        await page.keyboard.press('Escape');
        await expect(confirm).toBeHidden();
        await expect(dialog).toBeVisible();
        await expect(trigger).toBeFocused();

        await trigger.click();
        await cancel.click();
        await expect(trigger).toBeFocused();
        expect(fixture.mutations).toEqual([]);

        await trigger.click();
        await confirm.click();
        await expect(dialog.getByText(MINTED_TOKEN)).toBeVisible();
        await expect(trigger).toBeFocused();
        const mints = fixture.mutations.filter(
          (request) => request.pathname === '/v1/provision-app',
        );
        expect(mints).toHaveLength(1);
        expect(mints[0].body).toEqual({ app_id: 'urls4irl', rotate: true });
        expect(fixture.unmocked).toEqual([]);
      });

      test('a shown token keeps the dialog open on Save and guards the close', async ({ page }) => {
        const { fixture, dialog } = await openApp(page);
        await dialog.getByRole('button', { name: 'Re-mint token', exact: true }).click();
        await expect(dialog.getByText(MINTED_TOKEN)).toBeVisible();

        await dialog.getByLabel('Display name').fill('URLs4IRL Renamed');
        await dialog.getByRole('button', { name: 'Save', exact: true }).click();
        // Saved, but the token is on screen: the dialog stays open and announces inside itself.
        await expect(dialog.getByRole('status')).toHaveText('Saved urls4irl.');
        await expect(dialog).toBeVisible();
        expect(patches(fixture.mutations)).toHaveLength(1);

        const closeAnyway = dialog.getByRole('button', { name: 'Close anyway', exact: true });
        const keepOpen = dialog.getByRole('button', { name: 'Keep open', exact: true });
        await page.keyboard.press('Escape');
        await expect(dialog.getByRole('alert')).toHaveText(
          'This token is shown once. Close without copying it?',
        );
        await expect(keepOpen).toBeFocused();
        await keepOpen.click();
        await expect(closeAnyway).toBeHidden();
        await expect(dialog.getByText(MINTED_TOKEN)).toBeVisible();

        await page.keyboard.press('Escape');
        await closeAnyway.click();
        await expect(dialog).toBeHidden();
        await expect(APP_CASE.trigger({ page, compact })).toBeFocused();
        expect(fixture.unmocked).toEqual([]);
      });
    });

    test('a row that vanishes under an open dialog disables the form and keeps focus in the dialog', async ({
      page,
    }) => {
      // The Heartbeats list quietly reloads on a 30 s tick while a row is Down, so a fake clock
      // drives the refresh that finds the row gone.
      await page.clock.install();
      await mockProvisioning(page);
      const mock = await mockDelivery(page, {
        destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
        heartbeats: [
          heartbeatWire({
            id: 1,
            name: 'nightly_backup',
            state: 'down',
            downSince: '2026-10-17T08:00:00Z',
            downReason: 'late',
          }),
        ],
      });
      await page.goto('/#delivery');
      await expect(page.locator('.heartbeats__table tbody tr')).toHaveCount(1);
      await HEARTBEAT_CASE.open({ page, compact });
      const dialog = page.getByRole('dialog', { name: HEARTBEAT_CASE.dialogName, exact: true });
      await expect(dialog).toBeVisible();
      await HEARTBEAT_CASE.edit(dialog);

      // Another operator deletes the check; the next tick reloads the list.
      await page.evaluate(() =>
        fetch('/delivery/v1/admin/heartbeats/1', { method: 'DELETE' }).then(() => undefined),
      );
      await page.clock.fastForward(35_000);

      const gone = dialog.getByRole('alert').filter({ hasText: 'This item was removed.' });
      await expect(gone).toBeVisible();
      await expect(gone).toBeFocused();
      // The edit form is inert: focus can only visit the dialog's own controls.
      await expectFocusTrapped({ page, key: 'Tab', inertSelector: '.edit-modal' });
      await expectFocusTrapped({ page, key: 'Shift+Tab', inertSelector: '.edit-modal' });
      // The edit typed before the removal is still guarded: Close asks, Discard closes.
      await dialog.locator('.modal__close').click();
      await dialog.getByRole('button', { name: 'Discard changes', exact: true }).click();
      await expect(dialog).toBeHidden();
      // The row and its trigger are gone, so focus falls back to the section heading.
      await expect(page.locator('#heartbeats-heading')).toBeFocused();
      expect(mock.unmocked).toEqual([]);
    });
  });
}

test.describe('edit dialog at 420px', () => {
  test.use({ viewport: { width: 420, height: 900 } });

  test('the guard message and its buttons are visible together with the body scrolled', async ({
    page,
  }) => {
    const fixture = await setupHeartbeat(page);
    await HEARTBEAT_CASE.open({ page, compact: true });
    const dialog = page.getByRole('dialog', { name: HEARTBEAT_CASE.dialogName, exact: true });
    await HEARTBEAT_CASE.edit(dialog);
    await settle(page);
    await page.keyboard.press('Escape');

    await dialog.locator('.modal__body').evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    const keep = dialog.getByRole('button', { name: 'Keep editing', exact: true });
    const discard = dialog.getByRole('button', { name: 'Discard changes', exact: true });
    await expectWithinViewport({ page, locator: dialog.getByRole('alert') });
    await expectWithinViewport({ page, locator: keep });
    await expectWithinViewport({ page, locator: discard });
    await expectTouchTarget(keep);
    await expectTouchTarget(discard);
    expect(fixture.unmocked).toEqual([]);
  });
});

test.describe('edit dialog at 420x360 (landscape or keyboard height)', () => {
  test.use({ viewport: { width: 420, height: 360 } });

  test('with the guard showing, Keep editing is reachable and the body still scrolls', async ({
    page,
  }) => {
    const fixture = await setupHeartbeat(page);
    await HEARTBEAT_CASE.open({ page, compact: true });
    const dialog = page.getByRole('dialog', { name: HEARTBEAT_CASE.dialogName, exact: true });
    await HEARTBEAT_CASE.edit(dialog);
    await settle(page);
    await page.keyboard.press('Escape');

    const keep = dialog.getByRole('button', { name: 'Keep editing', exact: true });
    const discard = dialog.getByRole('button', { name: 'Discard changes', exact: true });
    await expectWithinViewport({ page, locator: keep });
    await expectWithinViewport({ page, locator: discard });
    // The actions region is capped (45% of the height), so the body keeps room and scrolls.
    const regions = await dialog.evaluate((element) => {
      const actions = element.querySelector('.modal__actions');
      const body = element.querySelector('.modal__body');
      if (actions === null || body === null) {
        throw new Error('dialog has no actions region or body');
      }
      body.scrollTop = body.scrollHeight;
      return {
        actionsHeight: actions.getBoundingClientRect().height,
        bodyClientHeight: body.clientHeight,
        bodyScrollHeight: body.scrollHeight,
        bodyScrollTop: body.scrollTop,
      };
    });
    expect(regions.actionsHeight).toBeLessThanOrEqual(360 * 0.45 + 1);
    expect(regions.bodyClientHeight).toBeGreaterThan(0);
    expect(regions.bodyScrollHeight).toBeGreaterThan(regions.bodyClientHeight);
    expect(regions.bodyScrollTop).toBeGreaterThan(0);

    await keep.click();
    await expect(keep).toBeHidden();
    await expect(dialog).toBeVisible();
    expect(fixture.mutations).toEqual([]);
    expect(fixture.unmocked).toEqual([]);
  });
});

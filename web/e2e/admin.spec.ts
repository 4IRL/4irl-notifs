import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import {
  READY_BODY,
  TIMESTAMP,
  deliveryWire,
  destinationWire,
  expectDialogInViewport,
  expectEditorInView,
  expectNoPageOverflow,
  expectTouchTarget,
  heartbeatWire,
  identityBindingWire,
  identityKeyWire,
  identityRequestWire,
  maxRenderedLines,
  mockDelivery,
  mockProvisioning,
  openRowMenu,
  rowKebab,
  rowMenu,
  routeWire,
  scheduledWire,
} from './support/adminMocks';
import type { DeliveryMockSeed } from './support/adminMocks';

// The real provisioning API is Cloudflare Access-gated, so these critical-flow
// e2e tests run against the production build (vite preview) with the API mocked
// at the network layer via page.route.

test.describe('admin UI critical flows', () => {
  test('lists users loaded from the API on page load', async ({ page }) => {
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          users: [
            {
              user_id: 'u_abcdefgh23456777',
              apps: ['urls4irl'],
              topic_patterns: ['urls4irl-abcdefgh23456777-*'],
            },
          ],
        }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });
    await page.route('**/apps', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"apps":[]}' });
    });

    await page.goto('/');

    await expect(page.getByRole('heading', { name: '4IRL Notifications Admin' })).toBeVisible();
    // Scope to the Users section: the same user id is now mirrored in the
    // Send-test section's scoped-rows table, so a global cell match resolves to
    // two elements. exact: true still excludes the row's "Delete ..." actions cell.
    const usersSection = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Users' }) });
    await expect(
      usersSection.getByRole('cell', { name: 'u_abcdefgh23456777', exact: true }),
    ).toBeVisible();
  });

  for (const withPassword of [true, false]) {
    test(`provisioning a user reveals the returned token ${withPassword ? 'and the one-time password' : 'and the no-password state'}`, async ({
      page,
    }) => {
      let usersReturned = 0;
      await page.route('**/v1/users', async (route) => {
        // Empty before provisioning, one user afterwards. The mobile-layout
        // case keeps the list empty: a user row widens the unrelated Send-test
        // table past 390px, which would mask the provision reveal's own layout.
        const users =
          usersReturned === 0 || withPassword
            ? []
            : [
                {
                  user_id: 'u_abcdefgh23456777',
                  apps: ['urls4irl'],
                  topic_patterns: ['urls4irl-abcdefgh23456777-*'],
                },
              ];
        usersReturned += 1;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ users }),
        });
      });
      await page.route('**/v1/provision', async (route) => {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            user_id: 'u_abcdefgh23456777',
            app_id: 'urls4irl',
            person_hash: 'abcdefgh23456777',
            topic_pattern: 'urls4irl-abcdefgh23456777-*',
            token: 'tk_e2e_secret',
            ...(withPassword ? { password: 'abcd-efgh-ijkm-npqr' } : {}),
          }),
        });
      });
      await page.route('**/people', async (route) => {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: '{"people":[]}',
        });
      });
      await page.route('**/apps', async (route) => {
        await route.fulfill({ status: 200, contentType: 'application/json', body: '{"apps":[]}' });
      });

      if (withPassword) {
        await page.setViewportSize({ width: 390, height: 844 });
      }
      await page.goto('/');

      // "App ID" also labels the Add-an-app form, so scope to the Provision
      // section (by its heading) to disambiguate.
      const provisionSection = page
        .locator('section')
        .filter({ has: page.getByRole('heading', { name: 'Provision a user into an app' }) });
      await provisionSection.getByLabel('App ID').fill('urls4irl');
      await provisionSection.getByLabel('Email').fill('alice@example.com');
      await provisionSection.getByRole('button', { name: 'Provision', exact: true }).click();

      await expect(page.getByText('tk_e2e_secret')).toBeVisible();
      if (withPassword) {
        await expect(
          provisionSection.getByText(
            "This person's password is shown only once. It replaces any earlier password for this person.",
          ),
        ).toBeVisible();
        await expect(provisionSection.getByLabel('Password', { exact: true })).toHaveValue(
          'abcd-efgh-ijkm-npqr',
        );
        const copyBox = await provisionSection
          .getByRole('button', { name: 'Copy password' })
          .boundingBox();
        expect(copyBox?.height ?? 0).toBeGreaterThanOrEqual(44);
        const overflow = await page.evaluate(
          () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
        );
        expect(overflow).toBeLessThanOrEqual(0);
      } else {
        await expect(
          provisionSection.getByText('No password was returned by this request.'),
        ).toBeVisible();
      }
    });
  }

  test.describe('operator password reset from the Users list', () => {
    const RESET_PASSWORD = 'abcd-efgh-ijkm-npqr';
    const EMAIL = 'alice@example.com';

    async function mockUsersWithEmail(page: Page): Promise<void> {
      await page.route('**/v1/users', async (route) => {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            users: [
              {
                user_id: 'u_abcdefgh23456777',
                apps: ['urls4irl'],
                topic_patterns: ['urls4irl-abcdefgh23456777-*'],
              },
            ],
          }),
        });
      });
      await page.route('**/people', async (route) => {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            people: [
              { person_hash: 'abcdefgh23456777', email: EMAIL, created_at: '2026-01-01T00:00:00Z' },
            ],
          }),
        });
      });
      await page.route('**/apps', async (route) => {
        await route.fulfill({ status: 200, contentType: 'application/json', body: '{"apps":[]}' });
      });
    }

    function usersSectionOf(page: Page): Locator {
      return page
        .locator('section')
        .filter({ has: page.getByRole('heading', { name: 'Users', exact: true }) });
    }

    async function confirmReset(page: Page): Promise<void> {
      await page.getByRole('button', { name: `Reset password for ${EMAIL}` }).click();
      await page.getByRole('button', { name: `Confirm reset password for ${EMAIL}` }).click();
    }

    test('confirm, reveal once, then Done clears it and refocuses the trigger', async ({
      page,
    }) => {
      await mockUsersWithEmail(page);
      let resetBody: unknown = null;
      let resetCalls = 0;
      await page.route('**/v1/reset-password', async (route) => {
        resetCalls += 1;
        resetBody = route.request().postDataJSON();
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          headers: { 'Cache-Control': 'no-store' },
          body: JSON.stringify({
            user_id: 'u_abcdefgh23456777',
            app_id: 'urls4irl',
            password: RESET_PASSWORD,
          }),
        });
      });
      await page.goto('/');
      const section = usersSectionOf(page);
      const dialog = page.getByRole('dialog');

      await section.getByRole('button', { name: `Reset password for ${EMAIL}` }).click();
      await expect(
        dialog.getByText('Resetting replaces the current password immediately.'),
      ).toBeVisible();
      await expect(dialog.getByRole('heading', { name: 'Reset password?' })).toBeVisible();
      expect(resetCalls).toBe(0);
      await dialog.getByRole('button', { name: `Confirm reset password for ${EMAIL}` }).click();

      await expect(
        dialog.getByText(
          `The new password for ${EMAIL} is shown only once. It replaces any earlier password.`,
        ),
      ).toBeVisible();
      await expect(dialog.getByLabel('Password', { exact: true })).toHaveValue(RESET_PASSWORD);
      expect(resetCalls).toBe(1);
      expect(resetBody).toEqual({ app_id: 'urls4irl', email: EMAIL });

      await dialog.getByRole('button', { name: `Done with password for ${EMAIL}` }).click();
      await expect(dialog).toHaveCount(0);
      await expect(page.getByText(RESET_PASSWORD)).toHaveCount(0);
      await expect(
        section.getByRole('button', { name: `Reset password for ${EMAIL}` }),
      ).toBeFocused();
    });

    test('429 shows the retry time from Retry-After and 404 says the person is gone', async ({
      page,
    }) => {
      await mockUsersWithEmail(page);
      let status = 429;
      await page.route('**/v1/reset-password', async (route) => {
        await route.fulfill({
          status,
          contentType: 'application/json',
          headers: status === 429 ? { 'Retry-After': '42' } : {},
          body: JSON.stringify({
            error: status === 429 ? 'too many password changes' : 'user does not exist',
          }),
        });
      });
      await page.goto('/');
      const dialog = page.getByRole('dialog');

      await confirmReset(page);
      await expect(
        dialog
          .getByRole('alert')
          .filter({ hasText: 'Too many password changes. Try again in 42 s.' }),
      ).toBeVisible();
      await expect(dialog.getByLabel('Password', { exact: true })).toHaveCount(0);
      await dialog.getByText('Close', { exact: true }).click();
      await expect(dialog).toHaveCount(0);

      status = 404;
      await confirmReset(page);
      await expect(
        dialog.getByRole('alert').filter({
          hasText: 'This person does not exist, or has no access in this app. Refresh the list.',
        }),
      ).toBeVisible();
      await expect(page.getByText('Too many password changes')).toHaveCount(0);
    });

    test('each phase fills the viewport at 390px with 44px controls', async ({ page }) => {
      await mockUsersWithEmail(page);
      let release: () => void = () => undefined;
      const gate = new Promise<void>((resolve) => {
        release = resolve;
      });
      await page.route('**/v1/reset-password', async (route) => {
        await gate;
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            user_id: 'u_abcdefgh23456777',
            app_id: 'urls4irl',
            password: RESET_PASSWORD,
          }),
        });
      });
      await page.setViewportSize({ width: 390, height: 844 });
      await page.goto('/');
      const dialog = page.getByRole('dialog');

      async function expectFullScreen(): Promise<void> {
        const box = await dialog.boundingBox();
        expect(box?.x ?? -1).toBeCloseTo(0, 0);
        expect(box?.width ?? 0).toBeCloseTo(390, 0);
        expect(box?.height ?? 0).toBeCloseTo(844, 0);
      }
      async function expectTall(names: string[]): Promise<void> {
        for (const name of names) {
          const box = await dialog.getByRole('button', { name }).boundingBox();
          expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
        }
      }

      const trigger = usersSectionOf(page).getByRole('button', {
        name: `Reset password for ${EMAIL}`,
      });
      expect((await trigger.boundingBox())?.height ?? 0).toBeGreaterThanOrEqual(44);
      await trigger.click();
      await expectFullScreen();
      await expectTall([
        `Confirm reset password for ${EMAIL}`,
        `Cancel Reset password for ${EMAIL}`,
      ]);

      await dialog.getByRole('button', { name: `Confirm reset password for ${EMAIL}` }).click();
      await expect(dialog).toHaveAttribute('aria-busy', 'true');
      await expectFullScreen();

      release();
      await expect(dialog.getByLabel('Password', { exact: true })).toHaveValue(RESET_PASSWORD);
      await expectFullScreen();
      await expectTall(['Copy password', `Done with password for ${EMAIL}`]);
    });
  });

  test('lists people from the person service', async ({ page }) => {
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ users: [] }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          people: [
            {
              person_hash: '76gzqgp4byjl6dje',
              email: 'alice@example.com',
              created_at: '2026-07-19T18:12:03Z',
            },
          ],
        }),
      });
    });
    await page.route('**/apps', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"apps":[]}' });
    });

    await page.goto('/');

    await expect(page.getByRole('heading', { name: 'People' })).toBeVisible();
    // exact: true — the row's Delete button ("Delete alice@example.com") would
    // also substring-match the email otherwise.
    await expect(page.getByRole('cell', { name: 'alice@example.com', exact: true })).toBeVisible();
  });

  test('adding an app registers it and reveals the publisher token', async ({ page }) => {
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ users: [] }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });
    // GET lists apps (empty), POST registers a new one.
    await page.route('**/apps', async (route) => {
      if (route.request().method() === 'POST') {
        await route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            app_id: 'tasktracker',
            display_name: 'Task Tracker',
            description: null,
            created_at: '2026-07-25T10:00:00Z',
          }),
        });
        return;
      }
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"apps":[]}' });
    });
    await page.route('**/v1/provision-app', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          app_id: 'tasktracker',
          publisher_user_id: 'tasktracker-publisher',
          topic_pattern: 'tasktracker-*',
          token: 'tk_pub_e2e_secret',
        }),
      });
    });

    await page.goto('/');

    // "App ID" also labels the ProvisionForm input, so scope to the Add-an-app
    // section (by its heading) to disambiguate.
    const addSection = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Add an app' }) });
    await addSection.getByLabel('App ID').fill('tasktracker');
    await addSection.getByLabel('Display name').fill('Task Tracker');
    await addSection.getByRole('button', { name: 'Add app' }).click();

    await expect(page.getByText('tk_pub_e2e_secret')).toBeVisible();
  });

  test('removing an app cascades after confirming', async ({ page }) => {
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ users: [] }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });
    // First GET lists one app; after removal the refresh returns an empty list.
    let appsListed = 0;
    await page.route('**/apps', async (route) => {
      const apps =
        appsListed === 0
          ? [
              {
                app_id: 'urls4irl',
                display_name: 'URLs4IRL',
                description: 'Shared URL app',
                created_at: '2026-07-25T10:00:00Z',
              },
            ]
          : [];
      appsListed += 1;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ apps }),
      });
    });
    await page.route('**/v1/deprovision-app', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ app_id: 'urls4irl', removed: true }),
      });
    });

    await page.goto('/');

    // exact: true — the Edit/Remove buttons in the actions cell also contain
    // "URLs4IRL" and would otherwise make this ambiguous.
    await expect(page.getByRole('cell', { name: 'URLs4IRL', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Remove URLs4IRL', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Remove app?' });
    await expect(dialog).toContainText('URLs4IRL');
    await dialog.getByRole('button', { name: 'Confirm remove URLs4IRL', exact: true }).click();

    await expect(page.getByText('No apps registered yet.')).toBeVisible();
  });

  test('re-mint dialog Escape closes only the dialog', async ({ page }) => {
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ users: [] }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });
    await page.route('**/apps', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          apps: [
            {
              app_id: 'urls4irl',
              display_name: 'URLs4IRL',
              description: 'Shared URL app',
              created_at: '2026-07-25T10:00:00Z',
            },
          ],
        }),
      });
    });
    let tokenCalls = 0;
    await page.route('**/v1/provision-app', async (route) => {
      tokenCalls += 1;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          app_id: 'urls4irl',
          publisher_user_id: 'urls4irl-publisher',
          topic_pattern: 'urls4irl-*',
          token: 'tk_remint_e2e',
        }),
      });
    });

    await page.goto('/');
    await page.getByRole('button', { name: 'Edit URLs4IRL', exact: true }).click();
    const trigger = page.getByRole('button', { name: 'Revoke & re-mint', exact: true });
    await trigger.click();

    const dialog = page.getByRole('dialog', { name: 'Revoke and re-mint publisher token?' });
    await expect(dialog).toBeVisible();
    await page.keyboard.press('Escape');

    await expect(dialog).toBeHidden();
    await expect(trigger).toBeVisible();
    await expect(trigger).toBeFocused();
    expect(tokenCalls).toBe(0);
  });

  test('remove app dialog is full screen at 390px', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ users: [] }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });
    await page.route('**/apps', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          apps: [
            {
              app_id: 'urls4irl',
              display_name: 'URLs4IRL',
              description: 'Shared URL app',
              created_at: '2026-07-25T10:00:00Z',
            },
          ],
        }),
      });
    });

    await page.goto('/');
    await page.getByRole('button', { name: 'Remove URLs4IRL', exact: true }).click();

    const dialog = page.getByRole('dialog', { name: 'Remove app?' });
    await expect(dialog).toBeVisible();
    const box = await dialog.boundingBox();
    const viewport = page.viewportSize();
    expect(box).not.toBeNull();
    expect(box?.x).toBe(0);
    expect(box?.y).toBe(0);
    expect(box?.width).toBe(viewport?.width);
    expect(box?.height).toBe(viewport?.height);
    expect(await page.evaluate(() => document.body.style.overflow)).toBe('hidden');
    for (const name of ['Confirm remove URLs4IRL', 'Cancel Remove URLs4IRL']) {
      const button = dialog.getByRole('button', { name, exact: true });
      const buttonBox = await button.boundingBox();
      expect(buttonBox?.height ?? 0).toBeGreaterThanOrEqual(44);
    }
  });

  test('App-ID combobox suggests registered apps + offers unprovisioned, and ghost apps are listed', async ({
    page,
  }) => {
    // A user is provisioned into urls4irl (unregistered); tasktracker is the
    // only registered app.
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          users: [
            {
              user_id: 'u_76gzqgp4byjl6dje',
              apps: ['urls4irl'],
              topic_patterns: ['urls4irl-76gzqgp4byjl6dje-*'],
            },
          ],
        }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });
    await page.route('**/apps', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          apps: [
            {
              app_id: 'tasktracker',
              display_name: 'Task Tracker',
              description: null,
              created_at: '2026-07-25T10:00:00Z',
            },
          ],
        }),
      });
    });

    await page.goto('/');

    const provisionSection = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Provision a user into an app' }) });
    const appIdField = provisionSection.getByLabel('App ID');

    // Typing a fragment surfaces the matching registered app.
    await appIdField.click();
    await appIdField.fill('task');
    await expect(page.getByRole('option', { name: /tasktracker/i })).toBeVisible();

    // Typing a valid, unregistered id surfaces the "use unprovisioned" option.
    await appIdField.fill('newapp');
    await expect(page.getByRole('option', { name: /Use unprovisioned app/ })).toBeVisible();

    // The ghost app (urls4irl: in use, unregistered) is listed under Unprovisioned apps.
    const ghostSection = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Unprovisioned apps' }) });
    await expect(ghostSection.getByRole('cell', { name: 'urls4irl', exact: true })).toBeVisible();
  });

  test('sends a test notification and shows a delivered result', async ({ page }) => {
    // Two subscribers on one app so the Send-test section has scoped rows.
    await page.route('**/v1/users', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          users: [
            {
              user_id: 'u_76gzqgp4byjl6dje',
              apps: ['urls4irl'],
              topic_patterns: ['urls4irl-76gzqgp4byjl6dje-*'],
            },
            {
              user_id: 'u_4x2k9m7pqrs1twvz',
              apps: ['urls4irl'],
              topic_patterns: ['urls4irl-4x2k9m7pqrs1twvz-*'],
            },
          ],
        }),
      });
    });
    await page.route('**/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });
    await page.route('**/apps', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: '{"apps":[{"app_id":"urls4irl","display_name":"URLs 4 IRL","description":null,"created_at":"2026-01-01T00:00:00Z"}]}',
      });
    });
    let testNotifyBody: unknown = null;
    await page.route('**/v1/test-notify', async (route) => {
      testNotifyBody = route.request().postDataJSON();
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results: [
            {
              recipient: '76gzqgp4byjl6dje',
              user_id: 'u_76gzqgp4byjl6dje',
              topic: 'urls4irl-76gzqgp4byjl6dje-all',
              ok: true,
              message_id: 'abc',
              error: '',
            },
          ],
        }),
      });
    });

    await page.goto('/');

    // Scope everything to the Send-test section (by its heading) so the mirrored
    // user id in the Users table below never satisfies a locator by accident.
    const sendTestSection = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Send test notification' }) });

    // targetApp defaults to the only sorted option (urls4irl) once users load.
    await expect(sendTestSection.getByLabel('Target app')).toHaveValue('urls4irl');

    // The hint carries the registry display name once /apps has loaded; waiting
    // on it also guarantees the title sent below is the registry-backed one.
    await expect(
      sendTestSection.getByText('Subscribers listen on all. Arrives titled “URLs 4 IRL test”.'),
    ).toBeVisible();

    await sendTestSection.getByRole('checkbox', { name: 'Select u_76gzqgp4byjl6dje' }).check();
    await sendTestSection.getByRole('button', { name: 'Send test notification' }).click();

    await expect(sendTestSection.getByText('Delivered')).toBeVisible();
    expect(testNotifyBody).toMatchObject({ channel: 'all', title: 'URLs 4 IRL test' });
  });
});

/**
 * Asserts a status badge sits at the vertical middle of its table row (within
 * 2px), and that the row is taller than the badge so the check can fail.
 */
async function expectCenteredInRow({
  badge,
  row,
}: {
  badge: Locator;
  row: Locator;
}): Promise<void> {
  await expect(badge).toBeVisible();
  const badgeBox = await badge.boundingBox();
  const rowBox = await row.boundingBox();
  if (badgeBox === null || rowBox === null) {
    throw new Error('badge or row has no bounding box');
  }
  expect(rowBox.height).toBeGreaterThan(badgeBox.height + 8);
  const badgeCentre = badgeBox.y + badgeBox.height / 2;
  const rowCentre = rowBox.y + rowBox.height / 2;
  expect(Math.abs(badgeCentre - rowCentre)).toBeLessThanOrEqual(2);
}

/**
 * Asserts a control is reachable by scrolling the wrapper sideways: scrolled so its start sits
 * just right of the pinned first column, it is inside the wrapper's visible box, not under the
 * pinned column and below the pinned titles. (A row's controls can sit far left in a wide
 * Actions cell, so "the end of the scroll" is not where every control lands.)
 */
async function expectReachableByScrolling({
  wrapper,
  control,
}: {
  wrapper: Locator;
  control: Locator;
}): Promise<void> {
  await control.evaluate(async (element) => {
    const scroller = element.closest('.admin-table__scroll');
    const lead = scroller?.querySelector('tbody .admin-table__lead');
    if (!(scroller instanceof HTMLElement) || lead === null || lead === undefined) {
      throw new Error('control is not inside a pinned table wrapper');
    }
    const gap = element.getBoundingClientRect().left - lead.getBoundingClientRect().right - 8;
    scroller.scrollLeft += gap;
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  });
  const box = await control.boundingBox();
  const wrapperBox = await wrapper.boundingBox();
  const leadBox = await wrapper.locator('tbody .admin-table__lead').first().boundingBox();
  const headerBox = await wrapper.locator('thead th').first().boundingBox();
  if (box === null || wrapperBox === null || leadBox === null || headerBox === null) {
    throw new Error('control, wrapper, pinned column or pinned titles have no bounding box');
  }
  // Not under the pinned column, and starting inside the visible part of the wrapper.
  expect(box.x).toBeGreaterThanOrEqual(leadBox.x + leadBox.width - 1);
  expect(box.x).toBeLessThan(wrapperBox.x + wrapperBox.width - 24);
  // Not under the pinned titles.
  expect(box.y).toBeGreaterThanOrEqual(headerBox.y + headerBox.height - 1);
  // Leave the wrapper as found, so a later click is not made against a scrolled table.
  await wrapper.evaluate((element) => {
    element.scrollLeft = 0;
  });
}

/** The Actions cell of every ordinary row: its last cell (inline-editor rows have none). */
const ACTIONS_CELL = 'tbody tr:not(:has(.admin-table__editor-cell)) > td:last-child';

/** What an Actions cell offers that must never be left under the pinned column. */
const ACTIONS_ITEM = 'button, [class*="pill"]';

/**
 * Scrolls a table wrapper to its start or its end and returns every way the row actions
 * (buttons and status pills in the last cell) or the pinned first column are not fully
 * visible there. At the end each action must lie wholly right of the pinned column and inside
 * the scrollport; at the start the pinned column must lie wholly inside the scrollport and
 * take no more than `maxLeadShare` of it. No violations means the layout holds. At the end it
 * also counts the rows whose whole cluster fits beside the pinned column (judged control by
 * control) and those too wide (judged by their trailing control), so a test can tell the two
 * branches really ran.
 */
async function pinnedLayoutViolations({
  wrapper,
  position,
  maxLeadShare,
}: {
  wrapper: Locator;
  position: 'start' | 'end';
  maxLeadShare: number;
}): Promise<{ violations: string[]; whole: number; clipped: number }> {
  return wrapper.evaluate(
    async (scroller, { position, maxLeadShare, cellSelector, itemSelector }) => {
      scroller.scrollLeft = position === 'end' ? scroller.scrollWidth : 0;
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      const portLeft = scroller.getBoundingClientRect().left + scroller.clientLeft;
      const portRight = portLeft + scroller.clientWidth;
      const corner = scroller.querySelector('thead .admin-table__lead');
      if (corner === null) {
        return { violations: ['no pinned corner cell'], whole: 0, clipped: 0 };
      }
      const leadRight = corner.getBoundingClientRect().right;
      const found: string[] = [];
      if (position === 'start') {
        for (const lead of Array.from(scroller.querySelectorAll('.admin-table__lead'))) {
          const box = lead.getBoundingClientRect();
          if (box.left < portLeft - 0.5 || box.right > portRight + 0.5) {
            found.push(`pinned cell outside the scrollport: ${box.left}..${box.right}`);
          }
        }
        const share = (leadRight - portLeft) / scroller.clientWidth;
        if (share > maxLeadShare) {
          found.push(`pinned column takes ${share.toFixed(2)} of the scrollport`);
        }
        return { violations: found, whole: 0, clipped: 0 };
      }
      let items = 0;
      let whole = 0;
      let clipped = 0;
      const visibleWidth = portRight - leadRight;
      for (const cell of Array.from(scroller.querySelectorAll(cellSelector))) {
        const boxes = Array.from(cell.querySelectorAll(itemSelector))
          .map((item) => ({
            box: item.getBoundingClientRect(),
            label: (item.getAttribute('aria-label') ?? item.textContent ?? '').trim(),
          }))
          .filter(({ box }) => box.width > 0);
        if (boxes.length === 0) {
          continue;
        }
        items += boxes.length;
        const clusterWidth =
          Math.max(...boxes.map(({ box }) => box.right)) -
          Math.min(...boxes.map(({ box }) => box.left));
        const trailing = boxes.reduce((last, next) =>
          next.box.right > last.box.right ? next : last,
        );
        // A row's actions that fit beside the pinned column are all wholly visible; a longer
        // cluster still ends in view (its last control wholly visible, no blank space after
        // it) and each control in it is reached by scrolling back (see the focus checks).
        const cellPadding = Number.parseFloat(getComputedStyle(cell).paddingRight);
        const fits = clusterWidth + cellPadding <= visibleWidth;
        if (fits) {
          whole += 1;
        } else {
          clipped += 1;
        }
        const judged = fits ? boxes : [trailing];
        for (const { box, label } of judged) {
          if (box.left < leadRight - 0.5 || box.right > portRight + 0.5) {
            found.push(
              `${label}: ${box.left.toFixed(0)}..${box.right.toFixed(0)} not within ` +
                `${leadRight.toFixed(0)}..${portRight.toFixed(0)}`,
            );
          }
        }
        if (trailing.box.right < portRight - 40) {
          found.push(
            `${trailing.label}: blank space after it, ends at ${trailing.box.right.toFixed(0)} ` +
              `of ${portRight.toFixed(0)}`,
          );
        }
      }
      if (items === 0) {
        found.push('no row actions found');
      }
      return { violations: found, whole, clipped };
    },
    { position, maxLeadShare, cellSelector: ACTIONS_CELL, itemSelector: ACTIONS_ITEM },
  );
}

/**
 * Tabs through every button of each row (the Keys column's Revoke included) from the wrapper's
 * start edge, then focuses the first one from the far end, and returns every focused button
 * that is not wholly right of the pinned column and inside the scrollport, plus how many
 * focused buttons were checked (so a test can tell the loop was not vacuous).
 */
async function focusedActionViolations({
  page,
  wrapper,
}: {
  page: Page;
  wrapper: Locator;
}): Promise<{ violations: string[]; checks: number }> {
  const found: string[] = [];
  let checks = 0;
  const check = async (): Promise<void> => {
    checks += 1;
    const problem = await wrapper.evaluate(async (scroller) => {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !scroller.contains(active)) {
        return 'focus left the wrapper';
      }
      const portLeft = scroller.getBoundingClientRect().left + scroller.clientLeft;
      const portRight = portLeft + scroller.clientWidth;
      const leadRight =
        scroller.querySelector('thead .admin-table__lead')?.getBoundingClientRect().right ??
        Infinity;
      const box = active.getBoundingClientRect();
      const label = (active.getAttribute('aria-label') ?? active.textContent ?? '').trim();
      if (box.left < leadRight - 0.5 || box.right > portRight + 0.5) {
        return `${label}: ${box.left.toFixed(0)}..${box.right.toFixed(0)} not within ${leadRight.toFixed(0)}..${portRight.toFixed(0)} (scrolled ${scroller.scrollLeft} of ${scroller.scrollWidth - scroller.clientWidth})`;
      }
      return null;
    });
    if (problem !== null) {
      found.push(problem);
    }
  };
  const rows = wrapper.locator(ACTIONS_CELL.replace(/ > td:last-child$/, ''));
  const rowCount = await rows.count();
  for (let index = 0; index < rowCount; index += 1) {
    const buttons = rows.nth(index).locator('button');
    const count = await buttons.count();
    if (count === 0) {
      continue;
    }
    await wrapper.evaluate((scroller) => {
      scroller.scrollLeft = 0;
    });
    await buttons.first().focus();
    await check();
    for (let step = 1; step < count; step += 1) {
      await page.keyboard.press('Tab');
      await check();
    }
    // From the far end: focusing the row's first button scrolls back, clear of the column.
    await wrapper.evaluate((scroller) => {
      scroller.scrollLeft = scroller.scrollWidth;
    });
    await buttons.first().focus();
    await check();
  }
  return { violations: found, checks };
}

/**
 * Asserts a desktop (fine pointer) button keeps the compact table-button size: the 44px touch
 * target is only for coarse pointers and the narrow layout's kebab and menu items.
 */
async function expectCompactButton(locator: Locator): Promise<void> {
  const box = await locator.boundingBox();
  expect(box?.height ?? Infinity).toBeLessThan(40);
  expect(box?.height ?? 0).toBeGreaterThan(20);
}

/**
 * Asserts a confirm dialog is full screen (its box is the viewport) and that its named buttons
 * are fully visible and at least 44px tall.
 */
async function expectFullScreenConfirm({
  page,
  dialog,
  buttons,
}: {
  page: Page;
  dialog: Locator;
  buttons: string[];
}): Promise<void> {
  await expect(dialog).toBeVisible();
  const box = await dialog.boundingBox();
  const viewport = page.viewportSize();
  expect(box).not.toBeNull();
  expect(box?.x).toBe(0);
  expect(box?.y).toBe(0);
  expect(box?.width).toBe(viewport?.width);
  expect(box?.height).toBe(viewport?.height);
  for (const name of buttons) {
    const button = dialog.getByRole('button', { name, exact: true });
    await expect(button).toBeVisible();
    await expectInViewport({ page, locator: button });
    const buttonBox = await button.boundingBox();
    expect(buttonBox?.height ?? 0).toBeGreaterThanOrEqual(44);
  }
}

/** Asserts a box lies wholly inside the viewport. */
async function expectInViewport({
  page,
  locator,
}: {
  page: Page;
  locator: Locator;
}): Promise<void> {
  const box = await locator.boundingBox();
  const viewport = page.viewportSize();
  if (box === null || viewport === null) {
    throw new Error('element or viewport has no size');
  }
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
  expect(box.y + box.height).toBeLessThanOrEqual(viewport.height);
}

/**
 * Asserts nothing clips or covers an element: what is painted just inside each of its four
 * corners is the element itself (or something in it).
 */
async function expectFullyPainted(locator: Locator): Promise<void> {
  const painted = await locator.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    const inset = 3;
    return [
      [rect.left + inset, rect.top + inset],
      [rect.right - inset, rect.top + inset],
      [rect.left + inset, rect.bottom - inset],
      [rect.right - inset, rect.bottom - inset],
    ].map(([x, y]) => {
      const hit = document.elementFromPoint(x, y);
      return hit !== null && element.contains(hit);
    });
  });
  expect(painted).toEqual([true, true, true, true]);
}

/** The element that has focus, described for an assertion message, or `body`. */
async function focusedDescription(page: Page): Promise<string> {
  return page.evaluate(() => {
    const active = document.activeElement;
    return active === null
      ? 'none'
      : `${active.tagName.toLowerCase()}:${active.getAttribute('aria-label') ?? active.textContent?.trim().slice(0, 40) ?? ''}`;
  });
}

/**
 * Asserts the narrow-layout (<= 30rem) table contract on one wrapper at its current size:
 *  - it scrolls both ways inside itself, and the page never scrolls sideways;
 *  - the column titles stay pinned to its top while it scrolls, opaque and painted above rows;
 *  - the first column is NOT pinned: it scrolls away like every other column and its cells
 *    show their whole text (no ellipsis, nothing clipped by the cell);
 *  - the kebab column is pinned to the wrapper's end edge at every scroll position, opaque,
 *    painted over the scrolling cells, below the title row's corner cell;
 *  - the body rows carry no buttons but the kebab (no inline row buttons, no notes, errors or
 *    confirms widening the table) and no cell wraps.
 * Needs enough rows to overflow vertically. Leaves the wrapper scrolled.
 */
async function expectCompactTable({ wrapper }: { wrapper: Locator }): Promise<void> {
  expect(await maxRenderedLines({ root: wrapper, skip: '.admin-table__editor-cell' })).toBe(1);

  const report = await wrapper.evaluate(async (element) => {
    element.scrollIntoView({ block: 'start' });
    const style = getComputedStyle(element);
    const alphaOf = (target: Element): number => {
      const color = getComputedStyle(target).backgroundColor;
      const channels = /^rgba?\(([^)]+)\)$/.exec(color)?.[1].split(',') ?? [];
      if (channels.length === 3) {
        return 1;
      }
      return channels.length === 4 ? Number(channels[3]) : Number.NaN;
    };
    const frame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    const scrollsBothWays =
      element.scrollWidth > element.clientWidth && element.scrollHeight > element.clientHeight;
    element.scrollTop = 160;
    element.scrollLeft = 120;
    await frame();

    const wrapperRect = element.getBoundingClientRect();
    const portRight = wrapperRect.left + element.clientLeft + element.clientWidth;
    const headers = Array.from(element.querySelectorAll('thead th'));
    const headerBottom = Math.max(
      ...headers.map((header) => header.getBoundingClientRect().bottom),
    );
    /** True when what is painted at the middle of `target` is `target` (or inside it). */
    const paintedAtCentre = (target: Element): boolean | null => {
      const rect = target.getBoundingClientRect();
      if (
        rect.top < headerBottom ||
        rect.bottom > Math.min(wrapperRect.bottom, window.innerHeight) ||
        rect.width < 4
      ) {
        return null;
      }
      const hit = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
      return hit !== null && target.contains(hit);
    };
    const kebabInfo = ({ cell, header }: { cell: Element; header: boolean }) => {
      const rect = cell.getBoundingClientRect();
      const computed = getComputedStyle(cell);
      return {
        header,
        position: computed.position,
        alpha: alphaOf(cell),
        zIndex: Number(computed.zIndex),
        gapRight: portRight - rect.right,
        width: rect.width,
        painted: header ? null : paintedAtCentre(cell.querySelector('button') ?? cell),
      };
    };
    const kebabsNow = () => [
      ...Array.from(element.querySelectorAll('thead th.admin-table__kebab')).map((cell) =>
        kebabInfo({ cell, header: true }),
      ),
      ...Array.from(element.querySelectorAll('tbody td.admin-table__kebab')).map((cell) =>
        kebabInfo({ cell, header: false }),
      ),
    ];
    const kebabsMiddle = kebabsNow();
    const leadCells = Array.from(element.querySelectorAll('tbody td.admin-table__lead'));
    const leads = leadCells.map((cell) => {
      const computed = getComputedStyle(cell);
      return {
        position: computed.position,
        // Pinned would keep it at the wrapper's start edge; unpinned, 120px of scroll moves it.
        gapLeft: cell.getBoundingClientRect().left - wrapperRect.left,
        // No ellipsis, nothing clipped: the cell is as wide as its content.
        clipped: cell.scrollWidth > cell.clientWidth + 1,
        textOverflow: computed.textOverflow,
        overflow: computed.overflowX,
        maxWidth: computed.maxWidth,
      };
    });
    const leadCorner = element.querySelector('thead th.admin-table__lead');

    // At the far end the kebab column is still on the end edge.
    element.scrollLeft = element.scrollWidth;
    await frame();
    const kebabsAtEnd = kebabsNow();

    return {
      overflow: { x: style.overflowX, y: style.overflowY },
      scrollsBothWays,
      scrolled: { top: element.scrollTop },
      pageScrollLeft: document.documentElement.scrollLeft,
      headers: headers.map((header) => ({
        position: getComputedStyle(header).position,
        alpha: alphaOf(header),
        gapTop: header.getBoundingClientRect().top - wrapperRect.top,
        zIndex: Number(getComputedStyle(header).zIndex),
      })),
      cornerPosition: leadCorner === null ? null : getComputedStyle(leadCorner).position,
      leads,
      kebabsMiddle,
      kebabsAtEnd,
      // Every body button is a kebab: no inline buttons, no confirm pairs.
      strayButtons: Array.from(element.querySelectorAll('tbody button'))
        .filter((button) => !button.classList.contains('row-menu__trigger'))
        .map((button) => (button.textContent ?? '').trim()),
      // No cell may wrap (the editor row aside).
      wrappingCells: Array.from(element.querySelectorAll('th, td:not(.admin-table__editor-cell)'))
        .filter((cell) => getComputedStyle(cell).whiteSpace !== 'nowrap')
        .map((cell) => cell.className || cell.tagName),
    };
  });

  expect(report.overflow).toEqual({ x: 'auto', y: 'auto' });
  expect(report.scrollsBothWays).toBe(true);
  expect(report.scrolled.top).toBeGreaterThan(0);
  expect(report.pageScrollLeft).toBe(0);

  // Titles: pinned to the wrapper's top, opaque, above the rows.
  expect(report.headers.length).toBeGreaterThan(2);
  for (const header of report.headers) {
    expect(header.position).toBe('sticky');
    expect(header.alpha).toBe(1);
    expect(Math.abs(header.gapTop)).toBeLessThanOrEqual(1);
  }

  // The first column scrolls away and shows every character of its cells.
  expect(report.leads.length).toBeGreaterThan(4);
  for (const lead of report.leads) {
    expect(lead.position).toBe('static');
    expect(lead.gapLeft).toBeLessThan(0);
    expect(lead.clipped).toBe(false);
    expect(lead.textOverflow).not.toBe('ellipsis');
    expect(lead.overflow).toBe('visible');
    expect(lead.maxWidth).toBe('none');
  }
  // ... and so does its title (the corner is a plain pinned title here, not a pinned column).
  expect(report.cornerPosition).toBe('sticky');

  // Kebab column: on the end edge, opaque, painted, below the corner, at both scroll ends.
  for (const kebabs of [report.kebabsMiddle, report.kebabsAtEnd]) {
    const header = kebabs.find((kebab) => kebab.header);
    const cells = kebabs.filter((kebab) => !kebab.header);
    expect(header).toBeDefined();
    expect(cells.length).toBeGreaterThan(4);
    expect(Math.abs(header?.gapRight ?? NaN)).toBeLessThanOrEqual(1);
    for (const cell of cells) {
      expect(cell.position).toBe('sticky');
      expect(cell.alpha).toBe(1);
      expect(Math.abs(cell.gapRight)).toBeLessThanOrEqual(1);
      expect(cell.width).toBeLessThanOrEqual(56);
      expect(cell.zIndex).toBeLessThan(header?.zIndex ?? 0);
      expect(cell.painted).not.toBe(false);
    }
    // At least some kebabs were really hit-tested over the scrolling cells.
    expect(cells.filter((cell) => cell.painted === true).length).toBeGreaterThan(2);
  }
  expect(report.strayButtons).toEqual([]);
  expect(report.wrappingCells).toEqual([]);
}

/**
 * Asserts the roomy-layout pinned-table contract on one wrapper at its current size: it
 * scrolls both ways inside itself, the column titles stay pinned to its top while it scrolls
 * vertically and sideways, the first column stays at its start edge, both are opaque and
 * painted above the scrolling rows, no cell wraps (computed `white-space: nowrap`, text on one
 * line, action buttons on one line), an ordinary first-column value is never truncated and a
 * pathologically long one is capped (60% of the wrapper, at most 32rem) with its full value in
 * `title`. Needs enough rows to overflow vertically, and at least one first-column value
 * longer than the cap. Leaves the wrapper scrolled.
 */
async function expectPinnedTable({ wrapper }: { wrapper: Locator }): Promise<void> {
  // No wrapped text anywhere in the table (the editor row wraps by design).
  expect(await maxRenderedLines({ root: wrapper, skip: '.admin-table__editor-cell' })).toBe(1);

  const report = await wrapper.evaluate(async (element) => {
    element.scrollIntoView({ block: 'start' });
    const style = getComputedStyle(element);
    // NaN when the colour is not parseable, so a surprise fails the assertion below.
    const alphaOf = (target: Element): number => {
      const color = getComputedStyle(target).backgroundColor;
      const channels = /^rgba?\(([^)]+)\)$/.exec(color)?.[1].split(',') ?? [];
      if (channels.length === 3) {
        return 1;
      }
      return channels.length === 4 ? Number(channels[3]) : Number.NaN;
    };
    const scrollsBothWays =
      element.scrollWidth > element.clientWidth && element.scrollHeight > element.clientHeight;
    element.scrollTop = 160;
    element.scrollLeft = 120;
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));

    const wrapperRect = element.getBoundingClientRect();
    const headers = Array.from(element.querySelectorAll('thead th'));
    const headerBottom = Math.max(
      ...headers.map((header) => header.getBoundingClientRect().bottom),
    );
    const leadRight =
      element.querySelector('thead .admin-table__lead')?.getBoundingClientRect().right ?? 0;
    const visibleRight = Math.min(wrapperRect.right, window.innerWidth);
    /** What is painted at the middle of the part of `target` that is on screen, or null. */
    const hitAtVisibleCentre = ({
      target,
      minLeft,
    }: {
      target: Element;
      minLeft: number;
    }): Element | null => {
      const rect = target.getBoundingClientRect();
      const left = Math.max(rect.left, minLeft);
      const right = Math.min(rect.right, visibleRight);
      const top = Math.max(rect.top, wrapperRect.top);
      const bottom = Math.min(rect.bottom, wrapperRect.bottom, window.innerHeight);
      if (right - left < 4 || bottom - top < 4) {
        return null;
      }
      return document.elementFromPoint((left + right) / 2, (top + bottom) / 2);
    };
    const headerHits = headers.map((header) => {
      const hit = hitAtVisibleCentre({
        target: header,
        // The corner cell is the pinned column itself; every other title is judged where it
        // shows, to the right of that column.
        minLeft: header.classList.contains('admin-table__lead') ? wrapperRect.left : leadRight,
      });
      return hit === null ? null : hit.closest('th') === header;
    });
    const cells = Array.from(element.querySelectorAll('th, td:not(.admin-table__editor-cell)'));
    const buttonRows = Array.from(element.querySelectorAll('tbody td'))
      .map((cell) => Array.from(cell.querySelectorAll(':scope > div > button')))
      .filter((buttons) => buttons.length > 1);
    return {
      overflow: { x: style.overflowX, y: style.overflowY },
      scrollsBothWays,
      scrolled: { top: element.scrollTop, left: element.scrollLeft },
      pageScrollLeft: document.documentElement.scrollLeft,
      wrapperWidth: wrapperRect.width,
      remPx: Number.parseFloat(getComputedStyle(document.documentElement).fontSize),
      headers: headers.map((header, index) => ({
        position: getComputedStyle(header).position,
        alpha: alphaOf(header),
        gapTop: header.getBoundingClientRect().top - wrapperRect.top,
        // null: this header is scrolled out of sight or under the pinned column.
        painted: headerHits[index],
      })),
      leads: Array.from(element.querySelectorAll('.admin-table__lead')).map((cell) => {
        const rect = cell.getBoundingClientRect();
        // Body cells below the titles are hit at their middle, never a scrolled neighbour.
        const hit =
          cell.tagName === 'TH' || rect.top < headerBottom
            ? null
            : hitAtVisibleCentre({ target: cell, minLeft: wrapperRect.left });
        return {
          header: cell.tagName === 'TH',
          position: getComputedStyle(cell).position,
          alpha: alphaOf(cell),
          gapLeft: rect.left - wrapperRect.left,
          width: rect.width,
          painted: hit === null ? null : hit.closest('td') === cell,
          truncated: cell.scrollWidth > cell.clientWidth + 1,
          title: cell.getAttribute('title'),
          text: (cell.textContent ?? '').trim(),
        };
      }),
      // Every body cell that scrolls (not pinned) is a different, non-sticky cell.
      scrollingCellsAreNotSticky: Array.from(
        element.querySelectorAll('tbody td:not(.admin-table__lead):not(.admin-table__editor-cell)'),
      ).every((cell) => getComputedStyle(cell).position === 'static'),
      // No cell may wrap: computed white-space is nowrap everywhere (the editor row aside).
      wrappingCells: cells
        .filter((cell) => getComputedStyle(cell).whiteSpace !== 'nowrap')
        .map((cell) => cell.className || cell.tagName),
      // Buttons side by side in an Actions cell share a top edge (a flex-wrap would stack them).
      stackedButtonRows: buttonRows.filter(
        (buttons) =>
          new Set(buttons.map((button) => Math.round(button.getBoundingClientRect().top))).size > 1,
      ).length,
      buttonRowCount: buttonRows.length,
    };
  });

  expect(report.overflow).toEqual({ x: 'auto', y: 'auto' });
  expect(report.scrollsBothWays).toBe(true);
  expect(report.scrolled.top).toBeGreaterThan(0);
  expect(report.scrolled.left).toBeGreaterThan(0);
  // The wrapper scrolled, not the page.
  expect(report.pageScrollLeft).toBe(0);
  expect(report.headers.length).toBeGreaterThan(2);
  for (const header of report.headers) {
    expect(header.position).toBe('sticky');
    expect(header.alpha).toBe(1);
    // Still at the wrapper's top edge although the rows scrolled up under it.
    expect(Math.abs(header.gapTop)).toBeLessThanOrEqual(1);
    expect(header.painted).not.toBe(false);
  }
  // At least the corner and one more title were really hit-tested, over the scrolled rows.
  expect(report.headers.filter((header) => header.painted === true).length).toBeGreaterThan(1);
  // The title row and every body row have a pinned first cell.
  expect(report.leads.filter((lead) => lead.header)).toHaveLength(1);
  expect(report.leads.length).toBeGreaterThan(4);
  const cap = Math.min(report.wrapperWidth * 0.6, 32 * report.remPx);
  for (const lead of report.leads) {
    expect(lead.position).toBe('sticky');
    expect(lead.alpha).toBe(1);
    // Still at the wrapper's start edge although the table scrolled sideways.
    expect(Math.abs(lead.gapLeft)).toBeLessThanOrEqual(1);
    expect(lead.painted).not.toBe(false);
    // Only a pathological identifier reaches the cap (plus a pixel of rounding).
    expect(lead.width).toBeLessThanOrEqual(cap + 1);
  }
  expect(report.leads.some((lead) => !lead.header && lead.painted === true)).toBe(true);
  // The seeded long identifier really reached the cap, so the cap is what held it, and it is
  // the only kind of value that is truncated; its full value is in `title`.
  const bodyLeads = report.leads.filter((lead) => !lead.header);
  expect(Math.max(...bodyLeads.map((lead) => lead.width))).toBeGreaterThan(cap - 2);
  for (const lead of bodyLeads) {
    // The full value travels in `title`, for the one that is cut off to be readable.
    expect(lead.title ?? '').not.toBe('');
    expect(lead.text).toContain(lead.title ?? '');
    if (lead.text.includes('very_long_identifier')) {
      expect((lead.title ?? '').length).toBeGreaterThan(80);
    }
    // (Every cell of the column is as wide as the capped one, so judge each by its content.)
    if (lead.text.includes('very_long_identifier')) {
      expect(lead.truncated, `"${lead.text}" (${lead.width}px) is capped`).toBe(true);
    } else {
      expect(lead.truncated, `"${lead.text}" is not truncated`).toBe(false);
    }
  }
  // The Actions cell and every other column scroll away.
  expect(report.scrollingCellsAreNotSticky).toBe(true);
  expect(report.wrappingCells).toEqual([]);
  expect(report.stackedButtonRows).toBe(0);
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/**
 * Every way an admin table's Actions column does not fit its wrapper without sideways
 * scrolling: the wrapper scrolls, a button or an extra line (owner note, test result, row
 * error) sticks out of its cell's content box or the wrapper, an extra line is cut off, or the
 * column gets narrower when the extra lines are hidden (so they are what widens it). Judge it
 * with every row idle (no confirm open); an open confirm is judged on its own, by the
 * "open confirm" tests below.
 */
async function actionsFitViolations({
  wrapper,
  extraLines,
}: {
  wrapper: Locator;
  /** Selector of the lines under the buttons that must stay inside the cell. */
  extraLines: string;
}): Promise<string[]> {
  return wrapper.evaluate(
    (scroller, selectors) => {
      const problems: string[] = [];
      const port = scroller.getBoundingClientRect();
      if (scroller.scrollWidth > scroller.clientWidth) {
        problems.push(
          `wrapper scrolls sideways (${scroller.scrollWidth} > ${scroller.clientWidth})`,
        );
      }
      const cells = [...scroller.querySelectorAll(selectors.cell)];
      if (cells.length === 0) {
        return ['no Actions cells found'];
      }
      for (const cell of cells) {
        const cellBox = cell.getBoundingClientRect();
        const style = getComputedStyle(cell);
        const contentLeft = cellBox.left + parseFloat(style.paddingLeft);
        const contentRight = cellBox.right - parseFloat(style.paddingRight);
        if (cellBox.right > port.right + 0.5) {
          problems.push(`actions cell ends at ${cellBox.right} past the wrapper at ${port.right}`);
        }
        for (const button of cell.querySelectorAll('button')) {
          const box = button.getBoundingClientRect();
          if (box.right > contentRight + 0.5 || box.right > port.right + 0.5) {
            problems.push(`a button ends at ${box.right}, past its cell or the wrapper`);
          }
        }
        for (const line of cell.querySelectorAll(selectors.lines)) {
          const box = line.getBoundingClientRect();
          if (box.width > 0 && (box.right > contentRight + 0.5 || box.left < contentLeft - 0.5)) {
            problems.push(`"${line.textContent}" sticks out of its cell`);
          }
          // No extra line is ever cut off: the owner note is short enough to show in full,
          // and a result or an error wraps.
          const cutOff =
            line.scrollWidth > line.clientWidth + 1 || line.scrollHeight > line.clientHeight + 1;
          if (cutOff) {
            problems.push(`"${line.textContent}" is cut off`);
          }
        }
      }
      // Hiding the extra lines must not narrow the column: the buttons alone set its width.
      // The owner note is the one exception: it is a short line shown in full, so it may set
      // the width (the wrapper still must not scroll, checked above).
      const widths = () => cells.map((cell) => cell.getBoundingClientRect().width);
      const withLines = widths();
      const lines = [...scroller.querySelectorAll<HTMLElement>(selectors.lines)].filter(
        (line) => !line.classList.contains('admin-table__note'),
      );
      const shown = lines.map((line) => line.style.display);
      for (const line of lines) {
        line.style.display = 'none';
      }
      const withoutLines = widths();
      for (const [index, line] of lines.entries()) {
        line.style.display = shown[index];
      }
      for (const [index, width] of withLines.entries()) {
        if (width > withoutLines[index] + 0.5) {
          problems.push(
            `the extra lines widen the Actions column from ${withoutLines[index]}px to ${width}px`,
          );
        }
      }
      return problems;
    },
    { cell: ACTIONS_CELL, lines: extraLines },
  );
}

test.describe('admin UI Delivery tab', () => {
  test.beforeEach(async ({ page }) => {
    await mockProvisioning(page);
  });

  test('status card, destination create, send-test, rotate and disable', async ({ page }) => {
    const mock = await mockDelivery(page);

    await page.goto('/');
    await page.getByRole('tab', { name: 'Delivery' }).click();

    const statusCard = page.getByRole('region', { name: 'delivery-api' });
    await expect(statusCard.getByText('Ready', { exact: true })).toBeVisible();
    await expect(page.getByText('No destinations yet.')).toBeVisible();

    // Create a webhook destination.
    await page.getByRole('button', { name: 'Add destination', exact: true }).click();
    const addForm = page.getByRole('region', { name: 'Add destination' });
    await addForm.getByLabel('Name', { exact: true }).fill('hook1');
    await addForm.getByLabel('Type', { exact: true }).selectOption('webhook');
    await addForm.getByLabel('Webhook URL').fill('https://hooks.example.com/in');
    await addForm.getByLabel('Signing secret').fill('s'.repeat(40));
    await addForm.getByRole('button', { name: 'Add destination', exact: true }).click();
    await expect(page.getByRole('cell', { name: 'hook1', exact: true })).toBeVisible();

    const create = mock.mutations.find(
      (request) =>
        request.method === 'POST' && request.pathname === '/delivery/v1/admin/destinations',
    );
    expect(create?.body).toEqual({
      name: 'hook1',
      type: 'webhook',
      config: {},
      secrets: { url: 'https://hooks.example.com/in', signing_secret: 's'.repeat(40) },
    });

    // Send a test delivery.
    await page.getByRole('button', { name: 'Send test to hook1', exact: true }).click();
    await expect(page.getByText('HTTP 200')).toBeVisible();
    const sendTest = mock.mutations.find(
      (request) => request.pathname === '/delivery/v1/admin/messages',
    );
    expect(sendTest?.method).toBe('POST');
    expect(sendTest?.search).toBe('?wait=10');
    const envelope = sendTest?.body as {
      app_id: string;
      to: { destination: string };
      channel: string;
      idempotency_key: string;
    };
    expect(envelope).toMatchObject({
      app_id: 'admin_test',
      to: { destination: 'hook1' },
      channel: 'test',
    });
    expect(envelope.idempotency_key).toMatch(UUID_PATTERN);

    // Rotate the signing secret and see the one-time reveal.
    await page.getByRole('button', { name: 'Edit hook1', exact: true }).click();
    await page.getByRole('button', { name: 'Rotate hook1', exact: true }).click();
    await page
      .getByRole('dialog', { name: 'Rotate signing secret?' })
      .getByRole('button', { name: 'Confirm rotate hook1', exact: true })
      .click();
    await expect(page.getByText('rotated_signing_secret_e2e_0123456789abcdef')).toBeVisible();
    const rotate = mock.mutations.find((request) => request.pathname.endsWith('/rotate-secret'));
    expect(rotate?.method).toBe('POST');
    expect(rotate?.pathname).toBe('/delivery/v1/admin/destinations/hook1/rotate-secret');
    expect(rotate?.body).toEqual({ overlap_hours: 24 });
    await page.getByRole('button', { name: 'Done / I have saved it' }).click();
    await expect(page.getByText('rotated_signing_secret_e2e_0123456789abcdef')).toBeHidden();

    // Disable through the confirm.
    await page.getByRole('button', { name: 'Disable hook1', exact: true }).click();
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Confirm disable hook1', exact: true })
      .click();
    await expect(page.getByRole('button', { name: 'Enable hook1', exact: true })).toBeVisible();
    const disable = mock.mutations.find((request) => request.method === 'PATCH');
    expect(disable?.pathname).toBe('/delivery/v1/admin/destinations/hook1');
    expect(disable?.body).toEqual({ enabled: false });

    expect(mock.unmocked).toEqual([]);
  });

  test('creates a route', async ({ page }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({
          name: 'hook1',
          type: 'webhook',
          secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
        }),
      ],
    });

    await page.goto('/#delivery');
    await expect(page.getByRole('cell', { name: 'hook1', exact: true })).toBeVisible();
    await expect(page.getByText('No routes yet.')).toBeVisible();

    await page.getByRole('button', { name: 'Add route', exact: true }).click();
    const form = page.getByRole('form', { name: 'Add route' });
    await form.getByLabel('App pattern').fill('urls4irl');
    await form.getByLabel('Channel pattern').fill('alerts*');
    await form.getByRole('checkbox', { name: 'hook1' }).check();
    await form.getByRole('button', { name: 'Add route', exact: true }).click();

    await expect(page.getByRole('cell', { name: 'urls4irl', exact: true })).toBeVisible();
    const create = mock.mutations.find(
      (request) => request.pathname === '/delivery/v1/admin/routes',
    );
    expect(create?.method).toBe('POST');
    expect(create?.body).toEqual({
      app_pattern: 'urls4irl',
      channel_pattern: 'alerts*',
      destinations: ['hook1'],
      enabled: true,
    });
    expect(mock.unmocked).toEqual([]);
  });

  test('delivery log: filter, attempt history and replay', async ({ page }) => {
    const mock = await mockDelivery(page, {
      deliveries: { dead: [deliveryWire({ id: 7 })] },
      attempts: {
        7: [
          {
            attempt_number: 1,
            outcome: 'http_error',
            status_code: 500,
            latency_ms: 812,
            error: null,
            response_snippet: 'upstream exploded',
            started_at: TIMESTAMP,
          },
        ],
      },
    });

    await page.goto('/#delivery');
    const log = page.getByRole('region', { name: 'Delivery log' });
    await expect(log.getByRole('cell', { name: /urls4irl/ })).toBeVisible();

    // Switch away and back to Dead; each switch reloads with the filter.
    await log.getByRole('button', { name: 'Pending', exact: true }).click();
    await expect(log.getByText('No pending deliveries.')).toBeVisible();
    await log.getByRole('button', { name: 'Dead', exact: true }).click();
    await expect(log.getByRole('cell', { name: /urls4irl/ })).toBeVisible();
    expect(mock.reads.map((request) => request.search)).toEqual(
      expect.arrayContaining(['?state=pending&limit=50', '?state=dead&limit=50']),
    );

    await log.getByRole('button', { name: 'Show attempts for delivery 7', exact: true }).click();
    await expect(log.getByText('upstream exploded')).toBeVisible();

    await log.getByRole('button', { name: 'Replay delivery 7', exact: true }).click();
    const replayDialog = page.getByRole('dialog', { name: 'Replay delivery?' });
    await expect(replayDialog).toContainText(/Delivery\s*7/i);
    await replayDialog
      .getByRole('button', { name: 'Confirm replay delivery 7', exact: true })
      .click();
    await expect(log.getByText(/Replay queued/)).toBeVisible();
    await expect(log.getByText('No dead deliveries.')).toBeVisible();

    const replay = mock.mutations.find((request) => request.pathname.endsWith('/replay'));
    expect(replay?.method).toBe('POST');
    expect(replay?.pathname).toBe('/delivery/v1/admin/deliveries/7/replay');
    expect(replay?.body).toBeNull();
    expect(mock.unmocked).toEqual([]);
  });

  test('pending sends: keep sends nothing, cancel needs two activations and reloads in place', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      scheduled: [
        scheduledWire({ id: 'msg-1', key: 'digest-1' }),
        scheduledWire({ id: 'msg-2', key: 'digest-2', deliverAt: '2099-02-01T08:00:00Z' }),
      ],
    });

    await page.goto('/#delivery');
    const pending = page.getByRole('region', { name: 'Pending sends' });
    await expect(pending.getByText('digest-1', { exact: true })).toBeVisible();
    await expect(pending.getByText('digest-2', { exact: true })).toBeVisible();

    const trigger = pending.getByRole('button', { name: 'Cancel send digest-1', exact: true });
    const refresh = pending.getByRole('button', { name: 'Refresh pending sends', exact: true });
    for (const button of [trigger, refresh]) {
      expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44);
    }

    // Keep restores the trigger and sends no request.
    await trigger.click();
    // The confirm is a body-level dialog, so its buttons are found on the page.
    const confirm = page.getByRole('button', {
      name: 'Confirm cancel send digest-1',
      exact: true,
    });
    const keep = page.getByRole('button', { name: 'Keep send digest-1', exact: true });
    await expect(confirm).toBeVisible();
    for (const button of [confirm, keep]) {
      expect((await button.boundingBox())?.height).toBeGreaterThanOrEqual(44);
    }
    // The sticky Actions cell is opaque (alpha 1) so scrolled content never shows through.
    const actionsCell = pending.locator('td.pending-sends__actions-cell').first();
    const alpha = await actionsCell.evaluate((element) => {
      const color = getComputedStyle(element).backgroundColor;
      const channels = /rgba?\(([^)]+)\)/.exec(color)?.[1].split(',') ?? [];
      return channels.length === 4 ? Number(channels[3]) : 1;
    });
    expect(alpha).toBe(1);
    await keep.click();
    await expect(trigger).toBeFocused();
    expect(mock.mutations).toEqual([]);

    // Cancel: trigger, then confirm.
    await trigger.click();
    await confirm.click();
    await expect(
      pending.getByText('Cancelled send digest-1. It will not be delivered.'),
    ).toBeVisible();
    await expect(pending.getByText('digest-1', { exact: true })).toBeHidden();
    await expect(pending.getByText('digest-2', { exact: true })).toBeVisible();
    await expect(
      pending.getByRole('button', { name: 'Cancel send digest-2', exact: true }),
    ).toBeFocused();

    expect(mock.mutations).toHaveLength(1);
    expect(mock.mutations[0].method).toBe('DELETE');
    expect(mock.mutations[0].pathname).toBe('/delivery/v1/admin/scheduled-messages/msg-1');
    expect(mock.mutations[0].body).toBeNull();
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats: pause is two-step, resume one click, delete hands focus to a neighbour', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      heartbeats: [
        heartbeatWire({ id: 1, name: 'nightly_backup' }),
        heartbeatWire({
          id: 2,
          name: 'hourly_gauge',
          state: 'down',
          lastPingKind: 'fail',
          downSince: '2026-10-18T14:00:00Z',
        }),
        heartbeatWire({
          id: 3,
          name: 'reaper',
          state: 'new',
          lastPingAt: null,
          lastPingKind: null,
        }),
      ],
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Heartbeats' });
    await expect(section.getByText('1 of 3 checks down')).toBeVisible();
    await expect(section.getByText('Waiting', { exact: true })).toBeVisible();
    await expect(section.getByText('reported failure')).toBeVisible();

    const pause = section.getByRole('button', {
      name: 'Pause urls4irl/nightly_backup',
      exact: true,
    });
    await expectCompactButton(pause);

    // Keep running sends nothing.
    await pause.click();
    await page.getByRole('button', { name: 'Keep running urls4irl/nightly_backup' }).click();
    await expect(pause).toBeFocused();
    expect(mock.mutations).toEqual([]);

    // Pause needs a confirm, then focus lands on the row's Resume.
    await pause.click();
    await page.getByRole('button', { name: 'Confirm pause urls4irl/nightly_backup' }).click();
    const resume = section.getByRole('button', {
      name: 'Resume urls4irl/nightly_backup',
      exact: true,
    });
    await expect(resume).toBeFocused();
    await expect(
      section.getByText(
        'Paused urls4irl/nightly_backup. Its alerts are suppressed until you resume it.',
      ),
    ).toBeVisible();

    // Resume is one click and focus returns to Pause.
    await resume.click();
    await expect(pause).toBeFocused();
    expect(mock.mutations.map((request) => request.pathname)).toEqual([
      '/delivery/v1/admin/heartbeats/1/pause',
      '/delivery/v1/admin/heartbeats/1/resume',
    ]);

    // Delete the last row: focus lands on the neighbour's first control.
    await section.getByRole('button', { name: 'Delete urls4irl/reaper', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm delete urls4irl/reaper' }).click();
    await expect(section.getByText('Deleted urls4irl/reaper.')).toBeVisible();
    await expect(
      section.getByRole('button', { name: 'Pause urls4irl/hourly_gauge', exact: true }),
    ).toBeFocused();
    expect(mock.mutations[2].method).toBe('DELETE');
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats: a stalled checker shows on the status card without changing its pill', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      readiness: {
        status: 200,
        body: {
          ...READY_BODY,
          heartbeat_checker: { last_tick_at: '2026-10-17T08:00:00Z', stale: true },
        },
      },
    });

    await page.goto('/#delivery');
    const statusCard = page.getByRole('region', { name: 'delivery-api' });
    await expect(statusCard.getByText('Ready', { exact: true })).toBeVisible();
    await expect(statusCard.getByText(/^Stalled · last run /)).toBeVisible();
    await expect(statusCard.getByText(/heartbeat checker has not run recently/)).toBeVisible();
    await expect(page.getByText('No heartbeat checks yet.')).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats: Add check is blocked without destinations', async ({ page }) => {
    // No destinations seeded: Add check stays blocked with its hint.
    const mock = await mockDelivery(page);
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Heartbeats' });
    await expect(section.getByText('No heartbeat checks yet.')).toBeVisible();
    const add = section.getByRole('button', { name: 'Add check', exact: true });
    await expect(add).toBeDisabled();
    await expect(add).toHaveAttribute('aria-disabled', 'true');
    await expect(section.getByText(/^Add a destination first\./)).toBeVisible();
    // Playwright would wait forever on an aria-disabled button; force the click.
    await add.click({ force: true });
    await expect(page.getByRole('form', { name: 'Add check' })).toHaveCount(0);
    expect(mock.mutations).toEqual([]);
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats: a failed list load shows an error, and Refresh recovers', async ({ page }) => {
    const mock = await mockDelivery(page, {
      heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })],
    });
    let failNext = true;
    // Registered after mockDelivery's catch-all, so it wins for this one URL.
    await page.route('**/delivery/v1/admin/heartbeats', async (route) => {
      if (failNext && route.request().method() === 'GET') {
        failNext = false;
        await route.fulfill({
          status: 502,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'upstream down' }),
        });
        return;
      }
      await route.fallback();
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Heartbeats' });
    await expect(section.getByRole('alert')).toContainText('upstream down');
    await section.getByRole('button', { name: 'Refresh heartbeats' }).click();
    await expect(
      section.getByRole('cell', { name: 'urls4irl / nightly_backup', exact: true }),
    ).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats: add validates on submit, creates a Waiting row, then pause and delete it', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({ name: 'hook1', type: 'webhook' }),
        destinationWire({ name: 'ops_discord', type: 'discord', enabled: false }),
      ],
      heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })],
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Heartbeats' });
    const add = section.getByRole('button', { name: 'Add check', exact: true });
    await expect(add).toBeEnabled();
    await expectCompactButton(add);
    await add.click();

    const form = section.getByRole('form', { name: 'Add check' });
    await expect(form.getByLabel('App ID')).toBeFocused();
    await expect(form.getByLabel('Period unit')).toHaveValue('hours');
    await expect(form.getByLabel('Grace unit')).toHaveValue('hours');
    await expect(form.getByLabel('Period', { exact: true })).toHaveValue('24');
    await expect(form.getByLabel('Grace', { exact: true })).toHaveValue('2');
    await expect(form.locator('label', { hasText: 'ops_discord' })).toContainText('(disabled)');

    // Submit-time validation: nothing is sent while it fails.
    await form.getByLabel('App ID').fill('minecrafting');
    await form.getByLabel('Check name').fill('daily_report');
    await form.getByRole('button', { name: 'Add check', exact: true }).click();
    await expect(form.locator('.destination-form__field-error')).toHaveText(
      'Select at least one alert destination.',
    );
    expect(mock.mutations).toEqual([]);

    // A duplicate surfaces the server's message and keeps the form open.
    await form.getByLabel('App ID').fill('urls4irl');
    await form.getByLabel('Check name').fill('nightly_backup');
    await form.getByRole('checkbox', { name: /^hook1/ }).check();
    await form.getByRole('button', { name: 'Add check', exact: true }).click();
    await expect(form.getByRole('alert')).toHaveText('check already exists');

    await form.getByLabel('App ID').fill('minecrafting');
    await form.getByLabel('Check name').fill('daily_report');
    await form.getByRole('button', { name: 'Add check', exact: true }).click();

    await expect(section.getByText('Added minecrafting/daily_report.')).toBeVisible();
    await expect(form).toHaveCount(0);
    await expect(add).toBeFocused();
    const createdRow = section.getByRole('row', { name: /minecrafting\s*\/\s*daily_report/ });
    await expect(createdRow.getByText('Waiting', { exact: true })).toBeVisible();
    const creates = mock.mutations.filter((request) => request.method === 'POST');
    expect(creates[creates.length - 1].body).toEqual({
      app_id: 'minecrafting',
      name: 'daily_report',
      period_seconds: 86400,
      grace_seconds: 7200,
      destinations: ['hook1'],
    });

    // Pause the new check: Paused pill, Resume button.
    await section
      .getByRole('button', { name: 'Pause minecrafting/daily_report', exact: true })
      .click();
    await page.getByRole('button', { name: 'Confirm pause minecrafting/daily_report' }).click();
    await expect(createdRow.getByText('Paused', { exact: true })).toBeVisible();
    await expect(
      section.getByRole('button', { name: 'Resume minecrafting/daily_report', exact: true }),
    ).toBeFocused();

    // Delete it: the row goes and focus lands on the neighbour's first control.
    await section
      .getByRole('button', { name: 'Delete minecrafting/daily_report', exact: true })
      .click();
    await page.getByRole('button', { name: 'Confirm delete minecrafting/daily_report' }).click();
    await expect(section.getByText('Deleted minecrafting/daily_report.')).toBeVisible();
    await expect(createdRow).toHaveCount(0);
    await expect(
      section.getByRole('button', { name: 'Pause urls4irl/nightly_backup', exact: true }),
    ).toBeFocused();
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats: edit sends only the changed fields and keeps a stale destination', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({ name: 'hook1', type: 'webhook' }),
        destinationWire({ name: 'family_ntfy', type: 'ntfy' }),
      ],
      heartbeats: [
        heartbeatWire({
          id: 1,
          name: 'nightly_backup',
          destinations: [
            { name: 'hook1', type: 'webhook', enabled: true },
            { name: 'old_hook', type: 'webhook', enabled: true },
          ],
        }),
        heartbeatWire({ id: 2, name: 'reaper' }),
      ],
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Heartbeats' });
    const edit = section.getByRole('button', {
      name: 'Edit urls4irl/nightly_backup',
      exact: true,
    });
    await edit.click();

    // The edit form lives in the shared dialog, portalled outside the section.
    const form = page
      .getByRole('dialog', { name: 'Edit urls4irl/nightly_backup', exact: true })
      .locator('form');
    await expect(form.getByLabel('Period', { exact: true })).toBeFocused();
    await expect(form.getByLabel('Period', { exact: true })).toHaveValue('24');
    await expect(form.getByLabel('Period unit')).toHaveValue('hours');
    await expect(form.getByLabel('Grace', { exact: true })).toHaveValue('1');
    await expect(form.getByText(/The app ID and name cannot change\./)).toBeVisible();
    await expect(form.getByLabel('App ID')).toHaveCount(0);
    const stale = form.getByRole('checkbox', { name: /^old_hook/ });
    await expect(stale).toBeChecked();
    await expect(stale).toBeVisible();
    await expect(form.locator('label', { hasText: 'old_hook' })).toContainText('(not found)');

    // Changing only the grace sends only the grace.
    await form.getByLabel('Grace', { exact: true }).fill('3');
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(section.getByText('Saved urls4irl/nightly_backup.')).toBeVisible();
    await expect(form).toHaveCount(0);
    await expect(edit).toBeFocused();
    const patches = mock.mutations.filter((request) => request.method === 'PATCH');
    expect(patches).toHaveLength(1);
    expect(patches[0].pathname).toBe('/delivery/v1/admin/heartbeats/1');
    expect(patches[0].body).toEqual({ grace_seconds: 10800 });

    // Changing the destinations sends the full set, stale name included.
    await edit.click();
    await form.getByRole('checkbox', { name: /^family_ntfy/ }).check();
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form.getByRole('alert')).toHaveText('unknown destination');
    await form.getByRole('checkbox', { name: /^old_hook/ }).uncheck();
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form).toHaveCount(0);
    const allPatches = mock.mutations.filter((request) => request.method === 'PATCH');
    expect(allPatches[1].body).toEqual({ destinations: ['hook1', 'family_ntfy', 'old_hook'] });
    expect(allPatches[2].body).toEqual({ destinations: ['hook1', 'family_ntfy'] });

    // Close without saving sends nothing and refocuses the trigger.
    await edit.click();
    await form.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(form).toHaveCount(0);
    await expect(edit).toBeFocused();
    expect(mock.mutations.filter((request) => request.method === 'PATCH')).toHaveLength(3);
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats at 390px: one kebab per row holds every action, cells stay on one line and the forms stay in view', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({ name: 'hook1', type: 'webhook' }),
        destinationWire({ name: 'ops_discord', type: 'discord' }),
        destinationWire({ name: 'family_ntfy', type: 'ntfy' }),
      ],
      heartbeats: [
        heartbeatWire({ id: 1, name: 'nightly_backup' }),
        heartbeatWire({ id: 2, name: 'reaper', paused: true }),
        heartbeatWire({ id: 3, name: 'owned', ownerKind: 'app', ownerAppId: 'chores4irl' }),
        heartbeatWire({ id: 4, name: 'mystery', ownerKind: null }),
      ],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Heartbeats' });
    const wrapper = section.locator('.heartbeats__scroll');
    await expect(wrapper).toBeVisible();
    await expectNoPageOverflow(page);

    // Nothing in the table wraps, and the wrapper (not the page) is what scrolls sideways.
    expect(await maxRenderedLines({ root: wrapper })).toBe(1);
    const sizes = await wrapper.evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
    }));
    expect(sizes.scrollWidth).toBeGreaterThan(sizes.clientWidth);

    // The Actions cell is the kebab and nothing else: no inline buttons, no notes, no confirms.
    await expect(wrapper.locator('tbody button')).toHaveCount(4);
    await expect(wrapper.locator('tbody .row-menu__trigger')).toHaveCount(4);
    await expect(
      section.getByRole('button', { name: /^(Edit|Pause|Resume|Delete) urls4irl/ }),
    ).toHaveCount(0);
    for (const row of ['nightly_backup', 'reaper', 'owned', 'mystery']) {
      const kebab = rowKebab({ page, row: `urls4irl/${row}` });
      await expectTouchTarget(kebab);
      // Pinned at the wrapper's end edge: wholly visible without scrolling the wrapper.
      const box = await kebab.boundingBox();
      const wrapperBox = await wrapper.boundingBox();
      if (box === null || wrapperBox === null) {
        throw new Error('kebab or wrapper has no bounding box');
      }
      expect(box.x + box.width).toBeLessThanOrEqual(wrapperBox.x + wrapperBox.width);
      expect(box.x).toBeGreaterThanOrEqual(wrapperBox.x);
    }

    // A paused check offers Resume (one selection), a running one Pause.
    let menu = await openRowMenu({ page, row: 'urls4irl/reaper' });
    await expect(menu.getByRole('menuitem', { name: 'Resume urls4irl/reaper' })).toBeVisible();
    await expect(menu.getByRole('menuitem', { name: /^Pause / })).toHaveCount(0);
    await page.keyboard.press('Escape');

    // A row with an owner note (app-owned, unknown): the short note is the menu's first line
    // and the long one is in the Pause and Delete confirms, which wrap inside the viewport;
    // the table row's text stays on one line throughout.
    for (const check of ['owned', 'mystery']) {
      const row = section.getByRole('row', { name: new RegExp(`urls4irl\\s*/\\s*${check}`) });
      menu = await openRowMenu({ page, row: `urls4irl/${check}` });
      const note = menu.locator('[role="presentation"]').first();
      await expect(note).toHaveText(
        check === 'owned'
          ? 'Edits are overridden on next deploy'
          : 'Owner unknown — edits may be overridden',
      );
      await expect(menu).not.toContainText(
        check === 'owned' ? 'Managed by chores4irl' : 'deploy may manage this',
      );
      await menu.getByRole('menuitem', { name: `Pause urls4irl/${check}`, exact: true }).click();
      const confirm = page.getByRole('dialog', { name: 'Pause check?', exact: true });
      await expect(confirm).toContainText('No alerts are sent while paused.');
      await expect(confirm).toContainText(
        check === 'owned' ? 'Managed by chores4irl' : 'deploy may manage this',
      );
      await expectFullScreenConfirm({
        page,
        dialog: confirm,
        buttons: [`Confirm pause urls4irl/${check}`, `Keep running urls4irl/${check}`],
      });
      expect(await maxRenderedLines({ root: row })).toBe(1);
      await page
        .getByRole('button', { name: `Keep running urls4irl/${check}`, exact: true })
        .click();
      await expect(confirm).toBeHidden();
      await expect(rowKebab({ page, row: `urls4irl/${check}` })).toBeFocused();
      await expectNoPageOverflow(page);
    }

    // The add card: no page overflow, 44px fields and buttons (the form is not the table).
    await section.getByRole('button', { name: 'Add check', exact: true }).click();
    const addForm = section.getByRole('form', { name: 'Add check' });
    await expect(addForm).toBeVisible();
    await expectNoPageOverflow(page);
    for (const control of [
      addForm.getByLabel('App ID'),
      addForm.getByLabel('Period', { exact: true }),
      addForm.getByLabel('Period unit'),
      addForm.getByLabel('Grace unit'),
      addForm.getByRole('button', { name: 'Add check', exact: true }),
      addForm.getByRole('button', { name: 'Cancel', exact: true }),
    ]) {
      const box = await control.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
      expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(390);
    }

    // The edit dialog, opened from the menu, takes focus and fits the viewport without
    // scrolling the page.
    menu = await openRowMenu({ page, row: 'urls4irl/nightly_backup' });
    await menu.getByRole('menuitem', { name: 'Edit urls4irl/nightly_backup', exact: true }).click();
    const editForm = page
      .getByRole('dialog', { name: 'Edit urls4irl/nightly_backup', exact: true })
      .locator('form');
    await expect(editForm).toBeVisible();
    await expect(editForm.locator(':focus')).toHaveCount(1);
    await expectNoPageOverflow(page);
    await expectDialogInViewport(page);
    const editBox = await editForm.boundingBox();
    expect(editBox?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((editBox?.x ?? 0) + (editBox?.width ?? 0)).toBeLessThanOrEqual(390);
    for (const control of [
      editForm.getByLabel('Period', { exact: true }),
      editForm.getByRole('button', { name: 'Save', exact: true }),
      editForm.getByRole('button', { name: 'Close', exact: true }),
    ]) {
      const box = await control.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
      expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(390);
    }
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });

  test('heartbeats at 390px: pause is two-step in the menu, resume one selection, delete hands focus to a neighbour kebab', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      heartbeats: [
        heartbeatWire({ id: 1, name: 'nightly_backup' }),
        heartbeatWire({
          id: 2,
          name: 'reaper',
          state: 'new',
          lastPingAt: null,
          lastPingKind: null,
        }),
      ],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'Heartbeats' });
    const first = 'urls4irl/nightly_backup';
    // Pause: Keep running sends nothing; Confirm pauses and focus stays on the kebab.
    let menu = await openRowMenu({ page, row: first });
    await menu.getByRole('menuitem', { name: `Pause ${first}`, exact: true }).click();
    await expectFullScreenConfirm({
      page,
      dialog: page.getByRole('dialog', { name: 'Pause check?', exact: true }),
      buttons: [`Confirm pause ${first}`, `Keep running ${first}`],
    });
    await page.getByRole('button', { name: `Keep running ${first}`, exact: true }).click();
    await expect(rowKebab({ page, row: first })).toBeFocused();
    expect(mock.mutations).toEqual([]);
    menu = await openRowMenu({ page, row: first });
    await menu.getByRole('menuitem', { name: `Pause ${first}`, exact: true }).click();
    await page.getByRole('button', { name: `Confirm pause ${first}`, exact: true }).click();
    await expect(menu).toBeHidden();
    await expect(rowKebab({ page, row: first })).toBeFocused();
    await expect(
      section.getByText(`Paused ${first}. Its alerts are suppressed until you resume it.`),
    ).toBeVisible();

    // Resume is one selection.
    menu = await openRowMenu({ page, row: first });
    await menu.getByRole('menuitem', { name: `Resume ${first}`, exact: true }).click();
    await expect(rowKebab({ page, row: first })).toBeFocused();
    await expect(section.getByText(`Resumed ${first}.`)).toBeVisible();
    expect(mock.mutations.map((request) => request.pathname)).toEqual([
      '/delivery/v1/admin/heartbeats/1/pause',
      '/delivery/v1/admin/heartbeats/1/resume',
    ]);

    // Delete the last row: focus lands on the neighbour's kebab.
    menu = await openRowMenu({ page, row: 'urls4irl/reaper' });
    await menu.getByRole('menuitem', { name: 'Delete urls4irl/reaper', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm delete urls4irl/reaper', exact: true }).click();
    await expect(section.getByText('Deleted urls4irl/reaper.')).toBeVisible();
    await expect(rowKebab({ page, row: first })).toBeFocused();
    expect(mock.mutations[2].method).toBe('DELETE');
    expect(mock.unmocked).toEqual([]);
  });

  test('app identities: approve, reject, revoke and unbind move focus as designed and show hints only', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      identityRequests: [
        identityRequestWire({ id: 1, appId: 'urls4irl' }),
        identityRequestWire({ id: 2, appId: 'chores4irl' }),
        identityRequestWire({ id: 3, appId: 'oldapp', status: 'rejected' }),
      ],
      identityBindings: [
        identityBindingWire({
          appId: 'tasktracker',
          keys: [
            identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' }),
            identityKeyWire({
              id: 12,
              appId: 'tasktracker',
              hint: 'dak_Kf09',
              state: 'superseded',
            }),
          ],
        }),
      ],
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'App identities' });
    await expect(section.getByText('2 requests awaiting approval')).toBeVisible();
    // A request already rejected on load has only its status pill, never Approve.
    await expect(section.locator('tr[data-request-id="3"]').getByRole('button')).toHaveCount(0);
    await expect(section.getByText('Superseded', { exact: true })).toBeVisible();

    // Approve removes the row and focus moves to the neighbouring request's Approve.
    const approve = section.getByRole('button', {
      name: 'Approve identity request for urls4irl',
      exact: true,
    });
    await expectCompactButton(approve);
    await approve.click();
    await expect(
      section.getByText('Approved urls4irl for 4IRL/urls4irl. Its next deploy receives a key.'),
    ).toBeVisible();
    await expect(
      section.getByRole('button', { name: 'Approve identity request for chores4irl', exact: true }),
    ).toBeFocused();
    await expect(
      section.getByRole('button', { name: 'Unbind urls4irl', exact: true }),
    ).toBeVisible();
    await expect(section.getByText('1 request awaiting approval')).toBeVisible();

    // Reject needs a confirm; the row's Actions become the Rejected pill and focus goes to the heading.
    await section
      .getByRole('button', { name: 'Reject identity request for chores4irl', exact: true })
      .click();
    await expect(
      page
        .getByRole('dialog', { name: 'Reject identity request?' })
        .getByText('The deploy keeps failing until a new request is approved.'),
    ).toBeVisible();
    await page
      .getByRole('button', { name: 'Confirm reject identity request for chores4irl', exact: true })
      .click();
    await expect(section.getByRole('heading', { name: 'Requests' })).toBeFocused();
    await expect(section.locator('tr[data-request-id="2"]').getByRole('button')).toHaveCount(0);

    // Revoke needs a confirm; the key's button becomes a pill and focus goes to that app's Unbind.
    await section
      .getByRole('button', { name: 'Revoke key dak_7Q2m for tasktracker', exact: true })
      .click();
    await page
      .getByRole('button', { name: 'Confirm revoke key dak_7Q2m for tasktracker', exact: true })
      .click();
    await expect(
      section.getByRole('button', { name: 'Unbind tasktracker', exact: true }),
    ).toBeFocused();
    await expect(
      section.getByRole('button', { name: 'Revoke key dak_7Q2m for tasktracker', exact: true }),
    ).toHaveCount(0);

    // Unbind removes the row; focus goes to the neighbouring binding's first control.
    await section.getByRole('button', { name: 'Unbind tasktracker', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm unbind tasktracker', exact: true }).click();
    await expect(section.getByText('Unbound tasktracker. Its keys are revoked.')).toBeVisible();
    await expect(
      section.getByRole('button', { name: 'Unbind urls4irl', exact: true }),
    ).toBeFocused();

    // Only a key's hint was ever on the page.
    await expect(page.locator('body')).not.toContainText(/dak_[A-Za-z0-9_-]{9,}/);
    expect(mock.mutations.map((request) => `${request.method} ${request.pathname}`)).toEqual([
      'POST /delivery/v1/admin/app-identity/requests/1/approve',
      'POST /delivery/v1/admin/app-identity/requests/2/reject',
      'POST /delivery/v1/admin/app-identity/keys/11/revoke',
      'DELETE /delivery/v1/admin/app-identity/bindings/tasktracker',
    ]);
    expect(mock.unmocked).toEqual([]);
  });

  test('app identities: an approve that answers 409 shows the server error and keeps focus on the row', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      identityRequests: [identityRequestWire({ id: 1, appId: 'urls4irl' })],
      approveError: { status: 409, message: 'request is not pending' },
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'App identities' });
    const approve = section.getByRole('button', {
      name: 'Approve identity request for urls4irl',
      exact: true,
    });
    await approve.click();

    const row = section.locator('tr[data-request-id="1"]');
    await expect(row.getByRole('alert')).toHaveText('Could not approve: request is not pending');
    await expect(approve).toBeFocused();
    await expect(approve).not.toHaveAttribute('aria-disabled', 'true');
    expect(mock.mutations).toHaveLength(1);
    expect(mock.unmocked).toEqual([]);
  });

  test('app identities: an approve refused for overlapping an approved app id shows the reason and keeps the request', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      identityRequests: [identityRequestWire({ id: 1, appId: 'urls4irl_beta' })],
      approveError: { status: 409, message: 'app id overlaps an approved app id' },
    });

    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'App identities' });
    const approve = section.getByRole('button', {
      name: 'Approve identity request for urls4irl_beta',
      exact: true,
    });
    await approve.click();

    const row = section.locator('tr[data-request-id="1"]');
    await expect(row.getByRole('alert')).toHaveText(
      'Could not approve: app id overlaps an approved app id',
    );
    await expect(approve).toBeFocused();
    await expect(approve).not.toHaveAttribute('aria-disabled', 'true');
    expect(mock.mutations).toHaveLength(1);
    expect(mock.unmocked).toEqual([]);
  });

  test('app identities: a failed load shows an error and Retry recovers', async ({ page }) => {
    const mock = await mockDelivery(page, {
      identityRequests: [identityRequestWire({ id: 1, appId: 'urls4irl' })],
    });
    let failures = 1;
    await page.route('**/delivery/v1/admin/app-identity/requests', async (route) => {
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
    const section = page.getByRole('region', { name: 'App identities' });
    await expect(section.getByRole('alert')).toContainText(
      'Could not load app identities (delivery-api unavailable).',
    );
    await section.getByRole('button', { name: 'Retry', exact: true }).click();
    await expect(
      section.getByRole('button', { name: 'Approve identity request for urls4irl', exact: true }),
    ).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('app identities at 390px: every action is in a row menu, the rows keep their colours and nothing wraps or widens', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      identityRequests: [
        identityRequestWire({ id: 1, appId: 'urls4irl' }),
        identityRequestWire({ id: 2, appId: 'oldapp', status: 'rejected' }),
        identityRequestWire({ id: 3, appId: 'mystery', status: 'weird' }),
      ],
      identityBindings: [
        identityBindingWire({
          appId: 'tasktracker',
          keys: [
            identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' }),
            identityKeyWire({ id: 13, appId: 'tasktracker', hint: 'dak_a1Zx', state: 'revoked' }),
          ],
        }),
      ],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'App identities' });
    const wrappers = section.locator('.app-identities__scroll');
    await expect(wrappers).toHaveCount(2);
    await expectNoPageOverflow(page);

    // Nothing wraps (the unrecognised-status note included), and each wrapper, not the page,
    // is what scrolls sideways.
    for (let index = 0; index < 2; index += 1) {
      expect(await maxRenderedLines({ root: wrappers.nth(index) })).toBe(1);
      const sizes = await wrappers.nth(index).evaluate((element) => ({
        scrollWidth: element.scrollWidth,
        clientWidth: element.clientWidth,
      }));
      expect(sizes.scrollWidth).toBeGreaterThan(sizes.clientWidth);
    }
    // The unrecognised status explains itself under its pill (its Actions cell is empty), the
    // rejected row has no kebab, and no inline Approve / Reject / Revoke / Unbind button exists.
    await expect(section.locator('tr[data-request-id="3"]')).toContainText(
      'does not recognise the status',
    );
    await expect(section.locator('tr[data-request-id="2"] button')).toHaveCount(0);
    await expect(section.locator('tr[data-request-id="3"] button')).toHaveCount(0);
    // The rejected row's pill (its status; there is no Actions pill any more) is visible and
    // unclipped without scrolling the wrapper sideways.
    const rejectedPill = section
      .locator('tr[data-request-id="2"]')
      .getByText('Rejected', { exact: true });
    await expect(rejectedPill).toBeVisible();
    const pillBox = await rejectedPill.boundingBox();
    const requestsBox = await wrappers.nth(0).boundingBox();
    if (pillBox === null || requestsBox === null) {
      throw new Error('pill or wrapper has no bounding box');
    }
    expect(pillBox.x).toBeGreaterThanOrEqual(requestsBox.x);
    expect(pillBox.x + pillBox.width).toBeLessThanOrEqual(requestsBox.x + requestsBox.width);
    await expect(
      section.getByRole('button', { name: /^(Approve|Reject|Revoke|Unbind) /, exact: false }),
    ).toHaveCount(0);

    // The key rows read in full beside their app: pill, hint and last use, one line each.
    await expect(section.getByText('dak_7Q2m…')).toBeVisible();

    // Approve, Reject, Revoke and Unbind are items of the row menus.
    let menu = await openRowMenu({ page, row: 'urls4irl' });
    for (const name of [
      'Approve identity request for urls4irl',
      'Reject identity request for urls4irl',
    ]) {
      await expectTouchTarget(menu.getByRole('menuitem', { name, exact: true }));
    }
    await page.keyboard.press('Escape');
    menu = await openRowMenu({ page, row: 'tasktracker' });
    // The revoked key has no Revoke item.
    await expect(menu.getByRole('menuitem')).toHaveCount(2);
    for (const name of ['Revoke key dak_7Q2m for tasktracker', 'Unbind tasktracker']) {
      await expectTouchTarget(menu.getByRole('menuitem', { name, exact: true }));
    }
    await page.keyboard.press('Escape');

    // The pending row is one accent colour across every cell, the pinned kebab included (it
    // is opaque, so scrolled cells never show through it); other rows are the page background.
    const background = (locator: Locator) =>
      locator.evaluate((element) => {
        const style = getComputedStyle(element);
        return { background: style.backgroundColor, position: style.position };
      });
    const pendingRow = section.locator('tr[data-request-id="1"]');
    expect(await background(pendingRow.locator('td').first())).toEqual({
      background: 'rgb(42, 35, 32)',
      position: 'static',
    });
    expect(await background(pendingRow.locator('td.admin-table__kebab'))).toEqual({
      background: 'rgb(42, 35, 32)',
      position: 'sticky',
    });
    expect(await background(section.locator('tr[data-request-id="2"] td').first())).toEqual({
      background: 'rgb(26, 26, 26)',
      position: 'static',
    });

    // A Reject confirm is a step inside the menu: it adds nothing to the table.
    const widthBefore = await wrappers.nth(0).evaluate((element) => element.scrollWidth);
    menu = await openRowMenu({ page, row: 'urls4irl' });
    await menu.getByRole('menuitem', { name: 'Reject identity request for urls4irl' }).click();
    const confirm = page.getByRole('dialog', { name: 'Reject identity request?', exact: true });
    await expect(confirm).toContainText(
      'The deploy keeps failing until a new request is approved.',
    );
    await expectFullScreenConfirm({
      page,
      dialog: confirm,
      buttons: [
        'Confirm reject identity request for urls4irl',
        'Cancel reject identity request for urls4irl',
      ],
    });
    await page
      .getByRole('button', { name: 'Cancel reject identity request for urls4irl', exact: true })
      .click();
    await expect(confirm).toBeHidden();
    expect(await wrappers.nth(0).evaluate((element) => element.scrollWidth)).toBe(widthBefore);
    expect(await maxRenderedLines({ root: wrappers.nth(0) })).toBe(1);
    await expectNoPageOverflow(page);
    expect(mock.mutations).toEqual([]);
    expect(mock.unmocked).toEqual([]);
  });

  test('app identities at 390px: approve, reject, revoke and unbind from the menus move focus as designed', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      identityRequests: [
        identityRequestWire({ id: 1, appId: 'urls4irl' }),
        identityRequestWire({ id: 2, appId: 'chores4irl' }),
      ],
      identityBindings: [
        identityBindingWire({
          appId: 'tasktracker',
          keys: [identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' })],
        }),
      ],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'App identities' });

    // Approve removes the row; focus moves to the neighbouring request's kebab.
    let menu = await openRowMenu({ page, row: 'urls4irl' });
    await menu.getByRole('menuitem', { name: 'Approve identity request for urls4irl' }).click();
    await expect(
      section.getByText('Approved urls4irl for 4IRL/urls4irl. Its next deploy receives a key.'),
    ).toBeVisible();
    await expect(rowKebab({ page, row: 'chores4irl' })).toBeFocused();
    await expect(rowKebab({ page, row: 'urls4irl' })).toBeVisible();

    // Reject needs a confirm in the menu; the row loses its kebab and focus goes to the heading.
    menu = await openRowMenu({ page, row: 'chores4irl' });
    await menu.getByRole('menuitem', { name: 'Reject identity request for chores4irl' }).click();
    await page
      .getByRole('button', { name: 'Confirm reject identity request for chores4irl', exact: true })
      .click();
    await expect(section.getByRole('heading', { name: 'Requests' })).toBeFocused();
    await expect(section.locator('tr[data-request-id="2"] button')).toHaveCount(0);

    // Revoke needs a confirm in the menu; focus returns to that app's kebab (it holds Unbind).
    menu = await openRowMenu({ page, row: 'tasktracker' });
    await menu.getByRole('menuitem', { name: 'Revoke key dak_7Q2m for tasktracker' }).click();
    await page
      .getByRole('button', { name: 'Confirm revoke key dak_7Q2m for tasktracker', exact: true })
      .click();
    await expect(rowKebab({ page, row: 'tasktracker' })).toBeFocused();
    menu = await openRowMenu({ page, row: 'tasktracker' });
    await expect(menu.getByRole('menuitem')).toHaveCount(1);

    // Unbind removes the row; focus goes to the neighbouring binding's kebab.
    await menu.getByRole('menuitem', { name: 'Unbind tasktracker' }).click();
    await page.getByRole('button', { name: 'Confirm unbind tasktracker', exact: true }).click();
    await expect(section.getByText('Unbound tasktracker. Its keys are revoked.')).toBeVisible();
    await expect(rowKebab({ page, row: 'urls4irl' })).toBeFocused();

    await expect(page.locator('body')).not.toContainText(/dak_[A-Za-z0-9_-]{9,}/);
    expect(mock.mutations.map((request) => `${request.method} ${request.pathname}`)).toEqual([
      'POST /delivery/v1/admin/app-identity/requests/1/approve',
      'POST /delivery/v1/admin/app-identity/requests/2/reject',
      'POST /delivery/v1/admin/app-identity/keys/11/revoke',
      'DELETE /delivery/v1/admin/app-identity/bindings/tasktracker',
    ]);
    expect(mock.unmocked).toEqual([]);
  });

  test('app identities: a long revoke error reads in the menu, flags the kebab and never widens the table at 390px', async ({
    page,
  }) => {
    const longMessage = 'revoke-unavailable-'.repeat(12);
    const mock = await mockDelivery(page, {
      identityBindings: [
        identityBindingWire({
          appId: 'tasktracker',
          keys: [identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' })],
        }),
      ],
      revokeError: { status: 503, message: longMessage },
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    const section = page.getByRole('region', { name: 'App identities' });
    const wrapper = section.locator('.app-identities__scroll');
    const widthBefore = await wrapper.evaluate((element) => element.scrollWidth);
    // The key row has no Revoke button of its own at this width.
    await expect(
      section.getByRole('button', { name: 'Revoke key dak_7Q2m for tasktracker', exact: true }),
    ).toHaveCount(0);

    const menu = await openRowMenu({ page, row: 'tasktracker' });
    await menu.getByRole('menuitem', { name: 'Revoke key dak_7Q2m for tasktracker' }).click();
    await page
      .getByRole('button', { name: 'Confirm revoke key dak_7Q2m for tasktracker', exact: true })
      .click();

    // Announced (visually hidden alert), flagged on the kebab, focus stays on the kebab ...
    const alert = page.getByRole('alert');
    await expect(alert).toContainText('Could not revoke: revoke-unavailable-');
    const kebab = rowKebab({ page, row: 'tasktracker' });
    await expect(kebab).toHaveAttribute('data-has-error', 'true');
    await expect(kebab).toBeFocused();
    // ... and nothing about the table grew: the error is not in the row.
    expect(await wrapper.evaluate((element) => element.scrollWidth)).toBe(widthBefore);
    expect(await maxRenderedLines({ root: wrapper, skip: '.row-menu__sr' })).toBe(1);
    await expectNoPageOverflow(page);

    // The text is in the menu, which wraps it inside the viewport.
    const errorMenu = await openRowMenu({ page, row: 'tasktracker' });
    await expect(errorMenu).toContainText('Could not revoke: revoke-unavailable-');
    await expectInViewport({ page, locator: errorMenu });
    await expectFullyPainted(errorMenu);
    expect(mock.unmocked).toEqual([]);
  });

  test('pending sends: a failed cancel shows its error in the row', async ({ page }) => {
    const mock = await mockDelivery(page, {
      scheduled: [scheduledWire({ id: 'msg-1', key: 'digest-1' })],
      cancelError: { status: 503, message: 'delivery-service-unavailable-'.repeat(12) },
    });

    await page.goto('/#delivery');
    const pending = page.getByRole('region', { name: 'Pending sends' });
    await pending.getByRole('button', { name: 'Cancel send digest-1', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm cancel send digest-1', exact: true }).click();

    const rowError = pending.getByRole('alert');
    await expect(rowError).toContainText('Could not cancel: delivery-service-unavailable-');
    expect(mock.unmocked).toEqual([]);
  });

  test('pending sends: the page is wide and no cell wraps at 1280px', async ({ page }) => {
    const mock = await mockDelivery(page, {
      scheduled: [
        scheduledWire({ id: 'msg-1', key: 'digest-weekly-2099-01-01' }),
        scheduledWire({ id: 'msg-2', key: 'digest-weekly-2099-02-01' }),
      ],
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    // `.pending-sends__scroll` only mounts once the initial fetch resolves and
    // `loading` flips false (see PendingSends.tsx); register the wait before
    // navigating (which triggers the fetch) so a slow response under host
    // contention is awaited deterministically instead of padding the
    // visibility timeout.
    const scheduledMessagesLoaded = page.waitForResponse(
      (response) =>
        response.url().includes('/delivery/v1/admin/scheduled-messages') &&
        response.request().method() === 'GET',
    );
    await page.goto('/#delivery');
    await scheduledMessagesLoaded;
    const pending = page.getByRole('region', { name: 'Pending sends' });
    const wrapper = pending.locator('.pending-sends__scroll');
    await expect(wrapper).toBeVisible();

    // Wider than the old 60rem (960px) content column.
    expect((await pending.boundingBox())?.width ?? 0).toBeGreaterThan(1100);
    const wrapperBox = await wrapper.evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
    }));
    expect(wrapperBox.scrollWidth).toBeLessThanOrEqual(wrapperBox.clientWidth);
    expect(await maxRenderedLines({ root: pending.locator('table') })).toBe(1);

    // Each row is as tall as its two-line cells (name plus sub-line), not taller.
    const rowHeights = await pending
      .locator('tbody tr')
      .evaluateAll((rows) => rows.map((row) => row.getBoundingClientRect().height));
    for (const height of rowHeights) {
      expect(height).toBeLessThan(90);
    }
    expect(mock.unmocked).toEqual([]);
  });

  test('pending sends: the State pill is vertically centred in its multi-line row', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      scheduled: [scheduledWire({ id: 'msg-1', key: 'digest-weekly-2099-01-01' })],
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    // Same fetch-dependent mount as the "page is wide" test above: wait for
    // the scheduled-messages response before asserting, rather than relying
    // on the default visibility timeout to also cover the fetch.
    const scheduledMessagesLoaded = page.waitForResponse(
      (response) =>
        response.url().includes('/delivery/v1/admin/scheduled-messages') &&
        response.request().method() === 'GET',
    );
    await page.goto('/#delivery');
    await scheduledMessagesLoaded;
    const pending = page.getByRole('region', { name: 'Pending sends' });
    const row = pending.locator('tbody tr').first();
    await expectCenteredInRow({ badge: row.locator('.pending-sends__pill'), row });
    expect(mock.unmocked).toEqual([]);
  });

  test('delivery log: the status pill is vertically centred in its multi-line row', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      deliveries: { delivered: [deliveryWire({ id: 8, state: 'delivered' })] },
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/#delivery');
    const log = page.getByRole('region', { name: 'Delivery log' });
    await log.getByRole('button', { name: 'Delivered', exact: true }).click();
    const row = log.locator('.delivery-log__table > tbody > tr').first();
    await expectCenteredInRow({ badge: row.locator('.delivery-log__pill'), row });
    expect(mock.unmocked).toEqual([]);
  });

  test('destinations and routes: the status pills are vertically centred in their rows', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({
          name: 'hook1',
          type: 'webhook',
          secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
        }),
      ],
      routes: [routeWire({ id: 1 })],
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/#delivery');
    const destinationRow = page.locator('.destinations__table tbody tr').first();
    await expectCenteredInRow({
      badge: destinationRow.locator('.destinations__pill'),
      row: destinationRow,
    });
    const routeRow = page.locator('.routes__table tbody tr').first();
    await expectCenteredInRow({ badge: routeRow.locator('.routes__pill'), row: routeRow });
    expect(mock.unmocked).toEqual([]);
  });

  test('owner badges: operator, app-owned and unknown rows are centred and noted', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({ name: 'ops_hook', type: 'webhook' }),
        destinationWire({
          name: 'chores4irl_hook',
          type: 'webhook',
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        destinationWire({ name: 'mystery_hook', type: 'webhook', ownerKind: null }),
      ],
      routes: [
        routeWire({ id: 1, destinations: ['ops_hook'] }),
        routeWire({
          id: 2,
          appPattern: 'chores4irl',
          destinations: ['chores4irl_hook'],
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        routeWire({ id: 3, destinations: ['ops_hook'], ownerKind: 'robot' }),
      ],
      heartbeats: [
        heartbeatWire({ id: 1, name: 'nightly_backup', destinations: [] }),
        heartbeatWire({
          id: 2,
          appId: 'chores4irl',
          name: 'daily',
          destinations: [],
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        heartbeatWire({
          id: 3,
          name: 'mystery',
          destinations: [],
          ownerKind: 'app',
          ownerAppId: null,
        }),
      ],
    });

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/#delivery');

    const managed =
      "Managed by chores4irl's deploy — operator edits are reverted on its next deploy.";
    // The line under the row's buttons is the short form: no app id (the badge names it), and
    // the long text stays in the confirm notice and the row menu.
    const managedInline = 'Edits are overridden on next deploy';
    const unknownInline = 'Owner unknown — edits may be overridden';
    for (const table of ['.destinations__table', '.routes__table', '.heartbeats__table']) {
      const body = page.locator(`${table} tbody tr`);
      await expect(body).toHaveCount(3);
      const operator = body.nth(0);
      const app = body.nth(1);
      const unknown = body.nth(2);

      await expect(operator.locator('.owner-badge--operator')).toHaveText('Owner: Operator');
      await expect(operator.locator('.admin-table__note')).toHaveCount(0);
      await expect(app.locator('.owner-badge--app')).toHaveText('Owner: App · chores4irl');
      await expect(app.locator('.admin-table__note')).toHaveText(managedInline);
      await expect(app.getByText(managed)).toHaveCount(0);
      await expect(unknown.locator('.owner-badge--unknown')).toHaveText('Owner: Unknown');
      await expect(unknown.locator('.owner-badge--operator')).toHaveCount(0);
      await expect(unknown.locator('.admin-table__note')).toHaveText(unknownInline);

      for (const row of [operator, app, unknown]) {
        await expectCenteredInRow({ badge: row.locator('.owner-badge'), row });
        // Roomy layout: the one badge lives in the Owner column, never also under the value.
        await expect(row.locator('.owner-badge')).toHaveCount(1);
        await expect(row.locator('.admin-table__owner-line')).toHaveCount(0);
      }
      // The Owner column header is present and the visible badge text is real text.
      await expect(
        page.locator(`${table} thead`).getByRole('columnheader', { name: 'Owner' }),
      ).toBeVisible();
    }

    // The Check column keeps the bare app id; only the Owner badge carries the prefix.
    const checkRow = page.locator('.heartbeats__table tbody tr').nth(1);
    await expect(checkRow.locator('.heartbeats__mono').first()).toHaveText('chores4irl');

    // Deleting an app-owned row repeats the note on the confirm.
    await page.getByRole('button', { name: 'Delete route 2', exact: true }).click();
    await expect(page.getByRole('dialog').locator('.modal__notice')).toHaveText(managed);
    await page.getByRole('button', { name: 'Cancel Delete route 2', exact: true }).click();

    // An open editor row spans every column, Owner included.
    await page.getByRole('button', { name: 'Edit chores4irl_hook', exact: true }).click();
    const headerCount = await page.locator('.destinations__table thead th').count();
    const editorCell = page.locator('.destinations__table tbody td[colspan]');
    await expect(editorCell).toHaveAttribute('colspan', String(headerCount));
    expect(mock.unmocked).toEqual([]);
  });

  test('owner badges at 390px: the badge sits under the first cell value, in view at scroll 0, with no Owner column', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({ name: 'ops_hook', type: 'webhook' }),
        destinationWire({
          name: 'chores4irl_hook',
          type: 'webhook',
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        destinationWire({ name: 'mystery_hook', type: 'webhook', ownerKind: null }),
      ],
      routes: [
        routeWire({ id: 1, destinations: ['ops_hook'] }),
        routeWire({
          id: 2,
          appPattern: 'chores4irl',
          destinations: ['chores4irl_hook'],
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        routeWire({ id: 3, destinations: ['ops_hook'], ownerKind: 'robot' }),
      ],
      heartbeats: [
        heartbeatWire({ id: 1, name: 'nightly_backup', destinations: [] }),
        heartbeatWire({
          id: 2,
          appId: 'chores4irl',
          name: 'daily',
          destinations: [],
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        heartbeatWire({
          id: 3,
          name: 'mystery',
          destinations: [],
          ownerKind: 'app',
          ownerAppId: null,
        }),
      ],
      identityBindings: [
        identityBindingWire({
          appId: 'tasktracker',
          keys: [identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' })],
        }),
      ],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');

    const expectedBadges = [
      { kind: 'operator', text: 'Owner: Operator' },
      { kind: 'app', text: 'Owner: App · chores4irl' },
      { kind: 'unknown', text: 'Owner: Unknown' },
    ];
    // Each table's columns on the phone: the Owner column is gone (its badge moved under the
    // first cell's value), the Actions column (the kebab) stays.
    const tables = [
      { table: '.destinations__table', scroll: '.destinations__scroll', columns: 5 },
      { table: '.routes__table', scroll: '.routes__scroll', columns: 5 },
      { table: '.heartbeats__table', scroll: '.heartbeats__scroll', columns: 6 },
    ];
    for (const { table, scroll, columns } of tables) {
      const wrapper = page.locator(scroll);
      await expect(wrapper).toBeVisible();
      await wrapper.scrollIntoViewIfNeeded();
      await expect(page.locator(`${table} thead th`)).toHaveCount(columns);
      await expect(
        page.locator(`${table} thead`).getByRole('columnheader', { name: 'Owner' }),
      ).toHaveCount(0);
      const rows = page.locator(`${table} tbody tr`);
      await expect(rows).toHaveCount(3);
      // Exactly one badge per row, never a second copy beside the first cell.
      await expect(page.locator(`${table} tbody .owner-badge`)).toHaveCount(3);

      for (const [index, { kind, text }] of expectedBadges.entries()) {
        const row = rows.nth(index);
        await expect(row.locator('td')).toHaveCount(columns);
        const firstCell = row.locator('td').first();
        const badge = firstCell.locator(`.owner-badge--${kind}`);
        await expect(badge).toHaveText(text);
        await expect(row.locator('.owner-badge--operator')).toHaveCount(
          kind === 'operator' ? 1 : 0,
        );

        // Visible at scroll position 0: no sideways scrolling needed to see who owns the row
        // (the scroll-into-view is vertical only; it would move scrollLeft if the badge were
        // off to the side, which the assertion after it catches).
        await badge.scrollIntoViewIfNeeded();
        expect(await wrapper.evaluate((element) => element.scrollLeft)).toBe(0);
        const badgeBox = await badge.boundingBox();
        const wrapperBox = await wrapper.boundingBox();
        if (badgeBox === null || wrapperBox === null) {
          throw new Error('badge or wrapper has no bounding box');
        }
        expect(badgeBox.x).toBeGreaterThanOrEqual(wrapperBox.x);
        expect(badgeBox.x + badgeBox.width).toBeLessThanOrEqual(
          Math.min(wrapperBox.x + wrapperBox.width, 390),
        );
        expect(badgeBox.y).toBeGreaterThanOrEqual(wrapperBox.y);
        expect(badgeBox.y + badgeBox.height).toBeLessThanOrEqual(wrapperBox.y + wrapperBox.height);
        // Nothing (the pinned header, the kebab column) covers the badge's centre.
        expect(
          await badge.evaluate((element) => {
            const rect = element.getBoundingClientRect();
            const hit = document.elementFromPoint(
              rect.left + rect.width / 2,
              rect.top + rect.height / 2,
            );
            return hit !== null && element.contains(hit);
          }),
        ).toBe(true);

        // Its own line, under the value (never beside it) and never wrapping.
        const layout = await firstCell.evaluate((cell) => {
          const line = cell.querySelector('.admin-table__owner-line');
          const pill = cell.querySelector('.owner-badge');
          if (line === null || pill === null) {
            throw new Error('first cell has no owner line');
          }
          let valueBottom = 0;
          const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
          for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
            if ((node.textContent ?? '').trim() === '' || line.contains(node)) {
              continue;
            }
            const range = document.createRange();
            range.selectNodeContents(node);
            valueBottom = Math.max(valueBottom, range.getBoundingClientRect().bottom);
          }
          return {
            lineDisplay: getComputedStyle(line).display,
            valueBottom,
            badgeTop: pill.getBoundingClientRect().top,
          };
        });
        expect(layout.lineDisplay).toBe('block');
        // One pill-high line: the badge never wraps.
        expect(badgeBox.height).toBeLessThan(30);
        expect(layout.valueBottom).toBeGreaterThan(0);
        expect(layout.valueBottom).toBeLessThanOrEqual(layout.badgeTop + 1);
      }
    }

    // An open editor row spans every column of the narrow table (the kebab's included) and
    // stays inside the visible width.
    for (const { row, editName, table, scroll, columns } of [
      {
        row: 'chores4irl_hook',
        editName: 'Edit chores4irl_hook',
        table: '.destinations__table',
        scroll: '.destinations__scroll',
        columns: 5,
      },
      {
        row: 'route 2',
        editName: 'Edit route 2',
        table: '.routes__table',
        scroll: '.routes__scroll',
        columns: 5,
      },
    ]) {
      const menu = await openRowMenu({ page, row });
      await menu.getByRole('menuitem', { name: editName, exact: true }).click();
      const editorCell = page.locator(`${table} tbody td[colspan]`);
      await expect(editorCell).toHaveAttribute('colspan', String(columns));
      await expect(page.locator(`${table} thead th`)).toHaveCount(columns);
      await expectEditorInView({
        wrapper: page.locator(scroll),
        form: editorCell.locator('form'),
      });
    }

    // Editing opens the shared dialog inside the viewport and leaves the narrow table's columns
    // alone (no editor row spans them any more).
    for (const { row, editName, dialogName, table, columns } of [
      {
        row: 'chores4irl/daily',
        editName: 'Edit chores4irl/daily',
        dialogName: 'Edit chores4irl/daily',
        table: '.heartbeats__table',
        columns: 6,
      },
    ]) {
      const menu = await openRowMenu({ page, row });
      await menu.getByRole('menuitem', { name: editName, exact: true }).click();
      const dialog = page.getByRole('dialog', { name: dialogName, exact: true });
      await expect(dialog).toBeVisible();
      await expect(page.locator(`${table} tbody td[colspan]`)).toHaveCount(0);
      await expect(page.locator(`${table} thead th`)).toHaveCount(columns);
      await expectDialogInViewport(page);
      await page.keyboard.press('Escape');
      await expect(dialog).toBeHidden();
    }

    // App identities (no Owner column): the "approved ... UTC" sub-line under the app id is
    // wholly visible at scroll 0, clear of the sticky kebab column.
    const bindingsWrapper = page
      .locator('.app-identities__scroll')
      .filter({ has: page.locator('tr[data-app-id="tasktracker"]') });
    await bindingsWrapper.scrollIntoViewIfNeeded();
    expect(await bindingsWrapper.evaluate((element) => element.scrollLeft)).toBe(0);
    const approved = bindingsWrapper
      .locator('tbody .app-identities__sub')
      .filter({ hasText: /^approved .+ UTC$/ });
    await expect(approved).toHaveCount(1);
    const approvedBox = await approved.boundingBox();
    const kebabBox = await rowKebab({ page, row: 'tasktracker' }).boundingBox();
    if (approvedBox === null || kebabBox === null) {
      throw new Error('approved line or kebab has no bounding box');
    }
    expect(approvedBox.x).toBeGreaterThanOrEqual(0);
    expect(approvedBox.x + approvedBox.width).toBeLessThanOrEqual(kebabBox.x);
    // Nothing (the sticky kebab column included) is painted over the end of the line.
    expect(
      await approved.evaluate((element) => {
        const rect = element.getBoundingClientRect();
        const hit = document.elementFromPoint(rect.right - 2, rect.top + rect.height / 2);
        return hit !== null && element.contains(hit);
      }),
    ).toBe(true);
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });

  /**
   * Seed for the table-layout tests: enough rows that every wrapper scrolls both ways, with
   * row 4 of each table carrying a first-column value far longer than any column cap.
   */
  function manyRowsSeed(rows: number): DeliveryMockSeed {
    const numbered = <T>(build: (index: number) => T): T[] =>
      Array.from({ length: rows }, (_, index) => build(index + 1));
    const long = ({ length, prefix }: { length: number; prefix: string }): string =>
      length === 4
        ? `${prefix}_${'very_long_identifier_'.repeat(4)}${length}`
        : `${prefix}_${length}`;
    return {
      destinations: numbered((n) =>
        destinationWire({
          name: long({ length: n, prefix: 'dest' }),
          type: 'webhook',
          secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
          ...(n === 2 ? { ownerKind: 'app', ownerAppId: 'chores4irl' } : {}),
        }),
      ),
      routes: numbered((n) =>
        routeWire({
          id: n,
          appPattern: long({ length: n, prefix: 'app' }),
          destinations: ['dest_1', 'dest_2', 'dest_3'],
        }),
      ),
      heartbeats: numbered((n) =>
        heartbeatWire({ id: n, name: long({ length: n, prefix: 'check' }) }),
      ),
      identityRequests: numbered((n) =>
        identityRequestWire({ id: n, appId: long({ length: n, prefix: 'app' }) }),
      ),
      identityBindings: numbered((n) =>
        identityBindingWire({
          appId: long({ length: n, prefix: 'bound' }),
          keys: [
            identityKeyWire({
              id: n,
              appId: long({ length: n, prefix: 'bound' }),
              hint: `dak_${n}`,
            }),
          ],
        }),
      ),
    };
  }

  function tableWrappers(page: Page): Locator[] {
    return [
      page.locator('.destinations__scroll'),
      page.locator('.routes__scroll'),
      page.locator('.heartbeats__scroll'),
      page.locator('.app-identities__scroll').nth(0),
      page.locator('.app-identities__scroll').nth(1),
    ];
  }

  test('admin tables at 390px: titles and the kebab column stay pinned, the first column scrolls and shows every value in full', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, manyRowsSeed(16));

    // Tall enough that the wrapper (75vh) is fully on screen when scrolled to.
    await page.setViewportSize({ width: 390, height: 1000 });
    await page.goto('/#delivery');
    for (const wrapper of tableWrappers(page)) {
      await expect(wrapper).toBeVisible();
      // Bounded height: this is what lets the titles pin to the wrapper rather than the page.
      expect(await wrapper.evaluate((element) => getComputedStyle(element).maxHeight)).not.toBe(
        'none',
      );

      // Keyboard focus never lands under the pinned titles: with the first row's kebab
      // scrolled just out of sight above the wrapper, focusing it scrolls it back by the
      // minimum, which leaves it below the titles only because of the wrapper's scroll-padding.
      const control = wrapper.locator('tbody tr button').first();
      await control.evaluate((element) => {
        const scroller = element.closest('.admin-table__scroll');
        if (!(scroller instanceof HTMLElement)) {
          throw new Error('control is not inside a table wrapper');
        }
        scroller.scrollIntoView({ block: 'start' });
        scroller.scrollTop +=
          element.getBoundingClientRect().top - scroller.getBoundingClientRect().top + 12;
      });
      await control.focus();
      const controlBox = await control.boundingBox();
      const headerBox = await wrapper.locator('thead th').first().boundingBox();
      expect(controlBox?.y ?? -1).toBeGreaterThanOrEqual(
        (headerBox?.y ?? Infinity) + (headerBox?.height ?? 0) - 1,
      );
      // The kebab is a 44px target that nothing covers while focused, and the page did not move.
      await expectTouchTarget(control);
      await expectFullyPainted(control);

      await expectCompactTable({ wrapper });
      await expectNoPageOverflow(page);
    }
    expect(mock.unmocked).toEqual([]);
  });

  test('admin tables at 390px: Tab visits exactly one stop per row, its kebab, which stays in view without Tab ever scrolling the wrapper', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, manyRowsSeed(6));

    await page.setViewportSize({ width: 390, height: 1000 });
    await page.goto('/#delivery');
    for (const wrapper of tableWrappers(page)) {
      await expect(wrapper).toBeVisible();
      await wrapper.scrollIntoViewIfNeeded();
      const kebabs = wrapper.locator('tbody .row-menu__trigger');
      const count = await kebabs.count();
      expect(count).toBe(6);
      await kebabs.first().focus();
      for (let index = 0; index < count; index += 1) {
        await expect(kebabs.nth(index)).toBeFocused();
        // Wholly inside the visible part of the wrapper, whatever the scroll position.
        const box = await kebabs.nth(index).boundingBox();
        const wrapperBox = await wrapper.boundingBox();
        if (box === null || wrapperBox === null) {
          throw new Error('kebab or wrapper has no bounding box');
        }
        expect(box.x).toBeGreaterThanOrEqual(wrapperBox.x);
        expect(box.x + box.width).toBeLessThanOrEqual(wrapperBox.x + wrapperBox.width);
        await expectFullyPainted(kebabs.nth(index));
        if (index < count - 1) {
          // The very next Tab stop is the next row's kebab: no other control in between.
          await page.keyboard.press('Tab');
        }
      }
      // Focusing a pinned kebab must not drag the wrapper sideways (the row text would be
      // scrolled away from the control that is focused).
      expect(await wrapper.evaluate((element) => element.scrollLeft)).toBe(0);
    }
    expect(mock.unmocked).toEqual([]);
  });

  test('admin tables at 390px: every row menu opens fully inside the viewport, even for the last row, and is keyboard operable', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, manyRowsSeed(12));

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    const wrappers = tableWrappers(page);
    const cases = [
      {
        wrapper: wrappers[0],
        first: 'dest_1',
        second: 'dest_2',
        last: 'dest_12',
        itemsOf: (row: string) => [
          `Send test to ${row}`,
          `Edit ${row}`,
          `Disable ${row}`,
          `Delete ${row}`,
        ],
      },
      {
        wrapper: wrappers[1],
        first: 'route 1',
        second: 'route 2',
        last: 'route 12',
        itemsOf: (row: string) => [`Edit ${row}`, `Delete ${row}`],
      },
      {
        wrapper: wrappers[2],
        first: 'urls4irl/check_1',
        second: 'urls4irl/check_2',
        last: 'urls4irl/check_12',
        itemsOf: (row: string) => [`Edit ${row}`, `Pause ${row}`, `Delete ${row}`],
      },
      {
        wrapper: wrappers[3],
        first: 'app_1',
        second: 'app_2',
        last: 'app_12',
        itemsOf: (row: string) => [
          `Approve identity request for ${row}`,
          `Reject identity request for ${row}`,
        ],
      },
      {
        wrapper: wrappers[4],
        first: 'bound_1',
        second: 'bound_2',
        last: 'bound_12',
        itemsOf: (row: string) => [
          `Revoke key dak_${row.replace('bound_', '')} for ${row}`,
          `Unbind ${row}`,
        ],
      },
    ];
    for (const { wrapper, first, second, last, itemsOf } of cases) {
      await expect(wrapper).toBeVisible();
      for (const row of [first, last]) {
        const items = itemsOf(row);
        // The first row sits at the top of its wrapper; the last at the bottom of the page's
        // viewport, where there is no room below for a menu.
        await wrapper.evaluate((element, atEnd) => {
          element.scrollTop = atEnd ? element.scrollHeight : 0;
          element.scrollIntoView({ block: atEnd ? 'end' : 'start' });
        }, row === last);
        const kebab = rowKebab({ page, row });
        await expectTouchTarget(kebab);
        const menu = await openRowMenu({ page, row });
        expect(await kebab.getAttribute('aria-expanded')).toBe('true');

        // The popover is not clipped by the table wrapper: inside the viewport, painted whole.
        await expectInViewport({ page, locator: menu });
        await expectFullyPainted(menu);
        const menuBox = await menu.boundingBox();
        const kebabBox = await kebab.boundingBox();
        if (menuBox === null || kebabBox === null) {
          throw new Error('menu or kebab has no bounding box');
        }
        // Right-aligned to the kebab, which is pinned at the wrapper's end edge.
        expect(Math.abs(menuBox.x + menuBox.width - (kebabBox.x + kebabBox.width))).toBeLessThan(2);
        if (row === last) {
          // No room below: it flips above the kebab.
          expect(menuBox.y + menuBox.height).toBeLessThanOrEqual(kebabBox.y + 1);
        } else {
          expect(menuBox.y).toBeGreaterThanOrEqual(kebabBox.y + kebabBox.height - 1);
        }

        // Every action is in the menu, each a 44px target.
        for (const name of items) {
          const item = menu.getByRole('menuitem', { name, exact: true });
          await expect(item).toBeVisible();
          await expectTouchTarget(item);
        }
        // Arrow keys move through the items; focus starts on the first.
        const all = menu.getByRole('menuitem');
        await expect(all.first()).toBeFocused();
        await page.keyboard.press('ArrowDown');
        await expect(all.nth(1)).toBeFocused();
        await page.keyboard.press('End');
        await expect(all.last()).toBeFocused();
        await page.keyboard.press('Home');
        await expect(all.first()).toBeFocused();
        if (row === last) {
          // The destructive confirm (the last item of every table) closes the menu and opens a
          // full-screen dialog, so a row at the bottom of the viewport has no popover to flip.
          const destructive = items.at(-1) ?? '';
          await menu.getByRole('menuitem', { name: destructive, exact: true }).click();
          const step = page.getByRole('dialog');
          await expectFullScreenConfirm({ page, dialog: step, buttons: [] });
          await expect(menu).toBeHidden();
          // Escape closes the dialog without firing.
          await page.keyboard.press('Escape');
          await expect(step).toBeHidden();
        } else {
          // Escape closes the menu.
          await page.keyboard.press('Escape');
        }
        await expect(menu).toBeHidden();
        await expect(kebab).toBeFocused();
        expect(await kebab.getAttribute('aria-expanded')).toBe('false');
      }

      // Dismissal, on the first row: a press outside, a scroll of the wrapper, a scroll of the
      // page, and Tab (which carries focus on from the kebab, out of the menu).
      await wrapper.evaluate((element) => {
        element.scrollTop = 0;
        element.scrollIntoView({ block: 'start' });
      });
      const kebab = rowKebab({ page, row: first });
      // A press outside: a click at a fixed point (the page margin) scrolls nothing, so only
      // the outside-press handler can have closed the menu.
      let menu = await openRowMenu({ page, row: first });
      const scrollBefore = await page.evaluate(() => window.scrollY);
      await page.mouse.click(2, 2);
      await expect(menu).toBeHidden();
      expect(await page.evaluate(() => window.scrollY)).toBe(scrollBefore);

      menu = await openRowMenu({ page, row: first });
      await wrapper.evaluate((element) => element.scrollBy({ left: 30 }));
      await expect(menu).toBeHidden();
      await wrapper.evaluate((element) => {
        element.scrollLeft = 0;
      });

      menu = await openRowMenu({ page, row: first });
      // Scroll the page (whichever way there is room) so the document really moves.
      await page.evaluate(() => window.scrollTo(0, window.scrollY > 40 ? window.scrollY - 40 : 80));
      await expect(menu).toBeHidden();
      await wrapper.evaluate((element) => {
        element.scrollIntoView({ block: 'start' });
      });

      await kebab.focus();
      await page.keyboard.press('Enter');
      menu = rowMenu({ page, row: first });
      await expect(menu).toBeVisible();
      await page.keyboard.press('Tab');
      await expect(menu).toBeHidden();
      // Tab carried on from the kebab: the next row's kebab has focus, not <body>, the page
      // or the (portalled) menu.
      await expect(rowKebab({ page, row: second })).toBeFocused();
      expect(await focusedDescription(page)).not.toMatch(/^(body|none)/);

      // Only one menu is ever open.
      await wrapper.evaluate((element) => {
        element.scrollIntoView({ block: 'start' });
      });
      await openRowMenu({ page, row: first });
      await rowKebab({ page, row: last }).dispatchEvent('click');
      await expect(page.getByRole('menu')).toHaveCount(1);
      await page.keyboard.press('Escape');
    }
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });

  test('destinations and routes at 390px: Disable and Delete ask inside the menu, Escape and Cancel back out, and focus lands on a kebab', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({ name: 'hook1', type: 'webhook' }),
        destinationWire({ name: 'hook2', type: 'webhook' }),
      ],
      routes: [routeWire({ id: 1 }), routeWire({ id: 2, appPattern: 'chores4irl' })],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    // Roomy-layout buttons are not in the DOM at all (not merely hidden): every button in a body
    // row is a kebab.
    await expect(page.locator('.destinations__table tbody button')).toHaveCount(2);
    await expect(
      page.locator('.destinations__table tbody button:not(.row-menu__trigger)'),
    ).toHaveCount(0);
    await expect(page.locator('.routes__table tbody button')).toHaveCount(2);
    await expect(page.locator('.routes__table tbody button:not(.row-menu__trigger)')).toHaveCount(
      0,
    );

    // Disable: the confirm is a full-screen dialog; Escape and Cancel back out to the kebab.
    let menu = await openRowMenu({ page, row: 'hook1' });
    await menu.getByRole('menuitem', { name: 'Disable hook1', exact: true }).click();
    const confirm = page.getByRole('dialog', { name: 'Disable destination?', exact: true });
    await expectFullScreenConfirm({
      page,
      dialog: confirm,
      buttons: ['Confirm disable hook1', 'Cancel Disable hook1'],
    });
    await expect(menu).toBeHidden();
    // Initial focus is Cancel, so Enter on open never fires a destructive action.
    await expect(confirm.getByRole('button', { name: 'Cancel Disable hook1' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(confirm).toBeHidden();
    await expect(rowKebab({ page, row: 'hook1' })).toBeFocused();
    menu = await openRowMenu({ page, row: 'hook1' });
    await menu.getByRole('menuitem', { name: 'Disable hook1', exact: true }).click();
    await page.getByRole('button', { name: 'Cancel Disable hook1', exact: true }).click();
    await expect(confirm).toBeHidden();
    await expect(rowKebab({ page, row: 'hook1' })).toBeFocused();
    expect(mock.mutations).toEqual([]);
    // ... and the keyboard completes it: select Disable, Tab to Confirm, Enter.
    menu = await openRowMenu({ page, row: 'hook1' });
    await menu.getByRole('menuitem', { name: 'Disable hook1', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Cancel Disable hook1' })).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(page.getByRole('button', { name: 'Confirm disable hook1' })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(confirm).toBeHidden();
    await expect(rowKebab({ page, row: 'hook1' })).toBeFocused();
    expect(mock.mutations.map((request) => `${request.method} ${request.pathname}`)).toEqual([
      'PATCH /delivery/v1/admin/destinations/hook1',
    ]);

    // Enable is one selection.
    menu = await openRowMenu({ page, row: 'hook1' });
    await menu.getByRole('menuitem', { name: 'Enable hook1', exact: true }).click();
    await expect(menu).toBeHidden();
    await expect(rowKebab({ page, row: 'hook1' })).toBeFocused();
    expect(mock.mutations).toHaveLength(2);

    // Send test: the result reads in the row's Status cell, not in the (closed) menu.
    menu = await openRowMenu({ page, row: 'hook1' });
    await menu.getByRole('menuitem', { name: 'Send test to hook1', exact: true }).click();
    await expect(
      page.locator('.destinations__table tbody tr').first().getByText('HTTP 200'),
    ).toBeVisible();

    // Delete, with the keyboard alone: Enter opens the menu, End reaches Delete, Enter asks,
    // Enter on the focused Confirm deletes. Focus moves to the neighbour row's kebab.
    await rowKebab({ page, row: 'hook1' }).focus();
    await page.keyboard.press('Enter');
    menu = rowMenu({ page, row: 'hook1' });
    await expect(menu).toBeVisible();
    await page.keyboard.press('End');
    await expect(menu.getByRole('menuitem', { name: 'Delete hook1', exact: true })).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.getByRole('button', { name: 'Cancel Delete hook1' })).toBeFocused();
    await page.keyboard.press('Tab');
    await expect(page.getByRole('button', { name: 'Confirm delete hook1' })).toBeFocused();
    expect(mock.mutations).toHaveLength(3);
    await page.keyboard.press('Enter');
    await expect(rowKebab({ page, row: 'hook1' })).toHaveCount(0);
    await expect(rowKebab({ page, row: 'hook2' })).toBeFocused();
    expect(mock.mutations.at(-1)?.method).toBe('DELETE');

    // A route: the same, with the same focus hand-off.
    menu = await openRowMenu({ page, row: 'route 1' });
    await menu.getByRole('menuitem', { name: 'Delete route 1', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm delete route 1', exact: true }).click();
    await expect(rowKebab({ page, row: 'route 1' })).toHaveCount(0);
    await expect(rowKebab({ page, row: 'route 2' })).toBeFocused();
    expect(mock.mutations.at(-1)?.pathname).toBe('/delivery/v1/admin/routes/1');
    expect(mock.unmocked).toEqual([]);
  });

  test('admin tables at 800px keep the roomy layout: inline compact buttons, pinned first column, nothing truncated but a pathological value', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, manyRowsSeed(16));

    await page.setViewportSize({ width: 800, height: 1000 });
    await page.goto('/#delivery');
    // No kebab anywhere: the inline buttons are the row actions, at the compact size.
    await expect(page.locator('.row-menu__trigger')).toHaveCount(0);
    await expectCompactButton(
      page.getByRole('button', { name: 'Send test to dest_1', exact: true }),
    );
    await expectCompactButton(page.getByRole('button', { name: 'Edit route 1', exact: true }));
    for (const wrapper of tableWrappers(page)) {
      await expect(wrapper).toBeVisible();
      await expectPinnedTable({ wrapper });
      await expectNoPageOverflow(page);
    }
    expect(mock.unmocked).toEqual([]);
  });

  test.describe('on a touch screen', () => {
    test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true });

    test('the narrow layout keeps 44px targets: kebabs, menu items and the section buttons', async ({
      page,
    }) => {
      const mock = await mockDelivery(page, {
        destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
        heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })],
      });

      await page.goto('/#delivery');
      expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true);
      await expectTouchTarget(rowKebab({ page, row: 'hook1' }));
      await expectTouchTarget(rowKebab({ page, row: 'urls4irl/nightly_backup' }));
      const heartbeats = page.getByRole('region', { name: 'Heartbeats' });
      await expectTouchTarget(heartbeats.getByRole('button', { name: 'Add check', exact: true }));
      await expectTouchTarget(
        heartbeats.getByRole('button', { name: 'Refresh heartbeats', exact: true }),
      );
      const menu = await openRowMenu({ page, row: 'hook1' });
      for (const item of await menu.getByRole('menuitem').all()) {
        await expectTouchTarget(item);
      }
      expect(mock.unmocked).toEqual([]);
    });
  });

  test.describe('on a wide touch screen', () => {
    test.use({ viewport: { width: 800, height: 900 }, hasTouch: true, isMobile: true });

    test('the roomy layout keeps the inline row buttons at 44px', async ({ page }) => {
      const mock = await mockDelivery(page, {
        destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
        routes: [routeWire({ id: 1 })],
        heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })],
        identityRequests: [identityRequestWire({ id: 1, appId: 'urls4irl' })],
        identityBindings: [
          identityBindingWire({
            appId: 'tasktracker',
            keys: [identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' })],
          }),
        ],
      });

      await page.goto('/#delivery');
      expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true);
      await expect(page.locator('.row-menu__trigger')).toHaveCount(0);
      for (const name of [
        'Send test to hook1',
        'Edit hook1',
        'Delete hook1',
        'Edit route 1',
        'Pause urls4irl/nightly_backup',
        'Approve identity request for urls4irl',
        'Unbind tasktracker',
        'Revoke key dak_7Q2m for tasktracker',
      ]) {
        await expectTouchTarget(page.getByRole('button', { name, exact: true }));
      }
      expect(mock.unmocked).toEqual([]);
    });
  });

  test('the layout switches exactly at the 30rem breakpoint: kebabs at 480px, inline buttons at 481px', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
      routes: [routeWire({ id: 1 })],
      heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })],
      identityRequests: [identityRequestWire({ id: 1, appId: 'urls4irl' })],
    });

    await page.setViewportSize({ width: 480, height: 900 });
    await page.goto('/#delivery');
    await expect(page.locator('.row-menu__trigger')).toHaveCount(4);
    await expect(
      page.locator(
        ':is(.destinations__table, .routes__table, .heartbeats__table, .app-identities__table) tbody button:not(.row-menu__trigger)',
      ),
    ).toHaveCount(0);
    // The first column is not pinned at the narrow width ...
    expect(
      await page
        .locator('.destinations__table tbody .admin-table__lead')
        .first()
        .evaluate((element) => getComputedStyle(element).position),
    ).toBe('static');

    // ... and the very next pixel is the roomy layout, first column pinned, with no kebab.
    await page.setViewportSize({ width: 481, height: 900 });
    await expect(page.locator('.row-menu__trigger')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Delete hook1', exact: true })).toBeVisible();
    await expect(
      page.getByRole('button', { name: 'Approve identity request for urls4irl', exact: true }),
    ).toBeVisible();
    expect(
      await page
        .locator('.destinations__table tbody .admin-table__lead')
        .first()
        .evaluate((element) => getComputedStyle(element).position),
    ).toBe('sticky');
    expect(mock.unmocked).toEqual([]);
  });

  test('destinations and routes: the inline editors span the table and stay in view at 390px', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({
          name: 'hook1',
          type: 'webhook',
          secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
        }),
      ],
      routes: [routeWire({ id: 1 })],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');

    // The destination editor.
    const destinationWrapper = page.locator('.destinations__scroll');
    const destinationMenu = await openRowMenu({ page, row: 'hook1' });
    await destinationMenu.getByRole('menuitem', { name: 'Edit hook1', exact: true }).click();
    await expect(destinationMenu).toBeHidden();
    const destinationForm = destinationWrapper.locator('.admin-table__editor form');
    await expect(destinationForm).toBeVisible();
    // Opened from the menu, the editor takes focus and is in view.
    await expect(destinationForm.locator(':focus')).toHaveCount(1);
    await expectEditorInView({ wrapper: destinationWrapper, form: destinationForm });
    // It wraps like a form (no cell-wide single line) and never scrolls the page.
    await expectNoPageOverflow(page);
    const destinationFormBox = await destinationForm.boundingBox();
    expect(destinationFormBox?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((destinationFormBox?.x ?? 0) + (destinationFormBox?.width ?? 0)).toBeLessThanOrEqual(
      390,
    );
    for (const control of [
      destinationForm.getByRole('button', { name: 'Save hook1', exact: true }),
      destinationForm.getByRole('button', { name: 'Close', exact: true }),
    ]) {
      const box = await control.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
      expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(390);
    }
    await destinationForm.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(destinationForm).toBeHidden();
    // Closing hands focus back to the row's kebab.
    await expect(rowKebab({ page, row: 'hook1' })).toBeFocused();

    // The route editor.
    const routeWrapper = page.locator('.routes__scroll');
    const routeMenu = await openRowMenu({ page, row: 'route 1' });
    await routeMenu.getByRole('menuitem', { name: 'Edit route 1', exact: true }).click();
    const routeForm = routeWrapper.locator('.admin-table__editor form');
    await expect(routeForm).toBeVisible();
    await expect(routeForm.locator(':focus')).toHaveCount(1);
    await expectEditorInView({ wrapper: routeWrapper, form: routeForm });
    await expectNoPageOverflow(page);
    const routeFormBox = await routeForm.boundingBox();
    expect(routeFormBox?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((routeFormBox?.x ?? 0) + (routeFormBox?.width ?? 0)).toBeLessThanOrEqual(390);
    expect(mock.unmocked).toEqual([]);
  });

  test('pending sends: cells stay on one line and the wrapper scrolls at 390px', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      scheduled: [
        scheduledWire({ id: 'msg-1', key: 'digest-weekly-2099-01-01' }),
        scheduledWire({ id: 'msg-2', key: 'digest-weekly-2099-02-01' }),
      ],
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    const pending = page.getByRole('region', { name: 'Pending sends' });
    const wrapper = pending.locator('.pending-sends__scroll');
    await expect(wrapper).toBeVisible();

    expect(await maxRenderedLines({ root: pending.locator('table') })).toBe(1);
    const wrapperBox = await wrapper.evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
    }));
    expect(wrapperBox.scrollWidth).toBeGreaterThan(wrapperBox.clientWidth);
    const documentBox = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(documentBox.scrollWidth).toBeLessThanOrEqual(documentBox.clientWidth);

    // Cancel send keeps its one-line label and stays inside the viewport (sticky cell).
    const trigger = pending.getByRole('button', { name: 'Cancel send digest-weekly-2099-01-01' });
    const triggerBox = await trigger.boundingBox();
    expect(triggerBox?.height ?? Infinity).toBeLessThan(60);
    expect((triggerBox?.x ?? -1) + (triggerBox?.width ?? 0)).toBeLessThanOrEqual(390);

    // Scrolling the wrapper to its end moves the content; the page itself stays put.
    await wrapper.evaluate((element) => {
      element.scrollLeft = element.scrollWidth;
    });
    expect(await wrapper.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
    const after = await page.evaluate(() => document.documentElement.scrollLeft);
    expect(after).toBe(0);
    expect(mock.unmocked).toEqual([]);
  });

  test('shows the schema-mismatch status card on a 503', async ({ page }) => {
    const mock = await mockDelivery(page, {
      readiness: {
        status: 503,
        body: {
          status: 'schema_mismatch',
          schema_version: 4,
          expected_schema_version: 5,
          last_migration_failure: null,
        },
      },
    });

    await page.goto('/#delivery');
    const statusCard = page.getByRole('region', { name: 'delivery-api' });
    await expect(statusCard.getByText('Schema mismatch', { exact: true })).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('the inactive tabpanel is hidden in both tab states', async ({ page }) => {
    await mockDelivery(page);
    const provisioningPanel = page.locator('[role="tabpanel"]#app-panel-provisioning');
    const deliveryPanel = page.locator('[role="tabpanel"]#app-panel-delivery');

    await page.goto('/');
    await expect(provisioningPanel).toBeVisible();
    await expect(deliveryPanel).toBeHidden();

    await page.getByRole('tab', { name: 'Delivery' }).click();
    await expect(deliveryPanel).toBeVisible();
    await expect(provisioningPanel).toBeHidden();
  });

  test('has no document-level horizontal overflow at 390px', async ({ page }) => {
    const longSnippet = 'upstream-error-'.repeat(40);
    // An app-owned row at 390px: a long app id stays on one line inside the Owner badge
    // (the table wrapper scrolls instead of the page), and the Delete and Disable confirms
    // (which carry the managed note) stay on one line too.
    const LONG_OWNER_APP_ID = 'a-very-long-app-identifier-for-an-owner-badge';
    const managedNote = `Managed by ${LONG_OWNER_APP_ID}'s deploy — operator edits are reverted on its next deploy.`;
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({
          name: 'hook1',
          type: 'webhook',
          secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
        }),
        destinationWire({
          name: 'ops_discord',
          type: 'discord',
          secrets: { webhook_url: 'https://discord.com/****' },
          ownerKind: 'app',
          ownerAppId: LONG_OWNER_APP_ID,
        }),
      ],
      routes: [
        routeWire({
          id: 1,
          destinations: ['hook1', 'ops_discord'],
          ownerKind: 'app',
          ownerAppId: LONG_OWNER_APP_ID,
        }),
      ],
      heartbeats: [
        heartbeatWire({
          id: 1,
          name: 'nightly_backup',
          ownerKind: 'app',
          ownerAppId: LONG_OWNER_APP_ID,
        }),
      ],
      deliveries: { dead: [deliveryWire({ id: 7 })] },
      scheduled: [scheduledWire({ id: 'msg-1', key: `digest-${'x'.repeat(40)}` })],
      cancelError: { status: 503, message: 'delivery-service-unavailable-'.repeat(12) },
      attempts: {
        7: [
          {
            attempt_number: 1,
            outcome: 'http_error',
            status_code: 500,
            latency_ms: 812,
            error: null,
            response_snippet: longSnippet,
            started_at: TIMESTAMP,
          },
        ],
      },
    });

    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/#delivery');
    // The cell also carries its Owner badge on its second line.
    await expect(page.getByRole('cell', { name: /^ops_discord Owner: App · / })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'alerts*', exact: true })).toBeVisible();
    const log = page.getByRole('region', { name: 'Delivery log' });
    await log.getByRole('button', { name: 'Show attempts for delivery 7', exact: true }).click();
    await expect(log.getByText(longSnippet.trim())).toBeVisible();

    const overflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth);

    // The table wrapper itself scrolls instead of the page.
    const wrapper = await page.locator('.delivery-log__scroll').evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
    }));
    expect(wrapper.scrollWidth).toBeGreaterThan(wrapper.clientWidth);

    // Pending sends scrolls inside its own wrapper too, and its sticky Actions
    // cell keeps Cancel send reachable without scrolling that wrapper.
    const pending = page.getByRole('region', { name: 'Pending sends' });
    const pendingWrapper = pending.locator('.pending-sends__scroll');
    await expect(pendingWrapper).toBeVisible();
    const pendingBox = await pendingWrapper.evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
      scrollLeft: element.scrollLeft,
    }));
    expect(pendingBox.scrollWidth).toBeGreaterThan(pendingBox.clientWidth);
    expect(pendingBox.scrollLeft).toBe(0);
    const trigger = pending.getByRole('button', { name: /^Cancel send digest-/ });
    await expect(trigger).toBeVisible();
    const triggerBox = await trigger.boundingBox();
    expect(triggerBox).not.toBeNull();
    expect(triggerBox?.x).toBeGreaterThanOrEqual(0);
    expect((triggerBox?.x ?? 0) + (triggerBox?.width ?? 0)).toBeLessThanOrEqual(390);
    await trigger.click();
    // The confirm is a body-level dialog, so the wrapper is not scrolled by opening it.
    const confirmCancel = page.getByRole('button', { name: /^Confirm cancel send digest-/ });
    await expect(confirmCancel).toBeVisible();
    expect(await pendingWrapper.evaluate((element) => element.scrollLeft)).toBe(0);

    // A long server error stays inside the capped Actions cell: the cell keeps
    // its 14rem width and Cancel send stays reachable without scrolling.
    await confirmCancel.click();
    const rowError = pending.getByRole('alert');
    await expect(rowError).toContainText('Could not cancel: delivery-service-unavailable-');
    const actionsCell = pending.locator('td.pending-sends__actions-cell').first();
    const cellBox = await actionsCell.boundingBox();
    expect(cellBox?.width ?? Infinity).toBeLessThanOrEqual(272);
    const errorBox = await rowError.boundingBox();
    expect((errorBox?.x ?? 0) + (errorBox?.width ?? 0)).toBeLessThanOrEqual(390);
    expect(await pendingWrapper.evaluate((element) => element.scrollLeft)).toBe(0);
    const retryBox = await trigger.boundingBox();
    expect((retryBox?.x ?? 0) + (retryBox?.width ?? 0)).toBeLessThanOrEqual(390);
    const after = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(after.scrollWidth).toBeLessThanOrEqual(after.clientWidth);

    // The app-owned destination's Delete confirm carries the managed note: its
    // controls stay at least 44px tall and the page still never scrolls sideways.
    const ownedRow = page
      .locator('.destinations__table tbody tr')
      .filter({ hasText: 'ops_discord' })
      .first();
    await expect(ownedRow.locator('.owner-badge--app')).toContainText(LONG_OWNER_APP_ID);
    // The long id never wraps inside its badge, so the badge stays a single pill.
    expect(await maxRenderedLines({ root: ownedRow.locator('.owner-badge--app') })).toBe(1);
    expect(
      await ownedRow
        .locator('.owner-badge--app')
        .evaluate((element) => getComputedStyle(element).whiteSpace),
    ).toBe('nowrap');
    expect(
      (await ownedRow.locator('.owner-badge--app').boundingBox())?.height ?? Infinity,
    ).toBeLessThan(30);
    // The row's only control is its kebab; the short owner note (the menu's first line) and
    // the Delete confirm that carries the long managed note live in the menu, so they cannot
    // widen the table or the page.
    const widthBefore = await page
      .locator('.destinations__scroll')
      .evaluate((element) => element.scrollWidth);
    const ownedMenu = await openRowMenu({ page, row: 'ops_discord' });
    await expect(ownedMenu.getByText('Edits are overridden on next deploy')).toBeVisible();
    await expect(ownedMenu.getByText(managedNote)).toHaveCount(0);
    await ownedMenu.getByRole('menuitem', { name: 'Delete ops_discord', exact: true }).click();
    const ownedConfirm = page.getByRole('dialog', { name: 'Delete destination?', exact: true });
    await expect(ownedConfirm).toContainText(managedNote);
    // The dialog is full screen with 44px controls.
    await expectFullScreenConfirm({
      page,
      dialog: ownedConfirm,
      buttons: ['Confirm delete ops_discord', 'Cancel Delete ops_discord'],
    });
    expect(await maxRenderedLines({ root: ownedRow })).toBe(1);
    expect(
      await page.locator('.destinations__scroll').evaluate((element) => element.scrollWidth),
    ).toBe(widthBefore);
    await page.keyboard.press('Escape');
    await expect(ownedConfirm).toBeHidden();
    // Same for the app-owned route's Delete confirm.
    const ownedRoute = page.locator('.routes__table tbody tr').first();
    const routeMenu = await openRowMenu({ page, row: 'route 1' });
    await expect(routeMenu.getByText('Edits are overridden on next deploy')).toBeVisible();
    await expect(routeMenu.getByText(managedNote)).toHaveCount(0);
    await routeMenu.getByRole('menuitem', { name: 'Delete route 1', exact: true }).click();
    const routeConfirm = page.getByRole('dialog', { name: 'Delete route?', exact: true });
    await expect(routeConfirm).toContainText(managedNote);
    await expectFullScreenConfirm({
      page,
      dialog: routeConfirm,
      buttons: ['Confirm delete route 1', 'Cancel Delete route 1'],
    });
    expect(await maxRenderedLines({ root: ownedRoute })).toBe(1);
    const afterConfirm = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(afterConfirm.scrollWidth).toBeLessThanOrEqual(afterConfirm.clientWidth);
    expect(mock.unmocked).toEqual([]);
  });

  test('destinations, routes and heartbeats at 1280px: the inline buttons read as bare verbs and the Actions cell fits with no sideways scroll, whatever the note, test result or error says', async ({
    page,
  }) => {
    const longReason =
      'receiver returned a non-retryable error: connection refused by the remote host after three attempts, giving up';
    const longError = `delete refused by the server: ${'upstream is unavailable, try again later. '.repeat(3)}`;
    const mock = await mockDelivery(page, {
      destinations: [
        destinationWire({
          name: 'hook1',
          type: 'webhook',
          secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
        }),
        destinationWire({
          name: 'chores4irl_hook',
          type: 'webhook',
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        destinationWire({ name: 'mystery_hook', type: 'webhook', ownerKind: null }),
      ],
      routes: [
        routeWire({ id: 1, destinations: ['hook1'] }),
        routeWire({
          id: 2,
          appPattern: 'chores4irl',
          destinations: ['chores4irl_hook'],
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        routeWire({ id: 3, destinations: ['hook1'], ownerKind: null }),
      ],
      heartbeats: [
        heartbeatWire({ id: 1, name: 'nightly_backup' }),
        heartbeatWire({
          id: 2,
          appId: 'chores4irl',
          name: 'daily',
          ownerKind: 'app',
          ownerAppId: 'chores4irl',
        }),
        heartbeatWire({ id: 3, name: 'mystery', ownerKind: null }),
      ],
    });
    // A dead test delivery with a long reason, and a server error for each table's first delete.
    await page.route('**/delivery/v1/admin/messages**', async (route) => {
      if (route.request().method() !== 'POST') {
        await route.fallback();
        return;
      }
      await route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          message: {
            message_id: 'b3a1c6f0-1111-4222-8333-444455556666',
            status: 'dead',
            deliveries: [
              {
                id: 1,
                destination: 'hook1',
                state: 'dead',
                dead_reason: longReason,
                attempt_count: 3,
                last_status_code: null,
                next_attempt_at: null,
                delivered_at: null,
              },
            ],
          },
        }),
      });
    });
    await page.route(
      /\/delivery\/v1\/admin\/(destinations\/hook1|routes\/1|heartbeats\/1)$/,
      async (route) => {
        if (route.request().method() !== 'DELETE') {
          await route.fallback();
          return;
        }
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: longError }),
        });
      },
    );

    await page.setViewportSize({ width: 1280, height: 900 });
    await page.goto('/#delivery');

    const tables = [
      {
        name: 'Destinations',
        wrapper: page.locator('.destinations__scroll'),
        lines: '.destinations__sub, .destinations__result, .destinations__row-error',
        firstRowVisible: ['Test', 'Edit', 'Disable', 'Delete'],
        firstRowNames: ['Send test to hook1', 'Edit hook1', 'Disable hook1', 'Delete hook1'],
      },
      {
        name: 'Routes',
        wrapper: page.locator('.routes__scroll'),
        lines: '.routes__sub, .routes__row-error',
        firstRowVisible: ['Edit', 'Delete'],
        firstRowNames: ['Edit route 1', 'Delete route 1'],
      },
      {
        name: 'Heartbeats',
        wrapper: page.locator('.heartbeats__scroll'),
        lines: '.heartbeats__sub, .heartbeats__row-error',
        firstRowVisible: ['Edit', 'Pause', 'Delete'],
        firstRowNames: [
          'Edit urls4irl/nightly_backup',
          'Pause urls4irl/nightly_backup',
          'Delete urls4irl/nightly_backup',
        ],
      },
    ];
    for (const table of tables) {
      await expect(table.wrapper).toBeVisible();
      await table.wrapper.scrollIntoViewIfNeeded();
      // The row already names its object: each button shows the bare verb, and is still called
      // by the verb plus the object.
      const first = table.wrapper.locator('tbody tr').first().locator('button');
      await expect(first).toHaveText(table.firstRowVisible);
      for (const [index, name] of table.firstRowNames.entries()) {
        await expect(first.nth(index)).toHaveAttribute('aria-label', name);
      }
      // The owner note (an app-owned row's, and an unknown owner's) is a short single line,
      // fully visible: no ellipsis, no clipping, no `title` fallback.
      const notes = table.wrapper.locator('.admin-table__note');
      await expect(notes).toHaveCount(2);
      await expect(notes.nth(0)).toHaveText('Edits are overridden on next deploy');
      await expect(notes.nth(1)).toHaveText('Owner unknown — edits may be overridden');
      for (const note of await notes.all()) {
        await expect(note).not.toHaveAttribute('title', /.+/);
        const shown = await note.evaluate((element) => ({
          textOverflow: getComputedStyle(element).textOverflow,
          whiteSpace: getComputedStyle(element).whiteSpace,
          scrollWidth: element.scrollWidth,
          clientWidth: element.clientWidth,
        }));
        expect(shown.textOverflow).not.toBe('ellipsis');
        expect(shown.whiteSpace).toBe('nowrap');
        expect(shown.scrollWidth).toBeLessThanOrEqual(shown.clientWidth);
      }
      await expect
        .poll(() => actionsFitViolations({ wrapper: table.wrapper, extraLines: table.lines }), {
          message: `${table.name} idle`,
        })
        .toEqual([]);
    }

    // A long test result under Test, and a long error under Delete, do not widen the cell.
    await page.getByRole('button', { name: 'Send test to hook1', exact: true }).click();
    await expect(page.getByText(longReason)).toBeVisible();
    await expect
      .poll(
        () => actionsFitViolations({ wrapper: tables[0].wrapper, extraLines: tables[0].lines }),
        { message: 'Destinations with a long test result' },
      )
      .toEqual([]);
    for (const [index, table] of tables.entries()) {
      const target = ['hook1', 'route 1', 'urls4irl/nightly_backup'][index];
      await table.wrapper.getByRole('button', { name: `Delete ${target}`, exact: true }).click();
      // Every table confirms in a body-level dialog.
      await page
        .getByRole('dialog')
        .getByRole('button', { name: `Confirm delete ${target}`, exact: true })
        .click();
      await expect(table.wrapper.getByText(longError.trim())).toBeVisible();
      await expect
        .poll(() => actionsFitViolations({ wrapper: table.wrapper, extraLines: table.lines }), {
          message: `${table.name} with a long row error`,
        })
        .toEqual([]);
    }
    expect(mock.unmocked).toEqual([]);
  });

  // Every row confirm opens a body-level dialog, so the open confirm never changes a table's
  // width and nothing in the row steps aside.
  const CONFIRM_SEED: DeliveryMockSeed = {
    destinations: [
      destinationWire({
        name: 'hook1',
        type: 'webhook',
        secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
      }),
      destinationWire({
        name: 'chores4irl_hook',
        type: 'webhook',
        ownerKind: 'app',
        ownerAppId: 'chores4irl',
      }),
      destinationWire({ name: 'mystery_hook', type: 'webhook', ownerKind: null }),
      destinationWire({ name: 'off_hook', type: 'webhook', enabled: false }),
    ],
    routes: [
      routeWire({ id: 1, destinations: ['hook1'] }),
      routeWire({
        id: 2,
        appPattern: 'chores4irl',
        destinations: ['chores4irl_hook'],
        ownerKind: 'app',
        ownerAppId: 'chores4irl',
      }),
      routeWire({ id: 3, destinations: ['hook1'], ownerKind: null }),
    ],
    heartbeats: [
      heartbeatWire({ id: 1, name: 'nightly_backup' }),
      heartbeatWire({
        id: 2,
        appId: 'chores4irl',
        name: 'daily',
        ownerKind: 'app',
        ownerAppId: 'chores4irl',
      }),
      heartbeatWire({ id: 3, name: 'mystery', ownerKind: null }),
      heartbeatWire({ id: 4, name: 'stopped', paused: true }),
    ],
    identityRequests: [
      identityRequestWire({ id: 1, appId: 'urls4irl' }),
      identityRequestWire({ id: 2, appId: 'chores4irl' }),
    ],
    identityBindings: [
      identityBindingWire({
        appId: 'tasktracker',
        keys: [identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' })],
      }),
      identityBindingWire({ appId: 'notifier' }),
    ],
    scheduled: [scheduledWire({ id: 'msg-1', key: 'digest-1' })],
  };

  // Every row confirm is a modal dialog (body-level portal), not inline.
  interface DialogCase {
    label: string;
    trigger: string;
    title: string;
    itemLabel: string;
    itemValue: string;
    confirm: string;
    cancel: string;
    mutation: RegExp;
  }

  const DIALOG_CASES: DialogCase[] = [
    {
      label: 'destinations Delete (app-owned)',
      trigger: 'Delete chores4irl_hook',
      title: 'Delete destination?',
      itemLabel: 'Destination',
      itemValue: 'chores4irl_hook',
      confirm: 'Confirm delete chores4irl_hook',
      cancel: 'Cancel Delete chores4irl_hook',
      mutation: /^DELETE \/delivery\/v1\/admin\/destinations\/chores4irl_hook$/,
    },
    {
      label: 'destinations Disable (unknown owner)',
      trigger: 'Disable mystery_hook',
      title: 'Disable destination?',
      itemLabel: 'Destination',
      itemValue: 'mystery_hook',
      confirm: 'Confirm disable mystery_hook',
      cancel: 'Cancel Disable mystery_hook',
      mutation: /^PATCH \/delivery\/v1\/admin\/destinations\/mystery_hook$/,
    },
    {
      label: 'destinations Delete (disabled destination)',
      trigger: 'Delete off_hook',
      title: 'Delete destination?',
      itemLabel: 'Destination',
      itemValue: 'off_hook',
      confirm: 'Confirm delete off_hook',
      cancel: 'Cancel Delete off_hook',
      mutation: /^DELETE \/delivery\/v1\/admin\/destinations\/off_hook$/,
    },
    {
      label: 'routes Delete (app-owned)',
      trigger: 'Delete route 2',
      title: 'Delete route?',
      itemLabel: 'Route',
      itemValue: 'route 2',
      confirm: 'Confirm delete route 2',
      cancel: 'Cancel Delete route 2',
      mutation: /^DELETE \/delivery\/v1\/admin\/routes\/2$/,
    },
    {
      label: 'heartbeats Pause (app-owned)',
      trigger: 'Pause chores4irl/daily',
      title: 'Pause check?',
      itemLabel: 'Check',
      itemValue: 'chores4irl/daily',
      confirm: 'Confirm pause chores4irl/daily',
      cancel: 'Keep running chores4irl/daily',
      mutation: /^POST \/delivery\/v1\/admin\/heartbeats\/2\/pause$/,
    },
    {
      label: 'heartbeats Delete (app-owned)',
      trigger: 'Delete chores4irl/daily',
      title: 'Delete check?',
      itemLabel: 'Check',
      itemValue: 'chores4irl/daily',
      confirm: 'Confirm delete chores4irl/daily',
      cancel: 'Cancel Delete chores4irl/daily',
      mutation: /^DELETE \/delivery\/v1\/admin\/heartbeats\/2$/,
    },
    {
      label: 'heartbeats Delete (paused check)',
      trigger: 'Delete urls4irl/stopped',
      title: 'Delete check?',
      itemLabel: 'Check',
      itemValue: 'urls4irl/stopped',
      confirm: 'Confirm delete urls4irl/stopped',
      cancel: 'Cancel Delete urls4irl/stopped',
      mutation: /^DELETE \/delivery\/v1\/admin\/heartbeats\/4$/,
    },
    {
      label: 'app identity requests Reject',
      trigger: 'Reject identity request for chores4irl',
      title: 'Reject identity request?',
      itemLabel: 'App',
      itemValue: 'chores4irl',
      confirm: 'Confirm reject identity request for chores4irl',
      cancel: 'Cancel reject identity request for chores4irl',
      mutation: /^POST \/delivery\/v1\/admin\/app-identity\/requests\/2\/reject$/,
    },
    {
      label: 'app identity bindings Unbind',
      trigger: 'Unbind notifier',
      title: 'Unbind app?',
      itemLabel: 'App',
      itemValue: 'notifier',
      confirm: 'Confirm unbind notifier',
      cancel: 'Cancel unbind notifier',
      mutation: /^DELETE \/delivery\/v1\/admin\/app-identity\/bindings\/notifier$/,
    },
    {
      label: 'app identity keys Revoke',
      trigger: 'Revoke key dak_7Q2m for tasktracker',
      title: 'Revoke key?',
      itemLabel: 'Key',
      itemValue: 'dak_7Q2m for tasktracker',
      confirm: 'Confirm revoke key dak_7Q2m for tasktracker',
      cancel: 'Cancel revoke key dak_7Q2m for tasktracker',
      mutation: /^POST \/delivery\/v1\/admin\/app-identity\/keys\/11\/revoke$/,
    },
    {
      label: 'pending sends Cancel send',
      trigger: 'Cancel send digest-1',
      title: 'Cancel scheduled send?',
      itemLabel: 'Send key',
      itemValue: 'digest-1',
      confirm: 'Confirm cancel send digest-1',
      cancel: 'Keep send digest-1',
      mutation: /^DELETE \/delivery\/v1\/admin\/scheduled-messages\/msg-1$/,
    },
  ];

  for (const dialogCase of DIALOG_CASES) {
    test(`${dialogCase.label} at 1280px: the dialog names the action and item, all four exits close it with no mutation, Confirm fires once`, async ({
      page,
    }) => {
      const mock = await mockDelivery(page, CONFIRM_SEED);
      await page.setViewportSize({ width: 1280, height: 900 });
      await page.goto('/#delivery');

      const trigger = page.getByRole('button', { name: dialogCase.trigger, exact: true });
      await expect(trigger).toBeVisible();
      await trigger.scrollIntoViewIfNeeded();
      const dialog = page.getByRole('dialog');
      const row = page.locator('tbody tr').filter({ has: trigger });
      const rowButtonCount = await row.getByRole('button').count();

      const open = async () => {
        await trigger.click();
        await expect(dialog).toBeVisible();
        await expect(dialog.getByRole('heading', { name: dialogCase.title })).toBeVisible();
        await expect(dialog.locator('.modal__item')).toContainText(dialogCase.itemLabel);
        await expect(dialog.locator('.modal__item')).toContainText(dialogCase.itemValue);
        await expect(
          dialog.getByRole('button', { name: dialogCase.cancel, exact: true }),
        ).toBeFocused();
        // Nothing in the row steps aside while the dialog is open.
        await expect(row.getByRole('button', { includeHidden: true })).toHaveCount(rowButtonCount);
        for (const button of await row.getByRole('button', { includeHidden: true }).all()) {
          await expect(button).toBeVisible();
        }
      };
      const expectClosed = async () => {
        await expect(dialog).toHaveCount(0);
        await expect(trigger).toBeFocused();
        expect(mock.mutations).toEqual([]);
      };

      await open();
      await dialog.getByRole('button', { name: dialogCase.cancel, exact: true }).click();
      await expectClosed();

      await open();
      await dialog.getByRole('button', { name: 'Close', exact: true }).click();
      await expectClosed();

      await open();
      await page.keyboard.press('Escape');
      await expectClosed();

      await open();
      await page.locator('.modal__backdrop').click({ position: { x: 5, y: 5 } });
      await expectClosed();

      // Confirm runs the action exactly once.
      await open();
      await dialog.getByRole('button', { name: dialogCase.confirm, exact: true }).click();
      await expect(dialog).toHaveCount(0);
      await expect
        .poll(() =>
          mock.mutations.filter((request) =>
            dialogCase.mutation.test(`${request.method} ${request.pathname}`),
          ),
        )
        .toHaveLength(1);
      // A late duplicate would land within a frame or two of the first request.
      await page.evaluate(
        () =>
          new Promise<void>((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
          ),
      );
      expect(mock.mutations).toHaveLength(1);
      expect(mock.unmocked).toEqual([]);
    });
  }

  for (const { label, width, height, maxLeadShare, scrolls } of [
    // Just above the narrow breakpoint (30rem): the roomy layout with its inline buttons, in the
    // least room it ever gets, so the pinned first column really competes with the actions.
    // (`maxLeadShare` restates the column's cap, 60% of the wrapper or 32rem, plus rounding;
    // the point of this test is the actions' visibility beside it, not the cap.)
    // (The inline buttons read as bare verbs, so their cluster is narrow: only a viewport this
    // close to the breakpoint still leaves a row wider than what is left of the wrapper.)
    { label: '490px', width: 490, height: 1000, maxLeadShare: 0.62, scrolls: true },
    { label: 'desktop', width: 1280, height: 900, maxLeadShare: 0.45, scrolls: false },
  ]) {
    test(`admin tables: row actions stay fully visible clear of the pinned column at both scroll ends and under Tab at ${label}`, async ({
      page,
    }) => {
      const longName = `very_long_identifier_${'very_long_identifier_'.repeat(3)}`;
      const mock = await mockDelivery(page, {
        destinations: [
          destinationWire({
            name: 'hook1',
            type: 'webhook',
            secrets: { url: 'https://hooks.example.com/****', signing_secret: '****' },
          }),
          destinationWire({
            name: 'ops_discord',
            type: 'discord',
            ownerKind: 'app',
            ownerAppId: 'chores4irl',
          }),
          // (A destination's own buttons carry its name, so its name stays short.)
          destinationWire({ name: 'family_ntfy_relay', type: 'ntfy' }),
        ],
        routes: [
          routeWire({ id: 1, destinations: ['hook1', 'ops_discord'] }),
          routeWire({
            id: 2,
            appPattern: 'chores4irl',
            destinations: ['ops_discord'],
            ownerKind: 'app',
            ownerAppId: 'chores4irl',
          }),
          routeWire({ id: 3, appPattern: longName, destinations: ['hook1'] }),
        ],
        heartbeats: [
          heartbeatWire({ id: 1, name: 'nightly_backup' }),
          heartbeatWire({ id: 2, name: 'reaper', paused: true }),
          heartbeatWire({
            id: 3,
            appId: 'chores4irl',
            name: 'owned',
            ownerKind: 'app',
            ownerAppId: 'chores4irl',
          }),
          heartbeatWire({ id: 4, name: 'mystery', ownerKind: null }),
          heartbeatWire({ id: 5, name: longName }),
        ],
        identityRequests: [
          identityRequestWire({ id: 1, appId: 'urls4irl' }),
          identityRequestWire({ id: 2, appId: 'oldapp', status: 'rejected' }),
          identityRequestWire({ id: 3, appId: 'mystery', status: 'weird' }),
          identityRequestWire({ id: 4, appId: longName }),
        ],
        identityBindings: [
          identityBindingWire({
            appId: 'tasktracker',
            keys: [
              identityKeyWire({ id: 11, appId: 'tasktracker', hint: 'dak_7Q2m' }),
              identityKeyWire({ id: 13, appId: 'tasktracker', hint: 'dak_a1Zx', state: 'revoked' }),
            ],
          }),
          identityBindingWire({
            appId: longName,
            keys: [identityKeyWire({ id: 21, appId: longName, hint: 'dak_9Zz9' })],
          }),
        ],
      });

      await page.setViewportSize({ width, height });
      await page.goto('/#delivery');
      const wrappers = [
        page.locator('.destinations__scroll'),
        page.locator('.routes__scroll'),
        page.locator('.heartbeats__scroll'),
        page.locator('.app-identities__scroll').nth(0),
        page.locator('.app-identities__scroll').nth(1),
      ];
      let wholeRows = 0;
      let clippedRows = 0;
      for (const wrapper of wrappers) {
        await expect(wrapper).toBeVisible();
        await wrapper.scrollIntoViewIfNeeded();
        // The trailing cell of every row and of the title row is the shared Actions cell
        // (what trailing-aligns its controls).
        expect(
          await wrapper.evaluate(
            (scroller, cellSelector) =>
              Array.from(scroller.querySelectorAll(`${cellSelector}, thead th:last-child`)).filter(
                (cell) => !cell.classList.contains('admin-table__actions'),
              ).length,
            ACTIONS_CELL,
          ),
        ).toBe(0);
        // Scrolled all the way along, every Actions button and pill lies wholly right of the
        // pinned column (the row's start may be scrolled away, never its controls) ...
        const name = await wrapper.getAttribute('class');
        const sizes = await wrapper.evaluate((element) => ({
          scrollWidth: element.scrollWidth,
          clientWidth: element.clientWidth,
        }));
        if (scrolls) {
          // The wrapper really scrolls sideways, so "the end" differs from "the start".
          expect(sizes.scrollWidth, `${name} scrolls`).toBeGreaterThan(sizes.clientWidth);
        }
        const end = await pinnedLayoutViolations({ wrapper, position: 'end', maxLeadShare });
        expect.soft(end.violations, `${name} at the end`).toEqual([]);
        wholeRows += end.whole;
        clippedRows += end.clipped;
        // ... and scrolled back, the pinned column is wholly visible and not over-wide.
        const start = await pinnedLayoutViolations({ wrapper, position: 'start', maxLeadShare });
        expect.soft(start.violations, `${name} at the start`).toEqual([]);
        // Keyboard focus on any button (Revoke in the Keys column too) scrolls it fully clear
        // of the pinned column.
        const focus = await focusedActionViolations({ page, wrapper });
        expect.soft(focus.violations, `${name} under focus`).toEqual([]);
        expect(focus.checks, `${name} focus checks ran`).toBeGreaterThan(0);
        await expectNoPageOverflow(page);
      }
      // Both judgements really ran: rows whose cluster fits beside the pinned column (every
      // control wholly visible) and, on a phone, rows whose cluster is wider than what is
      // left (judged by the trailing control).
      expect(wholeRows).toBeGreaterThan(0);
      if (scrolls) {
        expect(clippedRows).toBeGreaterThan(0);
      }

      // The Revoke buttons sit in the Keys column (not the last): reachable by scrolling.
      const identities = page.getByRole('region', { name: 'App identities' });
      for (const name of [
        'Revoke key dak_7Q2m for tasktracker',
        `Revoke key dak_9Zz9 for ${longName}`,
      ]) {
        await expectReachableByScrolling({
          wrapper: wrappers[4],
          control: identities.getByRole('button', { name, exact: true }),
        });
      }
      expect(mock.unmocked).toEqual([]);
    });
  }
});

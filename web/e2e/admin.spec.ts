import { expect, test } from '@playwright/test';

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
    // exact: true — the row's actions cell has an accessible name containing
    // "Delete u_abcdefgh23456777", which a substring match would also hit.
    await expect(page.getByRole('cell', { name: 'u_abcdefgh23456777', exact: true })).toBeVisible();
  });

  test('provisioning a user reveals the returned token', async ({ page }) => {
    let usersReturned = 0;
    await page.route('**/v1/users', async (route) => {
      // Empty before provisioning, one user afterwards.
      const users =
        usersReturned === 0
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

    // "App ID" also labels the Add-an-app form, so scope to the Provision
    // section (by its heading) to disambiguate.
    const provisionSection = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Provision a user into an app' }) });
    await provisionSection.getByLabel('App ID').fill('urls4irl');
    await provisionSection.getByLabel('Email').fill('alice@example.com');
    await provisionSection.getByRole('button', { name: 'Provision', exact: true }).click();

    await expect(page.getByText('tk_e2e_secret')).toBeVisible();
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
    await page.getByRole('button', { name: 'Confirm remove URLs4IRL', exact: true }).click();

    await expect(page.getByText('No apps registered yet.')).toBeVisible();
  });
});

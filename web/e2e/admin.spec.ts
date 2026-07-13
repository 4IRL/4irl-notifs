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
          users: [{ user_id: 'alice', apps: ['urls4irl'], topic_patterns: ['urls4irl-*'] }],
        }),
      });
    });

    await page.goto('/');

    await expect(page.getByRole('heading', { name: '4IRL Notifications Admin' })).toBeVisible();
    // exact: true — the row's actions cell has an accessible name containing
    // "Delete alice", which a substring match would also hit.
    await expect(page.getByRole('cell', { name: 'alice', exact: true })).toBeVisible();
  });

  test('provisioning a user reveals the returned token', async ({ page }) => {
    let usersReturned = 0;
    await page.route('**/v1/users', async (route) => {
      // Empty before provisioning, one user afterwards.
      const users =
        usersReturned === 0
          ? []
          : [{ user_id: 'alice', apps: ['urls4irl'], topic_patterns: ['urls4irl-*'] }];
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
          user_id: 'alice',
          app_id: 'urls4irl',
          topic_pattern: 'urls4irl-*',
          token: 'tk_e2e_secret',
        }),
      });
    });

    await page.goto('/');

    await page.getByLabel('App ID').fill('urls4irl');
    await page.getByLabel('User ID').fill('alice');
    await page.getByRole('button', { name: 'Provision', exact: true }).click();

    await expect(page.getByText('tk_e2e_secret')).toBeVisible();
  });
});

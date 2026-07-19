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
    await page.route('https://person-service.e2e.test/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
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
    await page.route('https://person-service.e2e.test/people', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
    });

    await page.goto('/');

    await page.getByLabel('App ID').fill('urls4irl');
    await page.getByLabel('User ID').fill('alice');
    await page.getByLabel('Email').fill('alice@example.com');
    await page.getByRole('button', { name: 'Provision', exact: true }).click();

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
    await page.route('https://person-service.e2e.test/people', async (route) => {
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

    await page.goto('/');

    await expect(page.getByRole('heading', { name: 'People' })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'alice@example.com' })).toBeVisible();
  });
});

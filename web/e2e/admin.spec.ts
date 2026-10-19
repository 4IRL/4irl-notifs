import { expect, test } from '@playwright/test';
import type { Locator, Page, Route } from '@playwright/test';

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

// ---------------------------------------------------------------------------
// Delivery tab. delivery-api is reached through the same-origin `/delivery/*`
// Pages Function; here every `/delivery/**` request is answered by a small
// stateful mock, and any request the mock does not know fails the test.
// ---------------------------------------------------------------------------

type Json = Record<string, unknown>;

interface RecordedRequest {
  method: string;
  pathname: string;
  search: string;
  body: unknown;
}

interface DeliveryMockSeed {
  readiness?: { status: number; body: Json };
  destinations?: Json[];
  routes?: Json[];
  /** Deliveries by state, as the admin list returns them. */
  deliveries?: Record<string, Json[]>;
  attempts?: Record<number, Json[]>;
  /** Scheduled messages, as the admin list returns them (soonest first). */
  scheduled?: Json[];
  /** When set, DELETE of a scheduled message fails with this status and message. */
  cancelError?: { status: number; message: string };
}

interface DeliveryMock {
  /** Every non-GET request, in order. */
  mutations: RecordedRequest[];
  /** Every GET request, in order. */
  reads: RecordedRequest[];
  /** Requests the mock had no handler for (must stay empty). */
  unmocked: string[];
}

const TIMESTAMP = '2026-10-17T08:12:44Z';
const READY_BODY: Json = {
  status: 'ok',
  schema_version: 5,
  expected_schema_version: 5,
  last_migration_failure: null,
};

function destinationWire({
  name,
  type,
  enabled = true,
  config = {},
  secrets = {},
}: {
  name: string;
  type: string;
  enabled?: boolean;
  config?: Json;
  secrets?: Json;
}): Json {
  return {
    name,
    type,
    enabled,
    disabled_reason: enabled ? null : 'disabled by an operator',
    config,
    secrets,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

function deliveryWire({ id, state = 'dead' }: { id: number; state?: string }): Json {
  return {
    id,
    message_id: 'b3a1c6f0-1111-4222-8333-444455556666',
    app_id: 'urls4irl',
    channel: 'alerts',
    destination: 'hook1',
    destination_type: 'webhook',
    state,
    dead_reason: state === 'dead' ? 'max_attempts' : null,
    attempt_count: 3,
    last_status_code: 500,
    next_attempt_at: null,
    delivered_at: null,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

function scheduledWire({
  id,
  key,
  deliverAt = '2099-01-01T08:00:00Z',
}: {
  id: string;
  key: string;
  deliverAt?: string;
}): Json {
  return {
    message_id: id,
    app_id: 'urls4irl',
    channel: 'digest',
    idempotency_key: key,
    deliver_at: deliverAt,
    created_at: TIMESTAMP,
    recipient: false,
    destinations: [{ name: 'hook1', type: 'webhook' }],
  };
}

/**
 * The largest number of rendered lines of any single text node under `root`
 * (the elements matching `skip` are left out): 1 means nothing wraps.
 */
async function maxRenderedLines({
  root,
  skip = ':not(*)',
}: {
  root: Locator;
  skip?: string;
}): Promise<number> {
  return root.evaluate((element, skipSelector) => {
    const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
    let most = 0;
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      if (
        (node.textContent ?? '').trim() === '' ||
        node.parentElement?.closest(skipSelector) != null
      ) {
        continue;
      }
      const range = document.createRange();
      range.selectNodeContents(node);
      const tops = new Set(Array.from(range.getClientRects(), (rect) => Math.round(rect.top)));
      most = Math.max(most, tops.size);
    }
    return most;
  }, skip);
}

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

async function mockProvisioning(page: Page): Promise<void> {
  await page.route('**/v1/users', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{"users":[]}' });
  });
  await page.route('**/people', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{"people":[]}' });
  });
  await page.route('**/apps', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: '{"apps":[]}' });
  });
}

async function mockDelivery(page: Page, seed: DeliveryMockSeed = {}): Promise<DeliveryMock> {
  const mock: DeliveryMock = { mutations: [], reads: [], unmocked: [] };
  const destinations = [...(seed.destinations ?? [])];
  const routes = [...(seed.routes ?? [])];
  const deliveries: Record<string, Json[]> = {
    dead: [],
    pending: [],
    delivered: [],
    ...seed.deliveries,
  };
  let scheduled = [...(seed.scheduled ?? [])];
  let nextRouteId = 100;

  async function reply({
    route,
    status = 200,
    body,
  }: {
    route: Route;
    status?: number;
    body: unknown;
  }) {
    await route.fulfill({
      status,
      contentType: 'application/json',
      body: JSON.stringify(body),
    });
  }

  await page.route('**/delivery/**', async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const method = request.method();
    const pathname = url.pathname;
    const raw = request.postData();
    const recorded: RecordedRequest = {
      method,
      pathname,
      search: url.search,
      body: raw === null ? null : (JSON.parse(raw) as unknown),
    };
    if (method === 'GET') {
      mock.reads.push(recorded);
    } else {
      mock.mutations.push(recorded);
    }

    if (method === 'GET' && pathname === '/delivery/readyz') {
      const readiness = seed.readiness ?? { status: 200, body: READY_BODY };
      await reply({ route, status: readiness.status, body: readiness.body });
      return;
    }
    if (pathname === '/delivery/v1/admin/destinations') {
      if (method === 'GET') {
        await reply({ route, body: { destinations } });
        return;
      }
      if (method === 'POST') {
        const body = recorded.body as Json;
        const created = destinationWire({
          name: body.name as string,
          type: body.type as string,
          config: body.config as Json,
          secrets: Object.fromEntries(
            Object.keys(body.secrets as Json).map((key) => [
              key,
              key === 'url' ? 'https://hooks.example.com/****' : '****',
            ]),
          ),
        });
        destinations.push(created);
        await reply({ route, status: 201, body: { destination: created } });
        return;
      }
    }
    const destinationMatch =
      /^\/delivery\/v1\/admin\/destinations\/([^/]+)(\/rotate-secret)?$/.exec(pathname);
    if (destinationMatch !== null) {
      const target = destinations.find((candidate) => candidate.name === destinationMatch[1]);
      if (target === undefined) {
        await reply({ route, status: 404, body: { error: 'destination does not exist' } });
        return;
      }
      if (method === 'PATCH' && destinationMatch[2] === undefined) {
        const body = recorded.body as Json;
        if (typeof body.enabled === 'boolean') {
          target.enabled = body.enabled;
          target.disabled_reason = body.enabled ? null : 'disabled by an operator';
        }
        await reply({ route, body: { destination: target } });
        return;
      }
      if (method === 'POST' && destinationMatch[2] !== undefined) {
        target.config = { previous_secret_expires_at: '2026-10-18T08:12:44Z' };
        await reply({
          route,
          body: {
            destination: target,
            signing_secret: 'rotated_signing_secret_e2e_0123456789abcdef',
          },
        });
        return;
      }
    }
    if (pathname === '/delivery/v1/admin/routes') {
      if (method === 'GET') {
        await reply({ route, body: { routes } });
        return;
      }
      if (method === 'POST') {
        const body = recorded.body as Json;
        const created = {
          id: (nextRouteId += 1),
          ...body,
          created_at: TIMESTAMP,
          updated_at: TIMESTAMP,
        };
        routes.push(created);
        await reply({ route, status: 201, body: { route: created } });
        return;
      }
    }
    if (method === 'GET' && pathname === '/delivery/v1/admin/deliveries') {
      const state = url.searchParams.get('state') ?? '';
      await reply({ route, body: { deliveries: deliveries[state] ?? [] } });
      return;
    }
    const attemptsMatch = /^\/delivery\/v1\/admin\/deliveries\/(\d+)\/attempts$/.exec(pathname);
    if (method === 'GET' && attemptsMatch !== null) {
      await reply({ route, body: { attempts: seed.attempts?.[Number(attemptsMatch[1])] ?? [] } });
      return;
    }
    const replayMatch = /^\/delivery\/v1\/admin\/deliveries\/(\d+)\/replay$/.exec(pathname);
    if (method === 'POST' && replayMatch !== null) {
      const id = Number(replayMatch[1]);
      deliveries.dead = deliveries.dead.filter((delivery) => delivery.id !== id);
      await reply({
        route,
        body: {
          delivery: {
            ...deliveryWire({ id, state: 'pending' }),
            attempt_count: 0,
            next_attempt_at: '2026-10-17T08:15:00Z',
          },
        },
      });
      return;
    }
    if (method === 'GET' && pathname === '/delivery/v1/admin/scheduled-messages') {
      await reply({ route, body: { scheduled_messages: scheduled, total: scheduled.length } });
      return;
    }
    const scheduledMatch = /^\/delivery\/v1\/admin\/scheduled-messages\/([^/]+)$/.exec(pathname);
    if (method === 'DELETE' && scheduledMatch !== null) {
      if (seed.cancelError !== undefined) {
        await reply({
          route,
          status: seed.cancelError.status,
          body: { error: seed.cancelError.message },
        });
        return;
      }
      const id = scheduledMatch[1];
      const found = scheduled.some((message) => message.message_id === id);
      scheduled = scheduled.filter((message) => message.message_id !== id);
      await reply({
        route,
        body: found
          ? { message_id: id, cancelled: true, status: 'cancelled' }
          : { message_id: id, cancelled: false, status: 'not_found' },
      });
      return;
    }
    if (method === 'POST' && pathname === '/delivery/v1/messages') {
      await reply({
        route,
        status: 201,
        body: {
          message: {
            message_id: 'b3a1c6f0-1111-4222-8333-444455556666',
            status: 'delivered',
            deliveries: [
              {
                id: 1,
                destination: (recorded.body as { to: { destination: string } }).to.destination,
                state: 'delivered',
                dead_reason: null,
                attempt_count: 1,
                last_status_code: 200,
                next_attempt_at: null,
                delivered_at: TIMESTAMP,
              },
            ],
          },
        },
      });
      return;
    }

    mock.unmocked.push(`${method} ${pathname}${url.search}`);
    await reply({ route, status: 404, body: { error: 'unmocked' } });
  });
  return mock;
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

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
    const sendTest = mock.mutations.find((request) => request.pathname === '/delivery/v1/messages');
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
    await page.getByRole('button', { name: 'Confirm rotate hook1', exact: true }).click();
    await expect(page.getByText('rotated_signing_secret_e2e_0123456789abcdef')).toBeVisible();
    const rotate = mock.mutations.find((request) => request.pathname.endsWith('/rotate-secret'));
    expect(rotate?.method).toBe('POST');
    expect(rotate?.pathname).toBe('/delivery/v1/admin/destinations/hook1/rotate-secret');
    expect(rotate?.body).toEqual({ overlap_hours: 24 });
    await page.getByRole('button', { name: 'Done / I have saved it' }).click();
    await expect(page.getByText('rotated_signing_secret_e2e_0123456789abcdef')).toBeHidden();

    // Disable through the confirm.
    await page.getByRole('button', { name: 'Disable hook1', exact: true }).click();
    await page.getByRole('button', { name: 'Confirm disable hook1', exact: true }).click();
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
    await log.getByRole('button', { name: 'Confirm replay delivery 7', exact: true }).click();
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
    const confirm = pending.getByRole('button', {
      name: 'Confirm cancel send digest-1',
      exact: true,
    });
    const keep = pending.getByRole('button', { name: 'Keep', exact: true });
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

  test('pending sends: a long cancel error wraps inside the capped Actions cell', async ({
    page,
  }) => {
    const mock = await mockDelivery(page, {
      scheduled: [scheduledWire({ id: 'msg-1', key: 'digest-1' })],
      cancelError: { status: 503, message: 'delivery-service-unavailable-'.repeat(12) },
    });

    await page.goto('/#delivery');
    const pending = page.getByRole('region', { name: 'Pending sends' });
    await pending.getByRole('button', { name: 'Cancel send digest-1', exact: true }).click();
    await pending
      .getByRole('button', { name: 'Confirm cancel send digest-1', exact: true })
      .click();

    const rowError = pending.getByRole('alert');
    await expect(rowError).toContainText('Could not cancel: delivery-service-unavailable-');
    // 14rem cap (224px) plus the cell padding; an uncapped cell would take the spare width.
    const cellBox = await pending.locator('td.pending-sends__actions-cell').first().boundingBox();
    expect(cellBox?.width ?? Infinity).toBeLessThanOrEqual(272);
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
    await page.goto('/#delivery');
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
    await page.goto('/#delivery');
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
      routes: [
        {
          id: 1,
          app_pattern: 'urls4irl',
          channel_pattern: 'alerts*',
          destinations: ['hook1'],
          enabled: true,
          created_at: TIMESTAMP,
          updated_at: TIMESTAMP,
        },
      ],
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

    // The open confirm keeps both labels on one line too.
    await trigger.click();
    await expect(pending.getByRole('button', { name: /^Confirm cancel send/ })).toBeVisible();
    // (The confirm notice is a sentence that wraps inside the capped Actions cell by design.)
    expect(
      await maxRenderedLines({ root: pending.locator('table'), skip: '.confirm-button__notice' }),
    ).toBe(1);

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
        }),
      ],
      routes: [
        {
          id: 1,
          app_pattern: 'urls4irl',
          channel_pattern: 'alerts*',
          destinations: ['hook1', 'ops_discord'],
          enabled: true,
          created_at: TIMESTAMP,
          updated_at: TIMESTAMP,
        },
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
    await expect(page.getByRole('cell', { name: 'ops_discord', exact: true })).toBeVisible();
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
    const confirmCancel = pending.getByRole('button', { name: /^Confirm cancel send digest-/ });
    await expect(confirmCancel).toBeVisible();
    expect(await pendingWrapper.evaluate((element) => element.scrollLeft)).toBe(0);
    const confirmBox = await confirmCancel.boundingBox();
    expect((confirmBox?.x ?? 0) + (confirmBox?.width ?? 0)).toBeLessThanOrEqual(390);

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
    expect(mock.unmocked).toEqual([]);
  });
});

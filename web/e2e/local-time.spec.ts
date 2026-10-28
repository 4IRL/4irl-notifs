import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

import {
  appWire,
  deliveryWire,
  expectNoPageOverflow,
  heartbeatWire,
  identityBindingWire,
  identityKeyWire,
  identityRequestWire,
  mockDelivery,
  mockProvisioning,
  scheduledWire,
  TIMESTAMP,
} from './support/adminMocks';

interface ZoneCase {
  name: string;
  timezoneId: string;
  /** TIMESTAMP (2026-10-17T08:12:44Z) in this zone. */
  local: string;
  /** The Provisioning seed instants in this zone: apps 2026-07-25T10:00Z, people 2026-07-19T18:12:03Z. */
  appCreated: string;
  personCreated: string;
  /** The IANA zone name the hint prints (Intl may report a legacy alias). */
  hintZone: RegExp;
}

const ZONES: ZoneCase[] = [
  {
    name: 'America/Los_Angeles',
    timezoneId: 'America/Los_Angeles',
    local: '2026-10-17 01:12 PDT',
    appCreated: '2026-07-25 03:00 PDT',
    personCreated: '2026-07-19 11:12 PDT',
    hintZone: /America\/Los_Angeles/,
  },
  {
    name: 'Asia/Kolkata',
    timezoneId: 'Asia/Kolkata',
    local: '2026-10-17 13:42 GMT+5:30',
    appCreated: '2026-07-25 15:30 GMT+5:30',
    personCreated: '2026-07-19 23:42 GMT+5:30',
    hintZone: /Asia\/(Kolkata|Calcutta)/,
  },
];

const PERSON_HASH = '76gzqgp4byjl6dje';

const DELIVERY_SEED = {
  deliveries: { dead: [deliveryWire({ id: 7 })] },
  scheduled: [scheduledWire({ id: 'm1', key: 'sched-1' })],
  heartbeats: [heartbeatWire({ id: 1, name: 'nightly', lastPingAt: TIMESTAMP })],
  identityRequests: [identityRequestWire({ id: 1, appId: 'urls4irl' })],
  identityBindings: [
    identityBindingWire({
      appId: 'chores4irl',
      keys: [identityKeyWire({ id: 5, appId: 'chores4irl', hint: 'dak_Ab12' })],
    }),
  ],
};

const PROVISIONING_SEED = {
  users: [{ user_id: `u_${PERSON_HASH}`, apps: ['urls4irl'], topic_patterns: [] }],
  people: [
    { person_hash: PERSON_HASH, email: 'alice@example.com', created_at: '2026-07-19T18:12:03Z' },
  ],
  apps: [appWire({ appId: 'urls4irl', displayName: 'URLs4IRL' })],
};

async function openDelivery(page: Page): Promise<void> {
  await mockProvisioning(page, PROVISIONING_SEED);
  await mockDelivery(page, DELIVERY_SEED);
  await page.goto('/#delivery');
  await expect(page.locator('section.delivery-log time').first()).toBeVisible();
  await expect(page.locator('section.pending-sends time').first()).toBeVisible();
  await expect(page.locator('section.heartbeats time').first()).toBeVisible();
  await expect(page.locator('section.app-identities time').first()).toBeVisible();
}

async function openProvisioning(page: Page): Promise<void> {
  await mockDelivery(page, DELIVERY_SEED);
  await mockProvisioning(page, PROVISIONING_SEED);
  await page.goto('/');
  await expect(page.locator('section.apps-table time').first()).toBeVisible();
  await expect(page.locator('section.people-table time').first()).toBeVisible();
}

for (const zone of ZONES) {
  test.describe(`local time in ${zone.name}`, () => {
    test.use({ timezoneId: zone.timezoneId });

    test('Delivery tab surfaces show the local value with the zone abbreviation', async ({
      page,
    }) => {
      await openDelivery(page);

      for (const sectionSelector of [
        'section.delivery-log',
        'section.pending-sends',
        'section.heartbeats',
        'section.app-identities',
      ]) {
        const times = page.locator(`${sectionSelector} time`, { hasText: zone.local });
        await expect(times.first(), `${sectionSelector} shows ${zone.local}`).toBeVisible();
        await expect(times.first()).toHaveAttribute('datetime', '2026-10-17T08:12:44.000Z');
        await expect(times.first()).toHaveAttribute('title', '2026-10-17T08:12:44.000Z');
      }
      await expect(
        page.locator('section.app-identities').getByText(`approved ${zone.local}`),
      ).toBeVisible();
      await expect(page.locator('section.pending-sends .pending-sends__hint')).toContainText(
        zone.hintZone,
      );
      await expect(
        page.locator('section.heartbeats .heartbeats__hint', { hasText: 'Push checks' }),
      ).toContainText(zone.hintZone);
    });

    test('Provisioning createdAt cells show the local value with the zone abbreviation', async ({
      page,
    }) => {
      await openProvisioning(page);

      await expect(page.locator('section.apps-table time').first()).toHaveText(zone.appCreated);
      await expect(page.locator('section.people-table time').first()).toHaveText(
        zone.personCreated,
      );
    });

    test.describe('at 420px', () => {
      test.use({ viewport: { width: 420, height: 900 } });

      test('the Delivery tab has no horizontal page overflow with the zone abbreviation', async ({
        page,
      }) => {
        await openDelivery(page);
        await expectNoPageOverflow(page);
      });

      test('the Provisioning Apps and People tables have no horizontal page overflow', async ({
        page,
      }) => {
        await openProvisioning(page);
        await expect(page.locator('section.apps-table time').first()).toHaveText(zone.appCreated);
        await expect(page.locator('section.people-table time').first()).toHaveText(
          zone.personCreated,
        );
        await expectNoPageOverflow(page);
      });
    });
  });
}

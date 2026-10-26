import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

import {
  destinationWire,
  expectNoPageOverflow,
  expectTouchTarget,
  heartbeatWire,
  maxRenderedLines,
  mockDelivery,
  mockProvisioning,
  openRowMenu,
} from './support/adminMocks';
import type { DeliveryMockSeed } from './support/adminMocks';

// Website probe checks on the Heartbeats section. Like admin.spec.ts, the delivery API is
// mocked at the network layer (`mockDelivery`), so no stack is needed.

const PROBE_URL = 'https://status.example.com/health';
const MOBILE = { width: 390, height: 844 };

/** A probe result stamped "now", so the row is never stale and the list is not reloaded. */
function resultNow({
  status = null,
  reason = null,
}: { status?: number | null; reason?: string | null } = {}) {
  return { at: new Date().toISOString(), status, reason };
}

const OK_RESULT = (): ReturnType<typeof resultNow> => resultNow({ status: 200 });

/** Opens the Delivery tab with the standard destination and returns the Heartbeats section. */
async function openHeartbeats({
  page,
  seed,
  viewport,
}: {
  page: Page;
  seed: DeliveryMockSeed;
  viewport?: { width: number; height: number };
}) {
  await mockProvisioning(page);
  const mock = await mockDelivery(page, {
    destinations: [destinationWire({ name: 'hook1', type: 'webhook' })],
    ...seed,
  });
  if (viewport !== undefined) {
    await page.setViewportSize(viewport);
  }
  await page.goto('/#delivery');
  const section = page.getByRole('region', { name: 'Heartbeats' });
  await expect(section.getByRole('button', { name: 'Add check', exact: true })).toBeEnabled();
  return { mock, section };
}

function rowOf({ section, name }: { section: Locator; name: string }): Locator {
  return section.getByRole('row', { name: new RegExp(`urls4irl\\s*/\\s*${name}\\b`) });
}

/** Opens the add form with the Website probe kind selected. */
async function openProbeAddForm(section: Locator): Promise<Locator> {
  await section.getByRole('button', { name: 'Add check', exact: true }).click();
  const form = section.getByRole('form', { name: 'Add check' });
  await form.getByRole('radio', { name: 'Website probe' }).check();
  await expect(form.getByLabel('URL')).toBeVisible();
  return form;
}

test.describe('website probe checks', () => {
  test('add form creates a probe check with the exact POST body, then credentials show as set', async ({
    page,
  }) => {
    const { mock, section } = await openHeartbeats({
      page,
      seed: { heartbeats: [heartbeatWire({ id: 1, name: 'nightly_backup' })] },
    });

    const form = await openProbeAddForm(section);
    // The kind switch swaps Period/Grace for the probe fieldset.
    await expect(form.getByLabel('Period', { exact: true })).toHaveCount(0);
    await form.getByLabel('App ID').fill('minecrafting');
    await form.getByLabel('Check name').fill('site_up');
    await form.getByLabel('URL').fill(PROBE_URL);
    await form.getByLabel('Expected statuses').fill('200, 204');
    await form.getByLabel('Body must contain').fill('ok');
    await form.getByLabel('Client ID').fill('cf-id');
    await form.getByLabel('Client secret').fill('cf-secret');
    await form.getByRole('checkbox', { name: /^hook1/ }).check();
    await form.getByRole('button', { name: 'Add check', exact: true }).click();

    await expect(section.getByText('Added minecrafting/site_up.')).toBeVisible();
    await expect(form).toHaveCount(0);
    const creates = mock.mutations.filter((request) => request.method === 'POST');
    expect(creates).toHaveLength(1);
    // A probe create carries `probe` and never period_seconds / grace_seconds.
    expect(creates[0].body).toEqual({
      app_id: 'minecrafting',
      name: 'site_up',
      destinations: ['hook1'],
      probe: {
        url: PROBE_URL,
        expected_statuses: [200, 204],
        body_match: 'ok',
        timeout_seconds: 5,
        interval_seconds: 300,
        access_token: { client_id: 'cf-id', client_secret: 'cf-secret' },
      },
    });

    const createdRow = section.getByRole('row', { name: /minecrafting\s*\/\s*site_up/ });
    await expect(createdRow.getByText('New', { exact: true })).toBeVisible();
    await expect(createdRow.getByText('first probe')).toBeVisible();
    await expect(createdRow.getByText('status.example.com/health')).toBeVisible();
    // A brand-new probe never reads Late or Waiting.
    await expect(createdRow.getByText('Late', { exact: true })).toHaveCount(0);

    await section.getByRole('button', { name: 'Edit minecrafting/site_up', exact: true }).click();
    const editForm = section.getByRole('form', { name: 'Edit minecrafting/site_up' });
    await expect(editForm.getByText('Access credentials set')).toBeVisible();
    expect(mock.unmocked).toEqual([]);
  });

  test('a push check is still created without a probe object', async ({ page }) => {
    const { mock, section } = await openHeartbeats({ page, seed: {} });

    await section.getByRole('button', { name: 'Add check', exact: true }).click();
    const form = section.getByRole('form', { name: 'Add check' });
    await expect(form.getByRole('radio', { name: 'Push (app pings in)' })).toBeChecked();
    await form.getByLabel('App ID').fill('minecrafting');
    await form.getByLabel('Check name').fill('daily_report');
    await form.getByRole('checkbox', { name: /^hook1/ }).check();
    await form.getByRole('button', { name: 'Add check', exact: true }).click();

    await expect(section.getByText('Added minecrafting/daily_report.')).toBeVisible();
    const creates = mock.mutations.filter((request) => request.method === 'POST');
    expect(creates[0].body).toEqual({
      app_id: 'minecrafting',
      name: 'daily_report',
      period_seconds: 86400,
      grace_seconds: 7200,
      destinations: ['hook1'],
    });
    expect(mock.unmocked).toEqual([]);
  });

  test('a server 400 on probe create shows in the form alert and keeps the form and values', async ({
    page,
  }) => {
    const { mock, section } = await openHeartbeats({
      page,
      seed: { heartbeatCreateError: { status: 400, message: 'invalid probe.url' } },
    });

    const form = await openProbeAddForm(section);
    await form.getByLabel('App ID').fill('minecrafting');
    await form.getByLabel('Check name').fill('site_up');
    await form.getByLabel('URL').fill(PROBE_URL);
    await form.getByLabel('Body must contain').fill('ok');
    await form.getByRole('checkbox', { name: /^hook1/ }).check();
    await form.getByRole('button', { name: 'Add check', exact: true }).click();

    await expect(form.getByRole('alert')).toHaveText('invalid probe.url');
    await expect(form).toBeVisible();
    await expect(form.getByRole('radio', { name: 'Website probe' })).toBeChecked();
    await expect(form.getByLabel('App ID')).toHaveValue('minecrafting');
    await expect(form.getByLabel('Check name')).toHaveValue('site_up');
    await expect(form.getByLabel('URL')).toHaveValue(PROBE_URL);
    await expect(form.getByLabel('Body must contain')).toHaveValue('ok');
    await expect(form.getByRole('checkbox', { name: /^hook1/ })).toBeChecked();
    await expect(section.getByText('Added minecrafting/site_up.')).toHaveCount(0);
    expect(mock.unmocked).toEqual([]);
  });

  test('every probe display state: a probe never reads Late and a down row shows its reason', async ({
    page,
  }) => {
    const down = '2026-10-17T08:00:00Z';
    const { mock, section } = await openHeartbeats({
      page,
      seed: {
        heartbeats: [
          heartbeatWire({ id: 1, name: 'push_row' }),
          heartbeatWire({
            id: 2,
            name: 'new_probe',
            state: 'new',
            lastPingAt: null,
            lastPingKind: null,
            probe: { lastResult: null },
          }),
          heartbeatWire({
            id: 3,
            name: 'ok_probe',
            lastPingAt: null,
            lastPingKind: null,
            // Both derived backstop values are long past: the push rule would read this Late.
            deadlineAt: '2020-01-01T00:00:00Z',
            graceSeconds: 1,
            probe: { lastResult: OK_RESULT() },
          }),
          heartbeatWire({
            id: 4,
            name: 'failing',
            lastPingAt: null,
            lastPingKind: null,
            probe: {
              consecutiveFailures: 2,
              lastResult: resultNow({ reason: 'timeout' }),
            },
          }),
          heartbeatWire({
            id: 5,
            name: 'awaiting',
            lastPingAt: null,
            lastPingKind: null,
            probe: { lastResult: null },
          }),
          heartbeatWire({
            id: 6,
            name: 'down_probe',
            state: 'down',
            lastPingAt: null,
            lastPingKind: null,
            downSince: down,
            downReason: 'http_status',
            probe: {
              consecutiveFailures: 3,
              lastResult: resultNow({ status: 503, reason: 'http_status' }),
            },
          }),
          heartbeatWire({
            id: 7,
            name: 'paused_down',
            state: 'down',
            paused: true,
            lastPingAt: null,
            lastPingKind: null,
            downSince: down,
            downReason: 'timeout',
            probe: { lastResult: resultNow({ reason: 'timeout' }) },
          }),
          heartbeatWire({
            id: 8,
            name: 'config_fail',
            lastPingAt: null,
            lastPingKind: null,
            probe: {
              hasAccessToken: true,
              consecutiveFailures: 1,
              lastResult: resultNow({ reason: 'config' }),
            },
          }),
        ],
      },
    });

    const push = rowOf({ section, name: 'push_row' });
    await expect(push.getByText('Up', { exact: true })).toBeVisible();

    const fresh = rowOf({ section, name: 'new_probe' });
    await expect(fresh.getByText('New', { exact: true })).toBeVisible();
    await expect(fresh.getByText('first probe')).toBeVisible();

    const ok = rowOf({ section, name: 'ok_probe' });
    await expect(ok.getByText('Up', { exact: true })).toBeVisible();
    await expect(ok.getByText(/^OK · HTTP 200 · probed /)).toBeVisible();
    await expect(ok.getByText('Late', { exact: true })).toHaveCount(0);

    const failing = rowOf({ section, name: 'failing' });
    await expect(failing.getByText('Up', { exact: true })).toBeVisible();
    await expect(failing.getByText('2 of 3 failures')).toBeVisible();
    await expect(failing.getByText(/^Timed out · probed /)).toBeVisible();

    const awaiting = rowOf({ section, name: 'awaiting' });
    await expect(awaiting.getByText('Awaiting next probe')).toBeVisible();

    // The reason is visible text, not colour or a tooltip.
    const downRow = rowOf({ section, name: 'down_probe' });
    await expect(downRow.getByText('Down', { exact: true })).toBeVisible();
    await expect(downRow.getByText(/^HTTP 503 · since /)).toBeVisible();

    const paused = rowOf({ section, name: 'paused_down' });
    await expect(paused.getByText('Paused', { exact: true })).toBeVisible();
    await expect(paused.getByText('was down')).toBeVisible();

    const config = rowOf({ section, name: 'config_fail' });
    await expect(config.getByText('1 of 3 failures')).toBeVisible();
    await expect(config.getByText(/^Credentials could not be used · probed /)).toBeVisible();

    expect(mock.unmocked).toEqual([]);
  });

  test('edit: Replace focuses Client ID, Clear then Save sends null, Undo returns focus', async ({
    page,
  }) => {
    const { mock, section } = await openHeartbeats({
      page,
      seed: {
        heartbeats: [
          heartbeatWire({
            id: 1,
            name: 'guarded',
            lastPingAt: null,
            lastPingKind: null,
            probe: { hasAccessToken: true, lastResult: OK_RESULT() },
          }),
        ],
      },
    });

    const edit = section.getByRole('button', { name: 'Edit urls4irl/guarded', exact: true });
    const form = section.getByRole('form', { name: 'Edit urls4irl/guarded' });
    const replace = form.getByRole('button', { name: 'Replace', exact: true });
    const clear = form.getByRole('button', { name: 'Clear', exact: true });
    const patches = () => mock.mutations.filter((request) => request.method === 'PATCH');

    // A probe edit has no Period/Grace, focuses the URL, and shows credentials as set only.
    await edit.click();
    await expect(form.getByLabel('URL')).toBeFocused();
    await expect(form.getByLabel('Period', { exact: true })).toHaveCount(0);
    await expect(form.getByText('Access credentials set')).toBeVisible();
    await expect(form.getByLabel('Client secret')).toHaveCount(0);

    // Replace reveals the pair and focuses the client id; Cancel replace puts focus back.
    await replace.click();
    await expect(form.getByLabel('Client ID')).toBeFocused();
    await form.getByRole('button', { name: 'Cancel replace', exact: true }).click();
    await expect(replace).toBeFocused();
    await replace.click();
    await form.getByLabel('Client ID').fill('new-id');
    await form.getByLabel('Client secret').fill('new-secret');
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(section.getByText('Saved urls4irl/guarded.')).toBeVisible();
    await expect(form).toHaveCount(0);
    expect(patches()).toHaveLength(1);
    expect(patches()[0].pathname).toBe('/delivery/v1/admin/heartbeats/1');
    expect(patches()[0].body).toEqual({
      probe: { access_token: { client_id: 'new-id', client_secret: 'new-secret' } },
    });

    // Clear stages a removal and focuses Undo; Undo focuses Clear again; nothing is sent yet.
    await edit.click();
    await clear.click();
    await expect(form.getByText('Access credentials will be removed on save')).toBeVisible();
    const undo = form.getByRole('button', { name: 'Undo', exact: true });
    await expect(undo).toBeFocused();
    await undo.click();
    await expect(clear).toBeFocused();
    expect(patches()).toHaveLength(1);
    await clear.click();
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form).toHaveCount(0);
    expect(patches()).toHaveLength(2);
    expect(patches()[1].body).toEqual({ probe: { access_token: null } });

    // With no credentials left the pair is offered directly, and an ordinary field edit sends
    // only that field.
    await edit.click();
    await expect(replace).toHaveCount(0);
    await expect(form.getByLabel('Client ID')).toBeVisible();
    await form.getByLabel('Timeout (seconds)').fill('6');
    await form.getByRole('button', { name: 'Save', exact: true }).click();
    await expect(form).toHaveCount(0);
    expect(patches()).toHaveLength(3);
    expect(patches()[2].body).toEqual({ probe: { timeout_seconds: 6 } });
    expect(mock.unmocked).toEqual([]);
  });

  test('invalid probe submit marks fields aria-invalid, describes them, and focuses the first', async ({
    page,
  }) => {
    const { mock, section } = await openHeartbeats({ page, seed: {} });

    const form = await openProbeAddForm(section);
    await form.getByLabel('App ID').fill('minecrafting');
    await form.getByLabel('Check name').fill('site_up');
    await form.getByRole('checkbox', { name: /^hook1/ }).check();
    await form.getByLabel('Timeout (seconds)').fill('9');
    await form.getByRole('button', { name: 'Add check', exact: true }).click();

    // URL comes before Timeout in the form, so it takes focus.
    const url = form.getByLabel('URL');
    await expect(url).toBeFocused();
    await expect(url).toHaveAttribute('aria-invalid', 'true');
    await expect(url).toHaveAttribute(
      'aria-describedby',
      'heartbeat-probe-url-new-hint heartbeat-probe-url-new-error',
    );
    await expect(form.locator('#heartbeat-probe-url-new-error')).toHaveText(
      'Enter the URL to check.',
    );
    const timeout = form.getByLabel('Timeout (seconds)');
    await expect(timeout).toHaveAttribute('aria-invalid', 'true');
    await expect(timeout).toHaveAttribute(
      'aria-describedby',
      'heartbeat-probe-timeout-new-hint heartbeat-probe-timeout-new-error',
    );
    await expect(form.getByLabel('Expected statuses')).not.toHaveAttribute('aria-invalid', 'true');
    expect(mock.mutations).toEqual([]);

    // Fixing the URL clears its error; the next invalid field takes focus on the next submit.
    await url.fill(PROBE_URL);
    await expect(url).not.toHaveAttribute('aria-invalid', 'true');
    await form.getByRole('button', { name: 'Add check', exact: true }).click();
    await expect(timeout).toBeFocused();
    expect(mock.mutations).toEqual([]);

    // A half credential pair names the missing half.
    await timeout.fill('5');
    await form.getByLabel('Client ID').fill('cf-id');
    await form.getByRole('button', { name: 'Add check', exact: true }).click();
    const secret = form.getByLabel('Client secret');
    await expect(secret).toBeFocused();
    await expect(secret).toHaveAttribute('aria-invalid', 'true');
    expect(mock.mutations).toEqual([]);
    expect(mock.unmocked).toEqual([]);
  });
});

test.describe('website probe checks at 390px', () => {
  test('the config hint shows in the edit form for stored and missing credentials', async ({
    page,
  }) => {
    const { mock, section } = await openHeartbeats({
      page,
      viewport: MOBILE,
      seed: {
        heartbeats: [
          heartbeatWire({
            id: 1,
            name: 'cfg_stored',
            state: 'down',
            downSince: '2026-10-17T08:00:00Z',
            downReason: 'config',
            lastPingAt: null,
            lastPingKind: null,
            probe: {
              hasAccessToken: true,
              consecutiveFailures: 3,
              lastResult: resultNow({ reason: 'config' }),
            },
          }),
          heartbeatWire({
            id: 2,
            name: 'cfg_missing',
            state: 'down',
            downSince: '2026-10-17T08:00:00Z',
            downReason: 'config',
            lastPingAt: null,
            lastPingKind: null,
            probe: {
              hasAccessToken: false,
              consecutiveFailures: 3,
              lastResult: resultNow({ reason: 'config' }),
            },
          }),
        ],
      },
    });

    let menu = await openRowMenu({ page, row: 'urls4irl/cfg_stored' });
    await menu.getByRole('menuitem', { name: 'Edit urls4irl/cfg_stored', exact: true }).click();
    const stored = section.getByRole('form', { name: 'Edit urls4irl/cfg_stored' });
    await expect(
      stored.getByText(
        'The last probe could not use these credentials. Use Replace to enter new ones.',
      ),
    ).toBeVisible();
    await expect(stored.getByText('Enter new ones below.')).toHaveCount(0);
    await expect(stored.getByRole('button', { name: 'Replace', exact: true })).toBeVisible();
    await expectNoPageOverflow(page);

    // Opening the other row's editor replaces this one; its variant points at the inputs below.
    menu = await openRowMenu({ page, row: 'urls4irl/cfg_missing' });
    await menu.getByRole('menuitem', { name: 'Edit urls4irl/cfg_missing', exact: true }).click();
    const missing = section.getByRole('form', { name: 'Edit urls4irl/cfg_missing' });
    await expect(
      missing.getByText(
        'The last probe could not use the stored credentials. Enter new ones below.',
      ),
    ).toBeVisible();
    await expect(missing.getByText('Use Replace to enter new ones.')).toHaveCount(0);
    await expect(missing.getByLabel('Client ID')).toBeVisible();
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });

  test('a 403 row shows its whole hint wrapped, with no horizontal page overflow', async ({
    page,
  }) => {
    const { mock, section } = await openHeartbeats({
      page,
      viewport: MOBILE,
      seed: {
        heartbeats: [
          heartbeatWire({
            id: 1,
            name: 'edge_403',
            lastPingAt: null,
            lastPingKind: null,
            probe: {
              hasAccessToken: true,
              consecutiveFailures: 1,
              lastResult: resultNow({ status: 403, reason: 'http_status' }),
            },
          }),
          heartbeatWire({
            id: 2,
            name: 'edge_403_bare',
            lastPingAt: null,
            lastPingKind: null,
            probe: {
              hasAccessToken: false,
              consecutiveFailures: 1,
              lastResult: resultNow({ status: 403, reason: 'http_status' }),
            },
          }),
        ],
      },
    });
    await expect(section.locator('.heartbeats__scroll')).toBeVisible();

    const hints = [
      {
        name: 'edge_403',
        text: '403 with Access credentials set: the Cloudflare token may have expired or been revoked. Edit the check and use Replace.',
      },
      {
        name: 'edge_403_bare',
        text: '403: the endpoint may be behind Cloudflare Access. Edit the check and add credentials.',
      },
    ];
    for (const { name, text } of hints) {
      const hint = rowOf({ section, name }).locator('.heartbeats__sub--wrap');
      await expect(hint).toHaveText(text);
      // Wrapped over several lines rather than cut off or pushed sideways.
      expect(await maxRenderedLines({ root: hint })).toBeGreaterThan(1);
      const metrics = await hint.evaluate((element) => ({
        scrollWidth: element.scrollWidth,
        clientWidth: element.clientWidth,
        scrollHeight: element.scrollHeight,
        clientHeight: element.clientHeight,
        textOverflow: getComputedStyle(element).textOverflow,
      }));
      expect(metrics.textOverflow).not.toBe('ellipsis');
      expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth + 1);
      expect(metrics.scrollHeight).toBeLessThanOrEqual(metrics.clientHeight + 1);
    }
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });

  test('phone layout: 44px probe controls, long URL ellipsized, page and rows stay tidy', async ({
    page,
  }) => {
    const longPath = `/${'a'.repeat(2000)}`;
    const { mock, section } = await openHeartbeats({
      page,
      viewport: MOBILE,
      seed: {
        heartbeats: [
          heartbeatWire({ id: 1, name: 'push_row' }),
          heartbeatWire({
            id: 2,
            name: 'long_url',
            lastPingAt: null,
            lastPingKind: null,
            probe: { url: `https://example.com${longPath}`, lastResult: OK_RESULT() },
          }),
          heartbeatWire({
            id: 3,
            name: 'guarded',
            lastPingAt: null,
            lastPingKind: null,
            probe: { hasAccessToken: true, lastResult: OK_RESULT() },
          }),
        ],
      },
    });

    const wrapper = section.locator('.heartbeats__scroll');
    await expect(wrapper).toBeVisible();
    await expectNoPageOverflow(page);

    // The table scroll container (not the page) scrolls sideways.
    const sizes = await wrapper.evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
    }));
    expect(sizes.scrollWidth).toBeGreaterThan(sizes.clientWidth);

    // The 2000-char path is cut with an ellipsis on a single line, and no sub-line wraps.
    const longRow = rowOf({ section, name: 'long_url' });
    const url = longRow.locator('.heartbeats__url');
    await expect(url).toBeVisible();
    const urlMetrics = await url.evaluate((element) => ({
      scrollWidth: element.scrollWidth,
      clientWidth: element.clientWidth,
      textOverflow: getComputedStyle(element).textOverflow,
    }));
    expect(urlMetrics.textOverflow).toBe('ellipsis');
    expect(urlMetrics.scrollWidth).toBeGreaterThan(urlMetrics.clientWidth);
    expect(urlMetrics.clientWidth).toBeLessThanOrEqual(MOBILE.width);
    expect(await maxRenderedLines({ root: longRow })).toBe(1);
    // The last result stays readable in the row.
    await expect(longRow.getByText(/^OK · HTTP 200 · probed /)).toBeVisible();
    await expectNoPageOverflow(page);

    // The add form's kind radios and probe fields are touch sized and inside the viewport.
    await section.getByRole('button', { name: 'Add check', exact: true }).click();
    const addForm = section.getByRole('form', { name: 'Add check' });
    for (const radioLabel of ['Push (app pings in)', 'Website probe']) {
      await expectTouchTarget(addForm.locator('label', { hasText: radioLabel }));
    }
    await addForm.getByRole('radio', { name: 'Website probe' }).check();
    for (const control of [
      addForm.getByLabel('URL'),
      addForm.getByLabel('Expected statuses'),
      addForm.getByLabel('Body must contain'),
      addForm.getByLabel('Interval', { exact: true }),
      addForm.getByLabel('Interval unit'),
      addForm.getByLabel('Timeout (seconds)'),
      addForm.getByLabel('Client ID'),
      addForm.getByLabel('Client secret'),
      addForm.getByRole('button', { name: 'Add check', exact: true }),
      addForm.getByRole('button', { name: 'Cancel', exact: true }),
    ]) {
      const box = await control.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
      expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(MOBILE.width);
    }
    await expectNoPageOverflow(page);
    await addForm.getByRole('button', { name: 'Cancel', exact: true }).click();

    // The edit form's credential controls are touch sized in every staged state.
    const menu = await openRowMenu({ page, row: 'urls4irl/guarded' });
    await menu.getByRole('menuitem', { name: 'Edit urls4irl/guarded', exact: true }).click();
    const editForm = section.getByRole('form', { name: 'Edit urls4irl/guarded' });
    await expect(editForm).toBeVisible();
    await expectTouchTarget(editForm.getByRole('button', { name: 'Replace', exact: true }));
    await expectTouchTarget(editForm.getByRole('button', { name: 'Clear', exact: true }));
    await editForm.getByRole('button', { name: 'Clear', exact: true }).click();
    await expectTouchTarget(editForm.getByRole('button', { name: 'Undo', exact: true }));
    await editForm.getByRole('button', { name: 'Undo', exact: true }).click();
    await editForm.getByRole('button', { name: 'Replace', exact: true }).click();
    await expectTouchTarget(editForm.getByRole('button', { name: 'Cancel replace', exact: true }));
    for (const control of [
      editForm.getByLabel('Client ID'),
      editForm.getByLabel('Client secret'),
      editForm.getByRole('button', { name: 'Save', exact: true }),
    ]) {
      const box = await control.boundingBox();
      expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
      expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(MOBILE.width);
    }
    await expectNoPageOverflow(page);
    expect(mock.unmocked).toEqual([]);
  });
});

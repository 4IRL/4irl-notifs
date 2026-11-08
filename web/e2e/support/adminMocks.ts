import { expect } from '@playwright/test';
import type { Locator, Page, Route } from '@playwright/test';

// Shared fake-server and layout helpers for the admin browser tests. This is a plain module
// (not a spec), so several spec files can import it without importing each other.

// ---------------------------------------------------------------------------
// Delivery tab. delivery-api is reached through the same-origin `/delivery/*`
// Pages Function; here every `/delivery/**` request is answered by a small
// stateful mock, and any request the mock does not know fails the test.
// ---------------------------------------------------------------------------

export type Json = Record<string, unknown>;

export interface RecordedRequest {
  method: string;
  pathname: string;
  search: string;
  body: unknown;
}

export interface DeliveryMockSeed {
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
  /** Heartbeat checks, as the admin list returns them. */
  heartbeats?: Json[];
  /** When set, creating a heartbeat check fails with this status and message. */
  heartbeatCreateError?: { status: number; message: string };
  /** App identity requests, as the operator list returns them. */
  identityRequests?: Json[];
  /** Approved app identities with their keys, as the operator list returns them. */
  identityBindings?: Json[];
  /** Registrars, as the operator list returns them; empty by default (richer mocks come with the registrar e2e tests). */
  identityRegistrars?: Json[];
  /** When set, approving any request fails with this status and message. */
  approveError?: { status: number; message: string };
  /** When set, revoking any key fails with this status and message. */
  revokeError?: { status: number; message: string };
  /** When set, approving a registrar request fails with this status and message. */
  approveRegistrarError?: { status: number; message: string };
  /**
   * When set, the single-app acknowledge fails with this status and message; a 404 also
   * removes the binding, as if it had been unbound since the page loaded.
   */
  acknowledgeError?: { status: number; message: string };
  /** When set, acknowledge-all fails with this status and message. */
  acknowledgeAllError?: { status: number; message: string };
  /** Personal destinations, as the operator list returns them (empty by default). */
  personalDestinations?: Json[];
  /** The `total` the list reports; defaults to the number of rows served. */
  personalDestinationsTotal?: number;
}

export interface DeliveryMock {
  /** Every non-GET request, in order. */
  mutations: RecordedRequest[];
  /** Every GET request, in order. */
  reads: RecordedRequest[];
  /** Requests the mock had no handler for (must stay empty). */
  unmocked: string[];
}

export const TIMESTAMP = '2026-10-17T08:12:44Z';
/** The server derives a probe's grace; the exact value is irrelevant to the UI. */
const PROBE_DERIVED_GRACE_SECONDS = 300;
export const READY_BODY: Json = {
  status: 'ok',
  schema_version: 5,
  expected_schema_version: 5,
  last_migration_failure: null,
  heartbeat_checker: { last_tick_at: '2026-10-17T08:12:30Z', stale: false },
};

/** The wire owner fields; the default is the operator, as admin create always makes. */
export interface OwnerWireSeed {
  ownerKind?: string | null;
  ownerAppId?: string | null;
}

export function ownerWire({ ownerKind = 'operator', ownerAppId = null }: OwnerWireSeed): Json {
  return { owner_kind: ownerKind, owner_app_id: ownerAppId };
}

export function destinationWire({
  name,
  type,
  enabled = true,
  config = {},
  secrets = {},
  ...owner
}: {
  name: string;
  type: string;
  enabled?: boolean;
  config?: Json;
  secrets?: Json;
} & OwnerWireSeed): Json {
  return {
    name,
    type,
    enabled,
    disabled_reason: enabled ? null : 'disabled by an operator',
    config,
    secrets,
    ...ownerWire(owner),
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

export function routeWire({
  id,
  appPattern = 'urls4irl',
  channelPattern = 'alerts*',
  destinations = ['hook1'],
  ...owner
}: {
  id: number;
  appPattern?: string;
  channelPattern?: string;
  destinations?: string[];
} & OwnerWireSeed): Json {
  return {
    id,
    app_pattern: appPattern,
    channel_pattern: channelPattern,
    destinations,
    enabled: true,
    ...ownerWire(owner),
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

export function deliveryWire({
  id,
  state = 'dead',
  targetKind = 'destination',
  personHash = null,
  destination = 'hook1',
  deadReason = 'max_attempts',
}: {
  id: number;
  state?: string;
  targetKind?: 'destination' | 'recipient' | 'personal';
  personHash?: string | null;
  destination?: string;
  deadReason?: string;
}): Json {
  return {
    target_kind: targetKind,
    person_hash: personHash,
    id,
    message_id: 'b3a1c6f0-1111-4222-8333-444455556666',
    app_id: 'urls4irl',
    channel: 'alerts',
    destination,
    destination_type: 'webhook',
    state,
    dead_reason: state === 'dead' ? deadReason : null,
    attempt_count: 3,
    last_status_code: 500,
    next_attempt_at: null,
    delivered_at: null,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

export function scheduledWire({
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

/** A probe check's settings and live status, as the `probe` object of the check view. */
export interface ProbeWireSeed {
  url?: string;
  expectedStatuses?: number[];
  bodyMatch?: string | null;
  timeoutSeconds?: number;
  intervalSeconds?: number;
  consecutiveFailures?: number;
  failureThreshold?: number;
  hasAccessToken?: boolean;
  lastResult?: { at: string; status?: number | null; reason?: string | null } | null;
}

/** The full `probe` view for a seed; `body_match` is absent when unset, as on the real wire. */
export function probeWire({
  url = 'https://status.example.com/health',
  expectedStatuses = [200],
  bodyMatch = null,
  timeoutSeconds = 5,
  intervalSeconds = 300,
  consecutiveFailures = 0,
  failureThreshold = 3,
  hasAccessToken = false,
  lastResult = null,
}: ProbeWireSeed): Json {
  return {
    url,
    expected_statuses: expectedStatuses,
    ...(bodyMatch === null ? {} : { body_match: bodyMatch }),
    timeout_seconds: timeoutSeconds,
    interval_seconds: intervalSeconds,
    consecutive_failures: consecutiveFailures,
    failure_threshold: failureThreshold,
    has_access_token: hasAccessToken,
    last_result:
      lastResult === null
        ? null
        : {
            at: lastResult.at,
            ...(lastResult.status == null ? {} : { status: lastResult.status }),
            ...(lastResult.reason == null ? {} : { reason: lastResult.reason }),
          },
  };
}

export function heartbeatWire({
  id,
  appId = 'urls4irl',
  name,
  state = 'up',
  paused = false,
  lastPingAt = '2099-01-01T07:00:00Z',
  lastPingKind = 'success',
  downSince = null,
  downReason = null,
  periodSeconds = 86400,
  graceSeconds = 3600,
  deadlineAt = '2099-01-02T08:00:00Z',
  destinations = [{ name: 'hook1', type: 'webhook', enabled: true }],
  probe,
  ...owner
}: {
  id: number;
  appId?: string;
  name: string;
  state?: string;
  paused?: boolean;
  lastPingAt?: string | null;
  lastPingKind?: string | null;
  downSince?: string | null;
  downReason?: string | null;
  periodSeconds?: number;
  graceSeconds?: number;
  deadlineAt?: string;
  destinations?: Array<{ name: string; type: string; enabled: boolean }>;
  /** When set the check is a website probe; absent (not null) on a push check, as on the wire. */
  probe?: ProbeWireSeed;
} & OwnerWireSeed): Json {
  return {
    id,
    app_id: appId,
    name,
    period_seconds: periodSeconds,
    grace_seconds: graceSeconds,
    paused,
    state,
    last_ping_at: lastPingAt,
    last_ping_kind: lastPingKind,
    down_since: downSince,
    down_reason: downReason,
    deadline_at: deadlineAt,
    destinations,
    ...(probe === undefined ? {} : { probe: probeWire(probe) }),
    ...ownerWire(owner),
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

export function identityRequestWire({
  id,
  appId,
  kind = 'app',
  status = 'pending',
  repository = `4IRL/${appId}`,
  environment = 'production',
}: {
  id: number;
  /** Null for a registrar request. */
  appId: string | null;
  /** `app` by default, matching the Go body. */
  kind?: string;
  status?: string;
  repository?: string;
  environment?: string | null;
}): Json {
  return {
    id,
    kind,
    app_id: appId,
    github_repository: repository,
    github_repository_id: 812345000 + id,
    workflow_path: '.github/workflows/prod-deploy.yml',
    git_ref: 'refs/heads/main',
    environment,
    github_run_id: String(14000 + id),
    status,
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

export function identityKeyWire({
  id,
  appId,
  hint,
  state = 'active',
}: {
  id: number;
  appId: string;
  hint: string;
  state?: string;
}): Json {
  return {
    id,
    app_id: appId,
    key_hint: hint,
    state,
    created_at: TIMESTAMP,
    superseded_at: state === 'superseded' ? TIMESTAMP : null,
    revoked_at: state === 'revoked' ? TIMESTAMP : null,
    revoked_reason: state === 'revoked' ? 'operator' : null,
    last_used_at: null,
  };
}

export function identityBindingWire({
  appId,
  keys = [],
  registrar = null,
  acknowledgedAt,
}: {
  appId: string;
  keys?: Json[];
  /** The creating registrar, or null for an app approved one by one. */
  registrar?: { id: number; prefix: string } | null;
  /** Null marks a registrar app New; defaults to seen (approval time) as the Go body does. */
  acknowledgedAt?: string | null;
}): Json {
  return {
    app_id: appId,
    github_repository: registrar === null ? `4IRL/${appId}` : '4IRL/hosting',
    github_repository_id: registrar === null ? 812345678 : 900100,
    github_owner_id: 55,
    workflow_path: '.github/workflows/prod-deploy.yml',
    git_ref: 'refs/heads/main',
    environment: 'production',
    approved_at: TIMESTAMP,
    registrar,
    // An app approved one by one counts as seen at approval.
    acknowledged_at: acknowledgedAt === undefined ? TIMESTAMP : acknowledgedAt,
    keys,
  };
}

export const REGISTRAR_APP_LIMIT = 50;

export function identityRegistrarWire({
  id,
  prefix = 'hosted_',
  repository = '4IRL/hosting',
  appCount = 0,
}: {
  id: number;
  prefix?: string;
  repository?: string;
  appCount?: number;
}): Json {
  return {
    id,
    prefix,
    github_repository: repository,
    github_repository_id: 900100,
    github_owner_id: 55,
    workflow_path: '.github/workflows/prod-deploy.yml',
    git_ref: 'refs/heads/main',
    environment: 'production',
    approved_at: TIMESTAMP,
    app_count: appCount,
    app_limit: REGISTRAR_APP_LIMIT,
  };
}

export interface PersonalDestinationWireSeed {
  id: number;
  personHash?: string;
  label?: string;
  type?: 'discord' | 'webhook';
  registeredByAppId?: string;
  registrarRemoved?: boolean;
  optedInAppIds?: string[];
  enabled?: boolean;
  disabledBy?: 'app' | 'operator' | 'system' | null;
  disabledReason?: string | null;
}

/** The operator (admin) view of a personal destination, secrets masked like the server does. */
export function personalDestinationWire({
  id,
  personHash = '76gzqgp4byjl6dje',
  label = `Endpoint ${id}`,
  type = 'discord',
  registeredByAppId = 'urls4irl',
  registrarRemoved = false,
  optedInAppIds = [registeredByAppId],
  enabled = true,
  disabledBy = null,
  disabledReason = null,
}: PersonalDestinationWireSeed): Json {
  return {
    id,
    person_hash: personHash,
    label,
    type,
    config: {},
    secrets:
      type === 'discord'
        ? { webhook_url: 'https://discord.com/****' }
        : { url: 'https://hooks.example.net/****', signing_secret: '****' },
    enabled,
    disabled_by: enabled ? null : (disabledBy ?? 'operator'),
    disabled_reason: enabled ? null : disabledReason,
    registered_by_app_id: registeredByAppId,
    registrar_removed: registrarRemoved,
    opted_in_app_ids: [...optedInAppIds].sort(),
    created_at: TIMESTAMP,
    updated_at: TIMESTAMP,
  };
}

/**
 * The largest number of rendered lines of any single text node under `root`
 * (the elements matching `skip` are left out): 1 means nothing wraps.
 */
export async function maxRenderedLines({
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

/** Asserts the document itself never scrolls sideways: only a table wrapper may. */
export async function expectNoPageOverflow(page: Page): Promise<void> {
  const overflow = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth);
}

/**
 * Asserts the open dialog sits wholly inside the viewport (a small tolerance for rounding) and
 * that the document does not scroll sideways.
 */
export async function expectDialogInViewport(
  page: Page,
  { pageOverflow = true }: { pageOverflow?: boolean } = {},
): Promise<void> {
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  const box = await dialog.boundingBox();
  const viewport = page.viewportSize();
  if (box === null || viewport === null) {
    throw new Error('dialog or viewport has no size');
  }
  expect(box.x).toBeGreaterThanOrEqual(-1);
  expect(box.y).toBeGreaterThanOrEqual(-1);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 1);
  expect(box.y + box.height).toBeLessThanOrEqual(viewport.height + 1);
  if (pageOverflow) {
    await expectNoPageOverflow(page);
  }
}

/** Asserts a touch control is at least 44x44. */
export async function expectTouchTarget(locator: Locator): Promise<void> {
  const box = await locator.boundingBox();
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
  expect(box?.width ?? 0).toBeGreaterThanOrEqual(44);
}

/**
 * Asserts every cell of the table row `row` has its content centred vertically on the row (each
 * cell's content-box centre within 2px of the row centre). A cell with no content is skipped.
 */
export async function expectCellsCenteredInRow({ row }: { row: Locator }): Promise<void> {
  await expect(row).toBeVisible();
  const cells = await row.evaluate((element) => {
    const rowBox = element.getBoundingClientRect();
    const rowCentre = rowBox.top + rowBox.height / 2;
    return Array.from(element.querySelectorAll(':scope > td, :scope > th'), (cell) => {
      // A block wrapper (put back at once) measures the cell's line boxes, not a text node's
      // font box, whose centre is skewed by the font's ascent/descent (monospace ids).
      const wrapper = document.createElement('div');
      while (cell.firstChild !== null) {
        wrapper.appendChild(cell.firstChild);
      }
      cell.appendChild(wrapper);
      const box = wrapper.getBoundingClientRect();
      while (wrapper.firstChild !== null) {
        cell.insertBefore(wrapper.firstChild, wrapper);
      }
      cell.removeChild(wrapper);
      return {
        text: (cell.textContent ?? '').trim().slice(0, 30),
        empty: box.height === 0,
        offset: Math.abs(box.top + box.height / 2 - rowCentre),
      };
    });
  });
  expect(cells.length).toBeGreaterThan(0);
  for (const cell of cells) {
    if (!cell.empty) {
      expect(cell.offset, `cell "${cell.text}" is off the row centre`).toBeLessThanOrEqual(2);
    }
  }
}

/** The Actions kebab of the row named `row` (narrow layout only). */
export function rowKebab({ page, row }: { page: Page; row: string }): Locator {
  return page.getByRole('button', { name: `Actions for ${row}`, exact: true });
}

/** A row's menu. It is portalled to <body>, so it is found from the page, not the table. */
export function rowMenu({ page, row }: { page: Page; row: string }): Locator {
  return page.getByRole('menu', { name: `Actions for ${row}`, exact: true });
}

/** Opens a row's menu with a click and returns it, once visible. */
export async function openRowMenu({ page, row }: { page: Page; row: string }): Promise<Locator> {
  await rowKebab({ page, row }).click();
  const menu = rowMenu({ page, row });
  await expect(menu).toBeVisible();
  return menu;
}

/** Seed for the stateful provisioning mock, in the wire shapes the two APIs return. */
export interface ProvisioningSeed {
  /** `{ user_id, apps, topic_patterns }` rows of `GET /delivery/v1/admin/users`. */
  users?: Json[];
  /** `{ person_hash, email, created_at }` rows of `GET /people`. */
  people?: Json[];
  /** `{ app_id, display_name, description, created_at }` rows of `GET /apps`. */
  apps?: Json[];
  /** App ids whose publisher mint returns 404 `app has no approved binding` (default none). */
  unboundAppIds?: string[];
}

/** A request log like `DeliveryMock`'s, plus the live seeded state (mutated by the handlers). */
export interface ProvisioningMock extends DeliveryMock {
  users: Json[];
  people: Json[];
  apps: Json[];
}

export function appWire({
  appId,
  displayName,
  description = null,
}: {
  appId: string;
  displayName: string;
  description?: string | null;
}): Json {
  return {
    app_id: appId,
    display_name: displayName,
    description,
    created_at: '2026-07-25T10:00:00Z',
  };
}

/**
 * A fixture-only token-like field the mint mock adds to its response. The real wire shape has
 * no token (delivery-api keeps it sealed), so a spec asserting this text is absent from the page
 * proves the UI never surfaces a token-like field.
 */
export const DECOY_TOKEN = 'DECOY-TOKEN-SHOULD-NEVER-SURFACE';

// The relayed calls live at `/delivery/v1/admin/...` (users, users/{id}, provision,
// deprovision, reset-password, test-notify, apps/{id}/deprovision, apps/{id}/ntfy-publisher).
// `mockDelivery`'s `**/delivery/**` catch-all
// defers these paths with `route.fallback()` (see RELAYED_ADMIN_PATH), so this mock serves
// them whether it is registered before or after `mockDelivery`. Only users, people and apps
// take an `/<id>` suffix (capture group 1). A spec that installs `mockDelivery` without
// `mockProvisioning` and hits one of these paths reaches the network unrecorded, as it did
// with the old `/v1/...` paths; such specs must also install `mockProvisioning`.
const PROVISIONING_PATH =
  /^\/(?:delivery\/v1\/admin\/(?:provision|deprovision|reset-password|test-notify)|delivery\/v1\/admin\/apps\/[^/]+\/(?:deprovision|ntfy-publisher)|(?:delivery\/v1\/admin\/users|people|apps)(?:\/([^/]+))?)$/;

/** The relayed `/delivery/v1/admin/*` paths `mockProvisioning` owns (keep in sync with PROVISIONING_PATH). */
const RELAYED_ADMIN_PATH =
  /^\/delivery\/v1\/admin\/(?:users(?:\/[^/]+)?|provision|deprovision|reset-password|test-notify|apps\/[^/]+\/(?:deprovision|ntfy-publisher))$/;

/**
 * A stateful fake of the provisioning API and the person service: list, edit and delete users,
 * people and apps, mint and re-mint tokens, deprovision and reset passwords. Every non-GET is
 * recorded in `mutations`; a request with no handler lands in `unmocked`. With no seed every
 * list is empty, so a one-argument call stays valid.
 */
export async function mockProvisioning(
  page: Page,
  seed: ProvisioningSeed = {},
): Promise<ProvisioningMock> {
  const mock: ProvisioningMock = {
    mutations: [],
    reads: [],
    unmocked: [],
    users: [...(seed.users ?? [])],
    people: [...(seed.people ?? [])],
    apps: [...(seed.apps ?? [])],
  };
  // Every other app id counts as bound and mints.
  const unboundAppIds = seed.unboundAppIds ?? [];

  async function reply({
    route,
    status = 200,
    body,
  }: {
    route: Route;
    status?: number;
    body: unknown;
  }) {
    await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  }

  await page.route(
    (url) => PROVISIONING_PATH.test(url.pathname),
    async (route) => {
      const request = route.request();
      // Page navigations and assets under these paths are not API calls.
      if (request.resourceType() !== 'fetch' && request.resourceType() !== 'xhr') {
        await route.fallback();
        return;
      }
      const url = new URL(request.url());
      const method = request.method();
      const raw = request.postData();
      const recorded: RecordedRequest = {
        method,
        pathname: url.pathname,
        search: url.search,
        body: raw === null ? null : (JSON.parse(raw) as unknown),
      };
      if (method === 'GET') {
        mock.reads.push(recorded);
      } else {
        mock.mutations.push(recorded);
      }
      const body = (recorded.body ?? {}) as Json;
      const match = PROVISIONING_PATH.exec(url.pathname);
      const id = match?.[1] === undefined ? undefined : decodeURIComponent(match[1]);

      if (method === 'GET' && url.pathname === '/delivery/v1/admin/users') {
        await reply({ route, body: { users: mock.users } });
        return;
      }
      if (method === 'GET' && url.pathname === '/people') {
        await reply({ route, body: { people: mock.people } });
        return;
      }
      if (method === 'GET' && url.pathname === '/apps') {
        await reply({ route, body: { apps: mock.apps } });
        return;
      }
      if (method === 'POST' && url.pathname === '/apps') {
        const created = appWire({
          appId: body.app_id as string,
          displayName: body.display_name as string,
          description: (body.description as string | null | undefined) ?? null,
        });
        mock.apps.push(created);
        await reply({ route, status: 201, body: created });
        return;
      }
      if (url.pathname.startsWith('/apps/') && id !== undefined) {
        const target = mock.apps.find((candidate) => candidate.app_id === id);
        if (target === undefined) {
          await reply({ route, status: 404, body: { error: 'app not found' } });
          return;
        }
        if (method === 'PATCH') {
          // An omitted field is left unchanged; `description: null` clears it.
          if (body.display_name !== undefined) target.display_name = body.display_name;
          if (body.description !== undefined) target.description = body.description;
          await reply({ route, body: target });
          return;
        }
        if (method === 'DELETE') {
          mock.apps = mock.apps.filter((candidate) => candidate !== target);
          await reply({ route, body: {} });
          return;
        }
      }
      if (method === 'DELETE' && url.pathname.startsWith('/people/') && id !== undefined) {
        mock.people = mock.people.filter((candidate) => candidate.person_hash !== id);
        await reply({ route, body: {} });
        return;
      }
      if (
        method === 'DELETE' &&
        url.pathname.startsWith('/delivery/v1/admin/users/') &&
        id !== undefined
      ) {
        mock.users = mock.users.filter((candidate) => candidate.user_id !== id);
        await reply({ route, body: {} });
        return;
      }
      const mint = /^\/delivery\/v1\/admin\/apps\/([^/]+)\/ntfy-publisher$/.exec(url.pathname);
      if (method === 'POST' && mint !== null) {
        const mintAppId = decodeURIComponent(mint[1]);
        if (unboundAppIds.includes(mintAppId)) {
          await reply({ route, status: 404, body: { error: 'app has no approved binding' } });
          return;
        }
        await reply({
          route,
          body: {
            publisher: {
              app_id: mintAppId,
              publisher_user_id: `${mintAppId}-publisher`,
              topic_pattern: `${mintAppId}-*`,
              created_at: '2026-11-03T10:00:00Z',
              updated_at: '2026-11-03T10:00:00Z',
              // Deliberate fixture-only deviation from the real wire shape (see DECOY_TOKEN).
              token: DECOY_TOKEN,
            },
          },
        });
        return;
      }
      const cascade = /^\/delivery\/v1\/admin\/apps\/([^/]+)\/deprovision$/.exec(url.pathname);
      if (method === 'POST' && cascade !== null) {
        const pathAppId = decodeURIComponent(cascade[1]);
        // delivery-api requires the body's app_id to equal the path id.
        if (body.app_id !== pathAppId) {
          await reply({ route, status: 400, body: { error: 'app_id in body must match path' } });
          return;
        }
        mock.apps = mock.apps.filter((candidate) => candidate.app_id !== pathAppId);
        await reply({
          route,
          body: {
            app_id: pathAppId,
            removed: true,
            unbound: true,
            deleted: { checks: 0, routes: 0, destinations: 0 },
          },
        });
        return;
      }
      if (method === 'POST' && url.pathname === '/delivery/v1/admin/deprovision') {
        mock.users = mock.users.filter((candidate) => candidate.user_id !== body.user_id);
        await reply({ route, body: { app_id: body.app_id, user_id: body.user_id, removed: true } });
        return;
      }
      if (method === 'POST' && url.pathname === '/delivery/v1/admin/reset-password') {
        await reply({
          route,
          body: { user_id: 'u_reset', app_id: body.app_id, password: 'reset-password-e2e-0123' },
        });
        return;
      }

      mock.unmocked.push(`${method} ${url.pathname}${url.search}`);
      await reply({ route, status: 404, body: { error: 'unmocked' } });
    },
  );
  return mock;
}

export async function mockDelivery(page: Page, seed: DeliveryMockSeed = {}): Promise<DeliveryMock> {
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
  let heartbeats = [...(seed.heartbeats ?? [])];
  let identityRequests = [...(seed.identityRequests ?? [])];
  let identityBindings = [...(seed.identityBindings ?? [])];
  let identityRegistrars = [...(seed.identityRegistrars ?? [])];
  let personalDestinations = [...(seed.personalDestinations ?? [])];
  let nextRouteId = 100;
  let nextHeartbeatId = 100;

  /** The check view's embedded destinations for a list of names, or null when one is unknown. */
  function embeddedDestinations(
    names: string[],
  ): Array<{ name: string; type: string; enabled: boolean }> | null {
    const embedded: Array<{ name: string; type: string; enabled: boolean }> = [];
    for (const name of names) {
      const found = destinations.find((destination) => destination.name === name);
      if (found === undefined) {
        return null;
      }
      embedded.push({
        name,
        type: found.type as string,
        enabled: found.enabled as boolean,
      });
    }
    return embedded;
  }

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
    // The six relayed provisioning calls belong to `mockProvisioning` (which matches them via
    // PROVISIONING_PATH); defer so they are neither recorded nor counted as unmocked here.
    if (RELAYED_ADMIN_PATH.test(pathname)) {
      await route.fallback();
      return;
    }
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
      if (method === 'DELETE' && destinationMatch[2] === undefined) {
        destinations.splice(destinations.indexOf(target), 1);
        await route.fulfill({ status: 204 });
        return;
      }
      if (method === 'PATCH' && destinationMatch[2] === undefined) {
        const body = recorded.body as Json;
        if (typeof body.enabled === 'boolean') {
          target.enabled = body.enabled;
          target.disabled_reason = body.enabled ? null : 'disabled by an operator';
        }
        // Config edits merge into the stored config; typed secrets stay masked on the wire.
        if (body.config !== undefined) {
          target.config = { ...(target.config as Json), ...(body.config as Json) };
        }
        if (body.secrets !== undefined) {
          target.secrets = {
            ...(target.secrets as Json),
            ...Object.fromEntries(Object.keys(body.secrets as Json).map((key) => [key, '****'])),
          };
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
          ...ownerWire({}),
          created_at: TIMESTAMP,
          updated_at: TIMESTAMP,
        };
        routes.push(created);
        await reply({ route, status: 201, body: { route: created } });
        return;
      }
    }
    const routeMatch = /^\/delivery\/v1\/admin\/routes\/(\d+)$/.exec(pathname);
    if (method === 'PATCH' && routeMatch !== null) {
      const target = routes.find((candidate) => candidate.id === Number(routeMatch[1]));
      if (target === undefined) {
        await reply({ route, status: 404, body: { error: 'route does not exist' } });
        return;
      }
      // An omitted field is left unchanged, as on the real endpoint.
      Object.assign(target, recorded.body as Json, { updated_at: TIMESTAMP });
      await reply({ route, body: { route: target } });
      return;
    }
    if (method === 'DELETE' && routeMatch !== null) {
      const index = routes.findIndex((candidate) => candidate.id === Number(routeMatch[1]));
      if (index === -1) {
        await reply({ route, status: 404, body: { error: 'route does not exist' } });
        return;
      }
      routes.splice(index, 1);
      await route.fulfill({ status: 204 });
      return;
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
    if (method === 'GET' && pathname === '/delivery/v1/admin/heartbeats') {
      await reply({ route, body: { checks: heartbeats } });
      return;
    }
    if (method === 'POST' && pathname === '/delivery/v1/admin/heartbeats') {
      const body = recorded.body as {
        app_id: string;
        name: string;
        period_seconds: number;
        grace_seconds: number;
        destinations: string[];
        probe?: {
          url: string;
          expected_statuses: number[];
          body_match?: string;
          timeout_seconds: number;
          interval_seconds: number;
          access_token?: { client_id: string; client_secret: string };
        };
      };
      if (seed.heartbeatCreateError !== undefined) {
        await reply({
          route,
          status: seed.heartbeatCreateError.status,
          body: { error: seed.heartbeatCreateError.message },
        });
        return;
      }
      if (heartbeats.some((check) => check.app_id === body.app_id && check.name === body.name)) {
        await reply({ route, status: 409, body: { error: 'check already exists' } });
        return;
      }
      // Probe period and grace are derived by the server: sending either is a 400.
      const sentBody = recorded.body as Json;
      if (
        body.probe !== undefined &&
        (sentBody.period_seconds !== undefined || sentBody.grace_seconds !== undefined)
      ) {
        await reply({
          route,
          status: 400,
          body: { error: 'period_seconds and grace_seconds are derived for probe checks' },
        });
        return;
      }
      if (body.probe !== undefined && !body.probe.url.startsWith('https://')) {
        await reply({ route, status: 400, body: { error: 'invalid probe.url' } });
        return;
      }
      const embedded = embeddedDestinations(body.destinations);
      if (embedded === null) {
        await reply({ route, status: 400, body: { error: 'unknown destination' } });
        return;
      }
      // A new check has not been pinged: it waits, with its first deadline ahead.
      const created = heartbeatWire({
        id: nextHeartbeatId,
        appId: body.app_id,
        name: body.name,
        state: 'new',
        lastPingAt: null,
        lastPingKind: null,
        periodSeconds: body.probe === undefined ? body.period_seconds : body.probe.interval_seconds,
        graceSeconds: body.probe === undefined ? body.grace_seconds : PROBE_DERIVED_GRACE_SECONDS,
        destinations: embedded,
        ...(body.probe === undefined
          ? {}
          : {
              probe: {
                url: body.probe.url,
                expectedStatuses: body.probe.expected_statuses,
                bodyMatch: body.probe.body_match ?? null,
                timeoutSeconds: body.probe.timeout_seconds,
                intervalSeconds: body.probe.interval_seconds,
                hasAccessToken: body.probe.access_token !== undefined,
              },
            }),
      });
      nextHeartbeatId += 1;
      // The real list is ordered by app_id, then name.
      heartbeats = [...heartbeats, created].sort((left, right) =>
        `${left.app_id as string}/${left.name as string}`.localeCompare(
          `${right.app_id as string}/${right.name as string}`,
        ),
      );
      await reply({ route, status: 201, body: { check: created } });
      return;
    }
    const heartbeatMatch = /^\/delivery\/v1\/admin\/heartbeats\/(\d+)(?:\/(pause|resume))?$/.exec(
      pathname,
    );
    if (heartbeatMatch !== null) {
      const id = Number(heartbeatMatch[1]);
      const action = heartbeatMatch[2] as string | undefined;
      if (method === 'DELETE' && action === undefined) {
        const found = heartbeats.some((check) => check.id === id);
        heartbeats = heartbeats.filter((check) => check.id !== id);
        await reply({ route, body: { id, deleted: found } });
        return;
      }
      if (method === 'PATCH' && action === undefined) {
        const body = recorded.body as {
          app_id?: string;
          name?: string;
          period_seconds?: number;
          grace_seconds?: number;
          destinations?: string[];
          probe?: Json;
        };
        const existing = heartbeats.find((check) => check.id === id);
        if (existing === undefined) {
          await reply({ route, status: 404, body: { error: 'check not found' } });
          return;
        }
        // Probe settings merge into the stored probe; a push row has none to merge into.
        let mergedProbe: Json | undefined;
        if (body.probe !== undefined) {
          const storedProbe = existing.probe as Json | undefined;
          if (storedProbe === undefined) {
            await reply({
              route,
              status: 400,
              body: { error: 'probe config is not allowed on a push check' },
            });
            return;
          }
          mergedProbe = { ...storedProbe };
          let effectiveChange = false;
          for (const [key, value] of Object.entries(body.probe)) {
            if (key === 'access_token') {
              // Tri-state: absent keeps, null clears, an object replaces.
              mergedProbe.has_access_token = value !== null;
              effectiveChange = true;
              continue;
            }
            if (JSON.stringify(mergedProbe[key]) !== JSON.stringify(value)) {
              effectiveChange = true;
            }
            if (value === null) {
              delete mergedProbe[key];
            } else {
              mergedProbe[key] = value;
            }
          }
          if (effectiveChange) {
            mergedProbe.consecutive_failures = 0;
            mergedProbe.last_result = null;
          }
        }
        // The identity is immutable: the real endpoint rejects any attempt to send it.
        if (body.app_id !== undefined || body.name !== undefined) {
          await reply({ route, status: 400, body: { error: 'app_id and name are immutable' } });
          return;
        }
        const embedded =
          body.destinations === undefined ? undefined : embeddedDestinations(body.destinations);
        if (embedded === null) {
          await reply({ route, status: 400, body: { error: 'unknown destination' } });
          return;
        }
        // An omitted field is left unchanged, as on the real endpoint.
        const updated: Json = {
          ...existing,
          ...(body.period_seconds === undefined ? {} : { period_seconds: body.period_seconds }),
          ...(body.grace_seconds === undefined ? {} : { grace_seconds: body.grace_seconds }),
          ...(embedded === undefined ? {} : { destinations: embedded }),
          ...(mergedProbe === undefined ? {} : { probe: mergedProbe }),
        };
        heartbeats = heartbeats.map((check) => (check.id === id ? updated : check));
        await reply({ route, body: { check: updated } });
        return;
      }
      if (method === 'POST' && action !== undefined) {
        heartbeats = heartbeats.map((check) =>
          check.id === id ? { ...check, paused: action === 'pause' } : check,
        );
        const updated = heartbeats.find((check) => check.id === id);
        if (updated === undefined) {
          await reply({ route, status: 404, body: { error: 'check not found' } });
          return;
        }
        await reply({ route, body: { check: updated } });
        return;
      }
    }
    if (method === 'GET' && pathname === '/delivery/v1/admin/app-identity/requests') {
      await reply({ route, body: { requests: identityRequests } });
      return;
    }
    if (method === 'GET' && pathname === '/delivery/v1/admin/app-identity/registrars') {
      // The real list counts the registrar's bindings; derive it so Unbind and delete stay honest.
      await reply({
        route,
        body: {
          registrars: identityRegistrars.map((registrar) => ({
            ...registrar,
            app_count: identityBindings.filter(
              (binding) => (binding.registrar as { id: number } | null)?.id === registrar.id,
            ).length,
          })),
        },
      });
      return;
    }
    const approveRegistrarMatch =
      /^\/delivery\/v1\/admin\/app-identity\/requests\/(\d+)\/approve-registrar$/.exec(pathname);
    if (method === 'POST' && approveRegistrarMatch !== null) {
      const id = Number(approveRegistrarMatch[1]);
      const found = identityRequests.find((candidate) => candidate.id === id);
      if (found === undefined) {
        await reply({ route, status: 404, body: { error: 'request does not exist' } });
        return;
      }
      if (seed.approveRegistrarError !== undefined) {
        await reply({
          route,
          status: seed.approveRegistrarError.status,
          body: { error: seed.approveRegistrarError.message },
        });
        return;
      }
      const registrar = identityRegistrarWire({
        id: Math.max(0, ...identityRegistrars.map((candidate) => candidate.id as number)) + 1,
        prefix: (recorded.body as { prefix: string }).prefix,
        repository: found.github_repository as string,
      });
      identityRegistrars = [...identityRegistrars, registrar];
      identityRequests = identityRequests.filter((candidate) => candidate.id !== id);
      await reply({ route, status: 201, body: { registrar } });
      return;
    }
    const registrarMatch = /^\/delivery\/v1\/admin\/app-identity\/registrars\/(\d+)$/.exec(
      pathname,
    );
    if (method === 'DELETE' && registrarMatch !== null) {
      const id = Number(registrarMatch[1]);
      const deleted = identityRegistrars.some((candidate) => candidate.id === id);
      const owned = identityBindings.filter(
        (binding) => (binding.registrar as { id: number } | null)?.id === id,
      );
      identityRegistrars = identityRegistrars.filter((candidate) => candidate.id !== id);
      identityBindings = identityBindings.filter((binding) => !owned.includes(binding));
      await reply({ route, body: { id, deleted, unbound_apps: owned.length } });
      return;
    }
    const acknowledgeMatch =
      /^\/delivery\/v1\/admin\/app-identity\/bindings\/([^/]+)\/acknowledge$/.exec(pathname);
    if (method === 'POST' && acknowledgeMatch !== null) {
      const appId = decodeURIComponent(acknowledgeMatch[1]);
      if (seed.acknowledgeError !== undefined) {
        if (seed.acknowledgeError.status === 404) {
          identityBindings = identityBindings.filter((binding) => binding.app_id !== appId);
        }
        await reply({
          route,
          status: seed.acknowledgeError.status,
          body: { error: seed.acknowledgeError.message },
        });
        return;
      }
      const existing = identityBindings.find((binding) => binding.app_id === appId);
      if (existing === undefined) {
        await reply({ route, status: 404, body: { error: 'binding does not exist' } });
        return;
      }
      // Idempotent: the first acknowledge time is kept.
      const acknowledged = {
        ...existing,
        acknowledged_at: existing.acknowledged_at ?? TIMESTAMP,
      };
      identityBindings = identityBindings.map((binding) =>
        binding.app_id === appId ? acknowledged : binding,
      );
      await reply({ route, body: { binding: acknowledged } });
      return;
    }
    if (
      method === 'POST' &&
      pathname === '/delivery/v1/admin/app-identity/bindings/acknowledge-all'
    ) {
      if (seed.acknowledgeAllError !== undefined) {
        await reply({
          route,
          status: seed.acknowledgeAllError.status,
          body: { error: seed.acknowledgeAllError.message },
        });
        return;
      }
      const listed = new Set((recorded.body as { app_ids: string[] }).app_ids);
      let acknowledged = 0;
      identityBindings = identityBindings.map((binding) => {
        // Only still-unseen registrar apps in the list change; anything else is skipped silently.
        if (
          listed.has(binding.app_id as string) &&
          binding.registrar !== null &&
          binding.acknowledged_at === null
        ) {
          acknowledged += 1;
          return { ...binding, acknowledged_at: TIMESTAMP };
        }
        return binding;
      });
      await reply({ route, body: { acknowledged } });
      return;
    }
    if (method === 'GET' && pathname === '/delivery/v1/admin/app-identity/bindings') {
      await reply({ route, body: { bindings: identityBindings } });
      return;
    }
    const identityRequestMatch =
      /^\/delivery\/v1\/admin\/app-identity\/requests\/(\d+)\/(approve|reject)$/.exec(pathname);
    if (method === 'POST' && identityRequestMatch !== null) {
      const id = Number(identityRequestMatch[1]);
      const action = identityRequestMatch[2];
      const found = identityRequests.find((candidate) => candidate.id === id);
      if (found === undefined) {
        await reply({ route, status: 404, body: { error: 'request not found' } });
        return;
      }
      if (seed.approveError !== undefined && action === 'approve') {
        await reply({
          route,
          status: seed.approveError.status,
          body: { error: seed.approveError.message },
        });
        return;
      }
      if (action === 'approve') {
        identityRequests = identityRequests.filter((candidate) => candidate.id !== id);
        const binding = identityBindingWire({ appId: found.app_id as string });
        identityBindings = [...identityBindings, binding];
        await reply({ route, status: 201, body: { binding } });
        return;
      }
      const rejected = { ...found, status: 'rejected' };
      identityRequests = identityRequests.map((candidate) =>
        candidate.id === id ? rejected : candidate,
      );
      await reply({ route, body: { request: rejected } });
      return;
    }
    const identityKeyMatch = /^\/delivery\/v1\/admin\/app-identity\/keys\/(\d+)\/revoke$/.exec(
      pathname,
    );
    if (method === 'POST' && identityKeyMatch !== null) {
      if (seed.revokeError !== undefined) {
        await reply({
          route,
          status: seed.revokeError.status,
          body: { error: seed.revokeError.message },
        });
        return;
      }
      const id = Number(identityKeyMatch[1]);
      const existing = identityBindings
        .flatMap((binding) => binding.keys as Json[])
        .find((identityKey) => identityKey.id === id);
      if (existing === undefined) {
        await reply({ route, status: 404, body: { error: 'key not found' } });
        return;
      }
      const revoked = {
        ...existing,
        state: 'revoked',
        revoked_at: TIMESTAMP,
        revoked_reason: 'operator',
      };
      identityBindings = identityBindings.map((binding) => ({
        ...binding,
        keys: (binding.keys as Json[]).map((identityKey) =>
          identityKey.id === id ? revoked : identityKey,
        ),
      }));
      await reply({ route, body: { key: revoked } });
      return;
    }
    const identityBindingMatch = /^\/delivery\/v1\/admin\/app-identity\/bindings\/([^/]+)$/.exec(
      pathname,
    );
    if (method === 'DELETE' && identityBindingMatch !== null) {
      const appId = decodeURIComponent(identityBindingMatch[1]);
      const deleted = identityBindings.some((binding) => binding.app_id === appId);
      identityBindings = identityBindings.filter((binding) => binding.app_id !== appId);
      await reply({ route, body: { app_id: appId, deleted } });
      return;
    }
    if (method === 'GET' && pathname === '/delivery/v1/admin/personal-destinations') {
      const personHash = url.searchParams.get('person_hash');
      const served =
        personHash === null
          ? personalDestinations
          : personalDestinations.filter((row) => row.person_hash === personHash);
      await reply({
        route,
        body: {
          personal_destinations: served,
          total: seed.personalDestinationsTotal ?? served.length,
        },
      });
      return;
    }
    const personalMatch = /^\/delivery\/v1\/admin\/personal-destinations\/(\d+)$/.exec(pathname);
    if (personalMatch !== null && (method === 'PATCH' || method === 'DELETE')) {
      const id = Number(personalMatch[1]);
      const existing = personalDestinations.find((row) => row.id === id);
      if (existing === undefined) {
        await reply({
          route,
          status: 404,
          body: { error: 'personal destination does not exist' },
        });
        return;
      }
      if (method === 'DELETE') {
        personalDestinations = personalDestinations.filter((row) => row.id !== id);
        await reply({ route, body: { id, deleted: true } });
        return;
      }
      const body = recorded.body as { enabled: boolean; disabled_reason?: string };
      const updated: Json = {
        ...existing,
        enabled: body.enabled,
        disabled_by: body.enabled ? null : 'operator',
        disabled_reason: body.enabled ? null : (body.disabled_reason ?? null),
      };
      personalDestinations = personalDestinations.map((row) => (row.id === id ? updated : row));
      await reply({ route, body: { personal_destination: updated } });
      return;
    }
    if (method === 'POST' && pathname === '/delivery/v1/admin/messages') {
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

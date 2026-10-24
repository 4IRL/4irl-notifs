// Typed client for the delivery-api operator tier, reached same-origin through
// the `/delivery/*` Pages Function. The wire shape is snake_case JSON; this
// module maps it to camelCase TypeScript at the boundary, mirroring
// personClient.ts.

import { strings } from '../strings';
import { ApiError } from './client';

export type DestinationType = 'ntfy' | 'discord' | 'webhook';

export type DeliveryState = 'pending' | 'delivered' | 'dead';

/** Who owns a registry object. `unknown` is the fail-closed reading of a missing,
 *  unrecognised or inconsistent wire owner; it is never shown as `operator`. */
export type OwnerKind = 'operator' | 'app' | 'unknown';

/** The owner fields every destination, route and check view carries. */
export interface OwnerFields {
  ownerKind: OwnerKind;
  /** The owning app's id; non-null only when `ownerKind` is `app`. */
  ownerAppId: string | null;
}

/** A delivery destination as listed by the registry. Secret values are masked
 *  by the server and are display-only. */
export interface DestinationSummary extends OwnerFields {
  name: string;
  type: DestinationType;
  enabled: boolean;
  disabledReason: string | null;
  config: Record<string, string>;
  secrets: Record<string, string>;
  createdAt: string;
  updatedAt: string;
}

/** A routing rule mapping an app/channel pattern to destinations. */
export interface RouteSummary extends OwnerFields {
  id: number;
  appPattern: string;
  channelPattern: string;
  destinations: string[];
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

/** An operator-view delivery (admin list and replay). */
export interface DeliverySummary {
  id: number;
  messageId: string;
  appId: string;
  channel: string;
  destination: string;
  destinationType: DestinationType;
  state: DeliveryState;
  deadReason: string | null;
  attemptCount: number;
  lastStatusCode: number | null;
  nextAttemptAt: string | null;
  deliveredAt: string | null;
  createdAt: string;
  updatedAt: string;
}

/** One delivery attempt. */
export interface AttemptSummary {
  attemptNumber: number;
  outcome: string;
  statusCode: number | null;
  latencyMs: number;
  error: string | null;
  responseSnippet: string | null;
  startedAt: string;
}

/** A delivery as embedded in an app-facing message response. */
export interface SubmittedDelivery {
  id: number;
  destination: string;
  state: string;
  deadReason: string | null;
  attemptCount: number;
  lastStatusCode: number | null;
  nextAttemptAt: string | null;
  deliveredAt: string | null;
}

/** The app-facing message response used by send-test. */
export interface MessageSummary {
  messageId: string;
  status: string;
  deliveries: SubmittedDelivery[];
}

export interface MigrationFailure {
  version: number;
  error: string;
  failedAt: string;
}

/** Freshness of the heartbeat checker loop as reported by GET /readyz. */
export interface HeartbeatCheckerStatus {
  lastTickAt: string | null;
  stale: boolean;
}

/** Outcome of GET /readyz. getReadiness never throws: every failure mode is a
 *  member of this union. */
export type ReadinessResult =
  | {
      kind: 'ok' | 'schema_mismatch';
      schemaVersion: number;
      expectedSchemaVersion: number;
      lastMigrationFailure: MigrationFailure | null;
      /** An absent or malformed wire field reads as stale, never as an error. */
      heartbeatChecker: HeartbeatCheckerStatus;
    }
  | { kind: 'unavailable' }
  | { kind: 'unreachable'; message: string };

export interface CreateDestinationParams {
  name: string;
  type: DestinationType;
  enabled?: boolean;
  config: Record<string, string>;
  secrets: Record<string, string>;
}

/** An omitted field is left unchanged server-side; name and type are never sent. */
export interface UpdateDestinationParams {
  name: string;
  enabled?: boolean;
  config?: Record<string, string>;
  secrets?: Record<string, string>;
}

export interface DestinationNameParam {
  name: string;
}

export interface RotateSecretParams {
  name: string;
  overlapHours: number;
}

export interface RotateSecretResult {
  destination: DestinationSummary;
  signingSecret: string;
}

export interface SendTestParams {
  destination: string;
}

export interface CreateRouteParams {
  appPattern: string;
  channelPattern: string;
  destinations: string[];
  enabled?: boolean;
}

export interface UpdateRouteParams {
  id: number;
  appPattern?: string;
  channelPattern?: string;
  destinations?: string[];
  enabled?: boolean;
}

export interface RouteIdParam {
  id: number;
}

export interface ListDeliveriesParams {
  state: DeliveryState;
  limit?: number;
}

export interface DeliveryIdParam {
  deliveryId: number;
}

/** A scheduled (not yet released) message in the operator Pending sends list. */
export interface ScheduledMessageSummary {
  messageId: string;
  appId: string;
  channel: string;
  idempotencyKey: string;
  deliverAt: string;
  createdAt: string;
  /** True when the message targets `to.recipient`; `destinations` is then empty. */
  recipient: boolean;
  destinations: { name: string; type: DestinationType }[];
}

export interface ListScheduledMessagesParams {
  limit?: number;
}

export interface ScheduledMessagesList {
  items: ScheduledMessageSummary[];
  /** Every scheduled message, so the UI can say the list is truncated. */
  total: number;
}

export interface CancelScheduledMessageParams {
  id: string;
}

export interface CancelScheduledMessageResult {
  messageId: string;
  cancelled: boolean;
  /** The message status, or `not_found`. */
  status: string;
}

/** The derived state of a heartbeat check; `late` is computed server-side and never alerted on. */
export type HeartbeatState = 'new' | 'up' | 'late' | 'down';

export type HeartbeatPingKind = 'success' | 'fail';

/** An alert destination of a check, as embedded in the check view. */
export interface HeartbeatDestination {
  name: string;
  type: DestinationType;
  enabled: boolean;
}

/** A heartbeat check as listed by the operator API. */
export interface HeartbeatCheck extends OwnerFields {
  id: number;
  appId: string;
  name: string;
  periodSeconds: number;
  graceSeconds: number;
  paused: boolean;
  state: HeartbeatState;
  lastPingAt: string | null;
  lastPingKind: HeartbeatPingKind | null;
  /** Only populated while the check is down. */
  downSince: string | null;
  /** anchor + period + grace; rendered even for paused and down checks. */
  deadlineAt: string;
  destinations: HeartbeatDestination[];
  createdAt: string;
  updatedAt: string;
}

export interface CreateHeartbeatParams {
  appId: string;
  name: string;
  periodSeconds: number;
  graceSeconds: number;
  destinations: string[];
}

/** An omitted field is left unchanged server-side; app_id and name are immutable and never sent. */
export interface UpdateHeartbeatParams {
  id: number;
  periodSeconds?: number;
  graceSeconds?: number;
  destinations?: string[];
}

export interface HeartbeatIdParam {
  id: number;
}

/**
 * Status of an identity request. `unknown` is anything this build does not
 * recognise (fail closed: it renders as an explicit unknown state, offers no
 * Approve, and is never read as pending).
 */
export type IdentityRequestStatus = 'pending' | 'rejected' | 'unknown';

/** An app's request (filed by its first deploy) for a delivery identity. */
export interface IdentityRequest {
  id: number;
  appId: string;
  githubRepository: string;
  githubRepositoryId: number;
  workflowPath: string;
  gitRef: string;
  environment: string | null;
  githubRunId: string;
  status: IdentityRequestStatus;
  createdAt: string;
  updatedAt: string;
}

/** `unknown` is anything this build does not recognise (fail closed: shown, never read as active). */
export type IdentityKeyState = 'active' | 'superseded' | 'revoked' | 'unknown';

/** A delivery key as the operator sees it: only the hint, never the key itself. */
export interface IdentityKey {
  id: number;
  appId: string;
  keyHint: string;
  state: IdentityKeyState;
  createdAt: string;
  supersededAt: string | null;
  revokedAt: string | null;
  revokedReason: string | null;
  lastUsedAt: string | null;
}

/** An approved app identity: the GitHub workflow bound to an app id, with its keys. */
export interface IdentityBinding {
  appId: string;
  githubRepository: string;
  githubRepositoryId: number;
  githubOwnerId: number;
  workflowPath: string;
  gitRef: string;
  environment: string | null;
  approvedAt: string;
  keys: IdentityKey[];
}

export interface IdentityRequestIdParam {
  id: number;
}

export interface IdentityKeyIdParam {
  id: number;
}

export interface UnbindAppParam {
  appId: string;
}

export interface UnbindAppResult {
  appId: string;
  deleted: boolean;
}

/** Configuration for createDeliveryApiClient. */
export interface DeliveryApiClientConfig {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
}

/** The delivery-api surface consumed by the admin UI. */
export interface DeliveryApiClient {
  getReadiness(): Promise<ReadinessResult>;
  listDestinations(): Promise<DestinationSummary[]>;
  createDestination(params: CreateDestinationParams): Promise<DestinationSummary>;
  updateDestination(params: UpdateDestinationParams): Promise<DestinationSummary>;
  deleteDestination(param: DestinationNameParam): Promise<void>;
  rotateSecret(params: RotateSecretParams): Promise<RotateSecretResult>;
  sendTest(params: SendTestParams): Promise<MessageSummary>;
  listRoutes(): Promise<RouteSummary[]>;
  createRoute(params: CreateRouteParams): Promise<RouteSummary>;
  updateRoute(params: UpdateRouteParams): Promise<RouteSummary>;
  deleteRoute(param: RouteIdParam): Promise<void>;
  listDeliveries(params: ListDeliveriesParams): Promise<DeliverySummary[]>;
  listAttempts(param: DeliveryIdParam): Promise<AttemptSummary[]>;
  replayDelivery(param: DeliveryIdParam): Promise<DeliverySummary>;
  listScheduledMessages(params: ListScheduledMessagesParams): Promise<ScheduledMessagesList>;
  cancelScheduledMessage(
    params: CancelScheduledMessageParams,
  ): Promise<CancelScheduledMessageResult>;
  listHeartbeats(): Promise<HeartbeatCheck[]>;
  createHeartbeat(params: CreateHeartbeatParams): Promise<HeartbeatCheck>;
  updateHeartbeat(params: UpdateHeartbeatParams): Promise<HeartbeatCheck>;
  deleteHeartbeat(param: HeartbeatIdParam): Promise<void>;
  pauseHeartbeat(param: HeartbeatIdParam): Promise<HeartbeatCheck>;
  resumeHeartbeat(param: HeartbeatIdParam): Promise<HeartbeatCheck>;
  listIdentityRequests(): Promise<IdentityRequest[]>;
  approveIdentityRequest(param: IdentityRequestIdParam): Promise<IdentityBinding>;
  rejectIdentityRequest(param: IdentityRequestIdParam): Promise<IdentityRequest>;
  listIdentityBindings(): Promise<IdentityBinding[]>;
  revokeIdentityKey(param: IdentityKeyIdParam): Promise<IdentityKey>;
  unbindApp(param: UnbindAppParam): Promise<UnbindAppResult>;
}

/** Owner fields as sent on the wire; optional because an older server omits them. */
interface OwnerWire {
  owner_kind?: string | null;
  owner_app_id?: string | null;
}

interface DestinationWire extends OwnerWire {
  name: string;
  type: DestinationType;
  enabled: boolean;
  disabled_reason: string | null;
  config: Record<string, string> | null;
  secrets: Record<string, string> | null;
  created_at: string;
  updated_at: string;
}

interface RouteWire extends OwnerWire {
  id: number;
  app_pattern: string;
  channel_pattern: string;
  destinations: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

interface DeliveryWire {
  id: number;
  message_id: string;
  app_id: string;
  channel: string;
  destination: string;
  destination_type: DestinationType;
  state: DeliveryState;
  dead_reason: string | null;
  attempt_count: number;
  last_status_code: number | null;
  next_attempt_at: string | null;
  delivered_at: string | null;
  created_at: string;
  updated_at: string;
}

interface ScheduledMessageWire {
  message_id: string;
  app_id: string;
  channel: string;
  idempotency_key: string;
  deliver_at: string;
  created_at: string;
  recipient: boolean;
  destinations: { name: string; type: DestinationType }[] | null;
}

interface CancelWire {
  message_id: string;
  cancelled: boolean;
  status: string;
}

interface AttemptWire {
  attempt_number: number;
  outcome: string;
  status_code: number | null;
  latency_ms: number;
  error: string | null;
  response_snippet: string | null;
  started_at: string;
}

interface SubmittedDeliveryWire {
  id: number;
  destination: string;
  state: string;
  dead_reason: string | null;
  attempt_count: number;
  last_status_code: number | null;
  next_attempt_at: string | null;
  delivered_at: string | null;
}

interface MessageWire {
  message_id: string;
  status: string;
  deliveries: SubmittedDeliveryWire[] | null;
}

interface HeartbeatWire extends OwnerWire {
  id: number;
  app_id: string;
  name: string;
  period_seconds: number;
  grace_seconds: number;
  paused: boolean;
  state: HeartbeatState;
  last_ping_at: string | null;
  last_ping_kind: HeartbeatPingKind | null;
  down_since: string | null;
  deadline_at: string;
  destinations: HeartbeatDestination[] | null;
  created_at: string;
  updated_at: string;
}

interface IdentityRequestWire {
  id: number;
  app_id: string;
  github_repository: string;
  github_repository_id: number;
  workflow_path: string;
  git_ref: string;
  environment: string | null;
  github_run_id: string;
  status: string;
  created_at: string;
  updated_at: string;
}

interface IdentityKeyWire {
  id: number;
  app_id: string;
  key_hint: string;
  state: string;
  created_at: string;
  superseded_at: string | null;
  revoked_at: string | null;
  revoked_reason: string | null;
  last_used_at: string | null;
}

interface IdentityBindingWire {
  app_id: string;
  github_repository: string;
  github_repository_id: number;
  github_owner_id: number;
  workflow_path: string;
  git_ref: string;
  environment: string | null;
  approved_at: string;
  keys: IdentityKeyWire[] | null;
}

interface ReadinessWire {
  status?: string;
  schema_version?: number;
  expected_schema_version?: number;
  last_migration_failure?: { version: number; error: string; failed_at: string } | null;
  heartbeat_checker?: { last_tick_at?: unknown; stale?: unknown } | null;
}

/** Fail closed: only a well-formed pair reads as operator or app; everything else
 *  (absent, unrecognised kind, app without an id, operator with an id) is `unknown`. */
function mapOwner(wire: OwnerWire): OwnerFields {
  const appId = typeof wire.owner_app_id === 'string' ? wire.owner_app_id : null;
  if (wire.owner_kind === 'operator' && wire.owner_app_id == null) {
    return { ownerKind: 'operator', ownerAppId: null };
  }
  if (wire.owner_kind === 'app' && appId !== null && appId !== '') {
    return { ownerKind: 'app', ownerAppId: appId };
  }
  return { ownerKind: 'unknown', ownerAppId: null };
}

function mapHeartbeat(wire: HeartbeatWire): HeartbeatCheck {
  return {
    ...mapOwner(wire),
    id: wire.id,
    appId: wire.app_id,
    name: wire.name,
    periodSeconds: wire.period_seconds,
    graceSeconds: wire.grace_seconds,
    paused: wire.paused,
    state: wire.state,
    lastPingAt: wire.last_ping_at,
    lastPingKind: wire.last_ping_kind,
    downSince: wire.down_since,
    deadlineAt: wire.deadline_at,
    destinations: (wire.destinations ?? []).map(({ name, type, enabled }) => ({
      name,
      type,
      enabled,
    })),
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
  };
}

/** Fail closed: only the two statuses this build knows read as such; anything else is `unknown`. */
function mapIdentityRequestStatus(status: string): IdentityRequestStatus {
  return status === 'pending' || status === 'rejected' ? status : 'unknown';
}

/** Fail closed: only the three states this build knows read as such; anything else is `unknown`. */
function mapIdentityKeyState(state: string): IdentityKeyState {
  return state === 'active' || state === 'superseded' || state === 'revoked' ? state : 'unknown';
}

function mapIdentityRequest(wire: IdentityRequestWire): IdentityRequest {
  return {
    id: wire.id,
    appId: wire.app_id,
    githubRepository: wire.github_repository,
    githubRepositoryId: wire.github_repository_id,
    workflowPath: wire.workflow_path,
    gitRef: wire.git_ref,
    environment: wire.environment ?? null,
    githubRunId: wire.github_run_id,
    status: mapIdentityRequestStatus(wire.status),
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
  };
}

/** Copies only the display fields, so a key value can never ride along into the UI. */
function mapIdentityKey(wire: IdentityKeyWire): IdentityKey {
  return {
    id: wire.id,
    appId: wire.app_id,
    keyHint: wire.key_hint,
    state: mapIdentityKeyState(wire.state),
    createdAt: wire.created_at,
    supersededAt: wire.superseded_at ?? null,
    revokedAt: wire.revoked_at ?? null,
    revokedReason: wire.revoked_reason ?? null,
    lastUsedAt: wire.last_used_at ?? null,
  };
}

function mapIdentityBinding(wire: IdentityBindingWire): IdentityBinding {
  return {
    appId: wire.app_id,
    githubRepository: wire.github_repository,
    githubRepositoryId: wire.github_repository_id,
    githubOwnerId: wire.github_owner_id,
    workflowPath: wire.workflow_path,
    gitRef: wire.git_ref,
    environment: wire.environment ?? null,
    approvedAt: wire.approved_at,
    keys: (wire.keys ?? []).map(mapIdentityKey),
  };
}

/** Fail closed: anything but a well-formed `stale: false` with a last tick reads as stale. */
function mapHeartbeatChecker(wire: ReadinessWire['heartbeat_checker']): HeartbeatCheckerStatus {
  const lastTickAt = typeof wire?.last_tick_at === 'string' ? wire.last_tick_at : null;
  return { lastTickAt, stale: wire?.stale !== false || lastTickAt === null };
}

function mapDestination(wire: DestinationWire): DestinationSummary {
  return {
    ...mapOwner(wire),
    name: wire.name,
    type: wire.type,
    enabled: wire.enabled,
    disabledReason: wire.disabled_reason,
    config: wire.config ?? {},
    secrets: wire.secrets ?? {},
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
  };
}

function mapRoute(wire: RouteWire): RouteSummary {
  return {
    ...mapOwner(wire),
    id: wire.id,
    appPattern: wire.app_pattern,
    channelPattern: wire.channel_pattern,
    destinations: wire.destinations,
    enabled: wire.enabled,
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
  };
}

function mapDelivery(wire: DeliveryWire): DeliverySummary {
  return {
    id: wire.id,
    messageId: wire.message_id,
    appId: wire.app_id,
    channel: wire.channel,
    destination: wire.destination,
    destinationType: wire.destination_type,
    state: wire.state,
    deadReason: wire.dead_reason,
    attemptCount: wire.attempt_count,
    lastStatusCode: wire.last_status_code,
    nextAttemptAt: wire.next_attempt_at,
    deliveredAt: wire.delivered_at,
    createdAt: wire.created_at,
    updatedAt: wire.updated_at,
  };
}

function mapScheduledMessage(wire: ScheduledMessageWire): ScheduledMessageSummary {
  return {
    messageId: wire.message_id,
    appId: wire.app_id,
    channel: wire.channel,
    idempotencyKey: wire.idempotency_key,
    deliverAt: wire.deliver_at,
    createdAt: wire.created_at,
    recipient: wire.recipient,
    destinations: (wire.destinations ?? []).map(({ name, type }) => ({ name, type })),
  };
}

function mapAttempt(wire: AttemptWire): AttemptSummary {
  return {
    attemptNumber: wire.attempt_number,
    outcome: wire.outcome,
    statusCode: wire.status_code,
    latencyMs: wire.latency_ms,
    error: wire.error,
    responseSnippet: wire.response_snippet,
    startedAt: wire.started_at,
  };
}

function mapMessage(wire: MessageWire): MessageSummary {
  return {
    messageId: wire.message_id,
    status: wire.status,
    deliveries: (wire.deliveries ?? []).map((delivery) => ({
      id: delivery.id,
      destination: delivery.destination,
      state: delivery.state,
      deadReason: delivery.dead_reason,
      attemptCount: delivery.attempt_count,
      lastStatusCode: delivery.last_status_code,
      nextAttemptAt: delivery.next_attempt_at,
      deliveredAt: delivery.delivered_at,
    })),
  };
}

const DEFAULT_BASE_URL = '';
const DELIVERY_PREFIX = '/delivery';
const DEFAULT_DELIVERIES_LIMIT = 50;
const DEFAULT_SCHEDULED_LIMIT = 50;
const SEND_TEST_WAIT_SECONDS = 10;
const SEND_TEST_APP_ID = 'admin_test';
const SEND_TEST_CHANNEL = 'test';
const REQUESTED_WITH_VALUE = 'admin-ui';

/** Builds a DeliveryApiClient bound to a base URL and fetch implementation. */
export function createDeliveryApiClient({
  baseUrl = DEFAULT_BASE_URL,
  fetchImpl = fetch,
}: DeliveryApiClientConfig = {}): DeliveryApiClient {
  const trimmedBaseUrl = baseUrl.replace(/\/+$/, '');

  function buildInit({ method, body }: { method: string; body?: unknown }): RequestInit {
    const headers: Record<string, string> = {};
    // Every non-GET carries the custom header the Function's cross-site guard
    // accepts independent of browser-provided Sec-Fetch-Site.
    if (method !== 'GET') {
      headers['X-Requested-With'] = REQUESTED_WITH_VALUE;
    }
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    return {
      method,
      headers: Object.keys(headers).length === 0 ? undefined : headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'include',
    };
  }

  async function request({
    path,
    method,
    body,
  }: {
    path: string;
    method: string;
    body?: unknown;
  }): Promise<unknown> {
    const response = await fetchImpl(
      `${trimmedBaseUrl}${DELIVERY_PREFIX}${path}`,
      buildInit({ method, body }),
    );
    const payload = (await response.json().catch(() => null)) as { error?: string } | null;
    if (!response.ok) {
      const message =
        payload && typeof payload.error === 'string'
          ? payload.error
          : `request failed (${response.status})`;
      throw new ApiError({ status: response.status, message });
    }
    return payload;
  }

  function readinessFromBody({
    kind,
    body,
  }: {
    kind: 'ok' | 'schema_mismatch';
    body: ReadinessWire;
  }): ReadinessResult {
    const failure = body.last_migration_failure;
    return {
      kind,
      schemaVersion: body.schema_version ?? 0,
      expectedSchemaVersion: body.expected_schema_version ?? 0,
      lastMigrationFailure: failure
        ? { version: failure.version, error: failure.error, failedAt: failure.failed_at }
        : null,
      heartbeatChecker: mapHeartbeatChecker(body.heartbeat_checker),
    };
  }

  async function getReadiness(): Promise<ReadinessResult> {
    let response: Response;
    try {
      response = await fetchImpl(
        `${trimmedBaseUrl}${DELIVERY_PREFIX}/readyz`,
        buildInit({ method: 'GET' }),
      );
    } catch (error) {
      return {
        kind: 'unreachable',
        message: error instanceof Error ? error.message : 'request failed',
      };
    }
    const body = (await response.json().catch(() => null)) as
      (ReadinessWire & { error?: string }) | null;
    if (response.status === 200 && body?.status === 'ok') {
      return readinessFromBody({ kind: 'ok', body });
    }
    if (response.status === 503 && body?.status === 'schema_mismatch') {
      return readinessFromBody({ kind: 'schema_mismatch', body });
    }
    if (response.status === 503 && body?.status === 'unavailable') {
      return { kind: 'unavailable' };
    }
    return {
      kind: 'unreachable',
      message:
        body && typeof body.error === 'string' ? body.error : `request failed (${response.status})`,
    };
  }

  return {
    getReadiness,

    async listDestinations(): Promise<DestinationSummary[]> {
      const wire = (await request({ path: '/v1/admin/destinations', method: 'GET' })) as {
        destinations: DestinationWire[] | null;
      };
      return (wire.destinations ?? []).map(mapDestination);
    },

    async createDestination({
      name,
      type,
      enabled,
      config,
      secrets,
    }: CreateDestinationParams): Promise<DestinationSummary> {
      const body: Record<string, unknown> = { name, type, config, secrets };
      if (enabled !== undefined) {
        body.enabled = enabled;
      }
      const wire = (await request({
        path: '/v1/admin/destinations',
        method: 'POST',
        body,
      })) as { destination: DestinationWire };
      return mapDestination(wire.destination);
    },

    async updateDestination({
      name,
      enabled,
      config,
      secrets,
    }: UpdateDestinationParams): Promise<DestinationSummary> {
      const body: Record<string, unknown> = {};
      if (enabled !== undefined) {
        body.enabled = enabled;
      }
      if (config !== undefined) {
        body.config = config;
      }
      if (secrets !== undefined) {
        body.secrets = secrets;
      }
      const wire = (await request({
        path: `/v1/admin/destinations/${encodeURIComponent(name)}`,
        method: 'PATCH',
        body,
      })) as { destination: DestinationWire };
      return mapDestination(wire.destination);
    },

    async deleteDestination({ name }: DestinationNameParam): Promise<void> {
      await request({
        path: `/v1/admin/destinations/${encodeURIComponent(name)}`,
        method: 'DELETE',
      });
    },

    async rotateSecret({ name, overlapHours }: RotateSecretParams): Promise<RotateSecretResult> {
      const wire = (await request({
        path: `/v1/admin/destinations/${encodeURIComponent(name)}/rotate-secret`,
        method: 'POST',
        body: { overlap_hours: overlapHours },
      })) as { destination: DestinationWire; signing_secret: string };
      return {
        destination: mapDestination(wire.destination),
        signingSecret: wire.signing_secret,
      };
    },

    async sendTest({ destination }: SendTestParams): Promise<MessageSummary> {
      const wire = (await request({
        path: `/v1/admin/messages?wait=${SEND_TEST_WAIT_SECONDS}`,
        method: 'POST',
        body: {
          app_id: SEND_TEST_APP_ID,
          to: { destination },
          channel: SEND_TEST_CHANNEL,
          idempotency_key: crypto.randomUUID(),
          payload: { source: REQUESTED_WITH_VALUE },
          display: {
            title: strings.adminTestDeliveryTitle,
            body: strings.adminTestDeliveryBody({ destination }),
          },
        },
      })) as { message: MessageWire };
      return mapMessage(wire.message);
    },

    async listRoutes(): Promise<RouteSummary[]> {
      const wire = (await request({ path: '/v1/admin/routes', method: 'GET' })) as {
        routes: RouteWire[] | null;
      };
      return (wire.routes ?? []).map(mapRoute);
    },

    async createRoute({
      appPattern,
      channelPattern,
      destinations,
      enabled,
    }: CreateRouteParams): Promise<RouteSummary> {
      const body: Record<string, unknown> = {
        app_pattern: appPattern,
        channel_pattern: channelPattern,
        destinations,
      };
      if (enabled !== undefined) {
        body.enabled = enabled;
      }
      const wire = (await request({ path: '/v1/admin/routes', method: 'POST', body })) as {
        route: RouteWire;
      };
      return mapRoute(wire.route);
    },

    async updateRoute({
      id,
      appPattern,
      channelPattern,
      destinations,
      enabled,
    }: UpdateRouteParams): Promise<RouteSummary> {
      const body: Record<string, unknown> = {};
      if (appPattern !== undefined) {
        body.app_pattern = appPattern;
      }
      if (channelPattern !== undefined) {
        body.channel_pattern = channelPattern;
      }
      if (destinations !== undefined) {
        body.destinations = destinations;
      }
      if (enabled !== undefined) {
        body.enabled = enabled;
      }
      const wire = (await request({
        path: `/v1/admin/routes/${id}`,
        method: 'PATCH',
        body,
      })) as { route: RouteWire };
      return mapRoute(wire.route);
    },

    async deleteRoute({ id }: RouteIdParam): Promise<void> {
      await request({ path: `/v1/admin/routes/${id}`, method: 'DELETE' });
    },

    async listDeliveries({
      state,
      limit = DEFAULT_DELIVERIES_LIMIT,
    }: ListDeliveriesParams): Promise<DeliverySummary[]> {
      const wire = (await request({
        path: `/v1/admin/deliveries?state=${encodeURIComponent(state)}&limit=${limit}`,
        method: 'GET',
      })) as { deliveries: DeliveryWire[] | null };
      return (wire.deliveries ?? []).map(mapDelivery);
    },

    async listAttempts({ deliveryId }: DeliveryIdParam): Promise<AttemptSummary[]> {
      const wire = (await request({
        path: `/v1/admin/deliveries/${deliveryId}/attempts`,
        method: 'GET',
      })) as { attempts: AttemptWire[] | null };
      return (wire.attempts ?? []).map(mapAttempt);
    },

    async replayDelivery({ deliveryId }: DeliveryIdParam): Promise<DeliverySummary> {
      const wire = (await request({
        path: `/v1/admin/deliveries/${deliveryId}/replay`,
        method: 'POST',
      })) as { delivery: DeliveryWire };
      return mapDelivery(wire.delivery);
    },

    async listScheduledMessages({
      limit = DEFAULT_SCHEDULED_LIMIT,
    }: ListScheduledMessagesParams): Promise<ScheduledMessagesList> {
      const wire = (await request({
        path: `/v1/admin/scheduled-messages?limit=${limit}`,
        method: 'GET',
      })) as { scheduled_messages: ScheduledMessageWire[] | null; total: number };
      return {
        items: (wire.scheduled_messages ?? []).map(mapScheduledMessage),
        total: wire.total,
      };
    },

    async cancelScheduledMessage({
      id,
    }: CancelScheduledMessageParams): Promise<CancelScheduledMessageResult> {
      const wire = (await request({
        path: `/v1/admin/scheduled-messages/${encodeURIComponent(id)}`,
        method: 'DELETE',
      })) as CancelWire;
      return { messageId: wire.message_id, cancelled: wire.cancelled, status: wire.status };
    },

    async listHeartbeats(): Promise<HeartbeatCheck[]> {
      const wire = (await request({ path: '/v1/admin/heartbeats', method: 'GET' })) as {
        checks: HeartbeatWire[] | null;
      };
      return (wire.checks ?? []).map(mapHeartbeat);
    },

    async createHeartbeat({
      appId,
      name,
      periodSeconds,
      graceSeconds,
      destinations,
    }: CreateHeartbeatParams): Promise<HeartbeatCheck> {
      const wire = (await request({
        path: '/v1/admin/heartbeats',
        method: 'POST',
        body: {
          app_id: appId,
          name,
          period_seconds: periodSeconds,
          grace_seconds: graceSeconds,
          destinations,
        },
      })) as { check: HeartbeatWire };
      return mapHeartbeat(wire.check);
    },

    async updateHeartbeat({
      id,
      periodSeconds,
      graceSeconds,
      destinations,
    }: UpdateHeartbeatParams): Promise<HeartbeatCheck> {
      const body: Record<string, unknown> = {};
      if (periodSeconds !== undefined) {
        body.period_seconds = periodSeconds;
      }
      if (graceSeconds !== undefined) {
        body.grace_seconds = graceSeconds;
      }
      if (destinations !== undefined) {
        body.destinations = destinations;
      }
      const wire = (await request({
        path: `/v1/admin/heartbeats/${id}`,
        method: 'PATCH',
        body,
      })) as { check: HeartbeatWire };
      return mapHeartbeat(wire.check);
    },

    async deleteHeartbeat({ id }: HeartbeatIdParam): Promise<void> {
      await request({ path: `/v1/admin/heartbeats/${id}`, method: 'DELETE' });
    },

    async pauseHeartbeat({ id }: HeartbeatIdParam): Promise<HeartbeatCheck> {
      const wire = (await request({
        path: `/v1/admin/heartbeats/${id}/pause`,
        method: 'POST',
      })) as { check: HeartbeatWire };
      return mapHeartbeat(wire.check);
    },

    async resumeHeartbeat({ id }: HeartbeatIdParam): Promise<HeartbeatCheck> {
      const wire = (await request({
        path: `/v1/admin/heartbeats/${id}/resume`,
        method: 'POST',
      })) as { check: HeartbeatWire };
      return mapHeartbeat(wire.check);
    },

    async listIdentityRequests(): Promise<IdentityRequest[]> {
      const wire = (await request({
        path: '/v1/admin/app-identity/requests',
        method: 'GET',
      })) as { requests: IdentityRequestWire[] | null };
      return (wire.requests ?? []).map(mapIdentityRequest);
    },

    async approveIdentityRequest({ id }: IdentityRequestIdParam): Promise<IdentityBinding> {
      const wire = (await request({
        path: `/v1/admin/app-identity/requests/${id}/approve`,
        method: 'POST',
      })) as { binding: IdentityBindingWire };
      return mapIdentityBinding(wire.binding);
    },

    async rejectIdentityRequest({ id }: IdentityRequestIdParam): Promise<IdentityRequest> {
      const wire = (await request({
        path: `/v1/admin/app-identity/requests/${id}/reject`,
        method: 'POST',
      })) as { request: IdentityRequestWire };
      return mapIdentityRequest(wire.request);
    },

    async listIdentityBindings(): Promise<IdentityBinding[]> {
      const wire = (await request({
        path: '/v1/admin/app-identity/bindings',
        method: 'GET',
      })) as { bindings: IdentityBindingWire[] | null };
      return (wire.bindings ?? []).map(mapIdentityBinding);
    },

    async revokeIdentityKey({ id }: IdentityKeyIdParam): Promise<IdentityKey> {
      const wire = (await request({
        path: `/v1/admin/app-identity/keys/${id}/revoke`,
        method: 'POST',
      })) as { key: IdentityKeyWire };
      return mapIdentityKey(wire.key);
    },

    async unbindApp({ appId }: UnbindAppParam): Promise<UnbindAppResult> {
      const wire = (await request({
        path: `/v1/admin/app-identity/bindings/${encodeURIComponent(appId)}`,
        method: 'DELETE',
      })) as { app_id: string; deleted: boolean };
      return { appId: wire.app_id, deleted: wire.deleted };
    },
  };
}

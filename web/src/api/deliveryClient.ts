// Typed client for the delivery-api operator tier, reached same-origin through
// the `/delivery/*` Pages Function. The wire shape is snake_case JSON; this
// module maps it to camelCase TypeScript at the boundary, mirroring
// personClient.ts.

import { strings } from '../strings';
import { ApiError } from './client';

export type DestinationType = 'ntfy' | 'discord' | 'webhook';

export type DeliveryState = 'pending' | 'delivered' | 'dead';

/** A delivery destination as listed by the registry. Secret values are masked
 *  by the server and are display-only. */
export interface DestinationSummary {
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
export interface RouteSummary {
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

/** Outcome of GET /readyz. getReadiness never throws: every failure mode is a
 *  member of this union. */
export type ReadinessResult =
  | {
      kind: 'ok' | 'schema_mismatch';
      schemaVersion: number;
      expectedSchemaVersion: number;
      lastMigrationFailure: MigrationFailure | null;
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
}

interface DestinationWire {
  name: string;
  type: DestinationType;
  enabled: boolean;
  disabled_reason: string | null;
  config: Record<string, string> | null;
  secrets: Record<string, string> | null;
  created_at: string;
  updated_at: string;
}

interface RouteWire {
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

interface ReadinessWire {
  status?: string;
  schema_version?: number;
  expected_schema_version?: number;
  last_migration_failure?: { version: number; error: string; failed_at: string } | null;
}

function mapDestination(wire: DestinationWire): DestinationSummary {
  return {
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
        path: `/v1/messages?wait=${SEND_TEST_WAIT_SECONDS}`,
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
  };
}

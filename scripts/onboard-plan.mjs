// Pure desired-state diff engine for declarative app onboarding.
//
// Nothing here makes a network call or touches an env var: it only compares the resolved manifest
// (`onboard-manifest.mjs`'s `resolveManifest` output) against the server's current state (the real
// `DestinationView` / `RouteView` / `checkView` list shapes from delivery-api's `/v1/admin/*`
// endpoints) and returns an ordered, human-printable action list. `apply` (a later step) is the
// only thing that executes these actions against `onboard-client.mjs`'s management API.
//
// Ownership seam: every caller passes a `scope` carrying `knownAppIds` (every app_id found across
// `apps/`, collected by the CLI, not just the one being planned). `ownedServerState` and
// `computePlan` both apply the longest-prefix-wins rule through this one `scope` object, so a
// destination like `my_app_ops` is never attributed to `my` merely because `my_app_ops` also starts
// with `my_`, whenever `my_app` is itself a known app id. This is what makes `apply --prune` safe
// even when the authoring-time lint (which also catches this collision) hasn't run.

const WEBHOOK_ROTATION_CONFIG_KEY = 'previous_secret_expires_at';
const WEBHOOK_ROTATION_SECRET_KEY = 'signing_secret_previous';

function routeKey(appPattern, channelPattern) {
  return `${appPattern}\u0000${channelPattern}`;
}

function routeTarget(appPattern, channelPattern) {
  return `${appPattern}/${channelPattern}`;
}

function checkKey(appId, name) {
  return `${appId}\u0000${name}`;
}

function checkTarget(appId, name) {
  return `${appId}/${name}`;
}

function sortedUnique(names) {
  return [...names].sort();
}

function sameNameSet(a, b) {
  const sa = sortedUnique(a);
  const sb = sortedUnique(b);
  if (sa.length !== sb.length) return false;
  return sa.every((value, index) => value === sb[index]);
}

/**
 * Returns, for a destination name, the longest `knownAppIds` entry that owns it (the name starts
 * with `${id}_`), or `undefined` if none does. Longest match wins so `my_app_ops` is attributed to
 * `my_app`, never to `my`, whenever both are known app ids.
 */
function destinationOwnerAppId(name, knownAppIds) {
  let owner;
  for (const id of knownAppIds) {
    if (name.startsWith(`${id}_`) && (owner === undefined || id.length > owner.length)) {
      owner = id;
    }
  }
  return owner;
}

/**
 * Filters the server's full destinations/routes/checks lists down to the subset owned by `scope`
 * (`{ kind: 'app', appId, knownAppIds }` or `{ kind: 'platform', knownAppIds }`). `knownAppIds` is
 * unused for the platform branch: the `platform_`/`platform`/`platform_*` reservation in the
 * manifest schema already keeps that prefix from ever colliding with an app's own `<app_id>_`
 * prefix, so no exclusion is needed there the way the app branch needs the longest-prefix rule.
 */
export function ownedServerState(server, scope) {
  if (scope.kind === 'platform') {
    return {
      destinations: server.destinations.filter((d) => d.name.startsWith('platform_')),
      routes: server.routes.filter((r) => r.app_pattern === '*'),
      checks: [],
    };
  }
  const { appId } = scope;
  const knownAppIds = scope.knownAppIds ?? [];
  return {
    destinations: server.destinations.filter(
      (d) => destinationOwnerAppId(d.name, knownAppIds) === appId,
    ),
    routes: server.routes.filter((r) => r.app_pattern === appId),
    checks: server.checks.filter((c) => c.app_id === appId),
  };
}

function createDestinationAction(want) {
  return {
    type: 'create-destination',
    target: want.name,
    body: {
      name: want.name,
      type: want.type,
      enabled: want.enabled,
      config: { ...want.config },
      secrets: { ...want.secrets },
    },
  };
}

/**
 * A server-only `config`/`secrets` key means the manifest can never remove it via PATCH (both
 * merge per key), so it forces a blocking `replace-required` — except, for a webhook destination
 * only, the documented rotate-secret overlap pair (`config.previous_secret_expires_at`,
 * `secrets.signing_secret_previous`), which the server keeps permanently after a rotation and the
 * manifest schema never declares. Every other server-only key, for every type including webhook,
 * still triggers it.
 */
function replaceRequiredReason(want, existing) {
  if (want.type !== existing.type) {
    return `type: ${existing.type} → ${want.type}`;
  }
  const isWebhook = existing.type === 'webhook';
  for (const key of Object.keys(existing.config ?? {})) {
    if (isWebhook && key === WEBHOOK_ROTATION_CONFIG_KEY) continue;
    if (!Object.prototype.hasOwnProperty.call(want.config ?? {}, key)) {
      return `config.${key} is set on the server and absent from the manifest (PATCH cannot remove it)`;
    }
  }
  for (const key of Object.keys(existing.secrets ?? {})) {
    if (isWebhook && key === WEBHOOK_ROTATION_SECRET_KEY) continue;
    if (!Object.prototype.hasOwnProperty.call(want.secrets ?? {}, key)) {
      return `secrets.${key} is set on the server and absent from the manifest (PATCH cannot remove it)`;
    }
  }
  return null;
}

/**
 * Masked server secret values are never compared (only key names feed `replaceRequiredReason`
 * above), so a secret-only change produces no action unless `resendSecrets` is set, in which case
 * every owned existing destination gets an update carrying all of its manifest secret keys,
 * independent of whether `enabled`/`config` also changed.
 */
function updateDestinationAction(want, existing, { resendSecrets }) {
  const changes = [];
  const body = {};

  if (want.enabled !== existing.enabled) {
    changes.push({ field: 'enabled', before: existing.enabled, after: want.enabled });
    body.enabled = want.enabled;
  }

  let configChanged = false;
  for (const key of Object.keys(want.config ?? {})) {
    if ((existing.config ?? {})[key] !== want.config[key]) {
      changes.push({
        field: `config.${key}`,
        before: (existing.config ?? {})[key],
        after: want.config[key],
      });
      configChanged = true;
    }
  }
  if (configChanged) body.config = { ...want.config };

  if (resendSecrets) {
    body.secrets = { ...want.secrets };
    changes.push({
      field: 'secrets',
      after: `${sortedUnique(Object.keys(want.secrets ?? {})).join(', ')} (resent)`,
    });
  }

  if (Object.keys(body).length === 0) return null;
  return { type: 'update-destination', target: want.name, body, changes };
}

function missingSharedAction(name) {
  return {
    type: 'missing-shared',
    target: name,
    reason: `shared destination "${name}" does not exist on the server`,
  };
}

function createRouteAction(want) {
  return {
    type: 'create-route',
    target: routeTarget(want.app_pattern, want.channel_pattern),
    body: {
      app_pattern: want.app_pattern,
      channel_pattern: want.channel_pattern,
      destinations: want.destinations,
      enabled: want.enabled,
    },
  };
}

/** Route PATCH replaces `destinations` wholesale, so an update always sends the full sorted list. */
function updateRouteAction(want, existing) {
  const changes = [];
  if (!sameNameSet(want.destinations, existing.destinations)) {
    changes.push({
      field: 'destinations',
      before: sortedUnique(existing.destinations),
      after: want.destinations,
    });
  }
  if (want.enabled !== existing.enabled) {
    changes.push({ field: 'enabled', before: existing.enabled, after: want.enabled });
  }
  if (changes.length === 0) return null;
  return {
    type: 'update-route',
    target: routeTarget(want.app_pattern, want.channel_pattern),
    body: { destinations: want.destinations, enabled: want.enabled },
    changes,
  };
}

function createCheckAction(want) {
  return {
    type: 'create-check',
    target: checkTarget(want.app_id, want.name),
    body: {
      app_id: want.app_id,
      name: want.name,
      period_seconds: want.period_seconds,
      grace_seconds: want.grace_seconds,
      destinations: want.destinations,
    },
  };
}

/**
 * Check PATCH replaces `destinations` wholesale and never accepts `app_id`/`name` (even unchanged),
 * so an update always sends `period_seconds`, `grace_seconds` and the full sorted destination list,
 * and never includes `app_id`/`name`.
 */
function updateCheckAction(want, existing) {
  const changes = [];
  const existingDestinationNames = existing.destinations.map((d) => d.name);
  if (want.period_seconds !== existing.period_seconds) {
    changes.push({
      field: 'period_seconds',
      before: existing.period_seconds,
      after: want.period_seconds,
    });
  }
  if (want.grace_seconds !== existing.grace_seconds) {
    changes.push({
      field: 'grace_seconds',
      before: existing.grace_seconds,
      after: want.grace_seconds,
    });
  }
  if (!sameNameSet(want.destinations, existingDestinationNames)) {
    changes.push({
      field: 'destinations',
      before: sortedUnique(existingDestinationNames),
      after: want.destinations,
    });
  }
  if (changes.length === 0) return null;
  return {
    type: 'update-check',
    target: checkTarget(want.app_id, want.name),
    body: {
      period_seconds: want.period_seconds,
      grace_seconds: want.grace_seconds,
      destinations: want.destinations,
    },
    changes,
  };
}

function unmanagedAction(kind, target) {
  return { type: `unmanaged-${kind}`, target };
}

function deleteAction(kind, target) {
  return { type: `delete-${kind}`, target };
}

/**
 * Computes the ordered action list for one manifest's desired state against the server's full
 * (unfiltered) destinations/routes/checks lists. `scope` (see `ownedServerState`) governs only the
 * unmanaged/delete direction: which owned-but-undeclared server objects get flagged or, with
 * `prune`, deleted. Creates/updates/`replace-required`/`missing-shared` are driven entirely by the
 * manifest's own desired state matched against the server by conflict key (`name` for
 * destinations, `(app_pattern, channel_pattern)` for routes, `(app_id, name)` for checks) — the
 * same keys delivery-api itself uses for its `409` conflicts.
 *
 * Action order: destination creates/updates (manifest order) and `missing-shared` checks, then
 * route creates/updates, then check creates/updates, then — only for owned objects absent from the
 * manifest — unmanaged/delete in `checks → routes → destinations` order.
 */
export function computePlan(desired, server, scope, { prune = false, resendSecrets = false } = {}) {
  const actions = [];

  const serverDestinationsByName = new Map(server.destinations.map((d) => [d.name, d]));
  const serverRoutesByKey = new Map(
    server.routes.map((r) => [routeKey(r.app_pattern, r.channel_pattern), r]),
  );
  const serverChecksByKey = new Map(server.checks.map((c) => [checkKey(c.app_id, c.name), c]));

  const matchedDestinationNames = new Set();
  const matchedRouteKeys = new Set();
  const matchedCheckKeys = new Set();

  for (const want of desired.destinations) {
    matchedDestinationNames.add(want.name);
    const existing = serverDestinationsByName.get(want.name);
    if (!existing) {
      actions.push(createDestinationAction(want));
      continue;
    }
    const reason = replaceRequiredReason(want, existing);
    if (reason) {
      actions.push({ type: 'replace-required', target: want.name, reason });
      continue;
    }
    const update = updateDestinationAction(want, existing, { resendSecrets });
    if (update) actions.push(update);
  }

  for (const name of desired.shared) {
    if (!serverDestinationsByName.has(name)) {
      actions.push(missingSharedAction(name));
    }
  }

  for (const want of desired.routes) {
    const key = routeKey(want.app_pattern, want.channel_pattern);
    matchedRouteKeys.add(key);
    const existing = serverRoutesByKey.get(key);
    if (!existing) {
      actions.push(createRouteAction(want));
      continue;
    }
    const update = updateRouteAction(want, existing);
    if (update) actions.push(update);
  }

  for (const want of desired.checks) {
    const key = checkKey(want.app_id, want.name);
    matchedCheckKeys.add(key);
    const existing = serverChecksByKey.get(key);
    if (!existing) {
      actions.push(createCheckAction(want));
      continue;
    }
    const update = updateCheckAction(want, existing);
    if (update) actions.push(update);
  }

  const owned = ownedServerState(server, scope);
  const unmanagedChecks = owned.checks.filter(
    (c) => !matchedCheckKeys.has(checkKey(c.app_id, c.name)),
  );
  const unmanagedRoutes = owned.routes.filter(
    (r) => !matchedRouteKeys.has(routeKey(r.app_pattern, r.channel_pattern)),
  );
  const unmanagedDestinations = owned.destinations.filter(
    (d) => !matchedDestinationNames.has(d.name),
  );

  for (const c of unmanagedChecks) {
    const target = checkTarget(c.app_id, c.name);
    actions.push(prune ? deleteAction('check', target) : unmanagedAction('check', target));
  }
  for (const r of unmanagedRoutes) {
    const target = routeTarget(r.app_pattern, r.channel_pattern);
    actions.push(prune ? deleteAction('route', target) : unmanagedAction('route', target));
  }
  for (const d of unmanagedDestinations) {
    actions.push(
      prune ? deleteAction('destination', d.name) : unmanagedAction('destination', d.name),
    );
  }

  return actions;
}

function formatValue(value) {
  if (Array.isArray(value)) return `[${value.join(', ')}]`;
  return String(value);
}

// Invariant: never add a `changes` entry whose `field` starts with `'secrets'` and whose
// `before`/`after` holds a raw secret value — only the sanitized `(resent)` key-name summary.
function formatChanges(changes) {
  return changes
    .map(({ field, before, after }) =>
      field === 'secrets' || field.startsWith('secrets.')
        ? `${field}: ${after}`
        : `${field}: ${formatValue(before)} → ${formatValue(after)}`,
    )
    .join(', ');
}

function kindOf(actionType) {
  return actionType.slice(actionType.indexOf('-') + 1);
}

function formatAction(action) {
  if (action.type === 'replace-required' || action.type === 'missing-shared') {
    return `! blocking ${action.target}: ${action.reason}`;
  }
  if (action.type.startsWith('create-')) {
    return `+ create ${kindOf(action.type)} ${action.target}`;
  }
  if (action.type.startsWith('update-')) {
    return `~ update ${kindOf(action.type)} ${action.target}: ${formatChanges(action.changes)}`;
  }
  if (action.type.startsWith('delete-')) {
    return `- delete ${kindOf(action.type)} ${action.target}`;
  }
  if (action.type.startsWith('unmanaged-')) {
    return `? unmanaged ${kindOf(action.type)} ${action.target}`;
  }
  throw new Error(`unknown action type: ${action.type}`);
}

/**
 * Renders `computePlan`'s action list as the operator-facing preview: one line per action, then a
 * summary line, or exactly `No changes.` when there are no actions at all. Never prints a resolved
 * secret value — a secret-bearing update shows only `secrets: <keys> (resent)`.
 */
export function formatPlan(actions) {
  if (actions.length === 0) return 'No changes.';

  const lines = actions.map(formatAction);
  let create = 0;
  let update = 0;
  let del = 0;
  let unmanaged = 0;
  for (const action of actions) {
    if (action.type.startsWith('create-')) create += 1;
    else if (action.type.startsWith('update-')) update += 1;
    else if (action.type.startsWith('delete-')) del += 1;
    else if (action.type.startsWith('unmanaged-')) unmanaged += 1;
  }
  lines.push(`${create} to create, ${update} to update, ${del} to delete, ${unmanaged} unmanaged`);
  return lines.join('\n');
}

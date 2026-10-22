// Offline half of declarative app onboarding: parse and validate one app/platform manifest,
// lint a whole `apps/` directory, and resolve a manifest into its desired server state
// (durations converted, per-environment overrides applied, secrets resolved from env). No
// network call and no env read ever happens in `validateManifest` or `lintDirectory`.
//
// The validation regexes and bounds below mirror the Go source they are matched against so the
// two stay comparable; each has a comment naming its Go counterpart.

import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';

// Mirrors delivery-api/internal/registry/validate.go's destinationNamePattern / appIDPattern
// and delivery-api/internal/heartbeat/validate.go's namePattern: lowercase letters, digits and
// underscores, starting with a letter or digit, 1-63 characters. Used for app_id, destination
// names, `use` references and heartbeat names alike.
export const NAME_PATTERN = /^[a-z0-9][a-z0-9_]{0,62}$/;

// Mirrors registry/validate.go's channelExactPattern / channelPrefixPattern.
export const CHANNEL_EXACT_PATTERN = /^[a-z0-9][a-z0-9_]{0,31}$/;
export const CHANNEL_PREFIX_PATTERN = /^[a-z0-9][a-z0-9_]{0,30}\*$/;

// Mirrors registry/validate.go's ntfyTopicPattern.
export const NTFY_TOPIC_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;

// Manifest-only (no Go counterpart): a secret must reference an env var name, never carry a
// literal value.
export const ENV_VAR_NAME_PATTERN = /^[A-Z][A-Z0-9_]*$/;

// Manifest-only: an `environments.<env>` key.
export const ENVIRONMENT_NAME_PATTERN = /^[a-z][a-z0-9_-]{0,31}$/;

// "90s" / "5m" / "24h" / "7d": no leading zero, no decimals, a single lowercase unit letter.
export const DURATION_PATTERN = /^[1-9][0-9]*(s|m|h|d)$/;
const DURATION_UNIT_SECONDS = { s: 1, m: 60, h: 3600, d: 86400 };

// Mirrors heartbeat/validate.go's MinPeriodSeconds..MaxGraceSeconds.
export const MIN_PERIOD_SECONDS = 60;
export const MAX_PERIOD_SECONDS = 2_678_400;
export const MIN_GRACE_SECONDS = 60;
export const MAX_GRACE_SECONDS = 604_800;

// Mirrors registry/validate.go's minRouteDestinations / maxRouteDestinations and
// heartbeat/validate.go's MinDestinations / MaxDestinations (the two are the same bound).
export const MIN_ROUTE_DESTINATIONS = 1;
export const MAX_ROUTE_DESTINATIONS = 20;

const DESTINATION_TYPES = ['ntfy', 'discord', 'webhook'];

// Mirrors registry/validate.go's validateNtfyFields / validateDiscordFields /
// validateWebhookFields allowed-key sets, minus the server-only rotation pair
// (config.previous_secret_expires_at / secrets.signing_secret_previous), which a manifest never
// declares.
const DESTINATION_FIELD_RULES = {
  ntfy: {
    config: ['topic'],
    requiredConfig: ['topic'],
    secrets: ['access_token'],
    requiredSecrets: [],
  },
  discord: {
    config: [],
    requiredConfig: [],
    secrets: ['webhook_url'],
    requiredSecrets: ['webhook_url'],
  },
  webhook: {
    config: [],
    requiredConfig: [],
    secrets: ['url', 'signing_secret'],
    requiredSecrets: ['url', 'signing_secret'],
  },
};

const RESERVED_APP_IDS = new Set(['everyone', 'platform']);
const PLATFORM_DESTINATION_PREFIX = 'platform_';

/** Parse a "90s" / "5m" / "24h" / "7d"-style duration into whole seconds. Throws otherwise. */
export function parseDuration(text) {
  if (typeof text !== 'string' || !DURATION_PATTERN.test(text)) {
    throw new Error(`invalid duration ${JSON.stringify(text)}`);
  }
  const unit = text.slice(-1);
  const amount = Number(text.slice(0, -1));
  return amount * DURATION_UNIT_SECONDS[unit];
}

function isPlainObject(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function pathKey(base, key) {
  return base ? `${base}.${key}` : key;
}

function pathIndex(base, index) {
  return `${base}[${index}]`;
}

function validateSecretRef(value, base, push) {
  if (typeof value === 'string') {
    push(base, 'must reference an env var, not a literal value');
    return;
  }
  if (!isPlainObject(value)) {
    push(base, 'must be an object of the form { "env": "VAR_NAME" }');
    return;
  }
  for (const key of Object.keys(value)) {
    if (key !== 'env') push(pathKey(base, key), 'unknown key');
  }
  if (typeof value.env !== 'string' || !ENV_VAR_NAME_PATTERN.test(value.env)) {
    push(pathKey(base, 'env'), 'invalid env var name');
  }
}

function validateFieldMap(map, base, allowedKeys, requiredKeys, push, { allowSecretRef }) {
  if (map === undefined) {
    for (const key of requiredKeys) push(pathKey(base, key), 'required');
    return;
  }
  if (!isPlainObject(map)) {
    push(base, 'must be an object');
    return;
  }
  for (const key of Object.keys(map)) {
    if (!allowedKeys.includes(key)) push(pathKey(base, key), 'unknown key');
  }
  for (const key of requiredKeys) {
    if (!(key in map)) push(pathKey(base, key), 'required');
  }
  for (const key of allowedKeys) {
    if (!(key in map)) continue;
    const valuePath = pathKey(base, key);
    if (allowSecretRef) {
      validateSecretRef(map[key], valuePath, push);
    } else if (typeof map[key] !== 'string') {
      push(valuePath, 'must be a string');
    }
  }
}

function validateOwnedDestination(entry, base, { push, ownedNames, requirePrefix, prefixError }) {
  const allowedKeys = ['name', 'type', 'config', 'secrets', 'enabled'];
  for (const key of Object.keys(entry)) {
    if (!allowedKeys.includes(key)) push(pathKey(base, key), 'unknown key');
  }

  const name = entry.name;
  let nameOk = typeof name === 'string' && NAME_PATTERN.test(name);
  if (!nameOk) {
    push(pathKey(base, 'name'), 'invalid name');
  } else if (!requirePrefix(name)) {
    push(pathKey(base, 'name'), prefixError);
    nameOk = false;
  }
  if (nameOk) {
    if (ownedNames.has(name)) push(pathKey(base, 'name'), `duplicate destination name "${name}"`);
    ownedNames.add(name);
  }

  const type = entry.type;
  if (!DESTINATION_TYPES.includes(type)) {
    push(pathKey(base, 'type'), 'invalid type');
    return;
  }
  const rules = DESTINATION_FIELD_RULES[type];
  validateFieldMap(
    entry.config,
    pathKey(base, 'config'),
    rules.config,
    rules.requiredConfig,
    push,
    {
      allowSecretRef: false,
    },
  );
  validateFieldMap(
    entry.secrets,
    pathKey(base, 'secrets'),
    rules.secrets,
    rules.requiredSecrets,
    push,
    { allowSecretRef: true },
  );

  if (
    type === 'ntfy' &&
    isPlainObject(entry.config) &&
    typeof entry.config.topic === 'string' &&
    !NTFY_TOPIC_PATTERN.test(entry.config.topic)
  ) {
    push(pathKey(pathKey(base, 'config'), 'topic'), 'invalid topic');
  }

  if ('enabled' in entry && typeof entry.enabled !== 'boolean') {
    push(pathKey(base, 'enabled'), 'must be a boolean');
  }
}

function validateSharedReference(entry, base, { push, useNames, rejectPrefix, prefixError }) {
  for (const key of Object.keys(entry)) {
    if (key !== 'use') push(pathKey(base, key), 'unknown key');
  }
  const name = entry.use;
  if (typeof name !== 'string' || !NAME_PATTERN.test(name)) {
    push(pathKey(base, 'use'), 'invalid name');
    return;
  }
  if (rejectPrefix(name)) {
    push(pathKey(base, 'use'), prefixError);
    return;
  }
  if (useNames.has(name)) push(pathKey(base, 'use'), `duplicate shared reference "${name}"`);
  useNames.add(name);
}

/** Validates `destinations[]`; returns the owned and `use` name sets it collected. */
function validateDestinationsArray(
  destinations,
  { push, requirePrefix, rejectPrefix, prefixErrorOwned, prefixErrorUse },
) {
  const ownedNames = new Set();
  const useNames = new Set();
  if (destinations === undefined) return { ownedNames, useNames };
  if (!Array.isArray(destinations)) {
    push('destinations', 'must be an array');
    return { ownedNames, useNames };
  }
  destinations.forEach((entry, index) => {
    const base = pathIndex('destinations', index);
    if (!isPlainObject(entry)) {
      push(base, 'must be an object');
      return;
    }
    if (Object.prototype.hasOwnProperty.call(entry, 'use')) {
      validateSharedReference(entry, base, {
        push,
        useNames,
        rejectPrefix,
        prefixError: prefixErrorUse,
      });
    } else if (Object.prototype.hasOwnProperty.call(entry, 'name')) {
      validateOwnedDestination(entry, base, {
        push,
        ownedNames,
        requirePrefix,
        prefixError: prefixErrorOwned,
      });
    } else {
      push(base, 'must have a "name" (owned destination) or "use" (shared reference)');
    }
  });
  return { ownedNames, useNames };
}

function validateDestinationRefs(names, base, { push, knownNames }) {
  if (!Array.isArray(names)) {
    push(base, 'must be an array');
    return;
  }
  if (names.length < MIN_ROUTE_DESTINATIONS || names.length > MAX_ROUTE_DESTINATIONS) {
    push(base, `must have ${MIN_ROUTE_DESTINATIONS}-${MAX_ROUTE_DESTINATIONS} destinations`);
  }
  const seen = new Set();
  names.forEach((name, index) => {
    const entryPath = pathIndex(base, index);
    if (typeof name !== 'string') {
      push(entryPath, 'must be a string');
      return;
    }
    if (seen.has(name)) {
      push(entryPath, `duplicate destination "${name}"`);
      return;
    }
    seen.add(name);
    if (!knownNames.has(name)) push(entryPath, `undeclared destination "${name}"`);
  });
}

function validateRoutesArray(routes, { push, knownNames, allowWildcard }) {
  if (routes === undefined) return;
  if (!Array.isArray(routes)) {
    push('routes', 'must be an array');
    return;
  }
  const channels = new Set();
  routes.forEach((entry, index) => {
    const base = pathIndex('routes', index);
    if (!isPlainObject(entry)) {
      push(base, 'must be an object');
      return;
    }
    const allowedKeys = ['channel', 'destinations', 'enabled'];
    for (const key of Object.keys(entry)) {
      if (!allowedKeys.includes(key)) push(pathKey(base, key), 'unknown key');
    }
    const channel = entry.channel;
    const channelValid =
      typeof channel === 'string' &&
      ((allowWildcard && channel === '*') ||
        CHANNEL_EXACT_PATTERN.test(channel) ||
        CHANNEL_PREFIX_PATTERN.test(channel));
    if (!channelValid) {
      push(pathKey(base, 'channel'), 'invalid channel');
    } else {
      if (channels.has(channel)) push(pathKey(base, 'channel'), `duplicate channel "${channel}"`);
      channels.add(channel);
    }
    validateDestinationRefs(entry.destinations, pathKey(base, 'destinations'), {
      push,
      knownNames,
    });
    if ('enabled' in entry && typeof entry.enabled !== 'boolean') {
      push(pathKey(base, 'enabled'), 'must be a boolean');
    }
  });
}

function validateHeartbeatDuration(value, fieldPath, push, min, max) {
  if (typeof value !== 'string') {
    push(fieldPath, 'required');
    return;
  }
  let seconds;
  try {
    seconds = parseDuration(value);
  } catch {
    push(fieldPath, 'invalid duration');
    return;
  }
  if (seconds < min || seconds > max) push(fieldPath, `out of bounds (${min}-${max} seconds)`);
}

/** Validates `heartbeats[]`; returns the heartbeat name set it collected. */
function validateHeartbeatsArray(heartbeats, { push, knownNames }) {
  const names = new Set();
  if (heartbeats === undefined) return names;
  if (!Array.isArray(heartbeats)) {
    push('heartbeats', 'must be an array');
    return names;
  }
  heartbeats.forEach((entry, index) => {
    const base = pathIndex('heartbeats', index);
    if (!isPlainObject(entry)) {
      push(base, 'must be an object');
      return;
    }
    const allowedKeys = ['name', 'period', 'grace', 'destinations'];
    for (const key of Object.keys(entry)) {
      if (!allowedKeys.includes(key)) push(pathKey(base, key), 'unknown key');
    }
    const name = entry.name;
    if (typeof name !== 'string' || !NAME_PATTERN.test(name)) {
      push(pathKey(base, 'name'), 'invalid name');
    } else {
      if (names.has(name)) push(pathKey(base, 'name'), `duplicate heartbeat name "${name}"`);
      names.add(name);
    }
    validateHeartbeatDuration(
      entry.period,
      pathKey(base, 'period'),
      push,
      MIN_PERIOD_SECONDS,
      MAX_PERIOD_SECONDS,
    );
    validateHeartbeatDuration(
      entry.grace,
      pathKey(base, 'grace'),
      push,
      MIN_GRACE_SECONDS,
      MAX_GRACE_SECONDS,
    );
    validateDestinationRefs(entry.destinations, pathKey(base, 'destinations'), {
      push,
      knownNames,
    });
  });
  return names;
}

function validateEnvironments(environments, { push, heartbeatNames, ownedDestinationNames }) {
  if (environments === undefined) return;
  if (!isPlainObject(environments)) {
    push('environments', 'must be an object');
    return;
  }
  for (const [envName, block] of Object.entries(environments)) {
    const base = pathKey('environments', envName);
    if (!ENVIRONMENT_NAME_PATTERN.test(envName)) {
      push(base, 'invalid environment name');
      continue;
    }
    if (!isPlainObject(block)) {
      push(base, 'must be an object');
      continue;
    }
    const allowedTop = ['heartbeats', 'destinations'];
    for (const key of Object.keys(block)) {
      if (!allowedTop.includes(key)) push(pathKey(base, key), 'unknown key');
    }

    if (block.heartbeats !== undefined) {
      if (!isPlainObject(block.heartbeats)) {
        push(pathKey(base, 'heartbeats'), 'must be an object');
      } else {
        for (const [name, override] of Object.entries(block.heartbeats)) {
          const hbBase = pathKey(pathKey(base, 'heartbeats'), name);
          if (!heartbeatNames.has(name)) {
            push(hbBase, `unknown heartbeat "${name}"`);
            continue;
          }
          if (!isPlainObject(override)) {
            push(hbBase, 'must be an object');
            continue;
          }
          const allowed = ['period', 'grace'];
          for (const key of Object.keys(override)) {
            if (!allowed.includes(key)) push(pathKey(hbBase, key), 'unknown key');
          }
          if (
            'period' in override &&
            (typeof override.period !== 'string' || !DURATION_PATTERN.test(override.period))
          ) {
            push(pathKey(hbBase, 'period'), 'invalid duration');
          }
          if (
            'grace' in override &&
            (typeof override.grace !== 'string' || !DURATION_PATTERN.test(override.grace))
          ) {
            push(pathKey(hbBase, 'grace'), 'invalid duration');
          }
        }
      }
    }

    if (block.destinations !== undefined) {
      if (!isPlainObject(block.destinations)) {
        push(pathKey(base, 'destinations'), 'must be an object');
      } else {
        for (const [name, override] of Object.entries(block.destinations)) {
          const destBase = pathKey(pathKey(base, 'destinations'), name);
          if (!ownedDestinationNames.has(name)) {
            push(destBase, `unknown destination "${name}"`);
            continue;
          }
          if (!isPlainObject(override)) {
            push(destBase, 'must be an object');
            continue;
          }
          for (const key of Object.keys(override)) {
            if (key !== 'enabled') push(pathKey(destBase, key), 'unknown key');
          }
          if ('enabled' in override && typeof override.enabled !== 'boolean') {
            push(pathKey(destBase, 'enabled'), 'must be a boolean');
          }
        }
      }
    }
  }
}

function validateAppManifest(json, { push, fileName }) {
  const allowedTop = ['app_id', 'destinations', 'routes', 'heartbeats', 'environments'];
  for (const key of Object.keys(json)) {
    if (!allowedTop.includes(key)) push(key, 'unknown key');
  }

  const appId = json.app_id;
  let appIdValid = typeof appId === 'string' && NAME_PATTERN.test(appId);
  if (!appIdValid) {
    push('app_id', 'invalid app_id');
  } else if (RESERVED_APP_IDS.has(appId) || appId.startsWith(PLATFORM_DESTINATION_PREFIX)) {
    push('app_id', `app_id "${appId}" is reserved`);
    appIdValid = false;
  }
  if (appIdValid && typeof fileName === 'string') {
    const base = path.basename(fileName, '.json');
    if (base !== appId) push('app_id', `file name must equal app_id (expected ${base}.json)`);
  }

  const prefix = appIdValid ? `${appId}_` : null;
  const { ownedNames, useNames } = validateDestinationsArray(json.destinations, {
    push,
    requirePrefix: (name) => (prefix ? name.startsWith(prefix) : true),
    rejectPrefix: (name) => (prefix ? name.startsWith(prefix) : false),
    prefixErrorOwned: prefix
      ? `owned destination name must start with "${prefix}"`
      : 'cannot validate name prefix: invalid app_id',
    prefixErrorUse: prefix
      ? `shared reference name must not start with "${prefix}"`
      : 'cannot validate name prefix: invalid app_id',
  });
  const knownNames = new Set([...ownedNames, ...useNames]);

  validateRoutesArray(json.routes, { push, knownNames, allowWildcard: false });
  const heartbeatNames = validateHeartbeatsArray(json.heartbeats, { push, knownNames });
  validateEnvironments(json.environments, {
    push,
    heartbeatNames,
    ownedDestinationNames: ownedNames,
  });
}

function validatePlatformManifest(json, { push, fileName }) {
  const allowedTop = ['platform', 'destinations', 'routes'];
  for (const key of Object.keys(json)) {
    if (!allowedTop.includes(key)) push(key, 'unknown key');
  }
  if (json.platform !== true) push('platform', 'must be true');
  if (typeof fileName === 'string' && path.basename(fileName) !== '_platform.json') {
    push('platform', 'platform manifest file must be named _platform.json');
  }

  const { ownedNames, useNames } = validateDestinationsArray(json.destinations, {
    push,
    requirePrefix: (name) => name.startsWith(PLATFORM_DESTINATION_PREFIX),
    rejectPrefix: (name) => name.startsWith(PLATFORM_DESTINATION_PREFIX),
    prefixErrorOwned: `owned destination name must start with "${PLATFORM_DESTINATION_PREFIX}"`,
    prefixErrorUse: `shared reference name must not start with "${PLATFORM_DESTINATION_PREFIX}"`,
  });
  const knownNames = new Set([...ownedNames, ...useNames]);
  validateRoutesArray(json.routes, { push, knownNames, allowWildcard: true });
}

/**
 * Validate one parsed manifest. Never reads env values and never makes a network call.
 * Returns `{ kind: 'app'|'platform', errors: [{ path, message }] }`. `fileName` (the manifest's
 * own basename, e.g. "myapp.json") is optional; when given, it is checked against `app_id` (app
 * manifests) or against the fixed `_platform.json` name (platform manifests).
 */
export function validateManifest(json, { fileName } = {}) {
  const errors = [];
  const push = (fieldPath, message) => errors.push({ path: fieldPath, message });

  if (!isPlainObject(json)) {
    push('', 'manifest must be a JSON object');
    return { kind: 'app', errors };
  }

  const kind = Object.prototype.hasOwnProperty.call(json, 'platform') ? 'platform' : 'app';
  if (kind === 'platform') {
    validatePlatformManifest(json, { push, fileName });
  } else {
    validateAppManifest(json, { push, fileName });
  }
  return { kind, errors };
}

/**
 * Lint every manifest under `dir`: every per-file `validateManifest` rule, plus the
 * cross-manifest rules (app_id prefix collisions, duplicate owned destination names, unresolved
 * `use` references reported as warnings). Never reads env values and never makes a network or
 * fetch call. `readDir`/`readFile` are injectable for tests; `dir` missing or empty lints clean
 * with `count: 0`.
 */
export function lintDirectory({ dir, readDir = readdirSync, readFile = readFileSync } = {}) {
  const errors = [];
  const warnings = [];
  let entries;
  try {
    entries = readDir(dir);
  } catch (err) {
    if (err && err.code === 'ENOENT') entries = [];
    else throw err;
  }

  // `parsedFiles` holds every file that parsed as JSON, regardless of whether `validateManifest`
  // found other, unrelated errors in it: a destination-name collision or an app_id prefix clash
  // is useful to report even when the colliding file also has its own independent mistake (e.g.
  // a copy-pasted file whose app_id field was never updated).
  const parsedFiles = [];
  let count = 0;
  for (const entry of [...entries].sort()) {
    if (entry === 'README.md') continue;
    const label = `${dir}/${entry}`;
    if (entry.startsWith('_') && entry !== '_platform.json') {
      errors.push(
        `${label}: file: stray underscore-prefixed file (only _platform.json is allowed)`,
      );
      continue;
    }
    if (!entry.endsWith('.json')) {
      errors.push(`${label}: file: not a .json manifest file`);
      continue;
    }

    count += 1;
    let text;
    try {
      text = readFile(path.join(dir, entry), 'utf8');
    } catch (err) {
      errors.push(`${label}: file: unreadable (${err.message})`);
      continue;
    }
    let parsed;
    try {
      parsed = JSON.parse(text);
    } catch (err) {
      errors.push(`${label}: file: invalid JSON (${err.message})`);
      continue;
    }

    const { kind, errors: fileErrors } = validateManifest(parsed, { fileName: entry });
    for (const { path: jsonPath, message } of fileErrors) {
      errors.push(`${label}: ${jsonPath || '(root)'}: ${message}`);
    }
    parsedFiles.push({ entry, label, kind, manifest: parsed });
  }

  // Cross-manifest rule: no app_id where one id plus "_" prefixes the other.
  const appManifests = parsedFiles.filter(
    (file) => file.kind === 'app' && typeof file.manifest.app_id === 'string',
  );
  for (let i = 0; i < appManifests.length; i += 1) {
    for (let j = i + 1; j < appManifests.length; j += 1) {
      const a = appManifests[i];
      const b = appManifests[j];
      const idA = a.manifest.app_id;
      const idB = b.manifest.app_id;
      if (`${idA}_`.startsWith(`${idB}_`) || `${idB}_`.startsWith(`${idA}_`)) {
        errors.push(
          `${dir}: app_id: "${idA}" (${a.label}) and "${idB}" (${b.label}) collide ` +
            '(one id plus "_" prefixes the other)',
        );
      }
    }
  }

  // Cross-manifest rule: no owned destination name declared by two files.
  const ownedBy = new Map();
  const allOwnedNames = new Set();
  const allUseNames = new Set();
  for (const file of parsedFiles) {
    const destinations = Array.isArray(file.manifest.destinations)
      ? file.manifest.destinations
      : [];
    for (const entry of destinations) {
      if (!isPlainObject(entry)) continue;
      if (typeof entry.name === 'string') {
        allOwnedNames.add(entry.name);
        const owners = ownedBy.get(entry.name) ?? [];
        owners.push(file.label);
        ownedBy.set(entry.name, owners);
      } else if (typeof entry.use === 'string') {
        allUseNames.add(entry.use);
      }
    }
  }
  for (const [name, owners] of ownedBy) {
    if (owners.length > 1) {
      errors.push(
        `${dir}: destinations: "${name}" is declared by more than one file (${owners.join(', ')})`,
      );
    }
  }

  // Cross-manifest rule (warning only): every unresolved `use` name is reported once.
  for (const name of [...allUseNames].sort()) {
    if (!allOwnedNames.has(name)) warnings.push(`external: ${name}`);
  }

  return { errors, warnings, count };
}

/**
 * Resolve a validated manifest into its desired server state: `validateManifest` runs first and
 * its errors (if any) are thrown as one Error carrying `.errors`, before any override, duration
 * conversion or secret-resolution work happens. Then: environment overrides are applied, every
 * duration is converted to seconds (bounds re-checked on the overridden value), and every
 * secret's env var is resolved — a missing or empty one does not throw immediately; every such
 * var across the whole manifest is collected and reported together, and nothing partially
 * resolved is ever returned.
 */
export function resolveManifest(manifest, { env = process.env, envName } = {}) {
  const { kind, errors } = validateManifest(manifest);
  if (errors.length > 0) {
    const err = new Error(
      `invalid manifest: ${errors.map((e) => `${e.path || '(root)'}: ${e.message}`).join('; ')}`,
    );
    err.errors = errors;
    throw err;
  }

  const notices = [];
  let envOverrides;
  if (kind === 'app' && envName !== undefined) {
    const block = isPlainObject(manifest.environments)
      ? Object.prototype.hasOwnProperty.call(manifest.environments, envName)
        ? manifest.environments[envName]
        : undefined
      : undefined;
    envOverrides = block;
    if (block === undefined) notices.push(`env ${envName}: no overrides`);
  }

  const appPattern = kind === 'platform' ? '*' : manifest.app_id;
  const routeList = Array.isArray(manifest.routes) ? manifest.routes : [];
  const routes = routeList.map((route) => ({
    app_pattern: appPattern,
    channel_pattern: route.channel,
    destinations: [...route.destinations].sort(),
    enabled: route.enabled ?? true,
  }));

  const heartbeatList =
    kind === 'app' && Array.isArray(manifest.heartbeats) ? manifest.heartbeats : [];
  const checks = heartbeatList.map((heartbeat) => {
    const override = envOverrides?.heartbeats?.[heartbeat.name];
    const periodText = override?.period ?? heartbeat.period;
    const graceText = override?.grace ?? heartbeat.grace;
    const periodSeconds = parseDuration(periodText);
    const graceSeconds = parseDuration(graceText);
    if (periodSeconds < MIN_PERIOD_SECONDS || periodSeconds > MAX_PERIOD_SECONDS) {
      throw new Error(
        `heartbeats.${heartbeat.name}.period out of bounds after environment override: ${periodText}`,
      );
    }
    if (graceSeconds < MIN_GRACE_SECONDS || graceSeconds > MAX_GRACE_SECONDS) {
      throw new Error(
        `heartbeats.${heartbeat.name}.grace out of bounds after environment override: ${graceText}`,
      );
    }
    return {
      app_id: manifest.app_id,
      name: heartbeat.name,
      period_seconds: periodSeconds,
      grace_seconds: graceSeconds,
      destinations: [...heartbeat.destinations].sort(),
    };
  });

  const destinationList = Array.isArray(manifest.destinations) ? manifest.destinations : [];
  const shared = new Set();
  const missingVars = new Set();
  const resolvedDestinations = [];
  for (const entry of destinationList) {
    if (typeof entry.use === 'string') {
      shared.add(entry.use);
      continue;
    }
    const name = entry.name;
    const enabledOverride = envOverrides?.destinations?.[name]?.enabled;
    const enabled = enabledOverride !== undefined ? enabledOverride : (entry.enabled ?? true);
    const config = { ...(entry.config ?? {}) };
    const secrets = {};
    for (const [key, ref] of Object.entries(entry.secrets ?? {})) {
      const varName = ref.env;
      const value = env[varName];
      if (value === undefined || value === '') {
        missingVars.add(varName);
        continue;
      }
      secrets[key] = value;
    }
    resolvedDestinations.push({ name, type: entry.type, config, secrets, enabled });
  }
  if (missingVars.size > 0) {
    throw new Error(`missing secret env var(s): ${[...missingVars].sort().join(', ')}`);
  }

  return {
    destinations: resolvedDestinations,
    shared: [...shared].sort(),
    routes,
    checks,
    notices,
  };
}

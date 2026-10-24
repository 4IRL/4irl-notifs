// CLI entry point for declarative app onboarding: wires the offline manifest/lint module
// (`onboard-manifest.mjs`), the pluggable management target/HTTP client (`onboard-client.mjs`)
// and the pure desired-state diff engine (`onboard-plan.mjs`) into the four operator commands:
//
//   node scripts/onboard.mjs plan   <manifest path> [--env <name>] [--prune] [--resend-secrets]
//   node scripts/onboard.mjs apply  <manifest path> [--env <name>] [--prune] [--resend-secrets]
//   node scripts/onboard.mjs verify <manifest path> [--env <name>]
//   node scripts/onboard.mjs lint   [dir=apps]
//
// App-repo mode (run by an app's own deploy, against the app-scoped API with the app's key):
//
//   node scripts/onboard.mjs <plan|apply|verify> --manifest <path> --target app [--env <name>] ...
//   node scripts/onboard.mjs lint --manifest <path> --target app
//
// `--target app` talks to `/v1/apps/{app_id}/...` only (never `/v1/admin`) and requires
// ONBOARD_APP_KEY, even on loopback. The server scopes every list and write to the app's own
// objects, so this mode skips the cross-manifest checks that need a directory of every app's
// manifest (`lintDirectory`, the `--prune` lint gate, `knownAppIds` collection).
//
// `main(argv, deps)` is the one thing tests drive end-to-end: `deps` lets a test swap in a
// stateful fake management/app API (via `createAdminApi`/`createAppScopedApi`/`createAppApi`) and
// a spying `resolveTarget`/`createHttp`, while manifests and lint fixtures are read from a real
// temp directory the same way `onboard-manifest.test.mjs` does.
import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  NAME_PATTERN,
  lintDirectory,
  resolveManifest,
  validateManifest,
} from './onboard-manifest.mjs';
import {
  ApiError,
  createAdminApi,
  createAppApi,
  createAppScopedApi,
  createHttp,
  resolveTarget,
} from './onboard-client.mjs';
import { computePlan, formatPlan } from './onboard-plan.mjs';

const PLAN_APPLY_TIMEOUT_MS = 15000;
const VERIFY_TIMEOUT_MS = 25000;

const TARGET_OPERATOR = 'operator';
const TARGET_APP = 'app';

const USAGE =
  'usage: node scripts/onboard.mjs <plan|apply|verify> <manifest path> ' +
  '[--env <name>] [--prune] [--resend-secrets] | node scripts/onboard.mjs lint [dir=apps] | ' +
  'node scripts/onboard.mjs <plan|apply|verify> --manifest <path> --target app ' +
  '[--env <name>] [--prune] [--resend-secrets] | ' +
  'node scripts/onboard.mjs lint --manifest <path> --target app';

function usageError(message) {
  return new Error(message ? `${message}\n${USAGE}` : USAGE);
}

/** Reads the value following a `--flag <value>` pair; a missing or flag-like value is a usage error. */
function flagValue(rest, index, flag) {
  const value = rest[index + 1];
  if (value === undefined || value.startsWith('--')) throw usageError(`${flag} requires a value`);
  return value;
}

function parseTargetValue(value) {
  if (value !== TARGET_APP) throw usageError(`--target must be "${TARGET_APP}", got "${value}"`);
  return value;
}

/**
 * Parses argv into `{ command, manifestPath, envName, prune, resendSecrets, target }`
 * (plan/apply/verify; `target` is `operator` unless `--target app`) or `{ command: 'lint', dir }`
 * / `{ command: 'lint', manifestPath, target: 'app' }`. Every rejection here is a usage error
 * thrown before any manifest is read or any target resolved — this function touches neither `fs`
 * nor `deps`. `--manifest` and `--target app` go together: `--target app` takes its manifest only
 * through `--manifest` (never a positional), and `--manifest` is only valid with `--target app`.
 */
export function parseArgs(argv) {
  const [command, ...rest] = argv;

  if (command === 'lint') {
    const positional = [];
    let manifestFlag;
    let targetFlag;
    for (let i = 0; i < rest.length; i += 1) {
      const arg = rest[i];
      if (arg === '--manifest') {
        if (manifestFlag !== undefined) throw usageError('--manifest given more than once');
        manifestFlag = flagValue(rest, i, '--manifest');
        i += 1;
      } else if (arg === '--target') {
        if (targetFlag !== undefined) throw usageError('--target given more than once');
        targetFlag = parseTargetValue(flagValue(rest, i, '--target'));
        i += 1;
      } else if (arg.startsWith('--')) {
        throw usageError(`lint takes no flags other than --manifest/--target: ${arg}`);
      } else {
        positional.push(arg);
      }
    }
    if (manifestFlag !== undefined || targetFlag !== undefined) {
      if (manifestFlag === undefined || targetFlag === undefined) {
        throw usageError(
          'lint --manifest requires --target app, and --target app requires --manifest',
        );
      }
      if (positional.length > 0) {
        throw usageError('lint --manifest takes no directory argument');
      }
      return { command, manifestPath: manifestFlag, target: TARGET_APP };
    }
    if (positional.length > 1) throw usageError('lint takes at most one directory argument');
    return { command, dir: positional[0] ?? 'apps' };
  }

  if (command !== 'plan' && command !== 'apply' && command !== 'verify') {
    throw usageError(`unknown command: ${command ?? '(none)'}`);
  }

  const positional = [];
  let envName;
  let prune = false;
  let resendSecrets = false;
  let manifestFlag;
  let targetFlag;
  for (let i = 0; i < rest.length; i += 1) {
    const arg = rest[i];
    if (arg === '--env') {
      const value = rest[i + 1];
      if (value === undefined) throw usageError('--env requires a value');
      envName = value;
      i += 1;
    } else if (arg === '--manifest') {
      if (manifestFlag !== undefined) throw usageError('--manifest given more than once');
      manifestFlag = flagValue(rest, i, '--manifest');
      i += 1;
    } else if (arg === '--target') {
      if (targetFlag !== undefined) throw usageError('--target given more than once');
      targetFlag = parseTargetValue(flagValue(rest, i, '--target'));
      i += 1;
    } else if (arg === '--prune') {
      prune = true;
    } else if (arg === '--resend-secrets') {
      resendSecrets = true;
    } else if (arg.startsWith('--')) {
      throw usageError(`unknown flag: ${arg}`);
    } else {
      positional.push(arg);
    }
  }
  if ((prune || resendSecrets) && command === 'verify') {
    throw usageError('--prune/--resend-secrets are not valid with verify');
  }

  if (targetFlag === TARGET_APP) {
    if (manifestFlag === undefined) throw usageError('--target app requires --manifest <path>');
    if (positional.length > 0) {
      throw usageError(
        'with --target app pass the manifest as --manifest <path>, not a positional',
      );
    }
    return {
      command,
      manifestPath: manifestFlag,
      envName,
      prune,
      resendSecrets,
      target: TARGET_APP,
    };
  }
  if (manifestFlag !== undefined) throw usageError('--manifest requires --target app');
  if (positional.length !== 1) {
    throw usageError(`${command} requires exactly one manifest path`);
  }
  return {
    command,
    manifestPath: positional[0],
    envName,
    prune,
    resendSecrets,
    target: TARGET_OPERATOR,
  };
}

function readManifest(manifestPath) {
  const text = readFileSync(manifestPath, 'utf8');
  return JSON.parse(text);
}

function manifestKind(manifest) {
  // A null or non-object manifest is not a platform manifest; validateManifest rejects it later
  // with its own friendly message.
  const isObject = manifest !== null && typeof manifest === 'object';
  return isObject && Object.prototype.hasOwnProperty.call(manifest, 'platform')
    ? 'platform'
    : 'app';
}

/** Fail closed on the target: only `operator`, `app` or unset (direct callers, operator) pass. */
function assertTarget(target) {
  if (target !== undefined && target !== TARGET_OPERATOR && target !== TARGET_APP) {
    throw new Error(`unknown onboard target: ${String(target)}`);
  }
}

/**
 * Every `app_id` found across every `.json` file in `dir`, best-effort: a sibling whose read,
 * `JSON.parse` or `app_id` extraction fails is silently excluded (no error raised), per
 * Contracts -> Desired state and diff -> Ownership. This is independent of the Prune lint gate,
 * which still fails closed on a broken sibling whenever `--prune` is set.
 */
function collectKnownAppIds(dir) {
  const ids = new Set();
  let entries;
  try {
    entries = readdirSync(dir);
  } catch {
    return ids;
  }
  for (const entry of entries) {
    if (!entry.endsWith('.json')) continue;
    try {
      const text = readFileSync(path.join(dir, entry), 'utf8');
      const parsed = JSON.parse(text);
      if (typeof parsed.app_id === 'string' && NAME_PATTERN.test(parsed.app_id)) {
        ids.add(parsed.app_id);
      }
    } catch {
      // Best-effort: this sibling is excluded from knownAppIds for this run, no error raised.
    }
  }
  return ids;
}

function buildScope(manifest, knownAppIds) {
  return manifestKind(manifest) === 'platform'
    ? { kind: 'platform', knownAppIds }
    : { kind: 'app', appId: manifest.app_id, knownAppIds };
}

function printLintErrors(errors) {
  for (const line of errors) console.log(line);
}

/** Prints each `resolveManifest` notice (e.g. `env <name>: no overrides`) once, through the same
 * stdout channel as the rest of plan/apply/verify's operator-facing output. Never a secret value
 * — a notice only ever carries an environment name, computed in `resolveManifest`. */
function printNotices(notices = []) {
  for (const notice of notices) console.log(notice);
}

/**
 * `lint --manifest <path> --target app`: the single-file schema check only (`validateManifest`).
 * It deliberately never calls `lintDirectory`: an app repo cannot see other apps' manifests, so
 * the cross-manifest checks (app_id prefix collisions, duplicate owned-destination names,
 * unresolved `use` references) do not apply, and the server enforces cross-app ownership. The
 * file may have any name. Never reads env values and never makes a network call.
 */
function lintManifestFile(manifestPath) {
  let manifest;
  try {
    manifest = readManifest(manifestPath);
  } catch (err) {
    console.log(`${manifestPath}: file: unreadable or invalid JSON (${err.message})`);
    process.exitCode = 1;
    return;
  }
  const { kind, errors } = validateManifest(manifest);
  const lines = errors.map(({ path: jsonPath, message }) => {
    return `${manifestPath}: ${jsonPath || '(root)'}: ${message}`;
  });
  if (errors.length === 0 && kind === 'platform') {
    lines.push(
      `${manifestPath}: platform: --target app takes an app manifest; ` +
        'the platform manifest is applied by the operator',
    );
  }
  if (lines.length > 0) {
    printLintErrors(lines);
    process.exitCode = 1;
    return;
  }
  console.log('1 manifest');
}

/** Runs `lintDirectory` as the Prune lint gate. Returns `true` when it refused (caller returns). */
function runPruneLintGate(manifestPath) {
  const result = lintDirectory({ dir: path.dirname(manifestPath) });
  if (result.errors.length > 0) {
    printLintErrors(result.errors);
    process.exitCode = 1;
    return true;
  }
  return false;
}

/**
 * Logs a timestamped start/end line (with duration) around `fn`, per the stronghold logging
 * rule: enough to reproduce a failure from the logs alone. Never logs request bodies — only the
 * phase label, which callers build from action types/targets, never from a `body`.
 */
async function logPhase(label, fn) {
  const start = Date.now();
  console.error(`${label} ...`);
  try {
    const result = await fn();
    console.error(`${label} ... done in ${Date.now() - start}ms`);
    return result;
  } catch (err) {
    console.error(`${label} ... failed in ${Date.now() - start}ms: ${err.message}`);
    throw err;
  }
}

async function fetchServerState(adminApi) {
  const [destinations, routes, checks] = await Promise.all([
    adminApi.listDestinations(),
    adminApi.listRoutes(),
    adminApi.listChecks(),
  ]);
  return { destinations, routes, checks };
}

/**
 * Builds the desired state and ownership scope shared by `runPlan`/`runApply`/`runVerify`:
 * `resolveManifest` runs first (its `validateManifest` error is the one invalid-manifest exit
 * all three commands share), then `knownAppIds` is collected from every manifest in the given
 * manifest's own directory, per Contracts -> Desired state and diff -> Ownership.
 */
function loadDesiredAndScope(manifest, manifestPath, options, env) {
  const desired = resolveManifest(manifest, { env, envName: options.envName });
  if (options.target === TARGET_APP) {
    // The app-scoped API only ever returns the app's own rows, and the app repo has no view of any
    // other app's manifest, so ownership is just "this app": no directory scan.
    const scope = buildScope(manifest, new Set([manifest.app_id]));
    // `use` references name operator-owned destinations, which the app-scoped list never
    // includes, so absence from the listing proves nothing. The server checks every link when a
    // route or check is written (an unknown or foreign destination is a 400), so the client-side
    // `missing-shared` block is dropped here.
    return { desired: { ...desired, shared: [] }, scope, sharedNames: desired.shared };
  }
  const knownAppIds = collectKnownAppIds(path.dirname(manifestPath));
  const scope = buildScope(manifest, knownAppIds);
  return { desired, scope, sharedNames: [] };
}

/** Manual steps printed at the end of `plan` and `apply`. Never a resolved secret value. */
function formatManualSteps(manifest, { target = TARGET_OPERATOR } = {}) {
  if (manifestKind(manifest) === 'platform') {
    return 'Manual steps: none for the platform manifest.';
  }
  const appId = manifest.app_id;
  // An app running `--target app` from its own deploy already has its Access token and key, so
  // only the operator-mode output walks through creating them.
  const lines = ['Manual steps:'];
  let step = 1;
  if (target === TARGET_OPERATOR) {
    lines.push(
      `  1. Create the Cloudflare Access service token "${appId}" and add it to the whole-host`,
      '     notifs-delivery Access policy (never the /v1/admin app).',
      `  2. Add NOTIFS_ID / NOTIFS_SECRET to ${appId}'s own repository secrets.`,
    );
    step = 3;
  }
  const destinations = Array.isArray(manifest.destinations) ? manifest.destinations : [];
  const discordDestinations = destinations.filter((entry) => entry.type === 'discord');
  const webhookDestinations = destinations.filter((entry) => entry.type === 'webhook');
  const heartbeats = Array.isArray(manifest.heartbeats) ? manifest.heartbeats : [];

  if (discordDestinations.length > 0) {
    lines.push(
      `  ${step}. Create the Discord webhook for each of these destinations and set its env var:`,
    );
    for (const entry of discordDestinations) {
      const envVar = entry.secrets?.webhook_url?.env;
      lines.push(`       - "${entry.name}" -> ${envVar}`);
    }
    step += 1;
  }
  if (webhookDestinations.length > 0) {
    lines.push(`  ${step}. Give each of these webhook destinations its signing secret:`);
    for (const entry of webhookDestinations) {
      const envVar = entry.secrets?.signing_secret?.env;
      lines.push(`       - "${entry.name}" -> ${envVar}`);
    }
    step += 1;
  }
  if (heartbeats.length > 0) {
    lines.push(
      `  ${step}. ${appId} must ping POST /v1/heartbeats/${appId}/<name> every <period>, for each heartbeat:`,
    );
    for (const heartbeat of heartbeats) {
      lines.push(`       - ${heartbeat.name} every ${heartbeat.period}`);
    }
    step += 1;
  }
  if (lines.length === 1) return 'Manual steps: none.';
  return lines.join('\n');
}

function describeAction(action) {
  return `${action.type} ${action.target}`;
}

// What an app-target failure usually means, by HTTP status. Appended after the server's own
// message (which never carries a credential), so a deploy log says what to do next.
const APP_TARGET_HINTS = new Map([
  [
    400,
    'the server refused the manifest value (for example a URL that does not resolve to public ' +
      'addresses, a name outside this app prefix, or a link to a destination this app cannot use)',
  ],
  [
    401,
    'the app key was not accepted; it may be revoked or past its 24h grace, so redeploy to mint a new one',
  ],
  [403, 'the app key does not belong to this manifest app_id'],
  [404, 'the object does not exist or is not owned by this app'],
  [
    409,
    'that name or route key is already used by an object this app does not own (another app or ' +
      'the operator); rename it in the manifest',
  ],
  [
    503,
    'delivery-api could not finish a safety check; this is usually transient, retry the deploy',
  ],
]);

// A 409 on a DELETE is a different failure: the object is still referenced.
const APP_TARGET_DELETE_CONFLICT_HINT =
  'still referenced by a route or check this app cannot see; remove the reference first';

// The delivery-api's own auth failure texts. A 401/403 whose message is anything else (for
// example `HTTP 403` from a non-JSON Cloudflare Access refusal) did not come from the API's key
// check, so the app-key hints would blame the wrong credential.
const SERVER_AUTH_MESSAGES = new Set([
  'invalid app key',
  'app key required',
  'app_id does not match app key',
]);

const APP_TARGET_NEUTRAL_AUTH_HINT =
  'the app key or the Cloudflare Access token (ONBOARD_APP_ACCESS_ID/SECRET) was refused';

function appTargetHint(err) {
  if (err.status === 409 && err.method === 'DELETE') return APP_TARGET_DELETE_CONFLICT_HINT;
  if ((err.status === 401 || err.status === 403) && !SERVER_AUTH_MESSAGES.has(err.message)) {
    return APP_TARGET_NEUTRAL_AUTH_HINT;
  }
  return APP_TARGET_HINTS.get(err.status);
}

function formatApiErrorMessage(err, target = TARGET_OPERATOR) {
  if (!(err instanceof ApiError)) return err.message;
  const base = `${err.status} ${err.message}`;
  const hint = target === TARGET_APP ? appTargetHint(err) : undefined;
  return hint ? `${base} (${hint})` : base;
}

/** App target only: runs `fn`, rethrowing an `ApiError` from it as a plain `Error` carrying the
 * formatted status, message and hint, so a failed listing reads the same as a failed write.
 * Operator mode is passed through untouched. */
async function explainApiErrors(target, fn) {
  if (target !== TARGET_APP) return fn();
  try {
    return await fn();
  } catch (err) {
    if (err instanceof ApiError) throw new Error(formatApiErrorMessage(err, target));
    throw err;
  }
}

/** Splits a `routeTarget`/`checkTarget` string ("a/b") back into its two parts. Neither an
 * `app_pattern`/`app_id` nor a `channel_pattern`/check `name` can contain `/` (see the manifest
 * schema's name patterns), so the first `/` is always the right split point. */
function splitTarget(target) {
  const sep = target.indexOf('/');
  return [target.slice(0, sep), target.slice(sep + 1)];
}

function findRouteId(server, target) {
  const [appPattern, channelPattern] = splitTarget(target);
  return server.routes.find(
    (r) => r.app_pattern === appPattern && r.channel_pattern === channelPattern,
  )?.id;
}

function findCheckId(server, target) {
  const [appId, name] = splitTarget(target);
  return server.checks.find((c) => c.app_id === appId && c.name === name)?.id;
}

/**
 * Executes one `computePlan` action against the management API. `server` is the listing taken
 * immediately before `computePlan` ran, used only to resolve a route/check's numeric id for an
 * update/delete (destinations are keyed by name, so they need no id lookup). A `409` on create
 * re-lists that resource, finds it by its conflict key, and PATCHes it instead — the destination
 * recovery PATCH always carries the full manifest `secrets`/`config`, independent of
 * `--resend-secrets`, per Contracts -> Apply and verify behaviour.
 */
async function executeAction(action, adminApi, server, target = TARGET_OPERATOR) {
  try {
    await runAction(action, adminApi, server);
  } catch (err) {
    // App target only: DELETE of an absent own row is a 404 there (the operator DELETE is
    // idempotent), so an object another deploy removed first counts as already deleted. The
    // post-apply re-list still proves convergence.
    const alreadyGone =
      target === TARGET_APP &&
      action.type.startsWith('delete-') &&
      err instanceof ApiError &&
      err.status === 404;
    if (!alreadyGone) throw err;
    console.error(`${describeAction(action)}: already deleted (404), continuing`);
  }
}

async function runAction(action, adminApi, server) {
  switch (action.type) {
    case 'create-destination':
      try {
        await adminApi.createDestination(action.body);
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) {
          const existing = (await adminApi.listDestinations()).find(
            (d) => d.name === action.target,
          );
          if (!existing) throw err;
          await adminApi.patchDestination(action.target, {
            enabled: action.body.enabled,
            config: { ...action.body.config },
            secrets: { ...action.body.secrets },
          });
          return;
        }
        throw err;
      }
      return;
    case 'update-destination':
      await adminApi.patchDestination(action.target, action.body);
      return;
    case 'create-route':
      try {
        await adminApi.createRoute(action.body);
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) {
          const routes = await adminApi.listRoutes();
          const existing = routes.find(
            (r) =>
              r.app_pattern === action.body.app_pattern &&
              r.channel_pattern === action.body.channel_pattern,
          );
          if (!existing) throw err;
          await adminApi.patchRoute(existing.id, {
            destinations: action.body.destinations,
            enabled: action.body.enabled,
          });
          return;
        }
        throw err;
      }
      return;
    case 'update-route': {
      const id = findRouteId(server, action.target);
      if (id === undefined) throw new Error(`update-route: no server id for ${action.target}`);
      await adminApi.patchRoute(id, action.body);
      return;
    }
    case 'create-check':
      try {
        await adminApi.createCheck(action.body);
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) {
          const checks = await adminApi.listChecks();
          const existing = checks.find(
            (c) => c.app_id === action.body.app_id && c.name === action.body.name,
          );
          if (!existing) throw err;
          await adminApi.patchCheck(existing.id, {
            period_seconds: action.body.period_seconds,
            grace_seconds: action.body.grace_seconds,
            destinations: action.body.destinations,
          });
          return;
        }
        throw err;
      }
      return;
    case 'update-check': {
      const id = findCheckId(server, action.target);
      if (id === undefined) throw new Error(`update-check: no server id for ${action.target}`);
      await adminApi.patchCheck(id, action.body);
      return;
    }
    case 'delete-check': {
      const id = findCheckId(server, action.target);
      if (id === undefined) throw new Error(`delete-check: no server id for ${action.target}`);
      await adminApi.deleteCheck(id);
      return;
    }
    case 'delete-route': {
      const id = findRouteId(server, action.target);
      if (id === undefined) throw new Error(`delete-route: no server id for ${action.target}`);
      await adminApi.deleteRoute(id);
      return;
    }
    case 'delete-destination':
      await adminApi.deleteDestination(action.target);
      return;
    default:
      throw new Error(`apply: unexpected action type ${action.type}`);
  }
}

const APP_KEY_PATTERN = /^dak_[A-Za-z0-9_-]{43}$/;

/**
 * The app delivery key (`dak_...`) from `ONBOARD_APP_KEY`, trimmed. Fail closed: a missing or blank
 * value throws `missingMessage` before any manifest is resolved, any target resolved or any request
 * made. A key that is not the exact `dak_` + 43 URL-safe characters shape is rejected with a fixed
 * message: the key is never echoed, not even partially, and a value with an embedded newline never
 * reaches a request header (where `fetch` would quote it in its error).
 */
function requireAppKey(env, missingMessage) {
  const appKey = (env.ONBOARD_APP_KEY ?? '').trim();
  if (!appKey) throw new Error(missingMessage);
  if (!APP_KEY_PATTERN.test(appKey)) {
    throw new Error(
      'ONBOARD_APP_KEY is not a valid app delivery key ' +
        '(expected dak_ followed by 43 URL-safe characters)',
    );
  }
  return appKey;
}

const APP_TARGET_KEY_MISSING =
  'ONBOARD_APP_KEY is required for --target app: the app delivery key (dak_...) that the ' +
  "notifs-app action's identity exchange issues on the app's own deploy";

/** `--target app` takes an app manifest only: the `_platform` manifest stays operator-applied. */
function refuseAppTargetPlatform(manifest) {
  if (manifestKind(manifest) === 'platform') {
    throw new Error(
      '--target app takes an app manifest; the platform manifest is applied by the operator',
    );
  }
}

/**
 * The single construction site of the management API: the operator's `/v1/admin` client by
 * default, the app-scoped `/v1/apps/{app_id}` client (with the app's Access token and delivery
 * key) under `--target app`. `appKey` is only read for the app target.
 */
function buildManagementApi({ deps, options, manifest, timeoutMs, appKey }) {
  if (options.target === TARGET_APP) {
    const target = deps.resolveTarget({
      env: deps.env,
      idVar: 'ONBOARD_APP_ACCESS_ID',
      secretVar: 'ONBOARD_APP_ACCESS_SECRET',
    });
    const http = deps.createHttp({
      baseUrl: target.baseUrl,
      credentials: target.credentials,
      appKey,
      fetchImpl: deps.fetchImpl,
      timeoutMs,
    });
    return deps.createAppScopedApi(http, manifest.app_id);
  }
  const target = deps.resolveTarget({ env: deps.env });
  const http = deps.createHttp({
    baseUrl: target.baseUrl,
    credentials: target.credentials,
    fetchImpl: deps.fetchImpl,
    timeoutMs,
  });
  return deps.createAdminApi(http);
}

/** Prints one line naming the `use` references an app-target run cannot check client-side. */
function printSharedNotice(sharedNames) {
  if (sharedNames.length === 0) return;
  console.log(
    `shared destinations (${sharedNames.join(', ')}): operator-owned, not listed to apps; ` +
      'the server checks each link when the route or check is saved',
  );
}

/** `plan` always exits `0` — it is purely informational; the operator reads any printed
 * `! blocking` line directly and decides whether to run `apply`. */
export async function runPlan({ manifest, manifestPath, options, deps }) {
  assertTarget(options.target);
  const appKey = prepareAppTarget(manifest, options, deps);
  // `--prune`'s lint gate needs every app's manifest in one directory; an app repo has only its
  // own, and the server scopes a prune to the app's own objects anyway.
  if (options.target !== TARGET_APP && options.prune && runPruneLintGate(manifestPath)) return;

  const { desired, scope, sharedNames } = loadDesiredAndScope(
    manifest,
    manifestPath,
    options,
    deps.env,
  );
  printNotices(desired.notices);
  printSharedNotice(sharedNames);
  const api = buildManagementApi({
    deps,
    options,
    manifest,
    timeoutMs: PLAN_APPLY_TIMEOUT_MS,
    appKey,
  });
  const server = await explainApiErrors(options.target, () =>
    logPhase('plan: listing 3 resources', () => fetchServerState(api)),
  );
  const actions = computePlan(desired, server, scope, {
    prune: options.prune,
    resendSecrets: options.resendSecrets,
  });
  console.log(formatPlan(actions));
  console.log(formatManualSteps(manifest, { target: options.target }));
}

/**
 * App target only: refuses a platform manifest and returns the app key, failing closed on a
 * missing one before any manifest is resolved, any target resolved or any request made. The
 * operator target needs no key here and gets `undefined`.
 */
function prepareAppTarget(manifest, options, deps) {
  if (options.target !== TARGET_APP) return undefined;
  const appKey = requireAppKey(deps.env, APP_TARGET_KEY_MISSING);
  refuseAppTargetPlatform(manifest);
  return appKey;
}

export async function runApply({ manifest, manifestPath, options, deps }) {
  assertTarget(options.target);
  const appKey = prepareAppTarget(manifest, options, deps);
  if (options.target !== TARGET_APP && options.prune && runPruneLintGate(manifestPath)) return;

  const { desired, scope, sharedNames } = loadDesiredAndScope(
    manifest,
    manifestPath,
    options,
    deps.env,
  );
  printNotices(desired.notices);
  printSharedNotice(sharedNames);
  const adminApi = buildManagementApi({
    deps,
    options,
    manifest,
    timeoutMs: PLAN_APPLY_TIMEOUT_MS,
    appKey,
  });
  const server = await explainApiErrors(options.target, () =>
    logPhase('apply: listing 3 resources', () => fetchServerState(adminApi)),
  );
  const actions = computePlan(desired, server, scope, {
    prune: options.prune,
    resendSecrets: options.resendSecrets,
  });
  console.log(formatPlan(actions));

  const blocking = actions.filter(
    (a) => a.type === 'replace-required' || a.type === 'missing-shared',
  );
  if (blocking.length > 0) {
    console.error(`apply: refusing — ${blocking.length} blocking action(s)`);
    process.exitCode = 1;
    return;
  }

  const executable = actions.filter((a) => !a.type.startsWith('unmanaged-'));
  const completed = [];
  for (const action of executable) {
    try {
      await logPhase(`apply: ${describeAction(action)}`, () =>
        executeAction(action, adminApi, server, options.target),
      );
      completed.push(action);
    } catch (err) {
      if (!(err instanceof ApiError)) throw err;
      const notAttempted = executable.length - completed.length - 1;
      console.error(
        `failed: ${describeAction(action)}: ${formatApiErrorMessage(err, options.target)}`,
      );
      console.error(`completed: ${completed.length}, failed: 1, not attempted: ${notAttempted}`);
      process.exitCode = 1;
      return;
    }
  }

  const server2 = await explainApiErrors(options.target, () =>
    logPhase('apply: re-listing 3 resources', () => fetchServerState(adminApi)),
  );
  const residual = computePlan(desired, server2, scope, {
    prune: options.prune,
    resendSecrets: false,
  }).filter((a) => !a.type.startsWith('unmanaged-'));
  if (residual.length > 0) {
    console.error('apply did not converge');
    console.error(formatPlan(residual));
    process.exitCode = 1;
    return;
  }

  console.log(formatManualSteps(manifest, { target: options.target }));
}

export async function runVerify({ manifest, manifestPath, options, deps }) {
  assertTarget(options.target);
  if (manifestKind(manifest) === 'platform') {
    console.error('verify needs an app manifest');
    process.exitCode = 1;
    return;
  }
  // Every app-facing endpoint verify calls requires the app's delivery key. This is fail-closed
  // and checked before anything else: no manifest is resolved, no target is resolved and no
  // request is made. For an operator-run verify no production mechanism supplies the key: it is
  // set by the dev-mode token-exchange test harness only. `--target app` is the production path:
  // the app's own deploy verifies with the key its identity exchange just issued.
  const appKey = requireAppKey(
    deps.env,
    options.target === TARGET_APP
      ? APP_TARGET_KEY_MISSING
      : 'ONBOARD_APP_KEY is required for verify: the app delivery key (dak_...), supplied by the ' +
          'dev/test token-exchange harness only; an app verifies itself from its own deploy',
  );

  const desired = resolveManifest(manifest, { env: deps.env, envName: options.envName });
  printNotices(desired.notices);

  const target = deps.resolveTarget({
    env: deps.env,
    idVar: 'ONBOARD_APP_ACCESS_ID',
    secretVar: 'ONBOARD_APP_ACCESS_SECRET',
  });
  const http = deps.createHttp({
    baseUrl: target.baseUrl,
    credentials: target.credentials,
    appKey,
    fetchImpl: deps.fetchImpl,
    timeoutMs: VERIFY_TIMEOUT_MS,
  });
  const appApi = deps.createAppApi(http);

  const appId = manifest.app_id;
  let overallPassed = true;
  const pinged = [];

  for (const route of desired.routes) {
    if (route.channel_pattern.endsWith('*')) {
      console.log(`${route.channel_pattern}: skipped (pattern route)`);
      continue;
    }
    const idempotencyKey = `onboard-verify-${route.channel_pattern}-${deps.now().toISOString()}`;
    try {
      const payload = await logPhase(`verify: sending to ${route.channel_pattern}`, () =>
        appApi.sendMessage({
          app_id: appId,
          channel: route.channel_pattern,
          idempotency_key: idempotencyKey,
          payload: { source: 'onboard-verify' },
          display: {
            title: `${appId} onboarding check`,
            body: `Test message from scripts/onboard.mjs verify (channel ${route.channel_pattern}).`,
          },
        }),
      );
      // POST /v1/messages wraps the submission under `message` (see
      // web/src/api/deliveryClient.ts's MessageWire/`sendTest`); `payload.deliveries` is always
      // undefined, never the real array.
      const deliveries = payload?.message?.deliveries ?? [];
      const passed = deliveries.length > 0 && deliveries.every((d) => d.state === 'delivered');
      if (passed) {
        console.log(`${route.channel_pattern}: pass`);
      } else {
        overallPassed = false;
        if (deliveries.length === 0) {
          console.log(`${route.channel_pattern}: fail: no deliveries`);
        } else {
          for (const delivery of deliveries) {
            console.log(
              `${route.channel_pattern}: fail: ${delivery.destination ?? '(destination)'} ` +
                `state=${delivery.state} dead_reason=${delivery.dead_reason ?? ''} ` +
                `last_status_code=${delivery.last_status_code ?? ''}`,
            );
          }
        }
      }
    } catch (err) {
      overallPassed = false;
      if (err instanceof ApiError) {
        console.log(
          `${route.channel_pattern}: failed: ${formatApiErrorMessage(err, options.target)}`,
        );
      } else {
        throw err;
      }
    }
  }

  for (const check of desired.checks) {
    try {
      const result = await logPhase(`verify: pinging ${check.name}`, () =>
        appApi.pingCheck(appId, check.name),
      );
      pinged.push(check);
      if (result?.state === 'up') {
        console.log(`${check.name}: pass`);
      } else {
        overallPassed = false;
        console.log(`${check.name}: fail: unexpected state ${result?.state}`);
      }
    } catch (err) {
      overallPassed = false;
      if (err instanceof ApiError) {
        console.log(`${check.name}: failed: ${formatApiErrorMessage(err, options.target)}`);
      } else {
        throw err;
      }
    }
  }

  for (const check of pinged) {
    const downAt = new Date(
      deps.now().getTime() + (check.period_seconds + check.grace_seconds) * 1000,
    ).toISOString();
    console.log(
      `warning: pinged ${check.name}; it goes down at ${downAt} unless ${appId} pings it`,
    );
  }

  if (!overallPassed) process.exitCode = 1;
}

/**
 * `main(argv, deps)` — the CLI entry. `deps` overrides (all optional): `env`, `now`,
 * `fetchImpl`, `resolveTarget`, `createHttp`, `createAdminApi`, `createAppScopedApi`,
 * `createAppApi`. Argument parsing (`parseArgs`) always runs first and never touches `fs` or
 * `deps`, so every usage error (unknown command, unknown flag, `--prune`/`--resend-secrets` on
 * `verify`/`lint`) throws before any manifest is read, any target resolved, or any request made.
 */
export async function main(argv, deps = {}) {
  const merged = {
    env: process.env,
    now: () => new Date(),
    fetchImpl: fetch,
    resolveTarget,
    createHttp,
    createAdminApi,
    createAppScopedApi,
    createAppApi,
    ...deps,
  };

  const parsed = parseArgs(argv);

  if (parsed.command === 'lint' && parsed.manifestPath !== undefined) {
    lintManifestFile(parsed.manifestPath);
    return;
  }

  if (parsed.command === 'lint') {
    const result = lintDirectory({ dir: parsed.dir });
    if (result.errors.length > 0) {
      printLintErrors(result.errors);
      process.exitCode = 1;
    } else {
      console.log(`${result.count} manifests`);
    }
    return;
  }

  const manifest = readManifest(parsed.manifestPath);
  const options = {
    envName: parsed.envName,
    prune: parsed.prune,
    resendSecrets: parsed.resendSecrets,
    target: parsed.target,
  };
  const ctx = { manifest, manifestPath: parsed.manifestPath, options, deps: merged };

  if (parsed.command === 'plan') {
    await runPlan(ctx);
  } else if (parsed.command === 'apply') {
    await runApply(ctx);
  } else {
    await runVerify(ctx);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((err) => {
    console.error(`onboard: ${err.message}`);
    process.exitCode = 1;
  });
}

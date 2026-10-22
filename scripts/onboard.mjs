// CLI entry point for declarative app onboarding: wires the offline manifest/lint module
// (`onboard-manifest.mjs`), the pluggable management target/HTTP client (`onboard-client.mjs`)
// and the pure desired-state diff engine (`onboard-plan.mjs`) into the four operator commands:
//
//   node scripts/onboard.mjs plan   <manifest path> [--env <name>] [--prune] [--resend-secrets]
//   node scripts/onboard.mjs apply  <manifest path> [--env <name>] [--prune] [--resend-secrets]
//   node scripts/onboard.mjs verify <manifest path> [--env <name>]
//   node scripts/onboard.mjs lint   [dir=apps]
//
// `main(argv, deps)` is the one thing tests drive end-to-end: `deps` lets a test swap in a
// stateful fake management/app API (via `createAdminApi`/`createAppApi`) and a spying
// `resolveTarget`/`createHttp`, while manifests and lint fixtures are read from a real temp
// directory the same way `onboard-manifest.test.mjs` does.
import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { NAME_PATTERN, lintDirectory, resolveManifest } from './onboard-manifest.mjs';
import {
  ApiError,
  createAdminApi,
  createAppApi,
  createHttp,
  resolveTarget,
} from './onboard-client.mjs';
import { computePlan, formatPlan } from './onboard-plan.mjs';

const PLAN_APPLY_TIMEOUT_MS = 15000;
const VERIFY_TIMEOUT_MS = 25000;

const USAGE =
  'usage: node scripts/onboard.mjs <plan|apply|verify> <manifest path> ' +
  '[--env <name>] [--prune] [--resend-secrets] | node scripts/onboard.mjs lint [dir=apps]';

function usageError(message) {
  return new Error(message ? `${message}\n${USAGE}` : USAGE);
}

/**
 * Parses argv into `{ command, manifestPath, envName, prune, resendSecrets }` (plan/apply/verify)
 * or `{ command: 'lint', dir }`. Every rejection here is a usage error thrown before any manifest
 * is read or any target resolved — this function touches neither `fs` nor `deps`.
 */
export function parseArgs(argv) {
  const [command, ...rest] = argv;

  if (command === 'lint') {
    const positional = [];
    for (const arg of rest) {
      if (arg.startsWith('--')) throw usageError(`lint takes no flags: ${arg}`);
      positional.push(arg);
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
  for (let i = 0; i < rest.length; i += 1) {
    const arg = rest[i];
    if (arg === '--env') {
      const value = rest[i + 1];
      if (value === undefined) throw usageError('--env requires a value');
      envName = value;
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
  if (positional.length !== 1) {
    throw usageError(`${command} requires exactly one manifest path`);
  }
  if ((prune || resendSecrets) && command === 'verify') {
    throw usageError('--prune/--resend-secrets are not valid with verify');
  }
  return { command, manifestPath: positional[0], envName, prune, resendSecrets };
}

function readManifest(manifestPath) {
  const text = readFileSync(manifestPath, 'utf8');
  return JSON.parse(text);
}

function manifestKind(manifest) {
  return Object.prototype.hasOwnProperty.call(manifest, 'platform') ? 'platform' : 'app';
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
  const knownAppIds = collectKnownAppIds(path.dirname(manifestPath));
  const scope = buildScope(manifest, knownAppIds);
  return { desired, scope };
}

/** Manual steps printed at the end of `plan` and `apply`. Never a resolved secret value. */
function formatManualSteps(manifest) {
  if (manifestKind(manifest) === 'platform') {
    return 'Manual steps: none for the platform manifest.';
  }
  const appId = manifest.app_id;
  const lines = [
    'Manual steps:',
    `  1. Create the Cloudflare Access service token "${appId}" and add it to the whole-host`,
    '     notifs-delivery Access policy (never the /v1/admin app).',
    `  2. Add NOTIFS_ID / NOTIFS_SECRET to ${appId}'s own repository secrets.`,
  ];
  const destinations = Array.isArray(manifest.destinations) ? manifest.destinations : [];
  const discordDestinations = destinations.filter((entry) => entry.type === 'discord');
  const webhookDestinations = destinations.filter((entry) => entry.type === 'webhook');
  const heartbeats = Array.isArray(manifest.heartbeats) ? manifest.heartbeats : [];

  let step = 3;
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
  return lines.join('\n');
}

function describeAction(action) {
  return `${action.type} ${action.target}`;
}

function formatApiErrorMessage(err) {
  return err instanceof ApiError ? `${err.status} ${err.message}` : err.message;
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
async function executeAction(action, adminApi, server) {
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

function buildAdminApi(deps, timeoutMs) {
  const target = deps.resolveTarget({ env: deps.env });
  const http = deps.createHttp({
    baseUrl: target.baseUrl,
    credentials: target.credentials,
    fetchImpl: deps.fetchImpl,
    timeoutMs,
  });
  return deps.createAdminApi(http);
}

/** `plan` always exits `0` — it is purely informational; the operator reads any printed
 * `! blocking` line directly and decides whether to run `apply`. */
export async function runPlan({ manifest, manifestPath, options, deps }) {
  if (options.prune && runPruneLintGate(manifestPath)) return;

  const { desired, scope } = loadDesiredAndScope(manifest, manifestPath, options, deps.env);
  printNotices(desired.notices);
  const adminApi = buildAdminApi(deps, PLAN_APPLY_TIMEOUT_MS);
  const server = await logPhase('plan: listing 3 resources', () => fetchServerState(adminApi));
  const actions = computePlan(desired, server, scope, {
    prune: options.prune,
    resendSecrets: options.resendSecrets,
  });
  console.log(formatPlan(actions));
  console.log(formatManualSteps(manifest));
}

export async function runApply({ manifest, manifestPath, options, deps }) {
  if (options.prune && runPruneLintGate(manifestPath)) return;

  const { desired, scope } = loadDesiredAndScope(manifest, manifestPath, options, deps.env);
  printNotices(desired.notices);
  const adminApi = buildAdminApi(deps, PLAN_APPLY_TIMEOUT_MS);
  const server = await logPhase('apply: listing 3 resources', () => fetchServerState(adminApi));
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
        executeAction(action, adminApi, server),
      );
      completed.push(action);
    } catch (err) {
      if (!(err instanceof ApiError)) throw err;
      const notAttempted = executable.length - completed.length - 1;
      console.error(`failed: ${describeAction(action)}: ${formatApiErrorMessage(err)}`);
      console.error(`completed: ${completed.length}, failed: 1, not attempted: ${notAttempted}`);
      process.exitCode = 1;
      return;
    }
  }

  const server2 = await logPhase('apply: re-listing 3 resources', () => fetchServerState(adminApi));
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

  console.log(formatManualSteps(manifest));
}

export async function runVerify({ manifest, manifestPath, options, deps }) {
  if (manifestKind(manifest) === 'platform') {
    console.error('verify needs an app manifest');
    process.exitCode = 1;
    return;
  }
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
        console.log(`${route.channel_pattern}: failed: ${err.status} ${err.message}`);
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
        console.log(`${check.name}: failed: ${err.status} ${err.message}`);
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
 * `fetchImpl`, `resolveTarget`, `createHttp`, `createAdminApi`, `createAppApi`. Argument parsing
 * (`parseArgs`) always runs first and never touches `fs` or `deps`, so every usage error
 * (unknown command, unknown flag, `--prune`/`--resend-secrets` on `verify`/`lint`) throws before
 * any manifest is read, any target resolved, or any request made.
 */
export async function main(argv, deps = {}) {
  const merged = {
    env: process.env,
    now: () => new Date(),
    fetchImpl: fetch,
    resolveTarget,
    createHttp,
    createAdminApi,
    createAppApi,
    ...deps,
  };

  const parsed = parseArgs(argv);

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

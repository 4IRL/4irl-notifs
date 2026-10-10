// Standalone worktree lifecycle for 4irl-notifs (also the owned `worktree-new` /
// `worktree-rm` targets that the stronghold's wt.sh dispatches to).
//
//   WT_NAME=<slug> [WT_BRANCH=<b>] [WT_BASE=<ref>] node scripts/worktree.mjs new   (run from the primary)
//   node scripts/worktree.mjs rm                                                   (run inside the worktree)
//
// Every git/docker call goes through execFileSync with an argument array (no shell), so branch
// names and paths are always data. Git and docker runners are injectable for the unit tests.

import { execFileSync } from 'node:child_process';
import {
  appendFileSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  readdirSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { formatEnvFile, parseEnvFile, probePort, resolvePorts } from './ports.mjs';

const REPO_NAME = '4irl-notifs';
const PROJECT_PREFIX = `${REPO_NAME}-`;
const LOCK_NAME = 'notifs-worktree-slot.lock';
const LOCK_GRACE_MS = 5000;
const EXCLUDES = ['/.worktree.env', '/.dev/'];
const NPM_DIRS = ['web', 'person-service'];

function runGit(cwd, args) {
  return execFileSync('git', args, {
    cwd,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();
}

function gitOk(git, cwd, args) {
  try {
    git(cwd, args);
    return true;
  } catch {
    return false;
  }
}

function commonDir(git, root) {
  return git(root, ['rev-parse', '--path-format=absolute', '--git-common-dir']);
}

function worktreesDir(primaryRoot) {
  return path.join(primaryRoot, '.claude', 'worktrees');
}

/** Lowercase, non-[a-z0-9-] to '-', strip leading '-', cut to 40, strip trailing '-'. */
export function dnsSlug(value) {
  const slug = String(value)
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, '-')
    .replace(/^-+/, '')
    .slice(0, 40)
    .replace(/-+$/, '');
  if (!slug) throw new Error(`slug is empty after normalizing ${JSON.stringify(value)}`);
  return slug;
}

/** The remote's default branch (origin/HEAD), falling back to main. */
export function defaultBranch(git, cwd = '.') {
  try {
    const ref = git(cwd, ['symbolic-ref', '--short', 'refs/remotes/origin/HEAD']);
    const name = ref.replace(/^origin\//, '');
    return name || 'main';
  } catch {
    return 'main';
  }
}

/** Treat an empty string (what make passes for an omitted arg) as unset. */
export function parseWorktreeArgs(env) {
  const pick = (key) => (env[key] === undefined || env[key] === '' ? undefined : env[key]);
  return { name: pick('WT_NAME'), branch: pick('WT_BRANCH'), base: pick('WT_BASE') };
}

/** Names of all compose projects (including stopped), or null when docker is unavailable. */
export function listComposeProjects() {
  try {
    const out = execFileSync('docker', ['compose', 'ls', '-a', '--format', 'json'], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    const parsed = JSON.parse(out.trim() || '[]');
    return parsed.map((entry) => entry.Name);
  } catch {
    return null;
  }
}

/**
 * Validate a `new` request and decide how the branch is checked out. Pure validation: runs no
 * git mutation. `projectNames` is the list of existing compose project names, or null when docker
 * is unavailable (warn and skip that check). Returns
 * { slug, project, branch, base, path, mode: 'local'|'remote'|'new' }.
 */
export function planNew({
  primaryRoot,
  name,
  branch,
  base,
  git = runGit,
  projectNames,
  warn = console.warn,
}) {
  const rawName = name ?? (branch ? dnsSlug(branch) : undefined);
  if (!rawName) throw new Error('a name or branch is required');
  const slug = dnsSlug(rawName);
  const branchName = branch ?? slug;
  const project = `${PROJECT_PREFIX}${slug}`;

  if (slug === REPO_NAME || slug === dnsSlug(path.basename(primaryRoot))) {
    throw new Error(`refused slug "${slug}": it collides with the repo or primary directory name`);
  }
  const wtPath = path.join(worktreesDir(primaryRoot), slug);
  if (existsSync(wtPath)) throw new Error(`worktree ${wtPath} already exists`);

  // The explicit '-' guard stops a branch name from being parsed as a git option.
  if (
    branchName.startsWith('-') ||
    !gitOk(git, primaryRoot, ['check-ref-format', '--branch', branchName])
  ) {
    throw new Error(`invalid branch name ${JSON.stringify(branchName)}`);
  }

  if (projectNames === null || projectNames === undefined) {
    warn('worktree: docker is unavailable, skipping the compose project name collision check');
  } else if (projectNames.includes(project)) {
    throw new Error(`compose project ${project} already exists: pick another name`);
  }

  let mode = 'new';
  const baseRef = base ?? `origin/${defaultBranch(git, primaryRoot)}`;
  if (gitOk(git, primaryRoot, ['show-ref', '--verify', '--quiet', `refs/heads/${branchName}`])) {
    mode = 'local';
  } else if (
    gitOk(git, primaryRoot, [
      'show-ref',
      '--verify',
      '--quiet',
      `refs/remotes/origin/${branchName}`,
    ])
  ) {
    mode = 'remote';
  } else if (
    !gitOk(git, primaryRoot, ['rev-parse', '--verify', '--quiet', `${baseRef}^{commit}`])
  ) {
    throw new Error(
      `base ref ${baseRef} not found: run git fetch origin (or pass base=<existing ref>)`,
    );
  }

  return { slug, project, branch: branchName, base: baseRef, path: wtPath, mode };
}

/** Idempotently ignore per-worktree files via <git-common-dir>/info/exclude. */
export function ensureExcludes(commonDirPath) {
  const file = path.join(commonDirPath, 'info', 'exclude');
  mkdirSync(path.dirname(file), { recursive: true });
  const existing = existsSync(file) ? readFileSync(file, 'utf8') : '';
  const lines = new Set(existing.split('\n'));
  const missing = EXCLUDES.filter((entry) => !lines.has(entry));
  if (missing.length === 0) return;
  const prefix = existing === '' || existing.endsWith('\n') ? '' : '\n';
  appendFileSync(file, `${prefix}${missing.join('\n')}\n`);
}

function createWorktree({ git, primaryRoot, plan }) {
  mkdirSync(worktreesDir(primaryRoot), { recursive: true });
  ensureExcludes(commonDir(git, primaryRoot));
  if (plan.mode === 'local') {
    git(primaryRoot, ['worktree', 'add', plan.path, plan.branch]);
  } else if (plan.mode === 'remote') {
    git(primaryRoot, [
      'worktree',
      'add',
      '--track',
      '-b',
      plan.branch,
      plan.path,
      `origin/${plan.branch}`,
    ]);
  } else {
    git(primaryRoot, ['worktree', 'add', '--no-track', '-b', plan.branch, plan.path, plan.base]);
  }
}

/**
 * Symlink web/.dev.vars from the primary (copy web/.dev.vars.example when the primary has none);
 * never clobber a real file. Returns the warnings it emitted.
 */
export function linkDevVars({ primaryRoot, worktreeDir, warn = console.warn }) {
  const warnings = [];
  const emit = (message) => {
    warnings.push(message);
    warn(message);
  };
  const rel = path.join('web', '.dev.vars');
  const source = path.join(primaryRoot, rel);
  const dest = path.join(worktreeDir, rel);
  mkdirSync(path.dirname(dest), { recursive: true });

  let destExists = true;
  try {
    lstatSync(dest);
  } catch {
    destExists = false;
  }
  if (destExists) {
    emit(`worktree: ${rel} already exists in the worktree, leaving it alone`);
  } else if (existsSync(source)) {
    symlinkSync(source, dest);
  } else if (existsSync(`${dest}.example`)) {
    copyFileSync(`${dest}.example`, dest);
    emit(`worktree: the primary has no ${rel}, copied ${rel}.example instead`);
  } else {
    emit(`worktree: ${rel} not found in the primary and no example to copy, skipping`);
  }
  return warnings;
}

/** Atomically write <dir>/.worktree.env (plain KEY=VALUE) with mode 0600. */
export function writeWorktreeEnv({ dir, slug, primaryRoot, ports }) {
  const file = path.join(dir, '.worktree.env');
  const tmp = `${file}.tmp-${process.pid}`;
  const content = formatEnvFile({
    SLUG: slug,
    PRIMARY_ROOT: primaryRoot,
    IS_PRIMARY: 0,
    NOTIFS_SLOT: ports.NOTIFS_SLOT,
    COMPOSE_PROJECT_NAME: `${PROJECT_PREFIX}${slug}`,
    NTFY_PORT: ports.NTFY_PORT,
    API_PORT: ports.API_PORT,
    WEB_PORT: ports.WEB_PORT,
    E2E_PORT: ports.E2E_PORT,
    DELIVERY_PORT: ports.DELIVERY_PORT,
  });
  writeFileSync(tmp, content, { mode: 0o600 });
  renameSync(tmp, file);
}

/** NOTIFS_SLOT values recorded by sibling worktrees. */
export function readClaimedSlots(primaryRoot) {
  const slots = new Set();
  const dir = worktreesDir(primaryRoot);
  if (!existsSync(dir)) return slots;
  for (const entry of readdirSync(dir)) {
    const file = path.join(dir, entry, '.worktree.env');
    if (!existsSync(file)) continue;
    try {
      const slot = parseEnvFile(readFileSync(file, 'utf8')).NOTIFS_SLOT;
      if (/^\d+$/.test(slot ?? '')) slots.add(Number(slot));
    } catch {
      // unreadable env file: skip
    }
  }
  return slots;
}

function pidAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err) {
    return err.code === 'EPERM';
  }
}

/**
 * Claim the slot lock: exclusive mkdir of `lockDir` holding a pid file. A lock whose pid is dead,
 * or has no readable pid and is older than 5 s, is reclaimed by an atomic rename and retried once; a younger pid-less
 * lock is a racing holder between mkdir and its pid write, so it counts as held. Returns release().
 */
export function acquireSlotLock({ lockDir, isPidAlive = pidAlive, now = Date.now }) {
  for (let attempt = 0; attempt < 2; attempt += 1) {
    try {
      mkdirSync(lockDir);
      try {
        writeFileSync(path.join(lockDir, 'pid'), `${process.pid}\n`);
      } catch (writeErr) {
        // never leave a pid-less lock behind (it would block others for the grace period)
        rmSync(lockDir, { recursive: true, force: true });
        throw writeErr;
      }
      return () => rmSync(lockDir, { recursive: true, force: true });
    } catch (err) {
      if (err.code !== 'EEXIST') throw err;
      let pid = Number.NaN;
      try {
        pid = Number.parseInt(readFileSync(path.join(lockDir, 'pid'), 'utf8'), 10);
      } catch {
        // missing pid file: stale unless the lock is brand new
      }
      const validPid = Number.isInteger(pid) && pid > 0;
      if (validPid && isPidAlive(pid)) break;
      if (!validPid) {
        let fresh = false;
        try {
          fresh = now() - statSync(lockDir).mtimeMs < LOCK_GRACE_MS;
        } catch {
          // lock vanished: retry
        }
        if (fresh) break;
      }
      // Atomic reclaim: rename the stale dir away (only one racer can win the rename), then retry
      // the exclusive mkdir. ENOENT means another process already reclaimed it.
      const aside = `${lockDir}.stale-${process.pid}-${now()}`;
      try {
        renameSync(lockDir, aside);
        rmSync(aside, { recursive: true, force: true });
      } catch (renameErr) {
        if (renameErr.code !== 'ENOENT') throw renameErr;
      }
    }
  }
  throw new Error(
    `slot lock held (${lockDir}); if no other worktree-new is running, remove that directory and retry`,
  );
}

/**
 * The compose project to tear down for a worktree. Reads COMPOSE_PROJECT_NAME ONLY from
 * <worktreeDir>/.worktree.env (never the ambient environment) and requires it to be
 * `4irl-notifs-<dir basename>`; otherwise returns null after a warning, so the primary's live
 * stack (project `4irl-notifs`) can never be torn down.
 */
export function rmComposeProject({ worktreeDir, readFile = readFileSync, warn = console.warn }) {
  const expected = `${PROJECT_PREFIX}${path.basename(worktreeDir)}`;
  let value;
  try {
    value = parseEnvFile(
      readFile(path.join(worktreeDir, '.worktree.env'), 'utf8'),
    ).COMPOSE_PROJECT_NAME;
  } catch {
    warn(`worktree: no readable .worktree.env in ${worktreeDir}, skipping the docker teardown`);
    return null;
  }
  if (value !== expected) {
    warn(
      `worktree: COMPOSE_PROJECT_NAME in .worktree.env is ${JSON.stringify(value)}, expected ${expected}; ` +
        'skipping the docker teardown',
    );
    return null;
  }
  return value;
}

function defaultDocker(args) {
  execFileSync('docker', args, { stdio: 'inherit' });
}

function defaultDevStop(worktreeDir) {
  execFileSync('make', ['-C', worktreeDir, 'dev-web-stop'], { stdio: 'inherit' });
}

/**
 * Tear down a worktree: docker project (guarded), dev server, then a non-force
 * `git worktree remove` (the branch is kept).
 */
export function removeWorktree({
  worktreeRoot,
  git = runGit,
  docker = defaultDocker,
  runDevStop = defaultDevStop,
  warn = console.warn,
}) {
  const root = realpathSync(worktreeRoot);
  const primaryRoot = path.dirname(realpathSync(commonDir(git, root)));
  if (root === primaryRoot) throw new Error('refusing to remove the primary checkout');
  if (!root.startsWith(`${worktreesDir(primaryRoot)}${path.sep}`)) {
    throw new Error(`refusing to remove ${root}: not under ${worktreesDir(primaryRoot)}`);
  }

  const project = rmComposeProject({ worktreeDir: root, warn });
  if (project !== null) {
    try {
      docker([
        'compose',
        '-p',
        project,
        '--project-directory',
        root,
        '-f',
        path.join(root, 'docker-compose.yml'),
        'down',
        '-v',
        '--rmi',
        'local',
      ]);
    } catch (err) {
      warn(`worktree: docker teardown of ${project} failed (is docker running?): ${err.message}`);
    }
  }

  if (existsSync(path.join(root, '.dev', 'vite-dev.pid'))) {
    try {
      runDevStop(root);
    } catch (err) {
      warn(`worktree: make dev-web-stop failed: ${err.message}`);
    }
  }

  git(primaryRoot, ['worktree', 'remove', root]);
}

function defaultRunSetup(worktreeDir) {
  for (const dir of NPM_DIRS) {
    execFileSync('npm', ['ci'], { cwd: path.join(worktreeDir, dir), stdio: 'inherit' });
  }
}

/** Create a worktree end to end: plan, create, link, slot + ports under lock, env file, setup. */
export async function newWorktree({
  primaryRoot,
  name,
  branch,
  base,
  git = runGit,
  projectNames = listComposeProjects(),
  probe = probePort,
  runSetup = defaultRunSetup,
  warn = console.warn,
}) {
  const plan = planNew({ primaryRoot, name, branch, base, git, projectNames, warn });

  // Lock first: a held lock must fail before any worktree or branch is created.
  const release = acquireSlotLock({ lockDir: path.join(commonDir(git, primaryRoot), LOCK_NAME) });
  let ports;
  try {
    createWorktree({ git, primaryRoot, plan });
    linkDevVars({ primaryRoot, worktreeDir: plan.path, warn });
    try {
      // env is empty on purpose: ambient unprefixed port variables (make exports the primary's
      // defaults to every recipe) must never count as explicit overrides.
      ports = await resolvePorts({
        slug: plan.slug,
        claimed: readClaimedSlots(primaryRoot),
        probe,
        env: {},
      });
      writeWorktreeEnv({ dir: plan.path, slug: plan.slug, primaryRoot, ports });
    } catch (err) {
      throw new Error(
        `worktree created at ${plan.path} but port/env setup failed: ${err.message}\n` +
          `  discard it:   make -C ${plan.path} worktree-rm`,
        { cause: err },
      );
    }
  } finally {
    release();
  }

  try {
    await runSetup(plan.path);
  } catch (err) {
    throw new Error(
      `worktree created at ${plan.path} but setup failed: ${err.message}\n` +
        `  retry setup:  npm --prefix ${path.join(plan.path, 'web')} ci && npm --prefix ${path.join(plan.path, 'person-service')} ci\n` +
        `  discard it:   make -C ${plan.path} worktree-rm`,
      { cause: err },
    );
  }
  return { path: plan.path, branch: plan.branch, ports };
}

/** The one-line summary printed after `worktree new`. */
export function formatCreatedSummary({ path: worktreePath, branch, ports }) {
  const { NOTIFS_SLOT, NTFY_PORT, API_PORT, WEB_PORT, E2E_PORT, DELIVERY_PORT } = ports;
  return (
    `worktree: created ${worktreePath} on branch ${branch} ` +
    `(slot=${NOTIFS_SLOT} ntfy=${NTFY_PORT} api=${API_PORT} web=${WEB_PORT} e2e=${E2E_PORT} ` +
    `delivery=${DELIVERY_PORT})`
  );
}

async function main(argv) {
  const [command] = argv;
  if (command === 'new') {
    const args = parseWorktreeArgs(process.env);
    const primaryRoot = path.dirname(realpathSync(commonDir(runGit, process.cwd())));
    const result = await newWorktree({ primaryRoot, ...args });
    console.log(formatCreatedSummary(result));
  } else if (command === 'rm') {
    const worktreeRoot = runGit(process.cwd(), ['rev-parse', '--show-toplevel']);
    removeWorktree({ worktreeRoot });
    console.log(`worktree: removed ${worktreeRoot} (branch kept)`);
  } else {
    throw new Error(
      'usage: WT_NAME=<slug> [WT_BRANCH=<b>] [WT_BASE=<ref>] node scripts/worktree.mjs new | rm',
    );
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main(process.argv.slice(2)).catch((err) => {
    console.error(`worktree: ${err.message}`);
    process.exitCode = 1;
  });
}

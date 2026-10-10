import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, it } from 'node:test';

import { DEFAULT_PORTS, resolvePorts } from './ports.mjs';
import {
  acquireSlotLock,
  defaultBranch,
  dnsSlug,
  ensureExcludes,
  linkDevVars,
  newWorktree,
  parseWorktreeArgs,
  planNew,
  readClaimedSlots,
  removeWorktree,
  rmComposeProject,
  writeWorktreeEnv,
} from './worktree.mjs';

const allFree = async () => true;

function tmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-wt-'));
}

/** A primary root whose git is stubbed: every command succeeds unless `fail` matches. */
function stubGit({ fail = () => false, answers = {} } = {}) {
  const calls = [];
  const git = (cwd, args) => {
    calls.push({ cwd, args });
    if (fail(args)) throw new Error(`stub git failure: ${args.join(' ')}`);
    return answers[args[0]] ?? '';
  };
  git.calls = calls;
  return git;
}

describe('dnsSlug', () => {
  it('lowercases, replaces invalid characters, strips edges and caps at 40', () => {
    assert.equal(dnsSlug('infra/worktree-adoption'), 'infra-worktree-adoption');
    assert.equal(dnsSlug('Proof_A'), 'proof-a');
    assert.equal(dnsSlug('/leading'), 'leading');
    assert.equal(dnsSlug('a'.repeat(39) + '-b-more'), 'a'.repeat(39));
    assert.equal(dnsSlug('x'.repeat(50)).length, 40);
  });

  it('throws when nothing is left', () => {
    assert.throws(() => dnsSlug('///'), /empty/);
  });
});

describe('parseWorktreeArgs', () => {
  it('reads WT_NAME/WT_BRANCH/WT_BASE and treats empty as unset', () => {
    assert.deepEqual(parseWorktreeArgs({ WT_NAME: 'a', WT_BRANCH: 'b/c', WT_BASE: 'main' }), {
      name: 'a',
      branch: 'b/c',
      base: 'main',
    });
    assert.deepEqual(parseWorktreeArgs({ WT_NAME: 'a', WT_BRANCH: '', WT_BASE: '' }), {
      name: 'a',
      branch: undefined,
      base: undefined,
    });
  });
});

describe('defaultBranch', () => {
  it('strips the origin/ prefix from symbolic-ref', () => {
    const git = stubGit({ answers: { 'symbolic-ref': 'origin/trunk' } });
    assert.equal(defaultBranch(git, '/x'), 'trunk');
    assert.deepEqual(git.calls[0].args, ['symbolic-ref', '--short', 'refs/remotes/origin/HEAD']);
  });

  it('falls back to main on failure or empty output', () => {
    assert.equal(defaultBranch(stubGit({ fail: () => true }), '/x'), 'main');
    assert.equal(defaultBranch(stubGit({ answers: { 'symbolic-ref': '' } }), '/x'), 'main');
  });
});

describe('planNew', () => {
  function primary() {
    return fs.realpathSync(tmpDir());
  }

  it('sanitizes the name, derives path and uses origin/<default> as base', () => {
    const root = primary();
    const git = stubGit({
      fail: (args) => args[0] === 'show-ref',
      answers: { 'symbolic-ref': 'origin/main' },
    });
    const plan = planNew({ primaryRoot: root, name: 'Proof_A', git, projectNames: [] });
    assert.equal(plan.slug, 'proof-a');
    assert.equal(plan.path, path.join(root, '.claude', 'worktrees', 'proof-a'));
    assert.equal(plan.project, '4irl-notifs-proof-a');
    assert.equal(plan.base, 'origin/main');
    assert.equal(plan.branch, 'proof-a');
    assert.equal(plan.mode, 'new');
  });

  it('honors an explicit base and branch', () => {
    const root = primary();
    const git = stubGit({ fail: (args) => args[0] === 'show-ref' });
    const plan = planNew({
      primaryRoot: root,
      name: 'a',
      branch: 'proof/a',
      base: 'infra/worktree-adoption',
      git,
      projectNames: [],
    });
    assert.equal(plan.branch, 'proof/a');
    assert.equal(plan.base, 'infra/worktree-adoption');
  });

  it('refuses the repo name and the primary basename', () => {
    const root = primary();
    const git = stubGit();
    assert.throws(
      () => planNew({ primaryRoot: root, name: '4irl-notifs', git, projectNames: [] }),
      /refused slug/,
    );
    assert.throws(
      () => planNew({ primaryRoot: root, name: path.basename(root), git, projectNames: [] }),
      /refused slug/,
    );
  });

  it('refuses an existing worktree directory', () => {
    const root = primary();
    fs.mkdirSync(path.join(root, '.claude', 'worktrees', 'dup'), { recursive: true });
    assert.throws(
      () => planNew({ primaryRoot: root, name: 'dup', git: stubGit(), projectNames: [] }),
      /already exists/,
    );
  });

  it('refuses an invalid ref', () => {
    const root = primary();
    const git = stubGit({ fail: (args) => args[0] === 'check-ref-format' });
    assert.throws(
      () => planNew({ primaryRoot: root, name: 'ok', branch: 'bad..ref', git, projectNames: [] }),
      /invalid branch/,
    );
    assert.throws(
      () =>
        planNew({ primaryRoot: root, name: 'ok', branch: '-x', git: stubGit(), projectNames: [] }),
      /invalid branch/,
    );
  });

  it('refuses a candidate compose project that already exists', () => {
    const root = primary();
    const git = stubGit({ fail: (args) => args[0] === 'show-ref' });
    assert.throws(
      () => planNew({ primaryRoot: root, name: 'taken', git, projectNames: ['4irl-notifs-taken'] }),
      /compose project/,
    );
  });

  it('warns and skips the project check when docker is unavailable', () => {
    const root = primary();
    const git = stubGit({ fail: (args) => args[0] === 'show-ref' });
    const warnings = [];
    const plan = planNew({
      primaryRoot: root,
      name: 'nodocker',
      git,
      projectNames: null,
      warn: (msg) => warnings.push(msg),
    });
    assert.equal(plan.slug, 'nodocker');
    assert.equal(warnings.length, 1);
    assert.match(warnings[0], /docker/);
  });

  it('uses mode local when the branch exists locally', () => {
    const root = primary();
    const git = stubGit({
      fail: (args) => args[0] === 'show-ref' && !args.includes('refs/heads/feat-x'),
    });
    const plan = planNew({ primaryRoot: root, name: 'feat-x', git, projectNames: [] });
    assert.equal(plan.mode, 'local');
  });

  it('uses mode remote when the branch exists only on origin', () => {
    const root = primary();
    const git = stubGit({
      fail: (args) => args[0] === 'show-ref' && !args.includes('refs/remotes/origin/feat-x'),
    });
    const plan = planNew({ primaryRoot: root, name: 'feat-x', git, projectNames: [] });
    assert.equal(plan.mode, 'remote');
  });

  it('errors when the base ref does not exist for a new branch', () => {
    const root = primary();
    const git = stubGit({ fail: (args) => args[0] === 'show-ref' || args[0] === 'rev-parse' });
    assert.throws(
      () => planNew({ primaryRoot: root, name: 'nb', git, projectNames: [] }),
      /base ref/,
    );
  });
});

describe('resolvePorts with an empty env', () => {
  it('ignores ambient unprefixed port variables', async () => {
    const saved = { ...process.env };
    Object.assign(process.env, {
      NTFY_PORT: '8090',
      API_PORT: '8091',
      WEB_PORT: '5173',
      E2E_PORT: '4173',
      DELIVERY_PORT: '8300',
    });
    try {
      const ports = await resolvePorts({
        slug: 'proof-a',
        claimed: new Set(),
        probe: allFree,
        env: {},
      });
      assert.notEqual(ports.NOTIFS_SLOT, 0);
      for (const key of Object.keys(DEFAULT_PORTS)) {
        assert.notEqual(ports[key], DEFAULT_PORTS[key], key);
      }
    } finally {
      for (const key of Object.keys(DEFAULT_PORTS)) {
        if (saved[key] === undefined) delete process.env[key];
        else process.env[key] = saved[key];
      }
    }
  });
});

describe('readClaimedSlots', () => {
  it('collects numeric NOTIFS_SLOT values and skips bad or missing files', () => {
    const primary = tmpDir();
    const base = path.join(primary, '.claude', 'worktrees');
    for (const [dir, content] of [
      ['a', 'NOTIFS_SLOT=3\n'],
      ['b', 'NOTIFS_SLOT=abc\n'],
      ['c', null],
    ]) {
      fs.mkdirSync(path.join(base, dir), { recursive: true });
      if (content !== null) fs.writeFileSync(path.join(base, dir, '.worktree.env'), content);
    }
    assert.deepEqual(readClaimedSlots(primary), new Set([3]));
  });

  it('returns an empty set when the worktrees directory is missing', () => {
    assert.deepEqual(readClaimedSlots(tmpDir()), new Set());
  });
});

describe('writeWorktreeEnv', () => {
  it('writes KEY=VALUE lines at mode 0600 without leaving a temp file', () => {
    const dir = tmpDir();
    const ports = {
      NOTIFS_SLOT: 3,
      NTFY_PORT: 8096,
      API_PORT: 8097,
      WEB_PORT: 5276,
      E2E_PORT: 4276,
      DELIVERY_PORT: 8303,
    };
    writeWorktreeEnv({ dir, slug: 'proof-a', primaryRoot: '/primary', ports });
    const file = path.join(dir, '.worktree.env');
    assert.equal(
      fs.readFileSync(file, 'utf8'),
      [
        'SLUG=proof-a',
        'PRIMARY_ROOT=/primary',
        'IS_PRIMARY=0',
        'NOTIFS_SLOT=3',
        'COMPOSE_PROJECT_NAME=4irl-notifs-proof-a',
        'NTFY_PORT=8096',
        'API_PORT=8097',
        'WEB_PORT=5276',
        'E2E_PORT=4276',
        'DELIVERY_PORT=8303',
        '',
      ].join('\n'),
    );
    assert.equal(fs.statSync(file).mode & 0o777, 0o600);
    assert.deepEqual(fs.readdirSync(dir), ['.worktree.env']);
  });
});

describe('ensureExcludes', () => {
  it('adds /.worktree.env and /.dev/ idempotently', () => {
    const common = tmpDir();
    ensureExcludes(common);
    ensureExcludes(common);
    const lines = fs.readFileSync(path.join(common, 'info', 'exclude'), 'utf8').split('\n');
    assert.equal(lines.filter((line) => line === '/.worktree.env').length, 1);
    assert.equal(lines.filter((line) => line === '/.dev/').length, 1);
  });

  it('keeps existing content and handles a missing trailing newline', () => {
    const common = tmpDir();
    fs.mkdirSync(path.join(common, 'info'));
    fs.writeFileSync(path.join(common, 'info', 'exclude'), '# keep\n/.worktree.env');
    ensureExcludes(common);
    const lines = fs.readFileSync(path.join(common, 'info', 'exclude'), 'utf8').split('\n');
    assert.ok(lines.includes('# keep'));
    assert.ok(lines.includes('/.worktree.env'));
    assert.ok(lines.includes('/.dev/'));
  });
});

describe('rmComposeProject', () => {
  const worktreeDir = '/primary/.claude/worktrees/proof-a';
  const reader = (text) => () => {
    if (text === null) throw Object.assign(new Error('ENOENT'), { code: 'ENOENT' });
    return text;
  };

  function withAmbient(value, fn) {
    const saved = process.env.COMPOSE_PROJECT_NAME;
    process.env.COMPOSE_PROJECT_NAME = value;
    try {
      return fn();
    } finally {
      if (saved === undefined) delete process.env.COMPOSE_PROJECT_NAME;
      else process.env.COMPOSE_PROJECT_NAME = saved;
    }
  }

  it('skips with a warning when the file is missing', () => {
    const warnings = [];
    assert.equal(
      rmComposeProject({ worktreeDir, readFile: reader(null), warn: (m) => warnings.push(m) }),
      null,
    );
    assert.equal(warnings.length, 1);
  });

  it('skips on a mismatched value and on the primary project', () => {
    assert.equal(
      rmComposeProject({
        worktreeDir,
        readFile: reader('COMPOSE_PROJECT_NAME=4irl-notifs-other\n'),
        warn: () => {},
      }),
      null,
    );
    assert.equal(
      rmComposeProject({
        worktreeDir,
        readFile: reader('COMPOSE_PROJECT_NAME=4irl-notifs\n'),
        warn: () => {},
      }),
      null,
    );
  });

  it('ignores the ambient COMPOSE_PROJECT_NAME', () => {
    withAmbient('4irl-notifs-proof-a', () => {
      assert.equal(rmComposeProject({ worktreeDir, readFile: reader(null), warn: () => {} }), null);
    });
    withAmbient('something-else', () => {
      assert.equal(
        rmComposeProject({
          worktreeDir,
          readFile: reader('COMPOSE_PROJECT_NAME=4irl-notifs-proof-a\n'),
          warn: () => {},
        }),
        '4irl-notifs-proof-a',
      );
    });
  });

  it('returns a matching project', () => {
    assert.equal(
      rmComposeProject({
        worktreeDir,
        readFile: reader('SLUG=proof-a\nCOMPOSE_PROJECT_NAME=4irl-notifs-proof-a\n'),
        warn: () => {},
      }),
      '4irl-notifs-proof-a',
    );
  });
});

describe('removeWorktree', () => {
  function layout() {
    const primary = fs.realpathSync(tmpDir());
    const worktree = path.join(primary, '.claude', 'worktrees', 'proof-a');
    fs.mkdirSync(worktree, { recursive: true });
    fs.mkdirSync(path.join(primary, '.git'));
    const git = stubGit({ answers: { 'rev-parse': path.join(primary, '.git') } });
    return { primary, worktree, git };
  }

  it('refuses the primary checkout', () => {
    const { primary, git } = layout();
    assert.throws(
      () =>
        removeWorktree({
          worktreeRoot: primary,
          git,
          docker: () => {},
          runDevStop: () => {},
          warn: () => {},
        }),
      /primary/,
    );
  });

  it('refuses a path outside .claude/worktrees', () => {
    const { primary, git } = layout();
    const outside = path.join(primary, 'elsewhere');
    fs.mkdirSync(outside);
    assert.throws(
      () =>
        removeWorktree({
          worktreeRoot: outside,
          git,
          docker: () => {},
          runDevStop: () => {},
          warn: () => {},
        }),
      /not under/,
    );
  });

  it('tears down the project, stops the dev server, then removes non-force', () => {
    const { worktree, git } = layout();
    fs.writeFileSync(
      path.join(worktree, '.worktree.env'),
      'COMPOSE_PROJECT_NAME=4irl-notifs-proof-a\n',
    );
    fs.mkdirSync(path.join(worktree, '.dev'));
    fs.writeFileSync(path.join(worktree, '.dev', 'vite-dev.pid'), '123\n');
    const dockerCalls = [];
    const devStops = [];
    removeWorktree({
      worktreeRoot: worktree,
      git,
      docker: (args) => dockerCalls.push(args),
      runDevStop: (dir) => devStops.push(dir),
      warn: () => {},
    });
    assert.equal(dockerCalls.length, 1);
    assert.deepEqual(dockerCalls[0].slice(0, 3), ['compose', '-p', '4irl-notifs-proof-a']);
    assert.deepEqual(dockerCalls[0].slice(-4), ['down', '-v', '--rmi', 'local']);
    assert.deepEqual(devStops, [worktree]);
    const removal = git.calls.find((call) => call.args[0] === 'worktree');
    assert.deepEqual(removal.args, ['worktree', 'remove', worktree]);
  });

  it('tolerates docker and dev-stop failures with warnings', () => {
    const { worktree, git } = layout();
    fs.writeFileSync(
      path.join(worktree, '.worktree.env'),
      'COMPOSE_PROJECT_NAME=4irl-notifs-proof-a\n',
    );
    fs.mkdirSync(path.join(worktree, '.dev'));
    fs.writeFileSync(path.join(worktree, '.dev', 'vite-dev.pid'), '123\n');
    const warnings = [];
    removeWorktree({
      worktreeRoot: worktree,
      git,
      docker: () => {
        throw new Error('docker down');
      },
      runDevStop: () => {
        throw new Error('stop failed');
      },
      warn: (msg) => warnings.push(msg),
    });
    assert.equal(warnings.length, 2);
    assert.ok(git.calls.some((call) => call.args[0] === 'worktree'));
  });

  for (const [label, content] of [
    ['missing file', null],
    ['mismatched project', 'COMPOSE_PROJECT_NAME=4irl-notifs-other\n'],
    ['primary project', 'COMPOSE_PROJECT_NAME=4irl-notifs\n'],
  ]) {
    it(`never calls docker on ${label} but still removes the worktree`, () => {
      const { worktree, git } = layout();
      if (content !== null) fs.writeFileSync(path.join(worktree, '.worktree.env'), content);
      const saved = process.env.COMPOSE_PROJECT_NAME;
      process.env.COMPOSE_PROJECT_NAME = '4irl-notifs';
      const dockerCalls = [];
      try {
        removeWorktree({
          worktreeRoot: worktree,
          git,
          docker: (args) => dockerCalls.push(args),
          runDevStop: () => {},
          warn: () => {},
        });
      } finally {
        if (saved === undefined) delete process.env.COMPOSE_PROJECT_NAME;
        else process.env.COMPOSE_PROJECT_NAME = saved;
      }
      assert.deepEqual(dockerCalls, []);
      assert.deepEqual(git.calls.find((call) => call.args[0] === 'worktree').args, [
        'worktree',
        'remove',
        worktree,
      ]);
    });
  }
});

describe('linkDevVars', () => {
  function dirs() {
    const primaryRoot = tmpDir();
    const worktreeDir = tmpDir();
    fs.mkdirSync(path.join(primaryRoot, 'web'), { recursive: true });
    return { primaryRoot, worktreeDir };
  }

  it('symlinks the primary web/.dev.vars', () => {
    const { primaryRoot, worktreeDir } = dirs();
    fs.writeFileSync(path.join(primaryRoot, 'web', '.dev.vars'), 'A=1\n');
    linkDevVars({ primaryRoot, worktreeDir, warn: () => {} });
    const dest = path.join(worktreeDir, 'web', '.dev.vars');
    assert.ok(fs.lstatSync(dest).isSymbolicLink());
    assert.equal(fs.readlinkSync(dest), path.join(primaryRoot, 'web', '.dev.vars'));
  });

  it('copies the example when the primary has none', () => {
    const { primaryRoot, worktreeDir } = dirs();
    fs.mkdirSync(path.join(worktreeDir, 'web'), { recursive: true });
    fs.writeFileSync(path.join(worktreeDir, 'web', '.dev.vars.example'), 'EX=1\n');
    linkDevVars({ primaryRoot, worktreeDir, warn: () => {} });
    const dest = path.join(worktreeDir, 'web', '.dev.vars');
    assert.ok(!fs.lstatSync(dest).isSymbolicLink());
    assert.equal(fs.readFileSync(dest, 'utf8'), 'EX=1\n');
  });

  it('never clobbers a real file', () => {
    const { primaryRoot, worktreeDir } = dirs();
    fs.writeFileSync(path.join(primaryRoot, 'web', '.dev.vars'), 'A=1\n');
    fs.mkdirSync(path.join(worktreeDir, 'web'), { recursive: true });
    fs.writeFileSync(path.join(worktreeDir, 'web', '.dev.vars'), 'MINE=1\n');
    linkDevVars({ primaryRoot, worktreeDir, warn: () => {} });
    assert.equal(fs.readFileSync(path.join(worktreeDir, 'web', '.dev.vars'), 'utf8'), 'MINE=1\n');
  });

  it('warns without throwing when both are missing', () => {
    const { primaryRoot, worktreeDir } = dirs();
    const warnings = [];
    const returned = linkDevVars({ primaryRoot, worktreeDir, warn: (msg) => warnings.push(msg) });
    assert.equal(fs.existsSync(path.join(worktreeDir, 'web', '.dev.vars')), false);
    assert.equal(warnings.length, 1);
    assert.deepEqual(returned, warnings);
  });
});

describe('acquireSlotLock', () => {
  it('creates the lock with a pid file and release removes it', () => {
    const lockDir = path.join(tmpDir(), 'slot.lock');
    const release = acquireSlotLock({ lockDir, isPidAlive: () => true });
    assert.ok(fs.existsSync(path.join(lockDir, 'pid')));
    release();
    assert.equal(fs.existsSync(lockDir), false);
  });

  it('fails while held by a live pid', () => {
    const lockDir = path.join(tmpDir(), 'slot.lock');
    acquireSlotLock({ lockDir, isPidAlive: () => true });
    assert.throws(() => acquireSlotLock({ lockDir, isPidAlive: () => true }), /slot lock held/);
  });

  it('reclaims a lock whose pid is dead', () => {
    const lockDir = path.join(tmpDir(), 'slot.lock');
    fs.mkdirSync(lockDir);
    fs.writeFileSync(path.join(lockDir, 'pid'), '999999\n');
    const release = acquireSlotLock({ lockDir, isPidAlive: () => false });
    assert.equal(fs.readFileSync(path.join(lockDir, 'pid'), 'utf8').trim(), String(process.pid));
    release();
  });

  it('reclaims a pid-less lock older than 5 s and honors a younger one', () => {
    const lockDir = path.join(tmpDir(), 'slot.lock');
    fs.mkdirSync(lockDir);
    assert.throws(() => acquireSlotLock({ lockDir, isPidAlive: () => true }), /slot lock held/);
    const old = new Date(Date.now() - 6000);
    fs.utimesSync(lockDir, old, old);
    const release = acquireSlotLock({ lockDir, isPidAlive: () => true });
    release();
  });

  it('reclaims a stale lock by rename and leaves no renamed directories', () => {
    const parent = tmpDir();
    const lockDir = path.join(parent, 'slot.lock');
    fs.mkdirSync(lockDir);
    fs.writeFileSync(path.join(lockDir, 'pid'), '999999\n');
    const release = acquireSlotLock({ lockDir, isPidAlive: () => false });
    assert.deepEqual(fs.readdirSync(parent), ['slot.lock']);
    release();
  });

  it('still acquires when the stale lock vanishes before the rename', () => {
    const lockDir = path.join(tmpDir(), 'slot.lock');
    fs.mkdirSync(lockDir);
    fs.writeFileSync(path.join(lockDir, 'pid'), '999999\n');
    // simulate another process reclaiming it first: the liveness probe removes the lock
    const isPidAlive = () => {
      fs.rmSync(lockDir, { recursive: true, force: true });
      return false;
    };
    const release = acquireSlotLock({ lockDir, isPidAlive });
    assert.ok(fs.existsSync(path.join(lockDir, 'pid')));
    release();
  });
});

describe('newWorktree', () => {
  it('creates, links, allocates ports, writes the env file and runs setup', async () => {
    const primaryRoot = fs.realpathSync(tmpDir());
    const common = path.join(primaryRoot, '.git');
    fs.mkdirSync(common);
    const git = (cwd, args) => {
      if (args[0] === 'rev-parse' && args.includes('--git-common-dir')) return common;
      if (args[0] === 'show-ref') throw new Error('no ref');
      if (args[0] === 'worktree' && args[1] === 'add') {
        fs.mkdirSync(path.join(args.at(-2), 'web'), { recursive: true });
      }
      return '';
    };
    const setups = [];
    const result = await newWorktree({
      primaryRoot,
      name: 'proof-a',
      branch: 'proof/a',
      base: 'main',
      git,
      projectNames: [],
      probe: allFree,
      runSetup: (dir) => setups.push(dir),
      warn: () => {},
    });
    assert.equal(result.path, path.join(primaryRoot, '.claude', 'worktrees', 'proof-a'));
    const text = fs.readFileSync(path.join(result.path, '.worktree.env'), 'utf8');
    assert.match(text, /^COMPOSE_PROJECT_NAME=4irl-notifs-proof-a$/m);
    assert.deepEqual(setups, [result.path]);
    assert.equal(fs.existsSync(path.join(common, 'notifs-worktree-slot.lock')), false);
    assert.ok(fs.readFileSync(path.join(common, 'info', 'exclude'), 'utf8').includes('/.dev/'));
  });

  it('passes the right worktree add flags for each mode', async () => {
    for (const [mode, showRefOk, expected] of [
      ['local', 'refs/heads/feat-m', (p) => ['worktree', 'add', p, 'feat-m']],
      [
        'remote',
        'refs/remotes/origin/feat-m',
        (p) => ['worktree', 'add', '--track', '-b', 'feat-m', p, 'origin/feat-m'],
      ],
      ['new', null, (p) => ['worktree', 'add', '--no-track', '-b', 'feat-m', p, 'main']],
    ]) {
      const primaryRoot = fs.realpathSync(tmpDir());
      const common = path.join(primaryRoot, '.git');
      fs.mkdirSync(common);
      const adds = [];
      const git = (cwd, args) => {
        if (args[0] === 'rev-parse' && args.includes('--git-common-dir')) return common;
        if (args[0] === 'show-ref' && args.at(-1) !== showRefOk) throw new Error('no ref');
        if (args[0] === 'worktree' && args[1] === 'add') {
          adds.push(args);
          fs.mkdirSync(path.join(primaryRoot, '.claude', 'worktrees', 'feat-m'), {
            recursive: true,
          });
        }
        return '';
      };
      const result = await newWorktree({
        primaryRoot,
        name: 'feat-m',
        base: 'main',
        git,
        projectNames: [],
        probe: allFree,
        runSetup: () => {},
        warn: () => {},
      });
      assert.deepEqual(adds, [expected(result.path)], mode);
    }
  });

  it('fails on a held slot lock before creating any worktree', async () => {
    const primaryRoot = fs.realpathSync(tmpDir());
    const common = path.join(primaryRoot, '.git');
    const lockDir = path.join(common, 'notifs-worktree-slot.lock');
    fs.mkdirSync(lockDir, { recursive: true });
    fs.writeFileSync(path.join(lockDir, 'pid'), `${process.pid}\n`);
    const calls = [];
    const git = (cwd, args) => {
      calls.push(args);
      if (args[0] === 'rev-parse' && args.includes('--git-common-dir')) return common;
      if (args[0] === 'show-ref') throw new Error('no ref');
      return '';
    };
    await assert.rejects(
      newWorktree({
        primaryRoot,
        name: 'proof-d',
        base: 'main',
        git,
        projectNames: [],
        probe: allFree,
        runSetup: () => {},
        warn: () => {},
      }),
      /slot lock held/,
    );
    assert.equal(
      calls.some((args) => args[0] === 'worktree' && args[1] === 'add'),
      false,
    );
  });

  it('reports a port failure and releases the slot lock', async () => {
    const primaryRoot = fs.realpathSync(tmpDir());
    const common = path.join(primaryRoot, '.git');
    fs.mkdirSync(common);
    const git = (cwd, args) => {
      if (args[0] === 'rev-parse' && args.includes('--git-common-dir')) return common;
      if (args[0] === 'show-ref') throw new Error('no ref');
      if (args[0] === 'worktree' && args[1] === 'add')
        fs.mkdirSync(args.at(-2), { recursive: true });
      return '';
    };
    await assert.rejects(
      newWorktree({
        primaryRoot,
        name: 'proof-c',
        base: 'main',
        git,
        projectNames: [],
        probe: async () => false,
        runSetup: () => {},
        warn: () => {},
      }),
      /port\/env setup failed/,
    );
    assert.equal(fs.existsSync(path.join(common, 'notifs-worktree-slot.lock')), false);
  });

  it('reports retry and discard commands when setup fails', async () => {
    const primaryRoot = fs.realpathSync(tmpDir());
    const common = path.join(primaryRoot, '.git');
    fs.mkdirSync(common);
    const git = (cwd, args) => {
      if (args[0] === 'rev-parse' && args.includes('--git-common-dir')) return common;
      if (args[0] === 'show-ref') throw new Error('no ref');
      if (args[0] === 'worktree' && args[1] === 'add')
        fs.mkdirSync(args.at(-2), { recursive: true });
      return '';
    };
    await assert.rejects(
      newWorktree({
        primaryRoot,
        name: 'proof-b',
        base: 'main',
        git,
        projectNames: [],
        probe: allFree,
        runSetup: () => {
          throw new Error('npm exploded');
        },
        warn: () => {},
      }),
      /worktree-rm/,
    );
  });
});

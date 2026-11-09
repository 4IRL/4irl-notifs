// Structural contract test for the production workflows: prod-deploy.yml, prod-build-and-deploy.yml
// and notifs-ops-key.yml. No YAML parser (no new dependency): indent-aware text matching on the
// comment-stripped workflow text. Every assertion has a positive control: a minimal mutation of a
// fixture STRING (never the real file) that violates exactly that rule must make the same
// assertion throw, so an assertion that can never fail is itself a test failure.
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const readRepo = (rel) => readFileSync(path.join(ROOT, rel), 'utf8');

const DEPLOY = readRepo('.github/workflows/prod-deploy.yml');
const BUILD_AND_DEPLOY = readRepo('.github/workflows/prod-build-and-deploy.yml');
const KEY_WORKFLOW = readRepo('.github/workflows/notifs-ops-key.yml');

const LINT_RUN =
  'node scripts/onboard.mjs lint --manifest ops/notifs-ops/manifest.json --target app';
const APPLY_RUN =
  'node scripts/onboard.mjs apply --manifest ops/notifs-ops/manifest.json --target app';
const APPLY_ENV_KEYS = [
  'ONBOARD_URL',
  'ONBOARD_APP_ACCESS_ID',
  'ONBOARD_APP_ACCESS_SECRET',
  'ONBOARD_APP_KEY',
  'NOTIFS_OPS_DISCORD_WEBHOOK',
];
const OPS_SECRETS = [
  'NOTIFS_OPS_ACCESS_CLIENT_ID',
  'NOTIFS_OPS_ACCESS_CLIENT_SECRET',
  'NOTIFS_OPS_DISCORD_WEBHOOK',
];

// ---------------------------------------------------------------------------------------------
// Text helpers
// ---------------------------------------------------------------------------------------------

function stripComments(text) {
  return text
    .split('\n')
    .filter((line) => !line.trimStart().startsWith('#'))
    .join('\n');
}

const indentOf = (line) => line.length - line.trimStart().length;

/** Lines of the top-level block `key:` (excluding the key line), up to the next top-level key. */
function topBlock(text, key) {
  const lines = stripComments(text).split('\n');
  const start = lines.findIndex((line) => line === `${key}:`);
  if (start === -1) return [];
  const out = [];
  for (let i = start + 1; i < lines.length; i += 1) {
    if (lines[i].trim() !== '' && indentOf(lines[i]) === 0) break;
    out.push(lines[i]);
  }
  return out;
}

/** Lines of one job under `jobs:` (indent-2 key), excluding the job key line. */
function jobLines(text, jobName) {
  const lines = topBlock(text, 'jobs');
  const start = lines.findIndex((line) => line === `  ${jobName}:`);
  if (start === -1) return [];
  const out = [];
  for (let i = start + 1; i < lines.length; i += 1) {
    if (lines[i].trim() !== '' && indentOf(lines[i]) <= 2) break;
    out.push(lines[i]);
  }
  return out;
}

function jobNames(text) {
  return topBlock(text, 'jobs')
    .filter((line) => indentOf(line) === 2 && line.trim().endsWith(':'))
    .map((line) => line.trim().slice(0, -1));
}

/** Steps of a job: each is its lines normalised so `- key: v` becomes an indent-8 `key: v`. */
function stepsOf(text, jobName) {
  const lines = jobLines(text, jobName);
  const start = lines.findIndex((line) => line === '    steps:');
  if (start === -1) return [];
  const steps = [];
  for (let i = start + 1; i < lines.length; i += 1) {
    const line = lines[i];
    if (line.trim() !== '' && indentOf(line) <= 4) break;
    if (/^ {6}- /.test(line)) {
      steps.push([`        ${line.slice(8)}`]);
    } else if (steps.length > 0) {
      steps[steps.length - 1].push(line);
    }
  }
  return steps.map((stepLines) => makeStep(stepLines));
}

function makeStep(lines) {
  const scalar = (key) => {
    const found = lines.find((line) => line.startsWith(`        ${key}: `));
    return found === undefined ? undefined : found.slice(`        ${key}: `.length).trim();
  };
  const block = (key) => {
    const start = lines.findIndex(
      (line) =>
        line === `        ${key}:` ||
        (/^ {8}[a-z-]+: [|>]-?$/.test(line) && line.startsWith(`        ${key}:`)),
    );
    if (start === -1) return [];
    const out = [];
    for (let i = start + 1; i < lines.length; i += 1) {
      if (lines[i].trim() !== '' && indentOf(lines[i]) <= 8) break;
      out.push(lines[i]);
    }
    return out;
  };
  const keysOf = (blockLines) =>
    blockLines.filter((line) => indentOf(line) === 10).map((line) => line.trim().split(':')[0]);
  const runValue = () => {
    const inline = scalar('run');
    if (inline !== undefined && !['|', '>', '|-', '>-'].includes(inline)) return inline;
    return block('run')
      .map((line) => line.trim())
      .filter((line) => line !== '')
      .join('\n');
  };
  return {
    lines,
    text: lines.join('\n'),
    name: scalar('name'),
    id: scalar('id'),
    uses: scalar('uses'),
    ifCondition: scalar('if'),
    continueOnError: scalar('continue-on-error'),
    run: runValue(),
    envBlock: block('env'),
    envKeys: keysOf(block('env')),
    withBlock: block('with'),
    withKeys: keysOf(block('with')),
  };
}

function stepByName(text, jobName, name) {
  const step = stepsOf(text, jobName).find((candidate) => candidate.name === name);
  assert.ok(step, `step "${name}" exists in job ${jobName}`);
  return step;
}

function indexOfStep(steps, predicate, description) {
  const index = steps.findIndex(predicate);
  assert.ok(index !== -1, `step found: ${description}`);
  return index;
}

// ---------------------------------------------------------------------------------------------
// Assertions (each takes workflow text and throws when the rule is violated)
// ---------------------------------------------------------------------------------------------

const JOB = 'NotifsOpsKey';
const NAME_VALIDATE = 'Validate NOTIFS_OPS secrets';
const NAME_SSH_KEY = 'Install SSH key';
const NAME_READY = 'Wait for delivery-api readiness';
const NAME_LINT = 'Lint the notifs_ops manifest';
const NAME_APPLY = 'Apply the notifs_ops manifest';
const NAME_WRITE = 'Write the key on the VPS';

function assertKeyWorkflowTriggers(text) {
  const onLines = topBlock(text, 'on');
  const triggers = onLines.filter((line) => indentOf(line) === 2).map((line) => line.trim());
  assert.ok(triggers.includes('push:'), 'triggers on push');
  assert.ok(
    onLines.some((line) => line.trim() === 'branches: [main]'),
    'push is limited to branches: [main]',
  );
  for (const forbidden of [
    'pull_request:',
    'pull_request_target:',
    'workflow_call:',
    'workflow_run:',
  ]) {
    assert.ok(!triggers.includes(forbidden), `no ${forbidden} trigger`);
  }
  if (triggers.includes('workflow_dispatch:')) {
    const jobIf = jobLines(text, JOB).find((line) => line.startsWith('    if: '));
    assert.ok(jobIf && jobIf.includes("github.ref == 'refs/heads/main'"), 'dispatch gated on main');
  }
}

function assertNoJobUsesKeyWorkflow(text) {
  assert.ok(!stripComments(text).includes('notifs-ops-key'), 'no job uses notifs-ops-key.yml');
}

function assertDeployProdPermissions(text) {
  const lines = jobLines(text, 'deploy-prod');
  assert.ok(lines.length > 0, 'deploy-prod exists');
  const joined = lines.join('\n');
  assert.ok(!joined.includes('read-all'), 'deploy-prod has no read-all');
  assert.ok(!joined.includes('id-token'), 'deploy-prod has no id-token');
  assert.ok(
    /permissions:\n {6}contents: read\n {6}packages: read/.test(joined),
    'deploy-prod perms',
  );
}

function assertNoIdTokenInDeployWorkflows(deployText, buildText) {
  for (const text of [deployText, buildText]) {
    assert.ok(!stripComments(text).includes('id-token'), 'no id-token in the stack deploy');
  }
}

function assertKeyWorkflowPermissions(text) {
  const stripped = stripComments(text);
  assert.ok(!stripped.includes('read-all'), 'no read-all anywhere');
  const jobText = jobLines(text, JOB).join('\n');
  assert.ok(/ {6}id-token: write/.test(jobText), 'job has id-token: write');
  const top = topBlock(text, 'permissions').filter((line) => line.trim() !== '');
  assert.deepEqual(
    top.map((line) => line.trim()),
    ['contents: read'],
    'workflow-level permissions are contents: read only',
  );
  assert.ok(!top.join('\n').includes('id-token'), 'no workflow-level id-token');
}

function assertProductionEnvironment(text) {
  assert.ok(
    jobLines(text, JOB).some((line) => line === '    environment: production'),
    'environment: production',
  );
}

function assertMintOnce(text) {
  const steps = stepsOf(text, JOB);
  const users = steps.filter((step) => step.uses === './.github/actions/notifs-app');
  assert.equal(users.length, 1, 'exactly one notifs-app step');
  const [exchange] = users;
  assert.ok(exchange.id, 'exchange step has an id');
  assert.ok(!exchange.withKeys.includes('manifest'), 'no manifest input');
  const reference = `\${{ steps.${exchange.id}.outputs.key }}`;
  const apply = stepByName(text, JOB, NAME_APPLY);
  assert.ok(
    apply.envBlock.some((line) => line.trim() === `ONBOARD_APP_KEY: ${reference}`),
    'apply ONBOARD_APP_KEY uses the exchange output',
  );
  const write = stepByName(text, JOB, NAME_WRITE);
  assert.equal(
    write.ifCondition,
    `always() && steps.${exchange.id}.outputs.key != ''`,
    'key write if is exactly always() && key != empty',
  );
}

function assertStepOrder(text) {
  const steps = stepsOf(text, JOB);
  const order = [
    indexOfStep(steps, (s) => s.name === NAME_VALIDATE, 'validate'),
    indexOfStep(steps, (s) => s.name === NAME_READY, 'readyz poll'),
    indexOfStep(steps, (s) => s.uses === './.github/actions/notifs-app', 'exchange'),
    indexOfStep(steps, (s) => s.name === NAME_LINT, 'lint'),
    indexOfStep(steps, (s) => s.name === NAME_APPLY, 'apply'),
    indexOfStep(steps, (s) => s.name === NAME_WRITE, 'key write'),
  ];
  const sorted = [...order].sort((a, b) => a - b);
  assert.deepEqual(order, sorted, 'steps are in the contract order');
  assert.equal(order[5], steps.length - 1, 'the key write is the last step');
  assert.ok(!stripComments(text).includes('chmod 644'), 'no chmod 644');
}

function assertFailureConditions(text) {
  for (const name of [NAME_LINT, NAME_APPLY]) {
    const step = stepByName(text, JOB, name);
    assert.ok(!(step.ifCondition ?? '').includes('always()'), `${name} has no always()`);
  }
  const exchange = stepsOf(text, JOB).find((s) => s.uses === './.github/actions/notifs-app');
  assert.ok(exchange, 'exchange step exists');
  assert.equal(exchange.continueOnError, undefined, 'exchange has no continue-on-error');
}

function assertKeyDelivery(text) {
  const write = stepByName(text, JOB, NAME_WRITE);
  const runLines = write.run.split('\n');
  const scpIndex = runLines.findIndex((line) => /^\s*if ! scp |^\s*scp /.test(line));
  const pipeIndex = runLines.findIndex((line) => line.includes('| ssh '));
  assert.ok(scpIndex !== -1 && pipeIndex !== -1, 'scp and the key-carrying ssh both exist');
  assert.ok(scpIndex < pipeIndex, 'scp comes before the ssh that runs the script');
  const scpLine = runLines[scpIndex];
  assert.ok(scpLine.includes('ops/notifs-ops/write-key.sh'), 'scp ships write-key.sh');
  assert.ok(!scpLine.includes('NOTIFS_OPS_KEY'), 'scp line has no key variable');
  assert.ok(!scpLine.includes('NOTIFS_OPS_DELIVERY_KEY'), 'scp line has no key content');
  const pipeLine = runLines[pipeIndex];
  assert.ok(
    /^printf '%s' "\$\{NOTIFS_OPS_KEY\}" \| ssh /.test(pipeLine),
    'the key is fed to ssh stdin by the local printf builtin',
  );
  assert.ok(
    /"trap 'rm -f \$\{remote_script\}' EXIT; bash \$\{remote_script\} \/home\/4irl-notifs\/secrets-persistent\/NOTIFS_OPS_DELIVERY_KEY"$/.test(
      pipeLine,
    ),
    'remote command is a trap-guarded `bash <path> <dest>`',
  );
  const keyUses = runLines.filter((line) => line.includes('NOTIFS_OPS_KEY'));
  assert.deepEqual(keyUses, [pipeLine], 'the key appears in exactly the printf pipe line');
  for (const line of runLines) {
    if (/\b(ssh|scp)\b/.test(line)) {
      assert.ok(
        !line.includes('NOTIFS_OPS_KEY') || line === pipeLine,
        'key in no other ssh/scp argv',
      );
    }
  }
  assert.deepEqual(write.envKeys.sort(), ['NOTIFS_OPS_KEY', 'SSH_HOST'].sort(), 'key only via env');
  const stripped = stripComments(text);
  assert.ok(!stripped.includes('bash -s'), 'no bash -s');
  assert.ok(!/<<-?\s*'?[A-Z_]+'?\s*\n[^]*write-key/.test(write.text), 'no heredoc framing');
  assert.ok(!write.run.includes('<<'), 'no heredoc in the key write step');
  const remoteScripts = runLines.filter((line) => /\bbash \$\{remote_script\}/.test(line));
  assert.equal(remoteScripts.length, 1, 'write-key.sh is the only script run remotely');
}

/** cloudflared comes from the sha256-pinned fetch-pinned.sh, never a moving latest URL or a bare curl. */
function assertCloudflaredPinned(text) {
  const stripped = stripComments(text);
  assert.ok(!stripped.includes('releases/latest'), 'no moving latest release URL');
  assert.ok(!/\bcurl\b/.test(stripped), 'no bare curl download in the workflow');
  const install = stepByName(text, JOB, 'Install Cloudflared');
  assert.equal(
    install.run,
    'bash ops/backup/fetch-pinned.sh cloudflared --dest "${RUNNER_TEMP}/bin"',
    'cloudflared installs through fetch-pinned.sh',
  );
  const pins = readRepo('ops/backup/pins.env');
  assert.ok(/^CLOUDFLARED_VERSION=\d+\.\d+\.\d+$/m.test(pins), 'pins.env pins an exact version');
  assert.ok(/^CLOUDFLARED_SHA256_AMD64=[0-9a-f]{64}$/m.test(pins), 'pins.env pins a sha256');
  const config = stepByName(text, JOB, 'Prepare SSH Config file');
  assert.ok(
    config.run.includes('ProxyCommand ${RUNNER_TEMP}/bin/cloudflared '),
    'ProxyCommand uses the pinned binary',
  );
  assert.ok(!stripped.includes('/usr/bin/cloudflared'), 'no unpinned system cloudflared');
}

/** The VPS host key is pinned and every ssh/scp is strict and time-bounded; the job has a timeout. */
function assertSshHardening(text) {
  const stripped = stripComments(text);
  assert.ok(!stripped.includes('StrictHostKeyChecking=no'), 'host key checking is never off');
  const config = stepByName(text, JOB, 'Prepare SSH Config file');
  assert.ok(
    config.envBlock.some(
      (line) => line.trim() === 'KNOWN_HOSTS: ${{ secrets.PROD_SSH_KNOWN_HOSTS }}',
    ),
    'the known_hosts entry comes from the PROD_SSH_KNOWN_HOSTS secret',
  );
  assert.ok(config.run.includes('> ~/.ssh/known_hosts'), 'known_hosts is written before any ssh');
  const validate = stepByName(text, JOB, NAME_VALIDATE);
  assert.ok(
    validate.run.includes('[[ -z "${PROD_SSH_KNOWN_HOSTS}" ]]'),
    'a missing host key pin fails before any exchange',
  );
  const write = stepByName(text, JOB, NAME_WRITE);
  const remoteCalls = write.run.split('\n').filter((line) => /\b(ssh|scp) -T/.test(line));
  assert.ok(remoteCalls.length >= 4, 'the key write has its ssh and scp calls');
  for (const line of remoteCalls) {
    assert.ok(line.includes('-o StrictHostKeyChecking=yes'), `strict host key checking: ${line}`);
    assert.ok(line.includes('-o ConnectTimeout=20'), `connect timeout: ${line}`);
    assert.ok(line.includes('-o ServerAliveInterval=15'), `server alive interval: ${line}`);
  }
  assert.ok(
    jobLines(text, JOB).some((line) => /^ {4}timeout-minutes: [1-9]\d*$/.test(line)),
    'the job has timeout-minutes',
  );
}

function assertReadinessAndOnboardingNames(text) {
  const ready = stepByName(text, JOB, NAME_READY);
  assert.ok(
    ready.run.includes('ops/notifs-ops/wait-ready.sh https://notifs-delivery.4irl.app/readyz'),
    'poll runs wait-ready.sh with the readyz URL',
  );
  assert.ok(!ready.run.includes('curl'), 'no inline curl loop');
  const lint = stepByName(text, JOB, NAME_LINT);
  assert.equal(lint.run, LINT_RUN);
  assert.deepEqual(lint.envKeys, [], 'lint has no env');
  assert.ok(lint.envBlock.length === 0, 'lint has no env block');
  const apply = stepByName(text, JOB, NAME_APPLY);
  assert.equal(apply.run, APPLY_RUN);
  assert.deepEqual([...apply.envKeys].sort(), [...APPLY_ENV_KEYS].sort(), 'apply env keys exact');
}

function assertSecretsPlacement(keyText, deployText, buildText) {
  const steps = stepsOf(keyText, JOB);
  const validate = indexOfStep(steps, (s) => s.name === NAME_VALIDATE, 'validate');
  const firstExternal = indexOfStep(
    steps,
    (s) => s.name === NAME_SSH_KEY || s.uses === './.github/actions/notifs-app',
    'first ssh or notifs-app step',
  );
  assert.ok(validate < firstExternal, 'secrets validated before the first ssh/notifs-app step');
  const validation = steps[validate];
  for (const secret of OPS_SECRETS) {
    assert.ok(
      validation.envBlock.some((line) => line.includes(`secrets.${secret}`)),
      secret,
    );
  }
  assert.ok(
    validation.run.includes('https://discord.com/api/webhooks/'),
    'webhook prefix is validated',
  );
  const apply = stepByName(keyText, JOB, NAME_APPLY);
  assert.ok(
    apply.envBlock.some(
      (line) =>
        line.trim() === 'NOTIFS_OPS_DISCORD_WEBHOOK: ${{ secrets.NOTIFS_OPS_DISCORD_WEBHOOK }}',
    ),
    'webhook is set in the apply env',
  );
  for (const text of [deployText, buildText]) {
    assert.ok(
      !stripComments(text).includes('NOTIFS_OPS_'),
      'stack deploy has no NOTIFS_OPS_ secrets',
    );
  }
}

// ---------------------------------------------------------------------------------------------
// Grounding: names the workflow depends on are still read by the CLI
// ---------------------------------------------------------------------------------------------

function assertGrounding() {
  const client = readRepo('scripts/onboard-client.mjs');
  const cli = readRepo('scripts/onboard.mjs');
  assert.ok(client.includes('ONBOARD_URL'), 'ONBOARD_URL is read by onboard-client.mjs');
  for (const name of ['ONBOARD_APP_ACCESS_ID', 'ONBOARD_APP_ACCESS_SECRET', 'ONBOARD_APP_KEY']) {
    assert.ok(cli.includes(name), `${name} is read by onboard.mjs`);
  }
  for (const fragment of ["arg === '--manifest'", "arg === '--target'", "TARGET_APP = 'app'"]) {
    assert.ok(cli.includes(fragment), `parseArgs still accepts ${fragment}`);
  }
  assert.ok(cli.includes("'lint'") && cli.includes("'apply'"), 'lint and apply commands exist');
}

// ---------------------------------------------------------------------------------------------
// Mutation helper
// ---------------------------------------------------------------------------------------------

function mutate(text, from, to) {
  assert.ok(text.includes(from), `mutation target present: ${from}`);
  return text.replace(from, to);
}

function assertFails(assertion, ...args) {
  assert.throws(() => assertion(...args), undefined, 'the mutated fixture must fail the assertion');
}

// ---------------------------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------------------------

describe('notifs-ops-key.yml and the stack deploy workflows', () => {
  it('triggers on push to main only', () => {
    assertKeyWorkflowTriggers(KEY_WORKFLOW);
    assertNoJobUsesKeyWorkflow(BUILD_AND_DEPLOY);
    assertNoJobUsesKeyWorkflow(DEPLOY);
  });

  it('control: trigger rules fail on violating fixtures', () => {
    assertFails(
      assertKeyWorkflowTriggers,
      mutate(
        KEY_WORKFLOW,
        '  push:\n    branches: [main]',
        '  pull_request:\n    branches: [main]',
      ),
    );
    assertFails(
      assertKeyWorkflowTriggers,
      mutate(KEY_WORKFLOW, 'branches: [main]', 'branches: [dev]'),
    );
    assertFails(
      assertKeyWorkflowTriggers,
      mutate(KEY_WORKFLOW, '  workflow_dispatch:', '  workflow_run:\n  workflow_dispatch:'),
    );
    assertFails(
      assertKeyWorkflowTriggers,
      mutate(KEY_WORKFLOW, " || github.ref == 'refs/heads/main'", ''),
    );
    assertFails(
      assertNoJobUsesKeyWorkflow,
      `${BUILD_AND_DEPLOY}\n  extra:\n    uses: ./.github/workflows/notifs-ops-key.yml\n`,
    );
  });

  it('keeps id-token out of the stack deploy and read-all out of everything', () => {
    assertDeployProdPermissions(BUILD_AND_DEPLOY);
    assertNoIdTokenInDeployWorkflows(DEPLOY, BUILD_AND_DEPLOY);
    assertKeyWorkflowPermissions(KEY_WORKFLOW);
  });

  it('control: permission rules fail on violating fixtures', () => {
    assertFails(
      assertDeployProdPermissions,
      mutate(
        BUILD_AND_DEPLOY,
        '    permissions:\n      contents: read\n      packages: read\n    secrets:',
        '    permissions: read-all\n    secrets:',
      ),
    );
    assertFails(
      assertDeployProdPermissions,
      mutate(
        BUILD_AND_DEPLOY,
        '      packages: read\n    secrets:',
        '      packages: read\n      id-token: write\n    secrets:',
      ),
    );
    assertFails(
      assertNoIdTokenInDeployWorkflows,
      mutate(
        DEPLOY,
        'permissions:\n  contents: read',
        'permissions:\n  id-token: write\n  contents: read',
      ),
      BUILD_AND_DEPLOY,
    );
    assertFails(assertKeyWorkflowPermissions, mutate(KEY_WORKFLOW, '      id-token: write\n', ''));
    assertFails(
      assertKeyWorkflowPermissions,
      mutate(
        KEY_WORKFLOW,
        '    permissions:\n      contents: read',
        '    permissions: read-all\n      contents: read',
      ),
    );
  });

  it('sets environment: production', () => {
    assertProductionEnvironment(KEY_WORKFLOW);
    assertFails(
      assertProductionEnvironment,
      mutate(KEY_WORKFLOW, '    environment: production\n', ''),
    );
  });

  it('mints once and keys the write on the same exchange id', () => {
    assertMintOnce(KEY_WORKFLOW);
  });

  it('control: mint-once rules fail on violating fixtures', () => {
    const doubled = mutate(
      KEY_WORKFLOW,
      '      - name: Set up Node',
      '      - uses: ./.github/actions/notifs-app\n        id: second\n        with:\n          app_id: notifs_ops\n\n      - name: Set up Node',
    );
    assertFails(assertMintOnce, doubled);
    assertFails(
      assertMintOnce,
      mutate(
        KEY_WORKFLOW,
        '          access_client_id: ${{ secrets.NOTIFS_OPS_ACCESS_CLIENT_ID }}',
        '          manifest: ops/notifs-ops/manifest.json\n          access_client_id: ${{ secrets.NOTIFS_OPS_ACCESS_CLIENT_ID }}',
      ),
    );
    assertFails(
      assertMintOnce,
      mutate(
        KEY_WORKFLOW,
        "if: always() && steps.notifs-ops-exchange.outputs.key != ''",
        "if: steps.notifs-ops-exchange.outputs.key != ''",
      ),
    );
    assertFails(
      assertMintOnce,
      mutate(
        KEY_WORKFLOW,
        'ONBOARD_APP_KEY: ${{ steps.notifs-ops-exchange.outputs.key }}',
        'ONBOARD_APP_KEY: ${{ steps.other.outputs.key }}',
      ),
    );
  });

  it('orders validate, readyz, exchange, lint, apply, key write (last)', () => {
    assertStepOrder(KEY_WORKFLOW);
  });

  it('control: order rules fail on violating fixtures', () => {
    assertFails(assertStepOrder, `${KEY_WORKFLOW}\n      - name: After\n        run: echo after\n`);
    assertFails(
      assertStepOrder,
      mutate(KEY_WORKFLOW, 'chmod 600 ~/.ssh/config', 'chmod 644 ~/.ssh/config'),
    );
    const swapped = KEY_WORKFLOW.replace('Lint the notifs_ops manifest', 'TMP_SWAP')
      .replace('Apply the notifs_ops manifest', 'Lint the notifs_ops manifest')
      .replace('TMP_SWAP', 'Apply the notifs_ops manifest');
    assertFails(assertStepOrder, swapped);
  });

  it('keeps always() off lint and apply and continue-on-error off the exchange', () => {
    assertFailureConditions(KEY_WORKFLOW);
    assertFails(
      assertFailureConditions,
      mutate(
        KEY_WORKFLOW,
        '      - name: Apply the notifs_ops manifest\n',
        '      - name: Apply the notifs_ops manifest\n        if: always()\n',
      ),
    );
    assertFails(
      assertFailureConditions,
      mutate(
        KEY_WORKFLOW,
        '        id: notifs-ops-exchange\n',
        '        id: notifs-ops-exchange\n        continue-on-error: true\n',
      ),
    );
  });

  it('delivers the key over a separate channel from the script', () => {
    assertKeyDelivery(KEY_WORKFLOW);
  });

  it('control: delivery rules fail on violating fixtures', () => {
    assertFails(
      assertKeyDelivery,
      mutate(
        KEY_WORKFLOW,
        '"${SSH_HOST}:${remote_script}"',
        '"${SSH_HOST}:${remote_script}" "${NOTIFS_OPS_KEY}"',
      ),
    );
    assertFails(
      assertKeyDelivery,
      mutate(
        KEY_WORKFLOW,
        'ssh -T -q -o StrictHostKeyChecking=yes -o ConnectTimeout=20 -o ServerAliveInterval=15 "${SSH_HOST}" "trap',
        'ssh -T -q -o StrictHostKeyChecking=yes -o ConnectTimeout=20 -o ServerAliveInterval=15 "${SSH_HOST}" bash -s "trap',
      ),
    );
    assertFails(
      assertKeyDelivery,
      mutate(
        KEY_WORKFLOW,
        'printf \'%s\' "${NOTIFS_OPS_KEY}" | ssh',
        'echo "${NOTIFS_OPS_KEY}" | ssh',
      ),
    );
    assertFails(
      assertKeyDelivery,
      mutate(KEY_WORKFLOW, "trap 'rm -f ${remote_script}' EXIT; ", ''),
    );
    assertFails(
      assertKeyDelivery,
      mutate(
        KEY_WORKFLOW,
        '          SSH_HOST: ${{ secrets.PROD_SSH_HOST }}\n          NOTIFS_OPS_KEY',
        '          SSH_HOST: ${{ secrets.PROD_SSH_HOST }}\n          EXTRA: x\n          NOTIFS_OPS_KEY',
      ),
    );
  });

  it('installs a sha256-pinned cloudflared', () => {
    assertCloudflaredPinned(KEY_WORKFLOW);
  });

  it('control: cloudflared pin rules fail on violating fixtures', () => {
    assertFails(
      assertCloudflaredPinned,
      mutate(
        KEY_WORKFLOW,
        'bash ops/backup/fetch-pinned.sh cloudflared --dest "${RUNNER_TEMP}/bin"',
        'sudo curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/bin/cloudflared',
      ),
    );
    assertFails(
      assertCloudflaredPinned,
      mutate(KEY_WORKFLOW, '${RUNNER_TEMP}/bin/cloudflared access', '/usr/bin/cloudflared access'),
    );
  });

  it('pins the VPS host key and bounds every ssh call and the job', () => {
    assertSshHardening(KEY_WORKFLOW);
  });

  it('control: ssh hardening rules fail on violating fixtures', () => {
    assertFails(
      assertSshHardening,
      mutate(
        KEY_WORKFLOW,
        'scp -T -q -o StrictHostKeyChecking=yes',
        'scp -T -q -o StrictHostKeyChecking=no',
      ),
    );
    assertFails(
      assertSshHardening,
      mutate(
        KEY_WORKFLOW,
        'scp -T -q -o StrictHostKeyChecking=yes -o ConnectTimeout=20',
        'scp -T -q -o StrictHostKeyChecking=yes',
      ),
    );
    assertFails(
      assertSshHardening,
      mutate(
        KEY_WORKFLOW,
        '-o ServerAliveInterval=15 "${SSH_HOST}" mktemp',
        '"${SSH_HOST}" mktemp',
      ),
    );
    assertFails(assertSshHardening, mutate(KEY_WORKFLOW, '    timeout-minutes: 25\n', ''));
    assertFails(
      assertSshHardening,
      mutate(KEY_WORKFLOW, '          KNOWN_HOSTS: ${{ secrets.PROD_SSH_KNOWN_HOSTS }}\n', ''),
    );
    assertFails(
      assertSshHardening,
      mutate(KEY_WORKFLOW, '[[ -z "${PROD_SSH_KNOWN_HOSTS}" ]]', '[[ -z "${OTHER}" ]]'),
    );
  });

  it('uses the checked-in wait-ready and the exact onboard command lines', () => {
    assertReadinessAndOnboardingNames(KEY_WORKFLOW);
    assertGrounding();
  });

  it('control: onboarding-name rules fail on violating fixtures', () => {
    assertFails(
      assertReadinessAndOnboardingNames,
      mutate(KEY_WORKFLOW, 'ONBOARD_APP_KEY:', 'ONBOARD_KEY:'),
    );
    assertFails(
      assertReadinessAndOnboardingNames,
      mutate(KEY_WORKFLOW, `${APPLY_RUN}`, APPLY_RUN.replace(' --target app', '')),
    );
    assertFails(
      assertReadinessAndOnboardingNames,
      mutate(
        KEY_WORKFLOW,
        `        run: ${LINT_RUN}`,
        `        env:\n          X: y\n        run: ${LINT_RUN}`,
      ),
    );
    assertFails(
      assertReadinessAndOnboardingNames,
      mutate(
        KEY_WORKFLOW,
        '          ONBOARD_URL: https://notifs-delivery.4irl.app\n',
        '          ONBOARD_URL: https://notifs-delivery.4irl.app\n          ONBOARD_EXTRA: x\n',
      ),
    );
    assertFails(
      assertReadinessAndOnboardingNames,
      mutate(
        KEY_WORKFLOW,
        `${APPLY_RUN}`,
        APPLY_RUN.replace('ops/notifs-ops/manifest.json', 'ops/other/manifest.json'),
      ),
    );
    assertFails(
      assertReadinessAndOnboardingNames,
      mutate(
        KEY_WORKFLOW,
        'run: bash ops/notifs-ops/wait-ready.sh',
        'run: curl -fsS https://notifs-delivery.4irl.app/readyz #',
      ),
    );
  });

  it('validates the NOTIFS_OPS secrets first and keeps them out of the stack deploy', () => {
    assertSecretsPlacement(KEY_WORKFLOW, DEPLOY, BUILD_AND_DEPLOY);
  });

  it('control: secrets rules fail on violating fixtures', () => {
    assertFails(
      assertSecretsPlacement,
      mutate(
        KEY_WORKFLOW,
        '      - name: Validate NOTIFS_OPS secrets',
        '      - name: Install SSH key\n        run: echo early\n\n      - name: Validate NOTIFS_OPS secrets',
      ),
      DEPLOY,
      BUILD_AND_DEPLOY,
    );
    assertFails(
      assertSecretsPlacement,
      mutate(
        KEY_WORKFLOW,
        '          NOTIFS_OPS_DISCORD_WEBHOOK: ${{ secrets.NOTIFS_OPS_DISCORD_WEBHOOK }}\n        run: node scripts/onboard.mjs apply',
        '        run: node scripts/onboard.mjs apply',
      ),
      DEPLOY,
      BUILD_AND_DEPLOY,
    );
    assertFails(
      assertSecretsPlacement,
      KEY_WORKFLOW,
      `${DEPLOY}\n# x\n  NOTIFS_OPS_DISCORD_WEBHOOK:\n`,
      BUILD_AND_DEPLOY,
    );
    assertFails(
      assertSecretsPlacement,
      KEY_WORKFLOW.replaceAll('https://discord.com/api/webhooks/', 'https://example.test/'),
      DEPLOY,
      BUILD_AND_DEPLOY,
    );
  });
});

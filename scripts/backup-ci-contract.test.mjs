// Structural contract test for the CI wiring of the ops/backup suites: lint.yml installs the pinned shellcheck and
// then runs `make backup-lint`, test.yml runs `make backup-test`, and test-integration.yml installs the pinned
// restic before `make backup-test-live` with REQUIRE_DOCKER=1 (so CI fails instead of skipping). No YAML parser: the
// comment-stripped text is split into steps at the indent-6 `- ` markers. Every assertion has a positive control: a
// minimal mutation of a fixture STRING (never the real file) must make the same assertion throw.
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const readRepo = (rel) => readFileSync(path.join(ROOT, rel), 'utf8');

const LINT = readRepo('.github/workflows/lint.yml');
const TEST = readRepo('.github/workflows/test.yml');
const INTEGRATION = readRepo('.github/workflows/test-integration.yml');

const SHELLCHECK_INSTALL = 'bash ops/backup/fetch-pinned.sh shellcheck --dest .local/bin';
const RESTIC_INSTALL = 'bash ops/backup/fetch-pinned.sh restic --dest .local/bin';

function stripComments(text) {
  return text
    .split('\n')
    .filter((line) => !line.trimStart().startsWith('#'))
    .join('\n');
}

/** Steps of the whole workflow in file order: the lines of each `      - ` item up to the next one. */
function stepsOf(text) {
  const steps = [];
  for (const line of stripComments(text).split('\n')) {
    if (/^ {6}- /.test(line)) steps.push([line]);
    else if (steps.length > 0 && /^ {8}/.test(line)) steps[steps.length - 1].push(line);
  }
  return steps.map((lines) => lines.join('\n'));
}

const runLine = (step) => /^ {8}run: (.+)$/m.exec(step)?.[1].trim();

function indexOfRun(steps, command) {
  const index = steps.findIndex((step) => runLine(step) === command);
  assert.ok(index !== -1, `a step runs exactly: ${command}`);
  return index;
}

function assertLintWiring(text) {
  const steps = stepsOf(text);
  const install = indexOfRun(steps, SHELLCHECK_INSTALL);
  const lint = indexOfRun(steps, 'make backup-lint');
  assert.ok(install < lint, 'the pinned shellcheck is installed before make backup-lint');
  assert.ok(
    !stripComments(text).includes('apt-get install'),
    'shellcheck never comes from apt (it must be the sha256-pinned release)',
  );
}

function assertUnitTestWiring(text) {
  indexOfRun(stepsOf(text), 'make backup-test');
}

function assertIntegrationWiring(text) {
  const steps = stepsOf(text);
  const install = indexOfRun(steps, RESTIC_INSTALL);
  const live = indexOfRun(steps, 'make backup-test-live');
  assert.ok(install < live, 'the pinned restic is installed before make backup-test-live');
  assert.match(
    steps[live],
    /^ {8}env:\n(?: {10}\S.*\n)*? {10}REQUIRE_DOCKER: ["']?1["']?$/m,
    'the live round trip runs with REQUIRE_DOCKER=1 so CI fails instead of skipping',
  );
}

function mutate(text, from, to) {
  assert.ok(text.includes(from), `mutation target present: ${from}`);
  return text.replace(from, to);
}

function assertFails(assertion, ...args) {
  assert.throws(() => assertion(...args), undefined, 'the mutated fixture must fail the assertion');
}

describe('CI wiring of the ops/backup suites', () => {
  it('lint.yml installs the pinned shellcheck, then runs make backup-lint', () => {
    assertLintWiring(LINT);
  });

  it('control: lint wiring fails on violating fixtures', () => {
    assertFails(assertLintWiring, mutate(LINT, 'make backup-lint', 'make lint-backup'));
    assertFails(assertLintWiring, mutate(LINT, SHELLCHECK_INSTALL, 'echo skipped'));
    assertFails(
      assertLintWiring,
      mutate(LINT, SHELLCHECK_INSTALL, 'sudo apt-get install shellcheck'),
    );
    const swapped = LINT.replace('make backup-lint', 'TMP_SWAP')
      .replace(SHELLCHECK_INSTALL, 'make backup-lint')
      .replace('TMP_SWAP', SHELLCHECK_INSTALL);
    assertFails(assertLintWiring, swapped);
  });

  it('test.yml runs make backup-test', () => {
    assertUnitTestWiring(TEST);
  });

  it('control: unit-test wiring fails on a violating fixture', () => {
    assertFails(assertUnitTestWiring, mutate(TEST, 'make backup-test', 'make backup-tests'));
  });

  it('test-integration.yml installs the pinned restic before make backup-test-live with REQUIRE_DOCKER=1', () => {
    assertIntegrationWiring(INTEGRATION);
  });

  it('control: integration wiring fails on violating fixtures', () => {
    assertFails(assertIntegrationWiring, mutate(INTEGRATION, RESTIC_INSTALL, 'echo skipped'));
    assertFails(
      assertIntegrationWiring,
      mutate(INTEGRATION, 'REQUIRE_DOCKER: "1"', 'REQUIRE_DOCKER: "0"'),
    );
    assertFails(
      assertIntegrationWiring,
      mutate(INTEGRATION, 'make backup-test-live', 'NBLIVE_SKIP=1 make backup-test-live'),
    );
    const swapped = INTEGRATION.replace('make backup-test-live', 'TMP_SWAP')
      .replace(RESTIC_INSTALL, 'make backup-test-live')
      .replace('TMP_SWAP', RESTIC_INSTALL);
    assertFails(assertIntegrationWiring, swapped);
  });
});

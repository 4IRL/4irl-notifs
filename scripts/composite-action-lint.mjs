#!/usr/bin/env node
/**
 * Lets `make workflows-lint` run actionlint over composite actions.
 *
 * actionlint (v1.7.x) only understands workflow files: pointed at an action metadata file
 * (`.github/actions/<name>/action.yml`) it reports "jobs section is missing" and nothing else. So this
 * script turns each composite action into a throwaway one-job workflow that carries exactly the
 * action's steps, and actionlint then checks the real expressions, `shell:` blocks (via
 * shellcheck when installed), step ids and `steps.<id>.outputs` references.
 *
 * Line numbers of the steps in the wrapper equal line numbers in the source `action.yml` (the header
 * is padded with comments up to the `steps:` line), so an actionlint finding in a step points at the
 * right source line. Columns shift by two (steps gain one indent level). The wrapper declares the
 * action's inputs as `workflow_call` string inputs so `inputs.<name>` resolves, and maps the
 * action's top-level `outputs:` `value:` expressions onto the wrapper job's `outputs:` (one line in
 * the padded header, so a bad `steps.<id>` reference there is linted too; a finding there points at
 * that header line, not at the source line).
 *
 * actionlint treats the steps as workflow steps, where `shell:` is optional, while a composite
 * action's `run:` step must declare `shell:` (GitHub rejects the action at load time otherwise).
 * That rule is enforced here: a `run:` step without a `shell:` key throws.
 *
 * Usage: node scripts/composite-action-lint.mjs [<out dir, default .local/action-lint>]
 *   prints one wrapper path per line. The wrappers are gitignored build output.
 *
 * Limits (the action files here follow them, a violation throws): `inputs:`, `outputs:` and `runs:`
 * are top-level, `  steps:` is the last key and indented two spaces under `runs:` (only comments
 * and blank lines may follow the steps), and each step is a `- ` list item at one consistent indent.
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { REPO_ROOT } from './ports.mjs';

const ACTIONS_DIR = path.join(REPO_ROOT, '.github', 'actions');
const DEFAULT_OUT_DIR = path.join(REPO_ROOT, '.local', 'action-lint');
const STEPS_PATTERN = /^ {2}steps:\s*(#.*)?$/;
const MIN_HEADER_LINES = 5;
const BLANK_OR_COMMENT = /^\s*(#.*)?$/;

function splitLines(text) {
  return text.replace(/\r\n?/g, '\n').split('\n');
}

/** Names declared directly under the top-level `inputs:` key. */
export function inputNames(rawLines) {
  const lines = rawLines.map((line) => line.replace(/\r$/, ''));
  const start = lines.findIndex((line) => /^inputs:\s*(\{\s*\})?\s*(#.*)?$/.test(line));
  if (start < 0 || /^inputs:\s*\{/.test(lines[start])) return [];
  const names = [];
  for (const line of lines.slice(start + 1)) {
    if (BLANK_OR_COMMENT.test(line)) continue;
    if (!line.startsWith(' ')) break;
    const match = /^ {2}([A-Za-z_][A-Za-z0-9_-]*):\s*(#.*)?$/.exec(line);
    if (match) names.push(match[1]);
  }
  return names;
}

/** `{name, value}` for each single-line `value:` under the top-level `outputs:` key. */
export function outputValues(rawLines) {
  const lines = rawLines.map((line) => line.replace(/\r$/, ''));
  const start = lines.findIndex((line) => /^outputs:\s*(#.*)?$/.test(line));
  if (start < 0) return [];
  const outputs = [];
  let current = null;
  for (const line of lines.slice(start + 1)) {
    if (BLANK_OR_COMMENT.test(line)) continue;
    if (!line.startsWith(' ')) break;
    const nameMatch = /^ {2}([A-Za-z_][A-Za-z0-9_-]*):\s*(#.*)?$/.exec(line);
    if (nameMatch) {
      current = nameMatch[1];
      continue;
    }
    const valueMatch = /^ {4}value:\s*(.+?)\s*$/.exec(line);
    if (current && valueMatch && !/^[|>]/.test(valueMatch[1])) {
      outputs.push({ name: current, value: valueMatch[1].replace(/^(['"])(.*)\1$/, '$2') });
    }
  }
  return outputs;
}

/**
 * Throws when a `run:` step has no `shell:` key. Steps are the `- ` items under `steps:`; a key
 * is a direct child of its step (the first key follows the `- `).
 */
function assertEveryRunHasShell(stepLines, firstLineNumber, source) {
  const first = stepLines.find((line) => !BLANK_OR_COMMENT.test(line));
  if (first === undefined) return;
  const stepIndent = /^ */.exec(first)[0].length;
  const keyIndent = stepIndent + 2;
  const itemStart = new RegExp(`^ {${stepIndent}}- `);
  const firstKey = new RegExp(`^ {${stepIndent}}- ([A-Za-z_][A-Za-z0-9_-]*):`);
  const laterKey = new RegExp(`^ {${keyIndent}}([A-Za-z_][A-Za-z0-9_-]*):`);

  let step = null;
  const finish = () => {
    if (step && step.keys.has('run') && !step.keys.has('shell')) {
      throw new Error(
        `${source}:${step.line}: a composite action's run step must declare shell: (GitHub rejects it otherwise)`,
      );
    }
  };
  for (const [i, line] of stepLines.entries()) {
    if (itemStart.test(line)) {
      finish();
      step = { line: firstLineNumber + i, keys: new Set() };
      const match = firstKey.exec(line);
      if (match) step.keys.add(match[1]);
      continue;
    }
    const match = step && laterKey.exec(line);
    if (match) step.keys.add(match[1]);
  }
  finish();
}

/** The wrapper workflow text for one composite action's text. `source` only labels errors. */
export function buildWrapper(actionText, source = 'action.yml') {
  const lines = splitLines(actionText);
  const stepsIndex = lines.findIndex((line) => STEPS_PATTERN.test(line));
  if (stepsIndex < 0) {
    throw new Error(`${source}: no "steps:" line at two-space indent (composite only)`);
  }
  if (stepsIndex < MIN_HEADER_LINES) {
    throw new Error(`${source}: "steps:" is on line ${stepsIndex + 1}, too early to wrap`);
  }
  const stepLines = lines.slice(stepsIndex + 1);
  const stray = stepLines.findIndex(
    (line) => !BLANK_OR_COMMENT.test(line) && !line.startsWith(' '),
  );
  if (stray >= 0) {
    throw new Error(
      `${source}:${stepsIndex + 2 + stray}: "steps:" must be the last key under runs: (found "${stepLines[stray].trim()}" after it)`,
    );
  }
  assertEveryRunHasShell(stepLines, stepsIndex + 2, source);

  const inputs = inputNames(lines)
    .map((name) => `${name}: {type: string}`)
    .join(', ');
  const header = [
    'name: composite-action-wrapper',
    inputs ? `on: {workflow_call: {inputs: {${inputs}}}}` : 'on: workflow_call',
    'jobs:',
    '  composite:',
    '    runs-on: ubuntu-latest',
  ];
  // Output expressions need one header line; with no room (a very early `steps:`) skip them.
  const outputs = outputValues(lines)
    .map(({ name, value }) => `${name}: ${JSON.stringify(value)}`)
    .join(', ');
  if (outputs && header.length + 1 <= stepsIndex) header.push(`    outputs: {${outputs}}`);
  let labelled = false;
  while (header.length < stepsIndex) {
    header.push(labelled ? '#' : `# generated from ${source}`);
    labelled = true;
  }
  const steps = stepLines.map((line) => (line === '' ? line : `  ${line}`));
  return [...header, '    steps:', ...steps].join('\n');
}

/** Writes one wrapper per `.github/actions/<name>/action.yml`; returns the written paths. */
export function writeWrappers({ actionsDir = ACTIONS_DIR, outDir = DEFAULT_OUT_DIR } = {}) {
  fs.mkdirSync(outDir, { recursive: true });
  for (const stale of fs.readdirSync(outDir)) {
    if (stale.endsWith('.yml')) fs.rmSync(path.join(outDir, stale));
  }
  const written = [];
  if (!fs.existsSync(actionsDir)) return written;
  for (const entry of fs.readdirSync(actionsDir, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const source = path.join(actionsDir, entry.name, 'action.yml');
    if (!fs.existsSync(source)) continue;
    const target = path.join(outDir, `${entry.name}.yml`);
    fs.writeFileSync(target, `${buildWrapper(fs.readFileSync(source, 'utf8'), source)}\n`);
    written.push(target);
  }
  return written;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const outDir = process.argv[2] ? path.resolve(process.argv[2]) : DEFAULT_OUT_DIR;
    for (const file of writeWrappers({ outDir })) console.log(file);
  } catch (err) {
    console.error(err.message);
    process.exitCode = 1;
  }
}

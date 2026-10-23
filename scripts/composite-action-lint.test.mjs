import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, describe, it } from 'node:test';

import { buildWrapper, inputNames, outputValues, writeWrappers } from './composite-action-lint.mjs';

const roots = [];

after(() => {
  for (const root of roots) fs.rmSync(root, { recursive: true, force: true });
});

function tmpDir() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'composite-lint-test-'));
  roots.push(root);
  return root;
}

const ACTION = [
  'name: demo', // 1
  'description: demo action', // 2
  '', // 3
  'inputs:', // 4
  '  app_id:', // 5
  '    description: id', // 6
  '    required: true', // 7
  '  audience:', // 8
  "    default: 'x'", // 9
  '', // 10
  'runs:', // 11
  '  using: composite', // 12
  '  steps:', // 13
  '    - id: one', // 14
  '      shell: bash', // 15
  '      run: |', // 16
  '        echo hi', // 17
  '',
].join('\n');

describe('inputNames', () => {
  it('lists only the keys directly under inputs:', () => {
    assert.deepEqual(inputNames(ACTION.split('\n')), ['app_id', 'audience']);
  });

  it('returns nothing without an inputs block', () => {
    assert.deepEqual(inputNames(['name: x', 'runs:']), []);
  });
});

describe('buildWrapper', () => {
  it('keeps every step on its source line number', () => {
    const wrapper = buildWrapper(ACTION).split('\n');

    assert.equal(wrapper[12], '    steps:');
    assert.equal(wrapper[13], '      - id: one');
    assert.equal(wrapper[16], '          echo hi');
  });

  it('declares the action inputs as workflow_call string inputs', () => {
    const wrapper = buildWrapper(ACTION);

    assert.match(
      wrapper,
      /^on: \{workflow_call: \{inputs: \{app_id: \{type: string\}, audience: \{type: string\}\}\}\}$/m,
    );
    assert.match(wrapper, /^ {4}runs-on: ubuntu-latest$/m);
  });

  it('refuses a file without a two-space steps: line', () => {
    assert.throws(() => buildWrapper('name: x\nruns:\n  using: node20\n'), /composite only/);
  });

  it('refuses a steps: line too early to pad a header before', () => {
    assert.throws(() => buildWrapper('name: x\nruns:\n  steps:\n    - run: x\n'), /too early/);
  });

  it('treats a CRLF file exactly like its LF twin', () => {
    assert.equal(buildWrapper(ACTION.replace(/\n/g, '\r\n')), buildWrapper(ACTION));
  });

  it('keeps later inputs when a comment or blank line sits inside the inputs block', () => {
    const action = ACTION.replace(
      "  audience:\n    default: 'x'",
      "# a column-zero comment\n\n  audience:\n    default: 'x'",
    );

    assert.deepEqual(inputNames(action.split('\n')), ['app_id', 'audience']);
    assert.deepEqual(inputNames(action.replace(/\n/g, '\r\n').split('\n')), ['app_id', 'audience']);
  });

  it('accepts inputs: with a trailing comment and treats inputs: {} as empty', () => {
    assert.deepEqual(inputNames(['inputs: # none yet', '  a:', '    x: 1']), ['a']);
    assert.deepEqual(inputNames(['inputs: {}', 'runs:']), []);
  });

  it('falls back to a bare workflow_call trigger when the action has no inputs', () => {
    const lines = ACTION.split('\n');
    const withoutInputs = [...lines.slice(0, 3), ...lines.slice(10)].join('\n');
    // Keep `steps:` at line 8 (index >= 5) so the wrapper can be built.
    const wrapper = buildWrapper(`${'# pad\n'.repeat(3)}${withoutInputs}`);

    assert.match(wrapper, /^on: workflow_call$/m);
    assert.ok(!wrapper.includes('inputs'));
  });

  it('refuses a key after steps: instead of indenting it into the steps', () => {
    assert.throws(
      () => buildWrapper(`${ACTION}branding:\n  icon: x\n`),
      /steps:" must be the last key under runs: \(found "branding:" after it\)/,
    );
    const withComment = `${ACTION}# trailing comment\n\n`;
    assert.doesNotThrow(() => buildWrapper(withComment));
  });
});

describe('buildWrapper: composite shell: rule', () => {
  const withSteps = (stepsText) =>
    ['name: x', 'description: y', '', 'inputs:', '  a:', '    required: true', '', 'runs:']
      .concat(['  using: composite', '  steps:'])
      .concat(stepsText)
      .join('\n');

  it('throws, naming the step line, for a run step without shell', () => {
    const action = withSteps([
      '    - name: ok',
      '      shell: bash',
      '      run: echo ok',
      '',
      '    - run: echo hi',
      '',
    ]);

    assert.throws(
      () => buildWrapper(action, 'demo/action.yml'),
      /demo\/action\.yml:15:.*must declare shell:/,
    );
  });

  it('throws when run is not the first key of its step', () => {
    const action = withSteps(['    - name: x', '      id: one', '      run: echo hi']);

    assert.throws(() => buildWrapper(action), /must declare shell:/);
  });

  it('accepts shell before run, after run, and as the first key', () => {
    for (const steps of [
      ['    - name: x', '      shell: bash', '      run: echo hi'],
      ['    - name: x', '      run: echo hi', '      shell: bash'],
      ['    - shell: bash', '      run: echo hi'],
      ['    - run: echo hi', '      shell: bash'],
    ]) {
      assert.doesNotThrow(() => buildWrapper(withSteps(steps)), steps.join('\n'));
    }
  });

  it('ignores a "run:" that is only text inside a script or another step', () => {
    const action = withSteps([
      '    - name: x',
      '      shell: bash',
      '      run: |',
      '        run: not a key',
      '    - uses: actions/checkout@v1',
      '      with:',
      '        run: nested',
    ]);

    assert.doesNotThrow(() => buildWrapper(action));
  });
});

describe('buildWrapper: outputs', () => {
  const ACTION_WITH_OUTPUTS = [
    'name: demo', // 1
    'description: d', // 2
    '', // 3
    'inputs:', // 4
    '  a:', // 5
    '    required: true', // 6
    '', // 7
    'outputs:', // 8
    '  key:', // 9
    '    description: the key', // 10
    '    value: ${{ steps.one.outputs.key }}', // 11
    '', // 12
    'runs:', // 13
    '  using: composite', // 14
    '  steps:', // 15
    '    - id: one', // 16
    '      shell: bash', // 17
    '      run: echo hi', // 18
    '',
  ].join('\n');

  it('maps output value expressions onto the wrapper job outputs, in the padded header', () => {
    const wrapper = buildWrapper(ACTION_WITH_OUTPUTS).split('\n');

    assert.deepEqual(outputValues(ACTION_WITH_OUTPUTS.split('\n')), [
      { name: 'key', value: '${{ steps.one.outputs.key }}' },
    ]);
    assert.equal(wrapper[5], '    outputs: {key: "${{ steps.one.outputs.key }}"}');
    assert.equal(wrapper[14], '    steps:');
    assert.equal(wrapper[15], '      - id: one');
    assert.equal(wrapper[17], '        run: echo hi');
  });

  it('skips the outputs line silently when the header has no room for it', () => {
    const action = [
      'outputs:',
      '  key:',
      '    value: ${{ steps.one.outputs.key }}',
      'runs:',
      '  using: composite',
      '  steps:',
      '    - id: one',
      '      shell: bash',
      '      run: echo hi',
      '',
    ].join('\n');

    const wrapper = buildWrapper(action);

    assert.ok(!wrapper.includes('outputs: {'));
    assert.equal(wrapper.split('\n')[5], '    steps:');
  });
});

describe('writeWrappers', () => {
  it('writes one wrapper per action directory and clears stale ones', () => {
    const root = tmpDir();
    const actionsDir = path.join(root, 'actions');
    fs.mkdirSync(path.join(actionsDir, 'demo'), { recursive: true });
    fs.mkdirSync(path.join(actionsDir, 'no-action-file'), { recursive: true });
    fs.writeFileSync(path.join(actionsDir, 'demo', 'action.yml'), ACTION);
    const outDir = path.join(root, 'out');
    fs.mkdirSync(outDir);
    fs.writeFileSync(path.join(outDir, 'stale.yml'), 'x');

    const written = writeWrappers({ actionsDir, outDir });

    assert.deepEqual(written, [path.join(outDir, 'demo.yml')]);
    assert.deepEqual(fs.readdirSync(outDir), ['demo.yml']);
  });

  it('writes nothing when there is no actions directory', () => {
    const root = tmpDir();

    assert.deepEqual(
      writeWrappers({ actionsDir: path.join(root, 'missing'), outDir: path.join(root, 'out') }),
      [],
    );
  });
});

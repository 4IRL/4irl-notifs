// Behaviour test for ops/notifs-ops/write-key.sh: the key arrives on stdin only and lands as the
// bare 47-character key, mode 0600, written atomically from a temp file in the destination dir.
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import {
  chmodSync,
  existsSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../ops/notifs-ops/write-key.sh',
);
const KEY = `dak_${'A1b2C3d4'.repeat(5)}abc`; // dak_ + 43 characters
const OTHER_KEY = `dak_${'Z9y8X7w6'.repeat(5)}xyz`;

function withDir(fn) {
  const dir = mkdtempSync(path.join(tmpdir(), 'wk-'));
  try {
    return fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function run({ args, input, dir, umaskValue = '000', pathPrefix }) {
  const env = { ...process.env };
  if (pathPrefix) env.PATH = `${pathPrefix}:${env.PATH}`;
  return spawnSync('bash', ['-c', `umask ${umaskValue}; exec bash "$0" "$@"`, SCRIPT, ...args], {
    input,
    env,
    cwd: dir,
    encoding: 'utf8',
  });
}

function stubMv(dir, { fail }) {
  const bin = path.join(dir, 'bin');
  spawnSync('mkdir', ['-p', bin]);
  const log = path.join(dir, 'mv.log');
  const body = fail
    ? `#!/bin/sh\nprintf '%s\\n' "$*" >> "${log}"\nexit 1\n`
    : `#!/bin/sh\nprintf '%s\\n' "$*" >> "${log}"\nexec /bin/mv "$@"\n`;
  const file = path.join(bin, 'mv');
  writeFileSync(file, body);
  chmodSync(file, 0o755);
  return { bin, log };
}

describe('write-key.sh', () => {
  it('writes the bare key at 0600 with no newline and no leftovers', () => {
    withDir((dir) => {
      const dest = path.join(dir, 'KEY');
      const result = run({ args: [dest], input: KEY, dir });
      assert.equal(result.status, 0, result.stderr);
      assert.equal(readFileSync(dest, 'utf8'), KEY);
      assert.equal(KEY.length, 47);
      assert.equal(statSync(dest).mode & 0o777, 0o600);
      assert.deepEqual(readdirSync(dir), ['KEY']);
    });
  });

  it('one trailing LF produces the identical file', () => {
    withDir((dir) => {
      const dest = path.join(dir, 'KEY');
      const result = run({ args: [dest], input: `${KEY}\n`, dir });
      assert.equal(result.status, 0, result.stderr);
      assert.equal(readFileSync(dest, 'utf8'), KEY);
    });
  });

  it('replaces an existing key file', () => {
    withDir((dir) => {
      const dest = path.join(dir, 'KEY');
      writeFileSync(dest, OTHER_KEY);
      const result = run({ args: [dest], input: KEY, dir });
      assert.equal(result.status, 0, result.stderr);
      assert.equal(readFileSync(dest, 'utf8'), KEY);
      assert.deepEqual(readdirSync(dir), ['KEY']);
    });
  });

  const invalid = {
    'wrong prefix': `dax_${'a'.repeat(43)}`,
    '42 characters': `dak_${'a'.repeat(42)}`,
    '44 characters': `dak_${'a'.repeat(44)}`,
    'bad character': `dak_${'a'.repeat(42)}!`,
    empty: '',
    'multi-line': `${KEY}\n${KEY}`,
    'two trailing LFs': `${KEY}\n\n`,
    CRLF: `${KEY}\r\n`,
  };
  for (const [name, value] of Object.entries(invalid)) {
    it(`rejects ${name} and leaves an existing file untouched`, () => {
      withDir((dir) => {
        const dest = path.join(dir, 'KEY');
        writeFileSync(dest, OTHER_KEY);
        const result = run({ args: [dest], input: value, dir });
        assert.notEqual(result.status, 0);
        assert.equal(readFileSync(dest, 'utf8'), OTHER_KEY);
        assert.deepEqual(readdirSync(dir), ['KEY']);
      });
    });
  }

  it('uses a temp file in the destination directory for the mv', () => {
    withDir((dir) => {
      const dest = path.join(dir, 'sub', 'KEY');
      spawnSync('mkdir', ['-p', path.dirname(dest)]);
      const { bin, log } = stubMv(dir, { fail: false });
      const result = run({ args: [dest], input: KEY, dir, pathPrefix: bin });
      assert.equal(result.status, 0, result.stderr);
      const argv = readFileSync(log, 'utf8').trim().split('\n').pop().split(' ');
      const source = argv.at(-2);
      assert.equal(path.dirname(source), path.dirname(dest));
      assert.equal(argv.at(-1), dest);
      assert.equal(readFileSync(dest, 'utf8'), KEY);
    });
  });

  it('a failing mv exits non-zero, removes the temp file and keeps the old key', () => {
    withDir((dir) => {
      const keyDir = path.join(dir, 'keys');
      spawnSync('mkdir', ['-p', keyDir]);
      const dest = path.join(keyDir, 'KEY');
      writeFileSync(dest, OTHER_KEY);
      const { bin } = stubMv(dir, { fail: true });
      const result = run({ args: [dest], input: KEY, dir, pathPrefix: bin });
      assert.notEqual(result.status, 0);
      assert.deepEqual(readdirSync(keyDir), ['KEY']);
      assert.equal(readFileSync(dest, 'utf8'), OTHER_KEY);
    });
  });

  it('refuses a key passed as an argument and any wrong argument count', () => {
    withDir((dir) => {
      const dest = path.join(dir, 'KEY');
      for (const args of [[], [dest, KEY], [dest, dest]]) {
        const result = run({ args, input: KEY, dir });
        assert.notEqual(result.status, 0);
      }
      assert.deepEqual(readdirSync(dir), []);
    });
  });

  it('fails without creating a missing destination directory', () => {
    withDir((dir) => {
      const dest = path.join(dir, 'missing', 'KEY');
      const result = run({ args: [dest], input: KEY, dir });
      assert.notEqual(result.status, 0);
      assert.equal(existsSync(path.dirname(dest)), false);
    });
  });

  it('never prints the key and creates the file under umask 077', () => {
    withDir((dir) => {
      const dest = path.join(dir, 'KEY');
      const good = run({ args: [dest], input: KEY, dir, umaskValue: '000' });
      assert.equal(good.status, 0);
      assert.ok(!good.stdout.includes(KEY) && !good.stderr.includes(KEY));
      assert.equal(statSync(dest).mode & 0o777, 0o600);
      const bad = run({ args: [dest], input: `${KEY}x`, dir });
      assert.notEqual(bad.status, 0);
      assert.ok(!bad.stdout.includes(KEY) && !bad.stderr.includes(KEY));
    });
  });
});

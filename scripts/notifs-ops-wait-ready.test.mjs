// Behaviour test for ops/notifs-ops/wait-ready.sh with a stub curl that serves scripted answers
// and records its argv and the -K config it was given.
import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPT = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../ops/notifs-ops/wait-ready.sh',
);
const URL_READYZ = 'https://notifs-delivery.example.test/readyz';
const ACCESS_ID = 'test-access-id-0001';
const ACCESS_SECRET = 'test-access-secret-0002';

const STUB_CURL = `#!/bin/bash
dir="$(dirname "$0")"
echo "$*" >> "$dir/argv.log"
out=""
config=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -K) config="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$config" ] && cp "$config" "$dir/config.log"
n=0
[ -f "$dir/count" ] && n="$(cat "$dir/count")"
n=$((n + 1))
echo "$n" > "$dir/count"
total="$(wc -l < "$dir/answers")"
line="$n"
[ "$line" -gt "$total" ] && line="$total"
answer="$(sed -n "\${line}p" "$dir/answers")"
if [ "$answer" = "FAIL" ]; then
  exit 7
fi
code="\${answer%%|*}"
body="\${answer#*|}"
printf '%s' "$body" > "$out"
printf '%s' "$code"
`;

function harness(answers, { args, env } = {}) {
  const dir = mkdtempSync(path.join(tmpdir(), 'wr-'));
  try {
    const curl = path.join(dir, 'curl');
    writeFileSync(curl, STUB_CURL);
    chmodSync(curl, 0o755);
    writeFileSync(path.join(dir, 'answers'), `${answers.join('\n')}\n`);
    const result = spawnSync('bash', [SCRIPT, ...(args ?? [URL_READYZ, '2', '0.2'])], {
      env: {
        ...process.env,
        PATH: `${dir}:${process.env.PATH}`,
        NOTIFS_OPS_ACCESS_CLIENT_ID: ACCESS_ID,
        NOTIFS_OPS_ACCESS_CLIENT_SECRET: ACCESS_SECRET,
        ...env,
      },
      encoding: 'utf8',
    });
    const read = (name) =>
      existsSync(path.join(dir, name)) ? readFileSync(path.join(dir, name), 'utf8') : '';
    return {
      status: result.status,
      calls: Number(read('count') || 0),
      argv: read('argv.log'),
      config: read('config.log'),
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const OK = '200|{"status":"ok"}';
const NOT_OK = '200|{"status":"starting"}';

describe('wait-ready.sh', () => {
  it('exits 0 on the first try for 200 with status ok', () => {
    const result = harness([OK]);
    assert.equal(result.status, 0);
    assert.equal(result.calls, 1);
  });

  it('retries 200 with a non-ok status until a later answer is ok', () => {
    const result = harness([NOT_OK, NOT_OK, OK]);
    assert.equal(result.status, 0);
    assert.equal(result.calls, 3);
  });

  it('exits 1 after retrying when never ready', () => {
    const result = harness([NOT_OK]);
    assert.equal(result.status, 1);
    assert.ok(result.calls > 1);
  });

  it('exits 2 at once on a 302 without retrying', () => {
    const result = harness(['302|']);
    assert.equal(result.status, 2);
    assert.equal(result.calls, 1);
  });

  it('exits 2 at once on a 403', () => {
    const result = harness(['403|']);
    assert.equal(result.status, 2);
    assert.equal(result.calls, 1);
  });

  it('retries after a curl failure and after a 502', () => {
    const result = harness(['FAIL', '502|', OK]);
    assert.equal(result.status, 0);
    assert.equal(result.calls, 3);
  });

  it('never follows redirects or puts the Access secrets on argv, but sends them via -K', () => {
    const result = harness([OK]);
    assert.equal(result.status, 0);
    assert.ok(result.argv.length > 0, 'the stub recorded the call');
    assert.ok(!/(^|\s)(-L|--location)(\s|$)/.test(result.argv));
    assert.ok(!result.argv.includes(ACCESS_ID));
    assert.ok(!result.argv.includes(ACCESS_SECRET));
    assert.ok(result.config.includes(`CF-Access-Client-Id: ${ACCESS_ID}`));
    assert.ok(result.config.includes(`CF-Access-Client-Secret: ${ACCESS_SECRET}`));
  });

  it('exits 2 with no curl call on a wrong argument count', () => {
    for (const args of [[], [URL_READYZ], [URL_READYZ, '2', '1', 'extra']]) {
      const result = harness([OK], { args });
      assert.equal(result.status, 2);
      assert.equal(result.calls, 0);
    }
  });

  it('exits 2 with no curl call when a header variable is missing', () => {
    for (const name of ['NOTIFS_OPS_ACCESS_CLIENT_ID', 'NOTIFS_OPS_ACCESS_CLIENT_SECRET']) {
      const result = harness([OK], { env: { [name]: '' } });
      assert.equal(result.status, 2);
      assert.equal(result.calls, 0);
    }
  });
});

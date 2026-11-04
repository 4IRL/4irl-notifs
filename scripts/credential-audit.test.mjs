import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

import { auditLog, HIT_MESSAGES, NO_LINES_MESSAGE } from './credential-audit.mjs';

const SCRIPT = fileURLToPath(new URL('./credential-audit.mjs', import.meta.url));
const MIDDLEWARE_SOURCE = fileURLToPath(
  new URL('../provisioning-api/internal/httpapi/middleware.go', import.meta.url),
);

const wouldReject = JSON.stringify({
  level: 'WARN',
  msg: 'internal credential would reject',
  reason: 'missing',
  method: 'POST',
  path: '/v1/provision',
  secret: 'SHOULD-NOT-PRINT',
});
const rejected = JSON.stringify({
  level: 'WARN',
  msg: 'internal credential rejected',
  reason: 'mismatch',
  method: 'DELETE',
  path: '/v1/users/abc',
  secret: 'SHOULD-NOT-PRINT',
});
const otherWarn = JSON.stringify({
  level: 'WARN',
  msg: 'something else',
  method: 'GET',
  path: '/x',
});
const info = JSON.stringify({ level: 'INFO', msg: 'request' });

test('would-reject line is a hit', () => {
  const result = auditLog(wouldReject + '\n');
  assert.equal(result.code, 1);
  assert.deepEqual(result.lines, [
    'POST /v1/provision reason=missing',
    'credential audit: 1 hit(s)',
  ]);
});

test('rejected line is a hit', () => {
  const result = auditLog(rejected + '\n');
  assert.equal(result.code, 1);
  assert.deepEqual(result.lines, [
    'DELETE /v1/users/abc reason=mismatch',
    'credential audit: 1 hit(s)',
  ]);
});

test('compose-prefixed line is parsed and counted', () => {
  const result = auditLog(`provisioning-api-1  | ${wouldReject}\n`);
  assert.equal(result.code, 1);
  assert.equal(result.lines[0], 'POST /v1/provision reason=missing');
});

test('non-JSON lines and other msgs are skipped; JSON without hits is clean', () => {
  const result = auditLog(`starting up\n${otherWarn}\nnot {json}\n${info}\n`);
  assert.equal(result.code, 0);
  assert.deepEqual(result.lines, ['credential audit: clean']);
});

test('multiple hits are all counted', () => {
  const result = auditLog([wouldReject, rejected, info].join('\n'));
  assert.equal(result.code, 1);
  assert.equal(result.lines.length, 3);
  assert.equal(result.lines[2], 'credential audit: 2 hit(s)');
});

test('output never contains fields beyond method, path and reason', () => {
  const result = auditLog([wouldReject, rejected].join('\n'));
  assert.ok(!result.lines.join('\n').includes('SHOULD-NOT-PRINT'));
});

test('a hit missing method, path and reason still counts with stable output', () => {
  const bare = JSON.stringify({ level: 'WARN', msg: 'internal credential would reject' });
  const result = auditLog(bare + '\n');
  assert.equal(result.code, 1);
  assert.deepEqual(result.lines, [
    'undefined undefined reason=undefined',
    'credential audit: 1 hit(s)',
  ]);
});

test('empty input fails with the distinct no-lines message', () => {
  const result = auditLog('');
  assert.equal(result.code, 1);
  assert.deepEqual(result.lines, [NO_LINES_MESSAGE]);
});

test('input with no parseable JSON line fails with the no-lines message', () => {
  const result = auditLog('plain text\nprovisioning-api-1  | still plain\n');
  assert.equal(result.code, 1);
  assert.deepEqual(result.lines, [NO_LINES_MESSAGE]);
});

test('CLI exit codes follow stdin', () => {
  const clean = spawnSync('node', [SCRIPT], { input: info + '\n', encoding: 'utf8' });
  assert.equal(clean.status, 0);
  assert.match(clean.stdout, /credential audit: clean/);
  const hit = spawnSync('node', [SCRIPT], { input: wouldReject + '\n', encoding: 'utf8' });
  assert.equal(hit.status, 1);
  assert.match(hit.stderr, /POST \/v1\/provision reason=missing/);
  const empty = spawnSync('node', [SCRIPT], { input: '', encoding: 'utf8' });
  assert.equal(empty.status, 1);
  assert.match(empty.stderr, /no JSON log lines parsed/);
});

test('every HIT_MESSAGES entry is still logged by the provisioning-api middleware', () => {
  const source = fs.readFileSync(MIDDLEWARE_SOURCE, 'utf8');
  assert.ok(HIT_MESSAGES.size > 0);
  for (const message of HIT_MESSAGES) {
    assert.ok(
      source.includes(`"${message}"`),
      `middleware.go no longer logs "${message}"; update HIT_MESSAGES in credential-audit.mjs`,
    );
  }
});

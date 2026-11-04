import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, before, describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';

const SCRIPT = fileURLToPath(new URL('./internal-credential.sh', import.meta.url));

// A stub `docker` on PATH: `compose ... ps -q` prints STUB_CID; `inspect` prints STUB_ENV.
const STUB = `#!/bin/sh
case "$1" in
  compose) printf '%s' "$STUB_CID" ;;
  inspect) printf '%s' "$STUB_ENV" ;;
esac
`;

describe('internal-credential.sh', () => {
  let binDir;

  before(() => {
    binDir = fs.mkdtempSync(path.join(os.tmpdir(), 'notifs-cred-stub-'));
    fs.writeFileSync(path.join(binDir, 'docker'), STUB, { mode: 0o755 });
  });

  after(() => {
    fs.rmSync(binDir, { recursive: true, force: true });
  });

  function run(extraEnv) {
    return spawnSync('sh', [SCRIPT], {
      encoding: 'utf8',
      env: {
        PATH: `${binDir}:${process.env.PATH}`,
        STUB_CID: '',
        STUB_ENV: '',
        ...extraEnv,
      },
    });
  }

  it('prefers the exported credential over the container', () => {
    const result = run({
      NOTIFS_INTERNAL_CREDENTIAL: 'from-env',
      STUB_CID: 'abc123',
      STUB_ENV: 'PROVISIONING_INTERNAL_CREDENTIAL=from-container\n',
    });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, 'from-env\n');
  });

  it('falls back to the provisioning-api container environment', () => {
    const result = run({
      STUB_CID: 'abc123',
      STUB_ENV: 'PATH=/bin\nPROVISIONING_INTERNAL_CREDENTIAL=from-container\nOTHER=x\n',
    });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, 'from-container\n');
  });

  it('fails closed when the container is missing', () => {
    const result = run({});
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /no internal credential/);
  });

  it('fails closed when the container credential is empty', () => {
    const result = run({
      STUB_CID: 'abc123',
      STUB_ENV: 'PROVISIONING_INTERNAL_CREDENTIAL=\n',
    });
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /no internal credential/);
  });
});

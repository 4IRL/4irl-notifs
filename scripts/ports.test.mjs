import assert from 'node:assert/strict';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { describe, it } from 'node:test';

import { DEFAULT_PORTS, crc32, loadPorts, probePort, resolvePorts, slotPorts } from './ports.mjs';

const allFree = async () => true;

function firstCandidate(slug) {
  return (crc32(`4irl-notifs:${slug}`) % 99) + 1;
}

describe('DEFAULT_PORTS', () => {
  it('holds the primary clone ports', () => {
    assert.deepEqual(DEFAULT_PORTS, {
      NTFY_PORT: 8090,
      API_PORT: 8091,
      WEB_PORT: 5173,
      E2E_PORT: 4173,
    });
  });
});

describe('crc32', () => {
  it('matches the standard CRC-32 check value', () => {
    assert.equal(crc32('123456789'), 0xcbf43926);
  });
});

describe('slotPorts', () => {
  it('slot 0 is the primary defaults', () => {
    assert.deepEqual(slotPorts(0), DEFAULT_PORTS);
  });

  it('slot 1 and slot 99 have the documented values', () => {
    assert.deepEqual(slotPorts(1), {
      NTFY_PORT: 8092,
      API_PORT: 8093,
      WEB_PORT: 5274,
      E2E_PORT: 4274,
    });
    assert.deepEqual(slotPorts(99), {
      NTFY_PORT: 8288,
      API_PORT: 8289,
      WEB_PORT: 5372,
      E2E_PORT: 4372,
    });
  });

  it('never repeats a port across slots 1..99 or keys, and never equals a default', () => {
    const seen = new Set(Object.values(DEFAULT_PORTS));
    for (let slot = 1; slot <= 99; slot += 1) {
      for (const port of Object.values(slotPorts(slot))) {
        assert.ok(!seen.has(port), `slot ${slot} port ${port} collides`);
        seen.add(port);
      }
    }
    assert.equal(seen.size, 4 + 99 * 4);
  });
});

describe('resolvePorts', () => {
  it('returns the defaults at slot 0 for a missing slug', async () => {
    const result = await resolvePorts({ env: {}, probe: allFree });
    assert.deepEqual(result, { ...DEFAULT_PORTS, NOTIFS_SLOT: 0 });
  });

  it('starts the walk at the repo-salted crc32 slot', async () => {
    const slug = 'proof-a';
    const result = await resolvePorts({ slug, env: {}, probe: allFree });
    assert.equal(result.NOTIFS_SLOT, firstCandidate(slug));
    assert.deepEqual(slotPorts(result.NOTIFS_SLOT), {
      NTFY_PORT: result.NTFY_PORT,
      API_PORT: result.API_PORT,
      WEB_PORT: result.WEB_PORT,
      E2E_PORT: result.E2E_PORT,
    });
  });

  it('salts with the repo name (differs from the unsalted slot for a fixed slug)', () => {
    const slug = 'proof-a';
    assert.notEqual(firstCandidate(slug), (crc32(slug) % 99) + 1);
  });

  it('skips a slot claimed by a sibling worktree', async () => {
    const slug = 'proof-a';
    const first = firstCandidate(slug);
    const result = await resolvePorts({ slug, env: {}, claimed: new Set([first]), probe: allFree });
    assert.equal(result.NOTIFS_SLOT, (first % 99) + 1);
  });

  it('skips a slot when the probe reports a port busy', async () => {
    const slug = 'proof-a';
    const first = firstCandidate(slug);
    const busy = new Set([slotPorts(first).WEB_PORT]);
    const result = await resolvePorts({ slug, env: {}, probe: async (port) => !busy.has(port) });
    assert.equal(result.NOTIFS_SLOT, (first % 99) + 1);
  });

  it('wraps around past slot 99', async () => {
    const slug = 'proof-a';
    const first = firstCandidate(slug);
    const claimed = new Set();
    for (let i = 0; i < 99; i += 1) {
      const slot = ((first - 1 + i) % 99) + 1;
      if (slot !== 1) claimed.add(slot);
    }
    const result = await resolvePorts({ slug, env: {}, claimed, probe: allFree });
    assert.equal(result.NOTIFS_SLOT, 1);
  });

  it('throws when every slot is unavailable', async () => {
    await assert.rejects(
      resolvePorts({ slug: 'x', env: {}, probe: async () => false }),
      /no free port slot/,
    );
  });

  it('lets explicit env keys override per key', async () => {
    const result = await resolvePorts({
      slug: 'proof-a',
      env: { API_PORT: '19000' },
      probe: allFree,
    });
    assert.equal(result.API_PORT, 19000);
    assert.equal(result.NTFY_PORT, slotPorts(result.NOTIFS_SLOT).NTFY_PORT);
  });

  it('rejects an invalid env port', async () => {
    await assert.rejects(
      resolvePorts({ slug: 'a', env: { WEB_PORT: 'abc' }, probe: allFree }),
      /WEB_PORT/,
    );
    await assert.rejects(
      resolvePorts({ slug: 'a', env: { WEB_PORT: '70000' }, probe: allFree }),
      /WEB_PORT/,
    );
  });

  it('skips the walk when all four keys are explicit', async () => {
    const env = { NTFY_PORT: '1001', API_PORT: '1002', WEB_PORT: '1003', E2E_PORT: '1004' };
    const result = await resolvePorts({
      slug: 'a',
      env,
      probe: async () => {
        throw new Error('probe must not run');
      },
    });
    assert.deepEqual(result, {
      NTFY_PORT: 1001,
      API_PORT: 1002,
      WEB_PORT: 1003,
      E2E_PORT: 1004,
      NOTIFS_SLOT: 0,
    });
  });
});

describe('probePort', () => {
  it('reports a bound port as busy and a released one as free', async () => {
    const server = net.createServer();
    await new Promise((resolve) => server.listen(0, '0.0.0.0', resolve));
    const { port } = server.address();
    assert.equal(await probePort(port), false);
    await new Promise((resolve) => server.close(resolve));
    assert.equal(await probePort(port), true);
  });

  it('rethrows non-EADDRINUSE errors', async () => {
    await assert.rejects(probePort(-1));
  });
});

describe('loadPorts', () => {
  function tmpDir() {
    return fs.mkdtempSync(path.join(os.tmpdir(), 'ports-test-'));
  }

  it('returns defaults when there is no file or env', () => {
    const cwd = tmpDir();
    assert.deepEqual(loadPorts({ cwd, env: {} }), { ports: { ...DEFAULT_PORTS } });
  });

  it('reads the file', () => {
    const cwd = tmpDir();
    fs.writeFileSync(path.join(cwd, '.worktree.env'), '# c\n\nWEB_PORT=5300\nSLUG=x\n');
    const { ports } = loadPorts({ cwd, env: {} });
    assert.equal(ports.WEB_PORT, 5300);
    assert.equal(ports.API_PORT, 8091);
  });

  it('env beats file', () => {
    const cwd = tmpDir();
    fs.writeFileSync(path.join(cwd, '.worktree.env'), 'WEB_PORT=5300\nAPI_PORT=8100\n');
    const { ports } = loadPorts({ cwd, env: { WEB_PORT: '5400', NTFY_PORT: '' } });
    assert.equal(ports.WEB_PORT, 5400);
    assert.equal(ports.API_PORT, 8100);
    assert.equal(ports.NTFY_PORT, 8090);
  });

  it('rejects an invalid file value', () => {
    const cwd = tmpDir();
    fs.writeFileSync(path.join(cwd, '.worktree.env'), 'WEB_PORT=0\n');
    assert.throws(() => loadPorts({ cwd, env: {} }), /WEB_PORT/);
  });
});

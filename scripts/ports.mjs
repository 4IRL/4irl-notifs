// Port resolver shared by the worktree script and the CLI (`make worktree-ports`).
//
// Each worktree gets one slot 1..99 derived from its slug; the primary clone is slot 0
// (the defaults). ntfy/API are adjacent (8090/8091), so that pair steps by 2 per slot; the
// web pair is offset by 100 from the defaults so worktree ports stay out of the 5174+/4174+
// range that other Vite apps auto-increment into.

import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const DEFAULT_PORTS = Object.freeze({
  NTFY_PORT: 8090,
  API_PORT: 8091,
  WEB_PORT: 5173,
  E2E_PORT: 4173,
});

export const REPO_ROOT = path.resolve(fileURLToPath(new URL('..', import.meta.url)));

const PORT_KEYS = Object.keys(DEFAULT_PORTS);
const SLOT_COUNT = 99;
const SLUG_SALT = '4irl-notifs:';

const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n += 1) {
    let c = n;
    for (let k = 0; k < 8; k += 1) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
})();

/** Standard CRC-32 (IEEE) of a string's UTF-8 bytes, as an unsigned integer. */
export function crc32(str) {
  let crc = 0xffffffff;
  for (const byte of Buffer.from(str, 'utf8')) {
    crc = CRC_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

/** Parse KEY=VALUE lines; comments (#) and blank lines are ignored. */
export function parseEnvFile(text) {
  const out = {};
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    out[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  }
  return out;
}

/** Serialize an object as KEY=VALUE lines (trailing newline). */
export function formatEnvFile(obj) {
  return Object.entries(obj)
    .map(([key, value]) => `${key}=${value}`)
    .join('\n')
    .concat('\n');
}

function parsePort(key, value) {
  const text = String(value).trim();
  const num = Number(text);
  if (!/^\d+$/.test(text) || num < 1 || num > 65535) {
    throw new Error(`${key} must be an integer port between 1 and 65535 (got ${JSON.stringify(value)})`);
  }
  return num;
}

/** The four ports for a slot (0 = primary defaults). */
export function slotPorts(slot) {
  return {
    NTFY_PORT: DEFAULT_PORTS.NTFY_PORT + 2 * slot,
    API_PORT: DEFAULT_PORTS.API_PORT + 2 * slot,
    WEB_PORT: slot === 0 ? DEFAULT_PORTS.WEB_PORT : 5273 + slot,
    E2E_PORT: slot === 0 ? DEFAULT_PORTS.E2E_PORT : 4273 + slot,
  };
}

/** Resolves true when the port can be bound on 0.0.0.0 (IPv4 only); only EADDRINUSE means held. */
export function probePort(port) {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', (err) => {
      if (err.code === 'EADDRINUSE') resolve(false);
      else reject(err);
    });
    server.listen(port, '0.0.0.0', () => {
      server.close(() => resolve(true));
    });
  });
}

function envOverrides(env) {
  const out = {};
  for (const key of PORT_KEYS) {
    if (env[key] !== undefined && env[key] !== '') out[key] = parsePort(key, env[key]);
  }
  return out;
}

/**
 * Pick ports for a worktree. No slug: the defaults (slot 0). Explicit *_PORT env values win
 * per key. Otherwise walk slots from the salted slug hash until a candidate is neither a
 * claimed slot (slot 0 is always claimed), overlapping a claimed slot's ports, nor has a
 * held port. `claimed` is an iterable of slot numbers read from sibling `.worktree.env`s.
 */
export async function resolvePorts({
  slug,
  claimed = new Set(),
  probe = probePort,
  env = process.env,
} = {}) {
  const overrides = envOverrides(env);
  let base;
  let slot = 0;

  if (!slug) {
    base = { ...DEFAULT_PORTS };
  } else if (PORT_KEYS.every((key) => key in overrides)) {
    base = {};
  } else {
    const claimedSlots = new Set([0, ...claimed]);
    const claimedPorts = new Set();
    for (const claimedSlot of claimedSlots) {
      for (const port of Object.values(slotPorts(claimedSlot))) claimedPorts.add(port);
    }
    const start = crc32(`${SLUG_SALT}${slug}`) % SLOT_COUNT;
    let found = null;
    for (let i = 0; i < SLOT_COUNT && found === null; i += 1) {
      const candidate = ((start + i) % SLOT_COUNT) + 1;
      if (claimedSlots.has(candidate)) continue;
      const candidatePorts = slotPorts(candidate);
      const values = Object.values(candidatePorts);
      if (values.some((port) => claimedPorts.has(port))) continue;
      let free = true;
      for (const port of values) {
        if (!(await probe(port))) {
          free = false;
          break;
        }
      }
      if (free) found = { candidate, candidatePorts };
    }
    if (found === null) {
      throw new Error(
        `no free port slot (1-${SLOT_COUNT}) for "${slug}": set NTFY_PORT, API_PORT, WEB_PORT and E2E_PORT explicitly`,
      );
    }
    base = found.candidatePorts;
    slot = found.candidate;
  }

  return { ...base, ...overrides, NOTIFS_SLOT: slot };
}

/**
 * Resolve ports for a running process: defaults < <cwd>/.worktree.env < env.
 * `cwd` defaults to the repo root.
 */
export function loadPorts({ cwd = REPO_ROOT, env = process.env } = {}) {
  const ports = { ...DEFAULT_PORTS };

  const file = path.join(cwd, '.worktree.env');
  if (fs.existsSync(file)) {
    const parsed = parseEnvFile(fs.readFileSync(file, 'utf8'));
    for (const key of PORT_KEYS) {
      if (parsed[key] !== undefined) {
        ports[key] = parsePort(key, parsed[key]);
      }
    }
  }

  const overrides = envOverrides(env);
  Object.assign(ports, overrides);

  return { ports };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv[2] === 'print') {
    process.stdout.write(formatEnvFile(loadPorts().ports));
  } else {
    process.stderr.write('usage: node scripts/ports.mjs print\n');
    process.exitCode = 1;
  }
}

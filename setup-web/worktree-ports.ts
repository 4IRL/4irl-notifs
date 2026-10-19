import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// Shared by vite.config.ts and playwright.config.ts so a bare `npm run dev` /
// `npx playwright test` in a worktree gets that worktree's ports without make.
// Precedence per key: defaults < ../.worktree.env < process.env (non-empty).
// Mirrors scripts/ports.mjs loadPorts (and web/worktree-ports.ts); deliberately not
// imported from either so setup-web stays self-contained.

const DEFAULTS = { SETUP_WEB_PORT: 5473, SETUP_E2E_PORT: 4473 } as const;

type PortKey = keyof typeof DEFAULTS;

interface SetupWebPortsInput {
  fileText?: string | null;
  env?: Record<string, string | undefined>;
}

interface SetupWebPorts {
  webPort: number;
  e2ePort: number;
}

function parsePort({ key, value }: { key: string; value: string }): number {
  const port = Number(value);
  if (!/^\d+$/.test(value.trim()) || !Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`Invalid port for ${key}: ${JSON.stringify(value)} (expected integer 1-65535)`);
  }
  return port;
}

function readWorktreeEnv(): string | null {
  try {
    return readFileSync(fileURLToPath(new URL('../.worktree.env', import.meta.url)), 'utf8');
  } catch {
    return null;
  }
}

function parseEnvText(text: string): Record<string, string> {
  const values: Record<string, string> = {};
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line === '' || line.startsWith('#')) continue;
    const separator = line.indexOf('=');
    if (separator < 1) continue;
    values[line.slice(0, separator).trim()] = line.slice(separator + 1).trim();
  }
  return values;
}

export function setupWebPorts({
  fileText,
  env = process.env,
}: SetupWebPortsInput = {}): SetupWebPorts {
  const text = fileText === undefined ? readWorktreeEnv() : fileText;
  const fileValues = text === null ? {} : parseEnvText(text);

  const resolve = (key: PortKey): number => {
    const envValue = env[key];
    if (envValue !== undefined && envValue !== '') return parsePort({ key, value: envValue });
    const fileValue = fileValues[key];
    if (fileValue !== undefined && fileValue !== '') return parsePort({ key, value: fileValue });
    return DEFAULTS[key];
  };

  return {
    webPort: resolve('SETUP_WEB_PORT'),
    e2ePort: resolve('SETUP_E2E_PORT'),
  };
}

// Credential audit: reads provisioning-api log lines and fails when any request was logged that the
// internal-credential check would reject (or did reject). Used by `make credential-audit` and CI.
import { fileURLToPath } from 'node:url';

export const HIT_MESSAGES = new Set([
  'internal credential would reject',
  'internal credential rejected',
]);

export const NO_LINES_MESSAGE = 'credential audit: no JSON log lines parsed';

// Matches a docker compose log prefix such as `provisioning-api-1  | `.
const COMPOSE_PREFIX = /^\S+-\d+\s+\|\s?/;

function parseJsonLine(rawLine) {
  const line = rawLine.replace(COMPOSE_PREFIX, '').trim();
  if (!line.startsWith('{')) return null;
  try {
    const parsed = JSON.parse(line);
    return parsed !== null && typeof parsed === 'object' ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * Audits log text. Only method, path and reason of a hit are ever printed.
 * @returns {{ code: number, lines: string[] }}
 */
export function auditLog(text) {
  const hits = [];
  let parsedCount = 0;
  for (const rawLine of text.split('\n')) {
    const entry = parseJsonLine(rawLine);
    if (entry === null) continue;
    parsedCount += 1;
    if (HIT_MESSAGES.has(entry.msg)) {
      hits.push(`${String(entry.method)} ${String(entry.path)} reason=${String(entry.reason)}`);
    }
  }
  if (parsedCount === 0) return { code: 1, lines: [NO_LINES_MESSAGE] };
  if (hits.length > 0) {
    return {
      code: 1,
      lines: [...hits, `credential audit: ${hits.length} hit(s)`],
    };
  }
  return { code: 0, lines: ['credential audit: clean'] };
}

async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks).toString('utf8');
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const { code, lines } = auditLog(await readStdin());
  const output = lines.join('\n') + '\n';
  (code === 0 ? process.stdout : process.stderr).write(output);
  process.exit(code);
}

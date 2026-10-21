// @vitest-environment node
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import { parseHeadersFile } from '../pages-headers.ts';

const HEADERS_FILE = fileURLToPath(new URL('../public/_headers', import.meta.url));

const CONTENT_SECURITY_POLICY = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self'",
  "connect-src 'none'",
  "font-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-ancestors 'none'",
].join('; ');

describe('public/_headers', () => {
  const rules = parseHeadersFile(readFileSync(HEADERS_FILE, 'utf8'));

  it('applies a single rule to every path', () => {
    expect(Object.keys(rules)).toEqual(['/*']);
  });

  it('sets exactly the expected security headers with exact values', () => {
    expect(rules['/*']).toEqual({
      'Referrer-Policy': 'no-referrer',
      'Content-Security-Policy': CONTENT_SECURITY_POLICY,
      'X-Content-Type-Options': 'nosniff',
      'X-Robots-Tag': 'noindex',
      'Permissions-Policy': 'camera=(), microphone=(), geolocation=()',
    });
  });

  it('never loosens the CSP with unsafe-inline, unsafe-eval or a wildcard source', () => {
    const csp = rules['/*']['Content-Security-Policy'];
    expect(csp).not.toMatch(/unsafe-inline|unsafe-eval|\*|https?:/);
  });
});

import { describe, expect, it } from 'vitest';
import { parseHeadersFile } from './pages-headers.ts';

describe('parseHeadersFile', () => {
  it('parses a pattern with its headers', () => {
    const text = '/*\n  X-Frame-Options: DENY\n  Cache-Control: no-store\n';
    expect(parseHeadersFile(text)).toEqual({
      '/*': { 'X-Frame-Options': 'DENY', 'Cache-Control': 'no-store' },
    });
  });

  it('parses multiple patterns', () => {
    const text = '/*\n  X-Frame-Options: DENY\n/assets/*\n  Cache-Control: immutable\n';
    expect(parseHeadersFile(text)).toEqual({
      '/*': { 'X-Frame-Options': 'DENY' },
      '/assets/*': { 'Cache-Control': 'immutable' },
    });
  });

  it('keeps colons inside a header value', () => {
    const text = "/*\n  Content-Security-Policy: default-src 'none'; img-src https://x.test:443\n";
    expect(parseHeadersFile(text)['/*']['Content-Security-Policy']).toBe(
      "default-src 'none'; img-src https://x.test:443",
    );
  });

  it('skips comments and blank lines', () => {
    const text = '# top comment\n\n/*\n  # indented comment\n\n  X-Frame-Options: DENY\n';
    expect(parseHeadersFile(text)).toEqual({ '/*': { 'X-Frame-Options': 'DENY' } });
  });

  it('keeps a pattern that has no headers', () => {
    expect(parseHeadersFile('/*\n')).toEqual({ '/*': {} });
  });

  it('throws on a duplicate URL pattern', () => {
    const text = '/*\n  X-Frame-Options: DENY\n/*\n  X-Other: 1\n';
    expect(() => parseHeadersFile(text)).toThrow(/duplicate URL pattern: \/\*/);
  });

  it('throws on a header line before any URL pattern', () => {
    expect(() => parseHeadersFile('  X-Frame-Options: DENY\n')).toThrow(
      /header line before any URL pattern/,
    );
  });

  it('throws on a header line with no colon', () => {
    expect(() => parseHeadersFile('/*\n  NotAHeader\n')).toThrow(/without a "Name: value" form/);
  });

  it('throws on a duplicate header under one pattern', () => {
    const text = '/*\n  X-Frame-Options: DENY\n  X-Frame-Options: SAMEORIGIN\n';
    expect(() => parseHeadersFile(text)).toThrow(/duplicate header X-Frame-Options under \/\*/);
  });
});

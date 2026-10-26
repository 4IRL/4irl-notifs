import { describe, expect, it } from 'vitest';

import {
  accessCredentialProblem,
  bodyMatchProblem,
  parseExpectedStatuses,
  probeIntervalProblem,
  probeTimeoutProblem,
  probeUrlProblem,
} from './validation';

describe('probeUrlProblem', () => {
  it.each([
    ['', 'empty'],
    ['   ', 'empty'],
    ['http://x', 'scheme'],
    ['HTTPS://x', 'scheme'],
    ['https://a#b', 'malformed'],
    ['https://u:p@h/', 'malformed'],
    ['https://localhost/', 'malformed'],
    ['https://LOCALHOST:8080/', 'malformed'],
    ['https://foo.localhost/', 'malformed'],
    ['https://127.0.0.1/', 'malformed'],
    ['https://[::1]/', 'malformed'],
    ['https:///path', 'malformed'],
    ['https://', 'malformed'],
    [`https://a.com/${'x'.repeat(2048)}`, 'tooLong'],
    ['https://example.com/health?x=1', null],
    ['https://example.com:8443', null],
    ['https://exa mple.com/', 'malformed'],
    ['https://example.com/a\tb', 'malformed'],
    ['https://example.com/a b', null],
    ['https://[2001:db8::1]:8443/', 'malformed'],
    ['https://localhost./', 'malformed'],
    ['https://foo.localhost.:8080/', 'malformed'],
    ['https://127.0.0.1./', 'malformed'],
    ['https://example.com:abc/', 'malformed'],
    ['https://example.com./', null],
    ['https://example.com:/', null],
  ])('probeUrlProblem(%j) === %s', (value, expected) => {
    expect(probeUrlProblem(value)).toBe(expected);
  });

  it('accepts a URL of exactly 2048 bytes and rejects 2049', () => {
    const prefix = 'https://a.com/';
    expect(probeUrlProblem(prefix + 'x'.repeat(2048 - prefix.length))).toBeNull();
    expect(probeUrlProblem(prefix + 'x'.repeat(2049 - prefix.length))).toBe('tooLong');
  });
});

describe('parseExpectedStatuses', () => {
  it.each([
    ['200', [200]],
    ['200, 204', [200, 204]],
    ['100,599', [100, 599]],
  ])('accepts %j', (text, statuses) => {
    expect(parseExpectedStatuses(text)).toEqual({ ok: true, statuses });
  });

  it.each([
    [''],
    ['200,200'],
    ['99'],
    ['600'],
    ['20x'],
    ['2 00'],
    ['200,'],
    ['100,101,102,103,104,105,106,107,108,109,110'],
  ])('rejects %j', (text) => {
    expect(parseExpectedStatuses(text)).toEqual({ ok: false });
  });

  it('accepts exactly 10 codes', () => {
    const result = parseExpectedStatuses('200,201,202,203,204,205,206,207,208,226');
    expect(result.ok).toBe(true);
  });
});

describe('bodyMatchProblem', () => {
  it.each([
    ['ok', null],
    ['a'.repeat(256), null],
    ['a'.repeat(257), 'invalid'],
    ['é'.repeat(128), null],
    ['é'.repeat(129), 'invalid'],
    ['\u0000', 'invalid'],
    ['a\u0000b', 'invalid'],
    ['   ', 'invalid'],
  ])('bodyMatchProblem(%j) === %s', (value, expected) => {
    expect(bodyMatchProblem(value)).toBe(expected);
  });
});

describe('accessCredentialProblem', () => {
  it.each([
    ['abc.DEF-123', null],
    ['a'.repeat(256), null],
    ['a'.repeat(257), 'invalid'],
    ['', 'invalid'],
    ['a b', 'invalid'],
    ['a\rb', 'invalid'],
    ['a\nb', 'invalid'],
    ['a\tb', 'invalid'],
    ['café', 'invalid'],
    ['a\u007fb', 'invalid'],
  ])('accessCredentialProblem(%j) === %s', (value, expected) => {
    expect(accessCredentialProblem(value)).toBe(expected);
  });
});

describe('probeTimeoutProblem', () => {
  it.each([
    [0, 'range'],
    [1, null],
    [8, null],
    [9, 'range'],
    [2.5, 'range'],
    [Number.NaN, 'range'],
  ])('probeTimeoutProblem(%s) === %s', (value, expected) => {
    expect(probeTimeoutProblem(value)).toBe(expected);
  });
});

describe('probeIntervalProblem', () => {
  it.each([
    [59, 'range'],
    [60, null],
    [86400, null],
    [86401, 'range'],
    [60.5, 'range'],
    [Number.NaN, 'range'],
  ])('probeIntervalProblem(%s) === %s', (value, expected) => {
    expect(probeIntervalProblem(value)).toBe(expected);
  });
});

import { describe, expect, it } from 'vitest';

import { strings } from '../strings';
import { displayProbeUrl, withHttpsScheme } from './probeUrl';

describe('withHttpsScheme', () => {
  it.each([
    ['', ''],
    ['   ', ''],
    [' example.com/x ', 'https://example.com/x'],
    ['example.com', 'https://example.com'],
    ['localhost:8080', 'https://localhost:8080'],
    ['http://x', 'http://x'],
    ['https://x.com', 'https://x.com'],
    ['HTTPS://X.com', 'https://X.com'],
    ['HTTP://X.com', 'http://X.com'],
    ['ftp://x', 'ftp://x'],
    ['//host', '//host'],
  ])('maps %j to %j', (input, expected) => {
    expect(withHttpsScheme(input)).toBe(expected);
  });

  it('never double-prefixes', () => {
    const once = withHttpsScheme('example.com');
    expect(withHttpsScheme(once)).toBe(once);
  });
});

describe('displayProbeUrl', () => {
  it.each([
    ['https://urls4irl.app/health', 'urls4irl.app/health'],
    ['https://host.example', 'host.example/'],
    ['https://host.example:8443/a/b', 'host.example:8443/a/b'],
    ['https://zq-user:zq-pw@host.example/p?token=SECRET#frag', 'host.example/p'],
    ['https://host.example/p?x=1', 'host.example/p'],
  ])('shows host and path only for %s', (url, expected) => {
    expect(displayProbeUrl(url)).toBe(expected);
  });

  it('fails closed on an unparsable value', () => {
    expect(displayProbeUrl('not a url token=SECRET')).toBe(strings.noValue);
    expect(displayProbeUrl('')).toBe(strings.noValue);
  });
});

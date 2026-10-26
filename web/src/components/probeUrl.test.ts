import { describe, expect, it } from 'vitest';

import { strings } from '../strings';
import { displayProbeUrl } from './probeUrl';

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

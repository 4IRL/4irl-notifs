import { describe, expect, it } from 'vitest';

import { shortMessageId } from './shortMessageId';

describe('shortMessageId', () => {
  it('returns a short id unchanged', () => {
    expect(shortMessageId('abc123')).toBe('abc123');
  });

  it('returns a 10-char id whole', () => {
    expect(shortMessageId('0123456789')).toBe('0123456789');
  });

  it('shortens an id longer than 10 chars to first 6, ellipsis, last 2', () => {
    expect(shortMessageId('01JA9XABCDEFGH7Q')).toBe('01JA9X…7Q');
  });
});

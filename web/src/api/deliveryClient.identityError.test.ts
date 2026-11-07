import { describe, expect, it } from 'vitest';

import { ApiError } from './client';
import { ACKNOWLEDGE_ALL_LIMIT, identityErrorCode } from './deliveryClient';

describe('identityErrorCode', () => {
  it.each([
    [400, 'invalid prefix', 'invalid-prefix'],
    [409, 'prefix already used', 'prefix-taken'],
    [409, 'prefix overlaps an approved app id or registrar', 'prefix-overlap'],
    [409, 'request is not pending', 'request-not-pending'],
    [409, 'request is not a registrar request', 'request-not-registrar'],
    [409, 'workflow identity already bound', 'workflow-bound'],
    [404, 'binding does not exist', 'binding-gone'],
    [404, 'request does not exist', 'not-found'],
  ])('maps %i "%s" to %s', (status, message, code) => {
    expect(identityErrorCode(new ApiError({ status, message }))).toBe(code);
  });

  it('fails closed on an unrecognised text, a mismatched status or a non-ApiError', () => {
    expect(identityErrorCode(new ApiError({ status: 500, message: 'boom' }))).toBe('unknown');
    expect(identityErrorCode(new ApiError({ status: 400, message: 'prefix already used' }))).toBe(
      'unknown',
    );
    expect(identityErrorCode(new Error('invalid prefix'))).toBe('unknown');
    expect(identityErrorCode(null)).toBe('unknown');
  });

  it('exports the acknowledge-all limit', () => {
    expect(ACKNOWLEDGE_ALL_LIMIT).toBe(200);
  });
});

import assert from 'node:assert/strict';
import { describe, it } from 'node:test';

import {
  EMPTY_CREDENTIAL_MESSAGE,
  requireInternalCredential,
} from './internal-credential-guard.mjs';

describe('requireInternalCredential', () => {
  it('returns a set credential unchanged', () => {
    assert.equal(requireInternalCredential('cred-value'), 'cred-value');
  });

  it('throws the actionable message on an empty or missing credential', () => {
    for (const value of ['', undefined, null]) {
      assert.throws(() => requireInternalCredential(value), {
        message: EMPTY_CREDENTIAL_MESSAGE,
      });
    }
  });

  it('never echoes a credential value in the message', () => {
    assert.doesNotMatch(EMPTY_CREDENTIAL_MESSAGE, /cred-value/);
  });
});

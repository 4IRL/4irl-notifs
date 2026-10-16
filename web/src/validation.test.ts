import { describe, expect, it } from 'vitest';

import { isValidAppId, isValidChannel, isValidEmail, isValidTitle } from './validation';

describe('isValidAppId', () => {
  it.each([
    ['urls4irl', true],
    ['chores_4irl', true],
    ['a', true],
    ['urls4irl-alerts', false], // hyphens are not allowed in app_id
    ['UrlsIRL', false], // uppercase rejected
    ['', false],
    ['everyone', false], // reserved
    ['1'.repeat(64), false], // too long (>63)
  ])('isValidAppId(%j) === %s', (value, expected) => {
    expect(isValidAppId(value)).toBe(expected);
  });
});

describe('isValidEmail', () => {
  it.each([
    ['alice@example.com', true],
    ['  Alice@Example.COM  ', true], // uppercase/whitespace-padded valid
    ['aliceexample.com', false], // missing @
    ['alice@ex@ample.com', false], // two @s
    ['@example.com', false], // empty local
    ['alice@', false], // empty domain
    ['alice @example.com', false], // internal space
    ['', false], // empty string
    [`${'a'.repeat(250)}@example.com`, false], // >254 chars
  ])('isValidEmail(%j) === %s', (value, expected) => {
    expect(isValidEmail(value)).toBe(expected);
  });
});

describe('isValidTitle', () => {
  it.each([
    ['URLs 4 IRL test', true],
    ['Chore Reaper 🧹 test', true], // emoji
    ['a'.repeat(256), true], // exactly the byte limit
    ['é'.repeat(128), true], // 256 bytes, 128 runes
    ['a'.repeat(257), false],
    ['é'.repeat(129), false], // 258 bytes: the limit is bytes, not runes
    ['a\nb', false], // LF
    ['a\tb', false], // tab
    ['a\x00b', false], // NUL
    ['a\x7fb', false], // DEL
    ['a\u0085b', false], // C1 control (NEL)
  ])('isValidTitle(%j) === %s', (value, expected) => {
    expect(isValidTitle(value)).toBe(expected);
  });
});

describe('isValidChannel', () => {
  it.each([
    ['alerts', true],
    ['digest', true],
    ['all', true],
    ['a_b2', true],
    ['', false], // empty
    ['Alerts', false], // uppercase rejected
    ['has-hyphen', false], // hyphens not allowed
    ['a'.repeat(33), false], // too long (>32)
    ['_lead', false], // leading underscore not allowed
  ])('isValidChannel(%j) === %s', (value, expected) => {
    expect(isValidChannel(value)).toBe(expected);
  });
});

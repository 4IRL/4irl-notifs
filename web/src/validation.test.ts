import { describe, expect, it } from 'vitest';

import { isValidAppId, isValidEmail } from './validation';

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

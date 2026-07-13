import { describe, expect, it } from 'vitest';

import { isValidAppId, isValidUserId } from './validation';

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

describe('isValidUserId', () => {
  it.each([
    ['alice', true],
    ['alice-2', true], // hyphens allowed in user_id
    ['user_name', true],
    ['Alice', false], // uppercase rejected
    ['', false],
    ['everyone', false], // reserved
    ['*', false], // reserved
    ['a'.repeat(64), false], // too long
  ])('isValidUserId(%j) === %s', (value, expected) => {
    expect(isValidUserId(value)).toBe(expected);
  });
});

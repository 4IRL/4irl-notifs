import { describe, expect, it } from 'vitest';

import {
  hasSurroundingWhitespace,
  isHttpsUrl,
  isValidAppId,
  isValidChannel,
  isValidDestinationName,
  isValidEmail,
  isValidHeartbeatAppId,
  isValidHeartbeatName,
  isValidNtfyTopic,
  isValidRouteAppPattern,
  isValidRouteChannelPattern,
  isValidSigningSecret,
  isValidTitle,
} from './validation';

describe('route pattern validators', () => {
  it.each([
    ['*', true],
    ['chores4irl', true],
    ['everyone', false],
    ['Chores', false],
    ['_x', false],
    ['', false],
    ['a'.repeat(64), false],
  ])('isValidRouteAppPattern(%j) === %s', (value, expected) => {
    expect(isValidRouteAppPattern(value)).toBe(expected);
  });

  it.each([
    ['*', true],
    ['digest', true],
    ['alerts*', true],
    ['a'.repeat(32), true],
    ['a'.repeat(33), false],
    ['a'.repeat(31) + '*', true],
    ['a'.repeat(32) + '*', false],
    ['*alerts', false],
    ['a*b', false],
    ['Alerts', false],
    ['', false],
  ])('isValidRouteChannelPattern(%j) === %s', (value, expected) => {
    expect(isValidRouteChannelPattern(value)).toBe(expected);
  });
});

describe('heartbeat id validators', () => {
  it.each([
    ['minecrafting', true],
    ['daily_report', true],
    ['a', true],
    ['a'.repeat(63), true],
    ['a'.repeat(64), false],
    // Unlike the provisioning app id, the heartbeat rule reserves no names.
    ['everyone', true],
    ['Minecrafting', false],
    ['_x', false],
    ['has-dash', false],
    ['', false],
  ])('isValidHeartbeatAppId and isValidHeartbeatName agree on %j === %s', (value, expected) => {
    expect(isValidHeartbeatAppId(value)).toBe(expected);
    expect(isValidHeartbeatName(value)).toBe(expected);
  });
});

describe('destination validators', () => {
  it.each([
    ['ops_discord', true],
    ['Ops', false],
    ['_x', false],
    ['', false],
    ['a'.repeat(64), false],
  ])('isValidDestinationName(%j) === %s', (value, expected) => {
    expect(isValidDestinationName(value)).toBe(expected);
  });

  it.each([
    ['family_alerts', true],
    ['Mixed-Case_1', true],
    ['has space', false],
    ['', false],
    ['a'.repeat(65), false],
  ])('isValidNtfyTopic(%j) === %s', (value, expected) => {
    expect(isValidNtfyTopic(value)).toBe(expected);
  });

  it.each([
    ['a'.repeat(32), true],
    ['a'.repeat(31), false],
    ['a'.repeat(256), true],
    ['a'.repeat(257), false],
    [`${'a'.repeat(31)}é`, false],
  ])('isValidSigningSecret(%j) === %s', (value, expected) => {
    expect(isValidSigningSecret(value)).toBe(expected);
  });

  it('checks the https prefix and surrounding whitespace', () => {
    expect(isHttpsUrl('https://example.com/x')).toBe(true);
    expect(isHttpsUrl('http://example.com')).toBe(false);
    expect(isHttpsUrl('https://')).toBe(false);
    expect(hasSurroundingWhitespace(' x')).toBe(true);
    expect(hasSurroundingWhitespace('x ')).toBe(true);
    expect(hasSurroundingWhitespace('x y')).toBe(false);
  });
});

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

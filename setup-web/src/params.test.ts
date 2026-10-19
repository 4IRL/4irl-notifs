import { describe, expect, it } from 'vitest';

import { NAME_MAX_LENGTH, NTFY_SERVER, parseSetupParams } from './params';

const VALID_USER = 'u_v4sf4e5teivpe3zi';

describe('parseSetupParams', () => {
  it('derives topics and falls back to app for the display name', () => {
    expect(parseSetupParams(`?app=tasktracker&user=${VALID_USER}`)).toEqual({
      ok: true,
      app: 'tasktracker',
      user: VALID_USER,
      displayName: 'tasktracker',
      nameIsFallback: true,
      personalTopic: 'tasktracker-v4sf4e5teivpe3zi-all',
      broadcastTopic: 'tasktracker-broadcast',
    });
  });

  it('accepts a search string without the leading question mark', () => {
    const result = parseSetupParams(`app=tasktracker&user=${VALID_USER}`);
    expect(result.ok).toBe(true);
  });

  it('uses a trimmed name as the display name', () => {
    const result = parseSetupParams(
      `?app=tasktracker&user=${VALID_USER}&name=%20Task%20Tracker%20`,
    );
    expect(result).toMatchObject({
      ok: true,
      displayName: 'Task Tracker',
      nameIsFallback: false,
    });
  });

  it('accepts the example link from docs/app-integration-guide.md', () => {
    // Keep in sync with "Link your users to the setup page" in docs/app-integration-guide.md.
    const exampleLink =
      'https://notifs-setup.4irl.app/?app=tasktracker&user=u_v4sf4e5teivpe3zi&name=Task%20Tracker';
    expect(parseSetupParams(new URL(exampleLink).search)).toMatchObject({
      ok: true,
      app: 'tasktracker',
      user: 'u_v4sf4e5teivpe3zi',
      displayName: 'Task Tracker',
      nameIsFallback: false,
    });
  });

  it('exposes the ntfy server constant without a trailing slash', () => {
    expect(NTFY_SERVER).toBe('https://notifs.4irl.app');
  });

  describe('invalid app or user', () => {
    const cases: Array<[string, string]> = [
      ['missing app', `?user=${VALID_USER}`],
      ['empty app', `?app=&user=${VALID_USER}`],
      ['missing user', '?app=tasktracker'],
      ['empty user', '?app=tasktracker&user='],
      ['no params at all', ''],
      ['reserved app everyone', `?app=everyone&user=${VALID_USER}`],
      ['uppercase app', `?app=TaskTracker&user=${VALID_USER}`],
      ['hyphenated app', `?app=task-tracker&user=${VALID_USER}`],
      ['leading underscore app', `?app=_tasktracker&user=${VALID_USER}`],
      ['64-char app', `?app=${'a'.repeat(64)}&user=${VALID_USER}`],
      ['user without u_ prefix', '?app=tasktracker&user=v4sf4e5teivpe3zi'],
      ['user with 0', '?app=tasktracker&user=u_v4sf4e5teivpe3z0'],
      ['user with 1', '?app=tasktracker&user=u_v4sf4e5teivpe3z1'],
      ['user with 8', '?app=tasktracker&user=u_v4sf4e5teivpe3z8'],
      ['user with 9', '?app=tasktracker&user=u_v4sf4e5teivpe3z9'],
      ['user with uppercase', '?app=tasktracker&user=u_V4sf4e5teivpe3zi'],
      ['user with 15 hash chars', '?app=tasktracker&user=u_v4sf4e5teivpe3z'],
      ['user with 17 hash chars', '?app=tasktracker&user=u_v4sf4e5teivpe3zia'],
    ];

    it.each(cases)('returns ok:false for %s', (_label, search) => {
      expect(parseSetupParams(search)).toEqual({ ok: false });
    });

    it('accepts a 63-char app (the pattern maximum)', () => {
      const app = 'a'.repeat(63);
      expect(parseSetupParams(`?app=${app}&user=${VALID_USER}`)).toMatchObject({ ok: true, app });
    });
  });

  describe('name handling', () => {
    const base = `?app=tasktracker&user=${VALID_USER}`;

    it.each([
      ['whitespace only', '%20%20%20'],
      ['empty', ''],
      ['65 chars after trim', 'a'.repeat(NAME_MAX_LENGTH + 1)],
      ['65 chars after trim with surrounding spaces', `%20${'a'.repeat(NAME_MAX_LENGTH + 1)}%20`],
    ])('falls back to app for a name that is %s', (_label, name) => {
      expect(parseSetupParams(`${base}&name=${name}`)).toMatchObject({
        ok: true,
        displayName: 'tasktracker',
        nameIsFallback: true,
      });
    });

    it('keeps a 64-char name', () => {
      const name = 'a'.repeat(NAME_MAX_LENGTH);
      expect(parseSetupParams(`${base}&name=${name}`)).toMatchObject({
        displayName: name,
        nameIsFallback: false,
      });
    });

    it('counts the name cap in characters, not UTF-16 units', () => {
      const name = '\u{1F600}'.repeat(NAME_MAX_LENGTH);
      expect(parseSetupParams(`${base}&name=${encodeURIComponent(name)}`)).toMatchObject({
        displayName: name,
        nameIsFallback: false,
      });
      const tooLong = '\u{1F600}'.repeat(NAME_MAX_LENGTH + 1);
      expect(parseSetupParams(`${base}&name=${encodeURIComponent(tooLong)}`)).toMatchObject({
        displayName: 'tasktracker',
        nameIsFallback: true,
      });
    });

    it.each([
      ['a control character', 'Task%00Tracker'],
      ['a newline in the middle', 'Task%0ATracker'],
      ['a bidi override (U+202E)', 'Task%E2%80%AETracker'],
      ['a zero-width space (U+200B)', 'Task%E2%80%8BTracker'],
    ])('falls back to app for a name containing %s', (_label, name) => {
      expect(parseSetupParams(`${base}&name=${name}`)).toMatchObject({
        ok: true,
        displayName: 'tasktracker',
        nameIsFallback: true,
      });
    });

    it('keeps markup-looking text as literal text', () => {
      expect(parseSetupParams(`${base}&name=%3Cb%3E`)).toMatchObject({
        displayName: '<b>',
        nameIsFallback: false,
      });
    });

    it('falls back to app when name is absent', () => {
      expect(parseSetupParams(base)).toMatchObject({ nameIsFallback: true });
    });
  });

  describe('unknown and duplicate params', () => {
    it('ignores unrecognized params', () => {
      const plain = parseSetupParams(`?app=tasktracker&user=${VALID_USER}`);
      const withExtra = parseSetupParams(`?app=tasktracker&user=${VALID_USER}&foo=bar`);
      expect(withExtra).toEqual(plain);
    });

    it('uses only the first occurrence of a repeated app', () => {
      const result = parseSetupParams(`?app=first&app=second&user=${VALID_USER}`);
      expect(result).toMatchObject({
        ok: true,
        app: 'first',
        personalTopic: 'first-v4sf4e5teivpe3zi-all',
        broadcastTopic: 'first-broadcast',
      });
    });

    it('uses only the first occurrence of a repeated user', () => {
      const result = parseSetupParams(
        `?app=tasktracker&user=${VALID_USER}&user=u_aaaaaaaaaaaaaaaa`,
      );
      expect(result).toMatchObject({ ok: true, user: VALID_USER });
    });

    it('does not fall through to a valid later duplicate when the first is invalid', () => {
      expect(parseSetupParams(`?app=BAD&app=tasktracker&user=${VALID_USER}`)).toEqual({
        ok: false,
      });
    });
  });
});

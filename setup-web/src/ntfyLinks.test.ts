import { describe, expect, it } from 'vitest';

import { subscribeHref } from './ntfyLinks';

describe('ntfyLinks', () => {
  it.each([
    ['tasktracker-v4sf4e5teivpe3zi-all', 'ntfy://notifs.4irl.app/tasktracker-v4sf4e5teivpe3zi-all'],
    ['tasktracker-broadcast', 'ntfy://notifs.4irl.app/tasktracker-broadcast'],
  ])('subscribeHref(%s) builds the ntfy:// deep link', (topic, expected) => {
    expect(subscribeHref(topic)).toBe(expected);
  });
});

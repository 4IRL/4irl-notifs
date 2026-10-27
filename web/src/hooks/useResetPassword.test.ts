import { act, renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import { strings } from '../strings';
import { useResetPassword } from './useResetPassword';

const USER_ID = 'u_abc';
const EMAIL = 'a@example.com';

function setup(
  onResetPassword: (params: { appId: string; email: string }) => Promise<{ password: string }>,
) {
  return renderHook(() =>
    useResetPassword({
      users: [{ userId: USER_ID, apps: ['app1'], topicPatterns: [] }],
      emailByPersonHash: new Map([['abc', EMAIL]]),
      onResetPassword: onResetPassword as never,
      headingRef: { current: null },
      triggerByUserIdRef: { current: new Map() },
    }),
  );
}

async function openAndConfirm(result: ReturnType<typeof setup>['result']) {
  act(() => result.current.open({ userId: USER_ID, user: EMAIL }));
  await act(async () => {
    result.current.confirm();
  });
}

describe('useResetPassword', () => {
  it('opens in confirm and closes back to null', () => {
    const { result } = setup(vi.fn());
    act(() => result.current.open({ userId: USER_ID, user: EMAIL }));
    expect(result.current.dialog?.phase).toBe('confirm');
    act(() => result.current.close());
    expect(result.current.dialog).toBeNull();
  });

  it('goes busy then revealed with the password', async () => {
    let release!: (value: { password: string }) => void;
    const request = vi.fn().mockReturnValue(new Promise((resolve) => (release = resolve)));
    const { result } = setup(request);
    await openAndConfirm(result);
    expect(request).toHaveBeenCalledWith({ appId: 'app1', email: EMAIL });
    expect(result.current.dialog?.phase).toBe('busy');
    expect(result.current.busyUserId).toBe(USER_ID);
    await act(async () => release({ password: 'pw-1' }));
    expect(result.current.dialog).toMatchObject({ phase: 'revealed', password: 'pw-1' });
    expect(result.current.busyUserId).toBeNull();
  });

  it.each([
    [new ApiError({ status: 404, message: 'x' }), strings.resetPasswordNotFound],
    [
      new ApiError({ status: 429, message: 'x', retryAfterSeconds: 12 }),
      strings.resetPasswordTooMany({ seconds: 12 }),
    ],
    [
      new ApiError({ status: 429, message: 'x' }),
      strings.resetPasswordTooMany({ seconds: strings.resetPasswordDefaultRetrySeconds }),
    ],
    [new ApiError({ status: 500, message: 'x' }), strings.resetPasswordFailed],
    [new Error('boom'), strings.resetPasswordFailed],
  ])('maps a rejection to a failed message', async (rejection, message) => {
    const { result } = setup(vi.fn().mockRejectedValue(rejection));
    await openAndConfirm(result);
    expect(result.current.dialog).toMatchObject({ phase: 'failed', message });
    expect(result.current.busyUserId).toBeNull();
  });

  it('does not fire a second request on a repeated confirm', async () => {
    const request = vi.fn().mockReturnValue(new Promise(() => undefined));
    const { result } = setup(request);
    await openAndConfirm(result);
    await act(async () => {
      result.current.confirm();
    });
    expect(request).toHaveBeenCalledTimes(1);
  });
});

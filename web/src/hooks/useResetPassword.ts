import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { RefObject } from 'react';

import { ApiError } from '../api/client';
import type { ResetPasswordParams, ResetPasswordResult, UserSummary } from '../api/client';
import { strings } from '../strings';
import { useCopyStatus } from './useCopyStatus';

/** The reset dialog, owned by the section so it outlives its row once a request is in flight. */
export type ResetDialog =
  | { phase: 'confirm' | 'busy'; userId: string; user: string }
  | { phase: 'revealed'; userId: string; user: string; password: string }
  | { phase: 'failed'; userId: string; user: string; message: string };

/** The client-side bound on one reset request, so the non-dismissible busy phase cannot hang. */
const RESET_TIMEOUT_MS = 30_000;

/** Maps a failed reset to its plain message (404, 429 with the retry time, anything else). */
function resetFailureMessage(rejection: unknown): string {
  if (rejection instanceof ApiError && rejection.status === 404) {
    return strings.resetPasswordNotFound;
  }
  if (rejection instanceof ApiError && rejection.status === 429) {
    return strings.resetPasswordTooMany({
      seconds: rejection.retryAfterSeconds ?? strings.resetPasswordDefaultRetrySeconds,
    });
  }
  return strings.resetPasswordFailed;
}

interface UseResetPasswordParams {
  users: UserSummary[];
  /** personHash (ntfy userId minus its "u_" prefix) -> email. */
  emailByPersonHash: Map<string, string>;
  onResetPassword?: (params: ResetPasswordParams) => Promise<ResetPasswordResult>;
  /** The Users heading: where focus lands when the row's trigger is gone. */
  headingRef: RefObject<HTMLHeadingElement | null>;
  /** Each row's Reset trigger, by user id: where focus returns on close. */
  triggerByUserIdRef: RefObject<Map<string, HTMLButtonElement>>;
}

/**
 * The reset flow's state machine (confirm, busy, revealed, failed) with its request sequencing,
 * 30 s timeout and focus targets. The section owns it, so the dialog outlives its row once a
 * request is in flight.
 */
export function useResetPassword({
  users,
  emailByPersonHash,
  onResetPassword,
  headingRef,
  triggerByUserIdRef,
}: UseResetPasswordParams) {
  const [busyUserId, setBusyUserId] = useState<string | null>(null);
  const [dialog, setDialog] = useState<ResetDialog | null>(null);
  const { status: copyStatus, copy, clear: clearCopyStatus } = useCopyStatus();
  const resetSequenceRef = useRef(0);
  const timeoutRef = useRef<number | null>(null);
  /** True from Confirm until the next dialog opens: a second activation can never re-fire. */
  const startedRef = useRef(false);
  const fallbackFocusRef = useRef<HTMLElement | null>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);

  /** The row's identity as the reset needs it, or null when it is gone or not reset-eligible. */
  function resetTarget(
    userId: string,
  ): { displayName: string; appId: string; email: string } | null {
    const row = users.find((candidate) => candidate.userId === userId);
    if (row === undefined || row.apps.length === 0 || !row.userId.startsWith('u_')) {
      return null;
    }
    const email = emailByPersonHash.get(row.userId.slice(2));
    return email === undefined ? null : { displayName: email, appId: row.apps[0], email };
  }

  /** Points the fallback at the Users heading when it is visible, else the Provisioning tab. */
  function resolveFallback() {
    const heading = headingRef.current;
    fallbackFocusRef.current =
      heading !== null && heading.closest('[hidden]') === null
        ? heading
        : document.getElementById('app-tab-provisioning');
  }

  function clearResetTimeout() {
    if (timeoutRef.current !== null) {
      window.clearTimeout(timeoutRef.current);
      timeoutRef.current = null;
    }
  }

  // Keep the focus targets current before Modal's close effect reads them, so even the
  // render-time confirm-phase close (which has no handler) lands somewhere visible.
  useLayoutEffect(() => {
    resolveFallback();
    // Never reset to null on close: Modal reads this in its passive cleanup, after this effect.
    if (dialog !== null) {
      returnFocusRef.current = triggerByUserIdRef.current.get(dialog.userId) ?? null;
    }
  });

  // No timer or late response may touch a table that is gone.
  useEffect(
    () => () => {
      clearResetTimeout();
      resetSequenceRef.current += 1;
    },
    [],
  );

  // The confirm phase belongs to its row: when the row leaves the list or stops being
  // reset-eligible the dialog closes without firing. Busy, revealed and failed persist.
  if (dialog?.phase === 'confirm' && resetTarget(dialog.userId) === null) {
    setDialog(null);
  }

  function open({ userId, user }: { userId: string; user: string }) {
    if (busyUserId !== null) {
      return;
    }
    startedRef.current = false;
    setDialog({ phase: 'confirm', userId, user });
  }

  function close() {
    resolveFallback();
    clearResetTimeout();
    clearCopyStatus();
    setDialog(null);
  }

  function confirm() {
    if (
      onResetPassword === undefined ||
      dialog?.phase !== 'confirm' ||
      busyUserId !== null ||
      startedRef.current
    ) {
      return;
    }
    const target = resetTarget(dialog.userId);
    if (target === null) {
      close();
      return;
    }
    const { userId } = dialog;
    const { displayName, appId, email } = target;
    resetSequenceRef.current += 1;
    const sequence = resetSequenceRef.current;
    clearCopyStatus();
    setDialog({ phase: 'busy', userId, user: displayName });
    setBusyUserId(userId);
    clearResetTimeout();
    timeoutRef.current = window.setTimeout(() => {
      timeoutRef.current = null;
      // Invalidate the request so its late response and finally are discarded.
      resetSequenceRef.current += 1;
      setDialog({
        phase: 'failed',
        userId,
        user: displayName,
        message: strings.resetPasswordTimedOut,
      });
      setBusyUserId(null);
    }, RESET_TIMEOUT_MS);
    startedRef.current = true;
    let request: Promise<ResetPasswordResult>;
    try {
      request = onResetPassword({ appId, email });
    } catch (thrown) {
      request = Promise.reject(thrown);
    }
    request
      .then((result) => {
        if (sequence === resetSequenceRef.current) {
          setDialog({ phase: 'revealed', userId, user: displayName, password: result.password });
        }
      })
      .catch((rejection: unknown) => {
        if (sequence === resetSequenceRef.current) {
          setDialog({
            phase: 'failed',
            userId,
            user: displayName,
            message: resetFailureMessage(rejection),
          });
        }
      })
      .finally(() => {
        if (sequence === resetSequenceRef.current) {
          clearResetTimeout();
          setBusyUserId(null);
        }
      });
  }

  return {
    dialog,
    busyUserId,
    copyStatus,
    copy,
    returnFocusRef,
    fallbackFocusRef,
    open,
    close,
    confirm,
  };
}

import { useRef, useState } from 'react';
import type { JSX } from 'react';

import { ApiError } from '../api/client';
import type { ResetPasswordParams, ResetPasswordResult, UserSummary } from '../api/client';
import { useCopyStatus } from '../hooks/useCopyStatus';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import './UsersTable.css';

/** Identifies an app-scoped deprovision target within a user's row. */
interface DeprovisionParams {
  userId: string;
  appId: string;
}

/** Identifies a user targeted for deletion. */
interface DeleteParams {
  userId: string;
}

interface UsersTableProps {
  users: UserSummary[];
  loading: boolean;
  /** personHash (ntfy userId minus its "u_" prefix) -> email, for display only. */
  emailByPersonHash: Map<string, string>;
  onDeprovision: (params: DeprovisionParams) => void;
  onDelete: (params: DeleteParams) => void;
  /** Makes the person a new password. Without it the Reset password action is not offered. */
  onResetPassword?: (params: ResetPasswordParams) => Promise<ResetPasswordResult>;
}

/** The latest reset attempt: the one-time password, or a failure message (never both). */
type ResetOutcome =
  | { kind: 'revealed'; userId: string; user: string; password: string }
  | { kind: 'failed'; userId: string; message: string };

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

export function UsersTable({
  users,
  loading,
  emailByPersonHash,
  onDeprovision,
  onDelete,
  onResetPassword,
}: UsersTableProps): JSX.Element {
  const [busyUserId, setBusyUserId] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<ResetOutcome | null>(null);
  const { status: copyStatus, copy, clear: clearCopyStatus } = useCopyStatus();
  const resetSequenceRef = useRef(0);
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  const triggerByUserIdRef = useRef(new Map<string, HTMLButtonElement>());

  // The reveal belongs to a row: when that row leaves the list (the user was
  // deleted or the list reloaded without them) the password is dropped.
  if (outcome !== null && !users.some((user) => user.userId === outcome.userId)) {
    setOutcome(null);
  }

  function handleReset({
    userId,
    user,
    appId,
    email,
  }: {
    userId: string;
    user: string;
    appId: string;
    email: string;
  }) {
    if (onResetPassword === undefined || busyUserId !== null) {
      return;
    }
    resetSequenceRef.current += 1;
    const sequence = resetSequenceRef.current;
    clearCopyStatus();
    setOutcome(null);
    setBusyUserId(userId);
    onResetPassword({ appId, email })
      .then((result) => {
        if (sequence === resetSequenceRef.current) {
          setOutcome({ kind: 'revealed', userId, user, password: result.password });
        }
      })
      .catch((rejection: unknown) => {
        if (sequence === resetSequenceRef.current) {
          setOutcome({ kind: 'failed', userId, message: resetFailureMessage(rejection) });
        }
      })
      .finally(() => {
        if (sequence === resetSequenceRef.current) {
          setBusyUserId(null);
        }
      });
  }

  function handleDone(userId: string) {
    setOutcome(null);
    clearCopyStatus();
    // If the row's confirm pair is open its trigger is unmounted; fall back to
    // the section heading so focus never drops to the page body.
    (triggerByUserIdRef.current.get(userId) ?? headingRef.current)?.focus();
  }

  return (
    <section className="users-table">
      <h2 ref={headingRef} tabIndex={-1}>
        {strings.usersHeading}
      </h2>
      {loading && <p className="users-table__status">{strings.usersLoading}</p>}
      {!loading && users.length === 0 && (
        <p className="users-table__status">{strings.usersEmpty}</p>
      )}
      {!loading && users.length > 0 && (
        <table className="users-table__table">
          <thead>
            <tr>
              <th scope="col">{strings.columnUser}</th>
              <th scope="col">{strings.columnApps}</th>
              <th scope="col">{strings.columnTopicPatterns}</th>
              <th scope="col" aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {users.map(({ userId, apps, topicPatterns }) => {
              const personHash = userId.startsWith('u_') ? userId.slice(2) : null;
              const email = personHash !== null ? emailByPersonHash.get(personHash) : undefined;
              const displayName = email ?? userId;
              const resetTriggerLabel = strings.resetPasswordTriggerNamed({ user: displayName });
              const isResetBusy = busyUserId === userId;
              return (
                <tr key={userId}>
                  <td>{displayName}</td>
                  <td>
                    {apps.map((app) => (
                      <span key={app} className="users-table__chip">
                        {app}
                      </span>
                    ))}
                  </td>
                  <td>{topicPatterns.join(', ')}</td>
                  <td>
                    <div className="users-table__actions">
                      {onResetPassword !== undefined && email !== undefined && apps.length > 0 && (
                        <ConfirmButton
                          triggerLabel={strings.resetPasswordAction}
                          confirmLabel={strings.confirmResetPasswordAction}
                          triggerAriaLabel={resetTriggerLabel}
                          confirmAriaLabel={strings.resetPasswordConfirmNamed({
                            user: displayName,
                          })}
                          dismissAriaLabel={strings.cancelActionNamed({
                            subject: resetTriggerLabel,
                          })}
                          confirmNotice={strings.resetPasswordNotice}
                          busy={busyUserId !== null}
                          busyLabel={isResetBusy ? strings.resettingPasswordAction : undefined}
                          busyAriaLabel={
                            isResetBusy
                              ? strings.resetPasswordBusyNamed({ user: displayName })
                              : resetTriggerLabel
                          }
                          className="users-table__button users-table__button--secondary users-table__button--tall"
                          registerTrigger={(element) => {
                            if (element === null) {
                              triggerByUserIdRef.current.delete(userId);
                            } else {
                              triggerByUserIdRef.current.set(userId, element);
                            }
                          }}
                          onConfirm={() =>
                            handleReset({ userId, user: displayName, appId: apps[0], email })
                          }
                        />
                      )}
                      {onResetPassword !== undefined &&
                        personHash !== null &&
                        email === undefined && (
                          <span className="users-table__hint">
                            {strings.resetPasswordUnavailable}
                          </span>
                        )}
                      {apps.map((app) => (
                        <ConfirmButton
                          key={app}
                          triggerLabel={`${strings.deprovisionAction} ${app}`}
                          confirmLabel={`${strings.confirmDeprovisionAction} ${app}`}
                          dismissAriaLabel={strings.cancelActionNamed({
                            subject: strings.deprovisionAppForUser({ app, user: displayName }),
                          })}
                          className="users-table__button users-table__button--secondary"
                          onConfirm={() => onDeprovision({ userId, appId: app })}
                        />
                      ))}
                      <ConfirmButton
                        triggerLabel={`${strings.deleteAction} ${displayName}`}
                        confirmLabel={`${strings.confirmDeleteAction} ${displayName}`}
                        dismissAriaLabel={strings.cancelActionNamed({
                          subject: `${strings.deleteAction} ${displayName}`,
                        })}
                        className="users-table__button users-table__button--danger"
                        onConfirm={() => onDelete({ userId })}
                      />
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {onResetPassword !== undefined && (
        <>
          <div role="status" aria-atomic="false" className="users-table__reset-status">
            {outcome?.kind === 'revealed' && (
              <p>{strings.resetPasswordRevealLead({ user: outcome.user })}</p>
            )}
            {outcome?.kind === 'failed' && <p>{outcome.message}</p>}
            {outcome?.kind === 'revealed' && copyStatus !== null && (
              <p
                key={copyStatus.count}
                className={
                  copyStatus.kind === 'copied'
                    ? 'users-table__copy-ok'
                    : 'users-table__copy-fallback'
                }
              >
                {copyStatus.kind === 'copied'
                  ? strings.passwordCopied
                  : strings.passwordCopyUnavailable}
              </p>
            )}
          </div>
          {outcome?.kind === 'revealed' && (
            <div
              role="group"
              aria-labelledby="users-reset-password-heading"
              className="users-table__reveal"
              ref={(element) => {
                // Bring the one-time reveal into view without moving focus.
                element?.scrollIntoView?.({ block: 'nearest' });
              }}
            >
              <h3 id="users-reset-password-heading">{strings.passwordGroupHeading}</h3>
              <label htmlFor="users-reset-password">{strings.passwordLabel}</label>
              <div className="users-table__reveal-row">
                <input
                  id="users-reset-password"
                  type="text"
                  readOnly
                  autoComplete="off"
                  spellCheck={false}
                  className="users-table__password"
                  value={outcome.password}
                  onFocus={(event) => event.currentTarget.select()}
                />
                <button
                  type="button"
                  className="users-table__button users-table__button--tall users-table__button--secondary"
                  onClick={() => copy(outcome.password)}
                >
                  {strings.copyPassword}
                </button>
                <button
                  type="button"
                  className="users-table__button users-table__button--tall users-table__button--plain"
                  aria-label={strings.resetPasswordDoneNamed({ user: outcome.user })}
                  onClick={() => handleDone(outcome.userId)}
                >
                  {strings.resetPasswordDone}
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </section>
  );
}

import { useRef } from 'react';
import type { JSX } from 'react';

import type { ResetPasswordParams, ResetPasswordResult, UserSummary } from '../api/client';
import { useResetPassword } from '../hooks/useResetPassword';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { ResetPasswordDialog } from './ResetPasswordDialog';
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

export function UsersTable({
  users,
  loading,
  emailByPersonHash,
  onDeprovision,
  onDelete,
  onResetPassword,
}: UsersTableProps): JSX.Element {
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  const triggerByUserIdRef = useRef(new Map<string, HTMLButtonElement>());
  const reset = useResetPassword({
    users,
    emailByPersonHash,
    onResetPassword,
    headingRef,
    triggerByUserIdRef,
  });
  const { busyUserId } = reset;

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
                        <button
                          type="button"
                          className="users-table__button users-table__button--secondary users-table__button--tall"
                          aria-label={
                            isResetBusy
                              ? strings.resetPasswordBusyNamed({ user: displayName })
                              : resetTriggerLabel
                          }
                          aria-disabled={busyUserId !== null ? 'true' : undefined}
                          ref={(element) => {
                            if (element === null) {
                              triggerByUserIdRef.current.delete(userId);
                            } else {
                              triggerByUserIdRef.current.set(userId, element);
                            }
                          }}
                          onClick={() => reset.open({ userId, user: displayName })}
                        >
                          {isResetBusy
                            ? strings.resettingPasswordAction
                            : strings.resetPasswordAction}
                        </button>
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
                          dialogTitle={strings.deprovisionDialogTitle}
                          dialogItem={{
                            label: strings.dialogItemUserApp,
                            value: strings.dialogUserApp({ user: displayName, app }),
                          }}
                          confirmNotice={strings.deprovisionNotice}
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
                        dialogTitle={strings.deleteUserDialogTitle}
                        dialogItem={{ label: strings.dialogItemUser, value: displayName }}
                        confirmNotice={strings.deleteUserNotice}
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
        <ResetPasswordDialog
          dialog={reset.dialog}
          copyStatus={reset.copyStatus}
          copy={reset.copy}
          returnFocusRef={reset.returnFocusRef}
          fallbackFocusRef={reset.fallbackFocusRef}
          onClose={reset.close}
          onConfirm={reset.confirm}
        />
      )}
    </section>
  );
}

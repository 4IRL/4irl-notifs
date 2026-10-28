import { useRef } from 'react';
import type { JSX } from 'react';

import type { ResetPasswordParams, ResetPasswordResult, UserSummary } from '../api/client';
import { useCompactTable } from '../hooks/useCompactTable';
import { useResetPassword } from '../hooks/useResetPassword';
import { strings } from '../strings';
import { revealFocusedControl } from './adminTableFocus';
import { ConfirmButton } from './ConfirmButton';
import { ResetPasswordDialog } from './ResetPasswordDialog';
import { RowActionsMenu } from './RowActionsMenu';
import './adminTable.css';
import './UsersTable.css';

// Focus-registry key prefix the compact kebab registers under, so the reset dialog's focus
// return lands on the row's kebab (the inline Reset button does not exist in that layout).
const RESET_FOCUS_PREFIX = 'reset:';

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
  const compact = useCompactTable();
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
        <div
          className="admin-table__scroll users-table__scroll"
          onFocus={revealFocusedControl}
          role="group"
          aria-label={strings.usersHeading}
          tabIndex={0}
        >
          <table className="admin-table">
            <thead>
              <tr>
                <th scope="col" className="admin-table__lead">
                  {strings.columnUser}
                </th>
                {!compact && (
                  <>
                    <th scope="col">{strings.columnApps}</th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnTopicPatterns}
                    </th>
                  </>
                )}
                <th
                  scope="col"
                  className={`admin-table__actions${compact ? ' admin-table__kebab' : ''}`}
                  aria-label={strings.columnActions}
                />
              </tr>
            </thead>
            <tbody>
              {users.map(({ userId, apps, topicPatterns }) => {
                const personHash = userId.startsWith('u_') ? userId.slice(2) : null;
                const email = personHash !== null ? emailByPersonHash.get(personHash) : undefined;
                const displayName = email ?? userId;
                const resetTriggerLabel = strings.resetPasswordTriggerNamed({ user: displayName });
                const isResetBusy = busyUserId === userId;
                const canReset =
                  onResetPassword !== undefined && email !== undefined && apps.length > 0;
                const resetUnavailable =
                  onResetPassword !== undefined && personHash !== null && email === undefined;
                const resetAriaLabel = isResetBusy
                  ? strings.resetPasswordBusyNamed({ user: displayName })
                  : resetTriggerLabel;
                const resetVisible = isResetBusy
                  ? strings.resettingPasswordRowAction
                  : strings.resetPasswordAction;
                const deleteLabel = `${strings.deleteAction} ${displayName}`;
                const appChips = apps.map((app) => (
                  <span key={app} className="users-table__chip">
                    {app}
                  </span>
                ));
                return (
                  <tr key={userId}>
                    <td className="admin-table__lead" title={displayName}>
                      {displayName}
                      {compact && (
                        <span className="admin-table__sub">
                          {appChips}
                          {topicPatterns.length > 0 && (
                            <span className="users-table__patterns">
                              {topicPatterns.join(', ')}
                            </span>
                          )}
                        </span>
                      )}
                    </td>
                    {!compact && (
                      <>
                        <td>{appChips}</td>
                        <td className="admin-table__fill">{topicPatterns.join(', ')}</td>
                      </>
                    )}
                    {compact ? (
                      <td className="admin-table__actions admin-table__kebab">
                        <RowActionsMenu
                          rowLabel={displayName}
                          note={resetUnavailable ? strings.resetPasswordUnavailable : null}
                          focusKeys={[`${RESET_FOCUS_PREFIX}${userId}`]}
                          registerControl={({ key, element }) => {
                            if (element === null) {
                              triggerByUserIdRef.current.delete(userId);
                            } else if (key === `${RESET_FOCUS_PREFIX}${userId}`) {
                              triggerByUserIdRef.current.set(userId, element as HTMLButtonElement);
                            }
                          }}
                          items={[
                            ...(canReset
                              ? [
                                  {
                                    id: 'reset',
                                    label: resetVisible,
                                    ariaLabel: resetAriaLabel,
                                    disabled: busyUserId !== null,
                                    onSelect: () => reset.open({ userId, user: displayName }),
                                  },
                                ]
                              : []),
                            ...apps.map((app) => ({
                              id: `deprovision:${app}`,
                              label: `${strings.deprovisionAction} ${app}`,
                              ariaLabel: strings.deprovisionAppForUser({ app, user: displayName }),
                              confirm: {
                                confirmLabel: strings.confirmDeprovisionAction,
                                confirmAriaLabel: `${strings.confirmDeprovisionAction} ${app}`,
                                cancelLabel: strings.cancelAction,
                                cancelAriaLabel: strings.cancelActionNamed({
                                  subject: strings.deprovisionAppForUser({
                                    app,
                                    user: displayName,
                                  }),
                                }),
                                notice: strings.deprovisionNotice,
                                dialogTitle: strings.deprovisionDialogTitle,
                                dialogItem: {
                                  label: strings.dialogItemUserApp,
                                  value: strings.dialogUserApp({ user: displayName, app }),
                                },
                                onConfirm: () => onDeprovision({ userId, appId: app }),
                              },
                            })),
                            {
                              id: 'delete',
                              label: strings.deleteAction,
                              ariaLabel: deleteLabel,
                              tone: 'danger' as const,
                              confirm: {
                                confirmLabel: strings.confirmDeleteAction,
                                confirmAriaLabel: `${strings.confirmDeleteAction} ${displayName}`,
                                cancelLabel: strings.cancelAction,
                                cancelAriaLabel: strings.cancelActionNamed({
                                  subject: deleteLabel,
                                }),
                                notice: strings.deleteUserNotice,
                                dialogTitle: strings.deleteUserDialogTitle,
                                dialogItem: { label: strings.dialogItemUser, value: displayName },
                                onConfirm: () => onDelete({ userId }),
                              },
                            },
                          ]}
                        />
                      </td>
                    ) : (
                      <td className="admin-table__actions">
                        <div className="users-table__actions">
                          {canReset && (
                            <button
                              type="button"
                              className="users-table__button users-table__button--secondary users-table__button--tall"
                              aria-label={resetAriaLabel}
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
                              {resetVisible}
                            </button>
                          )}
                          {resetUnavailable && (
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
                            triggerLabel={deleteLabel}
                            confirmLabel={`${strings.confirmDeleteAction} ${displayName}`}
                            dismissAriaLabel={strings.cancelActionNamed({ subject: deleteLabel })}
                            dialogTitle={strings.deleteUserDialogTitle}
                            dialogItem={{ label: strings.dialogItemUser, value: displayName }}
                            confirmNotice={strings.deleteUserNotice}
                            className="users-table__button users-table__button--danger"
                            onConfirm={() => onDelete({ userId })}
                          />
                        </div>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
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

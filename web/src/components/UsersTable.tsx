import type { JSX } from 'react';

import { strings } from '../strings';
import type { UserSummary } from '../api/client';
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
}

export function UsersTable({
  users,
  loading,
  emailByPersonHash,
  onDeprovision,
  onDelete,
}: UsersTableProps): JSX.Element {
  return (
    <section className="users-table">
      <h2>{strings.usersHeading}</h2>
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
              const displayName =
                (personHash !== null ? emailByPersonHash.get(personHash) : undefined) ?? userId;
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
                      {apps.map((app) => (
                        <ConfirmButton
                          key={app}
                          triggerLabel={`${strings.deprovisionAction} ${app}`}
                          confirmLabel={`${strings.confirmDeprovisionAction} ${app}`}
                          className="users-table__button users-table__button--secondary"
                          onConfirm={() => onDeprovision({ userId, appId: app })}
                        />
                      ))}
                      <ConfirmButton
                        triggerLabel={`${strings.deleteAction} ${displayName}`}
                        confirmLabel={`${strings.confirmDeleteAction} ${displayName}`}
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
    </section>
  );
}

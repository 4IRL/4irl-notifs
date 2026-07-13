import type { JSX } from 'react';

import { strings } from '../strings';
import type { UserSummary } from '../api/client';
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
  onDeprovision: (params: DeprovisionParams) => void;
  onDelete: (params: DeleteParams) => void;
}

export function UsersTable({
  users,
  loading,
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
            {users.map(({ userId, apps, topicPatterns }) => (
              <tr key={userId}>
                <td>{userId}</td>
                <td>
                  {apps.map((app) => (
                    <span key={app} className="users-table__chip">
                      {app}
                    </span>
                  ))}
                </td>
                <td>{topicPatterns.join(', ')}</td>
                <td className="users-table__actions">
                  {apps.map((app) => (
                    <button
                      key={app}
                      type="button"
                      className="users-table__button users-table__button--secondary"
                      onClick={() => onDeprovision({ userId, appId: app })}
                    >
                      {`${strings.deprovisionAction} ${app}`}
                    </button>
                  ))}
                  <button
                    type="button"
                    className="users-table__button users-table__button--danger"
                    onClick={() => onDelete({ userId })}
                  >
                    {`${strings.deleteAction} ${userId}`}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

import { Fragment, useState } from 'react';
import type { JSX } from 'react';

import type { ProvisionAppResult } from '../api/client';
import type { AppSummary, UpdateAppParams } from '../api/personClient';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { EditAppForm } from './EditAppForm';
import './AppsTable.css';

/** Identifies an app targeted for removal. */
interface RemoveAppParams {
  appId: string;
}

interface AppsTableProps {
  apps: AppSummary[];
  /** app_id -> live subscriber count, derived from the users list. */
  subscriberCountByApp: Map<string, number>;
  loading: boolean;
  error: string | null;
  onUpdateApp: (params: UpdateAppParams) => Promise<void>;
  onRemintToken: (params: { appId: string; rotate: boolean }) => Promise<ProvisionAppResult>;
  onRemoveApp: (params: RemoveAppParams) => void;
}

// The Edit form spans every column when expanded under its row.
const COLUMN_COUNT = 6;

export function AppsTable({
  apps,
  subscriberCountByApp,
  loading,
  error,
  onUpdateApp,
  onRemintToken,
  onRemoveApp,
}: AppsTableProps): JSX.Element {
  const [editingAppId, setEditingAppId] = useState<string | null>(null);

  return (
    <section className="apps-table">
      <h2>{strings.appsHeading}</h2>
      {loading && <p className="apps-table__status">{strings.appsLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="apps-table__status">
          {error}
        </p>
      )}
      {!loading && error === null && apps.length === 0 && (
        <p className="apps-table__status">{strings.appsEmpty}</p>
      )}
      {!loading && error === null && apps.length > 0 && (
        <table className="apps-table__table">
          <thead>
            <tr>
              <th scope="col">{strings.columnAppName}</th>
              <th scope="col">{strings.columnAppId}</th>
              <th scope="col">{strings.columnAppDescription}</th>
              <th scope="col">{strings.columnSubscribers}</th>
              <th scope="col">{strings.columnCreated}</th>
              <th scope="col" aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {apps.map((app) => {
              const isEditing = editingAppId === app.appId;
              return (
                <Fragment key={app.appId}>
                  <tr>
                    <td>{app.displayName}</td>
                    <td className="apps-table__id">{app.appId}</td>
                    <td>{app.description ?? ''}</td>
                    <td>{subscriberCountByApp.get(app.appId) ?? 0}</td>
                    <td>{app.createdAt}</td>
                    <td>
                      <div className="apps-table__actions">
                        <button
                          type="button"
                          className="apps-table__button apps-table__button--secondary"
                          onClick={() => setEditingAppId(isEditing ? null : app.appId)}
                        >
                          {`${strings.editAction} ${app.displayName}`}
                        </button>
                        <ConfirmButton
                          triggerLabel={`${strings.removeAction} ${app.displayName}`}
                          confirmLabel={`${strings.confirmRemoveAction} ${app.displayName}`}
                          className="apps-table__button apps-table__button--danger"
                          onConfirm={() => onRemoveApp({ appId: app.appId })}
                        />
                      </div>
                    </td>
                  </tr>
                  {isEditing && (
                    <tr>
                      <td colSpan={COLUMN_COUNT}>
                        <EditAppForm
                          app={app}
                          onUpdateApp={onUpdateApp}
                          onRemintToken={onRemintToken}
                          onClose={() => setEditingAppId(null)}
                        />
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      )}
    </section>
  );
}

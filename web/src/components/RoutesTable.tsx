import { Fragment } from 'react';
import type { JSX } from 'react';

import type { DestinationSummary, RouteSummary } from '../api/deliveryClient';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { RouteForm } from './RouteForm';
import type { RouteSubmission } from './RouteForm';
import './RoutesTable.css';

/** Mutating route actions that carry a per-route busy flag. */
export type RouteAction = 'save' | 'delete';

interface RoutesTableProps {
  routes: RouteSummary[];
  /** Destinations for the edit form's checkboxes (empty while loading or failed). */
  destinations: DestinationSummary[];
  editingId: number | null;
  isBusy: (params: { action: RouteAction; id: number }) => boolean;
  rowErrors: Record<number, string>;
  /** Registers a focusable control under a key so the owner can move focus to it. */
  registerControl: (params: { key: string; element: HTMLElement | null }) => void;
  onToggleEditor: (id: number) => void;
  onCloseEditor: (id: number) => void;
  onDelete: (id: number) => void;
  onSave: (params: { id: number } & RouteSubmission) => Promise<void>;
}

const COLUMN_COUNT = 5;

export function RoutesTable({
  routes,
  destinations,
  editingId,
  isBusy,
  rowErrors,
  registerControl,
  onToggleEditor,
  onCloseEditor,
  onDelete,
  onSave,
}: RoutesTableProps): JSX.Element {
  return (
    <div className="routes__scroll">
      <table className="routes__table">
        <thead>
          <tr>
            <th scope="col">{strings.columnRouteApp}</th>
            <th scope="col">{strings.columnRouteChannel}</th>
            <th scope="col">{strings.columnRouteDestinations}</th>
            <th scope="col">{strings.columnRouteStatus}</th>
            <th scope="col" aria-label={strings.columnRouteActions} />
          </tr>
        </thead>
        <tbody>
          {routes.map((route) => {
            const { id } = route;
            const isEditing = editingId === id;
            const rowError = rowErrors[id];
            return (
              <Fragment key={id}>
                <tr>
                  <td className="routes__mono">{route.appPattern}</td>
                  <td className="routes__mono">{route.channelPattern}</td>
                  <td>
                    {route.destinations.map((name) => (
                      <span key={name} className="routes__chip">
                        {name}
                      </span>
                    ))}
                  </td>
                  <td>
                    <span
                      className={`routes__pill routes__pill--${route.enabled ? 'ok' : 'neutral'}`}
                    >
                      {route.enabled ? strings.routeEnabled : strings.routeOff}
                    </span>
                  </td>
                  <td>
                    <div className="routes__actions">
                      <button
                        ref={(element) => registerControl({ key: `edit:${id}`, element })}
                        type="button"
                        className="routes__button"
                        aria-expanded={isEditing}
                        onClick={() => onToggleEditor(id)}
                      >
                        {isEditing ? strings.closeRoute({ id }) : strings.editRoute({ id })}
                      </button>
                      <ConfirmButton
                        triggerLabel={strings.deleteRoute({ id })}
                        confirmLabel={strings.confirmDeleteRoute({ id })}
                        className="routes__button routes__button--danger"
                        busy={isBusy({ action: 'delete', id })}
                        busyLabel={strings.deletingRoute}
                        onConfirm={() => onDelete(id)}
                      />
                    </div>
                    {rowError !== undefined && (
                      <p role="alert" className="routes__row-error">
                        {rowError}
                      </p>
                    )}
                  </td>
                </tr>
                {isEditing && (
                  <tr>
                    <td colSpan={COLUMN_COUNT}>
                      <RouteForm
                        route={route}
                        destinations={destinations}
                        saving={isBusy({ action: 'save', id })}
                        onSubmit={(submission) => onSave({ id, ...submission })}
                        onClose={() => onCloseEditor(id)}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

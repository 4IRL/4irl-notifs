import type { JSX } from 'react';

import type { RouteSummary } from '../api/deliveryClient';
import { useCompactTable } from '../hooks/useCompactTable';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { OwnerBadge } from './OwnerBadge';
import { ownerInlineNoteOf, ownerNoteOf } from './ownership';
import { RowActionsMenu } from './RowActionsMenu';
import type { RowMenuItem } from './RowActionsMenu';
import { routeActionLabels } from './rowActionLabels';
import { revealFocusedControl } from './adminTableFocus';
import './adminTable.css';
import './RoutesTable.css';

/** Mutating route actions that carry a per-route busy flag. */
export type RouteAction = 'save' | 'delete';

interface RoutesTableProps {
  routes: RouteSummary[];
  isBusy: (params: { action: RouteAction; id: number }) => boolean;
  rowErrors: Record<number, string>;
  /** Registers a focusable control under a key so the owner can move focus to it. */
  registerControl: (params: { key: string; element: HTMLElement | null }) => void;
  /** Opens the edit dialog for the route. */
  onEdit: (id: number) => void;
  onDelete: (id: number) => void;
}

type RouteColumn = 'app' | 'channel' | 'owner' | 'destinations' | 'status';

// Roomy layout: the table's natural order.
const WIDE_COLUMNS: RouteColumn[] = ['app', 'channel', 'owner', 'destinations', 'status'];
// Narrow layout: identity and state first, so the first screen shows what the row is. No Owner
// column here: the owner badge sits under the App value in the first cell.
const COMPACT_COLUMNS: RouteColumn[] = ['app', 'channel', 'status', 'destinations'];

export function RoutesTable({
  routes,
  isBusy,
  rowErrors,
  registerControl,
  onEdit,
  onDelete,
}: RoutesTableProps): JSX.Element {
  const compact = useCompactTable();
  const columns = compact ? COMPACT_COLUMNS : WIDE_COLUMNS;
  const headers: Record<RouteColumn, JSX.Element> = {
    app: (
      <th key="app" scope="col" className="admin-table__lead">
        {strings.columnRouteApp}
      </th>
    ),
    channel: (
      <th key="channel" scope="col">
        {strings.columnRouteChannel}
      </th>
    ),
    owner: (
      <th key="owner" scope="col">
        {strings.columnOwner}
      </th>
    ),
    destinations: (
      <th key="destinations" scope="col" className="admin-table__fill">
        {strings.columnRouteDestinations}
      </th>
    ),
    status: (
      <th key="status" scope="col">
        {strings.columnRouteStatus}
      </th>
    ),
  };

  return (
    <div
      className="admin-table__scroll routes__scroll"
      onFocus={revealFocusedControl}
      role="group"
      aria-labelledby="routes-heading"
      tabIndex={0}
    >
      <table className="admin-table routes__table">
        <thead>
          <tr>
            {columns.map((column) => headers[column])}
            <th
              scope="col"
              className={`admin-table__actions${compact ? ' admin-table__kebab' : ''}`}
              aria-label={strings.columnRouteActions}
            />
          </tr>
        </thead>
        <tbody>
          {routes.map((route) => {
            const { id } = route;
            const rowError = rowErrors[id];
            const ownerNote = ownerNoteOf(route);
            const ownerInlineNote = ownerInlineNoteOf(route);
            const deleteBusy = isBusy({ action: 'delete', id });
            // The visible text is the bare verb (the row names the route); the accessible name
            // keeps the route. A busy action shows its progress text for both.
            const labels = routeActionLabels({ id });
            const menuItems: RowMenuItem[] = [
              {
                id: 'edit',
                label: labels.edit.visible,
                ariaLabel: labels.edit.accessible,
                onSelect: () => onEdit(id),
              },
              {
                id: 'delete',
                label: deleteBusy ? strings.deletingRoute : labels.delete.visible,
                ariaLabel: deleteBusy ? undefined : labels.delete.accessible,
                tone: 'danger',
                disabled: deleteBusy,
                confirm: {
                  confirmLabel: labels.confirmDelete.visible,
                  confirmAriaLabel: labels.confirmDelete.accessible,
                  cancelLabel: labels.cancelDelete.visible,
                  cancelAriaLabel: labels.cancelDelete.accessible,
                  notice: ownerNote ?? undefined,
                  dialogTitle: strings.deleteRouteDialogTitle,
                  dialogItem: {
                    label: strings.dialogItemRoute,
                    value: strings.routeRowLabel({ id }),
                  },
                  onConfirm: () => onDelete(id),
                },
              },
            ];
            const cells: Record<RouteColumn, JSX.Element> = {
              app: (
                <td key="app" className="admin-table__lead routes__mono" title={route.appPattern}>
                  {route.appPattern}
                  {compact && (
                    <span className="admin-table__owner-line">
                      <OwnerBadge ownerKind={route.ownerKind} ownerAppId={route.ownerAppId} />
                    </span>
                  )}
                </td>
              ),
              channel: (
                <td key="channel" className="routes__mono">
                  {route.channelPattern}
                </td>
              ),
              owner: (
                <td key="owner">
                  <OwnerBadge ownerKind={route.ownerKind} ownerAppId={route.ownerAppId} />
                </td>
              ),
              destinations: (
                <td key="destinations" className="admin-table__fill">
                  {route.destinations.map((name) => (
                    <span key={name} className="routes__chip">
                      {name}
                    </span>
                  ))}
                </td>
              ),
              status: (
                <td key="status">
                  <span
                    className={`routes__pill routes__pill--${route.enabled ? 'ok' : 'neutral'}`}
                  >
                    {route.enabled ? strings.routeEnabled : strings.routeOff}
                  </span>
                </td>
              ),
            };
            return (
              <tr key={id}>
                {columns.map((column) => cells[column])}
                {compact ? (
                  <td className="admin-table__actions admin-table__kebab">
                    <RowActionsMenu
                      rowLabel={strings.routeRowLabel({ id })}
                      items={menuItems}
                      note={ownerInlineNote}
                      errors={rowError === undefined ? undefined : [rowError]}
                      focusKeys={[`edit:${id}`]}
                      registerControl={registerControl}
                    />
                  </td>
                ) : (
                  <td className="admin-table__actions">
                    <div className="routes__actions">
                      <button
                        ref={(element) => registerControl({ key: `edit:${id}`, element })}
                        type="button"
                        className="routes__button"
                        aria-label={labels.edit.accessible}
                        aria-haspopup="dialog"
                        onClick={() => onEdit(id)}
                      >
                        {labels.edit.visible}
                      </button>
                      <ConfirmButton
                        triggerLabel={labels.delete.visible}
                        triggerAriaLabel={labels.delete.accessible}
                        confirmLabel={labels.confirmDelete.visible}
                        confirmAriaLabel={labels.confirmDelete.accessible}
                        dismissAriaLabel={labels.cancelDelete.accessible}
                        className="routes__button routes__button--danger"
                        confirmNotice={ownerNote ?? undefined}
                        busy={deleteBusy}
                        busyLabel={strings.deletingRoute}
                        dialogTitle={strings.deleteRouteDialogTitle}
                        dialogItem={{
                          label: strings.dialogItemRoute,
                          value: strings.routeRowLabel({ id }),
                        }}
                        onConfirm={() => onDelete(id)}
                      />
                    </div>
                    {ownerNote !== null && (
                      <span className="routes__sub admin-table__aux admin-table__note">
                        {ownerInlineNote}
                      </span>
                    )}
                    {rowError !== undefined && (
                      <p role="alert" className="routes__row-error admin-table__aux">
                        {rowError}
                      </p>
                    )}
                  </td>
                )}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

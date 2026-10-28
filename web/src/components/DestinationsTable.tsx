import { Fragment, useEffect, useRef } from 'react';
import type { JSX } from 'react';

import type { DestinationSummary } from '../api/deliveryClient';
import { useCompactTable } from '../hooks/useCompactTable';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { hasUnreadableSecrets } from './destinationFields';
import { formatDateTimeZone } from './formatLocalTime';
import { OwnerBadge } from './OwnerBadge';
import { ownerInlineNoteOf, ownerNoteOf } from './ownership';
import { RowActionsMenu } from './RowActionsMenu';
import type { RowMenuItem } from './RowActionsMenu';
import { destinationActionLabels } from './rowActionLabels';
import { revealFocusedControl } from './adminTableFocus';
import './adminTable.css';
import './DestinationsTable.css';

/** Mutating actions that carry a per-row busy flag. */
export type DestinationAction = 'test' | 'toggle' | 'delete' | 'save' | 'rotate';

/** Outcome of a send-test, shown under the row actions. */
export type TestResult =
  | { kind: 'delivery'; state: string; statusCode: number | null; deadReason: string | null }
  | { kind: 'none' }
  | { kind: 'error'; message: string };

/** A signing secret shown exactly once, keyed by destination name in the owner. */
export interface SecretRevealState {
  secret: string;
  /** RFC3339 expiry of the previous secret after a rotation; null for a fresh add. */
  previousExpiresAt: string | null;
  source: 'add' | 'rotate';
}

interface DestinationsTableProps {
  destinations: DestinationSummary[];
  /** The destination whose edit dialog is open: its reveal renders in the dialog, not the row. */
  editingName: string | null;
  isBusy: (params: { action: DestinationAction; name: string }) => boolean;
  rowErrors: Record<string, string>;
  testResults: Record<string, TestResult>;
  reveals: Record<string, SecretRevealState>;
  /** Registers a focusable control under a key so the owner can move focus to it. */
  registerControl: (params: { key: string; element: HTMLElement | null }) => void;
  /** Opens the edit dialog for the destination. */
  onEdit: (name: string) => void;
  onSendTest: (name: string) => void;
  onSetEnabled: (params: { name: string; enabled: boolean }) => void;
  onDelete: (name: string) => void;
  onDismissReveal: (name: string) => void;
}

// The reveal row spans every column of the layout in use, Actions included:
// Name, Owner, Type, Target, Status, Actions when roomy; the narrow layout drops Owner.
const WIDE_COLUMN_COUNT = 6;
const COMPACT_COLUMN_COUNT = 5;

type DestinationColumn = 'name' | 'owner' | 'type' | 'target' | 'status';

// Roomy layout: the table's natural order.
const WIDE_COLUMNS: DestinationColumn[] = ['name', 'owner', 'type', 'target', 'status'];
// Narrow layout: identity and state first, so the first screen shows what the row is. No Owner
// column here: the owner badge sits under the name in the first cell, in view without scrolling.
const COMPACT_COLUMNS: DestinationColumn[] = ['name', 'status', 'type', 'target'];

function targetOf(destination: DestinationSummary): string {
  if (destination.type === 'ntfy') {
    return strings.destinationTarget({ topic: destination.config.topic ?? '' });
  }
  const key = destination.type === 'discord' ? 'webhook_url' : 'url';
  return destination.secrets[key] ?? '';
}

function stateLabel(state: string): { label: string; modifier: string } {
  switch (state) {
    case 'delivered':
      return { label: strings.testStateDelivered, modifier: 'ok' };
    case 'pending':
      return { label: strings.testStatePending, modifier: 'neutral' };
    case 'dead':
      return { label: strings.testStateDead, modifier: 'fail' };
    default:
      return { label: state, modifier: 'neutral' };
  }
}

/**
 * `underButtons`: the line sits in the Actions cell, under the row's buttons, where it must fit
 * the width the buttons give the cell rather than widen it (see adminTable.css).
 */
function TestResultLine({
  result,
  underButtons,
}: {
  result: TestResult | undefined;
  underButtons: boolean;
}): JSX.Element {
  const aux = underButtons ? ' admin-table__aux' : '';
  let content: JSX.Element | null = null;
  if (result?.kind === 'delivery') {
    const pill = stateLabel(result.state);
    let detail: string | null = null;
    if (result.statusCode !== null) {
      detail = strings.testHttpStatus({ code: result.statusCode });
    } else if (result.state === 'dead') {
      detail = result.deadReason;
    }
    content = (
      <>
        <span className={`destinations__pill destinations__pill--${pill.modifier}`}>
          {pill.label}
        </span>
        {detail !== null && <span>{detail}</span>}
      </>
    );
  } else if (result?.kind === 'none') {
    content = <span>{strings.testNoDelivery}</span>;
  }
  return (
    <>
      {/* Always rendered, text-only changes, so assistive tech announces the result. */}
      <div role="status" aria-live="polite" className={`destinations__result${aux}`}>
        {content}
      </div>
      {result?.kind === 'error' && (
        <p role="alert" className={`destinations__row-error${aux}`}>
          {result.message}
        </p>
      )}
    </>
  );
}

export function RevealBox({
  name,
  reveal,
  onDismiss,
}: {
  name: string;
  reveal: SecretRevealState;
  onDismiss: () => void;
}): JSX.Element {
  const containerRef = useRef<HTMLDivElement | null>(null);

  // The container takes focus whenever a (new) secret appears.
  useEffect(() => {
    containerRef.current?.focus();
  }, [reveal.secret]);

  return (
    <div ref={containerRef} tabIndex={-1} className="destinations__reveal" role="status">
      <p>
        {reveal.source === 'rotate'
          ? strings.revealLeadNew({ name })
          : strings.revealLeadAdded({ name })}
      </p>
      <p className="destinations__secret">{reveal.secret}</p>
      {reveal.previousExpiresAt !== null && (
        <p className="destinations__hint">
          {strings.revealPreviousValidUntil({
            when: formatDateTimeZone({ timestamp: reveal.previousExpiresAt }),
          })}
        </p>
      )}
      <button type="button" className="destinations__button" onClick={onDismiss}>
        {strings.revealDismiss}
      </button>
    </div>
  );
}

export function DestinationsTable({
  destinations,
  editingName,
  isBusy,
  rowErrors,
  testResults,
  reveals,
  registerControl,
  onEdit,
  onSendTest,
  onSetEnabled,
  onDelete,
  onDismissReveal,
}: DestinationsTableProps): JSX.Element {
  const compact = useCompactTable();
  const columns = compact ? COMPACT_COLUMNS : WIDE_COLUMNS;
  const columnCount = compact ? COMPACT_COLUMN_COUNT : WIDE_COLUMN_COUNT;
  const headers: Record<DestinationColumn, JSX.Element> = {
    name: (
      <th key="name" scope="col" className="admin-table__lead">
        {strings.columnDestName}
      </th>
    ),
    owner: (
      <th key="owner" scope="col">
        {strings.columnOwner}
      </th>
    ),
    type: (
      <th key="type" scope="col">
        {strings.columnDestType}
      </th>
    ),
    target: (
      <th key="target" scope="col" className="admin-table__fill">
        {strings.columnDestTarget}
      </th>
    ),
    status: (
      <th key="status" scope="col">
        {strings.columnDestStatus}
      </th>
    ),
  };

  return (
    <div
      className="admin-table__scroll destinations__scroll"
      onFocus={revealFocusedControl}
      role="group"
      aria-labelledby="destinations-heading"
      tabIndex={0}
    >
      <table className="admin-table destinations__table">
        <thead>
          <tr>
            {columns.map((column) => headers[column])}
            <th
              scope="col"
              className={`admin-table__actions${compact ? ' admin-table__kebab' : ''}`}
              aria-label={strings.columnDestActions}
            />
          </tr>
        </thead>
        <tbody>
          {destinations.map((destination) => {
            const { name } = destination;
            const unreadable = hasUnreadableSecrets(destination.secrets);
            const isEditing = editingName === name;
            const reveal = reveals[name];
            const reasonId = `dest-reason-${name}`;
            const testBusy = isBusy({ action: 'test', name });
            const testBlocked = !destination.enabled || testBusy;
            const toggleBusy = isBusy({ action: 'toggle', name });
            const deleteBusy = isBusy({ action: 'delete', name });
            const rowError = rowErrors[name];
            const ownerNote = ownerNoteOf(destination);
            const ownerInlineNote = ownerInlineNoteOf(destination);

            // The visible text is the bare verb (the row names the destination); the accessible
            // name keeps the destination. A busy action shows its progress text for both.
            const labels = destinationActionLabels({ name });

            const menuItems: RowMenuItem[] = [
              {
                id: 'test',
                label: testBusy ? strings.sendTestToSending : labels.test.visible,
                ariaLabel: testBusy ? undefined : labels.test.accessible,
                disabled: testBlocked,
                onSelect: () => onSendTest(name),
              },
            ];
            if (!unreadable) {
              menuItems.push({
                id: 'edit',
                label: labels.edit.visible,
                ariaLabel: labels.edit.accessible,
                onSelect: () => onEdit(name),
              });
            }
            if (destination.enabled) {
              menuItems.push({
                id: 'disable',
                label: toggleBusy ? strings.disablingDestination : labels.disable.visible,
                ariaLabel: toggleBusy ? undefined : labels.disable.accessible,
                tone: 'danger',
                disabled: toggleBusy,
                confirm: {
                  confirmLabel: labels.confirmDisable.visible,
                  confirmAriaLabel: labels.confirmDisable.accessible,
                  cancelLabel: labels.cancelDisable.visible,
                  cancelAriaLabel: labels.cancelDisable.accessible,
                  notice: ownerNote ?? undefined,
                  dialogTitle: strings.disableDestinationDialogTitle,
                  dialogItem: { label: strings.dialogItemDestination, value: name },
                  onConfirm: () => onSetEnabled({ name, enabled: false }),
                },
              });
            } else {
              menuItems.push({
                id: 'enable',
                label: toggleBusy ? strings.enablingDestination : labels.enable.visible,
                ariaLabel: toggleBusy ? undefined : labels.enable.accessible,
                disabled: toggleBusy,
                onSelect: () => onSetEnabled({ name, enabled: true }),
              });
            }
            menuItems.push({
              id: 'delete',
              label: deleteBusy ? strings.deletingDestination : labels.delete.visible,
              ariaLabel: deleteBusy ? undefined : labels.delete.accessible,
              tone: 'danger',
              disabled: deleteBusy,
              confirm: {
                confirmLabel: labels.confirmDelete.visible,
                confirmAriaLabel: labels.confirmDelete.accessible,
                cancelLabel: labels.cancelDelete.visible,
                cancelAriaLabel: labels.cancelDelete.accessible,
                notice: ownerNote ?? undefined,
                dialogTitle: strings.deleteDestinationDialogTitle,
                dialogItem: { label: strings.dialogItemDestination, value: name },
                onConfirm: () => onDelete(name),
              },
            });

            const cells: Record<DestinationColumn, JSX.Element> = {
              name: (
                <td key="name" className="admin-table__lead destinations__name" title={name}>
                  {name}
                  {compact && (
                    <span className="admin-table__owner-line">
                      <OwnerBadge
                        ownerKind={destination.ownerKind}
                        ownerAppId={destination.ownerAppId}
                      />
                    </span>
                  )}
                </td>
              ),
              owner: (
                <td key="owner">
                  <OwnerBadge
                    ownerKind={destination.ownerKind}
                    ownerAppId={destination.ownerAppId}
                  />
                </td>
              ),
              type: <td key="type">{strings.destinationTypeLabels[destination.type]}</td>,
              target: (
                <td key="target" className="destinations__target admin-table__fill">
                  {unreadable ? (
                    <>
                      <span className="destinations__pill destinations__pill--fail">
                        {strings.destinationSecretsUnreadable}
                      </span>
                      <span className="destinations__sub">
                        {strings.destinationSecretsUnreadableHint}
                      </span>
                    </>
                  ) : (
                    targetOf(destination)
                  )}
                </td>
              ),
              status: (
                <td key="status">
                  <span
                    className={`destinations__pill destinations__pill--${destination.enabled ? 'ok' : 'fail'}`}
                  >
                    {destination.enabled ? strings.destinationEnabled : strings.destinationDisabled}
                  </span>
                  {!destination.enabled && (
                    <span id={reasonId} className="destinations__sub">
                      {destination.disabledReason ?? strings.destinationDisabledNoReason}
                    </span>
                  )}
                  {/* The narrow layout has no room for the result under the buttons, so it
                      reads under the row's state. */}
                  {compact && <TestResultLine result={testResults[name]} underButtons={false} />}
                </td>
              ),
            };

            return (
              <Fragment key={name}>
                <tr>
                  {columns.map((column) => cells[column])}
                  {compact ? (
                    <td className="admin-table__actions admin-table__kebab">
                      <RowActionsMenu
                        rowLabel={name}
                        items={menuItems}
                        note={ownerInlineNote}
                        errors={rowError === undefined ? undefined : [rowError]}
                        focusKeys={[`first:${name}`, `edit:${name}`, `enable:${name}`]}
                        registerControl={registerControl}
                      />
                    </td>
                  ) : (
                    <td className="admin-table__actions">
                      <div className="destinations__actions">
                        <button
                          ref={(element) => registerControl({ key: `first:${name}`, element })}
                          type="button"
                          className="destinations__button"
                          aria-label={testBusy ? undefined : labels.test.accessible}
                          aria-disabled={testBlocked ? 'true' : undefined}
                          aria-describedby={!destination.enabled ? reasonId : undefined}
                          onClick={() => {
                            if (testBlocked) {
                              return;
                            }
                            onSendTest(name);
                          }}
                        >
                          {testBusy ? strings.sendTestToSending : labels.test.visible}
                        </button>
                        {!unreadable && (
                          <button
                            ref={(element) => registerControl({ key: `edit:${name}`, element })}
                            type="button"
                            className="destinations__button"
                            aria-label={labels.edit.accessible}
                            aria-haspopup="dialog"
                            onClick={() => onEdit(name)}
                          >
                            {labels.edit.visible}
                          </button>
                        )}
                        {destination.enabled ? (
                          <ConfirmButton
                            triggerLabel={labels.disable.visible}
                            triggerAriaLabel={labels.disable.accessible}
                            confirmLabel={labels.confirmDisable.visible}
                            confirmAriaLabel={labels.confirmDisable.accessible}
                            dismissAriaLabel={labels.cancelDisable.accessible}
                            className="destinations__button destinations__button--danger"
                            confirmNotice={ownerNote ?? undefined}
                            busy={toggleBusy}
                            busyLabel={strings.disablingDestination}
                            dialogTitle={strings.disableDestinationDialogTitle}
                            dialogItem={{ label: strings.dialogItemDestination, value: name }}
                            onConfirm={() => onSetEnabled({ name, enabled: false })}
                          />
                        ) : (
                          <button
                            ref={(element) => registerControl({ key: `enable:${name}`, element })}
                            type="button"
                            className="destinations__button"
                            aria-label={toggleBusy ? undefined : labels.enable.accessible}
                            aria-disabled={toggleBusy ? 'true' : undefined}
                            onClick={() => {
                              if (toggleBusy) {
                                return;
                              }
                              onSetEnabled({ name, enabled: true });
                            }}
                          >
                            {toggleBusy ? strings.enablingDestination : labels.enable.visible}
                          </button>
                        )}
                        <ConfirmButton
                          triggerLabel={labels.delete.visible}
                          triggerAriaLabel={labels.delete.accessible}
                          confirmLabel={labels.confirmDelete.visible}
                          confirmAriaLabel={labels.confirmDelete.accessible}
                          dismissAriaLabel={labels.cancelDelete.accessible}
                          className="destinations__button destinations__button--danger"
                          confirmNotice={ownerNote ?? undefined}
                          busy={deleteBusy}
                          busyLabel={strings.deletingDestination}
                          dialogTitle={strings.deleteDestinationDialogTitle}
                          dialogItem={{ label: strings.dialogItemDestination, value: name }}
                          onConfirm={() => onDelete(name)}
                        />
                      </div>
                      {ownerNote !== null && (
                        <span className="destinations__sub admin-table__aux admin-table__note">
                          {ownerInlineNote}
                        </span>
                      )}
                      <TestResultLine result={testResults[name]} underButtons />
                      {rowError !== undefined && (
                        <p role="alert" className="destinations__row-error admin-table__aux">
                          {rowError}
                        </p>
                      )}
                    </td>
                  )}
                </tr>
                {reveal !== undefined && !isEditing && (
                  <tr>
                    <td colSpan={columnCount} className="destinations__reveal-cell">
                      <div className="destinations__reveal-row">
                        <RevealBox
                          name={name}
                          reveal={reveal}
                          onDismiss={() => onDismissReveal(name)}
                        />
                      </div>
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

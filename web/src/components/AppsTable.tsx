import { useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import type { ProvisionAppResult } from '../api/client';
import type { AppSummary, UpdateAppParams } from '../api/personClient';
import { useCompactTable } from '../hooks/useCompactTable';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { strings } from '../strings';
import { revealFocusedControl } from './adminTableFocus';
import { ConfirmButton } from './ConfirmButton';
import { EditAppForm } from './EditAppForm';
import { EditModal } from './EditModal';
import type { ConfirmStep } from './EditModal';
import { LocalTime } from './LocalTime';
import { RowActionsMenu } from './RowActionsMenu';
import './adminTable.css';
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

// Stamped on the edit dialog so a focused element inside it resolves to its app row.
const ROW_ID_ATTRIBUTE = 'data-app-id';

export function AppsTable({
  apps,
  subscriberCountByApp,
  loading,
  error,
  onUpdateApp,
  onRemintToken,
  onRemoveApp,
}: AppsTableProps): JSX.Element {
  const compact = useCompactTable();
  const [editingAppId, setEditingAppId] = useState<string | null>(null);
  // The edited app as it was when the dialog opened: titles the dialog and renders the form
  // after a refresh drops the row.
  const [editSnapshot, setEditSnapshot] = useState<AppSummary | null>(null);
  const [formDirty, setFormDirty] = useState(false);
  const [formBusy, setFormBusy] = useState(false);
  const [tokenShown, setTokenShown] = useState(false);
  const [confirmStep, setConfirmStep] = useState<ConfirmStep | null>(null);
  const [result, setResult] = useState<string | null>(null);
  const [pendingResult, setPendingResult] = useState<string | null>(null);
  // Mirror state for async callbacks that outlive the render they started in.
  const editingAppIdRef = useRef<string | null>(null);
  const tokenShownRef = useRef(false);
  const editReturnRef = useRef<HTMLElement | null>(null);
  const firstEditFieldRef = useRef<HTMLInputElement | null>(null);
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  const { registerControl, getControl } = useFocusRegistry();

  // Announces a save once its dialog has closed. This must be a post-commit effect: Modal lifts
  // `inert` from `#root` in its unmount cleanup, and text written into an inert live region in
  // the same commit would never be announced. The copy and the clear are one step.
  useEffect(() => {
    if (editingAppId === null && pendingResult !== null) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- deliberate post-close hand-off
      setResult(pendingResult);
      setPendingResult(null);
    }
  }, [editingAppId, pendingResult]);

  /** Opens the editor for `appId` (or closes it with null) and resets every per-dialog flag. */
  function setEditor(appId: string | null) {
    editingAppIdRef.current = appId;
    tokenShownRef.current = false;
    setEditingAppId(appId);
    setFormDirty(false);
    setFormBusy(false);
    setTokenShown(false);
    setConfirmStep(null);
    if (appId === null) {
      setEditSnapshot(null);
    } else {
      setResult(null);
    }
  }

  function openEditor(app: AppSummary) {
    editReturnRef.current = getControl(`edit:${app.appId}`);
    setEditor(app.appId);
    setEditSnapshot(app);
  }

  async function handleUpdate(params: UpdateAppParams): Promise<void> {
    await onUpdateApp(params);
    // Close and announce only the dialog this save came from, and only with no token on screen:
    // a shown token keeps the dialog open and the form announces inside it.
    if (editingAppIdRef.current === params.appId && !tokenShownRef.current) {
      setEditor(null);
      setPendingResult(strings.appSavedResult({ appId: params.appId }));
    }
  }

  function handleTokenShownChange(shown: boolean) {
    tokenShownRef.current = shown;
    setTokenShown(shown);
  }

  function handleRequestConfirm(step: ConfirmStep) {
    setConfirmStep({
      ...step,
      onConfirm: () => {
        setConfirmStep(null);
        step.onConfirm();
      },
      onCancel: () => {
        setConfirmStep(null);
        step.onCancel();
      },
    });
  }

  const liveEditing = apps.find((candidate) => candidate.appId === editingAppId);
  const editingApp = editingAppId === null ? null : (liveEditing ?? editSnapshot);
  // Only a successful list can say the row is gone: a failed reload may empty `apps` too.
  const rowGone =
    !loading &&
    error === null &&
    editingAppId !== null &&
    liveEditing === undefined &&
    editSnapshot !== null;

  return (
    <section className="apps-table">
      <h2 tabIndex={-1} ref={headingRef}>
        {strings.appsHeading}
      </h2>
      <p role="status" className="apps-table__status">
        {result}
      </p>
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
        <div
          className="admin-table__scroll apps-table__scroll"
          onFocus={revealFocusedControl}
          role="group"
          aria-label={strings.appsHeading}
          tabIndex={0}
        >
          <table className="admin-table">
            <thead>
              <tr>
                {compact ? (
                  <>
                    <th scope="col" className="admin-table__lead">
                      {strings.columnApp}
                    </th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnCreated}
                    </th>
                  </>
                ) : (
                  <>
                    <th scope="col" className="admin-table__lead">
                      {strings.columnAppName}
                    </th>
                    <th scope="col">{strings.columnAppId}</th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnAppDescription}
                    </th>
                    <th scope="col">{strings.columnSubscribers}</th>
                    <th scope="col">{strings.columnCreated}</th>
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
              {apps.map((app) => {
                const editLabel = `${strings.editAction} ${app.displayName}`;
                const removeLabel = `${strings.removeAction} ${app.displayName}`;
                const confirmRemoveLabel = `${strings.confirmRemoveAction} ${app.displayName}`;
                const cancelRemoveLabel = strings.cancelActionNamed({ subject: removeLabel });
                return (
                  <tr key={app.appId}>
                    {compact ? (
                      <>
                        <td className="admin-table__lead apps-table__lead" title={app.appId}>
                          <span className="apps-table__id">{app.appId}</span>
                          <span className="admin-table__sub">{app.displayName}</span>
                        </td>
                        <td className="admin-table__fill">
                          <LocalTime timestamp={app.createdAt} />
                        </td>
                        <td className="admin-table__actions admin-table__kebab">
                          <RowActionsMenu
                            rowLabel={app.displayName}
                            focusKeys={[`edit:${app.appId}`]}
                            registerControl={registerControl}
                            items={[
                              {
                                id: 'edit',
                                label: strings.editAction,
                                ariaLabel: editLabel,
                                onSelect: () => openEditor(app),
                              },
                              {
                                id: 'remove',
                                label: strings.removeAction,
                                ariaLabel: removeLabel,
                                tone: 'danger',
                                confirm: {
                                  confirmLabel: strings.confirmRemoveAction,
                                  confirmAriaLabel: confirmRemoveLabel,
                                  cancelLabel: strings.cancelAction,
                                  cancelAriaLabel: cancelRemoveLabel,
                                  notice: strings.removeAppNotice,
                                  dialogTitle: strings.removeAppDialogTitle,
                                  dialogItem: {
                                    label: strings.dialogItemApp,
                                    value: app.displayName,
                                  },
                                  onConfirm: () => onRemoveApp({ appId: app.appId }),
                                },
                              },
                            ]}
                          />
                        </td>
                      </>
                    ) : (
                      <>
                        <td className="admin-table__lead">{app.displayName}</td>
                        <td className="apps-table__id">{app.appId}</td>
                        <td className="admin-table__fill">{app.description ?? ''}</td>
                        <td>{subscriberCountByApp.get(app.appId) ?? 0}</td>
                        <td>
                          <LocalTime timestamp={app.createdAt} />
                        </td>
                        <td className="admin-table__actions">
                          <div className="apps-table__actions">
                            <button
                              ref={(element) =>
                                registerControl({ key: `edit:${app.appId}`, element })
                              }
                              type="button"
                              className="apps-table__button apps-table__button--secondary"
                              aria-haspopup="dialog"
                              onClick={() => openEditor(app)}
                            >
                              {editLabel}
                            </button>
                            <ConfirmButton
                              triggerLabel={removeLabel}
                              confirmLabel={confirmRemoveLabel}
                              dismissAriaLabel={cancelRemoveLabel}
                              dialogTitle={strings.removeAppDialogTitle}
                              dialogItem={{ label: strings.dialogItemApp, value: app.displayName }}
                              confirmNotice={strings.removeAppNotice}
                              className="apps-table__button apps-table__button--danger"
                              onConfirm={() => onRemoveApp({ appId: app.appId })}
                            />
                          </div>
                        </td>
                      </>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <EditModal
        open={editingAppId !== null}
        title={editingApp === null ? '' : strings.editAppNamed({ appId: editingApp.appId })}
        onClose={() => setEditor(null)}
        dirty={formDirty || tokenShown}
        busy={formBusy}
        guardMessage={tokenShown ? strings.tokenShownOnceGuard : undefined}
        discardLabel={tokenShown ? strings.closeAnyway : undefined}
        keepLabel={tokenShown ? strings.keepOpen : undefined}
        returnFocusRef={editReturnRef}
        fallbackFocusRef={headingRef}
        initialFocusRef={firstEditFieldRef}
        confirmStep={confirmStep}
        rowGone={rowGone}
        ownerRow={
          editingAppId === null ? undefined : { attribute: ROW_ID_ATTRIBUTE, id: editingAppId }
        }
      >
        {({ requestClose }) =>
          editingApp === null ? null : (
            <EditAppForm
              key={editingApp.appId}
              app={editingApp}
              onUpdateApp={handleUpdate}
              onRemintToken={onRemintToken}
              onClose={requestClose}
              onRequestConfirm={handleRequestConfirm}
              onDirtyChange={setFormDirty}
              onBusyChange={setFormBusy}
              onTokenShownChange={handleTokenShownChange}
              firstFieldRef={firstEditFieldRef}
            />
          )
        }
      </EditModal>
    </section>
  );
}

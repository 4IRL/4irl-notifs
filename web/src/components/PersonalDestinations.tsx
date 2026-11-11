import { useCallback, useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import { ApiError } from '../api/client';
import type {
  DeliveryApiClient,
  DestinationType,
  PersonalDestinationSummary,
  PersonalDisabledBy,
} from '../api/deliveryClient';
import { useCompactTable } from '../hooks/useCompactTable';
import { useExclusive } from '../hooks/useExclusive';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { revealFocusedControl } from './adminTableFocus';
import { ConfirmButton } from './ConfirmButton';
import { LocalTime } from './LocalTime';
import { RefreshButton } from './RefreshButton';
import { RowActionsMenu } from './RowActionsMenu';
import type { RowMenuItem } from './RowActionsMenu';
import { messageOf, withoutKey } from './rowState';
import './adminTable.css';
import './OwnerBadge.css';
import './PersonalDestinations.css';
import './visuallyHidden.css';

interface PersonalDestinationsProps {
  client: DeliveryApiClient;
  /** Bumped by the owner when the list may have changed server-side (an app or person was removed); a change reloads quietly. */
  refreshSignal?: number;
}

const HEADING_KEY = 'personal-destinations-heading';
// Stamped on each row (as `data-personal-id`) so a focused element resolves to the row it sits in.
const ROW_ATTRIBUTE = 'data-personal-id';

// deliveryClient exports these only as string-literal types, so the runtime values are typed against them.
const DISCORD_TYPE: DestinationType = 'discord';
const DISCORD_SECRET_KEY = 'webhook_url';
const WEBHOOK_SECRET_KEY = 'url';
const DISABLED_BY_OPERATOR: PersonalDisabledBy = 'operator';
const DISABLED_BY_SYSTEM: PersonalDisabledBy = 'system';
const DISABLED_BY_APP: PersonalDisabledBy = 'app';

/** The accessible row name: "<label> of <hash>". */
function rowNameOf(row: PersonalDestinationSummary): string {
  return strings.personalRowName({ label: row.label, hash: row.personHash });
}

/** The masked endpoint URL: a Discord webhook URL or a webhook URL. */
function targetOf(row: PersonalDestinationSummary): string {
  const key = row.type === DISCORD_TYPE ? DISCORD_SECRET_KEY : WEBHOOK_SECRET_KEY;
  return row.secrets[key] ?? '';
}

/** The line under a Disabled pill: the stored reason, else a fallback by who disabled it. */
function disabledReasonOf(row: PersonalDestinationSummary): string {
  if (row.disabledReason !== null && row.disabledReason !== '') {
    return row.disabledReason;
  }
  switch (row.disabledBy) {
    case DISABLED_BY_OPERATOR:
      return strings.destinationDisabledNoReason;
    case DISABLED_BY_SYSTEM:
      return strings.personalDisabledBySystem;
    case DISABLED_BY_APP:
      return strings.personalDisabledByApp;
    default:
      // A disabler this build does not know is never read as an operator's.
      return strings.personalDisabledUnknown;
  }
}

function deleteNoticeOf(row: PersonalDestinationSummary): string {
  return row.optedInAppIds.length === 0
    ? strings.deletePersonalNoAppsNotice
    : strings.deletePersonalNotice({ apps: row.optedInAppIds });
}

function dialogItemOf(row: PersonalDestinationSummary): { label: string; value: string } {
  return {
    label: strings.dialogItemPersonalDestination,
    value: strings.personalDialogItem({ label: row.label, id: row.id, hash: row.personHash }),
  };
}

function AppBadge({ appId, hidden }: { appId: string; hidden: boolean }): JSX.Element {
  return (
    <span className="owner-badge owner-badge--app">
      {hidden && <span className="visually-hidden">{strings.personalRegisteredByPrefix}</span>}
      {appId}
    </span>
  );
}

function RegisteredBy({ row }: { row: PersonalDestinationSummary }): JSX.Element {
  if (row.registrarRemoved) {
    return (
      <>
        <span className="personal__pill personal__pill--neutral">{strings.personalAppRemoved}</span>
        <span className="personal__sub">{row.registeredByAppId}</span>
      </>
    );
  }
  return <AppBadge appId={row.registeredByAppId} hidden />;
}

function OptedInFor({ row }: { row: PersonalDestinationSummary }): JSX.Element {
  if (row.optedInAppIds.length === 0) {
    return <span className="personal__pill personal__pill--neutral">{strings.personalNoApps}</span>;
  }
  return (
    <span className="personal__chips">
      {row.optedInAppIds.map((appId) => (
        <AppBadge key={appId} appId={appId} hidden={false} />
      ))}
    </span>
  );
}

function StatusPill({ row }: { row: PersonalDestinationSummary }): JSX.Element {
  return (
    <span className={`personal__pill personal__pill--${row.enabled ? 'ok' : 'fail'}`}>
      {row.enabled ? strings.destinationEnabled : strings.destinationDisabled}
    </span>
  );
}

function StatusCell({ row }: { row: PersonalDestinationSummary }): JSX.Element {
  return (
    <>
      <StatusPill row={row} />
      {!row.enabled && <span className="personal__sub">{disabledReasonOf(row)}</span>}
    </>
  );
}

/** The revealed part of a compact row: everything the collapsed two lines leave out. */
function CompactDetails({
  row,
  detailsId,
  open,
}: {
  row: PersonalDestinationSummary;
  detailsId: string;
  open: boolean;
}): JSX.Element {
  return (
    <dl id={detailsId} hidden={!open} className="personal__details">
      <div className="personal__field">
        <dt>{strings.columnPersonalTarget}</dt>
        <dd className="personal__target">{targetOf(row)}</dd>
      </div>
      <div className="personal__field">
        <dt>{strings.columnPersonalType}</dt>
        <dd>{strings.destinationTypeLabels[row.type]}</dd>
      </div>
      <div className="personal__field">
        {/* The badge carries its own hidden "Registered by: " prefix, so this label is for sight
            only; the App removed pill has no prefix, so there the label is read. */}
        <dt aria-hidden={row.registrarRemoved ? undefined : 'true'}>
          {strings.columnPersonalRegisteredBy}
        </dt>
        <dd>
          <RegisteredBy row={row} />
        </dd>
      </div>
      <div className="personal__field">
        <dt>{strings.columnPersonalOptedIn}</dt>
        <dd>
          <OptedInFor row={row} />
        </dd>
      </div>
      {!row.enabled && (
        <div className="personal__field">
          <dt>{strings.columnPersonalStatus}</dt>
          <dd>{disabledReasonOf(row)}</dd>
        </div>
      )}
    </dl>
  );
}

/**
 * The Personal destinations section of the Delivery tab: the endpoints people registered
 * through an app, listed by person hash, which the operator can disable, enable or delete.
 * The list stays in this component (never in the shared destinations state), so Routes and
 * Heartbeats never offer a personal row. Reloads after an action are quiet (no loading state),
 * so open dialogs, row errors and focus survive them.
 */
export function PersonalDestinations({
  client,
  refreshSignal,
}: PersonalDestinationsProps): JSX.Element {
  const [rows, setRows] = useState<PersonalDestinationSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const [result, setResult] = useState<string | null>(null);
  // Compact layout only: which rows are expanded, by destination id.
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const compact = useCompactTable();
  const rowsRef = useRef<PersonalDestinationSummary[]>([]);
  const loadingRef = useRef(true);
  const { registerControl, requestFocus } = useFocusRegistry();
  const { isBusy, exclusive } = useExclusive<string>();
  const trackList = useLatestRequest();

  const commitLoading = useCallback((next: boolean) => {
    loadingRef.current = next;
    setLoading(next);
  }, []);

  const load = useCallback(
    ({ quiet }: { quiet: boolean }): Promise<void> =>
      trackList(client.listPersonalDestinations()).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        // A quiet reload that superseded the initial or Retry load must still settle the
        // loading state, so it only stays quiet otherwise.
        const stayQuiet = quiet && !loadingRef.current;
        if (outcome.ok) {
          const { personalDestinations, total: nextTotal } = outcome.value;
          rowsRef.current = personalDestinations;
          setRows(personalDestinations);
          setTotal(nextTotal);
          setError(null);
          // A row error stays only while its row is still listed.
          const present = new Set(personalDestinations.map((row) => String(row.id)));
          setRowErrors((current) =>
            Object.fromEntries(Object.entries(current).filter(([key]) => present.has(key))),
          );
          // Expansion likewise survives only for rows that are still listed.
          setExpanded((current) =>
            Object.fromEntries(Object.entries(current).filter(([key]) => present.has(key))),
          );
        } else if (!stayQuiet) {
          rowsRef.current = [];
          setRows([]);
          setTotal(0);
          setError(
            messageOf({ rejection: outcome.error, fallback: strings.personalLoadFailedFallback }),
          );
        } else {
          return;
        }
        commitLoading(false);
      }),
    [client, trackList, commitLoading],
  );

  // Initial load.
  useEffect(() => {
    void load({ quiet: false });
  }, [load]);

  // A refresh signal change after mount reloads quietly; the mount value triggers nothing extra.
  const lastRefreshSignalRef = useRef(refreshSignal);
  useEffect(() => {
    if (lastRefreshSignalRef.current === refreshSignal) {
      return;
    }
    lastRefreshSignalRef.current = refreshSignal;
    void load({ quiet: true });
  }, [refreshSignal, load]);

  function startRefresh() {
    setResult(null);
    commitLoading(true);
    void load({ quiet: false });
  }

  function failRow({ id, message }: { id: number; message: string }) {
    setRowErrors((current) => ({ ...current, [String(id)]: message }));
  }

  function toggleExpanded(id: number) {
    setExpanded((current) => ({ ...current, [String(id)]: current[String(id)] !== true }));
  }

  function clearRowError(id: number) {
    setRowErrors((current) => withoutKey({ record: current, key: String(id) }));
  }

  /** Hands focus on only when it was inside this row (or lost to <body>), so a concurrent action cannot steal it. */
  function requestFocusIfRowOwnsIt({ id, key }: { id: number; key: string }) {
    const focused = document.activeElement;
    const owner = focused?.closest(`[${ROW_ATTRIBUTE}]`)?.getAttribute(ROW_ATTRIBUTE) ?? null;
    if (focused === null || focused === document.body || owner === String(id)) {
      requestFocus(key);
    }
  }

  /** Where focus goes when `id` leaves the list: the next row's first control, else the heading. */
  function neighbourFocusKey(id: number): string {
    const latest = rowsRef.current;
    const index = latest.findIndex((candidate) => candidate.id === id);
    const neighbour = index === -1 ? undefined : (latest[index + 1] ?? latest[index - 1]);
    return neighbour === undefined ? HEADING_KEY : `first:${neighbour.id}`;
  }

  /** The wanted focus key, or the heading when that neighbour row is no longer listed. */
  function mountedFocusKey(key: string): string {
    // rowsRef is updated synchronously by the load, ahead of the render that unmounts a row.
    const listed = rowsRef.current.some((candidate) => key === `first:${candidate.id}`);
    return listed || !key.startsWith('first:') ? key : HEADING_KEY;
  }

  /** A 404 means the row is already gone: announce it, reload, then move focus per the delete rule. */
  function handleGone({ row, focusKey }: { row: PersonalDestinationSummary; focusKey: string }) {
    clearRowError(row.id);
    setResult(strings.personalGone({ label: row.label, id: row.id }));
    // The reload may also remove the neighbour, so the key is checked after it lands.
    void load({ quiet: true }).then(() => {
      requestFocusIfRowOwnsIt({ id: row.id, key: mountedFocusKey(focusKey) });
    });
  }

  function handleSetEnabled({
    row,
    enabled,
  }: {
    row: PersonalDestinationSummary;
    enabled: boolean;
  }) {
    const { id } = row;
    clearRowError(id);
    setResult(null);
    const focusKeyIfGone = neighbourFocusKey(id);
    exclusive({
      key: `toggle:${id}`,
      work: async () => {
        try {
          await client.setPersonalDestinationEnabled({ id, enabled });
          setResult(
            enabled
              ? strings.personalEnabledResult({ label: row.label, id })
              : strings.personalDisabledResult({ label: row.label, id }),
          );
          await load({ quiet: true });
          // Disable keeps the row, so land on its Enable button; Enable swaps that button
          // away, so land on the row's first control.
          requestFocusIfRowOwnsIt({ id, key: enabled ? `first:${id}` : `enable:${id}` });
        } catch (rejection: unknown) {
          if (rejection instanceof ApiError && rejection.status === 404) {
            handleGone({ row, focusKey: focusKeyIfGone });
            return;
          }
          failRow({
            id,
            message: enabled ? strings.personalEnableFailed : strings.personalDisableFailed,
          });
          requestFocusIfRowOwnsIt({ id, key: enabled ? `enable:${id}` : `disable:${id}` });
          void load({ quiet: true });
        }
      },
    }).catch(() => undefined);
  }

  function handleDelete(row: PersonalDestinationSummary) {
    const { id } = row;
    clearRowError(id);
    setResult(null);
    const focusKey = neighbourFocusKey(id);
    exclusive({
      key: `delete:${id}`,
      work: async () => {
        try {
          await client.deletePersonalDestination({ id });
          setResult(strings.personalDeletedResult({ label: row.label, id }));
          await load({ quiet: true });
          requestFocusIfRowOwnsIt({ id, key: mountedFocusKey(focusKey) });
        } catch (rejection: unknown) {
          if (rejection instanceof ApiError && rejection.status === 404) {
            handleGone({ row, focusKey });
            return;
          }
          failRow({ id, message: strings.personalDeleteFailed });
          requestFocusIfRowOwnsIt({ id, key: `delete:${id}` });
          void load({ quiet: true });
        }
      },
    }).catch(() => undefined);
  }

  function renderActions({
    row,
    rowBusy,
  }: {
    row: PersonalDestinationSummary;
    rowBusy: boolean;
  }): JSX.Element {
    const { id } = row;
    const name = rowNameOf(row);
    const toggling = isBusy(`toggle:${id}`);
    const deleting = isBusy(`delete:${id}`);
    const deleteName = strings.deletePersonal({ row: name });
    const disableName = strings.disablePersonal({ row: name });
    return (
      <div className="personal__actions">
        {row.enabled ? (
          <ConfirmButton
            triggerLabel={strings.rowActionDisable}
            triggerAriaLabel={disableName}
            confirmLabel={strings.rowActionConfirmDisable}
            confirmAriaLabel={strings.confirmDisablePersonal({ row: name })}
            dismissAriaLabel={strings.cancelActionNamed({ subject: disableName })}
            className="personal__button personal__button--danger"
            confirmNotice={strings.disablePersonalNotice}
            busy={rowBusy}
            busyLabel={toggling ? strings.disablingDestination : strings.rowActionDisable}
            busyAriaLabel={disableName}
            dialogTitle={strings.disablePersonalDialogTitle}
            dialogItem={dialogItemOf(row)}
            registerTrigger={(element) => {
              registerControl({ key: `first:${id}`, element });
              registerControl({ key: `disable:${id}`, element });
            }}
            onConfirm={() => handleSetEnabled({ row, enabled: false })}
          />
        ) : (
          <button
            ref={(element) => {
              registerControl({ key: `first:${id}`, element });
              registerControl({ key: `enable:${id}`, element });
            }}
            type="button"
            className="personal__button"
            aria-label={strings.enablePersonal({ row: name })}
            aria-disabled={rowBusy ? 'true' : undefined}
            onClick={() => {
              if (rowBusy) {
                return;
              }
              handleSetEnabled({ row, enabled: true });
            }}
          >
            {toggling ? strings.enablingDestination : strings.rowActionEnable}
          </button>
        )}
        <ConfirmButton
          triggerLabel={strings.rowActionDelete}
          triggerAriaLabel={deleteName}
          confirmLabel={strings.rowActionConfirmDelete}
          confirmAriaLabel={strings.confirmDeletePersonal({ row: name })}
          dismissAriaLabel={strings.cancelActionNamed({ subject: deleteName })}
          className="personal__button personal__button--danger"
          confirmNotice={deleteNoticeOf(row)}
          busy={rowBusy}
          busyLabel={deleting ? strings.deletingDestination : strings.rowActionDelete}
          busyAriaLabel={deleteName}
          dialogTitle={strings.deletePersonalDialogTitle}
          dialogItem={dialogItemOf(row)}
          registerTrigger={(element) => registerControl({ key: `delete:${id}`, element })}
          onConfirm={() => handleDelete(row)}
        />
      </div>
    );
  }

  function menuItemsOf({
    row,
    rowBusy,
  }: {
    row: PersonalDestinationSummary;
    rowBusy: boolean;
  }): RowMenuItem[] {
    const name = rowNameOf(row);
    const toggling = isBusy(`toggle:${row.id}`);
    const deleting = isBusy(`delete:${row.id}`);
    const disableName = strings.disablePersonal({ row: name });
    const deleteName = strings.deletePersonal({ row: name });
    const toggleItem: RowMenuItem = row.enabled
      ? {
          id: 'disable',
          label: toggling ? strings.disablingDestination : strings.rowActionDisable,
          ariaLabel: toggling ? undefined : disableName,
          tone: 'danger',
          disabled: rowBusy,
          confirm: {
            confirmLabel: strings.rowActionConfirmDisable,
            confirmAriaLabel: strings.confirmDisablePersonal({ row: name }),
            cancelLabel: strings.cancelAction,
            cancelAriaLabel: strings.cancelActionNamed({ subject: disableName }),
            notice: strings.disablePersonalNotice,
            dialogTitle: strings.disablePersonalDialogTitle,
            dialogItem: dialogItemOf(row),
            onConfirm: () => handleSetEnabled({ row, enabled: false }),
          },
        }
      : {
          id: 'enable',
          label: toggling ? strings.enablingDestination : strings.rowActionEnable,
          ariaLabel: toggling ? undefined : strings.enablePersonal({ row: name }),
          disabled: rowBusy,
          onSelect: () => handleSetEnabled({ row, enabled: true }),
        };
    return [
      toggleItem,
      {
        id: 'delete',
        label: deleting ? strings.deletingDestination : strings.rowActionDelete,
        ariaLabel: deleting ? undefined : deleteName,
        tone: 'danger',
        disabled: rowBusy,
        confirm: {
          confirmLabel: strings.rowActionConfirmDelete,
          confirmAriaLabel: strings.confirmDeletePersonal({ row: name }),
          cancelLabel: strings.cancelAction,
          cancelAriaLabel: strings.cancelActionNamed({ subject: deleteName }),
          notice: deleteNoticeOf(row),
          dialogTitle: strings.deletePersonalDialogTitle,
          dialogItem: dialogItemOf(row),
          onConfirm: () => handleDelete(row),
        },
      },
    ];
  }

  const showContent = !loading && error === null;
  const disabledCount = rows.filter((row) => !row.enabled).length;
  const truncated = total > rows.length;
  let summary = '';
  if (showContent) {
    summary =
      total === 0
        ? strings.personalSummaryEmpty
        : strings.personalSummary({ total, disabled: disabledCount, shown: rows.length });
  }

  return (
    <section className="personal" aria-labelledby="personal-destinations-heading">
      <div className="personal__head">
        <h2
          id="personal-destinations-heading"
          tabIndex={-1}
          ref={(element) => registerControl({ key: HEADING_KEY, element })}
        >
          {strings.personalHeading}
        </h2>
        <RefreshButton
          label={strings.personalRefresh}
          className="personal__button personal__button--ghost"
          busy={loading}
          onClick={startRefresh}
        />
      </div>
      <p className="personal__hint">{strings.personalHint}</p>
      {/* Both live regions are always rendered so they exist before their first write; an
          empty one takes no space. Neither is ever focused. */}
      <p role="status" aria-live="polite" className="personal__summary">
        {summary}
      </p>
      {showContent && truncated && (
        <p className="personal__hint">
          {strings.personalTruncatedHint({ shown: rows.length, total })}
        </p>
      )}
      <p role="status" aria-live="polite" className="personal__result">
        {result}
      </p>
      {loading && <p className="personal__status">{strings.personalLoading}</p>}
      {!loading && error !== null && (
        <div role="alert" className="personal__empty personal__empty--error">
          <span>{strings.personalLoadFailed({ message: error })}</span>
          <button
            type="button"
            className="personal__button personal__button--ghost"
            onClick={() => {
              commitLoading(true);
              void load({ quiet: false });
            }}
          >
            {strings.personalRetry}
          </button>
        </div>
      )}
      {showContent && rows.length === 0 && (
        <p className="personal__empty">{strings.personalEmpty}</p>
      )}
      {showContent && rows.length > 0 && (
        <div
          className="admin-table__scroll"
          onFocus={revealFocusedControl}
          role="group"
          aria-labelledby="personal-destinations-heading"
          tabIndex={0}
        >
          <table className="admin-table personal__table">
            <thead>
              <tr>
                <th scope="col" className="admin-table__lead">
                  {strings.columnPersonalHash}
                </th>
                {!compact && (
                  <>
                    <th scope="col">{strings.columnPersonalLabel}</th>
                    <th scope="col">{strings.columnPersonalType}</th>
                    <th scope="col">{strings.columnPersonalTarget}</th>
                    <th scope="col">{strings.columnPersonalRegisteredBy}</th>
                    <th scope="col">{strings.columnPersonalOptedIn}</th>
                    <th scope="col">{strings.columnPersonalStatus}</th>
                    <th scope="col">{strings.columnPersonalCreated}</th>
                  </>
                )}
                <th
                  scope="col"
                  className={`admin-table__actions${compact ? ' admin-table__kebab' : ''}`}
                  aria-label={strings.columnPersonalActions}
                />
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const { id } = row;
                const rowError = rowErrors[String(id)];
                const rowBusy = isBusy(`toggle:${id}`) || isBusy(`delete:${id}`);
                const typeLabel = strings.destinationTypeLabels[row.type];
                const open = expanded[String(id)] === true;
                const detailsId = `personal-details-${id}`;
                if (compact) {
                  return (
                    <tr key={id} data-personal-id={id}>
                      <td className="admin-table__lead personal__lead" title={row.personHash}>
                        <button
                          type="button"
                          className="personal__expand"
                          aria-expanded={open}
                          aria-controls={detailsId}
                          onClick={() => toggleExpanded(id)}
                        >
                          <span className="personal__expand-line">
                            <span className="personal__hash">{row.personHash}</span>
                            <StatusPill row={row} />
                            <span className="personal__chevron" aria-hidden="true" />
                          </span>
                          <span className="personal__expand-line personal__expand-line--sub">
                            <span>{row.label}</span>
                            <span aria-hidden="true">{strings.personalLabelSeparator}</span>
                            <span className="personal__id">{`#${id}`}</span>
                          </span>
                        </button>
                        {rowError !== undefined && (
                          <p role="alert" className="personal__row-error">
                            {rowError}
                          </p>
                        )}
                        <CompactDetails row={row} detailsId={detailsId} open={open} />
                      </td>
                      <td className="admin-table__actions admin-table__kebab">
                        <RowActionsMenu
                          rowLabel={rowNameOf(row)}
                          items={menuItemsOf({ row, rowBusy })}
                          busy={rowBusy}
                          focusKeys={[
                            `first:${id}`,
                            `enable:${id}`,
                            `disable:${id}`,
                            `delete:${id}`,
                          ]}
                          registerControl={registerControl}
                        />
                      </td>
                    </tr>
                  );
                }
                return (
                  <tr key={id} data-personal-id={id}>
                    <td className="admin-table__lead personal__hash" title={row.personHash}>
                      {row.personHash}
                    </td>
                    <td>
                      {row.label}
                      <span className="personal__sub personal__id">{`#${id}`}</span>
                    </td>
                    <td>{typeLabel}</td>
                    <td className="personal__target">{targetOf(row)}</td>
                    <td>
                      <RegisteredBy row={row} />
                    </td>
                    <td>
                      <OptedInFor row={row} />
                    </td>
                    <td>
                      <StatusCell row={row} />
                    </td>
                    <td>
                      <LocalTime timestamp={row.createdAt} />
                    </td>
                    <td className="admin-table__actions">
                      {renderActions({ row, rowBusy })}
                      {rowError !== undefined && (
                        <p role="alert" className="personal__row-error admin-table__aux">
                          {rowError}
                        </p>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

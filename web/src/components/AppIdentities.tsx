import { useCallback, useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import type {
  DeliveryApiClient,
  IdentityBinding,
  IdentityKey,
  IdentityRequest,
} from '../api/deliveryClient';
import { useExclusive } from '../hooks/useExclusive';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useCompactTable } from '../hooks/useCompactTable';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { formatRelativePast } from './formatRelative';
import { formatMinute, formatUtc } from './formatUtc';
import { RowActionsMenu } from './RowActionsMenu';
import type { RowMenuItem } from './RowActionsMenu';
import { messageOf, withoutKey } from './rowState';
import { revealFocusedControl } from './adminTableFocus';
import './adminTable.css';
import './AppIdentities.css';

interface AppIdentitiesProps {
  client: DeliveryApiClient;
}

const REQUESTS_HEADING_KEY = 'requests-heading';
const BINDINGS_HEADING_KEY = 'bindings-heading';
// Match the data attributes rendered on each request and binding row.
const REQUEST_ROW_ATTRIBUTE = 'data-request-id';
const BINDING_ROW_ATTRIBUTE = 'data-app-id';
// A superseded key keeps working this long after the deploy that replaced it.
const SUPERSEDED_GRACE_MS = 24 * 60 * 60 * 1000;

/** The value of `attribute` on the row containing `element`, or null. */
function rowIdOf({
  element,
  attribute,
}: {
  element: Element | null;
  attribute: string;
}): string | null {
  return element?.closest(`[${attribute}]`)?.getAttribute(attribute) ?? null;
}

/** The registry key of a request row's first control, or null when it has none (not pending). */
function requestControlKey(request: IdentityRequest): string | null {
  return request.status === 'pending' ? `approve:${request.id}` : null;
}

/** The registry key of a binding row's first control: its first revocable key, else Unbind. */
function bindingControlKey(binding: IdentityBinding): string {
  const revocable = binding.keys.find((identityKey) => identityKey.state !== 'revoked');
  return revocable === undefined ? `unbind:${binding.appId}` : `revoke:${revocable.id}`;
}

/** The first control to hand focus to after request `id` leaves its row: the nearest row that has one. */
function requestNeighbourKey({
  requests,
  id,
}: {
  requests: IdentityRequest[];
  id: number;
}): string | null {
  const index = requests.findIndex((candidate) => candidate.id === id);
  if (index === -1) {
    return null;
  }
  const nearest = [...requests.slice(index + 1), ...requests.slice(0, index).reverse()];
  for (const candidate of nearest) {
    const key = requestControlKey(candidate);
    if (key !== null) {
      return key;
    }
  }
  return null;
}

function revokedReasonText(reason: string | null): string | null {
  switch (reason) {
    case null:
      return null;
    case 'operator':
      return strings.identityRevokedByOperator;
    case 'unbound':
      return strings.identityRevokedWhenUnbound;
    case 'superseded':
      return strings.identityRevokedAfterGrace;
    default:
      // A reason this build does not know is shown as sent, never hidden.
      return reason;
  }
}

function lastUsedText({ identityKey, now }: { identityKey: IdentityKey; now: number }): string {
  return identityKey.lastUsedAt === null
    ? strings.identityKeyNeverUsed
    : strings.identityKeyLastUsed({
        relative: formatRelativePast({ at: identityKey.lastUsedAt, now }),
      });
}

function keySubText({ identityKey, now }: { identityKey: IdentityKey; now: number }): string {
  const issued = formatMinute(identityKey.createdAt);
  const lastUsed = lastUsedText({ identityKey, now });
  switch (identityKey.state) {
    case 'active':
      return strings.identityKeyIssued({ time: issued, lastUsed });
    case 'superseded': {
      const supersededMs =
        identityKey.supersededAt === null ? Number.NaN : Date.parse(identityKey.supersededAt);
      if (Number.isNaN(supersededMs)) {
        return strings.identityKeyIssued({ time: issued, lastUsed });
      }
      const stopsAt = new Date(supersededMs + SUPERSEDED_GRACE_MS).toISOString();
      return strings.identityKeySupersededSub({ time: formatMinute(stopsAt), lastUsed });
    }
    case 'revoked':
      return strings.identityKeyRevokedSub({
        time: formatMinute(identityKey.revokedAt ?? identityKey.createdAt),
        reason: revokedReasonText(identityKey.revokedReason),
      });
    default:
      // `unknown` and anything else: shown as unrecognised, never as active.
      return strings.identityKeyUnknownSub({ time: issued });
  }
}

function renderKeyPill(identityKey: IdentityKey): JSX.Element {
  switch (identityKey.state) {
    case 'active':
      return (
        <span className="app-identities__pill app-identities__pill--ok">
          {strings.identityKeyActive}
        </span>
      );
    case 'superseded':
      return (
        <span className="app-identities__pill app-identities__pill--neutral">
          {strings.identityKeySuperseded}
        </span>
      );
    case 'revoked':
      return (
        <span className="app-identities__pill app-identities__pill--fail">
          {strings.identityKeyRevoked}
        </span>
      );
    default:
      return (
        <span className="app-identities__pill app-identities__pill--unknown">
          {strings.identityKeyUnknown}
        </span>
      );
  }
}

function renderRequestStatusPill(request: IdentityRequest): JSX.Element {
  switch (request.status) {
    case 'pending':
      return (
        <span className="app-identities__pill app-identities__pill--pending">
          {strings.identityRequestPending}
        </span>
      );
    case 'rejected':
      return (
        <span className="app-identities__pill app-identities__pill--fail">
          {strings.identityRequestRejected}
        </span>
      );
    default:
      return (
        <span className="app-identities__pill app-identities__pill--unknown">
          {strings.identityRequestUnknown}
        </span>
      );
  }
}

/**
 * The App identities section of the Delivery tab: approve or reject the identity
 * requests an app's deploy files, and manage the approved bindings and their
 * keys. Only a key's hint is ever shown. Reloads after a mutation are quiet (no
 * loading state) so open confirms and focus survive them.
 */
export function AppIdentities({ client }: AppIdentitiesProps): JSX.Element {
  const [requests, setRequests] = useState<IdentityRequest[]>([]);
  const [bindings, setBindings] = useState<IdentityBinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const [result, setResult] = useState<string | null>(null);

  const compact = useCompactTable();
  const requestsRef = useRef<IdentityRequest[]>([]);
  const bindingsRef = useRef<IdentityBinding[]>([]);
  const loadingRef = useRef(true);
  const { registerControl, requestFocus } = useFocusRegistry();
  const { isBusy, exclusive } = useExclusive<string>();
  const trackList = useLatestRequest();

  const commitRequests = useCallback((next: IdentityRequest[]) => {
    requestsRef.current = next;
    setRequests(next);
  }, []);

  const commitBindings = useCallback((next: IdentityBinding[]) => {
    bindingsRef.current = next;
    setBindings(next);
  }, []);

  const commitLoading = useCallback((next: boolean) => {
    loadingRef.current = next;
    setLoading(next);
  }, []);

  const loadAll = useCallback(
    ({ quiet }: { quiet: boolean }): Promise<void> =>
      trackList(Promise.all([client.listIdentityRequests(), client.listIdentityBindings()])).then(
        (outcome) => {
          if (outcome.stale) {
            return;
          }
          // A quiet reload that superseded the initial or Retry load must still
          // settle the loading state, so it only stays quiet otherwise.
          const stayQuiet = quiet && !loadingRef.current;
          if (outcome.ok) {
            commitRequests(outcome.value[0]);
            commitBindings(outcome.value[1]);
            setError(null);
            setNow(Date.now());
          } else if (!stayQuiet) {
            commitRequests([]);
            commitBindings([]);
            setError(
              messageOf({
                rejection: outcome.error,
                fallback: strings.identitiesLoadFailedFallback,
              }),
            );
          } else {
            return;
          }
          commitLoading(false);
        },
      ),
    [client, trackList, commitRequests, commitBindings, commitLoading],
  );

  // Initial load.
  useEffect(() => {
    void loadAll({ quiet: false });
  }, [loadAll]);

  function startRefresh() {
    setResult(null);
    commitLoading(true);
    void loadAll({ quiet: false });
  }

  function failRow({ rowKey, message }: { rowKey: string; message: string }) {
    setRowErrors((current) => ({ ...current, [rowKey]: message }));
  }

  function clearRowError(rowKey: string) {
    setRowErrors((current) => withoutKey({ record: current, key: rowKey }));
  }

  /** Hands focus on only when it was inside this row (or lost to <body>), so a concurrent action cannot steal it. */
  function requestFocusIfRowOwnsIt({
    attribute,
    id,
    key,
  }: {
    attribute: string;
    id: string;
    key: string;
  }) {
    const focused = document.activeElement;
    if (
      focused === null ||
      focused === document.body ||
      rowIdOf({ element: focused, attribute }) === id
    ) {
      requestFocus(key);
    }
  }

  function handleApprove(request: IdentityRequest) {
    const { id, appId } = request;
    const rowKey = `request:${id}`;
    clearRowError(rowKey);
    exclusive({
      key: `approve:${id}`,
      work: async () => {
        try {
          const binding = await client.approveIdentityRequest({ id });
          // The neighbour is picked from the list as it is now, not as it was at
          // click time, so its control's key is one that is registered.
          const target = requestNeighbourKey({ requests: requestsRef.current, id });
          commitRequests(requestsRef.current.filter((candidate) => candidate.id !== id));
          commitBindings(
            [
              ...bindingsRef.current.filter((candidate) => candidate.appId !== binding.appId),
              binding,
            ].sort((left, right) => left.appId.localeCompare(right.appId)),
          );
          setNow(Date.now());
          setResult(strings.identityApproved({ app: appId, repository: binding.githubRepository }));
          requestFocusIfRowOwnsIt({
            attribute: REQUEST_ROW_ATTRIBUTE,
            id: String(id),
            key: target ?? REQUESTS_HEADING_KEY,
          });
          void loadAll({ quiet: true });
        } catch (rejection: unknown) {
          // The row stays listed and keeps focus, with the server's reason on it.
          failRow({
            rowKey,
            message: strings.identityApproveFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
          requestFocusIfRowOwnsIt({
            attribute: REQUEST_ROW_ATTRIBUTE,
            id: String(id),
            key: `approve:${id}`,
          });
        }
      },
    }).catch(() => undefined);
  }

  function handleReject(request: IdentityRequest) {
    const { id, appId } = request;
    const rowKey = `request:${id}`;
    clearRowError(rowKey);
    exclusive({
      key: `reject:${id}`,
      work: async () => {
        try {
          const rejected = await client.rejectIdentityRequest({ id });
          commitRequests(
            requestsRef.current.map((candidate) => (candidate.id === id ? rejected : candidate)),
          );
          setResult(strings.identityRejected({ app: appId }));
          // The row's whole Actions cell is now a status pill, so no control is
          // left in it to land on.
          requestFocusIfRowOwnsIt({
            attribute: REQUEST_ROW_ATTRIBUTE,
            id: String(id),
            key: REQUESTS_HEADING_KEY,
          });
          void loadAll({ quiet: true });
        } catch (rejection: unknown) {
          failRow({
            rowKey,
            message: strings.identityRejectFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        }
      },
    }).catch(() => undefined);
  }

  function handleRevoke({ identityKey, appId }: { identityKey: IdentityKey; appId: string }) {
    const { id, keyHint } = identityKey;
    const rowKey = `key:${id}`;
    clearRowError(rowKey);
    exclusive({
      key: `revoke:${id}`,
      work: async () => {
        try {
          const revoked = await client.revokeIdentityKey({ id });
          // The app was unbound while this was in flight: nothing to update or focus.
          if (!bindingsRef.current.some((candidate) => candidate.appId === appId)) {
            return;
          }
          commitBindings(
            bindingsRef.current.map((candidate) =>
              candidate.appId === appId
                ? {
                    ...candidate,
                    keys: candidate.keys.map((existing) =>
                      existing.id === id ? revoked : existing,
                    ),
                  }
                : candidate,
            ),
          );
          setNow(Date.now());
          setResult(strings.identityKeyRevokedResult({ hint: keyHint, app: appId }));
          // The key's button became a "Revoked" pill, so its own trigger-refocus
          // has nothing to land on: hand focus to the app's Unbind.
          requestFocusIfRowOwnsIt({
            attribute: BINDING_ROW_ATTRIBUTE,
            id: appId,
            key: `unbind:${appId}`,
          });
          void loadAll({ quiet: true });
        } catch (rejection: unknown) {
          failRow({
            rowKey,
            message: strings.identityRevokeFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        }
      },
    }).catch(() => undefined);
  }

  function handleUnbind(binding: IdentityBinding) {
    const { appId } = binding;
    const rowKey = `app:${appId}`;
    clearRowError(rowKey);
    exclusive({
      key: `unbind:${appId}`,
      work: async () => {
        try {
          const outcome = await client.unbindApp({ appId });
          const latest = bindingsRef.current;
          const index = latest.findIndex((candidate) => candidate.appId === appId);
          const neighbour = index === -1 ? undefined : (latest[index + 1] ?? latest[index - 1]);
          commitBindings(latest.filter((candidate) => candidate.appId !== appId));
          setResult(
            outcome.deleted
              ? strings.identityUnbound({ app: appId })
              : strings.identityAlreadyUnbound({ app: appId }),
          );
          requestFocusIfRowOwnsIt({
            attribute: BINDING_ROW_ATTRIBUTE,
            id: appId,
            key: neighbour === undefined ? BINDINGS_HEADING_KEY : bindingControlKey(neighbour),
          });
          void loadAll({ quiet: true });
        } catch (rejection: unknown) {
          failRow({
            rowKey,
            message: strings.identityUnbindFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        }
      },
    }).catch(() => undefined);
  }

  function renderRequestActions(request: IdentityRequest): JSX.Element {
    const { id, appId } = request;
    const rowError = rowErrors[`request:${id}`];
    switch (request.status) {
      case 'pending': {
        const approving = isBusy(`approve:${id}`);
        const rejecting = isBusy(`reject:${id}`);
        const anyBusy = approving || rejecting;
        if (compact) {
          const items: RowMenuItem[] = [
            {
              id: 'approve',
              label: approving ? strings.approvingIdentity : strings.approveIdentity,
              ariaLabel: approving
                ? strings.approvingIdentityNamed({ app: appId })
                : strings.approveIdentityNamed({ app: appId }),
              tone: 'primary',
              disabled: anyBusy,
              onSelect: () => handleApprove(request),
            },
            {
              id: 'reject',
              label: rejecting ? strings.rejectingIdentity : strings.rejectIdentity,
              ariaLabel: rejecting
                ? strings.rejectingIdentityNamed({ app: appId })
                : strings.rejectIdentityNamed({ app: appId }),
              tone: 'danger',
              disabled: anyBusy,
              confirm: {
                confirmLabel: strings.confirmRejectIdentity,
                confirmAriaLabel: strings.confirmRejectIdentityNamed({ app: appId }),
                cancelLabel: strings.cancelAction,
                cancelAriaLabel: strings.cancelRejectIdentityNamed({ app: appId }),
                notice: strings.rejectIdentityNotice,
                dialogTitle: strings.rejectIdentityDialogTitle,
                dialogItem: { label: strings.dialogItemApp, value: appId },
                onConfirm: () => handleReject(request),
              },
            },
          ];
          return (
            <RowActionsMenu
              rowLabel={appId}
              items={items}
              errors={rowError === undefined ? undefined : [rowError]}
              focusKeys={[`approve:${id}`, `reject:${id}`]}
              registerControl={registerControl}
            />
          );
        }
        return (
          <div className="app-identities__actions">
            <button
              ref={(element) => registerControl({ key: `approve:${id}`, element })}
              type="button"
              className="app-identities__button app-identities__button--primary"
              aria-label={
                approving
                  ? strings.approvingIdentityNamed({ app: appId })
                  : strings.approveIdentityNamed({ app: appId })
              }
              aria-disabled={anyBusy ? 'true' : undefined}
              onClick={() => {
                if (anyBusy) {
                  return;
                }
                handleApprove(request);
              }}
            >
              {approving ? strings.approvingIdentity : strings.approveIdentity}
            </button>
            <ConfirmButton
              triggerLabel={strings.rejectIdentity}
              triggerAriaLabel={strings.rejectIdentityNamed({ app: appId })}
              confirmLabel={strings.confirmRejectIdentity}
              confirmAriaLabel={strings.confirmRejectIdentityNamed({ app: appId })}
              dismissAriaLabel={strings.cancelRejectIdentityNamed({ app: appId })}
              className="app-identities__button app-identities__button--danger"
              confirmNotice={strings.rejectIdentityNotice}
              busy={anyBusy}
              busyLabel={rejecting ? strings.rejectingIdentity : strings.rejectIdentity}
              busyAriaLabel={
                rejecting
                  ? strings.rejectingIdentityNamed({ app: appId })
                  : strings.rejectIdentityNamed({ app: appId })
              }
              dialogTitle={strings.rejectIdentityDialogTitle}
              dialogItem={{ label: strings.dialogItemApp, value: appId }}
              registerTrigger={(element) => registerControl({ key: `reject:${id}`, element })}
              onConfirm={() => handleReject(request)}
            />
            {rowError !== undefined && (
              <p role="alert" className="app-identities__row-error">
                {rowError}
              </p>
            )}
          </div>
        );
      }
      case 'rejected':
        // Approve is gone with the rest of the cell: re-approving needs a fresh request. The
        // narrow layout has no room to repeat the Status column's pill, so its cell stays empty.
        return compact ? <></> : renderRequestStatusPill(request);
      default:
        // Fail closed: an unrecognised status offers no action at all. (The narrow layout
        // shows the explanation under the Status pill instead.)
        return compact ? (
          <></>
        ) : (
          <span className="app-identities__sub">{strings.identityRequestUnknownSub}</span>
        );
    }
  }

  function renderKeyRow({
    identityKey,
    appId,
  }: {
    identityKey: IdentityKey;
    appId: string;
  }): JSX.Element {
    const { id, keyHint } = identityKey;
    const rowError = rowErrors[`key:${id}`];
    return (
      <div key={id} className="app-identities__key" data-key-id={id}>
        {renderKeyPill(identityKey)}
        <span className="app-identities__mono">{`${keyHint}…`}</span>
        <span className="app-identities__sub app-identities__sub--inline">
          {keySubText({ identityKey, now })}
        </span>
        {/* The narrow layout moves Revoke into the app's row menu. */}
        {!compact && identityKey.state !== 'revoked' && (
          <ConfirmButton
            triggerLabel={strings.revokeKey}
            triggerAriaLabel={strings.revokeKeyNamed({ hint: keyHint, app: appId })}
            confirmLabel={strings.confirmRevokeKey}
            confirmAriaLabel={strings.confirmRevokeKeyNamed({ hint: keyHint, app: appId })}
            dismissAriaLabel={strings.cancelRevokeKeyNamed({ hint: keyHint, app: appId })}
            className="app-identities__button app-identities__button--danger"
            confirmNotice={strings.revokeKeyNotice}
            dialogTitle={strings.revokeKeyDialogTitle}
            dialogItem={{
              label: strings.dialogItemKey,
              value: strings.dialogKeyForApp({ hint: keyHint, app: appId }),
            }}
            busy={isBusy(`revoke:${id}`)}
            busyLabel={strings.revokingKey}
            busyAriaLabel={strings.revokingKeyNamed({ hint: keyHint, app: appId })}
            registerTrigger={(element) => registerControl({ key: `revoke:${id}`, element })}
            onConfirm={() => handleRevoke({ identityKey, appId })}
          />
        )}
        {!compact && rowError !== undefined && (
          <p role="alert" className="app-identities__row-error">
            {rowError}
          </p>
        )}
      </div>
    );
  }

  const pendingCount = requests.filter((candidate) => candidate.status === 'pending').length;
  const showContent = !loading && error === null;

  return (
    <section className="app-identities" aria-labelledby="app-identities-heading">
      <div className="app-identities__head">
        <h2 id="app-identities-heading">{strings.identitiesHeading}</h2>
        <button
          type="button"
          className="app-identities__button app-identities__button--ghost"
          onClick={startRefresh}
        >
          {strings.identitiesRefresh}
        </button>
      </div>
      <p className="app-identities__hint">{strings.identitiesHint}</p>
      {/* Both live regions are always rendered so they exist before their first
          write; an empty one takes no space. Neither is ever focused. */}
      <p role="status" aria-live="polite" className="app-identities__summary">
        {showContent && pendingCount > 0
          ? strings.identitiesPendingSummary({ count: pendingCount })
          : ''}
      </p>
      <p role="status" aria-live="polite" className="app-identities__result">
        {result}
      </p>
      {loading && <p className="app-identities__status">{strings.identitiesLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="app-identities__empty app-identities__empty--error">
          {strings.identitiesLoadFailed({ message: error })}{' '}
          <button
            type="button"
            className="app-identities__button"
            onClick={() => {
              commitLoading(true);
              void loadAll({ quiet: false });
            }}
          >
            {strings.identitiesRetry}
          </button>
        </p>
      )}
      {showContent && (
        <>
          <h3
            id="requests-heading"
            tabIndex={-1}
            ref={(element) => registerControl({ key: REQUESTS_HEADING_KEY, element })}
          >
            {strings.identitiesRequestsHeading}
          </h3>
          {requests.length === 0 ? (
            <p className="app-identities__empty">{strings.identitiesRequestsEmpty}</p>
          ) : (
            <div
              className="admin-table__scroll app-identities__scroll"
              onFocus={revealFocusedControl}
              role="group"
              aria-labelledby="requests-heading"
              tabIndex={0}
            >
              <table
                className="admin-table app-identities__table"
                aria-labelledby="requests-heading"
              >
                <thead>
                  <tr>
                    <th scope="col" className="admin-table__lead">
                      {strings.columnIdentityApp}
                    </th>
                    <th scope="col">{strings.columnIdentityStatus}</th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnIdentityRequestedBy}
                    </th>
                    <th scope="col">{strings.columnIdentityRequested}</th>
                    <th
                      scope="col"
                      className={`admin-table__actions app-identities__actions-cell${compact ? ' admin-table__kebab' : ''}`}
                      aria-label={strings.columnIdentityActions}
                    />
                  </tr>
                </thead>
                <tbody>
                  {requests.map((request) => (
                    <tr
                      key={request.id}
                      data-request-id={request.id}
                      className={
                        request.status === 'pending' ? 'app-identities__row--pending' : undefined
                      }
                    >
                      <td className="admin-table__lead" title={request.appId}>
                        <span className="app-identities__mono">{request.appId}</span>
                      </td>
                      <td>
                        {renderRequestStatusPill(request)}
                        {compact &&
                          request.status !== 'pending' &&
                          request.status !== 'rejected' && (
                            <span className="app-identities__sub">
                              {strings.identityRequestUnknownSub}
                            </span>
                          )}
                      </td>
                      <td>
                        <span className="app-identities__mono">{request.githubRepository}</span>
                        <span className="app-identities__sub">
                          {strings.identityRepoSub({
                            repositoryId: request.githubRepositoryId,
                            workflow: request.workflowPath,
                          })}
                        </span>
                        <span className="app-identities__sub">
                          {strings.identityRefSub({
                            ref: request.gitRef,
                            environment: request.environment,
                            runId: request.githubRunId,
                          })}
                        </span>
                      </td>
                      <td>
                        <time dateTime={request.createdAt}>{formatUtc(request.createdAt)}</time>
                      </td>
                      <td
                        className={`admin-table__actions app-identities__actions-cell${compact ? ' admin-table__kebab' : ''}`}
                      >
                        {renderRequestActions(request)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {requests.some((request) => request.status === 'rejected') && (
            <p className="app-identities__sub">{strings.identitiesRejectedNote}</p>
          )}

          <h3
            id="bindings-heading"
            tabIndex={-1}
            ref={(element) => registerControl({ key: BINDINGS_HEADING_KEY, element })}
          >
            {strings.identitiesBindingsHeading}
          </h3>
          {bindings.length === 0 ? (
            <p className="app-identities__empty">{strings.identitiesBindingsEmpty}</p>
          ) : (
            <div
              className="admin-table__scroll app-identities__scroll"
              onFocus={revealFocusedControl}
              role="group"
              aria-labelledby="bindings-heading"
              tabIndex={0}
            >
              <table
                className="admin-table app-identities__table"
                aria-labelledby="bindings-heading"
              >
                <thead>
                  <tr>
                    <th scope="col" className="admin-table__lead">
                      {strings.columnIdentityApp}
                    </th>
                    <th scope="col">{strings.columnIdentityBoundTo}</th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnIdentityKeys}
                    </th>
                    <th
                      scope="col"
                      className={`admin-table__actions app-identities__actions-cell${compact ? ' admin-table__kebab' : ''}`}
                      aria-label={strings.columnIdentityActions}
                    />
                  </tr>
                </thead>
                <tbody>
                  {bindings.map((binding) => {
                    const { appId } = binding;
                    const rowError = rowErrors[`app:${appId}`];
                    const unbinding = isBusy(`unbind:${appId}`);
                    const menuItems: RowMenuItem[] = [
                      ...binding.keys
                        .filter((identityKey) => identityKey.state !== 'revoked')
                        .map((identityKey): RowMenuItem => {
                          const { id, keyHint } = identityKey;
                          const revoking = isBusy(`revoke:${id}`);
                          const named = strings.revokeKeyNamed({ hint: keyHint, app: appId });
                          return {
                            id: `revoke:${id}`,
                            label: revoking
                              ? strings.revokingKeyNamed({ hint: keyHint, app: appId })
                              : named,
                            tone: 'danger',
                            disabled: revoking,
                            confirm: {
                              confirmLabel: strings.confirmRevokeKey,
                              confirmAriaLabel: strings.confirmRevokeKeyNamed({
                                hint: keyHint,
                                app: appId,
                              }),
                              cancelLabel: strings.cancelAction,
                              cancelAriaLabel: strings.cancelRevokeKeyNamed({
                                hint: keyHint,
                                app: appId,
                              }),
                              notice: strings.revokeKeyNotice,
                              dialogTitle: strings.revokeKeyDialogTitle,
                              dialogItem: {
                                label: strings.dialogItemKey,
                                value: strings.dialogKeyForApp({ hint: keyHint, app: appId }),
                              },
                              onConfirm: () => handleRevoke({ identityKey, appId }),
                            },
                          };
                        }),
                      {
                        id: 'unbind',
                        label: unbinding
                          ? strings.unbindingAppNamed({ app: appId })
                          : strings.unbindAppNamed({ app: appId }),
                        tone: 'danger',
                        disabled: unbinding,
                        confirm: {
                          confirmLabel: strings.confirmUnbindApp,
                          confirmAriaLabel: strings.confirmUnbindAppNamed({ app: appId }),
                          cancelLabel: strings.cancelAction,
                          cancelAriaLabel: strings.cancelUnbindAppNamed({ app: appId }),
                          notice: strings.unbindAppNotice,
                          dialogTitle: strings.unbindAppDialogTitle,
                          dialogItem: { label: strings.dialogItemApp, value: appId },
                          onConfirm: () => handleUnbind(binding),
                        },
                      },
                    ];
                    // The row's own error, then any key's: the menu is where they are read.
                    const menuErrors = [
                      rowError,
                      ...binding.keys.map((identityKey) => rowErrors[`key:${identityKey.id}`]),
                    ].filter((message): message is string => message !== undefined);
                    return (
                      <tr key={appId} data-app-id={appId}>
                        <td className="admin-table__lead" title={appId}>
                          <span className="app-identities__mono">{appId}</span>
                          <span className="app-identities__sub">
                            {strings.identityApprovedSub({ time: formatUtc(binding.approvedAt) })}
                          </span>
                        </td>
                        <td>
                          <span className="app-identities__mono">{binding.githubRepository}</span>
                          <span className="app-identities__sub">
                            {strings.identityBoundSub({
                              workflow: binding.workflowPath,
                              ref: binding.gitRef,
                              environment: binding.environment,
                            })}
                          </span>
                        </td>
                        <td className="admin-table__fill">
                          {binding.keys.length === 0 ? (
                            <span className="app-identities__sub">{strings.identityNoKeys}</span>
                          ) : (
                            <div className="app-identities__keys">
                              {binding.keys.map((identityKey) =>
                                renderKeyRow({ identityKey, appId }),
                              )}
                            </div>
                          )}
                        </td>
                        {compact ? (
                          <td className="admin-table__actions admin-table__kebab app-identities__actions-cell">
                            <RowActionsMenu
                              rowLabel={appId}
                              items={menuItems}
                              errors={menuErrors}
                              focusKeys={[
                                `unbind:${appId}`,
                                ...binding.keys.map((identityKey) => `revoke:${identityKey.id}`),
                              ]}
                              registerControl={registerControl}
                            />
                          </td>
                        ) : (
                          <td className="admin-table__actions app-identities__actions-cell">
                            <div className="app-identities__actions">
                              <ConfirmButton
                                triggerLabel={strings.unbindApp}
                                triggerAriaLabel={strings.unbindAppNamed({ app: appId })}
                                confirmLabel={strings.confirmUnbindApp}
                                confirmAriaLabel={strings.confirmUnbindAppNamed({ app: appId })}
                                dismissAriaLabel={strings.cancelUnbindAppNamed({ app: appId })}
                                className="app-identities__button app-identities__button--danger"
                                confirmNotice={strings.unbindAppNotice}
                                dialogTitle={strings.unbindAppDialogTitle}
                                dialogItem={{ label: strings.dialogItemApp, value: appId }}
                                busy={isBusy(`unbind:${appId}`)}
                                busyLabel={strings.unbindingApp}
                                busyAriaLabel={strings.unbindingAppNamed({ app: appId })}
                                registerTrigger={(element) =>
                                  registerControl({ key: `unbind:${appId}`, element })
                                }
                                onConfirm={() => handleUnbind(binding)}
                              />
                              {rowError !== undefined && (
                                <p role="alert" className="app-identities__row-error">
                                  {rowError}
                                </p>
                              )}
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
        </>
      )}
    </section>
  );
}

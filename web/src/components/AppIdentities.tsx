import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { JSX } from 'react';

import { ACKNOWLEDGE_ALL_LIMIT, identityErrorCode } from '../api/deliveryClient';
import type {
  DeliveryApiClient,
  IdentityBinding,
  IdentityKey,
  IdentityRegistrar,
  IdentityRequest,
} from '../api/deliveryClient';
import { useExclusive } from '../hooks/useExclusive';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useCompactTable } from '../hooks/useCompactTable';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { ConfirmActions, ConfirmItem, ConfirmNotice } from './ConfirmDialog';
import { Modal } from './Modal';
import { formatRelativePast } from './formatRelative';
import { formatDateTimeZone } from './formatLocalTime';
import { LocalTime } from './LocalTime';
import { RefreshButton } from './RefreshButton';
import { RowActionsMenu } from './RowActionsMenu';
import type { RowMenuItem } from './RowActionsMenu';
import { messageOf, withoutKey } from './rowState';
import { revealFocusedControl } from './adminTableFocus';
import './adminTable.css';
import './AppIdentities.css';

interface AppIdentitiesProps {
  client: DeliveryApiClient;
  /** Bumped by the owner when the lists may have changed server-side; a change reloads quietly. */
  refreshSignal?: number;
  /** Called once after an approval succeeds (never on a rejected approve). */
  onBindingApproved?: () => void;
}

const REQUESTS_HEADING_KEY = 'requests-heading';
const BINDINGS_HEADING_KEY = 'bindings-heading';
const REGISTRARS_HEADING_KEY = 'registrars-heading';
// Match the data attributes rendered on each request, registrar and binding row.
const REQUEST_ROW_ATTRIBUTE = 'data-request-id';
const REGISTRAR_ROW_ATTRIBUTE = 'data-registrar-id';
const BINDING_ROW_ATTRIBUTE = 'data-app-id';

// Mirrors the server's registrar prefix grammar (2-32 chars ending in "_"); the server stays
// the authority, this only keeps an obviously invalid prefix from being sent.
const PREFIX_GRAMMAR = /^[a-z0-9][a-z0-9_]{0,30}_$/;
const PLATFORM_PREFIX = 'platform_';
const EVERYONE_PREFIX = 'everyone_';

type ApproveRegistrarErrorClass = 'prefix' | 'request-gone' | 'request-pending' | 'other';

/**
 * Where an approve-registrar failure belongs. Fail closed: anything not recognised is `other`,
 * shown in the dialog but never under the prefix field.
 */
function classifyApproveRegistrarError(rejection: unknown): ApproveRegistrarErrorClass {
  switch (identityErrorCode(rejection)) {
    case 'invalid-prefix':
    case 'prefix-taken':
    case 'prefix-overlap':
      return 'prefix';
    case 'request-not-pending':
    case 'request-not-registrar':
    case 'binding-gone':
    case 'not-found':
      return 'request-gone';
    case 'workflow-bound':
      return 'request-pending';
    default:
      return 'other';
  }
}

/** The client-side problem with a trimmed prefix, or null when it may be sent. */
function prefixProblem(prefix: string): string | null {
  if (prefix.startsWith(PLATFORM_PREFIX)) {
    return strings.prefixReservedPlatform;
  }
  if (prefix === EVERYONE_PREFIX) {
    return strings.prefixReservedEveryone;
  }
  return PREFIX_GRAMMAR.test(prefix) ? null : strings.prefixInvalidGrammar;
}

/** How a request is named in every label: the app id, or `<repository> (registrar)`. */
interface RequestLabels {
  rowLabel: string;
  approveLabel: string;
  approveAria: string;
  approvingAria: string;
  rejectAria: string;
  confirmRejectAria: string;
  cancelRejectAria: string;
  rejectingAria: string;
  dialogItem: { label: string; value: string };
  rejectedResult: string;
}

/** The labels for a request an action is offered on, or null (unknown kind: no actions). */
function requestLabels(request: IdentityRequest): RequestLabels | null {
  const { appId, githubRepository: repository } = request;
  switch (request.kind) {
    case 'app':
      if (appId === null) {
        return null;
      }
      return {
        rowLabel: appId,
        approveLabel: strings.approveIdentity,
        approveAria: strings.approveIdentityNamed({ app: appId }),
        approvingAria: strings.approvingIdentityNamed({ app: appId }),
        rejectAria: strings.rejectIdentityNamed({ app: appId }),
        confirmRejectAria: strings.confirmRejectIdentityNamed({ app: appId }),
        cancelRejectAria: strings.cancelRejectIdentityNamed({ app: appId }),
        rejectingAria: strings.rejectingIdentityNamed({ app: appId }),
        dialogItem: { label: strings.dialogItemApp, value: appId },
        rejectedResult: strings.identityRejected({ app: appId }),
      };
    case 'registrar':
      return {
        rowLabel: strings.registrarRequestRowLabel({ repository }),
        approveLabel: strings.approveRegistrar,
        approveAria: strings.approveRegistrarNamed({ repository }),
        approvingAria: strings.approvingRegistrarNamed({ repository }),
        rejectAria: strings.rejectRegistrarNamed({ repository }),
        confirmRejectAria: strings.confirmRejectRegistrarNamed({ repository }),
        cancelRejectAria: strings.cancelRejectRegistrarNamed({ repository }),
        rejectingAria: strings.rejectingRegistrarNamed({ repository }),
        dialogItem: {
          label: strings.dialogItemWorkflow,
          value: strings.dialogWorkflowValue({
            repository,
            workflowPath: request.workflowPath,
          }),
        },
        rejectedResult: strings.identityRegistrarRejected({ repository }),
      };
    default:
      return null;
  }
}
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

/**
 * The registry key of a request row's first control, or null when it has none (not pending, or
 * a kind this build does not know). Request ids are unique across kinds, so an app request's
 * Approve and a registrar request's "Approve as registrar…" share this key shape.
 */
function requestControlKey(request: IdentityRequest): string | null {
  return request.status === 'pending' && request.kind !== 'unknown'
    ? `approve:${request.id}`
    : null;
}

/** A registrar-created app the operator has not marked seen yet. */
function isNewBinding(binding: IdentityBinding): boolean {
  return binding.registrar !== null && binding.acknowledgedAt === null;
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
  const issued = formatDateTimeZone({ timestamp: identityKey.createdAt });
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
      return strings.identityKeySupersededSub({
        time: formatDateTimeZone({ timestamp: stopsAt }),
        lastUsed,
      });
    }
    case 'revoked':
      return strings.identityKeyRevokedSub({
        time: formatDateTimeZone({ timestamp: identityKey.revokedAt ?? identityKey.createdAt }),
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
  if (request.kind === 'unknown') {
    // Fail closed: whatever its status says, a request this build cannot read is never "Pending".
    return (
      <span className="app-identities__pill app-identities__pill--unknown">
        {strings.identityRequestUnknown}
      </span>
    );
  }
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
export function AppIdentities({
  client,
  refreshSignal,
  onBindingApproved,
}: AppIdentitiesProps): JSX.Element {
  const [requests, setRequests] = useState<IdentityRequest[]>([]);
  const [bindings, setBindings] = useState<IdentityBinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [registrars, setRegistrars] = useState<IdentityRegistrar[]>([]);
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const [resultState, setResultState] = useState<{
    message: string;
    kind: 'success' | 'error';
  } | null>(null);
  // The approve-as-registrar dialog. `dialogRequest` is kept after a close (the closed Modal
  // renders nothing); `dialogRestoreFocus` is set in the same update as `dialogOpen`, so Modal's
  // close cleanup sees the right value for that close.
  const [dialogRequest, setDialogRequest] = useState<IdentityRequest | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogRestoreFocus, setDialogRestoreFocus] = useState(true);
  const [prefixValue, setPrefixValue] = useState('');
  const [prefixError, setPrefixError] = useState<string | null>(null);
  const [dialogError, setDialogError] = useState<string | null>(null);

  const compact = useCompactTable();
  const requestsRef = useRef<IdentityRequest[]>([]);
  const registrarsRef = useRef<IdentityRegistrar[]>([]);
  const bindingsRef = useRef<IdentityBinding[]>([]);
  const loadingRef = useRef(true);
  const prefixInputRef = useRef<HTMLInputElement | null>(null);
  const approveReturnRef = useRef<HTMLElement | null>(null);
  const prefixHintId = useId();
  const prefixErrorId = useId();
  const dialogErrorId = useId();
  const { registerControl, requestFocus, getControl } = useFocusRegistry();
  const { isBusy, exclusive } = useExclusive<string>();
  const trackList = useLatestRequest();

  function setResult(message: string | null) {
    setResultState(message === null ? null : { message, kind: 'success' });
  }

  function announceError(message: string) {
    setResultState({ message, kind: 'error' });
  }

  const commitRegistrars = useCallback((next: IdentityRegistrar[]) => {
    registrarsRef.current = next;
    setRegistrars(next);
  }, []);

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
      trackList(
        Promise.all([
          client.listIdentityRequests(),
          client.listIdentityBindings(),
          client.listRegistrars(),
        ]),
      ).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        // A quiet reload that superseded the initial or Retry load must still
        // settle the loading state, so it only stays quiet otherwise.
        const stayQuiet = quiet && !loadingRef.current;
        if (outcome.ok) {
          commitRequests(outcome.value[0]);
          commitBindings(outcome.value[1]);
          commitRegistrars(outcome.value[2]);
          setError(null);
          setNow(Date.now());
        } else if (!stayQuiet) {
          commitRequests([]);
          commitBindings([]);
          commitRegistrars([]);
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
      }),
    [client, trackList, commitRequests, commitBindings, commitRegistrars, commitLoading],
  );

  // Initial load.
  useEffect(() => {
    void loadAll({ quiet: false });
  }, [loadAll]);

  // A refresh signal change after mount reloads quietly; the mount value triggers nothing extra.
  const lastRefreshSignalRef = useRef(refreshSignal);
  useEffect(() => {
    if (lastRefreshSignalRef.current === refreshSignal) {
      return;
    }
    lastRefreshSignalRef.current = refreshSignal;
    void loadAll({ quiet: true });
  }, [refreshSignal, loadAll]);

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
    if (request.kind !== 'app' || appId === null) {
      // A registrar request is approved only through the prefix dialog.
      return;
    }
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
          onBindingApproved?.();
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
    const { id } = request;
    const labels = requestLabels(request);
    if (labels === null) {
      return;
    }
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
          setResult(labels.rejectedResult);
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

  function handleMarkSeen(binding: IdentityBinding) {
    const { appId } = binding;
    if (isBusy('ack-all') || anyAckBusy()) {
      return;
    }
    const rowKey = `app:${appId}`;
    clearRowError(rowKey);
    exclusive({
      key: `ack:${appId}`,
      work: async () => {
        try {
          const updated = await client.acknowledgeBinding({ appId });
          commitBindings(
            bindingsRef.current.map((candidate) =>
              candidate.appId === appId
                ? { ...candidate, acknowledgedAt: updated.acknowledgedAt }
                : candidate,
            ),
          );
          setResult(strings.identityMarkedSeen({ app: appId }));
          // Mark seen is gone from the row; `unbind:<app>` is the row's Unbind in the wide
          // layout and its kebab trigger in the compact one (Unbind is then a menu item).
          requestFocusIfRowOwnsIt({
            attribute: BINDING_ROW_ATTRIBUTE,
            id: appId,
            key: `unbind:${appId}`,
          });
        } catch (rejection: unknown) {
          if (identityErrorCode(rejection) === 'binding-gone') {
            // Unbound meanwhile: not a row error. Show the neutral result and refresh.
            setResult(strings.identityNoLongerBound({ app: appId }));
            requestFocusIfRowOwnsIt({
              attribute: BINDING_ROW_ATTRIBUTE,
              id: appId,
              key: BINDINGS_HEADING_KEY,
            });
            void loadAll({ quiet: true });
            return;
          }
          failRow({
            rowKey,
            message: strings.identityMarkSeenFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        }
      },
    }).catch(() => undefined);
  }

  function handleMarkAllSeen() {
    if (isBusy('ack-all') || anyAckBusy()) {
      return;
    }
    clearRowError('ack-all');
    const appIds = bindingsRef.current
      .filter(isNewBinding)
      .map((candidate) => candidate.appId)
      .slice(0, ACKNOWLEDGE_ALL_LIMIT);
    if (appIds.length === 0) {
      return;
    }
    exclusive({
      key: 'ack-all',
      work: async () => {
        try {
          const acknowledged = await client.acknowledgeAllBindings({ appIds });
          const listed = new Set(appIds);
          const seenAt = new Date().toISOString();
          commitBindings(
            bindingsRef.current.map((candidate) =>
              listed.has(candidate.appId) && isNewBinding(candidate)
                ? { ...candidate, acknowledgedAt: seenAt }
                : candidate,
            ),
          );
          setResult(strings.identityMarkedAllSeen({ count: acknowledged }));
          requestFocus(BINDINGS_HEADING_KEY);
          void loadAll({ quiet: true });
        } catch (rejection: unknown) {
          failRow({
            rowKey: 'ack-all',
            message: strings.identityMarkAllSeenFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        }
      },
    }).catch(() => undefined);
  }

  /** True while any per-row Mark seen request is in flight. */
  function anyAckBusy(): boolean {
    return bindingsRef.current.some((candidate) => isBusy(`ack:${candidate.appId}`));
  }

  /** Opens the prefix dialog for a registrar request; focus returns to its trigger on Cancel. */
  function openApproveRegistrarDialog(request: IdentityRequest) {
    clearRowError(`request:${request.id}`);
    approveReturnRef.current = getControl(`approve:${request.id}`);
    setDialogRequest(request);
    setPrefixValue('');
    setPrefixError(null);
    setDialogError(null);
    setDialogRestoreFocus(true);
    setDialogOpen(true);
  }

  /** Closes the dialog; `restoreFocus` false when the caller moves focus to an explicit target. */
  function closeApproveRegistrarDialog({ restoreFocus }: { restoreFocus: boolean }) {
    setDialogRestoreFocus(restoreFocus);
    setDialogOpen(false);
  }

  const approveRegistrarBusy =
    dialogRequest !== null && isBusy(`approve-registrar:${dialogRequest.id}`);

  function dismissApproveRegistrarDialog() {
    if (approveRegistrarBusy) {
      return;
    }
    closeApproveRegistrarDialog({ restoreFocus: true });
  }

  function focusPrefixInput() {
    prefixInputRef.current?.focus();
  }

  function submitApproveRegistrar() {
    if (dialogRequest === null || approveRegistrarBusy) {
      return;
    }
    const { id } = dialogRequest;
    const prefix = prefixValue.trim();
    setPrefixValue(prefix);
    setDialogError(null);
    const problem = prefixProblem(prefix);
    if (problem !== null) {
      setPrefixError(problem);
      focusPrefixInput();
      return;
    }
    setPrefixError(null);
    exclusive({
      key: `approve-registrar:${id}`,
      work: async () => {
        try {
          const registrar = await client.approveRegistrarRequest({ id, prefix });
          closeApproveRegistrarDialog({ restoreFocus: false });
          commitRequests(requestsRef.current.filter((candidate) => candidate.id !== id));
          commitRegistrars(
            [
              ...registrarsRef.current.filter((candidate) => candidate.id !== registrar.id),
              registrar,
            ].sort((left, right) => left.prefix.localeCompare(right.prefix)),
          );
          setResult(
            strings.registrarApproved({
              prefix: registrar.prefix,
              repository: registrar.githubRepository,
            }),
          );
          requestFocus(`delete-registrar:${registrar.id}`);
          // The sibling requests it swept and any apps are server-side state: refetch.
          void loadAll({ quiet: true });
        } catch (rejection: unknown) {
          const message = messageOf({ rejection, fallback: strings.genericError });
          switch (classifyApproveRegistrarError(rejection)) {
            case 'prefix':
              // The dialog stays open with the server's reason under the field.
              setPrefixError(message);
              focusPrefixInput();
              break;
            case 'request-gone':
              // The request row is no longer pending: its trigger is gone, so the message goes
              // to the result region and focus to the Requests heading.
              closeApproveRegistrarDialog({ restoreFocus: false });
              announceError(strings.registrarApproveFailed({ message }));
              requestFocus(REQUESTS_HEADING_KEY);
              void loadAll({ quiet: true });
              break;
            case 'request-pending':
              // The row stays pending, so the error sits on it and focus returns to its trigger.
              closeApproveRegistrarDialog({ restoreFocus: false });
              failRow({
                rowKey: `request:${id}`,
                message: strings.registrarApproveFailed({ message }),
              });
              requestFocus(`approve:${id}`);
              void loadAll({ quiet: true });
              break;
            default:
              // Not about the prefix and not about the row (network, 5xx): retry from the dialog.
              setDialogError(strings.registrarApproveFailed({ message }));
              focusPrefixInput();
              break;
          }
        }
      },
    }).catch(() => undefined);
  }

  function handleDeleteRegistrar(registrar: IdentityRegistrar) {
    const { id, prefix } = registrar;
    const rowKey = `registrar:${id}`;
    clearRowError(rowKey);
    exclusive({
      key: `delete-registrar:${id}`,
      work: async () => {
        try {
          const outcome = await client.deleteRegistrar({ id });
          const latest = registrarsRef.current;
          const index = latest.findIndex((candidate) => candidate.id === id);
          const neighbour = index === -1 ? undefined : (latest[index + 1] ?? latest[index - 1]);
          commitRegistrars(latest.filter((candidate) => candidate.id !== id));
          commitBindings(bindingsRef.current.filter((candidate) => candidate.registrar?.id !== id));
          // The count comes from the response: registrars create apps, so a cached one drifts.
          setResult(
            outcome.deleted
              ? strings.registrarDeleted({ prefix, count: outcome.unboundApps })
              : strings.registrarAlreadyDeleted({ prefix }),
          );
          requestFocusIfRowOwnsIt({
            attribute: REGISTRAR_ROW_ATTRIBUTE,
            id: String(id),
            key:
              neighbour === undefined ? REGISTRARS_HEADING_KEY : `delete-registrar:${neighbour.id}`,
          });
          void loadAll({ quiet: true });
        } catch (rejection: unknown) {
          failRow({
            rowKey,
            message: strings.registrarDeleteFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        }
      },
    }).catch(() => undefined);
  }

  function renderRequestActions(request: IdentityRequest): JSX.Element {
    const { id } = request;
    const rowError = rowErrors[`request:${id}`];
    const labels = requestLabels(request);
    if (labels === null) {
      // Fail closed: a request kind this build does not know offers no action at all.
      return compact ? (
        <></>
      ) : (
        <span className="app-identities__sub">{strings.identityUnknownRequestSub}</span>
      );
    }
    switch (request.status) {
      case 'pending': {
        const isRegistrar = request.kind === 'registrar';
        // The approve button registers as `approve:<id>` for both kinds (request ids are unique
        // across kinds, so the neighbour-focus helpers need no change); only the busy key
        // differs, because the registrar flow runs from the dialog under its own key.
        const approveBusyKey = isRegistrar ? `approve-registrar:${id}` : `approve:${id}`;
        const approving = isBusy(approveBusyKey);
        const rejecting = isBusy(`reject:${id}`);
        const anyBusy = approving || rejecting;
        const approve = () => {
          if (isRegistrar) {
            openApproveRegistrarDialog(request);
          } else {
            handleApprove(request);
          }
        };
        if (compact) {
          const items: RowMenuItem[] = [
            {
              id: 'approve',
              label: approving ? strings.approvingIdentity : labels.approveLabel,
              ariaLabel: approving ? labels.approvingAria : labels.approveAria,
              tone: 'primary',
              disabled: anyBusy,
              onSelect: approve,
            },
            {
              id: 'reject',
              label: rejecting ? strings.rejectingIdentity : strings.rejectIdentity,
              ariaLabel: rejecting ? labels.rejectingAria : labels.rejectAria,
              tone: 'danger',
              disabled: anyBusy,
              confirm: {
                confirmLabel: strings.confirmRejectIdentity,
                confirmAriaLabel: labels.confirmRejectAria,
                cancelLabel: strings.cancelAction,
                cancelAriaLabel: labels.cancelRejectAria,
                notice: strings.rejectIdentityNotice,
                dialogTitle: strings.rejectIdentityDialogTitle,
                dialogItem: labels.dialogItem,
                onConfirm: () => handleReject(request),
              },
            },
          ];
          return (
            <RowActionsMenu
              rowLabel={labels.rowLabel}
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
              aria-label={approving ? labels.approvingAria : labels.approveAria}
              aria-disabled={anyBusy ? 'true' : undefined}
              onClick={() => {
                if (anyBusy) {
                  return;
                }
                approve();
              }}
            >
              {approving ? strings.approvingIdentity : labels.approveLabel}
            </button>
            <ConfirmButton
              triggerLabel={strings.rejectIdentity}
              triggerAriaLabel={labels.rejectAria}
              confirmLabel={strings.confirmRejectIdentity}
              confirmAriaLabel={labels.confirmRejectAria}
              dismissAriaLabel={labels.cancelRejectAria}
              className="app-identities__button app-identities__button--danger"
              confirmNotice={strings.rejectIdentityNotice}
              busy={anyBusy}
              busyLabel={rejecting ? strings.rejectingIdentity : strings.rejectIdentity}
              busyAriaLabel={rejecting ? labels.rejectingAria : labels.rejectAria}
              dialogTitle={strings.rejectIdentityDialogTitle}
              dialogItem={labels.dialogItem}
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

  function renderRegistrarRow(registrar: IdentityRegistrar): JSX.Element {
    const { id, prefix, githubRepository: repository } = registrar;
    const rowError = rowErrors[`registrar:${id}`];
    const deleting = isBusy(`delete-registrar:${id}`);
    const notice = strings.deleteRegistrarNotice({ count: registrar.appCount });
    const dialogItem = {
      label: strings.dialogItemRegistrar,
      value: strings.dialogRegistrarValue({ prefix, repository }),
    };
    return (
      <tr key={id} data-registrar-id={id}>
        <td className="admin-table__lead" title={prefix}>
          <span className="app-identities__pill app-identities__pill--prefix">{prefix}</span>
        </td>
        <td className="admin-table__fill">
          <span className="app-identities__mono">{repository}</span>
          <span className="app-identities__sub">
            {strings.identityBoundSub({
              workflow: registrar.workflowPath,
              ref: registrar.gitRef,
              environment: registrar.environment,
            })}
          </span>
        </td>
        <td>
          {registrar.appCount}
          <span className="app-identities__sub">
            {strings.registrarAppsOf({ limit: registrar.appLimit })}
          </span>
        </td>
        <td>
          <LocalTime timestamp={registrar.approvedAt} />
        </td>
        {compact ? (
          <td className="admin-table__actions admin-table__kebab app-identities__actions-cell">
            <RowActionsMenu
              rowLabel={prefix}
              items={[
                {
                  id: 'delete',
                  label: deleting
                    ? strings.deletingRegistrarNamed({ prefix })
                    : strings.deleteRegistrarNamed({ prefix }),
                  tone: 'danger',
                  disabled: deleting,
                  confirm: {
                    confirmLabel: strings.confirmDeleteRegistrar,
                    confirmAriaLabel: strings.confirmDeleteRegistrarNamed({ prefix }),
                    cancelLabel: strings.cancelAction,
                    cancelAriaLabel: strings.cancelDeleteRegistrarNamed({ prefix }),
                    notice,
                    dialogTitle: strings.deleteRegistrarDialogTitle,
                    dialogItem,
                    onConfirm: () => handleDeleteRegistrar(registrar),
                  },
                },
              ]}
              errors={rowError === undefined ? undefined : [rowError]}
              focusKeys={[`delete-registrar:${id}`]}
              registerControl={registerControl}
            />
          </td>
        ) : (
          <td className="admin-table__actions app-identities__actions-cell">
            <div className="app-identities__actions">
              <ConfirmButton
                triggerLabel={strings.deleteRegistrar}
                triggerAriaLabel={strings.deleteRegistrarNamed({ prefix })}
                confirmLabel={strings.confirmDeleteRegistrar}
                confirmAriaLabel={strings.confirmDeleteRegistrarNamed({ prefix })}
                dismissAriaLabel={strings.cancelDeleteRegistrarNamed({ prefix })}
                className="app-identities__button app-identities__button--danger"
                confirmNotice={notice}
                dialogTitle={strings.deleteRegistrarDialogTitle}
                dialogItem={dialogItem}
                busy={deleting}
                busyLabel={strings.deletingRegistrar}
                busyAriaLabel={strings.deletingRegistrarNamed({ prefix })}
                registerTrigger={(element) =>
                  registerControl({ key: `delete-registrar:${id}`, element })
                }
                onConfirm={() => handleDeleteRegistrar(registrar)}
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
  }

  /** The lead cell of a request row: what the request asks for. */
  function renderRequestLead(request: IdentityRequest): JSX.Element {
    switch (request.kind) {
      case 'app':
        return <span className="app-identities__mono">{request.appId}</span>;
      case 'registrar':
        return (
          <>
            <span className="app-identities__pill app-identities__pill--prefix">
              <span className="visually-hidden">{strings.identityRequestKindLabel}</span>
              {strings.identityRegistrarPill}
            </span>
            <span className="app-identities__sub">{strings.identityRegistrarRequestSub}</span>
          </>
        );
      default:
        return (
          <span className="app-identities__pill app-identities__pill--unknown">
            <span className="visually-hidden">{strings.identityRequestKindLabel}</span>
            {strings.identityUnknownRequestPill}
          </span>
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

  // Only requests an action is offered on count as awaiting approval.
  const pendingCount = requests.filter(
    (candidate) => candidate.status === 'pending' && candidate.kind !== 'unknown',
  ).length;
  const showContent = !loading && error === null;
  const newCount = bindings.filter(isNewBinding).length;
  const ackAllBusy = isBusy('ack-all');
  const ackBusy = bindings.some((candidate) => isBusy(`ack:${candidate.appId}`));
  const ackAllError = rowErrors['ack-all'];

  return (
    <section className="app-identities" aria-labelledby="app-identities-heading">
      <div className="app-identities__head">
        <h2 id="app-identities-heading">{strings.identitiesHeading}</h2>
        <RefreshButton
          label={strings.identitiesRefresh}
          className="app-identities__button app-identities__button--ghost"
          busy={loading}
          onClick={startRefresh}
        />
      </div>
      <p className="app-identities__hint">{strings.identitiesHint}</p>
      {/* Both live regions are always rendered so they exist before their first
          write; an empty one takes no space. Neither is ever focused. */}
      <p role="status" aria-live="polite" className="app-identities__summary">
        {showContent && pendingCount > 0
          ? strings.identitiesPendingSummary({ count: pendingCount })
          : ''}
        {/* The new-apps part updates silently: Mark seen announces through the result region. */}
        {showContent && newCount > 0 && (
          <span aria-live="off">
            {pendingCount > 0 ? ' · ' : ''}
            {strings.identitiesNewAppsSummary({ count: newCount })}
          </span>
        )}
      </p>
      <p
        role="status"
        aria-live="polite"
        className={`app-identities__result${resultState?.kind === 'error' ? ' app-identities__result--error' : ''}`}
      >
        {resultState?.message ?? ''}
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
                      {strings.columnIdentityAsksFor}
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
                      <td className="admin-table__lead" title={requestLabels(request)?.rowLabel}>
                        {renderRequestLead(request)}
                      </td>
                      <td>
                        {renderRequestStatusPill(request)}
                        {compact && request.kind === 'unknown' && (
                          <span className="app-identities__sub">
                            {strings.identityUnknownRequestSub}
                          </span>
                        )}
                        {compact &&
                          request.kind !== 'unknown' &&
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
                        <LocalTime timestamp={request.createdAt} />
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
            id="registrars-heading"
            tabIndex={-1}
            ref={(element) => registerControl({ key: REGISTRARS_HEADING_KEY, element })}
          >
            {strings.identitiesRegistrarsHeading}
          </h3>
          {registrars.length === 0 ? (
            <p className="app-identities__empty">{strings.identitiesRegistrarsEmpty}</p>
          ) : (
            <div
              className="admin-table__scroll app-identities__scroll"
              onFocus={revealFocusedControl}
              role="group"
              aria-labelledby="registrars-heading"
              tabIndex={0}
            >
              <table
                className="admin-table app-identities__table"
                aria-labelledby="registrars-heading"
              >
                <thead>
                  <tr>
                    <th scope="col" className="admin-table__lead">
                      {strings.columnRegistrarPrefix}
                    </th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnRegistrarWorkflow}
                    </th>
                    <th scope="col">{strings.columnRegistrarApps}</th>
                    <th scope="col">{strings.columnRegistrarApproved}</th>
                    <th
                      scope="col"
                      className={`admin-table__actions app-identities__actions-cell${compact ? ' admin-table__kebab' : ''}`}
                      aria-label={strings.columnIdentityActions}
                    />
                  </tr>
                </thead>
                <tbody>{registrars.map((registrar) => renderRegistrarRow(registrar))}</tbody>
              </table>
            </div>
          )}

          <h3
            id="bindings-heading"
            tabIndex={-1}
            ref={(element) => registerControl({ key: BINDINGS_HEADING_KEY, element })}
          >
            {strings.identitiesBindingsHeading}
          </h3>
          {newCount >= 2 && (
            <p className="app-identities__bulk">
              <button
                type="button"
                className="app-identities__button app-identities__button--ghost"
                aria-label={strings.markAllSeenAria}
                aria-disabled={ackAllBusy || ackBusy ? 'true' : undefined}
                onClick={handleMarkAllSeen}
              >
                {ackAllBusy ? strings.markingAllSeen : strings.markAllSeen}
              </button>
            </p>
          )}
          {/* Always mounted (`hidden` while empty, so it is also out of the accessibility tree);
              a bulk failure shows here, never in the load-failure slot (which would unmount the
              tables). */}
          <p
            role="alert"
            className="app-identities__row-error app-identities__row-error--block"
            hidden={ackAllError === undefined}
          >
            {ackAllError}
          </p>
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
                    const isNew = isNewBinding(binding);
                    const registrarPrefix = binding.registrar?.prefix ?? null;
                    const marking = isBusy(`ack:${appId}`);
                    const markDisabled = marking || ackAllBusy || ackBusy;
                    const unbindNotice =
                      registrarPrefix === null
                        ? strings.unbindAppNotice
                        : strings.unbindRegistrarAppNotice({ prefix: registrarPrefix });
                    const menuItems: RowMenuItem[] = [
                      ...(isNew
                        ? [
                            {
                              id: 'mark-seen',
                              label: marking
                                ? strings.markingSeenNamed({ app: appId })
                                : strings.markSeenNamed({ app: appId }),
                              tone: 'default',
                              disabled: markDisabled,
                              onSelect: () => handleMarkSeen(binding),
                            } satisfies RowMenuItem,
                          ]
                        : []),
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
                          notice: unbindNotice,
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
                      <tr
                        key={appId}
                        data-app-id={appId}
                        className={isNew ? 'app-identities__row--new' : undefined}
                      >
                        <td className="admin-table__lead" title={appId}>
                          <span className="app-identities__mono">{appId}</span>
                          {registrarPrefix !== null && (
                            <span className="app-identities__marks">
                              {isNew && (
                                <span className="app-identities__pill app-identities__pill--pending">
                                  <span className="visually-hidden">
                                    {strings.identityStatusLabel}
                                  </span>
                                  {strings.identityNewMark}
                                </span>
                              )}
                              <span className="app-identities__pill app-identities__pill--prefix">
                                <span className="visually-hidden">
                                  {strings.identityCreatedByLabel}
                                </span>
                                {strings.identityViaRegistrar({ prefix: registrarPrefix })}
                              </span>
                            </span>
                          )}
                          <span className="app-identities__sub">
                            {registrarPrefix === null
                              ? strings.identityApprovedSub({
                                  time: formatDateTimeZone({ timestamp: binding.approvedAt }),
                                })
                              : strings.identityCreatedAutomaticallySub({
                                  time: formatDateTimeZone({ timestamp: binding.approvedAt }),
                                })}
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
                              {isNew && (
                                <button
                                  ref={(element) =>
                                    registerControl({ key: `ack:${appId}`, element })
                                  }
                                  type="button"
                                  className="app-identities__button app-identities__button--ghost"
                                  aria-label={
                                    marking
                                      ? strings.markingSeenNamed({ app: appId })
                                      : strings.markSeenNamed({ app: appId })
                                  }
                                  aria-disabled={markDisabled ? 'true' : undefined}
                                  onClick={() => handleMarkSeen(binding)}
                                >
                                  {marking ? strings.markingSeen : strings.markSeen}
                                </button>
                              )}
                              <ConfirmButton
                                triggerLabel={strings.unbindApp}
                                triggerAriaLabel={strings.unbindAppNamed({ app: appId })}
                                confirmLabel={strings.confirmUnbindApp}
                                confirmAriaLabel={strings.confirmUnbindAppNamed({ app: appId })}
                                dismissAriaLabel={strings.cancelUnbindAppNamed({ app: appId })}
                                className="app-identities__button app-identities__button--danger"
                                confirmNotice={unbindNotice}
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
      {/* Always mounted (closed it renders nothing) so its close cleanup reads the
          `restoreFocus` set for that very close. */}
      <Modal
        open={dialogOpen && dialogRequest !== null}
        title={strings.approveRegistrarDialogTitle}
        onDismiss={dismissApproveRegistrarDialog}
        dismissible={!approveRegistrarBusy}
        closeOnHashChange={!approveRegistrarBusy}
        busy={approveRegistrarBusy}
        initialFocusRef={prefixInputRef}
        returnFocusRef={approveReturnRef}
        restoreFocus={dialogRestoreFocus}
        actions={
          <ConfirmActions
            cancel={{
              label: strings.cancelAction,
              disabled: approveRegistrarBusy,
              onClick: dismissApproveRegistrarDialog,
            }}
            confirm={{
              label: approveRegistrarBusy
                ? strings.approvingRegistrar
                : strings.approveRegistrarConfirm,
              tone: 'primary',
              disabled: approveRegistrarBusy,
              onClick: submitApproveRegistrar,
            }}
          />
        }
      >
        {dialogRequest !== null && (
          <>
            <ConfirmItem
              label={strings.dialogItemWorkflow}
              value={strings.dialogWorkflowValue({
                repository: dialogRequest.githubRepository,
                workflowPath: dialogRequest.workflowPath,
              })}
            />
            <div className="app-identities__field">
              <label htmlFor={`${prefixHintId}-input`}>{strings.approveRegistrarPrefixLabel}</label>
              <input
                id={`${prefixHintId}-input`}
                ref={prefixInputRef}
                type="text"
                value={prefixValue}
                readOnly={approveRegistrarBusy}
                autoCapitalize="none"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={prefixError === null ? undefined : true}
                aria-describedby={[
                  prefixHintId,
                  prefixError === null ? null : prefixErrorId,
                  dialogError === null ? null : dialogErrorId,
                ]
                  .filter((id): id is string => id !== null)
                  .join(' ')}
                onChange={(event) => setPrefixValue(event.target.value)}
              />
              <p id={prefixHintId} className="app-identities__field-hint">
                {strings.approveRegistrarPrefixHint}
              </p>
              {prefixError !== null && (
                <p id={prefixErrorId} role="alert" className="app-identities__field-error">
                  {prefixError}
                </p>
              )}
            </div>
            <ConfirmNotice>{strings.approveRegistrarNotice}</ConfirmNotice>
            {dialogError !== null && (
              <p id={dialogErrorId} role="alert" className="app-identities__field-error">
                {dialogError}
              </p>
            )}
          </>
        )}
      </Modal>
    </section>
  );
}

import { useCallback, useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import type {
  CancelScheduledMessageResult,
  DeliveryApiClient,
  ScheduledMessageSummary,
} from '../api/deliveryClient';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { formatRelativeFuture } from './formatRelative';
import { localZoneName } from './formatLocalTime';
import { LocalTime } from './LocalTime';
import { OWNER_ROW_ATTRIBUTE, OWNER_ROW_ID } from './Modal';
import { RefreshButton } from './RefreshButton';
import { messageOf, withoutKey } from './rowState';
import './PendingSends.css';

interface PendingSendsProps {
  client: DeliveryApiClient;
}

const PENDING_LIMIT = 50;
const TICK_MS = 30_000;
// Matches the `data-message-id` attribute rendered on each row.
const ROW_ID_ATTRIBUTE = 'data-message-id';
const NOT_FOUND = 'not_found';

function isDue({ deliverAt, now }: { deliverAt: string; now: number }): boolean {
  return Date.parse(deliverAt) <= now;
}

/** The id of the Pending sends row containing `element`, or null. */
function rowIdOf(element: Element | null): string | null {
  const row = element?.closest(`[${ROW_ID_ATTRIBUTE}]`)?.getAttribute(ROW_ID_ATTRIBUTE);
  if (row !== undefined && row !== null) {
    return row;
  }
  // A confirm dialog is portaled out of its row; it names its owner instead.
  return (
    element
      ?.closest(`[${OWNER_ROW_ATTRIBUTE}="${ROW_ID_ATTRIBUTE}"]`)
      ?.getAttribute(OWNER_ROW_ID) ?? null
  );
}

/** Ids of the rows around `index`: the following rows first, then the preceding ones, nearest first. */
function neighbourIds({
  items,
  index,
}: {
  items: ScheduledMessageSummary[];
  index: number;
}): string[] {
  const following = items.slice(index + 1).map((item) => item.messageId);
  const preceding = items
    .slice(0, index)
    .reverse()
    .map((item) => item.messageId);
  return [...following, ...preceding];
}

function cancelOutcomeCopy({
  result,
  key,
}: {
  result: CancelScheduledMessageResult;
  key: string;
}): string {
  if (result.cancelled) {
    return strings.cancelSendDone({ key });
  }
  if (result.status === NOT_FOUND) {
    return strings.cancelSendGone({ key });
  }
  return strings.cancelSendReleased({ key });
}

/**
 * The Pending sends section of the Delivery tab: the scheduled (not yet
 * released) messages with a per-row two-step cancel. Mounted lazily by the tab
 * shell. Every list load goes through the shared latest-request helper; reloads
 * after a cancel or on the 30s tick are quiet (no loading state) so open
 * confirms, in-flight triggers and the focus handoff survive them.
 */
export function PendingSends({ client }: PendingSendsProps): JSX.Element {
  const [items, setItems] = useState<ScheduledMessageSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [busyIds, setBusyIds] = useState<Record<string, true>>({});
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const [result, setResult] = useState<string | null>(null);

  const itemsRef = useRef<ScheduledMessageSummary[]>([]);
  const loadingRef = useRef(true);
  const busyRef = useRef(new Set<string>());
  // False once unmounted, so a late cancel response starts no list request.
  const mountedRef = useRef(true);
  // The shared useFocusRegistry is not used: Pending sends needs a nearest-neighbour candidate list with a heading fallback, which the hook does not support.
  const triggers = useRef(new Map<string, HTMLButtonElement>());
  const headingRef = useRef<HTMLHeadingElement | null>(null);
  // Candidate rows (nearest first) whose trigger should take focus after the
  // next commit; the heading is the fallback. Always resolved by that commit.
  const focusRequest = useRef<string[] | null>(null);
  const trackList = useLatestRequest();

  const commitItems = useCallback((next: ScheduledMessageSummary[]) => {
    itemsRef.current = next;
    setItems(next);
  }, []);

  const commitLoading = useCallback((next: boolean) => {
    loadingRef.current = next;
    setLoading(next);
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // After every render: hand focus to the first requested neighbour whose
  // trigger is mounted (a dropped row has none), else the heading.
  useEffect(() => {
    const candidates = focusRequest.current;
    if (candidates === null) {
      return;
    }
    focusRequest.current = null;
    for (const id of candidates) {
      const trigger = triggers.current.get(id);
      if (trigger !== undefined && trigger.isConnected) {
        trigger.focus();
        return;
      }
    }
    headingRef.current?.focus();
  });

  // A quiet reload that drops the row holding focus moves focus to a
  // neighbour and announces the release (last outcome wins).
  const reconcileFocus = useCallback((next: ScheduledMessageSummary[]) => {
    const previous = itemsRef.current;
    const focusedId = rowIdOf(document.activeElement);
    if (focusedId === null || next.some((item) => item.messageId === focusedId)) {
      return;
    }
    const index = previous.findIndex((item) => item.messageId === focusedId);
    if (index === -1) {
      return;
    }
    const remaining = new Set(next.map((item) => item.messageId));
    focusRequest.current = neighbourIds({ items: previous, index }).filter((id) =>
      remaining.has(id),
    );
    // A row whose cancel is in flight gets its outcome line from the cancel
    // response, so the release line would only be overwritten (and contradict it).
    if (!busyRef.current.has(focusedId)) {
      setResult(strings.pendingSendReleased({ key: previous[index].idempotencyKey }));
    }
  }, []);

  const loadList = useCallback(
    ({ quiet }: { quiet: boolean }): Promise<void> =>
      trackList(client.listScheduledMessages({ limit: PENDING_LIMIT })).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        // A quiet reload that superseded the initial or Refresh load must
        // still settle the loading state, so it only stays quiet otherwise.
        const stayQuiet = quiet && !loadingRef.current;
        if (outcome.ok) {
          if (stayQuiet) {
            reconcileFocus(outcome.value.items);
          }
          commitItems(outcome.value.items);
          setTotal(outcome.value.total);
          setError(null);
          setNow(Date.now());
        } else if (!stayQuiet) {
          commitItems([]);
          setTotal(0);
          setError(
            messageOf({ rejection: outcome.error, fallback: strings.pendingSendsLoadError }),
          );
        } else {
          return;
        }
        commitLoading(false);
      }),
    [client, trackList, reconcileFocus, commitItems, commitLoading],
  );

  // Initial load.
  useEffect(() => {
    void loadList({ quiet: false });
  }, [loadList]);

  // Re-render the relative times every 30s, and quietly reload while any row
  // is due so released rows leave the list. Runs on a hidden tab too.
  useEffect(() => {
    const timer = setInterval(() => {
      const current = Date.now();
      setNow(current);
      if (itemsRef.current.some((item) => isDue({ deliverAt: item.deliverAt, now: current }))) {
        void loadList({ quiet: true });
      }
    }, TICK_MS);
    return () => {
      clearInterval(timer);
    };
  }, [loadList]);

  function startRefresh() {
    setResult(null);
    commitLoading(true);
    void loadList({ quiet: false });
  }

  function registerTrigger({ id, element }: { id: string; element: HTMLButtonElement | null }) {
    if (element === null) {
      triggers.current.delete(id);
    } else {
      triggers.current.set(id, element);
    }
  }

  function handleCancel(id: string) {
    const index = itemsRef.current.findIndex((item) => item.messageId === id);
    if (busyRef.current.has(id) || index === -1) {
      return;
    }
    const key = itemsRef.current[index].idempotencyKey;
    busyRef.current.add(id);
    setBusyIds((current) => ({ ...current, [id]: true }));
    setRowErrors((current) => withoutKey({ record: current, key: id }));
    client
      .cancelScheduledMessage({ id })
      .then((outcome) => {
        if (!mountedRef.current) {
          return;
        }
        // Hand focus off only when it was on this row (or lost to <body>), so a
        // concurrent cancel cannot steal focus from another row's open confirm.
        const focused = document.activeElement;
        const ownsFocus = focused === null || focused === document.body || rowIdOf(focused) === id;
        const current = itemsRef.current;
        const currentIndex = current.findIndex((item) => item.messageId === id);
        if (ownsFocus && currentIndex !== -1) {
          focusRequest.current = neighbourIds({ items: current, index: currentIndex });
        }
        commitItems(current.filter((item) => item.messageId !== id));
        setTotal((count) => Math.max(0, count - 1));
        setResult(cancelOutcomeCopy({ result: outcome, key }));
        // Reload through the tracked loader so the newest response wins.
        void loadList({ quiet: true });
      })
      .catch((rejection: unknown) => {
        setRowErrors((current) => ({
          ...current,
          [id]: strings.cancelSendFailed({
            message: messageOf({ rejection, fallback: strings.genericError }),
          }),
        }));
      })
      .finally(() => {
        busyRef.current.delete(id);
        setBusyIds((current) => withoutKey({ record: current, key: id }));
      });
  }

  function renderDestinations(item: ScheduledMessageSummary): JSX.Element {
    if (item.recipient) {
      return (
        <>
          <span className="pending-sends__name">{strings.pendingRecipientName}</span>
          <span className="pending-sends__sub">{strings.pendingRecipientDetail}</span>
        </>
      );
    }
    return (
      <>
        {item.destinations.map((destination) => (
          <span key={destination.name}>
            <span className="pending-sends__name">{destination.name}</span>
            <span className="pending-sends__sub">{destination.type}</span>
          </span>
        ))}
      </>
    );
  }

  const showTable = !loading && items.length > 0;

  return (
    <section className="pending-sends" aria-labelledby="pending-sends-heading">
      <div className="pending-sends__head">
        <h2 id="pending-sends-heading" tabIndex={-1} ref={headingRef}>
          {strings.pendingSendsHeading}
        </h2>
        <RefreshButton
          label={strings.pendingSendsRefresh}
          className="pending-sends__button pending-sends__button--ghost"
          busy={loading}
          onClick={startRefresh}
        />
      </div>
      <p className="pending-sends__hint">
        {strings.pendingSendsHint({ limit: PENDING_LIMIT, zone: localZoneName() })}
      </p>
      {!loading && error === null && total > items.length && (
        <p className="pending-sends__hint">
          {strings.pendingSendsTruncated({ shown: items.length, total })}
        </p>
      )}
      <p role="status" className="pending-sends__result">
        {result}
      </p>
      {loading && <p className="pending-sends__status">{strings.pendingSendsLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="pending-sends__status">
          {error}
        </p>
      )}
      {!loading && error === null && items.length === 0 && (
        <p className="pending-sends__status">{strings.pendingSendsEmpty}</p>
      )}
      {showTable && (
        <div className="pending-sends__scroll">
          <table className="pending-sends__table">
            <thead>
              <tr>
                <th scope="col">{strings.columnPendingState}</th>
                <th scope="col" className="pending-sends__fill">
                  {strings.columnLogAppChannel}
                </th>
                <th scope="col">{strings.columnPendingDestinations}</th>
                <th scope="col">{strings.columnPendingDeliverAt}</th>
                <th scope="col">{strings.columnPendingCreated}</th>
                <th
                  scope="col"
                  className="pending-sends__actions-cell"
                  aria-label={strings.columnPendingActions}
                />
              </tr>
            </thead>
            <tbody>
              {items.map((item) => {
                const id = item.messageId;
                const key = item.idempotencyKey;
                const due = isDue({ deliverAt: item.deliverAt, now });
                const rowError = rowErrors[id];
                return (
                  <tr key={id} data-message-id={id}>
                    <td>
                      <span
                        className={`pending-sends__pill pending-sends__pill--${due ? 'due' : 'scheduled'}`}
                      >
                        {due ? strings.pendingStateDue : strings.pendingStateScheduled}
                      </span>
                      {due && (
                        <span className="pending-sends__sub">{strings.pendingReleasing}</span>
                      )}
                    </td>
                    <td className="pending-sends__fill">
                      <span className="pending-sends__mono">{item.appId}</span>
                      {strings.separator}
                      <span className="pending-sends__mono">{item.channel}</span>
                      <span className="pending-sends__sub pending-sends__mono">{key}</span>
                    </td>
                    <td>{renderDestinations(item)}</td>
                    <td>
                      <LocalTime timestamp={item.deliverAt} className="pending-sends__mono" />
                      <span className="pending-sends__sub">
                        {formatRelativeFuture({ deliverAt: item.deliverAt, now })}
                      </span>
                    </td>
                    <td className="pending-sends__mono">
                      <LocalTime timestamp={item.createdAt} />
                    </td>
                    <td className="pending-sends__actions-cell">
                      <div className="pending-sends__actions">
                        <ConfirmButton
                          triggerLabel={strings.cancelSend}
                          triggerAriaLabel={strings.cancelSendNamed({ key })}
                          confirmLabel={strings.confirmCancelSend}
                          confirmAriaLabel={strings.confirmCancelSendNamed({ key })}
                          dismissLabel={strings.keepSend}
                          dismissAriaLabel={strings.keepSendNamed({ key })}
                          className="pending-sends__button pending-sends__button--danger"
                          confirmNotice={strings.cancelSendNotice({ key })}
                          dialogTitle={strings.cancelSendDialogTitle}
                          dialogItem={{ label: strings.dialogItemSend, value: key }}
                          ownerRow={{ attribute: ROW_ID_ATTRIBUTE, id }}
                          busy={busyIds[id] === true}
                          busyLabel={strings.cancellingSend}
                          busyAriaLabel={strings.cancellingSendNamed({ key })}
                          registerTrigger={(element) => registerTrigger({ id, element })}
                          onConfirm={() => handleCancel(id)}
                        />
                        {rowError !== undefined && (
                          <p role="alert" className="pending-sends__row-error">
                            {rowError}
                          </p>
                        )}
                      </div>
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

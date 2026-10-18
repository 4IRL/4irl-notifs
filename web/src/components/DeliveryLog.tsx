import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import type {
  AttemptSummary,
  DeliveryApiClient,
  DeliveryState,
  DeliverySummary,
} from '../api/deliveryClient';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { messageOf, withoutKey } from './rowState';
import './DeliveryLog.css';

interface DeliveryLogProps {
  client: DeliveryApiClient;
}

type AttemptsState =
  | { status: 'loading' }
  | { status: 'error'; message: string }
  | { status: 'ready'; attempts: AttemptSummary[] };

const HEADING_KEY = 'heading';
const COLUMN_COUNT = 7;
const DESTINATION_DELETED = 'destination_deleted';

const FILTERS: { state: DeliveryState; label: string }[] = [
  { state: 'dead', label: strings.deliveryLogFilterDead },
  { state: 'pending', label: strings.deliveryLogFilterPending },
  { state: 'delivered', label: strings.deliveryLogFilterDelivered },
];

const STATE_LABELS: Record<DeliveryState, string> = {
  dead: strings.deliveryStateDead,
  pending: strings.deliveryStatePending,
  delivered: strings.deliveryStateDelivered,
};

function spaced(text: string): string {
  return text.replaceAll('_', ' ');
}

function capitalised(text: string): string {
  const spacedText = spaced(text);
  return spacedText.charAt(0).toUpperCase() + spacedText.slice(1);
}

/** "2026-10-17T08:12:44Z" to "2026-10-17 08:12" (UTC, as the server sends it). */
function formatMinute(timestamp: string): string {
  return timestamp.replace('T', ' ').slice(0, 16);
}

function formatClock(timestamp: string): string {
  return timestamp.slice(11, 19);
}

function shortMessageId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 6)}…${id.slice(-2)}` : id;
}

/**
 * The Delivery Log section of the Delivery tab. Mounted lazily by the tab
 * shell, it owns the state filter, the expanded attempts row and the per-row
 * replay flags; every load goes through the shared latest-request helper.
 */
export function DeliveryLog({ client }: DeliveryLogProps): JSX.Element {
  const [filter, setFilter] = useState<DeliveryState>('dead');
  const [deliveries, setDeliveries] = useState<DeliverySummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<number | null>(null);
  const [attemptsState, setAttemptsState] = useState<AttemptsState>({ status: 'loading' });
  const [busyIds, setBusyIds] = useState<Record<number, true>>({});
  const [rowErrors, setRowErrors] = useState<Record<number, string>>({});
  const [replayResult, setReplayResult] = useState<string | null>(null);

  const busyRef = useRef(new Set<number>());
  const filterRef = useRef(filter);
  const { registerControl, requestFocus } = useFocusRegistry();
  const trackDeliveries = useLatestRequest();
  const trackAttempts = useLatestRequest();

  const loadDeliveries = useCallback(
    (state: DeliveryState): Promise<void> =>
      trackDeliveries(client.listDeliveries({ state })).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        if (outcome.ok) {
          setDeliveries(outcome.value);
          setError(null);
        } else {
          setDeliveries([]);
          setError(messageOf({ rejection: outcome.error, fallback: strings.deliveryLogLoadError }));
        }
        setLoading(false);
      }),
    [client, trackDeliveries],
  );

  // Initial load, and a reload whenever the state filter changes.
  useEffect(() => {
    filterRef.current = filter;
    void loadDeliveries(filter);
  }, [filter, loadDeliveries]);

  function collapseAttempts() {
    // Supersede any attempts load still in flight for the row being collapsed.
    void trackAttempts(Promise.resolve());
    setExpandedId(null);
  }

  function startReload(state: DeliveryState) {
    collapseAttempts();
    setReplayResult(null);
    setLoading(true);
    void loadDeliveries(state);
  }

  function selectFilter(state: DeliveryState) {
    if (state === filter) {
      return;
    }
    collapseAttempts();
    setReplayResult(null);
    setLoading(true);
    setFilter(state);
  }

  function toggleAttempts(id: number) {
    if (expandedId === id) {
      collapseAttempts();
      return;
    }
    setExpandedId(id);
    setAttemptsState({ status: 'loading' });
    void trackAttempts(client.listAttempts({ deliveryId: id })).then((outcome) => {
      if (outcome.stale) {
        return;
      }
      if (outcome.ok) {
        setAttemptsState({ status: 'ready', attempts: outcome.value });
      } else {
        setAttemptsState({
          status: 'error',
          message: messageOf({ rejection: outcome.error, fallback: strings.attemptsLoadError }),
        });
      }
    });
  }

  function handleReplay(id: number) {
    if (busyRef.current.has(id)) {
      return;
    }
    busyRef.current.add(id);
    setBusyIds((current) => ({ ...current, [id]: true }));
    setRowErrors((current) => withoutKey({ record: current, key: id }));
    const index = deliveries.findIndex((delivery) => delivery.id === id);
    const neighbour = deliveries[index + 1] ?? deliveries[index - 1];
    client
      .replayDelivery({ deliveryId: id })
      .then((replayed) => {
        setDeliveries((current) => current.filter((delivery) => delivery.id !== id));
        setExpandedId((current) => (current === id ? null : current));
        setReplayResult(
          strings.replayQueued({
            next: replayed.nextAttemptAt === null ? null : formatClock(replayed.nextAttemptAt),
          }),
        );
        requestFocus(neighbour === undefined ? HEADING_KEY : `attempts:${neighbour.id}`);
        // Reload through the tracked loader so the newest response wins: an
        // earlier in-flight reload can no longer re-insert the replayed row.
        void loadDeliveries(filterRef.current);
      })
      .catch((rejection: unknown) => {
        setRowErrors((current) => ({
          ...current,
          [id]: messageOf({ rejection, fallback: strings.genericError }),
        }));
      })
      .finally(() => {
        busyRef.current.delete(id);
        setBusyIds((current) => withoutKey({ record: current, key: id }));
      });
  }

  function renderAttempts(delivery: DeliverySummary): JSX.Element {
    if (attemptsState.status === 'loading') {
      return <p className="delivery-log__status">{strings.attemptsLoading}</p>;
    }
    if (attemptsState.status === 'error') {
      return (
        <p role="alert" className="delivery-log__status">
          {attemptsState.message}
        </p>
      );
    }
    if (attemptsState.attempts.length === 0) {
      return <p className="delivery-log__status">{strings.attemptsEmpty}</p>;
    }
    return (
      <table
        className="delivery-log__attempts"
        aria-label={strings.attemptsTableLabel({ id: delivery.id })}
      >
        <thead>
          <tr>
            <th scope="col">{strings.columnAttemptNumber}</th>
            <th scope="col">{strings.columnAttemptOutcome}</th>
            <th scope="col">{strings.columnAttemptStatus}</th>
            <th scope="col">{strings.columnAttemptLatency}</th>
            <th scope="col">{strings.columnAttemptStarted}</th>
            <th scope="col">{strings.columnAttemptDetail}</th>
          </tr>
        </thead>
        <tbody>
          {attemptsState.attempts.map((attempt) => (
            <tr key={attempt.attemptNumber}>
              <td>{attempt.attemptNumber}</td>
              <td>
                <span className="delivery-log__pill delivery-log__pill--neutral">
                  {capitalised(attempt.outcome)}
                </span>
              </td>
              <td className="delivery-log__mono">{attempt.statusCode ?? strings.noValue}</td>
              <td className="delivery-log__mono">
                {strings.attemptLatency({ ms: attempt.latencyMs })}
              </td>
              <td className="delivery-log__mono">{formatClock(attempt.startedAt)}</td>
              <td className="delivery-log__mono delivery-log__wrap">
                {attempt.error !== null && <div>{attempt.error}</div>}
                {attempt.responseSnippet !== null && (
                  <pre className="delivery-log__snippet">{attempt.responseSnippet}</pre>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    );
  }

  const showTable = !loading && deliveries.length > 0;

  return (
    <section className="delivery-log" aria-labelledby="delivery-log-heading">
      <div className="delivery-log__head">
        <h2
          id="delivery-log-heading"
          tabIndex={-1}
          ref={(element) => registerControl({ key: HEADING_KEY, element })}
        >
          {strings.deliveryLogHeading}
        </h2>
        <div
          className="delivery-log__filters"
          role="group"
          aria-label={strings.deliveryLogFilterLabel}
        >
          {FILTERS.map(({ state, label }) => (
            <button
              key={state}
              type="button"
              className="delivery-log__button"
              aria-pressed={filter === state}
              onClick={() => selectFilter(state)}
            >
              {label}
            </button>
          ))}
        </div>
        <button type="button" className="delivery-log__button" onClick={() => startReload(filter)}>
          {strings.deliveryLogRefresh}
        </button>
      </div>
      <p className="delivery-log__hint">
        {strings.deliveryLogHint({ state: filter })}
        {filter === 'dead' && ` ${strings.deliveryLogReplayHint}`}
      </p>
      <p role="status" className="delivery-log__result">
        {replayResult}
      </p>
      {loading && <p className="delivery-log__status">{strings.deliveryLogLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="delivery-log__status">
          {error}
        </p>
      )}
      {!loading && error === null && deliveries.length === 0 && (
        <p className="delivery-log__status">{strings.deliveryLogEmpty({ state: filter })}</p>
      )}
      {showTable && (
        <div className="delivery-log__scroll">
          <table className="delivery-log__table">
            <thead>
              <tr>
                <th scope="col">{strings.columnLogState}</th>
                <th scope="col">{strings.columnLogAppChannel}</th>
                <th scope="col">{strings.columnLogDestination}</th>
                <th scope="col">{strings.columnLogAttempts}</th>
                <th scope="col">{strings.columnLogLastStatus}</th>
                <th scope="col">{strings.columnLogUpdated}</th>
                <th scope="col" aria-label={strings.columnLogActions} />
              </tr>
            </thead>
            <tbody>
              {deliveries.map((delivery) => {
                const { id } = delivery;
                const expanded = expandedId === id;
                const rowError = rowErrors[id];
                const replayable =
                  delivery.state === 'dead' && delivery.deadReason !== DESTINATION_DELETED;
                return (
                  <Fragment key={id}>
                    <tr>
                      <td>
                        <span
                          className={`delivery-log__pill delivery-log__pill--${delivery.state}`}
                        >
                          {STATE_LABELS[delivery.state]}
                        </span>
                        {delivery.deadReason !== null && (
                          <span className="delivery-log__sub delivery-log__wrap">
                            {spaced(delivery.deadReason)}
                          </span>
                        )}
                      </td>
                      <td>
                        <span className="delivery-log__mono">{delivery.appId}</span>
                        {strings.separator}
                        <span className="delivery-log__mono">{delivery.channel}</span>
                        <span className="delivery-log__sub delivery-log__mono">
                          {strings.deliveryMessageId({ id: shortMessageId(delivery.messageId) })}
                        </span>
                      </td>
                      <td>
                        <span className="delivery-log__name">{delivery.destination}</span>
                        <span className="delivery-log__sub">{delivery.destinationType}</span>
                      </td>
                      <td>{delivery.attemptCount}</td>
                      <td className="delivery-log__mono">
                        {delivery.lastStatusCode ?? strings.noValue}
                      </td>
                      <td className="delivery-log__mono">{formatMinute(delivery.updatedAt)}</td>
                      <td>
                        <div className="delivery-log__actions">
                          <button
                            ref={(element) => registerControl({ key: `attempts:${id}`, element })}
                            type="button"
                            className="delivery-log__button"
                            aria-expanded={expanded}
                            onClick={() => toggleAttempts(id)}
                          >
                            {expanded ? strings.hideAttempts({ id }) : strings.showAttempts({ id })}
                          </button>
                          {replayable && (
                            <ConfirmButton
                              triggerLabel={strings.replayDelivery({ id })}
                              confirmLabel={strings.confirmReplayDelivery({ id })}
                              className="delivery-log__button"
                              busy={busyIds[id] === true}
                              busyLabel={strings.replayingDelivery}
                              onConfirm={() => handleReplay(id)}
                            />
                          )}
                          {delivery.state === 'dead' && !replayable && (
                            <span className="delivery-log__sub">
                              {strings.destinationWasDeleted}
                            </span>
                          )}
                        </div>
                        {rowError !== undefined && (
                          <p role="alert" className="delivery-log__row-error">
                            {rowError}
                          </p>
                        )}
                      </td>
                    </tr>
                    {expanded && (
                      <tr>
                        <td colSpan={COLUMN_COUNT}>
                          <div className="delivery-log__inset">{renderAttempts(delivery)}</div>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

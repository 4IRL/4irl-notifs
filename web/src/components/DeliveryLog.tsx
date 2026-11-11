import { Fragment, useCallback, useEffect, useId, useRef, useState } from 'react';
import type { JSX } from 'react';

import type {
  AttemptSummary,
  DeliveryApiClient,
  DeliveryState,
  DeliverySummary,
  HeldFailuresList,
} from '../api/deliveryClient';
import { useCompactTable } from '../hooks/useCompactTable';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { ChoiceMenu } from './ChoiceMenu';
import { ConfirmButton } from './ConfirmButton';
import { formatTimeOfDay } from './formatLocalTime';
import { LocalTime } from './LocalTime';
import { RefreshButton } from './RefreshButton';
import { ReleaseFailures } from './ReleaseFailures';
import { messageOf, withoutKey } from './rowState';
import { shortMessageId } from './shortMessageId';
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
const OPTED_OUT = 'opted_out';
const PERSONAL_NAME_PREFIX = '@personal_';

type LogFilter = DeliveryState | 'release_failed';

const RELEASE_FAILED = 'release_failed';

const FILTERS: { value: LogFilter; label: string }[] = [
  { value: 'dead', label: strings.deliveryLogFilterDead },
  { value: 'pending', label: strings.deliveryLogFilterPending },
  { value: 'delivered', label: strings.deliveryLogFilterDelivered },
  { value: RELEASE_FAILED, label: strings.deliveryLogFilterReleaseFailed },
];

const DEFAULT_FILTER: LogFilter = 'dead';

function FunnelGlyph(): JSX.Element {
  return (
    <svg
      className="row-menu__glyph"
      viewBox="0 0 24 24"
      width="20"
      height="20"
      aria-hidden="true"
      focusable="false"
    >
      <path
        d="M3 5h18l-7 8.5V20l-4-2v-4.5z"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  );
}

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

/** `@personal_12` renders as `#12`; an unparseable name falls back to itself. */
function personalEndpointLabel(name: string): string {
  const id = name.startsWith(PERSONAL_NAME_PREFIX) ? name.slice(PERSONAL_NAME_PREFIX.length) : '';
  return /^[1-9][0-9]*$/.test(id) ? strings.deliveryPersonalEndpoint({ id }) : name;
}

function formatClock(timestamp: string): string {
  return formatTimeOfDay({ timestamp, withSeconds: true });
}

/**
 * The Delivery Log section of the Delivery tab. Mounted lazily by the tab
 * shell, it owns the state filter, the expanded attempts row and the per-row
 * replay flags; every load goes through the shared latest-request helper.
 */
export function DeliveryLog({ client }: DeliveryLogProps): JSX.Element {
  const compact = useCompactTable();
  const hintId = useId();
  const [filter, setFilter] = useState<LogFilter>(DEFAULT_FILTER);
  const [deliveries, setDeliveries] = useState<DeliverySummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<number | null>(null);
  const [attemptsState, setAttemptsState] = useState<AttemptsState>({ status: 'loading' });
  const [busyIds, setBusyIds] = useState<Record<number, true>>({});
  const [rowErrors, setRowErrors] = useState<Record<number, string>>({});
  const [replayResult, setReplayResult] = useState<string | null>(null);
  // Release failures have their own state so a late response from the other source can never
  // change the loading, error or empty state of the current filter.
  const [failures, setFailures] = useState<HeldFailuresList | null>(null);
  const [failuresLoading, setFailuresLoading] = useState(false);
  const [failuresUnavailable, setFailuresUnavailable] = useState(false);

  const busyRef = useRef(new Set<number>());
  const filterRef = useRef(filter);
  const { registerControl, requestFocus } = useFocusRegistry();
  const trackDeliveries = useLatestRequest();
  const trackAttempts = useLatestRequest();
  const trackFailures = useLatestRequest();

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

  const loadFailures = useCallback(
    (): Promise<void> =>
      trackFailures(client.listHeldFailures()).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        if (outcome.ok) {
          setFailures(outcome.value);
          setFailuresUnavailable(false);
        } else {
          setFailures(null);
          setFailuresUnavailable(true);
        }
        setFailuresLoading(false);
      }),
    [client, trackFailures],
  );

  // Initial load, and a reload whenever the filter changes.
  useEffect(() => {
    filterRef.current = filter;
    if (filter === RELEASE_FAILED) {
      void loadFailures();
    } else {
      void loadDeliveries(filter);
    }
  }, [filter, loadDeliveries, loadFailures]);

  function beginFailuresLoad() {
    // Supersede any deliveries load, and show only the loading text until the newest settles.
    void trackDeliveries(Promise.resolve());
    setFailures(null);
    setFailuresUnavailable(false);
    setFailuresLoading(true);
  }

  function collapseAttempts() {
    // Supersede any attempts load still in flight for the row being collapsed.
    void trackAttempts(Promise.resolve());
    setExpandedId(null);
  }

  function startReload() {
    collapseAttempts();
    setReplayResult(null);
    if (filter === RELEASE_FAILED) {
      beginFailuresLoad();
      void loadFailures();
      return;
    }
    void trackFailures(Promise.resolve());
    setLoading(true);
    void loadDeliveries(filter);
  }

  function selectFilter(next: LogFilter) {
    if (next === filter) {
      return;
    }
    collapseAttempts();
    setReplayResult(null);
    if (next === RELEASE_FAILED) {
      beginFailuresLoad();
    } else {
      void trackFailures(Promise.resolve());
      setLoading(true);
    }
    setFilter(next);
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
    const startedOn = filterRef.current;
    setBusyIds((current) => ({ ...current, [id]: true }));
    setRowErrors((current) => withoutKey({ record: current, key: id }));
    const index = deliveries.findIndex((delivery) => delivery.id === id);
    const neighbour = deliveries[index + 1] ?? deliveries[index - 1];
    client
      .replayDelivery({ deliveryId: id })
      .then((replayed) => {
        setDeliveries((current) => current.filter((delivery) => delivery.id !== id));
        setExpandedId((current) => (current === id ? null : current));
        // Only the chip the replay started on shows its result and takes focus.
        if (filterRef.current === startedOn) {
          setReplayResult(
            strings.replayQueued({
              next: replayed.nextAttemptAt === null ? null : formatClock(replayed.nextAttemptAt),
            }),
          );
          requestFocus(neighbour === undefined ? HEADING_KEY : `attempts:${neighbour.id}`);
        }
        // Reload through the tracked loader so the newest response wins: an
        // earlier in-flight reload can no longer re-insert the replayed row. The
        // failures view has no replayed rows, so it reloads when re-entered.
        const current = filterRef.current;
        if (current !== RELEASE_FAILED) {
          void loadDeliveries(current);
        }
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
              <td className="delivery-log__mono">
                <LocalTime timestamp={attempt.startedAt} format="timeOfDaySeconds" />
              </td>
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

  const releaseFailedActive = filter === RELEASE_FAILED;
  const showTable = !releaseFailedActive && !loading && deliveries.length > 0;

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
        {!compact && (
          <div
            className="delivery-log__filters"
            role="group"
            aria-label={strings.deliveryLogFilterLabel}
          >
            {FILTERS.map(({ value, label }) => (
              <button
                key={value}
                type="button"
                className="delivery-log__button"
                aria-pressed={filter === value}
                onClick={() => selectFilter(value)}
              >
                {label}
              </button>
            ))}
          </div>
        )}
        <RefreshButton
          label={strings.deliveryLogRefresh}
          className="delivery-log__button"
          busy={releaseFailedActive ? failuresLoading : loading}
          onClick={startReload}
        />
        {compact && (
          <ChoiceMenu
            label={strings.deliveryLogFiltersMenu}
            choices={FILTERS}
            value={filter}
            onSelect={selectFilter}
            active={filter !== DEFAULT_FILTER}
            describedById={hintId}
            icon={<FunnelGlyph />}
          />
        )}
      </div>
      <p id={hintId} className="delivery-log__hint">
        {filter === RELEASE_FAILED ? (
          strings.releaseFailuresHint
        ) : (
          <>
            {strings.deliveryLogHint({ state: filter })}
            {filter === 'dead' && ` ${strings.deliveryLogReplayHint}`}
          </>
        )}
      </p>
      <p role="status" className="delivery-log__result">
        {replayResult}
      </p>
      {releaseFailedActive && (
        <ReleaseFailures
          loading={failuresLoading}
          unavailable={failuresUnavailable}
          failures={failures}
        />
      )}
      {!releaseFailedActive && loading && (
        <p className="delivery-log__status">{strings.deliveryLogLoading}</p>
      )}
      {!releaseFailedActive && !loading && error !== null && (
        <p role="alert" className="delivery-log__status">
          {error}
        </p>
      )}
      {!releaseFailedActive && !loading && error === null && deliveries.length === 0 && (
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
                  delivery.state === 'dead' &&
                  delivery.deadReason !== DESTINATION_DELETED &&
                  delivery.deadReason !== OPTED_OUT;
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
                        {delivery.targetKind === 'personal' ? (
                          <>
                            <span className="delivery-log__pill delivery-log__pill--neutral">
                              {strings.deliveryPersonalPill}
                            </span>{' '}
                            <span className="delivery-log__mono">
                              {personalEndpointLabel(delivery.destination)}
                            </span>
                            <span className="delivery-log__sub">
                              {strings.deliveryPersonalSub({
                                type: capitalised(delivery.destinationType),
                                hash: delivery.personHash,
                              })}
                            </span>
                          </>
                        ) : (
                          <>
                            <span className="delivery-log__name">{delivery.destination}</span>
                            <span className="delivery-log__sub">{delivery.destinationType}</span>
                          </>
                        )}
                      </td>
                      <td>{delivery.attemptCount}</td>
                      <td className="delivery-log__mono">
                        {delivery.lastStatusCode ?? strings.noValue}
                      </td>
                      <td className="delivery-log__mono">
                        <LocalTime timestamp={delivery.updatedAt} />
                      </td>
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
                              dismissAriaLabel={strings.cancelActionNamed({
                                subject: strings.replayDelivery({ id }),
                              })}
                              dialogTitle={strings.replayDeliveryDialogTitle}
                              dialogItem={{ label: strings.dialogItemDelivery, value: `${id}` }}
                              tone="primary"
                              confirmNotice={strings.replayDeliveryNotice}
                              className="delivery-log__button"
                              busy={busyIds[id] === true}
                              busyLabel={strings.replayingDelivery}
                              onConfirm={() => handleReplay(id)}
                            />
                          )}
                          {delivery.state === 'dead' && !replayable && (
                            <span className="delivery-log__sub">
                              {
                                delivery.deadReason === OPTED_OUT
                                  ? strings.personOptedOut
                                  : strings.destinationWasDeleted /* the only other non-replayable reason */
                              }
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

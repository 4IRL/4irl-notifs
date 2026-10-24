import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import type { DeliveryApiClient, HeartbeatCheck } from '../api/deliveryClient';
import { useExclusive } from '../hooks/useExclusive';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useCompactTable } from '../hooks/useCompactTable';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { formatDuration, formatRelativeFuture, formatRelativePast } from './formatRelative';
import { formatMinute } from './formatUtc';
import { HeartbeatForm } from './HeartbeatForm';
import type { HeartbeatAddSubmission, HeartbeatEditSubmission } from './HeartbeatForm';
import { OwnerBadge } from './OwnerBadge';
import { ownerInlineNoteOf, ownerNoteOf } from './ownership';
import { RowActionsMenu } from './RowActionsMenu';
import type { RowMenuItem } from './RowActionsMenu';
import type { DestinationsLoadState } from './RoutesSection';
import { messageOf, withoutKey } from './rowState';
import { useConfirmingRows } from './useConfirmingRows';
import { revealFocusedControl } from './adminTableFocus';
import './adminTable.css';
import './Heartbeats.css';

interface HeartbeatsProps {
  client: DeliveryApiClient;
  /**
   * The destinations list the Destinations section last reported (lifted to App
   * so there is one shared fetch). It gates "Add check" and feeds the form's
   * destination checkboxes, so both track a destination mutation.
   */
  destinationsState: DestinationsLoadState;
}

/** What a row shows; `late` is derived client-side and never stored or alerted on. */
type RowState = 'up' | 'late' | 'down' | 'paused' | 'waiting';

type ToggleKind = 'pause' | 'resume';

const TICK_MS = 30_000;
const MS_PER_SECOND = 1000;
const HEADING_KEY = 'heading';
const ADD_KEY = 'add';
const ADD_HINT_ID = 'heartbeats-add-hint';
// The editor row spans every column of the layout in use, Actions included:
// Check, State, Owner, Last ping, Schedule, Alerts to, Actions when roomy; the narrow layout
// drops Owner.
const WIDE_COLUMN_COUNT = 7;
const COMPACT_COLUMN_COUNT = 6;
// Matches the `data-check-id` attribute rendered on each row.
const ROW_ID_ATTRIBUTE = 'data-check-id';
// "YYYY-MM-DD HH:MM" -> "HH:MM".
const TIME_OF_DAY_START = 11;

function labelOf(check: HeartbeatCheck): string {
  return `${check.appId}/${check.name}`;
}

function isPastDeadline({ check, now }: { check: HeartbeatCheck; now: number }): boolean {
  return now > Date.parse(check.deadlineAt);
}

/** The state a check would show if it were not paused. */
function liveStateOf({ check, now }: { check: HeartbeatCheck; now: number }): RowState {
  switch (check.state) {
    case 'new':
      return 'waiting';
    case 'up':
    case 'late': {
      // The late boundary is anchor + period = deadline - grace. The server's
      // own `late` is never hidden, so a skewed browser clock cannot mask it.
      const lateAfter = Date.parse(check.deadlineAt) - check.graceSeconds * MS_PER_SECOND;
      return check.state === 'late' || now > lateAfter ? 'late' : 'up';
    }
    default:
      // `down`, and fail closed on any state this build does not know.
      return 'down';
  }
}

function rowStateOf({ check, now }: { check: HeartbeatCheck; now: number }): RowState {
  return check.paused ? 'paused' : liveStateOf({ check, now });
}

/**
 * True when the server reports `up` but the local clock says the check has
 * crossed its late boundary: the server will by now report it `late`, so one
 * reload brings the list back in line. A server-reported `late` never counts
 * (the server already knows), and a paused check is never late.
 */
function turnedLateLocally({ check, now }: { check: HeartbeatCheck; now: number }): boolean {
  return !check.paused && check.state === 'up' && liveStateOf({ check, now }) === 'late';
}

/** Identifies one lateness episode: a ping moves `deadline_at`, so it starts a new one. */
function lateKeyOf(check: HeartbeatCheck): string {
  return `${check.id}:${check.deadlineAt}`;
}

/**
 * True when the server may have moved a live check on since the last load: it
 * is Down (it recovers on the server without the list knowing) or past its
 * deadline (about to go, or just gone, Down). The caller's own Late-once rule
 * and the in-flight and visibility guards bound how often this reloads.
 */
function needsReload({ check, now }: { check: HeartbeatCheck; now: number }): boolean {
  if (check.paused) {
    return false;
  }
  return liveStateOf({ check, now }) === 'down' || isPastDeadline({ check, now });
}

/** True when the section is unmounted, inside a `hidden` tab panel, or the browser tab is hidden. */
function isSectionHidden(section: HTMLElement | null): boolean {
  return section === null || document.hidden || section.closest('[hidden]') !== null;
}

function wasLabel(state: RowState): string {
  switch (state) {
    case 'up':
      return strings.heartbeatWasUp;
    case 'late':
      return strings.heartbeatWasLate;
    case 'waiting':
      return strings.heartbeatWasWaiting;
    default:
      return strings.heartbeatWasDown;
  }
}

/** The id of the row containing `element`, or null. */
function rowIdOf(element: Element | null): string | null {
  return element?.closest(`[${ROW_ID_ATTRIBUTE}]`)?.getAttribute(ROW_ID_ATTRIBUTE) ?? null;
}

/** The registry key of a row's first action control (its Pause or Resume). */
function firstControlKey(check: HeartbeatCheck): string {
  return check.paused ? `resume:${check.id}` : `pause:${check.id}`;
}

function renderStateCell({
  check,
  state,
  now,
}: {
  check: HeartbeatCheck;
  state: RowState;
  now: number;
}): JSX.Element {
  switch (state) {
    case 'up':
      return (
        <span className="heartbeats__pill heartbeats__pill--up">{strings.heartbeatStateUp}</span>
      );
    case 'late':
      return (
        <>
          <span className="heartbeats__pill heartbeats__pill--late">
            {strings.heartbeatStateLate}
          </span>
          <span className="heartbeats__sub">
            {strings.heartbeatLateSub({
              relative: formatRelativeFuture({ deliverAt: check.deadlineAt, now }),
            })}
          </span>
        </>
      );
    case 'down': {
      let detail: string | null = null;
      if (check.lastPingKind === 'fail') {
        detail = strings.heartbeatReportedFailure;
      } else if (check.downSince !== null) {
        detail = strings.heartbeatDownSince({
          time: formatMinute(check.downSince).slice(TIME_OF_DAY_START),
        });
      }
      return (
        <>
          <span className="heartbeats__pill heartbeats__pill--down">
            {strings.heartbeatStateDown}
          </span>
          {detail !== null && <span className="heartbeats__sub">{detail}</span>}
        </>
      );
    }
    case 'paused':
      return (
        <>
          <span className="heartbeats__pill heartbeats__pill--neutral">
            {strings.heartbeatStatePaused}
          </span>
          <span className="heartbeats__sub">
            {strings.heartbeatPausedWas({ was: wasLabel(liveStateOf({ check, now })) })}
          </span>
        </>
      );
    default:
      return (
        <>
          <span className="heartbeats__pill heartbeats__pill--neutral">
            {strings.heartbeatStateWaiting}
          </span>
          <span className="heartbeats__sub">{strings.heartbeatWaitingSub}</span>
        </>
      );
  }
}

function renderLastPingCell({
  check,
  state,
  now,
}: {
  check: HeartbeatCheck;
  state: RowState;
  now: number;
}): JSX.Element {
  if (check.lastPingAt === null) {
    return (
      <>
        <span className="heartbeats__mono">{strings.noValue}</span>
        {state === 'waiting' && (
          <span className="heartbeats__sub">
            {strings.heartbeatWaitingDeadline({ deadline: formatMinute(check.deadlineAt) })}
          </span>
        )}
      </>
    );
  }
  const relative = formatRelativePast({ at: check.lastPingAt, now });
  return (
    <>
      <time dateTime={check.lastPingAt} className="heartbeats__mono">
        {formatMinute(check.lastPingAt)}
      </time>
      {check.lastPingKind === 'fail' ? (
        <span className="heartbeats__sub heartbeats__fail">
          {strings.heartbeatFailPing({ relative })}
        </span>
      ) : (
        <span className="heartbeats__sub">{relative}</span>
      )}
    </>
  );
}

interface HeartbeatActionsMenuProps {
  check: HeartbeatCheck;
  isEditing: boolean;
  formLocked: boolean;
  toggleBusy: boolean;
  toggleKind: ToggleKind;
  deleteBusy: boolean;
  ownerNote: string | null;
  rowError: string | undefined;
  registerControl: (params: { key: string; element: HTMLElement | null }) => void;
  onToggleEditor: () => void;
  onToggle: (kind: ToggleKind) => void;
  onDelete: () => void;
}

/** A check's row actions in the narrow layout: the same actions and guards as the inline buttons. */
function HeartbeatActionsMenu({
  check,
  isEditing,
  formLocked,
  toggleBusy,
  toggleKind,
  deleteBusy,
  ownerNote,
  rowError,
  registerControl,
  onToggleEditor,
  onToggle,
  onDelete,
}: HeartbeatActionsMenuProps): JSX.Element {
  const { id } = check;
  const label = labelOf(check);
  const items: RowMenuItem[] = [
    {
      id: 'edit',
      label: isEditing ? strings.closeHeartbeatTrigger : strings.editHeartbeatTrigger,
      ariaLabel: isEditing
        ? strings.closeHeartbeatNamed({ check: label })
        : strings.editHeartbeatNamed({ check: label }),
      expanded: isEditing,
      disabled: formLocked,
      onSelect: onToggleEditor,
    },
  ];
  if (check.paused) {
    const resuming = toggleBusy && toggleKind === 'resume';
    items.push({
      id: 'resume',
      label: resuming ? strings.resumingHeartbeat : strings.resumeHeartbeat,
      ariaLabel: resuming
        ? strings.resumingHeartbeatNamed({ check: label })
        : strings.resumeHeartbeatNamed({ check: label }),
      disabled: toggleBusy,
      onSelect: () => onToggle('resume'),
    });
  } else {
    const resumingElsewhere = toggleKind === 'resume';
    let pauseLabel: string = strings.pauseHeartbeat;
    let pauseAriaLabel = strings.pauseHeartbeatNamed({ check: label });
    if (toggleBusy) {
      pauseLabel = resumingElsewhere ? strings.resumingHeartbeat : strings.pausingHeartbeat;
      pauseAriaLabel = resumingElsewhere
        ? strings.resumingHeartbeatNamed({ check: label })
        : strings.pausingHeartbeatNamed({ check: label });
    }
    items.push({
      id: 'pause',
      label: pauseLabel,
      ariaLabel: pauseAriaLabel,
      disabled: toggleBusy,
      confirm: {
        confirmLabel: strings.confirmPauseHeartbeat,
        confirmAriaLabel: strings.confirmPauseHeartbeatNamed({ check: label }),
        cancelLabel: strings.keepHeartbeatRunning,
        cancelAriaLabel: strings.keepHeartbeatRunningNamed({ check: label }),
        notice:
          ownerNote === null
            ? strings.pauseHeartbeatNotice
            : `${strings.pauseHeartbeatNotice} ${ownerNote}`,
        onConfirm: () => onToggle('pause'),
      },
    });
  }
  items.push({
    id: 'delete',
    label: deleteBusy ? strings.deletingHeartbeat : strings.deleteHeartbeat,
    ariaLabel: deleteBusy
      ? strings.deletingHeartbeatNamed({ check: label })
      : strings.deleteHeartbeatNamed({ check: label }),
    tone: 'danger',
    disabled: deleteBusy,
    confirm: {
      confirmLabel: strings.confirmDeleteHeartbeat,
      confirmAriaLabel: strings.confirmDeleteHeartbeatNamed({ check: label }),
      cancelLabel: strings.cancelAction,
      cancelAriaLabel: strings.cancelActionNamed({
        subject: strings.deleteHeartbeatNamed({ check: label }),
      }),
      notice: ownerNote ?? undefined,
      onConfirm: onDelete,
    },
  });
  return (
    <RowActionsMenu
      rowLabel={label}
      items={items}
      note={ownerInlineNoteOf(check)}
      errors={rowError === undefined ? undefined : [rowError]}
      focusKeys={[`edit:${id}`, `pause:${id}`, `resume:${id}`, `delete:${id}`]}
      registerControl={registerControl}
    />
  );
}

/**
 * The Heartbeats section of the Delivery tab. Reloads after a mutation and on the 30s tick are
 * quiet (no loading state) so open confirms and focus survive them; late is derived locally
 * while the server's state stays authoritative.
 */
export function Heartbeats({ client, destinationsState }: HeartbeatsProps): JSX.Element {
  const compact = useCompactTable();
  const columnCount = compact ? COMPACT_COLUMN_COUNT : WIDE_COLUMN_COUNT;
  const [adding, setAdding] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [addSubmitting, setAddSubmitting] = useState(false);
  // Mirrors `editingId` for async callbacks that outlive the render they started in.
  const editingIdRef = useRef<number | null>(null);
  const [checks, setChecks] = useState<HeartbeatCheck[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [rowErrors, setRowErrors] = useState<Record<number, string>>({});
  const [toggling, setToggling] = useState<Record<number, ToggleKind>>({});
  const [result, setResult] = useState<string | null>(null);

  const checksRef = useRef<HeartbeatCheck[]>([]);
  const loadingRef = useRef(true);
  const sectionRef = useRef<HTMLElement | null>(null);
  // List loads currently awaiting the server; the tick never starts another.
  const inFlightRef = useRef(0);
  // Lateness episodes (`lateKeyOf`) the tick has already reloaded for.
  const lateReloadedRef = useRef<Set<string>>(new Set());
  const { registerControl, requestFocus } = useFocusRegistry();
  const { isBusy, exclusive } = useExclusive<string>();
  const { isSteppedAside, reportConfirming } = useConfirmingRows();
  const trackList = useLatestRequest();

  const commitChecks = useCallback((next: HeartbeatCheck[]) => {
    checksRef.current = next;
    setChecks(next);
  }, []);

  const commitLoading = useCallback((next: boolean) => {
    loadingRef.current = next;
    setLoading(next);
  }, []);

  const loadList = useCallback(
    ({ quiet }: { quiet: boolean }): Promise<void> => {
      inFlightRef.current += 1;
      return trackList(client.listHeartbeats()).then((outcome) => {
        inFlightRef.current -= 1;
        if (outcome.stale) {
          return;
        }
        // A quiet reload that superseded the initial or Refresh load must
        // still settle the loading state, so it only stays quiet otherwise.
        const stayQuiet = quiet && !loadingRef.current;
        if (outcome.ok) {
          commitChecks(outcome.value);
          setError(null);
          setNow(Date.now());
        } else if (!stayQuiet) {
          commitChecks([]);
          setError(messageOf({ rejection: outcome.error, fallback: strings.heartbeatsLoadError }));
        } else {
          return;
        }
        commitLoading(false);
      });
    },
    [client, trackList, commitChecks, commitLoading],
  );

  // Initial load.
  useEffect(() => {
    void loadList({ quiet: false });
  }, [loadList]);

  // Re-render the relative times and the derived late state every 30s. The
  // same tick quietly reloads, never overlapping an in-flight load and never
  // while the section is hidden: once per lateness episode when a live row
  // first turns Late locally, and while a row is Down or past its deadline.
  useEffect(() => {
    const timer = setInterval(() => {
      const current = Date.now();
      setNow(current);
      if (inFlightRef.current > 0 || isSectionHidden(sectionRef.current)) {
        return;
      }
      const latest = checksRef.current;
      const episodes = new Set(latest.map(lateKeyOf));
      // Forget episodes whose row is gone or has since been pinged.
      const handled = new Set([...lateReloadedRef.current].filter((key) => episodes.has(key)));
      lateReloadedRef.current = handled;
      const newlyLate = latest
        .filter((check) => turnedLateLocally({ check, now: current }))
        .map(lateKeyOf)
        .filter((key) => !handled.has(key));
      if (newlyLate.length === 0 && !latest.some((check) => needsReload({ check, now: current }))) {
        return;
      }
      for (const key of newlyLate) {
        handled.add(key);
      }
      void loadList({ quiet: true });
    }, TICK_MS);
    return () => {
      clearInterval(timer);
    };
  }, [loadList]);

  function startRefresh() {
    setResult(null);
    // The loud reload unmounts the table (and the open editor row with it), so
    // close the editor rather than let it re-open with a fresh baseline; one
    // with a save in flight stays, as its error would otherwise be dropped.
    if (!formLocked) {
      setEditor(null);
    }
    commitLoading(true);
    void loadList({ quiet: false });
  }

  function replaceCheck(updated: HeartbeatCheck) {
    commitChecks(checksRef.current.map((check) => (check.id === updated.id ? updated : check)));
  }

  function failRow({ id, message }: { id: number; message: string }) {
    setRowErrors((current) => ({ ...current, [id]: message }));
  }

  /** Hands focus on only when it was on this row (or lost to <body>), so a concurrent action cannot steal it. */
  function requestFocusIfRowOwnsIt({ id, key }: { id: number; key: string }) {
    const focused = document.activeElement;
    if (focused === null || focused === document.body || rowIdOf(focused) === String(id)) {
      requestFocus(key);
    }
  }

  function toggle({ check, kind }: { check: HeartbeatCheck; kind: ToggleKind }) {
    const { id } = check;
    const label = labelOf(check);
    setRowErrors((current) => withoutKey({ record: current, key: id }));
    exclusive({
      key: `toggle:${id}`,
      work: async () => {
        setToggling((current) => ({ ...current, [id]: kind }));
        try {
          const updated =
            kind === 'pause'
              ? await client.pauseHeartbeat({ id })
              : await client.resumeHeartbeat({ id });
          // The row was deleted while this was in flight: nothing to update,
          // announce or focus (its Resume/Pause control will never mount).
          if (!checksRef.current.some((candidate) => candidate.id === id)) {
            return;
          }
          replaceCheck(updated);
          setNow(Date.now());
          setResult(
            kind === 'pause'
              ? strings.heartbeatPaused({ check: label })
              : strings.heartbeatResumed({ check: label }),
          );
          // Pause swaps its trigger for the row's Resume and vice versa.
          requestFocusIfRowOwnsIt({
            id,
            key: kind === 'pause' ? `resume:${id}` : `pause:${id}`,
          });
          void loadList({ quiet: true });
        } catch (rejection: unknown) {
          failRow({
            id,
            message: (kind === 'pause'
              ? strings.heartbeatPauseFailed
              : strings.heartbeatResumeFailed)({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        } finally {
          setToggling((current) => withoutKey({ record: current, key: id }));
        }
      },
    }).catch(() => undefined);
  }

  /** Opens the editor for `id` (or closes it with null); the ref lets late async callbacks check it. */
  function setEditor(id: number | null) {
    editingIdRef.current = id;
    setEditingId(id);
  }

  async function handleAdd(submission: HeartbeatAddSubmission): Promise<void> {
    setAddSubmitting(true);
    let created: HeartbeatCheck;
    try {
      created = await client.createHeartbeat(submission);
    } finally {
      setAddSubmitting(false);
    }
    setAdding(false);
    // Show the new row at once; the quiet reload below puts it in server order.
    if (!checksRef.current.some((candidate) => candidate.id === created.id)) {
      commitChecks([...checksRef.current, created]);
    }
    setNow(Date.now());
    setResult(strings.heartbeatAddedResult({ check: labelOf(created) }));
    requestFocus(ADD_KEY);
    void loadList({ quiet: true });
  }

  async function handleSave({
    id,
    ...changes
  }: { id: number } & HeartbeatEditSubmission): Promise<void> {
    await exclusive({
      key: `save:${id}`,
      work: async () => {
        const updated = await client.updateHeartbeat({
          id,
          periodSeconds: changes.periodSeconds,
          graceSeconds: changes.graceSeconds,
          destinations: changes.destinations,
        });
        // The row was deleted while this was in flight: nothing to update or announce.
        if (!checksRef.current.some((candidate) => candidate.id === id)) {
          return;
        }
        replaceCheck(updated);
        setNow(Date.now());
        setResult(strings.heartbeatSavedResult({ check: labelOf(updated) }));
        void loadList({ quiet: true });
      },
    });
    // Only close (and refocus) the editor this save came from, never one opened since.
    if (editingIdRef.current === id) {
      setEditor(null);
      requestFocus(`edit:${id}`);
    }
  }

  function closeEditor(id: number) {
    setEditor(null);
    requestFocus(`edit:${id}`);
  }

  function toggleEditor(id: number) {
    // A submit in flight owns the open form: leaving it would drop its error.
    if (formLocked) {
      return;
    }
    if (editingId === id) {
      closeEditor(id);
    } else {
      // One open editor at a time across add and edit.
      setAdding(false);
      setEditor(id);
    }
  }

  function handleDelete(check: HeartbeatCheck) {
    const { id } = check;
    const label = labelOf(check);
    setRowErrors((current) => withoutKey({ record: current, key: id }));
    exclusive({
      key: `delete:${id}`,
      work: async () => {
        try {
          await client.deleteHeartbeat({ id });
          if (editingIdRef.current === id) {
            setEditor(null);
          }
          // The neighbour is picked from the list as it is now, not as it was at
          // click time, so its first control's key is one that is registered.
          const latest = checksRef.current;
          const index = latest.findIndex((candidate) => candidate.id === id);
          const neighbour = index === -1 ? undefined : (latest[index + 1] ?? latest[index - 1]);
          commitChecks(latest.filter((candidate) => candidate.id !== id));
          setResult(strings.heartbeatDeleted({ check: label }));
          requestFocusIfRowOwnsIt({
            id,
            key: neighbour === undefined ? HEADING_KEY : firstControlKey(neighbour),
          });
          void loadList({ quiet: true });
        } catch (rejection: unknown) {
          failRow({
            id,
            message: strings.heartbeatDeleteFailed({
              message: messageOf({ rejection, fallback: strings.genericError }),
            }),
          });
        }
      },
    }).catch(() => undefined);
  }

  // A create or save in flight owns its open form until it settles.
  const formLocked = addSubmitting || (editingId !== null && isBusy(`save:${editingId}`));
  const downCount = checks.filter((check) => rowStateOf({ check, now }) === 'down').length;
  const showTable = !loading && checks.length > 0;

  // A check alerts through registered destinations, so Add stays blocked until
  // the shared destinations list has loaded with at least one entry.
  let addHint: string | null = null;
  if (destinationsState.status === 'loading') {
    addHint = strings.addHeartbeatHintLoading;
  } else if (destinationsState.status === 'error') {
    addHint = strings.addHeartbeatHintError;
  } else if (destinationsState.destinations.length === 0) {
    addHint = strings.addHeartbeatHintEmpty;
  }
  const addBlocked = addHint !== null;
  const destinationsReady = destinationsState.status === 'ready';

  return (
    <section ref={sectionRef} className="heartbeats" aria-labelledby="heartbeats-heading">
      <div className="heartbeats__head">
        <h2
          id="heartbeats-heading"
          tabIndex={-1}
          ref={(element) => registerControl({ key: HEADING_KEY, element })}
        >
          {strings.heartbeatsHeading}
        </h2>
        <button
          type="button"
          className="heartbeats__button heartbeats__button--ghost"
          onClick={startRefresh}
        >
          {strings.heartbeatsRefresh}
        </button>
        <button
          ref={(element) => registerControl({ key: ADD_KEY, element })}
          type="button"
          className="heartbeats__button"
          aria-expanded={adding}
          aria-disabled={(addBlocked && !adding) || formLocked ? 'true' : undefined}
          aria-describedby={addBlocked ? ADD_HINT_ID : undefined}
          onClick={() => {
            // Blocked only blocks opening: an open card can still be closed.
            if ((addBlocked && !adding) || formLocked) {
              return;
            }
            if (adding) {
              setAdding(false);
              requestFocus(ADD_KEY);
            } else {
              setEditor(null);
              setAdding(true);
            }
          }}
        >
          {strings.addHeartbeatAction}
        </button>
      </div>
      <p className="heartbeats__hint">{strings.heartbeatsHint}</p>
      {addHint !== null && (
        <p id={ADD_HINT_ID} className="heartbeats__hint">
          {addHint}
        </p>
      )}
      {adding && (
        <HeartbeatForm
          destinations={destinationsState.destinations}
          destinationsReady={destinationsReady}
          onSubmit={handleAdd}
          onClose={() => {
            setAdding(false);
            requestFocus(ADD_KEY);
          }}
        />
      )}
      {/* Both live regions are always rendered so they exist before their first
          write; an empty one takes no space. The summary text only changes when
          the Down count (or the list size) does, so the 30s tick stays silent. */}
      <p role="status" className="heartbeats__summary">
        {checks.length > 0
          ? strings.heartbeatsDownSummary({ down: downCount, total: checks.length })
          : ''}
      </p>
      <p role="status" className="heartbeats__result">
        {result}
      </p>
      {loading && <p className="heartbeats__status">{strings.heartbeatsLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="heartbeats__status">
          {error}
        </p>
      )}
      {!loading && error === null && checks.length === 0 && (
        <p className="heartbeats__status">{strings.heartbeatsEmpty}</p>
      )}
      {showTable && (
        <div
          className="admin-table__scroll heartbeats__scroll"
          onFocus={revealFocusedControl}
          role="group"
          aria-labelledby="heartbeats-heading"
          tabIndex={0}
        >
          <table className="admin-table heartbeats__table">
            <thead>
              <tr>
                <th scope="col" className="admin-table__lead">
                  {strings.columnHeartbeatCheck}
                </th>
                <th scope="col">{strings.columnHeartbeatState}</th>
                {!compact && <th scope="col">{strings.columnOwner}</th>}
                <th scope="col">{strings.columnHeartbeatLastPing}</th>
                <th scope="col">{strings.columnHeartbeatSchedule}</th>
                <th scope="col" className="admin-table__fill">
                  {strings.columnHeartbeatAlertsTo}
                </th>
                <th
                  scope="col"
                  className={`admin-table__actions heartbeats__actions-cell${compact ? ' admin-table__kebab' : ''}`}
                  aria-label={strings.columnHeartbeatActions}
                />
              </tr>
            </thead>
            <tbody>
              {checks.map((check) => {
                const { id } = check;
                const label = labelOf(check);
                const state = rowStateOf({ check, now });
                const rowError = rowErrors[id];
                const toggleBusy = isBusy(`toggle:${id}`);
                const toggleKind: ToggleKind = toggling[id] ?? 'pause';
                const isEditing = editingId === id;
                const ownerNote = ownerNoteOf(check);
                const ownerInlineNote = ownerInlineNoteOf(check);
                // While one of the row's destructive confirms is open, its other buttons step
                // aside so the pair takes their place and the Actions cell never gets wider.
                const rowKey = String(id);
                const steppedAside = isSteppedAside({ rowKey });
                return (
                  <Fragment key={id}>
                    <tr data-check-id={id}>
                      <td
                        className="admin-table__lead heartbeats__check"
                        title={`${check.appId}${strings.heartbeatCheckSeparator}${check.name}`}
                      >
                        <span className="heartbeats__mono">{check.appId}</span>
                        {strings.heartbeatCheckSeparator}
                        <span className="heartbeats__name">{check.name}</span>
                        {compact && (
                          <span className="admin-table__owner-line">
                            <OwnerBadge ownerKind={check.ownerKind} ownerAppId={check.ownerAppId} />
                          </span>
                        )}
                      </td>
                      <td>{renderStateCell({ check, state, now })}</td>
                      {!compact && (
                        <td>
                          <OwnerBadge ownerKind={check.ownerKind} ownerAppId={check.ownerAppId} />
                        </td>
                      )}
                      <td>{renderLastPingCell({ check, state, now })}</td>
                      <td className="heartbeats__mono">
                        {strings.heartbeatEvery({
                          period: formatDuration({ seconds: check.periodSeconds }),
                        })}
                        <span className="heartbeats__sub">
                          {strings.heartbeatGrace({
                            grace: formatDuration({ seconds: check.graceSeconds }),
                          })}
                        </span>
                      </td>
                      <td className="admin-table__fill">
                        {check.destinations.map((destination) => (
                          <span key={destination.name}>
                            <span className="heartbeats__name">{destination.name}</span>
                            <span className="heartbeats__sub">
                              {destination.enabled
                                ? destination.type
                                : `${destination.type} ${strings.routeDestinationDisabledMark}`}
                            </span>
                          </span>
                        ))}
                      </td>
                      {compact ? (
                        <td className="admin-table__actions admin-table__kebab">
                          <HeartbeatActionsMenu
                            check={check}
                            isEditing={isEditing}
                            formLocked={formLocked}
                            toggleBusy={toggleBusy}
                            toggleKind={toggleKind}
                            deleteBusy={isBusy(`delete:${id}`)}
                            ownerNote={ownerNote}
                            rowError={rowError}
                            registerControl={registerControl}
                            onToggleEditor={() => toggleEditor(id)}
                            onToggle={(kind) => toggle({ check, kind })}
                            onDelete={() => handleDelete(check)}
                          />
                        </td>
                      ) : (
                        <td className="admin-table__actions heartbeats__actions-cell">
                          <div className="heartbeats__actions">
                            <button
                              ref={(element) => registerControl({ key: `edit:${id}`, element })}
                              type="button"
                              className="heartbeats__button heartbeats__button--ghost"
                              hidden={steppedAside}
                              aria-expanded={isEditing}
                              aria-disabled={formLocked ? 'true' : undefined}
                              aria-label={
                                isEditing
                                  ? strings.closeHeartbeatNamed({ check: label })
                                  : strings.editHeartbeatNamed({ check: label })
                              }
                              onClick={() => toggleEditor(id)}
                            >
                              {isEditing
                                ? strings.closeHeartbeatTrigger
                                : strings.editHeartbeatTrigger}
                            </button>
                            {check.paused ? (
                              <button
                                ref={(element) => registerControl({ key: `resume:${id}`, element })}
                                type="button"
                                className="heartbeats__button"
                                hidden={steppedAside}
                                aria-label={
                                  toggleBusy && toggleKind === 'resume'
                                    ? strings.resumingHeartbeatNamed({ check: label })
                                    : strings.resumeHeartbeatNamed({ check: label })
                                }
                                aria-disabled={toggleBusy ? 'true' : undefined}
                                onClick={() => {
                                  if (toggleBusy) {
                                    return;
                                  }
                                  toggle({ check, kind: 'resume' });
                                }}
                              >
                                {toggleBusy && toggleKind === 'resume'
                                  ? strings.resumingHeartbeat
                                  : strings.resumeHeartbeat}
                              </button>
                            ) : (
                              <ConfirmButton
                                triggerLabel={strings.pauseHeartbeat}
                                triggerAriaLabel={strings.pauseHeartbeatNamed({ check: label })}
                                confirmLabel={strings.confirmPauseHeartbeat}
                                confirmAriaLabel={strings.confirmPauseHeartbeatNamed({
                                  check: label,
                                })}
                                dismissLabel={strings.keepHeartbeatRunning}
                                dismissAriaLabel={strings.keepHeartbeatRunningNamed({
                                  check: label,
                                })}
                                className="heartbeats__button heartbeats__button--ghost"
                                confirmNotice={
                                  ownerNote === null
                                    ? strings.pauseHeartbeatNotice
                                    : `${strings.pauseHeartbeatNotice} ${ownerNote}`
                                }
                                busy={toggleBusy}
                                busyLabel={
                                  toggleKind === 'resume'
                                    ? strings.resumingHeartbeat
                                    : strings.pausingHeartbeat
                                }
                                busyAriaLabel={
                                  toggleKind === 'resume'
                                    ? strings.resumingHeartbeatNamed({ check: label })
                                    : strings.pausingHeartbeatNamed({ check: label })
                                }
                                hidden={isSteppedAside({ rowKey, control: 'pause' })}
                                onConfirmingChange={(confirming) =>
                                  reportConfirming({ rowKey, control: 'pause', confirming })
                                }
                                registerTrigger={(element) =>
                                  registerControl({ key: `pause:${id}`, element })
                                }
                                onConfirm={() => toggle({ check, kind: 'pause' })}
                              />
                            )}
                            <ConfirmButton
                              triggerLabel={strings.deleteHeartbeat}
                              triggerAriaLabel={strings.deleteHeartbeatNamed({ check: label })}
                              confirmLabel={strings.confirmDeleteHeartbeat}
                              confirmAriaLabel={strings.confirmDeleteHeartbeatNamed({
                                check: label,
                              })}
                              dismissAriaLabel={strings.cancelActionNamed({
                                subject: strings.deleteHeartbeatNamed({ check: label }),
                              })}
                              className="heartbeats__button heartbeats__button--danger"
                              confirmNotice={ownerNote ?? undefined}
                              busy={isBusy(`delete:${id}`)}
                              busyLabel={strings.deletingHeartbeat}
                              busyAriaLabel={strings.deletingHeartbeatNamed({ check: label })}
                              hidden={isSteppedAside({ rowKey, control: 'delete' })}
                              onConfirmingChange={(confirming) =>
                                reportConfirming({ rowKey, control: 'delete', confirming })
                              }
                              registerTrigger={(element) =>
                                registerControl({ key: `delete:${id}`, element })
                              }
                              onConfirm={() => handleDelete(check)}
                            />
                          </div>
                          {ownerNote !== null && (
                            <span className="heartbeats__sub admin-table__aux admin-table__note">
                              {ownerInlineNote}
                            </span>
                          )}
                          {rowError !== undefined && (
                            <p role="alert" className="heartbeats__row-error admin-table__aux">
                              {rowError}
                            </p>
                          )}
                        </td>
                      )}
                    </tr>
                    {isEditing && (
                      <tr>
                        <td colSpan={columnCount} className="admin-table__editor-cell">
                          <div className="admin-table__editor">
                            <HeartbeatForm
                              check={check}
                              destinations={destinationsState.destinations}
                              destinationsReady={destinationsReady}
                              saving={isBusy(`save:${id}`)}
                              onSubmit={(submission) => handleSave({ id, ...submission })}
                              onClose={() => closeEditor(id)}
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
      )}
    </section>
  );
}

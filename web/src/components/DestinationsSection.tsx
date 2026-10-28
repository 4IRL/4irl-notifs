import { useCallback, useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import { ApiError } from '../api/client';
import type {
  DeliveryApiClient,
  DestinationSummary,
  RotateSecretResult,
} from '../api/deliveryClient';
import { useExclusive } from '../hooks/useExclusive';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { AddDestinationForm } from './AddDestinationForm';
import type { AddDestinationSubmission } from './AddDestinationForm';
import { hasUnreadableSecrets } from './destinationFields';
import { DestinationsTable, RevealBox } from './DestinationsTable';
import type { DestinationAction, SecretRevealState, TestResult } from './DestinationsTable';
import { EditDestinationForm } from './EditDestinationForm';
import type { EditDestinationSubmission } from './EditDestinationForm';
import { EditModal } from './EditModal';
import type { ConfirmStep } from './EditModal';
import type { DestinationsLoadState } from './RoutesSection';
import { messageOf, withoutKey } from './rowState';
import './DestinationsTable.css';

interface DestinationsSectionProps {
  client: DeliveryApiClient;
  /** Reports the list and its load status so sibling sections can use it. */
  onLoadStateChange?: (state: DestinationsLoadState) => void;
  /** Called after a destination delete (and the list refresh) settles. */
  onDestinationDeleted?: () => void;
}

const HEADING_KEY = 'heading';
const ADD_KEY = 'add';
const MAX_REFRESH_ATTEMPTS = 3;
// Stamped on the edit dialog so a focused element inside it resolves to its destination row.
const ROW_ID_ATTRIBUTE = 'data-destination-name';

/** A rotated secret waiting for the edit dialog to close before it moves into the row. */
interface PendingReveal {
  name: string;
  reveal: SecretRevealState;
}

/**
 * The Destinations section of the Delivery tab. It is mounted lazily by the
 * tab shell, so it owns (and survives tab switches with) the destinations list,
 * the per-row busy flags, send-test results and the one-time secret reveals —
 * all keyed by destination name so they outlive list refreshes and row/editor
 * remounts. Every failure stays in this section: nothing here touches the
 * shared provisioning error banner.
 */
export function DestinationsSection({
  client,
  onLoadStateChange,
  onDestinationDeleted,
}: DestinationsSectionProps): JSX.Element {
  const [destinations, setDestinations] = useState<DestinationSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const [testResults, setTestResults] = useState<Record<string, TestResult>>({});
  const [reveals, setReveals] = useState<Record<string, SecretRevealState>>({});
  const [editingName, setEditingName] = useState<string | null>(null);
  // The edited destination as it was when the dialog opened: titles the dialog and renders the
  // form after a refresh drops the row.
  const [editSnapshot, setEditSnapshot] = useState<DestinationSummary | null>(null);
  const [formDirty, setFormDirty] = useState(false);
  const [confirmStep, setConfirmStep] = useState<ConfirmStep | null>(null);
  const [pendingReveal, setPendingReveal] = useState<PendingReveal | null>(null);
  // True when the dialog that is closing must not restore focus (a live reveal takes it).
  const [skipReturnFocus, setSkipReturnFocus] = useState(false);
  const [adding, setAdding] = useState(false);
  // Mirrors of state for async callbacks that outlive the render they started in.
  const editingNameRef = useRef<string | null>(null);
  const destinationsRef = useRef<DestinationSummary[]>([]);
  const revealsRef = useRef<Record<string, SecretRevealState>>({});
  const pendingRevealRef = useRef<PendingReveal | null>(null);
  const rotatedInDialogRef = useRef(false);
  // The edit dialog's focus targets: the Edit button or kebab to return to, the field to focus
  // first, and the heading to fall back to.
  const editReturnRef = useRef<HTMLElement | null>(null);
  const firstEditFieldRef = useRef<HTMLInputElement | null>(null);
  const headingRef = useRef<HTMLElement | null>(null);

  const { registerControl, requestFocus, getControl } = useFocusRegistry();
  const { isBusy: isKeyBusy, exclusive: exclusiveByKey } = useExclusive<string>();
  const track = useLatestRequest();

  const refreshDestinations = useCallback(
    /** Resolves true when this request's outcome was applied, false when a newer one superseded it. */
    (): Promise<boolean> =>
      track(client.listDestinations()).then((outcome) => {
        if (outcome.stale) {
          return false;
        }
        if (outcome.ok) {
          destinationsRef.current = outcome.value;
          setDestinations(outcome.value);
          setError(null);
        } else {
          setError(
            outcome.error instanceof ApiError
              ? outcome.error.message
              : strings.destinationsLoadError,
          );
        }
        setLoading(false);
        return true;
      }),
    [client, track],
  );

  /** Updates the reveals through the ref so async callers always start from the latest. */
  const updateReveals = useCallback(
    (update: (current: Record<string, SecretRevealState>) => Record<string, SecretRevealState>) => {
      revealsRef.current = update(revealsRef.current);
      setReveals(revealsRef.current);
    },
    [],
  );

  const queueReveal = useCallback((next: PendingReveal | null) => {
    pendingRevealRef.current = next;
    setPendingReveal(next);
  }, []);

  /** Moves a queued rotate reveal into the reveals (copy and clear are one step). */
  const flushPendingReveal = useCallback(() => {
    const queued = pendingRevealRef.current;
    if (queued === null) {
      return;
    }
    updateReveals((current) => ({ ...current, [queued.name]: queued.reveal }));
    queueReveal(null);
  }, [queueReveal, updateReveals]);

  // Moves a rotated secret into its row once the dialog has closed. This must be a post-commit
  // effect: the row's reveal mounts (and takes focus) only after Modal's cleanup has lifted
  // `inert` from `#root`.
  useEffect(() => {
    if (editingName === null && pendingReveal !== null) {
      flushPendingReveal();
    }
  }, [editingName, pendingReveal, flushPendingReveal]);

  useEffect(() => {
    void refreshDestinations();
  }, [refreshDestinations]);

  useEffect(() => {
    if (onLoadStateChange === undefined) {
      return;
    }
    let status: DestinationsLoadState['status'] = 'ready';
    if (loading) {
      status = 'loading';
    } else if (error !== null) {
      status = 'error';
    }
    onLoadStateChange({ status, destinations });
  }, [loading, error, destinations, onLoadStateChange]);

  function isBusy({ action, name }: { action: DestinationAction; name: string }): boolean {
    return isKeyBusy(`${action}:${name}`);
  }

  /** Runs `work` unless the same action is already in flight on that row. */
  function exclusive<T>({
    action,
    name,
    work,
  }: {
    action: DestinationAction;
    name: string;
    work: () => Promise<T>;
  }): Promise<T> {
    return exclusiveByKey({ key: `${action}:${name}`, work });
  }

  function clearRowError(name: string) {
    setRowErrors((current) => withoutKey({ record: current, key: name }));
  }

  function forgetDestination(name: string) {
    updateReveals((current) => withoutKey({ record: current, key: name }));
    setTestResults((current) => withoutKey({ record: current, key: name }));
    setRowErrors((current) => withoutKey({ record: current, key: name }));
    if (editingNameRef.current === name) {
      setEditor(null);
    }
  }

  function handleSendTest(name: string) {
    exclusive({
      action: 'test',
      name,
      work: async () => {
        try {
          const message = await client.sendTest({ destination: name });
          const first = message.deliveries[0];
          const result: TestResult =
            first === undefined
              ? { kind: 'none' }
              : {
                  kind: 'delivery',
                  state: first.state,
                  statusCode: first.lastStatusCode,
                  deadReason: first.deadReason,
                };
          setTestResults((current) => ({ ...current, [name]: result }));
        } catch (rejection: unknown) {
          setTestResults((current) => ({
            ...current,
            [name]: {
              kind: 'error',
              message: messageOf({ rejection, fallback: strings.genericError }),
            },
          }));
        }
      },
    }).catch(() => undefined);
  }

  function handleSetEnabled({ name, enabled }: { name: string; enabled: boolean }) {
    clearRowError(name);
    exclusive({
      action: 'toggle',
      name,
      work: async () => {
        try {
          await client.updateDestination({ name, enabled });
          await refreshDestinations();
          // Disable keeps the row, so land on its Enable button; Enable swaps
          // that button away, so land on the row's first control.
          requestFocus(enabled ? `first:${name}` : `enable:${name}`);
        } catch (rejection: unknown) {
          setRowErrors((current) => ({
            ...current,
            [name]: messageOf({ rejection, fallback: strings.genericError }),
          }));
        }
      },
    }).catch(() => undefined);
  }

  function handleDelete(name: string) {
    clearRowError(name);
    const index = destinations.findIndex((destination) => destination.name === name);
    const neighbour = destinations[index + 1] ?? destinations[index - 1];
    exclusive({
      action: 'delete',
      name,
      work: async () => {
        try {
          await client.deleteDestination({ name });
          forgetDestination(name);
          await refreshDestinations();
          onDestinationDeleted?.();
          requestFocus(neighbour === undefined ? HEADING_KEY : `first:${neighbour.name}`);
        } catch (rejection: unknown) {
          setRowErrors((current) => ({
            ...current,
            [name]: messageOf({ rejection, fallback: strings.genericError }),
          }));
        }
      },
    }).catch(() => undefined);
  }

  async function handleAdd({ params, generatedSecret }: AddDestinationSubmission): Promise<void> {
    await client.createDestination(params);
    if (generatedSecret !== null) {
      updateReveals((current) => ({
        ...current,
        [params.name]: { secret: generatedSecret, previousExpiresAt: null, source: 'add' },
      }));
    }
    setAdding(false);
    await refreshDestinations();
    // A generated-secret reveal takes focus itself when it appears.
    if (generatedSecret === null) {
      requestFocus(ADD_KEY);
    }
  }

  async function handleSave({
    name,
    config,
    secrets,
  }: { name: string } & EditDestinationSubmission): Promise<void> {
    await exclusive({
      action: 'save',
      name,
      work: async () => {
        await client.updateDestination({ name, config, secrets });
        // A superseded refresh never updates `destinationsRef`; re-read (a few times at most) so
        // the close gate below judges the newest list, not a stale one.
        for (let attempt = 0; attempt < MAX_REFRESH_ATTEMPTS; attempt += 1) {
          if (await refreshDestinations()) {
            break;
          }
        }
      },
    });
    // Close only the dialog this save came from, and only while its destination still exists.
    // Focus return belongs to the dialog's returnFocusRef; a rotate reveal is not cleared.
    if (
      editingNameRef.current === name &&
      destinationsRef.current.some((destination) => destination.name === name)
    ) {
      setEditor(null);
    }
  }

  function handleRotate({
    name,
    overlapHours,
  }: {
    name: string;
    overlapHours: number;
  }): Promise<RotateSecretResult> {
    return exclusive({
      action: 'rotate',
      name,
      work: async () => {
        const result = await client.rotateSecret({ name, overlapHours });
        // Reload through the tracked refresh so the newest response wins: a
        // stale in-flight list response can no longer revert the rotation.
        await refreshDestinations();
        // Queued, not shown: the form's `onRotated` decides whether the dialog closes first.
        queueReveal({
          name,
          reveal: {
            secret: result.signingSecret,
            previousExpiresAt: result.destination.config.previous_secret_expires_at ?? null,
            source: 'rotate',
          },
        });
        return result;
      },
    });
  }

  /**
   * Opens the editor for `name` (or closes it with null) and resets every per-dialog flag. On a
   * close it decides, from the state at that moment, whether the dialog may restore focus: not
   * when a rotate reveal is live (or queued) for the destination, which takes focus itself.
   */
  function setEditor(name: string | null) {
    if (name === null) {
      const closing = editingNameRef.current;
      const liveReveal =
        closing !== null &&
        (revealsRef.current[closing] !== undefined || pendingRevealRef.current?.name === closing);
      setSkipReturnFocus(rotatedInDialogRef.current && liveReveal);
    } else {
      setSkipReturnFocus(false);
    }
    editingNameRef.current = name;
    rotatedInDialogRef.current = false;
    setEditingName(name);
    setFormDirty(false);
    setConfirmStep(null);
    if (name === null) {
      setEditSnapshot(null);
    }
  }

  function openEditor(name: string) {
    const destination = destinations.find((candidate) => candidate.name === name);
    if (destination === undefined || hasUnreadableSecrets(destination.secrets)) {
      return;
    }
    // One editor at a time across add and edit.
    setAdding(false);
    editReturnRef.current = getControl(`edit:${name}`);
    setEditor(name);
    setEditSnapshot(destination);
  }

  function handleRotated({ name, dirty }: { name: string; dirty: boolean }) {
    if (editingNameRef.current !== name) {
      flushPendingReveal();
      return;
    }
    rotatedInDialogRef.current = true;
    if (dirty) {
      // Typed edits stay under the guard; the secret shows inside the dialog meanwhile.
      flushPendingReveal();
    } else {
      // The post-close effect moves the queued reveal into the row.
      setEditor(null);
    }
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

  function dismissReveal(name: string) {
    updateReveals((current) => withoutKey({ record: current, key: name }));
    if (editingNameRef.current === name) {
      // The in-dialog reveal is gone; registry targets sit under the inert `#root`.
      firstEditFieldRef.current?.focus();
      return;
    }
    requestFocus(getControl(`edit:${name}`) === null ? `first:${name}` : `edit:${name}`);
  }

  // The edited destination: live while it exists, else the snapshot taken at open (row is gone).
  const liveEditing = destinations.find((candidate) => candidate.name === editingName);
  const editingDestination = editingName === null ? null : (liveEditing ?? editSnapshot);
  // Only a successful list can say the row is gone: a failed reload empties `destinations` too.
  const rowGone =
    !loading &&
    error === null &&
    editingName !== null &&
    liveEditing === undefined &&
    editSnapshot !== null;
  const editingReveal = editingName === null ? undefined : reveals[editingName];
  const editBusy =
    editingName !== null &&
    (isBusy({ action: 'save', name: editingName }) ||
      isBusy({ action: 'rotate', name: editingName }));

  return (
    <section className="destinations" aria-labelledby="destinations-heading">
      <div className="destinations__head">
        <h2
          id="destinations-heading"
          tabIndex={-1}
          ref={(element) => {
            headingRef.current = element;
            registerControl({ key: HEADING_KEY, element });
          }}
        >
          {strings.destinationsHeading}
        </h2>
        <button
          ref={(element) => registerControl({ key: ADD_KEY, element })}
          type="button"
          className="destinations__button"
          aria-expanded={adding}
          onClick={() => {
            if (adding) {
              setAdding(false);
              requestFocus(ADD_KEY);
            } else {
              setAdding(true);
            }
          }}
        >
          {strings.addDestinationAction}
        </button>
      </div>
      {adding && (
        <AddDestinationForm
          onAdd={handleAdd}
          onCancel={() => {
            setAdding(false);
            requestFocus(ADD_KEY);
          }}
        />
      )}
      {loading && <p className="destinations__status">{strings.destinationsLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="destinations__status">
          {error}
        </p>
      )}
      {!loading && error === null && destinations.length === 0 && (
        <p className="destinations__status">{strings.destinationsEmpty}</p>
      )}
      {!loading && destinations.length > 0 && (
        <DestinationsTable
          destinations={destinations}
          editingName={editingName}
          isBusy={isBusy}
          rowErrors={rowErrors}
          testResults={testResults}
          reveals={reveals}
          registerControl={registerControl}
          onEdit={openEditor}
          onSendTest={handleSendTest}
          onSetEnabled={handleSetEnabled}
          onDelete={handleDelete}
          onDismissReveal={dismissReveal}
        />
      )}
      <EditModal
        open={editingName !== null}
        title={
          editingDestination === null
            ? ''
            : strings.editDestinationNamed({ name: editingDestination.name })
        }
        onClose={() => setEditor(null)}
        dirty={formDirty}
        busy={editBusy}
        returnFocusRef={editReturnRef}
        fallbackFocusRef={headingRef}
        initialFocusRef={firstEditFieldRef}
        confirmStep={confirmStep}
        rowGone={rowGone}
        restoreFocus={!skipReturnFocus}
        ownerRow={
          editingName === null ? undefined : { attribute: ROW_ID_ATTRIBUTE, id: editingName }
        }
      >
        {({ requestClose }) =>
          editingDestination === null ? null : (
            <>
              {editingReveal !== undefined && (
                <RevealBox
                  name={editingDestination.name}
                  reveal={editingReveal}
                  onDismiss={() => dismissReveal(editingDestination.name)}
                />
              )}
              <EditDestinationForm
                key={editingDestination.name}
                destination={editingDestination}
                saving={isBusy({ action: 'save', name: editingDestination.name })}
                rotating={isBusy({ action: 'rotate', name: editingDestination.name })}
                onSave={(submission) =>
                  handleSave({ name: editingDestination.name, ...submission })
                }
                onRotate={(overlapHours) =>
                  handleRotate({ name: editingDestination.name, overlapHours })
                }
                onClose={requestClose}
                onRequestConfirm={handleRequestConfirm}
                onDirtyChange={setFormDirty}
                onRotated={({ dirty }) => handleRotated({ name: editingDestination.name, dirty })}
                firstFieldRef={firstEditFieldRef}
              />
            </>
          )
        }
      </EditModal>
    </section>
  );
}

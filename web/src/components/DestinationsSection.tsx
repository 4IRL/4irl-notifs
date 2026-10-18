import { useCallback, useEffect, useState } from 'react';
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
import { DestinationsTable } from './DestinationsTable';
import type { DestinationAction, SecretRevealState, TestResult } from './DestinationsTable';
import type { EditDestinationSubmission } from './EditDestinationForm';
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
  const [adding, setAdding] = useState(false);

  const { registerControl, requestFocus } = useFocusRegistry();
  const { isBusy: isKeyBusy, exclusive: exclusiveByKey } = useExclusive<string>();
  const track = useLatestRequest();

  const refreshDestinations = useCallback(
    (): Promise<void> =>
      track(client.listDestinations()).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        if (outcome.ok) {
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
      }),
    [client, track],
  );

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
    setReveals((current) => withoutKey({ record: current, key: name }));
    setTestResults((current) => withoutKey({ record: current, key: name }));
    setRowErrors((current) => withoutKey({ record: current, key: name }));
    setEditingName((current) => (current === name ? null : current));
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
      setReveals((current) => ({
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
        await refreshDestinations();
      },
    });
    setEditingName(null);
    setReveals((current) => withoutKey({ record: current, key: name }));
    requestFocus(`edit:${name}`);
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
        setReveals((current) => ({
          ...current,
          [name]: {
            secret: result.signingSecret,
            previousExpiresAt: result.destination.config.previous_secret_expires_at ?? null,
            source: 'rotate',
          },
        }));
        return result;
      },
    });
  }

  function closeEditor(name: string) {
    setEditingName(null);
    setReveals((current) => withoutKey({ record: current, key: name }));
    requestFocus(`edit:${name}`);
  }

  function toggleEditor(name: string) {
    if (editingName === name) {
      closeEditor(name);
      return;
    }
    if (editingName !== null) {
      // Opening another editor closes the current one, clearing its reveal.
      const previous = editingName;
      setReveals((current) => withoutKey({ record: current, key: previous }));
    }
    setEditingName(name);
  }

  function dismissReveal(name: string) {
    setReveals((current) => withoutKey({ record: current, key: name }));
    requestFocus(`first:${name}`);
  }

  return (
    <section className="destinations" aria-labelledby="destinations-heading">
      <div className="destinations__head">
        <h2
          id="destinations-heading"
          tabIndex={-1}
          ref={(element) => registerControl({ key: HEADING_KEY, element })}
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
          onToggleEditor={toggleEditor}
          onCloseEditor={closeEditor}
          onSendTest={handleSendTest}
          onSetEnabled={handleSetEnabled}
          onDelete={handleDelete}
          onDismissReveal={dismissReveal}
          onSave={handleSave}
          onRotate={handleRotate}
        />
      )}
    </section>
  );
}

import { useCallback, useEffect, useState } from 'react';
import type { JSX } from 'react';

import { ApiError } from '../api/client';
import type { DeliveryApiClient, DestinationSummary, RouteSummary } from '../api/deliveryClient';
import { useExclusive } from '../hooks/useExclusive';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { RouteForm } from './RouteForm';
import type { RouteSubmission } from './RouteForm';
import { RoutesTable } from './RoutesTable';
import type { RouteAction } from './RoutesTable';
import { messageOf, withoutKey } from './rowState';
import './RoutesTable.css';

/** The destinations list as the Destinations section last reported it. */
export interface DestinationsLoadState {
  status: 'loading' | 'error' | 'ready';
  destinations: DestinationSummary[];
}

interface RoutesSectionProps {
  client: DeliveryApiClient;
  destinationsState: DestinationsLoadState;
  /** Bumped by the owner whenever routes may have changed server-side (a destination delete). */
  refreshSignal: number;
}

const HEADING_KEY = 'heading';
const ADD_KEY = 'add';
const ADD_HINT_ID = 'routes-add-hint';

/**
 * The Routes section of the Delivery tab. Mounted lazily by the tab shell, it
 * owns the routes list and per-route busy flags; failures stay in this section.
 */
export function RoutesSection({
  client,
  destinationsState,
  refreshSignal,
}: RoutesSectionProps): JSX.Element {
  const [routes, setRoutes] = useState<RouteSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [rowErrors, setRowErrors] = useState<Record<number, string>>({});
  const [editingId, setEditingId] = useState<number | null>(null);
  const [adding, setAdding] = useState(false);

  const { registerControl, requestFocus } = useFocusRegistry();
  const { isBusy: isKeyBusy, exclusive: exclusiveByKey } = useExclusive<string>();
  const track = useLatestRequest();

  const refreshRoutes = useCallback(
    (): Promise<void> =>
      track(client.listRoutes()).then((outcome) => {
        if (outcome.stale) {
          return;
        }
        if (outcome.ok) {
          setRoutes(outcome.value);
          setError(null);
        } else {
          setError(
            outcome.error instanceof ApiError ? outcome.error.message : strings.routesLoadError,
          );
        }
        setLoading(false);
      }),
    [client, track],
  );

  // Initial load, and a refetch whenever the owner bumps the refresh signal.
  useEffect(() => {
    void refreshRoutes();
  }, [refreshRoutes, refreshSignal]);

  function isBusy({ action, id }: { action: RouteAction; id: number }): boolean {
    return isKeyBusy(`${action}:${id}`);
  }

  function exclusive<T>({
    action,
    id,
    work,
  }: {
    action: RouteAction;
    id: number;
    work: () => Promise<T>;
  }): Promise<T> {
    return exclusiveByKey({ key: `${action}:${id}`, work });
  }

  function handleDelete(id: number) {
    setRowErrors((current) => withoutKey({ record: current, key: id }));
    const index = routes.findIndex((route) => route.id === id);
    const neighbour = routes[index + 1] ?? routes[index - 1];
    exclusive({
      action: 'delete',
      id,
      work: async () => {
        try {
          await client.deleteRoute({ id });
          setEditingId((current) => (current === id ? null : current));
          await refreshRoutes();
          requestFocus(neighbour === undefined ? HEADING_KEY : `edit:${neighbour.id}`);
        } catch (rejection: unknown) {
          setRowErrors((current) => ({
            ...current,
            [id]: messageOf({ rejection, fallback: strings.genericError }),
          }));
        }
      },
    }).catch(() => undefined);
  }

  async function handleAdd(submission: RouteSubmission): Promise<void> {
    await client.createRoute({
      appPattern: submission.appPattern ?? '',
      channelPattern: submission.channelPattern ?? '',
      destinations: submission.destinations ?? [],
      enabled: submission.enabled,
    });
    setAdding(false);
    await refreshRoutes();
    requestFocus(ADD_KEY);
  }

  async function handleSave({ id, ...changes }: { id: number } & RouteSubmission): Promise<void> {
    await exclusive({
      action: 'save',
      id,
      work: async () => {
        await client.updateRoute({ id, ...changes });
        await refreshRoutes();
      },
    });
    setEditingId(null);
    requestFocus(`edit:${id}`);
  }

  function closeEditor(id: number) {
    setEditingId(null);
    requestFocus(`edit:${id}`);
  }

  function toggleEditor(id: number) {
    if (editingId === id) {
      closeEditor(id);
    } else {
      setEditingId(id);
    }
  }

  let addHint: string | null = null;
  if (destinationsState.status === 'loading') {
    addHint = strings.addRouteHintLoading;
  } else if (destinationsState.status === 'error') {
    addHint = strings.addRouteHintError;
  } else if (destinationsState.destinations.length === 0) {
    addHint = strings.addRouteHintEmpty;
  }
  const addBlocked = addHint !== null;

  return (
    <section className="routes" aria-labelledby="routes-heading">
      <div className="routes__head">
        <h2
          id="routes-heading"
          tabIndex={-1}
          ref={(element) => registerControl({ key: HEADING_KEY, element })}
        >
          {strings.routesHeading}
        </h2>
        <button
          ref={(element) => registerControl({ key: ADD_KEY, element })}
          type="button"
          className="routes__button"
          aria-expanded={adding}
          aria-disabled={addBlocked ? 'true' : undefined}
          aria-describedby={addBlocked ? ADD_HINT_ID : undefined}
          onClick={() => {
            if (addBlocked) {
              return;
            }
            if (adding) {
              setAdding(false);
              requestFocus(ADD_KEY);
            } else {
              setAdding(true);
            }
          }}
        >
          {strings.addRouteAction}
        </button>
      </div>
      <p className="routes__hint">{strings.routesHint}</p>
      {addHint !== null && (
        <p id={ADD_HINT_ID} className="routes__hint">
          {addHint}
        </p>
      )}
      {adding && (
        <RouteForm
          destinations={destinationsState.destinations}
          onSubmit={handleAdd}
          onClose={() => {
            setAdding(false);
            requestFocus(ADD_KEY);
          }}
        />
      )}
      {loading && <p className="routes__status">{strings.routesLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="routes__status">
          {error}
        </p>
      )}
      {!loading && error === null && routes.length === 0 && (
        <p className="routes__status">{strings.routesEmpty}</p>
      )}
      {!loading && routes.length > 0 && (
        <RoutesTable
          routes={routes}
          destinations={destinationsState.destinations}
          editingId={editingId}
          isBusy={isBusy}
          rowErrors={rowErrors}
          registerControl={registerControl}
          onToggleEditor={toggleEditor}
          onCloseEditor={closeEditor}
          onDelete={handleDelete}
          onSave={handleSave}
        />
      )}
    </section>
  );
}

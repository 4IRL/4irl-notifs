import { useCallback, useEffect, useRef, useState } from 'react';
import type { JSX } from 'react';

import { ApiError } from '../api/client';
import type { DeliveryApiClient, DestinationSummary, RouteSummary } from '../api/deliveryClient';
import { useExclusive } from '../hooks/useExclusive';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { useLatestRequest } from '../hooks/useLatestRequest';
import { strings } from '../strings';
import { EditModal } from './EditModal';
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
const MAX_REFRESH_ATTEMPTS = 3;
const ADD_HINT_ID = 'routes-add-hint';
// Stamped on the edit dialog so a focused element inside it resolves to its route row.
const ROW_ID_ATTRIBUTE = 'data-route-id';

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
  // The edited route as it was when the dialog opened: titles the dialog and renders the form
  // after a refresh drops the row.
  const [editSnapshot, setEditSnapshot] = useState<RouteSummary | null>(null);
  const [dirty, setDirty] = useState(false);
  const [adding, setAdding] = useState(false);
  // Mirrors `editingId` and `routes` for async callbacks that outlive the render they started in.
  const editingIdRef = useRef<number | null>(null);
  const routesRef = useRef<RouteSummary[]>([]);
  // The edit dialog's focus targets: the Edit button or kebab to return to, the field to focus
  // first, and the heading to fall back to.
  const editReturnRef = useRef<HTMLElement | null>(null);
  const firstEditFieldRef = useRef<HTMLInputElement | null>(null);
  const headingRef = useRef<HTMLElement | null>(null);

  const { registerControl, requestFocus, getControl } = useFocusRegistry();
  const { isBusy: isKeyBusy, exclusive: exclusiveByKey } = useExclusive<string>();
  const track = useLatestRequest();

  const refreshRoutes = useCallback(
    /** Resolves true when this request's outcome was applied, false when a newer one superseded it. */
    (): Promise<boolean> =>
      track(client.listRoutes()).then((outcome) => {
        if (outcome.stale) {
          return false;
        }
        if (outcome.ok) {
          routesRef.current = outcome.value;
          setRoutes(outcome.value);
          setError(null);
        } else {
          setError(
            outcome.error instanceof ApiError ? outcome.error.message : strings.routesLoadError,
          );
        }
        setLoading(false);
        return true;
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
    // An edit dialog opened meanwhile owns focus (and `#root` is inert): leave it alone.
    if (editingIdRef.current === null) {
      requestFocus(ADD_KEY);
    }
  }

  async function handleSave({ id, ...changes }: { id: number } & RouteSubmission): Promise<void> {
    await exclusive({
      action: 'save',
      id,
      work: async () => {
        await client.updateRoute({ id, ...changes });
        // A superseded refresh never updates `routesRef`; re-read (a few times at most) so the
        // close gate below judges the newest list, not a stale one.
        for (let attempt = 0; attempt < MAX_REFRESH_ATTEMPTS; attempt += 1) {
          if (await refreshRoutes()) {
            break;
          }
        }
      },
    });
    // Close only the dialog this save came from, and only while its route still exists. Focus
    // return belongs to the dialog's returnFocusRef.
    if (editingIdRef.current === id && routesRef.current.some((route) => route.id === id)) {
      setEditor(null);
    }
  }

  /** Opens the editor for `id` (or closes it with null) and resets `dirty` in the same update. */
  function setEditor(id: number | null) {
    editingIdRef.current = id;
    setEditingId(id);
    setDirty(false);
    if (id === null) {
      setEditSnapshot(null);
    }
  }

  function closeEditor() {
    setEditor(null);
  }

  function openEditor(id: number) {
    const route = routes.find((candidate) => candidate.id === id);
    if (route === undefined) {
      return;
    }
    // One editor at a time across add and edit.
    setAdding(false);
    editReturnRef.current = getControl(`edit:${id}`);
    setEditor(id);
    setEditSnapshot(route);
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

  // The edited route: live while it exists, else the snapshot taken at open (the row is gone).
  const liveEditing = routes.find((candidate) => candidate.id === editingId);
  const editingRoute = editingId === null ? null : (liveEditing ?? editSnapshot);
  // Only a successful list can say the row is gone: a failed reload empties `routes` too.
  const rowGone =
    !loading &&
    error === null &&
    editingId !== null &&
    liveEditing === undefined &&
    editSnapshot !== null;

  return (
    <section className="routes" aria-labelledby="routes-heading">
      <div className="routes__head">
        <h2
          id="routes-heading"
          tabIndex={-1}
          ref={(element) => {
            headingRef.current = element;
            registerControl({ key: HEADING_KEY, element });
          }}
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
          isBusy={isBusy}
          rowErrors={rowErrors}
          registerControl={registerControl}
          onEdit={openEditor}
          onDelete={handleDelete}
        />
      )}
      <EditModal
        open={editingId !== null}
        title={editingRoute === null ? '' : strings.editRoute({ id: editingRoute.id })}
        onClose={closeEditor}
        dirty={dirty}
        busy={editingId !== null && isBusy({ action: 'save', id: editingId })}
        returnFocusRef={editReturnRef}
        fallbackFocusRef={headingRef}
        initialFocusRef={firstEditFieldRef}
        rowGone={rowGone}
        ownerRow={
          editingId === null ? undefined : { attribute: ROW_ID_ATTRIBUTE, id: String(editingId) }
        }
      >
        {({ requestClose }) =>
          editingRoute === null ? null : (
            <RouteForm
              key={editingRoute.id}
              route={editingRoute}
              destinations={destinationsState.destinations}
              saving={isBusy({ action: 'save', id: editingRoute.id })}
              onSubmit={(submission) => handleSave({ id: editingRoute.id, ...submission })}
              onClose={requestClose}
              onDirtyChange={setDirty}
              firstFieldRef={firstEditFieldRef}
            />
          )
        }
      </EditModal>
    </section>
  );
}

import { useEffect, useRef, useState } from 'react';
import type { ChangeEvent, FormEvent, JSX, RefObject } from 'react';

import { ApiError } from '../api/client';
import type { DestinationSummary, RouteSummary } from '../api/deliveryClient';
import { useLowercaseInput } from '../hooks/useLowercaseInput';
import { strings } from '../strings';
import { isValidRouteAppPattern, isValidRouteChannelPattern } from '../validation';
import './DestinationForm.css';
import './RouteForm.css';

const MAX_ROUTE_DESTINATIONS = 20;

/** The fields a route form hands to its owner. For an edit only changed fields are set. */
export interface RouteSubmission {
  appPattern?: string;
  channelPattern?: string;
  destinations?: string[];
  enabled?: boolean;
}

interface RouteFormProps {
  /** The route being edited; absent for the add form. */
  route?: RouteSummary;
  /** Destinations from the owner's last successful load (empty while loading/failed). */
  destinations: DestinationSummary[];
  /** True while an edit Save for this route is in flight. */
  saving?: boolean;
  onSubmit: (submission: RouteSubmission) => Promise<void>;
  /** Cancel (add) or Close (edit, and an untouched Save). */
  onClose: () => void;
  /** Edit only: reports whether any field differs from the baseline (false again on unmount). */
  onDirtyChange?: (dirty: boolean) => void;
  /** Receives the first field; the owner's dialog focuses it. Defaults to a private ref. */
  firstFieldRef?: RefObject<HTMLInputElement | null>;
}

interface DestinationOption {
  name: string;
  disabled: boolean;
}

function sameSet({ left, right }: { left: string[]; right: string[] }): boolean {
  return left.length === right.length && left.every((name) => right.includes(name));
}

/**
 * Add / inline-edit form for one route. The edit baseline is the route as it was
 * when the editor opened (snapshotted in state), so a routes refresh never
 * overwrites typed values; an edit submits only the fields that changed, with
 * `destinations` as the full set when it changed. Pattern inputs are trimmed
 * before validation. Nothing is sent when validation fails.
 */
export function RouteForm({
  route,
  destinations,
  saving = false,
  onSubmit,
  onClose,
  onDirtyChange,
  firstFieldRef: firstFieldRefProp,
}: RouteFormProps): JSX.Element {
  const isEdit = route !== undefined;
  const idSuffix = isEdit ? String(route.id) : 'new';
  const [baseline] = useState(() => ({
    appPattern: route?.appPattern ?? '',
    channelPattern: route?.channelPattern ?? '',
    destinations: route?.destinations ?? [],
    enabled: route?.enabled ?? true,
  }));
  const [appPattern, setAppPattern] = useState(baseline.appPattern);
  const [channelPattern, setChannelPattern] = useState(baseline.channelPattern);
  const appPatternInput = useLowercaseInput({ setValue: setAppPattern });
  const channelPatternInput = useLowercaseInput({ setValue: setChannelPattern });
  const [selected, setSelected] = useState<string[]>(baseline.destinations);
  const [enabled, setEnabled] = useState(baseline.enabled);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const localFirstFieldRef = useRef<HTMLInputElement | null>(null);
  const firstFieldRef = firstFieldRefProp ?? localFirstFieldRef;

  // Add focuses its first field on mount; an edit's dialog focuses it through `initialFocusRef`.
  useEffect(() => {
    if (!isEdit) {
      firstFieldRef.current?.focus();
    }
    // Mount-only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const dirty =
    isEdit &&
    (appPattern.trim() !== baseline.appPattern ||
      channelPattern.trim() !== baseline.channelPattern ||
      !sameSet({ left: selected, right: baseline.destinations }) ||
      enabled !== baseline.enabled);
  const onDirtyChangeRef = useRef(onDirtyChange);
  useEffect(() => {
    onDirtyChangeRef.current = onDirtyChange;
  });
  useEffect(() => {
    onDirtyChangeRef.current?.(dirty);
  }, [dirty]);
  useEffect(
    () => () => {
      onDirtyChangeRef.current?.(false);
    },
    [],
  );

  // Loaded destinations first, then any selected name the list does not know
  // (e.g. a stale reference) so it stays visible and can be unticked.
  const options: DestinationOption[] = destinations.map((destination) => ({
    name: destination.name,
    disabled: !destination.enabled,
  }));
  for (const name of selected) {
    if (!options.some((option) => option.name === name)) {
      options.push({ name, disabled: false });
    }
  }

  function toggleDestination({ name, checked }: { name: string; checked: boolean }) {
    setSelected((current) =>
      checked ? [...current, name] : current.filter((existing) => existing !== name),
    );
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting || saving) {
      return;
    }
    // Add mode, Enter/Save mid-composition: submit what the fields will show.
    const app = isEdit ? appPattern.trim() : appPattern.toLowerCase().trim();
    const channel = isEdit ? channelPattern.trim() : channelPattern.toLowerCase().trim();
    if (!isEdit) {
      if (appPattern.toLowerCase() !== appPattern) {
        setAppPattern(appPattern.toLowerCase());
      }
      if (channelPattern.toLowerCase() !== channelPattern) {
        setChannelPattern(channelPattern.toLowerCase());
      }
    }
    if (!isValidRouteAppPattern(app)) {
      setError(strings.invalidRouteAppPattern);
      return;
    }
    if (!isValidRouteChannelPattern(channel)) {
      setError(strings.invalidRouteChannelPattern);
      return;
    }
    if (selected.length < 1 || selected.length > MAX_ROUTE_DESTINATIONS) {
      setError(strings.routeNeedsDestination);
      return;
    }

    // Selected names in the display order, so the sent set is stable.
    const orderedSelected = options
      .map((option) => option.name)
      .filter((name) => selected.includes(name));

    let submission: RouteSubmission;
    if (isEdit) {
      submission = {};
      if (app !== baseline.appPattern) {
        submission.appPattern = app;
      }
      if (channel !== baseline.channelPattern) {
        submission.channelPattern = channel;
      }
      if (!sameSet({ left: orderedSelected, right: baseline.destinations })) {
        submission.destinations = orderedSelected;
      }
      if (enabled !== baseline.enabled) {
        submission.enabled = enabled;
      }
      if (Object.keys(submission).length === 0) {
        setError(null);
        onClose();
        return;
      }
    } else {
      submission = {
        appPattern: app,
        channelPattern: channel,
        destinations: orderedSelected,
        enabled,
      };
    }

    setError(null);
    setSubmitting(true);
    onSubmit(submission)
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setSubmitting(false);
      });
  }

  const inFlight = submitting || saving;
  let submitLabel: string = strings.addRouteAction;
  if (isEdit) {
    submitLabel = inFlight ? strings.savingRoute : strings.saveRoute({ id: route.id });
  } else if (inFlight) {
    submitLabel = strings.addingRoute;
  }

  const form = (
    <form
      aria-label={isEdit ? undefined : strings.addRouteHeading}
      onSubmit={handleSubmit}
      noValidate
    >
      <div className="destination-form__grid">
        <div className="destination-form__field">
          <label htmlFor={`route-app-${idSuffix}`}>{strings.routeAppLabel}</label>
          <input
            ref={firstFieldRef}
            id={`route-app-${idSuffix}`}
            type="text"
            autoComplete="off"
            value={appPattern}
            {...(isEdit
              ? {
                  onChange: (event: ChangeEvent<HTMLInputElement>) =>
                    setAppPattern(event.target.value),
                }
              : {
                  autoCapitalize: 'none',
                  autoCorrect: 'off',
                  spellCheck: false,
                  ...appPatternInput,
                })}
          />
        </div>
        <div className="destination-form__field">
          <label htmlFor={`route-channel-${idSuffix}`}>{strings.routeChannelLabel}</label>
          <input
            id={`route-channel-${idSuffix}`}
            type="text"
            autoComplete="off"
            value={channelPattern}
            {...(isEdit
              ? {
                  onChange: (event: ChangeEvent<HTMLInputElement>) =>
                    setChannelPattern(event.target.value),
                }
              : {
                  autoCapitalize: 'none',
                  autoCorrect: 'off',
                  spellCheck: false,
                  ...channelPatternInput,
                })}
          />
        </div>
      </div>
      <p className="destination-form__hint">{strings.routePatternHint}</p>
      <fieldset className="route-form__fieldset">
        <legend>{strings.routeDestinationsLegend}</legend>
        <div className="route-form__checks">
          {options.map((option) => (
            <label key={option.name}>
              <input
                type="checkbox"
                checked={selected.includes(option.name)}
                onChange={(event) =>
                  toggleDestination({ name: option.name, checked: event.target.checked })
                }
              />
              {option.name}
              {option.disabled && (
                <span className="route-form__sub"> {strings.routeDestinationDisabledMark}</span>
              )}
            </label>
          ))}
        </div>
      </fieldset>
      <div className="route-form__checks">
        <label>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => setEnabled(event.target.checked)}
          />
          {strings.routeEnabledLabel}
        </label>
      </div>
      <div className="destination-form__actions">
        <button
          type="submit"
          className="destination-form__button destination-form__button--primary"
          aria-disabled={inFlight ? 'true' : undefined}
        >
          {submitLabel}
        </button>
        <button
          type="button"
          className="destination-form__button destination-form__button--ghost"
          onClick={onClose}
        >
          {isEdit ? strings.closeEditAction : strings.cancelAction}
        </button>
      </div>
      {error !== null && (
        <p role="alert" className="destination-form__error">
          {error}
        </p>
      )}
    </form>
  );

  if (isEdit) {
    return <div className="destination-form">{form}</div>;
  }
  return (
    <section className="destination-form" aria-labelledby="add-route-heading">
      <h3 id="add-route-heading">{strings.addRouteHeading}</h3>
      {form}
    </section>
  );
}

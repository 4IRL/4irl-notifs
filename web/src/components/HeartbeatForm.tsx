import { useEffect, useRef, useState } from 'react';
import type { FormEvent, JSX, Ref } from 'react';

import { ApiError } from '../api/client';
import type {
  CreateHeartbeatParams,
  DestinationSummary,
  DestinationType,
  HeartbeatCheck,
  UpdateHeartbeatParams,
} from '../api/deliveryClient';
import { strings } from '../strings';
import {
  HEARTBEAT_GRACE_RANGE_SECONDS,
  HEARTBEAT_PERIOD_RANGE_SECONDS,
  MAX_HEARTBEAT_DESTINATIONS,
  isValidHeartbeatAppId,
  isValidHeartbeatName,
} from '../validation';
import { DURATION_UNITS, splitDuration, toSeconds } from './heartbeatDuration';
import type { DurationInput, DurationUnit } from './heartbeatDuration';
import './DestinationForm.css';
import './RouteForm.css';

/** What the add form hands to its owner: every field of a new check. */
export type HeartbeatAddSubmission = CreateHeartbeatParams;

/** What the edit form hands to its owner: only the changed fields (app and name are immutable). */
export type HeartbeatEditSubmission = Partial<Omit<UpdateHeartbeatParams, 'id'>>;

interface HeartbeatFormBaseProps {
  /** Destinations from the owner's last successful load (empty while loading/failed). */
  destinations: DestinationSummary[];
  /**
   * True once `destinations` is a real loaded list. A selected name the loaded
   * list does not know is only marked "(not found)" then; while the list is
   * loading or failed the check's own embedded destination info is shown instead.
   */
  destinationsReady: boolean;
  /** True while an edit Save for this check is in flight. */
  saving?: boolean;
  /** Cancel (add) or Close (edit, and an untouched Save). */
  onClose: () => void;
}

interface HeartbeatAddFormProps extends HeartbeatFormBaseProps {
  check?: undefined;
  onSubmit: (submission: HeartbeatAddSubmission) => Promise<void>;
}

interface HeartbeatEditFormProps extends HeartbeatFormBaseProps {
  /** The check being edited. */
  check: HeartbeatCheck;
  onSubmit: (submission: HeartbeatEditSubmission) => Promise<void>;
}

type HeartbeatFormProps = HeartbeatAddFormProps | HeartbeatEditFormProps;

interface DestinationOption {
  name: string;
  type: DestinationType | null;
  disabled: boolean;
  notFound: boolean;
}

const DEFAULT_PERIOD: DurationInput = { value: '24', unit: 'hours' };
const DEFAULT_GRACE: DurationInput = { value: '2', unit: 'hours' };

function sameSet({ left, right }: { left: string[]; right: string[] }): boolean {
  return left.length === right.length && left.every((name) => right.includes(name));
}

/** The small text after a destination's name: its type, then "(disabled)" and "(not found)". */
function marksOf(option: DestinationOption): string {
  return [
    option.type,
    option.disabled ? strings.routeDestinationDisabledMark : null,
    option.notFound ? strings.heartbeatDestinationNotFoundMark : null,
  ]
    .filter((part) => part !== null)
    .join(' ');
}

function isUnit(value: string): value is DurationUnit {
  return DURATION_UNITS.some((entry) => entry.unit === value);
}

interface DurationFieldProps {
  id: string;
  label: string;
  unitLabel: string;
  hint: string;
  duration: DurationInput;
  onChange: (next: DurationInput) => void;
  inputRef?: Ref<HTMLInputElement>;
}

function DurationField({
  id,
  label,
  unitLabel,
  hint,
  duration,
  onChange,
  inputRef,
}: DurationFieldProps): JSX.Element {
  return (
    <div className="destination-form__field">
      <label htmlFor={id}>{label}</label>
      <div className="destination-form__row">
        <input
          ref={inputRef}
          id={id}
          type="number"
          step="any"
          inputMode="decimal"
          aria-describedby={`${id}-hint`}
          value={duration.value}
          onChange={(event) => onChange({ ...duration, value: event.target.value })}
        />
        <select
          className="destination-form__select"
          aria-label={unitLabel}
          value={duration.unit}
          onChange={(event) => {
            if (isUnit(event.target.value)) {
              onChange({ ...duration, unit: event.target.value });
            }
          }}
        >
          {DURATION_UNITS.map((entry) => (
            <option key={entry.unit} value={entry.unit}>
              {entry.label}
            </option>
          ))}
        </select>
      </div>
      <span id={`${id}-hint`} className="destination-form__hint">
        {hint}
      </span>
    </div>
  );
}

/**
 * Add / inline-edit form for one heartbeat check. The edit baseline is the check
 * as it was when the editor opened (snapshotted in state), so a list refresh
 * never overwrites typed values; an edit submits only the fields that changed,
 * with `destinations` as the full set when it changed. Validation runs at submit
 * time only, and nothing is sent when it fails. Period and grace are typed as a
 * number plus a unit and converted to seconds.
 */
export function HeartbeatForm({
  check,
  destinations,
  destinationsReady,
  saving = false,
  onSubmit,
  onClose,
}: HeartbeatFormProps): JSX.Element {
  const isEdit = check !== undefined;
  const idSuffix = isEdit ? String(check.id) : 'new';
  const [baseline] = useState(() => ({
    periodSeconds: check?.periodSeconds,
    graceSeconds: check?.graceSeconds,
    destinations: check?.destinations.map((destination) => destination.name) ?? [],
  }));
  const [appId, setAppId] = useState('');
  const [name, setName] = useState('');
  const [period, setPeriod] = useState<DurationInput>(
    check === undefined ? DEFAULT_PERIOD : splitDuration(check.periodSeconds),
  );
  const [grace, setGrace] = useState<DurationInput>(
    check === undefined ? DEFAULT_GRACE : splitDuration(check.graceSeconds),
  );
  const [selected, setSelected] = useState<string[]>(baseline.destinations);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const firstFieldRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    firstFieldRef.current?.focus();
  }, []);

  // Loaded destinations first, then any name the check started with or has
  // selected that the list does not know (a stale reference), so it stays
  // visible, selected, and can be unticked and re-ticked.
  const options: DestinationOption[] = destinations.map((destination) => ({
    name: destination.name,
    type: destination.type,
    disabled: !destination.enabled,
    notFound: false,
  }));
  for (const staleName of [...baseline.destinations, ...selected]) {
    if (options.some((option) => option.name === staleName)) {
      continue;
    }
    const embedded = check?.destinations.find((destination) => destination.name === staleName);
    options.push({
      name: staleName,
      type: embedded?.type ?? null,
      disabled: embedded !== undefined && !embedded.enabled,
      notFound: destinationsReady,
    });
  }

  function toggleDestination({
    destinationName,
    checked,
  }: {
    destinationName: string;
    checked: boolean;
  }) {
    setSelected((current) =>
      checked
        ? [...current, destinationName]
        : current.filter((existing) => existing !== destinationName),
    );
  }

  function validate({
    periodSeconds,
    graceSeconds,
  }: {
    periodSeconds: number | null;
    graceSeconds: number | null;
  }): string | null {
    if (!isEdit && !isValidHeartbeatAppId(appId.trim())) {
      return strings.invalidHeartbeatAppId;
    }
    if (!isEdit && !isValidHeartbeatName(name.trim())) {
      return strings.invalidHeartbeatName;
    }
    if (
      periodSeconds === null ||
      periodSeconds < HEARTBEAT_PERIOD_RANGE_SECONDS.min ||
      periodSeconds > HEARTBEAT_PERIOD_RANGE_SECONDS.max
    ) {
      return strings.invalidHeartbeatPeriod;
    }
    if (
      graceSeconds === null ||
      graceSeconds < HEARTBEAT_GRACE_RANGE_SECONDS.min ||
      graceSeconds > HEARTBEAT_GRACE_RANGE_SECONDS.max
    ) {
      return strings.invalidHeartbeatGrace;
    }
    if (selected.length < 1) {
      return strings.heartbeatNeedsDestination;
    }
    if (selected.length > MAX_HEARTBEAT_DESTINATIONS) {
      return strings.heartbeatTooManyDestinations;
    }
    return null;
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting || saving) {
      return;
    }
    const periodSeconds = toSeconds(period);
    const graceSeconds = toSeconds(grace);
    const invalid = validate({ periodSeconds, graceSeconds });
    if (invalid !== null || periodSeconds === null || graceSeconds === null) {
      setError(invalid ?? strings.genericError);
      return;
    }

    // Selected names in the display order, so the sent set is stable.
    const orderedSelected = options
      .map((option) => option.name)
      .filter((optionName) => selected.includes(optionName));

    let pending: Promise<void>;
    if (check === undefined) {
      pending = onSubmit({
        appId: appId.trim(),
        name: name.trim(),
        periodSeconds,
        graceSeconds,
        destinations: orderedSelected,
      });
    } else {
      const changes: HeartbeatEditSubmission = {};
      if (periodSeconds !== baseline.periodSeconds) {
        changes.periodSeconds = periodSeconds;
      }
      if (graceSeconds !== baseline.graceSeconds) {
        changes.graceSeconds = graceSeconds;
      }
      if (!sameSet({ left: orderedSelected, right: baseline.destinations })) {
        changes.destinations = orderedSelected;
      }
      if (Object.keys(changes).length === 0) {
        setError(null);
        onClose();
        return;
      }
      pending = onSubmit(changes);
    }

    setError(null);
    setSubmitting(true);
    pending
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setSubmitting(false);
      });
  }

  const inFlight = submitting || saving;
  let submitLabel: string = strings.addHeartbeatAction;
  if (isEdit) {
    submitLabel = inFlight ? strings.savingHeartbeat : strings.saveHeartbeat;
  } else if (inFlight) {
    submitLabel = strings.addingHeartbeat;
  }
  const checkLabel = isEdit ? `${check.appId}/${check.name}` : '';

  const form = (
    <form
      aria-label={
        isEdit ? strings.editHeartbeatNamed({ check: checkLabel }) : strings.addHeartbeatHeading
      }
      onSubmit={handleSubmit}
      noValidate
    >
      {isEdit && (
        <p className="destination-form__hint">
          {strings.heartbeatEditingNotice({ check: checkLabel })}
        </p>
      )}
      <div className="destination-form__grid">
        {!isEdit && (
          <>
            <div className="destination-form__field">
              <label htmlFor={`heartbeat-app-${idSuffix}`}>{strings.heartbeatAppIdLabel}</label>
              <input
                ref={firstFieldRef}
                id={`heartbeat-app-${idSuffix}`}
                type="text"
                autoComplete="off"
                value={appId}
                onChange={(event) => setAppId(event.target.value)}
              />
            </div>
            <div className="destination-form__field">
              <label htmlFor={`heartbeat-name-${idSuffix}`}>{strings.heartbeatNameLabel}</label>
              <input
                id={`heartbeat-name-${idSuffix}`}
                type="text"
                autoComplete="off"
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </div>
          </>
        )}
        <DurationField
          id={`heartbeat-period-${idSuffix}`}
          label={strings.heartbeatPeriodLabel}
          unitLabel={strings.heartbeatPeriodUnitLabel}
          hint={strings.heartbeatPeriodHint}
          duration={period}
          onChange={setPeriod}
          inputRef={isEdit ? firstFieldRef : undefined}
        />
        <DurationField
          id={`heartbeat-grace-${idSuffix}`}
          label={strings.heartbeatGraceLabel}
          unitLabel={strings.heartbeatGraceUnitLabel}
          hint={strings.heartbeatGraceHint}
          duration={grace}
          onChange={setGrace}
        />
      </div>
      <fieldset className="route-form__fieldset">
        <legend>{strings.heartbeatDestinationsLegend}</legend>
        <div className="route-form__checks">
          {options.map((option) => (
            <label key={option.name}>
              <input
                type="checkbox"
                checked={selected.includes(option.name)}
                onChange={(event) =>
                  toggleDestination({ destinationName: option.name, checked: event.target.checked })
                }
              />
              {option.name}
              {marksOf(option) !== '' && (
                <span className="route-form__sub">{` ${marksOf(option)}`}</span>
              )}
            </label>
          ))}
        </div>
      </fieldset>
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
          aria-disabled={inFlight ? 'true' : undefined}
          onClick={() => {
            // Leaving mid-request would drop its error (and a late success).
            if (!inFlight) {
              onClose();
            }
          }}
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
    <section className="destination-form" aria-labelledby="add-heartbeat-heading">
      <h3 id="add-heartbeat-heading">{strings.addHeartbeatHeading}</h3>
      {form}
    </section>
  );
}

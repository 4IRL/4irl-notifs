import { useEffect, useRef, useState } from 'react';
import type { FormEvent, JSX } from 'react';

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
  accessCredentialProblem,
  bodyMatchProblem,
  isValidHeartbeatAppId,
  isValidHeartbeatName,
  parseExpectedStatuses,
  probeIntervalProblem,
  probeTimeoutProblem,
  probeUrlProblem,
} from '../validation';
import type { ProbeUrlProblem } from '../validation';
import { DurationField } from './DurationField';
import { HeartbeatProbeFields } from './HeartbeatProbeFields';
import type { ProbeFieldValues } from './HeartbeatProbeFields';
import { ProbeCredentialsField } from './ProbeCredentialsField';
import { describedBy, fieldErrorId, focusFirstInvalid } from './fieldErrors';
import type { FieldErrors } from './fieldErrors';
import { splitDuration, toSeconds } from './heartbeatDuration';
import type { DurationInput } from './heartbeatDuration';
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
const DEFAULT_PROBE: ProbeFieldValues = {
  url: '',
  statuses: '200',
  bodyMatch: '',
  interval: { value: '5', unit: 'minutes' },
  timeout: '5',
};

type CheckKind = 'push' | 'probe';

type FormField =
  | 'appId'
  | 'name'
  | 'period'
  | 'grace'
  | 'url'
  | 'statuses'
  | 'bodyMatch'
  | 'interval'
  | 'timeout'
  | 'clientId'
  | 'clientSecret'
  | 'destinations';

const PUSH_FIELDS: readonly FormField[] = ['period', 'grace'];
const PROBE_FIELDS: readonly FormField[] = [
  'url',
  'statuses',
  'bodyMatch',
  'interval',
  'timeout',
  'clientId',
  'clientSecret',
];

const PROBE_URL_ERRORS: Record<ProbeUrlProblem, string> = {
  empty: strings.invalidProbeUrlEmpty,
  scheme: strings.invalidProbeUrlScheme,
  tooLong: strings.invalidProbeUrlTooLong,
  malformed: strings.invalidProbeUrlMalformed,
};

function withoutFields({
  errors,
  fields,
}: {
  errors: FieldErrors<FormField>;
  fields: readonly FormField[];
}): FieldErrors<FormField> {
  const next = { ...errors };
  for (const field of fields) {
    delete next[field];
  }
  return next;
}

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
  const [kind, setKind] = useState<CheckKind>('push');
  const [probe, setProbe] = useState<ProbeFieldValues>(DEFAULT_PROBE);
  const [clientId, setClientId] = useState('');
  const [clientSecret, setClientSecret] = useState('');
  const [selected, setSelected] = useState<string[]>(baseline.destinations);
  const [fieldErrors, setFieldErrors] = useState<FieldErrors<FormField>>({});
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const firstFieldRef = useRef<HTMLInputElement | null>(null);
  const nameRef = useRef<HTMLInputElement | null>(null);
  const periodRef = useRef<HTMLInputElement | null>(null);
  const graceRef = useRef<HTMLInputElement | null>(null);
  const urlRef = useRef<HTMLInputElement | null>(null);
  const statusesRef = useRef<HTMLInputElement | null>(null);
  const bodyMatchRef = useRef<HTMLInputElement | null>(null);
  const intervalRef = useRef<HTMLInputElement | null>(null);
  const timeoutRef = useRef<HTMLInputElement | null>(null);
  const clientIdRef = useRef<HTMLInputElement | null>(null);
  const clientSecretRef = useRef<HTMLInputElement | null>(null);
  const destinationsRef = useRef<HTMLInputElement | null>(null);
  const isProbeAdd = !isEdit && kind === 'probe';

  useEffect(() => {
    // Add focuses App ID; edit has no App ID input, so it focuses Period.
    (isEdit ? periodRef : firstFieldRef).current?.focus();
    // Mount-only: the target never changes for the life of the form.
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
    clearFieldErrors(['destinations']);
  }

  function clearFieldErrors(fields: readonly FormField[]) {
    setFieldErrors((current) => withoutFields({ errors: current, fields }));
  }

  function changeKind(next: CheckKind) {
    if (submitting || saving || next === kind) {
      return;
    }
    setKind(next);
    if (next === 'push') {
      // A hidden secret must not linger: only the credential pair is dropped.
      setClientId('');
      setClientSecret('');
      clearFieldErrors(PROBE_FIELDS);
    } else {
      clearFieldErrors(PUSH_FIELDS);
    }
  }

  function changeProbe(next: Partial<ProbeFieldValues>) {
    setProbe((current) => ({ ...current, ...next }));
    const touched: FormField[] = [];
    if (next.url !== undefined) touched.push('url');
    if (next.statuses !== undefined) touched.push('statuses');
    if (next.bodyMatch !== undefined) touched.push('bodyMatch');
    if (next.interval !== undefined) touched.push('interval', 'timeout');
    if (next.timeout !== undefined) touched.push('timeout');
    clearFieldErrors(touched);
  }

  function updateText({
    field,
    setter,
    value,
  }: {
    field: FormField;
    setter: (value: string) => void;
    value: string;
  }) {
    setter(value);
    clearFieldErrors([field]);
  }

  function updateDuration({
    field,
    setter,
    value,
  }: {
    field: FormField;
    setter: (value: DurationInput) => void;
    value: DurationInput;
  }) {
    setter(value);
    clearFieldErrors([field]);
  }

  function validatePush({
    periodSeconds,
    graceSeconds,
  }: {
    periodSeconds: number | null;
    graceSeconds: number | null;
  }): FieldErrors<FormField> {
    const errors: FieldErrors<FormField> = {};
    if (
      periodSeconds === null ||
      periodSeconds < HEARTBEAT_PERIOD_RANGE_SECONDS.min ||
      periodSeconds > HEARTBEAT_PERIOD_RANGE_SECONDS.max
    ) {
      errors.period = strings.invalidHeartbeatPeriod;
    }
    if (
      graceSeconds === null ||
      graceSeconds < HEARTBEAT_GRACE_RANGE_SECONDS.min ||
      graceSeconds > HEARTBEAT_GRACE_RANGE_SECONDS.max
    ) {
      errors.grace = strings.invalidHeartbeatGrace;
    }
    return errors;
  }

  function validateProbe({
    intervalSeconds,
    timeoutSeconds,
  }: {
    intervalSeconds: number | null;
    timeoutSeconds: number;
  }): FieldErrors<FormField> {
    const errors: FieldErrors<FormField> = {};
    const urlProblem = probeUrlProblem(probe.url.trim());
    if (urlProblem !== null) {
      errors.url = PROBE_URL_ERRORS[urlProblem];
    }
    if (!parseExpectedStatuses(probe.statuses).ok) {
      errors.statuses = strings.invalidProbeStatuses;
    }
    if (probe.bodyMatch !== '' && bodyMatchProblem(probe.bodyMatch) !== null) {
      errors.bodyMatch = strings.invalidProbeBodyMatch;
    }
    const intervalValid =
      intervalSeconds !== null && probeIntervalProblem(intervalSeconds) === null;
    if (!intervalValid) {
      errors.interval = strings.invalidProbeInterval;
    }
    const timeoutValid = probeTimeoutProblem(timeoutSeconds) === null;
    if (!timeoutValid) {
      errors.timeout = strings.invalidProbeTimeout;
    } else if (intervalValid && timeoutSeconds >= intervalSeconds) {
      errors.timeout = strings.invalidProbeTimeoutVsInterval;
    }
    if (clientId === '' && clientSecret !== '') {
      errors.clientId = strings.invalidProbeCredentialPair;
    } else if (clientId !== '' && clientSecret === '') {
      errors.clientSecret = strings.invalidProbeCredentialPair;
    } else if (clientId !== '') {
      if (accessCredentialProblem(clientId) !== null) {
        errors.clientId = strings.invalidProbeCredential;
      }
      if (accessCredentialProblem(clientSecret) !== null) {
        errors.clientSecret = strings.invalidProbeCredential;
      }
    }
    return errors;
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting || saving) {
      return;
    }
    const periodSeconds = toSeconds(period);
    const graceSeconds = toSeconds(grace);
    const intervalSeconds = toSeconds(probe.interval);
    const timeoutText = probe.timeout.trim();
    const timeoutSeconds = timeoutText === '' ? Number.NaN : Number(timeoutText);

    const errors: FieldErrors<FormField> = {};
    if (!isEdit && !isValidHeartbeatAppId(appId.trim())) {
      errors.appId = strings.invalidHeartbeatAppId;
    }
    if (!isEdit && !isValidHeartbeatName(name.trim())) {
      errors.name = strings.invalidHeartbeatName;
    }
    Object.assign(
      errors,
      isProbeAdd
        ? validateProbe({ intervalSeconds, timeoutSeconds })
        : validatePush({ periodSeconds, graceSeconds }),
    );
    if (selected.length < 1) {
      errors.destinations = strings.heartbeatNeedsDestination;
    } else if (selected.length > MAX_HEARTBEAT_DESTINATIONS) {
      errors.destinations = strings.heartbeatTooManyDestinations;
    }
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) {
      setError(null);
      focusFirstInvalid({
        order: ['appId', 'name', ...(isProbeAdd ? PROBE_FIELDS : PUSH_FIELDS), 'destinations'],
        errors,
        refs: {
          appId: firstFieldRef,
          name: nameRef,
          period: periodRef,
          grace: graceRef,
          url: urlRef,
          statuses: statusesRef,
          bodyMatch: bodyMatchRef,
          interval: intervalRef,
          timeout: timeoutRef,
          clientId: clientIdRef,
          clientSecret: clientSecretRef,
          destinations: destinationsRef,
        },
      });
      return;
    }

    // Selected names in the display order, so the sent set is stable.
    const orderedSelected = options
      .map((option) => option.name)
      .filter((optionName) => selected.includes(optionName));

    let pending: Promise<void>;
    if (check === undefined && isProbeAdd) {
      const statuses = parseExpectedStatuses(probe.statuses);
      if (!statuses.ok || intervalSeconds === null) {
        setError(strings.genericError);
        return;
      }
      pending = onSubmit({
        kind: 'probe',
        appId: appId.trim(),
        name: name.trim(),
        destinations: orderedSelected,
        probe: {
          url: probe.url.trim(),
          expectedStatuses: statuses.statuses,
          bodyMatch: probe.bodyMatch === '' ? null : probe.bodyMatch,
          timeoutSeconds,
          intervalSeconds,
        },
        ...(clientId !== '' ? { accessToken: { clientId, clientSecret } } : {}),
      });
    } else if (check === undefined) {
      if (periodSeconds === null || graceSeconds === null) {
        setError(strings.genericError);
        return;
      }
      pending = onSubmit({
        kind: 'push',
        appId: appId.trim(),
        name: name.trim(),
        periodSeconds,
        graceSeconds,
        destinations: orderedSelected,
      });
    } else if (periodSeconds === null || graceSeconds === null) {
      setError(strings.genericError);
      return;
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
      {!isEdit && (
        <fieldset className="route-form__fieldset">
          <legend>{strings.heartbeatKindLegend}</legend>
          <div className="route-form__checks">
            {(
              [
                { value: 'push', label: strings.heartbeatKindPush },
                { value: 'probe', label: strings.heartbeatKindProbe },
              ] as const
            ).map((option) => (
              <label key={option.value}>
                <input
                  type="radio"
                  name={`heartbeat-kind-${idSuffix}`}
                  value={option.value}
                  checked={kind === option.value}
                  aria-disabled={inFlight ? 'true' : undefined}
                  onChange={() => changeKind(option.value)}
                />
                {option.label}
              </label>
            ))}
          </div>
        </fieldset>
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
                aria-invalid={fieldErrors.appId ? 'true' : undefined}
                aria-describedby={describedBy({
                  fieldId: `heartbeat-app-${idSuffix}`,
                  hasHint: false,
                  error: fieldErrors.appId,
                })}
                onChange={(event) =>
                  updateText({ field: 'appId', setter: setAppId, value: event.target.value })
                }
              />
              {fieldErrors.appId && (
                <span
                  id={fieldErrorId({ fieldId: `heartbeat-app-${idSuffix}` })}
                  className="destination-form__field-error"
                >
                  {fieldErrors.appId}
                </span>
              )}
            </div>
            <div className="destination-form__field">
              <label htmlFor={`heartbeat-name-${idSuffix}`}>{strings.heartbeatNameLabel}</label>
              <input
                ref={nameRef}
                id={`heartbeat-name-${idSuffix}`}
                type="text"
                autoComplete="off"
                value={name}
                aria-invalid={fieldErrors.name ? 'true' : undefined}
                aria-describedby={describedBy({
                  fieldId: `heartbeat-name-${idSuffix}`,
                  hasHint: false,
                  error: fieldErrors.name,
                })}
                onChange={(event) =>
                  updateText({ field: 'name', setter: setName, value: event.target.value })
                }
              />
              {fieldErrors.name && (
                <span
                  id={fieldErrorId({ fieldId: `heartbeat-name-${idSuffix}` })}
                  className="destination-form__field-error"
                >
                  {fieldErrors.name}
                </span>
              )}
            </div>
          </>
        )}
        {!isProbeAdd && (
          <>
            <DurationField
              id={`heartbeat-period-${idSuffix}`}
              label={strings.heartbeatPeriodLabel}
              unitLabel={strings.heartbeatPeriodUnitLabel}
              hint={strings.heartbeatPeriodHint}
              duration={period}
              onChange={(value) => updateDuration({ field: 'period', setter: setPeriod, value })}
              inputRef={periodRef}
              error={fieldErrors.period}
            />
            <DurationField
              id={`heartbeat-grace-${idSuffix}`}
              label={strings.heartbeatGraceLabel}
              unitLabel={strings.heartbeatGraceUnitLabel}
              hint={strings.heartbeatGraceHint}
              duration={grace}
              onChange={(value) => updateDuration({ field: 'grace', setter: setGrace, value })}
              inputRef={graceRef}
              error={fieldErrors.grace}
            />
          </>
        )}
      </div>
      {isProbeAdd && (
        <>
          <HeartbeatProbeFields
            idSuffix={idSuffix}
            values={probe}
            errors={fieldErrors}
            refs={{
              url: urlRef,
              statuses: statusesRef,
              bodyMatch: bodyMatchRef,
              interval: intervalRef,
              timeout: timeoutRef,
            }}
            onChange={changeProbe}
            intervalHint={strings.heartbeatProbeIntervalHintAdd}
          />
          <ProbeCredentialsField
            idSuffix={idSuffix}
            clientId={clientId}
            clientSecret={clientSecret}
            onClientIdChange={(value) =>
              updateText({ field: 'clientId', setter: setClientId, value })
            }
            onClientSecretChange={(value) =>
              updateText({ field: 'clientSecret', setter: setClientSecret, value })
            }
            clientIdRef={clientIdRef}
            clientSecretRef={clientSecretRef}
            clientIdError={fieldErrors.clientId}
            clientSecretError={fieldErrors.clientSecret}
            hint={strings.heartbeatCredentialsHintAdd}
            inFlight={inFlight}
          />
        </>
      )}
      <fieldset
        className="route-form__fieldset"
        aria-describedby={
          fieldErrors.destinations
            ? fieldErrorId({ fieldId: `heartbeat-destinations-${idSuffix}` })
            : undefined
        }
      >
        <legend>{strings.heartbeatDestinationsLegend}</legend>
        <div className="route-form__checks">
          {options.map((option, index) => (
            <label key={option.name}>
              <input
                ref={index === 0 ? destinationsRef : undefined}
                type="checkbox"
                checked={selected.includes(option.name)}
                aria-invalid={fieldErrors.destinations ? 'true' : undefined}
                aria-describedby={
                  fieldErrors.destinations
                    ? fieldErrorId({ fieldId: `heartbeat-destinations-${idSuffix}` })
                    : undefined
                }
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
      {fieldErrors.destinations && (
        <span
          id={fieldErrorId({ fieldId: `heartbeat-destinations-${idSuffix}` })}
          className="destination-form__field-error"
        >
          {fieldErrors.destinations}
        </span>
      )}
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

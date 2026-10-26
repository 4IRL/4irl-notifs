import type { JSX, Ref } from 'react';

import { strings } from '../strings';
import { DurationField } from './DurationField';
import { describedBy, fieldErrorId } from './fieldErrors';
import type { DurationInput } from './heartbeatDuration';
import './DestinationForm.css';
import './RouteForm.css';

/** The probe inputs the user types, held by the form. */
export interface ProbeFieldValues {
  url: string;
  statuses: string;
  bodyMatch: string;
  interval: DurationInput;
  timeout: string;
}

export interface ProbeFieldErrors {
  url?: string;
  statuses?: string;
  bodyMatch?: string;
  interval?: string;
  timeout?: string;
}

interface ProbeFieldRefs {
  url: Ref<HTMLInputElement>;
  statuses: Ref<HTMLInputElement>;
  bodyMatch: Ref<HTMLInputElement>;
  interval: Ref<HTMLInputElement>;
  timeout: Ref<HTMLInputElement>;
}

interface HeartbeatProbeFieldsProps {
  idSuffix: string;
  values: ProbeFieldValues;
  errors: ProbeFieldErrors;
  refs: ProbeFieldRefs;
  onChange: (next: Partial<ProbeFieldValues>) => void;
  intervalHint: string;
}

interface TextFieldProps {
  field: 'url' | 'statuses' | 'bodyMatch' | 'timeout';
  idSuffix: string;
  label: string;
  hint: string;
  value: string;
  error: string | undefined;
  inputRef: Ref<HTMLInputElement>;
  onChange: (value: string) => void;
  type: 'url' | 'text' | 'number';
  fullWidth?: boolean;
}

const FIELD_SLUG: Record<TextFieldProps['field'], string> = {
  url: 'probe-url',
  statuses: 'probe-statuses',
  bodyMatch: 'probe-body',
  timeout: 'probe-timeout',
};

function ProbeTextField({
  field,
  idSuffix,
  label,
  hint,
  value,
  error,
  inputRef,
  onChange,
  type,
  fullWidth = false,
}: TextFieldProps): JSX.Element {
  const id = `heartbeat-${FIELD_SLUG[field]}-${idSuffix}`;
  return (
    <div
      className={
        fullWidth ? 'destination-form__field heartbeat-form__full' : 'destination-form__field'
      }
    >
      <label htmlFor={id}>{label}</label>
      <input
        ref={inputRef}
        id={id}
        type={type}
        autoComplete="off"
        inputMode={type === 'number' ? 'numeric' : undefined}
        value={value}
        aria-invalid={error ? 'true' : undefined}
        aria-describedby={describedBy({ fieldId: id, hasHint: true, error })}
        onChange={(event) => onChange(event.target.value)}
      />
      <span id={`${id}-hint`} className="destination-form__hint">
        {hint}
      </span>
      {error && (
        <span id={fieldErrorId({ fieldId: id })} className="destination-form__field-error">
          {error}
        </span>
      )}
    </div>
  );
}

/** The controlled website-probe inputs (everything except the Access credential pair). */
export function HeartbeatProbeFields({
  idSuffix,
  values,
  errors,
  refs,
  onChange,
  intervalHint,
}: HeartbeatProbeFieldsProps): JSX.Element {
  return (
    <fieldset className="route-form__fieldset heartbeat-form__probe">
      <legend>{strings.heartbeatProbeLegend}</legend>
      <div className="destination-form__grid">
        <ProbeTextField
          field="url"
          idSuffix={idSuffix}
          label={strings.heartbeatProbeUrlLabel}
          hint={strings.heartbeatProbeUrlHint}
          value={values.url}
          error={errors.url}
          inputRef={refs.url}
          onChange={(url) => onChange({ url })}
          type="url"
          fullWidth
        />
        <ProbeTextField
          field="statuses"
          idSuffix={idSuffix}
          label={strings.heartbeatProbeStatusesLabel}
          hint={strings.heartbeatProbeStatusesHint}
          value={values.statuses}
          error={errors.statuses}
          inputRef={refs.statuses}
          onChange={(statuses) => onChange({ statuses })}
          type="text"
        />
        <ProbeTextField
          field="bodyMatch"
          idSuffix={idSuffix}
          label={strings.heartbeatProbeBodyMatchLabel}
          hint={strings.heartbeatProbeBodyMatchHint}
          value={values.bodyMatch}
          error={errors.bodyMatch}
          inputRef={refs.bodyMatch}
          onChange={(bodyMatch) => onChange({ bodyMatch })}
          type="text"
        />
        <DurationField
          id={`heartbeat-probe-interval-${idSuffix}`}
          label={strings.heartbeatProbeIntervalLabel}
          unitLabel={strings.heartbeatProbeIntervalUnitLabel}
          hint={intervalHint}
          duration={values.interval}
          onChange={(interval) => onChange({ interval })}
          inputRef={refs.interval}
          error={errors.interval}
        />
        <ProbeTextField
          field="timeout"
          idSuffix={idSuffix}
          label={strings.heartbeatProbeTimeoutLabel}
          hint={strings.heartbeatProbeTimeoutHint}
          value={values.timeout}
          error={errors.timeout}
          inputRef={refs.timeout}
          onChange={(timeout) => onChange({ timeout })}
          type="number"
        />
      </div>
    </fieldset>
  );
}

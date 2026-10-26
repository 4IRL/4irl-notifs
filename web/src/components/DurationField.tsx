import type { JSX, Ref } from 'react';

import { describedBy, fieldErrorId } from './fieldErrors';
import { DURATION_UNITS } from './heartbeatDuration';
import type { DurationInput, DurationUnit } from './heartbeatDuration';
import './DestinationForm.css';

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
  /** Per-field error text; shown under the hint and wired to the number input. */
  error?: string;
}

/** A number input plus a unit select, with a hint and an optional per-field error. */
export function DurationField({
  id,
  label,
  unitLabel,
  hint,
  duration,
  onChange,
  inputRef,
  error,
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
          aria-invalid={error ? 'true' : undefined}
          aria-describedby={describedBy({ fieldId: id, hasHint: true, error })}
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
      {error && (
        <span id={fieldErrorId({ fieldId: id })} className="destination-form__field-error">
          {error}
        </span>
      )}
    </div>
  );
}

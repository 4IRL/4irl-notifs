import { strings } from '../strings';

/** The units a heartbeat period or grace can be entered in. */
export type DurationUnit = 'minutes' | 'hours' | 'days';

const SECONDS_PER_UNIT: Record<DurationUnit, number> = {
  minutes: 60,
  hours: 3600,
  days: 86_400,
};

/** Largest first, so the first unit that divides a value evenly is the largest one. */
const UNITS_LARGEST_FIRST: DurationUnit[] = ['days', 'hours', 'minutes'];

/**
 * Days start at two, like the list's own duration text (formatDuration): one day
 * reads as "24 hours", the common daily-job schedule, never "1 days".
 */
const MIN_SECONDS_FOR_DAYS = 2 * SECONDS_PER_UNIT.days;

/** Float noise from `0.1 * 3600`-style products stays well below this. */
const SECONDS_TOLERANCE = 1e-6;

export const DURATION_UNITS: Array<{ unit: DurationUnit; label: string }> = [
  { unit: 'minutes', label: strings.heartbeatUnitMinutes },
  { unit: 'hours', label: strings.heartbeatUnitHours },
  { unit: 'days', label: strings.heartbeatUnitDays },
];

/** A duration as the form shows it: the typed number and its unit. */
export interface DurationInput {
  value: string;
  unit: DurationUnit;
}

/**
 * The form representation of a stored duration: the largest unit that divides
 * it evenly (days only from two days up; falling back to minutes, which may
 * leave a fractional value for a duration set through the API), so an untouched
 * field converts back to the exact stored seconds.
 */
export function splitDuration(seconds: number): DurationInput {
  const unit =
    UNITS_LARGEST_FIRST.find(
      (candidate) =>
        seconds % SECONDS_PER_UNIT[candidate] === 0 &&
        (candidate !== 'days' || seconds >= MIN_SECONDS_FOR_DAYS),
    ) ?? 'minutes';
  return { value: String(seconds / SECONDS_PER_UNIT[unit]), unit };
}

/**
 * Converts a typed duration to whole seconds, or null when the text is not a
 * finite number or does not come to a whole number of seconds.
 */
export function toSeconds({ value, unit }: DurationInput): number | null {
  const text = value.trim();
  if (text === '') {
    return null;
  }
  const typed = Number(text);
  if (!Number.isFinite(typed)) {
    return null;
  }
  const exact = typed * SECONDS_PER_UNIT[unit];
  const seconds = Math.round(exact);
  return Math.abs(exact - seconds) <= SECONDS_TOLERANCE ? seconds : null;
}

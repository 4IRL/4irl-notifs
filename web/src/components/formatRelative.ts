import { strings } from '../strings';

const MS_PER_MINUTE = 60_000;
const MINUTES_PER_HOUR = 60;
const MINUTES_PER_DAY = 24 * MINUTES_PER_HOUR;

const MS_PER_SECOND = 1000;
const SECONDS_PER_MINUTE = 60;

/**
 * How long ago `at` was relative to `now`: "just now" at or after now (or when
 * unparseable), "N s ago" under a minute, "N min ago" under an hour, "N h ago"
 * under 24 hours, then "1 day ago" / "N days ago". Every unit rounds down.
 */
export function formatRelativePast({ at, now }: { at: string; now: number }): string {
  const atMs = Date.parse(at);
  if (Number.isNaN(atMs) || atMs >= now) {
    return strings.relativeJustNow;
  }
  const seconds = Math.floor((now - atMs) / MS_PER_SECOND);
  if (seconds < 1) {
    return strings.relativeJustNow;
  }
  if (seconds < SECONDS_PER_MINUTE) {
    return strings.relativeSecondsAgo({ seconds });
  }
  const minutes = Math.floor(seconds / SECONDS_PER_MINUTE);
  if (minutes < MINUTES_PER_HOUR) {
    return strings.relativeMinutesAgo({ minutes });
  }
  if (minutes < MINUTES_PER_DAY) {
    return strings.relativeHoursAgo({ hours: Math.floor(minutes / MINUTES_PER_HOUR) });
  }
  return strings.relativeDaysAgo({ days: Math.floor(minutes / MINUTES_PER_DAY) });
}

/**
 * Compact schedule duration: the largest of d / h / min that divides `seconds`
 * evenly ("5 min", "24 h", "2 d"), otherwise plain seconds. One day reads as
 * "24 h" (the common daily-job schedule); days start at two.
 */
export function formatDuration({ seconds }: { seconds: number }): string {
  const secondsPerDay = SECONDS_PER_MINUTE * MINUTES_PER_DAY;
  const secondsPerHour = SECONDS_PER_MINUTE * MINUTES_PER_HOUR;
  if (seconds >= 2 * secondsPerDay && seconds % secondsPerDay === 0) {
    return strings.durationDays({ value: seconds / secondsPerDay });
  }
  if (seconds > 0 && seconds % secondsPerHour === 0) {
    return strings.durationHours({ value: seconds / secondsPerHour });
  }
  if (seconds > 0 && seconds % SECONDS_PER_MINUTE === 0) {
    return strings.durationMinutes({ value: seconds / SECONDS_PER_MINUTE });
  }
  return strings.durationSeconds({ value: seconds });
}

/**
 * How far `deliverAt` is in the future relative to `now`, at minute
 * granularity (whole minutes, rounded down): "due now" at or before now, "in
 * <1 min" under a minute, "in N min" under an hour, "in H h M min" under 24
 * hours (the minutes part omitted when 0), then "in 1 day" / "in N days".
 * An unparseable `deliverAt` reads as "due now".
 */
export function formatRelativeFuture({
  deliverAt,
  now,
}: {
  deliverAt: string;
  now: number;
}): string {
  const deliverMs = Date.parse(deliverAt);
  if (Number.isNaN(deliverMs) || deliverMs <= now) {
    return strings.relativeDueNow;
  }
  const minutes = Math.floor((deliverMs - now) / MS_PER_MINUTE);
  if (minutes < 1) {
    return strings.relativeUnderMinute;
  }
  if (minutes < MINUTES_PER_HOUR) {
    return strings.relativeMinutes({ minutes });
  }
  if (minutes < MINUTES_PER_DAY) {
    return strings.relativeHoursMinutes({
      hours: Math.floor(minutes / MINUTES_PER_HOUR),
      minutes: minutes % MINUTES_PER_HOUR,
    });
  }
  return strings.relativeDays({ days: Math.floor(minutes / MINUTES_PER_DAY) });
}

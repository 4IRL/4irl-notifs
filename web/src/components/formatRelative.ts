import { strings } from '../strings';

const MS_PER_MINUTE = 60_000;
const MINUTES_PER_HOUR = 60;
const MINUTES_PER_DAY = 24 * MINUTES_PER_HOUR;

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

import { formatDateTimeZone, formatTimeOfDay, toIsoInstant } from './formatLocalTime';

type LocalTimeFormat = 'dateTimeZone' | 'timeOfDaySeconds';

interface LocalTimeProps {
  /** Server timestamp (RFC 3339; an offset-less value is read as UTC). */
  timestamp: string;
  format?: LocalTimeFormat;
  className?: string;
}

function render({ timestamp, format }: { timestamp: string; format: LocalTimeFormat }): string {
  if (format === 'timeOfDaySeconds') {
    return formatTimeOfDay({ timestamp, withSeconds: true });
  }
  return formatDateTimeZone({ timestamp });
}

/** A time in the viewer's zone as its own element; the title carries the UTC instant. */
export function LocalTime({ timestamp, format = 'dateTimeZone', className }: LocalTimeProps) {
  const iso = toIsoInstant({ timestamp });
  // An unparseable timestamp comes back unchanged; never advertise it as a datetime or UTC instant.
  const parsed = iso !== timestamp || !Number.isNaN(Date.parse(timestamp));
  const instant = parsed ? iso : undefined;
  return (
    <time dateTime={instant} title={instant} className={className}>
      {render({ timestamp, format })}
    </time>
  );
}

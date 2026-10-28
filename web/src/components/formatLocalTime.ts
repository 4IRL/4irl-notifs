// Fixed locale so zone abbreviations are stable across hosts
// (America/Los_Angeles -> PDT/PST, Asia/Kolkata -> GMT+5:30).
const LOCALE = 'en-US';

interface TimestampArgs {
  timestamp: string;
  /** IANA zone; undefined means the browser's zone. Exists so tests can inject one. */
  timeZone?: string;
}

interface TimeOfDayArgs extends TimestampArgs {
  withSeconds?: boolean;
}

// A time part ending in Z or a +hh[:mm] / -hh[:mm] offset.
const HAS_OFFSET = /T.*(Z|[+-]\d{2}(:?\d{2})?)$/i;

/** Parses a server timestamp; an offset-less value is UTC. Null when unparseable. */
function parseTimestamp(timestamp: string): Date | null {
  const trimmed = timestamp.trim();
  if (trimmed === '') {
    return null;
  }
  let normalized = trimmed.replace(' ', 'T');
  if (normalized.includes('T')) {
    // Engine-independent offset forms: "... +05:30" -> "...+05:30", "z" -> "Z",
    // "+0530" -> "+05:30", "+05" -> "+05:00".
    normalized = normalized
      .replace(/\s+(?=[+-]\d{2})/, '')
      .replace(/z$/, 'Z')
      .replace(/(T.*[+-]\d{2})(\d{2})$/, '$1:$2')
      .replace(/(T.*[+-]\d{2})$/, '$1:00');
    if (!HAS_OFFSET.test(normalized)) {
      normalized = `${normalized}Z`;
    }
  }
  const parsed = new Date(normalized);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

type PartMap = Partial<Record<Intl.DateTimeFormatPartTypes, string>>;

function getParts({
  date,
  timeZone,
  withSeconds,
  withZoneName,
  withDate,
}: {
  date: Date;
  timeZone?: string;
  withSeconds: boolean;
  withZoneName: boolean;
  withDate: boolean;
}): PartMap {
  const options: Intl.DateTimeFormatOptions = {
    timeZone,
    hourCycle: 'h23',
    hour: '2-digit',
    minute: '2-digit',
  };
  if (withDate) {
    options.year = 'numeric';
    options.month = '2-digit';
    options.day = '2-digit';
  }
  if (withSeconds) {
    options.second = '2-digit';
  }
  if (withZoneName) {
    options.timeZoneName = 'short';
  }
  const parts: PartMap = {};
  for (const part of new Intl.DateTimeFormat(LOCALE, options).formatToParts(date)) {
    parts[part.type] = part.value;
  }
  // Defensive: some engines still emit "24" for midnight.
  if (parts.hour === '24') {
    parts.hour = '00';
  }
  return parts;
}

function clock({ parts, withSeconds }: { parts: PartMap; withSeconds: boolean }): string {
  const base = `${parts.hour}:${parts.minute}`;
  return withSeconds ? `${base}:${parts.second}` : base;
}

/** "2026-10-17T08:12:44Z" -> "2026-10-17 01:12 PDT" in the viewer's zone. */
export function formatDateTimeZone({ timestamp, timeZone }: TimestampArgs): string {
  const date = parseTimestamp(timestamp);
  if (date === null) {
    return timestamp;
  }
  const parts = getParts({
    date,
    timeZone,
    withSeconds: false,
    withZoneName: true,
    withDate: true,
  });
  return `${parts.year}-${parts.month}-${parts.day} ${clock({ parts, withSeconds: false })} ${parts.timeZoneName}`;
}

/** "HH:mm", or "HH:mm:ss" when withSeconds is true. */
export function formatTimeOfDay({
  timestamp,
  timeZone,
  withSeconds = false,
}: TimeOfDayArgs): string {
  const date = parseTimestamp(timestamp);
  if (date === null) {
    return timestamp;
  }
  const parts = getParts({ date, timeZone, withSeconds, withZoneName: false, withDate: false });
  return clock({ parts, withSeconds });
}

/** The UTC ISO instant ("2026-10-17T08:12:44.000Z"); the raw value when unparseable. */
export function toIsoInstant({ timestamp }: { timestamp: string }): string {
  const date = parseTimestamp(timestamp);
  return date === null ? timestamp : date.toISOString();
}

/** The IANA zone name (e.g. "America/Los_Angeles") of the viewer, or the injected one. */
export function localZoneName({ timeZone }: { timeZone?: string } = {}): string {
  return new Intl.DateTimeFormat(LOCALE, { timeZone }).resolvedOptions().timeZone;
}

/** "2026-10-17T08:12:44Z" to "2026-10-17 08:12" (UTC, as the server sends it). */
export function formatMinute(timestamp: string): string {
  return timestamp.replace('T', ' ').slice(0, 16);
}

/** `2026-10-18T14:05:00Z` -> `2026-10-18 14:05 UTC`; unparseable input is shown as-is. */
export function formatUtc(iso: string): string {
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) {
    return iso;
  }
  return `${parsed.toISOString().slice(0, 16).replace('T', ' ')} UTC`;
}

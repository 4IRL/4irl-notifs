/** `2026-10-18T14:05:00Z` -> `2026-10-18 14:05 UTC`; unparseable input is shown as-is. */
export function formatUtc(iso: string): string {
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) {
    return iso;
  }
  return `${parsed.toISOString().slice(0, 16).replace('T', ' ')} UTC`;
}

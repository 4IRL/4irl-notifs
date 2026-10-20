/** Discord's hard limit for a message's `content`. */
export const DISCORD_CONTENT_LIMIT = 2000;

const MS_PER_MINUTE = 60_000;
const MINUTES_PER_HOUR = 60;
const HOURS_PER_DAY = 24;

/** Formats an elapsed time as `N min`, `H h M min` or `D d H h` (never below `1 min`). */
export function formatDuration(elapsedMs: number): string {
  const totalMinutes = Math.max(1, Math.floor(elapsedMs / MS_PER_MINUTE));
  if (totalMinutes < MINUTES_PER_HOUR) {
    return `${totalMinutes} min`;
  }
  const totalHours = Math.floor(totalMinutes / MINUTES_PER_HOUR);
  const minutes = totalMinutes % MINUTES_PER_HOUR;
  if (totalHours < 2 * HOURS_PER_DAY) {
    return minutes === 0 ? `${totalHours} h` : `${totalHours} h ${minutes} min`;
  }
  const days = Math.floor(totalHours / HOURS_PER_DAY);
  const hours = totalHours % HOURS_PER_DAY;
  return hours === 0 ? `${days} d` : `${days} d ${hours} h`;
}

/** `2026-10-17T09:12:00.000Z` becomes `10-17 09:12` (UTC). */
export function formatSince(iso: string): string {
  return iso.slice(5, 16).replace('T', ' ');
}

/** Cuts text to the Discord content limit without splitting a surrogate pair. */
export function truncateContent(text: string): string {
  if (text.length <= DISCORD_CONTENT_LIMIT) {
    return text;
  }
  let end = DISCORD_CONTENT_LIMIT - 1;
  const lastKept = text.charCodeAt(end - 1);
  if (lastKept >= 0xd800 && lastKept <= 0xdbff) {
    end -= 1;
  }
  return `${text.slice(0, end)}…`;
}

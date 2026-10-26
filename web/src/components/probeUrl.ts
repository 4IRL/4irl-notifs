import { strings } from '../strings';

/**
 * A probe URL as shown in the table: host (with any port) plus path only. Userinfo, query and
 * fragment can hold secrets, so they never reach the page. Fails closed: an unparsable value
 * shows the empty marker, never the raw string.
 */
export function displayProbeUrl(url: string): string {
  try {
    const parsed = new URL(url);
    return `${parsed.host}${parsed.pathname}`;
  } catch {
    return strings.noValue;
  }
}

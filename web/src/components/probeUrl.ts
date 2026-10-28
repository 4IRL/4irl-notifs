import { strings } from '../strings';

const SCHEME_PATTERN = /^([a-z][a-z0-9+.-]*):\/\//i;

/**
 * Trims a typed probe URL and prepends `https://` when it has no scheme. An existing scheme is
 * kept (lowercased only), so `http://` and `ftp://` still fail the scheme rule at submit. Blank
 * stays blank and an already-prefixed value is never prefixed twice.
 */
export function withHttpsScheme(url: string): string {
  const trimmed = url.trim();
  if (trimmed === '') {
    return '';
  }
  if (trimmed.startsWith('//')) {
    return trimmed;
  }
  const match = SCHEME_PATTERN.exec(trimmed);
  if (match === null) {
    return `https://${trimmed}`;
  }
  return `${match[1].toLowerCase()}://${trimmed.slice(match[0].length)}`;
}

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

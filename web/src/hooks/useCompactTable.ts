import { useSyncExternalStore } from 'react';

/**
 * The one narrow breakpoint of the admin tables (30rem = 480px at the default font size).
 * `adminTable.css` mirrors it in its `@media` rule; keep the two in step.
 */
export const COMPACT_TABLE_QUERY = '(max-width: 30rem)';

function subscribe(onChange: () => void): () => void {
  if (typeof window.matchMedia !== 'function') {
    return () => undefined;
  }
  const list = window.matchMedia(COMPACT_TABLE_QUERY);
  list.addEventListener('change', onChange);
  return () => {
    list.removeEventListener('change', onChange);
  };
}

function getSnapshot(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia(COMPACT_TABLE_QUERY).matches;
}

function getServerSnapshot(): boolean {
  return false;
}

/**
 * True while the viewport is at or below the narrow breakpoint. Below it the admin tables
 * trade their inline row buttons for one kebab menu per row, so the DOM carries exactly one
 * set of controls (never both, hidden by CSS). Fails closed to the roomy layout when
 * `matchMedia` is missing.
 */
export function useCompactTable(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

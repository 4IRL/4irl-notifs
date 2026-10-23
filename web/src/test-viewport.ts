import { act } from '@testing-library/react';

import { COMPACT_TABLE_QUERY } from './hooks/useCompactTable';

/**
 * A controllable `window.matchMedia` for jsdom, which has none. Only the admin tables'
 * compact breakpoint can match; every other query never does. Installed once by
 * `test-setup.ts`, which also resets it between tests; a test calls `setCompactViewport(true)`
 * to render the narrow layout.
 */
interface StubList {
  query: string;
  listeners: Set<() => void>;
}

const lists = new Set<StubList>();
let compact = false;

function matchesOf(query: string): boolean {
  return query === COMPACT_TABLE_QUERY && compact;
}

export function installMatchMediaStub(): void {
  window.matchMedia = (query: string): MediaQueryList => {
    const entry: StubList = { query, listeners: new Set() };
    lists.add(entry);
    return {
      get matches() {
        return matchesOf(query);
      },
      media: query,
      onchange: null,
      addEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
        entry.listeners.add(listener as () => void);
      },
      removeEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
        entry.listeners.delete(listener as () => void);
      },
      addListener: () => undefined,
      removeListener: () => undefined,
      dispatchEvent: () => false,
    } as MediaQueryList;
  };
}

/** Switches the stubbed viewport between the narrow (compact) and the roomy layout. */
export function setCompactViewport(next: boolean): void {
  if (compact === next) {
    return;
  }
  compact = next;
  act(() => {
    for (const entry of lists) {
      for (const listener of entry.listeners) {
        listener();
      }
    }
  });
}

/** Back to the roomy layout; called after every test. */
export function resetViewport(): void {
  compact = false;
  lists.clear();
}

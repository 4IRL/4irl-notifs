import type { FocusEvent } from 'react';

/**
 * `onFocus` for an `.admin-table__scroll` wrapper: brings a keyboard-focused control fully into
 * view, clear of the pinned first column and the pinned titles.
 *
 * Browsers scroll a newly focused element only when it is wholly out of view; one that is
 * partly visible (half under the pinned column, or cut off at the far edge) is left where it
 * is. `scrollIntoView` with `nearest` alignment moves it the least distance that brings it
 * wholly inside the wrapper's scrollport, inset by its `scroll-padding` (the most the pinned
 * column can cover, so a control just clear of a narrower column may still be nudged). Only
 * keyboard and programmatic-after-keyboard focus (`:focus-visible`) is handled: the focus a
 * mouse press gives must not move the control out from under the pointer before the click
 * lands. The inline editor rows are left to the browser: their form is pinned to the visible
 * width already, and so is the narrow layout's kebab column.
 */
export function revealFocusedControl(event: FocusEvent<HTMLElement>): void {
  const { target, currentTarget } = event;
  // `contains` also drops focus events that React bubbles from a portal (the row menu is
  // rendered outside the wrapper's DOM).
  // The kebab column is pinned (sticky) to the wrapper's end edge, so it is always in view;
  // scrolling it into view would only drag the wrapper to the end of its row.
  if (
    target === currentTarget ||
    !currentTarget.contains(target) ||
    target.closest('.admin-table__editor-cell, .admin-table__kebab') !== null
  ) {
    return;
  }
  if (!target.matches(':focus-visible')) {
    return;
  }
  target.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

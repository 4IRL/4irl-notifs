import { OWNER_ROW_ATTRIBUTE, OWNER_ROW_ID } from './Modal';

/** The attribute rendered on each Heartbeats row; it also names the row an edit dialog owns. */
export const ROW_ID_ATTRIBUTE = 'data-check-id';

/**
 * The id of the row containing `element`, or null. Focus inside the portaled edit dialog (which
 * lives outside the table) resolves to the row the dialog edits through the Modal's owner-row
 * attributes.
 */
export function rowIdOf(element: Element | null): string | null {
  const row = element?.closest(`[${ROW_ID_ATTRIBUTE}]`)?.getAttribute(ROW_ID_ATTRIBUTE);
  if (row !== undefined && row !== null) {
    return row;
  }
  return (
    element
      ?.closest(`[${OWNER_ROW_ATTRIBUTE}="${ROW_ID_ATTRIBUTE}"]`)
      ?.getAttribute(OWNER_ROW_ID) ?? null
  );
}

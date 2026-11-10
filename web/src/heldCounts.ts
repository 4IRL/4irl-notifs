/** Held-message counts as the admin UI knows them; each state is shown differently, never as 0. */
export type HeldCounts =
  | { status: 'loading' }
  | { status: 'unavailable' }
  | { status: 'ready'; byApp: ReadonlyMap<string, number>; unassignedTotal: number };

/** Held count for one app: a ready snapshot with no entry for the app means 0. */
export function heldCountFor({ held, appId }: { held: HeldCounts; appId: string }): number {
  if (held.status !== 'ready') {
    return 0;
  }
  return held.byApp.get(appId) ?? 0;
}

/** Total held messages across every app in a ready snapshot (0 otherwise). */
export function heldTotal(held: HeldCounts): number {
  if (held.status !== 'ready') {
    return 0;
  }
  let total = 0;
  for (const count of held.byApp.values()) {
    total += count;
  }
  return total;
}

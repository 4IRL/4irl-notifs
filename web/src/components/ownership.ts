import type { OwnerFields } from '../api/deliveryClient';
import { strings } from '../strings';

/**
 * The one reading of an owner that the badge and the note both use, so they can
 * never disagree. Fail closed: only a well-formed operator (no app id) or app (a
 * non-empty app id) is trusted; everything else, including an unrecognised kind,
 * is `unknown` and is never treated as the operator.
 */
export type OwnerClass =
  { kind: 'operator' } | { kind: 'app'; appId: string } | { kind: 'unknown' };

export function classifyOwner({ ownerKind, ownerAppId }: OwnerFields): OwnerClass {
  if (ownerKind === 'operator' && ownerAppId === null) {
    return { kind: 'operator' };
  }
  if (ownerKind === 'app' && typeof ownerAppId === 'string' && ownerAppId !== '') {
    return { kind: 'app', appId: ownerAppId };
  }
  return { kind: 'unknown' };
}

/**
 * The note shown on a row the operator does not own outright, or null for an
 * operator-owned row. An unknown owner gets a note too: it is never presented as
 * safe to edit.
 */
export function ownerNoteOf(owner: OwnerFields): string | null {
  const classified = classifyOwner(owner);
  switch (classified.kind) {
    case 'operator':
      return null;
    case 'app':
      return strings.ownerManagedNote({ appId: classified.appId });
    default:
      return strings.ownerUnknownNote;
  }
}

/**
 * The short form of {@link ownerNoteOf} for the single line under a row's buttons and the first
 * line of the row menu, which is never cut off. It does not name the app (the Owner badge does);
 * the long note stays in the destructive-action confirm notices. Null exactly when `ownerNoteOf`
 * is.
 */
export function ownerInlineNoteOf(owner: OwnerFields): string | null {
  const classified = classifyOwner(owner);
  switch (classified.kind) {
    case 'operator':
      return null;
    case 'app':
      return strings.ownerManagedInlineNote;
    default:
      return strings.ownerUnknownInlineNote;
  }
}

import type { RefObject } from 'react';

/** Per-field error messages keyed by field name. A missing key means no error. */
export type FieldErrors<K extends string> = Partial<Record<K, string>>;

/** The id of the element that renders a field's error message. */
export function fieldErrorId({ fieldId }: { fieldId: string }): string {
  return `${fieldId}-error`;
}

/**
 * The `aria-describedby` value for a field: the hint id first, then the error id.
 * `hintId` overrides the default `${fieldId}-hint` (a shared hint element).
 */
export function describedBy({
  fieldId,
  hasHint,
  error,
  hintId,
}: {
  fieldId: string;
  hasHint: boolean;
  error: string | undefined;
  hintId?: string;
}): string | undefined {
  const ids: string[] = [];
  if (hasHint) {
    ids.push(hintId ?? `${fieldId}-hint`);
  }
  if (error) {
    ids.push(fieldErrorId({ fieldId }));
  }
  return ids.length > 0 ? ids.join(' ') : undefined;
}

/**
 * Focuses the first key in DOM `order` that has an error and a mounted ref.
 * Keys whose ref is unmounted (null) are skipped; if none qualifies, nothing is focused.
 */
export function focusFirstInvalid<K extends string>({
  order,
  errors,
  refs,
}: {
  order: readonly K[];
  errors: FieldErrors<K>;
  refs: Partial<Record<K, RefObject<HTMLElement | null>>>;
}): void {
  for (const key of order) {
    if (!errors[key]) {
      continue;
    }
    const element = refs[key]?.current;
    if (element) {
      element.focus();
      return;
    }
  }
}

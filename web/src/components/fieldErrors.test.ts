import { describe, expect, it } from 'vitest';

import { describedBy, fieldErrorId, focusFirstInvalid } from './fieldErrors';

function mountedRef(): { ref: { current: HTMLInputElement }; element: HTMLInputElement } {
  const element = document.createElement('input');
  document.body.appendChild(element);
  return { ref: { current: element }, element };
}

describe('fieldErrorId', () => {
  it('appends -error', () => {
    expect(fieldErrorId({ fieldId: 'probe-url' })).toBe('probe-url-error');
  });
});

describe('describedBy', () => {
  it('is undefined with no hint and no error', () => {
    expect(describedBy({ fieldId: 'f', hasHint: false, error: undefined })).toBeUndefined();
  });

  it('returns the hint id alone', () => {
    expect(describedBy({ fieldId: 'f', hasHint: true, error: undefined })).toBe('f-hint');
  });

  it('returns the error id alone', () => {
    expect(describedBy({ fieldId: 'f', hasHint: false, error: 'bad' })).toBe('f-error');
  });

  it('keeps the hint id first when an error is added', () => {
    expect(describedBy({ fieldId: 'f', hasHint: true, error: 'bad' })).toBe('f-hint f-error');
  });

  it('uses a custom hintId when given', () => {
    expect(describedBy({ fieldId: 'f', hasHint: true, error: 'bad', hintId: 'shared-hint' })).toBe(
      'shared-hint f-error',
    );
  });
});

describe('focusFirstInvalid', () => {
  it('focuses the earliest errored field in DOM order', () => {
    const first = mountedRef();
    const second = mountedRef();
    focusFirstInvalid({
      order: ['a', 'b'] as const,
      errors: { a: 'x', b: 'y' },
      refs: { a: first.ref, b: second.ref },
    });
    expect(document.activeElement).toBe(first.element);
  });

  it('follows order, not error insertion order', () => {
    const first = mountedRef();
    const second = mountedRef();
    focusFirstInvalid({
      order: ['a', 'b'] as const,
      errors: { b: 'y', a: 'x' },
      refs: { a: first.ref, b: second.ref },
    });
    expect(document.activeElement).toBe(first.element);
  });

  it('skips fields without errors', () => {
    const first = mountedRef();
    const second = mountedRef();
    focusFirstInvalid({
      order: ['a', 'b'] as const,
      errors: { b: 'y' },
      refs: { a: first.ref, b: second.ref },
    });
    expect(document.activeElement).toBe(second.element);
  });

  it('skips an errored key whose ref is null and focuses the next mounted one', () => {
    const second = mountedRef();
    focusFirstInvalid({
      order: ['a', 'b'] as const,
      errors: { a: 'x', b: 'y' },
      refs: { a: { current: null }, b: second.ref },
    });
    expect(document.activeElement).toBe(second.element);
  });

  it('focuses nothing and does not throw when every errored ref is null', () => {
    const before = document.activeElement;
    expect(() =>
      focusFirstInvalid({
        order: ['a', 'b'] as const,
        errors: { a: 'x', b: 'y' },
        refs: { a: { current: null } },
      }),
    ).not.toThrow();
    expect(document.activeElement).toBe(before);
  });
});

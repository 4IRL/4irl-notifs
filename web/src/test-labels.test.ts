import { describe, expect, it } from 'vitest';

import { accessibleNameOf, labelInNameViolations, visibleTextInName } from './test-labels';

function button({ text, ariaLabel }: { text: string; ariaLabel?: string }): HTMLButtonElement {
  const element = document.createElement('button');
  element.textContent = text;
  if (ariaLabel !== undefined) {
    element.setAttribute('aria-label', ariaLabel);
  }
  return element;
}

describe('visibleTextInName', () => {
  it('accepts a name that contains the visible text, ignoring case and a trailing ellipsis', () => {
    expect(visibleTextInName({ visible: 'Delete', accessible: 'Delete hook1' })).toBe(true);
    expect(visibleTextInName({ visible: 'Test', accessible: 'Send test to hook1' })).toBe(true);
    expect(visibleTextInName({ visible: 'Deleting…', accessible: 'Deleting hook1' })).toBe(true);
  });

  it('rejects a name that does not contain the visible text', () => {
    expect(visibleTextInName({ visible: 'Delete', accessible: 'Remove hook1' })).toBe(false);
    expect(visibleTextInName({ visible: 'Confirm delete', accessible: 'Delete hook1' })).toBe(
      false,
    );
  });
});

describe('labelInNameViolations', () => {
  it('names each control whose aria-label drops its visible text', () => {
    const good = button({ text: 'Edit', ariaLabel: 'Edit hook1' });
    const bad = button({ text: 'Edit', ariaLabel: 'Change hook1' });

    expect(labelInNameViolations([good])).toEqual([]);
    expect(labelInNameViolations([good, bad])).toEqual([
      'visible "Edit" is not in the name "Change hook1"',
    ]);
  });

  it('falls back to the text when there is no aria-label, and skips a control with no text', () => {
    expect(accessibleNameOf(button({ text: 'Cancel' }))).toBe('Cancel');
    expect(labelInNameViolations([button({ text: 'Cancel' }), button({ text: '' })])).toEqual([]);
  });
});

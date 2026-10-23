/**
 * Test helpers for WCAG 2.5.3 (Label in Name): the accessible name of a control must contain
 * its visible text, so a voice-control user who says what they read can activate it.
 */

/** Lowercased, whitespace-collapsed, trailing ellipsis dropped ("Sending…" reads as "sending"). */
function normalize(text: string): string {
  return text
    .toLowerCase()
    .replace(/[…]|\.{3}/g, '')
    .replace(/\s+/g, ' ')
    .trim();
}

/** The name a control exposes: its `aria-label` when set, else its text. */
export function accessibleNameOf(element: Element): string {
  return element.getAttribute('aria-label') ?? element.textContent ?? '';
}

/** True when the accessible name contains the visible text (case and trailing ellipsis aside). */
export function visibleTextInName({
  visible,
  accessible,
}: {
  visible: string;
  accessible: string;
}): boolean {
  return normalize(accessible).includes(normalize(visible));
}

/**
 * Describes every control whose accessible name does not contain its visible text. An empty
 * result means label-in-name holds for all of them.
 */
export function labelInNameViolations(elements: Element[]): string[] {
  const violations: string[] = [];
  for (const element of elements) {
    const visible = element.textContent ?? '';
    const accessible = accessibleNameOf(element);
    if (visible.trim() !== '' && !visibleTextInName({ visible, accessible })) {
      violations.push(`visible "${visible}" is not in the name "${accessible}"`);
    }
  }
  return violations;
}

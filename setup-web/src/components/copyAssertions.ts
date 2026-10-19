import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect } from 'vitest';

import { strings } from '../strings';

/**
 * Clicks the Copy button named after each label and asserts the clipboard then holds
 * the expected value. Verifies what is actually copied (and the label-to-value
 * pairing), not just what is displayed. `user-event` installs a clipboard stub.
 */
export async function expectCopyButtonsCopy(valuesByLabel: Record<string, string>) {
  const user = userEvent.setup();
  expect(screen.getAllByRole('button')).toHaveLength(Object.keys(valuesByLabel).length);
  for (const [label, value] of Object.entries(valuesByLabel)) {
    await user.click(screen.getByRole('button', { name: strings.copyButtonLabel(label) }));
    expect(await navigator.clipboard.readText()).toBe(value);
  }
}

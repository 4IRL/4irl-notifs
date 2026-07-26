import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';

afterEach(() => {
  cleanup();
});

describe('ConfirmButton', () => {
  it('does not fire onConfirm until the trigger is confirmed', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={onConfirm}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(onConfirm).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Confirm remove foo' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('reverts to the trigger without firing when cancelled', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={onConfirm}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await user.click(screen.getByRole('button', { name: strings.cancelAction }));

    expect(onConfirm).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Remove foo' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Confirm remove foo' })).not.toBeInTheDocument();
  });
});

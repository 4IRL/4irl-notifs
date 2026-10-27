import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ConfirmDialog } from './ConfirmDialog';

afterEach(cleanup);

function renderDialog(overrides: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
  const onDismiss = vi.fn();
  const onConfirm = vi.fn();
  render(
    <ConfirmDialog
      title="Delete thing?"
      open
      onDismiss={onDismiss}
      item={{ label: 'Destination', value: 'hook1' }}
      notice="This cannot be undone."
      cancel={{ label: 'Cancel' }}
      confirm={{ label: 'Delete', onClick: onConfirm }}
      {...overrides}
    />,
  );
  return { onDismiss, onConfirm };
}

describe('ConfirmDialog', () => {
  it('renders the item box and the notice', () => {
    renderDialog();
    const dialog = screen.getByRole('dialog', { name: 'Delete thing?' });
    expect(dialog).toHaveTextContent('Destination');
    expect(dialog).toHaveTextContent('hook1');
    expect(dialog.querySelector('.modal__notice')).toHaveTextContent('This cannot be undone.');
  });

  it('omits the notice when none is given', () => {
    renderDialog({ notice: undefined });
    expect(screen.getByRole('dialog').querySelector('.modal__notice')).toBeNull();
  });

  it('focuses Cancel first', () => {
    renderDialog();
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveFocus();
  });

  it('Cancel dismisses and Confirm confirms', async () => {
    const user = userEvent.setup();
    const { onDismiss, onConfirm } = renderDialog();
    await user.click(screen.getByRole('button', { name: 'Delete' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('marks cancel and confirm aria-disabled only when disabled', () => {
    renderDialog({
      cancel: { label: 'Cancel', disabled: true },
      confirm: { label: 'Delete', disabled: true, onClick: vi.fn() },
    });
    expect(screen.getByRole('button', { name: 'Cancel' })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveAttribute('aria-disabled', 'true');
  });

  it('has no aria-disabled by default', () => {
    renderDialog();
    expect(screen.getByRole('button', { name: 'Cancel' })).not.toHaveAttribute('aria-disabled');
    expect(screen.getByRole('button', { name: 'Delete' })).not.toHaveAttribute('aria-disabled');
  });

  it('defaults the confirm tone to danger and honours primary', () => {
    renderDialog();
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveClass('modal__button--danger');
    cleanup();
    renderDialog({ confirm: { label: 'Delete', tone: 'primary', onClick: vi.fn() } });
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveClass('modal__button--primary');
  });
});

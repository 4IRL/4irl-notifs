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

  it('shows confirmNotice only while the pair is showing', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
        confirmNotice="Heads up"
      />,
    );

    expect(screen.queryByText('Heads up')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(screen.getByText('Heads up')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: strings.cancelAction }));
    expect(screen.queryByText('Heads up')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await user.click(screen.getByRole('button', { name: 'Confirm remove foo' }));
    expect(screen.queryByText('Heads up')).not.toBeInTheDocument();
  });

  it('focuses Confirm when the pair appears and the trigger after Cancel', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(screen.getByRole('button', { name: 'Confirm remove foo' })).toHaveFocus();

    await user.click(screen.getByRole('button', { name: strings.cancelAction }));
    expect(screen.getByRole('button', { name: 'Remove foo' })).toHaveFocus();
  });

  it('returns focus to the trigger after Confirm', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await user.click(screen.getByRole('button', { name: 'Confirm remove foo' }));

    expect(screen.getByRole('button', { name: 'Remove foo' })).toHaveFocus();
  });

  it('is aria-disabled with the busy label and opens no pair while busy', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={onConfirm}
        busy
        busyLabel="Removing…"
      />,
    );

    const trigger = screen.getByRole('button', { name: 'Removing…' });
    expect(trigger).toHaveAttribute('aria-disabled', 'true');
    await user.click(trigger);

    expect(screen.queryByRole('button', { name: 'Confirm remove foo' })).not.toBeInTheDocument();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('closes an open pair when busy turns true and never double-fires', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const { rerender } = render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={onConfirm}
        busyLabel="Removing…"
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(screen.getByRole('button', { name: 'Confirm remove foo' })).toBeInTheDocument();

    rerender(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={onConfirm}
        busy
        busyLabel="Removing…"
      />,
    );
    expect(screen.queryByRole('button', { name: 'Confirm remove foo' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Removing…' })).toHaveAttribute(
      'aria-disabled',
      'true',
    );

    await user.click(screen.getByRole('button', { name: 'Removing…' }));
    expect(onConfirm).not.toHaveBeenCalled();

    rerender(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={onConfirm}
        busyLabel="Removing…"
      />,
    );
    // The pair does not reappear once busy clears.
    expect(screen.queryByRole('button', { name: 'Confirm remove foo' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove foo' })).not.toHaveAttribute('aria-disabled');
  });
});

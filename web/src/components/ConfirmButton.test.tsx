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

  it('omitting the optional props leaves the default output unchanged', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Remove foo' })).not.toHaveAttribute('aria-label');
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(screen.getByRole('button', { name: 'Confirm remove foo' })).not.toHaveAttribute(
      'aria-label',
    );
    expect(screen.getByRole('button', { name: strings.cancelAction })).toBeInTheDocument();
  });

  it('uses dismissLabel for the dismiss button', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        dismissLabel="Keep"
        onConfirm={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));

    expect(screen.getByRole('button', { name: 'Keep' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: strings.cancelAction })).not.toBeInTheDocument();
  });

  it('applies triggerAriaLabel and confirmAriaLabel as accessible names', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Remove"
        triggerAriaLabel="Remove foo"
        confirmLabel="Confirm"
        confirmAriaLabel="Confirm remove foo"
        onConfirm={vi.fn()}
      />,
    );

    const trigger = screen.getByRole('button', { name: 'Remove foo' });
    expect(trigger).toHaveTextContent('Remove');
    await user.click(trigger);

    const confirm = screen.getByRole('button', { name: 'Confirm remove foo' });
    expect(confirm).toHaveTextContent('Confirm');
  });

  it('applies busyAriaLabel only while busy', () => {
    const { rerender } = render(
      <ConfirmButton
        triggerLabel="Remove"
        triggerAriaLabel="Remove foo"
        confirmLabel="Confirm"
        busy
        busyLabel="Removing…"
        busyAriaLabel="Removing foo"
        onConfirm={vi.fn()}
      />,
    );

    const busyTrigger = screen.getByRole('button', { name: 'Removing foo' });
    expect(busyTrigger).toHaveTextContent('Removing…');
    expect(busyTrigger).toHaveAttribute('aria-disabled', 'true');

    rerender(
      <ConfirmButton
        triggerLabel="Remove"
        triggerAriaLabel="Remove foo"
        confirmLabel="Confirm"
        busyLabel="Removing…"
        busyAriaLabel="Removing foo"
        onConfirm={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Removing foo' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove foo' })).toBeInTheDocument();
  });

  it('keeps the visible text as the busy name when busyAriaLabel is omitted', () => {
    render(
      <ConfirmButton
        triggerLabel="Remove"
        triggerAriaLabel="Remove foo"
        confirmLabel="Confirm"
        busy
        busyLabel="Removing…"
        onConfirm={vi.fn()}
      />,
    );

    const busyTrigger = screen.getByRole('button', { name: 'Removing…' });
    expect(busyTrigger).not.toHaveAttribute('aria-label');
  });

  it('registerTrigger gets the trigger on mount and null while the pair shows', async () => {
    const user = userEvent.setup();
    const registerTrigger = vi.fn();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        registerTrigger={registerTrigger}
        onConfirm={vi.fn()}
      />,
    );

    const trigger = screen.getByRole('button', { name: 'Remove foo' });
    expect(registerTrigger).toHaveBeenLastCalledWith(trigger);

    await user.click(trigger);
    expect(registerTrigger).toHaveBeenLastCalledWith(null);

    await user.click(screen.getByRole('button', { name: strings.cancelAction }));
    expect(registerTrigger).toHaveBeenLastCalledWith(
      screen.getByRole('button', { name: 'Remove foo' }),
    );
  });
});

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { UserEvent } from '@testing-library/user-event';
import type { ComponentProps, FormEvent } from 'react';
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

  it('applies dismissAriaLabel as the accessible name of the dismiss button, keeping its visible text', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Pause"
        confirmLabel="Confirm pause"
        dismissLabel="Keep running"
        dismissAriaLabel="Keep running app/check"
        onConfirm={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Pause' }));

    const dismiss = screen.getByRole('button', { name: 'Keep running app/check' });
    expect(dismiss).toHaveTextContent('Keep running');
    expect(screen.queryByRole('button', { name: 'Keep running' })).not.toBeInTheDocument();
  });

  it('leaves the dismiss button name as its visible text when dismissAriaLabel is omitted', async () => {
    const user = userEvent.setup();
    render(<ConfirmButton triggerLabel="Remove" confirmLabel="Confirm" onConfirm={vi.fn()} />);

    await user.click(screen.getByRole('button', { name: 'Remove' }));

    expect(screen.getByText(strings.cancelAction)).not.toHaveAttribute('aria-label');
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

  it('Escape closes the pair like Cancel: no confirm, focus back on the trigger, no bubbling', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onOuterKeyDown = vi.fn();
    render(
      <div onKeyDown={onOuterKeyDown}>
        <ConfirmButton
          triggerLabel="Remove foo"
          confirmLabel="Confirm remove foo"
          onConfirm={onConfirm}
          confirmNotice="Gone for good."
        />
      </div>,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(screen.getByRole('button', { name: 'Confirm remove foo' })).toHaveFocus();
    await user.keyboard('{Escape}');

    expect(onConfirm).not.toHaveBeenCalled();
    expect(onOuterKeyDown).not.toHaveBeenCalled();
    expect(screen.queryByText('Gone for good.')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove foo' })).toHaveFocus();
  });

  it('Escape from the Cancel button closes the pair too, and other keys leave it open', async () => {
    const user = userEvent.setup();
    render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await user.keyboard('{Tab}');
    expect(screen.getByRole('button', { name: strings.cancelAction })).toHaveFocus();
    await user.keyboard('a');
    expect(screen.getByRole('button', { name: 'Confirm remove foo' })).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.getByRole('button', { name: 'Remove foo' })).toHaveFocus();
  });

  it('reports every open and close through onConfirmingChange', async () => {
    const user = userEvent.setup();
    const onConfirmingChange = vi.fn();
    const { rerender } = render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
        busyLabel="Removing…"
        onConfirmingChange={onConfirmingChange}
      />,
    );
    const lastReport = () => onConfirmingChange.mock.calls.at(-1)?.[0];
    expect(lastReport()).toBe(false);

    // Cancel, Escape and Confirm each close it.
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(lastReport()).toBe(true);
    await user.click(screen.getByRole('button', { name: strings.cancelAction }));
    expect(lastReport()).toBe(false);

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(lastReport()).toBe(true);
    await user.keyboard('{Escape}');
    expect(lastReport()).toBe(false);

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await user.click(screen.getByRole('button', { name: 'Confirm remove foo' }));
    expect(lastReport()).toBe(false);

    // Going busy while open closes it.
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(lastReport()).toBe(true);
    rerender(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
        busy
        busyLabel="Removing…"
        onConfirmingChange={onConfirmingChange}
      />,
    );
    expect(lastReport()).toBe(false);
  });

  it('closes the owner’s state when it unmounts while open, and follows a new callback', async () => {
    const user = userEvent.setup();
    const first = vi.fn();
    const second = vi.fn();
    const { rerender, unmount } = render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
        onConfirmingChange={first}
      />,
    );
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    rerender(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
        onConfirmingChange={second}
      />,
    );
    // A new inline callback does not re-announce the unchanged state.
    expect(second).not.toHaveBeenCalled();

    unmount();
    expect(second).toHaveBeenLastCalledWith(false);
  });

  it('hidden hides the trigger but keeps it mounted and registered', () => {
    const registerTrigger = vi.fn();
    const { rerender } = render(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
        registerTrigger={registerTrigger}
        hidden
      />,
    );

    expect(screen.queryByRole('button', { name: 'Remove foo' })).not.toBeInTheDocument();
    const trigger = screen.getByText('Remove foo');
    expect(trigger).toHaveAttribute('hidden');
    expect(registerTrigger).toHaveBeenLastCalledWith(trigger);

    rerender(
      <ConfirmButton
        triggerLabel="Remove foo"
        confirmLabel="Confirm remove foo"
        onConfirm={vi.fn()}
        registerTrigger={registerTrigger}
      />,
    );
    expect(screen.getByRole('button', { name: 'Remove foo' })).not.toHaveAttribute('hidden');
  });
});

describe('ConfirmButton dialog mode', () => {
  function renderDialog(
    props: Partial<ComponentProps<typeof ConfirmButton>> = {},
  ): ReturnType<typeof render> {
    return render(
      <ConfirmButton
        triggerLabel="Remove"
        triggerAriaLabel="Remove foo"
        confirmLabel="Remove"
        confirmAriaLabel="Confirm remove foo"
        dismissAriaLabel="Cancel remove foo"
        onConfirm={vi.fn()}
        dialogTitle="Remove app?"
        dialogItem={{ label: 'App', value: 'foo' }}
        confirmNotice="Heads up"
        {...props}
      />,
    );
  }

  it('opens a dialog naming the action, the item and the notice, keeping the trigger', async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));

    const dialog = await screen.findByRole('dialog', { name: 'Remove app?' });
    expect(within(dialog).getByText('App')).toBeInTheDocument();
    expect(within(dialog).getByText('foo')).toBeInTheDocument();
    expect(within(dialog).getByText('Heads up')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove foo', hidden: true })).toBeInTheDocument();
  });

  it('is aria-disabled with the busy label and opens no dialog while busy', async () => {
    const user = userEvent.setup();
    renderDialog({ busy: true, busyLabel: 'Removing…', busyAriaLabel: 'Removing foo' });

    const trigger = screen.getByRole('button', { name: 'Removing foo' });
    expect(trigger).toHaveAttribute('aria-disabled', 'true');
    expect(trigger).toHaveTextContent('Removing…');
    await user.click(trigger);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('registers null when unmounted', () => {
    const registerTrigger = vi.fn();
    const { unmount } = renderDialog({ registerTrigger });
    unmount();
    expect(registerTrigger).toHaveBeenLastCalledWith(null);
  });

  it('does not reopen or stay disabled when busy clears after a force-close', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const props = {
      triggerLabel: 'Remove',
      triggerAriaLabel: 'Remove foo',
      confirmLabel: 'Remove',
      onConfirm,
      dialogTitle: 'Remove app?',
      dialogItem: { label: 'App', value: 'foo' },
    };
    const { rerender } = render(<ConfirmButton {...props} />);
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await screen.findByRole('dialog');
    rerender(<ConfirmButton {...props} busy />);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    rerender(<ConfirmButton {...props} />);

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove foo' })).not.toHaveAttribute('aria-disabled');
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('renders no notice paragraph when confirmNotice is omitted', async () => {
    const user = userEvent.setup();
    renderDialog({ confirmNotice: undefined });
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await screen.findByRole('dialog');
    expect(document.querySelector('.modal__notice')).toBeNull();
  });

  it('uses dismissLabel as the visible dismiss text', async () => {
    const user = userEvent.setup();
    renderDialog({ dismissLabel: 'Keep it' });
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(await screen.findByRole('button', { name: 'Cancel remove foo' })).toHaveTextContent(
      'Keep it',
    );
  });

  it('focuses Cancel first and keeps the dismiss and confirm names and tone', async () => {
    const user = userEvent.setup();
    renderDialog({ tone: 'primary' });

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));

    const cancel = await screen.findByRole('button', { name: 'Cancel remove foo' });
    expect(cancel).toHaveTextContent(strings.cancelAction);
    expect(cancel).toHaveFocus();
    expect(cancel).toHaveClass('modal__button--cancel');
    const confirm = screen.getByRole('button', { name: 'Confirm remove foo' });
    expect(confirm).toHaveTextContent('Remove');
    expect(confirm).toHaveClass('modal__button--primary');
  });

  it('defaults to the danger tone', async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    expect(await screen.findByRole('button', { name: 'Confirm remove foo' })).toHaveClass(
      'modal__button--danger',
    );
  });

  it('fires onConfirm once, closes and refocuses the trigger', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    renderDialog({ onConfirm });

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    const confirm = await screen.findByRole('button', { name: 'Confirm remove foo' });
    // Both activations land before React re-renders, so only the fired guard can stop the second.
    act(() => {
      confirm.click();
      confirm.click();
    });

    expect(onConfirm).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Remove foo' })).toHaveFocus();
  });

  it.each([
    [
      'Cancel',
      (user: UserEvent) => user.click(screen.getByRole('button', { name: 'Cancel remove foo' })),
    ],
    [
      'X',
      (user: UserEvent) => user.click(screen.getByRole('button', { name: strings.closeDialog })),
    ],
    ['Escape', (user: UserEvent) => user.keyboard('{Escape}')],
    [
      'backdrop',
      async () => {
        const backdrop = document.querySelector('.modal__backdrop') as HTMLElement;
        fireEvent.pointerDown(backdrop);
        fireEvent.pointerUp(backdrop);
        fireEvent.click(backdrop);
      },
    ],
  ])('closes without firing on %s, refocusing the trigger', async (_name, close) => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    renderDialog({ onConfirm });
    const trigger = screen.getByRole('button', { name: 'Remove foo' });

    await user.click(trigger);
    await screen.findByRole('dialog');
    await close(user);

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(trigger).toHaveFocus();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('closes without firing when busy turns true while open', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const { rerender } = renderDialog({ onConfirm });
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await screen.findByRole('dialog');

    rerender(
      <ConfirmButton
        triggerLabel="Remove"
        triggerAriaLabel="Remove foo"
        confirmLabel="Remove"
        onConfirm={onConfirm}
        dialogTitle="Remove app?"
        dialogItem={{ label: 'App', value: 'foo' }}
        busy
      />,
    );

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('closes without firing on hashchange', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    renderDialog({ onConfirm });
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await screen.findByRole('dialog');

    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('passes ownerRow to the dialog', async () => {
    const user = userEvent.setup();
    renderDialog({ ownerRow: { attribute: 'data-message-id', id: '7' } });
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveAttribute('data-owner-row-attribute', 'data-message-id');
    expect(dialog).toHaveAttribute('data-owner-row-id', '7');
  });

  it('registers the trigger once and never with null while the dialog is open', async () => {
    const user = userEvent.setup();
    const registerTrigger = vi.fn();
    renderDialog({ registerTrigger });
    const trigger = screen.getByRole('button', { name: 'Remove foo' });
    expect(registerTrigger).toHaveBeenCalledWith(trigger);

    await user.click(trigger);
    await screen.findByRole('dialog');

    expect(registerTrigger).not.toHaveBeenCalledWith(null);
    expect(registerTrigger).toHaveBeenCalledTimes(1);
  });

  it('fires nothing and leaves no dialog when unmounted while open', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const { unmount } = renderDialog({ onConfirm });
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await screen.findByRole('dialog');

    unmount();

    expect(onConfirm).not.toHaveBeenCalled();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('does not submit an ancestor form from Confirm or Cancel', async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn((event: FormEvent) => {
      event.preventDefault();
    });
    render(
      <form onSubmit={onSubmit}>
        <ConfirmButton
          triggerLabel="Remove"
          triggerAriaLabel="Remove foo"
          confirmLabel="Remove"
          confirmAriaLabel="Confirm remove foo"
          dismissAriaLabel="Cancel remove foo"
          onConfirm={vi.fn()}
          dialogTitle="Remove app?"
          dialogItem={{ label: 'App', value: 'foo' }}
        />
      </form>,
    );

    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await user.click(await screen.findByRole('button', { name: 'Cancel remove foo' }));
    await user.click(screen.getByRole('button', { name: 'Remove foo' }));
    await user.click(await screen.findByRole('button', { name: 'Confirm remove foo' }));

    expect(onSubmit).not.toHaveBeenCalled();
  });
});

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { UserEvent } from '@testing-library/user-event';
import { createElement } from 'react';
import type { ComponentProps, FormEvent } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';

afterEach(() => {
  cleanup();
});

describe('ConfirmButton', () => {
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

  it('requires dialogTitle and dialogItem', () => {
    const base = { triggerLabel: 'Remove', confirmLabel: 'Remove', onConfirm: vi.fn() };
    const item = { label: 'App', value: 'foo' };
    // @ts-expect-error dialogTitle is required: every confirm names its action.
    const noTitle = createElement(ConfirmButton, { ...base, dialogItem: item });
    // @ts-expect-error dialogItem is required: every confirm names its item.
    const noItem = createElement(ConfirmButton, { ...base, dialogTitle: 'Remove app?' });
    expect(noTitle).toBeTruthy();
    expect(noItem).toBeTruthy();
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

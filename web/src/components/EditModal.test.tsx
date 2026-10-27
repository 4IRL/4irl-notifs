import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useRef, useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { EditModal } from './EditModal';
import type { ConfirmStep } from './EditModal';

interface HarnessProps {
  initialDirty?: boolean;
  busy?: boolean;
  rowGone?: boolean;
  withStep?: boolean;
  onConfirm?: () => void;
  guardMessage?: string;
  discardLabel?: string;
  keepLabel?: string;
  restoreFocus?: boolean;
  ownerRow?: { attribute: string; id: string };
}

function Harness({
  initialDirty = false,
  busy = false,
  rowGone = false,
  withStep = false,
  onConfirm,
  guardMessage,
  discardLabel,
  keepLabel,
  restoreFocus,
  ownerRow,
}: HarnessProps) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState('');
  const [dirty, setDirty] = useState(initialDirty);
  const [step, setStep] = useState<ConfirmStep | null>(null);
  const returnRef = useRef<HTMLElement | null>(null);
  const initialRef = useRef<HTMLInputElement | null>(null);

  const close = () => {
    setStep(null);
    setOpen(false);
  };
  return (
    <div id="root">
      <button type="button" onClick={() => setOpen(true)}>
        opener
      </button>
      <EditModal
        open={open}
        title="Edit thing"
        onClose={close}
        dirty={dirty}
        busy={busy}
        rowGone={rowGone}
        guardMessage={guardMessage}
        discardLabel={discardLabel}
        keepLabel={keepLabel}
        restoreFocus={restoreFocus}
        ownerRow={ownerRow}
        returnFocusRef={returnRef}
        initialFocusRef={initialRef}
        confirmStep={step}
      >
        {({ requestClose }) => (
          <form onSubmit={(event) => event.preventDefault()}>
            <input
              aria-label="name"
              ref={initialRef}
              value={value}
              onChange={(event) => {
                setValue(event.target.value);
                setDirty(true);
              }}
            />
            <button type="button" onClick={close}>
              save
            </button>
            <button type="button" onClick={requestClose}>
              form cancel
            </button>
            {withStep ? (
              <button
                type="button"
                onClick={() =>
                  setStep({
                    message: 'Really rotate?',
                    confirmLabel: 'Rotate now',
                    cancelLabel: 'Not now',
                    onConfirm: () => {
                      setStep(null);
                      onConfirm?.();
                    },
                    onCancel: () => setStep(null),
                  })
                }
              >
                rotate
              </button>
            ) : null}
          </form>
        )}
      </EditModal>
    </div>
  );
}

const user = () => userEvent.setup();
const openIt = async (userEvents: ReturnType<typeof userEvent.setup>) =>
  userEvents.click(screen.getByRole('button', { name: 'opener' }));
const typeIt = async (userEvents: ReturnType<typeof userEvent.setup>) => {
  await userEvents.type(screen.getByLabelText('name'), 'x');
};

const typeInto = async (userEvents: ReturnType<typeof userEvent.setup>) => {
  await openIt(userEvents);
  await typeIt(userEvents);
};
const openStepAfterTyping = async (userEvents: ReturnType<typeof userEvent.setup>) => {
  const rotate = screen.getByRole('button', { name: 'rotate' });
  rotate.focus();
  await userEvents.click(rotate);
};

beforeEach(() => {
  document.body.style.overflow = '';
});
afterEach(() => {
  cleanup();
  document.body.style.overflow = '';
});

describe('EditModal', () => {
  it('uses the wide modal and focuses initialFocusRef on open', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    expect(screen.getByRole('dialog').classList.contains('modal--wide')).toBe(true);
    expect(document.activeElement).toBe(screen.getByLabelText('name'));
  });

  it('renders an empty actions bar normally', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    expect(document.querySelector('.modal__actions')?.childNodes.length).toBe(0);
  });

  it('closes a clean form on Escape, X and backdrop without a guard', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await userEvents.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    await openIt(userEvents);
    await userEvents.click(screen.getByRole('button', { name: strings.closeDialog }));
    expect(screen.queryByRole('dialog')).toBeNull();
    await openIt(userEvents);
    const backdrop = document.querySelector('.modal__backdrop') as HTMLElement;
    fireEvent.pointerDown(backdrop);
    fireEvent.pointerUp(backdrop);
    fireEvent.click(backdrop);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('shows the guard on X, Escape and hashchange for a dirty form', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await typeIt(userEvents);
    await userEvents.click(screen.getByRole('button', { name: strings.closeDialog }));
    expect(screen.getByRole('alert').textContent).toBe(strings.discardChangesMessage);
    await userEvents.click(screen.getByRole('button', { name: strings.keepEditing }));
    await userEvents.keyboard('{Escape}');
    expect(screen.getByRole('alert')).toBeTruthy();
    await userEvents.keyboard('{Escape}');
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('dialog')).toBeTruthy();
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(screen.getByRole('alert')).toBeTruthy();
    expect(document.querySelectorAll('[role="dialog"]').length).toBe(1);
  });

  it('shows the guard on a backdrop click and Escape on the guard keeps editing', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await typeIt(userEvents);
    const backdrop = document.querySelector('.modal__backdrop') as HTMLElement;
    fireEvent.pointerDown(backdrop);
    fireEvent.pointerUp(backdrop);
    fireEvent.click(backdrop);
    expect(screen.getByRole('alert')).toBeTruthy();
    await userEvents.keyboard('{Escape}');
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('dialog')).toBeTruthy();
  });

  it('routes the form Cancel through the guard when dirty and closes when clean', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await userEvents.click(screen.getByRole('button', { name: 'form cancel' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    await openIt(userEvents);
    await typeIt(userEvents);
    await userEvents.click(screen.getByRole('button', { name: 'form cancel' }));
    expect(screen.getByRole('alert')).toBeTruthy();
  });

  it('Discard changes closes and the next open shows no guard', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await typeIt(userEvents);
    await userEvents.keyboard('{Escape}');
    await userEvents.click(screen.getByRole('button', { name: strings.discardChanges }));
    expect(screen.queryByRole('dialog')).toBeNull();
    await openIt(userEvents);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByLabelText('name').closest('[inert]')).toBeNull();
  });

  it('replaces the guard message and button labels', async () => {
    const userEvents = user();
    render(<Harness guardMessage="Sure?" discardLabel="Throw away" keepLabel="Stay" />);
    await openIt(userEvents);
    await typeIt(userEvents);
    await userEvents.keyboard('{Escape}');
    expect(screen.getByRole('alert').textContent).toBe('Sure?');
    expect(screen.getByRole('button', { name: 'Throw away' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Stay' })).toBeTruthy();
  });

  it('keeps the form mounted and inert under the guard, then restores focus', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await typeIt(userEvents);
    const field = screen.getByLabelText('name');
    field.focus();
    await userEvents.keyboard('{Escape}');
    expect(field.closest('[inert]')).not.toBeNull();
    const keep = screen.getByRole('button', { name: strings.keepEditing });
    expect(document.activeElement).toBe(keep);
    const describedBy = keep.getAttribute('aria-describedby') as string;
    expect(document.getElementById(describedBy)?.textContent).toBe(strings.discardChangesMessage);
    await userEvents.click(keep);
    expect(field.closest('[inert]')).toBeNull();
    expect((field as HTMLInputElement).value).toBe('x');
    expect(document.activeElement).toBe(field);
  });

  it('restores focus after Escape on the guard', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await typeIt(userEvents);
    const field = screen.getByLabelText('name');
    await userEvents.keyboard('{Escape}');
    await userEvents.keyboard('{Escape}');
    expect(document.activeElement).toBe(field);
  });

  it('ignores every dismissal while busy and sets aria-busy', async () => {
    const userEvents = user();
    const { rerender } = render(<Harness initialDirty />);
    await openIt(userEvents);
    rerender(<Harness initialDirty busy />);
    expect(screen.getByRole('dialog').getAttribute('aria-busy')).toBe('true');
    await userEvents.keyboard('{Escape}');
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: strings.closeDialog })).toBeNull();
  });

  it('ignores a backdrop click while busy', async () => {
    const userEvents = user();
    const { rerender } = render(<Harness initialDirty />);
    await openIt(userEvents);
    rerender(<Harness initialDirty busy />);
    const backdrop = document.querySelector('.modal__backdrop') as HTMLElement;
    fireEvent.pointerDown(backdrop);
    fireEvent.pointerUp(backdrop);
    fireEvent.click(backdrop);
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('Discard changes does nothing when busy flips true while the guard shows', async () => {
    const userEvents = user();
    const { rerender } = render(<Harness initialDirty />);
    await openIt(userEvents);
    await userEvents.keyboard('{Escape}');
    rerender(<Harness initialDirty busy />);
    const discard = screen.getByRole('button', { name: strings.discardChanges });
    expect(discard.getAttribute('aria-disabled')).toBe('true');
    await userEvents.click(discard);
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toBe(strings.discardChangesMessage);
  });

  it('a second hashchange on a dirty form equals Keep editing', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    await typeIt(userEvents);
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(screen.getByRole('alert')).toBeTruthy();
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByRole('dialog')).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText('name'));
  });

  it('passes ownerRow data attributes through to the dialog', async () => {
    const userEvents = user();
    render(<Harness ownerRow={{ attribute: 'data-destination-id', id: 'd1' }} />);
    await openIt(userEvents);
    expect(screen.getByRole('dialog').getAttribute('data-owner-row-attribute')).toBe(
      'data-destination-id',
    );
    expect(screen.getByRole('dialog').getAttribute('data-owner-row-id')).toBe('d1');
  });

  it('returns focus to the opener on close and restores page locks', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    expect(document.body.style.overflow).toBe('hidden');
    expect(document.getElementById('root')?.hasAttribute('inert')).toBe(true);
    await userEvents.keyboard('{Escape}');
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'opener' }));
    expect(document.body.style.overflow).toBe('');
    expect(document.getElementById('root')?.hasAttribute('inert')).toBe(false);
  });

  it('skips the focus return with restoreFocus=false', async () => {
    const userEvents = user();
    render(<Harness restoreFocus={false} />);
    await openIt(userEvents);
    await userEvents.keyboard('{Escape}');
    expect(document.activeElement).not.toBe(screen.getByRole('button', { name: 'opener' }));
  });

  it('closes on hashchange for a clean form without throwing', async () => {
    const userEvents = user();
    render(<Harness />);
    await openIt(userEvents);
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect([document.body, screen.getByRole('button', { name: 'opener' })]).toContain(
      document.activeElement,
    );
  });

  describe('confirmStep', () => {
    const openStep = async (userEvents: ReturnType<typeof userEvent.setup>) => {
      await openIt(userEvents);
      const rotate = screen.getByRole('button', { name: 'rotate' });
      rotate.focus();
      await userEvents.click(rotate);
      return rotate;
    };

    it('renders in the dialog, focuses Cancel and keeps the form mounted', async () => {
      const userEvents = user();
      render(<Harness withStep />);
      await typeInto(userEvents);
      await openStepAfterTyping(userEvents);
      expect(document.querySelectorAll('[role="dialog"]').length).toBe(1);
      expect(screen.getByRole('alert').textContent).toBe('Really rotate?');
      const cancel = screen.getByRole('button', { name: 'Not now' });
      expect(document.activeElement).toBe(cancel);
      const describedBy = cancel.getAttribute('aria-describedby') as string;
      expect(document.getElementById(describedBy)?.textContent).toBe('Really rotate?');
      expect((screen.getByLabelText('name') as HTMLInputElement).value).toBe('x');
      await userEvents.click(cancel);
      expect((screen.getByLabelText('name') as HTMLInputElement).value).toBe('x');
    });

    it('Confirm calls onConfirm and restores focus to the originating control', async () => {
      const userEvents = user();
      const onConfirm = vi.fn();
      render(<Harness withStep onConfirm={onConfirm} />);
      const rotate = await openStep(userEvents);
      await userEvents.click(screen.getByRole('button', { name: 'Rotate now' }));
      expect(onConfirm).toHaveBeenCalledOnce();
      expect(screen.queryByRole('alert')).toBeNull();
      expect(document.activeElement).toBe(rotate);
    });

    it('Cancel and Escape restore focus to the originating control', async () => {
      const userEvents = user();
      render(<Harness withStep />);
      const rotate = await openStep(userEvents);
      await userEvents.click(screen.getByRole('button', { name: 'Not now' }));
      expect(document.activeElement).toBe(rotate);
      await userEvents.click(rotate);
      await userEvents.keyboard('{Escape}');
      expect(screen.queryByRole('alert')).toBeNull();
      expect(document.activeElement).toBe(rotate);
    });

    it('focuses the rowGone alert after Cancel when rowGone is still true', async () => {
      const userEvents = user();
      const { rerender } = render(<Harness withStep />);
      await openStep(userEvents);
      rerender(<Harness withStep rowGone />);
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Not now' }));
      await userEvents.click(screen.getByRole('button', { name: 'Not now' }));
      expect(document.activeElement).toBe(screen.getByText(strings.editRowGone));
    });

    it('Confirm and Cancel do nothing while busy', async () => {
      const userEvents = user();
      const onConfirm = vi.fn();
      const { rerender } = render(<Harness withStep onConfirm={onConfirm} />);
      await openStep(userEvents);
      rerender(<Harness withStep onConfirm={onConfirm} busy />);
      const confirm = screen.getByRole('button', { name: 'Rotate now' });
      expect(confirm.getAttribute('aria-disabled')).toBe('true');
      await userEvents.click(confirm);
      await userEvents.click(screen.getByRole('button', { name: 'Not now' }));
      expect(onConfirm).not.toHaveBeenCalled();
      expect(screen.getByRole('alert').textContent).toBe('Really rotate?');
    });

    it('hashchange cancels the step instead of closing', async () => {
      const userEvents = user();
      render(<Harness withStep />);
      await openStep(userEvents);
      act(() => {
        window.dispatchEvent(new HashChangeEvent('hashchange'));
      });
      expect(screen.queryByRole('alert')).toBeNull();
      expect(screen.getByRole('dialog')).toBeTruthy();
    });

    it('wins over the discard guard and over rowGone', async () => {
      const userEvents = user();
      const { rerender } = render(<Harness withStep initialDirty />);
      await openStep(userEvents);
      rerender(<Harness withStep initialDirty rowGone />);
      expect(screen.getAllByRole('alert').map((node) => node.textContent)).toContain(
        'Really rotate?',
      );
      expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Not now' }));
    });
  });

  describe('rowGone', () => {
    it('shows the notice outside the inert wrapper, focuses it and offers Close', async () => {
      const userEvents = user();
      const { rerender } = render(<Harness />);
      await openIt(userEvents);
      rerender(<Harness rowGone />);
      const notice = screen.getByText(strings.editRowGone);
      expect(notice.closest('[inert]')).toBeNull();
      expect(document.activeElement).toBe(notice);
      expect(screen.getByLabelText('name').closest('[inert]')).not.toBeNull();
      expect(screen.getByLabelText('name').matches(':disabled')).toBe(true);
      await userEvents.click(screen.getByRole('button', { name: strings.closeAction }));
      expect(screen.queryByRole('dialog')).toBeNull();
    });

    it('focuses the alert when the dialog opens with the row already gone', async () => {
      const userEvents = user();
      render(<Harness rowGone />);
      await openIt(userEvents);
      expect(document.activeElement).toBe(screen.getByText(strings.editRowGone));
    });

    it('still routes Close through the guard when dirty', async () => {
      const userEvents = user();
      const { rerender } = render(<Harness />);
      await openIt(userEvents);
      await typeIt(userEvents);
      rerender(<Harness rowGone />);
      await userEvents.click(screen.getByRole('button', { name: strings.closeAction }));
      expect(screen.getByRole('button', { name: strings.keepEditing })).toBeTruthy();
    });

    it('does not steal focus from the guard and takes focus after Keep editing', async () => {
      const userEvents = user();
      const { rerender } = render(<Harness />);
      await openIt(userEvents);
      await typeIt(userEvents);
      await userEvents.keyboard('{Escape}');
      rerender(<Harness rowGone />);
      const keep = screen.getByRole('button', { name: strings.keepEditing });
      expect(document.activeElement).toBe(keep);
      await userEvents.click(keep);
      expect(document.activeElement).toBe(screen.getByText(strings.editRowGone));
    });

    it('keeps Tab inside the dialog and off disabled fields', async () => {
      const userEvents = user();
      const { rerender } = render(<Harness />);
      await openIt(userEvents);
      rerender(<Harness rowGone />);
      const dialogNode = screen.getByRole('dialog');
      for (let index = 0; index < 4; index += 1) {
        await userEvents.tab();
        expect(dialogNode.contains(document.activeElement)).toBe(true);
        expect(document.activeElement).not.toBe(screen.getByLabelText('name'));
      }
      await userEvents.tab({ shift: true });
      expect(dialogNode.contains(document.activeElement)).toBe(true);
    });
  });
});

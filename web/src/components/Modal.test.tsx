import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { Modal } from './Modal';
import type { ModalProps } from './Modal';

type HarnessProps = Partial<Omit<ModalProps, 'open' | 'children' | 'actions'>> & {
  withRoot?: boolean;
  body?: ReactNode;
  initialOpen?: boolean;
  onOuterKeyDown?: () => void;
  useInitialRef?: boolean;
  useFallbackRef?: boolean;
  openerHidden?: boolean;
};

function Harness({
  withRoot = true,
  body,
  initialOpen = false,
  onOuterKeyDown,
  useInitialRef = false,
  useFallbackRef = false,
  openerHidden = false,
  onDismiss,
  ...modalProps
}: HarnessProps) {
  const [open, setOpen] = useState(initialOpen);
  const secondRef = useRef<HTMLButtonElement | null>(null);
  const fallbackRef = useRef<HTMLButtonElement | null>(null);
  const dismiss = () => {
    onDismiss?.();
    setOpen(false);
  };
  const content = (
    <div onKeyDown={onOuterKeyDown}>
      <div hidden={openerHidden}>
        <button type="button" onClick={() => setOpen(true)}>
          opener
        </button>
      </div>
      <button type="button" ref={fallbackRef}>
        fallback
      </button>
      <Modal
        title="Delete thing?"
        open={open}
        onDismiss={dismiss}
        initialFocusRef={useInitialRef ? secondRef : undefined}
        fallbackFocusRef={useFallbackRef ? fallbackRef : undefined}
        actions={
          <>
            <button type="button">first action</button>
            <button type="button" ref={secondRef}>
              second action
            </button>
          </>
        }
        {...modalProps}
      >
        {body ?? <p data-testid="body">Body text</p>}
      </Modal>
    </div>
  );
  return withRoot ? <div id="root">{content}</div> : content;
}

const dialog = () => screen.getByRole('dialog');
const openDialog = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(screen.getByRole('button', { name: 'opener' }));
};

beforeEach(() => {
  document.body.style.overflow = '';
});

afterEach(() => {
  cleanup();
  document.body.style.overflow = '';
});

describe('Modal', () => {
  it('renders nothing when closed', () => {
    render(<Harness />);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('renders one modal dialog named by title and described by body', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await openDialog(user);
    expect(screen.getAllByRole('dialog')).toHaveLength(1);
    expect(dialog().getAttribute('aria-modal')).toBe('true');
    expect(screen.getByRole('dialog', { name: 'Delete thing?' })).toBeTruthy();
    expect(dialog().getAttribute('aria-describedby')).toBe(
      screen.getByTestId('body').parentElement?.id,
    );
  });

  it('portals into document.body, outside the app root', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await openDialog(user);
    expect(document.getElementById('root')?.contains(dialog())).toBe(false);
  });

  it('names the X with the closeDialog string and dismisses on click', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness onDismiss={onDismiss} />);
    await openDialog(user);
    await user.click(screen.getByRole('button', { name: strings.closeDialog }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('dismisses once on Escape without reaching an outer onKeyDown', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    const onOuterKeyDown = vi.fn();
    render(<Harness onDismiss={onDismiss} onOuterKeyDown={onOuterKeyDown} />);
    await openDialog(user);
    onOuterKeyDown.mockClear();
    await user.keyboard('{Escape}');
    expect(onDismiss).toHaveBeenCalledTimes(1);
    expect(onOuterKeyDown).not.toHaveBeenCalled();
  });

  it('ignores Escape, backdrop and hides the X when not dismissible', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness dismissible={false} onDismiss={onDismiss} />);
    await openDialog(user);
    expect(screen.queryByRole('button', { name: strings.closeDialog })).toBeNull();
    await user.keyboard('{Escape}');
    const backdrop = dialog().parentElement as HTMLElement;
    await user.click(backdrop);
    expect(onDismiss).not.toHaveBeenCalled();
    expect(screen.getByRole('dialog')).toBeTruthy();
  });

  it('dismisses on a backdrop click', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness onDismiss={onDismiss} />);
    await openDialog(user);
    await user.click(dialog().parentElement as HTMLElement);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('does not dismiss when a press starts in the card and releases on the backdrop', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness onDismiss={onDismiss} />);
    await openDialog(user);
    const backdrop = dialog().parentElement as HTMLElement;
    fireEvent.pointerDown(screen.getByTestId('body'));
    fireEvent.click(backdrop);
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it('does not dismiss when a press starts on the backdrop and releases in the card', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness onDismiss={onDismiss} />);
    await openDialog(user);
    const backdrop = dialog().parentElement as HTMLElement;
    fireEvent.pointerDown(backdrop);
    fireEvent.pointerUp(screen.getByTestId('body'));
    fireEvent.click(backdrop);
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it('dismisses when pointerdown, pointerup and click are all on the backdrop', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness onDismiss={onDismiss} />);
    await openDialog(user);
    const backdrop = dialog().parentElement as HTMLElement;
    fireEvent.pointerDown(backdrop);
    fireEvent.pointerUp(backdrop);
    fireEvent.click(backdrop);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('prevents default on backdrop mousedown but not on card mousedown', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await openDialog(user);
    expect(fireEvent.mouseDown(dialog().parentElement as HTMLElement)).toBe(false);
    expect(fireEvent.mouseDown(screen.getByTestId('body'))).toBe(true);
  });

  it('Escape is stopped and default-prevented, also when not dismissible', async () => {
    const user = userEvent.setup();
    const onOuterKeyDown = vi.fn();
    render(<Harness dismissible={false} onOuterKeyDown={onOuterKeyDown} />);
    await openDialog(user);
    onOuterKeyDown.mockClear();
    expect(fireEvent.keyDown(dialog(), { key: 'Escape' })).toBe(false);
    expect(onOuterKeyDown).not.toHaveBeenCalled();
  });

  it('still dismisses on Escape after the focused action unmounts', () => {
    const onDismiss = vi.fn();
    const { rerender } = render(
      <Modal title="t" open onDismiss={onDismiss} actions={<button type="button">a</button>}>
        body
      </Modal>,
    );
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'a' }));
    rerender(
      <Modal title="t" open onDismiss={onDismiss} actions={<span>busy</span>}>
        body
      </Modal>,
    );
    expect(document.activeElement).toBe(document.body);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('does not dismiss on Escape outside the dialog when not dismissible', () => {
    const onDismiss = vi.fn();
    render(
      <Modal
        title="t"
        open
        dismissible={false}
        onDismiss={onDismiss}
        actions={<button type="button">a</button>}
      >
        body
      </Modal>,
    );
    expect(fireEvent.keyDown(document.body, { key: 'Escape' })).toBe(false);
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it('pulls Tab back into the dialog when focus is outside it', () => {
    render(
      <Modal title="t" open onDismiss={() => {}} actions={<button type="button">a</button>}>
        body
      </Modal>,
    );
    act(() => (document.activeElement as HTMLElement).blur());
    expect(document.activeElement).toBe(document.body);
    expect(fireEvent.keyDown(document.body, { key: 'Tab' })).toBe(false);
    expect(document.activeElement).toBe(screen.getByRole('button', { name: strings.closeDialog }));
  });

  it('skips hidden inputs and hidden regions when wrapping Tab', () => {
    render(
      <Modal
        title="t"
        open
        onDismiss={() => {}}
        actions={
          <>
            <button type="button">last real</button>
            <input type="hidden" name="h" />
            <div hidden>
              <button type="button">in hidden</button>
            </div>
          </>
        }
      >
        body
      </Modal>,
    );
    const lastReal = screen.getByRole('button', { name: 'last real' });
    act(() => lastReal.focus());
    fireEvent.keyDown(lastReal, { key: 'Tab' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: strings.closeDialog }));
    act(() => screen.getByRole('button', { name: strings.closeDialog }).focus());
    fireEvent.keyDown(document.activeElement as HTMLElement, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(lastReal);
  });

  it('skips controls inside an inert wrapper when wrapping Tab', () => {
    render(
      <Modal title="t" open onDismiss={() => {}} actions={<button type="button">live last</button>}>
        <div inert>
          <button type="button">inert one</button>
        </div>
      </Modal>,
    );
    const close = screen.getByRole('button', { name: strings.closeDialog });
    const liveLast = screen.getByRole('button', { name: 'live last' });
    act(() => liveLast.focus());
    fireEvent.keyDown(liveLast, { key: 'Tab' });
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(close, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(liveLast);
  });

  it('keeps focus on the dialog when Tab is pressed with nothing focusable', () => {
    render(
      <Modal title="t" open dismissible={false} onDismiss={() => {}} actions={null}>
        body
      </Modal>,
    );
    act(() => dialog().focus());
    expect(fireEvent.keyDown(dialog(), { key: 'Tab' })).toBe(false);
    expect(document.activeElement).toBe(dialog());
  });

  it('opens correctly under StrictMode', async () => {
    const user = userEvent.setup();
    render(
      <StrictMode>
        <Harness />
      </StrictMode>,
    );
    await openDialog(user);
    expect(screen.getAllByRole('dialog')).toHaveLength(1);
    expect(document.getElementById('root')?.hasAttribute('inert')).toBe(true);
    expect(document.body.style.overflow).toBe('hidden');
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'first action' }));
  });

  it('ignores backdrop clicks with backdropDismiss=false but still honours Escape and X', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness backdropDismiss={false} onDismiss={onDismiss} />);
    await openDialog(user);
    await user.click(dialog().parentElement as HTMLElement);
    expect(onDismiss).not.toHaveBeenCalled();
    await user.keyboard('{Escape}');
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('wraps Tab from the last focusable to the first and Shift+Tab back', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await openDialog(user);
    const close = screen.getByRole('button', { name: strings.closeDialog });
    const second = screen.getByRole('button', { name: 'second action' });
    act(() => second.focus());
    await user.tab();
    expect(document.activeElement).toBe(close);
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(second);
  });

  it('focuses the first focusable in the body or actions by default', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await openDialog(user);
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'first action' }));
  });

  it('focuses initialFocusRef when given', async () => {
    const user = userEvent.setup();
    render(<Harness useInitialRef />);
    await openDialog(user);
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'second action' }));
  });

  it('returns focus to the opener on close', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await openDialog(user);
    await user.keyboard('{Escape}');
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'opener' }));
  });

  it('returns focus to the opener on unmount while open', () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();
    const { unmount } = render(
      <Modal title="t" open onDismiss={() => {}} actions={<button type="button">a</button>}>
        body
      </Modal>,
    );
    expect(document.activeElement).not.toBe(opener);
    unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  it('skips a disconnected opener and uses fallbackFocusRef after close', async () => {
    const user = userEvent.setup();
    render(<Harness useFallbackRef />);
    await openDialog(user);
    const opener = screen.getByRole('button', { name: 'opener' });
    opener.remove();
    await user.keyboard('{Escape}');
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'fallback' }));
  });

  it('uses fallbackFocusRef when the opener is inside a hidden panel', async () => {
    const user = userEvent.setup();
    render(<Harness useFallbackRef />);
    await openDialog(user);
    const opener = screen.getByRole('button', { name: 'opener', hidden: true });
    (opener.parentElement as HTMLElement).hidden = true;
    await user.keyboard('{Escape}');
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'fallback', hidden: true }),
    );
  });

  it('does not use fallbackFocusRef when the opener is valid', async () => {
    const user = userEvent.setup();
    render(<Harness useFallbackRef />);
    const fallback = screen.getByRole('button', { name: 'fallback' });
    const fallbackFocus = vi.spyOn(fallback, 'focus');
    await openDialog(user);
    await user.keyboard('{Escape}');
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'opener' }));
    expect(fallbackFocus).not.toHaveBeenCalled();
  });

  it('focuses returnFocusRef instead of the opener', async () => {
    const user = userEvent.setup();
    function Custom() {
      const target = useRef<HTMLButtonElement | null>(null);
      return (
        <>
          <button type="button" ref={target}>
            elsewhere
          </button>
          <Harness returnFocusRef={target} />
        </>
      );
    }
    render(<Custom />);
    await openDialog(user);
    await user.keyboard('{Escape}');
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'elsewhere' }));
  });

  it('uses fallbackFocusRef when the opener is document.body', () => {
    const fallback = document.createElement('button');
    document.body.appendChild(fallback);
    const ref = { current: fallback };
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).toBe(document.body);
    const { unmount } = render(
      <Modal
        title="t"
        open
        onDismiss={() => {}}
        fallbackFocusRef={ref}
        actions={<button type="button">a</button>}
      >
        body
      </Modal>,
    );
    unmount();
    expect(document.activeElement).toBe(fallback);
    fallback.remove();
  });

  it('removes inert from #root before moving focus to the opener', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const opener = screen.getByRole('button', { name: 'opener' });
    const root = document.getElementById('root') as HTMLElement;
    const seen: { inert: boolean | null } = { inert: null };
    const realFocus = opener.focus.bind(opener);
    vi.spyOn(opener, 'focus').mockImplementation((options) => {
      seen.inert = root.hasAttribute('inert');
      realFocus(options);
    });
    await user.click(opener);
    expect(root.hasAttribute('inert')).toBe(true);
    seen.inert = null;
    await user.keyboard('{Escape}');
    expect(seen.inert).toBe(false);
  });

  it('removes inert from #root before moving focus to the fallback', async () => {
    const user = userEvent.setup();
    render(<Harness useFallbackRef />);
    await openDialog(user);
    const root = document.getElementById('root') as HTMLElement;
    const fallback = screen.getByRole('button', { name: 'fallback', hidden: true });
    let inertAtFocus: boolean | null = null;
    const realFocus = fallback.focus.bind(fallback);
    vi.spyOn(fallback, 'focus').mockImplementation((options) => {
      inertAtFocus = root.hasAttribute('inert');
      realFocus(options);
    });
    screen.getByRole('button', { name: 'opener', hidden: true }).remove();
    await user.keyboard('{Escape}');
    expect(inertAtFocus).toBe(false);
  });

  it('focuses .modal when non-focusable content is clicked, and Escape and Tab still work', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness onDismiss={onDismiss} />);
    await openDialog(user);
    expect(dialog().getAttribute('tabindex')).toBe('-1');
    await user.click(screen.getByTestId('body'));
    expect(document.activeElement).toBe(dialog());
    await user.tab();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: strings.closeDialog }));
    act(() => dialog().focus());
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'second action' }));
    act(() => dialog().focus());
    await user.keyboard('{Escape}');
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('restores the previous body overflow', async () => {
    const user = userEvent.setup();
    document.body.style.overflow = 'scroll';
    render(<Harness />);
    await openDialog(user);
    expect(document.body.style.overflow).toBe('hidden');
    await user.keyboard('{Escape}');
    expect(document.body.style.overflow).toBe('scroll');
  });

  it('sets and clears inert on #root', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const root = document.getElementById('root') as HTMLElement;
    expect(root.hasAttribute('inert')).toBe(false);
    await openDialog(user);
    expect(root.hasAttribute('inert')).toBe(true);
    await user.keyboard('{Escape}');
    expect(root.hasAttribute('inert')).toBe(false);
  });

  it('restores overflow and inert and uses the fallback when unmounted while open', () => {
    const root = document.createElement('div');
    root.id = 'root';
    document.body.appendChild(root);
    const fallback = document.createElement('button');
    document.body.appendChild(fallback);
    document.body.style.overflow = 'scroll';
    const { unmount } = render(
      <Modal
        title="t"
        open
        onDismiss={() => {}}
        fallbackFocusRef={{ current: fallback }}
        actions={<button type="button">a</button>}
      >
        body
      </Modal>,
    );
    expect(root.hasAttribute('inert')).toBe(true);
    expect(document.body.style.overflow).toBe('hidden');
    unmount();
    expect(root.hasAttribute('inert')).toBe(false);
    expect(document.body.style.overflow).toBe('scroll');
    expect(document.activeElement).toBe(fallback);
    root.remove();
    fallback.remove();
  });

  it('leaves #root inert when it was already inert before opening', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const root = document.getElementById('root') as HTMLElement;
    root.setAttribute('inert', '');
    // inert blocks user-event clicks, so open through the DOM directly.
    act(() => screen.getByRole('button', { name: 'opener', hidden: true }).click());
    expect(screen.getByRole('dialog')).toBeTruthy();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(root.hasAttribute('inert')).toBe(true);
  });

  it('opens and closes without throwing when there is no #root', async () => {
    const user = userEvent.setup();
    render(<Harness withRoot={false} />);
    await openDialog(user);
    expect(document.getElementById('root')).toBeNull();
    expect(screen.getByRole('dialog')).toBeTruthy();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('dismisses on hashchange, also when not dismissible', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness dismissible={false} onDismiss={onDismiss} />);
    await openDialog(user);
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('ignores hashchange with closeOnHashChange=false', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness closeOnHashChange={false} onDismiss={onDismiss} />);
    await openDialog(user);
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it('removes the hashchange listener after close', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(<Harness onDismiss={onDismiss} />);
    await openDialog(user);
    await user.keyboard('{Escape}');
    onDismiss.mockClear();
    act(() => {
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it('removes the hashchange listener on unmount', () => {
    const onDismiss = vi.fn();
    const { unmount } = render(
      <Modal title="t" open onDismiss={onDismiss} actions={<button type="button">a</button>}>
        body
      </Modal>,
    );
    unmount();
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it('stamps the ownerRow attributes on the dialog', async () => {
    const user = userEvent.setup();
    render(<Harness ownerRow={{ attribute: 'data-message-id', id: 'm1' }} />);
    await openDialog(user);
    expect(dialog().getAttribute('data-owner-row-attribute')).toBe('data-message-id');
    expect(dialog().getAttribute('data-owner-row-id')).toBe('m1');
  });

  it('omits the ownerRow attributes by default', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await openDialog(user);
    expect(dialog().hasAttribute('data-owner-row-attribute')).toBe(false);
  });

  it('adds the wide modifier class only for size="wide"', () => {
    const actions = <button type="button">a</button>;
    const { rerender } = render(
      <Modal title="t" open onDismiss={() => {}} actions={actions}>
        body
      </Modal>,
    );
    expect(dialog().classList.contains('modal--wide')).toBe(false);
    rerender(
      <Modal title="t" open size="wide" onDismiss={() => {}} actions={actions}>
        body
      </Modal>,
    );
    expect(dialog().classList.contains('modal--wide')).toBe(true);
  });

  describe('restoreFocus', () => {
    const actions = <button type="button">a</button>;
    const openers: HTMLElement[] = [];
    afterEach(() => {
      openers.splice(0).forEach((node) => node.remove());
    });
    const setup = () => {
      const opener = document.createElement('button');
      openers.push(opener);
      document.body.appendChild(opener);
      opener.focus();
      const focusSpy = vi.spyOn(opener, 'focus');
      return { opener, focusSpy };
    };

    it('restores focus to the opener on close by default', () => {
      const { focusSpy } = setup();
      const { rerender } = render(
        <Modal title="t" open onDismiss={() => {}} actions={actions}>
          body
        </Modal>,
      );
      rerender(
        <Modal title="t" open={false} onDismiss={() => {}} actions={actions}>
          body
        </Modal>,
      );
      expect(focusSpy).toHaveBeenCalled();
    });

    it('skips the return-focus write when restoreFocus is false', () => {
      const { focusSpy } = setup();
      const { rerender } = render(
        <Modal title="t" open restoreFocus={false} onDismiss={() => {}} actions={actions}>
          body
        </Modal>,
      );
      rerender(
        <Modal title="t" open={false} restoreFocus={false} onDismiss={() => {}} actions={actions}>
          body
        </Modal>,
      );
      expect(focusSpy).not.toHaveBeenCalled();
    });

    it('skips it when restoreFocus flips to false in the rerender that closes', () => {
      const { focusSpy } = setup();
      const { rerender } = render(
        <Modal title="t" open onDismiss={() => {}} actions={actions}>
          body
        </Modal>,
      );
      rerender(
        <Modal title="t" open={false} restoreFocus={false} onDismiss={() => {}} actions={actions}>
          body
        </Modal>,
      );
      expect(focusSpy).not.toHaveBeenCalled();
    });
  });

  it('sets aria-busy only when busy', () => {
    const actions = <button type="button">a</button>;
    const { rerender } = render(
      <Modal title="t" open busy={false} onDismiss={() => {}} actions={actions}>
        body
      </Modal>,
    );
    expect(dialog().hasAttribute('aria-busy')).toBe(false);
    rerender(
      <Modal title="t" open busy onDismiss={() => {}} actions={actions}>
        body
      </Modal>,
    );
    expect(dialog().getAttribute('aria-busy')).toBe('true');
    rerender(
      <Modal title="t" open busy={false} onDismiss={() => {}} actions={actions}>
        body
      </Modal>,
    );
    expect(dialog().hasAttribute('aria-busy')).toBe(false);
  });
});
